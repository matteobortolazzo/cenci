package dispatch

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/pkg/watch"
)

// Board-state labels the reconciler reads and writes.
const (
	labelWorking        = "Working"
	labelPlanned        = "Planned"
	labelDispatchFailed = "dispatch-failed"
	labelPlanInvalid    = "plan-invalid"
)

// attemptMarker is the hidden HTML comment stamped on every retry comment so
// countAttempts can tally durable attempts from the ticket thread across cron
// invocations and daemon restarts (in-memory state would be amnesiac).
const attemptMarker = "<!-- agentwatch-dispatch-attempt -->"

// RecoveryKind is the class of leak a Recovery addresses.
type RecoveryKind string

const (
	RecoveryRetry       RecoveryKind = "retry"        // Working→Planned, re-enters the dispatch queue
	RecoveryFailed      RecoveryKind = "failed"       // Working→dispatch-failed, surfaced for a human
	RecoveryPlanInvalid RecoveryKind = "plan-invalid" // Planned with no parseable plan file
	RecoveryOrphanPlan  RecoveryKind = "orphan-plan"  // plan file whose ticket is not open (report only)
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
}

// ReconcileResult is Reconcile's output. NextObservations is the grace map to
// persist for the next pass; Failed lists every ticket that ends this pass in a
// surfaced leak state (dispatch-failed or plan-invalid) so the daemon can badge
// the snapshot.
type ReconcileResult struct {
	Recoveries       []Recovery
	NextObservations map[string]time.Time
	Failed           []Ticket
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
		// only by a human or a re-plan (which drops the label).
		if hasLabel(t.Labels, labelDispatchFailed) || hasLabel(t.Labels, labelPlanInvalid) {
			res.Failed = append(res.Failed, t)
			continue
		}

		// Inverse leak: a Planned ticket with no parseable plan file. ReadPlans
		// drops unparseable files, so "missing" and "unparseable" both present as
		// no matched plan. A plan that exists but is not approved is a normal
		// human-in-loop state — left alone.
		if hasLabel(t.Labels, labelPlanned) {
			if planByTicket[key] != nil {
				continue
			}
			// A Planned ticket with no local plan file is not always a leak: plan
			// files are ephemeral and may be mid-write, or the plan legitimately
			// lives on another host (agentflow treats "Planned, no local plan" as a
			// normal state). So this path mirrors the failure path's guards —
			// never act blind on a nil snapshot, and require the signal to hold
			// past the grace period before escalating to plan-invalid.
			if in.Snapshot == nil {
				if ts, ok := in.Observations[key]; ok {
					res.NextObservations[key] = ts
				}
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
			if ts, ok := in.Observations[key]; ok {
				res.NextObservations[key] = ts
			}
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
			res.Recoveries = append(res.Recoveries, Recovery{
				Ticket:       t,
				Kind:         RecoveryRetry,
				AddLabels:    []string{labelPlanned},
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
	return fmt.Sprintf("%s\n🔄 agentwatch dispatch attempt %d: window `%s` is gone (last status %s). "+
		"Re-queuing the ticket for pickup.", attemptMarker, attempt, window, status)
}

func failedComment(attempts int, window, status string) string {
	return fmt.Sprintf("⛔ agentwatch dispatch failed after %d attempt(s): window `%s` is gone (last status %s). "+
		"Labeled `%s` — clear the label or re-plan to retry.", attempts, window, status, labelDispatchFailed)
}

func planInvalidComment() string {
	return fmt.Sprintf("⚠️ agentwatch: this ticket is `%s` but has no parseable plan file in `.plans/`. "+
		"Labeled `%s` — re-run planning to produce an approved plan.", labelPlanned, labelPlanInvalid)
}
