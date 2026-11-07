package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// ErrMalformedSocket marks a non-empty tmux socket that fails the shape check
// (ValidSocket). A socket-carrying call returns it before any tmux is exec'd and
// never falls back to the ambient server.
var ErrMalformedSocket = errors.New("tmux socket is malformed")

// ErrUntrustedSocket marks a socket CheckTrustedSocket rejects: a path that is
// not exactly <trusted dir>/<name>, or a trusted directory that is not a real,
// private directory owned by the current uid.
var ErrUntrustedSocket = errors.New("tmux socket is not trusted")

// socketShape validates a tmux socket before it is ever passed to `tmux -S`
// (#1007): an absolute path containing no tab or newline (mirroring the pane
// pattern's shape-validation style), length-bounded so a pathological value
// can't overload a classification path (Go's regexp repeat-count ceiling caps
// this at 1000). A container process fully controls its own environ, so
// anything else is malformed.
var socketShape = regexp.MustCompile(`^/[^\t\n]{0,1000}$`)

// ValidSocket reports whether s passes the socket shape check. It is the single
// definition: internal/sandbox calls it for the reaper's CENCI_TMUX_SOCKET.
func ValidSocket(s string) bool {
	return socketShape.MatchString(s)
}

// envAllowlist is the minimal set of env vars a `tmux -S <path>` child process
// needs. tmux forwards its full process environment to whatever server it
// connects to (the MSG_IDENTIFY_ENVIRON/update-environment handshake), and a
// socket can be attacker-controlled (any container process can set
// CENCI_TMUX_SOCKET), so minimalEnv builds an explicit allowlisted env from
// scratch instead of scrubbing a denylist out of os.Environ(): no secret ambient
// var is ever available to hand to a hostile peer. This does not restrict which
// socket path gets dialed, only what is in that connection's environment.
var envAllowlist = []string{"PATH", "HOME", "TERM", "LANG"}

// minimalEnv builds the child env for a `tmux -S <path>` process: only the vars
// in envAllowlist, each included only if actually set in the current process env
// (never fabricated). TMUX, TMUX_PANE and TMUX_TMPDIR are never in the allowlist
// (watch/docs/tmux.md: an unscoped target resolution honors an ambient
// $TMUX_PANE that does not exist on the socket being queried).
func minimalEnv() []string {
	out := make([]string, 0, len(envAllowlist))
	for _, key := range envAllowlist {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}

// RunSocket runs `tmux -S <socket> <args...>` writing the child's output to
// stdout and stderr. It is the one hardened exec for every socket-carrying
// call: a socket that fails ValidSocket returns an error wrapping
// ErrMalformedSocket before anything is exec'd; the child runs with only the
// allowlisted env; and the call is bounded by windowCmdTimeout (inside the
// caller's ctx deadline) plus windowCmdWaitDelay for a grandchild holding the
// output pipes. A timeout is reported by joining context.DeadlineExceeded into
// the returned error, since os/exec otherwise returns a plain *exec.ExitError
// ("signal: killed") or bare exec.ErrWaitDelay (watch/docs/go-gotchas.md).
// Every other failure is os/exec's own error, unwrapped, so a caller can
// classify it from stderr.
func RunSocket(ctx context.Context, socket string, stdout, stderr io.Writer, args ...string) error {
	if !ValidSocket(socket) {
		return fmt.Errorf("%w: %q", ErrMalformedSocket, socket)
	}
	cctx, cancel := context.WithTimeout(ctx, windowCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "tmux", append([]string{"-S", socket}, args...)...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = minimalEnv()
	cmd.WaitDelay = windowCmdWaitDelay
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if cctx.Err() == context.DeadlineExceeded || errors.Is(err, exec.ErrWaitDelay) {
		return errors.Join(err, context.DeadlineExceeded)
	}
	return err
}

// socketOwnerUID is the uid a trusted socket directory must belong to. It is a
// variable only so a test can stand in for a foreign owner, following
// dirOwnerUID in pkg/watch/socket.go.
var socketOwnerUID = os.Getuid

// TrustedSocketDir returns the directory a trusted tmux socket lives in:
// EvalSymlinks(${TMUX_TMPDIR:-/tmp})/tmux-<uid>. It does not touch the
// directory itself; CheckTrustedSocket does that on every call.
func TrustedSocketDir() (string, error) {
	base := os.Getenv("TMUX_TMPDIR")
	if base == "" {
		base = "/tmp"
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("resolving tmux tmpdir %q: %w", base, err)
	}
	return filepath.Join(resolved, fmt.Sprintf("tmux-%d", os.Getuid())), nil
}

// CheckTrustedSocket reports (as a nil error) that socket is exactly one name
// directly under dir and that dir is a real directory (never a symlink) owned
// by the current uid with no group or other permission bits. Every rejection
// wraps ErrUntrustedSocket. The directory is checked on every call, so a
// directory swapped after dir was computed fails closed.
func CheckTrustedSocket(dir, socket string) error {
	if filepath.Clean(socket) != socket || filepath.Dir(socket) != dir {
		return fmt.Errorf("%w: %q is not a socket directly under %q", ErrUntrustedSocket, socket, dir)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUntrustedSocket, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a real directory", ErrUntrustedSocket, dir)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != socketOwnerUID() {
		return fmt.Errorf("%w: %q is not owned by the current user", ErrUntrustedSocket, dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %q has group or other permissions (%04o)", ErrUntrustedSocket, dir, info.Mode().Perm())
	}
	return nil
}

// ResolveSocket returns the first comma-separated field of $TMUX when it is set
// and non-empty (tmux's own "<socket>,<server pid>,<session id>" value), else
// the cenci socket under TrustedSocketDir. A non-empty $TMUX field is returned
// as-is: every socket-carrying call shape-checks it anyway. A $TMUX whose first
// field is empty (",", ",1,2") wraps ErrMalformedSocket, because "" means "bare
// tmux with the ambient env" to every socket-carrying call: a nil-error result is
// never "".
func ResolveSocket() (string, error) {
	if v := os.Getenv("TMUX"); v != "" {
		socket, _, _ := strings.Cut(v, ",")
		if socket == "" {
			return "", fmt.Errorf("%w: empty socket field in $TMUX", ErrMalformedSocket)
		}
		return socket, nil
	}
	dir, err := TrustedSocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cenci"), nil
}
