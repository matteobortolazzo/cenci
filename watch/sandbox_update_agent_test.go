package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

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
		// #709: --all sweeps every existing agent-CLI volume across every
		// runtime and cannot be combined with an explicitly-set --agent,
		// --version, or --unpin (mirrors update-plugins --all's guard).
		{"sandbox", "update-agent", "--all", "--agent", "codex"},
		{"sandbox", "update-agent", "--all", "--version", "1.2.3"},
		{"sandbox", "update-agent", "--all", "--unpin"},
		// #709: --unpin clears the pin and updates to latest, so a
		// simultaneous --version (which would instead re-pin to that
		// version) is a conflicting-input usage error.
		{"sandbox", "update-agent", "--unpin", "--version", "1.2.3"},
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

// TestSandboxUpdateAgent_PinnedRefusal_Exits2WithMessage pins #709's pin-gate
// propagation: a bare `update-agent` against a pinned shared volume must
// surface the isolated updater's exit-2 refusal as the CLI's own exit code
// (not the generic exit-1 "some error occurred" every other updater failure
// gets), with the refusal message naming both escape hatches.
func TestSandboxUpdateAgent_PinnedRefusal_Exits2WithMessage(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_RUN_EXIT=2")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected the pin refusal to propagate as exit 2, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "--unpin") || !strings.Contains(string(output), "--version") {
		t.Errorf("expected the refusal message to name both --unpin and --version, got:\n%s", output)
	}
}

// TestSandboxUpdateAgent_OrdinaryUpdaterFailure_Exits1 is the sibling
// baseline to the pin-refusal case: a non-pin-related updater failure (exit
// 1 from agent-cli.sh, e.g. a network error) must still exit 1, proving the
// new refusal-aware exit classification doesn't broaden exit-2 to every
// updater failure.
func TestSandboxUpdateAgent_OrdinaryUpdaterFailure_Exits1(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_RUN_EXIT=1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected an ordinary updater failure to exit 1, got %T %v\n%s", err, err, output)
	}
}

// TestSandboxUpdateAgent_Unpin_RunsUnpinBeforeUpdate pins #709's --unpin
// sequence: `agent-cli.sh unpin <agent>` runs (hardened the same way as the
// updater, but network-isolated and with no version argument) strictly
// before the plain `agent-cli.sh update <agent>` (no version, no
// --skip-if-pinned — the pin is already cleared).
func TestSandboxUpdateAgent_Unpin_RunsUnpinBeforeUpdate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude", "--unpin")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent --unpin: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	unpinLine := "run --rm --user root --cap-drop=ALL --security-opt=no-new-privileges --network none --entrypoint /bin/bash -v cenci-agent-cli-claude:/opt/cenci-agent cenci-sandbox:latest /usr/local/bin/lib/agent-cli.sh unpin claude"
	updateLine := "run --rm --user root --cap-drop=ALL --security-opt=no-new-privileges --entrypoint /bin/bash -v cenci-agent-cli-claude:/opt/cenci-agent cenci-sandbox:latest /usr/local/bin/lib/agent-cli.sh update claude"
	unpinIdx, updateIdx := -1, -1
	for i, l := range lines {
		if l == unpinLine {
			unpinIdx = i
		}
		if l == updateLine {
			updateIdx = i
		}
	}
	if unpinIdx < 0 {
		t.Fatalf("expected the exact hardened unpin call [%s], got calls:\n%s", unpinLine, strings.Join(lines, "\n"))
	}
	if updateIdx < 0 {
		t.Fatalf("expected the exact no-version, no-flag update call [%s], got calls:\n%s", updateLine, strings.Join(lines, "\n"))
	}
	if unpinIdx >= updateIdx {
		t.Errorf("expected unpin to run before update, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

// TestSandboxUpdateAgent_UnpinFailure_SkipsUpdateExits1 pins #709: when the
// unpin call itself fails for an owner, that owner's update must never run
// (an un-cleared pin would make the following plain `update` re-refuse), and
// the overall command exits 1.
func TestSandboxUpdateAgent_UnpinFailure_SkipsUpdateExits1(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude", "--unpin")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_AGENT_UNPIN_EXIT=1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected an unpin failure to exit 1, got %T %v\n%s", err, err, output)
	}
	lines := callLogLines(t, callLog)
	if !anyLineContains(lines, "agent-cli.sh unpin claude") {
		t.Errorf("expected the unpin call to have been attempted, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "agent-cli.sh update claude") {
		t.Errorf("expected no update call after a failed unpin, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

// TestSandboxUpdateAgent_All_NeverBootstraps_OnlyRefreshesExistingVolume pins
// #709: --all refreshes only agent-CLI volumes that already exist, passing
// --skip-if-pinned, and never bootstraps a volume for an agent that has none
// anywhere. writeScriptedRuntimes puts both a docker and a podman fake on
// PATH sharing one unscoped FAKE_VOLUMES, so both fakes genuinely report
// owning the claude volume here — this pins that --all refreshes every
// runtime that owns a volume (not just one), while codex/opencode (which
// neither fake reports owning) are still never bootstrapped.
func TestSandboxUpdateAgent_All_NeverBootstraps_OnlyRefreshesExistingVolume(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--all")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_VOLUMES=cenci-agent-cli-claude\ncenci-agent-cli-opencode\n")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent --all: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	count := 0
	for _, l := range lines {
		if strings.Contains(l, "agent-cli.sh update") {
			count++
			if !strings.HasSuffix(l, "agent-cli.sh update claude --skip-if-pinned") {
				t.Errorf("expected every updater call to target claude with --skip-if-pinned, got: %s", l)
			}
		}
	}
	if count != 2 {
		t.Fatalf("expected exactly two updater calls (claude in both docker and podman, which both own the volume; codex/opencode never bootstrapped), got %d; calls:\n%s", count, strings.Join(lines, "\n"))
	}
}

// TestSandboxUpdateAgent_All_NoVolumesAnywhere_ZeroUpdaterRunsExitZero pins
// #709: when no agent-CLI volume exists anywhere, --all is a no-op success
// (never bootstraps anything).
func TestSandboxUpdateAgent_All_NoVolumesAnywhere_ZeroUpdaterRunsExitZero(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--all")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_VOLUMES=")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent --all (no volumes anywhere): %v\n%s", err, output)
	}
	if lines := callLogLines(t, callLog); anyLineContains(lines, "agent-cli.sh update") {
		t.Errorf("expected zero updater runs when no volume exists anywhere, got calls:\n%s", strings.Join(lines, "\n"))
	}
}
