// Package sandbox translates cenci's `sandbox`/`open` verbs into
// invocations of the sandbox/cenci-sand bash launcher (batch verbs,
// interactive open) or direct docker/podman calls (ls/stop, which have no
// cenci-sand equivalent and are implemented natively here instead).
//
// The shortcut tables and the container-runtime/name-prefix conventions below
// are mirrored byte-for-byte from sandbox/cenci-sand (there is no shared
// code across the bash/Go boundary) so a desync between the two front doors
// is a review-time concern, not a runtime one. See sandbox/cenci-sand for
// the source of truth each mirrors:
//   - runtime detection: top of the script (podman if present, else docker)
//   - shortcut tables: CLAUDE_MODEL_SHORTCUTS / CODEX_MODEL_SHORTCUTS (~line 65)
//   - recognized long flags: the `case "$1" in ... --*)` block (~line 106)
//   - container name prefixes: CONTAINER_PREFIX="${AGENT}-sand" and the
//     `^(claude-sand-|codex-sand-)` filter used by --prune (~line 199, 262, 387)
package sandbox

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
)

// BinaryName is the cenci-sand executable resolved from PATH.
const BinaryName = "cenci-sand"

// batchFlags maps each `sandbox <verb>` that translates 1:1 to a single
// cenci-sand long flag. `prune` is deliberately excluded: it takes an
// optional --volumes flag, so main.go builds its argv by hand instead of
// going through this table.
var batchFlags = map[string]string{
	"build":          "--build",
	"build-base":     "--build-base",
	"update-plugins": "--update-plugins",
	"reseed-creds":   "--reseed-creds",
	"reap-orphans":   "--reap-orphans",
}

// BatchFlag returns the cenci-sand long flag verb translates to, and whether
// verb is a recognized batch verb.
func BatchFlag(verb string) (string, bool) {
	f, ok := batchFlags[verb]
	return f, ok
}

// ClaudeModelShortcuts mirrors sandbox/cenci-sand's CLAUDE_MODEL_SHORTCUTS
// table exactly: ch/cs/co/cf select Claude with the haiku/sonnet/opus/fable
// model alias.
var ClaudeModelShortcuts = map[string]string{
	"ch": "haiku",
	"cs": "sonnet",
	"co": "opus",
	"cf": "fable",
}

// CodexModelShortcuts mirrors sandbox/cenci-sand's CODEX_MODEL_SHORTCUTS
// table exactly: xl/xt/xs select Codex with the gpt-5.6-luna/terra/sol model.
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

// RunCenciSand resolves cenci-sand from PATH and runs it with argv, wiring
// stdin/stdout/stderr straight through (batch verbs are non-interactive but
// may still print progress or a confirmation prompt, e.g. `--prune
// --volumes`'s y/N). It returns the child's exit code on a normal exit, or a
// non-nil error when the binary can't be resolved or fails to start.
func RunCenciSand(argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	path, err := exec.LookPath(BinaryName)
	if err != nil {
		return 0, fmt.Errorf("%s not found on PATH: %w", BinaryName, err)
	}
	cmd := exec.Command(path, argv...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

// ExecCenciSand resolves cenci-sand from PATH and execs it (replacing the
// current process image) with argv, so the interactive session owns the TTY
// exactly as if the user had invoked cenci-sand directly. It only returns on
// failure (LookPath or syscall.Exec error) — success never returns.
func ExecCenciSand(argv []string) error {
	path, err := exec.LookPath(BinaryName)
	if err != nil {
		return fmt.Errorf("%s not found on PATH: %w", BinaryName, err)
	}
	fullArgv := append([]string{path}, argv...)
	return syscall.Exec(path, fullArgv, os.Environ())
}

// -- sandbox ls / stop: implemented natively in Go against docker/podman ---

// sandboxNamePattern matches the claude-sand-/codex-sand- container name
// prefixes cenci-sand uses (CONTAINER_PREFIX="${AGENT}-sand"), mirroring the
// filter cenci-sand's --prune applies to its container list.
var sandboxNamePattern = regexp.MustCompile(`^(claude-sand-|codex-sand-)`)

// ContainerRuntime resolves the preferred container runtime the same way
// cenci-sand does: podman if present on PATH, else docker. Returns an error
// if neither is found.
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

// ListContainers lists every claude-sand-*/codex-sand-* container (running or
// stopped) known to runtime.
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

// RunningSandboxContainers lists the names of running claude-sand-*/codex-sand-*
// containers, optionally narrowed to names containing filter (a plain
// substring match against the full container name, e.g. a repo slug).
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
