package main_test

// Black-box CLI integration tests for `cenci pipeline plan-check <id>`
// (ticket #560): flag/usage-exit surface, JSON shape (decision/plan added
// alongside the existing {state, next_actions, artifacts, warnings, errors}
// contract), and the exit-code framing that deliberately diverges from
// every other mechanics verb's blanket "err != nil -> exit 1" -- decision
// "none"/"multiple" exit 0 despite a non-nil internal Go error and a
// populated errors[]; only decision "" (ErrPlanMalformed) exits 1. Runs the
// real built `cenci` binary as a subprocess (binaryPath, built once in
// TestMain in main_test.go), reusing pipeline_cmd_test.go's own
// pipelineEnv/writeFakeGh helpers since this file lives in the same
// `package main_test` (its own initGitRepoWithCommitForCLITest below is a
// local helper, not a reuse of pipeline_cmd_test.go's commit-less
// initGitRepoForCLITest -- these tests need an initial commit for
// gitHeadShaForCLITest's `git rev-parse HEAD` to resolve).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
)

// planCheckContract mirrors the JSON contract's field names exactly,
// intentionally NOT importing internal/pipeline's own Output type so this
// black-box test only cares about the wire format.
type planCheckContract struct {
	State       string   `json:"state"`
	NextActions []string `json:"next_actions"`
	Artifacts   []string `json:"artifacts"`
	Warnings    []string `json:"warnings"`
	Errors      []string `json:"errors"`
	Decision    string   `json:"decision"`
	Plan        *struct {
		Mode        string `json:"mode"`
		Slug        string `json:"slug"`
		TicketID    int    `json:"ticketId"`
		IsChild     bool   `json:"isChild"`
		IsLastChild bool   `json:"isLastChild"`
		ParentID    int    `json:"parentId"`
	} `json:"plan"`
}

// writeFakeGhTicketView writes a fake `gh` covering only `gh issue view <id>
// --json state,updatedAt [...]`, responding with the given state/updatedAt.
func writeFakeGhTicketView(t *testing.T, dir, state, updatedAt string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = \"issue\" ] && [ \"$2\" = \"view\" ]; then echo '{\"state\":\"" + state + "\",\"updatedAt\":\"" + updatedAt + "\"}'; exit 0; fi\n" +
		"echo \"unexpected gh invocation: $*\" 1>&2\n" +
		"exit 1\n"
	exectest.WriteExecutable(t, filepath.Join(dir, "gh"), body)
}

func gitCommitPlanFile(t *testing.T, repoRoot, id, slug, sha, createdAt string) {
	t.Helper()
	gitCommitPlanFileWithStatus(t, repoRoot, id, slug, sha, createdAt, "planned")
}

// gitCommitPlanFileWithStatus is gitCommitPlanFile's underlying writer, with
// the front-matter `status` value exposed so the awaiting-input decision
// test (#826) can override it away from the default "planned".
func gitCommitPlanFileWithStatus(t *testing.T, repoRoot, id, slug, sha, createdAt, status string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .plans: %v", err)
	}
	content := "---\n" +
		"version: 1\n" +
		"mode: ticket\n" +
		"ticketId: " + id + "\n" +
		"ticketTitle: \"Some ticket\"\n" +
		"slug: " + slug + "\n" +
		"isChild: false\n" +
		"isLastChild: false\n" +
		"parentId: null\n" +
		"createdAt: " + createdAt + "\n" +
		"status: " + status + "\n" +
		"planCommitSha: " + sha + "\n" +
		"stalenessPaths: watch\n" +
		"---\n" +
		"## Ticket Details\nsome ticket details\n\n" +
		"## Implementation Plan\ndo the thing\n\n" +
		"## Architectural Context\nsome architectural context\n\n" +
		"## Design Context\nsome design context\n"
	path := filepath.Join(dir, id+"-"+slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
}

func gitHeadShaForCLITest(t *testing.T, repoRoot string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func initGitRepoWithCommitForCLITest(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-q", "-m", "init")
}

// -- usage/exit-2 -----------------------------------------------------------

func TestPipelinePlanCheck_MissingID_Exit2(t *testing.T) {
	cmd := exec.Command(binaryPath, "pipeline", "plan-check")
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "cenci pipeline plan-check: usage:") {
		t.Errorf("output = %q, want it to contain the plan-check usage marker", output)
	}
	if strings.TrimSpace(string(output)) == "" {
		t.Fatal("expected non-empty usage output")
	}
}

func TestPipelinePlanCheck_UnknownFlag_Exit2(t *testing.T) {
	cmd := exec.Command(binaryPath, "pipeline", "plan-check", "42", "--bogus")
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

// -- decision "none": no plan file yet, exit 0 despite a populated errors[] -

func TestPipelinePlanCheck_NoPlanFile_DecisionNoneExit0(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoWithCommitForCLITest(t, repoDir)

	cmd := exec.Command(binaryPath, "pipeline", "plan-check", "42", "--repo", repoDir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("plan-check with no plan file: want exit 0, got error: %v\n%s", err, output)
	}
	var c planCheckContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", jerr, output)
	}
	if c.Decision != "none" {
		t.Errorf("decision = %q, want none", c.Decision)
	}
	if len(c.Errors) == 0 {
		t.Error("errors = [], want a populated errors[] naming the missing plan file (still exit 0 -- 'none' is a normal continuation branch, not a hard-stop domain error)")
	}
	if c.NextActions == nil || c.Artifacts == nil || c.Warnings == nil {
		t.Error("next_actions/artifacts/warnings must stay non-nil arrays")
	}
}

// -- decision "multiple": ambiguous, exit 0, both paths in artifacts[] -----

func TestPipelinePlanCheck_TwoMatches_DecisionMultipleExit0(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoWithCommitForCLITest(t, repoDir)
	sha := gitHeadShaForCLITest(t, repoDir)
	gitCommitPlanFile(t, repoDir, "42", "add-thing", sha, "2026-07-20T20:00:00Z")
	gitCommitPlanFile(t, repoDir, "42", "add-thing-v2", sha, "2026-07-20T20:00:00Z")

	cmd := exec.Command(binaryPath, "pipeline", "plan-check", "42", "--repo", repoDir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("plan-check with two plan files: want exit 0, got error: %v\n%s", err, output)
	}
	var c planCheckContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", jerr, output)
	}
	if c.Decision != "multiple" {
		t.Errorf("decision = %q, want multiple", c.Decision)
	}
	if len(c.Artifacts) != 2 {
		t.Errorf("artifacts = %v, want both matched plan paths", c.Artifacts)
	}
	if len(c.Errors) == 0 {
		t.Error("errors = [], want a populated errors[] naming the ambiguous match (still exit 0)")
	}
}

// -- decision "resume": happy path, fresh plan, exit 0, plan metadata echoed

func TestPipelinePlanCheck_ValidFreshPlan_DecisionResumeExit0(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeGhTicketView(t, fakeDir, "OPEN", "2026-07-20T19:00:00Z")
	repoDir := t.TempDir()
	initGitRepoWithCommitForCLITest(t, repoDir)
	sha := gitHeadShaForCLITest(t, repoDir)
	gitCommitPlanFile(t, repoDir, "42", "add-thing", sha, "2026-07-20T20:00:00Z")

	cmd := exec.Command(binaryPath, "pipeline", "plan-check", "42", "--repo", repoDir, "--repo-slug", "o/r")
	cmd.Env = pipelineEnv(fakeDir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("plan-check valid fresh plan: unexpected error: %v\n%s", err, output)
	}
	var c planCheckContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", jerr, output)
	}
	if c.Decision != "resume" {
		t.Errorf("decision = %q, want resume", c.Decision)
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on a clean resume", c.Errors)
	}
	if c.Plan == nil {
		t.Fatal("plan metadata must be echoed on a resume decision")
	}
	if c.Plan.Slug != "add-thing" || c.Plan.TicketID != 42 {
		t.Errorf("plan metadata = %+v, want slug=add-thing ticketId=42", c.Plan)
	}
}

// -- --replan-requested short-circuits to "replan", exit 0 -----------------

func TestPipelinePlanCheck_ReplanRequested_DecisionReplanExit0(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoWithCommitForCLITest(t, repoDir)
	gitCommitPlanFile(t, repoDir, "42", "add-thing", "deadbeef", "2026-07-20T20:00:00Z")

	cmd := exec.Command(binaryPath, "pipeline", "plan-check", "42", "--repo", repoDir, "--replan-requested")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("plan-check --replan-requested: unexpected error: %v\n%s", err, output)
	}
	var c planCheckContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", jerr, output)
	}
	if c.Decision != "replan" {
		t.Errorf("decision = %q, want replan", c.Decision)
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on replan", c.Errors)
	}
}

// -- decision "awaiting-input" (#826): exit 0, no errors[], zero gh calls --

// TestPipelinePlanCheck_AwaitingInputStatus_DecisionExit0 covers the new
// plan-check decision at the CLI surface: no fake `gh` on PATH at all --
// a real gh invocation would fail the subprocess outright, proving the
// awaiting-input short-circuit never reaches the freshness check.
func TestPipelinePlanCheck_AwaitingInputStatus_DecisionExit0(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoWithCommitForCLITest(t, repoDir)
	gitCommitPlanFileWithStatus(t, repoDir, "42", "add-thing", "deadbeef", "2026-07-20T20:00:00Z", "awaiting-input")

	cmd := exec.Command(binaryPath, "pipeline", "plan-check", "42", "--repo", repoDir)
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir()) // no gh on PATH at all
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("plan-check awaiting-input: unexpected error: %v\n%s", err, output)
	}
	var c planCheckContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", jerr, output)
	}
	if c.Decision != "awaiting-input" {
		t.Errorf("decision = %q, want awaiting-input", c.Decision)
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors = %v, want none on an awaiting-input decision", c.Errors)
	}
	if c.Plan == nil {
		t.Fatal("plan metadata must be echoed on an awaiting-input decision")
	}
	if c.Plan.Slug != "add-thing" || c.Plan.TicketID != 42 {
		t.Errorf("plan metadata = %+v, want slug=add-thing ticketId=42", c.Plan)
	}
}

// -- malformed plan: the one genuine domain error, exit 1 -------------------

func TestPipelinePlanCheck_MalformedPlan_Exit1(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoWithCommitForCLITest(t, repoDir)
	dir := filepath.Join(repoDir, ".plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .plans: %v", err)
	}
	// Missing front matter entirely.
	content := "## Ticket Details\nx\n## Implementation Plan\nx\n## Architectural Context\nx\n## Design Context\nx\n"
	if err := os.WriteFile(filepath.Join(dir, "42-add-thing.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}

	cmd := exec.Command(binaryPath, "pipeline", "plan-check", "42", "--repo", repoDir)
	output, err := cmd.Output()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("malformed plan: expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("malformed plan: exit = %d, want 1 (the one genuine domain error)", exitErr.ExitCode())
	}
	var c planCheckContract
	if jerr := json.Unmarshal(output, &c); jerr != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", jerr, output)
	}
	if c.Decision != "" {
		t.Errorf("decision = %q, want unset on a malformed plan", c.Decision)
	}
	if len(c.Errors) == 0 {
		t.Error("errors = [], want a populated errors[] naming the front-matter failure")
	}
}
