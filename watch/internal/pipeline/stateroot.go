package pipeline

// The os.Root-scoped access to a ticket's persisted state (ticket #1306). The
// state directory (.cenci/pipeline/) lives in the agent-writable bind-mounted
// repo, so a sandboxed agent can plant a symlink, a FIFO or an oversized file
// there, or swap a directory for a symlink while a host-side process is
// between two path-based operations. Every read, create, rename, remove and
// lock open of <id>.json, <id>.json.lock and the temp files therefore goes
// through a stateDir: it walks .cenci and then pipeline one component at a
// time from an os.Root on the repo root (Lstat, OpenRoot, SameFile at each
// level) and keeps only the innermost root, so a directory swapped after the
// walk cannot redirect anything.
//
// os.Root.OpenFile does not honour O_NOFOLLOW for a symlink in the last path
// component (it follows one whose target stays inside the root), and O_CREATE
// creates through a dangling one. So every file operation here pairs Lstat with
// the open handle's Fstat and os.SameFile, and every create is O_EXCL. Errors
// name paths only, never file content.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// maxStateFileBytes bounds the read of a pipeline state file: a real one is
// well under a kilobyte, so anything larger is hostile or corrupt. A package
// var so tests can shrink it.
var maxStateFileBytes = 1 << 20

// afterStateDirWalkFn is a test-only seam, like flockFn: when set, it runs
// after the state directory walk finishes and before the first file operation,
// so a test can swap a directory at exactly the point a race would.
var afterStateDirWalkFn func()

// afterDirLstatFn is a test-only seam of the same kind: when set, descend runs
// it with the directory's display path after the Lstat plain-directory check
// and before the OpenRoot, so a test can swap that one level at exactly the
// point the per-level os.SameFile check guards.
var afterDirLstatFn func(path string)

// maxTempNameAttempts bounds the O_EXCL retries for a colliding random temp
// name; a collision of 64 random bits is not expected at all.
const maxTempNameAttempts = 8

// stateLocation is where one ticket's state lives: the repo layout
// <repoRoot>/.cenci/pipeline/<id>.json, or an explicit stateDir holding
// <id>.json directly. display is the state file's path as shown in
// Output.Artifacts and in error texts.
type stateLocation struct {
	repoRoot string
	stateDir string
	display  string
}

// resolveStateLocation validates o.ID and computes where the state lives for
// this invocation, per Opts' StateDir/RepoRoot precedence. Nothing is touched
// on disk.
func resolveStateLocation(o Opts) (stateLocation, error) {
	if !idPattern.MatchString(o.ID) {
		return stateLocation{}, fmt.Errorf("invalid ticket id %q: must match ^\\d+$", o.ID)
	}
	if o.StateDir != "" {
		return stateLocation{stateDir: o.StateDir, display: filepath.Join(o.StateDir, o.ID+".json")}, nil
	}
	repoRoot := o.RepoRoot
	if repoRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return stateLocation{}, fmt.Errorf("resolve working directory: %w", err)
		}
		repoRoot, err = resolveRepoRoot(cwd)
		if err != nil {
			return stateLocation{}, fmt.Errorf("resolve repo root: %w", err)
		}
	}
	display, err := statePath(repoRoot, o.ID)
	if err != nil {
		return stateLocation{}, err
	}
	return stateLocation{repoRoot: repoRoot, display: display}, nil
}

// stateDir is an open handle on the innermost state directory plus the name of
// the one state file it serves. Close it when done.
type stateDir struct {
	root *os.Root
	dir  string // display path of the directory root was opened on
	file string // base name of the state file, e.g. "42.json"
}

// openStateDir walks to the state directory of loc. With create, missing
// directories are made (mode 0o755 before the umask); without it a missing
// level is an error wrapping fs.ErrNotExist, so a read-only caller can tell
// "nothing recorded yet" from a refusal. Any level that is not a plain
// directory (a symlink included), or that changed while it was being opened,
// is refused.
func openStateDir(loc stateLocation, create bool) (*stateDir, error) {
	var root *os.Root
	var err error
	if loc.stateDir != "" {
		root, err = openExplicitStateDir(loc.stateDir, create)
	} else {
		root, err = openRepoStateDir(loc.repoRoot, create)
	}
	if err != nil {
		return nil, err
	}
	if afterStateDirWalkFn != nil {
		afterStateDirWalkFn()
	}
	return &stateDir{root: root, dir: filepath.Dir(loc.display), file: filepath.Base(loc.display)}, nil
}

// openRepoStateDir walks .cenci and then pipeline from an os.Root on repoRoot.
// Each intermediate root is closed once the next level is open.
func openRepoStateDir(repoRoot string, create bool) (*os.Root, error) {
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("open repo root %s: %w", repoRoot, err)
	}
	path := repoRoot
	for _, comp := range []string{".cenci", "pipeline"} {
		next, err := enterDir(root, path, comp, create)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		root, path = next, filepath.Join(path, comp)
	}
	return root, nil
}

// openExplicitStateDir opens the operator-chosen --state-dir itself. The
// directory is created with MkdirAll (the operator's own path, as before), then
// entered from an os.Root on its parent, so a symlink as the final component is
// refused like any other level. Symlinks in the parent path components are the
// operator's responsibility: --state-dir is an operator-supplied CLI/test hook,
// and host-side automation always uses the repo layout.
func openExplicitStateDir(stateDirPath string, create bool) (*os.Root, error) {
	abs, err := filepath.Abs(stateDirPath)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory %s: %w", stateDirPath, err)
	}
	if create {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
	}
	parent := filepath.Dir(abs)
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, fmt.Errorf("open state directory parent %s: %w", parent, err)
	}
	defer func() { _ = root.Close() }()
	return enterDir(root, parent, filepath.Base(abs), false)
}

// enterDir opens the directory comp under root (displayed as path/comp),
// creating it first when asked. An already existing entry of any type is left
// as it is and judged by descend.
func enterDir(root *os.Root, path, comp string, create bool) (*os.Root, error) {
	if create {
		if err := root.Mkdir(comp, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create state directory %s: %w", filepath.Join(path, comp), err)
		}
	}
	return descend(root, path, comp)
}

// descend opens the directory comp under root as a new root, but only if comp
// is a plain directory (not a symlink) and the opened directory is the one the
// Lstat saw.
func descend(root *os.Root, path, comp string) (*os.Root, error) {
	full := filepath.Join(path, comp)
	before, err := root.Lstat(comp)
	if err != nil {
		return nil, fmt.Errorf("stat state directory %s: %w", full, err)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("%s is not a plain directory (mode %s)", full, before.Mode().Type())
	}
	if afterDirLstatFn != nil {
		afterDirLstatFn(full)
	}
	next, err := root.OpenRoot(comp)
	if err != nil {
		return nil, fmt.Errorf("open state directory %s: %w", full, err)
	}
	opened, err := next.Stat(".")
	if err != nil {
		_ = next.Close()
		return nil, fmt.Errorf("stat state directory %s: %w", full, err)
	}
	if !os.SameFile(before, opened) {
		_ = next.Close()
		return nil, fmt.Errorf("%s changed while it was being opened", full)
	}
	return next, nil
}

// Close releases the directory handle.
func (d *stateDir) Close() error { return d.root.Close() }

// displayPath is the state file's path for Output.Artifacts and error texts.
func (d *stateDir) displayPath() string { return filepath.Join(d.dir, d.file) }

func (d *stateDir) path(name string) string { return filepath.Join(d.dir, name) }

// lstatState returns the state file's Lstat info, an error wrapping
// fs.ErrNotExist when it is missing, and an error when it is anything but a
// regular file (a symlink, FIFO, device or directory).
func (d *stateDir) lstatState() (fs.FileInfo, error) {
	info, err := d.root.Lstat(d.file)
	if err != nil {
		return nil, fmt.Errorf("stat state file %s: %w", d.displayPath(), err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("state file %s is not a regular file (mode %s)", d.displayPath(), info.Mode().Type())
	}
	return info, nil
}

// readRaw returns the state file's bytes. The file must be regular both before
// and after the open (a non-blocking one, so a FIFO swapped in between cannot
// stall it), the opened file must be the one the Lstat saw, and the read is
// capped at maxStateFileBytes. A missing file wraps fs.ErrNotExist.
func (d *stateDir) readRaw() ([]byte, error) {
	before, err := d.lstatState()
	if err != nil {
		return nil, err
	}
	f, err := d.root.OpenFile(d.file, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open state file %s: %w", d.displayPath(), err)
	}
	defer func() { _ = f.Close() }() // read-only handle: a close error cannot lose data

	opened, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat state file %s: %w", d.displayPath(), err)
	}
	if !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("state file %s is not a regular file (mode %s)", d.displayPath(), opened.Mode().Type())
	}
	if !os.SameFile(before, opened) {
		return nil, fmt.Errorf("state file %s changed while it was being opened", d.displayPath())
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(maxStateFileBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read state file %s: %w", d.displayPath(), err)
	}
	if len(data) > maxStateFileBytes {
		return nil, fmt.Errorf("state file %s is larger than the %d-byte limit", d.displayPath(), maxStateFileBytes)
	}
	return data, nil
}

// load reads and decodes the state. A missing file is tolerated (mirrors
// babysit's load): it is the normal "no pipeline run yet" case, returned as
// StageNew with no error, since the very first `cenci pipeline prepare <id>`
// run for a ticket must not fail just because no state file exists yet.
func (d *stateDir) load() (State, error) {
	b, err := d.readRaw()
	if errors.Is(err, fs.ErrNotExist) {
		return State{Stage: StageNew}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("decode state file: %w", err)
	}
	return s, nil
}

// peekStage is the best-effort stage read the lock-never-acquired fallbacks of
// Run and Reset report, outside the lock and for display only: anything that
// keeps the state from being read counts as StageNew.
func (d *stateDir) peekStage() Stage {
	s, err := d.load()
	if err != nil {
		return StageNew
	}
	return s.Stage
}

// save writes s atomically: marshal, write to a fresh exclusive temp file in
// the same directory under an unpredictable name, then rename it over the
// state file so a reader never observes a partial write. A symlink planted at a
// predictable temp name cannot be written through, because the create is
// O_EXCL.
func (d *stateDir) save(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	f, tmp, err := d.createTemp()
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = d.root.Remove(tmp)
		return fmt.Errorf("write temp state file: %w", err)
	}
	// File.Chmod acts on the open handle; Root.Chmod would follow a symlink.
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		_ = d.root.Remove(tmp)
		return fmt.Errorf("chmod temp state file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = d.root.Remove(tmp)
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := d.root.Rename(tmp, d.file); err != nil {
		_ = d.root.Remove(tmp)
		return fmt.Errorf("rename temp state file: %w", err)
	}
	return nil
}

// createTemp exclusively creates "."+file+".tmp-<random hex>" (mode 0o600) and
// returns it with its name, retrying a colliding name.
func (d *stateDir) createTemp() (*os.File, string, error) {
	for range maxTempNameAttempts {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, "", fmt.Errorf("create temp state file: %w", err)
		}
		name := "." + d.file + ".tmp-" + hex.EncodeToString(suffix[:])
		f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("create temp state file %s: %w", d.path(name), err)
		}
		return f, name, nil
	}
	return nil, "", fmt.Errorf("create temp state file in %s: every random name was taken", d.dir)
}

// remove deletes the state file itself; a symlink at its place is unlinked, not
// followed.
func (d *stateDir) remove() error {
	if err := d.root.Remove(d.file); err != nil {
		return fmt.Errorf("remove %s: %w", d.displayPath(), err)
	}
	return nil
}

// openLock opens the per-ticket lock file <file>.lock for flock. A missing file
// is created exclusively (O_EXCL, so a dangling symlink planted there is not
// created through); an existing one must be a regular file and the opened file
// must be the one the Lstat saw. Anything else is refused.
func (d *stateDir) openLock() (*os.File, error) {
	name := d.file + ".lock"
	path := d.path(name)
	for range 2 {
		info, err := d.root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			f, cerr := d.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(cerr, fs.ErrExist) {
				continue // created by a concurrent process since the Lstat: judge it
			}
			if cerr != nil {
				return nil, fmt.Errorf("open lock file %s: %w", path, cerr)
			}
			return f, nil
		}
		if err != nil {
			return nil, fmt.Errorf("open lock file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("lock file %s is not a regular file (mode %s)", path, info.Mode().Type())
		}
		f, err := d.root.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, fmt.Errorf("open lock file %s: %w", path, err)
		}
		opened, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("stat lock file %s: %w", path, err)
		}
		if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			_ = f.Close()
			return nil, fmt.Errorf("lock file %s changed while it was being opened", path)
		}
		return f, nil
	}
	return nil, fmt.Errorf("lock file %s changed while it was being opened", path)
}
