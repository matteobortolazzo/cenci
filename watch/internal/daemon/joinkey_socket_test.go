package daemon_test

// Ticket #1433 (P14, P38): the `<number>-<skill>` join key survives end to end on
// the dedicated cenci socket. Every leg runs its real code across the existing
// seams over tmuxtest.MockClient, with no live tmux:
//
//	run.Run (window launch) -> hook events -> daemon + tmux frontend (badges)
//	-> snapshot (socket, window name) -> closecmd.Run (kill target)
//
// The cenci socket path comes from tmux.ResolveSocket/TrustedSocketDir on the
// launch side and the same directory on the ingest side, over real Unix
// listeners, so neither side can quietly disagree. The test lives in the
// external package because internal/run imports internal/daemon.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/closecmd"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

const (
	joinKeyWindow = "40-implement"
	joinKeyPane   = "%40"

	// Badge glyphs are the acceptance criterion's literal values (running, needs
	// input, done); the styles come from the config defaults.
	symbolRunning   = "▶"
	symbolNeedInput = "!"
	symbolDone      = "✓"
)

// socketEnv is the hermetic tmux environment of one test: a private trusted
// directory under a short $TMUX_TMPDIR holding real Unix listeners for the cenci
// server and the user's default server.
type socketEnv struct{ cenci, def string }

// newSocketEnv clears $TMUX, points $TMUX_TMPDIR at a short test-owned directory
// (a Unix socket path is length-bound, and t.TempDir() embeds the test name) and
// listens on <dir>/tmux-<uid>/cenci and <dir>/tmux-<uid>/default, so the
// daemon's ingest check (an Lstat for a socket file) passes without a stub.
func newSocketEnv(t *testing.T) socketEnv {
	t.Helper()
	t.Setenv("TMUX", "")
	base, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", base)
	dir := filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := socketEnv{cenci: filepath.Join(dir, "cenci"), def: filepath.Join(dir, "default")}
	for _, path := range []string{env.cenci, env.def} {
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatalf("listen %s: %v", path, err)
		}
		t.Cleanup(func() { _ = ln.Close() })
	}
	if got, err := tmux.CenciSocketPath(); err != nil || got != env.cenci {
		t.Fatalf("CenciSocketPath() = %q, %v; the fixture assumes %q", got, err, env.cenci)
	}
	return env
}

// createdWindow is one window run.Run asked the controller to create.
type createdWindow struct{ socket, session, name, target string }

// serverCtrl is the run.Controller over tmuxtest.MockClient: NewWindow adds the
// window's pane to the mock server of the socket it was created on, and
// SetWindowOption translates run.Run's exact-match target (=session:=name) into
// the window target the daemon's frontend reads, so the `automatic-rename off`
// that run.Run really writes is what the frontend later finds.
type serverCtrl struct {
	mc      *tmuxtest.MockClient
	created []createdWindow
}

func (c *serverCtrl) CurrentSession(string) (string, error) {
	return "", errors.New("CurrentSession: the launch names its session explicitly")
}

func (c *serverCtrl) IsGroupedSession(string, string) (bool, error) { return false, nil }

func (c *serverCtrl) HasSession(string, string) (bool, error) { return true, nil }

func (c *serverCtrl) NewWindow(socket, session, name, _ string) error {
	panes := c.mc.PanesBySocket[socket]
	pane := tmux.PaneInfo{
		SessionName: strings.TrimPrefix(session, "="), WindowIndex: strconv.Itoa(len(panes) + 1),
		WindowName: name, PaneIndex: "0", PaneCurrentCmd: "claude", PaneTitle: "⠋ implementing", PaneID: joinKeyPane,
	}
	c.mc.PanesBySocket[socket] = append(panes, pane)
	c.created = append(c.created, createdWindow{socket: socket, session: session, name: name, target: pane.WindowTarget()})
	return nil
}

func (c *serverCtrl) SetWindowOption(socket, target, key, value string) error {
	sessionPart, name, ok := strings.Cut(target, ":=")
	if !ok {
		return fmt.Errorf("target %q is not the exact session:=name form", target)
	}
	for _, p := range c.mc.PanesBySocket[socket] {
		if p.SessionName == strings.TrimPrefix(sessionPart, "=") && p.WindowName == name {
			return c.mc.SetWindowOption(socket, p.WindowTarget(), key, value)
		}
	}
	return fmt.Errorf("no window %q on socket %s", target, socket)
}

// launch runs the real run.Run for opts over ctrl, pinned to a nonexistent
// config (built-ins only) with the daemon-start hook stubbed (the real one would
// re-exec this test binary), and requires a clean launch with no warnings.
func launch(t *testing.T, ctrl *serverCtrl, opts run.Opts) {
	t.Helper()
	var warn bytes.Buffer
	opts.ConfigPath = filepath.Join(t.TempDir(), "none.json")
	opts.EnsureDaemon = func() {}
	opts.Out, opts.Warn = io.Discard, &warn
	if err := run.Run(opts, ctrl); err != nil {
		t.Fatalf("run.Run: %v", err)
	}
	if warn.Len() != 0 {
		t.Fatalf("run.Run warned: %s", warn.String())
	}
}

func hookEvent(eventType, sessionID, pane, socket string) ipc.HookEvent {
	return ipc.HookEvent{EventType: eventType, SessionID: sessionID, Agent: "claude", TmuxPane: pane, TmuxSocket: socket}
}

// requireBadge checks the @cenci-symbol and @cenci-style the daemon last wrote on
// the window target of socket.
func requireBadge(t *testing.T, mc *tmuxtest.MockClient, socket, target, wantSymbol, wantStyle string) {
	t.Helper()
	symbol, _ := tmuxtest.FindSocketWindowOpt(mc.WindowOpts, socket, target, "@cenci-symbol")
	style, _ := tmuxtest.FindSocketWindowOpt(mc.WindowOpts, socket, target, "@cenci-style")
	if symbol != wantSymbol || style != wantStyle {
		t.Errorf("badge on %s %s = (%q, %q), want (%q, %q)", socket, target, symbol, style, wantSymbol, wantStyle)
	}
}

// requireNameKept fails when any rename of target is not the join key, or when
// the pane's window no longer carries it.
func requireNameKept(t *testing.T, mc *tmuxtest.MockClient, socket, target, name string) {
	t.Helper()
	for _, r := range mc.Renames {
		if r.Target == target && r.Name != name {
			t.Errorf("rename %+v overwrote the join key %q", r, name)
		}
	}
	panes := mc.PanesBySocket[socket]
	if socket == "" {
		panes = mc.Panes // the ambient server's panes
	}
	for _, p := range panes {
		if p.WindowTarget() == target && p.WindowName != name {
			t.Errorf("window %s on %s is named %q, want the join key %q", target, socket, p.WindowName, name)
		}
	}
}

// snapshotWindow returns the one snapshot window named name.
func snapshotWindow(t *testing.T, snap watch.StateSnapshot, name string) watch.WindowState {
	t.Helper()
	var found []watch.WindowState
	for _, w := range snap.Windows {
		if w.WindowName == name {
			found = append(found, w)
		}
	}
	if len(found) != 1 {
		t.Fatalf("snapshot has %d windows named %q, want 1 (windows: %+v)", len(found), name, snap.Windows)
	}
	return found[0]
}

// serverTarget is a (tmux socket, kill target) pair.
type serverTarget struct{ socket, target string }

// closeRecorder stands in for the process killer and the window killer behind
// `cenci close`, recording the (socket, target) of every call.
type closeRecorder struct{ procs, killed []serverTarget }

func (r *closeRecorder) KillProcesses(socket, target string) error {
	r.procs = append(r.procs, serverTarget{socket, target})
	return nil
}

func (r *closeRecorder) KillWindow(socket, target string) error {
	r.killed = append(r.killed, serverTarget{socket, target})
	return nil
}

// closeTicket runs the real closecmd.Run for target over the daemon's own
// snapshot and returns its decisions.
func closeTicket(t *testing.T, td *daemon.TestDaemon, rec *closeRecorder, target string) []closecmd.Decision {
	t.Helper()
	decisions, err := closecmd.Run(closecmd.Opts{
		Target: target,
		ReadSnapshot: func(string) (*watch.StateSnapshot, error) {
			snap := td.Snapshot()
			return &snap, nil
		},
		Killer:        rec,
		KillProcesses: rec.KillProcesses,
	})
	if err != nil {
		t.Fatalf("closecmd.Run(%q): %v", target, err)
	}
	return decisions
}

// P14: an agent window launched by `cenci run` -- manually, or the way dispatch
// launches it -- lands on the cenci server, keeps its `<number>-<skill>` name
// through the daemon's status updates, badges ▶ / ! / ✓ on that server, shows up
// in the snapshot with the cenci socket, and `cenci close` kills it there once
// its agent stops (and refuses while it is working).
func TestJoinKey_RunWindowBadgesSnapshotAndClose(t *testing.T) {
	cases := []struct {
		name string
		opts func(t *testing.T) run.Opts
	}{
		{"manual cenci run", func(t *testing.T) run.Opts {
			return run.Opts{Workflow: "implement", Ticket: "40", Session: "cenci"}
		}},
		{"dispatch-shaped launch", func(t *testing.T) run.Opts {
			socket, err := tmux.ResolveSocket()
			if err != nil {
				t.Fatalf("ResolveSocket: %v", err)
			}
			return run.Opts{
				Workflow: "implement", Ticket: ".plans/40-x.md", WindowTicket: "40", Session: "cenci",
				Unattended: true, TmuxSocket: socket, Dir: t.TempDir(),
			}
		}},
	}
	cfg := config.Default()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSocketEnv(t)
			mc := &tmuxtest.MockClient{PanesBySocket: map[string][]tmux.PaneInfo{}}
			ctrl := &serverCtrl{mc: mc}
			td := daemon.NewSocketTestDaemon(mc)

			launch(t, ctrl, tc.opts(t))

			if len(ctrl.created) != 1 {
				t.Fatalf("run.Run created %d windows (%+v), want 1", len(ctrl.created), ctrl.created)
			}
			win := ctrl.created[0]
			if win.socket != env.cenci || win.name != joinKeyWindow || win.session != "=cenci" {
				t.Fatalf("created window = %+v, want %s on %s in =cenci", win, joinKeyWindow, env.cenci)
			}

			send := func(eventType string) { td.HandleEvent(hookEvent(eventType, "sess-40", joinKeyPane, env.cenci)) }

			send("SessionStart")
			send("UserPromptSubmit")
			requireBadge(t, mc, env.cenci, win.target, symbolRunning, cfg.StyleRunning)
			rec := &closeRecorder{}
			decisions := closeTicket(t, td, rec, "40")
			if len(decisions) != 1 || decisions[0].Action != closecmd.ActionSkippedBusy {
				t.Fatalf("close while running = %+v, want one skipped-busy", decisions)
			}
			if len(rec.procs)+len(rec.killed) != 0 {
				t.Errorf("a busy window was killed: processes %v, windows %v", rec.procs, rec.killed)
			}

			send("PermissionRequest")
			requireBadge(t, mc, env.cenci, win.target, symbolNeedInput, cfg.StyleNeedInput)

			send("Stop")
			requireBadge(t, mc, env.cenci, win.target, symbolDone, cfg.StyleDone)
			requireNameKept(t, mc, env.cenci, win.target, joinKeyWindow)

			w := snapshotWindow(t, td.Snapshot(), joinKeyWindow)
			if w.Socket != env.cenci || w.PaneID != joinKeyPane || w.Session != "cenci" {
				t.Errorf("snapshot window = %+v, want socket %s, pane %s, session cenci", w, env.cenci, joinKeyPane)
			}

			decisions = closeTicket(t, td, rec, "40")
			if len(decisions) != 1 || decisions[0].Action != closecmd.ActionClosed {
				t.Fatalf("close after Stop = %+v, want one closed", decisions)
			}
			want := []serverTarget{{env.cenci, joinKeyPane}}
			if !reflect.DeepEqual(rec.procs, want) || !reflect.DeepEqual(rec.killed, want) {
				t.Errorf("kills: processes %v, windows %v; want both %v", rec.procs, rec.killed, want)
			}
		})
	}
}

// P38: a window already tracked on the user's default server (the `default`
// socket, or the ambient server an event without a socket names) is never
// relocated to the cenci server. Its events are interleaved with those of a
// window `cenci run` just launched on the cenci server: each window's badges,
// snapshot entry and close target stay on its own server, and no call for one
// window reaches the other's server.
func TestJoinKey_DefaultServerWindowsAreNeverRelocated(t *testing.T) {
	const (
		oldWindow  = "41-implement"
		oldPane    = "%41"
		oldTarget  = "main:1" // a different session from the cenci window's, so a target names one server
		newTarget  = "cenci:1"
		sessionOld = "sess-41"
		sessionNew = "sess-40"
	)
	servers := []struct {
		name   string
		socket func(socketEnv) string
	}{
		{"the default socket", func(e socketEnv) string { return e.def }},
		{"the ambient server", func(socketEnv) string { return "" }},
	}
	cfg := config.Default()
	for _, srv := range servers {
		t.Run(srv.name, func(t *testing.T) {
			env := newSocketEnv(t)
			own := srv.socket(env)
			mc := &tmuxtest.MockClient{PanesBySocket: map[string][]tmux.PaneInfo{}}
			// The window `cenci run` launched before the migration: named by the join
			// key, automatic-rename off.
			old := tmux.PaneInfo{
				SessionName: "main", WindowIndex: "1", WindowName: oldWindow, PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ older work", PaneID: oldPane,
			}
			if own == "" {
				mc.Panes = []tmux.PaneInfo{old}
				mc.WindowOptValues = map[string]string{oldTarget + ":automatic-rename": "off"}
			} else {
				mc.PanesBySocket[own] = []tmux.PaneInfo{old}
				mc.SocketWindowOptValues = map[string]map[string]string{own: {oldTarget + ":automatic-rename": "off"}}
			}
			ctrl := &serverCtrl{mc: mc}
			td := daemon.NewSocketTestDaemon(mc)

			launch(t, ctrl, run.Opts{Workflow: "implement", Ticket: "40", Session: "cenci"})
			if len(ctrl.created) != 1 || ctrl.created[0].socket != env.cenci {
				t.Fatalf("created windows = %+v, want exactly one, on the cenci socket %s", ctrl.created, env.cenci)
			}

			steps := []struct {
				eventType, symbol, style string
			}{
				{"SessionStart", cfg.SymbolIdle, cfg.StyleIdle},
				{"UserPromptSubmit", symbolRunning, cfg.StyleRunning},
				{"Stop", symbolDone, cfg.StyleDone},
			}
			for _, s := range steps {
				td.HandleEvent(hookEvent(s.eventType, sessionNew, joinKeyPane, env.cenci))
				td.HandleEvent(hookEvent(s.eventType, sessionOld, oldPane, own))
				requireBadge(t, mc, env.cenci, newTarget, s.symbol, s.style)
				requireBadge(t, mc, own, oldTarget, s.symbol, s.style)
			}

			// Every call for a window stayed on its own server.
			for _, s := range mc.Sockets {
				if s != env.cenci && s != own {
					t.Errorf("a tmux call went to socket %q, want only the cenci socket %q and the window's own %q", s, env.cenci, own)
				}
			}
			for _, c := range mc.WindowOpts {
				if c.Target == oldTarget && c.Socket != own {
					t.Errorf("window option %+v for the default-server window reached socket %q, want %q", c, c.Socket, own)
				}
				if c.Target == newTarget && c.Socket != env.cenci {
					t.Errorf("window option %+v for the cenci window reached socket %q, want %q", c, c.Socket, env.cenci)
				}
			}
			for _, r := range mc.Renames {
				if r.Target == oldTarget && r.Socket != own {
					t.Errorf("rename %+v for the default-server window reached socket %q, want %q", r, r.Socket, own)
				}
			}
			requireNameKept(t, mc, own, oldTarget, oldWindow)
			requireNameKept(t, mc, env.cenci, newTarget, joinKeyWindow)

			snap := td.Snapshot()
			if w := snapshotWindow(t, snap, oldWindow); w.Socket != own || w.PaneID != oldPane {
				t.Errorf("snapshot of the default-server window = %+v, want socket %q, pane %s", w, own, oldPane)
			}
			if w := snapshotWindow(t, snap, joinKeyWindow); w.Socket != env.cenci || w.PaneID != joinKeyPane {
				t.Errorf("snapshot of the cenci window = %+v, want socket %q, pane %s", w, env.cenci, joinKeyPane)
			}

			rec := &closeRecorder{}
			decisions := closeTicket(t, td, rec, "41")
			if len(decisions) != 1 || decisions[0].Action != closecmd.ActionClosed {
				t.Fatalf("close 41 = %+v, want one closed", decisions)
			}
			want := []serverTarget{{own, oldPane}}
			if !reflect.DeepEqual(rec.procs, want) || !reflect.DeepEqual(rec.killed, want) {
				t.Errorf("close 41 kills: processes %v, windows %v; want both %v (its own server)", rec.procs, rec.killed, want)
			}
		})
	}
}
