package closecmd_test

import (
	"errors"
	"testing"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/closecmd"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/pkg/watch"
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
