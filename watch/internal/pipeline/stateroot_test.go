package pipeline

// Tests for the os.Root-scoped state-file access (ticket #1306). The state
// directory lives in the agent-writable bind-mounted repo, so every entry point
// that reads or writes <id>.json (ClearLaunch, SetArtifacts, GetArtifacts, Run,
// Reset) must refuse a planted symlink, FIFO or oversized file and must not be
// redirected by a directory swapped after the walk. The tests drive those
// exported entry points; the Go-behaviour pins at the bottom record the
// os.Root semantics the design depends on.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	hostileTicketID    = "42"
	secretSessionValue = "SECRET-SESSION-VALUE"
	// seededState is a ticket-42 state file carrying the launch triple plus a
	// distinctive session value, so a test can prove an error never echoes file
	// content and that ClearLaunch has something to clear.
	seededState = `{"schemaVersion":2,"id":"42","stage":"prepared","updatedAt":"2024-01-01T00:00:00Z",` +
		`"session":{"runId":"` + secretSessionValue + `","launchId":"0123456789abcdef0123456789abcdef","container":"claude-cenci-repo","runtime":"docker"}}`
	// fifoDeadline is how long an entry point may take on a FIFO state file
	// before the test calls it blocked.
	fifoDeadline = 5 * time.Second
	// recordedBranch is what the SetArtifacts entry point records.
	recordedBranch = "feature/42-x"
)

// -- fixtures ---------------------------------------------------------------

// stateLoc is where an entry point finds the state: a repo root or an explicit
// state directory.
type stateLoc struct{ repoRoot, stateDir string }

func (l stateLoc) artifactOpts() ArtifactOpts {
	return ArtifactOpts{ID: hostileTicketID, RepoRoot: l.repoRoot, StateDir: l.stateDir}
}

// entryPoint is one exported function that touches the state file.
type entryPoint struct {
	name string
	call func(l stateLoc) error
}

var (
	entryClearLaunch = entryPoint{"ClearLaunch", func(l stateLoc) error { return ClearLaunch(l.artifactOpts()) }}
	entryGetArtifact = entryPoint{"GetArtifacts", func(l stateLoc) error { _, err := GetArtifacts(l.artifactOpts()); return err }}
	entrySetArtifact = entryPoint{"SetArtifacts", func(l stateLoc) error {
		o := l.artifactOpts()
		o.Branch = recordedBranch
		_, err := SetArtifacts(o)
		return err
	}}
	entryRun = entryPoint{"Run", func(l stateLoc) error {
		_, err := Run(Opts{Stage: "prepare", ID: hostileTicketID, RepoRoot: l.repoRoot, StateDir: l.stateDir})
		return err
	}}
	entryReset = entryPoint{"Reset", func(l stateLoc) error {
		_, err := Reset(ResetOpts{ID: hostileTicketID, RepoRoot: l.repoRoot, StateDir: l.stateDir})
		return err
	}}

	allEntries = []entryPoint{entryClearLaunch, entryGetArtifact, entrySetArtifact, entryRun, entryReset}
	// readingEntries are the entry points that must fail on an unusable state
	// file (Reset instead warns and deletes it, see reset_test.go).
	readingEntries = []entryPoint{entryClearLaunch, entryGetArtifact, entrySetArtifact, entryRun}
)

// layout builds a fresh state directory for one of the two location modes and
// returns how to reach it plus the directory that holds <id>.json.
type layout struct {
	name  string
	setup func(t *testing.T) (stateLoc, string)
}

var layouts = []layout{
	{"repo layout", func(t *testing.T) (stateLoc, string) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".cenci", "pipeline")
		mustMkdirAll(t, dir)
		return stateLoc{repoRoot: repo}, dir
	}},
	{"explicit state dir", func(t *testing.T) (stateLoc, string) {
		dir := t.TempDir()
		return stateLoc{stateDir: dir}, dir
	}},
}

// forEachEntry runs fn once per entry point as a subtest. Run's prepare stage
// shells out to gh for a brand new ticket, so gh is stubbed to succeed.
func forEachEntry(t *testing.T, entries []entryPoint, fn func(t *testing.T, e entryPoint)) {
	t.Helper()
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			stubGhOK(t)
			fn(t, e)
		})
	}
}

func stubGhOK(t *testing.T) {
	t.Helper()
	original := command
	command = func(string, ...string) ([]byte, error) { return []byte(`{"number":42}`), nil }
	t.Cleanup(func() { command = original })
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// readStateFile decodes the state file at path without going through the code
// under test.
func readStateFile(t *testing.T, path string) State {
	t.Helper()
	var s State
	if err := json.Unmarshal([]byte(mustReadFile(t, path)), &s); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return s
}

// makeFifo creates a FIFO with no writer at path. Its cleanup opens (and
// closes) a writer, so an entry point that wrongly blocked in open(2) on it is
// released instead of leaking a stuck goroutine into later tests.
func makeFifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}
	t.Cleanup(func() {
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})
}

// completesWithin runs fn and fails the test when it has not returned within d.
func completesWithin[T any](t *testing.T, d time.Duration, fn func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- fn() }()
	select {
	case v := <-done:
		return v
	case <-time.After(d):
		t.Fatalf("call did not return within %v: it is blocked on the state file", d)
		var zero T
		return zero
	}
}

// snapshotTree records every entry under root without following symlinks:
// regular files by content, symlinks by target, directories by name.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			snap[rel] = "dir"
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snap[rel] = "symlink->" + target
		case info.Mode().IsRegular():
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snap[rel] = "file:" + string(b)
		default:
			snap[rel] = "special:" + info.Mode().Type().String()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	if after := snapshotTree(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("tree %s changed:\n before: %v\n  after: %v", root, before, after)
	}
}

func assertErrNamesPathNotContent(t *testing.T, err error, path string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got a nil error, want one naming %s", path)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
	if strings.Contains(err.Error(), secretSessionValue) {
		t.Errorf("error %q leaks the state file content", err)
	}
}

// paddedJSON pads body with trailing spaces to exactly size bytes (still valid
// JSON), so only the size, not the content, can make a read refuse it.
func paddedJSON(t *testing.T, body string, size int) string {
	t.Helper()
	if len(body) > size {
		t.Fatalf("fixture body is %d bytes, larger than the %d requested", len(body), size)
	}
	return body + strings.Repeat(" ", size-len(body))
}

func setMaxStateFileBytes(t *testing.T, n int) {
	t.Helper()
	original := maxStateFileBytes
	maxStateFileBytes = n
	t.Cleanup(func() { maxStateFileBytes = original })
}

// installWalkSeam sets afterStateDirWalkFn to run swap once, on the first call,
// and returns a counter of how often the helper invoked the seam.
func installWalkSeam(t *testing.T, swap func()) *int {
	t.Helper()
	fired := 0
	original := afterStateDirWalkFn
	afterStateDirWalkFn = func() {
		fired++
		if fired == 1 {
			swap()
		}
	}
	t.Cleanup(func() { afterStateDirWalkFn = original })
	return &fired
}

// -- AC 25: a directory swapped after the walk cannot redirect a write -------

func TestStateWrites_DirectorySwappedAfterTheWalk_StayInTheOriginalDirectory(t *testing.T) {
	swaps := []struct {
		name string
		// swap moves the real directory aside, plants a symlink to outside in
		// its place and returns the state dir the walk had already opened.
		swap func(t *testing.T, repo, outside string) (opened string)
	}{
		{".cenci/pipeline swapped", func(t *testing.T, repo, outside string) string {
			pipelineDir := filepath.Join(repo, ".cenci", "pipeline")
			moved := pipelineDir + ".moved"
			if err := os.Rename(pipelineDir, moved); err != nil {
				t.Fatalf("move pipeline dir aside: %v", err)
			}
			mustSymlink(t, outside, pipelineDir)
			return moved
		}},
		{".cenci swapped", func(t *testing.T, repo, outside string) string {
			cenciDir := filepath.Join(repo, ".cenci")
			moved := cenciDir + ".moved"
			if err := os.Rename(cenciDir, moved); err != nil {
				t.Fatalf("move .cenci aside: %v", err)
			}
			mustSymlink(t, outside, cenciDir)
			return filepath.Join(moved, "pipeline")
		}},
	}
	writers := []struct {
		entry entryPoint
		// effect reports whether the write landed in the state file.
		effect func(s State) bool
	}{
		{entryClearLaunch, func(s State) bool { _, ok := s.Session[SessionKeyLaunchID]; return !ok }},
		{entrySetArtifact, func(s State) bool { return s.Branch == recordedBranch }},
	}
	for _, sw := range swaps {
		for _, w := range writers {
			t.Run(w.entry.name+"/"+sw.name, func(t *testing.T) {
				repo := t.TempDir()
				pipelineDir := filepath.Join(repo, ".cenci", "pipeline")
				mustMkdirAll(t, pipelineDir)
				mustWriteFile(t, filepath.Join(pipelineDir, "42.json"), seededState)
				outside := t.TempDir()
				before := snapshotTree(t, outside)
				var opened string
				fired := installWalkSeam(t, func() { opened = sw.swap(t, repo, outside) })

				if err := w.entry.call(stateLoc{repoRoot: repo}); err != nil {
					t.Fatalf("%s: %v", w.entry.name, err)
				}

				if *fired == 0 {
					t.Fatal("afterStateDirWalkFn was never invoked: the state directory walk must run the seam before its first file operation")
				}
				assertTreeUnchanged(t, outside, before)
				if !w.effect(readStateFile(t, filepath.Join(opened, "42.json"))) {
					t.Errorf("%s did not land in the directory the walk opened (%s)", w.entry.name, opened)
				}
				if _, err := os.Lstat(filepath.Join(opened, "42.json.lock")); err != nil {
					t.Errorf("lock file is not in the directory the walk opened: %v", err)
				}
			})
		}
	}
}

// installLevelSeam sets afterDirLstatFn to run swap once, when the walk has just
// Lstat'd the directory at level (and not yet opened it), and returns a counter
// of how often that happened.
func installLevelSeam(t *testing.T, level string, swap func()) *int {
	t.Helper()
	fired := 0
	original := afterDirLstatFn
	afterDirLstatFn = func(path string) {
		if path == level {
			fired++
			swap()
		}
	}
	t.Cleanup(func() { afterDirLstatFn = original })
	return &fired
}

// -- AC 2: each level's identity is checked between its Lstat and its open ----

func TestStateDirWalk_LevelSwappedBetweenLstatAndOpen_IsRefused(t *testing.T) {
	levels := []struct {
		name string
		// build returns the location, the directory path whose walk step gets
		// swapped, and a sibling decoy laid out exactly like it (with a seeded
		// state file), so an open that wrongly succeeded would have found
		// something to read and write.
		build func(t *testing.T) (loc stateLoc, level, decoy string)
	}{
		{".cenci", func(t *testing.T) (stateLoc, string, string) {
			repo := t.TempDir()
			mustMkdirAll(t, filepath.Join(repo, ".cenci", "pipeline"))
			mustMkdirAll(t, filepath.Join(repo, "decoy", "pipeline"))
			mustWriteFile(t, filepath.Join(repo, "decoy", "pipeline", "42.json"), seededState)
			return stateLoc{repoRoot: repo}, filepath.Join(repo, ".cenci"), filepath.Join(repo, "decoy")
		}},
		{".cenci/pipeline", func(t *testing.T) (stateLoc, string, string) {
			repo := t.TempDir()
			mustMkdirAll(t, filepath.Join(repo, ".cenci", "pipeline"))
			mustMkdirAll(t, filepath.Join(repo, ".cenci", "decoy"))
			mustWriteFile(t, filepath.Join(repo, ".cenci", "decoy", "42.json"), seededState)
			return stateLoc{repoRoot: repo}, filepath.Join(repo, ".cenci", "pipeline"), filepath.Join(repo, ".cenci", "decoy")
		}},
		{"explicit --state-dir", func(t *testing.T) (stateLoc, string, string) {
			parent := t.TempDir()
			state := filepath.Join(parent, "state")
			mustMkdirAll(t, state)
			mustMkdirAll(t, filepath.Join(parent, "decoy"))
			mustWriteFile(t, filepath.Join(parent, "decoy", "42.json"), seededState)
			return stateLoc{stateDir: state}, state, filepath.Join(parent, "decoy")
		}},
	}
	swaps := []struct {
		name string
		// swap moves the Lstat'd directory aside and puts the decoy at its path,
		// as a different directory or as an in-root symlink to it, and returns
		// where the swapped-in directory's content now lives.
		swap func(t *testing.T, level, decoy string) (swappedIn string)
	}{
		{"replaced by a different directory", func(t *testing.T, level, decoy string) string {
			if err := os.Rename(level, level+".moved"); err != nil {
				t.Fatalf("move %s aside: %v", level, err)
			}
			if err := os.Rename(decoy, level); err != nil {
				t.Fatalf("move decoy into %s: %v", level, err)
			}
			return level
		}},
		{"replaced by an in-root symlink", func(t *testing.T, level, decoy string) string {
			if err := os.Rename(level, level+".moved"); err != nil {
				t.Fatalf("move %s aside: %v", level, err)
			}
			mustSymlink(t, filepath.Base(decoy), level)
			return decoy
		}},
	}
	for _, lv := range levels {
		for _, sw := range swaps {
			t.Run(lv.name+"/"+sw.name, func(t *testing.T) {
				forEachEntry(t, allEntries, func(t *testing.T, e entryPoint) {
					loc, level, decoy := lv.build(t)
					decoyBefore := snapshotTree(t, decoy)
					var swappedIn string
					fired := installLevelSeam(t, level, func() { swappedIn = sw.swap(t, level, decoy) })

					err := e.call(loc)

					if *fired != 1 {
						t.Fatalf("seam fired %d times for %s, want 1: the walk must run it between that level's Lstat and open", *fired, level)
					}
					assertErrNamesPathNotContent(t, err, level)
					if !strings.Contains(err.Error(), "changed while it was being opened") {
						t.Errorf("error %q does not say the directory changed while it was being opened", err)
					}
					assertTreeUnchanged(t, swappedIn, decoyBefore)
				})
			})
		}
	}
}

// -- AC 26: static directory symlinks ---------------------------------------

func TestSymlinkedStateDirectory_IsRefusedByEveryEntryPoint(t *testing.T) {
	cases := []struct {
		name string
		// build returns the location, the directory path the error must name
		// and the directory the symlink points at.
		build func(t *testing.T) (loc stateLoc, refused, target string)
	}{
		{".cenci is a symlink", func(t *testing.T) (stateLoc, string, string) {
			repo := t.TempDir()
			outsideCenci := t.TempDir()
			mustMkdirAll(t, filepath.Join(outsideCenci, "pipeline"))
			mustWriteFile(t, filepath.Join(outsideCenci, "pipeline", "42.json"), seededState)
			mustSymlink(t, outsideCenci, filepath.Join(repo, ".cenci"))
			return stateLoc{repoRoot: repo}, filepath.Join(repo, ".cenci"), outsideCenci
		}},
		{".cenci/pipeline is a symlink", func(t *testing.T) (stateLoc, string, string) {
			repo := t.TempDir()
			mustMkdirAll(t, filepath.Join(repo, ".cenci"))
			outside := t.TempDir()
			mustWriteFile(t, filepath.Join(outside, "42.json"), seededState)
			mustSymlink(t, outside, filepath.Join(repo, ".cenci", "pipeline"))
			return stateLoc{repoRoot: repo}, filepath.Join(repo, ".cenci", "pipeline"), outside
		}},
		{"explicit --state-dir is a symlink", func(t *testing.T) (stateLoc, string, string) {
			outside := t.TempDir()
			mustWriteFile(t, filepath.Join(outside, "42.json"), seededState)
			link := filepath.Join(t.TempDir(), "state-link")
			mustSymlink(t, outside, link)
			return stateLoc{stateDir: link}, link, outside
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			forEachEntry(t, allEntries, func(t *testing.T, e entryPoint) {
				loc, refused, target := c.build(t)
				before := snapshotTree(t, target)

				err := e.call(loc)

				assertErrNamesPathNotContent(t, err, refused)
				assertTreeUnchanged(t, target, before)
			})
		})
	}
}

// -- AC 27 / Edge 1: the read cap -------------------------------------------

func TestGetArtifacts_StateFileExactlyAtTheCap_IsDecoded(t *testing.T) {
	const capBytes = 512
	setMaxStateFileBytes(t, capBytes)
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			loc, dir := l.setup(t)
			mustWriteFile(t, filepath.Join(dir, "42.json"), paddedJSON(t, seededState, capBytes))

			s, err := GetArtifacts(loc.artifactOpts())
			if err != nil {
				t.Fatalf("GetArtifacts on a state file of exactly the cap: %v", err)
			}
			if s.Stage != StagePrepared {
				t.Errorf("Stage = %q, want %q (the file must be decoded, not just accepted)", s.Stage, StagePrepared)
			}
		})
	}
}

func TestOversizedStateFile_IsRefusedWithLargerThan_AndNeverRewritten(t *testing.T) {
	const capBytes = 512
	setMaxStateFileBytes(t, capBytes)
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			forEachEntry(t, readingEntries, func(t *testing.T, e entryPoint) {
				loc, dir := l.setup(t)
				path := filepath.Join(dir, "42.json")
				oversized := paddedJSON(t, seededState, capBytes+1)
				mustWriteFile(t, path, oversized)

				err := e.call(loc)

				assertErrNamesPathNotContent(t, err, path)
				if !strings.Contains(err.Error(), "larger than") {
					t.Errorf("error %q does not say the state file is larger than the limit", err)
				}
				if got := mustReadFile(t, path); got != oversized {
					t.Errorf("oversized state file was rewritten (%d bytes, was %d)", len(got), len(oversized))
				}
			})
		})
	}
}

// -- AC 28 / Edge 2 / Edge 4: a FIFO must fail fast, never block ------------

func TestFifoStateFile_FailsFastWithNotARegularFile(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			forEachEntry(t, readingEntries, func(t *testing.T, e entryPoint) {
				loc, dir := l.setup(t)
				path := filepath.Join(dir, "42.json")
				makeFifo(t, path)

				err := completesWithin(t, fifoDeadline, func() error { return e.call(loc) })

				assertErrNamesPathNotContent(t, err, path)
				if !strings.Contains(err.Error(), "not a regular file") {
					t.Errorf("error %q does not say the state file is not a regular file", err)
				}
			})
		})
	}
}

// failFlock makes every lock acquisition fail, so Run and Reset take their
// "lock never acquired" branch and peek at the on-disk stage outside the lock.
// It fails with EBADF rather than EWOULDBLOCK: Run and Reset build their retry
// policy from defaultRetryConfig (real sleeps, 7.5s of backoff) and no seam
// exists for it, while any acquisition failure reaches the same peek.
func failFlock(t *testing.T) {
	t.Helper()
	original := flockFn
	flockFn = func(int, int) error { return syscall.EBADF }
	t.Cleanup(func() { flockFn = original })
}

func TestRun_LockNotAcquired_PeekAtAFifoStateFileReturnsNewPromptly(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			stubGhOK(t)
			failFlock(t)
			loc, dir := l.setup(t)
			makeFifo(t, filepath.Join(dir, "42.json"))

			type result struct {
				out Output
				err error
			}
			got := completesWithin(t, fifoDeadline, func() result {
				out, err := Run(Opts{Stage: "prepare", ID: hostileTicketID, RepoRoot: loc.repoRoot, StateDir: loc.stateDir})
				return result{out, err}
			})

			if got.err == nil {
				t.Error("Run returned nil although the lock was never acquired")
			}
			if got.out.State != string(StageNew) {
				t.Errorf("Output.State = %q, want %q for an unreadable state file", got.out.State, StageNew)
			}
		})
	}
}

func TestReset_LockNotAcquired_PeekAtAFifoStateFileReturnsNewPromptly(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			failFlock(t)
			loc, dir := l.setup(t)
			makeFifo(t, filepath.Join(dir, "42.json"))

			type result struct {
				out Output
				err error
			}
			got := completesWithin(t, fifoDeadline, func() result {
				out, err := Reset(ResetOpts{ID: hostileTicketID, RepoRoot: loc.repoRoot, StateDir: loc.stateDir})
				return result{out, err}
			})

			if got.err == nil {
				t.Error("Reset returned nil although the lock was never acquired")
			}
			if got.out.State != string(StageNew) {
				t.Errorf("Output.State = %q, want %q for an unreadable state file", got.out.State, StageNew)
			}
		})
	}
}

// -- AC 29: a symlinked state file is refused -------------------------------

func TestSymlinkedStateFile_IsRefusedAndItsTargetLeftAlone(t *testing.T) {
	targets := []struct {
		name string
		// place returns the target path for a link in dir.
		place func(t *testing.T, dir string) string
	}{
		{"target inside the state directory", func(t *testing.T, dir string) string { return filepath.Join(dir, "real-state.json") }},
		{"target outside the state directory", func(t *testing.T, dir string) string { return filepath.Join(t.TempDir(), "victim.json") }},
	}
	for _, l := range layouts {
		for _, tg := range targets {
			t.Run(l.name+"/"+tg.name, func(t *testing.T) {
				forEachEntry(t, readingEntries, func(t *testing.T, e entryPoint) {
					loc, dir := l.setup(t)
					target := tg.place(t, dir)
					mustWriteFile(t, target, seededState)
					link := filepath.Join(dir, "42.json")
					mustSymlink(t, target, link)

					err := e.call(loc)

					assertErrNamesPathNotContent(t, err, link)
					if !strings.Contains(err.Error(), "not a regular file") {
						t.Errorf("error %q does not say the state file is not a regular file", err)
					}
					if got := mustReadFile(t, target); got != seededState {
						t.Errorf("symlink target was modified through the link:\n%s", got)
					}
					if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
						t.Errorf("the planted symlink at %s was replaced (lstat = %v, err = %v)", link, info, err)
					}
				})
			})
		}
	}
}

// -- AC 30: a symlinked lock file is refused --------------------------------

func TestSymlinkedLockFile_IsRefusedAndNothingIsCreatedOrModifiedAtTheTarget(t *testing.T) {
	const keptLockBody = "existing lock target"
	targets := []struct {
		name string
		// place prepares the link's target in dir and returns its path; an
		// existing target holds keptLockBody, a dangling one does not exist.
		place func(t *testing.T, dir string) (target string, exists bool)
	}{
		{"dangling target inside the state directory", func(t *testing.T, dir string) (string, bool) {
			return filepath.Join(dir, "lock-victim"), false
		}},
		{"existing target inside the state directory", func(t *testing.T, dir string) (string, bool) {
			target := filepath.Join(dir, "real.lock")
			mustWriteFile(t, target, keptLockBody)
			return target, true
		}},
		{"dangling target outside the state directory", func(t *testing.T, dir string) (string, bool) {
			return filepath.Join(t.TempDir(), "lock-victim"), false
		}},
	}
	lockingEntries := []entryPoint{entryRun, entrySetArtifact, entryReset, entryClearLaunch}
	for _, tg := range targets {
		t.Run(tg.name, func(t *testing.T) {
			forEachEntry(t, lockingEntries, func(t *testing.T, e entryPoint) {
				loc, dir := layouts[0].setup(t)
				statePath := filepath.Join(dir, "42.json")
				mustWriteFile(t, statePath, seededState)
				target, exists := tg.place(t, dir)
				lockPath := filepath.Join(dir, "42.json.lock")
				mustSymlink(t, target, lockPath)

				err := e.call(loc)

				assertErrNamesPathNotContent(t, err, lockPath)
				if errors.Is(err, ErrLockContention) {
					t.Errorf("error %v reports lock contention, want a refusal of the symlinked lock file", err)
				}
				if exists {
					if got := mustReadFile(t, target); got != keptLockBody {
						t.Errorf("lock symlink target was modified: %q", got)
					}
				} else if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
					t.Errorf("the lock symlink was followed and created %s (lstat err = %v)", target, statErr)
				}
				if got := mustReadFile(t, statePath); got != seededState {
					t.Errorf("state file changed although the lock was refused:\n%s", got)
				}
			})
		})
	}
}

// -- AC 34: first use creates plain directories and a 0644 file --------------

func TestRunAndSetArtifacts_NoStateDirectoryYet_CreateItWithTheDocumentedModes(t *testing.T) {
	// The directory mode is the requested 0755 minus the umask, so pin the umask
	// the production default assumes.
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })

	forEachEntry(t, []entryPoint{entryRun, entrySetArtifact}, func(t *testing.T, e entryPoint) {
		repo := t.TempDir()

		if err := e.call(stateLoc{repoRoot: repo}); err != nil {
			t.Fatalf("%s on a repo without .cenci/pipeline: %v", e.name, err)
		}

		for _, dir := range []string{filepath.Join(repo, ".cenci"), filepath.Join(repo, ".cenci", "pipeline")} {
			info, err := os.Lstat(dir)
			if err != nil {
				t.Fatalf("lstat %s: %v", dir, err)
			}
			if !info.IsDir() {
				t.Errorf("%s is %s, want a plain directory", dir, info.Mode().Type())
			}
			if perm := info.Mode().Perm(); perm != 0o755 {
				t.Errorf("%s mode = %o, want 755", dir, perm)
			}
		}
		stateFile := filepath.Join(repo, ".cenci", "pipeline", "42.json")
		info, err := os.Lstat(stateFile)
		if err != nil {
			t.Fatalf("lstat %s: %v", stateFile, err)
		}
		if !info.Mode().IsRegular() {
			t.Errorf("%s is %s, want a regular file", stateFile, info.Mode().Type())
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("%s mode = %o, want 644", stateFile, perm)
		}
	})
}

// -- Edge 7: .cenci is a regular file ---------------------------------------

func TestRunAndSetArtifacts_CenciIsARegularFile_FailNamingItAndCreateNothing(t *testing.T) {
	forEachEntry(t, []entryPoint{entryRun, entrySetArtifact}, func(t *testing.T, e entryPoint) {
		repo := t.TempDir()
		cenci := filepath.Join(repo, ".cenci")
		mustWriteFile(t, cenci, "not a directory")
		before := snapshotTree(t, repo)

		err := e.call(stateLoc{repoRoot: repo})

		assertErrNamesPathNotContent(t, err, cenci)
		assertTreeUnchanged(t, repo, before)
	})
}

// -- Go behaviour the design rests on ---------------------------------------
//
// os.Root.OpenFile does not honour O_NOFOLLOW for a symlink in the last path
// component, and O_CREATE creates through a dangling one. The state helper
// therefore pairs Lstat with Fstat and SameFile and creates with O_EXCL. If a
// newer toolchain changes any of this, these fail loudly so the design is
// revisited rather than silently resting on stale assumptions.

func TestGoRoot_OpenFile_FollowsAFinalInRootSymlinkDespiteONofollow(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "real"), "payload")
	mustSymlink(t, "real", filepath.Join(dir, "link"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := root.OpenFile("link", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("Root.OpenFile(link, O_NOFOLLOW) = %v; this toolchain now refuses a final in-root symlink, so revisit the Lstat/SameFile pairing", err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, len("payload"))
	if _, err := f.Read(buf); err != nil || string(buf) != "payload" {
		t.Errorf("read through the link = %q, %v, want the target's content", buf, err)
	}
}

func TestGoRoot_OpenFile_OCreateThroughADanglingInRootSymlinkCreatesTheTarget(t *testing.T) {
	dir := t.TempDir()
	mustSymlink(t, "target", filepath.Join(dir, "link"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := root.OpenFile("link", os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatalf("Root.OpenFile(link, O_CREATE) = %v; this toolchain now refuses to create through a dangling symlink, so revisit the lock-open design", err)
	}
	_ = f.Close()
	if _, err := os.Lstat(filepath.Join(dir, "target")); err != nil {
		t.Errorf("O_CREATE through the dangling link did not create its target: %v", err)
	}
}

func TestGoRoot_OpenFile_OExclOnADanglingSymlinkFailsWithEEXIST(t *testing.T) {
	dir := t.TempDir()
	mustSymlink(t, "target", filepath.Join(dir, "link"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	f, err := root.OpenFile("link", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_ = f.Close()
		t.Fatal("Root.OpenFile(link, O_CREATE|O_EXCL) succeeded on a dangling symlink, want EEXIST")
	}
	if !errors.Is(err, syscall.EEXIST) {
		t.Errorf("Root.OpenFile(link, O_CREATE|O_EXCL) = %v, want EEXIST", err)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, "target")); !os.IsNotExist(statErr) {
		t.Errorf("O_EXCL created the symlink's target (lstat err = %v)", statErr)
	}
}
