package daemon

import (
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux/tmuxtest"
)

// trackedSessionMock returns a mock tmux client with one pane so tests can
// track a session's window (SessionStart + UserPromptSubmit) before
// exercising the pending-close/SessionEnd interaction (#522).
func trackedSessionMock() *tmuxtest.MockClient {
	return &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}
}

// TestDaemon_PendingCloseKilledOnSessionEnd covers the core self-healing
// path (#522 AC1/AC2): a window registered as pending-close is killed via
// the injected killer seam once the daemon observes the owning session's
// main-agent SessionEnd, targeting the exact "=session:index" the window
// resolves to.
func TestDaemon_PendingCloseKilledOnSessionEnd(t *testing.T) {
	mc := trackedSessionMock()
	d := newTestDaemon(mc)
	fk := d.killer.(*fakeKiller)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	d.registerPendingClose(ipc.PendingClose{Session: "main", WindowIndex: "0", WindowName: "bash"})

	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(fk.killed) != 1 {
		t.Fatalf("killed = %v, want exactly 1 kill on SessionEnd for a registered pending-close", fk.killed)
	}
	if fk.killed[0] != "=main:0" {
		t.Errorf("killed target = %q, want %q", fk.killed[0], "=main:0")
	}
}

// TestDaemon_PendingCloseDuplicateRegistrationDedup covers #522 AC ("exactly
// one pending-close entry and exactly one eventual kill"): registering the
// same session:index pending-close twice (e.g. lazyboards re-firing cleanup
// before the session ends) must not produce a duplicate registry entry or a
// duplicate kill.
func TestDaemon_PendingCloseDuplicateRegistrationDedup(t *testing.T) {
	mc := trackedSessionMock()
	d := newTestDaemon(mc)
	fk := d.killer.(*fakeKiller)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	d.registerPendingClose(ipc.PendingClose{Session: "main", WindowIndex: "0", WindowName: "bash"})
	d.registerPendingClose(ipc.PendingClose{Session: "main", WindowIndex: "0", WindowName: "bash"})

	if len(d.pending) != 1 {
		t.Fatalf("pending registry size = %d, want exactly 1 entry after registering the same session:index twice", len(d.pending))
	}

	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(fk.killed) != 1 {
		t.Errorf("killed = %v, want exactly 1 kill despite duplicate registration", fk.killed)
	}
}

// TestDaemon_SessionEndWithoutPendingCloseDoesNotKill asserts the existing
// restore-on-SessionEnd path is untouched when nothing was ever registered
// as pending-close: no kill call, and the window is still restored to its
// original name exactly as before this feature (#522 AC "existing restore
// path intact").
func TestDaemon_SessionEndWithoutPendingCloseDoesNotKill(t *testing.T) {
	mc := trackedSessionMock()
	d := newTestDaemon(mc)
	fk := d.killer.(*fakeKiller)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	mc.Renames = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(fk.killed) != 0 {
		t.Errorf("killed = %v, want none when no pending-close was registered", fk.killed)
	}
	if len(mc.Renames) != 1 || mc.Renames[0].Name != "bash" {
		t.Errorf("expected the existing restore-to-original-name behavior to still fire, got renames=%+v", mc.Renames)
	}
}

// TestDaemon_PendingClosePrunedWhenWindowGoneViaSweep covers the #522
// registry-growth risk mitigation: a window closed by something other than
// a tracked SessionEnd (manual `tmux kill-window`, `--force` from
// elsewhere, a renumber-discard conflict) must still have its pending-close
// entry pruned once runSweep observes the pane is gone. Sweep's own
// stale-window cleanup forgets the window's tracking state internally
// before runSweep gets a chance to resolve WindowInfo on it, so the prune
// must resolve WindowInfo *before* calling Sweep — this test guards against
// silently regressing back to an after-the-fact resolution that always sees
// nil and leaks the entry.
func TestDaemon_PendingClosePrunedWhenWindowGoneViaSweep(t *testing.T) {
	mc := trackedSessionMock()
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	d.registerPendingClose(ipc.PendingClose{Session: "main", WindowIndex: "0", WindowName: "bash"})
	if len(d.pending) != 1 {
		t.Fatalf("pending registry size = %d, want exactly 1 entry after registration", len(d.pending))
	}

	// Simulate the window disappearing by means other than a tracked
	// SessionEnd (manual kill, --force elsewhere, renumber conflict).
	mc.Panes = []tmux.PaneInfo{}

	d.runSweep()

	if len(d.pending) != 0 {
		t.Errorf("pending registry size = %d, want 0 after the owning window is swept away", len(d.pending))
	}
}

// TestDaemon_SubagentSessionEndDoesNotKillPendingClose asserts that a
// subagent's own SessionEnd (non-empty AgentID) never triggers a
// pending-close kill — only the main agent's own SessionEnd does (mirrors
// the existing subagent-SessionEnd guard in handleEvent).
func TestDaemon_SubagentSessionEndDoesNotKillPendingClose(t *testing.T) {
	mc := trackedSessionMock()
	d := newTestDaemon(mc)
	fk := d.killer.(*fakeKiller)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	d.registerPendingClose(ipc.PendingClose{Session: "main", WindowIndex: "0", WindowName: "bash"})

	// A subagent's own SessionEnd must not trigger the kill.
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0", AgentID: "sub1"})

	if len(fk.killed) != 0 {
		t.Errorf("killed = %v, want none for a subagent SessionEnd", fk.killed)
	}
}
