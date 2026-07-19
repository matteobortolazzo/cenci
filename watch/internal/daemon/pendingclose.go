package daemon

import (
	"fmt"
	"log"

	"github.com/matteobortolazzo/cenci/watch/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

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
	d.pending[key] = pc
	if d.cfg.Verbose {
		log.Printf("pending-close: registered %s (%s)", key, pc.WindowName)
	}
}

// killPendingClose kills wi's window if it matches a registered
// pending-close, and removes the entry regardless of kill success (a
// disappeared window is nothing to retry). wi is nil for paneless sessions,
// which never have a tracked window to match against.
func (d *Daemon) killPendingClose(wi *frontend.WindowInfo) {
	if wi == nil {
		return
	}
	key := pendingCloseKey(wi.Session, wi.WindowIndex)
	if _, ok := d.pending[key]; !ok {
		return
	}
	delete(d.pending, key)
	target := fmt.Sprintf("=%s", key)
	if err := d.killer.KillWindow(target); err != nil {
		log.Printf("pending-close: error killing %s: %v", target, err)
	}
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
