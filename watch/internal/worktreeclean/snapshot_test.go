package worktreeclean

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests cover the private snapshot git dir Inspect builds for each call
// (#1278): what it is made of, the limits and type checks on its sources, and
// that it is always removed. Everything runs real git. A snapshot is only
// observable through the package seams afterSnapshot (the built dir),
// removeSnapshotDir (every removal) and snapshotParent (where dirs are made), so
// each test that claims "the snapshot was X" first proves a snapshot was built
// at all; without that, a missing implementation would pass every "is gone" or
// "is private" check vacuously.

// useSnapshotParent points the snapshot builder at a fresh test-owned directory
// and returns it (symlink-resolved, so it compares equal to a git argv path).
func useSnapshotParent(t *testing.T) string {
	t.Helper()
	dir := realPath(t, t.TempDir())
	setVar(t, &snapshotParent, dir)
	return dir
}

// onSnapshot installs fn as the afterSnapshot hook and returns how many times
// the hook fired, so a test can fail when Inspect never built a snapshot.
func onSnapshot(t *testing.T, fn func(gitDir string)) *int {
	t.Helper()
	fired := new(int)
	setVar(t, &afterSnapshot, func(gitDir string) {
		*fired++
		fn(gitDir)
	})
	return fired
}

func requireSnapshotBuilt(t *testing.T, fired *int) {
	t.Helper()
	if *fired == 0 {
		t.Fatal("afterSnapshot never fired: Inspect built no snapshot git dir, so nothing below proves anything about it")
	}
}

// removal is one removeSnapshotDir call; existed records whether the directory
// was really on disk at that moment (a positive control that a snapshot was
// built before it was removed).
type removal struct {
	path    string
	existed bool
}

// recordRemovals replaces removeSnapshotDir with a recorder. With failWith nil it
// still removes the directory; otherwise it removes nothing and returns
// failWith, simulating a cleanup that fails (the test's temp dir reaps the rest).
func recordRemovals(t *testing.T, failWith error) *[]removal {
	t.Helper()
	var removals []removal
	setVar(t, &removeSnapshotDir, func(path string) error {
		_, err := os.Lstat(path)
		removals = append(removals, removal{path: path, existed: err == nil})
		if failWith != nil {
			return failWith
		}
		return os.RemoveAll(path)
	})
	return &removals
}

// errText is err's lower-cased text with each path replaced by a placeholder:
// fixture paths embed the test name (t.TempDir), which can itself contain a
// marker word such as "symlink" or "include" and would satisfy a marker check
// the message never made.
func errText(err error, paths ...string) string {
	text := err.Error()
	for _, p := range paths {
		text = strings.ReplaceAll(text, p, "<path>")
	}
	return strings.ToLower(text)
}

// assertRefused asserts Inspect refused with an error whose text carries
// wantMarker and returned no Facts. Distinct failure classes get distinct
// markers (error-handling rule 8), so a regression that collapsed two classes
// into one message fails here.
func assertRefused(t *testing.T, facts Facts, err error, wantMarker string, paths ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Inspect = %+v, nil, want an error (entry kept)", facts)
	}
	if text := errText(err, paths...); !strings.Contains(text, wantMarker) {
		t.Errorf("err = %q, want it to carry the marker %q", err, wantMarker)
	}
	if facts != (Facts{}) {
		t.Errorf("facts = %+v, want none for a refused worktree", facts)
	}
}

// sumCopiedBytes is the bytes of every regular file in a built snapshot that was
// copied from a source; the alternates file is written by the builder, not
// copied, so it does not count toward maxSnapshotBytes.
func sumCopiedBytes(t *testing.T, gitDir string) int {
	t.Helper()
	alternates := filepath.Join(gitDir, "objects", "info", "alternates")
	total := 0
	err := filepath.WalkDir(gitDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || p == alternates {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += int(info.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

// countWalkEntries is the number of entries filepath.WalkDir yields under root,
// the root itself included (the way diskUsage counts toward maxSizeEntries).
func countWalkEntries(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(string, fs.DirEntry, error) error { n++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func inspectWT(t *testing.T, r *fixtureRepo, e Entry) (Facts, error) {
	t.Helper()
	return Inspect(context.Background(), r.Repo(), e)
}

func mustInspect(t *testing.T, r *fixtureRepo, e Entry) Facts {
	t.Helper()
	facts, err := inspectWT(t, r, e)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return facts
}

// --- what the snapshot is ---

func TestSnapshotIsPrivateHasNoCommonDirAndKeepsTheConfigAndIndexAsTheyWere(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	setMtime(t, filepath.Join(w.AdminDir, "index"), time.Unix(1609459200, 123456789))
	e := entryFor(t, r, w)
	parent := useSnapshotParent(t)
	liveConfig, err := os.ReadFile(filepath.Join(r.CommonDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	liveIndex, err := os.Lstat(filepath.Join(w.AdminDir, "index"))
	if err != nil {
		t.Fatal(err)
	}

	fired := onSnapshot(t, func(gitDir string) {
		if filepath.Dir(gitDir) != parent {
			t.Errorf("snapshot dir %s is not directly under snapshotParent %s", gitDir, parent)
		}
		info, err := os.Lstat(gitDir)
		switch {
		case err != nil:
			t.Errorf("Lstat(snapshot dir): %v", err)
		case !info.IsDir():
			t.Errorf("snapshot %s is %v, want a directory", gitDir, info.Mode())
		case info.Mode().Perm() != 0o700:
			t.Errorf("snapshot dir mode = %v, want 0700 (only cenci may write it)", info.Mode().Perm())
		}
		if _, err := os.Lstat(filepath.Join(gitDir, "commondir")); !os.IsNotExist(err) {
			t.Errorf("snapshot has a commondir file (Lstat err = %v), want none so git never falls back to the live common dir", err)
		}
		gotConfig, err := os.ReadFile(filepath.Join(gitDir, "config"))
		if err != nil || string(gotConfig) != string(liveConfig) {
			t.Errorf("snapshot config = %q (err %v), want the live config byte for byte", gotConfig, err)
		}
		alt, err := os.ReadFile(filepath.Join(gitDir, "objects", "info", "alternates"))
		if err != nil || strings.TrimSpace(string(alt)) != filepath.Join(r.CommonDir, "objects") {
			t.Errorf("alternates = %q (err %v), want the live %s", alt, err, filepath.Join(r.CommonDir, "objects"))
		}
		if _, err := os.Lstat(filepath.Join(gitDir, "refs")); err != nil {
			t.Errorf("snapshot has no refs/ dir (Lstat err = %v), want one so git accepts it as a git dir", err)
		}
		idx, err := os.Lstat(filepath.Join(gitDir, "index"))
		if err != nil {
			t.Errorf("snapshot index: %v", err)
		} else if !idx.ModTime().Equal(liveIndex.ModTime()) {
			t.Errorf("snapshot index mtime = %v, want the live %v (racy-git needs it)", idx.ModTime(), liveIndex.ModTime())
		}
	})

	mustInspect(t, r, e)
	requireSnapshotBuilt(t, fired)
}

func TestSnapshotCarriesPackedRefsAndInfoExclude(t *testing.T) {
	r := newRepo(t, "hostrepo")
	newBareOrigin(t, r)
	w := addWorktree(t, r, "1-x", "feature/1-x")
	w.push(t)
	r.git(t, "pack-refs", "--all")
	packed, err := os.ReadFile(filepath.Join(r.CommonDir, "packed-refs"))
	if err != nil || !strings.Contains(string(packed), "refs/remotes/origin/feature/1-x") {
		t.Fatalf("fixture error: packed-refs = %q (err %v), want the remote-tracking ref packed", packed, err)
	}
	if _, err := os.Lstat(filepath.Join(r.CommonDir, "refs", "remotes", "origin", "feature", "1-x")); !os.IsNotExist(err) {
		t.Fatalf("fixture error: the remote-tracking ref is still loose (Lstat err = %v)", err)
	}
	writeFile(t, filepath.Join(w.Path, "notes.scratch"), "scratch\n")
	e := entryFor(t, r, w)
	fired := onSnapshot(t, func(string) {})

	// Control: nothing ignores the file yet, so it reads as untracked.
	if facts := mustInspect(t, r, e); !facts.Untracked {
		t.Fatalf("control: facts = %+v, want Untracked before info/exclude lists the file", facts)
	}
	requireSnapshotBuilt(t, fired)

	exclude := filepath.Join(r.CommonDir, "info", "exclude")
	prior, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, exclude, string(prior)+"*.scratch\n")

	facts := mustInspect(t, r, e)
	if facts.Untracked {
		t.Errorf("facts = %+v, want the file ignored through info/exclude in the snapshot", facts)
	}
	if !facts.ReachableFromRemote || facts.Upstream != "origin/feature/1-x" || !facts.AheadKnown {
		t.Errorf("facts = %+v, want the packed remote-tracking ref used for reachability and the upstream", facts)
	}
}

// --- limits ---

func TestInspectSnapshotByteLimit(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	useSnapshotParent(t)

	copied := 0
	fired := onSnapshot(t, func(gitDir string) { copied = sumCopiedBytes(t, gitDir) })
	mustInspect(t, r, e)
	requireSnapshotBuilt(t, fired)
	if copied == 0 {
		t.Fatal("fixture error: the snapshot copied no bytes")
	}

	t.Run("exactly at the cap is not an error", func(t *testing.T) {
		setVar(t, &maxSnapshotBytes, copied)
		mustInspect(t, r, e)
	})
	t.Run("one byte over the cap is a truncation error", func(t *testing.T) {
		setVar(t, &maxSnapshotBytes, copied-1)
		facts, err := inspectWT(t, r, e)
		if !errors.Is(err, errOutputTruncated) {
			t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated): a partial snapshot must never be used", err)
		}
		if facts != (Facts{}) {
			t.Errorf("facts = %+v, want none", facts)
		}
	})
}

func TestInspectSnapshotRefLimit(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x") // refs/heads/{main,feature/1-x}: nested, no remotes
	e := entryFor(t, r, w)
	useSnapshotParent(t)

	entries := 0
	fired := onSnapshot(t, func(gitDir string) {
		entries = countWalkEntries(t, filepath.Join(gitDir, "refs", "heads"))
	})
	mustInspect(t, r, e)
	requireSnapshotBuilt(t, fired)
	if entries < 4 {
		t.Fatalf("fixture error: %d entries under refs/heads, want the root, feature/, feature/1-x and main at least", entries)
	}

	t.Run("exactly at the cap is not an error", func(t *testing.T) {
		setVar(t, &maxSnapshotRefs, entries)
		mustInspect(t, r, e)
	})
	t.Run("one entry over the cap is a truncation error", func(t *testing.T) {
		setVar(t, &maxSnapshotRefs, entries-1)
		facts, err := inspectWT(t, r, e)
		if !errors.Is(err, errOutputTruncated) {
			t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated)", err)
		}
		if facts != (Facts{}) {
			t.Errorf("facts = %+v, want none", facts)
		}
	})
}

// --- sources that must not be copied ---

// Distinct markers for the two classes of unsafe source (error-handling rule 8).
const (
	markSymlink    = "symlink"
	markNonRegular = "regular"
)

// unsafeSource is a file or directory the snapshot copies. prepare makes it
// exist (nil when the fixture already has it).
type unsafeSource struct {
	name    string
	prepare func(t *testing.T, r *fixtureRepo, w *fixtureWT)
	path    func(r *fixtureRepo, w *fixtureWT) string
}

func unsafeSources() []unsafeSource {
	return []unsafeSource{
		{"HEAD", nil, func(_ *fixtureRepo, w *fixtureWT) string { return filepath.Join(w.AdminDir, "HEAD") }},
		{"index", nil, func(_ *fixtureRepo, w *fixtureWT) string { return filepath.Join(w.AdminDir, "index") }},
		{"config", nil, func(r *fixtureRepo, _ *fixtureWT) string { return filepath.Join(r.CommonDir, "config") }},
		{"config.worktree", func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			r.git(t, "config", "extensions.worktreeConfig", "true")
			w.git(t, "config", "--worktree", "core.abbrev", "12")
		}, func(_ *fixtureRepo, w *fixtureWT) string { return filepath.Join(w.AdminDir, "config.worktree") }},
		{"packed-refs", func(t *testing.T, r *fixtureRepo, _ *fixtureWT) {
			r.git(t, "pack-refs", "--all")
		}, func(r *fixtureRepo, _ *fixtureWT) string { return filepath.Join(r.CommonDir, "packed-refs") }},
		{"loose ref", nil, func(r *fixtureRepo, _ *fixtureWT) string {
			return filepath.Join(r.CommonDir, "refs", "heads", "feature", "1-x")
		}},
		{"refs/remotes/origin directory", func(t *testing.T, r *fixtureRepo, _ *fixtureWT) {
			newBareOrigin(t, r)
		}, func(r *fixtureRepo, _ *fixtureWT) string {
			return filepath.Join(r.CommonDir, "refs", "remotes", "origin")
		}},
	}
}

// moveAside renames path into the fixture's "elsewhere" dir and returns the new
// location, leaving path free to be replaced.
func moveAside(t *testing.T, r *fixtureRepo, path string) string {
	t.Helper()
	aside := filepath.Join(r.Tmp, "elsewhere", filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(aside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, aside); err != nil {
		t.Fatal(err)
	}
	return aside
}

func TestInspectRefusesASymlinkedSnapshotSource(t *testing.T) {
	for _, tc := range unsafeSources() {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			if tc.prepare != nil {
				tc.prepare(t, r, w)
			}
			e := entryFor(t, r, w)
			parent := useSnapshotParent(t)
			// Control: the untouched fixture inspects fine.
			mustInspect(t, r, e)

			// The symlink points at an identical copy, so plain git still works
			// through it: the refusal below is the snapshot's rule, not git failing.
			path := tc.path(r, w)
			if err := os.Symlink(moveAside(t, r, path), path); err != nil {
				t.Fatal(err)
			}
			if tc.name != "HEAD" { // git itself refuses a HEAD that is not a plain file
				w.git(t, "--no-optional-locks", "status", "--porcelain")
			}

			facts, err := inspectWT(t, r, e)
			assertRefused(t, facts, err, markSymlink, r.Tmp, parent)
		})
	}
}

func TestInspectRefusesANonRegularSnapshotSource(t *testing.T) {
	// git would block reading a FIFO, so bound the red run; with the snapshot the
	// FIFO is refused before any git process reads it.
	setVar(t, &gitTimeout, 2*time.Second)
	setVar(t, &activityTimeout, 2*time.Second)
	for _, tc := range []unsafeSource{unsafeSources()[1], unsafeSources()[5]} { // index, loose ref
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			e := entryFor(t, r, w)
			parent := useSnapshotParent(t)
			mustInspect(t, r, e) // control: the untouched fixture inspects fine

			path := tc.path(r, w)
			moveAside(t, r, path)
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}

			facts, err := inspectWT(t, r, e)
			assertRefused(t, facts, err, markNonRegular, r.Tmp, parent)
			if strings.Contains(errText(err, r.Tmp, parent), markSymlink) {
				t.Errorf("err = %q, want a FIFO reported as non-regular, not as a symlink", err)
			}
		})
	}
}

// --- cleanup ---

func TestInspectRemovesItsSnapshotAfterEveryOutcome(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
		setup   func(t *testing.T, r *fixtureRepo, w *fixtureWT)
	}{
		{"success", false, nil},
		{"build error: symlinked HEAD", true, func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			path := filepath.Join(w.AdminDir, "HEAD")
			if err := os.Symlink(moveAside(t, r, path), path); err != nil {
				t.Fatal(err)
			}
		}},
		{"probe refusal: include", true, func(t *testing.T, r *fixtureRepo, _ *fixtureWT) {
			inc := filepath.Join(r.Tmp, "extra.gitconfig")
			writeFile(t, inc, "[user]\n\tname = Extra\n")
			r.git(t, "config", "include.path", inc)
		}},
		{"later-stage refusal: gitlink", true, func(t *testing.T, _ *fixtureRepo, w *fixtureWT) {
			w.git(t, "update-index", "--add", "--cacheinfo", "160000,"+w.head(t)+",sub")
			runGit(t, w.Path, "init", "-q", "sub")
		}},
		{"later error: size walk timeout", true, func(t *testing.T, _ *fixtureRepo, _ *fixtureWT) {
			setVar(t, &sizeTimeout, time.Nanosecond)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			e := entryFor(t, r, w)
			parent := useSnapshotParent(t)
			removals := recordRemovals(t, nil)
			if tc.setup != nil {
				tc.setup(t, r, w)
			}

			_, err := inspectWT(t, r, e)
			if tc.wantErr != (err != nil) {
				t.Fatalf("Inspect err = %v, want an error: %v", err, tc.wantErr)
			}

			if len(*removals) == 0 {
				t.Fatal("removeSnapshotDir was never called: no snapshot was built, so the empty-parent check below proves nothing")
			}
			for _, rm := range *removals {
				if filepath.Dir(rm.path) != parent {
					t.Errorf("removed %s, want a dir directly under snapshotParent %s", rm.path, parent)
				}
				if !rm.existed {
					t.Errorf("removeSnapshotDir(%s): the snapshot dir did not exist when its removal was requested", rm.path)
				}
			}
			left, rerr := os.ReadDir(parent)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(left) != 0 {
				t.Errorf("snapshotParent still holds %d entries after Inspect returned, want the snapshot gone", len(left))
			}
		})
	}
}

func TestInspectJoinsASnapshotRemovalFailureIntoTheError(t *testing.T) {
	errStub := errors.New("stub: cannot remove the snapshot dir")
	cases := []struct {
		name string
		// setup makes Inspect fail for its own reason (nil: it succeeds);
		// wantMarker is that reason's text.
		setup      func(t *testing.T, r *fixtureRepo, w *fixtureWT)
		wantMarker string
	}{
		{"on success", nil, ""},
		// Co-occurrence (error-handling rule 26): each error alone would pass a
		// picker that returns only one of them.
		{"together with a later-stage refusal", func(t *testing.T, _ *fixtureRepo, w *fixtureWT) {
			w.git(t, "update-index", "--add", "--cacheinfo", "160000,"+w.head(t)+",sub")
			runGit(t, w.Path, "init", "-q", "sub")
		}, "submodule"},
		{"together with a snapshot build failure", func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			path := filepath.Join(w.AdminDir, "HEAD")
			if err := os.Symlink(moveAside(t, r, path), path); err != nil {
				t.Fatal(err)
			}
		}, markSymlink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			e := entryFor(t, r, w)
			if tc.setup != nil {
				tc.setup(t, r, w)
			}
			parent := useSnapshotParent(t)

			// Control: without the failing cleanup the outcome is the case's own.
			_, err := inspectWT(t, r, e)
			switch {
			case tc.wantMarker == "" && err != nil:
				t.Fatalf("control: Inspect = %v, want success", err)
			case tc.wantMarker != "" && (err == nil || !strings.Contains(errText(err, r.Tmp, parent), tc.wantMarker)):
				t.Fatalf("control: Inspect err = %v, want one carrying %q", err, tc.wantMarker)
			}

			removals := recordRemovals(t, errStub)
			_, err = inspectWT(t, r, e)
			if len(*removals) == 0 {
				t.Fatal("removeSnapshotDir was never called: no snapshot was built")
			}
			if !errors.Is(err, errStub) || !strings.Contains(err.Error(), errStub.Error()) {
				t.Errorf("err = %v, want the failed removal joined into it", err)
			}
			if tc.wantMarker != "" && !strings.Contains(errText(err, r.Tmp, parent), tc.wantMarker) {
				t.Errorf("err = %v, want the %q failure kept alongside the removal failure", err, tc.wantMarker)
			}
		})
	}
}

// --- split index ---

// A split index needs its sharedindex.* file, which the snapshot does not copy,
// so git fails and the worktree is kept. This pins that "keep" decision.
func TestInspectKeepsAWorktreeWithASplitIndex(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	w.git(t, "update-index", "--split-index")
	shared, _ := filepath.Glob(filepath.Join(w.AdminDir, "sharedindex.*"))
	commonShared, _ := filepath.Glob(filepath.Join(r.CommonDir, "sharedindex.*"))
	if len(shared)+len(commonShared) == 0 {
		t.Fatal("fixture error: update-index --split-index wrote no sharedindex file")
	}
	e := entryFor(t, r, w)
	useSnapshotParent(t)
	fired := onSnapshot(t, func(string) {})
	// Control: plain git reads the live split index fine.
	w.git(t, "--no-optional-locks", "status", "--porcelain")

	_, err := inspectWT(t, r, e)
	if err == nil {
		t.Fatal("Inspect returned nil, want an error: the snapshot has no shared index, so the worktree is kept")
	}
	requireSnapshotBuilt(t, fired)
}

// --- sources that must be there ---

// A valid repo always has a common-dir config; without it the include and
// ref-storage probe would be skipped, so its absence is an error, not a skip.
func TestInspectRefusesARepoWithoutACommonConfig(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	parent := useSnapshotParent(t)
	mustInspect(t, r, e) // control: with its config the fixture inspects fine

	if err := os.Remove(filepath.Join(r.CommonDir, "config")); err != nil {
		t.Fatal(err)
	}

	facts, err := inspectWT(t, r, e)
	assertRefused(t, facts, err, "does not exist", r.Tmp, parent)
}

// The worktree's HEAD is the other required source.
func TestInspectRefusesAWorktreeWithoutAHead(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	parent := useSnapshotParent(t)
	mustInspect(t, r, e) // control: with its HEAD the fixture inspects fine

	if err := os.Remove(filepath.Join(w.AdminDir, "HEAD")); err != nil {
		t.Fatal(err)
	}

	facts, err := inspectWT(t, r, e)
	assertRefused(t, facts, err, "does not exist", r.Tmp, parent)
}

// A line break in the common dir cannot be written into the alternates file
// (one path per line), so the snapshot refuses instead of mis-naming the objects.
// Enumerate already rejects such a path, so no Inspect fixture reaches this check:
// it is covered through newSnapshot, the defense behind that gate.
func TestNewSnapshotRefusesACommonDirWithALineBreak(t *testing.T) {
	parent := useSnapshotParent(t)
	repo := Repo{MainRoot: "/host\nrepo", CommonDir: "/host\nrepo/.git"}

	snap, err := newSnapshot(context.Background(), repo, Entry{})
	if err == nil {
		t.Fatalf("newSnapshot = %+v, nil, want an error", snap)
	}
	if text := errText(err, parent); !strings.Contains(text, "line break") {
		t.Errorf("err = %q, want it to name the line break", err)
	}
	if left, readErr := os.ReadDir(parent); readErr != nil || len(left) != 0 {
		t.Errorf("snapshot parent holds %v (read err %v), want the half-built dir removed", left, readErr)
	}
}
