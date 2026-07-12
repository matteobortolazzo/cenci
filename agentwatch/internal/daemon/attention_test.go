package daemon

import (
	"testing"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/tmux/tmuxtest"
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
