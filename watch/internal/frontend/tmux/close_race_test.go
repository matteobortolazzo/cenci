package tmux

import (
	"fmt"
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
