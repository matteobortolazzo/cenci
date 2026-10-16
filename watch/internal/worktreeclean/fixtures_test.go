package worktreeclean

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Real-git fixtures shared by every *_test.go in this package. Nothing here
// mocks git: worktrees are created by the real binary, and the "container side"
// of a translated worktree is produced by rewriting git's own pointer files to a
// path that is never created.

// oldCommitDate (2020-01-01T00:00:00Z) is the author/committer date every
// fixture commit gets unless a test asks for another, so commit dates are
// deterministic and older than any mtime a test sets.
const oldCommitDate int64 = 1577836800

// repoLocationVars are the environment variables that relocate a git
// repository; the fixtures clear them so an ambient value (e.g. when the suite
// runs inside a git hook) cannot redirect a fixture command.
var repoLocationVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
}

// isolateGit keeps real-git fixtures independent of the developer's gitconfig
// and ambient repo-location variables.
func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for the real-git fixtures: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Fixture")
	t.Setenv("GIT_AUTHOR_EMAIL", "fixture@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Fixture")
	t.Setenv("GIT_COMMITTER_EMAIL", "fixture@example.invalid")
	for _, k := range append([]string{"GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE", "GIT_OPTIONAL_LOCKS"}, repoLocationVars...) {
		unsetenv(t, k)
	}
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setMtime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// tryGit runs `git -C dir args...` with both dates pinned to date.
func tryGit(dir string, date int64, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	stamp := fmt.Sprintf("@%d +0000", date)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %v in %s: %w\n%s", args, dir, err, stderr.String())
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func runGitAt(t *testing.T, dir string, date int64, args ...string) string {
	t.Helper()
	out, err := tryGit(dir, date, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runGitAt(t, dir, oldCommitDate, args...)
}

// fixtureRepo is a real main checkout. Tmp is the (symlink-resolved) temp dir
// that holds it, its origin and any "foreign" paths a test rewrites to.
type fixtureRepo struct {
	Tmp       string
	Root      string
	CommonDir string
}

// Repo is the Repo value ResolveRepo is expected to return for this fixture.
func (r *fixtureRepo) Repo() Repo { return Repo{MainRoot: r.Root, CommonDir: r.CommonDir} }

func (r *fixtureRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	return runGit(t, r.Root, args...)
}

// newRepo creates a real repository at <tmp>/<dirName> (dirName may hold
// several components) with one commit holding tracked.txt and a .gitignore that
// ignores ignored/ and *.log.
func newRepo(t *testing.T, dirName string) *fixtureRepo {
	t.Helper()
	isolateGit(t)
	tmp := realPath(t, t.TempDir())
	root := filepath.Join(tmp, filepath.FromSlash(dirName))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(root, "tracked.txt"), "tracked\n")
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored/\n*.log\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "init")
	return &fixtureRepo{Tmp: tmp, Root: root, CommonDir: filepath.Join(root, ".git")}
}

// newBareOrigin gives r an `origin` remote (a bare repo next to it) holding
// main, so refs/remotes/origin/main exists.
func newBareOrigin(t *testing.T, r *fixtureRepo) string {
	t.Helper()
	origin := filepath.Join(r.Tmp, "origin.git")
	runGit(t, r.Tmp, "init", "-q", "--bare", "-b", "main", origin)
	r.git(t, "remote", "add", "origin", origin)
	r.git(t, "push", "-q", "origin", "main")
	return origin
}

// fixtureWT is a real linked worktree.
type fixtureWT struct {
	R         *fixtureRepo
	Name      string // basename of the worktree dir
	Branch    string // empty for a detached HEAD
	Path      string // where the worktree really lives on this machine
	AdminName string
	AdminDir  string
	// Registered is the path git recorded for the worktree; it equals Path
	// until rewriteAsForeign moves the record to another root.
	Registered string
}

// addWorktree creates <root>/.worktrees/<name> on a new branch (detached at
// main's HEAD when branch is empty).
func addWorktree(t *testing.T, r *fixtureRepo, name, branch string) *fixtureWT {
	t.Helper()
	return addWorktreeAt(t, r, filepath.Join(r.Root, ".worktrees", name), branch)
}

func addWorktreeAt(t *testing.T, r *fixtureRepo, path, branch string) *fixtureWT {
	t.Helper()
	args := []string{"worktree", "add", "-q"}
	if branch == "" {
		args = append(args, "--detach", path)
	} else {
		args = append(args, "-b", branch, path)
	}
	r.git(t, args...)
	return loadWorktree(t, r, path, branch)
}

// addOrphanWorktree creates a worktree whose branch is unborn, skipping the
// test on a git too old for `worktree add --orphan`.
func addOrphanWorktree(t *testing.T, r *fixtureRepo, name, branch string) *fixtureWT {
	t.Helper()
	path := filepath.Join(r.Root, ".worktrees", name)
	if _, err := tryGit(r.Root, oldCommitDate, "worktree", "add", "-q", "--orphan", "-b", branch, path); err != nil {
		t.Skipf("git without `worktree add --orphan`: %v", err)
	}
	return loadWorktree(t, r, path, branch)
}

func loadWorktree(t *testing.T, r *fixtureRepo, path, branch string) *fixtureWT {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	admin, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		t.Fatalf("%s/.git = %q, want a gitdir: pointer", path, data)
	}
	adminName := filepath.Base(admin)
	if want := filepath.Join(r.CommonDir, "worktrees", adminName); admin != want {
		t.Fatalf("worktree admin dir = %q, want %q", admin, want)
	}
	return &fixtureWT{
		R: r, Name: filepath.Base(path), Branch: branch, Path: path,
		AdminName: adminName, AdminDir: admin, Registered: path,
	}
}

func (w *fixtureWT) git(t *testing.T, args ...string) string {
	t.Helper()
	return runGit(t, w.Path, args...)
}

func (w *fixtureWT) head(t *testing.T) string {
	t.Helper()
	return w.git(t, "rev-parse", "HEAD")
}

// commit writes file, stages it and commits with the given commit date.
func (w *fixtureWT) commit(t *testing.T, file, content string, date int64) {
	t.Helper()
	writeFile(t, filepath.Join(w.Path, file), content)
	w.git(t, "add", file)
	runGitAt(t, w.Path, date, "commit", "-q", "-m", "change "+file)
}

// push publishes the branch to origin and sets it as upstream.
func (w *fixtureWT) push(t *testing.T) {
	t.Helper()
	w.git(t, "push", "-q", "-u", "origin", w.Branch)
}

// wantLocal is the Entry a correct mapping yields for a worktree registered at
// its real location.
func (w *fixtureWT) wantLocal() Entry {
	return Entry{
		AdminName: w.AdminName, AdminDir: w.AdminDir, Name: w.Name,
		RegisteredPath: w.Path, LocalPath: w.Path, Mapping: MappingLocal,
	}
}

// wantTranslated is the Entry a correct mapping yields after rewriteAsForeign.
func (w *fixtureWT) wantTranslated() Entry {
	e := w.wantLocal()
	e.RegisteredPath = w.Registered
	e.Mapping = MappingTranslated
	return e
}

// rewritePreservingMtime replaces a file's content but keeps its mtime, so
// rewriting pointer files never perturbs the last-activity fixtures.
func rewritePreservingMtime(t *testing.T, path, content string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	setMtime(t, path, st.ModTime())
}

// rewriteAsForeign makes the worktree look like one created on the other side
// of the sandbox mount: the admin `gitdir` and the worktree's `.git` file both
// point under foreignRoot, which is never created. Afterwards it checks the
// fixture is still a working worktree through explicit --git-dir/--work-tree.
func rewriteAsForeign(t *testing.T, w *fixtureWT, foreignRoot string) {
	t.Helper()
	if _, err := os.Lstat(foreignRoot); !os.IsNotExist(err) {
		t.Fatalf("foreign root %s must not exist locally (Lstat err = %v)", foreignRoot, err)
	}
	w.Registered = filepath.Join(foreignRoot, ".worktrees", w.Name)
	rewritePreservingMtime(t, filepath.Join(w.AdminDir, "gitdir"),
		filepath.Join(w.Registered, ".git")+"\n")
	rewritePreservingMtime(t, filepath.Join(w.Path, ".git"),
		"gitdir: "+filepath.Join(foreignRoot, ".git", "worktrees", w.AdminName)+"\n")

	out, err := exec.Command("git", "--no-optional-locks", "--git-dir="+w.AdminDir, "--work-tree="+w.Path,
		"status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("rewritten fixture no longer works through --git-dir/--work-tree: %v\n%s", err, out)
	}
	if _, err := os.Lstat(foreignRoot); !os.IsNotExist(err) {
		t.Fatalf("something created the foreign root %s (Lstat err = %v)", foreignRoot, err)
	}
}

// ageAll sets the mtime of every file and directory under the roots (symlinks
// themselves are left alone) to when.
func ageAll(t *testing.T, when time.Time, roots ...string) {
	t.Helper()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			return os.Chtimes(p, when, when)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// snapshotTree records, for every entry under the roots, its type, mode, size,
// mtime and a content hash (a symlink target for symlinks), without following
// symlinks. Directory mtimes are included so a transient lock file that was
// created and removed still shows up.
func snapshotTree(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	snap := make(map[string]string)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			desc := fmt.Sprintf("mode=%v size=%d mtime=%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				desc += " -> " + target
			case info.Mode().IsRegular():
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(data)
				desc += " sha256=" + hex.EncodeToString(sum[:])
			}
			snap[p] = desc
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return snap
}

// diffSnapshots lists every path whose recorded state differs, appeared or
// vanished between two snapshots.
func diffSnapshots(before, after map[string]string) []string {
	var diffs []string
	for p, b := range before {
		if a, ok := after[p]; !ok {
			diffs = append(diffs, "removed: "+p)
		} else if a != b {
			diffs = append(diffs, fmt.Sprintf("changed: %s\n  before: %s\n  after:  %s", p, b, a))
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			diffs = append(diffs, "added: "+p)
		}
	}
	sort.Strings(diffs)
	return diffs
}

// entryFor returns the Enumerate entry for w. It exercises production
// Enumerate on purpose: Inspect tests start from the Entry a real caller gets.
func entryFor(t *testing.T, r *fixtureRepo, w *fixtureWT) Entry {
	t.Helper()
	_, entries, err := Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	for _, e := range entries {
		if e.AdminName == w.AdminName {
			return e
		}
	}
	t.Fatalf("Enumerate did not list admin entry %q: %+v", w.AdminName, entries)
	return Entry{}
}
