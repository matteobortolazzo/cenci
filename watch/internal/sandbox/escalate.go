package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// livenessScript is the pre-SIGKILL liveness/identity probe, copied VERBATIM
// from sandbox/cenci-sand's REAP_LIVENESS_SCRIPT: invoked as
// `sh -c "<script>" _ <pid>` (pid passed as an argument, never interpolated).
// Prints the process's *current* start time if alive, the sentinel __GONE__
// if not, or __NOSTAT__ if alive but unverifiable. Always exits 0 if it ran
// at all, so a non-zero exit from the surrounding `exec` means the exec
// transport itself failed -- see KillSurvivors for the host-side
// classification.
const livenessScript = `pid="$1"; st=$(cat "/proc/$pid/stat" 2>/dev/null); if [ -z "$st" ]; then printf "__GONE__\n"; exit 0; fi; st=${st##*) }; set -- $st; if [ "$#" -ge 20 ]; then shift 19; printf "%s\n" "$1"; else printf "__NOSTAT__\n"; fi; exit 0`

// numericPattern validates start times before trusting a mismatch as genuine
// PID reuse (a forging process can corrupt its own stat field 22).
var numericPattern = regexp.MustCompile(`^[0-9]+$`)

const (
	goneSentinel   = "__GONE__"
	noStatSentinel = "__NOSTAT__"

	// defaultGrace is the TERM -> KILL grace window when
	// CENCI_SANDBOX_REAP_GRACE_SECS is unset or unparseable.
	defaultGrace = 5 * time.Second
	// maxGrace caps CENCI_SANDBOX_REAP_GRACE_SECS so a huge value can neither
	// overflow time.Duration nor stretch the close path's off-loop timeout
	// (grace plus a margin) without bound.
	maxGrace = time.Hour
	// pollInterval is how often AwaitExit re-probes the processes it waits on.
	pollInterval = 250 * time.Millisecond

	// CloseKillMargin is the headroom added to the grace window (GraceFromEnv) to
	// bound one close-path process kill: it covers the runtime listing and signal
	// execs around the wait. Shared by `cenci close` and the daemon's off-loop
	// pending-close kill so the two bounds cannot drift apart.
	CloseKillMargin = 60 * time.Second
)

// ProcTarget is one in-container process an escalation signals: its PID and the
// /proc start time recorded when it was first seen (the PID-reuse guard).
type ProcTarget struct {
	PID, Start string
}

// sleep and now are the escalation's time seams, so tests drive the poll loop
// with a fake clock instead of waiting out real grace windows.
var (
	sleep = time.Sleep
	now   = time.Now
)

// GraceFromEnv returns the TERM -> KILL grace window from
// CENCI_SANDBOX_REAP_GRACE_SECS: default 5s, an unparseable (or non-finite)
// value falls back to the default, negative values clamp to 0, and anything
// above maxGrace clamps to it. The one knob shared by the orphan reaper and the
// close-path kill.
func GraceFromEnv() time.Duration {
	v := os.Getenv("CENCI_SANDBOX_REAP_GRACE_SECS")
	if v == "" {
		return defaultGrace
	}
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(secs) || math.IsInf(secs, 0) {
		return defaultGrace
	}
	switch {
	case secs <= 0:
		return 0
	case secs >= maxGrace.Seconds():
		return maxGrace
	}
	return time.Duration(secs * float64(time.Second))
}

// runRuntime runs `<rt> <args...>` bounded by containerCmdTimeout (on top of
// ctx) and containerCmdWaitDelay, so a hung runtime daemon can never stall an
// escalation indefinitely. A timeout comes back joined with
// context.DeadlineExceeded.
func runRuntime(ctx context.Context, rt string, stdout, stderr io.Writer, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, containerCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, rt, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = containerCmdWaitDelay
	if err := cmd.Run(); err != nil {
		return classifyExecTimeout(cctx, err)
	}
	return nil
}

// probeStartTime runs the in-container liveness/identity probe for pid and
// returns its output: the process's current /proc stat start time, __GONE__
// (process gone), or __NOSTAT__ (alive but unverifiable). Pid is passed as an
// argument to the probe, never interpolated. An error means the exec
// transport itself failed; the probe's stderr is folded into the error text.
func probeStartTime(ctx context.Context, rt, container, pid string) (string, error) {
	// -u root: `docker run --user X` persists as the container's Config.User
	// for the container's lifetime, so this exec call site carries its own
	// explicit -u (see sandbox project's CLAUDE.md). The probe stays root
	// regardless of which user the scan runs as.
	var probeOut, probeErr bytes.Buffer
	if err := runRuntime(ctx, rt, &probeOut, &probeErr, "exec", "-u", "root", container, "sh", "-c", livenessScript, "_", pid); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimRight(probeErr.String(), "\n"))
	}
	return strings.TrimRight(probeOut.String(), "\n"), nil
}

// SignalTerm sends SIGTERM to pid in container. gone reports a pid that
// legitimately exited between the caller's scan and the signal (the kill failed
// and the liveness probe says __GONE__): not a failure, and the caller decides
// whether to say so. Any other failure comes back as err. kill's own
// stdout/stderr stream to the given writers.
//
// A pid can legitimately exit between the scan and the signal. One guaranteed
// source on containers created by pre-#356 launchers: the scan's own
// in-container sh inherits the container-lifetime TMUX_PANE (docker/podman exec
// merges Config.Env), so once that baked pane goes stale the scan reports itself
// as an orphan -- and it has always exited by kill time. Hence the probe before
// classifying.
func SignalTerm(ctx context.Context, rt, container, pid string, stdout, stderr io.Writer) (gone bool, err error) {
	// -u root: `docker run --user X` persists as the container's Config.User for
	// the container's lifetime, not just its initial process, so every exec call
	// site must carry its own explicit -u rather than relying on the
	// container's default user (see sandbox project's CLAUDE.md). Signaling
	// stays root regardless of which user the scan runs as.
	termErr := runRuntime(ctx, rt, stdout, stderr, "exec", "-u", "root", container, "kill", "-TERM", pid)
	if termErr == nil {
		return false, nil
	}
	if start, perr := probeStartTime(ctx, rt, container, pid); perr == nil && start == goneSentinel {
		return true, nil
	}
	return false, fmt.Errorf("%s exec kill -TERM: %w", rt, termErr)
}

// probeVerdict compares a target's recorded start time with what the liveness
// probe just reported. alive is false when the process is gone, or when the pid
// was reused by an unrelated process during the grace window (both start times
// strictly numeric and different). Both sides must be numeric before trusting a
// mismatch as genuine PID reuse (a forging process can corrupt its own stat
// field 22; the scan sanitizes the pane, socket and launch-ID fields so a
// hostile value can no longer shift the TSV columns, and any corruption is
// confined to the forging process's own record, so this is unparseable-value
// handling, not a security boundary). __NOSTAT__ and an empty recorded start
// fall back to liveness-only silently; a non-empty but non-numeric value on
// either side falls back the same way and is flagged unparseable since it is
// unexpected.
func probeVerdict(recorded, probe string) (alive, unparseable bool) {
	if probe == goneSentinel {
		return false, false
	}
	recordedNumeric := numericPattern.MatchString(recorded)
	probeNumeric := numericPattern.MatchString(probe)
	if recordedNumeric && probeNumeric {
		return probe == recorded, false
	}
	unparseable = (recorded != "" && !recordedNumeric) || (probe != noStatSentinel && !probeNumeric)
	return true, unparseable
}

// AwaitExit polls the liveness probe after SIGTERM until every target is gone
// (or its start time changed) or grace elapses, so a prompt exit does not wait
// out the whole window. Each round probes the remaining targets in order and
// stops at the first one still alive. Grace <= 0 issues no probes. A probe error
// ends the wait: it is not a verdict, and KillSurvivors reports it.
func AwaitExit(ctx context.Context, rt, container string, targets []ProcTarget, grace time.Duration) {
	if grace <= 0 {
		return
	}
	deadline := now().Add(grace)
	remaining := targets
	for {
		for len(remaining) > 0 {
			probe, err := probeStartTime(ctx, rt, container, remaining[0].PID)
			if err != nil {
				return
			}
			if alive, _ := probeVerdict(remaining[0].Start, probe); alive {
				break
			}
			remaining = remaining[1:]
		}
		if len(remaining) == 0 {
			return
		}
		left := deadline.Sub(now())
		if left <= 0 {
			return
		}
		sleep(min(pollInterval, left))
	}
}

// KillSurvivors SIGKILLs every target that is still the same live process: a
// liveness probe first (a gone pid or one reused by an unrelated process is
// skipped), then the signal. A SIGKILL that fails is re-probed, like SignalTerm's
// failed SIGTERM: a process that exited in the window since the liveness probe
// is benign and the pass carries on. Any other failure stops the pass, printing
// the "Error:" line the reaper's bash-pinned contract expects; the returned
// error then also says how many later targets were never signaled.
func KillSurvivors(ctx context.Context, rt, container string, targets []ProcTarget, stdout, stderr io.Writer) error {
	for i, t := range targets {
		// -u root on every exec call site, as in SignalTerm. A non-zero exit from
		// the probe means the exec transport itself failed, not "process gone" --
		// a hard error, not swallowed.
		probe, err := probeStartTime(ctx, rt, container, t.PID)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: failed to probe liveness of pid %s in container %s: %v\n", t.PID, container, err)
			return fmt.Errorf("%s exec liveness probe: %w", rt, err)
		}
		alive, unparseable := probeVerdict(t.Start, probe)
		if !alive {
			continue
		}
		if unparseable {
			_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s has an unparseable start-time value; proceeding best-effort.\n", t.PID, container)
		}

		if err := runRuntime(ctx, rt, stdout, stderr, "exec", "-u", "root", container, "kill", "-KILL", t.PID); err != nil {
			if after, perr := probeStartTime(ctx, rt, container, t.PID); perr == nil && after == goneSentinel {
				continue
			}
			_, _ = fmt.Fprintf(stderr, "Error: failed to send SIGKILL to pid %s in container %s.\n", t.PID, container)
			if unsignaled := len(targets) - i - 1; unsignaled > 0 {
				return fmt.Errorf("%s exec kill -KILL: %w (%d further target(s) not signaled)", rt, err, unsignaled)
			}
			return fmt.Errorf("%s exec kill -KILL: %w", rt, err)
		}
	}
	return nil
}
