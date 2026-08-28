package launcher

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/errcode"
)

// -- Engine.Verify -------------------------------------------------------
//
// `cenci diagnose --name <session> --verify` re-runs the read-only diagnostic
// probes behind the recovery commands diagnose surfaces and prints a
// pass/fail line per check, so an operator can confirm a suggested recovery
// command actually worked. It reuses daemonStatusFinding/
// containerStartupState — the same probes Diagnose already calls — never a
// new framework, and never launches or attaches (read-only contract). These
// tests reuse the Engine{Runtime: "cenci-test-runtime-does-not-exist..."}
// fake pattern from diagnose_test.go in this same package.

// verifyScope is a minimal Scope shared by the Verify tests below; its
// exact values don't matter for the daemon-reachability check, only that
// ComputeScope-shaped fields are populated.
var verifyScope = Scope{
	ContainerName: "claude-cenci-test",
	VolumeName:    "claude-cenci-home-test",
	Image:         "cenci-sandbox:test",
}

// TestVerify_DaemonSocketStillMissing_ReportsFailWithCode pins case (a) from
// the ticket: the daemon's event socket still doesn't exist (recovery
// command was never run, or didn't work) -> Verify must report a "[fail]"
// line carrying the DaemonSocketMissing code, distinguishing this failure
// class from any other verifiable check (the #446 content-specific-marker
// rule).
func TestVerify_DaemonSocketStillMissing_ReportsFailWithCode(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // empty runtime dir -> no socket file at all

	var stdout, stderr bytes.Buffer
	e := &Engine{
		Runtime: "cenci-test-runtime-does-not-exist-574",
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	if err := e.Verify(verifyScope); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "[fail]") {
		t.Errorf("expected a [fail] line for the still-missing daemon socket, got:\n%s", out)
	}
	if !strings.Contains(out, string(errcode.DaemonSocketMissing)) {
		t.Errorf("expected the %s code in the verify output, got:\n%s", errcode.DaemonSocketMissing, out)
	}
	if strings.Contains(out, "[pass]") {
		t.Errorf("did not expect a [pass] line for the daemon check when the socket is missing, got:\n%s", out)
	}
}

// TestVerify_DaemonReachable_ReportsPass pins case (b): once the socket is
// reachable (the recovery command worked), Verify must report a "[pass]"
// line for the daemon check and must not report DaemonSocketMissing or
// DaemonConnUnreachable — the same two codes it fails on above.
func TestVerify_DaemonReachable_ReportsPass(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	socketDir := filepath.Join(xdg, "cenci")
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	l, err := net.Listen("unix", filepath.Join(socketDir, "cenci-events.sock"))
	if err != nil {
		t.Fatalf("listen events socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	var stdout, stderr bytes.Buffer
	e := &Engine{
		Runtime: "cenci-test-runtime-does-not-exist-574",
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	if err := e.Verify(verifyScope); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "[pass]") {
		t.Errorf("expected a [pass] line for the reachable daemon, got:\n%s", out)
	}
	if strings.Contains(out, string(errcode.DaemonSocketMissing)) {
		t.Errorf("did not expect %s when the daemon is reachable, got:\n%s", errcode.DaemonSocketMissing, out)
	}
	if strings.Contains(out, string(errcode.DaemonConnUnreachable)) {
		t.Errorf("did not expect %s when the daemon is reachable, got:\n%s", errcode.DaemonConnUnreachable, out)
	}
}

// TestVerify_ContainerExistenceInconclusive_ReportsSkip pins the #574 fix:
// when the container-existence probe can't conclusively answer "found" or
// "not found" (here, because Engine.Runtime points at a binary that doesn't
// exist at all, so containerStartupState fails with something other than an
// *exec.ExitError), Verify must print an explicit "[skip]" line carrying the
// underlying reason rather than silently omitting the check — matching
// Diagnose's own "Status: unknown (runtime unreachable)" reporting for this
// same inconclusive case.
func TestVerify_ContainerExistenceInconclusive_ReportsSkip(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg) // no daemon socket -> daemon check fails, unrelated to this assertion

	var stdout, stderr bytes.Buffer
	e := &Engine{
		Runtime: "cenci-test-runtime-does-not-exist-574",
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	if err := e.Verify(verifyScope); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "[skip] container existence:") {
		t.Errorf("expected a [skip] line for the inconclusive container-existence check, got:\n%s", out)
	}
	if !strings.Contains(out, "runtime unreachable") {
		t.Errorf("expected the [skip] line to explain the runtime was unreachable, got:\n%s", out)
	}
	if strings.Contains(out, string(errcode.SandboxSessionNotFound)) {
		t.Errorf("did not expect %s to be attached to an inconclusive check, got:\n%s", errcode.SandboxSessionNotFound, out)
	}
}

// Read-only/no-launch-or-attach-side-effects coverage lives at the CLI
// level (watch/diagnose_cmd_test.go), which drives the real built binary
// against a scripted fake runtime and asserts directly on its call log —
// the strongest available evidence that no "run"/interactive-attach
// invocation ever happened, matching the ticket's "assert on the fake
// runtime that no launch/attach calls were made."
