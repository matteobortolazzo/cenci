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
		verb := fields[0]
		// `info` is the read-only host-memory query the create branch makes to
		// derive the default memory limit (#1216); any other `info` form is
		// still unexpected.
		isMemTotalQuery := verb == "info" && strings.Contains(line, "MemTotal")
		if verb != "ps" && verb != "inspect" && !isMemTotalQuery {
			t.Errorf("expected only read-only ps/inspect/info-MemTotal container-runtime calls, got unexpected call %q; calls:\n%s", line, strings.Join(lines, "\n"))
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

// TestOpenDryRun_CodexPassthroughIsNeverReadAsShortcut pins #1232 A4: the
// built-in Codex sandbox launch is `cenci open [--model m] --agent codex --
// <prompt>`, and everything after `--` is forwarded to the agent verbatim, so a
// prompt identical to a shortcut token (xs, ch) neither selects that shortcut's
// agent or model nor conflicts with --agent codex, and the full built-in prompt
// shape renders as one quoted token.
func TestOpenDryRun_CodexPassthroughIsNeverReadAsShortcut(t *testing.T) {
	const codexAttach = "/opt/cenci-agent/current/node_modules/.bin/codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust"
	for _, tc := range []struct {
		name    string
		args    []string
		wantEnd string
	}{
		{"prompt xs", []string{"--agent", "codex", "--dry-run", "--", "xs"}, " --model gpt-5.6-terra xs"},
		{"prompt ch", []string{"--agent", "codex", "--dry-run", "--", "ch"}, " --model gpt-5.6-terra ch"},
		{"model then prompt xs", []string{"--model", "m", "--agent", "codex", "--dry-run", "--", "xs"}, " --model m xs"},
		{"full prompt", []string{"--agent", "codex", "--dry-run", "--", "$cenci:implement 42 -- xs"}, " --model gpt-5.6-terra '$cenci:implement 42 -- xs'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			env, home, _ := openTestEnv(t, fakeDir, writeAssetFixture(t))
			stageCodexAuth(t, home)

			cmd := exec.Command(binaryPath, append([]string{"open"}, tc.args...)...)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("open %v: %v\n%s", tc.args, err, output)
			}

			found := false
			for _, line := range strings.Split(string(output), "\n") {
				if strings.Contains(line, "exec -it ") && strings.HasSuffix(line, codexAttach+tc.wantEnd) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an attach argv ending %q, got:\n%s", codexAttach+tc.wantEnd, output)
			}
			assertOnlyReadOnlyContainerProbeCalls(t, callLogLines(t, callLog))
		})
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

// TestOpenDryRunNoDind_SucceedsDespiteMalformedConfig pinned --no-dind as a
// config-free escape hatch for --dry-run too pre-#1002 (#632). #1002 narrows
// it the same way as the real-launch case (sandbox_open_test.go's
// TestOpenNoDind_SucceedsDespiteMalformedConfig): resolveLaunchContext (which
// DryRun shares) now ALSO resolves sandbox.plugins unconditionally, so the
// same corrupt config.json --no-dind used to route around now hard-fails the
// plugins read instead.
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

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected --dry-run --no-dind with a malformed .cenci/config.json to exit 1 (the plugins read still hard-fails on it, #1002), got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "config.json") {
		t.Errorf("expected a path-bearing error naming config.json, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected no runtime calls once the plugins config read hard-fails, got:\n%s", strings.Join(lines, "\n"))
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
	assertOnlyReadOnlyContainerProbeCalls(t, callLogLines(t, callLog))
}
