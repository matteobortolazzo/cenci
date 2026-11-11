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
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
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

// -- #1391: host-only `babysit reset` ---------------------------------------

// TestBabysitResetRefusesInSandboxWithoutSocketTraffic covers P8: inside a
// sandbox the reset exits 1, says the supervisor runs on the host, names the
// host command, and never touches the event socket (a reset lifts the repair
// cap, so no socket kind carries it). A positive control proves the listener
// would have seen a connection.
func TestBabysitResetRefusesInSandboxWithoutSocketTraffic(t *testing.T) {
	socket := armTestSocketPath(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	unixLn := ln.(*net.UnixListener)

	stdout, stderr, code := runBabysitCLI(t, armChildEnv(t, writeArmTestGhShim(t)), "babysit", "reset", "42")

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "runs on the host") || !strings.Contains(stderr, "cenci babysit reset 42") {
		t.Errorf("stderr = %q, want it to say the supervisor runs on the host and name `cenci babysit reset 42`", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}

	_ = unixLn.SetDeadline(time.Now().Add(300 * time.Millisecond))
	conn, err := unixLn.Accept()
	if err == nil {
		_ = conn.Close()
		t.Fatal("the reset opened a connection to the event socket")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Accept err = %v, want a timeout (no connection)", err)
	}

	go func() {
		if c, dialErr := net.Dial("unix", socket); dialErr == nil {
			_ = c.Close()
		}
	}()
	_ = unixLn.SetDeadline(time.Now().Add(5 * time.Second))
	control, err := unixLn.Accept()
	if err != nil {
		t.Fatalf("control: a dial was not observed by the listener: %v", err)
	}
	_ = control.Close()
}

// hostResetStatePath returns the host state-file path for owner/repo PR pr
// under the isolated XDG_STATE_HOME that armTestSocketPath set.
func hostResetStatePath(t *testing.T, pr string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "cenci", "babysit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("owner/repo"))
	return filepath.Join(dir, hex.EncodeToString(sum[:6])+"-"+pr+".json")
}

// TestBabysitResetHostModeExitCodes covers the host-side refusals through the
// real binary: P13 (no state), P14 (undecodable state), P17 (lock without a
// pid) and P19 (refused agent, exit 2). Every refusal leaves the files alone.
func TestBabysitResetHostModeExitCodes(t *testing.T) {
	armTestSocketPath(t) // isolates CENCI_SOCKET_DIR and XDG_STATE_HOME

	t.Run("no state", func(t *testing.T) {
		_, stderr, code := runBabysitCLI(t, hostChildEnv(t), "babysit", "reset", "43")

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "no supervisor found for PR #43") {
			t.Errorf("stderr = %q, want %q", stderr, "no supervisor found for PR #43")
		}
	})

	t.Run("undecodable state", func(t *testing.T) {
		path := hostResetStatePath(t, "44")
		const garbage = "{not json"
		if err := os.WriteFile(path, []byte(garbage), 0o600); err != nil {
			t.Fatal(err)
		}

		_, stderr, code := runBabysitCLI(t, hostChildEnv(t), "babysit", "reset", "44")

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "decode babysit state") {
			t.Errorf("stderr = %q, want the strict decode error", stderr)
		}
		if got, _ := os.ReadFile(path); string(got) != garbage {
			t.Errorf("state file = %q, want it unchanged", got)
		}
	})

	t.Run("lock without a pid", func(t *testing.T) {
		path := hostResetStatePath(t, "45")
		body := `{"schemaVersion":2,"pr":"45","repo":"owner/repo","agent":"claude","status":"needs-input","ciFixAttempts":3}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".lock", []byte("99999\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, stderr, code := runBabysitCLI(t, hostChildEnv(t), "babysit", "reset", "45")

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "supervisor already running for PR #45") {
			t.Errorf("stderr = %q, want the already-running refusal", stderr)
		}
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Errorf("state file = %q, want it unchanged", got)
		}
		if _, err := os.Stat(path + ".lock"); err != nil {
			t.Errorf("lock was removed: %v", err)
		}
	})

	t.Run("refused agent exits 2 before any change", func(t *testing.T) {
		path := hostResetStatePath(t, "46")
		body := `{"schemaVersion":2,"pr":"46","repo":"owner/repo","agent":"opencode","status":"needs-input","ciFixAttempts":3}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		_, stderr, code := runBabysitCLI(t, hostChildEnv(t), "babysit", "reset", "46")

		if code != 2 {
			t.Errorf("exit code = %d, want 2 (stderr %q)", code, stderr)
		}
		if reason := sandbox.AgentDisabledReason("opencode"); !strings.Contains(stderr, reason) {
			t.Errorf("stderr = %q, want the refusal %q", stderr, reason)
		}
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Errorf("state file = %q, want it unchanged", got)
		}
	})
}

// TestBabysitResetReArmFailureExitsOne covers Q7 through the real binary: the
// budget is zeroed and saved, then the re-arm fails (the supervisor log path
// is a directory, so it cannot be opened and no supervisor is spawned). The
// command exits 1 with the "budget reset ... but not re-armed" message naming
// the manual re-arm, and the zeroed budget stays on disk.
func TestBabysitResetReArmFailureExitsOne(t *testing.T) {
	armTestSocketPath(t) // isolates CENCI_SOCKET_DIR and XDG_STATE_HOME
	path := hostResetStatePath(t, "47")
	body := `{"schemaVersion":2,"pr":"47","repo":"owner/repo","agent":"claude","intervalSeconds":300,"status":"needs-input","ciFixAttempts":3,"conflictFixAttempts":3}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := strings.TrimSuffix(path, ".json") + ".log"
	if err := os.Mkdir(logPath, 0o700); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runBabysitCLI(t, hostChildEnv(t), "babysit", "reset", "47")

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (stderr %q)", code, stderr)
	}
	for _, want := range []string{"budget reset for PR #47 but not re-armed:", "re-arm with cenci babysit 47 --agent claude"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
	if !strings.Contains(stdout, "Reset the retry budget for PR #47.") {
		t.Errorf("stdout = %q, want the reset line (the budget was reset before the re-arm failed)", stdout)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		PR                  string `json:"pr"`
		Repo                string `json:"repo"`
		CIFixAttempts       int    `json:"ciFixAttempts"`
		ConflictFixAttempts int    `json:"conflictFixAttempts"`
	}
	if err := json.Unmarshal(saved, &got); err != nil {
		t.Fatalf("state = %s, want valid JSON: %v", saved, err)
	}
	if got.PR != "47" || got.Repo != "owner/repo" {
		t.Errorf("state identity = pr %q repo %q, want pr \"47\" repo \"owner/repo\" (state %s)", got.PR, got.Repo, saved)
	}
	if got.CIFixAttempts != 0 || got.ConflictFixAttempts != 0 {
		t.Errorf("state budget = ci %d conflict %d, want both left zeroed (state %s)", got.CIFixAttempts, got.ConflictFixAttempts, saved)
	}
}

func TestBabysitResetUsageErrorsExitTwo(t *testing.T) {
	armTestSocketPath(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no pr", []string{"babysit", "reset"}},
		{"extra argument", []string{"babysit", "reset", "42", "extra"}},
		{"non-numeric pr", []string{"babysit", "reset", "abc"}},
		{"non-positive pr", []string{"babysit", "reset", "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runBabysitCLI(t, hostChildEnv(t), tc.args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, "cenci babysit reset") {
				t.Errorf("stderr = %q, want a usage line naming the command", stderr)
			}
		})
	}
}

// -- #1472: host-only, attended-only `babysit resolve` ------------------------

// resolveChildEnv is the host-mode child with an attended session: the ambient
// CENCI_ATTENDED (the dev sandbox sets it to 0) and its origin marker are cleared
// (watch/docs/test-isolation.md), so each case sets the gates it exercises.
func resolveChildEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	return append(hostChildEnv(t), append([]string{"CENCI_ATTENDED=", "CENCI_ATTENDED_ORIGIN="}, extra...)...)
}

// resolveStateBody is a stopped supervisor state for owner/repo PR pr holding
// two pending discussion keys and one addressed key.
func resolveStateBody(pr string) string {
	rev := strings.Repeat("b", 64)
	return `{"schemaVersion":2,"pr":"` + pr + `","repo":"owner/repo","status":"stopped",` +
		`"addressedCommentKeys":["discussion:9"],"pendingCommentKeys":["comment:2","discussion:11","discussion:12"],` +
		`"launchedFeedbackKeys":["discussion:11"],"discussionRevisions":{"discussion:11":"` + rev + `","discussion:12":"` + rev + `"}}`
}

// TestBabysitResolveExitCodes covers AC 80 through the real binary: exit 0 on a
// host with an attended environment (the resolution recorded as a human-cli
// receipt; a stopped state is not re-armed), exit 1 for every refusal with the
// state file byte-identical, and exit 2 for usage errors.
func TestBabysitResolveExitCodes(t *testing.T) {
	armTestSocketPath(t) // isolates CENCI_SOCKET_DIR and XDG_STATE_HOME

	t.Run("a pending discussion key on an attended host exits 0", func(t *testing.T) {
		path := hostResetStatePath(t, "50")
		if err := os.WriteFile(path, []byte(resolveStateBody("50")), 0o600); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, code := runBabysitCLI(t, resolveChildEnv(t), "babysit", "resolve", "50", "discussion:11")

		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
		}
		if !strings.Contains(stdout, "Resolved discussion:11 for PR #50") {
			t.Errorf("stdout = %q, want the resolved line", stdout)
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Status   string                    `json:"status"`
			Pending  []string                  `json:"pendingCommentKeys"`
			Resolved []string                  `json:"addressedCommentKeys"`
			Launched []string                  `json:"launchedFeedbackKeys"`
			Receipts map[string]map[string]any `json:"discussionReceipts"`
		}
		if err := json.Unmarshal(saved, &got); err != nil {
			t.Fatalf("state = %s, want valid JSON: %v", saved, err)
		}
		if got.Status != "stopped" {
			t.Errorf("status = %q, want the stopped state kept (not re-armed)", got.Status)
		}
		if strings.Join(got.Pending, ",") != "comment:2,discussion:12" || strings.Join(got.Resolved, ",") != "discussion:9,discussion:11" || len(got.Launched) != 0 {
			t.Errorf("pending %v addressed %v launched %v, want discussion:11 moved to addressed", got.Pending, got.Resolved, got.Launched)
		}
		if src := got.Receipts["discussion:11"]["source"]; src != "human-cli" {
			t.Errorf("receipt source = %v, want human-cli (state %s)", src, saved)
		}
	})

	refusals := []struct {
		name   string
		pr     string
		key    string
		env    []string
		stderr string
	}{
		{"CENCI_SANDBOX=1 exits 1", "51", "discussion:11", []string{"CENCI_SANDBOX=1"}, "runs on the host"},
		{"CENCI_ATTENDED=0 exits 1", "52", "discussion:11", []string{"CENCI_ATTENDED=0"}, "CENCI_ATTENDED=0"},
		{"CENCI_ATTENDED=0 with the host-config origin still exits 1", "53", "discussion:11", []string{"CENCI_ATTENDED=0", "CENCI_ATTENDED_ORIGIN=host-config"}, "CENCI_ATTENDED=0"},
		{"a babysit supervisor environment exits 1", "54", "discussion:11", []string{"CENCI_BABYSIT_SUPERVISOR=1"}, "inside a babysit supervisor"},
		{"a comment key exits 1 naming GitHub", "55", "comment:2", nil, "resolve it on GitHub"},
		{"a review key exits 1 naming GitHub", "56", "review:7", nil, "resolve it on GitHub"},
		{"an addressed key exits 1", "57", "discussion:9", nil, "is not pending"},
		{"an unknown key exits 1", "58", "discussion:99", nil, "is not pending"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			path := hostResetStatePath(t, tc.pr)
			body := resolveStateBody(tc.pr)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			stdout, stderr, code := runBabysitCLI(t, resolveChildEnv(t, tc.env...), "babysit", "resolve", tc.pr, tc.key)

			if code != 1 {
				t.Errorf("exit code = %d, want 1 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, tc.stderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if got, _ := os.ReadFile(path); string(got) != body {
				t.Errorf("state file = %q, want it byte-identical", got)
			}
		})
	}

	t.Run("an unreadable state exits 1 naming the path and keeps the file", func(t *testing.T) {
		path := hostResetStatePath(t, "59")
		const garbage = `{"schemaVersion":2,"pr":"59","pendingCommentKeys":["discussion:11"`
		if err := os.WriteFile(path, []byte(garbage), 0o600); err != nil {
			t.Fatal(err)
		}

		_, stderr, code := runBabysitCLI(t, resolveChildEnv(t), "babysit", "resolve", "59", "discussion:11")

		if code != 1 {
			t.Errorf("exit code = %d, want 1 (stderr %q)", code, stderr)
		}
		if !strings.Contains(stderr, "babysit state unreadable: "+path+": ") {
			t.Errorf("stderr = %q, want the unreadable reason naming %s", stderr, path)
		}
		if got, _ := os.ReadFile(path); string(got) != garbage {
			t.Errorf("state file = %q, want it byte-identical", got)
		}
	})

	t.Run("no state exits 1", func(t *testing.T) {
		_, stderr, code := runBabysitCLI(t, resolveChildEnv(t), "babysit", "resolve", "60", "discussion:11")

		if code != 1 || !strings.Contains(stderr, "no supervisor found for PR #60") {
			t.Errorf("exit code = %d stderr = %q, want 1 and the no-supervisor message", code, stderr)
		}
	})

	t.Run("a refused agent exits 2 before any change", func(t *testing.T) {
		path := hostResetStatePath(t, "61")
		body := `{"schemaVersion":2,"pr":"61","repo":"owner/repo","agent":"opencode","status":"needs-input","pendingCommentKeys":["discussion:11"],"discussionRevisions":{"discussion:11":"` + strings.Repeat("b", 64) + `"}}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		_, stderr, code := runBabysitCLI(t, resolveChildEnv(t), "babysit", "resolve", "61", "discussion:11")

		if code != 2 {
			t.Errorf("exit code = %d, want 2 (stderr %q)", code, stderr)
		}
		if reason := sandbox.AgentDisabledReason("opencode"); !strings.Contains(stderr, reason) {
			t.Errorf("stderr = %q, want the refusal %q", stderr, reason)
		}
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Errorf("state file = %q, want it byte-identical", got)
		}
	})
}

func TestBabysitResolveUsageErrorsExitTwo(t *testing.T) {
	armTestSocketPath(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no pr", []string{"babysit", "resolve"}},
		{"no key", []string{"babysit", "resolve", "42"}},
		{"extra argument", []string{"babysit", "resolve", "42", "discussion:11", "extra"}},
		{"non-numeric pr", []string{"babysit", "resolve", "abc", "discussion:11"}},
		{"non-positive pr", []string{"babysit", "resolve", "0", "discussion:11"}},
		{"malformed key", []string{"babysit", "resolve", "42", "foo"}},
		{"discussion key without an id", []string{"babysit", "resolve", "42", "discussion:"}},
		{"discussion key with a non-numeric id", []string{"babysit", "resolve", "42", "discussion:abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runBabysitCLI(t, resolveChildEnv(t), tc.args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, "cenci babysit resolve") {
				t.Errorf("stderr = %q, want a usage line naming the command", stderr)
			}
		})
	}
}

// TestBabysitArmRefusesAnUnreadableStateAtArm covers AC 91 through the real
// binary: an arm over an undecodable state file exits 1 naming the reason and the
// path, leaves the file byte-identical and starts no supervisor (no lock, no log).
func TestBabysitArmRefusesAnUnreadableStateAtArm(t *testing.T) {
	armTestSocketPath(t) // isolates CENCI_SOCKET_DIR and XDG_STATE_HOME
	path := hostResetStatePath(t, "62")
	const garbage = `{"schemaVersion":2,"pr":"62","repo":"owner/repo","pendingCommentKeys":["discussion:11"`
	if err := os.WriteFile(path, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runBabysitCLI(t, hostChildEnv(t), "babysit", "62", "--agent", "claude")

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (stdout %q stderr %q)", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "babysit state unreadable") || !strings.Contains(stderr, path) {
		t.Errorf("stderr = %q, want the unreadable reason naming %s", stderr, path)
	}
	if got, _ := os.ReadFile(path); string(got) != garbage {
		t.Errorf("state file = %q, want it byte-identical", got)
	}
	for _, leftover := range []string{path + ".lock", strings.TrimSuffix(path, ".json") + ".log"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Errorf("%s exists (stat err = %v), want no supervisor started", leftover, err)
		}
	}
}
