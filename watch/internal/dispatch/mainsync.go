package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout bounds every individual git invocation the local-main sync
// makes (#822 Q4), so a hung network call (or any other stuck git process)
// can never stall a dispatch pass indefinitely.
const gitTimeout = 60 * time.Second

// gitWaitDelay bounds how long cmd.Wait can block *after* the git process
// itself has exited or been killed by gitTimeout's context. Without it, a
// grandchild process that inherited the stdout/stderr pipes (ssh, a
// credential helper, git-remote-https) can keep those pipes open and stall
// CombinedOutput indefinitely even though git itself is gone -- defeating
// gitTimeout's "can never stall a dispatch pass indefinitely" guarantee
// (#822 review fix #2). WaitDelay forcibly closes the I/O pipes after this
// delay past the point the process itself has been waited on.
const gitWaitDelay = 5 * time.Second

// execGit runs `git -C dir <args...>` under a fresh gitTimeout-bounded
// context per call, with GIT_TERMINAL_PROMPT=0 plus SSH/askpass hardening
// (gitEnv) appended to the inherited environment so a missing credential can
// never block on any kind of interactive prompt, and gitWaitDelay bounding
// how long Wait can stall on a lingering grandchild. It returns trimmed
// combined output and the exec error, if any.
//
// Named execGit (not runGit) because enroll_test.go already declares a
// package-level runGit test helper with a different signature
// (runGit(t *testing.T, dir string, args ...string)) predating #822.
func execGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = gitEnv()
	cmd.WaitDelay = gitWaitDelay
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// gitEnv builds the environment for a git child process: the inherited
// os.Environ() plus GIT_TERMINAL_PROMPT=0 (suppresses git's own terminal
// prompts) and SSH/askpass hardening (#822 review fix #3) -- ssh's own
// passphrase/host-key prompts and a GUI SSH_ASKPASS/GIT_ASKPASS dialog are
// NOT covered by GIT_TERMINAL_PROMPT alone, and the daemon inherits the full
// ambient environment via os.Environ(). Each hardening var is only added
// when the caller's environment does not already set it, so a deliberate
// operator override is never clobbered.
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

// envHasKey reports whether env (an os.Environ()-shaped slice) already sets
// key, so gitEnv never clobbers a deliberate caller override.
func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// isAncestor reports whether ancestor is an ancestor of (or equal to)
// descendant, via `git merge-base --is-ancestor`. Locale-independent and
// deterministic, unlike matching git's (gettext-localized) stderr prose.
//
// merge-base --is-ancestor's own exit codes distinguish a real negative
// result from a command failure: exit 0 means true, exit 1 means false (a
// normal, successful "no" answer -- NOT an error), and anything else (exit
// >=2, or a non-ExitError such as a context-deadline kill) means the probe
// itself failed to run to completion. Collapsing exit-1 and a genuine
// command error into the same `false` (as an earlier version of this
// function did) would let a real git failure masquerade as legitimate
// divergence -- the caller must be able to tell them apart (#822 review
// fix #1).
func isAncestor(dir, ancestor, descendant string) (bool, error) {
	out, err := execGit(dir, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w (%s)", ancestor, descendant, err, out)
}

// syncMain runs the once-per-pass local `main` sync for one enrolled repo
// (#822): `git fetch origin`, then a fast-forward-only merge of origin/main
// into main -- but only when dir is checked out on main, and never via
// --reset, --force, or `git fetch origin main:main`. It returns the
// classified outcome plus a short human-readable detail for logging.
//
// dir == "" is never probed (mirrors probeStage, collect.go): resolving a
// repo root from the daemon's own cwd would risk syncing an unrelated repo.
//
// Divergence is classified by two `git merge-base --is-ancestor` probes,
// not by matching git's merge failure message -- deterministic and
// locale-independent. The merge is skipped entirely in the two cases where
// it would be a no-op (already up to date / local ahead) or provably doomed
// (diverged); it only actually runs when local main is strictly behind.
// dryRun still fetches and classifies (an accurate gate needs the fetch) but
// never runs the merge itself.
func syncMain(dir string, dryRun bool) (MainSync, string) {
	if dir == "" {
		return MainSyncSkipped, "no repo dir configured"
	}

	branch, err := execGit(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		// symbolic-ref fails both for a detached HEAD and for a dir that
		// isn't a git repo at all -- rev-parse --git-dir distinguishes them.
		if _, gitDirErr := execGit(dir, "rev-parse", "--git-dir"); gitDirErr != nil {
			return MainSyncFailed, "not a git repository"
		}
		return MainSyncSkipped, "HEAD is detached"
	}
	if branch != "main" {
		return MainSyncSkipped, fmt.Sprintf("checked-out branch %q is not main", branch)
	}

	// -c core.hooksPath=/dev/null (global option, must precede the
	// subcommand): an automated dispatch-loop sync must never execute
	// repo-supplied git hooks (e.g. a committed/husky-style post-merge hook)
	// unattended (#822 review round 2 fix #2).
	if _, err := execGit(dir, "-c", "core.hooksPath=/dev/null", "fetch", "origin"); err != nil {
		return MainSyncFetchFailed, fmt.Sprintf("git fetch origin failed: %v", err)
	}

	// localBehindOrEqual: local HEAD is an ancestor of (or equal to)
	// origin/main. localAheadOrEqual: origin/main is an ancestor of (or
	// equal to) local HEAD. A genuine merge-base command error (not merely
	// "not an ancestor") must never be reported as MainSyncDiverged -- that
	// would mask a real git failure as a false divergence (#822 review
	// fix #1).
	localBehindOrEqual, err := isAncestor(dir, "HEAD", "origin/main")
	if err != nil {
		return MainSyncFailed, fmt.Sprintf("git merge-base --is-ancestor check failed: %v", err)
	}
	localAheadOrEqual, err := isAncestor(dir, "origin/main", "HEAD")
	if err != nil {
		return MainSyncFailed, fmt.Sprintf("git merge-base --is-ancestor check failed: %v", err)
	}

	switch {
	case localBehindOrEqual && localAheadOrEqual:
		return MainSyncSynced, "already up to date"
	case localAheadOrEqual:
		return MainSyncSynced, "local main is ahead of origin/main"
	case !localBehindOrEqual:
		return MainSyncDiverged, "local main and origin/main have diverged"
	}

	// Strictly behind: there is something to fast-forward.
	if dryRun {
		return MainSyncSynced, "would fast-forward origin/main (dry-run, not merged)"
	}

	// Re-verify HEAD is still on main immediately before merging (#822
	// review fix #4, TOCTOU): `git fetch` above is a network call that can
	// take seconds, and something else could have checked out a different
	// branch during that window -- `merge --ff-only` trusts the *current*
	// HEAD, so relying solely on the early check above could silently
	// fast-forward the wrong branch. Treat a changed branch exactly like the
	// existing "not on main" case.
	//
	// A probe error here must NOT collapse into MainSyncSkipped: mainSyncSkip
	// (decide.go) treats Skipped as ungated, so folding a genuine error (repo
	// removed mid-pass, unreadable .git, a WaitDelay kill) into it would
	// silently downgrade the gate to permissive on a broken git state -- the
	// same failure-class conflation the initial symbolic-ref on-main check
	// above already avoids (#822 review round 2 fix #1).
	branchNow, err := execGit(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return MainSyncFailed, fmt.Sprintf("re-verifying HEAD before merge failed: %v", err)
	}
	if branchNow != "main" {
		return MainSyncSkipped, "checked-out branch changed before merge"
	}

	// Same hook-suppression rationale as the fetch above: a pure fast-forward
	// still runs post-merge, so this must stay unattended-safe too.
	if _, err := execGit(dir, "-c", "core.hooksPath=/dev/null", "merge", "--ff-only", "origin/main"); err != nil {
		return MainSyncFailed, fmt.Sprintf("git merge --ff-only origin/main failed: %v", err)
	}
	return MainSyncSynced, "fast-forwarded to origin/main"
}

// syncMains runs syncMain once per repo in repos, unconditionally logging
// each repo's outcome (independent of ticket count) and returning a map
// keyed by repo for CollectTickets to stamp onto every Ticket. One repo's
// outcome never affects another's -- mirrors CollectTickets' best-effort
// per-repo loop.
//
// Log lines intentionally avoid the " skip:" / " dispatch " substrings --
// lazyboards classifies decision lines by matching them (formatDecision's
// doc comment, dispatch.go).
func syncMains(repos []RepoConfig, out io.Writer, dryRun bool) map[string]MainSync {
	if out == nil {
		out = os.Stdout
	}
	result := make(map[string]MainSync, len(repos))
	for _, rc := range repos {
		status, detail := syncMain(rc.Dir, dryRun)
		result[rc.Repo] = status
		// Collapse any newlines in detail before logging: isAncestor's wrapped
		// error can embed merge-base's combined output, which could in
		// principle contain newlines, and this log format is one line per
		// repo -- downstream tooling (lazyboards) parses it as such (#822
		// review round 2 fix #3).
		detail = strings.ReplaceAll(detail, "\n", "; ")
		logf(out, "dispatch: main sync %s: %s (%s)\n", rc.Repo, status, detail)
	}
	return result
}
