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

// =====================================================================
// AttachWorktree (ticket #688, closing #718 item 2): `cenci pipeline
// worktree <id> --attach PATH` reuse mode. Real `git worktree add` fixtures
// throughout, matching this file's own no-fakes convention (see file header)
// -- the porcelain membership check is exercised against real `git worktree
// list --porcelain` output, never a scripted/faked one.
//
// RED: AttachWorktree, WorktreeOpts.AttachPath, ErrWorktreeNotFound, and
// ErrWorktreeNotAttachable do not exist yet -- every test below is a compile
// error today (undefined symbols / unknown struct field), which is the
// expected RED signal for a not-yet-created verb (mirrors this ticket's
// adopt_test.go, whose cases instead fail as assertions since Run already
// exists to compile against).
// =====================================================================

// gitWorktreeAdd creates a worktree at dir on a NEW branch (deliberately not
// matching the feature/<id>-<slug> naming CreateWorktree uses -- Item 2's
// "non-standard branch" case is the whole point of --attach), via real `git
// worktree add`.
func gitWorktreeAdd(t *testing.T, repoRoot, dir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-q", dir, "-b", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add %s -b %s: %v\n%s", dir, branch, err, out)
	}
}

// gitWorktreeAddDetached creates a worktree at dir checked out at sha with NO
// branch (detached HEAD) -- Item 2's detached-HEAD safety-gate case.
func gitWorktreeAddDetached(t *testing.T, repoRoot, dir, sha string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-q", "--detach", dir, sha)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add --detach %s %s: %v\n%s", dir, sha, err, out)
	}
}

// -- case 14: attach a real non-standard worktree on a non-feature/ branch --

func TestAttachWorktree_NonStandardBranch_ReturnsAndPersistsBranchAndPath(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	worktreeDir := filepath.Join(t.TempDir(), "external-worktree")
	gitWorktreeAdd(t, repoRoot, worktreeDir, "custom-work")

	got, warnings, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: worktreeDir})
	if err != nil {
		t.Fatalf("AttachWorktree: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for a first-time attach", warnings)
	}
	wantPath, evalErr := filepath.EvalSymlinks(worktreeDir)
	if evalErr != nil {
		t.Fatalf("EvalSymlinks(worktreeDir): %v", evalErr)
	}
	if got.WorktreePath != wantPath {
		t.Errorf("State.WorktreePath = %q, want %q", got.WorktreePath, wantPath)
	}
	if got.Branch != "custom-work" {
		t.Errorf("State.Branch = %q, want %q (derived from git, a non-feature/ branch)", got.Branch, "custom-work")
	}

	reloaded, rerr := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
	if rerr != nil {
		t.Fatalf("GetArtifacts after AttachWorktree: %v", rerr)
	}
	if reloaded.WorktreePath != wantPath {
		t.Errorf("persisted State.WorktreePath = %q, want %q", reloaded.WorktreePath, wantPath)
	}
	if reloaded.Branch != "custom-work" {
		t.Errorf("persisted State.Branch = %q, want %q", reloaded.Branch, "custom-work")
	}
}

// -- case 15: relative --attach path resolves against the main root --------

func TestAttachWorktree_RelativePath_ResolvesAgainstMainRoot(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	worktreeDir := filepath.Join(repoRoot, "ext", "rel-worktree")
	gitWorktreeAdd(t, repoRoot, worktreeDir, "custom-rel")

	got, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: filepath.Join("ext", "rel-worktree")})
	if err != nil {
		t.Fatalf("AttachWorktree (relative path): %v", err)
	}
	wantPath, evalErr := filepath.EvalSymlinks(worktreeDir)
	if evalErr != nil {
		t.Fatalf("EvalSymlinks(worktreeDir): %v", evalErr)
	}
	if got.WorktreePath != wantPath {
		t.Errorf("State.WorktreePath = %q, want %q (relative --attach path resolved against the main checkout root)", got.WorktreePath, wantPath)
	}
}

// -- case 16: plain directory inside the repo -> ErrWorktreeNotFound -------

func TestAttachWorktree_PlainDirectory_ReturnsErrWorktreeNotFound(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	plainDir := filepath.Join(repoRoot, "just-a-dir")
	if err := os.MkdirAll(plainDir, 0o755); err != nil {
		t.Fatalf("mkdir plain dir: %v", err)
	}

	_, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: plainDir})
	if err == nil {
		t.Fatal("attach a plain (non-worktree) directory: want an error, got nil")
	}
	if !errors.Is(err, ErrWorktreeNotFound) {
		t.Errorf("error = %v, want errors.Is(_, ErrWorktreeNotFound)", err)
	}
	if !strings.Contains(err.Error(), "is not a registered worktree of") {
		t.Errorf("error = %q, want it to name the specific not-registered failure", err.Error())
	}
}

// -- case 17: non-existent path -> ErrWorktreeNotFound, nothing created ----

func TestAttachWorktree_NonExistentPath_ReturnsErrWorktreeNotFoundAndCreatesNothing(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	missing := filepath.Join(repoRoot, "does-not-exist")

	_, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: missing})
	if err == nil {
		t.Fatal("attach a non-existent path: want an error, got nil")
	}
	if !errors.Is(err, ErrWorktreeNotFound) {
		t.Errorf("error = %v, want errors.Is(_, ErrWorktreeNotFound)", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Errorf("expected nothing created at %s, stat err = %v", missing, statErr)
	}
}

// -- case 18: main checkout -> ErrWorktreeNotAttachable ---------------------

func TestAttachWorktree_MainCheckout_ReturnsErrWorktreeNotAttachable(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()

	_, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: repoRoot})
	if err == nil {
		t.Fatal("attach the main checkout: want an error, got nil")
	}
	if !errors.Is(err, ErrWorktreeNotAttachable) {
		t.Errorf("error = %v, want errors.Is(_, ErrWorktreeNotAttachable)", err)
	}
	if !strings.Contains(err.Error(), "main checkout") {
		t.Errorf("error = %q, want it to name the main checkout", err.Error())
	}
}

// -- case 19: detached-HEAD worktree -> ErrWorktreeNotAttachable -----------

func TestAttachWorktree_DetachedHead_ReturnsErrWorktreeNotAttachable(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	sha := gitHeadSha(t, repoRoot)
	worktreeDir := filepath.Join(t.TempDir(), "detached-worktree")
	gitWorktreeAddDetached(t, repoRoot, worktreeDir, sha)

	_, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: worktreeDir})
	if err == nil {
		t.Fatal("attach a detached-HEAD worktree: want an error, got nil")
	}
	if !errors.Is(err, ErrWorktreeNotAttachable) {
		t.Errorf("error = %v, want errors.Is(_, ErrWorktreeNotAttachable)", err)
	}
	if !strings.Contains(err.Error(), "detached HEAD") {
		t.Errorf("error = %q, want it to name detached HEAD", err.Error())
	}
}

// -- case 20: a worktree registered in a different repo -> ErrWorktreeNotFound

func TestAttachWorktree_WorktreeOfDifferentRepo_ReturnsErrWorktreeNotFound(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()

	foreignRepo := t.TempDir()
	initGitRepoWithCommit(t, foreignRepo)
	foreignWorktree := filepath.Join(t.TempDir(), "foreign-worktree")
	gitWorktreeAdd(t, foreignRepo, foreignWorktree, "foreign-branch")

	_, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: foreignWorktree})
	if err == nil {
		t.Fatal("attach a worktree registered in a different repo: want an error, got nil")
	}
	if !errors.Is(err, ErrWorktreeNotFound) {
		t.Errorf("error = %v, want errors.Is(_, ErrWorktreeNotFound) (membership check against THIS repo's porcelain list, not a path-shape heuristic)", err)
	}
}

// -- case 21: attaching over a different recorded worktree -> succeeds with
// the replacement warning ----------------------------------------------

func TestAttachWorktree_ReplacesDifferentRecordedWorktree_WithWarning(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()

	firstDir := filepath.Join(t.TempDir(), "first-worktree")
	gitWorktreeAdd(t, repoRoot, firstDir, "first-branch")
	if _, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: firstDir}); err != nil {
		t.Fatalf("first AttachWorktree: %v", err)
	}
	firstResolved, evalErr := filepath.EvalSymlinks(firstDir)
	if evalErr != nil {
		t.Fatalf("EvalSymlinks(firstDir): %v", evalErr)
	}

	secondDir := filepath.Join(t.TempDir(), "second-worktree")
	gitWorktreeAdd(t, repoRoot, secondDir, "second-branch")

	got, warnings, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: secondDir})
	if err != nil {
		t.Fatalf("second AttachWorktree: %v", err)
	}
	secondResolved, evalErr := filepath.EvalSymlinks(secondDir)
	if evalErr != nil {
		t.Fatalf("EvalSymlinks(secondDir): %v", evalErr)
	}
	if got.WorktreePath != secondResolved {
		t.Errorf("State.WorktreePath = %q, want %q", got.WorktreePath, secondResolved)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one replacement warning", warnings)
	}
	if !strings.Contains(warnings[0], firstResolved) || !strings.Contains(warnings[0], secondResolved) {
		t.Errorf("warnings[0] = %q, want it to name both the old (%q) and new (%q) worktree", warnings[0], firstResolved, secondResolved)
	}
}

// -- case 22: attaching the same path twice -> idempotent, no replacement
// warning -------------------------------------------------------------

func TestAttachWorktree_SamePathTwice_IdempotentNoReplacementWarning(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	worktreeDir := filepath.Join(t.TempDir(), "idempotent-worktree")
	gitWorktreeAdd(t, repoRoot, worktreeDir, "idempotent-branch")

	if _, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: worktreeDir}); err != nil {
		t.Fatalf("first AttachWorktree: %v", err)
	}
	got, warnings, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: worktreeDir})
	if err != nil {
		t.Fatalf("second (idempotent) AttachWorktree: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for re-attaching the same path (idempotent success)", warnings)
	}
	wantPath, evalErr := filepath.EvalSymlinks(worktreeDir)
	if evalErr != nil {
		t.Fatalf("EvalSymlinks(worktreeDir): %v", evalErr)
	}
	if got.WorktreePath != wantPath || got.Branch != "idempotent-branch" {
		t.Errorf("State after idempotent re-attach = %+v, want WorktreePath=%q Branch=%q", got, wantPath, "idempotent-branch")
	}
}

// -- security fix-up (review of ticket #688): --repo cannot disguise the
// real main checkout as an attachable target --------------------------------

// TestAttachWorktree_MainCheckoutDisguisedByRepoFlag_StillReturnsErrWorktreeNotAttachable
// covers the security-review finding that the "never attach the main
// checkout" guard was bypassable: resolveMainRoot returns o.RepoRoot
// VERBATIM when the --repo test hook is set, so pointing --repo at a
// DIFFERENT, linked worktree while still targeting the true main checkout
// via --attach must not slip past the guard. `git worktree list
// --porcelain` is repo-wide regardless of which worktree it's invoked
// against, so records[0].Path (the main worktree itself, per git's own
// documented ordering) must still be the identity this comparison trusts.
func TestAttachWorktree_MainCheckoutDisguisedByRepoFlag_StillReturnsErrWorktreeNotAttachable(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()

	disguiseWorktree := filepath.Join(t.TempDir(), "disguise-worktree")
	gitWorktreeAdd(t, repoRoot, disguiseWorktree, "disguise-branch")

	// --repo points at the linked worktree (a DIFFERENT path than the real
	// main checkout), while --attach targets the TRUE main checkout itself.
	_, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: disguiseWorktree, StateDir: stateDir, AttachPath: repoRoot})
	if err == nil {
		t.Fatal("attach the true main checkout while --repo disguises it via a different linked worktree: want an error, got nil")
	}
	if !errors.Is(err, ErrWorktreeNotAttachable) {
		t.Errorf("error = %v, want errors.Is(_, ErrWorktreeNotAttachable) (the main-checkout guard must not be bypassable via --repo)", err)
	}
	if !strings.Contains(err.Error(), "main checkout") {
		t.Errorf("error = %q, want it to name the main checkout", err.Error())
	}
}

// -- security fix-up (review of ticket #688): unsafe branch names rejected --

// TestAttachWorktree_UnsafeBranchName_ReturnsErrWorktreeNotAttachable covers
// the security-review finding that match.Branch (parsed straight from `git
// worktree list --porcelain` output) was never validated before being
// recorded/returned, even though flow/skills/implement/phases/phase-9-pr.md
// later interpolates it, unquoted, into `git -C <path> push -u origin
// <branch>`. Git permits a semicolon in a real branch name (verified
// against real git while writing this test), so a worktree genuinely
// checked out on such a branch must be rejected here rather than silently
// passed through.
func TestAttachWorktree_UnsafeBranchName_ReturnsErrWorktreeNotAttachable(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	worktreeDir := filepath.Join(t.TempDir(), "unsafe-branch-worktree")
	gitWorktreeAdd(t, repoRoot, worktreeDir, "weird;rm-rf")

	_, _, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: worktreeDir})
	if err == nil {
		t.Fatal("attach a worktree checked out on an unsafe branch name: want an error, got nil")
	}
	if !errors.Is(err, ErrWorktreeNotAttachable) {
		t.Errorf("error = %v, want errors.Is(_, ErrWorktreeNotAttachable)", err)
	}
	if !strings.Contains(err.Error(), "not a safe name") {
		t.Errorf("error = %q, want it to name the unsafe-branch-name rejection", err.Error())
	}
}

// TestValidateBranchName_LeadingDash_Rejected is a direct unit test at the
// package boundary for a branch shape real git itself refuses to let any
// fixture actually check out (`git branch -- -foo` fails with "'-foo' is
// not a valid branch name", verified while writing this test) -- so the
// only way to exercise this specific rejection is to call the validation
// helper directly, per the defensive-validation fallback this finding calls
// for when a real git-legal fixture isn't obtainable for every unsafe case.
func TestValidateBranchName_LeadingDash_Rejected(t *testing.T) {
	if err := validateBranchName("-foo"); err == nil {
		t.Error("validateBranchName(\"-foo\"): want an error for a leading-dash branch name, got nil")
	}
}

// -- silent-failure fix-up (review of ticket #688): a corrupted prior state
// file surfaces an advisory warning instead of being silently swallowed ----

// TestAttachWorktree_CorruptedPriorState_SucceedsWithAdvisoryWarning covers
// the silent-failure finding that GetArtifacts' error was discarded before
// deciding whether to emit the "replaced tracked worktree" warning:
// loadState returns (State{Stage: StageNew}, nil) -- no error -- for the
// normal "no state file yet" case, so a non-nil error here means something
// genuinely went wrong (e.g. a corrupted/unreadable state file), which must
// not be silently treated the same as "nothing was there."
//
// GetArtifacts' own doc comment calls it "a point-in-time read" -- unlike
// SetArtifacts, it does not hold the per-ticket lock -- so it can genuinely
// observe a transient state a lock-protected reader moments later would not
// (e.g. a concurrent, legitimate writer repairing/replacing the file in
// between). This test reproduces exactly that window deterministically,
// without any wall-clock timing (matching lock_test.go's own injected-seam
// convention, "asserts on outcome... via the injected clock rather than
// wall-clock timing"): AttachWorktree's GetArtifacts call runs first and
// unconditionally observes the corrupted seed below; only THEN, inside the
// flockFn hook -- which fires when SetArtifacts (moments later, holding the
// lock) makes its own read -- do we repair the file, so SetArtifacts' own
// read succeeds against valid content. This proves the advisory-warning
// branch this fix adds is not just theoretical: it fires, and the overall
// operation still succeeds, exactly as the finding requires.
func TestAttachWorktree_CorruptedPriorState_SucceedsWithAdvisoryWarning(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	stateDir := t.TempDir()
	worktreeDir := filepath.Join(t.TempDir(), "corrupted-state-worktree")
	gitWorktreeAdd(t, repoRoot, worktreeDir, "corrupted-state-branch")

	statePath := filepath.Join(stateDir, "42.json")
	if err := os.WriteFile(statePath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("seed corrupted state file at %s: %v", statePath, err)
	}

	originalFlockFn := flockFn
	defer func() { flockFn = originalFlockFn }()
	flockFn = func(fd int, how int) error {
		// Runs synchronously inside SetArtifacts' withLock, strictly AFTER
		// AttachWorktree's earlier, unlocked GetArtifacts call above has
		// already run (Go executes both sequentially in the same
		// goroutine) -- so GetArtifacts is guaranteed to have already
		// observed the corrupted seed by the time this repair happens.
		if err := os.WriteFile(statePath, []byte(`{"schemaVersion":2,"id":"42","stage":"new"}`), 0o644); err != nil {
			t.Fatalf("repair state file: %v", err)
		}
		return originalFlockFn(fd, how)
	}

	got, warnings, err := AttachWorktree(WorktreeOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, AttachPath: worktreeDir})
	if err != nil {
		t.Fatalf("AttachWorktree with a corrupted prior state file: want success, got: %v", err)
	}
	wantPath, evalErr := filepath.EvalSymlinks(worktreeDir)
	if evalErr != nil {
		t.Fatalf("EvalSymlinks(worktreeDir): %v", evalErr)
	}
	if got.WorktreePath != wantPath {
		t.Errorf("State.WorktreePath = %q, want %q (the new artifacts must still be recorded)", got.WorktreePath, wantPath)
	}

	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one advisory warning", warnings)
	}
	if !strings.Contains(warnings[0], "could not read prior pipeline state") {
		t.Errorf("warnings[0] = %q, want it to name the unreadable prior state, not silently proceed with no warning", warnings[0])
	}
	if strings.Contains(warnings[0], "replaced tracked worktree") {
		t.Errorf("warnings[0] = %q, must not also emit the (untrustworthy) replacement warning off of a corrupted read", warnings[0])
	}
}

// -- case 23: sentinel identity/distinctness --------------------------------

func TestErrWorktreeNotFoundAndNotAttachable_AreDistinctAndDetectableViaErrorsIs(t *testing.T) {
	if ErrWorktreeNotFound == nil {
		t.Fatal("ErrWorktreeNotFound must not be nil")
	}
	if ErrWorktreeNotAttachable == nil {
		t.Fatal("ErrWorktreeNotAttachable must not be nil")
	}
	if !errors.Is(ErrWorktreeNotFound, ErrWorktreeNotFound) {
		t.Error("ErrWorktreeNotFound must satisfy errors.Is against itself")
	}
	if !errors.Is(ErrWorktreeNotAttachable, ErrWorktreeNotAttachable) {
		t.Error("ErrWorktreeNotAttachable must satisfy errors.Is against itself")
	}
	if errors.Is(ErrWorktreeNotFound, ErrWorktreeNotAttachable) {
		t.Error("ErrWorktreeNotFound must be distinct from ErrWorktreeNotAttachable")
	}
	if errors.Is(ErrWorktreeNotFound, ErrWorktreeExists) {
		t.Error("ErrWorktreeNotFound must be distinct from the unrelated ErrWorktreeExists sentinel")
	}
	if errors.Is(ErrWorktreeNotAttachable, ErrWorktreeExists) {
		t.Error("ErrWorktreeNotAttachable must be distinct from the unrelated ErrWorktreeExists sentinel")
	}
}
