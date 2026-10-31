// Package run implements the `cenci run` launcher: it spawns a detached
// tmux window running a coding-agent CLI for a chosen (agent, workflow) pair.
// The (agent, workflow) → command mapping is config-driven with built-in Go
// defaults, so switching Claude↔Codex is a config change, not code. The
// built-in defaults (builtinConfig) are the only source of which (agent,
// workflow) pairs are supported: a file config can add user-defined templates
// but never makes a disabled agent eligible.
package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// WorkflowTemplate is the command-line template for one (agent, workflow) pair.
// Args are appended after the agent command. The tokens {ticket} and {model}
// are substituted at build time.
type WorkflowTemplate struct {
	Args []string `json:"args"`
	// SandboxArgs, when non-empty, replaces Args only when SandboxCommand
	// replaces the host command. It has the same placeholder substitutions.
	// This lets host CLI flags follow a sandbox launcher's passthrough marker.
	SandboxArgs []string `json:"sandboxArgs,omitempty"`

	// adapter is the flow-plugin file, relative to the plugin root, that makes
	// this built-in workflow runnable in its client: skills/<wf>/SKILL.md for
	// Claude and portable skills, skills/<wf>/codex.md for a Codex workflow
	// with a native companion. It is set only by builtinConfig(). Unexported
	// on purpose: encoding/json skips it, and merge() never copies it from a
	// file, so no config can change or invent an adapter (#1237).
	adapter string
}

// portable marks a Codex workflow whose flow skill has no codex.md companion
// and runs from its portable SKILL.md. Used only as `codexWF("<wf>").portable()`
// so the `codexWF("<name>")` call shape that
// flow/tests/dispatch-target-invocable.test.sh parses stays intact.
func (t WorkflowTemplate) portable() WorkflowTemplate {
	t.adapter = strings.TrimSuffix(t.adapter, "codex.md") + "SKILL.md"
	return t
}

// AgentConfig describes how to launch one agent CLI.
type AgentConfig struct {
	// Command is the host executable, e.g. "claude". Multi-token commands
	// (e.g. "cenci open") are split on whitespace at build time.
	Command string `json:"command"`
	// SandboxCommand replaces Command when the sandbox is requested, e.g.
	// "cenci open". A sandbox request against an agent without one (empty or
	// whitespace-only) fails closed instead of falling back to the host command.
	SandboxCommand string `json:"sandboxCommand"`
	// Model, when set, is injected: substituted into any {model} placeholder,
	// otherwise appended as "--model <model>". A specific model value is never
	// hardcoded in the built-ins — it is purely a config/flag concern.
	Model string `json:"model"`
	// Workflows maps a workflow name (refine/implement/...) to its template.
	Workflows map[string]WorkflowTemplate `json:"workflows"`
}

// FileConfig is the on-disk config.json schema and the shape of the built-in
// defaults. An optional file is merged over the built-ins.
type FileConfig struct {
	// DefaultAgent is used when neither --agent nor a built-in default applies.
	DefaultAgent string `json:"defaultAgent"`
	// Sandbox sets the default sandbox choice when --sandbox/--no-sandbox are
	// not passed. Nil means the sandbox is on (#98); false is the explicit
	// host opt-out.
	Sandbox *bool `json:"sandbox"`
	// Agents maps an agent name to its launch config.
	Agents map[string]AgentConfig `json:"agents"`

	// path is the resolved config file location recorded by Load, even when
	// the file does not exist. It is empty for a bare builtinConfig(). Error
	// text and template provenance name it as the place to edit.
	path string
}

// Sentinels for the pre-spawn failure classes BuildCommand and Run report
// (the first three come from BuildCommand, ErrAdapterUnavailable from Run).
// Callers (dispatch, babysit) detect them with errors.Is; none of them ever
// wraps ErrWindowSpawned because nothing has been spawned yet.
var (
	// ErrAgentDisabled marks a launch of a built-in-disabled agent.
	ErrAgentDisabled = errors.New("agent disabled")
	// ErrMissingTemplate marks an unknown agent or an agent without the
	// requested workflow template.
	ErrMissingTemplate = errors.New("missing launch template")
	// ErrSandboxUnavailable marks a sandbox request the agent cannot satisfy
	// because it has no usable sandboxCommand.
	ErrSandboxUnavailable = errors.New("sandbox unavailable")
	// ErrAdapterUnavailable marks a host (sandbox off) Codex launch of a
	// built-in workflow refused before spawn because the installed cenci@cenci
	// Codex plugin is missing, disabled, lacks the workflow's declared adapter,
	// or the probe could not tell (#1237). It is raised by Run, not
	// BuildCommand (which stays a pure resolver), and never wraps
	// ErrWindowSpawned.
	ErrAdapterUnavailable = errors.New("codex adapter unavailable")
)

// DisabledAgent reports whether launches of agent are refused and why. It
// delegates to sandbox.AgentDisabledReason, the one shared disable mechanism, so
// there is no second table. The answer is built-in Go only, never JSON-backed,
// and BuildCommand checks it before any template lookup, so no file config can
// re-enable an agent. The reason never suggests that editing a config file
// bypasses the disable.
func DisabledAgent(agent string) (reason string, disabled bool) {
	reason = sandbox.AgentDisabledReason(agent)
	return reason, reason != ""
}

// disabledError carries the shared disable reason verbatim as its message and
// matches ErrAgentDisabled through errors.Is.
type disabledError struct{ reason string }

func (e disabledError) Error() string { return e.reason }
func (e disabledError) Unwrap() error { return ErrAgentDisabled }

// checkAgentEnabled returns a disabledError for a disabled agent, else nil.
func checkAgentEnabled(agent string) error {
	if reason, disabled := DisabledAgent(agent); disabled {
		return disabledError{reason: reason}
	}
	return nil
}

// Capability is one built-in (agent, workflow) pair.
type Capability struct{ Agent, Workflow string }

// BuiltinCapabilities lists every (agent, workflow) pair of builtinConfig()
// whose agent is not disabled, sorted by agent then workflow. It is derived
// from the built-in templates, so there is no second workflow list to drift.
func BuiltinCapabilities() []Capability {
	cfg := builtinConfig()
	var caps []Capability
	for _, agent := range slices.Sorted(maps.Keys(cfg.Agents)) {
		if _, disabled := DisabledAgent(agent); disabled {
			continue
		}
		for _, wf := range slices.Sorted(maps.Keys(cfg.Agents[agent].Workflows)) {
			caps = append(caps, Capability{Agent: agent, Workflow: wf})
		}
	}
	return caps
}

// Adapter returns the flow-plugin file this capability declares (see
// WorkflowTemplate.adapter), or "" for a pair that is not a built-in capability.
func (c Capability) Adapter() string {
	adapter, _ := BuiltinAdapter(c.Agent, c.Workflow)
	return adapter
}

// BuiltinAdapter returns the adapter declared by the built-in capability
// (agent, workflow). ok is false for a disabled agent, an unknown pair, or a
// user-defined workflow; file config never changes the answer.
func BuiltinAdapter(agent, workflow string) (adapter string, ok bool) {
	if !BuiltinCapability(agent, workflow) {
		return "", false
	}
	adapter = builtinConfig().Agents[agent].Workflows[workflow].adapter
	return adapter, adapter != ""
}

// BuiltinCapability reports whether (agent, workflow) is a built-in template of
// an enabled agent. File config never changes the answer.
func BuiltinCapability(agent, workflow string) bool {
	if _, disabled := DisabledAgent(agent); disabled {
		return false
	}
	_, ok := builtinConfig().Agents[agent].Workflows[workflow]
	return ok
}

// TemplateProvenance labels where the (agent, workflow) template comes from:
// "built-in" when the pair is a built-in capability (even if the file
// overrides its args or command), otherwise "user-defined (<config path>)".
func (fc FileConfig) TemplateProvenance(agent, workflow string) string {
	if BuiltinCapability(agent, workflow) {
		return "built-in"
	}
	return "user-defined (" + fc.configRef() + ")"
}

// configRef names the config file for error text and provenance.
func (fc FileConfig) configRef() string {
	if fc.path == "" {
		return "the config file"
	}
	return fc.path
}

// CapabilityTableMarkdown renders the README capability table from
// BuiltinCapabilities(): one row per workflow, one column per enabled agent,
// a sandbox command row, and one line per disabled agent with its reason.
func CapabilityTableMarkdown() string {
	cfg := builtinConfig()
	var agents []string
	supports := map[Capability]bool{}
	workflowSet := map[string]bool{}
	for _, c := range BuiltinCapabilities() {
		if len(agents) == 0 || agents[len(agents)-1] != c.Agent {
			agents = append(agents, c.Agent)
		}
		supports[c] = true
		workflowSet[c.Workflow] = true
	}
	workflows := slices.Sorted(maps.Keys(workflowSet))

	var b strings.Builder
	b.WriteString("| Workflow | " + strings.Join(agents, " | ") + " |\n")
	b.WriteString("|---|" + strings.Repeat("---|", len(agents)) + "\n")
	for _, wf := range workflows {
		b.WriteString("| `" + wf + "` |")
		for _, agent := range agents {
			cell := "—"
			if supports[Capability{Agent: agent, Workflow: wf}] {
				cell = "yes"
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
	}
	b.WriteString("| sandbox command |")
	for _, agent := range agents {
		b.WriteString(" `" + strings.TrimSpace(cfg.Agents[agent].SandboxCommand) + "` |")
	}
	b.WriteString("\n")
	for _, agent := range slices.Sorted(maps.Keys(cfg.Agents)) {
		if reason, disabled := DisabledAgent(agent); disabled {
			b.WriteString("\n`" + agent + "`: " + reason + "\n")
		}
	}
	return b.String()
}

// builtinConfig returns the zero-config defaults and the only source of the
// supported (agent, workflow) pairs: the claude and codex workflows calling the
// matching cenci skill, with `cenci open` as their sandbox command. Claude's
// sandbox launch is `cenci open -- <prompt>`; Codex's is `cenci open --agent
// codex -- <prompt>` (its SandboxArgs), since `cenci open` defaults to Claude.
// Fresh maps are constructed on each call so callers may mutate the result freely.
func builtinConfig() FileConfig {
	claudeWF := func(wf string) WorkflowTemplate {
		return WorkflowTemplate{
			Args:    []string{"--", "/cenci:" + wf + " {ticket}"},
			adapter: "skills/" + wf + "/SKILL.md",
		}
	}
	// A Codex sandbox launch must name its agent: `cenci open` reads a leading
	// non-flag word as a shortcut and otherwise defaults to Claude, so the
	// prompt rides behind `--agent codex --` as one verbatim passthrough element.
	codexWF := func(wf string) WorkflowTemplate {
		// The prompt never stages Plan mode (#1234): a planning companion checks
		// the active collaboration mode itself and asks the user for `/plan`.
		prompt := "$cenci:" + wf + " {ticket}"
		return WorkflowTemplate{
			Args:        []string{prompt},
			SandboxArgs: []string{"--agent", "codex", "--", prompt},
			adapter:     "skills/" + wf + "/codex.md",
		}
	}
	return FileConfig{
		DefaultAgent: "claude",
		Agents: map[string]AgentConfig{
			"claude": {
				Command:        "claude",
				SandboxCommand: "cenci open",
				Workflows: map[string]WorkflowTemplate{
					"refine":            claudeWF("refine"),
					"implement":         claudeWF("implement"),
					"address-review":    claudeWF("address-review"),
					"babysit":           claudeWF("babysit"),
					"babysit-attention": claudeWF("babysit-attention"),
					"ci-repair":         claudeWF("ci-repair"),
					// Babysit selects Claude Opus explicitly for conflict repair,
					// regardless of the client that originally armed supervision.
					"merge-repair": {
						Args:        []string{"--print", "--", "/cenci:merge-repair {ticket}"},
						SandboxArgs: []string{"--", "--print", "/cenci:merge-repair {ticket}"},
						adapter:     "skills/merge-repair/SKILL.md",
					},
				},
			},
			"codex": {
				Command:        "codex",
				SandboxCommand: "cenci open",
				Workflows: map[string]WorkflowTemplate{
					"configure": codexWF("configure"), "refine": codexWF("refine"),
					"implement": codexWF("implement"),
					"review":    codexWF("review"), "address-review": codexWF("address-review"),
					"refactor": codexWF("refactor"), "sync": codexWF("sync"),
					"maintain": codexWF("maintain"),
					// Babysit has no codex.md: it runs from its portable SKILL.md.
					"babysit": codexWF("babysit").portable(), "babysit-attention": codexWF("babysit-attention"), "ci-repair": codexWF("ci-repair"),
				},
			},
			// OpenCode is disabled (see DisabledAgent). The entry keeps its
			// executable as the hook for re-enablement (#1222) but ships no
			// workflows, so it has no capability.
			"opencode": {Command: "opencode"},
		},
	}
}

// Load returns the built-in config with an optional JSON file merged over it.
// A missing file is not an error (the built-ins stand alone). When path is
// empty the default location is used: $XDG_CONFIG_HOME/cenci/config.json,
// falling back to ~/.config/cenci/config.json.
func Load(path string) (FileConfig, error) {
	base := builtinConfig()
	if path == "" {
		path = defaultConfigPath()
	}
	if path == "" {
		return base, nil
	}
	base.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return base, nil
		}
		return base, fmt.Errorf("reading config %s: %w", path, err)
	}
	var fromFile FileConfig
	if err := json.Unmarshal(data, &fromFile); err != nil {
		return base, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return merge(base, fromFile), nil
}

// DefaultConfigPath returns the default config.json location, or "" if no home
// is known. It is a thin exported wrapper over defaultConfigPath so sibling
// packages (e.g. dispatch) resolve the same path in one place.
func DefaultConfigPath() string { return defaultConfigPath() }

// defaultConfigPath resolves the XDG config path, or "" if no home is known.
func defaultConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cenci", "config.json")
}

// merge overlays over onto base: scalar fields override when set, agents and
// their workflows are merged key-by-key (file entries add or override).
func merge(base, over FileConfig) FileConfig {
	if over.DefaultAgent != "" {
		base.DefaultAgent = over.DefaultAgent
	}
	if over.Sandbox != nil {
		base.Sandbox = over.Sandbox
	}
	if base.Agents == nil {
		base.Agents = map[string]AgentConfig{}
	}
	for name, oa := range over.Agents {
		ba, ok := base.Agents[name]
		if !ok {
			base.Agents[name] = oa
			continue
		}
		if oa.Command != "" {
			ba.Command = oa.Command
		}
		if oa.SandboxCommand != "" {
			ba.SandboxCommand = oa.SandboxCommand
		}
		if oa.Model != "" {
			ba.Model = oa.Model
		}
		if ba.Workflows == nil {
			ba.Workflows = map[string]WorkflowTemplate{}
		}
		// Merge per field, not whole-struct replacement: a file entry that
		// overrides only Args (a common partial override) must not silently
		// drop other built-in fields. Args and SandboxArgs each override
		// when non-empty; an explicit empty array is treated as unset and
		// does not clear the corresponding built-in arguments.
		for wf, wt := range oa.Workflows {
			existing := ba.Workflows[wf]
			if len(wt.Args) > 0 {
				existing.Args = wt.Args
			}
			if len(wt.SandboxArgs) > 0 {
				existing.SandboxArgs = wt.SandboxArgs
			}
			ba.Workflows[wf] = existing
		}
		base.Agents[name] = ba
	}
	return base
}

// BuildCommand resolves the full argv for (agent, workflow). It refuses, before
// anything is spawned, in this order: a disabled agent (ErrAgentDisabled), an
// unknown agent or missing workflow template (ErrMissingTemplate), a sandbox
// request the agent has no usable sandboxCommand for (ErrSandboxUnavailable --
// it never falls back to the host command), and an empty command. When sandbox
// is true, the agent's sandboxCommand replaces the host command. model
// overrides the agent's configured model when non-empty.
func (fc FileConfig) BuildCommand(agent, workflow, ticket, model string, sandbox bool) ([]string, error) {
	if err := checkAgentEnabled(agent); err != nil {
		return nil, err
	}
	ac, ok := fc.Agents[agent]
	tmpl, hasTemplate := ac.Workflows[workflow]
	if !ok || !hasTemplate {
		return nil, fc.missingTemplateError(agent, workflow, ok)
	}

	cmd := ac.Command
	args := tmpl.Args
	if sandbox {
		sandboxCmd := strings.TrimSpace(ac.SandboxCommand)
		if sandboxCmd == "" {
			return nil, fmt.Errorf("%w: agent %q has no agents.%s.sandboxCommand in %s; "+
				"configure agents.%s.sandboxCommand there, or opt out of the sandbox with --no-sandbox "+
				`or "sandbox": false`,
				ErrSandboxUnavailable, agent, agent, fc.configRef(), agent)
		}
		cmd = sandboxCmd
		if len(tmpl.SandboxArgs) > 0 {
			args = tmpl.SandboxArgs
		}
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, fmt.Errorf("agent %q has no command configured", agent)
	}

	if model == "" {
		model = ac.Model
	}

	usedModelPlaceholder := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if strings.Contains(a, "{model}") {
			usedModelPlaceholder = true
		}
		a = strings.ReplaceAll(a, "{ticket}", ticket)
		a = strings.ReplaceAll(a, "{model}", model)
		rest = append(rest, a)
	}

	// The resolved command may be multi-token ("cenci open"): split it into
	// separate argv entries so the shell join downstream never quotes it into
	// a single nonexistent program word.
	argv := strings.Fields(cmd)
	if model != "" && !usedModelPlaceholder {
		argv = append(argv, "--model", model)
	}
	argv = append(argv, rest...)
	return argv, nil
}

// missingTemplateError reports an unknown agent (knownAgent false) or a known
// agent without the workflow, naming the agent, the workflow and the remedy.
// Disabled agents are left out of the hint so it never advertises them.
func (fc FileConfig) missingTemplateError(agent, workflow string, knownAgent bool) error {
	remedy := fmt.Sprintf("define agents.%s.workflows.%s in %s", agent, workflow, fc.configRef())
	if !knownAgent {
		var names []string
		for _, name := range slices.Sorted(maps.Keys(fc.Agents)) {
			if _, disabled := DisabledAgent(name); !disabled {
				names = append(names, name)
			}
		}
		return fmt.Errorf("%w: no %q template for unknown agent %q (configured agents: %s); %s",
			ErrMissingTemplate, workflow, agent, strings.Join(names, ", "), remedy)
	}
	return fmt.Errorf("%w: agent %q has no %q workflow template (available: %s); %s",
		ErrMissingTemplate, agent, workflow,
		strings.Join(slices.Sorted(maps.Keys(fc.Agents[agent].Workflows)), ", "), remedy)
}
