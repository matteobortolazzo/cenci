// Package worktreeclean lists a repo's .worktrees/* worktrees from either side
// of the sandbox mount and reports each one's git and filesystem facts (#1264).
// It is strictly read-only: it never fetches, prunes or refreshes an index, and
// every work-tree git call is pinned to an explicit --git-dir/--work-tree. It
// is a leaf package: it must not import internal/dispatch, internal/run,
// internal/daemon or internal/pipeline.
package worktreeclean

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Timeouts and caps are package vars so tests can shrink them. gitTimeout is
// shorter than mainsync's 60s because every git call here is local-only.
// activityTimeout bounds the streamed file listing, which walks every
// non-ignored file of a worktree and so legitimately takes longer.
var (
	gitTimeout      = 10 * time.Second
	activityTimeout = 120 * time.Second
	// sizeTimeout and maxSizeEntries bound the in-process directory walk that
	// measures a worktree's size on disk: a worktree can hold any number of files
	// (and an agent can create them), and the walk must not run unbounded.
	sizeTimeout    = 120 * time.Second
	maxSizeEntries = 2000000
	// waitDelay bounds how long Wait can block after the process itself is
	// gone, so a grandchild holding the output pipes cannot defeat the timeout.
	waitDelay          = 5 * time.Second
	maxStdoutBytes     = 1 << 20
	maxStderrBytes     = 64 << 10
	maxActivityEntries = 500000
)

// maxStreamEntryBytes bounds one streamed entry (a path); PATH_MAX is 4096, so
// anything longer is malformed output rather than a real path. An entry of
// exactly this many bytes is accepted.
const maxStreamEntryBytes = 64 << 10

// errTimeout classifies a deadline kill or a WaitDelay-forced pipe close.
var errTimeout = errors.New("subprocess timed out")

// errOutputTruncated classifies a stdout, stderr or entry stream over its cap.
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

// execGit runs `git -C dir <args...>` for repo resolution, bounded by gitTimeout,
// waitDelay and the output caps.
func execGit(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	return execCapture(ctx, "", append([]string{"-C", dir}, args...))
}

// execGitWT runs git against one worktree with an explicit --git-dir and
// --work-tree, never touching the index: --no-optional-locks (plus
// GIT_OPTIONAL_LOCKS=0 in the environment) stops `status` from refreshing it and
// core.fsmonitor=false stops a configured fsmonitor hook from running.
func execGitWT(ctx context.Context, gitDir, workTree string, args ...string) (stdout, stderr string, err error) {
	return execCapture(ctx, workTree, wtArgs(gitDir, workTree, args))
}

// execGitWTStream runs git like execGitWT but hands each NUL-terminated stdout
// entry to onEntry as it arrives, buffering nothing, so a listing of any length
// stays bounded by maxActivityEntries and activityTimeout instead of
// maxStdoutBytes. An error from onEntry aborts the command and is returned.
func execGitWTStream(ctx context.Context, gitDir, workTree string, onEntry func(string) error, args ...string) error {
	w := &entryWriter{onEntry: onEntry}
	stderr, runErr := runBounded(ctx, activityTimeout, workTree, wtArgs(gitDir, workTree, args), w)
	switch {
	case w.err != nil:
		return w.err
	case runErr != nil:
		return withStderr(runErr, stderr)
	case len(w.partial) > 0:
		return fmt.Errorf("%w: unterminated trailing entry", errOutputTruncated)
	}
	return nil
}

func wtArgs(gitDir, workTree string, args []string) []string {
	pinned := []string{
		"--no-optional-locks", "-c", "core.fsmonitor=false",
		"--git-dir=" + gitDir, "--work-tree=" + workTree,
	}
	return append(pinned, args...)
}

// execCapture runs git in dir (the current directory when empty) under
// gitTimeout with stdout and stderr captured separately, so a stderr diagnostic
// can never splice into bytes a caller parses.
func execCapture(ctx context.Context, dir string, args []string) (stdout, stderr string, err error) {
	out := boundedWriter{max: maxStdoutBytes}
	stderr, err = runBounded(ctx, gitTimeout, dir, args, &out)
	if out.truncated {
		err = errors.Join(err, errOutputTruncated)
	}
	return out.String(), stderr, err
}

// runBounded runs git under a timeout-bounded child of ctx with stdin unset and
// the hardened environment, writing stdout to the given writer. The returned
// error carries errTimeout for a deadline or WaitDelay kill and
// errOutputTruncated for a stderr stream over its cap.
func runBounded(ctx context.Context, timeout time.Duration, dir string, args []string, stdout io.Writer) (stderr string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	cmd.WaitDelay = waitDelay
	errBuf := boundedWriter{max: maxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = &errBuf
	err = cmd.Run()
	// ctx.Err() is read before the deferred cancel fires (which would turn it
	// into context.Canceled), and os/exec never wraps it into the returned
	// error itself (go-gotchas rule 30). A WaitDelay kill is a bounded hang too.
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = errors.Join(err, ctxErr)
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			err = errors.Join(err, errTimeout)
		}
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = errors.Join(err, errTimeout)
	}
	if errBuf.truncated {
		err = errors.Join(err, errOutputTruncated)
	}
	return errBuf.String(), err
}

// entryWriter splits a -z stream into NUL-terminated entries and passes each to
// onEntry. After the first failure (a callback error, the entry-count cap or an
// oversized entry) it records the error in err and rejects further writes, which
// closes the pipe so git stops on SIGPIPE.
type entryWriter struct {
	onEntry func(string) error
	partial []byte
	count   int
	err     error
}

func (w *entryWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	total := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			if len(w.partial)+len(p) > maxStreamEntryBytes {
				w.err = fmt.Errorf("%w: entry longer than %d bytes", errOutputTruncated, maxStreamEntryBytes)
				return 0, w.err
			}
			w.partial = append(w.partial, p...)
			break
		}
		if len(w.partial)+i > maxStreamEntryBytes {
			w.err = fmt.Errorf("%w: entry longer than %d bytes", errOutputTruncated, maxStreamEntryBytes)
			return 0, w.err
		}
		entry := string(append(w.partial, p[:i]...))
		w.partial = w.partial[:0]
		p = p[i+1:]
		w.count++
		if w.count > maxActivityEntries {
			w.err = fmt.Errorf("%w: more than %d entries", errOutputTruncated, maxActivityEntries)
			return 0, w.err
		}
		if err := w.onEntry(entry); err != nil {
			w.err = err
			return 0, err
		}
	}
	return total, nil
}

// maxErrTextBytes bounds subprocess stderr quoted in errors.
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

// inheritedRepoVars relocate a git repository; an inherited value would point the
// read-only inspection at some other repo's refs, index or objects.
var inheritedRepoVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
}

// droppedEnvPrefixes are inherited variables, matched by name prefix, that must
// not reach git: ambient config injection (GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT
// and its KEY_/VALUE_ pairs; GIT_CONFIG_GLOBAL/SYSTEM/NOSYSTEM are host-owned
// locations and are kept), an alternate attribute source, and tracing that would
// write to a caller-chosen file.
var droppedEnvPrefixes = []string{
	"GIT_CONFIG_PARAMETERS=", "GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_",
	"GIT_ATTR_SOURCE=", "GIT_TRACE",
}

// gitEnv is the inherited environment minus the repo-location variables, the
// config-injection/attribute-source/trace variables and any caller
// GIT_OPTIONAL_LOCKS/GIT_TERMINAL_PROMPT/GIT_NO_LAZY_FETCH/GIT_ALLOW_PROTOCOL,
// plus GIT_OPTIONAL_LOCKS=0, GIT_TERMINAL_PROMPT=0, GIT_NO_LAZY_FETCH=1 (a partial
// clone must never fetch a missing object during a read-only inspection) and
// GIT_ALLOW_PROTOCOL=none (overrides every protocol.*.allow, so no transport,
// ext:: included, can start for a lazy-fetch child on a git too old to honor
// GIT_NO_LAZY_FETCH), all forced over the caller's values; the ssh/askpass
// hardening from dispatch/mainsync.go is added only when the caller's environment
// does not already set it.
func gitEnv() []string {
	drop := append([]string{"GIT_OPTIONAL_LOCKS", "GIT_TERMINAL_PROMPT", "GIT_NO_LAZY_FETCH", "GIT_ALLOW_PROTOCOL"}, inheritedRepoVars...)
	base := os.Environ()
	env := make([]string, 0, len(base)+6)
	for _, kv := range base {
		if slices.ContainsFunc(drop, func(k string) bool { return strings.HasPrefix(kv, k+"=") }) ||
			slices.ContainsFunc(droppedEnvPrefixes, func(p string) bool { return strings.HasPrefix(kv, p) }) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=none")
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
	return slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, key+"=") })
}
