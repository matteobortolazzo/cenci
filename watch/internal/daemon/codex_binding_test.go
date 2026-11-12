package daemon

// Ticket #1529: every Codex pane association is checked against the tmux socket
// and the tmux server pid from the same pane listing, and `cenci bind` pins a
// Codex session to the caller's pane. Every case drives a real tmux frontend
// over tmuxtest.MockClient through handleEvent and applyBind (the loop half of
// a bind); the wire and loop hop are covered by
// TestHandleBindRoundTripsThroughTheLoop, the raw pid key by the ingest test.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

const (
	livePID = "4242" // the live server pid of the fixture's trusted socket
	deadPID = "5151" // a pid from another server lifetime
)

// The fixture's panes, each in its own window on the trusted socket. stalePane
// is never listed: it is the pane a closed client left in the shared Codex
// app-server's environment.
const (
	paneA, winA = "%10", "main:1"
	paneB, winB = "%20", "main:2"
	paneC, winC = "%30", "main:3"
	stalePane   = "%99"
)

func codexPane(index, id string) tmux.PaneInfo {
	return tmux.PaneInfo{
		SessionName: "main", WindowIndex: index, WindowName: "bash", PaneIndex: "0",
		PaneCurrentCmd: "codex", PaneTitle: "codex", PaneID: id,
	}
}

type bindEnv struct {
	d      *Daemon
	mc     *tmuxtest.MockClient
	socket string
}

// newBindEnv is a daemon whose trusted socket lists panes (A, B and C by
// default) under livePID.
func newBindEnv(t *testing.T, panes ...tmux.PaneInfo) *bindEnv {
	t.Helper()
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	socket := filepath.Join(trustedSocketDir(t, d), "default")
	if panes == nil {
		panes = []tmux.PaneInfo{codexPane("1", paneA), codexPane("2", paneB), codexPane("3", paneC)}
	}
	mc.PanesBySocket = map[string][]tmux.PaneInfo{socket: panes}
	mc.ServerPIDBySocket = map[string]string{socket: livePID}
	return &bindEnv{d: d, mc: mc, socket: socket}
}

// event delivers a Codex hook event carrying pane on the trusted socket.
func (e *bindEnv) event(eventType, session, pane, pid string) {
	e.d.handleEvent(ipc.HookEvent{EventType: eventType, SessionID: session, Agent: "codex", TaskName: session,
		TmuxPane: pane, TmuxSocket: e.socket, TmuxServerPID: pid})
}

// bind applies a bind from pane on the trusted socket under the live pid.
func (e *bindEnv) bind(session, pane string) ipc.BindResponse {
	return e.d.applyBind(ipc.BindRequest{SessionID: session, TmuxPane: pane, TmuxSocket: e.socket, TmuxServerPID: livePID})
}

// opt is the last value written to a window option on the trusted socket.
func (e *bindEnv) opt(target, key string) string {
	v, _ := tmuxtest.FindSocketWindowOpt(e.mc.WindowOpts, e.socket, target, key)
	return v
}

// writesSince reports the option writes and renames made since the marks.
func (e *bindEnv) writesSince(opts, renames int) int {
	return len(e.mc.WindowOpts) - opts + len(e.mc.Renames) - renames
}

func (e *bindEnv) mustBind(t *testing.T, session, pane string) {
	t.Helper()
	if resp := e.bind(session, pane); !resp.OK || resp.Pane != pane {
		t.Fatalf("bind %s to %s = %+v, want an ack naming the pane", session, pane, resp)
	}
}

// entryByTask is the snapshot entry of the session whose task is task (the
// fixture's events use the session id as the task name); a missing one fails.
func entryByTask(t *testing.T, snap ipc.StateSnapshot, task string) ipc.WindowState {
	t.Helper()
	w := windowByTask(&snap, task)
	if w == nil {
		t.Fatalf("no snapshot entry for %q in %+v", task, snap.Windows)
	}
	return *w
}

func assertPaneless(t *testing.T, d *Daemon, session string) {
	t.Helper()
	sess := d.sessions[session]
	if sess == nil {
		t.Fatalf("session %s is gone, want it kept paneless", session)
	}
	if sess.TmuxPane != "" || sess.Binding != nil || d.frontend.WindowInfo(session) != nil {
		t.Fatalf("session %s = pane %q binding %+v window %+v, want paneless", session, sess.TmuxPane, sess.Binding, d.frontend.WindowInfo(session))
	}
}

// P2: after a bind from B, every later event of the session styles B's window
// and the snapshot reports B, although the events still carry the stale pane.
func TestDaemon_CodexBindStylesBoundPaneDespiteStaleEvents(t *testing.T) {
	e := newBindEnv(t, codexPane("2", paneB))
	e.event("UserPromptSubmit", "thread", stalePane, livePID)
	assertPaneless(t, e.d, "thread")

	e.mustBind(t, "thread", paneB)
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
		t.Fatalf("B's @cenci-symbol right after the bind = %q, want %q", got, e.d.cfg.SymbolRunning)
	}

	for _, tc := range []struct{ event, symbol, style string }{
		{"PreToolUse", e.d.cfg.SymbolRunning, e.d.cfg.StyleRunning},
		{"Stop", e.d.cfg.SymbolDone, e.d.cfg.StyleDone},
	} {
		e.event(tc.event, "thread", stalePane, livePID)
		e.d.runSweep()
		if got := e.opt(winB, "@cenci-symbol"); got != tc.symbol {
			t.Errorf("after %s B's @cenci-symbol = %q, want %q", tc.event, got, tc.symbol)
		}
		if got := e.opt(winB, "@cenci-style"); got != tc.style {
			t.Errorf("after %s B's @cenci-style = %q, want %q", tc.event, got, tc.style)
		}
		snap := e.d.buildSnapshot()
		if len(snap.Windows) != 1 || snap.Windows[0].PaneID != paneB || snap.Windows[0].Association != watch.AssociationBound || snap.Windows[0].SessionID != "" {
			t.Errorf("after %s snapshot = %+v, want one bound entry on %s without a session id", tc.event, snap.Windows, paneB)
		}
	}
	// The broadcast socket is mounted into sandboxes: a bound entry never
	// carries the host session id.
	if js := snapshotJSON(t, e.d); !strings.Contains(js, `"association":"bound"`) || strings.Contains(js, `"session_id"`) {
		t.Errorf("snapshot JSON = %s, want a bound entry with no session_id key", js)
	}
}

// P4: pane %5 on socket X (or claimed on an untrusted X, which falls back to the
// default server) never touches the window holding %5 on another server. The
// last case is the control that %5 on its own server is styled.
func TestDaemon_CodexEqualPaneIDOnOtherSocketNeverStyled(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{codexPane("1", "%5")}}
	d := newTestDaemon(mc)
	dir := trustedSocketDir(t, d)
	sockX, sockY := filepath.Join(dir, "x"), filepath.Join(dir, "y")
	untrusted := filepath.Join(privateDir(t), "x")
	mc.PanesBySocket = map[string][]tmux.PaneInfo{sockX: {codexPane("1", "%7")}, sockY: {codexPane("1", "%5")}}
	mc.ServerPIDBySocket = map[string]string{sockX: "100", sockY: "200", "": "300"}

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "on-x", Agent: "codex", TmuxPane: "%5", TmuxSocket: sockX, TmuxServerPID: "100"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "untrusted-x", Agent: "codex", TmuxPane: "%5", TmuxSocket: untrusted, TmuxServerPID: "100"})
	if resp := d.applyBind(ipc.BindRequest{SessionID: "on-x", TmuxPane: "%5", TmuxSocket: sockX, TmuxServerPID: "100"}); resp.OK || resp.Reason != ReasonBindPaneNotListed {
		t.Errorf("bind of %%5 on X = %+v, want %q", resp, ReasonBindPaneNotListed)
	}
	defaultListings := countOf(mc.ListSockets, "")
	if resp := d.applyBind(ipc.BindRequest{SessionID: "untrusted-x", TmuxPane: "%5", TmuxSocket: untrusted, TmuxServerPID: "100"}); resp.OK || resp.Reason != ReasonBindUntrustedSocket {
		t.Errorf("bind on an untrusted socket = %+v, want %q", resp, ReasonBindUntrustedSocket)
	}
	if got := countOf(mc.ListSockets, ""); got != defaultListings {
		t.Errorf("a rejected bind socket listed the default server: %d listings, want %d", got, defaultListings)
	}
	if len(mc.WindowOpts) != 0 || len(mc.Renames) != 0 {
		t.Fatalf("window writes %+v renames %+v, want none on any server", mc.WindowOpts, mc.Renames)
	}
	assertPaneless(t, d, "on-x")
	assertPaneless(t, d, "untrusted-x")

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "on-y", Agent: "codex", TmuxPane: "%5", TmuxSocket: sockY, TmuxServerPID: "200"})
	if got, ok := tmuxtest.FindSocketWindowOpt(mc.WindowOpts, sockY, "main:1", "@cenci-symbol"); !ok || got != d.cfg.SymbolRunning {
		t.Errorf("control: %%5 on Y under Y's pid set @cenci-symbol = (%q, %v), want %q", got, ok, d.cfg.SymbolRunning)
	}
}

func countOf(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}

// P5: an event whose server pid differs from the live one never attaches: a
// fresh pane stays unstyled, and a tracked window whose pane id was reused after
// a server restart is forgotten without any tmux write.
func TestDaemon_CodexServerPIDMismatchStaysPaneless(t *testing.T) {
	t.Run("a listed pane under another server pid", func(t *testing.T) {
		e := newBindEnv(t)
		e.event("UserPromptSubmit", "thread", paneB, deadPID)
		assertPaneless(t, e.d, "thread")
		if e.writesSince(0, 0) != 0 {
			t.Fatalf("window writes %+v renames %+v, want none", e.mc.WindowOpts, e.mc.Renames)
		}
		if got := entryByTask(t, e.d.buildSnapshot(), "thread").Association; got != watch.AssociationUnproven {
			t.Errorf("association = %q, want %q", got, watch.AssociationUnproven)
		}
	})

	t.Run("a tracked pane id reused after a server restart", func(t *testing.T) {
		e := newBindEnv(t)
		e.event("UserPromptSubmit", "thread", paneB, livePID)
		if wi := e.d.frontend.WindowInfo("thread"); wi == nil || wi.PaneID != paneB {
			t.Fatalf("control: WindowInfo = %+v, want B tracked under the matching pid", wi)
		}
		e.mc.ServerPIDBySocket[e.socket] = "7777"
		e.mc.PanesBySocket[e.socket] = []tmux.PaneInfo{codexPane("5", paneB)}
		opts, renames := len(e.mc.WindowOpts), len(e.mc.Renames)

		e.event("PreToolUse", "thread", paneB, livePID)
		e.d.runSweep()
		assertPaneless(t, e.d, "thread")
		if n := e.writesSince(opts, renames); n != 0 {
			t.Errorf("the mismatch made %d tmux writes (%+v), want none on the reused pane", n, e.mc.WindowOpts[opts:])
		}
	})
}

// P5 on teardown: a Codex SessionEnd (or a clear/resume handoff) claimed under
// a server pid that no longer matches never reaches frontend.OnEvent, so the
// teardown itself must not restore, rename or keep tracking the window behind a
// pane id that tmux reused after a restart. The session is still removed. The
// first case is the control that a matching pid restores the window.
func TestDaemon_CodexSessionEndUnderAnotherServerPIDWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		restart      bool
	}{
		{"control: matching pid restores the window", "other", false},
		{"pane id reused after a server restart", "other", true},
		{"handoff on a pane id reused after a server restart", "clear", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t)
			e.event("UserPromptSubmit", "thread", paneB, livePID)
			if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
				t.Fatalf("control: B's @cenci-symbol = %q, want B styled under the matching pid", got)
			}
			e.d.registerPendingClose(ipc.PendingClose{PaneID: paneB, Socket: e.socket, Session: "main", WindowIndex: "2", WindowName: "thread"})
			fk := e.d.killer.(*fakeKiller)
			if tc.restart {
				e.mc.ServerPIDBySocket[e.socket] = "7777"
				e.mc.PanesBySocket[e.socket] = []tmux.PaneInfo{codexPane("5", paneB)}
			}
			opts, renames := len(e.mc.WindowOpts), len(e.mc.Renames)

			e.d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "thread", Agent: "codex", SessionEndReason: tc.reason,
				TmuxPane: paneB, TmuxSocket: e.socket, TmuxServerPID: livePID})
			if _, ok := e.d.sessions["thread"]; ok {
				t.Fatal("SessionEnd kept the session")
			}
			if !tc.restart {
				if got := e.opt(winB, "@cenci-symbol"); got != "" || e.writesSince(opts, renames) == 0 {
					t.Errorf("B's @cenci-symbol after SessionEnd = %q, want the window restored", got)
				}
				if len(fk.killed) != 1 || fk.killed[0] != paneB {
					t.Errorf("killed = %v, want the pending-close on %s fired", fk.killed, paneB)
				}
				return
			}
			// The registered pane id now names an unrelated window: it is never
			// killed, and the entry is dropped so a later SessionEnd on the
			// reused id cannot fire it either.
			if len(fk.killed) != 0 || len(e.d.pending) != 0 {
				t.Errorf("killed = %v pending = %+v, want no kill and no entry left on the reused pane", fk.killed, e.d.pending)
			}
			// Daemon shutdown restores every window the frontend still tracks,
			// so a window kept tracked would be written later.
			e.d.frontend.Cleanup(e.d.sessions)
			e.d.runSweep()
			if n := e.writesSince(opts, renames); n != 0 {
				t.Errorf("teardown made %d tmux writes on the reused pane (%+v, renames %+v), want none", n, e.mc.WindowOpts[opts:], e.mc.Renames[renames:])
			}
		})
	}
}

// P6: an unproven Codex session stays listed and counted, follows its own
// Stop/SessionEnd, and expires through the paneless TTL. Only Codex entries
// carry an association, and a Codex session that never reported a pane has none.
func TestDaemon_CodexUnprovenSessionSnapshotAndLifecycle(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "unproven", paneB, "")
	e.event("UserPromptSubmit", "proven", paneA, livePID)
	e.d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "outside", Agent: "codex", TaskName: "outside"})
	e.d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "claude", Agent: "claude", TaskName: "claude", TmuxPane: paneC, TmuxSocket: e.socket})
	e.d.runSweep()

	snap := e.d.buildSnapshot()
	if snap.Summary.Total != 4 || snap.Summary.Running != 4 {
		t.Fatalf("summary = %+v, want all four sessions counted as running", snap.Summary)
	}
	for task, want := range map[string][2]string{
		"unproven": {watch.AssociationUnproven, "unproven"},
		"proven":   {watch.AssociationProven, ""},
		"outside":  {"", ""},
		"claude":   {"", ""},
	} {
		w := entryByTask(t, snap, task)
		if w.Association != want[0] || w.SessionID != want[1] {
			t.Errorf("%s: association %q session_id %q, want %q %q", task, w.Association, w.SessionID, want[0], want[1])
		}
	}
	// Only the unproven entry carries session_id (the hint needs it); the
	// proven one omits it, keeping host ids off the broadcast socket.
	js := snapshotJSON(t, e.d)
	if !strings.Contains(js, `"association":"unproven","session_id":"unproven"`) || strings.Count(js, `"association"`) != 2 ||
		strings.Count(js, `"session_id"`) != 1 || strings.Contains(js, `"session_id":"proven"`) {
		t.Errorf("snapshot JSON = %s, want association on the two associated Codex entries and session_id on the unproven one only", js)
	}

	e.event("Stop", "unproven", paneB, "")
	snap = e.d.buildSnapshot()
	if w := entryByTask(t, snap, "unproven"); w.Status != "done" || w.Association != watch.AssociationUnproven || snap.Summary.Done != 1 {
		t.Errorf("after Stop: %+v summary %+v, want done, still unproven, counted", w, snap.Summary)
	}
	e.event("SessionEnd", "unproven", paneB, "")
	if _, ok := e.d.sessions["unproven"]; ok {
		t.Error("SessionEnd kept the unproven session")
	}

	e.event("UserPromptSubmit", "expiring", paneB, "")
	e.d.now = func() time.Time { return time.Now().Add(e.d.cfg.SessionTTL + time.Second) }
	e.d.runSweep()
	if _, ok := e.d.sessions["expiring"]; ok {
		t.Error("the unproven session outlived the paneless TTL")
	}
}

// P12: a bind for a session the daemon does not track is refused and changes
// nothing.
func TestBind_UnknownSessionIsRefusedAndChangesNothing(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "known", paneA, livePID)
	before := e.d.buildSnapshot().Windows
	opts, renames := len(e.mc.WindowOpts), len(e.mc.Renames)

	if resp := e.bind("ghost", paneB); resp.OK || resp.Reason != ReasonBindUnknownSession {
		t.Fatalf("bind = %+v, want %q", resp, ReasonBindUnknownSession)
	}
	if after := e.d.buildSnapshot().Windows; !reflect.DeepEqual(before, after) || len(e.d.sessions) != 1 {
		t.Errorf("sessions changed: %+v -> %+v", before, after)
	}
	if n := e.writesSince(opts, renames); n != 0 {
		t.Errorf("a refused bind made %d tmux writes", n)
	}
}

// P13 and the non-Codex holder rule: a Claude session cannot be bound, and a
// Codex session cannot take a pane a Claude session tracks. The Claude event
// carries a socket and no pid and still attaches (the Claude path is unchanged).
func TestBind_RefusesNonCodexSessionAndNonCodexPaneHolder(t *testing.T) {
	e := newBindEnv(t)
	e.d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "claude", Agent: "claude", TmuxPane: paneB, TmuxSocket: e.socket})
	if wi := e.d.frontend.WindowInfo("claude"); wi == nil || wi.PaneID != paneB {
		t.Fatalf("control: the Claude event with a socket and no pid did not attach: %+v", wi)
	}
	e.event("UserPromptSubmit", "codex", stalePane, livePID)

	if resp := e.bind("claude", paneC); resp.OK || resp.Reason != ReasonBindNotCodex {
		t.Errorf("binding the Claude session = %+v, want %q", resp, ReasonBindNotCodex)
	}
	if resp := e.bind("codex", paneB); resp.OK || resp.Reason != ReasonBindNonCodexPaneHolder {
		t.Errorf("binding onto the Claude pane = %+v, want %q", resp, ReasonBindNonCodexPaneHolder)
	}
	e.d.runSweep()
	if wi := e.d.frontend.WindowInfo("claude"); wi == nil || wi.PaneID != paneB || e.d.sessions["claude"].TmuxPane != paneB {
		t.Errorf("the Claude association changed: %+v", wi)
	}
	assertPaneless(t, e.d, "codex")
}

// P3's daemon-side refusals: an untrusted socket (never retried on the default
// server), a pane not listed, a server pid mismatch and a failed listing.
func TestBind_RefusesAPaneItCannotProve(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		req        func(e *bindEnv, t *testing.T) ipc.BindRequest
	}{
		{"untrusted socket", ReasonBindUntrustedSocket, func(e *bindEnv, t *testing.T) ipc.BindRequest {
			return ipc.BindRequest{TmuxPane: paneB, TmuxSocket: filepath.Join(privateDir(t), "default"), TmuxServerPID: livePID}
		}},
		{"pane not listed", ReasonBindPaneNotListed, func(e *bindEnv, t *testing.T) ipc.BindRequest {
			return ipc.BindRequest{TmuxPane: stalePane, TmuxSocket: e.socket, TmuxServerPID: livePID}
		}},
		{"server pid mismatch", ReasonBindServerPIDMismatch, func(e *bindEnv, t *testing.T) ipc.BindRequest {
			return ipc.BindRequest{TmuxPane: paneB, TmuxSocket: e.socket, TmuxServerPID: deadPID}
		}},
		{"listing failure", ReasonBindListingFailed, func(e *bindEnv, t *testing.T) ipc.BindRequest {
			e.mc.ListErrBySocket = map[string]error{e.socket: errors.New("tmux unavailable")}
			return ipc.BindRequest{TmuxPane: paneB, TmuxSocket: e.socket, TmuxServerPID: livePID}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t)
			e.event("UserPromptSubmit", "thread", stalePane, livePID)
			req := tc.req(e, t)
			req.SessionID = "thread"
			if resp := e.d.applyBind(req); resp.OK || resp.Reason != tc.want {
				t.Errorf("bind = %+v, want %q", resp, tc.want)
			}
			if tc.name == "untrusted socket" && countOf(e.mc.ListSockets, "") != 0 {
				t.Errorf("listings %q, want none on the default server", e.mc.ListSockets)
			}
			assertPaneless(t, e.d, "thread")
			if len(e.mc.WindowOpts) != 0 {
				t.Errorf("window writes %+v, want none", e.mc.WindowOpts)
			}
		})
	}
}

// P14: binding while the hook pane A is alive and styled: the binding wins, A is
// restored, and later events style only B.
func TestBind_OverridesALiveDifferentHookPane(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "thread", paneA, livePID)
	if got := e.opt(winA, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
		t.Fatalf("control: A's @cenci-symbol = %q, want the hook pane styled", got)
	}
	e.mustBind(t, "thread", paneB)
	if got := e.opt(winA, "@cenci-symbol"); got != "" {
		t.Errorf("A's @cenci-symbol after the bind = %q, want it restored", got)
	}
	opts := len(e.mc.WindowOpts)
	e.event("PreToolUse", "thread", paneA, livePID)
	e.event("Stop", "thread", paneA, livePID)
	e.d.runSweep()
	for _, o := range e.mc.WindowOpts[opts:] {
		if o.Target == winA {
			t.Errorf("a later event wrote %+v on A, want only B styled", o)
		}
	}
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
		t.Errorf("B's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolDone)
	}
	if wi := e.d.frontend.WindowInfo("thread"); wi == nil || wi.PaneID != paneB {
		t.Errorf("WindowInfo = %+v, want B", wi)
	}
}

// P15: rebinding from C moves the session to C and restores B's options.
func TestBind_RebindFromAnotherPaneRestoresThePreviousWindow(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "thread", stalePane, livePID)
	e.mustBind(t, "thread", paneB)
	e.mustBind(t, "thread", paneC)
	for _, key := range []string{"@cenci-symbol", "@cenci-style", "window-status-style"} {
		if got := e.opt(winB, key); got != "" {
			t.Errorf("B's %s = %q, want its original (empty) value back", key, got)
		}
	}
	if got := e.opt(winC, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
		t.Errorf("C's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolRunning)
	}
	if snap := e.d.buildSnapshot(); len(snap.Windows) != 1 || snap.Windows[0].PaneID != paneC {
		t.Errorf("snapshot = %+v, want the session on C", snap.Windows)
	}
}

// P16: a second session binding an already bound pane detaches the first to
// paneless (kept and listed, never deleted), and the window shows the second
// session's state, also after the first session's next event names that very
// pane under the live pid: a pane bound to another session is never adopted.
func TestBind_SecondSessionOnABoundPaneDetachesTheFirst(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "first", stalePane, livePID)
	e.event("Stop", "second", stalePane, livePID)
	e.mustBind(t, "first", paneB)
	e.mustBind(t, "second", paneB)

	assertPaneless(t, e.d, "first")
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
		t.Errorf("B's @cenci-symbol = %q, want the second session's %q", got, e.d.cfg.SymbolDone)
	}
	e.event("PreToolUse", "first", paneB, livePID)
	e.d.runSweep()
	assertPaneless(t, e.d, "first")
	if wi := e.d.frontend.WindowInfo("second"); wi == nil || wi.PaneID != paneB {
		t.Errorf("second's window = %+v, want B", wi)
	}
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
		t.Errorf("after first's event B's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolDone)
	}
	snap := e.d.buildSnapshot()
	if snap.Summary.Total != 2 || entryByTask(t, snap, "first").Association != watch.AssociationUnproven {
		t.Errorf("snapshot = %+v, want both listed and the first unproven", snap.Windows)
	}
}

// The frontend tracks windows, not panes: binding pane %6 in a split window
// whose other pane %5 a session already tracks must treat that session as the
// window's holder. A non-Codex holder refuses the bind and keeps its session and
// window; a Codex holder is detached to paneless (kept, listed, unproven), never
// deleted by the restyle, and the binder owns the window.
func TestBind_SplitWindowHolderOnAnotherPane(t *testing.T) {
	const heldPane, bindPane, split = "%5", "%6", "main:2"
	splitPane := func(index, id string) tmux.PaneInfo {
		p := codexPane("2", id)
		p.PaneIndex = index
		return p
	}
	for _, tc := range []struct {
		name, holderAgent string
		wantOK            bool
	}{
		{"a Claude session on the other pane refuses the bind", "claude", false},
		{"a Codex session on the other pane is detached", "codex", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t, splitPane("0", heldPane), splitPane("1", bindPane))
			e.d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "holder", Agent: tc.holderAgent, TaskName: "holder",
				TmuxPane: heldPane, TmuxSocket: e.socket, TmuxServerPID: livePID})
			if wi := e.d.frontend.WindowInfo("holder"); wi == nil || wi.PaneID != heldPane {
				t.Fatalf("control: holder's WindowInfo = %+v, want %s tracked", wi, heldPane)
			}
			e.event("UserPromptSubmit", "binder", stalePane, livePID)
			opts, renames := len(e.mc.WindowOpts), len(e.mc.Renames)

			resp := e.bind("binder", bindPane)
			e.d.runSweep()
			holder := e.d.sessions["holder"]
			if holder == nil {
				t.Fatalf("the bind deleted the %s holder, want it kept", tc.holderAgent)
			}
			if !tc.wantOK {
				if resp.OK || resp.Reason != ReasonBindNonCodexPaneHolder {
					t.Fatalf("bind = %+v, want %q", resp, ReasonBindNonCodexPaneHolder)
				}
				if wi := e.d.frontend.WindowInfo("holder"); wi == nil || wi.PaneID != heldPane || holder.TmuxPane != heldPane {
					t.Errorf("the Claude holder's association changed: pane %q window %+v", holder.TmuxPane, wi)
				}
				if n := e.writesSince(opts, renames); n != 0 {
					t.Errorf("a refused bind made %d tmux writes", n)
				}
				assertPaneless(t, e.d, "binder")
				return
			}
			if !resp.OK || resp.Pane != bindPane {
				t.Fatalf("bind = %+v, want an ack naming %s", resp, bindPane)
			}
			assertPaneless(t, e.d, "holder")
			if got := entryByTask(t, e.d.buildSnapshot(), "holder").Association; got != watch.AssociationUnproven {
				t.Errorf("holder association = %q, want %q", got, watch.AssociationUnproven)
			}
			if wi := e.d.frontend.WindowInfo("binder"); wi == nil || wi.PaneID != bindPane || wi.Session+":"+wi.WindowIndex != split {
				t.Errorf("binder's window = %+v, want %s on %s", wi, bindPane, split)
			}
		})
	}
}

// P17: a bound pane proven gone removes the session through the normal
// cleanup; a later stale event creates a paneless session and styles nothing.
func TestBind_BoundPaneGoneRemovesTheSessionAndStaleEventsStayPaneless(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "thread", paneA, livePID)
	e.mustBind(t, "thread", paneB)
	e.mc.PanesBySocket[e.socket] = []tmux.PaneInfo{codexPane("1", paneA)}
	e.d.runSweep()
	if _, ok := e.d.sessions["thread"]; ok {
		t.Fatal("the session survived its bound pane")
	}

	e.mc.PanesBySocket[e.socket] = []tmux.PaneInfo{codexPane("3", paneC)}
	opts, renames := len(e.mc.WindowOpts), len(e.mc.Renames)
	e.event("PreToolUse", "thread", paneA, livePID)
	e.d.runSweep()
	assertPaneless(t, e.d, "thread")
	if n := e.writesSince(opts, renames); n != 0 {
		t.Errorf("the stale event made %d tmux writes, want none", n)
	}
}

// P18: a listing failure while bound keeps the binding.
func TestBind_ListingFailureKeepsTheBinding(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "thread", stalePane, livePID)
	e.mustBind(t, "thread", paneB)
	e.mc.ListErrBySocket = map[string]error{e.socket: errors.New("tmux unavailable")}
	e.event("PreToolUse", "thread", stalePane, livePID)
	e.d.runSweep()
	if sess := e.d.sessions["thread"]; sess == nil || sess.Binding == nil || sess.TmuxPane != paneB {
		t.Fatalf("session = %+v, want the binding to B kept", sess)
	}
	e.mc.ListErrBySocket = nil
	e.event("Stop", "thread", stalePane, livePID)
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
		t.Errorf("B's @cenci-symbol after the listing recovered = %q, want %q", got, e.d.cfg.SymbolDone)
	}
}

// P19: a Codex event with a socket and no server pid (an older notify) is
// unproven: paneless, listed with the association, and nothing is styled.
func TestDaemon_CodexEventWithoutServerPIDIsUnproven(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "thread", paneB, "")
	e.d.runSweep()
	assertPaneless(t, e.d, "thread")
	if len(e.mc.WindowOpts) != 0 || len(e.mc.Renames) != 0 {
		t.Errorf("window writes %+v renames %+v, want none", e.mc.WindowOpts, e.mc.Renames)
	}
	if w := entryByTask(t, e.d.buildSnapshot(), "thread"); w.Association != watch.AssociationUnproven || w.SessionID != "thread" {
		t.Errorf("entry = %+v, want unproven with the session id", w)
	}
}

// The gate is narrow: a Codex event with no socket at all (the sandbox path)
// attaches without a pid, and so does a pane-only Codex event (no session id)
// that sends a trusted socket and no pid: it attaches under its
// pane:<id>@<socket> key exactly as before #1529.
func TestDaemon_CodexEventWithoutSocketAttachesAsBefore(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{codexPane("1", "%5")}}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sandboxed", Agent: "codex", TmuxPane: "%5"})
	if wi := d.frontend.WindowInfo("sandboxed"); wi == nil || wi.PaneID != "%5" {
		t.Fatalf("WindowInfo = %+v, want the pane attached", wi)
	}
	if got := d.buildSnapshot().Windows[0].Association; got != watch.AssociationProven {
		t.Errorf("association = %q, want %q", got, watch.AssociationProven)
	}

	e := newBindEnv(t)
	e.d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", Agent: "codex", TmuxPane: paneB, TmuxSocket: e.socket})
	key := "pane:" + paneB + "@" + e.socket
	if len(e.d.sessions) != 1 || e.d.sessions[key] == nil || e.d.sessions[key].TmuxPane != paneB {
		t.Fatalf("sessions = %+v, want one session keyed %q on %s", e.d.sessions, key, paneB)
	}
	if wi := e.d.frontend.WindowInfo(key); wi == nil || wi.PaneID != paneB || wi.Socket != e.socket {
		t.Fatalf("pane-only WindowInfo = %+v, want %s attached on the trusted socket", wi, paneB)
	}
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
		t.Errorf("pane-only B's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolRunning)
	}
	if w := e.d.buildSnapshot().Windows[0]; w.Association != watch.AssociationProven || w.SessionID != "" {
		t.Errorf("pane-only entry = association %q session_id %q, want proven with no session id", w.Association, w.SessionID)
	}
}

// Hook-events rule 3: a malformed tmux_server_pid written raw to a real
// EventReceiver is treated as absent (unproven) with one verbose line that
// never echoes the value. The first case is the control that a well-formed pid
// on the same path attaches.
func TestRawHookEventWithMalformedTmuxServerPIDIsTreatedAsAbsent(t *testing.T) {
	for _, tc := range []struct {
		name, pid  string
		wantAttach bool
	}{
		{"control: a well-formed pid", livePID, true},
		{"leading zero", "04242", false},
		{"not a number", "42ab", false},
		{"eleven digits", "12345678901", false},
		{"signed", "-4242", false},
		{"zero", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t)
			e.d.cfg.Verbose = true
			logs := captureLog(t)
			b, err := json.Marshal(map[string]string{
				"event_type": "UserPromptSubmit", "session_id": "raw-1", "agent": "codex", "tmux_pane": paneB,
				"tmux_socket": e.socket, "tmux_server_pid": tc.pid, "timestamp": "2024-01-01T00:00:00Z",
			})
			if err != nil {
				t.Fatal(err)
			}
			newRawFeeder(t, e.d)(string(b))

			if wi := e.d.frontend.WindowInfo("raw-1"); (wi != nil) != tc.wantAttach {
				t.Fatalf("WindowInfo = %+v, wantAttach %v", wi, tc.wantAttach)
			}
			lines := linesContaining(logs.String(), "tmux_server_pid")
			if tc.wantAttach {
				if len(lines) != 0 {
					t.Errorf("a well-formed pid logged %q", lines)
				}
				return
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "malformed") || !strings.Contains(lines[0], "raw-1") {
				t.Fatalf("log lines %q, want one malformed line naming the session", lines)
			}
			if len(tc.pid) > 2 && strings.Contains(lines[0], tc.pid) {
				t.Errorf("log line %q echoes the malformed value", lines[0])
			}
		})
	}
}

// The bind wire and loop hop: a real receiver hands the request to handleBind,
// the loop applies it and the reply reaches the client.
func TestHandleBindRoundTripsThroughTheLoop(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "thread", stalePane, livePID)

	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "e.sock")
	recv, err := ipc.NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	recv.SetBindHandler(e.d.handleBind)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)
	done := make(chan error, 1)
	go func() { done <- e.d.loop(ctx, nil) }()

	resp, err := ipc.SendBind(path, ipc.BindRequest{SessionID: "thread", TmuxPane: paneB, TmuxSocket: e.socket, TmuxServerPID: livePID})
	if err != nil || !resp.OK || resp.Pane != paneB {
		t.Fatalf("SendBind = %+v, %v, want an ack naming %s", resp, err, paneB)
	}
	resp, err = ipc.SendBind(path, ipc.BindRequest{SessionID: "ghost", TmuxPane: paneB, TmuxSocket: e.socket, TmuxServerPID: livePID})
	if err != nil || resp.OK || resp.Reason != ReasonBindUnknownSession {
		t.Fatalf("SendBind(ghost) = %+v, %v, want %q", resp, err, ReasonBindUnknownSession)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("loop returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit")
	}
	if sess := e.d.sessions["thread"]; sess == nil || sess.Binding == nil || sess.Binding.Pane != paneB {
		t.Fatalf("session = %+v, want it bound to B by the loop", sess)
	}
	styled := false
	for _, o := range e.mc.WindowOpts {
		styled = styled || (o.Socket == e.socket && o.Target == winB && o.Key == "@cenci-symbol" && o.Value == e.d.cfg.SymbolRunning)
	}
	if !styled {
		t.Error("the loop did not style B when it applied the bind")
	}
}

// handleBind refuses a malformed request on the connection goroutine without
// waiting for the loop, gives up within its budget when the loop never answers,
// and rate limits valid requests.
func TestHandleBindValidatesAndBoundsTheLoopHop(t *testing.T) {
	d := newTestDaemon(&tmuxtest.MockClient{})
	valid := ipc.BindRequest{SessionID: "019a-thread", TmuxPane: "%5", TmuxSocket: "/tmp/tmux-1/default", TmuxServerPID: livePID}
	for name, mutate := range map[string]func(r *ipc.BindRequest){
		"empty session id":  func(r *ipc.BindRequest) { r.SessionID = "" },
		"path session id":   func(r *ipc.BindRequest) { r.SessionID = "../x" },
		"bad pane":          func(r *ipc.BindRequest) { r.TmuxPane = "5" },
		"empty socket":      func(r *ipc.BindRequest) { r.TmuxSocket = "" },
		"empty pid":         func(r *ipc.BindRequest) { r.TmuxServerPID = "" },
		"malformed pid":     func(r *ipc.BindRequest) { r.TmuxServerPID = "0x10" },
		"control character": func(r *ipc.BindRequest) { r.SessionID = "a\x1bb" },
	} {
		req := valid
		mutate(&req)
		start := time.Now()
		if resp := d.handleBind(req); resp.OK || resp.Reason != ReasonBindInvalidRequest {
			t.Errorf("%s: %+v, want %q", name, resp, ReasonBindInvalidRequest)
		}
		if time.Since(start) > time.Second {
			t.Errorf("%s: the refusal waited on the loop", name)
		}
	}

	old := bindBudget
	bindBudget = 20 * time.Millisecond
	t.Cleanup(func() { bindBudget = old })
	for i := range armBucketBurst {
		if resp := d.handleBind(valid); resp.OK || resp.Reason != ReasonBindTimedOut {
			t.Fatalf("request %d with no loop = %+v, want %q", i, resp, ReasonBindTimedOut)
		}
	}
	if resp := d.handleBind(valid); resp.OK || resp.Reason != ReasonBindRateLimited {
		t.Errorf("request past the burst = %+v, want %q", resp, ReasonBindRateLimited)
	}
	if bindBudget >= 15*time.Second || old >= 15*time.Second {
		t.Errorf("bind budget %s must stay under the client's 15s deadline", old)
	}
}
