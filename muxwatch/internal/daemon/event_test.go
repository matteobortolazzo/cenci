package daemon

import (
	"errors"
	"testing"

	"github.com/matteobortolazzo/claude-tools/muxwatch/internal/ipc"
	"github.com/matteobortolazzo/claude-tools/muxwatch/internal/tmux"
)

func TestDaemon_SessionStartTracksWindow(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{
		EventType: "SessionStart",
		SessionID: "sess1",
		TmuxPane:  "%0",
	})

	// Should rename to task name (no symbol prefix). Task name from "✳ Claude Code" → "Claude Code".
	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "Claude Code" {
		t.Errorf("expected rename to 'Claude Code', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "dim" {
		t.Errorf("expected window-status-style=dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-current-style"); !ok || v != "dim" {
		t.Errorf("expected window-status-current-style=dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-style"); !ok || v != "dim" {
		t.Errorf("expected @muxwatch-style=dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "~" {
		t.Errorf("expected @muxwatch-symbol=~, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_UserPromptSubmitSetsRunning(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-style=fg=blue,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "▶" {
		t.Errorf("expected @muxwatch-symbol=▶, got %q (found=%v)", v, ok)
	}
}

func TestCompactTaskNameCapsWindowLabel(t *testing.T) {
	got := compactTaskName("abcdefghijklmnopqrstuvwxyz0123456789")
	want := "abcdefghijklmnopqrstuvwxyz0..."
	if got != want {
		t.Errorf("compactTaskName() = %q, want %q", got, want)
	}
}

func TestDaemon_NotificationPermissionSetsNeedInput(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ writing files", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{
		EventType:        "Notification",
		SessionID:        "sess1",
		TmuxPane:         "%0",
		NotificationType: "permission_prompt",
	})

	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing files" {
		t.Errorf("expected rename to 'writing files', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "fg=red,dim" {
		t.Errorf("expected window-status-style=fg=red,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "!" {
		t.Errorf("expected @muxwatch-symbol=!, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PreToolUseClearsNeedInput(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing files", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Notification", SessionID: "sess1", TmuxPane: "%0", NotificationType: "permission_prompt"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})

	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing files" {
		t.Errorf("expected rename to 'writing files' after permission grant, got %q (found=%v)", name, ok)
	}
}

func TestDaemon_StopSetsDone(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "fg=green,dim" {
		t.Errorf("expected window-status-style=fg=green,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "✓" {
		t.Errorf("expected @muxwatch-symbol=✓, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_SessionEndRestoresWindow(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	mc.renames = nil
	mc.windowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(mc.renames) != 1 {
		t.Fatalf("expected 1 restore rename, got %d", len(mc.renames))
	}
	if mc.renames[0].name != "bash" {
		t.Errorf("expected restore to 'bash', got %q", mc.renames[0].name)
	}

	// Should clear @muxwatch-style and @muxwatch-symbol.
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-style"); !ok || v != "" {
		t.Errorf("expected @muxwatch-style cleared, got %q", v)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "" {
		t.Errorf("expected @muxwatch-symbol cleared, got %q", v)
	}

	found := false
	for _, opt := range mc.windowOpts {
		if opt.target == "main:0" && opt.key == "automatic-rename" && opt.value == "on" {
			found = true
		}
	}
	if !found {
		t.Error("expected automatic-rename re-enabled on restore")
	}

	if len(d.windows) != 0 {
		t.Errorf("expected windows map empty after session end, got %d", len(d.windows))
	}
}

func TestDaemon_AskUserQuestionSetsNeedInput(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// AskUserQuestion should transition to need-input.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "AskUserQuestion"})

	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests' after AskUserQuestion, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "fg=red,dim" {
		t.Errorf("expected window-status-style=fg=red,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "!" {
		t.Errorf("expected @muxwatch-symbol=!, got %q (found=%v)", v, ok)
	}

	// Next tool call should transition back to running.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Read"})

	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests' after next tool, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "▶" {
		t.Errorf("expected @muxwatch-symbol=▶ after next tool, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PermissionRequestSetsNeedInput(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing files", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing files" {
		t.Errorf("expected rename to 'writing files' after PermissionRequest, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "fg=red,dim" {
		t.Errorf("expected window-status-style=fg=red,dim, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PostToolUseSetsRunning(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing files", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "sess1", TmuxPane: "%0"})

	// Verify we're in need-input first.
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing files" {
		t.Fatalf("precondition: expected 'writing files' after PermissionRequest, got %q", name)
	}

	// PostToolUse should transition back to running.
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing files" {
		t.Errorf("expected rename to 'writing files' after PostToolUse, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-style=fg=blue,dim, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_NoStatusChangeNoOp(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Send a PreToolUse while already running — returns Running but applyStatus short-circuits.
	optsBeforeCount := len(mc.windowOpts)
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0"})

	if len(mc.windowOpts) != optsBeforeCount {
		t.Errorf("expected no additional window opts for no-op event, got %d more", len(mc.windowOpts)-optsBeforeCount)
	}
}

func TestDaemon_EmptyPaneIDDropsEvent(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{},
	}

	d := newTestDaemon(mc)
	d.cfg.Verbose = true
	d.handleEvent(ipc.HookEvent{
		EventType: "SessionStart",
		SessionID: "sess1",
		TmuxPane:  "",
	})

	if len(mc.renames) != 0 {
		t.Errorf("expected no renames for empty pane ID, got %d", len(mc.renames))
	}
}

func TestDaemon_SymbolVariableAcrossAllStatuses(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ fixing bug", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	tests := []struct {
		event      ipc.HookEvent
		wantSymbol string
	}{
		{ipc.HookEvent{EventType: "SessionStart", SessionID: "s1", TmuxPane: "%0"}, "~"},
		{ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", TmuxPane: "%0"}, "▶"},
		{ipc.HookEvent{EventType: "PermissionRequest", SessionID: "s1", TmuxPane: "%0"}, "!"},
		{ipc.HookEvent{EventType: "PostToolUse", SessionID: "s1", TmuxPane: "%0"}, "▶"},
		{ipc.HookEvent{EventType: "Stop", SessionID: "s1", TmuxPane: "%0"}, "✓"},
	}

	for _, tc := range tests {
		d.handleEvent(tc.event)
		// Name should always be task name without symbol.
		if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "fixing bug" {
			t.Errorf("after %s: expected name 'fixing bug', got %q", tc.event.EventType, name)
		}
		// Symbol should be in the user variable.
		if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != tc.wantSymbol {
			t.Errorf("after %s: expected @muxwatch-symbol=%q, got %q", tc.event.EventType, tc.wantSymbol, v)
		}
	}
}

func TestDaemon_StylesSetEvenWhenRenameFails(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})

	// Enable rename error for the next event.
	mc.renameErr = errors.New("rename failed")
	mc.windowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Styles and symbol should still be set even though rename failed.
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-style=fg=blue,dim despite rename failure, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-current-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-current-style=fg=blue,dim despite rename failure, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected @muxwatch-style=fg=blue,dim despite rename failure, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "▶" {
		t.Errorf("expected @muxwatch-symbol=▶ despite rename failure, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_SessionEndForStalePaneIgnored(t *testing.T) {
	// A late SessionEnd for a dead pane (old window) must not restore the new
	// window that now occupies the same window target.
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%5"},
		},
	}

	d := newTestDaemon(mc)

	// Track original window.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5"})

	// Window killed, new window at same index with new session.
	mc.panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "1", WindowName: "fish", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ writing code", PaneID: "%8"},
	}

	// New session tracked (mismatch detected, fresh track).
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess2", TmuxPane: "%8"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess2", TmuxPane: "%8"})

	mc.renames = nil
	mc.windowOpts = nil

	// Late SessionEnd arrives for old pane %5 (e.g., Claude process finally exits).
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%5"})

	// Should NOT have renamed or restored the window — the new session owns it.
	for _, r := range mc.renames {
		if r.target == "main:1" {
			t.Errorf("expected no rename from stale SessionEnd, got rename to %q", r.name)
		}
	}

	// New session's state should still be intact.
	ws := d.windows["main:1"]
	if ws == nil {
		t.Fatal("expected new session state to still exist")
	}
	if ws.PaneID != "%8" {
		t.Errorf("expected PaneID=%%8, got %q", ws.PaneID)
	}
	if ws.OriginalName != "fish" {
		t.Errorf("expected OriginalName='fish', got %q", ws.OriginalName)
	}
}
