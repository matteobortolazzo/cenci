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
)

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
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci close: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	decisions, err := closecmd.Run(closecmd.Opts{
		Target:       target,
		Force:        *force,
		DryRun:       *dryRun,
		SocketPath:   *socketPath,
		ReadSnapshot: dispatch.ReadSnapshot,
		Killer:       &tmux.ExecClient{},
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

// printCloseDecisions renders the outcome of `cenci close`. No matches
// is reported explicitly (rather than silent empty output) so a caller can
// distinguish "ran and found nothing" from "produced no output"; both cases
// exit 0 since cleanup running after a window is already gone is expected
// (idempotent).
func printCloseDecisions(w io.Writer, target string, decisions []closecmd.Decision) {
	if len(decisions) == 0 {
		_, _ = fmt.Fprintf(w, "no matching windows for %q\n", target)
		return
	}
	for _, d := range decisions {
		switch d.Action {
		case closecmd.ActionClosed:
			_, _ = fmt.Fprintf(w, "closed %s (%s:%s)\n", d.Window.WindowName, d.Window.Session, d.Window.WindowIndex)
		case closecmd.ActionWouldClose:
			_, _ = fmt.Fprintf(w, "would close %s (%s:%s)\n", d.Window.WindowName, d.Window.Session, d.Window.WindowIndex)
		case closecmd.ActionSkippedBusy:
			_, _ = fmt.Fprintf(w, "skip %s (%s:%s): status=%s, use --force to close\n", d.Window.WindowName, d.Window.Session, d.Window.WindowIndex, d.Window.Status)
		}
	}
}
