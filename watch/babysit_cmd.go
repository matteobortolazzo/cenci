package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/babysit"
)

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

	fs := flag.NewFlagSet("babysit", flag.ExitOnError)
	agent := fs.String("agent", "", "agent to launch for actionable work (claude or codex)")
	interval := fs.Duration("interval", 15*time.Minute, "base polling interval")
	once := fs.Bool("once", false, "run one tick in the foreground")
	stateDir := fs.String("state-dir", "", "state directory (default: $XDG_STATE_HOME/cenci/babysit)")
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci babysit: usage: cenci babysit <pr> --agent <claude|codex> [--interval 15m] [--once]")
		os.Exit(2)
	}
	pr := strings.TrimPrefix(args[0], "#")
	_ = fs.Parse(args[1:])
	if len(fs.Args()) != 0 || (*agent != "claude" && *agent != "codex") {
		fmt.Fprintln(os.Stderr, "cenci babysit: usage: cenci babysit <pr> --agent <claude|codex> [--interval 15m] [--once]")
		os.Exit(2)
	}
	opts := babysit.Options{PR: pr, Agent: *agent, Interval: *interval, Once: *once, StateDir: *stateDir}
	if err := babysit.Run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "cenci babysit: %v\n", err)
		os.Exit(1)
	}
}
