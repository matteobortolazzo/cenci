package main_test

// Black-box CLI integration tests for `cenci pipeline <stage> <id>` (ticket
// #558): the structured {state, next_actions, artifacts, warnings, errors}
// contract for all five stages, --approve, and exit codes 0/1/2 per
// docs/cli-conventions.md (domain errors: full contract on stdout + exit 1;
// malformed CLI: one-line stderr hint + exit 2). Runs the real built
// `cenci` binary as a subprocess (binaryPath, built once in TestMain in
// main_test.go), matching close_test.go's own convention, with a fake `gh`
// on PATH (exectest helpers) standing in for the one real op wired in this
// ticket (prepare's `gh issue view <id>`, Q&A #2).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
)

// pipelineContract mirrors the JSON contract's field names exactly
// (docs/cli-conventions.md + the plan's Structured Output Contract),
// intentionally NOT importing internal/pipeline's own Output type so this
// black-box test only cares about the wire format, not an internal Go type.
type pipelineContract struct {
	State       string   `json:"state"`
	NextActions []string `json:"next_actions"`
	Artifacts   []string `json:"artifacts"`
	Warnings    []string `json:"warnings"`
	Errors      []string `json:"errors"`
}

// writeFakeGh writes a fake `gh` to dir. When invoked as `gh issue view
// <id> ...`, it exits 0 with a minimal valid issue JSON if notFound is
// false, or exits 1 with gh's real "could not resolve" GraphQL error text
// if notFound is true (the ticket-not-found domain-error path).
func writeFakeGh(t *testing.T, dir string, notFound bool) {
	t.Helper()
	var body string
	if notFound {
		body = "#!/bin/sh\n" +
			"echo 'GraphQL: Could not resolve to an Issue with the number of 424242. (repository.issue)' 1>&2\n" +
			"exit 1\n"
	} else {
		body = "#!/bin/sh\n" +
			"echo '{\"number\":42,\"title\":\"Change\",\"state\":\"OPEN\"}'\n" +
			"exit 0\n"
	}
	exectest.WriteExecutable(t, filepath.Join(dir, "gh"), body)
}

// pipelineEnv returns a subprocess environment with fakeDir prepended to
// the inherited PATH so a fake `gh` resolves first, while everything else
// (git, sh, coreutils) still resolves normally off the real system PATH.
func pipelineEnv(fakeDir string) []string {
	return append(os.Environ(), "PATH="+fakeDir+":"+os.Getenv("PATH"))
}

func runPipelineCLI(t *testing.T, fakeDir string, args ...string) (pipelineContract, []byte, *exec.ExitError) {
	t.Helper()
	cmd := exec.Command(binaryPath, append([]string{"pipeline"}, args...)...)
	if fakeDir != "" {
		cmd.Env = pipelineEnv(fakeDir)
	}
	output, err := cmd.Output()
	var exitErr *exec.ExitError
	if err != nil {
		var ok bool
		exitErr, ok = err.(*exec.ExitError)
		if !ok {
			t.Fatalf("pipeline %v: unexpected non-ExitError: %v", args, err)
		}
	}
	var contract pipelineContract
	if len(output) > 0 {
		if jerr := json.Unmarshal(output, &contract); jerr != nil {
			t.Fatalf("pipeline %v: stdout is not valid JSON: %v\n%s", args, jerr, output)
		}
	}
	return contract, output, exitErr
}

func assertArraysNonNil(t *testing.T, stage string, c pipelineContract) {
	t.Helper()
	if c.NextActions == nil {
		t.Errorf("%s: next_actions = nil, want non-nil (possibly empty) array", stage)
	}
	if c.Artifacts == nil {
		t.Errorf("%s: artifacts = nil, want non-nil (possibly empty) array", stage)
	}
	if c.Warnings == nil {
		t.Errorf("%s: warnings = nil, want non-nil (possibly empty) array", stage)
	}
	if c.Errors == nil {
		t.Errorf("%s: errors = nil, want non-nil (possibly empty) array", stage)
	}
}

// -- structured output contract, exit 0 path -----------------------------

// TestPipelinePrepare_ContractAllArraysPresentExit0 covers AC1/AC2 for the
// first stage: prepare succeeds (fake gh confirms the ticket exists),
// exits 0, and the JSON contract's four arrays are always present.
func TestPipelinePrepare_ContractAllArraysPresentExit0(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	c, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("pipeline prepare: unexpected exit %d", exitErr.ExitCode())
	}
	if c.State != "prepared" {
		t.Errorf("state = %q, want %q", c.State, "prepared")
	}
	assertArraysNonNil(t, "prepare", c)
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on success", c.Errors)
	}
}

// TestPipelinePrepare_Idempotent_ReprepareSucceeds covers the plan's
// assumption that prepare is idempotent: running it twice for the same
// ticket must not error the second time.
func TestPipelinePrepare_Idempotent_ReprepareSucceeds(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	if _, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "42", "--state-dir", stateDir); exitErr != nil {
		t.Fatalf("first prepare: unexpected exit %d", exitErr.ExitCode())
	}
	c, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("second prepare: unexpected exit %d, want idempotent success", exitErr.ExitCode())
	}
	if c.State != "prepared" {
		t.Errorf("state = %q, want %q (idempotent re-run)", c.State, "prepared")
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on idempotent re-prepare", c.Errors)
	}
}

// TestPipelineFullValidSequence_AllFiveStagesExit0 drives prepare -> plan ->
// plan --approve -> execute -> review -> finalize end to end against the
// same ticket id and state dir, asserting the contract's `state` field
// advances exactly as the plan's state table describes, with all five
// stages tested (AC1).
func TestPipelineFullValidSequence_AllFiveStagesExit0(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	steps := []struct {
		args      []string
		wantState string
	}{
		{[]string{"prepare", "42"}, "prepared"},
		{[]string{"plan", "42"}, "waiting_for_plan_approval"},
		{[]string{"plan", "42", "--approve"}, "plan_approved"},
		{[]string{"execute", "42"}, "executed"},
		{[]string{"review", "42"}, "reviewed"},
		{[]string{"finalize", "42"}, "finalized"},
	}
	for _, step := range steps {
		args := append(append([]string{}, step.args...), "--state-dir", stateDir)
		c, _, exitErr := runPipelineCLI(t, fakeDir, args...)
		if exitErr != nil {
			t.Fatalf("%v: unexpected exit %d", step.args, exitErr.ExitCode())
		}
		if c.State != step.wantState {
			t.Fatalf("%v: state = %q, want %q", step.args, c.State, step.wantState)
		}
		assertArraysNonNil(t, strings.Join(step.args, " "), c)
		if len(c.Errors) != 0 {
			t.Errorf("%v: errors = %v, want none", step.args, c.Errors)
		}
	}
}

// -- domain errors: exit 1, full contract with errors[] -------------------

// TestPipelineExecute_BeforePlanApproved_Exit1WithErrors is the AC's key
// guard, exercised at the CLI surface: execute must not be reachable
// before plan_approved, and the failure must be a domain error (full JSON
// contract on stdout, errors[] populated, exit 1) — not a malformed-CLI
// exit 2.
func TestPipelineExecute_BeforePlanApproved_Exit1WithErrors(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	if _, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "42", "--state-dir", stateDir); exitErr != nil {
		t.Fatalf("prepare: unexpected exit %d", exitErr.ExitCode())
	}
	if _, _, exitErr := runPipelineCLI(t, fakeDir, "plan", "42", "--state-dir", stateDir); exitErr != nil {
		t.Fatalf("plan: unexpected exit %d", exitErr.ExitCode())
	}

	c, _, exitErr := runPipelineCLI(t, fakeDir, "execute", "42", "--state-dir", stateDir)
	if exitErr == nil {
		t.Fatal("execute before plan_approved: want a non-zero exit, got 0")
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("execute before plan_approved: exit = %d, want 1 (domain error)", exitErr.ExitCode())
	}
	assertArraysNonNil(t, "execute (blocked)", c)
	if len(c.Errors) == 0 {
		t.Error("execute before plan_approved: errors = [], want at least one populated error")
	}
	if c.State != "waiting_for_plan_approval" {
		t.Errorf("execute before plan_approved: state = %q, want unchanged %q", c.State, "waiting_for_plan_approval")
	}
}

// TestPipelinePlan_BeforePrepare_Exit1WithErrors covers "plan before
// prepare" from the plan's state table.
func TestPipelinePlan_BeforePrepare_Exit1WithErrors(t *testing.T) {
	fakeDir := t.TempDir()
	stateDir := t.TempDir()

	c, _, exitErr := runPipelineCLI(t, fakeDir, "plan", "42", "--state-dir", stateDir)
	if exitErr == nil {
		t.Fatal("plan before prepare: want a non-zero exit, got 0")
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("plan before prepare: exit = %d, want 1 (domain error)", exitErr.ExitCode())
	}
	assertArraysNonNil(t, "plan (blocked)", c)
	if len(c.Errors) == 0 {
		t.Error("plan before prepare: errors = [], want at least one populated error")
	}
}

// TestPipelineReview_BeforeExecute_Exit1WithErrors and
// TestPipelineFinalize_BeforeReview_Exit1WithErrors cover the remaining two
// "Invalid-from example (tested)" rows of the plan's state table.

func TestPipelineReview_BeforeExecute_Exit1WithErrors(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	for _, step := range [][]string{{"prepare", "42"}, {"plan", "42"}, {"plan", "42", "--approve"}} {
		args := append(append([]string{}, step...), "--state-dir", stateDir)
		if _, _, exitErr := runPipelineCLI(t, fakeDir, args...); exitErr != nil {
			t.Fatalf("%v: unexpected exit %d", step, exitErr.ExitCode())
		}
	}

	c, _, exitErr := runPipelineCLI(t, fakeDir, "review", "42", "--state-dir", stateDir)
	if exitErr == nil || exitErr.ExitCode() != 1 {
		t.Fatalf("review before execute: exit = %v, want 1 (domain error)", exitErr)
	}
	if len(c.Errors) == 0 {
		t.Error("review before execute: errors = [], want at least one populated error")
	}
}

func TestPipelineFinalize_BeforeReview_Exit1WithErrors(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	for _, step := range [][]string{{"prepare", "42"}, {"plan", "42"}, {"plan", "42", "--approve"}, {"execute", "42"}} {
		args := append(append([]string{}, step...), "--state-dir", stateDir)
		if _, _, exitErr := runPipelineCLI(t, fakeDir, args...); exitErr != nil {
			t.Fatalf("%v: unexpected exit %d", step, exitErr.ExitCode())
		}
	}

	c, _, exitErr := runPipelineCLI(t, fakeDir, "finalize", "42", "--state-dir", stateDir)
	if exitErr == nil || exitErr.ExitCode() != 1 {
		t.Fatalf("finalize before review: exit = %v, want 1 (domain error)", exitErr)
	}
	if len(c.Errors) == 0 {
		t.Error("finalize before review: errors = [], want at least one populated error")
	}
}

// TestPipelinePrepare_TicketNotFound_Exit1WithErrors covers the state
// table's "ticket-not-found -> errors[], exit 1" row: gh confirms the
// ticket does not exist, which must be a domain error, not a crash or a
// malformed-CLI exit 2.
func TestPipelinePrepare_TicketNotFound_Exit1WithErrors(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, true)
	stateDir := t.TempDir()

	c, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "424242", "--state-dir", stateDir)
	if exitErr == nil {
		t.Fatal("prepare with unknown ticket: want a non-zero exit, got 0")
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("prepare with unknown ticket: exit = %d, want 1 (domain error)", exitErr.ExitCode())
	}
	assertArraysNonNil(t, "prepare (ticket not found)", c)
	if len(c.Errors) == 0 {
		t.Error("prepare with unknown ticket: errors = [], want at least one populated error")
	}
	if c.State != "new" {
		t.Errorf("prepare with unknown ticket: state = %q, want unchanged %q (prepare never completed)", c.State, "new")
	}
}

// -- malformed CLI: exit 2, one-line stderr hint, no stdout JSON ---------
//
// usageMarker is the expected prefix of pipeline_cmd.go's own one-line
// usage hint (mirroring close_cmd.go's "cenci close: usage: ..." and
// babysit_cmd.go's "cenci babysit: usage: ..." pattern). Asserting on it
// (not just the exit code) matters: without it, every case below would
// coincidentally exit 2 today already via main.go's *generic*
// "unknown subcommand \"pipeline\"" fallback (since "pipeline" isn't a
// registered verb yet) even before pipeline_cmd.go exists — a false green
// that would prove nothing about pipeline's own flag parsing.
const usageMarker = "cenci pipeline: usage:"

func assertPipelineUsageExit2(t *testing.T, args ...string) {
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
	if !strings.Contains(string(output), usageMarker) {
		t.Errorf("%v: output = %q, want it to contain pipeline's own usage hint %q (not the generic unknown-subcommand fallback)", args, output, usageMarker)
	}
}

func TestPipeline_MissingID_Exit2(t *testing.T) {
	assertPipelineUsageExit2(t, "pipeline", "prepare")
}

func TestPipeline_NonNumericID_Exit2(t *testing.T) {
	assertPipelineUsageExit2(t, "pipeline", "prepare", "abc")
}

func TestPipeline_UnknownStage_Exit2(t *testing.T) {
	assertPipelineUsageExit2(t, "pipeline", "frobnicate", "42")
}

func TestPipeline_MissingStage_Exit2(t *testing.T) {
	assertPipelineUsageExit2(t, "pipeline")
}

func TestPipeline_TrailingUnexpectedArg_Exit2(t *testing.T) {
	assertPipelineUsageExit2(t, "pipeline", "prepare", "42", "unexpected")
}

// TestPipeline_ApproveOnNonPlanStage_Exit2 covers the CLI-grammar guard on
// --approve: it is meaningful only for `plan` (Q&A #1). Using it on any
// other stage is an unrecognized/conflicting flag for that stage, which
// docs/cli-conventions.md treats as a usage error (exit 2), not a domain
// error.
func TestPipeline_ApproveOnNonPlanStage_Exit2(t *testing.T) {
	assertPipelineUsageExit2(t, "pipeline", "execute", "42", "--approve")
}

// TestPipeline_MalformedCLI_ErrorsGoToStderrNotStdout locks in
// docs/cli-conventions.md's split: usage errors print a one-line hint to
// stderr and must never emit the stdout JSON contract (that's reserved for
// domain errors).
func TestPipeline_MalformedCLI_ErrorsGoToStderrNotStdout(t *testing.T) {
	cmd := exec.Command(binaryPath, "pipeline", "prepare", "abc")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()

	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("stdout = %q, want empty for a malformed-CLI usage error", stdout.String())
	}
	if !strings.Contains(stderr.String(), usageMarker) {
		t.Errorf("stderr = %q, want it to contain pipeline's own usage hint %q", stderr.String(), usageMarker)
	}
}

// -- state persisted at .cenci/pipeline/<id>.json (AC3), via --repo -------

// TestPipeline_StateFilePersistedAtCanonicalRepoPath proves the AC's exact
// on-disk location by using --repo (not --state-dir) to point at a real
// temp git repo, letting the command compute the canonical
// <repo>/.cenci/pipeline/<id>.json path itself.
func TestPipeline_StateFilePersistedAtCanonicalRepoPath(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	repoDir := t.TempDir()
	initGitRepoForCLITest(t, repoDir)

	if _, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "42", "--repo", repoDir); exitErr != nil {
		t.Fatalf("prepare --repo: unexpected exit %d", exitErr.ExitCode())
	}

	wantPath := filepath.Join(repoDir, ".cenci", "pipeline", "42.json")
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("expected state file at %s: %v", wantPath, err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("state file at %s is not valid JSON: %v\n%s", wantPath, err, raw)
	}
	if _, ok := probe["schemaVersion"]; !ok {
		t.Errorf("state file missing schemaVersion field: %s", raw)
	}
}

func initGitRepoForCLITest(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}
