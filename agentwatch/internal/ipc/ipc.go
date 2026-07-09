package ipc

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ErrAlreadyRunning is returned by safeListen (and the constructors that use it)
// when a live listener is already bound to the target socket path. Callers can
// treat this as a signal that another daemon owns the socket and back off
// instead of stealing it.
var ErrAlreadyRunning = errors.New("agentwatch: daemon already running")

// aliveDialTimeout bounds the probe that checks whether an existing socket has a
// live listener. It is short because the socket is local and a stale socket has
// no one to answer.
const aliveDialTimeout = 200 * time.Millisecond

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
		// Something exists at the path. Probe it before removing: a live daemon
		// answers a dial, and stealing its socket would break it. Only remove
		// the file when the probe fails (stale socket or non-socket leftover).
		if conn, derr := net.DialTimeout("unix", socketPath, aliveDialTimeout); derr == nil {
			_ = conn.Close()
			return nil, ErrAlreadyRunning
		}
		// Not answering — treat as stale and remove.
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
// Uses $XDG_RUNTIME_DIR/agentwatch.sock, falling back to /tmp/agentwatch-<uid>.sock.
func DefaultSocketPath() string { return defaultSocketPath("agentwatch") }

// DefaultEventSocketPath returns the default event socket path.
func DefaultEventSocketPath() string { return defaultSocketPath("agentwatch-events") }
