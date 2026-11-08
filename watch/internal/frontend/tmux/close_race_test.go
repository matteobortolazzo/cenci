package tmux

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	tmuxc "github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// Closing a window can happen after ListPanes returns but before the next tmux
// command. Model that boundary explicitly: a second listing cannot close it.
type closingClient struct {
	*tmuxtest.MockClient
	closeAfterList bool
}

func (c *closingClient) ListPanes(string) ([]tmuxc.PaneInfo, error) {
	panes := append([]tmuxc.PaneInfo(nil), c.Panes...)
	if c.closeAfterList {
		c.closeAfterList = false
		c.Panes = c.Panes[1:]
		c.Panes[0].WindowIndex = "0"
		for key, value := range c.WindowOptValues {
			const old = "main:1:"
			if len(key) >= len(old) && key[:len(old)] == old {
				c.WindowOptValues["main:0:"+key[len(old):]] = value
			}
		}
	}
	return panes, nil
}

func (c *closingClient) resolve(target string) (string, error) {
	for _, p := range c.Panes {
		if target == p.WindowTarget() || target == p.PaneID {
			return p.WindowTarget(), nil
		}
	}
	return "", fmt.Errorf("can't find pane: %s", target)
}

func (c *closingClient) RenameWindow(socket, target, name string) error {
	resolved, err := c.resolve(target)
	if err != nil {
		return err
	}
	return c.MockClient.RenameWindow(socket, resolved, name)
}

func (c *closingClient) SetWindowOption(socket, target, key, value string) error {
	resolved, err := c.resolve(target)
	if err != nil {
		return err
	}
	return c.MockClient.SetWindowOption(socket, resolved, key, value)
}

func TestClosingWindowDuringRestoreDoesNotResetSurvivor(t *testing.T) {
	c := &closingClient{MockClient: &tmuxtest.MockClient{Panes: []tmuxc.PaneInfo{
		{SessionName: "main", WindowIndex: "0", WindowName: "docker", PaneCurrentCmd: "docker", PaneTitle: "◐ first task", PaneID: "%0"},
		{SessionName: "main", WindowIndex: "1", WindowName: "docker", PaneCurrentCmd: "docker", PaneTitle: "◐ second task", PaneID: "%1"},
	}}}
	f := New(config.Default(), c)
	sessions := map[string]*frontend.SessionState{}
	for _, id := range []string{"0", "1"} {
		sess := &frontend.SessionState{SessionID: id, TmuxPane: "%" + id, Agent: "claude", Status: detect.StatusRunning}
		sessions[id] = sess
		f.OnEvent(sess, ipc.HookEvent{EventType: "PreToolUse", SessionID: id})
	}
	c.closeAfterList = true
	f.OnSessionEnd(sessions["0"])
	delete(sessions, "0")
	f.Sweep(sessions)
	if c.Panes[0].WindowName != "second task" {
		t.Errorf("survivor name = %q, want second task", c.Panes[0].WindowName)
	}
	for key, want := range map[string]string{
		"@cenci-symbol":       config.Default().SymbolRunning,
		"window-status-style": config.Default().StyleRunning,
		"automatic-rename":    "off",
	} {
		if got := c.WindowOptValues["main:0:"+key]; got != want {
			t.Errorf("survivor %s = %q, want %q", key, got, want)
		}
	}
	if wi := f.WindowInfo("1"); wi == nil || wi.WindowName != "second task" || wi.WindowIndex != "0" {
		t.Errorf("survivor lost tracking after close: %+v", wi)
	}
}

// #1428: the frontend still talks to the ambient server, so every tmux call it
// makes carries the empty socket, and MockClient records one socket per call
// (readable by tests) across ListPanes, RenameWindow, SetWindowOption and
// SetOption.
func TestFrontendCallsRecordTheEmptySocket(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmuxc.PaneInfo{
		{SessionName: "main", WindowIndex: "0", WindowName: "docker", PaneCurrentCmd: "docker", PaneTitle: "◐ first task", PaneID: "%0"},
	}}
	f := New(config.Default(), mc)
	sess := &frontend.SessionState{SessionID: "0", TmuxPane: "%0", Agent: "claude", Status: detect.StatusRunning}

	f.OnEvent(sess, ipc.HookEvent{EventType: "PreToolUse", SessionID: "0"})
	f.RenderHeadroom(map[string]float64{"claude": 0.5})
	f.OnSessionEnd(sess)
	f.Sweep(map[string]*frontend.SessionState{})

	if mc.ListCalls == 0 || len(mc.Renames) == 0 || len(mc.WindowOpts) == 0 || len(mc.Opts) == 0 {
		t.Fatalf("flow did not exercise every recorded call: list=%d renames=%d windowOpts=%d opts=%d",
			mc.ListCalls, len(mc.Renames), len(mc.WindowOpts), len(mc.Opts))
	}
	if calls := mc.ListCalls + len(mc.Renames) + len(mc.WindowOpts) + len(mc.Opts); len(mc.Sockets) < calls {
		t.Errorf("MockClient recorded %d sockets, want at least one per call (%d)", len(mc.Sockets), calls)
	}
	for i, socket := range mc.Sockets {
		if socket != "" {
			t.Errorf("call %d targeted socket %q, want the ambient server (empty socket)", i, socket)
		}
	}
}

// trackedOnSockets returns a frontend that tracks windows on three servers: the
// ambient one ("d"), A (two windows, "a1" and "a2") and B ("b"), plus the mock
// that backs them.
func trackedOnSockets(t *testing.T) (*Frontend, *tmuxtest.MockClient, map[string]*frontend.SessionState) {
	t.Helper()
	mc := &tmuxtest.MockClient{
		Panes: []tmuxc.PaneInfo{socketPane("bash", "0", "%0", "default task")},
		PanesBySocket: map[string][]tmuxc.PaneInfo{
			sockA: {socketPane("bash", "0", "%1", "alpha one"), socketPane("bash", "1", "%2", "alpha two")},
			sockB: {socketPane("bash", "0", "%3", "beta task")},
		},
	}
	f := New(config.Default(), mc)
	sessions := map[string]*frontend.SessionState{
		"d":  runningOn("d", "%0", ""),
		"a1": runningOn("a1", "%1", sockA),
		"a2": runningOn("a2", "%2", sockA),
		"b":  runningOn("b", "%3", sockB),
	}
	for _, id := range []string{"d", "a1", "a2", "b"} {
		touch(f, sessions[id])
		if f.WindowInfo(id) == nil {
			t.Fatalf("setup: session %s was not tracked", id)
		}
	}
	return f, mc, sessions
}

// A sweep tick lists panes once per distinct socket that hosts a tracked window
// or a pane-backed session: windows on "", A, A and B, plus a windowless session
// on C, give one listing each, in sorted socket order.
func TestSweepListsPanesOncePerDistinctSocket(t *testing.T) {
	f, mc, sessions := trackedOnSockets(t)
	sessions["c"] = &frontend.SessionState{SessionID: "c", TmuxPane: "%9", TmuxSocket: sockC, Agent: "claude", Status: detect.StatusRunning}
	mc.ListSockets = nil

	f.Sweep(sessions)

	want := []string{"", sockA, sockB, sockC}
	if !slices.Equal(mc.ListSockets, want) {
		t.Errorf("ListPanes sockets during one sweep = %q, want %q", mc.ListSockets, want)
	}
}

// A pane gone from its own server is swept even when the same pane id is live on
// another server: A's session is removed with PaneGone, B's is kept, and the
// restore goes to A only.
func TestSweepSweepsPaneGoneFromItsOwnSocketEvenIfLiveElsewhere(t *testing.T) {
	mc := twoSocketMock()
	f := New(config.Default(), mc)
	a, b := runningOn("a", "%5", sockA), runningOn("b", "%5", sockB)
	touch(f, a)
	touch(f, b)
	sessions := map[string]*frontend.SessionState{"a": a, "b": b}

	mc.PanesBySocket[sockA] = nil // %5 closes on A and stays live on B
	renames, opts := len(mc.Renames), len(mc.WindowOpts)
	actions := f.Sweep(sessions)

	var removedA, removedB, paneGoneA bool
	for _, act := range actions {
		switch act.SessionKey {
		case "a":
			removedA, paneGoneA = act.Remove, act.PaneGone
		case "b":
			removedB = act.Remove
		}
	}
	if !removedA || !paneGoneA {
		t.Errorf("actions = %+v, want A's session removed with PaneGone", actions)
	}
	if removedB {
		t.Errorf("actions = %+v, want B's session kept: its pane %%5 is live on its own server", actions)
	}
	if len(mc.Renames) == renames {
		t.Error("no restore reached A's server")
	}
	for _, r := range mc.Renames[renames:] {
		if r.Socket != sockA {
			t.Errorf("restore rename %+v went to %q, want only A (%q)", r, r.Socket, sockA)
		}
	}
	for _, o := range mc.WindowOpts[opts:] {
		if o.Socket != sockA {
			t.Errorf("restore option %+v went to %q, want only A (%q)", o, o.Socket, sockA)
		}
	}
	if f.WindowInfo("b") == nil {
		t.Error("B's window lost its tracking")
	}
}

// A failed listing on one server is never read as "pane gone": that server's
// windows and sessions are left alone for the tick, and the other servers are
// swept normally.
func TestSweepSkipsOnlyTheSocketWhoseListingFails(t *testing.T) {
	mc := twoSocketMock()
	f := New(config.Default(), mc)
	a, b := runningOn("a", "%5", sockA), runningOn("b", "%5", sockB)
	touch(f, a)
	touch(f, b)
	sessions := map[string]*frontend.SessionState{"a": a, "b": b}

	mc.ListErrBySocket = map[string]error{sockA: errors.New("tmux: no server running")}
	mc.PanesBySocket[sockA] = nil // what a failed listing must never be read as
	mc.PanesBySocket[sockB] = nil // B's pane is genuinely gone
	mc.ListSockets = nil
	renames, opts := len(mc.Renames), len(mc.WindowOpts)
	actions := f.Sweep(sessions)

	var removedA, removedB, paneGoneB bool
	for _, act := range actions {
		switch act.SessionKey {
		case "a":
			removedA = act.Remove
		case "b":
			removedB, paneGoneB = act.Remove, act.PaneGone
		}
	}
	if removedA {
		t.Errorf("actions = %+v, want A's session kept: its listing failed", actions)
	}
	if !removedB || !paneGoneB {
		t.Errorf("actions = %+v, want B's session removed with PaneGone: B was swept normally", actions)
	}
	if want := []string{sockA, sockB}; !slices.Equal(mc.ListSockets, want) {
		t.Errorf("ListPanes sockets = %q, want %q: both servers are attempted", mc.ListSockets, want)
	}
	for _, r := range mc.Renames[renames:] {
		if r.Socket == sockA {
			t.Errorf("rename %+v touched A, whose listing failed", r)
		}
	}
	for _, o := range mc.WindowOpts[opts:] {
		if o.Socket == sockA {
			t.Errorf("option %+v touched A, whose listing failed", o)
		}
	}
	if f.WindowInfo("a") == nil {
		t.Error("A's window lost its tracking after a failed listing")
	}
}
