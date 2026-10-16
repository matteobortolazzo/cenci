package worktreeclean

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
)

// Removal tests run the real thing: real worktrees, real `git branch -D`, and
// snapshotTree of the whole fixture to prove what was and was not touched.

// finishedFixture is a ticket-42 worktree whose PR was closed unmerged, clean,
// pushed and idle for two hours: the simplest finished-path candidate.
func finishedFixture(t *testing.T) (*fixtureRepo, *fixtureWT, Options) {
	t.Helper()
	r := newOriginRepo(t)
	w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	ageWorktree(t, w, twoHours)
	return r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRClosedUnmerged)))
}

// staleFixture is a non-ticket worktree, clean, pushed and idle for 30 days:
// the simplest stale-path candidate (branch kept).
func staleFixture(t *testing.T) (*fixtureRepo, *fixtureWT, Options) {
	t.Helper()
	r := newOriginRepo(t)
	w := publishedWorktree(t, r, "scratch", "scratch")
	ageWorktree(t, w, thirtyDays)
	return r, w, testOptions(nil)
}

// parentDirsOf are the two directories whose entry list a removal changes, so
// whose mtime legitimately moves.
func parentDirsOf(r *fixtureRepo) []string {
	return []string{filepath.Join(r.Root, ".worktrees"), filepath.Join(r.CommonDir, "worktrees")}
}

// --- AC1 / AC2 / AC3: end-to-end reclaim ----------------------------------

func TestReclaimMergedPRRemovesWorktreeAndDeletesBranch(t *testing.T) {
	r := newOriginRepo(t)
	w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	prHead := squashMerge(t, r, w)
	ageWorktree(t, w, twoHours)
	mainTip := r.git(t, "rev-parse", "main")
	o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, prHead)))

	cs, rs := reclaimAll(t, r, o)
	wantVerdict(t, classificationFor(t, cs, w), VerdictFinished, "")
	rem := removalFor(t, rs, w)
	if !rem.Removed || !rem.BranchDeleted || rem.Err != nil || rem.Verdict != VerdictFinished {
		t.Fatalf("removal = %+v, want removed with its branch deleted and no error", rem)
	}
	if rem.BytesFreed <= 0 {
		t.Errorf("BytesFreed = %d, want the worktree's size", rem.BytesFreed)
	}
	if pathExists(w.Path) || pathExists(w.AdminDir) {
		t.Errorf("worktree dir (%v) or admin dir (%v) survived", pathExists(w.Path), pathExists(w.AdminDir))
	}
	if branchExists(r, w.Branch) {
		t.Errorf("local branch %s survived a finished removal", w.Branch)
	}
	if got := r.git(t, "rev-parse", "main"); got != mainTip {
		t.Errorf("main moved from %s to %s", mainTip, got)
	}
	if !pathExists(filepath.Join(r.Root, "tracked.txt")) {
		t.Error("the main checkout lost a file")
	}
}

func TestReclaimClosedUnmergedPushedCleanRemovesWithBranch(t *testing.T) {
	for _, out := range []ticketstate.Outcome{ticketstate.OutcomePRClosedUnmerged, ticketstate.OutcomeIssueClosed} {
		t.Run(out.String(), func(t *testing.T) {
			r := newOriginRepo(t)
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			tip := w.head(t)
			ageWorktree(t, w, twoHours)

			_, rs := reclaimAll(t, r, testOptions(outcomesFor(42, outcome(out))))
			rem := removalFor(t, rs, w)
			if !rem.Removed || !rem.BranchDeleted || rem.Err != nil {
				t.Fatalf("removal = %+v, want removed with its branch deleted and no error", rem)
			}
			if pathExists(w.Path) || pathExists(w.AdminDir) || branchExists(r, w.Branch) {
				t.Error("a finished worktree left its directory, admin dir or branch behind")
			}
			// The pushed work is the safety net: it is still on the remote.
			if got := runGit(t, filepath.Join(r.Tmp, "origin.git"), "rev-parse", "refs/heads/"+w.Branch); got != tip {
				t.Errorf("origin's %s = %s, want %s", w.Branch, got, tip)
			}
		})
	}
}

func TestReclaimStaleCleanPushedKeepsBranchAndIsRestorable(t *testing.T) {
	r, w, o := staleFixture(t)
	tip := w.head(t)

	cs, rs := reclaimAll(t, r, o)
	wantVerdict(t, classificationFor(t, cs, w), VerdictStale, "")
	rem := removalFor(t, rs, w)
	if !rem.Removed || rem.BranchDeleted || rem.Err != nil || rem.Verdict != VerdictStale {
		t.Fatalf("removal = %+v, want removed, branch kept, no error", rem)
	}
	if pathExists(w.Path) || pathExists(w.AdminDir) {
		t.Fatal("the stale worktree or its admin dir survived")
	}
	if !branchExists(r, w.Branch) {
		t.Fatalf("the stale path must keep branch %s", w.Branch)
	}

	r.git(t, "worktree", "add", "-q", w.Path, w.Branch)
	if got := runGit(t, w.Path, "rev-parse", "HEAD"); got != tip {
		t.Errorf("restored HEAD = %s, want %s", got, tip)
	}
	data, err := os.ReadFile(filepath.Join(w.Path, "work.txt"))
	if err != nil || string(data) != "work\n" {
		t.Errorf("restored work.txt = %q, %v", data, err)
	}
}

// --- AC4: kept worktrees are never touched --------------------------------

func TestReclaimLeavesKeptWorktreesUntouched(t *testing.T) {
	r := newOriginRepo(t)
	dirty := publishedWorktree(t, r, "scratch-dirty", "scratch-dirty")
	writeFile(t, filepath.Join(dirty.Path, "tracked.txt"), "modified\n")
	untracked := publishedWorktree(t, r, "scratch-untracked", "scratch-untracked")
	writeFile(t, filepath.Join(untracked.Path, "untracked.txt"), "untracked\n")
	unpushed := unpushedWorktree(t, r, "scratch-unpushed", "scratch-unpushed")
	locked := atMainWorktree(t, r, "scratch-locked", "scratch-locked")
	r.git(t, "worktree", "lock", locked.Path)
	recent := publishedWorktree(t, r, "42-recent", "feature/42-recent")
	mid := publishedWorktree(t, r, "43-mid", "feature/43-mid")
	writePipelineState(t, r, "43", "executed", fixedNow.Add(-time.Hour))
	unknown := publishedWorktree(t, r, "44-unknown", "feature/44-unknown")

	for _, w := range []*fixtureWT{dirty, untracked, unpushed, locked, mid} {
		ageWorktree(t, w, thirtyDays)
	}
	ageWorktree(t, recent, 30*time.Minute)
	ageWorktree(t, unknown, twoHours)
	o := testOptions(map[int]ticketstate.OutcomeResult{
		42: outcome(ticketstate.OutcomePRClosedUnmerged),
		43: outcome(ticketstate.OutcomePRClosedUnmerged),
	})

	want := map[*fixtureWT]KeepReason{
		dirty: KeepDirty, untracked: KeepUntracked, unpushed: KeepUnpushed, locked: KeepLocked,
		recent: KeepRecentlyActive, mid: KeepPipelineMidStage, unknown: KeepOutcomeUnknown,
	}
	before := snapshotTree(t, r.Tmp)
	cs, rs := reclaimAll(t, r, o)

	for w, reason := range want {
		wantVerdict(t, classificationFor(t, cs, w), VerdictKeep, reason)
		rem := removalFor(t, rs, w)
		if rem.Removed || rem.BranchDeleted || rem.Reason != reason {
			t.Errorf("%s: removal = %+v, want nothing removed and reason %q", w.Name, rem, reason)
		}
	}
	if len(rs) != len(cs) {
		t.Errorf("Reclaim returned %d removals for %d classifications", len(rs), len(cs))
	}
	if diffs := diffSnapshots(before, snapshotTree(t, r.Tmp)); len(diffs) != 0 {
		t.Fatalf("Reclaim of only kept worktrees changed the tree:\n%v", diffs)
	}
}

// --- AC7: removal safety --------------------------------------------------

func TestRemoveDeletesOnlyItsDirAndAdminDir(t *testing.T) {
	r := newOriginRepo(t)
	target := publishedWorktree(t, r, "scratch-a", "scratch-a")
	local := publishedWorktree(t, r, "7-local", "feature/7-local")
	translated := publishedWorktree(t, r, "8-translated", "feature/8-translated")
	gone := publishedWorktree(t, r, "9-gone", "feature/9-gone")
	for _, w := range []*fixtureWT{target, local, translated, gone} {
		ageWorktree(t, w, thirtyDays)
	}
	foreign := filepath.Join(r.Tmp, "foreign")
	rewriteAsForeign(t, translated, foreign)
	if err := os.RemoveAll(gone.Path); err != nil { // an admin entry that cannot be resolved from here
		t.Fatal(err)
	}
	o := testOptions(nil)
	c := classifyOne(t, r, target, o)
	wantVerdict(t, c, VerdictStale, "")

	before := snapshotTree(t, r.Tmp)
	rem := Remove(context.Background(), r.Repo(), c, o)
	after := snapshotTree(t, r.Tmp)

	if !rem.Removed || rem.Err != nil {
		t.Fatalf("removal = %+v, want removed without error", rem)
	}
	if pathExists(target.Path) || pathExists(target.AdminDir) {
		t.Fatal("the target's directory or admin dir survived")
	}
	removed := []string{target.Path, target.AdminDir}
	if bad := unexpectedDiffs(diffSnapshots(before, after), removed, parentDirsOf(r), nil); len(bad) != 0 {
		t.Fatalf("Remove changed more than the worktree dir and its admin dir:\n%v", bad)
	}
	for _, sibling := range []*fixtureWT{local, translated} {
		if !pathExists(sibling.Path) || !pathExists(sibling.AdminDir) {
			t.Errorf("sibling %s lost its directory or admin dir", sibling.Name)
		}
	}
	if !pathExists(gone.AdminDir) {
		t.Error("the unresolvable sibling's admin dir was removed")
	}
	if pathExists(foreign) {
		t.Errorf("the foreign root %s was created", foreign)
	}
}

func TestRemoveLeavesUnresolvableAdminEntryUntouched(t *testing.T) {
	r := newOriginRepo(t)
	target := publishedWorktree(t, r, "scratch-a", "scratch-a")
	gone := publishedWorktree(t, r, "9-gone", "feature/9-gone")
	ageWorktree(t, target, thirtyDays)
	if err := os.RemoveAll(gone.Path); err != nil {
		t.Fatal(err)
	}
	// Premise: this is the entry `git worktree prune` would delete, so a prune
	// anywhere in the removal would make the assertion below fail.
	if out := r.git(t, "worktree", "list", "--porcelain"); !strings.Contains(out, "\nprunable ") {
		t.Skipf("this git does not mark the entry prunable:\n%s", out)
	}
	adminBefore := snapshotTree(t, gone.AdminDir)

	cs, rs := reclaimAll(t, r, testOptions(nil))

	wantVerdict(t, classificationFor(t, cs, gone), VerdictKeep, KeepUnmapped)
	if rem := removalFor(t, rs, gone); rem.Removed {
		t.Errorf("the unresolvable entry was reported removed: %+v", rem)
	}
	if rem := removalFor(t, rs, target); !rem.Removed {
		t.Fatalf("the resolvable stale worktree was not removed: %+v", rem)
	}
	if diffs := diffSnapshots(adminBefore, snapshotTree(t, gone.AdminDir)); len(diffs) != 0 {
		t.Fatalf("the unresolvable admin entry changed:\n%v", diffs)
	}
}

func TestReclaimNeverRunsWorktreePruneOrForce(t *testing.T) {
	r := newOriginRepo(t)
	finished := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	stale := publishedWorktree(t, r, "scratch", "scratch")
	gone := publishedWorktree(t, r, "9-gone", "feature/9-gone") // a prunable entry to tempt a prune
	ageWorktree(t, finished, twoHours)
	ageWorktree(t, stale, thirtyDays)
	if err := os.RemoveAll(gone.Path); err != nil {
		t.Fatal(err)
	}
	calls := installGitLogger(t)

	_, rs := reclaimAll(t, r, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRClosedUnmerged))))

	if !removalFor(t, rs, finished).Removed || !removalFor(t, rs, stale).Removed {
		t.Fatalf("both candidates must be removed for the argv log to be meaningful: %+v", rs)
	}
	logged := calls()
	if len(logged) == 0 {
		t.Fatal("the git shim saw no calls: it is not in use")
	}
	sawBranchDelete := false
	for _, argv := range logged {
		for _, banned := range []string{"worktree", "prune", "remove", "--force", "-f", "gc"} {
			if slices.Contains(argv, banned) {
				t.Errorf("git was run with %q: %v", banned, argv)
			}
		}
		if slices.Contains(argv, "-D") {
			if !slices.Contains(argv, "branch") {
				t.Errorf("-D used outside `git branch`: %v", argv)
			}
			sawBranchDelete = true
		}
	}
	if !sawBranchDelete {
		t.Error("the finished removal never ran `git branch -D`: the argv log does not cover the deletion")
	}
	if !pathExists(gone.AdminDir) {
		t.Error("the prunable admin entry was deleted")
	}
}

func TestRemoveNeverTouchesMainWorktree(t *testing.T) {
	t.Run("forged paths in the classification", func(t *testing.T) {
		r, w, o := staleFixture(t)
		c := classifyOne(t, r, w, o)
		wantVerdict(t, c, VerdictStale, "")
		c.Entry.LocalPath = r.Root
		c.Entry.AdminDir = r.CommonDir
		c.Entry.Name = ".."
		before := snapshotTree(t, r.Tmp)

		Remove(context.Background(), r.Repo(), c, o)

		// Whatever Remove decided about the real worktree, the paths came from
		// AdminName, so the main checkout is intact.
		if bad := unexpectedDiffs(diffSnapshots(before, snapshotTree(t, r.Tmp)),
			[]string{w.Path, w.AdminDir}, parentDirsOf(r), nil); len(bad) != 0 {
			t.Fatalf("Remove with forged paths changed more than the real worktree:\n%v", bad)
		}
		for _, p := range []string{filepath.Join(r.Root, "tracked.txt"), filepath.Join(r.Root, ".gitignore"), filepath.Join(r.CommonDir, "HEAD")} {
			if !pathExists(p) {
				t.Errorf("main checkout lost %s", p)
			}
		}
		if got := r.git(t, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
			t.Errorf("main checkout HEAD = %q", got)
		}
	})

	t.Run("invalid admin names are refused", func(t *testing.T) {
		for _, name := range []string{"..", ".", "", "../..", "a/b", "../x"} {
			r, w, o := staleFixture(t)
			c := classifyOne(t, r, w, o)
			wantVerdict(t, c, VerdictStale, "")
			c.Entry.AdminName = name
			c.Entry.LocalPath = r.Root
			c.Entry.AdminDir = filepath.Join(r.CommonDir, "worktrees", name)
			before := snapshotTree(t, r.Tmp)

			rem := Remove(context.Background(), r.Repo(), c, o)

			if rem.Removed {
				t.Errorf("AdminName %q: Remove reported success: %+v", name, rem)
			}
			if diffs := diffSnapshots(before, snapshotTree(t, r.Tmp)); len(diffs) != 0 {
				t.Errorf("AdminName %q: Remove changed the tree:\n%v", name, diffs)
			}
		}
	})
}

func TestRemoveRechecksBeforeDeletion(t *testing.T) {
	cases := []struct {
		name    string
		stale   bool // use the stale fixture instead of the finished one
		wantErr string
		mutate  func(t *testing.T, r *fixtureRepo, w *fixtureWT)
	}{
		{name: "tracked file modified", wantErr: "keep/dirty", mutate: func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, "tracked.txt"), "modified\n")
		}},
		{name: "untracked file added", wantErr: "keep/untracked", mutate: func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, "untracked.txt"), "untracked\n")
		}},
		{name: "new unpushed commit", wantErr: "keep/unpushed", mutate: func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			w.commit(t, "late.txt", "late\n", oldCommitDate)
		}},
		{name: "worktree locked", wantErr: "keep/locked", mutate: func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			r.git(t, "worktree", "lock", w.Path)
		}},
		{name: "pipeline run started", wantErr: "keep/pipeline-mid-stage", mutate: func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			writePipelineState(t, r, "42", "executed", fixedNow)
		}},
		{name: "pipeline state became unreadable", wantErr: "keep/pipeline-unreadable", mutate: func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			writePipelineRaw(t, r.Root, "42", `{"stage":`)
		}},
		{name: "new pushed commit keeps the verdict but moves HEAD", stale: true, wantErr: "HEAD", mutate: func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			w.commit(t, "late.txt", "late\n", oldCommitDate)
			w.push(t)
		}},
		{name: "switched to another branch at the same HEAD", stale: true, wantErr: "branch", mutate: func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			w.git(t, "switch", "-q", "-c", "other-branch")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				r *fixtureRepo
				w *fixtureWT
				o Options
			)
			want := VerdictFinished
			if tc.stale {
				r, w, o = staleFixture(t)
				want = VerdictStale
			} else {
				r, w, o = finishedFixture(t)
			}
			c := classifyOne(t, r, w, o)
			wantVerdict(t, c, want, "")

			tc.mutate(t, r, w)
			before := snapshotTree(t, w.Path, w.AdminDir)
			rem := Remove(context.Background(), r.Repo(), c, o)

			if rem.Removed || rem.BranchDeleted || rem.Reason != KeepChanged {
				t.Fatalf("removal = %+v, want nothing removed and reason %q", rem, KeepChanged)
			}
			if rem.Err == nil || !strings.Contains(rem.Err.Error(), tc.wantErr) {
				t.Errorf("Err = %v, want it to name what changed (%q)", rem.Err, tc.wantErr)
			}
			if diffs := diffSnapshots(before, snapshotTree(t, w.Path, w.AdminDir)); len(diffs) != 0 {
				t.Fatalf("a refused removal still changed the worktree:\n%v", diffs)
			}
			if !branchExists(r, w.Branch) {
				t.Fatal("a refused removal deleted the branch")
			}
		})
	}
}

// A moved worktree keeps its admin name, so only the classified Entry itself
// shows the re-check that the worktree is no longer the one that was classified.
func TestRemoveKeepsWorktreeWhoseAdminEntryWasRepointed(t *testing.T) {
	r, w, o := staleFixture(t)
	c := classifyOne(t, r, w, o)
	wantVerdict(t, c, VerdictStale, "")
	movedTo := filepath.Join(r.Root, ".worktrees", "scratch-moved")
	r.git(t, "worktree", "move", w.Path, movedTo)
	fresh := classifyOne(t, r, w, o)
	wantVerdict(t, fresh, VerdictStale, "") // premise: verdict, HEAD and branch alone still match
	if fresh.Facts.HeadSHA != c.Facts.HeadSHA || fresh.Facts.Branch != c.Facts.Branch {
		t.Fatalf("fixture premise broken: HEAD/branch changed (%s %s -> %s %s)", c.Facts.HeadSHA, c.Facts.Branch, fresh.Facts.HeadSHA, fresh.Facts.Branch)
	}
	before := snapshotTree(t, movedTo, w.AdminDir)

	rem := Remove(context.Background(), r.Repo(), c, o)

	if rem.Removed || rem.Reason != KeepChanged || rem.Err == nil {
		t.Fatalf("removal = %+v, want a KeepChanged refusal with an error", rem)
	}
	if diffs := diffSnapshots(before, snapshotTree(t, movedTo, w.AdminDir)); len(diffs) != 0 {
		t.Fatalf("a refused removal still changed the worktree:\n%v", diffs)
	}
}

func TestRemoveDoesNotFollowSymlinks(t *testing.T) {
	r := newOriginRepo(t)
	outside := outsideSentinel(t, r, "outside")
	sentinel := filepath.Join(outside, "sentinel.txt")
	w := publishedWorktree(t, r, "scratch", "scratch")

	// Committed symlinks: one to a directory outside, one to a file outside.
	if err := os.Symlink(outside, filepath.Join(w.Path, "link-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(w.Path, "link-file")); err != nil {
		t.Fatal(err)
	}
	w.git(t, "add", "link-dir", "link-file")
	runGitAt(t, w.Path, oldCommitDate, "commit", "-q", "-m", "add symlinks")
	w.push(t)
	// Ignored symlinks: invisible to git status, but still inside the tree.
	if err := os.MkdirAll(filepath.Join(w.Path, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.Path, "ignored", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(w.Path, "build.log")); err != nil {
		t.Fatal(err)
	}
	ageWorktree(t, w, thirtyDays)
	o := testOptions(nil)
	c := classifyOne(t, r, w, o)
	wantVerdict(t, c, VerdictStale, "")
	outsideBefore := snapshotTree(t, outside)

	rem := Remove(context.Background(), r.Repo(), c, o)

	if !rem.Removed || rem.Err != nil {
		t.Fatalf("removal = %+v, want removed without error", rem)
	}
	if pathExists(w.Path) {
		t.Fatal("the worktree directory survived")
	}
	if diffs := diffSnapshots(outsideBefore, snapshotTree(t, outside)); len(diffs) != 0 {
		t.Fatalf("Remove followed a symlink out of the worktree:\n%v", diffs)
	}
}

func TestRemoveRefusesSymlinkSwapBeforeDelete(t *testing.T) {
	t.Run("the worktree directory is swapped for a symlink", func(t *testing.T) {
		r, w, o := staleFixture(t)
		decoy := outsideSentinel(t, r, "decoy")
		c := classifyOne(t, r, w, o)
		wantVerdict(t, c, VerdictStale, "")
		decoyBefore := snapshotTree(t, decoy)
		moved := w.Path + ".moved"
		setVar(t, &beforeDelete, func() {
			if err := os.Rename(w.Path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(decoy, w.Path); err != nil {
				t.Fatal(err)
			}
		})

		rem := Remove(context.Background(), r.Repo(), c, o)

		if rem.Removed || rem.Err == nil {
			t.Fatalf("removal = %+v, want a refusal with an error", rem)
		}
		if diffs := diffSnapshots(decoyBefore, snapshotTree(t, decoy)); len(diffs) != 0 {
			t.Fatalf("the delete followed the swapped-in symlink:\n%v", diffs)
		}
		if !pathExists(filepath.Join(moved, "work.txt")) {
			t.Fatal("the real worktree was deleted")
		}
	})

	t.Run("the .worktrees directory is swapped for a symlink", func(t *testing.T) {
		r, w, o := staleFixture(t)
		c := classifyOne(t, r, w, o)
		wantVerdict(t, c, VerdictStale, "")
		// A decoy parent holding a directory of the same name as the worktree.
		decoyParent := filepath.Join(r.Tmp, "decoy-parent")
		writeFile(t, filepath.Join(decoyParent, w.Name, "sentinel.txt"), "must survive\n")
		decoyBefore := snapshotTree(t, decoyParent)
		parent := filepath.Join(r.Root, ".worktrees")
		moved := parent + ".moved"
		setVar(t, &beforeDelete, func() {
			if err := os.Rename(parent, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(decoyParent, parent); err != nil {
				t.Fatal(err)
			}
		})

		rem := Remove(context.Background(), r.Repo(), c, o)

		if rem.Removed || rem.Err == nil {
			t.Fatalf("removal = %+v, want a refusal with an error", rem)
		}
		if diffs := diffSnapshots(decoyBefore, snapshotTree(t, decoyParent)); len(diffs) != 0 {
			t.Fatalf("the delete followed the swapped-in .worktrees symlink:\n%v", diffs)
		}
		if !pathExists(filepath.Join(moved, w.Name, "work.txt")) {
			t.Fatal("the real worktree was deleted")
		}
	})

	t.Run("the .git directory is swapped for a symlink to a decoy admin tree", func(t *testing.T) {
		r, w, o := finishedFixture(t)
		c := classifyOne(t, r, w, o)
		wantVerdict(t, c, VerdictFinished, "")
		// A decoy "git dir" holding worktrees/<AdminName>, the path an
		// intermediate-symlink-following delete would remove.
		decoy := filepath.Join(r.Tmp, "decoy-git")
		writeFile(t, filepath.Join(decoy, "worktrees", w.AdminName, "sentinel.txt"), "must survive\n")
		decoyBefore := snapshotTree(t, decoy)
		moved := r.CommonDir + ".moved"
		setVar(t, &beforeDelete, func() {
			if err := os.Rename(r.CommonDir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(decoy, r.CommonDir); err != nil {
				t.Fatal(err)
			}
		})

		rem := Remove(context.Background(), r.Repo(), c, o)

		if rem.Err == nil || rem.BranchDeleted {
			t.Fatalf("removal = %+v, want an error and the branch kept", rem)
		}
		if diffs := diffSnapshots(decoyBefore, snapshotTree(t, decoy)); len(diffs) != 0 {
			t.Fatalf("the admin delete followed the swapped-in .git symlink:\n%v", diffs)
		}
		if !pathExists(filepath.Join(moved, "worktrees", w.AdminName, "gitdir")) {
			t.Error("the real admin dir was deleted")
		}
		if !gitBranchExistsIn(moved, w.Branch) {
			t.Error("the branch was deleted")
		}
	})

	t.Run("the .git directory is swapped for a symlink before the branch deletion", func(t *testing.T) {
		r, w, o := finishedFixture(t)
		c := classifyOne(t, r, w, o)
		wantVerdict(t, c, VerdictFinished, "")
		moved := r.CommonDir + ".moved"
		setVar(t, &beforeBranchDelete, func() {
			if err := os.Rename(r.CommonDir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, r.CommonDir); err != nil {
				t.Fatal(err)
			}
		})

		rem := Remove(context.Background(), r.Repo(), c, o)

		if rem.BranchDeleted || rem.Err == nil {
			t.Fatalf("removal = %+v, want the branch kept and an error reported", rem)
		}
		if !gitBranchExistsIn(moved, w.Branch) {
			t.Error("git branch -D ran through the swapped-in .git symlink")
		}
	})
}

// git honors a commondir file inside the --git-dir it is given, and its refs
// backend does so even when GIT_COMMON_DIR is set. An agent that can write
// <MainRoot>/.git/commondir must not be able to point the branch deletion at
// another repository's refs.
func TestRemoveRefusesCommondirRedirectBeforeBranchDeletion(t *testing.T) {
	r, w, o := finishedFixture(t)
	c := classifyOne(t, r, w, o)
	wantVerdict(t, c, VerdictFinished, "")

	// Another host repo holding the same branch name at the same tip, the one case
	// where a redirected tip check would still pass.
	other := newRepo(t, "other")
	runGit(t, other.Root, "fetch", "-q", r.Root, w.Branch)
	runGit(t, other.Root, "branch", w.Branch, "FETCH_HEAD")
	if got, want := runGit(t, other.Root, "rev-parse", "refs/heads/"+w.Branch), c.Facts.HeadSHA; got != want {
		t.Fatalf("decoy branch tip = %s, want %s", got, want)
	}
	setVar(t, &beforeBranchDelete, func() {
		writeFile(t, filepath.Join(r.CommonDir, "commondir"), other.CommonDir+"\n")
	})

	rem := Remove(context.Background(), r.Repo(), c, o)

	if !gitBranchExistsIn(other.CommonDir, w.Branch) {
		t.Error("git branch -D followed .git/commondir into another repository")
	}
	if !rem.Removed || rem.BranchDeleted || rem.Err == nil || !strings.Contains(rem.Err.Error(), "commondir") {
		t.Fatalf("removal = %+v, want removed, the branch kept and an error naming commondir", rem)
	}
	// Drop the planted file first: while it exists, every git call against the
	// repo reads the other repository's refs.
	if err := os.Remove(filepath.Join(r.CommonDir, "commondir")); err != nil {
		t.Fatal(err)
	}
	if !branchExists(r, w.Branch) {
		t.Error("the repo's own branch was deleted despite the refusal")
	}
}

func TestRemoveAdminDirRefusesCommonDirOutsideMainRootGit(t *testing.T) {
	r, w, _ := finishedFixture(t)
	other := newRepo(t, "other")
	// A hand-built Repo whose CommonDir is another repo's .git: the admin delete
	// must refuse it instead of falling back to a weaker single-parent check.
	repo := Repo{MainRoot: r.Root, CommonDir: other.CommonDir}

	if _, err := removeAdminDir(repo, w.AdminName); err == nil {
		t.Fatal("removeAdminDir accepted a CommonDir that is not <MainRoot>/.git")
	}
	if !pathExists(w.AdminDir) {
		t.Error("the admin dir was deleted")
	}
}

func TestRemoveDisablesRepoHooksForBranchDeletion(t *testing.T) {
	r, w, o := finishedFixture(t)
	c := classifyOne(t, r, w, o)
	wantVerdict(t, c, VerdictFinished, "")

	// The repo's hooks dir is agent-writable. Plant a reference-transaction hook
	// (it runs on every ref update, branch -D included) after the fixture is built.
	marker := filepath.Join(r.Tmp, "hook-ran")
	hook := filepath.Join(r.CommonDir, "hooks", "reference-transaction")
	exectest.WriteExecutable(t, hook, "#!/bin/sh\necho \"$1\" >> "+exectest.ShellQuote(marker)+"\ncat >/dev/null\n")

	// Positive control: a plain `git branch -D` does run the hook here.
	r.git(t, "branch", "control-branch")
	r.git(t, "branch", "-D", "control-branch")
	if !pathExists(marker) {
		t.Skip("this git does not run reference-transaction hooks; nothing to prove")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	rem := Remove(context.Background(), r.Repo(), c, o)

	if !rem.Removed || !rem.BranchDeleted || rem.Err != nil {
		t.Fatalf("removal = %+v, want removed with its branch deleted", rem)
	}
	if branchExists(r, w.Branch) {
		t.Fatal("the branch survived, so the hook assertion below proves nothing")
	}
	if pathExists(marker) {
		t.Fatal("the repo's reference-transaction hook ran during the removal")
	}
}

func TestReclaimContinuesAfterPartialFailure(t *testing.T) {
	r := newOriginRepo(t)
	first := publishedWorktree(t, r, "42-a", "feature/42-a")
	second := publishedWorktree(t, r, "43-b", "feature/43-b")
	ageWorktree(t, first, twoHours)
	ageWorktree(t, second, twoHours)
	o := testOptions(map[int]ticketstate.OutcomeResult{
		42: outcome(ticketstate.OutcomePRClosedUnmerged),
		43: outcome(ticketstate.OutcomePRClosedUnmerged),
	})
	// A leftover ref lock makes git refuse to delete the first branch.
	lock := filepath.Join(r.CommonDir, "refs", "heads", "feature", "42-a.lock")
	writeFile(t, lock, "")

	cs, rs := reclaimAll(t, r, o)

	wantVerdict(t, classificationFor(t, cs, first), VerdictFinished, "")
	a := removalFor(t, rs, first)
	if a.Err == nil || a.BranchDeleted {
		t.Fatalf("first removal = %+v, want the branch deletion reported as an error", a)
	}
	if !a.Removed || pathExists(first.Path) || pathExists(first.AdminDir) {
		t.Errorf("first removal = %+v: its directories were deleted before the branch step and must be reported as removed", a)
	}
	if !branchExists(r, first.Branch) {
		t.Error("the branch whose deletion failed disappeared")
	}
	b := removalFor(t, rs, second)
	if !b.Removed || !b.BranchDeleted || b.Err != nil {
		t.Fatalf("second removal = %+v, want it to proceed after the first one failed", b)
	}
	if pathExists(second.Path) || branchExists(r, second.Branch) {
		t.Error("the second worktree was not fully reclaimed")
	}
}

// --- AC9: translated worktrees --------------------------------------------

func TestReclaimTranslatedWorktree(t *testing.T) {
	t.Run("finished", func(t *testing.T) {
		r, w, o := finishedFixture(t)
		sibling := publishedWorktree(t, r, "7-sibling", "feature/7-sibling")
		// Idle for two hours: inside staleAfter, with no outcome for ticket 7, so
		// reclaimAll keeps the sibling.
		ageWorktree(t, sibling, twoHours)
		foreign := filepath.Join(r.Tmp, "foreign")
		rewriteAsForeign(t, w, foreign)
		before := snapshotTree(t, r.Tmp)

		cs, rs := reclaimAll(t, r, o)

		c := classificationFor(t, cs, w)
		wantVerdict(t, c, VerdictFinished, "")
		if c.Entry.Mapping != MappingTranslated {
			t.Fatalf("mapping = %q, want %q: the fixture is not translated", c.Entry.Mapping, MappingTranslated)
		}
		rem := removalFor(t, rs, w)
		if !rem.Removed || !rem.BranchDeleted || rem.Err != nil {
			t.Fatalf("removal = %+v, want removed with its branch deleted", rem)
		}
		if !pathExists(sibling.Path) || !pathExists(sibling.AdminDir) {
			t.Errorf("the sibling %s lost its directory or admin dir", sibling.Name)
		}
		if pathExists(foreign) {
			t.Errorf("the foreign root %s was created", foreign)
		}
		refsAreFine := func(p string) bool {
			return p == r.CommonDir || underAny(p, []string{
				filepath.Join(r.CommonDir, "refs"), filepath.Join(r.CommonDir, "logs"),
				filepath.Join(r.CommonDir, "packed-refs"), filepath.Join(r.CommonDir, "config"),
			})
		}
		after := snapshotTree(t, r.Tmp)
		if bad := unexpectedDiffs(diffSnapshots(before, after), []string{w.Path, w.AdminDir}, parentDirsOf(r), refsAreFine); len(bad) != 0 {
			t.Fatalf("reclaiming the translated worktree changed other paths:\n%v", bad)
		}
	})

	t.Run("stale", func(t *testing.T) {
		r, w, o := staleFixture(t)
		foreign := filepath.Join(r.Tmp, "foreign")
		rewriteAsForeign(t, w, foreign)
		before := snapshotTree(t, r.Tmp)

		cs, rs := reclaimAll(t, r, o)

		c := classificationFor(t, cs, w)
		wantVerdict(t, c, VerdictStale, "")
		if c.Entry.Mapping != MappingTranslated {
			t.Fatalf("mapping = %q, want %q: the fixture is not translated", c.Entry.Mapping, MappingTranslated)
		}
		rem := removalFor(t, rs, w)
		if !rem.Removed || rem.BranchDeleted || rem.Err != nil {
			t.Fatalf("removal = %+v, want removed, branch kept", rem)
		}
		if !branchExists(r, w.Branch) {
			t.Error("the stale path must keep the branch")
		}
		if pathExists(foreign) {
			t.Errorf("the foreign root %s was created", foreign)
		}
		if bad := unexpectedDiffs(diffSnapshots(before, snapshotTree(t, r.Tmp)),
			[]string{w.Path, w.AdminDir}, parentDirsOf(r), nil); len(bad) != 0 {
			t.Fatalf("reclaiming the translated worktree changed other paths:\n%v", bad)
		}
	})
}

// --- hardening beyond the table --------------------------------------------

func TestRemoveKeepsBranchWhenTipMovedAfterRecheck(t *testing.T) {
	r, w, o := finishedFixture(t)
	c := classifyOne(t, r, w, o)
	wantVerdict(t, c, VerdictFinished, "")
	moved := r.git(t, "rev-parse", "main")
	if moved == c.Facts.HeadSHA {
		t.Fatal("fixture premise broken: the branch must move to a different commit")
	}
	setVar(t, &beforeDelete, func() {
		// Someone moves the branch after the safety re-check passed.
		r.git(t, "update-ref", "refs/heads/"+w.Branch, moved)
	})

	rem := Remove(context.Background(), r.Repo(), c, o)

	if rem.BranchDeleted || rem.Err == nil {
		t.Fatalf("removal = %+v, want the branch kept and an error reported", rem)
	}
	if !branchExists(r, w.Branch) {
		t.Fatal("the branch was deleted although its tip no longer matched the re-checked HEAD")
	}
	if got := r.git(t, "rev-parse", "refs/heads/"+w.Branch); got != moved {
		t.Fatalf("branch tip = %s, want the moved tip %s untouched", got, moved)
	}
}

func TestReclaimCancelledContextMarksRemainingEntries(t *testing.T) {
	r := newOriginRepo(t)
	first := publishedWorktree(t, r, "42-a", "feature/42-a")
	second := publishedWorktree(t, r, "43-b", "feature/43-b")
	ageWorktree(t, first, twoHours)
	ageWorktree(t, second, twoHours)
	o := testOptions(map[int]ticketstate.OutcomeResult{
		42: outcome(ticketstate.OutcomePRClosedUnmerged),
		43: outcome(ticketstate.OutcomePRClosedUnmerged),
	})
	repo, entries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	cs := ClassifyAll(context.Background(), repo, entries, o)
	for _, w := range []*fixtureWT{first, second} {
		wantVerdict(t, classificationFor(t, cs, w), VerdictFinished, "") // both are removable on a live context
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rs := Reclaim(ctx, repo, cs, o)

	if len(rs) != len(cs) {
		t.Fatalf("Reclaim returned %d removals for %d classifications", len(rs), len(cs))
	}
	for _, rem := range rs {
		if rem.Removed || !errors.Is(rem.Err, context.Canceled) {
			t.Errorf("removal = %+v, want nothing removed and context.Canceled", rem)
		}
	}
	for _, w := range []*fixtureWT{first, second} {
		if !pathExists(w.Path) || !pathExists(w.AdminDir) || !branchExists(r, w.Branch) {
			t.Errorf("%s lost something to a cancelled Reclaim", w.Name)
		}
	}
}

// gitBranchExistsIn reports whether refs/heads/<branch> exists in the git dir.
func gitBranchExistsIn(gitDir, branch string) bool {
	_, err := tryGit(filepath.Dir(gitDir), oldCommitDate, "--git-dir="+gitDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}
