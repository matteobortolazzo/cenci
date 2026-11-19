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
// malformed one, a socket whose tmux query failed, or a malformed launch ID
// fails open: treated as live, never signaled — a false kill here is
// irreversible loss of in-flight agent work. A socket failure is scoped to the
// rows carrying that socket: its "Error:" line is printed once, the sweep
// continues with the other sockets, containers and runtimes (a failed socket is
// never dialed twice), and after the summary the returned error joins every
// distinct failed socket's error so the command still exits non-zero. A scan
// failure, a signal failure or a caller context that ends still aborts the
// sweep at once, joined with the socket errors collected so far.
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
// the returned error only signals a non-zero exit to the caller. The summary
// prints "Reaped N orphaned process(es)." when N > 0 and "No orphaned processes
// found." only when nothing was reaped and no socket failed (a failed socket's
// rows are unknown, not absent).
func ReapOrphans(stdout, stderr io.Writer) error {
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "Error: no container runtime (docker/podman) found on PATH.")
		return fmt.Errorf("no container runtime found")
	}
	return reapOrphans(context.Background(), runtimes, "", stdout, stderr)
}

// onlyContainer scopes an attach-time sweep to the container being reopened.
// The explicit reap command leaves it empty to scan all sandbox containers.
func reapOrphans(ctx context.Context, runtimes []string, onlyContainer string, stdout, stderr io.Writer) error {
	// CENCI_SANDBOX_REAP_GRACE_SECS lets callers tune the SIGTERM → SIGKILL
	// grace window (e.g. to 0 for fast test runs).
	grace := sandbox.GraceFromEnv()

	// The scanner memoizes per-socket live-pane resolution across every
	// container/runtime in this sweep (#1007).
	scanner := sandbox.NewOrphanScanner(stdout, stderr)

	// socketErrs collects one error per distinct failed socket across every
	// container/runtime; the sweep keeps going past them and returns them joined
	// after the summary. A socket's own "Error:" line was already printed by the
	// scanner.
	var socketErrs []error
	failedSockets := map[string]bool{}
	// abort ends the sweep on an error that is not a socket failure, keeping the
	// socket errors collected so far visible alongside it.
	abort := func(err error) error {
		if len(socketErrs) == 0 {
			return err
		}
		return errors.Join(append(socketErrs, err)...)
	}

	reapedCount := 0
	for _, rt := range runtimes {
		containers := []string{onlyContainer}
		if onlyContainer == "" {
			var err error
			containers, err = scanner.RunningContainers(ctx, rt)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "Error: failed to list running containers for %s.\n", rt)
				return abort(err)
			}
		}

		for _, container := range containers {
			scanned, err := scanner.ScanContainer(ctx, rt, container)
			// Collected before the error branch: a context that ends mid-scan
			// still returns the failures recorded before it, and abort must see
			// them (their Error: lines are already printed).
			for _, f := range scanned.SocketFailures {
				if !failedSockets[f.Socket] {
					failedSockets[f.Socket] = true
					socketErrs = append(socketErrs, f.Err)
				}
			}
			if err != nil {
				var scanErr *sandbox.ScanError
				if errors.As(err, &scanErr) {
					_, _ = fmt.Fprintf(stderr, "Error: failed to scan processes in container %s: %s\n", container, scanErr.Stderr)
				}
				return abort(err)
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
				// unresolved-socket, malformed-launch-ID and live rows fail open,
				// and a class unknown here is never signaled either.
				if r.Class != sandbox.OrphanDeadPane {
					continue
				}

				// A pid that exited between the scan and the signal is a Note,
				// not a failure (see sandbox.SignalTerm).
				gone, err := sandbox.SignalTerm(ctx, rt, container, pid, stdout, stderr)
				if err != nil {
					_, _ = fmt.Fprintf(stderr, "Error: failed to send SIGTERM to pid %s in container %s.\n", pid, container)
					return abort(err)
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
				return abort(err)
			}
		}
	}

	// A failed socket's rows are unknown, not absent: with nothing reaped, the
	// no-op summary would claim a clean sweep, so it is only for a sweep where
	// no socket failed.
	if reapedCount > 0 {
		_, _ = fmt.Fprintf(stdout, "Reaped %d orphaned process(es).\n", reapedCount)
	} else if len(socketErrs) == 0 {
		_, _ = fmt.Fprintln(stdout, "No orphaned processes found.")
	}
	return errors.Join(socketErrs...)
}
