package main

import (
	"context"
	_ "embed" // go:embed for the login-service templates
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/autostart"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/dispatch"
	tmuxfe "github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/hookhealth"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/logging"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// daemonRestartReadyTimeout bounds how long `daemon restart` waits for the
// freshly spawned daemon to become reachable before reporting failure. It
// mirrors internal/daemon's EnsureRunning readyTimeout default.
const daemonRestartReadyTimeout = 3 * time.Second

// daemonRestartPollInterval is the polling cadence within daemonRestartReadyTimeout.
const daemonRestartPollInterval = 50 * time.Millisecond

// runDaemonGroup implements `cenci daemon [start|stop|restart|status|hook-health|
// install-service|uninstall-service|service-status]`.
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
	case "hook-health":
		runDaemonHookHealth(args[1:])
	case "install-service":
		runDaemonInstallService(args[1:])
	case "uninstall-service":
		runDaemonUninstallService(args[1:])
	case "service-status":
		runDaemonServiceStatus(args[1:])
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
	fs.BoolVar(&cfg.LogJSON, "json", os.Getenv("CENCI_LOG_JSON") == "1", "emit structured JSON log lines instead of plain text (default from CENCI_LOG_JSON)")
	fs.StringVar(&cfg.SocketPath, "socket", ipc.DefaultSocketPath(), "IPC broadcast socket path (empty to disable)")
	fs.StringVar(&cfg.EventSocketPath, "event-socket", ipc.DefaultEventSocketPath(), "event socket path for hook notifications")

	sweepSec := int(cfg.SweepInterval / time.Second)
	fs.IntVar(&sweepSec, "sweep", sweepSec, "stale session sweep interval in seconds")
	fs.DurationVar(&cfg.SessionTTL, "session-ttl", cfg.SessionTTL, "idle expiry for sessions outside tmux (e.g. 2h)")
	fs.DurationVar(&cfg.ReapInterval, "reap-interval", cfg.ReapInterval, "period of the sandbox orphan-reap backstop (e.g. 5m); 0 disables it")
	fs.DurationVar(&cfg.WindowCleanupInterval, "window-cleanup-interval", cfg.WindowCleanupInterval, "period of the finished-ticket window-cleanup pass (e.g. 2m); 0 disables it")
	fs.DurationVar(&cfg.WorktreeCleanupInterval, "worktree-cleanup-interval", cfg.WorktreeCleanupInterval, "period of the finished/stale worktree-cleanup pass (e.g. 10m); 0 disables it")

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

	logger := logging.New(os.Stderr, cfg.LogJSON)

	if cfg.Verbose {
		logger.Log(logging.SeverityInfo, "", fmt.Sprintf("cenci starting (event-driven, sweep every %s)", cfg.SweepInterval))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		if cfg.Verbose {
			logger.Log(logging.SeverityInfo, "", fmt.Sprintf("received %s, shutting down", sig))
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
		if err := daemon.WritePIDFile(pidPath); err != nil {
			logger.Log(logging.SeverityWarn, "", fmt.Sprintf("warning: could not write pid file %s: %v", pidPath, err))
		}
	}
	defer func() {
		if started {
			daemon.RemovePIDFile(pidPath)
		}
	}()

	if err := daemon.Run(ctx, cfg, fe, attention, onStarted, run.BuiltinCapability); err != nil {
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
	rejectExtra("cenci daemon stop", fs.Args())

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

// runDaemonRestart implements `cenci daemon restart`. With the login service
// enabled it restarts through the service manager and never spawns a daemon of
// its own, so supervision is kept (the installer's update path lands here); the
// backend first stops a daemon the inactive unit does not own. Otherwise it stops the daemon (if running), spawns a fresh detached one the
// same way EnsureRunning does (daemon.Spawn — `cenci daemon start`, Setsid'd),
// and waits briefly for it to become reachable.
func runDaemonRestart(args []string) {
	fs := flag.NewFlagSet("daemon restart", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtra("cenci daemon restart", fs.Args())

	service := newAutostart()
	if service.Status(context.Background()) == autostart.StateEnabled {
		if err := service.Restart(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "cenci daemon restart: %v\n", err)
			os.Exit(1)
		}
	} else {
		outcome, err := daemon.Stop(ipc.DefaultEventSocketPath(), ipc.DefaultPIDPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "cenci daemon restart: %v\n", err)
			os.Exit(1)
		}
		if outcome.WasRunning {
			fmt.Printf("stopped daemon (pid %d)\n", outcome.PID)
		}
		daemon.Spawn()
	}

	if !waitDaemonReady() {
		fmt.Fprintln(os.Stderr, "cenci daemon restart: daemon did not become ready in time")
		os.Exit(1)
	}
	fmt.Println("daemon restarted")
}

// waitDaemonReady polls the event socket for up to daemonRestartReadyTimeout.
func waitDaemonReady() bool {
	deadline := time.Now().Add(daemonRestartReadyTimeout)
	for time.Now().Before(deadline) {
		if daemon.Alive(ipc.DefaultEventSocketPath()) {
			return true
		}
		time.Sleep(daemonRestartPollInterval)
	}
	return false
}

//go:embed plugin/service/cenci-watch.service
var systemdUnitTemplate string

//go:embed plugin/service/cenci-watch.plist
var launchdPlistTemplate string

// newAutostart wires the login-service backend to this process: the real OS,
// home, environment and uid, the embedded templates, and daemon control over the
// default event socket and PID file.
func newAutostart() autostart.Backend {
	home, _ := os.UserHomeDir() // an unknown home makes autostart.New return its unsupported stub
	return autostart.New(autostart.Options{
		GOOS:      runtime.GOOS,
		Home:      home,
		LookupEnv: os.LookupEnv,
		UID:       os.Getuid(),
		Templates: autostart.Templates{Systemd: systemdUnitTemplate, Launchd: launchdPlistTemplate},
		Daemon: autostart.Daemon{
			Alive: func() bool { return daemon.Alive(ipc.DefaultEventSocketPath()) },
			Stop: func() error {
				_, err := daemon.Stop(ipc.DefaultEventSocketPath(), ipc.DefaultPIDPath())
				return err
			},
		},
	})
}

// parseServiceFlags parses a service subverb's flags. Usage errors print a
// one-line hint (the flag package's own multi-line usage block is discarded) and
// exit 2. --force exists only when allowForce is set.
func parseServiceFlags(verb string, args []string, allowForce bool) (force bool) {
	prefix := "cenci daemon " + verb
	fs := flag.NewFlagSet("daemon "+verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if allowForce {
		fs.BoolVar(&force, "force", false, "replace or remove a service file cenci did not write")
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", prefix, err)
		os.Exit(2)
	}
	rejectExtra(prefix, fs.Args())
	return force
}

// runDaemonInstallService implements `cenci daemon install-service [--force]`:
// write the login service for this OS, hand over from an unsupervised daemon,
// then enable and start it. Exits 1 on any failed precondition or command.
func runDaemonInstallService(args []string) {
	force := parseServiceFlags("install-service", args, true)

	outcome, err := newAutostart().Install(context.Background(), force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci daemon install-service: %v\n", err)
		os.Exit(1)
	}
	if outcome == autostart.OutcomeAlreadyInstalled {
		fmt.Println("service already installed")
		return
	}
	fmt.Println("service installed and started")
}

// runDaemonUninstallService implements `cenci daemon uninstall-service
// [--force]`: disable, stop and remove the login service. The daemon is not
// respawned; the next agent session's bootstrap restores it.
func runDaemonUninstallService(args []string) {
	force := parseServiceFlags("uninstall-service", args, true)

	outcome, err := newAutostart().Uninstall(context.Background(), force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci daemon uninstall-service: %v\n", err)
		os.Exit(1)
	}
	if outcome == autostart.OutcomeNotInstalled {
		fmt.Println("service not installed")
		return
	}
	fmt.Println("service removed; the daemon starts again on your next agent session or with: cenci daemon start")
}

// runDaemonServiceStatus implements `cenci daemon service-status`: a read-only
// report whose first line is the stable token `service: <enabled|disabled|
// not-installed|unsupported>` that `cenci doctor` matches. It takes no flags and
// always exits 0 once it parses; any doubt reads as `unsupported`.
func runDaemonServiceStatus(args []string) {
	parseServiceFlags("service-status", args, false)

	fmt.Println(newAutostart().Status(context.Background()).Line())
}

// runDaemonStatus implements `cenci daemon status`: a narrow
// running/not-running + PID report, distinct from the broader `cenci
// status` overview (sessions + dispatch loop). Exits 1 when the daemon is
// not running so scripts can branch on it; exits 0 when running.
func runDaemonStatus(args []string) {
	fs := flag.NewFlagSet("daemon status", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtra("cenci daemon status", fs.Args())

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

// runDaemonHookHealth implements `cenci daemon hook-health` (#1239): a
// read-only report over the per-user hook-delivery diagnostics store. The
// first line is the stable token `hook health: <healthy|recovered|no-evidence|
// current-failure>` that `cenci doctor` matches. It exits 0 whenever it renders,
// whatever the state; it never creates or repairs anything.
func runDaemonHookHealth(args []string) {
	fs := flag.NewFlagSet("daemon hook-health", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtra("cenci daemon hook-health", fs.Args())

	fmt.Print(hookhealth.Render(hookhealth.Read(hookhealth.Dir(), time.Now())))
}
