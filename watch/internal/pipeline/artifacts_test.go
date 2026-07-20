package pipeline

// Unit tests for artifact set/get (ticket #559): round-trips through the
// persisted state file, and session-metadata merge semantics (setting one
// key must never clobber another already-set key). Package-boundary
// ("white box") tests, matching store_test.go's own convention. The
// CLI-level `cenci pipeline artifact <id>` dispatch tests live in the
// black-box watch/pipeline_mechanics_cmd_test.go instead.

import (
	"path/filepath"
	"testing"
)

// -- round-trips: set a field, reload from disk, confirm it persisted -----

func TestSetArtifacts_PlanPath_RoundTrips(t *testing.T) {
	stateDir := t.TempDir()

	if _, err := SetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir, PlanPath: ".plans/42-add-thing.md"}); err != nil {
		t.Fatalf("SetArtifacts: %v", err)
	}

	got, err := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("GetArtifacts: %v", err)
	}
	if got.PlanPath != ".plans/42-add-thing.md" {
		t.Errorf("PlanPath = %q, want %q", got.PlanPath, ".plans/42-add-thing.md")
	}
}

func TestSetArtifacts_BranchWorktreePathPR_RoundTrip(t *testing.T) {
	stateDir := t.TempDir()

	_, err := SetArtifacts(ArtifactOpts{
		ID:           "42",
		StateDir:     stateDir,
		Branch:       "feature/42-add-thing",
		WorktreePath: filepath.Join(stateDir, "..", ".worktrees", "42-add-thing"),
		PRURL:        "https://github.com/o/r/pull/7",
		PRNumber:     7,
		PRNumberSet:  true,
	})
	if err != nil {
		t.Fatalf("SetArtifacts: %v", err)
	}

	got, err := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("GetArtifacts: %v", err)
	}
	if got.Branch != "feature/42-add-thing" {
		t.Errorf("Branch = %q, want %q", got.Branch, "feature/42-add-thing")
	}
	if got.PRURL != "https://github.com/o/r/pull/7" {
		t.Errorf("PRURL = %q, want %q", got.PRURL, "https://github.com/o/r/pull/7")
	}
	if got.PRNumber != 7 {
		t.Errorf("PRNumber = %d, want %d", got.PRNumber, 7)
	}
}

// -- session metadata: merge, never clobber ------------------------------

func TestSetArtifacts_SessionMerge_DoesNotClobberOtherKeys(t *testing.T) {
	stateDir := t.TempDir()

	if _, err := SetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir, Session: map[string]string{"runId": "abc123"}}); err != nil {
		t.Fatalf("first SetArtifacts (runId): %v", err)
	}
	if _, err := SetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir, Session: map[string]string{"reviewPath": "full"}}); err != nil {
		t.Fatalf("second SetArtifacts (reviewPath): %v", err)
	}

	got, err := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("GetArtifacts: %v", err)
	}
	if got.Session["runId"] != "abc123" {
		t.Errorf("Session[runId] = %q, want %q (must survive the second, unrelated SetArtifacts call)", got.Session["runId"], "abc123")
	}
	if got.Session["reviewPath"] != "full" {
		t.Errorf("Session[reviewPath] = %q, want %q", got.Session["reviewPath"], "full")
	}
}

func TestSetArtifacts_SessionMerge_OverwritesSameKey(t *testing.T) {
	stateDir := t.TempDir()

	if _, err := SetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir, Session: map[string]string{"reviewPath": "lite-docs"}}); err != nil {
		t.Fatalf("first SetArtifacts: %v", err)
	}
	if _, err := SetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir, Session: map[string]string{"reviewPath": "full"}}); err != nil {
		t.Fatalf("second SetArtifacts (same key, new value): %v", err)
	}

	got, err := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("GetArtifacts: %v", err)
	}
	if got.Session["reviewPath"] != "full" {
		t.Errorf("Session[reviewPath] = %q, want %q (the later call's value)", got.Session["reviewPath"], "full")
	}
}

// -- GetArtifacts on an untouched ticket -----------------------------------

func TestGetArtifacts_NoPriorState_ReturnsNewStageNoError(t *testing.T) {
	stateDir := t.TempDir()

	got, err := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
	if err != nil {
		t.Fatalf("GetArtifacts on an untouched ticket: unexpected error: %v", err)
	}
	if got.Stage != StageNew {
		t.Errorf("Stage = %q, want %q (mirrors loadState's missing-file tolerance)", got.Stage, StageNew)
	}
}
