package pipeline

// Integration tests for plan-file-triggered stage adoption (ticket #688,
// closing #718 item 1): "cenci pipeline plan <id> --approve succeeds on a
// ticket whose persisted stage predates/lacks waiting_for_plan_approval."
// Exercised entirely through the public Run entry point (pipeline.go), per
// the plan's Test Strategy row for adopt.go + Run wiring -- these are
// integration tests against real temp git-adjacent directories and real
// `.plans/<id>-*.md` files (reusing planfile_test.go's writePlanFile/
// validPlanBody/defaultPlanFields fixtures and worktree_test.go's
// recordingCommand helper, both package-level and reusable from this file),
// not unit tests of the not-yet-written adoptPlanFileStage directly.
//
// Detection semantics under test (plan's "Detection semantics -- item 1
// (exact)" section, unified onto the shared planfile.Read/Select identity
// contract by #884): adopt only when ALL of (1) o.Stage=="plan" &&
// o.Approve==true, (2) the persisted stage ranks strictly below
// waiting_for_plan_approval (new or prepared), (3) the plan repo root
// resolves, (4) planfile.Read(repoRoot).Select(id) resolves exactly one
// healthy claim, (5) that file passes parseAndValidatePlan, (6) (#884, Q4:
// unify) a front-matter ticketId that is absent/0 is NO LONGER exempt -- it
// is already denied at gate (4) as an identity mismatch, so a legacy plan
// file needs a ticketId line added, or a re-plan, to become adoptable
// again. Any failure means no adoption and today's ErrInvalidTransition/
// ErrNotPrepared behavior is preserved verbatim -- default-deny throughout
// (watch/docs/go-gotchas.md #598, watch/docs/error-handling.md #628).
//
// RED: adopt.go does not exist yet, so Run never calls adoptPlanFileStage.
// Every "should adopt" case below currently hard-fails with
// ErrInvalidTransition instead of succeeding -- an assertion failure, not a
// compile error, since these tests only call the already-existing exported
// Run/mustSeedState/loadState surface.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// mustWriteValidAdoptPlan writes a valid .plans/<id>-<slug>.md (all four
// required sections, a well-formed slug, ticketId matching id) under
// repoRoot, returning its path. Mirrors planfile_test.go's own fixture
// construction (defaultPlanFields + validPlanBody) so this file does not
// duplicate front-matter-shape knowledge.
func mustWriteValidAdoptPlan(t *testing.T, repoRoot, id, slug string) string {
	t.Helper()
	fields := defaultPlanFields(id, slug, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	return writePlanFile(t, repoRoot, id, slug, fields, validPlanBody)
}

// loadPersistedStage reads the on-disk Stage for id under stateDir (mirrors
// mustSeedState's own path convention), failing the test on any read error.
func loadPersistedStage(t *testing.T, stateDir, id string) Stage {
	t.Helper()
	path := filepath.Join(stateDir, id+".json")
	s, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState(%s): %v", path, err)
	}
	return s.Stage
}

// assertAdoptionWarning asserts warnings holds exactly one entry that names
// the adopted plan path, the new stage (waiting_for_plan_approval), and the
// old persisted stage -- per the plan's example warning text. Content
// markers, not the full literal ("e.g."-qualified in the plan), per rule
// #446's content-specific-assertion discipline.
func assertAdoptionWarning(t *testing.T, warnings []string, wantPath string, wantOldStage Stage) {
	t.Helper()
	if len(warnings) == 0 {
		t.Fatal("Warnings = [], want an adoption warning")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "adopted plan file") &&
			strings.Contains(w, wantPath) &&
			strings.Contains(w, string(StageWaitingForPlanApproval)) &&
			strings.Contains(w, string(wantOldStage)) {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want one naming the adopted plan path %q, target stage %q, and old stage %q",
			warnings, wantPath, StageWaitingForPlanApproval, wantOldStage)
	}
}

func assertNoAdoptionWarning(t *testing.T, warnings []string) {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, "adopted plan file") {
			t.Errorf("Warnings = %v, want no adoption warning here", warnings)
		}
	}
}

// -- case 1: stage new + one valid plan file -> plan_approved --------------

func TestAdopt_StageNew_ValidPlanFile_AdoptsToPlanApproved(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	path := mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan --approve from stage new with a valid plan file: unexpected error: %v", err)
	}
	if out.State != string(StagePlanApproved) {
		t.Errorf("Output.State = %q, want %q", out.State, StagePlanApproved)
	}
	assertAdoptionWarning(t, out.Warnings, path, StageNew)

	reloaded, rerr := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
	if rerr != nil {
		t.Fatalf("GetArtifacts after adoption: %v", rerr)
	}
	if reloaded.PlanPath != path {
		t.Errorf("persisted State.PlanPath = %q, want %q", reloaded.PlanPath, path)
	}
	if reloaded.Stage != StagePlanApproved {
		t.Errorf("persisted State.Stage = %q, want %q", reloaded.Stage, StagePlanApproved)
	}
}

// -- case 2: stage prepared + one valid plan file -> plan_approved (the
// literal #718 repro) -------------------------------------------------------

func TestAdopt_StagePrepared_ValidPlanFile_AdoptsToPlanApproved(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	path := mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan --approve from stage prepared with a valid plan file: unexpected error: %v", err)
	}
	if out.State != string(StagePlanApproved) {
		t.Errorf("Output.State = %q, want %q", out.State, StagePlanApproved)
	}
	assertAdoptionWarning(t, out.Warnings, path, StagePrepared)

	if got := loadPersistedStage(t, stateDir, "42"); got != StagePlanApproved {
		t.Errorf("persisted stage = %q, want %q", got, StagePlanApproved)
	}
}

// -- case 3: no plan file -> unchanged ErrInvalidTransition, stage unchanged
// on disk ---------------------------------------------------------------

func TestAdopt_NoPlanFile_UnchangedErrInvalidTransition(t *testing.T) {
	repoRoot := t.TempDir() // no .plans/ dir at all
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve from prepared with no plan file: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition)", err)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none (no adoption, no no-op)", out.Warnings)
	}
	if got := loadPersistedStage(t, stateDir, "42"); got != StagePrepared {
		t.Errorf("persisted stage = %q, want unchanged %q", got, StagePrepared)
	}
}

// -- case 4: two matching plan files -> no adoption, ErrInvalidTransition --

func TestAdopt_TwoMatchingPlanFiles_NoAdoption(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing-v2")

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve with two matching plan files: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
	if got := loadPersistedStage(t, stateDir, "42"); got != StagePrepared {
		t.Errorf("persisted stage = %q, want unchanged %q", got, StagePrepared)
	}
}

// -- case 5: malformed plan (missing a required section) -> no adoption ---

func TestAdopt_MalformedPlan_MissingSection_NoAdoption(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)

	malformedBody := `
## Ticket Details
some ticket details

## Implementation Plan
do the thing

## Design Context
some design context
` // missing "## Architectural Context"
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	writePlanFile(t, repoRoot, "42", "add-thing", fields, malformedBody)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve with a malformed plan file: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
}

// -- case 6: ticketId front-matter mismatch -> no adoption -----------------

func TestAdopt_TicketIDMismatch_NoAdoption(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)

	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["ticketId"] = "43" // mismatch: file lives at .plans/42-*.md but front matter says 43
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve with a ticketId front-matter mismatch: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
}

// -- #884 Q4: legacy plan file with no ticketId at all -> no adoption -------

// TestAdopt_LegacyPlanFileNoTicketID_NoAdoption_UnifiedRule covers Q4: the
// pre-#884 exemption for a legacy plan file pre-dating the ticketId field
// (front-matter ticketId absent/0) is dropped -- it is now denied exactly
// like TestAdopt_TicketIDMismatch_NoAdoption above (unified onto the same
// identity contract), with the caller falling through to today's unchanged
// ErrInvalidTransition behavior. A ticketId line added, or a re-plan, is the
// escape hatch back to adoptable.
func TestAdopt_LegacyPlanFileNoTicketID_NoAdoption_UnifiedRule(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)

	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	delete(fields, "ticketId") // legacy plan file, pre-dating the ticketId field
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve with a legacy no-ticketId plan file: want an error, got nil (Q4: no legacy exemption)")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
}

// -- case 7: bare `plan` at new with a valid plan file -> still
// ErrNotPrepared (adoption is narrowly scoped to `plan --approve` only) -----

func TestAdopt_BarePlanAtNew_StillErrNotPrepared(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: false, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("bare plan from stage new with a valid plan file: want an error, got nil")
	}
	if !errors.Is(err, ErrNotPrepared) {
		t.Errorf("error = %v, want errors.Is(_, ErrNotPrepared) (adoption must not widen bare `plan`'s own precondition)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
}

// -- case 8: already waiting_for_plan_approval -> normal transition, no
// adoption warning -----------------------------------------------------

func TestAdopt_AlreadyWaitingForPlanApproval_NormalTransitionNoAdoptionWarning(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageWaitingForPlanApproval)
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan --approve from waiting_for_plan_approval: unexpected error: %v", err)
	}
	if out.State != string(StagePlanApproved) {
		t.Errorf("Output.State = %q, want %q", out.State, StagePlanApproved)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none for a genuine forward transition (not adoption-eligible: already at the predecessor rank)", out.Warnings)
	}
}

// -- case 9: already plan_approved -> #636 no-op warning only, no adoption
// warning -----------------------------------------------------------------

func TestAdopt_AlreadyPlanApproved_NoOpWarningOnlyNoAdoptionWarning(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePlanApproved)
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan --approve from plan_approved (no-op): unexpected error: %v", err)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly the #636 no-op warning (not adoption -- rank is at/past target, adoption never applies)", out.Warnings)
	}
	wantWarning := `already at stage "plan_approved"; plan --approve is a no-op`
	if out.Warnings[0] != wantWarning {
		t.Errorf("Warnings[0] = %q, want %q", out.Warnings[0], wantWarning)
	}
	assertNoAdoptionWarning(t, out.Warnings)
}

// -- case 10: unknown/corrupt persisted stage + valid plan file -> no
// adoption, ErrInvalidTransition (default-deny) ----------------------------

func TestAdopt_UnknownPersistedStage_NoAdoptionDefaultDeny(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	if err := saveState(path, State{SchemaVersion: CurrentSchemaVersion, ID: "42", Stage: Stage("bogus-corrupt-stage")}); err != nil {
		t.Fatalf("seed corrupt stage: %v", err)
	}
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve with an unknown persisted stage: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition) (default-deny on an unrecognized stage)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
}

// -- case 11: --state-dir set, --repo empty, cwd outside any git repo -> no
// adoption, no new error class ---------------------------------------------

func TestAdopt_RepoRootUnresolvableOutsideGitRepo_NoAdoptionNoNewErrorClass(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)

	nonRepoDir := t.TempDir() // deliberately never git-init'd
	t.Chdir(nonRepoDir)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, StateDir: stateDir}) // RepoRoot left empty on purpose
	if err == nil {
		t.Fatal("plan --approve with an unresolvable plan repo root: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition) -- a plan-repo-root resolution failure must be non-fatal and fall back to today's transition error, not a new error class", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
	if got := loadPersistedStage(t, stateDir, "42"); got != StagePrepared {
		t.Errorf("persisted stage = %q, want unchanged %q", got, StagePrepared)
	}
}

// -- case 13 (#826): gate (2) retargeted to waiting_for_input -- a persisted
// stage already at waiting_for_input must not be adopted, even with a
// perfectly valid, non-draft plan file on disk (the gate boundary itself
// moved, not just the front-matter check below) --------------------------

func TestAdopt_PersistedStageWaitingForInput_NoAdoption_GateTwoRetarget(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageWaitingForInput)
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing") // status: planned (defaultPlanFields), not a draft

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve with persisted stage waiting_for_input: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition) (gate 2 must deny adoption once the persisted stage reaches waiting_for_input)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
	if got := loadPersistedStage(t, stateDir, "42"); got != StageWaitingForInput {
		t.Errorf("persisted stage = %q, want unchanged %q", got, StageWaitingForInput)
	}
}

// -- case 14 (#826): new gate (7) -- a valid plan file whose front matter
// carries status: awaiting-input must never be adopted, even when the
// persisted stage itself was deleted/never tracked (ranks strictly below
// waiting_for_input, so gate (2) alone would let it through) ---------------

func TestAdopt_AwaitingInputDraftStatus_GateSevenDenial_StateFileDeleted(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir() // no state file at all: simulates "state file deleted"
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve with an awaiting-input draft plan and no state file: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition) (gate 7 must deny adoption of an awaiting-input draft)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
}

// TestAdopt_AwaitingInputDraftStatus_GateSevenDenial_StagePrepared covers
// the same gate-7 denial with a tracked (not deleted) prepared state, so the
// front-matter check is proven independent of gate (2)'s rank comparison.
func TestAdopt_AwaitingInputDraftStatus_GateSevenDenial_StagePrepared(t *testing.T) {
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err == nil {
		t.Fatal("plan --approve with an awaiting-input draft plan at stage prepared: want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want errors.Is(_, ErrInvalidTransition)", err)
	}
	assertNoAdoptionWarning(t, out.Warnings)
	if got := loadPersistedStage(t, stateDir, "42"); got != StagePrepared {
		t.Errorf("persisted stage = %q, want unchanged %q", got, StagePrepared)
	}
}

// -- case 12: adoption path invokes the `command` seam zero times (proves it
// is offline -- no gh/git freshness call) -----------------------------------

func TestAdopt_InvokesCommandSeamZeroTimes(t *testing.T) {
	calls := recordingCommand(t)
	repoRoot := t.TempDir()
	stateDir := t.TempDir()
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, RepoRoot: repoRoot, StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan --approve (adoption): unexpected error: %v", err)
	}
	if out.State != string(StagePlanApproved) {
		t.Errorf("Output.State = %q, want %q", out.State, StagePlanApproved)
	}
	if len(*calls) != 0 {
		t.Errorf("command seam invocations = %v, want none -- adoption must be a purely local/offline decision (no gh/git freshness re-check)", *calls)
	}
}
