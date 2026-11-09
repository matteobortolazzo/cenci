package babysit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
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

// execTmux runs `tmux <args...>` on socket, bounded by a fresh
// tmuxTimeout-scoped context per call, with separate bounded stdout/stderr
// buffers (boundedWriter, shared with gh.go).
//
// An empty socket is today's bare call: the ambient server and env, with
// cmd.WaitDelay bounding a lingering grandchild and cmd.Stdin = nil. A
// non-empty socket (#1431) is hardened, because the supervisor inherits the
// daemon's full env and the value can come from an arm request: it must pass the
// shape check and the trusted-directory check -- a failure returns the error and
// execs nothing, never falling back to the ambient server -- and then runs
// through tmux.RunSocket (`tmux -S <socket>`, allowlisted env, 5s bound, a
// timeout joined with context.DeadlineExceeded).
func execTmux(socket string, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxTimeout)
	defer cancel()
	var outBuf, errBuf boundedWriter
	var runErr error
	if socket == "" {
		cmd := exec.CommandContext(ctx, "tmux", args...)
		cmd.WaitDelay = tmuxWaitDelay
		cmd.Stdin = nil
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		runErr = cmd.Run()
	} else {
		if !tmux.ValidSocket(socket) {
			return "", "", fmt.Errorf("%w: %q", tmux.ErrMalformedSocket, socket)
		}
		dir, derr := tmux.TrustedSocketDir()
		if derr != nil {
			return "", "", derr
		}
		if cerr := tmux.CheckTrustedSocket(dir, socket); cerr != nil {
			return "", "", cerr
		}
		runErr = tmux.RunSocket(ctx, socket, &outBuf, &errBuf, args...)
	}
	if outBuf.truncated || errBuf.truncated {
		runErr = errors.Join(runErr, errors.New("tmux output exceeded bounded cap"))
	}
	return outBuf.String(), errBuf.String(), runErr
}

// paneInventoryFormat is the one pane inventory a tick reads (#1457): every
// pane's window name, whether it is dead, and the @cenci-repo-dir window option
// `cenci run` records on each window it spawns (frontend.RepoDirOption). tmux
// expands an unset user option to "", so a row always has three columns.
const paneInventoryFormat = "#{window_name}\t#{pane_dead}\t#{@cenci-repo-dir}"

// tmuxPaneInventory reads the raw pane inventory of the recorded session on the
// recorded socket. It is a test seam, like command/execGh.
var tmuxPaneInventory = defaultTmuxPaneInventory

// defaultTmuxPaneInventory is a single bounded execTmux call (#1431's
// socket-explicit path, its timeout, WaitDelay and stderr conventions). A tmux
// failure, a timeout or a truncated read is an error, never "no worker".
func defaultTmuxPaneInventory(socket, session string) (string, error) {
	stdout, stderr, err := execTmux(socket, "list-panes", "-s", "-t", "="+session, "-F", paneInventoryFormat)
	if err != nil {
		return "", fmt.Errorf("read the pane inventory of tmux session %q: %s: %w", session, strings.TrimSpace(stderr), err)
	}
	return stdout, nil
}

// paneRow is one parsed pane inventory row.
type paneRow struct {
	window  string
	dead    bool
	repoDir string
}

// parsePaneInventory parses the 3-column inventory, trimming only the trailing
// newline so a row with an empty repo-dir keeps its trailing TAB (watch/docs/
// tmux.md). Empty output or any row that is not exactly a non-empty name, a
// 0/1 pane_dead and a repo-dir cannot prove a worker absent, so it fails the
// whole read. The error never echoes a row: a repo-dir is a local path.
func parsePaneInventory(out string) ([]paneRow, error) {
	if out == "" {
		return nil, errors.New("empty pane inventory")
	}
	var rows []paneRow
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[0] == "" || (fields[1] != "0" && fields[1] != "1") {
			return nil, fmt.Errorf("malformed pane inventory (row %d)", i+1)
		}
		rows = append(rows, paneRow{window: fields[0], dead: fields[1] == "1", repoDir: fields[2]})
	}
	return rows, nil
}

// gitCommonDirTimeout bounds the one `git rev-parse --git-common-dir` call per
// lookup, mirroring tmuxTimeout; a var so a test can shorten it.
var gitCommonDirTimeout = 10 * time.Second

// gitCommonDir resolves a directory's git common dir. It is a test seam.
var gitCommonDir = defaultGitCommonDir

// defaultGitCommonDir runs `git -C <dir> rev-parse --path-format=absolute
// --git-common-dir` with mainsync.go's conventions: a bounded context,
// WaitDelay, bounded separate stdout/stderr, no stdin, and prompts disabled. A
// failure, a timeout or a non-absolute answer is an error.
func defaultGitCommonDir(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommonDirTimeout)
	defer cancel()
	var outBuf, errBuf boundedWriter
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS_REQUIRE=never")
	cmd.WaitDelay = tmuxWaitDelay
	cmd.Stdin = nil
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	if outBuf.truncated || errBuf.truncated {
		err = errors.Join(err, errors.New("git output exceeded bounded cap"))
	}
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir: %s: %w", strings.TrimSpace(errBuf.String()), err)
	}
	common := strings.TrimSuffix(outBuf.String(), "\n")
	if !filepath.IsAbs(common) {
		return "", fmt.Errorf("git rev-parse --git-common-dir: not an absolute path")
	}
	return filepath.Clean(common), nil
}

// workerLiveness is one tick's view of this PR's branch repair workers: a live
// `<PR>-ci-repair` or `<PR>-merge-repair` pane that is ours or of unknown
// ownership, plus the recovery note for each unknown one.
type workerLiveness struct {
	ciLive, mergeLive bool
	notes             []string
}

func (w workerLiveness) any() bool { return w.ciLive || w.mergeLive }

// describe names the live workers for a log line.
func (w workerLiveness) describe(pr string) string {
	switch {
	case w.ciLive && w.mergeLive:
		return pr + "-ci-repair and " + pr + "-merge-repair are still running"
	case w.ciLive:
		return "ci-repair window " + pr + "-ci-repair is still running"
	default:
		return "merge-repair window " + pr + "-merge-repair is still running"
	}
}

type paneOwner int

const (
	paneOwned paneOwner = iota
	paneUnknown
	paneForeign
)

// observeWorkers reads the recorded session's pane inventory once and derives
// both CI and merge liveness for s.PR (#1457). Only an exact `<PR>-ci-repair`
// or `<PR>-merge-repair` window with a live pane counts. Its @cenci-repo-dir
// decides ownership: Clean-equal to LaunchDir or RepoRoot is ours; otherwise
// the git common dirs are compared, equal is ours and different is another
// repository's worker, which is ignored with a printed note. An empty or
// relative repo-dir, or a lookup that fails on either side, is unknown: it
// counts as live (it blocks launches) and adds a recovery note. The note never
// echoes the raw repo-dir. Our own common dir is resolved at most once.
func observeWorkers(s *State) (workerLiveness, error) {
	var w workerLiveness
	out, err := tmuxPaneInventory(s.LaunchSocket, s.LaunchSession)
	if err != nil {
		return w, err
	}
	rows, err := parsePaneInventory(out)
	if err != nil {
		return w, fmt.Errorf("observe repair workers in tmux session %q: %w", s.LaunchSession, err)
	}
	ownCommonDir := sync.OnceValues(func() (string, error) {
		dir := s.LaunchDir
		if dir == "" {
			dir = s.RepoRoot
		}
		if dir == "" {
			return "", errors.New("no launch directory or repository root recorded")
		}
		return gitCommonDir(dir)
	})
	classify := func(repoDir string) paneOwner {
		if repoDir == "" || !filepath.IsAbs(repoDir) {
			return paneUnknown
		}
		dir := filepath.Clean(repoDir)
		for _, recorded := range []string{s.LaunchDir, s.RepoRoot} {
			if recorded != "" && filepath.Clean(recorded) == dir {
				return paneOwned
			}
		}
		ours, err := ownCommonDir()
		if err != nil {
			return paneUnknown
		}
		theirs, err := gitCommonDir(dir)
		if err != nil {
			return paneUnknown
		}
		if theirs == ours {
			return paneOwned
		}
		return paneForeign
	}
	for _, row := range rows {
		var workflow string
		switch row.window {
		case s.PR + "-ci-repair":
			workflow = "ci-repair"
		case s.PR + "-merge-repair":
			workflow = "merge-repair"
		default:
			continue
		}
		if row.dead {
			continue
		}
		target := s.LaunchSession + ":" + row.window
		switch classify(row.repoDir) {
		case paneForeign:
			fmt.Printf("PR #%s: ignoring live %s window %s: it belongs to another repository\n", s.PR, workflow, target)
			continue
		case paneUnknown:
			note := fmt.Sprintf("%s window %s is live but its repository is unknown; let the worker finish, or close that window once its edits are preserved; cenci babysit reset %s re-arms the retry budget", workflow, target, s.PR)
			if !slices.Contains(w.notes, note) {
				w.notes = append(w.notes, note)
				fmt.Printf("PR #%s: %s\n", s.PR, note)
			}
		}
		if workflow == "ci-repair" {
			w.ciLive = true
		} else {
			w.mergeLive = true
		}
	}
	return w, nil
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
	stdout, stderr, err := execTmux("", "display-message", "-t", pane, "-p", "#{session_name}")
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
// against folding "probe errored" into "condition false" (#822). The probe runs
// on socket (#1431; empty is the legacy bare call). A timeout on a socket call
// is joined onto an *exec.ExitError, so the deadline is checked first: it is a
// probe failure, never "absent". A refused socket (malformed or untrusted) is an
// error too.
func defaultTmuxHasSession(socket, session string) (bool, error) {
	_, _, err := execTmux(socket, "has-session", "-t", "="+session)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
