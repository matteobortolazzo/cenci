package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// reapLivenessScript is the pre-SIGKILL liveness/identity probe, copied
// VERBATIM from sandbox/cenci-sand's REAP_LIVENESS_SCRIPT: invoked as
// `sh -c "<script>" _ <pid>` (pid passed as an argument, never interpolated).
// Prints the process's *current* start time if alive, the sentinel __GONE__
// if not, or __NOSTAT__ if alive but unverifiable. Always exits 0 if it ran
// at all, so a non-zero exit from the surrounding `exec` means the exec
// transport itself failed — see the escalation loop for the host-side
// classification.
const reapLivenessScript = `pid="$1"; st=$(cat "/proc/$pid/stat" 2>/dev/null); if [ -z "$st" ]; then printf "__GONE__\n"; exit 0; fi; st=${st##*) }; set -- $st; if [ "$#" -ge 20 ]; then shift 19; printf "%s\n" "$1"; else printf "__NOSTAT__\n"; fi; exit 0`

// numericPattern validates start times before trusting a mismatch as genuine
// PID reuse (a forging process can corrupt its own stat field 22).
var numericPattern = regexp.MustCompile(`^[0-9]+$`)

// reapSleep is the SIGTERM→SIGKILL grace sleep, a package var so tests could
// stub it; CENCI_SANDBOX_REAP_GRACE_SECS already tunes the duration to 0 for
// fast test runs, matching the bash suite's usage.
var reapSleep = time.Sleep

// ReapOrphans kills container-side agent processes whose owning tmux pane no
// longer exists on the host. Ownership is recorded via the TMUX_PANE and
// CENCI_TMUX_SOCKET env vars injected into every exec'd session (#1007),
// present in /proc/<pid>/environ of every container-side agent process and
// descendants. A process is live iff its (socket, pane) pair is present in
// that socket's resolved live-pane set — never a union of pane ids across
// sockets, since both a personal default tmux server and the `cenci` server
// allocate pane ids from %0. Every distinct, shape-validated socket a
// container's scan output references is resolved at most once per sweep
// (memoized across containers/runtimes) before any signal is sent in that
// container. A row carrying no socket (pre-#1007 launcher) or a malformed
// one fails open: treated as live, never signaled — a false kill here is
// irreversible loss of in-flight agent work. It is a global sweep (not
// scoped to the current repo) and scans every installed runtime, since
// orphans can exist under either docker or podman regardless of which one
// the launcher prefers.
//
// The scan and per-row classification are shared with the dispatch capacity
// gate's read-only count (sandbox.OrphanScanner, #1218); this function only
// acts on the rows classified sandbox.OrphanDeadPane.
//
// It prints its own "Error:"/"Note:" lines (errors to stderr, matching the
// bash launcher whose retargeted test suite asserts these exact strings);
// the returned error only signals a non-zero exit to the caller.
func ReapOrphans(stdout, stderr io.Writer) error {
	// CENCI_SANDBOX_REAP_GRACE_SECS lets callers tune the SIGTERM → SIGKILL
	// grace window (e.g. to 0 for fast test runs).
	grace := 5.0
	if v := os.Getenv("CENCI_SANDBOX_REAP_GRACE_SECS"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			grace = parsed
		}
	}

	// The scanner memoizes per-socket live-pane resolution across every
	// container/runtime in this sweep (#1007).
	scanner := sandbox.NewOrphanScanner(stdout, stderr)
	ctx := context.Background()

	// ── Runtimes to scan (independent of the single preferred runtime) ──
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "Error: no container runtime (docker/podman) found on PATH.")
		return fmt.Errorf("no container runtime found")
	}

	reapedCount := 0
	for _, rt := range runtimes {
		containers, err := scanner.RunningContainers(ctx, rt)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: failed to list running containers for %s.\n", rt)
			return err
		}

		for _, container := range containers {
			scanned, err := scanner.ScanContainer(ctx, rt, container)
			if err != nil {
				// A tmux failure has already printed its own Error line.
				var scanErr *sandbox.ScanError
				if errors.As(err, &scanErr) {
					_, _ = fmt.Fprintf(stderr, "Error: failed to scan processes in container %s: %s\n", container, scanErr.Stderr)
				}
				return err
			}

			var termPids, termStarts []string
			for _, r := range scanned.Rows {
				pid, pane, start := r.PID, r.Pane, r.Start

				switch r.Class {
				case sandbox.OrphanMalformedPane:
					_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s has a malformed TMUX_PANE value; skipping.\n", pid, container)
					continue
				case sandbox.OrphanContainerInit:
					_, _ = fmt.Fprintf(stdout, "Note: process 1 in container %s is the container init; skipping.\n", container)
					continue
				}
				// Only a dead pane is signaled; no-socket and live rows fail
				// open, and a class unknown here is never signaled either.
				if r.Class != sandbox.OrphanDeadPane {
					continue
				}

				// -u root: `docker run --user X` persists as the container's
				// Config.User for the container's lifetime, not just its
				// initial process, so every exec call site must carry its
				// own explicit -u rather than relying on the container's
				// default user (see sandbox project's CLAUDE.md). Signaling
				// stays root regardless of which user the scan runs as.
				term := exec.Command(rt, "exec", "-u", "root", container, "kill", "-TERM", pid)
				term.Stdout = stdout
				term.Stderr = stderr
				if err := term.Run(); err != nil {
					// A pid can legitimately exit between the scan and the
					// signal. One guaranteed source on containers created by
					// pre-#356 launchers: the scan's own in-container sh
					// inherits the container-lifetime TMUX_PANE (docker/podman
					// exec merges Config.Env), so once that baked pane goes
					// stale the scan reports itself as an orphan — and it has
					// always exited by kill time. Probe before classifying:
					// __GONE__ means the TERM raced an exit, not a failure.
					if start, perr := probeStartTime(rt, container, pid); perr == nil && start == "__GONE__" {
						_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s exited before SIGTERM could be delivered; skipping.\n", pid, container)
						continue
					}
					_, _ = fmt.Fprintf(stderr, "Error: failed to send SIGTERM to pid %s in container %s.\n", pid, container)
					return fmt.Errorf("%s exec kill -TERM: %w", rt, err)
				}
				_, _ = fmt.Fprintf(stdout, "reaped\t%s\t%s\t%s\n", container, pid, pane)
				reapedCount++
				termPids = append(termPids, pid)
				termStarts = append(termStarts, start)
			}

			// (pre-#1007 launcher): the ticket reference stays in this
			// comment, not in the printed string below — a raw GitHub issue
			// number in a user-facing CLI diagnostic is opaque to an operator
			// without repo context.
			if scanned.NoSocketCount > 0 {
				_, _ = fmt.Fprintf(stdout, "Note: %d process(es) in container %s carried no CENCI_TMUX_SOCKET; failing open and treating as live so no in-flight agent work is ever killed.\n", scanned.NoSocketCount, container)
			}

			if scanned.UnreadableCount > 0 {
				_, _ = fmt.Fprintf(stdout, "Note: %d process environ(s) in container %s were unreadable during the -u dev scan; if this persists it may mean the scan user's UID no longer matches the agent process UID, so orphans could go undetected.\n", scanned.UnreadableCount, container)
			}

			if len(termPids) == 0 {
				continue
			}
			reapSleep(time.Duration(grace * float64(time.Second)))
			for i, pid := range termPids {
				recordedStart := termStarts[i]

				// -u root: `docker run --user X` persists as the container's
				// Config.User for the container's lifetime, so this exec
				// call site carries its own explicit -u (see sandbox
				// project's CLAUDE.md). A non-zero exit here means the exec
				// transport itself failed, not "process gone" — a hard
				// error, not swallowed.
				probeStart, err := probeStartTime(rt, container, pid)
				if err != nil {
					_, _ = fmt.Fprintf(stderr, "Error: failed to probe liveness of pid %s in container %s: %v\n", pid, container, err)
					return fmt.Errorf("%s exec liveness probe: %w", rt, err)
				}

				// __GONE__: process already gone by probe time — no-op, not
				// an error.
				if probeStart == "__GONE__" {
					continue
				}

				// A recorded start and a probe start that are both strictly
				// numeric and differ means the pid was reused by an unrelated
				// process during the grace window — treat as already-gone,
				// skip SIGKILL. Both sides must be numeric before trusting a
				// mismatch as genuine PID reuse (a forging process can
				// corrupt its own stat field 22 or shift the scan's TSV
				// columns; both vectors are confined to the forging process's
				// own record, so this is unparseable-value handling, not a
				// security boundary). __NOSTAT__ and an empty recorded start
				// fall back to liveness-only silently; a non-empty but
				// non-numeric value on either side falls back the same way
				// but is noted since it's unexpected.
				recordedNumeric := numericPattern.MatchString(recordedStart)
				probeNumeric := numericPattern.MatchString(probeStart)
				if recordedNumeric && probeNumeric {
					if probeStart != recordedStart {
						continue
					}
				} else if (recordedStart != "" && !recordedNumeric) ||
					(probeStart != "__NOSTAT__" && !probeNumeric) {
					_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s has an unparseable start-time value; proceeding best-effort.\n", pid, container)
				}

				kill := exec.Command(rt, "exec", "-u", "root", container, "kill", "-KILL", pid)
				kill.Stdout = stdout
				kill.Stderr = stderr
				if err := kill.Run(); err != nil {
					_, _ = fmt.Fprintf(stderr, "Error: failed to send SIGKILL to pid %s in container %s.\n", pid, container)
					return fmt.Errorf("%s exec kill -KILL: %w", rt, err)
				}
			}
		}
	}

	if reapedCount > 0 {
		_, _ = fmt.Fprintf(stdout, "Reaped %d orphaned process(es).\n", reapedCount)
	} else {
		_, _ = fmt.Fprintln(stdout, "No orphaned processes found.")
	}
	return nil
}

// probeStartTime runs the in-container liveness/identity probe for pid and
// returns its output: the process's current /proc stat start time, __GONE__
// (process gone), or __NOSTAT__ (alive but unverifiable). Pid is passed as an
// argument to the probe, never interpolated. An error means the exec
// transport itself failed; the probe's stderr is folded into the error text.
func probeStartTime(rt, container, pid string) (string, error) {
	// -u root: `docker run --user X` persists as the container's Config.User
	// for the container's lifetime, so this exec call site carries its own
	// explicit -u (see sandbox project's CLAUDE.md). The probe stays root
	// regardless of which user the scan runs as.
	var probeOut, probeErr bytes.Buffer
	probe := exec.Command(rt, "exec", "-u", "root", container, "sh", "-c", reapLivenessScript, "_", pid)
	probe.Stdout = &probeOut
	probe.Stderr = &probeErr
	if err := probe.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimRight(probeErr.String(), "\n"))
	}
	return strings.TrimRight(probeOut.String(), "\n"), nil
}
