package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

// version is stamped at build time via -ldflags "-X main.version=<ver>".
// It defaults to "dev" for local/test builds.
var version = "dev"

func main() {
	// argv[0] alias: a binary invoked (directly or via a symlink/copy) as
	// "cn" behaves as `cenci open <args>` — a shorthand entry point kept
	// alongside the canonical "cenci" binary name.
	if filepath.Base(os.Args[0]) == "cn" {
		runOpen(os.Args[1:])
		return
	}

	// Tombstone argv[0] alias: the cenci-sand bash launcher was folded into
	// this binary, and install.sh repoints a stale ~/.local/bin/cenci-sand
	// symlink here. Old flags and new verbs don't map 1:1, so instead of
	// guessing we fail loudly with the migration map — stale references in
	// user scripts and repo docs get guidance instead of a dangling command.
	if filepath.Base(os.Args[0]) == "cenci-sand" {
		fmt.Fprint(os.Stderr, `cenci-sand has been folded into the cenci binary.
  cenci-sand [shortcut] [flags] [-- args]  ->  cenci open ...   (or: cn ...)
  cenci-sand --build | --build-base | --prune | --update-plugins | --reap-orphans | --reseed-creds  ->  cenci sandbox <verb>
Details: docs/migrating-to-cenci.md in the cenci repo.
`)
		os.Exit(2)
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
	case "babysit":
		runBabysit(os.Args[2:])
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
	case "uninstall":
		runUninstall(os.Args[2:])
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
  babysit                            supervise an open PR until it merges or closes
  dispatch                           fleet auto-dispatch (enroll/unenroll/status/loop)
  close                              close a finished/idle agent window
  sandbox                            manage the sandbox container (build|build-base|prune|update-agent|update-plugins|reseed-creds|reap-orphans|ls|stop)
  open [shortcut]                    launch or attach an interactive sandbox session (aliased by the "cn" binary name)
  doctor                             check prerequisites and installed stack components, change nothing (delegates to the installed cenci-installer wrapper)
  update                             update installed plugins and restart the daemon (delegates to the installed cenci-installer wrapper)
  uninstall                          remove installed plugins, PATH links, daemon, and config (delegates to the installed cenci-installer wrapper)
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
// exits 0, so shell consumers (widget scripts, tests) don't reimplement
// the XDG-vs-fallback logic themselves and risk drift. Exits 1 on error.
func runSocketDir() {
	dir, err := ipc.DefaultSocketDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci socket-dir: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(dir)
}

// -- doctor / update / uninstall ------------------------------------------

// wrapperBinaryName is the marketplace-backed front door installed on user
// machines (see the repo-root `cenci` script), which routes
// "doctor"/"update"/"uninstall" into install.sh's MODE handling. `cenci
// doctor`/`update`/`uninstall` shell out to it rather than reimplementing
// installer logic in Go, so there is exactly one implementation of each mode.
// It is installed at ~/.local/bin/cenci-installer (not "cenci") to avoid
// colliding with the "cenci" launcher symlink that points at this very
// daemon binary.
const wrapperBinaryName = "cenci-installer"

// runDoctor implements `cenci doctor`: shells out to the installed
// `cenci doctor` wrapper.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtra("cenci doctor", fs.Args())
	runWrapperMode("doctor", nil)
}

// runUpdate implements `cenci update`: shells out to the installed
// `cenci update` wrapper.
func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	yes := fs.Bool("yes", false, "accept defaults without prompting")
	shortYes := fs.Bool("y", false, "accept defaults without prompting")
	build := fs.Bool("build", false, "build the sandbox image")
	noBuild := fs.Bool("no-build", false, "skip the sandbox image build")
	lazyboards := fs.Bool("lazyboards", false, "install or reconcile lazyboards")
	noLazyboards := fs.Bool("no-lazyboards", false, "skip lazyboards reconciliation")
	_ = fs.Parse(args)
	rejectExtra("cenci update", fs.Args())
	if *build && *noBuild {
		fmt.Fprintln(os.Stderr, "cenci update: --build and --no-build conflict")
		os.Exit(2)
	}
	if *lazyboards && *noLazyboards {
		fmt.Fprintln(os.Stderr, "cenci update: --lazyboards and --no-lazyboards conflict")
		os.Exit(2)
	}
	forward := make([]string, 0, 3)
	if *yes || *shortYes {
		forward = append(forward, "--yes")
	}
	if *build {
		forward = append(forward, "--build")
	} else if *noBuild {
		forward = append(forward, "--no-build")
	}
	if *lazyboards {
		forward = append(forward, "--lazyboards")
	} else if *noLazyboards {
		forward = append(forward, "--no-lazyboards")
	}
	runWrapperMode("update", forward)
}

// runUninstall implements `cenci uninstall`: shells out to the installed
// `cenci uninstall` wrapper.
func runUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtra("cenci uninstall", fs.Args())
	runWrapperMode("uninstall", nil)
}

// runWrapperMode is the shared implementation behind
// runDoctor/runUpdate/runUninstall: it resolves the installer from the
// managed user path rather than accepting a same-named PATH shadow, then
// runs it with inherited stdio and propagates its exit code.
func runWrapperMode(mode string, args []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci %s: resolve home directory: %v\n", mode, err)
		os.Exit(1)
	}
	path := filepath.Join(home, ".local", "bin", wrapperBinaryName)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		fmt.Fprintf(os.Stderr, "cenci %s: managed installer %s is missing or not executable — re-run the cenci installer\n", mode, path)
		os.Exit(1)
	}

	cmdArgs := append([]string{mode}, args...)
	cmd := exec.Command(path, cmdArgs...)
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
