package reap

import (
	"errors"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// TestExecReaper_ReapReturnsImmediately asserts Reap() does not block the
// caller while a run is in flight (#292: non-blocking for the event loop).
func TestExecReaper_ReapReturnsImmediately(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	r := &ExecReaper{run: func() error {
		started <- struct{}{}
		<-release
		return nil
	}}
	defer close(release)

	callDone := make(chan struct{})
	go func() {
		r.Reap()
		close(callDone)
	}()

	select {
	case <-callDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Reap() blocked instead of returning immediately")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expected the injected run to be invoked in the background")
	}
}

// TestExecReaper_CoalescesConcurrentReap asserts a second Reap() call while
// one is already in flight is a no-op (single-flight, no pile-up).
func TestExecReaper_CoalescesConcurrentReap(t *testing.T) {
	release := make(chan struct{})
	calls := make(chan struct{}, 10)
	r := &ExecReaper{run: func() error {
		calls <- struct{}{}
		<-release
		return nil
	}}

	r.Reap()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("expected first Reap() to invoke run")
	}

	// A second Reap() while the first is still running must be coalesced —
	// the underlying run must NOT be invoked a second time.
	r.Reap()
	select {
	case <-calls:
		t.Fatal("expected concurrent Reap() to be coalesced, but run was invoked again")
	case <-time.After(100 * time.Millisecond):
		// Expected: no second invocation observed in the window.
	}

	close(release)
}

// TestExecReaper_GuardResetsAfterCompletion asserts that once a run
// completes, a later Reap() triggers a new run (the single-flight guard is
// not stuck permanently held).
func TestExecReaper_GuardResetsAfterCompletion(t *testing.T) {
	calls := make(chan struct{}, 10)
	r := &ExecReaper{run: func() error {
		calls <- struct{}{}
		return nil
	}}

	r.Reap()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("expected first Reap() to invoke run")
	}

	// Wait for the guard to reset after the (fast, already-returned) run.
	deadline := time.Now().Add(time.Second)
	for r.running.Load() {
		if time.Now().After(deadline) {
			t.Fatal("single-flight guard never reset after run completed")
		}
		time.Sleep(time.Millisecond)
	}

	r.Reap()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("expected a later Reap() to trigger a new run after the guard reset")
	}
}

// TestExecReaper_MissingBinaryNonFatal asserts that a run failing the way an
// unresolvable self binary would (an *exec.Error wrapping exec.ErrNotFound)
// is swallowed: no panic, and the guard still resets for future calls.
func TestExecReaper_MissingBinaryNonFatal(t *testing.T) {
	calls := make(chan struct{}, 10)
	r := &ExecReaper{run: func() error {
		calls <- struct{}{}
		return &exec.Error{Name: "cenci", Err: exec.ErrNotFound}
	}}

	r.Reap()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("expected Reap() to invoke run despite the missing-binary error")
	}

	// Non-fatal: the guard must still reset so a later Reap() works normally.
	deadline := time.Now().Add(time.Second)
	for r.running.Load() {
		if time.Now().After(deadline) {
			t.Fatal("single-flight guard never reset after missing-binary error")
		}
		time.Sleep(time.Millisecond)
	}
	r.Reap()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("expected a later Reap() to still work after a missing-binary error")
	}
}

// TestExecReaper_RunErrorNotRetriedAutomatically asserts that a run
// returning a generic error (e.g. `cenci sandbox reap-orphans` exiting
// non-zero) is logged and dropped — NOT retried in a tight loop by the
// reaper itself.
func TestExecReaper_RunErrorNotRetriedAutomatically(t *testing.T) {
	var calls int32
	invoked := make(chan struct{})
	r := &ExecReaper{run: func() error {
		atomic.AddInt32(&calls, 1)
		close(invoked)
		return errors.New("cenci sandbox reap-orphans exited with status 1")
	}}

	r.Reap()
	select {
	case <-invoked:
	case <-time.After(time.Second):
		t.Fatal("expected Reap() to invoke run")
	}

	// Give any errant automatic retry a chance to fire, then assert it didn't.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 run invocation (no automatic retry), got %d", got)
	}
}

// TestNewExecReaper_WiresVerboseAndDefaultRun asserts the constructor stores
// the verbose flag and installs a default run implementation, without
// actually invoking it (never shell out in tests).
func TestNewExecReaper_WiresVerboseAndDefaultRun(t *testing.T) {
	r := NewExecReaper(true)
	if r == nil {
		t.Fatal("expected NewExecReaper to return a non-nil *ExecReaper")
	}
	if !r.verbose {
		t.Error("expected verbose=true to be retained")
	}
	if r.run == nil {
		t.Error("expected a default run implementation to be installed")
	}
}

// TestExecReaper_ImplementsReaper is a compile-time-ish assertion that
// *ExecReaper satisfies the Reaper seam the daemon injects.
func TestExecReaper_ImplementsReaper(t *testing.T) {
	var _ Reaper = (*ExecReaper)(nil)
}
