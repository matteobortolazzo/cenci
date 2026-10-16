package worktreeclean

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// foreignRootFor is a root that is never created on this machine, standing in
// for "the other side of the sandbox mount".
func foreignRootFor(r *fixtureRepo, parts ...string) string {
	return filepath.Join(append([]string{r.Tmp, "gone"}, parts...)...)
}

// checkUnmapped asserts the exact reason (error-handling rule 8), that nothing
// local is offered for a later removal to touch, and that the admin identity is
// still reported so the entry can be shown.
func checkUnmapped(t *testing.T, got Entry, w *fixtureWT, reason UnmappedReason) {
	t.Helper()
	if got.Mapping != MappingUnmapped {
		t.Errorf("Mapping = %q, want %q", got.Mapping, MappingUnmapped)
	}
	if got.Reason != reason {
		t.Errorf("Reason = %q, want %q", got.Reason, reason)
	}
	if got.LocalPath != "" {
		t.Errorf("LocalPath = %q, want empty for an unmapped entry", got.LocalPath)
	}
	if got.AdminName != w.AdminName || got.AdminDir != w.AdminDir {
		t.Errorf("admin identity = (%q, %q), want (%q, %q)", got.AdminName, got.AdminDir, w.AdminName, w.AdminDir)
	}
}

func TestReasonValuesArePinned(t *testing.T) {
	// The reasons are the closed set #1265/#1266 render and branch on; their
	// strings are part of that contract.
	want := map[UnmappedReason]string{
		ReasonInvalidAdminName:    "invalid-admin-name",
		ReasonAdminNotDir:         "admin-not-dir",
		ReasonGitdirUnreadable:    "gitdir-unreadable",
		ReasonGitdirInvalid:       "gitdir-invalid",
		ReasonWorktreesSymlink:    "worktrees-symlink",
		ReasonWorktreeMissing:     "worktree-missing",
		ReasonWorktreeSymlink:     "worktree-symlink",
		ReasonDotgitInvalid:       "dotgit-invalid",
		ReasonDotgitMismatch:      "dotgit-mismatch",
		ReasonRegisteredElsewhere: "registered-elsewhere",
		ReasonCommondirMismatch:   "commondir-mismatch",
	}
	seen := map[string]bool{}
	for reason, s := range want {
		if string(reason) != s {
			t.Errorf("reason constant = %q, want %q", reason, s)
		}
		if seen[s] {
			t.Errorf("duplicate reason string %q", s)
		}
		seen[s] = true
	}
	if MappingLocal == MappingTranslated || MappingLocal == MappingUnmapped || MappingTranslated == MappingUnmapped {
		t.Errorf("Mapping constants must be distinct: %q %q %q", MappingLocal, MappingTranslated, MappingUnmapped)
	}
}

func TestValidComponent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"ticket worktree name", "1264-enumerate-translate", true},
		{"scratch", "scratch", true},
		{"dots inside a name", "a.b", true},
		{"leading dot name", ".hidden", true},
		{"empty", "", false},
		{"dot", ".", false},
		{"dot dot", "..", false},
		{"slash inside", "a/b", false},
		{"leading slash", "/abs", false},
		{"traversal", "../x", false},
		{"NUL inside", "a\x00b", false},
		{"newline inside", "a\nb", false},
		{"trailing newline", "ab\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validComponent(tc.in); got != tc.want {
				t.Errorf("validComponent(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestReadPointerFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		writeFile(t, p, content)
		return p
	}

	// Preconditions (fatal, so the rejection cases below can never pass merely
	// because every read fails): a pointer file is read and its trailing
	// newline trimmed, with or without one.
	for name, content := range map[string]string{"with-newline": "/a/b/.git\n", "no-newline": "/a/b/.git"} {
		got, err := readPointerFile(write(name, content))
		if err != nil || got != "/a/b/.git" {
			t.Fatalf("readPointerFile(%q) = %q, %v, want /a/b/.git", content, got, err)
		}
	}
	rejected := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(dir, "does-not-exist") }},
		{"NUL in content", func(t *testing.T) string { return write("nul", "/a/b\x00c/.git\n") }},
		{"newline inside content", func(t *testing.T) string { return write("nl", "/a/b\nc/.git\n") }},
		{"oversized content", func(t *testing.T) string { return write("big", strings.Repeat("a", 1<<20)) }},
		{"directory", func(t *testing.T) string {
			p := filepath.Join(dir, "adir")
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"symlink to a regular file", func(t *testing.T) string {
			target := write("target", "/a/b/.git\n")
			p := filepath.Join(dir, "link")
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			return p
		}},
	}
	for _, tc := range rejected {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			got, err := readPointerFile(tc.path(t))
			if err == nil {
				t.Errorf("readPointerFile = %q, nil, want an error", got)
			}
		})
	}

	// The symlink refusal is the open's own (O_NOFOLLOW), so it also covers a
	// dangling link and reads as a symlink, not as a missing file.
	t.Run("names a symlink as a symlink, dangling or not", func(t *testing.T) {
		live := filepath.Join(dir, "live-link")
		dangling := filepath.Join(dir, "dangling-link")
		if err := os.Symlink(write("live-target", "/a/b/.git\n"), live); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "no-such-target"), dangling); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{live, dangling} {
			if _, err := readPointerFile(p); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Errorf("readPointerFile(%s) err = %v, want one naming a symlink", filepath.Base(p), err)
			}
		}
	})

	// A FIFO must be rejected without blocking: opening it for read would hang
	// forever unless the open is O_NONBLOCK and the handle is Fstat-checked
	// (go-gotchas rule 15).
	t.Run("rejects a FIFO without blocking", func(t *testing.T) {
		p := filepath.Join(dir, "fifo")
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Skipf("mkfifo unavailable: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := readPointerFile(p)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Error("readPointerFile on a FIFO returned nil, want an error")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("readPointerFile blocked on a FIFO")
		}
	})
}

// --- mapEntry: the mapping decision ---

func TestMapEntryLocalWorktree(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1264-enumerate", "feature/1264-enumerate")

	got := mapEntry(r.Repo(), w.AdminName)
	if want := w.wantLocal(); got != want {
		t.Errorf("mapEntry = %+v, want %+v", got, want)
	}
}

func TestMapEntryContainerRegisteredWorktreeMapsToHostPath(t *testing.T) {
	// Created inside the container (recorded under /workspace), seen from the
	// host, where that path does not exist.
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1264-enumerate", "feature/1264-enumerate")
	rewriteAsForeign(t, w, foreignRootFor(r, "workspace"))

	got := mapEntry(r.Repo(), w.AdminName)
	want := w.wantTranslated()
	if got != want {
		t.Errorf("mapEntry = %+v, want %+v", got, want)
	}
	if want.LocalPath != filepath.Join(r.Root, ".worktrees", "1264-enumerate") {
		t.Fatalf("fixture error: LocalPath = %q is not <root>/.worktrees/<name>", want.LocalPath)
	}
	if want.RegisteredPath != filepath.Join(r.Tmp, "gone", "workspace", ".worktrees", "1264-enumerate") {
		t.Fatalf("fixture error: RegisteredPath = %q is not under the foreign workspace", want.RegisteredPath)
	}
}

func TestMapEntryHostRegisteredWorktreeMapsToContainerPath(t *testing.T) {
	// Created on the host (recorded under /home/dev/repo), seen from the
	// container, where the repo is mounted at a directory named workspace.
	r := newRepo(t, "workspace")
	if filepath.Base(r.Root) != "workspace" {
		t.Fatalf("fixture error: local root %q must be named workspace", r.Root)
	}
	w := addWorktree(t, r, "1264-enumerate", "feature/1264-enumerate")
	rewriteAsForeign(t, w, foreignRootFor(r, "home", "dev", "repo"))

	got := mapEntry(r.Repo(), w.AdminName)
	want := w.wantTranslated()
	if got != want {
		t.Errorf("mapEntry = %+v, want %+v", got, want)
	}
	if want.LocalPath != filepath.Join(r.Tmp, "workspace", ".worktrees", "1264-enumerate") {
		t.Fatalf("fixture error: LocalPath = %q is not <workspace>/.worktrees/<name>", want.LocalPath)
	}
	if want.RegisteredPath != filepath.Join(r.Tmp, "gone", "home", "dev", "repo", ".worktrees", "1264-enumerate") {
		t.Fatalf("fixture error: RegisteredPath = %q is not under the foreign host repo", want.RegisteredPath)
	}
}

func TestMapEntryRelativePointerFilesGiveLocal(t *testing.T) {
	// git's worktree.useRelativePaths writes both pointer files relative.
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-rel", "feature/1-rel")
	writeFile(t, filepath.Join(w.AdminDir, "gitdir"), "../../../.worktrees/1-rel/.git\n")
	writeFile(t, filepath.Join(w.Path, ".git"), "gitdir: ../../.git/worktrees/"+w.AdminName+"\n")

	got := mapEntry(r.Repo(), w.AdminName)
	if want := w.wantLocal(); got != want {
		t.Errorf("mapEntry = %+v, want %+v", got, want)
	}
}

func TestMapEntryRelativeGitdirOnlyGivesLocal(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-rel", "feature/1-rel")
	writeFile(t, filepath.Join(w.AdminDir, "gitdir"), "../../../.worktrees/1-rel/.git\n")

	got := mapEntry(r.Repo(), w.AdminName)
	if want := w.wantLocal(); got != want {
		t.Errorf("mapEntry = %+v, want %+v", got, want)
	}
}

// TestMapEntryCrossPointerMismatchIsUnmapped: a mapping is kept only when the
// worktree's .git file and the admin gitdir point at each other.
func TestMapEntryCrossPointerMismatchIsUnmapped(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, r *fixtureRepo, w *fixtureWT)
	}{
		{"local .git points at another admin dir", func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, ".git"), "gitdir: "+filepath.Join(r.CommonDir, "worktrees", "other")+"\n")
		}},
		{"translated .git points at another admin name", func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			rewriteAsForeign(t, w, foreignRootFor(r, "workspace"))
			writeFile(t, filepath.Join(w.Path, ".git"),
				"gitdir: "+filepath.Join(foreignRootFor(r, "workspace"), ".git", "worktrees", "other")+"\n")
		}},
		{"translated .git points at another foreign root", func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			rewriteAsForeign(t, w, foreignRootFor(r, "workspace"))
			writeFile(t, filepath.Join(w.Path, ".git"),
				"gitdir: "+filepath.Join(foreignRootFor(r, "elsewhere"), ".git", "worktrees", w.AdminName)+"\n")
		}},
		{"translated gitdir rewritten but .git still points at the local admin dir", func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			writeFile(t, filepath.Join(w.AdminDir, "gitdir"),
				filepath.Join(foreignRootFor(r, "workspace"), ".worktrees", w.Name, ".git")+"\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			tc.setup(t, r, w)

			checkUnmapped(t, mapEntry(r.Repo(), w.AdminName), w, ReasonDotgitMismatch)
		})
	}
}

func TestMapEntryRegisteredPathExistingElsewhereIsUnmapped(t *testing.T) {
	// The recorded path exists on this machine but is not <root>/.worktrees/<name>
	// (here: a different repo's .worktrees dir with the same worktree name).
	// Treating it as local would let a later removal act on the wrong tree.
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	elsewhere := filepath.Join(r.Tmp, "other")
	if err := os.MkdirAll(filepath.Join(elsewhere, ".worktrees", w.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(w.AdminDir, "gitdir"), filepath.Join(elsewhere, ".worktrees", w.Name, ".git")+"\n")

	checkUnmapped(t, mapEntry(r.Repo(), w.AdminName), w, ReasonRegisteredElsewhere)
}

func TestMapEntryMissingWorktreeDirIsUnmapped(t *testing.T) {
	for _, translated := range []bool{false, true} {
		name := "local"
		if translated {
			name = "translated"
		}
		t.Run(name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			if translated {
				rewriteAsForeign(t, w, foreignRootFor(r, "workspace"))
			}
			if err := os.RemoveAll(w.Path); err != nil {
				t.Fatal(err)
			}

			checkUnmapped(t, mapEntry(r.Repo(), w.AdminName), w, ReasonWorktreeMissing)
		})
	}
}

func TestMapEntrySymlinksAreRejectedWithTheMatchingReason(t *testing.T) {
	cases := []struct {
		name   string
		reason UnmappedReason
		setup  func(t *testing.T, r *fixtureRepo, w *fixtureWT)
	}{
		{".worktrees is a symlink", ReasonWorktreesSymlink, func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			real := filepath.Join(r.Tmp, "real-worktrees")
			if err := os.Rename(filepath.Join(r.Root, ".worktrees"), real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(r.Root, ".worktrees")); err != nil {
				t.Fatal(err)
			}
		}},
		{"the worktree dir is a symlink", ReasonWorktreeSymlink, func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			moved := filepath.Join(r.Tmp, "moved", w.Name)
			if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(w.Path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, w.Path); err != nil {
				t.Fatal(err)
			}
		}},
		{"the .git file is a symlink", ReasonDotgitInvalid, func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			target := filepath.Join(r.Tmp, "dotgit-target")
			data, err := os.ReadFile(filepath.Join(w.Path, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, target, string(data))
			if err := os.Remove(filepath.Join(w.Path, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(w.Path, ".git")); err != nil {
				t.Fatal(err)
			}
		}},
		{"the admin entry is a symlink", ReasonAdminNotDir, func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			moved := filepath.Join(r.Tmp, "moved-admin")
			if err := os.Rename(w.AdminDir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, w.AdminDir); err != nil {
				t.Fatal(err)
			}
		}},
		{"the admin entry is a regular file", ReasonAdminNotDir, func(t *testing.T, r *fixtureRepo, w *fixtureWT) {
			if err := os.RemoveAll(w.AdminDir); err != nil {
				t.Fatal(err)
			}
			writeFile(t, w.AdminDir, "not a directory\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			tc.setup(t, r, w)

			checkUnmapped(t, mapEntry(r.Repo(), w.AdminName), w, tc.reason)
		})
	}
}

func TestMapEntryInvalidAdminNamesAreRejectedBeforeAnyPathIsBuilt(t *testing.T) {
	r := newRepo(t, "hostrepo")
	for _, name := range []string{"", ".", "..", "a/b", "../x", "/abs", "a\x00b", "a\nb"} {
		t.Run("name "+strings.NewReplacer("\x00", `\x00`, "\n", `\n`, "/", "_").Replace(name), func(t *testing.T) {
			got := mapEntry(r.Repo(), name)
			if got.Mapping != MappingUnmapped || got.Reason != ReasonInvalidAdminName {
				t.Errorf("mapEntry(%q) = {%q %q}, want unmapped %q", name, got.Mapping, got.Reason, ReasonInvalidAdminName)
			}
			if got.AdminName != name {
				t.Errorf("AdminName = %q, want the offered name echoed", got.AdminName)
			}
			if got.AdminDir != "" || got.LocalPath != "" {
				t.Errorf("AdminDir=%q LocalPath=%q, want no path built from an invalid name", got.AdminDir, got.LocalPath)
			}
		})
	}
}

func TestMapEntryGitdirProblemsAreUnmappedWithTheirReason(t *testing.T) {
	cases := []struct {
		name    string
		reason  UnmappedReason
		content func(r *fixtureRepo, w *fixtureWT) string // "" with remove=true deletes the file
		prep    func(t *testing.T, w *fixtureWT)
	}{
		{name: "newline inside the recorded path", reason: ReasonGitdirUnreadable,
			content: func(r *fixtureRepo, w *fixtureWT) string {
				return filepath.Join(r.Root, ".worktrees", "a\nb", ".git") + "\n"
			}},
		{name: "NUL inside the recorded path", reason: ReasonGitdirUnreadable,
			content: func(r *fixtureRepo, w *fixtureWT) string {
				return filepath.Join(r.Root, ".worktrees", "a\x00b", ".git") + "\n"
			}},
		{name: "oversized gitdir file", reason: ReasonGitdirUnreadable,
			content: func(r *fixtureRepo, w *fixtureWT) string { return strings.Repeat("a", 1<<20) }},
		{name: "gitdir file missing", reason: ReasonGitdirUnreadable,
			prep: func(t *testing.T, w *fixtureWT) {
				if err := os.Remove(filepath.Join(w.AdminDir, "gitdir")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "gitdir is a directory", reason: ReasonGitdirUnreadable,
			prep: func(t *testing.T, w *fixtureWT) {
				p := filepath.Join(w.AdminDir, "gitdir")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "gitdir is a symlink", reason: ReasonGitdirUnreadable,
			prep: func(t *testing.T, w *fixtureWT) {
				p := filepath.Join(w.AdminDir, "gitdir")
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(w.R.Tmp, "gitdir-target")
				writeFile(t, target, string(data))
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "dot-dot component", reason: ReasonGitdirInvalid,
			content: func(r *fixtureRepo, w *fixtureWT) string {
				return filepath.Join(r.Root, ".worktrees") + "/../.worktrees/" + w.Name + "/.git\n"
			}},
		{name: "non-canonical dot component", reason: ReasonGitdirInvalid,
			content: func(r *fixtureRepo, w *fixtureWT) string {
				return filepath.Join(r.Root, ".worktrees") + "/./" + w.Name + "/.git\n"
			}},
		{name: "doubled slash", reason: ReasonGitdirInvalid,
			content: func(r *fixtureRepo, w *fixtureWT) string {
				return filepath.Join(r.Root, ".worktrees") + "//" + w.Name + "/.git\n"
			}},
		{name: "does not end in .git", reason: ReasonGitdirInvalid,
			content: func(r *fixtureRepo, w *fixtureWT) string {
				return filepath.Join(r.Root, ".worktrees", w.Name, "notgit") + "\n"
			}},
		{name: "garbage text", reason: ReasonGitdirInvalid,
			content: func(r *fixtureRepo, w *fixtureWT) string { return "garbage\n" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			if tc.content != nil {
				writeFile(t, filepath.Join(w.AdminDir, "gitdir"), tc.content(r, w))
			}
			if tc.prep != nil {
				tc.prep(t, w)
			}

			checkUnmapped(t, mapEntry(r.Repo(), w.AdminName), w, tc.reason)
		})
	}
}

func TestMapEntryDotGitFileProblemsAreUnmappedAsDotgitInvalid(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, w *fixtureWT)
	}{
		{"missing", func(t *testing.T, w *fixtureWT) {
			if err := os.Remove(filepath.Join(w.Path, ".git")); err != nil {
				t.Fatal(err)
			}
		}},
		{"garbage content without the gitdir prefix", func(t *testing.T, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, ".git"), "hello\n")
		}},
		{"a directory (a nested repository)", func(t *testing.T, w *fixtureWT) {
			p := filepath.Join(w.Path, ".git")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"non-canonical path", func(t *testing.T, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, ".git"), "gitdir: "+w.AdminDir+"/../"+w.AdminName+"\n")
		}},
		{"newline inside the path", func(t *testing.T, w *fixtureWT) {
			writeFile(t, filepath.Join(w.Path, ".git"), "gitdir: "+w.AdminDir+"\nextra\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			tc.setup(t, w)

			checkUnmapped(t, mapEntry(r.Repo(), w.AdminName), w, ReasonDotgitInvalid)
		})
	}
}

func TestMapEntryCommondirMustResolveToTheLocalCommonDir(t *testing.T) {
	for _, translated := range []bool{false, true} {
		name := "local"
		if translated {
			name = "translated"
		}
		t.Run(name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			if translated {
				rewriteAsForeign(t, w, foreignRootFor(r, "workspace"))
			}
			other := newRepo(t, "otherrepo")
			// Without the commondir check git would read other's refs.
			writeFile(t, filepath.Join(w.AdminDir, "commondir"), other.CommonDir+"\n")

			checkUnmapped(t, mapEntry(r.Repo(), w.AdminName), w, ReasonCommondirMismatch)
		})
	}
}

func TestMapEntryRelativeCommondirResolvingToTheCommonDirStaysMapped(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	writeFile(t, filepath.Join(w.AdminDir, "commondir"), "../..\n")
	// and the absolute spelling of the same directory
	absolute := mapEntry(r.Repo(), w.AdminName)
	if want := w.wantLocal(); absolute != want {
		t.Fatalf("relative commondir: mapEntry = %+v, want %+v", absolute, want)
	}
	writeFile(t, filepath.Join(w.AdminDir, "commondir"), r.CommonDir+"\n")
	if got := mapEntry(r.Repo(), w.AdminName); got != w.wantLocal() {
		t.Errorf("absolute commondir: mapEntry = %+v, want %+v", got, w.wantLocal())
	}
}

func TestMapEntryMissingCommondirIsNotMapped(t *testing.T) {
	// Default deny: an unreadable commondir can never be shown to agree with
	// the local common dir.
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	if err := os.Remove(filepath.Join(w.AdminDir, "commondir")); err != nil {
		t.Fatal(err)
	}

	checkUnmapped(t, mapEntry(r.Repo(), w.AdminName), w, ReasonCommondirMismatch)
}
