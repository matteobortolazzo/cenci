package daemon

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strconv"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
)

// windowCleanupPassTimeout bounds one evaluate step: repo resolution, the
// opt-out read and the GitHub reads of every repo with a candidate window.
const windowCleanupPassTimeout = 2 * time.Minute

// The window-cleanup pass (#1251) closes the windows of tickets whose work is
// over, in three steps: snapshotCleanupCandidates collects the candidate windows
// on the loop, evaluateWindowCleanup resolves repos and reads ticket state off
// it, and applyWindowCleanup re-checks each verdict on the loop immediately
// before the kill. At most one pass is in flight.

// ticketReader reads ticket state for a repo; production is *ticketstate.Reader.
type ticketReader interface {
	Read(ctx context.Context, repo ticketstate.Repo, nums []int) map[int]ticketstate.Result
}

// repoResolver maps a recorded directory to its repo identity; production is
// *ticketstate.Resolver.
type repoResolver interface {
	Resolve(ctx context.Context, dir string) (ticketstate.Identity, error)
}

// cleanupCandidate is one tracked, ticket-named window with a recorded repo dir.
// It carries everything apply needs to confirm the window is still the one that
// was evaluated.
type cleanupCandidate struct {
	sessionKey  string
	session     string
	windowIndex string
	windowName  string
	paneID      string // the kill target: unlike session:index, tmux never reuses or shifts it
	ticket      string // the window name's ticket prefix, as the babysit guard takes it
	num         int
	repoDir     string
}

// matches reports whether wi is still the window c was evaluated for.
func (c cleanupCandidate) matches(wi *frontend.WindowInfo) bool {
	return wi != nil && wi.Session == c.session && wi.WindowIndex == c.windowIndex &&
		wi.WindowName == c.windowName && wi.RepoDir == c.repoDir && wi.PaneID == c.paneID
}

// windowCleanupResult is what the evaluate step hands back to the loop: the
// candidates whose ticket's work is over.
type windowCleanupResult struct {
	closable []cleanupCandidate
}

// cleanupDeps are the seams the evaluate step runs against.
type cleanupDeps struct {
	resolver repoResolver
	enabled  func(toplevel string) (bool, error)
	reader   ticketReader
	verbose  bool
}

// snapshotCleanupCandidates collects the tracked windows the pass may close:
// ticket-named windows with a recorded repo dir, one per session:index. It runs
// on the loop.
func (d *Daemon) snapshotCleanupCandidates() []cleanupCandidate {
	var cands []cleanupCandidate
	seen := make(map[string]bool)
	for key := range d.sessions {
		wi := d.frontend.WindowInfo(key)
		if wi == nil || wi.RepoDir == "" {
			continue
		}
		// The kill is bound to the pane id, so a window without a well-formed one
		// is never closed.
		if !sandbox.ValidPaneID(wi.PaneID) {
			continue
		}
		ticket := detect.TicketFromWindowName(wi.WindowName)
		// The round trip rejects what Atoi alone would accept as another number
		// ("042" is not ticket 42, "+7" is not ticket 7), and an unparseable or
		// empty prefix.
		num, err := strconv.Atoi(ticket)
		if err != nil || num < 1 || strconv.Itoa(num) != ticket {
			continue
		}
		windowKey := wi.Session + ":" + wi.WindowIndex
		if seen[windowKey] {
			continue
		}
		seen[windowKey] = true
		cands = append(cands, cleanupCandidate{
			sessionKey:  key,
			session:     wi.Session,
			windowIndex: wi.WindowIndex,
			windowName:  wi.WindowName,
			paneID:      wi.PaneID,
			ticket:      ticket,
			num:         num,
			repoDir:     wi.RepoDir,
		})
	}
	return cands
}

// dirResolution is the outcome of resolving one recorded directory.
type dirResolution struct {
	id ticketstate.Identity
	ok bool
}

// evaluateWindowCleanup decides which candidates' tickets are finished. It runs
// off the loop and touches no daemon state. It fails open: a repo that cannot be
// resolved, a malformed opt-out config, a failed read or an Unknown verdict
// closes nothing for the affected scope, and is logged regardless of -verbose.
func evaluateWindowCleanup(ctx context.Context, deps cleanupDeps, cands []cleanupCandidate) windowCleanupResult {
	resolutions := make(map[string]dirResolution)
	usable := make(map[string]bool) // git toplevel -> windowCleanup is enabled and its config readable
	byRepo := make(map[ticketstate.Repo][]cleanupCandidate)
	for _, c := range cands {
		res, seen := resolutions[c.repoDir]
		if !seen {
			id, err := deps.resolver.Resolve(ctx, c.repoDir)
			if err != nil {
				// %q: the directory is a raw tmux option value, so a newline in it must
				// not be able to forge extra log lines.
				log.Printf("window-cleanup: skipping windows launched in %q: %v", c.repoDir, err)
			}
			res = dirResolution{id: id, ok: err == nil}
			resolutions[c.repoDir] = res
		}
		if !res.ok {
			continue
		}
		ok, seen := usable[res.id.Toplevel]
		if !seen {
			enabled, err := deps.enabled(res.id.Toplevel)
			switch {
			case err != nil:
				log.Printf("window-cleanup: skipping repo %s: %v", res.id.Toplevel, err)
			case !enabled && deps.verbose:
				log.Printf("window-cleanup: skipping repo %s: windowCleanup.enabled is false", res.id.Toplevel)
			}
			ok = err == nil && enabled
			usable[res.id.Toplevel] = ok
		}
		if ok {
			byRepo[res.id.Repo] = append(byRepo[res.id.Repo], c)
		}
	}

	var out windowCleanupResult
	for repo, group := range byRepo {
		finished := readFinished(ctx, deps.reader, repo, group)
		for _, c := range group {
			if finished[c.num] {
				out.closable = append(out.closable, c)
			}
		}
	}
	return out
}

// readFinished reads the ticket state of group's windows with one call and
// returns the ticket numbers whose work is over. A missing result, a read error
// or an Unknown verdict is logged and leaves the number out.
func readFinished(ctx context.Context, reader ticketReader, repo ticketstate.Repo, group []cleanupCandidate) map[int]bool {
	var nums []int
	for _, c := range group {
		if !slices.Contains(nums, c.num) {
			nums = append(nums, c.num)
		}
	}
	results := reader.Read(ctx, repo, nums)
	finished := make(map[int]bool, len(nums))
	for _, n := range nums {
		r, ok := results[n]
		switch {
		case !ok:
			log.Printf("window-cleanup: no ticket state returned for %s/%s#%d", repo.Owner, repo.Name, n)
		case r.Err != nil:
			log.Printf("window-cleanup: reading %s/%s#%d: %v", repo.Owner, repo.Name, n, r.Err)
		case r.Verdict == ticketstate.Finished:
			finished[n] = true
		case r.Verdict == ticketstate.Unknown:
			log.Printf("window-cleanup: %s/%s#%d has an unknown state, leaving its window open", repo.Owner, repo.Name, n)
		}
	}
	return finished
}

// applyWindowCleanup closes the evaluated windows that are still safe to close.
// It runs on the loop, immediately before each kill: the verdicts were computed
// off the loop, so the window may since have been renumbered or reused, turned
// busy, or come under a babysit supervisor.
func (d *Daemon) applyWindowCleanup(res windowCleanupResult) {
	d.cleanupInFlight = false
	for _, c := range res.closable {
		sess := d.sessions[c.sessionKey]
		if sess == nil {
			continue
		}
		if !c.matches(d.frontend.WindowInfo(c.sessionKey)) {
			continue
		}
		if sess.Status == detect.StatusRunning || sess.Status == detect.StatusNeedInput {
			continue
		}
		if d.closeGuard != nil {
			if blocked, reason := d.closeGuard(c.ticket); blocked {
				if d.cfg.Verbose {
					log.Printf("window-cleanup: holding %s:%s (%s): %s", c.session, c.windowIndex, c.windowName, reason)
				}
				continue
			}
		}
		key := c.session + ":" + c.windowIndex
		delete(d.pending, c.paneID)
		log.Printf("window-cleanup: closing %s (%s): ticket #%d is finished", key, c.windowName, c.num)
		// The kill runs off the loop after a grace wait, so it targets the pane id
		// (resolved to its window by tmux at kill time), not session:index: with
		// renumber-windows a close earlier in this pass shifts later indexes, and
		// a freed index can be reused by a freshly dispatched window.
		d.killWindowTarget(c.paneID, fmt.Sprintf("%s (pane %s)", key, c.paneID), "window-cleanup")
	}
}

// startWindowCleanup begins one pass: it snapshots the candidates on the loop
// and evaluates them off it. It does nothing while the reader is unwired, a pass
// is already in flight, or no window is a candidate (so no GitHub read is made).
func (d *Daemon) startWindowCleanup() {
	if d.cleanupReader == nil || d.cleanupInFlight {
		return
	}
	cands := d.snapshotCleanupCandidates()
	if len(cands) == 0 {
		return
	}
	// A fresh Resolver per pass: a long-lived one can keep a stale origin and
	// answer for the wrong repo, which here means killing an agent.
	deps := cleanupDeps{
		resolver: d.newCleanupResolver(),
		enabled:  d.cleanupEnabled,
		reader:   d.cleanupReader,
		verbose:  d.cfg.Verbose,
	}
	results := d.cleanupResults
	d.cleanupInFlight = true
	d.runOffLoop(windowCleanupPassTimeout, func(ctx context.Context) {
		var res windowCleanupResult
		// Sent in a defer so the loop's in-flight flag always clears; the channel's
		// capacity of 1 means this never blocks, even after the loop has exited.
		defer func() { results <- res }()
		res = evaluateWindowCleanup(ctx, deps, cands)
	})
}
