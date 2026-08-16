package daemon

import (
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux/tmuxtest"
)

// heldPaneDaemon wires a daemon whose single session has just been held at
// running by a main-agent Stop reporting in-flight background work (#698).
func heldPaneDaemon(t *testing.T, paneTitle string) (*Daemon, *tmuxtest.MockClient) {
	t.Helper()
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: paneTitle, PaneID: "%0"},
		},
	}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0", BackgroundWork: true})

	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning || !sess.BackgroundHold {
		t.Fatalf("precondition: expected a held running session, got %+v", sess)
	}
	return d, mc
}

// TestDaemon_SweepReleasesExpiredBackgroundHoldToDone covers #1079: the hold
// #698 puts on a finished turn assumes the reported background work will wake
// the session, and that is false for work that never re-invokes the agent — a
// backgrounded server, a paused task, a far-future wakeup. With no expiry the
// window sat at running until the user's next prompt. Once hook events have
// been quiet past BackgroundHoldTTL the session is released to done, not
// stopped: the main agent's Stop did fire, so the agent really is parked at
// the prompt.
func TestDaemon_SweepReleasesExpiredBackgroundHoldToDone(t *testing.T) {
	d, mc := heldPaneDaemon(t, "⠋ delegating work")

	// Parked at the prompt: idle-marker title, hooks quiet past the TTL.
	mc.Panes[0].PaneTitle = "✳ delegating work"
	sess := d.sessions["sess1"]
	sess.LastEvent = time.Now().Add(-frontend.BackgroundHoldTTL - time.Second)
	mc.WindowOpts = nil

	d.runSweep()

	if sess.Status != detect.StatusDone {
		t.Errorf("expected StatusDone after the background hold expired, got %v", sess.Status)
	}
	if sess.BackgroundHold {
		t.Error("expected BackgroundHold cleared once the hold was released")
	}
	sym, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol")
	if !ok {
		t.Fatal("expected the released window to be restyled")
	}
	if sym != d.cfg.SymbolDone {
		t.Errorf("expected done symbol %q on the released window, got %q", d.cfg.SymbolDone, sym)
	}
}

// TestDaemon_SweepReleasesExpiredBackgroundHoldRegardlessOfTitle pins the
// release to event quiescence, not to the pane title. A held session whose
// title still shows a stale working marker is exactly the stuck case users
// report — Stop has fired, so no marker in the title can mean the main agent
// is still generating.
func TestDaemon_SweepReleasesExpiredBackgroundHoldRegardlessOfTitle(t *testing.T) {
	d, mc := heldPaneDaemon(t, "⠋ delegating work")

	sess := d.sessions["sess1"]
	sess.LastEvent = time.Now().Add(-frontend.BackgroundHoldTTL - time.Second)
	mc.WindowOpts = nil

	d.runSweep()

	if sess.Status != detect.StatusDone {
		t.Errorf("expected StatusDone with a working-marker title, got %v", sess.Status)
	}
}

// TestDaemon_SweepKeepsFreshBackgroundHoldRunning keeps #698/#706 intact: a
// hold well inside the TTL still outranks the idle-title backstop, so genuine
// in-flight background work is never cut short.
func TestDaemon_SweepKeepsFreshBackgroundHoldRunning(t *testing.T) {
	d, mc := heldPaneDaemon(t, "⠋ delegating work")

	mc.Panes[0].PaneTitle = "✳ delegating work"
	sess := d.sessions["sess1"]
	sess.LastEvent = time.Now().Add(-frontend.BackgroundHoldTTL / 2)

	d.runSweep()

	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning while the hold is still fresh, got %v", sess.Status)
	}
	if !sess.BackgroundHold {
		t.Error("expected BackgroundHold to survive a sweep inside the TTL")
	}
}

// TestDaemon_SubagentEventRearmsBackgroundHold pins the TTL to quiet rather
// than to hold age: the backgrounded work's own events land on the session key
// and refresh LastEvent, so work that is genuinely in flight keeps the hold
// alive indefinitely and only a phantom hold expires.
func TestDaemon_SubagentEventRearmsBackgroundHold(t *testing.T) {
	d, mc := heldPaneDaemon(t, "⠋ delegating work")

	sess := d.sessions["sess1"]
	sess.LastEvent = time.Now().Add(-frontend.BackgroundHoldTTL - time.Second)

	// The backgrounded subagent is still working: its tool event re-arms the
	// hold window even though it may not clear the hold itself (#706).
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash", AgentID: "sub1"})
	mc.Panes[0].PaneTitle = "✳ delegating work"

	d.runSweep()

	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning after the subagent re-armed the hold, got %v", sess.Status)
	}
	if !sess.BackgroundHold {
		t.Error("expected BackgroundHold to survive a subagent event")
	}
}

// TestDaemon_PanelessSessionReleasesExpiredBackgroundHold covers the sessions
// the tmux sweep can never reach — a sandboxed or plain-terminal agent with no
// pane. They are shown by `cenci status` and the read-only widgets exactly
// like tmux-backed ones, so the release lives in the daemon core too.
func TestDaemon_PanelessSessionReleasesExpiredBackgroundHold(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", BackgroundWork: true})

	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning || !sess.BackgroundHold {
		t.Fatalf("precondition: expected a held running paneless session, got %+v", sess)
	}

	sess.LastEvent = time.Now().Add(-frontend.BackgroundHoldTTL - time.Second)
	d.runSweep()

	if sess.Status != detect.StatusDone {
		t.Errorf("expected StatusDone for the expired paneless hold, got %v", sess.Status)
	}
	if sess.BackgroundHold {
		t.Error("expected BackgroundHold cleared on the paneless release")
	}
	if len(mc.Renames) != 0 || len(mc.WindowOpts) != 0 {
		t.Errorf("expected zero tmux calls for a paneless release, got %d renames, %d opts", len(mc.Renames), len(mc.WindowOpts))
	}
}

// TestDaemon_PanelessSessionKeepsFreshBackgroundHold is the paneless mirror of
// the fresh-hold case: the core release must not fire early either.
func TestDaemon_PanelessSessionKeepsFreshBackgroundHold(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", BackgroundWork: true})

	sess := d.sessions["sess1"]
	sess.LastEvent = time.Now().Add(-frontend.BackgroundHoldTTL / 2)

	d.runSweep()

	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning while the paneless hold is fresh, got %v", sess.Status)
	}
}

// TestDaemon_ReleasedBackgroundHoldReturnsToRunningOnWake pins the release as
// self-correcting in both directions: if the background work does eventually
// wake the session, its next main-agent event moves the window straight back
// to running.
func TestDaemon_ReleasedBackgroundHoldReturnsToRunningOnWake(t *testing.T) {
	d, mc := heldPaneDaemon(t, "⠋ delegating work")

	sess := d.sessions["sess1"]
	sess.LastEvent = time.Now().Add(-frontend.BackgroundHoldTTL - time.Second)
	d.runSweep()
	if sess.Status != detect.StatusDone {
		t.Fatalf("precondition: expected StatusDone after release, got %v", sess.Status)
	}

	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})

	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning after the background work woke the session, got %v", sess.Status)
	}
	if sym, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || sym != d.cfg.SymbolRunning {
		t.Errorf("expected running symbol %q on the woken window, got %q (set=%v)", d.cfg.SymbolRunning, sym, ok)
	}
}

// TestDaemon_QuietRunningSessionWithoutHoldStillStops keeps the ESC backstop
// unchanged for the sessions the release does not own: no hold means the
// idle-marker title still resolves to stopped after titleStopQuiescence, not
// to done.
func TestDaemon_QuietRunningSessionWithoutHoldStillStops(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	mc.Panes[0].PaneTitle = "✳ writing tests"
	sess := d.sessions["sess1"]
	sess.LastEvent = time.Now().Add(-frontend.BackgroundHoldTTL - time.Second)

	d.runSweep()

	if sess.Status != detect.StatusStopped {
		t.Errorf("expected StatusStopped for a quiet unheld session, got %v", sess.Status)
	}
}
