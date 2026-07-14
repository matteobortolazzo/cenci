package daemon

import (
	"errors"
	"testing"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/tmux/tmuxtest"
)

func TestDaemon_SessionStartTracksWindow(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
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
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "Claude Code" {
		t.Errorf("expected rename to 'Claude Code', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "dim" {
		t.Errorf("expected window-status-style=dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-current-style"); !ok || v != "dim" {
		t.Errorf("expected window-status-current-style=dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-style"); !ok || v != "dim" {
		t.Errorf("expected @agentwatch-style=dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "~" {
		t.Errorf("expected @agentwatch-symbol=~, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_UserPromptSubmitSetsRunning(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-style=fg=blue,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "▶" {
		t.Errorf("expected @agentwatch-symbol=▶, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_NotificationPermissionSetsNeedInput(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
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

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing files" {
		t.Errorf("expected rename to 'writing files', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=red,dim" {
		t.Errorf("expected window-status-style=fg=red,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "!" {
		t.Errorf("expected @agentwatch-symbol=!, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_NotificationAgentNeedsInputSetsNeedInput(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
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
		NotificationType: "agent_needs_input",
	})

	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=red,dim" {
		t.Errorf("expected window-status-style=fg=red,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "!" {
		t.Errorf("expected @agentwatch-symbol=!, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_NotificationElicitationDialogSetsNeedInput(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
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
		NotificationType: "elicitation_dialog",
	})

	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=red,dim" {
		t.Errorf("expected window-status-style=fg=red,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "!" {
		t.Errorf("expected @agentwatch-symbol=!, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_NotificationAgentCompletedSetsDone(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{
		EventType:        "Notification",
		SessionID:        "sess1",
		TmuxPane:         "%0",
		NotificationType: "agent_completed",
	})

	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=green,dim" {
		t.Errorf("expected window-status-style=fg=green,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "✓" {
		t.Errorf("expected @agentwatch-symbol=✓, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_StopFailureSetsStopped(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "StopFailure", SessionID: "sess1", TmuxPane: "%0"})

	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=yellow,dim" {
		t.Errorf("expected window-status-style=fg=yellow,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @agentwatch-symbol=⏹, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PreToolUseClearsNeedInput(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing files", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Notification", SessionID: "sess1", TmuxPane: "%0", NotificationType: "permission_prompt"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing files" {
		t.Errorf("expected rename to 'writing files' after permission grant, got %q (found=%v)", name, ok)
	}
}

func TestDaemon_StopSetsDone(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=green,dim" {
		t.Errorf("expected window-status-style=fg=green,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "✓" {
		t.Errorf("expected @agentwatch-symbol=✓, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_SessionEndRestoresWindow(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	mc.Renames = nil
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(mc.Renames) != 1 {
		t.Fatalf("expected 1 restore rename, got %d", len(mc.Renames))
	}
	if mc.Renames[0].Name != "bash" {
		t.Errorf("expected restore to 'bash', got %q", mc.Renames[0].Name)
	}

	// Should clear @agentwatch-style and @agentwatch-symbol.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-style"); !ok || v != "" {
		t.Errorf("expected @agentwatch-style cleared, got %q", v)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "" {
		t.Errorf("expected @agentwatch-symbol cleared, got %q", v)
	}

	found := false
	for _, opt := range mc.WindowOpts {
		if opt.Target == "main:0" && opt.Key == "automatic-rename" && opt.Value == "on" {
			found = true
		}
	}
	if !found {
		t.Error("expected automatic-rename re-enabled on restore")
	}

	if len(d.sessions) != 0 {
		t.Errorf("expected sessions map empty after session end, got %d", len(d.sessions))
	}
}

func TestDaemon_AskUserQuestionSetsNeedInput(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// AskUserQuestion should transition to need-input.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "AskUserQuestion"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests' after AskUserQuestion, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=red,dim" {
		t.Errorf("expected window-status-style=fg=red,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "!" {
		t.Errorf("expected @agentwatch-symbol=!, got %q (found=%v)", v, ok)
	}

	// Next tool call should transition back to running.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Read"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests' after next tool, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "▶" {
		t.Errorf("expected @agentwatch-symbol=▶ after next tool, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PermissionRequestSetsNeedInput(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing files", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing files" {
		t.Errorf("expected rename to 'writing files' after PermissionRequest, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=red,dim" {
		t.Errorf("expected window-status-style=fg=red,dim, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PostToolUseSetsRunning(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing files", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "sess1", TmuxPane: "%0"})

	// Verify we're in need-input first.
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing files" {
		t.Fatalf("precondition: expected 'writing files' after PermissionRequest, got %q", name)
	}

	// PostToolUse should transition back to running.
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing files" {
		t.Errorf("expected rename to 'writing files' after PostToolUse, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-style=fg=blue,dim, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_NoStatusChangeNoOp(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Send a PreToolUse while already running — returns Running but applyStatus short-circuits.
	optsBeforeCount := len(mc.WindowOpts)
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0"})

	if len(mc.WindowOpts) != optsBeforeCount {
		t.Errorf("expected no additional window opts for no-op event, got %d more", len(mc.WindowOpts)-optsBeforeCount)
	}
}

func TestDaemon_SymbolVariableAcrossAllStatuses(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
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
		if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "fixing bug" {
			t.Errorf("after %s: expected name 'fixing bug', got %q", tc.event.EventType, name)
		}
		// Symbol should be in the user variable.
		if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != tc.wantSymbol {
			t.Errorf("after %s: expected @agentwatch-symbol=%q, got %q", tc.event.EventType, tc.wantSymbol, v)
		}
	}
}

func TestDaemon_StylesSetEvenWhenRenameFails(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})

	// Enable rename error for the next event.
	mc.RenameErr = errors.New("rename failed")
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Styles and symbol should still be set even though rename failed.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-style=fg=blue,dim despite rename failure, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-current-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-current-style=fg=blue,dim despite rename failure, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected @agentwatch-style=fg=blue,dim despite rename failure, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "▶" {
		t.Errorf("expected @agentwatch-symbol=▶ despite rename failure, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_SessionEndForStalePaneIgnored(t *testing.T) {
	// A late SessionEnd for a dead pane (old window) must not restore the new
	// window that now occupies the same window target.
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%5"},
		},
	}

	d := newTestDaemon(mc)

	// Track original window.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5"})

	// Window killed, new window at same index with new session.
	mc.Panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "1", WindowName: "fish", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ writing code", PaneID: "%8"},
	}

	// New session tracked (mismatch detected, fresh track).
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess2", TmuxPane: "%8"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess2", TmuxPane: "%8"})

	mc.Renames = nil
	mc.WindowOpts = nil

	// Late SessionEnd arrives for old pane %5 (e.g., Claude process finally exits).
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%5"})

	// Should NOT have renamed or restored the window — the new session owns it.
	for _, r := range mc.Renames {
		if r.Target == "main:1" {
			t.Errorf("expected no rename from stale SessionEnd, got rename to %q", r.Name)
		}
	}

	// New session's state should still be intact.
	if d.sessions["sess2"] == nil {
		t.Fatal("expected new session state to still exist")
	}
	wi := d.frontend.WindowInfo("sess2")
	if wi == nil {
		t.Fatal("expected new session's window still tracked")
		return
	}
	if wi.Session != "main" || wi.WindowIndex != "1" {
		t.Errorf("expected window main:1 for sess2, got %s:%s", wi.Session, wi.WindowIndex)
	}

	// A real SessionEnd for the new session must restore the NEW window's name.
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess2", TmuxPane: "%8"})
	if name, ok := lastRename(mc.Renames, "main:1"); !ok || name != "fish" {
		t.Errorf("expected restore to 'fish', got %q (found=%v)", name, ok)
	}
}
