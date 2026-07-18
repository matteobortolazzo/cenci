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
//   - container name prefixes: the
//     `^(claude-cenci-|codex-cenci-|opencode-cenci-)` pattern behind
//     IsSandboxContainerName
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

// sandboxNamePattern matches the claude-cenci-/codex-cenci-/opencode-cenci-
// container name prefixes every sandbox container carries
// (launcher.ComputeScope's CONTAINER_PREFIX is "<agent>-cenci"); prune and
// reap filter on it too.
var sandboxNamePattern = regexp.MustCompile(`^(claude-cenci-|codex-cenci-|opencode-cenci-)`)

// IsSandboxContainerName reports whether name carries one of the sandbox
// container name prefixes (claude-cenci-/codex-cenci-/opencode-cenci-).
// Exported so sibling packages (internal/sandbox/launcher) share the one
// prefix table instead of duplicating the regex.
func IsSandboxContainerName(name string) bool {
	return sandboxNamePattern.MatchString(name)
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
