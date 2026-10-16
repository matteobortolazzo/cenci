package worktreeclean

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
)

// Every test here classifies real git worktrees through the production
// Enumerate -> Classify path. Activity is controlled by aging files with
// ageWorktree (relative to the pinned fixedNow); a fixture that writes after
// aging would only add ancient (real-clock) mtimes, never recent ones.

const (
	ticket42Name   = "42-feature"
	ticket42Branch = "feature/42-feature"
	// twoHours is comfortably past the finished grace and well inside staleAfter,
	// so only the finished path can apply. thirtyDays is past staleAfter.
	twoHours   = 2 * time.Hour
	thirtyDays = 30 * 24 * time.Hour
)

func TestKeepReasonValuesAreTheClosedKebabSet(t *testing.T) {
	want := map[KeepReason]string{
		KeepUnmapped: "unmapped", KeepGitError: "git-error", KeepLocked: "locked",
		KeepPipelineMidStage: "pipeline-mid-stage", KeepPipelineUnreadable: "pipeline-unreadable",
		KeepRecentlyActive: "recently-active", KeepDirty: "dirty", KeepUntracked: "untracked", KeepUnpushed: "unpushed",
		KeepOutcomeUnknown: "outcome-unknown", KeepActive: "active",
		KeepInUse: "in-use", KeepInUseUnknown: "in-use-unknown", KeepBabysit: "babysit", KeepBabysitUnknown: "babysit-unknown",
		KeepChanged: "changed", KeepInvalidOptions: "invalid-options",
	}
	if len(want) != 17 {
		t.Fatalf("the closed set has 17 reasons, test lists %d", len(want))
	}
	for reason, text := range want {
		if string(reason) != text {
			t.Errorf("KeepReason %q, want %q", reason, text)
		}
	}
	for v, text := range map[Verdict]string{VerdictKeep: "keep", VerdictFinished: "finished", VerdictStale: "stale"} {
		if string(v) != text {
			t.Errorf("Verdict %q, want %q", v, text)
		}
	}
}

// --- AC1: merged PR -------------------------------------------------------

func TestClassifyMergedPRHeadEqualsHEADIsFinished(t *testing.T) {
	// No remote at all: the commit was never pushed, so only the merged-PR
	// head rule can make it count as pushed.
	r := newRepo(t, "repo")
	w := unpushedWorktree(t, r, ticket42Name, ticket42Branch)
	head := w.head(t)
	ageWorktree(t, w, twoHours)

	t.Run("single head equal to HEAD", func(t *testing.T) {
		c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, head))))
		wantVerdict(t, c, VerdictFinished, "")
	})
	t.Run("among several heads, absent and unrelated ones first", func(t *testing.T) {
		absent := strings.Repeat("a", 40)
		unrelated := r.git(t, "rev-parse", "main") // present locally, but HEAD is not its ancestor
		c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, absent, unrelated, head))))
		wantVerdict(t, c, VerdictFinished, "")
	})
}

func TestClassifySquashMergedAncestorOfPRHeadIsFinished(t *testing.T) {
	t.Run("HEAD equals the PR head", func(t *testing.T) {
		r := newOriginRepo(t)
		w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
		head := squashMerge(t, r, w)
		ageWorktree(t, w, twoHours)

		c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, head))))
		wantVerdict(t, c, VerdictFinished, "")
	})
	t.Run("HEAD is a strict ancestor of the PR head", func(t *testing.T) {
		r := newOriginRepo(t)
		w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
		first := w.head(t)
		w.commit(t, "second.txt", "second\n", oldCommitDate)
		w.push(t)
		prHead := squashMerge(t, r, w) // the PR head is the second commit
		w.git(t, "reset", "-q", "--hard", first)
		if first == prHead {
			t.Fatal("fixture premise broken: HEAD must be a strict ancestor of the PR head")
		}
		ageWorktree(t, w, twoHours)

		c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, prHead))))
		wantVerdict(t, c, VerdictFinished, "")
	})
	t.Run("without the PR head the squash-merged branch is not pushed", func(t *testing.T) {
		// Control: the generic pushed rule cannot see this commit on the remote
		// (the squash rewrote it), so the verdicts above come from the PR head.
		r := newOriginRepo(t)
		w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
		squashMerge(t, r, w)
		ageWorktree(t, w, twoHours)

		c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRClosedUnmerged))))
		wantVerdict(t, c, VerdictKeep, KeepUnpushed)
	})
}

func TestClassifyMergedPRHeadBeyondPRHeadIsUnpushed(t *testing.T) {
	r := newRepo(t, "repo")
	w := unpushedWorktree(t, r, ticket42Name, ticket42Branch)
	prHead := w.head(t)
	w.commit(t, "beyond.txt", "beyond\n", oldCommitDate)
	beyond := w.head(t)
	ageWorktree(t, w, twoHours)

	c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, prHead))))
	wantVerdict(t, c, VerdictKeep, KeepUnpushed)

	// Control: the same worktree is finished once the PR head covers HEAD.
	c = classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, beyond))))
	wantVerdict(t, c, VerdictFinished, "")
}

func TestClassifyMergedPRHeadNotPresentLocallyIsUnpushed(t *testing.T) {
	// The branch IS pushed and clean, so the generic pushed rule would accept
	// it: the merged-PR rule must not fall back to it.
	r := newOriginRepo(t)
	w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	head := w.head(t)
	ageWorktree(t, w, twoHours)

	tests := []struct {
		name  string
		heads []string
	}{
		{"head absent from the local object store", []string{strings.Repeat("a", 40)}},
		{"no head reported", nil},
		{"head is not a hex object name", []string{"--output=" + filepath.Join(r.Tmp, "injected")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, tc.heads...))))
			wantVerdict(t, c, VerdictKeep, KeepUnpushed)
		})
	}
	if pathExists(filepath.Join(r.Tmp, "injected")) {
		t.Fatal("a malformed PR head reached git as an option")
	}

	// Control: with the real head the same worktree is finished.
	c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, head))))
	wantVerdict(t, c, VerdictFinished, "")
}

func TestClassifyFinishedGraceBoundary(t *testing.T) {
	r := newRepo(t, "repo")
	w := unpushedWorktree(t, r, ticket42Name, ticket42Branch)
	o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, w.head(t))))

	tests := []struct {
		age  time.Duration
		want Verdict
		why  KeepReason
	}{
		{recentActivityGrace - time.Minute, VerdictKeep, KeepRecentlyActive},
		{recentActivityGrace - time.Second, VerdictKeep, KeepRecentlyActive},
		{recentActivityGrace, VerdictFinished, ""},
		{recentActivityGrace + time.Minute, VerdictFinished, ""},
	}
	for _, tc := range tests {
		t.Run(tc.age.String(), func(t *testing.T) {
			ageWorktree(t, w, tc.age)
			wantVerdict(t, classifyOne(t, r, w, o), tc.want, tc.why)
		})
	}
}

func TestClassifyFinishedInsideGraceStillGoesThroughTheStalePath(t *testing.T) {
	// staleAfter below the 1h grace: inside the grace the finished path keeps
	// the worktree, but the stale path (branch kept) still removes it.
	r := newOriginRepo(t)
	w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	ageWorktree(t, w, 45*time.Minute)
	o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, w.head(t))))
	o.StaleAfter = 30 * time.Minute

	wantVerdict(t, classifyOne(t, r, w, o), VerdictStale, "")
}

// --- AC2: closed unmerged / issue closed ----------------------------------

func TestClassifyClosedUnmergedKeepsWithReason(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, r *fixtureRepo) *fixtureWT
		want  Verdict
		why   KeepReason
	}{
		{"clean and pushed is finished (control)", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			return publishedWorktree(t, r, ticket42Name, ticket42Branch)
		}, VerdictFinished, ""},
		{"modified tracked file", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			writeFile(t, filepath.Join(w.Path, "tracked.txt"), "modified\n")
			return w
		}, VerdictKeep, KeepDirty},
		{"staged new file", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			writeFile(t, filepath.Join(w.Path, "staged.txt"), "staged\n")
			w.git(t, "add", "staged.txt")
			return w
		}, VerdictKeep, KeepDirty},
		{"untracked non-ignored file", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			writeFile(t, filepath.Join(w.Path, "untracked.txt"), "untracked\n")
			return w
		}, VerdictKeep, KeepUntracked},
		{"ignored file only (control)", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			writeFile(t, filepath.Join(w.Path, "ignored", "cache.bin"), "cache\n")
			writeFile(t, filepath.Join(w.Path, "debug.log"), "log\n")
			return w
		}, VerdictFinished, ""},
		{"commit ahead of upstream", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			w.commit(t, "ahead.txt", "ahead\n", oldCommitDate)
			return w
		}, VerdictKeep, KeepUnpushed},
		{"no upstream and unreachable from the remote", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			return unpushedWorktree(t, r, ticket42Name, ticket42Branch)
		}, VerdictKeep, KeepUnpushed},
		{"upstream gone and unreachable from the remote", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			deleteRemoteBranch(t, r, w.Branch)
			return w
		}, VerdictKeep, KeepUnpushed},
		{"no upstream but reachable from the remote (control)", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			return atMainWorktree(t, r, ticket42Name, ticket42Branch)
		}, VerdictFinished, ""},
	}
	for _, out := range []ticketstate.Outcome{ticketstate.OutcomePRClosedUnmerged, ticketstate.OutcomeIssueClosed} {
		for _, tc := range cases {
			t.Run(out.String()+"/"+tc.name, func(t *testing.T) {
				r := newOriginRepo(t)
				w := tc.setup(t, r)
				ageWorktree(t, w, twoHours)
				c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(out))))
				wantVerdict(t, c, tc.want, tc.why)
			})
		}
	}
}

// --- AC3/AC4: stale path --------------------------------------------------

func TestClassifyStaleBoundary(t *testing.T) {
	r := newOriginRepo(t)
	w := atMainWorktree(t, r, "scratch", "scratch")
	o := testOptions(nil)

	tests := []struct {
		name string
		age  time.Duration
		want Verdict
		why  KeepReason
	}{
		{"one second inside staleAfter", testStaleAfter - time.Second, VerdictKeep, KeepActive},
		{"exactly staleAfter is not stale", testStaleAfter, VerdictKeep, KeepActive},
		{"one second past staleAfter", testStaleAfter + time.Second, VerdictStale, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ageWorktree(t, w, tc.age)
			wantVerdict(t, classifyOne(t, r, w, o), tc.want, tc.why)
		})
	}
}

func TestClassifyStaleNonTicketDirIsEligible(t *testing.T) {
	r := newOriginRepo(t)
	w := atMainWorktree(t, r, "scratch", "scratch")
	ageWorktree(t, w, thirtyDays)

	wantVerdict(t, classifyOne(t, r, w, testOptions(nil)), VerdictStale, "")
}

func TestClassifyStaleKeepsWithReason(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, r *fixtureRepo) *fixtureWT
		want  Verdict
		why   KeepReason
	}{
		{"ignored file only is removable (control)", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, "scratch", "scratch")
			writeFile(t, filepath.Join(w.Path, "ignored", "cache.bin"), "cache\n")
			writeFile(t, filepath.Join(w.Path, "debug.log"), "log\n")
			return w
		}, VerdictStale, ""},
		{"upstream gone but reachable from the remote is removable (control)", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, "scratch", "scratch")
			mergeIntoRemoteMain(t, r, w)
			deleteRemoteBranch(t, r, w.Branch)
			return w
		}, VerdictStale, ""},
		{"modified tracked file", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, "scratch", "scratch")
			writeFile(t, filepath.Join(w.Path, "tracked.txt"), "modified\n")
			return w
		}, VerdictKeep, KeepDirty},
		{"staged new file", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, "scratch", "scratch")
			writeFile(t, filepath.Join(w.Path, "staged.txt"), "staged\n")
			w.git(t, "add", "staged.txt")
			return w
		}, VerdictKeep, KeepDirty},
		{"untracked non-ignored file", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, "scratch", "scratch")
			writeFile(t, filepath.Join(w.Path, "untracked.txt"), "untracked\n")
			return w
		}, VerdictKeep, KeepUntracked},
		{"ahead of upstream", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, "scratch", "scratch")
			w.commit(t, "ahead.txt", "ahead\n", oldCommitDate)
			return w
		}, VerdictKeep, KeepUnpushed},
		{"no upstream and unreachable from the remote", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			return unpushedWorktree(t, r, "scratch", "scratch")
		}, VerdictKeep, KeepUnpushed},
		{"upstream gone and unreachable from the remote", func(t *testing.T, r *fixtureRepo) *fixtureWT {
			w := publishedWorktree(t, r, "scratch", "scratch")
			deleteRemoteBranch(t, r, w.Branch)
			return w
		}, VerdictKeep, KeepUnpushed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newOriginRepo(t)
			w := tc.setup(t, r)
			ageWorktree(t, w, thirtyDays)
			wantVerdict(t, classifyOne(t, r, w, testOptions(nil)), tc.want, tc.why)
		})
	}
}

// --- AC5: recently active -------------------------------------------------

func TestClassifyFinishedRecentlyActiveIsNotRemoved(t *testing.T) {
	recent := fixedNow.Add(-10 * time.Minute)
	cases := []struct {
		name string
		// touch makes one source of activity recent, after the worktree was aged.
		touch func(t *testing.T, w *fixtureWT)
	}{
		{"a worktree file was touched", func(t *testing.T, w *fixtureWT) {
			setMtime(t, filepath.Join(w.Path, "work.txt"), recent)
		}},
		{"the admin index changed", func(t *testing.T, w *fixtureWT) {
			setMtime(t, filepath.Join(w.AdminDir, "index"), recent)
		}},
		{"the HEAD reflog changed", func(t *testing.T, w *fixtureWT) {
			setMtime(t, filepath.Join(w.AdminDir, "logs", "HEAD"), recent)
		}},
		{"a file is future-dated", func(t *testing.T, w *fixtureWT) {
			setMtime(t, filepath.Join(w.Path, "work.txt"), fixedNow.Add(24*time.Hour))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "repo")
			w := unpushedWorktree(t, r, ticket42Name, ticket42Branch)
			o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, w.head(t))))
			ageWorktree(t, w, twoHours)
			// Control: aged like this the worktree is finished, so the touch below
			// is the only thing that can keep it.
			wantVerdict(t, classifyOne(t, r, w, o), VerdictFinished, "")

			tc.touch(t, w)
			c := classifyOne(t, r, w, o)
			wantVerdict(t, c, VerdictKeep, KeepRecentlyActive)

			rem := Reclaim(context.Background(), r.Repo(), []Classification{c}, o)
			if len(rem) != 1 || rem[0].Removed {
				t.Fatalf("Reclaim of a recently active worktree = %+v, want nothing removed", rem)
			}
			if !pathExists(w.Path) || !pathExists(w.AdminDir) || !branchExists(r, w.Branch) {
				t.Fatal("a recently active worktree lost its directory, admin dir or branch")
			}
		})
	}

	t.Run("a recent commit date", func(t *testing.T) {
		r := newRepo(t, "repo")
		w := addWorktree(t, r, ticket42Name, ticket42Branch)
		runGitAt(t, w.Path, recent.Unix(), "commit", "-q", "--allow-empty", "-m", "recent")
		o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, w.head(t))))
		ageWorktree(t, w, twoHours) // files are old; only the commit date is recent

		wantVerdict(t, classifyOne(t, r, w, o), VerdictKeep, KeepRecentlyActive)
	})
}

// --- AC6: non-ticket and detached -----------------------------------------

func TestClassifyNonTicketDirNeverFinished(t *testing.T) {
	names := []string{"scratch", "12abc", "0-x", "2147483648-x", "x-42", "-42-x"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			r := newOriginRepo(t)
			w := atMainWorktree(t, r, name, "b/"+strings.TrimPrefix(name, "-"))
			head := w.head(t)
			// Whatever number the name might be mistaken for, a merged outcome with
			// the exact HEAD is on offer: only the name can stop the finished path.
			outcomes := map[int]ticketstate.OutcomeResult{
				0: outcome(ticketstate.OutcomePRMerged, head), 12: outcome(ticketstate.OutcomePRMerged, head),
				42: outcome(ticketstate.OutcomePRMerged, head), 2147483648: outcome(ticketstate.OutcomePRMerged, head),
			}
			o := testOptions(outcomes)

			ageWorktree(t, w, twoHours)
			wantVerdict(t, classifyOne(t, r, w, o), VerdictKeep, KeepActive)

			ageWorktree(t, w, thirtyDays)
			wantVerdict(t, classifyOne(t, r, w, o), VerdictStale, "")
		})
	}

	t.Run("a ticket dir is finished (control)", func(t *testing.T) {
		r := newOriginRepo(t)
		w := atMainWorktree(t, r, ticket42Name, ticket42Branch)
		o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, w.head(t))))
		ageWorktree(t, w, twoHours)
		wantVerdict(t, classifyOne(t, r, w, o), VerdictFinished, "")
	})
}

func TestClassifyDetachedHeadNeverFinished(t *testing.T) {
	for _, out := range []ticketstate.Outcome{ticketstate.OutcomePRMerged, ticketstate.OutcomePRClosedUnmerged, ticketstate.OutcomeIssueClosed} {
		t.Run(out.String(), func(t *testing.T) {
			r := newOriginRepo(t)
			w := addWorktree(t, r, ticket42Name, "") // detached at main's HEAD, pushed
			o := testOptions(outcomesFor(42, outcome(out, w.head(t))))

			ageWorktree(t, w, twoHours)
			wantVerdict(t, classifyOne(t, r, w, o), VerdictKeep, KeepActive)

			// The stale path is open to a detached HEAD.
			ageWorktree(t, w, thirtyDays)
			wantVerdict(t, classifyOne(t, r, w, o), VerdictStale, "")
		})
	}
}

func TestClassifyDetachedStalePushedOnlyIfReachableFromRemote(t *testing.T) {
	t.Run("detached at a commit no remote ref contains", func(t *testing.T) {
		r := newOriginRepo(t)
		w := addWorktree(t, r, "scratch", "scratch")
		w.commit(t, "local.txt", "local\n", oldCommitDate)
		w.git(t, "checkout", "-q", "--detach")
		ageWorktree(t, w, thirtyDays)
		wantVerdict(t, classifyOne(t, r, w, testOptions(nil)), VerdictKeep, KeepUnpushed)
	})
	t.Run("detached at main's pushed commit", func(t *testing.T) {
		r := newOriginRepo(t)
		w := addWorktree(t, r, "scratch", "")
		ageWorktree(t, w, thirtyDays)
		wantVerdict(t, classifyOne(t, r, w, testOptions(nil)), VerdictStale, "")
	})
	t.Run("detached at a commit a remote branch contains", func(t *testing.T) {
		r := newOriginRepo(t)
		w := addWorktree(t, r, "scratch", "scratch")
		w.commit(t, "side.txt", "side\n", oldCommitDate)
		w.git(t, "push", "-q", "origin", "HEAD:refs/heads/side")
		w.git(t, "checkout", "-q", "--detach")
		if out := r.git(t, "branch", "-r", "--contains", w.head(t)); out == "" {
			t.Fatal("fixture premise broken: no remote-tracking ref contains HEAD")
		}
		ageWorktree(t, w, thirtyDays)
		wantVerdict(t, classifyOne(t, r, w, testOptions(nil)), VerdictStale, "")
	})
	t.Run("without any remote nothing is reachable", func(t *testing.T) {
		r := newRepo(t, "repo")
		w := addWorktree(t, r, "scratch", "")
		ageWorktree(t, w, thirtyDays)
		wantVerdict(t, classifyOne(t, r, w, testOptions(nil)), VerdictKeep, KeepUnpushed)
	})
}

// --- AC7 (classification side): locked ------------------------------------

func TestClassifyLockedWorktreeKept(t *testing.T) {
	t.Run("finished-eligible", func(t *testing.T) {
		r := newOriginRepo(t)
		w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
		o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRClosedUnmerged)))
		r.git(t, "worktree", "lock", w.Path)
		ageWorktree(t, w, twoHours)
		wantVerdict(t, classifyOne(t, r, w, o), VerdictKeep, KeepLocked)
	})
	t.Run("stale-eligible", func(t *testing.T) {
		r := newOriginRepo(t)
		w := atMainWorktree(t, r, "scratch", "scratch")
		r.git(t, "worktree", "lock", w.Path)
		ageWorktree(t, w, thirtyDays)
		wantVerdict(t, classifyOne(t, r, w, testOptions(nil)), VerdictKeep, KeepLocked)
	})
	t.Run("unlocking makes it eligible (control)", func(t *testing.T) {
		r := newOriginRepo(t)
		w := atMainWorktree(t, r, "scratch", "scratch")
		r.git(t, "worktree", "lock", w.Path)
		r.git(t, "worktree", "unlock", w.Path)
		ageWorktree(t, w, thirtyDays)
		wantVerdict(t, classifyOne(t, r, w, testOptions(nil)), VerdictStale, "")
	})
}

// --- AC8: pipeline guard and Unknown outcomes through Classify ------------

func TestClassifyMidStagePipelineBlocksBothPaths(t *testing.T) {
	type pathCase struct {
		name  string
		age   time.Duration
		build func(t *testing.T, r *fixtureRepo) (*fixtureWT, Options)
		clear Verdict // the verdict once the pipeline guard is not in the way
	}
	paths := []pathCase{
		{"finished path", twoHours, func(t *testing.T, r *fixtureRepo) (*fixtureWT, Options) {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			return w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRClosedUnmerged)))
		}, VerdictFinished},
		{"stale path", thirtyDays, func(t *testing.T, r *fixtureRepo) (*fixtureWT, Options) {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			return w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomeOpen)))
		}, VerdictStale},
	}
	states := []struct {
		name  string
		write func(t *testing.T, r *fixtureRepo)
		want  Verdict
		why   KeepReason
	}{
		{"no state file (control)", func(*testing.T, *fixtureRepo) {}, "", ""},
		{"recent executed", func(t *testing.T, r *fixtureRepo) {
			writePipelineState(t, r, "42", "executed", fixedNow.Add(-time.Hour))
		}, VerdictKeep, KeepPipelineMidStage},
		{"executed within staleAfter but older than a day", func(t *testing.T, r *fixtureRepo) {
			writePipelineState(t, r, "42", "executed", fixedNow.Add(-3*24*time.Hour))
		}, VerdictKeep, KeepPipelineMidStage},
		{"future-dated executed", func(t *testing.T, r *fixtureRepo) {
			writePipelineState(t, r, "42", "reviewed", fixedNow.Add(24*time.Hour))
		}, VerdictKeep, KeepPipelineMidStage},
		{"finalized", func(t *testing.T, r *fixtureRepo) {
			writePipelineState(t, r, "42", "finalized", fixedNow.Add(-time.Hour))
		}, "", ""},
		{"executed but older than staleAfter", func(t *testing.T, r *fixtureRepo) {
			writePipelineState(t, r, "42", "executed", fixedNow.Add(-testStaleAfter-time.Hour))
		}, "", ""},
		{"malformed state file", func(t *testing.T, r *fixtureRepo) {
			writePipelineRaw(t, r.Root, "42", `{"stage":`)
		}, VerdictKeep, KeepPipelineUnreadable},
		{"state file without a stage", func(t *testing.T, r *fixtureRepo) {
			writePipelineRaw(t, r.Root, "42", `{"updatedAt":"2050-06-15T11:00:00Z"}`)
		}, VerdictKeep, KeepPipelineUnreadable},
		{"another ticket's state does not matter", func(t *testing.T, r *fixtureRepo) {
			writePipelineState(t, r, "43", "executed", fixedNow)
		}, "", ""},
	}
	for _, p := range paths {
		for _, s := range states {
			t.Run(p.name+"/"+s.name, func(t *testing.T) {
				r := newOriginRepo(t)
				w, o := p.build(t, r)
				s.write(t, r)
				ageWorktree(t, w, p.age)
				c := classifyOne(t, r, w, o)
				if s.want == "" {
					wantVerdict(t, c, p.clear, "")
					return
				}
				wantVerdict(t, c, s.want, s.why)
			})
		}
	}
}

func TestClassifyPipelineIDIsTheDirPrefixAsWritten(t *testing.T) {
	// A zero-padded dir name is ticket 7 for the outcome map, but its pipeline
	// state lives under the id exactly as written ("007"), matching how the
	// worktree was named from the id.
	r := newOriginRepo(t)
	w := publishedWorktree(t, r, "007-padded", "feature/007-padded")
	o := testOptions(outcomesFor(7, outcome(ticketstate.OutcomePRClosedUnmerged)))
	ageWorktree(t, w, twoHours)

	writePipelineState(t, r, "7", "executed", fixedNow)
	wantVerdict(t, classifyOne(t, r, w, o), VerdictFinished, "") // "7.json" is another ticket's file

	writePipelineState(t, r, "007", "executed", fixedNow)
	wantVerdict(t, classifyOne(t, r, w, o), VerdictKeep, KeepPipelineMidStage)
}

func TestClassifyUnknownOutcomeNeverFinishedButStaleEvaluated(t *testing.T) {
	unknowns := []struct {
		name string
		outs map[int]ticketstate.OutcomeResult
	}{
		{"Unknown with an error", outcomesFor(42, ticketstate.OutcomeResult{Outcome: ticketstate.OutcomeUnknown, Err: errors.New("gh api failed")})},
		{"ticket missing from the map", map[int]ticketstate.OutcomeResult{43: outcome(ticketstate.OutcomePRMerged)}},
		{"nil map", nil},
	}
	for _, u := range unknowns {
		t.Run(u.name, func(t *testing.T) {
			r := newOriginRepo(t)
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			o := testOptions(u.outs)

			ageWorktree(t, w, twoHours)
			wantVerdict(t, classifyOne(t, r, w, o), VerdictKeep, KeepOutcomeUnknown)

			ageWorktree(t, w, thirtyDays)
			wantVerdict(t, classifyOne(t, r, w, o), VerdictStale, "")
		})
	}

	t.Run("a removable outcome that carries an error is not trusted", func(t *testing.T) {
		r := newOriginRepo(t)
		w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
		ageWorktree(t, w, twoHours)
		res := ticketstate.OutcomeResult{Outcome: ticketstate.OutcomePRMerged, Heads: []string{w.head(t)}}
		// Control: without the error the same worktree is finished.
		wantVerdict(t, classifyOne(t, r, w, testOptions(outcomesFor(42, res))), VerdictFinished, "")

		res.Err = errors.New("partial read")
		c := classifyOne(t, r, w, testOptions(outcomesFor(42, res)))
		if c.Verdict == VerdictFinished {
			t.Fatalf("an outcome with Err set produced a finished verdict: %+v", c)
		}
	})
}

func TestClassifyOpenOutcomeIsActive(t *testing.T) {
	r := newOriginRepo(t)
	w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomeOpen)))

	ageWorktree(t, w, twoHours)
	wantVerdict(t, classifyOne(t, r, w, o), VerdictKeep, KeepActive)

	// An open ticket's worktree can still go stale.
	ageWorktree(t, w, thirtyDays)
	wantVerdict(t, classifyOne(t, r, w, o), VerdictStale, "")
}

// --- Guards / options -----------------------------------------------------

func TestClassifyInjectedGuardsBlockRemoval(t *testing.T) {
	guardErr := errors.New("tmux unreachable")
	blocking := func(context.Context, Target) (bool, error) { return true, nil }
	failing := func(context.Context, Target) (bool, error) { return false, guardErr }

	type setup struct {
		name   string
		build  func(t *testing.T, r *fixtureRepo) (*fixtureWT, Options)
		age    time.Duration
		target func(w *fixtureWT) Target
		clear  Verdict
	}
	setups := []setup{
		{"finished ticket", func(t *testing.T, r *fixtureRepo) (*fixtureWT, Options) {
			w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
			return w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRClosedUnmerged)))
		}, twoHours, func(w *fixtureWT) Target { return Target{Ticket: 42, Name: ticket42Name, LocalPath: w.Path} }, VerdictFinished},
		{"stale non-ticket dir", func(t *testing.T, r *fixtureRepo) (*fixtureWT, Options) {
			return atMainWorktree(t, r, "scratch", "scratch"), testOptions(nil)
		}, thirtyDays, func(w *fixtureWT) Target { return Target{Ticket: 0, Name: "scratch", LocalPath: w.Path} }, VerdictStale},
	}
	guards := []struct {
		name string
		set  func(o *Options, g Guard)
		fn   Guard
		why  KeepReason
	}{
		{"in-use blocks", func(o *Options, g Guard) { o.InUse = g }, blocking, KeepInUse},
		{"in-use error", func(o *Options, g Guard) { o.InUse = g }, failing, KeepInUseUnknown},
		{"babysit blocks", func(o *Options, g Guard) { o.Babysit = g }, blocking, KeepBabysit},
		{"babysit error", func(o *Options, g Guard) { o.Babysit = g }, failing, KeepBabysitUnknown},
	}
	for _, s := range setups {
		for _, g := range guards {
			t.Run(s.name+"/"+g.name, func(t *testing.T) {
				r := newOriginRepo(t)
				w, o := s.build(t, r)
				ageWorktree(t, w, s.age)
				wantVerdict(t, classifyOne(t, r, w, o), s.clear, "") // control: pass-through guards

				var seen []Target
				record := func(ctx context.Context, tg Target) (bool, error) {
					seen = append(seen, tg)
					return g.fn(ctx, tg)
				}
				g.set(&o, record)
				c := classifyOne(t, r, w, o)
				wantVerdict(t, c, VerdictKeep, g.why)
				want := s.target(w)
				if len(seen) == 0 {
					t.Fatal("the injected guard was never consulted for a removable worktree")
				}
				for _, got := range seen {
					if got != want {
						t.Errorf("guard received %+v, want %+v", got, want)
					}
				}
			})
		}
	}

	t.Run("a guard error is carried on the classification", func(t *testing.T) {
		r := newOriginRepo(t)
		w := atMainWorktree(t, r, "scratch", "scratch")
		ageWorktree(t, w, thirtyDays)
		o := testOptions(nil)
		o.InUse = failing
		c := classifyOne(t, r, w, o)
		if !errors.Is(c.Err, guardErr) {
			t.Fatalf("Classification.Err = %v, want it to wrap the guard error", c.Err)
		}
	})
}

func TestClassifyInvalidOptionsKeepsEverything(t *testing.T) {
	r := newOriginRepo(t)
	finished := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	stale := atMainWorktree(t, r, "scratch", "scratch")
	ageWorktree(t, finished, twoHours)
	ageWorktree(t, stale, thirtyDays)
	valid := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRClosedUnmerged)))

	// Control: with valid options both are removable.
	wantVerdict(t, classifyOne(t, r, finished, valid), VerdictFinished, "")
	wantVerdict(t, classifyOne(t, r, stale, valid), VerdictStale, "")

	invalid := []struct {
		name   string
		mutate func(o *Options)
	}{
		{"nil InUse guard", func(o *Options) { o.InUse = nil }},
		{"nil Babysit guard", func(o *Options) { o.Babysit = nil }},
		{"zero StaleAfter", func(o *Options) { o.StaleAfter = 0 }},
		{"negative StaleAfter", func(o *Options) { o.StaleAfter = -time.Hour }},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			o := valid
			tc.mutate(&o)
			wantVerdict(t, classifyOne(t, r, finished, o), VerdictKeep, KeepInvalidOptions)
			wantVerdict(t, classifyOne(t, r, stale, o), VerdictKeep, KeepInvalidOptions)
		})
	}
}

func TestClassifyNilNowUsesTheWallClock(t *testing.T) {
	r := newOriginRepo(t)
	w := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	ageAll(t, time.Now().Add(-twoHours), w.Path, w.AdminDir)
	o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRClosedUnmerged)))
	o.Now = nil

	wantVerdict(t, classifyOne(t, r, w, o), VerdictFinished, "")
}

func TestClassifyInspectFailureKeeps(t *testing.T) {
	t.Run("a configured clean filter makes git status unsafe", func(t *testing.T) {
		r := newOriginRepo(t)
		w := atMainWorktree(t, r, "scratch", "scratch")
		r.git(t, "config", "filter.evil.clean", "cat")
		ageWorktree(t, w, thirtyDays)
		c := classifyOne(t, r, w, testOptions(nil))
		wantVerdict(t, c, VerdictKeep, KeepGitError)
		if c.Err == nil {
			t.Fatal("a git-error keep must carry the underlying error")
		}
	})
	t.Run("an entry whose worktree dir vanished is unmapped", func(t *testing.T) {
		r := newOriginRepo(t)
		w := atMainWorktree(t, r, "scratch", "scratch")
		ageWorktree(t, w, thirtyDays)
		e := entryFor(t, r, w)
		if err := os.RemoveAll(w.Path); err != nil {
			t.Fatal(err)
		}
		wantVerdict(t, Classify(context.Background(), r.Repo(), e, testOptions(nil)), VerdictKeep, KeepUnmapped)
	})
	t.Run("an entry with a forged admin name is unmapped", func(t *testing.T) {
		r := newOriginRepo(t)
		for _, name := range []string{"..", "", "../x", "a/b"} {
			c := Classify(context.Background(), r.Repo(), Entry{AdminName: name}, testOptions(nil))
			wantVerdict(t, c, VerdictKeep, KeepUnmapped)
		}
	})
	t.Run("a cancelled context is a git error, not an unmapped entry", func(t *testing.T) {
		r := newOriginRepo(t)
		w := atMainWorktree(t, r, "scratch", "scratch")
		ageWorktree(t, w, thirtyDays)
		e := entryFor(t, r, w)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		wantVerdict(t, Classify(ctx, r.Repo(), e, testOptions(nil)), VerdictKeep, KeepGitError)
	})
}

// --- ClassifyAll / TicketNumbers -------------------------------------------

func TestClassifyAllClassifiesEveryEntryInOrder(t *testing.T) {
	r := newOriginRepo(t)
	finished := publishedWorktree(t, r, ticket42Name, ticket42Branch)
	stale := atMainWorktree(t, r, "scratch", "scratch")
	dirty := publishedWorktree(t, r, "43-dirty", "feature/43-dirty")
	writeFile(t, filepath.Join(dirty.Path, "tracked.txt"), "modified\n")
	for _, w := range []*fixtureWT{finished, stale, dirty} {
		ageWorktree(t, w, thirtyDays)
	}
	ageWorktree(t, finished, twoHours)
	o := testOptions(map[int]ticketstate.OutcomeResult{
		42: outcome(ticketstate.OutcomePRClosedUnmerged), 43: outcome(ticketstate.OutcomeOpen),
	})

	_, entries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	cs := ClassifyAll(context.Background(), r.Repo(), entries, o)
	if len(cs) != len(entries) {
		t.Fatalf("ClassifyAll returned %d classifications for %d entries", len(cs), len(entries))
	}
	for i := range entries {
		if cs[i].Entry.AdminName != entries[i].AdminName {
			t.Fatalf("classification %d is for %q, want %q (input order)", i, cs[i].Entry.AdminName, entries[i].AdminName)
		}
	}
	wantVerdict(t, classificationFor(t, cs, finished), VerdictFinished, "")
	wantVerdict(t, classificationFor(t, cs, stale), VerdictStale, "")
	wantVerdict(t, classificationFor(t, cs, dirty), VerdictKeep, KeepDirty)
}

func TestTicketNumbers(t *testing.T) {
	names := []string{"42-a", "7-b", "scratch", "0-x", "42-c", "12abc", "007-pad", "2147483648-big", "2147483647-max"}
	var entries []Entry
	for _, n := range names {
		entries = append(entries, Entry{Name: n})
	}
	got := TicketNumbers(entries)
	want := []int{7, 42, 2147483647}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TicketNumbers(%v) = %v, want %v (sorted, unique, 1..MaxInt32 only)", names, got, want)
	}
	if got := TicketNumbers(nil); len(got) != 0 {
		t.Fatalf("TicketNumbers(nil) = %v, want none", got)
	}
}

// --- git failures are errors, never a quiet "no" ----------------------------

func TestClassifyMergedPRGitFailureIsGitErrorNotUnpushed(t *testing.T) {
	// The commit exists only locally, so the merged-PR rule is the only thing
	// that can make it count as pushed.
	tests := []struct {
		name    string
		pattern string // which git call the shim fails
	}{
		{"merge-base --is-ancestor", "merge-base"},
		{"rev-parse --verify of the PR head", "*'^{commit}'"},
	}
	for _, tc := range tests {
		r := newRepo(t, "repo")
		w := unpushedWorktree(t, r, ticket42Name, ticket42Branch)
		head := w.head(t)
		ageWorktree(t, w, twoHours)
		o := testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, head)))

		t.Run(tc.name+" exiting 128 keeps with git-error", func(t *testing.T) {
			installFailingGit(t, tc.pattern, 128)
			c := classifyOne(t, r, w, o)
			wantVerdict(t, c, VerdictKeep, KeepGitError)
			if c.Err == nil || !strings.Contains(c.Err.Error(), "simulated git failure") {
				t.Errorf("Err = %v, want the git failure", c.Err)
			}
		})
		t.Run(tc.name+" exiting 1 is the clean no", func(t *testing.T) {
			installFailingGit(t, tc.pattern, 1)
			c := classifyOne(t, r, w, o)
			wantVerdict(t, c, VerdictKeep, KeepUnpushed)
			if c.Err != nil {
				t.Errorf("Err = %v, want none for a clean no", c.Err)
			}
		})
	}
}

// coveredByMergedHead's contract: a covering head wins over an earlier head
// whose check failed, but a failure with no covering head is an error.
func TestClassifyMergedPRHeadCheckFailingEarlierHead(t *testing.T) {
	r := newRepo(t, "repo")
	w := unpushedWorktree(t, r, ticket42Name, ticket42Branch)
	head := w.head(t)
	unrelated := r.git(t, "rev-parse", "main") // present locally, but HEAD is not its ancestor
	ageWorktree(t, w, twoHours)
	broken := strings.Repeat("b", 40)
	installFailingGit(t, "'"+broken+"^{commit}'", 128)

	t.Run("a later covering head wins", func(t *testing.T) {
		c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, broken, head))))
		wantVerdict(t, c, VerdictFinished, "")
	})
	t.Run("no covering head reports the failure", func(t *testing.T) {
		c := classifyOne(t, r, w, testOptions(outcomesFor(42, outcome(ticketstate.OutcomePRMerged, broken, unrelated))))
		wantVerdict(t, c, VerdictKeep, KeepGitError)
		if c.Err == nil || !strings.Contains(c.Err.Error(), "simulated git failure") {
			t.Errorf("Err = %v, want the git failure", c.Err)
		}
	})
}
