package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/matteobortolazzo/claude-tools/muxwatch/internal/ipc"
	"github.com/matteobortolazzo/claude-tools/muxwatch/internal/tmux"
)

func TestDaemon_FullLifecycle(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	// SessionStart → idle (task name from "✳ Claude Code" → "Claude Code")
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "Claude Code" {
		t.Errorf("after SessionStart: expected 'Claude Code', got %q", name)
	}

	// UserPromptSubmit → running
	mc.panes[0].PaneTitle = "⠋ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing tests" {
		t.Errorf("after UserPromptSubmit: expected 'writing tests', got %q", name)
	}

	// Notification(permission_prompt) → need-input
	d.handleEvent(ipc.HookEvent{EventType: "Notification", SessionID: "sess1", TmuxPane: "%0", NotificationType: "permission_prompt"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing tests" {
		t.Errorf("after Notification: expected 'writing tests', got %q", name)
	}

	// PreToolUse → back to running
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing tests" {
		t.Errorf("after PreToolUse: expected 'writing tests', got %q", name)
	}

	// Stop → done
	mc.panes[0].PaneTitle = "✳ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing tests" {
		t.Errorf("after Stop: expected 'writing tests', got %q", name)
	}

	// SessionEnd → restored
	mc.renames = nil
	mc.windowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	if len(mc.renames) != 1 || mc.renames[0].name != "bash" {
		t.Errorf("after SessionEnd: expected restore to 'bash', got %v", mc.renames)
	}
}

func TestDaemon_FullLifecycleWithPermission(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	// UserPromptSubmit → running
	mc.panes[0].PaneTitle = "⠋ writing files"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing files" {
		t.Errorf("after UserPromptSubmit: expected 'writing files', got %q", name)
	}

	// PreToolUse(Bash) → still running (no-op, same status)
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "sess1", TmuxPane: "%0", ToolName: "Bash"})

	// PermissionRequest → need-input
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing files" {
		t.Errorf("after PermissionRequest: expected 'writing files', got %q", name)
	}

	// PostToolUse → back to running (user approved, tool completed)
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing files" {
		t.Errorf("after PostToolUse: expected 'writing files', got %q", name)
	}

	// Stop → done
	mc.panes[0].PaneTitle = "✳ writing files"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "writing files" {
		t.Errorf("after Stop: expected 'writing files', got %q", name)
	}
}

func TestDaemon_CodexLifecycleWithoutSessionEndRestoresAfterExit(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "codex", PaneTitle: "Codex", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if name, _ := lastRename(mc.renames, "main:0"); name != "Codex" {
		t.Errorf("after SessionStart: expected 'Codex', got %q", name)
	}

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "!" {
		t.Errorf("expected @muxwatch-symbol=! after PermissionRequest, got %q (found=%v)", v, ok)
	}

	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@muxwatch-symbol"); !ok || v != "✓" {
		t.Errorf("expected @muxwatch-symbol=✓ after Stop, got %q (found=%v)", v, ok)
	}

	// Codex does not currently document a SessionEnd hook. When the process
	// exits back to the user's shell, sweep should restore the original window.
	mc.panes[0].PaneCurrentCmd = "zsh"
	mc.renames = nil
	mc.windowOpts = nil

	d.sweepStale()

	if len(mc.renames) != 1 || mc.renames[0].name != "zsh" {
		t.Fatalf("expected restore to 'zsh' after Codex exit, got %v", mc.renames)
	}
	if len(d.windows) != 0 {
		t.Errorf("expected windows map empty after Codex exit cleanup, got %d entries", len(d.windows))
	}
}

func TestDaemon_DaemonRestartRediscoversSession(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	if len(d.windows) != 1 {
		t.Fatalf("expected 1 tracked window, got %d", len(d.windows))
	}
	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests', got %q (found=%v)", name, ok)
	}
}

func TestDaemon_LoopExitsOnCancel(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{},
	}

	ch := make(chan ipc.HookEvent, 16)
	d := newDaemon(testConfig(), mc, ch)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.loop(ctx)
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
	mc := &mockClient{
		panes: []tmux.PaneInfo{
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

	mc.renames = nil
	mc.windowOpts = nil
	d.cleanup()

	if len(mc.renames) != 2 {
		t.Fatalf("expected 2 restore renames on cleanup, got %d", len(mc.renames))
	}

	names := map[string]bool{}
	for _, r := range mc.renames {
		names[r.name] = true
	}
	if !names["dev"] || !names["test"] {
		t.Errorf("expected restore to 'dev' and 'test', got %v", mc.renames)
	}

	if len(d.windows) != 0 {
		t.Errorf("expected windows map to be empty after cleanup, got %d entries", len(d.windows))
	}
}

func TestDaemon_SweepStaleRestoresWindow(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Simulate pane disappearing (Claude crash).
	mc.panes = []tmux.PaneInfo{}
	mc.renames = nil
	mc.windowOpts = nil

	d.sweepStale()

	if len(mc.renames) < 1 {
		t.Fatalf("expected at least 1 restore rename from sweep, got %d", len(mc.renames))
	}
	if mc.renames[0].name != "bash" {
		t.Errorf("expected restore to 'bash', got %q", mc.renames[0].name)
	}
	if len(d.windows) != 0 {
		t.Errorf("expected windows map empty after sweep, got %d", len(d.windows))
	}
}

func TestDaemon_SweepStaleSkipsReusedWindowTarget(t *testing.T) {
	// When a tracked pane is gone but the window target is now occupied by a
	// different pane, sweep should discard state without restoring (the old
	// window is gone — nothing to restore to).
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%5"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%5"})

	// Simulate: old pane %5 gone, new pane %8 at same window target.
	mc.panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "1", WindowName: "fish", PaneIndex: "0",
			PaneCurrentCmd: "bash", PaneTitle: "", PaneID: "%8"},
	}

	mc.renames = nil
	mc.windowOpts = nil
	d.sweepStale()

	// Should NOT have renamed the window (old window is gone, new window shouldn't be touched).
	for _, r := range mc.renames {
		if r.target == "main:1" {
			t.Errorf("expected no rename for reused window target, got rename to %q", r.name)
		}
	}

	// State should be cleaned up.
	if _, ok := d.windows["main:1"]; ok {
		t.Error("expected window state removed after sweep")
	}
	if _, ok := d.panes["%5"]; ok {
		t.Error("expected old pane removed from cache after sweep")
	}
}

func TestDaemon_SweepStaleWithRenumbering(t *testing.T) {
	// Setup: 3 windows, all tracked.
	mc := &mockClient{
		panes: []tmux.PaneInfo{
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
	mc.panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "0", WindowName: "task-zero", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-zero", PaneID: "%0"},
		{SessionName: "main", WindowIndex: "1", WindowName: "task-two", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-two", PaneID: "%2"},
	}
	mc.renames = nil
	mc.windowOpts = nil

	d.sweepStale()

	// %2's state should be migrated from main:2 to main:1.
	ws := d.windows["main:1"]
	if ws == nil {
		t.Fatal("expected state migrated to main:1")
	}
	if ws.PaneID != "%2" {
		t.Errorf("expected PaneID=%%2 at main:1, got %q", ws.PaneID)
	}
	if _, ok := d.windows["main:2"]; ok {
		t.Error("expected main:2 removed after migration")
	}

	// Dead pane %1's original name should NOT be restored to the window now at main:1.
	for _, r := range mc.renames {
		if r.target == "main:1" && r.name == "zsh" {
			t.Error("dead pane's original name 'zsh' should not be restored to main:1 (now occupied by %2)")
		}
	}

	// main:0 should still be tracked (untouched).
	if ws := d.windows["main:0"]; ws == nil || ws.PaneID != "%0" {
		t.Error("expected main:0 still tracked with %0")
	}
}

func TestDaemon_PaneMismatchDiscardsStaleState(t *testing.T) {
	// Window main:1 tracked with pane %5, then window killed and new window
	// created at same index with pane %8. Event for %8 should discard old state
	// and track fresh.
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%5"},
		},
	}

	d := newTestDaemon(mc)

	// Track original window at main:1 with pane %5.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%5"})

	ws := d.windows["main:1"]
	if ws == nil || ws.PaneID != "%5" {
		t.Fatalf("precondition: expected window tracked with pane %%5, got %v", ws)
	}
	if ws.OriginalName != "zsh" {
		t.Fatalf("precondition: expected OriginalName='zsh', got %q", ws.OriginalName)
	}

	// Simulate: old window killed, new window at index 1 with pane %8.
	mc.panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "1", WindowName: "fish", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%8"},
	}

	mc.renames = nil
	mc.windowOpts = nil

	// New event arrives for %8 → resolves to main:1 → should detect mismatch.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess2", TmuxPane: "%8"})

	// Old state should be discarded, new state tracked with correct OriginalName.
	ws = d.windows["main:1"]
	if ws == nil {
		t.Fatal("expected window main:1 to be tracked after mismatch reset")
	}
	if ws.PaneID != "%8" {
		t.Errorf("expected PaneID=%%8, got %q", ws.PaneID)
	}
	if ws.OriginalName != "fish" {
		t.Errorf("expected OriginalName='fish' (new window), got %q", ws.OriginalName)
	}

	// Old pane should be removed from cache.
	if _, ok := d.panes["%5"]; ok {
		t.Error("expected old pane %5 removed from cache")
	}
}

func TestDaemon_PaneMismatchRestoresCorrectNameOnEnd(t *testing.T) {
	// After a pane mismatch reset, SessionEnd should restore the NEW window's
	// original name, not the old one.
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%5"},
		},
	}

	d := newTestDaemon(mc)

	// Track original window.
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%5"})

	// Window killed, new window at same index.
	mc.panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "1", WindowName: "fish", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ writing code", PaneID: "%8"},
	}

	// New session starts at same window (mismatch detected, fresh track).
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess2", TmuxPane: "%8"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess2", TmuxPane: "%8"})

	mc.renames = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess2", TmuxPane: "%8"})

	// Should restore to "fish" (new window's name), not "zsh" (old window's name).
	if len(mc.renames) < 1 {
		t.Fatal("expected at least 1 restore rename")
	}
	if name, ok := lastRename(mc.renames, "main:1"); !ok || name != "fish" {
		t.Errorf("expected restore to 'fish', got %q (found=%v)", name, ok)
	}
}

func TestDaemon_SessionEndAfterRenumberDoesNotRestoreWrongWindow(t *testing.T) {
	// Two windows tracked. Window 0 is killed, renumber-windows slides
	// window 1 into index 0. SessionEnd for dead pane %0 must NOT restore
	// its original name/styles onto the surviving window now at main:0.
	mc := &mockClient{
		panes: []tmux.PaneInfo{
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
	mc.panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "0", WindowName: "task-one", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-one", PaneID: "%1"},
	}
	mc.renames = nil
	mc.windowOpts = nil

	// SessionEnd for the dead pane %0 — cached target is main:0, which is
	// now occupied by %1 after renumbering.
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "s0", TmuxPane: "%0"})

	// Must NOT rename or change options on main:0 (that's %1's window now).
	for _, r := range mc.renames {
		if r.target == "main:0" {
			t.Errorf("SessionEnd for dead pane must not rename main:0, got rename to %q", r.name)
		}
	}
	for _, opt := range mc.windowOpts {
		if opt.target == "main:0" {
			t.Errorf("SessionEnd for dead pane must not set option on main:0: %s=%s", opt.key, opt.value)
		}
	}

	// Dead pane's state should be discarded.
	if _, ok := d.panes["%0"]; ok {
		t.Error("expected dead pane %0 removed from pane cache")
	}

	// %1's state should still exist (at main:1 — will be migrated by next event/sweep).
	if ws := d.windows["main:1"]; ws == nil || ws.PaneID != "%1" {
		t.Error("expected %1's state still at main:1")
	}
}

func TestDaemon_WindowRenumberingMigratesState(t *testing.T) {
	// Setup: 3 windows, all tracked.
	mc := &mockClient{
		panes: []tmux.PaneInfo{
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

	if ws := d.windows["main:2"]; ws == nil || ws.PaneID != "%2" {
		t.Fatalf("precondition: expected main:2 tracked with %%2")
	}

	// Simulate: window 1 killed, renumber-windows on → window 2 becomes window 1.
	mc.panes = []tmux.PaneInfo{
		{SessionName: "main", WindowIndex: "0", WindowName: "task-zero", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-zero", PaneID: "%0"},
		{SessionName: "main", WindowIndex: "1", WindowName: "task-two", PaneIndex: "0",
			PaneCurrentCmd: "claude", PaneTitle: "⠋ task-two", PaneID: "%2"},
	}
	mc.renames = nil
	mc.windowOpts = nil

	// Event arrives for pane %2 (now at window 1, not 2).
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "s2", TmuxPane: "%2"})

	// State should be migrated from main:2 to main:1.
	ws := d.windows["main:1"]
	if ws == nil {
		t.Fatal("expected state migrated to main:1")
	}
	if ws.PaneID != "%2" {
		t.Errorf("expected PaneID=%%2 at main:1, got %q", ws.PaneID)
	}
	if _, ok := d.windows["main:2"]; ok {
		t.Error("expected main:2 removed after migration")
	}

	// Styles should be applied to the new target main:1.
	if v, ok := findWindowOpt(mc.windowOpts, "main:1", "window-status-style"); !ok || v != "fg=green,dim" {
		t.Errorf("expected window-status-style=fg=green,dim on main:1, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:1", "@muxwatch-symbol"); !ok || v != "✓" {
		t.Errorf("expected @muxwatch-symbol=✓ on main:1, got %q (found=%v)", v, ok)
	}

	// Nothing should target stale main:2.
	for _, opt := range mc.windowOpts {
		if opt.target == "main:2" {
			t.Errorf("unexpected SetWindowOption targeting stale main:2: %s=%s", opt.key, opt.value)
		}
	}
	for _, r := range mc.renames {
		if r.target == "main:2" {
			t.Errorf("unexpected RenameWindow targeting stale main:2: %s", r.name)
		}
	}
}
