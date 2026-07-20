package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// runDiagnose implements `cenci diagnose <session> [--agent
// claude|codex|opencode]`: a read-only report on a sandbox session (container
// status/exit, the timestamped startup marker, recent logs, daemon/socket
// reachability, plugin + image versions, and mounted volumes), each failure
// annotated with a registered errcode.Code and a fatal/degraded/warning
// severity — see internal/sandbox/launcher/diagnose.go. Unlike `cenci open`,
// diagnose never launches, attaches, or wires the daemon; it only reads.
//
// diagnose is a report, not a pass/fail gate: a successful render exits 0
// even when it finds fatal/degraded issues. Only usage errors (missing
// session, unknown flag, invalid --agent) and cwd/home/runtime resolution
// failures produce a non-zero exit.
func runDiagnose(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci diagnose: usage: cenci diagnose <session> [--agent claude|codex|opencode]")
		os.Exit(2)
	}
	session := args[0]

	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agent := fs.String("agent", "claude", "agent whose sandbox session to diagnose (claude, codex, or opencode)")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(2)
	}
	rejectExtra("cenci diagnose", fs.Args())

	if err := launcher.ValidateAgent(*agent); err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: cannot determine working directory: %v\n", err)
		os.Exit(1)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	scope := launcher.ComputeScope(*agent, session, cwd, home)

	eng, err := launcher.New(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(1)
	}
	if err := eng.Diagnose(scope); err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(1)
	}
}
