package capacity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// storeDirs returns a state root, the capacity parent and the capacity
// directory under it. Nothing is created.
func storeDirs(t *testing.T) (root, parent, dir string) {
	t.Helper()
	root = t.TempDir()
	parent = filepath.Join(root, "cenci")
	return root, parent, filepath.Join(parent, "capacity")
}

// trustedDir creates the capacity directory and its parent at 0700.
func trustedDir(t *testing.T) string {
	t.Helper()
	_, parent, dir := storeDirs(t)
	for _, p := range []string{parent, dir} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDir(t *testing.T) {
	t.Run("absolute XDG_STATE_HOME wins", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("HOME", t.TempDir())
		if got, want := Dir(), filepath.Join(state, "cenci", "capacity"); got != want {
			t.Fatalf("Dir() = %q, want %q", got, want)
		}
	})
	t.Run("relative XDG_STATE_HOME falls back to HOME", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("XDG_STATE_HOME", "relative/state")
		t.Setenv("HOME", home)
		if got, want := Dir(), filepath.Join(home, ".local", "state", "cenci", "capacity"); got != want {
			t.Fatalf("Dir() = %q, want %q", got, want)
		}
	})
	t.Run("nothing resolves", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		if got := Dir(); got != "" {
			t.Fatalf("Dir() = %q, want empty", got)
		}
	})
}

func TestStoreWriteFileCreatesHardenedDirectoryAndFile(t *testing.T) {
	_, parent, dir := storeDirs(t)
	s := NewStore(dir)

	if err := s.WriteFile("state.json", []byte("payload")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	for _, p := range []string{parent, dir} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s mode = %o, want 0700", filepath.Base(p), perm)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
	got, exists, err := s.ReadFile("state.json")
	if err != nil || !exists || string(got) != "payload" {
		t.Fatalf("ReadFile = %q, %v, %v", got, exists, err)
	}
	for _, name := range listDir(t, dir) {
		if strings.HasSuffix(name, ".tmp") {
			t.Errorf("temp file %q left behind", name)
		}
	}
}

func TestStoreWriteFileReplacesExistingContent(t *testing.T) {
	s := NewStore(trustedDir(t))
	for _, content := range []string{"first", "second"} {
		if err := s.WriteFile("state.json", []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := s.ReadFile("state.json")
	if err != nil || string(got) != "second" {
		t.Fatalf("ReadFile = %q, %v; want second", got, err)
	}
}

func TestStoreReadFileAbsentIsNotAnError(t *testing.T) {
	s := NewStore(trustedDir(t))
	got, exists, err := s.ReadFile("missing.json")
	if err != nil || exists || got != nil {
		t.Fatalf("ReadFile = %q, %v, %v; want nil, false, nil", got, exists, err)
	}
}

func TestStoreRejectsUntrustedDirectories(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"symlinked capacity directory": func(t *testing.T) string {
			_, parent, dir := storeDirs(t)
			target := filepath.Join(t.TempDir(), "elsewhere")
			for _, p := range []string{parent, target} {
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, dir); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"symlinked parent directory": func(t *testing.T) string {
			_, parent, dir := storeDirs(t)
			target := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.MkdirAll(filepath.Join(target, "capacity"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, parent); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"group-writable capacity directory": func(t *testing.T) string {
			dir := trustedDir(t)
			if err := os.Chmod(dir, 0o770); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"world-writable capacity directory": func(t *testing.T) string {
			dir := trustedDir(t)
			if err := os.Chmod(dir, 0o707); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"capacity path is a regular file": func(t *testing.T) string {
			_, parent, dir := storeDirs(t)
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := setup(t)
			s := NewStore(dir)

			if err := s.WriteFile("state.json", []byte("payload")); !errors.Is(err, ErrUntrusted) {
				t.Errorf("WriteFile err = %v, want ErrUntrusted", err)
			}
			if _, _, err := s.ReadFile("state.json"); !errors.Is(err, ErrUntrusted) {
				t.Errorf("ReadFile err = %v, want ErrUntrusted", err)
			}
			if err := s.WithLock(func() error { return nil }); !errors.Is(err, ErrUntrusted) {
				t.Errorf("WithLock err = %v, want ErrUntrusted", err)
			}
			// Whatever the path resolves to must hold nothing we wrote.
			resolved, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(resolved); err == nil && info.IsDir() {
				if names := listDir(t, resolved); len(names) != 0 {
					t.Errorf("files written through the untrusted path: %v", names)
				}
			}
		})
	}
}

func TestStoreUnresolvedDirectory(t *testing.T) {
	s := NewStore("")
	if err := s.WriteFile("state.json", []byte("x")); !errors.Is(err, ErrUnresolved) {
		t.Errorf("WriteFile err = %v, want ErrUnresolved", err)
	}
	if _, _, err := s.ReadFile("state.json"); !errors.Is(err, ErrUnresolved) {
		t.Errorf("ReadFile err = %v, want ErrUnresolved", err)
	}
	if err := s.WithLock(func() error { return nil }); !errors.Is(err, ErrUnresolved) {
		t.Errorf("WithLock err = %v, want ErrUnresolved", err)
	}
}

func TestStoreReadFileRejectsSymlinkedFile(t *testing.T) {
	dir := trustedDir(t)
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}

	_, exists, err := NewStore(dir).ReadFile("state.json")
	if !errors.Is(err, ErrNotRegular) || !exists {
		t.Fatalf("ReadFile = exists %v, err %v; want exists and ErrNotRegular", exists, err)
	}
}

func TestStoreWriteFileNeverWritesThroughSymlinkedFile(t *testing.T) {
	dir := trustedDir(t)
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if err := NewStore(dir).WriteFile("state.json", []byte("replacement")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "original" {
		t.Fatalf("symlink target = %q, %v; want untouched", got, err)
	}
}

func TestStoreReadFileRejectsOversizedAndNonRegularFiles(t *testing.T) {
	dir := trustedDir(t)
	s := NewStore(dir)

	if err := os.WriteFile(filepath.Join(dir, "big.json"), make([]byte, 4097), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadFile("big.json"); !errors.Is(err, ErrNotRegular) {
		t.Errorf("oversized: err = %v, want ErrNotRegular", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "exact.json"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadFile("exact.json"); err != nil {
		t.Errorf("4 KiB file: err = %v, want nil", err)
	}

	if err := syscall.Mkfifo(filepath.Join(dir, "fifo.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadFile("fifo.json"); !errors.Is(err, ErrNotRegular) {
		t.Errorf("fifo: err = %v, want ErrNotRegular", err)
	}

	if err := os.Mkdir(filepath.Join(dir, "subdir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadFile("subdir.json"); !errors.Is(err, ErrNotRegular) {
		t.Errorf("directory: err = %v, want ErrNotRegular", err)
	}
}

func TestStoreWithLockSerializesHolders(t *testing.T) {
	dir := trustedDir(t)
	holder, waiter := NewStore(dir), NewStore(dir)
	waiter.lockTimeout = 100 * time.Millisecond

	ran := false
	err := holder.WithLock(func() error {
		return waiter.WithLock(func() error {
			ran = true
			return nil
		})
	})
	if !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("contended WithLock err = %v, want ErrLockUnavailable", err)
	}
	if ran {
		t.Fatal("the contended callback ran without the lock")
	}

	if err := waiter.WithLock(func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("released WithLock = %v (ran %v); want nil and ran", err, ran)
	}
}

func TestStoreWithLockPropagatesCallbackError(t *testing.T) {
	s := NewStore(trustedDir(t))
	sentinel := errors.New("callback failed")
	if err := s.WithLock(func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("WithLock err = %v, want the callback error", err)
	}
}

func TestStoreWithLockRefusesSymlinkedLockFile(t *testing.T) {
	dir := trustedDir(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".lock")); err != nil {
		t.Fatal(err)
	}

	err := NewStore(dir).WithLock(func() error { return nil })
	if !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("WithLock err = %v, want ErrLockUnavailable", err)
	}
}

// A lock-free read races a writer's rename: the file Lstat saw can be replaced
// before it is opened, which reads as a different file. That is a retry, not a
// refusal.
func TestStoreReadFileRetriesWhenAWriterRenamesBetweenLstatAndOpen(t *testing.T) {
	dir := trustedDir(t)
	s := NewStore(dir)
	if err := s.WriteFile("state.json", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	afterReadLstat = func() {
		calls++
		if calls == 1 {
			if err := s.WriteFile("state.json", []byte("v2")); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(func() { afterReadLstat = nil })

	data, exists, err := s.ReadFile("state.json")

	if err != nil || !exists || string(data) != "v2" {
		t.Fatalf("ReadFile = %q, exists %v, err %v; want the replaced file read on the retry", data, exists, err)
	}
}

// The retry is bounded: a file that keeps being replaced still ends in an
// answer, and the one real non-regular case, a symlink, is never retried into
// success.
func TestStoreReadFileGivesUpOnAFileThatKeepsChanging(t *testing.T) {
	dir := trustedDir(t)
	s := NewStore(dir)
	if err := s.WriteFile("state.json", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	afterReadLstat = func() {
		calls++
		if err := s.WriteFile("state.json", []byte("again")); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterReadLstat = nil })

	_, exists, err := s.ReadFile("state.json")

	if !errors.Is(err, ErrRaced) || errors.Is(err, ErrNotRegular) || !exists {
		t.Fatalf("ReadFile = exists %v, err %v; want ErrRaced (not ErrNotRegular) once the retries are spent", exists, err)
	}
	if calls != readAttempts {
		t.Errorf("attempts = %d, want exactly %d", calls, readAttempts)
	}
	assertPathFree(t, err, dir)
}

// A symlink is a hostile path, not a busy writer: it is refused on the first
// attempt as ErrNotRegular, never retried and never reported as a race.
func TestStoreReadFileRefusesASymlinkOnTheFirstAttempt(t *testing.T) {
	dir := trustedDir(t)
	s := NewStore(dir)
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	afterReadLstat = func() { calls++ }
	t.Cleanup(func() { afterReadLstat = nil })

	data, exists, err := s.ReadFile("state.json")

	if !errors.Is(err, ErrNotRegular) || errors.Is(err, ErrRaced) || !exists || string(data) == "secret" {
		t.Fatalf("ReadFile = %q, exists %v, err %v; want ErrNotRegular", data, exists, err)
	}
	if calls != 1 {
		t.Errorf("attempts = %d, want 1: a symlink is not retried", calls)
	}
}

func TestStoreReadFileStillRefusesASymlinkSwappedInBetweenLstatAndOpen(t *testing.T) {
	dir := trustedDir(t)
	s := NewStore(dir)
	if err := s.WriteFile("state.json", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	afterReadLstat = func() {
		path := filepath.Join(dir, "state.json")
		_ = os.Remove(path)
		if err := os.Symlink(outside, path); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterReadLstat = nil })

	data, _, err := s.ReadFile("state.json")

	if !errors.Is(err, ErrNotRegular) || string(data) == "secret" {
		t.Fatalf("ReadFile = %q, %v; want ErrNotRegular and never the symlink target", data, err)
	}
}
