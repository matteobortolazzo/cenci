package daemon

import (
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// HandleArmRequestWith builds a test daemon whose capability predicate and arm
// spawn seam are the given ones (a nil capable stays nil, i.e. refuses) and runs
// req through handleArmRequest. It exists so the external daemon_test package
// can wire the real run.BuiltinCapability, which this package cannot import
// (internal/run imports internal/daemon).
func HandleArmRequestWith(capable func(agent, workflow string) bool, spawn func(ipc.ArmRequest) ipc.ArmResponse, req ipc.ArmRequest) ipc.ArmResponse {
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.armCapable = capable
	d.armSpawn = spawn
	return d.handleArmRequest(req)
}
