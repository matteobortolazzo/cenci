package pipeline

// Plan-file-triggered stage adoption (ticket #688, closing #718 item 1):
// `cenci pipeline plan <id> --approve` on a ticket whose persisted stage
// pre-dates or lacks `waiting_for_plan_approval` tracking (e.g. the
// `.cenci/pipeline/` state file was deleted, or the plan was written before
// this machinery existed) would otherwise hard-fail with
// ErrInvalidTransition even though a valid `.plans/<id>-*.md` plan file is
// sitting on disk. adoptPlanFileStage lets that call succeed by treating
// the persisted stage as if it were already waiting_for_plan_approval,
// purely on local, offline, validated evidence -- never by relaxing
// transition() itself (statemachine.go and statemachine_test.go:205 stay
// byte-identical).
//
// adoptPlanFileStage is called from Run's own locked closure (pipeline.go),
// immediately before transition() runs. It only ever returns a decision:
// the actual Stage/PlanPath mutation happens in Run, inside the same lock
// acquisition -- adoptPlanFileStage itself performs no write and calls
// neither SetArtifacts nor Reset (withLock is not reentrant in-process, so
// doing so from here would self-deadlock into ErrLockContention).
//
// Reuses planfile.go's existing discovery (.plans/<id>-*.md glob) and
// parseAndValidatePlan validation verbatim -- this file does not
// reimplement front-matter parsing or glob logic.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// adoptPlanFileStage implements the ticket #688 plan's "Detection semantics
// -- item 1 (exact)" gate. Adoption is granted only when ALL of the
// following hold; any failure means no adoption (default-deny, per
// watch/AGENTS.md #598/#628) and the caller must fall through to today's
// unmodified transition()/ErrInvalidTransition (or ErrNotPrepared) behavior,
// verbatim:
//
//  1. o.Stage == "plan" && o.Approve == true -- adoption is narrowly scoped
//     to `plan --approve` only; bare `plan` keeps its own strict precondition.
//  2. The persisted stage s.Stage is a KNOWN stage (stageRank ok) ranking
//     STRICTLY BELOW StageWaitingForPlanApproval (i.e. new or prepared). An
//     unknown/corrupt persisted stage is default-deny.
//  3. The plan repo root resolves (resolvePlanRepoRoot(o.RepoRoot)); a
//     resolution failure (e.g. --state-dir used outside a git repo) is
//     non-fatal and simply means no adoption.
//  4. `.plans/<id>-*.md` returns EXACTLY ONE match (0 or 2+ means no
//     adoption -- ambiguity is never resolved silently).
//  5. That single match passes parseAndValidatePlan -- the identical gate
//     plan-check applies (front matter, all four required sections, slug).
//  6. The plan's front-matter ticketId is either absent/0 (pre-dating the
//     field) or equal to <id>. A mismatch means no adoption.
//
// adoptPlanFileStage makes no gh/git call: the decision is purely local and
// offline (TestAdopt_InvokesCommandSeamZeroTimes pins this).
func adoptPlanFileStage(o Opts, s State) (planPath string, ok bool) {
	// (1) Scoped to `plan --approve` only.
	if o.Stage != "plan" || !o.Approve {
		return "", false
	}

	// (2) Persisted stage must be known and rank strictly below the
	// adoption target.
	fromRank, fromOk := stageRank(s.Stage)
	if !fromOk {
		return "", false
	}
	targetRank, targetOk := stageRank(StageWaitingForPlanApproval)
	if !targetOk || fromRank >= targetRank {
		return "", false
	}

	// (3) The plan repo root must resolve.
	repoRoot, err := resolvePlanRepoRoot(o.RepoRoot)
	if err != nil {
		return "", false
	}

	// (4) Exactly one .plans/<id>-*.md match.
	matches, err := filepath.Glob(filepath.Join(repoRoot, ".plans", o.ID+"-*.md"))
	if err != nil || len(matches) != 1 {
		return "", false
	}
	path := matches[0]

	content, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}

	// (5) The single match must pass the identical validation gate
	// plan-check applies.
	_, meta, err := parseAndValidatePlan(path, string(content))
	if err != nil {
		return "", false
	}

	// (6) ticketId absent/0, or equal to <id>. o.ID is already validated
	// against ^\d+$ by resolveStatePath before Run's locked closure runs,
	// so strconv.Atoi never fails here in practice; a conversion failure is
	// treated the same as any other precondition failure -- no adoption.
	if meta.TicketID != 0 {
		wantID, convErr := strconv.Atoi(o.ID)
		if convErr != nil || meta.TicketID != wantID {
			return "", false
		}
	}

	return path, true
}

// planAdoptionWarning renders the one-line advisory surfaced in
// Output.Warnings when adoptPlanFileStage grants adoption: names the
// adopted plan path, the new stage, and the stage that was persisted before
// adoption.
func planAdoptionWarning(path string, oldStage Stage) string {
	return fmt.Sprintf(
		"adopted plan file %s as stage %q (persisted stage was %q; no prior plan approval was recorded)",
		path, StageWaitingForPlanApproval, oldStage,
	)
}
