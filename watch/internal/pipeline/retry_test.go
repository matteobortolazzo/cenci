package pipeline

// Unit tests for the generic retry/backoff helper and the one real op it
// backs in this ticket: prepare's `gh issue view <id>` call (ticket #558,
// Q&A #2). Deterministic via an injected clock (retryConfig.Sleep), never
// wall-clock timing. The `command` seam mirrors internal/babysit's `var
// command = func(...)` gh/git test seam. Content-specific error assertions
// distinguish lock-contention from gh-transient failure per watch/AGENTS.md
// rule #446 — not just empty/non-empty checks. In-package ("white box")
// test file, matching internal/babysit/babysit_test.go's own convention.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// -- deterministic backoff -------------------------------------------------

// TestRetryDo_DeterministicBackoff_DoublesUntilExhausted locks in the
// backoff shape (fixed base delay, exponential doubling, capped attempts)
// via an injected Sleep, asserting the exact recorded delay sequence rather
// than depending on real elapsed wall-clock time.
func TestRetryDo_DeterministicBackoff_DoublesUntilExhausted(t *testing.T) {
	var slept []time.Duration
	cfg := retryConfig{
		Attempts: 5,
		Base:     100 * time.Millisecond,
		Max:      1 * time.Second,
		Sleep:    func(d time.Duration) { slept = append(slept, d) },
	}
	attempts := 0
	alwaysTransient := errors.New("simulated transient failure")
	err := retryDo(func() error {
		attempts++
		return alwaysTransient
	}, func(error) bool { return true }, cfg)

	if !errors.Is(err, alwaysTransient) {
		t.Fatalf("retryDo final error = %v, want the last underlying error (not swallowed)", err)
	}
	if attempts != cfg.Attempts {
		t.Errorf("attempts = %d, want %d (cfg.Attempts)", attempts, cfg.Attempts)
	}
	// One fewer sleep than attempts: no sleep follows the final attempt.
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}
	if len(slept) != len(want) {
		t.Fatalf("slept = %v, want %v", slept, want)
	}
	for i, d := range want {
		if slept[i] != d {
			t.Errorf("slept[%d] = %v, want %v", i, slept[i], d)
		}
	}
}

// TestRetryDo_SucceedsBeforeExhaustingAttempts_StopsRetrying asserts that a
// success on a later attempt short-circuits: no further sleeps, no error.
func TestRetryDo_SucceedsBeforeExhaustingAttempts_StopsRetrying(t *testing.T) {
	var slept []time.Duration
	cfg := retryConfig{
		Attempts: 5,
		Base:     10 * time.Millisecond,
		Max:      time.Second,
		Sleep:    func(d time.Duration) { slept = append(slept, d) },
	}
	attempts := 0
	err := retryDo(func() error {
		attempts++
		if attempts < 3 {
			return errors.New("still failing")
		}
		return nil
	}, func(error) bool { return true }, cfg)

	if err != nil {
		t.Fatalf("retryDo: unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (stop as soon as fn succeeds)", attempts)
	}
	if len(slept) != 2 {
		t.Errorf("slept = %v, want 2 sleeps (between the 2 failures and the success)", slept)
	}
}

// TestRetryDo_NonTransientError_StopsImmediately asserts a non-transient
// error is never retried, regardless of remaining attempts budget.
func TestRetryDo_NonTransientError_StopsImmediately(t *testing.T) {
	var slept []time.Duration
	cfg := retryConfig{
		Attempts: 5,
		Base:     10 * time.Millisecond,
		Max:      time.Second,
		Sleep:    func(d time.Duration) { slept = append(slept, d) },
	}
	attempts := 0
	terminal := errors.New("terminal failure")
	err := retryDo(func() error {
		attempts++
		return terminal
	}, func(error) bool { return false }, cfg)

	if !errors.Is(err, terminal) {
		t.Fatalf("retryDo error = %v, want the terminal error unwrapped", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (non-transient error must not be retried)", attempts)
	}
	if len(slept) != 0 {
		t.Errorf("slept = %v, want none (non-transient error must not sleep/retry)", slept)
	}
}

// -- gh issue view: transient-then-success (Q&A #2's real exercised op) --

// TestGhIssueView_TransientThenSuccess_RetriesViaCommandSeam scripts the
// `command` seam to fail with a transient (network-flavored) error twice,
// then succeed, and asserts ghIssueView retries exactly that many times
// before returning the successful output — end-to-end retry over a real
// (faked) `gh` call.
func TestGhIssueView_TransientThenSuccess_RetriesViaCommandSeam(t *testing.T) {
	var calls [][]string
	responses := []struct {
		out []byte
		err error
	}{
		{[]byte("error connecting to api.github.com: timeout"), fmt.Errorf("exit status 1")},
		{[]byte("error connecting to api.github.com: timeout"), fmt.Errorf("exit status 1")},
		{[]byte(`{"number":42,"title":"Change","state":"OPEN"}`), nil},
	}
	original := command
	i := 0
	command = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		r := responses[i]
		i++
		return r.out, r.err
	}
	t.Cleanup(func() { command = original })

	var slept []time.Duration
	cfg := retryConfig{
		Attempts: 5,
		Base:     time.Millisecond,
		Max:      10 * time.Millisecond,
		Sleep:    func(d time.Duration) { slept = append(slept, d) },
	}
	out, err := ghIssueView("42", cfg)
	if err != nil {
		t.Fatalf("ghIssueView: unexpected error: %v", err)
	}
	if !strings.Contains(string(out), `"number":42`) {
		t.Errorf("ghIssueView output = %s, want the final successful gh response", out)
	}
	if len(calls) != 3 {
		t.Fatalf("gh calls = %d, want 3 (2 transient failures + 1 success)", len(calls))
	}
	for _, c := range calls {
		if len(c) < 3 || c[0] != "gh" || c[1] != "issue" || c[2] != "view" {
			t.Errorf("unexpected command invocation: %v, want a `gh issue view ...` call", c)
		}
	}
	if len(slept) != 2 {
		t.Errorf("slept = %v, want 2 (one retry delay per transient failure)", slept)
	}
}

// TestGhIssueView_TicketNotFound_TerminalNoRetry_ReturnsSentinel covers the
// Risks section's warning: "issue not found" must classify as terminal, not
// retryable, and must surface as the ErrTicketNotFound sentinel so the
// state machine layer can map it to the domain-error JSON contract (exit
// 1, errors[] populated) rather than exhausting retries pointlessly.
func TestGhIssueView_TicketNotFound_TerminalNoRetry_ReturnsSentinel(t *testing.T) {
	var calls int
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		calls++
		return []byte("GraphQL: Could not resolve to an Issue with the number of 42. (repository.issue)"), fmt.Errorf("exit status 1")
	}
	t.Cleanup(func() { command = original })

	var slept []time.Duration
	cfg := retryConfig{
		Attempts: 5,
		Base:     time.Millisecond,
		Max:      10 * time.Millisecond,
		Sleep:    func(d time.Duration) { slept = append(slept, d) },
	}
	_, err := ghIssueView("42", cfg)
	if err == nil {
		t.Fatal("ghIssueView: want an error for a not-found ticket, got nil")
	}
	if !errors.Is(err, ErrTicketNotFound) {
		t.Errorf("ghIssueView error = %v, want errors.Is(_, ErrTicketNotFound)", err)
	}
	if calls != 1 {
		t.Errorf("gh calls = %d, want 1 (not-found must not be retried)", calls)
	}
	if len(slept) != 0 {
		t.Errorf("slept = %v, want none (not-found must not be retried)", slept)
	}
}

// -- isTransientGhError classification (rule #446) ------------------------

// TestIsTransientGhError_ClassifiesByContent covers the Risks section's
// requirement that classification be content-specific (network/rate-limit
// == transient; "not found" == terminal), not a blanket "any gh failure is
// retryable" rule.
func TestIsTransientGhError_ClassifiesByContent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"ticket not found is terminal", fmt.Errorf("%w: no such issue", ErrTicketNotFound), false},
		{"network timeout is transient", errors.New("gh issue view 42: error connecting to api.github.com: timeout: exit status 1"), true},
		{"rate limit is transient", errors.New("gh issue view 42: API rate limit exceeded: exit status 1"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isTransientGhError(c.err)
			if got != c.want {
				t.Errorf("isTransientGhError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// -- content-specific error assertions: lock-contention vs. gh-transient --

// TestRetryExhaustion_LockContentionVsGhTransient_AreContentDistinguishable
// is the rule #446 test named directly in the plan: "content-specific error
// assertions distinguish lock-contention vs. gh-transient failure ... not
// just empty/non-empty checks." It forces both failure classes through the
// same generic retryDo/command machinery and asserts their error text is
// distinguishable by content, not merely both non-nil — a regression that
// collapsed both into the same placeholder message would pass a bare
// "err != nil" check but fail these substring assertions.
func TestRetryExhaustion_LockContentionVsGhTransient_AreContentDistinguishable(t *testing.T) {
	// -- gh-transient exhaustion: every attempt fails as transient.
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		return []byte("error connecting to api.github.com: timeout"), fmt.Errorf("exit status 1")
	}
	t.Cleanup(func() { command = original })
	ghCfg := retryConfig{Attempts: 2, Base: time.Millisecond, Max: 2 * time.Millisecond, Sleep: func(time.Duration) {}}
	_, ghErr := ghIssueView("42", ghCfg)
	if ghErr == nil {
		t.Fatal("ghIssueView: want an exhausted-retry error, got nil")
	}

	// -- lock-contention exhaustion: hold a real flock on path ourselves so
	// withLock can never acquire it, forcing genuine EWOULDBLOCK contention.
	dir := t.TempDir()
	lockPath := dir + "/42.json.lock"
	holder, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open %s for locking: %v", lockPath, err)
	}
	defer func() { _ = holder.Close() }()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock %s: %v", lockPath, err)
	}

	lockCfg := retryConfig{Attempts: 2, Base: time.Millisecond, Max: 2 * time.Millisecond, Sleep: func(time.Duration) {}}
	lockErr := withLock(lockPath, lockCfg, func() error { return nil })
	if lockErr == nil {
		t.Fatal("withLock: want a lock-contention error while the lock is externally held, got nil")
	}

	if !errors.Is(lockErr, ErrLockContention) {
		t.Errorf("withLock error = %v, want errors.Is(_, ErrLockContention)", lockErr)
	}
	if errors.Is(ghErr, ErrLockContention) {
		t.Errorf("ghIssueView exhaustion error must not satisfy errors.Is(_, ErrLockContention), got %v", ghErr)
	}

	// Content-specific, not just non-empty: each message must carry its own
	// distinguishing marker and must NOT carry the other failure class's
	// marker, so a regression collapsing both into a shared placeholder
	// string would be caught here.
	if !strings.Contains(lockErr.Error(), "lock") {
		t.Errorf("lock-contention error = %q, want it to mention %q", lockErr.Error(), "lock")
	}
	if strings.Contains(lockErr.Error(), "gh issue view") {
		t.Errorf("lock-contention error = %q, must not mention the unrelated gh op", lockErr.Error())
	}
	if !strings.Contains(ghErr.Error(), "gh issue view") {
		t.Errorf("gh-transient error = %q, want it to mention %q", ghErr.Error(), "gh issue view")
	}
	if strings.Contains(ghErr.Error(), "lock") {
		t.Errorf("gh-transient error = %q, must not mention the unrelated lock op", ghErr.Error())
	}
}
