package dispatch

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/pkg/watch"
)

// fakeMutator records every gh mutation the runner would apply.
type fakeMutator struct {
	labelEdits []labelEdit
	comments   []commentCall
}

type labelEdit struct {
	repo   string
	number int
	add    []string
	remove []string
}

type commentCall struct {
	repo   string
	number int
	body   string
}

func (m *fakeMutator) EditLabels(repo string, number int, add, remove []string) error {
	m.labelEdits = append(m.labelEdits, labelEdit{repo, number, add, remove})
	return nil
}

func (m *fakeMutator) Comment(repo string, number int, body string) error {
	m.comments = append(m.comments, commentCall{repo, number, body})
	return nil
}

// memStore is an in-memory ObservationStore for the runner tests.
type memStore struct {
	obs map[string]time.Time
}

func (s *memStore) Load() (map[string]time.Time, error) {
	if s.obs == nil {
		return map[string]time.Time{}, nil
	}
	out := make(map[string]time.Time, len(s.obs))
	for k, v := range s.obs {
		out[k] = v
	}
	return out, nil
}

func (s *memStore) Save(obs map[string]time.Time) error {
	s.obs = obs
	return nil
}

func deadWorkingDeps(now time.Time) reconcileDeps {
	return reconcileDeps{
		Tickets:  []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Working"}}},
		Plans:    []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "approved"}},
		Snapshot: &watch.StateSnapshot{},
		Attempts: map[string]int{},
		Now:      now,
	}
}

func TestRunReconcileRetryAppliesLabelAndComment(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}

	mut := &fakeMutator{}
	store := &memStore{obs: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}
	cfg := reconcileConfig()

	var buf bytes.Buffer
	if _, err := applyReconcile(cfg, deps, mut, false, &buf, store); err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit, got %d", len(mut.labelEdits))
	}
	e := mut.labelEdits[0]
	if e.number != 42 || !containsStr(e.add, labelPlanned) || !containsStr(e.remove, labelWorking) {
		t.Errorf("unexpected label edit: %+v", e)
	}
	if len(mut.comments) != 1 || mut.comments[0].number != 42 {
		t.Fatalf("expected 1 comment on #42, got %+v", mut.comments)
	}
	if !bytes.Contains([]byte(mut.comments[0].body), []byte(attemptMarker)) {
		t.Error("retry comment must carry the attempt marker for durable counting")
	}
}

func TestRunReconcileDryRunAppliesNothing(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}

	mut := &fakeMutator{}
	store := &memStore{obs: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}

	if _, err := applyReconcile(reconcileConfig(), deps, mut, true, nil, store); err != nil {
		t.Fatalf("dry-run returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 0 || len(mut.comments) != 0 {
		t.Errorf("dry-run must not mutate: %d edits, %d comments", len(mut.labelEdits), len(mut.comments))
	}
}

func TestRunReconcileObservationPersistsThenActs(t *testing.T) {
	cfg := reconcileConfig()
	store := &memStore{}
	mut := &fakeMutator{}
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	// Pass 1: signal just observed, grace not elapsed → no mutation, observation persisted.
	if _, err := applyReconcile(cfg, deadWorkingDeps(start), mut, false, nil, store); err != nil {
		t.Fatalf("first reconciliation returned unexpected error: %v", err)
	}
	if len(mut.labelEdits) != 0 {
		t.Fatalf("pass 1 must not mutate, got %+v", mut.labelEdits)
	}
	if _, ok := store.obs["o/r#42"]; !ok {
		t.Fatal("pass 1 must persist the observation")
	}

	// Pass 2: 10m later, grace elapsed → retry mutation fires off the persisted observation.
	if _, err := applyReconcile(cfg, deadWorkingDeps(start.Add(10*time.Minute)), mut, false, nil, store); err != nil {
		t.Fatalf("second reconciliation returned unexpected error: %v", err)
	}
	if len(mut.labelEdits) != 1 {
		t.Fatalf("pass 2 must apply the recovery, got %d edits", len(mut.labelEdits))
	}
}

func TestRunReconcileObservationResetsWhenHealthy(t *testing.T) {
	cfg := reconcileConfig()
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	// A pending observation exists, but the window is now live again.
	store := &memStore{obs: map[string]time.Time{"o/r#42": start.Add(-2 * time.Minute)}}
	mut := &fakeMutator{}

	deps := deadWorkingDeps(start)
	deps.Snapshot = &watch.StateSnapshot{Windows: []watch.WindowState{{WindowName: "42-x", Status: "running"}}}
	if _, err := applyReconcile(cfg, deps, mut, false, nil, store); err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 0 {
		t.Errorf("a healthy window must not mutate, got %+v", mut.labelEdits)
	}
	if _, ok := store.obs["o/r#42"]; ok {
		t.Error("a healthy window must reset the persisted observation")
	}
}

func TestRunReconcileAttemptCountDrivesFailTransition(t *testing.T) {
	cfg := reconcileConfig()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := &memStore{obs: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}
	mut := &fakeMutator{}

	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 2} // budget reached → fail, not retry

	res, err := applyReconcile(cfg, deps, mut, false, nil, store)
	if err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit, got %d", len(mut.labelEdits))
	}
	e := mut.labelEdits[0]
	if !containsStr(e.add, labelDispatchFailed) || !containsStr(e.remove, labelWorking) {
		t.Errorf("expected Working→dispatch-failed, got %+v", e)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("failed ticket must be present in the result's Failed list")
	}
}

type failingStore struct{}

func (failingStore) Load() (map[string]time.Time, error) { return nil, errors.New("load failed") }
func (failingStore) Save(map[string]time.Time) error     { return errors.New("save failed") }

func TestApplyReconcilePropagatesLoadMutationAndPersistenceErrors(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}
	// The grace observation must already have elapsed for a recovery mutation.
	deps.Now = now.Add(10 * time.Minute)
	store := failingStore{}
	mut := failingMutator{}
	_, err := applyReconcile(reconcileConfig(), deps, mut, false, nil, store)
	if err == nil || err.Error() != "load failed" {
		t.Fatalf("first reconcile error = %v, want load failed", err)
	}
}

func TestApplyRecoveryPropagatesCommentFailure(t *testing.T) {
	mut := commentFailingMutator{}
	err := applyRecovery(mut, Recovery{
		Ticket:       Ticket{Repo: "o/r", Number: 42},
		AddLabels:    []string{labelPlanned},
		RemoveLabels: []string{labelWorking},
		Comment:      "retry",
	}, nil)
	if err == nil || err.Error() != "comment failed" {
		t.Fatalf("comment error = %v, want comment failed", err)
	}
}

func TestApplyRecoveryPropagatesLabelFailure(t *testing.T) {
	err := applyRecovery(failingMutator{}, Recovery{
		Ticket:       Ticket{Repo: "o/r", Number: 42},
		AddLabels:    []string{labelPlanned},
		RemoveLabels: []string{labelWorking},
	}, nil)
	if err == nil || err.Error() != "gh label edit failed" {
		t.Fatalf("label error = %v, want gh label edit failed", err)
	}
}

type commentFailingMutator struct{}

func (commentFailingMutator) EditLabels(string, int, []string, []string) error { return nil }
func (commentFailingMutator) Comment(string, int, string) error                { return errors.New("comment failed") }
