package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tmuxfe "github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
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

// TestDaemon_PendingCloseSurvivesSessionHandoff pins the pending-close ×
// handoff interaction (#707): a SessionEnd with reason "clear"/"resume" hands
// the window to a successor session, so a registered pending-close must NOT
// kill the window mid-continuation. The entry stays registered — it is keyed
// by "session:index", which survives the handoff — and fires once the
// successor session's own SessionEnd arrives.
func TestDaemon_PendingCloseSurvivesSessionHandoff(t *testing.T) {
	mc := trackedSessionMock()
	d := newTestDaemon(mc)
	fk := d.killer.(*fakeKiller)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	d.registerPendingClose(ipc.PendingClose{Session: "main", WindowIndex: "0", WindowName: "bash"})

	// /clear ends sess1 but the successor continues in the same pane: the
	// window must survive and the pending-close entry must stay registered.
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0", SessionEndReason: "clear"})

	if len(fk.killed) != 0 {
		t.Fatalf("killed = %v, want none on a clear/resume handoff — the successor still owns the window", fk.killed)
	}
	if len(d.pending) != 1 {
		t.Fatalf("pending registry size = %d, want the entry retained across the handoff", len(d.pending))
	}

	// The successor session takes over the window, then ends for real.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess2", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess2", TmuxPane: "%0"})

	if len(fk.killed) != 1 || fk.killed[0] != "=main:0" {
		t.Errorf("killed = %v, want exactly [=main:0] once the successor session ends", fk.killed)
	}
	if len(d.pending) != 0 {
		t.Errorf("pending registry size = %d, want 0 after the successor's SessionEnd fired the kill", len(d.pending))
	}
}

// -- babysit close guard (#787) ----------------------------------------------

// countingGuard is a call-recording babysit close guard for daemon tests
// (#787). blocked is flipped by the test to simulate CI turning green.
type countingGuard struct {
	asked   []string
	blocked bool
}

func (g *countingGuard) guard(ticket string) (bool, string) {
	g.asked = append(g.asked, ticket)
	if g.blocked {
		return true, "babysit supervising PR #790, CI not green"
	}
	return false, ""
}

// babysitTrackedMock mirrors trackedSessionMock but names the window with the
// "<ticket>-<skill>" convention the close guard joins on.
func babysitTrackedMock() *tmuxtest.MockClient {
	return &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "782-implement", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}
}

// startTrackedSession drives the two events that make the daemon track a
// session's window, then registers it as pending-close.
func startTrackedSession(d *Daemon) {
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.registerPendingClose(ipc.PendingClose{Session: "main", WindowIndex: "0", WindowName: "782-implement"})
}

// TestDaemon_PendingCloseHeldWhileBabysitBlocks covers the deferred-kill half
// of #787: the implement session ends while `cenci babysit` is still
// supervising its PR, so the daemon must NOT fire the registered
// pending-close — and must keep the entry so it can retry later.
func TestDaemon_PendingCloseHeldWhileBabysitBlocks(t *testing.T) {
	d := newTestDaemon(babysitTrackedMock())
	fk := d.killer.(*fakeKiller)
	g := &countingGuard{blocked: true}
	d.closeGuard = g.guard

	startTrackedSession(d)
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(fk.killed) != 0 {
		t.Fatalf("killed = %v, want none while babysit still owns the ticket", fk.killed)
	}
	if len(d.pending) != 1 {
		t.Fatalf("pending registry size = %d, want the entry retained for a later retry", len(d.pending))
	}
	if len(g.asked) == 0 || g.asked[0] != "782" {
		t.Errorf("guard asked about %v, want the ticket number from the window name", g.asked)
	}
}

// TestDaemon_BlockedPendingCloseKilledOnceGuardClears covers the retry half of
// #787: once babysit stops blocking (CI green, or the supervisor exits), the
// next re-check from the sweep closes the window — exactly once.
func TestDaemon_BlockedPendingCloseKilledOnceGuardClears(t *testing.T) {
	d := newTestDaemon(babysitTrackedMock())
	fk := d.killer.(*fakeKiller)
	g := &countingGuard{blocked: true}
	d.closeGuard = g.guard
	now := time.Now()
	d.now = func() time.Time { return now }

	startTrackedSession(d)
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	if len(fk.killed) != 0 {
		t.Fatalf("killed = %v, want none while blocked", fk.killed)
	}

	g.blocked = false
	now = now.Add(pendingCloseRecheckInterval + time.Second)
	d.runSweep()

	if len(fk.killed) != 1 || fk.killed[0] != "=main:0" {
		t.Fatalf("killed = %v, want exactly [=main:0] once the guard clears", fk.killed)
	}
	if len(d.pending) != 0 {
		t.Errorf("pending registry size = %d, want 0 after the retry fired", len(d.pending))
	}

	// A further sweep must not kill the same window a second time.
	now = now.Add(pendingCloseRecheckInterval + time.Second)
	d.runSweep()
	if len(fk.killed) != 1 {
		t.Errorf("killed = %v, want the retry to fire exactly once", fk.killed)
	}
}

// TestDaemon_BlockedPendingCloseRecheckIsThrottled pins the re-check cadence
// (#787): runSweep ticks once a second, but a blocked entry must consult the
// guard at most once per pendingCloseRecheckInterval.
func TestDaemon_BlockedPendingCloseRecheckIsThrottled(t *testing.T) {
	d := newTestDaemon(babysitTrackedMock())
	g := &countingGuard{blocked: true}
	d.closeGuard = g.guard
	now := time.Now()
	d.now = func() time.Time { return now }

	startTrackedSession(d)
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	atSessionEnd := len(g.asked)

	for i := 0; i < 5; i++ {
		now = now.Add(time.Second)
		d.runSweep()
	}
	if len(g.asked) != atSessionEnd {
		t.Errorf("guard consulted %d times over 5s of sweeps, want no re-check inside the %s throttle", len(g.asked)-atSessionEnd, pendingCloseRecheckInterval)
	}

	now = now.Add(pendingCloseRecheckInterval)
	d.runSweep()
	if len(g.asked) != atSessionEnd+1 {
		t.Errorf("guard consulted %d times past the throttle window, want exactly 1", len(g.asked)-atSessionEnd)
	}
}

// TestDaemon_NonBlockingGuardKeepsSessionEndKill asserts a guard that never
// blocks leaves the existing #522 behavior byte-for-byte intact.
func TestDaemon_NonBlockingGuardKeepsSessionEndKill(t *testing.T) {
	d := newTestDaemon(babysitTrackedMock())
	fk := d.killer.(*fakeKiller)
	g := &countingGuard{}
	d.closeGuard = g.guard

	startTrackedSession(d)
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(fk.killed) != 1 || fk.killed[0] != "=main:0" {
		t.Errorf("killed = %v, want the pending-close to fire at SessionEnd when nothing blocks", fk.killed)
	}
	if len(d.pending) != 0 {
		t.Errorf("pending registry size = %d, want 0 after the kill", len(d.pending))
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

// TestDaemon_PendingCloseHeldByArmingPidZeroState drives the pending-close
// path through the *real* BlocksClose (newDaemon's default closeGuard, not
// newTestDaemon's nil-disabled one), proving #924's arming-liveness branch
// end to end: a supervisor state recorded as "arming" (PID 0, no live
// process to check) still holds a deferred pending-close open, closing
// ticket #782 exactly as a live PID would (ticket #782, closing issue for
// #790 as the joined supervised PR).
func TestDaemon_PendingCloseHeldByArmingPidZeroState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	stateDir := filepath.Join(dir, "cenci", "babysit")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	state := `{"schemaVersion":2,"pr":"790","repo":"o/r","agent":"claude","closingIssues":[782],"ciStatus":"pending","status":"arming","pid":0,"updatedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(filepath.Join(stateDir, "abc123-790.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}

	mc := babysitTrackedMock()
	cfg := testConfig()
	ch := make(chan ipc.HookEvent, 16)
	d := newDaemon(cfg, tmuxfe.New(cfg, mc), ch, &mockReaper{})
	fk := &fakeKiller{}
	d.killer = fk

	startTrackedSession(d)
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(fk.killed) != 0 {
		t.Fatalf("killed = %v, want none while an arming (PID 0) supervisor still owns the ticket", fk.killed)
	}
	if len(d.pending) != 1 {
		t.Fatalf("pending registry size = %d, want the entry retained for a later retry", len(d.pending))
	}
}
