package dispatch

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// ghTimeout bounds every individual `gh` invocation execGh makes (#852),
// mirroring gitTimeout's rationale (mainsync.go): a hung network call must
// never stall a dispatch/reconcile pass indefinitely.
const ghTimeout = 60 * time.Second

// ghWaitDelay bounds how long cmd.Wait can block *after* the gh process
// itself has exited or been killed by ghTimeout's context, mirroring
// gitWaitDelay (mainsync.go) -- without it, a grandchild process that
// inherited the stdout/stderr pipes could keep those pipes open and stall
// indefinitely even though gh itself is gone, defeating ghTimeout's
// guarantee.
const ghWaitDelay = 5 * time.Second

// execGh runs `gh <args...>` under a fresh ghTimeout-bounded context per
// call, with cmd.WaitDelay bounding how long Wait can stall on a lingering
// grandchild (watch/AGENTS.md #825: every new internal/dispatch subprocess
// call must mirror mainsync.go's execGit conventions). stdout and stderr
// are captured into separate buffers rather than via CombinedOutput, so a
// benign stderr diagnostic on an otherwise-successful (exit 0) call can
// never get merged into bytes a caller decodes as JSON.
//
// Named execGh (not runGh) to mirror execGit's own naming rationale
// (mainsync.go): consistency with the sibling bounded-exec helper this
// ticket (#852) introduces it alongside.
func execGh(args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.WaitDelay = ghWaitDelay
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// errGhOutputTruncated is execGhBounded's sentinel for a bounded-buffer
// overflow on stdout or stderr (#881, openpr.go's paginated probe): exceeding
// the caller-supplied maxStdout cap is an explicit truncation error, never a
// silently-short body a caller's json.Unmarshal could decode as if it were
// complete (watch/docs/error-handling.md's default-deny rule). Mirrors
// internal/babysit/gh.go:143-204's boundedWriter/errGhOutputTruncated
// pattern.
var errGhOutputTruncated = errors.New("gh output exceeded bounded cap")

// errGhTimeout is execGhBounded's sentinel for a ghTimeout deadline kill or
// cmd.WaitDelay forcing a lingering grandchild's pipes closed -- both are,
// operationally, a hang that had to be bounded, so both classify here
// (mirrors babysit's errGhTimeout).
var errGhTimeout = errors.New("gh invocation timed out")

// boundedGhWriter is an io.Writer that caps how many bytes it will actually
// buffer at max, silently dropping (not erroring) any bytes beyond that cap
// and recording that it did so via truncated -- returning a short write
// count here would make exec.Cmd itself fail with a generic "short write"
// error that loses the distinction between "gh legitimately failed" and
// "gh's output exceeded the bound" (mirrors babysit's boundedWriter).
type boundedGhWriter struct {
	max       int
	buf       bytes.Buffer
	truncated bool
}

func (w *boundedGhWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.buf.Len()
	switch {
	case remaining <= 0:
		w.truncated = true
	case len(p) > remaining:
		w.buf.Write(p[:remaining])
		w.truncated = true
	default:
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *boundedGhWriter) String() string { return w.buf.String() }

// execGhBounded runs `gh <args...>` like execGh -- a fresh ghTimeout-bounded
// context per call, cmd.WaitDelay bounding a lingering grandchild, separate
// stdout/stderr buffers -- but caps each stream at maxStdout bytes and
// classifies the outcome via errGhTimeout/errGhOutputTruncated, both
// detectable with errors.Is (#881, mirroring
// internal/babysit/gh.go:143-204). A sibling of execGh, added alongside it
// without perturbing execGh itself (byte-unchanged): openpr.go's paginated
// probe needs an explicit output cap and classification sentinels execGh's
// existing callers (collectRepoTickets, currentGitHubLogin) do not.
func execGhBounded(maxStdout int, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	return execGhBoundedCtx(ctx, maxStdout, args...)
}

// execGhBoundedCtx is execGhBounded's context-parameterized sibling (#881
// review fix #3): identical bounded-buffer/classification behavior, but the
// caller supplies (and owns cancellation of) ctx instead of getting a fresh
// ghTimeout-bounded one per call. This lets a caller that makes several
// invocations in sequence -- openPRInventory's page loop -- derive each
// call's context from one shared, overarching deadline (via
// context.WithTimeout(sharedCtx, ghTimeout)), so a per-call context.Err()
// reports DeadlineExceeded whether *that call's own* ghTimeout elapsed or
// the shared deadline wrapping the whole traversal did, without this
// function needing to tell the two apart itself.
func execGhBoundedCtx(ctx context.Context, maxStdout int, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.WaitDelay = ghWaitDelay
	outBuf := boundedGhWriter{max: maxStdout}
	errBuf := boundedGhWriter{max: maxStdout}
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	// ctx.Err() is read here, before the caller's own deferred cancel (if
	// any) fires, so a context.DeadlineExceeded cause is still observable --
	// reading it after cancel() runs would always report context.Canceled
	// (cancel's own doing), losing the distinction between "the deadline
	// actually elapsed" and "cleanup ran". A child context derived via
	// context.WithTimeout(parent, ...) also reports DeadlineExceeded here
	// when its *parent's* deadline is what actually fired first (the
	// stdlib's own propagateCancel adopts the parent's Err()), so this stays
	// correct even when ctx is one of several per-call contexts sharing one
	// overarching parent deadline.
	if ctx.Err() == context.DeadlineExceeded {
		runErr = errors.Join(runErr, errGhTimeout)
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		// A lingering grandchild holding the stdout/stderr pipes open forced
		// cmd.Wait to give up after ghWaitDelay -- operationally
		// indistinguishable from a hang that had to be bounded, so this
		// classifies as errGhTimeout too, same as ctx's own deadline kill.
		runErr = errors.Join(runErr, errGhTimeout)
	}
	if outBuf.truncated || errBuf.truncated {
		// errors.Join preserves the real underlying exec error (a nonzero
		// exit, a context-deadline kill, ...) alongside the truncation
		// sentinel instead of discarding it outright -- errors.Is(err,
		// errGhOutputTruncated) still matches (Join's Unwrap walks every
		// joined error).
		return outBuf.String(), errBuf.String(), errors.Join(runErr, errGhOutputTruncated)
	}
	return outBuf.String(), errBuf.String(), runErr
}
