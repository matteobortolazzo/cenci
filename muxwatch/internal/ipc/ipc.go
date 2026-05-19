package ipc

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
)

// StateSnapshot is the top-level message broadcast to IPC clients.
type StateSnapshot struct {
	Timestamp string        `json:"timestamp"`
	Windows   []WindowState `json:"windows"`
	Summary   StatusSummary `json:"summary"`
}

// WindowState describes a single tracked window.
type WindowState struct {
	Session       string `json:"session"`
	WindowIndex   string `json:"window_index"`
	WindowName    string `json:"window_name"`
	TaskName      string `json:"task_name"`
	Status        string `json:"status"`
	Agent         string `json:"agent,omitempty"`
	ManuallyNamed bool   `json:"manually_named"`
}

// StatusSummary counts sessions by status.
type StatusSummary struct {
	Total     int `json:"total"`
	Idle      int `json:"idle"`
	Running   int `json:"running"`
	Done      int `json:"done"`
	Stopped   int `json:"stopped"`
	NeedInput int `json:"need_input"`
}

// secureSocketDir returns a directory suitable for Unix sockets.
// Uses $XDG_RUNTIME_DIR if set and valid (exists, is a real directory, not world/group-writable),
// otherwise creates /tmp/muxwatch-<uid>/ with 0700 permissions.
func secureSocketDir() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		info, err := os.Lstat(dir)
		if err == nil && info.IsDir() && info.Mode().Perm()&0022 == 0 {
			return dir, nil
		}
		// XDG_RUNTIME_DIR is not usable (missing, symlink, or wrong perms);
		// fall through to /tmp fallback.
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("muxwatch-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// safeListen creates a Unix domain socket listener, rejecting symlinks at the path.
// Note: The Lstat-to-Listen sequence has a small residual TOCTOU window.
// Full protection relies on the containing directory being 0700 and user-owned
// (as provided by secureSocketDir), which prevents other users from planting symlinks.
func safeListen(socketPath string) (net.Listener, error) {
	info, err := os.Lstat(socketPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing to bind: %s is a symlink", socketPath)
		}
		// Exists but not a symlink — remove stale socket.
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return net.Listen("unix", socketPath)
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

// DefaultSocketPath returns the default Unix socket path.
// Uses $XDG_RUNTIME_DIR/muxwatch.sock, falling back to /tmp/muxwatch-<uid>.sock.
func DefaultSocketPath() string { return defaultSocketPath("muxwatch") }

// DefaultEventSocketPath returns the default event socket path.
func DefaultEventSocketPath() string { return defaultSocketPath("muxwatch-events") }
