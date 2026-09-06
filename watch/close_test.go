package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// -- close subcommand (ticket #314) ------------------------------------------

// emptyStateHome returns an environment whose XDG_STATE_HOME points at an
// empty temp directory. `cenci close` consults `cenci babysit`'s state files
// there before killing a window (#787), so any test asserting a close/skip
// decision must isolate itself from a real supervisor that happens to be
// running on the dev machine — same discipline as the ambient daemon-socket
// isolation in watch/docs/test-isolation.md.
func emptyStateHome(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(), "XDG_STATE_HOME="+t.TempDir())
}

// startCloseIPCServer starts a real ipc.Server on a temp socket and
// broadcasts snapshot to it, giving a `close` test a live daemon socket to
// query without needing a real tmux binary — shared by every test below that
// exercises `close`/`close --dry-run` against a crafted window snapshot.
// Cleanup is registered via t.Cleanup; the two sleeps give the accept
// goroutine and the broadcast time to land before the close subcommand
// connects.
func startCloseIPCServer(t *testing.T, snapshot ipc.StateSnapshot) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "close.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(snapshot)
	time.Sleep(20 * time.Millisecond)
	return socket
}

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
	socket := startCloseIPCServer(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", Status: "done"},
			{Session: "sess-a", WindowIndex: "1", WindowName: "77-review", Status: "running"},
		},
	})

	cmd := exec.Command(binaryPath, "close", "77", "--dry-run", "--socket", socket)
	cmd.Env = emptyStateHome(t)
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

// TestClose_BabysitSupervisedWindow_SkippedAndForceable drives the whole
// #787 guard end to end against the real binary: a supervisor state file on
// disk that owns a PR closing ticket 77 with CI not green must make
// `cenci close 77` report a babysit skip instead of killing the window (the
// killer is never reached, so no tmux binary is required), while `--force`
// bypasses the guard.
func TestClose_BabysitSupervisedWindow_SkippedAndForceable(t *testing.T) {
	socket := startCloseIPCServer(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", Status: "done"},
		},
	})

	// A supervisor paused for human input records PID 0 but is still working
	// on the PR — that state blocks the close without needing a live process.
	stateHome := t.TempDir()
	stateDir := filepath.Join(stateHome, "cenci", "babysit")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	state := `{"schemaVersion":1,"pr":"790","repo":"o/r","agent":"claude","closingIssues":[77],"ciStatus":"pending","status":"needs-input"}`
	if err := os.WriteFile(filepath.Join(stateDir, "abc123-790.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(binaryPath, args...)
		cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("close %v: %v\n%s", args, err, output)
		}
		return string(output)
	}

	for _, args := range [][]string{
		{"close", "77", "--socket", socket},
		{"close", "77", "--dry-run", "--socket", socket},
	} {
		output := run(args...)
		wantLine := "skip 77-implement (sess-a:0): babysit supervising PR #790, CI not green — will close once CI passes (or use --force now)"
		if !strings.Contains(output, wantLine) {
			t.Errorf("%v: output missing the babysit skip line\n want: %s\n  got:\n%s", args, wantLine, output)
		}
		if strings.Contains(output, "closed 77-implement") || strings.Contains(output, "would close 77-implement") {
			t.Errorf("%v: a babysit-guarded window must never be reported as closed, got:\n%s", args, output)
		}
	}

	// --force bypasses the guard entirely: it reaches the killer, which is
	// the only path here that needs a real tmux, so assert on the decision
	// rather than on success.
	cmd := exec.Command(binaryPath, "close", "77", "--force", "--socket", socket)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome)
	forced, _ := cmd.CombinedOutput()
	if strings.Contains(string(forced), "babysit supervising") {
		t.Errorf("--force must bypass the babysit guard, got:\n%s", forced)
	}
}

// TestClose_ArmingZeroChecksWindow_GraceBounded covers two #924 additions at
// once against the real binary: a supervisor recorded as "arming" (PID 0, no
// process to check) now counts as live, and an "unknown" CI verdict blocks
// only inside the settle grace -- a fresh checksAbsentSince still blocks the
// close, while one already past checksSettleGrace (10m) allows it.
func TestClose_ArmingZeroChecksWindow_GraceBounded(t *testing.T) {
	for _, tc := range []struct {
		name              string
		checksAbsentSince time.Time
		wantBlocks        bool
	}{
		{"fresh clock blocks", time.Now().UTC().Add(-2 * time.Minute), true},
		{"past-grace clock allows", time.Now().UTC().Add(-11 * time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := startCloseIPCServer(t, ipc.StateSnapshot{
				Windows: []ipc.WindowState{
					{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", Status: "done"},
				},
			})

			stateHome := t.TempDir()
			stateDir := filepath.Join(stateHome, "cenci", "babysit")
			if err := os.MkdirAll(stateDir, 0700); err != nil {
				t.Fatal(err)
			}
			state := `{"schemaVersion":2,"pr":"790","repo":"o/r","agent":"claude","closingIssues":[77],"ciStatus":"unknown","status":"arming","pid":0,"updatedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","checksAbsentSince":"` + tc.checksAbsentSince.Format(time.RFC3339Nano) + `"}`
			if err := os.WriteFile(filepath.Join(stateDir, "abc123-790.json"), []byte(state), 0600); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(binaryPath, "close", "77", "--dry-run", "--socket", socket)
			cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("close --dry-run: %v\n%s", err, output)
			}

			if tc.wantBlocks {
				wantLine := "skip 77-implement (sess-a:0): babysit supervising PR #790, CI not green — will close once CI passes (or use --force now)"
				if !strings.Contains(string(output), wantLine) {
					t.Errorf("output missing the babysit skip line for an arming PID-0 supervisor within the settle grace\n want: %s\n  got:\n%s", wantLine, output)
				}
			} else {
				if !strings.Contains(string(output), "would close 77-implement") {
					t.Errorf("output missing the would-close line once the settle grace has elapsed, got:\n%s", output)
				}
				if strings.Contains(string(output), "babysit supervising") {
					t.Errorf("a checksAbsentSince past the settle grace must not block, got:\n%s", output)
				}
			}
		})
	}
}

// TestClose_NoMatches_EmptyStdoutExit0 locks in the quieted no-op output
// (#522): a target that matches zero windows must produce no stdout and
// exit 0 -- replacing the old `no matching windows for %q` line, which
// lazyboards surfaced as a spurious "warning" for a legitimate no-op (there
// is nothing actionable to report).
func TestClose_NoMatches_EmptyStdoutExit0(t *testing.T) {
	socket := startCloseIPCServer(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "99-other", Status: "done"},
		},
	})

	cmd := exec.Command(binaryPath, "close", "42", "--socket", socket)
	cmd.Env = emptyStateHome(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("close with zero matches: unexpected error/exit code: %v\n%s", err, output)
	}
	if len(output) != 0 {
		t.Errorf("expected empty stdout for zero matches, got:\n%s", output)
	}
}
