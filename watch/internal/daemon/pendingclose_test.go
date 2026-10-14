package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tmuxfe "github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc/windowproctest"
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
	// newDaemon defaults to the production goroutine runner; a kill that ran
	// off-loop would race the assertions below, so install the synchronous one.
	d.runOffLoop = (&syncOffLoop{}).run

	startTrackedSession(d)
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(fk.killed) != 0 {
		t.Fatalf("killed = %v, want none while an arming (PID 0) supervisor still owns the ticket", fk.killed)
	}
	if len(d.pending) != 1 {
		t.Fatalf("pending registry size = %d, want the entry retained for a later retry", len(d.pending))
	}
}

// -- process-tree kill before kill-window (#1253) ----------------------------
//
// The daemon tests below drive the real windowproc.Killer and the real
// tmux.ExecClient against fake tmux/docker/podman binaries alone on PATH
// (windowproctest, whose Install verifies the fakes are what PATH resolves
// to), through the synchronous off-loop runner newTestDaemon installs. The
// grace window is 0 unless a test says otherwise.

const (
	dcLaunchID = "0123456789abcdef0123456789abcdef"
	// dcTarget is the exact target of babysitTrackedMock's window ("main", index 0).
	dcTarget = "=main:0"
	// dcOffLoopMargin is the headroom the off-loop timeout adds on top of the
	// SIGTERM->SIGKILL grace window (plan assumption).
	dcOffLoopMargin = 60 * time.Second
)

// closePathDaemon is a test daemon wired to the real kill path.
func closePathDaemon(t *testing.T) (*Daemon, *syncOffLoop) {
	t.Helper()
	d, runner := newTestDaemonWithRunner(babysitTrackedMock())
	d.killer = &tmux.ExecClient{}
	d.procKiller = windowproc.New(io.Discard)
	return d, runner
}

func endSession(d *Daemon) {
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
}

func dcStubbornProcs() []windowproctest.Proc {
	return []windowproctest.Proc{
		{PID: "101", Start: "5001", IgnoreTerm: true},
		{PID: "102", Start: "5002", IgnoreTerm: true},
	}
}

// AC11 + AC15: when the agent session ends, a registered pending-close kills the
// pane's launch processes (TERM, then KILL) before it kills the window, for
// Claude and Codex containers on both runtimes.
func TestDaemon_PendingCloseOnSessionEndKillsLaunchProcessesBeforeKillWindow(t *testing.T) {
	for _, f := range windowproctest.Flavors {
		t.Run(f.Name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
			fakes := windowproctest.Install(t, f.SandboxSpec(dcLaunchID, windowproctest.HarmlessPanePID(t), dcStubbornProcs()...))
			d, _ := closePathDaemon(t)
			startTrackedSession(d)

			endSession(d)

			want := windowproctest.SandboxCloseCalls(f.Runtime, f.Container, dcLaunchID, dcTarget, "101", "102")
			if got := fakes.Calls(t); !slices.Equal(got, want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if len(d.pending) != 0 {
				t.Errorf("pending registry size = %d, want the fired entry removed", len(d.pending))
			}
		})
	}
}

// AC11 + AC15: the babysit-guard retry path gives the same guarantee. While the
// guard blocks, nothing is listed, signaled or killed; once it clears, the next
// sweep past the recheck interval runs the full kill sequence.
func TestDaemon_BabysitRetryKillsLaunchProcessesBeforeKillWindow(t *testing.T) {
	for _, f := range windowproctest.Flavors {
		t.Run(f.Name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
			fakes := windowproctest.Install(t, f.SandboxSpec(dcLaunchID, windowproctest.HarmlessPanePID(t), dcStubbornProcs()...))
			d, _ := closePathDaemon(t)
			g := &countingGuard{blocked: true}
			d.closeGuard = g.guard
			now := time.Now()
			d.now = func() time.Time { return now }
			startTrackedSession(d)

			endSession(d)
			if calls := fakes.Calls(t); len(calls) != 0 {
				t.Fatalf("calls:\n%s\nwant no tmux or runtime call while babysit still owns the ticket", strings.Join(calls, "\n"))
			}

			g.blocked = false
			now = now.Add(pendingCloseRecheckInterval + time.Second)
			d.runSweep()

			want := windowproctest.SandboxCloseCalls(f.Runtime, f.Container, dcLaunchID, dcTarget, "101", "102")
			if got := fakes.Calls(t); !slices.Equal(got, want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// AC12: for a host pane the daemon TERMs the pane's process group before
// kill-window; a Setsid process (the babysit supervisor shape) survives.
func TestDaemon_HostWindowProcessGroupIsTermedBeforeKillWindowAndSetsidProcessSurvives(t *testing.T) {
	leader, member, survivor := windowproctest.StartCooperativeGroup(t)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "5")
	fakes := windowproctest.Install(t, windowproctest.Spec{
		Panes:                 []windowproctest.Pane{{ID: "%0", PanePID: fmt.Sprint(leader.PID)}},
		ProbePIDsAtKillWindow: []string{fmt.Sprint(leader.PID), fmt.Sprint(member.PID)},
	})
	d, _ := closePathDaemon(t)
	startTrackedSession(d)

	endSession(d)

	leader.RequireDeathBy(t, "group leader", syscall.SIGTERM)
	member.RequireDeathBy(t, "group member", syscall.SIGTERM)
	survivor.RequireAlive(t, "Setsid process")
	want := append(windowproctest.ListCalls(dcTarget), []string{
		"tmux kill-window -t " + dcTarget,
		fmt.Sprintf("tmux kill-window-check %d gone", leader.PID),
		fmt.Sprintf("tmux kill-window-check %d gone", member.PID),
	}...)
	if got := fakes.Calls(t); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// AC13 + AC15: a failed process kill is logged -- with the runtime's own
// message -- and the window is still killed.
func TestDaemon_FailedProcessKillIsLoggedAndWindowStillClosed(t *testing.T) {
	for _, f := range windowproctest.Flavors {
		t.Run(f.Name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
			spec := f.SandboxSpec(dcLaunchID, windowproctest.HarmlessPanePID(t), dcStubbornProcs()...)
			spec.ProbeFails = true
			fakes := windowproctest.Install(t, spec)
			logs := captureLog(t)
			d, _ := closePathDaemon(t)
			startTrackedSession(d)

			endSession(d)

			for _, want := range []string{"pending-close: error killing processes of", dcTarget, windowproctest.TransportMarker} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log = %q, want it to contain %q", logs.String(), want)
				}
			}
			calls := fakes.Calls(t)
			if len(calls) == 0 || calls[len(calls)-1] != "tmux kill-window -t "+dcTarget {
				t.Errorf("calls:\n%s\nwant kill-window to still run last", strings.Join(calls, "\n"))
			}
		})
	}
}

// AC13: a window that already closed itself (gone at the listing, or vanished
// before kill-window) is closed, not an error -- nothing is logged as one.
func TestDaemon_WindowAlreadyGoneIsNotLoggedAsAnError(t *testing.T) {
	cases := map[string]func(*windowproctest.Spec){
		"vanished before kill-window": func(s *windowproctest.Spec) { s.KillWindowGone = true },
		"gone at the pane listing":    func(s *windowproctest.Spec) { s.ListGone = true; s.KillWindowGone = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
			spec := windowproctest.Flavors[0].SandboxSpec(dcLaunchID, windowproctest.HarmlessPanePID(t), dcStubbornProcs()...)
			mutate(&spec)
			fakes := windowproctest.Install(t, spec)
			logs := captureLog(t)
			d, _ := closePathDaemon(t)
			startTrackedSession(d)

			endSession(d)

			if strings.Contains(strings.ToLower(logs.String()), "error") {
				t.Errorf("log = %q, want no error logged for a window that was already gone", logs.String())
			}
			if calls := fakes.Calls(t); len(calls) == 0 || calls[len(calls)-1] != "tmux kill-window -t "+dcTarget {
				t.Errorf("calls:\n%s\nwant kill-window attempted last", strings.Join(calls, "\n"))
			}
			if len(d.pending) != 0 {
				t.Errorf("pending registry size = %d, want the entry removed", len(d.pending))
			}
		})
	}
}

// AC13 guard: only a missing-window message counts as closed; any other
// kill-window failure is still logged.
func TestDaemon_KillWindowFailureOtherThanGoneIsStillLogged(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	spec := windowproctest.Flavors[0].SandboxSpec(dcLaunchID, windowproctest.HarmlessPanePID(t), dcStubbornProcs()...)
	spec.KillWindowFails = true
	fakes := windowproctest.Install(t, spec)
	logs := captureLog(t)
	d, _ := closePathDaemon(t)
	startTrackedSession(d)

	endSession(d)

	for _, want := range []string{"pending-close: error killing " + dcTarget, windowproctest.TmuxUnrelatedErrorText} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log = %q, want it to contain %q", logs.String(), want)
		}
	}
	// The processes were still killed first; only the window kill failed.
	flavor := windowproctest.Flavors[0]
	want := windowproctest.SandboxCloseCalls(flavor.Runtime, flavor.Container, dcLaunchID, dcTarget, "101", "102")
	if got := fakes.Calls(t); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The off-loop work is bounded: the daemon asks the runner for the SIGTERM->KILL
// grace window plus a fixed margin, so a hung runtime exec cannot outlive it.
func TestDaemon_OffLoopTimeoutIsGraceWindowPlusMargin(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "3")
	windowproctest.Install(t, windowproctest.Flavors[0].SandboxSpec(dcLaunchID, windowproctest.HarmlessPanePID(t)))
	d, runner := closePathDaemon(t)
	startTrackedSession(d)

	endSession(d)

	want := []time.Duration{3*time.Second + dcOffLoopMargin}
	if !slices.Equal(runner.timeouts, want) {
		t.Errorf("off-loop timeouts = %v, want %v", runner.timeouts, want)
	}
}

// gatedProcKiller blocks inside the process kill until released, reporting
// whether the context it got carries a deadline.
type gatedProcKiller struct {
	deadlineSeen chan bool
	release      chan struct{}
}

func (g *gatedProcKiller) KillWindowProcesses(ctx context.Context, target string) error {
	_, ok := ctx.Deadline()
	g.deadlineSeen <- ok
	<-g.release
	return nil
}

// targetKiller reports the kill-window targets it receives.
type targetKiller struct {
	killed chan string
}

func (k *targetKiller) KillWindow(target string) error {
	k.killed <- target
	return nil
}

// AC11: the event loop is never blocked by the grace window. With the
// production runner and a process kill that blocks, SessionEnd's handling
// returns at once, the registry entry is already gone (removed on the loop),
// the kill got a deadline-bearing context, and kill-window only runs after the
// process kill finishes.
func TestDaemon_PendingCloseNeverBlocksTheEventLoopOnTheProcessKill(t *testing.T) {
	const wait = 5 * time.Second
	cfg := testConfig()
	d := newDaemon(cfg, tmuxfe.New(cfg, babysitTrackedMock()), make(chan ipc.HookEvent, 16), &mockReaper{})
	d.closeGuard = nil
	gate := &gatedProcKiller{deadlineSeen: make(chan bool, 1), release: make(chan struct{})}
	killer := &targetKiller{killed: make(chan string, 1)}
	d.procKiller = gate
	d.killer = killer
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	startTrackedSession(d)

	returned := make(chan struct{})
	go func() {
		endSession(d)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(wait):
		t.Fatal("handling SessionEnd blocked on the process kill: the grace window ran on the event loop")
	}

	if len(d.pending) != 0 {
		t.Errorf("pending registry size = %d, want the entry removed on the loop before the kill is dispatched", len(d.pending))
	}
	select {
	case hasDeadline := <-gate.deadlineSeen:
		if !hasDeadline {
			t.Error("the process kill got a context without a deadline, want one bounded by the off-loop timeout")
		}
	case <-time.After(wait):
		t.Fatal("the process kill never started off the loop")
	}
	select {
	case target := <-killer.killed:
		t.Fatalf("kill-window of %s ran before the process kill finished", target)
	default:
	}

	release()
	select {
	case target := <-killer.killed:
		if target != dcTarget {
			t.Errorf("kill-window target = %q, want %q", target, dcTarget)
		}
	case <-time.After(wait):
		t.Fatal("kill-window never ran after the process kill finished")
	}
}

// Daemon.Run must install the real window-process killer: without it every
// pending-close window would be killed with its agent processes still running,
// and nothing else would notice. Run delegates its production wiring to
// installHostSeams, so this pins that seam. The test helpers deliberately leave
// procKiller nil (asserted first), so no other test can ever signal a real
// process by accident.
func TestInstallHostSeams_InstallsTheRealProcessKiller(t *testing.T) {
	d := newTestDaemon(babysitTrackedMock())
	if d.procKiller != nil {
		t.Fatalf("test daemon procKiller = %T, want nil so tests never signal real processes", d.procKiller)
	}

	d.installHostSeams(func(agent, workflow string) bool { return true })

	killer, ok := d.procKiller.(*windowproc.Killer)
	if !ok || killer == nil {
		t.Fatalf("procKiller = %T, want a non-nil *windowproc.Killer", d.procKiller)
	}
	if d.armCapable == nil {
		t.Error("armCapable = nil, want the capability predicate installed")
	}
}

// The window-cleanup pass (#1251) is inert until installHostSeams: newDaemon
// leaves the ticket reader nil so newDaemon-based tests never shell out to gh,
// and Run's installHostSeams must install the real reader and a real resolver
// factory (one fresh Resolver per call, never shared across passes), or the
// production pass would silently do nothing.
func TestInstallHostSeams_InstallsTheRealTicketReader(t *testing.T) {
	d := newTestDaemon(babysitTrackedMock())
	if d.cleanupReader != nil {
		t.Fatalf("test daemon cleanupReader = %T, want nil so tests never shell out to gh", d.cleanupReader)
	}
	if d.cleanupEnabled == nil {
		t.Error("cleanupEnabled = nil on a newDaemon daemon, want ticketstate.CleanupEnabled as the default")
	}

	d.installHostSeams(func(agent, workflow string) bool { return true })

	reader, ok := d.cleanupReader.(*ticketstate.Reader)
	if !ok || reader == nil {
		t.Fatalf("cleanupReader = %T, want a non-nil *ticketstate.Reader", d.cleanupReader)
	}
	if d.newCleanupResolver == nil {
		t.Fatal("newCleanupResolver = nil, want the real resolver factory installed")
	}
	first, ok := d.newCleanupResolver().(*ticketstate.Resolver)
	if !ok || first == nil {
		t.Fatalf("newCleanupResolver() = %T, want a non-nil *ticketstate.Resolver", d.newCleanupResolver())
	}
	if second := d.newCleanupResolver(); second == repoResolver(first) {
		t.Error("newCleanupResolver returned the same Resolver twice, want a fresh one per pass (a long-lived one can cache a stale origin)")
	}
}

// Under Verbose the "window already gone" outcome is logged as a closed window,
// not an error.
func TestDaemon_VerboseLogsAlreadyGoneWindowAsClosed(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	spec := windowproctest.Flavors[0].SandboxSpec(dcLaunchID, windowproctest.HarmlessPanePID(t), dcStubbornProcs()...)
	spec.ListGone = true
	spec.KillWindowGone = true
	windowproctest.Install(t, spec)
	logs := captureLog(t)
	d, _ := closePathDaemon(t)
	d.cfg.Verbose = true
	startTrackedSession(d)

	endSession(d)

	want := "pending-close: " + dcTarget + " already gone, counted as closed"
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q, want it to contain %q", logs.String(), want)
	}
	if strings.Contains(strings.ToLower(logs.String()), "error") {
		t.Errorf("log = %q, want no error logged for a window that was already gone", logs.String())
	}
}
