package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// -- dual-runtime host-wide/scope-resolving command coverage (#629) --------
//
// On a host with both docker and podman installed, host-wide sandbox
// commands (ls, stop, prune, update-agent, update-plugins --all,
// support-bundle) must enumerate every installed runtime instead of
// collapsing to one preferred runtime, and scope-resolving commands
// (diagnose, scoped update-plugins) must act on every runtime that actually
// owns the target — both, on a same-name collision, never silently one. A
// failed per-runtime query must surface the healthy runtime's output plus a
// stderr error and a non-zero exit, never a silently empty result (AC #4).
// These black-box tests drive the real built `cenci` binary as a subprocess,
// reusing the fakes/env-builders from sandbox_open_test.go/diagnose_test.go/
// support_bundle_test.go (same package).

// -- sandbox ls -------------------------------------------------------------

func TestSandboxLs_DualRuntime_ListsBothRuntimesWithRuntimeColumn(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	writeFakeDocker(t, dir, "docker", dockerLog, "claude-cenci-dind\tUp 2 hours\tcenci-sandbox:latest\n")
	writeFakeDocker(t, dir, "podman", podmanLog, "codex-cenci-other\tUp 1 hour\tcenci-sandbox:latest\n")

	cmd := exec.Command(binaryPath, "sandbox", "ls")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox ls: %v\n%s", err, output)
	}

	lines := strings.Split(string(output), "\n")
	var header string
	for _, l := range lines {
		if strings.Contains(l, "NAME") {
			header = l
			break
		}
	}
	if !strings.Contains(header, "RUNTIME") {
		t.Errorf("expected the ls header to include a RUNTIME column, got %q (full output:\n%s)", header, output)
	}
	dockerRow, ok := findLineWithPrefix(lines, "claude-cenci-dind")
	if !ok || !strings.Contains(dockerRow, "docker") {
		t.Errorf("expected the docker-only DinD container listed and tagged docker even though podman is also installed, got:\n%s", output)
	}
	podmanRow, ok := findLineWithPrefix(lines, "codex-cenci-other")
	if !ok || !strings.Contains(podmanRow, "podman") {
		t.Errorf("expected the podman container listed and tagged podman, got:\n%s", output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "ps -a") {
		t.Errorf("expected docker to be queried too, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "ps -a") {
		t.Errorf("expected podman to be queried too, got podman calls:\n%s", podmanCalls)
	}
}

// TestSandboxLs_SameNameUnderBothRuntimes_ListsTwoDistinctRuntimeTaggedRows
// pins AC #3: same-name resources on both runtimes must remain
// distinguishable rather than silently deduplicated.
func TestSandboxLs_SameNameUnderBothRuntimes_ListsTwoDistinctRuntimeTaggedRows(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	row := "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n"
	writeFakeDocker(t, dir, "docker", dockerLog, row)
	writeFakeDocker(t, dir, "podman", podmanLog, row)

	cmd := exec.Command(binaryPath, "sandbox", "ls")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox ls: %v\n%s", err, output)
	}

	count := 0
	for _, l := range strings.Split(string(output), "\n") {
		if strings.Contains(l, "claude-cenci-agentstack") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected two distinct rows for the same-named container under both runtimes, got %d; output:\n%s", count, output)
	}
	if !strings.Contains(string(output), "docker") || !strings.Contains(string(output), "podman") {
		t.Errorf("expected both runtimes represented distinctly (no silent dedup), got:\n%s", output)
	}
}

// TestSandboxLs_OneRuntimeFails_HealthyRuntimeStillListedNonZeroExit pins AC
// #4: a failed runtime query must never be silently read as "nothing
// found" merely because the other runtime succeeded.
func TestSandboxLs_OneRuntimeFails_HealthyRuntimeStillListedNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	exectest.WriteExecutable(t, filepath.Join(dir, "docker"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+exectest.ShellQuote(dockerLog)+"\nexit 1\n")
	writeFakeDocker(t, dir, "podman", podmanLog, "codex-cenci-other\tUp 1 hour\tcenci-sandbox:latest\n")

	cmd := exec.Command(binaryPath, "sandbox", "ls")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected sandbox ls to exit non-zero when one runtime's query fails")
	}
	out := string(output)
	if !strings.Contains(out, "codex-cenci-other") {
		t.Errorf("expected the healthy podman runtime's container still listed, got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "docker") {
		t.Errorf("expected the docker failure to be surfaced in the output, got:\n%s", out)
	}
}

// -- sandbox stop -------------------------------------------------------------

// TestSandboxStop_DualRuntime_StopsDockerContainerWhenPodmanAlsoInstalled
// pins AC #1: a Docker DinD container must be stoppable when podman is also
// installed, and the Decisions-locked output tag "stopped <name> (<runtime>)".
func TestSandboxStop_DualRuntime_StopsDockerContainerWhenPodmanAlsoInstalled(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	writeFakeDocker(t, dir, "docker", dockerLog, "claude-cenci-dind\tUp 2 hours\tcenci-sandbox:latest\n")
	writeFakeDocker(t, dir, "podman", podmanLog, "codex-cenci-other\tUp 1 hour\tcenci-sandbox:latest\n")

	cmd := exec.Command(binaryPath, "sandbox", "stop")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox stop: %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "stopped claude-cenci-dind (docker)") {
		t.Errorf("expected the docker container stopped and tagged with its runtime, got:\n%s", out)
	}
	if !strings.Contains(out, "stopped codex-cenci-other (podman)") {
		t.Errorf("expected the podman container stopped and tagged with its runtime, got:\n%s", out)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "stop claude-cenci-dind") {
		t.Errorf("expected docker stop invoked, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "stop codex-cenci-other") {
		t.Errorf("expected podman stop invoked, got podman calls:\n%s", podmanCalls)
	}
}

func TestSandboxStop_FilterNarrowsAcrossBothRuntimes(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	writeFakeDocker(t, dir, "docker", dockerLog, "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n")
	writeFakeDocker(t, dir, "podman", podmanLog, "codex-cenci-otherrepo\tUp 1 hour\tcenci-sandbox:latest\n")

	cmd := exec.Command(binaryPath, "sandbox", "stop", "agentstack")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox stop agentstack: %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "stopped claude-cenci-agentstack (docker)") {
		t.Errorf("expected the matching docker container stopped, got:\n%s", out)
	}
	if strings.Contains(out, "codex-cenci-otherrepo") {
		t.Errorf("expected the non-matching podman container left alone, got:\n%s", out)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if strings.Contains(string(podmanCalls), "stop codex-cenci-otherrepo") {
		t.Errorf("expected podman stop never invoked for the filtered-out container, got podman calls:\n%s", podmanCalls)
	}
}

// TestSandboxStop_OneRuntimeFails_HealthyRuntimeStillStoppedNonZeroExit pins
// AC #4 for stop.
func TestSandboxStop_OneRuntimeFails_HealthyRuntimeStillStoppedNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	exectest.WriteExecutable(t, filepath.Join(dir, "docker"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+exectest.ShellQuote(dockerLog)+"\nexit 1\n")
	writeFakeDocker(t, dir, "podman", podmanLog, "codex-cenci-other\tUp 1 hour\tcenci-sandbox:latest\n")

	cmd := exec.Command(binaryPath, "sandbox", "stop")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected sandbox stop to exit non-zero when one runtime's query fails")
	}
	out := string(output)
	if !strings.Contains(out, "stopped codex-cenci-other (podman)") {
		t.Errorf("expected the healthy podman runtime's container still stopped, got:\n%s", out)
	}
}

// -- sandbox prune -------------------------------------------------------------

func TestSandboxPrune_DualRuntime_PrunesBothRuntimes(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "prune")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_IMAGES_DOCKER=cenci-sandbox-base:docker-old\ncenci-sandbox-base:latest\ncenci-sandbox-base:"+tag+"\n",
		"FAKE_IMAGES_PODMAN=cenci-sandbox-base:podman-old\ncenci-sandbox-base:latest\ncenci-sandbox-base:"+tag+"\n",
		"FAKE_PS_DOCKER=claude-cenci-docker-old\n",
		"FAKE_PS_PODMAN=codex-cenci-podman-old\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox prune: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "rmi cenci-sandbox-base:docker-old") {
		t.Errorf("expected docker's superseded base tag removed, got docker calls:\n%s", dockerCalls)
	}
	if !strings.Contains(string(dockerCalls), "rm claude-cenci-docker-old") {
		t.Errorf("expected docker's stopped container removed, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "rmi cenci-sandbox-base:podman-old") {
		t.Errorf("expected podman's superseded base tag removed, got podman calls:\n%s", podmanCalls)
	}
	if !strings.Contains(string(podmanCalls), "rm codex-cenci-podman-old") {
		t.Errorf("expected podman's stopped container removed, got podman calls:\n%s", podmanCalls)
	}
}

// TestSandboxPrune_OneRuntimeFails_HealthyRuntimeStillPrunedNonZeroExit pins
// AC #4 for prune: docker's `images` listing fails outright, but podman must
// still be pruned, with the failure surfaced and a non-zero exit.
func TestSandboxPrune_OneRuntimeFails_HealthyRuntimeStillPrunedNonZeroExit(t *testing.T) {
	fakeDir := t.TempDir()
	_, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "prune")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_INSPECT_EXIT_DOCKER=1", // docker's `images` listing fails
		"FAKE_PS_PODMAN=codex-cenci-old\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected sandbox prune to exit non-zero when one runtime's images listing fails")
	}

	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "rm codex-cenci-old") {
		t.Errorf("expected the healthy podman runtime still pruned, got podman calls:\n%s", podmanCalls)
	}
	if !strings.Contains(strings.ToLower(string(output)), "docker") {
		t.Errorf("expected the docker failure surfaced on output, got:\n%s", output)
	}
}

// TestSandboxPrune_DualRuntime_SharedReaderAppliesSinglePipedConfirmationToBothRuntimePasses
// pins the Should-Fix from #629's second review pass: the dual-runtime
// host-wide prune loop must share one bufio.Reader across every runtime's
// Prune call, so a piped confirmation answer meant for the second runtime's
// pass isn't silently swallowed by the first pass's own independent
// bufio.Reader over the same shared os.Stdin. Before the fix, each
// WithRuntime(rt).Prune call constructed its own fresh
// bufio.NewReader(e.Stdin); with "y\ny\n" piped in as a single upfront block,
// the first runtime's reader would read-ahead and buffer BOTH lines
// internally, returning only the first "y\n" via ReadString and trapping the
// second inside its own now-discarded reader — so the second runtime's fresh
// reader construction observed EOF and silently default-denied removal, even
// though a genuine second confirmation line was piped in.
func TestSandboxPrune_DualRuntime_SharedReaderAppliesSinglePipedConfirmationToBothRuntimePasses(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "prune", "--volumes")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_IMAGES_DOCKER=cenci-sandbox-base:latest\ncenci-sandbox-base:"+tag+"\n",
		"FAKE_IMAGES_PODMAN=cenci-sandbox-base:latest\ncenci-sandbox-base:"+tag+"\n",
		"FAKE_VOLUMES_DOCKER=claude-cenci-home-docker\n",
		"FAKE_VOLUMES_PODMAN=claude-cenci-home-podman\n",
	)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader("y\ny\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox prune --volumes: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "volume rm claude-cenci-home-docker") {
		t.Errorf("expected docker's confirmed volume removed (first runtime pass); docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "volume rm claude-cenci-home-podman") {
		t.Errorf("expected podman's confirmed volume removed too (a fresh bufio.Reader per runtime pass would lose this second confirmation to the first pass's read-ahead buffering); podman calls:\n%s", podmanCalls)
	}
}

// -- sandbox update-agent -------------------------------------------------------------

// TestSandboxUpdateAgent_DualRuntime_UpdatesOnlyTheRuntimeThatAlreadyHasTheVolume
// pins Q4: update every runtime where the shared volume already exists —
// never a runtime that doesn't have it.
func TestSandboxUpdateAgent_DualRuntime_UpdatesOnlyTheRuntimeThatAlreadyHasTheVolume(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES_DOCKER=cenci-agent-cli-claude\n",
		"FAKE_VOLUMES_PODMAN=",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "agent-cli.sh update claude") {
		t.Errorf("expected the volume updated under docker (the runtime that already has it), got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if strings.Contains(string(podmanCalls), "agent-cli.sh update") {
		t.Errorf("expected podman (which doesn't have the volume) left untouched, got podman calls:\n%s", podmanCalls)
	}
}

// TestSandboxUpdateAgent_DualRuntime_BootstrapsInPreferredRuntimeWhenAbsentEverywhere
// pins Q4's fallback: bootstrap in the preferred (podman-first) runtime only
// when the volume exists nowhere.
func TestSandboxUpdateAgent_DualRuntime_BootstrapsInPreferredRuntimeWhenAbsentEverywhere(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES_DOCKER=",
		"FAKE_VOLUMES_PODMAN=",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent: %v\n%s", err, output)
	}

	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "agent-cli.sh update claude") {
		t.Errorf("expected the volume bootstrapped in the preferred (podman-first) runtime when absent everywhere, got podman calls:\n%s", podmanCalls)
	}
	dockerCalls, _ := os.ReadFile(dockerLog)
	if strings.Contains(string(dockerCalls), "agent-cli.sh update") {
		t.Errorf("expected docker left untouched (podman is preferred), got docker calls:\n%s", dockerCalls)
	}
	// Must actually have checked docker for the volume too (not merely
	// defaulted to the single preferred runtime while never looking at
	// docker at all) — otherwise "podman updated, docker untouched" would
	// be indistinguishable from the old single-preferred-runtime behavior
	// that never resolves ownership in the first place.
	if !strings.Contains(string(dockerCalls), "volume ls") {
		t.Errorf("expected docker to be queried for the shared volume too (to establish it's absent everywhere) before bootstrapping in podman, got docker calls:\n%s", dockerCalls)
	}
}

// TestSandboxUpdateAgent_DualRuntime_BothRuntimesHaveVolume_UpdatesBoth pins
// Q4's "update every runtime where it already exists" for the same-name
// collision case: both runtimes have the volume, so both get updated.
func TestSandboxUpdateAgent_DualRuntime_BothRuntimesHaveVolume_UpdatesBoth(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES_DOCKER=cenci-agent-cli-claude\n",
		"FAKE_VOLUMES_PODMAN=cenci-agent-cli-claude\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "agent-cli.sh update claude") {
		t.Errorf("expected docker's copy updated too, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "agent-cli.sh update claude") {
		t.Errorf("expected podman's copy updated, got podman calls:\n%s", podmanCalls)
	}
}

// TestSandboxUpdateAgent_OneRuntimeVolumeQueryFails_HealthyRuntimeStillUpdatedNonZeroExit
// pins the Should-Fix from #629's second review pass: RuntimesWithVolume
// returns partial owners plus a joined error when only one runtime's `volume
// ls` query fails — docker's query fails outright here, while podman's
// query genuinely resolves ownership. update-agent must still act on
// podman's resolved ownership (not abort with zero action taken merely
// because docker's query errored), surfacing the enumeration failure and a
// non-zero exit.
func TestSandboxUpdateAgent_OneRuntimeVolumeQueryFails_HealthyRuntimeStillUpdatedNonZeroExit(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUME_LS_EXIT_DOCKER=1", // docker's volume ls query fails outright
		"FAKE_VOLUMES_PODMAN=cenci-agent-cli-claude\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected sandbox update-agent to exit non-zero when one runtime's volume ls query fails, output:\n%s", output)
	}

	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "agent-cli.sh update claude") {
		t.Errorf("expected the healthy podman runtime (which genuinely owns the volume) to still be updated despite docker's enumeration failure, got podman calls:\n%s", podmanCalls)
	}
	dockerCalls, _ := os.ReadFile(dockerLog)
	if strings.Contains(string(dockerCalls), "agent-cli.sh update") {
		t.Errorf("expected docker left untouched (its query failed, so it was never resolved as an owner), got docker calls:\n%s", dockerCalls)
	}
	if !strings.Contains(strings.ToLower(string(output)), "docker") {
		t.Errorf("expected the docker enumeration failure surfaced on output, got:\n%s", output)
	}
}

// TestSandboxUpdateAgentUnpin_NoVolumeAnywhere_RunsInPreferredRuntime pins
// #709: `--unpin` follows the same owner-resolution as a bare update-agent —
// when the volume exists nowhere, the unpin-then-update sequence runs once,
// in the preferred (podman-first) runtime, never docker.
func TestSandboxUpdateAgentUnpin_NoVolumeAnywhere_RunsInPreferredRuntime(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude", "--unpin")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES_DOCKER=",
		"FAKE_VOLUMES_PODMAN=",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent --unpin: %v\n%s", err, output)
	}

	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "agent-cli.sh unpin claude") || !strings.Contains(string(podmanCalls), "agent-cli.sh update claude") {
		t.Errorf("expected the unpin-then-update sequence to run in the preferred (podman-first) runtime, got podman calls:\n%s", podmanCalls)
	}
	dockerCalls, _ := os.ReadFile(dockerLog)
	if strings.Contains(string(dockerCalls), "agent-cli.sh unpin") || strings.Contains(string(dockerCalls), "agent-cli.sh update") {
		t.Errorf("expected docker left untouched (podman is preferred), got docker calls:\n%s", dockerCalls)
	}
}

// TestSandboxUpdateAgent_DualRuntime_MixedRefusalAndFailure_Exits1 pins
// #709's refusal-aware exit classification: exit 2 only fires when EVERY
// collected error is a pin refusal. Here docker refuses (pinned, exit 2) but
// podman fails for an ordinary reason (exit 1) — the mix must exit 1, not 2.
func TestSandboxUpdateAgent_DualRuntime_MixedRefusalAndFailure_Exits1(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES_DOCKER=cenci-agent-cli-claude\n",
		"FAKE_VOLUMES_PODMAN=cenci-agent-cli-claude\n",
		"FAKE_RUN_EXIT_DOCKER=2",
		"FAKE_RUN_EXIT_PODMAN=1",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a mix of pin-refusal and ordinary failure to exit 1 (not every owner refused), got %T %v\n%s", err, err, output)
	}
}

// TestSandboxUpdateAgent_DualRuntime_BothRefused_Exits2 pins #709's
// refusal-aware exit classification's other half: when EVERY collected error
// is a pin refusal (both runtimes' volumes are pinned), the aggregate exit
// is 2, not the generic 1.
func TestSandboxUpdateAgent_DualRuntime_BothRefused_Exits2(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES_DOCKER=cenci-agent-cli-claude\n",
		"FAKE_VOLUMES_PODMAN=cenci-agent-cli-claude\n",
		"FAKE_RUN_EXIT_DOCKER=2",
		"FAKE_RUN_EXIT_PODMAN=2",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected both owners refusing (pinned) to exit 2, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "--unpin") || !strings.Contains(string(output), "--version") {
		t.Errorf("expected the refusal message to name both --unpin and --version, got:\n%s", output)
	}
}

// TestSandboxUpdateAgent_DualRuntime_VolumeLsFailurePlusPodmanRefusal_Exits1
// pins #709's classification edge case: docker's `volume ls` enumeration
// query fails outright (a generic infrastructure error, never a pin
// refusal), while podman resolves ownership and refuses (pinned, exit 2).
// Since not every collected error is a pin refusal, the aggregate must exit
// 1, not 2.
func TestSandboxUpdateAgent_DualRuntime_VolumeLsFailurePlusPodmanRefusal_Exits1(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--agent", "claude")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUME_LS_EXIT_DOCKER=1", // docker's volume ls query fails outright
		"FAKE_VOLUMES_PODMAN=cenci-agent-cli-claude\n",
		"FAKE_RUN_EXIT_PODMAN=2",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a volume-ls enumeration failure plus a pin refusal to exit 1 (not every error is a refusal), got %T %v\n%s", err, err, output)
	}
}

// TestSandboxUpdateAgentAll_DualRuntime_SpansAgentsAndRuntimes pins #709:
// --all iterates sandbox.SupportedAgents across every runtime that already
// owns each agent's volume — docker owns claude, podman owns codex — and
// refreshes each only in its owner, never cross-contaminating.
func TestSandboxUpdateAgentAll_DualRuntime_SpansAgentsAndRuntimes(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--all")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES_DOCKER=cenci-agent-cli-claude\n",
		"FAKE_VOLUMES_PODMAN=cenci-agent-cli-codex\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent --all: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "agent-cli.sh update claude --skip-if-pinned") {
		t.Errorf("expected docker's claude volume refreshed, got docker calls:\n%s", dockerCalls)
	}
	if strings.Contains(string(dockerCalls), "agent-cli.sh update codex") || strings.Contains(string(dockerCalls), "agent-cli.sh update opencode") {
		t.Errorf("expected docker never touched for agents it doesn't own, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "agent-cli.sh update codex --skip-if-pinned") {
		t.Errorf("expected podman's codex volume refreshed, got podman calls:\n%s", podmanCalls)
	}
	if strings.Contains(string(podmanCalls), "agent-cli.sh update claude") || strings.Contains(string(podmanCalls), "agent-cli.sh update opencode") {
		t.Errorf("expected podman never touched for agents it doesn't own, got podman calls:\n%s", podmanCalls)
	}
}

// TestSandboxUpdateAgentAll_DualRuntime_SameAgentOwnedByBothRuntimes_RefreshesBoth
// pins #709: when the same agent's volume genuinely exists under both
// runtimes (a same-name collision, using scoped FAKE_VOLUMES_DOCKER/
// FAKE_VOLUMES_PODMAN so ownership isn't a shared-fake artifact), --all
// refreshes every owning runtime's copy, not just one.
func TestSandboxUpdateAgentAll_DualRuntime_SameAgentOwnedByBothRuntimes_RefreshesBoth(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--all")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUMES_DOCKER=cenci-agent-cli-claude\n",
		"FAKE_VOLUMES_PODMAN=cenci-agent-cli-claude\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-agent --all: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "agent-cli.sh update claude --skip-if-pinned") {
		t.Errorf("expected docker's copy of claude refreshed, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "agent-cli.sh update claude --skip-if-pinned") {
		t.Errorf("expected podman's copy of claude refreshed too, got podman calls:\n%s", podmanCalls)
	}
}

// TestSandboxUpdateAgentAll_DualRuntime_PartialSuccess_DockerVolumeLsFails_PodmanStillRefreshedNonZeroExit
// pins #709's partial-success requirement: docker's `volume ls` fails
// outright, but podman's genuinely resolved ownership of claude must still
// be refreshed, with the failure aggregated and a non-zero final exit.
func TestSandboxUpdateAgentAll_DualRuntime_PartialSuccess_DockerVolumeLsFails_PodmanStillRefreshedNonZeroExit(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-agent", "--all")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_VOLUME_LS_EXIT_DOCKER=1", // docker's volume ls query fails outright
		"FAKE_VOLUMES_PODMAN=cenci-agent-cli-claude\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected sandbox update-agent --all to exit non-zero when one runtime's volume ls query fails, output:\n%s", output)
	}

	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "agent-cli.sh update claude --skip-if-pinned") {
		t.Errorf("expected the healthy podman runtime's claude volume still refreshed despite docker's enumeration failure, got podman calls:\n%s", podmanCalls)
	}
	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "volume ls") {
		t.Errorf("expected docker's volume ls to have been attempted (and failed), got docker calls:\n%s", dockerCalls)
	}
	if !strings.Contains(strings.ToLower(string(output)), "docker") {
		t.Errorf("expected the docker enumeration failure surfaced on output, got:\n%s", output)
	}
}

// -- sandbox update-plugins -------------------------------------------------------------

// TestSandboxUpdatePlugins_Scoped_SameContainerRunningUnderBothRuntimes_UpdatesBoth
// pins Q3: a scoped update-plugins run resolves every runtime that owns the
// target container and acts on each in sequence — never silently one.
func TestSandboxUpdatePlugins_Scoped_SameContainerRunningUnderBothRuntimes_UpdatesBoth(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_PS_DOCKER=claude-cenci-default\n",
		"FAKE_PS_PODMAN=claude-cenci-default\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-plugins: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "exec -u dev claude-cenci-default") {
		t.Errorf("expected docker's running copy refreshed too, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "exec -u dev claude-cenci-default") {
		t.Errorf("expected podman's running copy refreshed, got podman calls:\n%s", podmanCalls)
	}
}

// TestSandboxUpdatePlugins_Scoped_OneOwnerFails_HealthyOwnerStillUpdatedNonZeroExit
// pins the Must Fix from #629's review: in the same-name collision case
// (both runtimes own the target container), a failure on one owning
// runtime's EnsureImage/UpdatePlugins must not prevent the operation from
// still being attempted — and completing — against the other owning
// runtime, matching the plan's locked Q3 decision ("act on both in
// sequence... report both results"). Before the fix, the per-owner loop
// called os.Exit(1) immediately inside the loop body on the first failure,
// so podman's copy was never even attempted.
func TestSandboxUpdatePlugins_Scoped_OneOwnerFails_HealthyOwnerStillUpdatedNonZeroExit(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_PS_DOCKER=claude-cenci-default\n",
		"FAKE_PS_PODMAN=claude-cenci-default\n",
		"FAKE_INSPECT_EXIT_DOCKER=1", // docker's EnsureImage (images listing) fails
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected sandbox update-plugins to exit non-zero when one owning runtime's EnsureImage fails")
	}

	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "exec -u dev claude-cenci-default") {
		t.Errorf("expected the healthy podman owner's copy still refreshed despite docker's failure, got podman calls:\n%s", podmanCalls)
	}
	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "images") {
		t.Errorf("expected docker's EnsureImage to have been attempted (and failed), got docker calls:\n%s", dockerCalls)
	}
	if strings.Contains(string(dockerCalls), "exec -u dev claude-cenci-default") {
		t.Errorf("expected docker's UpdatePlugins never reached after its EnsureImage failed, got docker calls:\n%s", dockerCalls)
	}
	if !strings.Contains(strings.ToLower(string(output)), "docker") {
		t.Errorf("expected the docker failure surfaced on output, got:\n%s", output)
	}
}

// TestSandboxUpdatePlugins_Scoped_OneRuntimeContainerQueryFails_HealthyRuntimeStillUpdatedNonZeroExit
// pins the Should-Fix from #629's second review pass: RuntimesWithContainer
// returns partial owners plus a joined error when only one runtime's `ps -a`
// query fails — docker's query fails outright here, while podman's query
// genuinely resolves ownership of the running container. update-plugins
// must still act on podman's resolved ownership (not abort with zero action
// taken merely because docker's enumeration query errored), surfacing the
// enumeration failure and a non-zero exit.
func TestSandboxUpdatePlugins_Scoped_OneRuntimeContainerQueryFails_HealthyRuntimeStillUpdatedNonZeroExit(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_PS_EXIT_DOCKER=1", // docker's ps -a container-listing query fails outright
		"FAKE_PS_PODMAN=claude-cenci-default\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected sandbox update-plugins to exit non-zero when one runtime's container query fails, output:\n%s", output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "ps -a") {
		t.Errorf("expected docker's container listing to have been attempted (and failed), got docker calls:\n%s", dockerCalls)
	}
	if strings.Contains(string(dockerCalls), "exec -u dev claude-cenci-default") {
		t.Errorf("expected docker never resolved as an owner (its enumeration query failed), got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "exec -u dev claude-cenci-default") {
		t.Errorf("expected the healthy podman owner (resolved despite docker's enumeration failure) to still be refreshed, got podman calls:\n%s", podmanCalls)
	}
	if !strings.Contains(strings.ToLower(string(output)), "docker") {
		t.Errorf("expected the docker enumeration failure surfaced on output, got:\n%s", output)
	}
}

// TestSandboxUpdatePluginsAll_DualRuntime_SweepsBothRuntimes is a CLI-level
// smoke test for the `--all` flag wiring atop RefreshRunningPlugins' host-wide
// sweep (engine-level coverage lives in
// internal/sandbox/launcher/dual_runtime_test.go).
func TestSandboxUpdatePluginsAll_DualRuntime_SweepsBothRuntimes(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--all")
	cmd.Env = append(batchEnv(t, fakeDir, assets),
		"FAKE_PS_DOCKER=claude-cenci-agentstack\n",
		"FAKE_PS_PODMAN=codex-cenci-otherrepo\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-plugins --all: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "exec -u dev claude-cenci-agentstack") {
		t.Errorf("expected the docker-owned running container refreshed, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "exec -u dev codex-cenci-otherrepo") {
		t.Errorf("expected the podman-owned running container refreshed, got podman calls:\n%s", podmanCalls)
	}
}

// -- diagnose -------------------------------------------------------------

// TestDiagnose_Collision_SameContainerUnderBothRuntimes_ActsOnBothReportsBoth
// pins Q3 for diagnose: with the same-named container under both runtimes,
// diagnose acts on both and reports both (one labeled report per owning
// runtime) — not a usage error, no disambiguation prompt.
func TestDiagnose_Collision_SameContainerUnderBothRuntimes_ActsOnBothReportsBoth(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)
	home := t.TempDir()
	xdg := t.TempDir()
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "diagnose", "default")
	cmd.Env = append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+home,
		"CENCI_SANDBOX_ASSETS="+assets,
		"XDG_RUNTIME_DIR="+xdg,
		"FAKE_VOLUMES=cenci-agent-cli-claude\ncenci-agent-cli-codex\n",
		"FAKE_IMAGE_BASE_VERSION="+tag,
		"FAKE_PS_DOCKER=claude-cenci-default\n",
		"FAKE_PS_PODMAN=claude-cenci-default\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci diagnose default: %v\n%s", err, output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "inspect") {
		t.Errorf("expected docker's copy to be diagnosed too, got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "inspect") {
		t.Errorf("expected podman's copy to be diagnosed, got podman calls:\n%s", podmanCalls)
	}
	out := string(output)
	if !strings.Contains(out, "docker") || !strings.Contains(out, "podman") {
		t.Errorf("expected the report to label each owner's runtime, got:\n%s", out)
	}
}

// TestDiagnose_OneRuntimeContainerQueryFails_HealthyRuntimeStillDiagnosedNonZeroExit
// pins the Should-Fix from #629's second review pass: RuntimesWithContainer
// returns partial owners plus a joined error when only one runtime's `ps -a`
// query fails — docker's query fails outright here, while podman's query
// genuinely resolves ownership of the running container. diagnose must
// still act on podman's resolved ownership (not abort with zero action
// taken merely because docker's enumeration query errored), surfacing the
// enumeration failure and a non-zero exit.
func TestDiagnose_OneRuntimeContainerQueryFails_HealthyRuntimeStillDiagnosedNonZeroExit(t *testing.T) {
	fakeDir := t.TempDir()
	dockerLog, podmanLog := writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)
	home := t.TempDir()
	xdg := t.TempDir()
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "diagnose", "default")
	cmd.Env = append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+home,
		"CENCI_SANDBOX_ASSETS="+assets,
		"XDG_RUNTIME_DIR="+xdg,
		"FAKE_VOLUMES=cenci-agent-cli-claude\ncenci-agent-cli-codex\n",
		"FAKE_IMAGE_BASE_VERSION="+tag,
		"FAKE_PS_EXIT_DOCKER=1", // docker's ps -a container-listing query fails outright
		"FAKE_PS_PODMAN=claude-cenci-default\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected cenci diagnose to exit non-zero when one runtime's container query fails, output:\n%s", output)
	}

	dockerCalls, _ := os.ReadFile(dockerLog)
	if !strings.Contains(string(dockerCalls), "ps") {
		t.Errorf("expected docker's container listing to have been attempted (and failed), got docker calls:\n%s", dockerCalls)
	}
	podmanCalls, _ := os.ReadFile(podmanLog)
	if !strings.Contains(string(podmanCalls), "inspect") {
		t.Errorf("expected the healthy podman owner (resolved despite docker's enumeration failure) to still be diagnosed, got podman calls:\n%s", podmanCalls)
	}
	if !strings.Contains(strings.ToLower(string(output)), "docker") {
		t.Errorf("expected the docker enumeration failure surfaced on output, got:\n%s", output)
	}
}

// -- support-bundle -------------------------------------------------------------

// TestSupportBundle_Collision_SameNameContainersUnderBothRuntimes_BothAppearAsDistinctEntries
// pins Q3's "acts on both, reports both" for support-bundle: same-name
// containers under both runtimes must both appear as distinct bundle
// entries, each identifiable by its owning runtime — never silently
// deduplicated to one entry.
func TestSupportBundle_Collision_SameNameContainersUnderBothRuntimes_BothAppearAsDistinctEntries(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimePair(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)
	env = append(env,
		"FAKE_PS_DOCKER=claude-cenci-myrepo\tUp 2 hours\tcenci-sandbox:latest\n",
		"FAKE_PS_PODMAN=claude-cenci-myrepo\tUp 2 hours\tcenci-sandbox:latest\n",
	)

	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "-o", out, "-y")
	cmd.Env = env
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	entries := extractBundle(t, out)
	var diagnoseEntries []string
	for name := range entries {
		if strings.HasPrefix(name, "diagnose-") && strings.Contains(name, "claude-cenci-myrepo") {
			diagnoseEntries = append(diagnoseEntries, name)
		}
	}
	sort.Strings(diagnoseEntries)
	if len(diagnoseEntries) != 2 {
		t.Fatalf("expected two distinct diagnose entries for the colliding container name (one per runtime), got %v; all entries: %v", diagnoseEntries, entryNames(entries))
	}
	if diagnoseEntries[0] == diagnoseEntries[1] {
		t.Fatalf("expected the two colliding entries to have distinct, runtime-disambiguated filenames, got the same name twice: %v", diagnoseEntries)
	}
	for _, name := range diagnoseEntries {
		if !strings.Contains(name, "docker") && !strings.Contains(name, "podman") {
			t.Errorf("expected the entry filename to identify its owning runtime, got %q", name)
		}
	}
	if !strings.Contains(string(output), "Containers found: 2") {
		t.Errorf("expected the manifest to count both colliding entries distinctly (Containers found: 2), got:\n%s", output)
	}
}
