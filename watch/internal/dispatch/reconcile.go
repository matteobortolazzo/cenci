package dispatch

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// Board-state labels the reconciler reads and writes.
const (
	labelWorking        = "Working"
	labelPlanned        = "Planned"
	labelDispatchFailed = "dispatch-failed"
	labelPlanInvalid    = "plan-invalid"
	// labelInputNeeded (#826) marks a ticket the unattended planner
	// escalated: it stopped cleanly (no dispatched work in flight, so it is
	// never a dispatch failure), and is blocked on a human answering the
	// open questions posted on the ticket. Distinct from labelDispatchFailed
	// and labelPlanInvalid -- see ReconcileResult.Escalated.
	labelInputNeeded = "Input Needed"
	// labelRefined (#828) marks a ticket the refiner has broken down into an
	// actionable plan candidate but not yet planned -- the stage-aware
	// planning pickup gate (decide.go) and the crashed-planning-pickup
	// recovery branch below both key off it.
	labelRefined = "Refined"
	// labelReconcileStuck (#265) is a terminal label the reconciler may apply:
	// it marks a ticket whose apply-retry budget was exhausted, i.e.
	// reconciliation itself is stuck (distinct from dispatch-failed, which
	// means the dispatched work failed). Recognized by the terminal
	// short-circuit below, so an escalated ticket is never touched again.
	labelReconcileStuck = "reconcile-stuck"
)

// cenciMarkerPrefix is the hidden-HTML-comment prefix every cenci-authored
// ticket comment must carry, from #827 onward: the escalation answer
// classifier (resume.go's classifyComments/isCenciAuthored) treats any
// marker-free, non-bot comment posted after the escalation anchor as a human
// answer, so an unmarked future comment helper would silently become a false
// "answered" trigger. Every comment helper in this file embeds a marker with
// this prefix as the body's first line.
const cenciMarkerPrefix = "<!-- cenci-"

// attemptMarker is the hidden HTML comment stamped on every retry comment so
// countAttempts can tally durable attempts from the ticket thread across cron
// invocations and daemon restarts (in-memory state would be amnesiac).
const attemptMarker = cenciMarkerPrefix + "dispatch-attempt -->"

// failedMarker, planInvalidMarker, and reconcileStuckMarker (#827) back-fill
// hidden markers onto the three comment helpers below that previously had
// none, so they are never mistaken for a human escalation answer by
// resume.go's classifier. Each is distinct from attemptMarker and from each
// other, per watch/docs/error-handling.md #446 -- countAttempts' own
// strings.Contains(c.Body, attemptMarker) check must never match any of them.
const (
	failedMarker         = cenciMarkerPrefix + "dispatch-failed -->"
	planInvalidMarker    = cenciMarkerPrefix + "plan-invalid -->"
	reconcileStuckMarker = cenciMarkerPrefix + "reconcile-stuck -->"
)

// RecoveryKind is the class of leak a Recovery addresses.
type RecoveryKind string

const (
	RecoveryRetry       RecoveryKind = "retry"        // Working→Planned, re-enters the dispatch queue
	RecoveryFailed      RecoveryKind = "failed"       // Working→dispatch-failed, surfaced for a human
	RecoveryPlanInvalid RecoveryKind = "plan-invalid" // Planned with no parseable plan file
	RecoveryOrphanPlan  RecoveryKind = "orphan-plan"  // plan file whose ticket is not open (report only)
	// RecoveryReconcileStuck (#265) is synthesized by the impure runner
	// (applyReconcile), never by the pure engine: it escalates a recovery whose
	// gh apply kept failing for cfg.ApplyRetryBudget consecutive passes to the
	// reconcile-stuck terminal label.
	RecoveryReconcileStuck RecoveryKind = "reconcile-stuck"
	// RecoveryResumeInterrupted (#853) classifies a dead Working ticket whose
	// matched plan is a still-open awaiting-input draft: the resume launch
	// (manual or dispatch-triggered) was interrupted before it could finalize
	// -- Working→Input Needed, restoring the ticket to the human-input queue
	// rather than converting it to ordinary Planned, which would silently
	// discard the unanswered escalation. Also covers the crash-after-partial-
	// label-mutation cleanup: a ticket already carrying both Input Needed and
	// Working (a claim or rollback that landed its first label edit but not
	// its second) removes the stray Working with no relabeling needed.
	RecoveryResumeInterrupted RecoveryKind = "resume-interrupted"
)

// Recovery is the reconciler's verdict for a single stranded item. AddLabels/
// RemoveLabels/Comment are empty for report-only kinds (orphan-plan).
type Recovery struct {
	Ticket       Ticket
	Kind         RecoveryKind
	AddLabels    []string
	RemoveLabels []string
	Comment      string
	Attempt      int    // the attempt number this recovery represents (retry) or reached (failed)
	Detail       string // human-readable log context
}

// ReconcileInputs is the full, explicit input to Reconcile. Observations maps
// ticketKey→first-seen-failing (the grace clock) and Attempts maps
// ticketKey→prior durable attempt-comment count. Snapshot is nil when the
// daemon is unreachable — Reconcile then never reconciles blind.
type ReconcileInputs struct {
	Tickets      []Ticket
	Plans        []Plan
	Snapshot     *watch.StateSnapshot // nil ⇒ daemon unreachable
	Now          time.Time            // injected clock value
	Observations map[string]time.Time // ticketKey → first-seen-failing
	Attempts     map[string]int       // ticketKey → prior attempt-comment count
	// AttemptsUnknown holds ticket keys whose durable attempt count could not be
	// read this pass (a gh error). Reconcile refuses to pick retry-vs-fail on an
	// assumed-zero count for these — acting blind would exceed the retry budget —
	// so it preserves the grace clock and defers the decision to a later pass.
	AttemptsUnknown map[string]bool
	Config          Config

	// PlanProbes maps planKey(repo, ticketId) -> the resolved PlanProbe for
	// every plan file ReadPlans encountered this pass (#852), mirroring
	// Inputs.PlanProbes (decide.go): lets Reconcile distinguish a
	// verified-absent plan from one that is broken in some way (read/parse/
	// ticket-id/staleness error), so the inverse-leak branch below defers
	// (preserves the grace clock) instead of escalating to plan-invalid on a
	// transient read/parse hiccup.
	PlanProbes map[string]PlanProbe
}

// ReconcileResult is Reconcile's output. NextObservations is the grace map to
// persist for the next pass; Failed lists every ticket that ends this pass in a
// surfaced leak state (dispatch-failed or plan-invalid) so the daemon can badge
// the snapshot. Escalated (#826) lists every ticket labeled Input Needed --
// a distinct, non-failure attention state (see watch/pkg/watch/snapshot.go's
// reciprocal NeedInput/Escalated doc comments): it must never be appended to
// Failed, and must never feed dispatch's fleet-wide NeedInput pause.
type ReconcileResult struct {
	Recoveries       []Recovery
	NextObservations map[string]time.Time
	Failed           []Ticket
	Escalated        []Ticket
}

// Reconcile is pure: identical ReconcileInputs yield an identical ordered
// []Recovery with no I/O. Tickets are evaluated in ascending (repo, number)
// order, then orphan plans in (repo, ticketId, path) order, for determinism.
func Reconcile(in ReconcileInputs) ReconcileResult {
	tickets := make([]Ticket, len(in.Tickets))
	copy(tickets, in.Tickets)
	sort.Slice(tickets, func(i, j int) bool {
		if tickets[i].Repo != tickets[j].Repo {
			return tickets[i].Repo < tickets[j].Repo
		}
		return tickets[i].Number < tickets[j].Number
	})

	// Match a plan to a ticket by (repo, ticketId), first-wins on duplicates —
	// mirrors Decide's planByTicket.
	planByTicket := make(map[string]*Plan, len(in.Plans))
	for i := range in.Plans {
		p := &in.Plans[i]
		key := planKey(p.Repo, p.TicketID)
		if _, ok := planByTicket[key]; !ok {
			planByTicket[key] = p
		}
	}

	openTicket := make(map[string]bool, len(tickets))
	for _, t := range tickets {
		openTicket[planKey(t.Repo, t.Number)] = true
	}

	res := ReconcileResult{NextObservations: make(map[string]time.Time)}

	for _, t := range tickets {
		key := planKey(t.Repo, t.Number)

		// Terminal leak states: surface for a human, never touch again. Cleared
		// only by a human or a re-plan (which drops the label). reconcile-stuck
		// (#265) is terminal too: once the apply-retry budget escalates a
		// ticket, reconciliation stops touching it so reconcile_pass_failed
		// never resurfaces for it again.
		if hasLabel(t.Labels, labelDispatchFailed) || hasLabel(t.Labels, labelPlanInvalid) || hasLabel(t.Labels, labelReconcileStuck) {
			res.Failed = append(res.Failed, t)
			continue
		}

		// Escalated (#826): the unattended planner stopped cleanly to ask a
		// human a question. Its window is gone by design (the escalating run
		// exited, not crashed), so it must never enter crash-recovery -- record
		// it and move on, distinct from every leak state above (never Failed)
		// and never reconciled (no Recovery, no grace observation).
		if hasLabel(t.Labels, labelInputNeeded) {
			res.Escalated = append(res.Escalated, t)

			// Crash-after-partial-label-mutation cleanup (#853 AC3): a
			// ticket carrying BOTH Input Needed and Working is the
			// signature of a crash between the two label edits of a
			// partial resume-rollback or claim -- Input Needed is
			// already present, so this is cleanup (remove the stray
			// Working), not a fresh restore (AddLabels stays nil). It
			// still must never reconcile blind, never fire on a live
			// window/open PR, and only fire once past grace -- exactly
			// the same signals the ordinary failure path below applies
			// to a stranded Working ticket.
			if hasLabel(t.Labels, labelWorking) {
				if in.Snapshot == nil {
					deferObservation(in.Observations, res.NextObservations, key)
					continue
				}
				// A non-complete open-PR probe (#881) means t.HasOpenPR
				// could not actually be proven false -- defer (preserve the
				// grace observation), mirroring the blind in.Snapshot == nil
				// guard just above, rather than risking a stray-Working
				// cleanup on unverified input.
				if t.OpenPRProbe != OpenPRProbeComplete {
					deferObservation(in.Observations, res.NextObservations, key)
					continue
				}
				if hasLiveWindow(t.Number, in.Snapshot) || t.HasOpenPR {
					continue
				}
				firstSeen, ok := in.Observations[key]
				if !ok {
					firstSeen = in.Now
				}
				if in.Now.Sub(firstSeen) < in.Config.GracePeriod {
					res.NextObservations[key] = firstSeen
					continue
				}
				res.Recoveries = append(res.Recoveries, Recovery{
					Ticket:       t,
					Kind:         RecoveryResumeInterrupted,
					RemoveLabels: []string{labelWorking},
					Detail:       "ticket carries both Input Needed and Working (crash after partial label mutation); removing the stray Working",
				})
			}
			continue
		}

		// Inverse leak: a Planned, not-Working ticket with a verified-absent
		// plan file (no PlanProbes entry, or PlanProbeAbsent -- #852). A plan
		// that exists but does not yet carry status: planned is a normal
		// human-in-loop state — left alone.
		//
		// The `&& !hasLabel(t.Labels, labelWorking)` guard is load-bearing
		// (#828 review fix #1): Planned is never removed while Working is
		// added, so a Planned+Working ticket is the NORMAL state of every
		// in-flight implementation or crashed-planning-pickup session, not
		// just a plan-missing leak. Without this guard, this branch would
		// `continue` for every such ticket whenever a plan file is present,
		// permanently starving the failure/grace/retry path below --
		// including this ticket's own stage-aware RecoveryRetry branch --
		// of any Planned+Working ticket, so a crashed session would never
		// retry or escalate.
		if hasLabel(t.Labels, labelPlanned) && !hasLabel(t.Labels, labelWorking) {
			if planByTicket[key] != nil {
				continue
			}
			// A plan-probe error (#852) -- the plan file exists but could not
			// be read/parsed/attributed, or its staleness could not be
			// computed -- must never be escalated to plan-invalid on what may
			// be a transient hiccup (a mid-write file, a momentary read
			// error): defer instead, preserving the grace observation exactly
			// like the nil-snapshot guard below, so a later pass can retry
			// once the condition clears. Only a genuinely verified-absent
			// plan (PlanProbeAbsent, the zero value -- no entry at all) falls
			// through to the escalation path.
			if _, gated := planProbeSkip(in.PlanProbes[key]); gated {
				deferObservation(in.Observations, res.NextObservations, key)
				continue
			}
			// A Planned ticket with a verified-absent local plan file is not
			// always a leak: plan files are ephemeral and may be mid-write, or
			// the plan legitimately lives on another host (cenci treats
			// "Planned, no local plan" as a normal state). So this path
			// mirrors the failure path's guards — never act blind on a nil
			// snapshot, and require the signal to hold past the grace period
			// before escalating to plan-invalid.
			if in.Snapshot == nil {
				deferObservation(in.Observations, res.NextObservations, key)
				continue
			}
			firstSeen, ok := in.Observations[key]
			if !ok {
				firstSeen = in.Now
			}
			if in.Now.Sub(firstSeen) < in.Config.GracePeriod {
				res.NextObservations[key] = firstSeen
				continue
			}
			res.Recoveries = append(res.Recoveries, Recovery{
				Ticket:       t,
				Kind:         RecoveryPlanInvalid,
				AddLabels:    []string{labelPlanInvalid},
				RemoveLabels: []string{labelPlanned},
				Comment:      planInvalidComment(),
				Detail:       "Planned ticket has no parseable plan file",
			})
			res.Failed = append(res.Failed, t)
			continue
		}

		// Failure path: dispatched work that may have stranded. Only Working
		// tickets qualify — a Working ticket is never re-dispatched, so the
		// reconciler and dispatcher never race over it.
		if !hasLabel(t.Labels, labelWorking) {
			continue
		}

		// Never reconcile blind: without a snapshot we cannot distinguish a dead
		// window from a healthy one, so preserve the pending grace observation
		// untouched and take no action.
		if in.Snapshot == nil {
			deferObservation(in.Observations, res.NextObservations, key)
			continue
		}

		// A non-complete open-PR probe (#881) means t.HasOpenPR could not
		// actually be proven false -- defer (preserve the grace
		// observation), mirroring the blind in.Snapshot == nil guard just
		// above, rather than risking a retry/failed label mutation on
		// unverified input.
		if t.OpenPRProbe != OpenPRProbeComplete {
			deferObservation(in.Observations, res.NextObservations, key)
			continue
		}

		// A live window or an open PR means the work reached far enough (phase 9
		// opens the PR before moving the label). Clear the grace clock and leave
		// it alone.
		if hasLiveWindow(t.Number, in.Snapshot) || t.HasOpenPR {
			continue
		}

		// The failure signal holds. Start or continue the grace clock, keeping the
		// original first-seen time so the window survives restart/renumber races.
		firstSeen, ok := in.Observations[key]
		if !ok {
			firstSeen = in.Now
		}
		if in.Now.Sub(firstSeen) < in.Config.GracePeriod {
			res.NextObservations[key] = firstSeen
			continue
		}

		// Grace elapsed: recover. Durable attempt count chooses retry vs fail.
		// If the count could not be read this pass, defer rather than act on an
		// assumed zero (which would re-dispatch past the retry budget and could
		// keep a genuinely-failed ticket from ever reaching dispatch-failed).
		if in.AttemptsUnknown[key] {
			res.NextObservations[key] = firstSeen
			continue
		}
		attempts := in.Attempts[key]
		win := matchingWindow(t.Number, in.Snapshot)
		name, lastStatus := windowDetail(t.Number, win)

		if attempts < in.Config.RetryBudget {
			attempt := attempts + 1

			// Interrupted resume (#853): a Working ticket matched to a
			// plan still at status: awaiting-input is a resume launch
			// (manual or dispatch-triggered) that never finalized -- the
			// human's answer was collected but the session died before
			// it could complete. Recovering it with the ordinary
			// +Planned retry below would silently discard the still-open
			// escalation and route the ticket back into dispatch as if
			// it were a fresh, answerable plan (the plan's central
			// never-Planned invariant). Restore Input Needed instead,
			// bounded by the same durable attempt budget (Q2): the
			// comment below shares attemptMarker with retryComment, so
			// countAttempts tallies it, and once attempts >= RetryBudget
			// the existing RecoveryFailed branch below takes over -- no
			// unbounded restore loop. planByTicket[key] != nil already
			// implies clean front-matter parsing (ReadPlans drops
			// probe-errored files), so no extra PlanProbes gate is
			// needed here -- unlike the plan == nil cases below, which
			// remain ambiguous per #852.
			if p := planByTicket[key]; p != nil && p.Status == "awaiting-input" {
				res.Recoveries = append(res.Recoveries, Recovery{
					Ticket:       t,
					Kind:         RecoveryResumeInterrupted,
					AddLabels:    []string{labelInputNeeded},
					RemoveLabels: []string{labelWorking},
					Comment:      resumeInterruptedComment(attempt, name, lastStatus),
					Attempt:      attempt,
					Detail:       fmt.Sprintf("window %s, last status %s (attempt %d): resume interrupted, restoring Input Needed", name, lastStatus, attempt),
				})
				continue
			}

			// Stage-aware retry (#828): a Working ticket carrying Refined
			// with no matched plan file is a crashed planning pickup, not a
			// crashed implementation run -- Planned may or may not also be
			// present (the inverse-leak branch above only claims Planned
			// tickets that are not Working, so a Planned+Refined+Working
			// ticket with no plan file legitimately reaches here too; see
			// TestReconcileWorkingRefinedAndPlannedNoPlan_FallsThroughToStageAwareRetry).
			// Recovering it with today's +Planned would dead-end it at
			// plan-invalid next pass (Planned added, still no plan file);
			// instead, drop only Working so it returns to plain Refined and
			// is re-picked as a planning candidate. Every other Working
			// ticket keeps today's +Planned -Working retry verbatim.
			// planByTicket[key] == nil is ambiguous exactly the way #852
			// exists to fix: nil both for a verified-absent plan and for a
			// plan file that exists but hit a probe error (ReadPlans drops
			// probe-errored files from `plans`, so they are indistinguishable
			// from "no file at all" here). Only a verified-absent plan
			// (PlanProbeAbsent, the zero value -- no PlanProbes entry at all)
			// may nil AddLabels; a probe-errored plan must defer to the
			// ordinary +Planned retry below rather than being treated as
			// verified absence (Phase 6+7 review finding #2) -- this is a
			// state-mutating call site (a durable GitHub label change plus a
			// comment), so acting on unverified input here is exactly the
			// class of bug this ticket closes, mirroring the inverse-leak
			// branch's planProbeSkip gate above.
			addLabels := []string{labelPlanned}
			if hasLabel(t.Labels, labelRefined) && planByTicket[key] == nil && in.PlanProbes[key] == PlanProbeAbsent {
				addLabels = nil
			}

			res.Recoveries = append(res.Recoveries, Recovery{
				Ticket:       t,
				Kind:         RecoveryRetry,
				AddLabels:    addLabels,
				RemoveLabels: []string{labelWorking},
				Comment:      retryComment(attempt, name, lastStatus),
				Attempt:      attempt,
				Detail:       fmt.Sprintf("window %s, last status %s (attempt %d)", name, lastStatus, attempt),
			})
			// Observation resolved into an action; drop it (the ticket leaves
			// Working and re-enters the dispatch queue via its still-present plan).
			continue
		}

		res.Recoveries = append(res.Recoveries, Recovery{
			Ticket:       t,
			Kind:         RecoveryFailed,
			AddLabels:    []string{labelDispatchFailed},
			RemoveLabels: []string{labelWorking},
			Comment:      failedComment(attempts, name, lastStatus),
			Attempt:      attempts,
			Detail:       fmt.Sprintf("window %s, last status %s (%d attempts exhausted)", name, lastStatus, attempts),
		})
		res.Failed = append(res.Failed, t)
	}

	// Orphan plans: plan files whose ticket is not open. Report only — no gh
	// mutation, no auto-delete.
	plans := make([]Plan, len(in.Plans))
	copy(plans, in.Plans)
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].Repo != plans[j].Repo {
			return plans[i].Repo < plans[j].Repo
		}
		if plans[i].TicketID != plans[j].TicketID {
			return plans[i].TicketID < plans[j].TicketID
		}
		return plans[i].Path < plans[j].Path
	})
	for i := range plans {
		p := plans[i]
		if openTicket[planKey(p.Repo, p.TicketID)] {
			continue
		}
		res.Recoveries = append(res.Recoveries, Recovery{
			Ticket: Ticket{Repo: p.Repo, Number: p.TicketID},
			Kind:   RecoveryOrphanPlan,
			Detail: fmt.Sprintf("orphan plan %s (ticket #%d not open)", p.Path, p.TicketID),
		})
	}

	return res
}

// deferObservation preserves key's pending grace-clock observation (if any)
// from observations into next, without producing a Recovery. Shared by every
// "must not act on unverified or blind input" guard in Reconcile -- a nil
// snapshot, a non-complete open-PR probe (#881), or a plan-probe error
// (#852) -- so a ticket stuck behind a chronically blind/non-complete signal
// never silently resets its grace clock (watch/docs/dispatch-reconcile.md's
// "clear the retry counter only for healthy tickets, not deferred verdicts"
// rule).
func deferObservation(observations, next map[string]time.Time, key string) {
	if ts, ok := observations[key]; ok {
		next[key] = ts
	}
}

// hasLiveWindow reports whether the snapshot holds a window for this ticket that
// is not stopped. A window named "<number>" or "<number>-..." with any status
// other than "stopped" is live; a missing or stopped window is dead.
func hasLiveWindow(number int, snap *watch.StateSnapshot) bool {
	if snap == nil {
		return false
	}
	prefix := strconv.Itoa(number)
	for _, w := range snap.Windows {
		if w.WindowName != prefix && !strings.HasPrefix(w.WindowName, prefix+"-") {
			continue
		}
		if w.Status != "stopped" {
			return true
		}
	}
	return false
}

// matchingWindow returns the first window named for this ticket, regardless of
// status, so a recovery can report the dead window's name and last status.
func matchingWindow(number int, snap *watch.StateSnapshot) *watch.WindowState {
	if snap == nil {
		return nil
	}
	prefix := strconv.Itoa(number)
	for i := range snap.Windows {
		w := &snap.Windows[i]
		if w.WindowName == prefix || strings.HasPrefix(w.WindowName, prefix+"-") {
			return w
		}
	}
	return nil
}

// windowDetail resolves the window name and last status to report. A missing
// window reports the bare number and "gone".
func windowDetail(number int, w *watch.WindowState) (name, status string) {
	if w == nil {
		return strconv.Itoa(number), "gone"
	}
	status = w.Status
	if status == "" {
		status = "unknown"
	}
	return w.WindowName, status
}

func retryComment(attempt int, window, status string) string {
	return fmt.Sprintf("%s\n🔄 cenci dispatch attempt %d: window `%s` is gone (last status %s). "+
		"Re-queuing the ticket for pickup.", attemptMarker, attempt, window, status)
}

// resumeInterruptedComment (#853) is posted when the reconciler classifies a
// dead Working ticket with an awaiting-input draft as an interrupted resume.
// It deliberately embeds the SAME attemptMarker retryComment uses, so
// countAttempts' shared durable tally is not doubled by a distinct marker
// (Q2's bounded-restore requirement) -- its text is otherwise content-
// distinct from retryComment (#446).
func resumeInterruptedComment(attempt int, window, status string) string {
	return fmt.Sprintf("%s\n↩️ cenci dispatch attempt %d: the resumed session's window `%s` is gone (last status %s) "+
		"before it could finish. Restoring `%s` so the escalation can be re-triggered.", attemptMarker, attempt, window, status, labelInputNeeded)
}

func failedComment(attempts int, window, status string) string {
	return fmt.Sprintf("%s\n⛔ cenci dispatch failed after %d attempt(s): window `%s` is gone (last status %s). "+
		"Labeled `%s` — clear the label or re-plan to retry.", failedMarker, attempts, window, status, labelDispatchFailed)
}

func planInvalidComment() string {
	return fmt.Sprintf("%s\n⚠️ cenci: this ticket is `%s` but has no parseable plan file in `.plans/`. "+
		"Labeled `%s` — re-run planning to produce a plan.", planInvalidMarker, labelPlanned, labelPlanInvalid)
}

// reconcileStuckComment (#265) is posted when the runner escalates a ticket
// whose gh apply kept failing for the apply-retry budget. It is distinct from
// failedComment: dispatch-failed means the dispatched work failed, while
// reconcile-stuck means reconciliation itself could not apply its verdict.
func reconcileStuckComment() string {
	return fmt.Sprintf("%s\n🛑 cenci: this ticket's reconciliation could not be applied after repeated attempts. "+
		"Labeled `%s` — reconciliation itself is stuck (distinct from `%s`, which means the dispatched work failed). "+
		"Clear the label once `gh` connectivity/permissions are restored to retry.", reconcileStuckMarker, labelReconcileStuck, labelDispatchFailed)
}
