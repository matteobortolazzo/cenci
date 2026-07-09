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

// SocketDir returns the shared, XDG-aware runtime directory that agentwatch
// sockets live in. It uses $XDG_RUNTIME_DIR when set and secure, otherwise a
// user-private /tmp/agentwatch-<uid> directory created with 0700 permissions.
// This is the single home of the runtime-dir fallback logic; other agentwatch
// packages build their socket paths from it.
func SocketDir() (string, error) {
	return secureSocketDir()
}

// defaultSocketPath returns a socket path for the given name, using the secure
// directory from secureSocketDir with a flat fallback under os.TempDir().
func defaultSocketPath(name string) string {
	dir, err := secureSocketDir()
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
