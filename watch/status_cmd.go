package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/config"
	"github.com/matteobortolazzo/cenci/watch/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/internal/frontend/status"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// runWidgetJSON implements the hidden plumbing subcommand `widget-json`
// (alias `waybar`): prints a single line of Waybar custom-module JSON and
// exits. This is the exact behavior the old `cenci status` had before
// `status` became the human-readable overview below — every widget frontend
// (noctalia, dms, gnome, plasma, macOS/SwiftBar) and any real Waybar config
// should invoke this subcommand, not `status`.
func runWidgetJSON(args []string) {
	fs := flag.NewFlagSet("widget-json", flag.ExitOnError)
	defaults := config.Default()
	wcfg := status.Config{
		SymbolIdle:            defaults.SymbolIdle,
		SymbolRunning:         defaults.SymbolRunning,
		SymbolDone:            defaults.SymbolDone,
		SymbolNeedInput:       defaults.SymbolNeedInput,
		SymbolStopped:         defaults.SymbolStopped,
		SymbolFailed:          defaults.SymbolFailed,
		SymbolDispatch:        status.DefaultSymbolDispatch,
		SymbolDispatchRunning: status.DefaultSymbolDispatchRunning,
	}
	fs.StringVar(&wcfg.SocketPath, "socket", ipc.DefaultSocketPath(), "IPC socket path")
	fs.StringVar(&wcfg.SymbolIdle, "symbol-idle", wcfg.SymbolIdle, "symbol for idle state")
	fs.StringVar(&wcfg.SymbolRunning, "symbol-running", wcfg.SymbolRunning, "symbol for running state")
	fs.StringVar(&wcfg.SymbolDone, "symbol-done", wcfg.SymbolDone, "symbol for done state")
	fs.StringVar(&wcfg.SymbolNeedInput, "symbol-input", wcfg.SymbolNeedInput, "symbol for need-input state")
	fs.StringVar(&wcfg.SymbolStopped, "symbol-stopped", wcfg.SymbolStopped, "symbol for stopped (interrupted) state")
	fs.StringVar(&wcfg.SymbolFailed, "symbol-failed", wcfg.SymbolFailed, "symbol for dispatch-failed state")
	fs.StringVar(&wcfg.SymbolDispatch, "symbol-dispatch", wcfg.SymbolDispatch, "symbol for the fleet dispatch loop indicator")
	fs.StringVar(&wcfg.SymbolDispatchRunning, "symbol-dispatch-running", wcfg.SymbolDispatchRunning, "symbol for a fleet dispatch pass actively running")
	_ = fs.Parse(args)

	if err := status.Run(wcfg); err != nil {
		if errors.Is(err, status.ErrNoOutput) {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "cenci widget-json: %v\n", err)
		os.Exit(1)
	}
}

// runStatus implements the human-readable `cenci status` overview:
// daemon running/pid, active sessions (read from the same broadcast state
// snapshot widget-json reads), and the embedded fleet dispatch loop's state
// (reusing renderDispatchState, the same renderer `dispatch loop status`
// uses). It degrades gracefully when the daemon is down — it still prints a
// report and always exits 0 (unlike `daemon status`, which exits 1 when not
// running; see the exit-code note in README.md).
func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "IPC broadcast socket path")
	eventSocketPath := fs.String("event-socket", ipc.DefaultEventSocketPath(), "event socket path")
	_ = fs.Parse(args)
	rejectExtra("cenci status", fs.Args())

	info := daemon.Status(*eventSocketPath, ipc.DefaultPIDPath())
	fmt.Print(renderHumanStatus(info, *socketPath))
}

// renderHumanStatus renders the report body for runStatus. Split out for
// unit testing without a subprocess.
func renderHumanStatus(info daemon.StatusInfo, socketPath string) string {
	var b strings.Builder

	if info.Running {
		if info.PID > 0 {
			fmt.Fprintf(&b, "daemon: running (pid %d)\n", info.PID)
		} else {
			fmt.Fprintf(&b, "daemon: running (pid unknown)\n")
		}
	} else {
		fmt.Fprintf(&b, "daemon: not running\n")
	}

	snap, err := dispatch.ReadSnapshot(socketPath)
	switch {
	case errors.Is(err, watch.ErrDaemonUnreachable):
		fmt.Fprintf(&b, "sessions: unavailable (daemon not reachable)\n")
	case err != nil:
		fmt.Fprintf(&b, "sessions: unavailable (error reading snapshot: %v)\n", err)
	case snap == nil:
		fmt.Fprintf(&b, "sessions: unavailable (daemon not reachable)\n")
	case len(snap.Windows) == 0:
		fmt.Fprintf(&b, "sessions: none\n")
	default:
		fmt.Fprintf(&b, "sessions (%d):\n", len(snap.Windows))
		for _, w := range snap.Windows {
			fmt.Fprintf(&b, "  %s\n", status.FormatSessionLine(w, status.Identity))
		}
	}

	dstate := dispatch.ResolveDispatchState("", socketPath, io.Discard)
	b.WriteString(renderDispatchState(dstate))

	return b.String()
}
