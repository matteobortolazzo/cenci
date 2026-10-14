package daemon

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	tmuxfe "github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/reap"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
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

// readCall is one recorded fakeTicketReader.Read invocation (#1251).
type readCall struct {
	Repo ticketstate.Repo
	Nums []int
}

// fakeTicketReader is the window-cleanup pass's ticket-state reader double
// (#1251). It records every (repo, nums) call and answers from a script, so tests
// can assert both the verdict-driven closes and "makes no GitHub reads". It is
// not wired into newTestDaemon: tests install it on d.cleanupReader.
//
// An unscripted number answers Unknown with an error, the way the real reader
// fails open. Safe for concurrent use: the loop tests call it from the daemon
// goroutine and read the recorded calls from the test goroutine.
type fakeTicketReader struct {
	mu      sync.Mutex
	results map[ticketstate.Repo]map[int]ticketstate.Result
	repoErr map[ticketstate.Repo]error
	omitted map[ticketstate.Repo]map[int]bool
	calls   []readCall
	// beforeRead, when set, runs in every Read after the call is recorded and
	// outside the lock, for tests that need a blocking reader.
	beforeRead func(ctx context.Context)
}

func (f *fakeTicketReader) Read(ctx context.Context, repo ticketstate.Repo, nums []int) map[int]ticketstate.Result {
	f.mu.Lock()
	f.calls = append(f.calls, readCall{Repo: repo, Nums: slices.Clone(nums)})
	f.mu.Unlock()
	if f.beforeRead != nil {
		f.beforeRead(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]ticketstate.Result, len(nums))
	for _, n := range nums {
		if f.omitted[repo][n] {
			continue // the map has no entry for n, as a reader that dropped it would answer
		}
		if err := f.repoErr[repo]; err != nil {
			out[n] = ticketstate.Result{Err: err}
			continue
		}
		res, ok := f.results[repo][n]
		if !ok {
			res = ticketstate.Result{Err: fmt.Errorf("fakeTicketReader: no scripted result for %s/%s#%d", repo.Owner, repo.Name, n)}
		}
		out[n] = res
	}
	return out
}

// script sets the verdict the reader answers for repo's ticket n.
func (f *fakeTicketReader) script(repo ticketstate.Repo, n int, v ticketstate.Verdict) {
	f.setResult(repo, n, ticketstate.Result{Verdict: v})
}

// scriptErr makes the reader answer Unknown with err for repo's ticket n.
func (f *fakeTicketReader) scriptErr(repo ticketstate.Repo, n int, err error) {
	f.setResult(repo, n, ticketstate.Result{Err: err})
}

// scriptResult sets the exact Result the reader answers for repo's ticket n, for
// shapes script and scriptErr cannot express (e.g. Finished together with an
// error).
func (f *fakeTicketReader) scriptResult(repo ticketstate.Repo, n int, res ticketstate.Result) {
	f.setResult(repo, n, res)
}

// omit makes the reader leave repo's ticket n out of the returned map entirely.
func (f *fakeTicketReader) omit(repo ticketstate.Repo, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.omitted == nil {
		f.omitted = make(map[ticketstate.Repo]map[int]bool)
	}
	if f.omitted[repo] == nil {
		f.omitted[repo] = make(map[int]bool)
	}
	f.omitted[repo][n] = true
}

// failRepo makes every number requested for repo fail with err (a whole-call
// failure).
func (f *fakeTicketReader) failRepo(repo ticketstate.Repo, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.repoErr == nil {
		f.repoErr = make(map[ticketstate.Repo]error)
	}
	f.repoErr[repo] = err
}

func (f *fakeTicketReader) setResult(repo ticketstate.Repo, n int, res ticketstate.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.results == nil {
		f.results = make(map[ticketstate.Repo]map[int]ticketstate.Result)
	}
	if f.results[repo] == nil {
		f.results[repo] = make(map[int]ticketstate.Result)
	}
	f.results[repo][n] = res
}

// recorded returns a copy of every Read call so far.
func (f *fakeTicketReader) recorded() []readCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// readsFor returns how many Read calls named repo.
func (f *fakeTicketReader) readsFor(repo ticketstate.Repo) int {
	n := 0
	for _, c := range f.recorded() {
		if c.Repo == repo {
			n++
		}
	}
	return n
}

// fakeResolver is the window-cleanup pass's repo-resolver double (#1251): dir ->
// Identity, or an error for a dir with a scripted failure. A dir with neither is
// an error, like an unresolvable directory. Not wired by default.
type fakeResolver struct {
	mu   sync.Mutex
	ids  map[string]ticketstate.Identity
	errs map[string]error
	dirs []string
}

func (r *fakeResolver) Resolve(_ context.Context, dir string) (ticketstate.Identity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs = append(r.dirs, dir)
	if err := r.errs[dir]; err != nil {
		return ticketstate.Identity{}, err
	}
	id, ok := r.ids[dir]
	if !ok {
		return ticketstate.Identity{}, fmt.Errorf("fakeResolver: %s is not a known repo dir", dir)
	}
	return id, nil
}

// add registers dir as the checkout of repo.
func (r *fakeResolver) add(dir string, repo ticketstate.Repo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids == nil {
		r.ids = make(map[string]ticketstate.Identity)
	}
	r.ids[dir] = ticketstate.Identity{Toplevel: dir, Repo: repo}
}

// fail makes resolving dir fail with err.
func (r *fakeResolver) fail(dir string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.errs == nil {
		r.errs = make(map[string]error)
	}
	r.errs[dir] = err
}
