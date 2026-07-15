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

// This file implements the `cenci sandbox <verb>` group and `cenci open`
// (plus the "cn" argv[0] alias). Every verb runs natively against the
// internal/sandbox/launcher engine and docker/podman — nothing shells out to
// the sandbox/cenci-sand bash launcher anymore.

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
	if extra := fs.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "cenci sandbox %s: unexpected argument %q\n", verb, extra[0])
		os.Exit(2)
	}
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

// runSandboxBuild implements `cenci sandbox build`: build the image the
// current directory selects (the repo's own image when .cenci/Dockerfile
// opts in, otherwise the shared monolith), building the base first if its
// content-hash tag is missing. The agent passed to ComputeScope is
// irrelevant here — image selection depends only on the repo, never on the
// agent-namespaced container/volume names.
func runSandboxBuild(args []string) {
	fs := flag.NewFlagSet("sandbox build", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtraArgs("build", fs)

	scope := currentScope("build", "claude", "")
	if err := newEngine("build").BuildSelected(scope); err != nil {
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
// already everything after the binary name). It launches natively via the
// internal/sandbox/launcher engine, whose final attach execs the container
// runtime in place of this process so the interactive session owns the TTY
// and its exit code propagates.
//
// Grammar: an optional one-token shortcut (ch/cs/co/cf, xl/xt/xs — the
// internal/sandbox shortcut tables) may appear first; after that, only the
// recognized flags below and an optional "--" passthrough sentinel are
// accepted. Any other leading positional is a usage error, matching the
// strict-parsing convention used by the other verbs in this file.
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
	reseedFlag := fs.Bool("reseed-creds", false, "force a credential reseed from the host on the next container create")
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

	// Empty agent/model stay empty here — Launch applies the claude default
	// and the per-agent model default.
	eng, err := launcher.New(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci open: %v\n", err)
		os.Exit(1)
	}
	err = eng.Launch(launcher.Options{
		Agent:       finalAgent,
		Model:       finalModel,
		Name:        *nameFlag,
		Shell:       *shellFlag,
		Docker:      *dockerFlag,
		HostNetwork: *hostNetworkFlag,
		ReseedCreds: *reseedFlag,
		AgentArgs:   passthrough,
	})
	if err == nil {
		return // unreachable in practice: a successful Launch never returns
	}
	if launcher.IsUsage(err) {
		fmt.Fprintf(os.Stderr, "cenci open: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "cenci open: %v\n", err)
	os.Exit(1)
}
