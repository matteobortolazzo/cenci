package main_test

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// -- daemon lifecycle subcommands (daemon start|stop|restart|status) --------
//
// Every test in this section sets its own isolated XDG_RUNTIME_DIR so the
// real "cenci daemon start" subprocess it spawns never touches a real
// daemon's sockets/PID file, and so parallel test runs never collide.

// bgDaemon wraps a backgrounded `daemon start` subprocess together with a
// channel that closes once it has actually been reaped.
type bgDaemon struct {
	cmd  *exec.Cmd
	done chan struct{} // closed once cmd.Wait() has returned
}

// startDaemonBackground spawns `cenci daemon start` in the background
// (not context-bound: these tests need it to keep running until explicitly
// stopped, unlike the bounded-context daemon smoke tests elsewhere in this
// file) and waits for its PID file to appear before returning. The caller's
// test must have already set XDG_RUNTIME_DIR via t.Setenv.
//
// The reaping goroutine below is started immediately (not lazily, on-demand
// later) so the moment the daemon process actually exits — however it dies,
// including from an external `cenci daemon stop`/SIGKILL — it gets
// reaped right away. Otherwise this test process (the daemon subprocess's
// OS-level parent) would leave it as a zombie until something got around to
// calling Wait(), and os.Process.Signal(0) reports a zombie as still
// "alive", making an external stop look like it hung.
func startDaemonBackground(t *testing.T) *bgDaemon {
	t.Helper()
	cmd := exec.Command(binaryPath, "daemon", "start")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start 'daemon start': %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	})
	waitForFile(t, ipc.DefaultPIDPath(), 3*time.Second)
	return &bgDaemon{cmd: cmd, done: done}
}

// waitForFile polls for path to exist, bounded by timeout.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to appear", path)
}

// TestDaemonStart_WritesPIDFile_RemovedOnCleanShutdown covers the PID-file
// half of `daemon start`: the file appears (containing the daemon's real
// PID) once the daemon is up, and is removed again on a clean SIGTERM
// shutdown.
func TestDaemonStart_WritesPIDFile_RemovedOnCleanShutdown(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	bg := startDaemonBackground(t)
	pidPath := ipc.DefaultPIDPath()

	pid, err := daemon.ReadPIDFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != bg.cmd.Process.Pid {
		t.Errorf("pid file contains %d, want %d", pid, bg.cmd.Process.Pid)
	}

	if err := bg.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	select {
	case <-bg.done:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not exit after SIGTERM")
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected pid file to be removed after clean shutdown")
	}
}

// TestDaemonStatus_NotRunning covers `daemon status` against an empty
// runtime dir: it must report not-running and exit non-zero.
func TestDaemonStatus_NotRunning(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	cmd := exec.Command(binaryPath, "daemon", "status")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "not running") {
		t.Errorf("expected 'not running', got:\n%s", output)
	}
}

// TestDaemonStatus_Running covers `daemon status` against a real,
// live-spawned daemon: it must report running (with the PID) and exit 0.
func TestDaemonStatus_Running(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	startDaemonBackground(t)

	cmd := exec.Command(binaryPath, "daemon", "status")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon status: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "running") {
		t.Errorf("expected 'running', got:\n%s", output)
	}
	if strings.Contains(string(output), "not running") {
		t.Errorf("expected running, not 'not running', got:\n%s", output)
	}
}

// TestDaemonStop_RemovesPIDFileAndKillsProcess is the ticket's required
// black-box regression: `daemon stop` against a real, live-spawned daemon
// must remove the PID file and the process must actually be gone.
func TestDaemonStop_RemovesPIDFileAndKillsProcess(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	bg := startDaemonBackground(t)
	pidPath := ipc.DefaultPIDPath()
	pid := bg.cmd.Process.Pid

	cmd := exec.Command(binaryPath, "daemon", "stop")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon stop: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "stopped daemon") {
		t.Errorf("expected 'stopped daemon', got:\n%s", output)
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected pid file to be removed after 'daemon stop'")
	}
	if daemon.ProcessAlive(pid) {
		t.Errorf("expected daemon process %d to be gone after 'daemon stop'", pid)
	}

	select {
	case <-bg.done:
	case <-time.After(2 * time.Second):
		t.Error("expected the daemon subprocess to have been reaped promptly")
	}
}

// TestDaemonStop_NothingRunningIsNoop covers the idempotent no-op path: with
// nothing running, `daemon stop` exits 0 and reports "daemon not running".
func TestDaemonStop_NothingRunningIsNoop(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	cmd := exec.Command(binaryPath, "daemon", "stop")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon stop: expected exit 0, got %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "not running") {
		t.Errorf("expected 'not running', got:\n%s", output)
	}
}

// TestDaemonRestart_StopsOldAndStartsNew covers `daemon restart`: it must
// tear down an existing daemon and bring up a fresh one reachable at the
// same socket, reporting the old PID and a "restarted" confirmation.
func TestDaemonRestart_StopsOldAndStartsNew(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	oldBg := startDaemonBackground(t)
	oldPID := oldBg.cmd.Process.Pid

	cmd := exec.Command(binaryPath, "daemon", "restart")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon restart: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "daemon restarted") {
		t.Errorf("expected 'daemon restarted', got:\n%s", output)
	}

	if daemon.ProcessAlive(oldPID) {
		t.Errorf("expected old daemon process %d to be gone after restart", oldPID)
	}
	if !daemon.Alive(ipc.DefaultEventSocketPath()) {
		t.Error("expected a new daemon to be reachable after restart")
	}

	// The freshly spawned daemon is detached (Setsid) and outlives this test
	// process; stop it directly so it doesn't leak.
	if _, err := daemon.Stop(ipc.DefaultEventSocketPath(), ipc.DefaultPIDPath()); err != nil {
		t.Logf("cleanup: daemon.Stop: %v", err)
	}
}

// TestDaemonGroup_UnknownSubcommand_Exits2 mirrors the top-level unknown
// subcommand guard for the `daemon` subcommand group.
func TestDaemonGroup_UnknownSubcommand_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "daemon", "frobnicate")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), `unknown subcommand "frobnicate"`) {
		t.Errorf("expected unknown-subcommand message, got:\n%s", output)
	}
}
