package daemon

import (
	"os"
	"testing"
)

// TestMain points XDG_STATE_HOME at a throwaway directory for the whole
// package. deliverEvent records hook-health diagnostics under
// $XDG_STATE_HOME/cenci/hook-health (#1239), so without this the existing
// failure-path tests would write into the developer's real store
// (watch/docs/test-isolation.md). Tests that need their own store override it
// with t.Setenv.
func TestMain(m *testing.M) {
	state, err := os.MkdirTemp("", "cenci-daemon-state-*")
	if err != nil {
		panic("failed to create state dir: " + err.Error())
	}
	if err := os.Setenv("XDG_STATE_HOME", state); err != nil {
		panic("failed to set XDG_STATE_HOME: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(state)
	os.Exit(code)
}
