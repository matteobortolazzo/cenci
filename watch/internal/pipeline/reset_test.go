package pipeline

// Engine-level tests for pipeline.Reset (#732): the `cenci pipeline reset
// <id>` escape hatch that deletes a ticket's persisted state file outright,
// returning it to StageNew with every recorded artifact untracked. Mirrors
// pipeline_test.go's own Run() integration-test convention: seeded via
// Opts.StateDir, package-boundary ("white box") tests exercising Reset
// directly. It never refuses based on stage, never calls gh, and never
// touches labels -- there is no stage check anywhere in Reset.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mustSeedFullState writes an arbitrary State (beyond mustSeedState's
// stage-only seeding in labels_test.go) at <stateDir>/<id>.json, so the
// per-artifact-field warning tests below can seed Branch/WorktreePath/
// PRURL/PRNumber/PlanPath/Labels/Session/TicketUpdatedAt independently.
func mustSeedFullState(t *testing.T, stateDir, id string, s State) {
	t.Helper()
	s.ID = id
	if s.SchemaVersion == 0 {
		s.SchemaVersion = CurrentSchemaVersion
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now().UTC()
	}
	path := filepath.Join(stateDir, id+".json")
	if err := saveState(path, s); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

// assertWarningPresent fails unless want is present verbatim somewhere in
// warnings.
func assertWarningPresent(t *testing.T, warnings []string, want string) {
	t.Helper()
	for _, w := range warnings {
		if w == want {
			return
		}
	}
	t.Errorf("Output.Warnings = %v, want it to contain %q", warnings, want)
}

// -- deletes an existing file -------------------------------------------------

// TestReset_ExistingFile_DeletesAndReturnsToNew covers the AC's core
// contract: an existing state file is deleted, the returned Output reports
// state "new" with StageNew's next_actions, empty artifacts (the file is
// gone), and no errors.
func TestReset_ExistingFile_DeletesAndReturnsToNew(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageFinalized)
	path := filepath.Join(stateDir, "42.json")

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: unexpected error: %v", err)
	}
	if out.State != "new" {
		t.Errorf("Output.State = %q, want %q", out.State, "new")
	}
	if len(out.Errors) != 0 {
		t.Errorf("Output.Errors = %v, want none", out.Errors)
	}
	if len(out.Artifacts) != 0 {
		t.Errorf("Output.Artifacts = %v, want empty (the file is gone)", out.Artifacts)
	}
	wantNext := nextActionsFor(StageNew)
	if len(out.NextActions) != len(wantNext) {
		t.Fatalf("Output.NextActions = %v, want %v", out.NextActions, wantNext)
	}
	for i := range wantNext {
		if out.NextActions[i] != wantNext[i] {
			t.Errorf("Output.NextActions[%d] = %q, want %q", i, out.NextActions[i], wantNext[i])
		}
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("state file %s still exists after Reset, want it deleted", path)
	}
}

// -- idempotent when absent ---------------------------------------------------

// TestReset_NoStateFile_IdempotentNoOp covers the AC's "missing state file"
// row: exit 0 (nil error), the exact idempotent warning, and empty errors[].
func TestReset_NoStateFile_IdempotentNoOp(t *testing.T) {
	stateDir := t.TempDir()

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset on a never-run ticket: unexpected error: %v", err)
	}
	if out.State != "new" {
		t.Errorf("Output.State = %q, want %q", out.State, "new")
	}
	if len(out.Errors) != 0 {
		t.Errorf("Output.Errors = %v, want none", out.Errors)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("Output.Warnings = %v, want exactly one idempotent no-op warning", out.Warnings)
	}
	wantWarning := "no pipeline state for 42; nothing to reset"
	if out.Warnings[0] != wantWarning {
		t.Errorf("Output.Warnings[0] = %q, want %q", out.Warnings[0], wantWarning)
	}
}

// -- corrupt/undecodable file: still deleted, exit-0 shape -------------------

// TestReset_CorruptFile_DecodeWarningStillDeletesExit0 covers the AC's
// recovery-path row: a state file that fails to decode is still deleted,
// with a warning naming the decode failure, and exits 0 (this is the
// documented remedy for the dispatcher's "pipeline state unreadable" skip).
func TestReset_CorruptFile_DecodeWarningStillDeletesExit0(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset on a corrupt state file: unexpected error: %v", err)
	}
	if out.State != "new" {
		t.Errorf("Output.State = %q, want %q", out.State, "new")
	}
	if len(out.Errors) != 0 {
		t.Errorf("Output.Errors = %v, want none (recovery path must exit 0)", out.Errors)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("Output.Warnings = %v, want exactly one decode-failure warning", out.Warnings)
	}
	if !strings.Contains(out.Warnings[0], "could not be decoded") || !strings.Contains(out.Warnings[0], "resetting anyway") {
		t.Errorf("Output.Warnings[0] = %q, want it to mention decode failure and %q", out.Warnings[0], "resetting anyway")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("corrupt state file %s still exists after Reset, want it deleted", path)
	}
}

// -- unreadable file (non-ENOENT read failure): same recovery path -----------

// TestReset_UnreadableFile_ReadFailureWarningStillDeletesExit0 covers the
// sibling malformed-input class to corrupt JSON
// (watch/docs/error-handling.md #710: audit all related malformed-input
// classes under the same principle): a state
// file that exists but cannot be read (EACCES on the file itself, not
// ENOENT) must be treated the same way -- warn, still delete, exit 0.
func TestReset_UnreadableFile_ReadFailureWarningStillDeletesExit0(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses Unix file permission checks; cannot simulate permission-denied read")
	}
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":2,"id":"42","stage":"finalized"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset on an unreadable state file: unexpected error: %v", err)
	}
	if out.State != "new" {
		t.Errorf("Output.State = %q, want %q", out.State, "new")
	}
	if len(out.Errors) != 0 {
		t.Errorf("Output.Errors = %v, want none (recovery path must exit 0)", out.Errors)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("Output.Warnings = %v, want exactly one read-failure warning", out.Warnings)
	}
	if !strings.Contains(out.Warnings[0], "could not be read") || !strings.Contains(out.Warnings[0], "resetting anyway") {
		t.Errorf("Output.Warnings[0] = %q, want it to mention read failure and %q", out.Warnings[0], "resetting anyway")
	}
}

// -- allowed from every stage, including finalized ---------------------------

// TestReset_AllowedFromEveryStageIncludingFinalized covers the AC's "never
// refuses based on stage" contract table-driven across the entire total
// order: no error, and the result always returns to "new".
func TestReset_AllowedFromEveryStageIncludingFinalized(t *testing.T) {
	stages := []Stage{
		StageNew,
		StagePrepared,
		StageWaitingForPlanApproval,
		StagePlanApproved,
		StageExecuted,
		StageReviewed,
		StageFinalized,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			stateDir := t.TempDir()
			mustSeedState(t, stateDir, "42", stage)

			out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
			if err != nil {
				t.Fatalf("Reset from stage %s: unexpected error: %v", stage, err)
			}
			if out.State != "new" {
				t.Errorf("Reset from stage %s: Output.State = %q, want %q (never refuses based on stage)", stage, out.State, "new")
			}
		})
	}
}

// -- per-field warning content (Q2: only these four fields ever warn) --------

// TestReset_WarningContent_HeaderStatesOriginalStage covers the header
// warning that is always present on a decoded existing file, naming the
// stage it was reset from.
func TestReset_WarningContent_HeaderStatesOriginalStage(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	want := `reset ticket 42 from stage "prepared"; all recorded artifacts are now untracked (they still exist on disk/GitHub and are not deleted)`
	assertWarningPresent(t, out.Warnings, want)
}

func TestReset_WarningContent_BranchOnly(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedFullState(t, stateDir, "42", State{Stage: StageExecuted, Branch: "feature/42-thing"})

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	wantHeader := `reset ticket 42 from stage "executed"; all recorded artifacts are now untracked (they still exist on disk/GitHub and are not deleted)`
	wantBranch := `dropped tracked branch: feature/42-thing (the branch still exists in git)`
	if len(out.Warnings) != 2 {
		t.Fatalf("Output.Warnings = %v, want exactly 2 (header + branch)", out.Warnings)
	}
	if out.Warnings[0] != wantHeader {
		t.Errorf("Output.Warnings[0] = %q, want %q", out.Warnings[0], wantHeader)
	}
	if out.Warnings[1] != wantBranch {
		t.Errorf("Output.Warnings[1] = %q, want %q", out.Warnings[1], wantBranch)
	}
}

func TestReset_WarningContent_WorktreeOnly(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedFullState(t, stateDir, "42", State{Stage: StageExecuted, WorktreePath: "/repo/.worktrees/42-thing"})

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	wantWorktree := "dropped tracked worktree: /repo/.worktrees/42-thing (the worktree still exists on disk; remove it before re-running " +
		"`cenci pipeline worktree <id> --slug <slug>`" +
		`, which fails with "worktree or branch already exists" otherwise)`
	if len(out.Warnings) != 2 {
		t.Fatalf("Output.Warnings = %v, want exactly 2 (header + worktree)", out.Warnings)
	}
	if out.Warnings[1] != wantWorktree {
		t.Errorf("Output.Warnings[1] = %q, want %q", out.Warnings[1], wantWorktree)
	}
}

func TestReset_WarningContent_PRURLOnly(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedFullState(t, stateDir, "42", State{Stage: StageFinalized, PRURL: "https://github.com/o/r/pull/7"})

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	wantPR := "dropped tracked PR: https://github.com/o/r/pull/7 (the PR still exists on GitHub)"
	if len(out.Warnings) != 2 {
		t.Fatalf("Output.Warnings = %v, want exactly 2 (header + PR)", out.Warnings)
	}
	if out.Warnings[1] != wantPR {
		t.Errorf("Output.Warnings[1] = %q, want %q", out.Warnings[1], wantPR)
	}
}

func TestReset_WarningContent_PRNumberOnly(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedFullState(t, stateDir, "42", State{Stage: StageFinalized, PRNumber: 7})

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	wantPR := "dropped tracked PR: #7 (the PR still exists on GitHub)"
	if len(out.Warnings) != 2 {
		t.Fatalf("Output.Warnings = %v, want exactly 2 (header + PR)", out.Warnings)
	}
	if out.Warnings[1] != wantPR {
		t.Errorf("Output.Warnings[1] = %q, want %q", out.Warnings[1], wantPR)
	}
}

// TestReset_WarningContent_BothPRFields_SingleCombinedWarning covers the
// AC's "PR URL/number" bullet: when both are set, they combine into exactly
// ONE warning (never two separate PR warnings).
func TestReset_WarningContent_BothPRFields_SingleCombinedWarning(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedFullState(t, stateDir, "42", State{Stage: StageFinalized, PRURL: "https://github.com/o/r/pull/7", PRNumber: 7})

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	wantPR := "dropped tracked PR: https://github.com/o/r/pull/7 (#7) (the PR still exists on GitHub)"
	if len(out.Warnings) != 2 {
		t.Fatalf("Output.Warnings = %v, want exactly 2 (header + one combined PR warning)", out.Warnings)
	}
	if out.Warnings[1] != wantPR {
		t.Errorf("Output.Warnings[1] = %q, want %q", out.Warnings[1], wantPR)
	}
	prWarnings := 0
	for _, w := range out.Warnings {
		if strings.Contains(w, "dropped tracked PR") {
			prWarnings++
		}
	}
	if prWarnings != 1 {
		t.Errorf("got %d PR-related warnings, want exactly 1 combined warning: %v", prWarnings, out.Warnings)
	}
}

func TestReset_WarningContent_PlanOnly(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedFullState(t, stateDir, "42", State{Stage: StageWaitingForPlanApproval, PlanPath: ".plans/42-thing.md"})

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	wantPlan := "dropped tracked plan file: .plans/42-thing.md (the plan file still exists on disk)"
	if len(out.Warnings) != 2 {
		t.Fatalf("Output.Warnings = %v, want exactly 2 (header + plan)", out.Warnings)
	}
	if out.Warnings[1] != wantPlan {
		t.Errorf("Output.Warnings[1] = %q, want %q", out.Warnings[1], wantPlan)
	}
}

// TestReset_WarningContent_AllFourTogether_ExactWarningsNoBookkeepingFieldsMentioned
// is Q2, resolved: with all four artifact fields AND the bookkeeping fields
// (Labels, Session, TicketUpdatedAt) populated, the warnings[] must contain
// exactly the five expected lines in order (header, branch, worktree, PR,
// plan) and must never mention Labels/Session/TicketUpdatedAt even though
// they are populated.
func TestReset_WarningContent_AllFourTogether_ExactWarningsNoBookkeepingFieldsMentioned(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedFullState(t, stateDir, "42", State{
		Stage:           StageFinalized,
		Branch:          "feature/42-thing",
		WorktreePath:    "/repo/.worktrees/42-thing",
		PRURL:           "https://github.com/o/r/pull/7",
		PRNumber:        7,
		PlanPath:        ".plans/42-thing.md",
		Labels:          []string{"Planned", "Working"},
		Session:         map[string]string{"runId": "abc123"},
		TicketUpdatedAt: "2026-07-27T00:00:00Z",
	})

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	wantWarnings := []string{
		`reset ticket 42 from stage "finalized"; all recorded artifacts are now untracked (they still exist on disk/GitHub and are not deleted)`,
		`dropped tracked branch: feature/42-thing (the branch still exists in git)`,
		"dropped tracked worktree: /repo/.worktrees/42-thing (the worktree still exists on disk; remove it before re-running " +
			"`cenci pipeline worktree <id> --slug <slug>`" +
			`, which fails with "worktree or branch already exists" otherwise)`,
		"dropped tracked PR: https://github.com/o/r/pull/7 (#7) (the PR still exists on GitHub)",
		"dropped tracked plan file: .plans/42-thing.md (the plan file still exists on disk)",
	}
	if len(out.Warnings) != len(wantWarnings) {
		t.Fatalf("Output.Warnings = %v (%d), want exactly %d warnings: %v", out.Warnings, len(out.Warnings), len(wantWarnings), wantWarnings)
	}
	for i, w := range wantWarnings {
		if out.Warnings[i] != w {
			t.Errorf("Output.Warnings[%d] = %q, want %q", i, out.Warnings[i], w)
		}
	}

	joined := strings.Join(out.Warnings, "\n")
	for _, forbidden := range []string{"Labels", "Session", "TicketUpdatedAt", "runId", "abc123", "Planned", "Working"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("Output.Warnings unexpectedly mentions %q -- only branch/worktree/PR/plan get warnings (Q2, resolved)", forbidden)
		}
	}
}

// -- delete failure: reports the stage still on disk, errors[], exit 1 ------

// TestReset_DeleteFails_ReportsStageStillOnDiskWithErrorsAndExit1 is Q1,
// resolved: on a delete failure, the contract's state field must report the
// stage genuinely still on disk (never "new" -- that would falsely claim a
// rewind occurred), artifacts must still name the file (it's still there),
// errors[] must be populated, and the call must return a non-nil error.
// Simulated by chmod'ing the state directory read-only (0o500) after
// pre-creating the lock file, so the failure is specifically os.Remove's,
// not lock acquisition's. Skipped on root/CI where chmod is ineffective.
func TestReset_DeleteFails_ReportsStageStillOnDiskWithErrorsAndExit1(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses Unix directory permission checks; cannot simulate a delete failure")
	}
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageFinalized)
	path := filepath.Join(stateDir, "42.json")

	// Pre-create the lock file so withLock's O_CREATE|O_RDWR open still
	// succeeds once the directory itself is read-only -- isolating the
	// chmod's effect to os.Remove(path) specifically, not lock acquisition.
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
	if err == nil {
		t.Fatal("Reset with an undeletable state file: want a non-nil error, got nil")
	}
	if out.State != "finalized" {
		t.Errorf(`Output.State = %q, want %q (the stage genuinely still on disk, never "new")`, out.State, "finalized")
	}
	if len(out.Artifacts) != 1 || out.Artifacts[0] != path {
		t.Errorf("Output.Artifacts = %v, want [%q] (the file is still there)", out.Artifacts, path)
	}
	if len(out.Errors) == 0 {
		t.Error("Output.Errors = [], want the delete error populated")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("state file %s must still exist after a failed delete: %v", path, statErr)
	}
}

// -- never touches stage-gate mechanics --------------------------------------

// TestReset_NeverInvokesTransition locks in the "no stage check anywhere in
// Reset" constraint at the behavioral level: finalized resets exactly like
// prepared (both succeed identically, with no ErrInvalidTransition-style
// rejection), proving Reset never routes through transition()/stageRank().
func TestReset_NeverInvokesTransition(t *testing.T) {
	for _, stage := range []Stage{StagePrepared, StageFinalized} {
		stateDir := t.TempDir()
		mustSeedState(t, stateDir, "42", stage)

		out, err := Reset(ResetOpts{ID: "42", StateDir: stateDir})
		if err != nil {
			t.Fatalf("Reset from %s: unexpected error: %v", stage, err)
		}
		if out.State != "new" {
			t.Errorf("Reset from %s: Output.State = %q, want %q", stage, out.State, "new")
		}
	}
}
