package closecmd_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/closecmd"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc/windowproctest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

type fakeKiller struct {
	killed []string
	err    error
}

func (f *fakeKiller) KillWindow(target string) error {
	if f.err != nil {
		return f.err
	}
	f.killed = append(f.killed, target)
	return nil
}

func fakeReadSnapshot(snap *watch.StateSnapshot, err error) func(string) (*watch.StateSnapshot, error) {
	return func(string) (*watch.StateSnapshot, error) {
		return snap, err
	}
}

// fakeRegister is a call-recording pending-close registrar for tests (#522).
// It mirrors fakeKiller above: no real IPC call, just records every window
// Run asked to register as pending-close.
type fakeRegister struct {
	registered []watch.WindowState
	err        error
}

func (f *fakeRegister) Register(w watch.WindowState) error {
	f.registered = append(f.registered, w)
	return f.err
}

func TestRun_NumberMatchesExactAndPrefixNotLongerNumber(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "42", Status: "done"},
		{Session: "s1", WindowIndex: "1", PaneID: "%1", WindowName: "42-refine", Status: "done"},
		{Session: "s1", WindowIndex: "2", PaneID: "%2", WindowName: "420-x", Status: "done"},
	}}
	killer := &fakeKiller{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v, want 2 (42 and 42-refine, not 420-x)", decisions)
	}
	for _, d := range decisions {
		if d.Window.WindowName == "420-x" {
			t.Errorf("420-x must not match target 42 (prefix boundary)")
		}
	}
	if len(killer.killed) != 2 {
		t.Errorf("killed = %v, want 2 targets", killer.killed)
	}
}

func TestRun_ExactNameMatch(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "my-window", Status: "done"},
		{Session: "s1", WindowIndex: "1", PaneID: "%1", WindowName: "my-window-2", Status: "done"},
	}}
	killer := &fakeKiller{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "my-window",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Window.WindowName != "my-window" {
		t.Fatalf("decisions = %+v, want exactly [my-window]", decisions)
	}
}

func TestRun_MultiMatchAcrossSessionsClosesAllNonBusy(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "sess-a", WindowIndex: "3", PaneID: "%3", WindowName: "42-implement", Status: "done"},
		{Session: "sess-b", WindowIndex: "1", PaneID: "%1", WindowName: "42-review", Status: "idle"},
	}}
	killer := &fakeKiller{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v, want 2", decisions)
	}
	wantTargets := map[string]bool{"%3": true, "%1": true}
	for _, target := range killer.killed {
		if !wantTargets[target] {
			t.Errorf("unexpected kill target %q", target)
		}
		delete(wantTargets, target)
	}
	if len(wantTargets) != 0 {
		t.Errorf("missing kill targets: %v", wantTargets)
	}
}

func TestRun_RunningAndNeedInputSkippedWithoutForce(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "42-a", Status: "running"},
		{Session: "s1", WindowIndex: "1", PaneID: "%1", WindowName: "42-b", Status: "need-input"},
	}}
	killer := &fakeKiller{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(killer.killed) != 0 {
		t.Errorf("killed = %v, want none (both busy, no --force)", killer.killed)
	}
	for _, d := range decisions {
		if d.Action != closecmd.ActionSkippedBusy {
			t.Errorf("window %s action = %s, want skipped-busy", d.Window.WindowName, d.Action)
		}
	}
}

func TestRun_RunningAndNeedInputClosedWithForce(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "42-a", Status: "running"},
		{Session: "s1", WindowIndex: "1", PaneID: "%1", WindowName: "42-b", Status: "need-input"},
	}}
	killer := &fakeKiller{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		Force:        true,
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(killer.killed) != 2 {
		t.Errorf("killed = %v, want both windows killed with --force", killer.killed)
	}
	for _, d := range decisions {
		if d.Action != closecmd.ActionClosed {
			t.Errorf("window %s action = %s, want closed", d.Window.WindowName, d.Action)
		}
	}
}

func TestRun_SnapshotReadErrorReturnsErrorKillerNeverCalled(t *testing.T) {
	killer := &fakeKiller{}
	readErr := errors.New("dial unix: no such file or directory")
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		ReadSnapshot: fakeReadSnapshot(nil, readErr),
		Killer:       killer,
	})
	if err == nil {
		t.Fatal("Run: want error on snapshot read failure, got nil")
	}
	if len(decisions) != 0 {
		t.Errorf("decisions = %+v, want none", decisions)
	}
	if len(killer.killed) != 0 {
		t.Errorf("killer.killed = %v, want zero tmux calls on daemon read failure", killer.killed)
	}
}

// -- pending-close registration (#522) ---------------------------------------

// TestRun_BusySkipCallsRegisterForEachSkippedWindow covers #522 AC1: a
// matched window skipped as busy (no --force) must be registered with the
// daemon as pending-close, one call per skipped window.
func TestRun_BusySkipCallsRegisterForEachSkippedWindow(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "42-a", Status: "running"},
		{Session: "s1", WindowIndex: "1", PaneID: "%1", WindowName: "42-b", Status: "need-input"},
	}}
	killer := &fakeKiller{}
	fr := &fakeRegister{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
		Register:     fr.Register,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fr.registered) != 2 {
		t.Fatalf("registered = %+v, want 2 (one per busy-skipped window)", fr.registered)
	}
	if len(killer.killed) != 0 {
		t.Errorf("killed = %v, want none for busy-skipped windows", killer.killed)
	}
	for _, d := range decisions {
		if d.Action != closecmd.ActionSkippedBusy {
			t.Errorf("window %s action = %s, want skipped-busy", d.Window.WindowName, d.Action)
		}
	}
}

// TestRun_DryRunNeverCallsRegisterEvenForBusyWindows covers #522 AC
// ("--dry-run never registers a pending-close intent"): a mix of a
// non-busy and a busy match under --dry-run still renders the normal
// would-close/skipped-busy decisions, but must call Register zero times.
func TestRun_DryRunNeverCallsRegisterEvenForBusyWindows(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "42-a", Status: "done"},
		{Session: "s1", WindowIndex: "1", PaneID: "%1", WindowName: "42-b", Status: "running"},
	}}
	fr := &fakeRegister{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		DryRun:       true,
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       &fakeKiller{},
		Register:     fr.Register,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fr.registered) != 0 {
		t.Errorf("registered = %+v, want zero Register calls under --dry-run", fr.registered)
	}
	var gotWouldClose, gotSkippedBusy bool
	for _, d := range decisions {
		switch d.Action {
		case closecmd.ActionWouldClose:
			gotWouldClose = true
		case closecmd.ActionSkippedBusy:
			gotSkippedBusy = true
		}
	}
	if !gotWouldClose || !gotSkippedBusy {
		t.Fatalf("decisions = %+v, want both would-close (42-a) and skipped-busy (42-b) present", decisions)
	}
}

// TestRun_ForceClosesImmediatelyNeverRegisters covers #522 AC ("--force
// closes are unaffected ... with no pending-close bookkeeping"): --force
// kills busy windows immediately and never registers them.
func TestRun_ForceClosesImmediatelyNeverRegisters(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "42-a", Status: "running"},
		{Session: "s1", WindowIndex: "1", PaneID: "%1", WindowName: "42-b", Status: "need-input"},
	}}
	killer := &fakeKiller{}
	fr := &fakeRegister{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		Force:        true,
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
		Register:     fr.Register,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(killer.killed) != 2 {
		t.Errorf("killed = %v, want both windows killed immediately with --force", killer.killed)
	}
	if len(fr.registered) != 0 {
		t.Errorf("registered = %+v, want zero Register calls when --force closes immediately", fr.registered)
	}
	for _, d := range decisions {
		if d.Action != closecmd.ActionClosed {
			t.Errorf("window %s action = %s, want closed", d.Window.WindowName, d.Action)
		}
	}
}

// -- babysit close guard (#787) ----------------------------------------------

// fakeGuard is a call-recording babysit close guard for tests (#787). It
// records every ticket Run asked about and blocks the ones in blocked.
type fakeGuard struct {
	asked   []string
	blocked map[string]string
}

func (f *fakeGuard) Guard(ticket string) (bool, string) {
	f.asked = append(f.asked, ticket)
	if reason, ok := f.blocked[ticket]; ok {
		return true, reason
	}
	return false, ""
}

// TestRun_BabysitGuardSkipsWithoutKillOrRegistration covers #787 AC1: an idle
// window whose ticket is still owned by a live babysit supervisor with CI not
// green is skipped rather than killed — and, unlike a busy skip, it is *not*
// registered as pending-close, because the guard is not tied to the session's
// lifetime.
func TestRun_BabysitGuardSkipsWithoutKillOrRegistration(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "782-implement", Status: "done"},
	}}
	killer := &fakeKiller{}
	fr := &fakeRegister{}
	fg := &fakeGuard{blocked: map[string]string{"782": "babysit supervising PR #790, CI not green"}}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "782",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
		Register:     fr.Register,
		Guard:        fg.Guard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != closecmd.ActionSkippedBabysit {
		t.Fatalf("decisions = %+v, want exactly one skipped-babysit", decisions)
	}
	if decisions[0].Reason != "babysit supervising PR #790, CI not green" {
		t.Errorf("reason = %q, want the guard's reason carried into the decision", decisions[0].Reason)
	}
	if len(killer.killed) != 0 {
		t.Errorf("killed = %v, want none while a supervisor still owns the ticket", killer.killed)
	}
	if len(fr.registered) != 0 {
		t.Errorf("registered = %+v, want zero pending-close registrations for a babysit skip", fr.registered)
	}
	if len(fg.asked) != 1 || fg.asked[0] != "782" {
		t.Errorf("guard asked about %v, want the ticket number extracted from the window name", fg.asked)
	}
}

// TestRun_BabysitGuardAllowsWhenNotBlocking asserts a non-blocking guard is
// fully transparent: the window closes exactly as it does today.
func TestRun_BabysitGuardAllowsWhenNotBlocking(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "782-implement", Status: "done"},
	}}
	killer := &fakeKiller{}
	fg := &fakeGuard{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "782",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
		Guard:        fg.Guard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != closecmd.ActionClosed {
		t.Fatalf("decisions = %+v, want the window closed when the guard does not block", decisions)
	}
	if len(killer.killed) != 1 {
		t.Errorf("killed = %v, want the window killed", killer.killed)
	}
}

// TestRun_NilGuardPreservesExistingBehavior pins the "machines that never use
// babysit are unaffected" contract (#787): with no guard wired, an idle
// window closes and no guard-shaped decision is ever produced.
func TestRun_NilGuardPreservesExistingBehavior(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "782-implement", Status: "done"},
	}}
	killer := &fakeKiller{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "782",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != closecmd.ActionClosed {
		t.Fatalf("decisions = %+v, want the window closed with no guard wired", decisions)
	}
	if len(killer.killed) != 1 {
		t.Errorf("killed = %v, want the window killed", killer.killed)
	}
}

// TestRun_ForceBypassesBabysitGuard covers #787 AC2: --force closes a
// guard-blocked window exactly as it bypasses the busy skip, and never even
// consults the guard.
func TestRun_ForceBypassesBabysitGuard(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "782-implement", Status: "done"},
	}}
	killer := &fakeKiller{}
	fg := &fakeGuard{blocked: map[string]string{"782": "babysit supervising PR #790, CI not green"}}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "782",
		Force:        true,
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
		Guard:        fg.Guard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(killer.killed) != 1 {
		t.Fatalf("killed = %v, want the window killed with --force despite the guard", killer.killed)
	}
	if len(decisions) != 1 || decisions[0].Action != closecmd.ActionClosed {
		t.Errorf("decisions = %+v, want closed", decisions)
	}
	if len(fg.asked) != 0 {
		t.Errorf("guard asked about %v, want --force to skip the guard entirely", fg.asked)
	}
}

// TestRun_DryRunReportsBabysitSkipWithoutSideEffects covers #787 AC3: a dry
// run reports the guard's skip and performs no kill and no registration.
func TestRun_DryRunReportsBabysitSkipWithoutSideEffects(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "782-implement", Status: "done"},
	}}
	killer := &fakeKiller{}
	fr := &fakeRegister{}
	fg := &fakeGuard{blocked: map[string]string{"782": "babysit supervising PR #790, CI not green"}}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "782",
		DryRun:       true,
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
		Register:     fr.Register,
		Guard:        fg.Guard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != closecmd.ActionSkippedBabysit {
		t.Fatalf("decisions = %+v, want skipped-babysit under --dry-run", decisions)
	}
	if len(killer.killed) != 0 || len(fr.registered) != 0 {
		t.Errorf("killed = %v, registered = %+v, want no side effects under --dry-run", killer.killed, fr.registered)
	}
}

// TestRun_BusySkipTakesPrecedenceOverBabysitGuard pins the branch order: a
// busy window is still reported (and registered) as a busy skip, so the
// daemon's own deferred close — which re-checks the guard itself — stays in
// charge of that window.
func TestRun_BusySkipTakesPrecedenceOverBabysitGuard(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "782-implement", Status: "running"},
	}}
	fr := &fakeRegister{}
	fg := &fakeGuard{blocked: map[string]string{"782": "babysit supervising PR #790, CI not green"}}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "782",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       &fakeKiller{},
		Register:     fr.Register,
		Guard:        fg.Guard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != closecmd.ActionSkippedBusy {
		t.Fatalf("decisions = %+v, want skipped-busy to win for a running window", decisions)
	}
	if len(fr.registered) != 1 {
		t.Errorf("registered = %+v, want the busy window still registered as pending-close", fr.registered)
	}
}

// TestRun_GuardNotConsultedForSlugWindows asserts the guard is only asked
// about windows that carry a ticket number: a free-text window name has no
// ticket to join a supervisor on, so it closes as before (fail open).
func TestRun_GuardNotConsultedForSlugWindows(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "add-dark-mode", Status: "done"},
	}}
	killer := &fakeKiller{}
	fg := &fakeGuard{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "add-dark-mode",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
		Guard:        fg.Guard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fg.asked) != 0 {
		t.Errorf("guard asked about %v, want no guard call for a window with no ticket number", fg.asked)
	}
	if len(decisions) != 1 || decisions[0].Action != closecmd.ActionClosed || len(killer.killed) != 1 {
		t.Errorf("decisions = %+v, killed = %v, want the slug window closed", decisions, killer.killed)
	}
}

func TestRun_NoMatchesNoKillsNoError(t *testing.T) {
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "0", PaneID: "%0", WindowName: "99-other", Status: "done"},
	}}
	killer := &fakeKiller{}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       "42",
		ReadSnapshot: fakeReadSnapshot(snap, nil),
		Killer:       killer,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(decisions) != 0 {
		t.Errorf("decisions = %+v, want none", decisions)
	}
	if len(killer.killed) != 0 {
		t.Errorf("killer.killed = %v, want none", killer.killed)
	}
}

// -- process-tree kill before kill-window (#1253) ----------------------------
//
// These run closecmd.Run with the real windowproc.Killer, the real
// tmux.ExecClient and fake tmux/docker/podman binaries alone on PATH
// (windowproctest, whose Install verifies the fakes are what PATH resolves
// to). The grace window is 0 unless a test says otherwise.

const (
	ccLaunchID = "0123456789abcdef0123456789abcdef"
	ccTarget   = "%1"
	ccWindow   = "42-implement"
)

// stubbornProcs are two processes that ignore SIGTERM, so a close has to
// escalate to SIGKILL.
func stubbornProcs() []windowproctest.Proc {
	return []windowproctest.Proc{
		{PID: "101", Start: "5001", IgnoreTerm: true},
		{PID: "102", Start: "5002", IgnoreTerm: true},
	}
}

func realProcessKiller() func(string) error {
	k := windowproc.New(io.Discard)
	return func(target string) error { return k.KillWindowProcesses(context.Background(), target) }
}

func closeSnapshot(status string) *watch.StateSnapshot {
	return &watch.StateSnapshot{Windows: []watch.WindowState{
		{Session: "s1", WindowIndex: "1", PaneID: "%1", WindowName: ccWindow, Status: status},
	}}
}

func realCloseOpts(snap *watch.StateSnapshot) closecmd.Opts {
	return closecmd.Opts{
		Target:        "42",
		ReadSnapshot:  fakeReadSnapshot(snap, nil),
		Killer:        &tmux.ExecClient{},
		KillProcesses: realProcessKiller(),
	}
}

func requireClosedOnce(t *testing.T, decisions []closecmd.Decision) {
	t.Helper()
	if len(decisions) != 1 || decisions[0].Action != closecmd.ActionClosed {
		t.Fatalf("decisions = %+v, want exactly one closed window", decisions)
	}
}

// AC10 + AC15: closing a sandbox window TERMs, then KILLs, every process of the
// pane's launch -- through the runtime the pane recorded, for Claude and Codex
// containers -- and only then kills the window. The whole call log is asserted
// line by line: the listing with the exact format, the -u dev probe with the ID
// as argv, -u root signals, the liveness checks, the bounded re-list, and
// kill-window last.
func TestRun_SandboxWindowLaunchProcessesAreTermedThenKilledBeforeKillWindow(t *testing.T) {
	for _, f := range windowproctest.Flavors {
		t.Run(f.Name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
			fakes := windowproctest.Install(t, f.SandboxSpec(ccLaunchID, windowproctest.HarmlessPanePID(t), stubbornProcs()...))

			decisions, err := closecmd.Run(realCloseOpts(closeSnapshot("done")))

			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			requireClosedOnce(t, decisions)
			want := windowproctest.SandboxCloseCalls(f.Runtime, f.Container, ccLaunchID, ccTarget, "101", "102")
			if got := fakes.Calls(t); !slices.Equal(got, want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// AC12: a host pane's process group is TERMed before kill-window, a member of
// that group dies with it, and a process under Setsid (the babysit supervisor
// shape) survives. The fake kill-window records whether the group's processes
// were already gone when it ran.
func TestRun_HostWindowProcessGroupIsTermedBeforeKillWindowAndSetsidProcessSurvives(t *testing.T) {
	leader, member, survivor := windowproctest.StartCooperativeGroup(t)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "5")
	fakes := windowproctest.Install(t, windowproctest.Spec{
		Panes:                 []windowproctest.Pane{{ID: "%1", PanePID: fmt.Sprint(leader.PID)}},
		ProbePIDsAtKillWindow: []string{fmt.Sprint(leader.PID), fmt.Sprint(member.PID)},
	})

	decisions, err := closecmd.Run(realCloseOpts(closeSnapshot("done")))

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireClosedOnce(t, decisions)
	leader.RequireDeathBy(t, "group leader", syscall.SIGTERM)
	member.RequireDeathBy(t, "group member", syscall.SIGTERM)
	survivor.RequireAlive(t, "Setsid process")
	want := append(windowproctest.ListCalls(ccTarget), []string{
		"tmux kill-window -t " + ccTarget,
		fmt.Sprintf("tmux kill-window-check %d gone", leader.PID),
		fmt.Sprintf("tmux kill-window-check %d gone", member.PID),
	}...)
	if got := fakes.Calls(t); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// AC12: a group that ignores SIGTERM is KILLed, and the window is still closed.
func TestRun_HostWindowProcessGroupIgnoringTermIsKilledThenWindowClosed(t *testing.T) {
	leader, member := windowproctest.StartTermIgnoringGroup(t)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	fakes := windowproctest.Install(t, windowproctest.Spec{
		Panes: []windowproctest.Pane{{ID: "%1", PanePID: fmt.Sprint(leader.PID)}},
	})

	decisions, err := closecmd.Run(realCloseOpts(closeSnapshot("done")))

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireClosedOnce(t, decisions)
	leader.RequireDeathBy(t, "TERM-ignoring group leader", syscall.SIGKILL)
	member.RequireDeathBy(t, "group member", syscall.SIGTERM, syscall.SIGKILL)
	want := append(windowproctest.ListCalls(ccTarget), []string{
		"tmux kill-window -t " + ccTarget,
	}...)
	if got := fakes.Calls(t); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// AC13: a failed process kill (the container is gone) never strands the window:
// kill-window still runs and the window counts as closed, but Run returns the
// failure -- naming the window and carrying the runtime's own message -- so
// `cenci close` exits non-zero.
func TestRun_FailedProcessKillStillClosesWindowAndReportsTheError(t *testing.T) {
	for _, f := range windowproctest.Flavors {
		t.Run(f.Name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
			spec := f.SandboxSpec(ccLaunchID, windowproctest.HarmlessPanePID(t), stubbornProcs()...)
			spec.ProbeFails = true
			fakes := windowproctest.Install(t, spec)

			decisions, err := closecmd.Run(realCloseOpts(closeSnapshot("done")))

			if err == nil {
				t.Fatal("Run err = nil, want the failed process kill reported")
			}
			for _, want := range []string{"killing processes of " + ccWindow + " (" + ccTarget + ")", windowproctest.TransportMarker} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Run err = %q, want it to contain %q", err, want)
				}
			}
			requireClosedOnce(t, decisions)
			calls := fakes.Calls(t)
			if len(calls) == 0 || calls[len(calls)-1] != "tmux kill-window -t "+ccTarget {
				t.Errorf("calls:\n%s\nwant kill-window to still run last", strings.Join(calls, "\n"))
			}
			if slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, "kill -") }) {
				t.Errorf("a signal was sent although the probe failed; calls:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// AC13: a process-kill failure of any kind lets kill-window run. Recorded in
// one shared log so the order is part of the assertion.
func TestRun_KillProcessesErrorDoesNotSkipKillWindow(t *testing.T) {
	var log []string
	opts := closecmd.Opts{
		Target:       "42",
		ReadSnapshot: fakeReadSnapshot(closeSnapshot("done"), nil),
		Killer:       &logKiller{log: &log},
		KillProcesses: func(target string) error {
			log = append(log, "kill-processes "+target)
			return errors.New("exec transport broke")
		},
	}

	decisions, err := closecmd.Run(opts)

	if err == nil || !strings.Contains(err.Error(), "exec transport broke") {
		t.Fatalf("Run err = %v, want the process-kill error", err)
	}
	requireClosedOnce(t, decisions)
	want := []string{"kill-processes " + ccTarget, "kill-window " + ccTarget}
	if !slices.Equal(log, want) {
		t.Errorf("order = %v, want %v", log, want)
	}
}

// logKiller records kill-window calls into a log shared with other fakes.
type logKiller struct {
	log *[]string
	err error
}

func (k *logKiller) KillWindow(target string) error {
	*k.log = append(*k.log, "kill-window "+target)
	return k.err
}

// AC13: if kill-window fails because the window already closed itself after its
// agent exited, that counts as closed -- no error -- whether the window was
// already gone at the pane listing or vanished between the listing and the kill.
func TestRun_WindowAlreadyGoneCountsAsClosedNotAnError(t *testing.T) {
	for _, f := range windowproctest.Flavors {
		t.Run(f.Name+"/vanished before kill-window", func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
			spec := f.SandboxSpec(ccLaunchID, windowproctest.HarmlessPanePID(t), stubbornProcs()...)
			spec.KillWindowGone = true
			windowproctest.Install(t, spec)

			decisions, err := closecmd.Run(realCloseOpts(closeSnapshot("done")))

			if err != nil {
				t.Fatalf("Run err = %v, want nil for a window that already closed itself", err)
			}
			requireClosedOnce(t, decisions)
		})
		t.Run(f.Name+"/gone at the pane listing", func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
			spec := f.SandboxSpec(ccLaunchID, windowproctest.HarmlessPanePID(t), stubbornProcs()...)
			spec.ListGone = true
			spec.KillWindowGone = true
			fakes := windowproctest.Install(t, spec)

			decisions, err := closecmd.Run(realCloseOpts(closeSnapshot("done")))

			if err != nil {
				t.Fatalf("Run err = %v, want nil for a window that is already gone", err)
			}
			requireClosedOnce(t, decisions)
			if calls := fakes.Calls(t); slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, " exec ") }) {
				t.Errorf("a runtime exec was issued for a gone window; calls:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// AC13 guard: only a missing-window message counts as closed. Any other
// kill-window failure is still a reported error, with no closed decision.
func TestRun_KillWindowFailureOtherThanGoneIsStillAnError(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	spec := windowproctest.Flavors[0].SandboxSpec(ccLaunchID, windowproctest.HarmlessPanePID(t), stubbornProcs()...)
	spec.KillWindowFails = true
	fakes := windowproctest.Install(t, spec)

	decisions, err := closecmd.Run(realCloseOpts(closeSnapshot("done")))

	if err == nil || !strings.Contains(err.Error(), windowproctest.TmuxUnrelatedErrorText) {
		t.Fatalf("Run err = %v, want the tmux failure %q", err, windowproctest.TmuxUnrelatedErrorText)
	}
	if len(decisions) != 0 {
		t.Errorf("decisions = %+v, want none for a window that could not be killed", decisions)
	}
	// The processes were still killed first; only the window kill failed.
	want := windowproctest.SandboxCloseCalls(windowproctest.Flavors[0].Runtime, windowproctest.Flavors[0].Container, ccLaunchID, ccTarget, "101", "102")
	if got := fakes.Calls(t); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// AC14: --dry-run, a busy skip and a babysit skip send no signal and read no
// pane at all -- not a single tmux or runtime call -- and their decisions are
// exactly what they were before the process kill existed.
func TestRun_DryRunAndSkippedWindowsNeverTouchProcesses(t *testing.T) {
	const guardReason = "babysit supervising PR #790, CI not green"
	tests := []struct {
		name   string
		status string
		mutate func(*closecmd.Opts)
		want   closecmd.Decision
	}{
		{"dry run", "done", func(o *closecmd.Opts) { o.DryRun = true }, closecmd.Decision{Action: closecmd.ActionWouldClose}},
		{"busy window", "running", func(o *closecmd.Opts) {}, closecmd.Decision{Action: closecmd.ActionSkippedBusy}},
		{"dry run of a busy window", "need-input", func(o *closecmd.Opts) { o.DryRun = true }, closecmd.Decision{Action: closecmd.ActionSkippedBusy}},
		{"babysit guard", "done", func(o *closecmd.Opts) {
			fg := &fakeGuard{blocked: map[string]string{"42": guardReason}}
			o.Guard = fg.Guard
		}, closecmd.Decision{Action: closecmd.ActionSkippedBabysit, Reason: guardReason}},
	}
	for _, f := range windowproctest.Flavors[:1] {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
				fakes := windowproctest.Install(t, f.SandboxSpec(ccLaunchID, windowproctest.HarmlessPanePID(t), stubbornProcs()...))
				snap := closeSnapshot(tt.status)
				var killProcessCalls []string
				real := realProcessKiller()
				opts := realCloseOpts(snap)
				opts.KillProcesses = func(target string) error {
					killProcessCalls = append(killProcessCalls, target)
					return real(target)
				}
				tt.mutate(&opts)

				decisions, err := closecmd.Run(opts)

				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				want := tt.want
				want.Window = snap.Windows[0]
				if len(decisions) != 1 || decisions[0] != want {
					t.Errorf("decisions = %+v, want [%+v]", decisions, want)
				}
				if len(killProcessCalls) != 0 {
					t.Errorf("KillProcesses called for %v, want no call", killProcessCalls)
				}
				if calls := fakes.Calls(t); len(calls) != 0 {
					t.Errorf("calls:\n%s\nwant no tmux or runtime call at all", strings.Join(calls, "\n"))
				}

				// Control: the same wiring does read the window's panes on a real
				// close, so the empty log above cannot be an unwired call log.
				if _, err := closecmd.Run(realCloseOpts(closeSnapshot("done"))); err != nil {
					t.Fatalf("control close: %v", err)
				}
				control := fakes.Calls(t)
				if len(control) == 0 || !strings.HasPrefix(control[0], "tmux list-panes -t "+ccTarget) {
					t.Errorf("control close calls = %q, want it to start with the pane listing", control)
				}
			})
		}
	}
}
