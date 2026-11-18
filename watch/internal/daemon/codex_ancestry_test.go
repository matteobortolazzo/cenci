package daemon

// Ticket #1530: a Codex hook pane is attached without a bind only when the
// hook's process ancestry proves it. A proof needs all three: the pane's
// #{pane_pid} is in the ancestry the hook sent, the event's server pid matches
// the live server (#1529), and the nearest Codex ancestor is not the shared
// app-server (the single-client boolean). Every case drives a real tmux frontend
// over tmuxtest.MockClient through handleEvent, applyBind and runSweep; the wire
// keys are covered by the ipc receiver tests and the real `cenci notify` tests,
// the ingest validation by the raw-line test at the end. The fixture's helpers
// (withProof, bindEnv.event, bindEnv.hookEvent, which carries no proof) are in
// codex_binding_test.go.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/procancestry"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// writesOn counts the window-option writes and renames that target window since
// the marks.
func (e *bindEnv) writesOn(window string, opts, renames int) int {
	n := 0
	for _, o := range e.mc.WindowOpts[opts:] {
		if o.Socket == e.socket && o.Target == window {
			n++
		}
	}
	for _, r := range e.mc.Renames[renames:] {
		if r.Socket == e.socket && r.Target == window {
			n++
		}
	}
	return n
}

func (e *bindEnv) marks() (opts, renames int) { return len(e.mc.WindowOpts), len(e.mc.Renames) }

func entryByPane(t *testing.T, snap ipc.StateSnapshot, pane string) ipc.WindowState {
	t.Helper()
	for _, w := range snap.Windows {
		if w.PaneID == pane {
			return w
		}
	}
	t.Fatalf("no snapshot entry on pane %s in %+v", pane, snap.Windows)
	return ipc.WindowState{}
}

// assertProvenOn fails unless the session is attached to pane and the snapshot
// reports that attachment as proven.
func assertProvenOn(t *testing.T, e *bindEnv, session, pane string) {
	t.Helper()
	wi := e.d.frontend.WindowInfo(session)
	if wi == nil || wi.PaneID != pane {
		t.Fatalf("session %s WindowInfo = %+v, want it attached to %s", session, wi, pane)
	}
	if got := entryByPane(t, e.d.buildSnapshot(), pane).Association; got != watch.AssociationProven {
		t.Fatalf("association on %s = %q, want %q", pane, got, watch.AssociationProven)
	}
}

// assertUnprovenAndUntouched fails unless the session is paneless, reported
// unproven with its id, and nothing was written to tmux since the marks.
func assertUnprovenAndUntouched(t *testing.T, e *bindEnv, session string, opts, renames int) {
	t.Helper()
	assertPaneless(t, e.d, session)
	if n := e.writesSince(opts, renames); n != 0 {
		t.Errorf("window writes %+v renames %+v, want none", e.mc.WindowOpts[opts:], e.mc.Renames[renames:])
	}
	if w := entryByTask(t, e.d.buildSnapshot(), session); w.Association != watch.AssociationUnproven || w.SessionID != session {
		t.Errorf("entry = %+v, want unproven with the session id", w)
	}
}

// deepChain is a hook chain of n entries: the hook, pane's pane_pid, then
// filler pids. The fillers start at 5000 so none is a four-digit number that a
// log timestamp (its year) could also contain.
func deepChain(pane string, n int) []string {
	pids := []string{hookPID, panePID(pane)}
	for i := 0; len(pids) < n; i++ {
		pids = append(pids, strconv.Itoa(5000+i))
	}
	return pids
}

// P7: all three conditions together attach and style the hook pane, with no bind.
func TestDaemon_CodexAncestryProofAttachesHookPane(t *testing.T) {
	e := newBindEnv(t)
	e.event("UserPromptSubmit", "thread", paneB, livePID)
	e.d.runSweep()

	assertProvenOn(t, e, "thread", paneB)
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
		t.Errorf("B's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolRunning)
	}
	if got := e.opt(winB, "@cenci-style"); got != e.d.cfg.StyleRunning {
		t.Errorf("B's @cenci-style = %q, want %q", got, e.d.cfg.StyleRunning)
	}
	e.event("Stop", "thread", paneB, livePID)
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
		t.Errorf("after Stop B's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolDone)
	}
	for _, other := range []string{winA, winC} {
		if got := e.opt(other, "@cenci-symbol"); got != "" {
			t.Errorf("%s's @cenci-symbol = %q, want the other panes untouched", other, got)
		}
	}
}

// P7: the nearest Codex ancestor being the shared app-server leaves the session
// unproven while the supplied pane is alive, its pane_pid is in the chain and the
// server pid matches. The control attaches the same event with a single client.
func TestDaemon_CodexSharedAppServerAncestryStaysUnprovenWhilePaneAlive(t *testing.T) {
	e := newBindEnv(t)
	e.d.cfg.Verbose = true
	logs := captureLog(t)
	ev := withProof(e.hookEvent("UserPromptSubmit", "shared", paneA, livePID), paneA)
	ev.AncestrySingleClient = false
	opts, renames := e.marks()

	e.d.handleEvent(ev)
	e.d.runSweep()

	if wi := e.d.frontend.WindowInfo("shared"); wi != nil {
		t.Fatalf("WindowInfo = %+v, want none: the pane is alive but the client is the shared app-server", wi)
	}
	assertUnprovenAndUntouched(t, e, "shared", opts, renames)
	if lines := linesContaining(logs.String(), "carries no process-ancestry proof"); len(lines) != 1 || !strings.Contains(lines[0], "shared") || !strings.Contains(lines[0], "ancestry names no single Codex client") {
		t.Errorf("log lines %q, want one refusal naming the session and the no-single-client cause", lines)
	}

	e.event("UserPromptSubmit", "single", paneA, livePID)
	assertProvenOn(t, e, "single", paneA)
}

// P7: the pane's pane_pid must be in the chain. A chain for another pane, a
// listing row without the pane_pid column, an empty chain and a chain whose
// boolean is true but holds no pane_pid all stay unproven with no tmux write.
func TestDaemon_CodexAncestryWithoutPanePIDStaysUnproven(t *testing.T) {
	for _, tc := range []struct {
		name       string
		listedPID  string // the listed pane_pid of B; "" is a row without the column
		chain      []string
		wantAttach bool
	}{
		{name: "control: the pane_pid is in the chain", listedPID: panePID(paneB), chain: []string{hookPID, panePID(paneB), "1"}, wantAttach: true},
		{name: "the chain holds another pane's pane_pid", listedPID: panePID(paneB), chain: []string{hookPID, panePID(paneC), "1"}},
		{name: "the chain holds no pane_pid", listedPID: panePID(paneB), chain: []string{hookPID, "1"}},
		{name: "a listing row without the pane_pid column", listedPID: "", chain: []string{hookPID, panePID(paneB), "1"}},
		{name: "an empty-looking pane_pid never matches an empty chain entry", listedPID: "", chain: []string{hookPID, "", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := codexPane("2", paneB)
			b.PanePID = tc.listedPID
			e := newBindEnv(t, codexPane("1", paneA), b, codexPane("3", paneC))
			ev := e.hookEvent("UserPromptSubmit", "thread", paneB, livePID)
			ev.AncestryPIDs, ev.AncestrySingleClient = tc.chain, true
			opts, renames := e.marks()

			e.d.handleEvent(ev)
			e.d.runSweep()

			if tc.wantAttach {
				assertProvenOn(t, e, "thread", paneB)
				return
			}
			if wi := e.d.frontend.WindowInfo("thread"); wi != nil {
				t.Fatalf("WindowInfo = %+v, want none", wi)
			}
			assertUnprovenAndUntouched(t, e, "thread", opts, renames)
		})
	}
}

// P7: the server pid rule still applies next to the proof: a valid proof under a
// pid that is not the live server's, or under none, never attaches.
func TestDaemon_CodexAncestryNeedsServerPIDMatch(t *testing.T) {
	for _, tc := range []struct{ name, pid string }{
		{"another server lifetime", deadPID},
		{"no server pid", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t)
			opts, renames := e.marks()
			e.event("UserPromptSubmit", "thread", paneB, tc.pid)
			e.d.runSweep()
			assertUnprovenAndUntouched(t, e, "thread", opts, renames)
		})
	}
}

// P7 and rule 22 of hook-events.md: a SessionEnd (or a clear/resume handoff)
// from a session whose proof fails must neither restore or rename the window of
// the session that does own the pane, nor fire the pending-close registered on
// it. The owner is proven on B; the intruder names B with a failed proof. The
// last case is the control that the owner's own SessionEnd with a valid proof
// restores the window and fires the pending-close.
func TestDaemon_CodexSessionEndWithFailedProofTouchesNothing(t *testing.T) {
	failures := []struct {
		name  string
		apply func(ev *ipc.HookEvent)
	}{
		{"no proof", func(ev *ipc.HookEvent) {}},
		{"the shared app-server", func(ev *ipc.HookEvent) {
			ev.AncestryPIDs, ev.AncestrySingleClient = []string{hookPID, panePID(paneB), "1"}, false
		}},
		{"the pane_pid is not in the chain", func(ev *ipc.HookEvent) {
			ev.AncestryPIDs, ev.AncestrySingleClient = []string{hookPID, panePID(paneC), "1"}, true
		}},
	}
	for _, reason := range []string{"other", "clear", "resume"} {
		for _, f := range failures {
			t.Run(reason+": "+f.name, func(t *testing.T) {
				e := newBindEnv(t)
				e.event("UserPromptSubmit", "owner", paneB, livePID)
				e.d.registerPendingClose(ipc.PendingClose{PaneID: paneB, Socket: e.socket, Session: "main", WindowIndex: "2", WindowName: "owner"})
				fk := e.d.killer.(*fakeKiller)
				opts, renames := e.marks()

				ev := e.hookEvent("SessionEnd", "intruder", paneB, livePID)
				ev.SessionEndReason = reason
				f.apply(&ev)
				e.d.handleEvent(ev)

				if _, ok := e.d.sessions["intruder"]; ok {
					t.Error("the intruder's SessionEnd kept its session")
				}
				if n := e.writesOn(winB, opts, renames); n != 0 {
					t.Errorf("the failed proof made %d writes on the owner's window (%+v, renames %+v)", n, e.mc.WindowOpts[opts:], e.mc.Renames[renames:])
				}
				if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
					t.Errorf("B's @cenci-symbol = %q, want the owner's %q kept", got, e.d.cfg.SymbolRunning)
				}
				if len(fk.killed) != 0 || len(e.d.pending) != 1 {
					t.Errorf("killed = %v pending = %+v, want no kill and the entry kept", fk.killed, e.d.pending)
				}
				assertProvenOn(t, e, "owner", paneB)
			})
		}
	}

	t.Run("control: the owner's own SessionEnd with a valid proof", func(t *testing.T) {
		e := newBindEnv(t)
		e.event("UserPromptSubmit", "owner", paneB, livePID)
		e.d.registerPendingClose(ipc.PendingClose{PaneID: paneB, Socket: e.socket, Session: "main", WindowIndex: "2", WindowName: "owner"})
		fk := e.d.killer.(*fakeKiller)
		opts, renames := e.marks()

		ev := withProof(e.hookEvent("SessionEnd", "owner", paneB, livePID), paneB)
		ev.SessionEndReason = "other"
		e.d.handleEvent(ev)

		if got := e.opt(winB, "@cenci-symbol"); got != "" || e.writesOn(winB, opts, renames) == 0 {
			t.Errorf("B's @cenci-symbol = %q after the owner's SessionEnd, want the window restored", got)
		}
		if len(fk.killed) != 1 || fk.killed[0] != paneB {
			t.Errorf("killed = %v, want the pending-close on %s fired", fk.killed, paneB)
		}
	})
}

// P8: two clients share one app-server whose inherited pane A is alive. Their
// events name A with A's pane_pid in the chain, but the nearest Codex ancestor is
// the shared server, so neither session may set an option on A. After each client
// binds from its own pane, each window carries only its own session's symbol,
// the snapshot shows two distinct pane ids, and later events that still name A
// style only their own windows. Both sessions share one task name, as two
// clients in one cwd do.
func TestDaemon_CodexTwoClientsOnOneAppServerBindTheirOwnPanes(t *testing.T) {
	e := newBindEnv(t)
	shared := func(eventType, session string) ipc.HookEvent {
		ev := withProof(e.hookEvent(eventType, session, paneA, livePID), paneA)
		ev.TaskName = "shared task"
		ev.AncestrySingleClient = false
		return ev
	}

	e.d.handleEvent(shared("UserPromptSubmit", "client-1"))
	e.d.handleEvent(shared("Stop", "client-2"))
	e.d.runSweep()
	if len(e.mc.WindowOpts) != 0 || len(e.mc.Renames) != 0 {
		t.Fatalf("window writes %+v renames %+v, want none on A without a proof", e.mc.WindowOpts, e.mc.Renames)
	}
	snap := e.d.buildSnapshot()
	if snap.Summary.Total != 2 {
		t.Fatalf("summary = %+v, want both clients listed", snap.Summary)
	}
	for _, w := range snap.Windows {
		if w.PaneID != "" || w.Association != watch.AssociationUnproven {
			t.Errorf("entry = %+v, want paneless and unproven", w)
		}
	}

	e.mustBind(t, "client-1", paneB)
	e.mustBind(t, "client-2", paneC)
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
		t.Errorf("B's @cenci-symbol = %q, want client-1's %q", got, e.d.cfg.SymbolRunning)
	}
	if got := e.opt(winC, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
		t.Errorf("C's @cenci-symbol = %q, want client-2's %q", got, e.d.cfg.SymbolDone)
	}
	snap = e.d.buildSnapshot()
	if len(snap.Windows) != 2 || snap.Windows[0].PaneID == snap.Windows[1].PaneID {
		t.Fatalf("snapshot = %+v, want two entries on distinct panes", snap.Windows)
	}
	if b, c := entryByPane(t, snap, paneB), entryByPane(t, snap, paneC); b.Association != watch.AssociationBound || c.Association != watch.AssociationBound ||
		b.Status != "running" || c.Status != "done" {
		t.Errorf("entries B %+v C %+v, want two bound entries (running, done)", b, c)
	}

	e.d.handleEvent(shared("Stop", "client-1"))
	e.d.handleEvent(shared("PreToolUse", "client-2"))
	e.d.runSweep()
	if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
		t.Errorf("after client-1's Stop B's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolDone)
	}
	if got := e.opt(winC, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
		t.Errorf("after client-2's PreToolUse C's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolRunning)
	}
	if n := e.writesOn(winA, 0, 0); n != 0 {
		t.Errorf("%d writes reached A's window, want none for the whole scenario", n)
	}
}

// P9: for a bound session and a proven one, each lifecycle event changes only
// the acting session's window options and snapshot status. The bound session's
// events still name the shared server's pane A.
func TestDaemon_CodexBoundAndProvenLifecycleTouchesOnlyOwnWindow(t *testing.T) {
	cfgSymbols := func(e *bindEnv) map[string][2]string { // status -> symbol, style
		return map[string][2]string{
			"running":    {e.d.cfg.SymbolRunning, e.d.cfg.StyleRunning},
			"need-input": {e.d.cfg.SymbolNeedInput, e.d.cfg.StyleNeedInput},
			"done":       {e.d.cfg.SymbolDone, e.d.cfg.StyleDone},
			"stopped":    {e.d.cfg.SymbolStopped, e.d.cfg.StyleStopped},
		}
	}
	events := []struct{ name, event, tool, start, wantStatus string }{
		{"UserPromptSubmit", "UserPromptSubmit", "", "Stop", "running"},
		{"PreToolUse", "PreToolUse", "exec_command", "Stop", "running"},
		{"PostToolUse", "PostToolUse", "exec_command", "Stop", "running"},
		{"PermissionRequest", "PermissionRequest", "exec_command", "UserPromptSubmit", "need-input"},
		{"PreToolUse request_user_input", "PreToolUse", "request_user_input", "UserPromptSubmit", "need-input"},
		{"Stop", "Stop", "", "UserPromptSubmit", "done"},
		{"Interrupt", "Interrupt", "", "UserPromptSubmit", "stopped"},
		{"SessionEnd", "SessionEnd", "", "UserPromptSubmit", ""},
	}
	for _, acting := range []string{"bound", "proven"} {
		for _, tc := range events {
			t.Run(acting+" session: "+tc.name, func(t *testing.T) {
				e := newBindEnv(t)
				// "bound" is pinned to B (its hook events name A, the shared pane);
				// "proven" is attached to C by its own proof.
				deliver := func(session, eventType, tool string) {
					pane := paneC
					if session == "bound" {
						pane = paneA
					}
					ev := withProof(e.hookEvent(eventType, session, pane, livePID), pane)
					ev.ToolName = tool
					e.d.handleEvent(ev)
				}
				deliver("bound", "UserPromptSubmit", "")
				e.mustBind(t, "bound", paneB)
				deliver("proven", "UserPromptSubmit", "")
				assertProvenOn(t, e, "proven", paneC)

				win, other, otherWin := winB, "proven", winC
				if acting == "proven" {
					win, other, otherWin = winC, "bound", winB
				}
				deliver(acting, tc.start, "")
				deliver(other, "Stop", "")
				otherSymbol, otherStyle := e.opt(otherWin, "@cenci-symbol"), e.opt(otherWin, "@cenci-style")
				otherStatus := entryOfWindow(t, e, otherWin).Status
				if otherStatus != "done" {
					t.Fatalf("setup: the other session's status = %q, want done", otherStatus)
				}
				opts, renames := e.marks()

				deliver(acting, tc.event, tc.tool)
				e.d.runSweep()

				if n := e.writesOn(otherWin, opts, renames); n != 0 {
					t.Errorf("the acting session's %s made %d writes on the other window (%+v)", tc.name, n, e.mc.WindowOpts[opts:])
				}
				if got := e.opt(otherWin, "@cenci-symbol"); got != otherSymbol {
					t.Errorf("the other window's symbol = %q, want %q unchanged", got, otherSymbol)
				}
				if got := e.opt(otherWin, "@cenci-style"); got != otherStyle {
					t.Errorf("the other window's style = %q, want %q unchanged", got, otherStyle)
				}
				if got := entryOfWindow(t, e, otherWin).Status; got != otherStatus {
					t.Errorf("the other session's snapshot status = %q, want %q unchanged", got, otherStatus)
				}

				if tc.event == "SessionEnd" {
					if _, ok := e.d.sessions[acting]; ok {
						t.Errorf("SessionEnd kept the %s session", acting)
					}
					if got := e.opt(win, "@cenci-symbol"); got != "" {
						t.Errorf("the ended session's window symbol = %q, want it restored", got)
					}
					return
				}
				want := cfgSymbols(e)[tc.wantStatus]
				if got := e.opt(win, "@cenci-symbol"); got != want[0] {
					t.Errorf("own window symbol = %q, want %q", got, want[0])
				}
				if got := e.opt(win, "@cenci-style"); got != want[1] {
					t.Errorf("own window style = %q, want %q", got, want[1])
				}
				if got := entryOfWindow(t, e, win).Status; got != tc.wantStatus {
					t.Errorf("own snapshot status = %q, want %q", got, tc.wantStatus)
				}
			})
		}
	}
}

// entryOfWindow is the snapshot entry of the window whose target is window on
// the trusted socket (the fixture's panes each have their own window).
func entryOfWindow(t *testing.T, e *bindEnv, window string) ipc.WindowState {
	t.Helper()
	pane := map[string]string{winA: paneA, winB: paneB, winC: paneC}[window]
	return entryByPane(t, e.d.buildSnapshot(), pane)
}

// P9: a subagent event (agent_id set) never moves the parent's pane, never ends
// it and never leaves it in a terminal status, whether the parent is proven by an
// ancestry proof, attached without a socket (the sandbox path) or bound. The
// child's own proof for another pane is ignored. The last step of the first case
// is the control that the guard is narrow: the same event from the main agent
// does move the session.
func TestDaemon_CodexSubagentEventNeverRebindsOrEndsParent(t *testing.T) {
	childEvents := []struct{ name, event, tool string }{
		{"Stop", "Stop", ""},
		{"Interrupt", "Interrupt", ""},
		{"SessionEnd", "SessionEnd", ""},
		{"PreToolUse", "PreToolUse", "exec_command"},
	}

	check := func(t *testing.T, e *bindEnv, deliver func(ipc.HookEvent), parent ipc.HookEvent, paneOfParent, otherPane string, parentSym string) {
		t.Helper()
		for _, tc := range childEvents {
			child := parent
			child.EventType, child.ToolName, child.AgentID = tc.event, tc.tool, "child-1"
			deliver(child)
			e.d.runSweep()
			sess := e.d.sessions["parent"]
			if sess == nil {
				t.Fatalf("a child's %s ended the parent session", tc.name)
			}
			if sess.TmuxPane != paneOfParent {
				t.Fatalf("a child's %s moved the parent to %q, want %q", tc.name, sess.TmuxPane, paneOfParent)
			}
			if wi := e.d.frontend.WindowInfo("parent"); wi == nil || wi.PaneID != paneOfParent {
				t.Fatalf("a child's %s left the parent's window as %+v, want %s", tc.name, wi, paneOfParent)
			}
			if got := entryByPane(t, e.d.buildSnapshot(), paneOfParent).Status; got != "running" {
				t.Errorf("a child's %s left the parent %q, want running", tc.name, got)
			}
		}
		if n := e.writesOnPane(otherPane); n != 0 {
			t.Errorf("the child's events made %d writes on the other pane's window", n)
		}
		if got := e.symbolOf(paneOfParent); got != parentSym {
			t.Errorf("the parent's window symbol = %q, want %q", got, parentSym)
		}
	}

	t.Run("proven by an ancestry proof", func(t *testing.T) {
		e := newBindEnv(t)
		e.d.cfg.Verbose = true
		logs := captureLog(t)
		e.event("UserPromptSubmit", "parent", paneB, livePID)
		parent := withProof(e.hookEvent("PreToolUse", "parent", paneC, livePID), paneC) // the child's valid proof names C
		before := logs.Len()
		check(t, e, e.d.handleEvent, parent, paneB, paneC, e.d.cfg.SymbolRunning)
		if lines := linesContaining(logs.String()[before:], "parent"); len(lines) == 0 {
			t.Error("a child's event that was not allowed to rebind the parent logged nothing under -verbose")
		}

		// Control: the same event without agent_id moves the session to C.
		ev := parent
		ev.EventType = "PreToolUse"
		e.d.handleEvent(ev)
		if sess := e.d.sessions["parent"]; sess == nil || sess.TmuxPane != paneC {
			t.Errorf("control: the main agent's event left the session on %+v, want it moved to %s", sess, paneC)
		}
	})

	t.Run("attached without a socket (sandbox)", func(t *testing.T) {
		mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{codexPane("2", paneB), codexPane("3", paneC)}}
		d := newTestDaemon(mc)
		e := &bindEnv{d: d, mc: mc}
		parent := ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "parent", Agent: "codex", TaskName: "parent", TmuxPane: paneB}
		d.handleEvent(parent)
		if wi := d.frontend.WindowInfo("parent"); wi == nil || wi.PaneID != paneB {
			t.Fatalf("setup: WindowInfo = %+v, want %s attached without a socket", wi, paneB)
		}
		wrong := parent
		wrong.TmuxPane = paneC
		check(t, e, d.handleEvent, wrong, paneB, paneC, d.cfg.SymbolRunning)
	})

	t.Run("bound", func(t *testing.T) {
		e := newBindEnv(t)
		e.event("UserPromptSubmit", "parent", paneA, livePID)
		e.mustBind(t, "parent", paneB)
		parent := withProof(e.hookEvent("PreToolUse", "parent", paneC, livePID), paneC)
		check(t, e, e.d.handleEvent, parent, paneB, paneC, e.d.cfg.SymbolRunning)
	})
}

// The agent_id no-rebind guard is Codex-only (Q&A 4, "Claude is untouched"): a
// Claude session attached to B whose event carries an agent_id and names another
// live pane C moves to C, exactly as the same event without agent_id does, and
// never goes through the ancestry gate (it is not marked unproven, whatever proof
// the event carries). The shape is identical to the Codex subagent case, whose
// control row is that the Codex session stays on B and the Claude one does not.
func TestDaemon_ClaudeAgentIDEventKeepsTodaysPaneBehavior(t *testing.T) {
	move := func(e *bindEnv, agent, agentID string) *frontend.SessionState {
		e.d.handleEvent(withProof(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess", Agent: agent, TaskName: "sess",
			TmuxPane: paneB, TmuxSocket: e.socket, TmuxServerPID: livePID}, paneB))
		if wi := e.d.frontend.WindowInfo("sess"); wi == nil || wi.PaneID != paneB {
			t.Fatalf("setup: %s session WindowInfo = %+v, want it attached to %s", agent, wi, paneB)
		}
		e.d.handleEvent(withProof(ipc.HookEvent{EventType: "PreToolUse", ToolName: "Bash", SessionID: "sess", Agent: agent, AgentID: agentID,
			TaskName: "sess", TmuxPane: paneC, TmuxSocket: e.socket, TmuxServerPID: livePID}, paneC))
		e.d.runSweep()
		return e.d.sessions["sess"]
	}

	for _, agentID := range []string{"", "child-1"} {
		name := "claude event with agent_id=" + agentID
		if agentID == "" {
			name = "control: claude event without agent_id"
		}
		t.Run(name, func(t *testing.T) {
			e := newBindEnv(t)
			sess := move(e, "claude", agentID)
			if sess == nil || sess.TmuxPane != paneC || sess.Unproven {
				t.Fatalf("session = %+v, want it moved to %s and not unproven", sess, paneC)
			}
			if wi := e.d.frontend.WindowInfo("sess"); wi == nil || wi.PaneID != paneC {
				t.Errorf("WindowInfo = %+v, want the window on %s as today", wi, paneC)
			}
		})
	}

	t.Run("control: the same shape for a codex session is refused", func(t *testing.T) {
		e := newBindEnv(t)
		sess := move(e, "codex", "child-1")
		if sess == nil || sess.TmuxPane != paneB || sess.Unproven {
			t.Fatalf("session = %+v, want it kept on %s", sess, paneB)
		}
		assertProvenOn(t, e, "sess", paneB)
	})
}

// writesOnPane counts the writes on the window of the fixture pane (socket-less
// fixtures record the empty socket).
func (e *bindEnv) writesOnPane(pane string) int {
	target := map[string]string{paneA: winA, paneB: winB, paneC: winC}[pane]
	n := 0
	for _, o := range e.mc.WindowOpts {
		if o.Target == target {
			n++
		}
	}
	return n
}

func (e *bindEnv) symbolOf(pane string) string {
	target := map[string]string{paneA: winA, paneB: winB, paneC: winC}[pane]
	for i := len(e.mc.WindowOpts) - 1; i >= 0; i-- {
		if o := e.mc.WindowOpts[i]; o.Target == target && o.Key == "@cenci-symbol" {
			return o.Value
		}
	}
	return ""
}

// P20: an event with no proof (an older notify, a failed or timed-out read, a
// proof whose boolean is false or whose chain is empty) attaches nothing to a
// fresh session, and it logs one refusal under -verbose. The "keeps" cases are
// the control that such an event does not detach a session that is already
// proven: it stays on its pane and is still styled by the applied event.
func TestDaemon_CodexEventWithoutAncestryIsUnproven(t *testing.T) {
	const (
		noReadCause      = "(no ancestry read: the hook-side read failed or was not attempted)"
		noSingleCliCause = "(ancestry names no single Codex client: shared app-server, no Codex ancestor, or no shared-server marker recorded)"
	)
	noProof := []struct {
		name  string
		cause string // the verbose-log cause suffix of the refusal
		apply func(ev *ipc.HookEvent)
	}{
		{"no ancestry keys", noReadCause, func(ev *ipc.HookEvent) {}},
		{"a single-client flag with no pids", noReadCause, func(ev *ipc.HookEvent) { ev.AncestrySingleClient = true }},
		{"pids with no single-client flag", noSingleCliCause, func(ev *ipc.HookEvent) { ev.AncestryPIDs = []string{hookPID, panePID(paneB), "1"} }},
		{"a chain over the depth cap", noReadCause, func(ev *ipc.HookEvent) {
			ev.AncestryPIDs, ev.AncestrySingleClient = deepChain(paneB, procancestry.MaxDepth+1), true
		}},
	}
	t.Run("a fresh session stays unproven", func(t *testing.T) {
		for _, tc := range noProof {
			t.Run(tc.name, func(t *testing.T) {
				e := newBindEnv(t)
				e.d.cfg.Verbose = true
				logs := captureLog(t)
				ev := e.hookEvent("UserPromptSubmit", "thread", paneB, livePID)
				tc.apply(&ev)
				opts, renames := e.marks()

				e.d.handleEvent(ev)
				e.d.runSweep()

				assertUnprovenAndUntouched(t, e, "thread", opts, renames)
				if lines := linesContaining(logs.String(), "carries no process-ancestry proof"); len(lines) != 1 || !strings.Contains(lines[0], "thread") || !strings.Contains(lines[0], tc.cause) {
					t.Errorf("log lines %q, want one refusal naming the session and ending in the cause %q", lines, tc.cause)
				}
			})
		}
	})

	t.Run("a proven session keeps its association", func(t *testing.T) {
		for _, tc := range noProof {
			t.Run(tc.name, func(t *testing.T) {
				e := newBindEnv(t)
				e.event("UserPromptSubmit", "thread", paneB, livePID)
				assertProvenOn(t, e, "thread", paneB)

				ev := e.hookEvent("Stop", "thread", paneC, livePID) // even naming another pane
				tc.apply(&ev)
				e.d.handleEvent(ev)
				e.d.runSweep()

				assertProvenOn(t, e, "thread", paneB)
				if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
					t.Errorf("B's @cenci-symbol = %q, want the applied Stop styled on the kept pane (%q)", got, e.d.cfg.SymbolDone)
				}
				if got := e.opt(winC, "@cenci-symbol"); got != "" {
					t.Errorf("C's @cenci-symbol = %q, want the unproven pane untouched", got)
				}
				if sess := e.d.sessions["thread"]; sess == nil || sess.Unproven {
					t.Errorf("session = %+v, want it kept proven, not flagged unproven", sess)
				}
			})
		}
	})
}

// P21: a chain of exactly the depth cap is accepted, one more is unproven and
// never proof: it is cleared at ingest, so the pane is not attached, and it does
// not detach a session that is already proven.
func TestDaemon_CodexAncestryDeeperThanCapIsUnproven(t *testing.T) {
	if procancestry.MaxDepth != 32 {
		t.Fatalf("MaxDepth = %d, want 32", procancestry.MaxDepth)
	}
	for _, tc := range []struct {
		name       string
		depth      int
		wantAttach bool
	}{
		{"control: at the cap", procancestry.MaxDepth, true},
		{"one past the cap", procancestry.MaxDepth + 1, false},
		{"far past the cap", 1000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t)
			ev := e.hookEvent("UserPromptSubmit", "thread", paneB, livePID)
			ev.AncestryPIDs, ev.AncestrySingleClient = deepChain(paneB, tc.depth), true
			opts, renames := e.marks()

			e.d.handleEvent(ev)
			e.d.runSweep()

			if tc.wantAttach {
				assertProvenOn(t, e, "thread", paneB)
				return
			}
			assertUnprovenAndUntouched(t, e, "thread", opts, renames)
		})
	}
}

// P28: ownership is never inferred from a pane title, a window name or a single
// matching pane. The only listed pane is a codex pane whose title and window name
// both equal the task; events that name no pane, a pane that is gone, or that
// very pane without a proof leave the session unproven and write nothing. The
// cwd and the active window are not tested: neither reaches the association path
// (paneAdoptable, adoptEventPane, the frontend's OnEvent and identity check).
// ipc.HookEvent carries no cwd, and tmux.PaneInfo, the listing that path reads,
// carries neither a current path nor an active flag, so no fixture can feed them
// to it. A pane's cwd is read elsewhere (worktree cleanup through
// tmux.ListLivePanes, and the window restyle's @cenci-repo-dir option), but
// neither read feeds the association.
func TestDaemon_CodexOwnershipNeverInferredFromTitleWindowNameOrSinglePane(t *testing.T) {
	only := codexPane("2", paneB)
	only.PaneTitle = "inferred-task"
	only.WindowName = "inferred-task"
	for _, tc := range []struct {
		name  string
		event func(e *bindEnv) ipc.HookEvent
	}{
		{"no pane at all", func(e *bindEnv) ipc.HookEvent {
			return ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "inferred-task", Agent: "codex", TaskName: "inferred-task"}
		}},
		{"a pane that is gone", func(e *bindEnv) ipc.HookEvent {
			return e.hookEvent("UserPromptSubmit", "inferred-task", stalePane, livePID)
		}},
		{"the single listed pane, without a proof", func(e *bindEnv) ipc.HookEvent {
			return e.hookEvent("UserPromptSubmit", "inferred-task", paneB, livePID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t, only)
			opts, renames := e.marks()

			e.d.handleEvent(tc.event(e))
			e.d.runSweep()

			assertPaneless(t, e.d, "inferred-task")
			if n := e.writesSince(opts, renames); n != 0 {
				t.Errorf("window writes %+v renames %+v, want none: the pane's title, window name and uniqueness prove nothing", e.mc.WindowOpts, e.mc.Renames)
			}
			w := entryByTask(t, e.d.buildSnapshot(), "inferred-task")
			if w.PaneID != "" || w.Association == watch.AssociationProven || w.Association == watch.AssociationBound {
				t.Errorf("entry = %+v, want paneless and not proven or bound", w)
			}
		})
	}
}

// P7 on a tracked window: a session proven on B and styled there whose pane's
// live pane_pid later stops being in the stored ancestry (the pane id now names
// another process) forgets the window without restoring it. The next event, a
// SessionEnd or a clear handoff writes nothing to B's window (no restore of the
// original name or styles), the session becomes paneless and unproven, and no
// later sweep writes either. The first case is the control that the unchanged
// pane_pid keeps styling the window.
func TestDaemon_CodexTrackedWindowForgottenWithoutRestoreOnAncestryMismatch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		event         string
		reason        string
		changePanePID bool
		wantKept      bool
	}{
		{name: "control: the pane_pid is unchanged", event: "PreToolUse", wantKept: true},
		{name: "the next event", event: "PreToolUse", changePanePID: true},
		{name: "a SessionEnd", event: "SessionEnd", reason: "other", changePanePID: true},
		{name: "a clear handoff", event: "SessionEnd", reason: "clear", changePanePID: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t)
			e.event("UserPromptSubmit", "thread", paneB, livePID)
			assertProvenOn(t, e, "thread", paneB)
			styled := e.opt(winB, "@cenci-symbol")
			if styled != e.d.cfg.SymbolRunning {
				t.Fatalf("setup: B's @cenci-symbol = %q, want %q", styled, e.d.cfg.SymbolRunning)
			}
			if tc.changePanePID {
				e.mc.PanesBySocket[e.socket] = []tmux.PaneInfo{codexPane("1", paneA), reusedPane(codexPane("2", paneB)), codexPane("3", paneC)}
			}
			opts, renames := e.marks()

			ev := e.hookEvent(tc.event, "thread", paneB, livePID)
			ev.SessionEndReason = tc.reason
			e.d.handleEvent(withProof(ev, paneB)) // the chain still holds the pane_pid it was claimed under
			e.d.runSweep()

			if tc.wantKept {
				assertProvenOn(t, e, "thread", paneB)
				return
			}
			if wi := e.d.frontend.WindowInfo("thread"); wi != nil {
				t.Fatalf("WindowInfo = %+v, want the window forgotten", wi)
			}
			if n := e.writesOn(winB, opts, renames); n != 0 {
				t.Errorf("the mismatch made %d writes on B's window (%+v, renames %+v), want it forgotten unrestored", n, e.mc.WindowOpts[opts:], e.mc.Renames[renames:])
			}
			if got := e.opt(winB, "@cenci-symbol"); got != styled {
				t.Errorf("B's @cenci-symbol = %q, want the last styled value %q left untouched", got, styled)
			}
			if tc.event == "SessionEnd" {
				if _, ok := e.d.sessions["thread"]; ok {
					t.Error("the SessionEnd kept its session")
				}
				return
			}
			assertPaneless(t, e.d, "thread")
			if w := entryByTask(t, e.d.buildSnapshot(), "thread"); w.Association != watch.AssociationUnproven || w.PaneID != "" {
				t.Errorf("entry = %+v, want paneless and unproven", w)
			}
		})
	}
}

// reusedPane is p with a different live pane_pid: the same pane id, now running
// another process.
func reusedPane(p tmux.PaneInfo) tmux.PaneInfo {
	p.PanePID = "8888"
	return p
}

// A binding outranks a proof: a session bound to B stays on B when its later
// events carry a valid proof for A or C (a proof's pane_pid check never reaches
// the bound pane, whose pane_pid is not in the hook's chain), and another
// session's proven event never takes the bound pane.
func TestDaemon_CodexBindingOutranksAncestryProof(t *testing.T) {
	t.Run("the bound session's later proofs for other panes", func(t *testing.T) {
		e := newBindEnv(t)
		e.event("UserPromptSubmit", "thread", paneA, livePID)
		if got := e.opt(winA, "@cenci-symbol"); got != e.d.cfg.SymbolRunning {
			t.Fatalf("control: A's @cenci-symbol = %q, want the proven pane styled before the bind", got)
		}
		e.mustBind(t, "thread", paneB)
		if got := e.opt(winA, "@cenci-symbol"); got != "" {
			t.Errorf("A's @cenci-symbol after the bind = %q, want it restored", got)
		}
		opts, renames := e.marks()

		e.event("PreToolUse", "thread", paneA, livePID)
		e.event("PermissionRequest", "thread", paneC, livePID)
		e.event("Stop", "thread", paneA, livePID)
		e.d.runSweep()

		if n := e.writesOn(winA, opts, renames) + e.writesOn(winC, opts, renames); n != 0 {
			t.Errorf("proofs for A and C made %d writes there, want only B styled", n)
		}
		if got := e.opt(winB, "@cenci-symbol"); got != e.d.cfg.SymbolDone {
			t.Errorf("B's @cenci-symbol = %q, want %q", got, e.d.cfg.SymbolDone)
		}
		sess := e.d.sessions["thread"]
		if sess == nil || sess.Binding == nil || sess.TmuxPane != paneB {
			t.Fatalf("session = %+v, want the binding to B kept", sess)
		}
		if got := entryByPane(t, e.d.buildSnapshot(), paneB).Association; got != watch.AssociationBound {
			t.Errorf("association = %q, want %q", got, watch.AssociationBound)
		}
	})

	t.Run("another session's valid proof for the bound pane", func(t *testing.T) {
		e := newBindEnv(t)
		e.event("UserPromptSubmit", "owner", paneA, livePID)
		e.mustBind(t, "owner", paneB)
		opts, renames := e.marks()

		e.event("Stop", "rival", paneB, livePID) // a full, valid proof naming the bound pane

		assertPaneless(t, e.d, "rival")
		if n := e.writesOn(winB, opts, renames); n != 0 {
			t.Errorf("the rival's proof made %d writes on the bound window", n)
		}
		if wi := e.d.frontend.WindowInfo("owner"); wi == nil || wi.PaneID != paneB {
			t.Errorf("owner's window = %+v, want it kept on %s", wi, paneB)
		}
	})
}

// Hook-events rule 3: malformed ancestry written raw to a real EventReceiver is
// treated as absent (unproven) with one verbose line naming ancestry_pids that
// never echoes the value. An entry that is not a pid, or more than the depth cap
// of entries, voids the whole chain. The first case is the control that a
// well-formed proof on the same path attaches and logs nothing.
func TestRawHookEventWithMalformedAncestryIsTreatedAsAbsent(t *testing.T) {
	tooMany := deepChain(paneB, procancestry.MaxDepth+1)
	for _, tc := range []struct {
		name       string
		pids       []string
		wantAttach bool
	}{
		{"control: a well-formed chain", []string{hookPID, panePID(paneB), "1"}, true},
		{"leading zero", []string{hookPID, panePID(paneB), "04242"}, false},
		{"not a number", []string{hookPID, panePID(paneB), "42ab"}, false},
		{"a command line", []string{hookPID, panePID(paneB), "bash -c secret-token-xyz"}, false},
		{"eleven digits", []string{hookPID, panePID(paneB), "12345678901"}, false},
		{"signed", []string{hookPID, panePID(paneB), "-4242"}, false},
		{"zero", []string{hookPID, panePID(paneB), "0"}, false},
		{"an empty entry", []string{hookPID, panePID(paneB), ""}, false},
		{"over the depth cap", tooMany, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBindEnv(t)
			e.d.cfg.Verbose = true
			logs := captureLog(t)
			b, err := json.Marshal(map[string]any{
				"event_type": "UserPromptSubmit", "session_id": "raw-2", "agent": "codex", "tmux_pane": paneB,
				"tmux_socket": e.socket, "tmux_server_pid": livePID, "timestamp": "2024-01-01T00:00:00Z",
				"ancestry_pids": tc.pids, "ancestry_single_client": true,
			})
			if err != nil {
				t.Fatal(err)
			}
			newRawFeeder(t, e.d)(string(b))

			if wi := e.d.frontend.WindowInfo("raw-2"); (wi != nil) != tc.wantAttach {
				t.Fatalf("WindowInfo = %+v, wantAttach %v", wi, tc.wantAttach)
			}
			lines := linesContaining(logs.String(), "ancestry_pids")
			if tc.wantAttach {
				if len(lines) != 0 {
					t.Errorf("a well-formed chain logged %q", lines)
				}
				return
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "malformed") || !strings.Contains(lines[0], "raw-2") {
				t.Fatalf("log lines %q, want one malformed line naming the session", lines)
			}
			for _, pid := range tc.pids[2:] {
				if len(pid) > 2 && strings.Contains(lines[0], pid) {
					t.Errorf("log line %q echoes the malformed value %q", lines[0], pid)
				}
			}
			if sess := e.d.sessions["raw-2"]; sess == nil || sess.TmuxPane != "" || len(sess.AncestryPIDs) != 0 {
				t.Errorf("session = %+v, want it paneless with no stored chain", sess)
			}
		})
	}
}
