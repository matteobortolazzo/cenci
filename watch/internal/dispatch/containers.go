package dispatch

import (
	"context"
	"io"
	"math/big"
	"path/filepath"
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

// memoryProbeFn is the memory-headroom seam (#1290): resolveMemoryHeadroom
// calls it instead of defaultMemoryProbe directly so tests never exec into a
// developer's real docker/podman.
var memoryProbeFn = defaultMemoryProbe

// memoryProbeTimeout bounds the whole memory probe (every runtime, every
// container). A var only so a test can shorten it.
var memoryProbeTimeout = 60 * time.Second

// memoryContainer identifies one workload container on one runtime: the unit
// the probe reads at most once per pass.
type memoryContainer struct{ runtime, name string }

// memoryReading is one container's cached cgroup read (or the error that
// failed it).
type memoryReading struct {
	current, limit int64
	unlimited      bool
	err            error
}

// defaultMemoryProbe reads the cgroup memory of each repo's running workload
// containers (<agent>-cenci-<slug of the repo dir's resolved basename>, one per
// supported agent, on every installed runtime) and reduces them to the repo's
// most-loaded limited container (#1290). It lists each runtime's running
// containers once, reads each matched container at most once per pass (a
// cache keyed by runtime and name, so repos whose slugs collide share one
// exec), and runs every subprocess sequentially under one overall deadline
// (memoryProbeTimeout): once it passes, every later exec fails at once, so only
// repos whose read had not finished fail closed. Nothing is cached across
// passes.
//
// Failure semantics, all logged and never returned: a failed `ps` on any
// runtime means no repo's container set can be trusted, so every repo fails
// closed; a failed read (or an unresolvable directory) fails only that repo.
// "No docker/podman on PATH" and "no running container" are not failures: the
// repo gets the permissive zero RepoMemory. A healthy limited reading logs
// nothing; an unlimited container logs one line. Every line starts with
// "dispatch: " and carries neither " skip:" nor " dispatch ": untrusted text
// (runtime stderr) is already quoted and defused by the sandbox package, and
// the repo name is %q-quoted.
func defaultMemoryProbe(repos []RepoConfig, out io.Writer) map[string]RepoMemory {
	ctx, cancel := context.WithTimeout(context.Background(), memoryProbeTimeout)
	defer cancel()

	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		return uniformRepoMemory(repos, RepoMemory{})
	}

	running := make(map[memoryContainer]bool)
	for _, rt := range runtimes {
		names, err := sandbox.RunningContainerNames(ctx, rt)
		if err != nil {
			logf(out, "dispatch: memory probe failed: listing running containers: %v\n", err)
			return uniformRepoMemory(repos, RepoMemory{Probe: MemoryProbeFailed})
		}
		for _, name := range names {
			running[memoryContainer{runtime: rt, name: name}] = true
		}
	}

	readings := make(map[memoryContainer]memoryReading)
	read := func(c memoryContainer) memoryReading {
		if r, ok := readings[c]; ok {
			return r
		}
		var r memoryReading
		r.current, r.limit, r.unlimited, r.err = sandbox.ReadContainerMemory(ctx, c.runtime, c.name)
		if r.err == nil && r.unlimited {
			logf(out, "dispatch: memory probe: container %s on %s is unlimited (memory.max is max), not gated\n", c.name, c.runtime)
		}
		readings[c] = r
		return r
	}

	result := make(map[string]RepoMemory, len(repos))
	for _, rc := range repos {
		result[rc.Repo] = repoMemory(rc, runtimes, running, read, out)
	}
	return result
}

// uniformRepoMemory gives every repo the same reading: the probe's
// whole-fleet outcomes (no runtime installed, or a runtime listing failed).
func uniformRepoMemory(repos []RepoConfig, mem RepoMemory) map[string]RepoMemory {
	result := make(map[string]RepoMemory, len(repos))
	for _, rc := range repos {
		result[rc.Repo] = mem
	}
	return result
}

// repoMemory reduces one repo's running workload containers to its
// most-loaded limited reading. A directory that cannot be resolved or a failed
// read fails the repo closed.
func repoMemory(rc RepoConfig, runtimes []string, running map[memoryContainer]bool, read func(memoryContainer) memoryReading, out io.Writer) RepoMemory {
	// EvalSymlinks matches `git rev-parse --show-toplevel`'s resolved path, the
	// directory launcher.ComputeScope slugifies for a session started here.
	dir, err := filepath.EvalSymlinks(rc.Dir)
	if err != nil {
		logf(out, "dispatch: memory probe failed for %q: resolving repo directory: %q\n", rc.Repo, err.Error())
		return RepoMemory{Probe: MemoryProbeFailed}
	}

	var worst RepoMemory
	for _, name := range sandbox.RepoWorkloadContainerNames(dir) {
		for _, rt := range runtimes {
			c := memoryContainer{runtime: rt, name: name}
			if !running[c] {
				continue
			}
			r := read(c)
			if r.err != nil {
				logf(out, "dispatch: memory probe failed for %q: %v\n", rc.Repo, r.err)
				return RepoMemory{Probe: MemoryProbeFailed}
			}
			if r.unlimited {
				continue
			}
			candidate := RepoMemory{Limited: true, Current: r.current, Max: r.limit}
			if !worst.Limited || moreLoaded(candidate, worst) {
				worst = candidate
			}
		}
	}
	return worst
}

// moreLoaded reports whether a uses a strictly larger fraction of its limit
// than b. It compares the exact rationals (a.Current/a.Max vs b.Current/b.Max)
// by cross-multiplying in big integers: comparing truncated percents picks the
// wrong container right at the threshold, and int64 products overflow at real
// byte sizes. Both Max values are positive (ParseCgroupMemory rejects 0).
func moreLoaded(a, b RepoMemory) bool {
	left := new(big.Int).Mul(big.NewInt(a.Current), big.NewInt(b.Max))
	right := new(big.Int).Mul(big.NewInt(b.Current), big.NewInt(a.Max))
	return left.Cmp(right) > 0
}

// resolveMemoryHeadroom runs the memory probe once for the pass and returns
// the per-repo readings Decide's capacity gate consumes (#1290). It probes only
// repos that collected at least one ticket, and makes no seam call (so no
// runtime call at all) when there are none, so an idle fleet never execs into a
// container runtime. A probe failure is logged inside the probe and fails the
// affected repo closed; it is never an error to RunOnce, since a returned error
// would terminate RunLoop on a transient container-runtime outage.
func resolveMemoryHeadroom(repos []RepoConfig, tickets []Ticket, out io.Writer) map[string]RepoMemory {
	withTickets := make(map[string]bool, len(tickets))
	for _, t := range tickets {
		withTickets[t.Repo] = true
	}
	var candidates []RepoConfig
	for _, rc := range repos {
		if withTickets[rc.Repo] {
			candidates = append(candidates, rc)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return memoryProbeFn(candidates, out)
}
