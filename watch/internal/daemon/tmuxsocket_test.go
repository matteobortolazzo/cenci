package daemon

// Ticket #1429: each window records the tmux server socket it lives on, and the
// daemon keys its tmux frontend by (socket, target). The event socket also
// receives events from containers, so a hook event's tmux_socket is checked at
// daemon ingest, not only in `cenci notify`: any process in a container can
// write a raw event line. Every case drives a real tmux frontend over
// tmuxtest.MockClient through handleEvent; the raw-ingest cases feed the events
// through a real ipc.EventReceiver so the wire key itself is exercised.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// privateDir returns a fresh directory with mode 0700 (t.TempDir() is 0755, which
// the trusted-socket check rejects: watch/docs/test-isolation.md).
func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// trustedSocketDir points d's ingest check at a fresh 0700 fixture directory and
// returns it. A socket directly under it passes the path check; the socket file
// need not exist, because the file check (tmuxSocketFile, #1462) is stubbed to
// accept. The tests of that check itself restore the real one over a real
// listener (realTrustedSocketDir).
func trustedSocketDir(t *testing.T, d *Daemon) string {
	t.Helper()
	dir := privateDir(t)
	d.tmuxSocketDir = func() (string, error) { return dir, nil }
	d.tmuxSocketFile = func(string) error { return nil }
	return dir
}

// claudePane is a pane running Claude with a working-spinner title.
func claudePane(window, index, id, task string) tmux.PaneInfo {
	return tmux.PaneInfo{
		SessionName: "main", WindowIndex: index, WindowName: window, PaneIndex: "0",
		PaneCurrentCmd: "claude", PaneTitle: "⠋ " + task, PaneID: id,
	}
}

// serveRawLines serves a real EventReceiver and returns it with a function that
// writes one raw JSON line to its socket.
func serveRawLines(t *testing.T) (*ipc.EventReceiver, func(line string)) {
	t.Helper()
	// Short path: t.TempDir() embeds the full test name and can overflow sun_path.
	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "events.sock")
	recv, err := ipc.NewEventReceiver(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go recv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	return recv, func(line string) {
		t.Helper()
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.Close()
	}
}

// newRawFeeder writes one raw JSON line to a real EventReceiver's socket and
// hands the event the receiver decoded to d, as the daemon loop would.
func newRawFeeder(t *testing.T, d *Daemon) func(line string) {
	t.Helper()
	recv, send := serveRawLines(t)
	return func(line string) {
		t.Helper()
		send(line)
		select {
		case ev := <-recv.Events():
			d.handleEvent(ev)
		case <-time.After(2 * time.Second):
			t.Fatalf("the receiver delivered no event for %s", line)
		}
	}
}

// rawHookLine is a hook event exactly as a container process would write it: a
// JSON object with a literal tmux_socket key, never going through ipc.HookEvent.
func rawHookLine(t *testing.T, eventType, sessionID, pane string, socket *string) string {
	t.Helper()
	m := map[string]string{
		"event_type": eventType,
		"session_id": sessionID,
		"tmux_pane":  pane,
		"timestamp":  "2024-01-01T00:00:00Z",
	}
	if socket != nil {
		m["tmux_socket"] = *socket
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// linesContaining returns the captured log lines containing substr.
func linesContaining(logs, substr string) []string {
	var out []string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

// snapshotJSON is the broadcast snapshot as a consumer reads it off the wire.
func snapshotJSON(t *testing.T, d *Daemon) string {
	t.Helper()
	b, err := json.Marshal(d.buildSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// P24: a raw hook event whose tmux_socket fails the trusted-socket check is still
// tracked, with an empty socket on the ambient server, and logs exactly one
// verbose-gated line that names the event and the session. The first case is the
// control that proves a trusted socket does reach the daemon on this very path,
// so the empty-socket assertions below cannot pass vacuously.
func TestRawHookEventWithUntrustedTmuxSocketIsTrackedWithEmptySocket(t *testing.T) {
	t.Run("control: a trusted socket is kept", func(t *testing.T) {
		mc := &tmuxtest.MockClient{}
		d := newTestDaemon(mc)
		d.cfg.Verbose = true
		dir := trustedSocketDir(t, d)
		socket := filepath.Join(dir, "default")
		mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "writing tests")}}
		logs := captureLog(t)

		newRawFeeder(t, d)(rawHookLine(t, "SessionStart", "raw-1", "%5", &socket))

		wi := d.frontend.WindowInfo("raw-1")
		if wi == nil || wi.Socket != socket {
			t.Fatalf("WindowInfo = %+v, want the window tracked on the trusted socket %q", wi, socket)
		}
		if lines := linesContaining(logs.String(), "tmux_socket"); len(lines) != 0 {
			t.Errorf("a trusted socket logged %q, want nothing", lines)
		}
	})

	t.Run("control: a printable non-ASCII name is kept", func(t *testing.T) {
		mc := &tmuxtest.MockClient{}
		d := newTestDaemon(mc)
		d.cfg.Verbose = true
		dir := trustedSocketDir(t, d)
		socket := filepath.Join(dir, "défault")
		mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "writing tests")}}
		logs := captureLog(t)

		newRawFeeder(t, d)(rawHookLine(t, "SessionStart", "raw-1", "%5", &socket))

		wi := d.frontend.WindowInfo("raw-1")
		if wi == nil || wi.Socket != socket {
			t.Fatalf("WindowInfo = %+v, want the window tracked on the printable non-ASCII socket %q", wi, socket)
		}
		if lines := linesContaining(logs.String(), "tmux_socket"); len(lines) != 0 {
			t.Errorf("a printable non-ASCII socket logged %q, want nothing", lines)
		}
	})

	tests := []struct {
		name  string
		class string // "malformed" or "untrusted"
		setup func(t *testing.T, d *Daemon, trusted string) string
	}{
		{"invalid UTF-8 in the name", "malformed", func(t *testing.T, d *Daemon, trusted string) string {
			return trusted + "/a\xffb"
		}},
		{"outside the trusted directory", "untrusted", func(t *testing.T, d *Daemon, trusted string) string {
			return filepath.Join(privateDir(t), "default")
		}},
		{"nested below the trusted directory", "untrusted", func(t *testing.T, d *Daemon, trusted string) string {
			return filepath.Join(trusted, "a", "b")
		}},
		{"unclean path", "untrusted", func(t *testing.T, d *Daemon, trusted string) string {
			return trusted + "/../x"
		}},
		{"tab in the name", "malformed", func(t *testing.T, d *Daemon, trusted string) string {
			return trusted + "/de\tfault"
		}},
		{"control bytes in the name", "malformed", func(t *testing.T, d *Daemon, trusted string) string {
			return trusted + "/a\r\x1b[2K"
		}},
		{"DEL in the name", "malformed", func(t *testing.T, d *Daemon, trusted string) string {
			return trusted + "/a\x7fb"
		}},
		{"UTF-8 C1 control in the name", "malformed", func(t *testing.T, d *Daemon, trusted string) string {
			return trusted + "/a\u009bb"
		}},
		{"bidi override in the name", "malformed", func(t *testing.T, d *Daemon, trusted string) string {
			return trusted + "/a\u202eb"
		}},
		{"relative path", "malformed", func(t *testing.T, d *Daemon, trusted string) string {
			return "tmux-1000/default"
		}},
		{"directory with group and other bits", "untrusted", func(t *testing.T, d *Daemon, trusted string) string {
			loose := t.TempDir()
			if err := os.Chmod(loose, 0o755); err != nil {
				t.Fatal(err)
			}
			d.tmuxSocketDir = func() (string, error) { return loose, nil }
			return filepath.Join(loose, "default")
		}},
		{"symlinked directory", "untrusted", func(t *testing.T, d *Daemon, trusted string) string {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(trusted, link); err != nil {
				t.Fatal(err)
			}
			d.tmuxSocketDir = func() (string, error) { return link, nil }
			return filepath.Join(link, "default")
		}},
		{"directory resolution error", "untrusted", func(t *testing.T, d *Daemon, trusted string) string {
			d.tmuxSocketDir = func() (string, error) { return "", errors.New("no tmux tmpdir") }
			return filepath.Join(trusted, "default")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{claudePane("bash", "0", "%5", "writing tests")}}
			d := newTestDaemon(mc)
			d.cfg.Verbose = true
			socket := tc.setup(t, d, trustedSocketDir(t, d))
			logs := captureLog(t)

			if utf8.ValidString(socket) {
				newRawFeeder(t, d)(rawHookLine(t, "SessionStart", "raw-1", "%5", &socket))
			} else {
				// JSON decoding replaces invalid UTF-8 with U+FFFD, so such a value
				// never arrives over the wire; feed the event itself, the check is
				// defense in depth for it.
				d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "raw-1", TmuxPane: "%5", TmuxSocket: socket})
			}

			wi := d.frontend.WindowInfo("raw-1")
			if wi == nil {
				t.Fatal("WindowInfo = nil, want the window tracked on the ambient server")
			}
			if wi.Socket != "" {
				t.Errorf("WindowInfo.Socket = %q, want empty", wi.Socket)
			}
			for i, s := range mc.Sockets {
				if s != "" {
					t.Errorf("call %d went to socket %q, want every call on the ambient server", i, s)
				}
			}
			if want := []string{""}; !reflect.DeepEqual(mc.ListSockets, want) {
				t.Errorf("ListPanes sockets = %q, want %q", mc.ListSockets, want)
			}
			if js := snapshotJSON(t, d); strings.Contains(js, `"socket"`) {
				t.Errorf("snapshot = %s, want no socket key for an empty socket", js)
			}
			lines := linesContaining(logs.String(), "tmux_socket")
			if len(lines) != 1 {
				t.Fatalf("log has %d tmux_socket lines %q, want exactly one", len(lines), lines)
			}
			for _, want := range []string{"SessionStart", "raw-1", tc.class} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("log line %q does not name %q", lines[0], want)
				}
			}
			if tc.class == "malformed" && strings.Contains(logs.String(), socket) {
				t.Errorf("log %q echoes the malformed value %q", logs.String(), socket)
			}
		})
	}
}

// The rejection line is verbose-gated: with Verbose off an untrusted value is
// still dropped to the empty socket, but nothing is logged. The verbose run is the
// control that the line exists at all.
func TestUntrustedTmuxSocketLogIsVerboseGated(t *testing.T) {
	for _, tc := range []struct {
		verbose   bool
		wantLines int
	}{{true, 1}, {false, 0}} {
		mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{claudePane("bash", "0", "%5", "writing tests")}}
		d := newTestDaemon(mc)
		d.cfg.Verbose = tc.verbose
		trustedSocketDir(t, d)
		socket := filepath.Join(privateDir(t), "default") // not under the trusted directory
		logs := captureLog(t)

		newRawFeeder(t, d)(rawHookLine(t, "SessionStart", "raw-1", "%5", &socket))

		if got := len(linesContaining(logs.String(), "tmux_socket")); got != tc.wantLines {
			t.Errorf("verbose=%v: %d tmux_socket log lines, want %d", tc.verbose, got, tc.wantLines)
		}
		if wi := d.frontend.WindowInfo("raw-1"); wi == nil || wi.Socket != "" {
			t.Errorf("verbose=%v: WindowInfo = %+v, want tracked with an empty socket", tc.verbose, wi)
		}
	}
}

// #1462 AC1 and AC2: a socket path directly under the trusted directory that is
// not an existing Unix socket (no file, or a regular file) is rejected at ingest
// like any other untrusted value: the event is tracked on the default server and
// exactly one verbose-gated line names `untrusted`. The real file check runs
// over a real fixture directory; PanesBySocket gives the forged socket a pane
// too, so a wrongly kept socket shows as a socket on WindowInfo, not as a
// missing window.
func TestRawHookEventWhoseSocketIsNotAnExistingSocketIsTrackedWithEmptySocket(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"no file at the path", func(t *testing.T, path string) {}},
		{"a regular file", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{claudePane("bash", "0", "%5", "writing tests")}}
			d := newTestDaemon(mc)
			d.cfg.Verbose = true
			socket := filepath.Join(realTrustedSocketDir(t, d), "default")
			tc.setup(t, socket)
			mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "writing tests")}}
			logs := captureLog(t)

			newRawFeeder(t, d)(rawHookLine(t, "SessionStart", "raw-1", "%5", &socket))

			wi := d.frontend.WindowInfo("raw-1")
			if wi == nil {
				t.Fatal("WindowInfo = nil, want the window tracked on the ambient server")
			}
			if wi.Socket != "" {
				t.Errorf("WindowInfo.Socket = %q, want empty: the path is not an existing socket", wi.Socket)
			}
			if sess := d.sessions["raw-1"]; sess == nil || sess.TmuxSocket != "" {
				t.Errorf("session = %+v, want it recorded with an empty socket", sess)
			}
			for i, s := range mc.Sockets {
				if s != "" {
					t.Errorf("call %d went to socket %q, want every call on the ambient server", i, s)
				}
			}
			lines := linesContaining(logs.String(), "untrusted")
			if len(lines) != 1 {
				t.Fatalf("log has %d untrusted lines %q, want exactly one", len(lines), lines)
			}
			for _, want := range []string{"SessionStart", "raw-1", "tmux_socket"} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("log line %q does not name %q", lines[0], want)
				}
			}
		})
	}
}

// #1462 AC3: an existing Unix socket directly under the trusted directory,
// owned by the current uid, is still tracked on that socket (unchanged from
// #1429). The control that the rejections above are not a blanket drop.
func TestRawHookEventWithAnExistingSocketKeepsItWithNoRejectionLog(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.cfg.Verbose = true
	socket := filepath.Join(realTrustedSocketDir(t, d), "default")
	listenShortSocket(t, socket)
	mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "writing tests")}}
	logs := captureLog(t)

	newRawFeeder(t, d)(rawHookLine(t, "SessionStart", "raw-1", "%5", &socket))

	wi := d.frontend.WindowInfo("raw-1")
	if wi == nil || wi.Socket != socket {
		t.Fatalf("WindowInfo = %+v, want the window tracked on the existing socket %q", wi, socket)
	}
	if lines := linesContaining(logs.String(), "untrusted"); len(lines) != 0 {
		t.Errorf("an existing socket logged %q, want nothing", lines)
	}
}

// A trusted socket is recorded end to end: the pane listing, the rename and the
// style calls all carry it (and nothing goes to the ambient server), and the
// broadcast snapshot has the socket.
func TestTrustedTmuxSocketIsRecordedEndToEnd(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	socket := filepath.Join(trustedSocketDir(t, d), "work")
	mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "writing tests")}}

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5", TmuxSocket: socket})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%5", TmuxSocket: socket})

	for i, s := range mc.Sockets {
		if s != socket {
			t.Errorf("call %d went to socket %q, want every call on %q", i, s, socket)
		}
	}
	if len(mc.ListSockets) == 0 {
		t.Fatal("no pane listing was made")
	}
	if got, ok := tmuxtest.LastSocketRename(mc.Renames, socket, "main:0"); !ok || got != "writing tests" {
		t.Errorf("rename on the socket = (%q, %v), want %q", got, ok, "writing tests")
	}
	for key, want := range map[string]string{"@cenci-symbol": d.cfg.SymbolRunning, "@cenci-style": d.cfg.StyleRunning} {
		if got, ok := tmuxtest.FindSocketWindowOpt(mc.WindowOpts, socket, "main:0", key); !ok || got != want {
			t.Errorf("%s on the socket = (%q, %v), want %q", key, got, ok, want)
		}
	}
	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 || snap.Windows[0].Socket != socket {
		t.Fatalf("snapshot windows = %+v, want one window with socket %q", snap.Windows, socket)
	}
	if js := snapshotJSON(t, d); !strings.Contains(js, `"socket":"`+socket+`"`) {
		t.Errorf("snapshot = %s, want a socket key with %q", js, socket)
	}
}

// An event without a pane changes neither the recorded pane nor its socket: the
// listing keeps going to the pane's own server.
func TestEventWithoutPaneKeepsTheRecordedSocket(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	socket := filepath.Join(trustedSocketDir(t, d), "work")
	mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: {claudePane("bash", "0", "%5", "writing tests")}}

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5", TmuxSocket: socket})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	wi := d.frontend.WindowInfo("sess1")
	if wi == nil || wi.Socket != socket {
		t.Fatalf("WindowInfo = %+v, want the window still on %q", wi, socket)
	}
	for i, s := range mc.Sockets {
		if s != socket {
			t.Errorf("call %d went to socket %q, want every call on %q", i, s, socket)
		}
	}
	if sess := d.sessions["sess1"]; sess == nil || sess.TmuxSocket != socket || sess.TmuxPane != "%5" {
		t.Errorf("session = %+v, want pane %%5 and socket %q kept", sess, socket)
	}
}

// A hook event without tmux_socket, from an older `cenci notify`, is tracked
// exactly as before, and so is one that sends an explicit empty value: both leave
// the same call record, every call on the ambient server, and log nothing.
func TestHookEventWithoutTmuxSocketTrackedExactlyAsToday(t *testing.T) {
	type record struct {
		Sockets     []string
		ListSockets []string
		Renames     []tmuxtest.RenameCall
		WindowOpts  []tmuxtest.WindowOptCall
		Opts        []tmuxtest.OptCall
	}
	run := func(t *testing.T, socket *string) (record, string) {
		mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{claudePane("bash", "0", "%5", "writing tests")}}
		d := newTestDaemon(mc)
		d.cfg.Verbose = true
		trustedSocketDir(t, d)
		logs := captureLog(t)
		feed := newRawFeeder(t, d)
		for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
			feed(rawHookLine(t, ev, "old-1", "%5", socket))
			if ev == "Stop" {
				wi := d.frontend.WindowInfo("old-1")
				if wi == nil || wi.Socket != "" {
					t.Fatalf("WindowInfo = %+v, want the window tracked with an empty socket", wi)
				}
			}
		}
		return record{mc.Sockets, mc.ListSockets, mc.Renames, mc.WindowOpts, mc.Opts}, logs.String()
	}

	empty := ""
	absent, absentLog := run(t, nil)
	explicit, explicitLog := run(t, &empty)

	if len(absent.Renames) == 0 || len(absent.WindowOpts) == 0 {
		t.Fatalf("the flow made no rename or option calls: %+v", absent)
	}
	for i, s := range absent.Sockets {
		if s != "" {
			t.Errorf("call %d went to socket %q, want the ambient server", i, s)
		}
	}
	if !reflect.DeepEqual(absent, explicit) {
		t.Errorf("an absent tmux_socket left %+v, an explicit empty one left %+v, want identical call records", absent, explicit)
	}
	for _, l := range []string{absentLog, explicitLog} {
		if lines := linesContaining(l, "tmux_socket"); len(lines) != 0 {
			t.Errorf("an empty socket logged %q, want nothing: only a rejected value is logged", lines)
		}
	}
}

// P25 at the daemon: pane %5 on two servers is two sessions and two windows, each
// renamed and styled on its own server, and ending one session restores only its
// own window.
func TestSamePaneIDOnTwoSocketsTracksTwoWindows(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	dir := trustedSocketDir(t, d)
	sockA, sockB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mc.PanesBySocket = map[string][]tmux.PaneInfo{
		sockA: {claudePane("bash", "0", "%5", "alpha task")},
		sockB: {claudePane("bash", "0", "%5", "beta task")},
	}

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sa", TmuxPane: "%5", TmuxSocket: sockA})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sb", TmuxPane: "%5", TmuxSocket: sockB})

	snap := d.buildSnapshot()
	if len(snap.Windows) != 2 {
		t.Fatalf("snapshot has %d windows %+v, want two", len(snap.Windows), snap.Windows)
	}
	seen := map[string]bool{}
	for _, w := range snap.Windows {
		seen[w.Socket] = true
		if w.PaneID != "%5" {
			t.Errorf("window %+v, want pane %%5", w)
		}
	}
	if !seen[sockA] || !seen[sockB] {
		t.Errorf("snapshot sockets = %v, want both %q and %q", seen, sockA, sockB)
	}
	for socket, task := range map[string]string{sockA: "alpha task", sockB: "beta task"} {
		if got, ok := tmuxtest.LastSocketRename(mc.Renames, socket, "main:0"); !ok || got != task {
			t.Errorf("rename on %s = (%q, %v), want %q", socket, got, ok, task)
		}
		for key, want := range map[string]string{"@cenci-symbol": d.cfg.SymbolRunning, "@cenci-style": d.cfg.StyleRunning} {
			if got, ok := tmuxtest.FindSocketWindowOpt(mc.WindowOpts, socket, "main:0", key); !ok || got != want {
				t.Errorf("%s on %s = (%q, %v), want %q", key, socket, got, ok, want)
			}
		}
	}
	for i, s := range mc.Sockets {
		if s != sockA && s != sockB {
			t.Fatalf("call %d went to socket %q, want only A or B", i, s)
		}
	}

	renames, opts := len(mc.Renames), len(mc.WindowOpts)
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sa", TmuxPane: "%5", TmuxSocket: sockA})

	if len(mc.Renames) == renames || len(mc.WindowOpts) == opts {
		t.Fatalf("ending A restored nothing: renames %d→%d, options %d→%d", renames, len(mc.Renames), opts, len(mc.WindowOpts))
	}
	for _, r := range mc.Renames[renames:] {
		if r.Socket != sockA {
			t.Errorf("restore rename %+v went to %q, want only A", r, r.Socket)
		}
	}
	for _, o := range mc.WindowOpts[opts:] {
		if o.Socket != sockA {
			t.Errorf("restore option %+v went to %q, want only A", o, o.Socket)
		}
	}
	if snap := d.buildSnapshot(); len(snap.Windows) != 1 || snap.Windows[0].Socket != sockB {
		t.Errorf("snapshot after ending A = %+v, want only B's window", snap.Windows)
	}
}

// Pane-only events (no session id) on two servers are two sessions: the fallback
// key carries the socket when it is non-empty, an empty socket keeps today's
// "pane:<id>", and the daemon and the frontend derive the same key.
func TestPaneOnlyEventsOnTwoSocketsAreTwoSessions(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{claudePane("bash", "0", "%5", "default task")}}
	d := newTestDaemon(mc)
	dir := trustedSocketDir(t, d)
	sockA, sockB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mc.PanesBySocket = map[string][]tmux.PaneInfo{
		sockA: {claudePane("bash", "0", "%5", "alpha task")},
		sockB: {claudePane("bash", "0", "%5", "beta task")},
	}

	for _, socket := range []string{sockA, sockB, ""} {
		d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", TmuxPane: "%5", TmuxSocket: socket})
	}

	want := map[string]string{
		"pane:%5@" + sockA: sockA,
		"pane:%5@" + sockB: sockB,
		"pane:%5":          "",
	}
	if len(d.sessions) != len(want) {
		t.Fatalf("sessions = %v, want exactly %d", sessionKeys(d), len(want))
	}
	for key, socket := range want {
		sess := d.sessions[key]
		if sess == nil {
			t.Errorf("no session %q among %v", key, sessionKeys(d))
			continue
		}
		if sess.TmuxSocket != socket {
			t.Errorf("session %q socket = %q, want %q", key, sess.TmuxSocket, socket)
		}
		if got := sess.Key(); got != key {
			t.Errorf("frontend key for session %q = %q, want the daemon's key", key, got)
		}
		if wi := d.frontend.WindowInfo(key); wi == nil || wi.Socket != socket {
			t.Errorf("WindowInfo(%q) = %+v, want a window on socket %q", key, wi, socket)
		}
	}
}

func sessionKeys(d *Daemon) []string {
	keys := make([]string, 0, len(d.sessions))
	for k := range d.sessions {
		keys = append(keys, k)
	}
	return keys
}
