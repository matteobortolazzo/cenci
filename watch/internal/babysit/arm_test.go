package babysit

// Ticket #1094: the in-container client side of the babysit-arm forward --
// armOnHost's outcome mapping (ack/nack/no-response -> armed/not armed/arm
// status unknown), Run/Stop's CENCI_SANDBOX forward gates. armSocketPath and
// armOnHost don't exist yet; this file (and every file in this package,
// since it's all one build) is expected to fail to compile until Phase 4
// adds arm.go and wires babysit.go's Run/Stop gates.

import (
	"context"
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

// -- AC5: Stop's container branch --------------------------------------------

// TestStop_UnderSandboxReportsHostSupervisionNotNoSupervisorFound covers
// AC5: `cenci babysit stop` inside the container must report that the
// supervisor runs on the host and exit non-zero, instead of the local
// "no supervisor found for PR #N" message -- and the CENCI_SANDBOX gate must
// run before stateDir/repository, since no state directory or gh call is
// stubbed here.
func TestStop_UnderSandboxReportsHostSupervisionNotNoSupervisorFound(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "1")
	dir := t.TempDir()

	err := Stop("42", dir)
	if err == nil {
		t.Fatal("Stop: want a non-nil error when running inside the sandbox")
	}
	if strings.Contains(err.Error(), "no supervisor found") {
		t.Errorf("Stop err = %q, want the host-supervision message, not the local \"no supervisor found\" message (AC5)", err.Error())
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("Stop err = %q, want it to report that the supervisor runs on the host", err.Error())
	}
}
