package daemon

import (
	"context"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/worktreeclean"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// worktreeCleanupPassTimeout bounds each worker step of the worktree-cleanup
// pass, the evaluate step and the remove step separately. On expiry the rest of
// the step is skipped and logged.
const worktreeCleanupPassTimeout = 5 * time.Minute

// The worktree-cleanup pass (#1267) reclaims the finished and stale worktrees of
// every registered repo, in four steps that alternate between the loop and a
// worker: startWorktreeCleanup snapshots the window repo dirs and the daemon
// state on the loop; evaluateWorktreeCleanup updates the registry and judges
// every worktree off it; applyWorktreeGuards re-checks the in-use and babysit
// guards against fresh in-memory state on the loop; removeWorktrees reads the live
// state again before each survivor, re-judges it with strict guards and removes it
// off the loop. worktreeInFlight stays set from the first step to the last, so at most one
// pass is in flight. Every doubt keeps the worktree: if any in-use source cannot
// be read (the tmux window list, or the daemon snapshot for any reason other than
// the daemon not running), nothing is removed, and a babysit state that cannot be
// read keeps its ticket's worktree (#1300; the close paths stay fail-open). Every log line starts with
// "worktree-cleanup:", and none includes a remote URL.

// outcomeReader reads the detailed outcome of tickets for a repo; production is
// *ticketstate.Reader.
type outcomeReader interface {
	ReadOutcomes(ctx context.Context, repo ticketstate.Repo, nums []int) map[int]ticketstate.OutcomeResult
}

// worktreeCandidate is a worktree the evaluate step judged removable, with what the
// remove step needs to judge it again.
type worktreeCandidate struct {
	repo       worktreeclean.Repo
	class      worktreeclean.Classification
	staleAfter time.Duration
	outcomes   map[int]ticketstate.OutcomeResult
}

// worktreeEvalResult is what the evaluate step hands back to the loop: the
// worktrees that classified as removable.
type worktreeEvalResult struct {
	removable []worktreeCandidate
}

// worktreeDeps are the seams and loop-built inputs the worker steps run against.
type worktreeDeps struct {
	reader         outcomeReader
	resolver       repoResolver
	listPanes      func() ([]tmux.LivePane, error)
	inSandbox      func() bool
	registryPath   string
	userConfigPath string
	babysit        func(ticket string) (blocked bool, reason string, err error)
	verbose        bool
	snapshot       *watch.StateSnapshot
	windowDirs     []string
}

// worktreeSeams copies the daemon's seams for a worker. It runs on the loop.
func (d *Daemon) worktreeSeams(snap *watch.StateSnapshot) worktreeDeps {
	blocks := d.worktreeBabysit
	if blocks == nil {
		blocks = strictBabysit
	}
	return worktreeDeps{
		reader:         d.worktreeReader,
		resolver:       d.newWorktreeResolver(),
		listPanes:      d.worktreeListPanes,
		inSandbox:      d.worktreeInSandbox,
		registryPath:   d.worktreeRegistryPath,
		userConfigPath: d.worktreeUserConfigPath,
		babysit:        blocks,
		verbose:        d.cfg.Verbose,
		snapshot:       snap,
	}
}

// strictBabysit is the pass's default babysit lookup. Unlike d.closeGuard, which
// the close paths keep fail-open, it reports a state that cannot be read, so the
// worktree is kept as unknown instead of read as "not armed" (#1300).
func strictBabysit(ticket string) (bool, string, error) {
	return babysit.BlocksCloseStrict(ticket, "", "")
}

// liveSources reads tmux and the sandbox gate through the injected seams and the
// daemon snapshot from the loop-built copy: the daemon never dials its own socket.
func (deps worktreeDeps) liveSources() worktreeclean.LiveSources {
	return worktreeclean.LiveSources{
		ListPanes:    deps.listPanes,
		InSandbox:    deps.inSandbox,
		ReadSnapshot: func() (*watch.StateSnapshot, error) { return deps.snapshot, nil },
	}
}

// options are the classification options for one candidate over live, with strict
// guards: an unknown in-use status (an unreadable window list or a non-benign
// snapshot error) or an unreadable babysit state keeps the worktree.
func (deps worktreeDeps) options(c worktreeCandidate, live worktreeclean.LiveState) worktreeclean.Options {
	return worktreeclean.Options{
		StaleAfter: c.staleAfter,
		Outcomes:   c.outcomes,
		InUse:      worktreeclean.InUseGuard(live, true),
		Babysit:    worktreeclean.BabysitGuard(deps.babysit),
	}
}

// startWorktreeCleanup begins one pass: it snapshots the window repo dirs and the
// daemon state on the loop and evaluates off it. It does nothing while the reader
// is unwired or a pass is already in flight.
func (d *Daemon) startWorktreeCleanup() {
	if d.worktreeReader == nil || d.worktreeInFlight {
		return
	}
	snap := d.buildSnapshot()
	deps := d.worktreeSeams(&snap)
	deps.windowDirs = d.worktreeWindowDirs()
	results := d.worktreeEvalResults
	d.worktreeInFlight = true
	d.runOffLoop(worktreeCleanupPassTimeout, func(ctx context.Context) {
		var res worktreeEvalResult
		// Sent in a defer so the loop's in-flight flag always clears; the channel's
		// capacity of 1 means this never blocks, even after the loop has exited.
		defer func() { results <- res }()
		res = evaluateWorktreeCleanup(ctx, deps)
	})
}

// worktreeWindowDirs lists the distinct repo dirs the tracked windows were launched
// in (@cenci-repo-dir), sorted.
func (d *Daemon) worktreeWindowDirs() []string {
	var dirs []string
	for key := range d.sessions {
		if wi := d.frontend.WindowInfo(key); wi != nil && wi.RepoDir != "" && !slices.Contains(dirs, wi.RepoDir) {
			dirs = append(dirs, wi.RepoDir)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// evaluateWorktreeCleanup updates the registry and judges the worktrees of every
// registered repo. It runs off the loop and touches no daemon state.
func evaluateWorktreeCleanup(ctx context.Context, deps worktreeDeps) worktreeEvalResult {
	var res worktreeEvalResult
	repoDirs := updateWorktreeRegistry(ctx, deps)

	live := worktreeclean.ReadLiveState(ctx, deps.liveSources())
	if err := live.StrictErr(); err != nil {
		logWorktreeInUseUnknown(err)
		return res
	}

	seen := make(map[string]bool)
	for _, dir := range repoDirs {
		if err := ctx.Err(); err != nil {
			log.Printf("worktree-cleanup: evaluation stopped early, the remaining repos are skipped: %s", logText(err.Error()))
			break
		}
		identity, err := deps.resolver.Resolve(ctx, dir)
		if err != nil {
			// A fixed reason, not err: a resolution error can quote the remote URL.
			log.Printf("worktree-cleanup: skipping registered repo %q: it could not be resolved to a GitHub repository", dir)
			continue
		}
		repo, entries, err := worktreeclean.Enumerate(ctx, dir)
		if err != nil {
			log.Printf("worktree-cleanup: skipping repo %q: %s", dir, logText(err.Error()))
			continue
		}
		if seen[repo.MainRoot] {
			continue
		}
		seen[repo.MainRoot] = true
		res.removable = append(res.removable, evaluateWorktreeRepo(ctx, deps, live, identity.Repo, repo, entries)...)
	}
	return res
}

// updateWorktreeRegistry merges the dispatch config's repo dirs and the windows'
// repo dirs into the persisted registry and returns the repo dirs to process,
// logging what could not be registered.
func updateWorktreeRegistry(ctx context.Context, deps worktreeDeps) []string {
	dispatchDirs, err := worktreeclean.DispatchRepoDirs(deps.userConfigPath)
	if err != nil {
		log.Printf("worktree-cleanup: dispatch repos not read: %s", logText(err.Error()))
	}
	repoDirs, problems := worktreeclean.UpdateRegistry(ctx, worktreeclean.RegistryUpdate{
		Path:         deps.registryPath,
		DispatchDirs: dispatchDirs,
		WindowDirs:   deps.windowDirs,
	})
	for _, p := range problems {
		// Only the registry's own file and entry errors are logged as text; a
		// candidate's error can quote the remote URL, so a candidate gets a fixed
		// reason.
		switch p.Source {
		case worktreeclean.RegistrySourceFile, worktreeclean.RegistrySourceEntry:
			log.Printf("worktree-cleanup: registry %q: %s", p.Dir, logText(p.Err.Error()))
		case worktreeclean.RegistrySourceDispatch:
			log.Printf("worktree-cleanup: dispatch repo dir %q not registered: it is not a git repository with a parseable origin", p.Dir)
		case worktreeclean.RegistrySourceWindow:
			// A shell window opened outside any repo would log on every pass.
			if deps.verbose {
				log.Printf("worktree-cleanup: window repo dir %q not registered: it is not a git repository with a parseable origin", p.Dir)
			}
		default:
			log.Printf("worktree-cleanup: registry problem for %q (%s)", p.Dir, p.Source)
		}
	}
	return repoDirs
}

// evaluateWorktreeRepo judges the worktrees of one repo and returns the removable
// ones. A malformed or disabled worktreeCleanup config skips the repo before any
// GitHub read.
func evaluateWorktreeRepo(ctx context.Context, deps worktreeDeps, live worktreeclean.LiveState, identity ticketstate.Repo, repo worktreeclean.Repo, entries []worktreeclean.Entry) []worktreeCandidate {
	if len(entries) == 0 {
		return nil
	}
	cfg, err := ticketstate.WorktreeCleanupConfig(repo.MainRoot)
	if err != nil {
		log.Printf("worktree-cleanup: skipping repo %q: %s", repo.MainRoot, logText(err.Error()))
		return nil
	}
	if !cfg.Enabled {
		if deps.verbose {
			log.Printf("worktree-cleanup: skipping repo %q: worktreeCleanup.enabled is false", repo.MainRoot)
		}
		return nil
	}

	cand := worktreeCandidate{repo: repo, staleAfter: cfg.StaleAfter, outcomes: readWorktreeOutcomes(ctx, deps.reader, identity, entries)}
	o := deps.options(cand, live)
	var out []worktreeCandidate
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			log.Printf("worktree-cleanup: evaluation of %q stopped early, the remaining worktrees are skipped: %s", repo.MainRoot, logText(err.Error()))
			break
		}
		c := worktreeclean.Classify(ctx, repo, e, o)
		if isRemovable(c) {
			cand.class = c
			out = append(out, cand)
			continue
		}
		logKeptWorktree(deps.verbose, c)
	}
	return out
}

// readWorktreeOutcomes reads the GitHub outcome of every ticket the worktrees are
// named for, with one call. A failed or missing result leaves that ticket Unknown,
// which keeps its worktree from the finished path; it is logged with owner/name
// and the ticket numbers only, because the error text can quote the remote URL.
func readWorktreeOutcomes(ctx context.Context, reader outcomeReader, repo ticketstate.Repo, entries []worktreeclean.Entry) map[int]ticketstate.OutcomeResult {
	tickets := worktreeclean.TicketNumbers(entries)
	if len(tickets) == 0 {
		return nil
	}
	outcomes := reader.ReadOutcomes(ctx, repo, tickets)
	var failed []int
	for _, n := range tickets {
		if r, ok := outcomes[n]; !ok || r.Err != nil {
			failed = append(failed, n)
		}
	}
	if len(failed) > 0 {
		log.Printf("worktree-cleanup: reading ticket state of %s/%s failed for tickets %v; their finished worktrees are kept", repo.Owner, repo.Name, failed)
	}
	return outcomes
}

// applyWorktreeGuards re-checks the evaluated worktrees against fresh in-memory
// daemon state on the loop: a window or busy session may have appeared, or a
// babysit supervisor been armed, since the evaluation. The survivors go to the
// remove step; with none left the pass ends here.
func (d *Daemon) applyWorktreeGuards(res worktreeEvalResult) {
	if len(res.removable) == 0 {
		d.finishWorktreeCleanup()
		return
	}
	snap := d.buildSnapshot()
	deps := d.worktreeSeams(&snap)
	inUse := worktreeclean.InUseGuard(worktreeclean.SnapshotLiveState(&snap), false)
	babysitGuard := worktreeclean.BabysitGuard(deps.babysit)

	var survivors []worktreeCandidate
	for _, c := range res.removable {
		target := worktreeclean.TargetOf(c.class.Entry)
		// An error from either guard is a doubt, and every doubt keeps the worktree.
		// The in-memory snapshot read cannot fail, but the babysit state read can, so
		// the cause is always logged: a persistent failure would otherwise silently
		// disable the cleanup of that worktree.
		blocked, err := inUse(context.Background(), target)
		if err != nil {
			logHeldWorktreeUnknown(c, "in-use status unknown", err)
			continue
		}
		if blocked {
			logHeldWorktree(deps.verbose, c, "in use now")
			continue
		}
		blocked, err = babysitGuard(context.Background(), target)
		if err != nil {
			logHeldWorktreeUnknown(c, "babysit status unknown", err)
			continue
		}
		if blocked {
			logHeldWorktree(deps.verbose, c, "under a babysit supervisor now")
			continue
		}
		survivors = append(survivors, c)
	}
	if len(survivors) == 0 {
		d.finishWorktreeCleanup()
		return
	}
	done := d.worktreeRemoveDone
	d.runOffLoop(worktreeCleanupPassTimeout, func(ctx context.Context) {
		// Sent in a defer so the loop's in-flight flag always clears.
		defer func() { done <- struct{}{} }()
		removeWorktrees(ctx, deps, survivors)
	})
}

// finishWorktreeCleanup ends a pass: after its remove step has reported, or when no
// worktree is left for one.
func (d *Daemon) finishWorktreeCleanup() {
	d.worktreeInFlight = false
}

// removeWorktrees removes the survivors one by one. It runs off the loop. The live
// state is read again before EACH worktree, so a window or shell opened during a
// long removal step is seen, and the worktree is judged again with strict guards
// right before its removal (Remove re-runs the git checks itself); an unreadable
// tmux window list removes nothing further. A failure on one worktree is logged
// and the rest still run.
func removeWorktrees(ctx context.Context, deps worktreeDeps, survivors []worktreeCandidate) {
	for i, c := range survivors {
		if err := ctx.Err(); err != nil {
			log.Printf("worktree-cleanup: removal stopped early, %d worktrees left untouched: %s", len(survivors)-i, logText(err.Error()))
			return
		}
		live := worktreeclean.ReadLiveState(ctx, deps.liveSources())
		if err := live.StrictErr(); err != nil {
			logWorktreeInUseUnknown(err)
			return
		}
		o := deps.options(c, live)
		fresh := worktreeclean.Classify(ctx, c.repo, c.class.Entry, o)
		if !isRemovable(fresh) {
			logKeptWorktree(deps.verbose, fresh)
			continue
		}
		reportWorktreeRemoval(deps.verbose, worktreeclean.Remove(ctx, c.repo, fresh, o))
	}
}

// reportWorktreeRemoval logs the outcome of one Remove. Removals and failures are
// logged regardless of -verbose; a worktree that changed since it was judged is a
// keep, not a failure.
func reportWorktreeRemoval(verbose bool, rem worktreeclean.Removal) {
	label := worktreeLabel(rem.Entry)
	switch {
	case rem.Removed:
		log.Printf("worktree-cleanup: removed %q: %s, freed %d bytes", label, rem.Verdict, rem.BytesFreed)
		if rem.Err != nil {
			log.Printf("worktree-cleanup: %q was removed but not cleaned up completely: %s", label, logText(rem.Err.Error()))
		}
	case rem.Reason == worktreeclean.KeepChanged:
		// A change is a policy keep, shown with -verbose only; a read failure
		// while re-checking is always shown.
		if rem.Err != nil {
			log.Printf("worktree-cleanup: kept %q: %s: %s", label, rem.Reason, logText(rem.Err.Error()))
		} else if verbose {
			log.Printf("worktree-cleanup: kept %q: changed since it was judged", label)
		}
	case rem.Err != nil:
		log.Printf("worktree-cleanup: could not remove %q: %s", label, logText(rem.Err.Error()))
	default:
		log.Printf("worktree-cleanup: %q was not removed", label)
	}
}

// isRemovable reports whether c's worktree may be removed.
func isRemovable(c worktreeclean.Classification) bool {
	return c.Verdict == worktreeclean.VerdictFinished || c.Verdict == worktreeclean.VerdictStale
}

// worktreeLabel is the path to log for e: an unmapped entry has no trustworthy
// worktree path, only its admin directory name.
func worktreeLabel(e worktreeclean.Entry) string {
	if e.LocalPath != "" {
		return e.LocalPath
	}
	return e.AdminName
}

// logKeptWorktree logs why a worktree was kept. A keep that carries an error (a
// git inspection, in-use or pipeline-state read that failed) is always logged: a
// persistent failure would otherwise silently disable the cleanup of that
// worktree. A policy keep (dirty, recent activity) is logged with -verbose only.
func logKeptWorktree(verbose bool, c worktreeclean.Classification) {
	if c.Err != nil {
		log.Printf("worktree-cleanup: kept %q: %s: %s", worktreeLabel(c.Entry), c.Reason, logText(c.Err.Error()))
		return
	}
	if verbose {
		log.Printf("worktree-cleanup: kept %q: %s", worktreeLabel(c.Entry), c.Reason)
	}
}

// logHeldWorktree logs a worktree the loop's re-check held back, with -verbose only.
func logHeldWorktree(verbose bool, c worktreeCandidate, why string) {
	if verbose {
		log.Printf("worktree-cleanup: kept %q: %s", worktreeLabel(c.class.Entry), why)
	}
}

// logHeldWorktreeUnknown logs a worktree the loop's re-check held back because a
// guard could not answer. It is always logged, whatever -verbose says.
func logHeldWorktreeUnknown(c worktreeCandidate, why string, err error) {
	log.Printf("worktree-cleanup: kept %q: %s: %s", worktreeLabel(c.class.Entry), why, logText(err.Error()))
}

// logWorktreeInUseUnknown logs why the pass keeps every worktree. It is always
// logged: a persistent tmux problem would otherwise silently disable the cleanup.
func logWorktreeInUseUnknown(err error) {
	log.Printf("worktree-cleanup: in-use status unknown, keeping every worktree this pass: %s", logText(err.Error()))
}

// urlRE matches a scheme://... token.
var urlRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S*`)

// logText makes error text safe to log: control characters become spaces so it
// cannot forge extra log lines, and anything URL-shaped is replaced, because a
// daemon log never includes a remote URL.
func logText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return urlRE.ReplaceAllString(s, "<url>")
}
