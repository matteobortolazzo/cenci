package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// bindUsage is the usage line of `cenci bind`; a usage error prints it on one
// stderr line with exit 2, per docs/cli-conventions.md.
const bindUsage = "usage: cenci bind <session-id>"

// bindHelp is what -h/--help prints to stdout.
const bindHelp = bindUsage + `

Pin a Codex session to the tmux pane this command runs in. Run it in the
pane that shows the session. The daemon checks that the pane is listed on
this tmux socket under this tmux server pid before it records the binding.
Bindings are kept in memory until the daemon restarts.

Exit codes: 0 bound, 1 refused or the daemon is unreachable, 2 usage error.
`

// runBind is the `cenci bind <session-id>` verb (#1529).
func runBind(args []string) {
	if code := bindSession(args, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// bindSession is runBind's testable core: it returns the exit code (0 bound, 1
// refused, outside tmux or no daemon, 2 usage error) instead of exiting. The
// pane, socket and server pid are the caller's own $TMUX_PANE and the first two
// fields of $TMUX; outside tmux nothing is sent.
func bindSession(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = fmt.Fprint(stdout, bindHelp)
		return 0
	}
	if err != nil || fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "cenci bind: "+bindUsage)
		return 2
	}
	sessionID := fs.Arg(0)

	pane := os.Getenv("TMUX_PANE")
	socket, rest, _ := strings.Cut(os.Getenv("TMUX"), ",")
	pid, _, _ := strings.Cut(rest, ",")
	if pane == "" || socket == "" {
		_, _ = fmt.Fprintln(stderr, "cenci bind: not inside a tmux pane")
		return 1
	}

	path := ipc.DefaultEventSocketPath()
	if path == "" {
		_, _ = fmt.Fprintln(stderr, "cenci bind: the cenci daemon is not reachable (no secure socket directory)")
		return 1
	}
	resp, err := ipc.SendBind(path, ipc.BindRequest{SessionID: sessionID, TmuxPane: pane, TmuxSocket: socket, TmuxServerPID: pid})
	switch {
	case errors.Is(err, ipc.ErrArmUndelivered):
		_, _ = fmt.Fprintln(stderr, "cenci bind: the cenci daemon is not reachable")
		return 1
	case err != nil:
		_, _ = fmt.Fprintln(stderr, "cenci bind: the cenci daemon did not answer; the binding status is unknown")
		return 1
	case !resp.OK:
		_, _ = fmt.Fprintln(stderr, "cenci bind: "+bindReason(resp.Reason))
		return 1
	}
	shown := resp.Pane
	if !sandbox.ValidPaneID(shown) {
		shown = pane
	}
	_, _ = fmt.Fprintf(stdout, "bound Codex session %s to tmux pane %s\n", sessionID, shown)
	return 0
}

// bindReason is the daemon's nack reason as one printable line: it came from a
// socket, so every non-printable rune is replaced.
func bindReason(reason string) string {
	if reason == "" {
		return "the daemon refused the bind"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, reason)
}
