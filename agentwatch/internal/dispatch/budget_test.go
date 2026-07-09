package dispatch

import "testing"

func TestFloorProviderUnlimitedByDefault(t *testing.T) {
	p := FloorProvider{}
	b := p.Budget("claude")
	if !b.Unlimited {
		t.Errorf("expected unlimited budget for an unpinned agent, got %+v", b)
	}
}

func TestFloorProviderPinsToZero(t *testing.T) {
	p := FloorProvider{Floors: map[string]float64{"claude": 0}}
	b := p.Budget("claude")
	if b.Unlimited {
		t.Error("a pinned agent must not be unlimited")
	}
	if b.Remaining != 0 {
		t.Errorf("Remaining = %v, want 0", b.Remaining)
	}
}

func TestFloorProviderPositiveFloor(t *testing.T) {
	p := FloorProvider{Floors: map[string]float64{"claude": 5}}
	b := p.Budget("claude")
	if b.Unlimited || b.Remaining != 5 {
		t.Errorf("Budget = %+v, want {Remaining:5}", b)
	}
	// An agent absent from the floor map stays unlimited.
	if !p.Budget("codex").Unlimited {
		t.Error("unpinned agent should remain unlimited")
	}
}
