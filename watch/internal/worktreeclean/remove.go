package worktreeclean

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// beforeDelete is a test hook, nil in production: Remove calls it once after the
// final re-check and before it deletes anything, so a test can swap a path in
// the window a real attacker would use.
var beforeDelete func()

// beforeBranchDelete is the same kind of hook for the window between the
// directory deletions and the branch deletion.
var beforeBranchDelete func()

// Removal is the outcome of one Remove. Removed is true once the worktree
// directory is gone; Err then reports what did not complete after that (the
// admin dir, or the branch of a finished worktree), and BranchDeleted says
// whether the local branch went too. BytesFreed is the worktree's size as of the
// final re-check. A worktree that was not removed carries the reason in Reason
// and, when something failed, in Err.
type Removal struct {
	Entry         Entry
	Verdict       Verdict
	Removed       bool
	BranchDeleted bool
	BytesFreed    int64
	Reason        KeepReason
	Err           error
}

// Reclaim removes every removable classification, in order, and returns one
// Removal per input. A failure in one removal does not stop the rest; once ctx
// is done the remaining removable entries are reported with ctx's error.
func Reclaim(ctx context.Context, repo Repo, cs []Classification, o Options) []Removal {
	out := make([]Removal, len(cs))
	for i, c := range cs {
		out[i] = Remove(ctx, repo, c, o)
	}
	return out
}

// Remove deletes one classified worktree. It never runs `git worktree prune` or
// `git worktree remove`, never uses --force, and touches only
// <MainRoot>/.worktrees/<Name> and <CommonDir>/worktrees/<AdminName>, both
// re-derived from the AdminName: the Entry paths of the classification are never
// used.
//
// A classification that is not removable is returned untouched as a Removal. For
// a removable one the checks are re-run immediately before deleting: a fresh
// Inspect and pipeline guard must give the same verdict, HEAD and branch, and the
// entry must map to exactly the Entry that was classified (same name, paths and
// mapping, so an admin entry moved or re-pointed since is not the worktree that
// was checked). Any difference keeps the worktree as KeepChanged, and Err names
// what changed. Remove does not call the injected guards (Options.InUse and
// Babysit); the caller re-checks those itself right before calling it.
//
// Both directories are deleted below an os.Root opened on repo.MainRoot, with no
// symlink followed at any level (removeChild). A finished worktree's local branch
// is then deleted with `git branch -D` (a squash merge makes `-d` refuse), but
// only if its tip is still the re-checked HEAD and the common dir is still the
// directory the admin delete went through. A failure to delete the worktree
// directory skips the admin dir and the branch; a failure on the admin dir skips
// the branch. After the worktree directory is gone Removed stays true and Err
// reports what did not complete.
func Remove(ctx context.Context, repo Repo, c Classification, o Options) Removal {
	rem := Removal{Entry: c.Entry, Verdict: c.Verdict, Reason: c.Reason}
	if c.Verdict != VerdictFinished && c.Verdict != VerdictStale {
		rem.Verdict = VerdictKeep
		return rem
	}
	if err := ctx.Err(); err != nil {
		rem.Err = err
		return rem
	}

	fresh := evaluate(ctx, repo, c.Entry, o)
	if fresh.Verdict != c.Verdict || fresh.Facts.HeadSHA != c.Facts.HeadSHA || fresh.Facts.Branch != c.Facts.Branch ||
		fresh.Entry != c.Entry {
		rem.Verdict, rem.Reason, rem.Err = VerdictKeep, KeepChanged, recheckChange(c, fresh)
		return rem
	}
	m := fresh.Entry

	if beforeDelete != nil {
		beforeDelete()
	}
	if _, err := removeChild(repo.MainRoot, []string{worktreesDirName}, m.Name); err != nil {
		rem.Err = fmt.Errorf("remove worktree directory %s: %w", m.LocalPath, err)
		return rem
	}
	rem.Removed = true
	rem.BytesFreed = fresh.Facts.SizeBytes
	common, err := removeAdminDir(repo, m.AdminName)
	if err != nil {
		rem.Err = fmt.Errorf("remove admin directory %s: %w", m.AdminDir, err)
		return rem
	}

	if fresh.Verdict == VerdictFinished {
		if beforeBranchDelete != nil {
			beforeBranchDelete()
		}
		if err := deleteBranch(ctx, repo, common, fresh.Facts.Branch, fresh.Facts.HeadSHA); err != nil {
			rem.Err = err
			return rem
		}
		rem.BranchDeleted = true
	}
	return rem
}

// recheckChange is the error of a refused re-check: it names every difference
// between the classification and the fresh evaluation, and wraps the fresh
// evaluation's own error when there is one.
func recheckChange(c, fresh Classification) error {
	var changes []string
	if fresh.Verdict != c.Verdict {
		changes = append(changes, fmt.Sprintf("now %s (was %s)", verdictText(fresh), verdictText(c)))
	}
	if fresh.Facts.HeadSHA != c.Facts.HeadSHA {
		changes = append(changes, fmt.Sprintf("HEAD %q -> %q", c.Facts.HeadSHA, fresh.Facts.HeadSHA))
	}
	if fresh.Facts.Branch != c.Facts.Branch {
		changes = append(changes, fmt.Sprintf("branch %q -> %q", c.Facts.Branch, fresh.Facts.Branch))
	}
	if fresh.Entry != c.Entry {
		changes = append(changes, fmt.Sprintf("admin entry %s now maps to %q (mapping %s, was %q)",
			c.Entry.AdminName, fresh.Entry.LocalPath, fresh.Entry.Mapping, c.Entry.LocalPath))
	}
	err := fmt.Errorf("re-check changed: %s", strings.Join(changes, "; "))
	if fresh.Err != nil {
		return fmt.Errorf("%w: %w", err, fresh.Err)
	}
	return err
}

func verdictText(c Classification) string {
	if c.Reason == "" {
		return string(c.Verdict)
	}
	return string(c.Verdict) + "/" + string(c.Reason)
}

// removeAdminDir deletes <CommonDir>/worktrees/<adminName> and returns the
// directory identity of CommonDir as the delete walked it, for deleteBranch to
// compare against. The walk starts at the main root and descends through .git,
// so a symlink swapped in for .git cannot redirect the delete. A Repo whose
// CommonDir is not <MainRoot>/.git (anything but what ResolveRepo returns) is
// refused rather than handled with a weaker single-parent check.
func removeAdminDir(repo Repo, adminName string) (os.FileInfo, error) {
	if err := validRepo(repo); err != nil {
		return nil, err
	}
	infos, err := removeChild(repo.MainRoot, []string{".git", "worktrees"}, adminName)
	if err != nil {
		return nil, err
	}
	return infos[0], nil
}

// removeChild deletes <top>/<comps...>/<name> without following a symlink at any
// level. Every component of comps, and name itself, must be a plain directory
// (checked with Lstat on the root it sits in); each is then opened as a new
// os.Root and must be the same directory as the one checked. The delete goes
// through the innermost root, so a path swapped for a symlink after these checks
// cannot redirect it and no symlink below name is followed. top is trusted (the
// resolved main root). It returns the Lstat info of each component, in order.
func removeChild(top string, comps []string, name string) ([]os.FileInfo, error) {
	if !validComponent(name) {
		return nil, fmt.Errorf("invalid directory name %q", name)
	}
	root, err := os.OpenRoot(top)
	if err != nil {
		return nil, err
	}
	walked := top
	infos := make([]os.FileInfo, 0, len(comps))
	for _, comp := range comps {
		next, info, err := descend(root, walked, comp)
		_ = root.Close() // nothing is written through the handle
		if err != nil {
			return nil, err
		}
		root = next
		walked = filepath.Join(walked, comp)
		infos = append(infos, info)
	}
	defer func() { _ = root.Close() }()
	child, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !child.IsDir() {
		return nil, fmt.Errorf("%s is not a plain directory (mode %s)", filepath.Join(walked, name), child.Mode().Type())
	}
	if err := root.RemoveAll(name); err != nil {
		return nil, err
	}
	return infos, nil
}

// descend opens the plain directory comp inside root (named path, for errors) as
// a new os.Root, refusing a symlink, another file type, or a directory that is
// not the one that was checked. On success the caller owns the returned root.
func descend(root *os.Root, path, comp string) (*os.Root, os.FileInfo, error) {
	if !validComponent(comp) {
		return nil, nil, fmt.Errorf("invalid directory name %q", comp)
	}
	before, err := root.Lstat(comp)
	if err != nil {
		return nil, nil, err
	}
	if !before.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a plain directory (mode %s)", filepath.Join(path, comp), before.Mode().Type())
	}
	next, err := root.OpenRoot(comp)
	if err != nil {
		return nil, nil, err
	}
	opened, err := next.Stat(".")
	if err != nil {
		_ = next.Close()
		return nil, nil, err
	}
	if !os.SameFile(before, opened) {
		_ = next.Close()
		return nil, nil, fmt.Errorf("%s changed while it was being opened", filepath.Join(path, comp))
	}
	return next, before, nil
}

// deleteBranch deletes the local branch with `git branch -D`, with the repo's
// hooks disabled (gitCommon), but only while the common dir is still the plain
// directory common (the admin delete's walk of it) and the branch tip is still
// tip, the HEAD the re-check verified: a branch someone moved since is kept.
// gitCommon refuses to run, and discards its result, while a commondir file
// exists in the common dir (GIT_COMMON_DIR alone does not stop the refs backend
// from following it), so a planted commondir cannot redirect the deletion to
// another repository. The accepted remaining risk is the instant between those
// checks and git opening the common dir by path: a commondir written and removed
// inside that window, or a .git symlink swap there, needs exact timing (swapping
// takes RENAME_EXCHANGE) and could only delete a same-named branch, at the same
// tip, in another repository.
func deleteBranch(ctx context.Context, repo Repo, common os.FileInfo, branch, tip string) error {
	now, err := os.Lstat(repo.CommonDir)
	if err != nil {
		return fmt.Errorf("refusing to delete branch %s: %w", branch, err)
	}
	if !now.IsDir() || !os.SameFile(common, now) {
		return fmt.Errorf("refusing to delete branch %s: %s is no longer the directory the admin delete went through", branch, repo.CommonDir)
	}
	if !branchPattern.MatchString(branch) || !isHexSHA(tip) {
		return fmt.Errorf("refusing to delete branch %q at %q: malformed", branch, tip)
	}
	stdout, stderr, err := gitCommon(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("read the tip of branch %s: %w", branch, withStderr(err, stderr))
	}
	if got := strings.TrimSpace(stdout); got != tip {
		return fmt.Errorf("branch %s moved from %s to %s since the re-check; kept", branch, tip, got)
	}
	if _, stderr, err := gitCommon(ctx, repo, "branch", "-D", "--", branch); err != nil {
		return fmt.Errorf("delete branch %s: %w", branch, withStderr(err, stderr))
	}
	return nil
}
