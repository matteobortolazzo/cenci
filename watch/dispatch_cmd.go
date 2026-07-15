package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

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
	rejectExtra("cenci dispatch", fs.Args())

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

	rejectExtra("cenci dispatch loop", fs.Args())

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
