package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// This file implements the `cenci sandbox <verb>` group (build, build-base,
// prune, update-plugins, reseed-creds, reap-orphans, ls, stop). Every verb
// runs natively against the internal/sandbox/launcher engine and
// docker/podman — nothing shells out to the sandbox/cenci-sand bash launcher
// anymore. See open_cmd.go for `cenci open` (plus the "cn" argv[0] alias).

// runSandboxGroup implements `cenci sandbox <verb> [flags]`.
func runSandboxGroup(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci sandbox: expected a subcommand: build, build-base, prune, update-plugins, reseed-creds, reap-orphans, ls, stop")
		os.Exit(2)
	}
	verb := args[0]
	rest := args[1:]

	switch verb {
	case "build":
		runSandboxBuild(rest)
	case "build-base":
		runSandboxBuildBase(rest)
	case "update-plugins":
		runSandboxUpdatePlugins(rest)
	case "reap-orphans":
		runSandboxReapOrphans(rest)
	case "reseed-creds":
		runSandboxReseedCreds(rest)
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

// rejectExtraArgs enforces the strict-parsing convention shared by every
// sandbox verb: any positional left after flag parsing is a usage error
// (exit 2), because the flag parser stops at the first non-flag token and
// would otherwise silently swallow everything after it.
func rejectExtraArgs(verb string, fs *flag.FlagSet) {
	rejectExtra("cenci sandbox "+verb, fs.Args())
}

// newEngine constructs the launcher engine on process stdio, treating
// construction failures (no container runtime, no sandbox assets) as runtime
// errors (exit 1) attributed to the invoking verb.
func newEngine(verb string) *launcher.Engine {
	eng, err := launcher.New(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox %s: %v\n", verb, err)
		os.Exit(1)
	}
	return eng
}

// currentScope computes the launch scope for the current working directory,
// exiting with a runtime error when cwd or home cannot be resolved.
func currentScope(verb, agent, instanceName string) launcher.Scope {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox %s: %v\n", verb, err)
		os.Exit(1)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox %s: %v\n", verb, err)
		os.Exit(1)
	}
	return launcher.ComputeScope(agent, instanceName, cwd, home)
}

// runSandboxBuild implements `cenci sandbox build [--agents claude,codex]`:
// build the image the current directory selects (the repo's own image when
// .cenci/Dockerfile opts in, otherwise the shared monolith), building the base
// first if its content-hash tag is missing. --agents gates which agent CLIs the
// monolith bakes in (default: both); a per-repo image is a committed team
// artifact and always bakes both, so --agents is noted-and-ignored there. The
// agent passed to ComputeScope is irrelevant — image selection depends only on
// the repo, never on the agent-namespaced container/volume names.
func runSandboxBuild(args []string) {
	fs := flag.NewFlagSet("sandbox build", flag.ExitOnError)
	agentsFlag := fs.String("agents", "", "comma-separated agents to bake into the monolith image (claude, codex; default both)")
	_ = fs.Parse(args)
	rejectExtraArgs("build", fs)

	agents, err := launcher.ParseBuildAgents(*agentsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox build: %v\n", err)
		os.Exit(2)
	}

	scope := currentScope("build", "claude", "")
	if scope.UsingRepoImage && *agentsFlag != "" {
		fmt.Fprintln(os.Stderr, "cenci sandbox build: --agents ignored — this repo's .cenci image always bakes both agents")
	}
	if err := newEngine("build").BuildSelected(scope, agents); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox build: %v\n", err)
		os.Exit(1)
	}
}

// runSandboxBuildBase implements `cenci sandbox build-base`: build the
// stack-agnostic base image at its current content-hash tag (plus the
// :latest alias).
func runSandboxBuildBase(args []string) {
	fs := flag.NewFlagSet("sandbox build-base", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtraArgs("build-base", fs)

	if err := newEngine("build-base").BuildBase(); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox build-base: %v\n", err)
		os.Exit(1)
	}
}

// runSandboxUpdatePlugins implements `cenci sandbox update-plugins [--agent
// claude|codex] [--name N]`: refresh the selected agent's plugins inside the
// running container for the current scope, or in a one-shot container
// against its home volume. --agent restores the cenci-sand `--agent codex
// --update-plugins` capability the earlier 1:1 shim dropped.
func runSandboxUpdatePlugins(args []string) {
	fs := flag.NewFlagSet("sandbox update-plugins", flag.ExitOnError)
	agent := fs.String("agent", "claude", "agent whose plugins to update (claude or codex)")
	name := fs.String("name", "", "sandbox instance name")
	_ = fs.Parse(args)
	rejectExtraArgs("update-plugins", fs)

	if err := launcher.ValidateAgent(*agent); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-plugins: %v\n", err)
		os.Exit(2)
	}

	scope := currentScope("update-plugins", *agent, *name)
	eng := newEngine("update-plugins")
	// cenci-sand ensured the selected image existed before its
	// --update-plugins block ran; the one-shot volume branch needs it.
	if err := eng.EnsureImage(scope); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-plugins: %v\n", err)
		os.Exit(1)
	}
	if err := eng.UpdatePlugins(*agent, scope); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-plugins: %v\n", err)
		os.Exit(1)
	}
}

// runSandboxReapOrphans implements `cenci sandbox reap-orphans`: kill
// container-side agent processes whose owning tmux pane no longer exists on
// the host. ReapOrphans prints its own "Error: ..." diagnostics (the
// watch/tests/reap-orphans.test.sh contract asserts those exact strings), so
// a failure here only sets the exit code — nothing extra is printed.
func runSandboxReapOrphans(args []string) {
	fs := flag.NewFlagSet("sandbox reap-orphans", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtraArgs("reap-orphans", fs)

	if err := launcher.ReapOrphans(os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}

// runSandboxReseedCreds implements `cenci sandbox reseed-creds`, kept as an
// alias for `cenci open --reseed-creds` (the flag's honest home — reseeding
// is a launch modifier: CENCI_SANDBOX_RESEED_CREDS is set on the next
// container create, so a launch always follows).
func runSandboxReseedCreds(args []string) {
	fs := flag.NewFlagSet("sandbox reseed-creds", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtraArgs("reseed-creds", fs)

	err := newEngine("reseed-creds").Launch(launcher.Options{ReseedCreds: true})
	if err == nil {
		return // unreachable in practice: a successful Launch never returns
	}
	if launcher.IsUsage(err) {
		fmt.Fprintf(os.Stderr, "cenci sandbox reseed-creds: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "cenci sandbox reseed-creds: %v\n", err)
	os.Exit(1)
}

// runSandboxPrune implements `cenci sandbox prune [--volumes]`: remove
// superseded base tags, dangling images, and stopped sandbox containers;
// with --volumes, also prompt (default-deny) before removing stale home
// volumes.
func runSandboxPrune(args []string) {
	fs := flag.NewFlagSet("sandbox prune", flag.ExitOnError)
	volumes := fs.Bool("volumes", false, "also prompt to remove stale sandbox home volumes")
	_ = fs.Parse(args)
	rejectExtraArgs("prune", fs)

	if err := newEngine("prune").Prune(*volumes); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox prune: %v\n", err)
		os.Exit(1)
	}
}

// runSandboxLs implements `cenci sandbox ls`: lists every
// claude-cenci-*/codex-cenci-* container (running or stopped) as a table.
func runSandboxLs(args []string) {
	fs := flag.NewFlagSet("sandbox ls", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtraArgs("ls", fs)

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
// stops every running claude-cenci-*/codex-cenci-* container, optionally
// narrowed to names containing the given filter substring.
func runSandboxStop(args []string) {
	fs := flag.NewFlagSet("sandbox stop", flag.ExitOnError)
	_ = fs.Parse(args)
	extra := fs.Args()
	if len(extra) > 1 {
		rejectExtra("cenci sandbox stop", extra[1:])
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
