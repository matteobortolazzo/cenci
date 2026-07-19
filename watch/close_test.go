package main_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

// -- close subcommand (ticket #314) ------------------------------------------

// TestClose_UnreachableDaemon_ErrorsExit1_NoKill locks in the hard fail-safe:
// when the daemon socket cannot be reached, `close` exits 1 and never reports
// a closed window (i.e. it never reached a tmux call).
func TestClose_UnreachableDaemon_ErrorsExit1_NoKill(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nope.sock")

	cmd := exec.Command(binaryPath, "close", "42", "--socket", socket)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if strings.Contains(string(output), "closed ") {
		t.Errorf("output must not report any closed window when the daemon is unreachable, got:\n%s", output)
	}
}

func TestClose_MissingArg_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "close")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

func TestClose_TrailingUnexpectedArg_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "close", "42", "unexpected")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

// TestClose_DryRun_PrintsCloseAndSkipDecisions drives `close --dry-run`
// against a real ipc.Server broadcasting a crafted snapshot with one
// non-busy and one busy window sharing the same ticket-number prefix, and
// asserts the decision lines without ever requiring a real tmux binary
// (--dry-run never calls the killer).
func TestClose_DryRun_PrintsCloseAndSkipDecisions(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", Status: "done"},
			{Session: "sess-a", WindowIndex: "1", WindowName: "77-review", Status: "running"},
		},
	})
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "close", "77", "--dry-run", "--socket", socket)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("close --dry-run: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "would close 77-implement") {
		t.Errorf("output missing would-close line for done window, got:\n%s", output)
	}
	if !strings.Contains(string(output), "skip 77-review") {
		t.Errorf("output missing skip line for running window, got:\n%s", output)
	}
	if strings.Contains(string(output), "closed 77-implement") {
		t.Errorf("--dry-run must never print an actual 'closed' line, got:\n%s", output)
	}
	// #522: the skip line must communicate that the close will be retried
	// automatically at session end, not just "use --force to close now".
	if !strings.Contains(string(output), "will retry automatically") {
		t.Errorf("skip line should communicate automatic retry at session end, got:\n%s", output)
	}
}

// TestClose_NoMatches_EmptyStdoutExit0 locks in the quieted no-op output
// (#522): a target that matches zero windows must produce no stdout and
// exit 0 -- replacing the old `no matching windows for %q` line, which
// lazyboards surfaced as a spurious "warning" for a legitimate no-op (there
// is nothing actionable to report).
func TestClose_NoMatches_EmptyStdoutExit0(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "empty.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "99-other", Status: "done"},
		},
	})
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "close", "42", "--socket", socket)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("close with zero matches: unexpected error/exit code: %v\n%s", err, output)
	}
	if len(output) != 0 {
		t.Errorf("expected empty stdout for zero matches, got:\n%s", output)
	}
}
