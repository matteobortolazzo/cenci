package status

import (
	"testing"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
)

func testConfig() Config {
	return Config{
		SymbolIdle:      "~",
		SymbolRunning:   "▶",
		SymbolDone:      "✓",
		SymbolNeedInput: "!",
		SymbolStopped:   "⏹",
	}
}

func TestFormat_EmptySnapshot(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
	}
	out := Format(snap, testConfig())

	if out.Text != "" {
		t.Errorf("expected empty text, got %q", out.Text)
	}
	if out.Class != "none" {
		t.Errorf("expected class 'none', got %q", out.Class)
	}
	if out.Alt != "none" {
		t.Errorf("expected alt 'none', got %q", out.Alt)
	}
}

func TestFormat_RunningOnly(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
			{Session: "main", WindowIndex: "1", TaskName: "fixing auth", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 2, Running: 2},
	}
	out := Format(snap, testConfig())

	if out.Text != "▶ 2" {
		t.Errorf("expected '▶ 2', got %q", out.Text)
	}
	if out.Class != "running" {
		t.Errorf("expected class 'running', got %q", out.Class)
	}
	if out.Alt != "active" {
		t.Errorf("expected alt 'active', got %q", out.Alt)
	}
}

func TestFormat_MixedStatuses(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
			{Session: "main", WindowIndex: "1", TaskName: "fixing auth", Status: "need-input"},
			{Session: "main", WindowIndex: "2", TaskName: "done task", Status: "done"},
		},
		Summary: ipc.StatusSummary{Total: 3, Running: 1, NeedInput: 1, Done: 1},
	}
	out := Format(snap, testConfig())

	if out.Text != "▶ 1  ! 1  ✓ 1" {
		t.Errorf("expected '▶ 1  ! 1  ✓ 1', got %q", out.Text)
	}
	if out.Class != "need-input" {
		t.Errorf("expected class 'need-input' (highest priority), got %q", out.Class)
	}
}

func TestFormat_TooltipOrder(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
			{Session: "main", WindowIndex: "2", TaskName: "fixing auth", Status: "need-input"},
		},
		Summary: ipc.StatusSummary{Total: 2, Running: 1, NeedInput: 1},
	}
	out := Format(snap, testConfig())

	expected := "main:0 - writing tests (running)\nmain:2 - fixing auth (need-input)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip:\n%s\ngot:\n%s", expected, out.Tooltip)
	}
}

func TestFormat_DoneOnly(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "work", WindowIndex: "1", TaskName: "finished task", Status: "done"},
		},
		Summary: ipc.StatusSummary{Total: 1, Done: 1},
	}
	out := Format(snap, testConfig())

	if out.Text != "✓ 1" {
		t.Errorf("expected '✓ 1', got %q", out.Text)
	}
	if out.Class != "done" {
		t.Errorf("expected class 'done', got %q", out.Class)
	}
}

func TestFormat_AllStatuses(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "s", WindowIndex: "0", TaskName: "a", Status: "running"},
			{Session: "s", WindowIndex: "1", TaskName: "b", Status: "running"},
			{Session: "s", WindowIndex: "2", TaskName: "c", Status: "need-input"},
			{Session: "s", WindowIndex: "3", TaskName: "d", Status: "done"},
			{Session: "s", WindowIndex: "4", TaskName: "e", Status: "done"},
			{Session: "s", WindowIndex: "5", TaskName: "f", Status: "done"},
		},
		Summary: ipc.StatusSummary{Total: 6, Running: 2, NeedInput: 1, Done: 3},
	}
	out := Format(snap, testConfig())

	if out.Text != "▶ 2  ! 1  ✓ 3" {
		t.Errorf("expected '▶ 2  ! 1  ✓ 3', got %q", out.Text)
	}
}

func TestFormat_FallbackToWindowName(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", WindowName: "my-window", TaskName: "", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}
	out := Format(snap, testConfig())

	expected := "main:0 - my-window (running)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
	}
}

func TestFormat_ManuallyNamedShowsWindowName(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", WindowName: "my-project", TaskName: "writing tests", Status: "running", ManuallyNamed: true},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}
	out := Format(snap, testConfig())

	expected := "main:0 - my-project (running)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
	}
}

func TestFormat_AutoNamedShowsTaskName(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", WindowName: "writing tests", TaskName: "writing tests", Status: "running", ManuallyNamed: false},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}
	out := Format(snap, testConfig())

	expected := "main:0 - writing tests (running)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
	}
}

func TestFormat_IdleOnly(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", WindowName: "bash", TaskName: "", Status: "idle"},
		},
		Summary: ipc.StatusSummary{Total: 1, Idle: 1},
	}
	out := Format(snap, testConfig())

	if out.Text != "~ 1" {
		t.Errorf("expected '~ 1', got %q", out.Text)
	}
	if out.Class != "idle" {
		t.Errorf("expected class 'idle', got %q", out.Class)
	}
	if out.Alt != "active" {
		t.Errorf("expected alt 'active', got %q", out.Alt)
	}
}

func TestFormat_IdleAndRunning(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
			{Session: "main", WindowIndex: "1", WindowName: "bash", TaskName: "", Status: "idle"},
		},
		Summary: ipc.StatusSummary{Total: 2, Running: 1, Idle: 1},
	}
	out := Format(snap, testConfig())

	if out.Text != "▶ 1  ~ 1" {
		t.Errorf("expected '▶ 1  ~ 1', got %q", out.Text)
	}
	if out.Class != "running" {
		t.Errorf("expected class 'running' (higher priority than idle), got %q", out.Class)
	}
}

func TestFormat_StoppedOnly(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "interrupted task", Status: "stopped"},
		},
		Summary: ipc.StatusSummary{Total: 1, Stopped: 1},
	}
	out := Format(snap, testConfig())

	if out.Text != "⏹ 1" {
		t.Errorf("expected '⏹ 1', got %q", out.Text)
	}
	if out.Class != "stopped" {
		t.Errorf("expected class 'stopped', got %q", out.Class)
	}
}

func TestFormat_StoppedPriorityBetweenDoneAndIdle(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "interrupted", Status: "stopped"},
			{Session: "main", WindowIndex: "1", WindowName: "bash", Status: "idle"},
		},
		Summary: ipc.StatusSummary{Total: 2, Stopped: 1, Idle: 1},
	}
	out := Format(snap, testConfig())

	if out.Class != "stopped" {
		t.Errorf("expected class 'stopped' (higher priority than idle), got %q", out.Class)
	}
}

func TestFormat_DoneHigherThanStopped(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "done task", Status: "done"},
			{Session: "main", WindowIndex: "1", TaskName: "interrupted", Status: "stopped"},
		},
		Summary: ipc.StatusSummary{Total: 2, Done: 1, Stopped: 1},
	}
	out := Format(snap, testConfig())

	if out.Class != "done" {
		t.Errorf("expected class 'done' (higher priority than stopped), got %q", out.Class)
	}
	if out.Text != "✓ 1  ⏹ 1" {
		t.Errorf("expected '✓ 1  ⏹ 1', got %q", out.Text)
	}
}

func TestFormat_AllStatusesIncludingStopped(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "s", WindowIndex: "0", TaskName: "a", Status: "running"},
			{Session: "s", WindowIndex: "1", TaskName: "b", Status: "need-input"},
			{Session: "s", WindowIndex: "2", TaskName: "c", Status: "done"},
			{Session: "s", WindowIndex: "3", TaskName: "d", Status: "stopped"},
			{Session: "s", WindowIndex: "4", TaskName: "e", Status: "idle"},
		},
		Summary: ipc.StatusSummary{Total: 5, Running: 1, NeedInput: 1, Done: 1, Stopped: 1, Idle: 1},
	}
	out := Format(snap, testConfig())

	if out.Text != "▶ 1  ! 1  ✓ 1  ⏹ 1  ~ 1" {
		t.Errorf("expected '▶ 1  ! 1  ✓ 1  ⏹ 1  ~ 1', got %q", out.Text)
	}
	if out.Class != "need-input" {
		t.Errorf("expected class 'need-input' (highest priority), got %q", out.Class)
	}
}

func TestFormat_PanelessTooltipLine(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{WindowName: "sandbox task", TaskName: "sandbox task", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}
	out := Format(snap, testConfig())

	// No "sess:idx - " prefix for paneless entries.
	expected := "sandbox task (running)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
	}
	if out.Text != "▶ 1" {
		t.Errorf("expected '▶ 1', got %q", out.Text)
	}
}

func TestFormat_PanelessTooltipFallsBackToAgent(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Status: "running", Agent: "claude"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}
	out := Format(snap, testConfig())

	expected := "claude (running)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
	}
}

func TestFormat_MixedPanedAndPanelessTooltip(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
			{WindowName: "sandbox task", TaskName: "sandbox task", Status: "need-input"},
		},
		Summary: ipc.StatusSummary{Total: 2, Running: 1, NeedInput: 1},
	}
	out := Format(snap, testConfig())

	expected := "main:0 - writing tests (running)\nsandbox task (need-input)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip:\n%s\ngot:\n%s", expected, out.Tooltip)
	}
	if out.Class != "need-input" {
		t.Errorf("expected class 'need-input', got %q", out.Class)
	}
}
