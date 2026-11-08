package main

// Tests for `cenci dispatch approve <id> [--repo PATH]` (#1464): the exit-code
// contract (0 released or not-held, 1 refusal or runtime failure, 2 usage) and
// every approve edge case. They call dispatchApprove, the verb's exit-code
// returning entry point, directly: routing the `approve` token through
// runDispatch is wired in dispatch_cmd.go, which a different change owns.
//
// Each test pins CENCI_ATTENDED and its origin marker through the pipeline
// package's own constants; the developer sandbox these run in sets
// CENCI_ATTENDED=0 ambiently.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
)

func approveSetPosture(t *testing.T, attended string) {
	t.Helper()
	t.Setenv(pipeline.AttendedEnv, attended)
	t.Setenv(pipeline.AttendedOriginEnv, "")
}

const approvePlanContent = "---\nslug: add-thing\nticketId: 42\n---\n" +
	"\n## Ticket Details\nsome details\n\n## Implementation Plan\ndo things\n\n## Architectural Context\nsome context\n"

// approveRepo returns a repo directory holding one valid plan for ticket 42.
func approveRepo(t *testing.T) (repo, planPath string) {
	t.Helper()
	repo = t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".plans"), 0o755); err != nil {
		t.Fatalf("mkdir .plans: %v", err)
	}
	planPath = filepath.Join(repo, ".plans", "42-add-thing.md")
	if err := os.WriteFile(planPath, []byte(approvePlanContent), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return repo, planPath
}

func approveStatePath(repo string) string {
	return filepath.Join(repo, ".cenci", "pipeline", "42.json")
}

// approveSeedState writes a state file for ticket 42 whose authorization
// field is exactly authJSON ("" omits the field).
func approveSeedState(t *testing.T, repo, authJSON string) {
	t.Helper()
	body := `{"schemaVersion":2,"id":"42","stage":"waiting_for_plan_approval","updatedAt":"2026-01-01T00:00:00Z"`
	if authJSON != "" {
		body += `,"authorization":` + authJSON
	}
	body += "}"
	if err := os.MkdirAll(filepath.Dir(approveStatePath(repo)), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(approveStatePath(repo), []byte(body), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func approveAuthJSON(kind, hash, source string) string {
	s := `{"kind":"` + kind + `","planSha256":"` + hash + `","recordedAt":"2026-01-02T03:04:05Z"`
	if source != "" {
		s += `,"source":"` + source + `"`
	}
	return s + "}"
}

func approvePlanHash(t *testing.T, planPath string) string {
	t.Helper()
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func approveRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = dispatchApprove(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func approveReadState(t *testing.T, repo string) []byte {
	t.Helper()
	b, err := os.ReadFile(approveStatePath(repo))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	return b
}

func approveStateAuth(t *testing.T, repo string) map[string]any {
	t.Helper()
	var s struct {
		Authorization map[string]any `json:"authorization"`
	}
	if err := json.Unmarshal(approveReadState(t, repo), &s); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return s.Authorization
}

func approveAssertNothingCreated(t *testing.T, repo string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo, ".cenci")); !os.IsNotExist(err) {
		t.Errorf(".cenci stat err = %v, want it absent: nothing may be written on a refusal", err)
	}
}

func TestDispatchApprove_UsageErrors_Exit2WithNoSideEffects(t *testing.T) {
	approveSetPosture(t, "1")
	repo, _ := approveRepo(t)
	cases := []struct {
		name string
		args []string
	}{
		{"no id", nil},
		{"only flags", []string{"--repo", repo}},
		{"non-numeric id", []string{"abc", "--repo", repo}},
		{"extra positional", []string{"42", "43", "--repo", repo}},
		{"unknown flag", []string{"42", "--bogus", "--repo", repo}},
		{"--repo without a value", []string{"42", "--repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := approveRun(t, tc.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if !strings.Contains(stderr, "cenci dispatch approve: usage:") {
				t.Errorf("stderr = %q, want the one-line usage hint", stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			approveAssertNothingCreated(t, repo)
		})
	}
}

func TestDispatchApprove_AttendedReleasesTheExactPlan(t *testing.T) {
	approveSetPosture(t, "1")
	repo, planPath := approveRepo(t)
	hash := approvePlanHash(t, planPath)
	approveSeedState(t, repo, approveAuthJSON("held", hash, ""))

	code, stdout, stderr := approveRun(t, "42", "--repo", repo)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, planPath) {
		t.Errorf("stdout = %q, want the approved plan path %q", stdout, planPath)
	}
	if !strings.Contains(stdout, hash[:12]) {
		t.Errorf("stdout = %q, want the first 12 hex characters %q of the plan's SHA-256", stdout, hash[:12])
	}
	if strings.Contains(stdout, hash) {
		t.Errorf("stdout = %q, want only the 12-character prefix, never the full hash", stdout)
	}
	auth := approveStateAuth(t, repo)
	if auth["kind"] != "human" || auth["planSha256"] != hash || auth["source"] != "cli" {
		t.Errorf("authorization = %v, want kind human, planSha256 %s, source cli", auth, hash)
	}
	for k := range auth {
		if k != "kind" && k != "planSha256" && k != "recordedAt" && k != "source" {
			t.Errorf("authorization carries unexpected field %q (no usernames or hostnames are stored)", k)
		}
	}
}

func TestDispatchApprove_ResolvesTheRepoFromTheWorkingDirectory(t *testing.T) {
	approveSetPosture(t, "1")
	repo, planPath := approveRepo(t)
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Chdir(repo)

	code, stdout, stderr := approveRun(t, "42")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, ".plans/42-add-thing.md") {
		t.Errorf("stdout = %q, want the plan path", stdout)
	}
	if auth := approveStateAuth(t, repo); auth["kind"] != "human" || auth["planSha256"] != approvePlanHash(t, planPath) {
		t.Errorf("authorization = %v, want human for the plan", auth)
	}
}

func TestDispatchApprove_ChangedPlanIsReleasedAgainByAFreshApproval(t *testing.T) {
	approveSetPosture(t, "1")
	repo, planPath := approveRepo(t)
	approveSeedState(t, repo, approveAuthJSON("held", approvePlanHash(t, planPath), ""))
	if code, _, stderr := approveRun(t, "42", "--repo", repo); code != 0 {
		t.Fatalf("first approve: exit %d\n%s", code, stderr)
	}
	first := approveStateAuth(t, repo)["planSha256"]

	if err := os.WriteFile(planPath, []byte(approvePlanContent+"\nedited\n"), 0o644); err != nil {
		t.Fatalf("edit plan: %v", err)
	}
	newHash := approvePlanHash(t, planPath)
	code, stdout, stderr := approveRun(t, "42", "--repo", repo)
	if code != 0 {
		t.Fatalf("second approve: exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, newHash[:12]) {
		t.Errorf("stdout = %q, want the new hash prefix %q", stdout, newHash[:12])
	}
	if got := approveStateAuth(t, repo)["planSha256"]; got == first || got != newHash {
		t.Errorf("planSha256 = %v, want the new plan hash %s (was %v)", got, newHash, first)
	}
	if !strings.Contains(stdout, "human") {
		t.Errorf("stdout = %q, want the replaced human record named", stdout)
	}
}

func TestDispatchApprove_UnattendedIsRefusedAndLeavesStateByteIdentical(t *testing.T) {
	approveSetPosture(t, "0")
	repo, planPath := approveRepo(t)
	approveSeedState(t, repo, approveAuthJSON("held", approvePlanHash(t, planPath), ""))
	before := approveReadState(t, repo)

	code, stdout, stderr := approveRun(t, "42", "--repo", repo)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "attended session") {
		t.Errorf("stderr = %q, want a message saying approval requires an attended session", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if after := approveReadState(t, repo); !bytes.Equal(before, after) {
		t.Errorf("state changed on an unattended approve:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestDispatchApprove_UnattendedWithNoStateCreatesNothing(t *testing.T) {
	approveSetPosture(t, "0")
	repo, _ := approveRepo(t)
	if code, _, _ := approveRun(t, "42", "--repo", repo); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	approveAssertNothingCreated(t, repo)
}

func TestDispatchApprove_HostConfigSandboxSessionCountsAsAttended(t *testing.T) {
	t.Setenv(pipeline.AttendedEnv, "0")
	t.Setenv(pipeline.AttendedOriginEnv, pipeline.AttendedOriginHostConfig)
	repo, planPath := approveRepo(t)
	approveSeedState(t, repo, approveAuthJSON("held", approvePlanHash(t, planPath), ""))

	if code, _, stderr := approveRun(t, "42", "--repo", repo); code != 0 {
		t.Fatalf("exit = %d, want 0 for a human `cenci open` sandbox session\n%s", code, stderr)
	}
}

func TestDispatchApprove_NotHeldKinds_PrintTheNoticeAndLeaveStateByteIdentical(t *testing.T) {
	approveSetPosture(t, "1")
	for _, kind := range []string{"lean", "trivial", "lean-resumed", "lean-decided", "human"} {
		t.Run(kind, func(t *testing.T) {
			repo, planPath := approveRepo(t)
			source := ""
			if kind == "human" {
				source = "cli"
			}
			approveSeedState(t, repo, approveAuthJSON(kind, approvePlanHash(t, planPath), source))
			before := approveReadState(t, repo)

			code, stdout, stderr := approveRun(t, "42", "--repo", repo)
			if code != 0 {
				t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
			}
			if want := "#42 is not awaiting human review (" + kind + ")\n"; stdout != want {
				t.Errorf("stdout = %q, want exactly %q", stdout, want)
			}
			if after := approveReadState(t, repo); !bytes.Equal(before, after) {
				t.Errorf("state changed for a not-held plan:\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestDispatchApprove_UnreadableRecord_IsOverwrittenAndReportsPathHashAndReplacedKind(t *testing.T) {
	approveSetPosture(t, "1")
	repo, planPath := approveRepo(t)
	hash := approvePlanHash(t, planPath)
	approveSeedState(t, repo, `{"kind":"bogus","planSha256":"nothex"}`)

	code, stdout, stderr := approveRun(t, "42", "--repo", repo)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	for _, want := range []string{planPath, hash[:12], "bogus"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if auth := approveStateAuth(t, repo); auth["kind"] != "human" || auth["planSha256"] != hash {
		t.Errorf("authorization = %v, want the unreadable record overwritten with human for the plan", auth)
	}
}

func TestDispatchApprove_PlanFileEdgeCases_Exit1AndWriteNothing(t *testing.T) {
	approveSetPosture(t, "1")

	t.Run("no plan file", func(t *testing.T) {
		repo := t.TempDir()
		code, _, stderr := approveRun(t, "42", "--repo", repo)
		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(stderr, "no plan file for #42") {
			t.Errorf("stderr = %q, want %q", stderr, "no plan file for #42")
		}
		approveAssertNothingCreated(t, repo)
	})

	t.Run("two plan files name both paths", func(t *testing.T) {
		repo, planPath := approveRepo(t)
		second := filepath.Join(repo, ".plans", "42-add-thing-v2.md")
		if err := os.WriteFile(second, []byte(approvePlanContent), 0o644); err != nil {
			t.Fatalf("write second plan: %v", err)
		}
		code, _, stderr := approveRun(t, "42", "--repo", repo)
		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(stderr, planPath) || !strings.Contains(stderr, second) {
			t.Errorf("stderr = %q, want both plan paths named", stderr)
		}
		approveAssertNothingCreated(t, repo)
	})

	t.Run("awaiting-input draft", func(t *testing.T) {
		repo, planPath := approveRepo(t)
		draft := strings.Replace(approvePlanContent, "ticketId: 42\n", "ticketId: 42\nstatus: awaiting-input\n", 1)
		if err := os.WriteFile(planPath, []byte(draft), 0o644); err != nil {
			t.Fatalf("write draft: %v", err)
		}
		code, _, stderr := approveRun(t, "42", "--repo", repo)
		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(stderr, "answer the escalation on the ticket") {
			t.Errorf("stderr = %q, want it to say to answer the escalation on the ticket", stderr)
		}
		approveAssertNothingCreated(t, repo)
	})
}

func TestDispatchApprove_StateAccessFailures_Exit1AndWriteNothing(t *testing.T) {
	approveSetPosture(t, "1")

	t.Run("unreadable state file", func(t *testing.T) {
		repo, _ := approveRepo(t)
		if err := os.MkdirAll(filepath.Dir(approveStatePath(repo)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(approveStatePath(repo), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("corrupt state: %v", err)
		}
		before := approveReadState(t, repo)
		code, _, stderr := approveRun(t, "42", "--repo", repo)
		if code != 1 || stderr == "" {
			t.Errorf("exit = %d stderr = %q, want exit 1 with a reason", code, stderr)
		}
		if after := approveReadState(t, repo); !bytes.Equal(before, after) {
			t.Errorf("state changed: %s", after)
		}
	})

	t.Run("symlinked .cenci/pipeline level", func(t *testing.T) {
		repo, _ := approveRepo(t)
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".cenci"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(repo, ".cenci", "pipeline")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		code, _, stderr := approveRun(t, "42", "--repo", repo)
		if code != 1 || stderr == "" {
			t.Errorf("exit = %d stderr = %q, want exit 1 with a reason", code, stderr)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("the symlink target holds %d entries, want nothing written through it", len(entries))
		}
	})
}
