package daemon_test

import (
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// The real run.BuiltinCapability query wired through the arm path (#1215):
// claude and codex have built-in address-review, babysit-attention and
// ci-repair, so each reaches the spawn seam exactly once; opencode has none (and
// is disabled), so it never does. This lives in an external test package because
// internal/run imports internal/daemon.
func TestHandleArmRequest_RealBuiltinCapabilityQuery(t *testing.T) {
	arm := func(agent string) (resp ipc.ArmResponse, spawnCalls int) {
		spawn := func(ipc.ArmRequest) ipc.ArmResponse {
			spawnCalls++
			return ipc.ArmResponse{OK: true}
		}
		req := ipc.ArmRequest{PR: "42", Repo: "o/r", Agent: agent, Interval: 15 * time.Minute, TmuxPane: "%3"}
		return daemon.HandleArmRequestWith(run.BuiltinCapability, spawn, req), spawnCalls
	}

	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent+" reaches spawn exactly once", func(t *testing.T) {
			resp, calls := arm(agent)
			if !resp.OK || calls != 1 {
				t.Fatalf("resp = %+v, spawn calls = %d, want an ack and exactly 1 spawn", resp, calls)
			}
		})
	}

	t.Run("opencode never reaches spawn", func(t *testing.T) {
		resp, calls := arm("opencode")
		if resp.OK || resp.Reason == "" || calls != 0 {
			t.Fatalf("resp = %+v, spawn calls = %d, want a nack with a reason and 0 spawns", resp, calls)
		}
	})
}
