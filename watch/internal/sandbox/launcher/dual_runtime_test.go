package launcher

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
)

// This file covers the engine-level half of ticket #629's dual-runtime
// coverage: the new WithRuntime helper ("a small helper to run an engine
// action against a specified runtime", per the plan's engine.go bullet) and
// RefreshRunningPlugins' internal host-wide sweep (the one Engine method that
// genuinely loops over every installed runtime itself, rather than being
// invoked once per runtime by a caller-side loop — see engine.go's
// RefreshRunningPlugins doc comment). Every other host-wide/scope-resolving
// command (ls, stop, prune, update-agent, update-plugins, diagnose,
// support-bundle) is caller-orchestrated (sandbox_cmd.go/diagnose_cmd.go/
// support_bundle_cmd.go loop over sandbox.AvailableRuntimes/
// RuntimesWithContainer/RuntimesWithVolume, reassigning the engine's runtime
// via WithRuntime per iteration) and is covered black-box, against the built
// binary, in sandbox_dual_runtime_test.go instead — asserting the CLI-level
// contract is what actually matters to a user, and doesn't require pinning
// which internal call shape the caller-side loop takes.

// dualRuntimeAgentEngine wires an Engine (Runtime initially "docker") plus
// BOTH a docker and a podman fake on PATH simultaneously, each answering the
// monolith image as current and the given agent's shared volume as already
// existing/populated (mirroring engine_test.go's volumeCheckEngine, but for
// two runtimes at once) — with independent call logs, so a
// WithRuntime-targeting test can assert which single runtime's binary
// actually ran.
func dualRuntimeAgentEngine(t *testing.T) (e *Engine, dockerLog, podmanLog string) {
	t.Helper()
	dir := t.TempDir()
	dockerLog = filepath.Join(dir, "docker-calls.txt")
	podmanLog = filepath.Join(dir, "podman-calls.txt")
	writeAgentRuntimeStub(t, dir, "docker", dockerLog)
	writeAgentRuntimeStub(t, dir, "podman", podmanLog)
	t.Setenv("PATH", dir)

	var out bytes.Buffer
	return &Engine{
		Runtime:  "docker",
		AssetDir: "/assets",
		BaseTag:  "abc123def456",
		Stdin:    strings.NewReader(""),
		Stdout:   &out,
		Stderr:   &out,
	}, dockerLog, podmanLog
}

// writeAgentRuntimeStub writes a fake runtime binary that reports the
// monolith image as present/current, the claude agent-CLI volume as already
// existing and populated — the `agent-cli.sh status claude` probe
// (ticket #710's agent-cli.sh swap; must stay byte-parallel with
// engine_test.go's volumeCheckEngine and sandbox_open_test.go's
// writeScriptedRuntime per watch AGENTS.md #493) reports a fresh
// last_success (computed at fixture-write time, "now") so
// TestEnsureAgentVolume_ViaWithRuntime_BootstrapsUnderSpecifiedRuntimeOnly's
// sibling bootstrap-only tests below keep exercising only the
// populated-check branch, never the new TTL staleness branch — so
// UpdateAgent/EnsureAgentVolume/Diagnose can run against it without a real
// container runtime.
func writeAgentRuntimeStub(t *testing.T, dir, name, callLog string) {
	t.Helper()
	body := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = images ]; then
  printf 'cenci-sandbox:latest\n'
  exit 0
fi
if [ "$1" = image ] && [ "$2" = inspect ]; then
  printf '%%s\n' 'shared-v2|abc123def456'
  exit 0
fi
if [ "$1" = volume ] && [ "$2" = ls ]; then
  printf 'cenci-agent-cli-claude\n'
  exit 0
fi
if [ "$1" = run ]; then
  case "$*" in
  *'agent-cli.sh status'*) printf 'populated=yes\nversion=1.2.3\npin=\nlast_success=%d\nlast_attempt=\n'; exit 0 ;;
  esac
  exit 0
fi
exit 0
`, exectest.ShellQuote(callLog), time.Now().Unix())
	exectest.WriteExecutable(t, filepath.Join(dir, name), body)
}

// TestEngine_WithRuntime_ReturnsIndependentCopyTargetingGivenRuntime pins the
// "small helper to run an engine action against a specified runtime" the
// plan calls for: it must return a copy whose Runtime is the given one,
// leave the original Engine's Runtime untouched, and share the same stdio
// streams (so per-runtime output aggregates into one report) and asset/base
// tag fields.
func TestEngine_WithRuntime_ReturnsIndependentCopyTargetingGivenRuntime(t *testing.T) {
	var out, errOut bytes.Buffer
	e := &Engine{
		Runtime:  "docker",
		AssetDir: "/assets",
		BaseTag:  "abc123def456",
		Stdin:    strings.NewReader(""),
		Stdout:   &out,
		Stderr:   &errOut,
	}

	targeted := e.WithRuntime("podman")

	if targeted.Runtime != "podman" {
		t.Errorf("targeted.Runtime = %q, want podman", targeted.Runtime)
	}
	if e.Runtime != "docker" {
		t.Errorf("original e.Runtime = %q, want it left unmodified (docker)", e.Runtime)
	}
	if targeted.AssetDir != e.AssetDir || targeted.BaseTag != e.BaseTag {
		t.Errorf("WithRuntime copy diverged on AssetDir/BaseTag: %+v vs %+v", targeted, e)
	}
	if targeted.Stdout != e.Stdout || targeted.Stderr != e.Stderr {
		t.Error("WithRuntime copy must share the same stdio streams so per-runtime output aggregates into one report")
	}
}

// TestUpdateAgent_ViaWithRuntime_TargetsOnlySpecifiedRuntime pins that
// WithRuntime fully redirects execution (not just a label): calling
// UpdateAgent on a WithRuntime("podman") copy must invoke only the podman
// binary, never docker, even though the original Engine's Runtime is
// "docker".
func TestUpdateAgent_ViaWithRuntime_TargetsOnlySpecifiedRuntime(t *testing.T) {
	e, dockerLog, podmanLog := dualRuntimeAgentEngine(t)

	if err := e.WithRuntime("podman").UpdateAgent("claude", ""); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	if calls := readCallLog(t, dockerLog); len(calls) != 0 {
		t.Errorf("expected WithRuntime(\"podman\") to leave docker untouched; docker calls:\n%s", strings.Join(calls, "\n"))
	}
	podmanCalls := readCallLog(t, podmanLog)
	want := "run --rm --user root --cap-drop=ALL --security-opt=no-new-privileges --entrypoint /bin/bash -v cenci-agent-cli-claude:/opt/cenci-agent " + MonolithImage + " /usr/local/bin/lib/agent-cli.sh update claude"
	if !containsLine(podmanCalls, want) {
		t.Errorf("expected the updater to run against podman; podman calls:\n%s", strings.Join(podmanCalls, "\n"))
	}
	if e.Runtime != "docker" {
		t.Errorf("e.Runtime = %q, want the original engine's Runtime left unmodified by WithRuntime", e.Runtime)
	}
}

// TestEnsureAgentVolume_ViaWithRuntime_BootstrapsUnderSpecifiedRuntimeOnly
// mirrors the UpdateAgent test above for EnsureAgentVolume's bootstrap path
// (the volume is absent under both fakes here, so it always falls through
// to the updater) — pinning the same "WithRuntime fully redirects
// execution" contract for the other runtime-targeted agent-volume entry
// point (Q4's "bootstrap in the preferred runtime" resolution, done by the
// caller choosing which runtime to call WithRuntime with).
func TestEnsureAgentVolume_ViaWithRuntime_BootstrapsUnderSpecifiedRuntimeOnly(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	// Both fakes report the monolith current and no existing volume, so
	// EnsureAgentVolume always falls through to bootstrap via UpdateAgent.
	noVolumeStub := func(name, callLog string) {
		body := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
if [ "$1" = images ]; then printf 'cenci-sandbox:latest\n'; exit 0; fi
if [ "$1" = image ] && [ "$2" = inspect ]; then printf '%s\n' 'shared-v2|abc123def456'; exit 0; fi
if [ "$1" = volume ] && [ "$2" = ls ]; then printf ''; exit 0; fi
exit 0
`
		exectest.WriteExecutable(t, filepath.Join(dir, name), body)
	}
	noVolumeStub("docker", dockerLog)
	noVolumeStub("podman", podmanLog)
	t.Setenv("PATH", dir)

	var out bytes.Buffer
	e := &Engine{Runtime: "docker", AssetDir: "/assets", BaseTag: "abc123def456", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out}

	if err := e.WithRuntime("podman").EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, dockerLog); len(calls) != 0 {
		t.Errorf("expected WithRuntime(\"podman\") to leave docker untouched; docker calls:\n%s", strings.Join(calls, "\n"))
	}
	if calls := readCallLog(t, podmanLog); !containsLineWithAll(calls, "agent-cli.sh", "update", "claude") {
		t.Errorf("expected the bootstrap to run against podman; podman calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestRefreshRunningPlugins_SweepsEveryInstalledRuntimeWhenBothPresent pins
// RefreshRunningPlugins' internal host-wide sweep (engine.go's Files-to-
// Modify bullet): with both docker and podman installed, every running
// sandbox container gets refreshed regardless of which runtime it's running
// under, not just the Engine's own (pre-existing, single) Runtime field.
func TestRefreshRunningPlugins_SweepsEveryInstalledRuntimeWhenBothPresent(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	writeFakeRuntime(t, dir, "docker", dockerLog)
	writeFakeRuntime(t, dir, "podman", podmanLog)
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_PS_DOCKER", "claude-cenci-agentstack\n")
	t.Setenv("FAKE_PS_PODMAN", "codex-cenci-otherrepo\n")

	var out bytes.Buffer
	e := &Engine{Runtime: "docker", AssetDir: "/assets", BaseTag: "abc123def456", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out}

	if err := e.RefreshRunningPlugins(); err != nil {
		t.Fatalf("RefreshRunningPlugins: %v", err)
	}

	dockerCalls := readCallLog(t, dockerLog)
	wantClaude := "exec -u dev claude-cenci-agentstack /bin/bash -c " + wantClaudeRefreshCmd
	if !containsLine(dockerCalls, wantClaude) {
		t.Errorf("expected the docker-owned container refreshed via the docker binary; docker calls:\n%s", strings.Join(dockerCalls, "\n"))
	}

	podmanCalls := readCallLog(t, podmanLog)
	wantCodex := "exec -u dev codex-cenci-otherrepo /bin/bash -c " + wantCodexRefreshCmd
	if !containsLine(podmanCalls, wantCodex) {
		t.Errorf("expected the podman-owned container refreshed via the podman binary; podman calls:\n%s", strings.Join(podmanCalls, "\n"))
	}
}

// TestRefreshRunningPlugins_OneRuntimeFails_OtherStillRefreshedAndErrorAggregated
// pins AC #4 for the host-wide sweep: a failed per-runtime `ps` query must
// not stop the healthy runtime's containers from being refreshed, and the
// failure must be visible in the aggregated returned error.
func TestRefreshRunningPlugins_OneRuntimeFails_OtherStillRefreshedAndErrorAggregated(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker-calls.txt")
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	writeFakeRuntime(t, dir, "docker", dockerLog)
	writeFakeRuntime(t, dir, "podman", podmanLog)
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_PS_EXIT_DOCKER", "1") // docker's ps fails outright
	t.Setenv("FAKE_PS_PODMAN", "codex-cenci-otherrepo\n")

	var out bytes.Buffer
	e := &Engine{Runtime: "docker", AssetDir: "/assets", BaseTag: "abc123def456", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out}

	err := e.RefreshRunningPlugins()
	if err == nil {
		t.Fatal("expected an aggregated error when one runtime's ps listing fails")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("expected the aggregated error to name the failing runtime (docker), got: %v", err)
	}

	podmanCalls := readCallLog(t, podmanLog)
	wantCodex := "exec -u dev codex-cenci-otherrepo /bin/bash -c " + wantCodexRefreshCmd
	if !containsLine(podmanCalls, wantCodex) {
		t.Errorf("expected the healthy podman runtime still refreshed; podman calls:\n%s", strings.Join(podmanCalls, "\n"))
	}
}
