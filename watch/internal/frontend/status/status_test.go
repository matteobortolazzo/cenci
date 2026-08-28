package status

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

func testConfig() Config {
	return Config{
		SymbolIdle:            "~",
		SymbolRunning:         "▶",
		SymbolDone:            "✓",
		SymbolNeedInput:       "!",
		SymbolStopped:         "⏹",
		SymbolFailed:          "✗",
		SymbolEscalated:       "?",
		SymbolDispatch:        "⟳",
		SymbolDispatchRunning: "⚙",
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

// -- Escalated (#826): its own status/counter/class, distinct from Failed --

func TestFormat_EscalatedOnly(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{WindowName: "42-implement", Status: "escalated"},
		},
		Summary: ipc.StatusSummary{Total: 1, Escalated: 1},
	}
	out := Format(snap, testConfig())

	if out.Text != "? 1" {
		t.Errorf("expected '? 1', got %q", out.Text)
	}
	if out.Class != "escalated" {
		t.Errorf("expected class 'escalated', got %q", out.Class)
	}
}

// TestFormat_EscalatedWinsOverNeedInputAndRunning locks in the plan's
// updated priority order: failed > escalated > need-input > running > done >
// stopped > idle -- escalated must outrank need-input/running for both the
// leading text part and the CSS class, but must still lose to failed.
func TestFormat_EscalatedWinsOverNeedInputAndRunning(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "s", WindowIndex: "0", TaskName: "a", Status: "running"},
			{Session: "s", WindowIndex: "1", TaskName: "b", Status: "need-input"},
			{WindowName: "42-implement", Status: "escalated"},
		},
		Summary: ipc.StatusSummary{Total: 3, Running: 1, NeedInput: 1, Escalated: 1},
	}
	out := Format(snap, testConfig())

	if out.Class != "escalated" {
		t.Errorf("expected class 'escalated' (above need-input/running), got %q", out.Class)
	}
	if out.Text != "? 1  ▶ 1  ! 1" {
		t.Errorf("expected escalated count to lead (after any failed), got %q", out.Text)
	}
}

// TestFormat_FailedStillWinsOverEscalated proves failed remains the single
// highest-priority state even with escalated also present.
func TestFormat_FailedStillWinsOverEscalated(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{WindowName: "1-implement", Status: "failed"},
			{WindowName: "2-implement", Status: "escalated"},
		},
		Summary: ipc.StatusSummary{Total: 2, Failed: 1, Escalated: 1},
	}
	out := Format(snap, testConfig())

	if out.Class != "failed" {
		t.Errorf("expected class 'failed' (highest priority, above escalated), got %q", out.Class)
	}
	if out.Text != "✗ 1  ? 1" {
		t.Errorf("expected failed count to lead escalated, got %q", out.Text)
	}
}

// TestFormat_EscalatedSymbolOverride proves the symbol is config-driven, not
// hardcoded, mirroring every other status symbol's own override test.
func TestFormat_EscalatedSymbolOverride(t *testing.T) {
	cfg := testConfig()
	cfg.SymbolEscalated = "@"
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows:   []ipc.WindowState{{WindowName: "42-implement", Status: "escalated"}},
		Summary:   ipc.StatusSummary{Total: 1, Escalated: 1},
	}
	out := Format(snap, cfg)

	if out.Text != "@ 1" {
		t.Errorf("expected '@ 1' with the overridden symbol, got %q", out.Text)
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

	expected := "main:0 - writing tests (unknown) (running)\nmain:2 - fixing auth (unknown) (need-input)"
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

	expected := "main:0 - my-window (unknown) (running)"
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

	expected := "main:0 - my-project (unknown) (running)"
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

	expected := "main:0 - writing tests (unknown) (running)"
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

	// "(no session) - " prefix for paneless entries.
	expected := "(no session) - sandbox task (unknown) (running)"
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

	// No task/window name known yet, so the name slot shows the "(untitled)"
	// placeholder while the agent renders in its own slot (unified format,
	// #405) rather than being folded into the name slot as before.
	expected := "(no session) - (untitled) (claude) (running)"
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

	expected := "main:0 - writing tests (unknown) (running)\n(no session) - sandbox task (unknown) (need-input)"
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

	expected := "main:0 - writing tests (unknown) (running)\nclaude 73%  codex 15%"
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
	expectedTooltip := "main:0 - writing tests (unknown) (running)"
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

// --- Fleet dispatch loop indicator (#212) ----------------------------------

// TestDefaultSymbolDispatch_Value locks in the default dispatch glyph chosen
// in planning Q&A #1: exactly U+27F3 (⟳), no Nerd Font codepoint.
func TestDefaultSymbolDispatch_Value(t *testing.T) {
	if DefaultSymbolDispatch != "⟳" {
		t.Errorf("expected DefaultSymbolDispatch to be U+27F3 (⟳), got %q", DefaultSymbolDispatch)
	}
}

// TestDefaultSymbolDispatchRunning_Value locks in the default "pass actively
// running" dispatch glyph chosen in planning Q&A #1: exactly U+2699 (⚙),
// mirroring TestDefaultSymbolDispatch_Value.
func TestDefaultSymbolDispatchRunning_Value(t *testing.T) {
	if DefaultSymbolDispatchRunning != "⚙" {
		t.Errorf("expected DefaultSymbolDispatchRunning to be U+2699 (⚙), got %q", DefaultSymbolDispatchRunning)
	}
}

// TestFormat_DispatchGlyphByState covers all four enabled x pass_running
// combinations for the "at most one dispatch icon" priority rule (running >
// idle-enabled > none), per ticket #263 AC #1 and #4. Case 4
// (enabled=false, pass_running=true) is a defensive regression guard: per
// planning Q&A #3, `enabled` must gate the whole indicator regardless of
// `pass_running`. Each case runs with zero sessions and with one active
// session so the appendDispatch concatenation (glyph placement after any
// session parts) is exercised in both shapes.
func TestFormat_DispatchGlyphByState(t *testing.T) {
	cfg := testConfig()

	tests := []struct {
		name        string
		enabled     bool
		passRunning bool
		wantGlyph   string // "" means no dispatch glyph at all
	}{
		{"enabled and pass running shows running glyph", true, true, cfg.SymbolDispatchRunning},
		{"enabled and idle shows idle glyph", true, false, cfg.SymbolDispatch},
		{"disabled and not running shows no glyph", false, false, ""},
		{"disabled but pass running still shows no glyph (enabled gates everything)", false, true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, sessionShape := range []string{"zero sessions", "one active session"} {
				t.Run(sessionShape, func(t *testing.T) {
					snap := &ipc.StateSnapshot{
						Timestamp: "2024-01-01T00:00:00Z",
						Dispatch: &watch.DispatchState{
							Enabled:     tt.enabled,
							Interval:    "5m",
							PassRunning: tt.passRunning,
						},
					}
					if sessionShape == "one active session" {
						snap.Windows = []ipc.WindowState{
							{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
						}
						snap.Summary = ipc.StatusSummary{Total: 1, Running: 1}
					}

					out := Format(snap, cfg)

					switch tt.wantGlyph {
					case cfg.SymbolDispatchRunning:
						if !strings.Contains(out.Text, cfg.SymbolDispatchRunning) {
							t.Errorf("expected running glyph %q in text, got %q", cfg.SymbolDispatchRunning, out.Text)
						}
						if strings.Contains(out.Text, cfg.SymbolDispatch) {
							t.Errorf("expected idle glyph %q NOT in text, got %q", cfg.SymbolDispatch, out.Text)
						}
					case cfg.SymbolDispatch:
						if !strings.Contains(out.Text, cfg.SymbolDispatch) {
							t.Errorf("expected idle glyph %q in text, got %q", cfg.SymbolDispatch, out.Text)
						}
						if strings.Contains(out.Text, cfg.SymbolDispatchRunning) {
							t.Errorf("expected running glyph %q NOT in text, got %q", cfg.SymbolDispatchRunning, out.Text)
						}
					default:
						if strings.Contains(out.Text, cfg.SymbolDispatch) {
							t.Errorf("expected no idle glyph %q in text, got %q", cfg.SymbolDispatch, out.Text)
						}
						if strings.Contains(out.Text, cfg.SymbolDispatchRunning) {
							t.Errorf("expected no running glyph %q in text, got %q", cfg.SymbolDispatchRunning, out.Text)
						}
					}
				})
			}
		})
	}
}

// TestFormat_DispatchEnabledNoRunsYet covers the "enabled, no runs yet" case:
// zero sessions, Dispatch.Enabled true, no LastRunAt. The glyph must appear
// in text, a tooltip line must be present, Alt must switch to
// "dispatch-only" (not "none", so Run() doesn't hide the module), and the
// "dispatch" key must be present in the marshaled JSON. Class stays "none"
// since session-status semantics are unchanged.
func TestFormat_DispatchEnabledNoRunsYet(t *testing.T) {
	cfg := testConfig()
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Dispatch: &watch.DispatchState{
			Enabled:  true,
			Interval: "5m",
		},
	}
	out := Format(snap, cfg)

	if !strings.Contains(out.Text, cfg.SymbolDispatch) {
		t.Errorf("expected dispatch glyph %q in text, got %q", cfg.SymbolDispatch, out.Text)
	}
	if !strings.Contains(out.Tooltip, "dispatch: on (5m)") {
		t.Errorf("expected tooltip to contain a dispatch summary line, got %q", out.Tooltip)
	}
	if out.Class != "none" {
		t.Errorf("expected class to remain 'none' (session-status semantics unchanged), got %q", out.Class)
	}
	if out.Alt != "dispatch-only" {
		t.Errorf("expected alt 'dispatch-only' for zero sessions with dispatch enabled, got %q", out.Alt)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"dispatch"`) {
		t.Errorf("expected 'dispatch' key present in marshaled JSON, got %s", data)
	}
}

// TestFormat_DispatchSuccessCounts covers a completed pass with dispatched
// and skipped counts: the tooltip line must reflect both counts and the
// last-run time (rendered HH:MM local time, per the plan's assumption).
func TestFormat_DispatchSuccessCounts(t *testing.T) {
	lastRun := "2024-01-01T12:04:00Z"
	parsed, err := time.Parse(time.RFC3339, lastRun)
	if err != nil {
		t.Fatalf("time.Parse: %v", err)
	}
	wantTime := parsed.Local().Format("15:04")

	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
		Dispatch: &watch.DispatchState{
			Enabled:        true,
			Interval:       "5m",
			LastRunAt:      lastRun,
			LastDispatched: 2,
			LastSkipped:    3,
		},
	}
	out := Format(snap, testConfig())

	wantLine := fmt.Sprintf("dispatch: on (5m) — last run %s, 2 dispatched / 3 skipped", wantTime)
	if !strings.Contains(out.Tooltip, wantLine) {
		t.Errorf("expected tooltip to contain %q, got %q", wantLine, out.Tooltip)
	}
}

// TestFormat_DispatchLastError covers a failed pass: the tooltip line must
// use the "last run failed: <err>" variant instead of the counts variant.
func TestFormat_DispatchLastError(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
		Dispatch: &watch.DispatchState{
			Enabled:   true,
			Interval:  "5m",
			LastRunAt: "2024-01-01T12:04:00Z",
			LastError: "boom",
		},
	}
	out := Format(snap, testConfig())

	wantLine := "dispatch: on (5m) — last run failed: boom"
	if !strings.Contains(out.Tooltip, wantLine) {
		t.Errorf("expected tooltip to contain %q, got %q", wantLine, out.Tooltip)
	}
}

// TestFormat_DispatchLine covers the dispatch tooltip summary line across a
// table of Interval values, including the reachable "enabled but Interval
// unset/empty" config state (per internal/dispatch/config.go, LoopEnabled can
// be explicitly true while DaemonInterval is unset). The empty-Interval case
// must omit the parenthetical rather than rendering "dispatch: on ()".
func TestFormat_DispatchLine(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		want     string
	}{
		{"non-empty interval renders parenthetical", "5m", "dispatch: on (5m)"},
		{"empty interval omits empty parens", "", "dispatch: on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &ipc.StateSnapshot{
				Timestamp: "2024-01-01T00:00:00Z",
				Dispatch: &watch.DispatchState{
					Enabled:  true,
					Interval: tt.interval,
				},
			}
			out := Format(snap, testConfig())

			if !strings.Contains(out.Tooltip, tt.want) {
				t.Errorf("expected tooltip to contain %q, got %q", tt.want, out.Tooltip)
			}
			if tt.interval == "" && strings.Contains(out.Tooltip, "()") {
				t.Errorf("expected no empty parens in tooltip, got %q", out.Tooltip)
			}
		})
	}
}

// TestFormat_DispatchDisabledOrAbsent_NoIndicator asserts byte-compatibility:
// whether Dispatch is nil (daemon never wired the loop) or present with
// Enabled false (loop configured off), no tooltip line, no glyph in text,
// and no "dispatch" key at all in the marshaled JSON — existing consumers
// predating #212 must see identical output.
func TestFormat_DispatchDisabledOrAbsent_NoIndicator(t *testing.T) {
	cfg := testConfig()
	tests := []struct {
		name     string
		dispatch *watch.DispatchState
	}{
		{"nil dispatch (daemon never wired the loop)", nil},
		{"present but disabled", &watch.DispatchState{Enabled: false, DaemonRunning: true, Interval: "5m"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &ipc.StateSnapshot{
				Timestamp: "2024-01-01T00:00:00Z",
				Windows: []ipc.WindowState{
					{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
				},
				Summary:  ipc.StatusSummary{Total: 1, Running: 1},
				Dispatch: tt.dispatch,
			}
			out := Format(snap, cfg)

			if strings.Contains(out.Tooltip, "dispatch:") {
				t.Errorf("expected no dispatch tooltip line, got %q", out.Tooltip)
			}
			if strings.Contains(out.Text, cfg.SymbolDispatch) {
				t.Errorf("expected no dispatch glyph in text, got %q", out.Text)
			}

			data, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if strings.Contains(string(data), `"dispatch"`) {
				t.Errorf("expected no 'dispatch' key in marshaled JSON (byte-compatible), got %s", data)
			}
		})
	}
}

// TestFormat_DispatchFieldPassthrough asserts that Dispatch passes through
// output verbatim (unrounded/untouched), mirroring the existing headroom
// passthrough test, so richer frontends (SwiftBar, QML) can render the raw
// fields however they like.
func TestFormat_DispatchFieldPassthrough(t *testing.T) {
	dispatch := &watch.DispatchState{
		Enabled:        true,
		DaemonRunning:  true,
		Interval:       "5m",
		PassRunning:    true,
		LastRunAt:      "2024-01-01T12:04:00Z",
		LastDispatched: 2,
		LastSkipped:    3,
	}
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Dispatch:  dispatch,
	}
	out := Format(snap, testConfig())

	if !reflect.DeepEqual(out.Dispatch, dispatch) {
		t.Errorf("expected output.Dispatch to pass through verbatim %#v, got %#v", dispatch, out.Dispatch)
	}
}

// TestFormat_HideBehavior_DispatchOnlyVsNoLoop is the hide-behavior
// assertion required by the plan: a dispatch-only snapshot (zero sessions,
// loop enabled) must NOT be hidden, while the plain zero-sessions-no-loop
// case must stay hidden. Today Run() hides on `out.Class == "none"`, which
// would wrongly hide the dispatch-only case too (Class stays "none" by
// design — session-status semantics are unchanged). This test locks in that
// Alt is the field that must distinguish the two cases, since Run()'s hide
// check must move from Class to Alt for #212 to work.
func TestFormat_HideBehavior_DispatchOnlyVsNoLoop(t *testing.T) {
	t.Run("dispatch-only zero sessions is not hidden", func(t *testing.T) {
		snap := &ipc.StateSnapshot{
			Timestamp: "2024-01-01T00:00:00Z",
			Dispatch:  &watch.DispatchState{Enabled: true, Interval: "5m"},
		}
		out := Format(snap, testConfig())

		if out.Class != "none" {
			t.Errorf("expected class to remain 'none', got %q", out.Class)
		}
		if out.Alt == "none" {
			t.Errorf("expected alt != 'none' (dispatch-only) so an alt-based hide check does not hide the module, got %q", out.Alt)
		}
	})

	t.Run("zero sessions with no dispatch loop stays hidden", func(t *testing.T) {
		snap := &ipc.StateSnapshot{Timestamp: "2024-01-01T00:00:00Z"}
		out := Format(snap, testConfig())

		if out.Class != "none" {
			t.Errorf("expected class 'none', got %q", out.Class)
		}
		if out.Alt != "none" {
			t.Errorf("expected alt 'none' so the module stays hidden, got %q", out.Alt)
		}
	})
}

// --- Pango-escaped tooltip text (#250) -------------------------------------

// TestEscapePango exercises the escapePango helper directly: it must escape
// &, <, > for Pango markup safety, leave plain strings untouched, and use
// single-pass replacer semantics — an already-escaped "&lt;" must become
// "&amp;lt;" (the introduced "&" is not itself re-escaped), which would fail
// under naive sequential string.Replace calls.
func TestEscapePango(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ampersand", "&", "&amp;"},
		{"less-than", "<", "&lt;"},
		{"greater-than", ">", "&gt;"},
		{"combined specials", "a<b>&c", "a&lt;b&gt;&amp;c"},
		{"no specials passes through unchanged", "plain text", "plain text"},
		{"double-escape guard: already-escaped input is not re-escaped", "&lt;", "&amp;lt;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapePango(tt.in)
			if got != tt.want {
				t.Errorf("escapePango(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestFormat_TooltipPangoEscaped covers window/task names and session names
// containing Pango markup-special characters: the tooltip must carry the
// escaped forms, never a raw "<", ">", or unescaped "&", and the JSON-marshaled
// payload must be Pango-safe end to end. Includes a paneless case (empty
// Session) so the non-prefixed tooltip line path is also covered.
func TestFormat_TooltipPangoEscaped(t *testing.T) {
	tests := []struct {
		name     string
		windows  []ipc.WindowState
		wantSubs []string // escaped substrings expected in out.Tooltip
	}{
		{
			name: "task name with markup specials, paned session",
			windows: []ipc.WindowState{
				{Session: "main<1>&2", WindowIndex: "0", TaskName: "fix <bug> & ship", Status: "running"},
			},
			wantSubs: []string{"fix &lt;bug&gt; &amp; ship", "main&lt;1&gt;&amp;2"},
		},
		{
			name: "window name with markup specials (manually named), paned session",
			windows: []ipc.WindowState{
				{Session: "main", WindowIndex: "0", WindowName: "a<b>&c", ManuallyNamed: true, Status: "running"},
			},
			wantSubs: []string{"a&lt;b&gt;&amp;c"},
		},
		{
			name: "agent fallback with markup specials, paneless session",
			windows: []ipc.WindowState{
				{Status: "running", Agent: "a<gent>&x"},
			},
			wantSubs: []string{"a&lt;gent&gt;&amp;x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &ipc.StateSnapshot{
				Timestamp: "2024-01-01T00:00:00Z",
				Windows:   tt.windows,
				Summary:   ipc.StatusSummary{Total: 1, Running: 1},
			}
			out := Format(snap, testConfig())

			for _, want := range tt.wantSubs {
				if !strings.Contains(out.Tooltip, want) {
					t.Errorf("expected tooltip to contain escaped %q, got %q", want, out.Tooltip)
				}
			}
			if strings.Contains(out.Tooltip, "<") || strings.Contains(out.Tooltip, ">") {
				t.Errorf("expected no raw '<' or '>' in tooltip, got %q", out.Tooltip)
			}
			if hasUnescapedAmpersand(out.Tooltip) {
				t.Errorf("expected no unescaped '&' in tooltip, got %q", out.Tooltip)
			}

			data, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			var decoded output
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			if strings.Contains(decoded.Tooltip, "<") || strings.Contains(decoded.Tooltip, ">") {
				t.Errorf("expected no raw '<' or '>' in marshaled tooltip, got %q", decoded.Tooltip)
			}
			if hasUnescapedAmpersand(decoded.Tooltip) {
				t.Errorf("expected no unescaped '&' in marshaled tooltip, got %q", decoded.Tooltip)
			}
		})
	}
}

// hasUnescapedAmpersand reports whether s contains a "&" that is not the
// start of one of the three Pango entities this package emits
// (&amp; &lt; &gt;).
func hasUnescapedAmpersand(s string) bool {
	stripped := strings.NewReplacer("&amp;", "", "&lt;", "", "&gt;", "").Replace(s)
	return strings.Contains(stripped, "&")
}

// TestFormat_DispatchLastErrorPangoEscaped covers DispatchState.LastError
// containing markup specials: the dispatch tooltip summary line built by
// formatDispatch must carry the escaped form. TestFormat_DispatchLastError
// (LastError "boom", no specials) is left unmodified and must still pass.
func TestFormat_DispatchLastErrorPangoEscaped(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "0", TaskName: "writing tests", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
		Dispatch: &watch.DispatchState{
			Enabled:   true,
			Interval:  "5m",
			LastRunAt: "2024-01-01T12:04:00Z",
			LastError: "boom <x> & y",
		},
	}
	out := Format(snap, testConfig())

	wantLine := "dispatch: on (5m) — last run failed: boom &lt;x&gt; &amp; y"
	if !strings.Contains(out.Tooltip, wantLine) {
		t.Errorf("expected tooltip to contain %q, got %q", wantLine, out.Tooltip)
	}
	if strings.Contains(out.Tooltip, "<x>") {
		t.Errorf("expected no raw markup in tooltip, got %q", out.Tooltip)
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

// --- Unified session line format (#405) ------------------------------------

// TestFormat_PanelessBlankNameAndAgent covers the exact bug that motivated
// #405: a sessionless (paneless) window before UserPromptSubmit has set a
// task name AND before the agent type is known (Agent == "") must never
// collapse to a blank/truncated tooltip line. It must render the unified
// "(no session) - (untitled) (unknown) (status)" shape.
func TestFormat_PanelessBlankNameAndAgent(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}
	out := Format(snap, testConfig())

	expected := "(no session) - (untitled) (unknown) (running)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
	}
}

// TestFormat_PanelessKnownAgentNoTaskName covers a sessionless window with a
// known agent but no task name yet: the name placeholder must still show,
// alongside the real agent (not folded into the name slot as today).
func TestFormat_PanelessKnownAgentNoTaskName(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Status: "idle", Agent: "claude"},
		},
		Summary: ipc.StatusSummary{Total: 1, Idle: 1},
	}
	out := Format(snap, testConfig())

	expected := "(no session) - (untitled) (claude) (idle)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
	}
}

// TestFormat_TmuxAllFieldsKnown covers the normal, fully-known tmux-backed
// case: session:index prefix, name, agent, and status must all be present
// in the unified order "session:index - name (agent) (status)".
func TestFormat_TmuxAllFieldsKnown(t *testing.T) {
	snap := &ipc.StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []ipc.WindowState{
			{Session: "feature-x", WindowIndex: "1", TaskName: "fix-login", Agent: "claude", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}
	out := Format(snap, testConfig())

	expected := "feature-x:1 - fix-login (claude) (running)"
	if out.Tooltip != expected {
		t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
	}
}

// TestFormat_TrailingStatusTokenRegexCompat is a regression guard for the
// gnome/plasma/macOS plugins (plugin/gnome/extension.js, plugin/plasma/contents/ui/main.qml,
// plugin/macos/cenci.5s.sh), which each parse a tooltip line by the
// end-anchored regex /\(([a-z-]+)\)\s*$/ to recover the status. Adding
// "(agent)" as an earlier parenthesized group must not push status out of
// the final position, across every status wording the plugins expect
// (running, idle, done, stopped, need-input, failed).
func TestFormat_TrailingStatusTokenRegexCompat(t *testing.T) {
	statusRe := regexp.MustCompile(`\(([a-z-]+)\)\s*$`)

	statuses := []string{"running", "idle", "done", "stopped", "need-input", "failed"}
	for _, st := range statuses {
		t.Run(st, func(t *testing.T) {
			snap := &ipc.StateSnapshot{
				Timestamp: "2024-01-01T00:00:00Z",
				Windows: []ipc.WindowState{
					{Session: "feature-x", WindowIndex: "0", TaskName: "fix-login", Agent: "claude", Status: st},
				},
			}
			out := Format(snap, testConfig())

			expected := fmt.Sprintf("feature-x:0 - fix-login (claude) (%s)", st)
			if out.Tooltip != expected {
				t.Errorf("expected tooltip %q, got %q", expected, out.Tooltip)
			}

			m := statusRe.FindStringSubmatch(out.Tooltip)
			if m == nil {
				t.Fatalf("tooltip %q does not end in a parenthesized status token (regex used by gnome/plasma/macos plugins)", out.Tooltip)
			}
			if m[1] != st {
				t.Errorf("expected trailing status token %q, got %q", st, m[1])
			}
		})
	}
}

// TestFormatSessionLine_PangoEscapedThroughSharedHelper drives a Session and
// TaskName containing Pango markup-special characters ("<", ">", "&")
// directly through FormatSessionLine with escapePango — the exact call the
// waybar tooltip path (Format, above) makes — locking in that the shared
// #405 helper never lets raw markup from session/task-name fields reach the
// Pango-rendered tooltip, independent of any future refactor of Format
// itself.
func TestFormatSessionLine_PangoEscapedThroughSharedHelper(t *testing.T) {
	w := ipc.WindowState{
		Session:     "feature<x>&y",
		WindowIndex: "2",
		TaskName:    "<b>&evil</b>",
		Agent:       "claude",
		Status:      "running",
	}

	got := FormatSessionLine(w, escapePango)

	expected := "feature&lt;x&gt;&amp;y:2 - &lt;b&gt;&amp;evil&lt;/b&gt; (claude) (running)"
	if got != expected {
		t.Errorf("FormatSessionLine() = %q, want %q", got, expected)
	}
	if strings.Contains(got, "<b>") || strings.Contains(got, "</b>") || strings.Contains(got, "feature<x>") {
		t.Errorf("FormatSessionLine() leaked raw markup into output: %q", got)
	}
}
