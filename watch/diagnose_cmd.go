package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

// diagnoseUsage is the one-line grammar shown on a diagnose usage error (a
// bad/unknown flag, an invalid --agent, or a leftover positional — the
// retired <session> form).
const diagnoseUsage = "usage: cenci diagnose [--name <session>] [--agent claude|codex|opencode] [--verify]"

// runDiagnose implements `cenci diagnose [--name <session>] [--agent
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
// --name selects the sandbox session to diagnose; omitting it diagnoses the
// default (bare `cenci open`) session for the given agent, using the same
// scope resolution as `sandbox update-plugins`'s currentScope. There is no
// positional <session> form — it was fully retired (no deprecation period)
// so a bare `cenci open` session (no explicit name) is diagnosable too.
//
// diagnose is a report, not a pass/fail gate: a successful render exits 0
// even when it finds fatal/degraded issues. Only usage errors (unknown flag,
// invalid --agent, a leftover positional) and cwd/home/runtime resolution
// failures produce a non-zero exit.
func runDiagnose(args []string) {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "sandbox instance name (default: the bare `cenci open` session for --agent)")
	agent := fs.String("agent", "claude", "agent whose sandbox session to diagnose (claude, codex, or opencode)")
	verify := fs.Bool("verify", false, "re-run the read-only diagnostic probes and report pass/fail per verifiable check instead of the full report")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		fmt.Fprintln(os.Stderr, diagnoseUsage)
		os.Exit(2)
	}
	rejectExtraWithUsage("cenci diagnose", fs.Args(), diagnoseUsage)

	if err := launcher.ValidateAgent(*agent); err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(2)
	}

	scope := currentScope("cenci diagnose", *agent, *name)

	eng, err := launcher.New(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci diagnose: %v\n", err)
		os.Exit(1)
	}

	// Resolve every runtime that actually owns this scope's target: among
	// container owners, a runtime whose copy is currently running is acted
	// on before a runtime whose copy is merely stopped (OwnersRunningFirst);
	// when there is no container owner at all, fall back to the runtime(s)
	// holding the scope's home volume; else fall back to the single
	// preferred runtime — the same resolution `cenci sandbox update-plugins`
	// uses (kept consistent per the ticket's explicit ask), so a same-named
	// container under both docker and podman is diagnosed on both in
	// sequence and reported for each (Q3) — never silently one, and
	// running-first only changes which owner's report is printed first,
	// never which runtimes are diagnosed.
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
	containerOwners, err := sandbox.RuntimesWithContainer(runtimes, scope.ContainerName)
	if err != nil {
		ownerErrs = append(ownerErrs, err)
	}
	var owners []string
	if len(containerOwners) > 0 {
		owners = sandbox.OwnersRunningFirst(containerOwners)
	} else {
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
