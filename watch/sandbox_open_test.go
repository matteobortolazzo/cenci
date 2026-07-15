package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// -- fakes -------------------------------------------------------------
//
// These black-box tests exercise the real built `cenci` binary as a
// subprocess (binaryPath, built once in TestMain in main_test.go) with PATH
// overridden to a temp dir containing fake `cenci-sand`/`docker`/`podman`
// scripts. Fakes are plain POSIX `/bin/sh` scripts (not `#!/usr/bin/env ...`)
// so they resolve without depending on the overridden PATH containing a
// shell.

// writeFakeCenciSand writes a fake `cenci-sand` to dir that records the argv
// it receives (one arg per line) to captureFile and exits with exitCode.
func writeFakeCenciSand(t *testing.T, dir, captureFile string, exitCode int) {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(captureFile) + "\nexit " + itoa(exitCode) + "\n"
	writeExecutable(t, filepath.Join(dir, "cenci-sand"), body)
}

// writeFakeDocker writes a fake `docker` (or `podman`) to dir that appends
// each invocation's argv (space-joined) as a line to callLog, and — when
// invoked as `<name> ps ...` — prints psOutput to stdout.
func writeFakeDocker(t *testing.T, dir, name, callLog, psOutput string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(callLog) + "\n" +
		"if [ \"$1\" = \"ps\" ]; then printf '%s' " + shellQuote(psOutput) + "; fi\n" +
		"exit 0\n"
	writeExecutable(t, filepath.Join(dir, name), body)
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// shellQuote single-quotes s for embedding in a generated shell script body.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// readCapturedArgv reads a fake-cenci-sand capture file into a []string,
// one element per line (trailing blank line from the final \n dropped).
func readCapturedArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture %s: %v", path, err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func joinArgv(argv []string) string {
	return strings.Join(argv, " ")
}

// -- sandbox <batch verb> ------------------------------------------------
//
// build/build-base/prune/update-plugins run natively against docker/podman
// via internal/sandbox/launcher, so these tests assert what the fake
// *runtime* received, not what cenci-sand received. Both docker and podman
// fakes are always written so the podman-first runtime detection can never
// escape to a real runtime on machines (or CI runners) that have one.
// reap-orphans is pinned by the relocated bash contract suite
// (tests/reap-orphans.test.sh); reseed-creds still forwards to cenci-sand
// until `cenci open --reseed-creds` lands.

// writeScriptedRuntime writes a fake docker/podman to dir that appends each
// invocation's argv (space-joined) as a line to callLog and answers scripted
// responses from env vars:
//
//	FAKE_INSPECT_EXIT — `image inspect` exit code (default 0 = image exists)
//	FAKE_BUILD_EXIT   — `build` exit code (default 0)
//	FAKE_IMAGES       — `images ...` stdout
//	FAKE_PS           — `ps ...` stdout
//	FAKE_VOLUMES      — `volume ls` stdout
func writeScriptedRuntime(t *testing.T, dir, name, callLog string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(callLog) + "\n" +
		"case \"$1\" in\n" +
		"image) if [ \"$2\" = inspect ]; then exit \"${FAKE_INSPECT_EXIT:-0}\"; fi ;;\n" +
		"build) exit \"${FAKE_BUILD_EXIT:-0}\" ;;\n" +
		"images) printf '%s' \"${FAKE_IMAGES:-}\" ;;\n" +
		"ps) printf '%s' \"${FAKE_PS:-}\" ;;\n" +
		"volume) if [ \"$2\" = ls ]; then printf '%s' \"${FAKE_VOLUMES:-}\"; fi ;;\n" +
		"esac\n" +
		"exit 0\n"
	writeExecutable(t, filepath.Join(dir, name), body)
}

// writeScriptedRuntimes writes the same scripted fake as both docker and
// podman and returns the shared call log path.
func writeScriptedRuntimes(t *testing.T, dir string) string {
	t.Helper()
	callLog := filepath.Join(dir, "calls.txt")
	writeScriptedRuntime(t, dir, "docker", callLog)
	writeScriptedRuntime(t, dir, "podman", callLog)
	return callLog
}

// writeAssetFixture creates a minimal sandbox asset dir (Dockerfile.base,
// entrypoint.sh, lib/) for CENCI_SANDBOX_ASSETS.
func writeAssetFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.base"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile.base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM cenci-sandbox-base\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entrypoint.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write entrypoint.sh: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatalf("mkdir lib: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "seed.sh"), []byte("# lib\n"), 0o644); err != nil {
		t.Fatalf("write lib/seed.sh: %v", err)
	}
	return dir
}

// batchEnv is the black-box environment for native batch-verb runs: fake
// runtimes first on PATH (with git/sh still resolvable from the system
// dirs), an isolated HOME, and the asset fixture pinned via
// CENCI_SANDBOX_ASSETS. os/exec keeps the LAST duplicate env key, so these
// appends override the inherited values.
func batchEnv(t *testing.T, fakeDir, assets string) []string {
	t.Helper()
	return append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+t.TempDir(),
		"CENCI_SANDBOX_ASSETS="+assets,
	)
}

// callLogLines reads the shared runtime call log (missing file = no calls).
func callLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read call log: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func findLineWithPrefix(lines []string, prefix string) (string, bool) {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l, true
		}
	}
	return "", false
}

func anyLineContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestSandboxBuildBase_BuildsBaseImageNatively(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "build-base")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build-base: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	want := "build -f " + assets + "/Dockerfile.base -t cenci-sandbox-base:" + tag + " -t cenci-sandbox-base:latest " + assets
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("runtime calls = %v, want exactly [%s]", lines, want)
	}
	if !strings.Contains(string(output), "Building cenci-sandbox-base:"+tag) {
		t.Errorf("expected build progress message, got:\n%s", output)
	}
}

func TestSandboxBuild_MonolithBuildsBaseFirstWhenMissing(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_INSPECT_EXIT=1")
	cmd.Dir = t.TempDir() // non-git cwd → monolith image
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if _, ok := findLineWithPrefix(lines, "image inspect cenci-sandbox-base:"+tag); !ok {
		t.Errorf("expected a base-image inspect, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !anyLineContains(lines, "-t cenci-sandbox-base:"+tag) {
		t.Errorf("expected the missing base image to be built first, got calls:\n%s", strings.Join(lines, "\n"))
	}
	wantMonolith := "build --build-arg BASE_VERSION=" + tag + " -t cenci-sandbox:latest -f " + assets + "/Dockerfile " + assets
	if _, ok := findLineWithPrefix(lines, wantMonolith); !ok {
		t.Errorf("expected monolith build call [%s], got calls:\n%s", wantMonolith, strings.Join(lines, "\n"))
	}
}

func TestSandboxBuild_RepoImageFromRepoDockerfile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".cenci"), 0o755); err != nil {
		t.Fatalf("mkdir .cenci: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".cenci", "Dockerfile"), []byte("FROM cenci-sandbox-base\n"), 0o644); err != nil {
		t.Fatalf("write repo Dockerfile: %v", err)
	}
	slug := launcher.Slugify(filepath.Base(repo))

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_INSPECT_EXIT=1")
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build (repo): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	wantRepo := "build --build-arg BASE_VERSION=" + tag + " -t cenci-sandbox-" + slug + ":latest -f " + repo + "/.cenci/Dockerfile " + repo + "/.cenci"
	if _, ok := findLineWithPrefix(lines, wantRepo); !ok {
		t.Errorf("expected repo-image build call [%s], got calls:\n%s", wantRepo, strings.Join(lines, "\n"))
	}
}

func TestSandboxBuild_BuildFailureExits1(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_INSPECT_EXIT=1", "FAKE_BUILD_EXIT=3")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "cenci sandbox build:") {
		t.Errorf("expected a 'cenci sandbox build:' error, got:\n%s", output)
	}
}

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

func TestSandboxUpdatePluginsCodex_RunsOneShotVolumeUpdate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--agent", "codex")
	cmd.Env = batchEnv(t, fakeDir, assets) // image exists, no running container
	cmd.Dir = t.TempDir()                  // non-git cwd → legacy "default" scope
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-plugins --agent codex: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	prefix := "run --rm --entrypoint /bin/bash -e CENCI_SANDBOX_AGENT=codex -v codex-cenci-home-default:/home/dev cenci-sandbox:latest -c "
	line, ok := findLineWithPrefix(lines, prefix)
	if !ok {
		t.Fatalf("expected a one-shot volume update run [%s...], got calls:\n%s", prefix, strings.Join(lines, "\n"))
	}
	if !strings.Contains(line, "provision_codex_plugins") {
		t.Errorf("expected the codex provisioning command, got: %s", line)
	}
}

func TestSandboxUpdatePlugins_BadAgent_Exits2NoRuntimeCalls(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--agent", "bogus")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls on a bad --agent, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxReseedCreds_ForwardsReseedCredsFlag(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "sandbox", "reseed-creds")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox reseed-creds: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--reseed-creds" {
		t.Errorf("captured argv = %v, want [--reseed-creds]", got)
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

// -- open ------------------------------------------------------------------

func TestOpenCh_ResolvesClaudeAndHaikuModel(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--agent claude --model haiku" {
		t.Errorf("captured argv = %v, want [--agent claude --model haiku]", got)
	}
}

func TestOpenXs_ResolvesCodexAndSolModel(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "xs")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open xs: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--agent codex --model gpt-5.6-sol" {
		t.Errorf("captured argv = %v, want [--agent codex --model gpt-5.6-sol]", got)
	}
}

func TestOpenAllShortcuts_ResolveExactlyAsCenciSandWould(t *testing.T) {
	cases := []struct {
		token, agent, model string
	}{
		{"ch", "claude", "haiku"},
		{"cs", "claude", "sonnet"},
		{"co", "claude", "opus"},
		{"cf", "claude", "fable"},
		{"xl", "codex", "gpt-5.6-luna"},
		{"xt", "codex", "gpt-5.6-terra"},
		{"xs", "codex", "gpt-5.6-sol"},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			dir := t.TempDir()
			capture := filepath.Join(dir, "argv.txt")
			writeFakeCenciSand(t, dir, capture, 0)

			cmd := exec.Command(binaryPath, "open", tc.token)
			cmd.Env = append(os.Environ(), "PATH="+dir)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("open %s: %v\n%s", tc.token, err, output)
			}

			want := "--agent " + tc.agent + " --model " + tc.model
			got := readCapturedArgv(t, capture)
			if joinArgv(got) != want {
				t.Errorf("captured argv = %v, want [%s]", got, want)
			}
		})
	}
}

func TestOpenShortcutConflictsWithAgentFlag_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "ch", "--agent", "codex")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Error("expected cenci-sand to never be exec'd on a shortcut/--agent conflict")
	}
}

func TestOpenUnknownFlag_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "--bogus")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Error("expected cenci-sand to never be exec'd for an unknown flag")
	}
}

func TestOpenUnrecognizedLeadingPositional_Exits2NoExec(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "not-a-shortcut")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

func TestOpen_ForwardsNameShellDockerHostNetworkFlags(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "--name", "mybox", "--shell", "--docker", "--host-network")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	want := "--name mybox --shell --docker --host-network"
	if joinArgv(got) != want {
		t.Errorf("captured argv = %v, want [%s]", got, want)
	}
}

func TestOpen_PassthroughAfterDoubleDash(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeCenciSand(t, dir, capture, 0)

	cmd := exec.Command(binaryPath, "open", "ch", "--", "--resume", "--custom-flag")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	want := "--agent claude --model haiku -- --resume --custom-flag"
	if joinArgv(got) != want {
		t.Errorf("captured argv = %v, want [%s]", got, want)
	}
}

func TestOpen_MissingCenciSandBinary_Exits1(t *testing.T) {
	dir := t.TempDir() // no cenci-sand fake

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
}

// -- argv[0] == "cn" dispatch ------------------------------------------

// buildCnAlias copies the built cenci binary to <dir>/cn so
// filepath.Base(os.Args[0]) == "cn" inside the copy.
func buildCnAlias(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	cnPath := filepath.Join(dir, "cn")
	if err := os.WriteFile(cnPath, data, 0o755); err != nil {
		t.Fatalf("write cn alias: %v", err)
	}
	return cnPath
}

func TestCnArgv0_RoutesToOpen(t *testing.T) {
	binDir := t.TempDir()
	cnPath := buildCnAlias(t, binDir)

	fakeDir := t.TempDir()
	capture := filepath.Join(fakeDir, "argv.txt")
	writeFakeCenciSand(t, fakeDir, capture, 0)

	cmd := exec.Command(cnPath, "xs")
	cmd.Env = append(os.Environ(), "PATH="+fakeDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn xs: %v\n%s", err, output)
	}

	got := readCapturedArgv(t, capture)
	if joinArgv(got) != "--agent codex --model gpt-5.6-sol" {
		t.Errorf("captured argv = %v, want [--agent codex --model gpt-5.6-sol]", got)
	}
}

func TestCnArgv0_BareInvocationDoesNotErrorLikeCenci(t *testing.T) {
	// A bare `cenci` (no subcommand) exits 2. `cn` with no args is a
	// bare `open` with no shortcut/flags -- valid, not an error.
	binDir := t.TempDir()
	cnPath := buildCnAlias(t, binDir)

	fakeDir := t.TempDir()
	capture := filepath.Join(fakeDir, "argv.txt")
	writeFakeCenciSand(t, fakeDir, capture, 0)

	cmd := exec.Command(cnPath)
	cmd.Env = append(os.Environ(), "PATH="+fakeDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn (bare): %v\n%s", err, output)
	}
}
