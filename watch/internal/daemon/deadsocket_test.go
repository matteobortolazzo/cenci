package daemon

// Ticket #1462: a session recorded on a tmux socket whose server is gone is no
// longer tracked. These tests drive the daemon end to end over a real tmux
// frontend (tmuxtest.MockClient) through handleEvent and runSweep, with the real
// ingest file check and the frontend's real "socket file gone" rule over real
// Unix-socket fixtures: closing a listener removes its socket file exactly as a
// cleanly exiting tmux server does, and the mock's listing error stands in for
// the dead server.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// realTrustedSocketDir points TMUX_TMPDIR at a short private temp base (a Unix
// socket path is length-bound, and t.TempDir() embeds the test name), creates the
// real trusted directory under it with mode 0700 and returns it. d keeps the real
// directory resolution and the real socket-file check, so the ingest check and
// the frontend's default socketGone both run over real files.
func realTrustedSocketDir(t *testing.T, d *Daemon) string {
	t.Helper()
	base, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Setenv("TMUX_TMPDIR", base)
	dir, err := tmux.TrustedSocketDir()
	if err != nil {
		t.Fatalf("TrustedSocketDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d.tmuxSocketDir = tmux.TrustedSocketDir
	d.tmuxSocketFile = tmux.CheckSocketFile
	return dir
}

// AC4 and AC9 at the daemon: when the server behind a tracked socket exits (its
// socket file is removed and its listing fails), one sweep drops the window and
// removes the session from the daemon's sessions map, no tmux call reaches the
// dead socket, and the removal alone does not trigger the orphan reaper. The
// second half is the control that the reaper counter is wired: a pane that goes
// away on a healthy server does reap.
func TestSessionOnASocketWhoseServerExitedIsRemovedWithoutReaping(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	dir := realTrustedSocketDir(t, d)
	dead, live := filepath.Join(dir, "dead"), filepath.Join(dir, "live")
	closeDead := listenShortSocket(t, dead)
	listenShortSocket(t, live)
	mc.PanesBySocket = map[string][]tmux.PaneInfo{
		dead: {claudePane("bash", "0", "%5", "dead task")},
		live: {claudePane("bash", "0", "%6", "live task")},
	}
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "on-dead", TmuxPane: "%5", TmuxSocket: dead})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "on-live", TmuxPane: "%6", TmuxSocket: live})
	if d.frontend.WindowInfo("on-dead") == nil || d.frontend.WindowInfo("on-live") == nil {
		t.Fatal("setup: both sessions must be tracked before the server exits")
	}
	reaper := d.reaper.(*mockReaper)

	closeDead()
	mc.ListErrBySocket = map[string]error{dead: errors.New("error connecting to " + dead + " (No such file or directory)")}
	renames, windowOpts, opts := len(mc.Renames), len(mc.WindowOpts), len(mc.Opts)
	d.runSweep()

	if _, ok := d.sessions["on-dead"]; ok {
		t.Error("the session on the dead socket is still in d.sessions after one sweep")
	}
	if d.frontend.WindowInfo("on-dead") != nil {
		t.Error("the window on the dead socket is still tracked after one sweep")
	}
	if _, ok := d.sessions["on-live"]; !ok || d.frontend.WindowInfo("on-live") == nil {
		t.Error("the session on the healthy socket was lost")
	}
	if got := reaper.calls.Load(); got != 0 {
		t.Errorf("reaper calls = %d after a dead-socket removal, want 0", got)
	}
	for _, r := range mc.Renames[renames:] {
		if r.Socket == dead {
			t.Errorf("rename %+v went to the dead socket", r)
		}
	}
	for _, o := range mc.WindowOpts[windowOpts:] {
		if o.Socket == dead {
			t.Errorf("window option %+v went to the dead socket", o)
		}
	}
	for _, o := range mc.Opts[opts:] {
		if o.Socket == dead {
			t.Errorf("option %+v went to the dead socket", o)
		}
	}

	mc.PanesBySocket[live] = nil // control: a pane that vanishes on a healthy server reaps
	d.runSweep()

	if _, ok := d.sessions["on-live"]; ok {
		t.Error("control: the session whose pane vanished on the healthy socket was kept")
	}
	if got := reaper.calls.Load(); got != 1 {
		t.Errorf("control: reaper calls = %d after a pane-gone removal, want 1", got)
	}
}

// A pending close registered for a window on a dead socket is pruned when the
// session is removed, as ttlSweep does through prunePendingCloseForWindow, and it
// fires no kill. The registration is keyed (socket, pane): a registration for the
// same pane id on the default server is another window's and stays.
func TestPendingCloseForAWindowOnADeadSocketIsPruned(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	dir := realTrustedSocketDir(t, d)
	socket := filepath.Join(dir, "work")
	closeListener := listenShortSocket(t, socket)
	mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "writing tests")}}
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5", TmuxSocket: socket})
	d.registerPendingClose(ipc.PendingClose{PaneID: "%5", Session: "main", WindowIndex: "0", WindowName: "bash", Socket: socket})
	d.registerPendingClose(ipc.PendingClose{PaneID: "%5", Session: "main", WindowIndex: "0", WindowName: "bash"})
	if len(d.pending) != 2 {
		t.Fatalf("setup: pending = %+v, want both registrations", d.pending)
	}

	closeListener()
	mc.ListErrBySocket = map[string]error{socket: errors.New("no server running")}
	d.runSweep()

	if _, ok := d.pending[pendingKey{socket: socket, pane: "%5"}]; ok {
		t.Errorf("pending = %+v, want the dead socket's registration pruned", d.pending)
	}
	if _, ok := d.pending[pendingKey{pane: "%5"}]; !ok || len(d.pending) != 1 {
		t.Errorf("pending = %+v, want only the default server's registration kept", d.pending)
	}
	if killed := d.killer.(*fakeKiller).killed; len(killed) != 0 {
		t.Errorf("killed = %q, want no kill for a dead socket's window", killed)
	}
}

// An event that arrives after the socket was declared dead and names the same
// path with a live socket file at it is ingested again: tracked on that socket,
// and the socket is listed on the next tick.
func TestEventAfterASocketDiedTracksItAgainAndListsItOnTheNextTick(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	dir := realTrustedSocketDir(t, d)
	socket := filepath.Join(dir, "work")
	closeListener := listenShortSocket(t, socket)
	mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "first run")}}
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "first", TmuxPane: "%5", TmuxSocket: socket})
	closeListener()
	mc.ListErrBySocket = map[string]error{socket: errors.New("no server running")}
	d.runSweep()
	if _, ok := d.sessions["first"]; ok {
		t.Fatal("setup: the session on the dead socket must be gone before the server comes back")
	}

	listenShortSocket(t, socket) // a new server at the same path
	mc.ListErrBySocket = nil
	mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "second run")}}
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "second", TmuxPane: "%5", TmuxSocket: socket})

	wi := d.frontend.WindowInfo("second")
	if wi == nil || wi.Socket != socket {
		t.Fatalf("WindowInfo = %+v, want the window tracked again on %q", wi, socket)
	}
	mc.ListSockets = nil
	d.runSweep()

	listed := false
	for _, s := range mc.ListSockets {
		listed = listed || s == socket
	}
	if !listed {
		t.Errorf("ListPanes sockets on the next tick = %q, want %q listed again", mc.ListSockets, socket)
	}
	if _, ok := d.sessions["second"]; !ok {
		t.Error("the re-ingested session was removed on the next tick")
	}
}
