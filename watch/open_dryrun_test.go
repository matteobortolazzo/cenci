package main_test

import (
	"os/exec"
	"strings"
	"testing"
)

// -- cenci open --dry-run (ticket #589, corrected by #620) ----------------
//
// `cenci open --dry-run` (and the `cn --dry-run` argv[0] alias) renders the
// branch a real launch would take -- attach-only, create-then-attach, or the
// incompatible-container hard error -- followed by the full `cenci audit`
// Posture breakdown, without creating any container, volume, network, or
// daemon, and without attaching. These black-box tests drive the real built
// `cenci` binary as a subprocess against the scripted-runtime harness
// already shared by sandbox_open_test.go (writeScriptedRuntimes,
// openTestEnv, callLogLines, buildArgv0Alias, etc. --
// watch/docs/test-strategy.md #493) rather than reinventing it.
//
// Since #620, DryRun performs its own read-only container-disposition
// probing (a `ps` call via the shared planArgvs helper, mirroring a real
// launch's containerRunning check) even in these fixtures' no-container-
// running scenarios, so a bare "zero calls" assertion no longer holds --
// assertOnlyReadOnlyContainerProbeCalls enumerates the still-forbidden
// mutating verbs explicitly (AGENTS #446) rather than loosening to "any
// calls are fine".

// assertOnlyReadOnlyContainerProbeCalls asserts the container-runtime call
// log contains, at most, read-only listing/inspection verbs (the
// containerRunning/containerHasSharedAgentMount disposition probes planArgvs
// now always performs, ticket #620) -- never a mutating verb (run, rm, exec,
// create, volume create, network). Each forbidden verb is enumerated
// explicitly (AGENTS #446/plan Risks) rather than a loosened "some calls are
// fine" check, so a future accidental mutating call in the dry-run path
// can't hide behind it.
func assertOnlyReadOnlyContainerProbeCalls(t *testing.T, lines []string) {
	t.Helper()
	for _, mutatingPrefix := range []string{"run ", "rm ", "exec ", "create ", "volume create", "network"} {
		for _, line := range lines {
			if strings.HasPrefix(line, mutatingPrefix) {
				t.Errorf("expected only read-only ps/inspect container-runtime calls, got mutating call with prefix %q; calls:\n%s", mutatingPrefix, strings.Join(lines, "\n"))
			}
		}
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if verb := fields[0]; verb != "ps" && verb != "inspect" {
			t.Errorf("expected only read-only ps/inspect container-runtime calls, got unexpected call %q; calls:\n%s", line, strings.Join(lines, "\n"))
		}
	}
}

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

	assertOnlyReadOnlyContainerProbeCalls(t, callLogLines(t, callLog))
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
	assertOnlyReadOnlyContainerProbeCalls(t, callLogLines(t, callLog))
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
	assertOnlyReadOnlyContainerProbeCalls(t, callLogLines(t, callLog))
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

// TestOpenDryRun_MalformedDindConfig_Exits1 mirrors the real-launch #632
// hard-fail (TestOpen_MalformedDindConfig_Exits1 in sandbox_open_test.go)
// for --dry-run: a corrupt .cenci/config.json must hard-fail dry-run too,
// not render a plan with dind silently resolved off.
func TestOpenDryRun_MalformedDindConfig_Exits1(t *testing.T) {
	repoRoot := malformedDindRepoEnv(t)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--dry-run")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected --dry-run with malformed .cenci/config.json to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "config.json") {
		t.Errorf("expected a path-bearing error naming config.json, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected no runtime calls once config parsing hard-fails, got:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpenDryRunNoDind_SucceedsDespiteMalformedConfig pins --no-dind as a
// config-free escape hatch for --dry-run too (#632).
func TestOpenDryRunNoDind_SucceedsDespiteMalformedConfig(t *testing.T) {
	repoRoot := malformedDindRepoEnv(t)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--dry-run", "--no-dind")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open --dry-run --no-dind with corrupt config: %v\n%s", err, output)
	}
	assertOnlyReadOnlyContainerProbeCalls(t, callLogLines(t, callLog))
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
	assertOnlyReadOnlyContainerProbeCalls(t, callLogLines(t, callLog))
}
