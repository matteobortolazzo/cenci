package main_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeRunConfig writes a `cenci run --config` file and returns its path.
func writeRunConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runDryRun runs `cenci run <args> --dry-run` and returns stdout, stderr and
// the process exit code (0 on success).
func runDryRun(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binaryPath, append([]string{"run"}, append(args, "--dry-run")...)...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("running %s: %v", binaryPath, err)
	}
	return outBuf.String(), errBuf.String(), exitCode
}

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

// TestRunImplementSandboxDryRunStillResolvesSandbox pins the ordinary
// sandbox-resolution path: `--sandbox` on a normal workflow resolves `cenci
// open`.
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

// #1214: dry run labels where the resolved template came from.
func TestRunDryRunLabelsTemplateProvenance(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	override := writeRunConfig(t, `{"agents":{"claude":{"workflows":{"implement":{"args":["--","/overridden {ticket}"]},"file-added":{"args":["--","/added {ticket}"]}}},"acme":{"command":"acme","workflows":{"refine":{"args":["--task","{ticket}"]}}}}}`)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"built-in", []string{"implement", "40", "--session", "demo", "--config", noCfg}, "template: built-in"},
		{"args-only override stays built-in", []string{"implement", "40", "--session", "demo", "--config", override}, "template: built-in"},
		{"file-added workflow", []string{"file-added", "40", "--session", "demo", "--config", override}, "template: user-defined (" + override + ")"},
		{"custom agent", []string{"refine", "40", "--agent", "acme", "--no-sandbox", "--session", "demo", "--config", override}, "template: user-defined (" + override + ")"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runDryRun(t, tc.args...)
			if code != 0 {
				t.Fatalf("dry-run exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			found := false
			for _, line := range strings.Split(stdout, "\n") {
				if strings.TrimSpace(line) == tc.want {
					found = true
				}
			}
			if !found {
				t.Errorf("dry-run output missing line %q:\n%s", tc.want, stdout)
			}
		})
	}
}

// #1214 AC 3: a sandbox request (default, flag, or config) the agent cannot
// satisfy fails the dry run too, with exit 1 and no resolved command.
func TestRunDryRunUnsatisfiedSandboxFailsClosed(t *testing.T) {
	acme := `"agents":{"acme":{"command":"acme","workflows":{"refine":{"args":["--task","{ticket}"]}}}}`
	cases := []struct {
		name   string
		config string
		flags  []string
	}{
		{"default", `{` + acme + `}`, nil},
		{"--sandbox flag", `{` + acme + `}`, []string{"--sandbox"}},
		{"config sandbox true", `{"sandbox":true,` + acme + `}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeRunConfig(t, tc.config)
			args := append([]string{"refine", "40", "--agent", "acme", "--session", "demo", "--config", cfg}, tc.flags...)
			stdout, stderr, code := runDryRun(t, args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, "command:") {
				t.Errorf("a failed dry run must not print a resolved command:\n%s", stdout)
			}
			for _, want := range []string{"acme", "sandboxCommand", "--no-sandbox", `"sandbox": false`} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// #1214 AC 2: a custom agent without the requested workflow fails the dry run
// naming agent, workflow and remedy.
func TestRunDryRunMissingTemplateFails(t *testing.T) {
	cfg := writeRunConfig(t, `{"agents":{"acme":{"command":"acme","workflows":{"refine":{"args":["--task","{ticket}"]}}}}}`)
	stdout, stderr, code := runDryRun(t, "implement", "40", "--agent", "acme", "--no-sandbox", "--session", "demo", "--config", cfg)
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "command:") {
		t.Errorf("a failed dry run must not print a resolved command:\n%s", stdout)
	}
	for _, want := range []string{"acme", "implement", "agents.acme.workflows.implement"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
}

// #1214 AC 5/7: OpenCode stays disabled at the binary boundary -- by flag, by
// config defaultAgent, and even when a config override supplies workflows and a
// sandboxCommand -- and never suggests a config override can bypass it.
func TestRunDryRunDisabledOpenCodeFails(t *testing.T) {
	override := `{"agents":{"opencode":{"command":"opencode","sandboxCommand":"cenci open","workflows":{"implement":{"args":["--auto","--prompt","/cenci:implement {ticket}"]}}}}}`
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cases := []struct {
		name string
		args []string
	}{
		{"--agent opencode", []string{"--agent", "opencode", "--config", noCfg}},
		{"config defaultAgent", []string{"--config", writeRunConfig(t, `{"defaultAgent":"opencode"}`)}},
		{"config override", []string{"--agent", "opencode", "--config", writeRunConfig(t, override)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"implement", "40", "--session", "demo"}, tc.args...)
			stdout, stderr, code := runDryRun(t, args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, "command:") {
				t.Errorf("a failed dry run must not print a resolved command:\n%s", stdout)
			}
			if !strings.Contains(stderr, "temporarily disabled") {
				t.Errorf("stderr missing \"temporarily disabled\":\n%s", stderr)
			}
			for _, banned := range []string{"config.json", "add one"} {
				if strings.Contains(stderr, banned) {
					t.Errorf("stderr must not suggest a config override (%q):\n%s", banned, stderr)
				}
			}
		})
	}
}
