package dispatch

import (
	"context"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// oomProbeResult is one pass's OOM counter reading (#1291). Counters maps a
// "<runtime>/<container>" key to the oom_kill counter read this pass; only
// containers whose read succeeded have an entry, so a missing key means "not
// readable" and never "zero". Candidates maps planKey(repo, number) to the
// container keys a ticket's counter delta may come from: its recorded launch
// container when Ticket.Launch is valid, else every running workload container
// of its repo on every runtime.
type oomProbeResult struct {
	Counters   map[string]int64
	Candidates map[string][]string
}

// oomProbeFn is the whole-probe seam RunReconcileOnce calls (#1291), so tests
// never exec into a developer's real docker/podman: TestMain stubs it to read
// nothing, and oomprobe_test.go drives defaultOOMProbe directly over fakes.
var oomProbeFn = defaultOOMProbe

// oomRuntimesFn and oomKillReadFn are defaultOOMProbe's inner seams: the
// installed runtimes and the bounded single-exec oom_kill read
// (sandbox.ReadContainerFiles under CgroupReadTimeout and MaxCgroupFileBytes).
// The running-container listing reuses launchprobe.go's runningContainersFn.
var (
	oomRuntimesFn = sandbox.AvailableRuntimes
	oomKillReadFn = sandbox.ReadContainerOOMKills
)

// oomProbePassTimeout bounds the whole OOM probe step (every listing, every
// read), on top of the per-call bounds inside sandbox. A container not read
// before it expires counts as unreadable. A var only so a test can shorten it;
// mirrors launchProbePassTimeout.
var oomProbePassTimeout = 60 * time.Second

// oomContainerKey is the "<runtime>/<container>" key the OOM state uses.
func oomContainerKey(runtime, container string) string { return runtime + "/" + container }

// oomTarget is one container the probe may read. A fallback container is a
// target only when its runtime's listing shows it running (or failed), so a
// target that is not running is always some ticket's recorded launch.
type oomTarget struct{ runtime, name string }

// defaultOOMProbe reads the oom_kill counter of each container a non-terminal
// Working ticket may have died in (#1291). Only those tickets can reach the
// failure path, and a running session keeps its container's last-seen value
// fresh up to its death, so a pass with none makes no runtime call at all.
// Every Working ticket is covered, even a live one or one behind a nil
// snapshot: the last-seen value must stay current while the session runs.
//
// A ticket with a valid recorded launch (pipeline.ValidLaunchTriple) has
// exactly that container as its candidate; any other ticket has every running
// workload container of its repo (sandbox.RepoWorkloadContainerNames of the
// resolved directory) on every installed runtime. Each runtime is listed once
// per pass and each container read at most once, however many tickets share
// it, all under one deadline (oomProbePassTimeout).
//
// Failures never return: an unreadable counter only means the ordinary
// budgeted recovery runs, so each is logged once per pass, prefixed
// "reconcile: ", with a bounded cause -- one line for the pass when no runtime
// is installed, else one per unreadable container (a recorded container that
// is not running, a failed listing of its runtime, a failed, timed-out,
// over-long or malformed read, or one not reached before the deadline). A
// fallback container that is not running is simply not a candidate.
func defaultOOMProbe(tickets []Ticket, repos []RepoConfig, out io.Writer) oomProbeResult {
	res := oomProbeResult{Counters: map[string]int64{}, Candidates: map[string][]string{}}
	var eligible []Ticket
	for _, t := range tickets {
		if hasLabel(t.Labels, labelWorking) && !hasTerminalLabel(t.Labels) {
			eligible = append(eligible, t)
		}
	}
	if len(eligible) == 0 {
		return res
	}

	runtimes, err := oomRuntimesFn()
	if err != nil {
		logf(out, "reconcile: OOM classification unavailable: no docker or podman on PATH\n")
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), oomProbePassTimeout)
	defer cancel()
	listings := map[string]runningListing{}
	listing := func(rt string) runningListing {
		if l, ok := listings[rt]; ok {
			return l
		}
		names, err := runningContainersFn(ctx, rt)
		l := runningListing{names: names, err: err}
		listings[rt] = l
		return l
	}

	repoDirs := make(map[string]string, len(repos))
	for _, rc := range repos {
		repoDirs[rc.Repo] = rc.Dir
	}
	resolved := map[string]string{}
	dirFailed := map[string]bool{}

	targets := map[string]oomTarget{}
	addTarget := func(rt, name string) string {
		key := oomContainerKey(rt, name)
		targets[key] = oomTarget{runtime: rt, name: name}
		return key
	}

	for _, t := range eligible {
		ticketKey := planKey(t.Repo, t.Number)
		if pipeline.ValidLaunchTriple(t.Launch.ID, t.Launch.Container, t.Launch.Runtime) {
			res.Candidates[ticketKey] = []string{addTarget(t.Launch.Runtime, t.Launch.Container)}
			continue
		}
		dir, ok := resolved[t.Repo]
		if !ok {
			if dirFailed[t.Repo] {
				continue
			}
			// EvalSymlinks matches the resolved path launcher.ComputeScope
			// slugifies, exactly like the memory probe (repoMemory).
			d, err := filepath.EvalSymlinks(repoDirs[t.Repo])
			if err != nil {
				logf(out, "reconcile: OOM classification unavailable for %q: resolving repo directory: %q\n", t.Repo, err.Error())
				dirFailed[t.Repo] = true
				continue
			}
			resolved[t.Repo], dir = d, d
		}
		for _, name := range sandbox.RepoWorkloadContainerNames(dir) {
			for _, rt := range runtimes {
				l := listing(rt)
				if l.err == nil && !slices.Contains(l.names, name) {
					continue
				}
				res.Candidates[ticketKey] = append(res.Candidates[ticketKey], addTarget(rt, name))
			}
		}
	}

	for _, key := range slices.Sorted(maps.Keys(targets)) {
		tg := targets[key]
		why := ""
		l := listing(tg.runtime)
		switch {
		case l.err != nil:
			why = "running containers could not be listed: " + boundedErr(l.err)
		case !slices.Contains(l.names, tg.name):
			why = "the recorded launch container is not running"
		case ctx.Err() != nil:
			why = "probe budget for this pass is spent"
		default:
			n, err := oomKillReadFn(ctx, tg.runtime, tg.name)
			if err != nil {
				why = boundedErr(err)
				break
			}
			res.Counters[key] = n
			continue
		}
		logf(out, "reconcile: OOM classification unavailable for container %s on %s: %s\n", tg.name, tg.runtime, why)
	}
	return res
}
