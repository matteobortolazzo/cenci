package daemon

import (
	"net"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v4/internal/ipc"
)

func useTempSocketDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

func TestAliveFalseWhenNothingListening(t *testing.T) {
	useTempSocketDir(t)
	if alive() {
		t.Error("expected alive to be false with no listener")
	}
}

func TestAliveTrueWithLiveListener(t *testing.T) {
	useTempSocketDir(t)
	ln, err := net.Listen("unix", ipc.DefaultEventSocketPath())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if !alive() {
		t.Error("expected alive to be true with a live listener")
	}
}

func TestEnsureRunningSkipsSpawnWhenAlive(t *testing.T) {
	useTempSocketDir(t)
	t.Setenv("CENCI_SANDBOX", "")
	ln, err := net.Listen("unix", ipc.DefaultEventSocketPath())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	spawned := false
	restore := spawn
	spawn = func() { spawned = true }
	defer func() { spawn = restore }()

	EnsureRunning()

	if spawned {
		t.Error("expected daemon not to be spawned when already alive")
	}
}

func TestEnsureRunningWaitsForSpawnedSocket(t *testing.T) {
	useTempSocketDir(t)
	t.Setenv("CENCI_SANDBOX", "")
	restoreTimeout, restoreInterval := readyTimeout, pollInterval
	readyTimeout = 2 * time.Second
	pollInterval = 10 * time.Millisecond
	defer func() { readyTimeout, pollInterval = restoreTimeout, restoreInterval }()

	restoreSpawn := spawn
	defer func() { spawn = restoreSpawn }()
	spawn = func() {
		go func() {
			time.Sleep(30 * time.Millisecond)
			ln, err := net.Listen("unix", ipc.DefaultEventSocketPath())
			if err != nil {
				return
			}
			defer func() { _ = ln.Close() }()
			time.Sleep(time.Second)
		}()
	}

	start := time.Now()
	EnsureRunning()
	elapsed := time.Since(start)

	if !alive() {
		t.Error("expected daemon to be alive after EnsureRunning returns")
	}
	if elapsed >= readyTimeout {
		t.Errorf("EnsureRunning took %v, expected less than %v", elapsed, readyTimeout)
	}
}

// TestEnsureRunningSkipsSpawnUnderCenciSandbox asserts that inside a cenci-sand
// container (CENCI_SANDBOX=1), EnsureRunning never spawns a container-local
// daemon — such a daemon controls nothing on the host and only masks real
// wiring failures (#195, #202). It must return promptly without requiring a
// listener to ever come alive.
func TestEnsureRunningSkipsSpawnUnderCenciSandbox(t *testing.T) {
	useTempSocketDir(t)
	t.Setenv("CENCI_SANDBOX", "1")

	restoreTimeout, restoreInterval := readyTimeout, pollInterval
	readyTimeout = 200 * time.Millisecond
	pollInterval = 10 * time.Millisecond
	defer func() { readyTimeout, pollInterval = restoreTimeout, restoreInterval }()

	spawned := false
	restore := spawn
	spawn = func() { spawned = true }
	defer func() { spawn = restore }()

	done := make(chan struct{})
	go func() {
		EnsureRunning()
		close(done)
	}()

	// EnsureRunning must short-circuit immediately under CENCI_SANDBOX=1, well
	// before it would otherwise give up after readyTimeout.
	select {
	case <-done:
	case <-time.After(readyTimeout / 2):
		t.Fatal("EnsureRunning did not return promptly under CENCI_SANDBOX=1")
	}

	if spawned {
		t.Error("expected daemon not to be spawned when CENCI_SANDBOX=1")
	}
	if alive() {
		t.Error("expected no listener to be alive under CENCI_SANDBOX=1")
	}
}

// TestEnsureRunningSpawnsWhenCenciSandboxUnsetOrZero is a regression guard: the
// CENCI_SANDBOX gate must not change existing alive/spawn/poll behavior outside
// a cenci-sand container.
func TestEnsureRunningSpawnsWhenCenciSandboxUnsetOrZero(t *testing.T) {
	t.Run("CENCI_SANDBOX unset", func(t *testing.T) {
		useTempSocketDir(t)
		t.Setenv("CENCI_SANDBOX", "")
		restoreTimeout, restoreInterval := readyTimeout, pollInterval
		readyTimeout = 50 * time.Millisecond
		pollInterval = 10 * time.Millisecond
		defer func() { readyTimeout, pollInterval = restoreTimeout, restoreInterval }()

		spawned := false
		restore := spawn
		spawn = func() { spawned = true }
		defer func() { spawn = restore }()

		EnsureRunning()

		if !spawned {
			t.Error("expected daemon to be spawned when CENCI_SANDBOX is unset")
		}
	})

	t.Run("CENCI_SANDBOX=0", func(t *testing.T) {
		useTempSocketDir(t)
		restoreTimeout, restoreInterval := readyTimeout, pollInterval
		readyTimeout = 50 * time.Millisecond
		pollInterval = 10 * time.Millisecond
		defer func() { readyTimeout, pollInterval = restoreTimeout, restoreInterval }()

		t.Setenv("CENCI_SANDBOX", "0")

		spawned := false
		restore := spawn
		spawn = func() { spawned = true }
		defer func() { spawn = restore }()

		EnsureRunning()

		if !spawned {
			t.Error("expected daemon to be spawned when CENCI_SANDBOX=0")
		}
	})
}

func TestEnsureRunningGivesUpAfterTimeout(t *testing.T) {
	useTempSocketDir(t)
	t.Setenv("CENCI_SANDBOX", "")
	restoreTimeout, restoreInterval := readyTimeout, pollInterval
	readyTimeout = 100 * time.Millisecond
	pollInterval = 10 * time.Millisecond
	defer func() { readyTimeout, pollInterval = restoreTimeout, restoreInterval }()

	restoreSpawn := spawn
	spawn = func() {}
	defer func() { spawn = restoreSpawn }()

	done := make(chan struct{})
	go func() {
		EnsureRunning()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EnsureRunning did not return after the ready timeout")
	}
}
