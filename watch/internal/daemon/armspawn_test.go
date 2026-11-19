package daemon

// hostArmSpawn (#1095) and its launch binding and supervisor caps (#1135):
// the token bucket, the one pane-identity read, the constant-time launch-ID
// match, the bound-container repo resolution, the per-repo/per-host live
// supervisor caps with in-memory reservations, and the detached spawn.
//
// Every harness here isolates XDG_STATE_HOME (the babysit state directory the
// cap counts) to a t.TempDir() and clears CENCI_LAUNCH_ID
// (watch/docs/test-isolation.md), so neither a developer's real supervisors
// nor an ambient sandbox launch can reach an assertion.
//
// Filename note (watch/docs/go-gotchas.md #1094): deliberately NOT
// "armspawn_arm_test.go" or any *_<GOARCH>_test.go-shaped name -- Go's
// build-constraint filename trap would silently exclude it on non-matching
// platforms.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/hostrepo"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/selfexe"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// The pane every seamed test binds to by default: tagged by the launch the
// request claims (armTestLaunchID), in a sandbox container under docker.
const (
	armTestSession   = "host-session"
	armTestContainer = "claude-cenci-r-a"
	armTestRuntime   = "docker"

	// armOtherLaunchID is a well-formed launch ID that is not the request's.
	armOtherLaunchID = "fedcba9876543210fedcba9876543210"
)

func boundPane() armPaneInfo {
	return armPaneInfo{session: armTestSession, launchID: armTestLaunchID, container: armTestContainer, runtime: armTestRuntime}
}

type (
	armPaneFn    = func(ctx context.Context, socket, pane string) (armPaneInfo, error)
	armResolveFn = func(ctx context.Context, runtime, container, repo string) (string, error)
)

func fixedPane(p armPaneInfo) armPaneFn {
	return func(context.Context, string, string) (armPaneInfo, error) { return p, nil }
}

func failingPane(err error) armPaneFn {
	return func(context.Context, string, string) (armPaneInfo, error) { return armPaneInfo{}, err }
}

func fixedRepoDir(dir string) armResolveFn {
	return func(context.Context, string, string, string) (string, error) { return dir, nil }
}

func failingResolve(err error) armResolveFn {
	return func(context.Context, string, string, string) (string, error) { return "", err }
}

func okStart() func(*exec.Cmd) error {
	return func(*exec.Cmd) error { return nil }
}

// armPaneRead and armResolveCall are the recorded arguments of one pane read
// and one bound-container resolution.
type armPaneRead struct{ socket, pane string }
type armResolveCall struct{ runtime, container, repo string }

// armSeamCalls records every hostArmSpawn seam call: atomic counters so the
// concurrent tests stay -race clean, plus mutex-guarded argument logs and the
// last captured *exec.Cmd for the argv/Dir/Env/Setsid assertions.
type armSeamCalls struct {
	pane    atomic.Int32
	resolve atomic.Int32
	start   atomic.Int32

	// stateDir is the isolated babysit state directory the cap counts.
	stateDir string

	mu        sync.Mutex
	lastCmd   *exec.Cmd
	paneReads []armPaneRead
	resolves  []armResolveCall
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

func (c *armSeamCalls) recordPaneRead(socket, pane string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paneReads = append(c.paneReads, armPaneRead{socket, pane})
}

func (c *armSeamCalls) recordResolve(rt, container, repo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolves = append(c.resolves, armResolveCall{rt, container, repo})
}

func (c *armSeamCalls) recordedPaneReads() []armPaneRead {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.paneReads)
}

func (c *armSeamCalls) recordedResolves() []armResolveCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.resolves)
}

// isolateArmEnv points XDG_STATE_HOME at a fresh temp directory and clears
// CENCI_LAUNCH_ID, returning the babysit state directory under it (not yet
// created: a missing directory counts as zero live supervisors).
func isolateArmEnv(t *testing.T) string {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv(sandbox.LaunchIDEnvName, "")
	return filepath.Join(stateHome, "cenci", "babysit")
}

// installArmSeams isolates the environment (isolateArmEnv), swaps the pane,
// resolve and start seams for recording fakes and restores them via
// t.Cleanup. armLiveSupervisors stays the real babysit.LiveSupervisors over the
// isolated state directory, so cap tests drive it with real state files.
//
// It also stubs armResolveSelfExe with fakeSelfExe (#1372) so the suite never
// reads the developer's ~/.local/bin/cenci, PATH or own executable
// (watch/docs/test-isolation.md); tests that exercise the resolver replace it
// afterwards through installSelfExe.
func installArmSeams(t *testing.T, pane armPaneFn, resolve armResolveFn, start func(*exec.Cmd) error) *armSeamCalls {
	t.Helper()
	calls := &armSeamCalls{stateDir: isolateArmEnv(t)}
	installSelfExe(t, func() (string, error) { return fakeSelfExe, nil })
	origPane, origResolve, origStart, origLive := armPaneIdentity, armResolveContainerRepo, armStartBabysit, armLiveSupervisors
	armPaneIdentity = func(ctx context.Context, socket, p string) (armPaneInfo, error) {
		calls.pane.Add(1)
		calls.recordPaneRead(socket, p)
		return pane(ctx, socket, p)
	}
	armResolveContainerRepo = func(ctx context.Context, rt, container, repo string) (string, error) {
		calls.resolve.Add(1)
		calls.recordResolve(rt, container, repo)
		return resolve(ctx, rt, container, repo)
	}
	armStartBabysit = func(cmd *exec.Cmd) error {
		calls.start.Add(1)
		calls.recordCmd(cmd)
		return start(cmd)
	}
	t.Cleanup(func() {
		armPaneIdentity, armResolveContainerRepo, armStartBabysit, armLiveSupervisors = origPane, origResolve, origStart, origLive
	})
	return calls
}

// fakeSelfExe is the fixed absolute path the default test resolver returns.
const fakeSelfExe = "/opt/cenci-test/bin/cenci"

// installSelfExe swaps the armResolveSelfExe seam for resolve, restoring the
// previous value via t.Cleanup.
func installSelfExe(t *testing.T, resolve func() (string, error)) {
	t.Helper()
	orig := armResolveSelfExe
	armResolveSelfExe = resolve
	t.Cleanup(func() { armResolveSelfExe = orig })
}

// freezeArmClock pins armNow so the token bucket never refills mid-test, and
// returns a func that advances the pinned clock.
func freezeArmClock(t *testing.T) (advance func(time.Duration)) {
	t.Helper()
	orig := armNow
	var mu sync.Mutex
	fixed := time.Now()
	armNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return fixed
	}
	t.Cleanup(func() { armNow = orig })
	return func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		fixed = fixed.Add(d)
	}
}

// realStart starts a harmless real `true` in place of the supervisor (argv is
// untouched), so the reaper goroutine's Wait ends silently instead of logging a
// "not started" failure that would race a log assertion.
func realStart(t *testing.T) func(*exec.Cmd) error {
	t.Helper()
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("needs a `true` binary to stand in for the supervisor process")
	}
	return func(cmd *exec.Cmd) error {
		cmd.Path, cmd.Dir = truePath, ""
		return cmd.Start()
	}
}

// logLines splits captured log output into its non-empty lines.
func logLines(buf *bytes.Buffer) []string {
	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// newArmSpawnTestDaemon returns a fresh daemon for hostArmSpawn tests --
// d.armLimiter's zero value is a full bucket on first use and
// d.armReservations' zero value holds no reservation.
func newArmSpawnTestDaemon() *Daemon {
	return newTestDaemon(&tmuxtest.MockClient{})
}

// armReqFor is validArmRequest for another (repo, PR).
func armReqFor(repo string, pr int) ipc.ArmRequest {
	req := validArmRequest()
	req.Repo, req.PR = repo, strconv.Itoa(pr)
	return req
}

// writeLiveStates writes one real babysit.State per PR into stateDir with
// status needs-input, which supervisorLive counts as live whatever the PID.
func writeLiveStates(t *testing.T, stateDir, repo string, prs ...int) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, pr := range prs {
		b, err := json.Marshal(babysit.State{PR: strconv.Itoa(pr), Repo: repo, Status: "needs-input", UpdatedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		name := strings.ReplaceAll(repo, "/", "_") + "-" + strconv.Itoa(pr) + ".json"
		if err := os.WriteFile(filepath.Join(stateDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// prRange returns n consecutive PR numbers starting at from.
func prRange(from, n int) []int {
	prs := make([]int, n)
	for i := range prs {
		prs[i] = from + i
	}
	return prs
}

// -- nack-per-failure-mode matrix ---------------------------------------

// A pane read that fails (tmux error) nacks ReasonArmPaneUnresolvable and stops
// before the bound-container resolution and the spawn.
func TestHostArmSpawn_PaneUnresolvable_NacksAndStopsBeforeRepoResolution(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, failingPane(errors.New("tmux: no such pane")), fixedRepoDir("/repo"), okStart())

	resp := d.hostArmSpawn(validArmRequest())

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmPaneUnresolvable}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmPaneUnresolvable)
	}
	if got := calls.resolve.Load(); got != 0 {
		t.Errorf("armResolveContainerRepo calls = %d, want 0 -- the pane is read before the repo is resolved", got)
	}
	if got := calls.start.Load(); got != 0 {
		t.Errorf("armStartBabysit calls = %d, want 0", got)
	}
}

// A gone pane (empty display-message output, errArmPaneGone) and a pane read
// whose session field is empty both nack ReasonArmPaneUnresolvable.
func TestHostArmSpawn_GonePane_NacksPaneUnresolvable(t *testing.T) {
	emptySession := boundPane()
	emptySession.session = ""
	for name, pane := range map[string]armPaneFn{
		"gone pane":     failingPane(errArmPaneGone),
		"empty session": fixedPane(emptySession),
	} {
		t.Run(name, func(t *testing.T) {
			d := newArmSpawnTestDaemon()
			calls := installArmSeams(t, pane, fixedRepoDir("/repo"), okStart())

			resp := d.hostArmSpawn(validArmRequest())

			if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmPaneUnresolvable}) {
				t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmPaneUnresolvable)
			}
			if r, s := calls.resolve.Load(), calls.start.Load(); r != 0 || s != 0 {
				t.Errorf("resolve calls = %d, start calls = %d, want 0 and 0", r, s)
			}
		})
	}
}

// A pane read failing with context.DeadlineExceeded nacks
// ReasonArmResolutionTimedOut, distinct from a plain ReasonArmPaneUnresolvable.
func TestHostArmSpawn_PaneResolutionTimedOut_NacksDistinctReason(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t, failingPane(errors.Join(errors.New("tmux display-message"), context.DeadlineExceeded)), fixedRepoDir("/repo"), okStart())

	resp := d.hostArmSpawn(validArmRequest())

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmResolutionTimedOut}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmResolutionTimedOut)
	}
}

// -- #1135: the launch binding -----------------------------------------------

// Every identity failure past the request's own shape check -- a pane tagged by
// another launch, an untagged pane, a pane whose own ID is malformed, a
// malformed pane read, a missing or non-sandbox container, an unsupported
// runtime -- nacks the one ReasonArmLaunchUnverified and records zero resolve
// and zero spawn calls (so no inspect, and no command named after the runtime
// value, ever runs).
func TestHostArmSpawn_LaunchIdentityFailures_NackLaunchUnverifiedBeforeResolve(t *testing.T) {
	with := func(edit func(*armPaneInfo)) armPaneFn {
		p := boundPane()
		edit(&p)
		return fixedPane(p)
	}
	cases := map[string]armPaneFn{
		"launch ID mismatch":     with(func(p *armPaneInfo) { p.launchID = armOtherLaunchID }),
		"untagged pane":          with(func(p *armPaneInfo) { p.launchID, p.container, p.runtime = "", "", "" }),
		"pane ID is a prefix":    with(func(p *armPaneInfo) { p.launchID = armTestLaunchID[:31] }),
		"pane ID upper case":     with(func(p *armPaneInfo) { p.launchID = strings.ToUpper(armTestLaunchID) }),
		"malformed pane read":    failingPane(errArmPaneMalformed),
		"empty container":        with(func(p *armPaneInfo) { p.container = "" }),
		"non-sandbox container":  with(func(p *armPaneInfo) { p.container = "postgres" }),
		"runtime is a shell":     with(func(p *armPaneInfo) { p.runtime = "sh" }),
		"empty runtime":          with(func(p *armPaneInfo) { p.runtime = "" }),
		"runtime is a full path": with(func(p *armPaneInfo) { p.runtime = "/usr/bin/docker" }),
	}
	for name, pane := range cases {
		t.Run(name, func(t *testing.T) {
			d := newArmSpawnTestDaemon()
			calls := installArmSeams(t, pane, fixedRepoDir("/repo"), okStart())

			resp := d.hostArmSpawn(validArmRequest())

			if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmLaunchUnverified}) {
				t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmLaunchUnverified)
			}
			if got := calls.resolve.Load(); got != 0 {
				t.Errorf("armResolveContainerRepo calls = %d, want 0 -- an unverified launch must never reach the container runtime", got)
			}
			if got := calls.start.Load(); got != 0 {
				t.Errorf("armStartBabysit calls = %d, want 0", got)
			}
		})
	}
}

// One pane read per admitted request, on the request's own socket and pane,
// and the repo resolved from exactly the pane's recorded runtime and container.
func TestHostArmSpawn_OnePaneReadPerAdmittedRequest(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())

	reqs := []ipc.ArmRequest{validArmRequest(), validArmRequest(), validArmRequest()}
	reqs[0].TmuxSocket, reqs[0].TmuxPane = "/run/tmux/a", "%7"
	reqs[1].TmuxSocket, reqs[1].TmuxPane = "", "%8"
	reqs[2].TmuxSocket, reqs[2].TmuxPane, reqs[2].PR = "/run/tmux/b", "%9", "43"
	var want []armPaneRead
	for i, req := range reqs {
		if resp := d.hostArmSpawn(req); !resp.OK {
			t.Fatalf("request %d: nack %q, want an ack", i, resp.Reason)
		}
		want = append(want, armPaneRead{req.TmuxSocket, req.TmuxPane})
	}

	if got := calls.recordedPaneReads(); !reflect.DeepEqual(got, want) {
		t.Errorf("pane reads = %+v, want exactly one per request with its own socket and pane: %+v", got, want)
	}
	for _, got := range calls.recordedResolves() {
		if want := (armResolveCall{armTestRuntime, armTestContainer, "o/r"}); got != want {
			t.Errorf("resolve call = %+v, want the pane's runtime and container with the request's repo: %+v", got, want)
		}
	}
}

// -- bound-container resolution failures -----------------------------------

// ResolveContainer's failure classes map onto the existing host-repo reasons:
// ErrNoMatch (not running, no /workspace mount, origin mismatch) is not found,
// a wrapped ErrMalformedInspect is probe failed (fail closed), and a deadline is
// a timeout. None of them reaches the spawn.
func TestHostArmSpawn_BoundResolveFailures_Nack(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"no match", hostrepo.ErrNoMatch, ReasonArmHostRepoNotFound},
		{"malformed inspect", fmt.Errorf("hostrepo: docker inspect: %w", hostrepo.ErrMalformedInspect), ReasonArmHostRepoProbeFailed},
		{"other probe error", errors.New("hostrepo: docker inspect: permission denied"), ReasonArmHostRepoProbeFailed},
		{"deadline", errors.Join(errors.New("hostrepo: docker inspect"), context.DeadlineExceeded), ReasonArmResolutionTimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newArmSpawnTestDaemon()
			calls := installArmSeams(t, fixedPane(boundPane()), failingResolve(tc.err), okStart())

			resp := d.hostArmSpawn(validArmRequest())

			if resp != (ipc.ArmResponse{OK: false, Reason: tc.want}) {
				t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, tc.want)
			}
			if got := calls.start.Load(); got != 0 {
				t.Errorf("armStartBabysit calls = %d, want 0", got)
			}
		})
	}
}

// TestHostArmSpawn_SpawnFailed_Nacks covers the final stage: a fully
// resolved request whose armStartBabysit seam fails to start the process
// nacks ReasonArmSpawnFailed rather than reporting success.
func TestHostArmSpawn_SpawnFailed_Nacks(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"),
		func(*exec.Cmd) error { return errors.New("fork/exec: resource temporarily unavailable") })
	captureLog(t)

	resp := d.hostArmSpawn(validArmRequest())

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmSpawnFailed}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmSpawnFailed)
	}
}

// -- success path: argv/Dir/env/Setsid contract ---------------------------

// A fully resolved, valid request acks OK, and the captured *exec.Cmd carries
// the pane's --session, the bound container's /workspace source as --dir and
// cmd.Dir, the request's --agent/--interval, no --state-dir, neither
// CENCI_SANDBOX nor CENCI_BABYSIT_SUPERVISOR (#1095 auto-adopted answer #8:
// an inherited value would loop arm->spawn->arm or keep the child in the
// foreground), no launch ID in argv or env (#1135), and Setsid.
func TestHostArmSpawn_SuccessPath_CapturesCmdArgvDirEnvSetsid(t *testing.T) {
	const boundSource = "/resolved/host/repo"

	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir(boundSource), okStart())
	t.Setenv("CENCI_SANDBOX", "1")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")

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
	assertArgAfter(t, cmd.Args, "--session", armTestSession)
	assertArgAfter(t, cmd.Args, "--dir", boundSource)
	if slices.Contains(cmd.Args, "--state-dir") {
		t.Errorf("cmd.Args = %v, want no --state-dir (the daemon-spawned child resolves the host default)", cmd.Args)
	}
	if !slices.Contains(cmd.Args, req.PR) {
		t.Errorf("cmd.Args = %v, want the PR %q present", cmd.Args, req.PR)
	}
	if cmd.Path != fakeSelfExe {
		t.Errorf("cmd.Path = %q, want the resolved self-exec binary %q", cmd.Path, fakeSelfExe)
	}
	if cmd.Dir != boundSource {
		t.Errorf("cmd.Dir = %q, want the bound container's source %q", cmd.Dir, boundSource)
	}

	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "CENCI_SANDBOX=") || strings.HasPrefix(e, "CENCI_BABYSIT_SUPERVISOR=") {
			t.Errorf("cmd.Env carries %q, want CENCI_SANDBOX and CENCI_BABYSIT_SUPERVISOR stripped", e)
		}
		if strings.Contains(e, armTestLaunchID) {
			t.Errorf("cmd.Env carries the launch ID in %q", e)
		}
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, armTestLaunchID) {
			t.Errorf("cmd.Args = %v carries the launch ID", cmd.Args)
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

// -- #1135: two sandboxes of one repo, real ResolveContainer -----------------

// newOriginCheckout creates a real git checkout whose origin is url.
func newOriginCheckout(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", url}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

// writeFakeRuntime writes a fake container runtime named bin into shimDir that
// logs its argv to logPath, lists every container on `ps`, and answers
// `inspect` (with or without --type/--format) per container name with its
// /workspace record; an unknown name fails like a real "no such container".
func writeFakeRuntime(t *testing.T, shimDir, bin, logPath string, sources map[string]string) {
	t.Helper()
	var names, arms strings.Builder
	for name, src := range sources {
		names.WriteString(exectest.ShellQuote(name) + " ")
		fmt.Fprintf(&arms, "    %s) printf '%%s::/workspace::true\\n---\\n' %s; shift ;;\n", exectest.ShellQuote(name), exectest.ShellQuote(src))
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + exectest.ShellQuote(logPath) + "\n" +
		"case \"$1\" in\n" +
		"ps) printf '%s\\n' " + names.String() + ";;\n" +
		"inspect)\n" +
		"  shift\n" +
		"  while [ $# -gt 0 ]; do\n" +
		"    case \"$1\" in\n" +
		"    --type|--format) shift 2 ;;\n" +
		arms.String() +
		"    *) printf 'Error: No such container: %s\\n' \"$1\" >&2; exit 1 ;;\n" +
		"    esac\n" +
		"  done\n" +
		"  ;;\n" +
		"*) exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(shimDir, bin), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// readCallLog returns the logged argv lines (a missing log is no calls).
func readCallLog(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// The regression test for #1135 item 2: two running sandboxes whose origins
// both normalize to the request's repo. The old scan-every-container resolve is
// ambiguous over this fixture (the positive control), yet an arm bound to
// container A spawns in A's source and one bound to B in B's, each through
// exactly one `docker inspect` of the bound container and no `ps`, and never
// touching the other runtime.
func TestHostArmSpawn_TwoSandboxesOfOneRepo_BoundContainerWins(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs a real git for the origin read")
	}
	const containerA, containerB = "claude-cenci-r-a", "codex-cenci-r-b"
	srcA := newOriginCheckout(t, "https://github.com/o/r.git")
	srcB := newOriginCheckout(t, "git@github.com:o/r.git")
	sources := map[string]string{containerA: srcA, containerB: srcB}

	shimDir := t.TempDir()
	dockerLog, podmanLog := filepath.Join(t.TempDir(), "docker.log"), filepath.Join(t.TempDir(), "podman.log")
	writeFakeRuntime(t, shimDir, "docker", dockerLog, sources)
	writeFakeRuntime(t, shimDir, "podman", podmanLog, sources)

	var bound atomic.Value
	bound.Store(containerA)
	pane := func(context.Context, string, string) (armPaneInfo, error) {
		p := boundPane()
		p.container = bound.Load().(string)
		return p, nil
	}
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, pane, nil, okStart())
	armResolveContainerRepo = hostrepo.ResolveContainer // the real resolver; installArmSeams' cleanup restores it
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Positive control: the fixture really is ambiguous for the old resolver.
	if _, err := hostrepo.Resolve(context.Background(), "o/r"); !errors.Is(err, hostrepo.ErrAmbiguous) {
		t.Fatalf("hostrepo.Resolve over the fixture = %v, want ErrAmbiguous (the fixture must hold two matching sandboxes)", err)
	}
	for _, p := range []string{dockerLog, podmanLog} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct{ container, wantDir string }{{containerA, srcA}, {containerB, srcB}} {
		bound.Store(tc.container)
		resp := d.hostArmSpawn(validArmRequest())
		if resp.Reason == ReasonArmHostRepoAmbiguous {
			t.Fatalf("bound to %s: nack %q -- another sandbox's origin must never block a bound arm", tc.container, resp.Reason)
		}
		if !resp.OK {
			t.Fatalf("bound to %s: nack %q, want an ack", tc.container, resp.Reason)
		}
		cmd := calls.capturedCmd()
		assertArgAfter(t, cmd.Args, "--dir", tc.wantDir)
		if cmd.Dir != tc.wantDir {
			t.Errorf("bound to %s: cmd.Dir = %q, want %q", tc.container, cmd.Dir, tc.wantDir)
		}

		lines := readCallLog(t, dockerLog)
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "inspect --type container ") || !strings.HasSuffix(lines[0], " "+tc.container) {
			t.Errorf("bound to %s: docker calls = %q, want exactly one `inspect --type container ... %s` and no ps", tc.container, lines, tc.container)
		}
		if err := os.Remove(dockerLog); err != nil {
			t.Fatal(err)
		}
	}
	if got := readCallLog(t, podmanLog); got != nil {
		t.Errorf("podman calls = %q, want none: the pane recorded docker", got)
	}
}

// -- #1135: live-supervisor caps ---------------------------------------------

// assertCapNack asserts a ReasonArmSupervisorCapReached nack and no new spawn.
func assertCapNack(t *testing.T, resp ipc.ArmResponse, calls *armSeamCalls, startsBefore int32) {
	t.Helper()
	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmSupervisorCapReached}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmSupervisorCapReached)
	}
	if got := calls.start.Load(); got != startsBefore {
		t.Errorf("armStartBabysit calls = %d, want unchanged at %d", got, startsBefore)
	}
}

// 8 live supervisors for a repo (counted case-insensitively) refuse a new PR of
// that repo, while the same request for a repo with fewer is admitted.
func TestHostArmSpawn_RepoCap_RefusesNinthSupervisor(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
	writeLiveStates(t, calls.stateDir, "o/r", prRange(100, armRepoSupervisorCap/2)...)
	writeLiveStates(t, calls.stateDir, "O/R", prRange(200, armRepoSupervisorCap-armRepoSupervisorCap/2)...)
	writeLiveStates(t, calls.stateDir, "o/other", prRange(300, armRepoSupervisorCap-1)...)

	assertCapNack(t, d.hostArmSpawn(armReqFor("o/r", 42)), calls, 0)

	if resp := d.hostArmSpawn(armReqFor("o/other", 42)); !resp.OK {
		t.Errorf("o/other at %d live: nack %q, want an ack", armRepoSupervisorCap-1, resp.Reason)
	}
}

// 24 live supervisors across the host refuse a new (repo, PR) even for a repo
// below its own cap; 23 admit it (the boundary).
func TestHostArmSpawn_HostCap_RefusesAtTwentyFour(t *testing.T) {
	perRepo := armHostSupervisorCap / 4
	if perRepo >= armRepoSupervisorCap {
		t.Fatalf("fixture needs per-repo counts below the repo cap: %d", perRepo)
	}
	for _, tc := range []struct {
		live  int
		admit bool
	}{{armHostSupervisorCap, false}, {armHostSupervisorCap - 1, true}} {
		t.Run(fmt.Sprintf("%d live", tc.live), func(t *testing.T) {
			d := newArmSpawnTestDaemon()
			calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
			remaining := tc.live
			for i := 0; remaining > 0; i++ {
				n := min(perRepo, remaining)
				writeLiveStates(t, calls.stateDir, fmt.Sprintf("o/repo%d", i), prRange(100, n)...)
				remaining -= n
			}

			resp := d.hostArmSpawn(armReqFor("o/new", 1))

			if tc.admit {
				if !resp.OK {
					t.Errorf("nack %q, want an ack below the host cap", resp.Reason)
				}
				return
			}
			assertCapNack(t, resp, calls, 0)
		})
	}
}

// A re-arm of a (repo, PR) that already has a live supervisor is spawned at the
// repo cap: the child's own .lock refuses the duplicate with the clearer
// "supervisor already running".
func TestHostArmSpawn_RearmOfLiveSupervisor_SkipsCap(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
	writeLiveStates(t, calls.stateDir, "o/r", append(prRange(100, armRepoSupervisorCap-1), 42)...)

	if resp := d.hostArmSpawn(armReqFor("o/r", 42)); !resp.OK {
		t.Fatalf("re-arm of a live PR at the cap: nack %q, want an ack", resp.Reason)
	}
	if got := calls.start.Load(); got != 1 {
		t.Errorf("armStartBabysit calls = %d, want 1", got)
	}
}

// Five concurrent admitted arms for distinct PRs of a repo holding 7 live
// supervisors: arms that are spawning but not yet visible count toward the cap,
// so exactly one spawns and four are refused.
func TestHostArmSpawn_ConcurrentArmsAtSevenLive_OneSpawnFourCapNacks(t *testing.T) {
	freezeArmClock(t)
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
	writeLiveStates(t, calls.stateDir, "o/r", prRange(100, armRepoSupervisorCap-1)...)

	const arms = armBucketBurst
	var ok, capped atomic.Int32
	var other sync.Map
	var wg sync.WaitGroup
	wg.Add(arms)
	for i := 0; i < arms; i++ {
		go func(pr int) {
			defer wg.Done()
			switch resp := d.hostArmSpawn(armReqFor("o/r", pr)); {
			case resp.OK:
				ok.Add(1)
			case resp.Reason == ReasonArmSupervisorCapReached:
				capped.Add(1)
			default:
				other.Store(pr, resp.Reason)
			}
		}(1 + i)
	}
	wg.Wait()

	other.Range(func(pr, reason any) bool {
		t.Errorf("PR %v: nack %q, want an ack or the cap reason", pr, reason)
		return true
	})
	if ok.Load() != 1 || capped.Load() != arms-1 {
		t.Errorf("acks = %d, cap nacks = %d, want 1 and %d", ok.Load(), capped.Load(), arms-1)
	}
	if got := calls.start.Load(); got != 1 {
		t.Errorf("armStartBabysit calls = %d, want exactly 1", got)
	}
}

// A reservation is released at once when the spawn fails, so the next arm at 7
// live is admitted; a held reservation expires after babysit.ArmingLivenessGrace.
func TestHostArmSpawn_Reservations_ReleasedOnFailureAndExpire(t *testing.T) {
	advance := freezeArmClock(t)
	var failStart atomic.Bool
	start := func(*exec.Cmd) error {
		if failStart.Load() {
			return errors.New("fork/exec: start refused")
		}
		return nil
	}
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), start)
	writeLiveStates(t, calls.stateDir, "o/r", prRange(100, armRepoSupervisorCap-1)...)
	captureLog(t)

	failStart.Store(true)
	if resp := d.hostArmSpawn(armReqFor("o/r", 1)); resp.Reason != ReasonArmSpawnFailed {
		t.Fatalf("failed start: resp = %+v, want reason %q", resp, ReasonArmSpawnFailed)
	}
	failStart.Store(false)
	if resp := d.hostArmSpawn(armReqFor("o/r", 2)); !resp.OK {
		t.Fatalf("after a failed start: nack %q, want an ack -- the failed arm's reservation must be released", resp.Reason)
	}
	startsBefore := calls.start.Load()
	assertCapNack(t, d.hostArmSpawn(armReqFor("o/r", 3)), calls, startsBefore)

	advance(babysit.ArmingLivenessGrace)
	if resp := d.hostArmSpawn(armReqFor("o/r", 3)); !resp.OK {
		t.Errorf("after the grace: nack %q, want an ack -- an expired reservation no longer counts", resp.Reason)
	}
}

// A reservation whose supervisor became visible in the state directory is
// dropped rather than counted twice, and a re-arm of a reserved key skips the
// cap.
func TestHostArmSpawn_Reservations_DroppedOnceLiveAndReservedKeySkipsCap(t *testing.T) {
	freezeArmClock(t)
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
	writeLiveStates(t, calls.stateDir, "o/r", prRange(100, armRepoSupervisorCap-2)...)

	if resp := d.hostArmSpawn(armReqFor("o/r", 1)); !resp.OK {
		t.Fatalf("PR 1: nack %q, want an ack", resp.Reason)
	}
	if resp := d.hostArmSpawn(armReqFor("o/r", 2)); !resp.OK {
		t.Fatalf("PR 2: nack %q, want an ack (live + reserved = %d)", resp.Reason, armRepoSupervisorCap-1)
	}
	// Disk + reservations is now at the cap; a re-arm of reserved PR 1 skips it.
	if resp := d.hostArmSpawn(armReqFor("o/r", 1)); !resp.OK {
		t.Fatalf("re-arm of reserved PR 1: nack %q, want an ack", resp.Reason)
	}

	// PR 1's supervisor becomes visible: its reservation must not count twice.
	// The five arms here use exactly the frozen bucket's armBucketBurst tokens.
	writeLiveStates(t, calls.stateDir, "o/r", 1)
	assertCapNack(t, d.hostArmSpawn(armReqFor("o/r", 3)), calls, calls.start.Load())
	if err := os.Remove(filepath.Join(calls.stateDir, "o_r-100.json")); err != nil {
		t.Fatal(err)
	}
	if resp := d.hostArmSpawn(armReqFor("o/r", 3)); !resp.OK {
		t.Errorf("PR 3 at %d live + PR 2 reserved: nack %q, want an ack -- PR 1 must count once", armRepoSupervisorCap-2, resp.Reason)
	}
}

// The count source failing refuses the arm with
// ReasonArmSupervisorCountUnavailable before any spawn. Through the seam this
// runs on every runner, root included.
func TestHostArmSpawn_SupervisorCountUnavailable_ViaSeam(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
	armLiveSupervisors = func(string) (babysit.LiveCounts, error) {
		return babysit.LiveCounts{}, errors.New("list babysit state directory: permission denied")
	}

	resp := d.hostArmSpawn(validArmRequest())

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmSupervisorCountUnavailable}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmSupervisorCountUnavailable)
	}
	if got := calls.start.Load(); got != 0 {
		t.Errorf("armStartBabysit calls = %d, want 0", got)
	}
}

// A count failure is a host fault (a bad state directory nacks every arm until
// fixed), not something a container can trigger, so it is logged without
// cfg.Verbose, like a spawn failure (#1372). The line names the wrapped
// state-directory error and never the launch ID.
func TestHostArmSpawn_SupervisorCountUnavailable_LogsWithoutVerbose(t *testing.T) {
	d := newArmSpawnTestDaemon()
	d.cfg.Verbose = false
	installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
	const stateErr = "list babysit state directory: permission denied"
	armLiveSupervisors = func(string) (babysit.LiveCounts, error) {
		return babysit.LiveCounts{}, errors.New(stateErr)
	}
	buf := captureLog(t)

	resp := d.hostArmSpawn(validArmRequest())

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmSupervisorCountUnavailable}) {
		t.Fatalf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmSupervisorCountUnavailable)
	}
	if !strings.Contains(buf.String(), stateErr) {
		t.Errorf("log = %q, want a line carrying the state-directory error without cfg.Verbose", buf.String())
	}
	for _, id := range []string{armTestLaunchID, armOtherLaunchID} {
		if strings.Contains(buf.String(), id) {
			t.Errorf("log %q carries a launch ID", buf.String())
		}
	}
}

// An unreadable state directory (permission denied, real chmod 000) refuses the
// arm with ReasonArmSupervisorCountUnavailable. The positive control fails the
// test when the directory is still readable (a root runner), so it can never
// pass without exercising the branch (watch/docs/test-strategy.md). A missing
// directory counts as zero and is admitted.
func TestHostArmSpawn_SupervisorCountUnavailable_UnreadableStateDir(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())

	if resp := d.hostArmSpawn(armReqFor("o/r", 1)); !resp.OK {
		t.Fatalf("missing state directory: nack %q, want an ack (zero live)", resp.Reason)
	}

	writeLiveStates(t, calls.stateDir, "o/r", 100)
	if err := os.Chmod(calls.stateDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(calls.stateDir, 0o700) })
	if _, err := os.ReadDir(calls.stateDir); err == nil {
		t.Fatalf("positive control: %s is still readable after chmod 000 (root runner?); the permission-denied branch would not run", calls.stateDir)
	}

	resp := d.hostArmSpawn(armReqFor("o/r", 2))

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmSupervisorCountUnavailable}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmSupervisorCountUnavailable)
	}
	if got := calls.start.Load(); got != 1 {
		t.Errorf("armStartBabysit calls = %d, want 1 (only the missing-directory arm)", got)
	}
}

// One state file that fails to decode counts as a live supervisor toward the
// host total only: it does not fill a repo's quota, but it does reach the host
// cap.
func TestHostArmSpawn_CorruptStateFile_CountsTowardHostTotalOnly(t *testing.T) {
	writeCorrupt := func(t *testing.T, stateDir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(stateDir, "corrupt-1.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("not toward the repo", func(t *testing.T) {
		d := newArmSpawnTestDaemon()
		calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
		writeLiveStates(t, calls.stateDir, "o/r", prRange(100, armRepoSupervisorCap-1)...)
		writeCorrupt(t, calls.stateDir)

		if resp := d.hostArmSpawn(armReqFor("o/r", 42)); !resp.OK {
			t.Errorf("nack %q, want an ack: a corrupt file must not count toward o/r", resp.Reason)
		}
	})

	t.Run("toward the host", func(t *testing.T) {
		d := newArmSpawnTestDaemon()
		calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
		perRepo := armRepoSupervisorCap - 1
		for i, remaining := 0, armHostSupervisorCap-1; remaining > 0; i++ {
			n := min(perRepo, remaining)
			writeLiveStates(t, calls.stateDir, fmt.Sprintf("o/repo%d", i), prRange(100, n)...)
			remaining -= n
		}
		writeCorrupt(t, calls.stateDir)

		assertCapNack(t, d.hostArmSpawn(armReqFor("o/new", 1)), calls, 0)
	})
}

// -- no launch ID in replies or logs ----------------------------------------

// With Verbose on, no reply and no host log line on the arm path carries the
// request's launch ID or the pane's: a mismatch, a spawn failure and a success
// (through handleArmRequest, so the validation and socket-rejection logs are
// covered too).
func TestArmRequest_LaunchIDNeverInRepliesOrLogs(t *testing.T) {
	mismatch := boundPane()
	mismatch.launchID = armOtherLaunchID
	for _, tc := range []struct {
		name   string
		pane   armPaneFn
		start  func(t *testing.T) func(*exec.Cmd) error
		wantOK bool
	}{
		{"mismatch", fixedPane(mismatch), func(*testing.T) func(*exec.Cmd) error { return okStart() }, false},
		{"spawn failure", fixedPane(boundPane()), func(*testing.T) func(*exec.Cmd) error {
			return func(*exec.Cmd) error { return errors.New("fork/exec: start refused") }
		}, false},
		{"success", fixedPane(boundPane()), realStart, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newArmSpawnTestDaemon()
			d.armSpawn = d.hostArmSpawn
			d.cfg.Verbose = true
			installArmSeams(t, tc.pane, fixedRepoDir(t.TempDir()), tc.start(t))
			buf := captureLog(t)

			req := validArmRequest()
			req.TmuxSocket = filepath.Join(t.TempDir(), "untrusted") // logged and dropped under Verbose
			resp := d.handleArmRequest(req)

			if resp.OK != tc.wantOK {
				t.Fatalf("resp = %+v, want OK=%v", resp, tc.wantOK)
			}
			if buf.Len() == 0 {
				t.Fatal("verbose log is empty, want the arm path to log (or the assertion below is vacuous)")
			}
			for _, id := range []string{armTestLaunchID, armOtherLaunchID} {
				if strings.Contains(resp.Reason, id) {
					t.Errorf("reply %+v carries a launch ID", resp)
				}
				if strings.Contains(buf.String(), id) {
					t.Errorf("log %q carries a launch ID", buf.String())
				}
			}
		})
	}
}

// -- verbose-gated reject logging ------------------------------------------

// TestHostArmSpawn_RejectLogsUnderVerbose covers the reject-branch logging
// rule (watch/docs/hook-events.md rule 2) across the reject modes: each must
// log under cfg.Verbose and stay silent when it's off.
func TestHostArmSpawn_RejectLogsUnderVerbose(t *testing.T) {
	mismatch := boundPane()
	mismatch.launchID = armOtherLaunchID
	cases := []struct {
		name     string
		pane     armPaneFn
		resolve  armResolveFn
		liveRepo int // live supervisors of o/r to seed
	}{
		{"pane unresolvable", failingPane(errors.New("no pane")), fixedRepoDir("/repo"), 0},
		{"launch unverified", fixedPane(mismatch), fixedRepoDir("/repo"), 0},
		{"host repo not found", fixedPane(boundPane()), failingResolve(hostrepo.ErrNoMatch), 0},
		{"supervisor cap reached", fixedPane(boundPane()), fixedRepoDir("/repo"), armRepoSupervisorCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newArmSpawnTestDaemon()
			calls := installArmSeams(t, tc.pane, tc.resolve, okStart())
			writeLiveStates(t, calls.stateDir, "o/r", prRange(100, tc.liveRepo)...)

			buf := captureLog(t)
			d.cfg.Verbose = false
			if resp := d.hostArmSpawn(validArmRequest()); resp.OK {
				t.Fatal("resp.OK = true, want a reject")
			}
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

	// The rate-limited branch needs a primed bucket, so it cannot share the
	// table's single fresh-daemon shape. The clock is frozen so no token
	// refills between the primed requests and the asserted ones. The priming
	// requests are admitted (the token is taken first) and then fail at the
	// start seam: a successful start would spawn a reapArmSpawn goroutine that
	// reads d.cfg.Verbose while this test flips it.
	t.Run("rate limited", func(t *testing.T) {
		freezeArmClock(t)
		d := newArmSpawnTestDaemon()
		installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), func(*exec.Cmd) error { return errors.New("start refused") })
		buf := captureLog(t)
		for i := 0; i < armBucketBurst; i++ {
			if resp := d.hostArmSpawn(validArmRequest()); resp.Reason != ReasonArmSpawnFailed {
				t.Fatalf("priming request %d: resp.Reason = %q, want %q (admitted, then start refused)", i, resp.Reason, ReasonArmSpawnFailed)
			}
		}

		buf.Reset()
		d.cfg.Verbose = false
		if resp := d.hostArmSpawn(validArmRequest()); resp.Reason != ReasonArmRateLimited {
			t.Fatalf("resp.Reason = %q, want %q", resp.Reason, ReasonArmRateLimited)
		}
		if buf.Len() != 0 {
			t.Errorf("log output with cfg.Verbose=false: %q, want no output", buf.String())
		}

		buf.Reset()
		d.cfg.Verbose = true
		if resp := d.hostArmSpawn(validArmRequest()); resp.Reason != ReasonArmRateLimited {
			t.Fatalf("resp.Reason = %q, want %q", resp.Reason, ReasonArmRateLimited)
		}
		if buf.Len() == 0 {
			t.Error("log output with cfg.Verbose=true: empty, want the rate-limited reject to log")
		}
	})
}

// -- #1372: self-exec binary resolution and spawn-failure visibility ---------

// writeExecutableFixture creates an executable regular file in a fresh temp
// dir and returns its path.
func writeExecutableFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// deletedExecutablePath returns a path that existed and was then removed --
// what os.Executable() reports for a daemon whose plugin cache was refreshed.
func deletedExecutablePath(t *testing.T) string {
	t.Helper()
	path := writeExecutableFixture(t, "cenci-deleted")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestHostArmSpawn_DeletedExecutable_SpawnsInstallLink is the #1372 repro: the
// daemon's own binary was deleted by a plugin refresh, yet the install link
// under HOME still points at a working binary. The spawned child must come
// from that link, through the real selfexe resolver.
func TestHostArmSpawn_DeletedExecutable_SpawnsInstallLink(t *testing.T) {
	home := t.TempDir()
	fixture := writeExecutableFixture(t, "cenci-installed")
	link := filepath.Join(home, ".local", "bin", "cenci")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture, link); err != nil {
		t.Fatal(err)
	}
	deleted := deletedExecutablePath(t)

	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir(t.TempDir()), okStart())
	resolver := selfexe.Resolver{
		HomeDir:    func() string { return home },
		LookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
		Executable: func() (string, error) { return deleted, nil },
		GOOS:       runtime.GOOS,
	}
	installSelfExe(t, resolver.Resolve)

	resp := d.hostArmSpawn(validArmRequest())

	if resp != (ipc.ArmResponse{OK: true}) {
		t.Fatalf("resp = %+v, want an ack despite the deleted daemon executable", resp)
	}
	cmd := calls.capturedCmd()
	if cmd == nil {
		t.Fatal("armStartBabysit was invoked but captured no *exec.Cmd")
	}
	if cmd.Path != link {
		t.Errorf("cmd.Path = %q, want the install link %q, not the deleted executable %q", cmd.Path, link, deleted)
	}
}

// TestHostArmSpawn_SpawnFailure_AlwaysLogsAndNacksBareReason covers both
// spawn-failure branches (resolver error, start error): with cfg.Verbose off
// there is still exactly one host log line naming the attempted path (or the
// no-usable-binary sentinel), the error and a restart hint, while the nack
// carries only the bare constant -- no host path, no error text.
func TestHostArmSpawn_SpawnFailure_AlwaysLogsAndNacksBareReason(t *testing.T) {
	resolveErr := fmt.Errorf("%w: tried link, PATH and os.Executable (resolver-detail)", selfexe.ErrNoUsableBinary)
	startErr := errors.New("fork/exec: start-detail")

	cases := []struct {
		name       string
		resolve    func() (string, error)
		start      func(*exec.Cmd) error
		wantInLog  []string
		hostDetail []string
	}{
		{
			name:       "resolver error",
			resolve:    func() (string, error) { return "", resolveErr },
			start:      okStart(),
			wantInLog:  []string{"no usable cenci binary", resolveErr.Error(), "restart"},
			hostDetail: []string{"no usable cenci binary", "resolver-detail"},
		},
		{
			name:       "start error",
			resolve:    func() (string, error) { return fakeSelfExe, nil },
			start:      func(*exec.Cmd) error { return startErr },
			wantInLog:  []string{fakeSelfExe, startErr.Error(), "restart"},
			hostDetail: []string{fakeSelfExe, "start-detail"},
		},
	}
	for _, tc := range cases {
		for _, verbose := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s verbose=%v", tc.name, verbose), func(t *testing.T) {
				d := newArmSpawnTestDaemon()
				d.cfg.Verbose = verbose
				installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), tc.start)
				installSelfExe(t, tc.resolve)
				buf := captureLog(t)

				resp := d.hostArmSpawn(validArmRequest())

				if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmSpawnFailed}) {
					t.Errorf("resp = %+v, want exactly {OK:false Reason:%q}", resp, ReasonArmSpawnFailed)
				}
				for _, detail := range tc.hostDetail {
					if strings.Contains(resp.Reason, detail) {
						t.Errorf("resp.Reason = %q leaks host detail %q", resp.Reason, detail)
					}
				}
				lines := logLines(buf)
				if len(lines) != 1 {
					t.Fatalf("log lines = %d (%q), want exactly one line per failed admitted request", len(lines), buf.String())
				}
				for _, want := range tc.wantInLog {
					if !strings.Contains(lines[0], want) {
						t.Errorf("log line %q does not contain %q", lines[0], want)
					}
				}
			})
		}
	}
}

// TestHostArmSpawn_BinaryDeletedBeforeStart_NacksAndLogsPath covers the race
// between resolution and start: the resolved binary vanishes, the real
// cmd.Start() fails, and the nack is the bare reason while the always-on log
// line names the vanished path and the start error.
func TestHostArmSpawn_BinaryDeletedBeforeStart_NacksAndLogsPath(t *testing.T) {
	gone := deletedExecutablePath(t)
	var startErr error
	start := func(cmd *exec.Cmd) error {
		startErr = cmd.Start()
		return startErr
	}

	d := newArmSpawnTestDaemon()
	d.cfg.Verbose = false
	installArmSeams(t, fixedPane(boundPane()), fixedRepoDir(t.TempDir()), start)
	installSelfExe(t, func() (string, error) { return gone, nil })
	buf := captureLog(t)

	resp := d.hostArmSpawn(validArmRequest())

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmSpawnFailed}) {
		t.Errorf("resp = %+v, want exactly {OK:false Reason:%q}", resp, ReasonArmSpawnFailed)
	}
	if startErr == nil {
		t.Fatal("the real cmd.Start() succeeded for a deleted binary, want a start error")
	}
	lines := logLines(buf)
	if len(lines) != 1 {
		t.Fatalf("log lines = %d (%q), want exactly one", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], gone) {
		t.Errorf("log line %q does not name the deleted path %q", lines[0], gone)
	}
	if !strings.Contains(lines[0], startErr.Error()) {
		t.Errorf("log line %q does not contain the start error %q", lines[0], startErr)
	}
}

// TestHostArmSpawn_ProcSelfExe_ArgvKeepsBabysitAndPR covers the last-resort
// candidate: when the child is spawned through /proc/self/exe its argv must
// still carry `babysit` and the PR, because internal/babysit's
// /proc/<pid>/cmdline ownership check matches on argv, not on the binary path.
func TestHostArmSpawn_ProcSelfExe_ArgvKeepsBabysitAndPR(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/self/exe is only a spawn candidate on Linux")
	}
	if _, err := os.Lstat("/proc/self/exe"); err != nil {
		t.Skipf("procfs unavailable: %v", err)
	}
	resolver := selfexe.Resolver{
		HomeDir:    func() string { return "" },
		LookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
		Executable: func() (string, error) { return deletedExecutablePath(t), nil },
		GOOS:       "linux",
	}

	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir(t.TempDir()), okStart())
	installSelfExe(t, resolver.Resolve)

	req := validArmRequest()
	resp := d.hostArmSpawn(req)

	if !resp.OK {
		t.Fatalf("resp = %+v, want an ack through /proc/self/exe", resp)
	}
	cmd := calls.capturedCmd()
	if cmd == nil {
		t.Fatal("armStartBabysit was invoked but captured no *exec.Cmd")
	}
	if cmd.Path != "/proc/self/exe" {
		t.Errorf("cmd.Path = %q, want /proc/self/exe", cmd.Path)
	}
	var hasBabysit, hasPR bool
	for _, a := range cmd.Args[1:] {
		hasBabysit = hasBabysit || a == "babysit"
		hasPR = hasPR || a == req.PR
	}
	if !hasBabysit || !hasPR {
		t.Errorf("cmd.Args = %v, want both %q and the PR %q so babysit's cmdline ownership check still matches", cmd.Args, "babysit", req.PR)
	}
}

// TestHostArmSpawn_RepeatedSpawnFailures_OneLogLinePerAdmittedRequest bounds
// the always-on log volume: requests past the token bucket are rejected before
// resolution and stay verbose-only, so 2*armBucketBurst failing requests with
// verbose off write exactly armBucketBurst lines.
func TestHostArmSpawn_RepeatedSpawnFailures_OneLogLinePerAdmittedRequest(t *testing.T) {
	freezeArmClock(t)
	d := newArmSpawnTestDaemon()
	d.cfg.Verbose = false
	installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())
	installSelfExe(t, func() (string, error) { return "", selfexe.ErrNoUsableBinary })
	buf := captureLog(t)

	var spawnFailed, rateLimited int
	for i := 0; i < 2*armBucketBurst; i++ {
		switch resp := d.hostArmSpawn(validArmRequest()); resp.Reason {
		case ReasonArmSpawnFailed:
			spawnFailed++
		case ReasonArmRateLimited:
			rateLimited++
		default:
			t.Fatalf("request %d: resp = %+v, want a spawn-failed or rate-limited nack", i, resp)
		}
	}

	if spawnFailed != armBucketBurst || rateLimited != armBucketBurst {
		t.Fatalf("spawn-failed nacks = %d, rate-limited nacks = %d, want %d of each", spawnFailed, rateLimited, armBucketBurst)
	}
	if got := len(logLines(buf)); got != armBucketBurst {
		t.Errorf("log lines = %d (%q), want %d: one per admitted request, none for over-rate ones", got, buf.String(), armBucketBurst)
	}
}

// -- rate limiting ----------------------------------------------------------

// TestHostArmSpawn_BurstUpToCapacityAdmitted covers the token bucket's
// burst allowance: armBucketBurst consecutive requests on a fresh daemon
// (zero-value armLimiter = full bucket on first use) must all be admitted
// through to a successful spawn.
func TestHostArmSpawn_BurstUpToCapacityAdmitted(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())

	for i := 0; i < armBucketBurst; i++ {
		resp := d.hostArmSpawn(validArmRequest())
		if !resp.OK {
			t.Fatalf("request %d/%d: resp.OK = false (reason %q), want every request up to burst capacity (%d) admitted", i+1, armBucketBurst, resp.Reason, armBucketBurst)
		}
	}
}

// The request after burst capacity is exhausted nacks ReasonArmRateLimited
// before any pane read, bound-container resolution or fork (#1095 Decisions:
// "gate the whole arm path, not just the spawn"; #1135 keeps the order).
func TestHostArmSpawn_OverCapacity_NacksRateLimited_SeamsNeverInvoked(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())

	for i := 0; i < armBucketBurst; i++ {
		if resp := d.hostArmSpawn(validArmRequest()); !resp.OK {
			t.Fatalf("priming request %d: resp.OK = false (reason %q), want admitted", i, resp.Reason)
		}
	}
	paneBefore, resolveBefore, startBefore := calls.pane.Load(), calls.resolve.Load(), calls.start.Load()

	resp := d.hostArmSpawn(validArmRequest())

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmRateLimited}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmRateLimited)
	}
	if got := calls.pane.Load(); got != paneBefore {
		t.Errorf("armPaneIdentity calls = %d, want unchanged at %d -- the rate gate must run before any pane read", got, paneBefore)
	}
	if got := calls.resolve.Load(); got != resolveBefore {
		t.Errorf("armResolveContainerRepo calls = %d, want unchanged at %d", got, resolveBefore)
	}
	if got := calls.start.Load(); got != startBefore {
		t.Errorf("armStartBabysit calls = %d, want unchanged at %d", got, startBefore)
	}
}

// TestHostArmSpawn_RateLimitReplenishesAfterRefillInterval covers the
// bucket's refill side: once capacity is exhausted, advancing armNow past
// armBucketRefill must admit exactly one more request.
func TestHostArmSpawn_RateLimitReplenishesAfterRefillInterval(t *testing.T) {
	advance := freezeArmClock(t)
	d := newArmSpawnTestDaemon()
	installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())

	for i := 0; i < armBucketBurst; i++ {
		if resp := d.hostArmSpawn(validArmRequest()); !resp.OK {
			t.Fatalf("priming request %d: resp.OK = false (reason %q), want admitted", i, resp.Reason)
		}
	}
	if resp := d.hostArmSpawn(validArmRequest()); resp.OK || resp.Reason != ReasonArmRateLimited {
		t.Fatalf("resp = %+v, want a rate-limited nack once capacity is exhausted", resp)
	}

	advance(armBucketRefill)
	resp := d.hostArmSpawn(validArmRequest())
	if !resp.OK {
		t.Errorf("resp.OK = false (reason %q), want admitted once armNow advances past the refill interval", resp.Reason)
	}
}

// TestHostArmSpawn_RateLimiterRaceClean covers the mutex-guarding
// requirement: hostArmSpawn executes on the ipc.EventReceiver's connection
// goroutine, so concurrent callers must never race on d.armLimiter or
// d.armReservations. Run with -race; at most armBucketBurst calls out of a
// larger concurrent burst may be admitted.
func TestHostArmSpawn_RateLimiterRaceClean(t *testing.T) {
	d := newArmSpawnTestDaemon()
	installArmSeams(t, fixedPane(boundPane()), fixedRepoDir("/repo"), okStart())

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
// These let a real *exec.Cmd get killed by armSpawnBudget's context deadline
// and assert the resulting error, once it reaches hostArmSpawn through the
// real (non-seamed) pane read or bound-container resolution, produces
// ReasonArmResolutionTimedOut -- the seam-level timeout tests above fabricate
// the joined error and would stay green even if execArmTmux/runRuntime never
// wrapped context.DeadlineExceeded on a mid-call kill.

// A fake `tmux` on PATH sleeps well past armSpawnBudget with the real
// defaultArmPaneIdentity in place.
func TestHostArmSpawn_PaneResolutionRealDeadlineExceeded_NacksResolutionTimedOut(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, nil, fixedRepoDir("/repo"), okStart())
	armPaneIdentity = defaultArmPaneIdentity // the real reader; installArmSeams' cleanup restores the seam

	shimDir := t.TempDir()
	// "exec sleep N" (never a bare "sleep N" line) is load-bearing: it replaces
	// the shell's own image with sleep, so the CommandContext kill reaches the
	// sleeping process instead of orphaning a grandchild holding the pipe.
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	resp := d.hostArmSpawn(validArmRequest())
	elapsed := time.Since(start)

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmResolutionTimedOut}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmResolutionTimedOut)
	}
	if r, s := calls.resolve.Load(), calls.start.Load(); r != 0 || s != 0 {
		t.Errorf("resolve calls = %d, start calls = %d, want 0 and 0", r, s)
	}
	if elapsed >= 9*time.Second {
		t.Fatalf("hostArmSpawn took %v, want it bounded by armSpawnBudget (%v), not the full 10s sleep", elapsed, armSpawnBudget)
	}
}

// A fake `docker` on PATH sleeps well past armSpawnBudget when the real
// hostrepo.ResolveContainer inspects the bound container.
func TestHostArmSpawn_HostRepoResolutionRealDeadlineExceeded_NacksResolutionTimedOut(t *testing.T) {
	d := newArmSpawnTestDaemon()
	calls := installArmSeams(t, fixedPane(boundPane()), nil, okStart())
	armResolveContainerRepo = hostrepo.ResolveContainer // the real resolver

	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "docker"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	resp := d.hostArmSpawn(validArmRequest())
	elapsed := time.Since(start)

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmResolutionTimedOut}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmResolutionTimedOut)
	}
	if got := calls.start.Load(); got != 0 {
		t.Errorf("armStartBabysit calls = %d, want 0", got)
	}
	if elapsed >= 9*time.Second {
		t.Fatalf("hostArmSpawn took %v, want it bounded by armSpawnBudget (%v), not the full 10s sleep", elapsed, armSpawnBudget)
	}
}

// -- #1431 (P29) + #1135: the real pane-identity read ------------------------
//
// These drive handleArmRequest -> hostArmSpawn with the REAL pane reader
// (defaultArmPaneIdentity) over a fake `tmux` that answers per `-S <socket>`
// and per pane, so what reaches tmux, how its 4-field answer parses, and the
// spawned supervisor's argv are what is asserted.

// armWorld is a daemon wired to the real pane reader, with the repo and
// process-start seams faked and a trusted tmux socket directory.
type armWorld struct {
	d         *Daemon
	socketDir string // the trusted directory handleArmRequest checks sockets against
	tmuxLog   string
	calls     *armSeamCalls
}

const armWorldRepoDir = "/resolved/host/repo"

// The panes the armWorld fake tmux answers specially.
const (
	armWorldGonePane      = "%90" // empty output: tmux's answer for a pane that does not exist
	armWorldUntaggedPane  = "%91" // a session, no launch options: a host pane
	armWorldThreeFields   = "%92"
	armWorldFiveFields    = "%93"
	armWorldEmptySession  = "%94" // four empty fields
	armWorldUntaggedReply = "untagged-session\t\t\t\n"
)

// newArmWorld installs a fake tmux that logs every call (argv, one line per
// call) and answers `display-message` with a 4-field pane identity: by pane for
// the armWorld* panes, otherwise by socket -- session-a/claude-cenci-a on
// <trusted>/a, session-b/claude-cenci-b on <trusted>/b, ambient-session/
// claude-cenci-ambient when no -S leads -- every one tagged with the request's
// launch ID under docker. The log path is baked into the script: a socket
// call's child env is only PATH/HOME/TERM/LANG.
func newArmWorld(t *testing.T) *armWorld {
	t.Helper()
	d := newArmSpawnTestDaemon()
	d.armSpawn = d.hostArmSpawn
	w := &armWorld{d: d, socketDir: trustedSocketDir(t, d)}
	w.calls = installArmSeams(t, nil, fixedRepoDir(armWorldRepoDir), realStart(t))
	armPaneIdentity = defaultArmPaneIdentity // the real reader; installArmSeams' cleanup restores the seam

	shimDir := t.TempDir()
	w.tmuxLog = filepath.Join(t.TempDir(), "tmux.log")
	id := armTestLaunchID
	script := "#!/bin/sh\n" +
		`printf '%s\n' "$*" >> '` + w.tmuxLog + "'\n" +
		"sock=\n" +
		`if [ "$1" = -S ]; then sock=$2; shift 2; fi` + "\n" +
		`case "$3" in` + "\n" +
		"  " + armWorldGonePane + ") exit 0 ;;\n" +
		"  " + armWorldUntaggedPane + `) printf 'untagged-session\t\t\t\n'; exit 0 ;;` + "\n" +
		"  " + armWorldThreeFields + `) printf 's\t` + id + `\tclaude-cenci-a\n'; exit 0 ;;` + "\n" +
		"  " + armWorldFiveFields + `) printf 's\t` + id + `\tclaude-cenci-a\tdocker\textra\n'; exit 0 ;;` + "\n" +
		"  " + armWorldEmptySession + `) printf '\t\t\t\n'; exit 0 ;;` + "\n" +
		"esac\n" +
		`case "$sock" in` + "\n" +
		`  */a) printf 'session-a\t` + id + `\tclaude-cenci-a\tdocker\n' ;;` + "\n" +
		`  */b) printf 'session-b\t` + id + `\tclaude-cenci-b\tdocker\n' ;;` + "\n" +
		`  *) printf 'ambient-session\t` + id + `\tclaude-cenci-ambient\tdocker\n' ;;` + "\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return w
}

func (w *armWorld) socket(name string) string { return filepath.Join(w.socketDir, name) }

// arm sends a valid request for pane on socket through handleArmRequest.
func (w *armWorld) arm(pane, socket string) ipc.ArmResponse {
	req := validArmRequest()
	req.TmuxPane, req.TmuxSocket = pane, socket
	return w.d.handleArmRequest(req)
}

// tmuxCalls returns the logged tmux argv lines (a missing log is no calls).
func (w *armWorld) tmuxCalls(t *testing.T) []string {
	t.Helper()
	return readCallLog(t, w.tmuxLog)
}

// supervisorArgs returns the argv of the supervisor the last ack spawned.
func (w *armWorld) supervisorArgs(t *testing.T) []string {
	t.Helper()
	cmd := w.calls.capturedCmd()
	if cmd == nil {
		t.Fatal("no supervisor was spawned")
	}
	return cmd.Args
}

// paneReadArgv is the logged argv of one pane-identity read of pane.
func paneReadArgv(pane string) string {
	return "display-message -t " + pane + " -p " + armPaneIdentityFormat
}

// The same pane id exists on two servers. Each request's pane is read on its
// own socket in one 4-field call, the repo is resolved from that server's
// pane's container, and the supervisor is told which server its session lives
// on.
func TestArmRequest_PaneResolvesOnTheRequestsOwnSocket(t *testing.T) {
	w := newArmWorld(t)
	captureLog(t)

	for _, tc := range []struct{ name, wantSession string }{{"a", "session-a"}, {"b", "session-b"}} {
		sock := w.socket(tc.name)
		if resp := w.arm("%5", sock); !resp.OK {
			t.Fatalf("arm on %s: nack %q, want an ack", sock, resp.Reason)
		}
		args := w.supervisorArgs(t)
		assertArgAfter(t, args, "--session", tc.wantSession)
		if n := len(args); n < 2 || args[n-2] != "--tmux-socket" || args[n-1] != sock {
			t.Errorf("supervisor argv = %q, want it to end with --tmux-socket %s", args, sock)
		}
	}
	wantCalls := []string{
		"-S " + w.socket("a") + " " + paneReadArgv("%5"),
		"-S " + w.socket("b") + " " + paneReadArgv("%5"),
	}
	if got := w.tmuxCalls(t); !reflect.DeepEqual(got, wantCalls) {
		t.Errorf("tmux calls = %q, want %q", got, wantCalls)
	}
	wantResolves := []armResolveCall{{"docker", "claude-cenci-a", "o/r"}, {"docker", "claude-cenci-b", "o/r"}}
	if got := w.calls.recordedResolves(); !reflect.DeepEqual(got, wantResolves) {
		t.Errorf("resolves = %+v, want %+v", got, wantResolves)
	}
}

// A socket that fails the shape or trusted-directory check is dropped to the
// ambient default (never a nack): no -S reaches tmux, the supervisor gets no
// --tmux-socket, the rejection is logged only under Verbose, and a malformed value
// is never echoed into that log.
func TestArmRequest_UntrustedSocketSendsNoDashS(t *testing.T) {
	loose := t.TempDir() // 0755: not the trusted directory
	for _, tc := range []struct {
		name   string
		socket func(w *armWorld) string
	}{
		{"outside the trusted directory", func(w *armWorld) string { return filepath.Join(loose, "a") }},
		{"nested below the trusted directory", func(w *armWorld) string { return w.socket("sub/a") }},
		{"relative path", func(w *armWorld) string { return "relative/a" }},
		{"unprintable character", func(w *armWorld) string { return w.socket("evil\x1b[31mred") }},
		{"tab", func(w *armWorld) string { return w.socket("a\tb") }},
	} {
		for _, verbose := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/verbose=%v", tc.name, verbose), func(t *testing.T) {
				w := newArmWorld(t)
				w.d.cfg.Verbose = verbose
				buf := captureLog(t)

				resp := w.arm("%5", tc.socket(w))

				if !resp.OK {
					t.Fatalf("nack %q: a rejected socket falls back to the default server, it is not a nack", resp.Reason)
				}
				calls := w.tmuxCalls(t)
				if len(calls) != 1 || strings.HasPrefix(calls[0], "-S") {
					t.Errorf("tmux calls = %q, want exactly one with no -S", calls)
				}
				args := w.supervisorArgs(t)
				if slices.Contains(args, "--tmux-socket") {
					t.Errorf("supervisor argv = %q, want no --tmux-socket for a rejected socket", args)
				}
				assertArgAfter(t, args, "--session", "ambient-session")
				out := buf.String()
				if verbose && !strings.Contains(out, "socket") {
					t.Errorf("verbose log = %q, want a line naming the rejected socket", out)
				}
				if !verbose && out != "" {
					t.Errorf("log = %q, want nothing without Verbose", out)
				}
				if strings.Contains(out, "evil") || strings.Contains(out, "\x1b") || strings.Contains(out, "\t") {
					t.Errorf("log = %q echoes a malformed socket value", out)
				}
			})
		}
	}
}

// An empty socket keeps today's exact supervisor argv -- no --tmux-socket --
// and a bare tmux call.
func TestArmRequest_EmptySocketKeepsTodaysArgv(t *testing.T) {
	w := newArmWorld(t)
	captureLog(t)
	req := validArmRequest()

	if resp := w.d.handleArmRequest(req); !resp.OK {
		t.Fatalf("nack %q, want an ack", resp.Reason)
	}

	want := []string{fakeSelfExe, "babysit", req.PR, "--agent", req.Agent, "--interval", req.Interval.String(), "--session", "ambient-session", "--dir", armWorldRepoDir}
	if got := w.supervisorArgs(t); !slices.Equal(got, want) {
		t.Errorf("supervisor argv = %q, want %q", got, want)
	}
	if got, want := w.tmuxCalls(t), []string{paneReadArgv("%3")}; !slices.Equal(got, want) {
		t.Errorf("tmux calls = %q, want %q", got, want)
	}
}

// The real reader's answer shapes, on both the ambient server and a request
// socket: a gone pane (empty output) or a four-empty-field answer is
// ReasonArmPaneUnresolvable; an untagged pane, a 3-field and a 5-field answer
// are ReasonArmLaunchUnverified. None reaches the bound-container resolution.
func TestArmRequest_RealPaneReadShapes(t *testing.T) {
	for _, tc := range []struct {
		name, pane, want string
	}{
		{"gone pane", armWorldGonePane, ReasonArmPaneUnresolvable},
		{"empty session", armWorldEmptySession, ReasonArmPaneUnresolvable},
		{"untagged pane", armWorldUntaggedPane, ReasonArmLaunchUnverified},
		{"three fields", armWorldThreeFields, ReasonArmLaunchUnverified},
		{"five fields", armWorldFiveFields, ReasonArmLaunchUnverified},
	} {
		for _, onSocket := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/socket=%v", tc.name, onSocket), func(t *testing.T) {
				w := newArmWorld(t)
				captureLog(t)
				sock := ""
				if onSocket {
					sock = w.socket("a")
				}

				resp := w.arm(tc.pane, sock)

				if resp != (ipc.ArmResponse{OK: false, Reason: tc.want}) {
					t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, tc.want)
				}
				if r, s := w.calls.resolve.Load(), w.calls.start.Load(); r != 0 || s != 0 {
					t.Errorf("resolve calls = %d, start calls = %d, want 0 and 0", r, s)
				}
			})
		}
	}
}

// The reader keeps empty trailing fields: an untagged pane's answer
// ("session\t\t\t\n") parses as a session with no launch options rather than as
// a malformed read, on both the ambient server and a socket -- which is only
// possible when the tmux stdout is not whitespace-trimmed.
func TestDefaultArmPaneIdentity_UntaggedPaneKeepsEmptyTrailingFields(t *testing.T) {
	w := newArmWorld(t)
	for _, sock := range []string{"", w.socket("a")} {
		got, err := defaultArmPaneIdentity(context.Background(), sock, armWorldUntaggedPane)
		if err != nil {
			t.Fatalf("socket %q: err = %v, want the untagged pane read without error (%q)", sock, err, armWorldUntaggedReply)
		}
		if want := (armPaneInfo{session: "untagged-session"}); got != want {
			t.Errorf("socket %q: pane = %+v, want %+v", sock, got, want)
		}
	}
	got, err := defaultArmPaneIdentity(context.Background(), "", "%5")
	if err != nil {
		t.Fatal(err)
	}
	if want := (armPaneInfo{session: "ambient-session", launchID: armTestLaunchID, container: "claude-cenci-ambient", runtime: "docker"}); got != want {
		t.Errorf("tagged pane = %+v, want %+v", got, want)
	}
}

// The socket-carrying pane read stays bounded by armSpawnBudget: a tmux that
// never answers on the request's socket is a resolution timeout, classified like
// the bare call (the real subprocess is killed, not just a fabricated error).
func TestArmRequest_SocketResolutionRealDeadlineNacksResolutionTimedOut(t *testing.T) {
	d := newArmSpawnTestDaemon()
	d.armSpawn = d.hostArmSpawn
	sockDir := trustedSocketDir(t, d)
	calls := installArmSeams(t, nil, fixedRepoDir("/repo"), okStart())
	armPaneIdentity = defaultArmPaneIdentity
	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	req := validArmRequest()
	req.TmuxSocket = filepath.Join(sockDir, "a")

	start := time.Now()
	resp := d.handleArmRequest(req)
	elapsed := time.Since(start)

	if resp != (ipc.ArmResponse{OK: false, Reason: ReasonArmResolutionTimedOut}) {
		t.Errorf("resp = %+v, want {OK:false Reason:%q}", resp, ReasonArmResolutionTimedOut)
	}
	if r, s := calls.resolve.Load(), calls.start.Load(); r != 0 || s != 0 {
		t.Errorf("resolve calls = %d, start calls = %d, want 0 and 0", r, s)
	}
	if elapsed >= 9*time.Second {
		t.Errorf("handleArmRequest took %v, want it bounded by armSpawnBudget (%v)", elapsed, armSpawnBudget)
	}
}

// -- #1095 review fix: reapArmSpawn's non-zero-exit logging path -----------

// TestReapArmSpawn_LogsUnderVerbose covers reapArmSpawn directly: a real
// short-lived failing child's non-zero exit must log under cfg.Verbose and
// stay silent when it's off -- reapArmSpawn is the only place a spawned
// supervisor's own failure (as opposed to a rejected arm request) ever becomes
// visible.
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
