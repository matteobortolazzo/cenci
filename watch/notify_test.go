package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
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

	pluginRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginRoot, "bin"), 0o755); err != nil {
		t.Fatalf("create plugin bin dir: %v", err)
	}
	stub := filepath.Join(pluginRoot, "bin", "cenci")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatalf("write hook binary stub: %v", err)
	}

	cmd := exec.Command("sh", "-c", stopGroups[0].Hooks[0].Command)
	cmd.Env = append(os.Environ(), "PLUGIN_ROOT="+pluginRoot)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"test"}`)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run Codex Stop hook: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Codex Stop hook output must be JSON, got %q: %v", output, err)
	}
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
// The temp PLUGIN_ROOT holds a copy of the real codex/notify.sh plus a
// bin/cenci shim that execs the test-built binary with -event-socket pointing
// at the temp socket, so the default daemon socket is never touched and no
// daemon is autostarted. The hook command swallows failures (`|| true`), so
// receiving the event on the temp socket is also the proof that the shim — and
// therefore the wrapper — actually ran.
func runCodexManifestHook(t *testing.T, event, payload string) (stdout string, runErr error, got ipc.HookEvent) {
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

	pluginRoot := t.TempDir()
	wrapper, err := os.ReadFile(filepath.Join("plugin", "codex", "notify.sh"))
	if err != nil {
		t.Fatalf("read codex/notify.sh: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pluginRoot, "codex"), 0o755); err != nil {
		t.Fatalf("create plugin codex dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "codex", "notify.sh"), wrapper, 0o755); err != nil {
		t.Fatalf("write notify.sh copy: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pluginRoot, "bin"), 0o755); err != nil {
		t.Fatalf("create plugin bin dir: %v", err)
	}
	shim := "#!/bin/sh\nexec \"$CENCI_TEST_BIN\" \"$@\" -event-socket \"$CENCI_TEST_SOCKET\"\n"
	if err := os.WriteFile(filepath.Join(pluginRoot, "bin", "cenci"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write cenci shim: %v", err)
	}

	cmd := exec.Command("sh", "-c", hooks[0].Command)
	cmd.Env = append(os.Environ(),
		"PLUGIN_ROOT="+pluginRoot,
		"CENCI_TEST_BIN="+binaryPath,
		"CENCI_TEST_SOCKET="+socket,
		"CENCI_SANDBOX=",
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
	return out.String(), runErr, got
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

			stdout, runErr, got := runCodexManifestHook(t, tc.event, tc.payload)
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
	_, runErr, got := runCodexManifestHook(t, "Interrupt", `{"hook_event_name":"Interrupt","session_id":"codex-sess","agent_id":"child-1"}`)
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
