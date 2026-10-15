// Package ticketstate answers "is this ticket's work over?" for a batch of
// ticket numbers in one repository, resolves a recorded launch directory to
// its git toplevel and owner/name, and reads the per-repo windowCleanup
// opt-out (#1250). The same single query also yields a detailed outcome per
// number (open, issue closed, PR merged with its head SHAs, PR closed
// unmerged), and the worktreeCleanup config reader returns the worktree-cleanup
// enabled flag and staleAfter (#1263). It is a leaf package: it must not import
// internal/dispatch, internal/run or internal/daemon.
package ticketstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// Timeouts are package vars so tests can shorten them. gitTimeout is shorter
// than mainsync's 60s because every git call here is local-only (rev-parse and
// remote get-url never touch the network).
var (
	ghTimeout  = 60 * time.Second
	gitTimeout = 10 * time.Second
	// waitDelay bounds how long Wait can block after the process itself is
	// gone, so a grandchild holding the output pipes cannot defeat the timeout.
	waitDelay = 5 * time.Second
)

const (
	maxStdoutBytes = 1 << 20
	maxStderrBytes = 64 << 10
)

// errTimeout classifies a deadline kill or a WaitDelay-forced pipe close.
var errTimeout = errors.New("subprocess timed out")

// errOutputTruncated classifies a stdout or stderr stream over its cap.
var errOutputTruncated = errors.New("subprocess output exceeded bounded cap")

// boundedWriter buffers at most max bytes and drops the rest, recording that it
// did. Returning a short write instead would make exec.Cmd fail with a generic
// "short write" error that hides the difference between a failed command and
// an over-cap one.
type boundedWriter struct {
	max       int
	buf       bytes.Buffer
	truncated bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
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

func (w *boundedWriter) String() string { return w.buf.String() }

// execGh runs `gh <args...>` bounded by ghTimeout, waitDelay and the output caps.
func execGh(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	return execBounded(ctx, ghTimeout, nil, "gh", args...)
}

// execGit runs `git -C dir <args...>` bounded by gitTimeout, waitDelay and the
// output caps, with a non-interactive environment so a missing credential can
// never block on a prompt.
func execGit(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	return execBounded(ctx, gitTimeout, gitEnv(), "git", append([]string{"-C", dir}, args...)...)
}

// execBounded runs name under a timeout-bounded child of ctx, with stdin unset
// and stdout and stderr captured separately so a stderr diagnostic can never
// splice into bytes a caller decodes as JSON. A non-nil env replaces the
// inherited environment.
func execBounded(ctx context.Context, timeout time.Duration, env []string, name string, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.WaitDelay = waitDelay
	outBuf := boundedWriter{max: maxStdoutBytes}
	errBuf := boundedWriter{max: maxStderrBytes}
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	// ctx.Err() is read before the deferred cancel fires (which would turn it
	// into context.Canceled), and os/exec never wraps it into the returned
	// error itself (go-gotchas rule 30). A WaitDelay kill is a bounded hang too.
	if ctx.Err() == context.DeadlineExceeded || errors.Is(runErr, exec.ErrWaitDelay) {
		runErr = errors.Join(runErr, errTimeout)
	}
	if outBuf.truncated || errBuf.truncated {
		runErr = errors.Join(runErr, errOutputTruncated)
	}
	return outBuf.String(), errBuf.String(), runErr
}

// maxErrTextBytes bounds subprocess stderr and GraphQL messages quoted in errors.
const maxErrTextBytes = 512

// collapse makes s safe for a one-line error: whitespace runs (including
// newlines) become single spaces and the result is cut to maxErrTextBytes at a
// rune boundary, with a visible marker when cut.
func collapse(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxErrTextBytes {
		return s
	}
	cut := maxErrTextBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...(truncated)"
}

// withStderr appends the collapsed stderr text to err, when there is any.
func withStderr(err error, stderr string) error {
	if text := collapse(stderr); text != "" {
		return fmt.Errorf("%w: %s", err, text)
	}
	return err
}

// gitEnv is the inherited environment plus GIT_TERMINAL_PROMPT=0 and the
// ssh/askpass hardening from dispatch/mainsync.go, each added only when the
// caller's environment does not already set it.
func gitEnv() []string {
	base := os.Environ()
	env := append(append([]string{}, base...), "GIT_TERMINAL_PROMPT=0")
	if !envHasKey(base, "GIT_SSH_COMMAND") {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new")
	}
	if !envHasKey(base, "GIT_ASKPASS") {
		env = append(env, "GIT_ASKPASS=")
	}
	if !envHasKey(base, "SSH_ASKPASS_REQUIRE") {
		env = append(env, "SSH_ASKPASS_REQUIRE=never")
	}
	return env
}

func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}
