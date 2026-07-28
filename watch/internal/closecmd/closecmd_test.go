package closecmd_test

import (
	"errors"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/closecmd"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
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
		{Session: "s1", WindowIndex: "0", WindowName: "42", Status: "done"},
		{Session: "s1", WindowIndex: "1", WindowName: "42-refine", Status: "done"},
		{Session: "s1", WindowIndex: "2", WindowName: "420-x", Status: "done"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "my-window", Status: "done"},
		{Session: "s1", WindowIndex: "1", WindowName: "my-window-2", Status: "done"},
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
		{Session: "sess-a", WindowIndex: "3", WindowName: "42-implement", Status: "done"},
		{Session: "sess-b", WindowIndex: "1", WindowName: "42-review", Status: "idle"},
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
	wantTargets := map[string]bool{"=sess-a:3": true, "=sess-b:1": true}
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
		{Session: "s1", WindowIndex: "0", WindowName: "42-a", Status: "running"},
		{Session: "s1", WindowIndex: "1", WindowName: "42-b", Status: "need-input"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "42-a", Status: "running"},
		{Session: "s1", WindowIndex: "1", WindowName: "42-b", Status: "need-input"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "42-a", Status: "running"},
		{Session: "s1", WindowIndex: "1", WindowName: "42-b", Status: "need-input"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "42-a", Status: "done"},
		{Session: "s1", WindowIndex: "1", WindowName: "42-b", Status: "running"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "42-a", Status: "running"},
		{Session: "s1", WindowIndex: "1", WindowName: "42-b", Status: "need-input"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "782-implement", Status: "done"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "782-implement", Status: "done"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "782-implement", Status: "done"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "782-implement", Status: "done"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "782-implement", Status: "done"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "782-implement", Status: "running"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "add-dark-mode", Status: "done"},
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
		{Session: "s1", WindowIndex: "0", WindowName: "99-other", Status: "done"},
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
