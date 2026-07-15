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
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v4/internal/closecmd"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/frontend/status"
	tmuxfe "github.com/matteobortolazzo/cenci/watch/v4/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v4/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v4/pkg/watch"
)

// version is stamped at build time via -ldflags "-X main.version=<ver>".
// It defaults to "dev" for local/test builds.
var version = "dev"

func main() {
	// argv[0] alias: a binary invoked (directly or via a symlink/copy) as
	// "cn" behaves as `cenci open <args>` — the one forward-looking
	// exception ahead of the future "cenci" rename (this PR keeps every
	// other name under the old cenci/cenci-sand naming).
	if filepath.Base(os.Args[0]) == "cn" {
		runOpen(os.Args[1:])
		return
	}

	// BREAKING: bare `cenci` (and any unrecognized top-level subcommand
	// or flag) used to fall through to running the daemon in the foreground.
	// It now always prints usage and exits 2 — the daemon only starts via the
	// explicit `daemon` subcommand group below. This makes `cenci` with
	// a typo'd or missing subcommand fail loudly instead of silently
	// launching a long-running foreground process.
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "daemon":
		runDaemonGroup(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "widget-json", "waybar": // "waybar" is a hidden alias kept for existing consumers
		runWidgetJSON(os.Args[2:])
	case "notify":
		runNotify(os.Args[2:])
	case "run":
		runRun(os.Args[2:])
	case "dispatch":
		runDispatch(os.Args[2:])
	case "close":
		runClose(os.Args[2:])
	case "sandbox":
		runSandboxGroup(os.Args[2:])
	case "open":
		runOpen(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	case "update":
		runUpdate(os.Args[2:])
	case "version", "--version", "-version":
		runVersion()
	case "socket-dir":
		runSocketDir()
	case "help", "-h", "--help":
		printUsage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "cenci: unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

// printUsage writes the top-level command overview to w. It is what bare
// `cenci`, `cenci help`/`-h`/`--help`, and any unrecognized
// subcommand or flag print.
func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `cenci — attention layer for Claude Code / Codex tmux sessions

Usage:
  cenci <command> [flags]

Commands:
  daemon start|stop|restart|status   manage the background daemon (bare "daemon" acts as "start")
  status                             human-readable session/daemon/dispatch overview
  widget-json                        machine-readable status for bar widgets (Waybar custom module protocol); "waybar" is a hidden alias
  notify                             deliver a hook event to the daemon (used by installed hooks)
  run                                dispatch a workflow into a new tmux window
  dispatch                           fleet auto-dispatch (enroll/unenroll/status/loop)
  close                              close a finished/idle agent window
  sandbox                            manage the dev-sandbox container (build|build-base|prune|update-plugins|reseed-creds|reap-orphans|ls|stop)
  open [shortcut]                    launch or attach an interactive sandbox session (aliased by the "cn" binary name)
  doctor                             check prerequisites and installed stack components, change nothing (delegates to the installed cenci wrapper)
  update                             update installed plugins and restart the daemon (delegates to the installed cenci wrapper)
  version                            print the binary version
  socket-dir                         print the resolved socket directory

Run 'cenci <command> -h' for command-specific flags where supported.
`)
}

// runVersion prints the binary's stamped version and exits 0. It performs no
// side effects (no daemon start, no config load, no dispatch pass), so it is
// safe to use as a capability/version probe.
func runVersion() {
	fmt.Printf("cenci %s\n", version)
}

// runSocketDir prints the resolved cenci socket directory to stdout and
// exits 0, so shell consumers (dev-sandbox's cenci-sand) don't reimplement
// the XDG-vs-fallback logic themselves and risk drift. Exits 1 on error.
func runSocketDir() {
	dir, err := ipc.DefaultSocketDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci socket-dir: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(dir)
}

// daemonRestartReadyTimeout bounds how long `daemon restart` waits for the
// freshly spawned daemon to become reachable before reporting failure. It
// mirrors internal/daemon's EnsureRunning readyTimeout default.
const daemonRestartReadyTimeout = 3 * time.Second

// daemonRestartPollInterval is the polling cadence within daemonRestartReadyTimeout.
const daemonRestartPollInterval = 50 * time.Millisecond

// runDaemonGroup implements `cenci daemon [start|stop|restart|status]`.
// A bare `daemon` (no args) or `daemon` followed only by flags acts as
// `start`, so `cenci daemon -v` still works exactly as the old bare
// `cenci -v` did.
func runDaemonGroup(args []string) {
	if len(args) == 0 {
		runDaemonStart(args)
		return
	}
	switch args[0] {
	case "start":
		runDaemonStart(args[1:])
	case "stop":
		runDaemonStop(args[1:])
	case "restart":
		runDaemonRestart(args[1:])
	case "status":
		runDaemonStatus(args[1:])
	default:
		if strings.HasPrefix(args[0], "-") {
			runDaemonStart(args)
			return
		}
		fmt.Fprintf(os.Stderr, "cenci daemon: unknown subcommand %q\n", args[0])
		os.Exit(2)
	}
}

// runDaemonStart runs the daemon loop in the foreground — identical to the
// pre-#daemon-lifecycle bare `cenci [-flags]` invocation, plus PID-file
// bookkeeping: a PID file is written at ipc.DefaultPIDPath() once this
// process has become the one live daemon (never on the "already running"
// no-op path — see daemon.Run's onStarted contract) and removed on clean
// shutdown.
func runDaemonStart(args []string) {
	cfg := config.Default()
	fs := flag.NewFlagSet("daemon start", flag.ExitOnError)

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
		log.Printf("cenci starting (event-driven, sweep every %s)", cfg.SweepInterval)
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

	pidPath := ipc.DefaultPIDPath()
	started := false
	onStarted := func() {
		started = true
		if err := daemon.WritePIDFile(pidPath); err != nil && cfg.Verbose {
			log.Printf("warning: could not write pid file %s: %v", pidPath, err)
		}
	}
	defer func() {
		if started {
			daemon.RemovePIDFile(pidPath)
		}
	}()

	if err := daemon.Run(ctx, cfg, fe, attention, onStarted); err != nil {
		fmt.Fprintf(os.Stderr, "cenci: %v\n", err)
		os.Exit(1)
	}
}

// runDaemonStop implements `cenci daemon stop`. Exits 0 whether or not
// a daemon was actually stopped (idempotent) — the message on stdout reports
// which.
func runDaemonStop(args []string) {
	fs := flag.NewFlagSet("daemon stop", flag.ExitOnError)
	_ = fs.Parse(args)
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci daemon stop: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	outcome, err := daemon.Stop(ipc.DefaultEventSocketPath(), ipc.DefaultPIDPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci daemon stop: %v\n", err)
		os.Exit(1)
	}
	if outcome.WasRunning {
		fmt.Printf("stopped daemon (pid %d)\n", outcome.PID)
		return
	}
	fmt.Println("daemon not running")
}

// runDaemonRestart implements `cenci daemon restart`: stop (if running),
// then spawn a fresh detached daemon the same way EnsureRunning does
// (daemon.Spawn — `cenci daemon start`, Setsid'd), and wait briefly for
// it to become reachable.
func runDaemonRestart(args []string) {
	fs := flag.NewFlagSet("daemon restart", flag.ExitOnError)
	_ = fs.Parse(args)
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci daemon restart: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	outcome, err := daemon.Stop(ipc.DefaultEventSocketPath(), ipc.DefaultPIDPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci daemon restart: %v\n", err)
		os.Exit(1)
	}
	if outcome.WasRunning {
		fmt.Printf("stopped daemon (pid %d)\n", outcome.PID)
	}

	daemon.Spawn()

	deadline := time.Now().Add(daemonRestartReadyTimeout)
	for time.Now().Before(deadline) {
		if daemon.Alive(ipc.DefaultEventSocketPath()) {
			fmt.Println("daemon restarted")
			return
		}
		time.Sleep(daemonRestartPollInterval)
	}
	fmt.Fprintln(os.Stderr, "cenci daemon restart: daemon did not become ready in time")
	os.Exit(1)
}

// runDaemonStatus implements `cenci daemon status`: a narrow
// running/not-running + PID report, distinct from the broader `cenci
// status` overview (sessions + dispatch loop). Exits 1 when the daemon is
// not running so scripts can branch on it; exits 0 when running.
func runDaemonStatus(args []string) {
	fs := flag.NewFlagSet("daemon status", flag.ExitOnError)
	_ = fs.Parse(args)
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci daemon status: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	info := daemon.Status(ipc.DefaultEventSocketPath(), ipc.DefaultPIDPath())
	if !info.Running {
		fmt.Println("daemon not running")
		os.Exit(1)
	}
	if info.PID > 0 {
		fmt.Printf("daemon running (pid %d)\n", info.PID)
	} else {
		fmt.Println("daemon running (pid unknown)")
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
			fmt.Fprintf(os.Stderr, "cenci dispatch: unknown subcommand %q\n", args[0])
			os.Exit(2)
		}
	}

	fs := flag.NewFlagSet("dispatch", flag.ExitOnError)
	once := fs.Bool("once", false, "run a single dispatch pass then exit (default)")
	interval := fs.Duration("interval", 0, "run continuously on this interval (e.g. 5m); mutually exclusive with --once")
	dryRun := fs.Bool("dry-run", false, "print the decision table without dispatching")
	reconcile := fs.Bool("reconcile", false, "run a single failure-reconciliation pass instead of a dispatch pass (cron path)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	model := fs.String("model", "", "model override for every session dispatched this pass (overrides config.json dispatch.model / agents.*.model)")
	_ = fs.Parse(args)

	// Any positional left after flag parsing is unexpected: the flag parser
	// stops at the first non-flag token, so a trailing typo or stray argument
	// would otherwise be silently swallowed along with any flags after it.
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci dispatch: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	cfg, err := dispatch.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch: %v\n", err)
		os.Exit(1)
	}
	if *model != "" {
		cfg.Model = *model
	}

	// --reconcile runs the recovery pass once (cron path). It is independent of
	// the dispatch/loop flags.
	if *reconcile {
		if _, err := dispatch.RunReconcileOnce(cfg, &dispatch.GHMutator{}, *dryRun, os.Stdout, dispatch.NewStateStore("")); err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctrl := &tmux.ExecClient{}
	// --interval self-loops; otherwise a single pass. --once wins if both given.
	if *interval > 0 && !*once {
		if err := dispatch.RunLoop(*configPath, ctrl, &dispatch.GHMutator{}, *interval, os.Stdout, *model); err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch: %v\n", err)
			os.Exit(1)
		}
		return
	}
	prior := 0
	if _, err := dispatch.RunOnce(cfg, ctrl, &dispatch.GHMutator{}, *dryRun, os.Stdout, &prior); err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch: %v\n", err)
		os.Exit(1)
	}
}

func runDispatchEnroll(args []string) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	dir := fs.String("dir", ".", "repo directory to enroll (default: current directory)")
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	_ = fs.Parse(args)

	identity, err := dispatch.DetectRepoIdentity(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch enroll: %v\n", err)
		os.Exit(1)
	}

	changed, err := dispatch.EnrollRepo(*configPath, identity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch enroll: %v\n", err)
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
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	repo := fs.String("repo", "", "repo (owner/name) to unenroll, bypassing git detection")
	_ = fs.Parse(args)

	dirSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "dir" {
			dirSet = true
		}
	})

	if *repo != "" && dirSet {
		fmt.Fprintln(os.Stderr, "cenci dispatch unenroll: --repo and --dir are mutually exclusive")
		os.Exit(2)
	}

	var target string
	if *repo != "" {
		target = *repo
	} else {
		identity, err := dispatch.DetectRepoIdentity(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch unenroll: %v\n", err)
			os.Exit(1)
		}
		target = identity.Repo
	}

	changed, err := dispatch.UnenrollRepo(*configPath, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch unenroll: %v\n", err)
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
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	jsonOut := fs.Bool("json", false, "print result as JSON")
	_ = fs.Parse(args)

	identity, err := dispatch.DetectRepoIdentity(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch status: %v\n", err)
		os.Exit(1)
	}

	enrollment, err := dispatch.QueryEnrollment(*configPath, identity.Repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch status: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "cenci dispatch status: %v\n", err)
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

// runDispatchLoop implements `cenci dispatch loop on|off|status`. All
// three verbs resolve and print the current DispatchState via the same
// socket-first/config-fallback path (dispatch.ResolveDispatchState); on/off
// additionally persist the toggle to config.json first. Default output is
// human-readable; --json prints the raw DispatchState.
func runDispatchLoop(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci dispatch loop: expected a subcommand: on, off, or status")
		os.Exit(2)
	}
	verb := args[0]

	fs := flag.NewFlagSet("loop "+verb, flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	jsonOut := fs.Bool("json", false, "print result as JSON")
	_ = fs.Parse(args[1:])

	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci dispatch loop: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	switch verb {
	case "on":
		if err := dispatch.SetLoopEnabled(*configPath, true); err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch loop: %v\n", err)
			os.Exit(1)
		}
	case "off":
		if err := dispatch.SetLoopEnabled(*configPath, false); err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch loop: %v\n", err)
			os.Exit(1)
		}
	case "status":
		// no mutation; just resolve and print below.
	default:
		fmt.Fprintf(os.Stderr, "cenci dispatch loop: unknown subcommand %q\n", verb)
		os.Exit(2)
	}

	state := dispatch.ResolveDispatchState(*configPath, watch.DefaultSocketPath(), os.Stderr)

	if *jsonOut {
		data, err := json.Marshal(state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch loop: %v\n", err)
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
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci status: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

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
	case err != nil || snap == nil:
		fmt.Fprintf(&b, "sessions: unavailable (daemon not reachable)\n")
	case len(snap.Windows) == 0:
		fmt.Fprintf(&b, "sessions: none\n")
	default:
		fmt.Fprintf(&b, "sessions (%d):\n", len(snap.Windows))
		for _, w := range snap.Windows {
			name := w.WindowName
			if !w.ManuallyNamed && w.TaskName != "" {
				name = w.TaskName
			}
			if name == "" {
				name = w.Agent
			}
			if w.Session == "" {
				fmt.Fprintf(&b, "  %s (%s)\n", name, w.Status)
			} else {
				fmt.Fprintf(&b, "  %s:%s - %s (%s)\n", w.Session, w.WindowIndex, name, w.Status)
			}
		}
	}

	dstate := dispatch.ResolveDispatchState("", socketPath, io.Discard)
	b.WriteString(renderDispatchState(dstate))

	return b.String()
}

// runClose implements `cenci close <ticket-number|window-name> [--force]
// [--dry-run] [--socket PATH]`. It resolves the target against the daemon's
// live window registry (never re-derives a tmux target itself) and closes
// every matched window that isn't running/waiting for input, unless --force
// is given. See internal/closecmd for the matching/kill/guard logic; this
// function only parses flags and renders the result.
func runClose(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci close: usage: cenci close <ticket-number|window-name> [--force] [--dry-run] [--socket PATH]")
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
		fmt.Fprintf(os.Stderr, "cenci close: unexpected argument %q\n", extra[0])
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
		fmt.Fprintf(os.Stderr, "cenci close: %v\n", err)
		os.Exit(1)
	}

	printCloseDecisions(os.Stdout, target, decisions)

	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci close: %v\n", err)
		os.Exit(1)
	}
}

// printCloseDecisions renders the outcome of `cenci close`. No matches
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

// -- doctor / update ------------------------------------------------------

// wrapperBinaryName is the curl-and-exec front door installed on user
// machines (see the repo-root `cenci` script), which routes
// "doctor"/"update" into install.sh's MODE handling. `cenci
// doctor`/`update` shell out to it rather than reimplementing installer logic
// in Go, so there is exactly one implementation of each mode. It is installed
// on PATH as "cenci-installer" (not "cenci") to avoid colliding with the
// "cenci" launcher symlink that points at this very daemon binary.
const wrapperBinaryName = "cenci-installer"

// runDoctor implements `cenci doctor`: shells out to the installed
// `cenci doctor` wrapper.
func runDoctor(args []string) {
	runWrapperMode("doctor", args)
}

// runUpdate implements `cenci update`: shells out to the installed
// `cenci update` wrapper.
func runUpdate(args []string) {
	runWrapperMode("update", args)
}

// runWrapperMode is the shared implementation behind runDoctor/runUpdate: it
// takes no flags or positionals of its own (mirroring the trailing-positional
// guard used by the other verbs above), resolves wrapperBinaryName from PATH,
// and runs it with mode as its sole argument, stdio inherited so prompts and
// output pass straight through, propagating the child's exit code. A missing
// wrapper is a clear, non-zero-exit error rather than a silent no-op.
func runWrapperMode(mode string, args []string) {
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	_ = fs.Parse(args)
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci %s: unexpected argument %q\n", mode, extra[0])
		os.Exit(2)
	}

	path, err := exec.LookPath(wrapperBinaryName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci %s: %s not found on PATH — re-run the cenci installer to create it\n", mode, wrapperBinaryName)
		os.Exit(1)
	}

	cmd := exec.Command(path, mode)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "cenci %s: %v\n", mode, err)
		os.Exit(1)
	}
}

// -- sandbox ----------------------------------------------------------

// runSandboxGroup implements `cenci sandbox <verb> [flags]`. The batch
// verbs (build, build-base, update-plugins, reseed-creds, reap-orphans) and
// prune translate 1:1 (plus prune's optional --volumes) into a single
// dev-sandbox/cenci-sand invocation; ls and stop are implemented natively in
// Go against docker/podman since cenci-sand has no equivalent flag for them.
func runSandboxGroup(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci sandbox: expected a subcommand: build, build-base, prune, update-plugins, reseed-creds, reap-orphans, ls, stop")
		os.Exit(2)
	}
	verb := args[0]
	rest := args[1:]

	switch verb {
	case "build", "build-base", "update-plugins", "reseed-creds", "reap-orphans":
		runSandboxBatch(verb, rest)
	case "prune":
		runSandboxPrune(rest)
	case "ls":
		runSandboxLs(rest)
	case "stop":
		runSandboxStop(rest)
	default:
		fmt.Fprintf(os.Stderr, "cenci sandbox: unknown subcommand %q\n", verb)
		os.Exit(2)
	}
}

// runSandboxBatch implements the batch verbs that take no flags of their own
// and translate to a single cenci-sand long flag (internal/sandbox's
// BatchFlag table). Unknown flags or any trailing positional are a usage
// error (exit 2) before cenci-sand is ever invoked.
func runSandboxBatch(verb string, args []string) {
	fs := flag.NewFlagSet("sandbox "+verb, flag.ExitOnError)
	_ = fs.Parse(args)
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci sandbox %s: unexpected argument %q\n", verb, extra[0])
		os.Exit(2)
	}

	agentSandFlag, ok := sandbox.BatchFlag(verb)
	if !ok {
		fmt.Fprintf(os.Stderr, "cenci sandbox: unknown subcommand %q\n", verb)
		os.Exit(2)
	}

	code, err := sandbox.RunAgentSand([]string{agentSandFlag}, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox %s: %v\n", verb, err)
		os.Exit(1)
	}
	os.Exit(code)
}

// runSandboxPrune implements `cenci sandbox prune [--volumes]`, the one
// batch verb with a flag of its own.
func runSandboxPrune(args []string) {
	fs := flag.NewFlagSet("sandbox prune", flag.ExitOnError)
	volumes := fs.Bool("volumes", false, "also prompt to remove stale sandbox home volumes")
	_ = fs.Parse(args)
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci sandbox prune: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	argv := []string{"--prune"}
	if *volumes {
		argv = append(argv, "--volumes")
	}

	code, err := sandbox.RunAgentSand(argv, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox prune: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// runSandboxLs implements `cenci sandbox ls`: lists every
// claude-sand-*/codex-sand-* container (running or stopped) as a table.
func runSandboxLs(args []string) {
	fs := flag.NewFlagSet("sandbox ls", flag.ExitOnError)
	_ = fs.Parse(args)
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci sandbox ls: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	runtime, err := sandbox.ContainerRuntime()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox ls: %v\n", err)
		os.Exit(1)
	}
	containers, err := sandbox.ListContainers(runtime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox ls: %v\n", err)
		os.Exit(1)
	}
	if len(containers) == 0 {
		fmt.Println("no sandbox containers found")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tIMAGE")
	for _, c := range containers {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", c.Name, c.Status, c.Image)
	}
	_ = w.Flush()
}

// runSandboxStop implements `cenci sandbox stop [name-or-slug-filter]`:
// stops every running claude-sand-*/codex-sand-* container, optionally
// narrowed to names containing the given filter substring.
func runSandboxStop(args []string) {
	fs := flag.NewFlagSet("sandbox stop", flag.ExitOnError)
	_ = fs.Parse(args)
	extra := fs.Args()
	if len(extra) > 1 {
		fmt.Fprintf(os.Stderr, "cenci sandbox stop: unexpected argument %q\n", extra[1])
		os.Exit(2)
	}
	var filter string
	if len(extra) == 1 {
		filter = extra[0]
	}

	runtime, err := sandbox.ContainerRuntime()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox stop: %v\n", err)
		os.Exit(1)
	}
	names, err := sandbox.RunningSandboxContainers(runtime, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox stop: %v\n", err)
		os.Exit(1)
	}
	if len(names) == 0 {
		fmt.Println("no matching sandbox containers running")
		return
	}

	if err := sandbox.StopContainers(runtime, names, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox stop: %v\n", err)
		os.Exit(1)
	}
	for _, name := range names {
		fmt.Printf("stopped %s\n", name)
	}
}

// -- open ---------------------------------------------------------------

// runOpen implements `cenci open [shortcut] [flags] [-- passthrough]`
// (and the "cn" argv[0] alias, which prepends no extra token — args here is
// already everything after the binary name). It execs cenci-sand, replacing
// this process, so the interactive session owns the TTY.
//
// Grammar: an optional one-token shortcut (ch/cs/co/cf, xl/xt/xs — mirroring
// dev-sandbox/cenci-sand's own shortcut tables exactly) may appear first;
// after that, only the recognized flags below and an optional "--"
// passthrough sentinel are accepted. Any other leading positional is a usage
// error, matching the strict-parsing convention used by the other verbs in
// this file.
func runOpen(args []string) {
	var shortcutToken, shortcutAgent, shortcutModel string
	hasShortcut := false
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		agent, model, ok := sandbox.ResolveShortcut(args[0])
		if !ok {
			fmt.Fprintf(os.Stderr, "cenci open: unrecognized shortcut %q (expected one of ch, cs, co, cf, xl, xt, xs)\n", args[0])
			os.Exit(2)
		}
		shortcutToken, shortcutAgent, shortcutModel = args[0], agent, model
		hasShortcut = true
		args = args[1:]
	}

	// Split off a "--" passthrough sentinel ourselves (rather than relying on
	// the flag package's own "--" handling) so anything after it is forwarded
	// verbatim, while anything else left over after flag parsing is still
	// treated as an unexpected argument below.
	var passthrough []string
	for i, a := range args {
		if a == "--" {
			passthrough = args[i+1:]
			args = args[:i]
			break
		}
	}

	fs := flag.NewFlagSet("open", flag.ExitOnError)
	agentFlag := fs.String("agent", "", "agent to launch (claude or codex)")
	modelFlag := fs.String("model", "", "model override")
	nameFlag := fs.String("name", "", "sandbox instance name")
	shellFlag := fs.Bool("shell", false, "attach a shell instead of launching the agent")
	dockerFlag := fs.Bool("docker", false, "mount the host docker/podman socket (opt-in DooD)")
	hostNetworkFlag := fs.Bool("host-network", false, "use host network mode")
	_ = fs.Parse(args)
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci open: unexpected argument %q\n", extra[0])
		os.Exit(2)
	}

	// A shortcut implies a specific agent; a later explicit --agent that
	// disagrees would silently pair the wrong agent with the shortcut's
	// model, so reject the conflicting combination instead (mirrors
	// cenci-sand's own shortcut/--agent consistency check).
	if hasShortcut && *agentFlag != "" && *agentFlag != shortcutAgent {
		fmt.Fprintf(os.Stderr, "cenci open: shortcut %q selects the %s agent, but --agent %s was also given. Drop the shortcut or the --agent flag so they agree.\n", shortcutToken, shortcutAgent, *agentFlag)
		os.Exit(2)
	}

	finalAgent := shortcutAgent
	if *agentFlag != "" {
		finalAgent = *agentFlag
	}

	finalModel := shortcutModel
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "model" {
			finalModel = *modelFlag
		}
	})

	var argv []string
	if finalAgent != "" {
		argv = append(argv, "--agent", finalAgent)
	}
	if finalModel != "" {
		argv = append(argv, "--model", finalModel)
	}
	if *nameFlag != "" {
		argv = append(argv, "--name", *nameFlag)
	}
	if *shellFlag {
		argv = append(argv, "--shell")
	}
	if *dockerFlag {
		argv = append(argv, "--docker")
	}
	if *hostNetworkFlag {
		argv = append(argv, "--host-network")
	}
	if len(passthrough) > 0 {
		argv = append(argv, "--")
		argv = append(argv, passthrough...)
	}

	if err := sandbox.ExecAgentSand(argv); err != nil {
		fmt.Fprintf(os.Stderr, "cenci open: %v\n", err)
		os.Exit(1)
	}
}
