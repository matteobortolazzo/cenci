package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// windowCleanupPassTimeout bounds one worker step: the tmux inventory reads (each
// also bounded by its own tmux call timeout), repo resolution, the opt-out read
// and the GitHub reads of every repo with a candidate window.
const windowCleanupPassTimeout = 2 * time.Minute

// The window-cleanup pass (#1251) closes the windows of tickets whose work is
// over, in three steps: snapshotCleanup collects the tracked candidate windows on
// the loop, runCleanupPass adds the retained windows found in tmux's own inventory
// and evaluateWindowCleanup resolves repos and reads ticket state, both off the
// loop, and applyWindowCleanup re-checks each verdict on the loop immediately
// before the kill. At most one pass is in flight.
//
// A tracked window is one a session in d.sessions owns. A retained window (#1458)
// is one tmux still holds -- every pane dead, kept by remain-on-exit -- that no
// session tracks any more, because SessionEnd deleted the session or the daemon
// restarted. It is found from tmux, never from agent-session tracking, and goes
// through the same evaluation: only a Finished verdict closes it, and the kill is
// revalidated against tmux-owned identity (see retainedWindowCheck). A window that
// is alive, whose ticket is unknown, or whose name is not a ticket's (a legacy
// prose slug) is kept: nothing but a terminal verdict is evidence that its work is
// over, and a clean session end is not.

// ticketReader reads ticket state for a repo; production is *ticketstate.Reader.
type ticketReader interface {
	Read(ctx context.Context, repo ticketstate.Repo, nums []int) map[int]ticketstate.Result
}

// repoResolver maps a recorded directory to its repo identity; production is
// *ticketstate.Resolver.
type repoResolver interface {
	Resolve(ctx context.Context, dir string) (ticketstate.Identity, error)
}

// cleanupCandidate is one ticket-named window with a recorded repo dir: a tracked
// one, or a retained one found in tmux's inventory (untracked). It carries
// everything apply needs to confirm the window is still the one that was
// evaluated.
type cleanupCandidate struct {
	sessionKey  string // tracked only
	session     string // tracked only
	windowIndex string // tracked only
	windowName  string
	paneID      string // the kill target: unlike session:index, tmux never reuses or shifts it
	socket      string // the tmux server the window lives on; "" = the daemon's own
	ticket      string // the window name's ticket prefix, as the babysit guard takes it
	num         int
	repoDir     string

	// untracked marks a retained window. windowID is tmux's own id for it and panes
	// its pane ids in string order (paneID is the lowest by number); the kill
	// revalidates all of them.
	untracked bool
	windowID  string
	panes     []string
}

// ticketFromWindowName is the one window-name rule of both discovery paths: a
// ticket window is named "<N>-...", and the round trip rejects what Atoi alone
// would accept as another number ("042" is not ticket 42, "+7" is not ticket 7), a
// zero, and an unparseable or empty prefix.
func ticketFromWindowName(name string) (ticket string, num int, ok bool) {
	ticket = detect.TicketFromWindowName(name)
	num, err := strconv.Atoi(ticket)
	if err != nil || num < 1 || strconv.Itoa(num) != ticket {
		return "", 0, false
	}
	return ticket, num, true
}

// matches reports whether wi is still the window c was evaluated for.
func (c cleanupCandidate) matches(wi *frontend.WindowInfo) bool {
	return wi != nil && wi.Session == c.session && wi.WindowIndex == c.windowIndex &&
		wi.WindowName == c.windowName && wi.RepoDir == c.repoDir && wi.PaneID == c.paneID &&
		wi.Socket == c.socket
}

// windowCleanupResult is what the evaluate step hands back to the loop: the
// candidates whose ticket's work is over.
type windowCleanupResult struct {
	closable []cleanupCandidate
}

// cleanupDeps are the seams the evaluate step runs against.
type cleanupDeps struct {
	resolver  repoResolver
	enabled   func(toplevel string) (bool, error)
	reader    ticketReader
	inventory func(socket string) (tmux.CleanupInventory, error) // nil: no retained-window discovery
	verbose   bool
}

// paneRef is a pane on one tmux server: pane ids repeat across servers, so the
// pane alone never identifies one.
type paneRef struct{ socket, pane string }

// cleanupSnapshot is what the loop hands the worker: the tracked candidates, the
// tmux servers whose inventory the worker reads, and every pane a tracked session
// owns (an untracked window holding one is the tracked path's, never discovered).
type cleanupSnapshot struct {
	cands   []cleanupCandidate
	sockets []string
	tracked map[paneRef]bool
}

// runCleanupPass is the worker's whole job: it adds the retained windows found in
// the tmux inventory to the snapshot's tracked candidates and evaluates them all
// with one batched ticket read per repo.
func runCleanupPass(ctx context.Context, deps cleanupDeps, snap cleanupSnapshot) windowCleanupResult {
	cands := slices.Clone(snap.cands)
	cands = append(cands, discoverRetainedWindows(ctx, deps, snap)...)
	return evaluateWindowCleanup(ctx, deps, cands)
}

// snapshotCleanup is the loop-side half of a pass: the tracked candidates, the
// tmux servers to read the inventory of -- the daemon's own server "" first, then
// every distinct non-empty socket a tracked session recorded, sorted, the "" server's
// own socket path (defaultServerSocket) excluded because "" already names that
// server -- and every pane a tracked session owns. It logs once when the trusted
// socket directory is needed to name the "" server and cannot be resolved.
func (d *Daemon) snapshotCleanup() cleanupSnapshot {
	def, err := d.defaultServerSocket()
	if err != nil {
		// Once per pass, here on the loop: trackedPanes and applyRetainedClose run
		// per pass and per candidate and would repeat it. The pass degrades toward
		// keeping windows (see trackedPanes), so it is never visible otherwise.
		log.Printf("window-cleanup: tmux socket directory unresolved, the daemon's own server's socket path cannot be folded: %v", err)
	}
	snap := cleanupSnapshot{cands: d.snapshotCleanupCandidates(), sockets: []string{""}, tracked: d.trackedPanes(def)}
	for _, sess := range d.sessions {
		// The "" server's recorded socket path is the server "" already names:
		// reading it twice would give the same windows under two spellings.
		if def != "" && sess.TmuxSocket == def {
			continue
		}
		if sess.TmuxSocket != "" && !slices.Contains(snap.sockets, sess.TmuxSocket) {
			snap.sockets = append(snap.sockets, sess.TmuxSocket)
		}
	}
	slices.Sort(snap.sockets) // "" sorts before every non-empty socket
	return snap
}

// defaultServerSocket is the socket path the daemon's own ("") tmux server goes by
// in a session's TmuxSocket: the first comma-separated field of the daemon's own
// $TMUX when it passes tmux.ValidSocket, because that is the server a bare `tmux`
// uses (tmux prefers $TMUX to its default path, and daemon.Spawn inherits the
// environment, so a daemon spawned from a pane of another server S has "" == S),
// else tmux's default socket, <trusted socket dir>/default. A recorded socket
// equal to it is the same server as "" and is folded into it; any other recorded
// socket, the real default path included while "" is S, is a different server.
//
// An empty result means the name is unknown: $TMUX does not name it and the
// trusted directory cannot be resolved (err carries the seam's failure; a nil
// seam is unknown without one). $TMUX alone resolving the name never calls the
// seam, so a failing seam is not reported then.
func (d *Daemon) defaultServerSocket() (string, error) {
	if first, _, _ := strings.Cut(os.Getenv("TMUX"), ","); tmux.ValidSocket(first) {
		return first, nil
	}
	if d.tmuxSocketDir == nil {
		return "", nil
	}
	dir, err := d.tmuxSocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "default"), nil
}

// trackedPanes is every (socket, pane) a tracked session owns, by the session's
// own record and by the window the frontend resolves for it: a superset of the
// tracked candidates' panes, so the guard it backs only gets stricter. It runs on
// the loop. def is defaultServerSocket's result.
//
// A pane recorded under the "" server's socket path is keyed under "", the name
// the inventory reads that server by, so an untracked window of that server is
// matched against it. When that path is unknown (def is empty) the pane is keyed
// under "" as well as under its recorded socket: the guard then also holds a
// same-numbered pane of the "" server (a window kept open until the path
// resolves) instead of missing a tracked window.
func (d *Daemon) trackedPanes(def string) map[paneRef]bool {
	known := def != ""
	tracked := make(map[paneRef]bool)
	add := func(socket, pane string) {
		if known && socket == def {
			socket = ""
		}
		tracked[paneRef{socket: socket, pane: pane}] = true
		if !known {
			tracked[paneRef{socket: "", pane: pane}] = true
		}
	}
	for key, sess := range d.sessions {
		if sess.TmuxPane != "" {
			add(sess.TmuxSocket, sess.TmuxPane)
		}
		if wi := d.frontend.WindowInfo(key); wi != nil && wi.PaneID != "" {
			add(wi.Socket, wi.PaneID)
		}
	}
	return tracked
}

// discoverRetainedWindows reads the tmux inventory of every snapshot socket and
// returns the windows that are candidates for closing though no session tracks
// them: a ticket-named window (the tracked path's name rule) with a recorded repo
// dir, whose every pane has a well-formed id and is dead, and none of whose panes
// a tracked session owns on that socket (the tracked path owns such a window). It
// runs off the loop and touches no daemon state.
//
// It fails open: a listing that fails or times out, and every row the reader
// dropped, are logged whatever -verbose says and keep the affected windows open,
// while the other sockets are still read. tmux reporting no server is the one
// benign failure: nothing runs there. A live window, a prose-named one and one
// without a repo dir are kept silently, as on the tracked path.
func discoverRetainedWindows(ctx context.Context, deps cleanupDeps, snap cleanupSnapshot) []cleanupCandidate {
	if deps.inventory == nil {
		return nil
	}
	var cands []cleanupCandidate
	for _, socket := range snap.sockets {
		if ctx.Err() != nil {
			return cands
		}
		inv, err := deps.inventory(socket)
		switch {
		case errors.Is(err, tmux.ErrNoServer):
			continue
		case err != nil:
			// %q: the socket is a recorded value, so it must not forge log lines.
			log.Printf("window-cleanup: reading the window inventory of tmux socket %q: %v", socket, err)
			continue
		}
		for _, line := range inv.Dropped {
			log.Printf("window-cleanup: keeping a window on tmux socket %q: %s", socket, line)
		}
		for _, w := range inv.Windows {
			if c, ok := retainedCandidate(socket, w, snap.tracked); ok {
				cands = append(cands, c)
			}
		}
	}
	return cands
}

// retainedCandidate turns one inventory window into a candidate, or reports that
// it must stay open.
func retainedCandidate(socket string, w tmux.CleanupWindow, tracked map[paneRef]bool) (cleanupCandidate, bool) {
	ticket, num, ok := ticketFromWindowName(w.Name)
	if !ok || w.RepoDir == "" || !w.AllDead() {
		return cleanupCandidate{}, false
	}
	panes := make([]string, 0, len(w.Panes))
	for _, p := range w.Panes {
		if !sandbox.ValidPaneID(p.ID) {
			// The reader already enforces the shape; the kill is bound to a pane id,
			// so the daemon re-checks it rather than trust another package.
			log.Printf("window-cleanup: keeping window %q on tmux socket %q: invalid pane id %q", w.WindowID, socket, p.ID)
			return cleanupCandidate{}, false
		}
		if tracked[paneRef{socket: socket, pane: p.ID}] {
			return cleanupCandidate{}, false
		}
		panes = append(panes, p.ID)
	}
	slices.Sort(panes)
	return cleanupCandidate{
		windowName: w.Name,
		paneID:     w.LowestPane(),
		socket:     socket,
		ticket:     ticket,
		num:        num,
		repoDir:    w.RepoDir,
		untracked:  true,
		windowID:   w.WindowID,
		panes:      panes,
	}, true
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
		ticket, num, ok := ticketFromWindowName(wi.WindowName)
		if !ok {
			continue
		}
		// session:index repeats across tmux servers, so a window is one per
		// (socket, session:index).
		windowKey := wi.Socket + "|" + wi.Session + ":" + wi.WindowIndex
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
			socket:      wi.Socket,
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
// busy, or come under a babysit supervisor. A retained window has no session to
// be busy, so its apply is applyRetainedClose.
func (d *Daemon) applyWindowCleanup(res windowCleanupResult) {
	d.cleanupInFlight = false
	for _, c := range res.closable {
		if c.untracked {
			d.applyRetainedClose(c)
			continue
		}
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
		delete(d.pending, pendingKey{socket: c.socket, pane: c.paneID})
		log.Printf("window-cleanup: closing %s (%s): ticket #%d is finished", key, c.windowName, c.num)
		// The kill runs off the loop after a grace wait, so it targets the pane id
		// (resolved to its window by tmux at kill time), not session:index: with
		// renumber-windows a close earlier in this pass shifts later indexes, and
		// a freed index can be reused by a freshly dispatched window. It runs on the
		// window's recorded tmux socket, never a same-numbered pane elsewhere.
		d.killWindowTarget(c.socket, c.paneID, fmt.Sprintf("%s (pane %s)", key, c.paneID), "window-cleanup", nil)
	}
}

// applyRetainedClose closes one retained window (#1458) on the loop, immediately
// before the kill: the verdict was computed off it, so a session may since have
// started on one of the window's panes. Having no session is not a reason to hold
// the window (the babysit guard is consulted by ticket, as on the tracked path),
// and the kill is revalidated off the loop against tmux (retainedWindowCheck).
func (d *Daemon) applyRetainedClose(c cleanupCandidate) {
	desc := fmt.Sprintf("window %s (pane %s)", c.windowID, c.paneID)
	// The pass is a loop snapshot old by now: re-check on the loop that no tracked
	// session owns any pane of the window on its socket.
	// The seam's failure is not logged here: snapshotCleanup reports it once per pass.
	def, _ := d.defaultServerSocket()
	tracked := d.trackedPanes(def)
	for _, pane := range c.panes {
		if tracked[paneRef{socket: c.socket, pane: pane}] {
			if d.cfg.Verbose {
				log.Printf("window-cleanup: not closing %s: a tracked session owns pane %s", desc, pane)
			}
			return
		}
	}
	if d.closeGuard != nil {
		if blocked, reason := d.closeGuard(c.ticket); blocked {
			if d.cfg.Verbose {
				log.Printf("window-cleanup: holding %s (%q): %s", desc, c.windowName, reason)
			}
			return
		}
	}
	// A pending-close entry is keyed by the socket its window recorded, so the
	// "" server's entries may carry either spelling. Only that server's own path is
	// added: another server's entry for the same pane id is never deleted.
	sockets := []string{c.socket}
	if def != "" && c.socket == "" {
		sockets = append(sockets, def)
	}
	for _, pane := range c.panes {
		for _, socket := range sockets {
			delete(d.pending, pendingKey{socket: socket, pane: pane})
		}
	}
	log.Printf("window-cleanup: closing retained %s on tmux socket %q (%q): ticket #%d is finished", desc, c.socket, c.windowName, c.num)
	// The lowest pane id on the window's own socket, never session:index, for the
	// reason the tracked path gives; the window is re-read through it before the
	// process cleanup and again before kill-window.
	d.killWindowTarget(c.socket, c.paneID, desc, "window-cleanup", d.retainedWindowCheck(c))
}

// retainedWindowCheck returns the revalidation a retained window's kill runs
// twice, off the loop: it re-reads the window through its pane id and confirms
// it is still exactly the window that was evaluated -- same tmux window id, same
// set of panes, every pane still dead, same name and same @cenci-repo-dir. A
// difference (a respawned pane, a rename, a pane id that now belongs to another
// window) or an unreadable window is an error that keeps the window open, and a
// pane that no longer exists wraps tmux.ErrWindowGone.
func (d *Daemon) retainedWindowCheck(c cleanupCandidate) func(ctx context.Context) error {
	revalidate := d.cleanupRevalidate
	return func(context.Context) error {
		if revalidate == nil {
			return errors.New("no window reader is wired to revalidate it")
		}
		w, err := revalidate(c.socket, c.paneID)
		if err != nil {
			return fmt.Errorf("re-reading it: %w", err)
		}
		panes := make([]string, 0, len(w.Panes))
		for _, p := range w.Panes {
			panes = append(panes, p.ID)
		}
		slices.Sort(panes)
		switch {
		case w.WindowID != c.windowID:
			return fmt.Errorf("pane %s is now in window %q, was in %q", c.paneID, w.WindowID, c.windowID)
		case !slices.Equal(panes, c.panes):
			return fmt.Errorf("its panes changed from %q to %q", c.panes, panes)
		case !w.AllDead():
			return errors.New("a pane is live again")
		case w.Name != c.windowName:
			return fmt.Errorf("it was renamed from %q to %q", c.windowName, w.Name)
		case w.RepoDir != c.repoDir:
			return fmt.Errorf("its repo dir changed from %q to %q", c.repoDir, w.RepoDir)
		}
		return nil
	}
}

// startWindowCleanup begins one pass: it snapshots the candidates on the loop
// and evaluates them off it, the retained windows of the tmux inventory included.
// It does nothing while the reader is unwired or a pass is already in flight, and
// with no tracked candidate and no inventory reader wired. A pass that finds no
// candidate makes no GitHub read.
func (d *Daemon) startWindowCleanup() {
	if d.cleanupReader == nil || d.cleanupInFlight {
		return
	}
	snap := d.snapshotCleanup()
	if len(snap.cands) == 0 && d.cleanupInventory == nil {
		return
	}
	// A fresh Resolver per pass: a long-lived one can keep a stale origin and
	// answer for the wrong repo, which here means killing an agent.
	deps := cleanupDeps{
		resolver:  d.newCleanupResolver(),
		enabled:   d.cleanupEnabled,
		reader:    d.cleanupReader,
		inventory: d.cleanupInventory,
		verbose:   d.cfg.Verbose,
	}
	results := d.cleanupResults
	d.cleanupInFlight = true
	d.runOffLoop(windowCleanupPassTimeout, func(ctx context.Context) {
		var res windowCleanupResult
		// Sent in a defer so the loop's in-flight flag always clears; the channel's
		// capacity of 1 means this never blocks, even after the loop has exited.
		defer func() { results <- res }()
		res = runCleanupPass(ctx, deps, snap)
	})
}
