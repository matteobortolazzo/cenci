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

// remoteMainAuthRef is the fully-qualified remote-tracking ref for origin's
// main, never the short "origin/main" form (#877). Since #903 the autonomy
// probe no longer reads it by name: confirmRemoteMainOID reads this ref's
// object ID (the `git for-each-ref` pattern, matched by exact refname) and
// pins mainSyncResult.AutonomyRef to it only when it equals the remote's
// current `refs/heads/main` tip, because a successful `git fetch origin` does
// not prove this ref was updated (a deleted or renamed remote main leaves it
// stale, a non-standard remote.origin.fetch may never write it). It is still
// read by name, unconfirmed, by QueryRepoAutonomy (fleet.go), the status
// surface that never authorizes a pickup.
//
// It is deliberately fully-qualified because git's own rev-parse precedence
// resolves refs/heads/<name> before refs/remotes/<name> -- the short
// "origin/main" could in principle be shadowed by a local branch literally
// named "origin/main" (plan Q1), which would let a local, possibly
// unpushed/malicious grant leak into an authorization decision that must only
// ever be read from the confirmed remote object.
//
// The neighbouring `merge-base --is-ancestor`/`merge --ff-only` calls in
// this file deliberately KEEP their existing short "origin/main" form below
// -- they are ordinary fast-forward-sync operations, not the authorization
// read, and must NOT be "unified" to match this constant.
const remoteMainAuthRef = "refs/remotes/origin/main"

// remoteMainHeadRef is the ref confirmRemoteMainOID asks origin about: the
// remote's own main branch, as the remote advertises it.
const remoteMainHeadRef = "refs/heads/main"

// isObjectID reports whether s is a full git object ID: exactly 40 (SHA-1) or
// 64 (SHA-256) lowercase hex characters, nothing else.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// nonEmptyLines splits git output into its non-blank lines.
func nonEmptyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// parseLsRemoteMainOID extracts the object ID from `git ls-remote origin
// refs/heads/main` output: exactly one line, `<oid>\t<refname>`, whose
// refname is exactly refs/heads/main and whose OID is a valid object ID. Zero
// lines, several lines, any other refname or a malformed OID is not a
// confirmation.
func parseLsRemoteMainOID(out string) (string, bool) {
	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		return "", false
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 2 || fields[1] != remoteMainHeadRef || !isObjectID(fields[0]) {
		return "", false
	}
	return fields[0], true
}

// parseTrackingRefOID extracts the object ID from `git for-each-ref
// --format=%(objectname) %(refname) refs/remotes/origin/main` output. The
// pattern is a prefix match, so refs/remotes/origin/main/x is listed too: only
// the single line whose refname equals remoteMainAuthRef counts, every other
// line is ignored, and its OID must be a valid object ID.
func parseTrackingRefOID(out string) (string, bool) {
	var oid string
	matches := 0
	for _, line := range nonEmptyLines(out) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != remoteMainAuthRef {
			continue
		}
		oid = fields[0]
		matches++
	}
	if matches != 1 || !isObjectID(oid) {
		return "", false
	}
	return oid, true
}

// confirmRemoteMainOID (#903) returns the object ID the autonomy probe may
// read authorization from: the OID of refs/remotes/origin/main, but only when
// it equals what `git ls-remote --exit-code origin refs/heads/main` reports
// for the remote's current main tip. Run once, right after a successful
// `git fetch origin`. On any failure, absence, mismatch or malformed output it
// returns oid == "" and a one-line reason (newlines collapsed, never git's
// stderr for the network call, never config content) that
// probeRepoAutonomies logs; the repo is then RepoAutonomyFetchUnconfirmed.
//
// Both calls go through execGitSeparate (gitTimeout, gitWaitDelay, gitEnv),
// with stdout parsed separately from stderr. ls-remote runs no hooks, so it
// needs no core.hooksPath override.
func confirmRemoteMainOID(dir string) (oid, reason string) {
	unconfirmed := func(why string) (string, string) {
		return "", strings.ReplaceAll(why, "\n", "; ")
	}

	lsOut, _, lsErr := execGitSeparate(dir, "ls-remote", "--exit-code", "origin", remoteMainHeadRef)
	if lsErr != nil {
		// --exit-code makes "no matching ref" exit 2, distinct from a command
		// failure (exit 128, a timeout kill, a missing binary).
		var exitErr *exec.ExitError
		if errors.As(lsErr, &exitErr) && exitErr.ExitCode() == 2 {
			return unconfirmed("remote main not advertised by origin")
		}
		// git's stderr is deliberately dropped: a network call's stderr can
		// echo a remote URL (syncMain's fetch Detail leaves it out too).
		return unconfirmed(fmt.Sprintf("git ls-remote failed: %v", lsErr))
	}
	remoteOID, ok := parseLsRemoteMainOID(lsOut)
	if !ok {
		return unconfirmed("git ls-remote output for " + remoteMainHeadRef + " was malformed")
	}

	trackOut, trackErrOut, trackErr := execGitSeparate(dir, "for-each-ref", "--format=%(objectname) %(refname)", remoteMainAuthRef)
	if trackErr != nil {
		return unconfirmed(gitFailureDetail("git for-each-ref failed", trackErr, trackErrOut))
	}
	trackOID, ok := parseTrackingRefOID(trackOut)
	if !ok {
		return unconfirmed("tracking ref " + remoteMainAuthRef + " absent or malformed")
	}
	if trackOID != remoteOID {
		return unconfirmed("tracking ref " + remoteMainAuthRef + " does not match remote main tip")
	}
	return remoteOID, ""
}

// mainSyncResult is syncMain's full classified outcome (#851/#877):
// Status/Detail are the pre-#851 pair (now bundled into a struct rather than
// two return values). FreshRef names the git ref that staleness computation
// (planfile.CommitsBehindRef) should read against for this repo this pass --
// "origin/main" only in the dry-run strictly-behind branch (the fetched blob
// a real pass would fast-forward to), "HEAD" in every other case (including
// the real, non-dry-run strictly-behind branch, which has already merged
// into local HEAD by the time it returns). Sharing one FreshRef per repo per
// pass is what gives dry-run parity with the subsequent real pass (plan
// Q&A 3). AutonomyRef (#877, #903) is a distinct field: the confirmed object
// ID of the remote's main tip, non-empty if and only if `git fetch origin`
// succeeded in this pass AND refs/remotes/origin/main equals what `git
// ls-remote origin refs/heads/main` reports (confirmRemoteMainOID). It is the
// exact object the repo-autonomy probe must read authorization from, and
// unlike FreshRef it never falls back to local HEAD, so a fetch outage, a
// deleted or renamed remote main or a stale tracking ref can never silently
// authorize off a stale or unpushed grant. AutonomyDetail carries the reason
// AutonomyRef is empty when the fetch succeeded but the tip could not be
// confirmed; it is empty on a confirmed OID and on every pre-fetch and
// fetch-failed return (the status alone explains those).
type mainSyncResult struct {
	Status         MainSync
	Detail         string
	FreshRef       string
	AutonomyRef    string
	AutonomyDetail string
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
	// origin` above succeeded, so each one carries the same confirmation
	// result (#877, #903): the confirmed remote main OID in AutonomyRef (or ""
	// plus an AutonomyDetail reason when the remote tip could not be
	// confirmed). A successful fetch alone is not enough -- it does not prove
	// refs/remotes/origin/main was updated -- so the OID is checked against
	// the remote's current tip once, here, before any merge-base probe, and
	// the same pair is threaded through every return below via `result`: even
	// a return path whose Status reports a failure of some later step (a
	// merge-base probe error, a pre-merge re-check error, or the fast-forward
	// merge itself failing) still had a successful fetch this pass, and
	// Status, Detail and FreshRef never depend on the confirmation. The merge
	// never moves refs/remotes/origin/main, so confirming before it is
	// equivalent to confirming after.
	autonomyRef, autonomyDetail := confirmRemoteMainOID(dir)
	result := func(status MainSync, detail, freshRef string) mainSyncResult {
		return mainSyncResult{Status: status, Detail: detail, FreshRef: freshRef, AutonomyRef: autonomyRef, AutonomyDetail: autonomyDetail}
	}

	// localBehindOrEqual: local HEAD is an ancestor of (or equal to)
	// origin/main. localAheadOrEqual: origin/main is an ancestor of (or
	// equal to) local HEAD. A genuine merge-base command error (not merely
	// "not an ancestor") must never be reported as MainSyncDiverged -- that
	// would mask a real git failure as a false divergence (#822 review
	// fix #1).
	localBehindOrEqual, err := isAncestor(dir, "HEAD", "origin/main")
	if err != nil {
		return result(MainSyncFailed, fmt.Sprintf("git merge-base --is-ancestor check failed: %v", err), "HEAD")
	}
	localAheadOrEqual, err := isAncestor(dir, "origin/main", "HEAD")
	if err != nil {
		return result(MainSyncFailed, fmt.Sprintf("git merge-base --is-ancestor check failed: %v", err), "HEAD")
	}

	switch {
	case localBehindOrEqual && localAheadOrEqual:
		return result(MainSyncSynced, "already up to date", "HEAD")
	case localAheadOrEqual:
		return result(MainSyncSynced, "local main is ahead of origin/main", "HEAD")
	case !localBehindOrEqual:
		return result(MainSyncDiverged, "local main and origin/main have diverged", "HEAD")
	}

	// Strictly behind: there is something to fast-forward. #851: this is the
	// sole case where FreshRef is "origin/main" rather than "HEAD" -- the
	// dry-run branch never merges, so staleness/config reads must consult the
	// fetched blob, not stale local HEAD, to match what the subsequent real
	// pass would render.
	if dryRun {
		return result(MainSyncSynced, "would fast-forward origin/main (dry-run, not merged)", "origin/main")
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
		return result(MainSyncFailed, fmt.Sprintf("re-verifying HEAD before merge failed: %v", err), "HEAD")
	}
	if branchNow != "main" {
		// Fetch already succeeded before this mid-pass branch change was
		// discovered (#877): the remote-tip confirmation is about the fetch,
		// not the merge, so it carries through even though the merge never
		// runs.
		return result(MainSyncNotMain, "checked-out branch changed before merge", "HEAD")
	}

	// Same hook-suppression rationale as the fetch above: a pure fast-forward
	// still runs post-merge, so this must stay unattended-safe too.
	if _, err := execGit(dir, "-c", "core.hooksPath=/dev/null", "merge", "--ff-only", "origin/main"); err != nil {
		return result(MainSyncFailed, fmt.Sprintf("git merge --ff-only origin/main failed: %v", err), "HEAD")
	}
	return result(MainSyncSynced, "fast-forwarded to origin/main", "HEAD")
}

// syncMains runs syncMain once per repo in repos, unconditionally logging
// each repo's outcome (independent of ticket count) and returning a map
// keyed by repo. One repo's outcome never affects another's -- mirrors
// CollectTickets' best-effort per-repo loop.
//
// #851: the return type is now map[string]mainSyncResult (Status/Detail/
// FreshRef/AutonomyRef/AutonomyDetail) rather than map[string]MainSync, so
// downstream readers can resolve the per-repo object each concern needs this
// pass: readPlansForRepos reads FreshRef for plan staleness, while the
// autonomy probe (#877) reads the distinct AutonomyRef -- the remote-confirmed
// OID (#903) -- for authorization; the two are no longer the same ref.
// syncStatuses adapts this map back to
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
