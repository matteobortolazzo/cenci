package daemon

import (
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/config"
	tmuxfe "github.com/matteobortolazzo/agent-stack/agentwatch/internal/frontend/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/tmux/tmuxtest"
)

func testConfig() config.Config {
	cfg := config.Default()
	cfg.SweepInterval = 10 * time.Millisecond
	return cfg
}

// findWindowOpt returns the value of the last SetWindowOption call matching target and key.
func findWindowOpt(opts []tmuxtest.WindowOptCall, target, key string) (string, bool) {
	return tmuxtest.FindWindowOpt(opts, target, key)
}

// lastRename returns the last rename call for a given target.
func lastRename(renames []tmuxtest.RenameCall, target string) (string, bool) {
	return tmuxtest.LastRename(renames, target)
}

// newTestDaemon creates a daemon wired to a real tmux frontend over a mock
// tmux client — an integration setup across the frontend seam. Tests call
// handleEvent/runSweep directly for synchronous, deterministic behavior.
func newTestDaemon(mc *tmuxtest.MockClient) *Daemon {
	ch := make(chan ipc.HookEvent, 16)
	cfg := testConfig()
	return newDaemon(cfg, tmuxfe.New(cfg, mc), ch)
}
