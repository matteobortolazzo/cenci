package babysit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// currentTmuxSession and tmuxHasSession are the two package-level tmux seams
// ticket #975 introduces: currentTmuxSession resolves the arming pane's own
// tmux session (mirroring internal/tmux.ExecClient.CurrentSession's
// $TMUX_PANE -> `display-message -t <pane> -p '#{session_name}'`
// resolution, including the empty-output check docs/tmux.md documents),
// while tmuxHasSession probes whether a previously-recorded session still
// exists (the exact-match `tmux has-session -t =<name>` form, also per
// docs/tmux.md). Both defaults below are bounded subprocess calls mirroring
// gh.go's defaultExecGh (context timeout, WaitDelay, bounded stdout/stderr,
// Stdin=nil) -- watch/AGENTS.md rule #5's mandatory subprocess convention,
// extended here rather than reusing internal/tmux.ExecClient.tmuxCmd (which
// backs the daemon frontend's own hot path and stays unbounded, per the
// plan's rejected-alternative rationale). Tests stub these vars directly,
// exactly like the package's existing command/execGh/processOwned seams.

// tmuxTimeout bounds every individual tmux invocation execTmux makes -- a
// hung tmux server must never stall arming or a launch() probe indefinitely.
const tmuxTimeout = 10 * time.Second

// tmuxWaitDelay bounds how long cmd.Wait can block after tmux itself has
// exited or been killed by tmuxTimeout's context, mirroring gh.go's
// ghWaitDelay (watch/docs/go-gotchas.md #822).
const tmuxWaitDelay = 5 * time.Second

// execTmux runs `tmux <args...>` bounded by a fresh tmuxTimeout-scoped
// context per call, with cmd.WaitDelay bounding a lingering grandchild,
// separate bounded stdout/stderr buffers (boundedWriter, shared with gh.go),
// and cmd.Stdin = nil.
func execTmux(args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.WaitDelay = tmuxWaitDelay
	cmd.Stdin = nil
	var outBuf, errBuf boundedWriter
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	return outBuf.String(), errBuf.String(), runErr
}

var currentTmuxSession = defaultCurrentTmuxSession

// defaultCurrentTmuxSession resolves the arming pane's own tmux session via
// the inherited $TMUX_PANE environment variable, mirroring
// internal/tmux.ExecClient.CurrentSession. It returns early -- no subprocess
// call at all -- when $TMUX_PANE is unset/blank, so an unstubbed call in CI
// (no tmux, no pane) is a clean, cheap error rather than a spawn attempt.
func defaultCurrentTmuxSession() (string, error) {
	pane := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if pane == "" {
		return "", fmt.Errorf("TMUX_PANE is not set; not running inside a tmux pane")
	}
	stdout, stderr, err := execTmux("display-message", "-t", pane, "-p", "#{session_name}")
	if err != nil {
		return "", fmt.Errorf("tmux display-message: %s: %w", strings.TrimSpace(stderr), err)
	}
	session := strings.TrimSpace(stdout)
	if session == "" {
		// tmux display-message -p exits 0 with empty stdout when -t names a
		// pane that no longer exists, rather than erroring like most other
		// tmux subcommands do with an invalid target (docs/tmux.md).
		return "", fmt.Errorf("tmux could not resolve a session for pane %q", pane)
	}
	return session, nil
}

var tmuxHasSession = defaultTmuxHasSession

// defaultTmuxHasSession probes whether session still exists on the tmux
// server, via the exact-match `tmux has-session -t =<name>` form (docs/tmux.md:
// an unmarked -t prefix-matches, so a bare "work" would falsely resolve
// against an unrelated "work-2"). A nonzero tmux exit (no such session, or no
// server running at all) classifies as (false, nil): a normal negative
// result, never a probe failure. A failure to run tmux at all (e.g. the
// binary isn't resolvable, or the bounded call timed out) is kept as a
// distinct (false, non-nil error) classification -- collapsing that into the
// negative-boolean case would violate watch/docs/error-handling.md's rule
// against folding "probe errored" into "condition false" (#822).
func defaultTmuxHasSession(session string) (bool, error) {
	_, _, err := execTmux("has-session", "-t", "="+session)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
