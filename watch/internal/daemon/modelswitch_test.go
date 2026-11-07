package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

const (
	msSonnet = "claude-sonnet-5-5"
	msOpus   = "claude-opus-5-5"
)

// isolateModelSwitchEnv keeps the daemon branch from ever touching ambient
// state: the daemon holds switches in memory only, but the shared isolation
// rule (watch/docs/test-isolation.md) applies to every new test.
func isolateModelSwitchEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
}

func modelSwitchEvent(session, from, to, source string) ipc.HookEvent {
	return ipc.HookEvent{
		EventType:         "PostModelSwitch",
		SessionID:         session,
		Agent:             "claude",
		FromModel:         from,
		ToModel:           to,
		ModelSwitchSource: source,
	}
}

func onlyWindow(t *testing.T, d *Daemon) ipc.WindowState {
	t.Helper()
	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(snap.Windows))
	}
	return snap.Windows[0]
}

func TestDaemon_ModelSwitchKeepsRunningSessionRunning(t *testing.T) {
	isolateModelSwitchEnv(t)
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", Agent: "claude"})

	d.handleEvent(modelSwitchEvent("s1", msSonnet, msOpus, "auto"))

	snap := d.buildSnapshot()
	w := onlyWindow(t, d)
	if w.Status != "running" {
		t.Errorf("status = %q, want running: a model switch must not change status", w.Status)
	}
	if snap.Summary.Running != 1 || snap.Summary.Total != 1 {
		t.Errorf("summary = %+v, want total=1 running=1", snap.Summary)
	}
	if len(w.ModelSwitches) != 1 {
		t.Fatalf("got %d switches, want 1: %+v", len(w.ModelSwitches), w.ModelSwitches)
	}
	got := w.ModelSwitches[0]
	if got.FromModel != msSonnet || got.ToModel != msOpus || got.Source != "auto" || !got.Fallback || got.Subagent {
		t.Errorf("switch = %+v", got)
	}
}

func TestDaemon_ModelSwitchLeavesEveryOtherStatusUntouched(t *testing.T) {
	isolateModelSwitchEnv(t)
	cases := []struct {
		name   string
		events []ipc.HookEvent
		want   string
	}{
		{"done stays done", []ipc.HookEvent{
			{EventType: "UserPromptSubmit", SessionID: "s1"},
			{EventType: "Stop", SessionID: "s1"},
		}, "done"},
		{"need-input stays need-input", []ipc.HookEvent{
			{EventType: "UserPromptSubmit", SessionID: "s1"},
			{EventType: "PermissionRequest", SessionID: "s1"},
		}, "need-input"},
		{"idle stays idle", []ipc.HookEvent{
			{EventType: "SessionStart", SessionID: "s1"},
		}, "idle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDaemon(&tmuxtest.MockClient{})
			for _, e := range tc.events {
				d.handleEvent(e)
			}
			d.handleEvent(modelSwitchEvent("s1", msSonnet, msOpus, "auto"))
			w := onlyWindow(t, d)
			if w.Status != tc.want {
				t.Errorf("status = %q, want %q", w.Status, tc.want)
			}
			if len(w.ModelSwitches) != 1 {
				t.Errorf("switch not recorded: %+v", w.ModelSwitches)
			}
		})
	}
}

func TestDaemon_ModelSwitchNeverReachesTheFrontend(t *testing.T) {
	isolateModelSwitchEnv(t)
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", Agent: "claude"})
	renames, opts := len(mc.Renames), len(mc.WindowOpts)

	d.handleEvent(modelSwitchEvent("s1", msSonnet, msOpus, "auto"))

	if len(mc.Renames) != renames || len(mc.WindowOpts) != opts {
		t.Errorf("a model switch must not drive tmux: renames %d->%d, window options %d->%d",
			renames, len(mc.Renames), opts, len(mc.WindowOpts))
	}
}

func TestDaemon_SubagentSwitchIsRecordedUnderTheParentWithoutChangingItsStatus(t *testing.T) {
	isolateModelSwitchEnv(t)
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "parent", Agent: "claude"})

	ev := modelSwitchEvent("parent", msOpus, msSonnet, "sdk")
	ev.AgentID = "sub1"
	ev.AgentType = "cenci:reviewer"
	d.handleEvent(ev)

	if len(d.sessions) != 1 {
		t.Fatalf("a subagent switch must not create its own session, got %d sessions", len(d.sessions))
	}
	w := onlyWindow(t, d)
	if w.Status != "running" {
		t.Errorf("parent status = %q, want running", w.Status)
	}
	if len(w.ModelSwitches) != 1 {
		t.Fatalf("got %d switches, want 1", len(w.ModelSwitches))
	}
	got := w.ModelSwitches[0]
	if !got.Subagent || got.AgentType != "cenci:reviewer" || got.Fallback {
		t.Errorf("switch = %+v, want subagent cenci:reviewer, not a fallback (source sdk)", got)
	}
}

func TestDaemon_ModelSwitchesAreCappedWithAnOverflowCount(t *testing.T) {
	isolateModelSwitchEnv(t)
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", Agent: "claude"})

	for i := 0; i < 10; i++ {
		d.handleEvent(modelSwitchEvent("s1", fmt.Sprintf("model-%d", i), fmt.Sprintf("model-%d", i+1), "auto"))
	}

	w := onlyWindow(t, d)
	if len(w.ModelSwitches) != 8 {
		t.Fatalf("kept %d switches, want 8", len(w.ModelSwitches))
	}
	if w.ModelSwitchesMore != 2 {
		t.Errorf("more = %d, want 2", w.ModelSwitchesMore)
	}
	for i, sw := range w.ModelSwitches {
		if want := fmt.Sprintf("model-%d", i); sw.FromModel != want {
			t.Errorf("switch %d from = %q, want %q: order must be kept", i, sw.FromModel, want)
		}
	}
}

func TestDaemon_InvalidModelSwitchStringsNeverReachTheSnapshot(t *testing.T) {
	isolateModelSwitchEnv(t)
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", Agent: "claude"})

	// The event socket receives events from containers, so the daemon cannot
	// trust that cenci notify already sanitized these.
	rawFrom := "claude\x1b[31m-evil\nmodel"
	rawTo := strings.Repeat("x", 129)
	rawType := "evil\x1b]0;title\x07type"
	rawSource := "auto\x1b[0m"
	ev := modelSwitchEvent("s1", rawFrom, rawTo, rawSource)
	ev.AgentID = "sub1"
	ev.AgentType = rawType
	d.handleEvent(ev)

	w := onlyWindow(t, d)
	encoded, err := json.Marshal(d.buildSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{rawFrom, rawTo, rawType, rawSource, "\x1b", "\\u001b"} {
		if strings.Contains(string(encoded), raw) {
			t.Errorf("raw value %q reached the snapshot: %s", raw, encoded)
		}
	}
	if len(w.ModelSwitches) != 1 {
		t.Fatalf("an invalid switch must still be recorded, got %+v", w.ModelSwitches)
	}
	got := w.ModelSwitches[0]
	if got.FromModel != "(invalid model id)" || got.ToModel != "(invalid model id)" {
		t.Errorf("models = %q -> %q, want placeholders", got.FromModel, got.ToModel)
	}
	if got.AgentType != "unknown" {
		t.Errorf("agent type = %q, want unknown", got.AgentType)
	}
	if got.Fallback {
		t.Error("an invalid source must not be labelled fallback")
	}
}

func TestDaemon_OnlyAutoSourceIsLabelledFallback(t *testing.T) {
	isolateModelSwitchEnv(t)
	for source, want := range map[string]bool{"auto": true, "sdk": false, "command": false, "": false, "AUTO": false} {
		t.Run("source="+source, func(t *testing.T) {
			d := newTestDaemon(&tmuxtest.MockClient{})
			d.handleEvent(modelSwitchEvent("s1", msSonnet, msOpus, source))
			w := onlyWindow(t, d)
			if len(w.ModelSwitches) != 1 || w.ModelSwitches[0].Fallback != want {
				t.Errorf("fallback = %+v, want %v", w.ModelSwitches, want)
			}
		})
	}
}

func TestDaemon_ModelSwitchesGoWhenTheSessionEnds(t *testing.T) {
	isolateModelSwitchEnv(t)
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", Agent: "claude"})
	d.handleEvent(modelSwitchEvent("s1", msSonnet, msOpus, "auto"))

	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "s1"})

	if snap := d.buildSnapshot(); len(snap.Windows) != 0 {
		t.Errorf("session end must drop the in-memory switches with the session, got %+v", snap.Windows)
	}
}
