package pipeline

// Deterministic worktree create + cleanup (ticket #559): `.worktrees/<id>-
// <slug>` dir, `feature/<id>-<slug>` branch, created via `git -C <main-root>
// worktree add <dir> -b <branch>`, with <main-root> resolved the same way
// as resolveRepoRoot (store.go) so this always anchors on the MAIN
// checkout, never a linked worktree's own root. CreateWorktree records the
// resulting Branch + WorktreePath into the persisted pipeline State (via
// the artifacts API) and rolls back any partial state on failure.
// CleanupWorktree removes both the worktree directory (`git worktree
// remove`) and the branch (`git branch -D`) -- used only on the
// creation-failure rollback path, never on a "baseline gate failed" path
// (that path deliberately keeps the worktree/branch around for retry).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrWorktreeExists is returned when the target worktree directory or
// branch for <id>-<slug> already exists (errors.Is-detectable, rule #412).
var ErrWorktreeExists = errors.New("pipeline: worktree or branch already exists")

// slugPattern is the validation gate every --slug must pass before it is
// used to build a worktree dir/branch name, guarding against path traversal
// (e.g. a slug of "../99-foo") via a malformed slug. CreateWorktree is only
// incidentally protected against this today -- the resulting branch ref
// (`feature/<id>-<slug>`) happens to be rejected by git's own ref-format
// check -- but CleanupWorktree builds no ref at all and runs `git worktree
// remove --force` + `git branch -D` straight off the joined path, so a
// traversal slug there would silently act on a DIFFERENT ticket's
// worktree/branch. Mirrors idPattern's (store.go) role for <id>.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// validateSlug rejects any slug that doesn't match slugPattern (empty,
// containing "/" or ".", leading "-", etc.) before it is ever used to build
// a filesystem path or git ref.
func validateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("invalid worktree slug %q: must match ^[a-z0-9]+(?:-[a-z0-9]+)*$", slug)
	}
	return nil
}

// WorktreeOpts are the resolved inputs to CreateWorktree/CleanupWorktree.
// RepoRoot/StateDir mirror pipeline.Opts' own precedence for locating the
// persisted state file; RepoRoot additionally anchors where `git worktree
// add`/`remove`/`branch -D` run (resolved to the main checkout root the
// same way as resolveRepoRoot when RepoRoot is empty).
type WorktreeOpts struct {
	ID       string
	Slug     string
	RepoRoot string
	StateDir string
}

// resolveMainRoot mirrors resolveStatePath's own RepoRoot precedence: use
// repoRoot verbatim when set (a test/override hook), otherwise resolve the
// MAIN checkout root from the current working directory via
// resolveRepoRoot.
func resolveMainRoot(repoRoot string) (string, error) {
	if repoRoot != "" {
		return repoRoot, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return resolveRepoRoot(cwd)
}

// worktreeNames computes the deterministic dir + branch pair for id+slug:
// `.worktrees/<id>-<slug>` under mainRoot, and branch `feature/<id>-<slug>`.
func worktreeNames(mainRoot, id, slug string) (dir, branch string) {
	name := id + "-" + slug
	return filepath.Join(mainRoot, ".worktrees", name), "feature/" + name
}

// gitBranchExists reports whether branch exists in the repo at root.
func gitBranchExists(root, branch string) bool {
	out, err := command("git", "-C", root, "branch", "--list", branch)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// CreateWorktree creates `.worktrees/<id>-<slug>` and branch
// `feature/<id>-<slug>` via `git worktree add`, anchored at the main
// checkout root, records Branch + WorktreePath into the persisted pipeline
// state, and rolls back any partial state (dir and/or branch) on failure.
// Returns ErrWorktreeExists (errors.Is-detectable) when the target
// dir/branch already exists.
func CreateWorktree(o WorktreeOpts) (State, error) {
	if err := validateSlug(o.Slug); err != nil {
		return State{}, err
	}

	mainRoot, err := resolveMainRoot(o.RepoRoot)
	if err != nil {
		return State{}, fmt.Errorf("resolve main checkout root: %w", err)
	}

	dir, branch := worktreeNames(mainRoot, o.ID, o.Slug)

	if _, statErr := os.Stat(dir); statErr == nil {
		return State{}, fmt.Errorf("worktree dir %s already exists: %w", dir, ErrWorktreeExists)
	}
	if gitBranchExists(mainRoot, branch) {
		return State{}, fmt.Errorf("branch %s already exists: %w", branch, ErrWorktreeExists)
	}

	if out, cmdErr := command("git", "-C", mainRoot, "worktree", "add", dir, "-b", branch); cmdErr != nil {
		gitErr := fmt.Errorf("git worktree add %s -b %s: %s: %w", dir, branch, strings.TrimSpace(string(out)), cmdErr)
		if rbErr := cleanupWorktreeAndBranch(mainRoot, dir, branch); rbErr != nil {
			return State{}, fmt.Errorf("%w (rollback also failed: %v)", gitErr, rbErr)
		}
		return State{}, gitErr
	}

	result, artErr := SetArtifacts(ArtifactOpts{
		ID:           o.ID,
		RepoRoot:     o.RepoRoot,
		StateDir:     o.StateDir,
		Branch:       branch,
		WorktreePath: dir,
	})
	if artErr != nil {
		recordErr := fmt.Errorf("record worktree artifacts: %w", artErr)
		if rbErr := cleanupWorktreeAndBranch(mainRoot, dir, branch); rbErr != nil {
			return State{}, fmt.Errorf("%w (rollback also failed: %v)", recordErr, rbErr)
		}
		return State{}, recordErr
	}
	return result, nil
}

// CleanupWorktree removes both the worktree directory (`git worktree
// remove`) and the branch (`git branch -D`) for o.ID + o.Slug. Used only on
// the creation-failure rollback path -- never on a "baseline gate failed"
// path, which keeps the worktree/branch for retry. "Branch not found" (the
// branch was never created because creation failed before the branch
// existed) is treated as a successful no-op, not an error.
func CleanupWorktree(o WorktreeOpts) error {
	if err := validateSlug(o.Slug); err != nil {
		return err
	}

	mainRoot, err := resolveMainRoot(o.RepoRoot)
	if err != nil {
		return fmt.Errorf("resolve main checkout root: %w", err)
	}
	dir, branch := worktreeNames(mainRoot, o.ID, o.Slug)
	return cleanupWorktreeAndBranch(mainRoot, dir, branch)
}

// cleanupWorktreeAndBranch removes the worktree at dir (if it exists) and
// deletes branch (if it exists), tolerating "branch not found" as a
// successful no-op.
func cleanupWorktreeAndBranch(mainRoot, dir, branch string) error {
	if _, statErr := os.Stat(dir); statErr == nil {
		if out, cmdErr := command("git", "-C", mainRoot, "worktree", "remove", dir, "--force"); cmdErr != nil {
			return fmt.Errorf("git worktree remove %s: %s: %w", dir, strings.TrimSpace(string(out)), cmdErr)
		}
	}

	out, cmdErr := command("git", "-C", mainRoot, "branch", "-D", branch)
	if cmdErr != nil {
		text := strings.ToLower(strings.TrimSpace(string(out)))
		if strings.Contains(text, "not found") {
			return nil
		}
		return fmt.Errorf("git branch -D %s: %s: %w", branch, strings.TrimSpace(string(out)), cmdErr)
	}
	return nil
}
