package dispatch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// fakeMutator records every gh mutation the runner would apply, including the
// relative order the mutator's methods were called in (callOrder), so tests
// can assert ensure-then-add sequencing without depending on internals.
type fakeMutator struct {
	labelEdits  []labelEdit
	comments    []commentCall
	ensureCalls []ensureCall
	callOrder   []string // "ensure", "edit", "comment" in call order
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

type ensureCall struct {
	repo  string
	names []string
}

func (m *fakeMutator) EnsureLabels(repo string, names []string) error {
	m.ensureCalls = append(m.ensureCalls, ensureCall{repo, names})
	m.callOrder = append(m.callOrder, "ensure")
	return nil
}

func (m *fakeMutator) EditLabels(repo string, number int, add, remove []string) error {
	m.labelEdits = append(m.labelEdits, labelEdit{repo, number, add, remove})
	m.callOrder = append(m.callOrder, "edit")
	return nil
}

func (m *fakeMutator) Comment(repo string, number int, body string) error {
	m.comments = append(m.comments, commentCall{repo, number, body})
	m.callOrder = append(m.callOrder, "comment")
	return nil
}

// escalatingMutator fails every EditLabels call except the escalation
// mutation (adding reconcile-stuck), simulating a ticket whose ordinary
// recovery keeps failing to apply until the reconciler escalates it.
type escalatingMutator struct {
	labelEdits  []labelEdit
	comments    []commentCall
	ensureCalls []ensureCall
}

func (m *escalatingMutator) EnsureLabels(repo string, names []string) error {
	m.ensureCalls = append(m.ensureCalls, ensureCall{repo, names})
	return nil
}

func (m *escalatingMutator) Comment(repo string, number int, body string) error {
	m.comments = append(m.comments, commentCall{repo, number, body})
	return nil
}

func (m *escalatingMutator) EditLabels(repo string, number int, add, remove []string) error {
	m.labelEdits = append(m.labelEdits, labelEdit{repo, number, add, remove})
	if containsStr(add, labelReconcileStuck) {
		return nil
	}
	return errors.New("apply failed")
}

// memStore is an in-memory ReconcileStore for the runner tests.
type memStore struct {
	state ReconcileState
}

func (s *memStore) Load() (ReconcileState, error) {
	out := ReconcileState{Observations: map[string]time.Time{}, ApplyFailures: map[string]int{}}
	for k, v := range s.state.Observations {
		out.Observations[k] = v
	}
	for k, v := range s.state.ApplyFailures {
		out.ApplyFailures[k] = v
	}
	return out, nil
}

func (s *memStore) Save(state ReconcileState) error {
	s.state = state
	return nil
}

func deadWorkingDeps(now time.Time) reconcileDeps {
	return reconcileDeps{
		Tickets:  []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Working"}}},
		Plans:    []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "planned"}},
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
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}}
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
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}}

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
	if _, ok := store.state.Observations["o/r#42"]; !ok {
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
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": start.Add(-2 * time.Minute)}}}
	mut := &fakeMutator{}

	deps := deadWorkingDeps(start)
	deps.Snapshot = &watch.StateSnapshot{Windows: []watch.WindowState{{WindowName: "42-x", Status: "running"}}}
	if _, err := applyReconcile(cfg, deps, mut, false, nil, store); err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 0 {
		t.Errorf("a healthy window must not mutate, got %+v", mut.labelEdits)
	}
	if _, ok := store.state.Observations["o/r#42"]; ok {
		t.Error("a healthy window must reset the persisted observation")
	}
}

func TestRunReconcileAttemptCountDrivesFailTransition(t *testing.T) {
	cfg := reconcileConfig()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}}
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

func (failingStore) Load() (ReconcileState, error) {
	return ReconcileState{}, errors.New("load failed")
}
func (failingStore) Save(ReconcileState) error { return errors.New("save failed") }

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
	mutated, err := applyRecovery(mut, Recovery{
		Ticket:       Ticket{Repo: "o/r", Number: 42},
		AddLabels:    []string{labelPlanned},
		RemoveLabels: []string{labelWorking},
		Comment:      "retry",
	}, nil)
	if err == nil || err.Error() != "comment failed" {
		t.Fatalf("comment error = %v, want comment failed", err)
	}
	if !mutated {
		t.Error("a comment-only failure must still report mutated=true: the label swap landed on GitHub")
	}
}

func TestApplyRecoveryPropagatesLabelFailure(t *testing.T) {
	mutated, err := applyRecovery(failingMutator{}, Recovery{
		Ticket:       Ticket{Repo: "o/r", Number: 42},
		AddLabels:    []string{labelPlanned},
		RemoveLabels: []string{labelWorking},
	}, nil)
	if err == nil || err.Error() != "gh label edit failed" {
		t.Fatalf("label error = %v, want gh label edit failed", err)
	}
	if mutated {
		t.Error("a failed label edit must report mutated=false")
	}
}

type commentFailingMutator struct{}

func (commentFailingMutator) EditLabels(string, int, []string, []string) error { return nil }
func (commentFailingMutator) Comment(string, int, string) error                { return errors.New("comment failed") }
func (commentFailingMutator) EnsureLabels(string, []string) error              { return nil }

// TestApplyReconcilePreservesGraceOnFailedApply is the direct regression test
// for AC #3/#7: when applyRecovery's gh mutation fails, the original
// first-seen grace timestamp must survive into the next pass (not be dropped
// and restarted from Now), and the apply-failure counter must climb — this is
// the concrete bug behind the reported reconcile_pass_failed loop.
func TestApplyReconcilePreservesGraceOnFailedApply(t *testing.T) {
	cfg := reconcileConfig()
	mut := failingMutator{} // fails every EditLabels call
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	firstSeen := start.Add(-10 * time.Minute) // already past grace
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": firstSeen}}}

	deps := deadWorkingDeps(start)
	deps.Attempts = map[string]int{"o/r#42": 0}

	for i := 0; i < 2; i++ {
		deps.Now = start.Add(time.Duration(i) * time.Minute)
		if _, err := applyReconcile(cfg, deps, mut, false, nil, store); err == nil {
			t.Errorf("pass %d: expected the failed apply to surface an error", i+1)
		}
	}

	ts, ok := store.state.Observations["o/r#42"]
	if !ok || !ts.Equal(firstSeen) {
		t.Errorf("the grace clock must survive a failed apply with its original first-seen time preserved, got %v (present=%v), want %v", ts, ok, firstSeen)
	}
	if got := store.state.ApplyFailures["o/r#42"]; got < 2 {
		t.Errorf("ApplyFailures must climb across repeated failed applies, got %d, want >= 2", got)
	}
}

// TestApplyReconcileEscalatesToReconcileStuckAfterApplyRetryBudget is the
// direct regression test for AC #4/#5: once a ticket's apply keeps failing
// for cfg.ApplyRetryBudget consecutive passes, the reconciler must escalate
// it to the reconcile-stuck terminal label (distinct from dispatch-failed)
// instead of looping reconcile_pass_failed forever.
func TestApplyReconcileEscalatesToReconcileStuckAfterApplyRetryBudget(t *testing.T) {
	cfg := reconcileConfig()
	cfg.ApplyRetryBudget = 3
	mut := &escalatingMutator{}
	store := &memStore{}
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	firstSeen := start.Add(-10 * time.Minute)
	store.state = ReconcileState{Observations: map[string]time.Time{"o/r#42": firstSeen}}

	deps := deadWorkingDeps(start)
	deps.Attempts = map[string]int{"o/r#42": 0}

	// Passes below the budget are expected to surface an apply error each time
	// (TestApplyReconcilePreservesGraceOnFailedApply is the direct regression
	// test for that bounded-but-present error, AC #3/#7) — this test's own
	// contract (AC #4/#5) is that the pass which reaches the budget resolves
	// the ticket to reconcile-stuck and stops erroring, so only the final
	// pass's error is asserted here.
	var res ReconcileResult
	var lastErr error
	for i := 0; i < cfg.ApplyRetryBudget; i++ {
		deps.Now = start.Add(time.Duration(i) * time.Minute)
		res, lastErr = applyReconcile(cfg, deps, mut, false, nil, store)
	}
	if lastErr != nil {
		t.Errorf("the escalating pass must not surface an error once reconcile-stuck is successfully applied: %v", lastErr)
	}

	foundEscalation := false
	for _, e := range mut.labelEdits {
		if containsStr(e.add, labelReconcileStuck) && containsStr(e.remove, labelWorking) {
			foundEscalation = true
		}
	}
	if !foundEscalation {
		t.Errorf("expected an EditLabels call adding %q and removing %q after %d failed apply passes, got %+v",
			labelReconcileStuck, labelWorking, cfg.ApplyRetryBudget, mut.labelEdits)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("an escalated ticket must be surfaced in Failed for immediate badging")
	}
	if _, ok := store.state.Observations["o/r#42"]; ok {
		t.Error("the grace observation must be dropped once escalation succeeds")
	}
	if _, ok := store.state.ApplyFailures["o/r#42"]; ok {
		t.Error("the apply-failure counter must be dropped once escalation succeeds")
	}
}

// TestApplyReconcileEscalationDoesNotDuplicateFailedEntry is the direct
// regression test for code-review finding #1: unlike
// TestApplyReconcileEscalatesToReconcileStuckAfterApplyRetryBudget (which
// drives a RecoveryRetry apply failure — a kind the pure engine never adds to
// result.Failed on its own), this drives a RecoveryFailed apply failure
// (attempts already at RetryBudget). The pure engine unconditionally appends
// that ticket to result.Failed before applyReconcile ever attempts to apply
// it, so once the apply-retry budget is exhausted and escalation to
// reconcile-stuck succeeds, the ticket must appear exactly once in
// result.Failed — not twice.
func TestApplyReconcileEscalationDoesNotDuplicateFailedEntry(t *testing.T) {
	cfg := reconcileConfig()
	cfg.ApplyRetryBudget = 3
	mut := &escalatingMutator{}
	store := &memStore{}
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	firstSeen := start.Add(-10 * time.Minute)
	store.state = ReconcileState{Observations: map[string]time.Time{"o/r#42": firstSeen}}

	deps := deadWorkingDeps(start)
	// Attempts already at RetryBudget (2) → RecoveryFailed (Working→
	// dispatch-failed), which the pure engine appends to Failed unconditionally.
	deps.Attempts = map[string]int{"o/r#42": 2}

	var res ReconcileResult
	for i := 0; i < cfg.ApplyRetryBudget; i++ {
		deps.Now = start.Add(time.Duration(i) * time.Minute)
		res, _ = applyReconcile(cfg, deps, mut, false, nil, store)
	}

	count := 0
	for _, f := range res.Failed {
		if f.Repo == "o/r" && f.Number == 42 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 Failed entry for #42 after escalation succeeds, got %d: %+v", count, res.Failed)
	}
}

// TestApplyReconcileClearsStaleApplyFailuresWhenTicketBecomesHealthy is the
// direct regression test for code-review finding #2: applyFailures must be
// cleared for a ticket that produces no recovery at all this pass (healthy
// again), mirroring how the pure engine already drops Observations for a
// healthy ticket — otherwise a later, unrelated stranding episode for the same
// ticket would inherit the stale counter and escalate prematurely.
func TestApplyReconcileClearsStaleApplyFailuresWhenTicketBecomesHealthy(t *testing.T) {
	cfg := reconcileConfig()
	mut := failingMutator{} // fails every EditLabels call
	store := &memStore{}
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	firstSeen := start.Add(-10 * time.Minute)
	store.state = ReconcileState{Observations: map[string]time.Time{"o/r#42": firstSeen}}

	deps := deadWorkingDeps(start)
	deps.Attempts = map[string]int{"o/r#42": 0}

	// Pass 1: apply fails, applyFailures climbs.
	if _, err := applyReconcile(cfg, deps, mut, false, nil, store); err == nil {
		t.Fatal("expected pass 1's failed apply to surface an error")
	}
	if store.state.ApplyFailures["o/r#42"] == 0 {
		t.Fatalf("expected applyFailures to climb after a failed apply, got %+v", store.state.ApplyFailures)
	}

	// Pass 2: the window is live again → the ticket is healthy, no recovery at
	// all is produced for it this pass.
	deps2 := deadWorkingDeps(start.Add(time.Minute))
	deps2.Snapshot = &watch.StateSnapshot{Windows: []watch.WindowState{{WindowName: "42-x", Status: "running"}}}
	if _, err := applyReconcile(cfg, deps2, &fakeMutator{}, false, nil, store); err != nil {
		t.Fatalf("pass 2 returned unexpected error: %v", err)
	}

	if _, ok := store.state.ApplyFailures["o/r#42"]; ok {
		t.Errorf("a healthy ticket must clear its stale apply-failure counter, got %+v", store.state.ApplyFailures)
	}
}

// TestApplyReconcileDoesNotClearStaleApplyFailuresWhenTicketIsMerelyDeferred
// is the direct regression test for the follow-up fix to code-review finding
// #2: "no recovery this pass" is not by itself proof the ticket is healthy
// again. When the pure engine defers a verdict (here: a nil Snapshot, i.e.
// the daemon is unreachable) it deliberately carries the grace clock forward
// into NextObservations rather than dropping it — unlike a genuinely healthy
// ticket, which drops the observation entirely. The stale-clear loop must
// distinguish the two: it must not clear applyFailures for a merely-deferred
// ticket, or a ticket whose apply keeps failing could take far longer than
// cfg.ApplyRetryBudget passes to escalate (or never escalate) while outages
// and partial recoveries interleave.
func TestApplyReconcileDoesNotClearStaleApplyFailuresWhenTicketIsMerelyDeferred(t *testing.T) {
	cfg := reconcileConfig()
	mut := failingMutator{} // fails every EditLabels call
	store := &memStore{}
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	firstSeen := start.Add(-10 * time.Minute)
	store.state = ReconcileState{Observations: map[string]time.Time{"o/r#42": firstSeen}}

	deps := deadWorkingDeps(start)
	deps.Attempts = map[string]int{"o/r#42": 0}

	// Pass 1: apply fails, applyFailures climbs (same as the healthy-ticket
	// test's pass 1) — Working label is never actually removed since the
	// label edit failed, so the ticket is still Working going into pass 2.
	if _, err := applyReconcile(cfg, deps, mut, false, nil, store); err == nil {
		t.Fatal("expected pass 1's failed apply to surface an error")
	}
	if store.state.ApplyFailures["o/r#42"] == 0 {
		t.Fatalf("expected applyFailures to climb after a failed apply, got %+v", store.state.ApplyFailures)
	}

	// Pass 2: the daemon is unreachable (Snapshot == nil) — Reconcile defers
	// rather than deciding health, and produces no Recovery for the ticket at
	// all (indistinguishable, by recovery count alone, from "healthy").
	deps2 := deadWorkingDeps(start.Add(time.Minute))
	deps2.Snapshot = nil
	if _, err := applyReconcile(cfg, deps2, &fakeMutator{}, false, nil, store); err != nil {
		t.Fatalf("pass 2 returned unexpected error: %v", err)
	}

	if _, ok := store.state.ApplyFailures["o/r#42"]; !ok {
		t.Error("a merely-deferred ticket (daemon unreachable) must NOT clear its stale apply-failure counter")
	}
	if _, ok := store.state.Observations["o/r#42"]; !ok {
		t.Error("a merely-deferred ticket must carry its grace observation forward, not drop it")
	}
}

// TestApplyReconcileCommentOnlyFailureDoesNotBumpApplyFailuresOrReinjectGrace
// is the direct regression test for silent-failure-hunter finding #3: when
// only the trailing comment fails (the label mutation itself landed on
// GitHub), applyReconcile must not treat it as an unresolved apply failure —
// applyFailures must not climb and the grace observation must not be
// re-injected — even though the error still surfaces for that pass.
func TestApplyReconcileCommentOnlyFailureDoesNotBumpApplyFailuresOrReinjectGrace(t *testing.T) {
	cfg := reconcileConfig()
	mut := commentFailingMutator{} // EditLabels/EnsureLabels succeed, Comment fails
	store := &memStore{}
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	firstSeen := start.Add(-10 * time.Minute)
	store.state = ReconcileState{Observations: map[string]time.Time{"o/r#42": firstSeen}}

	deps := deadWorkingDeps(start)
	deps.Attempts = map[string]int{"o/r#42": 0}

	_, err := applyReconcile(cfg, deps, mut, false, nil, store)
	if err == nil {
		t.Fatal("a comment-only failure must still surface via the returned error")
	}

	if _, ok := store.state.ApplyFailures["o/r#42"]; ok {
		t.Errorf("a comment-only failure must not bump applyFailures (the label mutation succeeded), got %+v", store.state.ApplyFailures)
	}
	if _, ok := store.state.Observations["o/r#42"]; ok {
		t.Error("a comment-only failure must not re-inject the grace observation: the label mutation already landed and the ticket left Working")
	}
}

// TestApplyReconcileTerminalReconcileStuckProducesNoErrorOrMutation is the
// runner-level half of AC #6: a ticket already carrying reconcile-stuck must
// stop causing reconcile_pass_failed to resurface — no recovery, no gh call,
// no error.
func TestApplyReconcileTerminalReconcileStuckProducesNoErrorOrMutation(t *testing.T) {
	cfg := reconcileConfig()
	mut := &fakeMutator{}
	store := &memStore{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := reconcileDeps{
		Tickets:  []Ticket{{Repo: "o/r", Number: 42, Labels: []string{labelReconcileStuck}}},
		Snapshot: &watch.StateSnapshot{},
		Attempts: map[string]int{},
		Now:      now,
	}

	res, err := applyReconcile(cfg, deps, mut, false, nil, store)
	if err != nil {
		t.Fatalf("a terminal reconcile-stuck ticket must not resurface reconcile_pass_failed, got %v", err)
	}
	if len(res.Recoveries) != 0 {
		t.Errorf("a terminal ticket must produce no recovery, got %+v", res.Recoveries)
	}
	if len(mut.labelEdits) != 0 || len(mut.comments) != 0 {
		t.Errorf("a terminal ticket must never be mutated again, got edits=%+v comments=%+v", mut.labelEdits, mut.comments)
	}
}

// TestApplyReconcileEnsuresLabelsBeforeEditingOnFirstFailure is the direct
// regression test for AC #2: the first time a ticket needs a managed
// terminal label (e.g. dispatch-failed), the reconciler must ensure the
// label exists before attempting to add it, so a repo that never had the
// label pre-created doesn't hard-fail the gh mutation.
func TestApplyReconcileEnsuresLabelsBeforeEditingOnFirstFailure(t *testing.T) {
	cfg := reconcileConfig()
	mut := &fakeMutator{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}}

	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 2} // at budget → dispatch-failed, a label that may not yet exist

	if _, err := applyReconcile(cfg, deps, mut, false, nil, store); err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}

	if len(mut.ensureCalls) == 0 {
		t.Fatalf("expected EnsureLabels to be called before the first dispatch-failed label edit, got none (edits=%+v)", mut.labelEdits)
	}
	if len(mut.callOrder) == 0 || mut.callOrder[0] != "ensure" {
		t.Errorf("EnsureLabels must be called before EditLabels, got call order %v", mut.callOrder)
	}
	if !containsStr(mut.ensureCalls[0].names, labelDispatchFailed) {
		t.Errorf("expected %q among the ensured labels, got %v", labelDispatchFailed, mut.ensureCalls[0].names)
	}
}

// TestApplyReconcileSessionlessReachesDispatchFailedWithoutLooping is the
// runner-level regression test for AC #5: a ticket dispatched to a window
// that has since disappeared (no pane, no session) must reach dispatch-failed
// within a bounded number of passes when the mutator succeeds, never
// resurfacing reconcile_pass_failed along the way.
func TestApplyReconcileSessionlessReachesDispatchFailedWithoutLooping(t *testing.T) {
	cfg := reconcileConfig() // GracePeriod 5m, RetryBudget 2
	store := &memStore{}
	mut := &fakeMutator{}
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	// Pass 1: signal just observed, grace not elapsed → no mutation.
	deps := deadWorkingDeps(start)
	deps.Attempts = map[string]int{"o/r#42": 0}
	if _, err := applyReconcile(cfg, deps, mut, false, nil, store); err != nil {
		t.Fatalf("pass 1: unexpected error: %v", err)
	}
	if len(mut.labelEdits) != 0 {
		t.Fatalf("pass 1 must wait out grace, got %+v", mut.labelEdits)
	}

	// Pass 2: grace elapsed, 0 durable attempts so far → retry (Working→Planned).
	deps = deadWorkingDeps(start.Add(10 * time.Minute))
	deps.Attempts = map[string]int{"o/r#42": 0}
	res, err := applyReconcile(cfg, deps, mut, false, nil, store)
	if err != nil {
		t.Fatalf("pass 2: unexpected error: %v", err)
	}
	if len(mut.labelEdits) != 1 || !containsStr(mut.labelEdits[0].add, labelPlanned) {
		t.Fatalf("pass 2 must retry, got %+v", mut.labelEdits)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("pass 2 must not yet be terminal")
	}

	// Pass 3: the retry resolved the prior observation (pure-engine behavior:
	// the recovered ticket is assumed to have left Working), so the grace
	// clock legitimately restarts here — this harness re-presents the same
	// still-"Working" fixture ticket (no real dispatcher re-dispatches it),
	// so this pass is the fresh observation, not yet elapsed.
	deps = deadWorkingDeps(start.Add(10 * time.Minute))
	deps.Attempts = map[string]int{"o/r#42": 0}
	if _, err := applyReconcile(cfg, deps, mut, false, nil, store); err != nil {
		t.Fatalf("pass 3: unexpected error: %v", err)
	}
	if len(mut.labelEdits) != 1 {
		t.Fatalf("pass 3 must wait out the restarted grace, got %+v", mut.labelEdits)
	}

	// Pass 4: grace has elapsed again and the durable attempt count now
	// reflects the failed retry (2, at budget) → dispatch-failed, terminal,
	// no error — reached within a bounded number of passes, never looping.
	deps = deadWorkingDeps(start.Add(15 * time.Minute))
	deps.Attempts = map[string]int{"o/r#42": 2}
	res, err = applyReconcile(cfg, deps, mut, false, nil, store)
	if err != nil {
		t.Fatalf("pass 4: unexpected error (must not resurface reconcile_pass_failed): %v", err)
	}
	if len(mut.labelEdits) != 2 || !containsStr(mut.labelEdits[1].add, labelDispatchFailed) {
		t.Fatalf("pass 4 must reach dispatch-failed, got %+v", mut.labelEdits)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("ticket must be terminal (Failed) by pass 4, within a bounded number of passes")
	}
}

// TestStateStoreLoadsOldFormatWithoutApplyFailures is the direct store-level
// regression test for the ReconcileState schema back-compat requirement: an
// old reconcile.json written before ApplyFailures existed must still load
// cleanly, with ApplyFailures resolving to an empty (not nil) map.
func TestStateStoreLoadsOldFormatWithoutApplyFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	old := `{"observations":{"o/r#42":"2026-07-10T12:00:00Z"}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("writing old-format fixture: %v", err)
	}

	store := &stateStore{path: path}
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned an unexpected error on an old-format file: %v", err)
	}
	if state.ApplyFailures == nil {
		t.Error("ApplyFailures must load as an empty map (nil→empty) for back-compat with pre-#265 state files, got nil")
	}
	if _, ok := state.Observations["o/r#42"]; !ok {
		t.Error("existing observations must still load from an old-format file")
	}
}

// TestLabelAlreadyExistsClassifiesGHOutput is the unit test for the
// lessons-learned-mandated classification: "already exists" in gh's output is
// success, everything else (auth/network failure) must surface as an error —
// never inferred from a blanket exec error.
// TestGHMutatorEnsureLabelsCachesConfirmed verifies GHMutator.EnsureLabels
// caches a (repo, name) once it's confirmed to exist (created, or already
// existed), so a later pass never re-shells `gh label create` for the same
// key — but a genuine create failure is never cached and is retried on the
// next call.
func TestGHMutatorEnsureLabelsCachesConfirmed(t *testing.T) {
	var calls []string
	fail := map[string]bool{}
	m := &GHMutator{
		createLabel: func(repo, name, color, description string) error {
			calls = append(calls, repo+"/"+name)
			if fail[repo+"/"+name] {
				return errors.New("gh: authentication required. run `gh auth login`")
			}
			return nil
		},
	}

	// First call confirms dispatch-failed in o/r: create is invoked.
	if err := m.EnsureLabels("o/r", []string{labelDispatchFailed}); err != nil {
		t.Fatalf("EnsureLabels: %v", err)
	}
	// Second call for the same (repo, name): cache hit, no additional call.
	if err := m.EnsureLabels("o/r", []string{labelDispatchFailed}); err != nil {
		t.Fatalf("EnsureLabels: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 create call for o/r/%s, got %d: %v", labelDispatchFailed, len(calls), calls)
	}

	// A different, not-yet-confirmed label triggers its own create call.
	if err := m.EnsureLabels("o/r", []string{labelPlanInvalid}); err != nil {
		t.Fatalf("EnsureLabels: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 create calls after a new label, got %d: %v", len(calls), calls)
	}

	// A genuine failure must surface and must NOT be cached: a subsequent
	// call for the same key must retry (invoke create again).
	fail["o2/r2/"+labelReconcileStuck] = true
	if err := m.EnsureLabels("o2/r2", []string{labelReconcileStuck}); err == nil {
		t.Fatal("expected EnsureLabels to return the genuine create failure")
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 create calls after the failing call, got %d: %v", len(calls), calls)
	}
	if err := m.EnsureLabels("o2/r2", []string{labelReconcileStuck}); err == nil {
		t.Fatal("expected the retried EnsureLabels call to still fail (not cached)")
	}
	if len(calls) != 4 {
		t.Fatalf("expected the failed key to be retried (4 total create calls), got %d: %v", len(calls), calls)
	}
}

func TestLabelAlreadyExistsClassifiesGHOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"already exists is success", "GraphQL: label already exists (createLabel)", true},
		{"auth failure is a genuine error", "gh: authentication required. run `gh auth login`", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := labelAlreadyExists(tc.output); got != tc.want {
				t.Errorf("labelAlreadyExists(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}
