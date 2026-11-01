package daemon

// Ticket #1390: the daemon half of the forwarded `cenci babysit stop` and
// `cenci babysit status`. A container can ask the host daemon to SIGTERM a
// host process through these handlers, so the tests pin the validation order,
// the fail-closed nacks (with the seam provably not called), the accepted
// ambiguity, the reply text (no host paths or PIDs) and the end-to-end flow.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/hostrepo"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// ctlSeams records the stop/status seam calls and scripts their results.
type ctlSeams struct {
	stopCalls   atomic.Int32
	statusCalls atomic.Int32
	resolves    atomic.Int32

	lastRepo string
	lastPR   int

	stopErr   error
	statusErr error
	status    ipc.BabysitStatus
}

// newCtlDaemon builds a test daemon whose stop/status seams record into the
// returned ctlSeams and whose host-repo resolver is the given fake.
func newCtlDaemon(t *testing.T, resolve func(ctx context.Context, repo string) (string, error)) (*Daemon, *ctlSeams) {
	t.Helper()
	seams := &ctlSeams{}
	orig := armResolveHostRepo
	armResolveHostRepo = func(ctx context.Context, repo string) (string, error) {
		seams.resolves.Add(1)
		return resolve(ctx, repo)
	}
	t.Cleanup(func() { armResolveHostRepo = orig })

	d := newTestDaemon(&tmuxtest.MockClient{})
	d.babysitStop = func(repo string, pr int) error {
		seams.stopCalls.Add(1)
		seams.lastRepo, seams.lastPR = repo, pr
		return seams.stopErr
	}
	d.babysitStatus = func(repo string, pr int) (ipc.BabysitStatus, error) {
		seams.statusCalls.Add(1)
		seams.lastRepo, seams.lastPR = repo, pr
		return seams.status, seams.statusErr
	}
	return d, seams
}

func resolvesTo(dir string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return dir, nil }
}

func resolveFails(err error) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return "", err }
}

// ctlKind runs one control kind against a daemon and reports ack/reason plus
// the number of seam calls it made.
type ctlKind struct {
	name  string
	call  func(d *Daemon, pr, repo string) (bool, string)
	calls func(*ctlSeams) int32
}

var ctlKinds = []ctlKind{
	{
		name: "stop",
		call: func(d *Daemon, pr, repo string) (bool, string) {
			resp := d.handleBabysitStop(ipc.BabysitStopRequest{PR: pr, Repo: repo})
			return resp.OK, resp.Reason
		},
		calls: func(s *ctlSeams) int32 { return s.stopCalls.Load() },
	},
	{
		name: "status",
		call: func(d *Daemon, pr, repo string) (bool, string) {
			resp := d.handleBabysitStatus(ipc.BabysitStatusRequest{PR: pr, Repo: repo})
			return resp.OK, resp.Reason
		},
		calls: func(s *ctlSeams) int32 { return s.statusCalls.Load() },
	},
}

// TestBabysitControl_NacksNeverReachTheSeam is P3: every reject class nacks
// with its own stable reason, and neither the stop/status seam nor (for the
// classes that precede it) the host-repo resolver is touched.
func TestBabysitControl_NacksNeverReachTheSeam(t *testing.T) {
	for _, kind := range ctlKinds {
		for _, tc := range []struct {
			name         string
			pr, repo     string
			resolve      func(context.Context, string) (string, error)
			wantReason   string
			wantResolves int32
		}{
			{"PR zero", "0", "o/r", resolvesTo("/h"), ReasonInvalidPR, 0},
			{"PR negative", "-1", "o/r", resolvesTo("/h"), ReasonInvalidPR, 0},
			{"PR not numeric", "abc", "o/r", resolvesTo("/h"), ReasonInvalidPR, 0},
			{"PR empty", "", "o/r", resolvesTo("/h"), ReasonInvalidPR, 0},
			{"repo traversal", "42", "../..", resolvesTo("/h"), ReasonInvalidRepo, 0},
			{"repo dot owner", "42", "./x", resolvesTo("/h"), ReasonInvalidRepo, 0},
			{"repo dot name", "42", "o/..", resolvesTo("/h"), ReasonInvalidRepo, 0},
			{"repo single dot name", "42", "o/.", resolvesTo("/h"), ReasonInvalidRepo, 0},
			{"repo empty", "42", "", resolvesTo("/h"), ReasonInvalidRepo, 0},
			{"repo three segments", "42", "a/b/c", resolvesTo("/h"), ReasonInvalidRepo, 0},
			{"repo leading dash", "42", "-o/r", resolvesTo("/h"), ReasonInvalidRepo, 0},
			{"repo with space", "42", "o/r r", resolvesTo("/h"), ReasonInvalidRepo, 0},
			{"no running sandbox matches", "42", "o/r", resolveFails(hostrepo.ErrNoMatch), ReasonArmHostRepoNotFound, 1},
			{"probe failed", "42", "o/r", resolveFails(errors.New("docker inspect exploded at /secret/path")), ReasonArmHostRepoProbeFailed, 1},
			{"probe timed out", "42", "o/r", resolveFails(fmt.Errorf("probe: %w", context.DeadlineExceeded)), ReasonArmResolutionTimedOut, 1},
		} {
			t.Run(kind.name+"/"+tc.name, func(t *testing.T) {
				d, seams := newCtlDaemon(t, tc.resolve)

				ok, reason := kind.call(d, tc.pr, tc.repo)

				if ok {
					t.Fatal("ok = true, want a nack")
				}
				if reason != tc.wantReason {
					t.Errorf("reason = %q, want %q", reason, tc.wantReason)
				}
				if got := kind.calls(seams); got != 0 {
					t.Errorf("seam called %d times, want 0", got)
				}
				if got := seams.resolves.Load(); got != tc.wantResolves {
					t.Errorf("resolver calls = %d, want %d", got, tc.wantResolves)
				}
			})
		}
	}
}

// TestBabysitControl_RateLimitedBeforeResolveAndShared is P3's bucket row:
// stop and status draw from one bucket, an empty bucket nacks before any
// container inspect, and the arm bucket is a different bucket.
func TestBabysitControl_RateLimitedBeforeResolveAndShared(t *testing.T) {
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	origNow := armNow
	armNow = func() time.Time { return fixed }
	t.Cleanup(func() { armNow = origNow })

	d, seams := newCtlDaemon(t, resolvesTo("/h"))
	for i := 0; i < armBucketBurst; i++ {
		kind := ctlKinds[i%2] // alternate stop/status: one shared bucket
		if ok, reason := kind.call(d, "42", "o/r"); !ok {
			t.Fatalf("request %d (%s) nacked inside the burst: %q", i+1, kind.name, reason)
		}
	}
	resolvesBefore := seams.resolves.Load()
	callsBefore := make([]int32, len(ctlKinds))
	for i, kind := range ctlKinds {
		callsBefore[i] = kind.calls(seams)
	}

	for i, kind := range ctlKinds {
		ok, reason := kind.call(d, "42", "o/r")
		if ok || reason != ReasonArmRateLimited {
			t.Errorf("%s over the burst: ok=%v reason=%q, want a %q nack", kind.name, ok, reason, ReasonArmRateLimited)
		}
		if got := kind.calls(seams); got != callsBefore[i] {
			t.Errorf("%s seam calls grew from %d to %d: a rate-limited request must not reach the seam", kind.name, callsBefore[i], got)
		}
	}
	if got := seams.resolves.Load(); got != resolvesBefore {
		t.Errorf("resolver calls grew from %d to %d: an over-rate request must not pay for a container inspect", resolvesBefore, got)
	}
	if !d.armLimiter.take() {
		t.Error("the arm bucket was drained by stop/status requests; the control bucket must be separate")
	}

	// One token per armBucketRefill: advancing the clock admits a request again.
	armNow = func() time.Time { return fixed.Add(armBucketRefill + time.Second) }
	if ok, reason := ctlKinds[0].call(d, "42", "o/r"); !ok {
		t.Errorf("after the refill interval: nacked %q, want admitted", reason)
	}
}

// TestBabysitControl_AmbiguousMatchIsAccepted is P21: the state path depends
// only on repo and PR, so two matching sandboxes do not block a stop/status.
func TestBabysitControl_AmbiguousMatchIsAccepted(t *testing.T) {
	for _, kind := range ctlKinds {
		t.Run(kind.name, func(t *testing.T) {
			d, seams := newCtlDaemon(t, resolveFails(fmt.Errorf("two checkouts: %w", hostrepo.ErrAmbiguous)))

			ok, reason := kind.call(d, "0042", "o/r")

			if !ok {
				t.Fatalf("nacked %q, want the ambiguous match accepted", reason)
			}
			if got := kind.calls(seams); got != 1 {
				t.Fatalf("seam calls = %d, want 1", got)
			}
			if seams.lastRepo != "o/r" || seams.lastPR != 42 {
				t.Errorf("seam saw repo=%q pr=%d, want o/r and the canonical 42", seams.lastRepo, seams.lastPR)
			}
		})
	}
}

// TestBabysitControl_SeamErrorsMapToFixedReasons is P20/P33: each failure
// class has a fixed reason, and no host path or PID in the seam's error text
// reaches the reply.
func TestBabysitControl_SeamErrorsMapToFixedReasons(t *testing.T) {
	leaky := func(class error) error {
		return fmt.Errorf("%w: /home/dev/.local/state/cenci/babysit/abc-42.json pid 4242 output: boom", class)
	}
	stopCases := []struct {
		name string
		err  error
		want string
	}{
		{"no supervisor", babysit.ErrNoSupervisor, "no supervisor found for PR #42"},
		{"unverified process", leaky(babysit.ErrSupervisorUnverified), ReasonBabysitStopUnverified},
		{"unreadable state", leaky(babysit.ErrStateUnreadable), "babysit state for PR #42 could not be read"},
		{"signal failed", leaky(babysit.ErrStopFailed), ReasonBabysitStopFailed},
		{"unclassified", errors.New("open /home/dev/x: permission denied"), ReasonBabysitStopFailed},
	}
	for _, tc := range stopCases {
		t.Run("stop/"+tc.name, func(t *testing.T) {
			d, seams := newCtlDaemon(t, resolvesTo("/h"))
			seams.stopErr = tc.err
			ok, reason := ctlKinds[0].call(d, "42", "o/r")
			if ok || reason != tc.want {
				t.Errorf("ok=%v reason=%q, want a nack %q", ok, reason, tc.want)
			}
			assertReasonIsHostFree(t, reason)
		})
	}
	statusCases := []struct {
		name string
		err  error
		want string
	}{
		{"no supervisor", babysit.ErrNoSupervisor, "no supervisor found for PR #42"},
		{"unreadable state", leaky(babysit.ErrStateUnreadable), "babysit state for PR #42 could not be read"},
		{"unclassified", errors.New("open /home/dev/x: permission denied"), ReasonBabysitStatusFailed},
	}
	for _, tc := range statusCases {
		t.Run("status/"+tc.name, func(t *testing.T) {
			d, seams := newCtlDaemon(t, resolvesTo("/h"))
			seams.statusErr = tc.err
			ok, reason := ctlKinds[1].call(d, "42", "o/r")
			if ok || reason != tc.want {
				t.Errorf("ok=%v reason=%q, want a nack %q", ok, reason, tc.want)
			}
			assertReasonIsHostFree(t, reason)
		})
	}
}

func assertReasonIsHostFree(t *testing.T, reason string) {
	t.Helper()
	for _, leak := range []string{"/home", "/h", ".json", "4242", "boom", "output"} {
		if strings.Contains(reason, leak) {
			t.Errorf("reason %q contains host detail %q", reason, leak)
		}
	}
}

func TestBabysitControl_StatusAckCarriesTheSeamStatus(t *testing.T) {
	d, seams := newCtlDaemon(t, resolvesTo("/h"))
	seams.status = ipc.BabysitStatus{PR: 42, Repo: "o/r", Status: "needs-input", CIFixAttempts: 3, FixCap: 3}

	resp := d.handleBabysitStatus(ipc.BabysitStatusRequest{PR: "42", Repo: "o/r"})

	if !resp.OK || resp.Status == nil || *resp.Status != seams.status {
		t.Fatalf("resp = %+v, want an ack carrying %+v", resp, seams.status)
	}
}

// TestBabysitControl_InertSeamsNackUntilInstalled pins item 12: newDaemon's
// default seams never touch real state.
func TestBabysitControl_InertSeamsNackUntilInstalled(t *testing.T) {
	orig := armResolveHostRepo
	armResolveHostRepo = resolvesTo("/h")
	t.Cleanup(func() { armResolveHostRepo = orig })

	d := newDaemon(testConfig(), nil, make(chan ipc.HookEvent), &mockReaper{})
	for _, kind := range ctlKinds {
		ok, reason := kind.call(d, "42", "o/r")
		if ok || reason != ReasonBabysitControlUnavailable {
			t.Errorf("%s: ok=%v reason=%q, want the inert nack %q", kind.name, ok, reason, ReasonBabysitControlUnavailable)
		}
	}
}

// TestInstallHostSeams_InstallsTheRealBabysitControlSeams pins that
// installHostSeams (Run's only caller) wires babysit.StopRepo/StatusRepo, so
// dropping the wiring cannot go unnoticed: against an empty state directory
// the real seams report "no supervisor", the inert ones report "unavailable".
func TestInstallHostSeams_InstallsTheRealBabysitControlSeams(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	orig := armResolveHostRepo
	armResolveHostRepo = resolvesTo("/h")
	t.Cleanup(func() { armResolveHostRepo = orig })

	d := newTestDaemon(&tmuxtest.MockClient{})
	d.installHostSeams(func(string, string) bool { return true })

	for _, kind := range ctlKinds {
		ok, reason := kind.call(d, "42", "o/r")
		if ok || reason != "no supervisor found for PR #42" {
			t.Errorf("%s: ok=%v reason=%q, want the real seam's %q", kind.name, ok, reason, "no supervisor found for PR #42")
		}
	}
}

// -- end to end: sandbox client -> event socket -> daemon handler -> real host state ---

// shortSocketDir returns a 0700 CENCI_SOCKET_DIR short enough for sun_path.
func shortSocketDir(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CENCI_SOCKET_DIR", dir)
}

// writeGhShim puts a `gh` first on PATH that answers every call with repo,
// and neutralises the gh credential environment (watch/docs/test-isolation.md).
func writeGhShim(t *testing.T, repo string) {
	t.Helper()
	shim := t.TempDir()
	script := "#!/bin/sh\necho " + repo + "\n"
	if err := os.WriteFile(filepath.Join(shim, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(k, "")
	}
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
}

// hostStateFile returns the host babysit state file path for (repo, pr) under
// stateHome, mirroring babysit.statePath's `<12-hex of sha256(repo)>-<pr>.json`.
func hostStateFile(stateHome, repo, pr string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(stateHome, "cenci", "babysit", hex.EncodeToString(sum[:6])+"-"+pr+".json")
}

// startControlDaemon serves a real EventReceiver on the default event socket
// with d's stop/status handlers installed, as Run does.
func startControlDaemon(t *testing.T, d *Daemon) {
	t.Helper()
	recv, err := ipc.NewEventReceiver(ipc.DefaultEventSocketPath())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	recv.SetBabysitStopHandler(d.handleBabysitStop)
	recv.SetBabysitStatusHandler(d.handleBabysitStatus)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go recv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)
}

func captureStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	return func() string {
		os.Stdout = orig
		_ = w.Close()
		b, _ := io.ReadAll(r)
		_ = r.Close()
		return string(b)
	}
}

func newE2EDaemon(t *testing.T) (d *Daemon, stateHome string) {
	t.Helper()
	shortSocketDir(t)
	stateHome = t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("CENCI_SANDBOX", "1")
	writeGhShim(t, "o/r")

	orig := armResolveHostRepo
	armResolveHostRepo = resolvesTo("/host/checkout")
	t.Cleanup(func() { armResolveHostRepo = orig })

	d = newTestDaemon(&tmuxtest.MockClient{})
	d.installHostSeams(func(string, string) bool { return true })
	startControlDaemon(t, d)
	return d, stateHome
}

// TestBabysitControl_EndToEnd_ForwardedStopRemovesTheHostState is P1: the
// sandbox client's stop reaches the daemon over the real socket, the real
// StopRepo removes the host state file, and the client prints the host line.
func TestBabysitControl_EndToEnd_ForwardedStopRemovesTheHostState(t *testing.T) {
	_, stateHome := newE2EDaemon(t)
	path := hostStateFile(stateHome, "o/r", "42")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"pr":"42","repo":"o/r","status":"running"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	done := captureStdout(t)
	err := babysit.Stop("42", "")
	out := done()

	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if out != "Stopped babysitting PR #42 on the host.\n" {
		t.Errorf("stdout = %q, want %q", out, "Stopped babysitting PR #42 on the host.\n")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("host state file still exists (stat err = %v)", statErr)
	}
}

// TestBabysitControl_EndToEnd_StopNacksCarryNoHostDetail is P20/P33 over the
// real wire: a missing state and an unverifiable recorded process each come
// back as the fixed reason, and nothing in the reply names a path or a PID.
func TestBabysitControl_EndToEnd_StopNacksCarryNoHostDetail(t *testing.T) {
	_, stateHome := newE2EDaemon(t)

	t.Run("no state file", func(t *testing.T) {
		done := captureStdout(t)
		err := babysit.Stop("42", "")
		out := done()
		if err == nil || !strings.Contains(err.Error(), "not stopped: no supervisor found for PR #42") {
			t.Fatalf("err = %v, want it to relay %q", err, "not stopped: no supervisor found for PR #42")
		}
		if strings.Contains(out, "Stopped babysitting") {
			t.Errorf("stdout = %q, must not claim a stop", out)
		}
	})

	t.Run("recorded process cannot be verified", func(t *testing.T) {
		// A throwaway child's pid is real and alive, but its command line
		// carries neither "babysit" nor this PR number, so processOwned is
		// false and nothing is signalled. A child, never this test binary:
		// where /proc is unreadable processOwned falls back to a liveness
		// probe, which would let StopRepo SIGTERM the test runner.
		const pr = "987654"
		child := exec.Command("sleep", "300")
		if err := child.Start(); err != nil {
			t.Fatalf("start child: %v", err)
		}
		t.Cleanup(func() {
			_ = child.Process.Kill()
			_ = child.Wait()
		})
		if _, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "cmdline")); err != nil {
			t.Skipf("process command lines are unreadable here (%v): ownership falls back to liveness, so a refused stop cannot be exercised safely", err)
		}
		path := hostStateFile(stateHome, "o/r", pr)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"pr":%q,"repo":"o/r","pid":%d,"status":"running"}`, pr, child.Process.Pid)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		err := babysit.Stop(pr, "")

		if err == nil || !strings.Contains(err.Error(), "not stopped: "+ReasonBabysitStopUnverified) {
			t.Fatalf("err = %v, want it to relay %q", err, "not stopped: "+ReasonBabysitStopUnverified)
		}
		if strings.Contains(err.Error(), "/") || strings.Contains(err.Error(), "pid") {
			t.Errorf("err %q carries a pid or a path", err.Error())
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("state file must survive a refused stop, stat err = %v", statErr)
		}
	})

	t.Run("corrupt state is unreadable and kept", func(t *testing.T) {
		const pr = "987655"
		path := hostStateFile(stateHome, "o/r", pr)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}

		done := captureStdout(t)
		err := babysit.Stop(pr, "")
		out := done()

		want := "not stopped: " + fmt.Sprintf(ReasonBabysitStateUnreadableFmt, 987655)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to relay %q", err, want)
		}
		if strings.Contains(out, "Stopped babysitting") {
			t.Errorf("stdout = %q, must not claim a stop", out)
		}
		if strings.Contains(err.Error(), stateHome) || strings.Contains(err.Error(), ".json") {
			t.Errorf("err %q names a host path", err.Error())
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("corrupt state file must survive a refused stop, stat err = %v", statErr)
		}
	})

	t.Run("raw reply strings", func(t *testing.T) {
		resp, err := ipc.SendBabysitStop(ipc.DefaultEventSocketPath(), ipc.BabysitStopRequest{PR: "7", Repo: "o/r"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.OK {
			t.Fatal("ok = true for a PR with no state")
		}
		for _, leak := range []string{stateHome, "/host/checkout", "/"} {
			if strings.Contains(resp.Reason, leak) {
				t.Errorf("reason %q contains %q", resp.Reason, leak)
			}
		}
	})
}

// TestBabysitControl_EndToEnd_ForwardedStatusReportsTheHostState is P4/P24
// over the real wire: a PID-0 needs-input state reports supervisorLive false
// and the persisted status, and the printed line names no path.
func TestBabysitControl_EndToEnd_ForwardedStatusReportsTheHostState(t *testing.T) {
	_, stateHome := newE2EDaemon(t)
	path := hostStateFile(stateHome, "o/r", "42")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"schemaVersion":2,"pr":"42","repo":"o/r","status":"needs-input","ciFixAttempts":3,"conflictFixAttempts":1}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	done := captureStdout(t)
	err := babysit.Status("42", "")
	out := done()

	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	const want = `{"pr":42,"repo":"o/r","status":"needs-input","ciFixAttempts":3,"conflictFixAttempts":1,"fixCap":3,"supervisorLive":false}` + "\n"
	if out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if strings.Contains(out, stateHome) || strings.Contains(out, "/host/checkout") {
		t.Errorf("stdout %q names a host path", out)
	}
}
