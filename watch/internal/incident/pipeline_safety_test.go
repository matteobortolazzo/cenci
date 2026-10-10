package incident

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostGitDoesNotExecuteEmbeddedRepositoryFSMonitor(t *testing.T) {
	for _, operation := range []string{"add", "status"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			gitTest(t, dir, "init", "-b", "main")
			nested := filepath.Join(dir, "candidate")
			put(t, filepath.Join(nested, "source.txt"), "candidate source")
			gitTest(t, nested, "init", "-b", "main")
			gitTest(t, nested, "add", ".")
			gitTest(t, nested, "-c", "user.name=Incident Test", "-c", "user.email=incident@example.invalid", "commit", "-m", "candidate")
			marker := filepath.Join(t.TempDir(), "fsmonitor-executed")
			t.Setenv("CENCI_INCIDENT_FSMONITOR_MARKER", marker)
			hook := filepath.Join(nested, ".git", "candidate-fsmonitor")
			put(t, hook, "#!/bin/sh\nprintf executed > \"$CENCI_INCIDENT_FSMONITOR_MARKER\"\nprintf '\\000'\n")
			if err := os.Chmod(hook, 0700); err != nil {
				t.Fatal(err)
			}
			gitTest(t, nested, "config", "core.fsmonitor", hook)
			// The first add discovers the embedded repository; subsequent index
			// operations ask Git to inspect it using its own local configuration.
			if _, err := git(context.Background(), dir, "add", "-A"); err != nil {
				t.Fatal(err)
			}
			args := []string{"status", "--porcelain"}
			if operation == "add" {
				args = []string{"add", "-A"}
			}
			if _, err := git(context.Background(), dir, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("candidate-controlled fsmonitor ran on the host: %v", err)
			}
			// Positive control: prove this actual Git version reaches the hook.
			gitTest(t, dir, "-c", "core.hooksPath=/dev/null", "status", "--porcelain")
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("fixture did not exercise Git fsmonitor: %v", err)
			}
		})
	}
}

type investigationHandoffAgent struct{ scenario *scenarioAgent }

const handoffDiagnosis = "The incident is caused by Value returning 1 instead of the required 2."

func (a investigationHandoffAgent) Run(ctx context.Context, req AgentRequest) (AgentResult, error) {
	if req.Phase == "investigate" {
		return AgentResult{Verdict: "fix", Report: handoffDiagnosis}, nil
	}
	if req.Phase == "regression" && !strings.Contains(req.Evidence, handoffDiagnosis) {
		return AgentResult{Verdict: "review", Report: "The fresh regression agent did not receive the diagnosis."}, nil
	}
	return a.scenario.Run(ctx, req)
}

func TestFreshRegressionAgentReceivesInvestigation(t *testing.T) {
	c, r, p, a, pub := pipelineFixture(t)
	p.Agent = investigationHandoffAgent{a}
	result, err := p.Execute(context.Background(), r, r.Resource, func() error { return NewStore(c.StateDir).MarkPublishing(r.Key) })
	if err != nil || result.Status != "draft" || pub.url == "" {
		t.Fatalf("investigation did not reach the regression phase: %+v %v", result, err)
	}
}

func TestPublicationUnavailableWorktreeRequiresHumanReview(t *testing.T) {
	for _, damage := range []string{"missing-directory", "corrupt-git-pointer", "corrupt-head"} {
		t.Run(damage, func(t *testing.T) {
			c, r, p, a, pub := pipelineFixture(t)
			pub.failAfterCreate = true
			s := NewStore(c.StateDir)
			if _, err := p.Execute(context.Background(), r, r.Resource, func() error { return s.MarkPublishing(r.Key) }); err == nil {
				t.Fatal("expected ambiguous publication")
			}
			pub.url = ""
			worktree := filepath.Join(r.Resource.Dir, ".worktrees", "incident-"+r.Key)
			switch damage {
			case "missing-directory":
				if err := os.Rename(worktree, worktree+"-lost"); err != nil {
					t.Fatal(err)
				}
			case "corrupt-git-pointer":
				put(t, filepath.Join(worktree, ".git"), "gitdir: /nonexistent-incident-test-repository\n")
			case "corrupt-head":
				gitDir := gitTest(t, worktree, "rev-parse", "--absolute-git-dir")
				put(t, filepath.Join(gitDir, "HEAD"), "invalid-head\n")
			}
			if err := s.Recover(); err != nil {
				t.Fatal(err)
			}
			w := NewWorker(c, s, p)
			if err := w.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			w.Wait()
			rows, err := s.List()
			if err != nil || len(rows) != 1 || rows[0].Status != "review" || !strings.Contains(rows[0].Report, "worktree") || pub.created != 1 || a.calls != 3 {
				t.Fatalf("unrecoverable publication did not stop for review: %+v %v", rows, err)
			}
		})
	}
}

type cancellingLookup struct {
	*publications
	cancel context.CancelFunc
}

func (p cancellingLookup) Find(context.Context, Resource, string) (string, error) {
	p.cancel()
	return "", nil
}

func TestPublicationInterruptedHeadProbeRemainsRetryable(t *testing.T) {
	c, r, p, _, pub := pipelineFixture(t)
	pub.failAfterCreate = true
	s := NewStore(c.StateDir)
	if _, err := p.Execute(context.Background(), r, r.Resource, func() error { return s.MarkPublishing(r.Key) }); err == nil {
		t.Fatal("expected ambiguous publication")
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Claim(c, time.Now())
	if err != nil || len(rows) != 1 {
		t.Fatalf("claim publication: %+v %v", rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Publisher = cancellingLookup{pub, cancel}
	result, err := p.Execute(ctx, rows[0], r.Resource, func() error { return s.MarkPublishing(r.Key) })
	if !errors.Is(err, context.Canceled) || result.Status == "review" {
		t.Fatalf("interrupted probe was treated as permanent damage: %+v %v", result, err)
	}
}
