package worktreeclean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every Inspect scenario below runs twice, on a worktree registered at its real
// location ("local") and on one whose recorded paths were rewritten to the other
// side of the sandbox mount ("translated"), because the acceptance criteria
// require the same facts for both.

// scn is one fixture repo plus which flavor the worktrees under test take.
type scn struct {
	r         *fixtureRepo
	translate bool
}

func forEachVariant(t *testing.T, fn func(t *testing.T, s *scn)) {
	t.Helper()
	for _, translate := range []bool{false, true} {
		name := "local"
		if translate {
			name = "translated"
		}
		t.Run(name, func(t *testing.T) {
			fn(t, &scn{r: newRepo(t, "hostrepo"), translate: translate})
		})
	}
}

func (s *scn) origin(t *testing.T) { t.Helper(); newBareOrigin(t, s.r) }

// finish applies the variant (rewriting the worktree's records for the
// translated flavor) and returns the Entry a real Enumerate yields, failing the
// test if it is not mapped the way the variant promises.
func (s *scn) finish(t *testing.T, w *fixtureWT) Entry {
	t.Helper()
	wantMapping := MappingLocal
	if s.translate {
		rewriteAsForeign(t, w, foreignRootFor(s.r, "workspace"))
		wantMapping = MappingTranslated
	}
	e := entryFor(t, s.r, w)
	if e.Mapping != wantMapping || e.Reason != "" || e.LocalPath != w.Path {
		t.Fatalf("entry = %+v, want %s and LocalPath %s", e, wantMapping, w.Path)
	}
	return e
}

func (s *scn) inspect(t *testing.T, e Entry) Facts {
	t.Helper()
	f, err := Inspect(context.Background(), s.r.Repo(), e)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return f
}

// --- status-derived facts, locked, reachability ---

type statusCase struct {
	name string
	// unborn marks a worktree with no commit yet, which has no activity data.
	unborn bool
	// setup builds the worktree on the local filesystem and returns the facts
	// Inspect must report (LastActivity and SizeBytes are checked separately).
	setup func(t *testing.T, s *scn) (*fixtureWT, Facts)
}

// tracking is the Facts of a worktree whose branch is pushed and has origin/<branch>
// as upstream, in sync with it.
func tracking(t *testing.T, w *fixtureWT) Facts {
	t.Helper()
	return Facts{
		Branch: w.Branch, HeadSHA: w.head(t), Upstream: "origin/" + w.Branch,
		AheadKnown: true, Ahead: 0, ReachableFromRemote: true,
	}
}

func pushedWorktree(t *testing.T, s *scn) *fixtureWT {
	t.Helper()
	s.origin(t)
	w := addWorktree(t, s.r, "1-x", "feature/1-x")
	w.push(t)
	return w
}

func statusCases() []statusCase {
	return []statusCase{
		{name: "clean with upstream is not ahead and reachable", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			return w, tracking(t, w)
		}},
		{name: "tracked modification is dirty", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			writeFile(t, filepath.Join(w.Path, "tracked.txt"), "changed\n")
			f := tracking(t, w)
			f.Dirty = true
			return w, f
		}},
		{name: "staged change is dirty", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			writeFile(t, filepath.Join(w.Path, "tracked.txt"), "staged\n")
			w.git(t, "add", "tracked.txt")
			f := tracking(t, w)
			f.Dirty = true
			return w, f
		}},
		{name: "deleted tracked file is dirty", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			if err := os.Remove(filepath.Join(w.Path, "tracked.txt")); err != nil {
				t.Fatal(err)
			}
			f := tracking(t, w)
			f.Dirty = true
			return w, f
		}},
		{name: "touched tracked file with identical content is not dirty", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			setMtime(t, filepath.Join(w.Path, "tracked.txt"), time.Unix(1609459200, 0))
			return w, tracking(t, w)
		}},
		{name: "untracked non-ignored file", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			writeFile(t, filepath.Join(w.Path, "notes.txt"), "scratch\n")
			f := tracking(t, w)
			f.Untracked = true
			return w, f
		}},
		{name: "untracked file inside a new directory", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			writeFile(t, filepath.Join(w.Path, "newdir", "deep", "f.txt"), "x\n")
			f := tracking(t, w)
			f.Untracked = true
			return w, f
		}},
		{name: "ignored files alone are not untracked", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			writeFile(t, filepath.Join(w.Path, "ignored", "cache.bin"), "cache\n")
			writeFile(t, filepath.Join(w.Path, "debug.log"), "log\n")
			return w, tracking(t, w)
		}},
		{name: "modified and untracked together", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			writeFile(t, filepath.Join(w.Path, "tracked.txt"), "changed\n")
			writeFile(t, filepath.Join(w.Path, "notes.txt"), "scratch\n")
			f := tracking(t, w)
			f.Dirty, f.Untracked = true, true
			return w, f
		}},
		{name: "staged rename does not leak its source path as an untracked entry", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			// The renamed-from path rides along as an extra NUL-separated field
			// of the rename entry; named "? orig.txt" it would read as an
			// untracked entry to a parser that forgets to consume it.
			s.origin(t)
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			w.commit(t, "? orig.txt", "orig\n", oldCommitDate)
			w.push(t)
			w.git(t, "mv", "? orig.txt", "renamed.txt")
			f := tracking(t, w)
			f.Dirty = true
			return w, f
		}},
		{name: "staged rename plus a genuinely untracked file", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			s.origin(t)
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			w.commit(t, "? orig.txt", "orig\n", oldCommitDate)
			w.push(t)
			w.git(t, "mv", "? orig.txt", "renamed.txt")
			writeFile(t, filepath.Join(w.Path, "notes.txt"), "scratch\n")
			f := tracking(t, w)
			f.Dirty, f.Untracked = true, true
			return w, f
		}},
		{name: "locked worktree", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			s.r.git(t, "worktree", "lock", w.Path)
			f := tracking(t, w)
			f.Locked = true
			return w, f
		}},
		{name: "locked worktree with a reason", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			s.r.git(t, "worktree", "lock", "--reason", "in use", w.Path)
			f := tracking(t, w)
			f.Locked = true
			return w, f
		}},
		{name: "detached HEAD reachable from a remote-tracking ref", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			s.origin(t)
			w := addWorktree(t, s.r, "2-detached", "")
			return w, Facts{Detached: true, HeadSHA: w.head(t), ReachableFromRemote: true}
		}},
		{name: "detached HEAD not reachable from any remote-tracking ref", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			s.origin(t)
			w := addWorktree(t, s.r, "2-detached", "")
			w.commit(t, "local.txt", "local\n", oldCommitDate)
			return w, Facts{Detached: true, HeadSHA: w.head(t)}
		}},
		{name: "two commits ahead of upstream", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := pushedWorktree(t, s)
			w.commit(t, "a.txt", "a\n", oldCommitDate)
			w.commit(t, "b.txt", "b\n", oldCommitDate)
			f := tracking(t, w)
			f.Ahead, f.ReachableFromRemote = 2, false
			return w, f
		}},
		{name: "behind upstream is not ahead", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			s.origin(t)
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			w.commit(t, "a.txt", "a\n", oldCommitDate)
			w.push(t)
			w.git(t, "reset", "-q", "--hard", "HEAD~1")
			return w, tracking(t, w)
		}},
		{name: "no upstream and no new commits is still reachable via origin/main", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			s.origin(t)
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			return w, Facts{Branch: w.Branch, HeadSHA: w.head(t), ReachableFromRemote: true}
		}},
		{name: "no upstream with a local commit is not reachable", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			s.origin(t)
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			w.commit(t, "a.txt", "a\n", oldCommitDate)
			return w, Facts{Branch: w.Branch, HeadSHA: w.head(t)}
		}},
		{name: "upstream branch deleted on the remote and pruned", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			s.origin(t)
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			w.commit(t, "a.txt", "a\n", oldCommitDate)
			w.push(t)
			s.r.git(t, "push", "-q", "origin", "--delete", w.Branch)
			s.r.git(t, "fetch", "-q", "--prune", "origin")
			return w, Facts{Branch: w.Branch, HeadSHA: w.head(t), Upstream: "origin/" + w.Branch, UpstreamGone: true}
		}},
		{name: "no remote configured at all", setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			return w, Facts{Branch: w.Branch, HeadSHA: w.head(t)}
		}},
		{name: "unborn HEAD", unborn: true, setup: func(t *testing.T, s *scn) (*fixtureWT, Facts) {
			s.origin(t)
			w := addOrphanWorktree(t, s.r, "9-orphan", "orphan-branch")
			return w, Facts{Branch: "orphan-branch"}
		}},
	}
}

func TestInspectStatusFacts(t *testing.T) {
	for _, tc := range statusCases() {
		t.Run(tc.name, func(t *testing.T) {
			forEachVariant(t, func(t *testing.T, s *scn) {
				w, want := tc.setup(t, s)
				got := s.inspect(t, s.finish(t, w))

				if !tc.unborn && got.LastActivity.IsZero() {
					t.Error("LastActivity is zero, want a real time")
				}
				if got.SizeBytes <= 0 {
					t.Errorf("SizeBytes = %d, want > 0", got.SizeBytes)
				}
				got.LastActivity, got.SizeBytes = time.Time{}, 0
				if got != want {
					t.Errorf("Facts =\n%+v\nwant\n%+v", got, want)
				}
			})
		})
	}
}

// --- last activity ---

func TestInspectLastActivity(t *testing.T) {
	var (
		tOld       = time.Unix(1609459200, 0) // 2021-01-01: every mtime in the baseline
		tUntracked = time.Unix(1612137600, 0) // 2021-02-01
		tTracked   = time.Unix(1614556800, 0) // 2021-03-01
		tIndex     = time.Unix(1617235200, 0) // 2021-04-01
		tLog       = time.Unix(1619827200, 0) // 2021-05-01
		tIgnored   = time.Unix(1700000000, 0) // 2023-11: newer than everything it must not beat
		tCommit    = time.Unix(1654041600, 0) // 2022-06-01: committer date of a later commit
		tFuture    = time.Now().Add(48 * time.Hour).Truncate(time.Second)
	)
	cases := []struct {
		name  string
		want  time.Time
		prep  func(t *testing.T, w *fixtureWT) // before everything is aged to tOld
		touch func(t *testing.T, w *fixtureWT) // after aging
	}{
		{name: "everything old: the newest mtime", want: tOld},
		{name: "an untracked file's mtime", want: tUntracked,
			touch: func(t *testing.T, w *fixtureWT) {
				p := filepath.Join(w.Path, "notes.txt")
				writeFile(t, p, "x\n")
				setMtime(t, p, tUntracked)
			}},
		{name: "an untracked file inside an untracked directory", want: tUntracked,
			touch: func(t *testing.T, w *fixtureWT) {
				p := filepath.Join(w.Path, "newdir", "deep", "f.txt")
				writeFile(t, p, "x\n")
				setMtime(t, p, tUntracked)
			}},
		{name: "a modified tracked file's mtime", want: tTracked,
			touch: func(t *testing.T, w *fixtureWT) {
				p := filepath.Join(w.Path, "tracked.txt")
				writeFile(t, p, "changed\n")
				setMtime(t, p, tTracked)
			}},
		{name: "newer ignored files do not count", want: tOld,
			touch: func(t *testing.T, w *fixtureWT) {
				for _, rel := range []string{"ignored/cache.bin", "debug.log"} {
					p := filepath.Join(w.Path, rel)
					writeFile(t, p, "x\n")
					setMtime(t, p, tIgnored)
				}
			}},
		{name: "a later commit date wins over older file mtimes", want: tCommit,
			prep: func(t *testing.T, w *fixtureWT) {
				w.commit(t, "tracked.txt", "second\n", tCommit.Unix())
			}},
		{name: "a newer admin index mtime wins", want: tIndex,
			touch: func(t *testing.T, w *fixtureWT) { setMtime(t, filepath.Join(w.AdminDir, "index"), tIndex) }},
		{name: "a newer admin HEAD log mtime wins", want: tLog,
			touch: func(t *testing.T, w *fixtureWT) { setMtime(t, filepath.Join(w.AdminDir, "logs", "HEAD"), tLog) }},
		{name: "a future-dated mtime is not clamped", want: tFuture,
			touch: func(t *testing.T, w *fixtureWT) {
				p := filepath.Join(w.Path, "notes.txt")
				writeFile(t, p, "x\n")
				setMtime(t, p, tFuture)
			}},
		{name: "a deleted tracked file is skipped, not an error", want: tOld,
			touch: func(t *testing.T, w *fixtureWT) {
				if err := os.Remove(filepath.Join(w.Path, "tracked.txt")); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachVariant(t, func(t *testing.T, s *scn) {
				w := addWorktree(t, s.r, "1-x", "feature/1-x")
				if tc.prep != nil {
					tc.prep(t, w)
				}
				ageAll(t, tOld, w.Path, w.AdminDir)
				if tc.touch != nil {
					tc.touch(t, w)
				}

				got := s.inspect(t, s.finish(t, w))
				if !got.LastActivity.Equal(tc.want) {
					t.Errorf("LastActivity = %v, want exactly %v", got.LastActivity, tc.want)
				}
			})
		})
	}
}

// --- size on disk ---

func TestInspectSize(t *testing.T) {
	const (
		mib   = 1 << 20
		slack = 64 << 10 // directory/block rounding
	)
	// grows asserts Inspect's SizeBytes grew by [min, min+slack) after mutate.
	grows := func(t *testing.T, minDelta int64, mutate func(t *testing.T, s *scn, w *fixtureWT)) {
		t.Helper()
		forEachVariant(t, func(t *testing.T, s *scn) {
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			e := s.finish(t, w)
			before := s.inspect(t, e).SizeBytes
			if before <= 0 {
				t.Fatalf("baseline SizeBytes = %d, want > 0", before)
			}
			mutate(t, s, w)
			delta := s.inspect(t, e).SizeBytes - before
			if delta < minDelta || delta >= minDelta+slack {
				t.Errorf("SizeBytes grew by %d, want in [%d, %d)", delta, minDelta, minDelta+slack)
			}
		})
	}
	// stays asserts SizeBytes grew by less than slack after mutate.
	stays := func(t *testing.T, mutate func(t *testing.T, s *scn, w *fixtureWT)) {
		t.Helper()
		forEachVariant(t, func(t *testing.T, s *scn) {
			w := addWorktree(t, s.r, "1-x", "feature/1-x")
			e := s.finish(t, w)
			before := s.inspect(t, e).SizeBytes
			mutate(t, s, w)
			if delta := s.inspect(t, e).SizeBytes - before; delta >= slack {
				t.Errorf("SizeBytes grew by %d, want < %d", delta, slack)
			}
		})
	}

	t.Run("known file sizes count", func(t *testing.T) {
		grows(t, mib, func(t *testing.T, _ *scn, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, "data.bin"), strings.Repeat("x", mib))
		})
	})
	t.Run("ignored files count too", func(t *testing.T) {
		grows(t, mib/2, func(t *testing.T, _ *scn, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, "ignored", "cache.bin"), strings.Repeat("x", mib/2))
		})
	})
	t.Run("a symlink to a large outside file is not followed", func(t *testing.T) {
		stays(t, func(t *testing.T, s *scn, w *fixtureWT) {
			outside := filepath.Join(s.r.Tmp, "outside.bin")
			writeFile(t, outside, strings.Repeat("x", 8*mib))
			if err := os.Symlink(outside, filepath.Join(w.Path, "link.bin")); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("a symlink to an outside directory is not followed", func(t *testing.T) {
		stays(t, func(t *testing.T, s *scn, w *fixtureWT) {
			writeFile(t, filepath.Join(s.r.Tmp, "outside-dir", "big.bin"), strings.Repeat("x", 8*mib))
			if err := os.Symlink(filepath.Join(s.r.Tmp, "outside-dir"), filepath.Join(w.Path, "linkdir")); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("hard links to one inode count once", func(t *testing.T) {
		grows(t, mib, func(t *testing.T, _ *scn, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, "data.bin"), strings.Repeat("x", mib))
			if err := os.Link(filepath.Join(w.Path, "data.bin"), filepath.Join(w.Path, "data-copy.bin")); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("a sparse file counts allocated blocks, like du", func(t *testing.T) {
		stays(t, func(t *testing.T, _ *scn, w *fixtureWT) {
			p := filepath.Join(w.Path, "sparse.bin")
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(p, 64*mib); err != nil {
				t.Fatal(err)
			}
			var st syscall.Stat_t
			if err := syscall.Stat(p, &st); err != nil {
				t.Fatal(err)
			}
			if st.Blocks*512 >= mib {
				t.Skipf("filesystem allocated %d bytes for a sparse file", st.Blocks*512)
			}
		})
	})
}

// --- errors and refusals ---

func TestInspectRefusesAnEntryThatIsNotMappedNow(t *testing.T) {
	cases := []struct {
		name   string
		reason UnmappedReason
		// entry returns the Entry handed to Inspect, after mutating the fixture.
		entry func(t *testing.T, r *fixtureRepo, w *fixtureWT) Entry
	}{
		{"listed as unmapped (garbage gitdir)", ReasonGitdirInvalid, func(t *testing.T, r *fixtureRepo, w *fixtureWT) Entry {
			writeFile(t, filepath.Join(w.AdminDir, "gitdir"), "garbage\n")
			e := entryFor(t, r, w)
			if e.Mapping != MappingUnmapped {
				t.Fatalf("fixture error: entry = %+v, want unmapped", e)
			}
			return e
		}},
		{"mapped when listed but the .git file was repointed before inspection", ReasonDotgitMismatch, func(t *testing.T, r *fixtureRepo, w *fixtureWT) Entry {
			e := entryFor(t, r, w)
			writeFile(t, filepath.Join(w.Path, ".git"), "gitdir: "+filepath.Join(r.CommonDir, "worktrees", "other")+"\n")
			return e
		}},
		{"mapped when listed but the worktree dir vanished", ReasonWorktreeMissing, func(t *testing.T, r *fixtureRepo, w *fixtureWT) Entry {
			e := entryFor(t, r, w)
			if err := os.RemoveAll(w.Path); err != nil {
				t.Fatal(err)
			}
			return e
		}},
		{"mapped when listed but the worktree dir became a symlink", ReasonWorktreeSymlink, func(t *testing.T, r *fixtureRepo, w *fixtureWT) Entry {
			e := entryFor(t, r, w)
			moved := filepath.Join(r.Tmp, "moved")
			if err := os.Rename(w.Path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, w.Path); err != nil {
				t.Fatal(err)
			}
			return e
		}},
		{"forged admin name escaping the worktrees dir", ReasonInvalidAdminName, func(t *testing.T, r *fixtureRepo, w *fixtureWT) Entry {
			return Entry{AdminName: "../../etc", AdminDir: "/etc", Name: "etc", RegisteredPath: "/etc",
				LocalPath: "/etc", Mapping: MappingLocal}
		}},
		{"zero Entry", ReasonInvalidAdminName, func(t *testing.T, r *fixtureRepo, w *fixtureWT) Entry { return Entry{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			e := tc.entry(t, r, w)

			facts, err := Inspect(context.Background(), r.Repo(), e)
			if !errors.Is(err, ErrUnmapped) {
				t.Fatalf("err = %v, want errors.Is(_, ErrUnmapped)", err)
			}
			if !strings.Contains(err.Error(), string(tc.reason)) {
				t.Errorf("err = %q, want it to carry the reason %q", err, tc.reason)
			}
			if facts != (Facts{}) {
				t.Errorf("facts = %+v, want none for a refused entry", facts)
			}
		})
	}
}

// TestInspectIgnoresCallerSuppliedPaths: Inspect re-derives the mapping from
// AdminName, so an Entry whose path fields were tampered with (or went stale)
// can never redirect it to another worktree.
func TestInspectIgnoresCallerSuppliedPaths(t *testing.T) {
	r := newRepo(t, "hostrepo")
	a := addWorktree(t, r, "1-a", "feature/1-a")
	b := addWorktree(t, r, "2-b", "feature/2-b")
	writeFile(t, filepath.Join(b.Path, "notes.txt"), "only b is untracked\n")

	e := entryFor(t, r, a)
	e.LocalPath, e.AdminDir, e.RegisteredPath, e.Name = b.Path, b.AdminDir, b.Path, b.Name

	facts, err := Inspect(context.Background(), r.Repo(), e)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if facts.Branch != "feature/1-a" || facts.Untracked {
		t.Errorf("facts = %+v, want worktree a's (branch feature/1-a, clean), not b's", facts)
	}
}

func TestInspectStatusOutputOverTheCapIsAnError(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	for i := range 100 {
		writeFile(t, filepath.Join(w.Path, fmt.Sprintf("untracked-%03d.txt", i)), "x\n")
	}
	e := entryFor(t, r, w)
	setVar(t, &maxStdoutBytes, 256)

	_, err := Inspect(context.Background(), r.Repo(), e)
	if !errors.Is(err, errOutputTruncated) {
		t.Errorf("err = %v, want errors.Is(_, errOutputTruncated): truncated status must not become partial facts", err)
	}
}

func TestInspectActivityWalkOverTheEntryCapIsAnError(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	for i := range 10 {
		writeFile(t, filepath.Join(w.Path, fmt.Sprintf("untracked-%d.txt", i)), "x\n")
	}
	e := entryFor(t, r, w)
	setVar(t, &maxActivityEntries, 5)

	_, err := Inspect(context.Background(), r.Repo(), e)
	if !errors.Is(err, errOutputTruncated) {
		t.Errorf("err = %v, want errors.Is(_, errOutputTruncated): an unfinished activity walk must keep the entry", err)
	}
}

func TestInspectRejectsABranchNameThatFailsValidation(t *testing.T) {
	// Git allows ';' in a ref name; the value flows to later shell-adjacent
	// consumers, so anything outside the branch pattern is refused.
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "weird;name")

	facts, err := Inspect(context.Background(), r.Repo(), entryFor(t, r, w))
	if err == nil {
		t.Fatalf("Inspect = %+v, nil, want an error for branch %q", facts, w.Branch)
	}
	if facts.Branch == w.Branch {
		t.Errorf("facts.Branch = %q, an unvalidated branch name must not be reported", facts.Branch)
	}
}

func TestInspectWithACancelledContextIsAnError(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Inspect(ctx, r.Repo(), e); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(_, context.Canceled)", err)
	}
}

func TestInspectIgnoresAmbientRepoLocationVariables(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	head := w.head(t)
	for _, k := range repoLocationVars {
		t.Setenv(k, filepath.Join(r.Tmp, "junk", k))
	}

	facts, err := Inspect(context.Background(), r.Repo(), e)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if facts.Branch != "feature/1-x" || facts.HeadSHA != head || facts.Dirty || facts.Untracked {
		t.Errorf("facts = %+v, want the real clean worktree (branch feature/1-x, HEAD %s)", facts, head)
	}
}

// --- read-only proof ---

// readOnlyFixture builds every flavor Inspect must leave untouched and returns
// the repo plus a marker path that a git fsmonitor hook would create.
func readOnlyFixture(t *testing.T) (r *fixtureRepo, marker string) {
	t.Helper()
	r = newRepo(t, "hostrepo")
	newBareOrigin(t, r)

	clean := addWorktree(t, r, "1-clean", "feature/1-clean")
	clean.push(t)

	dirty := addWorktree(t, r, "2-dirty", "feature/2-dirty")
	writeFile(t, filepath.Join(dirty.Path, "tracked.txt"), "changed\n")
	writeFile(t, filepath.Join(dirty.Path, "notes.txt"), "untracked\n")
	writeFile(t, filepath.Join(dirty.Path, "ignored", "cache.bin"), "ignored\n")

	// A tracked file whose stat no longer matches the index but whose content
	// is unchanged: a plain `git status` would refresh and rewrite the index.
	statDirty := addWorktree(t, r, "3-statdirty", "feature/3-statdirty")
	statDirty.push(t)
	setMtime(t, filepath.Join(statDirty.Path, "tracked.txt"), time.Unix(1609459200, 0))

	addWorktree(t, r, "4-detached", "")

	locked := addWorktree(t, r, "5-locked", "feature/5-locked")
	r.git(t, "worktree", "lock", locked.Path)

	foreign := addWorktree(t, r, "6-foreign", "feature/6-foreign")
	foreign.push(t)
	setMtime(t, filepath.Join(foreign.Path, "tracked.txt"), time.Unix(1609459200, 0))
	rewriteAsForeign(t, foreign, foreignRootFor(r, "workspace"))

	unmapped := addWorktree(t, r, "7-unmapped", "feature/7-unmapped")
	writeFile(t, filepath.Join(unmapped.AdminDir, "gitdir"), "garbage\n")

	// Last: an fsmonitor hook records its own execution, and must never run.
	// (Configured after the fixture commands, which would trigger it.)
	marker = filepath.Join(r.Tmp, "fsmonitor-ran")
	hook := filepath.Join(r.Tmp, "fsmonitor-hook.sh")
	writeFile(t, hook, "#!/bin/sh\ntouch '"+marker+"'\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(t, "config", "core.fsmonitor", hook)
	return r, marker
}

func TestEnumerateAndInspectChangeNothingOnDisk(t *testing.T) {
	r, marker := readOnlyFixture(t)
	before := snapshotTree(t, r.Tmp)

	ctx := context.Background()
	for pass := 1; pass <= 2; pass++ {
		repo, entries, err := Enumerate(ctx, r.Root)
		if err != nil {
			t.Fatalf("pass %d: Enumerate: %v", pass, err)
		}
		if len(entries) != 7 {
			t.Fatalf("pass %d: %d entries, want 7: %+v", pass, len(entries), entries)
		}
		mapped, refused := 0, 0
		for _, e := range entries {
			_, err := Inspect(ctx, repo, e)
			switch {
			case e.Mapping == MappingUnmapped && errors.Is(err, ErrUnmapped):
				refused++
			case e.Mapping != MappingUnmapped && err == nil:
				mapped++
			default:
				t.Errorf("pass %d: entry %+v: Inspect err = %v", pass, e, err)
			}
		}
		if mapped != 6 || refused != 1 {
			t.Errorf("pass %d: mapped=%d refused=%d, want 6 and 1 (the run must actually inspect worktrees)", pass, mapped, refused)
		}
	}

	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Errorf("the configured fsmonitor hook ran (Lstat err = %v): -c core.fsmonitor=false was not honored", err)
	}
	if diffs := diffSnapshots(before, snapshotTree(t, r.Tmp)); len(diffs) > 0 {
		t.Errorf("Enumerate/Inspect modified the disk:\n%s", strings.Join(diffs, "\n"))
	}
}

// TestReadOnlyFixtureWouldCatchAnIndexRefresh is the control for the test
// above: the same fixture, inspected the naive way (a plain `git status`),
// changes the index and runs the fsmonitor hook, and snapshotTree sees both. It
// keeps the read-only proof from passing vacuously.
func TestReadOnlyFixtureWouldCatchAnIndexRefresh(t *testing.T) {
	r, marker := readOnlyFixture(t)
	before := snapshotTree(t, r.Tmp)

	runGit(t, filepath.Join(r.Root, ".worktrees", "3-statdirty"), "status", "--porcelain")

	if _, err := os.Lstat(marker); err != nil {
		t.Errorf("control: a plain git status did not run the fsmonitor hook (Lstat err = %v), so the hook check proves nothing", err)
	}
	if len(diffSnapshots(before, snapshotTree(t, r.Tmp))) == 0 {
		t.Error("control: a plain git status left the snapshot unchanged, so the snapshot cannot detect index refreshes")
	}
}
