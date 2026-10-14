package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// ReapOrphans kills container-side agent processes whose owning tmux pane (or
// launch) no longer exists on the host. Ownership is recorded via the
// TMUX_PANE, CENCI_TMUX_SOCKET and CENCI_LAUNCH_ID env vars injected into every
// exec'd session (#1007, #1252), present in /proc/<pid>/environ of every
// container-side agent process and descendants. A process carrying a launch ID
// is live iff that ID is recorded (@cenci-launch-id) on a live pane of its own
// socket, which reaps an orphan whose pane id was reused by a new session after
// a tmux restart. A process without one (started before launch IDs) is live iff
// its (socket, pane) pair is present in that socket's resolved live-pane set —
// never a union of pane ids across sockets, since both a personal default tmux
// server and the `cenci` server allocate pane ids from %0. Every distinct,
// shape-validated socket a container's scan output references is resolved at
// most once per sweep (memoized across containers/runtimes) before any signal
// is sent in that container. A row carrying no socket (pre-#1007 launcher), a
// malformed one, or a malformed launch ID fails open: treated as live, never
// signaled — a false kill here is irreversible loss of in-flight agent work.
// It is a global sweep (not scoped to the current repo) and scans every
// installed runtime, since
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
	grace := sandbox.GraceFromEnv()

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

			var termed []sandbox.ProcTarget
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
				// Only a dead pane or dead launch is signaled; no-socket,
				// malformed-launch-ID and live rows fail open, and a class
				// unknown here is never signaled either.
				if r.Class != sandbox.OrphanDeadPane {
					continue
				}

				// A pid that exited between the scan and the signal is a Note,
				// not a failure (see sandbox.SignalTerm).
				gone, err := sandbox.SignalTerm(ctx, rt, container, pid, stdout, stderr)
				if err != nil {
					_, _ = fmt.Fprintf(stderr, "Error: failed to send SIGTERM to pid %s in container %s.\n", pid, container)
					return err
				}
				if gone {
					_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s exited before SIGTERM could be delivered; skipping.\n", pid, container)
					continue
				}
				_, _ = fmt.Fprintf(stdout, "reaped\t%s\t%s\t%s\n", container, pid, pane)
				reapedCount++
				termed = append(termed, sandbox.ProcTarget{PID: pid, Start: start})
			}

			// (pre-#1007 launcher): the ticket reference stays in this
			// comment, not in the printed string below — a raw GitHub issue
			// number in a user-facing CLI diagnostic is opaque to an operator
			// without repo context.
			if scanned.NoSocketCount > 0 {
				_, _ = fmt.Fprintf(stdout, "Note: %d process(es) in container %s carried no CENCI_TMUX_SOCKET; failing open and treating as live so no in-flight agent work is ever killed.\n", scanned.NoSocketCount, container)
			}

			if scanned.MalformedLaunchIDCount > 0 {
				_, _ = fmt.Fprintf(stdout, "Note: %d process(es) in container %s carried a malformed CENCI_LAUNCH_ID; failing open and treating as live so no in-flight agent work is ever killed.\n", scanned.MalformedLaunchIDCount, container)
			}

			if scanned.UnreadableCount > 0 {
				_, _ = fmt.Fprintf(stdout, "Note: %d process environ(s) in container %s were unreadable during the -u dev scan; if this persists it may mean the scan user's UID no longer matches the agent process UID, so orphans could go undetected.\n", scanned.UnreadableCount, container)
			}

			if len(termed) == 0 {
				continue
			}
			// Wait out the grace window (stopping early once every pid is
			// gone), then SIGKILL whatever is still the same live process.
			// With a zero grace window the wait issues no probes at all.
			sandbox.AwaitExit(ctx, rt, container, termed, grace)
			if err := sandbox.KillSurvivors(ctx, rt, container, termed, stdout, stderr); err != nil {
				return err
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
