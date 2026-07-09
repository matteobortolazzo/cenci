package daemon

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/config"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/tmux"
)

// Daemon manages the event-driven loop and per-window state.
type Daemon struct {
	cfg     config.Config
	client  tmux.Client
	windows map[string]*windowState // key: window target (session:windowIdx)
	panes   map[string]string       // key: pane ID (%5) → window target
	ipc     *ipc.Server             // nil if IPC not enabled
	events  <-chan ipc.HookEvent
}

// newDaemon creates a Daemon with the given dependencies.
func newDaemon(cfg config.Config, client tmux.Client, events <-chan ipc.HookEvent) *Daemon {
	return &Daemon{
		cfg:     cfg,
		client:  client,
		windows: make(map[string]*windowState),
		panes:   make(map[string]string),
		events:  events,
	}
}

// Run starts the event-driven daemon. It blocks until ctx is cancelled, then cleans up.
func Run(ctx context.Context, cfg config.Config) error {
	// Start event receiver.
	recv, err := ipc.NewEventReceiver(cfg.EventSocketPath)
	if err != nil {
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

	d := newDaemon(cfg, &tmux.ExecClient{}, recv.Events())

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
	return d.loop(ctx)
}

// loop is the main event-driven loop.
func (d *Daemon) loop(ctx context.Context) error {
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
			d.sweepStale()
		}
	}
}

// cleanup restores all tracked windows.
func (d *Daemon) cleanup() {
	if d.cfg.Verbose && len(d.windows) > 0 {
		log.Printf("cleaning up %d tracked window(s)", len(d.windows))
	}
	for wt := range d.windows {
		d.restoreWindow(wt)
	}
}
