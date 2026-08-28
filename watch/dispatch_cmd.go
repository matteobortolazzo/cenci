package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
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
		case "plan-refined":
			runDispatchPlanRefined(args[1:])
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
	session := fs.String("session", "", "tmux session this repo's dispatches should target (optional; omit to preserve any existing value)")
	_ = fs.Parse(args)

	sessionSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "session" {
			sessionSet = true
		}
	})

	trimmedSession := strings.TrimSpace(*session)
	if sessionSet && trimmedSession == "" {
		fmt.Fprintln(os.Stderr, "cenci dispatch enroll: --session must not be empty or whitespace-only")
		os.Exit(2)
	}

	identity, err := dispatch.DetectRepoIdentity(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch enroll: %v\n", err)
		os.Exit(1)
	}

	changed, effective, err := dispatch.EnrollRepo(*configPath, identity, trimmedSession)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch enroll: %v\n", err)
		os.Exit(1)
	}

	verb := "Enrolled"
	if !changed {
		verb = "Already enrolled"
	}

	if effective != "" {
		fmt.Printf("%s %s (%s) → session %s\n", verb, identity.Repo, identity.Dir, effective)
		return
	}

	// #927/#933: repos[].session is required before this repo's dispatches
	// can spawn anywhere -- warn whenever the resulting entry's session is
	// still empty, driven by that resulting state rather than by whether
	// --session was passed on this invocation, so it fires on both the
	// fresh-enrollment and the idempotent "already enrolled" paths above.
	// ResolveConfigPath errors fall back to the raw --config value (or its
	// default) for display purposes only -- the warning itself still
	// prints. Any resolution failure is also surfaced to stderr (non-fatal)
	// so it is traceable rather than silently swallowed. Exit stays 0: this
	// is output-only, never a write.
	resolvedPath, perr := dispatch.ResolveConfigPath(*configPath)
	if perr != nil {
		resolvedPath = *configPath
		fmt.Fprintf(os.Stderr, "cenci dispatch enroll: resolving config path: %v\n", perr)
	}
	fmt.Printf("%s %s (%s); no tmux session set -- dispatch will skip this repo until you run: cenci dispatch enroll --session <name> (config: %s)\n",
		verb, identity.Repo, identity.Dir, resolvedPath)
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
			Session  string              `json:"session"`
			Loop     watch.DispatchState `json:"loop"`
		}{
			Repo:     enrollment.Repo,
			Dir:      enrollment.Dir,
			Enrolled: enrollment.Enrolled,
			Session:  enrollment.Session,
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

	switch {
	case !enrollment.Enrolled:
		fmt.Printf("Not enrolled: %s\n", enrollment.Repo)
	case enrollment.Session != "":
		fmt.Printf("Enrolled %s (%s) → session %s\n", enrollment.Repo, enrollment.Dir, enrollment.Session)
	default:
		fmt.Printf("Enrolled %s (%s); no tmux session set\n", enrollment.Repo, enrollment.Dir)
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

// runDispatchPlanRefined implements `cenci dispatch plan-refined
// on|off|status` (#964): the CLI writer/reader for the fleet-wide
// dispatch.planRefined planning-pickup switch, mirroring runDispatchLoop's
// shape. All three verbs print the same resolved state; on/off persist the
// toggle first. When --dir is inside a git repository, the output also
// reports that repo's remote-confirmed planning.autonomy verdict and the
// combined authorization — the fleet flag alone never authorizes a planning
// pickup (#851/#877), so status makes the full grant chain visible.
//
// #1086: the combined verdict is now three-factor (attended + planRefined +
// repo autonomy), reading the fleet-wide planning.attended switch via the
// same strict QueryPlanningAttended reader `cenci planning attended status`
// uses, so the two commands can never disagree about whether a repo's
// pickups would actually fire. A malformed planning block therefore also
// exits 1 here (Q3) — a status surface must never render a broken config as
// a confident authorized state.
func runDispatchPlanRefined(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci dispatch plan-refined: expected a subcommand: on, off, or status")
		os.Exit(2)
	}
	verb := args[0]

	fs := flag.NewFlagSet("plan-refined "+verb, flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	dir := fs.String("dir", ".", "repo directory for the autonomy verdict (default: current directory)")
	jsonOut := fs.Bool("json", false, "print result as JSON")
	_ = fs.Parse(args[1:])

	rejectExtra("cenci dispatch plan-refined", fs.Args())

	switch verb {
	case "on":
		if err := dispatch.SetPlanRefined(*configPath, true); err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch plan-refined: %v\n", err)
			os.Exit(1)
		}
	case "off":
		if err := dispatch.SetPlanRefined(*configPath, false); err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch plan-refined: %v\n", err)
			os.Exit(1)
		}
	case "status":
		// no mutation; just resolve and print below.
	default:
		fmt.Fprintf(os.Stderr, "cenci dispatch plan-refined: unknown subcommand %q\n", verb)
		os.Exit(2)
	}

	resolvedPath, err := dispatch.ResolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch plan-refined: %v\n", err)
		os.Exit(1)
	}
	enabled, err := dispatch.QueryPlanRefined(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch plan-refined: %v\n", err)
		os.Exit(1)
	}
	attended, err := dispatch.QueryPlanningAttended(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci dispatch plan-refined: %v\n", err)
		os.Exit(1)
	}

	// Repo fields only render when --dir resolves to a git repo with an
	// origin remote — status must work from anywhere, so a plain directory
	// just gets the fleet flags. The autonomy probe reads the committed
	// config at refs/remotes/origin/main as of the last fetch and fails
	// closed (unreadable) when that ref doesn't resolve, same as the
	// dispatch gate itself.
	var repoName string
	var autonomy dispatch.RepoAutonomy
	if identity, derr := dispatch.DetectRepoIdentity(*dir); derr == nil {
		repoName = identity.Repo
		autonomy = dispatch.QueryRepoAutonomy(*dir)
	}

	if *jsonOut {
		out := struct {
			Enabled      bool   `json:"enabled"`
			Config       string `json:"config"`
			Repo         string `json:"repo,omitempty"`
			RepoAutonomy string `json:"repo_autonomy,omitempty"`
			Authorized   *bool  `json:"authorized,omitempty"`
			Attended     bool   `json:"attended"`
		}{Enabled: enabled, Config: resolvedPath, Attended: attended}
		if repoName != "" {
			authorized := planningAttendedAuthorized(attended, enabled, autonomy)
			out.Repo = repoName
			out.RepoAutonomy = string(autonomy)
			out.Authorized = &authorized
		}
		data, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cenci dispatch plan-refined: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}

	enabledStr := "disabled"
	if enabled {
		enabledStr = "enabled"
	}
	fmt.Printf("Planning pickup (dispatch.planRefined): %s (config: %s)\n", enabledStr, resolvedPath)
	if !enabled {
		fmt.Printf("  enable with: cenci dispatch plan-refined on\n")
	}
	if repoName != "" {
		fmt.Printf("  repo: %s\n", repoName)
		fmt.Printf("  repo autonomy (planning.autonomy @ origin/main, as of last fetch): %s\n", autonomy)
		fmt.Printf("  attended (planning.attended): %v\n", attended)
		if planningAttendedAuthorized(attended, enabled, autonomy) {
			fmt.Printf("  authorized: yes — Refined tickets here are picked up for unattended planning\n")
		} else {
			fmt.Printf("  authorized: no — needs attended off, the fleet flag on, and planning.autonomy \"lean\" committed on origin/main\n")
		}
	}
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

	if state.ResolveError != "" {
		fmt.Fprintf(&b, "  resolve_error: %s\n", state.ResolveError)
	}

	return b.String()
}
