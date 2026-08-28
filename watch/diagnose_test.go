package main_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

// -- diagnose ----------------------------------------------------------
//
// `cenci diagnose [--name <session>] [--agent claude|codex]` is a read-only
// report:
// container status/exit, the timestamped startup marker, recent logs,
// daemon/socket reachability, plugin + image versions, and mounted volumes,
// each failure annotated with a registered errcode.Code and a
// fatal/degraded/warning severity. These black-box tests drive the real
// built `cenci` binary as a subprocess via writeScriptedRuntime/
// writeAssetFixture (shared with sandbox_open_test.go, same package).

// diagEnv builds the black-box environment for native `cenci diagnose` runs.
// Unlike openTestEnv (used by the `open` launch-path tests), diagnose is
// read-only and must never spawn its own daemon (see launch.go's
// resolveCenciWiring, which diagnose deliberately does not call), so
// withSocket controls whether a live events-socket listener is pre-created
// under XDG_RUNTIME_DIR: true mirrors openTestEnv for the "daemon
// reachable" cases, false leaves an empty runtime dir (no socket file at
// all) for the "event socket missing" case.
func diagEnv(t *testing.T, fakeDir, assets string, withSocket bool) (env []string, home string) {
	t.Helper()
	home = t.TempDir()
	xdg := t.TempDir()
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}
	if withSocket {
		socketDir := filepath.Join(xdg, "cenci")
		if err := os.Mkdir(socketDir, 0o700); err != nil {
			t.Fatalf("mkdir socket dir: %v", err)
		}
		l, err := net.Listen("unix", filepath.Join(socketDir, "cenci-events.sock"))
		if err != nil {
			t.Fatalf("listen events socket: %v", err)
		}
		t.Cleanup(func() { _ = l.Close() })
	}
	env = append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+home,
		"CENCI_SANDBOX_ASSETS="+assets,
		"XDG_RUNTIME_DIR="+xdg,
		"FAKE_VOLUMES=cenci-agent-cli-claude\ncenci-agent-cli-codex\n",
		"FAKE_IMAGE_BASE_VERSION="+tag,
	)
	return env, home
}

func TestDiagnose_RunningReadySession_ExitsZero(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession")
	cmd.Env = env
	cmd.Dir = t.TempDir() // non-git cwd -> legacy scope
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose: %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "claude-cenci-mysession") {
		t.Errorf("expected the container name in the report, got:\n%s", out)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("expected the running container status, got:\n%s", out)
	}
}

func TestDiagnose_ExitedContainer_ReportsFatalGenericEntrypointCode(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession")
	cmd.Env = append(env, "FAKE_INSPECT_STATE=exited 1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	// diagnose is a report, not a pass/fail gate: it exits 0 even when it
	// finds fatal issues — only usage errors and cwd/home/runtime resolution
	// failures produce a non-zero exit.
	if err != nil {
		t.Fatalf("diagnose (exited container): %v\n%s", err, output)
	}
	out := string(output)
	for _, want := range []string{"exited", "CENCI-SANDBOX-START-002", "fatal", "docker/podman logs <container> --tail 50"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the diagnose report, got:\n%s", want, out)
		}
	}
}

func TestDiagnose_AgentCLIMissingMarker_SurfacedVerbatimWithTimestamp(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	const marker = "2026-07-20T10:00:00Z agent CLI not found or not executable at /opt/cenci-agent/current/node_modules/.bin/claude"
	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession")
	cmd.Env = append(env,
		"FAKE_INSPECT_STATE=exited 1",
		"FAKE_STARTUP_ERROR="+marker,
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose (agent CLI missing): %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, marker) {
		t.Errorf("expected the timestamped startup marker surfaced verbatim, got:\n%s", out)
	}
	if !strings.Contains(out, "CENCI-SANDBOX-START-001") {
		t.Errorf("expected the agent-CLI-missing error code, got:\n%s", out)
	}
	if !strings.Contains(out, "fatal") {
		t.Errorf("expected fatal severity, got:\n%s", out)
	}
	if !strings.Contains(out, "cenci sandbox update-agent") {
		t.Errorf("expected the registered recovery hint reused verbatim, got:\n%s", out)
	}
}

func TestDiagnose_MissingEventSocket_ReportsDegraded(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, false) // no live events socket

	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose (missing event socket): %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "CENCI-DAEMON-SOCKET-001") {
		t.Errorf("expected the event-socket-missing error code, got:\n%s", out)
	}
	if !strings.Contains(out, "degraded") {
		t.Errorf("expected degraded severity, got:\n%s", out)
	}
}

func TestDiagnose_ContainerNotFound_StillReadsHomeVolumeMarkers(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	const marker = "generic startup failure: entrypoint trap fired at credential seeding"
	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession")
	cmd.Env = append(env,
		"FAKE_CONTAINER_INSPECT_EXIT=1", // container already gone (--rm)
		"FAKE_STARTUP_MARKER="+marker,
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose (container not found): %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "CENCI-SANDBOX-SESSION-001") {
		t.Errorf("expected the session/container-not-found error code, got:\n%s", out)
	}
	if !strings.Contains(out, "fatal") {
		t.Errorf("expected fatal severity, got:\n%s", out)
	}
	if !strings.Contains(out, marker) {
		t.Errorf("expected the home-volume startup marker still surfaced despite the missing container (readHomeVolumeFile's short-lived container read doesn't depend on the workload container existing), got:\n%s", out)
	}
}

func TestDiagnose_ReportsImageBaseVersionAndUnknownPluginVersionOnReadFailure(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}
	env, _ := diagEnv(t, fakeDir, assets, true)

	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession")
	cmd.Env = env // FAKE_PLUGIN_MANIFEST left unset -> the manifest read fails/empty
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose (versions): %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, tag) {
		t.Errorf("expected the image base-version tag %q in the report, got:\n%s", tag, out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected the plugin manifest version to fall back to \"unknown\" when the read fails, got:\n%s", out)
	}
}

func TestDiagnose_PluginManifestVersion_SurfacedWhenReadable(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	const manifest = `{"version":"3.4.5-plugin-marker"}`
	cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession")
	cmd.Env = append(env, "FAKE_PLUGIN_MANIFEST="+manifest)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose (plugin manifest present): %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "3.4.5-plugin-marker") {
		t.Errorf("expected the readable plugin manifest content surfaced, got:\n%s", output)
	}
}

func TestDiagnose_WorksForBothAgents(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			fakeDir := t.TempDir()
			writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, _ := diagEnv(t, fakeDir, assets, true)

			cmd := exec.Command(binaryPath, "diagnose", "--name", "mysession", "--agent", agent)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("diagnose --agent %s: %v\n%s", agent, err, output)
			}
			wantContainer := agent + "-cenci-mysession"
			if !strings.Contains(string(output), wantContainer) {
				t.Errorf("expected container name %q in the report, got:\n%s", wantContainer, output)
			}
		})
	}
}

// TestDiagnose_UsageErrors_Exit2 covers usage errors under the new --name
// grammar (AC #6's "--verify continues to work"-adjacent flag-parsing
// coverage). Each case's wantSubstr is specific to that failure (the actual
// offending flag name, or ValidateAgent's exact message) rather than the
// generic "cenci diagnose:" prefix alone, so a case can't pass merely
// because some other, unrelated usage-error path was hit first (e.g. the
// retired positional guard, which is pinned separately by
// TestDiagnose_PositionalArgumentRetired_Exits2WithNewGrammar).
func TestDiagnose_UsageErrors_Exit2(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantSubstr string
	}{
		{"unknown flag", []string{"diagnose", "--name", "mysession", "--bogus"}, "bogus"},
		{"bad agent", []string{"diagnose", "--name", "mysession", "--agent", "gemini"}, `unknown agent "gemini"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, _ := diagEnv(t, fakeDir, assets, true)

			cmd := exec.Command(binaryPath, tc.args...)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("expected a usage error exit 2, got %T %v\n%s", err, err, output)
			}
			// Distinguishes a real "cenci diagnose:"-prefixed usage error
			// (ValidateAgent / unknown flag) from the generic "cenci:
			// unknown subcommand" fallback main.go prints for an
			// unrecognized top-level verb — both happen to exit 2, so the
			// exit code alone can't tell them apart.
			if !strings.Contains(string(output), "cenci diagnose:") {
				t.Errorf("expected a \"cenci diagnose:\"-prefixed usage error, not the generic unknown-subcommand fallback, got:\n%s", output)
			}
			if !strings.Contains(string(output), tc.wantSubstr) {
				t.Errorf("expected the usage error to contain %q (proving --name itself parsed and the failure came from the intended cause), got:\n%s", tc.wantSubstr, output)
			}
		})
	}
}

// TestDiagnose_DefaultSession_NoNameFlag_ReportsDefaultScope pins AC #2/#6:
// omitting --name entirely diagnoses the default (bare `cenci open`) session
// for the given agent, using the same scope resolution as `sandbox
// update-plugins`'s currentScope — never a usage error.
func TestDiagnose_DefaultSession_NoNameFlag_ReportsDefaultScope(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	cmd := exec.Command(binaryPath, "diagnose")
	cmd.Env = env
	cmd.Dir = t.TempDir() // non-git cwd -> legacy "default" scope
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnose (default session, no --name): %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "claude-cenci-default") {
		t.Errorf("expected the default scope's container name in the report when --name is omitted, got:\n%s", out)
	}
}

// TestDiagnose_PositionalArgumentRetired_Exits2WithNewGrammar pins AC #1:
// the positional <session> form is fully retired (no deprecation period) —
// a leftover positional is rejected via the existing rejectExtra path with a
// usage message naming both the offending argument and the new --name
// grammar.
func TestDiagnose_PositionalArgumentRetired_Exits2WithNewGrammar(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := diagEnv(t, fakeDir, assets, true)

	cmd := exec.Command(binaryPath, "diagnose", "mysession")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected the retired positional form to exit 2, got %T %v\n%s", err, err, output)
	}
	out := string(output)
	if !strings.Contains(out, `unexpected argument "mysession"`) {
		t.Errorf("expected the usage error to name the leftover positional via rejectExtra, got:\n%s", out)
	}
	if !strings.Contains(out, "--name") {
		t.Errorf("expected the usage error to show the new --name grammar, got:\n%s", out)
	}
}
