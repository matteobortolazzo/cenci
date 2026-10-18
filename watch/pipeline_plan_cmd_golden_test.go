package main_test

// Golden test for `cenci pipeline plan-check <id>` (ticket #1293, AC1):
// the additive `pipeline` object must leave every other byte of the output --
// and the exit code -- exactly as it was before the object existed, for every
// decision in the closed decision set. The expected strings below were
// captured from the unmodified binary (main, before any #1293 Go edit);
// repo/state temp paths are normalized to <REPO>/<STATE>. `pipeline` is the
// last Output field, so stripping `,"pipeline":{...}` recovers today's bytes.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pipelineObjectSuffix matches the trailing `,"pipeline":{...}` member plus
// the closing brace and newline of the top-level object. The pipeline object
// is flat (no nested braces).
var pipelineObjectSuffix = regexp.MustCompile(`,"pipeline":\{[^{}]*\}\}\n$`)

func TestPipelinePlanCheck_Golden_EveryDecisionIsByteIdenticalOutsidePipelineObject(t *testing.T) {
	const (
		createdAt = "2026-07-20T20:00:00Z"
		slug      = "add-thing"
	)

	cases := []struct {
		name     string
		decision string
		wantExit int
		extra    []string
		setup    func(t *testing.T, repoDir string)
		want     string
	}{
		{
			name:     "resume",
			decision: "resume",
			setup: func(t *testing.T, repoDir string) {
				gitCommitPlanFile(t, repoDir, "42", slug, gitHeadShaForCLITest(t, repoDir), createdAt)
			},
			want: "{\"state\":\"new\",\"next_actions\":[\"run `cenci pipeline prepare \\u003cid\\u003e` to begin\"],\"artifacts\":[\"<REPO>/.plans/42-add-thing.md\"],\"warnings\":[],\"errors\":[],\"decision\":\"resume\",\"plan\":{\"mode\":\"ticket\",\"slug\":\"add-thing\",\"ticketId\":42,\"isChild\":false,\"isLastChild\":false,\"parentId\":0,\"escalationNonce\":\"\",\"escalationCommentId\":0}}\n",
		},
		{
			name:     "stale",
			decision: "stale",
			setup: func(t *testing.T, repoDir string) {
				sha := gitHeadShaForCLITest(t, repoDir)
				gitCommitPlanFile(t, repoDir, "42", slug, sha, createdAt)
				commitFileForCLITest(t, repoDir, "watch/main.go", "watch change")
			},
			want: "{\"state\":\"new\",\"next_actions\":[\"run `cenci pipeline prepare \\u003cid\\u003e` to begin\"],\"artifacts\":[\"<REPO>/.plans/42-add-thing.md\"],\"warnings\":[],\"errors\":[],\"decision\":\"stale\",\"plan\":{\"mode\":\"ticket\",\"slug\":\"add-thing\",\"ticketId\":42,\"isChild\":false,\"isLastChild\":false,\"parentId\":0,\"escalationNonce\":\"\",\"escalationCommentId\":0}}\n",
		},
		{
			name:     "replan",
			decision: "replan",
			extra:    []string{"--replan-requested"},
			setup: func(t *testing.T, repoDir string) {
				gitCommitPlanFile(t, repoDir, "42", slug, gitHeadShaForCLITest(t, repoDir), createdAt)
			},
			want: "{\"state\":\"new\",\"next_actions\":[\"run `cenci pipeline prepare \\u003cid\\u003e` to begin\"],\"artifacts\":[\"<REPO>/.plans/42-add-thing.md\"],\"warnings\":[],\"errors\":[],\"decision\":\"replan\",\"plan\":{\"mode\":\"ticket\",\"slug\":\"add-thing\",\"ticketId\":42,\"isChild\":false,\"isLastChild\":false,\"parentId\":0,\"escalationNonce\":\"\",\"escalationCommentId\":0}}\n",
		},
		{
			name:     "awaiting-input",
			decision: "awaiting-input",
			setup: func(t *testing.T, repoDir string) {
				gitCommitPlanFileWithStatus(t, repoDir, "42", slug, gitHeadShaForCLITest(t, repoDir), createdAt, "awaiting-input")
			},
			want: "{\"state\":\"new\",\"next_actions\":[\"run `cenci pipeline prepare \\u003cid\\u003e` to begin\"],\"artifacts\":[\"<REPO>/.plans/42-add-thing.md\"],\"warnings\":[],\"errors\":[],\"decision\":\"awaiting-input\",\"plan\":{\"mode\":\"ticket\",\"slug\":\"add-thing\",\"ticketId\":42,\"isChild\":false,\"isLastChild\":false,\"parentId\":0,\"escalationNonce\":\"\",\"escalationCommentId\":0},\"draft_freshness\":\"fresh\"}\n",
		},
		{
			name:     "none",
			decision: "none",
			setup:    func(t *testing.T, repoDir string) {},
			want:     "{\"state\":\"new\",\"next_actions\":[\"run `cenci pipeline prepare \\u003cid\\u003e` to begin\"],\"artifacts\":[],\"warnings\":[],\"errors\":[\"no plan file found for ticket 42: plan file not found\"],\"decision\":\"none\"}\n",
		},
		{
			name:     "multiple",
			decision: "multiple",
			setup: func(t *testing.T, repoDir string) {
				sha := gitHeadShaForCLITest(t, repoDir)
				gitCommitPlanFile(t, repoDir, "42", slug, sha, createdAt)
				gitCommitPlanFile(t, repoDir, "42", slug+"-v2", sha, createdAt)
			},
			want: "{\"state\":\"new\",\"next_actions\":[\"run `cenci pipeline prepare \\u003cid\\u003e` to begin\"],\"artifacts\":[\"<REPO>/.plans/42-add-thing-v2.md\",\"<REPO>/.plans/42-add-thing.md\"],\"warnings\":[],\"errors\":[\"multiple plan files found for ticket 42: multiple plan files found\"],\"decision\":\"multiple\"}\n",
		},
		{
			name:     "unset",
			decision: "",
			wantExit: 1,
			setup: func(t *testing.T, repoDir string) {
				plansDir := filepath.Join(repoDir, ".plans")
				if err := os.MkdirAll(plansDir, 0o755); err != nil {
					t.Fatalf("mkdir .plans: %v", err)
				}
				if err := os.WriteFile(filepath.Join(plansDir, "42-"+slug+".md"), []byte("no front matter\n"), 0o644); err != nil {
					t.Fatalf("write malformed plan: %v", err)
				}
			},
			want: "{\"state\":\"new\",\"next_actions\":[\"run `cenci pipeline prepare \\u003cid\\u003e` to begin\"],\"artifacts\":[\"<REPO>/.plans/42-add-thing.md\"],\"warnings\":[],\"errors\":[\"plan file <REPO>/.plans/42-add-thing.md: missing or unterminated front matter: plan file malformed\"]}\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			writeFakeGhTicketView(t, fakeDir, "OPEN", "2026-07-20T19:00:00Z")
			repoDir := t.TempDir()
			stateDir := t.TempDir()
			initGitRepoWithCommitForCLITest(t, repoDir)
			tc.setup(t, repoDir)

			args := append([]string{"pipeline", "plan-check", "42", "--repo", repoDir, "--state-dir", stateDir, "--repo-slug", "o/r"}, tc.extra...)
			cmd := exec.Command(binaryPath, args...)
			cmd.Env = pipelineEnv(fakeDir)
			stdout, err := cmd.Output()
			exit := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("plan-check: unexpected non-ExitError: %v", err)
				}
				exit = exitErr.ExitCode()
			}
			if exit != tc.wantExit {
				t.Errorf("exit code = %d, want %d\n%s", exit, tc.wantExit, stdout)
			}

			raw := string(stdout)
			var c struct {
				Decision string                     `json:"decision"`
				Pipeline map[string]json.RawMessage `json:"pipeline"`
			}
			if jerr := json.Unmarshal(stdout, &c); jerr != nil {
				t.Fatalf("stdout is not valid JSON: %v\n%s", jerr, raw)
			}
			if c.Decision != tc.decision {
				t.Errorf("decision = %q, want %q", c.Decision, tc.decision)
			}
			for _, key := range []string{"stage", "worktreeRegistered"} {
				if _, ok := c.Pipeline[key]; !ok {
					t.Errorf("pipeline object lacks key %q: %s", key, raw)
				}
			}

			if !pipelineObjectSuffix.MatchString(raw) {
				t.Fatalf("output does not end with a pipeline object: %s", raw)
			}
			stripped := pipelineObjectSuffix.ReplaceAllString(raw, "}\n")
			for _, dir := range []string{repoDir, stateDir} {
				placeholder := "<REPO>"
				if dir == stateDir {
					placeholder = "<STATE>"
				}
				stripped = strings.ReplaceAll(stripped, dir, placeholder)
				if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil {
					stripped = strings.ReplaceAll(stripped, resolved, placeholder)
				}
			}
			if stripped != tc.want {
				t.Errorf("output outside the pipeline object changed\n got: %q\nwant: %q", stripped, tc.want)
			}
		})
	}
}
