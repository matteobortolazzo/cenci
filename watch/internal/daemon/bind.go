package daemon

// Ticket #1529: the daemon half of `cenci bind <session-id>`. A bind pins a
// Codex session to the tmux pane the caller runs in, after the frontend proves
// the pane is listed on the caller's trusted socket under the caller's server
// pid. The connection goroutine validates the request shape and the rate limit;
// the loop, which owns d.sessions, applies it. Bindings live in memory only and
// are dropped on daemon restart, like the pending-close registry.

import (
	"errors"
	"log"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/measurement"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// Nack reasons of the codex-bind request (#1529). Like the arm and babysit
// control reasons they are plain exported constants (not CENCI-* codes) that
// `cenci bind` relays verbatim on one stderr line, so none carries host detail.
const (
	ReasonBindInvalidRequest     = "invalid bind request"
	ReasonBindRateLimited        = "bind requests are rate limited"
	ReasonBindTimedOut           = "the daemon did not apply the bind in time; check cenci status"
	ReasonBindUnknownSession     = "unknown session"
	ReasonBindNotCodex           = "not a Codex session"
	ReasonBindUntrustedSocket    = "tmux socket is not in the trusted socket directory"
	ReasonBindPaneNotListed      = "pane is not listed on that tmux socket"
	ReasonBindServerPIDMismatch  = "tmux server pid does not match the live server"
	ReasonBindListingFailed      = "tmux pane listing failed"
	ReasonBindNonCodexPaneHolder = "pane is tracked by a non-Codex session"
)

// bindBudget bounds handleBind's whole wait on the loop: handing the call over
// and reading the reply. It stays under ipc's 15s client deadline so a slow loop
// is answered with ReasonBindTimedOut rather than read as no response. A var
// only so a test can shorten it.
var bindBudget = 10 * time.Second

// bindCall hands a validated bind to the loop. reply has capacity 1, so the
// loop's send never blocks even when handleBind already gave up.
type bindCall struct {
	req   ipc.BindRequest
	reply chan ipc.BindResponse
}

// handleBind is the receiver's codex-bind handler. It runs on the connection
// goroutine: it reads only d.cfg and the mutex-guarded d.bindLimiter, and
// reaches loop-owned state only through d.binds.
func (d *Daemon) handleBind(req ipc.BindRequest) ipc.BindResponse {
	if !measurement.ValidSessionID(req.SessionID) || !sandbox.ValidPaneID(req.TmuxPane) ||
		req.TmuxSocket == "" || !tmux.ValidSocket(req.TmuxSocket) || !validServerPID(req.TmuxServerPID) {
		return d.nackBind(req, ReasonBindInvalidRequest)
	}
	if !d.bindLimiter.take() {
		return d.nackBind(req, ReasonBindRateLimited)
	}
	call := bindCall{req: req, reply: make(chan ipc.BindResponse, 1)}
	timeout := time.NewTimer(bindBudget)
	defer timeout.Stop()
	select {
	case d.binds <- call:
	case <-timeout.C:
		return d.nackBind(req, ReasonBindTimedOut)
	}
	select {
	case resp := <-call.reply:
		return resp
	case <-timeout.C:
		return d.nackBind(req, ReasonBindTimedOut)
	}
}

// applyBind applies a validated bind on the loop. The socket is checked with no
// default-server fallback (unlike a hook event's), and the pane, its window and
// the server pid are proven by one listing before any state or window changes.
// The frontend tracks windows, not panes, so every other session tracking the
// pane or any pane of its window is a holder: a non-Codex holder refuses the
// bind, and only then do changes start. The session's previous window is
// restored, Codex holders are detached to paneless, the binding is recorded and
// the bound window is styled at once. That restyle re-lists the panes: if the
// pane vanished or its server pid changed since the proof, the bind is nacked
// late, after the previous window was restored and the holders detached, so the
// binder is left paneless and unproven and the holders stay detached.
func (d *Daemon) applyBind(req ipc.BindRequest) ipc.BindResponse {
	sess := d.sessions[req.SessionID]
	if sess == nil {
		return d.nackBind(req, ReasonBindUnknownSession)
	}
	if sess.Agent != "codex" {
		return d.nackBind(req, ReasonBindNotCodex)
	}
	socket, rejection := d.sanitizeTmuxSocket(req.TmuxSocket)
	if rejection != "" || socket == "" {
		return d.nackBind(req, ReasonBindUntrustedSocket)
	}
	window, err := d.frontend.CheckPane(socket, req.TmuxPane, req.TmuxServerPID)
	if err != nil {
		reason := ReasonBindListingFailed
		switch {
		case errors.Is(err, frontend.ErrPaneNotListed):
			reason = ReasonBindPaneNotListed
		case errors.Is(err, frontend.ErrServerPIDMismatch):
			reason = ReasonBindServerPIDMismatch
		}
		return d.nackBind(req, reason)
	}

	// samePane holders track the bound pane itself; the others track another
	// pane of the bound window, which the restyle would otherwise discard as
	// stale and evict.
	samePane := make(map[string]bool)
	for key, s := range d.sessions {
		if key == req.SessionID {
			continue
		}
		onPane := s.TmuxPane == req.TmuxPane && s.TmuxSocket == socket
		if !onPane && !d.tracksWindow(key, socket, window) {
			continue
		}
		if s.Agent != "codex" {
			return d.nackBind(req, ReasonBindNonCodexPaneHolder)
		}
		samePane[key] = onPane
	}
	if sess.TmuxPane != "" && (sess.TmuxPane != req.TmuxPane || sess.TmuxSocket != socket) {
		d.frontend.OnSessionEnd(sess)
	}
	detached := make([]string, 0, len(samePane))
	for key, onPane := range samePane {
		h := d.sessions[key]
		if onPane {
			// Keep the window tracked for the binder, who re-keys it below.
			d.frontend.OnSessionHandoff(h)
		} else {
			// Restore the window's originals first, so the binder's tracking
			// below records them rather than the holder's styling.
			d.frontend.OnSessionEnd(h)
		}
		if h.SessionID == "" {
			// A pane-keyed session cannot exist without its pane.
			delete(d.sessions, key)
		} else {
			detachPaneless(h, false)
		}
		detached = append(detached, key)
	}

	sess.Binding = &frontend.PaneBinding{Socket: socket, Pane: req.TmuxPane, ServerPID: req.TmuxServerPID}
	sess.TmuxPane, sess.TmuxSocket = req.TmuxPane, socket
	sess.TmuxServerPID, sess.RequireServerPID = req.TmuxServerPID, true
	sess.MissingTmuxPane, sess.MissingTmuxSocket = "", ""
	sess.Unproven = false
	obs := d.frontend.OnEvent(sess, ipc.HookEvent{SessionID: sess.SessionID, Agent: sess.Agent})
	d.applyObservations(req.SessionID, sess, obs)
	if sess.Binding == nil {
		// The pane changed between the proof and the restyle; the session was
		// left paneless, so the bind is not recorded. The previous window was
		// already restored and the holders detached: they stay so.
		if d.cfg.Verbose {
			for _, key := range detached {
				log.Printf("bind: Codex session %q stays detached from pane %q although the bind of session %q was refused late", key, req.TmuxPane, req.SessionID)
			}
		}
		d.broadcast()
		if obs.ServerPIDMismatch {
			return d.nackBind(req, ReasonBindServerPIDMismatch)
		}
		return d.nackBind(req, ReasonBindPaneNotListed)
	}
	if d.cfg.Verbose {
		for _, key := range detached {
			log.Printf("bind: Codex session %q detached from pane %q's window, now bound to session %q", key, req.TmuxPane, req.SessionID)
		}
		log.Printf("bind: Codex session %q bound to pane %q", req.SessionID, req.TmuxPane)
	}
	d.broadcast()
	return ipc.BindResponse{OK: true, Pane: req.TmuxPane}
}

// tracksWindow reports whether the frontend tracks the session key's window as
// window (session:window_index) on socket.
func (d *Daemon) tracksWindow(key, socket, window string) bool {
	wi := d.frontend.WindowInfo(key)
	return wi != nil && wi.Socket == socket && wi.Session+":"+wi.WindowIndex == window
}

// nackBind logs a refused bind under cfg.Verbose (watch/docs/hook-events.md
// rule 2) and returns the nack. Request values come from a socket, so they are
// quoted, never printed raw.
func (d *Daemon) nackBind(req ipc.BindRequest, reason string) ipc.BindResponse {
	if d.cfg.Verbose {
		log.Printf("bind: rejected session=%q pane=%q: %s", req.SessionID, req.TmuxPane, reason)
	}
	return ipc.BindResponse{OK: false, Reason: reason}
}
