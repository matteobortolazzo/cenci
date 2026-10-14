// Package windowproc kills the process tree behind a tmux window before the
// window itself is killed (#1253): sandbox panes by launch ID inside their
// container, host panes by the process group of pane_pid.
//
// Every pane value comes from tmux user options, which a process in a container
// can write through the tmux socket, so each is validated default-deny before it
// reaches an exec argv or a signal: a malformed value is a reported error and
// nothing is signaled for that pane.
package windowproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// Process-signal seams. Production uses the real syscalls; the refusal tests
// (pgid <= 1, the caller's own group) replace them so no real group is ever
// signaled by a test that wants to prove nothing was.
var (
	sysKill    = syscall.Kill
	sysGetpgid = syscall.Getpgid
	sysGetpgrp = syscall.Getpgrp
)

// groupPollInterval is how often a host group is re-probed while waiting for it
// to exit after SIGTERM.
const groupPollInterval = 250 * time.Millisecond

// digitsPattern is the shape of a pane_pid: digits only (strconv.Atoi would also
// accept a sign).
var digitsPattern = regexp.MustCompile(`^[0-9]+$`)

// Killer kills the processes behind a tmux window.
type Killer struct {
	stderr io.Writer
}

// New returns a Killer that writes diagnostics to stderr.
func New(stderr io.Writer) *Killer {
	return &Killer{stderr: stderr}
}

// KillWindowProcesses reads target's panes and kills each pane's process tree.
// A window that is already gone is not an error; every other failure (the
// listing, a malformed pane, a signal that could not be delivered) is collected
// per pane and returned together, so one bad pane never shields or blocks its
// siblings. ctx bounds every in-container exec; the host path is local syscalls.
func (k *Killer) KillWindowProcesses(ctx context.Context, target string) error {
	panes, err := tmux.ListWindowPaneProcs(target)
	if errors.Is(err, tmux.ErrWindowGone) {
		return nil
	}
	var errs []error
	if err != nil {
		// Panes that did parse are still killed.
		errs = append(errs, fmt.Errorf("listing panes of %s: %w", target, err))
	}

	grace := sandbox.GraceFromEnv()
	for _, p := range panes {
		if err := k.killPane(ctx, p, grace); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// pane is a tmux pane whose values all passed validation.
type pane struct {
	id  string
	pid int
	// Set only for a sandbox pane (launch ID, container and runtime all recorded).
	launchID, container, runtime string
	// strandedContainer is set for a pane that records a container but no launch
	// ID (a partial tag failure): it takes the host path, which cannot reach the
	// container's processes.
	strandedContainer string
}

func (p pane) sandboxed() bool { return p.launchID != "" }

// parsePane validates every field of p, default-deny. A pane with no launch ID
// takes the host path -- including one with only a container and runtime: the ID
// is written last, so that is a partial tag failure, not a sandbox pane. A
// non-empty malformed value, or a launch ID without its container and runtime,
// is an error.
func parsePane(p tmux.PaneProc) (pane, error) {
	if !sandbox.ValidPaneID(p.PaneID) {
		return pane{}, fmt.Errorf("pane %q: invalid pane_id", p.PaneID)
	}
	// Digits only (Atoi alone would accept a sign), and above 1 so neither
	// Getpgid(0) nor Kill(-0) can resolve to the caller's own group and the init
	// is never a target.
	pid, err := strconv.Atoi(p.PanePID)
	if !digitsPattern.MatchString(p.PanePID) || err != nil || pid <= 1 {
		return pane{}, fmt.Errorf("pane %s: invalid pane_pid %q", p.PaneID, p.PanePID)
	}

	if p.LaunchID != "" && !sandbox.ValidLaunchID(p.LaunchID) {
		return pane{}, fmt.Errorf("pane %s: invalid %s %q", p.PaneID, sandbox.PaneOptionLaunchID, p.LaunchID)
	}
	if p.Container != "" && !sandbox.ValidSandboxContainerName(p.Container) {
		return pane{}, fmt.Errorf("pane %s: invalid %s %q", p.PaneID, sandbox.PaneOptionContainer, p.Container)
	}
	// The launcher records the bare runtime name, so a closed set: a path is
	// never executed.
	if p.Runtime != "" && p.Runtime != "docker" && p.Runtime != "podman" {
		return pane{}, fmt.Errorf("pane %s: invalid %s %q", p.PaneID, sandbox.PaneOptionRuntime, p.Runtime)
	}

	out := pane{id: p.PaneID, pid: pid}
	if p.LaunchID == "" {
		out.strandedContainer = p.Container
		return out, nil
	}
	if p.Container == "" {
		return pane{}, fmt.Errorf("pane %s: %s is set but %s is empty", p.PaneID, sandbox.PaneOptionLaunchID, sandbox.PaneOptionContainer)
	}
	if p.Runtime == "" {
		return pane{}, fmt.Errorf("pane %s: %s is set but %s is empty", p.PaneID, sandbox.PaneOptionLaunchID, sandbox.PaneOptionRuntime)
	}
	out.launchID, out.container, out.runtime = p.LaunchID, p.Container, p.Runtime
	return out, nil
}

func (k *Killer) killPane(ctx context.Context, raw tmux.PaneProc, grace time.Duration) error {
	p, err := parsePane(raw)
	if err != nil {
		return err
	}
	if p.sandboxed() {
		err = sandbox.KillLaunch(ctx, p.runtime, p.container, p.launchID, grace, k.stderr, k.stderr)
	} else {
		err = killHostGroup(ctx, p, grace)
		if p.strandedContainer != "" {
			// Non-fatal: the host group was still signaled above, but the agent
			// itself runs inside the container and no launch ID says which of its
			// processes belong to this pane.
			err = errors.Join(err, fmt.Errorf("%s is set but %s is empty: processes inside container %s were not killed (the orphan reaper covers them once the pane is gone)",
				sandbox.PaneOptionContainer, sandbox.PaneOptionLaunchID, p.strandedContainer))
		}
	}
	if err != nil {
		return fmt.Errorf("pane %s: %w", p.id, err)
	}
	return nil
}

// killHostGroup SIGTERMs the process group of the pane's process, waits up to
// grace for it to exit, then SIGKILLs what is left. Only that one group is
// reached: a process in another session or group (the babysit supervisor runs
// under Setsid) survives by design, and so does a job-controlled shell's
// foreground job, which is a separate group. Group 0 or 1 and the caller's own
// group are refused outright -- reported, never signaled, not even probed.
func killHostGroup(ctx context.Context, p pane, grace time.Duration) error {
	pgid, err := sysGetpgid(p.pid)
	switch {
	case errors.Is(err, syscall.ESRCH):
		// The leader is gone, but its group can outlive it (its children keep it
		// alive, and the kernel never reuses a pid that is still a group id): so
		// the group under the leader's own pid is probed before the pane is called
		// closed. Only ESRCH on both means there is nothing left.
		if !groupAlive(p.pid) {
			return nil
		}
		pgid = p.pid
	case err != nil:
		return fmt.Errorf("getpgid of pane_pid %d: %w", p.pid, err)
	}
	if pgid <= 1 {
		return fmt.Errorf("refusing to signal process group %d of pane_pid %d", pgid, p.pid)
	}
	if pgid == sysGetpgrp() {
		return fmt.Errorf("refusing to signal own process group %d (pane_pid %d)", pgid, p.pid)
	}

	if err := sysKill(-pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("SIGTERM to process group %d: %w", pgid, err)
	}
	awaitGroupExit(ctx, pgid, grace)
	// The liveness check comes right before the SIGKILL so a group that exited
	// on its own is never signaled again.
	if !groupAlive(pgid) {
		return nil
	}
	if err := sysKill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("SIGKILL to process group %d: %w", pgid, err)
	}
	return nil
}

// groupAlive reports whether any process is left in the group. Only ESRCH means
// gone: any other probe error (EPERM) leaves it presumed alive.
func groupAlive(pgid int) bool {
	return !errors.Is(sysKill(-pgid, 0), syscall.ESRCH)
}

// awaitGroupExit polls the group until it is gone, grace elapses or ctx ends.
// Grace <= 0 does not poll at all.
func awaitGroupExit(ctx context.Context, pgid int, grace time.Duration) {
	if grace <= 0 {
		return
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	ticker := time.NewTicker(groupPollInterval)
	defer ticker.Stop()
	for groupAlive(pgid) {
		select {
		case <-ticker.C:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}
