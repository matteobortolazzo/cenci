package main

// `cenci pipeline reset <id>` (ticket #732): the escape hatch that deletes a
// ticket's persisted pipeline state file outright, returning it to stage
// "new" with every recorded artifact dropped from tracking. Kept out of
// pipeline_mechanics_cmd.go because reset renders its own warnings-bearing
// pipeline.Output directly (from pipeline.Reset) rather than going through
// renderMechanicsOutput, which hardcodes Warnings: []string{}.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
)

const pipelineResetUsage = "cenci pipeline reset: usage: cenci pipeline reset <id> [--state-dir DIR] [--repo PATH]"

func pipelineResetUsageExit() {
	fmt.Fprintln(os.Stderr, pipelineResetUsage)
	os.Exit(2)
}

// runPipelineReset implements `cenci pipeline reset <id> [--state-dir DIR]
// [--repo PATH]`. No confirmation flag (docs/cli-conventions.md mandates no
// such convention, and the verb must stay scriptable): it never refuses
// based on stage, including a mid-run ticket a human may be rewinding while
// an agent works.
func runPipelineReset(rest []string) {
	id := mechanicsID(rest, pipelineResetUsageExit)

	fs := flag.NewFlagSet("pipeline reset", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", "", "override the pipeline state directory (test hook)")
	repo := fs.String("repo", "", "override the resolved repo root (test hook)")
	if err := fs.Parse(rest[1:]); err != nil {
		pipelineResetUsageExit()
	}
	if len(fs.Args()) > 0 {
		pipelineResetUsageExit()
	}

	out, err := pipeline.Reset(pipeline.ResetOpts{ID: id, RepoRoot: *repo, StateDir: *stateDir})

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
