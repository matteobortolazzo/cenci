// Package capacity is the hardened, per-user store of dispatch launch capacity
// under $XDG_STATE_HOME/cenci/capacity/, and the persisted local-day launch
// counter (DailyQuota) built on it.
//
// The store is a directory of small regular files guarded by one whole-store
// flock on `.lock`. Every write is a temp file plus rename, every read is
// bounded and refuses symlinks, and the directory must be a real directory
// owned by the current uid that is not group- or other-writable. Anything the
// store cannot read with certainty is a typed error, never a reset: a caller
// that gates on the answer must fail closed.
//
// This is a leaf package: it imports only the standard library, and the
// directory and the clock are injected so tests and other packages own them.
// The layout mirrors internal/modelswitch; keep the directory rules in sync
// with its classifyDir.
//
// Every error is path-free and never contains " dispatch " or " skip:", because
// the reasons end up in dispatch skip and log lines that lazyboards parses.
package capacity

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Typed, path-free errors. Detect them with errors.Is.
var (
	// ErrUnresolved means the state directory could not be resolved.
	ErrUnresolved = errors.New("the state directory cannot be resolved")
	// ErrUntrusted means the capacity directory, or the directory above it, is
	// a symlink, not a directory, foreign-owned or group/other-writable.
	ErrUntrusted = errors.New("the capacity store is untrusted (symlink, not a directory, foreign-owned or group/other-writable)")
	// ErrNotRegular means a store file is a symlink, not a regular file, or
	// larger than the size cap.
	ErrNotRegular = errors.New("a capacity file is not a regular file of bounded size")
	// ErrCorrupt means a store file does not decode strictly.
	ErrCorrupt = errors.New("a capacity file is corrupt")
	// ErrUnknownSchema means a store file carries a schema this build does not
	// understand.
	ErrUnknownSchema = errors.New("a capacity file has an unknown schema")
	// ErrLockUnavailable means the whole-store lock could not be taken in time.
	ErrLockUnavailable = errors.New("the capacity store lock is unavailable")
	// ErrIO means any other read or write failure.
	ErrIO = errors.New("the capacity store could not be read or written")
)

const (
	maxFileBytes       = 4096
	defaultLockTimeout = 3 * time.Second
	lockPoll           = 10 * time.Millisecond
	lockName           = ".lock"
)

// Dir returns <S>/cenci/capacity where <S> is $XDG_STATE_HOME when it is
// absolute, else $HOME/.local/state. It returns "" when neither resolves.
func Dir() string {
	state := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		home := os.Getenv("HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "cenci", "capacity")
}

// Store is the capacity directory. It holds no open resources.
type Store struct {
	dir         string
	lockTimeout time.Duration
}

// NewStore returns a Store over dir. An empty dir is accepted and makes every
// operation return ErrUnresolved.
func NewStore(dir string) *Store {
	return &Store{dir: dir, lockTimeout: defaultLockTimeout}
}

type dirClass int

const (
	dirAbsent dirClass = iota
	dirTrusted
	dirUntrusted
)

// classifyDir classifies path with one Lstat. Only fs.ErrNotExist is absent;
// any other failure is untrusted. A present path is trusted when it is a real
// directory owned by the current uid and not group/other writable.
//
// These rules mirror modelswitch.classifyDir, hookhealth.classifyDir and
// hardenDir in pkg/watch/socket.go; keep them in sync.
func classifyDir(path string) dirClass {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return dirAbsent
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return dirUntrusted
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return dirUntrusted
	}
	if info.Mode().Perm()&0o022 != 0 {
		return dirUntrusted
	}
	return dirTrusted
}

// ensureDir creates path at 0700 when absent, then verifies it.
func ensureDir(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return ErrIO
		}
		// A concurrent creator losing the race is fine; the check below
		// verifies whatever exists.
		_ = os.Mkdir(path, 0o700)
	}
	switch classifyDir(path) {
	case dirTrusted:
		return nil
	case dirAbsent:
		return ErrIO
	default:
		return ErrUntrusted
	}
}

// prepare creates and verifies the state root's cenci directory and the store
// directory.
func (s *Store) prepare() error {
	if s.dir == "" {
		return ErrUnresolved
	}
	if err := ensureDir(filepath.Dir(s.dir)); err != nil {
		return err
	}
	return ensureDir(s.dir)
}

// check verifies the store without creating anything. present is false when
// the store directory, or the one above it, does not exist yet.
func (s *Store) check() (present bool, err error) {
	if s.dir == "" {
		return false, ErrUnresolved
	}
	for _, p := range []string{filepath.Dir(s.dir), s.dir} {
		switch classifyDir(p) {
		case dirAbsent:
			return false, nil
		case dirUntrusted:
			return false, ErrUntrusted
		}
	}
	return true, nil
}

// lock takes the whole-store flock, polling so a stuck holder cannot hang a
// pass past the timeout. Without create, an absent lock file means no writer
// has ever run, so there is nothing to wait for and unlock is a no-op.
func (s *Store) lock(create bool) (unlock func(), err error) {
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(filepath.Join(s.dir, lockName), flags, 0o600)
	if errors.Is(err, fs.ErrNotExist) && !create {
		return func() {}, nil
	}
	if err != nil {
		return nil, ErrLockUnavailable
	}
	deadline := time.Now().Add(s.lockTimeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrLockUnavailable
		}
		time.Sleep(lockPoll)
	}
}

// WithLock creates and verifies the store, takes the whole-store lock and runs
// fn under it. It is the entry point for read-modify-write cycles.
func (s *Store) WithLock(fn func() error) error {
	if err := s.prepare(); err != nil {
		return err
	}
	unlock, err := s.lock(true)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// withReadLock runs fn under the lock when the store already exists, and
// creates nothing: an absent store is read as empty.
func (s *Store) withReadLock(fn func() error) error {
	present, err := s.check()
	if err != nil {
		return err
	}
	if !present {
		return fn()
	}
	unlock, err := s.lock(false)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func validName(name string) bool {
	return name != "" && name == filepath.Base(name) && !strings.HasPrefix(name, ".")
}

// ReadFile reads the store file name without creating or repairing anything.
// An absent store or file is (nil, false, nil). A file that is a symlink, not a
// regular file or over the size cap is ErrNotRegular, and exists is true.
func (s *Store) ReadFile(name string) (data []byte, exists bool, err error) {
	if !validName(name) {
		return nil, false, ErrIO
	}
	present, err := s.check()
	if err != nil || !present {
		return nil, false, err
	}
	path := filepath.Join(s.dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, ErrIO
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, true, ErrNotRegular
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, true, ErrNotRegular
	}
	if err != nil {
		return nil, true, ErrIO
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(info, fi) {
		return nil, true, ErrNotRegular
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, true, ErrIO
	}
	if len(buf) > maxFileBytes {
		return nil, true, ErrNotRegular
	}
	return buf, true, nil
}

// WriteFile creates and verifies the store, then replaces the store file name
// with data through a 0600 temp file and a rename. A symlink at name is
// replaced, never written through. Callers hold WithLock around a
// read-modify-write.
func (s *Store) WriteFile(name string, data []byte) error {
	if !validName(name) {
		return ErrIO
	}
	if err := s.prepare(); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "."+name+".*.tmp")
	if err != nil {
		return ErrIO
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil || os.Chmod(tmp, 0o600) != nil || os.Rename(tmp, filepath.Join(s.dir, name)) != nil {
		_ = os.Remove(tmp)
		return ErrIO
	}
	return nil
}
