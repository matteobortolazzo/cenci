package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

func runRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	agent := fs.String("agent", "", "agent to launch (claude, codex, ...); default from config or claude")
	sandbox := fs.Bool("sandbox", false, "launch inside the sandbox container (the default)")
	noSandbox := fs.Bool("no-sandbox", false, "force a host launch (overrides the sandbox default)")
	model := fs.String("model", "", "model override passed to the agent")
	session := fs.String("session", "", "target tmux session (default: current session)")
	dir := fs.String("dir", "", "working directory the window starts in (default: current)")
	slug := fs.String("slug", "", "window-name slug for free-text runs (ignored for numeric tickets, which are named <number>-<skill>)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	dryRun := fs.Bool("dry-run", false, "print the resolved session, window name, and command without spawning")
	unattended := fs.Bool("unattended", false, "pin CENCI_ATTENDED=0 on the launched agent command (cenci babysit sets it)")
	tmuxSocket := fs.String("tmux-socket", "", "tmux server socket to launch on (absolute path; default: the invoking pane's server, else the cenci socket)")

	// The stdlib flag parser stops at the first positional, but the documented
	// form is `run <workflow> [ticket] [flags]`. Peel leading positionals, parse
	// the rest as flags, then fold in any trailing positionals.
	var positionals []string
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		positionals = append(positionals, args[i])
		i++
	}
	_ = fs.Parse(args[i:])
	positionals = append(positionals, fs.Args()...)

	if len(positionals) < 1 {
		fmt.Fprintln(os.Stderr, "cenci run: usage: cenci run <workflow> [ticket] [flags]")
		os.Exit(2)
	}

	// A usage error, before any tmux call. The rejected value is never echoed:
	// it may hold a tab or newline that would break the one-line hint.
	if *tmuxSocket != "" {
		if !tmux.ValidSocket(*tmuxSocket) {
			fmt.Fprintln(os.Stderr, "cenci run: --tmux-socket must be an absolute path with no tab or newline")
			os.Exit(2)
		}
		// Without --session the target is $TMUX_PANE's session, a %N that only
		// means something on the invoking pane's own server (#1431).
		paneSocket, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
		if strings.TrimSpace(*session) == "" && *tmuxSocket != paneSocket {
			fmt.Fprintln(os.Stderr, "cenci run: --tmux-socket is not this pane's tmux server; pass --session <name> to launch on it")
			os.Exit(2)
		}
	}

	opts := run.Opts{
		Workflow:   positionals[0],
		Agent:      *agent,
		Model:      *model,
		Session:    *session,
		TmuxSocket: *tmuxSocket,
		Dir:        *dir,
		Slug:       *slug,
		ConfigPath: *configPath,
		DryRun:     *dryRun,
		Unattended: *unattended,
	}
	// Everything after the workflow is the skill argument: a ticket id or task
	// description plus optional context (mirrors `/cenci:<workflow> $ARGUMENTS`).
	// Join so unquoted multi-word context survives shell splitting.
	if len(positionals) >= 2 {
		opts.Ticket = strings.Join(positionals[1:], " ")
	}
	if *sandbox || *noSandbox {
		opts.SandboxSet = true
		opts.Sandbox = *sandbox && !*noSandbox
	}

	if err := run.Run(opts, &tmux.ExecClient{}); err != nil {
		fmt.Fprintf(os.Stderr, "cenci run: %v\n", err)
		// Exit 3: the window was spawned but its post-spawn setup failed
		// (#1455). A self-exec caller (babysit) cannot see the Go sentinel, so
		// the status is its only signal that an agent session is starting.
		if errors.Is(err, run.ErrWindowSpawned) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}
