package pipeline

// Test-only path-based wrappers over the os.Root-scoped state access (ticket
// #1306). Production code reaches the state only through a stateDir; these let
// the tests that seed or inspect a state file by path keep doing so.

import (
	"os"
	"path/filepath"
	"testing"
)

// openStateDirAt opens a stateDir on dir (created when missing) serving the
// state file named base, the way an explicit --state-dir does.
func openStateDirAt(dir, base string) (*stateDir, error) {
	return openStateDir(stateLocation{stateDir: dir, display: filepath.Join(dir, base)}, true)
}

// openTestStateDir is openStateDirAt for a test, closed with the test.
func openTestStateDir(t *testing.T, dir, base string) *stateDir {
	t.Helper()
	d, err := openStateDirAt(dir, base)
	if err != nil {
		t.Fatalf("open state dir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// loadState reads the state file at path; a missing file is StageNew.
func loadState(path string) (State, error) {
	d, err := openStateDirAt(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return State{}, err
	}
	defer func() { _ = d.Close() }()
	return d.load()
}

// saveState writes s to the state file at path, creating its directory.
func saveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	d, err := openStateDirAt(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.save(s)
}
