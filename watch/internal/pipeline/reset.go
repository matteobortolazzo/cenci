package pipeline

// Reset implements `cenci pipeline reset <id>` (ticket #732): the escape
// hatch that deletes a ticket's persisted state file outright, returning it
// to StageNew with every recorded artifact dropped from tracking. It is pure
// local mechanics: it never calls gh, never touches labels, and -- unlike
// every other mutating verb -- never checks the current stage. There is no
// stage check anywhere in Reset: `finalized` resets exactly like `prepared`.
//
// Structure mirrors Run (pipeline.go): resolveStatePath -> MkdirAll ->
// withLock(path+".lock", defaultRetryConfig(), fn). Unlike Run's
// lock-contention fallback, which detects "fn never ran" via the
// out.State == "" sentinel, Reset uses an explicit ran bool: "" is a
// legitimate out.State on Reset's own corrupt-file + delete-failure
// combination, so the sentinel would misfire here.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ResetOpts are the resolved inputs to one `cenci pipeline reset <id>`
// invocation, mirroring Opts' RepoRoot/StateDir precedence.
type ResetOpts struct {
	ID       string
	RepoRoot string
	StateDir string
}

// Reset deletes the persisted state file for o.ID and returns the standard
// {state, next_actions, artifacts, warnings, errors} contract. See the
// package doc comment above for the behavior matrix; the short version:
// missing file -> idempotent no-op; present file (decodable, corrupt, or
// unreadable) -> deleted, warnings explain what happened, state returns to
// "new"; a delete failure reports the stage still genuinely on disk (never
// "new" -- that would falsely claim a rewind occurred) with errors[]
// populated and a non-nil error.
func Reset(o ResetOpts) (Output, error) {
	path, err := resolveStatePath(Opts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})
	if err != nil {
		return errOutput(StageNew, "", err), err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		mkErr := fmt.Errorf("create state dir: %w", err)
		return errOutput(StageNew, path, mkErr), mkErr
	}

	var out Output
	ran := false
	lockErr := withLock(path+".lock", defaultRetryConfig(), func() error {
		ran = true
		var rerr error
		out, rerr = resetLocked(o.ID, path)
		return rerr
	})

	if lockErr != nil {
		if !ran {
			// The lock was never acquired (ErrLockContention or an open
			// failure): fn never ran, so out was never populated. Best-effort
			// peek at the current on-disk stage (outside the lock, for
			// reporting purposes only) so the contract's state field isn't
			// empty, mirroring Run's own fallback.
			cur, _ := loadState(path)
			out = errOutput(cur.Stage, path, lockErr)
		}
		return out, lockErr
	}
	return out, nil
}

// resetLocked runs while holding the per-ticket flock: read (or note the
// absence of) the current state file, delete it, and build the resulting
// Output. The returned error is nil except on a genuine delete failure.
func resetLocked(id, path string) (Output, error) {
	b, readErr := os.ReadFile(path)

	if readErr != nil && os.IsNotExist(readErr) {
		// Nothing to reset: the idempotent no-op case. No delete attempted.
		warning := fmt.Sprintf("no pipeline state for %s; nothing to reset", id)
		return Output{
			State:       string(StageNew),
			NextActions: nextActionsFor(StageNew),
			Artifacts:   []string{},
			Warnings:    []string{warning},
			Errors:      []string{},
		}, nil
	}

	var warnings []string
	var knownStage Stage // stays "" when the prior stage could not be determined (corrupt/unreadable)

	switch {
	case readErr != nil:
		// A non-ENOENT read failure (e.g. EACCES on the file itself) is the
		// sibling malformed-input class to corrupt JSON (#710): treated the
		// same way -- warn, still attempt delete.
		warnings = []string{fmt.Sprintf("pipeline state for %s could not be read (%v); resetting anyway", id, readErr)}
	default:
		var s State
		if jsonErr := json.Unmarshal(b, &s); jsonErr != nil {
			warnings = []string{fmt.Sprintf("previous pipeline state for %s could not be decoded (%v); resetting anyway", id, jsonErr)}
		} else {
			knownStage = s.Stage
			warnings = resetWarnings(id, s)
		}
	}

	if removeErr := os.Remove(path); removeErr != nil {
		return Output{
			State:       string(knownStage),
			NextActions: nextActionsFor(knownStage),
			Artifacts:   []string{path},
			Warnings:    warnings,
			Errors:      []string{removeErr.Error()},
		}, removeErr
	}

	return Output{
		State:       string(StageNew),
		NextActions: nextActionsFor(StageNew),
		Artifacts:   []string{},
		Warnings:    warnings,
		Errors:      []string{},
	}, nil
}

// resetWarnings renders the warning lines for a decoded existing state file:
// a header always, then only the four artifact fields the AC names --
// Branch, WorktreePath, PRURL/PRNumber, PlanPath -- each only when non-empty
// (Q2, resolved: Labels/Session/TicketUpdatedAt are bookkeeping, not
// orphaned external artifacts, and get no warnings even when populated).
func resetWarnings(id string, s State) []string {
	warnings := []string{fmt.Sprintf(
		"reset ticket %s from stage %q; all recorded artifacts are now untracked (they still exist on disk/GitHub and are not deleted)",
		id, s.Stage,
	)}
	if s.Branch != "" {
		warnings = append(warnings, fmt.Sprintf("dropped tracked branch: %s (the branch still exists in git)", s.Branch))
	}
	if s.WorktreePath != "" {
		warnings = append(warnings, fmt.Sprintf(
			"dropped tracked worktree: %s (the worktree still exists on disk; remove it before re-running "+
				"`cenci pipeline worktree <id> --slug <slug>`, which fails with %q otherwise)",
			s.WorktreePath, "worktree or branch already exists",
		))
	}
	if s.PRURL != "" || s.PRNumber != 0 {
		warnings = append(warnings, fmt.Sprintf("dropped tracked PR: %s (the PR still exists on GitHub)", prLabel(s.PRURL, s.PRNumber)))
	}
	if s.PlanPath != "" {
		warnings = append(warnings, fmt.Sprintf("dropped tracked plan file: %s (the plan file still exists on disk)", s.PlanPath))
	}
	return warnings
}

// prLabel renders the PR identity for the combined PR warning: both fields
// when both are set, degrading to just the one that is.
func prLabel(url string, number int) string {
	switch {
	case url != "" && number != 0:
		return fmt.Sprintf("%s (#%d)", url, number)
	case url != "":
		return url
	default:
		return fmt.Sprintf("#%d", number)
	}
}
