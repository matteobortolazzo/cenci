package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// babysitUsage is the one-line usage hint for `cenci babysit <pr> --agent
// ...`, printed to stderr on a malformed-CLI invocation (exit 2).
const babysitUsage = "cenci babysit: usage: cenci babysit <pr> --agent <claude|codex> [--interval 15m] [--once] [--session <name>] [--dir <path>]"

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
	agent := fs.String("agent", "", "agent to launch for actionable work (claude or codex; OpenCode is temporarily disabled)")
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

// babysitAgentRefusal returns the reason `cenci babysit` must refuse to arm
// agent, or "" when arming may proceed. A disabled agent (OpenCode) keeps its
// own disabled reason; otherwise the agent must provide every arm-time workflow
// according to has (#1215). runBabysit passes run.BuiltinCapability; tests
// inject a stub predicate.
func babysitAgentRefusal(agent string, has func(agent, workflow string) bool) string {
	if reason := sandbox.AgentDisabledReason(agent); reason != "" {
		return reason
	}
	return babysit.CapabilityRefusal(agent, has)
}

// babysitStatusUsage is the usage line for `cenci babysit status <pr>`,
// printed to stderr on a malformed invocation (exit 2).
const babysitStatusUsage = "cenci babysit status: usage: cenci babysit status <pr> [--state-dir <dir>]"

// runBabysitStatus implements `cenci babysit status <pr>` (#1390): one JSON
// object describing the supervisor for the PR, read from the host state file
// or, inside a sandbox, from the host daemon. Usage errors exit 2, runtime
// failures (no supervisor, unreadable state, an unreachable daemon) exit 1.
func runBabysitStatus(args []string) {
	fs := flag.NewFlagSet("babysit status", flag.ExitOnError)
	stateDir := fs.String("state-dir", "", "state directory (default: $XDG_STATE_HOME/cenci/babysit)")
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, babysitStatusUsage)
		os.Exit(2)
	}
	pr := args[0]
	_ = fs.Parse(args[1:])
	rejectExtra("cenci babysit status", fs.Args())
	if n, err := strconv.Atoi(strings.TrimPrefix(pr, "#")); err != nil || n <= 0 {
		fmt.Fprintf(os.Stderr, "cenci babysit status: PR must be a positive integer, got %q\n%s\n", pr, babysitStatusUsage)
		os.Exit(2)
	}
	if err := babysit.Status(pr, *stateDir); err != nil {
		fmt.Fprintf(os.Stderr, "cenci babysit status: %v\n", err)
		os.Exit(1)
	}
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
	if len(args) > 0 && args[0] == "status" {
		runBabysitStatus(args[1:])
		return
	}

	opts, ok := parseBabysitArgs(args)
	if !ok {
		fmt.Fprintln(os.Stderr, babysitUsage)
		os.Exit(2)
	}
	if reason := babysitAgentRefusal(opts.Agent, run.BuiltinCapability); reason != "" {
		fmt.Fprintf(os.Stderr, "cenci babysit: %s\n", reason)
		os.Exit(2)
	}
	if err := babysit.Run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "cenci babysit: %v\n", err)
		os.Exit(1)
	}
}
