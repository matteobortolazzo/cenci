package ipc

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

func TestSafeListen_RejectsSymlink(t *testing.T) {
	dir := tempSocketDir(t)
	socketPath := filepath.Join(dir, "s.sock")
	target := filepath.Join(dir, "evil-target")

	// Create a symlink at the socket path.
	if err := os.Symlink(target, socketPath); err != nil {
		t.Fatal(err)
	}

	ln, err := safeListen(socketPath)
	if err == nil {
		_ = ln.Close()
		t.Fatal("expected error for symlink, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected error mentioning symlink, got: %v", err)
	}
}

func TestSafeListen_WorksWithCleanPath(t *testing.T) {
	dir := tempSocketDir(t)
	socketPath := filepath.Join(dir, "s.sock")

	ln, err := safeListen(socketPath)
	if err != nil {
		t.Fatalf("safeListen() error: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Verify we can connect to the listener.
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to connect to socket: %v", err)
	}
	_ = conn.Close()
}

func TestSafeListen_RemovesStaleSocket(t *testing.T) {
	dir := tempSocketDir(t)
	socketPath := filepath.Join(dir, "s.sock")

	// Simulate a stale non-symlink file left behind (e.g., from a crash).
	if err := os.WriteFile(socketPath, nil, 0600); err != nil {
		t.Fatal(err)
	}

	// Verify the stale file exists.
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("stale socket file should exist: %v", err)
	}

	// safeListen should remove the stale file and create a new listener.
	ln, err := safeListen(socketPath)
	if err != nil {
		t.Fatalf("safeListen() error: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Verify the new listener works.
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to connect to new socket: %v", err)
	}
	_ = conn.Close()
}

func TestDefaultEventSocketPath_UsesSecureDir(t *testing.T) {
	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	got := DefaultEventSocketPath()
	secureDir, err := watch.SocketDir()
	if err != nil {
		t.Fatalf("watch.SocketDir() error: %v", err)
	}

	if !strings.HasPrefix(got, secureDir) {
		t.Errorf("DefaultEventSocketPath() = %q, want prefix %q", got, secureDir)
	}
}
