package ipc

import (
	"errors"
	"net"
	"os"
	"testing"
)

// A live listener already bound at the path must make safeListen report
// ErrAlreadyRunning without disturbing the existing listener.
func TestSafeListen_LiveListenerReportsAlreadyRunning(t *testing.T) {
	path := tempSocket(t)

	first, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind first listener: %v", err)
	}
	defer func() { _ = first.Close() }()

	ln, err := safeListen(path)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got err=%v", err)
	}
	if ln != nil {
		_ = ln.Close()
		t.Fatal("expected nil listener when already running")
	}

	// The original listener must still accept connections.
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("original listener should still be reachable: %v", err)
	}
	_ = c.Close()
}

// A plain (non-socket, non-listened) file at the path is stale and must be
// removed so the bind succeeds.
func TestSafeListen_StalePlainFileRemovedAndRebinds(t *testing.T) {
	path := tempSocket(t)

	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	ln, err := safeListen(path)
	if err != nil {
		t.Fatalf("expected rebind to succeed, got: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if ln == nil {
		t.Fatal("expected a listener after removing stale file")
	}
}

// NewEventReceiver must surface ErrAlreadyRunning when a daemon is already
// listening on the event socket.
func TestNewEventReceiver_AlreadyRunning(t *testing.T) {
	path := tempSocket(t)

	first, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind first listener: %v", err)
	}
	defer func() { _ = first.Close() }()

	recv, err := NewEventReceiver(path)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got err=%v", err)
	}
	if recv != nil {
		_ = recv.Close()
		t.Fatal("expected nil receiver when already running")
	}
}
