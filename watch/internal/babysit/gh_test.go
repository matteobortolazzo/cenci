package babysit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// -- #854: execGh bounding ----------------------------------------------------
//
// Mirrors internal/dispatch/gh_test.go's PATH-shim technique (Architectural
// Context: "test technique in dispatch/gh_test.go"). These tests exercise
// execGh directly (not defaultExecGh) so a future seam override in
// production code is still covered by the same assertions.

// installFakeGH installs a fake `gh` on a fresh PATH (replacing it wholly,
// matching dispatch/collect_test.go's installFakeGH -- no shell-pipeline
// dependency exists in these scripts, so PATH replacement is safe here per
// watch/docs/go-gotchas.md #852).
func installFakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// TestExecGh_TimeoutBounded covers the "PATH-shimmed gh that sleeps past
// ghTimeout returns bounded" requirement: a `gh` invocation that sleeps far
// longer than ghTimeout must be killed by the context deadline and return
// well before the full sleep duration elapses, not hang for it.
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
// dispatch's TestExecGh_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay: a
// `gh` invocation whose process exits normally but leaves a background
// grandchild holding the stdout/stderr pipes open must not stall execGh past
// its bounded WaitDelay.
func TestExecGh_HungGrandchildHoldingStdout_ReturnsWithinWaitDelay(t *testing.T) {
	shimDir := t.TempDir()
	script := "#!/bin/sh\n(sleep 30 &) \nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	_, _, err := execGh("issue", "list")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("execGh returned nil error; expected the documented WaitDelay-forced-close error")
	}
	if elapsed > 20*time.Second {
		t.Fatalf("execGh took %v, want it to return well within ghTimeout thanks to WaitDelay (~%v)", elapsed, ghWaitDelay)
	}
	if elapsed < ghWaitDelay {
		t.Fatalf("execGh returned in %v, faster than ghWaitDelay (%v) -- want it to actually wait out the grandchild rather than short-circuit before spawning the real subprocess at all", elapsed, ghWaitDelay)
	}
}

// TestExecGh_SeparatesStdoutAndStderr covers watch/AGENTS.md's #825 rule
// (separate stdout/stderr capture, not CombinedOutput): a benign stderr
// diagnostic on an otherwise-successful call must never corrupt stdout, and
// both streams must be independently retrievable by the caller.
func TestExecGh_SeparatesStdoutAndStderr(t *testing.T) {
	installFakeGH(t, "printf 'stdout-payload'\nprintf 'stderr-noise' >&2\n")

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

// TestExecGh_OversizedOutputIsExplicitTruncationError pins the ghOutputCap
// bound (Files to Create): stdout larger than the bounded cap must surface
// as an explicit errGhOutputTruncated error, never a silently-short JSON
// body that a caller's json.Unmarshal could decode as if it were complete
// (watch/docs/error-handling.md's default-deny rule).
func TestExecGh_OversizedOutputIsExplicitTruncationError(t *testing.T) {
	// dd is a multi-tool dependency -- per watch/docs/go-gotchas.md #852,
	// installFakeGH's PATH *replacement* would make it unresolvable, so this
	// script prepends to PATH instead (mirroring installFakeGHOnPath).
	dir := t.TempDir()
	script := "#!/bin/sh\ndd if=/dev/zero bs=1M count=5 2>/dev/null\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err := execGh("issue", "list")
	if err == nil {
		t.Fatal("execGh: err = nil, want an explicit truncation error for output exceeding ghOutputCap")
	}
	if !errors.Is(err, errGhOutputTruncated) {
		t.Fatalf("execGh err = %v, want errors.Is(err, errGhOutputTruncated)", err)
	}
}

// TestExecGh_UsesNoninteractiveEnv pins the noninteractive environment
// contract (Files to Create): GH_PROMPT_DISABLED, GH_NO_UPDATE_NOTIFIER,
// GH_PAGER, and GIT_TERMINAL_PROMPT must all reach the child process, so a
// `gh` invocation can never block on an interactive prompt or a pager under
// the supervisor's detached mode.
func TestExecGh_UsesNoninteractiveEnv(t *testing.T) {
	installFakeGH(t, `printf 'GH_PROMPT_DISABLED=%s GH_NO_UPDATE_NOTIFIER=%s GH_PAGER=%s GIT_TERMINAL_PROMPT=%s' "$GH_PROMPT_DISABLED" "$GH_NO_UPDATE_NOTIFIER" "$GH_PAGER" "$GIT_TERMINAL_PROMPT"`+"\n")

	stdout, _, err := execGh("issue", "list")
	if err != nil {
		t.Fatalf("execGh returned unexpected error: %v", err)
	}
	want := "GH_PROMPT_DISABLED=1 GH_NO_UPDATE_NOTIFIER=1 GH_PAGER=cat GIT_TERMINAL_PROMPT=0"
	if !strings.Contains(stdout, want) {
		t.Fatalf("stdout = %q, want it to contain %q (the noninteractive env vars)", stdout, want)
	}
}
