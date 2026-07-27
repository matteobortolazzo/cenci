// Package run implements the `cenci run` launcher: it spawns a detached
// tmux window running a coding-agent CLI for a chosen (agent, workflow) pair.
// The (agent, workflow) → command mapping is config-driven with built-in Go
// defaults, so switching Claude↔Codex (or adding opencode later) is a config
// change, not code.
package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkflowTemplate is the command-line template for one (agent, workflow) pair.
// Args are appended after the agent command. The tokens {ticket} and {model}
// are substituted at build time.
type WorkflowTemplate struct {
	Args []string `json:"args"`
	// Host, when true, pins this workflow to the host launch regardless of
	// the resolved sandbox default: an explicit --sandbox is then a usage
	// error (see run.ErrHostOnlyWorkflow) rather than a silent override. It
	// is a pointer, mirroring FileConfig.Sandbox, so "unset" is
	// distinguishable from an explicit false and a config.json can opt back
	// into sandbox dispatch. design is the only built-in host-only workflow
	// today (the Pencil desktop app it drives is never reachable inside the
	// sandbox); if a second host-only workflow is added, the host-only error
	// message must become reason-carrying instead of Pencil-specific.
	Host *bool `json:"host"`
}

// AgentConfig describes how to launch one agent CLI.
type AgentConfig struct {
	// Command is the host executable, e.g. "claude". Multi-token commands
	// (e.g. "cenci open") are split on whitespace at build time.
	Command string `json:"command"`
	// SandboxCommand replaces Command when --sandbox is on, e.g. "cenci open".
	SandboxCommand string `json:"sandboxCommand"`
	// Model, when set, is injected: substituted into any {model} placeholder,
	// otherwise appended as "--model <model>". A specific model value is never
	// hardcoded in the built-ins — it is purely a config/flag concern.
	Model string `json:"model"`
	// Workflows maps a workflow name (refine/design/implement/...) to its template.
	Workflows map[string]WorkflowTemplate `json:"workflows"`
}

// FileConfig is the on-disk config.json schema and the shape of the built-in
// defaults. An optional file is merged over the built-ins.
type FileConfig struct {
	// DefaultAgent is used when neither --agent nor a built-in default applies.
	DefaultAgent string `json:"defaultAgent"`
	// Sandbox sets the default sandbox choice when --sandbox/--no-sandbox are
	// not passed. Nil means host (off).
	Sandbox *bool `json:"sandbox"`
	// Agents maps an agent name to its launch config.
	Agents map[string]AgentConfig `json:"agents"`
}

// builtinConfig returns the zero-config defaults: claude refine/design/implement
// calling the matching cenci skill, with `cenci open` as the sandbox command.
// Fresh maps are constructed on each call so callers may mutate the result freely.
func builtinConfig() FileConfig {
	claudeWF := func(wf string) WorkflowTemplate {
		return WorkflowTemplate{Args: []string{"--", "/cenci:" + wf + " {ticket}"}}
	}
	codexWF := func(wf string) WorkflowTemplate {
		return WorkflowTemplate{Args: []string{"{codex_stage}$cenci:" + wf + " {ticket}"}}
	}
	// hostDesignWF builds a design template pinned to the host: a fresh *bool
	// per call so mutating one agent's Host pointer can never affect another's.
	hostDesignWF := func(wt WorkflowTemplate) WorkflowTemplate {
		wt.Host = hostPtr(true)
		return wt
	}
	// OpenCode shares Claude's "/cenci:<workflow>" skill-invocation format
	// (unlike Codex's "$cenci:" convention). Per ticket #488 Q&A, dispatch is
	// TUI + --auto in a tmux window (never "opencode run ..."), so the prompt
	// travels via the top-level --prompt flag rather than a "--" argument. No
	// SandboxCommand is configured yet: sandbox-launcher wiring for opencode is
	// deferred to #490, so BuildCommand falls back to the bare host command
	// even when sandbox=true.
	openCodeWF := func(wf string) WorkflowTemplate {
		return WorkflowTemplate{Args: []string{"--auto", "--prompt", "/cenci:" + wf + " {ticket}"}}
	}
	return FileConfig{
		DefaultAgent: "claude",
		Agents: map[string]AgentConfig{
			"claude": {
				Command:        "claude",
				SandboxCommand: "cenci open",
				Workflows: map[string]WorkflowTemplate{
					"refine":            claudeWF("refine"),
					"design":            hostDesignWF(claudeWF("design")),
					"implement":         claudeWF("implement"),
					"address-review":    claudeWF("address-review"),
					"babysit":           claudeWF("babysit"),
					"babysit-attention": claudeWF("babysit-attention"),
					"ci-repair":         claudeWF("ci-repair"),
				},
			},
			"codex": {
				Command:        "codex",
				SandboxCommand: "cenci open",
				Workflows: map[string]WorkflowTemplate{
					"configure": codexWF("configure"), "refine": codexWF("refine"),
					"design": hostDesignWF(codexWF("design")), "implement": codexWF("implement"),
					"review": codexWF("review"), "address-review": codexWF("address-review"),
					"refactor": codexWF("refactor"), "sync": codexWF("sync"),
					"maintain": codexWF("maintain"), "babysit": codexWF("babysit"), "babysit-attention": codexWF("babysit-attention"), "ci-repair": codexWF("ci-repair"),
				},
			},
			"opencode": {
				Command: "opencode",
				Workflows: map[string]WorkflowTemplate{
					"refine":            openCodeWF("refine"),
					"design":            hostDesignWF(openCodeWF("design")),
					"implement":         openCodeWF("implement"),
					"address-review":    openCodeWF("address-review"),
					"babysit":           openCodeWF("babysit"),
					"babysit-attention": openCodeWF("babysit-attention"),
					"ci-repair":         openCodeWF("ci-repair"),
				},
			},
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
		// overrides only Args (a common partial override, e.g. tweaking
		// design's skill invocation) must not silently drop the built-in
		// Host pin. Args overrides when non-empty; Host overrides when
		// non-nil. Deliberate behavior change: an explicit "args": [] in
		// config.json is now treated as unset (it no longer clears the
		// built-in args) since empty args produced an unusable launch
		// anyway.
		for wf, wt := range oa.Workflows {
			existing := ba.Workflows[wf]
			if len(wt.Args) > 0 {
				existing.Args = wt.Args
			}
			if wt.Host != nil {
				existing.Host = wt.Host
			}
			ba.Workflows[wf] = existing
		}
		base.Agents[name] = ba
	}
	return base
}

// hostOnly reports whether workflow is pinned to the host launch for agent,
// per the resolved (built-in + config.json) WorkflowTemplate.Host field.
func (fc FileConfig) hostOnly(agent, workflow string) bool {
	ac, ok := fc.Agents[agent]
	if !ok {
		return false
	}
	wt, ok := ac.Workflows[workflow]
	if !ok {
		return false
	}
	return wt.Host != nil && *wt.Host
}

// BuildCommand resolves the full argv for (agent, workflow). When sandbox is
// true and the agent defines a sandboxCommand, that replaces the host command.
// model overrides the agent's configured model when non-empty. Errors list the
// available agents/workflows so the codex-until-#33 path is self-documenting.
func (fc FileConfig) BuildCommand(agent, workflow, ticket, model string, sandbox bool) ([]string, error) {
	ac, ok := fc.Agents[agent]
	if !ok {
		return nil, fmt.Errorf("no launch template for agent %q (configured agents: %s); add one in config.json",
			agent, sortedKeys(fc.Agents))
	}
	tmpl, ok := ac.Workflows[workflow]
	if !ok {
		return nil, fmt.Errorf("no %q workflow template for agent %q (available: %s); add one in config.json",
			workflow, agent, sortedWorkflowKeys(ac.Workflows))
	}

	cmd := ac.Command
	if sandbox && ac.SandboxCommand != "" {
		cmd = ac.SandboxCommand
	}
	if cmd == "" {
		return nil, fmt.Errorf("agent %q has no command configured", agent)
	}

	if model == "" {
		model = ac.Model
	}

	usedModelPlaceholder := false
	codexStage := ""
	if agent == "codex" && codexPlanningWorkflow(workflow) && !codexApplyTarget(ticket) {
		codexStage = "/plan\n"
	}
	rest := make([]string, 0, len(tmpl.Args))
	for _, a := range tmpl.Args {
		if strings.Contains(a, "{model}") {
			usedModelPlaceholder = true
		}
		a = strings.ReplaceAll(a, "{ticket}", ticket)
		a = strings.ReplaceAll(a, "{codex_stage}", codexStage)
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

// hostPtr returns a fresh *bool holding v, so each call site (e.g. each
// agent's design template) owns an independent pointer.
func hostPtr(v bool) *bool {
	return &v
}

func codexPlanningWorkflow(workflow string) bool {
	switch workflow {
	case "configure", "refine", "implement", "address-review", "refactor", "maintain", "design":
		return true
	}
	return false
}

func codexApplyTarget(ticket string) bool {
	t := strings.TrimSpace(ticket)
	return strings.HasPrefix(t, "apply ") || strings.HasPrefix(t, "resume ") || strings.HasPrefix(t, ".plans/")
}

func sortedKeys(m map[string]AgentConfig) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func sortedWorkflowKeys(m map[string]WorkflowTemplate) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
