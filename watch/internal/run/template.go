// Package run implements the `agentwatch run` launcher: it spawns a detached
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
}

// AgentConfig describes how to launch one agent CLI.
type AgentConfig struct {
	// Command is the host executable, e.g. "claude".
	Command string `json:"command"`
	// SandboxCommand replaces Command when --sandbox is on, e.g. "agent-sand".
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
// calling the matching agentflow skill, with a agent-sand sandbox command. Fresh
// maps are constructed on each call so callers may mutate the result freely.
func builtinConfig() FileConfig {
	claudeWF := func(wf string) WorkflowTemplate {
		return WorkflowTemplate{Args: []string{"--", "/agentflow:" + wf + " {ticket}"}}
	}
	return FileConfig{
		DefaultAgent: "claude",
		Agents: map[string]AgentConfig{
			"claude": {
				Command:        "claude",
				SandboxCommand: "agent-sand",
				Workflows: map[string]WorkflowTemplate{
					"refine":    claudeWF("refine"),
					"design":    claudeWF("design"),
					"implement": claudeWF("implement"),
				},
			},
		},
	}
}

// Load returns the built-in config with an optional JSON file merged over it.
// A missing file is not an error (the built-ins stand alone). When path is
// empty the default location is used: $XDG_CONFIG_HOME/agentwatch/config.json,
// falling back to ~/.config/agentwatch/config.json.
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
	return filepath.Join(dir, "agentwatch", "config.json")
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
		for wf, wt := range oa.Workflows {
			ba.Workflows[wf] = wt
		}
		base.Agents[name] = ba
	}
	return base
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
	rest := make([]string, 0, len(tmpl.Args))
	for _, a := range tmpl.Args {
		if strings.Contains(a, "{model}") {
			usedModelPlaceholder = true
		}
		a = strings.ReplaceAll(a, "{ticket}", ticket)
		a = strings.ReplaceAll(a, "{model}", model)
		rest = append(rest, a)
	}

	argv := []string{cmd}
	if model != "" && !usedModelPlaceholder {
		argv = append(argv, "--model", model)
	}
	argv = append(argv, rest...)
	return argv, nil
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
