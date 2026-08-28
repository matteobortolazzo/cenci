package main_test

// Black-box CLI integration tests for the pipeline mechanics verbs added by
// ticket #559: `cenci pipeline label|worktree|worktree-cleanup|artifact
// <id> [flags]`, plus the cross-worktree state-continuity E2E that is this
// ticket's crux requirement. Runs the real built `cenci` binary as a
// subprocess (binaryPath, built once in TestMain in main_test.go), reusing
// pipeline_cmd_test.go's own helpers (pipelineContract, writeFakeGh,
// pipelineEnv, initGitRepoForCLITest) since this file lives in the same
// `package main_test`.
//
// RED phase: none of these four verbs are wired into pipeline_cmd.go's
// pipelineStages dispatch table yet. Every invocation below currently falls
// through to the existing five-stage usage path and exits 2 via the
// *generic* "cenci pipeline: usage: cenci pipeline prepare|plan|execute|
// review|finalize ..." fallback -- which does not mention any of "label",
// "worktree", "worktree-cleanup", "artifact", or their flags. The
// usage-error assertions below require verb-specific content that fallback
// does not carry, so they fail for the right reason (content mismatch, not
// a false-green shared exit code) -- mirroring pipeline_cmd_test.go's own
// usageMarker false-green guard (see that file's comment above
// assertPipelineUsageExit2). The happy-path assertions fail because no
// verb is recognized yet, so no contract JSON reaches stdout at all.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// -- usage/exit-2 cases for each new verb's required flags -----------------

// assertPipelineVerbUsageExit2 asserts exit code 2 AND that the output
// contains wantSubstr -- a marker only a genuine per-verb implementation
// would print, since the current generic pipeline usage fallback (asserted
// against in pipeline_cmd_test.go's own usageMarker) never mentions any of
// these new verbs or flags.
func assertPipelineVerbUsageExit2(t *testing.T, wantSubstr string, args ...string) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("%v: expected *exec.ExitError, got %T: %v\n%s", args, err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("%v: exit code = %d, want 2\n%s", args, exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), wantSubstr) {
		t.Errorf("%v: output = %q, want it to contain the verb-specific usage marker %q (not just the generic prepare|plan|execute|review|finalize fallback)", args, output, wantSubstr)
	}
}

func TestPipelineLabel_MissingTransition_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline label: usage:", "pipeline", "label", "42", "--repo-slug", "o/r")
}

func TestPipelineLabel_MissingID_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline label: usage:", "pipeline", "label")
}

func TestPipelineLabel_InvalidTransitionValue_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline label: usage:", "pipeline", "label", "42", "--transition", "bogus", "--repo-slug", "o/r")
}

func TestPipelineWorktree_MissingSlug_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline worktree: usage:", "pipeline", "worktree", "42")
}

func TestPipelineWorktree_MissingID_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline worktree: usage:", "pipeline", "worktree", "--slug", "add-thing")
}

func TestPipelineWorktreeCleanup_MissingID_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline worktree-cleanup: usage:", "pipeline", "worktree-cleanup")
}

func TestPipelineArtifact_MissingID_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline artifact: usage:", "pipeline", "artifact")
}

// TestPipelineLabel_MalformedParent_Exit2 covers --parent's required ^\d+$
// shape: a non-numeric value is malformed CLI input, not a domain error,
// and must never reach `gh issue edit` as flag-smuggled argv.
func TestPipelineLabel_MalformedParent_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline label: usage:", "pipeline", "label", "42", "--transition", "in-review", "--parent", "-x", "--repo-slug", "o/r")
}

// TestPipelineLabel_TrivialOnNonPlannedTransition_Exit2 covers finding 6:
// --trivial is only meaningful for --transition planned; passing it with a
// different transition must be rejected outright, not silently ignored.
func TestPipelineLabel_TrivialOnNonPlannedTransition_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline label: usage:", "pipeline", "label", "42", "--transition", "working", "--trivial", "--repo-slug", "o/r")
}

// TestPipelineLabel_ParentOnNonInReviewTransition_Exit2 covers finding 6's
// other half: --parent is only meaningful for --transition in-review.
func TestPipelineLabel_ParentOnNonInReviewTransition_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline label: usage:", "pipeline", "label", "42", "--transition", "planned", "--parent", "10", "--repo-slug", "o/r")
}

// TestPipelineArtifact_GetWithMutationFlag_Exit2 covers finding 4: --get is
// a read-only fetch, so combining it with a mutation flag (--plan here) is
// a conflicting input and must be rejected, not silently ignored.
func TestPipelineArtifact_GetWithMutationFlag_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline artifact: usage:", "pipeline", "artifact", "42", "--get", "--plan", ".plans/42-add-thing.md")
}

// TestPipelineArtifact_MalformedSessionFlag_Exit2 covers --session's
// required k=v shape: a value with no "=" is malformed CLI input, not a
// domain error.
func TestPipelineArtifact_MalformedSessionFlag_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline artifact: usage:", "pipeline", "artifact", "42", "--session", "no-equals-sign")
}

// -- happy-path invocation of each verb producing valid contract JSON ------

// seedPipelineState writes a minimal, valid v2 pipeline state file at path
// with the given stage, so a mechanics verb under test has state to gate
// against without first running through prepare/plan/execute.
func seedPipelineState(t *testing.T, path, id, stage string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	body := `{"schemaVersion":2,"id":"` + id + `","stage":"` + stage + `","updatedAt":"2024-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write seed state at %s: %v", path, err)
	}
}

// writeFakeGhForLabelApply is a minimal fake `gh` covering only the label
// self-healing create/apply calls plus the post-edit updatedAt baseline
// fetch (#669) -- enough for the "planned" transition's happy path, which
// (per labels.go's contract) never touches ownership/assignee gh calls.
func writeFakeGhForLabelApply(t *testing.T, dir string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = \"label\" ] && [ \"$2\" = \"create\" ]; then exit 0; fi\n" +
		"if [ \"$1\" = \"issue\" ] && [ \"$2\" = \"edit\" ]; then exit 0; fi\n" +
		"if [ \"$1\" = \"issue\" ] && [ \"$2\" = \"view\" ]; then echo '{\"updatedAt\":\"2024-01-01T00:00:00Z\"}'; exit 0; fi\n" +
		"echo \"unexpected gh invocation: $*\" 1>&2\n" +
		"exit 1\n"
	exectest.WriteExecutable(t, filepath.Join(dir, "gh"), body)
}

func TestPipelineLabel_PlannedTransition_HappyPath_ProducesContractJSON(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGhForLabelApply(t, fakeDir)
	stateDir := t.TempDir()
	seedPipelineState(t, filepath.Join(stateDir, "42.json"), "42", "waiting_for_plan_approval")

	cmd := exec.Command(binaryPath, "pipeline", "label", "42", "--transition", "planned", "--state-dir", stateDir, "--repo-slug", "octo/repo")
	cmd.Env = pipelineEnv(fakeDir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("pipeline label planned: unexpected error: %v\n%s", err, output)
	}
	var c pipelineContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("pipeline label planned: stdout is not valid JSON: %v\n%s", jerr, output)
	}
	assertArraysNonNil(t, "label planned", c)
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on success", c.Errors)
	}
}

// TestPipelineLabel_InputNeededTransition_HappyPath_ProducesContractJSON
// covers the escalation label swap (#826) at the CLI surface.
func TestPipelineLabel_InputNeededTransition_HappyPath_ProducesContractJSON(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGhForLabelApply(t, fakeDir)
	stateDir := t.TempDir()
	seedPipelineState(t, filepath.Join(stateDir, "42.json"), "42", "waiting_for_input")

	cmd := exec.Command(binaryPath, "pipeline", "label", "42", "--transition", "input-needed", "--state-dir", stateDir, "--repo-slug", "octo/repo")
	cmd.Env = pipelineEnv(fakeDir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("pipeline label input-needed: unexpected error: %v\n%s", err, output)
	}
	var c pipelineContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("pipeline label input-needed: stdout is not valid JSON: %v\n%s", jerr, output)
	}
	assertArraysNonNil(t, "label input-needed", c)
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on success", c.Errors)
	}
}

// TestPipelineLabel_TrivialOnInputNeededTransition_Exit2 mirrors
// TestPipelineLabel_TrivialOnNonPlannedTransition_Exit2: --trivial stays
// meaningful only for --transition planned.
func TestPipelineLabel_TrivialOnInputNeededTransition_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline label: usage:", "pipeline", "label", "42", "--transition", "input-needed", "--trivial", "--repo-slug", "o/r")
}

// TestPipelineLabel_ParentOnInputNeededTransition_Exit2 mirrors
// TestPipelineLabel_ParentOnNonInReviewTransition_Exit2: --parent stays
// meaningful only for --transition in-review.
func TestPipelineLabel_ParentOnInputNeededTransition_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline label: usage:", "pipeline", "label", "42", "--transition", "input-needed", "--parent", "10", "--repo-slug", "o/r")
}

func TestPipelineWorktree_HappyPath_ProducesContractJSON(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoForCLITest(t, repoDir)
	stateDir := t.TempDir()

	cmd := exec.Command(binaryPath, "pipeline", "worktree", "42", "--slug", "add-thing", "--repo", repoDir, "--state-dir", stateDir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("pipeline worktree: unexpected error: %v\n%s", err, output)
	}
	var c pipelineContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("pipeline worktree: stdout is not valid JSON: %v\n%s", jerr, output)
	}
	assertArraysNonNil(t, "worktree", c)
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on success", c.Errors)
	}

	wantDir := filepath.Join(repoDir, ".worktrees", "42-add-thing")
	if _, statErr := os.Stat(wantDir); statErr != nil {
		t.Errorf("expected worktree dir at %s: %v", wantDir, statErr)
	}
}

func TestPipelineWorktreeCleanup_HappyPath_ProducesContractJSON(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoForCLITest(t, repoDir)
	stateDir := t.TempDir()

	createCmd := exec.Command(binaryPath, "pipeline", "worktree", "42", "--slug", "add-thing", "--repo", repoDir, "--state-dir", stateDir)
	if out, err := createCmd.Output(); err != nil {
		t.Fatalf("pipeline worktree (setup): unexpected error: %v\n%s", err, out)
	}

	cleanupCmd := exec.Command(binaryPath, "pipeline", "worktree-cleanup", "42", "--slug", "add-thing", "--repo", repoDir, "--state-dir", stateDir)
	output, err := cleanupCmd.Output()
	if err != nil {
		t.Fatalf("pipeline worktree-cleanup: unexpected error: %v\n%s", err, output)
	}
	var c pipelineContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("pipeline worktree-cleanup: stdout is not valid JSON: %v\n%s", jerr, output)
	}
	assertArraysNonNil(t, "worktree-cleanup", c)
}

// -- ticket #688: `cenci pipeline worktree <id> --attach PATH` reuse mode --
//
// RED: --attach does not exist on runPipelineWorktree's flag set yet. Every
// invocation below either (a) hits the pre-existing generic `--slug` usage
// path (whose text never mentions "--attach", the marker these assertions
// pin, mirroring this file's own false-green guard idiom above for
// verb-specific vs. generic-fallback text) or (b) fails flag.Parse outright
// on the unrecognized `--attach` flag -- exit 2 either way, but never with
// "--attach" in the output, and the happy-path case never reaches exit 0 at
// all since no verb recognizes the flag.

func TestPipelineWorktree_SlugAndAttachTogether_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "--attach", "pipeline", "worktree", "42", "--slug", "add-thing", "--attach", "/tmp/somewhere")
}

// TestPipelineWorktree_NeitherSlugNorAttach_Exit2WithAttachNamed covers the
// "exactly one of --slug/--attach" mutual-exclusion gate's other half:
// omitting both must exit 2, and the usage hint must name --attach
// specifically -- proving the CLI surface actually grew the new flag rather
// than silently continuing to treat --slug as the only reuse path.
func TestPipelineWorktree_NeitherSlugNorAttach_Exit2WithAttachNamed(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "--attach", "pipeline", "worktree", "42")
}

// TestPipelineWorktree_EmptyAttach_Exit2 covers the plan's explicit
// "empty-string --attach value must exit 2" case: an empty value is a
// malformed CLI input, not a domain error (distinct from --attach naming a
// genuinely non-existent path, which is ErrWorktreeNotFound, a domain
// error).
func TestPipelineWorktree_EmptyAttach_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "--attach", "pipeline", "worktree", "42", "--attach", "")
}

// TestPipelineWorktree_AttachHappyPath_ProducesContractJSONWithAttachedPathInArtifacts
// covers the CLI-level attach happy path (case 24's E2E scope): a real
// pre-existing worktree on a non-standard branch, attached via the real
// built binary, with the attached path present in the contract's
// artifacts[].
func TestPipelineWorktree_AttachHappyPath_ProducesContractJSONWithAttachedPathInArtifacts(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoWithCommitForCLITest(t, repoDir)
	stateDir := t.TempDir()

	worktreeDir := filepath.Join(t.TempDir(), "attach-me")
	if out, err := exec.Command("git", "-C", repoDir, "worktree", "add", "-q", worktreeDir, "-b", "non-standard-branch").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	cmd := exec.Command(binaryPath, "pipeline", "worktree", "42", "--attach", worktreeDir, "--repo", repoDir, "--state-dir", stateDir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("pipeline worktree --attach: unexpected error: %v\n%s", err, output)
	}
	var c pipelineContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("pipeline worktree --attach: stdout is not valid JSON: %v\n%s", jerr, output)
	}
	assertArraysNonNil(t, "worktree --attach", c)
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on success", c.Errors)
	}
	wantPath, evalErr := filepath.EvalSymlinks(worktreeDir)
	if evalErr != nil {
		t.Fatalf("EvalSymlinks(worktreeDir): %v", evalErr)
	}
	found := false
	for _, a := range c.Artifacts {
		if a == wantPath {
			found = true
		}
	}
	if !found {
		t.Errorf("artifacts = %v, want the attached path %q present", c.Artifacts, wantPath)
	}
}

func TestPipelineArtifact_SetThenGet_HappyPath_ProducesContractJSON(t *testing.T) {
	stateDir := t.TempDir()
	seedPipelineState(t, filepath.Join(stateDir, "42.json"), "42", "prepared")

	setCmd := exec.Command(binaryPath, "pipeline", "artifact", "42", "--state-dir", stateDir, "--plan", ".plans/42-add-thing.md", "--branch", "feature/42-add-thing", "--session", "runId=abc123")
	setOut, err := setCmd.Output()
	if err != nil {
		t.Fatalf("pipeline artifact (set): unexpected error: %v\n%s", err, setOut)
	}
	var setContract pipelineContract
	if jerr := json.Unmarshal(setOut, &setContract); jerr != nil {
		t.Fatalf("pipeline artifact (set): stdout is not valid JSON: %v\n%s", jerr, setOut)
	}
	assertArraysNonNil(t, "artifact set", setContract)
	if len(setContract.Errors) != 0 {
		t.Errorf("errors = %v, want none on success", setContract.Errors)
	}

	getCmd := exec.Command(binaryPath, "pipeline", "artifact", "42", "--state-dir", stateDir, "--get")
	getOut, err := getCmd.Output()
	if err != nil {
		t.Fatalf("pipeline artifact (get): unexpected error: %v\n%s", err, getOut)
	}
	var getContract pipelineContract
	if jerr := json.Unmarshal(getOut, &getContract); jerr != nil {
		t.Fatalf("pipeline artifact (get): stdout is not valid JSON: %v\n%s", jerr, getOut)
	}
	assertArraysNonNil(t, "artifact get", getContract)
}

// TestPipelineArtifact_Get_ArtifactsContainsBranchKeyValueEntry covers the
// code-review finding (ticket #688 fix-up) that `--get`'s contract JSON
// never actually exposed `branch` anywhere: flow/skills/implement/phases/
// phase-9-pr.md's Push step sources the branch to push from this call's
// `artifacts[]`, so a `branch:<value>` entry must genuinely be present, not
// just an empty `artifacts: []`.
func TestPipelineArtifact_Get_ArtifactsContainsBranchKeyValueEntry(t *testing.T) {
	stateDir := t.TempDir()
	seedPipelineState(t, filepath.Join(stateDir, "42.json"), "42", "prepared")

	setCmd := exec.Command(binaryPath, "pipeline", "artifact", "42", "--state-dir", stateDir, "--branch", "feature/42-add-thing")
	if out, err := setCmd.Output(); err != nil {
		t.Fatalf("pipeline artifact (set --branch): unexpected error: %v\n%s", err, out)
	}

	getCmd := exec.Command(binaryPath, "pipeline", "artifact", "42", "--state-dir", stateDir, "--get")
	getOut, err := getCmd.Output()
	if err != nil {
		t.Fatalf("pipeline artifact (get): unexpected error: %v\n%s", err, getOut)
	}
	var getContract pipelineContract
	if jerr := json.Unmarshal(getOut, &getContract); jerr != nil {
		t.Fatalf("pipeline artifact (get): stdout is not valid JSON: %v\n%s", jerr, getOut)
	}
	assertArraysNonNil(t, "artifact get (branch check)", getContract)

	found := false
	for _, a := range getContract.Artifacts {
		if a == "branch:feature/42-add-thing" {
			found = true
		}
	}
	if !found {
		t.Errorf("artifacts = %v, want a %q entry", getContract.Artifacts, "branch:feature/42-add-thing")
	}
}

// -- the crux: cross-worktree state continuity ------------------------------

// TestPipelineCrossWorktreeContinuity_MainCheckoutAndLinkedWorktreeShareState
// is ticket #559's core requirement, exercised end to end against the real
// `cenci` binary and a real linked git worktree (no --repo/--state-dir
// overrides for the worktree-side calls -- this specifically exercises real
// cwd-based repo-root resolution, the exact mechanism the fix changes).
//
// It also pins the "authoritative hard-stop" requirement named in the
// ticket: `review` on a completely unprepared ticket (no prior `prepare` at
// all) must fail hard -- exit 1, non-empty errors[] -- not be silently
// tolerated. This is watch-level CLI behavior; docs/plan-fidelity.md rule
// #558 covers the *flow*-layer prose that currently downgrades
// review/finalize failures to warnings because of today's cross-worktree
// visibility bug -- fixing that bug here does not, by itself, change that
// prose (a later phase's job), but it does make the underlying watch-level
// hard-stop trustworthy again, which this test locks in.
func TestPipelineCrossWorktreeContinuity_MainCheckoutAndLinkedWorktreeShareState(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)

	mainRepo := t.TempDir()
	initGitRepoForCLITest(t, mainRepo)
	if out, err := exec.Command("git", "-C", mainRepo, "config", "user.email", "test@example.com").CombinedOutput(); err != nil {
		t.Fatalf("git config user.email: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", mainRepo, "config", "user.name", "Test").CombinedOutput(); err != nil {
		t.Fatalf("git config user.name: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", mainRepo, "commit", "--allow-empty", "-q", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit --allow-empty: %v\n%s", err, out)
	}

	// -- authoritative hard-stop: review before any prior prepare --------
	{
		cmd := exec.Command(binaryPath, "pipeline", "review", "42")
		cmd.Dir = mainRepo
		cmd.Env = pipelineEnv(fakeDir)
		output, err := cmd.Output()
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("review before prepare: expected *exec.ExitError, got %T: %v\n%s", err, err, output)
		}
		if exitErr.ExitCode() != 1 {
			t.Errorf("review before prepare: exit = %d, want 1 (domain-error hard stop)", exitErr.ExitCode())
		}
		var c pipelineContract
		if jerr := json.Unmarshal(output, &c); jerr != nil {
			t.Fatalf("review before prepare: stdout is not valid JSON: %v\n%s", jerr, output)
		}
		if len(c.Errors) == 0 {
			t.Error("review before prepare: errors = [], want at least one populated error")
		}
	}

	runAtMain := func(args ...string) pipelineContract {
		t.Helper()
		cmd := exec.Command(binaryPath, append([]string{"pipeline"}, args...)...)
		cmd.Dir = mainRepo
		cmd.Env = pipelineEnv(fakeDir)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("pipeline %v (main checkout): unexpected error: %v\n%s", args, err, output)
		}
		var c pipelineContract
		if jerr := json.Unmarshal(output, &c); jerr != nil {
			t.Fatalf("pipeline %v (main checkout): stdout is not valid JSON: %v\n%s", args, jerr, output)
		}
		return c
	}

	if c := runAtMain("prepare", "42"); c.State != "prepared" {
		t.Fatalf("prepare: state = %q, want %q", c.State, "prepared")
	}
	if c := runAtMain("plan", "42"); c.State != "waiting_for_plan_approval" {
		t.Fatalf("plan: state = %q, want %q", c.State, "waiting_for_plan_approval")
	}
	if c := runAtMain("plan", "42", "--approve"); c.State != "plan_approved" {
		t.Fatalf("plan --approve: state = %q, want %q", c.State, "plan_approved")
	}
	if c := runAtMain("execute", "42"); c.State != "executed" {
		t.Fatalf("execute: state = %q, want %q", c.State, "executed")
	}

	mainStatePath := filepath.Join(mainRepo, ".cenci", "pipeline", "42.json")
	if _, err := os.Stat(mainStatePath); err != nil {
		t.Fatalf("expected state file at the main checkout's canonical path %s: %v", mainStatePath, err)
	}

	// -- switch to a REAL linked worktree for review/finalize -- the crux:
	// resolveRepoRoot must anchor on the MAIN checkout root, not the
	// worktree's own root, so review/finalize read/write the exact same
	// state file prepare/plan/execute just wrote above.
	worktreeDir := filepath.Join(mainRepo, ".worktrees", "42-continuity-e2e")
	if out, err := exec.Command("git", "-C", mainRepo, "worktree", "add", "-q", worktreeDir, "-b", "feature/42-continuity-e2e").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	runAtWorktree := func(args ...string) pipelineContract {
		t.Helper()
		cmd := exec.Command(binaryPath, append([]string{"pipeline"}, args...)...)
		cmd.Dir = worktreeDir
		cmd.Env = pipelineEnv(fakeDir)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("pipeline %v (linked worktree): unexpected error: %v\n%s", args, err, output)
		}
		var c pipelineContract
		if jerr := json.Unmarshal(output, &c); jerr != nil {
			t.Fatalf("pipeline %v (linked worktree): stdout is not valid JSON: %v\n%s", args, jerr, output)
		}
		return c
	}

	if c := runAtWorktree("review", "42"); c.State != "reviewed" {
		t.Fatalf("review (from linked worktree): state = %q, want \"reviewed\" -- if this is still \"executed\"/\"new\" or errors[] is populated, resolveRepoRoot resolved the worktree's own root instead of the main checkout's, so review read a different, empty state file", c.State)
	}
	if c := runAtWorktree("finalize", "42"); c.State != "finalized" {
		t.Fatalf("finalize (from linked worktree): state = %q, want %q", c.State, "finalized")
	}

	// The state file must have been written at the MAIN checkout's
	// canonical path throughout -- never a second, stray copy under the
	// linked worktree's own .cenci/pipeline/.
	raw, err := os.ReadFile(mainStatePath)
	if err != nil {
		t.Fatalf("expected final state at the main checkout's canonical path %s: %v", mainStatePath, err)
	}
	var probe map[string]any
	if jerr := json.Unmarshal(raw, &probe); jerr != nil {
		t.Fatalf("main checkout state file is not valid JSON: %v\n%s", jerr, raw)
	}
	if probe["stage"] != "finalized" {
		t.Errorf("main checkout state file stage = %v, want %q -- the linked-worktree review/finalize calls above must have updated THIS file, not a separate one", probe["stage"], "finalized")
	}

	strayPath := filepath.Join(worktreeDir, ".cenci", "pipeline", "42.json")
	if _, err := os.Stat(strayPath); !os.IsNotExist(err) {
		t.Errorf("found a stray state file at the linked worktree's own path %s -- review/finalize must anchor on the main checkout root, not the worktree's own root", strayPath)
	}
}
