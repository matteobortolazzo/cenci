// Package up is the engine behind `cenci up`: it makes sure the cenci tmux
// server and its session exist on the cenci socket and hands the user over to
// it. It only ever runs `tmux -L cenci ...`.
package up

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

const (
	// SocketName is the tmux server name (`tmux -L cenci`).
	SocketName = "cenci"
	// SessionName is the one fixed session `cenci up` creates and enters.
	SessionName = "cenci"
	// BoardWindow is the name of the lazyboards window created with the session.
	BoardWindow = "board"
)

const (
	// callTimeout bounds every tmux call but the attach, mirroring
	// tmux.RunSocket.
	callTimeout = 5 * time.Second
	// callWaitDelay is the extra wait for a grandchild holding the output pipes.
	callWaitDelay = 2 * time.Second
)

var (
	// ErrConfigMissing marks a missing tmux config file.
	ErrConfigMissing = errors.New("cenci tmux config is missing")
	// ErrPersonalServer marks a launch from inside a tmux server that is not the
	// cenci one.
	ErrPersonalServer = errors.New("running inside a personal tmux server")
)

// Opts carries the inputs and the injected seams of Run.
type Opts struct {
	// ConfigPath is the tmux config the server is started with.
	ConfigPath string
	// EnsureDaemon makes sure the daemon is running; an error only warns.
	EnsureDaemon func() error
	// LookPath resolves an executable by name (tmux, lazyboards).
	LookPath func(file string) (string, error)
	// Exec replaces the process image, like syscall.Exec.
	Exec func(argv0 string, argv []string, env []string) error
	// Env returns the ambient environment.
	Env func() []string
	// Stderr receives warnings.
	Stderr io.Writer
}

// entryPath is where `cenci up` was started from.
type entryPath int

const (
	// outsideTmux: no $TMUX.
	outsideTmux entryPath = iota
	// insideCenci: $TMUX names the cenci server's socket.
	insideCenci
	// insidePersonal: $TMUX names anything else.
	insidePersonal
)

// classify sorts the ambient $TMUX value into an entry path. Only an exact
// match of its first field with the cenci socket counts as the cenci server;
// every other non-empty value, a malformed or empty first field included, is a
// personal server, so a doubtful value is refused rather than nested into.
func classify(tmuxEnv, cenciSocket string) entryPath {
	if tmuxEnv == "" {
		return outsideTmux
	}
	first, _, _ := strings.Cut(tmuxEnv, ",")
	if first != "" && first == cenciSocket {
		return insideCenci
	}
	return insidePersonal
}

// envValue returns the last value of key in env (os/exec's own duplicate rule).
func envValue(env []string, key string) string {
	value := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			value = v
		}
	}
	return value
}

// withoutTmuxVars drops TMUX and TMUX_PANE from env. A call that creates or
// probes the cenci server must not resolve its target through the caller's own
// tmux server, and the first client to start a server sets the environment every
// pane inherits.
func withoutTmuxVars(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// client runs tmux on the cenci socket. Every call starts `-L cenci`; none is
// ever bare or carries `-S`.
type client struct {
	path string
	// scrubbed is the ambient environment without TMUX/TMUX_PANE: it runs every
	// call but switch-client.
	scrubbed []string
	// ambient is the unchanged environment, for switch-client, which needs the
	// caller's $TMUX to know which client to move.
	ambient []string
}

// run runs one bounded tmux call with env and returns its trimmed stderr. A
// timeout is reported by joining context.DeadlineExceeded into the error,
// since os/exec otherwise returns a plain *exec.ExitError ("signal: killed") or
// a bare exec.ErrWaitDelay (watch/docs/go-gotchas.md).
func (c client) run(ctx context.Context, env []string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(cctx, c.path, append([]string{"-L", SocketName}, args...)...)
	cmd.Env = env
	cmd.Stderr = &stderr
	cmd.WaitDelay = callWaitDelay
	err := cmd.Run()
	if err == nil {
		return "", nil
	}
	if cctx.Err() == context.DeadlineExceeded || errors.Is(err, exec.ErrWaitDelay) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	return strings.TrimSpace(stderr.String()), err
}

// hasSession reports whether the cenci session exists. A non-zero exit is a
// normal "absent" (no session, or no server: watch/docs/tmux.md); anything else
// is a failed probe.
func (c client) hasSession(ctx context.Context) (bool, error) {
	_, err := c.run(ctx, c.scrubbed, "has-session", "-t", "="+SessionName)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && !errors.Is(err, context.DeadlineExceeded) {
		return false, nil
	}
	return false, fmt.Errorf("probing the %s session: %w", SessionName, err)
}

// ensureSession creates the cenci session when it is absent and reports whether
// this call created it. A failed create is re-probed: a rival `cenci up` that won
// the race leaves the session in place, and this run continues as if it had
// existed. A timeout counts as a failed create too, since tmux may have
// created the session before a lingering pipe held the call open.
func (c client) ensureSession(ctx context.Context, confPath string) (bool, error) {
	exists, err := c.hasSession(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	stderr, err := c.run(ctx, c.scrubbed, "-f", confPath, "new-session", "-d", "-s", SessionName)
	if err == nil {
		return true, nil
	}
	if exists, probeErr := c.hasSession(ctx); probeErr == nil && exists {
		return false, nil
	}
	return false, tmuxError("creating the "+SessionName+" session", stderr, err)
}

// tmuxError wraps err with what was being done and tmux's own stderr.
func tmuxError(what, stderr string, err error) error {
	if stderr == "" {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s: %s: %w", what, stderr, err)
}

// singleQuote quotes s for the shell tmux runs a window command through.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Run is the `cenci up` sequence: check the config, ensure the daemon and the
// session (with a lazyboards window when this run creates the session), then act
// on the entry path: attach from outside tmux, switch-client inside the cenci
// server, or refuse inside a personal server.
func Run(ctx context.Context, o Opts) error {
	if _, err := os.Stat(o.ConfigPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s does not exist; run `cenci-installer --tmux-defaults` to install it", ErrConfigMissing, o.ConfigPath)
		}
		return fmt.Errorf("checking the tmux config %s: %w", o.ConfigPath, err)
	}
	cenciSocket, err := tmux.CenciSocketPath()
	if err != nil {
		return err
	}
	tmuxPath, err := o.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux is required: %w", err)
	}

	if err := o.EnsureDaemon(); err != nil {
		_, _ = fmt.Fprintf(o.Stderr, "cenci up: warning: the daemon is not running (%v); continuing without it\n", err)
	}

	ambient := o.Env()
	c := client{path: tmuxPath, scrubbed: withoutTmuxVars(ambient), ambient: ambient}

	created, err := c.ensureSession(ctx, o.ConfigPath)
	if err != nil {
		return err
	}
	if created {
		c.addBoard(ctx, o)
	}

	switch classify(envValue(ambient, "TMUX"), cenciSocket) {
	case insideCenci:
		stderr, err := c.run(ctx, c.ambient, "switch-client", "-t", "="+SessionName)
		if err != nil {
			return tmuxError("switching to the "+SessionName+" session", stderr, err)
		}
		return nil
	case insidePersonal:
		return fmt.Errorf("%w: cenci up never nests tmux or touches your own server. "+
			"Detach from it (tmux detach), then run cenci up again, "+
			"or attach from another terminal with: tmux -L %s attach -t %s",
			ErrPersonalServer, SocketName, SessionName)
	default:
		argv := []string{"tmux", "-L", SocketName, "attach-session", "-t", "=" + SessionName}
		if err := o.Exec(tmuxPath, argv, c.scrubbed); err != nil {
			return fmt.Errorf("attaching to the %s session: %w", SessionName, err)
		}
		return nil
	}
}

// addBoard opens the lazyboards window in a session this run just created. It
// is best effort: no lazyboards (a lookup error, exec.ErrDot included) means no
// window, and a failed new-window only warns. The absolute path is used because
// an already-running server may have a different PATH.
func (c client) addBoard(ctx context.Context, o Opts) {
	lazyboards, err := o.LookPath("lazyboards")
	if err != nil {
		return
	}
	stderr, err := c.run(ctx, c.scrubbed, "new-window", "-d", "-t", "="+SessionName+":", "-n", BoardWindow, singleQuote(lazyboards))
	if err != nil {
		_, _ = fmt.Fprintf(o.Stderr, "cenci up: warning: could not open the %s window: %v\n", BoardWindow, tmuxError("new-window", stderr, err))
	}
}
