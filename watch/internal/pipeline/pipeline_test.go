package pipeline

// Integration tests for pipeline.Run (ticket #636): the first direct tests
// of Run -- until this ticket it was only exercised indirectly through
// pipeline_cmd_test.go's CLI subprocess harness. Covers the monotonic no-op
// rule's warnings plumbing, the never-rewind guarantee, and the three
// resume sequences the ticket exists to unblock (plan-file resume,
// interrupted resume, replan-over-executed) plus the stage-`new` sequence
// from Q&A #1 that proves the flow side's "don't skip bare `plan`" decision.
//
// Seeded via Opts.StateDir (mirrors labels_test.go's own StateDir contract),
// reusing labels_test.go's package-level mustSeedState/fakeGh helpers --
// both already package-level and reusable from this file (Architectural
// Context: "command seam stubbing via fakeGh.install() ... and state
// seeding via mustSeedState ... both package-level and reusable from the
// new pipeline_test.go").

import (
	"path/filepath"
	"testing"
)

// -- warnings plumbing (#636 AC5) --------------------------------------------

// TestRun_NoOp_SurfacesWarningWithEmptyErrorsExit0Contract proves Run wires
// the state machine's no-op signal into Output.Warnings (a contract field
// that, before this ticket, no code path ever populated): a nil error,
// empty Output.Errors, exactly one warning with the AC's exact text, and
// next_actions derived from the persisted (unchanged) stage.
func TestRun_NoOp_SurfacesWarningWithEmptyErrorsExit0Contract(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageExecuted)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, StateDir: stateDir})
	if err != nil {
		t.Fatalf("Run(plan --approve) no-op from executed: unexpected error: %v", err)
	}
	if out.State != string(StageExecuted) {
		t.Errorf("Output.State = %q, want the unchanged persisted stage %q", out.State, StageExecuted)
	}
	if len(out.Errors) != 0 {
		t.Errorf("Output.Errors = %v, want none for a no-op", out.Errors)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("Output.Warnings = %v, want exactly one no-op warning", out.Warnings)
	}
	wantWarning := `already at stage "executed"; plan --approve is a no-op`
	if out.Warnings[0] != wantWarning {
		t.Errorf("Output.Warnings[0] = %q, want %q", out.Warnings[0], wantWarning)
	}
	wantNext := nextActionsFor(StageExecuted)
	if len(out.NextActions) != len(wantNext) {
		t.Fatalf("Output.NextActions = %v, want the persisted stage's guidance %v", out.NextActions, wantNext)
	}
	for i := range wantNext {
		if out.NextActions[i] != wantNext[i] {
			t.Errorf("Output.NextActions[%d] = %q, want %q", i, out.NextActions[i], wantNext[i])
		}
	}
}

// TestRun_RealTransition_StillEmitsNoWarnings guards against a regression
// that always appends a no-op warning regardless of whether the call was
// actually a no-op: a genuine forward transition must still report
// Output.Warnings as empty, matching every existing pre-#636 assertion on
// stage commands' warnings field.
func TestRun_RealTransition_StillEmitsNoWarnings(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)

	out, err := Run(Opts{Stage: "plan", ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Run(plan) from prepared: unexpected error: %v", err)
	}
	if out.State != string(StageWaitingForPlanApproval) {
		t.Errorf("Output.State = %q, want %q (a real transition)", out.State, StageWaitingForPlanApproval)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("Output.Warnings = %v, want none for a real (non-no-op) transition", out.Warnings)
	}
}

// -- never rewind (#636 AC3) --------------------------------------------------

// TestRun_NoOp_NeverRewindsPersistedState is the plan's "Never rewind" case:
// seed executed, run plan --approve (which targets plan_approved, behind
// executed), and confirm the on-disk state file's stage field is
// byte-identical to its pre-call value -- the no-op must never mutate the
// persisted stage, even though Run still refreshes/re-saves the record.
func TestRun_NoOp_NeverRewindsPersistedState(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageExecuted)
	statePath := filepath.Join(stateDir, "42.json")

	beforeState, err := loadState(statePath)
	if err != nil {
		t.Fatalf("decode seeded state file: %v", err)
	}
	if beforeState.Stage != StageExecuted {
		t.Fatalf("seeded stage = %q, want %q (test setup sanity check)", beforeState.Stage, StageExecuted)
	}

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, StateDir: stateDir})
	if err != nil {
		t.Fatalf("Run(plan --approve) no-op from executed: unexpected error: %v", err)
	}

	afterState, err := loadState(statePath)
	if err != nil {
		t.Fatalf("decode state file after no-op: %v", err)
	}
	if afterState.Stage != beforeState.Stage {
		t.Errorf("persisted stage after no-op = %q, want byte-identical to the pre-call value %q (never rewound)", afterState.Stage, beforeState.Stage)
	}
	if out.State != string(StageExecuted) {
		t.Errorf("Output.State = %q, want %q", out.State, StageExecuted)
	}
	if len(out.Errors) != 0 {
		t.Errorf("Output.Errors = %v, want empty", out.Errors)
	}
	if len(out.Warnings) != 1 {
		t.Errorf("Output.Warnings = %v, want length 1", out.Warnings)
	}
	wantNext := nextActionsFor(StageExecuted)
	if len(out.NextActions) != len(wantNext) {
		t.Fatalf("Output.NextActions = %v, want executed's guidance %v", out.NextActions, wantNext)
	}
	for i := range wantNext {
		if out.NextActions[i] != wantNext[i] {
			t.Errorf("Output.NextActions[%d] = %q, want %q", i, out.NextActions[i], wantNext[i])
		}
	}
}

// -- AC resume sequences (#636) ----------------------------------------------

// TestResumeSequence_PlanFileResume_FromWaitingForPlanApproval is the
// ticket's reported repro, now fixed end to end: a session resumes from a
// persisted waiting_for_plan_approval state file. `label --transition
// working` (past-minimum today, since the minimum is `prepared`), `plan
// --approve`, and `execute` must all succeed with empty errors[].
func TestResumeSequence_PlanFileResume_FromWaitingForPlanApproval(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageWaitingForPlanApproval)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("label --transition working: %v", err)
	}

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan --approve: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("plan --approve: Output.Errors = %v, want empty", out.Errors)
	}
	if out.State != string(StagePlanApproved) {
		t.Errorf("plan --approve: Output.State = %q, want %q", out.State, StagePlanApproved)
	}

	out, err = Run(Opts{Stage: "execute", ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("execute: Output.Errors = %v, want empty", out.Errors)
	}
	if out.State != string(StageExecuted) {
		t.Errorf("execute: Output.State = %q, want %q", out.State, StageExecuted)
	}
}

// TestResumeSequence_InterruptedResume_FromExecuted covers a resume after an
// interrupted Phase 4: `plan --approve` and `execute` must both no-op (the
// persisted stage is already past both targets) with warnings and empty
// errors[], rather than dead-ending on the old exact-predecessor guard.
func TestResumeSequence_InterruptedResume_FromExecuted(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageExecuted)

	out, err := Run(Opts{Stage: "plan", ID: "42", Approve: true, StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan --approve (interrupted resume): %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("plan --approve: Output.Errors = %v, want empty", out.Errors)
	}
	if len(out.Warnings) != 1 {
		t.Errorf("plan --approve: Output.Warnings = %v, want exactly one no-op warning", out.Warnings)
	}
	if out.State != string(StageExecuted) {
		t.Errorf("plan --approve: Output.State = %q, want unchanged %q", out.State, StageExecuted)
	}

	out, err = Run(Opts{Stage: "execute", ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("execute (interrupted resume): %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("execute: Output.Errors = %v, want empty", out.Errors)
	}
	if len(out.Warnings) != 1 {
		t.Errorf("execute: Output.Warnings = %v, want exactly one no-op warning", out.Warnings)
	}
	if out.State != string(StageExecuted) {
		t.Errorf("execute: Output.State = %q, want unchanged %q", out.State, StageExecuted)
	}
}

// TestResumeSequence_ReplanOverExecuted covers a re-plan over an
// already-executed ticket: `label --transition working` (past-minimum),
// bare `plan` (a no-op, since executed is already past waiting_for_plan_
// approval), and `label --transition planned` (past-minimum) must all
// succeed.
func TestResumeSequence_ReplanOverExecuted(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageExecuted)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("label --transition working: %v", err)
	}

	out, err := Run(Opts{Stage: "plan", ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan (replan over executed): %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("plan: Output.Errors = %v, want empty", out.Errors)
	}

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "planned"}); err != nil {
		t.Fatalf("label --transition planned: %v", err)
	}
}

// TestResumeSequence_StageNew_PrepareThenFullSequence is the Q&A #1 case: a
// resume against a MISSING state file (stage new -- .cenci/pipeline/ is
// gitignored, or the plan file predates the pipeline CLI). `prepare` (a
// real transition, one `gh issue view`), `label --transition working` (at
// minimum), bare `plan` (prepared -> waiting_for_plan_approval, a real
// transition -- this is the assertion that proves the "don't skip plan"
// flow-side decision), `plan --approve`, and `execute` must all succeed
// with empty errors[].
func TestResumeSequence_StageNew_PrepareThenFullSequence(t *testing.T) {
	stateDir := t.TempDir()
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.install()

	out, err := Run(Opts{Stage: "prepare", ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("prepare from stage new: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("prepare: Output.Errors = %v, want empty", out.Errors)
	}
	if out.State != string(StagePrepared) {
		t.Errorf("prepare: Output.State = %q, want %q", out.State, StagePrepared)
	}

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("label --transition working: %v", err)
	}

	out, err = Run(Opts{Stage: "plan", ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan (bare) from prepared: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("plan: Output.Errors = %v, want empty", out.Errors)
	}
	if out.State != string(StageWaitingForPlanApproval) {
		t.Errorf("plan: Output.State = %q, want %q (a real transition, not skipped)", out.State, StageWaitingForPlanApproval)
	}

	out, err = Run(Opts{Stage: "plan", ID: "42", Approve: true, StateDir: stateDir})
	if err != nil {
		t.Fatalf("plan --approve: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("plan --approve: Output.Errors = %v, want empty", out.Errors)
	}
	if out.State != string(StagePlanApproved) {
		t.Errorf("plan --approve: Output.State = %q, want %q", out.State, StagePlanApproved)
	}

	out, err = Run(Opts{Stage: "execute", ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("execute: Output.Errors = %v, want empty", out.Errors)
	}
	if out.State != string(StageExecuted) {
		t.Errorf("execute: Output.State = %q, want %q", out.State, StageExecuted)
	}
}
