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

// armTestSocketPath isolates XDG_RUNTIME_DIR to a temp dir (so both this
// test process's ipc.NewEventReceiver and the child's own
// ipc.DefaultEventSocketPath() resolve to the identical socket path) and
// XDG_STATE_HOME to an empty temp dir (watch/docs/test-isolation.md), then
// returns the resolved event-socket path.
func armTestSocketPath(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
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
	socket := armTestSocketPath(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		_ = conn.Close()
	}()

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
