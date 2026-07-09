package ipc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecureSocketDir_UsesXDGWhenSet(t *testing.T) {
	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	got, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}
	if got != xdgDir {
		t.Errorf("expected %q, got %q", xdgDir, got)
	}
}

func TestSecureSocketDir_CreatesTmpDirWhenNoXDG(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	got, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}

	expectedSuffix := fmt.Sprintf("agentwatch-%d", os.Getuid())
	if !strings.HasSuffix(got, expectedSuffix) {
		t.Errorf("expected path ending with %q, got %q", expectedSuffix, got)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat(%q) error: %v", got, err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory, got file")
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("expected permissions 0700, got %04o", perm)
	}
}

func TestSecureSocketDir_ValidatesExistingDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	// Create the directory with wrong permissions before calling secureSocketDir.
	dir := filepath.Join(t.TempDir(), fmt.Sprintf("agentwatch-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0777); err != nil {
		t.Fatal(err)
	}

	// Override TMP so secureSocketDir uses our test directory's parent.
	t.Setenv("TMPDIR", filepath.Dir(dir))

	got, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}
	if got != dir {
		t.Errorf("expected %q, got %q", dir, got)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat(%q) error: %v", got, err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("expected permissions fixed to 0700, got %04o", perm)
	}
}

func TestSecureSocketDir_RejectsInsecureXDG(t *testing.T) {
	// Create a directory with world-writable permissions.
	insecureDir := filepath.Join(t.TempDir(), "insecure-xdg")
	if err := os.Mkdir(insecureDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Chmod bypasses umask, ensuring the directory is actually world-writable.
	if err := os.Chmod(insecureDir, 0777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", insecureDir)

	got, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}
	// Should NOT return the insecure XDG dir; should fall through to /tmp.
	if got == insecureDir {
		t.Errorf("secureSocketDir() returned insecure XDG dir %q; expected /tmp fallback", insecureDir)
	}
}

func TestSafeListen_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")
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
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")

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
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")

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

func TestDefaultSocketPath_UsesSecureDir(t *testing.T) {
	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	got := DefaultSocketPath()
	secureDir, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}

	if !strings.HasPrefix(got, secureDir) {
		t.Errorf("DefaultSocketPath() = %q, want prefix %q", got, secureDir)
	}
}

func TestDefaultEventSocketPath_UsesSecureDir(t *testing.T) {
	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	got := DefaultEventSocketPath()
	secureDir, err := secureSocketDir()
	if err != nil {
		t.Fatalf("secureSocketDir() error: %v", err)
	}

	if !strings.HasPrefix(got, secureDir) {
		t.Errorf("DefaultEventSocketPath() = %q, want prefix %q", got, secureDir)
	}
}
