package main

// `cenci pipeline plan-check <id>` (ticket #560): a thin CLI wrapper around
// pipeline.CheckPlan. It renders the same {state, next_actions, artifacts,
// warnings, errors} contract the other mechanics verbs do, plus the
// decision/plan fields ticket #560 added to pipeline.Output.
//
// Exit-code framing is a DELIBERATE deviation from renderMechanicsOutput's
// blanket "err != nil -> exit 1" (pipeline_mechanics_cmd.go): CheckPlan's
// design intentionally returns a non-nil Go error for the "none" decision
// (no `.plans/<id>-*.md` file yet -- the everyday "proceed with normal
// ticket-mode planning" outcome for most `/cenci:implement <ticket>`
// invocations) and the "multiple" decision (ask the human which plan file to
// use), mirroring pipeline.Run's own ErrTicketNotFound-on-prepare
// precedent: a well-defined, expected-ish domain outcome that still needs a
// non-nil error/errors[] entry for observability, but is NOT a hard-stop
// failure a caller should treat as "something is broken". So this wrapper
// exits 0 for every decision except the "" (unset) decision, which exits 1
// like any other domain error. Decision "" has two distinct sources (#560
// item 8), both genuinely "something needs fixing", never silently
// defaulted: (1) ErrPlanMalformed -- the single matching plan file exists but
// fails front-matter/section/slug validation; (2) a freshness-check failure
// -- planIsStale could not determine whether the plan is stale (an
// unreachable/invalid planCommitSha, a git failure, or a `gh issue view`
// failure/decode/parse error) and refuses to default that unverifiable
// signal to "not stale". Each failure class carries a content-distinct error
// message (rule #446) so callers/tests can tell them apart via
// errors.Is(ErrPlanMalformed) or a message-marker check, even though both
// render as decision "". Usage errors (missing/non-numeric <id>,
// unrecognized flag, trailing positional) still exit 2 with a one-line
// stderr hint, per docs/cli-conventions.md, and never reach
// pipeline.CheckPlan at all.
import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/matteobortolazzo/cenci/watch/internal/pipeline"
)

const pipelinePlanCheckUsage = "cenci pipeline plan-check: usage: cenci pipeline plan-check <id> " +
	"[--replan-requested] [--repo-slug OWNER/REPO] [--state-dir DIR] [--repo PATH]"

func pipelinePlanCheckUsageExit() {
	fmt.Fprintln(os.Stderr, pipelinePlanCheckUsage)
	os.Exit(2)
}

// runPipelinePlanCheck implements `cenci pipeline plan-check <id>
// [--replan-requested] [--repo-slug O/R] [--state-dir DIR] [--repo PATH]`.
func runPipelinePlanCheck(rest []string) {
	id := mechanicsID(rest, pipelinePlanCheckUsageExit)

	fs := flag.NewFlagSet("pipeline plan-check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	replanRequested := fs.Bool("replan-requested", false, "short-circuit straight to the replan decision, skipping freshness")
	repoSlug := fs.String("repo-slug", "", "override the resolved owner/repo gh target (test hook)")
	stateDir := fs.String("state-dir", "", "override the pipeline state directory (test hook)")
	repo := fs.String("repo", "", "override the resolved repo root (test hook)")
	if err := fs.Parse(rest[1:]); err != nil {
		pipelinePlanCheckUsageExit()
	}
	if len(fs.Args()) > 0 {
		pipelinePlanCheckUsageExit()
	}

	state, check, err := pipeline.CheckPlan(pipeline.PlanCheckOpts{
		ID:              id,
		ReplanRequested: *replanRequested,
		RepoRoot:        *repo,
		StateDir:        *stateDir,
		RepoSlug:        *repoSlug,
	})

	artifacts := check.Paths
	if artifacts == nil {
		artifacts = []string{}
	}
	out := pipeline.Output{
		State:       string(state.Stage),
		NextActions: pipeline.NextActionsFor(state.Stage),
		Artifacts:   artifacts,
		Warnings:    []string{},
		Errors:      []string{},
		Decision:    check.Decision,
		Plan:        check.Plan,
	}
	if err != nil {
		out.Errors = []string{err.Error()}
	}

	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "cenci pipeline: %v\n", marshalErr)
		os.Exit(1)
	}
	fmt.Println(string(b))

	// See this file's package-level doc comment: only the "" (unset)
	// decision -- ErrPlanMalformed -- is a genuine domain-error hard stop
	// here. "none" and "multiple" also carry a non-nil err/errors[] entry
	// but are normal continuation branches, so they exit 0.
	if err != nil && check.Decision == "" {
		os.Exit(1)
	}
}
