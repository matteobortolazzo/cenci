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
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
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
