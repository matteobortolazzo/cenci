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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
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
//
// The three launch variables are stripped from the inherited environment: a
// cenci-launched dev sandbox sets CENCI_LAUNCH_ID ambiently, and `pipeline
// label --transition working` records whatever launch it finds (#1292), so an
// ambient value would silently change what every working-transition test
// writes. A test that wants a launch sets it explicitly on top of this.
//
// The attended posture is stripped for the same reason (#1464): the sandbox
// also sets CENCI_ATTENDED=0 ambiently, and `plan --approve` now reads it, so
// the baseline here is a human at the keyboard. A test that wants the
// unattended posture passes it through pipelineEnvWith.
func pipelineEnv(fakeDir string) []string {
	return pipelineEnvWith(fakeDir)
}

// pipelineEnvWith is pipelineEnv plus extra KEY=VALUE entries. An empty
// fakeDir leaves PATH as inherited.
func pipelineEnvWith(fakeDir string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CENCI_LAUNCH_ID=") || strings.HasPrefix(kv, "CENCI_SANDBOX_CONTAINER=") || strings.HasPrefix(kv, "CENCI_SANDBOX_RUNTIME=") ||
			strings.HasPrefix(kv, pipeline.AttendedEnv+"=") || strings.HasPrefix(kv, pipeline.AttendedOriginEnv+"=") {
			continue
		}
		env = append(env, kv)
	}
	if fakeDir != "" {
		env = append(env, "PATH="+fakeDir+":"+os.Getenv("PATH"))
	}
	return append(env, extra...)
}

func runPipelineCLI(t *testing.T, fakeDir string, args ...string) (pipelineContract, []byte, *exec.ExitError) {
	t.Helper()
	return runPipelineCLIWith(t, fakeDir, nil, args...)
}

// runPipelineCLIWith is runPipelineCLI with extra KEY=VALUE environment entries.
func runPipelineCLIWith(t *testing.T, fakeDir string, extraEnv []string, args ...string) (pipelineContract, []byte, *exec.ExitError) {
	t.Helper()
	cmd := exec.Command(binaryPath, append([]string{"pipeline"}, args...)...)
	cmd.Env = pipelineEnvWith(fakeDir, extraEnv...)
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
// ticket must not error the second time. Ticket #636: the second run is now
// a monotonic no-op specifically -- exit 0 (asserted via exitErr == nil,
// i.e. process exit code 0) with the no-op warning surfaced in the JSON
// contract's warnings[], not just a bare "no error" outcome.
func TestPipelinePrepare_Idempotent_ReprepareSucceeds(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	if _, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "42", "--state-dir", stateDir); exitErr != nil {
		t.Fatalf("first prepare: unexpected exit %d", exitErr.ExitCode())
	}
	c, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("second prepare: unexpected exit %d, want idempotent success (exit 0)", exitErr.ExitCode())
	}
	if c.State != "prepared" {
		t.Errorf("state = %q, want %q (idempotent re-run)", c.State, "prepared")
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on idempotent re-prepare", c.Errors)
	}
	if len(c.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one no-op warning on the idempotent re-prepare", c.Warnings)
	}
	wantWarning := `already at stage "prepared"; prepare is a no-op`
	if c.Warnings[0] != wantWarning {
		t.Errorf("warnings[0] = %q, want %q", c.Warnings[0], wantWarning)
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
	planRepo := planRepoForCLITest(t)

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
		args := append(append([]string{}, step.args...), "--state-dir", stateDir, "--repo", planRepo)
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

// -- await-input (#826): new stage command, dual-predecessor bare `plan` --

// TestPipelineAwaitInput_FromPrepared_ContractExit0 covers await-input's own
// forward transition at the CLI surface.
func TestPipelineAwaitInput_FromPrepared_ContractExit0(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	if _, _, exitErr := runPipelineCLI(t, fakeDir, "prepare", "42", "--state-dir", stateDir); exitErr != nil {
		t.Fatalf("prepare: unexpected exit %d", exitErr.ExitCode())
	}
	c, _, exitErr := runPipelineCLI(t, fakeDir, "await-input", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("await-input: unexpected exit %d", exitErr.ExitCode())
	}
	if c.State != "waiting_for_input" {
		t.Errorf("state = %q, want %q", c.State, "waiting_for_input")
	}
	assertArraysNonNil(t, "await-input", c)
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on success", c.Errors)
	}
}

// TestPipelineAwaitInput_BeforePrepared_Exit1WithErrors covers await-input's
// "too early" sentinel: reuses ErrNotPrepared, same failure class as bare
// `plan` (the plan's Assumptions).
func TestPipelineAwaitInput_BeforePrepared_Exit1WithErrors(t *testing.T) {
	fakeDir := t.TempDir()
	stateDir := t.TempDir()

	c, _, exitErr := runPipelineCLI(t, fakeDir, "await-input", "42", "--state-dir", stateDir)
	if exitErr == nil {
		t.Fatal("await-input before prepare: want a non-zero exit, got 0")
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("await-input before prepare: exit = %d, want 1 (domain error)", exitErr.ExitCode())
	}
	if len(c.Errors) == 0 {
		t.Error("await-input before prepare: errors = [], want at least one populated error")
	}
}

// TestPipelineAwaitInput_ApproveFlag_Exit2 covers the CLI-grammar guard:
// --approve is meaningful only for `plan`, so using it on await-input is a
// usage error (exit 2), like every other non-plan stage.
func TestPipelineAwaitInput_ApproveFlag_Exit2(t *testing.T) {
	assertPipelineUsageExit2(t, "pipeline", "await-input", "42", "--approve")
}

// TestPipelineBarePlan_FromWaitingForInput_ResumesToWaitingForPlanApproval
// drives the full escalation-resume sequence at the CLI surface: prepare ->
// await-input -> bare plan must land at waiting_for_plan_approval, exactly
// like the never-escalated prepare -> plan path -- the dual-predecessor rule
// end to end, not just the in-package transition() unit test.
func TestPipelineBarePlan_FromWaitingForInput_ResumesToWaitingForPlanApproval(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	for _, step := range [][]string{{"prepare", "42"}, {"await-input", "42"}} {
		args := append(append([]string{}, step...), "--state-dir", stateDir)
		if _, _, exitErr := runPipelineCLI(t, fakeDir, args...); exitErr != nil {
			t.Fatalf("%v: unexpected exit %d", step, exitErr.ExitCode())
		}
	}

	c, _, exitErr := runPipelineCLI(t, fakeDir, "plan", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("plan (resume from waiting_for_input): unexpected exit %d", exitErr.ExitCode())
	}
	if c.State != "waiting_for_plan_approval" {
		t.Errorf("state = %q, want %q", c.State, "waiting_for_plan_approval")
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on the resume path", c.Errors)
	}
}

// TestPipelineAwaitInput_ReEscalation_MonotonicNoOp covers the epic's
// "re-escalation is a monotonic no-op" requirement at the CLI surface: once
// bare `plan` has already advanced a ticket to waiting_for_plan_approval,
// calling await-input again must not rewind it and must surface the #636
// no-op warning.
func TestPipelineAwaitInput_ReEscalation_MonotonicNoOp(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()

	for _, step := range [][]string{{"prepare", "42"}, {"await-input", "42"}, {"plan", "42"}} {
		args := append(append([]string{}, step...), "--state-dir", stateDir)
		if _, _, exitErr := runPipelineCLI(t, fakeDir, args...); exitErr != nil {
			t.Fatalf("%v: unexpected exit %d", step, exitErr.ExitCode())
		}
	}

	c, _, exitErr := runPipelineCLI(t, fakeDir, "await-input", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("re-escalation: unexpected exit %d", exitErr.ExitCode())
	}
	if c.State != "waiting_for_plan_approval" {
		t.Errorf("state = %q, want unchanged %q (re-escalation must never rewind)", c.State, "waiting_for_plan_approval")
	}
	if len(c.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one no-op warning", c.Warnings)
	}
	wantWarning := `already at stage "waiting_for_plan_approval"; await-input is a no-op`
	if c.Warnings[0] != wantWarning {
		t.Errorf("warnings[0] = %q, want %q", c.Warnings[0], wantWarning)
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
	planRepo := planRepoForCLITest(t)

	for _, step := range [][]string{{"prepare", "42"}, {"plan", "42"}, {"plan", "42", "--approve"}} {
		args := append(append([]string{}, step...), "--state-dir", stateDir, "--repo", planRepo)
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
	planRepo := planRepoForCLITest(t)

	for _, step := range [][]string{{"prepare", "42"}, {"plan", "42"}, {"plan", "42", "--approve"}, {"execute", "42"}} {
		args := append(append([]string{}, step...), "--state-dir", stateDir, "--repo", planRepo)
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

// -- `cenci pipeline reset <id>` (#732) --------------------------------------

// TestPipelineReset_AfterFinalize_ContractShowsNewStateAndStageWarning drives
// prepare through finalize, then resets: state returns to "new", next_actions
// is non-empty (StageNew's guidance), artifacts is empty (the file is gone),
// no errors, and a warning names the stage it was reset from.
func TestPipelineReset_AfterFinalize_ContractShowsNewStateAndStageWarning(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()
	planRepo := planRepoForCLITest(t)

	for _, step := range [][]string{
		{"prepare", "42"}, {"plan", "42"}, {"plan", "42", "--approve"},
		{"execute", "42"}, {"review", "42"}, {"finalize", "42"},
	} {
		args := append(append([]string{}, step...), "--state-dir", stateDir, "--repo", planRepo)
		if _, _, exitErr := runPipelineCLI(t, fakeDir, args...); exitErr != nil {
			t.Fatalf("%v: unexpected exit %d", step, exitErr.ExitCode())
		}
	}

	c, _, exitErr := runPipelineCLI(t, fakeDir, "reset", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("pipeline reset: unexpected exit %d", exitErr.ExitCode())
	}
	if c.State != "new" {
		t.Errorf("state = %q, want %q", c.State, "new")
	}
	if len(c.NextActions) == 0 {
		t.Error("next_actions = [], want the StageNew guidance")
	}
	if len(c.Artifacts) != 0 {
		t.Errorf("artifacts = %v, want empty (the file is gone)", c.Artifacts)
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none", c.Errors)
	}
	foundStageWarning := false
	for _, w := range c.Warnings {
		if strings.Contains(w, `from stage "finalized"`) {
			foundStageWarning = true
		}
	}
	if !foundStageWarning {
		t.Errorf("warnings = %v, want one naming the reset stage %q", c.Warnings, "finalized")
	}
}

// TestPipelineReset_NoStateFile_IdempotentWarningExit0 covers the missing-
// state-file idempotent case.
func TestPipelineReset_NoStateFile_IdempotentWarningExit0(t *testing.T) {
	stateDir := t.TempDir()

	c, _, exitErr := runPipelineCLI(t, "", "reset", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("pipeline reset (no state): unexpected exit %d", exitErr.ExitCode())
	}
	if c.State != "new" {
		t.Errorf("state = %q, want %q", c.State, "new")
	}
	if len(c.Warnings) != 1 || c.Warnings[0] != "no pipeline state for 42; nothing to reset" {
		t.Errorf("warnings = %v, want the idempotent no-op warning", c.Warnings)
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none", c.Errors)
	}
}

// TestPipelineReset_CorruptFile_DecodeWarningExit0FileGone covers the
// corrupt-state-file recovery path.
func TestPipelineReset_CorruptFile_DecodeWarningExit0FileGone(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, _, exitErr := runPipelineCLI(t, "", "reset", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("pipeline reset (corrupt): unexpected exit %d", exitErr.ExitCode())
	}
	if c.State != "new" {
		t.Errorf("state = %q, want %q", c.State, "new")
	}
	found := false
	for _, w := range c.Warnings {
		if strings.Contains(w, "could not be decoded") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a decode-failure warning", c.Warnings)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("corrupt state file %s still exists, want it deleted", path)
	}
}

// TestPipelineReset_DeleteFails_Exit1WithErrorsPopulated covers the delete-
// failure domain-error path: full contract on stdout, errors[] populated,
// exit 1.
func TestPipelineReset_DeleteFails_Exit1WithErrorsPopulated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses Unix directory permission checks; cannot simulate a delete failure")
	}
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "42.json")
	seedPipelineState(t, path, "42", "finalized")
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	c, output, exitErr := runPipelineCLI(t, "", "reset", "42", "--state-dir", stateDir)
	if exitErr == nil {
		t.Fatal("pipeline reset with an undeletable state dir: want a non-zero exit, got 0")
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit = %d, want 1 (domain error)", exitErr.ExitCode())
	}
	if len(output) == 0 {
		t.Fatal("stdout is empty, want the full JSON contract even on a delete failure")
	}
	if len(c.Errors) == 0 {
		t.Error("errors = [], want the delete error populated")
	}
	if c.State != "finalized" {
		t.Errorf("state = %q, want %q (the stage still on disk)", c.State, "finalized")
	}
}

// -- malformed CLI: exit 2, reset's own usage hint, no stdout JSON ----------

func TestPipelineReset_MissingID_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline reset: usage:", "pipeline", "reset")
}

func TestPipelineReset_NonNumericID_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline reset: usage:", "pipeline", "reset", "abc")
}

func TestPipelineReset_UnknownFlag_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline reset: usage:", "pipeline", "reset", "42", "--bogus-flag")
}

func TestPipelineReset_TrailingPositional_Exit2(t *testing.T) {
	assertPipelineVerbUsageExit2(t, "cenci pipeline reset: usage:", "pipeline", "reset", "42", "unexpected")
}

// -- #588: empty arrays marshal as [], never null ----------------------------

// TestPipelineReset_NeverEmitsNullInJSON guards the stable JSON contract on
// reset's own output specifically (the idempotent no-op path, whose
// artifacts[] is always empty).
func TestPipelineReset_NeverEmitsNullInJSON(t *testing.T) {
	stateDir := t.TempDir()

	_, output, exitErr := runPipelineCLI(t, "", "reset", "42", "--state-dir", stateDir)
	if exitErr != nil {
		t.Fatalf("pipeline reset: unexpected exit %d", exitErr.ExitCode())
	}
	if strings.Contains(string(output), ":null") {
		t.Errorf("reset JSON contains \":null\": %s (#588: empty arrays must marshal as [], never null)", output)
	}
}

// -- ticket #688: plan-file-triggered stage adoption (closing #718 item 1) --

// writePlanFileForCLITest writes a minimal, validly-shaped
// `.plans/<id>-<slug>.md` under repoDir (all three sections adoptPlanFileStage's
// reused parseAndValidatePlan requires, plus a matching slug/ticketId). This
// file lives in package main_test, a separate package from
// internal/pipeline's own test fixtures (adopt_test.go's writePlanFile), so
// it cannot reuse those package-internal helpers directly.
func writePlanFileForCLITest(t *testing.T, repoDir, id, slug string) string {
	t.Helper()
	dir := filepath.Join(repoDir, ".plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .plans: %v", err)
	}
	path := filepath.Join(dir, id+"-"+slug+".md")
	content := "---\n" +
		"slug: " + slug + "\n" +
		"ticketId: " + id + "\n" +
		"---\n" +
		"\n## Ticket Details\nsome details\n\n" +
		"## Implementation Plan\ndo things\n\n" +
		"## Architectural Context\nsome context\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	return path
}

// TestPipelinePlanApprove_AdoptsPreStageTrackingPlan_Exit0WithWarning is the
// plan's case-13 CLI-level E2E: a ticket with a valid `.plans/<id>-*.md`
// file on disk but NO prior pipeline state file at all (the literal #718
// repro -- stage tracking predates or was deleted for this ticket) must
// still succeed on `plan --approve` via the real binary, landing at
// plan_approved with the adoption warning surfaced in warnings[]. Uses
// --repo (not --state-dir) so plan discovery and the canonical state path
// both resolve under the same real temp git repo, exactly like
// TestPipeline_StateFilePersistedAtCanonicalRepoPath above. No fake `gh` is
// installed: adoption is offline (adopt_test.go's own case 12 pins that the
// `command` seam is never invoked), so a real ambient PATH suffices.
func TestPipelinePlanApprove_AdoptsPreStageTrackingPlan_Exit0WithWarning(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoForCLITest(t, repoDir)
	writePlanFileForCLITest(t, repoDir, "42", "add-thing")

	c, _, exitErr := runPipelineCLI(t, "", "plan", "42", "--approve", "--repo", repoDir)
	if exitErr != nil {
		t.Fatalf("plan --approve (adoption): unexpected exit %d", exitErr.ExitCode())
	}
	if c.State != "plan_approved" {
		t.Errorf("state = %q, want %q", c.State, "plan_approved")
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on a successful adoption", c.Errors)
	}
	found := false
	for _, w := range c.Warnings {
		if strings.Contains(w, "adopted plan file") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want an adoption warning", c.Warnings)
	}
}

// -- plan authorization (#1464) -----------------------------------------------

const (
	unattendedEnv = pipeline.AttendedEnv + "=0"
	attendedEnv   = pipeline.AttendedEnv + "=1"
)

// planRepoForCLITest returns a directory holding one valid plan for ticket 42,
// for the `--repo` flag: plan --approve now resolves the plan to bind or check
// its authorization record.
func planRepoForCLITest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writePlanFileForCLITest(t, dir, "42", "add-thing")
	return dir
}

func cliPlanHash(t *testing.T, repoDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoDir, ".plans", "42-add-thing.md"))
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// cliAuthorization returns the decoded authorization field of ticket 42's
// state file, or nil when it carries none.
func cliAuthorization(t *testing.T, stateDir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "42.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var s struct {
		Authorization map[string]any `json:"authorization"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return s.Authorization
}

// cliStep runs one CLI step against the shared state dir and plan repo and
// fails the test on a non-zero exit.
func cliStep(t *testing.T, fakeDir string, env []string, stateDir, planRepo string, args ...string) pipelineContract {
	t.Helper()
	full := append(append([]string{}, args...), "--state-dir", stateDir, "--repo", planRepo)
	c, _, exitErr := runPipelineCLIWith(t, fakeDir, env, full...)
	if exitErr != nil {
		t.Fatalf("%v: unexpected exit %d (errors %v)", args, exitErr.ExitCode(), c.Errors)
	}
	return c
}

func TestPipelineAuthorization_UsageErrors_Exit2AndNothingIsWritten(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"human is writable only by dispatch approve or an attended launch", []string{"plan", "42", "--authorization", "human"}},
		{"unknown kind", []string{"plan", "42", "--authorization", "bogus"}},
		{"empty kind", []string{"plan", "42", "--authorization", ""}},
		{"missing value", []string{"plan", "42", "--authorization"}},
		{"together with --approve", []string{"plan", "42", "--approve", "--authorization", "lean"}},
		{"on prepare", []string{"prepare", "42", "--authorization", "lean"}},
		{"on await-input", []string{"await-input", "42", "--authorization", "lean"}},
		{"on execute", []string{"execute", "42", "--authorization", "lean"}},
		{"on review", []string{"review", "42", "--authorization", "lean"}},
		{"on finalize", []string{"finalize", "42", "--authorization", "lean"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			args := append(append([]string{"pipeline"}, tc.args...), "--state-dir", stateDir, "--repo", planRepoForCLITest(t))
			cmd := exec.Command(binaryPath, args...)
			cmd.Env = pipelineEnvWith("")
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("exit = %v, want 2", err)
			}
			if !strings.Contains(stderr.String(), usageMarker) {
				t.Errorf("stderr = %q, want pipeline's usage hint", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty for a usage error", stdout.String())
			}
			if entries, _ := os.ReadDir(stateDir); len(entries) != 0 {
				t.Errorf("state dir holds %d entries after a usage error, want none", len(entries))
			}
		})
	}
}

func TestPipelineAuthorization_UsageLineNamesTheFlag(t *testing.T) {
	cmd := exec.Command(binaryPath, "pipeline")
	cmd.Env = pipelineEnvWith("")
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "--authorization") {
		t.Errorf("usage = %q, want it to name --authorization", out)
	}
}

func TestPipelineAuthorization_EachKindIsRecordedBoundToThePlanHash(t *testing.T) {
	for _, kind := range []string{"held", "lean", "trivial", "lean-resumed", "lean-decided"} {
		t.Run(kind, func(t *testing.T) {
			fakeDir := t.TempDir()
			writeFakeGh(t, fakeDir, false)
			stateDir := t.TempDir()
			planRepo := planRepoForCLITest(t)

			cliStep(t, fakeDir, []string{unattendedEnv}, stateDir, planRepo, "prepare", "42")
			c := cliStep(t, fakeDir, []string{unattendedEnv}, stateDir, planRepo, "plan", "42", "--authorization", kind)
			if c.State != "waiting_for_plan_approval" {
				t.Errorf("state = %q, want waiting_for_plan_approval", c.State)
			}
			auth := cliAuthorization(t, stateDir)
			if auth["kind"] != kind || auth["planSha256"] != cliPlanHash(t, planRepo) {
				t.Errorf("authorization = %v, want kind %s bound to the plan's SHA-256", auth, kind)
			}
			if _, has := auth["source"]; has {
				t.Errorf("authorization = %v, want no source on a non-human kind", auth)
			}
		})
	}
}

func assertHeld(t *testing.T, c pipelineContract, exitErr *exec.ExitError, wantState string) {
	t.Helper()
	if exitErr == nil || exitErr.ExitCode() != 1 {
		t.Fatalf("plan --approve: exit = %v, want 1 (domain error)", exitErr)
	}
	if len(c.Errors) == 0 {
		t.Error("plan --approve refused with errors[] = [], want the reason")
	}
	if c.State != wantState {
		t.Errorf("state = %q, want %q", c.State, wantState)
	}
	assertArraysNonNil(t, "plan --approve (held)", c)
}

// TestPipelinePlanApprove_UnattendedHold_EachStartForm pins the execution-time
// hold for the three ways a session can reach Phase 2's `plan <id> --approve`.
// The binary cannot tell the forms apart (they differ only in how the state
// got there), so each is reproduced by the state it leaves behind. Every one
// must exit 1 with errors[] and leave the persisted stage untouched.
func TestPipelinePlanApprove_UnattendedHold_EachStartForm(t *testing.T) {
	unattended := []string{unattendedEnv}

	t.Run("plan-file path: held record from the New Plan fallback", func(t *testing.T) {
		fakeDir := t.TempDir()
		writeFakeGh(t, fakeDir, false)
		stateDir := t.TempDir()
		planRepo := planRepoForCLITest(t)
		cliStep(t, fakeDir, unattended, stateDir, planRepo, "prepare", "42")
		cliStep(t, fakeDir, unattended, stateDir, planRepo, "plan", "42", "--authorization", "held")

		c, _, exitErr := runPipelineCLIWith(t, fakeDir, unattended, "plan", "42", "--approve", "--state-dir", stateDir, "--repo", planRepo)
		assertHeld(t, c, exitErr, "waiting_for_plan_approval")
		if auth := cliAuthorization(t, stateDir); auth["kind"] != "held" {
			t.Errorf("authorization = %v, want it left held", auth)
		}
	})

	t.Run("bare numeric ticket: nothing recorded, the plan file alone is not a review", func(t *testing.T) {
		fakeDir := t.TempDir()
		writeFakeGh(t, fakeDir, false)
		stateDir := t.TempDir()
		planRepo := planRepoForCLITest(t)

		c, _, exitErr := runPipelineCLIWith(t, fakeDir, unattended, "plan", "42", "--approve", "--state-dir", stateDir, "--repo", planRepo)
		assertHeld(t, c, exitErr, "new")
		if _, err := os.Stat(filepath.Join(stateDir, "42.json")); !os.IsNotExist(err) {
			t.Errorf("state file stat err = %v, want none created by a refused self-adoption", err)
		}
	})

	t.Run("resume argument: human approval no longer matches the plan", func(t *testing.T) {
		fakeDir := t.TempDir()
		writeFakeGh(t, fakeDir, false)
		stateDir := t.TempDir()
		planRepo := planRepoForCLITest(t)
		cliStep(t, fakeDir, nil, stateDir, planRepo, "prepare", "42")
		cliStep(t, fakeDir, nil, stateDir, planRepo, "plan", "42", "--authorization", "held")
		cliStep(t, fakeDir, []string{attendedEnv}, stateDir, planRepo, "plan", "42", "--approve")
		// The session crashed after the approval; the stage is rolled back to
		// the review gate the way a re-plan leaves it, and the plan then changes.
		statePath := filepath.Join(stateDir, "42.json")
		raw, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		rolledBack := strings.Replace(string(raw), `"stage": "plan_approved"`, `"stage": "waiting_for_plan_approval"`, 1)
		if err := os.WriteFile(statePath, []byte(rolledBack), 0o644); err != nil {
			t.Fatalf("roll back stage: %v", err)
		}
		planPath := filepath.Join(planRepo, ".plans", "42-add-thing.md")
		edited, err := os.ReadFile(planPath)
		if err != nil {
			t.Fatalf("read plan: %v", err)
		}
		if err := os.WriteFile(planPath, append(edited, []byte("\nedited after approval\n")...), 0o644); err != nil {
			t.Fatalf("edit plan: %v", err)
		}

		c, _, exitErr := runPipelineCLIWith(t, fakeDir, unattended, "plan", "42", "--approve", "--state-dir", stateDir, "--repo", planRepo)
		assertHeld(t, c, exitErr, "waiting_for_plan_approval")
	})
}

// TestPipelinePlanApprove_UnattendedLegitimatePaths_ReachPlanApprovedInOneSession
// runs the four autonomous approval paths end to end in the unattended posture
// each: persist with the path's kind, then the execution-boundary approve.
func TestPipelinePlanApprove_UnattendedLegitimatePaths_ReachPlanApprovedInOneSession(t *testing.T) {
	for _, kind := range []string{"lean", "trivial", "lean-resumed", "lean-decided"} {
		t.Run(kind, func(t *testing.T) {
			fakeDir := t.TempDir()
			writeFakeGh(t, fakeDir, false)
			stateDir := t.TempDir()
			planRepo := planRepoForCLITest(t)
			env := []string{unattendedEnv}

			cliStep(t, fakeDir, env, stateDir, planRepo, "prepare", "42")
			cliStep(t, fakeDir, env, stateDir, planRepo, "plan", "42", "--authorization", kind)
			c := cliStep(t, fakeDir, env, stateDir, planRepo, "plan", "42", "--approve")
			if c.State != "plan_approved" || len(c.Errors) != 0 {
				t.Errorf("state = %q errors = %v, want plan_approved with none", c.State, c.Errors)
			}
			if auth := cliAuthorization(t, stateDir); auth["kind"] != kind {
				t.Errorf("authorization = %v, want the %s record left as the path wrote it", auth, kind)
			}
			if c := cliStep(t, fakeDir, env, stateDir, planRepo, "execute", "42"); c.State != "executed" {
				t.Errorf("execute: state = %q, want executed", c.State)
			}
		})
	}
}

func TestPipelinePlanApprove_UnattendedCannotDowngradeAHeldRecord(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()
	planRepo := planRepoForCLITest(t)
	env := []string{unattendedEnv}
	cliStep(t, fakeDir, env, stateDir, planRepo, "prepare", "42")
	cliStep(t, fakeDir, env, stateDir, planRepo, "plan", "42", "--authorization", "held")

	c, _, exitErr := runPipelineCLIWith(t, fakeDir, env, "plan", "42", "--authorization", "lean", "--state-dir", stateDir, "--repo", planRepo)
	if exitErr == nil || exitErr.ExitCode() != 1 || len(c.Errors) == 0 {
		t.Fatalf("exit = %v errors = %v, want exit 1 with errors[]", exitErr, c.Errors)
	}
	if auth := cliAuthorization(t, stateDir); auth["kind"] != "held" {
		t.Errorf("authorization = %v, want it left held", auth)
	}
}

func TestPipelinePlanApprove_AttendedAgainstHeldRecordsHumanAndAdvances(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGh(t, fakeDir, false)
	stateDir := t.TempDir()
	planRepo := planRepoForCLITest(t)
	cliStep(t, fakeDir, []string{unattendedEnv}, stateDir, planRepo, "prepare", "42")
	cliStep(t, fakeDir, []string{unattendedEnv}, stateDir, planRepo, "plan", "42", "--authorization", "held")

	c := cliStep(t, fakeDir, []string{attendedEnv}, stateDir, planRepo, "plan", "42", "--approve")
	if c.State != "plan_approved" {
		t.Errorf("state = %q, want plan_approved", c.State)
	}
	auth := cliAuthorization(t, stateDir)
	if auth["kind"] != "human" || auth["source"] != "attended-launch" || auth["planSha256"] != cliPlanHash(t, planRepo) {
		t.Errorf("authorization = %v, want human/attended-launch bound to the plan hash", auth)
	}
}
