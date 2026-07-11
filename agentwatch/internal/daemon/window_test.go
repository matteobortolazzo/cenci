package daemon

import (
	"testing"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/tmux/tmuxtest"
)

func TestDaemon_ManuallyNamedWindowKeepsOriginalName(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "my-window", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
		WindowOptValues: map[string]string{
			"main:0:automatic-rename": "off",
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Should keep original name (symbol is in @agentwatch-symbol, not the name).
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "my-window" {
		t.Errorf("expected rename to 'my-window', got %q (found=%v)", name, ok)
	}

	// SHOULD have set styles and symbol.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-style=fg=blue,dim, got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "▶" {
		t.Errorf("expected @agentwatch-symbol=▶, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_ManuallyNamedRestoresOriginalNameOnEnd(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "my-window", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
		WindowOptValues: map[string]string{
			"main:0:automatic-rename": "off",
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	mc.Renames = nil
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	// Should restore to original name.
	if len(mc.Renames) != 1 || mc.Renames[0].Name != "my-window" {
		t.Errorf("expected restore rename to 'my-window', got %v", mc.Renames)
	}

	// Should NOT re-enable automatic-rename for manually-named windows.
	for _, opt := range mc.WindowOpts {
		if opt.Key == "automatic-rename" {
			t.Errorf("expected no automatic-rename changes for manually-named window")
			break
		}
	}

	// SHOULD clear @agentwatch-style and @agentwatch-symbol.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-style"); !ok || v != "" {
		t.Errorf("expected @agentwatch-style cleared, got %q", v)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "@agentwatch-symbol"); !ok || v != "" {
		t.Errorf("expected @agentwatch-symbol cleared, got %q", v)
	}
}

func TestDaemon_MidSessionRenameDetected(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// User manually renames the window.
	mc.Panes[0].WindowName = "my-custom-name"
	mc.Renames = nil
	mc.WindowOpts = nil

	// Another event triggers — daemon should detect the rename.
	d.handleEvent(ipc.HookEvent{EventType: "Stop", SessionID: "sess1", TmuxPane: "%0"})

	// After detecting mid-session rename, should use new name (symbol in @agentwatch-symbol).
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "my-custom-name" {
		t.Errorf("expected rename to 'my-custom-name', got %q (found=%v)", name, ok)
	}

	// SessionEnd should restore the user's custom name, not the pre-rename one.
	mc.Renames = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "my-custom-name" {
		t.Errorf("expected restore to 'my-custom-name', got %q (found=%v)", name, ok)
	}
}

func TestDaemon_DisablesAutoRenameOnTrack(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ task", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})

	found := false
	for _, opt := range mc.WindowOpts {
		if opt.Target == "main:0" && opt.Key == "automatic-rename" && opt.Value == "off" {
			found = true
		}
	}
	if !found {
		t.Error("expected automatic-rename to be disabled on first track")
	}
}

func TestDaemon_DisablesAutoRenameForManuallyNamed(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "my-project", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
		WindowOptValues: map[string]string{
			"main:0:automatic-rename": "off",
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})

	found := false
	for _, opt := range mc.WindowOpts {
		if opt.Target == "main:0" && opt.Key == "automatic-rename" && opt.Value == "off" {
			found = true
		}
	}
	if !found {
		t.Error("expected automatic-rename to be disabled for manually-named window during tracking")
	}
}

func TestDaemon_RestoresOriginalStyles(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
		WindowOptValues: map[string]string{
			"main:0:window-status-style": "fg=white",
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-style"); !ok || v != "fg=white" {
		t.Errorf("expected restored window-status-style=fg=white, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_CurrentStyleSetAndRestored(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
		WindowOptValues: map[string]string{
			"main:0:window-status-current-style": "fg=yellow,bold",
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// During active session, current-style should be set to the active status style.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-current-style"); !ok || v != "fg=blue,dim" {
		t.Errorf("expected window-status-current-style=fg=blue,dim during running, got %q (found=%v)", v, ok)
	}

	// On session end, current-style should be restored.
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-current-style"); !ok || v != "fg=yellow,bold" {
		t.Errorf("expected restored window-status-current-style=fg=yellow,bold, got %q (found=%v)", v, ok)
	}
}

func TestDaemon_FormatStringsSavedAndRestored(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
		WindowOptValues: map[string]string{
			"main:0:window-status-format":         "#I:#W",
			"main:0:window-status-current-format": "#I:#W*",
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})

	// Format strings should be prepended with symbol variable.
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-format"); !ok || v != "#{@agentwatch-symbol} #I:#W" {
		t.Errorf("expected window-status-format='#{@agentwatch-symbol} #I:#W', got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-current-format"); !ok || v != "#{@agentwatch-symbol} #I:#W*" {
		t.Errorf("expected window-status-current-format='#{@agentwatch-symbol} #I:#W*', got %q (found=%v)", v, ok)
	}

	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// On session end, format strings should be restored.
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-format"); !ok || v != "#I:#W" {
		t.Errorf("expected restored window-status-format='#I:#W', got %q (found=%v)", v, ok)
	}
	if v, ok := findWindowOpt(mc.WindowOpts, "main:0", "window-status-current-format"); !ok || v != "#I:#W*" {
		t.Errorf("expected restored window-status-current-format='#I:#W*', got %q (found=%v)", v, ok)
	}
}

func TestDaemon_BuildSnapshotUsesOriginalNameForManuallyNamed(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "my-project", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
		WindowOptValues: map[string]string{
			"main:0:automatic-rename": "off",
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(snap.Windows))
	}
	w := snap.Windows[0]
	if w.WindowName != "my-project" {
		t.Errorf("expected WindowName 'my-project' for manually-named window, got %q", w.WindowName)
	}
	if w.TaskName != "writing tests" {
		t.Errorf("expected TaskName 'writing tests', got %q", w.TaskName)
	}
	if !w.ManuallyNamed {
		t.Error("expected ManuallyNamed=true")
	}
}

func TestDaemon_BuildSnapshotUsesTaskNameForAutoNamed(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(snap.Windows))
	}
	w := snap.Windows[0]
	if w.WindowName != "writing tests" {
		t.Errorf("expected WindowName 'writing tests' for auto-named window, got %q", w.WindowName)
	}
}

func TestDaemon_MaliciousPaneTitleSanitized(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "bash", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ evil\x00name\x07here", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// detect.TaskName("⠋ evil\x00name\x07here") → "evil\x00name\x07here"
	// After sanitizeWindowName: "evilnamehere" (control chars stripped)
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "evilnamehere" {
		t.Errorf("expected rename to 'evilnamehere', got %q (found=%v)", name, ok)
	}

	// Also verify the session task name is sanitized (flows to IPC broadcast).
	sess := d.sessions["sess1"]
	if sess == nil {
		t.Fatal("expected session to be tracked")
	}
	if sess.TaskName != "evilnamehere" {
		t.Errorf("expected sess.TaskName='evilnamehere', got %q", sess.TaskName)
	}
	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 {
		t.Fatalf("expected 1 window in snapshot, got %d", len(snap.Windows))
	}
	if snap.Windows[0].TaskName != "evilnamehere" {
		t.Errorf("expected IPC TaskName='evilnamehere', got %q", snap.Windows[0].TaskName)
	}
}

func TestDaemon_ControlCharsInOriginalNameSanitized(t *testing.T) {
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "my\x07window", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	mc.Renames = nil
	mc.WindowOpts = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})

	// On restore, RenameWindow should receive "mywindow" (sanitized OriginalName).
	if len(mc.Renames) != 1 {
		t.Fatalf("expected 1 restore rename, got %d", len(mc.Renames))
	}
	if mc.Renames[0].Name != "mywindow" {
		t.Errorf("expected restore to 'mywindow', got %q", mc.Renames[0].Name)
	}
}

func TestDaemon_DaemonRestartStripsResidualSymbol(t *testing.T) {
	// Simulate a daemon restart where the old daemon left a symbol in the window name.
	mc := &tmuxtest.MockClient{
		Panes: []tmux.PaneInfo{
			{SessionName: "main", WindowIndex: "0", WindowName: "▶ writing tests", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%0"},
		},
	}

	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})

	// Window should be renamed to clean name (no symbol prefix).
	if name, ok := lastRename(mc.Renames, "main:0"); !ok || name != "writing tests" {
		t.Errorf("expected rename to 'writing tests', got %q (found=%v)", name, ok)
	}

	// SessionEnd should restore to the clean name.
	mc.Renames = nil
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sess1", TmuxPane: "%0"})
	if len(mc.Renames) < 1 || mc.Renames[0].Name != "writing tests" {
		t.Errorf("expected restore to 'writing tests', got %v", mc.Renames)
	}
}
