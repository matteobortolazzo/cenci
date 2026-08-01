package babysit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// ghTimeout bounds every individual `gh` invocation execGh makes (#854),
// mirroring internal/dispatch's ghTimeout (dispatch/gh.go) -- a hung network
// call must never stall a babysit tick indefinitely.
const ghTimeout = 60 * time.Second

// ghWaitDelay bounds how long cmd.Wait can block *after* the gh process
// itself has exited or been killed by ghTimeout's context, mirroring
// dispatch's ghWaitDelay -- without it a grandchild process that inherited
// the stdout/stderr pipes could keep those pipes open and stall
// indefinitely even though gh itself is gone (watch/docs/go-gotchas.md
// #822).
const ghWaitDelay = 5 * time.Second

// ghOutputCap bounds how many bytes of stdout/stderr execGh will buffer per
// stream -- generous enough for large GraphQL thread pages, but exceeding
// it is an explicit truncation error, never a silently-short JSON body
// (watch/docs/error-handling.md's default-deny rule: truncation means
// "verdict unreliable", never "no data").
const ghOutputCap = 4 << 20 // 4 MiB

// errGhOutputTruncated is execGh's sentinel for a bounded-buffer overflow on
// either stream.
var errGhOutputTruncated = errors.New("gh output exceeded bounded cap")

// errGhTimeout, errGhCancelled, and errGhDecode are execGh/ghJSON's
// remaining classification sentinels (#886), alongside errGhOutputTruncated
// above: errGhTimeout covers both a ghParentContext/ghTimeout deadline kill
// and cmd.WaitDelay forcing a lingering grandchild's pipes closed (both are,
// operationally, a hang that had to be bounded); errGhCancelled covers an
// already-cancelled (or cancelled mid-flight) parent context; errGhDecode
// covers ghJSON's own non-JSON stdout body on an otherwise-successful `gh`
// exit. classifyGhFailure below resolves any error carrying one of these
// (plus errGhOutputTruncated, plus the fail-closed command-failure default)
// to a single closed-set failureClassXxx string.
var (
	errGhTimeout   = errors.New("gh invocation timed out")
	errGhCancelled = errors.New("gh invocation was cancelled")
	errGhDecode    = errors.New("gh output failed to decode")
)

// ghExitError wraps a nonzero `gh` exit (*exec.ExitError) so a caller can
// recover the exact exit code via errors.As -- ghJSON's strict rewrite
// (#886) needs this to fail closed on a nonzero non-checks exit rather than
// the old blanket "any decodable stdout is success" tolerance. Unwrap
// exposes the original *exec.ExitError so existing errors.Is/errors.As
// checks against it keep working unchanged.
type ghExitError struct {
	code int
	err  error
}

func (e *ghExitError) Error() string { return e.err.Error() }
func (e *ghExitError) Unwrap() error { return e.err }
func (e *ghExitError) ExitCode() int { return e.code }

// ghParentContext is a test seam over context.Background, mirroring the
// package's existing execGh/fleetConfigPath seam shape (a var pointing at a
// default func, restorable via t.Cleanup): defaultExecGh derives its own
// ghTimeout-scoped context from whatever ghParentContext returns, so a
// caller-supplied deadline or cancellation can bound execGh tighter than
// ghTimeout itself, and a test can substitute an already-cancelled or
// short-deadlined context without waiting out the full 60s ghTimeout.
var ghParentContext = context.Background

// failureClassCommand, failureClassTimeout, failureClassCancelled,
// failureClassTruncated, and failureClassParse are classifyGhFailure's
// closed set of failure classes (#886) -- the orthogonal "cause" axis to the
// package's existing reasonXxx "site" constants (which reason string was
// hit), following the same plain-string-constant style. failureClassCommand
// is the fail-closed default: any non-nil, otherwise-unclassified `gh`
// failure (a nonzero exit, a start failure like exec.ErrNotFound, ...)
// still classifies as *something* rather than an empty/unknown class a
// caller's " class=" log-line rendering would silently omit.
const (
	failureClassCommand   = "command"
	failureClassTimeout   = "timeout"
	failureClassCancelled = "cancelled"
	failureClassTruncated = "truncated"
	failureClassParse     = "parse"
)

// classifyGhFailure resolves any non-nil `gh`-related error to one of the
// five failureClassXxx constants, in fixed precedence order (cancelled >
// timeout > truncated > parse > command) -- errors.Is walks the full
// errors.Join/%w chain, so a joined "timeout+truncation" error still
// classifies as timeout, the higher-precedence class, regardless of join
// order. Returns "" only for a nil err.
func classifyGhFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errGhCancelled):
		return failureClassCancelled
	case errors.Is(err, errGhTimeout):
		return failureClassTimeout
	case errors.Is(err, errGhOutputTruncated):
		return failureClassTruncated
	case errors.Is(err, errGhDecode):
		return failureClassParse
	default:
		return failureClassCommand
	}
}

// boundedWriter is an io.Writer that caps how many bytes it will actually
// buffer at ghOutputCap, silently dropping (not erroring) any bytes beyond
// that cap and recording that it did so via truncated -- returning a short
// write count here would make exec.Cmd itself fail with a generic
// "short write" error that loses the distinction between "gh legitimately
// failed" and "gh's output exceeded the bound".
type boundedWriter struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	remaining := ghOutputCap - w.buf.Len()
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

func (w *boundedWriter) String() string { return w.buf.String() }

// execGh runs `gh <args...>` bounded by a fresh ghTimeout-scoped context per
// call, with cmd.WaitDelay bounding how long Wait can stall on a lingering
// grandchild, separate bounded stdout/stderr buffers (never
// CombinedOutput), cmd.Stdin = nil, and a noninteractive environment
// (GH_PROMPT_DISABLED=1, GH_NO_UPDATE_NOTIFIER=1, GH_PAGER=cat,
// GIT_TERMINAL_PROMPT=0) -- watch/AGENTS.md #825/#852's subprocess rule
// extended to internal/babysit (#854). A test seam, alongside the package's
// existing `command` seam (which remains for `git rev-parse` and the
// `cenci run` self-exec, neither of which is a `gh` call).
var execGh = defaultExecGh

func defaultExecGh(args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(ghParentContext(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.WaitDelay = ghWaitDelay
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(),
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
		"GH_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
	)
	var outBuf, errBuf boundedWriter
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	// ctx.Err() is read here, before the deferred cancel() above fires, so a
	// context.DeadlineExceeded/context.Canceled cause set by ghParentContext
	// or ghTimeout is still observable -- reading it after cancel() runs
	// would always report context.Canceled (cancel's own doing), losing the
	// distinction between "the caller's own deadline elapsed" and "this
	// function's own cleanup ran" (#886).
	ctxErr := ctx.Err()
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		runErr = &ghExitError{code: exitErr.ExitCode(), err: runErr}
	}
	switch ctxErr {
	case context.DeadlineExceeded:
		runErr = errors.Join(runErr, errGhTimeout)
	case context.Canceled:
		runErr = errors.Join(runErr, errGhCancelled)
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
		// joined error), but the original error is no longer silently lost
		// from diagnostics (#854 fix, item 5).
		return outBuf.String(), errBuf.String(), errors.Join(runErr, errGhOutputTruncated)
	}
	return outBuf.String(), errBuf.String(), runErr
}
