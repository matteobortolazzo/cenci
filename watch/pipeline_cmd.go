package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/pipeline"
)

// pipelineUsage is the one-line hint printed for every malformed-CLI
// invocation of `cenci pipeline` (unknown/missing stage, missing/non-numeric
// id, unrecognized flag, trailing positional, --approve on a non-plan
// stage) — exit 2, per docs/cli-conventions.md.
const pipelineUsage = "cenci pipeline: usage: cenci pipeline prepare|plan|execute|review|finalize <id> [--approve] [--state-dir DIR] [--repo PATH]"

// pipelineStages are the five valid `cenci pipeline` subcommands, in the
// plan's State Machine Design order.
var pipelineStages = map[string]bool{
	"prepare":  true,
	"plan":     true,
	"execute":  true,
	"review":   true,
	"finalize": true,
}

var pipelineIDPattern = regexp.MustCompile(`^\d+$`)

// runPipeline implements `cenci pipeline <stage> <id> [--approve]
// [--state-dir DIR] [--repo PATH]`: it parses and validates the CLI surface
// (usage errors exit 2 with a one-line stderr hint, per
// docs/cli-conventions.md), dispatches into internal/pipeline's engine, and
// renders the returned {state, next_actions, artifacts, warnings, errors}
// contract as JSON on stdout. Domain errors (invalid transition, ticket not
// found, etc.) still print the full contract and exit 1 — only malformed CLI
// input exits 2 without ever reaching the engine.
func runPipeline(args []string) {
	if len(args) == 0 {
		pipelineUsageExit()
	}
	stage := args[0]
	if !pipelineStages[stage] {
		pipelineUsageExit()
	}

	rest := args[1:]
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		pipelineUsageExit()
	}
	id := rest[0]
	if !pipelineIDPattern.MatchString(id) {
		pipelineUsageExit()
	}

	fs := flag.NewFlagSet("pipeline "+stage, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	approve := fs.Bool("approve", false, "mark the plan as approved (plan stage only)")
	stateDir := fs.String("state-dir", "", "override the pipeline state directory (test hook)")
	repo := fs.String("repo", "", "override the resolved repo root (test hook)")
	if err := fs.Parse(rest[1:]); err != nil {
		pipelineUsageExit()
	}
	if len(fs.Args()) > 0 {
		pipelineUsageExit()
	}
	if *approve && stage != "plan" {
		pipelineUsageExit()
	}

	out, err := pipeline.Run(pipeline.Opts{
		Stage:    stage,
		ID:       id,
		Approve:  *approve,
		RepoRoot: *repo,
		StateDir: *stateDir,
	})

	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "cenci pipeline: %v\n", marshalErr)
		os.Exit(1)
	}
	fmt.Println(string(b))

	if err != nil {
		os.Exit(1)
	}
}

// pipelineUsageExit prints pipeline's own usage hint to stderr and exits 2.
// It never emits the stdout JSON contract — that's reserved for domain
// errors reached only after CLI parsing succeeds.
func pipelineUsageExit() {
	fmt.Fprintln(os.Stderr, pipelineUsage)
	os.Exit(2)
}
