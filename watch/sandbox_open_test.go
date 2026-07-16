package main_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// -- fakes -------------------------------------------------------------
//
// These black-box tests exercise the real built `cenci` binary as a
// subprocess (binaryPath, built once in TestMain in main_test.go) with PATH
// overridden to a temp dir containing fake `docker`/`podman` (and, for the
// open path, `claude`) scripts. Fakes are plain POSIX `/bin/sh` scripts (not
// `#!/usr/bin/env ...`) so they resolve without depending on the overridden
// PATH containing a shell.

// writeFakeDocker writes a fake `docker` (or `podman`) to dir that appends
// each invocation's argv (space-joined) as a line to callLog, and — when
// invoked as `<name> ps ...` — prints psOutput to stdout.
func writeFakeDocker(t *testing.T, dir, name, callLog, psOutput string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + exectest.ShellQuote(callLog) + "\n" +
		"if [ \"$1\" = \"ps\" ]; then printf '%s' " + exectest.ShellQuote(psOutput) + "; fi\n" +
		"exit 0\n"
	exectest.WriteExecutable(t, filepath.Join(dir, name), body)
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

// readCapturedArgv reads a fake binary's capture file into a []string, one
// element per line (trailing blank line from the final \n dropped). Shared
// with doctor_update_test.go's fake cenci-installer.
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
// build/build-base/prune/update-agent/update-plugins run natively against docker/podman
// via internal/sandbox/launcher, so these tests assert what the fake
// *runtime* received. Both docker and podman fakes are always written so the
// podman-first runtime detection can never escape to a real runtime on
// machines (or CI runners) that have one. reap-orphans is pinned by the
// relocated bash contract suite (tests/reap-orphans.test.sh); reseed-creds
// is an alias for `cenci open --reseed-creds` and is covered with the open
// tests below.

// writeScriptedRuntime writes a fake docker/podman to dir that appends each
// invocation's argv (space-joined) as a line to callLog and answers scripted
// responses from env vars:
//
//	FAKE_INSPECT_EXIT    — `image inspect` exit code (default 0 = image exists)
//	FAKE_IMAGE_AGENT_LIFECYCLE — writable-agent image label (default home-v1)
//	FAKE_BUILD_EXIT      — `build` exit code (default 0)
//	FAKE_IMAGES          — `images ...` stdout
//	FAKE_PS              — `ps ...` stdout
//	FAKE_VOLUMES         — `volume ls` stdout
//	FAKE_INSPECT_LABEL   — container `inspect` stdout for label lookups
//	FAKE_INSPECT_MOUNTS  — container `inspect` stdout for mount lookups
//	FAKE_INSPECT_STATE   — container startup state (default "running 0")
//	FAKE_READY_EXIT      — readiness probe exit code (default 0)
//	FAKE_AGENT_HELPER_EXIT — running-container helper probe exit (default 0)
//	FAKE_STARTUP_ERROR   — persistent startup error marker text
//	FAKE_ATTACH_EXIT     — exit code of the final `exec -it ...` attach
//
// The open path drives the extra verbs: `rm` (exit 0), `run` (prints a
// container id), container `inspect` (label vs mounts told apart by the
// format string), non-interactive `exec` (readiness probe, exit 0), and the
// final `exec -it` attach — which the binary syscall.Execs, so the fake's
// exit code IS the binary's exit code.
func writeScriptedRuntime(t *testing.T, dir, name, callLog string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + exectest.ShellQuote(callLog) + "\n" +
		"case \"$1\" in\n" +
		"image) if [ \"$2\" = inspect ]; then printf '%s\\n' \"${FAKE_IMAGE_AGENT_LIFECYCLE:-home-v1}\"; exit \"${FAKE_INSPECT_EXIT:-0}\"; fi ;;\n" +
		"build) exit \"${FAKE_BUILD_EXIT:-0}\" ;;\n" +
		"images) printf '%s' \"${FAKE_IMAGES:-}\" ;;\n" +
		"ps) printf '%s' \"${FAKE_PS:-}\" ;;\n" +
		"volume) if [ \"$2\" = ls ]; then printf '%s' \"${FAKE_VOLUMES:-}\"; fi ;;\n" +
		"rm) exit 0 ;;\n" +
		"run) case \"$*\" in *'/bin/cat'*) printf '%s' \"${FAKE_STARTUP_ERROR:-}\"; exit \"${FAKE_STARTUP_ERROR_EXIT:-0}\" ;; esac; printf '%s\\n' fake-container-id ;;\n" +
		"inspect)\n" +
		"  case \"$*\" in\n" +
		"  *State.Status*) printf '%s\\n' \"${FAKE_INSPECT_STATE:-running 0}\"; exit \"${FAKE_CONTAINER_INSPECT_EXIT:-0}\" ;;\n" +
		"  *Labels*) printf '%s\\n' \"${FAKE_INSPECT_LABEL:-}\" ;;\n" +
		"  *Mounts*) printf '%s' \"${FAKE_INSPECT_MOUNTS:-}\" ;;\n" +
		"  esac\n" +
		"  ;;\n" +
		"logs) printf '%s' \"${FAKE_LOGS:-}\" ;;\n" +
		"exec) if [ \"$2\" = \"-it\" ]; then exit \"${FAKE_ATTACH_EXIT:-0}\"; fi; case \"$*\" in *'/tmp/cenci-ready'*) exit \"${FAKE_READY_EXIT:-0}\" ;; *'agent-cli.sh'*) exit \"${FAKE_AGENT_HELPER_EXIT:-0}\" ;; esac; exit 0 ;;\n" +
		"esac\n" +
		"exit 0\n"
	exectest.WriteExecutable(t, filepath.Join(dir, name), body)
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
	want := "build --build-arg BASE_VERSION=" + tag + " --label cenci.agent-cli=home-v1 -t cenci-sandbox:latest -f " + assets + "/Dockerfile " + assets
	if !anyLineContains(lines, want) {
		t.Errorf("expected monolith build call %q, got calls:\n%s", want, strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "INSTALL_CLAUDE") || anyLineContains(lines, "INSTALL_CODEX") || anyLineContains(lines, "AGENTS_REFRESH") {
		t.Errorf("build still passed removed agent build arguments; calls:\n%s", strings.Join(lines, "\n"))
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
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_INSPECT_EXIT=1")
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build (repo): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	want := "build --build-arg BASE_VERSION=" + tag + " --label cenci.agent-cli=home-v1 -t cenci-sandbox-" + slug + ":latest -f " + repoRoot + "/.cenci/Dockerfile " + repoRoot + "/.cenci"
	if !anyLineContains(lines, want) {
		t.Errorf("expected repo-image build call %q, got calls:\n%s", want, strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "INSTALL_CLAUDE") || anyLineContains(lines, "INSTALL_CODEX") || anyLineContains(lines, "AGENTS_REFRESH") {
		t.Errorf("repo build still passed removed agent build arguments; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxBuild_AgentsFlagIsGone(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "build", "--agents", "claude")
	cmd.Env = batchEnv(t, fakeDir, assets)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected removed --agents flag to exit 2, got %T %v\n%s", err, err, output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls for removed --agents flag, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUpdateAgent_RebuildsLegacyImageBeforeMigration(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_IMAGE_AGENT_LIFECYCLE=legacy")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent legacy image migration: %v\n%s", err, output)
	}
	lines := callLogLines(t, callLog)
	if !anyLineContains(lines, "--label cenci.agent-cli=home-v1 -t cenci-sandbox:latest") {
		t.Errorf("expected legacy image to rebuild with the writable-agent label; calls:\n%s", strings.Join(lines, "\n"))
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

func TestSandboxUpdateAgent_StoppedVolumeUsesUIDSafeMaintenanceContainer(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "codex", "--name", "work")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent stopped volume: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	prefix := "run --rm --user root -e HOST_UID=" + itoa(os.Getuid()) +
		" -e HOST_GID=" + itoa(os.Getgid()) +
		" -e CENCI_SANDBOX_AGENT=codex -v codex-cenci-home-work:/home/dev cenci-sandbox:latest -c "
	line, ok := findLineWithPrefix(lines, prefix)
	if !ok {
		t.Fatalf("expected UID-safe maintenance run [%s...], got calls:\n%s", prefix, strings.Join(lines, "\n"))
	}
	if !strings.Contains(line, "source /usr/local/bin/lib/agent-cli.sh") || !strings.Contains(line, "update_agent_cli codex") {
		t.Errorf("expected maintenance command to force a Codex update, got: %s", line)
	}
}

func TestSandboxUpdateAgent_RunningContainerExecsAsDev(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_PS=claude-cenci-default\n")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent running container: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	prefix := "exec -u dev -e HOME=/home/dev -e NPM_CONFIG_PREFIX=/home/dev/.local claude-cenci-default /bin/bash -c "
	line, ok := findLineWithPrefix(lines, prefix)
	if !ok {
		t.Fatalf("expected running-container exec [%s...], got calls:\n%s", prefix, strings.Join(lines, "\n"))
	}
	if !strings.Contains(line, "update_agent_cli claude") {
		t.Errorf("expected default Claude update command, got: %s", line)
	}
	if anyLineContains(lines, "run --rm") {
		t.Errorf("running container should not use a maintenance container; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUpdateAgent_LegacyRunningContainerFailsClearly(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_AGENT_HELPER_EXIT=1",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected legacy running container error, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "predates writable agent CLI support") {
		t.Errorf("expected clear legacy-container diagnostic, got:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "update_agent_cli") {
		t.Errorf("must not invoke missing helper; calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
	}
}

func TestSandboxUpdateAgent_BuildsMissingImageBeforeMaintenance(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_INSPECT_EXIT=1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent image creation: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if !anyLineContains(lines, "-t cenci-sandbox:latest") || !anyLineContains(lines, "run --rm --user root") {
		t.Errorf("expected missing image to build before maintenance run; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUpdateAgent_UsageErrorsMakeNoRuntimeCalls(t *testing.T) {
	tests := [][]string{
		{"sandbox", "update-agent", "--agent", "gemini"},
		{"sandbox", "update-agent", "--bogus"},
		{"sandbox", "update-agent", "extra"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args[2:], "_"), func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			cmd := exec.Command(binaryPath, args...)
			cmd.Env = batchEnv(t, fakeDir, assets)
			output, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("expected usage error exit 2, got %T %v\n%s", err, err, output)
			}
			if lines := callLogLines(t, callLog); len(lines) > 0 {
				t.Errorf("expected no runtime calls, got:\n%s", strings.Join(lines, "\n"))
			}
		})
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
	prefix := "run --rm --user root -e HOST_UID=" + itoa(os.Getuid()) +
		" -e HOST_GID=" + itoa(os.Getgid()) +
		" -e CENCI_SANDBOX_AGENT=codex -v codex-cenci-home-default:/home/dev cenci-sandbox:latest -c "
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
	runLine, ok := findLineWithPrefix(lines, "run ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(runLine, "-e CENCI_SANDBOX_RESEED_CREDS=1") {
		t.Errorf("expected the reseed env flag in the run argv, got: %s", runLine)
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
//
// `cenci open` launches natively: the launcher engine creates/attaches the
// container itself and finally syscall.Execs the runtime CLI, so these tests
// assert the docker argv — including the entrypoint contract
// (launcher-lifecycle), the model shortcuts, the strict flag grammar
// (flag-parsing), and the #195 cenci wiring (cenci-wiring) that the retired
// bash suites used to pin against cenci-sand.

// openTestEnv builds the black-box environment for native `cenci open` runs:
// the scripted runtimes on PATH, an isolated HOME, the asset fixture,
// deterministic TERM/TMUX_PANE, scrubbed optional env passthroughs, and a live
// events socket under a private 0700 XDG_RUNTIME_DIR — pre-created here so the
// subprocess wires cenci without ever spawning a real daemon. No host `claude`
// is installed: agents live in the container's persistent home, so the launcher
// never resolves an agent binary from the host.
func openTestEnv(t *testing.T, fakeDir, assets string) (env []string, home, socketDir string) {
	t.Helper()
	home = t.TempDir()
	xdg := t.TempDir()
	socketDir = filepath.Join(xdg, "cenci")
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	l, err := net.Listen("unix", filepath.Join(socketDir, "cenci-events.sock"))
	if err != nil {
		t.Fatalf("listen events socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	// os/exec keeps the LAST duplicate env key, so these appends override the
	// inherited values; the empty assignments scrub optional passthroughs
	// (and a possible ambient CENCI_SANDBOX) for deterministic argv.
	env = append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+home,
		"CENCI_SANDBOX_ASSETS="+assets,
		"XDG_RUNTIME_DIR="+xdg,
		"TERM=xterm-256color",
		"TMUX_PANE=%7",
		"COLORTERM=", "CONTEXT7_API_KEY=", "OPENAI_API_KEY=", "CENCI_SANDBOX=",
	)
	return env, home, socketDir
}

// attachLine returns the final `exec -it ...` attach argv line from the call
// log (the line the binary syscall.Exec'd the fake runtime with).
func attachLine(t *testing.T, lines []string) string {
	t.Helper()
	line, ok := findLineWithPrefix(lines, "exec -it ")
	if !ok {
		t.Fatalf("expected an interactive attach exec, got calls:\n%s", strings.Join(lines, "\n"))
	}
	return line
}

func TestOpenAllShortcuts_AttachTheirAgentAndModel(t *testing.T) {
	cases := []struct {
		token, agent, model, permFlag string
	}{
		{"ch", "claude", "haiku", "--dangerously-skip-permissions"},
		{"cs", "claude", "sonnet", "--dangerously-skip-permissions"},
		{"co", "claude", "opus", "--dangerously-skip-permissions"},
		{"cf", "claude", "fable", "--dangerously-skip-permissions"},
		{"xl", "codex", "gpt-5.6-luna", "--dangerously-bypass-approvals-and-sandbox"},
		{"xt", "codex", "gpt-5.6-terra", "--dangerously-bypass-approvals-and-sandbox"},
		{"xs", "codex", "gpt-5.6-sol", "--dangerously-bypass-approvals-and-sandbox"},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, _ := openTestEnv(t, fakeDir, assets)
			if tc.agent == "codex" {
				// Codex refuses to launch without auth staged from the host.
				if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.Command(binaryPath, "open", tc.token)
			cmd.Env = env
			cmd.Dir = t.TempDir() // non-git cwd → legacy "default" scope
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("open %s: %v\n%s", tc.token, err, output)
			}

			line := attachLine(t, callLogLines(t, callLog))
			wantTail := tc.agent + "-cenci-default " + tc.agent + " " + tc.permFlag + " --model " + tc.model
			if !strings.HasSuffix(line, wantTail) {
				t.Errorf("attach argv = %q, want suffix %q", line, wantTail)
			}
		})
	}
}

func TestOpenBare_DefaultsToClaudeSonnet(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open: %v\n%s", err, output)
	}

	line := attachLine(t, callLogLines(t, callLog))
	if !strings.HasSuffix(line, "claude --dangerously-skip-permissions --model sonnet") {
		t.Errorf("attach argv = %q, want the claude/sonnet defaults", line)
	}
}

func TestOpenModelFlag_OverridesShortcutModel(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch", "--model", "opus")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch --model opus: %v\n%s", err, output)
	}

	line := attachLine(t, callLogLines(t, callLog))
	if !strings.HasSuffix(line, "claude --dangerously-skip-permissions --model opus") {
		t.Errorf("attach argv = %q, want the explicit --model to win", line)
	}
}

func TestOpen_FreshCreate_PinsEntrypointContract(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home, socketDir := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}

	// The entrypoint contract, byte-for-byte where it matters: privileged
	// create + detached lifecycle label + host identity env + the PID-1 idle
	// tail that owns readiness.
	for _, want := range []string{
		"--name claude-cenci-default",
		"--hostname sandbox-default",
		"--label cenci-sand.lifecycle=detached",
		"-d --init --rm",
		"--user root",
		"-e TERM=xterm-256color",
		"-e CENCI_SANDBOX=1",
		"-e CENCI_SANDBOX_AGENT=claude",
		fmt.Sprintf("-e HOST_UID=%d", os.Getuid()),
		fmt.Sprintf("-e HOST_GID=%d", os.Getgid()),
		"-e WORKSPACE_SCOPE=legacy",
		"-v " + home + "/Repos:/workspace",
		"-v claude-cenci-home-default:/home/dev",
		"-v " + socketDir + ":/run/user/1000/cenci:ro",
		":/usr/local/bin/cenci:ro",
		"-e XDG_RUNTIME_DIR=/run/user/1000",
	} {
		if !strings.Contains(runLine, want) {
			t.Errorf("run argv missing %q:\n%s", want, runLine)
		}
	}
	// Claude lives in the persistent home, not a host bind mount.
	if strings.Contains(runLine, ":/usr/local/bin/claude:ro") {
		t.Errorf("run argv must not bind-mount the host claude binary:\n%s", runLine)
	}
	// TMUX_PANE must only travel per exec session (asserted on the attach
	// below): baked into the container-lifetime env it goes stale when the
	// creating pane closes, and reap-orphans would kill PID 1 (#356).
	if strings.Contains(runLine, "TMUX_PANE") {
		t.Errorf("run argv must not carry TMUX_PANE (#356):\n%s", runLine)
	}
	if !strings.HasSuffix(runLine, "cenci-sandbox:latest -c touch /tmp/cenci-ready && exec sleep infinity") {
		t.Errorf("run argv must end with the image and PID-1 readiness tail, got:\n%s", runLine)
	}

	// Readiness was polled as dev before the attach.
	if !anyLineContains(lines, "exec -u dev claude-cenci-default test -e /tmp/cenci-ready") {
		t.Errorf("expected a readiness probe, got calls:\n%s", strings.Join(lines, "\n"))
	}

	// The attach itself runs as dev with the in-container marker set.
	line := attachLine(t, lines)
	for _, want := range []string{"-u dev", "-e CENCI_SANDBOX=1", "-e CENCI_SANDBOX_AGENT=claude", "-e TMUX_PANE=%7"} {
		if !strings.Contains(line, want) {
			t.Errorf("attach argv missing %q:\n%s", want, line)
		}
	}
}

func TestOpen_FirstInstallFailureSurfacesPersistentDiagnostic(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env,
		"FAKE_READY_EXIT=1",
		"FAKE_INSPECT_STATE=exited 1",
		"FAKE_STARTUP_ERROR=entrypoint: selected agent CLI is unavailable; first launch requires network access to npm",
	)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "first launch requires network access to npm") {
		t.Errorf("expected persistent first-install diagnostic, got:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("agent attach must not run after startup failure; calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
	}
}

func TestOpen_NameFlag_ScopesContainerVolumeHostname(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--name", "mybox")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open --name mybox: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	for _, want := range []string{
		"--name claude-cenci-mybox",
		"--hostname sandbox-mybox",
		"-v claude-cenci-home-mybox:/home/dev",
	} {
		if !strings.Contains(runLine, want) {
			t.Errorf("run argv missing %q:\n%s", want, runLine)
		}
	}
}

func TestOpenShell_AttachesBash(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--shell")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open --shell: %v\n%s", err, output)
	}

	line := attachLine(t, callLogLines(t, callLog))
	if !strings.HasSuffix(line, "claude-cenci-default /bin/bash") {
		t.Errorf("attach argv = %q, want a /bin/bash shell attach", line)
	}
	if !strings.Contains(string(output), "Attaching shell to running 'claude-cenci-default'") {
		t.Errorf("expected the shell-attach message, got:\n%s", output)
	}
}

func TestOpenPassthrough_ReachesAgentVerbatim(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch", "--", "--resume", "-p", "fix the bug")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch -- ...: %v\n%s", err, output)
	}

	line := attachLine(t, callLogLines(t, callLog))
	if !strings.HasSuffix(line, "claude --dangerously-skip-permissions --model haiku --resume -p fix the bug") {
		t.Errorf("attach argv = %q, want the passthrough tokens verbatim after the agent flags", line)
	}
}

func TestOpenReseedCreds_SetsReseedEnvOnCreate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--reseed-creds")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open --reseed-creds: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(runLine, "-e CENCI_SANDBOX_RESEED_CREDS=1") {
		t.Errorf("expected the reseed env flag in the run argv, got:\n%s", runLine)
	}
}

func TestOpenHostNetwork_AddsNetworkHostWithWarning(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--host-network")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open --host-network: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, _ := findLineWithPrefix(lines, "run ")
	if !strings.Contains(runLine, "--network host") {
		t.Errorf("expected --network host in the run argv, got:\n%s", runLine)
	}
	if !strings.Contains(string(output), "weakens the container's isolation boundary") {
		t.Errorf("expected the host-network warning, got:\n%s", output)
	}
}

func TestOpenDocker_MountsRuntimeSocketOrWarns(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--docker")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open --docker: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, _ := findLineWithPrefix(lines, "run ")
	if info, statErr := os.Stat("/var/run/docker.sock"); statErr == nil && info.Mode()&os.ModeSocket != 0 {
		// Host has a real docker socket: it takes precedence and gets mounted.
		if !strings.Contains(runLine, "-v /var/run/docker.sock:/var/run/docker.sock") {
			t.Errorf("expected the host docker socket mount, got:\n%s", runLine)
		}
		if !strings.Contains(string(output), "root-equivalent") {
			t.Errorf("expected the docker-socket-mounted warning, got:\n%s", output)
		}
	} else {
		// No discoverable socket (the private XDG_RUNTIME_DIR has no
		// podman.sock either): a warning, and no socket mount.
		if !strings.Contains(string(output), "no container runtime socket found") {
			t.Errorf("expected the no-socket warning, got:\n%s", output)
		}
		if strings.Contains(runLine, ":/var/run/docker.sock") {
			t.Errorf("expected no socket mount, got:\n%s", runLine)
		}
	}
}

// TestOpenDocker_SocketMountedPrintsWarning pins the mounted-socket warning
// deterministically (independent of whether the test host happens to have a
// real /var/run/docker.sock): it fabricates a podman.sock under a private
// XDG_RUNTIME_DIR so the runtime-socket lookup always finds one to mount.
func TestOpenDocker_SocketMountedPrintsWarning(t *testing.T) {
	if info, statErr := os.Stat("/var/run/docker.sock"); statErr == nil && info.Mode()&os.ModeSocket != 0 {
		t.Skip("host has a real docker socket; TestOpenDocker_MountsRuntimeSocketOrWarns already covers the mounted-warning branch")
	}

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, socketDir := openTestEnv(t, fakeDir, assets)

	xdg := filepath.Dir(socketDir)
	podmanDir := filepath.Join(xdg, "podman")
	if err := os.Mkdir(podmanDir, 0o700); err != nil {
		t.Fatalf("mkdir podman dir: %v", err)
	}
	podmanSock := filepath.Join(podmanDir, "podman.sock")
	l, err := net.Listen("unix", podmanSock)
	if err != nil {
		t.Fatalf("listen podman socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	cmd := exec.Command(binaryPath, "open", "--docker")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open --docker: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, _ := findLineWithPrefix(lines, "run ")
	if !strings.Contains(runLine, "-v "+podmanSock+":/var/run/docker.sock") {
		t.Errorf("expected the podman socket mount, got:\n%s", runLine)
	}
	if !strings.Contains(string(output), "root-equivalent") {
		t.Errorf("expected the docker-socket-mounted warning, got:\n%s", output)
	}
}

func TestOpenCodexWithoutAuth_Exits1(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets) // no ~/.codex/auth.json, OPENAI_API_KEY scrubbed

	cmd := exec.Command(binaryPath, "open", "xt")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "requires Codex auth") {
		t.Errorf("expected the codex auth error, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); anyLineContains(lines, "run ") {
		t.Errorf("expected no container run without codex auth, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpen_AttachToRunning_SkipsCreate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace\n/home/dev\n/run/user/1000/cenci\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch (running): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if _, ok := findLineWithPrefix(lines, "run "); ok {
		t.Errorf("expected no container create when already running, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !anyLineContains(lines, "exec -u dev claude-cenci-default test -e /tmp/cenci-ready") {
		t.Errorf("expected the detached-label readiness gate, got calls:\n%s", strings.Join(lines, "\n"))
	}
	attachLine(t, lines)
	if strings.Contains(string(output), "created without cenci wiring") {
		t.Errorf("wired container must not warn, got:\n%s", output)
	}
}

func TestOpen_AttachToUnwiredRunning_Warns(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace\n/home/dev\n", // no cenci socket mount
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch (unwired): %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "was created without cenci wiring") {
		t.Errorf("expected the #195 unwired warning, got:\n%s", output)
	}
	attachLine(t, callLogLines(t, callLog))
}

func TestOpen_NoEventsSocket_LaunchesUnwiredWithWarning(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	// Point XDG at a fresh dir with no live socket. CENCI_SANDBOX=1 keeps
	// daemon.EnsureRunning inert (the launcher reads it nowhere else), so the
	// subprocess never spawns a real daemon; the 3s socket poll then expires.
	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"XDG_RUNTIME_DIR="+t.TempDir(),
		"CENCI_SANDBOX=1",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch (no socket): %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "events socket is unavailable") {
		t.Errorf("expected the unavailable-socket warning, got:\n%s", output)
	}
	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run ")
	if !ok {
		t.Fatalf("expected the launch to proceed unwired, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(runLine, ":/usr/local/bin/cenci:ro") || strings.Contains(runLine, ":/run/user/1000/cenci:ro") {
		t.Errorf("expected no cenci mounts without a live events socket, got:\n%s", runLine)
	}
}

func TestOpen_AttachExitCodePropagates(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "FAKE_ATTACH_EXIT=7")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 7 {
		t.Errorf("exit code = %d, want the attach's own 7 (syscall.Exec propagation)\n%s", exitErr.ExitCode(), output)
	}
}

func TestOpenShortcutConflictsWithAgentFlag_Exits2NoRuntimeCalls(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch", "--agent", "codex")
	cmd.Env = env
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
		t.Errorf("expected no runtime calls on a shortcut/--agent conflict, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenUnknownFlag_Exits2NoRuntimeCalls(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--bogus")
	cmd.Env = env
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
		t.Errorf("expected no runtime calls for an unknown flag, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenUnrecognizedLeadingPositional_Exits2NoRuntimeCalls(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "not-a-shortcut")
	cmd.Env = env
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
		t.Errorf("expected no runtime calls for an unrecognized positional, got:\n%s", strings.Join(lines, "\n"))
	}
}

// -- argv[0] == "cn" dispatch ------------------------------------------

// buildArgv0Alias copies the built cenci binary to <dir>/<name> so
// filepath.Base(os.Args[0]) == name inside the copy ("cn" routes to open,
// "cenci-sand" hits the tombstone).
func buildArgv0Alias(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	aliasPath := filepath.Join(dir, name)
	if err := os.WriteFile(aliasPath, data, 0o755); err != nil {
		t.Fatalf("write %s alias: %v", name, err)
	}
	return aliasPath
}

func TestCnArgv0_RoutesToOpen(t *testing.T) {
	binDir := t.TempDir()
	cnPath := buildArgv0Alias(t, binDir, "cn")

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home, _ := openTestEnv(t, fakeDir, assets)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(cnPath, "xs")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn xs: %v\n%s", err, output)
	}

	line := attachLine(t, callLogLines(t, callLog))
	if !strings.HasSuffix(line, "codex --dangerously-bypass-approvals-and-sandbox --model gpt-5.6-sol") {
		t.Errorf("attach argv = %q, want the codex/sol shortcut resolution", line)
	}
}

func TestCnArgv0_BareInvocationDoesNotErrorLikeCenci(t *testing.T) {
	// A bare `cenci` (no subcommand) exits 2. `cn` with no args is a
	// bare `open` with no shortcut/flags -- valid, not an error.
	binDir := t.TempDir()
	cnPath := buildArgv0Alias(t, binDir, "cn")

	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(cnPath)
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn (bare): %v\n%s", err, output)
	}
}

// -- argv[0] == "cenci-sand" tombstone ----------------------------------

func TestCenciSandArgv0_TombstoneExits2WithMigrationMap(t *testing.T) {
	// install.sh repoints a stale ~/.local/bin/cenci-sand symlink at the
	// cenci binary; invoking it must fail loudly with the migration map
	// instead of guessing at the removed bash launcher's grammar.
	binDir := t.TempDir()
	sandPath := buildArgv0Alias(t, binDir, "cenci-sand")

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(sandPath, "--build")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	out := string(output)
	if !strings.Contains(out, "cenci open") || !strings.Contains(out, "cenci sandbox") {
		t.Errorf("expected the migration map to mention cenci open and cenci sandbox, got:\n%s", out)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls from the tombstone, got: %v", lines)
	}
}
