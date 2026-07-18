package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux/tmuxtest"
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
				PaneCurrentCmd: "codex", PaneTitle: "cenci", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "cenci" {
		t.Errorf("after SessionStart: expected native pane title 'cenci', got %q", name)
	}

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "cenci" {
		t.Errorf("after UserPromptSubmit: expected native pane title, got %q", name)
	}
	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "!" {
		t.Errorf("expected @cenci-symbol=! after PermissionRequest, got %q (found=%v)", v, ok)
	}
	if name, _ := lastRename(mc.Renames, "main:0"); name != "cenci" {
		t.Errorf("after PermissionRequest: expected retained native pane title, got %q", name)
	}

	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); !ok || v != "✓" {
		t.Errorf("expected @cenci-symbol=✓ after Stop, got %q (found=%v)", v, ok)
	}
	if name, _ := lastRename(mc.Renames, "main:0"); name != "cenci" {
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

// Regression for #432: Claude Code fires a SessionEnd hook, so the Codex-only
// "agent exited without SessionEnd" sweep restore must not touch a finished
// Claude session while its pane is still alive. #418 started tagging Claude
// events with -agent claude, which set ws.Agent="claude" and (before the fix)
// activated that Codex-intended restore whenever pane_current_command wasn't
// literally "claude" (e.g. an npm/node shim reports "node"), silently wiping the
// just-finished session and reverting the window — it read as idle.
func TestDaemon_ClaudeDoneSurvivesSweepWhenPaneCommandDiffers(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "claude-sess", Agent: "claude", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "claude-sess", Agent: "claude", TmuxPane: "%0"})

	// Stop → done.
	mc.Panes[0].PaneTitle = "✳ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "claude-sess", Agent: "claude", TmuxPane: "%0"})
	if got := d.sessions["claude-sess"].Status; got != detect.StatusDone {
		t.Fatalf("after Stop: expected done, got %v", got)
	}

	// The pane is still alive but its current command no longer reads as
	// "claude" (npm/node shim, or a sandbox lookup failure). A finished Claude
	// session must survive the sweep — it relies on its SessionEnd hook, not
	// this Codex-only exit restore.
	mc.Panes[0].PaneCurrentCmd = "node"
	mc.Renames = nil

	d.runSweep()

	if _, ok := d.sessions["claude-sess"]; !ok {
		t.Fatalf("expected Claude session to survive sweep, but it was removed")
	}
	if got := d.sessions["claude-sess"].Status; got != detect.StatusDone {
		t.Errorf("expected status to stay done after sweep, got %v", got)
	}
	if d.frontend.WindowInfo("claude-sess") == nil {
		t.Error("expected window to remain tracked after sweep")
	}
	if name, ok := lastRename(mc.Renames, "main:0"); ok && name == "zsh" {
		t.Errorf("expected no restore to original name 'zsh' after sweep, got rename to %q", name)
	}
}

func TestDaemon_CodexPromptLabelAndNativeQuestionReconciliation(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{{
		SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
		PaneCurrentCmd: "codex", PaneTitle: "cenci", PaneID: "%0",
	}}}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	if name, _ := lastRename(mc.Renames, "main:0"); name != "cenci" {
		t.Fatalf("folder fallback = %q, want cenci", name)
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

	mc.Panes[0].PaneTitle = "[ ! ] Action Required | cenci"
	d.runSweep()
	if got := d.sessions["codex-sess"].Status; got != detect.StatusNeedInput {
		t.Fatalf("action title status = %v, want need-input", got)
	}
	if got := d.sessions["codex-sess"].AttentionSource; got != "action-required-title" {
		t.Fatalf("attention source = %q, want action-required-title", got)
	}
	if got := d.sessions["codex-sess"].TaskName; got != "improve codex tmux names" {
		t.Fatalf("action title changed task = %q", got)
	}
	if symbol, _ := findWindowOpt(mc.WindowOpts, "main:0", "@cenci-symbol"); symbol != "!" {
		t.Fatalf("action title symbol = %q, want !", symbol)
	}

	mc.Panes[0].PaneTitle = "⠋ Working | cenci"
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
		PaneCurrentCmd: "codex", PaneTitle: "[ ! ] Action Required | cenci", PaneID: "%0",
	}}}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.runSweep()
	if got := d.sessions["codex-sess"].TaskName; got != "cenci" {
		t.Fatalf("restart fallback = %q, want cenci", got)
	}
	if got := d.sessions["codex-sess"].Status; got != detect.StatusNeedInput {
		t.Fatalf("restart action status = %v, want need-input", got)
	}
}

func TestDaemon_CodexKeepsDispatchedWindowName(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{{
			SessionName: "main", WindowIndex: "0", WindowName: "146-ci-shell-lint", PaneIndex: "0",
			PaneCurrentCmd: "codex", PaneTitle: "cenci", PaneID: "%0",
		}},
		WindowOptValues: map[string]string{"main:0:automatic-rename": "off"},
	}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%0", TaskName: "improve codex tmux names"})
	mc.Panes[0].PaneTitle = "[ ! ] Action Required | cenci"
	d.runSweep()
	if name, _ := lastRename(mc.Renames, "main:0"); name != "146-ci-shell-lint" {
		t.Fatalf("dispatched window renamed to %q", name)
	}
}

// --- OpenCode sweep/lifecycle tests (#488) ---
//
// OpenCode's mapped hook event stream (session/prompt/permission/tool/stop)
// drives status through the same agent-neutral daemon/event.go logic already
// exercised above for Claude/Codex — no daemon code changes are required for
// that path (see watch/internal/daemon/event.go: mapEventToStatusRaw and the
// AgentID subagent-suppression guard are agent-neutral already). What is new
// for #488 is the OpenCode analog of the Codex exit-restore/liveness sweep in
// frontend.go, which does not exist yet.

// TestDaemon_OpenCodeFullLifecycle exercises the mapped OpenCode event stream
// end to end (session/prompt/permission/tool/stop) through the existing
// agent-neutral daemon core — this proves the daemon needs no changes to
// drive OpenCode's running/need-input/done states, matching the architecture
// note in the #488 plan.
func TestDaemon_OpenCodeFullLifecycle(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "opencode", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	if got := d.sessions["oc-sess"].Status; got != detect.StatusIdle {
		t.Fatalf("after SessionStart: expected idle, got %v", got)
	}
	if got := d.sessions["oc-sess"].Agent; got != "opencode" {
		t.Fatalf("expected sess.Agent=opencode, got %q", got)
	}

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	if got := d.sessions["oc-sess"].Status; got != detect.StatusRunning {
		t.Fatalf("after UserPromptSubmit: expected running, got %v", got)
	}

	d.handleEvent(ipc.HookEvent{EventType: "PermissionRequest", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	if got := d.sessions["oc-sess"].Status; got != detect.StatusNeedInput {
		t.Fatalf("after PermissionRequest: expected need-input, got %v", got)
	}

	d.handleEvent(ipc.HookEvent{EventType: "PostToolUse", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	if got := d.sessions["oc-sess"].Status; got != detect.StatusRunning {
		t.Fatalf("after PostToolUse: expected running, got %v", got)
	}

	mc.Panes[0].PaneTitle = "✳ writing tests"
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	if got := d.sessions["oc-sess"].Status; got != detect.StatusDone {
		t.Fatalf("after Stop: expected done, got %v", got)
	}

	mc.Renames = nil
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "oc-sess", TmuxPane: "%0"})
	if len(mc.Renames) != 1 || mc.Renames[0].Name != "zsh" {
		t.Errorf("after SessionEnd: expected restore to 'zsh', got %v", mc.Renames)
	}
	if len(d.sessions) != 0 {
		t.Errorf("expected sessions map empty after SessionEnd, got %d", len(d.sessions))
	}
}

// TestDaemon_OpenCodeSubagentStopDoesNotFlipMainStatus is the OpenCode analog
// of TestDaemon_SubagentStopDoesNotFlipMainStatus (#277): a subagent-scoped
// Stop (same session_id, non-empty AgentID) must not complete the main
// OpenCode session.
func TestDaemon_OpenCodeSubagentStopDoesNotFlipMainStatus(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "opencode", PaneTitle: "✳ delegating work", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "PreToolUse", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0", ToolName: "Task"})

	if got := d.sessions["oc-sess"].Status; got != detect.StatusRunning {
		t.Fatalf("precondition: expected StatusRunning after PreToolUse(Task), got %v", got)
	}

	// Subagent's own Stop event: same session, scoped to the subagent.
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0", AgentID: "sub1"})
	if got := d.sessions["oc-sess"].Status; got != detect.StatusRunning {
		t.Errorf("expected StatusRunning after subagent Stop (AgentID set), got %v — main OpenCode session incorrectly flipped to done mid-delegation", got)
	}

	// Main agent's own Stop event (no AgentID) must still flip to done.
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	if got := d.sessions["oc-sess"].Status; got != detect.StatusDone {
		t.Errorf("expected StatusDone after main-agent Stop, got %v", got)
	}
}

// TestDaemon_OpenCodeLifecycleWithoutSessionEndRestoresAfterExit is the
// OpenCode analog of TestDaemon_CodexLifecycleWithoutSessionEndRestoresAfterExit
// (#488): frontend.go's Codex-only exit-restore/liveness sweep does not yet
// have an OpenCode branch, so a finished OpenCode session whose pane reverts
// to the user's shell (an unambiguous exit signal — never mapped to
// "opencode" by inferAgent) is never restored today. This is the primary red
// test driving the frontend.go sweep addition.
func TestDaemon_OpenCodeLifecycleWithoutSessionEndRestoresAfterExit(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "opencode", PaneTitle: "cenci", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
	if got := d.sessions["oc-sess"].Status; got != detect.StatusDone {
		t.Fatalf("precondition: expected done after Stop, got %v", got)
	}

	// OpenCode process exits back to the user's shell — an unambiguous signal
	// (unlike "node"/"bun", never aliased to "opencode" by inferAgent).
	mc.Panes[0].PaneCurrentCmd = "zsh"
	mc.Renames = nil
	mc.WindowOpts = nil

	d.runSweep()

	if len(mc.Renames) != 1 || mc.Renames[0].Name != "zsh" {
		t.Fatalf("expected restore to 'zsh' after OpenCode exit, got %v", mc.Renames)
	}
	if len(d.sessions) != 0 {
		t.Errorf("expected sessions map empty after OpenCode exit cleanup, got %d entries", len(d.sessions))
	}
	if d.frontend.WindowInfo("oc-sess") != nil {
		t.Error("expected window no longer tracked after OpenCode exit cleanup")
	}
}

// TestDaemon_OpenCodeSweepDoesNotUntrackOnAmbiguousRuntimeShimCommand is the
// OpenCode analog of TestDaemon_ClaudeDoneSurvivesSweepWhenPaneCommandDiffers:
// OpenCode's CLI runs atop a JS/Bun runtime, so a tmux pane may transiently
// report "node" or "bun" instead of "opencode" even while the session is
// still alive. The sweep must never treat that ambiguous signal as an exit —
// this pins the "never untrack on ambiguous/unrecognized command" guard from
// the #488 plan (mirroring the existing Claude npm/node-shim guard). Today
// this already holds trivially (no OpenCode sweep branch exists yet); once
// frontend.go's OpenCode analog is added, this guards it from regressing to
// Codex's broader "any mismatch restores" behavior.
func TestDaemon_OpenCodeSweepDoesNotUntrackOnAmbiguousRuntimeShimCommand(t *testing.T) {
	for _, shimCmd := range []string{"node", "bun"} {
		t.Run(shimCmd, func(t *testing.T) {
			mc := &tmuxtest.MockClient{
				Panes: []tmux.PaneInfo{
					{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
						PaneCurrentCmd: "opencode", PaneTitle: "cenci", PaneID: "%0"},
				},
			}
			d := newTestDaemon(mc)
			d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
			d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})
			d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%0"})

			mc.Panes[0].PaneCurrentCmd = shimCmd
			mc.Renames = nil

			d.runSweep()

			if _, ok := d.sessions["oc-sess"]; !ok {
				t.Fatalf("expected OpenCode session to survive sweep on ambiguous command %q, but it was removed", shimCmd)
			}
			if d.frontend.WindowInfo("oc-sess") == nil {
				t.Errorf("expected window to remain tracked after sweep on ambiguous command %q", shimCmd)
			}
			if name, ok := lastRename(mc.Renames, "main:0"); ok && name == "zsh" {
				t.Errorf("expected no restore to original name 'zsh' after sweep on ambiguous command %q, got rename to %q", shimCmd, name)
			}
		})
	}
}

// TestDaemon_MixedAgentSweepPreservesPerAgentSemantics is the regression
// guard the #488 plan calls for: Claude, Codex, and OpenCode sessions swept
// together in the same pass must each keep their own existing semantics —
// adding the OpenCode sweep branch must not perturb Claude's SessionEnd-only
// cleanup or Codex's broader "any mismatch restores" exit signal.
func TestDaemon_MixedAgentSweepPreservesPerAgentSemantics(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
			{SessionName: "main", WindowIndex: "1", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "codex", PaneTitle: "cenci", PaneID: "%1"},
			{SessionName: "main", WindowIndex: "2", WindowName: "zsh", PaneIndex: "0",
				PaneCurrentCmd: "opencode", PaneTitle: "cenci", PaneID: "%2"},
		},
	}
	d := newTestDaemon(mc)

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "claude-sess", Agent: "claude", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "claude-sess", Agent: "claude", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "claude-sess", Agent: "claude", TmuxPane: "%0"})

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%1"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "codex-sess", Agent: "codex", TmuxPane: "%1"})

	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%2"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%2"})
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "oc-sess", Agent: "opencode", TmuxPane: "%2"})

	// Claude's pane reads "node" (npm shim, ambiguous — must survive: Claude
	// relies on SessionEnd, not this sweep, per #432).
	mc.Panes[0].PaneCurrentCmd = "node"
	// Codex's pane reverts to its shell — Codex's existing "any mismatch"
	// exit signal must still restore it.
	mc.Panes[1].PaneCurrentCmd = "zsh"
	// OpenCode's pane reverts to its shell — unambiguous exit signal, must
	// restore (this is the new #488 behavior).
	mc.Panes[2].PaneCurrentCmd = "zsh"

	d.runSweep()

	if _, ok := d.sessions["claude-sess"]; !ok {
		t.Error("expected Claude session to survive sweep (node shim is ambiguous, not an exit signal)")
	}
	if _, ok := d.sessions["codex-sess"]; ok {
		t.Error("expected Codex session to be swept away on shell revert (unchanged existing behavior)")
	}
	if _, ok := d.sessions["oc-sess"]; ok {
		t.Error("expected OpenCode session to be swept away on shell revert (new #488 behavior)")
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
		return
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
		return
	}
	if wi.Session != "main" || wi.WindowIndex != "1" {
		t.Errorf("expected s2's state migrated to main:1, got %s:%s", wi.Session, wi.WindowIndex)
	}

	// Styles should be applied to the new target main:1.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:1", "window-status-style"); !ok || v != "fg=green,dim" {
		t.Errorf("expected window-status-style=fg=green,dim on main:1, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:1", "@cenci-symbol"); !ok || v != "✓" {
		t.Errorf("expected @cenci-symbol=✓ on main:1, got %q (found=%v)", v, ok)
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
