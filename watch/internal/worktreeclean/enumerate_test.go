package worktreeclean

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// --- ResolveRepo ---

func TestResolveRepoFromMainRootSubdirAndLinkedWorktreeGivesTheSameRepo(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	sub := filepath.Join(r.Root, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for name, dir := range map[string]string{
		"main root":       r.Root,
		"subdirectory":    sub,
		"linked worktree": w.Path,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveRepo(context.Background(), dir)
			if err != nil {
				t.Fatalf("ResolveRepo(%s): %v", dir, err)
			}
			if want := r.Repo(); got != want {
				t.Errorf("ResolveRepo(%s) = %+v, want %+v", dir, got, want)
			}
		})
	}
}

func TestResolveRepoResolvesSymlinksInTheMainRoot(t *testing.T) {
	r := newRepo(t, "hostrepo")
	link := filepath.Join(r.Tmp, "link-to-repo")
	if err := os.Symlink(r.Root, link); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveRepo(context.Background(), link)
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	if want := r.Repo(); got != want {
		t.Errorf("ResolveRepo(symlink) = %+v, want the real paths %+v", got, want)
	}
}

func TestResolveRepoDerivesTheCommonDirFromGitNotFromTheEnvironment(t *testing.T) {
	r := newRepo(t, "hostrepo")
	other := newRepo(t, "otherrepo")
	// An inherited GIT_DIR/GIT_COMMON_DIR must not redirect the answer.
	t.Setenv("GIT_DIR", other.CommonDir)
	t.Setenv("GIT_COMMON_DIR", other.CommonDir)

	got, err := ResolveRepo(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	if want := r.Repo(); got != want {
		t.Errorf("ResolveRepo = %+v, want %+v (ambient GIT_DIR must be ignored)", got, want)
	}
}

func TestResolveRepoRejectsWhatIsNotAMainCheckout(t *testing.T) {
	// Control: a real main checkout resolves, so the errors below come from the
	// rejection and not from ResolveRepo failing for every input.
	control := newRepo(t, "hostrepo")
	if _, err := ResolveRepo(context.Background(), control.Root); err != nil {
		t.Fatalf("control ResolveRepo(main checkout): %v", err)
	}
	t.Run("not a git repository", func(t *testing.T) {
		isolateGit(t)
		dir := realPath(t, t.TempDir())
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		if _, err := ResolveRepo(context.Background(), dir); err == nil {
			t.Error("ResolveRepo on a plain directory returned nil, want an error")
		}
	})
	t.Run("bare repository whose common dir is not named .git", func(t *testing.T) {
		isolateGit(t)
		bare := filepath.Join(realPath(t, t.TempDir()), "bare.git")
		runGit(t, filepath.Dir(bare), "init", "-q", "--bare", bare)
		if got, err := ResolveRepo(context.Background(), bare); err == nil {
			t.Errorf("ResolveRepo(bare) = %+v, nil, want an error (MainRoot would not be a checkout)", got)
		}
	})
	t.Run("separate git dir", func(t *testing.T) {
		isolateGit(t)
		tmp := realPath(t, t.TempDir())
		work := filepath.Join(tmp, "work")
		if err := os.Mkdir(work, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, work, "init", "-q", "--separate-git-dir", filepath.Join(tmp, "elsewhere-gitdir"))
		if got, err := ResolveRepo(context.Background(), work); err == nil {
			t.Errorf("ResolveRepo(separate git dir) = %+v, nil, want an error", got)
		}
	})
}

func TestResolveRepoRefusesALinkedWorktreeOutsideTheMainRootItPointsAt(t *testing.T) {
	// A sandboxed agent can rewrite a worktree's .git file, and git then
	// reports the repository that file names. The resolved root must still
	// contain the directory the caller asked about.
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	other := newRepo(t, "otherrepo")
	otherWT := addWorktree(t, other, "2-y", "feature/2-y")
	if _, err := ResolveRepo(context.Background(), w.Path); err != nil {
		t.Fatalf("control ResolveRepo(linked worktree): %v", err)
	}

	writeFile(t, filepath.Join(w.Path, ".git"), "gitdir: "+otherWT.AdminDir+"\n")

	if got, err := ResolveRepo(context.Background(), w.Path); err == nil {
		t.Errorf("ResolveRepo(%s) = %+v, nil, want an error: the worktree now points at another repository", w.Path, got)
	}
	if got, _, err := Enumerate(context.Background(), w.Path); err == nil {
		t.Errorf("Enumerate(%s) = %+v, nil, want an error", w.Path, got)
	}
}

// --- Enumerate ---

func TestEnumerateListsBranchDetachedAndTicketlessWorktrees(t *testing.T) {
	r := newRepo(t, "hostrepo")
	// Created out of admin-name order on purpose: results are sorted by it.
	scratch := addWorktree(t, r, "scratch", "scratch")
	detached := addWorktree(t, r, "2-detached", "")
	ticket := addWorktree(t, r, "1-feature-x", "feature/1-feature-x")

	repo, entries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if want := r.Repo(); repo != want {
		t.Errorf("Repo = %+v, want %+v", repo, want)
	}
	want := []Entry{ticket.wantLocal(), detached.wantLocal(), scratch.wantLocal()}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries =\n%+v\nwant (sorted by admin name)\n%+v", entries, want)
	}
	for _, e := range entries {
		if e.LocalPath == r.Root || e.RegisteredPath == r.Root {
			t.Errorf("entry %+v is the main worktree, which must never be listed", e)
		}
	}
}

func TestEnumerateListsTranslatedWorktreesWithTheirLocalPath(t *testing.T) {
	r := newRepo(t, "hostrepo")
	local := addWorktree(t, r, "1-local", "feature/1-local")
	foreign := addWorktree(t, r, "2-foreign", "feature/2-foreign")
	rewriteAsForeign(t, foreign, foreignRootFor(r, "workspace"))

	_, entries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	want := []Entry{local.wantLocal(), foreign.wantTranslated()}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries =\n%+v\nwant\n%+v", entries, want)
	}
}

func TestEnumerateExcludesOnlyWorktreesRegisteredOutsideADotWorktreesDir(t *testing.T) {
	r := newRepo(t, "hostrepo")
	kept := addWorktree(t, r, "1-kept", "feature/1-kept")
	addWorktreeAt(t, r, filepath.Join(r.Tmp, "elsewhere", "ext"), "feature/ext")
	addWorktreeAt(t, r, filepath.Join(r.Root, "other", "inside"), "feature/inside")
	addWorktreeAt(t, r, filepath.Join(r.Root, ".worktrees", "sub", "nested"), "feature/nested")

	_, entries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if want := []Entry{kept.wantLocal()}; !reflect.DeepEqual(entries, want) {
		t.Errorf("entries =\n%+v\nwant only the .worktrees/<name> worktree\n%+v", entries, want)
	}
}

// TestEnumerateKeepsMalformedAdminEntriesAsUnmapped is the regression for the
// non-matching case: the only silent exclusion is a cleanly parsed gitdir whose
// parent dir is not .worktrees. Everything malformed stays visible.
func TestEnumerateKeepsMalformedAdminEntriesAsUnmapped(t *testing.T) {
	r := newRepo(t, "hostrepo")
	healthy := addWorktree(t, r, "1-healthy", "feature/1-healthy")
	garbage := addWorktree(t, r, "2-garbage", "feature/2-garbage")
	noGitdir := addWorktree(t, r, "3-nogitdir", "feature/3-nogitdir")
	notDir := addWorktree(t, r, "4-notdir", "feature/4-notdir")

	writeFile(t, filepath.Join(garbage.AdminDir, "gitdir"), "garbage\n")
	if err := os.Remove(filepath.Join(noGitdir.AdminDir, "gitdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(notDir.AdminDir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, notDir.AdminDir, "not a directory\n")
	badName := "5-bad\nname"
	if err := os.Mkdir(filepath.Join(r.CommonDir, "worktrees", badName), 0o755); err != nil {
		t.Fatalf("cannot create an admin dir with a newline in its name on this filesystem: %v", err)
	}

	_, entries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	byAdmin := map[string]Entry{}
	for _, e := range entries {
		byAdmin[e.AdminName] = e
	}
	if len(entries) != 5 {
		t.Errorf("got %d entries, want 5 (1 healthy + 4 malformed, none silently dropped): %+v", len(entries), entries)
	}
	if got := byAdmin[healthy.AdminName]; got != healthy.wantLocal() {
		t.Errorf("healthy entry = %+v, want %+v (a bad neighbour must not affect it)", got, healthy.wantLocal())
	}
	for _, tc := range []struct {
		admin  string
		reason UnmappedReason
	}{
		{garbage.AdminName, ReasonGitdirInvalid},
		{noGitdir.AdminName, ReasonGitdirUnreadable},
		{notDir.AdminName, ReasonAdminNotDir},
		{badName, ReasonInvalidAdminName},
	} {
		got, ok := byAdmin[tc.admin]
		if !ok {
			t.Errorf("admin entry %q is not listed", tc.admin)
			continue
		}
		if got.Mapping != MappingUnmapped || got.Reason != tc.reason || got.LocalPath != "" {
			t.Errorf("entry %q = {Mapping:%q Reason:%q LocalPath:%q}, want unmapped %q with no LocalPath",
				tc.admin, got.Mapping, got.Reason, got.LocalPath, tc.reason)
		}
	}
}

func TestEnumerateNeverListsAMainRepoThatLivesUnderADotWorktreesDir(t *testing.T) {
	// The main checkout itself sits at <tmp>/outer/.worktrees/repo (e.g. a repo
	// cloned into another repo's .worktrees). It has no admin entry, so it must
	// never appear -- only its own linked worktrees do.
	r := newRepo(t, "outer/.worktrees/repo")

	repo, entries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if want := r.Repo(); repo != want {
		t.Errorf("Repo = %+v, want %+v", repo, want)
	}
	if entries == nil || len(entries) != 0 {
		t.Errorf("entries = %#v, want an empty non-nil slice (the main worktree is never listed)", entries)
	}

	w := addWorktree(t, r, "1-x", "feature/1-x")
	_, entries, err = Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if want := []Entry{w.wantLocal()}; !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v, want only the linked worktree %+v", entries, want)
	}
}

func TestEnumerateFromALinkedWorktreeSeesTheSameRepoAndEntries(t *testing.T) {
	r := newRepo(t, "hostrepo")
	a := addWorktree(t, r, "1-a", "feature/1-a")
	b := addWorktree(t, r, "2-b", "feature/2-b")

	fromMain, mainEntries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("Enumerate(main): %v", err)
	}
	fromLinked, linkedEntries, err := Enumerate(context.Background(), b.Path)
	if err != nil {
		t.Fatalf("Enumerate(linked): %v", err)
	}
	if fromMain != fromLinked || fromLinked.MainRoot != r.Root {
		t.Errorf("Repo from a linked worktree = %+v, want the main root's %+v", fromLinked, fromMain)
	}
	if want := []Entry{a.wantLocal(), b.wantLocal()}; !reflect.DeepEqual(linkedEntries, want) || !reflect.DeepEqual(mainEntries, want) {
		t.Errorf("entries main=%+v linked=%+v, want both %+v", mainEntries, linkedEntries, want)
	}
}

func TestEnumerateWithNoWorktreesIsAnEmptyNonNilSlice(t *testing.T) {
	assertEmpty := func(t *testing.T, r *fixtureRepo) {
		t.Helper()
		_, entries, err := Enumerate(context.Background(), r.Root)
		if err != nil {
			t.Fatalf("Enumerate: %v", err)
		}
		if entries == nil || len(entries) != 0 {
			t.Fatalf("entries = %#v, want an empty non-nil slice", entries)
		}
		data, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "[]" {
			t.Errorf("json = %s, want [] (go-gotchas rule 1: never null)", data)
		}
	}
	t.Run("no worktrees dir at all", func(t *testing.T) {
		r := newRepo(t, "hostrepo")
		if _, err := os.Lstat(filepath.Join(r.CommonDir, "worktrees")); !os.IsNotExist(err) {
			t.Fatalf("fixture error: .git/worktrees should not exist yet (err = %v)", err)
		}
		assertEmpty(t, r)
	})
	t.Run("after the only worktree was removed", func(t *testing.T) {
		r := newRepo(t, "hostrepo")
		w := addWorktree(t, r, "1-x", "feature/1-x")
		r.git(t, "worktree", "remove", w.Path)
		assertEmpty(t, r)
	})
}

func TestEnumerateNeverMapsEntriesBehindASymlinkedWorktreesAdminDir(t *testing.T) {
	// <common>/worktrees replaced by a symlink: nothing behind it can be
	// trusted. Whether Enumerate refuses (error) or lists the entries as
	// unmapped, no entry may come back mapped.
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	// Control: before the swap the same entry is listed and mapped.
	if _, entries, err := Enumerate(context.Background(), r.Root); err != nil || len(entries) != 1 || entries[0] != w.wantLocal() {
		t.Fatalf("control Enumerate = %+v, %v, want the one local entry %+v", entries, err, w.wantLocal())
	}
	real := filepath.Join(r.Tmp, "real-admin-worktrees")
	adminRoot := filepath.Join(r.CommonDir, "worktrees")
	if err := os.Rename(adminRoot, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, adminRoot); err != nil {
		t.Fatal(err)
	}

	_, entries, err := Enumerate(context.Background(), r.Root)
	if err == nil && len(entries) == 0 {
		t.Fatal("Enumerate returned no entries and no error, which silently hides the worktree")
	}
	for _, e := range entries {
		if e.Mapping != MappingUnmapped || e.LocalPath != "" {
			t.Errorf("entry %+v behind a symlinked worktrees admin dir must be unmapped", e)
		}
	}
}

func TestEnumerateRejectsWhatIsNotAMainCheckout(t *testing.T) {
	control := newRepo(t, "hostrepo")
	if _, _, err := Enumerate(context.Background(), control.Root); err != nil {
		t.Fatalf("control Enumerate(main checkout): %v", err)
	}
	dir := realPath(t, t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, entries, err := Enumerate(context.Background(), dir); err == nil {
		t.Errorf("Enumerate on a plain directory = %+v, nil, want an error", entries)
	}
}
