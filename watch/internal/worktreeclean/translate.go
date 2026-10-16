package worktreeclean

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Mapping classifies how a registered worktree maps to a local path.
type Mapping string

const (
	// MappingLocal: the registered path is <MainRoot>/.worktrees/<name> itself.
	MappingLocal Mapping = "local"
	// MappingTranslated: the worktree was registered on the other side of the
	// sandbox mount (that path does not exist here) and maps to
	// <MainRoot>/.worktrees/<name>.
	MappingTranslated Mapping = "translated"
	// MappingUnmapped: nothing local can be proven to be this worktree, so a
	// caller must keep it. Reason says why.
	MappingUnmapped Mapping = "unmapped"
)

// UnmappedReason is the closed set of reasons an entry is unmapped. Audit every
// switch over it as a group when adding a value.
type UnmappedReason string

const (
	ReasonInvalidAdminName    UnmappedReason = "invalid-admin-name"
	ReasonAdminNotDir         UnmappedReason = "admin-not-dir"
	ReasonGitdirUnreadable    UnmappedReason = "gitdir-unreadable"
	ReasonGitdirInvalid       UnmappedReason = "gitdir-invalid"
	ReasonWorktreesSymlink    UnmappedReason = "worktrees-symlink"
	ReasonWorktreeMissing     UnmappedReason = "worktree-missing"
	ReasonWorktreeSymlink     UnmappedReason = "worktree-symlink"
	ReasonDotgitInvalid       UnmappedReason = "dotgit-invalid"
	ReasonDotgitMismatch      UnmappedReason = "dotgit-mismatch"
	ReasonRegisteredElsewhere UnmappedReason = "registered-elsewhere"
	ReasonCommondirMismatch   UnmappedReason = "commondir-mismatch"
)

// maxPointerBytes bounds a pointer file (gitdir, commondir, .git): a path is at
// most PATH_MAX (4096) bytes plus a short prefix.
const maxPointerBytes = 8 << 10

const (
	worktreesDirName = ".worktrees"
	gitdirPrefix     = "gitdir: "
)

// validComponent reports whether name is a single path component: non-empty,
// not "." or "..", and free of "/", NUL and newline.
func validComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00\n")
}

// readPointerFile reads a one-line pointer file (gitdir, commondir, .git). The
// file may sit in a working tree, so it may be a symlink or a FIFO: a symlink
// is refused, the open is non-blocking (a FIFO would otherwise block until a
// writer appears), the type is checked on the open handle (no stat/open race)
// and the read is capped (go-gotchas rule 15). The symlink refusal is part of
// the open itself (O_NOFOLLOW), so a path swapped for a symlink between a check
// and the open cannot be followed. One trailing newline is trimmed; an empty
// result or a NUL or newline inside it is refused.
func readPointerFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ELOOP) {
		return "", fmt.Errorf("%s is a symlink: %w", path, err)
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only handle: a close error cannot lose data

	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file (mode %s)", path, st.Mode().Type())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPointerBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxPointerBytes {
		return "", fmt.Errorf("%s is larger than the %d-byte limit", path, maxPointerBytes)
	}
	content := strings.TrimSuffix(string(data), "\n")
	if content == "" || strings.ContainsAny(content, "\x00\n") {
		return "", fmt.Errorf("%s does not hold a single non-empty line", path)
	}
	return content, nil
}

// resolvePointer resolves a pointer-file path against base (the directory
// holding the pointer file, for git's relative form). An absolute path must
// already be canonical; a non-canonical one reports ok=false.
func resolvePointer(base, p string) (resolved string, ok bool) {
	if filepath.IsAbs(p) {
		return p, filepath.Clean(p) == p
	}
	return filepath.Join(base, p), true
}

// plainDirReason returns "" when path is a real directory, symlink when it is a
// symlink and other for anything else (including a failed Lstat).
func plainDirReason(path string, symlink, other UnmappedReason) UnmappedReason {
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return other
	case info.Mode()&fs.ModeSymlink != 0:
		return symlink
	case !info.IsDir():
		return other
	}
	return ""
}

func unmapped(e Entry, reason UnmappedReason) Entry {
	e.Mapping = MappingUnmapped
	e.Reason = reason
	e.LocalPath = ""
	return e
}

// mapEntry maps the admin entry <CommonDir>/worktrees/<adminName> to a local
// worktree path. A mapping is kept only when the admin gitdir, the worktree's
// .git file and the admin commondir all agree; every other outcome is
// MappingUnmapped with its reason, so a caller keeps the worktree.
func mapEntry(repo Repo, adminName string) Entry {
	e, _ := mapAdmin(repo, adminName)
	return e
}

// mapAdmin is mapEntry plus the one silent exclusion: outside is true when the
// admin gitdir parsed cleanly but registers a worktree whose parent directory is
// not named .worktrees. Such an entry is returned as unmapped (registered
// elsewhere) for a direct mapEntry caller, and Enumerate leaves it out.
func mapAdmin(repo Repo, adminName string) (e Entry, outside bool) {
	e = Entry{AdminName: adminName}
	if !validComponent(adminName) {
		return unmapped(e, ReasonInvalidAdminName), false
	}
	adminRoot := filepath.Join(repo.CommonDir, "worktrees")
	e.AdminDir = filepath.Join(adminRoot, adminName)
	if plainDirReason(adminRoot, ReasonAdminNotDir, ReasonAdminNotDir) != "" ||
		plainDirReason(e.AdminDir, ReasonAdminNotDir, ReasonAdminNotDir) != "" {
		return unmapped(e, ReasonAdminNotDir), false
	}

	recorded, err := readPointerFile(filepath.Join(e.AdminDir, "gitdir"))
	if err != nil {
		return unmapped(e, ReasonGitdirUnreadable), false
	}
	gitdir, ok := resolvePointer(e.AdminDir, recorded)
	if !ok || filepath.Base(gitdir) != ".git" {
		return unmapped(e, ReasonGitdirInvalid), false
	}
	registered := filepath.Dir(gitdir)
	e.RegisteredPath = registered
	e.Name = filepath.Base(registered)
	if filepath.Base(filepath.Dir(registered)) != worktreesDirName {
		return unmapped(e, ReasonRegisteredElsewhere), true
	}
	if !validComponent(e.Name) {
		return unmapped(e, ReasonGitdirInvalid), false
	}

	worktreesDir := filepath.Join(repo.MainRoot, worktreesDirName)
	if reason := plainDirReason(worktreesDir, ReasonWorktreesSymlink, ReasonWorktreeMissing); reason != "" {
		return unmapped(e, reason), false
	}
	local := filepath.Join(worktreesDir, e.Name)
	if reason := plainDirReason(local, ReasonWorktreeSymlink, ReasonWorktreeMissing); reason != "" {
		return unmapped(e, reason), false
	}
	dotgit, ok := readDotGit(local)
	if !ok {
		return unmapped(e, ReasonDotgitInvalid), false
	}

	// The worktree's .git file must point back at the admin dir that points at
	// the worktree. For a worktree registered on the other side of the mount,
	// the foreign root F is derived from the recorded path (F/.worktrees/<name>)
	// and the .git file must name F/.git/worktrees/<admin>.
	mapping := MappingLocal
	if registered == local {
		if dotgit != e.AdminDir {
			return unmapped(e, ReasonDotgitMismatch), false
		}
	} else {
		if _, err := os.Lstat(registered); !errors.Is(err, fs.ErrNotExist) {
			return unmapped(e, ReasonRegisteredElsewhere), false
		}
		foreignRoot := filepath.Dir(filepath.Dir(registered))
		if dotgit != filepath.Join(foreignRoot, ".git", "worktrees", adminName) {
			return unmapped(e, ReasonDotgitMismatch), false
		}
		mapping = MappingTranslated
	}

	// Otherwise git would read another repository's refs through this admin dir.
	if !commondirMatches(repo, e.AdminDir) {
		return unmapped(e, ReasonCommondirMismatch), false
	}
	e.Mapping = mapping
	e.LocalPath = local
	return e, false
}

// readDotGit returns the admin dir the worktree's .git file points at.
func readDotGit(worktree string) (string, bool) {
	content, err := readPointerFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return "", false
	}
	target, ok := strings.CutPrefix(content, gitdirPrefix)
	if !ok || target == "" {
		return "", false
	}
	return resolvePointer(worktree, target)
}

// commondirMatches reports whether the admin dir's commondir resolves to the
// local common dir. An unreadable or non-canonical commondir cannot be shown to
// agree, so it does not match.
func commondirMatches(repo Repo, adminDir string) bool {
	content, err := readPointerFile(filepath.Join(adminDir, "commondir"))
	if err != nil {
		return false
	}
	common, ok := resolvePointer(adminDir, content)
	return ok && common == repo.CommonDir
}
