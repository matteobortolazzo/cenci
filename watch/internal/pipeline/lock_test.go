package pipeline

// Integration tests for withLock (ticket #558): a flock-based, auto-release
// exclusive lock retried through the retryDo backoff helper on EWOULDBLOCK
// (Q&A #4: flock over an O_CREATE|O_EXCL lock file, since flock releases
// automatically on process exit and this command is short-lived). The
// concurrent-writer test asserts on outcome (no corruption, contention was
// retried) via the injected clock rather than wall-clock timing, per the
// plan's Risks section. In-package ("white box") test file, matching
// internal/babysit/babysit_test.go's own convention.

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// noSleepConfig returns a retryConfig with a very large attempt budget and
// a no-op Sleep, so the flock retry loop can spin through genuine EWOULDBLOCK
// contention almost instantly instead of depending on real elapsed time.
func noSleepConfig(sleptCount *int64) retryConfig {
	return retryConfig{
		Attempts: 1_000_000,
		Base:     time.Microsecond,
		Max:      time.Microsecond,
		Sleep: func(time.Duration) {
			if sleptCount != nil {
				atomic.AddInt64(sleptCount, 1)
			}
		},
	}
}

// TestWithLock_ConcurrentWriters_NoCorruptionAndContentionRetried is the
// AC's core concurrency assertion: many goroutines racing a
// load-increment-save cycle through withLock on the same state file must
// never lose an update (flock genuinely serializes them), and at least one
// of them must have observed real contention (proven by the injected Sleep
// having been invoked, not by timing).
func TestWithLock_ConcurrentWriters_NoCorruptionAndContentionRetried(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "42.json")
	lockPath := statePath + ".lock"

	if err := saveState(statePath, State{SchemaVersion: CurrentSchemaVersion, ID: "42", Stage: StageNew}); err != nil {
		t.Fatalf("seed saveState: %v", err)
	}

	const writers = 20
	var slept int64
	cfg := noSleepConfig(&slept)

	// counter is a plain (non-atomic) int, deliberately: the whole point of
	// this test is that withLock's mutual exclusion is what keeps a
	// read-increment-write sequence race-free. If flock genuinely
	// serializes every critical section, incrementing this from inside
	// withLock's fn is safe and every increment is preserved (proving "no
	// corruption" / no lost updates); a broken lock would either lose
	// increments (final count < writers) or trip `go test -race`.
	counter := 0

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // maximize overlap so genuine flock contention is likely
			err := withLock(lockPath, cfg, func() error {
				counter++
				s, err := loadState(statePath)
				if err != nil {
					return err
				}
				s.SchemaVersion = CurrentSchemaVersion
				s.ID = "42"
				return saveState(statePath, s)
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("withLock: unexpected error from a writer: %v", err)
		}
	}

	if counter != writers {
		t.Errorf("counter = %d, want %d (a lost increment means withLock failed to serialize concurrent writers)", counter, writers)
	}

	// No corruption: the file on disk must still be valid, loadable JSON
	// after N concurrent writers all held the lock in turn.
	final, err := loadState(statePath)
	if err != nil {
		t.Fatalf("loadState after concurrent writers: %v (file corrupted)", err)
	}
	if final.ID != "42" {
		t.Errorf("final state ID = %q, want %q (file corrupted or overwritten with wrong data)", final.ID, "42")
	}

	if atomic.LoadInt64(&slept) == 0 {
		t.Error("expected at least one retry (Sleep call) from genuine flock contention among 20 concurrent writers, got none")
	}
}

// TestWithLock_ExhaustsAttempts_ReturnsErrLockContention is the #412 direct
// sentinel test: hold the lock externally (simulating another process) and
// confirm withLock surfaces the detectable ErrLockContention sentinel
// after exhausting its retry budget, and never invokes fn.
func TestWithLock_ExhaustsAttempts_ReturnsErrLockContention(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "42.json.lock")

	holder, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", lockPath, err)
	}
	defer func() { _ = holder.Close() }()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock %s: %v", lockPath, err)
	}

	fnCalls := 0
	cfg := retryConfig{Attempts: 3, Base: time.Microsecond, Max: time.Microsecond, Sleep: func(time.Duration) {}}
	err = withLock(lockPath, cfg, func() error {
		fnCalls++
		return nil
	})

	if err == nil {
		t.Fatal("withLock: want a lock-contention error while externally held, got nil")
	}
	if !errors.Is(err, ErrLockContention) {
		t.Errorf("withLock error = %v, want errors.Is(_, ErrLockContention)", err)
	}
	if fnCalls != 0 {
		t.Errorf("fn was called %d times, want 0 (the lock was never actually acquired)", fnCalls)
	}
}

// TestWithLock_NonEWouldBlockFlockFailure_NotReportedAsLockContention is the
// silent-failure-hunter regression test (#558 review): a genuine,
// non-EWOULDBLOCK flock() errno (e.g. EBADF from a corrupt/unexpected fd
// state) must propagate under its own wrapping, never masquerade as
// ErrLockContention — otherwise a caller doing errors.Is(err,
// ErrLockContention) would misclassify a real acquisition failure as mere
// contention. Uses the flockFn seam to script a deterministic non-EWOULDBLOCK
// errno rather than depending on real kernel lock behavior.
func TestWithLock_NonEWouldBlockFlockFailure_NotReportedAsLockContention(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "42.json.lock")
	cfg := retryConfig{Attempts: 3, Base: time.Microsecond, Max: time.Microsecond, Sleep: func(time.Duration) {}}

	original := flockFn
	defer func() { flockFn = original }()
	flockFn = func(int, int) error { return syscall.EBADF }

	fnCalls := 0
	err := withLock(lockPath, cfg, func() error {
		fnCalls++
		return nil
	})

	if err == nil {
		t.Fatal("withLock: want an error for a non-EWOULDBLOCK flock failure, got nil")
	}
	if errors.Is(err, ErrLockContention) {
		t.Errorf("withLock error = %v, must NOT satisfy errors.Is(_, ErrLockContention) for a non-EWOULDBLOCK flock failure", err)
	}
	if !errors.Is(err, syscall.EBADF) {
		t.Errorf("withLock error = %v, want errors.Is(_, syscall.EBADF)", err)
	}
	if fnCalls != 0 {
		t.Errorf("fn was called %d times, want 0 (the lock was never actually acquired)", fnCalls)
	}
}

// TestWithLock_OpenFileFailure_NotReportedAsLockContention covers the other
// "not actually contention" acquisition failure named in the #558 review: a
// bad/unreadable lock path that fails at os.OpenFile, before the flock retry
// loop is ever entered.
func TestWithLock_OpenFileFailure_NotReportedAsLockContention(t *testing.T) {
	// A lock path under a nonexistent parent directory makes os.OpenFile
	// fail deterministically (ENOENT), independent of the flock/retry loop.
	lockPath := filepath.Join(t.TempDir(), "does-not-exist", "42.json.lock")
	cfg := retryConfig{Attempts: 3, Base: time.Microsecond, Max: time.Microsecond, Sleep: func(time.Duration) {}}

	fnCalls := 0
	err := withLock(lockPath, cfg, func() error {
		fnCalls++
		return nil
	})

	if err == nil {
		t.Fatal("withLock: want an error for a bad lock path, got nil")
	}
	if errors.Is(err, ErrLockContention) {
		t.Errorf("withLock error = %v, must NOT satisfy errors.Is(_, ErrLockContention) for an open-file failure", err)
	}
	if fnCalls != 0 {
		t.Errorf("fn was called %d times, want 0 (the lock was never actually acquired)", fnCalls)
	}
}

// TestWithLock_ReleasesOnSuccess_AllowsImmediateSubsequentAcquire proves the
// lock is actually released after fn returns (auto-release, not leaked),
// so a second, independent withLock call on the same path never contends.
func TestWithLock_ReleasesOnSuccess_AllowsImmediateSubsequentAcquire(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "42.json.lock")
	cfg := retryConfig{Attempts: 3, Base: time.Microsecond, Max: time.Microsecond, Sleep: func(time.Duration) {}}

	if err := withLock(lockPath, cfg, func() error { return nil }); err != nil {
		t.Fatalf("first withLock: %v", err)
	}

	var slept int
	cfg2 := retryConfig{Attempts: 3, Base: time.Microsecond, Max: time.Microsecond, Sleep: func(time.Duration) { slept++ }}
	if err := withLock(lockPath, cfg2, func() error { return nil }); err != nil {
		t.Fatalf("second withLock: %v", err)
	}
	if slept != 0 {
		t.Errorf("second withLock slept %d times, want 0 (the first call must have released the lock)", slept)
	}
}

// TestWithLock_FnErrorIsNotWrappedAsLockContention distinguishes "we
// acquired the lock but the wrapped operation failed" from "we could not
// acquire the lock" — content-specific per rule #446: a saveState failure
// inside fn must propagate as-is, never masquerade as ErrLockContention.
func TestWithLock_FnErrorIsNotWrappedAsLockContention(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "42.json.lock")
	cfg := retryConfig{Attempts: 3, Base: time.Microsecond, Max: time.Microsecond, Sleep: func(time.Duration) {}}

	sentinel := errors.New("simulated fn failure, unrelated to locking")
	err := withLock(lockPath, cfg, func() error { return sentinel })

	if !errors.Is(err, sentinel) {
		t.Fatalf("withLock error = %v, want errors.Is(_, sentinel) (fn's own error, unwrapped)", err)
	}
	if errors.Is(err, ErrLockContention) {
		t.Errorf("withLock error = %v, must NOT satisfy errors.Is(_, ErrLockContention) for a plain fn failure", err)
	}
}
