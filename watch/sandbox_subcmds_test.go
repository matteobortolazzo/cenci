package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

func TestSandboxPrune_RemovesSupersededTagsAndStoppedContainers(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "prune")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_IMAGES=cenci-sandbox-base:oldtag\ncenci-sandbox-base:latest\ncenci-sandbox-base:"+tag+"\n",
		"FAKE_PS=claude-cenci-old\nunrelated-container\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox prune: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if !anyLineContains(lines, "rmi cenci-sandbox-base:oldtag") {
		t.Errorf("expected the superseded tag to be removed, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "rmi cenci-sandbox-base:latest") || anyLineContains(lines, "rmi cenci-sandbox-base:"+tag) {
		t.Errorf("expected the current and :latest base tags to be kept, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if _, ok := findLineWithPrefix(lines, "rm claude-cenci-old"); !ok {
		t.Errorf("expected the stopped sandbox container to be removed, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "rm unrelated-container") {
		t.Errorf("expected non-sandbox containers to be left alone, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if _, ok := findLineWithPrefix(lines, "image prune -f"); !ok {
		t.Errorf("expected dangling images to be pruned, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "volume") {
		t.Errorf("expected no volume operations without --volumes, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxPruneVolumes_DefaultDeniesRemoval(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "prune", "--volumes")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES=claude-cenci-home-x\ncodex-cenci-home-y\n",
	)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader("n\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox prune --volumes: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "Skipping volume removal.") {
		t.Errorf("expected the default-deny skip message, got:\n%s", output)
	}
	lines := callLogLines(t, callLog)
	if _, ok := findLineWithPrefix(lines, "volume ls"); !ok {
		t.Errorf("expected volumes to be listed, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "volume rm") {
		t.Errorf("expected no volume removal on 'n', got calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxReseedCreds_IsOpenReseedAlias(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "sandbox", "reseed-creds")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox reseed-creds: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(runLine, "-e CENCI_SANDBOX_RESEED_CREDS=1") {
		t.Errorf("expected the reseed env flag in the run argv, got: %s", runLine)
	}
}

// TestSandboxReseedCreds_DindConfigRepo_BothRuntimesPresent_UsesDocker
// reproduces #585: `cenci sandbox reseed-creds` reaches Launch (via the
// open --reseed-creds alias path) just like `cenci open` does, so it must
// build its engine with NewForLaunch (leaving Runtime unresolved) rather than
// the eager, podman-first newEngine() every other batch verb uses. With both
// a docker and a podman fake registered (podman-first would normally win)
// and the repo's .cenci/config.json turning dind on, the eager podman-first
// resolution would lock the runtime to podman before Launch ever computes
// dind, and dindPreflight would then hard-fail with "requires Docker ...
// got \"podman\"" even though Docker is present. The fixed path must
// instead re-resolve docker-preferred once dind is known, succeeding and
// selecting the sysbox-runc runtime.
func TestSandboxReseedCreds_DindConfigRepo_BothRuntimesPresent_UsesDocker(t *testing.T) {
	repoRoot, slug := dindRepoEnv(t, true)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, `FAKE_INFO_RUNTIMES={"sysbox-runc":{},"runc":{}}`)

	cmd := exec.Command(binaryPath, "sandbox", "reseed-creds")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox reseed-creds (dind repo, both runtimes present): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	createLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(createLine, "--runtime=sysbox-runc") {
		t.Errorf("expected dind mode to resolve docker-preferred (--runtime=sysbox-runc), got: %s", createLine)
	}
	wantVolume := "claude-cenci-dind-" + slug + ":/var/lib/docker"
	if !strings.Contains(createLine, wantVolume) {
		t.Errorf("expected the dind storage volume mount %q, got: %s", wantVolume, createLine)
	}
	if !strings.Contains(createLine, "-e CENCI_SANDBOX_RESEED_CREDS=1") {
		t.Errorf("expected the reseed env flag in the run argv, got: %s", createLine)
	}
}

func TestSandboxUnknownFlag_Exits2NoRuntimeCalls(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "build", "--bogus")
	cmd.Env = batchEnv(t, fakeDir, assets)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected the runtime to never be invoked for an unknown flag, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUnknownFlag_OnPrune_Exits2NoRuntimeCalls(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "prune", "--bogus")
	cmd.Env = batchEnv(t, fakeDir, assets)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected the runtime to never be invoked for an unknown flag, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxTrailingPositional_Exits2NoRuntimeCalls(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "build", "extra")
	cmd.Env = batchEnv(t, fakeDir, assets)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected the runtime to never be invoked for a trailing positional, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUnknownSubcommand_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "sandbox", "bogus")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

func TestSandboxNoSubcommand_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "sandbox")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

// -- sandbox ls / stop (native Go against docker/podman) -----------------

const canonPSAllOutput = "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n" +
	"codex-cenci-agentstack\tExited (0) 5 minutes ago\tcenci-sandbox:latest\n" +
	"unrelated-container\tUp 1 hour\tnginx:latest\n"

func TestSandboxLs_ListsMatchingContainersFromFakeDocker(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeFakeDocker(t, dir, "docker", callLog, canonPSAllOutput)

	cmd := exec.Command(binaryPath, "sandbox", "ls")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox ls: %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "claude-cenci-agentstack") || !strings.Contains(out, "codex-cenci-agentstack") {
		t.Errorf("expected both sandbox containers listed, got:\n%s", out)
	}
	if strings.Contains(out, "unrelated-container") {
		t.Errorf("expected non-sandbox container to be filtered out, got:\n%s", out)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	if !strings.Contains(string(calls), "ps -a") {
		t.Errorf("expected a 'ps -a' invocation, got call log:\n%s", calls)
	}
}

func TestSandboxStop_StopsMatchingContainers(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	// Only running containers are relevant to `stop`.
	psRunning := "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n" +
		"unrelated-container\tUp 1 hour\tnginx:latest\n"
	writeFakeDocker(t, dir, "docker", callLog, psRunning)

	cmd := exec.Command(binaryPath, "sandbox", "stop")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox stop: %v\n%s", err, output)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	callsStr := string(calls)
	if !strings.Contains(callsStr, "stop claude-cenci-agentstack") {
		t.Errorf("expected a 'stop claude-cenci-agentstack' invocation, got call log:\n%s", callsStr)
	}
	if strings.Contains(callsStr, "stop unrelated-container") {
		t.Errorf("expected non-sandbox container to never be stopped, got call log:\n%s", callsStr)
	}
}

func TestSandboxStop_WithFilterArg_OnlyStopsMatchingName(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	psRunning := "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n" +
		"codex-cenci-otherrepo\tUp 1 hour\tcenci-sandbox:latest\n"
	writeFakeDocker(t, dir, "docker", callLog, psRunning)

	cmd := exec.Command(binaryPath, "sandbox", "stop", "agentstack")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox stop agentstack: %v\n%s", err, output)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	callsStr := string(calls)
	if !strings.Contains(callsStr, "stop claude-cenci-agentstack") {
		t.Errorf("expected the matching container to be stopped, got call log:\n%s", callsStr)
	}
	if strings.Contains(callsStr, "stop codex-cenci-otherrepo") {
		t.Errorf("expected the non-matching container to be left alone, got call log:\n%s", callsStr)
	}
}
