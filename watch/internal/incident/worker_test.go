package incident

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func alertBody(id, condition string) []byte {
	b, _ := json.Marshal(map[string]any{"schemaId": "azureMonitorCommonAlertSchema", "data": map[string]any{"essentials": map[string]any{"alertId": id, "monitorCondition": condition, "alertTargetIDs": []string{"/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/app"}, "firedDateTime": "2026-10-10T09:00:00Z"}}})
	return b
}

type stoppingJobs struct {
	calls                      atomic.Int32
	started, stopping, release chan struct{}
}

func (j *stoppingJobs) Execute(ctx context.Context, _ Incident, _ Resource, _ func() error) (Outcome, error) {
	if j.calls.Add(1) == 1 {
		close(j.started)
		<-ctx.Done()
		close(j.stopping)
		<-j.release
		return Outcome{}, ctx.Err()
	}
	return Outcome{Status: "review", Report: "Next incident investigated after cleanup."}, nil
}
func TestCancellationAndResolutionRetainCapacityUntilCleanup(t *testing.T) {
	for _, condition := range []string{"cancelled", "resolved"} {
		for _, repo := range []string{"same", "other"} {
			t.Run(condition+"-"+repo, func(t *testing.T) {
				c := testConfig(t)
				c.Concurrency = 1
				c.MaxAttempts = 1
				s := NewStore(c.StateDir)
				ingest(t, s, "a", "Fired", c)
				jobs := &stoppingJobs{started: make(chan struct{}), stopping: make(chan struct{}), release: make(chan struct{})}
				w := NewWorker(c, s, jobs)
				if err := w.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				<-jobs.started
				next := c
				next.Resources = append([]Resource{}, c.Resources...)
				if repo == "other" {
					next.Resources[0].Repo = "owner/other"
				}
				ingest(t, s, "b", "Fired", next)
				if condition == "cancelled" {
					if err := s.Cancel(Key("a")); err != nil {
						t.Fatal(err)
					}
				} else {
					ingest(t, s, "a", "Resolved", c)
				}
				select {
				case <-jobs.stopping:
				case <-time.After(time.Second):
					t.Fatal("job did not begin stopping")
				}
				if err := w.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				rows, _ := s.List()
				var premature bool
				for _, r := range rows {
					if r.Key == Key("b") && r.Attempts != 0 {
						premature = true
					}
				}
				close(jobs.release)
				w.Wait()
				if premature {
					t.Fatal("cancelled execution released capacity before cleanup finished")
				}
				if err := w.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				w.Wait()
				rows, _ = s.List()
				for _, r := range rows {
					if r.Key == Key("b") && (r.Attempts != 1 || r.Status != "review") {
						t.Fatalf("next incident lost after cleanup: %+v", r)
					}
				}
			})
		}
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{Enabled: true, StateDir: t.TempDir(), Concurrency: 2, TimeoutSeconds: 30, MaxAttempts: 2, BudgetUSD: 1,
		Resources: []Resource{{ID: "/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/app", Repo: "owner/repo", Dir: filepath.Join(t.TempDir(), "repo"), Base: "main", Environment: "production", Deployments: []Deployment{{Commit: strings.Repeat("a", 40), From: time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)}}, Runbooks: []string{"RUNBOOK.md"}, Regression: []string{"go", "test", "./..."}, Checks: [][]string{{"go", "test", "./..."}}, TestPaths: []string{"*_test.go"}}}}
}

type testJobs struct {
	mu    sync.Mutex
	calls int
	fail  bool
	block chan struct{}
}

func (j *testJobs) Execute(ctx context.Context, s Incident, r Resource, publish func() error) (Outcome, error) {
	j.mu.Lock()
	j.calls++
	j.mu.Unlock()
	if j.block != nil {
		select {
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		case <-j.block:
		}
	}
	if j.fail {
		return Outcome{}, errors.New("telemetry offline")
	}
	if err := publish(); err != nil {
		return Outcome{}, err
	}
	return Outcome{Status: "draft", PR: "https://github.com/owner/repo/pull/1", Report: "Evidence and verification"}, nil
}

func ingest(t *testing.T, s *Store, id, condition string, c Config) {
	t.Helper()
	if err := s.Accept(alertBody(id, condition), c); err != nil {
		t.Fatal(err)
	}
}

func TestDurableDeliveryAndRestart(t *testing.T) {
	c := testConfig(t)
	s := NewStore(c.StateDir)
	ingest(t, s, "alert-1", "Fired", c)
	ingest(t, s, "alert-1", "Fired", c)
	// Simulate crash after claiming: a new worker must recover the durable job.
	claimed, err := s.Claim(c, time.Now())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	restarted := NewStore(c.StateDir)
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	jobs := &testJobs{}
	w := NewWorker(c, restarted, jobs)
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.Wait()
	ingest(t, restarted, "alert-1", "Fired", c)
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.Wait()
	rows, err := restarted.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "draft" || rows[0].Attempts != 2 || rows[0].PR == "" || jobs.calls != 1 {
		t.Fatalf("duplicate/restart: %+v jobs=%d", rows, jobs.calls)
	}
}

func TestResolvedCancelledAndMissingDeployment(t *testing.T) {
	for _, tc := range []string{"resolved-before-fired", "resolved-after-queued", "cancelled", "missing-deployment"} {
		t.Run(tc, func(t *testing.T) {
			c := testConfig(t)
			s := NewStore(c.StateDir)
			want := "resolved"
			if tc == "resolved-before-fired" {
				ingest(t, s, "a", "Resolved", c)
			}
			if tc == "missing-deployment" {
				c.Resources[0].Deployments = nil
				want = "review"
			}
			ingest(t, s, "a", "Fired", c)
			if tc == "resolved-after-queued" {
				ingest(t, s, "a", "Resolved", c)
			}
			if tc == "cancelled" {
				want = "cancelled"
				if err := s.Cancel(Key("a")); err != nil {
					t.Fatal(err)
				}
			}
			jobs := &testJobs{}
			w := NewWorker(c, s, jobs)
			if err := w.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			w.Wait()
			rows, _ := s.List()
			if len(rows) != 1 || rows[0].Status != want || jobs.calls != 0 {
				t.Fatalf("got %+v calls=%d", rows, jobs.calls)
			}
			if want == "review" && !strings.Contains(rows[0].Report, "deployment") {
				t.Fatal("missing human report")
			}
		})
	}
}

func TestOneActivePerRepositoryAndBoundedConcurrency(t *testing.T) {
	c := testConfig(t)
	s := NewStore(c.StateDir)
	ingest(t, s, "a", "Fired", c)
	ingest(t, s, "b", "Fired", c)
	second := c
	second.Resources = append([]Resource{}, c.Resources...)
	second.Resources[0].Repo = "owner/other"
	second.Resources[0].Dir = "/other"
	ingest(t, s, "c", "Fired", second)
	claimed, err := s.Claim(c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 || claimed[0].Resource.Repo == claimed[1].Resource.Repo {
		t.Fatalf("claims %+v", claimed)
	}
	more, err := s.Claim(c, time.Now())
	if err != nil || len(more) != 0 {
		t.Fatalf("concurrency exceeded %+v %v", more, err)
	}
}

func TestBudgetAndRetryExhaustion(t *testing.T) {
	c := testConfig(t)
	s := NewStore(c.StateDir)
	ingest(t, s, "a", "Fired", c)
	jobs := &testJobs{fail: true}
	w := NewWorker(c, s, jobs)
	for i := 0; i < 3; i++ {
		if err := w.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.Wait()
		if err := s.Update(func(rows map[string]*Incident) error { rows[Key("a")].NextAt = time.Time{}; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := s.List()
	if rows[0].Status != "review" || rows[0].ReservedUSD != 2 || jobs.calls != 2 || !strings.Contains(rows[0].Report, "telemetry offline") {
		t.Fatalf("limits: %+v calls=%d", rows, jobs.calls)
	}
}

func TestPublishingCrashReconcilesEvenAfterLastAttempt(t *testing.T) {
	c := testConfig(t)
	c.MaxAttempts = 1
	s := NewStore(c.StateDir)
	ingest(t, s, "a", "Fired", c)
	_, err := s.Claim(c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublishing(Key("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	jobs := &testJobs{}
	w := NewWorker(c, s, jobs)
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.Wait()
	rows, _ := s.List()
	if rows[0].Status != "draft" || rows[0].Attempts != 1 {
		t.Fatalf("lost publication: %+v", rows)
	}
}

func TestCancellationStopsActiveJob(t *testing.T) {
	c := testConfig(t)
	s := NewStore(c.StateDir)
	ingest(t, s, "a", "Fired", c)
	jobs := &testJobs{block: make(chan struct{})}
	w := NewWorker(c, s, jobs)
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(Key("a")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { w.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("active cancellation did not stop job")
	}
	rows, _ := s.List()
	if rows[0].Status != "cancelled" || rows[0].PR != "" {
		t.Fatalf("cancellation overwritten %+v", rows)
	}
}
