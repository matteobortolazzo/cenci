package daemon

import (
	"testing"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/detect"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/tmux"
)

func TestDaemon_PostToolUseFailureInterruptSetsStopped(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// PostToolUseFailure with is_interrupt=true → Stopped.
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: true})

	ws := d.windows["main:0"]
	if ws == nil {
		t.Fatal("expected window to be tracked")
	}
	if ws.Status != detect.StatusStopped {
		t.Errorf("expected StatusStopped, got %v", ws.Status)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "window-status-style"); !ok || v != "fg=yellow,dim" {
		t.Errorf("expected window-status-style=fg=yellow,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @agentwatch-symbol=⏹, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PostToolUseFailureNoInterruptStaysRunning(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// PostToolUseFailure without interrupt → still Running (tool failed, Claude retries).
	optsBeforeCount := len(mc.windowOpts)
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: false})

	ws := d.windows["main:0"]
	if ws == nil {
		t.Fatal("expected window to be tracked")
	}
	if ws.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning, got %v", ws.Status)
	}
	// No status change (Running → Running) should be a no-op for styles.
	if len(mc.windowOpts) != optsBeforeCount {
		t.Errorf("expected no additional window opts for same-status, got %d more", len(mc.windowOpts)-optsBeforeCount)
	}
}

func TestDaemon_SweepDetectsIdlePaneTitle(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Verify running state.
	ws := d.windows["main:0"]
	if ws == nil || ws.Status != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning, got %v", ws.Status)
	}

	// Simulate: pane title reverts to idle marker (user pressed ESC during text gen).
	mc.panes[0].PaneTitle = "✳ writing tests"
	mc.windowOpts = nil

	d.sweepStale()

	// Sweep should detect idle marker and transition to Stopped.
	if ws.Status != detect.StatusStopped {
		t.Errorf("expected StatusStopped after sweep, got %v", ws.Status)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @agentwatch-symbol=⏹, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_SweepIgnoresRunningWithBrailleTitle(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Pane title still has braille spinner — Claude is actively working.
	mc.windowOpts = nil
	d.sweepStale()

	// Should remain Running (braille = active spinner, not idle).
	ws := d.windows["main:0"]
	if ws.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning (braille spinner active), got %v", ws.Status)
	}
}

func TestDaemon_SweepIgnoresNonRunningWindows(t *testing.T) {
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

	// Window is now Done.
	ws := d.windows["main:0"]
	if ws == nil || ws.Status != detect.StatusDone {
		t.Fatalf("precondition: expected StatusDone, got %v", ws.Status)
	}

	mc.windowOpts = nil
	d.sweepStale()

	// Should remain Done — sweep idle detection only applies to Running windows.
	if ws.Status != detect.StatusDone {
		t.Errorf("expected StatusDone unchanged, got %v", ws.Status)
	}
}

func TestDaemon_FullLifecycleWithInterrupt(t *testing.T) {
	mc := &mockClient{
		panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	// SessionStart → idle
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	if ws := d.windows["main:0"]; ws.Status != detect.StatusIdle {
		t.Errorf("after SessionStart: expected StatusIdle, got %v", ws.Status)
	}

	// UserPromptSubmit → running
	mc.panes[0].PaneTitle = "⠋ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if ws := d.windows["main:0"]; ws.Status != detect.StatusRunning {
		t.Errorf("after UserPromptSubmit: expected StatusRunning, got %v", ws.Status)
	}

	// PostToolUseFailure(interrupt) → stopped
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: true})
	if ws := d.windows["main:0"]; ws.Status != detect.StatusStopped {
		t.Errorf("after PostToolUseFailure: expected StatusStopped, got %v", ws.Status)
	}
	if v, ok := findWindowOpt(mc.windowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @agentwatch-symbol=⏹, got %q (found=%v)", v, ok)
	}

	// UserPromptSubmit → running again (user submits new prompt)
	mc.panes[0].PaneTitle = "⠋ fixing bug"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if ws := d.windows["main:0"]; ws.Status != detect.StatusRunning {
		t.Errorf("after second UserPromptSubmit: expected StatusRunning, got %v", ws.Status)
	}
	if name, ok := lastRename(mc.renames, "main:0"); !ok || name != "fixing bug" {
		t.Errorf("expected rename to 'fixing bug', got %q (found=%v)", name, ok)
	}

	// Stop → done (natural completion)
	mc.panes[0].PaneTitle = "✳ fixing bug"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})
	if ws := d.windows["main:0"]; ws.Status != detect.StatusDone {
		t.Errorf("after Stop: expected StatusDone, got %v", ws.Status)
	}

	// SessionEnd → restored
	mc.renames = nil
	mc.windowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	if len(mc.renames) != 1 || mc.renames[0].name != "bash" {
		t.Errorf("after SessionEnd: expected restore to 'bash', got %v", mc.renames)
	}
	if len(d.windows) != 0 {
		t.Errorf("expected windows map empty after SessionEnd, got %d", len(d.windows))
	}
}
