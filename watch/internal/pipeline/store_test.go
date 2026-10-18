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

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "42.json" {
		t.Errorf("expected only 42.json after a successful save (no leftover temp file), got %v", entries)
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

// -- recorded launch triple (#1292) ------------------------------------------

// launchTripleSession is a valid recorded triple plus two unrelated session
// keys that a clear must leave alone.
func launchTripleSession() map[string]string {
	return map[string]string{
		"launchId":   "0123456789abcdef0123456789abcdef",
		"container":  "claude-cenci-repo",
		"runtime":    "docker",
		"runId":      "run-7",
		"reviewPath": "/tmp/review.md",
	}
}

func TestSessionKeys_AreTheDocumentedStateFileKeys(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{SessionKeyLaunchID, "launchId"},
		{SessionKeyContainer, "container"},
		{SessionKeyRuntime, "runtime"},
	} {
		if tc.got != tc.want {
			t.Errorf("session key = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestValidLaunchTriple_AcceptsOnlyAWellFormedTriple(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name              string
		id, container, rt string
		want              bool
	}{
		{"valid docker", id, "claude-cenci-repo", "docker", true},
		{"valid podman", id, "codex-cenci-repo-feature", "podman", true},
		{"empty", "", "", "", false},
		{"id uppercase", "0123456789ABCDEF0123456789abcdef", "claude-cenci-repo", "docker", false},
		{"id 31 chars", id[:31], "claude-cenci-repo", "docker", false},
		{"container without sandbox prefix", id, "nginx", "docker", false},
		{"container with option injection", id, "claude-cenci-x --privileged", "docker", false},
		{"runtime sh", id, "claude-cenci-repo", "sh", false},
		{"runtime empty", id, "claude-cenci-repo", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidLaunchTriple(tc.id, tc.container, tc.rt); got != tc.want {
				t.Errorf("ValidLaunchTriple(%q, %q, %q) = %v, want %v", tc.id, tc.container, tc.rt, got, tc.want)
			}
		})
	}
}

func TestClearLaunch_RemovesTheTripleAndKeepsEveryOtherSessionKey(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	if err := saveState(path, State{SchemaVersion: CurrentSchemaVersion, ID: "42", Stage: StagePrepared, UpdatedAt: time.Now().UTC(), Session: launchTripleSession()}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err != nil {
		t.Fatalf("ClearLaunch: %v", err)
	}

	got, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	want := map[string]string{"runId": "run-7", "reviewPath": "/tmp/review.md"}
	if len(got.Session) != len(want) || got.Session["runId"] != "run-7" || got.Session["reviewPath"] != "/tmp/review.md" {
		t.Errorf("session after clear = %v, want %v", got.Session, want)
	}
	if got.Stage != StagePrepared {
		t.Errorf("stage = %q, want it untouched (%q)", got.Stage, StagePrepared)
	}
}

func TestClearLaunch_WhenTheTripleWasTheOnlySessionContent_DropsTheSessionObject(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	session := launchTripleSession()
	delete(session, "runId")
	delete(session, "reviewPath")
	if err := saveState(path, State{SchemaVersion: CurrentSchemaVersion, ID: "42", Stage: StagePrepared, UpdatedAt: time.Now().UTC(), Session: session}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err != nil {
		t.Fatalf("ClearLaunch: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "session") || strings.Contains(string(raw), "launchId") {
		t.Errorf("state file still mentions the session after clearing its only keys:\n%s", raw)
	}
}

func TestClearLaunch_MissingStateFile_IsANoOpThatCreatesNothing(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "never-created")

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err != nil {
		t.Fatalf("ClearLaunch on a missing state file: %v, want nil", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("ClearLaunch created %d entries under %s, want none", len(entries), root)
	}
}

func TestClearLaunch_FileWithoutTheTripleKeys_IsNotRewritten(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	// A hand-written file in a shape saveState would not reproduce byte for
	// byte: any rewrite changes it.
	body := []byte(`{"schemaVersion":2,"id":"42","stage":"prepared","updatedAt":"2024-01-01T00:00:00Z","session":{"runId":"run-7"}}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err != nil {
		t.Fatalf("ClearLaunch: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("state file was rewritten although it carried no launch keys:\n%s", got)
	}
}

// seedClearTarget writes a state file carrying the launch triple at
// <stateDir>/42.json and returns its path.
func seedClearTarget(t *testing.T, stateDir string) string {
	t.Helper()
	path := filepath.Join(stateDir, "42.json")
	if err := saveState(path, State{SchemaVersion: CurrentSchemaVersion, ID: "42", Stage: StagePrepared, UpdatedAt: time.Now().UTC(), Session: launchTripleSession()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return path
}

// The state dir lives in the agent-writable bind-mounted repo, so the host-side
// ClearLaunch must never write through a planted symlink.
func TestClearLaunch_PlantedTmpSymlink_DoesNotWriteThroughIt(t *testing.T) {
	stateDir := t.TempDir()
	path := seedClearTarget(t, stateDir)
	victim := filepath.Join(t.TempDir(), "victim")
	const original = "host file the daemon can write"
	if err := os.WriteFile(victim, []byte(original), 0o644); err != nil {
		t.Fatalf("victim: %v", err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err != nil {
		t.Fatalf("ClearLaunch: %v, want it to succeed without using the planted name", err)
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != original {
		t.Errorf("victim was written through the planted .tmp symlink: %q", got)
	}
	s, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if _, ok := s.Session[SessionKeyLaunchID]; ok {
		t.Errorf("launch triple still recorded after a successful clear: %v", s.Session)
	}
}

func TestClearLaunch_PlantedLockSymlink_RefusesAndLeavesTheTargetAlone(t *testing.T) {
	stateDir := t.TempDir()
	path := seedClearTarget(t, stateDir)
	victim := filepath.Join(t.TempDir(), "victim-lock")
	if err := os.Symlink(victim, path+".lock"); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err == nil {
		t.Fatal("ClearLaunch through a planted .lock symlink returned nil, want an error")
	}

	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Errorf("the planted .lock symlink was followed and created %s (lstat err = %v)", victim, err)
	}
}

func TestClearLaunch_StatePathIsASymlink_RefusesAndLeavesTheTargetAlone(t *testing.T) {
	stateDir := t.TempDir()
	victim := seedClearTarget(t, t.TempDir())
	before, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(stateDir, "42.json")); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err == nil {
		t.Fatal("ClearLaunch on a symlinked state path returned nil, want an error")
	}

	after, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("victim was modified through the symlinked state path:\n%s", after)
	}
}

func TestClearLaunch_StatePathIsADirectory_Refuses(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(stateDir, "42.json"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err == nil {
		t.Fatal("ClearLaunch on a directory at the state path returned nil, want an error")
	}
}

func TestClearLaunch_CorruptStateFile_ReturnsAnError(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: stateDir}); err == nil {
		t.Fatal("ClearLaunch on a corrupt state file returned nil, want an error")
	}
}

// assertOutsideDirUntouched fails unless outside still holds exactly the one
// seeded state file with its original bytes: no lock file, temp file or rewrite.
func assertOutsideDirUntouched(t *testing.T, stateFile string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read outside state file: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("outside state file was modified through the symlinked directory:\n%s", after)
	}
	entries, err := os.ReadDir(filepath.Dir(stateFile))
	if err != nil {
		t.Fatalf("ReadDir outside: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("outside dir has entries %v, want only the seeded state file", names)
	}
}

// A symlinked .cenci/pipeline directory (agent-writable via the bind mount)
// must not redirect the host-side lock/read/temp/rename outside the repo.
func TestClearLaunch_PipelineDirIsASymlinkOutsideTheRepo_RefusesAndLeavesTheTargetAlone(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".cenci"), 0o755); err != nil {
		t.Fatalf("mkdir .cenci: %v", err)
	}
	outside := t.TempDir()
	victim := seedClearTarget(t, outside)
	before, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".cenci", "pipeline")); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", RepoRoot: repo}); err == nil {
		t.Fatal("ClearLaunch through a symlinked .cenci/pipeline returned nil, want an error")
	}

	assertOutsideDirUntouched(t, victim, before)
}

// A symlinked .cenci directory redirects the whole state tree the same way.
func TestClearLaunch_CenciDirIsASymlinkOutsideTheRepo_RefusesAndLeavesTheTargetAlone(t *testing.T) {
	repo := t.TempDir()
	outsideCenci := t.TempDir()
	if err := os.Mkdir(filepath.Join(outsideCenci, "pipeline"), 0o755); err != nil {
		t.Fatalf("mkdir outside pipeline: %v", err)
	}
	victim := seedClearTarget(t, filepath.Join(outsideCenci, "pipeline"))
	before, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if err := os.Symlink(outsideCenci, filepath.Join(repo, ".cenci")); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", RepoRoot: repo}); err == nil {
		t.Fatal("ClearLaunch through a symlinked .cenci returned nil, want an error")
	}

	assertOutsideDirUntouched(t, victim, before)
}

func TestClearLaunch_StateDirIsASymlink_Refuses(t *testing.T) {
	outside := t.TempDir()
	victim := seedClearTarget(t, outside)
	before, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	link := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := ClearLaunch(ArtifactOpts{ID: "42", StateDir: link}); err == nil {
		t.Fatal("ClearLaunch through a symlinked explicit StateDir returned nil, want an error")
	}

	assertOutsideDirUntouched(t, victim, before)
}

func TestClearLaunch_RealRepoLayout_StillClearsTheTriple(t *testing.T) {
	repo := t.TempDir()
	path := seedClearTarget(t, filepath.Join(repo, ".cenci", "pipeline"))

	if err := ClearLaunch(ArtifactOpts{ID: "42", RepoRoot: repo}); err != nil {
		t.Fatalf("ClearLaunch: %v", err)
	}

	s, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if _, ok := s.Session[SessionKeyLaunchID]; ok {
		t.Errorf("launch triple still recorded after a successful clear: %v", s.Session)
	}
	if s.Session["runId"] != "run-7" {
		t.Errorf("runId = %q, want it kept", s.Session["runId"])
	}
}
