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

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/config"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/daemon"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/frontend/status"
	tmuxfe "github.com/matteobortolazzo/claude-tools/agentwatch/internal/frontend/tmux"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/run"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/tmux"
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

	var sweepSec int
	fs.IntVar(&sweepSec, "sweep", 30, "stale session sweep interval in seconds")
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
	if err := daemon.Run(ctx, cfg, fe); err != nil {
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

	// Send event to daemon, ignore errors (daemon might not be running).
	_ = ipc.SendEvent(*socketPath, event)
}

func runRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	agent := fs.String("agent", "", "agent to launch (claude, codex, ...); default from config or claude")
	sandbox := fs.Bool("sandbox", false, "launch inside the dev-sandbox container")
	noSandbox := fs.Bool("no-sandbox", false, "force a host launch (overrides the config default)")
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
	if len(positionals) >= 2 {
		opts.Ticket = positionals[1]
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

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	defaults := config.Default()
	wcfg := status.Config{
		SymbolIdle:      defaults.SymbolIdle,
		SymbolRunning:   defaults.SymbolRunning,
		SymbolDone:      defaults.SymbolDone,
		SymbolNeedInput: defaults.SymbolNeedInput,
		SymbolStopped:   defaults.SymbolStopped,
	}
	fs.StringVar(&wcfg.SocketPath, "socket", ipc.DefaultSocketPath(), "IPC socket path")
	fs.StringVar(&wcfg.SymbolIdle, "symbol-idle", wcfg.SymbolIdle, "symbol for idle state")
	fs.StringVar(&wcfg.SymbolRunning, "symbol-running", wcfg.SymbolRunning, "symbol for running state")
	fs.StringVar(&wcfg.SymbolDone, "symbol-done", wcfg.SymbolDone, "symbol for done state")
	fs.StringVar(&wcfg.SymbolNeedInput, "symbol-input", wcfg.SymbolNeedInput, "symbol for need-input state")
	fs.StringVar(&wcfg.SymbolStopped, "symbol-stopped", wcfg.SymbolStopped, "symbol for stopped (interrupted) state")
	_ = fs.Parse(args)

	if err := status.Run(wcfg); err != nil {
		if errors.Is(err, status.ErrNoOutput) {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "agentwatch status: %v\n", err)
		os.Exit(1)
	}
}
