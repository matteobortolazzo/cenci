package main_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

// writePodmanOnlyRuntime writes only a fake podman (no docker) to dir, for
// dind preflight tests that need a podman-only host (Docker absent from
// PATH).
func writePodmanOnlyRuntime(t *testing.T, dir string) string {
	t.Helper()
	callLog := filepath.Join(dir, "calls.txt")
	writeScriptedRuntime(t, dir, "podman", callLog)
	writeFakeTmux(t, dir)
	return callLog
}

// -- open ------------------------------------------------------------------
//
// `cenci open` launches natively: the launcher engine creates/attaches the
// container itself and finally syscall.Execs the runtime CLI, so these tests
// assert the docker argv — including the entrypoint contract
// (launcher-lifecycle), the model shortcuts, the strict flag grammar
// (flag-parsing), and the #195 cenci wiring (cenci-wiring) that the retired
// bash suites used to pin against cenci-sand.

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
		{"xl", "codex", "gpt-6-luna", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust"},
		{"xt", "codex", "gpt-5.6-terra", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust"},
		{"xa", "codex", "gpt-6-astra", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust"},
		{"xs", "codex", "gpt-6.1-sol", "--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust"},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, _ := openTestEnv(t, fakeDir, assets)
			if tc.agent == "codex" {
				stageCodexAuth(t, home)
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

// -- ticket #1252: the per-launch ID ------------------------------------------

// launchIDOptionCalls parses the fake tmux's call log into the value set for
// each pane option, requiring every call to be a `set-option -p` on the
// launching pane (%7, openTestEnv's TMUX_PANE) through the launching pane's
// own socket (the first field of openTestEnv's TMUX).
func launchIDOptionCalls(t *testing.T, tmuxLog string) map[string]string {
	t.Helper()
	const prefix = "-S /tmp/tmux-1000/cenci set-option -p -t %7 -- "
	values := map[string]string{}
	lines := callLogLines(t, tmuxLog)
	for _, line := range lines {
		rest, ok := strings.CutPrefix(line, prefix)
		if !ok {
			t.Fatalf("tmux call %q is not a set-option -p on the launching pane via its own socket (%q); all calls:\n%s", line, prefix, strings.Join(lines, "\n"))
		}
		key, value, _ := strings.Cut(rest, " ")
		values[key] = value
	}
	return values
}

// TestOpen_LaunchID_AttachCarriesTheIDRecordedOnThePane is AC1 end to end for
// both agents and both branches: whether the launcher creates the container
// or attaches to a running one, the three pane options are set on %7 through
// the pane's own socket and the attach line carries CENCI_LAUNCH_ID=<the very
// value set on the pane> -- while the create (run) line never carries it.
func TestOpen_LaunchID_AttachCarriesTheIDRecordedOnThePane(t *testing.T) {
	cases := []struct {
		token, agent string
		running      bool
	}{
		{"ch", "claude", false},
		{"ch", "claude", true},
		{"xl", "codex", false},
		{"xl", "codex", true},
	}
	for _, tc := range cases {
		name := tc.agent + "/create"
		if tc.running {
			name = tc.agent + "/attach-to-running"
		}
		t.Run(name, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, socketDir := openTestEnv(t, fakeDir, assets)
			if tc.agent == "codex" {
				stageCodexAuth(t, home)
			}
			container := tc.agent + "-cenci-default"
			if tc.running {
				env = append(env,
					"FAKE_PS="+container+"\n",
					"FAKE_INSPECT_LABEL=detached",
					"FAKE_AGENT_MOUNTS=cenci-agent-cli-"+tc.agent+"|/opt/cenci-agent|false\n",
					"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
				)
			}

			cmd := exec.Command(binaryPath, "open", tc.token)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("open %s: %v\n%s", tc.token, err, output)
			}

			values := launchIDOptionCalls(t, fakeTmuxLogPath(fakeDir))
			if len(values) != 3 {
				t.Fatalf("pane options set = %v, want exactly @cenci-launch-id, @cenci-container, @cenci-runtime", values)
			}
			id := values["@cenci-launch-id"]
			if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
				t.Errorf("@cenci-launch-id = %q, want 32 lowercase hex", id)
			}
			if values["@cenci-container"] != container {
				t.Errorf("@cenci-container = %q, want %q", values["@cenci-container"], container)
			}
			if rt := values["@cenci-runtime"]; rt != "docker" && rt != "podman" {
				t.Errorf("@cenci-runtime = %q, want docker or podman", rt)
			}
			if strings.Contains(string(output), "Warning: could not record") {
				t.Errorf("a fully tagged launch warned:\n%s", output)
			}

			lines := callLogLines(t, callLog)
			if got := attachLine(t, lines); !strings.Contains(got, " -e CENCI_LAUNCH_ID="+id+" ") {
				t.Errorf("attach argv does not carry CENCI_LAUNCH_ID=%s (the value set on the pane):\n%s", id, got)
			}
			// #1292: the container and runtime recorded on the pane travel in the
			// same per-exec env as the ID.
			attach := attachLine(t, lines)
			for _, want := range []string{
				" -e CENCI_SANDBOX_CONTAINER=" + values["@cenci-container"] + " ",
				" -e CENCI_SANDBOX_RUNTIME=" + values["@cenci-runtime"] + " ",
			} {
				if !strings.Contains(attach, want) {
					t.Errorf("attach argv does not carry %q:\n%s", strings.TrimSpace(want), attach)
				}
			}
			for _, line := range lines {
				if strings.HasPrefix(line, "exec -it ") {
					continue
				}
				// The pre-attach orphan scan reads this key in its script;
				// only an environment assignment would bake launch ownership
				// into a non-interactive invocation or the container init.
				for _, name := range []string{"CENCI_LAUNCH_ID", "CENCI_SANDBOX_CONTAINER", "CENCI_SANDBOX_RUNTIME"} {
					if strings.Contains(line, " -e "+name+"=") {
						t.Errorf("a non-attach runtime call injects %s (it must never be create-time env):\n%s", name, line)
					}
				}
			}
		})
	}
}

// AC1: if a pane option cannot be set (older tmux without `set-option -p`, a
// vanished pane) the launch still proceeds, but no CENCI_LAUNCH_ID is
// injected and a single-line warning is printed -- the legacy (socket, pane)
// rule then covers that session.
func TestOpen_LaunchID_FailedPaneTagInjectsNoIDAndWarns(t *testing.T) {
	for _, agent := range []struct{ token, name string }{{"ch", "claude"}, {"xl", "codex"}} {
		t.Run(agent.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, _ := openTestEnv(t, fakeDir, assets)
			if agent.name == "codex" {
				stageCodexAuth(t, home)
			}

			if err := os.WriteFile(fakeTmuxFailMarkerPath(fakeDir), nil, 0o600); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(binaryPath, "open", agent.token)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("open %s: %v\n%s", agent.token, err, output)
			}

			lines := callLogLines(t, callLog)
			got := attachLine(t, lines)
			for _, name := range []string{"CENCI_LAUNCH_ID", "CENCI_SANDBOX_CONTAINER", "CENCI_SANDBOX_RUNTIME"} {
				if strings.Contains(got, name) {
					t.Errorf("attach argv carries %s although tagging the pane failed:\n%s", name, got)
				}
			}
			// The fake tmux must have been reached and refused the first tag
			// (@cenci-container, the first option set); any other failure (the
			// fake missing from PATH, a malformed socket) would otherwise pass.
			tmuxLines := callLogLines(t, fakeTmuxLogPath(fakeDir))
			const firstTag = "-S /tmp/tmux-1000/cenci set-option -p -t %7 -- @cenci-container "
			if len(tmuxLines) != 1 || !strings.HasPrefix(tmuxLines[0], firstTag) {
				t.Errorf("fake tmux calls = %q, want exactly one line starting %q", tmuxLines, firstTag)
			}
			var warnings []string
			for _, line := range strings.Split(string(output), "\n") {
				if strings.HasPrefix(line, "Warning: could not record ") {
					warnings = append(warnings, line)
				}
			}
			if len(warnings) != 1 {
				t.Errorf("want exactly one single-line tagging warning, got %d in output:\n%s", len(warnings), output)
			} else if !strings.Contains(warnings[0], "fake tmux: set-option refused") {
				t.Errorf("warning does not carry the fake tmux's refusal text:\n%s", warnings[0])
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
		"-e CENCI_SANDBOX_PLUGINS=cenci cenci-watch",
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
		"-e CENCI_SOCKET_DIR=/run/user/1000/cenci",
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
	// CENCI_TMUX_SOCKET is pane-scoped identity exactly like TMUX_PANE (#1007):
	// baked into the container-lifetime env it would go stale the same way,
	// and reap-orphans' (socket, pane) matching would misclassify using a
	// stale socket.
	if strings.Contains(runLine, "CENCI_TMUX_SOCKET") {
		t.Errorf("run argv must not carry CENCI_TMUX_SOCKET (#1007):\n%s", runLine)
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
	for _, want := range []string{"-u dev", "-e CENCI_SANDBOX=1", "-e CENCI_SANDBOX_AGENT=claude", "-e TMUX_PANE=%7", "-e CENCI_TMUX_SOCKET=/tmp/tmux-1000/cenci", "-e DISABLE_UPDATES=1", "/opt/cenci-agent/current/node_modules/.bin/claude"} {
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
	if !anyLineContains(lines, "agent-cli.sh status claude") {
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

// agentStatusFakeLine builds a FAKE_AGENT_STATUS env assignment for
// writeScriptedRuntime's `agent-cli.sh status` branch: the five key=value
// lines (populated=yes, a fixed version, pin, last_success, last_attempt),
// with lastSuccess/lastAttempt resolved to unix-epoch-seconds at
// fixture-write time (zero time.Time renders as the empty "unset" fact) —
// ticket #710's staleness policy is exercised via these Go-computed relative
// timestamps rather than a production clock seam.
func agentStatusFakeLine(pin string, lastSuccess, lastAttempt time.Time) string {
	ls, la := "", ""
	if !lastSuccess.IsZero() {
		ls = fmt.Sprintf("%d", lastSuccess.Unix())
	}
	if !lastAttempt.IsZero() {
		la = fmt.Sprintf("%d", lastAttempt.Unix())
	}
	return fmt.Sprintf("FAKE_AGENT_STATUS=populated=yes\\nversion=1.2.3\\npin=%s\\nlast_success=%s\\nlast_attempt=%s\\n", pin, ls, la)
}

// TestOpen_StaleAgentVolume_StartsDetachedRefreshBeforeWorkloadCreate pins
// ticket #710's staleness policy under ticket #745's non-blocking refinement:
// a populated shared agent volume whose last_success is older than the
// (default 24h) TTL starts the updater *detached* (--detach, under the fixed
// cenci-agent-cli-refresh-<agent> name) before the workload container is
// created, tells the user the refresh happens in the background, and the
// launch still succeeds without waiting for the update's outcome.
func TestOpen_StaleAgentVolume_StartsDetachedRefreshBeforeWorkloadCreate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	stale := time.Now().Add(-25 * time.Hour)
	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, agentStatusFakeLine("", stale, time.Time{}))
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (TTL-stale existing volume): %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "background") {
		t.Errorf("expected the stale-refresh notice to say the refresh happens in the background, got:\n%s", output)
	}

	lines := callLogLines(t, callLog)
	updater, workload := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "agent-cli.sh update claude") {
			updater = i
			if !strings.Contains(line, "--detach") || !strings.Contains(line, "--name cenci-agent-cli-refresh-claude") {
				t.Errorf("expected the stale-refresh updater to start detached under the fixed refresh name, got: %s", line)
			}
		}
		if strings.HasPrefix(line, "run --name claude-cenci-default") {
			workload = i
		}
	}
	if updater < 0 || workload < 0 || updater >= workload {
		t.Errorf("a TTL-stale shared agent volume must start its detached refresh before workload create; calls:\n%s", strings.Join(lines, "\n"))
	}
	attachLine(t, lines)
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
				stageCodexAuth(t, home)
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

// -- ticket #1163 Q1: startupFailureDetail's mixed read-failed fallback ----
//
// startupFailureDetail checks up to three home-volume markers plus a
// `logs --tail 50` fallback, in precedence order. Before #1163, every
// non-successful read (a legitimate "no such marker" AND a genuine runtime
// read failure) was silently the same "absent" outcome, so a
// daemon-unreachable failure on all three reads degraded into the generic
// "entrypoint exited before initialization completed" fallback — actively
// misleading, since nothing was actually learned about the entrypoint at
// all. Q1's rule: if ANY of the three reads comes back read-failed, emit a
// distinct read-failed fallback string instead; the generic fallback is
// reserved for the case where every read was DEFINITIVELY absent (and the
// logs fallback also yielded nothing).

// TestOpen_AllHomeVolumeReadsFailed_ReportsReadFailedNotGenericFallback pins
// Q1 case (a): every one of the three home-volume reads fails at the
// runtime level (exit 125) — the read-failed fallback string must be
// emitted, and the generic "entrypoint exited before initialization
// completed" string must never appear (it would misrepresent "we could not
// look" as "we looked and found nothing").
func TestOpen_AllHomeVolumeReadsFailed_ReportsReadFailedNotGenericFallback(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_READY_EXIT=1",
		"FAKE_INSPECT_STATE=exited 1",
		"FAKE_STARTUP_ERROR_EXIT=125",
		"FAKE_BOOT_LOG_EXIT=125",
		"FAKE_STARTUP_MARKER_EXIT=125",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if strings.Contains(string(output), "entrypoint exited before initialization completed") {
		t.Errorf("expected the read-failed fallback, not the generic \"nothing found\" fallback (every read failed at the runtime level, not absent), got:\n%s", output)
	}
	if !strings.Contains(string(output), "read failed") {
		t.Errorf("expected a distinct read-failed fallback string naming the read failure, got:\n%s", output)
	}
}

// TestOpen_MixedHomeVolumeReadFailure_ReportsReadFailedNotGenericFallback
// pins Q1 case (b): only the FIRST read (the agent-CLI-missing marker)
// fails at the runtime level; the other two reads and the logs fallback are
// legitimately absent/empty. Even one read-failed among the three must
// still emit the read-failed fallback — the outcome isn't "upgraded" to
// looking clean just because the other two reads came back absent.
func TestOpen_MixedHomeVolumeReadFailure_ReportsReadFailedNotGenericFallback(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_READY_EXIT=1",
		"FAKE_INSPECT_STATE=exited 1",
		"FAKE_STARTUP_ERROR_EXIT=125",
		"FAKE_BOOT_LOG_EXIT=1",
		"FAKE_STARTUP_MARKER_EXIT=1",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if strings.Contains(string(output), "entrypoint exited before initialization completed") {
		t.Errorf("expected the read-failed fallback even though only ONE of the three reads failed, got:\n%s", output)
	}
	if !strings.Contains(string(output), "read failed") {
		t.Errorf("expected a distinct read-failed fallback string naming the read failure, got:\n%s", output)
	}
}

// TestOpen_AllHomeVolumeReadsDefinitivelyAbsent_ReportsGenericFallback pins
// Q1 case (c), the non-regression guard: when all three reads are
// DEFINITIVELY absent (exit 1, "no such marker") and the logs fallback
// yields nothing, the existing generic fallback string must still be
// emitted verbatim — the #1163 change must not make every startup failure
// report "read failed".
func TestOpen_AllHomeVolumeReadsDefinitivelyAbsent_ReportsGenericFallback(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_READY_EXIT=1",
		"FAKE_INSPECT_STATE=exited 1",
		"FAKE_STARTUP_ERROR_EXIT=1",
		"FAKE_BOOT_LOG_EXIT=1",
		"FAKE_STARTUP_MARKER_EXIT=1",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "entrypoint exited before initialization completed") {
		t.Errorf("expected the generic fallback when every read is definitively absent and logs are empty, got:\n%s", output)
	}
	if strings.Contains(string(output), "read failed") {
		t.Errorf("did not expect the read-failed fallback when every read was definitively absent, not failed, got:\n%s", output)
	}
}

// TestOpen_AgentStartupErrorMarkerAllControlBytes_FallsThroughToBootLog pins
// a #1163 security re-review regression: fix #2 made readHomeVolumeFile
// return (homeVolumeReadOK, "") for a genuinely successful read whose
// content sanitizes down to empty (e.g. a marker containing only stripped
// control bytes), rather than misclassifying it as Absent. Before this fix,
// startupFailureDetail's first tier matched a plain `case homeVolumeReadOK`
// and returned immediately on that empty content, suppressing every
// remaining tier (boot log, startup-failed, the logs fallback) down to a
// bare error code with no detail — a container-controlled way to blind the
// very diagnostics this ticket exists to make reliable. An all-control-byte
// .cenci-agent-startup-error marker must fall through to the boot-log tier
// exactly like a legitimately absent marker does, not short-circuit.
func TestOpen_AgentStartupErrorMarkerAllControlBytes_FallsThroughToBootLog(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	const bootLog = "boot log: mkdir /workspace/.cenci: permission denied"
	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_READY_EXIT=1",
		"FAKE_INSPECT_STATE=exited 1",
		"FAKE_STARTUP_ERROR=\x1b\x1b\x1b",
		"FAKE_BOOT_LOG="+bootLog,
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a startup failure exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), bootLog) {
		t.Errorf("expected an all-control-byte agent-CLI-missing marker to fall through to the boot-log tier instead of suppressing it, got:\n%s", output)
	}
	if strings.Contains(string(output), "CENCI-SANDBOX-START-001") {
		t.Errorf("expected the OK-but-empty agent-CLI-missing marker to NOT short-circuit with its own code (no real detail was ever found there), got:\n%s", output)
	}
	if !strings.Contains(string(output), "CENCI-SANDBOX-START-002") {
		t.Errorf("expected the generic-entrypoint code (the boot-log tier that should have fired), got:\n%s", output)
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

// -- open --dind (sysbox nested-Docker mode, #585) -------------------------
//
// dind replaces the removed --docker DooD path: instead of bind-mounting the
// host's runtime socket (root-equivalent access to the host), dind runs the
// workload container itself under the sysbox-runc OCI runtime with its own
// isolated per-repo Docker storage volume, so a nested `docker` inside the
// sandbox never touches the host runtime at all.

func TestOpenDind_ViaRepoConfig_AddsSysboxRuntimeVolumeAndEnv(t *testing.T) {
	repoRoot, slug := dindRepoEnv(t, true)

	fakeDir := t.TempDir()
	// docker-only: dind's preflight requires Docker as the outer runtime, and
	// podman-first detection would otherwise win were both fakes present.
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, `FAKE_INFO_RUNTIMES={"sysbox-runc":{},"runc":{}}`)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (dind via repo config): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	createLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(createLine, "--runtime=sysbox-runc") {
		t.Errorf("expected --runtime=sysbox-runc in the run argv, got: %s", createLine)
	}
	wantVolume := "claude-cenci-dind-" + slug + ":/var/lib/docker"
	if !strings.Contains(createLine, wantVolume) {
		t.Errorf("expected the dind storage volume mount %q, got: %s", wantVolume, createLine)
	}
	if !strings.Contains(createLine, "-e CENCI_SANDBOX_DIND=1") {
		t.Errorf("expected -e CENCI_SANDBOX_DIND=1 in the run argv, got: %s", createLine)
	}
}

func TestOpenNoDind_OverridesDindConfigRepo(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, true)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--no-dind")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open --no-dind: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	createLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	for _, marker := range []string{"--runtime=sysbox-runc", "-cenci-dind-", "CENCI_SANDBOX_DIND"} {
		if strings.Contains(createLine, marker) {
			t.Errorf("expected --no-dind to suppress dind despite the repo config, but found %q in: %s", marker, createLine)
		}
	}
}

// configuredRepoEnv (#1002) git-inits a temp dir (skipping the test if git
// isn't available, mirroring malformedDindRepoEnv) and writes config
// verbatim as its `.cenci/config.json` — a fixture for exercising
// sandbox.plugins values (valid subsets, the empty array, or a malformed
// key), as opposed to malformedDindRepoEnv's fixed unparsable-JSON case.
func configuredRepoEnv(t *testing.T, config string) (repoRoot string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".cenci"), 0o755); err != nil {
		t.Fatalf("mkdir .cenci: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".cenci", "config.json"), []byte(config), 0o644); err != nil {
		t.Fatalf("write .cenci/config.json: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}
	return resolved
}

// TestOpen_MalformedDindConfig_Exits1 pins the #632 hard-fail: a corrupt
// .cenci/config.json must never silently launch with dind off — it must
// hard-fail the launch (exit 1) with a path-bearing, non-usage error.
func TestOpen_MalformedDindConfig_Exits1(t *testing.T) {
	repoRoot := malformedDindRepoEnv(t)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected malformed .cenci/config.json to exit 1 (hard fail, not a usage error), got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "config.json") {
		t.Errorf("expected a path-bearing error naming config.json, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected no runtime calls once config parsing hard-fails, got:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpenNoDind_SucceedsDespiteMalformedConfig pinned --no-dind as a
// config-free escape hatch pre-#1002 (#632): it kept working even with a
// corrupt repo .cenci/config.json, because ResolveDind returns before
// RepoDindConfig is ever reached. #1002 narrows that escape hatch (Q&A #1):
// resolveLaunchContext now ALSO resolves sandbox.plugins unconditionally
// (after resolveDindForHost, regardless of --no-dind -- there is no
// "--no-plugins" equivalent), so the same unparsable config.json that
// --no-dind used to route around is read again for plugins and hard-fails
// the launch. --no-dind still suppresses dind's own config read; it can no
// longer make a corrupt config file launchable at all.
func TestOpenNoDind_SucceedsDespiteMalformedConfig(t *testing.T) {
	repoRoot := malformedDindRepoEnv(t)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--no-dind")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected --no-dind with a malformed .cenci/config.json to exit 1 (the plugins read still hard-fails on it, #1002), got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "config.json") {
		t.Errorf("expected a path-bearing error naming config.json, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected no runtime calls once the plugins config read hard-fails, got:\n%s", strings.Join(lines, "\n"))
	}
}

// -- ticket #1002: sandbox.plugins resolution ------------------------------

// TestOpen_MalformedSandboxPluginsConfig_Exits1 pins RepoSandboxPlugins'
// #632-mirroring hard-fail reaching `cenci open`: a well-formed JSON
// document whose "sandbox.plugins" value is outside the closed set must
// hard-fail the launch (exit 1) with a path-bearing, non-usage error naming
// the offending value — not silently fall back to the default pair.
func TestOpen_MalformedSandboxPluginsConfig_Exits1(t *testing.T) {
	repoRoot := configuredRepoEnv(t, `{"sandbox":{"plugins":["cenci","bogus-plugin"]}}`)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected an unrecognized sandbox.plugins value to exit 1 (hard fail, not a usage error), got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "config.json") {
		t.Errorf("expected a path-bearing error naming config.json, got:\n%s", output)
	}
	if !strings.Contains(string(output), "bogus-plugin") {
		t.Errorf("expected the error to name the offending value \"bogus-plugin\", got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("expected no runtime calls once the plugins config read hard-fails, got:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpen_FreshCreate_DefaultPluginsSubset_PrintsNoInformationalLine pins
// the negative half of the AC: when the resolved list equals the default
// pair (no repo config at all, the common case), nothing is printed about
// plugin provisioning.
func TestOpen_FreshCreate_DefaultPluginsSubset_PrintsNoInformationalLine(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = env
	cmd.Dir = t.TempDir() // non-git cwd -> legacy scope, always the default pair
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch: %v\n%s", err, output)
	}

	if strings.Contains(string(output), "plugin") {
		t.Errorf("expected no plugin-provisioning note for the default plugin list, got:\n%s", output)
	}

	runLine, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name ")
	if !ok {
		t.Fatal("expected a container run")
	}
	if !strings.Contains(runLine, "-e CENCI_SANDBOX_PLUGINS=cenci cenci-watch") {
		t.Errorf("expected the default resolved list in the create argv, got:\n%s", runLine)
	}
}

// TestOpen_FreshCreate_NarrowedPluginsSubset_PrintsInformationalLine pins
// the AC's informational line: when the repo config narrows sandbox.plugins
// away from the default pair, `cenci open` prints one line before the
// create/attach decision naming what will be provisioned, and the create
// argv's CENCI_SANDBOX_PLUGINS reflects exactly the narrowed subset.
func TestOpen_FreshCreate_NarrowedPluginsSubset_PrintsInformationalLine(t *testing.T) {
	repoRoot := configuredRepoEnv(t, `{"sandbox":{"plugins":["cenci"]}}`)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (narrowed sandbox.plugins): %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "plugin") {
		t.Errorf("expected an informational note about the narrowed plugin list, got:\n%s", out)
	}
	if strings.Contains(out, "cenci-watch") {
		t.Errorf("expected the informational note to name only the narrowed subset (not cenci-watch, which was dropped), got:\n%s", out)
	}

	runLine, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name ")
	if !ok {
		t.Fatal("expected a container run")
	}
	if !strings.Contains(runLine, "-e CENCI_SANDBOX_PLUGINS=cenci") {
		t.Errorf("expected the narrowed resolved list in the create argv, got:\n%s", runLine)
	}
	if strings.Contains(runLine, "CENCI_SANDBOX_PLUGINS=cenci cenci-watch") {
		t.Errorf("expected the create argv to carry only the narrowed subset, not the default pair, got:\n%s", runLine)
	}
}

// TestOpen_FreshCreate_EmptyPluginsList_PrintsDistinctInformationalLine pins
// the AC's "distinct wording for the empty list" requirement: an explicitly
// empty sandbox.plugins array is a valid, non-default resolution and must
// still print an informational line, worded differently from the narrowed-
// but-non-empty case above (this test only pins that the line exists and
// the create argv's CENCI_SANDBOX_PLUGINS is empty; the exact wording is an
// implementation choice for the next, non-red phase).
func TestOpen_FreshCreate_EmptyPluginsList_PrintsDistinctInformationalLine(t *testing.T) {
	repoRoot := configuredRepoEnv(t, `{"sandbox":{"plugins":[]}}`)

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (empty sandbox.plugins): %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "plugin") {
		t.Errorf("expected an informational note about the empty plugin list, got:\n%s", out)
	}

	runLine, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name ")
	if !ok {
		t.Fatal("expected a container run")
	}
	if !strings.Contains(runLine, "-e CENCI_SANDBOX_PLUGINS=") {
		t.Errorf("expected CENCI_SANDBOX_PLUGINS to still be set (with an empty value) even for the empty list, got:\n%s", runLine)
	}
	if strings.Contains(runLine, "-e CENCI_SANDBOX_PLUGINS=cenci") {
		t.Errorf("expected an empty resolved list in the create argv, got:\n%s", runLine)
	}
}

// openAttachEnvFor (#1002) is the shared setup for the attach-path plugins
// drift tests below: a running, detached-labeled, compatibly-mounted
// container named containerName (mirroring
// TestOpen_AttachToRunning_SkipsCreate's fixture) scripted to answer the
// drift probe with containerEnv (verbatim FAKE_CONTAINER_ENV content — e.g.
// "CENCI_SANDBOX_PLUGINS=cenci\n", or "" to simulate an absent var / legacy
// container).
func openAttachEnvFor(t *testing.T, fakeDir, assets, containerName, containerEnv string) []string {
	t.Helper()
	env, _, socketDir := openTestEnv(t, fakeDir, assets)
	return append(env,
		"FAKE_PS="+containerName+"\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
		"FAKE_CONTAINER_ENV="+containerEnv,
	)
}

// TestOpenAttach_PluginsDrift_WarnsWithStopRemedy pins the AC's attach-path
// drift warning: when the resolved list (from repo config) differs from the
// already-running container's actual CENCI_SANDBOX_PLUGINS (read back via
// the best-effort drift probe), the launcher prints exactly one drift line
// naming the `cenci sandbox stop <name>` remedy — and never auto-recreates
// or blocks the attach.
func TestOpenAttach_PluginsDrift_WarnsWithStopRemedy(t *testing.T) {
	repoRoot := configuredRepoEnv(t, `{"sandbox":{"plugins":["cenci"]}}`)
	containerName := "claude-cenci-" + launcher.Slugify(filepath.Base(repoRoot))

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env := openAttachEnvFor(t, fakeDir, assets, containerName, "CENCI_SANDBOX_PLUGINS=cenci cenci-watch\n")

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (attach, plugins drift): %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "cenci sandbox stop "+containerName) {
		t.Errorf("expected the drift warning to name the 'cenci sandbox stop %s' remedy, got:\n%s", containerName, out)
	}
	if _, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name "); ok {
		t.Errorf("expected drift to warn, never auto-recreate the container, got calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
	}
	attachLine(t, callLogLines(t, callLog))
}

// TestOpenAttach_PluginsNoDrift_ResolvedMatchesRunning_NoWarning pins the
// no-drift baseline: when the resolved list (the default pair, no repo
// config) matches the running container's actual CENCI_SANDBOX_PLUGINS
// exactly, no drift line is printed.
func TestOpenAttach_PluginsNoDrift_ResolvedMatchesRunning_NoWarning(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env := openAttachEnvFor(t, fakeDir, assets, "claude-cenci-default", "CENCI_SANDBOX_PLUGINS=cenci cenci-watch\n")

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = t.TempDir() // legacy scope -> resolved default pair
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (attach, no drift, default): %v\n%s", err, output)
	}
	if strings.Contains(string(output), "cenci sandbox stop") {
		t.Errorf("expected no drift warning when the resolved list matches the running container's value, got:\n%s", output)
	}
	attachLine(t, callLogLines(t, callLog))
}

// TestOpenAttach_PluginsNoDrift_LegacyUnsetRunning_ComparesAgainstDefault
// pins the legacy-container signal (Q&A #5): a running container that
// carries no CENCI_SANDBOX_PLUGINS at all (predates this ticket) must be
// treated as if it were created with the default pair — so a launch whose
// own resolved list IS the default pair sees no drift.
func TestOpenAttach_PluginsNoDrift_LegacyUnsetRunning_ComparesAgainstDefault(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env := openAttachEnvFor(t, fakeDir, assets, "claude-cenci-default", "") // no CENCI_SANDBOX_PLUGINS line at all

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = t.TempDir() // legacy scope -> resolved default pair
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (attach, legacy unset running env): %v\n%s", err, output)
	}
	if strings.Contains(string(output), "cenci sandbox stop") {
		t.Errorf("expected no drift warning: an unset running CENCI_SANDBOX_PLUGINS (legacy container) must compare against the default pair, which is what this launch resolved to, got:\n%s", output)
	}
	attachLine(t, callLogLines(t, callLog))
}

// TestOpenAttach_PluginsDriftProbeFails_DoesNotBlockAttach pins the AC's
// "never blocks the attach when the probe or parse fails" requirement: the
// drift probe's own inspect call failing (a transient runtime error, an
// unparsable response) must never abort the attach — unlike
// inspectReusePosture, which deliberately fails closed. The attach must
// still succeed and no drift warning is owed (the probe couldn't determine
// anything).
func TestOpenAttach_PluginsDriftProbeFails_DoesNotBlockAttach(t *testing.T) {
	repoRoot := configuredRepoEnv(t, `{"sandbox":{"plugins":["cenci"]}}`)
	containerName := "claude-cenci-" + launcher.Slugify(filepath.Base(repoRoot))

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env := append(openAttachEnvFor(t, fakeDir, assets, containerName, "CENCI_SANDBOX_PLUGINS=cenci cenci-watch\n"),
		"FAKE_CONTAINER_ENV_EXIT=1", // the drift probe itself fails
	)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (attach, drift probe failure must not block attach): %v\n%s", err, output)
	}
	if strings.Contains(string(output), "cenci sandbox stop") {
		t.Errorf("expected no drift warning when the drift probe itself fails (nothing could be determined), got:\n%s", output)
	}
	attachLine(t, callLogLines(t, callLog))
}

// symlinkGitOnlyDir symlinks the real git binary into dir (already holding a
// scripted single-runtime fake) and returns dir, so a caller can set PATH to
// dir alone: git resolves, but no host PATH directory can leak a real
// docker/podman into the runtime lookup a test deliberately made single.
func symlinkGitOnlyDir(t *testing.T, dir string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		t.Fatalf("symlink git into %s: %v", dir, err)
	}
	return dir
}

func TestOpenDind_PodmanOnlyHost_Exits1RequiresDocker(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, false)

	fakeDir := t.TempDir()
	callLog := writePodmanOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	// openTestEnv's PATH appends /usr/bin:/bin so git resolves, but on a host
	// that also has a real docker installed there, that would leak a real
	// docker back onto PATH and defeat this test's "docker absent from PATH"
	// premise. Symlink git into fakeDir and use it alone, so no host
	// directory can supply a real docker/podman.
	env = append(env, "PATH="+symlinkGitOnlyDir(t, fakeDir))

	cmd := exec.Command(binaryPath, "open", "--dind")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the podman-outer-runtime dind preflight failure to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "requires Docker") {
		t.Errorf("expected an error naming the Docker-as-outer-runtime requirement, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); anyLineContains(lines, "run --name ") {
		t.Errorf("expected no container to be created when dind preflight fails, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenDind_SysboxNotRegistered_Exits1WithInstallPointer(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, false)

	fakeDir := t.TempDir()
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, `FAKE_INFO_RUNTIMES={"runc":{}}`) // no sysbox-runc registered

	cmd := exec.Command(binaryPath, "open", "--dind")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the sysbox-not-registered preflight failure to exit 1, got %T %v\n%s", err, err, output)
	}
	lower := strings.ToLower(string(output))
	for _, want := range []string{"sysbox", "arch", "ubuntu", "nestybox"} {
		if !strings.Contains(lower, want) {
			t.Errorf("expected the sysbox install-pointer message to mention %q, got:\n%s", want, output)
		}
	}
	if lines := callLogLines(t, callLog); anyLineContains(lines, "run --name ") {
		t.Errorf("expected no container to be created when dind preflight fails, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpenDind_DockerInfoFails_Exits1 covers AC #7: dindPreflight's
// `docker info` call failing (nonzero exit — the daemon is down) must exit 1
// with the operator-facing wrapping-chain message ("checking sysbox-runc
// registration ... docker info: exit status 1"), distinct from the
// already-covered unregistered-sysbox install-pointer message (asserted
// absent below so this test can never accidentally pass by hitting that
// other branch), and must never create a container.
func TestOpenDind_DockerInfoFails_Exits1(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, false)

	fakeDir := t.TempDir()
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, "FAKE_INFO_EXIT=1")

	cmd := exec.Command(binaryPath, "open", "--dind")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the docker-info-fails dind preflight failure to exit 1, got %T %v\n%s", err, err, output)
	}
	out := string(output)
	if !strings.Contains(out, "checking sysbox-runc registration") {
		t.Errorf("expected the daemon-down wrapping message to mention checking sysbox-runc registration, got:\n%s", out)
	}
	if !strings.Contains(out, "info") {
		t.Errorf("expected the daemon-down wrapping message to mention the failing `info` call, got:\n%s", out)
	}
	if strings.Contains(out, "sysbox-ce") {
		t.Errorf("expected NOT to hit the already-covered unregistered-sysbox install-pointer message (mentions sysbox-ce), got:\n%s", out)
	}
	if lines := callLogLines(t, callLog); anyLineContains(lines, "run --name ") {
		t.Errorf("expected no container to be created when dind preflight fails, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpenDind_DockerdMarkerPresent_FreshCreate_WarnsBeforeAttachButStillAttaches
// pins ticket #630's Q1 (warn, still attach): a persistent dockerd
// startup-failure marker detected right before the first agent attach on a
// freshly-created container must print a prominent, non-fatal Warning naming
// CENCI-SANDBOX-DIND-001 and the captured diagnostic, but must NOT block the
// attach — the agent session still launches (matches #586's non-blocking
// dockerd-start mandate; the container and every other capability but nested
// Docker still work).
func TestOpenDind_DockerdMarkerPresent_FreshCreate_WarnsBeforeAttachButStillAttaches(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, true)

	fakeDir := t.TempDir()
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	const marker = "2026-07-24T09:00:00Z dockerd exited with status 1: failed to start daemon: mkdir /var/lib/docker/overlay2: read-only file system"
	env = append(env,
		`FAKE_INFO_RUNTIMES={"sysbox-runc":{},"runc":{}}`,
		"FAKE_DOCKERD_MARKER="+marker,
	)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (dind, dockerd marker present): %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "Warning:") {
		t.Errorf("expected a prominent Warning: for the dockerd startup-failure marker, got:\n%s", out)
	}
	if !strings.Contains(out, marker) {
		t.Errorf("expected the marker's captured diagnostic surfaced verbatim, got:\n%s", out)
	}
	if !strings.Contains(out, "CENCI-SANDBOX-DIND-001") {
		t.Errorf("expected the CENCI-SANDBOX-DIND-001 error code attached, got:\n%s", out)
	}

	// Non-fatal: the agent session still attached despite the warning.
	attachLine(t, callLogLines(t, callLog))
}

// TestOpenDind_NoDockerdMarker_NoWarning is the sibling of the test above:
// when no dockerd-startup-error marker was ever written, dind mode must
// launch silently on this front (no false-positive warning).
func TestOpenDind_NoDockerdMarker_NoWarning(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, true)

	fakeDir := t.TempDir()
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, `FAKE_INFO_RUNTIMES={"sysbox-runc":{},"runc":{}}`)
	// FAKE_DOCKERD_MARKER deliberately left unset.

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (dind, no dockerd marker): %v\n%s", err, output)
	}

	if strings.Contains(string(output), "CENCI-SANDBOX-DIND-001") {
		t.Errorf("did not expect the dind startup-failure warning when no marker was ever written, got:\n%s", output)
	}
	attachLine(t, callLogLines(t, callLog))
}

// TestOpen_NonDind_NeverProbesDockerdMarker asserts the dockerd-marker
// before-attach check is strictly gated on ctx.DindOn: a non-dind launch
// must never even attempt the short-lived-container home-volume read for
// .cenci-dockerd-startup-error (that path doesn't exist for a non-dind
// session's home volume in the first place).
func TestOpen_NonDind_NeverProbesDockerdMarker(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch (non-dind): %v\n%s", err, output)
	}

	if anyLineContains(callLogLines(t, callLog), ".cenci-dockerd-startup-error") {
		t.Errorf("non-dind launch must never probe the dockerd-startup-error marker, got calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
	}
}

// TestOpenDind_DockerdMarkerPresent_AttachToRunning_WarnsBeforeAttach covers
// the launcher's second warnDockerdStartupFailure call site (before the
// "attach to an already-running container" runAgent, not just fresh
// create): the marker must still surface non-fatally before this attach too.
func TestOpenDind_DockerdMarkerPresent_AttachToRunning_WarnsBeforeAttach(t *testing.T) {
	repoRoot, slug := dindRepoEnv(t, true)

	fakeDir := t.TempDir()
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, socketDir := openTestEnv(t, fakeDir, assets)
	const marker = "2026-07-24T09:05:00Z dockerd exited with status 137: killed"
	env = append(env,
		`FAKE_INFO_RUNTIMES={"sysbox-runc":{},"runc":{}}`,
		"FAKE_DOCKERD_MARKER="+marker,
		"FAKE_PS=claude-cenci-"+slug+"\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
		"FAKE_REUSE_POSTURE=on|sysbox-runc|1\nworkspace-vol::/workspace\ndind-vol::/var/lib/docker\n\n",
	)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (dind, attach to running, dockerd marker present): %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "CENCI-SANDBOX-DIND-001") {
		t.Errorf("expected the CENCI-SANDBOX-DIND-001 warning before attaching to the already-running container, got:\n%s", out)
	}
	if !strings.Contains(out, marker) {
		t.Errorf("expected the marker's captured diagnostic surfaced verbatim, got:\n%s", out)
	}
	attachLine(t, callLogLines(t, callLog))
}

func TestOpenDindAndNoDind_Together_Exits2(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--dind", "--no-dind")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected --dind --no-dind together to exit 2, got %T %v\n%s", err, err, output)
	}
	// Distinguishes the intended usage-conflict error from --dind simply not
	// existing yet as a recognized flag (which also exits 2, coincidentally).
	if strings.Contains(string(output), "flag provided but not defined") {
		t.Errorf("expected --dind and --no-dind to be recognized flags rejected for conflicting together, not unrecognized flags; got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls for the --dind/--no-dind usage error, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenDind_LegacyScope_Exits2(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--dind")
	cmd.Env = env
	cmd.Dir = t.TempDir() // non-git cwd -> legacy "default" scope
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected --dind in legacy scope to be a usage error (exit 2), got %T %v\n%s", err, err, output)
	}
	// Distinguishes the intended scope-conflict usage error from --dind
	// simply not existing yet as a recognized flag (which also exits 2,
	// coincidentally).
	if strings.Contains(string(output), "flag provided but not defined") {
		t.Errorf("expected --dind to be a recognized flag rejected for legacy scope, not an unrecognized flag; got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls for the dind/legacy-scope usage error, got:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpenDockerFlag_IsRemoved_Exits2NoRunCall regression-tests the removal
// of the --docker DooD flag (replaced by --dind): it must now be rejected as
// an unrecognized flag, and critically must never reach container creation.
func TestOpenDockerFlag_IsRemoved_Exits2NoRunCall(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "--docker")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected the removed --docker flag to exit 2, got %T %v\n%s", err, err, output)
	}
	if lines := callLogLines(t, callLog); anyLineContains(lines, "run --name ") {
		t.Errorf("expected no 'run' subcommand for the removed --docker flag, got calls:\n%s", strings.Join(lines, "\n"))
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

// TestOpenCodexWithoutAuth_DoesNotRemoveStoppedContainer is a #620 follow-up
// regression test (code review, "Should Fix"): Launch's create branch used to
// run `rm <container-name>` unconditionally before building the create argv,
// so a stopped/absent same-named container was always deleted even when the
// launch itself then failed (e.g. codex credential validation). The #620
// refactor moved `rm` to run after planArgvs (which includes buildRunArgv's
// credential validation) succeeds, so a failed launch must now leave any
// stopped, same-named container untouched — nothing removes it before the
// hard error is returned. FAKE_PS is left empty (its default), which is the
// "not running" case containerRunning reads regardless of whether the
// container is stopped or absent.
func TestOpenCodexWithoutAuth_DoesNotRemoveStoppedContainer(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets) // no ~/.codex/auth.json, OPENAI_API_KEY scrubbed

	cmd := exec.Command(binaryPath, "open", "xt")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected codex auth failure to exit 1, got %T %v\n%s", err, err, output)
	}

	lines := callLogLines(t, callLog)
	if anyLineContains(lines, "rm codex-cenci-default") {
		t.Errorf("expected the stopped/absent same-named container to survive a failed credential validation (rm must run after planArgvs succeeds, not before), got calls:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpenCodex_RemovesStoppedContainerOnSuccessfulCreate is the success-path
// sibling of TestOpenCodexWithoutAuth_DoesNotRemoveStoppedContainer: once
// credential validation (and the rest of planArgvs) succeeds, Launch must
// still remove a stopped/absent same-named container before creating the new
// one, exactly as it always has.
func TestOpenCodex_RemovesStoppedContainerOnSuccessfulCreate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "xt")
	cmd.Env = append(env, "OPENAI_API_KEY=sk-cenci-test-secret") // satisfies the codex auth gate
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open xt: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	rmIdx := -1
	runIdx := -1
	for i, l := range lines {
		if l == "rm codex-cenci-default" {
			rmIdx = i
		}
		if strings.HasPrefix(l, "run --name codex-cenci-default") {
			runIdx = i
		}
	}
	if rmIdx == -1 {
		t.Fatalf("expected 'rm codex-cenci-default' before creating the container, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if runIdx == -1 {
		t.Fatalf("expected a 'run --name codex-cenci-default' create call, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if rmIdx > runIdx {
		t.Errorf("expected rm (index %d) to run before create (index %d), got calls:\n%s", rmIdx, runIdx, strings.Join(lines, "\n"))
	}
}

// TestOpenCodex_ForwardsProviderKeyPerExecOnly pins Codex's OPENAI_API_KEY
// forwarding to the per-exec-only model OpenCode already uses (#490/#509):
// the env var alone must satisfy the codex auth gate (no ~/.codex/auth.json
// staged) and reach the exec-time attach, but it must never appear in the
// create-time `run --name` args — else it would sit in the container's PID-1
// environ for the container's whole lifetime, readable via `docker inspect`/
// `/proc/1/environ` (#510). The attach exec argv itself must carry the bare
// "-e OPENAI_API_KEY" token only, never the secret VALUE — the full argv
// stays live in `ps` for the whole interactive session via execAttach's
// syscall.Exec handoff (#759), so the runtime CLI reads the value from its
// own inherited environment instead.
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
	if !strings.Contains(line, "-e OPENAI_API_KEY") {
		t.Errorf("attach argv missing per-exec OPENAI_API_KEY forwarding:\n%s", line)
	}
	if strings.Contains(line, "sk-cenci-test-secret") {
		t.Errorf("attach argv must never carry the OPENAI_API_KEY secret VALUE (readable via ps for the whole session, #759); got:\n%s", line)
	}
	if strings.Contains(line, "OPENAI_API_KEY=") {
		t.Errorf("attach argv must forward OPENAI_API_KEY value-less (bare -e NAME, letting the runtime CLI read its own inherited environment, #759); got:\n%s", line)
	}
}

// Authenticated OpenCode launches also refuse before runtime side effects.
func TestOpenOpencodeDisabledWithCredentialsDoesNotTouchRuntime(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	cmd := exec.Command(binaryPath, "open", "--agent", "opencode")
	cmd.Env = append(env, "ANTHROPIC_API_KEY=sk-test-anthropic", "OPENAI_API_KEY=sk-test-openai")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "OpenCode is temporarily disabled") {
		t.Fatalf("expected pause refusal: %v %s", err, out)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Fatalf("disabled launch touched runtime: %v", lines)
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

// hasBareEnvToken reports whether the space-joined argv line carries the
// value-less `-e NAME` pair for name (an exact-token match, so the
// value-bearing `-e NAME=value` form never counts).
func hasBareEnvToken(line, name string) bool {
	args := strings.Fields(line)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" && args[i+1] == name {
			return true
		}
	}
	return false
}

// TestOpenClaude_Context7KeyForwardedPerExecNeverAtCreateTime pins ticket
// #1201: CONTEXT7_API_KEY has no container-side consumer outside the per-exec
// agent session, so the create-time `run --name` argv must carry no mention of
// it at all (it would land in `docker inspect` and the PID-1 environ for the
// container's whole lifetime). The attach `exec -it` argv still carries the
// bare `-e CONTEXT7_API_KEY` token (#759: never the value, never `NAME=`).
// openTestEnv scrubs CONTEXT7_API_KEY= from the inherited env, and os/exec
// keeps the last duplicate key, so the sentinel is appended explicitly here.
func TestOpenClaude_Context7KeyForwardedPerExecNeverAtCreateTime(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	const context7Secret = "sk-context7-test-secret"
	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "CONTEXT7_API_KEY="+context7Secret)
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(runLine, "CONTEXT7_API_KEY") {
		t.Errorf("create-time run args must not mention CONTEXT7_API_KEY at all (#1201: no create-time forward); got:\n%s", runLine)
	}

	attach := attachLine(t, lines)
	if !hasBareEnvToken(attach, "CONTEXT7_API_KEY") {
		t.Errorf("attach exec args missing the bare -e CONTEXT7_API_KEY token; got:\n%s", attach)
	}
	if strings.Contains(attach, "CONTEXT7_API_KEY=") {
		t.Errorf("attach exec args must forward CONTEXT7_API_KEY value-less (bare -e NAME, #759); got:\n%s", attach)
	}
	if strings.Contains(attach, context7Secret) {
		t.Errorf("attach exec args must never carry the CONTEXT7_API_KEY secret VALUE (#759); got:\n%s", attach)
	}
}

// TestOpenClaude_EmptyContext7Key_NoTokenAnywhere pins the empty-string edge
// case: a host CONTEXT7_API_KEY set to the empty string (openTestEnv's own
// scrub value) is "unset" for forwarding, so neither the create argv nor the
// attach argv carries a `-e CONTEXT7_API_KEY` token. (The per-agent matrix for
// codex/opencode lives in the launcher package's unit test.)
func TestOpenClaude_EmptyContext7Key_NoTokenAnywhere(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "CONTEXT7_API_KEY=")
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open ch: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if hasBareEnvToken(runLine, "CONTEXT7_API_KEY") {
		t.Errorf("create-time run args carry -e CONTEXT7_API_KEY with an empty host value; got:\n%s", runLine)
	}
	if attach := attachLine(t, lines); hasBareEnvToken(attach, "CONTEXT7_API_KEY") {
		t.Errorf("attach exec args carry -e CONTEXT7_API_KEY with an empty host value; got:\n%s", attach)
	}
}

// TestOpenAttachToRunning_StaleHostKey_PerExecHostStateWins documents the
// accepted #1201 delta: a container created while the host CONTEXT7_API_KEY
// was set, later attached to with the key unset, gets an attach argv that
// carries no `-e CONTEXT7_API_KEY`. Per-exec host state wins, the same "set
// and non-empty at exec time" gate the provider keys and CENCI_ATTENDED use.
// The second launch attaches to the now-running container (FAKE_PS) with the
// key scrubbed and must carry no CONTEXT7_API_KEY token. This pins only the
// cenci-built argv: the fake runtime cannot model docker's Config.Env
// inheritance, and only containers created after #1201 stop receiving a
// create-time copy. A container created before this change keeps its old
// create-time copy until `cenci sandbox stop <name>` and a relaunch.
// Written as a characterization on the pre-change code (where nothing reads
// a create-time copy at attach), and it must keep passing after the
// create-time forward is deleted.
func TestOpenAttachToRunning_StaleHostKey_PerExecHostStateWins(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, socketDir := openTestEnv(t, fakeDir, assets)
	launchDir := t.TempDir()

	// Launch 1: container absent, key set -> creates it and attaches.
	create := exec.Command(binaryPath, "open", "ch")
	create.Env = append(slices.Clone(env), "CONTEXT7_API_KEY=sk-context7-created-with")
	create.Dir = launchDir
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("launch 1 (create): %v\n%s", err, output)
	}
	if _, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name "); !ok {
		t.Fatalf("launch 1 must create the container, got calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
	}

	// Launch 2: container running, key unset on the host.
	before := len(callLogLines(t, callLog))
	attach := exec.Command(binaryPath, "open", "ch")
	attach.Env = append(slices.Clone(env),
		"CONTEXT7_API_KEY=",
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
	)
	attach.Dir = launchDir
	if output, err := attach.CombinedOutput(); err != nil {
		t.Fatalf("launch 2 (attach to running): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)[before:]
	if _, ok := findLineWithPrefix(lines, "run --name "); ok {
		t.Fatalf("launch 2 must reuse the running container, got calls:\n%s", strings.Join(lines, "\n"))
	}
	line := attachLine(t, lines)
	if strings.Contains(line, "CONTEXT7_API_KEY") {
		t.Errorf("attach with the host key unset must carry no CONTEXT7_API_KEY token (per-exec host state wins); got:\n%s", line)
	}
}

// A missing credential must not mask the explicit product pause.
func TestOpenOpencodeWithoutAuth_ReportsDisabled(t *testing.T) {
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
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "OpenCode is temporarily disabled") {
		t.Errorf("expected an OpenCode pause refusal, got:\n%s", output)
	}
	if lines := callLogLines(t, callLog); anyLineContains(lines, "run --name ") {
		t.Errorf("expected no workload container run without opencode auth, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpen_AttachToRunning_SkipsCreate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, socketDir := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
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
	if strings.Contains(string(output), "stale cenci socket mount") {
		t.Errorf("a matching-source cenci socket mount must not trigger the stale-mount warning, got:\n%s", output)
	}
}

// TestOpen_RunningContainer_StaleAgentVolume_SkipsRefresh pins ticket #710's
// attach skip: EnsureAgentVolume's TTL staleness branch must be skipped
// entirely (no updater run) when the scoped container is already running —
// an attach must stay instant — even though the shared agent volume itself
// is well past the TTL. The populated/bootstrap check still runs (it's not
// container-scoped: the volume is host-global).
func TestOpen_RunningContainer_StaleAgentVolume_SkipsRefresh(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	stale := time.Now().Add(-25 * time.Hour)
	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		agentStatusFakeLine("", stale, time.Time{}),
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch (running, stale shared agent volume): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if !anyLineContains(lines, "agent-cli.sh status claude") {
		t.Errorf("expected the populated/bootstrap probe to still run on the attach path, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "agent-cli.sh update") {
		t.Errorf("expected a running (attach) container to skip the TTL staleness refresh even with a stale shared agent volume; calls:\n%s", strings.Join(lines, "\n"))
	}
	attachLine(t, lines)
}

// TestOpen_ContainerRunningProbeFailure_AbortsBeforeAgentVolumeBootstrap pins
// the ticket's ordering regression (watch/docs/test-strategy.md #620):
// hoisting containerRunning into Launch (between EnsureImage and
// EnsureAgentVolume) means a `ps` failure now aborts BEFORE the agent-volume
// bootstrap/refresh probe ever runs — not after it, as it did pre-#710.
// EnsureAgentVolume's very first side effect is volumeExists (`docker
// volume ls`), so its absence from the call log proves EnsureAgentVolume
// was never entered.
func TestOpen_ContainerRunningProbeFailure_AbortsBeforeAgentVolumeBootstrap(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env, "FAKE_PS_EXIT=1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected a ps-failure runtime error, got %T %v\n%s", err, err, output)
	}

	lines := callLogLines(t, callLog)
	if anyLineContains(lines, "volume ls") {
		t.Errorf("a containerRunning (ps) failure must abort before EnsureAgentVolume's volumeExists probe ever runs (#620 ordering); calls:\n%s", strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "agent-cli.sh") {
		t.Errorf("a ps failure must abort before any agent-volume bootstrap/refresh work; calls:\n%s", strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "exec -it") {
		t.Errorf("attach must not run after a ps failure")
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

// -- ticket #628: reject legacy socket mounts and DinD-mode mismatches ----
//
// Before attaching to a running same-name container, planArgvs must also
// inspect its mounts/runtime/DinD env/storage mount (inspectReusePosture)
// and reject: a host Docker/Podman socket exposure (independent of DinD
// posture), a requested/derived DinD mismatch, and an ambiguous
// (unrecognized) cenci-sand.dind label -- all with the same
// "cenci sandbox stop <name>", then relaunch instruction as the sibling
// "predates shared read-only agent CLIs" rejection, and all provably never
// reaching the interactive agent exec (anyLineContains(lines, "exec -it")
// is false), per watch/docs/error-handling.md #446 (content-specific error
// assertions distinguishing each rejection class).

func TestOpen_RunningContainerWithHostSocketMount_RejectsEvenWithCompatibleAgentMount(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		// FAKE_AGENT_MOUNTS left at its default (compatible shared agent-CLI
		// mount present) -- the socket rejection must fire even when the
		// agent-CLI mount is otherwise fine.
		"FAKE_REUSE_POSTURE=off|runc|0\n/var/run/docker.sock::/var/run/docker.sock\n\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the host-socket rejection to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "host Docker/Podman socket") {
		t.Errorf("expected the rejection to name the host Docker/Podman socket, got:\n%s", output)
	}
	if !strings.Contains(string(output), "cenci sandbox stop claude-cenci-default") || !strings.Contains(string(output), "then relaunch") {
		t.Errorf("missing precise migration instruction:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("a container exposing a host socket must not receive an agent session")
	}
}

func TestOpenDind_OntoDerivedNonDindContainer_Rejects(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, false)
	containerName := "claude-cenci-" + launcher.Slugify(filepath.Base(repoRoot))

	fakeDir := t.TempDir()
	// docker-only: dind's preflight requires Docker as the outer runtime.
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env,
		`FAKE_INFO_RUNTIMES={"sysbox-runc":{},"runc":{}}`,
		"FAKE_PS="+containerName+"\n",
		"FAKE_REUSE_POSTURE=off|runc|0\nworkspace-vol::/workspace\n\n",
	)

	cmd := exec.Command(binaryPath, "open", "--dind")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the --dind/non-dind-container mismatch to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "without --dind") {
		t.Errorf("expected the rejection to mention the container was created without --dind, got:\n%s", output)
	}
	if !strings.Contains(string(output), "cenci sandbox stop "+containerName) || !strings.Contains(string(output), "then relaunch") {
		t.Errorf("missing precise migration instruction:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("a DinD-mode-mismatched container must not receive an agent session")
	}
}

func TestOpen_DefaultOntoDerivedDindContainer_Rejects(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_REUSE_POSTURE=on|sysbox-runc|1\nworkspace-vol::/workspace\ndind-vol::/var/lib/docker\n\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the default(--no-dind)/dind-container mismatch to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "with --dind") {
		t.Errorf("expected the rejection to mention the container was created with --dind, got:\n%s", output)
	}
	if !strings.Contains(string(output), "cenci sandbox stop claude-cenci-default") || !strings.Contains(string(output), "then relaunch") {
		t.Errorf("missing precise migration instruction:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("a DinD-mode-mismatched container must not receive an agent session")
	}
}

func TestOpen_AmbiguousDindLabel_Rejects(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_REUSE_POSTURE=maybe|runc|0\nworkspace-vol::/workspace\n\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the ambiguous-label rejection to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "ambiguous") {
		t.Errorf("expected the rejection to call the posture ambiguous (watch/docs/go-gotchas.md #598: never collapse an unrecognized enum value into the safest-looking case), got:\n%s", output)
	}
	if !strings.Contains(string(output), "cenci sandbox stop claude-cenci-default") || !strings.Contains(string(output), "then relaunch") {
		t.Errorf("missing precise migration instruction:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("a container with ambiguous DinD posture must not receive an agent session")
	}
}

// TestOpen_MalformedReusePostureHeader_Rejects covers the Critical
// silent-failure finding on ticket #628's parseReusePosture: a scripted
// docker fake answering the reuse-posture inspect with a header line that
// exits 0 but doesn't match the expected "<label>|<runtime>|<dindenv>"
// shape (fewer than 3 "|"-delimited fields) must reject the reuse attempt
// -- fail closed -- rather than silently defaulting to the zero-value
// reusePosture{}, which derives to the fully permissive "no host socket,
// dindOff" outcome (indistinguishable from a legitimately compatible
// legacy container).
func TestOpen_MalformedReusePostureHeader_Rejects(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_REUSE_POSTURE=garbage-no-pipes\n\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the malformed reuse-posture header to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "reuse posture") {
		t.Errorf("expected the rejection to mention the reuse posture parse failure, got:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("a container with malformed reuse-posture inspect output must not receive an agent session")
	}
}

// TestOpen_MalformedReusePostureMountLine_Rejects covers the second gap
// this finding calls out: a mount line that fails to split on "::" must be
// treated as a parse failure (fail closed), not silently dropped -- the
// dropped line could be exactly the host-socket bind or DinD storage mount
// the security checks depend on. The malformed line is deliberately followed
// by the trailing blank line real `docker inspect` always appends (ticket
// #684), pinning that the trailing-blank-line tolerance only trims a
// genuinely-trailing empty line and never masks a malformed non-trailing one.
func TestOpen_MalformedReusePostureMountLine_Rejects(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_REUSE_POSTURE=off|runc|0\nno-delimiter-here\n\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the malformed reuse-posture mount line to exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "reuse posture") {
		t.Errorf("expected the rejection to mention the reuse posture parse failure, got:\n%s", output)
	}
	if anyLineContains(callLogLines(t, callLog), "exec -it") {
		t.Errorf("a container with an unparseable mount line must not receive an agent session")
	}
}

func TestOpen_CompatibleNormalContainer_ReusesSuccessfully(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_REUSE_POSTURE=off|runc|0\nworkspace-vol::/workspace\n\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch (compatible normal reuse): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if _, ok := findLineWithPrefix(lines, "run --name "); ok {
		t.Errorf("expected no container create when reusing a compatible normal container, got calls:\n%s", strings.Join(lines, "\n"))
	}
	attachLine(t, lines)
}

func TestOpenDind_CompatibleDindContainer_ReusesSuccessfully(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, false)
	containerName := "claude-cenci-" + launcher.Slugify(filepath.Base(repoRoot))

	fakeDir := t.TempDir()
	// docker-only: dind's preflight requires Docker as the outer runtime.
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env,
		`FAKE_INFO_RUNTIMES={"sysbox-runc":{},"runc":{}}`,
		"FAKE_PS="+containerName+"\n",
		"FAKE_REUSE_POSTURE=on|sysbox-runc|1\nworkspace-vol::/workspace\ndind-vol::/var/lib/docker\n\n",
	)

	cmd := exec.Command(binaryPath, "open", "--dind")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open --dind (compatible dind reuse): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if _, ok := findLineWithPrefix(lines, "run --name "); ok {
		t.Errorf("expected no container create when reusing a compatible DinD container, got calls:\n%s", strings.Join(lines, "\n"))
	}
	attachLine(t, lines)
}

// -- ticket #628: new containers stamp an authoritative DinD-mode label ----

func TestOpen_FreshCreate_StampsDindLabelOff(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(runLine, "--label cenci-sand.dind=off") {
		t.Errorf("expected the new container to be stamped --label cenci-sand.dind=off, got:\n%s", runLine)
	}
}

func TestOpenDind_FreshCreate_StampsDindLabelOn(t *testing.T) {
	repoRoot, _ := dindRepoEnv(t, true)

	fakeDir := t.TempDir()
	// docker-only: dind's preflight requires Docker as the outer runtime.
	callLog := writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, `FAKE_INFO_RUNTIMES={"sysbox-runc":{},"runc":{}}`)

	cmd := exec.Command(binaryPath, "open")
	cmd.Env = env
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open (dind via repo config): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(runLine, "--label cenci-sand.dind=on") {
		t.Errorf("expected the new dind container to be stamped --label cenci-sand.dind=on, got:\n%s", runLine)
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
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n", // no cenci socket mount
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

// TestOpen_AttachToRunning_StaleSocketMount_Warns pins the new (#1143)
// stale-mount check: a reused container whose cenci socket mount's
// DESTINATION matches (so the existing #195 "created without cenci wiring"
// check sees it as wired) but whose SOURCE is the old host socket path (the
// pre-#1146 default, coincidentally identical to the in-container
// destination) — while CENCI_SOCKET_DIR now resolves the host socket dir to
// a different path (openTestEnv's fresh t.TempDir()) — must still warn, but
// with wording distinct from the "created without cenci wiring" line (that
// literal is asserted for the missing-mount case above and must not appear
// here), naming stop-and-relaunch as the remedy.
func TestOpen_AttachToRunning_StaleSocketMount_Warns(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"FAKE_PS=claude-cenci-default\n",
		"FAKE_INSPECT_LABEL=detached",
		// Source is the pre-#1146 default host socket path, which
		// coincidentally is the same string as the in-container destination;
		// the currently-resolved host socket dir (CENCI_SOCKET_DIR, set by
		// openTestEnv to a fresh t.TempDir()) is guaranteed to differ.
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n/run/user/1000/cenci::/run/user/1000/cenci\n",
	)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open ch (stale socket mount): %v\n%s", err, output)
	}

	out := string(output)
	if strings.Contains(out, "was created without cenci wiring") {
		t.Errorf("a stale-source mismatch must use wording distinct from the missing-mount warning, got:\n%s", out)
	}
	if !strings.Contains(out, "stale cenci socket mount") {
		t.Errorf("expected a stale cenci socket mount warning, got:\n%s", out)
	}
	if !strings.Contains(out, "stop claude-cenci-default") {
		t.Errorf("expected the warning to name the 'stop' remedy, got:\n%s", out)
	}
	if !strings.Contains(out, "relaunch") {
		t.Errorf("expected the warning to name relaunching as part of the remedy, got:\n%s", out)
	}
	attachLine(t, callLogLines(t, callLog))
}

func TestOpen_NoEventsSocket_LaunchesUnwiredWithWarning(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	// Point the socket dir at a fresh dir with no live socket. CENCI_SANDBOX=1
	// keeps daemon.EnsureRunning inert (the launcher reads it nowhere else),
	// so the subprocess never spawns a real daemon; the 3s socket poll then
	// expires.
	cmd := exec.Command(binaryPath, "open", "ch")
	cmd.Env = append(env,
		"CENCI_SOCKET_DIR="+t.TempDir(),
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

// -- ticket #737: cenci open --add-dir -----------------------------------------
//
// Black-box coverage of the real binary: the create argv's /repos binds, the
// forwarded single-token --add-dir=/repos/<slug> on the exec line that Launch
// actually hands to the runtime, the usage-error table (exit 2, one stderr
// line naming the path, an empty call log), and the both-directions reuse
// check against a running container.

// addDirOpenFixture is a git repo nested away from $HOME (so its parent is a
// legitimate "parent of the primary repo" case), the scripted runtimes, and
// the isolated open environment.
type addDirOpenFixture struct {
	env       []string
	callLog   string
	home      string
	socketDir string
	repo      string
	container string
}

func newAddDirOpenFixture(t *testing.T) addDirOpenFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "work", "app", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	env, home, socketDir := openTestEnv(t, fakeDir, writeAssetFixture(t))
	// An ambient ssh-agent or tmux socket directory is a denylist entry (#737):
	// scrub them so a developer's own SSH_AUTH_SOCK=/tmp/agent.sock cannot make
	// every temp-dir target look sensitive.
	env = append(env, "SSH_AUTH_SOCK=", "TMUX_TMPDIR=")
	stageCodexAuth(t, home)
	return addDirOpenFixture{env: env, callLog: callLog, home: home, socketDir: socketDir, repo: repo, container: containerNameFor(repo)}
}

// addDirTarget makes a fresh resolved directory called name outside the repo
// and $HOME.
func addDirTarget(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func mkdirAll(t *testing.T, parts ...string) string {
	t.Helper()
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runningEnv scripts a running, detached-labeled, otherwise compatible
// container named f.container whose reuse-inspect reports the given
// "<source>::<destination>" mounts after the workspace mount.
func (f addDirOpenFixture) runningEnv(mounts ...string) []string {
	return f.runningEnvNamed(f.container, mounts...)
}

// runningEnvNamed is runningEnv for a running container called name (the
// default name or a --name-suffixed one); no other container is running.
func (f addDirOpenFixture) runningEnvNamed(name string, mounts ...string) []string {
	posture := "off|runc|0\nworkspace-vol::/workspace\n"
	for _, m := range mounts {
		posture += m + "\n"
	}
	return append(slices.Clone(f.env),
		"FAKE_PS="+name+"\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+f.socketDir+"::/run/user/1000/cenci\n",
		"FAKE_CONTAINER_ENV="+defaultPluginsContainerEnv,
		"FAKE_REUSE_POSTURE="+posture+"\n",
	)
}

func runLineOf(t *testing.T, lines []string) string {
	t.Helper()
	line, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container create, got calls:\n%s", strings.Join(lines, "\n"))
	}
	return line
}

func TestOpenAddDir_Claude_MountsEachDirReadWriteAndForwardsSingleTokensAfterTheModel(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a, b := addDirTarget(t, "notes-repo"), addDirTarget(t, "Shared Lib")

	run := runCenci(t, f.env, f.repo, "open", "--add-dir", a, "--add-dir", b)
	if run.exitCode != 0 {
		t.Fatalf("open --add-dir: exit %d\n%s", run.exitCode, run.stderr)
	}

	lines := callLogLines(t, f.callLog)
	create := runLineOf(t, lines)
	for _, pair := range []string{a + ":/repos/notes-repo", b + ":/repos/shared-lib"} {
		if !strings.Contains(create, " -v "+pair+" ") {
			t.Errorf("create argv missing the read-write bind %q:\n%s", "-v "+pair, create)
		}
	}
	// The added dirs never change the container's identity, volume or image.
	for _, want := range []string{"--name " + f.container + " ", "-v claude-cenci-home-" + strings.TrimPrefix(f.container, "claude-cenci-") + ":/home/dev", " -v " + f.repo + ":/workspace "} {
		if !strings.Contains(create, want) {
			t.Errorf("create argv missing %q:\n%s", want, create)
		}
	}
	attach := attachLine(t, lines)
	wantTail := "/opt/cenci-agent/current/node_modules/.bin/claude --dangerously-skip-permissions --model sonnet --add-dir=/repos/notes-repo --add-dir=/repos/shared-lib"
	if !strings.HasSuffix(attach, wantTail) {
		t.Errorf("attach argv = %q, want it to end with %q", attach, wantTail)
	}
}

func TestOpenAddDir_Codex_ForwardsTheSameSingleTokensAfterTheModel(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a, b := addDirTarget(t, "notes-repo"), addDirTarget(t, "other")

	run := runCenci(t, f.env, f.repo, "open", "--agent", "codex", "--add-dir", a, "--add-dir", b)
	if run.exitCode != 0 {
		t.Fatalf("open --agent codex --add-dir: exit %d\n%s", run.exitCode, run.stderr)
	}

	lines := callLogLines(t, f.callLog)
	create := runLineOf(t, lines)
	for _, pair := range []string{a + ":/repos/notes-repo", b + ":/repos/other"} {
		if !strings.Contains(create, " -v "+pair+" ") {
			t.Errorf("create argv missing %q:\n%s", "-v "+pair, create)
		}
	}
	wantTail := "/opt/cenci-agent/current/node_modules/.bin/codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust --model gpt-5.6-terra --add-dir=/repos/notes-repo --add-dir=/repos/other"
	if attach := attachLine(t, lines); !strings.HasSuffix(attach, wantTail) {
		t.Errorf("attach argv = %q, want it to end with %q", attach, wantTail)
	}
}

func TestOpenAddDir_Shell_StillMountsButAttachesBashWithNoToken(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a := addDirTarget(t, "notes-repo")

	run := runCenci(t, f.env, f.repo, "open", "--shell", "--add-dir", a)
	if run.exitCode != 0 {
		t.Fatalf("open --shell --add-dir: exit %d\n%s", run.exitCode, run.stderr)
	}

	lines := callLogLines(t, f.callLog)
	if create := runLineOf(t, lines); !strings.Contains(create, " -v "+a+":/repos/notes-repo ") {
		t.Errorf("--shell must still mount the added dir:\n%s", create)
	}
	attach := attachLine(t, lines)
	if !strings.HasSuffix(attach, " /bin/bash") || strings.Contains(attach, "--add-dir") {
		t.Errorf("attach argv = %q, want a bare /bin/bash with no --add-dir token", attach)
	}
}

func TestOpenAddDir_PositionalPassthroughPromptStaysRightAfterTheLastToken(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a := addDirTarget(t, "notes-repo")

	run := runCenci(t, f.env, f.repo, "open", "--add-dir", a, "--", "fix the flaky test")
	if run.exitCode != 0 {
		t.Fatalf("open --add-dir -- <prompt>: exit %d\n%s", run.exitCode, run.stderr)
	}

	attach := attachLine(t, callLogLines(t, f.callLog))
	if !strings.HasSuffix(attach, "--model sonnet --add-dir=/repos/notes-repo fix the flaky test") {
		t.Errorf("attach argv = %q, want the prompt immediately after the --add-dir= token", attach)
	}
}

func TestOpenAddDir_UserPassthroughAddDirIsForwardedVerbatimWithNoMount(t *testing.T) {
	f := newAddDirOpenFixture(t)

	run := runCenci(t, f.env, f.repo, "open", "--", "--add-dir", "/x")
	if run.exitCode != 0 {
		t.Fatalf("open -- --add-dir /x: exit %d\n%s", run.exitCode, run.stderr)
	}

	lines := callLogLines(t, f.callLog)
	if create := runLineOf(t, lines); strings.Contains(create, "/repos/") {
		t.Errorf("a passthrough --add-dir must not create a mount:\n%s", create)
	}
	if attach := attachLine(t, lines); !strings.HasSuffix(attach, "--model sonnet --add-dir /x") {
		t.Errorf("attach argv = %q, want the user's own `--add-dir /x` verbatim", attach)
	}
}

func TestOpenAddDir_RelativePathAndSymlinkAndRepeat_ResolveToOneAbsoluteBind(t *testing.T) {
	f := newAddDirOpenFixture(t)
	target := addDirTarget(t, "notes-repo")
	link := filepath.Join(t.TempDir(), "short")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(f.repo, target)
	if err != nil {
		t.Fatal(err)
	}

	run := runCenci(t, f.env, f.repo, "open", "--add-dir", rel, "--add-dir", link, "--add-dir", target)
	if run.exitCode != 0 {
		t.Fatalf("open --add-dir x3: exit %d\n%s", run.exitCode, run.stderr)
	}

	lines := callLogLines(t, f.callLog)
	create := runLineOf(t, lines)
	if got := strings.Count(create, ":/repos/"); got != 1 {
		t.Errorf("three spellings of one dir produced %d binds, want exactly 1:\n%s", got, create)
	}
	if !strings.Contains(create, " -v "+target+":/repos/notes-repo ") {
		t.Errorf("bind source must be the absolute symlink-resolved path %q:\n%s", target, create)
	}
	if got := strings.Count(attachLine(t, lines), "--add-dir="); got != 1 {
		t.Errorf("attach argv forwarded --add-dir= %d times, want exactly 1", got)
	}
}

func TestOpenAddDir_CnAlias_AcceptsTheFlag(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a := addDirTarget(t, "notes-repo")
	cnPath := buildArgv0Alias(t, t.TempDir(), "cn")

	cmd := exec.Command(cnPath, "--add-dir", a)
	cmd.Env = f.env
	cmd.Dir = f.repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn --add-dir: %v\n%s", err, output)
	}

	lines := callLogLines(t, f.callLog)
	if create := runLineOf(t, lines); !strings.Contains(create, " -v "+a+":/repos/notes-repo ") {
		t.Errorf("cn did not mount the added dir:\n%s", create)
	}
	if attach := attachLine(t, lines); !strings.HasSuffix(attach, "--add-dir=/repos/notes-repo") {
		t.Errorf("cn attach argv = %q, want the forwarded token", attach)
	}
}

func TestOpenAddDir_NameFlag_CreatesTheSeparateMultiRepoContainerLeavingTheDefaultAlone(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a := addDirTarget(t, "notes-repo")
	// The default container is running WITHOUT added dirs: had --name not
	// scoped the launch to its own container, this would be a reuse mismatch.
	env := f.runningEnv()

	run := runCenci(t, env, f.repo, "open", "--name", "multi", "--add-dir", a)
	if run.exitCode != 0 {
		t.Fatalf("open --name multi --add-dir: exit %d\n%s", run.exitCode, run.stderr)
	}

	multi := f.container + "-multi"
	lines := callLogLines(t, f.callLog)
	if create := runLineOf(t, lines); !strings.Contains(create, "--name "+multi+" ") || !strings.Contains(create, " -v "+a+":/repos/notes-repo ") {
		t.Errorf("create argv = %q, want a fresh %s with the bind", create, multi)
	}
	if attach := attachLine(t, lines); !strings.Contains(attach, " "+multi+" /opt/cenci-agent/") {
		t.Errorf("attach argv = %q, want it aimed at %s", attach, multi)
	}
	wantNoCallTargetsDefaultContainer(t, lines, f.container, multi)
}

// wantNoCallTargetsDefaultContainer fails when any runtime call names the
// default container: the -multi name is removed first so it cannot match the
// default name as a prefix.
func wantNoCallTargetsDefaultContainer(t *testing.T, lines []string, container, multi string) {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(strings.ReplaceAll(line, multi, ""), container) {
			t.Errorf("runtime call %q targets the default container %s, which --name must leave alone", line, container)
		}
	}
}

func TestOpenAddDir_NameFlag_ReusesTheRunningMultiRepoContainerLeavingTheDefaultAlone(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a := addDirTarget(t, "notes-repo")
	multi := f.container + "-multi"
	// Only the -multi container is running, with the matching /repos mount.
	env := f.runningEnvNamed(multi, a+"::/repos/notes-repo")

	run := runCenci(t, env, f.repo, "open", "--name", "multi", "--add-dir", a)
	if run.exitCode != 0 {
		t.Fatalf("open --name multi --add-dir: exit %d\n%s", run.exitCode, run.stderr)
	}

	lines := callLogLines(t, f.callLog)
	if _, ok := findLineWithPrefix(lines, "run --name "); ok {
		t.Errorf("a matching running %s must be attached to, never recreated, got calls:\n%s", multi, strings.Join(lines, "\n"))
	}
	if attach := attachLine(t, lines); !strings.Contains(attach, " "+multi+" /opt/cenci-agent/") {
		t.Errorf("attach argv = %q, want it aimed at %s", attach, multi)
	}
	wantNoCallTargetsDefaultContainer(t, lines, f.container, multi)
}

// -- usage errors: exit 2, one stderr line naming the path, no runtime call ----

func TestOpenAddDir_RejectedPaths_Exit2WithOneLineHintAndMakeNoRuntimeCall(t *testing.T) {
	f := newAddDirOpenFixture(t)
	file := filepath.Join(addDirTarget(t, "files"), "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeDir := mkdirAll(t, f.home, ".claude")
	sharedA, sharedB := addDirTarget(t, "shared"), addDirTarget(t, "shared")

	cases := []struct {
		name   string
		paths  []string
		marker string
	}{
		{"non-existent path", []string{filepath.Join(f.home, "..", "no-such-dir")}, "does not exist"},
		{"regular file", []string{file}, "not a directory"},
		{"two dirs with colliding slugs", []string{sharedA, sharedB}, "both mount at"},
		{"primary repo root", []string{f.repo}, "overlaps the primary workspace"},
		{"subdirectory of the primary repo", []string{mkdirAll(t, f.repo, "pkg")}, "overlaps the primary workspace"},
		{"parent of the primary repo", []string{filepath.Dir(f.repo)}, "overlaps the primary workspace"},
		{"filesystem root", []string{"/"}, "sensitive host location"},
		{"home directory", []string{f.home}, "sensitive host location"},
		{"parent of home", []string{filepath.Dir(f.home)}, "sensitive host location"},
		{"~/.ssh", []string{mkdirAll(t, f.home, ".ssh")}, "sensitive host location"},
		{"inside ~/.ssh", []string{mkdirAll(t, f.home, ".ssh", "keys")}, "sensitive host location"},
		{"~/.gnupg", []string{mkdirAll(t, f.home, ".gnupg")}, "sensitive host location"},
		{"directory holding a credential file (~/.claude)", []string{claudeDir}, "sensitive host location"},
		{"inside a credential directory", []string{mkdirAll(t, claudeDir, "projects")}, "sensitive host location"},
		{"cenci fleet config dir", []string{mkdirAll(t, f.home, ".config", "cenci")}, "sensitive host location"},
		{"cenci state dir", []string{mkdirAll(t, f.home, ".local", "state", "cenci")}, "sensitive host location"},
		{"cenci socket dir", []string{f.socketDir}, "sensitive host location"},
		{"cenci install link dir", []string{mkdirAll(t, f.home, ".local", "bin")}, "sensitive host location"},
		{"system directory /etc", []string{"/etc"}, "sensitive host location"},
		{"system directory /usr", []string{"/usr"}, "sensitive host location"},
		{"system directory /dev", []string{"/dev"}, "sensitive host location"},
		{"shared tmp directory /tmp (host tmux and ssh-agent sockets)", []string{"/tmp"}, "sensitive host location"},
		{"shared state parent /var", []string{"/var"}, "sensitive host location"},
		{"container runtime socket dir ~/.docker", []string{mkdirAll(t, f.home, ".docker")}, "sensitive host location"},
		{"container runtime socket dir ~/.colima", []string{mkdirAll(t, f.home, ".colima")}, "sensitive host location"},
		{"container runtime socket dir ~/.orbstack", []string{mkdirAll(t, f.home, ".orbstack")}, "sensitive host location"},
		{"inside ~/.docker", []string{mkdirAll(t, f.home, ".docker", "run")}, "sensitive host location"},
		{"cenci autostart unit dir", []string{mkdirAll(t, f.home, ".config", "systemd", "user")}, "sensitive host location"},
		{"login autostart dir", []string{mkdirAll(t, f.home, ".config", "autostart")}, "sensitive host location"},
		{"launchd agents dir", []string{mkdirAll(t, f.home, "Library", "LaunchAgents")}, "sensitive host location"},
		{"empty path", []string{""}, "non-empty"},
		{"colon in path", []string{filepath.Join(f.home, "..", "a:b")}, "':' or a non-printable character"},
		{"newline in path", []string{filepath.Join(f.home, "..", "a\nb")}, "':' or a non-printable character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Truncate(f.callLog, 0); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			args := []string{"open"}
			for _, p := range tc.paths {
				args = append(args, "--add-dir", p)
			}
			run := runCenci(t, f.env, f.repo, args...)

			if run.exitCode != 2 {
				t.Fatalf("exit code = %d, want 2\nstderr: %s", run.exitCode, run.stderr)
			}
			stderr := strings.TrimRight(run.stderr, "\n")
			if stderr == "" || strings.Contains(stderr, "\n") {
				t.Errorf("stderr = %q, want exactly one hint line", run.stderr)
			}
			if !strings.Contains(stderr, tc.marker) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.marker)
			}
			offending := tc.paths[len(tc.paths)-1]
			if !strings.Contains(stderr, strconv.Quote(offending)) {
				t.Errorf("stderr = %q, want it to name the offending path %s", stderr, strconv.Quote(offending))
			}
			if lines := callLogLines(t, f.callLog); len(lines) != 0 {
				t.Errorf("a rejected --add-dir made runtime calls:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

func TestOpenAddDir_LinkedWorktree_Exits2AndHintNamesTheMainCheckout(t *testing.T) {
	f := newAddDirOpenFixture(t)
	elsewhere := addDirTarget(t, "elsewhere")
	mainCheckout := mkdirAll(t, elsewhere, "main-checkout")
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-q", filepath.Join(elsewhere, "wt")},
	} {
		if out, err := exec.Command("git", append([]string{"-C", mainCheckout}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	worktree := filepath.Join(elsewhere, "wt")

	run := runCenci(t, f.env, f.repo, "open", "--add-dir", worktree)

	if run.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr: %s", run.exitCode, run.stderr)
	}
	stderr := strings.TrimRight(run.stderr, "\n")
	if strings.Contains(stderr, "\n") || !strings.Contains(stderr, "linked git worktree") || !strings.Contains(stderr, "main checkout "+strconv.Quote(mainCheckout)) {
		t.Errorf("stderr = %q, want one line naming the main checkout %s", stderr, strconv.Quote(mainCheckout))
	}
	if lines := callLogLines(t, f.callLog); len(lines) != 0 {
		t.Errorf("a rejected worktree made runtime calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenAddDir_DryRunWithARejectedPath_Exits2AndMakesNoRuntimeCall(t *testing.T) {
	f := newAddDirOpenFixture(t)

	run := runCenci(t, f.env, f.repo, "open", "--dry-run", "--add-dir", f.repo)

	if run.exitCode != 2 || !strings.Contains(run.stderr, "overlaps the primary workspace") {
		t.Fatalf("exit %d, stderr %q; want exit 2 with the overlap hint", run.exitCode, run.stderr)
	}
	if lines := callLogLines(t, f.callLog); len(lines) != 0 {
		t.Errorf("a rejected --dry-run --add-dir made runtime calls:\n%s", strings.Join(lines, "\n"))
	}
}

// A socket directory that cannot be resolved must not silently drop the socket
// guard: with --add-dir the launch is refused (exit 2, one line naming the
// cause, no runtime call) so an added directory can never be validated against
// an incomplete denylist.
func TestOpenAddDir_UnresolvableSocketDir_Exits2NamingTheCauseAndMakesNoRuntimeCall(t *testing.T) {
	f := newAddDirOpenFixture(t)
	target := addDirTarget(t, "notes-repo")
	env := append(slices.Clone(f.env), "CENCI_SOCKET_DIR=relative/socket-dir")

	run := runCenci(t, env, f.repo, "open", "--add-dir", target)

	if run.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr: %s", run.exitCode, run.stderr)
	}
	stderr := strings.TrimRight(run.stderr, "\n")
	if strings.Contains(stderr, "\n") || !strings.Contains(stderr, "CENCI_SOCKET_DIR") {
		t.Errorf("stderr = %q, want one line naming the CENCI_SOCKET_DIR cause", run.stderr)
	}
	if lines := callLogLines(t, f.callLog); len(lines) != 0 {
		t.Errorf("a refused --add-dir made runtime calls:\n%s", strings.Join(lines, "\n"))
	}
}

// -- reuse: the added-dir set is fixed for a container's lifetime ------------

func wantAddDirMismatch(t *testing.T, run openRun, container string, lines []string) {
	t.Helper()
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", run.exitCode, run.stderr)
	}
	want := "running container '" + container + "' was created with a different --add-dir set; run 'cenci sandbox stop " + container + "', then relaunch"
	if !strings.Contains(run.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", run.stderr, want)
	}
	if anyLineContains(lines, "exec -it") {
		t.Errorf("a mismatched container must not receive an agent session, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if _, ok := findLineWithPrefix(lines, "run --name "); ok {
		t.Errorf("a running container is never recreated automatically, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenAddDir_RunningContainerCreatedWithoutDirs_IsRejectedWithTheStopHint(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a := addDirTarget(t, "notes-repo")

	run := runCenci(t, f.runningEnv(), f.repo, "open", "--add-dir", a)

	wantAddDirMismatch(t, run, f.container, callLogLines(t, f.callLog))
}

func TestOpenAddDir_RunningContainerWithDirs_RejectsALaunchWithoutAnyFlag(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a := addDirTarget(t, "notes-repo")

	// Also the dispatch case: `cenci open` with no --add-dir aimed at a repo
	// whose running container has added dirs.
	run := runCenci(t, f.runningEnv(a+"::/repos/notes-repo"), f.repo, "open")

	wantAddDirMismatch(t, run, f.container, callLogLines(t, f.callLog))
}

func TestOpenAddDir_RunningContainerWithADifferentDirOfTheSameCount_IsRejected(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a, b := addDirTarget(t, "notes-repo"), addDirTarget(t, "other")

	// Same size, different member: a count-only comparison would attach.
	run := runCenci(t, f.runningEnv(a+"::/repos/notes-repo"), f.repo, "open", "--add-dir", b)
	wantAddDirMismatch(t, run, f.container, callLogLines(t, f.callLog))
}

func TestOpenAddDir_SameSetInADifferentOrder_AttachesNormally(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a, b := addDirTarget(t, "notes-repo"), addDirTarget(t, "other")

	for _, args := range [][]string{
		{"open", "--add-dir", a, "--add-dir", b},
		{"open", "--add-dir", b, "--add-dir", a},
	} {
		if err := os.Truncate(f.callLog, 0); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		// The container reports its mounts in yet another order.
		run := runCenci(t, f.runningEnv(b+"::/repos/other", a+"::/repos/notes-repo"), f.repo, args...)

		if run.exitCode != 0 {
			t.Fatalf("%v: exit %d, want a normal attach\n%s", args[1:], run.exitCode, run.stderr)
		}
		lines := callLogLines(t, f.callLog)
		if _, ok := findLineWithPrefix(lines, "run --name "); ok {
			t.Errorf("%v: must attach, never create, got calls:\n%s", args[1:], strings.Join(lines, "\n"))
		}
		attachLine(t, lines)
	}
}

func TestOpenAddDir_LegacyRunningContainerWithoutReposMounts_AttachesNormally(t *testing.T) {
	f := newAddDirOpenFixture(t)

	run := runCenci(t, f.runningEnv(), f.repo, "open")

	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want a normal attach\n%s", run.exitCode, run.stderr)
	}
	attachLine(t, callLogLines(t, f.callLog))
}

func TestOpenAddDir_MalformedReposMountLineInReuseInspect_FailsClosed(t *testing.T) {
	f := newAddDirOpenFixture(t)
	a := addDirTarget(t, "notes-repo")

	for badName, bad := range map[string]string{
		// A /repos destination with a single colon instead of the "::"
		// delimiter: it must hit the malformed-mount-line error, not be skipped.
		"single colon": "/srv/alpha:/repos/alpha",
		// A second "::" must not be split at the first one: that would read
		// the line as source "/x" and destination "y::/repos/z" and drop it
		// from the /repos set instead of failing closed.
		"two delimiters": "/x::y::/repos/z",
		// A bind source ending in ':' renders "/x:::/repos/y": one "::" by
		// count, but its destination would read ":/repos/y" and hide the
		// mount from the /repos set (SF1).
		"triple colon": "/x:::/repos/y",
	} {
		env := f.runningEnv(a+"::/repos/notes-repo", bad)
		for name, args := range map[string][]string{
			"with the flag":    {"open", "--add-dir", a},
			"without the flag": {"open"},
		} {
			t.Run(badName+" "+name, func(t *testing.T) {
				if err := os.Truncate(f.callLog, 0); err != nil && !errors.Is(err, fs.ErrNotExist) {
					t.Fatal(err)
				}
				run := runCenci(t, env, f.repo, args...)

				if run.exitCode != 1 || !strings.Contains(run.stderr, "reuse posture") {
					t.Fatalf("exit %d, stderr %q; want exit 1 naming the reuse-posture parse failure", run.exitCode, run.stderr)
				}
				if !strings.Contains(run.stderr, bad) {
					t.Errorf("stderr = %q, want it to quote the malformed mount line %q", run.stderr, bad)
				}
				lines := callLogLines(t, f.callLog)
				if anyLineContains(lines, "exec -it") {
					t.Errorf("an unparseable /repos line must never read as \"no added dirs\" and attach")
				}
				if _, ok := findLineWithPrefix(lines, "run --name "); ok {
					t.Errorf("an unparseable /repos line must never fall through to a create, got calls:\n%s", strings.Join(lines, "\n"))
				}
			})
		}
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

func TestCnArgv0_RoutesToOpen(t *testing.T) {
	binDir := t.TempDir()
	cnPath := buildArgv0Alias(t, binDir, "cn")

	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home, _ := openTestEnv(t, fakeDir, assets)
	stageCodexAuth(t, home)

	cmd := exec.Command(cnPath, "xs")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn xs: %v\n%s", err, output)
	}

	line := attachLine(t, callLogLines(t, callLog))
	if !strings.HasSuffix(line, "/opt/cenci-agent/current/node_modules/.bin/codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust --model gpt-6.1-sol") {
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

// -- ticket #1087: CENCI_ATTENDED reaches the container ---------------------

// TestOpenClaude_ForwardsResolvedAttendedFlag is the end-to-end half of
// #1087: the unit tests pin assembleExecEnv's resolution, this one proves the
// resolved value actually survives the whole `cenci open` path into the
// attach argv, reading the fleet config from its real resolved location
// rather than an injected path. Every host state is covered in one table so
// the "always explicit, never unset" contract is visible as a set: absent
// must stay meaningful as its own third state for the consuming skill, so an
// off, missing, or broken host flag has to say "0" rather than omit the
// variable. A fleet config whose `planning.attended` is wrong-typed still folds
// to "0" here (resolveAttendedFlag). An UNPARSABLE fleet config used to fold
// to "0" too, but since #1016 `sandboxEnv.forward` is read from the same file
// and fails closed, so `cenci open` exits 1 on it instead; see
// TestOpen_UnparsableFleetConfig_FailsClosedNamingThePath.
func TestOpenClaude_ForwardsResolvedAttendedFlag(t *testing.T) {
	cases := []struct {
		name        string
		fleetConfig string
		want        string
		// wantOrigin, when set, is the CENCI_ATTENDED_ORIGIN the attach argv
		// must carry (#1464: an unreadable planning.attended is never
		// reported as host-config).
		wantOrigin string
	}{
		{name: "attended on", fleetConfig: `{"planning": {"attended": true}}`, want: "CENCI_ATTENDED=1"},
		{name: "attended off", fleetConfig: `{"planning": {"attended": false}}`, want: "CENCI_ATTENDED=0"},
		{name: "no fleet config", fleetConfig: "", want: "CENCI_ATTENDED=0"},
		{name: "non-bool attended value", fleetConfig: `{"planning": {"attended": "yes"}}`, want: "CENCI_ATTENDED=0", wantOrigin: "CENCI_ATTENDED_ORIGIN=config-unreadable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, _ := openTestEnv(t, fakeDir, assets)

			if tc.fleetConfig != "" {
				writeFleetConfigFixture(t, home, tc.fleetConfig)
			}

			cmd := exec.Command(binaryPath, "open", "ch")
			cmd.Env = env
			cmd.Dir = t.TempDir()
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("open ch: %v\n%s", err, output)
			}

			line := attachLine(t, callLogLines(t, callLog))
			if !strings.Contains(line, "-e "+tc.want) {
				t.Errorf("attach exec args missing %q; got:\n%s", "-e "+tc.want, line)
			}
			if tc.wantOrigin != "" && !strings.Contains(line, "-e "+tc.wantOrigin) {
				t.Errorf("attach exec args missing %q; got:\n%s", "-e "+tc.wantOrigin, line)
			}
		})
	}
}

// TestOpenClaude_PinnedAttendedEnvOverridesFleetConfig proves the
// dispatch-pin precedence end to end: `cenci dispatch` spawns its window with
// CENCI_ATTENDED=0 already in the environment (run.Opts.Unattended), and that
// pin must survive `cenci open` even when the host fleet config says
// attended. A dispatched session that reached an interactive question inside
// a detached tmux window would wait there forever with its ticket on Working.
//
// #1497: `cenci babysit` pins its sessions the same way (`cenci run
// --unattended` puts CENCI_ATTENDED=0 on the window command), so this is the
// launcher side of a pinned babysit launch. Whatever the host's
// planning.attended says, the attach forwards the pin with origin `pinned`,
// never `host-config` (which would count the session as human).
func TestOpenClaude_PinnedAttendedEnvOverridesFleetConfig(t *testing.T) {
	for _, hostAttended := range []bool{true, false} {
		t.Run(fmt.Sprintf("planning.attended=%t", hostAttended), func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, _ := openTestEnv(t, fakeDir, assets)
			writeFleetConfigFixture(t, home, fmt.Sprintf(`{"planning": {"attended": %t}}`, hostAttended))

			cmd := exec.Command(binaryPath, "open", "ch")
			cmd.Env = append(env, "CENCI_ATTENDED=0")
			cmd.Dir = t.TempDir()
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("open ch: %v\n%s", err, output)
			}

			line := attachLine(t, callLogLines(t, callLog))
			if !strings.Contains(line, "-e CENCI_ATTENDED=0") {
				t.Errorf("attach exec args must honor the pinned CENCI_ATTENDED=0 over the host fleet config; got:\n%s", line)
			}
			if !strings.Contains(line, "-e CENCI_ATTENDED_ORIGIN=pinned") {
				t.Errorf("attach exec args must mark the pin with CENCI_ATTENDED_ORIGIN=pinned; got:\n%s", line)
			}
			if strings.Contains(line, "CENCI_ATTENDED_ORIGIN=host-config") {
				t.Errorf("attach exec args must never label a pinned launch host-config; got:\n%s", line)
			}
			if strings.Contains(line, "CENCI_ATTENDED=1") {
				t.Errorf("attach exec args must never forward CENCI_ATTENDED=1 for a pinned-unattended launch; got:\n%s", line)
			}
		})
	}
}

// TestOpenAttachToRunning_ReloadsAttendedFlagBetweenLaunches covers the AC that
// toggling planning.attended on the host and running `cenci open` again
// against an ALREADY-RUNNING container yields the new value with no container
// recreation. This is the whole reason the flag rides at exec time rather
// than container-create time: the sandbox container is long-lived and every
// later launch only execs into it, so a create-time forward would freeze the
// posture the container happened to be created with.
func TestOpenAttachToRunning_ReloadsAttendedFlagBetweenLaunches(t *testing.T) {
	for _, firstAttended := range []bool{true, false} {
		t.Run(fmt.Sprintf("%t to %t", firstAttended, !firstAttended), func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, socketDir := openTestEnv(t, fakeDir, assets)
			const container = "claude-cenci-default"
			env = append(env,
				"FAKE_PS="+container+"\n",
				"FAKE_INSPECT_LABEL=detached",
				"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
			)
			launchDir := t.TempDir()

			for launch, attended := range []bool{firstAttended, !firstAttended} {
				writeFleetConfigFixture(t, home, fmt.Sprintf(`{"planning": {"attended": %t}}`, attended))
				before := len(callLogLines(t, callLog))
				cmd := exec.Command(binaryPath, "open", "ch")
				cmd.Env = env
				cmd.Dir = launchDir
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("launch %d into running container: %v\n%s", launch+1, err, output)
				}

				// Read only this launch's calls so the first exec cannot satisfy
				// assertions about the second launch's changed configuration.
				lines := callLogLines(t, callLog)[before:]
				var attaches []string
				for _, line := range lines {
					for _, prefix := range []string{"run --name ", "create ", "rm ", "restart ", "stop ", "kill "} {
						if strings.HasPrefix(line, prefix) {
							t.Fatalf("launch %d must reuse the running container, got %q", launch+1, line)
						}
					}
					if strings.HasPrefix(line, "exec -it ") {
						attaches = append(attaches, line)
					}
				}
				if len(attaches) != 1 {
					t.Fatalf("launch %d: got %d interactive execs, want one; calls: %v", launch+1, len(attaches), lines)
				}
				args := strings.Fields(attaches[0])
				var targets, attendedValues []string
				for i, arg := range args {
					if arg == container {
						targets = append(targets, arg)
					}
					if i > 0 && args[i-1] == "-e" && strings.HasPrefix(arg, "CENCI_ATTENDED=") {
						attendedValues = append(attendedValues, arg)
					}
				}
				if len(targets) != 1 {
					t.Errorf("launch %d must exec into %s exactly once; got %q", launch+1, container, attaches[0])
				}
				want := "CENCI_ATTENDED=0"
				if attended {
					want = "CENCI_ATTENDED=1"
				}
				if len(attendedValues) != 1 || attendedValues[0] != want {
					t.Errorf("launch %d: attended env = %v, want exactly [%s]; exec: %s", launch+1, attendedValues, want, attaches[0])
				}
			}
		})
	}
}

// TestOpenClaude_AttendedFlagAddsNoFleetConfigMount covers the AC that the
// forward introduces no new bind mount of ~/.config/cenci or any file under
// it. The fleet config is read on the host and forwarded as a value precisely
// so the container never sees the file: a file-level bind would pin the
// pre-toggle inode (the config is written temp-file-then-rename), and a
// directory bind would expose the whole fleet config — repos list included —
// to every sandbox.
func TestOpenClaude_AttendedFlagAddsNoFleetConfigMount(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home, _ := openTestEnv(t, fakeDir, assets)
	writeFleetConfigFixture(t, home, `{"planning": {"attended": true}}`)

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
	for _, forbidden := range []string{
		filepath.Join(home, ".config", "cenci"),
		".config/cenci",
	} {
		if strings.Contains(runLine, forbidden) {
			t.Errorf("create-time run args must never mount the host fleet config (%q); got:\n%s", forbidden, runLine)
		}
	}
}

// -- ticket #1016: sandboxEnv.forward in the user fleet config ----------------
//
// The fleet config is read from $XDG_CONFIG_HOME/cenci/config.json, which
// openTestEnv pins under the fixture home (writeFleetConfigFixture). These
// black-box runs pin what the launcher does with the allowlist end to end:
// bare per-exec tokens for set names, one stderr warning per unset name, and a
// fail-closed exit 1 with no runtime call for a malformed entry.

// TestOpen_ConfiguredForward_AttachCarriesBareTokensForSetNamesOnly covers the
// headline AC in repo scope and in legacy (non-repo) workspace scope: a set
// configured name is a bare `-e NAME` pair on the attach argv, never on the
// create argv, never with a value; an unset or empty name gets no token and one
// `Warning:` line on stderr, and the launch still proceeds.
func TestOpen_ConfiguredForward_AttachCarriesBareTokensForSetNamesOnly(t *testing.T) {
	scopes := []struct {
		name string
		repo bool
	}{
		{"legacy non-repo scope", false},
		{"repo scope", true},
	}
	for _, sc := range scopes {
		t.Run(sc.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, _ := openTestEnv(t, fakeDir, assets)
			writeFleetConfigFixture(t, home, `{"sandboxEnv":{"forward":["MY_THING","TAVILY_API_KEY","UNSET_ONE","EMPTY_TWO"]}}`)
			const thingValue, tavilyValue = "sk-cenci-sentinel-thing", "sk-cenci-sentinel-tavily"
			env = append(env, "MY_THING="+thingValue, "TAVILY_API_KEY="+tavilyValue, "UNSET_ONE=", "EMPTY_TWO=")
			dir := t.TempDir()
			if sc.repo {
				dir = configuredRepoEnv(t, `{}`)
			}

			run := runCenci(t, env, dir, "open", "ch")
			if run.exitCode != 0 {
				t.Fatalf("open ch: exit %d (unset configured names must never fail the launch)\n%s", run.exitCode, run.stderr)
			}

			lines := callLogLines(t, callLog)
			runLine, ok := findLineWithPrefix(lines, "run --name ")
			if !ok {
				t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
			}
			attach := attachLine(t, lines)
			for _, name := range []string{"MY_THING", "TAVILY_API_KEY"} {
				if !hasBareEnvToken(attach, name) {
					t.Errorf("attach argv lacks the bare -e %s pair:\n%s", name, attach)
				}
			}
			for _, name := range []string{"UNSET_ONE", "EMPTY_TWO"} {
				if strings.Contains(attach, name) {
					t.Errorf("attach argv carries %s although it is unset or empty on the host:\n%s", name, attach)
				}
			}
			for label, line := range map[string]string{"create": runLine, "attach": attach} {
				for _, leaked := range []string{thingValue, tavilyValue, "MY_THING=", "TAVILY_API_KEY="} {
					if strings.Contains(line, leaked) {
						t.Errorf("%s argv carries %q; values and NAME= forms must never reach argv:\n%s", label, leaked, line)
					}
				}
			}
			for _, name := range []string{"MY_THING", "TAVILY_API_KEY", "UNSET_ONE", "EMPTY_TWO"} {
				if strings.Contains(runLine, name) {
					t.Errorf("create argv mentions the configured name %s; forwarding is per exec only:\n%s", name, runLine)
				}
			}

			var warnings []string
			for _, line := range strings.Split(run.stderr, "\n") {
				if strings.HasPrefix(line, "Warning:") && strings.Contains(line, "sandboxEnv.forward") {
					warnings = append(warnings, line)
				}
			}
			want := []string{
				"Warning: UNSET_ONE is listed in sandboxEnv.forward but is unset or empty in this environment; not forwarding it",
				"Warning: EMPTY_TWO is listed in sandboxEnv.forward but is unset or empty in this environment; not forwarding it",
			}
			if !slices.Equal(warnings, want) {
				t.Errorf("sandboxEnv.forward warnings on stderr = %q, want exactly %q (one per unset name)", warnings, want)
			}
		})
	}
}

// TestOpen_ConfiguredForward_EntryWithEquals_FailsClosedBeforeAnyRuntimeCall is
// the open and open --dry-run half of the "=" AC: exit 1 (a plain config error,
// not the usage exit 2) naming the config path, the key and FOO=<redacted>,
// never the value, with an empty runtime call log whether the container would be
// created or attached.
func TestOpen_ConfiguredForward_EntryWithEquals_FailsClosedBeforeAnyRuntimeCall(t *testing.T) {
	modes := []struct {
		name      string
		args      []string
		attaching bool
	}{
		{"create", []string{"open", "ch"}, false},
		{"attach", []string{"open", "ch"}, true},
		{"dry-run", []string{"open", "ch", "--dry-run"}, false},
		{"dry-run against a running container", []string{"open", "ch", "--dry-run"}, true},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			var env []string
			if m.attaching {
				env = openAttachEnvFor(t, fakeDir, assets, "claude-cenci-default", defaultPluginsContainerEnv)
			} else {
				env, _, _ = openTestEnv(t, fakeDir, assets)
			}
			xdg := t.TempDir()
			env = append(env, "XDG_CONFIG_HOME="+xdg)
			configPath := filepath.Join(xdg, "cenci", "config.json")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(configPath, []byte(`{"sandboxEnv":{"forward":["FOO=bar"]}}`), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			run := runCenci(t, env, t.TempDir(), m.args...)

			if run.exitCode != 1 {
				t.Fatalf("expected exit 1 (plain config error, not usage exit 2), got %d\nstdout:\n%s\nstderr:\n%s", run.exitCode, run.stdout, run.stderr)
			}
			if !strings.Contains(run.stderr, configPath) {
				t.Errorf("error does not name the config file %s:\n%s", configPath, run.stderr)
			}
			// The path is host-derived (a temp dir named after this test); strip
			// it before looking for a leaked value.
			for label, stream := range map[string]string{"stderr": run.stderr, "stdout": run.stdout} {
				msg := strings.ReplaceAll(stream, configPath, "<path>")
				if strings.Contains(msg, "bar") {
					t.Errorf("%s leaks the value after the first '=':\n%s", label, msg)
				}
			}
			for _, want := range []string{"sandboxEnv.forward", "FOO=<redacted>"} {
				if !strings.Contains(run.stderr, want) {
					t.Errorf("error does not contain %q:\n%s", want, run.stderr)
				}
			}
			if lines := callLogLines(t, callLog); len(lines) != 0 {
				t.Errorf("a malformed sandboxEnv.forward must make no runtime call, got:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// TestOpen_UnparsableFleetConfig_FailsClosedNamingThePath: sandboxEnv.forward is
// read from the user fleet config, so a file that is not valid JSON fails
// `cenci open` with exit 1 and the path, before any runtime call. This replaces
// the old fold-to-off behavior TestOpenClaude_ForwardsResolvedAttendedFlag used
// to pin for an unparsable file: silently dropping the allowlist would hide a
// config error behind a confusing in-container failure.
func TestOpen_UnparsableFleetConfig_FailsClosedNamingThePath(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home, _ := openTestEnv(t, fakeDir, assets)
	writeFleetConfigFixture(t, home, `{"planning": `)
	configPath := filepath.Join(home, ".config", "cenci", "config.json")

	run := runCenci(t, env, t.TempDir(), "open", "ch")

	if run.exitCode != 1 {
		t.Fatalf("expected exit 1, got %d\nstdout:\n%s\nstderr:\n%s", run.exitCode, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, configPath) {
		t.Errorf("error does not name the config file %s:\n%s", configPath, run.stderr)
	}
	if lines := callLogLines(t, callLog); len(lines) != 0 {
		t.Errorf("an unparsable fleet config must make no runtime call, got:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpen_FleetConfigWithOtherBlocksAndNoSandboxEnv_LaunchesWithBuiltinSet: a
// fleet config that is valid JSON with other blocks (including "sandbox": false,
// which run.Load reads as cenci run's own boolean) but no sandboxEnv resolves to
// the built-in set with no error and no warning.
func TestOpen_FleetConfigWithOtherBlocksAndNoSandboxEnv_LaunchesWithBuiltinSet(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home, _ := openTestEnv(t, fakeDir, assets)
	writeFleetConfigFixture(t, home, `{"dispatch":{},"automerge":{},"planning":{},"sandbox":false}`)
	env = append(env, "CONTEXT7_API_KEY=sk-cenci-sentinel-context7")

	run := runCenci(t, env, t.TempDir(), "open", "ch")
	if run.exitCode != 0 {
		t.Fatalf("open ch: exit %d\n%s", run.exitCode, run.stderr)
	}
	if strings.Contains(run.stderr, "sandboxEnv") || strings.Contains(run.stderr, "Warning:") {
		t.Errorf("a config without sandboxEnv must produce no sandboxEnv error or warning, got stderr:\n%s", run.stderr)
	}
	attach := attachLine(t, callLogLines(t, callLog))
	if !hasBareEnvToken(attach, "CONTEXT7_API_KEY") {
		t.Errorf("built-in CONTEXT7_API_KEY no longer forwarded with an unrelated fleet config present:\n%s", attach)
	}
}

// TestOpenAttachToRunning_ReloadsForwardConfigBetweenLaunches: editing
// sandboxEnv.forward while a container runs takes effect on the next attach to
// that same container, with no recreate; the second attach reflects only the
// second config.
func TestOpenAttachToRunning_ReloadsForwardConfigBetweenLaunches(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home, socketDir := openTestEnv(t, fakeDir, assets)
	const container = "claude-cenci-default"
	env = append(env,
		"FAKE_PS="+container+"\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
		"FIRST_ONE=sentinel-first",
		"SECOND_ONE=sentinel-second",
	)
	launchDir := t.TempDir()

	forwards := []struct{ listed, notListed string }{
		{"FIRST_ONE", "SECOND_ONE"},
		{"SECOND_ONE", "FIRST_ONE"},
	}
	for launch, f := range forwards {
		writeFleetConfigFixture(t, home, fmt.Sprintf(`{"sandboxEnv":{"forward":[%q]}}`, f.listed))
		before := len(callLogLines(t, callLog))

		run := runCenci(t, env, launchDir, "open", "ch")
		if run.exitCode != 0 {
			t.Fatalf("launch %d into running container: exit %d\n%s", launch+1, run.exitCode, run.stderr)
		}

		lines := callLogLines(t, callLog)[before:]
		if _, ok := findLineWithPrefix(lines, "run --name "); ok {
			t.Fatalf("launch %d must reuse the running container, got calls:\n%s", launch+1, strings.Join(lines, "\n"))
		}
		attach := attachLine(t, lines)
		if !hasBareEnvToken(attach, f.listed) {
			t.Errorf("launch %d attach lacks the bare -e %s pair of its own config:\n%s", launch+1, f.listed, attach)
		}
		if strings.Contains(attach, f.notListed) {
			t.Errorf("launch %d attach still carries %s from the other config:\n%s", launch+1, f.notListed, attach)
		}
	}
}

// TestOpenAttachToRunning_UnsetConfiguredNamesWarnOncePerNameAndProceed: the
// attach path (an already-running container, so no create) prints exactly one
// `Warning:` line on stderr per configured name that is unset or empty (one
// never exported, one exported empty), emits no `-e <NAME>` token for either,
// still forwards the set name, proceeds with exit 0 and makes no create call.
func TestOpenAttachToRunning_UnsetConfiguredNamesWarnOncePerNameAndProceed(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home, socketDir := openTestEnv(t, fakeDir, assets)
	const container = "claude-cenci-default"
	env = append(env,
		"FAKE_PS="+container+"\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
		"SET_ONE=sk-cenci-sentinel-set",
		"EMPTY_TWO=",
	)
	writeFleetConfigFixture(t, home, `{"sandboxEnv":{"forward":["SET_ONE","NEVER_EXPORTED","EMPTY_TWO"]}}`)

	run := runCenci(t, env, t.TempDir(), "open", "ch")
	if run.exitCode != 0 {
		t.Fatalf("attach into running container: exit %d (unset configured names must never fail the launch)\n%s", run.exitCode, run.stderr)
	}

	lines := callLogLines(t, callLog)
	if runLine, ok := findLineWithPrefix(lines, "run --name "); ok {
		t.Fatalf("attach must reuse the running container, got a create call:\n%s", runLine)
	}
	attach := attachLine(t, lines)
	if !hasBareEnvToken(attach, "SET_ONE") {
		t.Errorf("attach argv lacks the bare -e SET_ONE pair:\n%s", attach)
	}
	for _, name := range []string{"NEVER_EXPORTED", "EMPTY_TWO"} {
		if strings.Contains(attach, name) {
			t.Errorf("attach argv carries %s although it is unset or empty on the host:\n%s", name, attach)
		}
	}

	var warnings []string
	for _, line := range strings.Split(run.stderr, "\n") {
		if strings.HasPrefix(line, "Warning:") && strings.Contains(line, "sandboxEnv.forward") {
			warnings = append(warnings, line)
		}
	}
	want := []string{
		"Warning: NEVER_EXPORTED is listed in sandboxEnv.forward but is unset or empty in this environment; not forwarding it",
		"Warning: EMPTY_TWO is listed in sandboxEnv.forward but is unset or empty in this environment; not forwarding it",
	}
	if !slices.Equal(warnings, want) {
		t.Errorf("sandboxEnv.forward warnings on stderr = %q, want exactly %q (one per unset name)", warnings, want)
	}
	if strings.Contains(run.stderr, "SET_ONE") {
		t.Errorf("a set name must produce no warning, got stderr:\n%s", run.stderr)
	}
}

// TestOpen_RepoConfigSandboxEnvForward_IsIgnored pins the user-scope-only
// boundary: a repo-committed .cenci/config.json listing sandboxEnv.forward is
// never a forwarding source. Even with the host value set, the attach argv
// carries no `-e REPO_ONLY_SECRET` (the create argv neither), there is no
// warning or error for it, and the launch proceeds. The user fleet config
// carries a different name so the user-scope path stays exercised.
func TestOpen_RepoConfigSandboxEnvForward_IsIgnored(t *testing.T) {
	fleets := []struct {
		name  string
		fleet string
	}{
		{"no fleet config", ""},
		{"fleet config naming a different variable", `{"sandboxEnv":{"forward":["USER_ONLY"]}}`},
	}
	for _, f := range fleets {
		t.Run(f.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, home, _ := openTestEnv(t, fakeDir, assets)
			if f.fleet != "" {
				writeFleetConfigFixture(t, home, f.fleet)
			}
			const repoSecret = "sk-cenci-sentinel-repo-only"
			env = append(env, "REPO_ONLY_SECRET="+repoSecret, "USER_ONLY=sk-cenci-sentinel-user")
			repo := configuredRepoEnv(t, `{"sandboxEnv":{"forward":["REPO_ONLY_SECRET"]}}`)

			run := runCenci(t, env, repo, "open", "ch")
			if run.exitCode != 0 {
				t.Fatalf("open ch: exit %d (a repo sandboxEnv block must be ignored, not an error)\n%s", run.exitCode, run.stderr)
			}
			if strings.Contains(run.stderr, "REPO_ONLY_SECRET") || strings.Contains(run.stderr, "sandboxEnv") {
				t.Errorf("a repo sandboxEnv.forward must produce no warning or error, got stderr:\n%s", run.stderr)
			}

			lines := callLogLines(t, callLog)
			runLine, ok := findLineWithPrefix(lines, "run --name ")
			if !ok {
				t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
			}
			attach := attachLine(t, lines)
			for label, line := range map[string]string{"create": runLine, "attach": attach} {
				if strings.Contains(line, "REPO_ONLY_SECRET") || strings.Contains(line, repoSecret) {
					t.Errorf("%s argv forwards the repo-listed REPO_ONLY_SECRET; the repo must not widen the boundary:\n%s", label, line)
				}
			}
			if f.fleet != "" && !hasBareEnvToken(attach, "USER_ONLY") {
				t.Errorf("the user fleet config's USER_ONLY must still be forwarded:\n%s", attach)
			}
		})
	}
}

// -- ticket #1216: memory and pids limits on the workload container ----------
//
// `cenci open` applies --memory/--memory-swap/--pids-limit to the long-lived
// workload container (sandbox.memoryLimit / sandbox.pidsLimit in the repo's
// .cenci/config.json; default 50% of the runtime's reported RAM with no swap,
// and 4096 pids). These black-box tests drive the real binary against the
// scripted runtime (FAKE_INFO_MEMTOTAL, FAKE_CONTAINER_LIMITS,
// FAKE_CREATE_STDERR/FAKE_CREATE_EXIT; see writeScriptedRuntime).

// openRun is one finished `cenci` invocation with its streams kept apart:
// the limit tests assert on stderr (errors, warnings) separately from stdout
// (the dry-run preview).
type openRun struct {
	stdout   string
	stderr   string
	exitCode int
}

// runCenci runs the built binary with env in dir and captures both streams.
func runCenci(t *testing.T, env []string, dir string, args ...string) openRun {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = env
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	run := openRun{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run cenci %v: %v", args, err)
		}
		run.exitCode = exitErr.ExitCode()
	}
	return run
}

// limitFlagTokens extracts, in order of appearance, every limit flag and its
// value from a space-joined argv line: ["--memory","8192m","--memory-swap",
// "8192m","--pids-limit","4096"] for a default launch.
func limitFlagTokens(line string) []string {
	var out []string
	fields := strings.Fields(line)
	for i, f := range fields {
		if (f == "--memory" || f == "--memory-swap" || f == "--pids-limit") && i+1 < len(fields) {
			out = append(out, f, fields[i+1])
		}
	}
	return out
}

// limitFlagValue returns the value following flag in a space-joined argv line.
func limitFlagValue(line, flag string) (string, bool) {
	fields := strings.Fields(line)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1], true
		}
	}
	return "", false
}

// containerNameFor is the scoped container name `cenci open` derives for a
// claude launch in repoRoot.
func containerNameFor(repoRoot string) string {
	return "claude-cenci-" + launcher.Slugify(filepath.Base(repoRoot))
}

// defaultPluginsContainerEnv is the FAKE_CONTAINER_ENV answer that keeps the
// (separate, #1002) plugins drift check quiet in the limits attach tests, so
// the limit-drift line is the only drift line that can appear.
const defaultPluginsContainerEnv = "CENCI_SANDBOX_PLUGINS=cenci cenci-watch\n"

// stopRemedyLines returns the output lines carrying the stop-and-relaunch
// remedy ("cenci sandbox stop <name>") the drift warnings end with.
func stopRemedyLines(output, containerName string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "cenci sandbox stop "+containerName) {
			out = append(out, line)
		}
	}
	return out
}

// -- AC1: a fresh create carries the default limits --------------------------

func TestOpen_FreshCreate_AppliesDefaultMemorySwapAndPidsLimits(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)

	run := runCenci(t, env, t.TempDir(), "open", "ch")
	if run.exitCode != 0 {
		t.Fatalf("open ch: exit %d\n%s", run.exitCode, run.stderr)
	}

	lines := callLogLines(t, callLog)
	runLine, ok := findLineWithPrefix(lines, "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(lines, "\n"))
	}
	// FAKE_INFO_MEMTOTAL defaults to 16 GiB: 50% is 8192 MiB, no swap.
	for flag, want := range map[string]string{"--memory": "8192m", "--memory-swap": "8192m", "--pids-limit": "4096"} {
		if got, _ := limitFlagValue(runLine, flag); got != want {
			t.Errorf("%s = %q, want %q; run argv:\n%s", flag, got, want, runLine)
		}
	}
}

// -- AC4: malformed limit config fails closed on every path -------------------

// TestOpen_MalformedLimitsConfig_FailsClosedOnCreateAttachAndDryRun is AC4:
// a wrong-typed, mis-formatted or out-of-range sandbox.memoryLimit /
// sandbox.pidsLimit makes `cenci open` (create AND attach paths) and `cenci
// open --dry-run` fail with a plain error (exit 1, never the usage exit 2)
// naming the key -- before any runtime call, and never by falling back to a
// default.
func TestOpen_MalformedLimitsConfig_FailsClosedOnCreateAttachAndDryRun(t *testing.T) {
	configs := []struct {
		name   string
		config string
		key    string
	}{
		{"memory bad unit case", `{"sandbox":{"memoryLimit":"16G"}}`, "memoryLimit"},
		{"memory wrong JSON type", `{"sandbox":{"memoryLimit":16}}`, "memoryLimit"},
		{"memory null", `{"sandbox":{"memoryLimit":null}}`, "memoryLimit"},
		{"pids negative", `{"sandbox":{"pidsLimit":-1}}`, "pidsLimit"},
		{"pids non-integer", `{"sandbox":{"pidsLimit":1.5}}`, "pidsLimit"},
		{"pids string", `{"sandbox":{"pidsLimit":"4096"}}`, "pidsLimit"},
	}
	modes := []struct {
		name      string
		args      []string
		attaching bool
	}{
		{"create", []string{"open"}, false},
		{"attach", []string{"open"}, true},
		{"dry-run", []string{"open", "--dry-run"}, false},
		{"dry-run against a running container", []string{"open", "--dry-run"}, true},
	}
	for _, c := range configs {
		for _, m := range modes {
			t.Run(c.name+"/"+m.name, func(t *testing.T) {
				repoRoot := configuredRepoEnv(t, c.config)
				fakeDir := t.TempDir()
				callLog := writeScriptedRuntimes(t, fakeDir)
				assets := writeAssetFixture(t)
				var env []string
				if m.attaching {
					env = openAttachEnvFor(t, fakeDir, assets, containerNameFor(repoRoot), defaultPluginsContainerEnv)
				} else {
					env, _, _ = openTestEnv(t, fakeDir, assets)
				}

				run := runCenci(t, env, repoRoot, m.args...)

				if run.exitCode != 1 {
					t.Fatalf("expected a malformed %s to exit 1 (plain error, not a usage error), got %d\nstdout:\n%s\nstderr:\n%s", c.key, run.exitCode, run.stdout, run.stderr)
				}
				if !strings.Contains(run.stderr, c.key) {
					t.Errorf("expected the error to name %q, got stderr:\n%s", c.key, run.stderr)
				}
				if !strings.Contains(run.stderr, "config.json") {
					t.Errorf("expected a path-bearing error naming config.json, got stderr:\n%s", run.stderr)
				}
				if lines := callLogLines(t, callLog); len(lines) != 0 {
					t.Errorf("expected no runtime calls once the limits config hard-fails (no silent default), got:\n%s", strings.Join(lines, "\n"))
				}
			})
		}
	}
}

// -- AC5: unreadable host memory fails the launch before any create -----------

func TestOpen_DefaultMemoryUnresolvable_FailsBeforeCreateNamingTheOptOut(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, "FAKE_INFO_MEMTOTAL_EXIT=1")

	run := runCenci(t, env, t.TempDir(), "open", "ch")

	if run.exitCode != 1 {
		t.Fatalf("expected an unreadable host memory total to exit 1, got %d\nstderr:\n%s", run.exitCode, run.stderr)
	}
	for _, want := range []string{"CENCI-SANDBOX-LIMITS-001", `"memoryLimit": "none"`} {
		if !strings.Contains(run.stderr, want) {
			t.Errorf("expected the error to contain %q, got stderr:\n%s", want, run.stderr)
		}
	}
	lines := callLogLines(t, callLog)
	if _, created := findLineWithPrefix(lines, "run --name "); created {
		t.Errorf("no container may be created when the memory limit cannot be resolved, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if _, removed := findLineWithPrefix(lines, "rm "); removed {
		t.Errorf("no container may be removed when the launch fails before create, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if _, attached := findLineWithPrefix(lines, "exec -it "); attached {
		t.Errorf("the launch must not attach after failing, got calls:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOpen_MemoryNoneOptOut_LaunchesDespiteUnreadableHostMemory pins that the
// error's own advice works: with "memoryLimit": "none" the same unreadable
// total no longer matters, the memory flags are omitted and the pids limit
// stays.
func TestOpen_MemoryNoneOptOut_LaunchesDespiteUnreadableHostMemory(t *testing.T) {
	repoRoot := configuredRepoEnv(t, `{"sandbox":{"memoryLimit":"none"}}`)
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, "FAKE_INFO_MEMTOTAL_EXIT=1")

	run := runCenci(t, env, repoRoot, "open")
	if run.exitCode != 0 {
		t.Fatalf("open with the memory opt-out: exit %d\n%s", run.exitCode, run.stderr)
	}

	runLine, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name ")
	if !ok {
		t.Fatalf("expected a container run, got calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
	}
	if flags := limitFlagTokens(runLine); strings.Join(flags, " ") != "--pids-limit 4096" {
		t.Errorf("limit flags = %v, want only --pids-limit 4096 (memory opted out); run argv:\n%s", flags, runLine)
	}
}

// -- AC6: a create rejected because of a limit --------------------------------

// runcMemoryMaxCreateStderr is the stderr Docker streams when the engine's
// cgroup has no delegated memory controller: the runc failure arrives
// wrapped in the daemon's "OCI runtime create failed" text.
const runcMemoryMaxCreateStderr = `docker: Error response from daemon: failed to create task for container: ` +
	`failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: ` +
	`error during container init: error setting cgroup config for procHooks process: ` +
	`openat2 /sys/fs/cgroup/docker/0d3c1f/memory.max: no such file or directory: unknown`

func TestOpen_CreateRejectedForLimit_StreamsStderrAndNamesCodeAndBothOptOuts(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, "FAKE_CREATE_STDERR="+runcMemoryMaxCreateStderr, "FAKE_CREATE_EXIT=125")

	run := runCenci(t, env, t.TempDir(), "open", "ch")

	if run.exitCode != 1 {
		t.Fatalf("expected a limit-rejected create to exit 1, got %d\nstderr:\n%s", run.exitCode, run.stderr)
	}
	// The runtime's own diagnostics keep streaming through verbatim.
	if !strings.Contains(run.stderr, runcMemoryMaxCreateStderr) {
		t.Errorf("the runtime's stderr must still be streamed to the user; got stderr:\n%s", run.stderr)
	}
	for _, want := range []string{"CENCI-SANDBOX-LIMITS-002", `"memoryLimit": "none"`, `"pidsLimit": 0`} {
		if !strings.Contains(run.stderr, want) {
			t.Errorf("expected the failure to contain %q, got stderr:\n%s", want, run.stderr)
		}
	}
	if _, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name "); !ok {
		t.Errorf("expected the create to have been attempted, got calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
	}
}

// TestOpen_CreateFailureUnrelatedToLimits_KeepsPlainErrorPath is AC6's
// regression half: a create failure that is not about a limit keeps the
// existing "<runtime> run: <err>" path, with no LIMITS code.
func TestOpen_CreateFailureUnrelatedToLimits_KeepsPlainErrorPath(t *testing.T) {
	const conflictStderr = `docker: Error response from daemon: Conflict. The container name "/claude-cenci-default" is already in use by container "0d3c1f".`
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, "FAKE_CREATE_STDERR="+conflictStderr, "FAKE_CREATE_EXIT=125")

	run := runCenci(t, env, t.TempDir(), "open", "ch")

	if run.exitCode != 1 {
		t.Fatalf("expected a failed create to exit 1, got %d\nstderr:\n%s", run.exitCode, run.stderr)
	}
	if !strings.Contains(run.stderr, conflictStderr) {
		t.Errorf("the runtime's stderr must still be streamed; got stderr:\n%s", run.stderr)
	}
	if !strings.Contains(run.stderr, "run: exit status 125") {
		t.Errorf("an unrelated create failure must keep the plain \"<runtime> run: exit status 125\" wording; got stderr:\n%s", run.stderr)
	}
	if strings.Contains(run.stderr, "CENCI-SANDBOX-LIMITS") {
		t.Errorf("an unrelated create failure must not carry a LIMITS code; got stderr:\n%s", run.stderr)
	}
}

// TestOpen_LimitLikeStderrWithBothOptOuts_KeepsPlainErrorPath pins that the
// classification follows what was actually sent: with both opt-outs no limit
// flag is on the argv, so even a memory.max-shaped rejection cannot be the
// limits' doing.
func TestOpen_LimitLikeStderrWithBothOptOuts_KeepsPlainErrorPath(t *testing.T) {
	repoRoot := configuredRepoEnv(t, `{"sandbox":{"memoryLimit":"none","pidsLimit":0}}`)
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	env = append(env, "FAKE_CREATE_STDERR="+runcMemoryMaxCreateStderr, "FAKE_CREATE_EXIT=125")

	run := runCenci(t, env, repoRoot, "open")

	if run.exitCode != 1 {
		t.Fatalf("expected a failed create to exit 1, got %d\nstderr:\n%s", run.exitCode, run.stderr)
	}
	if strings.Contains(run.stderr, "CENCI-SANDBOX-LIMITS") {
		t.Errorf("no limit flag was sent, so the failure must not be classified as a limit rejection; got stderr:\n%s", run.stderr)
	}
	runLine, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name ")
	if !ok {
		t.Fatalf("expected the create to have been attempted, got calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
	}
	if flags := limitFlagTokens(runLine); len(flags) != 0 {
		t.Errorf("both opt-outs must leave every limit flag off the argv, got %v in:\n%s", flags, runLine)
	}
}

// -- AC7: --dry-run previews exactly what a real create applies ---------------

// TestOpenDryRun_LimitFlagsMatchTheRealCreate runs `open --dry-run` and then
// the real `open` in the same repo and asserts the two carry the identical
// limit flags -- for the default, an override, and the opt-outs.
func TestOpenDryRun_LimitFlagsMatchTheRealCreate(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   string
	}{
		{"default", `{}`, "--memory 8192m --memory-swap 8192m --pids-limit 4096"},
		{"override", `{"sandbox":{"memoryLimit":"2g","pidsLimit":512}}`, "--memory 2g --memory-swap 2g --pids-limit 512"},
		{"memory opt-out", `{"sandbox":{"memoryLimit":"none"}}`, "--pids-limit 4096"},
		{"pids opt-out", `{"sandbox":{"pidsLimit":0}}`, "--memory 8192m --memory-swap 8192m"},
		{"both opt-outs", `{"sandbox":{"memoryLimit":"none","pidsLimit":0}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := configuredRepoEnv(t, tc.config)
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, _, _ := openTestEnv(t, fakeDir, assets)

			dry := runCenci(t, env, repoRoot, "open", "--dry-run")
			if dry.exitCode != 0 {
				t.Fatalf("open --dry-run: exit %d\n%s", dry.exitCode, dry.stderr)
			}
			var previewLine string
			for _, line := range strings.Split(dry.stdout, "\n") {
				if strings.Contains(line, " run --name ") {
					previewLine = line
				}
			}
			if previewLine == "" {
				t.Fatalf("expected the create argv in the dry-run preview, got:\n%s", dry.stdout)
			}
			if got := strings.Join(limitFlagTokens(previewLine), " "); got != tc.want {
				t.Errorf("dry-run limit flags = %q, want %q; preview:\n%s", got, tc.want, previewLine)
			}

			real := runCenci(t, env, repoRoot, "open")
			if real.exitCode != 0 {
				t.Fatalf("open: exit %d\n%s", real.exitCode, real.stderr)
			}
			runLine, ok := findLineWithPrefix(callLogLines(t, callLog), "run --name ")
			if !ok {
				t.Fatalf("expected a container run, got calls:\n%s", strings.Join(callLogLines(t, callLog), "\n"))
			}
			if got, preview := strings.Join(limitFlagTokens(runLine), " "), strings.Join(limitFlagTokens(previewLine), " "); got != preview {
				t.Errorf("the real create's limit flags %q differ from the dry-run preview's %q", got, preview)
			}
		})
	}
}

// -- AC9: attach-path drift warning -------------------------------------------

// TestOpenAttach_LimitsDrift_WarnsExactlyOnceAndAttachContinues covers the
// drift matrix: a differing limit (including an unlimited container created
// before limits shipped) prints exactly one warning line with the
// stop-and-relaunch remedy and the attach goes on; matching limits, the
// pidsLimit-0 opt-out, and a failed probe print nothing. An attach never
// creates, never queries host memory, and never fails because of limit
// resolution.
func TestOpenAttach_LimitsDrift_WarnsExactlyOnceAndAttachContinues(t *testing.T) {
	const (
		gib8      = "8589934592"
		matching8 = gib8 + "|" + gib8 + "|4096"
	)
	cases := []struct {
		name       string
		config     string
		limits     string // FAKE_CONTAINER_LIMITS
		extraEnv   []string
		warnings   int
		mentions   []string // case-insensitive substrings the single warning line must contain
		notMention []string
	}{
		{name: "unlimited legacy container, default config", config: `{}`, limits: "0|0|<nil>", warnings: 1, mentions: []string{"memory limit differs", "pids limit differs"}},
		{name: "unlimited podman container (-1), default config", config: `{}`, limits: "-1|-1|-1", warnings: 1, mentions: []string{"memory limit differs", "pids limit differs"}},
		{name: "memory drift against an explicit size", config: `{"sandbox":{"memoryLimit":"16g"}}`, limits: matching8, warnings: 1, mentions: []string{"memory limit differs", "container 8192 mib", "configured 16g"}, notMention: []string{"pids"}},
		{name: "pids drift", config: `{"sandbox":{"pidsLimit":2048}}`, limits: matching8, warnings: 1, mentions: []string{"pids limit differs", "container 4096", "configured 2048"}, notMention: []string{"memory"}},
		{name: "memory opt-out but container is limited", config: `{"sandbox":{"memoryLimit":"none"}}`, limits: matching8, warnings: 1, mentions: []string{"memory limit differs", "configured none"}, notMention: []string{"pids"}},

		{name: "default config, default-limited container", config: `{}`, limits: matching8, warnings: 0},
		{name: "default config, container at another size is not drift", config: `{}`, limits: "4294967296|4294967296|4096", warnings: 0},
		{name: "explicit size matches exactly", config: `{"sandbox":{"memoryLimit":"8g","pidsLimit":4096}}`, limits: matching8, warnings: 0},
		{name: "memory opt-out, unlimited container", config: `{"sandbox":{"memoryLimit":"none"}}`, limits: "0|0|4096", warnings: 0},
		{name: "pidsLimit 0 never differs, container pids nil", config: `{"sandbox":{"pidsLimit":0}}`, limits: gib8 + "|" + gib8 + "|<nil>", warnings: 0},
		{name: "pidsLimit 0 never differs, container pids unlimited", config: `{"sandbox":{"pidsLimit":0}}`, limits: gib8 + "|" + gib8 + "|-1", warnings: 0},
		{name: "pidsLimit 0 never differs, container pids 4096", config: `{"sandbox":{"pidsLimit":0}}`, limits: matching8, warnings: 0},
		{name: "pidsLimit 0 does not hide a memory drift", config: `{"sandbox":{"pidsLimit":0}}`, limits: "0|0|<nil>", warnings: 1, mentions: []string{"memory limit differs"}, notMention: []string{"pids"}},

		{name: "probe failure prints nothing", config: `{}`, limits: "0|0|<nil>", extraEnv: []string{"FAKE_CONTAINER_LIMITS_EXIT=1"}, warnings: 0},
		{name: "unreadable host memory never fails an attach", config: `{}`, limits: matching8, extraEnv: []string{"FAKE_INFO_MEMTOTAL_EXIT=1"}, warnings: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := configuredRepoEnv(t, tc.config)
			containerName := containerNameFor(repoRoot)
			fakeDir := t.TempDir()
			callLog := writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env := openAttachEnvFor(t, fakeDir, assets, containerName, defaultPluginsContainerEnv)
			env = append(env, "FAKE_CONTAINER_LIMITS="+tc.limits)
			env = append(env, tc.extraEnv...)

			run := runCenci(t, env, repoRoot, "open")

			if run.exitCode != 0 {
				t.Fatalf("open (attach): exit %d, want the attach to continue\nstderr:\n%s", run.exitCode, run.stderr)
			}
			lines := callLogLines(t, callLog)
			attachLine(t, lines)
			if _, created := findLineWithPrefix(lines, "run --name "); created {
				t.Errorf("a drifted container must be warned about, never recreated; got calls:\n%s", strings.Join(lines, "\n"))
			}
			if anyLineContains(lines, "MemTotal") {
				t.Errorf("an attach must not query the runtime for total memory; got calls:\n%s", strings.Join(lines, "\n"))
			}

			remedy := stopRemedyLines(run.stderr+run.stdout, containerName)
			if len(remedy) != tc.warnings {
				t.Fatalf("got %d drift warning line(s) with the stop remedy, want %d:\n%s\nstderr:\n%s", len(remedy), tc.warnings, strings.Join(remedy, "\n"), run.stderr)
			}
			for _, line := range remedy {
				// The container name is derived from the temp repo path; keep it
				// out of the substring checks so it can never satisfy or trip them.
				lower := strings.ReplaceAll(strings.ToLower(line), strings.ToLower(containerName), "")
				for _, m := range tc.mentions {
					if !strings.Contains(lower, m) {
						t.Errorf("the drift warning must mention %q; got: %s", m, line)
					}
				}
				for _, m := range tc.notMention {
					if strings.Contains(lower, m) {
						t.Errorf("the drift warning must not mention %q; got: %s", m, line)
					}
				}
			}
		})
	}
}

// TestOpenAttach_LimitsProbeTimesOut_NoWarningAndAttachContinues is AC9's
// timeout half: a limits inspect that never returns is cut off by the probe's
// bounded timeout, prints no warning, and the attach still goes ahead. The
// wrapper runtime hangs only on the limits inspect (recording that it was
// reached) and delegates everything else to the scripted fake.
func TestOpenAttach_LimitsProbeTimesOut_NoWarningAndAttachContinues(t *testing.T) {
	repoRoot := configuredRepoEnv(t, `{}`)
	containerName := containerNameFor(repoRoot)
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	hangLog := filepath.Join(fakeDir, "hung.txt")
	for _, rt := range []string{"docker", "podman"} {
		writeScriptedRuntime(t, fakeDir, rt+".real", callLog)
		// `exec sleep` (never a bare sleep) so the context kill reaches the
		// process that holds the output pipes (watch/docs/go-gotchas.md).
		wrapper := "#!/bin/sh\n" +
			"case \"$*\" in\n" +
			"*'.HostConfig.PidsLimit'*)\n" +
			"  printf '%s\\n' \"$*\" >> " + exectest.ShellQuote(hangLog) + "\n" +
			"  exec sleep 60\n" +
			"  ;;\n" +
			"esac\n" +
			"exec \"$0.real\" \"$@\"\n"
		exectest.WriteExecutable(t, filepath.Join(fakeDir, rt), wrapper)
	}
	// The launcher tags its pane through `tmux` on PATH (#1252): a fake keeps
	// the real /usr/bin/tmux away from a developer's live pane.
	writeFakeTmux(t, fakeDir)
	assets := writeAssetFixture(t)
	env := openAttachEnvFor(t, fakeDir, assets, containerName, defaultPluginsContainerEnv)
	// Would warn (unlimited container) if the probe ever answered.
	env = append(env, "FAKE_CONTAINER_LIMITS=0|0|<nil>")

	start := time.Now()
	run := runCenci(t, env, repoRoot, "open")
	elapsed := time.Since(start)

	if run.exitCode != 0 {
		t.Fatalf("open (attach, hung limits probe): exit %d, want the attach to continue\nstderr:\n%s", run.exitCode, run.stderr)
	}
	if elapsed > 30*time.Second {
		t.Errorf("the attach took %v: the limits probe must be bounded by a short timeout, not wait for the hung runtime", elapsed)
	}
	if hung, err := os.ReadFile(hangLog); err != nil || !strings.Contains(string(hung), ".HostConfig.PidsLimit") {
		t.Errorf("the limits probe was never reached, so this test proved nothing (hang log: %q, err: %v)", hung, err)
	}
	if remedy := stopRemedyLines(run.stderr+run.stdout, containerName); len(remedy) != 0 {
		t.Errorf("a timed-out probe must print no warning; got:\n%s", strings.Join(remedy, "\n"))
	}
	attachLine(t, callLogLines(t, callLog))
}
