package daemon

import (
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// -- socketTierWarning (ticket #1142 AC #6) ----------------------------------
//
// The daemon's startup warning used to key off unset $XDG_RUNTIME_DIR. Now
// that SocketDir() resolves through a three-tier chain, the warning must key
// off which tier actually won: only a resolution that landed on the /tmp
// tier is worth flagging (the same signal a lower-security "no XDG runtime
// dir" fallback used to represent), and it must name the resolver's own
// Reason rather than re-deriving one. socketTierWarning is a pure function
// (no I/O) so this is testable without running Run().
//
// NOTE (red phase): socketTierWarning does not exist yet (lands in
// daemon.go, a later phase) -- every test below fails to COMPILE until then.
// That is the intended red-phase state.

func TestSocketTierWarning_OverrideTier_NoWarning(t *testing.T) {
	res := watch.SocketDirResolution{Dir: "/some/override/dir", Tier: watch.TierOverride}
	got := socketTierWarning(res, true)
	if got != "" {
		t.Errorf("socketTierWarning(override) = %q, want empty string", got)
	}
}

func TestSocketTierWarning_StateTier_NoWarning(t *testing.T) {
	res := watch.SocketDirResolution{Dir: "/some/state/dir", Tier: watch.TierState}
	got := socketTierWarning(res, true)
	if got != "" {
		t.Errorf("socketTierWarning(state) = %q, want empty string", got)
	}
}

func TestSocketTierWarning_TmpTier_WarnsWithReason(t *testing.T) {
	res := watch.SocketDirResolution{
		Dir:    "/tmp/cenci-1000/cenci",
		Tier:   watch.TierTmp,
		Reason: "XDG_STATE_HOME and HOME are both unresolvable",
	}
	got := socketTierWarning(res, true)
	if got == "" {
		t.Fatal("socketTierWarning(tmp) = \"\", want a non-empty warning")
	}
	if !strings.Contains(got, res.Reason) {
		t.Errorf("socketTierWarning(tmp) = %q, want it to contain the reason %q", got, res.Reason)
	}
}

// TestSocketTierWarning_TmpTierWithCustomSocketPaths_NoWarning covers the
// existing usingDefaults guard: an operator who explicitly passed
// -socket/-event-socket has opted out of the default path entirely, so
// landing on the /tmp tier is expected and must not warn.
func TestSocketTierWarning_TmpTierWithCustomSocketPaths_NoWarning(t *testing.T) {
	res := watch.SocketDirResolution{
		Dir:    "/tmp/cenci-1000/cenci",
		Tier:   watch.TierTmp,
		Reason: "XDG_STATE_HOME and HOME are both unresolvable",
	}
	got := socketTierWarning(res, false)
	if got != "" {
		t.Errorf("socketTierWarning(tmp, usingDefaults=false) = %q, want empty string", got)
	}
}
