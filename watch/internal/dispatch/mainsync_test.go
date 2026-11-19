package dispatch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// -- #822: local main sync (mainsync.go) test infrastructure ----------------

// mainSyncGitEnv pins GIT_CONFIG_GLOBAL/SYSTEM and a committer/author identity
// into the *test process* environment via t.Setenv, so execGit's git
// invocations -- which (per the plan) inherit os.Environ() plus
// GIT_TERMINAL_PROMPT=0, rather than crafting a bespoke per-call env like
// gitTest/gitTestNoDir -- never pick up ambient git config (init.defaultBranch,
// pull.rebase, merge.ff, ...) or an unset identity (watch/docs/test-isolation.md).
func mainSyncGitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@test")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@test")
}

// gitTestNoDir runs a git command with no -C target (e.g. `git clone src
// dst`, where dst does not exist yet), inheriting the test process
// environment (mainSyncGitEnv must have been called first).
func gitTestNoDir(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initOriginAndLocal creates a real "origin" repo (git init -b main, one
// commit) and clones it into "local", so local's `origin` remote and `main`
// branch are wired exactly as a real enrolled repo's would be. Both are
// created fresh per call so tests can independently advance either side.
func initOriginAndLocal(t *testing.T) (local, origin string) {
	t.Helper()
	origin = t.TempDir()
	gitTest(t, origin, "init", "-b", "main")
	commitFile(t, origin, "base.txt", "base")

	parent := t.TempDir()
	local = filepath.Join(parent, "local")
	gitTestNoDir(t, "clone", origin, local)
	return local, origin
}

// initBareOriginAndLocal (#903) is initOriginAndLocal's bare-origin twin: a
// bare "origin" seeded with one commit on main through a scratch clone, and a
// "local" clone of it. A bare origin is what a real remote looks like, and its
// `git rev-parse refs/heads/main` is the exact OID the autonomy probe must be
// pinned to. Only the #903 OID-confirmation tests need it; every other test
// keeps the non-bare helper.
func initBareOriginAndLocal(t *testing.T) (local, bare string) {
	t.Helper()
	bare = t.TempDir()
	gitTest(t, bare, "init", "--bare", "-b", "main")

	seed := t.TempDir()
	gitTest(t, seed, "init", "-b", "main")
	commitFile(t, seed, "base.txt", "base")
	gitTest(t, seed, "remote", "add", "origin", bare)
	gitTest(t, seed, "push", "origin", "main")

	local = filepath.Join(t.TempDir(), "local")
	gitTestNoDir(t, "clone", bare, local)
	return local, bare
}

// pushCommit advances a bare origin's main by one commit, made through a
// scratch clone (a bare repo has no working tree to commit in).
func pushCommit(t *testing.T, bare, name, content string) {
	t.Helper()
	scratch := filepath.Join(t.TempDir(), "scratch")
	gitTestNoDir(t, "clone", bare, scratch)
	commitFile(t, scratch, name, content)
	gitTest(t, scratch, "push", "origin", "main")
}

// bareMainOID is the object ID the bare origin's main currently points at.
func bareMainOID(t *testing.T, bare string) string {
	t.Helper()
	return gitTest(t, bare, "rev-parse", "refs/heads/main")
}

// installGitShim writes a `git` shim to a temp dir and puts it first on PATH.
// body is a POSIX-sh fragment run before delegating; $REAL_GIT is the real git,
// and a body that does not exit falls through to `exec "$REAL_GIT" "$@"`.
func installGitShim(t *testing.T, body string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nREAL_GIT=%q\n%s\nexec \"$REAL_GIT\" \"$@\"\n", realGit, body)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// -- 1-12: syncMain / syncMains classifier suite (real temp repos) ----------

// TestSyncMain_BehindOrigin_FastForwardsAndReportsSynced covers plan test 1:
// a local repo strictly behind origin/main is fast-forwarded for real, and
// the outcome is MainSyncSynced.
func TestSyncMain_BehindOrigin_FastForwardsAndReportsSynced(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	originHEAD := gitTest(t, origin, "rev-parse", "HEAD")

	result := syncMain(local, false)
	if result.Status != MainSyncSynced {
		t.Fatalf("status = %q, want MainSyncSynced", result.Status)
	}
	// #851: a real (non-dry-run) strictly-behind pass merges into local HEAD,
	// so subsequent reads (staleness, config) must consult HEAD itself, not
	// the fetched origin/main blob -- that's reserved for the dry-run branch.
	if result.FreshRef != "HEAD" {
		t.Errorf("FreshRef = %q, want %q (a real strictly-behind pass reads against the post-fast-forward local HEAD)", result.FreshRef, "HEAD")
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != originHEAD {
		t.Errorf("local HEAD = %s, want it fast-forwarded to origin HEAD %s", got, originHEAD)
	}
}

// TestSyncMain_EqualToOrigin_ReportsSynced covers plan test 2: a local repo
// already at origin/main (nothing to fast-forward) still reports Synced.
func TestSyncMain_EqualToOrigin_ReportsSynced(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)

	result := syncMain(local, false)
	if result.Status != MainSyncSynced {
		t.Fatalf("status = %q, want MainSyncSynced", result.Status)
	}
}

// TestSyncMain_LocalAheadOfOrigin_ReportsSyncedHeadUnchanged covers plan test
// 3: "Already up to date" (local ahead) is explicitly NOT divergence, and the
// merge must be a true no-op -- local HEAD must not move.
func TestSyncMain_LocalAheadOfOrigin_ReportsSyncedHeadUnchanged(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	commitFile(t, local, "ahead.txt", "local ahead")
	beforeHEAD := gitTest(t, local, "rev-parse", "HEAD")

	result := syncMain(local, false)
	if result.Status != MainSyncSynced {
		t.Fatalf("status = %q, want MainSyncSynced", result.Status)
	}
	// #851: local-ahead is not the dry-run-strictly-behind branch, so it
	// always reads against local HEAD.
	if result.FreshRef != "HEAD" {
		t.Errorf("FreshRef = %q, want %q (local-ahead reads against local HEAD)", result.FreshRef, "HEAD")
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Errorf("local HEAD = %s, want unchanged %s (local-ahead must be a no-op)", got, beforeHEAD)
	}
}

// TestSyncMain_BothSidesMoved_ReportsDivergedHeadUnchanged covers plan test
// 4: both origin and local advanced independently -- neither is an ancestor
// of the other -- MainSyncDiverged, and the merge must never touch local HEAD.
func TestSyncMain_BothSidesMoved_ReportsDivergedHeadUnchanged(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, local, "local-only.txt", "local change")
	commitFile(t, origin, "origin-only.txt", "origin change")
	beforeHEAD := gitTest(t, local, "rev-parse", "HEAD")

	result := syncMain(local, false)
	if result.Status != MainSyncDiverged {
		t.Fatalf("status = %q, want MainSyncDiverged", result.Status)
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Errorf("local HEAD = %s, want unchanged %s (a diverged repo must never be mutated)", got, beforeHEAD)
	}
}

// TestSyncMain_OriginUnresolvable_ReportsFetchFailedDistinctFromDivergedAndFailed
// covers plan test 5: a `git fetch` failure (unresolvable remote) must be
// classified into its own outcome, distinguishable from both MainSyncDiverged
// and MainSyncFailed (the ticket's "must be distinguishable and separately
// tested" requirement).
func TestSyncMain_OriginUnresolvable_ReportsFetchFailedDistinctFromDivergedAndFailed(t *testing.T) {
	mainSyncGitEnv(t)
	local := t.TempDir()
	gitTest(t, local, "init", "-b", "main")
	commitFile(t, local, "base.txt", "base")
	gitTest(t, local, "remote", "add", "origin", filepath.Join(t.TempDir(), "does-not-exist"))

	result := syncMain(local, false)
	if result.Status != MainSyncFetchFailed {
		t.Fatalf("status = %q, want MainSyncFetchFailed", result.Status)
	}
	if result.Status == MainSyncDiverged || result.Status == MainSyncFailed {
		t.Fatalf("MainSyncFetchFailed must not collapse into Diverged or Failed, got %q", result.Status)
	}
}

// TestSyncMain_DirtyTrackedFileBlocksFastForward_ReportsFailedNotDivergedOrSynced
// covers plan test 6 (Q1): a local repo strictly behind origin (not
// diverged), but with an uncommitted change to a file the fast-forward would
// overwrite, must report MainSyncFailed -- not Diverged (ancestry says it
// isn't) and not Synced (the merge itself failed).
func TestSyncMain_DirtyTrackedFileBlocksFastForward_ReportsFailedNotDivergedOrSynced(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "base.txt", "changed-upstream")
	if err := os.WriteFile(filepath.Join(local, "base.txt"), []byte("dirty-local-uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := syncMain(local, false)
	if result.Status != MainSyncFailed {
		t.Fatalf("status = %q, want MainSyncFailed", result.Status)
	}
	if result.Status == MainSyncDiverged || result.Status == MainSyncSynced {
		t.Fatalf("a dirty-tree-blocked fast-forward must not collapse into Diverged or Synced, got %q", result.Status)
	}
}

// TestSyncMain_NonRepoDir_ReportsFailed covers plan test 7 (Q1): a plain
// non-repo temp dir (one that exists on disk, so the #851 os.Stat precheck
// passes) must report MainSyncFailed -- distinct from a configured dir that
// does not exist on disk at all (MainSyncMissing, tested separately below).
func TestSyncMain_NonRepoDir_ReportsFailed(t *testing.T) {
	mainSyncGitEnv(t)
	dir := t.TempDir()

	result := syncMain(dir, false)
	if result.Status != MainSyncFailed {
		t.Fatalf("status = %q, want MainSyncFailed", result.Status)
	}
}

// TestSyncMain_ConfiguredDirAbsentFromDisk_ReportsMissing covers #851's new
// os.Stat precheck: a configured (non-empty) Dir that does not exist on disk
// at all must report the new gated MainSyncMissing -- distinct from
// MainSyncFailed (a real, existing dir that merely isn't a git repo, tested
// above) and distinct from the ungated MainSyncSkipped zero value reserved
// for dir == "" (no Dir configured at all, tested below).
func TestSyncMain_ConfiguredDirAbsentFromDisk_ReportsMissing(t *testing.T) {
	mainSyncGitEnv(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "does-not-exist-on-disk")

	result := syncMain(dir, false)
	if result.Status != MainSyncMissing {
		t.Fatalf("status = %q, want MainSyncMissing", result.Status)
	}
	if result.Status == MainSyncFailed || result.Status == MainSyncSkipped {
		t.Fatalf("a configured-but-absent dir must not collapse into MainSyncFailed or the ungated MainSyncSkipped zero value, got %q", result.Status)
	}
}

// TestSyncMain_FeatureBranchCheckedOut_ReportsNotMainWithoutTouchingMainOrFeatureHEAD
// covers plan test 8 (Q2) as updated by #851: a repo with a feature branch
// checked out while origin/main advances must report the new gated
// MainSyncNotMain (previously the ungated MainSyncSkipped) -- never
// fast-forward the wrong branch -- and neither the local `main` ref nor the
// checked-out feature branch's HEAD may move.
func TestSyncMain_FeatureBranchCheckedOut_ReportsNotMainWithoutTouchingMainOrFeatureHEAD(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	gitTest(t, local, "checkout", "-b", "feature")
	featureHEADBefore := gitTest(t, local, "rev-parse", "HEAD")
	mainRefBefore := gitTest(t, local, "rev-parse", "main")
	commitFile(t, origin, "advance.txt", "advance")

	result := syncMain(local, false)
	if result.Status != MainSyncNotMain {
		t.Fatalf("status = %q, want MainSyncNotMain", result.Status)
	}
	if got := gitTest(t, local, "rev-parse", "main"); got != mainRefBefore {
		t.Errorf("local main ref = %s, want unchanged %s (no wrong-branch fast-forward)", got, mainRefBefore)
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != featureHEADBefore {
		t.Errorf("feature branch HEAD = %s, want unchanged %s", got, featureHEADBefore)
	}
}

// TestSyncMain_DetachedHEAD_ReportsDetachedNotFailed covers plan test 9 as
// updated by #851: a detached HEAD in a real repo now reports the new gated
// MainSyncDetached (previously the ungated MainSyncSkipped) -- the dir IS a
// repo, it just isn't on main -- distinct from the non-repo-dir
// MainSyncFailed case (test 7) and from the feature-branch MainSyncNotMain
// case above.
func TestSyncMain_DetachedHEAD_ReportsDetachedNotFailed(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	sha := gitTest(t, local, "rev-parse", "HEAD")
	gitTest(t, local, "checkout", "--detach", sha)

	result := syncMain(local, false)
	if result.Status != MainSyncDetached {
		t.Fatalf("status = %q, want MainSyncDetached (detached HEAD, not Failed or NotMain)", result.Status)
	}
	if result.Status == MainSyncNotMain {
		t.Fatalf("detached HEAD must not collapse into MainSyncNotMain -- distinct checkout-state classes, got %q", result.Status)
	}
}

// TestSyncMain_EmptyDir_NeverProbesCwd covers plan test 10 (mirrors
// TestProbeStage_EmptyDir_NeverProbes_NoCwdRelativeRead, collect_test.go):
// dir=="" must skip entirely rather than resolve a repo root from the
// daemon's own cwd, even when the cwd IS a real repo behind its origin.
//
// #851: dir=="" is the deliberately preserved exception that stays the
// ungated MainSyncSkipped zero value ("no sync attempted at all" -- no Dir
// was configured for this repo), distinct from the new gated
// MainSyncMissing (a configured, non-empty Dir that is absent from disk,
// tested by TestSyncMain_ConfiguredDirAbsentFromDisk_ReportsMissing above).
func TestSyncMain_EmptyDir_NeverProbesCwd(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	beforeHEAD := gitTest(t, local, "rev-parse", "HEAD")

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(local); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	result := syncMain("", false)
	if result.Status != MainSyncSkipped {
		t.Fatalf("status = %q, want MainSyncSkipped -- dir=\"\" must never probe/sync", result.Status)
	}
	if result.Status == MainSyncMissing {
		t.Fatalf("dir==\"\" must not collapse into MainSyncMissing -- that state is reserved for a configured-but-absent dir, got %q", result.Status)
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Errorf("cwd repo HEAD = %s, want unchanged %s -- dir=\"\" must never mutate the cwd repo", got, beforeHEAD)
	}
}

// TestSyncMain_DryRun_ReportsSyncedWithoutMutatingHead covers plan test 11:
// dryRun=true still fetches and classifies (an accurate gate needs the fetch)
// but must never run the merge -- HEAD stays put even though the outcome
// reported is the would-be-synced status. #851: the dry-run strictly-behind
// branch is the one case where FreshRef must be "origin/main" rather than
// "HEAD", so staleness/config reads consult the fetched blob, not stale local
// HEAD, giving dry-run parity with the subsequent real pass.
func TestSyncMain_DryRun_ReportsSyncedWithoutMutatingHead(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	beforeHEAD := gitTest(t, local, "rev-parse", "HEAD")

	result := syncMain(local, true)
	if result.Status != MainSyncSynced {
		t.Fatalf("status = %q, want MainSyncSynced (classified, not merged)", result.Status)
	}
	if result.FreshRef != "origin/main" {
		t.Errorf("FreshRef = %q, want %q (dry-run strictly-behind must read the fetched blob, not local HEAD)", result.FreshRef, "origin/main")
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Errorf("local HEAD = %s, want unchanged %s -- dry-run must never merge", got, beforeHEAD)
	}
}

// TestSyncMains_TwoRepos_MapCarriesBothAndLogsDistinctOutcomesSafely covers
// plan test 12: syncMains loops every repo, returns a map keyed by repo, logs
// each repo's outcome unconditionally, and its repo-level log lines must never
// contain the lazyboards-reserved " skip:"/" dispatch " substrings
// (formatDecision's doc comment, dispatch.go).
func TestSyncMains_TwoRepos_MapCarriesBothAndLogsDistinctOutcomesSafely(t *testing.T) {
	mainSyncGitEnv(t)
	local1, origin1 := initOriginAndLocal(t)
	commitFile(t, origin1, "advance.txt", "advance") // behind -> Synced

	local2, origin2 := initOriginAndLocal(t)
	commitFile(t, local2, "local-only.txt", "local change")
	commitFile(t, origin2, "origin-only.txt", "origin change") // Diverged

	repos := []RepoConfig{
		{Repo: "o/r1", Dir: local1},
		{Repo: "o/r2", Dir: local2},
	}
	var buf bytes.Buffer
	got := syncMains(repos, &buf, false)

	if got["o/r1"].Status != MainSyncSynced {
		t.Errorf(`syncMains()["o/r1"].Status = %q, want MainSyncSynced`, got["o/r1"].Status)
	}
	if got["o/r2"].Status != MainSyncDiverged {
		t.Errorf(`syncMains()["o/r2"].Status = %q, want MainSyncDiverged`, got["o/r2"].Status)
	}

	foundR1, foundR2 := false, false
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, " skip:") || strings.Contains(line, " dispatch ") {
			t.Errorf("repo-level main-sync log line must not contain the lazyboards-reserved substrings, got %q", line)
		}
		if strings.Contains(line, "o/r1") {
			foundR1 = true
			if !strings.Contains(line, string(MainSyncSynced)) {
				t.Errorf("expected o/r1's log line to name its synced outcome, got %q", line)
			}
		}
		if strings.Contains(line, "o/r2") {
			foundR2 = true
			if !strings.Contains(line, string(MainSyncDiverged)) {
				t.Errorf("expected o/r2's log line to name its diverged outcome, got %q", line)
			}
		}
	}
	if !foundR1 || !foundR2 {
		t.Fatalf("expected syncMains to unconditionally log a line naming each repo, got:\n%s", buf.String())
	}
}

// -- #822 review fix regression tests ----------------------------------------

// TestSyncMain_MergeBaseCommandError_ReportsFailedNotDivergedOrSynced covers
// review fix #1: a genuine `git merge-base --is-ancestor` command error (not
// merely a "not an ancestor" exit-1 result) must never be reported as
// MainSyncDiverged -- that would mask a real git failure as a false
// divergence.
//
// Triggered deterministically in a real repo via a `git` PATH shim that lets
// a real `git fetch origin` complete first (fetch itself performs its own
// connectivity check over existing objects, so corrupting HEAD's object
// beforehand would make fetch -- not merge-base -- the one to fail) and
// only *then* corrupts the on-disk object for local HEAD's commit, so the
// merge-base probes that run afterward hit an unreadable object and error
// out with something other than exit 1.
func TestSyncMain_MergeBaseCommandError_ReportsFailedNotDivergedOrSynced(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	headSHA := gitTest(t, local, "rev-parse", "HEAD")
	if len(headSHA) < 3 {
		t.Fatalf("unexpected short HEAD sha %q", headSHA)
	}
	objPath := filepath.Join(local, ".git", "objects", headSHA[:2], headSHA[2:])

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	// Matches "fetch" anywhere in the argv (not just $3): execGit now injects
	// `-c core.hooksPath=/dev/null` before the subcommand (#822 review round
	// 2 fix #2), which shifts fetch's position.
	script := fmt.Sprintf(`#!/bin/sh
is_fetch=0
for arg in "$@"; do
  if [ "$arg" = "fetch" ]; then is_fetch=1; fi
done
if [ "$1" = "-C" ] && [ "$is_fetch" = "1" ]; then
  "%s" "$@"
  status=$?
  chmod 0644 "%s" 2>/dev/null
  printf 'not a valid git object' > "%s"
  exit $status
fi
exec "%s" "$@"
`, realGit, objPath, objPath, realGit)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := syncMain(local, false)
	if result.Status != MainSyncFailed {
		t.Fatalf("status = %q, detail = %q, want MainSyncFailed", result.Status, result.Detail)
	}
	if result.Status == MainSyncDiverged || result.Status == MainSyncSynced {
		t.Fatalf("a genuine merge-base command error must not collapse into Diverged or Synced, got %q", result.Status)
	}
}

// TestExecGit_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay covers
// review fix #2: a `git` invocation whose process exits normally but leaves
// a background grandchild holding the stdout/stderr pipes open must not
// stall execGit past its bounded WaitDelay -- otherwise a lingering ssh /
// credential-helper / git-remote-https grandchild could stall a dispatch
// pass indefinitely even though git itself finished.
func TestExecGit_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay(t *testing.T) {
	shimDir := t.TempDir()
	script := "#!/bin/sh\n(sleep 30 &) \nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	// The grandchild keeps the output pipes open well past WaitDelay, so
	// Wait is documented (os/exec) to force them closed and return an error
	// wrapping ErrWaitDelay -- that error IS the fix working as designed
	// (bounded, not a hang), so it's expected here, not a failure.
	_, err := execGit(t.TempDir(), "status")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("execGit returned nil error; expected the documented WaitDelay-forced-close error")
	}
	if elapsed > 20*time.Second {
		t.Fatalf("execGit took %v, want it to return well within gitTimeout thanks to WaitDelay (~%v)", elapsed, gitWaitDelay)
	}
	if elapsed < gitWaitDelay {
		t.Fatalf("execGit returned in %v, faster than gitWaitDelay (%v) -- test may not be exercising the intended lingering-grandchild path", elapsed, gitWaitDelay)
	}
}

// TestGitEnv_HardensSSHAndAskpassWithoutClobberingCallerOverride covers
// review fix #3: gitEnv must set GIT_SSH_COMMAND/GIT_ASKPASS/
// SSH_ASKPASS_REQUIRE so a missing credential can never block on an ssh
// prompt or a GUI askpass dialog -- but must never clobber a value the
// caller's environment already set.
func TestGitEnv_HardensSSHAndAskpassWithoutClobberingCallerOverride(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "ssh -o UserKnownHostsFile=/custom/known_hosts")
	env := gitEnv()
	if !envContainsExact(env, "GIT_SSH_COMMAND=ssh -o UserKnownHostsFile=/custom/known_hosts") {
		t.Errorf("gitEnv() must preserve the caller's GIT_SSH_COMMAND override, got %v", env)
	}

	// Cleared directly (rather than via t.Setenv, which cannot unset): safe
	// because t.Setenv above already captured this test's pre-existing
	// ambient value and will restore it at cleanup regardless of any
	// unset/set done in between.
	for _, key := range []string{"GIT_SSH_COMMAND", "GIT_ASKPASS", "SSH_ASKPASS_REQUIRE"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unsetting %s: %v", key, err)
		}
	}
	env = gitEnv()
	if !envHasKey(env, "GIT_TERMINAL_PROMPT") {
		t.Errorf("gitEnv() missing GIT_TERMINAL_PROMPT")
	}
	if !envHasKey(env, "GIT_SSH_COMMAND") {
		t.Errorf("gitEnv() must set a default GIT_SSH_COMMAND when unset, got %v", env)
	}
	if !envHasKey(env, "GIT_ASKPASS") {
		t.Errorf("gitEnv() must set a default GIT_ASKPASS when unset, got %v", env)
	}
	if !envHasKey(env, "SSH_ASKPASS_REQUIRE") {
		t.Errorf("gitEnv() must set a default SSH_ASKPASS_REQUIRE when unset, got %v", env)
	}
}

// envContainsExact reports whether env contains kv verbatim.
func envContainsExact(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// TestSyncMain_BranchChangedBetweenFetchAndMerge_ReportsNotMainWithoutFastForwardingWrongBranch
// covers review fix #4 (TOCTOU), updated by #851: if the checked-out branch
// changes during the fetch window, the merge must never run against the
// wrong branch -- syncMain must re-verify HEAD immediately before merging,
// and the outcome is now the gated MainSyncNotMain (previously the ungated
// MainSyncSkipped) -- a legitimate off-main checkout gates every pickup for
// the repo, not only planning. Simulated deterministically (no real race)
// via a `git` PATH shim that, only for the `fetch` subcommand, delegates to
// the real git fetch and then checks the repo out onto a different branch as
// a side effect -- exercising the same re-check code path a genuine
// mid-fetch checkout would hit.
func TestSyncMain_BranchChangedBetweenFetchAndMerge_ReportsNotMainWithoutFastForwardingWrongBranch(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	mainRefBefore := gitTest(t, local, "rev-parse", "main")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	// Matches "fetch" anywhere in the argv (not just $3): execGit now injects
	// `-c core.hooksPath=/dev/null` before the subcommand (#822 review round
	// 2 fix #2), which shifts fetch's position.
	script := fmt.Sprintf(`#!/bin/sh
is_fetch=0
for arg in "$@"; do
  if [ "$arg" = "fetch" ]; then is_fetch=1; fi
done
if [ "$1" = "-C" ] && [ "$is_fetch" = "1" ]; then
  "%s" "$@"
  status=$?
  "%s" -C "$2" checkout -b sneaky >/dev/null 2>&1
  exit $status
fi
exec "%s" "$@"
`, realGit, realGit, realGit)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := syncMain(local, false)
	if result.Status != MainSyncNotMain {
		t.Fatalf("status = %q, detail = %q, want MainSyncNotMain", result.Status, result.Detail)
	}
	if got := gitTest(t, local, "rev-parse", "main"); got != mainRefBefore {
		t.Errorf("local main ref = %s, want unchanged %s (must never fast-forward after a mid-fetch branch change)", got, mainRefBefore)
	}
}

// TestSyncMain_PreMergeHEADRecheckErrors_ReportsFailedNotSkipped covers #822
// review round 2 fix #1, updated by #851: a genuine probe error on the
// pre-merge HEAD re-check (not merely a legitimate branch change, already
// covered by
// TestSyncMain_BranchChangedBetweenFetchAndMerge_ReportsNotMainWithoutFastForwardingWrongBranch)
// must report MainSyncFailed, distinct from MainSyncNotMain (the now-gated
// legitimate-branch-change outcome) -- collapsing a genuine probe error into
// MainSyncNotMain would blur two different failure classes together even
// though both are gated today.
//
// Triggered deterministically via a `git` PATH shim that lets the first
// `symbolic-ref --short HEAD` call (the early on-main check) succeed for
// real, but fails every subsequent one -- which is exactly the pre-merge
// re-check syncMain performs immediately before merging.
func TestSyncMain_PreMergeHEADRecheckErrors_ReportsFailedNotSkipped(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	mainRefBefore := gitTest(t, local, "rev-parse", "main")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	countFile := filepath.Join(t.TempDir(), "symbolic-ref-count")
	shimDir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "-C" ] && [ "$3" = "symbolic-ref" ]; then
  count=$(cat "%[2]s" 2>/dev/null || echo 0)
  count=$((count + 1))
  echo "$count" > "%[2]s"
  if [ "$count" -ge 2 ]; then
    echo "simulated symbolic-ref failure" >&2
    exit 128
  fi
fi
exec "%[1]s" "$@"
`, realGit, countFile)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := syncMain(local, false)
	if result.Status != MainSyncFailed {
		t.Fatalf("status = %q, detail = %q, want MainSyncFailed", result.Status, result.Detail)
	}
	if result.Status == MainSyncNotMain {
		t.Fatalf("a genuine pre-merge HEAD re-check error must not collapse into MainSyncNotMain (the legitimate-branch-change case), got %q", result.Status)
	}
	if got := gitTest(t, local, "rev-parse", "main"); got != mainRefBefore {
		t.Errorf("local main ref = %s, want unchanged %s (a failed re-check must never fast-forward)", got, mainRefBefore)
	}
}

// TestMainSyncNewCheckoutStates_StringsAreNonBlankAndDistinct covers #851's
// Implementation Order step 1 ("non-blank String() for each"), mirroring
// MainSyncSkipped's existing non-blank convention (review nitpick fix #6): a
// regression that collapses any two of the three new gated checkout states'
// rendered forms into the same word would otherwise be silent.
func TestMainSyncNewCheckoutStates_StringsAreNonBlankAndDistinct(t *testing.T) {
	values := []MainSync{MainSyncNotMain, MainSyncDetached, MainSyncMissing}
	seen := map[string]MainSync{}
	for _, v := range values {
		s := v.String()
		if s == "" {
			t.Errorf("%v.String() = %q, want a non-empty display string", v, s)
		}
		if prior, ok := seen[s]; ok {
			t.Errorf("both %v and %v render as %q; String() must be distinct per state", prior, v, s)
		}
		seen[s] = v
	}
}

// TestMainSyncSkipped_StringIsNonBlank covers review nitpick fix #6:
// MainSyncSkipped's rendered form must be a legible word, not a blank token,
// so syncMains' "dispatch: main sync %s: %s (%s)" log line never prints an
// empty status field.
func TestMainSyncSkipped_StringIsNonBlank(t *testing.T) {
	if got := MainSyncSkipped.String(); got == "" {
		t.Fatalf("MainSyncSkipped.String() = %q, want a non-empty display string", got)
	}
	if MainSyncSkipped != "" {
		t.Fatalf("MainSyncSkipped's underlying zero value must stay \"\" (unchanged enum semantics), got %q", string(MainSyncSkipped))
	}
}

// TestSyncMains_SkippedRepo_LogsNonBlankStatusWord covers review nitpick fix
// #6 end-to-end: a repo whose sync is Skipped (dir=="") must log a legible
// status word, not a blank token, in syncMains' log line.
func TestSyncMains_SkippedRepo_LogsNonBlankStatusWord(t *testing.T) {
	repos := []RepoConfig{{Repo: "o/r", Dir: ""}}
	var buf bytes.Buffer
	got := syncMains(repos, &buf, false)

	if got["o/r"].Status != MainSyncSkipped {
		t.Fatalf(`syncMains()["o/r"].Status = %q, want MainSyncSkipped`, got["o/r"].Status)
	}
	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, "o/r:  (") {
		t.Errorf("expected a non-blank status word before the detail parens, got %q", line)
	}
	if !strings.Contains(line, "o/r: skipped (") {
		t.Errorf("expected the log line to render MainSyncSkipped as %q, got %q", "skipped", line)
	}
}

// -- 21-22: wiring (RunOnce syncs once per pass, RunReconcileOnce never does) -

// emptyIssuesFakeGH is the fake gh script shared by the wiring tests: zero
// open issues and zero open PRs, so RunOnce/RunReconcileOnce collect nothing
// and never need a GitHub identity, isolating the assertions to the main-sync
// wiring alone.
const emptyIssuesFakeGH = `
case "$1 $2" in
  "issue list") printf '[]' ;;
  "api graphql") printf '` + emptyOpenPRPageJSON + `' ;;
  *) exit 1 ;;
esac
`

// TestRunReconcileOnce_NeverSyncsMain covers plan test 21: the reconciler
// must never run the local-main sync -- RunOnce owns it exclusively (once per
// combined pass, per combined.go).
func TestRunReconcileOnce_NeverSyncsMain(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	beforeHEAD := gitTest(t, local, "rev-parse", "HEAD")

	installFakeGHOnPath(t, emptyIssuesFakeGH)

	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local}}
	store := &memStore{}
	var buf bytes.Buffer
	if _, err := RunReconcileOnce(cfg, &fakeMutator{}, false, &buf, store); err != nil {
		t.Fatalf("RunReconcileOnce returned unexpected error: %v", err)
	}

	if got := gitTest(t, local, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Errorf("RunReconcileOnce must never sync main: HEAD changed %s -> %s", beforeHEAD, got)
	}
}

// TestRunOnce_SyncsMainOncePerPassIndependentOfTicketCount covers plan test
// 22: RunOnce must run the sync even on an empty pass (zero collected
// tickets) -- the sync is a per-repo pass concern, not a per-ticket one.
func TestRunOnce_SyncsMainOncePerPassIndependentOfTicketCount(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	beforeHEAD := gitTest(t, local, "rev-parse", "HEAD")
	originHEAD := gitTest(t, origin, "rev-parse", "HEAD")

	installFakeGHOnPath(t, emptyIssuesFakeGH)

	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local}}
	var buf bytes.Buffer
	if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}

	got := gitTest(t, local, "rev-parse", "HEAD")
	if got == beforeHEAD {
		t.Fatalf("RunOnce must sync main once per pass even with zero tickets; HEAD did not advance from %s", beforeHEAD)
	}
	if got != originHEAD {
		t.Errorf("local HEAD after RunOnce = %s, want it fast-forwarded to origin HEAD %s", got, originHEAD)
	}
}

// -- #877: mainSyncResult.AutonomyRef ----------------------------------------
//
// AutonomyRef is the exact object the repo-autonomy probe (autonomy.go) must
// read authorization from this pass (#903): the object ID of the remote's
// current main tip, set if and only if `git fetch origin` succeeded this pass
// AND `refs/remotes/origin/main` equals what `git ls-remote origin
// refs/heads/main` reports. It is never a ref name (git's rev-parse precedence
// resolves refs/heads/<name> before refs/remotes/<name>, so a name could be
// shadowed by a local branch; see autonomy_test.go's Q1 regression test), and
// it is empty whenever the remote tip cannot be confirmed -- a deleted or
// renamed remote main, a non-standard remote.origin.fetch, a concurrent push.

// TestRemoteMainAuthRef_IsFullyQualified pins the authorization ref
// constant's literal value.
func TestRemoteMainAuthRef_IsFullyQualified(t *testing.T) {
	if remoteMainAuthRef != "refs/remotes/origin/main" {
		t.Fatalf("remoteMainAuthRef = %q, want %q", remoteMainAuthRef, "refs/remotes/origin/main")
	}
}

// assertConfirmedOID fails unless result.AutonomyRef is the bare origin's
// current main object ID (what `git -C <bare> rev-parse refs/heads/main`
// reports) and no reason is carried -- and never the ref name.
func assertConfirmedOID(t *testing.T, result mainSyncResult, bare string) {
	t.Helper()
	want := bareMainOID(t, bare)
	if len(want) != 40 && len(want) != 64 {
		t.Fatalf("test setup: bare rev-parse = %q, want a 40- or 64-hex object ID", want)
	}
	if result.AutonomyRef != want {
		t.Errorf("AutonomyRef = %q, want the bare origin's main OID %q (never the ref name %q)", result.AutonomyRef, want, remoteMainAuthRef)
	}
	if result.AutonomyDetail != "" {
		t.Errorf("AutonomyDetail = %q, want empty when the OID is confirmed", result.AutonomyDetail)
	}
}

// TestSyncMain_AutonomyRef_Table asserts mainSyncResult.AutonomyRef across
// every syncMain outcome family against a local bare origin (#903): the exact
// object ID `git -C <bare> rev-parse refs/heads/main` reports on all seven
// outcomes reached after a successful fetch (already up to date, local ahead,
// diverged, dry-run strictly-behind, real fast-forward, a branch change
// discovered only AFTER the fetch, and a failing fast-forward merge), and
// empty ("") on every fetch-failed path (an unresolvable remote, or a fetch
// that never completes at all).
func TestSyncMain_AutonomyRef_Table(t *testing.T) {
	t.Run("behind origin, real fast-forward: confirmed OID", func(t *testing.T) {
		mainSyncGitEnv(t)
		local, bare := initBareOriginAndLocal(t)
		pushCommit(t, bare, "advance.txt", "advance")

		result := syncMain(local, false)
		if result.Status != MainSyncSynced {
			t.Fatalf("status = %q, want MainSyncSynced", result.Status)
		}
		assertConfirmedOID(t, result, bare)
	})

	t.Run("behind origin, dry run: confirmed OID", func(t *testing.T) {
		mainSyncGitEnv(t)
		local, bare := initBareOriginAndLocal(t)
		pushCommit(t, bare, "advance.txt", "advance")

		result := syncMain(local, true)
		if result.Status != MainSyncSynced {
			t.Fatalf("status = %q, want MainSyncSynced", result.Status)
		}
		assertConfirmedOID(t, result, bare)
	})

	t.Run("already up to date: confirmed OID", func(t *testing.T) {
		mainSyncGitEnv(t)
		local, bare := initBareOriginAndLocal(t)

		result := syncMain(local, false)
		if result.Status != MainSyncSynced {
			t.Fatalf("status = %q, want MainSyncSynced", result.Status)
		}
		assertConfirmedOID(t, result, bare)
	})

	t.Run("local ahead: confirmed OID is the remote's, not local HEAD", func(t *testing.T) {
		mainSyncGitEnv(t)
		local, bare := initBareOriginAndLocal(t)
		commitFile(t, local, "ahead.txt", "local ahead")

		result := syncMain(local, false)
		if result.Status != MainSyncSynced {
			t.Fatalf("status = %q, want MainSyncSynced", result.Status)
		}
		assertConfirmedOID(t, result, bare)
		if localHead := gitTest(t, local, "rev-parse", "HEAD"); result.AutonomyRef == localHead {
			t.Errorf("AutonomyRef = local HEAD %q, want the remote's tip (an unpushed local commit must never authorize)", localHead)
		}
	})

	t.Run("diverged: confirmed OID", func(t *testing.T) {
		mainSyncGitEnv(t)
		local, bare := initBareOriginAndLocal(t)
		commitFile(t, local, "local-only.txt", "local change")
		pushCommit(t, bare, "origin-only.txt", "origin change")

		result := syncMain(local, false)
		if result.Status != MainSyncDiverged {
			t.Fatalf("status = %q, want MainSyncDiverged", result.Status)
		}
		assertConfirmedOID(t, result, bare)
	})

	t.Run("fetch failed (unresolvable remote): empty", func(t *testing.T) {
		mainSyncGitEnv(t)
		local := t.TempDir()
		gitTest(t, local, "init", "-b", "main")
		commitFile(t, local, "base.txt", "base")
		gitTest(t, local, "remote", "add", "origin", filepath.Join(t.TempDir(), "does-not-exist"))

		result := syncMain(local, false)
		if result.Status != MainSyncFetchFailed {
			t.Fatalf("status = %q, want MainSyncFetchFailed", result.Status)
		}
		if result.AutonomyRef != "" {
			t.Errorf("AutonomyRef = %q, want \"\" (fetch failed, no remote-confirmed object this pass)", result.AutonomyRef)
		}
	})

	t.Run("branch changed mid-pass (after a successful fetch): confirmed OID", func(t *testing.T) {
		// Reuses the #822 review-fix-#4 TOCTOU shim: the fetch itself
		// succeeds for real (so AutonomyRef must still be confirmed), and only
		// the checkout changes afterward, blocking the subsequent merge --
		// MainSyncNotMain. AutonomyRef's invariant is about the fetch, not
		// the merge, so it must stay set even though the merge never runs.
		mainSyncGitEnv(t)
		local, bare := initBareOriginAndLocal(t)
		pushCommit(t, bare, "advance.txt", "advance")

		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		shimDir := t.TempDir()
		script := fmt.Sprintf(`#!/bin/sh
is_fetch=0
for arg in "$@"; do
  if [ "$arg" = "fetch" ]; then is_fetch=1; fi
done
if [ "$1" = "-C" ] && [ "$is_fetch" = "1" ]; then
  "%s" "$@"
  status=$?
  "%s" -C "$2" checkout -b sneaky-autonomy-ref >/dev/null 2>&1
  exit $status
fi
exec "%s" "$@"
`, realGit, realGit, realGit)
		if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		result := syncMain(local, false)
		if result.Status != MainSyncNotMain {
			t.Fatalf("status = %q, want MainSyncNotMain", result.Status)
		}
		// The fetch succeeded before the mid-pass branch change blocked only
		// the merge.
		assertConfirmedOID(t, result, bare)
	})

	t.Run("fast-forward merge fails: confirmed OID", func(t *testing.T) {
		// A shim fails only `merge --ff-only` and passes every other git call
		// (fetch, ls-remote, for-each-ref, merge-base, symbolic-ref) to the
		// real git, so the fetch and the remote-tip confirmation succeed and
		// only the final merge step fails -- MainSyncFailed.
		mainSyncGitEnv(t)
		local, bare := initBareOriginAndLocal(t)
		pushCommit(t, bare, "advance.txt", "advance")
		installGitShim(t, `for arg in "$@"; do
  if [ "$arg" = "merge" ]; then echo "simulated merge failure" >&2; exit 1; fi
done`)

		result := syncMain(local, false)
		if result.Status != MainSyncFailed {
			t.Fatalf("status = %q, detail = %q, want MainSyncFailed", result.Status, result.Detail)
		}
		if !strings.Contains(result.Detail, "merge --ff-only") {
			t.Fatalf("detail = %q, want the failure to come from the merge step", result.Detail)
		}
		assertConfirmedOID(t, result, bare)
	})

	t.Run("fetch timeout (WaitDelay-forced close): empty", func(t *testing.T) {
		// Mirrors TestExecGit_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay,
		// but scoped to only the fetch subcommand (via a directory-aware
		// shim) so the earlier symbolic-ref/rev-parse probes still resolve
		// for real -- exercising the same forced-close path syncMain would
		// hit on a genuinely hung `git fetch origin`.
		mainSyncGitEnv(t)
		local, _ := initOriginAndLocal(t)

		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		shimDir := t.TempDir()
		script := fmt.Sprintf(`#!/bin/sh
is_fetch=0
for arg in "$@"; do
  if [ "$arg" = "fetch" ]; then is_fetch=1; fi
done
if [ "$is_fetch" = "1" ]; then
  (sleep 30 &)
  exit 0
fi
exec "%s" "$@"
`, realGit)
		if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		start := time.Now()
		result := syncMain(local, false)
		elapsed := time.Since(start)

		if result.Status != MainSyncFetchFailed {
			t.Fatalf("status = %q, detail = %q, want MainSyncFetchFailed", result.Status, result.Detail)
		}
		if result.AutonomyRef != "" {
			t.Errorf("AutonomyRef = %q, want \"\" (fetch never actually completed)", result.AutonomyRef)
		}
		if elapsed > 20*time.Second {
			t.Fatalf("syncMain took %v, want it to return well within gitTimeout thanks to WaitDelay (~%v)", elapsed, gitWaitDelay)
		}
	})
}

// -- #903: remote-tip confirmation of AutonomyRef ----------------------------
//
// A successful `git fetch origin` does not prove refs/remotes/origin/main was
// updated: a deleted or renamed remote main leaves the local tracking ref
// stale (fetch does not prune), and a non-standard remote.origin.fetch may
// never write it. So syncMain confirms the tracking ref's OID against
// `git ls-remote --exit-code origin refs/heads/main` and pins AutonomyRef to
// it only when the two agree; every failure leaves AutonomyRef empty (the
// probe then reports RepoAutonomyFetchUnconfirmed) with the reason in
// AutonomyDetail, and never touches Status/Detail/FreshRef.

// TestSyncMain_FetchFailed_NeverRunsLsRemote: a failed fetch leaves
// AutonomyRef empty and git ls-remote is never invoked.
func TestSyncMain_FetchFailed_NeverRunsLsRemote(t *testing.T) {
	mainSyncGitEnv(t)
	local := t.TempDir()
	gitTest(t, local, "init", "-b", "main")
	commitFile(t, local, "base.txt", "base")
	gitTest(t, local, "remote", "add", "origin", filepath.Join(t.TempDir(), "does-not-exist"))

	invocations := filepath.Join(t.TempDir(), "invocations.log")
	installGitShim(t, fmt.Sprintf(`printf '%%s\n' "$*" >> %q`, invocations))

	result := syncMain(local, false)
	if result.Status != MainSyncFetchFailed {
		t.Fatalf("status = %q, want MainSyncFetchFailed", result.Status)
	}
	if result.AutonomyRef != "" {
		t.Errorf("AutonomyRef = %q, want empty after a failed fetch", result.AutonomyRef)
	}
	data, err := os.ReadFile(invocations)
	if err != nil {
		t.Fatalf("reading the shim's invocation log: %v", err)
	}
	if !strings.Contains(string(data), "fetch") {
		t.Fatalf("shim never saw the fetch, so this test proves nothing; log:\n%s", data)
	}
	if strings.Contains(string(data), "ls-remote") {
		t.Errorf("git ls-remote ran after a failed fetch; invocations:\n%s", data)
	}
}

// TestSyncMain_RemoteMainDeleted_AutonomyUnconfirmedWhileTrackingRefStaysStale
// covers a deleted remote main: `git fetch origin` still exits 0 (so the
// outcome is not MainSyncFetchFailed -- proving the ls-remote path ran) and
// the local tracking ref keeps its old value, but the remote does not
// advertise refs/heads/main, so the stale ref must not be trusted.
func TestSyncMain_RemoteMainDeleted_AutonomyUnconfirmedWhileTrackingRefStaysStale(t *testing.T) {
	mainSyncGitEnv(t)
	local, bare := initBareOriginAndLocal(t)
	staleOID := gitTest(t, local, "rev-parse", remoteMainAuthRef)
	gitTest(t, bare, "update-ref", "-d", "refs/heads/main")

	result := syncMain(local, false)
	if result.Status == MainSyncFetchFailed {
		t.Fatalf("status = MainSyncFetchFailed (%q): the fetch must succeed against an origin with no main, otherwise the ls-remote path never ran", result.Detail)
	}
	if got := gitTest(t, local, "rev-parse", remoteMainAuthRef); got != staleOID {
		t.Fatalf("test setup: tracking ref = %q, want it left stale at %q (fetch does not prune)", got, staleOID)
	}
	if result.AutonomyRef != "" {
		t.Errorf("AutonomyRef = %q, want empty: the remote no longer advertises main", result.AutonomyRef)
	}
	if !strings.Contains(result.AutonomyDetail, "not advertised") {
		t.Errorf("AutonomyDetail = %q, want it to name remote main as not advertised", result.AutonomyDetail)
	}
	if result.Status != MainSyncSynced || result.Detail != "already up to date" {
		t.Errorf("status/detail = %q/%q, want the unchanged merge-classification outcome synced/already up to date", result.Status, result.Detail)
	}
}

// TestSyncMain_NonStandardFetchRefspec_StaleTrackingRefIsUnconfirmed covers a
// remote.origin.fetch that never writes refs/remotes/origin/main: the remote
// main advances, the fetch succeeds, but the tracking ref stays at the old
// tip -- a mismatch with the remote's actual tip.
func TestSyncMain_NonStandardFetchRefspec_StaleTrackingRefIsUnconfirmed(t *testing.T) {
	mainSyncGitEnv(t)
	local, bare := initBareOriginAndLocal(t)
	gitTest(t, local, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/mirror-main")
	pushCommit(t, bare, "advance.txt", "advance")

	result := syncMain(local, false)
	if result.Status == MainSyncFetchFailed {
		t.Fatalf("status = MainSyncFetchFailed (%q), want the fetch to succeed", result.Detail)
	}
	if got := gitTest(t, local, "rev-parse", remoteMainAuthRef); got == bareMainOID(t, bare) {
		t.Fatalf("test setup: tracking ref was updated to %q, want it stale", got)
	}
	if result.AutonomyRef != "" {
		t.Errorf("AutonomyRef = %q, want empty for a stale tracking ref", result.AutonomyRef)
	}
	if !strings.Contains(result.AutonomyDetail, "does not match remote main tip") {
		t.Errorf("AutonomyDetail = %q, want a tracking-ref/remote-tip mismatch reason", result.AutonomyDetail)
	}
}

// TestSyncMain_LsRemoteFailsAfterFetch_OnlyAutonomyIsUnconfirmed: an ls-remote
// failure after a good fetch (origin unreachable between the two calls)
// leaves AutonomyRef empty but Status/Detail/FreshRef exactly what the
// merge-classification path would otherwise produce, and never leaks git's
// stderr (which can echo a remote URL) into the logged reason.
func TestSyncMain_LsRemoteFailsAfterFetch_OnlyAutonomyIsUnconfirmed(t *testing.T) {
	cases := []struct {
		name       string
		dryRun     bool
		wantDetail string
		wantFresh  string
	}{
		{"real pass", false, "fast-forwarded to origin/main", "HEAD"},
		{"dry run", true, "would fast-forward origin/main (dry-run, not merged)", "origin/main"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mainSyncGitEnv(t)
			local, bare := initBareOriginAndLocal(t)
			pushCommit(t, bare, "advance.txt", "advance")
			installGitShim(t, `for arg in "$@"; do
  if [ "$arg" = "ls-remote" ]; then
    echo "fatal: unable to access 'https://secret.example/repo.git/'" >&2
    exit 128
  fi
done`)

			result := syncMain(local, tc.dryRun)
			if result.Status != MainSyncSynced || result.Detail != tc.wantDetail || result.FreshRef != tc.wantFresh {
				t.Errorf("status/detail/freshRef = %q/%q/%q, want %q/%q/%q (unchanged by the ls-remote failure)",
					result.Status, result.Detail, result.FreshRef, MainSyncSynced, tc.wantDetail, tc.wantFresh)
			}
			if result.AutonomyRef != "" {
				t.Errorf("AutonomyRef = %q, want empty when ls-remote fails", result.AutonomyRef)
			}
			if !strings.Contains(result.AutonomyDetail, "ls-remote") {
				t.Errorf("AutonomyDetail = %q, want it to name the failed ls-remote", result.AutonomyDetail)
			}
			if strings.Contains(result.AutonomyDetail, "secret.example") {
				t.Errorf("AutonomyDetail = %q leaks git's stderr (a network call's stderr can echo a remote URL)", result.AutonomyDetail)
			}
		})
	}
}

// TestSyncMain_PushBetweenFetchAndLsRemote_UnconfirmedThisPassConfirmedNext
// covers a concurrent push: the remote tip advances after the fetch, so the
// tracking ref and the ls-remote tip disagree for that one pass (fail
// closed), and the next pass -- fetch and ls-remote agreeing again -- confirms
// normally. A fetch shim does the racing push right after the real fetch.
func TestSyncMain_PushBetweenFetchAndLsRemote_UnconfirmedThisPassConfirmedNext(t *testing.T) {
	mainSyncGitEnv(t)
	local, bare := initBareOriginAndLocal(t)
	scratch := filepath.Join(t.TempDir(), "scratch")
	gitTestNoDir(t, "clone", bare, scratch)
	commitFile(t, scratch, "racing.txt", "racing")
	oldTip := bareMainOID(t, bare)
	marker := filepath.Join(t.TempDir(), "pushed")
	installGitShim(t, fmt.Sprintf(`is_fetch=0
for arg in "$@"; do
  if [ "$arg" = "fetch" ]; then is_fetch=1; fi
done
if [ "$is_fetch" = "1" ] && [ ! -e %[1]q ]; then
  "$REAL_GIT" "$@"
  status=$?
  : > %[1]q
  "$REAL_GIT" -C %[2]q push origin main >/dev/null 2>&1
  exit $status
fi`, marker, scratch))

	first := syncMain(local, false)
	if got := bareMainOID(t, bare); got == oldTip {
		t.Fatal("test setup: the racing push never happened")
	}
	if first.AutonomyRef != "" {
		t.Errorf("first pass AutonomyRef = %q, want empty (remote tip moved after the fetch)", first.AutonomyRef)
	}
	if !strings.Contains(first.AutonomyDetail, "does not match remote main tip") {
		t.Errorf("first pass AutonomyDetail = %q, want a tracking-ref/remote-tip mismatch reason", first.AutonomyDetail)
	}

	second := syncMain(local, false)
	assertConfirmedOID(t, second, bare)
}

// TestSyncMain_TrackingRefAbsentButMainSlashXExists_IsNotMatched: `git
// for-each-ref refs/remotes/origin/main` matches refs/remotes/origin/main/x as
// a prefix, so only an exact refname may count. The fetch refspec here does
// not write refs/remotes/origin/main (otherwise the directory/file conflict
// would fail the fetch before the confirmation ever ran).
func TestSyncMain_TrackingRefAbsentButMainSlashXExists_IsNotMatched(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initBareOriginAndLocal(t)
	gitTest(t, local, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/mirror-main")
	oid := gitTest(t, local, "rev-parse", remoteMainAuthRef)
	gitTest(t, local, "update-ref", "-d", remoteMainAuthRef)
	gitTest(t, local, "update-ref", remoteMainAuthRef+"/x", oid)

	result := syncMain(local, false)
	if result.Status == MainSyncFetchFailed {
		t.Fatalf("status = MainSyncFetchFailed (%q), want the fetch to succeed", result.Detail)
	}
	if result.AutonomyRef != "" {
		t.Errorf("AutonomyRef = %q, want empty: only refs/remotes/origin/main/x exists, which must not match", result.AutonomyRef)
	}
	if !strings.Contains(result.AutonomyDetail, "absent") {
		t.Errorf("AutonomyDetail = %q, want the tracking-ref-absent reason (not a prefix match on main/x)", result.AutonomyDetail)
	}
}

// TestSyncMain_ConfirmationParsesStdoutSeparatelyFromStderr proves each
// confirmation call reads stdout alone, one call per subtest: a shim makes ONE
// of the two calls write to stderr on success before running the real git.
// The exactly-one-line parsers reject a merged stream, so the bare origin's
// OID still being confirmed is what distinguishes execGitSeparate from
// execGit's combined output for that call.
//   - ls-remote: a junk line, so merged output is two lines and malformed.
//   - for-each-ref: a line shaped like a real answer for a DIFFERENT object
//     (the parser skips a junk line as a non-match, so only an
//     answer-shaped one turns a merge into two matching lines).
//
// Timeout and WaitDelay bounding of execGitSeparate is covered by
// TestExecGitSeparate_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay.
func TestSyncMain_ConfirmationParsesStdoutSeparatelyFromStderr(t *testing.T) {
	const (
		lsRemoteNoise = "warning: stderr noise that must not reach the parser"
		// A valid 40-hex object ID that is not the bare origin's tip.
		otherOID = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	)
	forEachRefNoise := otherOID + " " + remoteMainAuthRef

	calls := []struct {
		name  string
		verb  string
		noise string
	}{
		{"ls-remote", "ls-remote", lsRemoteNoise},
		{"for-each-ref", "for-each-ref", forEachRefNoise},
	}
	for _, call := range calls {
		for _, dryRun := range []bool{false, true} {
			name := call.name + " noisy, real pass"
			if dryRun {
				name = call.name + " noisy, dry run"
			}
			t.Run(name, func(t *testing.T) {
				mainSyncGitEnv(t)
				local, bare := initBareOriginAndLocal(t)
				pushCommit(t, bare, "advance.txt", "advance")
				if want := bareMainOID(t, bare); want == otherOID {
					t.Fatalf("test setup: the bare tip %q collides with the decoy OID", want)
				}
				installGitShim(t, fmt.Sprintf(`for arg in "$@"; do
  if [ "$arg" = %q ]; then
    echo %q >&2
  fi
done`, call.verb, call.noise))

				result := syncMain(local, dryRun)
				if result.Status != MainSyncSynced {
					t.Fatalf("status = %q (%q), want MainSyncSynced", result.Status, result.Detail)
				}
				assertConfirmedOID(t, result, bare)
				if strings.Contains(result.AutonomyDetail, call.noise) || strings.Contains(result.Detail, call.noise) {
					t.Errorf("stderr noise leaked into detail: AutonomyDetail=%q Detail=%q", result.AutonomyDetail, result.Detail)
				}
			})
		}
	}
}

// TestExecGitSeparate_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay is
// the execGitSeparate twin of the execGit WaitDelay test: a `git` whose
// process exits normally but leaves a background grandchild holding the
// stdout/stderr pipes open must not stall execGitSeparate past its bounded
// WaitDelay. The shim also writes distinct text to each stream first, so the
// returned stdout and stderr are asserted to stay separate values. (The 60s
// gitTimeout is not exercised here: it is far too slow for a unit run.)
func TestExecGitSeparate_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay(t *testing.T) {
	const (
		outText = "answer on stdout"
		errText = "noise on stderr"
	)
	shimDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\necho %q\necho %q >&2\n(sleep 30 &) \nexit 0\n", outText, errText)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	// The grandchild keeps the pipes open well past WaitDelay, so Wait forces
	// them closed and returns an error wrapping ErrWaitDelay -- the fix working
	// as designed (bounded, not a hang), so it is expected here.
	stdout, stderr, err := execGitSeparate(t.TempDir(), "status")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("execGitSeparate returned nil error; expected the documented WaitDelay-forced-close error")
	}
	if elapsed > 20*time.Second {
		t.Fatalf("execGitSeparate took %v, want it to return well within gitTimeout thanks to WaitDelay (~%v)", elapsed, gitWaitDelay)
	}
	if elapsed < gitWaitDelay {
		t.Fatalf("execGitSeparate returned in %v, faster than gitWaitDelay (%v) -- test may not be exercising the intended lingering-grandchild path", elapsed, gitWaitDelay)
	}
	if stdout != outText {
		t.Errorf("stdout = %q, want %q", stdout, outText)
	}
	if stderr != errText {
		t.Errorf("stderr = %q, want %q", stderr, errText)
	}
}

const (
	testOID40 = "0123456789abcdef0123456789abcdef01234567"
	testOID64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// TestParseLsRemoteMainOID_Table: only exactly one line whose refname is
// exactly refs/heads/main with a 40- or 64-lowercase-hex object ID yields an
// OID. Real git against a local origin cannot produce the malformed shapes.
func TestParseLsRemoteMainOID_Table(t *testing.T) {
	cases := []struct {
		name   string
		out    string
		want   string
		wantOK bool
	}{
		{"one line sha1", testOID40 + "\trefs/heads/main", testOID40, true},
		{"one line sha256", testOID64 + "\trefs/heads/main", testOID64, true},
		{"trailing newline", testOID40 + "\trefs/heads/main\n", testOID40, true},
		{"zero lines", "", "", false},
		{"blank only", "\n", "", false},
		{"multiple lines", testOID40 + "\trefs/heads/main\n" + testOID64 + "\trefs/heads/main", "", false},
		{"wrong refname", testOID40 + "\trefs/heads/other", "", false},
		{"refname with main prefix", testOID40 + "\trefs/heads/main-old", "", false},
		{"refname nested under main", testOID40 + "\trefs/heads/main/x", "", false},
		{"tag refname", testOID40 + "\trefs/tags/main", "", false},
		{"no refname", testOID40, "", false},
		{"uppercase hex", strings.ToUpper(testOID40) + "\trefs/heads/main", "", false},
		{"short oid", "abc123\trefs/heads/main", "", false},
		{"41 hex chars", testOID40 + "0\trefs/heads/main", "", false},
		{"non-hex chars", strings.Repeat("g", 40) + "\trefs/heads/main", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLsRemoteMainOID(tc.out)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parseLsRemoteMainOID(%q) = (%q, %v), want (%q, %v)", tc.out, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestParseTrackingRefOID_Table: exactly one line whose refname equals
// remoteMainAuthRef counts; every other line (refs/remotes/origin/main/x,
// which a prefix match returns) is ignored; the OID must be 40 or 64
// lowercase hex.
func TestParseTrackingRefOID_Table(t *testing.T) {
	const ref = "refs/remotes/origin/main"
	cases := []struct {
		name   string
		out    string
		want   string
		wantOK bool
	}{
		{"exact line sha1", testOID40 + " " + ref, testOID40, true},
		{"exact line sha256", testOID64 + " " + ref, testOID64, true},
		{"trailing newline", testOID40 + " " + ref + "\n", testOID40, true},
		{"exact line plus main/x ignored", testOID64 + " " + ref + "/x\n" + testOID40 + " " + ref, testOID40, true},
		{"zero lines", "", "", false},
		{"only main/x", testOID40 + " " + ref + "/x", "", false},
		{"duplicate exact lines", testOID40 + " " + ref + "\n" + testOID40 + " " + ref, "", false},
		{"other remote's main", testOID40 + " refs/remotes/upstream/main", "", false},
		{"uppercase hex", strings.ToUpper(testOID40) + " " + ref, "", false},
		{"short oid", "abc123 " + ref, "", false},
		{"non-hex chars", strings.Repeat("z", 40) + " " + ref, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTrackingRefOID(tc.out)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parseTrackingRefOID(%q) = (%q, %v), want (%q, %v)", tc.out, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
