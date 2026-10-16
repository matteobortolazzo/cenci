package worktreeclean

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Repo is the main checkout, resolved from git's own answer rather than from
// the caller's directory.
type Repo struct {
	// MainRoot is the main checkout's real path (symlinks resolved).
	MainRoot string
	// CommonDir is MainRoot/.git.
	CommonDir string
}

// Entry is one registered worktree admin entry. AdminDir and LocalPath are the
// only paths a later removal may touch; LocalPath is empty unless Mapping is
// MappingLocal or MappingTranslated.
type Entry struct {
	// AdminName is the directory name under <CommonDir>/worktrees.
	AdminName string
	// AdminDir is <CommonDir>/worktrees/<AdminName>; empty for an invalid name.
	AdminDir string
	// Name is the worktree directory name under .worktrees.
	Name string
	// RegisteredPath is the worktree path git recorded, on either side of the
	// sandbox mount.
	RegisteredPath string
	// LocalPath is <MainRoot>/.worktrees/<Name> for a mapped entry.
	LocalPath string
	Mapping   Mapping
	// Reason is set only when Mapping is MappingUnmapped.
	Reason UnmappedReason
}

// ResolveRepo resolves the main checkout containing dir (a main checkout, one
// of its subdirectories or a linked worktree). The common dir comes from git,
// never from the caller or the environment, and must be named .git so that its
// parent is a real checkout root (error-handling rule 16).
func ResolveRepo(ctx context.Context, dir string) (Repo, error) {
	stdout, stderr, err := execGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Repo{}, fmt.Errorf("resolve git common dir of %s: %w", dir, withStderr(err, stderr))
	}
	common := strings.TrimSuffix(stdout, "\n")
	if !filepath.IsAbs(common) || filepath.Clean(common) != common || strings.ContainsAny(common, "\x00\n") {
		return Repo{}, fmt.Errorf("git reported a malformed common dir %q for %s", common, dir)
	}
	if filepath.Base(common) != ".git" {
		return Repo{}, fmt.Errorf("git common dir %q of %s is not a .git directory of a main checkout", common, dir)
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(common))
	if err != nil {
		return Repo{}, fmt.Errorf("resolve main root of %s: %w", dir, err)
	}
	// A linked worktree's .git file chooses the repository git reports, and a
	// sandboxed agent can rewrite it. The answer is trusted only when it
	// contains the directory asked about (a main checkout, a subdirectory or a
	// worktree under <root>/.worktrees).
	if err := requireWithin(root, dir); err != nil {
		return Repo{}, fmt.Errorf("git resolved %s to the repository at %s: %w", dir, root, err)
	}
	return Repo{MainRoot: root, CommonDir: filepath.Join(root, ".git")}, nil
}

// requireWithin reports an error unless dir, with symlinks resolved, is root or
// a path below it.
func requireWithin(root, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(root, resolved); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s is not inside it", resolved)
	}
	return nil
}

// Enumerate lists every worktree git has registered under a .worktrees/
// directory, sorted by admin name, and never the main worktree (it has no admin
// entry). The only entry left out is one whose admin gitdir parses cleanly but
// registers a worktree outside a .worktrees directory; every malformed admin
// entry is listed as unmapped with its reason. With no worktrees the result is
// an empty non-nil slice.
func Enumerate(ctx context.Context, dir string) (Repo, []Entry, error) {
	repo, err := ResolveRepo(ctx, dir)
	if err != nil {
		return Repo{}, nil, err
	}
	entries := make([]Entry, 0)
	adminRoot := filepath.Join(repo.CommonDir, "worktrees")
	info, err := os.Lstat(adminRoot)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return repo, entries, nil
	case err != nil:
		return Repo{}, nil, fmt.Errorf("stat %s: %w", adminRoot, err)
	case !info.IsDir():
		return Repo{}, nil, fmt.Errorf("%s is not a plain directory (mode %s)", adminRoot, info.Mode().Type())
	}
	// ReadDir returns its entries sorted by file name.
	dirEntries, err := os.ReadDir(adminRoot)
	if err != nil {
		return Repo{}, nil, fmt.Errorf("read %s: %w", adminRoot, err)
	}
	for _, de := range dirEntries {
		if err := ctx.Err(); err != nil {
			return Repo{}, nil, err
		}
		if e, outside := mapAdmin(repo, de.Name()); !outside {
			entries = append(entries, e)
		}
	}
	return repo, entries, nil
}
