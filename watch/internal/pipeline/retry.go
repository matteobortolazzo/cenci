package pipeline

// Generic deterministic retry/backoff (ticket #558): a fixed base delay,
// exponential backoff, capped attempts, and an injectable Sleep so tests
// never depend on wall-clock timing. Also hosts the one real op wired
// through it this ticket: prepare's `gh issue view <id>` (Q&A #2), and the
// `command` seam (mirrors internal/babysit's `var command = func(...)` gh
// test seam) both it and the lock retry loop share.

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// retryConfig parameterizes retryDo. Sleep is injectable so tests can assert
// on the exact recorded delay sequence instead of depending on real elapsed
// time.
type retryConfig struct {
	Attempts int
	Base     time.Duration
	Max      time.Duration
	Sleep    func(time.Duration)
}

// defaultRetryConfig is the production backoff policy used by Run(): a
// modest attempt budget with real time.Sleep. Tests construct their own
// retryConfig directly rather than going through this.
func defaultRetryConfig() retryConfig {
	return retryConfig{
		Attempts: 5,
		Base:     500 * time.Millisecond,
		Max:      10 * time.Second,
		Sleep:    time.Sleep,
	}
}

// retryDo calls fn until it succeeds, isTransient(err) says the failure is
// not worth retrying, or cfg.Attempts is exhausted. Delay doubles from
// cfg.Base after each transient failure, capped at cfg.Max; no sleep follows
// the final attempt. The last error is returned unwrapped so errors.Is
// against the caller's own sentinel still works.
func retryDo(fn func() error, isTransient func(error) bool, cfg retryConfig) error {
	var err error
	delay := cfg.Base
	for attempt := 0; attempt < cfg.Attempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !isTransient(err) {
			return err
		}
		if attempt == cfg.Attempts-1 {
			break
		}
		cfg.Sleep(delay)
		delay *= 2
		if delay > cfg.Max {
			delay = cfg.Max
		}
	}
	return err
}

// command is the gh/git test seam: production code shells out for real,
// tests replace it to script deterministic responses.
var command = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// ghIssueView confirms a ticket exists via `gh issue view <id>`, retried
// through cfg for transient (network/rate-limit) failures. A "ticket not
// found" response is classified as terminal and returned as the
// ErrTicketNotFound sentinel without being retried.
func ghIssueView(id string, cfg retryConfig) ([]byte, error) {
	var out []byte
	err := retryDo(func() error {
		var cmdErr error
		out, cmdErr = command("gh", "issue", "view", id, "--json", "number,title,state")
		if cmdErr == nil {
			return nil
		}
		text := strings.TrimSpace(string(out))
		if isNotFoundOutput(text) {
			return fmt.Errorf("gh issue view %s: %w: %s", id, ErrTicketNotFound, text)
		}
		return fmt.Errorf("gh issue view %s: %s: %w", id, text, cmdErr)
	}, isTransientGhError, cfg)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// isNotFoundOutput reports whether gh's output text is its standard
// "no such issue" GraphQL error, distinguishing a terminal domain error from
// a genuinely transient failure.
func isNotFoundOutput(text string) bool {
	return strings.Contains(strings.ToLower(text), "could not resolve")
}

// isTransientGhError classifies a `gh issue view` failure by content (rule
// #446): ticket-not-found is always terminal; network/rate-limit failures
// are transient and worth retrying; anything else is treated as terminal
// (fail fast rather than retry indefinitely on an unrecognized failure).
func isTransientGhError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTicketNotFound) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "error connecting") || strings.Contains(msg, "timeout") || strings.Contains(msg, "rate limit")
}
