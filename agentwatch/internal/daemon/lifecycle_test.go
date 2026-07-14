package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/detect"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/tmux/tmuxtest"
)

func TestDaemon_FullLifecycle(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	// SessionStart → idle (task name from "✳ Claude Code" → "Claude Code")
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "Claude Code" {
		t.Errorf("after SessionStart: expected 'Claude Code', got %q", name)
	}

	// UserPromptSubmit → running
	mc.Panes[0].PaneTitle = "⠋ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing tests" {
		t.Errorf("after UserPromptSubmit: expected 'writing tests', got %q", name)
	}

	// Notification(permission_prompt) → need-input
	d.handleEvent(ipc.HookEvent{EventType: "Notification", SessionID: "sess1", TmuxPane: "%0", NotificationType: "permission_prompt"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing tests" {
		t.Errorf("after Notification: expected 'writing tests', got %q", name)
	}

	// PreToolUse → back to running
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing tests" {
		t.Errorf("after PreToolUse: expected 'writing tests', got %q", name)
	}

	// Stop → done
	mc.Panes[0].PaneTitle = "✳ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing tests" {
		t.Errorf("after Stop: expected 'writing tests', got %q", name)
	}

	// SessionEnd → restored
	mc.Renames = nil
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	if len(mc.Renames) != 1 || mc.Renames[0].Name != "bash" {
		t.Errorf("after SessionEnd: expected restore to 'bash', got %v", mc.Renames)
	}
}

func TestDaemon_FullLifecycleWithPermission(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	// UserPromptSubmit → running
	mc.Panes[0].PaneTitle = "⠋ writing files"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing files" {
		t.Errorf("after UserPromptSubmit: expected 'writing files', got %q", name)
	}

	// PreToolUse(Bash) → still running (no-op, same status)
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})

	// PermissionRequest → need-input
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing files" {
		t.Errorf("after PermissionRequest: expected 'writing files', got %q", name)
	}

	// PostToolUse → back to running (user approved, tool completed)
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing files" {
		t.Errorf("after PostToolUse: expected 'writing files', got %q", name)
	}

	// Stop → done
	mc.Panes[0].PaneTitle = "✳ writing files"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "writing files" {
		t.Errorf("after Stop: expected 'writing files', got %q", name)
	}
}

func TestDaemon_CodexLifecycleWithoutSessionEndRestoresAfterExit(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "codex", PaneTitle: "agent-stack", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "agent-stack" {
		t.Errorf("after SessionStart: expected native pane title 'agent-stack', got %q", name)
	}

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "agent-stack" {
		t.Errorf("after UserPromptSubmit: expected native pane title, got %q", name)
	}
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "!" {
		t.Errorf("expected @agentwatch-symbol=! after PermissionRequest, got %q (found=%v)", v, ok)
	}
	if name, _ := lastRename(mc.Renames, "main:0"); name != "agent-stack" {
		t.Errorf("after PermissionRequest: expected retained native pane title, got %q", name)
	}

	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "✓" {
		t.Errorf("expected @agentwatch-symbol=✓ after Stop, got %q (found=%v)", v, ok)
	}
	if name, _ := lastRename(mc.Renames, "main:0"); name != "agent-stack" {
		t.Errorf("after Stop: expected retained native pane title, got %q", name)
	}

	// Codex does not currently document a SessionEnd hook. When the process
	// exits back to the user's shell, sweep should restore the original window.
	mc.Panes[0].PaneCurrentCmd = "zsh"
	mc.Renames = nil
	mc.WindowOpts = nil

	d.runSweep()

	if len(mc.Renames) != 1 || mc.Renames[0].Name != "zsh" {
		t.Fatalf("expected restore to 'zsh' after Codex exit, got %v", mc.Renames)
	}
	if len(d.sessions) != 0 {
		t.Errorf("expected sessions map empty after Codex exit cleanup, got %d entries", len(d.sessions))
	}
	if d.frontend.WindowInfo("codex-sess") != nil {
		t.Error("expected window no longer tracked after Codex exit cleanup")
	}
}

func TestDaemon_CodexPromptLabelAndNativeQuestionReconciliation(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{{
		SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
		PaneCurrentCmd: "codex", PaneTitle: "agent-stack", PaneID: "%0",
	}}}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "agent-stack" {
		t.Fatalf("folder fallback = %q, want agent-stack", name)
	}

	// An empty prompt does not pin the folder fallback. The first later
	// non-empty prompt does, even though the status is already running.
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0", TaskName: "improve codex tmux names"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "improve codex tmux names" {
		t.Fatalf("first prompt label = %q", name)
	}
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0", TaskName: "replace this label"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "improve codex tmux names" {
		t.Fatalf("later prompt replaced pinned label: %q", name)
	}

	mc.Panes[0].PaneTitle = "[ ! ] Action Required | agent-stack"
	d.runSweep()
	if got := d.sessions["codex-sess"].Status; got != detect.StatusNeedInput {
		t.Fatalf("action title status = %v, want need-input", got)
	}
	if got := d.sessions["codex-sess"].TaskName; got != "improve codex tmux names" {
		t.Fatalf("action title changed task = %q", got)
	}
	if symbol, _ := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); symbol != "!" {
		t.Fatalf("action title symbol = %q, want !", symbol)
	}

	mc.Panes[0].PaneTitle = "⠋ Working | agent-stack"
	d.runSweep()
	if got := d.sessions["codex-sess"].Status; got != detect.StatusRunning {
		t.Fatalf("spinner status = %v, want running", got)
	}
	if got := d.sessions["codex-sess"].TaskName; got != "improve codex tmux names" {
		t.Fatalf("spinner changed task = %q", got)
	}
}

func TestDaemon_CodexActionTitleFallsBackAfterRestart(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{{
		SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
		PaneCurrentCmd: "codex", PaneTitle: "[ ! ] Action Required | agent-stack", PaneID: "%0",
	}}}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.runSweep()
	if got := d.sessions["codex-sess"].TaskName; got != "agent-stack" {
		t.Fatalf("restart fallback = %q, want agent-stack", got)
	}
	if got := d.sessions["codex-sess"].Status; got != detect.StatusNeedInput {
		t.Fatalf("restart action status = %v, want need-input", got)
	}
}

func TestDaemon_CodexKeepsDispatchedWindowName(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{{
			SessionName: "main", WindowIndex: "0", WindowName: "146-ci-shell-lint", PaneIndex: "0",
			PaneCurrentCmd: "codex", PaneTitle: "agent-stack", PaneID: "%0",
		}},
		WindowOptValues: map[string]string{"main:0:automatic-rename": "off"},
	}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0", TaskName: "improve codex tmux names"})
	mc.Panes[0].PaneTitle = "[ ! ] Action Required | agent-stack"
	d.runSweep()
	if name, _ := lastRename(mc.Renames, "main:0"); name != "146-ci-shell-lint" {
		t.Fatalf("dispatched window renamed to %q", name)
	}
}

func TestDaemon_DaemonRestartRediscoversSession(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	if wi := d.frontend.WindowInfo("sess1"); wi == nil {
		t.Fatal("expected window tracked after first event")
	}
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests', got %q (found=%v)", name, ok)
	}
}

func TestDaemon_LoopExitsOnCancel(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{},
	}

	d := newTestDaemon(mc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.loop(ctx, nil)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not exit after cancel")
	}
}

func TestDaemon_Cleanup(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "dev", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task1", PaneID: "%0"},
			{SessionName: "main", WindowIndex: "1", WindowName: "test", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠙ task2", PaneID: "%1"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess2", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess2", TmuxPane: "%1"})

	mc.Renames = nil
	mc.WindowOpts = nil
	d.cleanup()

	if len(mc.Renames) != 2 {
		t.Fatalf("expected 2 restore renames on cleanup, got %d", len(mc.Renames))
	}

	names := map[string]bool{}
	for _, r := range mc.Renames {
		names[r.Name] = true
	}
	if !names["dev"] || !names["test"] {
		t.Errorf("expected restore to 'dev' and 'test', got %v", mc.Renames)
	}

	if d.frontend.WindowInfo("sess1") != nil || d.frontend.WindowInfo("sess2") != nil {
		t.Error("expected no windows tracked after cleanup")
	}
}

func TestDaemon_SweepStaleRestoresWindow(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Simulate pane disappearing (Claude crash).
	mc.Panes = []tmux.PaneInfo{}
	mc.Renames = nil
	mc.WindowOpts = nil

	d.runSweep()

	if len(mc.Renames) < 1 {
		t.Fatalf("expected at least 1 restore rename from sweep, got %d", len(mc.Renames))
	}
	if mc.Renames[0].Name != "bash" {
		t.Errorf("expected restore to 'bash', got %q", mc.Renames[0].Name)
	}
	if len(d.sessions) != 0 {
		t.Errorf("expected sessions map empty after sweep, got %d", len(d.sessions))
	}
	if d.frontend.WindowInfo("sess1") != nil {
		t.Error("expected window no longer tracked after sweep")
	}
}

func TestDaemon_SweepStaleSkipsReusedWindowTarget(t *testing.T) {
	// When a tracked pane is gone but the window target is now occupied by a
	// different pane, sweep should discard state without restoring (the old
	// window is gone — nothing to restore to).
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%5"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%5"})

	// Simulate: old pane %5 gone, new pane %8 at same window target.
	mc.Panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "1", WindowName: "fish", PaneIndex: "0",
			PaneCurrentCmd: "bash", PaneTitle: "", PaneID: "%8"},
	}

	mc.Renames = nil
	mc.WindowOpts = nil
	d.runSweep()

	// Should NOT have renamed the window (old window is gone, new window shouldn't be touched).
	for _, r := range mc.Renames {
		if r.Target == "main:1" {
			t.Errorf("expected no rename for reused window target, got rename to %q", r.Name)
		}
	}

	// State should be cleaned up.
	if d.frontend.WindowInfo("sess1") != nil {
		t.Error("expected window state removed after sweep")
	}
	if len(d.sessions) != 0 {
		t.Errorf("expected session removed after sweep, got %d", len(d.sessions))
	}
}

func TestDaemon_SweepStaleWithRenumbering(t *testing.T) {
	// Setup: 3 windows, all tracked.
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-zero", PaneID: "%0"},
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-one", PaneID: "%1"},
			{SessionName: "main", WindowIndex: "2", WindowName: "fish", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-two", PaneID: "%2"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s0", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s0", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s1", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s2", TmuxPane: "%2"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s2", TmuxPane: "%2"})

	// Simulate: window 1 killed, renumber-windows on → window 2 becomes window 1.
	mc.Panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "0", WindowName: "task-zero", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-zero", PaneID: "%0"},
		{SessionName: "main", WindowIndex: "1", WindowName: "task-two", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-two", PaneID: "%2"},
	}
	mc.Renames = nil
	mc.WindowOpts = nil

	d.runSweep()

	// %2's state should be migrated from main:2 to main:1.
	wi := d.frontend.WindowInfo("s2")
	if wi == nil {
		t.Fatal("expected s2's window still tracked after migration")
	}
	if wi.Session != "main" || wi.WindowIndex != "1" {
		t.Errorf("expected s2's state migrated to main:1, got %s:%s", wi.Session, wi.WindowIndex)
	}

	// Dead pane %1's original name should NOT be restored to the window now at main:1.
	for _, r := range mc.Renames {
		if r.Target == "main:1" && r.Name == "zsh" {
			t.Error("dead pane's original name 'zsh' should not be restored to main:1 (now occupied by %2)")
		}
	}
	// s1's session should be gone.
	if _, ok := d.sessions["s1"]; ok {
		t.Error("expected s1 removed after its pane disappeared")
	}

	// main:0 should still be tracked (untouched).
	if wi := d.frontend.WindowInfo("s0"); wi == nil || wi.WindowIndex != "0" {
		t.Error("expected s0 still tracked at main:0")
	}
}

func TestDaemon_PaneMismatchDiscardsStaleState(t *testing.T) {
	// Window main:1 tracked with pane %5, then window killed and new window
	// created at same index with pane %8. Event for %8 should discard old state
	// and track fresh.
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%5"},
		},
	}

	d := newTestDaemon(mc)

	// Track original window at main:1 with pane %5.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%5"})

	if wi := d.frontend.WindowInfo("sess1"); wi == nil || wi.WindowIndex != "1" {
		t.Fatalf("precondition: expected sess1 tracked at main:1, got %+v", wi)
	}

	// Simulate: old window killed, new window at index 1 with pane %8.
	mc.Panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "1", WindowName: "fish", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%8"},
	}

	mc.Renames = nil
	mc.WindowOpts = nil

	// New event arrives for %8 → resolves to main:1 → should detect mismatch.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess2", TmuxPane: "%8"})

	// Old state should be discarded (sess1 evicted), new state tracked.
	if wi := d.frontend.WindowInfo("sess2"); wi == nil || wi.WindowIndex != "1" {
		t.Fatalf("expected sess2 tracked at main:1 after mismatch reset, got %+v", wi)
	}
	if d.frontend.WindowInfo("sess1") != nil {
		t.Error("expected sess1's window state discarded")
	}
	if _, ok := d.sessions["sess1"]; ok {
		t.Error("expected stale session sess1 evicted from core state")
	}
}

func TestDaemon_PaneMismatchRestoresCorrectNameOnEnd(t *testing.T) {
	// After a pane mismatch reset, SessionEnd should restore the NEW window's
	// original name, not the old one.
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%5"},
		},
	}

	d := newTestDaemon(mc)

	// Track original window.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5"})

	// Window killed, new window at same index.
	mc.Panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "1", WindowName: "fish", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ writing code", PaneID: "%8"},
	}

	// New session starts at same window (mismatch detected, fresh track).
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess2", TmuxPane: "%8"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess2", TmuxPane: "%8"})

	mc.Renames = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess2", TmuxPane: "%8"})

	// Should restore to "fish" (new window's name), not "zsh" (old window's name).
	if len(mc.Renames) < 1 {
		t.Fatal("expected at least 1 restore rename")
	}
	if name, ok := lastRename(mc.Renames, "main:1"); !ok || name != "fish" {
		t.Errorf("expected restore to 'fish', got %q (found=%v)", name, ok)
	}
}

func TestDaemon_SessionEndAfterRenumberDoesNotRestoreWrongWindow(t *testing.T) {
	// Two windows tracked. Window 0 is killed, renumber-windows slides
	// window 1 into index 0. SessionEnd for dead pane %0 must NOT restore
	// its original name/styles onto the surviving window now at main:0.
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-zero", PaneID: "%0"},
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-one", PaneID: "%1"},
		},
	}

	d := newTestDaemon(mc)

	// Track both windows.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s0", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s0", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s1", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", TmuxPane: "%1"})

	// Simulate: window 0 killed + renumber-windows on.
	// Pane %0 is gone, pane %1 moved from main:1 to main:0.
	mc.Panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "0", WindowName: "task-one", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-one", PaneID: "%1"},
	}
	mc.Renames = nil
	mc.WindowOpts = nil

	// SessionEnd for the dead pane %0 — cached target is main:0, which is
	// now occupied by %1 after renumbering.
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "s0", TmuxPane: "%0"})

	// Must NOT rename or change options on main:0 (that's %1's window now).
	for _, r := range mc.Renames {
		if r.Target == "main:0" {
			t.Errorf("SessionEnd for dead pane must not rename main:0, got rename to %q", r.Name)
		}
	}
	for _, opt := range mc.WindowOpts {
		if opt.Target == "main:0" {
			t.Errorf("SessionEnd for dead pane must not set option on main:0: %s=%s", opt.Key, opt.Value)
		}
	}

	// The ended session should be gone from core state.
	if _, ok := d.sessions["s0"]; ok {
		t.Error("expected ended session s0 removed")
	}

	// %1's state should still exist (at main:1 — will be migrated by next event/sweep).
	if wi := d.frontend.WindowInfo("s1"); wi == nil || wi.WindowIndex != "1" {
		t.Error("expected s1's state still at main:1")
	}
}

func TestDaemon_WindowRenumberingMigratesState(t *testing.T) {
	// Setup: 3 windows, all tracked.
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-zero", PaneID: "%0"},
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-one", PaneID: "%1"},
			{SessionName: "main", WindowIndex: "2", WindowName: "fish", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task-two", PaneID: "%2"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s0", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s0", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s1", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s1", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "s2", TmuxPane: "%2"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "s2", TmuxPane: "%2"})

	if wi := d.frontend.WindowInfo("s2"); wi == nil || wi.WindowIndex != "2" {
		t.Fatalf("precondition: expected s2 tracked at main:2, got %+v", wi)
	}

	// Simulate: window 1 killed, renumber-windows on → window 2 becomes window 1.
	mc.Panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "0", WindowName: "task-zero", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-zero", PaneID: "%0"},
		{SessionName: "main", WindowIndex: "1", WindowName: "task-two", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-two", PaneID: "%2"},
	}
	mc.Renames = nil
	mc.WindowOpts = nil

	// Event arrives for pane %2 (now at window 1, not 2).
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "s2", TmuxPane: "%2"})

	// State should be migrated from main:2 to main:1.
	wi := d.frontend.WindowInfo("s2")
	if wi == nil {
		t.Fatal("expected s2's window still tracked after migration")
	}
	if wi.Session != "main" || wi.WindowIndex != "1" {
		t.Errorf("expected s2's state migrated to main:1, got %s:%s", wi.Session, wi.WindowIndex)
	}

	// Styles should be applied to the new target main:1.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:1", "window-status-style"); !ok || v != "fg=green,dim" {
		t.Errorf("expected window-status-style=fg=green,dim on main:1, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:1", "@agentwatch-symbol"); !ok || v != "✓" {
		t.Errorf("expected @agentwatch-symbol=✓ on main:1, got %q (found=%v)", v, ok)
	}

	// Nothing should target stale main:2.
	for _, opt := range mc.WindowOpts {
		if opt.Target == "main:2" {
			t.Errorf("unexpected SetWindowOption targeting stale main:2: %s=%s", opt.Key, opt.Value)
		}
	}
	for _, r := range mc.Renames {
		if r.Target == "main:2" {
			t.Errorf("unexpected RenameWindow targeting stale main:2: %s", r.Name)
		}
	}
}
