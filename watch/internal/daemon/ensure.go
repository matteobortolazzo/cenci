package daemon

import (
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/ipc"
)

var readyTimeout = 3 * time.Second
var pollInterval = 50 * time.Millisecond

// Spawn starts a new detached `agentwatch daemon start` process in the
// background (Setsid, so it survives the parent's exit). `daemon start` is
// the canonical form spawned both here (via the spawn var, on-demand startup)
// and by `agentwatch daemon restart` (internal/main.go), so every
// daemon-spawning code path launches the process the same way. It is silent
// on failure — callers that need to know whether the daemon became reachable
// should poll Alive afterward.
func Spawn() {
	// Refuse to spawn from inside a test binary: os.Executable() would be
	// the `*.test` binary, and re-invoking it runs the whole test suite —
	// every unstubbed EnsureRunning call then spawns another copy, fork-
	// bombing the machine. Unit tests stub the `spawn` var instead;
	// black-box tests exercise the real installed binary, where
	// testing.Testing() is false.
	if testing.Testing() {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "daemon", "start")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

// spawn is a package var (defaulting to Spawn) so tests can stub it without a
// real "agentwatch" binary spawning a background process.
var spawn = Spawn

var ensureMu sync.Mutex

// EnsureRunning starts the daemon when the default event socket has no live
// listener and waits briefly for it to become reachable. It is deliberately
// silent and bounded so hooks and launchers remain non-fatal when startup is
// impossible. Concurrent callers serialize the probe/start sequence. Inside an
// agent-sand container (AGENT_SAND=1) it returns immediately without ever
// spawning: a container-local daemon controls nothing on the host and would
// only mask real wiring failures (#195, #202).
func EnsureRunning() {
	if os.Getenv("AGENT_SAND") == "1" {
		return
	}

	ensureMu.Lock()
	defer ensureMu.Unlock()

	if alive() {
		return
	}
	spawn()

	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		if alive() {
			return
		}
		time.Sleep(pollInterval)
	}
}

func alive() bool {
	return Alive(ipc.DefaultEventSocketPath())
}

// Alive reports whether a daemon is listening on the given event socket
// path. It is exported for `daemon stop`/`daemon status`/`daemon restart`
// (internal/main.go), which need to probe liveness against a caller-supplied
// path the same way EnsureRunning's on-demand startup does.
func Alive(eventSocketPath string) bool {
	conn, err := net.DialTimeout("unix", eventSocketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
