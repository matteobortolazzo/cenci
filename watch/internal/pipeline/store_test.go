package pipeline

// Unit tests for the pipeline state store (ticket #558): atomic load/save
// round-trip, schemaVersion tagging (mirrors internal/babysit's State), and
// <id> validation against ^\d+$ before it is used to build a path (Files to
// Create: store.go). Repo-root resolution mirrors
// internal/sandbox/launcher/scope.go's ResolveRepoRoot pattern. In-package
// ("white box") test file, matching internal/babysit/babysit_test.go's own
// convention.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// -- atomic load/save round-trip -----------------------------------------

func TestSaveState_ThenLoadState_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "42.json")

	want := State{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "42",
		Stage:         StagePrepared,
		UpdatedAt:     time.Now().UTC().Round(time.Second),
	}
	if err := saveState(path, want); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	got, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if got.ID != want.ID || got.Stage != want.Stage || got.SchemaVersion != want.SchemaVersion {
		t.Fatalf("loadState round-trip = %+v, want %+v", got, want)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("loadState UpdatedAt = %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
}

// TestSaveState_WritesAtomicallyViaTmpAndRename locks in the "atomic writes"
// requirement from the plan (`.tmp`+`os.Rename`, mirroring babysit's save):
// after a successful save, no leftover ".tmp" file remains and the target
// path itself contains valid, complete JSON.
func TestSaveState_WritesAtomicallyViaTmpAndRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "42.json")

	s := State{SchemaVersion: CurrentSchemaVersion, ID: "42", Stage: StageExecuted, UpdatedAt: time.Now().UTC()}
	if err := saveState(path, s); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected no leftover %s.tmp after a successful save, stat err = %v", path, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved state: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("saved state is not valid JSON: %v\n%s", err, raw)
	}
}

// TestSaveState_IncludesSchemaVersionField locks in the schema-tagging
// requirement (mirrors babysit's SchemaVersion field) so a future format
// change has something to gate on.
func TestSaveState_IncludesSchemaVersionField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "42.json")

	if err := saveState(path, State{SchemaVersion: CurrentSchemaVersion, ID: "42", Stage: StageNew}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved state: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal saved state: %v", err)
	}
	got, ok := probe["schemaVersion"]
	if !ok {
		t.Fatalf("saved state missing schemaVersion field, got: %s", raw)
	}
	if got != float64(CurrentSchemaVersion) {
		t.Errorf("schemaVersion = %v, want %d", got, CurrentSchemaVersion)
	}
}

// TestLoadState_MissingFile_ReturnsNewStageNoError mirrors babysit's `load`
// tolerance of a missing file: the very first `cenci pipeline prepare <id>`
// run for a ticket must not error just because no state file exists yet.
func TestLoadState_MissingFile_ReturnsNewStageNoError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	got, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState on missing file: unexpected error: %v", err)
	}
	if got.Stage != StageNew {
		t.Errorf("loadState on missing file: Stage = %q, want %q", got.Stage, StageNew)
	}
}

// -- <id> validation before path use -------------------------------------

// TestStatePath_ValidatesIDBeforeBuildingPath covers the plan's assumption
// that "<id> is validated ^\d+$ before path use": a malicious or malformed
// id must never reach filepath.Join, since that could otherwise be used to
// escape .cenci/pipeline (path traversal) or collide with an unintended
// file.
func TestStatePath_ValidatesIDBeforeBuildingPath(t *testing.T) {
	repoRoot := t.TempDir()

	valid := []string{"1", "42", "000123"}
	for _, id := range valid {
		path, err := statePath(repoRoot, id)
		if err != nil {
			t.Errorf("statePath(%q) unexpected error: %v", id, err)
			continue
		}
		want := filepath.Join(repoRoot, ".cenci", "pipeline", id+".json")
		if path != want {
			t.Errorf("statePath(%q) = %q, want %q", id, path, want)
		}
	}

	invalid := []string{"", "abc", "42a", "-1", "1.5", "../../etc/passwd", "42; rm -rf /", "42/../../etc"}
	for _, id := range invalid {
		path, err := statePath(repoRoot, id)
		if err == nil {
			t.Errorf("statePath(%q) = %q, <nil>, want an error (id must match ^\\d+$)", id, path)
			continue
		}
		if path != "" {
			t.Errorf("statePath(%q) returned non-empty path %q alongside an error; must not construct a path from an invalid id", id, path)
		}
		if !strings.Contains(err.Error(), "id") {
			t.Errorf("statePath(%q) error = %q, want a content-specific message mentioning the invalid id", id, err.Error())
		}
	}
}

// -- repo-root resolution (reuses the launcher/scope.go pattern) --------

// TestResolveRepoRoot_ReturnsGitToplevel exercises the real `git
// rev-parse --show-toplevel` pattern named in the Architectural Context
// (internal/sandbox/launcher/scope.go:ResolveRepoRoot).
func TestResolveRepoRoot_ReturnsGitToplevel(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	sub := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	got, err := resolveRepoRoot(sub)
	if err != nil {
		t.Fatalf("resolveRepoRoot: %v", err)
	}

	wantReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(want): %v", err)
	}
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(got): %v", err)
	}
	if gotReal != wantReal {
		t.Errorf("resolveRepoRoot(%q) = %q, want %q", sub, got, dir)
	}
}

// TestResolveRepoRoot_NonRepoDir_ReturnsError covers the fallback signal a
// caller needs to detect "not inside a git repo" (mirrors
// launcher.ResolveRepoRoot's own contract).
func TestResolveRepoRoot_NonRepoDir_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveRepoRoot(dir); err == nil {
		t.Fatal("resolveRepoRoot on a non-git dir: want an error, got nil")
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}
