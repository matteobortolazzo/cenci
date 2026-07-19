package daemon

import (
	"sync/atomic"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/config"
	tmuxfe "github.com/matteobortolazzo/cenci/watch/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/internal/reap"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux/tmuxtest"
)

// mockReaper is a call-counting reap.Reaper for daemon tests (#292). It never
// shells out — Reap() only increments a counter so tests can assert on how
// many times the daemon triggered a reap pass (and, for startup, that it did
// at all) without touching the sandbox launcher/docker.
type mockReaper struct {
	calls atomic.Int32
}

var _ reap.Reaper = (*mockReaper)(nil)

func (m *mockReaper) Reap() {
	m.calls.Add(1)
}

// fakeKiller is a call-recording windowKiller for daemon tests (#522). It
// mirrors internal/closecmd/closecmd_test.go's fakeKiller pattern in the
// sibling package: no real tmux call, just records every target the daemon
// asked to kill so tests can assert on pending-close-triggered kills at
// SessionEnd without touching a real tmux binary.
type fakeKiller struct {
	killed []string
}

func (f *fakeKiller) KillWindow(target string) error {
	f.killed = append(f.killed, target)
	return nil
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.SweepInterval = 10 * time.Millisecond
	return cfg
}

// findWindowOpt returns the value of the last SetWindowOption call matching target and key.
func findWindowOpt(opts []tmuxtest.WindowOptCall, target, key string) (string, bool) {
	return tmuxtest.FindWindowOpt(opts, target, key)
}

// lastRename returns the last rename call for a given target.
func lastRename(renames []tmuxtest.RenameCall, target string) (string, bool) {
	return tmuxtest.LastRename(renames, target)
}

// newTestDaemon creates a daemon wired to a real tmux frontend over a mock
// tmux client — an integration setup across the frontend seam. Tests call
// handleEvent/runSweep directly for synchronous, deterministic behavior. A
// mockReaper is always injected (#292) so existing tests are unaffected and
// reap-specific tests can type-assert d.reaper.(*mockReaper). A fakeKiller is
// likewise always injected (#522) so pending-close tests can type-assert
// d.killer.(*fakeKiller) without touching a real tmux binary.
func newTestDaemon(mc *tmuxtest.MockClient) *Daemon {
	ch := make(chan ipc.HookEvent, 16)
	cfg := testConfig()
	d := newDaemon(cfg, tmuxfe.New(cfg, mc), ch, &mockReaper{})
	d.killer = &fakeKiller{}
	return d
}
