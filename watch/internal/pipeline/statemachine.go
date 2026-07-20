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

// transition computes the next stage for command (one of "prepare", "plan",
// "execute", "review", "finalize") given the current stage, enforcing the
// plan's State Machine Design table. approve is only meaningful for "plan"
// (Q&A #1: bare `plan` records waiting_for_plan_approval, `plan --approve`
// advances to plan_approved).
func transition(from Stage, command string, approve bool) (Stage, error) {
	switch command {
	case "prepare":
		switch from {
		case StageNew:
			return StagePrepared, nil
		case StagePrepared:
			// Idempotent: re-running prepare once already prepared re-emits
			// the current state without error.
			return StagePrepared, nil
		default:
			return from, fmt.Errorf("prepare from %s: %w", from, ErrInvalidTransition)
		}
	case "plan":
		if approve {
			if from != StageWaitingForPlanApproval {
				return from, fmt.Errorf("plan --approve from %s: %w", from, ErrInvalidTransition)
			}
			return StagePlanApproved, nil
		}
		if from != StagePrepared {
			return from, fmt.Errorf("plan from %s: %w", from, ErrNotPrepared)
		}
		return StageWaitingForPlanApproval, nil
	case "execute":
		if from != StagePlanApproved {
			return from, fmt.Errorf("execute from %s: %w", from, ErrPlanNotApproved)
		}
		return StageExecuted, nil
	case "review":
		if from != StageExecuted {
			return from, fmt.Errorf("review from %s: %w", from, ErrInvalidTransition)
		}
		return StageReviewed, nil
	case "finalize":
		if from != StageReviewed {
			return from, fmt.Errorf("finalize from %s: %w", from, ErrInvalidTransition)
		}
		return StageFinalized, nil
	default:
		return from, fmt.Errorf("unknown pipeline command %q: %w", command, ErrInvalidTransition)
	}
}

// NextActionsFor is the exported form of nextActionsFor (ticket #559): the
// mechanics verbs' CLI rendering (pipeline_cmd.go, package main) renders the
// same {state, next_actions, artifacts, warnings, errors} contract the five
// stage commands do, and needs this guidance table without duplicating it.
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
