package pipeline

// Unit tests for the pipeline state machine: the transition table and its
// preconditions (ticket #558). Table-driven per docs/plan-fidelity.md's
// state table, including the acceptance criteria's key guard ("execute"
// blocked before "plan_approved"). Sentinel errors are asserted via
// errors.Is at the package boundary per watch/AGENTS.md rule #412 — this is
// an in-package ("white box") test file, matching internal/babysit's own
// convention (babysit_test.go is `package babysit`), so it exercises the
// unexported transition() function directly rather than only indirectly via
// a higher-level Run().

import (
	"errors"
	"testing"
)

// -- valid transitions --------------------------------------------------

func TestTransition_ValidSequence(t *testing.T) {
	cases := []struct {
		name    string
		from    Stage
		command string
		approve bool
		want    Stage
	}{
		{"prepare from new", StageNew, "prepare", false, StagePrepared},
		{"prepare is idempotent once already prepared", StagePrepared, "prepare", false, StagePrepared},
		{"plan from prepared", StagePrepared, "plan", false, StageWaitingForPlanApproval},
		{"plan --approve from waiting_for_plan_approval", StageWaitingForPlanApproval, "plan", true, StagePlanApproved},
		{"execute from plan_approved", StagePlanApproved, "execute", false, StageExecuted},
		{"review from executed", StageExecuted, "review", false, StageReviewed},
		{"finalize from reviewed", StageReviewed, "finalize", false, StageFinalized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := transition(c.from, c.command, c.approve)
			if err != nil {
				t.Fatalf("transition(%s, %q, approve=%v) unexpected error: %v", c.from, c.command, c.approve, err)
			}
			if got != c.want {
				t.Errorf("transition(%s, %q, approve=%v) = %s, want %s", c.from, c.command, c.approve, got, c.want)
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
			got, err := transition(c.from, c.command, c.approve)
			if err == nil {
				t.Fatalf("transition(%s, %q, approve=%v) = %s, <nil>, want a sentinel error", c.from, c.command, c.approve, got)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("transition(%s, %q, approve=%v) error = %v, want errors.Is(_, %v)", c.from, c.command, c.approve, err, c.wantErr)
			}
		})
	}
}

// TestTransition_ExecuteNeverSucceedsWithoutApproval directly locks in the
// acceptance criteria's key guard as its own standalone assertion (not just
// buried in the table above): for every stage short of plan_approved,
// "execute" must fail with ErrPlanNotApproved, never silently advance.
func TestTransition_ExecuteNeverSucceedsWithoutApproval(t *testing.T) {
	for _, from := range []Stage{StageNew, StagePrepared, StageWaitingForPlanApproval} {
		got, err := transition(from, "execute", false)
		if err == nil {
			t.Errorf("transition(%s, execute) = %s, <nil>, want ErrPlanNotApproved", from, got)
			continue
		}
		if !errors.Is(err, ErrPlanNotApproved) {
			t.Errorf("transition(%s, execute) error = %v, want errors.Is(_, ErrPlanNotApproved)", from, err)
		}
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
