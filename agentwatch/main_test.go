package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "agentwatch-test-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	binaryPath = filepath.Join(tmp, "agentwatch")

	// Build the binary from the current module root.
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("failed to build binary: " + err.Error())
	}

	os.Exit(m.Run())
}

// TestCodexHooksJSONHasNoUnknownKeys guards against Claude-only keys leaking
// into the Codex hooks file. Codex parses hooks.json with deny_unknown_fields,
// so any extra key would break loading for every Codex user.
func TestCodexHooksJSONHasNoUnknownKeys(t *testing.T) {
	// main_test runs with cwd = package dir (repo root), so this resolves.
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}
	if len(root.Hooks) == 0 {
		t.Fatal("expected at least one event group in codex hooks.json")
	}

	allowedGroupKeys := map[string]bool{"matcher": true, "hooks": true}
	allowedHookKeys := map[string]bool{"type": true, "command": true, "timeout": true, "async": true}

	for event, groups := range root.Hooks {
		for i, group := range groups {
			for key := range group {
				if !allowedGroupKeys[key] {
					t.Errorf("%s[%d]: unexpected group key %q (allowed: matcher, hooks)", event, i, key)
				}
			}
			raw, ok := group["hooks"]
			if !ok {
				t.Errorf("%s[%d]: missing required \"hooks\" key", event, i)
				continue
			}
			var hooks []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &hooks); err != nil {
				t.Errorf("%s[%d]: parse hooks array: %v", event, i, err)
				continue
			}
			for j, hook := range hooks {
				for key := range hook {
					if !allowedHookKeys[key] {
						t.Errorf("%s[%d].hooks[%d]: unexpected hook key %q (allowed: type, command, timeout, async)", event, i, j, key)
					}
				}
			}
		}
	}
}

func TestUnknownSubcommand_ExitCode2(t *testing.T) {
	cmd := exec.Command(binaryPath, "garbage")
	output, err := cmd.CombinedOutput()

	// Expect a non-zero exit.
	if err == nil {
		t.Fatal("expected non-zero exit code for unknown subcommand, got 0")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}

	if exitErr.ExitCode() != 2 {
		t.Errorf("expected exit code 2, got %d", exitErr.ExitCode())
	}

	if !strings.Contains(string(output), `unknown subcommand "garbage"`) {
		t.Errorf("expected stderr to contain %q, got:\n%s",
			`unknown subcommand "garbage"`, output)
	}
}

func TestUnknownSubcommand_ErrorMessageFormat(t *testing.T) {
	cmd := exec.Command(binaryPath, "frobnicate")
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected non-zero exit for unknown subcommand, got 0")
	}

	if !strings.Contains(string(output), `agentwatch: unknown subcommand "frobnicate"`) {
		t.Errorf("expected stderr to contain %q, got:\n%s",
			`agentwatch: unknown subcommand "frobnicate"`, output)
	}
}

func TestFlagRouting_DashV_NotUnknownSubcommand(t *testing.T) {
	// -v should route to daemon, not be treated as unknown subcommand.
	// The daemon will likely fail (no tmux), but should NOT print
	// "unknown subcommand". Use a timeout because the daemon blocks if it starts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "-v")
	output, _ := cmd.CombinedOutput()

	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("expected -v flag NOT to trigger unknown subcommand error, got:\n%s", output)
	}
}

func TestNoArgs_NotUnknownSubcommand(t *testing.T) {
	// No arguments should route to daemon, not unknown subcommand.
	// Use a timeout because the daemon blocks if it starts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath)
	output, _ := cmd.CombinedOutput()

	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("expected no-args NOT to trigger unknown subcommand error, got:\n%s", output)
	}
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
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "agentwatch-run", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "40-agentwatch-run") {
		t.Errorf("expected window name 40-agentwatch-run, got:\n%s", s)
	}
	if !strings.Contains(s, "claude") || !strings.Contains(s, "/ccflow:implement 40") {
		t.Errorf("expected claude command with the ccflow skill, got:\n%s", s)
	}
}

func TestRunForwardsUnquotedCustomText(t *testing.T) {
	// Unquoted multi-word context (id + additional context) must all reach the
	// skill argument, not just the first token.
	// Use a non-existent issue number so the gh title lookup yields nothing and
	// the trailing context deterministically drives the slug.
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "99999999", "focus", "on", "the", "API", "layer",
		"--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("custom-text dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "/ccflow:implement 99999999 focus on the API layer") {
		t.Errorf("expected full argument forwarded, got:\n%s", s)
	}
	if !strings.Contains(s, "99999999-focus-on-the-api-layer") {
		t.Errorf("expected window name 99999999-focus-on-the-api-layer, got:\n%s", s)
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
	if !strings.Contains(string(output), "agent-sand") {
		t.Errorf("expected agent-sand command, got:\n%s", output)
	}
}

func TestRunCodexWithoutTemplateErrors(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--agent", "codex", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit when no codex template exists")
	}
	if !strings.Contains(strings.ToLower(string(output)), "codex") {
		t.Errorf("expected an error mentioning codex, got:\n%s", output)
	}
}

func TestStatusAndWaybarSubcommandsBothRoute(t *testing.T) {
	// Both "status" and its hidden alias "waybar" must route to the status
	// frontend. With no daemon on the socket they exit 1 with no output —
	// never the "unknown subcommand" error.
	for _, sub := range []string{"status", "waybar"} {
		cmd := exec.Command(binaryPath, sub, "-socket", filepath.Join(t.TempDir(), "nope.sock"))
		output, err := cmd.CombinedOutput()

		if strings.Contains(string(output), "unknown subcommand") {
			t.Errorf("%s: expected routing to status frontend, got:\n%s", sub, output)
		}
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: expected exit error (daemon not running), got %T: %v", sub, err, err)
		}
		if exitErr.ExitCode() != 1 {
			t.Errorf("%s: expected exit code 1 when daemon not running, got %d", sub, exitErr.ExitCode())
		}
	}
}
