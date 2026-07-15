package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v4/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/tmux/tmuxtest"
)

// TestDaemon_SweepPaneGoneTriggersOneReap covers the Phase-2 stale-window
// path: a sweep pass that finds one tmux-backed session whose pane no longer
// exists must trigger exactly one Reap() call (#292 AC1).
func TestDaemon_SweepPaneGoneTriggersOneReap(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	mr := d.reaper.(*mockReaper)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Simulate the pane disappearing (Claude crash / window closed).
	mc.Panes = []tmux.PaneInfo{}

	d.runSweep()

	if got := mr.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 Reap() call for a pane-gone sweep, got %d", got)
	}
}

// TestDaemon_SweepMultipleStaleWindowsCoalescesReap asserts that when
// multiple windows go stale in the same runSweep() pass, the daemon still
// triggers exactly one Reap() call — coalesced, not one per window (#292 AC1).
func TestDaemon_SweepMultipleStaleWindowsCoalescesReap(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-zero", PaneID: "%0"},
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-one", PaneID: "%1"},
		},
	}

	d := newTestDaemon(mc)
	mr := d.reaper.(*mockReaper)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s0", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s0", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s1", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", TmuxPane: "%1"})

	// Both panes disappear in the same pass.
	mc.Panes = []tmux.PaneInfo{}

	d.runSweep()

	if got := mr.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 coalesced Reap() call for 2 stale windows in one pass, got %d", got)
	}
	if len(d.sessions) != 0 {
		t.Fatalf("precondition: expected both sessions removed, got %d", len(d.sessions))
	}
}

// TestDaemon_SweepPhase4UntrackedPaneGoneTriggersReap covers the Phase-4
// path: a core session whose pane is gone but that no tracked tmux window
// pointed at must also trigger the reaper (#292 Q&A 1 — Phase-4 included).
func TestDaemon_SweepPhase4UntrackedPaneGoneTriggersReap(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{}}
	d := newTestDaemon(mc)
	mr := d.reaper.(*mockReaper)

	// A core session references a pane that was never tracked by the tmux
	// frontend (e.g. tracking never succeeded) — Phase 4's untracked
	// pane-gone removal path, not Phase 2's tracked-window cleanup.
	d.sessions["sess1"] = &frontend.SessionState{SessionID: "sess1", TmuxPane: "%0"}

	d.runSweep()

	if got := mr.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 Reap() call for a Phase-4 untracked pane-gone session, got %d", got)
	}
}

// TestDaemon_SweepPhase3AgentExitedDoesNotTriggerReap asserts that Phase-3
// sweep behavior (agent process exited, pane still alive) does NOT trigger
// the reaper — the pane still exists, so it is never flagged pane-gone
// (#292 Implementation Plan assumption).
func TestDaemon_SweepPhase3AgentExitedDoesNotTriggerReap(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "codex", PaneTitle: "cenci", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	mr := d.reaper.(*mockReaper)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})

	// The codex process exits back to the shell, but the pane itself is
	// still alive (no SessionEnd hook for codex).
	mc.Panes[0].PaneCurrentCmd = "zsh"

	d.runSweep()

	if got := mr.calls.Load(); got != 0 {
		t.Errorf("expected zero Reap() calls for a Phase-3 agent-exited (pane alive) sweep, got %d", got)
	}
}

// TestDaemon_SweepNoOpTriggersZeroReap asserts a no-op sweep pass (nothing
// tracked, nothing changed) never triggers the reaper.
func TestDaemon_SweepNoOpTriggersZeroReap(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	mr := d.reaper.(*mockReaper)

	d.runSweep()

	if got := mr.calls.Load(); got != 0 {
		t.Errorf("expected zero Reap() calls for a no-op sweep, got %d", got)
	}
}

// TestDaemon_StartupTriggersOneReap asserts the daemon runs exactly one reap
// pass at startup, before entering its event loop (#292 AC2) — covering
// panes that closed while the daemon was down.
func TestDaemon_StartupTriggersOneReap(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	mr := d.reaper.(*mockReaper)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.loop(ctx, nil)
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error from loop, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after cancel")
	}

	if got := mr.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 startup Reap() call, got %d", got)
	}
}
