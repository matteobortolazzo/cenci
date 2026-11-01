package babysit

// Tickets #1094 and #1390: the in-container client side of the forwards to the
// host daemon -- armOnHost's outcome mapping (ack/nack/no-response -> armed/not
// armed/arm status unknown), Run's CENCI_SANDBOX arm gate, and the Stop/Status
// forwards (stopOnHost, statusOnHost) with their own three outcomes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// tempArmSocket returns a bind-safe Unix socket path, mirroring internal/ipc's
// own tempSocket helper -- a short os.MkdirTemp-backed dir keeps the path
// under macOS's 104-byte sun_path cap.
func tempArmSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bsarm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir + "/s.sock"
}

// captureStdout redirects os.Stdout into a pipe for the duration of the
// call, returning a function that restores the original os.Stdout and
// returns everything written meanwhile -- mirroring launch_test.go's inline
// os.Stderr-swap pattern in TestArmOutsideTmuxWarnsAndRecordsEmptySession,
// adapted for stdout.
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
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		_ = r.Close()
		return string(buf[:n])
	}
}

// startSilentArmListener starts a raw (non-ipc.EventReceiver) Unix listener
// that accepts one connection, reads whatever line arrives, and closes
// without ever writing a response -- exactly the pre-#1094 daemon's
// observable behavior for an unrecognized "kind" (drop to Events(), close).
// Used to drive armOnHost's "arm status unknown" outcome (AC4).
func startSilentArmListener(t *testing.T) string {
	t.Helper()
	path := tempArmSocket(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		_ = conn.Close()
	}()
	return path
}

// -- AC1: forwarding writes no local state and never spawns a supervisor ----

// TestRun_ForwardsArmRequestWhenSandboxed drives babysit.Run itself (not
// armOnHost directly) through the CENCI_SANDBOX forward gate: it must send
// exactly one arm request carrying pr/repo/agent/interval/tmux_pane, must
// never invoke startSupervisor, and must leave --state-dir empty (AC1).
func TestRun_ForwardsArmRequestWhenSandboxed(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "1")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
	t.Setenv("TMUX_PANE", "%7")
	dir := t.TempDir()

	path := tempArmSocket(t)
	recv, err := ipc.NewEventReceiver(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = recv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var received []ipc.ArmRequest
	recv.SetArmHandler(func(req ipc.ArmRequest) ipc.ArmResponse {
		mu.Lock()
		received = append(received, req)
		mu.Unlock()
		return ipc.ArmResponse{OK: true}
	})
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)

	originalArmSocketPath := armSocketPath
	armSocketPath = func() string { return path }
	t.Cleanup(func() { armSocketPath = originalArmSocketPath })

	originalExecGh := execGh
	execGh = func(args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "repo" {
			return "o/r\n", "", nil
		}
		return "", "", nil
	}
	t.Cleanup(func() { execGh = originalExecGh })

	originalStartSupervisor := startSupervisor
	startSupervisor = func(cmd *exec.Cmd) error {
		t.Error("startSupervisor must never be invoked when forwarding to the host")
		return nil
	}
	t.Cleanup(func() { startSupervisor = originalStartSupervisor })

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: 15 * time.Minute}); err != nil {
		t.Fatalf("Run (forward): %v", err)
	}

	mu.Lock()
	gotReceived := append([]ipc.ArmRequest{}, received...)
	mu.Unlock()
	if len(gotReceived) != 1 {
		t.Fatalf("arm requests received = %d, want exactly 1", len(gotReceived))
	}
	got := gotReceived[0]
	if got.PR != "42" || got.Repo != "o/r" || got.Agent != "claude" || got.Interval != 15*time.Minute || got.TmuxPane != "%7" {
		t.Errorf("arm request = %+v, want PR=42 Repo=o/r Agent=claude Interval=15m0s TmuxPane=%%7", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("state dir has %d entries, want 0 -- the forward path must write no container-side state/lock", len(entries))
	}
}

// -- outcome mapping: ack / nack / no-response -------------------------------

// TestArmOnHost_AckPrintsHostSupervisionMessage covers AC3's ack outcome:
// armOnHost returns nil and prints that the supervisor now runs on the host.
func TestArmOnHost_AckPrintsHostSupervisionMessage(t *testing.T) {
	path := tempArmSocket(t)
	recv, err := ipc.NewEventReceiver(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = recv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recv.SetArmHandler(func(ipc.ArmRequest) ipc.ArmResponse { return ipc.ArmResponse{OK: true} })
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)

	originalArmSocketPath := armSocketPath
	armSocketPath = func() string { return path }
	t.Cleanup(func() { armSocketPath = originalArmSocketPath })

	stop := captureStdout(t)
	err = armOnHost(Options{PR: "42", Agent: "claude", Interval: 15 * time.Minute}, "o/r")
	out := stop()

	if err != nil {
		t.Fatalf("armOnHost (ack): %v", err)
	}
	if !strings.Contains(out, "runs on the host") {
		t.Errorf("stdout = %q, want a message that the supervisor runs on the host", out)
	}
}

// TestArmOnHost_NackRelaysReasonVerbatim covers AC3's nack outcome and the
// "relay, never re-derive" rule: the reason is one this client package never
// itself defines, proving armOnHost passes it through unchanged.
func TestArmOnHost_NackRelaysReasonVerbatim(t *testing.T) {
	path := tempArmSocket(t)
	recv, err := ipc.NewEventReceiver(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = recv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const reason = "a reason ticket 2 defines, not this client package"
	recv.SetArmHandler(func(ipc.ArmRequest) ipc.ArmResponse {
		return ipc.ArmResponse{OK: false, Reason: reason}
	})
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)

	originalArmSocketPath := armSocketPath
	armSocketPath = func() string { return path }
	t.Cleanup(func() { armSocketPath = originalArmSocketPath })

	err = armOnHost(Options{PR: "42", Agent: "claude", Interval: 15 * time.Minute}, "o/r")
	if err == nil {
		t.Fatal("armOnHost: want a non-nil error on a nack")
	}
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("armOnHost err = %q, want it to contain the daemon's reason %q verbatim", err.Error(), reason)
	}
}

// TestArmOnHost_DialFailureReportsNotArmedDistinctFromUnknown covers
// auto-adopted answer #8: a dial failure (no socket, no daemon) is "not
// armed" with a client-derived reason, textually distinct from the
// "arm status unknown" outcome (nothing was written, so nothing can have
// spawned).
func TestArmOnHost_DialFailureReportsNotArmedDistinctFromUnknown(t *testing.T) {
	path := tempArmSocket(t) // nothing listens on this path

	originalArmSocketPath := armSocketPath
	armSocketPath = func() string { return path }
	t.Cleanup(func() { armSocketPath = originalArmSocketPath })

	err := armOnHost(Options{PR: "42", Agent: "claude", Interval: 15 * time.Minute}, "o/r")
	if err == nil {
		t.Fatal("armOnHost: want a non-nil error when nothing is listening")
	}
	if strings.Contains(err.Error(), "arm status unknown") {
		t.Errorf("armOnHost err = %q, want a dial failure reported as a distinct outcome from arm status unknown", err.Error())
	}
}

// TestArmOnHost_NoResponseReportsArmStatusUnknownWithReArmCommand covers
// AC4: a daemon that reads the request and closes without responding
// (wire-compatible with a pre-#1094 daemon dropping the line to Events())
// must report the third outcome, "arm status unknown", and print the host
// verification/re-arm command (auto-adopted answer #9: the re-arm command
// itself doubles as verification, since Run's lock refuses a duplicate).
func TestArmOnHost_NoResponseReportsArmStatusUnknownWithReArmCommand(t *testing.T) {
	path := startSilentArmListener(t)

	originalArmSocketPath := armSocketPath
	armSocketPath = func() string { return path }
	t.Cleanup(func() { armSocketPath = originalArmSocketPath })

	stop := captureStdout(t)
	err := armOnHost(Options{PR: "42", Agent: "claude", Interval: 15 * time.Minute}, "o/r")
	out := stop()
	combined := out
	if err != nil {
		combined += err.Error()
	}

	if err == nil {
		t.Fatal("armOnHost: want a non-nil error when the daemon closes without responding")
	}
	if !strings.Contains(combined, "arm status unknown") {
		t.Errorf("armOnHost output = %q, want the \"arm status unknown\" outcome", combined)
	}
	if !strings.Contains(combined, "cenci babysit 42 --agent claude") {
		t.Errorf("armOnHost output = %q, want the host verification/re-arm command", combined)
	}
}

// -- #1390: Stop and Status forwarded from the sandbox -----------------------

// sandboxControlFixture puts the process in forwarding mode: CENCI_SANDBOX=1,
// the gh repo lookup answered with "o/r", and armSocketPath pointed at path.
func sandboxControlFixture(t *testing.T, path string) {
	t.Helper()
	t.Setenv("CENCI_SANDBOX", "1")
	originalSocket := armSocketPath
	armSocketPath = func() string { return path }
	t.Cleanup(func() { armSocketPath = originalSocket })
	originalExecGh := execGh
	execGh = func(args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "repo" {
			return "o/r\n", "", nil
		}
		return "", "", fmt.Errorf("unexpected gh call: %v", args)
	}
	t.Cleanup(func() { execGh = originalExecGh })
}

// startControlReceiver runs an ipc receiver with the given handlers (either
// may be nil) and returns its socket path.
func startControlReceiver(t *testing.T, stop func(ipc.BabysitStopRequest) ipc.BabysitStopResponse, status func(ipc.BabysitStatusRequest) ipc.BabysitStatusResponse) string {
	t.Helper()
	path := tempArmSocket(t)
	recv, err := ipc.NewEventReceiver(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	if stop != nil {
		recv.SetBabysitStopHandler(stop)
	}
	if status != nil {
		recv.SetBabysitStatusHandler(status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)
	return path
}

func stopWith(resp ipc.BabysitStopResponse, got *ipc.BabysitStopRequest) func(ipc.BabysitStopRequest) ipc.BabysitStopResponse {
	return func(req ipc.BabysitStopRequest) ipc.BabysitStopResponse {
		if got != nil {
			*got = req
		}
		return resp
	}
}

func statusWith(resp ipc.BabysitStatusResponse) func(ipc.BabysitStatusRequest) ipc.BabysitStatusResponse {
	return func(ipc.BabysitStatusRequest) ipc.BabysitStatusResponse { return resp }
}

// TestStop_SandboxAckPrintsHostMessageAndWritesNoLocalState covers P1: the
// forwarded stop sends the repo and canonical PR, prints the host-stop line,
// and (state dir accepted and ignored) touches no container-side state.
func TestStop_SandboxAckPrintsHostMessageAndWritesNoLocalState(t *testing.T) {
	var got ipc.BabysitStopRequest
	path := startControlReceiver(t, stopWith(ipc.BabysitStopResponse{OK: true}, &got), nil)
	sandboxControlFixture(t, path)
	dir := t.TempDir()

	done := captureStdout(t)
	err := Stop("#42", dir)
	out := done()

	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if out != "Stopped babysitting PR #42 on the host.\n" {
		t.Errorf("stdout = %q, want %q", out, "Stopped babysitting PR #42 on the host.\n")
	}
	if got.PR != "42" || got.Repo != "o/r" {
		t.Errorf("stop request = %+v, want PR=42 Repo=o/r", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("state dir has %d entries, want 0", len(entries))
	}
}

// TestStop_SandboxNackRelaysReasonWithHostCommand covers P2/P20: the daemon's
// reason is relayed verbatim after "not stopped: ", the host command is
// printed, and the success line is not.
func TestStop_SandboxNackRelaysReasonWithHostCommand(t *testing.T) {
	const reason = "no supervisor found for PR #42"
	path := startControlReceiver(t, stopWith(ipc.BabysitStopResponse{OK: false, Reason: reason}, nil), nil)
	sandboxControlFixture(t, path)

	done := captureStdout(t)
	err := Stop("42", "")
	out := done()

	if err == nil {
		t.Fatal("Stop: want an error on a nack")
	}
	if !strings.Contains(err.Error(), "not stopped: "+reason) {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "not stopped: "+reason)
	}
	if !strings.Contains(err.Error(), "cenci babysit stop 42") {
		t.Errorf("err = %q, want the host command", err.Error())
	}
	if strings.Contains(out+err.Error(), "Stopped babysitting") {
		t.Errorf("output %q / err %q must not claim the supervisor was stopped", out, err.Error())
	}
}

// TestStop_SandboxNoReplyReportsStopStatusUnknown covers P2/P25 for a daemon
// that reads the line and closes without replying (a pre-#1390 daemon
// routes the unknown kind to its hook-event path and drops the connection).
func TestStop_SandboxNoReplyReportsStopStatusUnknown(t *testing.T) {
	sandboxControlFixture(t, startSilentArmListener(t))

	done := captureStdout(t)
	err := Stop("42", "")
	out := done()

	if err == nil {
		t.Fatal("Stop: want an error when the daemon never replies")
	}
	if !strings.Contains(err.Error(), "stop status unknown") {
		t.Errorf("err = %q, want %q", err.Error(), "stop status unknown")
	}
	if !strings.Contains(err.Error(), "cenci babysit stop 42") {
		t.Errorf("err = %q, want the host command", err.Error())
	}
	if strings.Contains(err.Error(), "not stopped") {
		t.Errorf("err = %q: an unknown outcome must not read as a definite not-stopped", err.Error())
	}
	if strings.Contains(out, "Stopped babysitting") {
		t.Errorf("stdout = %q, must not claim the supervisor was stopped", out)
	}
}

// startHeldOpenListener starts a raw Unix listener that reads the request line
// and then holds the connection open without ever replying, so the client can
// only give up at its response deadline.
func startHeldOpenListener(t *testing.T) string {
	t.Helper()
	path := tempArmSocket(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		<-release
	}()
	return path
}

// TestStop_SandboxHeldOpenConnectionReportsStopStatusUnknownAtTheDeadline is
// the P25 deadline half at the client level: a daemon that reads the request
// but neither replies nor closes yields "stop status unknown" plus the host
// command once the (shortened) response deadline passes.
func TestStop_SandboxHeldOpenConnectionReportsStopStatusUnknownAtTheDeadline(t *testing.T) {
	t.Cleanup(ipc.SetControlResponseDeadlineForTest(150 * time.Millisecond))
	sandboxControlFixture(t, startHeldOpenListener(t))

	done := captureStdout(t)
	start := time.Now()
	err := Stop("42", "")
	elapsed := time.Since(start)
	out := done()

	if err == nil {
		t.Fatal("Stop: want an error when the daemon never replies")
	}
	if !strings.Contains(err.Error(), "stop status unknown") {
		t.Errorf("err = %q, want %q", err.Error(), "stop status unknown")
	}
	if !strings.Contains(err.Error(), "cenci babysit stop 42") {
		t.Errorf("err = %q, want the host command", err.Error())
	}
	if strings.Contains(out+err.Error(), "Stopped babysitting") {
		t.Errorf("stdout %q / err %q must not claim the supervisor was stopped", out, err.Error())
	}
	if elapsed > 2*time.Second {
		t.Errorf("Stop took %v, want the shortened deadline to bound the wait", elapsed)
	}
}

// TestStop_SandboxDialFailureReportsNotStopped mirrors armOnHost's
// "not armed" branch: nothing was written, so the stop definitely did not
// happen.
func TestStop_SandboxDialFailureReportsNotStopped(t *testing.T) {
	sandboxControlFixture(t, tempArmSocket(t)) // nothing listens

	err := Stop("42", "")
	if err == nil {
		t.Fatal("Stop: want an error when no daemon is reachable")
	}
	if !strings.Contains(err.Error(), "not stopped: no cenci daemon reachable on the host event socket") {
		t.Errorf("err = %q", err.Error())
	}
	if !strings.Contains(err.Error(), "cenci babysit stop 42") {
		t.Errorf("err = %q, want the host command", err.Error())
	}
	if strings.Contains(err.Error(), "stop status unknown") {
		t.Errorf("err = %q: a dial failure is distinct from an unknown outcome", err.Error())
	}
}

// TestStatus_SandboxAckPrintsExactlyOneJSONLine covers P4's sandbox half.
func TestStatus_SandboxAckPrintsExactlyOneJSONLine(t *testing.T) {
	want := ipc.BabysitStatus{PR: 42, Repo: "o/r", Status: "needs-input", CIFixAttempts: 3, ConflictFixAttempts: 0, FixCap: 3, SupervisorLive: false}
	path := startControlReceiver(t, nil, statusWith(ipc.BabysitStatusResponse{OK: true, Status: &want}))
	sandboxControlFixture(t, path)

	done := captureStdout(t)
	err := Status("42", "")
	out := done()

	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout = %q, want exactly one newline-terminated line", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q is not one JSON object: %v", out, err)
	}
	for _, key := range []string{"pr", "repo", "status", "ciFixAttempts", "conflictFixAttempts", "fixCap", "supervisorLive"} {
		if _, ok := got[key]; !ok {
			t.Errorf("status JSON %q lacks key %q", out, key)
		}
	}
	if len(got) != 7 {
		t.Errorf("status JSON has %d keys, want exactly 7: %q", len(got), out)
	}
	if got["pr"] != float64(42) {
		t.Errorf("pr = %#v, want the integer 42", got["pr"])
	}
	if got["supervisorLive"] != false || got["status"] != "needs-input" || got["ciFixAttempts"] != float64(3) {
		t.Errorf("status JSON = %q, want the daemon's values", out)
	}
}

// TestStatus_SandboxStateDirIsAcceptedAndIgnored covers the forwarded half of
// the ignored --state-dir: the ack JSON is printed, the supplied directory is
// left empty, and the request carries only the validated PR and repo.
func TestStatus_SandboxStateDirIsAcceptedAndIgnored(t *testing.T) {
	want := ipc.BabysitStatus{PR: 42, Repo: "o/r", Status: "watching", FixCap: 3}
	var got ipc.BabysitStatusRequest
	path := startControlReceiver(t, nil, func(req ipc.BabysitStatusRequest) ipc.BabysitStatusResponse {
		got = req
		return ipc.BabysitStatusResponse{OK: true, Status: &want}
	})
	sandboxControlFixture(t, path)
	dir := t.TempDir()

	done := captureStdout(t)
	err := Status("#42", dir)
	out := done()

	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var decoded ipc.BabysitStatus
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("stdout %q is not the status JSON: %v", out, err)
	}
	if decoded != want {
		t.Errorf("status = %+v, want %+v", decoded, want)
	}
	if got != (ipc.BabysitStatusRequest{PR: "42", Repo: "o/r"}) {
		t.Errorf("status request = %+v, want only PR=42 Repo=o/r", got)
	}
	if strings.Contains(fmt.Sprintf("%+v", got), dir) {
		t.Errorf("status request %+v leaked the local state dir %q", got, dir)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("state dir has %d entries, want 0", len(entries))
	}
}

// TestStatus_SandboxFailuresPrintTheHostCommand covers P5's sandbox half for
// a nack, a missing reply, and an unreachable daemon.
func TestStatus_SandboxFailuresPrintTheHostCommand(t *testing.T) {
	const reason = "no supervisor found for PR #42"
	for _, tc := range []struct {
		name string
		path func(t *testing.T) string
		want string
	}{
		{"nack", func(t *testing.T) string {
			return startControlReceiver(t, nil, statusWith(ipc.BabysitStatusResponse{OK: false, Reason: reason}))
		}, "status unavailable: " + reason},
		{"no reply", startSilentArmListener, "status unavailable: no response from the host daemon"},
		{"dial failure", tempArmSocket, "status unavailable: no cenci daemon reachable on the host event socket"},
		{"ack without a status object", func(t *testing.T) string {
			return startControlReceiver(t, nil, statusWith(ipc.BabysitStatusResponse{OK: true}))
		}, "status unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxControlFixture(t, tc.path(t))

			done := captureStdout(t)
			err := Status("42", "")
			out := done()

			if err == nil {
				t.Fatal("Status: want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "cenci babysit status 42") {
				t.Errorf("err = %q, want the host command", err.Error())
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing on a failure", out)
			}
		})
	}
}

// TestStatus_HostReadsTheStateFile covers P4's host half and P5's no-state
// case: JSON from the state file, and "no supervisor found" when absent.
func TestStatus_HostReadsTheStateFile(t *testing.T) {
	t.Run("prints one JSON line", func(t *testing.T) {
		dir, path := hostStopFixture(t, "o/r", "42")
		stubProcessOwned(t, true)
		writeStateFile(t, path, State{PR: "42", Repo: "o/r", Status: "needs-input", FixAttempts: 3})

		done := captureStdout(t)
		err := Status("42", dir)
		out := done()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		const want = `{"pr":42,"repo":"o/r","status":"needs-input","ciFixAttempts":3,"conflictFixAttempts":0,"fixCap":3,"supervisorLive":false}` + "\n"
		if out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
		if strings.Contains(out, dir) {
			t.Errorf("stdout %q leaks the state directory", out)
		}
	})

	t.Run("no state is no supervisor found", func(t *testing.T) {
		dir, _ := hostStopFixture(t, "o/r", "42")
		err := Status("42", dir)
		if err == nil || err.Error() != "no supervisor found for PR #42" {
			t.Fatalf("Status err = %v, want exactly %q", err, "no supervisor found for PR #42")
		}
	})
}
