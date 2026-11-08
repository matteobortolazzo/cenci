package tmux

import (
	"math"
	"slices"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// TestRenderHeadroom_SingleAgent covers #171: a single agent-type's headroom
// is exposed as a session-wide (global) @cenci-headroom-<agent> tmux
// user variable, rounded to the nearest integer percent.
func TestRenderHeadroom_SingleAgent(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	f := New(config.Default(), mc)

	f.RenderHeadroom(map[string]float64{"claude": 0.73})

	got, ok := tmuxtest.FindOpt(mc.Opts, "@cenci-headroom-claude")
	if !ok {
		t.Fatalf("expected @cenci-headroom-claude to be set, opts: %+v", mc.Opts)
	}
	if got != "73" {
		t.Errorf("@cenci-headroom-claude = %q, want %q", got, "73")
	}
}

// TestRenderHeadroom_MultipleAgents covers multiple agent types set together
// in a single call.
func TestRenderHeadroom_MultipleAgents(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	f := New(config.Default(), mc)

	f.RenderHeadroom(map[string]float64{"claude": 0.73, "codex": 0.4})

	gotClaude, ok := tmuxtest.FindOpt(mc.Opts, "@cenci-headroom-claude")
	if !ok {
		t.Fatalf("expected @cenci-headroom-claude to be set, opts: %+v", mc.Opts)
	}
	if gotClaude != "73" {
		t.Errorf("@cenci-headroom-claude = %q, want %q", gotClaude, "73")
	}

	gotCodex, ok := tmuxtest.FindOpt(mc.Opts, "@cenci-headroom-codex")
	if !ok {
		t.Fatalf("expected @cenci-headroom-codex to be set, opts: %+v", mc.Opts)
	}
	if gotCodex != "40" {
		t.Errorf("@cenci-headroom-codex = %q, want %q", gotCodex, "40")
	}
}

// TestRenderHeadroom_RoundingAndClamping covers boundary rounding (Go's
// math.Round rounds half away from zero) and clamping to the 0..100 range
// for out-of-range inputs.
func TestRenderHeadroom_RoundingAndClamping(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  string
	}{
		{"rounds half up at .725 boundary", 0.725, "73"},
		{"rounds down below half", 0.724, "72"},
		{"clamps above 1.0 to 100", 1.5, "100"},
		{"clamps below 0.0 to 0", -0.2, "0"},
		{"exact zero", 0.0, "0"},
		{"exact one", 1.0, "100"},
		{"NaN coerces to 0", math.NaN(), "0"},
		{"positive infinity coerces to 100", math.Inf(1), "100"},
		{"negative infinity coerces to 0", math.Inf(-1), "0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc := &tmuxtest.MockClient{}
			f := New(config.Default(), mc)

			f.RenderHeadroom(map[string]float64{"claude": tc.value})

			got, ok := tmuxtest.FindOpt(mc.Opts, "@cenci-headroom-claude")
			if !ok {
				t.Fatalf("expected @cenci-headroom-claude to be set, opts: %+v", mc.Opts)
			}
			if got != tc.want {
				t.Errorf("RenderHeadroom(%v): @cenci-headroom-claude = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestRenderHeadroom_ClearsOnDisappear covers the clearing contract: when an
// agent that previously had headroom data is absent from a later call (e.g.
// the embedded dispatch loop was toggled off between daemon ticks), its
// global option must be cleared, not left stale.
func TestRenderHeadroom_ClearsOnDisappear(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	f := New(config.Default(), mc)

	f.RenderHeadroom(map[string]float64{"claude": 0.73, "codex": 0.4})
	f.RenderHeadroom(map[string]float64{"codex": 0.4})

	got, ok := tmuxtest.FindOpt(mc.Opts, "@cenci-headroom-claude")
	if !ok {
		t.Fatalf("expected @cenci-headroom-claude to have a recorded (clearing) SetOption call, opts: %+v", mc.Opts)
	}
	if got != "" {
		t.Errorf("@cenci-headroom-claude = %q, want cleared (\"\")", got)
	}

	// codex must remain set, untouched by the clear of claude.
	gotCodex, ok := tmuxtest.FindOpt(mc.Opts, "@cenci-headroom-codex")
	if !ok {
		t.Fatalf("expected @cenci-headroom-codex to be set, opts: %+v", mc.Opts)
	}
	if gotCodex != "40" {
		t.Errorf("@cenci-headroom-codex = %q, want %q", gotCodex, "40")
	}
}

// TestRenderHeadroom_EmptyMapOnFirstCall covers the no-data case: no vars
// are set and no panics occur when nothing has ever been tracked.
func TestRenderHeadroom_EmptyMapOnFirstCall(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	f := New(config.Default(), mc)

	f.RenderHeadroom(map[string]float64{})

	if len(mc.Opts) != 0 {
		t.Errorf("expected no SetOption calls on empty map, got %+v", mc.Opts)
	}
}

// TestCleanup_ClearsAllTrackedHeadroomVars mirrors the existing
// symbol/style cleanup behavior (restoreWindowIndicators clears
// @cenci-style/@cenci-symbol): Cleanup must clear every
// @cenci-headroom-<agent> variable it has ever set.
func TestCleanup_ClearsAllTrackedHeadroomVars(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	f := New(config.Default(), mc)

	f.RenderHeadroom(map[string]float64{"claude": 0.73, "codex": 0.4})

	f.Cleanup(nil)

	gotClaude, ok := tmuxtest.FindOpt(mc.Opts, "@cenci-headroom-claude")
	if !ok || gotClaude != "" {
		t.Errorf("@cenci-headroom-claude after Cleanup = (%q, %v), want cleared", gotClaude, ok)
	}
	gotCodex, ok := tmuxtest.FindOpt(mc.Opts, "@cenci-headroom-codex")
	if !ok || gotCodex != "" {
		t.Errorf("@cenci-headroom-codex after Cleanup = (%q, %v), want cleared", gotCodex, ok)
	}
}

// -- per-socket headroom (#1429) ---------------------------------------------

const headroomClaude = "@cenci-headroom-claude"

// optCalls counts the SetOption calls for key on socket.
func optCalls(opts []tmuxtest.OptCall, socket, key string) int {
	n := 0
	for _, o := range opts {
		if o.Socket == socket && o.Key == key {
			n++
		}
	}
	return n
}

// With no window on any other server, headroom is set, left alone when
// unchanged, updated and cleared on the default server only, exactly as before
// sockets existed.
func TestRenderHeadroom_DefaultServerExactlyAsToday(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	f := New(config.Default(), mc)

	f.RenderHeadroom(map[string]float64{"claude": 0.73})
	f.RenderHeadroom(map[string]float64{"claude": 0.73})
	f.RenderHeadroom(map[string]float64{"claude": 0.5})
	f.RenderHeadroom(map[string]float64{})

	want := []tmuxtest.OptCall{
		{Socket: "", Key: headroomClaude, Value: "73"},
		{Socket: "", Key: headroomClaude, Value: "50"},
		{Socket: "", Key: headroomClaude, Value: ""},
	}
	if !slices.Equal(mc.Opts, want) {
		t.Errorf("SetOption calls = %+v, want %+v", mc.Opts, want)
	}
}

// Each non-empty socket hosting a tracked window gets the variable too: one set
// per socket per change (two windows on A are still one set), and an unchanged
// value makes no call.
func TestRenderHeadroom_AlsoSetsEachHostingSocket(t *testing.T) {
	mc := twoSocketMock()
	mc.PanesBySocket[sockA] = append(mc.PanesBySocket[sockA], socketPane("bash", "1", "%6", "alpha two"))
	f := New(config.Default(), mc)
	touch(f, runningOn("a1", "%5", sockA))
	touch(f, runningOn("a2", "%6", sockA))
	touch(f, runningOn("b", "%5", sockB))

	f.RenderHeadroom(map[string]float64{"claude": 0.73})

	for _, socket := range []string{"", sockA, sockB} {
		if got, ok := tmuxtest.FindSocketOpt(mc.Opts, socket, headroomClaude); !ok || got != "73" {
			t.Errorf("%s on %q = (%q, %v), want 73", headroomClaude, socket, got, ok)
		}
		if n := optCalls(mc.Opts, socket, headroomClaude); n != 1 {
			t.Errorf("%d sets on %q, want exactly 1", n, socket)
		}
	}

	calls := len(mc.Opts)
	f.RenderHeadroom(map[string]float64{"claude": 0.73})
	if len(mc.Opts) != calls {
		t.Errorf("an unchanged value made %d more SetOption calls: %+v", len(mc.Opts)-calls, mc.Opts[calls:])
	}

	f.RenderHeadroom(map[string]float64{"claude": 0.5})
	for _, socket := range []string{"", sockA, sockB} {
		if got, _ := tmuxtest.FindSocketOpt(mc.Opts, socket, headroomClaude); got != "50" {
			t.Errorf("%s on %q = %q after the change, want 50", headroomClaude, socket, got)
		}
		if n := optCalls(mc.Opts, socket, headroomClaude); n != 2 {
			t.Errorf("%d sets on %q after one change, want 2", n, socket)
		}
	}
}

// A window first tracked on a server the frontend has not seen gets the headroom
// the frontend last rendered right away, not at the next reconciler tick.
func TestTrackingOnNewSocketPushesCurrentHeadroom(t *testing.T) {
	mc := twoSocketMock()
	mc.PanesBySocket[sockA] = append(mc.PanesBySocket[sockA], socketPane("bash", "1", "%6", "alpha two"))
	f := New(config.Default(), mc)
	f.RenderHeadroom(map[string]float64{"claude": 0.73})
	if n := optCalls(mc.Opts, sockA, headroomClaude); n != 0 {
		t.Fatalf("%d sets on A before any window is tracked there, want 0", n)
	}

	touch(f, runningOn("a1", "%5", sockA))

	if got, ok := tmuxtest.FindSocketOpt(mc.Opts, sockA, headroomClaude); !ok || got != "73" {
		t.Errorf("%s on A right after tracking = (%q, %v), want the current 73 pushed immediately", headroomClaude, got, ok)
	}
	touch(f, runningOn("a2", "%6", sockA))
	if n := optCalls(mc.Opts, sockA, headroomClaude); n != 1 {
		t.Errorf("%d sets on A after a second window there, want 1: only the first window on a socket pushes", n)
	}
	touch(f, runningOn("b", "%5", sockB))
	if got, ok := tmuxtest.FindSocketOpt(mc.Opts, sockB, headroomClaude); !ok || got != "73" {
		t.Errorf("%s on B right after tracking = (%q, %v), want 73", headroomClaude, got, ok)
	}
}

// The next RenderHeadroom clears the variable on a server that no longer hosts a
// tracked window and then stops tracking that server.
func TestRenderHeadroom_ClearsASocketNoLongerHosting(t *testing.T) {
	mc := twoSocketMock()
	f := New(config.Default(), mc)
	a := runningOn("a", "%5", sockA)
	touch(f, a)
	f.RenderHeadroom(map[string]float64{"claude": 0.73})
	if got, _ := tmuxtest.FindSocketOpt(mc.Opts, sockA, headroomClaude); got != "73" {
		t.Fatalf("%s on A = %q, want 73 while A hosts a window", headroomClaude, got)
	}

	f.OnSessionEnd(a)
	f.RenderHeadroom(map[string]float64{"claude": 0.73})

	if got, ok := tmuxtest.FindSocketOpt(mc.Opts, sockA, headroomClaude); !ok || got != "" {
		t.Errorf("%s on A after it stopped hosting = (%q, %v), want cleared", headroomClaude, got, ok)
	}
	calls := optCalls(mc.Opts, sockA, headroomClaude)
	f.RenderHeadroom(map[string]float64{"claude": 0.73})
	if n := optCalls(mc.Opts, sockA, headroomClaude); n != calls {
		t.Errorf("%d more calls on A after the clear, want none: A is no longer tracked", n-calls)
	}
	if got, _ := tmuxtest.FindSocketOpt(mc.Opts, "", headroomClaude); got != "73" {
		t.Errorf("%s on the default server = %q, want 73 untouched", headroomClaude, got)
	}
}

// Cleanup clears every headroom variable on every server it was set on.
func TestCleanup_ClearsHeadroomOnEverySocket(t *testing.T) {
	mc := twoSocketMock()
	f := New(config.Default(), mc)
	touch(f, runningOn("a", "%5", sockA))
	touch(f, runningOn("b", "%5", sockB))
	f.RenderHeadroom(map[string]float64{"claude": 0.73, "codex": 0.4})

	f.Cleanup(nil)

	for _, socket := range []string{"", sockA, sockB} {
		for _, key := range []string{headroomClaude, "@cenci-headroom-codex"} {
			if got, ok := tmuxtest.FindSocketOpt(mc.Opts, socket, key); !ok || got != "" {
				t.Errorf("%s on %q after Cleanup = (%q, %v), want cleared", key, socket, got, ok)
			}
		}
	}
}
