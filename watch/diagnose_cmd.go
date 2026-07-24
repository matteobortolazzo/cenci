package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// runDiagnose implements `cenci diagnose <session> [--agent
// claude|codex|opencode] [--verify]`: a read-only report on a sandbox
// session (container status/exit, the timestamped startup marker, recent
// logs, daemon/socket reachability, plugin + image versions, and mounted
// volumes), each failure annotated with a registered errcode.Code and a
// fatal/degraded/warning severity — see
// internal/sandbox/launcher/diagnose.go. Unlike `cenci open`, diagnose never
// launches, attaches, or wires the daemon; it only reads. --verify re-runs
// the same read-only probes and prints a pass/fail line per verifiable
// check instead of the full report, so an operator can confirm a suggested
// recovery command actually worked.
//
// diagnose is a report, not a pass/fail gate: a successful render exits 0
// even when it finds fatal/degraded issues. Only usage errors (missing
// session, unknown flag, invalid --agent) and cwd/home/runtime resolution
// failures produce a non-zero exit.
func runDiagnose(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci diagnose: usage: cenci diagnose <session> [--agent claude|codex|opencode] [--verify]")
		os.Exit(2)
	}
	session := args[0]

	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agent := fs.String("agent", "claude", "agent whose sandbox session to diagnose (claude, codex, or opencode)")
	verify := fs.Bool("verify", false, "re-run the read-only diagnostic probes and report pass/fail per verifiable check instead of the full report")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(2)
	}
	rejectExtra("cenci diagnose", fs.Args())

	if err := launcher.ValidateAgent(*agent); err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: cannot determine working directory: %v\n", err)
		os.Exit(1)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	scope := launcher.ComputeScope(*agent, session, cwd, home)

	eng, err := launcher.New(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(1)
	}

	// Resolve every runtime that actually owns this scope's target: a
	// running container wins first; else the runtime(s) holding the scope's
	// home volume; else fall back to the single preferred runtime — the same
	// three-tier resolution `cenci sandbox update-plugins` uses (kept
	// consistent per the ticket's explicit ask), so a same-named container
	// under both docker and podman is diagnosed on both in sequence and
	// reported for each (Q3) — never silently one.
	//
	// A per-runtime enumeration failure (container or volume listing) is
	// aggregated into ownerErrs rather than aborting immediately: when the
	// other runtime's query still resolved a genuine owner, that owner must
	// still be diagnosed (mirroring ls/stop/prune's partial-result
	// handling) — a tier only falls through to the next when owners is
	// genuinely empty, never merely because that tier's query errored on one
	// runtime. ownerErrs is folded into runPerOwner's aggregated error below
	// so the enumeration failure is still surfaced and still produces a
	// non-zero exit.
	runtimes := []string{eng.Runtime}
	if available, availErr := sandbox.AvailableRuntimes(); availErr == nil {
		runtimes = available
	} else {
		fmt.Fprintf(os.Stderr, "warning: dual-runtime enumeration failed, falling back to %s: %v\n", eng.Runtime, availErr)
	}
	var ownerErrs []error
	owners, err := sandbox.RuntimesWithContainer(runtimes, scope.ContainerName)
	if err != nil {
		ownerErrs = append(ownerErrs, err)
	}
	if len(owners) == 0 {
		var volErr error
		owners, volErr = sandbox.RuntimesWithVolume(runtimes, scope.VolumeName)
		if volErr != nil {
			ownerErrs = append(ownerErrs, volErr)
		}
	}
	if len(owners) == 0 {
		owners = []string{eng.Runtime}
	}
	labelEach := len(owners) > 1

	// runPerOwner runs action against each owning runtime in turn, printing
	// a "Runtime: <rt>" label first whenever more than one runtime owns the
	// target (Q3). A failure on one owner is collected but never aborts the
	// loop, so a healthy owner's report/verify still runs and is printed —
	// matching sandbox_cmd.go's update-agent/update-plugins/prune/ls/stop
	// pattern in the same package; only after the full loop completes does a
	// collected failure produce a non-zero exit.
	runPerOwner := func(action func(*launcher.Engine) error) {
		errs := append([]error(nil), ownerErrs...)
		for _, rt := range owners {
			if labelEach {
				_, _ = fmt.Fprintf(os.Stdout, "Runtime: %s\n", rt)
			}
			if err := action(eng.WithRuntime(rt)); err != nil {
				errs = append(errs, err)
			}
		}
		if err := errors.Join(errs...); err != nil {
			fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
			os.Exit(1)
		}
	}

	if *verify {
		runPerOwner(func(e *launcher.Engine) error { return e.Verify(scope) })
		return
	}
	runPerOwner(func(e *launcher.Engine) error { return e.Diagnose(scope) })
}
