package status

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/ipc"
)

func testConfig() Config {
	return Config{
		SymbolIdle:      "~",
		SymbolRunning:   "▶",
		SymbolDone:      "✓",
		SymbolNeedInput: "!",
		SymbolStopped:   "⏹",
		SymbolFailed:    "✗",
	}
}

func TestFormat_FailedOnly(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{WindowName: "42-fix-thing", Status: "failed"},
		},
		Summary: ipc.StatusSummary{Total: 1, Failed: 1},
	}
	out := Format(snap, testConfig())

	if out.Text != "✗ 1" {
		t.Errorf("expected '✗ 1', got %q", out.Text)
	}
	if out.Class != "failed" {
		t.Errorf("expected class 'failed', got %q", out.Class)
	}
}

func TestFormat_FailedWinsHighestClass(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "s", WindowIndex: "0", TaskName: "a", Status: "running"},
			{Session: "s", WindowIndex: "1", TaskName: "b", Status: "need-input"},
			{WindowName: "42-x", Status: "failed"},
		},
		Summary: ipc.StatusSummary{Total: 3, Running: 1, NeedInput: 1, Failed: 1},
	}
	out := Format(snap, testConfig())

	if out.Class != "failed" {
		t.Errorf("expected class 'failed' (highest priority, above need-input), got %q", out.Class)
	}
	if out.Text != "✗ 1  ▶ 1  ! 1" {
		t.Errorf("expected failed count to lead, got %q", out.Text)
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

// --- Budget headroom (#170) ------------------------------------------------

func TestFormat_HeadroomMultiAgentText(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
		Headroom: map[string]float64{
			"claude": 0.73,
			"codex":  0.15,
		},
	}
	out := Format(snap, testConfig())

	expected := "▶ 1  claude 73%  codex 15%"
	if out.Text != expected {
		t.Errorf("expected text %q, got %q", expected, out.Text)
	}
}

func TestFormat_HeadroomSortedKeyOrder(t *testing.T) {
	// Map key order is nondeterministic in Go; headroom rendering must sort
	// keys alphabetically for stable output regardless of insertion order.
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
		Headroom: map[string]float64{
			"zulu":  0.50,
			"alpha": 0.90,
		},
	}
	out := Format(snap, testConfig())

	if !strings.Contains(out.Text, "alpha 90%  zulu 50%") {
		t.Errorf("expected sorted-key headroom order 'alpha 90%%  zulu 50%%' in text, got %q", out.Text)
	}
}

func TestFormat_HeadroomTooltipAppended(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
		Headroom: map[string]float64{
			"claude": 0.73,
			"codex":  0.15,
		},
	}
	out := Format(snap, testConfig())

	expected := "main:0 - writing tests (running)\nclaude 73%  codex 15%"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip:\n%s\ngot:\n%s", expected, out.Tooltip)
	}
}

func TestFormat_HeadroomFieldPassthrough(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
		Headroom: map[string]float64{
			"claude": 0.73,
			"codex":  0.15,
		},
	}
	out := Format(snap, testConfig())

	// SwiftBar (and other rich frontends) read the numeric fraction directly
	// rather than re-parsing formatted text, so the raw values must pass
	// through unrounded.
	if !reflect.DeepEqual(out.Headroom, snap.Headroom) {
		t.Errorf("expected output.Headroom to pass through raw fractions %#v, got %#v", snap.Headroom, out.Headroom)
	}
}

// TestFormat_HeadroomAbsentParity asserts that when the snapshot carries no
// headroom data (nil map, per #169's "loop disabled/unconfigured" case), the
// waybar output is byte-for-byte identical to a snapshot with an explicit
// empty headroom map, and to today's (pre-#170) output shape: no empty or
// placeholder headroom text, and no "headroom" key at all in the marshaled
// JSON (relying on `omitempty`).
func TestFormat_HeadroomAbsentParity(t *testing.T) {
	baseWindows := []ipc.WindowState{
		{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
	}
	baseSummary := ipc.StatusSummary{Total: 1, Running: 1}

	snapNilHeadroom := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows:   baseWindows,
		Summary:   baseSummary,
	}
	snapEmptyHeadroom := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows:   baseWindows,
		Summary:   baseSummary,
		Headroom:  map[string]float64{},
	}

	outNil := Format(snapNilHeadroom, testConfig())
	outEmpty := Format(snapEmptyHeadroom, testConfig())

	if !reflect.DeepEqual(outNil, outEmpty) {
		t.Errorf("expected identical output for nil vs empty headroom map, got %+v vs %+v", outNil, outEmpty)
	}

	if outNil.Text != "▶ 1" {
		t.Errorf("expected text unchanged from today's no-headroom output '▶ 1', got %q", outNil.Text)
	}
	expectedTooltip := "main:0 - writing tests (running)"
	if outNil.Tooltip != expectedTooltip {
		t.Errorf("expected tooltip unchanged from today's no-headroom output %q, got %q", expectedTooltip, outNil.Tooltip)
	}

	data, err := json.Marshal(outNil)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), "headroom") {
		t.Errorf("expected no 'headroom' key in marshaled JSON when headroom is absent, got %s", data)
	}
}

func TestFormat_HeadroomEmptyMapVsNoDataOnEmptySnapshot(t *testing.T) {
	// Even the "no sessions" early-return path must not leak headroom text.
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Headroom:  map[string]float64{"claude": 0.73},
	}
	out := Format(snap, testConfig())

	if out.Text != "" {
		t.Errorf("expected empty text when there are no windows, got %q", out.Text)
	}
	if out.Class != "none" {
		t.Errorf("expected class 'none', got %q", out.Class)
	}
}

func TestHeadroomPercent_RoundHalfUp(t *testing.T) {
	tests := []struct {
		name    string
		frac    float64
		wantPct int
	}{
		{"documented rounding example", 0.156, 16},
		{"exact half rounds up", 0.155, 16},
		{"exact half rounds up (small)", 0.005, 1},
		{"zero", 0.0, 0},
		{"full", 1.0, 100},
		{"exact quarter, no rounding needed", 0.25, 25},
		{"round down below half", 0.154, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := headroomPercent(tt.frac)
			if got != tt.wantPct {
				t.Errorf("headroomPercent(%v) = %d, want %d", tt.frac, got, tt.wantPct)
			}
		})
	}
}

func TestHeadroomClass_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		pct  int
		want string
	}{
		{"well above warning threshold is normal", 90, "normal"},
		{"just above 25 is normal", 26, "normal"},
		{"exactly 25 is warning (inclusive upper bound)", 25, "warning"},
		{"mid warning band", 15, "warning"},
		{"exactly 10 is warning (inclusive lower bound)", 10, "warning"},
		{"just below 10 is critical", 9, "critical"},
		{"zero is critical", 0, "critical"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := headroomClass(tt.pct)
			if got != tt.want {
				t.Errorf("headroomClass(%d) = %q, want %q", tt.pct, got, tt.want)
			}
		})
	}
}
