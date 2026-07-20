package pipeline

// File-locking for concurrent pipeline invocations (ticket #558, Q&A #4):
// flock(LOCK_EX|LOCK_NB) on a sibling ".lock" file, retried on EWOULDBLOCK
// through the generic retryDo backoff helper, auto-released on process exit
// (or explicitly, on fn returning). Chosen over an O_CREATE|O_EXCL lock file
// (babysit's style) specifically because flock can't strand a lock on a
// crash, which matters for this short-lived command.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrLockContention is returned when withLock exhausts its retry budget
// without acquiring the lock (detectable via errors.Is, rule #412).
var ErrLockContention = errors.New("lock contention")

// flockFn is the flock syscall seam: production code calls the real
// syscall.Flock, tests override it to script a deterministic non-EWOULDBLOCK
// errno so the "not every acquisition failure is contention" branch below
// can be exercised without depending on real kernel lock behavior.
var flockFn = syscall.Flock

// withLock acquires an exclusive flock on lockPath (creating it if needed),
// retried via cfg on EWOULDBLOCK, then calls fn while holding the lock. The
// lock is released before withLock returns, regardless of fn's outcome. fn's
// own error is returned unwrapped — never masqueraded as ErrLockContention
// (rule #446) — so callers can distinguish "we never got the lock" from "we
// got the lock but the operation itself failed".
func withLock(lockPath string, cfg retryConfig, fn func() error) error {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()

	acquireErr := retryDo(func() error {
		if flockErr := flockFn(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
			return fmt.Errorf("flock %s: %w", lockPath, flockErr)
		}
		return nil
	}, func(err error) bool {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}, cfg)

	if acquireErr != nil {
		// Only an EWOULDBLOCK-exhaustion failure is genuine lock contention
		// (rule #446, content-specific classification): any other
		// acquisition failure (e.g. an unexpected flock errno) must
		// propagate under its own wrapping, not masquerade as
		// ErrLockContention, or callers doing errors.Is(err,
		// ErrLockContention) would misclassify it.
		if errors.Is(acquireErr, syscall.EWOULDBLOCK) {
			return fmt.Errorf("%w: could not acquire lock %s: %v", ErrLockContention, lockPath, acquireErr)
		}
		return acquireErr
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}
