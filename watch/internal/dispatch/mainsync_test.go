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

// -- 1-12: syncMain / syncMains classifier suite (real temp repos) ----------

// TestSyncMain_BehindOrigin_FastForwardsAndReportsSynced covers plan test 1:
// a local repo strictly behind origin/main is fast-forwarded for real, and
// the outcome is MainSyncSynced.
func TestSyncMain_BehindOrigin_FastForwardsAndReportsSynced(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	originHEAD := gitTest(t, origin, "rev-parse", "HEAD")

	status, _ := syncMain(local, false)
	if status != MainSyncSynced {
		t.Fatalf("status = %q, want MainSyncSynced", status)
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

	status, _ := syncMain(local, false)
	if status != MainSyncSynced {
		t.Fatalf("status = %q, want MainSyncSynced", status)
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

	status, _ := syncMain(local, false)
	if status != MainSyncSynced {
		t.Fatalf("status = %q, want MainSyncSynced", status)
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

	status, _ := syncMain(local, false)
	if status != MainSyncDiverged {
		t.Fatalf("status = %q, want MainSyncDiverged", status)
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

	status, _ := syncMain(local, false)
	if status != MainSyncFetchFailed {
		t.Fatalf("status = %q, want MainSyncFetchFailed", status)
	}
	if status == MainSyncDiverged || status == MainSyncFailed {
		t.Fatalf("MainSyncFetchFailed must not collapse into Diverged or Failed, got %q", status)
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

	status, _ := syncMain(local, false)
	if status != MainSyncFailed {
		t.Fatalf("status = %q, want MainSyncFailed", status)
	}
	if status == MainSyncDiverged || status == MainSyncSynced {
		t.Fatalf("a dirty-tree-blocked fast-forward must not collapse into Diverged or Synced, got %q", status)
	}
}

// TestSyncMain_NonRepoDir_ReportsFailed covers plan test 7 (Q1): a plain
// non-repo temp dir must report MainSyncFailed.
func TestSyncMain_NonRepoDir_ReportsFailed(t *testing.T) {
	mainSyncGitEnv(t)
	dir := t.TempDir()

	status, _ := syncMain(dir, false)
	if status != MainSyncFailed {
		t.Fatalf("status = %q, want MainSyncFailed", status)
	}
}

// TestSyncMain_FeatureBranchCheckedOut_SkipsWithoutTouchingMainOrFeatureHEAD
// covers plan test 8 (Q2): a repo with a feature branch checked out while
// origin/main advances must be skipped entirely -- never fast-forward the
// wrong branch -- and neither the local `main` ref nor the checked-out
// feature branch's HEAD may move.
func TestSyncMain_FeatureBranchCheckedOut_SkipsWithoutTouchingMainOrFeatureHEAD(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	gitTest(t, local, "checkout", "-b", "feature")
	featureHEADBefore := gitTest(t, local, "rev-parse", "HEAD")
	mainRefBefore := gitTest(t, local, "rev-parse", "main")
	commitFile(t, origin, "advance.txt", "advance")

	status, _ := syncMain(local, false)
	if status != MainSyncSkipped {
		t.Fatalf("status = %q, want MainSyncSkipped", status)
	}
	if got := gitTest(t, local, "rev-parse", "main"); got != mainRefBefore {
		t.Errorf("local main ref = %s, want unchanged %s (no wrong-branch fast-forward)", got, mainRefBefore)
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != featureHEADBefore {
		t.Errorf("feature branch HEAD = %s, want unchanged %s", got, featureHEADBefore)
	}
}

// TestSyncMain_DetachedHEAD_ReportsSkippedNotFailed covers plan test 9: a
// detached HEAD in a real repo is a Skipped case (the dir IS a repo, it just
// isn't on main), distinct from the non-repo-dir MainSyncFailed case (test 7).
func TestSyncMain_DetachedHEAD_ReportsSkippedNotFailed(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	sha := gitTest(t, local, "rev-parse", "HEAD")
	gitTest(t, local, "checkout", "--detach", sha)

	status, _ := syncMain(local, false)
	if status != MainSyncSkipped {
		t.Fatalf("status = %q, want MainSyncSkipped (detached HEAD, not Failed)", status)
	}
}

// TestSyncMain_EmptyDir_NeverProbesCwd covers plan test 10 (mirrors
// TestProbeStage_EmptyDir_NeverProbes_NoCwdRelativeRead, collect_test.go):
// dir=="" must skip entirely rather than resolve a repo root from the
// daemon's own cwd, even when the cwd IS a real repo behind its origin.
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

	status, _ := syncMain("", false)
	if status != MainSyncSkipped {
		t.Fatalf("status = %q, want MainSyncSkipped -- dir=\"\" must never probe/sync", status)
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Errorf("cwd repo HEAD = %s, want unchanged %s -- dir=\"\" must never mutate the cwd repo", got, beforeHEAD)
	}
}

// TestSyncMain_DryRun_ReportsSyncedWithoutMutatingHead covers plan test 11:
// dryRun=true still fetches and classifies (an accurate gate needs the fetch)
// but must never run the merge -- HEAD stays put even though the outcome
// reported is the would-be-synced status.
func TestSyncMain_DryRun_ReportsSyncedWithoutMutatingHead(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	beforeHEAD := gitTest(t, local, "rev-parse", "HEAD")

	status, _ := syncMain(local, true)
	if status != MainSyncSynced {
		t.Fatalf("status = %q, want MainSyncSynced (classified, not merged)", status)
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

	if got["o/r1"] != MainSyncSynced {
		t.Errorf(`syncMains()["o/r1"] = %q, want MainSyncSynced`, got["o/r1"])
	}
	if got["o/r2"] != MainSyncDiverged {
		t.Errorf(`syncMains()["o/r2"] = %q, want MainSyncDiverged`, got["o/r2"])
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

	status, detail := syncMain(local, false)
	if status != MainSyncFailed {
		t.Fatalf("status = %q, detail = %q, want MainSyncFailed", status, detail)
	}
	if status == MainSyncDiverged || status == MainSyncSynced {
		t.Fatalf("a genuine merge-base command error must not collapse into Diverged or Synced, got %q", status)
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

// TestSyncMain_BranchChangedBetweenFetchAndMerge_SkipsWithoutFastForwardingWrongBranch
// covers review fix #4 (TOCTOU): if the checked-out branch changes during
// the fetch window, the merge must never run against the wrong branch --
// syncMain must re-verify HEAD immediately before merging. Simulated
// deterministically (no real race) via a `git` PATH shim that, only for the
// `fetch` subcommand, delegates to the real git fetch and then checks the
// repo out onto a different branch as a side effect -- exercising the same
// re-check code path a genuine mid-fetch checkout would hit.
func TestSyncMain_BranchChangedBetweenFetchAndMerge_SkipsWithoutFastForwardingWrongBranch(t *testing.T) {
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

	status, detail := syncMain(local, false)
	if status != MainSyncSkipped {
		t.Fatalf("status = %q, detail = %q, want MainSyncSkipped", status, detail)
	}
	if got := gitTest(t, local, "rev-parse", "main"); got != mainRefBefore {
		t.Errorf("local main ref = %s, want unchanged %s (must never fast-forward after a mid-fetch branch change)", got, mainRefBefore)
	}
}

// TestSyncMain_PreMergeHEADRecheckErrors_ReportsFailedNotSkipped covers #822
// review round 2 fix #1: a genuine probe error on the pre-merge HEAD re-check
// (not merely a legitimate branch change, already covered by
// TestSyncMain_BranchChangedBetweenFetchAndMerge_SkipsWithoutFastForwardingWrongBranch)
// must report MainSyncFailed, not MainSyncSkipped -- mainSyncSkip (decide.go)
// treats Skipped as ungated, so folding a real error into it would silently
// downgrade the gate to permissive on a broken git state.
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

	status, detail := syncMain(local, false)
	if status != MainSyncFailed {
		t.Fatalf("status = %q, detail = %q, want MainSyncFailed", status, detail)
	}
	if status == MainSyncSkipped {
		t.Fatalf("a genuine pre-merge HEAD re-check error must not collapse into MainSyncSkipped (ungated), got %q", status)
	}
	if got := gitTest(t, local, "rev-parse", "main"); got != mainRefBefore {
		t.Errorf("local main ref = %s, want unchanged %s (a failed re-check must never fast-forward)", got, mainRefBefore)
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

	if got["o/r"] != MainSyncSkipped {
		t.Fatalf(`syncMains()["o/r"] = %q, want MainSyncSkipped`, got["o/r"])
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
  "pr list") printf '[]' ;;
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
	if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil); err != nil {
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
