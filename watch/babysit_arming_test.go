package main_test

// Ticket #1094 AC7: an integration test spawning the real binary, asserting
// the arm-request wire format parses into the daemon's typed ipc.ArmRequest
// and the response line parses back into the client's own outcome, per
// watch/docs/hook-events.md's "test JSON parsing via the real entry point"
// rule. Depends on ipc.ArmRequest/ArmResponse/NewEventReceiver.SetArmHandler
// and babysit's CENCI_SANDBOX forward gate, none of which exist yet; this
// file is expected to fail to compile until Phase 4 lands.
//
// Named "babysit_arming_test.go" rather than the plan's literal
// "babysit_arm_test.go": Go's filename-based build-constraint rules treat a
// trailing "_arm" (after stripping "_test") as an implicit GOARCH=arm
// constraint, which silently excludes the file from every non-arm build
// (confirmed via `go list -json .`'s IgnoredGoFiles on this amd64 dev
// machine). Renamed to avoid the collision; contents/package/purpose are
// otherwise exactly what the plan specifies for this file.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// writeArmTestGhShim writes a `gh` PATH shim that unconditionally answers
// any invocation (in particular `gh repo view --json nameWithOwner --jq
// .nameWithOwner`, the only gh call babysit's arming path makes before
// forwarding) with "owner/repo" -- mirroring
// internal/dispatch/chainfake_test.go's installChainGH PATH-shim pattern.
// Returns the shim directory, to prepend (never replace) PATH.
func writeArmTestGhShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho owner/repo\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// armChildEnv builds the child `cenci babysit` process's environment: the gh
// shim prepended to PATH, the six gh-credential env vars neutralized
// (watch/docs/test-isolation.md), CENCI_SANDBOX=1 (the forward-branch gate),
// and a fixed TMUX_PANE so the arm request's TmuxPane field is
// deterministic.
func armChildEnv(t *testing.T, ghShimDir string) []string {
	t.Helper()
	return append(os.Environ(),
		"PATH="+ghShimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_TOKEN=",
		"GITHUB_TOKEN=",
		"GH_CONFIG_DIR="+t.TempDir(),
		"GH_ENTERPRISE_TOKEN=",
		"GITHUB_ENTERPRISE_TOKEN=",
		"GH_HOST=",
		"CENCI_SANDBOX=1",
		"CENCI_BABYSIT_SUPERVISOR=",
		"TMUX_PANE=%3",
	)
}

// armTestSocketPath isolates CENCI_SOCKET_DIR to a temp dir (so both this
// test process's ipc.NewEventReceiver and the child's own
// ipc.DefaultEventSocketPath() resolve to the identical socket path) and
// XDG_STATE_HOME to an empty temp dir (watch/docs/test-isolation.md), then
// returns the resolved event-socket path.
func armTestSocketPath(t *testing.T) string {
	t.Helper()
	t.Setenv("CENCI_SOCKET_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return ipc.DefaultEventSocketPath()
}

// TestBabysitArmWireFormat_Ack covers AC7's ack sub-case: the real `cenci
// babysit <pr> --agent <agent>` binary, run with CENCI_SANDBOX=1, sends an
// arm request that parses into the daemon's typed ipc.ArmRequest, and the
// ack response line parses back into the child's own success outcome (exit
// 0, stdout reporting the supervisor runs on the host).
func TestBabysitArmWireFormat_Ack(t *testing.T) {
	socket := armTestSocketPath(t)
	recv, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = recv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var got ipc.ArmRequest
	var calls int
	recv.SetArmHandler(func(req ipc.ArmRequest) ipc.ArmResponse {
		mu.Lock()
		got = req
		calls++
		mu.Unlock()
		return ipc.ArmResponse{OK: true}
	})
	go recv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	ghDir := writeArmTestGhShim(t)
	cmd := exec.Command(binaryPath, "babysit", "42", "--agent", "claude")
	cmd.Env = armChildEnv(t, ghDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci babysit (ack): %v: %s", err, output)
	}
	if !strings.Contains(string(output), "runs on the host") {
		t.Errorf("stdout = %q, want a message that the supervisor runs on the host", output)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("arm handler invocations = %d, want exactly 1", calls)
	}
	if got.PR != "42" || got.Repo != "owner/repo" || got.Agent != "claude" || got.TmuxPane != "%3" {
		t.Errorf("parsed arm request = %+v, want PR=42 Repo=owner/repo Agent=claude TmuxPane=%%3", got)
	}
	if got.Interval != 15*time.Minute {
		t.Errorf("parsed arm request Interval = %v, want the default 15m0s", got.Interval)
	}
}

// TestBabysitArmWireFormat_Nack covers AC7's nack sub-case: the daemon's
// response line parses back into a non-zero exit with the reason relayed
// verbatim to stderr, even though the reason is one this client binary never
// itself defines (the "relay, never re-derive" contract).
func TestBabysitArmWireFormat_Nack(t *testing.T) {
	socket := armTestSocketPath(t)
	recv, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = recv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const reason = "a reason ticket 2 defines, not this client binary"
	recv.SetArmHandler(func(ipc.ArmRequest) ipc.ArmResponse {
		return ipc.ArmResponse{OK: false, Reason: reason}
	})
	go recv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	ghDir := writeArmTestGhShim(t)
	cmd := exec.Command(binaryPath, "babysit", "42", "--agent", "claude")
	cmd.Env = armChildEnv(t, ghDir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError on a nack, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(string(output), reason) {
		t.Errorf("output = %q, want the daemon's reason %q relayed verbatim", output, reason)
	}
}

// TestBabysitArmWireFormat_NoResponse covers AC7's third sub-case (AC4): a
// raw listener that accepts the connection, reads the arm-request line, and
// closes without ever writing a response -- exactly the pre-#1094 daemon's
// observable behavior for an unrecognized "kind" (drop to Events(), close).
// The client must report the third outcome, "arm status unknown", textually
// distinct from a nack, and exit non-zero.
func TestBabysitArmWireFormat_NoResponse(t *testing.T) {
	startSilentListener(t, armTestSocketPath(t))

	ghDir := writeArmTestGhShim(t)
	cmd := exec.Command(binaryPath, "babysit", "42", "--agent", "claude")
	cmd.Env = armChildEnv(t, ghDir)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError when the daemon never responds, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(string(output), "arm status unknown") {
		t.Errorf("output = %q, want the \"arm status unknown\" outcome", output)
	}
}

// -- #1390: forwarded `babysit stop` / `babysit status` ----------------------

// runBabysitCLI runs the real binary with env and returns its separate stdout and
// stderr plus the exit code (-1 when the process did not exit normally).
func runBabysitCLI(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = env
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	switch e := err.(type) {
	case nil:
		code = 0
	case *exec.ExitError:
		code = e.ExitCode()
	default:
		t.Fatalf("run cenci %v: %v", args, err)
	}
	return outBuf.String(), errBuf.String(), code
}

// startSilentListener accepts one connection, reads the request line and
// closes without replying -- the observable behavior of a daemon that
// predates the kind.
func startSilentListener(t *testing.T, socket string) {
	t.Helper()
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
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
}

func startControlReceiver(t *testing.T, socket string, stop func(ipc.BabysitStopRequest) ipc.BabysitStopResponse, status func(ipc.BabysitStatusRequest) ipc.BabysitStatusResponse) {
	t.Helper()
	recv, err := ipc.NewEventReceiver(socket)
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
	time.Sleep(20 * time.Millisecond)
}

func TestBabysitStopWireFormat(t *testing.T) {
	t.Run("ack", func(t *testing.T) {
		socket := armTestSocketPath(t)
		var mu sync.Mutex
		var got ipc.BabysitStopRequest
		var calls int
		startControlReceiver(t, socket, func(req ipc.BabysitStopRequest) ipc.BabysitStopResponse {
			mu.Lock()
			defer mu.Unlock()
			got, calls = req, calls+1
			return ipc.BabysitStopResponse{OK: true}
		}, nil)

		stdout, stderr, code := runBabysitCLI(t, armChildEnv(t, writeArmTestGhShim(t)), "babysit", "stop", "42")

		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
		}
		if stdout != "Stopped babysitting PR #42 on the host.\n" {
			t.Errorf("stdout = %q, want %q", stdout, "Stopped babysitting PR #42 on the host.\n")
		}
		mu.Lock()
		defer mu.Unlock()
		if calls != 1 || got.PR != "42" || got.Repo != "owner/repo" {
			t.Errorf("handler calls=%d request=%+v, want one request for PR 42 owner/repo", calls, got)
		}
	})

	t.Run("nack relays the reason with the host command", func(t *testing.T) {
		socket := armTestSocketPath(t)
		const reason = "no supervisor found for PR #42"
		startControlReceiver(t, socket, func(ipc.BabysitStopRequest) ipc.BabysitStopResponse {
			return ipc.BabysitStopResponse{OK: false, Reason: reason}
		}, nil)

		stdout, stderr, code := runBabysitCLI(t, armChildEnv(t, writeArmTestGhShim(t)), "babysit", "stop", "42")

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "not stopped: "+reason) || !strings.Contains(stderr, "cenci babysit stop 42") {
			t.Errorf("stderr = %q, want %q and the host command", stderr, "not stopped: "+reason)
		}
		if strings.Contains(stdout+stderr, "Stopped babysitting") {
			t.Errorf("output %q / %q must not claim a stop", stdout, stderr)
		}
	})

	t.Run("no reply reports stop status unknown", func(t *testing.T) {
		socket := armTestSocketPath(t)
		startSilentListener(t, socket)

		stdout, stderr, code := runBabysitCLI(t, armChildEnv(t, writeArmTestGhShim(t)), "babysit", "stop", "42")

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "stop status unknown") || !strings.Contains(stderr, "cenci babysit stop 42") {
			t.Errorf("stderr = %q, want %q and the host command", stderr, "stop status unknown")
		}
		if strings.Contains(stdout, "Stopped babysitting") {
			t.Errorf("stdout = %q must not claim a stop", stdout)
		}
	})
}

func TestBabysitStatusWireFormat(t *testing.T) {
	t.Run("ack prints one JSON line", func(t *testing.T) {
		socket := armTestSocketPath(t)
		var mu sync.Mutex
		var got ipc.BabysitStatusRequest
		startControlReceiver(t, socket, nil, func(req ipc.BabysitStatusRequest) ipc.BabysitStatusResponse {
			mu.Lock()
			defer mu.Unlock()
			got = req
			return ipc.BabysitStatusResponse{OK: true, Status: &ipc.BabysitStatus{PR: 42, Repo: "owner/repo", Status: "needs-input", CIFixAttempts: 3, FixCap: 3}}
		})

		stdout, stderr, code := runBabysitCLI(t, armChildEnv(t, writeArmTestGhShim(t)), "babysit", "status", "42")

		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
		}
		const want = `{"pr":42,"repo":"owner/repo","status":"needs-input","ciFixAttempts":3,"conflictFixAttempts":0,"fixCap":3,"supervisorLive":false}` + "\n"
		if stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		mu.Lock()
		defer mu.Unlock()
		if got.PR != "42" || got.Repo != "owner/repo" {
			t.Errorf("handler saw %+v, want PR 42 owner/repo", got)
		}
	})

	t.Run("nack prints the host command and exits 1", func(t *testing.T) {
		socket := armTestSocketPath(t)
		startControlReceiver(t, socket, nil, func(ipc.BabysitStatusRequest) ipc.BabysitStatusResponse {
			return ipc.BabysitStatusResponse{OK: false, Reason: "no supervisor found for PR #42"}
		})

		stdout, stderr, code := runBabysitCLI(t, armChildEnv(t, writeArmTestGhShim(t)), "babysit", "status", "42")

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "no supervisor found for PR #42") || !strings.Contains(stderr, "cenci babysit status 42") {
			t.Errorf("stderr = %q, want the reason and the host command", stderr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want nothing", stdout)
		}
	})

	t.Run("no reply prints the host command and exits 1", func(t *testing.T) {
		socket := armTestSocketPath(t)
		startSilentListener(t, socket)

		_, stderr, code := runBabysitCLI(t, armChildEnv(t, writeArmTestGhShim(t)), "babysit", "status", "42")

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "cenci babysit status 42") {
			t.Errorf("stderr = %q, want the host command", stderr)
		}
	})
}

// hostChildEnv is armChildEnv without the sandbox gate: the host-mode child.
func hostChildEnv(t *testing.T) []string {
	t.Helper()
	return append(armChildEnv(t, writeArmTestGhShim(t)), "CENCI_SANDBOX=")
}

// TestBabysitStatusHostMode covers P4 (state file to one JSON line, no path)
// and P5 (no state exits 1) on the host.
func TestBabysitStatusHostMode(t *testing.T) {
	armTestSocketPath(t) // isolates CENCI_SOCKET_DIR and XDG_STATE_HOME
	stateHome := os.Getenv("XDG_STATE_HOME")
	sum := sha256.Sum256([]byte("owner/repo"))
	stateDir := filepath.Join(stateHome, "cenci", "babysit")

	t.Run("prints the state as one JSON line without a path", func(t *testing.T) {
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(stateDir, hex.EncodeToString(sum[:6])+"-42.json")
		body := `{"schemaVersion":2,"pr":"42","repo":"owner/repo","status":"needs-input","ciFixAttempts":2,"conflictFixAttempts":1}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, code := runBabysitCLI(t, hostChildEnv(t), "babysit", "status", "42")

		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
		}
		const want = `{"pr":42,"repo":"owner/repo","status":"needs-input","ciFixAttempts":2,"conflictFixAttempts":1,"fixCap":3,"supervisorLive":false}` + "\n"
		if stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		if strings.Contains(stdout, stateHome) {
			t.Errorf("stdout %q contains the state home path", stdout)
		}
	})

	t.Run("no state exits 1", func(t *testing.T) {
		_, stderr, code := runBabysitCLI(t, hostChildEnv(t), "babysit", "status", "43")

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "no supervisor found for PR #43") {
			t.Errorf("stderr = %q, want %q", stderr, "no supervisor found for PR #43")
		}
	})
}

func TestBabysitStatusUsageErrorsExitTwo(t *testing.T) {
	armTestSocketPath(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no pr", []string{"babysit", "status"}},
		{"extra argument", []string{"babysit", "status", "42", "extra"}},
		{"non-numeric pr", []string{"babysit", "status", "abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runBabysitCLI(t, hostChildEnv(t), tc.args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, "cenci babysit status") {
				t.Errorf("stderr = %q, want a usage line naming the command", stderr)
			}
		})
	}
}
