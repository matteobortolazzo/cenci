package daemon

import (
	"testing"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/detect"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/tmux/tmuxtest"
)

func TestDaemon_PostToolUseFailureInterruptSetsStopped(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// PostToolUseFailure with is_interrupt=true → Stopped.
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: true})

	sess := d.sessions["sess1"]
	if sess == nil {
		t.Fatal("expected session to be tracked")
	}
	if sess.Status != detect.StatusStopped {
		t.Errorf("expected StatusStopped, got %v", sess.Status)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=yellow,dim" {
		t.Errorf("expected window-status-style=fg=yellow,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @agentwatch-symbol=⏹, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_PostToolUseFailureNoInterruptStaysRunning(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// PostToolUseFailure without interrupt → still Running (tool failed, Claude retries).
	optsBeforeCount := len(mc.WindowOpts)
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: false})

	sess := d.sessions["sess1"]
	if sess == nil {
		t.Fatal("expected session to be tracked")
	}
	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning, got %v", sess.Status)
	}
	// No status change (Running → Running) should be a no-op for styles.
	if len(mc.WindowOpts) != optsBeforeCount {
		t.Errorf("expected no additional window opts for same-status, got %d more", len(mc.WindowOpts)-optsBeforeCount)
	}
}

func TestDaemon_SweepDetectsIdlePaneTitle(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Verify running state.
	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning, got %v", sess.Status)
	}

	// Simulate: pane title reverts to idle marker (user pressed ESC during text gen).
	mc.Panes[0].PaneTitle = "✳ writing tests"
	mc.WindowOpts = nil

	d.runSweep()

	// Sweep should detect idle marker and transition to Stopped.
	if sess.Status != detect.StatusStopped {
		t.Errorf("expected StatusStopped after sweep, got %v", sess.Status)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @agentwatch-symbol=⏹, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_SweepIgnoresRunningWithBrailleTitle(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Pane title still has braille spinner — Claude is actively working.
	mc.WindowOpts = nil
	d.runSweep()

	// Should remain Running (braille = active spinner, not idle).
	sess := d.sessions["sess1"]
	if sess.Status != detect.StatusRunning {
		t.Errorf("expected StatusRunning (braille spinner active), got %v", sess.Status)
	}
}

func TestDaemon_SweepIgnoresNonRunningWindows(t *testing.T) {
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

	// Window is now Done.
	sess := d.sessions["sess1"]
	if sess == nil || sess.Status != detect.StatusDone {
		t.Fatalf("precondition: expected StatusDone, got %v", sess.Status)
	}

	mc.WindowOpts = nil
	d.runSweep()

	// Should remain Done — sweep idle detection only applies to Running windows.
	if sess.Status != detect.StatusDone {
		t.Errorf("expected StatusDone unchanged, got %v", sess.Status)
	}
}

func TestDaemon_FullLifecycleWithInterrupt(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	// SessionStart → idle
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusIdle {
		t.Errorf("after SessionStart: expected StatusIdle, got %v", sess.Status)
	}

	// UserPromptSubmit → running
	mc.Panes[0].PaneTitle = "⠋ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusRunning {
		t.Errorf("after UserPromptSubmit: expected StatusRunning, got %v", sess.Status)
	}

	// PostToolUseFailure(interrupt) → stopped
	d.handleEvent(ipc.HookEvent{EventType: "PostToolUseFailure", SessionID: "sess1", TmuxPane: "%0", IsInterrupt: true})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusStopped {
		t.Errorf("after PostToolUseFailure: expected StatusStopped, got %v", sess.Status)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "⏹" {
		t.Errorf("expected @agentwatch-symbol=⏹, got %q (found=%v)", v, ok)
	}

	// UserPromptSubmit → running again (user submits new prompt)
	mc.Panes[0].PaneTitle = "⠋ fixing bug"
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusRunning {
		t.Errorf("after second UserPromptSubmit: expected StatusRunning, got %v", sess.Status)
	}
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "fixing bug" {
		t.Errorf("expected rename to 'fixing bug', got %q (found=%v)", name, ok)
	}

	// Stop → done (natural completion)
	mc.Panes[0].PaneTitle = "✳ fixing bug"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})
	if sess := d.sessions["sess1"]; sess.Status != detect.StatusDone {
		t.Errorf("after Stop: expected StatusDone, got %v", sess.Status)
	}

	// SessionEnd → restored
	mc.Renames = nil
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	if len(mc.Renames) != 1 || mc.Renames[0].Name != "bash" {
		t.Errorf("after SessionEnd: expected restore to 'bash', got %v", mc.Renames)
	}
	if len(d.sessions) != 0 {
		t.Errorf("expected sessions map empty after SessionEnd, got %d", len(d.sessions))
	}
}
