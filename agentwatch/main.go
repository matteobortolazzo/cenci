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

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/closecmd"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/config"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/daemon"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/dispatch"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/frontend"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/frontend/status"
	tmuxfe "github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/frontend/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/run"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/tmux"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/pkg/watch"
)

// version is stamped at build time via -ldflags "-X main.version=<ver>".
// It defaults to "dev" for local/test builds.
var version = "dev"

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
	case "close":
		runClose(os.Args[2:])
	case "version", "--version", "-version":
		runVersion()
	case "socket-dir":
		runSocketDir()
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

// runVersion prints the binary's stamped version and exits 0. It performs no
// side effects (no daemon start, no config load, no dispatch pass), so it is
// safe to use as a capability/version probe.
func runVersion() {
	fmt.Printf("agentwatch %s\n", version)
}

// runSocketDir prints the resolved agentwatch socket directory to stdout and
// exits 0, so shell consumers (dev-sandbox's agent-sand) don't reimplement
// the XDG-vs-fallback logic themselves and risk drift. Exits 1 on error.
func runSocketDir() {
	dir, err := ipc.DefaultSocketDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch socket-dir: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(dir)
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

	// The embedded dispatch + reconcile loop always runs alongside the daemon
	// (#220); its own per-tick config reload decides whether dispatch.loopEnabled
	// is on, so a disabled config simply idles the loop rather than gating
	// whether it starts at all.
	ch := make(chan ipc.AttentionUpdate, 1)
	var attention <-chan ipc.AttentionUpdate = ch
	go dispatch.RunCombinedLoop(ctx, "", &tmux.ExecClient{}, &dispatch.GHMutator{}, os.Stdout, ch)

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
		// AgentID is set when the hook fires inside a subagent (Task tool) call.
		AgentID string `json:"agent_id"`
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
		AgentID:          hookInput.AgentID,
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
	slug := fs.String("slug", "", "window-name slug for free-text runs (ignored for numeric tickets, which are named <number>-<skill>)")
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
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
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
		case "loop":
			runDispatchLoop(args[1:])
			return
		default:
			// A stale/rebuilt binary invoked with a typo'd verb (e.g. "statas")
			// must never silently fall through to a real dispatch pass: Go's
			// flag parser stops at the first positional and would otherwise
			// discard everything after it, including --json/--dir.
			fmt.Fprintf(os.Stderr, "agentwatch dispatch: unknown subcommand %q\n", args[0])
			os.Exit(2)
		}
	}

	fs := flag.NewFlagSet("dispatch", flag.ExitOnError)
	once := fs.Bool("once", false, "run a single dispatch pass then exit (default)")
	interval := fs.Duration("interval", 0, "run continuously on this interval (e.g. 5m); mutually exclusive with --once")
	dryRun := fs.Bool("dry-run", false, "print the decision table without dispatching")
	reconcile := fs.Bool("reconcile", false, "run a single failure-reconciliation pass instead of a dispatch pass (cron path)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/agentwatch/config.json)")
	model := fs.String("model", "", "model override for every session dispatched this pass (overrides config.json dispatch.model / agents.*.model)")
	_ = fs.Parse(args)

	// Any positional left after flag parsing is unexpected: the flag parser
	// stops at the first non-flag token, so a trailing typo or stray argument
	// would otherwise be silently swallowed along with any flags after it.
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	cfg, err := dispatch.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch: %v\n", err)
		os.Exit(1)
	}
	if *model != "" {
		cfg.Model = *model
	}

	// --reconcile runs the recovery pass once (cron path). It is independent of
	// the dispatch/loop flags.
	if *reconcile {
		if _, err := dispatch.RunReconcileOnce(cfg, &dispatch.GHMutator{}, *dryRun, os.Stdout, dispatch.NewStateStore("")); err != nil {
			fmt.Fprintf(os.Stderr, "agentwatch dispatch: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctrl := &tmux.ExecClient{}
	// --interval self-loops; otherwise a single pass. --once wins if both given.
	if *interval > 0 && !*once {
		if err := dispatch.RunLoop(*configPath, ctrl, &dispatch.GHMutator{}, *interval, os.Stdout, *model); err != nil {
			fmt.Fprintf(os.Stderr, "agentwatch dispatch: %v\n", err)
			os.Exit(1)
		}
		return
	}
	prior := 0
	if _, err := dispatch.RunOnce(cfg, ctrl, &dispatch.GHMutator{}, *dryRun, os.Stdout, &prior); err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch: %v\n", err)
		os.Exit(1)
	}
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
		out := struct {
			Repo     string              `json:"repo"`
			Dir      string              `json:"dir"`
			Enrolled bool                `json:"enrolled"`
			Loop     watch.DispatchState `json:"loop"`
		}{
			Repo:     enrollment.Repo,
			Dir:      enrollment.Dir,
			Enrolled: enrollment.Enrolled,
			Loop:     dispatch.ResolveDispatchState(*configPath, watch.DefaultSocketPath(), os.Stderr),
		}
		data, err := json.Marshal(out)
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

// runDispatchLoop implements `agentwatch dispatch loop on|off|status`. All
// three verbs resolve and print the current DispatchState via the same
// socket-first/config-fallback path (dispatch.ResolveDispatchState); on/off
// additionally persist the toggle to config.json first. Default output is
// human-readable; --json prints the raw DispatchState.
func runDispatchLoop(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "agentwatch dispatch loop: expected a subcommand: on, off, or status")
		os.Exit(2)
	}
	verb := args[0]

	fs := flag.NewFlagSet("loop "+verb, flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/agentwatch/config.json)")
	jsonOut := fs.Bool("json", false, "print result as JSON")
	_ = fs.Parse(args[1:])

	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "agentwatch dispatch loop: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	switch verb {
	case "on":
		if err := dispatch.SetLoopEnabled(*configPath, true); err != nil {
			fmt.Fprintf(os.Stderr, "agentwatch dispatch loop: %v\n", err)
			os.Exit(1)
		}
	case "off":
		if err := dispatch.SetLoopEnabled(*configPath, false); err != nil {
			fmt.Fprintf(os.Stderr, "agentwatch dispatch loop: %v\n", err)
			os.Exit(1)
		}
	case "status":
		// no mutation; just resolve and print below.
	default:
		fmt.Fprintf(os.Stderr, "agentwatch dispatch loop: unknown subcommand %q\n", verb)
		os.Exit(2)
	}

	state := dispatch.ResolveDispatchState(*configPath, watch.DefaultSocketPath(), os.Stderr)

	if *jsonOut {
		data, err := json.Marshal(state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentwatch dispatch loop: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}

	fmt.Print(renderDispatchState(state))
}

// renderDispatchState formats a watch.DispatchState as human-readable text
// for `dispatch loop on|off|status`. DaemonRunning gates whether the
// live-daemon fields (pass_running, last_run_at, last_dispatched,
// last_skipped, last_error) are shown. Since #220 the daemon always runs the
// embedded dispatch loop and publishes DaemonRunning=true via its broadcast
// snapshot, so this branch fires whenever a daemon is reachable; it only
// stays false when ResolveDispatchState falls back to config (no daemon
// running).
func renderDispatchState(state watch.DispatchState) string {
	var b strings.Builder

	enabledStr := "disabled"
	if state.Enabled {
		enabledStr = "enabled"
	}
	fmt.Fprintf(&b, "Dispatch loop: %s\n", enabledStr)

	daemonStr := "not running"
	if state.DaemonRunning {
		daemonStr = "running"
	}
	fmt.Fprintf(&b, "  daemon:   %s\n", daemonStr)

	if state.Interval != "" {
		fmt.Fprintf(&b, "  interval: %s\n", state.Interval)
	}

	if state.DaemonRunning {
		fmt.Fprintf(&b, "  pass_running: %v\n", state.PassRunning)
		if state.LastRunAt != "" {
			fmt.Fprintf(&b, "  last_run_at: %s\n", state.LastRunAt)
		}
		fmt.Fprintf(&b, "  last_dispatched: %d\n", state.LastDispatched)
		fmt.Fprintf(&b, "  last_skipped: %d\n", state.LastSkipped)
		if state.LastError != "" {
			fmt.Fprintf(&b, "  last_error: %s\n", state.LastError)
		}
	}

	return b.String()
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
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
		fmt.Fprintf(os.Stderr, "agentwatch status: %v\n", err)
		os.Exit(1)
	}
}

// runClose implements `agentwatch close <ticket-number|window-name> [--force]
// [--dry-run] [--socket PATH]`. It resolves the target against the daemon's
// live window registry (never re-derives a tmux target itself) and closes
// every matched window that isn't running/waiting for input, unless --force
// is given. See internal/closecmd for the matching/kill/guard logic; this
// function only parses flags and renders the result.
func runClose(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "agentwatch close: usage: agentwatch close <ticket-number|window-name> [--force] [--dry-run] [--socket PATH]")
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
		fmt.Fprintf(os.Stderr, "agentwatch close: unexpected argument %q\n", extra[0])
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
		fmt.Fprintf(os.Stderr, "agentwatch close: %v\n", err)
		os.Exit(1)
	}

	printCloseDecisions(os.Stdout, target, decisions)

	if err != nil {
		fmt.Fprintf(os.Stderr, "agentwatch close: %v\n", err)
		os.Exit(1)
	}
}

// printCloseDecisions renders the outcome of `agentwatch close`. No matches
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
