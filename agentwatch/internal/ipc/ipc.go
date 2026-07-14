package ipc

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/pkg/watch"
)

// The read-side contract (state types and streaming client) lives in the public
// pkg/watch package. These aliases keep internal call sites unchanged while the
// canonical definitions stay in one place. Type aliases are identity-equal, so
// ipc.StateSnapshot and watch.StateSnapshot are the same type.
type (
	StateSnapshot   = watch.StateSnapshot
	WindowState     = watch.WindowState
	StatusSummary   = watch.StatusSummary
	AttentionUpdate = watch.AttentionUpdate
	Client          = watch.Client
)

// Dial connects to the agentwatch broadcast socket. It is re-exported from
// pkg/watch so existing internal callers keep working.
var Dial = watch.Dial

// ErrAlreadyRunning is returned by safeListen (and the constructors that use it)
// when a live listener is already bound to the target socket path. Callers can
// treat this as a signal that another daemon owns the socket and back off
// instead of stealing it.
var ErrAlreadyRunning = errors.New("agentwatch: daemon already running")

// aliveDialTimeout bounds the probe that checks whether an existing socket has a
// live listener. It is short because the socket is local and a stale socket has
// no one to answer.
const aliveDialTimeout = 200 * time.Millisecond

// safeListen creates a Unix domain socket listener, rejecting symlinks at the path.
// Note: The Lstat-to-Listen sequence has a small residual TOCTOU window.
// Full protection relies on the containing directory being 0700 and user-owned
// (as provided by watch.SocketDir), which prevents other users from planting symlinks.
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

// DefaultSocketPath returns the default broadcast Unix socket path
// (<SocketDir>/agentwatch.sock). It is re-exported from pkg/watch.
func DefaultSocketPath() string { return watch.DefaultSocketPath() }

// DefaultSocketDir returns the resolved agentwatch socket directory
// (creating it with 0700 if missing). It is re-exported from pkg/watch so
// main.go's CLI routing doesn't need its own import of pkg/watch.
func DefaultSocketDir() (string, error) { return watch.SocketDir() }

// DefaultEventSocketPath returns the default event (inbound) socket path the
// daemon listens on for hook notifications. This write-side socket stays
// internal; it is rebuilt from the shared watch.SocketDir so the runtime-dir
// fallback logic lives in exactly one place.
func DefaultEventSocketPath() string {
	dir, err := watch.SocketDir()
	if err != nil {
		log.Printf("warning: could not create secure socket dir: %v; using fallback event-socket path", err)
		return filepath.Join(os.TempDir(), fmt.Sprintf("agentwatch-events-%d.sock", os.Getuid()))
	}
	return filepath.Join(dir, "agentwatch-events.sock")
}
