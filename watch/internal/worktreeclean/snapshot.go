package worktreeclean

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// afterSnapshot is a test hook called with the snapshot git dir right after it
// is built and before the first git read. It is nil in production.
var afterSnapshot func(gitDir string)

// snapshot is a private, cenci-owned git dir that Inspect points every git read
// at (#1278). The repository configuration, index and refs git reads are copies
// only cenci can write, so a sandboxed agent that edits the live repo after the
// config was checked cannot change what the later reads use. It has no
// commondir file (git never falls back to the live common dir) and reaches the
// live objects, which are content-addressed and only read, through
// objects/info/alternates.
type snapshot struct{ dir string }

// budget is what is left of the limits one snapshot build may use.
type budget struct {
	bytes int // bytes still allowed to be copied
	refs  int // loose-ref walk entries still allowed
}

// snapshotConfigKeyPattern matches the config keys the snapshot refuses: an
// include (git would read the included file live again) and the ref storage
// format (a non-files ref store cannot be copied as loose files).
const snapshotConfigKeyPattern = `^(include|includeif)\.|^extensions\.refstorage$`

// filesRefStorage is the config output line of the only accepted ref storage.
const filesRefStorage = "extensions.refstorage files"

// newSnapshot builds the snapshot of the worktree m of repo. Any failure removes
// the half-built directory again and joins a failed removal into the error.
//
// Build order matters. The config copies are probed (includes, ref storage)
// before the index and refs are copied, so a reftable repository fails with its
// own message rather than on a stub refs/heads file. The loose refs are copied
// before packed-refs because git reads loose refs first and packed-refs second:
// a concurrent `pack-refs --prune` moves a ref from the first to the second, so
// copying in git's own order can see it twice but never lose it.
func newSnapshot(ctx context.Context, repo Repo, m Entry) (*snapshot, error) {
	dir, err := os.MkdirTemp(snapshotParent, "cenci-wtsnap-")
	if err != nil {
		return nil, fmt.Errorf("create the snapshot git dir: %w", err)
	}
	// A relative TMPDIR would make --git-dir resolve against the worktree.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("resolve the snapshot git dir: %w", err), removeSnapshotDir(dir))
	}
	s := &snapshot{dir: abs}
	if err := s.build(ctx, repo, m); err != nil {
		return nil, errors.Join(err, s.remove())
	}
	return s, nil
}

// remove deletes the snapshot directory.
func (s *snapshot) remove() error {
	if err := removeSnapshotDir(s.dir); err != nil {
		return fmt.Errorf("remove the snapshot git dir %s: %w", s.dir, err)
	}
	return nil
}

func (s *snapshot) build(ctx context.Context, repo Repo, m Entry) error {
	if strings.ContainsAny(repo.CommonDir, "\r\n") {
		return fmt.Errorf("common dir %q holds a line break and cannot be named in the snapshot's alternates", collapse(repo.CommonDir))
	}
	b := &budget{bytes: maxSnapshotBytes, refs: maxSnapshotRefs}
	at := func(rel string) string { return filepath.Join(s.dir, rel) }
	common := func(rel string) string { return filepath.Join(repo.CommonDir, rel) }
	admin := func(rel string) string { return filepath.Join(m.AdminDir, rel) }

	for _, step := range []struct {
		src, rel string
		required bool
	}{
		{common("config"), "config", true},
		{admin("config.worktree"), "config.worktree", false},
		{admin("HEAD"), "HEAD", true},
	} {
		if err := s.copyFile(step.src, step.rel, step.required, b); err != nil {
			return err
		}
	}

	for _, dir := range []string{"refs", "info", "objects", filepath.Join("objects", "info")} {
		if err := os.Mkdir(at(dir), 0o700); err != nil {
			return fmt.Errorf("create %s in the snapshot: %w", dir, err)
		}
	}
	alternates := common("objects") + "\n"
	if err := os.WriteFile(at("objects/info/alternates"), []byte(alternates), 0o600); err != nil {
		return fmt.Errorf("write the snapshot's alternates: %w", err)
	}

	for _, rel := range []string{"config", "config.worktree"} {
		if _, err := os.Lstat(at(rel)); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := checkSnapshotConfig(ctx, s.dir, m.LocalPath, at(rel)); err != nil {
			return err
		}
	}

	if err := s.copyFile(admin("index"), "index", false, b); err != nil {
		return err
	}
	for _, rel := range []string{"refs/heads", "refs/remotes"} {
		if err := copyRefTree(ctx, common(rel), at(rel), b); err != nil {
			return err
		}
	}
	for _, rel := range []string{"packed-refs", "info/exclude", "info/attributes", "shallow"} {
		if err := s.copyFile(common(rel), rel, false, b); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies src to rel in the snapshot. A missing src is skipped unless
// required.
func (s *snapshot) copyFile(src, rel string, required bool, b *budget) error {
	present, err := copyRegular(src, filepath.Join(s.dir, rel), b)
	if err != nil {
		return err
	}
	if !present && required {
		return fmt.Errorf("snapshot source %s does not exist", src)
	}
	return nil
}

// copyRegular copies the regular file src to the new file dst and reports
// whether src existed. src is opened without following a symlink or blocking on
// a FIFO and its type is checked on the open handle (the readPointerFile
// pattern), so a path swapped for a symlink cannot be followed. The bytes copied
// are charged to b; going over it wraps errOutputTruncated. dst gets src's
// mtime: git's racy-entry check compares each index entry's mtime with the index
// file's own, and a fresh mtime would let a same-size edit read as clean.
func copyRegular(src, dst string, b *budget) (present bool, err error) {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	switch {
	case errors.Is(err, syscall.ELOOP):
		return true, fmt.Errorf("%s is a symlink: %w", src, err)
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return true, err
	}
	defer func() { _ = in.Close() }() // read-only handle: a close error cannot lose data

	st, err := in.Stat()
	if err != nil {
		return true, err
	}
	if !st.Mode().IsRegular() {
		return true, notRegular(src, st.Mode())
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return true, fmt.Errorf("create %s in the snapshot: %w", filepath.Base(dst), err)
	}
	n, copyErr := io.Copy(out, io.LimitReader(in, int64(b.bytes)+1))
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return true, fmt.Errorf("copy %s into the snapshot: %w", src, copyErr)
	}
	if int(n) > b.bytes {
		return true, fmt.Errorf("%w: copying %s goes over the %d-byte snapshot limit", errOutputTruncated, src, maxSnapshotBytes)
	}
	b.bytes -= int(n)
	if err := os.Chtimes(dst, st.ModTime(), st.ModTime()); err != nil {
		return true, fmt.Errorf("keep the mtime of %s in the snapshot: %w", filepath.Base(dst), err)
	}
	return true, nil
}

// notRegular is the error for a source that is neither a plain directory nor a
// regular file. A symlink and any other type get distinct wording.
func notRegular(path string, mode fs.FileMode) error {
	if mode&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	return fmt.Errorf("%s is not a regular file (mode %s)", path, mode.Type())
}

// copyRefTree copies the loose-ref tree srcRoot to dstRoot without following
// symlinks. Every entry must be a plain directory or a regular file; a symlink or
// any other type is an error, as is going over the walk-entry limit (which counts
// the root and the directories too). A missing srcRoot is not an error, but a ref
// file that vanishes after the directory listed it is: it may have been deleted
// or packed, and a snapshot without it would misreport the refs.
func copyRefTree(ctx context.Context, srcRoot, dstRoot string, b *budget) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == srcRoot && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if b.refs--; b.refs < 0 {
			return fmt.Errorf("%w: more than %d entries under %s", errOutputTruncated, maxSnapshotRefs, srcRoot)
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("ref path %q is not inside %s", path, srcRoot)
		}
		dst := filepath.Join(dstRoot, rel)
		switch {
		case d.IsDir():
			return os.Mkdir(dst, 0o700)
		case d.Type().IsRegular():
			present, err := copyRegular(path, dst, b)
			if err == nil && !present {
				return fmt.Errorf("ref vanished during snapshot: %s", path)
			}
			return err
		}
		return notRegular(path, d.Type())
	})
}

// checkSnapshotConfig asks git itself, with includes off, whether the snapshot
// config file defines an include or a ref storage other than files; parsing the
// config in Go could disagree with git and reopen the gap. Only a clean "no
// match" (exit 1, no output at all) passes; every other outcome is an error,
// and so is any line it does not recognise.
func checkSnapshotConfig(ctx context.Context, gitDir, workTree, file string) error {
	stdout, stderr, err := execGitWT(ctx, gitDir, workTree,
		"config", "--file", file, "--no-includes", "--get-regexp", snapshotConfigKeyPattern)
	if err != nil {
		// Compared by type, not errors.As: a timeout or truncation joined to the
		// exit error must not read as a clean no-match.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && stdout == "" && stderr == "" {
			return nil
		}
		return fmt.Errorf("check the snapshot's %s: %w", filepath.Base(file), withStderr(err, stderr))
	}
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		key := strings.ToLower(line)
		switch {
		case line == filesRefStorage:
		case strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif."):
			return fmt.Errorf("the %s defines an include (%s); includes are read live by git, so the worktree is kept", filepath.Base(file), collapse(line))
		default:
			return fmt.Errorf("the %s sets extensions.refStorage to something other than files (%s); only files can be snapshotted, so the worktree is kept", filepath.Base(file), collapse(line))
		}
	}
	return nil
}
