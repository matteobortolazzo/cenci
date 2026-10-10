package incident

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}
func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

type scenarioAgent struct {
	t         *testing.T
	calls     int
	uncertain bool
	alterTest bool
}

func (a *scenarioAgent) Run(ctx context.Context, req AgentRequest) (AgentResult, error) {
	a.calls++
	if a.uncertain {
		return AgentResult{Verdict: "review", Report: "Infrastructure cause uncertain; human investigation needed."}, nil
	}
	if req.Phase == "investigate" {
		return AgentResult{Verdict: "fix", Report: "Telemetry shows Value returned 1; expected 2 at deployed commit."}, nil
	}
	if req.Phase == "regression" {
		put(a.t, filepath.Join(req.Worktree, "value_test.go"), "package example\nimport \"testing\"\nfunc TestValue(t *testing.T){if Value()!=2 { t.Fatal(\"incident regression\") }}\n")
	}
	if req.Phase == "fix" {
		put(a.t, filepath.Join(req.Worktree, "value.go"), "package example\nfunc Value()int{return 2}\n")
		if a.alterTest {
			put(a.t, filepath.Join(req.Worktree, "value_test.go"), "package example\n")
		}
	}
	return AgentResult{Verdict: "fix", Report: "Behavioral regression and code correction."}, nil
}

type localChecks struct{}

func (localChecks) Run(ctx context.Context, dir string, argv []string) (string, error) {
	return command(ctx, dir, nil, nil, argv[0], argv[1:]...)
}

type telemetryFixture struct{}

func (telemetryFixture) Collect(context.Context, Incident, Resource) (string, error) {
	return `{"tables":[{"rows":[["Value returned 1"]]}]}`, nil
}

type publications struct {
	url             string
	created         int
	failAfterCreate bool
	body            string
}

func (p *publications) Find(context.Context, Resource, string) (string, error) { return p.url, nil }
func (p *publications) Draft(_ context.Context, _ Resource, _ string, body string) (string, error) {
	p.created++
	p.body = body
	p.url = "https://github.com/owner/repo/pull/7"
	if p.failAfterCreate {
		return "", errors.New("response lost after creation")
	}
	return p.url, nil
}

func pipelineFixture(t *testing.T) (Config, Incident, *Pipeline, *scenarioAgent, *publications) {
	t.Helper()
	c := testConfig(t)
	dir := c.Resources[0].Dir
	put(t, filepath.Join(dir, "value.go"), "package example\nfunc Value()int{return 1}\n")
	put(t, filepath.Join(dir, "go.mod"), "module example\ngo 1.25\n")
	put(t, filepath.Join(dir, "AGENTS.md"), "Use behavioral Go tests. Never merge or deploy.\n")
	put(t, filepath.Join(dir, "RUNBOOK.md"), "Investigate Value mismatches using telemetry.\n")
	put(t, filepath.Join(dir, ".cenci/config.json"), `{"guidanceLocation":"AGENTS.md"}`)
	gitTest(t, dir, "init", "-b", "main")
	gitTest(t, dir, "config", "user.email", "incident-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "Incident Test")
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-m", "deployed")
	sha := gitTest(t, dir, "rev-parse", "HEAD")
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitTest(t, dir, "clone", "--bare", dir, remote)
	gitTest(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	gitTest(t, dir, "config", "url."+remote+".insteadOf", "https://github.com/owner/repo.git")
	c.Resources[0].Deployments[0].Commit = sha
	c.Resources[0].Workflow = "Follow configured cenci implementation workflow."
	s := NewStore(c.StateDir)
	ingest(t, s, "pipeline-alert", "Fired", c)
	rows, _ := s.Claim(c, time.Now())
	r := rows[0]
	a := &scenarioAgent{t: t}
	p := &publications{}
	pipeline := &Pipeline{Config: c, Agent: a, Checks: localChecks{}, Telemetry: telemetryFixture{}, Publisher: p}
	return c, r, pipeline, a, p
}

func TestPipelineBehavioralRegressionAndDraft(t *testing.T) {
	c, r, p, a, pub := pipelineFixture(t)
	s := NewStore(c.StateDir)
	result, err := p.Execute(context.Background(), r, r.Resource, func() error { return s.MarkPublishing(r.Key) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "draft" || pub.created != 1 || a.calls != 3 {
		t.Fatalf("outcome %+v publications=%d jobs=%d", result, pub.created, a.calls)
	}
	for _, want := range []string{r.AlertID, r.DeployedCommit, "Telemetry", "regression failed before", "checks passed", "Human review"} {
		if !strings.Contains(pub.body, want) {
			t.Fatalf("draft missing %q: %s", want, pub.body)
		}
	}
	if got := gitTest(t, r.Resource.Dir, "show", "main:value.go"); !strings.Contains(got, "return 1") {
		t.Fatal("main checkout modified")
	}
	if got := gitTest(t, r.Resource.Dir, "ls-remote", "--heads", "origin", Branch(r.Key)); got == "" {
		t.Fatal("incident branch not pushed")
	}
	history := filepath.Join(c.StateDir, "artifacts", r.Key, "history.git")
	if got := gitTest(t, history, "status", "--porcelain"); !strings.Contains(got, "value.go") || !strings.Contains(got, "value_test.go") {
		t.Fatalf("container Git view cannot inspect candidate changes: %s", got)
	}
}

func TestFailedChecksAndUncertainCauseProduceReports(t *testing.T) {
	for _, kind := range []string{"failed-checks", "uncertain", "test-tampering"} {
		t.Run(kind, func(t *testing.T) {
			c, r, p, a, pub := pipelineFixture(t)
			if kind == "failed-checks" {
				r.Resource.Checks = [][]string{{"false"}}
			}
			if kind == "uncertain" {
				a.uncertain = true
			}
			if kind == "test-tampering" {
				a.alterTest = true
			}
			result, err := p.Execute(context.Background(), r, r.Resource, func() error { return NewStore(c.StateDir).MarkPublishing(r.Key) })
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "review" || result.Report == "" || pub.created != 0 {
				t.Fatalf("unsafe draft: %+v publications=%d", result, pub.created)
			}
			want := map[string]string{"failed-checks": "Required checks failed", "uncertain": "Infrastructure cause uncertain", "test-tampering": "Regression test changed during code fix"}[kind]
			if !strings.Contains(result.Report, want) {
				t.Fatalf("wrong review reason; want %q, got %s", want, result.Report)
			}
		})
	}
}

func TestRetryAfterPRCreationDoesNotReinvokeAgent(t *testing.T) {
	c, r, p, a, pub := pipelineFixture(t)
	pub.failAfterCreate = true
	s := NewStore(c.StateDir)
	_, err := p.Execute(context.Background(), r, r.Resource, func() error { return s.MarkPublishing(r.Key) })
	if err == nil || !strings.Contains(err.Error(), "response lost") {
		t.Fatalf("expected lost response: %v", err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Claim(c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("recovery lost job %+v", rows)
	}
	result, err := p.Execute(context.Background(), rows[0], r.Resource, func() error { return s.MarkPublishing(r.Key) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "draft" || pub.created != 1 || a.calls != 3 {
		t.Fatalf("duplicate creation %+v calls=%d publications=%d", result, a.calls, pub.created)
	}
}

type unavailableChecks struct{ runs int }

func (c *unavailableChecks) Run(context.Context, string, []string) (string, error) {
	c.runs++
	if c.runs == 1 {
		return "baseline passed", nil
	}
	return "", &CheckUnavailable{errors.New("container could not start")}
}
func TestRegressionRuntimeFailureCannotCountAsFailingTest(t *testing.T) {
	c, r, p, a, pub := pipelineFixture(t)
	p.Checks = &unavailableChecks{}
	_, err := p.Execute(context.Background(), r, r.Resource, func() error { return NewStore(c.StateDir).MarkPublishing(r.Key) })
	var unavailable *CheckUnavailable
	if !errors.As(err, &unavailable) || pub.created != 0 || a.calls != 2 {
		t.Fatalf("runtime failure treated as red test: %v publications=%d agent phases=%d", err, pub.created, a.calls)
	}
}

func TestPublicationReceiptRejectsChangedVerifiedHead(t *testing.T) {
	c, r, p, a, pub := pipelineFixture(t)
	pub.failAfterCreate = true
	s := NewStore(c.StateDir)
	if _, err := p.Execute(context.Background(), r, r.Resource, func() error { return s.MarkPublishing(r.Key) }); err == nil {
		t.Fatal("expected ambiguous publication")
	}
	pub.url = ""
	worktree := filepath.Join(r.Resource.Dir, ".worktrees", "incident-"+r.Key)
	put(t, filepath.Join(worktree, "human.txt"), "human intervention")
	gitTest(t, worktree, "add", "human.txt")
	gitTest(t, worktree, "commit", "-m", "human intervention")
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Claim(c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Execute(context.Background(), rows[0], r.Resource, func() error { return s.MarkPublishing(r.Key) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" || !strings.Contains(result.Report, "changed after verification") || pub.created != 1 || a.calls != 3 {
		t.Fatalf("unverified push: %+v", result)
	}
}

func TestAmbiguousCreationWithEmptyLookupCannotCreateSecondPR(t *testing.T) {
	c, r, p, a, pub := pipelineFixture(t)
	pub.failAfterCreate = true
	s := NewStore(c.StateDir)
	if _, err := p.Execute(context.Background(), r, r.Resource, func() error { return s.MarkPublishing(r.Key) }); err == nil {
		t.Fatal("expected lost create response")
	}
	// An empty inventory is not proof the prior write failed: simulate delayed
	// visibility (or a quickly closed PR) after GitHub accepted the first create.
	pub.url = ""
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Claim(c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Execute(context.Background(), rows[0], r.Resource, func() error { return s.MarkPublishing(r.Key) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" || !strings.Contains(result.Report, "No second create request") || pub.created != 1 || a.calls != 3 {
		t.Fatalf("ambiguous create retried: %+v publications=%d calls=%d", result, pub.created, a.calls)
	}
}

func TestCorruptPublicationReceiptRequiresHumanReview(t *testing.T) {
	c, r, p, a, pub := pipelineFixture(t)
	pub.failAfterCreate = true
	s := NewStore(c.StateDir)
	if _, err := p.Execute(context.Background(), r, r.Resource, func() error { return s.MarkPublishing(r.Key) }); err == nil {
		t.Fatal("expected ambiguous publication")
	}
	pub.url = ""
	put(t, filepath.Join(c.StateDir, "artifacts", r.Key, "publication.json"), `{"head":`)
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Claim(c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Execute(context.Background(), rows[0], r.Resource, func() error { return s.MarkPublishing(r.Key) })
	if err != nil || result.Status != "review" || !strings.Contains(result.Report, "receipt is corrupt") || pub.created != 1 || a.calls != 3 {
		t.Fatalf("corrupt receipt retried indefinitely: %+v %v", result, err)
	}
}

func TestReceiptWriteFailurePreservesPreviousVersion(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "publication.json")
	original := `{"head":"verified","createAttempted":false}`
	put(t, path, original)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0700) }()
	if err := durableFile(path, []byte(`{"head":"verified","createAttempted":true}`)); err == nil {
		t.Fatal("receipt replacement should fail when its directory is not writable")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != original {
		t.Fatalf("failed update damaged durable receipt: %s %v", b, err)
	}
}
