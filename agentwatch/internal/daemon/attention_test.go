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
