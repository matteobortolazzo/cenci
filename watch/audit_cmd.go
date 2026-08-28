package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

// runAudit implements `cenci audit [flags]` (ticket #588): a read-only
// report of the effective sandbox security posture the launcher WOULD apply
// for the current repo/agent/flags — mounts (ro/rw), env var NAMES, network
// mode, nested-Docker (dind) mode, credential sources, named volumes, image
// type, and opt-in boundary weakenings — as human-readable text by default,
// or stable JSON under --json. See internal/sandbox/launcher/audit.go for
// the Posture construction, which reuses the same assemble* methods Launch
// itself calls rather than re-deriving them.
//
// Like diagnose (diagnose_cmd.go), audit never launches, attaches, or wires
// the daemon: the Engine constructed here carries no resolved container
// runtime and a discarded Stderr, since the reused assemble* methods print
// launch-time isolation warnings that don't apply to a report.
func runAudit(args []string) {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agentFlag := fs.String("agent", "claude", "agent to audit (claude, codex, or opencode)")
	nameFlag := fs.String("name", "", "sandbox instance name")
	dindFlag := fs.Bool("dind", false, "audit as if launched with nested Docker (sysbox-runc) enabled")
	noDindFlag := fs.Bool("no-dind", false, "audit as if launched with nested Docker forced off")
	hostNetworkFlag := fs.Bool("host-network", false, "audit as if launched with host network mode")
	reseedFlag := fs.Bool("reseed-creds", false, "audit as if launched with a forced credential reseed")
	jsonFlag := fs.Bool("json", false, "emit the posture as stable JSON instead of human-readable text")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "cenci audit: %v\n", err)
		os.Exit(2)
	}
	rejectExtra("cenci audit", fs.Args())

	if err := launcher.ValidateAgent(*agentFlag); err != nil {
		fmt.Fprintf(os.Stderr, "cenci audit: %v\n", err)
		os.Exit(2)
	}

	// No resolved AssetDir/BaseTag: Audit never needs the sandbox asset dir,
	// so NewForAuditWithRuntime (rather than launcher.New/NewForLaunch)
	// avoids an unrelated asset-dir-resolution failure for what is otherwise
	// a read-only report. Unlike NewForAudit, it best-effort resolves the
	// container runtime (ticket #627) so Audit can report a running scoped
	// container's actual inspected posture (basis:"running") instead of
	// always deriving a hypothetical plan; a missing runtime degrades to the
	// same runtime-less, planned-only behavior NewForAudit always had.
	eng := launcher.NewForAuditWithRuntime(os.Stdin, os.Stdout)
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
			fmt.Fprintf(os.Stderr, "cenci audit: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "cenci audit: %v\n", err)
		os.Exit(1)
	}

	if *jsonFlag {
		data, err := json.MarshalIndent(posture, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "cenci audit: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}
	if err := posture.WriteText(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "cenci audit: %v\n", err)
		os.Exit(1)
	}
}
