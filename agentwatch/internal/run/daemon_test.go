package run

import (
	"net"
	"testing"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/ipc"
)

// tempSocketDir points the default socket resolution at a fresh, private temp
// dir for the duration of the test, so daemonAlive/ensureDaemonRunning never
// touch the real runtime socket.
func tempSocketDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

func TestDaemonAliveFalseWhenNothingListening(t *testing.T) {
	tempSocketDir(t)
	if daemonAlive() {
		t.Error("expected daemonAlive to be false with no listener")
	}
}

func TestDaemonAliveTrueWithLiveListener(t *testing.T) {
	tempSocketDir(t)
	ln, err := net.Listen("unix", ipc.DefaultEventSocketPath())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if !daemonAlive() {
		t.Error("expected daemonAlive to be true with a live listener")
	}
}

func TestEnsureDaemonRunningSkipsSpawnWhenAlive(t *testing.T) {
	tempSocketDir(t)
	ln, err := net.Listen("unix", ipc.DefaultEventSocketPath())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	spawned := false
	restore := spawnDaemon
	spawnDaemon = func() { spawned = true }
	defer func() { spawnDaemon = restore }()

	ensureDaemonRunning()

	if spawned {
		t.Error("expected spawnDaemon not to be called when the daemon is already alive")
	}
}

func TestEnsureDaemonRunningWaitsForSpawnedSocket(t *testing.T) {
	tempSocketDir(t)

	restoreTimeout, restoreInterval := daemonReadyTimeout, daemonPollInterval
	daemonReadyTimeout = 2 * time.Second
	daemonPollInterval = 10 * time.Millisecond
	defer func() { daemonReadyTimeout, daemonPollInterval = restoreTimeout, restoreInterval }()

	restoreSpawn := spawnDaemon
	defer func() { spawnDaemon = restoreSpawn }()
	spawnDaemon = func() {
		// Simulate the daemon coming up shortly after being spawned.
		go func() {
			time.Sleep(30 * time.Millisecond)
			ln, err := net.Listen("unix", ipc.DefaultEventSocketPath())
			if err != nil {
				return
			}
			defer func() { _ = ln.Close() }()
			time.Sleep(1 * time.Second)
		}()
	}

	start := time.Now()
	ensureDaemonRunning()
	elapsed := time.Since(start)

	if !daemonAlive() {
		t.Error("expected the daemon to be alive after ensureDaemonRunning returns")
	}
	if elapsed >= daemonReadyTimeout {
		t.Errorf("ensureDaemonRunning took %v, expected it to return once the socket came up, well under the %v timeout", elapsed, daemonReadyTimeout)
	}
}

func TestEnsureDaemonRunningGivesUpAfterTimeout(t *testing.T) {
	tempSocketDir(t)

	restoreTimeout, restoreInterval := daemonReadyTimeout, daemonPollInterval
	daemonReadyTimeout = 100 * time.Millisecond
	daemonPollInterval = 10 * time.Millisecond
	defer func() { daemonReadyTimeout, daemonPollInterval = restoreTimeout, restoreInterval }()

	restoreSpawn := spawnDaemon
	defer func() { spawnDaemon = restoreSpawn }()
	spawnDaemon = func() {} // never comes up

	done := make(chan struct{})
	go func() {
		ensureDaemonRunning()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("ensureDaemonRunning did not return after the ready timeout elapsed")
	}
}
