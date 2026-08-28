package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/dispatch"
)

// runPlanning implements `cenci planning` (#1086): a new top-level noun,
// alongside dispatch/automerge, for fleet-scoped planning switches. Today it
// only routes to the "attended" subverb, mirroring the two-level shape
// `cenci dispatch plan-refined` uses -- planning is a distinct top-level
// noun (not `cenci dispatch attended`) because the flag is not
// dispatch-scoped: the flow-side consumption ticket reads it too.
func runPlanning(args []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "attended":
			runPlanningAttended(args[1:])
			return
		default:
			fmt.Fprintf(os.Stderr, "cenci planning: unknown subcommand %q\n", args[0])
			os.Exit(2)
		}
	}
	fmt.Fprintln(os.Stderr, "cenci planning: expected a subcommand: attended")
	os.Exit(2)
}

// runPlanningAttended implements `cenci planning attended on|off|status`
// (#1086): the CLI writer/reader for the fleet-wide planning.attended
// narrowing switch, mirroring runDispatchPlanRefined's shape exactly. All
// three verbs print the same resolved state; on/off persist the toggle
// first. status prints the fleet flag, the repo's remote-confirmed
// planning.autonomy verdict (QueryRepoAutonomy, unnarrowed), dispatch's own
// planRefined flag, and the three-factor combined verdict (Q1: attended +
// planRefined + repo autonomy) -- the same shape `cenci dispatch plan-refined
// status` prints, so the two commands can never disagree.
func runPlanningAttended(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci planning attended: expected a subcommand: on, off, or status")
		os.Exit(2)
	}
	verb := args[0]

	fs := flag.NewFlagSet("attended "+verb, flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	dir := fs.String("dir", ".", "repo directory for the autonomy verdict (default: current directory)")
	jsonOut := fs.Bool("json", false, "print result as JSON")
	_ = fs.Parse(args[1:])

	rejectExtra("cenci planning attended", fs.Args())

	switch verb {
	case "on":
		if err := dispatch.SetPlanningAttended(*configPath, true); err != nil {
			fmt.Fprintf(os.Stderr, "cenci planning attended: %v\n", err)
			os.Exit(1)
		}
	case "off":
		if err := dispatch.SetPlanningAttended(*configPath, false); err != nil {
			fmt.Fprintf(os.Stderr, "cenci planning attended: %v\n", err)
			os.Exit(1)
		}
	case "status":
		// no mutation; just resolve and print below.
	default:
		fmt.Fprintf(os.Stderr, "cenci planning attended: unknown subcommand %q\n", verb)
		os.Exit(2)
	}

	resolvedPath, err := dispatch.ResolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci planning attended: %v\n", err)
		os.Exit(1)
	}
	attended, err := dispatch.QueryPlanningAttended(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci planning attended: %v\n", err)
		os.Exit(1)
	}
	planRefined, err := dispatch.QueryPlanRefined(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci planning attended: %v\n", err)
		os.Exit(1)
	}

	// Repo fields only render when --dir resolves to a git repo with an
	// origin remote -- status must work from anywhere. Mirrors
	// runDispatchPlanRefined's own identity/autonomy probing exactly.
	var repoName string
	var autonomy dispatch.RepoAutonomy
	if identity, derr := dispatch.DetectRepoIdentity(*dir); derr == nil {
		repoName = identity.Repo
		autonomy = dispatch.QueryRepoAutonomy(*dir)
	}

	if *jsonOut {
		out := struct {
			Attended     bool   `json:"attended"`
			Config       string `json:"config"`
			Repo         string `json:"repo,omitempty"`
			RepoAutonomy string `json:"repo_autonomy,omitempty"`
			Authorized   *bool  `json:"authorized,omitempty"`
			PlanRefined  bool   `json:"plan_refined"`
		}{Attended: attended, Config: resolvedPath, PlanRefined: planRefined}
		if repoName != "" {
			authorized := planningAttendedAuthorized(attended, planRefined, autonomy)
			out.Repo = repoName
			out.RepoAutonomy = string(autonomy)
			out.Authorized = &authorized
		}
		data, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cenci planning attended: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}

	attendedStr := "disabled"
	if attended {
		attendedStr = "enabled"
	}
	fmt.Printf("Attended mode (planning.attended): %s (config: %s)\n", attendedStr, resolvedPath)
	if !attended {
		fmt.Printf("  enable with: cenci planning attended on\n")
	}
	if repoName != "" {
		fmt.Printf("  repo: %s\n", repoName)
		fmt.Printf("  repo autonomy (planning.autonomy @ origin/main, as of last fetch): %s\n", autonomy)
		fmt.Printf("  plan-refined (dispatch.planRefined): %v\n", planRefined)
		if planningAttendedAuthorized(attended, planRefined, autonomy) {
			fmt.Printf("  authorized: yes — Refined tickets here are picked up for unattended planning\n")
		} else {
			fmt.Printf("  authorized: no — needs attended off, the fleet plan-refined flag on, and planning.autonomy \"lean\" committed on origin/main\n")
		}
	}
}

// planningAttendedAuthorized computes the three-factor combined verdict
// (#1086, Q1): attended, dispatch.planRefined, and the repo's own committed
// planning.autonomy must ALL agree for an unattended planning pickup to
// actually fire -- attended being on always suppresses it regardless of the
// other two, so this can never disagree with runDispatchPlanRefined's own
// authorized computation.
func planningAttendedAuthorized(attended, planRefined bool, autonomy dispatch.RepoAutonomy) bool {
	return !attended && planRefined && autonomy == dispatch.RepoAutonomyLean
}
