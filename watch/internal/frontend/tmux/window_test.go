package tmux

import (
	"fmt"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	tmuxc "github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

const (
	sockA = "/run/user/1000/tmux-1000/a"
	sockB = "/run/user/1000/tmux-1000/b"
	sockC = "/run/user/1000/tmux-1000/c"
)

// socketPane is one pane of a mock server: a Claude pane titled with a running
// spinner and the given task.
func socketPane(window, index, id, task string) tmuxc.PaneInfo {
	return tmuxc.PaneInfo{
		SessionName: "main", WindowIndex: index, WindowName: window, PaneIndex: "0",
		PaneCurrentCmd: "claude", PaneTitle: "⠋ " + task, PaneID: id,
	}
}

// twoSocketMock has the same pane id %5 at main:0 on two different servers, each
// running a different task: the #646 collision.
func twoSocketMock() *tmuxtest.MockClient {
	return &tmuxtest.MockClient{PanesBySocket: map[string][]tmuxc.PaneInfo{
		sockA: {socketPane("bash", "0", "%5", "alpha task")},
		sockB: {socketPane("bash", "0", "%5", "beta task")},
	}}
}

// runningOn is a running session whose pane lives on socket.
func runningOn(id, pane, socket string) *frontend.SessionState {
	return &frontend.SessionState{SessionID: id, TmuxPane: pane, TmuxSocket: socket, Agent: "claude", Status: detect.StatusRunning}
}

func touch(f *Frontend, sess *frontend.SessionState) frontend.Observations {
	return f.OnEvent(sess, ipc.HookEvent{EventType: "PreToolUse", SessionID: sess.SessionID})
}

// call is one tmux.Client call as the frontend made it.
type call struct {
	Method, Socket, Target, Key, Value string
}

// recordingClient logs every call, in order, across all five Client methods
// before delegating to the mock.
type recordingClient struct {
	*tmuxtest.MockClient
	calls []call
}

func (r *recordingClient) ListPanes(socket string) ([]tmuxc.PaneInfo, error) {
	r.calls = append(r.calls, call{Method: "ListPanes", Socket: socket})
	return r.MockClient.ListPanes(socket)
}

func (r *recordingClient) RenameWindow(socket, target, name string) error {
	r.calls = append(r.calls, call{Method: "RenameWindow", Socket: socket, Target: target, Value: name})
	return r.MockClient.RenameWindow(socket, target, name)
}

func (r *recordingClient) SetWindowOption(socket, target, key, value string) error {
	r.calls = append(r.calls, call{Method: "SetWindowOption", Socket: socket, Target: target, Key: key, Value: value})
	return r.MockClient.SetWindowOption(socket, target, key, value)
}

func (r *recordingClient) GetWindowOption(socket, target, key string) (string, error) {
	r.calls = append(r.calls, call{Method: "GetWindowOption", Socket: socket, Target: target, Key: key})
	return r.MockClient.GetWindowOption(socket, target, key)
}

func (r *recordingClient) SetOption(socket, key, value string) error {
	r.calls = append(r.calls, call{Method: "SetOption", Socket: socket, Key: key, Value: value})
	return r.MockClient.SetOption(socket, key, value)
}

// goldenEmptySocketCalls is the exact call sequence the frontend made on the
// ambient server before sockets were threaded through it (#1429), pinned from
// that code before any frontend edit. An empty socket means bare tmux with
// today's argv, so a window tracked before an upgrade must see these calls and
// no others, in this order.
var goldenEmptySocketCalls = []call{
	{Method: "ListPanes", Socket: "", Target: "", Key: "", Value: ""},
	{Method: "GetWindowOption", Socket: "", Target: "main:0", Key: "automatic-rename", Value: ""},
	{Method: "GetWindowOption", Socket: "", Target: "main:0", Key: "window-status-style", Value: ""},
	{Method: "GetWindowOption", Socket: "", Target: "main:0", Key: "window-status-current-style", Value: ""},
	{Method: "GetWindowOption", Socket: "", Target: "main:0", Key: "window-status-format", Value: ""},
	{Method: "GetWindowOption", Socket: "", Target: "main:0", Key: "window-status-current-format", Value: ""},
	{Method: "GetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-repo-dir", Value: ""},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "automatic-rename", Value: "off"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-format", Value: "#{@cenci-symbol} #I:#W"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-current-format", Value: "#{@cenci-symbol} #I:#W*"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-style", Value: "fg=blue,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-current-style", Value: "fg=blue,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-style", Value: "fg=blue,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-symbol", Value: "▶"},
	{Method: "RenameWindow", Socket: "", Target: "main:0", Key: "", Value: "writing tests"},
	{Method: "ListPanes", Socket: "", Target: "", Key: "", Value: ""},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-style", Value: "fg=green,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-current-style", Value: "fg=green,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-style", Value: "fg=green,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-symbol", Value: "✓"},
	{Method: "RenameWindow", Socket: "", Target: "main:0", Key: "", Value: "writing tests"},
	{Method: "ListPanes", Socket: "", Target: "", Key: "", Value: ""},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-style", Value: "fg=blue,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-current-style", Value: "fg=blue,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-style", Value: "fg=blue,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-symbol", Value: "▶"},
	{Method: "RenameWindow", Socket: "", Target: "main:0", Key: "", Value: "writing tests"},
	{Method: "ListPanes", Socket: "", Target: "", Key: "", Value: ""},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-style", Value: "fg=yellow,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "window-status-current-style", Value: "fg=yellow,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-style", Value: "fg=yellow,dim"},
	{Method: "SetWindowOption", Socket: "", Target: "main:0", Key: "@cenci-symbol", Value: "⏹"},
	{Method: "RenameWindow", Socket: "", Target: "main:0", Key: "", Value: "writing tests"},
	{Method: "ListPanes", Socket: "", Target: "", Key: "", Value: ""},
	{Method: "RenameWindow", Socket: "", Target: "%0", Key: "", Value: "bash"},
	{Method: "SetWindowOption", Socket: "", Target: "%0", Key: "window-status-style", Value: ""},
	{Method: "SetWindowOption", Socket: "", Target: "%0", Key: "window-status-current-style", Value: ""},
	{Method: "SetWindowOption", Socket: "", Target: "%0", Key: "window-status-format", Value: "#I:#W"},
	{Method: "SetWindowOption", Socket: "", Target: "%0", Key: "window-status-current-format", Value: "#I:#W*"},
	{Method: "SetWindowOption", Socket: "", Target: "%0", Key: "@cenci-style", Value: ""},
	{Method: "SetWindowOption", Socket: "", Target: "%0", Key: "@cenci-symbol", Value: ""},
	{Method: "SetWindowOption", Socket: "", Target: "%0", Key: "automatic-rename", Value: "on"},
}

// TestEmptySocketWindowKeepsTodaysCallSequence is P26: a window with an empty
// socket is tracked, restyled on a status change, idle-title swept and restored
// on pane-gone through exactly the pinned (method, socket, target, key, value)
// sequence. Together with internal/tmux's TestSocketCalls_EmptySocketKeepsArgvAndAmbientEnv
// (an empty socket is bare tmux with the ambient env), the argv is identical.
func TestEmptySocketWindowKeepsTodaysCallSequence(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmuxc.PaneInfo{socketPane("bash", "0", "%0", "writing tests")},
		WindowOptValues: map[string]string{
			"main:0:window-status-format":         "#I:#W",
			"main:0:window-status-current-format": "#I:#W*",
		},
	}
	rc := &recordingClient{MockClient: mc}
	f := New(config.Default(), rc)
	sess := &frontend.SessionState{SessionID: "s1", TmuxPane: "%0", Agent: "claude", Status: detect.StatusRunning}
	sessions := map[string]*frontend.SessionState{"s1": sess}

	// Track the window and mark it running.
	touch(f, sess)
	// A status change.
	sess.Status = detect.StatusDone
	f.OnEvent(sess, ipc.HookEvent{EventType: "Stop", SessionID: "s1"})
	// Idle-title sweep: running again, then the pane title reverts to an idle marker.
	sess.Status = detect.StatusRunning
	touch(f, sess)
	mc.Panes[0].PaneTitle = "✳ writing tests"
	f.Sweep(sessions)
	// Pane gone: the sweep restores the window.
	mc.Panes = nil
	f.Sweep(sessions)

	if len(rc.calls) != len(goldenEmptySocketCalls) {
		t.Errorf("made %d calls, want %d", len(rc.calls), len(goldenEmptySocketCalls))
	}
	for i := 0; i < len(rc.calls) && i < len(goldenEmptySocketCalls); i++ {
		if rc.calls[i] != goldenEmptySocketCalls[i] {
			t.Errorf("call %d = %+v, want %+v", i, rc.calls[i], goldenEmptySocketCalls[i])
		}
	}
	if t.Failed() {
		var b strings.Builder
		for _, c := range rc.calls {
			fmt.Fprintf(&b, "\t{Method: %q, Socket: %q, Target: %q, Key: %q, Value: %q},\n", c.Method, c.Socket, c.Target, c.Key, c.Value)
		}
		t.Logf("actual call sequence:\n%s", b.String())
	}
}

// P25 at the frontend: the same pane id on two servers is two windows, each
// renamed and styled on its own server, and ending one session restores only
// its own window.
func TestSamePaneIDOnTwoSocketsTracksTwoWindows(t *testing.T) {
	mc := twoSocketMock()
	cfg := config.Default()
	f := New(cfg, mc)
	a, b := runningOn("a", "%5", sockA), runningOn("b", "%5", sockB)

	touch(f, a)
	touch(f, b)

	wiA, wiB := f.WindowInfo("a"), f.WindowInfo("b")
	if wiA == nil || wiB == nil {
		t.Fatalf("WindowInfo a=%+v b=%+v, want both windows tracked", wiA, wiB)
	}
	if wiA.Socket != sockA || wiB.Socket != sockB {
		t.Errorf("sockets = %q and %q, want %q and %q", wiA.Socket, wiB.Socket, sockA, sockB)
	}
	if wiA.PaneID != "%5" || wiB.PaneID != "%5" {
		t.Errorf("pane ids = %q and %q, want %%5 on both", wiA.PaneID, wiB.PaneID)
	}
	for socket, task := range map[string]string{sockA: "alpha task", sockB: "beta task"} {
		if got, ok := tmuxtest.LastSocketRename(mc.Renames, socket, "main:0"); !ok || got != task {
			t.Errorf("rename on %s = (%q, %v), want %q", socket, got, ok, task)
		}
		for key, want := range map[string]string{"@cenci-symbol": cfg.SymbolRunning, "@cenci-style": cfg.StyleRunning} {
			if got, ok := tmuxtest.FindSocketWindowOpt(mc.WindowOpts, socket, "main:0", key); !ok || got != want {
				t.Errorf("%s on %s = (%q, %v), want %q", key, socket, got, ok, want)
			}
		}
	}
	for _, s := range mc.Sockets {
		if s != sockA && s != sockB {
			t.Fatalf("a call went to socket %q, want only %q or %q", s, sockA, sockB)
		}
	}

	renames, opts := len(mc.Renames), len(mc.WindowOpts)
	f.OnSessionEnd(a)

	if len(mc.Renames) == renames || len(mc.WindowOpts) == opts {
		t.Fatalf("ending A restored nothing: renames %d→%d, window options %d→%d", renames, len(mc.Renames), opts, len(mc.WindowOpts))
	}
	for _, r := range mc.Renames[renames:] {
		if r.Socket != sockA {
			t.Errorf("restore rename %+v went to %q, want only %q", r, r.Socket, sockA)
		}
	}
	for _, o := range mc.WindowOpts[opts:] {
		if o.Socket != sockA {
			t.Errorf("restore option %+v went to %q, want only %q", o, o.Socket, sockA)
		}
	}
	if got := mc.PanesBySocket[sockB][0].WindowName; got != "beta task" {
		t.Errorf("B's window name = %q after ending A, want it untouched", got)
	}
	if f.WindowInfo("a") != nil || f.WindowInfo("b") == nil {
		t.Errorf("after ending A: a=%+v b=%+v, want only B still tracked", f.WindowInfo("a"), f.WindowInfo("b"))
	}
}

// A /clear-style handoff lists panes on the session's own server. The window is
// on A, and the ambient server has no pane %5: listing the wrong server would
// make the pane look gone and tear the window down (restore calls, tracking
// dropped) instead of keeping it styled for the successor.
func TestOnSessionHandoffListsPanesOnTheSessionsSocket(t *testing.T) {
	mc := &tmuxtest.MockClient{PanesBySocket: map[string][]tmuxc.PaneInfo{
		sockA: {socketPane("bash", "0", "%5", "alpha task")},
	}}
	f := New(config.Default(), mc)
	a := runningOn("a", "%5", sockA)
	touch(f, a)
	if f.WindowInfo("a") == nil {
		t.Fatal("setup: the window on A was not tracked")
	}

	renames, opts := len(mc.Renames), len(mc.WindowOpts)
	mc.ListSockets = nil
	f.OnSessionHandoff(a)

	if len(mc.Renames) != renames || len(mc.WindowOpts) != opts {
		t.Errorf("handoff restored the window: renames %d→%d, window options %d→%d, want none", renames, len(mc.Renames), opts, len(mc.WindowOpts))
	}
	for _, s := range mc.ListSockets {
		if s != sockA {
			t.Errorf("handoff listed panes on %q, want only %q", s, sockA)
		}
	}
	if f.WindowInfo("a") != nil {
		t.Errorf("WindowInfo(a) = %+v, want the ended session's claim released", f.WindowInfo("a"))
	}
	successor := runningOn("a2", "%5", sockA)
	touch(f, successor)
	wi := f.WindowInfo("a2")
	if wi == nil || wi.Socket != sockA || wi.PaneID != "%5" {
		t.Errorf("WindowInfo(a2) = %+v, want the same window on %q taken over by the successor", wi, sockA)
	}
	if got := mc.PanesBySocket[sockA][0].WindowName; got != "alpha task" {
		t.Errorf("A's window name = %q, want it still styled for the successor, not restored", got)
	}
}

// A session whose pane is already gone from its own server restores its cached
// window there, and only there. The ambient server has a different pane at the
// same session:index, which would read as a renumber survivor and skip the
// restore if the listing went to the wrong server.
func TestOnSessionEndPaneGoneRestoresOnTheSessionsSocketOnly(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmuxc.PaneInfo{socketPane("bash", "0", "%9", "ambient task")},
		PanesBySocket: map[string][]tmuxc.PaneInfo{
			sockA: {socketPane("bash", "0", "%5", "alpha task")},
		},
	}
	f := New(config.Default(), mc)
	a := runningOn("a", "%5", sockA)
	touch(f, a)

	mc.PanesBySocket[sockA] = nil // %5 closed on A
	renames, opts := len(mc.Renames), len(mc.WindowOpts)
	f.OnSessionEnd(a)

	if len(mc.Renames) == renames || len(mc.WindowOpts) == opts {
		t.Fatalf("ending a pane-gone session restored nothing: renames %d→%d, window options %d→%d", renames, len(mc.Renames), opts, len(mc.WindowOpts))
	}
	for _, r := range mc.Renames[renames:] {
		if r.Socket != sockA {
			t.Errorf("restore rename %+v went to %q, want only %q", r, r.Socket, sockA)
		}
	}
	for _, o := range mc.WindowOpts[opts:] {
		if o.Socket != sockA {
			t.Errorf("restore option %+v went to %q, want only %q", o, o.Socket, sockA)
		}
	}
	if got := mc.Panes[0].WindowName; got != "bash" {
		t.Errorf("ambient window name = %q, want it untouched", got)
	}
	if f.WindowInfo("a") != nil {
		t.Errorf("WindowInfo(a) = %+v, want the window forgotten after the restore", f.WindowInfo("a"))
	}
}

// A pane that renumbers on one server migrates only within that server: another
// server's window at the destination index, or carrying the same pane id, is a
// different window and keeps its state.
func TestRenumberMigrationIsScopedToItsSocket(t *testing.T) {
	t.Run("event", func(t *testing.T) {
		mc := &tmuxtest.MockClient{PanesBySocket: map[string][]tmuxc.PaneInfo{
			sockA: {socketPane("bash", "1", "%5", "alpha task")},
			sockB: {socketPane("bash", "0", "%7", "beta task")},
		}}
		f := New(config.Default(), mc)
		a, b := runningOn("a", "%5", sockA), runningOn("b", "%7", sockB)
		touch(f, a)
		touch(f, b)

		// A's pane renumbers into the index B's window occupies on its own server.
		mc.PanesBySocket[sockA][0].WindowIndex = "0"
		touch(f, a)

		wiA, wiB := f.WindowInfo("a"), f.WindowInfo("b")
		if wiA == nil || wiA.WindowIndex != "0" {
			t.Errorf("A after renumber = %+v, want it migrated to window index 0", wiA)
		}
		if wiB == nil || wiB.PaneID != "%7" || wiB.WindowIndex != "0" || wiB.Socket != sockB {
			t.Errorf("B after A's renumber = %+v, want it untouched at main:0 on %s", wiB, sockB)
		}
	})

	t.Run("same pane id on another socket is not a renumber", func(t *testing.T) {
		mc := &tmuxtest.MockClient{PanesBySocket: map[string][]tmuxc.PaneInfo{
			sockA: {socketPane("bash", "1", "%5", "alpha task")},
			sockB: {socketPane("bash", "0", "%5", "beta task")},
		}}
		f := New(config.Default(), mc)
		a, b := runningOn("a", "%5", sockA), runningOn("b", "%5", sockB)
		touch(f, a)
		touch(f, b)

		wiA, wiB := f.WindowInfo("a"), f.WindowInfo("b")
		if wiA == nil || wiA.WindowIndex != "1" {
			t.Errorf("A = %+v, want it still at window index 1: pane %%5 on B is a different pane", wiA)
		}
		if wiB == nil || wiB.WindowIndex != "0" {
			t.Errorf("B = %+v, want it tracked at window index 0", wiB)
		}
		if actions := f.Sweep(map[string]*frontend.SessionState{"a": a, "b": b}); len(actions) != 0 {
			t.Errorf("sweep actions = %+v, want none: nothing renumbered or went away", actions)
		}
		if wiA := f.WindowInfo("a"); wiA == nil || wiA.WindowIndex != "1" {
			t.Errorf("A after the sweep = %+v, want it still at window index 1", wiA)
		}
	})
}

// A mid-session rename by the user is detected per window: renaming the window on
// one server marks only that window manually named.
func TestManualRenameDetectionIsPerSocket(t *testing.T) {
	mc := twoSocketMock()
	f := New(config.Default(), mc)
	a, b := runningOn("a", "%5", sockA), runningOn("b", "%5", sockB)
	touch(f, a)
	touch(f, b)

	mc.PanesBySocket[sockA][0].WindowName = "my work"
	touch(f, a)
	touch(f, b)

	wiA, wiB := f.WindowInfo("a"), f.WindowInfo("b")
	if wiA == nil || wiB == nil {
		t.Fatalf("WindowInfo a=%+v b=%+v, want both tracked", wiA, wiB)
	}
	if !wiA.ManuallyNamed {
		t.Error("A was renamed by the user, want it marked manually named")
	}
	if wiB.ManuallyNamed {
		t.Error("B was never renamed by the user, want it not marked manually named")
	}
}

// TestInferAgentOpenCode pins inferAgent's OpenCode alias list (#488 Q&A #5).
// OpenCode's own pane_current_command is unknown/undocumented, so a
// conservative default is used: "opencode" itself plus the JS/Bun runtimes it
// may transiently report ("bun", "node"). Aliasing these to "opencode" (not
// leaving them unrecognized) is the safety property the exit-restore sweep
// depends on — an ambiguous runtime name must read as "still opencode", never
// as "opencode exited", mirroring the existing Claude npm/node-shim guard in
// frontend.go.
func TestInferAgentOpenCode(t *testing.T) {
	cases := map[string]string{
		"opencode":     "opencode",
		"OpenCode":     "opencode",
		"  opencode  ": "opencode",
		"bun":          "opencode",
		"node":         "opencode",
	}
	for cmd, want := range cases {
		if got := inferAgent(cmd); got != want {
			t.Errorf("inferAgent(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// TestInferAgentUnrecognizedCommandStaysEmpty guards the narrow-exclusion
// convention (watch/AGENTS.md): adding OpenCode's alias list must not turn
// inferAgent into a silent "anything unknown -> some agent" catch-all. A
// genuinely unrelated command (not opencode/bun/node, not claude/codex) must
// still resolve to "" (unknown), same as before this ticket.
func TestInferAgentUnrecognizedCommandStaysEmpty(t *testing.T) {
	for _, cmd := range []string{"vim", "python3", "htop", ""} {
		if got := inferAgent(cmd); got != "" {
			t.Errorf("inferAgent(%q) = %q, want \"\" (unrecognized)", cmd, got)
		}
	}
}

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string under limit", "hello", 50, "hello"},
		{"exactly at limit", strings.Repeat("a", 50), 50, strings.Repeat("a", 50)},
		{"over limit truncated with ellipsis", strings.Repeat("b", 60), 50, strings.Repeat("b", 50) + "..."},
		{"empty string unchanged", "", 50, ""},
		{"multi-byte unicode truncates at rune boundary", strings.Repeat("世", 60), 50, strings.Repeat("世", 50) + "..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateForLog(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateForLog(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}
