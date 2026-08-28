package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

// This file implements the `cenci sandbox <verb>` group (build, build-base,
// prune, update-agent, update-plugins, reseed-creds, reap-orphans, ls, stop). Every verb
// runs natively against the internal/sandbox/launcher engine and
// docker/podman — nothing shells out to the sandbox/cenci-sand bash launcher
// anymore. See open_cmd.go for `cenci open` (plus the "cn" argv[0] alias).

// runSandboxGroup implements `cenci sandbox <verb> [flags]`.
func runSandboxGroup(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci sandbox: expected a subcommand: build, build-base, prune, update-agent, update-plugins, reseed-creds, reap-orphans, ls, stop")
		os.Exit(2)
	}
	verb := args[0]
	rest := args[1:]

	switch verb {
	case "build":
		runSandboxBuild(rest)
	case "build-base":
		runSandboxBuildBase(rest)
	case "update-agent":
		runSandboxUpdateAgent(rest)
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

// runSandboxBuild implements `cenci sandbox build [--check]`:
// build the image the current directory selects (the repo's own image when
// .cenci/Dockerfile opts in, otherwise the shared monolith), building the base
// first if its content-hash tag is missing. Agent CLIs are runtime-managed in
// shared runtime volumes and are not selected as image build inputs. The
// agent passed to ComputeScope is irrelevant — image selection depends only on
// the repo, never on the agent-namespaced container/volume names.
// --check reports the selected image's freshness (the same imageCurrent gate
// BuildSelected relies on) via exit code only — 0 when current, non-zero when
// a rebuild is needed or the check itself errors — without building anything.
// install.sh's step_sandbox_setup consults this before its BUILD_IMAGE=ask
// rebuild prompt so it can skip asking when nothing needs to rebuild.
func runSandboxBuild(args []string) {
	fs := flag.NewFlagSet("sandbox build", flag.ExitOnError)
	check := fs.Bool("check", false, "report whether the selected image is current, without building (0 = current, non-zero = rebuild needed or error)")
	_ = fs.Parse(args)
	rejectExtraArgs("build", fs)

	scope := currentScope("cenci sandbox build", "claude", "")

	if *check {
		current, err := newEngine("build").CheckSelected(scope)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cenci sandbox build --check: %v\n", err)
			os.Exit(1)
		}
		if current {
			os.Exit(0)
		}
		os.Exit(1)
	}

	if err := newEngine("build").BuildSelected(scope); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox build: %v\n", err)
		os.Exit(1)
	}
}

// runSandboxUpdateAgent implements `cenci sandbox update-agent [--agent
// claude|codex] [--version exact-semver]`, `--unpin`, and `--all` for the
// host-global agent volume(s). The updater always targets the shared
// monolith image (never the current directory's own per-repo image, if any
// — see UpdateAgent), so this verb never needs to resolve or build a
// per-repo scope. On a dual-runtime host it updates the shared agent-CLI
// volume in every runtime that already has it (never a runtime that
// doesn't), and bootstraps it in the preferred (podman-first) runtime only
// when it exists nowhere (Q4).
//
// --unpin clears a version pin the shared volume may carry (#708's
// pin/unpin/skip-if-pinned contract) before updating: for each owner
// runtime, `agent-cli.sh unpin` runs first, and that owner's update only
// runs when the unpin itself succeeds — an uncleared pin would make the
// following plain update re-refuse. --unpin follows the same owner
// resolution as the bare form (bootstrapping in the preferred runtime only
// when the volume exists nowhere) and cannot be combined with --version
// (unpinning updates to latest; --version would instead re-pin).
//
// --all sweeps every agent in sandbox.SupportedAgents across every runtime
// that already owns that agent's volume, passing --skip-if-pinned so a
// deliberately pinned volume is left alone rather than refused — it never
// bootstraps a volume for an (agent, runtime) pair that has none, and cannot
// be combined with an explicitly-set --agent, --version, or --unpin (those
// select a single agent/behavior that --all's host-wide sweep would
// silently ignore).
//
// A bare `update-agent` run's owner-loop errors are exit-code classified: if
// every collected error is the isolated updater's pin-refusal (exit 2), the
// command exits 2 (naming --unpin/--version); if any error is an ordinary
// failure, it exits 1 — a real failure must never silently collapse into
// "pinned".
func runSandboxUpdateAgent(args []string) {
	fs := flag.NewFlagSet("sandbox update-agent", flag.ExitOnError)
	agent := fs.String("agent", "claude", "agent CLI to update (claude, codex, or opencode)")
	version := fs.String("version", "", "exact semantic version (default: official latest)")
	unpin := fs.Bool("unpin", false, "clear the shared volume's version pin (if any), then update to latest")
	all := fs.Bool("all", false, "refresh every existing agent-CLI volume across every installed runtime, leaving pinned volumes untouched")
	_ = fs.Parse(args)
	rejectExtraArgs("update-agent", fs)

	var agentSet, versionSet, unpinSet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "agent":
			agentSet = true
		case "version":
			versionSet = true
		case "unpin":
			unpinSet = true
		}
	})

	if *all {
		if agentSet || versionSet || unpinSet {
			fmt.Fprintln(os.Stderr, "cenci sandbox update-agent: --all refreshes every existing agent-CLI volume across every runtime and cannot be combined with --agent, --version, or --unpin")
			os.Exit(2)
		}
		runSandboxUpdateAgentAll()
		return
	}
	if *unpin && versionSet {
		fmt.Fprintln(os.Stderr, "cenci sandbox update-agent: --unpin clears the pin and updates to latest, and cannot be combined with --version")
		os.Exit(2)
	}

	if err := launcher.ValidateAgent(*agent); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-agent: %v\n", err)
		os.Exit(2)
	}

	if *version != "" && !launcher.IsExactSemver(*version) {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-agent: version %q is not an exact semantic version\n", *version)
		os.Exit(2)
	}
	if *version != "" {
		fmt.Println("Note: --version pins/downgrades the shared agent CLI volume, which is used by every sandbox on this host.")
	}

	eng := newEngine("update-agent")

	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-agent: %v\n", err)
		os.Exit(1)
	}

	// A per-runtime `volume ls` failure is aggregated into errs rather than
	// aborting immediately: when the other runtime's query still resolved a
	// genuine owner, that owner must still be updated (mirroring ls/stop's
	// partial-result handling) — only an empty owners set (nothing resolved
	// anywhere) falls through to the bootstrap fallback below.
	// No running/stopped tie-break applies here: volumes carry no container
	// status, so there is nothing for OwnersRunningFirst to rank (#761 Q3).
	var errs []error
	volumeName := launcher.AgentCLIVolumeName(*agent)
	owners, err := sandbox.RuntimesWithVolume(runtimes, volumeName)
	if err != nil {
		errs = append(errs, err)
	}
	if len(owners) == 0 {
		// Absent everywhere (or every query failed): bootstrap in the
		// preferred (podman-first) runtime rather than every installed one.
		// eng.Runtime is already that preferred runtime (newEngine resolves
		// it via sandbox.ContainerRuntime()), so no separate re-resolution is
		// needed here.
		owners = []string{eng.Runtime}
	}

	for _, rt := range owners {
		target := eng.WithRuntime(rt)
		if *unpin {
			if err := target.UnpinAgent(*agent); err != nil {
				// An uncleared pin would make the following plain update
				// re-refuse — never run it for this owner.
				errs = append(errs, err)
				continue
			}
		}
		if err := target.UpdateAgent(*agent, *version); err != nil {
			errs = append(errs, err)
		}
	}
	exitOnUpdateAgentErrors("update-agent", errs)
}

// exitOnUpdateAgentErrors classifies a bare/--unpin `update-agent` owner
// loop's collected errors: no errors is success (returns, exit code
// untouched); every error being the isolated updater's pin-refusal (exit 2)
// propagates that same exit 2, naming both escape hatches (--unpin,
// --version); any other error exits 1 — a genuine failure must never
// silently collapse into "pinned" (the ticket's Q1).
func exitOnUpdateAgentErrors(verb string, errs []error) {
	if len(errs) == 0 {
		return
	}
	allRefusals := true
	for _, e := range errs {
		if !launcher.IsAgentPinRefusal(e) {
			allRefusals = false
			break
		}
	}
	joined := errors.Join(errs...)
	if allRefusals {
		fmt.Fprintf(os.Stderr, "cenci sandbox %s: %v (the shared agent CLI volume is pinned to an exact version — use --unpin to clear the pin, or --version to update to a different exact version)\n", verb, joined)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "cenci sandbox %s: %v\n", verb, joined)
	os.Exit(1)
}

// runSandboxUpdateAgentAll implements `cenci sandbox update-agent --all`:
// refresh every agent-CLI volume that already exists, passing
// --skip-if-pinned so a deliberately pinned volume is left alone rather than
// refused. Never bootstraps a volume for an agent that has none anywhere.
// When the same agent's volume exists under more than one runtime, every
// resolved owner is refreshed — matching the bare/--unpin form's explicit Q4
// same-name-collision handling (which also updates every owner). Zero owners
// anywhere is a no-op success.
func runSandboxUpdateAgentAll() {
	eng := newEngine("update-agent")

	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-agent: %v\n", err)
		os.Exit(1)
	}

	// A per-runtime `volume ls` failure is aggregated into errs rather than
	// aborting immediately, mirroring the bare form's partial-result
	// handling — the other runtime's genuinely resolved owner must still be
	// refreshed.
	var errs []error
	for _, agent := range sandbox.SupportedAgents {
		volumeName := launcher.AgentCLIVolumeName(agent)
		owners, err := sandbox.RuntimesWithVolume(runtimes, volumeName)
		if err != nil {
			errs = append(errs, err)
		}
		if len(owners) == 0 {
			continue
		}
		for _, owner := range owners {
			if err := eng.WithRuntime(owner).UpdateAgentSkipIfPinned(agent); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-agent: %v\n", err)
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
// claude|codex] [--name N]` and `cenci sandbox update-plugins --all`.
// --agent/--name refresh the selected agent's plugins inside the running
// container for the current scope, or in a one-shot container against its
// home volume (--agent restores the cenci-sand `--agent codex
// --update-plugins` capability the earlier 1:1 shim dropped). --all instead
// refreshes plugins in every running sandbox container on the host
// (scope-independent) and is a usage error when combined with an explicitly
// given --name or --agent, since those select a single scope that --all's
// host-wide sweep would silently ignore.
func runSandboxUpdatePlugins(args []string) {
	fs := flag.NewFlagSet("sandbox update-plugins", flag.ExitOnError)
	agent := fs.String("agent", "claude", "agent whose plugins to update (claude, codex, or opencode)")
	name := fs.String("name", "", "sandbox instance name")
	all := fs.Bool("all", false, "refresh plugins in every running sandbox container on the host")
	_ = fs.Parse(args)
	rejectExtraArgs("update-plugins", fs)

	var agentSet, nameSet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "agent":
			agentSet = true
		case "name":
			nameSet = true
		}
	})

	if *all {
		if agentSet || nameSet {
			fmt.Fprintln(os.Stderr, "cenci sandbox update-plugins: --all refreshes every running container on the host and cannot be combined with --agent or --name")
			os.Exit(2)
		}
		eng := newEngine("update-plugins")
		if err := eng.RefreshRunningPlugins(); err != nil {
			fmt.Fprintf(os.Stderr, "cenci sandbox update-plugins: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := launcher.ValidateAgent(*agent); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-plugins: %v\n", err)
		os.Exit(2)
	}

	scope := currentScope("cenci sandbox update-plugins", *agent, *name)
	eng := newEngine("update-plugins")

	// Resolve every runtime that actually owns this scope's target: among
	// container owners, a runtime whose copy is currently running is acted
	// on before a runtime whose copy is merely stopped (OwnersRunningFirst);
	// when there is no container owner at all, fall back to the runtime(s)
	// holding the scope's home volume; else fall back to the single
	// preferred runtime (matching prior behavior when no evidence of prior
	// use exists under either runtime). A same-name collision (both runtimes
	// have it) runs the update against both in sequence (Q3) — never
	// silently one; running-first only changes which owner's update runs
	// first, never which runtimes are touched.
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox update-plugins: %v\n", err)
		os.Exit(1)
	}
	// A per-runtime enumeration failure (container or volume listing) is
	// aggregated into errs rather than aborting immediately: when the other
	// runtime's query still resolved a genuine owner, that owner must still
	// be updated (mirroring ls/stop/prune's partial-result handling) — a
	// tier only falls through to the next when owners is genuinely empty,
	// never merely because that tier's query errored on one runtime.
	var errs []error
	containerOwners, err := sandbox.RuntimesWithContainer(runtimes, scope.ContainerName)
	if err != nil {
		errs = append(errs, err)
	}
	var owners []string
	if len(containerOwners) > 0 {
		owners = sandbox.OwnersRunningFirst(containerOwners)
	} else {
		var volErr error
		owners, volErr = sandbox.RuntimesWithVolume(runtimes, scope.VolumeName)
		if volErr != nil {
			errs = append(errs, volErr)
		}
	}
	if len(owners) == 0 {
		owners = []string{eng.Runtime}
	}

	for _, rt := range owners {
		target := eng.WithRuntime(rt)
		// cenci-sand ensured the selected image existed before its
		// --update-plugins block ran; the one-shot volume branch needs it. A
		// failure on one owner (e.g. the same-name collision case) must not
		// prevent the operation from still being attempted against the
		// other owning runtime (Q3) — collect and continue, matching
		// update-agent/prune/ls/stop's pattern in this file.
		if err := target.EnsureImage(scope); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := target.UpdatePlugins(*agent, scope); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
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
// container create, so a launch always follows). Because this verb reaches
// Launch (unlike every other newEngine() call site), it must build its engine
// via NewForLaunch — exactly like open_cmd.go — so Launch's e.Runtime == ""
// guard can re-resolve the runtime docker-preferred when dind mode is on
// (#585); the eager, podman-first newEngine() would lock the runtime before
// Launch ever computes dind, breaking dind-enabled repos on hosts with both
// docker and podman installed.
func runSandboxReseedCreds(args []string) {
	fs := flag.NewFlagSet("sandbox reseed-creds", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtraArgs("reseed-creds", fs)

	eng, err := launcher.NewForLaunch(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox reseed-creds: %v\n", err)
		os.Exit(1)
	}
	err = eng.Launch(launcher.Options{ReseedCreds: true})
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

// runSandboxPrune implements `cenci sandbox prune [--images] [--volumes]`:
// remove superseded base tags, dangling images, and stopped sandbox
// containers; with --images, also prompt (default-deny) before removing
// per-repo sandbox images (cenci-sandbox-<slug>:latest); with --volumes,
// also prompt (default-deny) before removing stale home volumes. --images
// and --volumes are independent and may be combined. On a dual-runtime host
// this runs once per installed runtime (reassigning the engine's runtime via
// WithRuntime each pass) so a Docker-backed DinD container or image is
// pruned even when Podman is also installed; a per-runtime failure still
// lets the healthy runtime's pass complete, surfacing the failure on stderr
// with a non-zero exit (AC #4). Every pass shares one *bufio.Reader
// constructed once, up front, over eng.Stdin (via PruneWithReader) rather
// than letting each runtime's Prune call build its own fresh reader: a fresh
// bufio.NewReader(os.Stdin) per pass would independently buffer-ahead from
// the same underlying stdin stream, so a piped confirmation answer meant for
// the second runtime's pass could be silently swallowed by the first pass's
// reader (see prune.go's PruneWithReader doc comment).
func runSandboxPrune(args []string) {
	fs := flag.NewFlagSet("sandbox prune", flag.ExitOnError)
	images := fs.Bool("images", false, "prompt to remove per-repo sandbox images (cenci-sandbox-<slug>:latest)")
	volumes := fs.Bool("volumes", false, "also prompt to remove stale sandbox home volumes")
	_ = fs.Parse(args)
	rejectExtraArgs("prune", fs)

	eng := newEngine("prune")
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox prune: %v\n", err)
		os.Exit(1)
	}

	reader := bufio.NewReader(eng.Stdin)
	var errs []error
	for _, rt := range runtimes {
		if err := eng.WithRuntime(rt).PruneWithReader(*images, *volumes, reader); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox prune: %v\n", err)
		os.Exit(1)
	}
}

// runSandboxLs implements `cenci sandbox ls`: lists every
// claude-cenci-*/codex-cenci-* container (running or stopped) across every
// installed runtime as a table, tagging each row with its owning runtime in
// a RUNTIME column so a same-name container under both docker and podman
// shows up as two distinct rows (AC #1, #3) instead of collapsing to one
// preferred runtime. A per-runtime listing failure still prints the healthy
// runtime's rows, plus the failure on stderr and a non-zero exit (AC #4).
func runSandboxLs(args []string) {
	fs := flag.NewFlagSet("sandbox ls", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtraArgs("ls", fs)

	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox ls: %v\n", err)
		os.Exit(1)
	}
	rows, listErr := sandbox.ListAllContainers(runtimes)
	if listErr != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox ls: %v\n", listErr)
	}

	if len(rows) == 0 {
		// A total query failure (every runtime's listing errored) is not the
		// same as a genuinely empty result — only print the "nothing found"
		// message when the emptiness is trustworthy (listErr == nil); the
		// stderr error above already explains the failure case.
		if listErr == nil {
			fmt.Println("no sandbox containers found")
		}
		if listErr != nil {
			os.Exit(1)
		}
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tIMAGE\tRUNTIME")
	for _, r := range rows {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Container.Name, r.Container.Status, r.Container.Image, r.Runtime)
	}
	_ = w.Flush()

	if listErr != nil {
		os.Exit(1)
	}
}

// runSandboxStop implements `cenci sandbox stop [name-or-slug-filter]`:
// stops every running claude-cenci-*/codex-cenci-* container across every
// installed runtime, optionally narrowed to names containing the given
// filter substring, tagging each stopped-container line with its runtime
// (`stopped <name> (<runtime>)`) so a same-name container under both docker
// and podman is stoppable and distinguishable (AC #1, #3). A per-runtime
// listing/stop failure still stops the healthy runtime's containers, plus
// the failure on stderr and a non-zero exit (AC #4).
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

	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox stop: %v\n", err)
		os.Exit(1)
	}
	targets, listErr := sandbox.RunningSandboxContainersAll(runtimes, filter)
	if listErr != nil {
		fmt.Fprintf(os.Stderr, "cenci sandbox stop: %v\n", listErr)
	}

	if len(targets) == 0 {
		// A total query failure (every runtime's listing errored) is not the
		// same as a genuinely empty result — only print the "nothing found"
		// message when the emptiness is trustworthy (listErr == nil); the
		// stderr error above already explains the failure case.
		if listErr == nil {
			fmt.Println("no matching sandbox containers running")
		}
		if listErr != nil {
			os.Exit(1)
		}
		return
	}

	exitCode := 0
	if listErr != nil {
		exitCode = 1
	}
	for _, t := range targets {
		if err := sandbox.StopContainers(t.Runtime, []string{t.Name}, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "cenci sandbox stop: %v\n", err)
			exitCode = 1
			continue
		}
		fmt.Printf("stopped %s (%s)\n", t.Name, t.Runtime)
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
