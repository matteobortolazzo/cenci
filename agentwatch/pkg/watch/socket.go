package watch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// secureSocketDir returns a directory suitable for Unix sockets.
// Uses $XDG_RUNTIME_DIR if set and valid (exists, is a real directory, not world/group-writable),
// otherwise creates /tmp/agentwatch-<uid>/ with 0700 permissions.
func secureSocketDir() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		info, err := os.Lstat(dir)
		if err == nil && info.IsDir() && info.Mode().Perm()&0022 == 0 {
			return dir, nil
		}
		// XDG_RUNTIME_DIR is not usable (missing, symlink, or wrong perms);
		// fall through to /tmp fallback.
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("agentwatch-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// SocketDir returns the dedicated agentwatch/ subdirectory nested under the
// shared, XDG-aware runtime directory: <secureSocketDir>/agentwatch/. Nesting
// keeps this directory mountable in isolation (the shared XDG runtime dir
// also holds wayland/pulse/keyring sockets that must not leak into a sandbox).
// It is created with 0700 permissions if missing. Creation is idempotent
// against a directory that already exists (e.g. a container bind-mount
// pre-created by the host) — permissions are only forced on fresh creation so
// this never fights an already-mounted directory's existing ownership/perms.
// This is the single home of the runtime-dir fallback logic; other agentwatch
// packages build their socket paths from it.
func SocketDir() (string, error) {
	base, err := secureSocketDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "agentwatch")
	if info, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return "", err
		}
	} else if statErr != nil {
		return "", statErr
	} else if !info.IsDir() {
		return "", fmt.Errorf("socket dir path %q exists but is not a directory", dir)
	}
	return dir, nil
}

// defaultSocketPath returns a socket path for the given name, nested under
// SocketDir(), with a flat fallback under os.TempDir() if the directory
// cannot be resolved/created.
func defaultSocketPath(name string) string {
	dir, err := SocketDir()
	if err != nil {
		log.Printf("warning: could not create secure socket dir: %v; using fallback path", err)
		return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d.sock", name, os.Getuid()))
	}
	return filepath.Join(dir, name+".sock")
}

// DefaultSocketPath returns the broadcast socket path that subscribers connect
// to with Dial. It resolves to <SocketDir>/agentwatch.sock, falling back to
// /tmp/agentwatch-<uid>.sock if the secure directory cannot be created.
func DefaultSocketPath() string { return defaultSocketPath("agentwatch") }
