package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// CgroupReadTimeout bounds one cgroup-file read exec (and, as WaitDelay,
	// the wait after the kill).
	CgroupReadTimeout = 3 * time.Second
	// MaxCgroupFileBytes caps what one cgroup-file read accepts from the
	// container: a real cgroup v2 file is well under 1 KiB, so anything larger
	// is not that file.
	MaxCgroupFileBytes = 4 << 10

	// cgroupMemoryCurrentPath and cgroupMemoryMaxPath are the cgroup v2 files
	// ReadContainerMemory reads in one exec.
	cgroupMemoryCurrentPath = "/sys/fs/cgroup/memory.current"
	cgroupMemoryMaxPath     = "/sys/fs/cgroup/memory.max"

	// cgroupMemoryEventsPath is the cgroup v2 file ReadContainerOOMKills reads
	// its oom_kill counter from.
	cgroupMemoryEventsPath = "/sys/fs/cgroup/memory.events"

	// maxStderrCaptureBytes caps the runtime stderr kept for an error cause
	// (cgroup reads, container listing); logSafeText truncates it further when
	// it is embedded.
	maxStderrCaptureBytes = 1 << 10
)

// cappedBuffer accumulates at most limit bytes and discards the rest, noting
// that it did, so a runaway writer costs bounded memory and the caller can
// refuse the over-long output instead of parsing a truncated prefix.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.limit - c.buf.Len(); n > room {
		c.overflow = true
		p = p[:room]
	}
	c.buf.Write(p)
	return n, nil
}

// ReadContainerFiles runs `<runtime> exec <container> /bin/cat <paths...>`
// bounded by timeout (also its WaitDelay) and the caller's ctx, and returns
// its stdout. It execs the absolute /bin/cat (present in the sandbox base
// image) with no shell. Output over maxBytes is an error, never a truncated
// prefix; a timeout joins context.DeadlineExceeded; a non-zero exit carries
// the runtime's bounded, quoted stderr.
func ReadContainerFiles(ctx context.Context, runtime, container string, timeout time.Duration, maxBytes int, paths ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append([]string{"exec", container, "/bin/cat"}, paths...)
	cmd := exec.CommandContext(cctx, runtime, args...)
	cmd.WaitDelay = timeout
	stdout := &cappedBuffer{limit: maxBytes}
	stderr := &cappedBuffer{limit: maxStderrCaptureBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s exec %s: %w%s", runtime, container, classifyExecTimeout(cctx, err), stderrSuffix(&stderr.buf))
	}
	if stdout.overflow {
		return nil, fmt.Errorf("%s exec %s: output exceeds %d bytes", runtime, container, maxBytes)
	}
	return stdout.buf.Bytes(), nil
}

// ParseCgroupMemory parses the output of `cat memory.current memory.max`:
// exactly two non-empty lines, a non-negative integer then a positive integer
// or the literal "max". unlimited is true for "max" (limit is then 0). Anything
// else, including a zero limit, is an error so no caller can divide by zero or
// mistake garbage for an unlimited container.
func ParseCgroupMemory(out []byte) (current, limit int64, unlimited bool, err error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return 0, 0, false, fmt.Errorf("want 2 lines (memory.current, memory.max), got %d", len(lines))
	}
	current, err = parseCgroupInt(lines[0])
	if err != nil {
		return 0, 0, false, fmt.Errorf("memory.current: %w", err)
	}
	maxLine := strings.TrimSpace(lines[1])
	if maxLine == "max" {
		return current, 0, true, nil
	}
	limit, err = parseCgroupInt(maxLine)
	if err != nil {
		return 0, 0, false, fmt.Errorf("memory.max: %w", err)
	}
	if limit == 0 {
		return 0, 0, false, errors.New("memory.max: want a positive integer or \"max\", got 0")
	}
	return current, limit, false, nil
}

// parseCgroupInt parses a plain non-negative decimal integer: digits only (no
// sign, no fraction) that fit int64.
func parseCgroupInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, fmt.Errorf("want a non-negative integer, got %s", logSafeText(s))
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("want a non-negative integer, got %s: %w", logSafeText(s), err)
	}
	return n, nil
}

// ParseMemoryEventsOOMKill extracts the oom_kill counter from a cgroup v2
// memory.events file. The separate "oom" line counts times the limit was hit
// and is deliberately ignored: only oom_kill is processes actually killed. ok
// is false when there is no oom_kill line or its value is not a non-negative
// integer that fits int64.
func ParseMemoryEventsOOMKill(content string) (count int64, ok bool) {
	for _, line := range SplitLines(content) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "oom_kill" {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// ReadContainerOOMKills reads a running container's cgroup v2 oom_kill counter
// from memory.events in one bounded exec. The runtime must be docker or podman
// and the container a well-formed sandbox container name, so neither reaches an
// exec argv unchecked. A failed, timed-out, over-long or malformed read, or a
// file with no oom_kill line (cgroup v1, a host cgroup namespace), is an error
// rather than a zero count.
func ReadContainerOOMKills(ctx context.Context, runtime, container string) (int64, error) {
	if !ValidLaunchRuntime(runtime) {
		return 0, fmt.Errorf("oom_kill read: runtime %s is not docker or podman", logSafeText(runtime))
	}
	if !ValidSandboxContainerName(container) {
		return 0, fmt.Errorf("oom_kill read: %s is not a sandbox container name", logSafeText(container))
	}
	out, err := ReadContainerFiles(ctx, runtime, container, CgroupReadTimeout, MaxCgroupFileBytes, cgroupMemoryEventsPath)
	if err != nil {
		return 0, err
	}
	count, ok := ParseMemoryEventsOOMKill(string(out))
	if !ok {
		return 0, fmt.Errorf("%s exec %s: %s has no readable oom_kill line", runtime, container, cgroupMemoryEventsPath)
	}
	return count, nil
}

// ReadContainerMemory reads and parses a running container's cgroup v2
// memory.current and memory.max in one bounded exec. unlimited is true when
// memory.max reads "max". A failed, timed-out, over-long or malformed read is
// an error (a cgroup v1 host, where cat fails, is therefore never "unlimited").
func ReadContainerMemory(ctx context.Context, runtime, container string) (current, limit int64, unlimited bool, err error) {
	out, err := ReadContainerFiles(ctx, runtime, container, CgroupReadTimeout, MaxCgroupFileBytes, cgroupMemoryCurrentPath, cgroupMemoryMaxPath)
	if err != nil {
		return 0, 0, false, err
	}
	current, limit, unlimited, err = ParseCgroupMemory(out)
	if err != nil {
		return 0, 0, false, fmt.Errorf("%s exec %s: %w", runtime, container, err)
	}
	return current, limit, unlimited, nil
}
