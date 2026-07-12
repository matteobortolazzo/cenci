package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/config"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/daemon"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/dispatch"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/frontend"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/frontend/status"
	tmuxfe "github.com/matteobortolazzo/agent-stack/agentwatch/internal/frontend/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/run"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/tmux"
)

func main() {
	if len(os.Args) < 2 {
		runDaemon(os.Args[1:])
		return
	}
	switch os.Args[1] {
	case "daemon":
		runDaemon(os.Args[2:])
	case "status", "waybar": // "waybar" is a hidden alias for existing consumers
		runStatus(os.Args[2:])
	case "notify":
		runNotify(os.Args[2:])
	case "run":
		runRun(os.Args[2:])
	case "dispatch":
		runDispatch(os.Args[2:])
	default:
		if strings.HasPrefix(os.Args[1], "-") {
			// Flags like -v go to daemon.
			runDaemon(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "agentwatch: unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

func runDaemon(args []string) {
	cfg := config.Default()
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)

	fs.BoolVar(&cfg.Verbose, "v", false, "verbose logging")
	fs.StringVar(&cfg.SocketPath, "socket", ipc.DefaultSocketPath(), "IPC broadcast socket path (empty to disable)")
	fs.StringVar(&cfg.EventSocketPath, "event-socket", ipc.DefaultEventSocketPath(), "event socket path for hook notifications")

	sweepSec := int(cfg.SweepInterval / time.Second)
	fs.IntVar(&sweepSec, "sweep", sweepSec, "stale session sweep interval in seconds")
	fs.DurationVar(&cfg.SessionTTL, "session-ttl", cfg.SessionTTL, "idle expiry for sessions outside tmux (e.g. 2h)")

	fs.StringVar(&cfg.StyleIdle, "style-idle", cfg.StyleIdle, "tmux style for idle state")
	fs.StringVar(&cfg.StyleRunning, "style-running", cfg.StyleRunning, "tmux style for running state")
	fs.StringVar(&cfg.StyleDone, "style-done", cfg.StyleDone, "tmux style for done state")
	fs.StringVar(&cfg.StyleNeedInput, "style-input", cfg.StyleNeedInput, "tmux style for need-input state")
	fs.StringVar(&cfg.SymbolIdle, "symbol-idle", cfg.SymbolIdle, "symbol prefix for idle state")
	fs.StringVar(&cfg.SymbolRunning, "symbol-running", cfg.SymbolRunning, "symbol prefix for running state")
	fs.StringVar(&cfg.SymbolDone, "symbol-done", cfg.SymbolDone, "symbol prefix for done state")
	fs.StringVar(&cfg.SymbolNeedInput, "symbol-input", cfg.SymbolNeedInput, "symbol prefix for need-input state")
	fs.StringVar(&cfg.StyleStopped, "style-stopped", cfg.StyleStopped, "tmux style for stopped (interrupted) state")
	fs.StringVar(&cfg.SymbolStopped, "symbol-stopped", cfg.SymbolStopped, "symbol prefix for stopped (interrupted) state")
	_ = fs.Parse(args)

	cfg.SweepInterval = time.Duration(sweepSec) * time.Second

	if cfg.Verbose {
		log.Printf("agentwatch starting (event-driven, sweep every %s)", cfg.SweepInterval)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		if cfg.Verbose {
			log.Printf("received %s, shutting down", sig)
		}
		cancel()
	}()

	// tmux is the one interactive frontend; it is constructed here and
	// injected so the daemon core stays tmux-free.
	fe := tmuxfe.New(cfg, &tmux.ExecClient{})

	// When dispatch.daemonInterval is configured, run the embedded dispatch +
	// reconcile loop in one process and feed its failure overlay into the daemon
	// snapshot. Interval 0 (the default) leaves daemon behavior unchanged.
	var attention <-chan []ipc.WindowState
	if dcfg, err := dispatch.LoadConfig(""); err != nil {
		log.Printf("dispatch config: %v (embedded loop disabled)", err)
	} else if dcfg.DaemonInterval > 0 {
		ch := make(chan []ipc.WindowState, 1)
		attention = ch
		go dispatch.RunCombinedLoop(ctx, "", &tmux.ExecClient{}, &dispatch.GHMutator{}, dcfg.DaemonInterval, os.Stdout, ch)
	}

	if err := daemon.Run(ctx, cfg, fe, attention); err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch: %v\n", err)
		os.Exit(1)
	}
}

func runNotify(args []string) {
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	socketPath := fs.String("event-socket", ipc.DefaultEventSocketPath(), "event socket path")
	agent := fs.String("agent", "", "agent name (claude or codex)")
	_ = fs.Parse(args)

	// Read hook JSON from stdin.
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(0) // fail silently
	}

	// Parse the hook input to extract event type and relevant fields.
	var hookInput struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
		// Notification fields
		Notification struct {
			Type string `json:"type"`
		} `json:"notification"`
		// PreToolUse fields
		ToolName string `json:"tool_name"`
		// UserPromptSubmit field. It is reduced to a compact label before IPC.
		Prompt string `json:"prompt"`
		// PostToolUseFailure fields
		IsInterrupt bool `json:"is_interrupt"`
	}
	if err := json.Unmarshal(data, &hookInput); err != nil {
		os.Exit(0) // fail silently
	}

	// TMUX_PANE may be empty: sessions outside tmux (plain terminals,
	// dev-sandbox) are still tracked by the daemon as paneless sessions.
	tmuxPane := os.Getenv("TMUX_PANE")

	event := ipc.HookEvent{
		EventType:        hookInput.HookEventName,
		SessionID:        hookInput.SessionID,
		Agent:            strings.ToLower(strings.TrimSpace(*agent)),
		TmuxPane:         tmuxPane,
		NotificationType: hookInput.Notification.Type,
		ToolName:         hookInput.ToolName,
		IsInterrupt:      hookInput.IsInterrupt,
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
	}
	if event.Agent == "codex" && event.EventType == "UserPromptSubmit" {
		event.TaskName = frontend.PromptTaskName(hookInput.Prompt)
	}

	// Delivery is silent and non-fatal. For the default socket it starts a
	// missing daemon on demand and retries this exact event once.
	daemon.DeliverEvent(*socketPath, event)
}

func runRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	agent := fs.String("agent", "", "agent to launch (claude, codex, ...); default from config or claude")
	sandbox := fs.Bool("sandbox", false, "launch inside the dev-sandbox container (the default)")
	noSandbox := fs.Bool("no-sandbox", false, "force a host launch (overrides the sandbox default)")
	model := fs.String("model", "", "model override passed to the agent")
	session := fs.String("session", "", "target tmux session (default: current session)")
	slug := fs.String("slug", "", "window-name slug (default: gh issue title, else the bare ticket)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/agentwatch/config.json)")
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
		fmt.Fprintln(os.Stderr, "agentwatch run: usage: agentwatch run <workflow> [ticket] [flags]")
		os.Exit(2)
	}

	opts := run.Opts{
		Workflow:   positionals[0],
		Agent:      *agent,
		Model:      *model,
		Session:    *session,
		Slug:       *slug,
		ConfigPath: *configPath,
		DryRun:     *dryRun,
	}
	// Everything after the workflow is the skill argument: a ticket id or task
	// description plus optional context (mirrors `/agentflow:<workflow> $ARGUMENTS`).
	// Join so unquoted multi-word context survives shell splitting.
	if len(positionals) >= 2 {
		opts.Ticket = strings.Join(positionals[1:], " ")
	}
	if *sandbox || *noSandbox {
		opts.SandboxSet = true
		opts.Sandbox = *sandbox && !*noSandbox
	}

	if err := run.Run(opts, &tmux.ExecClient{}); err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch run: %v\n", err)
		os.Exit(1)
	}
}

func runDispatch(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "enroll":
			runDispatchEnroll(args[1:])
			return
		case "unenroll":
			runDispatchUnenroll(args[1:])
			return
		case "status":
			runDispatchStatus(args[1:])
			return
		}
	}

	fs := flag.NewFlagSet("dispatch", flag.ExitOnError)
	once := fs.Bool("once", false, "run a single dispatch pass then exit (default)")
	interval := fs.Duration("interval", 0, "run continuously on this interval (e.g. 5m); mutually exclusive with --once")
	dryRun := fs.Bool("dry-run", false, "print the decision table without dispatching")
	reconcile := fs.Bool("reconcile", false, "run a single failure-reconciliation pass instead of a dispatch pass (cron path)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/agentwatch/config.json)")
	_ = fs.Parse(args)

	cfg, err := dispatch.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch: %v\n", err)
		os.Exit(1)
	}

	// --reconcile runs the recovery pass once (cron path). It is independent of
	// the dispatch/loop flags.
	if *reconcile {
		dispatch.RunReconcileOnce(cfg, &dispatch.GHMutator{}, *dryRun, os.Stdout, dispatch.NewStateStore(""))
		return
	}

	ctrl := &tmux.ExecClient{}
	// --interval self-loops; otherwise a single pass. --once wins if both given.
	if *interval > 0 && !*once {
		dispatch.RunLoop(*configPath, ctrl, *interval, os.Stdout)
		return
	}
	prior := 0
	dispatch.RunOnce(cfg, ctrl, *dryRun, os.Stdout, &prior)
}

func runDispatchEnroll(args []string) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	dir := fs.String("dir", ".", "repo directory to enroll (default: current directory)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/agentwatch/config.json)")
	_ = fs.Parse(args)

	identity, err := dispatch.DetectRepoIdentity(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch enroll: %v\n", err)
		os.Exit(1)
	}

	changed, err := dispatch.EnrollRepo(*configPath, identity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch enroll: %v\n", err)
		os.Exit(1)
	}
	if changed {
		fmt.Printf("Enrolled %s (%s)\n", identity.Repo, identity.Dir)
	} else {
		fmt.Printf("Already enrolled %s (%s)\n", identity.Repo, identity.Dir)
	}
}

func runDispatchUnenroll(args []string) {
	fs := flag.NewFlagSet("unenroll", flag.ExitOnError)
	dir := fs.String("dir", ".", "repo directory to unenroll (default: current directory)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/agentwatch/config.json)")
	repo := fs.String("repo", "", "repo (owner/name) to unenroll, bypassing git detection")
	_ = fs.Parse(args)

	dirSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "dir" {
			dirSet = true
		}
	})

	if *repo != "" && dirSet {
		fmt.Fprintln(os.Stderr, "agentwatch dispatch unenroll: --repo and --dir are mutually exclusive")
		os.Exit(2)
	}

	var target string
	if *repo != "" {
		target = *repo
	} else {
		identity, err := dispatch.DetectRepoIdentity(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentwatch dispatch unenroll: %v\n", err)
			os.Exit(1)
		}
		target = identity.Repo
	}

	changed, err := dispatch.UnenrollRepo(*configPath, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch unenroll: %v\n", err)
		os.Exit(1)
	}
	if changed {
		fmt.Printf("Unenrolled %s\n", target)
	} else {
		fmt.Printf("Not enrolled: %s\n", target)
	}
}

func runDispatchStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("dir", ".", "repo directory to query (default: current directory)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/agentwatch/config.json)")
	jsonOut := fs.Bool("json", false, "print result as JSON")
	_ = fs.Parse(args)

	identity, err := dispatch.DetectRepoIdentity(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch status: %v\n", err)
		os.Exit(1)
	}

	enrollment, err := dispatch.QueryEnrollment(*configPath, identity.Repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch status: %v\n", err)
		os.Exit(1)
	}
	if !enrollment.Enrolled {
		enrollment.Dir = identity.Dir
	}

	if *jsonOut {
		data, err := json.Marshal(enrollment)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentwatch dispatch status: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}

	if enrollment.Enrolled {
		fmt.Printf("Enrolled %s (%s)\n", enrollment.Repo, enrollment.Dir)
	} else {
		fmt.Printf("Not enrolled: %s\n", enrollment.Repo)
	}
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	defaults := config.Default()
	wcfg := status.Config{
		SymbolIdle:      defaults.SymbolIdle,
		SymbolRunning:   defaults.SymbolRunning,
		SymbolDone:      defaults.SymbolDone,
		SymbolNeedInput: defaults.SymbolNeedInput,
		SymbolStopped:   defaults.SymbolStopped,
		SymbolFailed:    defaults.SymbolFailed,
	}
	fs.StringVar(&wcfg.SocketPath, "socket", ipc.DefaultSocketPath(), "IPC socket path")
	fs.StringVar(&wcfg.SymbolIdle, "symbol-idle", wcfg.SymbolIdle, "symbol for idle state")
	fs.StringVar(&wcfg.SymbolRunning, "symbol-running", wcfg.SymbolRunning, "symbol for running state")
	fs.StringVar(&wcfg.SymbolDone, "symbol-done", wcfg.SymbolDone, "symbol for done state")
	fs.StringVar(&wcfg.SymbolNeedInput, "symbol-input", wcfg.SymbolNeedInput, "symbol for need-input state")
	fs.StringVar(&wcfg.SymbolStopped, "symbol-stopped", wcfg.SymbolStopped, "symbol for stopped (interrupted) state")
	fs.StringVar(&wcfg.SymbolFailed, "symbol-failed", wcfg.SymbolFailed, "symbol for dispatch-failed state")
	_ = fs.Parse(args)

	if err := status.Run(wcfg); err != nil {
		if errors.Is(err, status.ErrNoOutput) {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "agentwatch status: %v\n", err)
		os.Exit(1)
	}
}
