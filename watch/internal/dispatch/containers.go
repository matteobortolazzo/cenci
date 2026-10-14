package dispatch

import (
	"context"
	"io"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// containerSessionProbeFn is the orphan-count seam (#1218): resolveContainerSessions
// calls it instead of defaultContainerSessionProbe directly so tests never exec
// into a developer's real docker/podman (same idiom as runFn).
var containerSessionProbeFn = defaultContainerSessionProbe

// containerSessionProbeTimeout bounds the whole container-session probe (every
// runtime, every container), on top of the per-call bounds inside
// sandbox.CountOrphanedSessions, so many slow containers can never stall a
// dispatch pass. An overrun is an error (the pass fails closed). A var only so
// a test can shorten it.
var containerSessionProbeTimeout = 60 * time.Second

// defaultContainerSessionProbe counts the orphaned container-side agent
// sessions (distinct dead (socket, launch ID) launches, or (socket, pane) pairs
// for processes without a launch ID -- the same classification
// `cenci sandbox reap-orphans` acts on) across every sandbox container on every
// installed runtime. It is read-only and its subprocesses are bounded
// (sandbox.CountOrphanedSessions). The one exclusion is "no container runtime
// on PATH" => (0, nil): there is nowhere for an orphan to live. Every other
// error -- a runtime `ps`/`exec` failure, a tmux query failure, a timeout -- is
// returned, never folded into a zero count (watch/AGENTS.md: narrow a
// match-miss exclusion to the exact case being excluded).
func defaultContainerSessionProbe() (int, error) {
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), containerSessionProbeTimeout)
	defer cancel()
	return sandbox.CountOrphanedSessions(ctx, runtimes, io.Discard, io.Discard)
}

// resolveContainerSessions runs the container-session probe once for the pass
// and maps the result to the (count, status) pair Decide's capacity gate
// consumes (#1218). It probes only when the pass collected at least one ticket,
// so an idle fleet never execs into a container runtime. A probe failure is
// logged with its cause and fails closed (ContainerSessionProbeFailed); it is
// never an error to RunOnce, since a returned error would terminate RunLoop on
// a transient container-runtime outage. A zero count logs nothing. Both log
// lines are prefixed "dispatch: " at column 0 (neither contains the
// lazyboards-reserved " skip:"/" dispatch " substrings, per formatDecision's
// doc comment).
func resolveContainerSessions(tickets []Ticket, out io.Writer) (int, ContainerSessionProbe) {
	if len(tickets) == 0 {
		return 0, ContainerSessionProbeOK
	}
	count, err := containerSessionProbeFn()
	if err != nil {
		logf(out, "dispatch: container session probe failed: %v\n", err)
		return 0, ContainerSessionProbeFailed
	}
	if count > 0 {
		logf(out, "dispatch: container session probe: %d orphaned session(s)\n", count)
	}
	return count, ContainerSessionProbeOK
}
