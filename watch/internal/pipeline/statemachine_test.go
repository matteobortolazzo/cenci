package pipeline

// Unit tests for the pipeline state machine: the transition table and its
// preconditions (ticket #558), extended by ticket #636 for the monotonic
// no-op rule -- a total stage order, a no-op result when the persisted stage
// is already at or past a command's target, and a hard fail (never a silent
// no-op) on an unrecognized persisted stage. Table-driven per
// docs/plan-fidelity.md's state table, including the acceptance criteria's
// key guard ("execute" blocked before "plan_approved"). Sentinel errors are
// asserted via errors.Is at the package boundary per
// watch/docs/error-handling.md rule #412 -- this is an in-package ("white
// box") test file, matching
// internal/babysit's own convention (babysit_test.go is `package babysit`),
// so it exercises the unexported transition()/stageRank()/noopWarning()
// functions directly rather than only indirectly via a higher-level Run().
//
// transition() now returns a third value, noop bool, signaling that the
// persisted stage was already at or past the command's target and the call
// is a monotonic no-op (returns the *persisted* stage unchanged, nil error).
// Every call site below is updated to the 3-value signature.

import (
	"errors"
	"strings"
	"testing"
)

// -- stage ordering helper (#636 AC1) ---------------------------------------

// TestStageRank_TotalOrder locks in the documented total order (new <
// prepared < waiting_for_plan_approval < plan_approved < executed <
// reviewed < finalized): each stage's rank must be strictly greater than
// the previous one's.
func TestStageRank_TotalOrder(t *testing.T) {
	order := []Stage{
		StageNew,
		StagePrepared,
		StageWaitingForInput,
		StageWaitingForPlanApproval,
		StagePlanApproved,
		StageExecuted,
		StageReviewed,
		StageFinalized,
	}
	prev := -1
	for _, s := range order {
		rank, ok := stageRank(s)
		if !ok {
			t.Fatalf("stageRank(%s) ok = false, want true", s)
		}
		if rank <= prev {
			t.Errorf("stageRank(%s) = %d, want strictly greater than the previous stage's rank %d", s, rank, prev)
		}
		prev = rank
	}
}

// TestStageRank_UnknownStage_NotOk locks in the default-deny requirement
// (watch/docs/go-gotchas.md #598, watch/docs/error-handling.md #628): an
// unrecognized stage must never rank
// alongside the known total order, which is what makes it possible for
// transition()/ApplyLabelTransition to reject it explicitly rather than
// having it silently compare as "before" or "at or past" some target.
func TestStageRank_UnknownStage_NotOk(t *testing.T) {
	if rank, ok := stageRank(Stage("bogus")); ok {
		t.Errorf("stageRank(bogus) = (%d, true), want ok = false for an unrecognized stage", rank)
	}
}

// TestIsKnownStage locks in IsKnownStage's contract (ticket #732): every
// member of stageOrder must report true, and any unrecognized value --
// including the empty string and a wrong-case near-match of a real stage --
// must report false (default-deny per watch/docs/go-gotchas.md #598,
// watch/docs/error-handling.md #628).
func TestIsKnownStage(t *testing.T) {
	cases := []struct {
		name  string
		stage Stage
		want  bool
	}{
		{"new", StageNew, true},
		{"prepared", StagePrepared, true},
		{"waiting_for_input", StageWaitingForInput, true},
		{"waiting_for_plan_approval", StageWaitingForPlanApproval, true},
		{"plan_approved", StagePlanApproved, true},
		{"executed", StageExecuted, true},
		{"reviewed", StageReviewed, true},
		{"finalized", StageFinalized, true},
		{"empty string", Stage(""), false},
		{"bogus", Stage("bogus"), false},
		{"wrong case", Stage("FINALIZED"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsKnownStage(c.stage)
			if got != c.want {
				t.Errorf("IsKnownStage(%q) = %v, want %v", c.stage, got, c.want)
			}
		})
	}
}

// -- valid transitions (from < target: real forward transitions, never a no-op) --

func TestTransition_ValidSequence(t *testing.T) {
	cases := []struct {
		name    string
		from    Stage
		command string
		approve bool
		want    Stage
	}{
		{"prepare from new", StageNew, "prepare", false, StagePrepared},
		{"plan from prepared", StagePrepared, "plan", false, StageWaitingForPlanApproval},
		{"plan --approve from waiting_for_plan_approval", StageWaitingForPlanApproval, "plan", true, StagePlanApproved},
		{"execute from plan_approved", StagePlanApproved, "execute", false, StageExecuted},
		{"review from executed", StageExecuted, "review", false, StageReviewed},
		{"finalize from reviewed", StageReviewed, "finalize", false, StageFinalized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, noop, err := transition(c.from, c.command, c.approve)
			if err != nil {
				t.Fatalf("transition(%s, %q, approve=%v) unexpected error: %v", c.from, c.command, c.approve, err)
			}
			if got != c.want {
				t.Errorf("transition(%s, %q, approve=%v) = %s, want %s", c.from, c.command, c.approve, got, c.want)
			}
			if noop {
				t.Errorf("transition(%s, %q, approve=%v) noop = true, want false (a real forward transition, not a no-op)", c.from, c.command, c.approve)
			}
		})
	}
}

// -- await-input (#826): new stage, its own command, dual-predecessor bare
// `plan` -------------------------------------------------------------------

// TestTransition_AwaitInput_FromPrepared_Succeeds locks in the new
// `await-input` command's real forward transition: prepared -> waiting_for_input.
func TestTransition_AwaitInput_FromPrepared_Succeeds(t *testing.T) {
	got, noop, err := transition(StagePrepared, "await-input", false)
	if err != nil {
		t.Fatalf("transition(prepared, await-input) unexpected error: %v", err)
	}
	if noop {
		t.Error("transition(prepared, await-input) noop = true, want false (a real forward transition)")
	}
	if got != StageWaitingForInput {
		t.Errorf("transition(prepared, await-input) = %s, want %s", got, StageWaitingForInput)
	}
}

// TestTransition_AwaitInput_BeforePrepared_ErrNotPrepared locks in
// await-input's sentinel: it reuses ErrNotPrepared (same failure class as
// bare `plan`), per the plan's Assumptions.
func TestTransition_AwaitInput_BeforePrepared_ErrNotPrepared(t *testing.T) {
	got, noop, err := transition(StageNew, "await-input", false)
	if err == nil {
		t.Fatalf("transition(new, await-input) = %s, noop=%v, <nil>, want ErrNotPrepared", got, noop)
	}
	if !errors.Is(err, ErrNotPrepared) {
		t.Errorf("transition(new, await-input) error = %v, want errors.Is(_, ErrNotPrepared)", err)
	}
	if noop {
		t.Error("transition(new, await-input) noop = true, want false")
	}
}

// TestTransition_AwaitInput_NoOp_WhenAtOrPastTarget locks in the monotonic
// no-op rule (#636) for the new command: re-escalating (or escalating past
// the point of escalation) must never rewind and must never hard-fail.
func TestTransition_AwaitInput_NoOp_WhenAtOrPastTarget(t *testing.T) {
	cases := []Stage{
		StageWaitingForInput,
		StageWaitingForPlanApproval,
		StagePlanApproved,
		StageExecuted,
		StageReviewed,
		StageFinalized,
	}
	for _, from := range cases {
		t.Run(string(from), func(t *testing.T) {
			got, noop, err := transition(from, "await-input", false)
			if err != nil {
				t.Fatalf("transition(%s, await-input) unexpected error: %v", from, err)
			}
			if !noop {
				t.Errorf("transition(%s, await-input) noop = false, want true", from)
			}
			if got != from {
				t.Errorf("transition(%s, await-input) = %s, want the persisted stage %s unchanged", from, got, from)
			}
		})
	}
}

// TestTransition_BarePlan_FromWaitingForInput_ResumesToWaitingForPlanApproval
// locks in the dual-predecessor rule: bare `plan` must accept
// waiting_for_input as a predecessor too (the escalation resume path), not
// just prepared, and must land at the *same* target waiting_for_plan_approval
// as the prepared path -- never a rewind, never a distinct target.
func TestTransition_BarePlan_FromWaitingForInput_ResumesToWaitingForPlanApproval(t *testing.T) {
	got, noop, err := transition(StageWaitingForInput, "plan", false)
	if err != nil {
		t.Fatalf("transition(waiting_for_input, plan) unexpected error: %v", err)
	}
	if noop {
		t.Error("transition(waiting_for_input, plan) noop = true, want false (a real forward transition, the escalation resume path)")
	}
	if got != StageWaitingForPlanApproval {
		t.Errorf("transition(waiting_for_input, plan) = %s, want %s", got, StageWaitingForPlanApproval)
	}
}

// TestTransition_BarePlan_FromWaitingForInput_ReEscalation_MonotonicNoOp
// covers the plan's "re-escalation is a monotonic no-op" requirement from
// the opposite direction: once bare `plan` has already advanced a ticket to
// waiting_for_plan_approval, `await-input` called again must not rewind it
// back to waiting_for_input -- it must land as a no-op at the persisted
// (later) stage.
func TestTransition_BarePlan_FromWaitingForInput_ReEscalation_MonotonicNoOp(t *testing.T) {
	got, noop, err := transition(StageWaitingForPlanApproval, "await-input", false)
	if err != nil {
		t.Fatalf("transition(waiting_for_plan_approval, await-input) unexpected error: %v", err)
	}
	if !noop {
		t.Error("transition(waiting_for_plan_approval, await-input) noop = false, want true (re-escalation must never rewind)")
	}
	if got != StageWaitingForPlanApproval {
		t.Errorf("transition(waiting_for_plan_approval, await-input) = %s, want unchanged %s", got, StageWaitingForPlanApproval)
	}
}

// TestCommandTarget_Plan_PredecessorSet directly asserts the "predecessor
// set" data fact commandTarget now owns for bare `plan`: exactly
// {prepared, waiting_for_input}, per the plan's chosen alternative
// ("commandTarget returns a predecessor set").
func TestCommandTarget_Plan_PredecessorSet(t *testing.T) {
	target, predecessors, sentinel, ok := commandTarget("plan", false)
	if !ok {
		t.Fatal("commandTarget(plan, false) ok = false, want true")
	}
	if target != StageWaitingForPlanApproval {
		t.Errorf("commandTarget(plan, false) target = %s, want %s", target, StageWaitingForPlanApproval)
	}
	if !errors.Is(sentinel, ErrNotPrepared) {
		t.Errorf("commandTarget(plan, false) sentinel = %v, want ErrNotPrepared", sentinel)
	}
	want := map[Stage]bool{StagePrepared: true, StageWaitingForInput: true}
	if len(predecessors) != len(want) {
		t.Fatalf("commandTarget(plan, false) predecessors = %v, want exactly %v", predecessors, want)
	}
	for _, p := range predecessors {
		if !want[p] {
			t.Errorf("commandTarget(plan, false) predecessors = %v, unexpected member %s", predecessors, p)
		}
	}
}

// -- monotonic no-op table (#636 AC2/AC3): from every stage >= target -------

// TestTransition_NoOp_WhenAtOrPastTarget is the plan's "No-op table":
// for every command, every persisted stage at or past that command's target
// returns the *persisted* stage unchanged (never the target), noop == true,
// and a nil error.
func TestTransition_NoOp_WhenAtOrPastTarget(t *testing.T) {
	cases := []struct {
		name    string
		from    Stage
		command string
		approve bool
	}{
		// prepare: target prepared
		{"prepare at target (prepared)", StagePrepared, "prepare", false},
		{"prepare past target (waiting_for_plan_approval)", StageWaitingForPlanApproval, "prepare", false},
		{"prepare past target (plan_approved)", StagePlanApproved, "prepare", false},
		{"prepare past target (executed)", StageExecuted, "prepare", false},
		{"prepare past target (reviewed)", StageReviewed, "prepare", false},
		{"prepare past target (finalized)", StageFinalized, "prepare", false},

		// plan (bare): target waiting_for_plan_approval
		{"plan at target (waiting_for_plan_approval)", StageWaitingForPlanApproval, "plan", false},
		{"plan past target (plan_approved)", StagePlanApproved, "plan", false},
		{"plan past target (executed)", StageExecuted, "plan", false},
		{"plan past target (reviewed)", StageReviewed, "plan", false},
		{"plan past target (finalized)", StageFinalized, "plan", false},

		// plan --approve: target plan_approved
		{"plan --approve at target (plan_approved)", StagePlanApproved, "plan", true},
		{"plan --approve past target (executed)", StageExecuted, "plan", true},
		{"plan --approve past target (reviewed)", StageReviewed, "plan", true},
		{"plan --approve past target (finalized)", StageFinalized, "plan", true},

		// execute: target executed
		{"execute at target (executed)", StageExecuted, "execute", false},
		{"execute past target (reviewed)", StageReviewed, "execute", false},
		{"execute past target (finalized)", StageFinalized, "execute", false},

		// review: target reviewed
		{"review at target (reviewed)", StageReviewed, "review", false},
		{"review past target (finalized)", StageFinalized, "review", false},

		// finalize: target finalized -- finalized is the maximum rank, so
		// "at target" is the only case; there is no stage past it.
		{"finalize at target (finalized)", StageFinalized, "finalize", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, noop, err := transition(c.from, c.command, c.approve)
			if err != nil {
				t.Fatalf("transition(%s, %q, approve=%v) unexpected error: %v", c.from, c.command, c.approve, err)
			}
			if !noop {
				t.Errorf("transition(%s, %q, approve=%v) noop = false, want true", c.from, c.command, c.approve)
			}
			if got != c.from {
				t.Errorf("transition(%s, %q, approve=%v) = %s, want the persisted stage %s unchanged (a no-op never rewinds and never jumps to the target)", c.from, c.command, c.approve, got, c.from)
			}
		})
	}
}

// -- invalid transitions (the plan's "Invalid-from example (tested)" column) --

func TestTransition_InvalidFromExamplesReturnSentinelErrors(t *testing.T) {
	cases := []struct {
		name    string
		from    Stage
		command string
		approve bool
		wantErr error
	}{
		{"plan before prepare", StageNew, "plan", false, ErrNotPrepared},
		{"plan --approve before plan", StagePrepared, "plan", true, ErrInvalidTransition},
		{"await-input before prepare", StageNew, "await-input", false, ErrNotPrepared},
		// AC's key guard: execute must not be reachable before plan_approved,
		// from either intermediate stage short of it.
		{"execute at prepared (AC key guard)", StagePrepared, "execute", false, ErrPlanNotApproved},
		{"execute at waiting_for_plan_approval (AC key guard)", StageWaitingForPlanApproval, "execute", false, ErrPlanNotApproved},
		{"execute at new (AC key guard)", StageNew, "execute", false, ErrPlanNotApproved},
		{"review before execute", StagePlanApproved, "review", false, ErrInvalidTransition},
		{"finalize before review", StageExecuted, "finalize", false, ErrInvalidTransition},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, noop, err := transition(c.from, c.command, c.approve)
			if err == nil {
				t.Fatalf("transition(%s, %q, approve=%v) = %s, noop=%v, <nil>, want a sentinel error", c.from, c.command, c.approve, got, noop)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("transition(%s, %q, approve=%v) error = %v, want errors.Is(_, %v)", c.from, c.command, c.approve, err, c.wantErr)
			}
			if noop {
				t.Errorf("transition(%s, %q, approve=%v) noop = true, want false (a too-early call is a hard failure, never a no-op)", c.from, c.command, c.approve)
			}
		})
	}
}

// TestTransition_ExecuteNeverSucceedsWithoutApproval directly locks in the
// acceptance criteria's key guard as its own standalone assertion (not just
// buried in the table above): for every stage short of plan_approved,
// "execute" must fail with ErrPlanNotApproved, never silently advance and
// never silently no-op.
func TestTransition_ExecuteNeverSucceedsWithoutApproval(t *testing.T) {
	for _, from := range []Stage{StageNew, StagePrepared, StageWaitingForPlanApproval} {
		got, noop, err := transition(from, "execute", false)
		if err == nil {
			t.Errorf("transition(%s, execute) = %s, noop=%v, <nil>, want ErrPlanNotApproved", from, got, noop)
			continue
		}
		if !errors.Is(err, ErrPlanNotApproved) {
			t.Errorf("transition(%s, execute) error = %v, want errors.Is(_, ErrPlanNotApproved)", from, err)
		}
		if noop {
			t.Errorf("transition(%s, execute) noop = true, want false", from)
		}
	}
}

// -- unknown persisted stage (#636): hard fail, never a silent no-op --------

// TestTransition_UnknownPersistedStage_HardFailsForEveryCommand locks in the
// default-deny requirement (watch/docs/go-gotchas.md #598,
// watch/docs/error-handling.md #628): a corrupt/forward-incompatible
// persisted stage value must never rank as "at or past" any
// command's target (which would silently no-op every command instead of
// failing), and must be classified as ErrInvalidTransition specifically
// (not one of the other sentinels, which would be misleading for a
// corrupt-state condition rather than a genuine "too early" one).
func TestTransition_UnknownPersistedStage_HardFailsForEveryCommand(t *testing.T) {
	bogus := Stage("bogus")
	cases := []struct {
		command string
		approve bool
	}{
		{"prepare", false},
		{"await-input", false},
		{"plan", false},
		{"plan", true},
		{"execute", false},
		{"review", false},
		{"finalize", false},
	}
	for _, c := range cases {
		t.Run(c.command+"_approve_"+boolLabel(c.approve), func(t *testing.T) {
			got, noop, err := transition(bogus, c.command, c.approve)
			if err == nil {
				t.Fatalf("transition(bogus, %q, approve=%v) = %s, noop=%v, <nil>, want ErrInvalidTransition", c.command, c.approve, got, noop)
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("transition(bogus, %q, approve=%v) error = %v, want errors.Is(_, ErrInvalidTransition)", c.command, c.approve, err)
			}
			if !strings.Contains(err.Error(), "unknown persisted stage") {
				t.Errorf("transition(bogus, %q, approve=%v) error = %q, want it to contain %q", c.command, c.approve, err.Error(), "unknown persisted stage")
			}
			if noop {
				t.Errorf("transition(bogus, %q, approve=%v) noop = true, want false (an unknown stage must never be treated as at-or-past a target)", c.command, c.approve)
			}
		})
	}
}

func boolLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// -- no-op warning text (#636 AC5) ------------------------------------------

// TestNoopWarning_ExactText locks in the AC's exact wording, including the
// `plan --approve` rendering for the approve variant.
func TestNoopWarning_ExactText(t *testing.T) {
	cases := []struct {
		name        string
		current     Stage
		command     string
		approve     bool
		wantWarning string
	}{
		{"plan --approve no-op", StageExecuted, "plan", true, `already at stage "executed"; plan --approve is a no-op`},
		{"bare plan no-op", StageWaitingForPlanApproval, "plan", false, `already at stage "waiting_for_plan_approval"; plan is a no-op`},
		{"prepare no-op", StagePrepared, "prepare", false, `already at stage "prepared"; prepare is a no-op`},
		{"execute no-op", StageExecuted, "execute", false, `already at stage "executed"; execute is a no-op`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := noopWarning(c.current, c.command, c.approve)
			if got != c.wantWarning {
				t.Errorf("noopWarning(%s, %q, approve=%v) = %q, want %q", c.current, c.command, c.approve, got, c.wantWarning)
			}
		})
	}
}

// -- sentinel identity (#412: a direct unit test at the package boundary) --

func TestSentinelErrors_AreDistinctAndDetectableViaErrorsIs(t *testing.T) {
	sentinels := map[string]error{
		"ErrInvalidTransition": ErrInvalidTransition,
		"ErrNotPrepared":       ErrNotPrepared,
		"ErrPlanNotApproved":   ErrPlanNotApproved,
		"ErrTicketNotFound":    ErrTicketNotFound,
	}
	for name, sentinel := range sentinels {
		if sentinel == nil {
			t.Errorf("%s must not be nil", name)
			continue
		}
		if !errors.Is(sentinel, sentinel) {
			t.Errorf("%s must satisfy errors.Is against itself", name)
		}
	}
	// Every sentinel must be distinguishable from every other one: a
	// regression that collapsed two of these into the same var would let
	// errors.Is silently misclassify a failure class (rule #446/#412).
	seen := map[error]string{}
	for name, sentinel := range sentinels {
		if other, dup := seen[sentinel]; dup {
			t.Errorf("%s and %s must be distinct sentinel values, got the same error", name, other)
		}
		seen[sentinel] = name
	}
}

// -- next_actions never nil (structured output contract) ----------------

// TestNextActionsFor_AlwaysNonNil locks in the contract's "all four arrays
// are always present (non-nil, [] when empty)" rule at the state-machine
// level: nextActionsFor must never return a nil slice, even for the
// terminal finalized stage where there is nothing left to do.
func TestNextActionsFor_AlwaysNonNil(t *testing.T) {
	for _, stage := range []Stage{
		StageNew,
		StagePrepared,
		StageWaitingForInput,
		StageWaitingForPlanApproval,
		StagePlanApproved,
		StageExecuted,
		StageReviewed,
		StageFinalized,
	} {
		got := nextActionsFor(stage)
		if got == nil {
			t.Errorf("nextActionsFor(%s) = nil, want a non-nil slice (possibly empty)", stage)
		}
	}
}

// TestNextActionsFor_WaitingForInput_MentionsResumeViaBarePlan locks in
// guidance content for the new escalation stage: it must point at the
// resume path (bare `plan`, per the dual-predecessor rule), not at
// `await-input` again (which would just no-op).
func TestNextActionsFor_WaitingForInput_MentionsResumeViaBarePlan(t *testing.T) {
	got := nextActionsFor(StageWaitingForInput)
	if len(got) == 0 {
		t.Fatal("nextActionsFor(StageWaitingForInput) = [], want guidance")
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "cenci pipeline plan") {
		t.Errorf("nextActionsFor(StageWaitingForInput) = %v, want it to mention `cenci pipeline plan <id>` (the resume path)", got)
	}
}

// TestNextActionsFor_FinalizedHasNoFurtherAction covers the one stage where
// there genuinely is nothing left to do: the returned slice must be
// non-nil but empty, not carry stale guidance from an earlier stage.
func TestNextActionsFor_FinalizedHasNoFurtherAction(t *testing.T) {
	got := nextActionsFor(StageFinalized)
	if got == nil {
		t.Fatal("nextActionsFor(StageFinalized) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("nextActionsFor(StageFinalized) = %v, want empty (nothing left to do)", got)
	}
}
