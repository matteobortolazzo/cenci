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
// Reuses the shared planfile.Read/Select identity contract (#884) and
// parseAndValidatePlan validation verbatim -- this file does not
// reimplement front-matter parsing or directory-enumeration logic.

import (
	"fmt"
	"strconv"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/planfile"
)

// adoptPlanFileStage implements the ticket #688 plan's "Detection semantics
// -- item 1 (exact)" gate, retargeted by ticket #826 (gate 2), extended with
// gate 7, and unified onto the shared plan-inventory identity contract by
// ticket #884 (gates 4/6). Adoption is granted only when ALL of the
// following hold; any failure means no adoption (default-deny, per
// watch/docs/go-gotchas.md #598, watch/docs/error-handling.md #628) and the
// caller must fall through to today's unmodified
// transition()/ErrInvalidTransition (or ErrNotPrepared) behavior, verbatim:
//
//  1. o.Stage == "plan" && o.Approve == true -- adoption is narrowly scoped
//     to `plan --approve` only; bare `plan` keeps its own strict precondition.
//  2. The persisted stage s.Stage is a KNOWN stage (stageRank ok) ranking
//     STRICTLY BELOW StageWaitingForInput (i.e. new or prepared; #826
//     retargeted this from StageWaitingForPlanApproval so a ticket already
//     parked at waiting_for_input -- escalated, blocked on a human -- is
//     never silently adopted). An unknown/corrupt persisted stage is
//     default-deny.
//  3. The plan repo root resolves (resolvePlanRepoRoot(o.RepoRoot)); a
//     resolution failure (e.g. --state-dir used outside a git repo) is
//     non-fatal and simply means no adoption.
//  4. planfile.Read(repoRoot).Select(id) resolves exactly one healthy claim
//     (planfile.SelectSingle) -- an absent, ambiguous, or broken (including
//     identity-mismatched) inventory means no adoption. This subsumes the
//     pre-#884 "exactly one .plans/<id>-*.md glob match" gate AND the old
//     gate 6 below: Select's identity contract already requires the
//     filename's numeric prefix to equal the front-matter ticketId for a
//     claim to resolve healthy.
//  5. That single match passes parseAndValidatePlan -- the identical gate
//     plan-check applies (front matter, all three required sections, slug).
//  6. (#884, Q4: unify) A plan file whose front matter carries no ticketId
//     at all (a legacy plan pre-dating the field) is NO LONGER exempt --
//     Select's identity contract already denied it at gate (4) above
//     (HealthIDMismatch, since a missing ticketId can never equal the
//     filename's numeric prefix), so there is nothing further to check
//     here. A legacy plan file needs a ticketId line added, or a re-plan
//     (`plan --replan`), to become adoptable again.
//  7. The plan's front-matter status is NOT "awaiting-input" (#826). Gate
//     (2)'s stage retarget alone cannot close this hole: if the
//     `.cenci/pipeline/<id>.json` state file itself was deleted (or never
//     written), the persisted stage reads as new/prepared regardless of what
//     the draft on disk says, so the front matter must be checked directly.
//
// adoptPlanFileStage makes no gh/git call: the decision is purely local and
// offline (TestAdopt_InvokesCommandSeamZeroTimes pins this).
func adoptPlanFileStage(o Opts, s State) (planPath string, ok bool) {
	// (1) Scoped to `plan --approve` only.
	if o.Stage != "plan" || !o.Approve {
		return "", false
	}

	// (2) Persisted stage must be known and rank strictly below the
	// adoption gate (waiting_for_input, #826).
	fromRank, fromOk := stageRank(s.Stage)
	if !fromOk {
		return "", false
	}
	gateRank, gateOk := stageRank(StageWaitingForInput)
	if !gateOk || fromRank >= gateRank {
		return "", false
	}

	// (3) The plan repo root must resolve.
	repoRoot, err := resolvePlanRepoRoot(o.RepoRoot)
	if err != nil {
		return "", false
	}

	// o.ID is already validated against ^\d+$ by resolveStatePath before
	// Run's locked closure runs, so strconv.Atoi never fails here in
	// practice; a conversion failure is treated the same as any other
	// precondition failure -- no adoption.
	wantID, convErr := strconv.Atoi(o.ID)
	if convErr != nil {
		return "", false
	}

	// (4)/(6) Exactly one healthy claim under the shared identity contract.
	sel := planfile.Read(repoRoot).Select(wantID)
	if sel.Result != planfile.SelectSingle {
		return "", false
	}
	path := sel.Entry.Path

	// (5) The single match must pass the identical validation gate
	// plan-check applies. Consumes sel.Entry.Content -- the content
	// planfile.Read already read once during the inventory scan -- instead
	// of re-opening path (Phase 6+7 review finding #3): a second by-path
	// os.ReadFile is a TOCTOU window, since identity was proven against the
	// first read but a second read follows whatever now sits at that path.
	fm, _, err := parseAndValidatePlan(path, sel.Entry.Content)
	if err != nil {
		return "", false
	}

	// (7) Never adopt a draft still awaiting human input (#826).
	if fm["status"] == "awaiting-input" {
		return "", false
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
