package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// This file implements the process-control half of `cenci daemon
// start|stop|restart|status`: PID file read/write, liveness probing of an
// already-running daemon process, and the SIGTERM/SIGKILL stop sequence. It
// is distinct from lifecycle_test.go in this package, which covers the
// session-hook event lifecycle (SessionStart/Stop/SessionEnd), not the OS
// process lifecycle covered here.

// stopGraceTimeout bounds how long Stop waits after SIGTERM before
// escalating to SIGKILL. stopPollInterval is the polling cadence within that
// window. Both are vars so tests can shrink them.
var stopGraceTimeout = 3 * time.Second
var stopPollInterval = 50 * time.Millisecond

// daemonProcessPattern is the pgrep -f pattern used to find a running daemon
// process when the PID file is missing or stale. The leading "[/]" avoids
// pgrep matching its own invocation (a literal "[/]" never appears as a raw
// "/" in pgrep's own argv). The trailing "( start)?$" anchors on the two
// forms `cenci daemon` (bare, e.g. hand-started in a pane) and
// `cenci daemon start` (the canonical form Spawn uses) without matching
// `daemon stop`/`daemon status`/`daemon restart` invocations, including our
// own.
const daemonProcessPattern = `[/]cenci daemon( start)?$`

// pgrepDaemon is the process-table fallback used by Stop when the PID file is
// missing or stale but the daemon's socket is still alive. It is a package
// var so tests can stub it without depending on a real "cenci daemon"
// process existing in the table.
var pgrepDaemon = func() (int, error) {
	out, err := exec.Command("pgrep", "-f", daemonProcessPattern).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return 0, nil // pgrep's documented "no processes matched" exit code
		}
		return 0, err
	}
	for _, field := range strings.Fields(string(out)) {
		pid, perr := strconv.Atoi(field)
		if perr != nil || pid == os.Getpid() {
			continue
		}
		return pid, nil
	}
	return 0, nil
}

// WritePIDFile writes the current process's PID to path (0600: owner-only,
// matching the 0700 socket dir it lives alongside).
func WritePIDFile(path string) error {
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// RemovePIDFile best-effort removes the PID file at path. A missing file is
// not an error — removal is idempotent so callers can defer it unconditionally.
func RemovePIDFile(path string) {
	_ = os.Remove(path)
}

// ReadPIDFile reads and parses the PID file at path.
func ReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parsing pid file %s: %w", path, err)
	}
	return pid, nil
}

// ProcessAlive reports whether a process with the given PID currently
// exists, using signal 0 (a null signal that performs existence/permission
// checks without actually signaling the process).
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// StatusInfo is the result of Status: whether a daemon is running, and its
// PID if known.
type StatusInfo struct {
	// Running is authoritative — it reflects a live socket dial.
	Running bool
	// PID is best-effort informational context from the PID file. It may be
	// 0 even when Running is true (e.g. the PID file could not be read).
	PID int
}

// Status reports whether a daemon is currently reachable at eventSocketPath.
// The PID, read from pidPath, is included for display but is never used to
// determine Running — a stale PID file must never report "not running" as
// "running", nor vice versa.
func Status(eventSocketPath, pidPath string) StatusInfo {
	info := StatusInfo{Running: Alive(eventSocketPath)}
	if pid, err := ReadPIDFile(pidPath); err == nil {
		info.PID = pid
	}
	return info
}

// StopOutcome describes what Stop did.
type StopOutcome struct {
	// WasRunning reports whether a daemon was found and stopped. False means
	// Stop found nothing to do (already-idempotent no-op).
	WasRunning bool
	// PID is the process that was signaled. Zero when WasRunning is false.
	PID int
}

// Stop stops a running daemon. Liveness is determined the same way
// EnsureRunning's on-demand startup does: a dial against eventSocketPath.
// Given a live daemon, it reads pidPath for the PID, sends SIGTERM, and
// polls (bounded by stopGraceTimeout) for the process to exit, escalating to
// SIGKILL if it is still alive afterward. If the PID file is missing or
// stale (points at a dead/foreign process) but the socket reports a live
// daemon, it falls back to a pgrep -f process-table scan
// (daemonProcessPattern) to find the process to signal. A stale PID file is
// always removed. Safe to call unconditionally: when nothing is running, it
// returns a zero StopOutcome and a nil error.
func Stop(eventSocketPath, pidPath string) (StopOutcome, error) {
	pid, pidErr := ReadPIDFile(pidPath)
	pidFileValid := pidErr == nil && ProcessAlive(pid)

	if pidErr == nil && !pidFileValid {
		// The file exists but points at a dead/foreign process — stale.
		RemovePIDFile(pidPath)
	}

	if !pidFileValid {
		if !Alive(eventSocketPath) {
			// Nothing running by either signal — idempotent no-op.
			return StopOutcome{}, nil
		}
		found, err := pgrepDaemon()
		if err != nil {
			return StopOutcome{}, fmt.Errorf("daemon socket is alive but locating its process failed: %w", err)
		}
		if found == 0 {
			return StopOutcome{}, fmt.Errorf("daemon socket is alive but no matching process was found (pid file missing/stale)")
		}
		pid = found
	}

	if err := terminateAndWait(pid); err != nil {
		return StopOutcome{}, err
	}
	RemovePIDFile(pidPath)
	return StopOutcome{WasRunning: true, PID: pid}, nil
}

// terminateAndWait sends SIGTERM to pid and polls (bounded by
// stopGraceTimeout) for it to exit, escalating to SIGKILL if it is still
// alive once the grace period elapses.
func terminateAndWait(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	if !ProcessAlive(pid) {
		return nil // already gone
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if !ProcessAlive(pid) {
			return nil // exited between the check above and the signal
		}
		return fmt.Errorf("signaling pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(stopGraceTimeout)
	for time.Now().Before(deadline) {
		if !ProcessAlive(pid) {
			return nil
		}
		time.Sleep(stopPollInterval)
	}

	if !ProcessAlive(pid) {
		return nil
	}
	_ = proc.Signal(syscall.SIGKILL)
	killDeadline := time.Now().Add(stopGraceTimeout)
	for time.Now().Before(killDeadline) {
		if !ProcessAlive(pid) {
			return nil
		}
		time.Sleep(stopPollInterval)
	}
	return fmt.Errorf("pid %d did not exit after SIGTERM and SIGKILL", pid)
}
