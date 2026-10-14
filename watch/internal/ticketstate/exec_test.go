package ticketstate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// --- shared fake-binary helpers (used by every *_test.go in this package) ---

// installShim writes an executable sh script called name into a fresh temp dir
// and PREPENDS that dir to PATH (never replaces it, so tr/head/sleep stay
// available -- go-gotchas rule 17). It verifies the shim is what name
// resolves to, not an ambient real binary (test-strategy rule 21), and
// returns the shim directory. The script may locate its dir via
// `dir=$(dirname "$0")`.
func installShim(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	exectest.WriteExecutable(t, path, "#!/bin/sh\n"+script+"\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := exec.LookPath(name)
	if err != nil || got != path {
		t.Fatalf("LookPath(%q) = %q, %v; want the shim %q", name, got, err, path)
	}
	return dir
}

// setVar overrides *p for the test and restores it afterwards.
func setVar[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// unsetenv removes key for the test (restored on cleanup).
func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// --- execGh ---

func TestExecGhKeepsStdoutAndStderrSeparateAndPassesArgvVerbatim(t *testing.T) {
	installShim(t, "gh", `for a in "$@"; do printf '%s|' "$a"; done
printf 'DIAGNOSTIC' >&2`)

	stdout, stderr, err := execGh(context.Background(), "api", "graphql", "-f", "query=a b $x")
	if err != nil {
		t.Fatalf("execGh: %v", err)
	}
	if want := "api|graphql|-f|query=a b $x|"; stdout != want {
		t.Errorf("stdout = %q, want %q (stderr must never merge into it)", stdout, want)
	}
	if stderr != "DIAGNOSTIC" {
		t.Errorf("stderr = %q, want DIAGNOSTIC", stderr)
	}
}

func TestExecGhNonZeroExitKeepsOutputAndIsNotATimeout(t *testing.T) {
	installShim(t, "gh", `printf 'partial-body'; printf 'gh: bad' >&2; exit 3`)

	stdout, stderr, err := execGh(context.Background(), "api")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("err = %v, want an *exec.ExitError with code 3", err)
	}
	if stdout != "partial-body" || stderr != "gh: bad" {
		t.Errorf("stdout=%q stderr=%q, want the streams preserved on a non-zero exit", stdout, stderr)
	}
	if errors.Is(err, errTimeout) || errors.Is(err, errOutputTruncated) {
		t.Errorf("err = %v, a plain non-zero exit is neither a timeout nor a truncation", err)
	}
}

func TestExecGhDeadlineKillIsClassifiedAsTimeout(t *testing.T) {
	setVar(t, &ghTimeout, 200*time.Millisecond)
	installShim(t, "gh", `exec sleep 30`)

	start := time.Now()
	_, _, err := execGh(context.Background(), "api")
	if !errors.Is(err, errTimeout) {
		t.Fatalf("err = %v, want errors.Is(_, errTimeout)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("execGh took %v, the 200ms ghTimeout did not bound it", elapsed)
	}
}

func TestExecGhCallerDeadlineIsClassifiedAsTimeout(t *testing.T) {
	// A shared overarching deadline shorter than ghTimeout must classify the
	// same way (go-gotchas rule 22), not surface as a bare "signal: killed".
	installShim(t, "gh", `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := execGh(ctx, "api")
	if !errors.Is(err, errTimeout) {
		t.Fatalf("err = %v, want errors.Is(_, errTimeout)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("execGh took %v, the caller's 200ms deadline did not bound it", elapsed)
	}
}

func TestExecGhLingeringGrandchildIsBoundedByWaitDelay(t *testing.T) {
	// gh itself exits 0 at once, but a background grandchild keeps the output
	// pipes open for 3s. Without WaitDelay, Wait would block that long.
	setVar(t, &waitDelay, 200*time.Millisecond)
	installShim(t, "gh", `sleep 3 &
exit 0`)

	start := time.Now()
	_, _, err := execGh(context.Background(), "api")
	if !errors.Is(err, errTimeout) {
		t.Fatalf("err = %v, want a WaitDelay kill classified as errors.Is(_, errTimeout)", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("execGh took %v, WaitDelay did not bound the lingering grandchild", elapsed)
	}
}

func TestExecGhStdoutCap(t *testing.T) {
	t.Run("under the cap is returned whole", func(t *testing.T) {
		installShim(t, "gh", `head -c 1000000 /dev/zero | tr '\0' x`)
		stdout, _, err := execGh(context.Background(), "api")
		if err != nil {
			t.Fatalf("err = %v, want nil for output under the cap", err)
		}
		if len(stdout) != 1000000 {
			t.Errorf("len(stdout) = %d, want 1000000", len(stdout))
		}
	})
	t.Run("over the cap is a truncation error, never a silently short body", func(t *testing.T) {
		installShim(t, "gh", `head -c 1100000 /dev/zero | tr '\0' x`)
		stdout, _, err := execGh(context.Background(), "api")
		if !errors.Is(err, errOutputTruncated) {
			t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated)", err)
		}
		if len(stdout) > maxStdoutBytes {
			t.Errorf("len(stdout) = %d, want it bounded to %d", len(stdout), maxStdoutBytes)
		}
	})
}

func TestExecGhStderrCap(t *testing.T) {
	t.Run("under the cap", func(t *testing.T) {
		installShim(t, "gh", `head -c 60000 /dev/zero | tr '\0' e >&2`)
		_, stderr, err := execGh(context.Background(), "api")
		if err != nil {
			t.Fatalf("err = %v, want nil for stderr under the cap", err)
		}
		if len(stderr) != 60000 {
			t.Errorf("len(stderr) = %d, want 60000", len(stderr))
		}
	})
	t.Run("over the cap", func(t *testing.T) {
		installShim(t, "gh", `head -c 70000 /dev/zero | tr '\0' e >&2`)
		_, stderr, err := execGh(context.Background(), "api")
		if !errors.Is(err, errOutputTruncated) {
			t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated)", err)
		}
		if len(stderr) > maxStderrBytes {
			t.Errorf("len(stderr) = %d, want it bounded to %d", len(stderr), maxStderrBytes)
		}
	})
}

// --- execGit ---

func TestExecGitRunsInDirWithHardenedNonInteractiveEnv(t *testing.T) {
	for _, k := range []string{"GIT_SSH_COMMAND", "GIT_ASKPASS", "SSH_ASKPASS_REQUIRE", "GIT_TERMINAL_PROMPT"} {
		unsetenv(t, k)
	}
	installShim(t, "git", `for a in "$@"; do printf 'arg=%s\n' "$a"; done
printf 'prompt=%s\n' "$GIT_TERMINAL_PROMPT"
printf 'askpass_require=%s\n' "$SSH_ASKPASS_REQUIRE"
printf 'ssh=%s\n' "$GIT_SSH_COMMAND"
printf 'DIAG' >&2`)

	stdout, stderr, err := execGit(context.Background(), "/some/dir", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("execGit: %v", err)
	}
	for _, want := range []string{
		"arg=-C\narg=/some/dir\narg=rev-parse\narg=--show-toplevel\n",
		"prompt=0\n",
		"askpass_require=never\n",
		"BatchMode=yes",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "DIAG") || stderr != "DIAG" {
		t.Errorf("stdout=%q stderr=%q, want the streams kept separate", stdout, stderr)
	}
}

func TestExecGitDeadlineKillIsClassifiedAsTimeout(t *testing.T) {
	setVar(t, &gitTimeout, 200*time.Millisecond)
	installShim(t, "git", `exec sleep 30`)

	start := time.Now()
	_, _, err := execGit(context.Background(), t.TempDir(), "rev-parse")
	if !errors.Is(err, errTimeout) {
		t.Fatalf("err = %v, want errors.Is(_, errTimeout)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("execGit took %v, the 200ms gitTimeout did not bound it", elapsed)
	}
}

func TestExecGitLingeringGrandchildIsBoundedByWaitDelay(t *testing.T) {
	setVar(t, &waitDelay, 200*time.Millisecond)
	installShim(t, "git", `sleep 3 &
exit 0`)

	start := time.Now()
	_, _, err := execGit(context.Background(), t.TempDir(), "rev-parse")
	if !errors.Is(err, errTimeout) {
		t.Fatalf("err = %v, want a WaitDelay kill classified as errors.Is(_, errTimeout)", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("execGit took %v, WaitDelay did not bound the lingering grandchild", elapsed)
	}
}

func TestExecGitOutputCaps(t *testing.T) {
	installShim(t, "git", `head -c 1100000 /dev/zero | tr '\0' x`)
	if _, _, err := execGit(context.Background(), t.TempDir(), "x"); !errors.Is(err, errOutputTruncated) {
		t.Errorf("stdout over cap: err = %v, want errors.Is(_, errOutputTruncated)", err)
	}
	installShim(t, "git", `head -c 70000 /dev/zero | tr '\0' e >&2`)
	if _, _, err := execGit(context.Background(), t.TempDir(), "x"); !errors.Is(err, errOutputTruncated) {
		t.Errorf("stderr over cap: err = %v, want errors.Is(_, errOutputTruncated)", err)
	}
}

func TestExecGitNonZeroExitKeepsStderr(t *testing.T) {
	installShim(t, "git", `printf 'fatal: not a git repository' >&2; exit 128`)

	_, stderr, err := execGit(context.Background(), t.TempDir(), "rev-parse")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 128 {
		t.Fatalf("err = %v, want an *exec.ExitError with code 128", err)
	}
	if !strings.Contains(stderr, "not a git repository") {
		t.Errorf("stderr = %q, want git's diagnostic preserved", stderr)
	}
}
