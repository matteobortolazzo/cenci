package run

import (
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/ipc"
)

// daemonReadyTimeout bounds how long ensureDaemonRunning waits for the event
// socket to come up after spawning the daemon. It is a bounded wait, not a
// fire-and-forget background start like the SessionStart bootstrap hook,
// because agent-sand's socket mount is a one-shot check made at container
// launch (#139): a window spawned before the socket exists never gets
// agentwatch wired in for the container's whole lifetime, so `run` must give
// the daemon a moment to come up before creating the window.
var daemonReadyTimeout = 3 * time.Second

// daemonPollInterval is how often ensureDaemonRunning re-probes the event
// socket while waiting for a freshly spawned daemon to come up.
var daemonPollInterval = 50 * time.Millisecond

// spawnDaemon launches `<self> daemon` detached from the current process
// group so it outlives this short-lived `run` invocation, mirroring the
// plugin's `nohup agentwatch daemon &` bootstrap. Overridable in tests to
// avoid spawning a real process.
var spawnDaemon = func() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

// ensureDaemonRunning starts the agentwatch daemon if the event socket has no
// live listener, then polls briefly for it to come up. It never reports an
// error: launching the window must proceed even when the daemon can't be
// started (e.g. the binary isn't self-resolvable). The daemon's own
// already-running guard (ipc.ErrAlreadyRunning) makes a redundant start here a
// harmless no-op.
func ensureDaemonRunning() {
	if daemonAlive() {
		return
	}
	spawnDaemon()

	deadline := time.Now().Add(daemonReadyTimeout)
	for time.Now().Before(deadline) {
		if daemonAlive() {
			return
		}
		time.Sleep(daemonPollInterval)
	}
}

// daemonAlive reports whether a live daemon is listening on the default event
// socket, mirroring the aliveness probe safeListen uses internally (dial
// rather than trust a stale socket file left behind by a crashed process).
func daemonAlive() bool {
	conn, err := net.DialTimeout("unix", ipc.DefaultEventSocketPath(), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
