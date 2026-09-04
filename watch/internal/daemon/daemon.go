package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/reap"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// windowKiller is the minimal seam the daemon needs to kill a tmux window on
// a pending-close match at SessionEnd. Production is (*tmux.ExecClient); kept
// as a small daemon-owned interface rather than growing frontend.Frontend or
// tmux.Client, mirroring internal/closecmd.windowKiller and the rationale
// documented on tmux.ExecClient.KillWindow (#522).
type windowKiller interface {
	KillWindow(target string) error
}

// Daemon manages the event-driven loop and per-session core state. All tmux
// interaction lives behind the injected frontend.
type Daemon struct {
	cfg           config.Config
	frontend      frontend.Frontend
	sessions      map[string]*frontend.SessionState // key: session ID (fallback "pane:<id>")
	ipc           *ipc.Server                       // nil if IPC not enabled
	events        <-chan ipc.HookEvent
	pendingCloses <-chan ipc.PendingClose // nil unless wired by Run (#522); loop skips this select case when nil
	now           func() time.Time        // injectable clock for TTL tests
	reaper        reap.Reaper             // triggers cenci sandbox reap-orphans on pane-gone sweep/startup (#292)
	killer        windowKiller            // kills a pending-close window's target at SessionEnd (#522)

	// closeGuard reports whether a window's ticket is still owned by a live
	// `cenci babysit` supervisor with CI not green, holding back a deferred
	// pending-close kill until it clears (#787). Production is
	// babysit.BlocksClose with an empty repo root: the daemon is
	// repo-agnostic (ipc.PendingClose carries no repo), so its match is by
	// ticket number alone — a benign cross-match across two repos
	// babysitting the same issue number only keeps a window open longer, and
	// `cenci close --force` overrides. Nil disables the guard entirely
	// (tests).
	closeGuard func(ticket string) (bool, string)

	// pending is the in-memory pending-close registry (#522), keyed by
	// "session:index" (the same identity closecmd kills by). Dropped on
	// daemon restart, per plan assumption; pruned when the owning session is
	// removed (runSweep/ttlSweep) to bound growth from windows closed by
	// other means.
	pending map[string]*pendingCloseEntry

	// attention is the reconciler's overlay of synthetic "failed"/"escalated"
	// windows (#46, extended by #826). It is appended to every snapshot
	// until the next overlay replaces it. Empty when the embedded dispatch
	// loop is disabled.
	attention []ipc.WindowState

	// headroom is the reconciler's per-agent-type token-budget headroom
	// overlay (#169), carried alongside attention on the same channel. Empty
	// when the embedded dispatch loop is disabled or no AgentLimits are
	// configured.
	headroom map[string]float64

	// dispatch is the embedded fleet dispatch loop's live state (#220),
	// carried alongside attention on the same channel. Nil until the loop's
	// first publish.
	dispatch *watch.DispatchState

	// armSpawn is the injectable seam a validated babysit-arm request (#1094)
	// delegates to, mirroring closeGuard/killer/reaper. It executes on the
	// ipc.EventReceiver's connection goroutine (see handleArmRequest), not
	// d.loop, so it must stay pure/bounded over immutable daemon state.
	// Production defaults to defaultArmSpawn (nacks every request with
	// ReasonHostRepoResolutionUnavailable); Run installs the real seam,
	// d.hostArmSpawn (armspawn.go), so newDaemon-based tests keep defaulting
	// to defaultArmSpawn and never shell out.
	armSpawn func(ipc.ArmRequest) ipc.ArmResponse

	// armLimiter is hostArmSpawn's mutex-guarded token bucket (armspawn.go),
	// gating the whole arm path -- before pane/repo resolution, not just the
	// fork (#1095 Decisions). Its zero value is a full bucket on first use.
	// Reached only from the ipc.EventReceiver connection goroutine (see
	// hostArmSpawn); it must never touch d.sessions/d.pending/d.attention or
	// any d.loop-owned field.
	armLimiter armTokenBucket
}

// newDaemon creates a Daemon with the given dependencies.
func newDaemon(cfg config.Config, fe frontend.Frontend, events <-chan ipc.HookEvent, reaper reap.Reaper) *Daemon {
	return &Daemon{
		cfg:      cfg,
		frontend: fe,
		sessions: make(map[string]*frontend.SessionState),
		events:   events,
		now:      time.Now,
		reaper:   reaper,
		killer:   &tmux.ExecClient{},
		pending:  make(map[string]*pendingCloseEntry),
		closeGuard: func(ticket string) (bool, string) {
			return babysit.BlocksClose(ticket, "", "")
		},
		armSpawn: defaultArmSpawn,
	}
}

// Run starts the event-driven daemon with the given interactive frontend.
// It blocks until ctx is cancelled, then cleans up. attention is the optional
// channel of reconciler failure overlays (#46); pass nil to leave the daemon's
// behavior unchanged. onStarted, if non-nil, is invoked once every listener
// (event socket, and broadcast socket when configured) is successfully bound
// — i.e. once this call has become the one live daemon — and before the main
// loop blocks. It is never invoked on the "already running" no-op path below,
// so a caller that writes a PID file from onStarted never clobbers a
// different, already-running daemon's PID file.
func Run(ctx context.Context, cfg config.Config, fe frontend.Frontend, attention <-chan ipc.AttentionUpdate, onStarted func()) error {
	// Start event receiver.
	recv, err := ipc.NewEventReceiver(cfg.EventSocketPath)
	if err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			log.Printf("daemon already running; nothing to do")
			return nil
		}
		return err
	}
	defer func() { _ = recv.Close() }()
	if cfg.Verbose {
		log.Printf("event socket: %s", cfg.EventSocketPath)
	}

	if os.Getenv("XDG_RUNTIME_DIR") == "" && (cfg.EventSocketPath == ipc.DefaultEventSocketPath() || cfg.SocketPath == ipc.DefaultSocketPath()) {
		log.Printf("warning: XDG_RUNTIME_DIR is not set; socket paths fall back to /tmp (less secure on multi-user systems)")
	}

	d := newDaemon(cfg, fe, recv.Events(), reap.NewExecReaper(cfg.Verbose))
	d.pendingCloses = recv.PendingCloses()
	// Install the real host-side arm seam (#1095): newDaemon's constructor
	// keeps defaulting to defaultArmSpawn so newDaemon-based tests never
	// shell out, and only a live Run installs the resolver/spawn seam.
	d.armSpawn = d.hostArmSpawn
	// Install the arm handler before Accept starts serving connections
	// (#1094): nothing between NewEventReceiver and Accept depends on
	// accepting, and a nil handler still nacks, but installing it first
	// keeps the window where an arm request could race an unset handler at
	// zero rather than merely small.
	recv.SetArmHandler(d.handleArmRequest)
	go recv.Accept(ctx)

	if cfg.SocketPath != "" {
		srv, err := ipc.NewServer(cfg.SocketPath)
		if err != nil {
			return err
		}
		d.ipc = srv
		go srv.Accept(ctx)
		defer func() { _ = srv.Close() }()
		if cfg.Verbose {
			log.Printf("broadcast socket: %s", cfg.SocketPath)
		}
	}
	if onStarted != nil {
		onStarted()
	}
	return d.loop(ctx, attention)
}

// loop is the main event-driven loop. The attention channel is always live
// now (main.go always constructs a non-nil channel and always starts
// RunCombinedLoop, per #220); disabled dispatch state flows through
// u.Dispatch.Enabled rather than through a nil channel.
func (d *Daemon) loop(ctx context.Context, attention <-chan ipc.AttentionUpdate) error {
	d.reapOnStartup()

	sweep := time.NewTicker(d.cfg.SweepInterval)
	defer sweep.Stop()

	for {
		select {
		case <-ctx.Done():
			d.cleanup()
			return nil
		case event := <-d.events:
			d.handleEvent(event)
		case pc := <-d.pendingCloses:
			d.registerPendingClose(pc)
		case <-sweep.C:
			d.runSweep()
		case u := <-attention:
			d.attention = u.Windows
			d.headroom = u.Headroom
			d.dispatch = u.Dispatch
			d.frontend.RenderHeadroom(u.Headroom)
			d.broadcast()
		}
	}
}

// runSweep runs the frontend sweep and the paneless TTL sweep, applying the
// resulting core-state changes and broadcasting once if anything changed. If
// any action in this pass flagged PaneGone, the orphan reaper is triggered
// exactly once for the whole pass (#292 AC1), not once per action.
func (d *Daemon) runSweep() {
	// Resolve WindowInfo for every in-scope session *before* calling Sweep:
	// Sweep's own stale-window cleanup already tears down (forgets) a
	// pane-gone window's tracking state internally, so resolving afterward
	// would always see nil and silently no-op the pending-close prune below
	// (#522).
	preSweep := make(map[string]*frontend.WindowInfo, len(d.sessions))
	for key := range d.sessions {
		preSweep[key] = d.frontend.WindowInfo(key)
	}

	actions := d.frontend.Sweep(d.sessions)
	changed := len(actions) > 0
	paneGone := false
	for _, a := range actions {
		if a.PaneGone {
			paneGone = true
		}
		sess := d.sessions[a.SessionKey]
		if sess == nil {
			continue
		}
		if a.Remove {
			d.prunePendingCloseForWindow(preSweep[a.SessionKey])
			delete(d.sessions, a.SessionKey)
			continue
		}
		if a.NewStatus != detect.StatusUnknown {
			sess.Status = a.NewStatus
			sess.TaskName = a.NewTask
			sess.AttentionSource = a.AttentionSource
			// A sweep that moves the session out of running has resolved the
			// turn the hold was covering — the expired-hold release (#1079)
			// and the ESC backstop both land here — so the hold ends with it.
			// Leaving it set would suppress the ESC backstop for the session's
			// whole remaining life.
			if a.NewStatus != detect.StatusRunning {
				sess.BackgroundHold = false
			}
			if d.cfg.Verbose && a.AttentionSource != "" {
				log.Printf("attention: session=%s source=%s", a.SessionKey, a.AttentionSource)
			}
		}
	}
	if paneGone && d.reaper != nil {
		d.reaper.Reap()
	}
	if d.backgroundHoldSweep() {
		changed = true
	}
	if d.ttlSweep() {
		changed = true
	}
	// Re-check pending-closes held back by the babysit guard *after* the
	// prune above, so an entry whose window has already disappeared is
	// reclaimed rather than retried against a dead target (#787).
	d.retryBlockedPendingCloses()
	if changed {
		d.broadcast()
	}
}

// reapOnStartup triggers one reap pass at daemon startup, covering panes that
// closed while the daemon was down or restarting (#292 AC2). Window state is
// in-memory only, so the live sweep alone has a restart blind spot.
func (d *Daemon) reapOnStartup() {
	if d.reaper != nil {
		d.reaper.Reap()
	}
}

// backgroundHoldSweep releases paneless sessions whose background hold (#698)
// has gone silent past frontend.BackgroundHoldTTL, marking them done — the
// core-side half of the release the tmux sweep performs for pane-backed
// windows (#1079). Paneless sessions (sandboxed or plain-terminal agents) have
// no frontend to sweep them, yet they surface in `cenci status` and every
// read-only widget exactly like tmux-backed ones, so a stuck hold is just as
// visible there. Reports whether any session changed.
func (d *Daemon) backgroundHoldSweep() bool {
	now := d.now()
	changed := false
	for key, sess := range d.sessions {
		if sess.TmuxPane != "" || !sess.BackgroundHold || sess.Status != detect.StatusRunning {
			continue
		}
		if now.Sub(sess.LastEvent) < frontend.BackgroundHoldTTL {
			continue
		}
		if d.cfg.Verbose {
			log.Printf("sweep: paneless session %s background hold expired after %s of silence, setting done", key, frontend.BackgroundHoldTTL)
		}
		sess.Status = detect.StatusDone
		sess.BackgroundHold = false
		changed = true
	}
	return changed
}

// ttlSweep expires paneless sessions that have been idle past the configured
// TTL. Pane-backed sessions are covered by the tmux pane sweep instead.
// Reports whether any session was removed.
func (d *Daemon) ttlSweep() bool {
	if d.cfg.SessionTTL <= 0 {
		return false
	}
	now := d.now()
	changed := false
	for key, sess := range d.sessions {
		if sess.TmuxPane != "" {
			continue
		}
		if now.Sub(sess.LastEvent) > d.cfg.SessionTTL {
			if d.cfg.Verbose {
				log.Printf("sweep: paneless session %s idle past TTL, removing", key)
			}
			d.prunePendingCloseForWindow(d.frontend.WindowInfo(key))
			delete(d.sessions, key)
			changed = true
		}
	}
	return changed
}

// cleanup releases all frontend presentation state (restores tmux windows).
func (d *Daemon) cleanup() {
	d.frontend.Cleanup(d.sessions)
}
