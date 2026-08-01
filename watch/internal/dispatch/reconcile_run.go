package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// TicketMutator applies the reconciler's gh side effects. The seam keeps
// RunReconcileOnce testable without shelling out.
type TicketMutator interface {
	EditLabels(repo string, number int, add, remove []string) error
	Comment(repo string, number int, body string) error
	// EnsureLabels (#265) creates any of the named managed labels that don't
	// already exist in repo, so a first-time `--add-label` never hard-fails
	// against a repo that never had the label created.
	EnsureLabels(repo string, names []string) error
}

// ReconcileState is the schema persisted between passes: the grace-observation
// map (ticketKey → first-seen-failing) plus the apply-retry-failure counters
// (ticketKey → consecutive failed-apply-mutation count). Splitting the counter
// out from Observations (#265) lets a failed gh mutation preserve the grace
// clock while still bounding how many passes will keep retrying the apply.
type ReconcileState struct {
	Observations  map[string]time.Time
	ApplyFailures map[string]int
}

// ReconcileStore persists ReconcileState between passes so grace and the
// apply-retry budget survive cron invocations and daemon restarts.
type ReconcileStore interface {
	Load() (ReconcileState, error)
	Save(ReconcileState) error
}

// GHMutator is the real TicketMutator; it shells out to gh, mirroring collect.go.
//
// EnsureLabels caches every (repo, name) it has already confirmed to exist
// (created, or "already exists") in confirmed, guarded by mu, so a later pass
// never re-shells `gh label create` for the same key (#274). createLabel is
// the seam tests inject in place of the real `gh label create` exec call; a
// nil createLabel falls back to createLabelViaGH.
type GHMutator struct {
	mu          sync.Mutex
	confirmed   map[string]struct{}
	createLabel func(repo, name, color, description string) error
}

// EditLabels adds and removes labels on an issue. A no-op call (no labels) does
// not invoke gh.
func (m *GHMutator) EditLabels(repo string, number int, add, remove []string) error {
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	args := []string{"issue", "edit", strconv.Itoa(number), "--repo", repo}
	for _, l := range add {
		args = append(args, "--add-label", l)
	}
	for _, l := range remove {
		args = append(args, "--remove-label", l)
	}
	stdout, stderr, err := execGh(args...)
	if err != nil {
		return fmt.Errorf("gh issue edit #%d in %s: %w: %s", number, repo, err, collapseLines(stdout+stderr))
	}
	return nil
}

// managedLabelSpec is one cenci-owned terminal label's color/description,
// used by EnsureLabels to create the label on first use (#265). No ensure-label
// pattern existed in Go before this; cenci's Go path assumed labels
// pre-exist, which is why dispatch-failed/plan-invalid never got created.
type managedLabelSpec struct {
	color string
	desc  string
}

// managedLabelSpecs are the only labels EnsureLabels knows how to create:
// the reconciler's own terminal labels. Every other label (Working, Planned,
// Refined, ...) is owned and pre-created by cenci's skills.
var managedLabelSpecs = map[string]managedLabelSpec{
	labelDispatchFailed: {
		color: "b60205",
		desc:  "cenci: dispatched work failed after exhausting its retry budget",
	},
	labelPlanInvalid: {
		color: "d93f0b",
		desc:  "cenci: ticket is Planned but has no parseable plan file",
	},
	labelReconcileStuck: {
		color: "5319e7",
		desc:  "cenci: reconciliation itself is stuck (apply-retry budget exhausted)",
	},
}

// managedLabelsAmong returns the subset of names that are cenci-managed
// (and therefore need EnsureLabels), preserving names' order.
func managedLabelsAmong(names []string) []string {
	var out []string
	for _, n := range names {
		if _, ok := managedLabelSpecs[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// EnsureLabels creates each named managed label in repo if it doesn't already
// exist, so the first `--add-label` for a terminal label never hard-fails
// against a repo that never had it pre-created. "already exists" in gh's
// output is treated as success; any other failure (auth, network, ...) is a
// genuine error and surfaces (lessons-learned.md: never infer resource state
// from a blanket exec error, never swallow with a catch-all).
//
// Once a (repo, name) is confirmed (created, or already existed), it is
// cached in m.confirmed (#274) so a later call for the same key skips the
// create call entirely. The create call itself runs without m.mu held, so
// concurrent EnsureLabels calls for different labels don't serialize behind
// a network/exec call; the cache is only touched under the lock. A genuine
// failure is never cached, so the next call retries it.
func (m *GHMutator) EnsureLabels(repo string, names []string) error {
	for _, name := range names {
		spec, ok := managedLabelSpecs[name]
		if !ok {
			continue
		}
		key := repo + "/" + name

		m.mu.Lock()
		if m.confirmed == nil {
			m.confirmed = map[string]struct{}{}
		}
		_, already := m.confirmed[key]
		m.mu.Unlock()
		if already {
			continue
		}

		create := m.createLabel
		if create == nil {
			create = m.createLabelViaGH
		}
		if err := create(repo, name, spec.color, spec.desc); err != nil {
			return err
		}

		m.mu.Lock()
		m.confirmed[key] = struct{}{}
		m.mu.Unlock()
	}
	return nil
}

// createLabelViaGH is the default createLabel implementation: it shells out
// to `gh label create`, classifying "already exists" output as success
// (lessons-learned.md: never infer resource state from a blanket exec error).
//
// labelAlreadyExists is checked against stdout+stderr combined (#852): `gh
// label create` writes its "already exists" diagnostic to stderr, not
// stdout, so checking stdout alone (a mechanical but incorrect port of the
// separate-stream execGh convention) would misclassify every already-exists
// case as a genuine failure and hard-fail every managed-label create on a
// repo where the label already exists.
func (m *GHMutator) createLabelViaGH(repo, name, color, description string) error {
	stdout, stderr, err := execGh("label", "create", name,
		"--repo", repo, "--color", color, "--description", description)
	if err != nil && !labelAlreadyExists(stdout+stderr) {
		return fmt.Errorf("gh label create %s in %s: %w: %s", name, repo, err, collapseLines(stdout+stderr))
	}
	return nil
}

// labelAlreadyExists classifies gh label create's combined output: an
// "already exists" message is success, everything else (auth/network
// failure) must surface as a genuine error — never inferred from a blanket
// exec error (lessons-learned.md).
func labelAlreadyExists(output string) bool {
	return strings.Contains(strings.ToLower(output), "already exists")
}

// Comment posts a comment on an issue. The body is passed as an argv element (no
// shell), so newlines and markup need no escaping.
func (m *GHMutator) Comment(repo string, number int, body string) error {
	stdout, stderr, err := execGh("issue", "comment", strconv.Itoa(number),
		"--repo", repo, "--body", body)
	if err != nil {
		return fmt.Errorf("gh issue comment #%d in %s: %w: %s", number, repo, err, collapseLines(stdout+stderr))
	}
	return nil
}

// countAttempts tallies durable attempt markers on a ticket's comment thread.
// It is called only for Working candidate tickets (few at a time), so the extra
// gh call per candidate is cheap. An error is returned rather than swallowed to
// 0: the caller must not confuse "no attempts yet" with "could not read", since
// the count gates the retry-vs-fail decision.
func countAttempts(repo string, number int) (int, error) {
	stdout, stderr, err := execGh("issue", "view", strconv.Itoa(number),
		"--repo", repo, "--json", "comments")
	if err != nil {
		// Detail is truncated to maxProbeLogDetailBytes (#852 review finding
		// #3): `--json comments` stdout can be an entire (attacker-authored,
		// on a public repo) comment thread, and a ghTimeout kill mid-stream
		// can leave a large partial JSON payload in stdout -- both would
		// otherwise flood this error string unbounded.
		return 0, fmt.Errorf("gh issue view #%d in %s: %w: %s", number, repo, err, truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes))
	}
	var v struct {
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		return 0, fmt.Errorf("parsing comments for #%d in %s: %w", number, repo, err)
	}
	n := 0
	for _, c := range v.Comments {
		if strings.Contains(c.Body, attemptMarker) {
			n++
		}
	}
	return n, nil
}

// reconcileDeps are the collected, impure inputs to a reconcile pass. Separating
// them from RunReconcileOnce lets applyReconcile be exercised without gh.
type reconcileDeps struct {
	Tickets         []Ticket
	Plans           []Plan
	Snapshot        *watch.StateSnapshot
	Attempts        map[string]int
	AttemptsUnknown map[string]bool
	Now             time.Time
	// PlanProbes maps planKey(repo, ticketId) -> the resolved PlanProbe for
	// every plan file ReadPlans encountered this pass (#852), merged across
	// every repo's ReadPlans call, mirroring dispatchDeps.PlanProbes
	// (dispatch.go). Threaded straight through to ReconcileInputs.PlanProbes.
	PlanProbes map[string]PlanProbe
}

// RunReconcileOnce collects tickets, plans, the daemon snapshot, and durable
// attempt counts, runs the pure Reconcile engine, logs every recovery, applies
// the gh mutations (unless dryRun), and persists the grace map. The daemon
// consumes result.Failed to badge the snapshot. It also returns the first of
// the collection/plan/attempt errors encountered during the pass, if any —
// all existing logging is unchanged, this only additionally surfaces the
// error to the caller instead of swallowing it.
func RunReconcileOnce(cfg Config, mut TicketMutator, dryRun bool, out io.Writer, store ReconcileStore) (ReconcileResult, error) {
	if out == nil {
		out = os.Stdout
	}

	// Pre-collection hold probe (#883): reuses the same Load classification
	// applyReconcile's own authoritative load will use later in this same
	// pass, so a held pass (unreadable/corrupt/schema-mismatched/invalid
	// state) burns zero gh issue list/view budget on collection before
	// applyReconcile inevitably aborts anyway. The loaded state itself is
	// discarded here -- applyReconcile always reloads it fresh -- so this
	// probe can never diverge from the abort contract's real coverage.
	if _, err := store.Load(); err != nil {
		logf(out, "reconcile: state unreadable, holding pass (no mutations, state not overwritten): %v\n", err)
		return ReconcileResult{}, err
	}

	// The reconciler deliberately never runs the local-main sync (#822):
	// RunOnce owns it exclusively, once per combined pass (combined.go runs
	// RunOnce then RunReconcileOnce in the same pass) -- passing nil here
	// leaves every collected ticket at the ungated zero value. It also
	// deliberately never resolves dependencies (#825 review fix #1): the
	// reconciler never reads Ticket.DependsOn/DependencyStates, and
	// resolving them here would burn this call's own
	// maxDependencyResolutions gh issue view budget per repo on a result
	// that's immediately discarded, doubling the API-rate-limit exposure of
	// a combined pass for no benefit.
	tickets, err := CollectTickets(cfg.Repos, nil, false, out)
	if err != nil {
		logf(out, "reconcile: collecting tickets: %v\n", err)
	}
	collectErr := err

	var plans []Plan
	planProbes := map[string]PlanProbe{}
	for _, rc := range cfg.Repos {
		ps, probes, err := ReadPlans(rc.Repo, rc.Dir, nil, out)
		if err != nil {
			logf(out, "reconcile: reading plans in %s: %v\n", rc.Dir, err)
			if collectErr == nil {
				collectErr = err
			}
			continue
		}
		plans = append(plans, ps...)
		for k, v := range probes {
			planProbes[k] = v
		}
	}

	snap, _ := ReadSnapshot(watch.DefaultSocketPath()) // nil on error ⇒ Reconcile never reconciles blind

	// Count durable attempts only for Working candidate tickets — the only ones
	// the failure path can act on. A gh read error marks the ticket unknown so
	// Reconcile defers rather than acting on an assumed-zero count.
	attempts := make(map[string]int)
	attemptsUnknown := make(map[string]bool)
	for _, t := range tickets {
		if !hasLabel(t.Labels, labelWorking) {
			continue
		}
		n, err := countAttempts(t.Repo, t.Number)
		if err != nil {
			logf(out, "reconcile: #%d counting attempts: %v\n", t.Number, err)
			attemptsUnknown[planKey(t.Repo, t.Number)] = true
			if collectErr == nil {
				collectErr = err
			}
			continue
		}
		attempts[planKey(t.Repo, t.Number)] = n
	}

	result, applyErr := applyReconcile(cfg, reconcileDeps{
		Tickets:         tickets,
		Plans:           plans,
		Snapshot:        snap,
		Attempts:        attempts,
		AttemptsUnknown: attemptsUnknown,
		Now:             time.Now(),
		PlanProbes:      planProbes,
	}, mut, dryRun, out, store)
	// #883: firstNonNil would let a same-pass collectErr mask the corruption
	// sentinel whenever collection also failed (a likely co-occurrence, e.g.
	// both driven by a wider outage) -- the sentinel must win so a held pass
	// is never misreported as an ordinary reconcile_pass_failed.
	if errors.Is(applyErr, ErrReconcileStateUnreadable) {
		return result, applyErr
	}
	return result, firstNonNil(collectErr, applyErr)
}

// applyReconcile runs the pure engine over already-collected deps, logs, applies
// (unless dryRun), and persists the next grace map. It is the testable core of
// RunReconcileOnce.
func applyReconcile(cfg Config, deps reconcileDeps, mut TicketMutator, dryRun bool, out io.Writer, store ReconcileStore) (ReconcileResult, error) {
	if out == nil {
		out = os.Stdout
	}

	state, err := store.Load()
	if err != nil {
		// #883: any non-nil Load error aborts the mutating pass before
		// Reconcile() ever runs and before the dryRun return below --
		// default-deny for a third-party ReconcileStore, not only the
		// sentinel. State is never overwritten (store.Save is unreachable
		// past this point in the same pass, AC #3), and the corrupt file
		// survives untouched for manual inspection.
		logf(out, "reconcile: state unreadable, holding pass (no mutations, state not overwritten): %v\n", err)
		return ReconcileResult{}, err
	}
	var firstErr error
	obs := state.Observations
	if obs == nil {
		obs = map[string]time.Time{}
	}
	applyFailures := state.ApplyFailures
	if applyFailures == nil {
		applyFailures = map[string]int{}
	}
	result := Reconcile(ReconcileInputs{
		Tickets:         deps.Tickets,
		Plans:           deps.Plans,
		Snapshot:        deps.Snapshot,
		Now:             deps.Now,
		Observations:    obs,
		Attempts:        deps.Attempts,
		AttemptsUnknown: deps.AttemptsUnknown,
		Config:          cfg,
		PlanProbes:      deps.PlanProbes,
	})

	for _, rec := range result.Recoveries {
		logf(out, "%s\n", formatRecovery(rec))
	}

	if dryRun {
		return result, firstErr
	}

	// #265 (code-review finding #1): the pure engine already appends
	// RecoveryFailed/RecoveryPlanInvalid tickets to result.Failed unconditionally,
	// before any apply is attempted. If that recovery's apply then exhausts the
	// budget and escalates successfully below, appending rec.Ticket to
	// result.Failed a second time would duplicate the entry (combined.go's
	// failedWindows() would then emit two identical WindowState entries for one
	// ticket). Track which keys are already present so the escalation append is
	// idempotent regardless of which recovery kind produced it.
	failedKeys := make(map[string]bool, len(result.Failed))
	for _, t := range result.Failed {
		failedKeys[planKey(t.Repo, t.Number)] = true
	}

	// recoveredKeys collects every ticket key that produced a mutating
	// recovery this pass. #265 (code-review finding #2): a ticket that
	// accumulated applyFailures in a prior pass but produces no recovery at
	// all this pass is healthy again (mirrors how the pure engine already
	// drops Observations for a healthy ticket) — its stale counter must be
	// cleared too, or a later, unrelated stranding episode would inherit it
	// and escalate prematurely.
	//
	// "No recovery this pass" is NOT by itself proof of health, though: the
	// pure engine also produces no Recovery when it *defers* a verdict — a
	// nil Snapshot (daemon unreachable, reconcile.go's Snapshot==nil guards)
	// or an unread durable attempt count (AttemptsUnknown) — and in both
	// deferral cases it deliberately carries the grace clock forward into
	// result.NextObservations[key] rather than dropping it. A genuinely
	// healthy ticket has no observation at all (dropped, not carried). So the
	// stale-clear loop below must only clear a key that is absent from BOTH
	// recoveredKeys AND result.NextObservations — clearing on "absent from
	// recoveredKeys" alone would zero the apply-retry counter mid-outage and
	// let a ticket whose apply keeps failing take far longer than
	// cfg.ApplyRetryBudget passes to escalate (or never escalate) while
	// outages and partial recoveries interleave.
	recoveredKeys := make(map[string]bool, len(result.Recoveries))

	for _, rec := range result.Recoveries {
		if len(rec.AddLabels) == 0 && len(rec.RemoveLabels) == 0 {
			// Report-only (orphan-plan): nothing to apply, nothing to track.
			continue
		}
		key := planKey(rec.Ticket.Repo, rec.Ticket.Number)
		recoveredKeys[key] = true
		mutated, err := applyRecovery(mut, rec, out)
		if mutated {
			// The label mutation (the state-changing half) landed on GitHub —
			// the ticket is resolved for this pass regardless of whether the
			// trailing comment also succeeded (#265 silent-failure-hunter
			// finding #3). Any prior apply-failure streak is over. A
			// comment-only failure still surfaces via firstErr/logging but
			// must not bump the counter or re-inject the grace clock, since
			// nothing about GitHub's label state actually failed.
			delete(applyFailures, key)
			if err != nil {
				firstErr = firstNonNil(firstErr, err)
			}
			continue
		}

		// Label mutation failed (#265 AC #3/#4): the pure engine assumed this
		// ticket's observation would clear because the mutation would land —
		// it didn't, so re-inject the original first-seen time (loaded from
		// the store, before Reconcile ran) rather than let the grace clock
		// silently restart next pass. Bump the bounded apply-retry counter
		// separately from the dispatch-attempt RetryBudget.
		if ts, ok := obs[key]; ok {
			result.NextObservations[key] = ts
		}
		applyFailures[key]++
		if applyFailures[key] < cfg.ApplyRetryBudget {
			firstErr = firstNonNil(firstErr, err)
			continue
		}

		// Apply-retry budget exhausted: escalate to reconcile-stuck so this
		// ticket stops resurfacing reconcile_pass_failed forever (#265 AC
		// #4/#6), removing the source label the failed recovery intended to
		// remove (Working or Planned).
		escRec := Recovery{
			Ticket:    rec.Ticket,
			Kind:      RecoveryReconcileStuck,
			AddLabels: []string{labelReconcileStuck},
			// Reuses rec.RemoveLabels as-is: every mutating Recovery kind
			// today (retry/failed/plan-invalid/resume-interrupted, #853)
			// removes exactly the one source label (Working/Planned) the
			// escalation must also remove. Revisit this passthrough if a
			// future recovery kind removes a different/additional set of
			// labels.
			RemoveLabels: rec.RemoveLabels,
			Comment:      reconcileStuckComment(),
			Detail:       fmt.Sprintf("apply-retry budget (%d) exhausted: %s", cfg.ApplyRetryBudget, rec.Detail),
		}
		logf(out, "%s\n", formatRecovery(escRec))
		escMutated, escErr := applyRecovery(mut, escRec, out)
		if !escMutated {
			// gh is still unreachable/failing: keep the counter and the
			// observation and retry escalation next pass — best-effort, only
			// blocked while gh is fully down.
			firstErr = firstNonNil(firstErr, escErr)
			continue
		}
		if escErr != nil {
			// Escalation's label swap landed but its trailing comment failed
			// (same partial-success shape as the ordinary case above) — the
			// ticket is still terminal, so fold into the same success path
			// rather than leaving a second divergent partial-success branch.
			firstErr = firstNonNil(firstErr, escErr)
		}
		delete(applyFailures, key)
		delete(result.NextObservations, key)
		if !failedKeys[key] {
			result.Failed = append(result.Failed, rec.Ticket)
			failedKeys[key] = true
		}
	}

	for key := range applyFailures {
		if recoveredKeys[key] {
			continue
		}
		if _, deferred := result.NextObservations[key]; deferred {
			// Reconcile deferred a verdict for this key (daemon-unreachable or
			// attempts-unknown) rather than finding it healthy — the grace
			// clock was carried forward, so the apply-retry counter must
			// survive too.
			continue
		}
		delete(applyFailures, key)
	}

	if err := store.Save(ReconcileState{Observations: result.NextObservations, ApplyFailures: applyFailures}); err != nil {
		logf(out, "reconcile: saving state: %v\n", err)
		firstErr = firstNonNil(firstErr, err)
	}
	return result, firstErr
}

// applyRecovery applies one recovery's gh side effects. Report-only kinds
// (orphan-plan) do nothing and report mutated=true (nothing to track). If the
// label swap (EnsureLabels+EditLabels — the state-changing half of the
// recovery) fails, the comment is skipped so a ticket is never annotated
// without the state change that explains it, and mutated is false.
//
// mutated reports whether that state-changing half landed on GitHub,
// independent of whether the trailing comment also succeeded. #265
// (silent-failure-hunter finding #3): a comment-only failure must not be
// treated the same as a failed label mutation — GitHub's label state already
// transitioned, so the caller must not bump the apply-retry counter or
// re-inject the grace observation for it. err is still returned whenever
// either half fails, so a comment-only failure is never silently dropped —
// callers must keep surfacing it (log/firstErr), just without touching the
// apply-retry bookkeeping.
func applyRecovery(mut TicketMutator, rec Recovery, out io.Writer) (mutated bool, err error) {
	if out == nil {
		out = os.Stdout
	}
	if len(rec.AddLabels) == 0 && len(rec.RemoveLabels) == 0 {
		return true, nil
	}
	// Ensure any managed terminal labels among AddLabels exist before adding
	// them (#265 AC #2), so a repo that never had e.g. dispatch-failed
	// pre-created doesn't hard-fail the label edit below.
	if managed := managedLabelsAmong(rec.AddLabels); len(managed) > 0 {
		if err := mut.EnsureLabels(rec.Ticket.Repo, managed); err != nil {
			logf(out, "reconcile: #%d ensure labels: %v\n", rec.Ticket.Number, err)
			return false, err
		}
	}
	if err := mut.EditLabels(rec.Ticket.Repo, rec.Ticket.Number, rec.AddLabels, rec.RemoveLabels); err != nil {
		logf(out, "reconcile: #%d edit labels: %v\n", rec.Ticket.Number, err)
		return false, err
	}
	if rec.Comment != "" {
		if err := mut.Comment(rec.Ticket.Repo, rec.Ticket.Number, rec.Comment); err != nil {
			logf(out, "reconcile: #%d comment: %v\n", rec.Ticket.Number, err)
			return true, err
		}
	}
	return true, nil
}

// formatRecovery renders one recovery as a single log line.
func formatRecovery(rec Recovery) string {
	switch rec.Kind {
	case RecoveryRetry:
		return fmt.Sprintf("#%d retry: %s", rec.Ticket.Number, rec.Detail)
	case RecoveryFailed:
		return fmt.Sprintf("#%d dispatch-failed: %s", rec.Ticket.Number, rec.Detail)
	case RecoveryPlanInvalid:
		return fmt.Sprintf("#%d plan-invalid: %s", rec.Ticket.Number, rec.Detail)
	case RecoveryOrphanPlan:
		return fmt.Sprintf("#%d orphan-plan (report only): %s", rec.Ticket.Number, rec.Detail)
	case RecoveryReconcileStuck:
		return fmt.Sprintf("#%d reconcile-stuck: %s", rec.Ticket.Number, rec.Detail)
	case RecoveryResumeInterrupted:
		return fmt.Sprintf("#%d resume-interrupted: %s", rec.Ticket.Number, rec.Detail)
	default:
		return fmt.Sprintf("#%d %s: %s", rec.Ticket.Number, rec.Kind, rec.Detail)
	}
}
