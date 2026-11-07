package main_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// -- login service subverbs (daemon install-service|uninstall-service|service-status) --
//
// Every test isolates the ambient state the service code reads: HOME and
// XDG_CONFIG_HOME point at temp dirs so a real installed unit can never be seen
// or touched, and CENCI_SANDBOX is cleared explicitly (the dev container sets
// it to 1, which would turn each non-sandbox case into an early return).

// serviceHost is one isolated host for a service test.
type serviceHost struct {
	home string
	xdg  string
}

func newServiceHost(t *testing.T) serviceHost {
	t.Helper()
	h := serviceHost{home: t.TempDir(), xdg: t.TempDir()}
	t.Setenv("HOME", h.home)
	t.Setenv("XDG_CONFIG_HOME", h.xdg)
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_SOCKET_DIR", t.TempDir())
	return h
}

func (h serviceHost) unitPath() string {
	return filepath.Join(h.xdg, "systemd", "user", "cenci-watch.service")
}

// installFakeSystemctl puts a logging fake systemctl first on PATH; every call
// exits 0. restartBody is extra shell run for `--user restart cenci-watch.service`.
func installFakeSystemctl(t *testing.T, restartBody string) (callLog string) {
	t.Helper()
	return installFakeSystemctlHooks(t, map[string]string{"--user restart cenci-watch.service": restartBody})
}

// installFakeSystemctlHooks replaces PATH with a dir holding only a logging fake
// systemctl, so a real one can never be reached. Every call exits 0 unless its
// argument string has a hook, whose shell body runs instead (and may `exit N`).
// The fake uses only shell builtins, which is all a replaced PATH leaves it.
func installFakeSystemctlHooks(t *testing.T, hooks map[string]string) (callLog string) {
	t.Helper()
	dir := t.TempDir()
	callLog = filepath.Join(dir, "systemctl.log")
	var script strings.Builder
	script.WriteString("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + callLog + "'\ncase \"$*\" in\n")
	for args, body := range hooks {
		script.WriteString("'" + args + "')\n" + body + "\n;;\n")
	}
	script.WriteString("esac\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return callLog
}

// writeLauncher creates the launcher the unit's ExecStart names, which
// install-service requires to exist.
func (h serviceHost) writeLauncher(t *testing.T) {
	t.Helper()
	dir := filepath.Join(h.home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cenci"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// startBody is the fake `enable --now` / `restart` body: it starts the real
// daemon detached, as a service manager would, and records its pid.
func startBody(pidRecord string) string {
	return "'" + binaryPath + "' daemon start </dev/null >/dev/null 2>&1 &\necho $! >> '" + pidRecord + "'"
}

// stopBody is the fake `disable --now` body: SIGTERM every recorded daemon, as a
// service manager stopping the unit would.
func stopBody(pidRecord string) string {
	return "while read pid; do kill \"$pid\" 2>/dev/null; done < '" + pidRecord + "'"
}

// killRecorded force-kills every daemon the fake started, so no test leaks one.
func killRecorded(t *testing.T, pidRecord string) {
	t.Helper()
	data, err := os.ReadFile(pidRecord)
	if err != nil {
		return
	}
	for _, field := range strings.Fields(string(data)) {
		if pid, convErr := strconv.Atoi(field); convErr == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// daemonRunning reports whether `cenci daemon status` finds a live daemon.
func daemonRunning(t *testing.T) bool {
	t.Helper()
	_, _, code := runServiceCmd(t, "daemon", "status")
	return code == 0
}

// runServiceCmd runs the built binary and returns its streams and exit code.
func runServiceCmd(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running %v: %v", args, err)
	}
	return out.String(), errOut.String(), code
}

func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the fake systemctl cases drive the Linux backend")
	}
}

func TestServiceStatus_SandboxReportsUnsupportedAndExitsZero(t *testing.T) {
	newServiceHost(t)
	t.Setenv("CENCI_SANDBOX", "1")

	stdout, stderr, code := runServiceCmd(t, "daemon", "service-status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stdout != "service: unsupported\n" {
		t.Errorf("stdout = %q, want %q", stdout, "service: unsupported\n")
	}
}

func TestServiceStatus_ReflectsTheUnitFileThroughTheServiceManager(t *testing.T) {
	requireLinux(t)
	h := newServiceHost(t)
	calls := installFakeSystemctl(t, "")

	stdout, _, code := runServiceCmd(t, "daemon", "service-status")
	if code != 0 || stdout != "service: not-installed\n" {
		t.Fatalf("no unit: exit %d, stdout %q; want 0 and %q", code, stdout, "service: not-installed\n")
	}

	if err := os.MkdirAll(filepath.Dir(h.unitPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.unitPath(), []byte("# Managed by cenci daemon install-service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runServiceCmd(t, "daemon", "service-status")
	if code != 0 || stdout != "service: enabled\n" {
		t.Fatalf("with unit: exit %d, stdout %q; want 0 and %q", code, stdout, "service: enabled\n")
	}
	// The fake was reached: proves the backend ran rather than a gate returning early.
	if data, err := os.ReadFile(calls); err != nil || !strings.Contains(string(data), "--user show-environment") {
		t.Errorf("the fake systemctl must have been consulted (err=%v, log %q)", err, data)
	}
}

func TestInstallService_AllServiceVerbsExit2WithOneLineHintOnUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		hint string
	}{
		{"service-status rejects --force", []string{"daemon", "service-status", "--force"}, "cenci daemon service-status"},
		{"service-status rejects any flag", []string{"daemon", "service-status", "--bogus"}, "cenci daemon service-status"},
		{"service-status rejects an argument", []string{"daemon", "service-status", "extra"}, "cenci daemon service-status"},
		{"install-service rejects an unknown flag", []string{"daemon", "install-service", "--bogus"}, "cenci daemon install-service"},
		{"install-service rejects an argument", []string{"daemon", "install-service", "extra"}, "cenci daemon install-service"},
		{"uninstall-service rejects an unknown flag", []string{"daemon", "uninstall-service", "--bogus"}, "cenci daemon uninstall-service"},
		{"uninstall-service rejects an argument", []string{"daemon", "uninstall-service", "extra"}, "cenci daemon uninstall-service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newServiceHost(t)
			stdout, stderr, code := runServiceCmd(t, tc.args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2 (stdout %q, stderr %q)", code, stdout, stderr)
			}
			if lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n"); len(lines) != 1 || !strings.Contains(lines[0], tc.hint) {
				t.Errorf("stderr = %q, want one line naming %q", stderr, tc.hint)
			}
			if _, err := os.Stat(h.unitPath()); !errors.Is(err, os.ErrNotExist) {
				t.Error("a usage error must write nothing")
			}
		})
	}
}

// TestInstallService_ForceIsAcceptedOnInstallAndUninstall shows --force parses
// on both mutating verbs: inside the sandbox they get past flag parsing and fail
// with the sandbox message (exit 1), not a usage error (exit 2), and write nothing.
func TestInstallService_ForceIsAcceptedOnInstallAndUninstall(t *testing.T) {
	for _, verb := range []string{"install-service", "uninstall-service"} {
		for _, args := range [][]string{{"daemon", verb}, {"daemon", verb, "--force"}} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				h := newServiceHost(t)
				t.Setenv("CENCI_SANDBOX", "1")

				stdout, stderr, code := runServiceCmd(t, args...)
				if code != 1 {
					t.Errorf("exit code = %d, want 1 (stdout %q, stderr %q)", code, stdout, stderr)
				}
				if !strings.Contains(stderr, "not supported inside the cenci sandbox") {
					t.Errorf("stderr = %q, want the sandbox message", stderr)
				}
				if _, err := os.Stat(h.unitPath()); !errors.Is(err, os.ErrNotExist) {
					t.Error("nothing may be written inside the sandbox")
				}
			})
		}
	}
}

func TestUninstallService_NothingInstalledExitsZeroWithMessage(t *testing.T) {
	requireLinux(t)
	newServiceHost(t)
	calls := installFakeSystemctl(t, "")

	stdout, stderr, code := runServiceCmd(t, "daemon", "uninstall-service")
	if code != 0 || strings.TrimSpace(stdout) != "service not installed" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and %q", code, stdout, stderr, "service not installed")
	}
	if data, err := os.ReadFile(calls); err == nil {
		t.Errorf("nothing installed must make no systemctl calls, got %q", data)
	}
}

func TestInstallService_MissingLauncherExitsOneNamingPath(t *testing.T) {
	requireLinux(t)
	h := newServiceHost(t)
	installFakeSystemctl(t, "")

	stdout, stderr, code := runServiceCmd(t, "daemon", "install-service")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout %q)", code, stdout)
	}
	if want := filepath.Join(h.home, ".local", "bin", "cenci"); !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to name %s", stderr, want)
	}
	if _, err := os.Stat(h.unitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("no unit may be written without a launcher")
	}
}

// TestDaemonRestart_ServiceBranchRestartsThroughTheManager covers the enabled
// service: restart goes through `systemctl --user restart cenci-watch.service`
// (whose fake starts the real daemon detached and records its PID), waits for the
// event socket, prints "daemon restarted", and never takes the Spawn path.
func TestDaemonRestart_ServiceBranchRestartsThroughTheManager(t *testing.T) {
	requireLinux(t)
	h := newServiceHost(t)
	pidRecord := filepath.Join(t.TempDir(), "started.pid")
	calls := installFakeSystemctl(t,
		"'"+binaryPath+"' daemon start </dev/null >/dev/null 2>&1 &\necho $! > '"+pidRecord+"'")
	if err := os.MkdirAll(filepath.Dir(h.unitPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.unitPath(), []byte("# Managed by cenci daemon install-service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidRecord); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	stdout, stderr, code := runServiceCmd(t, "daemon", "restart")
	if code != 0 {
		t.Fatalf("daemon restart: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "daemon restarted") {
		t.Errorf("stdout = %q, want %q", stdout, "daemon restarted")
	}
	if strings.Contains(stdout, "stopped daemon") {
		t.Errorf("the service branch must not stop the daemon itself: %q", stdout)
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "--user restart cenci-watch.service") {
		t.Errorf("systemctl log = %q, want the restart call", log)
	}

	recorded, err := os.ReadFile(pidRecord)
	if err != nil {
		t.Fatalf("the fake restart did not record a pid: %v", err)
	}
	want, err := strconv.Atoi(strings.TrimSpace(string(recorded)))
	if err != nil {
		t.Fatal(err)
	}
	waitForFile(t, ipc.DefaultPIDPath(), 3*time.Second)
	got, err := daemon.ReadPIDFile(ipc.DefaultPIDPath())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("pid file holds %d, want the manager-started daemon %d", got, want)
	}
}

const (
	unitMarker           = "# Managed by cenci daemon install-service"
	installedMessage     = "service installed and started\n"
	alreadyMessage       = "service already installed\n"
	uninstalledMessage   = "service removed; the daemon starts again on your next agent session or with: cenci daemon start\n"
	systemctlDaemonStart = "--user enable --now cenci-watch.service"
	systemctlDaemonStop  = "--user disable --now cenci-watch.service"
)

func readCallLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the systemctl call log: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// serviceWithFakeManager is a host whose fake systemctl starts and stops the
// real daemon on `enable --now` / `disable --now`, like a systemd user manager.
func serviceWithFakeManager(t *testing.T) (h serviceHost, calls string) {
	t.Helper()
	requireLinux(t)
	h = newServiceHost(t)
	pidRecord := filepath.Join(t.TempDir(), "started.pid")
	calls = installFakeSystemctlHooks(t, map[string]string{
		systemctlDaemonStart: startBody(pidRecord),
		systemctlDaemonStop:  stopBody(pidRecord),
	})
	h.writeLauncher(t)
	t.Cleanup(func() { killRecorded(t, pidRecord) })
	return h, calls
}

func TestInstallService_SuccessPrintsMessageAndExitsZero(t *testing.T) {
	h, calls := serviceWithFakeManager(t)

	stdout, stderr, code := runServiceCmd(t, "daemon", "install-service")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout %q, stderr %q)", code, stdout, stderr)
	}
	if stdout != installedMessage {
		t.Errorf("stdout = %q, want %q", stdout, installedMessage)
	}
	unit, err := os.ReadFile(h.unitPath())
	if err != nil {
		t.Fatalf("the unit must be written: %v", err)
	}
	if first, _, _ := strings.Cut(string(unit), "\n"); first != unitMarker {
		t.Errorf("first line = %q, want the marker %q", first, unitMarker)
	}
	want := []string{"--user show-environment", "--user daemon-reload", systemctlDaemonStart}
	if got := readCallLog(t, calls); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("systemctl calls = %q, want %q", got, want)
	}
	if !daemonRunning(t) {
		t.Error("the unit's start must leave a reachable daemon")
	}
}

func TestInstallService_SecondRunReportsAlreadyInstalledKeepsMtimeAndStillEnables(t *testing.T) {
	h, calls := serviceWithFakeManager(t)
	if _, stderr, code := runServiceCmd(t, "daemon", "install-service"); code != 0 {
		t.Fatalf("first install: exit %d, stderr %q", code, stderr)
	}
	past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(h.unitPath(), past, past); err != nil {
		t.Fatal(err)
	}
	before := len(readCallLog(t, calls))

	stdout, stderr, code := runServiceCmd(t, "daemon", "install-service")
	if code != 0 {
		t.Fatalf("second install: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if stdout != alreadyMessage {
		t.Errorf("stdout = %q, want %q", stdout, alreadyMessage)
	}
	if info, err := os.Stat(h.unitPath()); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("an identical unit must not be rewritten (err=%v)", err)
	}
	second := readCallLog(t, calls)[before:]
	if got := strings.Join(second, "\n"); !strings.Contains(got, systemctlDaemonStart) {
		t.Errorf("the second run must still enable --now, got %q", second)
	}
}

// TestUninstallService_SuccessPrintsMessageAndNeverRespawnsTheDaemon covers the
// decided "stops the supervised daemon and does not respawn it": after the
// service manager stops the unit, the CLI makes no start call and no daemon
// reappears.
func TestUninstallService_SuccessPrintsMessageAndNeverRespawnsTheDaemon(t *testing.T) {
	h, calls := serviceWithFakeManager(t)
	if _, stderr, code := runServiceCmd(t, "daemon", "install-service"); code != 0 {
		t.Fatalf("install: exit %d, stderr %q", code, stderr)
	}
	before := len(readCallLog(t, calls))

	stdout, stderr, code := runServiceCmd(t, "daemon", "uninstall-service")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout %q, stderr %q)", code, stdout, stderr)
	}
	if stdout != uninstalledMessage {
		t.Errorf("stdout = %q, want %q", stdout, uninstalledMessage)
	}
	if _, err := os.Stat(h.unitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("the unit file must be removed")
	}
	got := readCallLog(t, calls)[before:]
	want := []string{"--user show-environment", systemctlDaemonStop, "--user daemon-reload"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("systemctl calls = %q, want %q (no enable, start or restart)", got, want)
	}

	deadline := time.Now().Add(3 * time.Second)
	for daemonRunning(t) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if daemonRunning(t) {
		t.Fatal("the unit's stop must have ended the daemon")
	}
	time.Sleep(300 * time.Millisecond)
	if daemonRunning(t) {
		t.Error("uninstall-service must not respawn a daemon")
	}
}

// TestDaemonRestart_ServiceBranchHandsOverFromAnUnsupervisedDaemon covers the
// crash-restart race: a hook spawned a daemon while the unit was inactive, so
// `restart` must stop it first, otherwise the unit's own start would hit the
// already-running path and exit 0.
func TestDaemonRestart_ServiceBranchHandsOverFromAnUnsupervisedDaemon(t *testing.T) {
	requireLinux(t)
	h := newServiceHost(t)
	pidRecord := filepath.Join(t.TempDir(), "started.pid")
	installFakeSystemctlHooks(t, map[string]string{
		"--user is-active cenci-watch.service": "exit 3",
		"--user restart cenci-watch.service":   startBody(pidRecord),
	})
	if err := os.MkdirAll(filepath.Dir(h.unitPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.unitPath(), []byte(unitMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killRecorded(t, pidRecord) })
	old := startDaemonBackground(t)
	oldPID := old.cmd.Process.Pid

	stdout, stderr, code := runServiceCmd(t, "daemon", "restart")
	if code != 0 {
		t.Fatalf("daemon restart: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "daemon restarted") {
		t.Errorf("stdout = %q, want %q", stdout, "daemon restarted")
	}
	select {
	case <-old.done:
	case <-time.After(3 * time.Second):
		t.Fatalf("the unsupervised daemon %d must be stopped before the manager restarts the unit", oldPID)
	}
	data, err := os.ReadFile(pidRecord)
	if err != nil {
		t.Fatalf("the fake restart did not start a daemon: %v", err)
	}
	want, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, readErr := daemon.ReadPIDFile(ipc.DefaultPIDPath())
		if readErr == nil && got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid file holds %d (err %v), want the manager-started daemon %d", got, readErr, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
