package dispatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// -- #852 AC6: execGh bounding -----------------------------------------------

// TestExecGh_TimeoutBounded covers AC6's "PATH-shimmed gh that sleeps past
// ghTimeout returns bounded" requirement: a `gh` invocation that sleeps for
// far longer than ghTimeout must be killed by the context deadline and
// return well before the full sleep duration elapses, not hang for it.
func TestExecGh_TimeoutBounded(t *testing.T) {
	shimDir := t.TempDir()
	script := "#!/bin/sh\nsleep 120\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	_, _, err := execGh("issue", "list")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected execGh to return an error when gh is killed by ghTimeout")
	}
	if elapsed >= 100*time.Second {
		t.Fatalf("execGh took %v, want it bounded well within ghTimeout (%v), not the full 120s sleep", elapsed, ghTimeout)
	}
}

// TestExecGh_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay mirrors
// TestExecGit_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay
// (mainsync_test.go:392): a `gh` invocation whose process exits normally but
// leaves a background grandchild holding the stdout/stderr pipes open must
// not stall execGh past its bounded WaitDelay -- otherwise a lingering
// grandchild could stall a dispatch/reconcile pass indefinitely even though
// gh itself finished.
func TestExecGh_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay(t *testing.T) {
	shimDir := t.TempDir()
	script := "#!/bin/sh\n(sleep 30 &) \nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	// The grandchild keeps the output pipes open well past WaitDelay, so
	// Wait is documented (os/exec) to force them closed and return an error
	// wrapping ErrWaitDelay -- that error IS the fix working as designed
	// (bounded, not a hang), so it's expected here, not a failure.
	_, _, err := execGh("issue", "list")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("execGh returned nil error; expected the documented WaitDelay-forced-close error")
	}
	if elapsed > 20*time.Second {
		t.Fatalf("execGh took %v, want it to return well within ghTimeout thanks to WaitDelay (~%v)", elapsed, ghWaitDelay)
	}
	if elapsed < ghWaitDelay {
		t.Fatalf("execGh returned in %v, faster than ghWaitDelay (%v) -- test may not be exercising the intended lingering-grandchild path", elapsed, ghWaitDelay)
	}
}

// TestExecGh_SeparatesStdoutAndStderr covers watch/AGENTS.md's #825 rule
// (separate stdout/stderr capture, not CombinedOutput): a benign stderr
// diagnostic on an otherwise-successful call must never corrupt stdout, and
// both streams must be independently retrievable by the caller.
func TestExecGh_SeparatesStdoutAndStderr(t *testing.T) {
	installFakeGHOnPath(t, "printf 'stdout-payload'\nprintf 'stderr-noise' >&2\n")

	stdout, stderr, err := execGh("issue", "list")
	if err != nil {
		t.Fatalf("execGh returned unexpected error: %v", err)
	}
	if stdout != "stdout-payload" {
		t.Errorf("stdout = %q, want %q", stdout, "stdout-payload")
	}
	if stderr != "stderr-noise" {
		t.Errorf("stderr = %q, want %q", stderr, "stderr-noise")
	}
}
