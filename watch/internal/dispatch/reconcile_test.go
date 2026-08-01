package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
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
	bodies := map[string]string{
		"retryComment":          retryComment(1, "42-x", "gone"),
		"failedComment":         failedComment(2, "42-x", "gone"),
		"planInvalidComment":    planInvalidComment(),
		"reconcileStuckComment": reconcileStuckComment(),
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
	in.Plans = []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "awaiting-input"}}
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

// TestCountAttempts_CountsResumeInterruptedComment is the direct
// countAttempts-boundary regression for the shared-marker wiring (mirrors
// TestCountAttemptsRegressionAgainstTheThreeNewMarkers's pattern, but
// asserts the OPPOSITE: unlike failedMarker/planInvalidMarker/
// reconcileStuckMarker, a resumeInterruptedComment MUST be counted, since it
// consumes the same durable budget retryComment does.
func TestCountAttempts_CountsResumeInterruptedComment(t *testing.T) {
	type payloadComment struct {
		Body string `json:"body"`
	}
	payload := struct {
		Comments []payloadComment `json:"comments"`
	}{
		Comments: []payloadComment{
			{Body: resumeInterruptedComment(1, "42-x", "gone")},
		},
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
	type payloadComment struct {
		Body string `json:"body"`
	}
	payload := struct {
		Comments []payloadComment `json:"comments"`
	}{
		Comments: []payloadComment{
			{Body: failedComment(3, "42-x", "gone")},
			{Body: planInvalidComment()},
			{Body: reconcileStuckComment()},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling fixture payload: %v", err)
	}
	// Written to a file and `cat`, rather than shell-quoted inline, since the
	// comment bodies contain apostrophes (e.g. "ticket's") that would
	// otherwise break a single-quoted shell literal.
	fixture := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(fixture, data, 0o644); err != nil {
		t.Fatalf("writing fixture payload: %v", err)
	}
	// installFakeGHOnPath (not installFakeGH): the script below shells out to
	// the real `cat`, which installFakeGH's wholesale PATH replacement would
	// make unresolvable.
	installFakeGHOnPath(t, fmt.Sprintf("cat %q\n", fixture))

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
