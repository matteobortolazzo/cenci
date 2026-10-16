package worktreeclean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// --- shared fake-binary helpers (the shims below are only for argv, env,
// timeout and truncation classification; every behavior test uses real git) ---

// installShim writes an executable sh script called name into a fresh temp dir
// and PREPENDS that dir to PATH (never replaces it, so tr/head/sleep stay
// available -- go-gotchas rule 17). It verifies the shim is what name resolves
// to, not an ambient real binary (test-strategy rule 21).
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

// execRunner adapts the three exec entry points to one shape so the
// bounded-subprocess contract is asserted identically for each. timeout points
// at the package var that bounds that entry point.
type execRunner struct {
	name    string
	timeout *time.Duration
	// run executes the shimmed git and returns its stdout (for the streaming
	// runner: the entries joined with NUL), its stderr ("" for the streaming
	// runner, which folds stderr into the error) and the error.
	run func(ctx context.Context, dir string) (stdout, stderr string, err error)
}

func execRunners() []execRunner {
	return []execRunner{
		{"execGit", &gitTimeout, func(ctx context.Context, dir string) (string, string, error) {
			return execGit(ctx, dir, "rev-parse")
		}},
		{"execGitWT", &gitTimeout, func(ctx context.Context, dir string) (string, string, error) {
			return execGitWT(ctx, dir, dir, "rev-parse")
		}},
		{"execGitWTStream", &activityTimeout, func(ctx context.Context, dir string) (string, string, error) {
			var sb strings.Builder
			err := execGitWTStream(ctx, dir, dir, func(e string) error {
				sb.WriteString(e)
				sb.WriteByte(0)
				return nil
			}, "ls-files", "-z")
			return sb.String(), "", err
		}},
	}
}

// --- argv, cwd and stream separation ---

func TestExecGitRunsInDirWithArgvVerbatimAndStreamsSeparate(t *testing.T) {
	installShim(t, "git", `for a in "$@"; do printf 'arg=%s\n' "$a"; done
printf 'DIAG' >&2`)

	stdout, stderr, err := execGit(context.Background(), "/some/dir", "rev-parse", "--show-toplevel", "a b $x")
	if err != nil {
		t.Fatalf("execGit: %v", err)
	}
	want := "arg=-C\narg=/some/dir\narg=rev-parse\narg=--show-toplevel\narg=a b $x\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "DIAG" {
		t.Errorf("stderr = %q, want DIAG kept out of stdout", stderr)
	}
}

func TestExecGitWTPinsGitDirAndWorkTreeAndRunsInTheWorkTree(t *testing.T) {
	installShim(t, "git", `for a in "$@"; do printf 'arg=%s\n' "$a"; done
printf 'cwd=%s\n' "$(pwd -P)"
printf 'DIAG' >&2`)
	gitDir := realPath(t, t.TempDir())
	workTree := realPath(t, t.TempDir())

	stdout, stderr, err := execGitWT(context.Background(), gitDir, workTree, "status", "--porcelain=v2", "a b $x")
	if err != nil {
		t.Fatalf("execGitWT: %v", err)
	}
	want := "arg=--no-optional-locks\narg=-c\narg=core.fsmonitor=false\n" +
		"arg=--git-dir=" + gitDir + "\narg=--work-tree=" + workTree + "\n" +
		"arg=status\narg=--porcelain=v2\narg=a b $x\n" +
		"cwd=" + workTree + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "DIAG" {
		t.Errorf("stderr = %q, want DIAG kept out of stdout", stderr)
	}
}

func TestExecGitWTStreamPinsGitDirAndWorkTreeAndSplitsOnNUL(t *testing.T) {
	installShim(t, "git", `for a in "$@"; do printf '%s\000' "arg=$a"; done
printf '%s\000' "cwd=$(pwd -P)"`)
	gitDir := realPath(t, t.TempDir())
	workTree := realPath(t, t.TempDir())

	var got []string
	err := execGitWTStream(context.Background(), gitDir, workTree, func(e string) error {
		got = append(got, e)
		return nil
	}, "ls-files", "-z", "--others")
	if err != nil {
		t.Fatalf("execGitWTStream: %v", err)
	}
	want := []string{
		"arg=--no-optional-locks", "arg=-c", "arg=core.fsmonitor=false",
		"arg=--git-dir=" + gitDir, "arg=--work-tree=" + workTree,
		"arg=ls-files", "arg=-z", "arg=--others", "cwd=" + workTree,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("entries = %q, want %q", got, want)
	}
}

func TestExecGitWTStreamKeepsNewlinesInsideAnEntry(t *testing.T) {
	installShim(t, "git", `printf 'a\nb\000plain\000'`)

	var got []string
	err := execGitWTStream(context.Background(), t.TempDir(), t.TempDir(), func(e string) error {
		got = append(got, e)
		return nil
	}, "ls-files", "-z")
	if err != nil {
		t.Fatalf("execGitWTStream: %v", err)
	}
	if want := []string{"a\nb", "plain"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("entries = %q, want %q (entries end at NUL, not at a newline)", got, want)
	}
}

// --- environment hardening ---

// TestExecEnvIsHardenedAndRepoLocationVarsAreStripped asserts, for every exec
// entry point, the non-interactive hardening and that ambient repo-location
// variables never reach git (an inherited GIT_INDEX_FILE or GIT_DIR would make
// the read-only inspection read some other repo's state).
func TestExecEnvIsHardenedAndRepoLocationVarsAreStripped(t *testing.T) {
	for _, k := range []string{"GIT_SSH_COMMAND", "GIT_ASKPASS", "SSH_ASKPASS_REQUIRE", "GIT_TERMINAL_PROMPT"} {
		unsetenv(t, k)
	}
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("GIT_NO_LAZY_FETCH", "0")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file:ext:ssh")
	for _, k := range repoLocationVars {
		t.Setenv(k, "/junk/"+k)
	}
	// Ambient config injection, attribute-source and tracing variables.
	stripped := []string{
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_KEY_17",
		"GIT_CONFIG_VALUE_0", "GIT_CONFIG_VALUE_17", "GIT_ATTR_SOURCE",
		"GIT_TRACE", "GIT_TRACE2", "GIT_TRACE2_EVENT", "GIT_TRACE_PACKET", "GIT_TRACE_PERFORMANCE",
	}
	for _, k := range stripped {
		t.Setenv(k, "/junk/"+k)
	}
	// Host-owned config locations are not injection and must survive.
	kept := []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"}
	for _, k := range kept {
		t.Setenv(k, "keep-"+k)
	}
	installShim(t, "git", `env | while IFS= read -r l; do printf '%s\000' "$l"; done`)

	for _, rn := range execRunners() {
		t.Run(rn.name, func(t *testing.T) {
			stdout, _, err := rn.run(context.Background(), realPath(t, t.TempDir()))
			if err != nil {
				t.Fatalf("%s: %v", rn.name, err)
			}
			env := map[string]string{}
			for _, kv := range strings.Split(stdout, "\x00") {
				if k, v, ok := strings.Cut(kv, "="); ok {
					env[k] = v
				}
			}
			if env["GIT_TERMINAL_PROMPT"] != "0" {
				t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0", env["GIT_TERMINAL_PROMPT"])
			}
			if env["GIT_OPTIONAL_LOCKS"] != "0" {
				t.Errorf("GIT_OPTIONAL_LOCKS = %q, want 0 even when the caller set 1", env["GIT_OPTIONAL_LOCKS"])
			}
			if env["SSH_ASKPASS_REQUIRE"] != "never" {
				t.Errorf("SSH_ASKPASS_REQUIRE = %q, want never", env["SSH_ASKPASS_REQUIRE"])
			}
			if v, ok := env["GIT_ASKPASS"]; !ok || v != "" {
				t.Errorf("GIT_ASKPASS = %q (set=%v), want it set to empty", v, ok)
			}
			if !strings.Contains(env["GIT_SSH_COMMAND"], "BatchMode=yes") {
				t.Errorf("GIT_SSH_COMMAND = %q, want BatchMode=yes", env["GIT_SSH_COMMAND"])
			}
			for _, k := range append(append([]string{}, repoLocationVars...), stripped...) {
				if v, ok := env[k]; ok {
					t.Errorf("%s=%q reached git, want it stripped", k, v)
				}
			}
			for _, k := range kept {
				if env[k] != "keep-"+k {
					t.Errorf("%s = %q, want the caller's value kept", k, env[k])
				}
			}
			if env["GIT_NO_LAZY_FETCH"] != "1" {
				t.Errorf("GIT_NO_LAZY_FETCH = %q, want 1 even when the caller set 0 (a read-only inspection never fetches)", env["GIT_NO_LAZY_FETCH"])
			}
			if env["GIT_ALLOW_PROTOCOL"] != "none" {
				t.Errorf("GIT_ALLOW_PROTOCOL = %q, want none even when the caller allowed protocols (no transport may start, e.g. ext:: from a lazy fetch on an older git)", env["GIT_ALLOW_PROTOCOL"])
			}
		})
	}
}

// TestExecGitWTIgnoresAmbientRepoLocationVariables is the real-git proof of the
// stripping: with junk GIT_INDEX_FILE/GIT_OBJECT_DIRECTORY/GIT_NAMESPACE/...
// in the environment, a clean worktree still reports a clean status and its
// real HEAD.
func TestExecGitWTIgnoresAmbientRepoLocationVariables(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-a", "feature/1-a")
	head := w.head(t)
	for _, k := range repoLocationVars {
		t.Setenv(k, filepath.Join(r.Tmp, "junk", k))
	}
	t.Setenv("GIT_NAMESPACE", "junk-namespace")

	stdout, stderr, err := execGitWT(context.Background(), w.AdminDir, w.Path,
		"status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		t.Fatalf("execGitWT status: %v (stderr %q)", err, stderr)
	}
	if !strings.Contains(stdout, "# branch.head feature/1-a") {
		t.Errorf("status = %q, want the real worktree's branch", stdout)
	}
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if !strings.HasPrefix(line, "# ") {
			t.Errorf("status line %q: a clean worktree must list no entries (an inherited GIT_INDEX_FILE leaked in)", line)
		}
	}
	got, _, err := execGitWT(context.Background(), w.AdminDir, w.Path, "log", "-1", "--format=%H")
	if err != nil {
		t.Fatalf("execGitWT log: %v", err)
	}
	if strings.TrimSpace(got) != head {
		t.Errorf("log HEAD = %q, want %q", strings.TrimSpace(got), head)
	}
}

// --- timeouts: deadline, caller deadline, lingering grandchild ---

func TestExecDeadlineKillIsClassifiedAsTimeout(t *testing.T) {
	for _, rn := range execRunners() {
		t.Run(rn.name, func(t *testing.T) {
			setVar(t, rn.timeout, 200*time.Millisecond)
			installShim(t, "git", `exec sleep 30`)

			start := time.Now()
			_, _, err := rn.run(context.Background(), realPath(t, t.TempDir()))
			if !errors.Is(err, errTimeout) {
				t.Fatalf("err = %v, want errors.Is(_, errTimeout)", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("%s took %v, the 200ms timeout did not bound it", rn.name, elapsed)
			}
		})
	}
}

func TestExecCallerDeadlineIsClassifiedAsTimeout(t *testing.T) {
	for _, rn := range execRunners() {
		t.Run(rn.name, func(t *testing.T) {
			installShim(t, "git", `exec sleep 30`)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			start := time.Now()
			_, _, err := rn.run(ctx, realPath(t, t.TempDir()))
			if !errors.Is(err, errTimeout) {
				t.Fatalf("err = %v, want errors.Is(_, errTimeout)", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("%s took %v, the caller's 200ms deadline did not bound it", rn.name, elapsed)
			}
		})
	}
}

func TestExecLingeringGrandchildIsBoundedByWaitDelay(t *testing.T) {
	for _, rn := range execRunners() {
		t.Run(rn.name, func(t *testing.T) {
			// git exits 0 at once, but a background grandchild keeps the output
			// pipes open for 3s. Without WaitDelay, Wait would block that long.
			setVar(t, &waitDelay, 200*time.Millisecond)
			installShim(t, "git", "sleep 3 &\nexit 0")

			start := time.Now()
			_, _, err := rn.run(context.Background(), realPath(t, t.TempDir()))
			if !errors.Is(err, errTimeout) {
				t.Fatalf("err = %v, want a WaitDelay kill classified as errors.Is(_, errTimeout)", err)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("%s took %v, WaitDelay did not bound the lingering grandchild", rn.name, elapsed)
			}
		})
	}
}

// --- output caps ---

func TestExecStdoutCap(t *testing.T) {
	runners := execRunners()[:2] // the streaming runner has no stdout buffer to cap
	for _, rn := range runners {
		t.Run(rn.name+" at the cap is returned whole", func(t *testing.T) {
			setVar(t, &maxStdoutBytes, 100)
			installShim(t, "git", `head -c 100 /dev/zero | tr '\0' x`)
			stdout, _, err := rn.run(context.Background(), realPath(t, t.TempDir()))
			if err != nil {
				t.Fatalf("err = %v, want nil for output exactly at the cap", err)
			}
			if len(stdout) != 100 {
				t.Errorf("len(stdout) = %d, want 100", len(stdout))
			}
		})
		t.Run(rn.name+" over the cap is a truncation error, never a silently short body", func(t *testing.T) {
			setVar(t, &maxStdoutBytes, 100)
			installShim(t, "git", `head -c 500 /dev/zero | tr '\0' x`)
			stdout, _, err := rn.run(context.Background(), realPath(t, t.TempDir()))
			if !errors.Is(err, errOutputTruncated) {
				t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated)", err)
			}
			if len(stdout) > 100 {
				t.Errorf("len(stdout) = %d, want it bounded to 100", len(stdout))
			}
		})
	}
}

func TestExecStderrCap(t *testing.T) {
	for _, rn := range execRunners() {
		t.Run(rn.name+" at the cap", func(t *testing.T) {
			setVar(t, &maxStderrBytes, 100)
			installShim(t, "git", `head -c 100 /dev/zero | tr '\0' e >&2`)
			if _, _, err := rn.run(context.Background(), realPath(t, t.TempDir())); err != nil {
				t.Fatalf("err = %v, want nil for stderr exactly at the cap", err)
			}
		})
		t.Run(rn.name+" over the cap", func(t *testing.T) {
			setVar(t, &maxStderrBytes, 100)
			installShim(t, "git", `head -c 500 /dev/zero | tr '\0' e >&2`)
			_, stderr, err := rn.run(context.Background(), realPath(t, t.TempDir()))
			if !errors.Is(err, errOutputTruncated) {
				t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated)", err)
			}
			if len(stderr) > 100 {
				t.Errorf("len(stderr) = %d, want it bounded to 100", len(stderr))
			}
		})
	}
}

// --- non-zero exit ---

func TestExecNonZeroExitKeepsStderrAndIsNotATimeout(t *testing.T) {
	for _, rn := range execRunners() {
		t.Run(rn.name, func(t *testing.T) {
			installShim(t, "git", `printf 'fatal: boom' >&2; exit 128`)
			_, stderr, err := rn.run(context.Background(), realPath(t, t.TempDir()))
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 128 {
				t.Fatalf("err = %v, want an *exec.ExitError with code 128", err)
			}
			if errors.Is(err, errTimeout) || errors.Is(err, errOutputTruncated) {
				t.Errorf("err = %v, a plain non-zero exit is neither a timeout nor a truncation", err)
			}
			if rn.name == "execGitWTStream" {
				if !strings.Contains(err.Error(), "fatal: boom") {
					t.Errorf("err = %q, want git's stderr folded into the streaming error", err)
				}
			} else if !strings.Contains(stderr, "fatal: boom") {
				t.Errorf("stderr = %q, want git's diagnostic preserved", stderr)
			}
		})
	}
}

// --- streaming specifics ---

func TestExecGitWTStreamIsNotBufferedAgainstTheStdoutCap(t *testing.T) {
	// 200 entries of ~20 KiB (4 MiB) pass through while maxStdoutBytes is 1 KiB:
	// the stream hands entries to the callback as they arrive, nothing is
	// accumulated.
	setVar(t, &maxStdoutBytes, 1024)
	installShim(t, "git", `i=0
while [ $i -lt 200 ]; do
  head -c 20479 /dev/zero | tr '\0' x
  printf '\000'
  i=$((i+1))
done`)

	n := 0
	err := execGitWTStream(context.Background(), t.TempDir(), t.TempDir(), func(e string) error {
		if len(e) != 20479 {
			t.Errorf("entry %d has %d bytes, want 20479", n, len(e))
		}
		n++
		return nil
	}, "ls-files", "-z")
	if err != nil {
		t.Fatalf("execGitWTStream: %v", err)
	}
	if n != 200 {
		t.Errorf("callback saw %d entries, want 200", n)
	}
}

func TestExecGitWTStreamEntryCapIsATruncationError(t *testing.T) {
	setVar(t, &maxActivityEntries, 5)
	installShim(t, "git", `i=0
while [ $i -lt 20 ]; do printf 'e%d\000' $i; i=$((i+1)); done`)

	var got []string
	err := execGitWTStream(context.Background(), t.TempDir(), t.TempDir(), func(e string) error {
		got = append(got, e)
		return nil
	}, "ls-files", "-z")
	if !errors.Is(err, errOutputTruncated) {
		t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated) when the entry cap is exceeded", err)
	}
	if len(got) > 5 {
		t.Errorf("callback saw %d entries, want at most the cap of 5", len(got))
	}
}

func TestExecGitWTStreamOverlongEntryIsATruncationError(t *testing.T) {
	over := maxStreamEntryBytes + 1
	cases := []struct {
		name   string
		script string
	}{
		{"no terminator within the cap", fmt.Sprintf(`head -c %d /dev/zero | tr '\0' x`, over)},
		{"terminated, but over the cap", fmt.Sprintf(`head -c %d /dev/zero | tr '\0' x
printf '\000'`, over)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installShim(t, "git", tc.script)

			n := 0
			err := execGitWTStream(context.Background(), t.TempDir(), t.TempDir(), func(string) error {
				n++
				return nil
			}, "ls-files", "-z")
			if !errors.Is(err, errOutputTruncated) {
				t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated) for an entry over %d bytes", err, maxStreamEntryBytes)
			}
			if n != 0 {
				t.Errorf("callback saw %d entries, want none from an over-cap entry", n)
			}
		})
	}
}

// TestEntryWriterCapHoldsWhenTheTerminatorArrivesInTheChunkThatCrossesIt drives
// the writer directly because pipe chunking decides, in a shimmed run, whether
// the last bytes of an over-cap entry share a write with its NUL.
func TestEntryWriterCapHoldsWhenTheTerminatorArrivesInTheChunkThatCrossesIt(t *testing.T) {
	n := 0
	w := &entryWriter{onEntry: func(string) error { n++; return nil }}
	if _, err := w.Write([]byte(strings.Repeat("x", maxStreamEntryBytes-10))); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	_, err := w.Write([]byte(strings.Repeat("x", 20) + "\x00"))
	if !errors.Is(err, errOutputTruncated) {
		t.Errorf("err = %v, want errors.Is(_, errOutputTruncated): the entry is %d bytes", err, maxStreamEntryBytes+10)
	}
	if n != 0 {
		t.Errorf("callback saw %d entries, want none from an over-cap entry", n)
	}
}

func TestExecGitWTStreamEntryExactlyAtTheByteCapIsNotAnError(t *testing.T) {
	installShim(t, "git", fmt.Sprintf(`head -c %d /dev/zero | tr '\0' x
printf '\000'`, maxStreamEntryBytes))

	var got []string
	err := execGitWTStream(context.Background(), t.TempDir(), t.TempDir(), func(e string) error {
		got = append(got, e)
		return nil
	}, "ls-files", "-z")
	if err != nil || len(got) != 1 || len(got[0]) != maxStreamEntryBytes {
		t.Errorf("err = %v, entries = %d, want nil and one entry of exactly %d bytes", err, len(got), maxStreamEntryBytes)
	}
}

func TestExecGitWTStreamUnterminatedTrailingEntryIsATruncationError(t *testing.T) {
	installShim(t, "git", `printf 'complete\000cut-off'`)

	var got []string
	err := execGitWTStream(context.Background(), t.TempDir(), t.TempDir(), func(e string) error {
		got = append(got, e)
		return nil
	}, "ls-files", "-z")
	if !errors.Is(err, errOutputTruncated) {
		t.Fatalf("err = %v, want errors.Is(_, errOutputTruncated) when git's output ends mid-entry", err)
	}
	if strings.Join(got, "|") != "complete" {
		t.Errorf("entries = %q, want only the terminated one", got)
	}
}

func TestExecGitWTStreamUnderTheEntryCapIsNotAnError(t *testing.T) {
	setVar(t, &maxActivityEntries, 5)
	installShim(t, "git", `printf 'a\000b\000c\000d\000e\000'`)

	n := 0
	err := execGitWTStream(context.Background(), t.TempDir(), t.TempDir(), func(string) error {
		n++
		return nil
	}, "ls-files", "-z")
	if err != nil || n != 5 {
		t.Errorf("err = %v, entries = %d, want nil and 5 for exactly the cap", err, n)
	}
}

func TestExecGitWTStreamCallbackErrorAbortsAndIsReturned(t *testing.T) {
	errStop := errors.New("stop")
	installShim(t, "git", `i=0
while [ $i -lt 1000 ]; do printf 'e%d\000' $i; i=$((i+1)); done`)

	calls := 0
	start := time.Now()
	err := execGitWTStream(context.Background(), t.TempDir(), t.TempDir(), func(string) error {
		calls++
		if calls == 2 {
			return errStop
		}
		return nil
	}, "ls-files", "-z")
	if !errors.Is(err, errStop) {
		t.Fatalf("err = %v, want errors.Is(_, the callback's error)", err)
	}
	if calls != 2 {
		t.Errorf("callback ran %d times, want it not called again after returning an error", calls)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("abort took %v, want it prompt", elapsed)
	}
}
