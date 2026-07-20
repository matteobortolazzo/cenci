package pipeline

// Integration tests for worktree create + cleanup (ticket #559), against
// REAL temp git repos and actual `git worktree add`/`remove` (no fakes --
// the plan calls for exercising the real git plumbing). Package-boundary
// ("white box") tests, matching store_test.go's own convention.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepoWithCommit mirrors store_test.go's initGitRepo, plus an
// initial commit -- `git worktree add -b <branch>` needs a HEAD to branch
// from for the "already exists" collision scenarios below to be meaningful
// (an empty repo has no branches to collide with).
func initGitRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-q", "-m", "init")
}

func branchExists(t *testing.T, repoRoot, branch string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--list", branch).Output()
	if err != nil {
		t.Fatalf("git branch --list %s: %v", branch, err)
	}
	return strings.TrimSpace(string(out)) != ""
}

// -- success path: dir + branch created, state updated ---------------------

func TestCreateWorktree_Success_CreatesDirAndBranchAndUpdatesState(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	o := WorktreeOpts{ID: "42", Slug: "add-thing", RepoRoot: repoRoot, StateDir: stateDir}

	got, err := CreateWorktree(o)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	wantDir := filepath.Join(repoRoot, ".worktrees", "42-add-thing")
	if info, statErr := os.Stat(wantDir); statErr != nil || !info.IsDir() {
		t.Errorf("expected worktree dir at %s, stat err = %v", wantDir, statErr)
	}
	wantBranch := "feature/42-add-thing"
	if !branchExists(t, repoRoot, wantBranch) {
		t.Errorf("expected branch %q to exist after CreateWorktree", wantBranch)
	}
	if got.WorktreePath != wantDir {
		t.Errorf("returned State.WorktreePath = %q, want %q", got.WorktreePath, wantDir)
	}
	if got.Branch != wantBranch {
		t.Errorf("returned State.Branch = %q, want %q", got.Branch, wantBranch)
	}

	// Reload from disk to confirm the artifacts actually persisted, not
	// just present on the in-memory return value.
	reloaded, err := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("GetArtifacts after CreateWorktree: %v", err)
	}
	if reloaded.WorktreePath != wantDir {
		t.Errorf("persisted State.WorktreePath = %q, want %q", reloaded.WorktreePath, wantDir)
	}
	if reloaded.Branch != wantBranch {
		t.Errorf("persisted State.Branch = %q, want %q", reloaded.Branch, wantBranch)
	}
}

// -- already-exists path: ErrWorktreeExists via errors.Is ------------------

func TestCreateWorktree_AlreadyExists_ReturnsErrWorktreeExists(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	o := WorktreeOpts{ID: "42", Slug: "add-thing", RepoRoot: repoRoot, StateDir: stateDir}

	if _, err := CreateWorktree(o); err != nil {
		t.Fatalf("first CreateWorktree: %v", err)
	}

	_, err := CreateWorktree(o)
	if err == nil {
		t.Fatal("second CreateWorktree for the same id+slug: want an error, got nil")
	}
	if !errors.Is(err, ErrWorktreeExists) {
		t.Errorf("second CreateWorktree error = %v, want errors.Is(_, ErrWorktreeExists)", err)
	}
}

// -- cleanup removes both tree and branch -----------------------------------

func TestCleanupWorktree_RemovesTreeAndBranch(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	o := WorktreeOpts{ID: "42", Slug: "add-thing", RepoRoot: repoRoot, StateDir: stateDir}

	if _, err := CreateWorktree(o); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := CleanupWorktree(o); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}

	wantDir := filepath.Join(repoRoot, ".worktrees", "42-add-thing")
	if _, statErr := os.Stat(wantDir); !os.IsNotExist(statErr) {
		t.Errorf("expected worktree dir removed at %s, stat err = %v", wantDir, statErr)
	}
	if branchExists(t, repoRoot, "feature/42-add-thing") {
		t.Error("expected branch feature/42-add-thing removed after CleanupWorktree, but it still exists")
	}
}

// -- cleanup no-ops cleanly when the branch was never created --------------

func TestCleanupWorktree_BranchNeverCreated_NoOp(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	o := WorktreeOpts{ID: "42", Slug: "never-created", RepoRoot: repoRoot, StateDir: stateDir}

	// Deliberately never call CreateWorktree: the dir/branch never existed,
	// simulating a rollback triggered before the branch was created.
	if err := CleanupWorktree(o); err != nil {
		t.Fatalf("CleanupWorktree on a never-created worktree/branch: want a successful no-op, got: %v", err)
	}
}

// -- slug validation: path traversal rejected before any git command -------

// recordingCommand installs a fake `command` seam that records every
// invocation and returns success, so a test can assert exactly which (if
// any) git subcommands ran.
func recordingCommand(t *testing.T) *[]ghCall {
	t.Helper()
	var calls []ghCall
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, ghCall{name: name, args: args})
		return nil, nil
	}
	t.Cleanup(func() { command = original })
	return &calls
}

func TestCreateWorktree_SlugPathTraversal_RejectedBeforeAnyGitCommand(t *testing.T) {
	calls := recordingCommand(t)
	stateDir := t.TempDir()
	o := WorktreeOpts{ID: "42", Slug: "../99-foo", RepoRoot: t.TempDir(), StateDir: stateDir}

	_, err := CreateWorktree(o)
	if err == nil {
		t.Fatal("CreateWorktree with a path-traversal slug: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid worktree slug") {
		t.Errorf("error = %q, want it to name the invalid-slug validation failure", err.Error())
	}
	if len(*calls) != 0 {
		t.Errorf("git/gh calls = %v, want none (the slug gate must fail before any git command runs)", *calls)
	}
}

func TestCleanupWorktree_SlugPathTraversal_RejectedBeforeAnyGitCommand(t *testing.T) {
	calls := recordingCommand(t)
	stateDir := t.TempDir()
	o := WorktreeOpts{ID: "42", Slug: "../99-foo", RepoRoot: t.TempDir(), StateDir: stateDir}

	err := CleanupWorktree(o)
	if err == nil {
		t.Fatal("CleanupWorktree with a path-traversal slug: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid worktree slug") {
		t.Errorf("error = %q, want it to name the invalid-slug validation failure", err.Error())
	}
	if len(*calls) != 0 {
		t.Errorf("git worktree-remove/branch-delete calls = %v, want none (the slug gate must fail before any git command runs)", *calls)
	}
}

// -- sentinel identity (#412: a direct unit test at the package boundary) --

func TestErrWorktreeExists_IsDistinctAndDetectableViaErrorsIs(t *testing.T) {
	if ErrWorktreeExists == nil {
		t.Fatal("ErrWorktreeExists must not be nil")
	}
	if !errors.Is(ErrWorktreeExists, ErrWorktreeExists) {
		t.Error("ErrWorktreeExists must satisfy errors.Is against itself")
	}
	if errors.Is(ErrWorktreeExists, ErrLockContention) {
		t.Error("ErrWorktreeExists must be distinct from the unrelated ErrLockContention sentinel")
	}
}
