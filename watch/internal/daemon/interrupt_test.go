package daemon

import (
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

func TestDaemon_PostToolUseFailureInterruptSetsStopped(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// PostToolUseFailure with is_interrupt=true → Stopped.
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: true})

	sess := d.sessions["sess1"]
	if sess == nil {
		t.Fatal("expected session to be tracked")
		return
	}
	if sess.Status != detect.StatusStopped {
		t.Errorf("expected StatusStopped, got %v", sess.Status)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=yellow,dim" {
		t.Errorf("expected window-status-style=fg=yellow,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @cenci-symbol=⏹, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PostToolUseFailureNoInterruptStaysRunning(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// PostToolUseFailure without interrupt → still Running (tool failed, Claude retries).
	optsBeforeCount := len(mc.WindowOpts)
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: false})

	sess := d.sessions["sess1"]
	if sess == nil {
		t.Fatal("expected session to be tracked")
		return
	}
	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning, got %v", sess.Status)
	}
	// No status change (Running → Running) should be a no-op for styles.
	if len(mc.WindowOpts) != optsBeforeCount {
		t.Errorf("expected no additional window opts for same-status, got %d more", len(mc.WindowOpts)-optsBeforeCount)
	}
}

func TestDaemon_SweepDetectsIdlePaneTitle(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Verify running state.
	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning, got %v", sess.Status)
	}

	// Simulate: pane title reverts to idle marker (user pressed ESC during text
	// gen). A real ESC fires no hook event, so the backstop requires hook-event
	// quiescence before trusting the title (#706) — age the last event past it.
	mc.Panes[0].PaneTitle = "✳ writing tests"
	mc.WindowOpts = nil
	sess.LastEvent = time.Now().Add(-10 * time.Second)

	d.runSweep()

	// Sweep should detect idle marker and transition to Stopped.
	if sess.Status != detect.StatusStopped {
		t.Errorf("expected StatusStopped after sweep, got %v", sess.Status)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @cenci-symbol=⏹, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_SweepIgnoresRunningWithBrailleTitle(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Pane title still has braille spinner — Claude is actively working.
	mc.WindowOpts = nil
	d.runSweep()

	// Should remain Running (braille = active spinner, not idle).
	sess := d.sessions["sess1"]
	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning (braille spinner active), got %v", sess.Status)
	}
}

// TestDaemon_SweepIgnoresRunningWithHalfCircleTitle guards the half-circle
// working marker (◐◑◒◓) against the ESC backstop (#1039). The marker is shown
// only while the agent is working, so a title carrying it must never be read
// as an idle title — even once hook events have gone quiet, which is exactly
// what a long tool call looks like.
func TestDaemon_SweepIgnoresRunningWithHalfCircleTitle(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "◑ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning, got %v", sess.Status)
	}

	// Age the last event past the backstop's quiescence window so only the
	// marker's classification keeps the session running.
	mc.WindowOpts = nil
	sess.LastEvent = time.Now().Add(-10 * time.Second)

	d.runSweep()

	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning (half-circle marker means working), got %v", sess.Status)
	}
}

func TestDaemon_SweepIgnoresNonRunningWindows(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})

	// Window is now Done.
	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusDone {
		t.Fatalf("precondition: expected StatusDone, got %v", sess.Status)
	}

	mc.WindowOpts = nil
	d.runSweep()

	// Should remain Done — sweep idle detection only applies to Running windows.
	if sess.Status != detect.StatusDone {
		t.Errorf("expected StatusDone unchanged, got %v", sess.Status)
	}
}

// TestDaemon_SweepDoesNotStopSessionHeldForBackgroundWork covers #706: a
// main-agent Stop with in-flight background work holds the session at running
// (#698), and the pane title then legitimately reverts to an idle marker
// ("✳ …") while the session waits at the prompt to be woken. The Phase-3
// title sweep must not override the hold while it lasts, since no hook event
// fires until the background work wakes the session. The hold is not
// unbounded: after frontend.BackgroundHoldTTL of total silence it resolves to
// done rather than stopped — see backgroundhold_test.go (#1079).
func TestDaemon_SweepDoesNotStopSessionHeldForBackgroundWork(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0", BackgroundWork: true})

	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning after held Stop, got %v", sess.Status)
	}

	// Paused at the prompt: idle-marker title, and hook events long quiet.
	mc.Panes[0].PaneTitle = "✳ delegating work"
	sess.LastEvent = time.Now().Add(-10 * time.Second)
	mc.WindowOpts = nil

	d.runSweep()

	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning to survive the sweep while background work is in flight, got %v", sess.Status)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); ok && v == "⏹" {
		t.Errorf("expected no stopped symbol while held for background work, got %q", v)
	}
}

// TestDaemon_SweepDoesNotStopFreshlyActiveSession covers the #706 race: a
// pane title sampled in the same second as fresh hook events must not outrank
// them — the sweep may only trust an idle-marker title after hook events have
// gone quiet.
func TestDaemon_SweepDoesNotStopFreshlyActiveSession(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning, got %v", sess.Status)
	}

	// LastEvent is fresh (just set by handleEvent) — the sweep must leave the
	// session running even though the title shows a non-braille marker.
	d.runSweep()

	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning to survive a sweep with fresh hook events, got %v", sess.Status)
	}
}

// TestDaemon_SubagentEventDoesNotClearBackgroundHold pins the hold against
// the background work itself: a backgrounded subagent's own tool events land
// on the same session key (AgentID set), and they are exactly the in-flight
// work the hold protects. They must not clear it — otherwise any >quiescence
// gap between the subagent's tool calls (a long shell command, a slow API
// call) re-exposes the idle-marker title to the sweep and stops the session
// mid-flight, the very bug (#706) the hold exists to fix.
func TestDaemon_SubagentEventDoesNotClearBackgroundHold(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0", BackgroundWork: true})

	// The backgrounded subagent keeps working on the same session key.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash", AgentID: "sub1"})

	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning, got %v", sess.Status)
	}

	// A long gap inside the subagent's tool call: idle-marker title, hooks
	// quiet past the quiescence window. The hold must still protect the
	// session.
	mc.Panes[0].PaneTitle = "✳ delegating work"
	sess.LastEvent = time.Now().Add(-10 * time.Second)

	d.runSweep()

	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning to survive the sweep while the subagent is mid-flight, got %v", sess.Status)
	}
}

// TestDaemon_EventAfterHoldClearsBackgroundHold pins the hold's lifecycle: a
// later main-agent event (the background work woke the session, it worked and
// went quiet again) clears the hold, so a subsequent quiescent idle-marker
// title may stop the session via the ESC backstop again.
func TestDaemon_EventAfterHoldClearsBackgroundHold(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0", BackgroundWork: true})

	// Background work woke the session; a new main-agent prompt runs a turn.
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning, got %v", sess.Status)
	}

	// User ESCs during pure text generation: idle-marker title, hooks quiet.
	mc.Panes[0].PaneTitle = "✳ delegating work"
	sess.LastEvent = time.Now().Add(-10 * time.Second)

	d.runSweep()

	if sess.Status != detect.StatusStopped {
		t.Errorf("expected StatusStopped after hold was cleared by a later event, got %v", sess.Status)
	}
}

func TestDaemon_FullLifecycleWithInterrupt(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	// SessionStart → idle
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusIdle {
		t.Errorf("after SessionStart: expected StatusIdle, got %v", sess.Status)
	}

	// UserPromptSubmit → running
	mc.Panes[0].PaneTitle = "⠋ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusRunning {
		t.Errorf("after UserPromptSubmit: expected StatusRunning, got %v", sess.Status)
	}

	// PostToolUseFailure(interrupt) → stopped
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: true})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusStopped {
		t.Errorf("after PostToolUseFailure: expected StatusStopped, got %v", sess.Status)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @cenci-symbol=⏹, got %q (found=%v)", v, ok)
	}

	// UserPromptSubmit → running again (user submits new prompt)
	mc.Panes[0].PaneTitle = "⠋ fixing bug"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusRunning {
		t.Errorf("after second UserPromptSubmit: expected StatusRunning, got %v", sess.Status)
	}
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "fixing bug" {
		t.Errorf("expected rename to 'fixing bug', got %q (found=%v)", name, ok)
	}

	// Stop → done (natural completion)
	mc.Panes[0].PaneTitle = "✳ fixing bug"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusDone {
		t.Errorf("after Stop: expected StatusDone, got %v", sess.Status)
	}

	// SessionEnd → restored
	mc.Renames = nil
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	if len(mc.Renames) != 1 || mc.Renames[0].Name != "bash" {
		t.Errorf("after SessionEnd: expected restore to 'bash', got %v", mc.Renames)
	}
	if len(d.sessions) != 0 {
		t.Errorf("expected sessions map empty after SessionEnd, got %d", len(d.sessions))
	}
}
