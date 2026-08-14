package daemon

import (
	"errors"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux/tmuxtest"
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-style"); !ok || v != "dim" {
		t.Errorf("expected @cenci-style=dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "~" {
		t.Errorf("expected @cenci-symbol=~, got %q (found=%v)", v, ok)
	}
}

// TestDaemon_StripsHalfCircleMarkerFromWindowName pins the rename side of
// #1039: Claude Code heads its pane title with a half-circle working marker
// (◐◑◒◓), which must not survive into the window name — the window would
// otherwise render as "▶ ◑ writing tests", doubling the status indicator.
func TestDaemon_StripsHalfCircleMarkerFromWindowName(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "◑ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests', got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "▶" {
		t.Errorf("expected @cenci-symbol=▶, got %q (found=%v)", v, ok)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "▶" {
		t.Errorf("expected @cenci-symbol=▶, got %q (found=%v)", v, ok)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "!" {
		t.Errorf("expected @cenci-symbol=!, got %q (found=%v)", v, ok)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "!" {
		t.Errorf("expected @cenci-symbol=!, got %q (found=%v)", v, ok)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "!" {
		t.Errorf("expected @cenci-symbol=!, got %q (found=%v)", v, ok)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "✓" {
		t.Errorf("expected @cenci-symbol=✓, got %q (found=%v)", v, ok)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @cenci-symbol=⏹, got %q (found=%v)", v, ok)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "✓" {
		t.Errorf("expected @cenci-symbol=✓, got %q (found=%v)", v, ok)
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

	// Should clear @cenci-style and @cenci-symbol.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-style"); !ok || v != "" {
		t.Errorf("expected @cenci-style cleared, got %q", v)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "" {
		t.Errorf("expected @cenci-symbol cleared, got %q", v)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "!" {
		t.Errorf("expected @cenci-symbol=!, got %q (found=%v)", v, ok)
	}

	// Next tool call should transition back to running.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Read"})

	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests' after next tool, got %q (found=%v)", name, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "▶" {
		t.Errorf("expected @cenci-symbol=▶ after next tool, got %q (found=%v)", v, ok)
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
		if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != tc.wantSymbol {
			t.Errorf("after %s: expected @cenci-symbol=%q, got %q", tc.event.EventType, tc.wantSymbol, v)
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
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected @cenci-style=fg=blue,dim despite rename failure, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "▶" {
		t.Errorf("expected @cenci-symbol=▶ despite rename failure, got %q (found=%v)", v, ok)
	}
}

// TestDaemon_SubagentStopDoesNotFlipMainStatus covers ticket #277: when the
// main agent delegates to a subagent via the Task tool and waits, the
// subagent's own Stop event (same session_id, non-empty AgentID) must not
// flip the main session to done. Only a subsequent main-agent Stop (empty
// AgentID) should do that.
func TestDaemon_SubagentStopDoesNotFlipMainStatus(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Task"})

	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning after PreToolUse(Task), got %v", got)
	}

	// Subagent's own Stop event: same session, but scoped to the subagent.
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0", AgentID: "sub1"})

	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Errorf("expected StatusRunning after subagent Stop (AgentID set), got %v — main session incorrectly flipped to done mid-delegation", got)
	}

	// Main agent's own Stop event (no AgentID) must still flip to done.
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})

	if got := d.sessions["sess1"].Status; got != detect.StatusDone {
		t.Errorf("expected StatusDone after main-agent Stop, got %v", got)
	}
}

// TestDaemon_SubagentNotificationAgentCompletedDoesNotFlipMainStatus covers
// the Notification:agent_completed path for the same guard: a subagent-scoped
// completion notification must not flip the main session to done.
func TestDaemon_SubagentNotificationAgentCompletedDoesNotFlipMainStatus(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Task"})

	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning after PreToolUse(Task), got %v", got)
	}

	// Subagent-scoped completion notification: same session, AgentID set.
	d.handleEvent(ipc.HookEvent{
		EventType:        "Notification",
		SessionID:        "sess1",
		TmuxPane:         "%0",
		NotificationType: "agent_completed",
		AgentID:          "sub1",
	})

	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Errorf("expected StatusRunning after subagent Notification:agent_completed (AgentID set), got %v — main session incorrectly flipped to done mid-delegation", got)
	}
}

// TestDaemon_SubagentRunningEventDoesNotClearDone covers ticket #656: each
// hook fires an independent `cenci notify` process racing to the daemon's
// socket, so a subagent's PreToolUse/PostToolUse can be delivered after the
// main agent's Stop already set done. Those late subagent events must not
// flip the session back to running — the subagent's own Stop is suppressed
// (#277), so nothing would ever restore done.
func TestDaemon_SubagentRunningEventDoesNotClearDone(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Task"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})

	if got := d.sessions["sess1"].Status; got != detect.StatusDone {
		t.Fatalf("precondition: expected StatusDone after main-agent Stop, got %v", got)
	}

	// Late subagent events delivered after the main agent's Stop.
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Read", AgentID: "sub1"})
	if got := d.sessions["sess1"].Status; got != detect.StatusDone {
		t.Errorf("expected StatusDone after late subagent PostToolUse, got %v — subagent event cleared the main session's done status", got)
	}
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Grep", AgentID: "sub1"})
	if got := d.sessions["sess1"].Status; got != detect.StatusDone {
		t.Errorf("expected StatusDone after late subagent PreToolUse, got %v — subagent event cleared the main session's done status", got)
	}

	// A main-agent event (empty AgentID) must still flip back to running:
	// it means the agent genuinely resumed (new prompt or re-invocation).
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Errorf("expected StatusRunning after main-agent UserPromptSubmit, got %v", got)
	}
}

// TestDaemon_SubagentRunningEventDoesNotClearStopped covers the stopped
// (interrupt/StopFailure) terminal state for the same #656 guard: a late
// subagent running event must not clear it either.
func TestDaemon_SubagentRunningEventDoesNotClearStopped(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Task"})
	d.handleEvent(ipc.HookEvent{EventType: "StopFailure", SessionID: "sess1", TmuxPane: "%0"})

	if got := d.sessions["sess1"].Status; got != detect.StatusStopped {
		t.Fatalf("precondition: expected StatusStopped after main-agent StopFailure, got %v", got)
	}

	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Read", AgentID: "sub1"})
	if got := d.sessions["sess1"].Status; got != detect.StatusStopped {
		t.Errorf("expected StatusStopped after late subagent PostToolUse, got %v — subagent event cleared the main session's stopped status", got)
	}
}

// TestDaemon_SubagentSessionEndDoesNotEndMainSession covers ticket #277: a
// subagent's own SessionEnd (same session_id, non-empty AgentID) must not
// delete the main session or restore its window — only the main agent's own
// SessionEnd (empty AgentID) does that.
func TestDaemon_SubagentSessionEndDoesNotEndMainSession(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Task"})

	mc.Renames = nil
	mc.WindowOpts = nil

	// Subagent's own SessionEnd: same session, but scoped to the subagent.
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0", AgentID: "sub1"})

	if len(mc.Renames) != 0 {
		t.Errorf("expected no restore rename from subagent SessionEnd, got %d", len(mc.Renames))
	}
	if d.sessions["sess1"] == nil {
		t.Fatal("expected main session state to still exist after subagent SessionEnd")
	}

	// Main agent's own SessionEnd (no AgentID) must still end the session.
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if len(mc.Renames) != 1 {
		t.Fatalf("expected 1 restore rename after main-agent SessionEnd, got %d", len(mc.Renames))
	}
	if d.sessions["sess1"] != nil {
		t.Error("expected main session state removed after main-agent SessionEnd")
	}
}

// TestDaemon_StalePostToolUseDoesNotClearNeedInput covers ticket #544: a
// prior tool call's PostToolUse can be delivered after a subsequent
// PreToolUse(AskUserQuestion) has already set NeedInput — each `cenci
// notify` invocation is an independent process racing to the daemon's
// socket. That stale, mismatched PostToolUse must not clobber the pending
// question; only the matching PostToolUse (or a genuinely new PreToolUse,
// covered by TestDaemon_AskUserQuestionSetsNeedInput) clears it.
func TestDaemon_StalePostToolUseDoesNotClearNeedInput(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	// A prior tool call (Bash) starts, still in flight when AskUserQuestion
	// fires — its own PostToolUse hasn't been delivered yet.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "AskUserQuestion"})

	if got := d.sessions["sess1"].Status; got != detect.StatusNeedInput {
		t.Fatalf("precondition: expected StatusNeedInput after PreToolUse(AskUserQuestion), got %v", got)
	}

	// Bash's PostToolUse arrives late — must not clear the pending question.
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})

	if got := d.sessions["sess1"].Status; got != detect.StatusNeedInput {
		t.Errorf("expected StatusNeedInput to survive a stale PostToolUse(Bash), got %v", got)
	}

	// The matching PostToolUse for the actual pending question clears it.
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "AskUserQuestion"})

	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Errorf("expected StatusRunning after PostToolUse(AskUserQuestion), got %v", got)
	}
}

// TestDaemon_SubagentEventDoesNotClearMainNeedInput covers ticket #544: a
// backgrounded subagent (Task tool delegation) fires its own PreToolUse and
// PostToolUse on the same session_id as the main agent. The existing
// suppression in mapEventToStatus only protects Done/Stopped from a
// subagent's terminal events (#277) — it must also stop a subagent's
// Running-mapping events from clobbering a main session that is waiting on
// user input.
func TestDaemon_SubagentEventDoesNotClearMainNeedInput(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "AskUserQuestion"})

	if got := d.sessions["sess1"].Status; got != detect.StatusNeedInput {
		t.Fatalf("precondition: expected StatusNeedInput after PreToolUse(AskUserQuestion), got %v", got)
	}

	// A backgrounded subagent is still working and fires its own
	// PreToolUse/PostToolUse on the same session_id.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Read", AgentID: "sub1"})
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Read", AgentID: "sub1"})

	if got := d.sessions["sess1"].Status; got != detect.StatusNeedInput {
		t.Errorf("expected StatusNeedInput to survive subagent PreToolUse/PostToolUse, got %v", got)
	}

	// The main agent's own tool activity still clears it normally.
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Read"})

	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Errorf("expected StatusRunning after main-agent PreToolUse, got %v", got)
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

// TestDaemon_StopWithInFlightBackgroundWorkKeepsSessionRunning covers ticket
// #698: when the main agent backgrounds work (a background subagent/fork, a
// Workflow, a run_in_background Bash command, a Monitor), its turn ends and
// Claude Code fires a normal main-agent Stop — but the session is paused
// waiting to be woken, not done. The Stop carries a non-empty in-flight
// background_tasks array, which must keep the session running.
func TestDaemon_StopWithInFlightBackgroundWorkKeepsSessionRunning(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Agent"})

	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning after PreToolUse(Agent), got %v", got)
	}

	// Main-agent Stop while a backgrounded subagent is still in flight.
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0", BackgroundWork: true})

	if got := d.sessions["sess1"].Status; got != detect.StatusRunning {
		t.Errorf("expected StatusRunning after Stop with in-flight background work, got %v — session reported done while a background subagent is still running", got)
	}
}

// TestDaemon_StopWithoutBackgroundWorkStillMarksDone pins the ordinary turn:
// a Stop with an empty/absent background_tasks array must still map to done
// (no regression from the #698 in-flight override).
func TestDaemon_StopWithoutBackgroundWorkStillMarksDone(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ working", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})

	if got := d.sessions["sess1"].Status; got != detect.StatusDone {
		t.Errorf("expected StatusDone after Stop with no background work, got %v", got)
	}
}

// TestDaemon_SubagentStopWithBackgroundWorkStaysSuppressed keeps the #277
// guard narrow: the #698 override applies only to main-agent Stop events. A
// subagent-scoped Stop (OpenCode's plugin synthesizes exactly this shape) must
// remain a no-op even if it reports background work, rather than resurrecting
// the main session.
func TestDaemon_SubagentStopWithBackgroundWorkStaysSuppressed(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ waiting", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "AskUserQuestion"})

	if got := d.sessions["sess1"].Status; got != detect.StatusNeedInput {
		t.Fatalf("precondition: expected StatusNeedInput, got %v", got)
	}

	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0", AgentID: "sub1", BackgroundWork: true})

	if got := d.sessions["sess1"].Status; got != detect.StatusNeedInput {
		t.Errorf("expected StatusNeedInput to survive a subagent Stop reporting background work, got %v", got)
	}
}
