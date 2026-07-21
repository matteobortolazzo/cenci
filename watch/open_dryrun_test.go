package main_test

import (
	"os/exec"
	"strings"
	"testing"
)

// -- cenci open --dry-run (ticket #589) ----------------------------------
//
// `cenci open --dry-run` (and the `cn --dry-run` argv[0] alias) prints the
// exact, redacted docker/podman argv `cenci open` would run -- both the
// detached container-create command and the interactive agent-attach
// command -- followed by the full `cenci audit` Posture breakdown, without
// creating any container, volume, or network. These black-box tests drive
// the real built `cenci` binary as a subprocess against the scripted-runtime
// harness already shared by sandbox_open_test.go (writeScriptedRuntimes,
// openTestEnv, callLogLines, buildArgv0Alias, etc. -- watch/AGENTS.md #493)
// rather than reinventing it.
//
// NOTE (red phase): main.go/open_cmd.go do not yet recognize --dry-run --
// every test below currently observes the stdlib flag package's "flag
// provided but not defined: -dry-run" usage error instead of real dry-run
// behavior, which is the intended red-phase failure.

func TestOpenDryRun_PrintsBothLabeledArgvsAndPostureWithoutMutating(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--dry-run")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open --dry-run: %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "run --name ") {
		t.Errorf("expected the labeled container-create argv, got:\n%s", out)
	}
	if !strings.Contains(out, "exec -it ") {
		t.Errorf("expected the labeled agent-attach argv, got:\n%s", out)
	}
	if !strings.Contains(out, "Mounts:") || !strings.Contains(out, "Boundary weakenings") {
		t.Errorf("expected the full cenci audit Posture breakdown, got:\n%s", out)
	}

	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected --dry-run to make zero container-runtime calls, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestCnDryRun_PrintsBothLabeledArgvsAndPostureWithoutMutating(t *testing.T) {
	binDir := t.TempDir()
	cnPath := buildArgv0Alias(t, binDir, "cn")

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(cnPath, "--dry-run")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cn --dry-run: %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "run --name ") || !strings.Contains(out, "exec -it ") {
		t.Errorf("expected both labeled argvs from the cn alias, got:\n%s", out)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected cn --dry-run to make zero container-runtime calls, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenDryRun_ClaudeHaikuShortcutAndTrailingForwardedArgs(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch", "--dry-run", "--", "--resume")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch --dry-run -- --resume: %v\n%s", err, output)
	}

	out := string(output)
	wantTail := "/opt/cenci-agent/current/node_modules/.bin/claude --dangerously-skip-permissions --model haiku --resume"
	if !strings.Contains(out, wantTail) {
		t.Errorf("expected the attach argv to show the claude+haiku shortcut resolution and trailing --resume, got:\n%s", out)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected --dry-run to make zero container-runtime calls, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenDryRun_DindAndNoDind_Exits2(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--dry-run", "--dind", "--no-dind")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected --dry-run --dind --no-dind to exit 2, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "cannot be combined") {
		t.Errorf("expected the --dind/--no-dind usage error, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected no runtime calls for the --dind/--no-dind usage error, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenDryRun_CodexNoAuth_Exits1(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets) // no ~/.codex/auth.json, OPENAI_API_KEY scrubbed

	cmd := exec.Command(binaryPath, "open", "xt", "--dry-run")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected codex-no-auth --dry-run to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "requires Codex auth") {
		t.Errorf("expected the codex auth error, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected no runtime calls for the codex-no-auth --dry-run failure, got:\n%s", strings.Join(lines, "\n"))
	}
}
