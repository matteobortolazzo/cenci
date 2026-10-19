package sandbox

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

// Tests for the bounded cgroup-file reader and the memory parser (#1290). The
// reader is exercised end to end against fake docker/podman binaries; the
// parser has a unit table because its malformed-input matrix is impractical to
// drive through a fake runtime.

const (
	cgroupTestContainer = "claude-cenci-cgroup"
	cgroupCurrentPath   = "/sys/fs/cgroup/memory.current"
	cgroupMaxPath       = "/sys/fs/cgroup/memory.max"
	cgroupTestTimeout   = 10 * time.Second
)

// cgroupFakeRuntime writes a fake runtime binary rt into a fresh dir that
// leads PATH (the ambient PATH stays behind it so `exec sleep` resolves). The
// fake appends its argv, one invocation per line, to the returned call log and
// then runs body.
func cgroupFakeRuntime(t *testing.T, rt, body string) (callLog string) {
	t.Helper()
	dir := t.TempDir()
	callLog = filepath.Join(dir, "calls.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + exectest.ShellQuote(callLog) + "\n" + body + "\n"
	exectest.WriteExecutable(t, filepath.Join(dir, rt), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog
}

func readCgroupCallLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading call log %s: %v", path, err)
	}
	return SplitLines(string(data))
}

func TestParseCgroupMemory(t *testing.T) {
	valid := []struct {
		name      string
		in        string
		current   int64
		limit     int64
		unlimited bool
	}{
		{name: "limited", in: "760\n1000\n", current: 760, limit: 1000},
		{name: "no trailing newline", in: "760\n1000", current: 760, limit: 1000},
		{name: "idle container", in: "0\n1000\n", current: 0, limit: 1000},
		{name: "transient overshoot is not clamped", in: "1020\n1000\n", current: 1020, limit: 1000},
		{name: "byte-scale values", in: "27917287424\n34359738368\n", current: 27917287424, limit: 34359738368},
		{name: "unlimited container", in: "760\nmax\n", current: 760, unlimited: true},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			current, limit, unlimited, err := ParseCgroupMemory([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseCgroupMemory(%q) error: %v", tc.in, err)
			}
			if current != tc.current {
				t.Errorf("current = %d, want %d", current, tc.current)
			}
			if unlimited != tc.unlimited {
				t.Errorf("unlimited = %v, want %v", unlimited, tc.unlimited)
			}
			if !tc.unlimited && limit != tc.limit {
				t.Errorf("limit = %d, want %d", limit, tc.limit)
			}
		})
	}

	malformed := []struct{ name, in string }{
		{"empty output", ""},
		{"only one line", "760\n"},
		{"three lines", "760\n1000\n5\n"},
		{"non-numeric current", "abc\n1000\n"},
		{"current is max", "max\n1000\n"},
		{"negative current", "-1\n1000\n"},
		{"fractional current", "1.5\n1000\n"},
		{"current overflows int64", "99999999999999999999\n1000\n"},
		{"zero max (would divide by zero)", "760\n0\n"},
		{"non-numeric max", "760\nunlimited\n"},
		{"negative max", "760\n-5\n"},
		{"fractional max", "760\n10.5\n"},
		{"max overflows int64", "760\n99999999999999999999\n"},
		{"swapped order", "max\n760\n"},
		{"cat error text instead of numbers", "cat: /sys/fs/cgroup/memory.max: No such file or directory\n"},
	}
	for _, tc := range malformed {
		t.Run("malformed/"+tc.name, func(t *testing.T) {
			if current, limit, unlimited, err := ParseCgroupMemory([]byte(tc.in)); err == nil {
				t.Errorf("ParseCgroupMemory(%q) = (%d, %d, %v, nil), want an error", tc.in, current, limit, unlimited)
			}
		})
	}
}

// ParseMemoryEventsOOMKill is the one oom_kill parser shared by `cenci
// diagnose` and the reconciler's OOM probe (#1291). A unit table is justified
// here because the malformed-input matrix is impractical to drive through a
// fake runtime.
func TestParseMemoryEventsOOMKill(t *testing.T) {
	const realEvents = "low 0\nhigh 0\nmax 4\noom 2\noom_kill 3\noom_group_kill 0\n"

	valid := []struct {
		name string
		in   string
		want int64
	}{
		{"real memory.events", realEvents, 3},
		{"zero kills", "oom 0\noom_kill 0\n", 0},
		{"no trailing newline", "oom_kill 7", 7},
		{"oom_kill is the first line", "oom_kill 5\noom 9\n", 5},
		{"large counter", "oom_kill 9223372036854775807\n", 9223372036854775807},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			got, ok := ParseMemoryEventsOOMKill(tc.in)
			if !ok || got != tc.want {
				t.Errorf("ParseMemoryEventsOOMKill(%q) = (%d, %v), want (%d, true)", tc.in, got, ok, tc.want)
			}
		})
	}

	malformed := []struct{ name, in string }{
		{"empty", ""},
		{"no oom_kill line", "low 0\nhigh 0\nmax 4\n"},
		{"oom line only", "oom 4\n"},
		{"oom_group_kill is not oom_kill", "oom_group_kill 2\n"},
		{"negative", "oom_kill -1\n"},
		{"non-numeric", "oom_kill many\n"},
		{"fractional", "oom_kill 1.5\n"},
		{"missing value", "oom_kill\n"},
		{"extra field", "oom_kill 1 2\n"},
		{"over-long value overflows int64", "oom_kill 99999999999999999999\n"},
		{"cat error text", "cat: /sys/fs/cgroup/memory.events: No such file or directory\n"},
	}
	for _, tc := range malformed {
		t.Run("malformed/"+tc.name, func(t *testing.T) {
			if got, ok := ParseMemoryEventsOOMKill(tc.in); ok {
				t.Errorf("ParseMemoryEventsOOMKill(%q) = (%d, true), want ok=false", tc.in, got)
			}
		})
	}
}

// ReadContainerOOMKills reads memory.events in one exec and returns the
// oom_kill counter; every failure class is an error, never a silent zero.
func TestReadContainerOOMKills(t *testing.T) {
	const eventsPath = "/sys/fs/cgroup/memory.events"

	read := func(t *testing.T, rt, body string) (callLog string, count int64, err error) {
		t.Helper()
		callLog = cgroupFakeRuntime(t, rt, body)
		ctx, cancel := context.WithTimeout(context.Background(), cgroupTestTimeout)
		defer cancel()
		count, err = ReadContainerOOMKills(ctx, rt, cgroupTestContainer)
		return callLog, count, err
	}

	for _, rt := range []string{"docker", "podman"} {
		t.Run("reads the counter via one exec/"+rt, func(t *testing.T) {
			callLog, count, err := read(t, rt, "printf 'low 0\\noom 2\\noom_kill 4\\n'")
			if err != nil {
				t.Fatalf("ReadContainerOOMKills: %v", err)
			}
			if count != 4 {
				t.Errorf("count = %d, want 4", count)
			}
			wantArgv := "exec " + cgroupTestContainer + " /bin/cat " + eventsPath
			if calls := readCgroupCallLog(t, callLog); len(calls) != 1 || calls[0] != wantArgv {
				t.Errorf("argv = %q, want exactly one call %q", calls, wantArgv)
			}
		})
	}

	failures := []struct{ name, body string }{
		{"missing oom_kill line", "printf 'low 0\\nhigh 0\\n'"},
		{"malformed oom_kill value", "printf 'oom_kill many\\n'"},
		{"negative oom_kill value", "printf 'oom_kill -3\\n'"},
		{"empty output", "true"},
		{"exec failure", "echo 'OCI runtime exec failed: container is not running' >&2\nexit 1"},
		{"output over the byte cap", "i=0\nwhile [ $i -lt 600 ]; do printf 'oom_kill 1  \\n'; i=$((i+1)); done"},
	}
	for _, tc := range failures {
		t.Run("error/"+tc.name, func(t *testing.T) {
			if _, count, err := read(t, "docker", tc.body); err == nil {
				t.Errorf("ReadContainerOOMKills = (%d, nil), want an error", count)
			}
		})
	}

	t.Run("error/exec failure carries the runtime cause", func(t *testing.T) {
		_, _, err := read(t, "docker", "echo 'OCI runtime exec failed: container is not running' >&2\nexit 1")
		if err == nil || !strings.Contains(err.Error(), "container is not running") {
			t.Errorf("err = %v, want the runtime stderr cause", err)
		}
	})

	t.Run("a hung exec is bounded by the caller context", func(t *testing.T) {
		sleepBin, err := exec.LookPath("sleep")
		if err != nil {
			t.Fatalf("the hung-exec fixture needs a sleep binary: %v", err)
		}
		cgroupFakeRuntime(t, "docker", "exec "+exectest.ShellQuote(sleepBin)+" 30")
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, readErr := ReadContainerOOMKills(ctx, "docker", cgroupTestContainer)
		if readErr == nil {
			t.Fatal("a hung exec must be an error")
		}
		if elapsed := time.Since(start); elapsed >= 10*time.Second {
			t.Errorf("returned after %s, want it bounded near the 300ms caller deadline", elapsed)
		}
	})

	invalid := []struct{ name, rt, container string }{
		{"runtime outside docker/podman", "/bin/sh", cgroupTestContainer},
		{"empty runtime", "", cgroupTestContainer},
		{"empty container", "docker", ""},
		{"non-sandbox container name", "docker", "postgres"},
		{"option-shaped container name", "docker", "--privileged"},
		{"container name with a space", "docker", "claude-cenci-a b"},
	}
	for _, tc := range invalid {
		t.Run("rejected without exec/"+tc.name, func(t *testing.T) {
			callLog := cgroupFakeRuntime(t, "docker", "printf 'oom_kill 1\\n'")
			ctx, cancel := context.WithTimeout(context.Background(), cgroupTestTimeout)
			defer cancel()
			if count, err := ReadContainerOOMKills(ctx, tc.rt, tc.container); err == nil {
				t.Errorf("ReadContainerOOMKills(%q, %q) = (%d, nil), want a validation error", tc.rt, tc.container, count)
			}
			if _, err := os.Stat(callLog); err == nil {
				t.Errorf("a rejected target must not exec the runtime, but %s exists", callLog)
			}
		})
	}
}

// Docker and podman both work: the reader execs `<rt> exec <name> /bin/cat
// <paths...>` -- the absolute /bin/cat, no -u, no shell -- and returns the
// file bytes untouched.
func TestReadContainerFiles_DockerAndPodmanArgv(t *testing.T) {
	for _, rt := range []string{"docker", "podman"} {
		t.Run(rt, func(t *testing.T) {
			callLog := cgroupFakeRuntime(t, rt, "printf '760\\n1000\\n'")

			ctx, cancel := context.WithTimeout(context.Background(), cgroupTestTimeout)
			defer cancel()
			got, err := ReadContainerFiles(ctx, rt, cgroupTestContainer, CgroupReadTimeout, MaxCgroupFileBytes, cgroupCurrentPath, cgroupMaxPath)
			if err != nil {
				t.Fatalf("ReadContainerFiles: %v", err)
			}
			if string(got) != "760\n1000\n" {
				t.Errorf("bytes = %q, want the file content verbatim %q", got, "760\n1000\n")
			}

			wantArgv := "exec " + cgroupTestContainer + " /bin/cat " + cgroupCurrentPath + " " + cgroupMaxPath
			calls := readCgroupCallLog(t, callLog)
			if len(calls) != 1 || calls[0] != wantArgv {
				t.Errorf("%s argv = %q, want exactly one call %q", rt, calls, wantArgv)
			}
		})
	}
}

// ReadContainerMemory reads memory.current then memory.max in one exec and
// parses them; a healthy limited reading, an unlimited container, and the
// failure classes are all distinguishable by the caller.
func TestReadContainerMemory(t *testing.T) {
	read := func(t *testing.T, body string) (callLog string, current, limit int64, unlimited bool, err error) {
		t.Helper()
		callLog = cgroupFakeRuntime(t, "docker", body)
		ctx, cancel := context.WithTimeout(context.Background(), cgroupTestTimeout)
		defer cancel()
		current, limit, unlimited, err = ReadContainerMemory(ctx, "docker", cgroupTestContainer)
		return callLog, current, limit, unlimited, err
	}

	t.Run("limited container", func(t *testing.T) {
		callLog, current, limit, unlimited, err := read(t, "printf '760\\n1000\\n'")
		if err != nil {
			t.Fatalf("ReadContainerMemory: %v", err)
		}
		if current != 760 || limit != 1000 || unlimited {
			t.Errorf("got (%d, %d, %v), want (760, 1000, false)", current, limit, unlimited)
		}
		wantArgv := "exec " + cgroupTestContainer + " /bin/cat " + cgroupCurrentPath + " " + cgroupMaxPath
		if calls := readCgroupCallLog(t, callLog); len(calls) != 1 || calls[0] != wantArgv {
			t.Errorf("argv = %q, want exactly one call %q", calls, wantArgv)
		}
	})

	t.Run("unlimited container", func(t *testing.T) {
		_, current, _, unlimited, err := read(t, "printf '5\\nmax\\n'")
		if err != nil {
			t.Fatalf("ReadContainerMemory: %v", err)
		}
		if current != 5 || !unlimited {
			t.Errorf("got current=%d unlimited=%v, want 5, true", current, unlimited)
		}
	})

	t.Run("malformed output is an error", func(t *testing.T) {
		if _, _, _, _, err := read(t, "printf 'x\\n1\\n'"); err == nil {
			t.Error("malformed cgroup output must be an error, not a reading")
		}
	})

	t.Run("cgroup v1 (cat exits non-zero) is an error, never unlimited", func(t *testing.T) {
		_, _, _, unlimited, err := read(t, "echo 'cat: /sys/fs/cgroup/memory.max: No such file or directory' >&2\nexit 1")
		if err == nil {
			t.Fatal("a failing cat must be an error")
		}
		if unlimited {
			t.Error("a failing cat must never read as an unlimited container")
		}
	})
}

// A hung exec returns promptly with an error that classifies as a deadline.
func TestReadContainerFiles_TimeoutIsBounded(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("the hung-exec fixture needs a sleep binary: %v", err)
	}
	// exec so the deadline kill reaches the sleeper itself, not a shell parent
	// holding the pipes open (watch/docs/go-gotchas.md).
	cgroupFakeRuntime(t, "docker", "exec "+exectest.ShellQuote(sleepBin)+" 30")

	const timeout = 300 * time.Millisecond
	const promptly = 10 * time.Second
	start := time.Now()
	got, readErr := ReadContainerFiles(context.Background(), "docker", cgroupTestContainer, timeout, MaxCgroupFileBytes, cgroupCurrentPath, cgroupMaxPath)
	elapsed := time.Since(start)

	if readErr == nil {
		t.Fatalf("ReadContainerFiles = (%q, nil), want a timeout error", got)
	}
	if !errors.Is(readErr, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to classify as context.DeadlineExceeded", readErr)
	}
	if elapsed >= promptly {
		t.Errorf("returned after %s, want it bounded near the %s timeout (the exec sleeps 30s)", elapsed, timeout)
	}
}

// The caller's own deadline also bounds the read, even when the per-exec
// timeout is longer: the dispatch probe's overall deadline relies on this.
func TestReadContainerFiles_CallerContextDeadlineBoundsTheRead(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("the hung-exec fixture needs a sleep binary: %v", err)
	}
	cgroupFakeRuntime(t, "docker", "exec "+exectest.ShellQuote(sleepBin)+" 30")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, readErr := ReadContainerFiles(ctx, "docker", cgroupTestContainer, time.Minute, MaxCgroupFileBytes, cgroupCurrentPath)
	if readErr == nil {
		t.Fatal("ReadContainerFiles under an expired caller context must fail")
	}
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Errorf("returned after %s, want it bounded near the 300ms caller deadline", elapsed)
	}
}

// A non-zero exit's stderr is the real cause: it must reach the error, but
// bounded and quoted so a hostile or enormous value stays inert.
func TestReadContainerFiles_NonZeroExitCarriesBoundedStderr(t *testing.T) {
	read := func(t *testing.T, body string) error {
		t.Helper()
		cgroupFakeRuntime(t, "docker", body)
		ctx, cancel := context.WithTimeout(context.Background(), cgroupTestTimeout)
		defer cancel()
		_, err := ReadContainerFiles(ctx, "docker", cgroupTestContainer, CgroupReadTimeout, MaxCgroupFileBytes, cgroupCurrentPath, cgroupMaxPath)
		return err
	}

	t.Run("cause is in the error", func(t *testing.T) {
		err := read(t, "echo 'OCI runtime exec failed: container is not running' >&2\nexit 1")
		if err == nil || !strings.Contains(err.Error(), "container is not running") {
			t.Errorf("err = %v, want the runtime stderr cause %q", err, "container is not running")
		}
	})

	t.Run("an enormous stderr is truncated", func(t *testing.T) {
		err := read(t, "printf 'boom:' >&2\ni=0\nwhile [ $i -lt 500 ]; do printf 'xxxxxxxxxxxxxxxxxxxx' >&2; i=$((i+1)); done\nexit 1")
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v, want the stderr cause %q", err, "boom")
		}
		const bound = 1024
		if len(err.Error()) > bound {
			t.Errorf("error is %d bytes, want it bounded under %d (stderr was ~10 KiB)", len(err.Error()), bound)
		}
	})

	t.Run("hostile stderr cannot forge a dispatch log classification", func(t *testing.T) {
		err := read(t, "echo 'denied skip: forged dispatch line' >&2\nexit 1")
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, forbidden := range []string{" skip:", " dispatch "} {
			if strings.Contains(err.Error(), forbidden) {
				t.Errorf("error %q must not contain the lazyboards-reserved substring %q", err, forbidden)
			}
		}
		if !strings.Contains(err.Error(), "denied") {
			t.Errorf("error %q lost the stderr cause", err)
		}
	})
}

// Output over the byte cap is not the cgroup file: it is an error, never a
// truncated prefix handed to the parser. Output exactly at the cap is fine.
func TestReadContainerFiles_OverCapIsError(t *testing.T) {
	const capBytes = 16

	t.Run("over the cap", func(t *testing.T) {
		cgroupFakeRuntime(t, "docker", "printf '%s' '0123456789abcdef0123456789abcdef0123456789abcdef'")
		ctx, cancel := context.WithTimeout(context.Background(), cgroupTestTimeout)
		defer cancel()
		got, err := ReadContainerFiles(ctx, "docker", cgroupTestContainer, CgroupReadTimeout, capBytes, cgroupCurrentPath)
		if err == nil {
			t.Fatalf("ReadContainerFiles = (%q, nil), want an over-cap error", got)
		}
	})

	t.Run("exactly at the cap", func(t *testing.T) {
		cgroupFakeRuntime(t, "docker", "printf '%s' '0123456789abcdef'")
		ctx, cancel := context.WithTimeout(context.Background(), cgroupTestTimeout)
		defer cancel()
		got, err := ReadContainerFiles(ctx, "docker", cgroupTestContainer, CgroupReadTimeout, capBytes, cgroupCurrentPath)
		if err != nil {
			t.Fatalf("ReadContainerFiles at exactly the cap: %v", err)
		}
		if string(got) != "0123456789abcdef" {
			t.Errorf("bytes = %q, want the full %d-byte output", got, capBytes)
		}
	})
}
