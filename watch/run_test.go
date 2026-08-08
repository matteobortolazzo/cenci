package main_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSubcommandRoutes(t *testing.T) {
	// `run` with no workflow is a usage error (exit 2) — but must never be
	// mistaken for an unknown subcommand.
	cmd := exec.Command(binaryPath, "run")
	output, _ := cmd.CombinedOutput()
	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("run must route to the launcher, got:\n%s", output)
	}
}

func TestRunDryRunPrintsCommandAndWindowName(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	// --slug is ignored for a numeric ticket: the window is `<number>-<skill>`.
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "cenci-run", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "40-implement") {
		t.Errorf("expected window name 40-implement, got:\n%s", s)
	}
	// No flag and no config default → the sandbox launcher (#98).
	if !strings.Contains(s, "cenci open") || !strings.Contains(s, "/cenci:implement 40") {
		t.Errorf("expected a cenci open command with the cenci skill, got:\n%s", s)
	}
}

func TestRunDryRunNoSandboxUsesHostCommand(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "cenci-run", "--session", "demo", "--config", noCfg, "--no-sandbox", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("no-sandbox dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if strings.Contains(s, "cenci open") {
		t.Errorf("--no-sandbox must not use the sandbox launcher, got:\n%s", s)
	}
	if !strings.Contains(s, "claude") || !strings.Contains(s, "/cenci:implement 40") {
		t.Errorf("expected host claude command with the cenci skill, got:\n%s", s)
	}
}

func TestRunForwardsUnquotedCustomText(t *testing.T) {
	// Unquoted multi-word context (id + additional context) must all reach the
	// skill argument, not just the first token — even though the numeric window
	// name is skill-only and the context never appears in it.
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "99999999", "focus", "on", "the", "API", "layer",
		"--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("custom-text dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "/cenci:implement 99999999 focus on the API layer") {
		t.Errorf("expected full argument forwarded, got:\n%s", s)
	}
	if !strings.Contains(s, "99999999-implement") {
		t.Errorf("expected window name 99999999-implement, got:\n%s", s)
	}
}

func TestRunDryRunSandboxUsesSandboxCommand(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "refine", "40",
		"--slug", "demo", "--session", "demo", "--config", noCfg, "--sandbox", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "cenci open") {
		t.Errorf("expected a cenci open command, got:\n%s", output)
	}
}

// --- Host-only design guard (#647) ---

// TestRunDesignSandboxDryRunExitsTwoWithHostOnlyHint pins the binary-level
// contract: an explicit --sandbox on the host-only design workflow is a
// usage error, exit 2, with the exact one-line stderr hint from ticket
// #647's Q1 — the guard fires before dry-run resolution (Q2), so this is the
// entire process output.
func TestRunDesignSandboxDryRunExitsTwoWithHostOnlyHint(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "design", "42",
		"--session", "demo", "--config", noCfg, "--sandbox", "--dry-run")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	want := "cenci run: design runs on the host only — the Pencil desktop app is unreachable inside the sandbox; drop --sandbox (or pass --no-sandbox)\n"
	if string(output) != want {
		t.Errorf("output = %q, want exactly %q", output, want)
	}
}

// TestRunDesignDryRunNoSandboxFlagResolvesHost covers the default path (no
// sandbox flags, default config): design resolves the host command and
// exits 0 with no `cenci open` in the printed resolution.
func TestRunDesignDryRunNoSandboxFlagResolvesHost(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "design", "42",
		"--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("design dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if strings.Contains(s, "cenci open") {
		t.Errorf("design must resolve host, not sandbox, got:\n%s", s)
	}
	if !strings.Contains(s, "42-design") || !strings.Contains(s, "/cenci:design 42") {
		t.Errorf("expected the host design command, got:\n%s", s)
	}
}

// TestRunImplementSandboxDryRunStillResolvesSandbox guards the negative
// case: implement is not host-only, so `--sandbox` must still resolve
// `cenci open` exactly as before the host-only guard was introduced.
func TestRunImplementSandboxDryRunStillResolvesSandbox(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "42",
		"--session", "demo", "--config", noCfg, "--sandbox", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("implement sandbox dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "cenci open") {
		t.Errorf("implement --sandbox must still resolve cenci open, got:\n%s", output)
	}
}

// TestRunDirDryRunShowsResolvedDirectory is ticket #975's AC 1: `cenci run
// --dir <path>` must be accepted, and --dry-run must show the resolved
// directory via the existing `command:` line's `cd '<dir>' && ` prefix (no
// new `dir:` output line — auto-adopted answer #1: the prefix already
// carries the resolved directory verbatim and shell-quoted). The path
// contains a space so shellQuote's own contract (single-quote only when a
// word isn't already shell-safe, matching internal/run's shellQuote/isShellSafe
// and TestRunPrependsDir's identical path choice) is what triggers quoting
// here, rather than hard-coding quotes production wouldn't actually emit for
// an all-safe-character path.
func TestRunDirDryRunShowsResolvedDirectory(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "cenci-run", "--session", "demo", "--config", noCfg, "--dir", "/repo/my project", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--dir dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "cd '/repo/my project' && ") {
		t.Errorf("expected the command: line's cd prefix to carry the resolved directory, got:\n%s", s)
	}
}

func TestRunCodexUsesBuiltinWorkflowTemplate(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--agent", "codex", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("codex dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "cenci open") || !strings.Contains(string(output), "$cenci:implement 40") {
		t.Errorf("expected built-in Codex skill invocation, got:\n%s", output)
	}
}
