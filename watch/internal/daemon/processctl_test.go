package daemon

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func withShortStopTimeouts(t *testing.T) {
	t.Helper()
	restoreGrace, restorePoll := stopGraceTimeout, stopPollInterval
	stopGraceTimeout = 300 * time.Millisecond
	stopPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { stopGraceTimeout, stopPollInterval = restoreGrace, restorePoll })
}

func TestPIDFile_WriteReadRemoveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cenci.pid")

	if err := WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	pid, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("ReadPIDFile = %d, want %d (own pid)", pid, os.Getpid())
	}

	RemovePIDFile(path)
	if _, err := ReadPIDFile(path); err == nil {
		t.Error("expected ReadPIDFile to fail after RemovePIDFile")
	}
}

func TestRemovePIDFile_MissingFileIsNoop(t *testing.T) {
	// Must not panic or error on a path that was never written.
	RemovePIDFile(filepath.Join(t.TempDir(), "never-written.pid"))
}

func TestReadPIDFile_MalformedContentErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cenci.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPIDFile(path); err == nil {
		t.Error("expected ReadPIDFile to error on malformed content")
	}
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Error("expected own process to be reported alive")
	}
	if ProcessAlive(0) {
		t.Error("expected pid 0 to be reported not alive")
	}
	if ProcessAlive(-1) {
		t.Error("expected negative pid to be reported not alive")
	}

	// A short-lived child process, waited on so it's reaped: its pid must
	// then report not-alive.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run 'true': %v", err)
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Errorf("expected exited+reaped pid %d to be reported not alive", cmd.Process.Pid)
	}
}

func TestStatus_NotRunning(t *testing.T) {
	dir := t.TempDir()
	info := Status(filepath.Join(dir, "events.sock"), filepath.Join(dir, "cenci.pid"))
	if info.Running {
		t.Error("expected Running=false with no listener")
	}
}

func TestStatus_RunningReportsPID(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "events.sock")
	pidPath := filepath.Join(dir, "cenci.pid")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if err := WritePIDFile(pidPath); err != nil {
		t.Fatal(err)
	}

	info := Status(socketPath, pidPath)
	if !info.Running {
		t.Error("expected Running=true with a live listener")
	}
	if info.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", info.PID, os.Getpid())
	}
}

func TestStatus_RunningWithMissingPIDFileReportsZeroPID(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "events.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	info := Status(socketPath, filepath.Join(dir, "missing.pid"))
	if !info.Running {
		t.Error("expected Running=true with a live listener even if the pid file is missing")
	}
	if info.PID != 0 {
		t.Errorf("PID = %d, want 0 (unknown)", info.PID)
	}
}

func TestStop_NothingRunningIsNoopNoError(t *testing.T) {
	dir := t.TempDir()
	outcome, err := Stop(filepath.Join(dir, "events.sock"), filepath.Join(dir, "cenci.pid"))
	if err != nil {
		t.Fatalf("Stop: unexpected error: %v", err)
	}
	if outcome.WasRunning {
		t.Error("expected WasRunning=false when nothing is running")
	}
}

func TestStop_StaleUnreachablePIDFileIsRemoved(t *testing.T) {
	// PID file points at a definitely-dead pid, and no socket is alive: Stop
	// must clean up the stale file and report nothing was running.
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "cenci.pid")

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := cmd.Process.Pid
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(deadPID)), 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Stop(filepath.Join(dir, "events.sock"), pidPath)
	if err != nil {
		t.Fatalf("Stop: unexpected error: %v", err)
	}
	if outcome.WasRunning {
		t.Error("expected WasRunning=false for a stale pid file with no live socket")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected stale pid file to be removed")
	}
}

func TestStop_ValidPIDFileSendsSIGTERMAndRemovesPIDFile(t *testing.T) {
	withShortStopTimeouts(t)
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "events.sock")
	pidPath := filepath.Join(dir, "cenci.pid")

	// A real long-running child process, so terminateAndWait has something
	// to signal and wait on. "sleep" ignores nothing special about SIGTERM —
	// the default disposition terminates the process, which is exactly the
	// behavior being exercised.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	outcome, err := Stop(socketPath, pidPath)
	if err != nil {
		t.Fatalf("Stop: unexpected error: %v", err)
	}
	if !outcome.WasRunning {
		t.Error("expected WasRunning=true")
	}
	if outcome.PID != cmd.Process.Pid {
		t.Errorf("PID = %d, want %d", outcome.PID, cmd.Process.Pid)
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Error("expected process to be gone after Stop")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected pid file to be removed after Stop")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("expected the child process's Wait() to have returned")
	}
}

func TestStop_EscalatesToSIGKILLWhenProcessIgnoresSIGTERM(t *testing.T) {
	withShortStopTimeouts(t)
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "events.sock")
	pidPath := filepath.Join(dir, "cenci.pid")

	// A shell that traps and ignores SIGTERM, forcing the SIGKILL escalation
	// path.
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	// Give the shell a moment to install the trap before it's signaled.
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	outcome, err := Stop(socketPath, pidPath)
	if err != nil {
		t.Fatalf("Stop: unexpected error: %v", err)
	}
	if !outcome.WasRunning {
		t.Error("expected WasRunning=true")
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Error("expected process to be gone after SIGKILL escalation")
	}
}

func TestStop_StaleUnreadablePIDFileButLiveSocketFallsBackToPgrep(t *testing.T) {
	withShortStopTimeouts(t)
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "events.sock")
	pidPath := filepath.Join(dir, "cenci.pid") // never written: missing

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	restore := pgrepDaemon
	pgrepDaemon = func() (int, error) { return cmd.Process.Pid, nil }
	defer func() { pgrepDaemon = restore }()

	outcome, err := Stop(socketPath, pidPath)
	if err != nil {
		t.Fatalf("Stop: unexpected error: %v", err)
	}
	if !outcome.WasRunning || outcome.PID != cmd.Process.Pid {
		t.Errorf("outcome = %+v, want WasRunning=true PID=%d", outcome, cmd.Process.Pid)
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Error("expected process found via pgrep fallback to be stopped")
	}
}

func TestStop_LiveSocketButPgrepFindsNothingErrors(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "events.sock")
	pidPath := filepath.Join(dir, "cenci.pid") // missing

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	restore := pgrepDaemon
	pgrepDaemon = func() (int, error) { return 0, nil }
	defer func() { pgrepDaemon = restore }()

	_, err = Stop(socketPath, pidPath)
	if err == nil {
		t.Error("expected an error when the socket is alive but no process can be found")
	}
}

func TestStop_PgrepErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "events.sock")
	pidPath := filepath.Join(dir, "cenci.pid")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	boom := errors.New("boom")
	restore := pgrepDaemon
	pgrepDaemon = func() (int, error) { return 0, boom }
	defer func() { pgrepDaemon = restore }()

	_, err = Stop(socketPath, pidPath)
	if err == nil {
		t.Fatal("expected an error to propagate from pgrepDaemon")
	}
}
