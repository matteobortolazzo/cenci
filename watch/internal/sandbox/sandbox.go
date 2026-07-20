// Package sandbox holds the shared primitives behind cenci's
// `sandbox`/`open` verbs: the container runtime detection, the one-token
// agent+model shortcut tables, the sandbox container name-prefix convention,
// and the native docker/podman listing/stopping used by `sandbox ls`/`sandbox
// stop`. The launch/build/prune/reap engine lives in the launcher subpackage
// (internal/sandbox/launcher).
//
// The tables and conventions in this package are the SOURCE OF TRUTH for the
// whole product (the cenci-sand bash launcher they were once mirrored from is
// gone). Per docs/cli-conventions.md they are defined exactly once in code
// (here) and documented exactly once (watch/README.md's CLI reference);
// everything else links to those two homes:
//   - runtime detection: ContainerRuntime (podman if present, else docker)
//   - shortcut tables: ClaudeModelShortcuts / CodexModelShortcuts
//   - supported agents: SupportedAgents is the single enumeration that
//     sandboxNamePattern (container name prefixes), IsHomeVolumeName, and
//     IsAgentCLIVolumeName all derive from
package sandbox

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// ClaudeModelShortcuts is the source-of-truth table for the Claude one-token
// shortcuts: ch/cs/co/cf select Claude with the haiku/sonnet/opus/fable
// model alias.
var ClaudeModelShortcuts = map[string]string{
	"ch": "haiku",
	"cs": "sonnet",
	"co": "opus",
	"cf": "fable",
}

// CodexModelShortcuts is the source-of-truth table for the Codex one-token
// shortcuts: xl/xt/xs select Codex with the gpt-5.6-luna/terra/sol model.
var CodexModelShortcuts = map[string]string{
	"xl": "gpt-5.6-luna",
	"xt": "gpt-5.6-terra",
	"xs": "gpt-5.6-sol",
}

// ResolveShortcut returns the agent and model a one-token shortcut (ch/cs/co/cf,
// xl/xt/xs) implies, and whether token matched either table.
func ResolveShortcut(token string) (agent, model string, ok bool) {
	if m, found := ClaudeModelShortcuts[token]; found {
		return "claude", m, true
	}
	if m, found := CodexModelShortcuts[token]; found {
		return "codex", m, true
	}
	return "", "", false
}

// -- sandbox ls / stop: implemented natively in Go against docker/podman ---

// SupportedAgents is the single source-of-truth enumeration of every agent
// cenci's sandbox owns resources for. sandboxNamePattern (container name
// prefixes), IsHomeVolumeName, and IsAgentCLIVolumeName are all derived from
// this slice, so adding a 4th agent here is the only Go-side change needed
// to bring prune and `sandbox ls`/`sandbox stop` in sync (#528).
var SupportedAgents = []string{"claude", "codex", "opencode"}

// init guards SupportedAgents against ever being left empty by a future
// edit. sandboxNamePatternSource/homeVolumePatternSource/
// agentCLIVolumePatternSource all derive their regexes from this slice, and
// an empty slice degenerates each pattern silently: sandboxNamePatternSource
// would collapse to `^()`, matching the start of every string (massive
// over-match in a deletion path used by `sandbox prune` and install.sh's
// full-uninstall), while the home/agent-CLI patterns would collapse to
// matching nothing (prune silently stops working). Fail loudly at process
// start instead.
func init() {
	if len(SupportedAgents) == 0 {
		panic("sandbox: SupportedAgents must not be empty")
	}
}

// sandboxNamePattern matches the claude-cenci-/codex-cenci-/opencode-cenci-
// container name prefixes every sandbox container carries
// (launcher.ComputeScope's CONTAINER_PREFIX is "<agent>-cenci"); prune and
// reap filter on it too. Built from SupportedAgents, preserving the
// `^(...)` capturing group AgentForContainerName relies on.
var sandboxNamePattern = regexp.MustCompile(sandboxNamePatternSource())

func sandboxNamePatternSource() string {
	alternatives := make([]string, len(SupportedAgents))
	for i, agent := range SupportedAgents {
		alternatives[i] = regexp.QuoteMeta(agent) + "-cenci-"
	}
	return `^(` + strings.Join(alternatives, "|") + `)`
}

// homeVolumePattern matches the per-agent home volume names cenci-sand
// creates (VOLUME_NAME="${CONTAINER_PREFIX}-home-..."), built from
// SupportedAgents.
var homeVolumePattern = regexp.MustCompile(homeVolumePatternSource())

func homeVolumePatternSource() string {
	return `^(` + strings.Join(quoteMetaAll(SupportedAgents), "|") + `)-cenci-home-`
}

// agentCLIVolumePattern matches the shared per-agent CLI volume names
// (cenci-agent-cli-<agent>), built from SupportedAgents. $-anchored so
// cenci-agent-cli-<agent>-<extra> is excluded.
var agentCLIVolumePattern = regexp.MustCompile(agentCLIVolumePatternSource())

func agentCLIVolumePatternSource() string {
	return `^cenci-agent-cli-(` + strings.Join(quoteMetaAll(SupportedAgents), "|") + `)$`
}

// quoteMetaAll escapes every agent name with regexp.QuoteMeta before it is
// spliced into a pattern string, so a future agent name containing a regex
// metacharacter can't silently broaden a match.
func quoteMetaAll(agents []string) []string {
	quoted := make([]string, len(agents))
	for i, agent := range agents {
		quoted[i] = regexp.QuoteMeta(agent)
	}
	return quoted
}

// IsSandboxContainerName reports whether name carries one of the sandbox
// container name prefixes (claude-cenci-/codex-cenci-/opencode-cenci-).
// Exported so sibling packages (internal/sandbox/launcher) share the one
// prefix table instead of duplicating the regex.
func IsSandboxContainerName(name string) bool {
	return sandboxNamePattern.MatchString(name)
}

// IsHomeVolumeName reports whether name is a supported agent's per-agent
// home volume (holds copied credentials, config, and session history).
// Exported so internal/sandbox/launcher's prune engine shares the one
// matcher instead of duplicating it, closing the drift #528 fixes.
func IsHomeVolumeName(name string) bool {
	return homeVolumePattern.MatchString(name)
}

// IsAgentCLIVolumeName reports whether name is a supported agent's shared
// agent-CLI volume (global executables; no credentials). Exported so
// internal/sandbox/launcher's prune engine shares the one matcher instead of
// duplicating it, closing the drift #528 fixes.
func IsAgentCLIVolumeName(name string) bool {
	return agentCLIVolumePattern.MatchString(name)
}

// AgentForContainerName derives the agent (claude/codex/opencode) a sandbox container
// belongs to from its name prefix, reusing sandboxNamePattern (the same
// source of truth IsSandboxContainerName matches against) rather than
// re-deriving the prefix table. ok is false when name doesn't carry a
// sandbox container name prefix.
func AgentForContainerName(name string) (agent string, ok bool) {
	m := sandboxNamePattern.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return strings.TrimSuffix(m[1], "-cenci-"), true
}

// ContainerRuntime resolves the preferred container runtime: podman if
// present on PATH, else docker. Returns an error if neither is found.
func ContainerRuntime() (string, error) {
	if _, err := exec.LookPath("podman"); err == nil {
		return "podman", nil
	}
	if _, err := exec.LookPath("docker"); err == nil {
		return "docker", nil
	}
	return "", fmt.Errorf("neither podman nor docker found on PATH")
}

// Container is one row of `sandbox ls` output.
type Container struct {
	Name   string
	Status string
	Image  string
}

// ListContainers lists every claude-cenci-*/codex-cenci-*/opencode-cenci-*
// container (running or stopped) known to runtime.
func ListContainers(runtime string) ([]Container, error) {
	out, err := exec.Command(runtime, "ps", "-a", "--format", "{{.Names}}\t{{.Status}}\t{{.Image}}").Output()
	if err != nil {
		return nil, fmt.Errorf("%s ps: %w", runtime, err)
	}
	return parseContainers(string(out)), nil
}

// parseContainers is the pure parsing/filtering step behind ListContainers,
// split out so it is unit-testable without shelling out.
func parseContainers(raw string) []Container {
	var containers []Container
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		name := parts[0]
		if !sandboxNamePattern.MatchString(name) {
			continue
		}
		containers = append(containers, Container{Name: name, Status: parts[1], Image: parts[2]})
	}
	return containers
}

// RunningSandboxContainers lists the names of running
// claude-cenci-*/codex-cenci-*/opencode-cenci-* containers, optionally
// narrowed to names containing filter (a plain substring match against the
// full container name, e.g. a repo slug).
func RunningSandboxContainers(runtime, filter string) ([]string, error) {
	out, err := exec.Command(runtime, "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil, fmt.Errorf("%s ps: %w", runtime, err)
	}
	return parseNames(string(out), filter), nil
}

// parseNames is the pure parsing/filtering step behind RunningSandboxContainers.
func parseNames(raw, filter string) []string {
	var names []string
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		name := scanner.Text()
		if name == "" || !sandboxNamePattern.MatchString(name) {
			continue
		}
		if filter != "" && !strings.Contains(name, filter) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// StopContainers stops each named container in turn via `<runtime> stop
// <name>`, wiring stdout/stderr through so any runtime warnings surface.
// Returns on the first failure.
func StopContainers(runtime string, names []string, stdout, stderr io.Writer) error {
	for _, name := range names {
		cmd := exec.Command(runtime, "stop", name)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s stop %s: %w", runtime, name, err)
		}
	}
	return nil
}
