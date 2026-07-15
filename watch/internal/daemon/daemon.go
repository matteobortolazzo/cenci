package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/config"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/detect"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/frontend"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/reap"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/pkg/watch"
)

// Daemon manages the event-driven loop and per-session core state. All tmux
// interaction lives behind the injected frontend.
type Daemon struct {
	cfg      config.Config
	frontend frontend.Frontend
	sessions map[string]*frontend.SessionState // key: session ID (fallback "pane:<id>")
	ipc      *ipc.Server                       // nil if IPC not enabled
	events   <-chan ipc.HookEvent
	now      func() time.Time // injectable clock for TTL tests
	reaper   reap.Reaper      // triggers agent-sand --reap-orphans on pane-gone sweep/startup (#292)

	// attention is the reconciler's overlay of synthetic "failed" windows
	// (#46). It is appended to every snapshot until the next overlay replaces
	// it. Empty when the embedded dispatch loop is disabled.
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
	go recv.Accept(ctx)
	defer func() { _ = recv.Close() }()
	if cfg.Verbose {
		log.Printf("event socket: %s", cfg.EventSocketPath)
	}

	if os.Getenv("XDG_RUNTIME_DIR") == "" && (cfg.EventSocketPath == ipc.DefaultEventSocketPath() || cfg.SocketPath == ipc.DefaultSocketPath()) {
		log.Printf("warning: XDG_RUNTIME_DIR is not set; socket paths fall back to /tmp (less secure on multi-user systems)")
	}

	d := newDaemon(cfg, fe, recv.Events(), reap.NewExecReaper(cfg.Verbose))

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
			delete(d.sessions, a.SessionKey)
			continue
		}
		if a.NewStatus != detect.StatusUnknown {
			sess.Status = a.NewStatus
			sess.TaskName = a.NewTask
		}
	}
	if paneGone && d.reaper != nil {
		d.reaper.Reap()
	}
	if d.ttlSweep() {
		changed = true
	}
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
