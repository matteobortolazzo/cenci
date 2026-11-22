package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gitTimeout bounds every individual git invocation the local default-branch sync
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
	rawOut, rawErr, err := execGitSeparateRaw(dir, args...)
	return strings.TrimSpace(rawOut), strings.TrimSpace(rawErr), err
}

// execGitSeparateRaw is execGitSeparate without the whitespace trim, for the
// NUL-delimited (-z) output whose leading space is data: `git status
// --porcelain -z` starts a " M path" entry with a space that a trim would eat
// (#1475). Same bounded context, gitEnv hardening and WaitDelay.
func execGitSeparateRaw(dir string, args ...string) (stdout string, stderr string, err error) {
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
	return outBuf.String(), errBuf.String(), err
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

// remoteTrackingRef is the fully-qualified remote-tracking ref for origin's
// branch, never the short "origin/<branch>" form (#877, generalized from the
// literal main by #1475). Since #903 the autonomy probe does not read it by
// name: confirmRemoteOID reads this ref's object ID (the `git for-each-ref`
// pattern, matched by exact refname) and pins mainSyncResult.AutonomyRef to it
// only when it equals the remote's current `refs/heads/<branch>` tip, because a
// successful `git fetch origin` does not prove this ref was updated (a deleted
// or renamed remote branch leaves it stale, a non-standard remote.origin.fetch
// may never write it). It is still read by name, unconfirmed, by
// QueryRepoAutonomy (fleet.go), the status surface that never authorizes a
// pickup.
//
// It is deliberately fully-qualified because git's own rev-parse precedence
// resolves refs/heads/<name> before refs/remotes/<name> -- the short
// "origin/<branch>" can be shadowed by a local branch literally named that
// (plan Q1), which would let a local, possibly unpushed/malicious grant leak
// into an authorization decision that must only ever be read from the confirmed
// remote object. Since #1475 every git operation in the sync -- the
// merge-base probes and the `merge --ff-only` included -- uses this form, so
// the fast-forward can never be pointed at a shadowing local branch either.
// Only log and detail text keeps the short form, for the main-repo strings
// that predate it.
func remoteTrackingRef(branch string) string { return "refs/remotes/origin/" + branch }

// remoteHeadRef is the ref confirmRemoteOID asks origin about: the remote's own
// branch, as the remote advertises it.
func remoteHeadRef(branch string) string { return "refs/heads/" + branch }

// maxBranchNameLen caps a default-branch name before any git call sees it: far
// above any real branch name, far below an argv hazard.
const maxBranchNameLen = 255

// invalidDefaultBranchReason returns "" when name is a usable remote default
// branch name, else a one-line reason (#1475). Usable means non-empty, bounded,
// not option-shaped, and accepted by `git check-ref-format --branch` with that
// command echoing the name back unchanged -- the echo check rejects a value git
// would expand (`@{-1}` prints the previous branch), so the name is never
// reinterpreted. The metadata read is the only authority for the name (never
// local `origin/HEAD`, a local branch or config), and this check is the only
// thing between that remote-supplied string and every ref the sync builds.
func invalidDefaultBranchReason(dir, name string) string {
	if name == "" {
		return "remote default branch name is empty"
	}
	invalid := fmt.Sprintf("remote default branch name %.80q is not a valid branch name", name)
	if len(name) > maxBranchNameLen || strings.HasPrefix(name, "-") {
		return invalid
	}
	out, _, err := execGitSeparate(dir, "check-ref-format", "--branch", name)
	if err != nil || out != name {
		return invalid
	}
	return ""
}

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

// parseLsRemoteOID extracts the object ID from `git ls-remote origin
// <headRef>` output (headRef is refs/heads/<default>): exactly one line,
// `<oid>\t<refname>`, whose refname is exactly headRef and whose OID is a valid
// object ID. Zero lines, several lines, any other refname or a malformed OID is
// not a confirmation.
func parseLsRemoteOID(out, headRef string) (string, bool) {
	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		return "", false
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 2 || fields[1] != headRef || !isObjectID(fields[0]) {
		return "", false
	}
	return fields[0], true
}

// parseTrackingRefOID extracts the object ID from `git for-each-ref
// --format=%(objectname) %(refname) <trackRef>` output. The pattern is a prefix
// match, so refs/remotes/origin/<d>/x is listed too: only the single line whose
// refname equals trackRef counts, every other line is ignored, and its OID must
// be a valid object ID.
func parseTrackingRefOID(out, trackRef string) (string, bool) {
	var oid string
	matches := 0
	for _, line := range nonEmptyLines(out) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != trackRef {
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

// readTrackingRefOID returns the object ID of the exact remote-tracking ref
// trackRef, or "" and a one-line reason when it is absent, ambiguous or the read
// fails (#1475). syncMain calls it right after `git fetch origin`: a successful
// fetch does not prove the ref exists (the remote may lack the branch).
func readTrackingRefOID(dir, trackRef string) (oid, reason string) {
	out, errOut, err := execGitSeparate(dir, "for-each-ref", "--format=%(objectname) %(refname)", trackRef)
	if err != nil {
		return "", strings.ReplaceAll(gitFailureDetail("git for-each-ref failed", err, errOut), "\n", "; ")
	}
	oid, ok := parseTrackingRefOID(out, trackRef)
	if !ok {
		return "", "tracking ref " + trackRef + " absent or malformed after git fetch origin"
	}
	return oid, ""
}

// confirmRemoteOID (#903, generalized by #1475) returns the object ID the
// autonomy probe may read authorization from: trackOID, the OID of
// refs/remotes/origin/<branch>, but only when it equals what `git ls-remote
// --exit-code origin refs/heads/<branch>` reports for the remote's current tip.
// Run once, right after a successful `git fetch origin` and the tracking-ref
// read. On any failure, absence, mismatch or malformed output it returns
// oid == "" and a one-line reason (newlines collapsed, never git's stderr for
// the network call, never config content) that probeRepoAutonomies logs; the
// repo is then RepoAutonomyFetchUnconfirmed.
//
// The call goes through execGitSeparate (gitTimeout, gitWaitDelay, gitEnv),
// with stdout parsed separately from stderr. ls-remote runs no hooks, so it
// needs no core.hooksPath override.
func confirmRemoteOID(dir, branch, trackOID string) (oid, reason string) {
	unconfirmed := func(why string) (string, string) {
		return "", strings.ReplaceAll(why, "\n", "; ")
	}
	headRef, trackRef := remoteHeadRef(branch), remoteTrackingRef(branch)

	lsOut, _, lsErr := execGitSeparate(dir, "ls-remote", "--exit-code", "origin", headRef)
	if lsErr != nil {
		// --exit-code makes "no matching ref" exit 2, distinct from a command
		// failure (exit 128, a timeout kill, a missing binary).
		var exitErr *exec.ExitError
		if errors.As(lsErr, &exitErr) && exitErr.ExitCode() == 2 {
			return unconfirmed("remote " + branch + " not advertised by origin")
		}
		// git's stderr is deliberately dropped: a network call's stderr can
		// echo a remote URL (syncMain's fetch Detail leaves it out too).
		return unconfirmed(fmt.Sprintf("git ls-remote failed: %v", lsErr))
	}
	remoteOID, ok := parseLsRemoteOID(lsOut, headRef)
	if !ok {
		return unconfirmed("git ls-remote output for " + headRef + " was malformed")
	}
	if trackOID != remoteOID {
		return unconfirmed("tracking ref " + trackRef + " does not match remote " + branch + " tip")
	}
	return remoteOID, ""
}

// porcelainEntry is one `git status --porcelain -z` record: its two status
// letters and the path(s) it names (a rename or copy names both sides).
type porcelainEntry struct {
	code  string
	paths []string
}

// parsePorcelainZ splits raw `git status --porcelain -z` output (v1: "XY path",
// NUL-terminated; a rename or copy is followed by its origin path as one more
// NUL-terminated token). Anything shorter than "XY p" is skipped.
func parsePorcelainZ(raw string) []porcelainEntry {
	tokens := strings.Split(raw, "\x00")
	var entries []porcelainEntry
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if len(tok) < 4 {
			continue
		}
		e := porcelainEntry{code: strings.TrimSpace(tok[:2]), paths: []string{tok[3:]}}
		if (tok[0] == 'R' || tok[0] == 'C' || tok[1] == 'R' || tok[1] == 'C') && i+1 < len(tokens) {
			i++
			e.paths = append(e.paths, tokens[i])
		}
		entries = append(entries, e)
	}
	return entries
}

// maxDirtyListing bounds how many porcelain entries a dirty detail carries.
const maxDirtyListing = 10

// dirtyFastForwardDetail classifies a failed `merge --ff-only trackRef` (#1475):
// it returns (detail, true) only when the working tree's `git status
// --porcelain` entries overlap the files the fast-forward would rewrite
// (`git diff --name-only HEAD <trackRef>`, renames split into both sides). Any
// other failure -- a lock file, a hook-less refusal, a read failure here -- is
// not "dirty": the caller keeps the plain failed status. The detail is a
// bounded one-line listing of the porcelain entries. Nothing is stashed, reset
// or discarded.
func dirtyFastForwardDetail(dir, branch, trackRef string) (string, bool) {
	statusOut, _, err := execGitSeparateRaw(dir, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return "", false
	}
	entries := parsePorcelainZ(statusOut)
	if len(entries) == 0 {
		return "", false
	}
	diffOut, _, err := execGitSeparateRaw(dir, "diff", "--name-only", "--no-renames", "-z", "HEAD", trackRef)
	if err != nil {
		return "", false
	}
	incoming := map[string]bool{}
	for _, p := range strings.Split(diffOut, "\x00") {
		if p != "" {
			incoming[p] = true
		}
	}
	overlap := false
	for _, e := range entries {
		for _, p := range e.paths {
			if incoming[p] {
				overlap = true
			}
		}
	}
	if !overlap {
		return "", false
	}
	// Each path is a tracked or untracked file name -- untrusted bytes -- so it is
	// strconv-quoted like every other untrusted value in the sync log line: an ESC,
	// a tab or a newline reaches the one-line log as an escape, never raw.
	var listed []string
	for i, e := range entries {
		if i == maxDirtyListing {
			listed = append(listed, fmt.Sprintf("... (+%d more)", len(entries)-maxDirtyListing))
			break
		}
		quoted := make([]string, len(e.paths))
		for j, p := range e.paths {
			quoted[j] = strconv.Quote(p)
		}
		listed = append(listed, e.code+" "+strings.Join(quoted, " <- "))
	}
	return fmt.Sprintf("git merge --ff-only origin/%s blocked by local changes: %s", branch, strings.Join(listed, "; ")), true
}

// mainSyncResult is syncMain's full classified outcome (#851/#877):
// Status/Detail are the pre-#851 pair (now bundled into a struct rather than
// two return values). FreshRef names the git ref that staleness computation
// (planfile.CommitsBehindRef) should read against for this repo this pass --
// the fully qualified refs/remotes/origin/<default> only in the dry-run
// strictly-behind branch (the fetched blob a real pass would fast-forward to),
// "HEAD" in every other case (including the real, non-dry-run strictly-behind
// branch, which has already merged into local HEAD by the time it returns).
// Sharing one FreshRef per repo per pass is what gives dry-run parity with the
// subsequent real pass (plan Q&A 3). AutonomyRef (#877, #903) is a distinct
// field: the confirmed object ID of the remote default branch's tip, non-empty
// if and only if `git fetch origin` succeeded in this pass AND
// refs/remotes/origin/<default> equals what `git ls-remote origin
// refs/heads/<default>` reports (confirmRemoteOID). It is the exact object the
// repo-autonomy probe must read authorization from, and unlike FreshRef it
// never falls back to local HEAD, so a fetch outage, a deleted or renamed
// remote default branch or a stale tracking ref can never silently authorize
// off a stale or unpushed grant. AutonomyDetail carries the reason
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

// syncMain runs the once-per-pass local default-branch sync for one enrolled
// repo (#822, generalized from the literal `main` by #1475): `git fetch
// origin`, then a fast-forward-only merge of refs/remotes/origin/<default> into
// the default branch -- but only when dir is checked out on it, and never via
// --reset, --force, `git stash`, a checkout or `git fetch origin main:main`.
// defaultBranch is the name the repository metadata `default_branch` gave
// (syncMains resolves it once per repo per pass); "" or a name that is not a
// valid branch name is MainSyncDefaultBranchUnreadable and runs no fetch. A repo
// whose default is `main` keeps the exact not_main, detached and diverged
// statuses and detail strings; a checkout on another branch of any other
// default is MainSyncNotDefault. It returns the classified outcome
// (mainSyncResult) for logging and downstream gating.
//
// Order: dir/stat/repo checks, then detached, then the default-branch name
// check, then the branch compare, then the fetch, then the tracking-ref
// existence check (MainSyncFetchFailed when the remote lacks the branch), then
// the remote-tip confirmation, then the merge-base probes. Every git operation
// after the fetch names the fully qualified refs/remotes/origin/<default>, never
// the short form a local branch named origin/<default> would shadow.
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
// (diverged); it only actually runs when the local default branch is strictly behind.
// dryRun still fetches and classifies (an accurate gate needs the fetch) but
// never runs the merge itself.
func syncMain(dir, defaultBranch string, dryRun bool) mainSyncResult {
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

	// #1475: the default branch name must be usable before any ref is built
	// from it. An unreadable or invalid name runs no fetch and authorizes
	// nothing -- never a guess from local HEAD, origin/HEAD or a local branch.
	if reason := invalidDefaultBranchReason(dir, defaultBranch); reason != "" {
		// Pre-fetch: no remote-confirmed object this pass.
		return mainSyncResult{Status: MainSyncDefaultBranchUnreadable, Detail: reason, FreshRef: "HEAD", AutonomyRef: ""}
	}
	if branch != defaultBranch {
		// #851: previously the ungated MainSyncSkipped -- see MainSyncDetached
		// above for the rationale.
		// Pre-fetch: no remote-confirmed object this pass.
		status, detail := notOnDefaultBranch(branch, defaultBranch)
		return mainSyncResult{Status: status, Detail: detail, FreshRef: "HEAD", AutonomyRef: ""}
	}
	trackRef := remoteTrackingRef(defaultBranch)

	// -c core.hooksPath=/dev/null (global option, must precede the
	// subcommand): an automated dispatch-loop sync must never execute
	// repo-supplied git hooks (e.g. a committed/husky-style post-merge hook)
	// unattended (#822 review round 2 fix #2).
	if _, err := execGit(dir, "-c", "core.hooksPath=/dev/null", "fetch", "origin"); err != nil {
		// Fetch itself failed: no remote-confirmed object this pass.
		return mainSyncResult{Status: MainSyncFetchFailed, Detail: fmt.Sprintf("git fetch origin failed: %v", err), FreshRef: "HEAD", AutonomyRef: ""}
	}

	// A successful fetch does not prove refs/remotes/origin/<default> exists: the
	// remote may lack its own default branch (a rename not yet reflected in
	// the metadata, a deleted branch). With no tracking ref there is nothing to
	// fast-forward to or to confirm, and the freshness and policy reads have
	// no remote object to trust, so the pass reports fetch_failed (#1475).
	trackOID, trackReason := readTrackingRefOID(dir, trackRef)
	if trackOID == "" {
		return mainSyncResult{Status: MainSyncFetchFailed, Detail: trackReason, FreshRef: "HEAD", AutonomyRef: ""}
	}

	// Every return path below this point is reached only after `git fetch
	// origin` above succeeded, so each one carries the same confirmation
	// result (#877, #903): the confirmed remote default-branch OID in
	// AutonomyRef (or "" plus an AutonomyDetail reason when the remote tip could
	// not be confirmed). A successful fetch alone is not enough -- it does not
	// prove refs/remotes/origin/<default> was updated -- so the OID is checked
	// against the remote's current tip once, here, before any merge-base probe,
	// and the same pair is threaded through every return below via `result`:
	// even a return path whose Status reports a failure of some later step (a
	// merge-base probe error, a pre-merge re-check error, or the fast-forward
	// merge itself failing) still had a successful fetch this pass, and
	// Status, Detail and FreshRef never depend on the confirmation. The merge
	// never moves refs/remotes/origin/<default>, so confirming before it is
	// equivalent to confirming after.
	autonomyRef, autonomyDetail := confirmRemoteOID(dir, defaultBranch, trackOID)
	result := func(status MainSync, detail, freshRef string) mainSyncResult {
		return mainSyncResult{Status: status, Detail: detail, FreshRef: freshRef, AutonomyRef: autonomyRef, AutonomyDetail: autonomyDetail}
	}

	// localBehindOrEqual: local HEAD is an ancestor of (or equal to)
	// the tracking ref. localAheadOrEqual: the tracking ref is an ancestor of
	// (or equal to) local HEAD. A genuine merge-base command error (not merely
	// "not an ancestor") must never be reported as MainSyncDiverged -- that
	// would mask a real git failure as a false divergence (#822 review
	// fix #1).
	localBehindOrEqual, err := isAncestor(dir, "HEAD", trackRef)
	if err != nil {
		return result(MainSyncFailed, fmt.Sprintf("git merge-base --is-ancestor check failed: %v", err), "HEAD")
	}
	localAheadOrEqual, err := isAncestor(dir, trackRef, "HEAD")
	if err != nil {
		return result(MainSyncFailed, fmt.Sprintf("git merge-base --is-ancestor check failed: %v", err), "HEAD")
	}

	// The detail strings below keep the short "origin/<default>" spelling: for a
	// `main` default they are byte-identical to the pre-#1475 ones.
	switch {
	case localBehindOrEqual && localAheadOrEqual:
		return result(MainSyncSynced, "already up to date", "HEAD")
	case localAheadOrEqual:
		return result(MainSyncSynced, fmt.Sprintf("local %s is ahead of origin/%s", defaultBranch, defaultBranch), "HEAD")
	case !localBehindOrEqual:
		return result(MainSyncDiverged, fmt.Sprintf("local %s and origin/%s have diverged", defaultBranch, defaultBranch), "HEAD")
	}

	// Strictly behind: there is something to fast-forward. #851: this is the
	// sole case where FreshRef is the fetched ref rather than "HEAD" -- the
	// dry-run branch never merges, so staleness/config reads must consult the
	// fetched blob (the fully qualified refs/remotes/origin/<default>), not
	// stale local HEAD, to match what the subsequent real pass would render.
	if dryRun {
		return result(MainSyncSynced, fmt.Sprintf("would fast-forward origin/%s (dry-run, not merged)", defaultBranch), trackRef)
	}

	// Re-verify HEAD is still on the default branch immediately before merging
	// (#822 review fix #4, TOCTOU): `git fetch` above is a network call that can
	// take seconds, and something else could have checked out a different
	// branch during that window -- `merge --ff-only` trusts the *current*
	// HEAD, so relying solely on the early check above could silently
	// fast-forward the wrong branch. Treat a changed branch exactly like the
	// existing "not on the default branch" case.
	//
	// A probe error here must NOT collapse into MainSyncNotMain (#851,
	// previously MainSyncSkipped): mainSyncSkip (decide.go) already gates
	// both, but folding a genuine error (repo removed mid-pass, unreadable
	// .git, a WaitDelay kill) into the legitimate-branch-change reason would
	// blur two different failure classes together -- the same failure-class
	// conflation the initial symbolic-ref on-branch check above already avoids
	// (#822 review round 2 fix #1).
	branchNow, err := execGit(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return result(MainSyncFailed, fmt.Sprintf("re-verifying HEAD before merge failed: %v", err), "HEAD")
	}
	if branchNow != defaultBranch {
		// Fetch already succeeded before this mid-pass branch change was
		// discovered (#877): the remote-tip confirmation is about the fetch,
		// not the merge, so it carries through even though the merge never
		// runs.
		status, _ := notOnDefaultBranch(branchNow, defaultBranch)
		return result(status, "checked-out branch changed before merge", "HEAD")
	}

	// Same hook-suppression rationale as the fetch above: a pure fast-forward
	// still runs post-merge, so this must stay unattended-safe too.
	if _, err := execGit(dir, "-c", "core.hooksPath=/dev/null", "merge", "--ff-only", trackRef); err != nil {
		// Classify only after the failed merge (#1475): a repo with local edits
		// the fast-forward does not touch merges fine, so refusing to sync
		// whenever the tree is dirty would regress it. A failure is `dirty`
		// only when the working tree overlaps what the merge would rewrite;
		// every other failure (a lock file, for one) stays `failed`.
		if detail, dirty := dirtyFastForwardDetail(dir, defaultBranch, trackRef); dirty {
			return result(MainSyncDirty, detail, "HEAD")
		}
		return result(MainSyncFailed, fmt.Sprintf("git merge --ff-only origin/%s failed: %v", defaultBranch, err), "HEAD")
	}
	return result(MainSyncSynced, "fast-forwarded to "+trackRef, "HEAD")
}

// notOnDefaultBranch classifies a checkout that is not on the repo's default
// branch (#1475): a repo whose default is `main` keeps the pre-#1475
// MainSyncNotMain and its exact detail, any other default is MainSyncNotDefault
// with a detail naming both the checked-out and the default branch.
func notOnDefaultBranch(checkedOut, defaultBranch string) (MainSync, string) {
	if defaultBranch == "main" {
		return MainSyncNotMain, fmt.Sprintf("checked-out branch %q is not main", checkedOut)
	}
	return MainSyncNotDefault, fmt.Sprintf("checked-out branch %q is not the default branch %q", checkedOut, defaultBranch)
}

// syncMains runs syncMain once per repo in repos, unconditionally logging
// each repo's outcome (independent of ticket count) and returning a map
// keyed by repo. One repo's outcome never affects another's -- mirrors
// CollectTickets' best-effort per-repo loop. Each repo's default branch is read
// once through defaultBranchFn (#1475), only for a repo that has a Dir.
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
		// The default branch is resolved once per repo per pass, through the
		// repository metadata (#1475), and only for a repo that has a Dir: a
		// repo with no Dir is never synced, so it needs no metadata call. A
		// failed read passes "" to syncMain, which reports it as unreadable
		// only after its own dir/repo/detached checks (so a missing dir or a
		// detached HEAD keeps its own status); the cause is put in the detail.
		var defaultBranch string
		var branchErr error
		if rc.Dir != "" {
			defaultBranch, branchErr = defaultBranchFn(rc.Repo)
		}
		r := syncMain(rc.Dir, defaultBranch, dryRun)
		if branchErr != nil && r.Status == MainSyncDefaultBranchUnreadable {
			r.Detail = fmt.Sprintf("default branch metadata read failed: %v", branchErr)
		}
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
