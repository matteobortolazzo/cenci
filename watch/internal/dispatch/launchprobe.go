package dispatch

import (
	"context"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// launchProbeFn is the launch-process probe seam (#1292): resolveLaunchVerdicts
// calls it instead of sandbox.ListLaunchProcesses directly so tests never exec
// into a developer's real docker/podman (same idiom as containerSessionProbeFn).
// The default is bounded by sandbox's runRuntime (a per-call timeout plus
// WaitDelay, stderr captured) on top of the pass context.
var launchProbeFn = sandbox.ListLaunchProcesses

// runningContainersFn is the running-container listing seam: after a failed
// probe, one listing per runtime per pass tells a container that is gone or
// stopped from a runtime that could not be queried. Bounded like launchProbeFn.
var runningContainersFn = sandbox.RunningContainerNames

// launchProbePassTimeout bounds the whole probe step (every ticket, every
// listing), on top of the per-call bounds inside sandbox, so many slow
// containers can never stall a reconcile pass. A ticket whose probe overruns it
// gets the unknown verdict and defers. A var only so a test can shorten it;
// mirrors containerSessionProbeTimeout.
var launchProbePassTimeout = 60 * time.Second

// needsLaunchProbe reports whether t's liveness may be decided by the launch-ID
// probe this pass: only the two Working-gated branches of Reconcile consume the
// verdict, and only once they could act on it, so the probe is skipped for a
// ticket without Working, with a terminal label, with no (or an unprovable)
// snapshot, whose open-PR probe is incomplete or that has an open PR, and for
// one with no recorded launch.
func needsLaunchProbe(t Ticket, snap *watch.StateSnapshot) bool {
	if snap == nil || !hasLabel(t.Labels, labelWorking) || hasTerminalLabel(t.Labels) {
		return false
	}
	return t.OpenPRProbe == OpenPRProbeComplete && !t.HasOpenPR && t.Launch != (RecordedLaunch{})
}

// runningListing is one runtime's running-container listing, cached for the
// pass whether it succeeded or not.
type runningListing struct {
	names []string
	err   error
}

// resolveLaunchVerdicts probes each eligible Working ticket's recorded launch
// at most once and returns the verdict per planKey (#1292). A ticket without a
// verdict is judged by Reconcile's window-name rule. It is read-only: it lists
// processes and containers and never signals or kills anything.
//
// Per ticket: a recorded triple that fails re-validation (or is incomplete) is
// never probed -- one log line names the ticket and no value is echoed. The
// probe finding a process is live, finding none is dead, and unreadable
// processes (sandbox.ErrUnreadableProcesses) count as live beside a match but
// cannot prove death alone. Any other probe error consults the runtime's
// running-container listing, fetched once per runtime per pass: a container
// absent from a successful listing is gone or stopped, hence dead; a failed
// listing, or a container that is still running, proves nothing. Everything
// that cannot prove death is unknown, including a ticket reached after the pass
// budget (launchProbePassTimeout) is spent.
func resolveLaunchVerdicts(tickets []Ticket, snap *watch.StateSnapshot, out io.Writer) map[string]LaunchVerdict {
	verdicts := map[string]LaunchVerdict{}
	var eligible []Ticket
	for _, t := range tickets {
		if !needsLaunchProbe(t, snap) {
			continue
		}
		// The state file lives in the bind-mounted repo, so the recorded values
		// are agent-writable: none may reach an exec argv unless it passes the
		// same default-deny checks the close path applies to pane options.
		if !pipeline.ValidLaunchTriple(t.Launch.ID, t.Launch.Container, t.Launch.Runtime) {
			logf(out, "reconcile: #%d recorded launch is invalid or incomplete, judging liveness by window name\n", t.Number)
			continue
		}
		eligible = append(eligible, t)
	}
	if len(eligible) == 0 {
		return verdicts
	}

	ctx, cancel := context.WithTimeout(context.Background(), launchProbePassTimeout)
	defer cancel()
	listings := map[string]runningListing{}
	for _, t := range eligible {
		verdict, why := probeLaunch(ctx, t.Launch, listings)
		switch {
		case verdict == LaunchVerdictUnknown:
			logf(out, "reconcile: #%d launch liveness unknown, deferring: %s\n", t.Number, why)
		case why != "":
			logf(out, "reconcile: #%d %s\n", t.Number, why) // dead, but inferred rather than proven
		}
		verdicts[planKey(t.Repo, t.Number)] = verdict
	}
	return verdicts
}

// probeLaunch judges one validated launch. why explains an unknown verdict, or
// a dead one inferred from a failed probe, for the log; it is empty otherwise.
func probeLaunch(ctx context.Context, l RecordedLaunch, listings map[string]runningListing) (verdict LaunchVerdict, why string) {
	if ctx.Err() != nil {
		return LaunchVerdictUnknown, "probe budget for this pass is spent"
	}
	procs, err := launchProbeFn(ctx, l.Runtime, l.Container, l.ID)
	unreadable := errors.Is(err, sandbox.ErrUnreadableProcesses)
	if err == nil || unreadable {
		switch {
		case len(procs) > 0:
			return LaunchVerdictLive, "" // a match is a match, even beside unreadable processes
		case unreadable:
			return LaunchVerdictUnknown, "no readable process matched and some could not be read: " + boundedErr(err)
		default:
			return LaunchVerdictDead, ""
		}
	}

	// The probe itself failed. If the container is no longer running the launch
	// is dead; if that cannot be established, nothing is proven.
	if ctx.Err() != nil {
		return LaunchVerdictUnknown, "probe budget for this pass is spent: " + boundedErr(err)
	}
	listing, ok := listings[l.Runtime]
	if !ok {
		names, lerr := runningContainersFn(ctx, l.Runtime)
		listing = runningListing{names: names, err: lerr}
		listings[l.Runtime] = listing
	}
	switch {
	case listing.err != nil:
		return LaunchVerdictUnknown, "probe failed (" + boundedErr(err) + ") and running containers could not be listed: " + boundedErr(listing.err)
	case slices.Contains(listing.names, l.Container):
		return LaunchVerdictUnknown, "probe failed although its container is running: " + boundedErr(err)
	default:
		return LaunchVerdictDead, "launch probe failed (" + boundedErr(err) + "), container absent from running list, treating as dead"
	}
}

// boundedErr renders err for one log line: newlines collapsed, length capped.
func boundedErr(err error) string {
	return truncateDetail(collapseLines(err.Error()), maxProbeLogDetailBytes)
}
