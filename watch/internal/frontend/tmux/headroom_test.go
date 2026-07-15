package tmux

import (
	"math"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v4/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/tmux/tmuxtest"
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
