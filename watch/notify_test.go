package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/modelswitch"
)

func TestNotifyCodexPromptSendsOnlyCompactTaskName(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.Accept(ctx)

	rawPrompt := "\n  improve\t codex tmux names\x00  \nprivate second line"
	input := `{"hook_event_name":"UserPromptSubmit","session_id":"codex-sess","prompt":` + string(mustJSON(t, rawPrompt)) + `}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "codex", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		if event.TaskName != "improve codex tmux names" {
			t.Fatalf("task_name = %q", event.TaskName)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private second line") || strings.Contains(string(encoded), "prompt") {
			t.Fatalf("raw prompt leaked into IPC: %s", encoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
}

// TestNotifyOpenCodePromptSendsOnlyCompactTaskName extends the existing Codex
// coverage (#488): notify_cmd.go's task-name-from-prompt gate is currently
// codex-only (`event.Agent == "codex" && event.EventType ==
// "UserPromptSubmit"`), so `-agent opencode` on a UserPromptSubmit event must
// also derive and send a compact TaskName, without leaking the raw prompt.
func TestNotifyOpenCodePromptSendsOnlyCompactTaskName(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.Accept(ctx)

	rawPrompt := "\n  improve\t opencode tmux names\x00  \nprivate second line"
	input := `{"hook_event_name":"UserPromptSubmit","session_id":"oc-sess","prompt":` + string(mustJSON(t, rawPrompt)) + `}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "opencode", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		if event.TaskName != "improve opencode tmux names" {
			t.Fatalf("task_name = %q", event.TaskName)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private second line") || strings.Contains(string(encoded), "prompt") {
			t.Fatalf("raw prompt leaked into IPC: %s", encoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
}

// TestNotifyParsesAgentIDFromStdin covers ticket #277: the real runNotify
// stdin-parsing path must decode the "agent_id" JSON key into
// ipc.HookEvent.AgentID, since that's the field the daemon's subagent guard
// (internal/daemon/event.go) relies on.
func TestNotifyParsesAgentIDFromStdin(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.Accept(ctx)

	input := `{"hook_event_name":"Stop","session_id":"s1","agent_id":"sub1"}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		if event.AgentID != "sub1" {
			t.Fatalf("agent_id = %q, want %q", event.AgentID, "sub1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
}

// TestNotifyParsesSessionEndReasonFromStdin covers ticket #707's real entry
// point: the runNotify stdin-parsing path must decode Claude Code's "reason"
// JSON key on SessionEnd into ipc.HookEvent.SessionEndReason, since that's
// the field the daemon's clear/resume window handoff keys on
// (internal/daemon/event.go). Covers both continuation reasons and the
// absent-reason fallback from an older Claude Code.
func TestNotifyParsesSessionEndReasonFromStdin(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"clear", `{"hook_event_name":"SessionEnd","session_id":"s1","reason":"clear"}`, "clear"},
		{"resume", `{"hook_event_name":"SessionEnd","session_id":"s1","reason":"resume"}`, "resume"},
		{"absent reason stays empty", `{"hook_event_name":"SessionEnd","session_id":"s1"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "events.sock")
			receiver, err := ipc.NewEventReceiver(socket)
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer func() { _ = receiver.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go receiver.Accept(ctx)

			cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
			cmd.Stdin = strings.NewReader(tc.input)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("notify: %v: %s", err, output)
			}

			select {
			case event := <-receiver.Events():
				if event.SessionEndReason != tc.want {
					t.Fatalf("session_end_reason = %q, want %q", event.SessionEndReason, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for notify event")
			}
		})
	}
}

// TestNotifyHostBranch_UnreachableSocketExitsZeroWithEmptyStdout covers
// ticket #1122's AC #8 regression requirement: on the host (CENCI_SANDBOX
// unset/cleared), notify's exit-0/empty-stdout contract for an unreachable
// event socket must stay byte-for-byte unchanged by the new in-sandbox
// drop-marker behavior.
func TestNotifyHostBranch_UnreachableSocketExitsZeroWithEmptyStdout(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "no-daemon-here.sock")
	input := `{"hook_event_name":"Stop","session_id":"host-regression-1"}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(), "CENCI_SANDBOX=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("notify: %v (stderr: %s)", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("expected empty stdout, got: %q", stdout.String())
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCodexHooksJSONHasNoUnknownKeys guards against unsupported keys leaking
// into the Codex hooks file. In particular, Codex currently does not support
// asynchronous hooks, and emits a startup warning for each such hook.
func TestCodexHooksJSONHasNoUnknownKeys(t *testing.T) {
	// main_test runs with cwd = package dir (repo root), so this resolves.
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}
	if len(root.Hooks) == 0 {
		t.Fatal("expected at least one event group in codex hooks.json")
	}

	allowedGroupKeys := map[string]bool{"matcher": true, "hooks": true}
	allowedHookKeys := map[string]bool{"type": true, "command": true, "timeout": true}

	for event, groups := range root.Hooks {
		for i, group := range groups {
			for key := range group {
				if !allowedGroupKeys[key] {
					t.Errorf("%s[%d]: unexpected group key %q (allowed: matcher, hooks)", event, i, key)
				}
			}
			raw, ok := group["hooks"]
			if !ok {
				t.Errorf("%s[%d]: missing required \"hooks\" key", event, i)
				continue
			}
			var hooks []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &hooks); err != nil {
				t.Errorf("%s[%d]: parse hooks array: %v", event, i, err)
				continue
			}
			for j, hook := range hooks {
				for key := range hook {
					if !allowedHookKeys[key] {
						t.Errorf("%s[%d].hooks[%d]: unexpected hook key %q (allowed: type, command, timeout)", event, i, j, key)
					}
				}
			}
		}
	}
}

// TestCodexHooksUseNotifyWrapper guards the Codex hooks.json contract
// (#1152): every hook command must go through the codex/notify.sh wrapper,
// which resolves a working cenci binary (the plugin-local path first,
// falling back through lib/resolve-bin.sh) rather than hard-pinning the
// plugin-local path directly in the command string — a missing/failed
// release download must not leave every hook permanently broken with no
// fallback.
func TestCodexHooksUseNotifyWrapper(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}

	const notifyWrapper = `"${PLUGIN_ROOT}/codex/notify.sh"`
	for event, groups := range root.Hooks {
		for i, group := range groups {
			for j, hook := range group.Hooks {
				if !strings.Contains(hook.Command, notifyWrapper) {
					t.Errorf("%s[%d].hooks[%d] does not use the codex/notify.sh wrapper: %q", event, i, j, hook.Command)
				}
			}
		}
	}
}

func TestCodexStopHookEmitsJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}
	stopGroups := root.Hooks["Stop"]
	if len(stopGroups) != 1 || len(stopGroups[0].Hooks) != 1 {
		t.Fatalf("expected exactly one Codex Stop hook, got %#v", stopGroups)
	}

	pluginRoot, pluginData, stub := newHookPluginFixture(t, "codex")
	marker := filepath.Join(t.TempDir(), "stop-ran")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf ran >\"$CENCI_TEST_STOP_MARKER\"\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatalf("write hook binary stub: %v", err)
	}

	cmd := exec.Command("sh", "-c", stopGroups[0].Hooks[0].Command)
	cmd.Env = append(os.Environ(), "PLUGIN_ROOT="+pluginRoot, "PLUGIN_DATA="+pluginData,
		"CLAUDE_PLUGIN_DATA=", "CENCI_TEST_STOP_MARKER="+marker,
		// A wrapper regression must not reach the host daemon or health store.
		"CENCI_SOCKET_DIR="+t.TempDir(), "XDG_STATE_HOME="+t.TempDir(), "CENCI_SANDBOX=1")
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"test"}`)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run Codex Stop hook: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Codex Stop hook output must be JSON, got %q: %v", output, err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "ran" {
		t.Fatalf("Codex Stop wrapper did not invoke its binary: marker=%q, err=%v", got, err)
	}
}

// newHookPluginFixture copies the real notification wrapper and its runtime
// dependencies. Codex fixtures provision the shim in the same external,
// versioned data directory used by native hooks; Claude retains ROOT/bin.
func newHookPluginFixture(t *testing.T, clientDir string) (root, dataDir, binary string) {
	t.Helper()
	root, dataDir = t.TempDir(), t.TempDir()
	manifestDir := ".claude-plugin"
	files := []string{filepath.Join(clientDir, "notify.sh"), "lib/resolve-bin.sh", "lib/hook-health.sh"}
	if clientDir == "codex" {
		manifestDir = ".codex-plugin"
		files = append(files, "lib/codex-bin-dir.sh")
	}
	files = append(files, filepath.Join(manifestDir, "plugin.json"))
	for _, rel := range files {
		contents, err := os.ReadFile(filepath.Join("plugin", rel))
		if err != nil {
			t.Fatalf("read plugin fixture %s: %v", rel, err)
		}
		dest := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatalf("create plugin fixture directory: %v", err)
		}
		if err := os.WriteFile(dest, contents, 0o755); err != nil {
			t.Fatalf("copy plugin fixture %s: %v", rel, err)
		}
	}
	binary = filepath.Join(root, "bin", "cenci")
	if clientDir == "codex" {
		contents, err := os.ReadFile(filepath.Join(root, manifestDir, "plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct{ Version string }
		if err := json.Unmarshal(contents, &manifest); err != nil || manifest.Version == "" {
			t.Fatalf("read Codex plugin version: %v", err)
		}
		binary = filepath.Join(dataDir, manifest.Version, "bin", "cenci")
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatalf("create fixture binary directory: %v", err)
	}
	return root, dataDir, binary
}

// codexManifestHook is one hook command registered in plugin/codex/hooks.json
// for an event, together with the group-level fields the tests pin.
type codexManifestHook struct {
	Command    string
	Timeout    int
	HasMatcher bool
}

// codexManifestHooks returns every hook command the Codex manifest registers
// for event (nil when the event is not registered).
func codexManifestHooks(t *testing.T, event string) []codexManifestHook {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}
	var root struct {
		Hooks map[string][]struct {
			Matcher *string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}
	var out []codexManifestHook
	for _, group := range root.Hooks[event] {
		for _, hook := range group.Hooks {
			out = append(out, codexManifestHook{Command: hook.Command, Timeout: hook.Timeout, HasMatcher: group.Matcher != nil})
		}
	}
	return out
}

// runCodexManifestHook runs the single command the Codex manifest registers for
// event, exactly as Codex would (sh -c with PLUGIN_ROOT set), and returns its
// stdout, its exit error and the ipc.HookEvent that reached a temp event socket.
//
// The temp PLUGIN_ROOT holds the real codex/notify.sh and its runtime files.
// A PLUGIN_DATA/<version>/bin/cenci shim execs the test-built binary with -event-socket pointing
// at the temp socket, so the default daemon socket is never touched and no
// daemon is autostarted. The hook command swallows failures (`|| true`), so
// receiving the event on the temp socket is also the proof that the shim — and
// therefore the wrapper — actually ran.
func runCodexManifestHook(t *testing.T, event, payload string) (stdout string, got ipc.HookEvent, runErr error) {
	t.Helper()
	hooks := codexManifestHooks(t, event)
	if len(hooks) != 1 {
		t.Fatalf("expected exactly one Codex %s hook in hooks.json, got %d", event, len(hooks))
	}

	sockDir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go receiver.Accept(ctx)

	pluginRoot, pluginData, fixtureBin := newHookPluginFixture(t, "codex")
	shim := "#!/bin/sh\nexec \"$CENCI_TEST_BIN\" \"$@\" -event-socket \"$CENCI_TEST_SOCKET\"\n"
	if err := os.WriteFile(fixtureBin, []byte(shim), 0o755); err != nil {
		t.Fatalf("write cenci shim: %v", err)
	}

	cmd := exec.Command("sh", "-c", hooks[0].Command)
	cmd.Env = append(os.Environ(),
		"PLUGIN_ROOT="+pluginRoot,
		"PLUGIN_DATA="+pluginData,
		"CLAUDE_PLUGIN_DATA=",
		"CENCI_TEST_BIN="+binaryPath,
		"CENCI_TEST_SOCKET="+socket,
		"CENCI_SANDBOX=",
		// A wrapper fallback to an installed cenci must never reach the real daemon.
		"CENCI_SOCKET_DIR="+t.TempDir(),
		"XDG_STATE_HOME="+t.TempDir(),
	)
	cmd.Stdin = strings.NewReader(payload)
	var out bytes.Buffer
	cmd.Stdout = &out
	runErr = cmd.Run()

	select {
	case got = <-receiver.Events():
	case <-time.After(2 * time.Second):
		t.Fatalf("codex %s hook delivered no event to the temp socket (shim not run?)", event)
	}
	return out.String(), got, runErr
}

// TestCodexInterruptAndSessionEndHooks covers #1238 AC1: the manifest registers
// Codex Interrupt and SessionEnd through the notify wrapper with a short
// timeout, no matcher and no stdout, and the real command delivers the exact
// event to the daemon socket.
func TestCodexInterruptAndSessionEndHooks(t *testing.T) {
	cases := []struct {
		event      string
		payload    string
		wantReason string
	}{
		{"Interrupt", `{"hook_event_name":"Interrupt","session_id":"codex-sess"}`, ""},
		{"SessionEnd", `{"hook_event_name":"SessionEnd","session_id":"codex-sess","reason":"other"}`, "other"},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			hooks := codexManifestHooks(t, tc.event)
			if len(hooks) != 1 {
				t.Fatalf("expected exactly one Codex %s hook, got %d", tc.event, len(hooks))
			}
			if hooks[0].Timeout <= 0 || hooks[0].Timeout > 3 {
				t.Errorf("%s timeout = %d, want 1..3", tc.event, hooks[0].Timeout)
			}
			if hooks[0].HasMatcher {
				t.Errorf("%s must not declare a matcher", tc.event)
			}
			if !strings.Contains(hooks[0].Command, `"${PLUGIN_ROOT}/codex/notify.sh"`) {
				t.Errorf("%s does not use the notify wrapper: %q", tc.event, hooks[0].Command)
			}

			stdout, got, runErr := runCodexManifestHook(t, tc.event, tc.payload)
			if runErr != nil {
				t.Fatalf("run %s hook: %v", tc.event, runErr)
			}
			if stdout != "" {
				t.Errorf("%s hook must write nothing to stdout, got %q", tc.event, stdout)
			}
			if got.EventType != tc.event {
				t.Errorf("event_type = %q, want %q", got.EventType, tc.event)
			}
			if got.Agent != "codex" {
				t.Errorf("agent = %q, want codex", got.Agent)
			}
			if got.SessionID != "codex-sess" {
				t.Errorf("session_id = %q, want codex-sess", got.SessionID)
			}
			if got.AgentID != "" {
				t.Errorf("agent_id = %q, want empty for a main-session event", got.AgentID)
			}
			if got.SessionEndReason != tc.wantReason {
				t.Errorf("session_end_reason = %q, want %q", got.SessionEndReason, tc.wantReason)
			}
		})
	}
}

// TestCodexManifestChildPayloadKeepsAgentID pins that an agent_id-tagged
// Interrupt keeps its AgentID end to end, since that field is what the
// daemon's subagent guards key on.
func TestCodexManifestChildPayloadKeepsAgentID(t *testing.T) {
	_, got, runErr := runCodexManifestHook(t, "Interrupt", `{"hook_event_name":"Interrupt","session_id":"codex-sess","agent_id":"child-1"}`)
	if runErr != nil {
		t.Fatalf("run Interrupt hook: %v", runErr)
	}
	if got.AgentID != "child-1" {
		t.Errorf("agent_id = %q, want child-1", got.AgentID)
	}
	if got.EventType != "Interrupt" || got.Agent != "codex" {
		t.Errorf("event = %+v, want a codex Interrupt", got)
	}
}

// TestCodexManifestRegistersNoSubagentHooks pins #1238 AC8: no subagent hook is
// registered, so child lifecycle events never reach the daemon.
func TestCodexManifestRegistersNoSubagentHooks(t *testing.T) {
	for _, event := range []string{"SubagentStart", "SubagentStop"} {
		if hooks := codexManifestHooks(t, event); len(hooks) != 0 {
			t.Errorf("%s must not be registered in the Codex manifest, got %+v", event, hooks)
		}
	}
}

// TestNotifyStopForwardsInFlightBackgroundWork covers ticket #698: Claude
// Code's Stop hook carries a background_tasks array describing in-flight
// background work ("running/pending + backgrounded"). notify must parse it and
// flag the event so the daemon can tell "session is done" from "session is
// paused waiting for background work to wake it".
//
// The task descriptions and shell command lines must never reach IPC, matching
// the raw-prompt privacy posture of the UserPromptSubmit path.
func TestNotifyStopForwardsInFlightBackgroundWork(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.Accept(ctx)

	input := `{"hook_event_name":"Stop","session_id":"sess1","stop_hook_active":false,` +
		`"background_tasks":[{"id":"t1","type":"subagent","status":"running",` +
		`"description":"secret task description","agent_type":"code-reviewer"},` +
		`{"id":"t2","type":"shell","status":"completed","description":"done already",` +
		`"command":"secret --command-line"}]}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		if !event.BackgroundWork {
			t.Fatalf("background_work = false, want true — a running subagent task is in flight")
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "secret task description") ||
			strings.Contains(string(encoded), "secret --command-line") {
			t.Fatalf("background task description/command leaked into IPC: %s", encoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
}

// TestNotifyStopWithOnlyTerminalBackgroundTasksReportsNoWork asserts the
// narrow exclusion: only task states that cannot wake the session on their own
// (completed, failed, killed, paused) are discounted. A Stop whose tasks have
// all finished — or are all parked — is an ordinary finished turn.
func TestNotifyStopWithOnlyTerminalBackgroundTasksReportsNoWork(t *testing.T) {
	for name, tasks := range map[string]string{
		"absent":    ``,
		"empty":     `,"background_tasks":[]`,
		"completed": `,"background_tasks":[{"id":"t1","type":"subagent","status":"completed","description":"d"}]`,
		"failed":    `,"background_tasks":[{"id":"t1","type":"shell","status":"failed","description":"d"}]`,
		"killed":    `,"background_tasks":[{"id":"t1","type":"workflow","status":"killed","description":"d"}]`,
		// #1079: a paused task is resumed by the user or the agent, never by
		// itself, so it must not hold the session at running.
		"paused": `,"background_tasks":[{"id":"t1","type":"shell","status":"paused","description":"d"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "events.sock")
			receiver, err := ipc.NewEventReceiver(socket)
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer func() { _ = receiver.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go receiver.Accept(ctx)

			input := `{"hook_event_name":"Stop","session_id":"sess1","stop_hook_active":false` + tasks + `}`
			cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
			cmd.Stdin = strings.NewReader(input)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("notify: %v: %s", err, output)
			}

			select {
			case event := <-receiver.Events():
				if event.BackgroundWork {
					t.Fatalf("background_work = true, want false for %s background_tasks", name)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for notify event")
			}
		})
	}
}

// TestNotifyStopWithPendingBackgroundTaskReportsWork pins the other side of
// that exclusion: pending work still wakes the session later, and an
// unrecognized status must count as in flight rather than silently collapsing
// to "done" — background_tasks is documented to contain only in-flight work.
func TestNotifyStopWithPendingBackgroundTaskReportsWork(t *testing.T) {
	for _, status := range []string{"pending", "running", "some_future_status"} {
		t.Run(status, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "events.sock")
			receiver, err := ipc.NewEventReceiver(socket)
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer func() { _ = receiver.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go receiver.Accept(ctx)

			input := `{"hook_event_name":"Stop","session_id":"sess1","stop_hook_active":false,` +
				`"background_tasks":[{"id":"t1","type":"subagent","status":"` + status + `","description":"d"}]}`
			cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
			cmd.Stdin = strings.NewReader(input)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("notify: %v: %s", err, output)
			}

			select {
			case event := <-receiver.Events():
				if !event.BackgroundWork {
					t.Fatalf("background_work = false, want true for status %q", status)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for notify event")
			}
		})
	}
}

// TestNotifyStopWithPendingSessionCronReportsBackgroundWork covers ticket
// #705: ScheduleWakeup and /loop timers are NOT reported in background_tasks —
// they appear in the sibling session_crons array on the same Stop input.
// Entries carry no status field; any entry means the session will be woken,
// so it must flag the event exactly like in-flight background work. The
// cron's prompt must never reach IPC, matching the raw-prompt privacy posture
// of UserPromptSubmit and background_tasks.
func TestNotifyStopWithPendingSessionCronReportsBackgroundWork(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		want    bool
	}{
		"pending cron": {
			payload: `,"background_tasks":[],"session_crons":[{"id":"c1","schedule":"in 20m",` +
				`"recurring":false,"prompt":"secret loop prompt"}]`,
			want: true,
		},
		"empty crons and tasks": {
			payload: `,"background_tasks":[],"session_crons":[]`,
			want:    false,
		},
		"terminal tasks but pending cron": {
			payload: `,"background_tasks":[{"id":"t1","type":"shell","status":"completed","description":"d"}],` +
				`"session_crons":[{"id":"c1","schedule":"*/5 * * * *","recurring":true,"prompt":"secret loop prompt"}]`,
			want: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "events.sock")
			receiver, err := ipc.NewEventReceiver(socket)
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer func() { _ = receiver.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go receiver.Accept(ctx)

			input := `{"hook_event_name":"Stop","session_id":"sess1","stop_hook_active":false` + tc.payload + `}`
			cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
			cmd.Stdin = strings.NewReader(input)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("notify: %v: %s", err, output)
			}

			select {
			case event := <-receiver.Events():
				if event.BackgroundWork != tc.want {
					t.Fatalf("background_work = %v, want %v for %s", event.BackgroundWork, tc.want, name)
				}
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "secret loop prompt") {
					t.Fatalf("session cron prompt leaked into IPC: %s", encoded)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for notify event")
			}
		})
	}
}

// -- watch.excludeCwd (#1240) ---------------------------------------------
//
// The opt-in exclusion list lives in the fleet config
// ($XDG_CONFIG_HOME/cenci/config.json, key watch.excludeCwd). Every test below
// drives the real built binary over stdin and a temp event socket, and proves
// non-delivery the only way a fire-and-forget receiver allows: send the
// candidate event, then a control event with no cwd, wait for the control, drain
// a short window, and assert the candidate never arrived.

const (
	excludeControlSession = "control"
	excludeDrainWindow    = 200 * time.Millisecond
)

// notifyFixture is a temp event socket with a running receiver, plus an
// isolated socket dir so the -event-socket flag default never warns on stderr.
type notifyFixture struct {
	socket   string
	receiver *ipc.EventReceiver
}

func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	setTempSocketDir(t)
	// Short path: t.TempDir() embeds the full subtest name and can overflow sun_path.
	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go receiver.Accept(ctx)
	return &notifyFixture{socket: socket, receiver: receiver}
}

// writeFleetConfig points XDG_CONFIG_HOME at a fresh temp dir and seeds the
// fleet config there through writeConfigJSON. An empty body writes no file (the
// missing-file case).
func writeFleetConfig(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if body != "" {
		writeConfigJSON(t, home, body)
	}
}

// excludeConfig renders a fleet config whose watch.excludeCwd is entries.
func excludeConfig(t *testing.T, entries ...string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"watch": map[string]any{"excludeCwd": entries}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hookPayload builds a hook stdin payload; a nil cwd omits the key entirely.
func hookPayload(t *testing.T, eventName, session string, cwd *string) string {
	t.Helper()
	m := map[string]any{"hook_event_name": eventName, "session_id": session}
	if cwd != nil {
		m["cwd"] = *cwd
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func strPtr(s string) *string { return &s }

// notifyRun runs `cenci notify` with the given payload. An empty socket omits
// -event-socket so the default socket is used. TMUX_PANE and CENCI_SANDBOX are
// cleared so the run is paneless and on the host regardless of the ambient
// environment. It fails the test on a non-zero exit and returns stderr.
func notifyRun(t *testing.T, socket, agent, payload string, extraEnv ...string) (stderr string) {
	t.Helper()
	args := []string{"notify", "-agent", agent}
	if socket != "" {
		args = append(args, "-event-socket", socket)
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append(append(os.Environ(), "TMUX_PANE=", "CENCI_SANDBOX="), extraEnv...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("notify: %v (stderr: %s)", err, errBuf.String())
	}
	return errBuf.String()
}

// collectEvents sends the control event, waits for it, drains for a short
// window and returns every non-control event that arrived. Send order does not
// guarantee receive order (the receiver handles each connection in its own
// goroutine), so absence can only be asserted after the control has arrived.
func collectEvents(t *testing.T, fx *notifyFixture) []ipc.HookEvent {
	t.Helper()
	notifyRun(t, fx.socket, "claude", hookPayload(t, "Stop", excludeControlSession, nil))
	var got []ipc.HookEvent
	timeout := time.After(2 * time.Second)
	for sawControl := false; !sawControl; {
		select {
		case ev := <-fx.receiver.Events():
			if ev.SessionID == excludeControlSession {
				sawControl = true
			} else {
				got = append(got, ev)
			}
		case <-timeout:
			t.Fatal("control event never arrived")
		}
	}
	drain := time.After(excludeDrainWindow)
	for {
		select {
		case ev := <-fx.receiver.Events():
			if ev.SessionID != excludeControlSession {
				got = append(got, ev)
			}
		case <-drain:
			return got
		}
	}
}

// assertOnlyControlArrives fails if any event for candidateSession was delivered.
func assertOnlyControlArrives(t *testing.T, fx *notifyFixture, candidateSession string) {
	t.Helper()
	for _, ev := range collectEvents(t, fx) {
		if ev.SessionID == candidateSession {
			t.Fatalf("event for session %q was delivered, want it dropped: %+v", candidateSession, ev)
		}
	}
}

// assertDelivered fails unless an event for session reaches the receiver.
func assertDelivered(t *testing.T, fx *notifyFixture, session string) {
	t.Helper()
	select {
	case ev := <-fx.receiver.Events():
		if ev.SessionID != session {
			t.Fatalf("session_id = %q, want %q", ev.SessionID, session)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("event for session %q was not delivered", session)
	}
}

// TestNotifyExcludeCwdDropsListedEvents covers AC1: an event whose cwd is a
// listed path, or sits below it at a segment boundary, never reaches the daemon
// socket, for both Claude and Codex.
func TestNotifyExcludeCwdDropsListedEvents(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, cwd := range []string{"/tmp", "/tmp/probe/x"} {
			t.Run(agent+" "+cwd, func(t *testing.T) {
				fx := newNotifyFixture(t)
				writeFleetConfig(t, excludeConfig(t, "/tmp"))
				const session = "excluded"
				notifyRun(t, fx.socket, agent, hookPayload(t, "UserPromptSubmit", session, strPtr(cwd)))
				assertOnlyControlArrives(t, fx, session)
			})
		}
	}
}

// TestNotifyExcludeCwdFailOpen covers AC2 and the non-array / non-string edge
// cases: any absent, empty, missing or unusable config excludes nothing, and
// nothing is written to stderr.
func TestNotifyExcludeCwdFailOpen(t *testing.T) {
	cases := []struct {
		name string
		body string // empty: no config file is written
	}{
		{"key absent", `{"watch":{}}`},
		{"empty list", `{"watch":{"excludeCwd":[]}}`},
		{"file missing", ""},
		{"malformed JSON", `{"watch":{"excludeCwd":["/tmp"]`},
		{"watch not an object", `{"watch":"/tmp"}`},
		{"excludeCwd a string", `{"watch":{"excludeCwd":"/tmp"}}`},
		{"mixed list, bad element last", `{"watch":{"excludeCwd":["/tmp",5]}}`},
		{"mixed list, bad element first", `{"watch":{"excludeCwd":[5,"/tmp"]}}`},
		{"mixed list, null element last", `{"watch":{"excludeCwd":["/tmp",null]}}`},
		{"mixed list, null element first", `{"watch":{"excludeCwd":[null,"/tmp"]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newNotifyFixture(t)
			writeFleetConfig(t, tc.body)
			const session = "failopen"
			stderr := notifyRun(t, fx.socket, "claude", hookPayload(t, "Stop", session, strPtr("/tmp/probe")))
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			assertDelivered(t, fx, session)
		})
	}
}

// TestNotifyExcludeCwdRegressionsStayVisible covers AC3: the exclusion is
// narrow. Everything that is not a listed path stays visible.
func TestNotifyExcludeCwdRegressionsStayVisible(t *testing.T) {
	payloadOnly := []struct {
		name    string
		entries []string
		cwd     *string
	}{
		{"unmanaged run outside the list", []string{"/tmp"}, strPtr("/home/someone/work")},
		{"no cwd key", []string{"/tmp"}, nil},
		{"sibling prefix /tmpfoo", []string{"/tmp"}, strPtr("/tmpfoo")},
		{"sibling prefix below /tmpfoo", []string{"/tmp"}, strPtr("/tmpfoo/probe")},
		{"parent of the listed path", []string{"/tmp/probe"}, strPtr("/tmp")},
		{"dot-dot resolves out of the listed path", []string{"/tmp"}, strPtr("/tmp/../home/x")},
	}
	for _, tc := range payloadOnly {
		t.Run(tc.name, func(t *testing.T) {
			fx := newNotifyFixture(t)
			writeFleetConfig(t, excludeConfig(t, tc.entries...))
			const session = "visible"
			stderr := notifyRun(t, fx.socket, "claude", hookPayload(t, "Stop", session, tc.cwd))
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			assertDelivered(t, fx, session)
		})
	}

	// A legitimate cenci headless run: paneless (TMUX_PANE is cleared by
	// notifyRun) and inside a repo with .cenci/config.json. The listed probe
	// directory is a sibling of the repo, not an ancestor, because t.TempDir()
	// lives under os.TempDir() and a literal /tmp entry would match it.
	t.Run("cenci headless run inside a repo", func(t *testing.T) {
		fx := newNotifyFixture(t)
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		if err := os.MkdirAll(filepath.Join(repo, ".cenci"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".cenci", "config.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		writeFleetConfig(t, excludeConfig(t, filepath.Join(root, "probe")))
		const session = "headless-repo"
		stderr := notifyRun(t, fx.socket, "codex", hookPayload(t, "Stop", session, strPtr(repo)))
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		assertDelivered(t, fx, session)
	})
}

// TestNotifyExcludeCwdEdgeCases pins the matcher's boundary behavior from the
// ticket's Edge Cases through the real stdin, config and socket stack.
func TestNotifyExcludeCwdEdgeCases(t *testing.T) {
	cases := []struct {
		name        string
		entries     []string
		cwd         string
		wantDropped bool
	}{
		{"relative cwd is delivered", []string{"/tmp"}, "tmp/x", false},
		{"empty cwd is delivered", []string{"/tmp"}, "", false},
		{"trailing-slash entry excludes a child", []string{"/tmp/"}, "/tmp/probe", true},
		{"trailing-slash entry excludes the directory itself", []string{"/tmp/"}, "/tmp", true},
		{"trailing-slash entry keeps the sibling prefix", []string{"/tmp/"}, "/tmpfoo", false},
		{"root entry excludes every absolute cwd", []string{"/"}, "/anything", true},
		{"relative and empty entries are ignored", []string{"", "tmp"}, "/tmp/x", false},
		{"absolute entry still applies next to ignored ones", []string{"", "tmp", "/tmp"}, "/tmp/x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newNotifyFixture(t)
			writeFleetConfig(t, excludeConfig(t, tc.entries...))
			const session = "edge"
			notifyRun(t, fx.socket, "claude", hookPayload(t, "Stop", session, strPtr(tc.cwd)))
			if tc.wantDropped {
				assertOnlyControlArrives(t, fx, session)
			} else {
				assertDelivered(t, fx, session)
			}
		})
	}
}

// TestNotifyExcludeCwdIsPerEvent pins that the decision is made per event: a
// later event from the same session with a non-listed cwd is delivered even
// though an earlier one was dropped.
func TestNotifyExcludeCwdIsPerEvent(t *testing.T) {
	fx := newNotifyFixture(t)
	writeFleetConfig(t, excludeConfig(t, "/tmp"))
	const session = "per-event"
	notifyRun(t, fx.socket, "claude", hookPayload(t, "UserPromptSubmit", session, strPtr("/tmp/probe")))
	notifyRun(t, fx.socket, "claude", hookPayload(t, "Stop", session, strPtr("/home/someone/work")))

	got := collectEvents(t, fx)
	if len(got) != 1 {
		t.Fatalf("delivered %d events for the session, want exactly 1 (the Stop): %+v", len(got), got)
	}
	if got[0].SessionID != session || got[0].EventType != "Stop" {
		t.Fatalf("delivered event = %+v, want the Stop for session %q", got[0], session)
	}
}

// TestNotifyExcludedEventDoesNotAutostartDaemon pins that an excluded event
// never reaches the default-socket delivery path, whose retry would start a
// missing daemon. The socket dir is an isolated temp dir, so after the run
// neither the PID file nor the event socket may exist. No positive autostart
// control is run: it would launch a real daemon against the ambient tmux.
func TestNotifyExcludedEventDoesNotAutostartDaemon(t *testing.T) {
	setTempSocketDir(t)
	writeFleetConfig(t, excludeConfig(t, "/tmp"))
	socketDir := os.Getenv("CENCI_SOCKET_DIR")
	pidPath := ipc.DefaultPIDPath()
	eventSocket := ipc.DefaultEventSocketPath()
	// An empty path would make os.Stat("") fail vacuously, and an over-long one
	// falls back to a shared tmp dir; either would void the absence assertions.
	for _, path := range []string{pidPath, eventSocket} {
		if path == "" || socketDir == "" || !strings.HasPrefix(path, socketDir+string(filepath.Separator)) {
			t.Fatalf("path %q is not inside the isolated socket dir %q", path, socketDir)
		}
	}
	// A regression that spawns a daemon must not leak it past the test.
	t.Cleanup(func() {
		_, pidErr := os.Stat(pidPath)
		_, sockErr := os.Stat(eventSocket)
		if pidErr != nil && sockErr != nil {
			return
		}
		if out, err := exec.Command(binaryPath, "daemon", "stop").CombinedOutput(); err != nil {
			t.Errorf("cleanup: daemon stop failed: %v: %s", err, out)
		}
	})

	notifyRun(t, "", "claude", hookPayload(t, "Stop", "no-autostart", strPtr("/tmp/probe")), "TMUX=")

	for _, path := range []string{pidPath, eventSocket} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s exists after an excluded event (stat err = %v), want no daemon autostart", path, err)
		}
	}
}

// --- PostModelSwitch (#1179) ------------------------------------------------

// modelSwitchNotify runs the real `cenci notify` binary with input on stdin
// against a receiver and an isolated state home, and returns the delivered
// event plus the store directory a Claude run would have written.
func modelSwitchNotify(t *testing.T, agent, input string) (ipc.HookEvent, string) {
	t.Helper()
	stateHome := t.TempDir()
	if err := os.Chmod(stateHome, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go receiver.Accept(ctx)

	cmd := exec.Command(binaryPath, "notify", "-agent", agent, "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	// XDG_CONFIG_HOME is isolated too: an ambient watch.excludeCwd covering the
	// payload's cwd would make notify return before recording or delivering
	// (watch/docs/test-isolation.md, "Ambient config-dir isolation").
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome, "XDG_CONFIG_HOME="+t.TempDir(), "CLAUDE_CODE_SESSION_ID=")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		return event, filepath.Join(stateHome, "cenci", "model-switch")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
	return ipc.HookEvent{}, ""
}

// TestNotifyParsesModelSwitchFromStdin covers AC4 and AC5 through the real
// entry point (watch/docs/hook-events.md): a PostModelSwitch payload shaped
// like the captured one reaches the daemon with the session id, the agent id
// when present, both models, the agent type and the source, and the same
// switch lands in the hook-side store.
func TestNotifyParsesModelSwitchFromStdin(t *testing.T) {
	const common = `"hook_event_name":"PostModelSwitch","session_id":"ms-session","transcript_path":"/tmp/t.jsonl","cwd":"/tmp","from_model":"claude-sonnet-5-5","to_model":"claude-opus-5-5","requested_model":"opus","context_tokens":1234,"prompt_cache_warm":true`
	cases := []struct {
		name          string
		input         string
		wantAgentID   string
		wantAgentType string
		wantSource    string
		wantSubagent  bool
	}{
		{"main session, no agent id", `{` + common + `,"source":"auto"}`, "", "", "auto", false},
		{"subagent with agent id and type", `{` + common + `,"source":"sdk","agent_id":"sub1","agent_type":"cenci:reviewer"}`, "sub1", "cenci:reviewer", "sdk", true},
		{"missing source stays empty", `{` + common + `}`, "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, store := modelSwitchNotify(t, "claude", tc.input)

			if event.EventType != "PostModelSwitch" || event.SessionID != "ms-session" {
				t.Errorf("event = %+v", event)
			}
			if event.AgentID != tc.wantAgentID {
				t.Errorf("agent_id = %q, want %q", event.AgentID, tc.wantAgentID)
			}
			if event.AgentType != tc.wantAgentType {
				t.Errorf("agent_type = %q, want %q", event.AgentType, tc.wantAgentType)
			}
			if event.FromModel != "claude-sonnet-5-5" || event.ToModel != "claude-opus-5-5" {
				t.Errorf("models = %q -> %q", event.FromModel, event.ToModel)
			}
			if event.ModelSwitchSource != tc.wantSource {
				t.Errorf("source = %q, want %q", event.ModelSwitchSource, tc.wantSource)
			}

			res := modelswitch.Read(store, "ms-session")
			if res.State != modelswitch.StateOK || len(res.Switches) != 1 {
				t.Fatalf("store = %+v, want one recorded switch", res)
			}
			got := res.Switches[0]
			if got.From != "claude-sonnet-5-5" || got.To != "claude-opus-5-5" || got.Subagent != tc.wantSubagent {
				t.Errorf("stored switch = %+v", got)
			}
			if tc.wantSubagent && got.AgentType != tc.wantAgentType {
				t.Errorf("stored agent type = %q, want %q", got.AgentType, tc.wantAgentType)
			}
		})
	}
}

// TestNotifyModelSwitchSanitizesInvalidStringsBeforeForwardingOrStoring covers
// the validation edge case at the real entry point: control characters and
// over-long values never reach the IPC event or the store; the switch is still
// delivered with the placeholder.
func TestNotifyModelSwitchSanitizesInvalidStringsBeforeForwardingOrStoring(t *testing.T) {
	rawFrom := "claude\x1b[31m-opus\nevil"
	rawTo := strings.Repeat("m", 129)
	rawType := "bad\x1b[0m type"
	input := `{"hook_event_name":"PostModelSwitch","session_id":"ms-session","agent_id":"sub1","agent_type":` +
		string(mustJSON(t, rawType)) + `,"from_model":` + string(mustJSON(t, rawFrom)) +
		`,"to_model":` + string(mustJSON(t, rawTo)) + `,"source":"auto"}`

	event, store := modelSwitchNotify(t, "claude", input)

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{rawFrom, rawTo, rawType, "\\u001b", "evil"} {
		if strings.Contains(string(encoded), raw) {
			t.Errorf("raw value %q was forwarded: %s", raw, encoded)
		}
	}
	if event.FromModel != "(invalid model id)" || event.ToModel != "(invalid model id)" || event.AgentType != "unknown" {
		t.Errorf("event = %+v, want placeholders", event)
	}
	data, err := os.ReadFile(filepath.Join(store, "ms-session"))
	if err != nil {
		t.Fatalf("the switch must still be recorded: %v", err)
	}
	if strings.ContainsAny(string(data), "\x1b") || strings.Contains(string(data), "evil") {
		t.Errorf("raw value reached the store: %q", data)
	}
}

// TestNotifyReadsSourceOnlyForPostModelSwitch pins that SessionStart's own
// `source` field (startup|resume|clear) is never mistaken for a model-switch
// source.
func TestNotifyReadsSourceOnlyForPostModelSwitch(t *testing.T) {
	event, store := modelSwitchNotify(t, "claude", `{"hook_event_name":"SessionStart","session_id":"ms-session","source":"startup"}`)
	if event.ModelSwitchSource != "" || event.FromModel != "" || event.ToModel != "" {
		t.Errorf("SessionStart carried model switch fields: %+v", event)
	}
	if _, err := os.Lstat(store); !os.IsNotExist(err) {
		t.Errorf("a SessionStart created the model-switch store (err = %v)", err)
	}
}

// TestNotifyNonStringModelSwitchFieldsNeverDropAnEvent pins the lenient decode:
// from_model, to_model, agent_type and source are model-switch fields, so a
// non-string value in any of them must never fail the whole payload and drop an
// event of another type (here a SessionStart, whose own `source` is read as a
// string elsewhere, so a numeric one must not matter).
func TestNotifyNonStringModelSwitchFieldsNeverDropAnEvent(t *testing.T) {
	event, store := modelSwitchNotify(t, "claude", `{"hook_event_name":"SessionStart","session_id":"ms-session","agent_id":"sub1","source":7,"agent_type":{"name":"cenci:reviewer"},"from_model":5,"to_model":[1]}`)
	if event.EventType != "SessionStart" || event.SessionID != "ms-session" || event.AgentID != "sub1" {
		t.Errorf("event = %+v, want the SessionStart delivered with its normal fields", event)
	}
	if event.FromModel != "" || event.ToModel != "" || event.ModelSwitchSource != "" || event.AgentType != "" {
		t.Errorf("a non-switch event carried model switch fields: %+v", event)
	}
	if _, err := os.Lstat(store); !os.IsNotExist(err) {
		t.Errorf("a SessionStart created the model-switch store (err = %v)", err)
	}
}

// TestNotifyPostModelSwitchWithNonStringFieldsUsesPlaceholders pins that a
// PostModelSwitch with a non-string field is still delivered and recorded, with
// the existing placeholder in place of the field, never dropped or forwarded raw.
func TestNotifyPostModelSwitchWithNonStringFieldsUsesPlaceholders(t *testing.T) {
	cases := []struct {
		name          string
		input         string
		wantFrom      string
		wantTo        string
		wantSource    string
		wantAgentType string
	}{
		{
			"numeric from_model",
			`{"hook_event_name":"PostModelSwitch","session_id":"ms-session","from_model":12345,"to_model":"claude-opus-5-5","source":"auto"}`,
			"(invalid model id)", "claude-opus-5-5", "auto", "",
		},
		{
			"object to_model",
			`{"hook_event_name":"PostModelSwitch","session_id":"ms-session","from_model":"claude-sonnet-5-5","to_model":{"id":"x"},"source":"auto"}`,
			"claude-sonnet-5-5", "(invalid model id)", "auto", "",
		},
		{
			"numeric source and object agent_type on a subagent",
			`{"hook_event_name":"PostModelSwitch","session_id":"ms-session","agent_id":"sub1","agent_type":{"name":"r"},"from_model":"claude-sonnet-5-5","to_model":"claude-opus-5-5","source":3}`,
			"claude-sonnet-5-5", "claude-opus-5-5", "unknown", "unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, store := modelSwitchNotify(t, "claude", tc.input)
			if event.EventType != "PostModelSwitch" || event.SessionID != "ms-session" {
				t.Fatalf("event = %+v, want the PostModelSwitch delivered", event)
			}
			if event.FromModel != tc.wantFrom || event.ToModel != tc.wantTo || event.ModelSwitchSource != tc.wantSource || event.AgentType != tc.wantAgentType {
				t.Errorf("event = %+v, want from %q to %q source %q agent type %q", event, tc.wantFrom, tc.wantTo, tc.wantSource, tc.wantAgentType)
			}
			res := modelswitch.Read(store, "ms-session")
			if res.State != modelswitch.StateOK || len(res.Switches) != 1 {
				t.Fatalf("store = %+v, want one recorded switch", res)
			}
			if got := res.Switches[0]; got.From != tc.wantFrom || got.To != tc.wantTo {
				t.Errorf("stored switch = %+v", got)
			}
		})
	}
}

// TestNotifyOnlyClaudeRecordsModelSwitches pins the client scoping: Codex and
// OpenCode have no flag fallback, so only `-agent claude` writes the store.
func TestNotifyOnlyClaudeRecordsModelSwitches(t *testing.T) {
	input := `{"hook_event_name":"PostModelSwitch","session_id":"ms-session","from_model":"claude-sonnet-5-5","to_model":"claude-opus-5-5","source":"auto"}`
	for _, agent := range []string{"codex", "opencode", ""} {
		t.Run("agent="+agent, func(t *testing.T) {
			_, store := modelSwitchNotify(t, agent, input)
			if _, err := os.Lstat(store); !os.IsNotExist(err) {
				t.Errorf("agent %q wrote the model-switch store (err = %v)", agent, err)
			}
		})
	}
}

// TestClaudeHooksRegisterPostModelSwitchThroughTheNotifyWrapper covers the
// manifest AC: PostModelSwitch uses the same wrapper command and timeout as the
// other events, and the Codex manifest stays unchanged.
func TestClaudeHooksRegisterPostModelSwitchThroughTheNotifyWrapper(t *testing.T) {
	type manifest struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	load := func(path string) manifest {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m manifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return m
	}

	claude := load(filepath.Join("plugin", "hooks", "hooks.json"))
	groups := claude.Hooks["PostModelSwitch"]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("expected exactly one PostModelSwitch hook, got %+v", groups)
	}
	got, stop := groups[0].Hooks[0], claude.Hooks["Stop"][0].Hooks[0]
	if got.Type != "command" || got.Command != stop.Command || got.Timeout != 5 {
		t.Errorf("PostModelSwitch hook = %+v, want the Stop wrapper command %q with a 5s timeout", got, stop.Command)
	}
	if groups[0].Matcher != "" {
		t.Errorf("PostModelSwitch must not be filtered by a matcher, got %q", groups[0].Matcher)
	}

	if _, ok := load(filepath.Join("plugin", "codex", "hooks.json")).Hooks["PostModelSwitch"]; ok {
		t.Error("the Codex manifest must not register PostModelSwitch")
	}
}

// --- tmux_socket (#1429) ----------------------------------------------------

// notifyRawLineWithTMUX runs the real `cenci notify` binary against a plain unix
// listener and returns the raw JSON line it wrote, so a test can assert on the
// wire key itself. tmux nil removes TMUX from the child's environment; non-nil
// sets it to *tmux (an empty string is a set-but-empty TMUX). An ambient
// CENCI_TMUX_SOCKET is always removed, so only extraEnv can set it.
func notifyRawLineWithTMUX(t *testing.T, tmux *string, extraEnv ...string) string {
	t.Helper()
	setTempSocketDir(t)
	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "events.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	lines := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			lines <- ""
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		raw, _ := io.ReadAll(conn)
		lines <- string(raw)
	}()

	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "CENCI_TMUX_SOCKET=") {
			env = append(env, kv)
		}
	}
	env = append(env, "TMUX_PANE=%5", "CENCI_SANDBOX=")
	if tmux != nil {
		env = append(env, "TMUX="+*tmux)
	}
	env = append(env, extraEnv...)
	cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"sock-session"}`)
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case line := <-lines:
		if line == "" {
			t.Fatal("notify wrote nothing to the event socket")
		}
		return line
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the raw event line")
	}
	return ""
}

// The socket is the first comma-separated field of $TMUX (tmux's own
// "<socket>,<server pid>,<session id>" value), sent through the real binary.
func TestNotifySendsTmuxSocketFromFirstFieldOfTMUX(t *testing.T) {
	const socket = "/x/tmux-1/default"
	fx := newNotifyFixture(t)

	notifyRun(t, fx.socket, "claude", hookPayload(t, "Stop", "sock-session", nil), "TMUX_PANE=%5", "TMUX="+socket+",123,0")

	select {
	case ev := <-fx.receiver.Events():
		if ev.TmuxSocket != socket {
			t.Errorf("TmuxSocket = %q, want exactly %q (the first field of $TMUX)", ev.TmuxSocket, socket)
		}
		if ev.TmuxPane != "%5" {
			t.Errorf("TmuxPane = %q, want %q: the pane still travels with its socket", ev.TmuxPane, "%5")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
}

// The field is omitted, not sent empty, when $TMUX is unset or its first field is
// empty. The first subtest is the control that proves the wire key is sent at all,
// so the absences below cannot pass vacuously.
func TestNotifyOmitsTmuxSocketWhenTMUXUnsetOrEmptyField(t *testing.T) {
	t.Run("control: TMUX set puts the key on the wire", func(t *testing.T) {
		tmux := "/x/tmux-1/default,123,0"
		line := notifyRawLineWithTMUX(t, &tmux)
		if !strings.Contains(line, `"tmux_socket":"/x/tmux-1/default"`) {
			t.Errorf("raw line = %s, want a %q key", line, `"tmux_socket":"/x/tmux-1/default"`)
		}
	})

	// Q&A 13: a container session has CENCI_TMUX_SOCKET but no $TMUX, and notify
	// must not fall back to it; such sessions keep default-server behavior.
	t.Run("TMUX unset with CENCI_TMUX_SOCKET set does not fall back to it", func(t *testing.T) {
		const containerSocket = "/tmp/tmux-1000/default"
		line := notifyRawLineWithTMUX(t, nil, "CENCI_TMUX_SOCKET="+containerSocket)
		if strings.Contains(line, "tmux_socket") || strings.Contains(line, containerSocket) {
			t.Errorf("raw line = %s, want no tmux_socket key and no CENCI_TMUX_SOCKET value", line)
		}
		if !strings.Contains(line, `"tmux_pane":"%5"`) {
			t.Errorf("raw line = %s, want the event itself still delivered with its pane", line)
		}
	})

	empty, leadingComma := "", ",1,2"
	for _, tc := range []struct {
		name string
		tmux *string
	}{
		{"TMUX unset", nil},
		{"TMUX empty", &empty},
		{"TMUX with an empty first field", &leadingComma},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := notifyRawLineWithTMUX(t, tc.tmux)
			if strings.Contains(line, "tmux_socket") {
				t.Errorf("raw line = %s, want no tmux_socket key", line)
			}
			if !strings.Contains(line, `"tmux_pane":"%5"`) {
				t.Errorf("raw line = %s, want the event itself still delivered with its pane", line)
			}
		})
	}
}
