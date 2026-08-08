package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/babysit"
)

// babysitUsage is the one-line usage hint for `cenci babysit <pr> --agent
// ...`, printed to stderr on a malformed-CLI invocation (exit 2).
const babysitUsage = "cenci babysit: usage: cenci babysit <pr> --agent <claude|codex|opencode> [--interval 15m] [--once] [--session <name>] [--dir <path>]"

// parseBabysitArgs parses and validates the arguments for the main (non-
// "stop") `cenci babysit <pr> --agent <agent> ...` invocation. It performs no
// side effects (no os.Exit, no babysit.Run) so it can be exercised directly
// by tests without spawning a subprocess or touching the filesystem/network.
// On success it returns the parsed options and ok == true; on a malformed
// invocation it returns ok == false and the caller should print babysitUsage
// and exit 2.
func parseBabysitArgs(args []string) (babysit.Options, bool) {
	fs := flag.NewFlagSet("babysit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agent := fs.String("agent", "", "agent to launch for actionable work (claude, codex, or opencode)")
	interval := fs.Duration("interval", 15*time.Minute, "base polling interval")
	once := fs.Bool("once", false, "run one tick in the foreground")
	stateDir := fs.String("state-dir", "", "state directory (default: $XDG_STATE_HOME/cenci/babysit)")
	session := fs.String("session", "", "tmux session every launch() call targets (default: resolved from the current tmux pane at arm time)")
	dir := fs.String("dir", "", "working directory every launch() call's window starts in (default: resolved from the local repo root at arm time)")
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return babysit.Options{}, false
	}
	pr := strings.TrimPrefix(args[0], "#")
	if err := fs.Parse(args[1:]); err != nil {
		return babysit.Options{}, false
	}
	if len(fs.Args()) != 0 || (*agent != "claude" && *agent != "codex" && *agent != "opencode") {
		return babysit.Options{}, false
	}
	return babysit.Options{PR: pr, Agent: *agent, Interval: *interval, Once: *once, StateDir: *stateDir, Session: *session, Dir: *dir}, true
}

func runBabysit(args []string) {
	if len(args) > 0 && args[0] == "stop" {
		fs := flag.NewFlagSet("babysit stop", flag.ExitOnError)
		stateDir := fs.String("state-dir", "", "state directory (default: $XDG_STATE_HOME/cenci/babysit)")
		stopArgs := args[1:]
		if len(stopArgs) == 0 {
			fmt.Fprintln(os.Stderr, "cenci babysit stop: usage: cenci babysit stop <pr>")
			os.Exit(2)
		}
		pr := stopArgs[0]
		_ = fs.Parse(stopArgs[1:])
		rejectExtra("cenci babysit stop", fs.Args())
		if err := babysit.Stop(pr, *stateDir); err != nil {
			fmt.Fprintf(os.Stderr, "cenci babysit stop: %v\n", err)
			os.Exit(1)
		}
		return
	}

	opts, ok := parseBabysitArgs(args)
	if !ok {
		fmt.Fprintln(os.Stderr, babysitUsage)
		os.Exit(2)
	}
	if err := babysit.Run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "cenci babysit: %v\n", err)
		os.Exit(1)
	}
}
