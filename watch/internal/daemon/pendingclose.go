package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// pendingCloseRecheckInterval throttles how often one blocked pending-close
// re-consults the babysit close guard (#787). runSweep ticks every second and
// the guard reads the filesystem, while babysit itself refreshes its state at
// most once per supervision interval (default 15m) — so a much finer cadence
// would only burn syscalls without noticing anything sooner.
const pendingCloseRecheckInterval = 30 * time.Second

// pendingCloseEntry is one entry of the pending-close registry: the
// registration itself plus the bookkeeping for a close deferred a second time
// by the babysit guard (#787).
type pendingCloseEntry struct {
	pc ipc.PendingClose
	// blocked records that the guard held this close back, so runSweep knows
	// to keep re-evaluating it after the owning session has already ended.
	blocked bool
	// lastChecked is when the guard was last consulted for this entry.
	lastChecked time.Time
}

// pendingCloseKey returns the registry key for a pending-close: the same
// "session:index" identity closecmd already kills by (#522).
func pendingCloseKey(session, windowIndex string) string {
	return session + ":" + windowIndex
}

// registerPendingClose records pc in the daemon's in-memory pending-close
// registry, keyed by "session:index". Registering the same session:index
// twice is a no-op past the first call, so a repeated lazyboards cleanup
// invocation before the session ends never produces more than one eventual
// kill (#522 dedup AC).
func (d *Daemon) registerPendingClose(pc ipc.PendingClose) {
	key := pendingCloseKey(pc.Session, pc.WindowIndex)
	if _, exists := d.pending[key]; exists {
		return
	}
	d.pending[key] = &pendingCloseEntry{pc: pc}
	if d.cfg.Verbose {
		log.Printf("pending-close: registered %s (%s)", key, pc.WindowName)
	}
}

// killPendingClose kills wi's window (and, first, the processes behind it, off
// the event loop -- see killWindowByKey) if it matches a registered
// pending-close, and removes the entry regardless of kill success (a
// disappeared window is nothing to retry). wi is nil for paneless sessions,
// which never have a tracked window to match against.
//
// A close still held by the babysit guard is the one exception: the entry is
// kept and marked blocked instead of fired, because the agent's session
// ending is not the same as its PR being done. This is the path #787 is most
// likely hitting today — lazyboards' cleanup fires while the implement agent
// is mid-Phase-9, gets deferred here, and then the window is destroyed the
// instant that session exits, with `cenci babysit` still supervising CI.
func (d *Daemon) killPendingClose(wi *frontend.WindowInfo) {
	if wi == nil {
		return
	}
	key := pendingCloseKey(wi.Session, wi.WindowIndex)
	entry, ok := d.pending[key]
	if !ok {
		return
	}
	if blocked, reason := d.babysitBlocks(entry); blocked {
		entry.blocked = true
		entry.lastChecked = d.now()
		if d.cfg.Verbose {
			log.Printf("pending-close: holding %s (%s): %s", key, entry.pc.WindowName, reason)
		}
		return
	}
	delete(d.pending, key)
	d.killWindowByKey(key, "pending-close")
}

// retryBlockedPendingCloses re-evaluates every pending-close the babysit
// guard held back, firing the ones whose guard has since cleared (CI green,
// PR merged, supervisor stopped). Called from runSweep, and throttled per
// entry to pendingCloseRecheckInterval (#787).
//
// An entry is dropped as soon as its guard clears, whether or not the kill
// lands — a blocked entry whose window disappeared some other way (its
// session is already gone, so prunePendingCloseForWindow no longer sees it)
// therefore costs one logged kill failure at most, never an unbounded
// registry.
func (d *Daemon) retryBlockedPendingCloses() {
	now := d.now()
	for key, entry := range d.pending {
		if !entry.blocked || now.Sub(entry.lastChecked) < pendingCloseRecheckInterval {
			continue
		}
		entry.lastChecked = now
		if blocked, _ := d.babysitBlocks(entry); blocked {
			continue
		}
		delete(d.pending, key)
		if d.cfg.Verbose {
			log.Printf("pending-close: babysit guard cleared for %s (%s), closing now", key, entry.pc.WindowName)
		}
		d.killWindowByKey(key, "pending-close")
	}
}

// babysitBlocks asks the injected close guard whether entry's ticket is still
// owned by a live babysit supervisor. Windows whose name carries no ticket
// number (free-text/slug windows) have nothing to join a supervisor on and
// are never blocked — the guard fails open, matching `cenci close` (#787).
func (d *Daemon) babysitBlocks(entry *pendingCloseEntry) (bool, string) {
	if d.closeGuard == nil {
		return false, ""
	}
	ticket := detect.TicketFromWindowName(entry.pc.WindowName)
	if ticket == "" {
		return false, ""
	}
	return d.closeGuard(ticket)
}

// killWindowByKey kills the window a "session:index" registry key names:
// first the processes behind it, then the window itself (#1253). Both run off
// the event loop through runOffLoop, because the process kill waits out a grace
// window and a runtime exec can hang; the caller has already removed the
// registry entry on the loop. The off-loop function captures only locals --
// never d.sessions, d.pending or any other loop-owned state.
//
// A failed process kill is logged and never stops kill-window. A window that is
// already gone counts as closed and is logged only under Verbose.
func (d *Daemon) killWindowByKey(key, prefix string) {
	target := fmt.Sprintf("=%s", key)
	d.killWindowTarget(target, target, prefix)
}

// killWindowTarget is killWindowByKey for an explicit tmux target. desc names
// the window in log lines (it equals target for the pending-close path). A
// deferred kill must pass a target that cannot drift to another window between
// scheduling and the kill: a "=session:index" is resolved again at kill time,
// after the grace wait, so a renumber or index reuse in between would hit the
// wrong window; a pane id ("%N") is never reused while the tmux server runs.
func (d *Daemon) killWindowTarget(target, desc, prefix string) {
	killer, procKiller, verbose := d.killer, d.procKiller, d.cfg.Verbose
	d.runOffLoop(sandbox.GraceFromEnv()+sandbox.CloseKillMargin, func(ctx context.Context) {
		if procKiller != nil {
			if err := procKiller.KillWindowProcesses(ctx, target); err != nil {
				log.Printf("%s: error killing processes of %s: %v", prefix, desc, err)
			}
		}
		err := killer.KillWindow(target)
		switch {
		case errors.Is(err, tmux.ErrWindowGone):
			if verbose {
				log.Printf("%s: %s already gone, counted as closed", prefix, desc)
			}
		case err != nil:
			log.Printf("%s: error killing %s: %v", prefix, desc, err)
		}
	})
}

// prunePendingCloseForWindow removes any pending-close registry entry for
// wi's window. wi must be resolved by the caller *before* the state-mutating
// call that tears the window down (d.frontend.Sweep for runSweep,
// delete(d.sessions, key) for ttlSweep) — mirroring handleEvent's SessionEnd
// handling in event.go, which resolves WindowInfo before OnSessionEnd for
// the same reason. In particular, Sweep's own stale-window cleanup already
// forgets the window (via restoreWindow) before runSweep gets a chance to
// call WindowInfo on it, so resolving after Sweep returns would always see
// nil and silently no-op the prune (#522). Called from runSweep/ttlSweep so
// entries left behind by a window closed through other means (manual kill,
// --force elsewhere) don't grow the registry forever (#522 risk
// mitigation). A no-op when wi is nil, which covers paneless sessions (never
// have a tracked window to match against).
func (d *Daemon) prunePendingCloseForWindow(wi *frontend.WindowInfo) {
	if wi == nil {
		return
	}
	delete(d.pending, pendingCloseKey(wi.Session, wi.WindowIndex))
}
