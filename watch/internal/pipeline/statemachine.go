package pipeline

// State machine for `cenci pipeline <stage> <id>` (ticket #558): the ordered
// transition table, its preconditions, per-stage next_actions, and the
// sentinel errors returned when a transition is invalid.

import (
	"errors"
	"fmt"
)

// Stage is a pipeline run's persisted state. The zero value is never used
// directly; StageNew represents "no state file yet".
type Stage string

const (
	StageNew                    Stage = "new"
	StagePrepared               Stage = "prepared"
	StageWaitingForInput        Stage = "waiting_for_input"
	StageWaitingForPlanApproval Stage = "waiting_for_plan_approval"
	StagePlanApproved           Stage = "plan_approved"
	StageExecuted               Stage = "executed"
	StageReviewed               Stage = "reviewed"
	StageFinalized              Stage = "finalized"
)

// Sentinel errors, detectable via errors.Is at the package boundary (rule
// #412). Each must remain a distinct value so callers can distinguish
// failure classes (rule #446).
var (
	ErrInvalidTransition = errors.New("invalid pipeline transition")
	ErrNotPrepared       = errors.New("ticket not prepared")
	ErrPlanNotApproved   = errors.New("plan not approved")
	ErrTicketNotFound    = errors.New("ticket not found")
)

// stageOrder is the pipeline's total order (ticket #636): each command's
// target is monotonic against this order, and a command whose persisted
// stage is already at or past its target is a no-op rather than a hard
// failure.
var stageOrder = []Stage{
	StageNew,
	StagePrepared,
	StageWaitingForInput,
	StageWaitingForPlanApproval,
	StagePlanApproved,
	StageExecuted,
	StageReviewed,
	StageFinalized,
}

// stageRank reports stage's position in stageOrder. The second return value
// is false for any value not in stageOrder (default-deny per
// watch/docs/go-gotchas.md #598, watch/docs/error-handling.md #628): an
// unrecognized stage must never rank alongside the known total order, so it
// can never compare as "at or past" some target.
func stageRank(stage Stage) (int, bool) {
	for i, s := range stageOrder {
		if s == stage {
			return i, true
		}
	}
	return 0, false
}

// commandTarget returns command's target stage, its required immediate
// predecessor SET (the "too early" boundary -- from must be a member for the
// transition to proceed), and the sentinel error returned when invoked
// before any of them. approve selects between "plan" (bare, target
// waiting_for_plan_approval) and "plan --approve" (target plan_approved).
// ok is false for an unrecognized command.
//
// Every command has exactly one predecessor except bare `plan`, whose
// predecessor set is {prepared, waiting_for_input} (ticket #826): a ticket
// that was never escalated reaches waiting_for_plan_approval straight from
// prepared, and an escalated ticket resumes into the same target from
// waiting_for_input once the human answers -- the "chosen alternative"
// documented in the ticket's plan (commandTarget owns the predecessor set as
// a data fact, rather than transition special-casing the resume path).
// commandTarget is called only from transition -- this signature change is
// contained entirely to this file.
func commandTarget(command string, approve bool) (target Stage, predecessors []Stage, sentinel error, ok bool) {
	switch command {
	case "prepare":
		return StagePrepared, []Stage{StageNew}, ErrInvalidTransition, true
	case "await-input":
		// #826: escalation only ever runs after prepare, mirroring bare
		// `plan`'s own "too early" sentinel (ErrNotPrepared) -- no new
		// sentinel, no new CENCI-* error code, per the plan's Assumptions.
		return StageWaitingForInput, []Stage{StagePrepared}, ErrNotPrepared, true
	case "plan":
		if approve {
			return StagePlanApproved, []Stage{StageWaitingForPlanApproval}, ErrInvalidTransition, true
		}
		return StageWaitingForPlanApproval, []Stage{StagePrepared, StageWaitingForInput}, ErrNotPrepared, true
	case "execute":
		return StageExecuted, []Stage{StagePlanApproved}, ErrPlanNotApproved, true
	case "review":
		return StageReviewed, []Stage{StageExecuted}, ErrInvalidTransition, true
	case "finalize":
		return StageFinalized, []Stage{StageReviewed}, ErrInvalidTransition, true
	default:
		return "", nil, nil, false
	}
}

// stageInSet reports whether s is a member of stages, the membership check
// transition uses in place of the old single-predecessor equality test.
func stageInSet(stages []Stage, s Stage) bool {
	for _, x := range stages {
		if x == s {
			return true
		}
	}
	return false
}

// commandLabel renders command (with approve) for error/warning text: bare
// "plan" stays "plan", the approve variant renders as "plan --approve",
// every other command renders as itself.
func commandLabel(command string, approve bool) string {
	if command == "plan" && approve {
		return "plan --approve"
	}
	return command
}

// noopWarning renders the AC5 no-op warning text for a command that is
// already at or past its target: `already at stage "<current>"; <command>
// is a no-op`.
func noopWarning(current Stage, command string, approve bool) string {
	return fmt.Sprintf("already at stage %q; %s is a no-op", current, commandLabel(command, approve))
}

// transition computes the next stage for command (one of "prepare",
// "await-input", "plan", "execute", "review", "finalize") given the current
// stage, enforcing the plan's State Machine Design table. approve is only
// meaningful for "plan" (Q&A #1: bare `plan` records
// waiting_for_plan_approval, `plan --approve` advances to plan_approved).
//
// The second return value, noop, signals a monotonic no-op (#636): when the
// persisted stage is already at or past command's target, transition
// returns the persisted stage unchanged (never the target -- the machine is
// strictly forward-only and never rewinds), noop=true, and a nil error.
// Otherwise the existing predecessor-set precondition applies (#826: bare
// `plan` now accepts either of two predecessors, every other command still
// exactly one): a stage in none of them hard-fails with its sentinel error,
// noop=false. An unrecognized persisted stage is default-deny: it never
// ranks as "at or past" a target and always hard-fails with
// ErrInvalidTransition, noop=false.
func transition(from Stage, command string, approve bool) (Stage, bool, error) {
	target, predecessors, sentinel, ok := commandTarget(command, approve)
	if !ok {
		return from, false, fmt.Errorf("unknown pipeline command %q: %w", command, ErrInvalidTransition)
	}

	fromRank, fromOk := stageRank(from)
	if !fromOk {
		return from, false, fmt.Errorf("%s from %s: unknown persisted stage: %w", commandLabel(command, approve), from, ErrInvalidTransition)
	}
	// Defensive invariant, not reachable via any external input today:
	// commandTarget only ever returns a Stage registered in stageOrder, so
	// targetOk is always true in practice. Checked anyway (default-deny per
	// watch/docs/go-gotchas.md #598, watch/docs/error-handling.md #628) so
	// that a future commandTarget entry referencing an unregistered Stage
	// constant hard-fails loudly instead of silently ranking as 0 and turning
	// fromRank >= targetRank into an unconditional no-op-success.
	targetRank, targetOk := stageRank(target)
	if !targetOk {
		return from, false, fmt.Errorf("%s target %s: unknown target stage: %w", commandLabel(command, approve), target, ErrInvalidTransition)
	}

	if fromRank >= targetRank {
		return from, true, nil
	}

	if !stageInSet(predecessors, from) {
		return from, false, fmt.Errorf("%s from %s: %w", commandLabel(command, approve), from, sentinel)
	}
	return target, false, nil
}

// IsKnownStage reports whether stage is registered in the pipeline's total
// stage order. It is the exported validity check for callers outside this
// package (internal/dispatch's stage probe, #732) so stage-order knowledge
// is never re-implemented elsewhere. Default-deny: an unrecognized value
// reports false (watch/docs/go-gotchas.md #598,
// watch/docs/error-handling.md #628).
func IsKnownStage(stage Stage) bool { _, ok := stageRank(stage); return ok }

// NextActionsFor is the exported form of nextActionsFor (ticket #559): the
// mechanics verbs' CLI rendering (pipeline_cmd.go, package main) renders the
// same {state, next_actions, artifacts, warnings, errors} contract the stage
// commands do, and needs this guidance table without duplicating it.
func NextActionsFor(stage Stage) []string {
	return nextActionsFor(stage)
}

// nextActionsFor returns the stage-appropriate guidance for the structured
// output contract's next_actions field. Always non-nil (possibly empty for
// the terminal finalized stage, where there is nothing left to do).
func nextActionsFor(stage Stage) []string {
	switch stage {
	case StageNew:
		return []string{"run `cenci pipeline prepare <id>` to begin"}
	case StagePrepared:
		return []string{"run `cenci pipeline plan <id>` to draft a plan"}
	case StageWaitingForInput:
		return []string{"answer the open questions posted on the ticket, then run `cenci pipeline plan <id>` to resume planning"}
	case StageWaitingForPlanApproval:
		return []string{"review the plan, then run `cenci pipeline plan <id> --approve`"}
	case StagePlanApproved:
		return []string{"run `cenci pipeline execute <id>` to implement the plan"}
	case StageExecuted:
		return []string{"run `cenci pipeline review <id>` to review the implementation"}
	case StageReviewed:
		return []string{"run `cenci pipeline finalize <id>` to finalize and open the PR"}
	case StageFinalized:
		return []string{}
	default:
		return []string{}
	}
}
