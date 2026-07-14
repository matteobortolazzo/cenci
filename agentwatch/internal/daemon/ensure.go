package daemon

import (
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/ipc"
)

var readyTimeout = 3 * time.Second
var pollInterval = 50 * time.Millisecond

var spawn = func() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

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
	conn, err := net.DialTimeout("unix", ipc.DefaultEventSocketPath(), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
