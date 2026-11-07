// Package autostart installs, removes, inspects and restarts the login service
// that keeps the cenci daemon running: a systemd user unit on Linux and a
// launchd agent on macOS (#1168).
//
// It is a stdlib-only leaf. Everything environmental is injected through
// Options (the OS, home directory, environment, uid, the embedded templates and
// the daemon-control seam), so the launchd backend is exercised on Linux CI and
// the handover ordering is tested without a real daemon.
package autostart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// State is the closed set of service states `cenci daemon service-status`
// reports. Its Line is the stable first-line token that `cenci doctor` matches.
type State string

const (
	StateEnabled      State = "enabled"
	StateDisabled     State = "disabled"
	StateNotInstalled State = "not-installed"
	StateUnsupported  State = "unsupported"
)

// Line renders the `service: <state>` token line.
func (s State) Line() string { return "service: " + string(s) }

// Outcome says what an Install or Uninstall did.
type Outcome string

const (
	OutcomeInstalled        Outcome = "installed"
	OutcomeAlreadyInstalled Outcome = "already-installed"
	OutcomeRemoved          Outcome = "removed"
	OutcomeNotInstalled     Outcome = "not-installed"
)

// Backend is one platform's login-service implementation.
type Backend interface {
	// Status never fails: it answers StateUnsupported on any doubt.
	Status(ctx context.Context) State
	// Probe is the restart-only read of the same state. It reports a definitive
	// read with a nil error, and a doubtful one as StateUnsupported plus a
	// *ProbeError: a cenci unit or plist exists but the service manager did not
	// answer cleanly (a timed-out command, or no user bus on a systemd-booted
	// host). Restart must not guess a state from a doubtful read.
	Probe(ctx context.Context) (State, error)
	// Install writes the marked unit or plist, hands over from an unsupervised
	// daemon, and enables and starts the service. It writes nothing when a
	// check made before the file is written fails (sandbox, unsupported host,
	// missing launcher, an unmarked file without force, a rejected environment
	// value). A later failure (the daemon-reload, the handover Stop, enable
	// --now) can leave the written file on disk with nothing enabled.
	Install(ctx context.Context, force bool) (Outcome, error)
	// Uninstall disables, stops and removes the service. An unmarked file is
	// only removed with force.
	Uninstall(ctx context.Context, force bool) (Outcome, error)
	// Restart restarts the daemon through the service manager, first stopping a
	// running daemon the service does not own (the same handover as Install).
	Restart(ctx context.Context) error
}

// ProbeError is the doubt Probe reports: the read of the service manager was not
// clean while a cenci service file exists, so the service may well be enabled.
type ProbeError struct {
	// Reason is one line saying what went wrong, for example that a command timed out.
	Reason string
	// Remedy is the manual command that restarts the service through its manager.
	Remedy string
}

func (e *ProbeError) Error() string { return e.Reason }

// Templates carries the embedded service templates; package main embeds them
// because //go:embed cannot reach outside its own module directory.
type Templates struct {
	Systemd string
	Launchd string
}

// Daemon is the seam onto daemon control. Both fields may be nil, which reads as
// "no daemon is ever running".
type Daemon struct {
	// Alive reports whether the daemon's event socket answers.
	Alive func() bool
	// Stop stops the running daemon.
	Stop func() error
}

// Options injects everything the backends read from the host.
type Options struct {
	// GOOS selects the backend: "linux" (systemd), "darwin" (launchd); anything
	// else is unsupported.
	GOOS string
	// Home is the user's home directory.
	Home string
	// LookupEnv reads the invoking environment (os.LookupEnv in production).
	LookupEnv func(string) (string, bool)
	// UID is the user id for the launchd gui/<uid> domain.
	UID       int
	Templates Templates
	Daemon    Daemon
	// ReadyTimeout bounds the wait for the event socket after the service was
	// started; zero means DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// CommandTimeout bounds each service-manager command; zero means
	// DefaultCommandTimeout.
	CommandTimeout time.Duration
	// SystemdBootedDir is the directory whose presence says the host was booted
	// with systemd; zero means /run/systemd/system.
	SystemdBootedDir string
}

const (
	// DefaultReadyTimeout is how long Install waits for the event socket.
	DefaultReadyTimeout = 3 * time.Second
	// DefaultCommandTimeout bounds each service-manager command.
	DefaultCommandTimeout = 10 * time.Second
	// defaultSystemdBootedDir is how systemd itself tells a booted host apart.
	defaultSystemdBootedDir = "/run/systemd/system"
)

const (
	// SystemdUnit is the systemd user unit name.
	SystemdUnit = "cenci-watch.service"
	// LaunchdLabel is the launchd agent label: reverse DNS of the GitHub repo.
	LaunchdLabel = "io.github.matteobortolazzo.cenci-watch"

	markerText       = "Managed by cenci daemon install-service"
	systemdMarker    = "# " + markerText
	launchdMarker    = "<!-- " + markerText + " -->"
	commandWaitGrace = 2 * time.Second
	readyPoll        = 50 * time.Millisecond
)

// envNames are the variables captured into the service, in render order. PATH
// is always captured (the daemon shells out to tmux, gh and docker/podman); the
// others only when set, since the socket directory resolves from them.
var envNames = []string{"PATH", "CENCI_SOCKET_DIR", "XDG_STATE_HOME", "XDG_CONFIG_HOME"}

// New returns the backend for opts.GOOS, or a stub whose Status is unsupported
// and whose mutating verbs fail with the reason: inside the cenci sandbox, on an
// unknown OS, or when the home directory is unknown.
func New(opts Options) Backend {
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.LookupEnv
	}
	if v, _ := opts.LookupEnv("CENCI_SANDBOX"); v == "1" {
		return unsupported{reason: errors.New("not supported inside the cenci sandbox")}
	}
	if opts.Home == "" {
		return unsupported{reason: errors.New("cannot determine the home directory")}
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = DefaultReadyTimeout
	}
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = DefaultCommandTimeout
	}
	if opts.SystemdBootedDir == "" {
		opts.SystemdBootedDir = defaultSystemdBootedDir
	}
	switch opts.GOOS {
	case "linux":
		return &systemd{opts}
	case "darwin":
		return &launchd{opts}
	default:
		return unsupported{reason: fmt.Errorf("login service is not supported on %s", opts.GOOS)}
	}
}

// unsupported is the backend for hosts with no usable service manager.
type unsupported struct{ reason error }

func (u unsupported) Status(context.Context) State { return StateUnsupported }

func (u unsupported) Probe(context.Context) (State, error) { return StateUnsupported, nil }

func (u unsupported) Install(context.Context, bool) (Outcome, error) { return "", u.reason }

func (u unsupported) Uninstall(context.Context, bool) (Outcome, error) { return "", u.reason }

func (u unsupported) Restart(context.Context) error { return u.reason }

type envVar struct{ name, value string }

// captureEnvironment reads the variables the service must carry and rejects any
// value that cannot be written into a unit or plist without quoting surprises.
func captureEnvironment(lookup func(string) (string, bool)) ([]envVar, error) {
	var vars []envVar
	for _, name := range envNames {
		value, ok := lookup(name)
		if !ok || value == "" {
			continue
		}
		if strings.ContainsAny(value, "\n\r\"\\") {
			return nil, fmt.Errorf("environment variable %s contains a newline, double quote or backslash and cannot be written into the service file", name)
		}
		vars = append(vars, envVar{name, value})
	}
	return vars, nil
}

// launcherPath is the stable launcher the service starts; the bootstrap hook
// relinks it on every version bump.
func launcherPath(home string) string {
	return filepath.Join(home, ".local", "bin", "cenci")
}

func checkLauncher(home string) error {
	path := launcherPath(home)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s not found; install cenci first so the service has a launcher to start", path)
	}
	return nil
}

// hasMarker reports whether content carries the marker line as its first line
// (systemd) or on the line after the XML declaration (plist).
func hasMarker(content, markerLine string) bool {
	lines := strings.SplitN(content, "\n", 3)
	for _, line := range lines[:min(2, len(lines))] {
		if strings.TrimRight(line, "\r") == markerLine {
			return true
		}
	}
	return false
}

// prepareFile applies the marker and --force rule to the file at path and
// writes content when it differs. changed is false only when an identical marked
// file was already there. Nothing is written when it returns an error.
func prepareFile(path, content, markerLine string, force bool) (changed bool, err error) {
	existing, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		if !hasMarker(string(existing), markerLine) && !force {
			return false, fmt.Errorf("%s exists and was not written by cenci; use --force to replace it", path)
		}
		if string(existing) == content {
			return false, nil
		}
	case !errors.Is(readErr, os.ErrNotExist):
		return false, fmt.Errorf("reading %s: %w", path, readErr)
	}
	return true, writeAtomic(path, content)
}

// checkRemovable fails when path exists without the marker and force is unset.
// It reports whether the file exists at all.
func checkRemovable(path, markerLine string, force bool) (exists bool, err error) {
	existing, readErr := os.ReadFile(path)
	if errors.Is(readErr, os.ErrNotExist) {
		return false, nil
	}
	if readErr != nil {
		return true, fmt.Errorf("reading %s: %w", path, readErr)
	}
	if !hasMarker(string(existing), markerLine) && !force {
		return true, fmt.Errorf("%s was not written by cenci; use --force to remove it", path)
	}
	return true, nil
}

// writeAtomic writes content to path (0644) through a temp file and a rename, so
// a reader never sees a partial unit.
func writeAtomic(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cenci-watch-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// run executes one service-manager command bounded by timeout and, on failure,
// returns an error that names the command and carries its output.
func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = commandWaitGrace
	out, err := cmd.CombinedOutput()
	ctxErr := ctx.Err()
	text := strings.TrimSpace(string(out))
	if err == nil {
		return text, nil
	}
	if ctxErr != nil || errors.Is(err, exec.ErrWaitDelay) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	cmdline := name + " " + strings.Join(args, " ")
	if text != "" {
		return text, fmt.Errorf("%s failed: %w: %s", cmdline, err, text)
	}
	return text, fmt.Errorf("%s failed: %w", cmdline, err)
}

// doubt builds the ProbeError for a failed command. A timeout names the command
// and the bound; any other failure carries the command's own error text. The
// reason is collapsed to one line so a refusal stays a single stderr line.
func (o Options) doubt(cmdline string, err error, remedy string) *ProbeError {
	reason := strings.Join(strings.Fields(err.Error()), " ")
	if timedOut(err) {
		reason = fmt.Sprintf("%s timed out after %s", cmdline, o.CommandTimeout)
	}
	return &ProbeError{Reason: reason, Remedy: remedy}
}

// timedOut reports whether err is a command that ran out of time, as run marks it.
func timedOut(err error) bool { return errors.Is(err, context.DeadlineExceeded) }

func (o Options) daemonAlive() bool { return o.Daemon.Alive != nil && o.Daemon.Alive() }

// handover stops a daemon that is running without supervision, so the service's
// own start does not hit the already-running path, exit 0 and go inactive.
// supervised reports whether the service manager already owns that daemon; it is
// only consulted when a daemon is alive.
func (o Options) handover(supervised func() bool) error {
	if !o.daemonAlive() || supervised() {
		return nil
	}
	if o.Daemon.Stop == nil {
		return nil
	}
	if err := o.Daemon.Stop(); err != nil {
		return fmt.Errorf("stopping the running daemon before the service takes over: %w", err)
	}
	return nil
}

// waitReady polls for the daemon's event socket after the service was started.
func (o Options) waitReady() error {
	if o.Daemon.Alive == nil {
		return nil
	}
	deadline := time.Now().Add(o.ReadyTimeout)
	for {
		if o.Daemon.Alive() {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the service was started but the daemon did not become reachable within %s", o.ReadyTimeout)
		}
		time.Sleep(readyPoll)
	}
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
