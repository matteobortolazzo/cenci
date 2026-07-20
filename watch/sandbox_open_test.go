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
//	FAKE_INSPECT_EXIT    — image listing exit code (default 0 = image exists)
//	FAKE_IMAGE_AGENT_LIFECYCLE — shared-agent image label (default shared-v2)
//	FAKE_IMAGE_BASE_VERSION — baked cenci.base-version image label (default
//	                        set per-test by batchEnv/openTestEnv to the
//	                        fixture's current BaseTag, so pre-existing
//	                        "image current -> skip build" tests keep passing
//	                        without every caller having to know the tag)
//	FAKE_BUILD_EXIT      — `build` exit code (default 0)
//	FAKE_IMAGES          — `images ...` stdout
//	FAKE_PS              — `ps ...` stdout
//	FAKE_VOLUMES         — `volume ls` stdout
//	FAKE_INSPECT_LABEL   — container `inspect` stdout for label lookups
//	FAKE_INSPECT_MOUNTS  — container `inspect` stdout for mount lookups
//	FAKE_INSPECT_STATE   — container startup state (default "running 0")
//	FAKE_CONTAINER_INSPECT_EXIT — container `inspect` exit code, for all three
//	                        format-string lookups (State.Status, .RW mounts,
//	                        Mounts) — default 0; set nonzero to simulate
//	                        inspect erroring (e.g. the container was already
//	                        removed by `--rm`)
//	FAKE_READY_EXIT      — readiness probe exit code (default 0)
//	FAKE_STARTUP_ERROR   — persistent agent-CLI-missing marker text, read
//	                        from /home/dev/.cenci-agent-startup-error
//	FAKE_BOOT_LOG        — entrypoint boot-log content, read from
//	                        /home/dev/.cenci-boot.log
//	FAKE_STARTUP_MARKER  — generic entrypoint-failure trap marker text, read
//	                        from /home/dev/.cenci-startup-failed
//	FAKE_AGENT_CHECK_EXIT — EnsureAgentVolume's populated-check exit code
//	                        (default 0 = the shared volume is populated)
//	FAKE_ATTACH_EXIT     — exit code of the final `exec -it ...` attach
//	FAKE_PLUGIN_MANIFEST — `cenci diagnose`'s best-effort plugin-manifest
//	                        read, from either agent's marketplace.json
//	                        (/home/dev/.claude/plugins/marketplaces/cenci/
//	                        .claude-plugin/marketplace.json or the Codex
//	                        equivalent under /home/dev/.codex/...) via the
//	                        same short-lived `run --entrypoint /bin/cat`
//	                        home-volume read pattern as the three
//	                        startupFailureDetail vars above; unset/empty
//	                        simulates a read failure (diagnose falls back to
//	                        "unknown")
//
// The open path drives the extra verbs: `rm` (exit 0), `run` (prints a
// container id), container `inspect` (label vs mounts told apart by the
// format string), non-interactive `exec` (readiness probe, exit 0), and the
// final `exec -it` attach — which the binary syscall.Execs, so the fake's
// exit code IS the binary's exit code. `run --entrypoint /bin/cat ... <path>`
// (startupFailureDetail's short-lived home-volume reads, and diagnose's
// plugin-manifest read) is told apart by the requested path, one of the four
// FAKE_* vars above.
func writeScriptedRuntime(t *testing.T, dir, name, callLog string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + exectest.ShellQuote(callLog) + "\n" +
		"case \"$1\" in\n" +
		"image) if [ \"$2\" = inspect ]; then printf '%s|%s\\n' \"${FAKE_IMAGE_AGENT_LIFECYCLE:-shared-v2}\" \"${FAKE_IMAGE_BASE_VERSION:-}\"; exit \"${FAKE_IMAGE_INSPECT_EXIT:-0}\"; fi ;;\n" +
		"build) exit \"${FAKE_BUILD_EXIT:-0}\" ;;\n" +
		"images) if [ -n \"${FAKE_IMAGES+x}\" ]; then printf '%s' \"${FAKE_IMAGES}\"; else for last do :; done; printf '%s\\n' \"${last}\"; fi; exit \"${FAKE_INSPECT_EXIT:-0}\" ;;\n" +
		"ps) printf '%s' \"${FAKE_PS:-}\" ;;\n" +
		"volume) if [ \"$2\" = ls ]; then printf '%s' \"${FAKE_VOLUMES:-}\"; exit \"${FAKE_VOLUME_LS_EXIT:-0}\"; fi ;;\n" +
		"rm) exit 0 ;;\n" +
		"run) case \"$*\" in\n" +
		"  *'/bin/cat'*)\n" +
		"    case \"$*\" in\n" +
		"    *'.cenci-agent-startup-error'*) printf '%s' \"${FAKE_STARTUP_ERROR:-}\"; exit \"${FAKE_STARTUP_ERROR_EXIT:-0}\" ;;\n" +
		"    *'.cenci-boot.log'*) printf '%s' \"${FAKE_BOOT_LOG:-}\"; exit \"${FAKE_BOOT_LOG_EXIT:-0}\" ;;\n" +
		"    *'.cenci-startup-failed'*) printf '%s' \"${FAKE_STARTUP_MARKER:-}\"; exit \"${FAKE_STARTUP_MARKER_EXIT:-0}\" ;;\n" +
		"    *'marketplace.json'*) printf '%s' \"${FAKE_PLUGIN_MANIFEST:-}\"; exit \"${FAKE_PLUGIN_MANIFEST_EXIT:-0}\" ;;\n" +
		"    esac\n" +
		"    ;;\n" +
		"  *'agent-cli.sh update'*) [ -z \"${FAKE_RUN_STDERR:-}\" ] || printf '%s\\n' \"${FAKE_RUN_STDERR}\" >&2; exit \"${FAKE_RUN_EXIT:-0}\" ;;\n" +
		"  *'test -x /opt/cenci-agent'*) exit \"${FAKE_AGENT_CHECK_EXIT:-0}\" ;;\n" +
		"  esac\n" +
		"  printf '%s\\n' fake-container-id ;;\n" +
		"inspect)\n" +
		"  case \"$*\" in\n" +
		"  *State.Status*) printf '%s\\n' \"${FAKE_INSPECT_STATE:-running 0}\"; exit \"${FAKE_CONTAINER_INSPECT_EXIT:-0}\" ;;\n" +
		"  *Labels*) printf '%s\\n' \"${FAKE_INSPECT_LABEL:-}\" ;;\n" +
		"  *'.RW'*) printf '%b' \"${FAKE_AGENT_MOUNTS:-cenci-agent-cli-claude|/opt/cenci-agent|false\\n}\"; exit \"${FAKE_CONTAINER_INSPECT_EXIT:-0}\" ;;\n" +
		"  *Mounts*) printf '%b' \"${FAKE_INSPECT_MOUNTS:-}\"; exit \"${FAKE_CONTAINER_INSPECT_EXIT:-0}\" ;;\n" +
		"  esac\n" +
		"  ;;\n" +
		"logs) printf '%s' \"${FAKE_LOGS:-}\" ;;\n" +
		"exec) if [ \"$2\" = \"-it\" ]; then exit \"${FAKE_ATTACH_EXIT:-0}\"; fi; case \"$*\" in *'/tmp/cenci-ready'*) exit \"${FAKE_READY_EXIT:-0}\" ;; esac; exit 0 ;;\n" +
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
// appends override the inherited values. FAKE_IMAGE_BASE_VERSION defaults to
// the fixture's own current BaseTag so pre-existing "image current -> skip
// build" tests keep passing now that the fake's `image inspect` branch
// carries a base-version field too; tests that want to exercise base-drift
// staleness override it with their own append.
func batchEnv(t *testing.T, fakeDir, assets string) []string {
	t.Helper()
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}
	return append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+t.TempDir(),
		"CENCI_SANDBOX_ASSETS="+assets,
		"FAKE_VOLUMES=cenci-agent-cli-claude\\ncenci-agent-cli-codex\\n",
		"FAKE_IMAGE_BASE_VERSION="+tag,
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
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_IMAGES=")
	cmd.Dir = t.TempDir() // non-git cwd → monolith image
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if _, ok := findLineWithPrefix(lines, "images --format {{.Repository}}:{{.Tag}} cenci-sandbox-base:"+tag); !ok {
		t.Errorf("expected a typed base-image listing, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !anyLineContains(lines, "-t cenci-sandbox-base:"+tag) {
		t.Errorf("expected the missing base image to be built first, got calls:\n%s", strings.Join(lines, "\n"))
	}
	want := "build --build-arg BASE_VERSION=" + tag + " --label cenci.agent-cli=shared-v2 --label cenci.base-version=" + tag + " -t cenci-sandbox:latest -f " + assets + "/Dockerfile " + assets
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
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_IMAGES=")
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build (repo): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	want := "build --build-arg BASE_VERSION=" + tag + " --label cenci.agent-cli=shared-v2 --label cenci.base-version=" + tag + " -t cenci-sandbox-" + slug + ":latest -f " + repoRoot + "/.cenci/Dockerfile " + repoRoot + "/.cenci"
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
	if !anyLineContains(lines, "--label cenci.agent-cli=shared-v2 --label cenci.base-version=") {
		t.Errorf("expected legacy image to rebuild with both the shared-agent and base-version labels; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxBuild_BuildFailureExits1(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_IMAGES=", "FAKE_BUILD_EXIT=3")
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

func TestSandboxUpdateAgent_UsesIsolatedGlobalVolumeAndExactVersion(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "codex", "--version", "1.2.3")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	prefix := "run --rm --user root --cap-drop=ALL --security-opt=no-new-privileges --entrypoint /bin/bash -v cenci-agent-cli-codex:/opt/cenci-agent cenci-sandbox:latest /usr/local/bin/lib/agent-cli.sh update codex 1.2.3"
	line, ok := findLineWithPrefix(lines, prefix)
	if !ok {
		t.Fatalf("expected isolated global update [%s], got calls:\n%s", prefix, strings.Join(lines, "\n"))
	}
	for _, forbidden := range []string{"/home/dev", "/workspace", "HOST_UID", "HOST_GID", "OPENAI_API_KEY", "docker.sock", "/tmp/host-"} {
		if strings.Contains(line, forbidden) {
			t.Errorf("isolated updater argv contains %q: %s", forbidden, line)
		}
	}
	if !strings.Contains(string(output), "host-global") && !strings.Contains(string(output), "used by every sandbox on this host") {
		t.Errorf("expected a note that --version pins/downgrades host-globally, got:\n%s", output)
	}
}

// TestSandboxUpdateAgent_UsesMonolithImageEvenInsideRepoWithOwnImage pins the
// finding-1 security fix: the current directory's own repo image
// (<repo-root>/.cenci/Dockerfile) is a caller-controlled build input, and the
// updater runs as root with the host-global agent CLI volume mounted
// read-write. If the updater ever honored that repo image, a malicious repo
// image could gain root write access to a volume every sandbox on the host
// mounts read-only. The updater must always target the trusted, checked-in
// monolith image, never the repo's own image, even when running from inside
// such a repo.
func TestSandboxUpdateAgent_UsesMonolithImageEvenInsideRepoWithOwnImage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

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
	repoImage := "cenci-sandbox-" + slug + ":latest"

	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = repo // a repo that opts into its own image via .cenci/Dockerfile
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent (repo scope): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	updateLine, ok := findLineWithPrefix(lines, "run --rm --user root --cap-drop=ALL --security-opt=no-new-privileges --entrypoint /bin/bash")
	if !ok {
		t.Fatalf("expected the isolated updater run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(updateLine, "cenci-sandbox:latest") {
		t.Errorf("expected the updater to target the monolith image, got: %s", updateLine)
	}
	if strings.Contains(updateLine, repoImage) {
		t.Errorf("updater must never target the repo's own image (%s); got: %s", repoImage, updateLine)
	}
	if anyLineContains(lines, "-f "+repo+"/.cenci/Dockerfile") {
		t.Errorf("update-agent must never build the repo's own image; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUpdateAgent_IsGlobalEvenWhenRepositoryContainersAreRunning(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_PS=claude-cenci-one\nclaude-cenci-two\n")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent running container: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if !anyLineContains(lines, "-v cenci-agent-cli-claude:/opt/cenci-agent") || anyLineContains(lines, "exec -u dev") {
		t.Errorf("update should use only the global volume, never a running workload; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUpdateAgent_RemovedNameFlagIsUsageError(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--name", "old")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected removed flag usage error, got %T %v\n%s", err, err, output)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("removed --name must make no runtime calls: %v", lines)
	}
}

func TestSandboxUpdateAgent_BuildsMissingImageBeforeMaintenance(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_IMAGES=")
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
		{"sandbox", "update-agent", "--version", "latest"},
		{"sandbox", "update-agent", "--version", "^1.2.3"},
		{"sandbox", "update-agent", "--name", "old"},
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
		" -e CENCI_SANDBOX_AGENT=codex -e CENCI_AGENT_CLI=/opt/cenci-agent/current/node_modules/.bin/codex" +
		" -v codex-cenci-home-default:/home/dev -v cenci-agent-cli-codex:/opt/cenci-agent:ro cenci-sandbox:latest -c "
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

func TestSandboxUpdatePluginsAll_RefreshesEveryRunningContainer(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--all")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_PS=claude-cenci-one\nclaude-cenci-two\ncodex-cenci-three\n")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-plugins --all: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	for _, name := range []string{"claude-cenci-one", "claude-cenci-two", "codex-cenci-three"} {
		if !anyLineContains(lines, "exec -u dev "+name+" /bin/bash -c") {
			t.Errorf("expected a plugin refresh exec for %q, got calls:\n%s", name, strings.Join(lines, "\n"))
		}
	}
}

func TestSandboxUpdatePluginsAll_WithExplicitName_UsageError(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--all", "--name", "x")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected --all --name usage error exit 2, got %T %v\n%s", err, err, output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls for --all --name usage error, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUpdatePluginsAll_WithExplicitAgent_UsageError(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--all", "--agent", "codex")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected --all --agent usage error exit 2, got %T %v\n%s", err, err, output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls for --all --agent usage error, got:\n%s", strings.Join(lines, "\n"))
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
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}
	// os/exec keeps the LAST duplicate env key, so these appends override the
	// inherited values; the empty assignments scrub optional passthroughs
	// (and a possible ambient CENCI_SANDBOX) for deterministic argv.
	// FAKE_IMAGE_BASE_VERSION defaults to the fixture's own current BaseTag so
	// pre-existing "image current -> skip build" tests keep passing; tests
	// that want to exercise base-drift staleness override it with their own
	// append (see batchEnv, whose FAKE_IMAGE_BASE_VERSION convention this
	// mirrors).
	env = append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+home,
		"CENCI_SANDBOX_ASSETS="+assets,
		"XDG_RUNTIME_DIR="+xdg,
		"TERM=xterm-256color",
		"TMUX_PANE=%7",
		"COLORTERM=", "CONTEXT7_API_KEY=", "OPENAI_API_KEY=", "CENCI_SANDBOX=",
		"FAKE_VOLUMES=cenci-agent-cli-claude\ncenci-agent-cli-codex\n",
		"FAKE_IMAGE_BASE_VERSION="+tag,
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
		// Codex additionally needs --dangerously-bypass-hook-trust: hook
		// trust lives in the user config layer and provisioning never seeds
		// it, so without the flag the cenci-watch hooks are silently skipped
		// as "pending review" and sandbox sessions never report (#426).
		{"xl", "codex", "gpt-5.6-luna", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust"},
		{"xt", "codex", "gpt-5.6-terra", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust"},
		{"xs", "codex", "gpt-5.6-sol", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust"},
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
			wantTail := tc.agent + "-cenci-default /opt/cenci-agent/current/node_modules/.bin/" + tc.agent + " " + tc.permFlag + " --model " + tc.model
			if !strings.HasSuffix(line, wantTail) {
				t.Errorf("attach argv = %q, want suffix %q", line, wantTail)
			}
			if tc.agent == "claude" && !strings.Contains(line, "-e DISABLE_UPDATES=1") {
				t.Errorf("Claude agent exec did not suppress native updates: %s", line)
			}
			if tc.agent == "codex" && strings.Contains(line, "DISABLE_UPDATES") {
				t.Errorf("Claude-only update env leaked into Codex: %s", line)
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
	if !strings.HasSuffix(line, "/opt/cenci-agent/current/node_modules/.bin/claude --dangerously-skip-permissions --model sonnet") {
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
	if !strings.HasSuffix(line, "/opt/cenci-agent/current/node_modules/.bin/claude --dangerously-skip-permissions --model opus") {
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
	runLine, ok := findLineWithPrefix(lines, "run --name ")
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
		"-e CENCI_AGENT_CLI=/opt/cenci-agent/current/node_modules/.bin/claude",
		fmt.Sprintf("-e HOST_UID=%d", os.Getuid()),
		fmt.Sprintf("-e HOST_GID=%d", os.Getgid()),
		"-e WORKSPACE_SCOPE=legacy",
		"-v " + home + "/Repos:/workspace",
		"-v claude-cenci-home-default:/home/dev",
		"-v cenci-agent-cli-claude:/opt/cenci-agent:ro",
		"-v " + socketDir + ":/run/user/1000/cenci:ro",
		":/usr/local/bin/cenci:ro",
		"-e XDG_RUNTIME_DIR=/run/user/1000",
	} {
		if !strings.Contains(runLine, want) {
			t.Errorf("run argv missing %q:\n%s", want, runLine)
		}
	}
	// Claude lives in the shared read-only volume, not the writable home or a
	// host bind mount.
	if strings.Contains(runLine, ":/usr/local/bin/claude:ro") {
		t.Errorf("run argv must not bind-mount the host claude binary:\n%s", runLine)
	}
	// TMUX_PANE must only travel per exec session (asserted on the attach
	// below): baked into the container-lifetime env it goes stale when the
	// creating pane closes, and reap-orphans would kill PID 1 (#356).
	if strings.Contains(runLine, "TMUX_PANE") {
		t.Errorf("run argv must not carry TMUX_PANE (#356):\n%s", runLine)
	}
	if strings.Contains(runLine, "DISABLE_UPDATES") {
		t.Errorf("DISABLE_UPDATES must be scoped to Claude agent execs, not plugin provisioning:\n%s", runLine)
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
	for _, want := range []string{"-u dev", "-e CENCI_SANDBOX=1", "-e CENCI_SANDBOX_AGENT=claude", "-e TMUX_PANE=%7", "-e DISABLE_UPDATES=1", "/opt/cenci-agent/current/node_modules/.bin/claude"} {
		if !strings.Contains(line, want) {
			t.Errorf("attach argv missing %q:\n%s", want, line)
		}
	}
}

func TestOpen_AbsentSharedVolumeBootstrapsBeforeWorkloadWithoutSecrets(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "FAKE_VOLUMES=", "CENCI_TEST_SENTINEL_SECRET=parent-only-secret")
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open bootstrap: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	updater, workload := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "agent-cli.sh update claude") {
			updater = i
			for _, forbidden := range []string{"/home/dev", "/workspace", "OPENAI_API_KEY", "parent-only-secret", "docker.sock", "/tmp/host-"} {
				if strings.Contains(line, forbidden) {
					t.Errorf("bootstrap updater contains %q: %s", forbidden, line)
				}
			}
		}
		if strings.Contains(line, "--name claude-cenci-default") {
			workload = i
		}
	}
	if updater < 0 || workload < 0 || updater >= workload {
		t.Errorf("updater must complete before workload create; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpen_ExistingSharedVolumePerformsNoUpdaterOrVersionCheck(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open existing volume: %v\n%s", err, output)
	}
	lines := callLogLines(t, callLog)
	if anyLineContains(lines, "agent-cli.sh update") {
		t.Errorf("existing shared volume triggered an updater/version check")
	}
	// The cheap populated-check (finding 3a) still runs against the existing
	// volume; it just never falls through to the updater when it passes.
	if !anyLineContains(lines, "test -x /opt/cenci-agent/current/node_modules/.bin/claude") {
		t.Errorf("expected the populated-check to run against the existing volume, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpen_ExistingButUnpopulatedVolume_FallsThroughToUpdater pins finding
// 3a: a volume that docker/podman auto-created via a concurrent first
// launch's `docker run -v` (or one left behind by a previously failed
// bootstrap) reports as "existing" but is actually empty/broken. The cheap
// populated-check must catch that and fall through to the updater instead of
// mounting a broken CLI into the workload.
func TestOpen_ExistingButUnpopulatedVolume_FallsThroughToUpdater(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "FAKE_AGENT_CHECK_EXIT=1")
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open (unpopulated existing volume): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	updater, workload := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "agent-cli.sh update claude") {
			updater = i
		}
		if strings.HasPrefix(line, "run --name claude-cenci-default") {
			workload = i
		}
	}
	if updater < 0 || workload < 0 || updater >= workload {
		t.Errorf("an existing-but-unpopulated volume must fall through to the updater before workload create; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpen_VolumeInspectionFailureIsNotMissingVolume(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "FAKE_VOLUME_LS_EXIT=17")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected volume infrastructure error, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "volume ls") || anyLineContains(callLogLines(t, callLog), "agent-cli.sh update") {
		t.Errorf("volume inspection failure was treated as an absent volume:\n%s", output)
	}
}

func TestOpen_FirstBootstrapFailureStreamsDiagnosticBeforeWorkloadCreate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env,
		"FAKE_VOLUMES=",
		"FAKE_RUN_STDERR=registry exploded: original diagnostic",
		"FAKE_RUN_EXIT=23",
	)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "registry exploded: original diagnostic") {
		t.Errorf("expected updater's original diagnostic, got:\n%s", output)
	}
	lines := callLogLines(t, callLog)
	if anyLineContains(lines, "--name claude-cenci-default") || anyLineContains(lines, "exec -it") {
		t.Errorf("workload must not be created after bootstrap failure; calls:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpen_ContainerExitsDuringStartup_ReportsStatusAndExitCode pins finding
// 4: a container that exits during startup is surfaced immediately with its
// status/exit code, not degraded into the generic 60-second readiness
// timeout.
func TestOpen_ContainerExitsDuringStartup_ReportsStatusAndExitCode(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "FAKE_READY_EXIT=1", "FAKE_INSPECT_STATE=exited 1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "failed during startup (status exited, exit 1)") {
		t.Errorf("expected the status/exit code in the error, got:\n%s", output)
	}
	if strings.Contains(string(output), "did not become ready within 60 seconds") {
		t.Errorf("expected the immediate startup-failure error, not the generic timeout, got:\n%s", output)
	}
}

// TestOpen_StartupErrorMarkerSurfacedVerbatim pins finding 4: when
// sandbox/entrypoint.sh writes /home/dev/.cenci-agent-startup-error (agent
// CLI path missing/not executable) before exiting non-zero, the launch error
// surfaces that marker's content verbatim instead of falling back to
// container logs or the generic failure message.
func TestOpen_StartupErrorMarkerSurfacedVerbatim(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	const marker = "agent CLI missing at /opt/cenci-agent/current/node_modules/.bin/claude"
	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_READY_EXIT=1",
		"FAKE_INSPECT_STATE=exited 1",
		"FAKE_STARTUP_ERROR="+marker,
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), marker) {
		t.Errorf("expected the startup-error marker content surfaced verbatim, got:\n%s", output)
	}
	if !strings.Contains(string(output), "CENCI-SANDBOX-START-001") {
		t.Errorf("expected the agent-CLI-missing error code CENCI-SANDBOX-START-001, got:\n%s", output)
	}
	if strings.Contains(string(output), "CENCI-SANDBOX-START-002") {
		t.Errorf("expected only the agent-CLI-missing code, not the generic-entrypoint code, got:\n%s", output)
	}
}

// TestOpen_WaitUntilReadyInspectFailure_NoBlankStatusExit pins ticket #473's
// first symptom: when the container is already gone (auto-removed by --rm)
// by the time waitUntilReady exhausts its inspect-error retry budget,
// containerStartupState returns blank status/exit strings alongside its
// error — the outer message must substitute a clear fallback instead of
// interpolating those blanks into "(status , exit )".
func TestOpen_WaitUntilReadyInspectFailure_NoBlankStatusExit(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_READY_EXIT=1",
		"FAKE_CONTAINER_INSPECT_EXIT=1",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if strings.Contains(string(output), "(status , exit )") {
		t.Errorf("expected a fallback in place of blank status/exit, got the literal blank parenthetical:\n%s", output)
	}
	if !strings.Contains(string(output), "(status unknown, exit unknown)") {
		t.Errorf("expected a clear status/exit fallback, got:\n%s", output)
	}
}

// TestOpen_GenericEntrypointFailureSurfacesBootLog pins ticket #473's second
// symptom: an entrypoint failure at a point other than the agent-CLI-missing
// check (so no /home/dev/.cenci-agent-startup-error marker exists) still
// surfaces real diagnostic content — sandbox/entrypoint.sh's teed boot log —
// instead of degrading into startupFailureDetail's fully generic fallback.
// Parametrized over both agents per the ticket's acceptance criteria.
func TestOpen_GenericEntrypointFailureSurfacesBootLog(t *testing.T) {
	cases := []struct {
		name, token string
	}{
		{"claude", "ch"},
		{"codex", "xt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, _ := openTestEnv(t, fakeDir, assets)
			if tc.name == "codex" {
				if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			const bootLog = "boot log: mkdir /workspace/.cenci: permission denied"
			cmd := exec.Command(binaryPath, "open", tc.token)
			cmd.Env = append(env,
				"FAKE_READY_EXIT=1",
				"FAKE_INSPECT_STATE=exited 1",
				"FAKE_BOOT_LOG="+bootLog,
			)
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()

			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 1 {
				t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
			}
			if !strings.Contains(string(output), bootLog) {
				t.Errorf("expected the boot log content surfaced, got:\n%s", output)
			}
			if strings.Contains(string(output), "entrypoint exited before initialization completed") {
				t.Errorf("expected the boot log, not the fully generic fallback, got:\n%s", output)
			}
			if !strings.Contains(string(output), "CENCI-SANDBOX-START-002") {
				t.Errorf("expected the generic-entrypoint error code CENCI-SANDBOX-START-002, got:\n%s", output)
			}
			if strings.Contains(string(output), "CENCI-SANDBOX-START-001") {
				t.Errorf("expected only the generic-entrypoint code, not the agent-CLI-missing code, got:\n%s", output)
			}
		})
	}
}

// TestOpen_GenericEntrypointFailureSurfacesStartupMarker pins the third
// startupFailureDetail precedence step: when no agent-CLI marker AND no boot
// log content exists, the generic EXIT-trap marker
// (/home/dev/.cenci-startup-failed) is still surfaced verbatim rather than
// falling through to the fully generic fallback string.
func TestOpen_GenericEntrypointFailureSurfacesStartupMarker(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	const marker = "generic startup failure: entrypoint trap fired at credential seeding"
	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_READY_EXIT=1",
		"FAKE_INSPECT_STATE=exited 1",
		"FAKE_STARTUP_MARKER="+marker,
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), marker) {
		t.Errorf("expected the generic trap marker content surfaced verbatim, got:\n%s", output)
	}
	if strings.Contains(string(output), "entrypoint exited before initialization completed") {
		t.Errorf("expected the trap marker, not the fully generic fallback, got:\n%s", output)
	}
	if !strings.Contains(string(output), "CENCI-SANDBOX-START-002") {
		t.Errorf("expected the generic-entrypoint error code CENCI-SANDBOX-START-002, got:\n%s", output)
	}
	if strings.Contains(string(output), "CENCI-SANDBOX-START-001") {
		t.Errorf("expected only the generic-entrypoint code, not the agent-CLI-missing code, got:\n%s", output)
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
	runLine, ok := findLineWithPrefix(lines, "run --name ")
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
	if !strings.HasSuffix(line, "/opt/cenci-agent/current/node_modules/.bin/claude --dangerously-skip-permissions --model haiku --resume -p fix the bug") {
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
	runLine, ok := findLineWithPrefix(lines, "run --name ")
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
	runLine, _ := findLineWithPrefix(lines, "run --name ")
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
	runLine, _ := findLineWithPrefix(lines, "run --name ")
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
	runLine, _ := findLineWithPrefix(lines, "run --name ")
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
	// Credential validation happens late, while assembling the workload's own
	// create args, so it never races the shared agent volume's read-only
	// populated-check (EnsureAgentVolume runs unconditionally early in
	// Launch) -- only the workload container itself must never be created.
	if lines := callLogLines(t, callLog); anyLineContains(lines, "run --name ") {
		t.Errorf("expected no workload container run without codex auth, got:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpenCodex_ForwardsProviderKeyPerExecOnly pins Codex's OPENAI_API_KEY
// forwarding to the per-exec-only model OpenCode already uses (#490/#509):
// the env var alone must satisfy the codex auth gate (no ~/.codex/auth.json
// staged) and reach the exec-time attach, but it must never appear in the
// create-time `run --name` args — else it would sit in the container's PID-1
// environ for the container's whole lifetime, readable via `docker inspect`/
// `/proc/1/environ` (#510).
func TestOpenCodex_ForwardsProviderKeyPerExecOnly(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets) // no ~/.codex/auth.json staged

	cmd := exec.Command(binaryPath, "open", "--agent", "codex")
	cmd.Env = append(env, "OPENAI_API_KEY=sk-cenci-test-secret")
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open --agent codex: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(runLine, "OPENAI_API_KEY") {
		t.Errorf("create-time run args must never bake the provider key into the PID-1 environ; got:\n%s", runLine)
	}

	line := attachLine(t, lines)
	if !strings.Contains(line, "-e OPENAI_API_KEY=sk-cenci-test-secret") {
		t.Errorf("attach argv missing per-exec OPENAI_API_KEY forwarding:\n%s", line)
	}
}

// TestOpenOpencode_FreshCreate_PinsBareInvocationAndScopesProviderKeys pins
// #490's launch contract for OpenCode: a bare `opencode` invocation (no
// --dangerously-skip-permissions equivalent — permissions are config-driven
// via the seeded opencode.json), and ANTHROPIC_API_KEY/OPENAI_API_KEY
// forwarded only at exec time (per-session), never baked into the
// container-lifetime create-time env/PID-1 environ.
func TestOpenOpencode_FreshCreate_PinsBareInvocationAndScopesProviderKeys(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--agent", "opencode")
	cmd.Env = append(env, "ANTHROPIC_API_KEY=sk-test-anthropic", "OPENAI_API_KEY=sk-test-openai")
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open --agent opencode: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	for _, forbidden := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if strings.Contains(runLine, forbidden) {
			t.Errorf("create-time run args must never bake provider keys into the PID-1 environ; got %q in:\n%s", forbidden, runLine)
		}
	}

	line := attachLine(t, lines)
	if !strings.HasSuffix(line, "/opt/cenci-agent/current/node_modules/.bin/opencode") {
		t.Errorf("attach argv = %q, want a bare opencode invocation with no permission-skip flag and no forced --model", line)
	}
	for _, want := range []string{"ANTHROPIC_API_KEY=sk-test-anthropic", "OPENAI_API_KEY=sk-test-openai"} {
		if !strings.Contains(line, want) {
			t.Errorf("attach argv missing %q (opencode-only per-exec provider key forwarding):\n%s", want, line)
		}
	}
}

// TestOpenClaude_NeverForwardsProviderKeys pins the opencode-only scoping of
// ANTHROPIC_API_KEY/OPENAI_API_KEY (#490): a Claude launch must never receive
// either provider key, at create time or exec time, even when both are set
// on the host.
func TestOpenClaude_NeverForwardsProviderKeys(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "OPENAI_API_KEY=sk-test-openai", "ANTHROPIC_API_KEY=sk-test-anthropic")
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	line := attachLine(t, lines)
	for _, forbidden := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		if strings.Contains(runLine, forbidden) {
			t.Errorf("claude create-time run args must never carry a provider key (opencode-only forwarding); got %q in:\n%s", forbidden, runLine)
		}
		if strings.Contains(line, forbidden) {
			t.Errorf("claude attach exec args must never carry a provider key (opencode-only forwarding); got %q in:\n%s", forbidden, line)
		}
	}
}

// TestOpenOpencodeWithoutAuth_Exits1 mirrors TestOpenCodexWithoutAuth_Exits1:
// OpenCode has a hard credential requirement (ANTHROPIC_API_KEY,
// OPENAI_API_KEY, or a staged ~/.local/share/opencode/auth.json), so a launch
// with none of those present must fail before ever creating the workload
// container.
func TestOpenOpencodeWithoutAuth_Exits1(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets) // no opencode auth.json, provider keys scrubbed

	cmd := exec.Command(binaryPath, "open", "--agent", "opencode")
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
	if !strings.Contains(string(output), "OpenCode") || !strings.Contains(string(output), "auth") {
		t.Errorf("expected an OpenCode auth error, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); anyLineContains(lines, "run --name ") {
		t.Errorf("expected no workload container run without opencode auth, got:\n%s", strings.Join(lines, "\n"))
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
	if _, ok := findLineWithPrefix(lines, "run --name "); ok {
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

func TestOpen_RunningLegacyContainerRequiresStopAndRelaunch(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch", "--name", "old")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-old\n",
		"FAKE_AGENT_MOUNTS=/workspace|/workspace|true\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected legacy rejection, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "cenci sandbox stop claude-cenci-old") || !strings.Contains(string(output), "then relaunch") {
		t.Errorf("missing precise migration instruction:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("legacy container must not receive an agent session")
	}
}

func TestOpen_RunningContainerInspectFailureIsInfrastructureError(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_CONTAINER_INSPECT_EXIT=17",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected runtime error, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "inspect claude-cenci-default mounts") || strings.Contains(string(output), "predates shared") {
		t.Errorf("runtime inspection failure was misclassified:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("attach must not run after inspection failure")
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
	runLine, ok := findLineWithPrefix(lines, "run --name ")
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
	if !strings.HasSuffix(line, "/opt/cenci-agent/current/node_modules/.bin/codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust --model gpt-5.6-sol") {
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
