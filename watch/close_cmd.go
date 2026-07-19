package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/closecmd"
	"github.com/matteobortolazzo/cenci/watch/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// registerPendingClose sends w to the daemon's default event socket as a
// pending-close registration so the daemon retries the close itself once it
// observes the window's session end (#522). Production wiring for
// closecmd.Opts.Register; the daemon read (which already succeeded before
// this branch runs) makes fire-and-forget acceptable here, matching the
// existing SendEvent pattern.
func registerPendingClose(w watch.WindowState) error {
	return ipc.SendPendingClose(ipc.DefaultEventSocketPath(), ipc.PendingClose{
		Session:     w.Session,
		WindowIndex: w.WindowIndex,
		WindowName:  w.WindowName,
	})
}

// runClose implements `cenci close <ticket-number|window-name> [--force]
// [--dry-run] [--socket PATH]`. It resolves the target against the daemon's
// live window registry (never re-derives a tmux target itself) and closes
// every matched window that isn't running/waiting for input, unless --force
// is given. See internal/closecmd for the matching/kill/guard logic; this
// function only parses flags and renders the result.
func runClose(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci close: usage: cenci close <ticket-number|window-name> [--force] [--dry-run] [--socket PATH]")
		os.Exit(2)
	}
	target := args[0]

	fs := flag.NewFlagSet("close", flag.ExitOnError)
	force := fs.Bool("force", false, "close windows even if the agent is running or needs input")
	dryRun := fs.Bool("dry-run", false, "print close/skip decisions without killing any window")
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "IPC broadcast socket path")
	_ = fs.Parse(args[1:])

	// The flag parser stops at the first non-flag token, so anything left in
	// fs.Args() after the target and recognized flags is unexpected — mirrors
	// the "dispatch"/"dispatch loop" trailing-positional guards above.
	rejectExtra("cenci close", fs.Args())

	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       target,
		Force:        *force,
		DryRun:       *dryRun,
		SocketPath:   *socketPath,
		ReadSnapshot: dispatch.ReadSnapshot,
		Killer:       &tmux.ExecClient{},
		Register:     registerPendingClose,
	})
	if err != nil && len(decisions) == 0 {
		// Daemon unreachable (or every match failed to kill): fail safe, no
		// partial output.
		fmt.Fprintf(os.Stderr, "cenci close: %v\n", err)
		os.Exit(1)
	}

	printCloseDecisions(os.Stdout, target, decisions)

	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci close: %v\n", err)
		os.Exit(1)
	}
}

// printCloseDecisions renders the outcome of `cenci close`. Zero matches is a
// legitimate no-op (e.g. a card that never had an agent, or whose window was
// already closed) and produces no output at all — lazyboards surfaces any
// stdout as a visible message, so a quiet no-op avoids reading like a
// spurious warning (#522). Both the zero-match and has-matches cases exit 0
// since cleanup running after a window is already gone is expected
// (idempotent).
func printCloseDecisions(w io.Writer, target string, decisions []closecmd.Decision) {
	for _, d := range decisions {
		switch d.Action {
		case closecmd.ActionClosed:
			_, _ = fmt.Fprintf(w, "closed %s (%s:%s)\n", d.Window.WindowName, d.Window.Session, d.Window.WindowIndex)
		case closecmd.ActionWouldClose:
			_, _ = fmt.Fprintf(w, "would close %s (%s:%s)\n", d.Window.WindowName, d.Window.Session, d.Window.WindowIndex)
		case closecmd.ActionSkippedBusy:
			_, _ = fmt.Fprintf(w, "skip %s (%s:%s): status=%s, will retry automatically once the session ends (or use --force now)\n", d.Window.WindowName, d.Window.Session, d.Window.WindowIndex, d.Window.Status)
		}
	}
}
