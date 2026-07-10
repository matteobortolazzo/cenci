package dispatch

import (
	"bytes"
	"testing"
	"time"

	"github.com/matteobortolazzo/claude-tools/agentwatch/pkg/watch"
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
	applyReconcile(cfg, deps, mut, false, &buf, store)

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

	applyReconcile(reconcileConfig(), deps, mut, true, nil, store)

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
	applyReconcile(cfg, deadWorkingDeps(start), mut, false, nil, store)
	if len(mut.labelEdits) != 0 {
		t.Fatalf("pass 1 must not mutate, got %+v", mut.labelEdits)
	}
	if _, ok := store.obs["o/r#42"]; !ok {
		t.Fatal("pass 1 must persist the observation")
	}

	// Pass 2: 10m later, grace elapsed → retry mutation fires off the persisted observation.
	applyReconcile(cfg, deadWorkingDeps(start.Add(10*time.Minute)), mut, false, nil, store)
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
	applyReconcile(cfg, deps, mut, false, nil, store)

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

	res := applyReconcile(cfg, deps, mut, false, nil, store)

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
