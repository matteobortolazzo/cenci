package dispatch

import (
	"testing"
	"time"

	"github.com/matteobortolazzo/claude-tools/agentwatch/pkg/watch"
)

// reconcileConfig has a 5m grace and a retry budget of 2 so a case can drive
// the retry→fail transition by varying only Attempts.
func reconcileConfig() Config {
	return Config{
		GracePeriod: 5 * time.Minute,
		RetryBudget: 2,
	}
}

var reconcileNow = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

// workingInputs is the failure happy path: one Working ticket #42 whose window
// is gone (empty snapshot) and which has no open PR, observed as failing long
// enough ago that grace has elapsed.
func workingInputs() ReconcileInputs {
	return ReconcileInputs{
		Tickets:      []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Working"}}},
		Plans:        []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "approved"}},
		Snapshot:     &watch.StateSnapshot{},
		Now:          reconcileNow,
		Observations: map[string]time.Time{"o/r#42": reconcileNow.Add(-10 * time.Minute)},
		Attempts:     map[string]int{},
		Config:       reconcileConfig(),
	}
}

func onlyRecovery(t *testing.T, res ReconcileResult) Recovery {
	t.Helper()
	if len(res.Recoveries) != 1 {
		t.Fatalf("got %d recoveries, want 1: %+v", len(res.Recoveries), res.Recoveries)
	}
	return res.Recoveries[0]
}

func hasFailed(res ReconcileResult, repo string, number int) bool {
	for _, f := range res.Failed {
		if f.Repo == repo && f.Number == number {
			return true
		}
	}
	return false
}

func TestReconcileGraceNotElapsed(t *testing.T) {
	in := workingInputs()
	// First observation is now; grace has not elapsed.
	in.Observations = map[string]time.Time{}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("expected no recoveries before grace, got %+v", res.Recoveries)
	}
	ts, ok := res.NextObservations["o/r#42"]
	if !ok {
		t.Fatalf("expected an observation to be recorded, got %+v", res.NextObservations)
	}
	if !ts.Equal(reconcileNow) {
		t.Errorf("observation time = %v, want %v", ts, reconcileNow)
	}
}

func TestReconcileGracePreservesFirstSeen(t *testing.T) {
	in := workingInputs()
	// Observed 2m ago; grace (5m) still not elapsed. The first-seen time must be
	// carried forward unchanged (not reset to Now).
	firstSeen := reconcileNow.Add(-2 * time.Minute)
	in.Observations = map[string]time.Time{"o/r#42": firstSeen}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("expected no recoveries, got %+v", res.Recoveries)
	}
	if ts := res.NextObservations["o/r#42"]; !ts.Equal(firstSeen) {
		t.Errorf("observation time = %v, want preserved %v", ts, firstSeen)
	}
}

func TestReconcileRetryUnderBudget(t *testing.T) {
	in := workingInputs()
	in.Attempts = map[string]int{"o/r#42": 1} // 1 < budget 2
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryRetry)
	}
	if rec.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", rec.Attempt)
	}
	if !containsStr(rec.AddLabels, labelPlanned) || !containsStr(rec.RemoveLabels, labelWorking) {
		t.Errorf("expected Working→Planned swap, got add=%v remove=%v", rec.AddLabels, rec.RemoveLabels)
	}
	if rec.Comment == "" {
		t.Error("expected a retry comment")
	}
	// A retried ticket is not surfaced as failed and its observation is cleared.
	if hasFailed(res, "o/r", 42) {
		t.Error("retried ticket must not appear in Failed")
	}
	if _, ok := res.NextObservations["o/r#42"]; ok {
		t.Error("observation must be cleared once the ticket is recovered")
	}
}

func TestReconcileFailedAtBudget(t *testing.T) {
	in := workingInputs()
	in.Attempts = map[string]int{"o/r#42": 2} // 2 >= budget 2
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryFailed {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryFailed)
	}
	if !containsStr(rec.AddLabels, labelDispatchFailed) || !containsStr(rec.RemoveLabels, labelWorking) {
		t.Errorf("expected Working→dispatch-failed swap, got add=%v remove=%v", rec.AddLabels, rec.RemoveLabels)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("failed ticket must appear in Failed")
	}
}

func TestReconcileLiveWindowResets(t *testing.T) {
	in := workingInputs()
	in.Snapshot = &watch.StateSnapshot{Windows: []watch.WindowState{
		{WindowName: "42-x", Status: "running"},
	}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a live window must produce no recovery, got %+v", res.Recoveries)
	}
	if _, ok := res.NextObservations["o/r#42"]; ok {
		t.Error("a live window must reset the observation")
	}
}

func TestReconcileStoppedWindowIsDead(t *testing.T) {
	in := workingInputs()
	// A stopped window counts as dead — the signal holds and grace has elapsed.
	in.Snapshot = &watch.StateSnapshot{Windows: []watch.WindowState{
		{WindowName: "42-x", Status: "stopped"},
	}}
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want retry", rec.Kind)
	}
}

func TestReconcileOpenPRResets(t *testing.T) {
	in := workingInputs()
	in.Tickets[0].HasOpenPR = true
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an open PR must produce no recovery, got %+v", res.Recoveries)
	}
	if _, ok := res.NextObservations["o/r#42"]; ok {
		t.Error("an open PR must reset the observation")
	}
}

func TestReconcileSnapshotNilNeverBlind(t *testing.T) {
	in := workingInputs()
	in.Snapshot = nil
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("nil snapshot must never reconcile, got %+v", res.Recoveries)
	}
	// The prior observation is carried forward, not lost.
	if _, ok := res.NextObservations["o/r#42"]; !ok {
		t.Error("nil snapshot must preserve the pending observation")
	}
}

func TestReconcileAlreadyFailedSurfacedNotTouched(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"dispatch-failed"}}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a dispatch-failed ticket must not be touched, got %+v", res.Recoveries)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a dispatch-failed ticket must be surfaced in Failed")
	}
}

func TestReconcilePlanInvalidSurfacedNotTouched(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"plan-invalid"}}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a plan-invalid ticket must not be touched, got %+v", res.Recoveries)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a plan-invalid ticket must be surfaced in Failed")
	}
}

func TestReconcilePlannedNoPlanIsInvalid(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}}}
	in.Plans = nil
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryPlanInvalid {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryPlanInvalid)
	}
	if !containsStr(rec.AddLabels, labelPlanInvalid) || !containsStr(rec.RemoveLabels, labelPlanned) {
		t.Errorf("expected Planned→plan-invalid swap, got add=%v remove=%v", rec.AddLabels, rec.RemoveLabels)
	}
	if rec.Comment == "" {
		t.Error("expected a plan-invalid comment")
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a plan-invalid ticket must be surfaced in Failed")
	}
}

func TestReconcilePlannedUnapprovedPlanLeftAlone(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}}}
	in.Plans = []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "draft"}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an unapproved plan is a normal state, got %+v", res.Recoveries)
	}
}

func TestReconcileOrphanPlanReportOnly(t *testing.T) {
	in := workingInputs()
	// A plan whose ticket is not open (not in Tickets).
	in.Tickets = nil
	in.Plans = []Plan{{Repo: "o/r", Path: ".plans/99-gone.md", TicketID: 99, Status: "approved"}}
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryOrphanPlan {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryOrphanPlan)
	}
	if len(rec.AddLabels) != 0 || len(rec.RemoveLabels) != 0 || rec.Comment != "" {
		t.Errorf("orphan-plan must be report-only, got add=%v remove=%v comment=%q",
			rec.AddLabels, rec.RemoveLabels, rec.Comment)
	}
}

func TestReconcileOrphanNotReportedWhenTicketOpen(t *testing.T) {
	in := workingInputs()
	// #42 is Working (open) with a matching plan — not an orphan, and the window
	// is gone so it is a normal retry, not an orphan report.
	res := Reconcile(in)
	for _, rec := range res.Recoveries {
		if rec.Kind == RecoveryOrphanPlan {
			t.Errorf("plan for an open ticket must not be reported as orphan: %+v", rec)
		}
	}
}

func TestReconcileMultiRepoNoKeyCollision(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{
		{Repo: "o/a", Number: 42, Labels: []string{"Working"}},
		{Repo: "o/b", Number: 42, Labels: []string{"Working"}},
	}
	in.Plans = nil
	// Only repo a has an elapsed observation; repo b starts fresh.
	in.Observations = map[string]time.Time{"o/a#42": reconcileNow.Add(-10 * time.Minute)}
	in.Attempts = map[string]int{}
	res := Reconcile(in)

	// a is past grace → retry; b just started → observation only.
	if !hasRecovery(res, "o/a", 42, RecoveryRetry) {
		t.Errorf("repo a #42 should retry, got %+v", res.Recoveries)
	}
	if hasRecovery(res, "o/b", 42, RecoveryRetry) {
		t.Errorf("repo b #42 should not retry yet, got %+v", res.Recoveries)
	}
	if _, ok := res.NextObservations["o/b#42"]; !ok {
		t.Error("repo b #42 should have a fresh observation")
	}
}

func hasRecovery(res ReconcileResult, repo string, number int, kind RecoveryKind) bool {
	for _, r := range res.Recoveries {
		if r.Ticket.Repo == repo && r.Ticket.Number == number && r.Kind == kind {
			return true
		}
	}
	return false
}
