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

// TestDaemon is the slice of a test daemon the external daemon_test package may
// drive: it feeds hook events in and reads the broadcast snapshot out. It exists
// for the same import cycle as HandleArmRequestWith: the join-key proof wires
// the real run.Run, which this package cannot import.
type TestDaemon struct{ d *Daemon }

// NewSocketTestDaemon builds a test daemon over mc (a real tmux frontend over the
// mock) that keeps the real tmux socket ingest checks: the trusted directory
// resolves from $TMUX_TMPDIR and a hook event's tmux_socket must exist as a Unix
// socket there, exactly as in production.
func NewSocketTestDaemon(mc *tmuxtest.MockClient) *TestDaemon {
	return &TestDaemon{d: newTestDaemon(mc)}
}

// HandleEvent runs one hook event through the daemon's synchronous ingest path.
func (t *TestDaemon) HandleEvent(event ipc.HookEvent) { t.d.handleEvent(event) }

// Snapshot returns the state snapshot the daemon would broadcast now.
func (t *TestDaemon) Snapshot() ipc.StateSnapshot { return t.d.buildSnapshot() }
