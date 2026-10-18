package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc/windowproctest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
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

// -- #853: interrupted-resume recovery applies through applyReconcile -------

// resumeAwaitingInputDeps mirrors deadWorkingDeps(now) but with the matched
// plan at status: awaiting-input rather than planned -- the runner-level
// twin of reconcile_test.go's resumeAwaitingInputInputs().
func resumeAwaitingInputDeps(now time.Time) reconcileDeps {
	deps := deadWorkingDeps(now)
	deps.Plans = awaitingInputPlans()
	return deps
}

// TestRunReconcileResumeInterruptedAppliesLabelAndComment covers the applied
// side effects: the +Input Needed -Working swap lands via EditLabels, and
// the posted comment carries attemptMarker for durable shared counting
// (mirrors TestRunReconcileRetryAppliesLabelAndComment's pattern for the new
// kind).
func TestRunReconcileResumeInterruptedAppliesLabelAndComment(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := resumeAwaitingInputDeps(now)
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
	if e.number != 42 || !containsStr(e.add, labelInputNeeded) || !containsStr(e.remove, labelWorking) {
		t.Errorf("unexpected label edit: %+v, want add=[%s] remove=[%s]", e, labelInputNeeded, labelWorking)
	}
	if len(mut.comments) != 1 || mut.comments[0].number != 42 {
		t.Fatalf("expected 1 comment on #42, got %+v", mut.comments)
	}
	if !strings.Contains(mut.comments[0].body, attemptMarker) {
		t.Error("resume-interrupted comment must carry the attempt marker for durable shared counting")
	}
}

// TestRunReconcileResumeInterrupted_BudgetExhausted_EscalatesToDispatchFailed
// is the runner-level twin of the pure-engine budget-exhaustion test:
// applied through applyReconcile, a budget-exhausted interrupted resume must
// land the ordinary Working→dispatch-failed swap, never another Input
// Needed restore.
func TestRunReconcileResumeInterrupted_BudgetExhausted_EscalatesToDispatchFailed(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := resumeAwaitingInputDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 2} // budget reached

	mut := &fakeMutator{}
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}}
	cfg := reconcileConfig()

	res, err := applyReconcile(cfg, deps, mut, false, nil, store)
	if err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}
	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit, got %d", len(mut.labelEdits))
	}
	e := mut.labelEdits[0]
	if !containsStr(e.add, labelDispatchFailed) || !containsStr(e.remove, labelWorking) {
		t.Errorf("expected Working→dispatch-failed once the shared attempt budget is exhausted, got %+v", e)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a budget-exhausted interrupted resume must appear in Failed")
	}
}

// -- #926: plan-driven heal applies through applyReconcile -------------------

// planHealDeps is the runner-level twin of reconcile_test.go's
// planHealRefinedInputs()/planHealInputNeededInputs(): #42 carries the given
// labels and a matched status: planned plan, window gone, no open PR.
func planHealDeps(now time.Time, labels ...string) reconcileDeps {
	deps := deadWorkingDeps(now)
	deps.Tickets = []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: labels}}
	return deps
}

// TestRunReconcilePlanHealRefined_AppliesAddOnlyEditAndMarkedComment covers
// heal A end to end: exactly one label edit adding Planned and removing
// nothing, one comment carrying planHealMarker (never attemptMarker), and no
// EnsureLabels call (Planned is not a cenci-managed label).
func TestRunReconcilePlanHealRefined_AppliesAddOnlyEditAndMarkedComment(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := planHealDeps(now, "Refined")
	mut := &fakeMutator{}
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}}

	res, err := applyReconcile(reconcileConfig(), deps, mut, false, nil, store)
	if err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit, got %+v", mut.labelEdits)
	}
	e := mut.labelEdits[0]
	if e.number != 42 || !slices.Equal(e.add, []string{labelPlanned}) || len(e.remove) != 0 {
		t.Errorf("label edit = %+v, want add=[%s] remove=[]", e, labelPlanned)
	}
	if len(mut.comments) != 1 || mut.comments[0].number != 42 {
		t.Fatalf("expected 1 comment on #42, got %+v", mut.comments)
	}
	if !strings.Contains(mut.comments[0].body, planHealMarker) || strings.Contains(mut.comments[0].body, attemptMarker) {
		t.Errorf("heal comment must carry %q and not %q, got %q", planHealMarker, attemptMarker, mut.comments[0].body)
	}
	if len(mut.ensureCalls) != 0 {
		t.Errorf("Planned is not a managed label; expected zero EnsureLabels calls, got %+v", mut.ensureCalls)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("a healed ticket must not be surfaced in Failed")
	}
	if _, ok := store.state.Observations["o/r#42"]; ok {
		t.Error("the grace observation must be cleared once the heal lands")
	}
}

// TestRunReconcilePlanHealInputNeeded_EditRemovesBothLabels covers heal B end
// to end: one edit adds Planned and removes both Working and Input Needed, with
// the marked comment, and the ticket is still reported as Escalated.
func TestRunReconcilePlanHealInputNeeded_EditRemovesBothLabels(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := planHealDeps(now, "Refined", "Input Needed")
	mut := &fakeMutator{}
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}}

	res, err := applyReconcile(reconcileConfig(), deps, mut, false, nil, store)
	if err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit, got %+v", mut.labelEdits)
	}
	e := mut.labelEdits[0]
	if !slices.Equal(e.add, []string{labelPlanned}) || !slices.Equal(e.remove, []string{labelWorking, labelInputNeeded}) {
		t.Errorf("label edit = %+v, want add=[%s] remove=[%s %s]", e, labelPlanned, labelWorking, labelInputNeeded)
	}
	if len(mut.comments) != 1 || !strings.Contains(mut.comments[0].body, planHealMarker) {
		t.Errorf("expected 1 comment carrying %q, got %+v", planHealMarker, mut.comments)
	}
	if len(mut.ensureCalls) != 0 {
		t.Errorf("expected zero EnsureLabels calls, got %+v", mut.ensureCalls)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("an Input Needed ticket must be reported in Escalated on the pass the heal fires")
	}
}

// TestRunReconcilePlanHealPlanned_AppliesAddInputNeededEditAndMarkedComment
// covers heal C end to end: a Planned ticket whose plan reads awaiting-input
// gets exactly one label edit adding Input Needed and removing nothing,
// followed by one comment carrying planHealMarker (never attemptMarker). The
// call order is exactly edit then comment, which also proves zero EnsureLabels
// calls (Input Needed is not a cenci-managed label).
func TestRunReconcilePlanHealPlanned_AppliesAddInputNeededEditAndMarkedComment(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := planHealDeps(now, "bug", "Refined", "Planned")
	deps.Plans = awaitingInputPlans()
	mut := &fakeMutator{}
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}}}

	res, err := applyReconcile(reconcileConfig(), deps, mut, false, nil, store)
	if err != nil {
		t.Fatalf("applyReconcile returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit, got %+v", mut.labelEdits)
	}
	e := mut.labelEdits[0]
	if e.number != 42 || !slices.Equal(e.add, []string{labelInputNeeded}) || len(e.remove) != 0 {
		t.Errorf("label edit = %+v, want add=[%s] remove=[]", e, labelInputNeeded)
	}
	if len(mut.comments) != 1 || mut.comments[0].number != 42 {
		t.Fatalf("expected 1 comment on #42, got %+v", mut.comments)
	}
	if !strings.Contains(mut.comments[0].body, planHealMarker) || strings.Contains(mut.comments[0].body, attemptMarker) {
		t.Errorf("heal comment must carry %q and not %q, got %q", planHealMarker, attemptMarker, mut.comments[0].body)
	}
	if want := []string{"edit", "comment"}; !slices.Equal(mut.callOrder, want) {
		t.Errorf("call order = %v, want exactly %v (label edit, then the marked comment, no EnsureLabels)", mut.callOrder, want)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("a healed ticket must not be surfaced in Failed")
	}
	if _, ok := store.state.Observations["o/r#42"]; ok {
		t.Error("the grace observation must be cleared once the heal lands")
	}
}

// driveHealToEscalation runs the plan heal for #42 (carrying labels) through
// escalatingMutator until the apply-retry budget is spent, and returns the
// reconcile-stuck label edit it produced (fatal if none), the last pass's
// result and error, and the store.
func driveHealToEscalation(t *testing.T, labels ...string) (labelEdit, ReconcileResult, *memStore, error) {
	t.Helper()
	return driveHealToEscalationWithPlans(t, nil, labels...)
}

// driveHealToEscalationWithPlans is driveHealToEscalation with the deps' plans
// overridden (nil keeps planHealDeps' status: planned plan), so a heal keyed on
// another plan status -- heal C's awaiting-input -- can be driven the same way.
func driveHealToEscalationWithPlans(t *testing.T, plans []Plan, labels ...string) (labelEdit, ReconcileResult, *memStore, error) {
	t.Helper()
	cfg := reconcileConfig()
	cfg.ApplyRetryBudget = 3
	mut := &escalatingMutator{}
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": start.Add(-10 * time.Minute)}}}
	deps := planHealDeps(start, labels...)
	if plans != nil {
		deps.Plans = plans
	}

	var res ReconcileResult
	var lastErr error
	for i := 0; i < cfg.ApplyRetryBudget; i++ {
		deps.Now = start.Add(time.Duration(i) * time.Minute)
		res, lastErr = applyReconcile(cfg, deps, mut, false, nil, store)
	}

	for _, e := range mut.labelEdits {
		if containsStr(e.add, labelReconcileStuck) {
			return e, res, store, lastErr
		}
	}
	t.Fatalf("expected an EditLabels call adding %q after %d failed passes, got %+v", labelReconcileStuck, cfg.ApplyRetryBudget, mut.labelEdits)
	return labelEdit{}, res, store, lastErr
}

// TestApplyReconcilePlanHealRefined_EscalatesToReconcileStuckWithEmptyRemove
// drives heal A through escalatingMutator until the apply-retry budget is
// spent. Heal A removes nothing, so the escalation (which reuses
// rec.RemoveLabels) must add reconcile-stuck with an empty remove set.
func TestApplyReconcilePlanHealRefined_EscalatesToReconcileStuckWithEmptyRemove(t *testing.T) {
	escalation, res, store, lastErr := driveHealToEscalation(t, "Refined")

	if lastErr != nil {
		t.Errorf("the escalating pass must not surface an error once reconcile-stuck is applied: %v", lastErr)
	}
	if !slices.Equal(escalation.add, []string{labelReconcileStuck}) || len(escalation.remove) != 0 {
		t.Errorf("escalation edit = %+v, want add=[%s] remove=[]", escalation, labelReconcileStuck)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("an escalated ticket must be surfaced in Failed")
	}
	if _, ok := store.state.Observations["o/r#42"]; ok {
		t.Error("the grace observation must be dropped once escalation succeeds")
	}
}

// TestApplyReconcilePlanHealPlanned_EscalatesToReconcileStuckWithEmptyRemove
// drives heal C through escalatingMutator until the apply-retry budget is
// spent. Heal C removes nothing, so the escalation (which reuses
// rec.RemoveLabels) must add reconcile-stuck with an empty remove set -- the
// ticket keeps Planned and becomes terminal.
func TestApplyReconcilePlanHealPlanned_EscalatesToReconcileStuckWithEmptyRemove(t *testing.T) {
	escalation, res, store, lastErr := driveHealToEscalationWithPlans(t, awaitingInputPlans(), "bug", "Refined", "Planned")

	if lastErr != nil {
		t.Errorf("the escalating pass must not surface an error once reconcile-stuck is applied: %v", lastErr)
	}
	if !slices.Equal(escalation.add, []string{labelReconcileStuck}) || len(escalation.remove) != 0 {
		t.Errorf("escalation edit = %+v, want add=[%s] remove=[]", escalation, labelReconcileStuck)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("an escalated ticket must be surfaced in Failed")
	}
	if _, ok := store.state.Observations["o/r#42"]; ok {
		t.Error("the grace observation must be dropped once escalation succeeds")
	}
}

// TestApplyReconcilePlanHealInputNeeded_EscalationRemovesBothLabels pins the
// Q1 pass-through: heal B removes Working and Input Needed, so when its apply
// keeps failing the terminal reconcile-stuck escalation removes the same two
// labels (the ticket is then shown under Failed).
func TestApplyReconcilePlanHealInputNeeded_EscalationRemovesBothLabels(t *testing.T) {
	escalation, res, _, _ := driveHealToEscalation(t, "Refined", "Input Needed")

	if !slices.Equal(escalation.remove, []string{labelWorking, labelInputNeeded}) {
		t.Errorf("escalation remove = %v, want [%s %s]", escalation.remove, labelWorking, labelInputNeeded)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("an escalated ticket must be surfaced in Failed")
	}
}

// loadFailingStore is a bare (non-sentinel) failing ReconcileStore: it
// stands in for a third-party ReconcileStore implementation that returns an
// ordinary error rather than a *StateLoadError. loadCalls/saveCalls let
// tests assert exactly how many times each method was invoked -- in
// particular, that Save is never reached on an aborted pass (AC #3).
type loadFailingStore struct {
	err                  error
	loadCalls, saveCalls int
}

func (s *loadFailingStore) Load() (ReconcileState, error) {
	s.loadCalls++
	return ReconcileState{}, s.err
}

func (s *loadFailingStore) Save(ReconcileState) error {
	s.saveCalls++
	return nil
}

// sentinelLoadFailingStore always fails Load with a *StateLoadError wrapping
// ErrReconcileStateUnreadable (the corruption-hold sentinel), so tests can
// assert the abort-before-mutation contract distinctly from a bare
// third-party load error (loadFailingStore above).
type sentinelLoadFailingStore struct {
	loadCalls, saveCalls int
}

func (s *sentinelLoadFailingStore) Load() (ReconcileState, error) {
	s.loadCalls++
	return ReconcileState{}, &StateLoadError{
		Probe: StateProbeDecodeError,
		Path:  "reconcile.json",
		Err:   errors.New("decode failed"),
	}
}

func (s *sentinelLoadFailingStore) Save(ReconcileState) error {
	s.saveCalls++
	return nil
}

// saveFailingStore loads a preset state cleanly but always fails Save, so
// tests can isolate persistence-error propagation from load-error handling
// (a successful mutation must not swallow a later Save failure).
type saveFailingStore struct {
	state     ReconcileState
	saveErr   error
	saveCalls int
}

func (s *saveFailingStore) Load() (ReconcileState, error) {
	return s.state, nil
}

func (s *saveFailingStore) Save(ReconcileState) error {
	s.saveCalls++
	return s.saveErr
}

// onceThenSentinelStore's first Load call succeeds (as the RunReconcileOnce
// pre-collection hold probe's call would), but every subsequent call fails
// with the sentinel -- simulating the state file being corrupted between the
// early probe and applyReconcile's own authoritative Load. This is the only
// way to actually exercise RunReconcileOnce's firstNonNil masking-bug guard
// with a real, non-nil collectErr in the same pass: a store that fails
// immediately would trip the pre-collection probe first and never reach
// CollectTickets at all.
type onceThenSentinelStore struct {
	loadCalls int
}

func (s *onceThenSentinelStore) Load() (ReconcileState, error) {
	s.loadCalls++
	if s.loadCalls == 1 {
		return emptyReconcileState(), nil
	}
	return ReconcileState{}, &StateLoadError{
		Probe: StateProbeDecodeError,
		Path:  "reconcile.json",
		Err:   errors.New("decode failed"),
	}
}

func (s *onceThenSentinelStore) Save(ReconcileState) error { return nil }

// TestApplyReconcileAbortsOnAnyLoadError is the default-deny regression test
// for a bare, non-sentinel load error (standing in for a third-party
// ReconcileStore implementation, per the ticket's assumption that
// applyReconcile aborts on ANY non-nil Load error, not only the sentinel):
// the pass still aborts before Reconcile()/any mutation, and Save is never
// reached in the same pass (AC #3).
func TestApplyReconcileAbortsOnAnyLoadError(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}
	// The grace observation must already have elapsed for a recovery mutation.
	deps.Now = now.Add(10 * time.Minute)
	store := &loadFailingStore{err: errors.New("load failed")}
	mut := &fakeMutator{}

	_, err := applyReconcile(reconcileConfig(), deps, mut, false, nil, store)
	if err == nil || !strings.Contains(err.Error(), "load failed") {
		t.Fatalf("applyReconcile err = %v, want an error wrapping %q", err, "load failed")
	}
	if len(mut.labelEdits) != 0 || len(mut.comments) != 0 || len(mut.ensureCalls) != 0 {
		t.Errorf("a bare non-sentinel load error must still default-deny (zero mutations), got labelEdits=%+v comments=%+v ensure=%+v",
			mut.labelEdits, mut.comments, mut.ensureCalls)
	}
	if store.saveCalls != 0 {
		t.Errorf("a load failure must never be overwritten by Save in the same pass (AC #3), got %d save calls", store.saveCalls)
	}
}

// TestApplyReconcilePropagatesMutationAndPersistenceErrors isolates the
// persistence-error propagation half of the original combined coverage
// (Load succeeds cleanly, the mutation applies successfully, but Save
// fails): the Save failure must still surface as the returned error rather
// than being swallowed because the pass otherwise "succeeded".
func TestApplyReconcilePropagatesMutationAndPersistenceErrors(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}
	deps.Now = now.Add(10 * time.Minute)
	store := &saveFailingStore{
		state:   ReconcileState{Observations: map[string]time.Time{"o/r#42": now.Add(-10 * time.Minute)}},
		saveErr: errors.New("save failed"),
	}
	mut := &fakeMutator{}

	_, err := applyReconcile(reconcileConfig(), deps, mut, false, nil, store)
	if err == nil || err.Error() != "save failed" {
		t.Fatalf("applyReconcile err = %v, want save failed (a successful mutation must not swallow a persistence failure)", err)
	}
	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected the mutation to still apply despite the later Save failure, got %+v", mut.labelEdits)
	}
	if store.saveCalls != 1 {
		t.Errorf("expected exactly one Save call, got %d", store.saveCalls)
	}
}

// TestApplyReconcileAbortsOnSentinelLoadErrorZeroMutatorCalls is the direct
// regression test for the corruption-hold contract: on a sentinel load
// error, applyReconcile must return before Reconcile() ever runs, applying
// zero GitHub mutations, even though the deps would otherwise produce a
// past-grace retry mutation.
func TestApplyReconcileAbortsOnSentinelLoadErrorZeroMutatorCalls(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}
	deps.Now = now.Add(10 * time.Minute)
	store := &sentinelLoadFailingStore{}
	mut := &fakeMutator{}

	_, err := applyReconcile(reconcileConfig(), deps, mut, false, nil, store)
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("applyReconcile err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("expected zero EditLabels calls on a corruption-held pass, got %+v", mut.labelEdits)
	}
	if len(mut.comments) != 0 {
		t.Errorf("expected zero Comment calls on a corruption-held pass, got %+v", mut.comments)
	}
	if len(mut.ensureCalls) != 0 {
		t.Errorf("expected zero EnsureLabels calls on a corruption-held pass, got %+v", mut.ensureCalls)
	}
}

// TestApplyReconcileAbortsOnSentinelLoadErrorZeroSaveCalls is AC #3's direct
// regression test: a load failure must never be overwritten by a later Save
// in the same pass.
func TestApplyReconcileAbortsOnSentinelLoadErrorZeroSaveCalls(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}
	deps.Now = now.Add(10 * time.Minute)
	store := &sentinelLoadFailingStore{}

	_, err := applyReconcile(reconcileConfig(), deps, &fakeMutator{}, false, nil, store)
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("applyReconcile err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if store.saveCalls != 0 {
		t.Errorf("expected zero Save calls (a load failure is never overwritten in the same pass, AC #3), got %d", store.saveCalls)
	}
}

// TestApplyReconcileAbortsOnSentinelLoadErrorInDryRun locks in the ticket's
// explicit decision: dry-run may only report prospective decisions when it
// can read authoritative prior state -- corruption remains a hold even in
// dry-run, not merely "reported without applying".
func TestApplyReconcileAbortsOnSentinelLoadErrorInDryRun(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}
	deps.Now = now.Add(10 * time.Minute)
	store := &sentinelLoadFailingStore{}
	mut := &fakeMutator{}

	_, err := applyReconcile(reconcileConfig(), deps, mut, true, nil, store)
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("dry-run applyReconcile err = %v, want errors.Is(err, ErrReconcileStateUnreadable) (corruption remains a hold in dry-run)", err)
	}
	if len(mut.labelEdits) != 0 || len(mut.comments) != 0 || len(mut.ensureCalls) != 0 {
		t.Errorf("dry-run must still apply zero mutations on a corruption hold, got labelEdits=%+v comments=%+v ensure=%+v",
			mut.labelEdits, mut.comments, mut.ensureCalls)
	}
	if store.saveCalls != 0 {
		t.Errorf("dry-run must never save on a corruption hold, got %d save calls", store.saveCalls)
	}
}

// TestRunReconcileOnceSentinelOutranksCollectionError is the direct
// regression test for the firstNonNil masking bug: RunReconcileOnce's
// return statement must not let a same-pass collection error mask the
// corruption sentinel. onceThenSentinelStore lets the pre-collection hold
// probe pass (so CollectTickets actually runs and fails, producing a real
// collectErr) while applyReconcile's own authoritative second Load then
// fails with the sentinel.
func TestRunReconcileOnceSentinelOutranksCollectionError(t *testing.T) {
	installFakeGH(t, "exit 1\n") // every gh call fails -> CollectTickets returns a non-nil collectErr
	cfg := reconcileConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: t.TempDir()}}
	store := &onceThenSentinelStore{}
	var buf bytes.Buffer

	_, err := RunReconcileOnce(cfg, &fakeMutator{}, false, &buf, store)
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("RunReconcileOnce err = %v, want errors.Is(err, ErrReconcileStateUnreadable): a same-pass collection error must not mask the corruption sentinel", err)
	}
}

// TestApplyReconcileMissingStateFileStillReconcilesAndMutates is the
// regression guard that the hold must not catch first runs: a genuinely
// missing state file (StateProbeAbsent) is valid empty initial state, not
// corruption, so a past-grace ticket must still be mutated normally.
func TestApplyReconcileMissingStateFileStillReconcilesAndMutates(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := &stateStore{path: filepath.Join(t.TempDir(), "does-not-exist", "reconcile.json")}
	mut := &fakeMutator{}

	// Pass 1: the state file is absent (StateProbeAbsent, valid empty initial
	// state) -- Load must not error, and since this is the ticket's very
	// first observation the pure engine only records it (firstSeen == Now on
	// a first sighting, so grace is trivially not elapsed yet); no mutation
	// is possible on a single first-ever pass regardless of Now, mirroring
	// every other "pass 1 observes, pass 2 acts" test in this file (e.g.
	// TestRunReconcileObservationPersistsThenActs).
	deps := deadWorkingDeps(now)
	deps.Attempts = map[string]int{"o/r#42": 0}
	if _, err := applyReconcile(reconcileConfig(), deps, mut, false, nil, store); err != nil {
		t.Fatalf("pass 1: a missing state file must not be treated as corruption, got err: %v", err)
	}
	if len(mut.labelEdits) != 0 {
		t.Fatalf("pass 1 must only observe (no prior state to act on), got %+v", mut.labelEdits)
	}

	// Pass 2: grace has now elapsed on the persisted observation from pass 1
	// -- the hold must not have caught the first run, so reconciliation
	// mutates normally here.
	deps2 := deadWorkingDeps(now.Add(10 * time.Minute))
	deps2.Attempts = map[string]int{"o/r#42": 0}
	res, err := applyReconcile(reconcileConfig(), deps2, mut, false, nil, store)
	if err != nil {
		t.Fatalf("pass 2: unexpected error: %v", err)
	}
	if len(mut.labelEdits) != 1 || !containsStr(mut.labelEdits[0].add, labelPlanned) {
		t.Fatalf("expected the retry mutation to apply once grace elapses from a first run with no prior state, got %+v", mut.labelEdits)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("a first run must not be terminal")
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

// -- #1292: the launch-ID probe (resolveLaunchVerdicts) -----------------------

// probeCall is one launchProbeFn invocation.
type probeCall struct{ rt, container, id string }

// launchSeams records every call into the two probe seams and answers them from
// the supplied funcs. A nil list func makes any listing call a test failure.
type launchSeams struct {
	probeCalls []probeCall
	listCalls  []string // the runtime of each running-container listing
}

func stubLaunchSeams(t *testing.T, probe func(ctx context.Context, c probeCall) ([]sandbox.ProcTarget, error), list func(ctx context.Context, rt string) ([]string, error)) *launchSeams {
	t.Helper()
	s := &launchSeams{}
	restoreProbe, restoreList := launchProbeFn, runningContainersFn
	launchProbeFn = func(ctx context.Context, rt, container, id string) ([]sandbox.ProcTarget, error) {
		c := probeCall{rt, container, id}
		s.probeCalls = append(s.probeCalls, c)
		return probe(ctx, c)
	}
	runningContainersFn = func(ctx context.Context, rt string) ([]string, error) {
		s.listCalls = append(s.listCalls, rt)
		if list == nil {
			t.Errorf("unexpected running-container listing on %s", rt)
			return nil, errors.New("unexpected listing")
		}
		return list(ctx, rt)
	}
	t.Cleanup(func() { launchProbeFn, runningContainersFn = restoreProbe, restoreList })
	return s
}

// launchTicket is an eligible ticket: Working, recorded launch, complete
// open-PR probe, no open PR.
func launchTicket(number int, launch RecordedLaunch) Ticket {
	return Ticket{Repo: "o/r", Number: number, Labels: []string{"Working", "Planned"}, Launch: launch}
}

func emptySnapshot() *watch.StateSnapshot { return &watch.StateSnapshot{} }

// TestResolveLaunchVerdicts_ProbeOutcomes is the verdict matrix, including the
// edge cases: unreadable processes with a match are live and without one defer,
// a container missing from a successful running listing is dead (stopped
// containers included), a failed listing defers, and a container that is still
// running while the probe keeps failing is not proven dead.
func TestResolveLaunchVerdicts_ProbeOutcomes(t *testing.T) {
	procs := []sandbox.ProcTarget{{PID: "7", Start: "100"}}
	unreadable := fmt.Errorf("1 process(es) unreadable: %w", sandbox.ErrUnreadableProcesses)
	probeFailed := errors.New("docker exec launch probe: exit status 1")
	tests := []struct {
		name          string
		probeProcs    []sandbox.ProcTarget
		probeErr      error
		running       []string
		listErr       error
		want          LaunchVerdict
		wantListCalls int
	}{
		{name: "a matching process is live", probeProcs: procs, want: LaunchVerdictLive},
		{name: "no matching process is dead", want: LaunchVerdictDead},
		{name: "unreadable processes beside a match are live", probeProcs: procs, probeErr: unreadable, want: LaunchVerdictLive},
		{name: "unreadable processes and no match cannot prove death", probeErr: unreadable, want: LaunchVerdictUnknown},
		{name: "probe error, container absent from the listing is dead", probeErr: probeFailed, running: []string{"claude-cenci-other"}, want: LaunchVerdictDead, wantListCalls: 1},
		{name: "probe error, stopped container (empty listing) is dead", probeErr: probeFailed, running: nil, want: LaunchVerdictDead, wantListCalls: 1},
		{name: "probe error, container still running is not proven dead", probeErr: probeFailed, running: []string{testLaunchContainer}, want: LaunchVerdictUnknown, wantListCalls: 1},
		{name: "probe error and failed listing is unknown", probeErr: probeFailed, listErr: errors.New("docker ps: exit status 1"), want: LaunchVerdictUnknown, wantListCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stubLaunchSeams(t,
				func(context.Context, probeCall) ([]sandbox.ProcTarget, error) { return tt.probeProcs, tt.probeErr },
				func(context.Context, string) ([]string, error) { return tt.running, tt.listErr })

			got := resolveLaunchVerdicts([]Ticket{launchTicket(42, testLaunch)}, emptySnapshot(), io.Discard)

			if got["o/r#42"] != tt.want {
				t.Errorf("verdict = %q, want %q", got["o/r#42"], tt.want)
			}
			want := []probeCall{{testLaunchRuntime, testLaunchContainer, testLaunchID}}
			if !reflect.DeepEqual(s.probeCalls, want) {
				t.Errorf("probe calls = %+v, want exactly %+v (the recorded triple, once)", s.probeCalls, want)
			}
			if len(s.listCalls) != tt.wantListCalls {
				t.Errorf("listing calls = %v, want %d", s.listCalls, tt.wantListCalls)
			}
		})
	}
}

// TestResolveLaunchVerdicts_InferredDeadFromFailedProbeIsLogged: a dead verdict
// inferred from a failed probe plus a container absent from a successful listing
// is a judgement call, so it leaves exactly one log line naming the ticket and
// the bounded error, never a recorded launch ID, container or runtime value. A
// dead verdict from an authoritative empty probe stays silent.
func TestResolveLaunchVerdicts_InferredDeadFromFailedProbeIsLogged(t *testing.T) {
	probeFailed := errors.New("exec launch probe: exit status 1")
	tests := []struct {
		name     string
		probeErr error
		wantLog  string
	}{
		{
			name:     "failed probe, container absent from the listing",
			probeErr: probeFailed,
			wantLog:  "reconcile: #42 launch probe failed (exec launch probe: exit status 1), container absent from running list, treating as dead\n",
		},
		{name: "authoritative empty probe is silent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubLaunchSeams(t,
				func(context.Context, probeCall) ([]sandbox.ProcTarget, error) { return nil, tt.probeErr },
				func(context.Context, string) ([]string, error) { return []string{"claude-cenci-other"}, nil })
			var buf bytes.Buffer

			got := resolveLaunchVerdicts([]Ticket{launchTicket(42, testLaunch)}, emptySnapshot(), &buf)

			if got["o/r#42"] != LaunchVerdictDead {
				t.Fatalf("verdict = %q, want dead", got["o/r#42"])
			}
			if buf.String() != tt.wantLog {
				t.Errorf("log = %q, want %q", buf.String(), tt.wantLog)
			}
			for _, secret := range []string{testLaunchID, testLaunchContainer, testLaunchRuntime} {
				if strings.Contains(buf.String(), secret) {
					t.Errorf("log %q echoes the recorded value %q", buf.String(), secret)
				}
			}
		})
	}
}

// TestResolveLaunchVerdicts_Eligibility covers AC7: the probe seam is called at
// most once per ticket, and not at all for a ticket that lacks Working, has no
// recorded launch, has a nil snapshot, an incomplete open-PR probe or an open
// PR, or already carries a terminal label. An ineligible ticket gets no verdict
// and costs no log line, even when its recorded values are junk.
func TestResolveLaunchVerdicts_Eligibility(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Ticket)
		nilSnap   bool
		wantCalls int
	}{
		{name: "eligible", mutate: func(*Ticket) {}, wantCalls: 1},
		{name: "no Working label", mutate: func(tk *Ticket) { tk.Labels = []string{"Planned"} }},
		{name: "no recorded launch", mutate: func(tk *Ticket) { tk.Launch = RecordedLaunch{} }},
		{name: "nil snapshot", mutate: func(*Ticket) {}, nilSnap: true},
		{name: "open-PR probe truncated", mutate: func(tk *Ticket) { tk.OpenPRProbe = OpenPRProbeTruncated }},
		{name: "open-PR probe timed out", mutate: func(tk *Ticket) { tk.OpenPRProbe = OpenPRProbeTimeout }},
		{name: "has an open PR", mutate: func(tk *Ticket) { tk.HasOpenPR = true }},
		{name: "dispatch-failed", mutate: func(tk *Ticket) { tk.Labels = append(tk.Labels, labelDispatchFailed) }},
		{name: "plan-invalid", mutate: func(tk *Ticket) { tk.Labels = append(tk.Labels, labelPlanInvalid) }},
		{name: "reconcile-stuck", mutate: func(tk *Ticket) { tk.Labels = append(tk.Labels, labelReconcileStuck) }},
		{name: "ineligible with a junk triple stays silent", mutate: func(tk *Ticket) {
			tk.Labels = []string{"Planned"}
			tk.Launch = RecordedLaunch{ID: "junk", Container: "; rm -rf /", Runtime: "sh"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stubLaunchSeams(t,
				func(context.Context, probeCall) ([]sandbox.ProcTarget, error) {
					return []sandbox.ProcTarget{{PID: "7"}}, nil
				},
				nil)
			tk := launchTicket(42, testLaunch)
			tt.mutate(&tk)
			snap := emptySnapshot()
			if tt.nilSnap {
				snap = nil
			}
			var buf bytes.Buffer

			got := resolveLaunchVerdicts([]Ticket{tk}, snap, &buf)

			if len(s.probeCalls) != tt.wantCalls {
				t.Errorf("probe calls = %d, want %d", len(s.probeCalls), tt.wantCalls)
			}
			if _, ok := got["o/r#42"]; ok != (tt.wantCalls == 1) {
				t.Errorf("verdict present = %v, want %v", ok, tt.wantCalls == 1)
			}
			if tt.wantCalls == 0 && buf.Len() != 0 {
				t.Errorf("an ineligible ticket must log nothing, got %q", buf.String())
			}
		})
	}
}

// TestResolveLaunchVerdicts_ProbesEachTicketOncePerPass: several eligible
// tickets, each probed exactly once, each under its own key even when two
// repos reuse a number.
func TestResolveLaunchVerdicts_ProbesEachTicketOncePerPass(t *testing.T) {
	s := stubLaunchSeams(t,
		func(context.Context, probeCall) ([]sandbox.ProcTarget, error) { return nil, nil },
		nil)
	other := testLaunch
	other.ID = "fedcba9876543210fedcba9876543210"
	a := launchTicket(42, testLaunch)
	b := launchTicket(43, other)
	c := launchTicket(42, other)
	c.Repo = "o/s"

	got := resolveLaunchVerdicts([]Ticket{a, b, c}, emptySnapshot(), io.Discard)

	if len(s.probeCalls) != 3 {
		t.Errorf("probe calls = %+v, want 3 (one per ticket)", s.probeCalls)
	}
	for _, key := range []string{"o/r#42", "o/r#43", "o/s#42"} {
		if got[key] != LaunchVerdictDead {
			t.Errorf("verdict[%s] = %q, want dead", key, got[key])
		}
	}
}

// TestResolveLaunchVerdicts_InvalidRecordedTripleNeverReachesExec covers AC12:
// a recorded triple that fails any re-validation, or is incomplete, is never
// handed to either seam (so never to an exec argv), earns no verdict (the ticket
// falls back to the window-name rule) and costs exactly one log line that names
// the ticket and echoes no recorded value.
func TestResolveLaunchVerdicts_InvalidRecordedTripleNeverReachesExec(t *testing.T) {
	tests := []struct {
		name   string
		launch RecordedLaunch
		leak   string // a fragment of the junk that must not reach the log
	}{
		{"uppercase launch ID", RecordedLaunch{ID: "0123456789ABCDEF0123456789ABCDEF", Container: testLaunchContainer, Runtime: testLaunchRuntime}, "0123456789ABCDEF"},
		{"31-character launch ID", RecordedLaunch{ID: testLaunchID[:31], Container: testLaunchContainer, Runtime: testLaunchRuntime}, testLaunchID[:31]},
		{"hostile container name", RecordedLaunch{ID: testLaunchID, Container: "claude-cenci-x; touch /tmp/pwn", Runtime: testLaunchRuntime}, "touch /tmp/pwn"},
		{"foreign container name", RecordedLaunch{ID: testLaunchID, Container: "nginx", Runtime: testLaunchRuntime}, "nginx"},
		{"unsupported runtime", RecordedLaunch{ID: testLaunchID, Container: testLaunchContainer, Runtime: "sh"}, "sh"},
		{"missing launch ID", RecordedLaunch{Container: testLaunchContainer, Runtime: testLaunchRuntime}, ""},
		{"missing container", RecordedLaunch{ID: testLaunchID, Runtime: testLaunchRuntime}, ""},
		{"missing runtime", RecordedLaunch{ID: testLaunchID, Container: testLaunchContainer}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stubLaunchSeams(t,
				func(context.Context, probeCall) ([]sandbox.ProcTarget, error) { return nil, nil },
				nil)
			var buf bytes.Buffer

			got := resolveLaunchVerdicts([]Ticket{launchTicket(42, tt.launch)}, emptySnapshot(), &buf)

			if len(s.probeCalls) != 0 || len(s.listCalls) != 0 {
				t.Errorf("seams called (probe %+v, listing %v), want neither", s.probeCalls, s.listCalls)
			}
			if v, ok := got["o/r#42"]; ok {
				t.Errorf("verdict = %q, want none (window-name rule)", v)
			}
			if lines := strings.Count(buf.String(), "\n"); lines != 1 || !strings.Contains(buf.String(), "#42") {
				t.Errorf("log = %q, want exactly one line naming #42", buf.String())
			}
			if tt.leak != "" && strings.Contains(buf.String(), tt.leak) {
				t.Errorf("log %q echoes the recorded value %q", buf.String(), tt.leak)
			}
		})
	}
}

// TestResolveLaunchVerdicts_ListsRunningContainersOncePerRuntime: after probe
// errors, one listing per runtime per pass answers every ticket on that
// runtime, success or failure alike.
func TestResolveLaunchVerdicts_ListsRunningContainersOncePerRuntime(t *testing.T) {
	tests := []struct {
		name      string
		running   []string
		listErr   error
		wantA     LaunchVerdict // the ticket whose container is in `running`
		wantB     LaunchVerdict // a docker ticket whose container is not
		wantPodm  LaunchVerdict // the podman ticket (its listing is a separate call)
		wantCalls []string
	}{
		{"successful listing", []string{"claude-cenci-a"}, nil, LaunchVerdictUnknown, LaunchVerdictDead, LaunchVerdictUnknown, []string{"docker", "podman"}},
		{"failed listing defers every ticket on that runtime", nil, errors.New("docker ps: boom"), LaunchVerdictUnknown, LaunchVerdictUnknown, LaunchVerdictUnknown, []string{"docker", "podman"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stubLaunchSeams(t,
				func(context.Context, probeCall) ([]sandbox.ProcTarget, error) { return nil, errors.New("probe failed") },
				func(_ context.Context, rt string) ([]string, error) {
					if rt == "podman" {
						return []string{"claude-cenci-c"}, nil
					}
					return tt.running, tt.listErr
				})
			launchOn := func(container, rt string) RecordedLaunch {
				return RecordedLaunch{ID: testLaunchID, Container: container, Runtime: rt}
			}

			got := resolveLaunchVerdicts([]Ticket{
				launchTicket(1, launchOn("claude-cenci-a", "docker")),
				launchTicket(2, launchOn("claude-cenci-b", "docker")),
				launchTicket(3, launchOn("claude-cenci-c", "podman")),
			}, emptySnapshot(), io.Discard)

			if !reflect.DeepEqual(s.listCalls, tt.wantCalls) {
				t.Errorf("listing calls = %v, want %v (one per runtime)", s.listCalls, tt.wantCalls)
			}
			for key, want := range map[string]LaunchVerdict{"o/r#1": tt.wantA, "o/r#2": tt.wantB, "o/r#3": tt.wantPodm} {
				if got[key] != want {
					t.Errorf("verdict[%s] = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

// TestResolveLaunchVerdicts_DeadlineHitDefers covers AC9 through the seam: a
// probe that blocks past the pass budget yields the unknown verdict, the ticket
// behind it is not probed at all once the budget is spent, and no listing is
// consulted on an expired context.
func TestResolveLaunchVerdicts_DeadlineHitDefers(t *testing.T) {
	restore := launchProbePassTimeout
	launchProbePassTimeout = 50 * time.Millisecond
	t.Cleanup(func() { launchProbePassTimeout = restore })
	s := stubLaunchSeams(t,
		func(ctx context.Context, _ probeCall) ([]sandbox.ProcTarget, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil)

	got := resolveLaunchVerdicts([]Ticket{launchTicket(42, testLaunch), launchTicket(43, testLaunch)}, emptySnapshot(), io.Discard)

	for _, key := range []string{"o/r#42", "o/r#43"} {
		if got[key] != LaunchVerdictUnknown {
			t.Errorf("verdict[%s] = %q, want unknown", key, got[key])
		}
	}
	if len(s.probeCalls) != 1 {
		t.Errorf("probe calls = %+v, want only the first (the budget was spent)", s.probeCalls)
	}
}

// writeFakeRuntime installs a docker and a podman that run body, as the only
// PATH entries.
func writeFakeRuntime(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"docker", "podman"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// TestResolveLaunchVerdicts_HungRuntimeBinaryIsCutOff covers AC9 end to end:
// with the real default seams, a runtime binary that never answers is killed at
// the pass budget and the ticket defers.
func TestResolveLaunchVerdicts_HungRuntimeBinaryIsCutOff(t *testing.T) {
	restore := launchProbePassTimeout
	launchProbePassTimeout = 200 * time.Millisecond
	t.Cleanup(func() { launchProbePassTimeout = restore })
	writeFakeRuntime(t, `while :; do :; done`) // a builtin loop: no grandchild keeps the pipes open

	start := time.Now()
	got := resolveLaunchVerdicts([]Ticket{launchTicket(42, testLaunch)}, emptySnapshot(), io.Discard)

	if got["o/r#42"] != LaunchVerdictUnknown {
		t.Errorf("verdict = %q, want unknown", got["o/r#42"])
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("resolve took %v, want it bounded by the pass budget", elapsed)
	}
}

// TestResolveLaunchVerdicts_FakeRuntimeBinary drives the real default seams
// against windowproctest's fake docker/podman: the exact argv a probe sends
// (AC9: the existing ListLaunchProcesses, no second environ scan), the one
// running-container listing after a failed probe, nothing at all for an invalid
// triple, and never a kill-window or a kill/signal (AC8).
func TestResolveLaunchVerdicts_FakeRuntimeBinary(t *testing.T) {
	probeLine := func(f windowproctest.Flavor) string {
		return f.Runtime + " exec -u dev " + f.Container + " sh -c " + windowproctest.LaunchProbeToken + " _ " + testLaunchID
	}
	tests := []struct {
		name      string
		spec      func(f windowproctest.Flavor) windowproctest.Spec
		launch    func(f windowproctest.Flavor) RecordedLaunch
		want      LaunchVerdict
		wantCalls func(f windowproctest.Flavor) []string
	}{
		{
			name: "a process carrying the ID is live",
			spec: func(f windowproctest.Flavor) windowproctest.Spec {
				return f.SandboxSpec(testLaunchID, "1", windowproctest.Proc{PID: "700", Start: "5"})
			},
			want:      LaunchVerdictLive,
			wantCalls: func(f windowproctest.Flavor) []string { return []string{probeLine(f)} },
		},
		{
			name:      "no process carrying the ID is dead",
			spec:      func(f windowproctest.Flavor) windowproctest.Spec { return f.SandboxSpec(testLaunchID, "1") },
			want:      LaunchVerdictDead,
			wantCalls: func(f windowproctest.Flavor) []string { return []string{probeLine(f)} },
		},
		{
			name: "unreadable processes beside a match are live",
			spec: func(f windowproctest.Flavor) windowproctest.Spec {
				s := f.SandboxSpec(testLaunchID, "1", windowproctest.Proc{PID: "700", Start: "5"})
				s.Unreadable = map[string][]string{testLaunchID: {"800"}}
				return s
			},
			want:      LaunchVerdictLive,
			wantCalls: func(f windowproctest.Flavor) []string { return []string{probeLine(f)} },
		},
		{
			name: "unreadable processes alone defer",
			spec: func(f windowproctest.Flavor) windowproctest.Spec {
				s := f.SandboxSpec(testLaunchID, "1")
				s.Unreadable = map[string][]string{testLaunchID: {"800"}}
				return s
			},
			want:      LaunchVerdictUnknown,
			wantCalls: func(f windowproctest.Flavor) []string { return []string{probeLine(f)} },
		},
		{
			name: "a failed exec and an empty running listing is a gone container, dead",
			spec: func(f windowproctest.Flavor) windowproctest.Spec {
				s := f.SandboxSpec(testLaunchID, "1")
				s.ProbeFails = true
				return s
			},
			want: LaunchVerdictDead,
			wantCalls: func(f windowproctest.Flavor) []string {
				return []string{probeLine(f), f.Runtime + " ps --format {{.Names}}"}
			},
		},
		{
			name: "an invalid recorded triple reaches no binary at all",
			spec: func(f windowproctest.Flavor) windowproctest.Spec { return f.SandboxSpec(testLaunchID, "1") },
			launch: func(f windowproctest.Flavor) RecordedLaunch {
				return RecordedLaunch{ID: testLaunchID, Container: f.Container + "; reboot", Runtime: f.Runtime}
			},
			want:      LaunchVerdictNone,
			wantCalls: func(windowproctest.Flavor) []string { return nil },
		},
	}
	for _, tt := range tests {
		for _, f := range windowproctest.Flavors {
			t.Run(tt.name+"/"+f.Name, func(t *testing.T) {
				fakes := windowproctest.Install(t, tt.spec(f))
				launch := RecordedLaunch{ID: testLaunchID, Container: f.Container, Runtime: f.Runtime}
				if tt.launch != nil {
					launch = tt.launch(f)
				}

				got := resolveLaunchVerdicts([]Ticket{launchTicket(42, launch)}, emptySnapshot(), io.Discard)

				if got["o/r#42"] != tt.want {
					t.Errorf("verdict = %q, want %q", got["o/r#42"], tt.want)
				}
				calls := fakes.Calls(t)
				if want := tt.wantCalls(f); !slices.Equal(calls, want) {
					t.Errorf("call log = %q, want %q", calls, want)
				}
				for _, c := range calls {
					if strings.Contains(c, "kill") {
						t.Errorf("call %q signals or kills; the reconciler must never do either", c)
					}
				}
			})
		}
	}
}

// -- #1292: RunReconcileOnce wiring -------------------------------------------

// TestRunReconcileOnce_LaunchProbeWiring runs a whole pass over a real state
// file: the collector reads the recorded triple, the probe judges it, and the
// verdict decides the recovery. Through the two Working-gated branches the
// window name stops mattering for a ticket with a recorded launch (dead beside a
// running window recovers with the dead-in-pane comment; live with no window is
// left alone; a failed probe defers with its grace observation intact), and an
// unreadable state file falls back to the window-name rule with one log line
// and never defers on its own.
func TestRunReconcileOnce_LaunchProbeWiring(t *testing.T) {
	stateWithLaunch := `{"schemaVersion":2,"id":"42","stage":"plan_approved","session":{"runId":"r1","launchId":"` + testLaunchID + `","container":"` + testLaunchContainer + `","runtime":"` + testLaunchRuntime + `"}}`
	probeFailed := errors.New("docker exec launch probe: exit status 1")
	tests := []struct {
		name          string
		state         string
		windows       []watch.WindowState
		probeProcs    []sandbox.ProcTarget
		probeErr      error
		wantProbes    int
		wantRecovery  bool
		wantComment   []string
		wantObsKept   bool
		wantStateLogs int // lines about an unreadable state file
	}{
		{name: "dead beside a running window recovers", state: stateWithLaunch, windows: []watch.WindowState{runningImplementWindow},
			wantProbes: 1, wantRecovery: true, wantComment: []string{"`42-implement`", "agent process was gone", attemptMarker}},
		{name: "live with no window is left alone", state: stateWithLaunch,
			probeProcs: []sandbox.ProcTarget{{PID: "7", Start: "1"}}, wantProbes: 1},
		{name: "failed probe and failed listing defers", state: stateWithLaunch, windows: []watch.WindowState{runningImplementWindow},
			probeErr: probeFailed, wantProbes: 1, wantObsKept: true},
		{name: "no state file keeps the window-name rule", state: "", windows: []watch.WindowState{runningImplementWindow}},
		{name: "unreadable state file, live window name: left alone, logged once", state: `{not json`, windows: []watch.WindowState{runningImplementWindow},
			wantStateLogs: 1},
		{name: "unreadable state file, no window: still recovers, never defers on that alone", state: `{not json`,
			wantRecovery: true, wantComment: []string{"window `42` is gone"}, wantStateLogs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeGH(t, `
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Fix thing","labels":[{"name":"Working"}],"assignees":[]}]' ;;
  "issue view") printf '{"comments":[]}' ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  *) exit 1 ;;
esac
`)
			serveChainSnapshot(t, watch.StateSnapshot{Windows: tt.windows})
			dir := t.TempDir()
			if tt.state != "" {
				writeStateFile(t, dir, 42, tt.state)
			}
			s := stubLaunchSeams(t,
				func(context.Context, probeCall) ([]sandbox.ProcTarget, error) { return tt.probeProcs, tt.probeErr },
				func(context.Context, string) ([]string, error) { return nil, errors.New("docker ps: exit status 1") })
			cfg := reconcileConfig()
			cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: dir, Session: "test-session"}}
			seed := time.Now().Add(-10 * time.Minute)
			store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": seed}}}
			mut := &fakeMutator{}
			var buf bytes.Buffer

			_, _ = RunReconcileOnce(cfg, mut, false, &buf, store)

			if len(s.probeCalls) != tt.wantProbes {
				t.Errorf("probe calls = %+v, want %d", s.probeCalls, tt.wantProbes)
			}
			if len(s.probeCalls) > 0 && s.probeCalls[0] != (probeCall{testLaunchRuntime, testLaunchContainer, testLaunchID}) {
				t.Errorf("probe call = %+v, want the triple recorded in the state file", s.probeCalls[0])
			}
			if tt.wantRecovery {
				if len(mut.labelEdits) != 1 || !containsStr(mut.labelEdits[0].add, labelPlanned) || !containsStr(mut.labelEdits[0].remove, labelWorking) {
					t.Fatalf("label edits = %+v, want one Working->Planned recovery", mut.labelEdits)
				}
				if len(mut.comments) != 1 {
					t.Fatalf("comments = %+v, want exactly one", mut.comments)
				}
				for _, want := range tt.wantComment {
					if !strings.Contains(mut.comments[0].body, want) {
						t.Errorf("comment %q does not contain %q", mut.comments[0].body, want)
					}
				}
			} else if len(mut.labelEdits) != 0 || len(mut.comments) != 0 {
				t.Errorf("want no mutation, got edits %+v comments %+v", mut.labelEdits, mut.comments)
			}
			ts, kept := store.state.Observations["o/r#42"]
			if tt.wantObsKept && (!kept || !ts.Equal(seed)) {
				t.Errorf("persisted observation = %v (present=%v), want the seeded %v preserved", ts, kept, seed)
			}
			if got := strings.Count(buf.String(), "#42 pipeline state unreadable"); got != tt.wantStateLogs {
				t.Errorf("unreadable-state log lines = %d, want %d; log:\n%s", got, tt.wantStateLogs, buf.String())
			}
		})
	}
}

// TestRunReconcileOnce_DeadVerdictBesideSurvivingWindowNeverKills covers AC8
// end to end: a whole RunReconcileOnce pass over the real default probe seams
// and windowproctest's fake tmux/docker/podman, where the fake runtime reports
// no process carrying the recorded launch ID (a dead verdict) while the snapshot
// still holds the ticket's running `42-implement` window. The reconcile apply
// recovers the ticket with the dead-in-pane comment (window name, "agent process
// was gone", attemptMarker), and the shared call log of every fake records the
// single probe exec and nothing that kills a window or signals a process.
func TestRunReconcileOnce_DeadVerdictBesideSurvivingWindowNeverKills(t *testing.T) {
	for _, f := range windowproctest.Flavors {
		t.Run(f.Name, func(t *testing.T) {
			fakes := windowproctest.Install(t, f.SandboxSpec(testLaunchID, "1"))
			// windowproctest.Install makes its directory the only PATH entry, so the
			// fake gh lives beside the fake tmux/docker/podman.
			ghScript := `#!/bin/sh
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Fix thing","labels":[{"name":"Working"}],"assignees":[]}]' ;;
  "issue view") printf '{"comments":[]}' ;;
  "api graphql") printf '` + emptyOpenPRPageJSON + `' ;;
  *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(fakes.Dir, "gh"), []byte(ghScript), 0o755); err != nil {
				t.Fatal(err)
			}
			serveChainSnapshot(t, watch.StateSnapshot{Windows: []watch.WindowState{runningImplementWindow}})
			dir := t.TempDir()
			writeStateFile(t, dir, 42, `{"schemaVersion":2,"id":"42","stage":"plan_approved","session":{"runId":"r1","launchId":"`+testLaunchID+`","container":"`+f.Container+`","runtime":"`+f.Runtime+`"}}`)
			cfg := reconcileConfig()
			cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: dir, Session: "test-session"}}
			store := &memStore{state: ReconcileState{Observations: map[string]time.Time{"o/r#42": time.Now().Add(-10 * time.Minute)}}}
			mut := &fakeMutator{}

			_, _ = RunReconcileOnce(cfg, mut, false, io.Discard, store)

			if len(mut.labelEdits) != 1 || !containsStr(mut.labelEdits[0].add, labelPlanned) || !containsStr(mut.labelEdits[0].remove, labelWorking) {
				t.Fatalf("label edits = %+v, want one Working->Planned recovery", mut.labelEdits)
			}
			if len(mut.comments) != 1 {
				t.Fatalf("comments = %+v, want exactly one", mut.comments)
			}
			for _, want := range []string{"`42-implement`", "agent process was gone", attemptMarker} {
				if !strings.Contains(mut.comments[0].body, want) {
					t.Errorf("comment %q does not contain %q", mut.comments[0].body, want)
				}
			}

			calls := fakes.Calls(t)
			wantProbe := f.Runtime + " exec -u dev " + f.Container + " sh -c " + windowproctest.LaunchProbeToken + " _ " + testLaunchID
			if want := []string{wantProbe}; !slices.Equal(calls, want) {
				t.Errorf("call log = %q, want only the launch probe %q", calls, want)
			}
			for _, c := range calls {
				if strings.Contains(c, "kill") {
					t.Errorf("call %q kills a window or signals a process; the reconciler must never do either", c)
				}
			}
		})
	}
}
