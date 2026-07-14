package daemon

import (
	"testing"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/tmux/tmuxtest"
)

func TestDaemon_PanelessEventCreatesSessionWithoutTmux(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	if mc.ListCalls != 0 {
		t.Errorf("expected zero ListPanes calls for paneless event, got %d", mc.ListCalls)
	}
	if len(mc.Renames) != 0 || len(mc.WindowOpts) != 0 {
		t.Errorf("expected zero tmux calls for paneless event, got %d renames, %d opts", len(mc.Renames), len(mc.WindowOpts))
	}

	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 {
		t.Fatalf("expected 1 snapshot entry, got %d", len(snap.Windows))
	}
	w := snap.Windows[0]
	if w.Session != "" || w.WindowIndex != "" {
		t.Errorf("expected empty session/window_index for paneless entry, got %q:%q", w.Session, w.WindowIndex)
	}
	// Paneless sessions have no pane title, so there is no task-name source.
	if w.WindowName != "" {
		t.Errorf("expected empty WindowName for paneless entry, got %q", w.WindowName)
	}
	if w.TaskName != "" {
		t.Errorf("expected empty TaskName for paneless entry, got %q", w.TaskName)
	}
	if w.Status != "running" {
		t.Errorf("expected status 'running', got %q", w.Status)
	}
	if snap.Summary.Total != 1 || snap.Summary.Running != 1 {
		t.Errorf("expected summary total=1 running=1, got %+v", snap.Summary)
	}
}

func TestDaemon_PanelessSessionEndRemovesSession(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1"})

	snap := d.buildSnapshot()
	if len(snap.Windows) != 0 {
		t.Errorf("expected no snapshot entries after SessionEnd, got %d", len(snap.Windows))
	}
	if len(mc.Renames) != 0 || len(mc.WindowOpts) != 0 {
		t.Errorf("expected zero tmux calls, got %d renames, %d opts", len(mc.Renames), len(mc.WindowOpts))
	}
}

func TestDaemon_EventWithoutSessionIDOrPaneDropped(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit"})

	if len(d.sessions) != 0 {
		t.Errorf("expected no session for event with empty session id and pane, got %d", len(d.sessions))
	}
	if len(d.buildSnapshot().Windows) != 0 {
		t.Error("expected empty snapshot")
	}
}

func TestDaemon_EmptyPaneEventDoesNotDowngradeKnownPane(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	sess := d.sessions["sess1"]
	if sess == nil {
		t.Fatal("expected session tracked")
		return
	}
	if sess.TmuxPane != "%0" {
		t.Errorf("expected known pane %%0 kept on empty-pane event, got %q", sess.TmuxPane)
	}

	// The session must still render as a tmux entry, not paneless.
	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 {
		t.Fatalf("expected 1 snapshot entry, got %d", len(snap.Windows))
	}
	if snap.Windows[0].Session != "main" || snap.Windows[0].WindowIndex != "0" {
		t.Errorf("expected tmux entry main:0, got %q:%q", snap.Windows[0].Session, snap.Windows[0].WindowIndex)
	}
}

func TestDaemon_PanelessUpgradesToPaneOnLaterEvent(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	sess := d.sessions["sess1"]
	if sess == nil || sess.TmuxPane != "%0" {
		t.Fatalf("expected session upgraded to pane %%0, got %+v", sess)
	}

	// tmux tracking should have started on the pane-carrying event.
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected window rename to 'writing tests' after upgrade, got %q (found=%v)", name, ok)
	}

	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 {
		t.Fatalf("expected 1 snapshot entry, got %d", len(snap.Windows))
	}
	if snap.Windows[0].Session != "main" {
		t.Errorf("expected tmux entry after upgrade, got session %q", snap.Windows[0].Session)
	}
}

func TestDaemon_MixedPanedAndPanelessSnapshot(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess2"})

	snap := d.buildSnapshot()
	if len(snap.Windows) != 2 {
		t.Fatalf("expected 2 snapshot entries, got %d", len(snap.Windows))
	}
	// tmux entries come first, paneless after.
	if snap.Windows[0].Session != "main" {
		t.Errorf("expected tmux entry first, got session %q", snap.Windows[0].Session)
	}
	// Paneless entry has no pane title, so no task-name/window-name source.
	if snap.Windows[1].Session != "" || snap.Windows[1].WindowName != "" {
		t.Errorf("expected paneless entry second with empty name, got %+v", snap.Windows[1])
	}
	if snap.Summary.Total != 2 || snap.Summary.Running != 2 {
		t.Errorf("expected summary total=2 running=2, got %+v", snap.Summary)
	}
}

func TestDaemon_TTLSweepExpiresIdlePaneless(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return base }

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	// Advance past the TTL (default 2h).
	d.now = func() time.Time { return base.Add(2*time.Hour + time.Minute) }
	if changed := d.ttlSweep(); !changed {
		t.Error("expected ttlSweep to report a change")
	}

	if len(d.sessions) != 0 {
		t.Errorf("expected session expired after TTL, got %d sessions", len(d.sessions))
	}
	if len(d.buildSnapshot().Windows) != 0 {
		t.Error("expected empty snapshot after TTL expiry")
	}
}

func TestDaemon_TTLRefreshedByEvents(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return base }

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	// Event at +1h refreshes the deadline.
	d.now = func() time.Time { return base.Add(time.Hour) }
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1"})

	// +2h30m since start, but only +1h30m since last event → still alive.
	d.now = func() time.Time { return base.Add(2*time.Hour + 30*time.Minute) }
	if changed := d.ttlSweep(); changed {
		t.Error("expected no change: last event within TTL")
	}
	if len(d.sessions) != 1 {
		t.Errorf("expected session still tracked, got %d sessions", len(d.sessions))
	}
}

func TestDaemon_TTLSweepIgnoresPaneBackedSessions(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}
	d := newTestDaemon(mc)
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return base }

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	d.now = func() time.Time { return base.Add(24 * time.Hour) }
	if changed := d.ttlSweep(); changed {
		t.Error("expected no TTL change for pane-backed session")
	}
	if len(d.sessions) != 1 {
		t.Errorf("expected pane-backed session untouched by TTL, got %d sessions", len(d.sessions))
	}
}
