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

// -- ticket #559: schema v2 + main-checkout-anchored repo-root resolution --

// TestCurrentSchemaVersion_IsV2 locks in the v2 bump named in ticket #559's
// store.go changes (new omitempty fields: PlanPath, Branch, WorktreePath,
// PRURL, PRNumber, Labels, Session). RED until CurrentSchemaVersion is
// bumped from 1 to 2.
func TestCurrentSchemaVersion_IsV2(t *testing.T) {
	if CurrentSchemaVersion != 2 {
		t.Errorf("CurrentSchemaVersion = %d, want 2 (ticket #559's schema bump for the new artifact-tracking fields)", CurrentSchemaVersion)
	}
}

// TestState_V2FieldsRoundTripThroughSaveAndLoad locks in that the new v2
// fields (PlanPath, Branch, WorktreePath, PRURL, PRNumber, Labels, Session)
// persist and reload exactly, alongside the existing v1 fields.
func TestState_V2FieldsRoundTripThroughSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "42.json")

	want := State{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "42",
		Stage:         StagePlanApproved,
		UpdatedAt:     time.Now().UTC().Round(time.Second),
		PlanPath:      ".plans/42-add-thing.md",
		Branch:        "feature/42-add-thing",
		WorktreePath:  "/repo/.worktrees/42-add-thing",
		PRURL:         "https://github.com/o/r/pull/7",
		PRNumber:      7,
		Labels:        []string{"Working", "Planned"},
		Session:       map[string]string{"runId": "abc123", "reviewPath": "full"},
	}
	if err := saveState(path, want); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	got, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if got.PlanPath != want.PlanPath {
		t.Errorf("PlanPath = %q, want %q", got.PlanPath, want.PlanPath)
	}
	if got.Branch != want.Branch {
		t.Errorf("Branch = %q, want %q", got.Branch, want.Branch)
	}
	if got.WorktreePath != want.WorktreePath {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, want.WorktreePath)
	}
	if got.PRURL != want.PRURL {
		t.Errorf("PRURL = %q, want %q", got.PRURL, want.PRURL)
	}
	if got.PRNumber != want.PRNumber {
		t.Errorf("PRNumber = %d, want %d", got.PRNumber, want.PRNumber)
	}
	if len(got.Labels) != len(want.Labels) {
		t.Fatalf("Labels = %v, want %v", got.Labels, want.Labels)
	}
	for i := range want.Labels {
		if got.Labels[i] != want.Labels[i] {
			t.Errorf("Labels[%d] = %q, want %q", i, got.Labels[i], want.Labels[i])
		}
	}
	if len(got.Session) != len(want.Session) {
		t.Fatalf("Session = %v, want %v", got.Session, want.Session)
	}
	for k, v := range want.Session {
		if got.Session[k] != v {
			t.Errorf("Session[%q] = %q, want %q", k, got.Session[k], v)
		}
	}
}

// TestLoadState_V1StateFile_LoadsWithNewFieldsZeroValued covers backward
// compatibility: a state file written before ticket #559 (schemaVersion=1,
// none of the v2 fields present in the JSON at all) must still load without
// error, with every new field defaulting to its zero value.
func TestLoadState_V1StateFile_LoadsWithNewFieldsZeroValued(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "42.json")

	v1JSON := `{
		"schemaVersion": 1,
		"id": "42",
		"stage": "executed",
		"updatedAt": "2024-01-01T00:00:00Z"
	}`
	if err := os.WriteFile(path, []byte(v1JSON), 0o644); err != nil {
		t.Fatalf("write v1 fixture: %v", err)
	}

	got, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState on a v1 file: unexpected error: %v", err)
	}
	if got.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1 (unchanged from the v1 fixture)", got.SchemaVersion)
	}
	if got.Stage != StageExecuted {
		t.Errorf("Stage = %q, want %q", got.Stage, StageExecuted)
	}
	if got.PlanPath != "" {
		t.Errorf("PlanPath = %q, want zero value %q for a v1 file", got.PlanPath, "")
	}
	if got.Branch != "" {
		t.Errorf("Branch = %q, want zero value %q for a v1 file", got.Branch, "")
	}
	if got.WorktreePath != "" {
		t.Errorf("WorktreePath = %q, want zero value %q for a v1 file", got.WorktreePath, "")
	}
	if got.PRURL != "" {
		t.Errorf("PRURL = %q, want zero value %q for a v1 file", got.PRURL, "")
	}
	if got.PRNumber != 0 {
		t.Errorf("PRNumber = %d, want zero value 0 for a v1 file", got.PRNumber)
	}
	if got.Labels != nil {
		t.Errorf("Labels = %v, want nil (zero value) for a v1 file", got.Labels)
	}
	if got.Session != nil {
		t.Errorf("Session = %v, want nil (zero value) for a v1 file", got.Session)
	}
}

// TestResolveRepoRoot_FromLinkedWorktree_ReturnsMainCheckoutRoot is ticket
// #559's crux fix, tested at the store.go boundary: resolveRepoRoot from
// inside a REAL linked worktree (created via `git worktree add`) must
// return the SAME absolute path as resolveRepoRoot from the main checkout
// root -- never the worktree's own root. RED against the current `git
// rev-parse --show-toplevel`-based implementation, which resolves to the
// worktree's own root instead.
//
// Expected values were verified empirically (not assumed) via `git
// rev-parse --path-format=absolute --git-common-dir` run from both a main
// checkout and one of its linked worktrees: both return
// "<main-repo>/.git", so filepath.Dir of that value is the main checkout's
// root in both cases.
func TestResolveRepoRoot_FromLinkedWorktree_ReturnsMainCheckoutRoot(t *testing.T) {
	mainRepo := t.TempDir()
	initGitRepo(t, mainRepo)
	if out, err := exec.Command("git", "-C", mainRepo, "config", "user.email", "test@example.com").CombinedOutput(); err != nil {
		t.Fatalf("git config user.email: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", mainRepo, "config", "user.name", "Test").CombinedOutput(); err != nil {
		t.Fatalf("git config user.name: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", mainRepo, "commit", "--allow-empty", "-q", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit --allow-empty: %v\n%s", err, out)
	}

	wantReal, err := filepath.EvalSymlinks(mainRepo)
	if err != nil {
		t.Fatalf("EvalSymlinks(mainRepo): %v", err)
	}

	fromMain, err := resolveRepoRoot(mainRepo)
	if err != nil {
		t.Fatalf("resolveRepoRoot(mainRepo): %v", err)
	}
	fromMainReal, err := filepath.EvalSymlinks(fromMain)
	if err != nil {
		t.Fatalf("EvalSymlinks(fromMain): %v", err)
	}
	if fromMainReal != wantReal {
		t.Errorf("resolveRepoRoot(mainRepo) = %q, want %q", fromMain, mainRepo)
	}

	worktreeDir := filepath.Join(t.TempDir(), "linked-worktree")
	if out, err := exec.Command("git", "-C", mainRepo, "worktree", "add", "-q", worktreeDir, "-b", "feature/559-test").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	fromWorktree, err := resolveRepoRoot(worktreeDir)
	if err != nil {
		t.Fatalf("resolveRepoRoot(worktreeDir): %v", err)
	}
	fromWorktreeReal, err := filepath.EvalSymlinks(fromWorktree)
	if err != nil {
		t.Fatalf("EvalSymlinks(fromWorktree): %v", err)
	}
	if fromWorktreeReal != wantReal {
		t.Errorf("resolveRepoRoot(worktreeDir) = %q, want the MAIN checkout root %q, not the worktree's own root %q", fromWorktree, mainRepo, worktreeDir)
	}
}
