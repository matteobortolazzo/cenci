package main

import (
	"fmt"
	"os"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// rejectExtra enforces the strict-parsing convention shared by every cenci
// subcommand: any positional argument left over after flag parsing is a
// usage error (exit 2), since the flag parser stops at the first non-flag
// token and would otherwise silently swallow everything after it. cmdPrefix
// is the full "cenci <command...>" prefix used in the error message.
func rejectExtra(cmdPrefix string, extra []string) {
	rejectExtraWithUsage(cmdPrefix, extra, "")
}

// rejectExtraWithUsage is rejectExtra's sibling for verbs whose grammar
// changed enough that naming just the offending argument isn't enough — it
// also prints a usage line showing the current grammar, so e.g. `cenci
// diagnose mysession` (the retired positional form) tells the operator both
// what was rejected and what to use instead. An empty usage string reduces
// to rejectExtra's plain behavior, so rejectExtra's existing call sites are
// unaffected by this refactor.
func rejectExtraWithUsage(cmdPrefix string, extra []string, usage string) {
	if len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "%s: unexpected argument %q\n", cmdPrefix, extra[0])
		if usage != "" {
			fmt.Fprintln(os.Stderr, usage)
		}
		os.Exit(2)
	}
}

// currentScope computes the launch scope for the current working directory,
// exiting with a runtime error when cwd or home cannot be resolved.
// cmdPrefix is the full "cenci <command...>" prefix used in the error
// message (e.g. "cenci sandbox build", "cenci diagnose").
func currentScope(cmdPrefix, agent, instanceName string) launcher.Scope {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmdPrefix, err)
		os.Exit(1)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmdPrefix, err)
		os.Exit(1)
	}
	return launcher.ComputeScope(agent, instanceName, cwd, home)
}
