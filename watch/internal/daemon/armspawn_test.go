package daemon

// Ticket #1095 Phase 3 (red): hostArmSpawn is a thin stub in armspawn.go
// that unconditionally nacks with armSpawnStubReason and never touches
// d.armLimiter or any of the three injectable seams, so every test below
// fails for the right reason (behavior not yet implemented) rather than a
// compile error. Phase 4 makes these pass.
//
// Filename note (watch/docs/go-gotchas.md #1094): deliberately NOT
// "armspawn_arm_test.go" or any *_<GOARCH>_test.go-shaped name -- Go's
// build-constraint filename trap would silently exclude it on non-matching
// platforms.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/hostrepo"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// armSeamCalls is a call-recording wrapper for the three injectable
// hostArmSpawn seams, mirroring the package's existing recordingArmSpawn
// pattern -- atomic counters so the parallel-calls race test
// (TestHostArmSpawn_RateLimiterRaceClean) stays -race clean, plus a
// mutex-guarded lastCmd capture for the argv/Dir/Env/Setsid assertions.
type armSeamCalls struct {
	session atomic.Int32
	resolve atomic.Int32
	start   atomic.Int32

	mu      sync.Mutex
	lastCmd *exec.Cmd
}

func (c *armSeamCalls) recordCmd(cmd *exec.Cmd) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastCmd = cmd
}

func (c *armSeamCalls) capturedCmd() *exec.Cmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCmd
}

// installArmSeams swaps all three package-var seams for scripted fakes that
// also record their call count (and, for armStartBabysit, the captured
// *exec.Cmd), restoring the originals via t.Cleanup -- mirrors the
// package's existing d.armSpawn-swap test style (arm_test.go) but at the
// package-var level, since armResolveHostRepo/armSessionForPane/
// armStartBabysit are seams of hostArmSpawn itself, not of Daemon.
func installArmSeams(t *testing.T, session func(context.Context, string) (string, error), resolve func(context.Context, string) (string, error), start func(*exec.Cmd) error) *armSeamCalls {
	t.Helper()
	calls := &armSeamCalls{}
	origSession, origResolve, origStart := armSessionForPane, armResolveHostRepo, armStartBabysit
	armSessionForPane = func(ctx context.Context, pane string) (string, error) {
		calls.session.Add(1)
		return session(ctx, pane)
	}
	armResolveHostRepo = func(ctx context.Context, repo string) (string, error) {
		calls.resolve.Add(1)
		return resolve(ctx, repo)
	}
	armStartBabysit = func(cmd *exec.Cmd) error {
		calls.start.Add(1)
		calls.recordCmd(cmd)
		return start(cmd)
	}
	t.Cleanup(func() {
		armSessionForPane, armResolveHostRepo, armStartBabysit = origSession, origResolve, origStart
	})
	return calls
}

func fixedSession(session string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return session, nil }
}

func fixedRepoDir(dir string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return dir, nil }
}

func failingSeam[T any](err error) func(context.Context, string) (T, error) {
	return func(context.Context, string) (T, error) {
		var zero T
		return zero, err
	}
}

func okStart() func(*exec.Cmd) error {
	return func(*exec.Cmd) error { return nil }
}

// resetArmLimiter installs a fresh, never-yet-used Daemon for hostArmSpawn
// tests -- d.armLimiter's zero value is a full bucket on first use, per its
// own doc comment, so a freshly constructed newTestDaemon always starts
// with a full burst allowance.
func newArmSpawnTestDaemon() *Daemon {
	return newTestDaemon(&tmuxtest.MockClient{})
}

// -- nack-per-failure-mode matrix ---------------------------------------

// TestHostArmSpawn_PaneUnresolvable_NacksAndStopsBeforeRepoResolution
// covers the pane->session resolution failure branch: an unresolvable pane
// nacks with ReasonArmPaneUnresolvable, and per the ticket's sequencing
// (pane resolution before repo resolution, both before spawn), neither
// armResolveHostRepo nor armStartBabysit is ever invoked.
func TestHostArmSpawn_PaneUnresolvable_NacksAndStopsBeforeRepoResolution(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t,
		failingSeam[string](errors.New("tmux: no such pane")),
		fixedRepoDir("/repo"),
		okStart(),
	)

	resp := d.hostArmSpawn(validArmRequest())

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack for an unresolvable pane")
	}
	if resp.Reason != ReasonArmPaneUnresolvable {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, ReasonArmPaneUnresolvable)
	}
	if got := calls.resolve.Load(); got != 0 {
		t.Errorf("armResolveHostRepo calls = %d, want 0 -- pane resolution must be checked before repo resolution", got)
	}
	if got := calls.start.Load(); got != 0 {
		t.Errorf("armStartBabysit calls = %d, want 0", got)
	}
}

// TestHostArmSpawn_PaneResolutionTimedOut_NacksDistinctReason covers the
// budget-timeout classification: a pane-resolution failure wrapping
// context.DeadlineExceeded must nack ReasonArmResolutionTimedOut, distinct
// from a plain ReasonArmPaneUnresolvable.
func TestHostArmSpawn_PaneResolutionTimedOut_NacksDistinctReason(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t,
		failingSeam[string](errors.Join(errors.New("tmux display-message"), context.DeadlineExceeded)),
		fixedRepoDir("/repo"),
		okStart(),
	)

	resp := d.hostArmSpawn(validArmRequest())

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack when pane resolution exceeds the budget")
	}
	if resp.Reason != ReasonArmResolutionTimedOut {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, ReasonArmResolutionTimedOut)
	}
}

// TestHostArmSpawn_HostRepoNotFound_Nacks covers hostrepo.ErrNoMatch's
// classification into ReasonArmHostRepoNotFound, and that a repo-resolution
// nack never reaches armStartBabysit.
func TestHostArmSpawn_HostRepoNotFound_Nacks(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t,
		fixedSession("host-session"),
		failingSeam[string](hostrepo.ErrNoMatch),
		okStart(),
	)

	resp := d.hostArmSpawn(validArmRequest())

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack for hostrepo.ErrNoMatch")
	}
	if resp.Reason != ReasonArmHostRepoNotFound {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, ReasonArmHostRepoNotFound)
	}
	if got := calls.start.Load(); got != 0 {
		t.Errorf("armStartBabysit calls = %d, want 0", got)
	}
}

// TestHostArmSpawn_HostRepoAmbiguous_Nacks covers hostrepo.ErrAmbiguous's
// classification into ReasonArmHostRepoAmbiguous, distinct from
// ReasonArmHostRepoNotFound.
func TestHostArmSpawn_HostRepoAmbiguous_Nacks(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t,
		fixedSession("host-session"),
		failingSeam[string](hostrepo.ErrAmbiguous),
		okStart(),
	)

	resp := d.hostArmSpawn(validArmRequest())

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack for hostrepo.ErrAmbiguous")
	}
	if resp.Reason != ReasonArmHostRepoAmbiguous {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, ReasonArmHostRepoAmbiguous)
	}
}

// TestHostArmSpawn_HostRepoProbeFailed_Nacks covers every other
// hostrepo.Resolve failure (a failed/malformed container-runtime inspect,
// wrapped hostrepo.ErrMalformedInspect included) classifying into
// ReasonArmHostRepoProbeFailed -- fail closed, never guessed as a plain
// "not found".
func TestHostArmSpawn_HostRepoProbeFailed_Nacks(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t,
		fixedSession("host-session"),
		failingSeam[string](errors.Join(errors.New("docker inspect: exit status 1"), hostrepo.ErrMalformedInspect)),
		okStart(),
	)

	resp := d.hostArmSpawn(validArmRequest())

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack for a hostrepo probe failure")
	}
	if resp.Reason != ReasonArmHostRepoProbeFailed {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, ReasonArmHostRepoProbeFailed)
	}
}

// TestHostArmSpawn_HostRepoResolutionTimedOut_NacksDistinctReason mirrors
// the pane-side timeout classification for the repo-resolution seam.
func TestHostArmSpawn_HostRepoResolutionTimedOut_NacksDistinctReason(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t,
		fixedSession("host-session"),
		failingSeam[string](errors.Join(errors.New("hostrepo.Resolve"), context.DeadlineExceeded)),
		okStart(),
	)

	resp := d.hostArmSpawn(validArmRequest())

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack when repo resolution exceeds the budget")
	}
	if resp.Reason != ReasonArmResolutionTimedOut {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, ReasonArmResolutionTimedOut)
	}
}

// TestHostArmSpawn_SpawnFailed_Nacks covers the final stage: a fully
// resolved request whose armStartBabysit seam fails to start the process
// nacks ReasonArmSpawnFailed rather than reporting success.
func TestHostArmSpawn_SpawnFailed_Nacks(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t,
		fixedSession("host-session"),
		fixedRepoDir("/repo"),
		func(*exec.Cmd) error { return errors.New("fork/exec: resource temporarily unavailable") },
	)

	resp := d.hostArmSpawn(validArmRequest())

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack when armStartBabysit fails")
	}
	if resp.Reason != ReasonArmSpawnFailed {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, ReasonArmSpawnFailed)
	}
}

// -- success path: argv/Dir/env/Setsid contract ---------------------------

// TestHostArmSpawn_SuccessPath_CapturesCmdArgvDirEnvSetsid covers AC 2's
// spawn contract: a fully resolved, valid request acks OK, and the captured
// *exec.Cmd carries the resolved --session/--dir, the request's --agent/
// --interval, no --state-dir, cmd.Dir set to the resolved host path,
// CENCI_SANDBOX/CENCI_BABYSIT_SUPERVISOR stripped from the child env even
// though both are set in the daemon's own ambient environment (#1095
// auto-adopted answer #8: an inherited value would create an
// arm->spawn->arm loop or force the child to run its tick loop in the
// foreground), and SysProcAttr detaching the child (Setsid).
func TestHostArmSpawn_SuccessPath_CapturesCmdArgvDirEnvSetsid(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "1")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")

	const resolvedSession = "host-session"
	const resolvedDir = "/resolved/host/repo"

	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t,
		fixedSession(resolvedSession),
		fixedRepoDir(resolvedDir),
		okStart(),
	)

	req := validArmRequest()
	resp := d.hostArmSpawn(req)

	if !resp.OK {
		t.Fatalf("resp.OK = false (reason %q), want an ack for a fully resolved request", resp.Reason)
	}
	if got := calls.start.Load(); got != 1 {
		t.Fatalf("armStartBabysit calls = %d, want exactly 1", got)
	}

	cmd := calls.capturedCmd()
	if cmd == nil {
		t.Fatal("armStartBabysit was invoked but captured no *exec.Cmd")
	}

	assertArgAfter(t, cmd.Args, "--agent", req.Agent)
	assertArgAfter(t, cmd.Args, "--interval", req.Interval.String())
	assertArgAfter(t, cmd.Args, "--session", resolvedSession)
	assertArgAfter(t, cmd.Args, "--dir", resolvedDir)
	for _, a := range cmd.Args {
		if a == "--state-dir" {
			t.Errorf("cmd.Args = %v, want no --state-dir (the daemon-spawned child resolves the host default)", cmd.Args)
		}
	}
	found := false
	for _, a := range cmd.Args {
		if a == req.PR {
			found = true
		}
	}
	if !found {
		t.Errorf("cmd.Args = %v, want the PR %q present", cmd.Args, req.PR)
	}

	if cmd.Dir != resolvedDir {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, resolvedDir)
	}

	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "CENCI_SANDBOX=") {
			t.Errorf("cmd.Env = %v, want CENCI_SANDBOX stripped from the spawned child's environment", cmd.Env)
		}
		if strings.HasPrefix(e, "CENCI_BABYSIT_SUPERVISOR=") {
			t.Errorf("cmd.Env = %v, want CENCI_BABYSIT_SUPERVISOR stripped from the spawned child's environment", cmd.Env)
		}
	}

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Errorf("cmd.SysProcAttr = %#v, want Setsid true (the spawned supervisor must detach)", cmd.SysProcAttr)
	}
}

func assertArgAfter(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			if i+1 >= len(args) {
				t.Errorf("cmd.Args %v: %s has no following value", args, flag)
				return
			}
			if args[i+1] != want {
				t.Errorf("cmd.Args %v: %s = %q, want %q", args, flag, args[i+1], want)
			}
			return
		}
	}
	t.Errorf("cmd.Args %v: %s not found", args, flag)
}

// -- verbose-gated reject logging ------------------------------------------

// TestHostArmSpawn_RejectLogsUnderVerbose covers the reject-branch logging
// rule (watch/docs/hook-events.md rule 2) across every failure mode: each
// must log under cfg.Verbose and stay silent when it's off.
func TestHostArmSpawn_RejectLogsUnderVerbose(t *testing.T) {
	cases := []struct {
		name    string
		session func(context.Context, string) (string, error)
		resolve func(context.Context, string) (string, error)
		start   func(*exec.Cmd) error
	}{
		{"pane unresolvable", failingSeam[string](errors.New("no pane")), fixedRepoDir("/repo"), okStart()},
		{"host repo not found", fixedSession("s"), failingSeam[string](hostrepo.ErrNoMatch), okStart()},
		{"host repo ambiguous", fixedSession("s"), failingSeam[string](hostrepo.ErrAmbiguous), okStart()},
		{"spawn failed", fixedSession("s"), fixedRepoDir("/repo"), func(*exec.Cmd) error { return errors.New("boom") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newArmSpawnTestDaemon()
			installArmSeams(t, tc.session, tc.resolve, tc.start)

			buf := captureLog(t)
			d.cfg.Verbose = false
			d.hostArmSpawn(validArmRequest())
			if buf.Len() != 0 {
				t.Errorf("log output with cfg.Verbose=false: %q, want no output", buf.String())
			}

			buf.Reset()
			d.cfg.Verbose = true
			d.hostArmSpawn(validArmRequest())
			if buf.Len() == 0 {
				t.Error("log output with cfg.Verbose=true: empty, want the reject branch to log")
			}
		})
	}
}

// -- rate limiting ----------------------------------------------------------

// TestHostArmSpawn_BurstUpToCapacityAdmitted covers the token bucket's
// burst allowance: armBucketBurst consecutive requests on a fresh daemon
// (zero-value armLimiter = full bucket on first use) must all be admitted
// through to a successful spawn.
func TestHostArmSpawn_BurstUpToCapacityAdmitted(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t, fixedSession("s"), fixedRepoDir("/repo"), okStart())

	for i := 0; i < armBucketBurst; i++ {
		resp := d.hostArmSpawn(validArmRequest())
		if !resp.OK {
			t.Fatalf("request %d/%d: resp.OK = false (reason %q), want every request up to burst capacity (%d) admitted", i+1, armBucketBurst, resp.Reason, armBucketBurst)
		}
	}
}

// TestHostArmSpawn_OverCapacity_NacksRateLimited_SeamsNeverInvoked is the
// load-bearing assertion for "gate the whole arm path, not just the spawn"
// (#1095 Decisions, comment 3 on the ticket): the request immediately after
// burst capacity is exhausted must nack ReasonArmRateLimited, and none of
// armResolveHostRepo/armSessionForPane/armStartBabysit may be invoked for
// that over-rate request -- proving the bucket gates ahead of any
// resolution work, not just ahead of the fork.
func TestHostArmSpawn_OverCapacity_NacksRateLimited_SeamsNeverInvoked(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedSession("s"), fixedRepoDir("/repo"), okStart())

	for i := 0; i < armBucketBurst; i++ {
		if resp := d.hostArmSpawn(validArmRequest()); !resp.OK {
			t.Fatalf("priming request %d: resp.OK = false (reason %q), want admitted", i, resp.Reason)
		}
	}
	sessionBefore, resolveBefore, startBefore := calls.session.Load(), calls.resolve.Load(), calls.start.Load()

	resp := d.hostArmSpawn(validArmRequest())

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack once burst capacity is exhausted")
	}
	if resp.Reason != ReasonArmRateLimited {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, ReasonArmRateLimited)
	}
	if got := calls.session.Load(); got != sessionBefore {
		t.Errorf("armSessionForPane calls = %d, want unchanged at %d -- the rate gate must run before any resolution work", got, sessionBefore)
	}
	if got := calls.resolve.Load(); got != resolveBefore {
		t.Errorf("armResolveHostRepo calls = %d, want unchanged at %d -- the rate gate must run before any resolution work", got, resolveBefore)
	}
	if got := calls.start.Load(); got != startBefore {
		t.Errorf("armStartBabysit calls = %d, want unchanged at %d -- the rate gate must run before the fork", got, startBefore)
	}
}

// TestHostArmSpawn_RateLimitReplenishesAfterRefillInterval covers the
// bucket's refill side: once capacity is exhausted, advancing armNow past
// armBucketRefill must admit exactly one more request.
func TestHostArmSpawn_RateLimitReplenishesAfterRefillInterval(t *testing.T) {
	origNow := armNow
	fakeNow := time.Now()
	armNow = func() time.Time { return fakeNow }
	t.Cleanup(func() { armNow = origNow })

	d := newArmSpawnTestDaemon()
	installArmSeams(t, fixedSession("s"), fixedRepoDir("/repo"), okStart())

	for i := 0; i < armBucketBurst; i++ {
		if resp := d.hostArmSpawn(validArmRequest()); !resp.OK {
			t.Fatalf("priming request %d: resp.OK = false (reason %q), want admitted", i, resp.Reason)
		}
	}
	if resp := d.hostArmSpawn(validArmRequest()); resp.OK || resp.Reason != ReasonArmRateLimited {
		t.Fatalf("resp = %+v, want a rate-limited nack once capacity is exhausted", resp)
	}

	fakeNow = fakeNow.Add(armBucketRefill)
	resp := d.hostArmSpawn(validArmRequest())
	if !resp.OK {
		t.Errorf("resp.OK = false (reason %q), want admitted once armNow advances past the refill interval", resp.Reason)
	}
}

// TestHostArmSpawn_RateLimiterRaceClean covers the mutex-guarding
// requirement: hostArmSpawn executes on the ipc.EventReceiver's connection
// goroutine, so concurrent callers must never race on d.armLimiter. Run
// with -race; the assertion itself is that at most armBucketBurst calls out
// of a larger concurrent burst were admitted (proving the bucket's mutex
// actually serializes take() rather than double-spending tokens under
// concurrency, which -race would also have flagged as a data race on the
// unguarded fields).
func TestHostArmSpawn_RateLimiterRaceClean(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t, fixedSession("s"), fixedRepoDir("/repo"), okStart())

	const concurrentRequests = armBucketBurst * 4
	var admitted atomic.Int32
	var wg sync.WaitGroup
	wg.Add(concurrentRequests)
	for i := 0; i < concurrentRequests; i++ {
		go func() {
			defer wg.Done()
			if resp := d.hostArmSpawn(validArmRequest()); resp.OK {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := admitted.Load(); got != armBucketBurst {
		t.Errorf("admitted = %d, want exactly %d (burst capacity) out of %d concurrent requests", got, armBucketBurst, concurrentRequests)
	}
}

// -- #1095 review fix: a real subprocess deadline kill must actually
// classify as ReasonArmResolutionTimedOut -----------------------------------
//
// The seam-level tests above (TestHostArmSpawn_PaneResolutionTimedOut_
// NacksDistinctReason / TestHostArmSpawn_HostRepoResolutionTimedOut_
// NacksDistinctReason) fabricate the joined context.DeadlineExceeded error
// directly at the armSessionForPane/armResolveHostRepo seam boundary -- they
// never exercise a real subprocess timeout, so they'd stay green even if
// execArmTmux/execRuntime/execGit never actually wrapped
// context.DeadlineExceeded into their returned error on a mid-call kill (the
// bug this fix closes). These two tests let a real *exec.Cmd get killed by
// armSpawnBudget's context deadline and assert the resulting error, once it
// reaches hostArmSpawn through the real (non-seamed) resolution path,
// produces ReasonArmResolutionTimedOut.

// TestHostArmSpawn_PaneResolutionRealDeadlineExceeded_NacksResolutionTimedOut
// installs a fake `tmux` on PATH that sleeps well past armSpawnBudget and
// leaves armSessionForPane at its real defaultArmSessionForPane
// implementation (only armResolveHostRepo/armStartBabysit are seamed, and
// must never be invoked), proving a genuine mid-call context-deadline kill
// during pane resolution is classified as ReasonArmResolutionTimedOut end to
// end, not just at the fabricated seam boundary.
func TestHostArmSpawn_PaneResolutionRealDeadlineExceeded_NacksResolutionTimedOut(t *testing.T) {
	shimDir := t.TempDir()
	// "exec sleep N" (never a bare "sleep N" line) is load-bearing here,
	// mirroring internal/babysit/gh_test.go's
	// TestExecGh_ParentContextDeadlineClassifiesAsTimeout rationale: it
	// replaces the shell's own process image with sleep (no fork), so
	// SIGKILLing tmux (Cmd's default Cancel on the CommandContext deadline)
	// kills the actual sleeping process directly instead of leaving an
	// orphaned grandchild holding the output pipe open.
	script := "#!/bin/sh\nexec sleep 10\n"
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prepend (not replace) PATH: the script's own "exec sleep 10" still
	// needs a real `sleep` resolvable on PATH, mirroring
	// internal/babysit/gh_test.go's shimDir+PathListSeparator+os.Getenv
	// convention.
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := newArmSpawnTestDaemon()
	origResolve, origStart := armResolveHostRepo, armStartBabysit
	armResolveHostRepo = failingSeam[string](errors.New("armResolveHostRepo must never be invoked"))
	armStartBabysit = func(*exec.Cmd) error {
		t.Fatal("armStartBabysit must never be invoked when pane resolution times out")
		return nil
	}
	t.Cleanup(func() { armResolveHostRepo, armStartBabysit = origResolve, origStart })

	start := time.Now()
	resp := d.hostArmSpawn(validArmRequest())
	elapsed := time.Since(start)

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack when a real tmux subprocess exceeds armSpawnBudget")
	}
	if resp.Reason != ReasonArmResolutionTimedOut {
		t.Errorf("resp.Reason = %q, want %q -- a real mid-call context-deadline kill during pane resolution must classify as a timeout, not the step-specific reason", resp.Reason, ReasonArmResolutionTimedOut)
	}
	if elapsed >= 9*time.Second {
		t.Fatalf("hostArmSpawn took %v, want it bounded by armSpawnBudget (%v), not the full 10s sleep", elapsed, armSpawnBudget)
	}
}

// TestHostArmSpawn_HostRepoResolutionRealDeadlineExceeded_NacksResolutionTimedOut
// mirrors the pane-side test above for host-repo resolution: armSessionForPane
// is seamed to resolve instantly, but armResolveHostRepo is left at the real
// hostrepo.Resolve, and a fake `docker` on PATH sleeps well past
// armSpawnBudget when hostrepo.Resolve's own execRuntime("docker", "ps", ...)
// call reaches it -- proving the same real-kill classification for the
// repo-resolution step, through hostrepo.Resolve -> execRuntime, not just
// execArmTmux.
func TestHostArmSpawn_HostRepoResolutionRealDeadlineExceeded_NacksResolutionTimedOut(t *testing.T) {
	shimDir := t.TempDir()
	script := "#!/bin/sh\nexec sleep 10\n"
	if err := os.WriteFile(filepath.Join(shimDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prepend PATH so the fake `docker` is found first by
	// sandbox.AvailableRuntimes()/execRuntime's LookPath, while "exec sleep
	// 10" still resolves a real `sleep`.
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := newArmSpawnTestDaemon()
	origSession, origStart := armSessionForPane, armStartBabysit
	armSessionForPane = fixedSession("host-session")
	armStartBabysit = func(*exec.Cmd) error {
		t.Fatal("armStartBabysit must never be invoked when host-repo resolution times out")
		return nil
	}
	t.Cleanup(func() { armSessionForPane, armStartBabysit = origSession, origStart })

	start := time.Now()
	resp := d.hostArmSpawn(validArmRequest())
	elapsed := time.Since(start)

	if resp.OK {
		t.Fatal("resp.OK = true, want a nack when a real docker subprocess exceeds armSpawnBudget")
	}
	if resp.Reason != ReasonArmResolutionTimedOut {
		t.Errorf("resp.Reason = %q, want %q -- a real mid-call context-deadline kill during host-repo resolution must classify as a timeout, not the step-specific reason", resp.Reason, ReasonArmResolutionTimedOut)
	}
	if elapsed >= 9*time.Second {
		t.Fatalf("hostArmSpawn took %v, want it bounded by armSpawnBudget (%v), not the full 10s sleep", elapsed, armSpawnBudget)
	}
}

// -- #1095 review fix: reapArmSpawn's non-zero-exit logging path -----------

// TestReapArmSpawn_LogsUnderVerbose covers reapArmSpawn directly (it had
// zero test coverage): a real short-lived failing child's non-zero exit must
// log under cfg.Verbose and stay silent when it's off, mirroring
// TestHostArmSpawn_RejectLogsUnderVerbose's verbose/silent assertion style
// for hostArmSpawn's own reject branches -- reapArmSpawn is the only place a
// spawned supervisor's own failure (as opposed to a rejected arm request)
// ever becomes visible.
func TestReapArmSpawn_LogsUnderVerbose(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%v", verbose), func(t *testing.T) {
			d := newArmSpawnTestDaemon()
			d.cfg.Verbose = verbose

			cmd := exec.Command("sh", "-c", "exit 1")
			if err := cmd.Start(); err != nil {
				t.Fatalf("starting real failing child: %v", err)
			}

			buf := captureLog(t)
			d.reapArmSpawn(cmd)

			switch {
			case verbose && buf.Len() == 0:
				t.Error("log output with cfg.Verbose=true: empty, want the spawned child's non-zero exit logged")
			case !verbose && buf.Len() != 0:
				t.Errorf("log output with cfg.Verbose=false: %q, want no output", buf.String())
			}
		})
	}
}
