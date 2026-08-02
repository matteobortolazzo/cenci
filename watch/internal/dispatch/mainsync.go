package dispatch

import (
	"bytes"
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

// execGitSeparate runs `git -C dir <args...>` exactly like execGit (same
// gitTimeout-bounded context, gitEnv hardening, gitWaitDelay-bounded Wait),
// but keeps stdout and stderr in separate buffers rather than combining them
// (#851): a caller that parses the command's stdout as data (e.g. the
// autonomy probe's `git show <ref>:.cenci/config.json`, read as JSON) must
// never let a benign stderr line splice into the parsed content -- exactly
// the failure mode plan Q&A 3 calls out for execGit's CombinedOutput.
func execGitSeparate(dir string, args ...string) (stdout string, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = gitEnv()
	cmd.WaitDelay = gitWaitDelay
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return strings.TrimSpace(outBuf.String()), strings.TrimSpace(errBuf.String()), err
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

// remoteMainAuthRef is the exact object the repo-autonomy probe
// (autonomy.go) must read `planning.autonomy` authorization from (#877): the
// fully-qualified remote-tracking ref for origin's main, never the short
// "origin/main" form. It is deliberately fully-qualified because git's own
// rev-parse precedence resolves refs/heads/<name> before refs/remotes/<name>
// -- the short "origin/main" could in principle be shadowed by a local
// branch literally named "origin/main" (plan Q1), which would let a local,
// possibly unpushed/malicious grant leak into an authorization decision that
// must only ever be read from the confirmed remote object.
//
// The neighbouring `merge-base --is-ancestor`/`merge --ff-only` calls in
// this file deliberately KEEP their existing short "origin/main" form below
// -- they are ordinary fast-forward-sync operations, not the authorization
// read, and must NOT be "unified" to match this constant.
const remoteMainAuthRef = "refs/remotes/origin/main"

// mainSyncResult is syncMain's full classified outcome (#851/#877):
// Status/Detail are the pre-#851 pair (now bundled into a struct rather than
// two return values). FreshRef names the git ref that staleness computation
// (planfile.CommitsBehindRef) should read against for this repo this pass --
// "origin/main" only in the dry-run strictly-behind branch (the fetched blob
// a real pass would fast-forward to), "HEAD" in every other case (including
// the real, non-dry-run strictly-behind branch, which has already merged
// into local HEAD by the time it returns). Sharing one FreshRef per repo per
// pass is what gives dry-run parity with the subsequent real pass (plan
// Q&A 3). AutonomyRef (#877) is a distinct field: non-empty if and only if
// `git fetch origin` succeeded in this pass, and always exactly
// remoteMainAuthRef when set -- it names the exact object the repo-autonomy
// probe must read authorization from, and unlike FreshRef it never falls
// back to local HEAD, so a fetch outage can never silently authorize off a
// stale or unpushed local grant.
type mainSyncResult struct {
	Status      MainSync
	Detail      string
	FreshRef    string
	AutonomyRef string
}

// syncMain runs the once-per-pass local `main` sync for one enrolled repo
// (#822): `git fetch origin`, then a fast-forward-only merge of origin/main
// into main -- but only when dir is checked out on main, and never via
// --reset, --force, or `git fetch origin main:main`. It returns the
// classified outcome (mainSyncResult) for logging and downstream gating.
//
// dir == "" is never probed (mirrors probeStage, collect.go): resolving a
// repo root from the daemon's own cwd would risk syncing an unrelated repo.
// It is the one case that stays the ungated MainSyncSkipped zero value ("no
// sync attempted at all" -- no Dir was configured); a configured, non-empty
// Dir that is absent from disk is the new gated MainSyncMissing (#851, via
// the os.Stat precheck below).
//
// Divergence is classified by two `git merge-base --is-ancestor` probes,
// not by matching git's merge failure message -- deterministic and
// locale-independent. The merge is skipped entirely in the two cases where
// it would be a no-op (already up to date / local ahead) or provably doomed
// (diverged); it only actually runs when local main is strictly behind.
// dryRun still fetches and classifies (an accurate gate needs the fetch) but
// never runs the merge itself.
func syncMain(dir string, dryRun bool) mainSyncResult {
	if dir == "" {
		// Pre-fetch: no remote-confirmed object this pass.
		return mainSyncResult{Status: MainSyncSkipped, Detail: "no repo dir configured", FreshRef: "HEAD", AutonomyRef: ""}
	}

	// #851: a configured, non-empty Dir that does not exist on disk at all
	// must not fall through to a git command (which would misreport it as
	// "not a git repository", MainSyncFailed) -- it gets its own gated state.
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			// Pre-fetch: no remote-confirmed object this pass.
			return mainSyncResult{Status: MainSyncMissing, Detail: "configured dir does not exist on disk", FreshRef: "HEAD", AutonomyRef: ""}
		}
		// Pre-fetch: no remote-confirmed object this pass.
		return mainSyncResult{Status: MainSyncFailed, Detail: fmt.Sprintf("stat %s failed: %v", dir, err), FreshRef: "HEAD", AutonomyRef: ""}
	}

	branch, err := execGit(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		// symbolic-ref fails both for a detached HEAD and for a dir that
		// isn't a git repo at all -- rev-parse --git-dir distinguishes them.
		if _, gitDirErr := execGit(dir, "rev-parse", "--git-dir"); gitDirErr != nil {
			// Pre-fetch: no remote-confirmed object this pass.
			return mainSyncResult{Status: MainSyncFailed, Detail: "not a git repository", FreshRef: "HEAD", AutonomyRef: ""}
		}
		// #851: previously the ungated MainSyncSkipped -- a detached HEAD is
		// untrustworthy for plan-freshness/pipeline-stage gating exactly like
		// a non-main checkout, so it now gates every pickup in the repo too.
		// Pre-fetch: no remote-confirmed object this pass.
		return mainSyncResult{Status: MainSyncDetached, Detail: "HEAD is detached", FreshRef: "HEAD", AutonomyRef: ""}
	}
	if branch != "main" {
		// #851: previously the ungated MainSyncSkipped -- see MainSyncDetached
		// above for the rationale.
		// Pre-fetch: no remote-confirmed object this pass.
		return mainSyncResult{Status: MainSyncNotMain, Detail: fmt.Sprintf("checked-out branch %q is not main", branch), FreshRef: "HEAD", AutonomyRef: ""}
	}

	// -c core.hooksPath=/dev/null (global option, must precede the
	// subcommand): an automated dispatch-loop sync must never execute
	// repo-supplied git hooks (e.g. a committed/husky-style post-merge hook)
	// unattended (#822 review round 2 fix #2).
	if _, err := execGit(dir, "-c", "core.hooksPath=/dev/null", "fetch", "origin"); err != nil {
		// Fetch itself failed: no remote-confirmed object this pass.
		return mainSyncResult{Status: MainSyncFetchFailed, Detail: fmt.Sprintf("git fetch origin failed: %v", err), FreshRef: "HEAD", AutonomyRef: ""}
	}

	// Every return path below this point is reached only after `git fetch
	// origin` above succeeded, so each one sets AutonomyRef to
	// remoteMainAuthRef (#877): the fetch, not the subsequent merge, is what
	// confirms the remote object -- even a return path whose Status reports a
	// failure of some later step (a merge-base probe error, a pre-merge
	// re-check error, or the fast-forward merge itself failing) still had a
	// successful fetch this pass.

	// localBehindOrEqual: local HEAD is an ancestor of (or equal to)
	// origin/main. localAheadOrEqual: origin/main is an ancestor of (or
	// equal to) local HEAD. A genuine merge-base command error (not merely
	// "not an ancestor") must never be reported as MainSyncDiverged -- that
	// would mask a real git failure as a false divergence (#822 review
	// fix #1).
	localBehindOrEqual, err := isAncestor(dir, "HEAD", "origin/main")
	if err != nil {
		return mainSyncResult{Status: MainSyncFailed, Detail: fmt.Sprintf("git merge-base --is-ancestor check failed: %v", err), FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
	}
	localAheadOrEqual, err := isAncestor(dir, "origin/main", "HEAD")
	if err != nil {
		return mainSyncResult{Status: MainSyncFailed, Detail: fmt.Sprintf("git merge-base --is-ancestor check failed: %v", err), FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
	}

	switch {
	case localBehindOrEqual && localAheadOrEqual:
		return mainSyncResult{Status: MainSyncSynced, Detail: "already up to date", FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
	case localAheadOrEqual:
		return mainSyncResult{Status: MainSyncSynced, Detail: "local main is ahead of origin/main", FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
	case !localBehindOrEqual:
		return mainSyncResult{Status: MainSyncDiverged, Detail: "local main and origin/main have diverged", FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
	}

	// Strictly behind: there is something to fast-forward. #851: this is the
	// sole case where FreshRef is "origin/main" rather than "HEAD" -- the
	// dry-run branch never merges, so staleness/config reads must consult the
	// fetched blob, not stale local HEAD, to match what the subsequent real
	// pass would render.
	if dryRun {
		return mainSyncResult{Status: MainSyncSynced, Detail: "would fast-forward origin/main (dry-run, not merged)", FreshRef: "origin/main", AutonomyRef: remoteMainAuthRef}
	}

	// Re-verify HEAD is still on main immediately before merging (#822
	// review fix #4, TOCTOU): `git fetch` above is a network call that can
	// take seconds, and something else could have checked out a different
	// branch during that window -- `merge --ff-only` trusts the *current*
	// HEAD, so relying solely on the early check above could silently
	// fast-forward the wrong branch. Treat a changed branch exactly like the
	// existing "not on main" case.
	//
	// A probe error here must NOT collapse into MainSyncNotMain (#851,
	// previously MainSyncSkipped): mainSyncSkip (decide.go) already gates
	// both, but folding a genuine error (repo removed mid-pass, unreadable
	// .git, a WaitDelay kill) into the legitimate-branch-change reason would
	// blur two different failure classes together -- the same failure-class
	// conflation the initial symbolic-ref on-main check above already avoids
	// (#822 review round 2 fix #1).
	branchNow, err := execGit(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return mainSyncResult{Status: MainSyncFailed, Detail: fmt.Sprintf("re-verifying HEAD before merge failed: %v", err), FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
	}
	if branchNow != "main" {
		// Fetch already succeeded before this mid-pass branch change was
		// discovered (#877): AutonomyRef's invariant is about the fetch, not
		// the merge, so it must stay set even though the merge never runs.
		return mainSyncResult{Status: MainSyncNotMain, Detail: "checked-out branch changed before merge", FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
	}

	// Same hook-suppression rationale as the fetch above: a pure fast-forward
	// still runs post-merge, so this must stay unattended-safe too.
	if _, err := execGit(dir, "-c", "core.hooksPath=/dev/null", "merge", "--ff-only", "origin/main"); err != nil {
		return mainSyncResult{Status: MainSyncFailed, Detail: fmt.Sprintf("git merge --ff-only origin/main failed: %v", err), FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
	}
	return mainSyncResult{Status: MainSyncSynced, Detail: "fast-forwarded to origin/main", FreshRef: "HEAD", AutonomyRef: remoteMainAuthRef}
}

// syncMains runs syncMain once per repo in repos, unconditionally logging
// each repo's outcome (independent of ticket count) and returning a map
// keyed by repo. One repo's outcome never affects another's -- mirrors
// CollectTickets' best-effort per-repo loop.
//
// #851: the return type is now map[string]mainSyncResult (Status/Detail/
// FreshRef/AutonomyRef) rather than map[string]MainSync, so downstream
// readers can resolve the per-repo ref each concern needs this pass:
// readPlansForRepos reads FreshRef for plan staleness, while the autonomy
// probe (#877) reads the distinct AutonomyRef for authorization -- the two
// are no longer the same ref. syncStatuses adapts this map back to
// map[string]MainSync for CollectTickets, whose own signature stays
// unchanged.
//
// Log lines intentionally avoid the " skip:" / " dispatch " substrings --
// lazyboards classifies decision lines by matching them (formatDecision's
// doc comment, dispatch.go).
func syncMains(repos []RepoConfig, out io.Writer, dryRun bool) map[string]mainSyncResult {
	if out == nil {
		out = os.Stdout
	}
	result := make(map[string]mainSyncResult, len(repos))
	for _, rc := range repos {
		r := syncMain(rc.Dir, dryRun)
		result[rc.Repo] = r
		// Collapse any newlines in detail before logging: isAncestor's wrapped
		// error can embed merge-base's combined output, which could in
		// principle contain newlines, and this log format is one line per
		// repo -- downstream tooling (lazyboards) parses it as such (#822
		// review round 2 fix #3).
		detail := strings.ReplaceAll(r.Detail, "\n", "; ")
		logf(out, "dispatch: main sync %s: %s (%s)\n", rc.Repo, r.Status, detail)
	}
	return result
}

// syncStatuses adapts syncMains' map[string]mainSyncResult back into the
// map[string]MainSync shape CollectTickets expects (#851): extracting just
// the Status half of each entry keeps CollectTickets' own signature (and the
// reconciler's nil-map "never syncs" call) untouched by syncMains' richer
// return type.
func syncStatuses(syncs map[string]mainSyncResult) map[string]MainSync {
	out := make(map[string]MainSync, len(syncs))
	for repo, r := range syncs {
		out[repo] = r.Status
	}
	return out
}
