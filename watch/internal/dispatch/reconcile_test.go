package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// reconcileConfig has a 5m grace and a retry budget of 2 so a case can drive
// the retry→fail transition by varying only Attempts.
func reconcileConfig() Config {
	return Config{
		GracePeriod:      5 * time.Minute,
		RetryBudget:      2,
		ApplyRetryBudget: 3,
	}
}

var reconcileNow = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

// workingInputs is the failure happy path: one Working ticket #42 whose window
// is gone (empty snapshot) and which has no open PR, observed as failing long
// enough ago that grace has elapsed. Labels carry both Working and Planned
// (#828 review fix #1): Planned is never removed while Working is added, so
// Working+Planned -- not bare Working -- is the realistic combined-label
// state of every in-flight implementation session on a real board.
func workingInputs() ReconcileInputs {
	return ReconcileInputs{
		Tickets:      []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Working", "Planned"}}},
		Plans:        []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "planned"}},
		Snapshot:     &watch.StateSnapshot{},
		Now:          reconcileNow,
		Observations: map[string]time.Time{"o/r#42": reconcileNow.Add(-10 * time.Minute)},
		Attempts:     map[string]int{},
		Config:       reconcileConfig(),
	}
}

// awaitingInputPlans is the realistic plan shape for an Input Needed ticket
// (watch/docs/test-strategy.md rule 17): the unattended planner escalated, so
// its draft on disk reads status: awaiting-input. workingInputs() carries a
// status: planned plan, which for an Input Needed ticket is the contradictory
// #926 shape the plan-driven heal converges -- the pre-#926 Input Needed
// fixtures therefore swap to this plan so they keep pinning the unchanged
// escalation behavior (zero recoveries, stray-Working cleanup).
func awaitingInputPlans() []Plan {
	return []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "awaiting-input"}}
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

// TestReconcileReconcileStuckSurfacedNotTouched mirrors
// TestReconcileAlreadyFailedSurfacedNotTouched above: a ticket already
// escalated to the reconcile-stuck terminal label must never be re-processed
// (no recovery) and must stay surfaced in Failed so the daemon keeps badging
// it, instead of silently dropping off the board.
func TestReconcileReconcileStuckSurfacedNotTouched(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{labelReconcileStuck}}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a reconcile-stuck ticket must not be touched, got %+v", res.Recoveries)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a reconcile-stuck ticket must be surfaced in Failed")
	}
}

// -- escalated tickets (#826): Input Needed -> Escalated, never Failed -----

func hasEscalated(res ReconcileResult, repo string, number int) bool {
	for _, e := range res.Escalated {
		if e.Repo == repo && e.Number == number {
			return true
		}
	}
	return false
}

// TestReconcileInputNeededTicket_EscalatedNeverFailed covers the plan's
// central reconciler requirement: a ticket labeled "Input Needed" must be
// recorded in ReconcileResult.Escalated, never touched (zero Recoveries),
// never surfaced in Failed (which would badge it as a dispatch failure
// instead of an escalation), and never observed (its window is gone by
// design -- the escalating run stopped cleanly).
func TestReconcileInputNeededTicket_EscalatedNeverFailed(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Input Needed"}}}
	in.Plans = awaitingInputPlans() // realistic Input Needed shape; a planned plan would trip the #926 heal
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an escalated ticket must not be touched, got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an escalated ticket must never appear in Failed")
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("an escalated ticket must appear in Escalated")
	}
	if _, ok := res.NextObservations["o/r#42"]; ok {
		t.Error("an escalated ticket must not carry forward a grace-period observation")
	}
}

// TestReconcileInputNeededTicket_EvaluatedBeforePlannedBranch proves the
// short-circuit's placement: a ticket that somehow carries both Input
// Needed and Planned must classify as escalated, not fall into the
// plan-invalid inverse-leak branch.
func TestReconcileInputNeededTicket_EvaluatedBeforePlannedBranch(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Input Needed", "Planned"}}}
	in.Plans = nil // would trip RecoveryPlanInvalid if evaluated
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an escalated ticket must not be touched even if mislabeled Planned too, got %+v", res.Recoveries)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("an escalated ticket must appear in Escalated")
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

func TestReconcilePlanInvalidGraceNotElapsed(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}}}
	in.Plans = nil
	// No prior observation: the missing plan was just seen. A plan mid-write or
	// living on another host must not be flipped to plan-invalid on sight.
	in.Observations = map[string]time.Time{}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("plan-invalid must wait out grace, got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("a within-grace Planned ticket must not be surfaced as failed")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow) {
		t.Errorf("expected a fresh observation at %v, got %v (present=%v)", reconcileNow, ts, ok)
	}
}

func TestReconcilePlanInvalidNilSnapshotNeverBlind(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}}}
	in.Plans = nil
	in.Snapshot = nil
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("nil snapshot must never escalate plan-invalid, got %+v", res.Recoveries)
	}
	// The prior (elapsed) observation is preserved, not acted on.
	if _, ok := res.NextObservations["o/r#42"]; !ok {
		t.Error("nil snapshot must preserve the pending plan-invalid observation")
	}
}

func TestReconcileAttemptsUnknownDefers(t *testing.T) {
	in := workingInputs()
	// Grace has elapsed and the window is gone, but the durable attempt count
	// could not be read this pass — defer rather than act on an assumed zero.
	in.AttemptsUnknown = map[string]bool{"o/r#42": true}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("unknown attempt count must defer, got %+v", res.Recoveries)
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow.Add(-10*time.Minute)) {
		t.Errorf("deferring must preserve the original first-seen time, got %v (present=%v)", ts, ok)
	}
}

func TestReconcilePlannedNotReadyPlanLeftAlone(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}}}
	in.Plans = []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "draft"}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a not-yet-planned plan is a normal state, got %+v", res.Recoveries)
	}
}

// -- #852 AC3: verified plan absence vs. plan-probe errors, reconciler side -

// TestReconcilePlanProbeGate_MissingPlan_EscalatesToPlanInvalid pins the
// "missing" half of AC3 at the reconciler layer: a Planned, not-Working
// ticket with a verified-absent plan probe (nothing in PlanProbes, the zero
// value) still escalates to RecoveryPlanInvalid once grace has elapsed --
// unchanged from before #852.
func TestReconcilePlanProbeGate_MissingPlan_EscalatesToPlanInvalid(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}}}
	in.Plans = nil

	res := Reconcile(in)
	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryPlanInvalid {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryPlanInvalid)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a genuinely missing plan must still be surfaced in Failed")
	}
}

// TestReconcilePlanProbeGate_UnreadablePlan_DefersRatherThanEscalating pins
// the "unreadable" half of AC3: a Planned, not-Working ticket whose plan
// file exists but could not be read (PlanProbeReadError) must never be
// escalated to plan-invalid on a transient os.ReadFile hiccup -- it defers
// (no Recovery, no Failed entry), preserving the grace observation so a
// later pass can retry once the transient condition clears.
func TestReconcilePlanProbeGate_UnreadablePlan_DefersRatherThanEscalating(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}}}
	in.Plans = nil
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeReadError}

	res := Reconcile(in)
	if len(res.Recoveries) != 0 {
		t.Fatalf("an unreadable plan must produce no recovery, got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an unreadable plan must never be surfaced in Failed (deferred, not failed)")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow.Add(-10*time.Minute)) {
		t.Errorf("expected the grace observation preserved at the original first-seen time, got %v (present=%v)", ts, ok)
	}
}

// TestReconcilePlanInventoryGate_Unreadable_DefersRatherThanEscalating covers
// #884's Q5 requirement at the reconciler layer: a Planned, not-Working
// ticket with a verified-absent plan probe (the zero value, exactly like the
// "missing" test above) must still defer -- never escalate to plan-invalid
// -- when the repo's whole `.plans` directory could not be fully enumerated
// this pass, since an unreadable/partial read can never prove this ticket's
// plan is genuinely absent.
func TestReconcilePlanInventoryGate_Unreadable_DefersRatherThanEscalating(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}}}
	in.Plans = nil
	in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryUnreadable}

	res := Reconcile(in)
	if len(res.Recoveries) != 0 {
		t.Fatalf("an unverified plan inventory must produce no recovery, got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an unverified plan inventory must never be surfaced in Failed (deferred, not failed)")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow.Add(-10*time.Minute)) {
		t.Errorf("expected the grace observation preserved at the original first-seen time, got %v (present=%v)", ts, ok)
	}
}

// -- #881 (Q3): open-PR probe completeness gates the reconciler's two
// hasLiveWindow(...) || t.HasOpenPR label-mutation sites (reconcile.go:212,
// reconcile.go:320) -- mirroring the existing `in.Snapshot == nil` blind
// guard's observation-preserving shape: a non-complete probe means
// t.HasOpenPR cannot be trusted as "verified false", so the reconciler must
// never mutate labels on it, but must still preserve whatever grace
// observation was already pending (never drop it -- watch/docs/dispatch-
// reconcile.md's "a deferred verdict must carry its observation forward"
// rule).

// TestReconcileFailurePath_NonCompleteOpenPRProbe_DefersAndPreservesObservation
// covers the reconcile.go:320 failure-path site: a Working ticket past grace
// with HasOpenPR == false but a non-complete probe must produce NO Recovery
// and must preserve its NextObservations entry, rather than proceeding to
// RecoveryRetry as it would if HasOpenPR were trusted verbatim.
// workingInputs()'s realistic combined ["Working","Planned"] labels
// (watch/docs/test-strategy.md line 17 / #828) are used unchanged.
func TestReconcileFailurePath_NonCompleteOpenPRProbe_DefersAndPreservesObservation(t *testing.T) {
	in := workingInputs()
	in.Tickets[0].OpenPRProbe = OpenPRProbeUnreadable

	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a non-complete open-PR probe must defer (no recovery), got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("a deferred ticket must never be surfaced as failed")
	}
	wantFirstSeen := reconcileNow.Add(-10 * time.Minute) // workingInputs()'s own Observations entry
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(wantFirstSeen) {
		t.Errorf("expected the prior observation preserved at %v, got %v (present=%v)", wantFirstSeen, ts, ok)
	}
}

// TestReconcileFailurePath_CompleteOpenPRProbe_StillProducesRecoveryRetry is
// the failure path's control case: a complete probe (the ordinary healthy
// case) must NOT be swallowed by the new guard -- it still produces
// RecoveryRetry exactly as before #881, proving the guard doesn't silently
// disable the failure path entirely.
func TestReconcileFailurePath_CompleteOpenPRProbe_StillProducesRecoveryRetry(t *testing.T) {
	in := workingInputs()
	in.Tickets[0].OpenPRProbe = OpenPRProbeComplete // explicit, though also the zero value
	in.Attempts = map[string]int{"o/r#42": 1}       // 1 < reconcileConfig's budget 2

	res := Reconcile(in)
	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want %q (a complete probe must never disable the failure path)", rec.Kind, RecoveryRetry)
	}
}

// TestReconcileInputNeededAndWorking_NonCompleteOpenPRProbe_DefersAndPreservesObservation
// covers the reconcile.go:212 crash-cleanup site (the Input Needed + Working
// signature of a crash between the two label edits of a partial resume claim
// or rollback, #853): a non-complete open-PR probe must produce no Recovery
// (no stray-Working cleanup fires), must preserve the grace observation, and
// the ticket must still be recorded into Escalated regardless (per the
// plan's Q3 resolution: "the ticket is still recorded into Escalated
// regardless of whether the cleanup recovery itself fires this pass").
func TestReconcileInputNeededAndWorking_NonCompleteOpenPRProbe_DefersAndPreservesObservation(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Input Needed", "Working"}, OpenPRProbe: OpenPRProbeUnreadable}}
	in.Plans = awaitingInputPlans() // realistic Input Needed shape; a planned plan would trip the #926 heal
	firstSeen := reconcileNow.Add(-10 * time.Minute)
	in.Observations = map[string]time.Time{"o/r#42": firstSeen}

	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a non-complete open-PR probe must defer (no recovery), got %+v", res.Recoveries)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("must still be recorded into Escalated regardless of whether the cleanup recovery fires")
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("must never leak into Failed")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(firstSeen) {
		t.Errorf("expected the prior observation preserved at %v, got %v (present=%v)", firstSeen, ts, ok)
	}
}

// TestReconcileInputNeededAndWorking_CompleteOpenPRProbe_StillProducesCleanupRecovery
// is the crash-cleanup site's control case: a complete probe must not be
// swallowed by the new guard -- the stray-Working cleanup recovery still
// fires exactly as before #881.
func TestReconcileInputNeededAndWorking_CompleteOpenPRProbe_StillProducesCleanupRecovery(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Input Needed", "Working"}, OpenPRProbe: OpenPRProbeComplete}}
	in.Plans = awaitingInputPlans() // realistic Input Needed shape; a planned plan would trip the #926 heal
	in.Observations = map[string]time.Time{"o/r#42": reconcileNow.Add(-10 * time.Minute)}

	res := Reconcile(in)
	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryResumeInterrupted {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryResumeInterrupted)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("must still be recorded into Escalated")
	}
}

func TestReconcileOrphanPlanReportOnly(t *testing.T) {
	in := workingInputs()
	// A plan whose ticket is not open (not in Tickets).
	in.Tickets = nil
	in.Plans = []Plan{{Repo: "o/r", Path: ".plans/99-gone.md", TicketID: 99, Status: "planned"}}
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

// -- #827: every cenci-authored comment carries a distinct <!-- cenci- marker

// TestCommentHelpersEachCarryADistinctCenciMarker covers the plan's
// constraint that every reconciler/skill-authored ticket comment must carry
// a `<!-- cenci-<kind> -->` marker from #827 onward -- resume.go's
// classifier treats a marker-free non-bot comment as a human escalation
// answer, so an unmarked comment helper would silently become a false
// "answered" trigger. Each helper's body must contain a `<!-- cenci-` marker,
// and every helper's marker must be pairwise distinct (#446: content-specific
// assertions, not just non-empty checks).
func TestCommentHelpersEachCarryADistinctCenciMarker(t *testing.T) {
	// resumeInterruptedComment and planHealAwaitingInputComment are
	// deliberately left out: each shares an existing helper's marker
	// (attemptMarker and planHealMarker respectively), so the pairwise-distinct
	// check below would fail. Their markers are covered by dedicated tests
	// (TestPlanHealAwaitingInputComment_StartsWithPlanHealMarker for the latter).
	bodies := map[string]string{
		"retryComment":          retryComment(1, "42-x", "gone"),
		"failedComment":         failedComment(2, "42-x", "gone"),
		"planInvalidComment":    planInvalidComment(),
		"reconcileStuckComment": reconcileStuckComment(),
		"planHealComment":       planHealComment(),
	}

	markers := make(map[string]string, len(bodies))
	for name, body := range bodies {
		if !strings.Contains(body, cenciMarkerPrefix) {
			t.Errorf("%s body does not carry a %q marker: %q", name, cenciMarkerPrefix, body)
			continue
		}
		start := strings.Index(body, cenciMarkerPrefix)
		end := strings.Index(body[start:], "-->")
		if end < 0 {
			t.Fatalf("%s marker has no closing --> : %q", name, body)
		}
		markers[name] = body[start : start+end+len("-->")]
	}

	seen := make(map[string]string, len(markers))
	for name, marker := range markers {
		if other, ok := seen[marker]; ok {
			t.Errorf("%s and %s share the same marker %q, want each helper's marker distinct", name, other, marker)
		}
		seen[marker] = name
	}
}

// -- #828: crashed planning-pickup recovery ----------------------------------

// TestReconcileCrashedPlanningPickup_RecoversToRefined covers the plan's
// stage-aware RecoveryRetry requirement: a Working ticket carrying Refined,
// not Planned, with no matched plan file, is a crashed planning pickup --
// recover it with RemoveLabels: [Working] and empty AddLabels, so it returns
// to plain Refined and is re-picked as a planning candidate next pass,
// rather than today's +Planned recovery which would dead-end it at
// plan-invalid (Planned added, still no plan file).
func TestReconcileCrashedPlanningPickup_RecoversToRefined(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Working", "Refined"}}}
	in.Plans = nil
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryRetry)
	}
	if len(rec.AddLabels) != 0 {
		t.Errorf("AddLabels = %v, want empty (must not add Planned for a plan-less planning pickup)", rec.AddLabels)
	}
	if !containsStr(rec.RemoveLabels, labelWorking) || len(rec.RemoveLabels) != 1 {
		t.Errorf("RemoveLabels = %v, want exactly [%s]", rec.RemoveLabels, labelWorking)
	}
}

// TestReconcileCrashedPlanningPickup_UnverifiedInventoryDefers covers #884's
// Q5 requirement for the stage-aware RecoveryRetry branch, corrected by
// review round 2 finding #1: a Working+Refined ticket with no matched plan
// file (otherwise identical to TestReconcileCrashedPlanningPickup_RecoversToRefined,
// which nils AddLabels) is exactly the realistic shape a real ReadPlans
// directory-read failure produces (Plans always empty on that error, so
// planByTicket[key] is always nil) -- it must therefore defer entirely
// (no recovery at all, grace observation preserved), never fall through to
// the ordinary +Planned retry: an unverified inventory can never prove the
// plan is genuinely absent, and falling through to ordinary retry would
// silently discard whatever unproven plan state (including a possible
// still-open awaiting-input escalation) the unreadable directory was
// actually hiding. Before the review-round-2 fix, only the narrower
// AddLabels-nil branch checked PlanInventories, so this exact realistic
// scenario (Plans == nil, not just planByTicket[key] == nil for some other
// reason) fell through to an ordinary +Planned retry instead of deferring --
// this test pins the corrected top-level gate.
func TestReconcileCrashedPlanningPickup_UnverifiedInventoryDefers(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Working", "Refined"}}}
	in.Plans = nil
	in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryUnreadable}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an unverified plan inventory must produce no recovery, got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an unverified plan inventory must never be surfaced in Failed (deferred, not failed)")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow.Add(-10*time.Minute)) {
		t.Errorf("expected the grace observation preserved at the original first-seen time, got %v (present=%v)", ts, ok)
	}
}

// TestReconcileOrdinaryRetry_UnverifiedInventoryDefers is review round 2
// finding #1's headline regression test: the PLAIN Working+Planned retry
// path (no Refined label at all, so the stage-aware AddLabels-nil branch's
// own condition -- hasLabel(Refined) -- never even applies) is the exact
// realistic shape a real ReadPlans directory-read failure produces for the
// ordinary "window is gone, re-queue the ticket" case (workingInputs()'s own
// happy path, TestReconcileWorkingPlannedTicketPastGrace_RecoveryRetryReachable,
// with Plans forced nil to mimic ReadPlans returning an empty Plans slice on
// a directory-read error). Before the fix, neither individually-gated branch
// (inverse-leak, stage-aware AddLabels-nil) applies to this ticket at all --
// it carries Working, not Planned-without-Working, and it never carries
// Refined -- so it fell straight through to the ordinary +Planned -Working
// retry with no PlanInventories check whatsoever, mutating GitHub state on
// an unproven directory read. Proves the top-level gate, not just the two
// narrower inner guards, actually reaches this path.
func TestReconcileOrdinaryRetry_UnverifiedInventoryDefers(t *testing.T) {
	in := workingInputs() // Working+Planned, ordinary retry shape
	in.Plans = nil        // mimics ReadPlans returning an empty Plans slice on a directory-read error
	in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryUnreadable}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an unverified plan inventory must produce no recovery, got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an unverified plan inventory must never be surfaced in Failed (deferred, not failed)")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow.Add(-10*time.Minute)) {
		t.Errorf("expected the grace observation preserved at the original first-seen time, got %v (present=%v)", ts, ok)
	}
}

// TestReconcileOrdinaryWorkingTicketPlanPresent_UnchangedRetry confirms the
// existing ordinary Working-ticket retry path (+Planned -Working, no
// Refined label involved) stays byte-identical alongside the new
// stage-aware branch above -- mirrors workingInputs()'s own happy path
// (already covered by TestReconcileRetryUnderBudget), asserted directly here
// as the #828 "every other Working ticket keeps today's path verbatim"
// requirement.
//
// workingInputs()'s ticket carries both Working AND Planned (#828 review fix
// #1) -- the realistic combined-label state of every in-flight
// implementation session, since Planned is never removed while Working is
// added. Before the fix, the Planned inverse-leak branch's `continue` fired
// for this exact combination whenever a plan file matched, silently
// swallowing this recovery (0 recoveries, not 1) -- this test would have
// failed with "got 0 recoveries, want 1" pre-fix and passes post-fix, so it
// is the regression test proving the reconciler branch-ordering bug is
// fixed for the plan-present sub-case.
func TestReconcileOrdinaryWorkingTicketPlanPresent_UnchangedRetry(t *testing.T) {
	in := workingInputs() // Working+Planned, no Refined label, plan file present
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryRetry)
	}
	if !containsStr(rec.AddLabels, labelPlanned) || !containsStr(rec.RemoveLabels, labelWorking) {
		t.Errorf("expected Working→Planned swap unchanged, got add=%v remove=%v", rec.AddLabels, rec.RemoveLabels)
	}
}

// TestReconcileWorkingRefinedAndPlannedNoPlan_FallsThroughToStageAwareRetry
// locks in the corrected crashed-planning-pickup predicate boundary (#828
// review fix #1). Before the fix, the Planned inverse-leak branch fired for
// ANY Planned ticket regardless of Working, so a ticket carrying Working +
// Refined + Planned with no plan file was wrongly intercepted and routed to
// plan-invalid, never reaching the failure/retry path below. Post-fix, the
// branch is narrowed to `hasLabel(Planned) && !hasLabel(Working)`, so this
// Working ticket falls through to the ordinary failure/grace/retry path and
// is recovered by the stage-aware RecoveryRetry branch (Refined, no matched
// plan file -> AddLabels nil), exactly like a Refined-only crashed planning
// pickup (TestReconcileCrashedPlanningPickup_RecoversToRefined). A
// Planned-NOT-Working ticket with no plan file still hits plan-invalid
// unchanged (TestReconcilePlannedNoPlanIsInvalid).
func TestReconcileWorkingRefinedAndPlannedNoPlan_FallsThroughToStageAwareRetry(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Working", "Refined", "Planned"}}}
	in.Plans = nil
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want %q (a Working+Planned ticket must fall through to the failure/retry path, not be intercepted by the Planned inverse-leak branch)", rec.Kind, RecoveryRetry)
	}
	if len(rec.AddLabels) != 0 {
		t.Errorf("AddLabels = %v, want empty (stage-aware: Refined with no matched plan must not add Planned)", rec.AddLabels)
	}
	if !containsStr(rec.RemoveLabels, labelWorking) || len(rec.RemoveLabels) != 1 {
		t.Errorf("RemoveLabels = %v, want exactly [%s]", rec.RemoveLabels, labelWorking)
	}
}

// TestReconcileCrashedPlanningPickup_ProbeErrorDefersRatherThanTreatingAbsent
// covers the Phase 6+7 review's medium finding #2, mirroring
// TestReconcilePlanProbeGate_UnreadablePlan_DefersRatherThanEscalating's
// pattern for the RecoveryRetry stage-aware branch: a Working+Refined ticket
// with no matched plan file whose plan probe errored (PlanProbeReadError,
// e.g. a mid-write file) must NOT be treated as verified-absent -- it must
// fall back to the ordinary +Planned retry (AddLabels: [Planned]), not the
// stage-aware nil AddLabels reserved for a genuinely absent plan. Before the
// fix, planByTicket[key] == nil alone (ambiguous between verified-absent and
// probe-errored) drove this decision, so a probe-errored plan silently
// withheld the Planned label at this state-mutating call site exactly like
// a verified-absent one.
func TestReconcileCrashedPlanningPickup_ProbeErrorDefersRatherThanTreatingAbsent(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Working", "Refined"}}}
	in.Plans = nil
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeReadError}
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryRetry)
	}
	if !containsStr(rec.AddLabels, labelPlanned) || len(rec.AddLabels) != 1 {
		t.Errorf("AddLabels = %v, want exactly [%s] (a probe-errored plan must defer to the ordinary retry, never treated as verified-absent)", rec.AddLabels, labelPlanned)
	}
	if !containsStr(rec.RemoveLabels, labelWorking) || len(rec.RemoveLabels) != 1 {
		t.Errorf("RemoveLabels = %v, want exactly [%s]", rec.RemoveLabels, labelWorking)
	}
}

// TestReconcileWorkingPlannedTicketPastGrace_RecoveryRetryReachable is the
// dedicated regression test for the reconciler branch-ordering bug (#828
// review fix #1): before the fix, ANY Planned ticket with a matched plan
// file -- including the normal Planned+Working state of every in-flight
// implementation session -- was silently `continue`d by the Planned
// inverse-leak branch before ever reaching the failure/grace/retry path
// below, so a crashed Working+Planned session could never retry or
// escalate (Working stuck forever). This proves the failure/grace/retry
// path is actually reachable post-fix: a Working+Planned ticket with a
// matched (non-Refined) plan file, past GracePeriod, no live window, under
// RetryBudget gets an ordinary RecoveryRetry with AddLabels: [Planned],
// RemoveLabels: [Working].
func TestReconcileWorkingPlannedTicketPastGrace_RecoveryRetryReachable(t *testing.T) {
	in := workingInputs() // Working+Planned, matched plan file, past grace, empty snapshot, attempts under budget

	rec := onlyRecovery(t, Reconcile(in))
	if rec.Kind != RecoveryRetry {
		t.Fatalf("kind = %q, want %q -- the crashed-retry recovery path must be reachable for a Working+Planned ticket", rec.Kind, RecoveryRetry)
	}
	if !containsStr(rec.AddLabels, labelPlanned) || len(rec.AddLabels) != 1 {
		t.Errorf("AddLabels = %v, want exactly [%s]", rec.AddLabels, labelPlanned)
	}
	if !containsStr(rec.RemoveLabels, labelWorking) || len(rec.RemoveLabels) != 1 {
		t.Errorf("RemoveLabels = %v, want exactly [%s]", rec.RemoveLabels, labelWorking)
	}
}

// TestReconcileCrashedPlanningPickup_RetryBudgetExhaustionStillEscalates
// covers the Test Strategy table's "retry-budget exhaustion still escalates
// to dispatch-failed for the planning-pickup case" requirement: once the
// durable attempt count reaches RetryBudget, a crashed planning pickup
// escalates to RecoveryFailed exactly like any other stranded Working
// ticket -- the stage-aware label payload only changes the retry branch.
func TestReconcileCrashedPlanningPickup_RetryBudgetExhaustionStillEscalates(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Working", "Refined"}}}
	in.Plans = nil
	in.Attempts = map[string]int{"o/r#42": 2} // 2 >= reconcileConfig's budget of 2
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryFailed {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryFailed)
	}
	if !containsStr(rec.AddLabels, labelDispatchFailed) || !containsStr(rec.RemoveLabels, labelWorking) {
		t.Errorf("expected Working→dispatch-failed swap, got add=%v remove=%v", rec.AddLabels, rec.RemoveLabels)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a budget-exhausted crashed planning pickup must appear in Failed")
	}
}

// -- #853: interrupted-resume recovery ---------------------------------------
//
// Classifies a dead Working ticket whose matched plan carries
// `status: awaiting-input` as an interrupted resume (RecoveryResumeInterrupted,
// +Input Needed -Working) rather than an ordinary retry -- it must NEVER
// yield +Planned, which would silently discard the still-unanswered
// escalation and dead-end the ticket. Bounded by the same durable attempt
// budget ordinary retries use (Q2): the recovery's comment shares
// attemptMarker with retryComment so countAttempts tallies it, and once
// attempts >= RetryBudget it escalates to dispatch-failed exactly like any
// other stranded Working ticket, never restoring Input Needed again.

// resumeAwaitingInputDeps mirrors workingInputs()'s realistic Working ticket
// but WITHOUT the Planned label (#828's fixture-realism rule: a ticket mid-
// resume, before finalization, has not yet earned Planned) and with its
// matched plan at status: awaiting-input rather than planned.
func resumeAwaitingInputInputs() ReconcileInputs {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Working"}}}
	in.Plans = awaitingInputPlans()
	return in
}

// TestReconcileDeadWorkingAwaitingInputPlan_ClassifiesAsResumeInterrupted
// covers the classification itself: a dead Working ticket matched to an
// awaiting-input draft, under the retry budget, past grace, produces exactly
// one RecoveryResumeInterrupted restoring Input Needed.
func TestReconcileDeadWorkingAwaitingInputPlan_ClassifiesAsResumeInterrupted(t *testing.T) {
	in := resumeAwaitingInputInputs()
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryResumeInterrupted {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryResumeInterrupted)
	}
	if !containsStr(rec.AddLabels, labelInputNeeded) || len(rec.AddLabels) != 1 {
		t.Errorf("AddLabels = %v, want exactly [%s]", rec.AddLabels, labelInputNeeded)
	}
	if !containsStr(rec.RemoveLabels, labelWorking) || len(rec.RemoveLabels) != 1 {
		t.Errorf("RemoveLabels = %v, want exactly [%s]", rec.RemoveLabels, labelWorking)
	}
	if rec.Comment == "" {
		t.Error("expected a resume-interrupted comment")
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an interrupted resume under budget must not be surfaced as Failed")
	}
}

// TestReconcileDeadWorkingAwaitingInputPlan_NeverYieldsPlanned is the
// dedicated regression test for the plan's central invariant: an interrupted
// resume must never be recovered with +Planned, which would silently
// abandon the still-open escalation (the human's answer was never collected)
// and route the ticket back into ordinary dispatch as if it were a fresh,
// answerable plan.
func TestReconcileDeadWorkingAwaitingInputPlan_NeverYieldsPlanned(t *testing.T) {
	in := resumeAwaitingInputInputs()
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if containsStr(rec.AddLabels, labelPlanned) {
		t.Errorf("AddLabels = %v, must never include %q -- an interrupted resume must return to Input Needed, never ordinary Planned", rec.AddLabels, labelPlanned)
	}
}

// TestReconcileDeadWorkingAwaitingInputPlan_BudgetExhausted_EscalatesToDispatchFailed
// covers Q2's bounded restore: once the durable attempt count reaches
// RetryBudget, an interrupted resume escalates to RecoveryFailed exactly
// like any other stranded Working ticket, instead of restoring Input Needed
// yet again -- an environment that fails every relaunch must terminate, not
// loop forever.
func TestReconcileDeadWorkingAwaitingInputPlan_BudgetExhausted_EscalatesToDispatchFailed(t *testing.T) {
	in := resumeAwaitingInputInputs()
	in.Attempts = map[string]int{"o/r#42": 2} // 2 >= reconcileConfig's budget of 2
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryFailed {
		t.Errorf("kind = %q, want %q (budget exhausted must escalate to dispatch-failed, not keep restoring Input Needed)", rec.Kind, RecoveryFailed)
	}
	if !containsStr(rec.AddLabels, labelDispatchFailed) || !containsStr(rec.RemoveLabels, labelWorking) {
		t.Errorf("expected Working→dispatch-failed swap, got add=%v remove=%v", rec.AddLabels, rec.RemoveLabels)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a budget-exhausted interrupted resume must appear in Failed")
	}
}

// TestReconcileDeadWorkingAwaitingInputPlan_UnverifiedInventoryDefers covers
// #884's Q5 requirement for the RecoveryResumeInterrupted branch (Phase 6+7
// review finding #2): the same dead Working ticket matched to an
// awaiting-input draft as the classification test above must instead defer
// -- no recovery at all, grace observation preserved -- when the repo's
// whole `.plans` directory could not be fully enumerated this pass, mirroring
// the inverse-leak and stage-aware-retry gates' identical deferral shape
// (TestReconcilePlanInventoryGate_Unreadable_DefersRatherThanEscalating).
func TestReconcileDeadWorkingAwaitingInputPlan_UnverifiedInventoryDefers(t *testing.T) {
	in := resumeAwaitingInputInputs()
	in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryUnreadable}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an unverified plan inventory must produce no recovery, got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an unverified plan inventory must never be surfaced in Failed (deferred, not failed)")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow.Add(-10*time.Minute)) {
		t.Errorf("expected the grace observation preserved at the original first-seen time, got %v (present=%v)", ts, ok)
	}
}

// TestReconcileDeadWorkingAwaitingInputPlan_PartialInventoryDefers is the
// PlanInventoryPartial sibling of the Unreadable case above -- both
// non-verified classes must defer identically.
func TestReconcileDeadWorkingAwaitingInputPlan_PartialInventoryDefers(t *testing.T) {
	in := resumeAwaitingInputInputs()
	in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryPartial}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an unverified (partial) plan inventory must produce no recovery, got %+v", res.Recoveries)
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an unverified plan inventory must never be surfaced in Failed (deferred, not failed)")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow.Add(-10*time.Minute)) {
		t.Errorf("expected the grace observation preserved at the original first-seen time, got %v (present=%v)", ts, ok)
	}
}

// -- #853: Working+Input Needed cleanup branch (crash-after-partial-label-
// mutation) -------------------------------------------------------------
//
// A ticket carrying BOTH Input Needed and Working is the signature of a
// crash between the two label edits of a partial resume-rollback or claim:
// still recorded into Escalated (never Failed, never dispatch's NeedInput
// pause -- the existing #826 short-circuit), but also cleaned up by removing
// the stray Working with AddLabels: nil (Input Needed is already present --
// this is not a fresh restore).

func inputNeededAndWorkingInputs() ReconcileInputs {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Input Needed", "Working"}}}
	in.Plans = awaitingInputPlans() // realistic Input Needed shape; a planned plan would trip the #926 heal
	return in
}

// TestReconcileInputNeededAndWorking_PastGraceDeadWindow_EmitsCleanupRecoveryAddLabelsNil
// covers the positive case: snapshot present, no live window, no open PR,
// past grace -- the cleanup recovery fires with AddLabels nil (Input Needed
// is already on the ticket) and RemoveLabels: [Working] only.
func TestReconcileInputNeededAndWorking_PastGraceDeadWindow_EmitsCleanupRecoveryAddLabelsNil(t *testing.T) {
	in := inputNeededAndWorkingInputs()
	res := Reconcile(in)

	if !hasEscalated(res, "o/r", 42) {
		t.Error("a ticket carrying both Input Needed and Working must still be recorded in Escalated")
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("the cleanup recovery must never be surfaced as Failed")
	}
	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryResumeInterrupted {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryResumeInterrupted)
	}
	if len(rec.AddLabels) != 0 {
		t.Errorf("AddLabels = %v, want nil (Input Needed is already present -- this is cleanup, not a fresh restore)", rec.AddLabels)
	}
	if !containsStr(rec.RemoveLabels, labelWorking) || len(rec.RemoveLabels) != 1 {
		t.Errorf("RemoveLabels = %v, want exactly [%s]", rec.RemoveLabels, labelWorking)
	}
}

// TestReconcileInputNeededAndWorking_NilSnapshot_DefersPreservesGrace covers
// the never-reconcile-blind guard: without a snapshot the cleanup recovery
// must defer, preserving the grace observation exactly like the ordinary
// failure path does.
func TestReconcileInputNeededAndWorking_NilSnapshot_DefersPreservesGrace(t *testing.T) {
	in := inputNeededAndWorkingInputs()
	in.Snapshot = nil
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("nil snapshot must never fire the cleanup recovery, got %+v", res.Recoveries)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("must still be recorded in Escalated even while the cleanup recovery defers")
	}
	if _, ok := res.NextObservations["o/r#42"]; !ok {
		t.Error("nil snapshot must preserve the pending grace observation, exactly like the failure path")
	}
}

// TestReconcileInputNeededAndWorking_WithinGrace_NoRecoveryYet covers the
// grace-period guard: a just-observed crash state must not immediately fire
// the cleanup recovery.
func TestReconcileInputNeededAndWorking_WithinGrace_NoRecoveryYet(t *testing.T) {
	in := inputNeededAndWorkingInputs()
	in.Observations = map[string]time.Time{} // just observed
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("within-grace must produce no recovery yet, got %+v", res.Recoveries)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("must still be recorded in Escalated while within grace")
	}
	if ts, ok := res.NextObservations["o/r#42"]; !ok || !ts.Equal(reconcileNow) {
		t.Errorf("expected a fresh observation at %v, got %v (present=%v)", reconcileNow, ts, ok)
	}
}

// TestReconcileInputNeededAndWorking_LiveWindow_NoCleanupRecovery covers the
// negative: a live window means the session is not actually interrupted, so
// no cleanup recovery must fire even though the ticket carries the crash-
// shaped label combination.
func TestReconcileInputNeededAndWorking_LiveWindow_NoCleanupRecovery(t *testing.T) {
	in := inputNeededAndWorkingInputs()
	in.Snapshot = &watch.StateSnapshot{Windows: []watch.WindowState{{WindowName: "42-x", Status: "running"}}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a live window must produce no cleanup recovery, got %+v", res.Recoveries)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("must still be recorded in Escalated")
	}
}

// TestReconcileInputNeededAndWorking_OpenPR_NoCleanupRecovery mirrors the
// live-window negative for the open-PR signal.
func TestReconcileInputNeededAndWorking_OpenPR_NoCleanupRecovery(t *testing.T) {
	in := inputNeededAndWorkingInputs()
	in.Tickets[0].HasOpenPR = true
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("an open PR must produce no cleanup recovery, got %+v", res.Recoveries)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("must still be recorded in Escalated")
	}
}

// -- #853: resumeInterruptedComment shares attemptMarker with retryComment --

// TestResumeInterruptedComment_SharesAttemptMarkerButContentDistinctFromRetry
// covers Q2's wiring: resumeInterruptedComment deliberately embeds the SAME
// attemptMarker retryComment uses (so countAttempts' shared tally is not
// doubled by a distinct marker), while its text stays content-distinct from
// retryComment (#446).
func TestResumeInterruptedComment_SharesAttemptMarkerButContentDistinctFromRetry(t *testing.T) {
	retry := retryComment(1, "42-x", "gone")
	resume := resumeInterruptedComment(1, "42-x", "gone")
	if !strings.Contains(retry, attemptMarker) || !strings.Contains(resume, attemptMarker) {
		t.Fatalf("expected both comments to carry attemptMarker: retry=%q resume=%q", retry, resume)
	}
	if retry == resume {
		t.Error("resumeInterruptedComment must be content-distinct from retryComment (#446), even though it deliberately shares attemptMarker")
	}
}

// installFakeGHComments installs a fake `gh` that answers with a
// `{"comments":[{"body":...}]}` payload holding the given comment bodies. The
// payload is written to a file and `cat`, rather than shell-quoted inline,
// since comment bodies contain apostrophes (e.g. "ticket's") that would break a
// single-quoted shell literal. It uses installFakeGHOnPath (not installFakeGH)
// because the script shells out to the real `cat`, which installFakeGH's
// wholesale PATH replacement would make unresolvable.
func installFakeGHComments(t *testing.T, bodies ...string) {
	t.Helper()
	type payloadComment struct {
		Body string `json:"body"`
	}
	payload := struct {
		Comments []payloadComment `json:"comments"`
	}{}
	for _, b := range bodies {
		payload.Comments = append(payload.Comments, payloadComment{Body: b})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling fixture payload: %v", err)
	}
	fixture := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(fixture, data, 0o644); err != nil {
		t.Fatalf("writing fixture payload: %v", err)
	}
	installFakeGHOnPath(t, fmt.Sprintf("cat %q\n", fixture))
}

// TestCountAttempts_CountsResumeInterruptedComment is the direct
// countAttempts-boundary regression for the shared-marker wiring (mirrors
// TestCountAttemptsRegressionAgainstTheThreeNewMarkers's pattern, but
// asserts the OPPOSITE: unlike failedMarker/planInvalidMarker/
// reconcileStuckMarker, a resumeInterruptedComment MUST be counted, since it
// consumes the same durable budget retryComment does.
func TestCountAttempts_CountsResumeInterruptedComment(t *testing.T) {
	installFakeGHComments(t, resumeInterruptedComment(1, "42-x", "gone"))

	n, err := countAttempts("o/r", 42)
	if err != nil {
		t.Fatalf("countAttempts returned unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("countAttempts = %d, want 1 (resumeInterruptedComment must share attemptMarker with retryComment, per #853's shared-budget wiring)", n)
	}
}

// TestCountAttemptsRegressionAgainstTheThreeNewMarkers pins that
// countAttempts' strings.Contains(c.Body, attemptMarker) check still returns
// 0 for a comment thread containing only the three newly back-filled markers
// (failedMarker/planInvalidMarker/reconcileStuckMarker) and no
// attemptMarker -- none of them must be misclassified as a dispatch-attempt
// comment.
func TestCountAttemptsRegressionAgainstTheThreeNewMarkers(t *testing.T) {
	installFakeGHComments(t, failedComment(3, "42-x", "gone"), planInvalidComment(), reconcileStuckComment())

	n, err := countAttempts("o/r", 42)
	if err != nil {
		t.Fatalf("countAttempts returned unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("countAttempts = %d, want 0 (none of the three new markers is attemptMarker)", n)
	}
}

// TestCountAttempts_LargeGhOutputOnFailure_ErrorBounded pins #852 review
// finding #3: `--json comments` stdout can be an entire (possibly
// attacker-authored, on a public repo) comment thread, and a ghTimeout kill
// mid-stream can leave a large partial payload -- the returned error string
// must be bounded rather than splicing that content in verbatim.
func TestCountAttempts_LargeGhOutputOnFailure_ErrorBounded(t *testing.T) {
	installFakeGHOnPath(t, "yes x | tr -d '\\n' | head -c 5000 >&2\nexit 1\n")

	_, err := countAttempts("o/r", 42)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := len(err.Error()); got > maxProbeLogDetailBytes+200 {
		t.Fatalf("error string not bounded: got %d bytes (want roughly <= %d): %q", got, maxProbeLogDetailBytes, err.Error()[:200])
	}
}

// -- #926 / #1241: plan-driven heal -------------------------------------------
//
// Three plan-file-driven heals converge a ticket whose plan file and board
// labels disagree. A and B (#926) cover a plan that already reads
// status: planned; C (#1241) is the mirror drift, a plan that reads
// status: awaiting-input:
//
//	A. Refined, no Planned, no Working  -> +Planned
//	B. Input Needed (stale escalation)  -> +Planned -Working -Input Needed
//	C. Planned, no Working, no Input Needed, plan awaiting-input
//	                                    -> +Input Needed (Planned kept)
//
// Fixtures use realistic combined labels (watch/docs/test-strategy.md rule 17)
// and seed an elapsed observation (a virgin ticket always defers on its first
// pass, dispatch-reconcile.md rule 5) unless a case is about the first pass.
// Guard tables assert the exact NextObservations value (rule 22).

const planHealKey = "o/r#42"

// planHealRefinedInputs is heal A's happy path: #42 carries Refined (plus an
// unrelated "bug" label) but neither Planned nor Working, its matched plan
// reads status: planned, its window is gone, no open PR, and grace elapsed.
func planHealRefinedInputs() ReconcileInputs {
	in := workingInputs() // planned plan, empty snapshot, observation 10m old
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"bug", "Refined"}}}
	return in
}

// planHealInputNeededInputs is heal B's happy path: #42 carries the given
// labels (always including Input Needed) and a matched status: planned plan --
// the contradictory shape an escalation restore leaves when the draft's
// promoting `mv` had in fact landed.
func planHealInputNeededInputs(labels ...string) ReconcileInputs {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: labels}}
	return in
}

// planHealPlannedAwaitingInputInputs is heal C's happy path: #42 carries the
// realistic re-planned label set (Planned plus Refined and an unrelated "bug"
// label) but neither Working nor Input Needed, its matched plan reads
// status: awaiting-input, its window is gone, no open PR, and grace elapsed.
func planHealPlannedAwaitingInputInputs() ReconcileInputs {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"bug", "Refined", "Planned"}}}
	in.Plans = awaitingInputPlans()
	return in
}

// requirePlanHealRecovery asserts the exact shape heals A and B emit (both add
// Planned) and returns the recovery. It is a thin wrapper over
// requirePlanHealShape.
func requirePlanHealRecovery(t *testing.T, res ReconcileResult, wantRemove []string, wantDetailPrefix string) Recovery {
	t.Helper()
	return requirePlanHealShape(t, res, []string{labelPlanned}, wantRemove, wantDetailPrefix)
}

// requirePlanHealShape asserts the exact shape every plan heal emits and
// returns the recovery: a label-convergence RecoveryRetry (never a budgeted
// retry) adding wantAdd and removing wantRemove, the plan-heal marker (never
// the attempt marker), no attempt increment, and no leakage into Failed or a
// lingering grace observation.
func requirePlanHealShape(t *testing.T, res ReconcileResult, wantAdd, wantRemove []string, wantDetailPrefix string) Recovery {
	t.Helper()
	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryRetry)
	}
	if !slices.Equal(rec.AddLabels, wantAdd) {
		t.Errorf("AddLabels = %v, want exactly %v", rec.AddLabels, wantAdd)
	}
	if !slices.Equal(rec.RemoveLabels, wantRemove) {
		t.Errorf("RemoveLabels = %v, want exactly %v", rec.RemoveLabels, wantRemove)
	}
	if rec.Attempt != 0 {
		t.Errorf("Attempt = %d, want 0 (label convergence is not a retry of failed work)", rec.Attempt)
	}
	if !strings.Contains(rec.Comment, planHealMarker) {
		t.Errorf("comment must carry %q, got %q", planHealMarker, rec.Comment)
	}
	if strings.Contains(rec.Comment, attemptMarker) {
		t.Errorf("comment must never carry %q (countAttempts would tally the heal as a dispatch attempt), got %q", attemptMarker, rec.Comment)
	}
	if !strings.HasPrefix(rec.Detail, wantDetailPrefix) {
		t.Errorf("Detail = %q, want prefix %q", rec.Detail, wantDetailPrefix)
	}
	if len(res.Failed) != 0 {
		t.Errorf("a heal must never surface the ticket in Failed, got %+v", res.Failed)
	}
	if ts, ok := res.NextObservations[planHealKey]; ok {
		t.Errorf("the grace observation must be cleared once the heal fires, got %v", ts)
	}
	return rec
}

// requireObservation asserts NextObservations holds exactly one entry for #42
// equal to want.
func requireObservation(t *testing.T, res ReconcileResult, want time.Time) {
	t.Helper()
	ts, ok := res.NextObservations[planHealKey]
	if !ok || !ts.Equal(want) {
		t.Errorf("NextObservations[%s] = %v (present=%v), want %v", planHealKey, ts, ok, want)
	}
	if len(res.NextObservations) != 1 {
		t.Errorf("NextObservations = %v, want exactly the one %s entry", res.NextObservations, planHealKey)
	}
}

// planHealGuardCase blocks exactly one guard signal of a plan heal. The three
// heals share the signals but not the live-window/open-PR behavior: heals A and
// C defer (keep the pending observation) on every guard, heal B drops the
// observation on those two, mirroring its stray-Working cleanup.
type planHealGuardCase struct {
	name                  string
	seed                  time.Time
	healBDropsObservation bool // heal B drops (rather than keeps) the seeded observation
	mutate                func(in *ReconcileInputs)
}

func planHealGuardCases() []planHealGuardCase {
	elapsed := reconcileNow.Add(-10 * time.Minute)
	return []planHealGuardCase{
		{"inventory unreadable", elapsed, false, func(in *ReconcileInputs) {
			in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryUnreadable}
		}},
		{"inventory partial", elapsed, false, func(in *ReconcileInputs) {
			in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryPartial}
		}},
		{"nil snapshot", elapsed, false, func(in *ReconcileInputs) { in.Snapshot = nil }},
		{"open-PR probe unreadable", elapsed, false, func(in *ReconcileInputs) {
			in.Tickets[0].OpenPRProbe = OpenPRProbeUnreadable
		}},
		{"live window", elapsed, true, func(in *ReconcileInputs) {
			in.Snapshot = &watch.StateSnapshot{Windows: []watch.WindowState{{WindowName: "42-x", Status: "running"}}}
		}},
		{"open PR", elapsed, true, func(in *ReconcileInputs) { in.Tickets[0].HasOpenPR = true }},
		{"grace not elapsed", reconcileNow.Add(-2 * time.Minute), false, func(in *ReconcileInputs) {}},
	}
}

// requireNoObservation asserts no observation was recorded for #42.
func requireNoObservation(t *testing.T, res ReconcileResult) {
	t.Helper()
	if ts, ok := res.NextObservations[planHealKey]; ok {
		t.Errorf("expected no grace observation for %s, got %v", planHealKey, ts)
	}
}

// -- heal A: Refined, no Planned, no Working ---------------------------------

// TestReconcilePlanHeal_RefinedWithPlannedPlan_AddsPlanned covers AC "Refined,
// without Planned and without Working, whose matched plan reads status:
// planned, converges to Planned" and "-> +Planned, comment posted, no attempt
// increment".
func TestReconcilePlanHeal_RefinedWithPlannedPlan_AddsPlanned(t *testing.T) {
	res := Reconcile(planHealRefinedInputs())

	// wantRemove nil: heal A removes nothing (Working is provably absent).
	rec := requirePlanHealRecovery(t, res, nil, "plan heal (Refined):")
	if hasEscalated(res, "o/r", 42) {
		t.Error("a Refined ticket must never be surfaced in Escalated")
	}

	// The two entry paths stay distinguishable in the reconcile report.
	ordinary := onlyRecovery(t, Reconcile(workingInputs()))
	if rec.Detail == ordinary.Detail {
		t.Errorf("heal A Detail %q must differ from the ordinary retry's %q", rec.Detail, ordinary.Detail)
	}
}

// TestReconcilePlanHeal_Refined_FirstPassOnlyStartsGraceThenHeals pins
// dispatch-reconcile.md rule 5 for heal A: a virgin ticket records exactly
// NextObservations[key] == Now on its first pass, and a later pass past grace
// (fed pass 1's observations) heals it.
func TestReconcilePlanHeal_Refined_FirstPassOnlyStartsGraceThenHeals(t *testing.T) {
	in := planHealRefinedInputs()
	in.Observations = map[string]time.Time{}

	pass1 := Reconcile(in)
	if len(pass1.Recoveries) != 0 {
		t.Fatalf("a virgin ticket must defer on its first pass, got %+v", pass1.Recoveries)
	}
	requireObservation(t, pass1, reconcileNow)

	in.Now = reconcileNow.Add(6 * time.Minute) // past the 5m grace
	in.Observations = pass1.NextObservations
	requirePlanHealRecovery(t, Reconcile(in), nil, "plan heal (Refined):")
}

// TestReconcilePlanHeal_Refined_GuardsDeferAndPreserveObservation covers the
// AC "each guard independently defers the no-Working heal -- unverified
// inventory, nil snapshot, incomplete open-PR probe, live window, open PR,
// grace not elapsed -- and the pending grace observation survives into
// NextObservations in every deferring case". Every case blocks exactly one
// signal; the prior observation must come out byte-identical.
func TestReconcilePlanHeal_Refined_GuardsDeferAndPreservesObservation(t *testing.T) {
	for _, c := range planHealGuardCases() {
		t.Run(c.name, func(t *testing.T) {
			in := planHealRefinedInputs()
			c.mutate(&in)
			in.Observations = map[string]time.Time{planHealKey: c.seed}

			res := Reconcile(in)

			if len(res.Recoveries) != 0 {
				t.Fatalf("a guarded heal must produce no recovery, got %+v", res.Recoveries)
			}
			requireObservation(t, res, c.seed)
			if len(res.Failed) != 0 {
				t.Errorf("a deferred heal must never surface Failed, got %+v", res.Failed)
			}
		})
	}
}

// TestReconcilePlanHeal_Refined_ConvergesAfterHeal pins convergence: once the
// heal's +Planned has landed, the ticket (Refined + Planned, plan present) is
// excluded from the heal's own predicate -- no recovery, no observation -- so
// the budget-free heal cannot loop.
func TestReconcilePlanHeal_Refined_ConvergesAfterHeal(t *testing.T) {
	in := planHealRefinedInputs()
	in.Tickets[0].Labels = []string{"bug", "Refined", "Planned"}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a healed ticket must not match the heal again, got %+v", res.Recoveries)
	}
	requireNoObservation(t, res)
}

// -- heal A: Working shapes keep the budgeted failure path -------------------

// TestReconcilePlanHeal_RefinedWorking_PlannedPlan_KeepsBudgetedRetry covers
// the AC pinning that the heal did not claim a Working ticket: the ordinary
// +Planned -Working RecoveryRetry with retryComment and an incremented attempt.
func TestReconcilePlanHeal_RefinedWorking_PlannedPlan_KeepsBudgetedRetry(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Refined", "Working"}}}
	in.Attempts = map[string]int{planHealKey: 0}
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryRetry {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryRetry)
	}
	if want := []string{labelPlanned}; !slices.Equal(rec.AddLabels, want) {
		t.Errorf("AddLabels = %v, want exactly %v", rec.AddLabels, want)
	}
	if want := []string{labelWorking}; !slices.Equal(rec.RemoveLabels, want) {
		t.Errorf("RemoveLabels = %v, want exactly %v", rec.RemoveLabels, want)
	}
	if rec.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1 (the budgeted path increments)", rec.Attempt)
	}
	if !strings.Contains(rec.Comment, attemptMarker) || strings.Contains(rec.Comment, planHealMarker) {
		t.Errorf("a Working ticket must keep retryComment (attemptMarker, not planHealMarker), got %q", rec.Comment)
	}
	if strings.HasPrefix(rec.Detail, "plan heal") {
		t.Errorf("Detail = %q: the heal must not claim a Working ticket", rec.Detail)
	}
}

// TestReconcilePlanHeal_RefinedWorking_AttemptsUnknownDefers proves the
// budgeted path still owns the shape: an unreadable attempt count defers with
// the observation kept, exactly as before.
func TestReconcilePlanHeal_RefinedWorking_AttemptsUnknownDefers(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Refined", "Working"}}}
	in.AttemptsUnknown = map[string]bool{planHealKey: true}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("unknown attempt count must defer, got %+v", res.Recoveries)
	}
	requireObservation(t, res, reconcileNow.Add(-10*time.Minute))
}

// TestReconcilePlanHeal_RefinedWorking_BudgetExhaustedStillFails pins that
// terminal escalation survives: attempts >= RetryBudget yields RecoveryFailed
// (+dispatch-failed -Working) and the ticket lands in Failed.
func TestReconcilePlanHeal_RefinedWorking_BudgetExhaustedStillFails(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Refined", "Working"}}}
	in.Attempts = map[string]int{planHealKey: 2} // == RetryBudget
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryFailed {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryFailed)
	}
	if want := []string{labelDispatchFailed}; !slices.Equal(rec.AddLabels, want) {
		t.Errorf("AddLabels = %v, want exactly %v", rec.AddLabels, want)
	}
	if want := []string{labelWorking}; !slices.Equal(rec.RemoveLabels, want) {
		t.Errorf("RemoveLabels = %v, want exactly %v", rec.RemoveLabels, want)
	}
	if !hasFailed(res, "o/r", 42) {
		t.Error("a budget-exhausted ticket must appear in Failed")
	}
}

// TestReconcilePlanHeal_StrandedByNilAddLabelsRetry_HealsOncePlanBecomesVisible
// is the regression for the self-inflicted stranding: pass 1 recovers a
// Refined+Working ticket whose plan is verified absent with AddLabels nil
// (dropping only Working, leaving bare Refined); pass 2 sees the plan appear
// and starts the grace clock; pass 3, past grace, heals to +Planned. Before
// #926 the ticket sat at bare Refined forever.
func TestReconcilePlanHeal_StrandedByNilAddLabelsRetry_HealsOncePlanBecomesVisible(t *testing.T) {
	in := workingInputs()
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Refined", "Working"}}}
	in.Plans = nil
	in.Attempts = map[string]int{planHealKey: 0}

	pass1 := Reconcile(in)
	rec := onlyRecovery(t, pass1)
	if rec.Kind != RecoveryRetry || len(rec.AddLabels) != 0 {
		t.Fatalf("pass 1 must be the stage-aware retry with nil AddLabels, got %+v", rec)
	}
	if want := []string{labelWorking}; !slices.Equal(rec.RemoveLabels, want) {
		t.Fatalf("pass 1 RemoveLabels = %v, want %v", rec.RemoveLabels, want)
	}

	// Pass 2: Working removed, the plan file is now visible; the heal starts
	// its own grace clock rather than acting on first sight.
	pass2Now := reconcileNow.Add(time.Minute)
	in2 := workingInputs()
	in2.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Refined"}}}
	in2.Now = pass2Now
	in2.Observations = pass1.NextObservations
	pass2 := Reconcile(in2)
	if len(pass2.Recoveries) != 0 {
		t.Fatalf("pass 2 must only start the grace clock, got %+v", pass2.Recoveries)
	}
	requireObservation(t, pass2, pass2Now)

	// Pass 3: past grace, the plan still reads planned -> +Planned.
	in3 := in2
	in3.Now = pass2Now.Add(6 * time.Minute)
	in3.Observations = pass2.NextObservations
	requirePlanHealRecovery(t, Reconcile(in3), nil, "plan heal (Refined):")
}

// -- heal A: left alone ------------------------------------------------------

// TestReconcilePlanHeal_Refined_PlanNotPlannedLeftAlone covers the AC "a ticket
// whose matched plan is neither planned nor awaiting-input is left alone" (the
// normal human-in-loop state): no recovery, and no grace observation recorded.
func TestReconcilePlanHeal_Refined_PlanNotPlannedLeftAlone(t *testing.T) {
	in := planHealRefinedInputs()
	in.Plans = []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "draft"}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a draft plan must be left alone, got %+v", res.Recoveries)
	}
	requireNoObservation(t, res)
}

// TestReconcilePlanHeal_NoRefinedLabelLeftAlone covers the AC "a ticket without
// Refined whose matched plan reads status: planned is left alone": the heal
// never overrides a human who parked a ticket by removing board-state labels.
func TestReconcilePlanHeal_NoRefinedLabelLeftAlone(t *testing.T) {
	for _, labels := range [][]string{{}, {"bug"}} {
		t.Run(fmt.Sprintf("labels=%v", labels), func(t *testing.T) {
			in := planHealRefinedInputs()
			in.Tickets[0].Labels = labels
			res := Reconcile(in)

			if len(res.Recoveries) != 0 {
				t.Fatalf("a ticket without Refined must be left alone, got %+v", res.Recoveries)
			}
			requireNoObservation(t, res)
		})
	}
}

// TestReconcilePlanHeal_TerminalLabelsLeftAlone covers the AC "a ticket
// carrying dispatch-failed, plan-invalid, or reconcile-stuck plus a matched
// status: planned plan is left alone": the terminal short-circuit still wins
// (ticket surfaced in Failed, never healed), including ahead of heal B's
// Input Needed branch.
func TestReconcilePlanHeal_TerminalLabelsLeftAlone(t *testing.T) {
	cases := [][]string{
		{"Refined", labelDispatchFailed},
		{"Refined", labelPlanInvalid},
		{"Refined", labelReconcileStuck},
		{"Refined", "Input Needed", labelDispatchFailed},
	}
	for _, labels := range cases {
		t.Run(fmt.Sprintf("labels=%v", labels), func(t *testing.T) {
			in := planHealRefinedInputs()
			in.Tickets[0].Labels = labels
			res := Reconcile(in)

			if len(res.Recoveries) != 0 {
				t.Fatalf("a terminal ticket must not be healed, got %+v", res.Recoveries)
			}
			if !hasFailed(res, "o/r", 42) {
				t.Error("a terminal ticket must stay surfaced in Failed")
			}
			if hasEscalated(res, "o/r", 42) {
				t.Error("a terminal ticket must not be re-classified as Escalated")
			}
		})
	}
}

// -- heal B: Input Needed with a planned plan --------------------------------

// TestReconcilePlanHeal_InputNeededWithPlannedPlan_HealsAndStaysEscalated
// covers the AC "Refined + Input Needed + matched status: planned plan ->
// +Planned -Working -Input Needed; ticket still present in result.Escalated".
func TestReconcilePlanHeal_InputNeededWithPlannedPlan_HealsAndStaysEscalated(t *testing.T) {
	res := Reconcile(planHealInputNeededInputs("Refined", "Input Needed"))

	requirePlanHealRecovery(t, res, []string{labelWorking, labelInputNeeded}, "plan heal (Input Needed):")
	if !hasEscalated(res, "o/r", 42) {
		t.Error("an Input Needed ticket must be appended to Escalated on every pass, including the pass the heal fires")
	}
}

// TestReconcilePlanHeal_InputNeededWorkingWithPlannedPlan_HealSupersedesStrayWorkingCleanup
// covers the AC "Refined + Input Needed + Working + planned plan -> the heal
// fires (not the stray-Working cleanup)".
func TestReconcilePlanHeal_InputNeededWorkingWithPlannedPlan_HealSupersedesStrayWorkingCleanup(t *testing.T) {
	res := Reconcile(planHealInputNeededInputs("Refined", "Input Needed", "Working"))

	rec := requirePlanHealRecovery(t, res, []string{labelWorking, labelInputNeeded}, "plan heal (Input Needed):")
	if rec.Kind == RecoveryResumeInterrupted {
		t.Error("the plan heal must supersede the stray-Working cleanup, not yield RecoveryResumeInterrupted")
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("an Input Needed ticket must still be appended to Escalated")
	}
}

// TestReconcilePlanHeal_InputNeededWithoutRefinedStillHeals pins the ticket's
// assumption that heal B carries no Refined requirement: its predicate is
// Input Needed + a matched status: planned plan only.
func TestReconcilePlanHeal_InputNeededWithoutRefinedStillHeals(t *testing.T) {
	res := Reconcile(planHealInputNeededInputs("Input Needed"))

	requirePlanHealRecovery(t, res, []string{labelWorking, labelInputNeeded}, "plan heal (Input Needed):")
}

// TestReconcilePlanHeal_InputNeeded_FirstPassOnlyStartsGraceThenHeals is heal
// B's rule-5 twin: a virgin Input Needed ticket records exactly Now on its
// first pass (still Escalated), and a later pass past grace heals it.
func TestReconcilePlanHeal_InputNeeded_FirstPassOnlyStartsGraceThenHeals(t *testing.T) {
	in := planHealInputNeededInputs("Refined", "Input Needed")
	in.Observations = map[string]time.Time{}

	pass1 := Reconcile(in)
	if len(pass1.Recoveries) != 0 {
		t.Fatalf("a virgin ticket must defer on its first pass, got %+v", pass1.Recoveries)
	}
	if !hasEscalated(pass1, "o/r", 42) {
		t.Error("the ticket must be Escalated on the deferring pass")
	}
	requireObservation(t, pass1, reconcileNow)

	in.Now = reconcileNow.Add(6 * time.Minute)
	in.Observations = pass1.NextObservations
	requirePlanHealRecovery(t, Reconcile(in), []string{labelWorking, labelInputNeeded}, "plan heal (Input Needed):")
}

// TestReconcilePlanHeal_InputNeededAwaitingInput_UnchangedEscalation covers the
// AC "Input Needed whose matched plan reads awaiting-input is unchanged": with
// no Working, zero recoveries and no observation; with a stray Working, today's
// cleanup (RecoveryResumeInterrupted, remove only Working) -- never the heal.
func TestReconcilePlanHeal_InputNeededAwaitingInput_UnchangedEscalation(t *testing.T) {
	t.Run("without Working", func(t *testing.T) {
		in := planHealInputNeededInputs("Refined", "Input Needed")
		in.Plans = awaitingInputPlans()
		res := Reconcile(in)

		if len(res.Recoveries) != 0 {
			t.Fatalf("an awaiting-input escalation must not be touched, got %+v", res.Recoveries)
		}
		if !hasEscalated(res, "o/r", 42) {
			t.Error("the ticket must be Escalated")
		}
		requireNoObservation(t, res)
	})
	t.Run("with stray Working", func(t *testing.T) {
		in := planHealInputNeededInputs("Refined", "Input Needed", "Working")
		in.Plans = awaitingInputPlans()
		res := Reconcile(in)

		rec := onlyRecovery(t, res)
		if rec.Kind != RecoveryResumeInterrupted {
			t.Errorf("kind = %q, want %q (today's stray-Working cleanup)", rec.Kind, RecoveryResumeInterrupted)
		}
		if len(rec.AddLabels) != 0 {
			t.Errorf("AddLabels = %v, want none (cleanup, not a restore)", rec.AddLabels)
		}
		if want := []string{labelWorking}; !slices.Equal(rec.RemoveLabels, want) {
			t.Errorf("RemoveLabels = %v, want exactly %v", rec.RemoveLabels, want)
		}
		if strings.Contains(rec.Comment, planHealMarker) {
			t.Errorf("the stray-Working cleanup must not carry the plan-heal marker, got %q", rec.Comment)
		}
		if !hasEscalated(res, "o/r", 42) {
			t.Error("the ticket must be Escalated")
		}
	})
}

// TestReconcilePlanHeal_InputNeeded_GuardsDeferAndStayEscalated covers the AC
// "each guard independently defers the Input Needed heal, with the ticket still
// recorded in result.Escalated on the deferred pass", including the inline
// inventory check -- reachable here with a matched plan under a Partial
// inventory because this branch runs upstream of the top-level hold. The
// deferObservation guards keep the seeded observation; the live-window and
// open-PR guards mirror the branch's stray-Working cleanup and drop it.
func TestReconcilePlanHeal_InputNeeded_GuardsDeferAndStayEscalated(t *testing.T) {
	for _, c := range planHealGuardCases() {
		t.Run(c.name, func(t *testing.T) {
			in := planHealInputNeededInputs("Refined", "Input Needed")
			c.mutate(&in)
			in.Observations = map[string]time.Time{planHealKey: c.seed}

			res := Reconcile(in)

			if len(res.Recoveries) != 0 {
				t.Fatalf("a guarded heal must produce no recovery, got %+v", res.Recoveries)
			}
			if !hasEscalated(res, "o/r", 42) {
				t.Error("the ticket must still be recorded in Escalated on a deferred pass")
			}
			if hasFailed(res, "o/r", 42) {
				t.Error("an escalated ticket must never leak into Failed")
			}
			if c.healBDropsObservation {
				requireNoObservation(t, res)
			} else {
				requireObservation(t, res, c.seed)
			}
		})
	}
}

// -- heal C (#1241): Planned, no Working, no Input Needed, awaiting-input plan -

// TestReconcilePlanHeal_PlannedAwaitingInput_AddsInputNeeded covers the AC
// "a Planned ticket (no Working, no Input Needed, no terminal label) with a
// matched status: awaiting-input plan, past grace, produces exactly one
// RecoveryRetry (Attempt 0) adding Input Needed, removing nothing, with the
// plan-heal marker and a `plan heal (Planned):` Detail".
func TestReconcilePlanHeal_PlannedAwaitingInput_AddsInputNeeded(t *testing.T) {
	res := Reconcile(planHealPlannedAwaitingInputInputs())

	// wantRemove nil: heal C removes nothing (Planned is kept, Working is
	// provably absent).
	rec := requirePlanHealShape(t, res, []string{labelInputNeeded}, nil, "plan heal (Planned):")
	if hasEscalated(res, "o/r", 42) {
		t.Error("the ticket joins Escalated on the next pass, once Input Needed is on GitHub, not on the heal pass")
	}

	// The three entry paths stay distinguishable in the reconcile report.
	ordinary := onlyRecovery(t, Reconcile(workingInputs()))
	if rec.Detail == ordinary.Detail {
		t.Errorf("heal C Detail %q must differ from the ordinary retry's %q", rec.Detail, ordinary.Detail)
	}
}

// TestReconcilePlanHeal_PlannedAwaitingInput_WithoutRefinedStillHeals pins the
// ticket's assumption that heal C has no Refined requirement: its predicate is
// Planned (no Working, no Input Needed) plus an awaiting-input plan only.
func TestReconcilePlanHeal_PlannedAwaitingInput_WithoutRefinedStillHeals(t *testing.T) {
	in := planHealPlannedAwaitingInputInputs()
	in.Tickets[0].Labels = []string{"Planned"}

	requirePlanHealShape(t, Reconcile(in), []string{labelInputNeeded}, nil, "plan heal (Planned):")
}

// TestReconcilePlanHeal_PlannedAwaitingInput_FirstPassOnlyStartsGraceThenHeals
// is heal C's rule-5 twin: a virgin ticket records exactly Now on its first
// pass, and a later pass past grace (fed pass 1's observations) heals it.
func TestReconcilePlanHeal_PlannedAwaitingInput_FirstPassOnlyStartsGraceThenHeals(t *testing.T) {
	in := planHealPlannedAwaitingInputInputs()
	in.Observations = map[string]time.Time{}

	pass1 := Reconcile(in)
	if len(pass1.Recoveries) != 0 {
		t.Fatalf("a virgin ticket must defer on its first pass, got %+v", pass1.Recoveries)
	}
	requireObservation(t, pass1, reconcileNow)

	in.Now = reconcileNow.Add(6 * time.Minute) // past the 5m grace
	in.Observations = pass1.NextObservations
	requirePlanHealShape(t, Reconcile(in), []string{labelInputNeeded}, nil, "plan heal (Planned):")
}

// TestReconcilePlanHeal_PlannedAwaitingInput_GuardsDeferAndPreserveObservation
// covers the AC "each guard defers with no Recovery and keeps the pending grace
// observation unchanged": unverified or partial inventory, nil snapshot,
// incomplete open-PR probe, live window, open PR, and grace not elapsed. Unlike
// heal B, heal C keeps the seeded observation on the live-window and open-PR
// guards too (it copies heal A's deferObservation shape), so
// healBDropsObservation is deliberately ignored.
func TestReconcilePlanHeal_PlannedAwaitingInput_GuardsDeferAndPreserveObservation(t *testing.T) {
	for _, c := range planHealGuardCases() {
		t.Run(c.name, func(t *testing.T) {
			in := planHealPlannedAwaitingInputInputs()
			c.mutate(&in)
			in.Observations = map[string]time.Time{planHealKey: c.seed}

			res := Reconcile(in)

			if len(res.Recoveries) != 0 {
				t.Fatalf("a guarded heal must produce no recovery, got %+v", res.Recoveries)
			}
			requireObservation(t, res, c.seed)
			if len(res.Failed) != 0 {
				t.Errorf("a deferred heal must never surface Failed, got %+v", res.Failed)
			}
		})
	}
}

// TestReconcilePlanHeal_PlannedAwaitingInput_ConvergesToEscalatedAfterHeal pins
// convergence: once the heal's +Input Needed has landed (Planned + Input
// Needed, plan still awaiting-input) the Input Needed branch owns the ticket --
// no recovery, recorded in Escalated, never Failed, no observation -- so the
// budget-free heal cannot loop.
func TestReconcilePlanHeal_PlannedAwaitingInput_ConvergesToEscalatedAfterHeal(t *testing.T) {
	in := planHealPlannedAwaitingInputInputs()
	in.Tickets[0].Labels = []string{"bug", "Refined", "Planned", "Input Needed"}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a healed ticket must not match the heal again, got %+v", res.Recoveries)
	}
	if !hasEscalated(res, "o/r", 42) {
		t.Error("a healed Planned + Input Needed ticket must be recorded in Escalated")
	}
	if hasFailed(res, "o/r", 42) {
		t.Error("an escalated ticket must never leak into Failed")
	}
	requireNoObservation(t, res)
}

// TestReconcilePlanHeal_PlannedWorkingAwaitingInput_KeepsInterruptedResume
// covers the AC pinning that heal C did not claim a Working ticket: Planned +
// Working with an awaiting-input plan keeps the budgeted interrupted-resume
// path (+Input Needed -Working, attemptMarker comment, incremented attempt).
func TestReconcilePlanHeal_PlannedWorkingAwaitingInput_KeepsInterruptedResume(t *testing.T) {
	in := workingInputs() // Working + Planned, observation 10m old
	in.Plans = awaitingInputPlans()
	res := Reconcile(in)

	rec := onlyRecovery(t, res)
	if rec.Kind != RecoveryResumeInterrupted {
		t.Errorf("kind = %q, want %q", rec.Kind, RecoveryResumeInterrupted)
	}
	if want := []string{labelInputNeeded}; !slices.Equal(rec.AddLabels, want) {
		t.Errorf("AddLabels = %v, want exactly %v", rec.AddLabels, want)
	}
	if want := []string{labelWorking}; !slices.Equal(rec.RemoveLabels, want) {
		t.Errorf("RemoveLabels = %v, want exactly %v", rec.RemoveLabels, want)
	}
	if rec.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1 (the budgeted path increments)", rec.Attempt)
	}
	if !strings.Contains(rec.Comment, attemptMarker) || strings.Contains(rec.Comment, planHealMarker) {
		t.Errorf("a Working ticket must keep resumeInterruptedComment (attemptMarker, not planHealMarker), got %q", rec.Comment)
	}
	if strings.HasPrefix(rec.Detail, "plan heal") {
		t.Errorf("Detail = %q, must not carry the plan heal prefix", rec.Detail)
	}
}

// TestReconcilePlanHeal_Planned_DraftPlanLeftAlone covers the AC "a Planned
// ticket whose matched plan has any other non-planned status (e.g. draft) is
// left alone": no recovery, and no grace observation recorded.
func TestReconcilePlanHeal_Planned_DraftPlanLeftAlone(t *testing.T) {
	in := planHealPlannedAwaitingInputInputs()
	in.Plans = []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "draft"}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a draft plan must be left alone, got %+v", res.Recoveries)
	}
	requireNoObservation(t, res)
}

// TestReconcilePlanHeal_Planned_PlannedPlanLeftAlone covers the AC "a Planned
// ticket with a planned plan is left alone": the healthy ready-for-dispatch
// shape, no recovery and no grace observation.
func TestReconcilePlanHeal_Planned_PlannedPlanLeftAlone(t *testing.T) {
	in := planHealPlannedAwaitingInputInputs()
	in.Tickets[0].Labels = []string{"Planned"}
	in.Plans = []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "planned"}}
	res := Reconcile(in)

	if len(res.Recoveries) != 0 {
		t.Fatalf("a planned plan must be left alone, got %+v", res.Recoveries)
	}
	requireNoObservation(t, res)
}

// TestReconcilePlanHeal_PlannedAwaitingInput_TerminalLabelsLeftAlone covers the
// AC "tickets carrying dispatch-failed, plan-invalid or reconcile-stuck are
// left alone": the terminal short-circuit wins over heal C even with an
// awaiting-input plan (surfaced in Failed, never Escalated).
func TestReconcilePlanHeal_PlannedAwaitingInput_TerminalLabelsLeftAlone(t *testing.T) {
	for _, terminal := range []string{labelDispatchFailed, labelPlanInvalid, labelReconcileStuck} {
		t.Run(terminal, func(t *testing.T) {
			in := planHealPlannedAwaitingInputInputs()
			in.Tickets[0].Labels = []string{"Refined", "Planned", terminal}
			res := Reconcile(in)

			if len(res.Recoveries) != 0 {
				t.Fatalf("a terminal ticket must not be healed, got %+v", res.Recoveries)
			}
			if !hasFailed(res, "o/r", 42) {
				t.Error("a terminal ticket must stay surfaced in Failed")
			}
			if hasEscalated(res, "o/r", 42) {
				t.Error("a terminal ticket must not be re-classified as Escalated")
			}
		})
	}
}

// -- plan-heal comment marker ------------------------------------------------

// TestCountAttempts_DoesNotCountPlanHealComment is the gh-boundary regression
// for the marker rule: countAttempts must return 0 for a thread whose only
// cenci comment is a plan-heal comment, so a heal never consumes the durable
// RetryBudget. Justified unit-level test: the tally happens behind the gh
// boundary and cannot be observed through Reconcile.
func TestCountAttempts_DoesNotCountPlanHealComment(t *testing.T) {
	installFakeGHComments(t, planHealComment())

	n, err := countAttempts("o/r", 42)
	if err != nil {
		t.Fatalf("countAttempts returned unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("countAttempts = %d, want 0 (planHealMarker is distinct from attemptMarker)", n)
	}
}

// TestCountAttempts_DoesNotCountPlanHealAwaitingInputComment is the heal C twin
// of the regression above: its comment shares planHealMarker, so a thread whose
// only cenci comment is a heal C comment must still tally 0 attempts and never
// consume the durable RetryBudget. Justified unit-level test for the same
// reason: the tally happens behind the gh boundary.
func TestCountAttempts_DoesNotCountPlanHealAwaitingInputComment(t *testing.T) {
	body := planHealAwaitingInputComment()
	if !strings.Contains(body, planHealMarker) {
		// Without the marker the zero tally below would hold vacuously.
		t.Fatalf("heal C comment must carry %q for this test to be meaningful, got %q", planHealMarker, body)
	}
	installFakeGHComments(t, body)

	n, err := countAttempts("o/r", 42)
	if err != nil {
		t.Fatalf("countAttempts returned unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("countAttempts = %d, want 0 (planHealMarker is distinct from attemptMarker)", n)
	}
}

// TestPlanHealAwaitingInputComment_StartsWithPlanHealMarker pins the
// marker-first-line body-format contract for heal C's comment: the first line
// is exactly planHealMarker (the escalation answer classifier excludes it as a
// cenci-authored comment) and attemptMarker never appears. Justified unit-level
// test: TestCommentHelpersEachCarryADistinctCenciMarker cannot check this
// helper because it shares planHealComment's marker.
func TestPlanHealAwaitingInputComment_StartsWithPlanHealMarker(t *testing.T) {
	body := planHealAwaitingInputComment()

	firstLine, _, _ := strings.Cut(body, "\n")
	if firstLine != planHealMarker {
		t.Errorf("first line = %q, want exactly %q", firstLine, planHealMarker)
	}
	if strings.Contains(body, attemptMarker) {
		t.Errorf("comment must never carry %q (countAttempts would tally it as a dispatch attempt), got %q", attemptMarker, body)
	}
}
