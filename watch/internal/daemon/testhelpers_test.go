package daemon

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	tmuxfe "github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/reap"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
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
	d, _ := newTestDaemonWithRunner(mc)
	return d
}

// syncOffLoop is the synchronous stand-in for the daemon's off-loop runner
// (#1253): it runs fn inline with a background context and records the timeout
// the daemon asked for, so tests are deterministic and can still assert the
// bound. Tests that exercise the production goroutine runner build the daemon
// with newDaemon directly instead.
type syncOffLoop struct {
	timeouts []time.Duration
}

func (s *syncOffLoop) run(timeout time.Duration, fn func(ctx context.Context)) {
	s.timeouts = append(s.timeouts, timeout)
	fn(context.Background())
}

// newTestDaemonWithRunner is newTestDaemon plus access to the synchronous
// off-loop runner it installed.
func newTestDaemonWithRunner(mc *tmuxtest.MockClient) (*Daemon, *syncOffLoop) {
	ch := make(chan ipc.HookEvent, 16)
	cfg := testConfig()
	d := newDaemon(cfg, tmuxfe.New(cfg, mc), ch, &mockReaper{})
	runner := &syncOffLoop{}
	d.runOffLoop = runner.run
	d.killer = &fakeKiller{}
	// The babysit close guard is disabled by default so the suite never
	// reads the developer's ambient supervisor state directory (#787); the
	// guard's own tests wire an explicit fake.
	d.closeGuard = nil
	// newDaemon's armCapable is nil, which refuses every arm request (#1215).
	// The shared fixture installs a permissive predicate so the many existing
	// arm tests that expect an ack or a spawn keep exercising their own branch;
	// the capability gate's tests (arm_test.go, arm_capability_external_test.go)
	// override it explicitly, and the nil default is pinned by building through
	// newDaemon directly.
	d.armCapable = func(agent, workflow string) bool { return true }
	return d, runner
}
