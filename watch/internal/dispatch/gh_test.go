package dispatch

import (
	"errors"
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

// -- #881: execGhBounded (a sibling of execGh, execGh itself untouched) -----
//
// Mirrors internal/babysit/gh.go:143-204's existing boundary-test pattern
// (watch/docs/error-handling.md #412: a new sentinel detectable via
// errors.Is needs a direct unit test at the package boundary, not only
// indirect coverage via openpr.go's higher-level tests).

// TestExecGhBounded_TimeoutBounded covers the sleeping-shim half of #412's
// direct errors.Is requirement for errGhTimeout: a `gh` invocation that
// sleeps far longer than ghTimeout must be killed by the context deadline,
// classify as errGhTimeout via errors.Is, and return well before the full
// sleep duration elapses.
func TestExecGhBounded_TimeoutBounded(t *testing.T) {
	shimDir := t.TempDir()
	script := "#!/bin/sh\nsleep 120\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	_, _, err := execGhBounded(4<<20, "api", "graphql")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected execGhBounded to return an error when gh is killed by ghTimeout")
	}
	if !errors.Is(err, errGhTimeout) {
		t.Fatalf("execGhBounded err = %v, want errors.Is(err, errGhTimeout)", err)
	}
	if elapsed >= 100*time.Second {
		t.Fatalf("execGhBounded took %v, want it bounded well within ghTimeout (%v), not the full 120s sleep", elapsed, ghTimeout)
	}
}

// TestExecGhBounded_OversizedOutputIsExplicitTruncationError pins the
// maxStdout parameter (Files to Modify: `execGhBounded(maxStdout int,
// args ...string)`): stdout larger than the caller-supplied cap must
// surface as an explicit errGhOutputTruncated error, never a silently-short
// body a caller's json.Unmarshal could decode as if it were complete
// (watch/docs/error-handling.md's default-deny rule) -- mirroring
// babysit's TestExecGh_OversizedOutputIsExplicitTruncationError.
func TestExecGhBounded_OversizedOutputIsExplicitTruncationError(t *testing.T) {
	// dd is a multi-tool dependency -- installFakeGH's PATH *replacement*
	// would make it unresolvable, so this uses installFakeGHOnPath instead
	// (mirroring collect_test.go's own documented hazard).
	installFakeGHOnPath(t, "dd if=/dev/zero bs=1M count=5 2>/dev/null\n")

	const smallCap = 1024 // far below the 5 MiB the shim writes
	_, _, err := execGhBounded(smallCap, "api", "graphql")
	if err == nil {
		t.Fatal("execGhBounded: err = nil, want an explicit truncation error for output exceeding maxStdout")
	}
	if !errors.Is(err, errGhOutputTruncated) {
		t.Fatalf("execGhBounded err = %v, want errors.Is(err, errGhOutputTruncated)", err)
	}
}

// TestExecGhBounded_SeparatesStdoutAndStderr covers the same #825
// stdout/stderr-separation rule execGh already pins, applied to the new
// bounded sibling.
func TestExecGhBounded_SeparatesStdoutAndStderr(t *testing.T) {
	installFakeGHOnPath(t, "printf 'stdout-payload'\nprintf 'stderr-noise' >&2\n")

	stdout, stderr, err := execGhBounded(4<<20, "api", "graphql")
	if err != nil {
		t.Fatalf("execGhBounded returned unexpected error: %v", err)
	}
	if stdout != "stdout-payload" {
		t.Errorf("stdout = %q, want %q", stdout, "stdout-payload")
	}
	if stderr != "stderr-noise" {
		t.Errorf("stderr = %q, want %q", stderr, "stderr-noise")
	}
}

// TestExecGhBounded_NonzeroExitReturnsError covers the plain command-failure
// path (no timeout, no truncation): a `gh` invocation that exits nonzero
// with no other classification must still surface a non-nil error, distinct
// from both sentinels above (neither errors.Is(err, errGhTimeout) nor
// errors.Is(err, errGhOutputTruncated) matches), so openpr.go's own
// classification can tell "ordinary command failure" (mid-pagination
// failure -> OpenPRProbeUnreadable) apart from the two bounded sentinels.
func TestExecGhBounded_NonzeroExitReturnsError(t *testing.T) {
	installFakeGHOnPath(t, "echo boom >&2\nexit 1\n")

	_, _, err := execGhBounded(4<<20, "api", "graphql")
	if err == nil {
		t.Fatal("execGhBounded: err = nil, want an error for a nonzero exit")
	}
	if errors.Is(err, errGhTimeout) {
		t.Errorf("a plain nonzero exit must not classify as errGhTimeout, got %v", err)
	}
	if errors.Is(err, errGhOutputTruncated) {
		t.Errorf("a plain nonzero exit must not classify as errGhOutputTruncated, got %v", err)
	}
}

// TestExecGh_ByteUnchangedByExecGhBounded is a non-regression guard for the
// plan's explicit constraint: "execGh (gh.go) stays byte-unchanged" -- adding
// execGhBounded as a sibling must never perturb execGh's own existing
// unbounded behavior (no cap, no new sentinel wrapping) for its existing
// callers (collectRepoTickets, currentGitHubLogin, ...).
func TestExecGh_ByteUnchangedByExecGhBounded(t *testing.T) {
	installFakeGHOnPath(t, "dd if=/dev/zero bs=1M count=5 2>/dev/null\n")

	stdout, _, err := execGh("issue", "list")
	if err != nil {
		t.Fatalf("execGh returned unexpected error: %v (execGh itself must stay unbounded)", err)
	}
	if len(stdout) != 5*1024*1024 {
		t.Fatalf("execGh stdout length = %d, want the full unbounded 5 MiB payload (execGh must stay byte-unchanged)", len(stdout))
	}
}
