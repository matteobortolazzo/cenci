package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/run"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux"
)

func runRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	agent := fs.String("agent", "", "agent to launch (claude, codex, ...); default from config or claude")
	sandbox := fs.Bool("sandbox", false, "launch inside the sandbox container (the default, except host-only workflows like design)")
	noSandbox := fs.Bool("no-sandbox", false, "force a host launch (overrides the sandbox default)")
	model := fs.String("model", "", "model override passed to the agent")
	session := fs.String("session", "", "target tmux session (default: current session)")
	dir := fs.String("dir", "", "working directory the window starts in (default: current)")
	slug := fs.String("slug", "", "window-name slug for free-text runs (ignored for numeric tickets, which are named <number>-<skill>)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	dryRun := fs.Bool("dry-run", false, "print the resolved session, window name, and command without spawning")

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

	opts := run.Opts{
		Workflow:   positionals[0],
		Agent:      *agent,
		Model:      *model,
		Session:    *session,
		Dir:        *dir,
		Slug:       *slug,
		ConfigPath: *configPath,
		DryRun:     *dryRun,
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
		if errors.Is(err, run.ErrHostOnlyWorkflow) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
