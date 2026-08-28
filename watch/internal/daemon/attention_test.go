package daemon

import (
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

func TestBuildSnapshot_AttentionOverlayAppendsFailed(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)

	// One real running paneless session.
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	// A reconciler-surfaced failure overlay.
	d.attention = []ipc.WindowState{
		{WindowName: "42-fix-thing", Status: "failed"},
	}

	snap := d.buildSnapshot()

	if snap.Summary.Total != 2 {
		t.Errorf("Summary.Total = %d, want 2", snap.Summary.Total)
	}
	if snap.Summary.Running != 1 {
		t.Errorf("Summary.Running = %d, want 1", snap.Summary.Running)
	}
	if snap.Summary.Failed != 1 {
		t.Errorf("Summary.Failed = %d, want 1", snap.Summary.Failed)
	}

	var found bool
	for _, w := range snap.Windows {
		if w.WindowName == "42-fix-thing" && w.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected synthetic failed window in snapshot, got %+v", snap.Windows)
	}
}

// TestBuildSnapshot_AttentionOverlayAppendsEscalated (#826) covers the
// status switch's "escalated" branch: an attention entry with status
// "escalated" must count into Summary.Escalated, never Summary.Failed.
func TestBuildSnapshot_AttentionOverlayAppendsEscalated(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	d.attention = []ipc.WindowState{
		{WindowName: "42-implement", Status: "escalated"},
	}

	snap := d.buildSnapshot()

	if snap.Summary.Total != 2 {
		t.Errorf("Summary.Total = %d, want 2", snap.Summary.Total)
	}
	if snap.Summary.Escalated != 1 {
		t.Errorf("Summary.Escalated = %d, want 1", snap.Summary.Escalated)
	}
	if snap.Summary.Failed != 0 {
		t.Errorf("Summary.Failed = %d, want 0 (an escalated entry must never count as Failed)", snap.Summary.Failed)
	}

	var found bool
	for _, w := range snap.Windows {
		if w.WindowName == "42-implement" && w.Status == "escalated" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected synthetic escalated window in snapshot, got %+v", snap.Windows)
	}
}

// TestBuildSnapshot_AttentionOverlayMixedFailedAndEscalated proves both
// statuses count independently within the same overlay -- a regression
// collapsing the switch into a single counter would pass a failed-only or
// escalated-only test but fail this one.
func TestBuildSnapshot_AttentionOverlayMixedFailedAndEscalated(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)

	d.attention = []ipc.WindowState{
		{WindowName: "42-fix-thing", Status: "failed"},
		{WindowName: "7-implement", Status: "escalated"},
	}

	snap := d.buildSnapshot()

	if snap.Summary.Failed != 1 {
		t.Errorf("Summary.Failed = %d, want 1", snap.Summary.Failed)
	}
	if snap.Summary.Escalated != 1 {
		t.Errorf("Summary.Escalated = %d, want 1", snap.Summary.Escalated)
	}
}

// TestBuildSnapshot_AttentionOverlay_UnrecognizedStatusFallsBackToFailed
// pins the match-miss default (watch's Critical Rules: keep the original
// visible fallback for every other case and add a regression test for the
// non-matching case). No real producer emits an attention status other than
// "failed"/"escalated" today, but the switch's default branch must still
// resolve to the pre-#826 behavior (Failed++), not silently drop the count.
func TestBuildSnapshot_AttentionOverlay_UnrecognizedStatusFallsBackToFailed(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)

	d.attention = []ipc.WindowState{
		{WindowName: "99-implement", Status: "bogus-unrecognized-status"},
	}

	snap := d.buildSnapshot()

	if snap.Summary.Failed != 1 {
		t.Errorf("Summary.Failed = %d, want 1 (unrecognized attention status must default-fall-back to Failed)", snap.Summary.Failed)
	}
	if snap.Summary.Escalated != 0 {
		t.Errorf("Summary.Escalated = %d, want 0", snap.Summary.Escalated)
	}
}

func TestBuildSnapshot_NoAttentionIsUnchanged(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	snap := d.buildSnapshot()
	if snap.Summary.Total != 1 || snap.Summary.Failed != 0 {
		t.Errorf("expected total=1 failed=0 with no overlay, got %+v", snap.Summary)
	}
}

// TestBuildSnapshot_HeadroomPresentWhenSet covers #169: when the embedded
// dispatch loop has populated d.headroom (via the attention channel), the
// snapshot must carry it verbatim.
func TestBuildSnapshot_HeadroomPresentWhenSet(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	d.headroom = map[string]float64{"claude": 0.75, "codex": 1.0}

	snap := d.buildSnapshot()

	if len(snap.Headroom) != 2 {
		t.Fatalf("expected 2 agents in Headroom, got %v", snap.Headroom)
	}
	if snap.Headroom["claude"] != 0.75 {
		t.Errorf("claude headroom = %v, want 0.75", snap.Headroom["claude"])
	}
	if snap.Headroom["codex"] != 1.0 {
		t.Errorf("codex headroom = %v, want 1.0", snap.Headroom["codex"])
	}
}

// TestBuildSnapshot_HeadroomAbsentWhenUnset covers #169's "no data" contract:
// when the embedded dispatch loop is disabled (or AgentLimits unconfigured),
// d.headroom is never set, and the snapshot's Headroom field must be empty so
// downstream frontends render exactly as today.
func TestBuildSnapshot_HeadroomAbsentWhenUnset(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	snap := d.buildSnapshot()

	if len(snap.Headroom) != 0 {
		t.Errorf("expected no Headroom data when embedded loop disabled, got %v", snap.Headroom)
	}
}

// TestBuildSnapshot_DispatchOverlayPresentWhenSet covers #220: when the
// embedded dispatch loop has populated d.dispatch (via the attention
// channel's Dispatch field), the snapshot must carry it through verbatim.
func TestBuildSnapshot_DispatchOverlayPresentWhenSet(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	d.dispatch = &watch.DispatchState{Enabled: true, DaemonRunning: true, LastDispatched: 3}

	snap := d.buildSnapshot()

	if snap.Dispatch == nil {
		t.Fatal("expected snap.Dispatch to be set, got nil")
		return
	}
	if !snap.Dispatch.Enabled {
		t.Errorf("snap.Dispatch.Enabled = false, want true")
	}
	if !snap.Dispatch.DaemonRunning {
		t.Errorf("snap.Dispatch.DaemonRunning = false, want true")
	}
	if snap.Dispatch.LastDispatched != 3 {
		t.Errorf("snap.Dispatch.LastDispatched = %d, want 3", snap.Dispatch.LastDispatched)
	}
}

// TestBuildSnapshot_DispatchAbsentWhenUnset covers #220's "no data yet"
// contract: before the embedded dispatch loop's first publish, d.dispatch is
// nil, and the snapshot's Dispatch field must stay nil so the "dispatch" key
// is omitted from early NDJSON lines (json:"dispatch,omitempty").
func TestBuildSnapshot_DispatchAbsentWhenUnset(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1"})

	snap := d.buildSnapshot()

	if snap.Dispatch != nil {
		t.Errorf("expected snap.Dispatch to be nil before the first publish, got %+v", snap.Dispatch)
	}
}
