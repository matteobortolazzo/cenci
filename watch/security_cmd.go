package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// This file implements the `cenci security <verb>` group (ticket #594):
// today, only "explain". Mirrors runSandboxGroup's (sandbox_cmd.go) bare/
// unknown-subcommand handling.

// runSecurityGroup implements `cenci security <verb> [flags]`.
func runSecurityGroup(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci security: expected a subcommand: explain")
		os.Exit(2)
	}
	verb := args[0]
	rest := args[1:]

	switch verb {
	case "explain":
		runSecurityExplain(rest)
	default:
		fmt.Fprintf(os.Stderr, "cenci security: unknown subcommand %q\n", verb)
		os.Exit(2)
	}
}

// runSecurityExplain implements `cenci security explain [flags]`: renders
// the same Posture `cenci audit` derives (see
// internal/sandbox/launcher/audit.go) as a text-only, plain-language "why
// this is/isn't safe" narrative via Posture.WriteExplanation (explain.go).
// Like audit (audit_cmd.go), it never launches, attaches, or wires the
// daemon: the Engine constructed here carries no resolved container runtime
// and a discarded Stderr.
func runSecurityExplain(args []string) {
	fs := flag.NewFlagSet("security explain", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agentFlag := fs.String("agent", "claude", "agent to explain (claude, codex, or opencode)")
	nameFlag := fs.String("name", "", "sandbox instance name")
	dindFlag := fs.Bool("dind", false, "explain as if launched with nested Docker (sysbox-runc) enabled")
	noDindFlag := fs.Bool("no-dind", false, "explain as if launched with nested Docker forced off")
	hostNetworkFlag := fs.Bool("host-network", false, "explain as if launched with host network mode")
	reseedFlag := fs.Bool("reseed-creds", false, "explain as if launched with a forced credential reseed")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "cenci security explain: %v\n", err)
		os.Exit(2)
	}
	rejectExtra("cenci security explain", fs.Args())

	if err := launcher.ValidateAgent(*agentFlag); err != nil {
		fmt.Fprintf(os.Stderr, "cenci security explain: %v\n", err)
		os.Exit(2)
	}

	eng := launcher.NewForAudit(os.Stdin, os.Stdout)
	posture, err := eng.Audit(launcher.Options{
		Agent:       *agentFlag,
		Name:        *nameFlag,
		Dind:        *dindFlag,
		NoDind:      *noDindFlag,
		HostNetwork: *hostNetworkFlag,
		ReseedCreds: *reseedFlag,
	})
	if err != nil {
		if launcher.IsUsage(err) {
			fmt.Fprintf(os.Stderr, "cenci security explain: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "cenci security explain: %v\n", err)
		os.Exit(1)
	}

	if err := posture.WriteExplanation(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "cenci security explain: %v\n", err)
		os.Exit(1)
	}
}
