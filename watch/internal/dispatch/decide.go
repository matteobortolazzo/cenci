package dispatch

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// Stage-gate skip reasons (#732). reasonStageProbeUnknown is deliberately
// distinct from reasonPipelineUnreadable: watch/docs/go-gotchas.md #598
// requires the enum switch's default to be asserted by a test, and #446
// requires content-specific assertions per failure class -- if the default
// reused reasonPipelineUnreadable, that test would pass identically after a
// regression collapsed the default into the StageProbeError branch,
// defeating its purpose.
const (
	reasonPipelineFinalized  = "pipeline finalized (reset to re-dispatch)"
	reasonPipelineUnreadable = "pipeline state unreadable"
	reasonStageProbeUnknown  = "pipeline stage probe unrecognized"
)

// Main-sync gate skip reasons (#822). reasonMainSyncUnknown is deliberately
// distinct from reasonMainSyncFailed for the same reason reasonStageProbeUnknown
// is distinct from reasonPipelineUnreadable above: a regression collapsing the
// default branch into a known case must be caught by a content-specific
// assertion (#446/#598), not silently pass.
const (
	reasonMainDiverged    = "local main diverged"
	reasonMainSyncFailed  = "local main sync failed"
	reasonMainSyncUnknown = "local main sync probe unrecognized"
)

// New gated checkout-state skip reasons (#851): a repo parked off `main`,
// detached, or configured-but-absent from disk is untrustworthy for both
// plan-freshness and pipeline-stage gating, so it gates every pickup in the
// repo exactly like reasonMainDiverged/reasonMainSyncFailed above -- each
// with its own distinct reason, per the plan's Q&A 1 resolution.
const (
	reasonMainNotMain  = "local main not on main"
	reasonMainDetached = "local main detached"
	reasonMainMissing  = "local main missing"
)

// Repo-autonomy gate skip reasons (#851). reasonAutonomyUnknown is
// deliberately distinct from every other reason below for the same reason
// reasonMainSyncUnknown is distinct from reasonMainSyncFailed above: a
// regression collapsing the switch's default branch into a known case must
// be caught by a content-specific assertion (#446/#598). A nil
// Inputs.RepoAutonomy map, a map lookup miss, or an explicit zero-value
// RepoAutonomy("") entry all land here too, since RepoAutonomy deliberately
// has no permissive zero-value constant (types.go).
const (
	reasonAutonomyInteractive = "repo autonomy not lean"
	reasonAutonomyMissing     = "repo config missing"
	reasonAutonomyMalformed   = "repo config malformed"
	reasonAutonomyUnreadable  = "repo config unreadable"
	reasonAutonomyUnknown     = "repo autonomy probe unrecognized"
	// reasonAutonomyFetchUnconfirmed (#877) is content-distinct from every
	// reason above, including reasonAutonomyUnknown: it is not an
	// unrecognized enum value, it is the documented RepoAutonomyFetchUnconfirmed
	// classification for "no remote-confirmed object this pass" (`git fetch
	// origin` failed, or the repo carries no syncs entry at all) -- explicitly
	// retryable, since the very next pass may confirm a fetch.
	reasonAutonomyFetchUnconfirmed = "remote main not fetched this pass (retryable)"
)

// Dependency-gate skip reasons (#825). reasonDependencyStateUnknownFmt is
// deliberately distinct from both reasonDependencyWaitingFmt and
// reasonDependencyUnresolvedFmt for the same reason reasonStageProbeUnknown
// and reasonMainSyncUnknown are each distinct from their sibling reasons
// above: a regression collapsing the gate switch's default branch into a
// known case must be caught by a content-specific assertion (#446/#598), not
// silently pass.
const (
	reasonDependencyWaitingFmt      = "waiting on dependency #%d"
	reasonDependencyUnresolvedFmt   = "dependency #%d unresolved"
	reasonDependencyStateUnknownFmt = "dependency #%d state unrecognized"
	// reasonDependencyMalformedFmt (#852) names the malformed token itself
	// (already truncated to maxDependencyTokenBytes by parseDependsOn) so a
	// syntactically matching "Depends on #N" number that overflows or
	// cannot be represented is reported distinctly from every known
	// DependencyState reason above -- fails closed rather than being
	// silently dropped as "no dependency declared".
	reasonDependencyMalformedFmt = "dependency reference malformed: %q"
)

// Plan-probe gate skip reasons (#852). reasonPlanProbeUnknown is
// deliberately distinct from every other reason here, for the same reason
// reasonStageProbeUnknown/reasonMainSyncUnknown/reasonDependencyStateUnknownFmt
// are each distinct from their siblings above: a regression collapsing the
// gate switch's default branch into a known case must be caught by a
// content-specific assertion (#446/#598), not silently pass.
const (
	reasonPlanProbeUnreadable      = "plan file unreadable"
	reasonPlanProbeMalformed       = "plan file malformed"
	reasonPlanProbeTicketIDInvalid = "plan file ticket id unresolvable"
	reasonPlanProbeStale           = "plan staleness could not be determined"
	reasonPlanProbeUnknown         = "plan probe unrecognized"
	// reasonPlanProbeAmbiguous (#884, AC3/AC4) fires when two or more
	// claims (any mix of healthy/broken) attribute themselves to one plan
	// key -- never resolved first-wins.
	reasonPlanProbeAmbiguous = "plan file ambiguous: multiple claims for this ticket"
	// reasonPlanProbeIdentityMismatch (#884, Q2) fires when a plan file's
	// numeric filename prefix and front-matter ticketId disagree (or the
	// ticketId is missing entirely, Q4) -- both the filename claim and the
	// front-matter claim are held with this same reason.
	reasonPlanProbeIdentityMismatch = "plan file ticket identity mismatch"
	// reasonPlanProbePathAnomaly (#884) fires when a .plans entry
	// attributable to this ticket by filename is not a regular file (a
	// symlink, a directory, or other non-regular entry).
	reasonPlanProbePathAnomaly = "plan file path anomaly"
)

// Plan-inventory gate skip reasons (#884). reasonPlanInventoryUnknown is
// deliberately distinct from reasonPlanInventoryUnreadable/
// reasonPlanInventoryPartial for the same reason reasonPlanProbeUnknown is
// distinct from its siblings above: a regression collapsing the gate
// switch's default branch into a known case must be caught by a
// content-specific assertion (#446/#598).
const (
	reasonPlanInventoryUnreadable = "plan inventory directory unreadable"
	reasonPlanInventoryPartial    = "plan inventory directory partially read"
	reasonPlanInventoryUnknown    = "plan inventory probe unrecognized"
)

// Resume-gate skip reasons (#827). reasonAnswerProbeUnknown is deliberately
// distinct from the enumerated probe-class reasons below, for the same
// reason reasonStageProbeUnknown is distinct from reasonPipelineUnreadable
// above: a regression collapsing the switch's default branch into a known
// case must be caught by a content-specific assertion (#446/#598). A map
// miss in Inputs.Answers (an `Input Needed` ticket resolveAnswerProbes never
// covered this pass) also lands here, since AnswerProbe deliberately has no
// permissive zero-value constant (types.go).
const (
	reasonAnswerWaiting         = "escalation still awaiting a human answer"
	reasonAnswerUnresolved      = "escalation answer probe failed"
	reasonAnswerProbeUnknown    = "escalation answer probe unrecognized"
	reasonDraftNotAwaitingInput = "draft not awaiting input"
)

// Anchor-identity skip reasons (#849): resumeGateSkip's dedicated cases for
// AnswerProbeAnchorUnset/AnswerProbeAnchorMismatch, each content-distinct
// from reasonAnswerProbeUnknown and from each other (#446/#598).
const (
	reasonAnswerAnchorUnset    = "escalation anchor missing or malformed"
	reasonAnswerAnchorMismatch = "escalation anchor comment not found or nonce mismatch"
)

// Open-PR-inventory-completeness gate skip reasons (#881).
// reasonOpenPRProbeUnknown is deliberately distinct from every other reason
// here, for the same reason reasonStageProbeUnknown/reasonMainSyncUnknown/
// reasonPlanProbeUnknown are each distinct from their siblings above: a
// regression collapsing the switch's default branch into a known case must
// be caught by a content-specific assertion (#446/#598).
const (
	reasonOpenPRCapExhausted = "open PR state incomplete: pagination cap exhausted"
	reasonOpenPRTruncated    = "open PR state incomplete: closing-issue references truncated"
	reasonOpenPRMalformed    = "open PR state unreadable: malformed page"
	reasonOpenPRTimeout      = "open PR state unreadable: probe timed out"
	reasonOpenPRUnreadable   = "open PR state unreadable: pagination failed"
	reasonOpenPRProbeUnknown = "open PR probe unrecognized"
)

// Inputs is the full, explicit input to Decide. Now is an injected clock value
// and Snapshot is nil when the daemon is unreachable — both keep Decide pure.
type Inputs struct {
	Tickets     []Ticket
	Plans       []Plan
	Snapshot    *watch.StateSnapshot // nil ⇒ daemon unreachable
	Budgets     BudgetProvider
	Now         time.Time // injected clock value
	Prior       int       // dispatches already counted in the current quota window
	CurrentUser string    // active gh login; tickets must be solely assigned to it
	Config      Config

	// Answers maps planKey(repo, number) -> the resolved AnswerProbe for
	// every ticket that carried `Input Needed` this pass (#827), resolved by
	// resolveAnswerProbes (resume.go) before Decide is ever called -- all
	// I/O for the probe happens in RunOnce, never inside this gate chain
	// (Decide's own purity contract). A ticket not present in this map (any
	// non-`Input Needed` ticket) is simply never a resume candidate.
	Answers map[string]AnswerProbe

	// RepoAutonomy maps repo -> the resolved RepoAutonomy for that repo this
	// pass (#851, #877), resolved by probeRepoAutonomies (autonomy.go) before
	// Decide is ever called -- all I/O for the probe happens in RunOnce,
	// never inside this gate chain (Decide's own purity contract, mirroring
	// Answers above). Consulted only when Config.PlanRefined is true AND the
	// ticket is a fresh Refined planning candidate or an autonomous re-plan
	// candidate -- never for an ordinary already-Planned dispatch, and never
	// at all when PlanRefined is false (dispatch.planRefined remains the
	// fleet kill switch, but repo-lean alone never authorizes planning).
	//
	// #877: every non-fetch-unconfirmed value here is remote-confirmed --
	// probeRepoAutonomies only ever probes at a repo's confirmed AutonomyRef
	// (mainSyncResult.AutonomyRef, always the fully-qualified
	// remoteMainAuthRef when set), never a bare local ref that could be stale
	// or unpushed. A repo whose fetch did not succeed this pass (or that
	// carries no syncs entry at all) resolves RepoAutonomyFetchUnconfirmed --
	// the probe never ran, and the gate denies with its own explicitly
	// retryable reason, distinct from every other denial.
	RepoAutonomy map[string]RepoAutonomy

	// PlanProbes maps planKey(repo, ticketId) -> the resolved PlanProbe for
	// every plan file ReadPlans encountered this pass (#852), resolved
	// entirely inside the collector -- Decide only ever reads this map, it
	// never does I/O itself (Decide's own purity contract, mirroring
	// Answers above). A ticket with no entry here is the true "verified
	// absent" case (PlanProbeAbsent, the zero value).
	PlanProbes map[string]PlanProbe

	// PlanInventories maps repo -> the resolved PlanInventory for that
	// repo's `.plans` directory read this pass (#884), resolved entirely
	// inside the collector (readPlansForRepos) -- Decide only ever reads
	// this map, it never does I/O itself (Decide's own purity contract,
	// mirroring PlanProbes above). A repo with no entry here (a nil map, or
	// a map lookup miss) is the permissive zero value (PlanInventoryVerified),
	// mirroring RepoAutonomy's own map-miss convention documented at
	// types.go.
	PlanInventories map[string]PlanInventory
}

// Decide is pure: identical Inputs yield an identical ordered []Decision with no
// I/O. Tickets are evaluated in ascending number order; every ticket yields
// exactly one Decision. The first failing gate wins the skip reason.
func Decide(in Inputs) []Decision {
	tickets := make([]Ticket, len(in.Tickets))
	copy(tickets, in.Tickets)
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].Number < tickets[j].Number })

	// Match a plan to a ticket by (repo, ticketId) — issue numbers are only
	// unique within a repo, so a bare ticketId would collide across repos.
	// First wins on duplicates. indexPlans is shared with RunOnce/
	// resolveAnswerProbes (#849) so both consumers agree on exactly the same
	// "which plan belongs to this ticket" rule.
	planByTicket := indexPlans(in.Plans)

	// Running tallies so a single pass never over-commits beyond caps, plus the
	// per-parent record of children dispatched this pass (sibling serialization).
	// The parent key is (repo, parentId) so parents don't collide across repos.
	dispatchedThisPass := 0
	dispatchedChildByParent := map[string]int{}

	decisions := make([]Decision, 0, len(tickets))
	for _, t := range tickets {
		d := decideTicket(t, in, planByTicket, dispatchedThisPass, dispatchedChildByParent)
		if d.Action == ActionDispatch {
			dispatchedThisPass++
			if d.Plan != nil && d.Plan.IsChild {
				dispatchedChildByParent[planKey(t.Repo, d.Plan.ParentID)] = t.Number
			}
		}
		decisions = append(decisions, d)
	}
	return decisions
}

// planKey namespaces a ticket/parent id by its repo so ids from different repos
// never collide.
func planKey(repo string, id int) string {
	return repo + "#" + strconv.Itoa(id)
}

// indexPlans builds a plan lookup keyed by planKey(repo, ticketId), first
// wins on duplicates (#849): shared by Decide (matching a plan to each
// ticket in the pure gate chain) and RunOnce/resolveAnswerProbes (matching a
// plan to look up its escalation anchor fields for the REST probe), so both
// consumers agree on exactly the same "which plan belongs to this ticket"
// rule.
func indexPlans(plans []Plan) map[string]*Plan {
	m := make(map[string]*Plan, len(plans))
	for i := range plans {
		p := &plans[i]
		key := planKey(p.Repo, p.TicketID)
		if _, ok := m[key]; !ok {
			m[key] = p
		}
	}
	return m
}

func decideTicket(t Ticket, in Inputs, planByTicket map[string]*Plan, dispatchedThisPass int, dispatchedChildByParent map[string]int) Decision {
	skip := func(reason string) Decision {
		return Decision{Ticket: t, Action: ActionSkip, Reason: reason}
	}

	// Pickup rule 0: local main sync (#822). Evaluated first, before every
	// other gate -- plan freshness (CommitsBehind) and the pipeline stage
	// gate are both computed against the local tree, so nothing downstream
	// in this chain is trustworthy when main failed to sync. A repo whose
	// sync was never attempted or itself skipped (MainSyncSkipped, the zero
	// value) or whose fetch merely failed transiently (MainSyncFetchFailed,
	// self-heals next pass) is deliberately left ungated.
	if reason, gated := mainSyncSkip(t); gated {
		return skip(reason)
	}

	// Pickup rule 0.5: plan inventory (#884, Q5). Evaluated immediately
	// after the local-main-sync gate and before the plan lookup: an
	// unreadable or mid-enumeration-partial `.plans` directory can never
	// prove absence for ANY ticket in the repo, so it holds every ticket in
	// that repo -- ordinary Planned dispatch, resume, and planning pickup
	// alike, not merely the ticket whose own plan file happened to be
	// unreadable.
	if reason, gated := planInventorySkip(in.PlanInventories[t.Repo]); gated {
		return skip(reason)
	}

	// resuming (#827) is true when this ticket carries `Input Needed`: the
	// unattended planner escalated it and stopped cleanly, and it is a
	// dispatch candidate again once a human answers. Swaps only rule 1's
	// board-state check and rule 3's plan-status check below (an escalated
	// ticket never carries `Planned`, and its plan is `awaiting-input`, not
	// `planned`); skips rule 4 freshness entirely; every other gate
	// (assignee, dependency, sibling, capacity/budget) is shared verbatim
	// with the ordinary path, because it is literally the same code.
	resuming := hasLabel(t.Labels, labelInputNeeded)

	// Plan lookup is hoisted above rule 1 (#828, pure map read, no I/O and no
	// behavior change) so `planning` can be computed before the board-state
	// gate: a Refined ticket with no plan must be admitted past rule 1
	// instead of being turned away as "not Planned".
	plan := planByTicket[planKey(t.Repo, t.Number)]

	// Plan-probe gate (#852), evaluated immediately after the plan lookup
	// and BEFORE planning (below) is computed: a plan file that exists but
	// is broken in some way (unreadable, malformed front matter, an
	// unresolvable ticket id) must never be treated as "no plan file" and
	// fall through into the stage-aware planning-pickup gate as a fresh
	// planning candidate (AC4, the ticket's headline regression) -- that
	// would launch a spurious/duplicate planning session on top of a plan
	// file that is simply mid-write or otherwise broken. A verified-absent
	// plan (PlanProbeAbsent, the zero value -- no entry in PlanProbes at
	// all) and a healthy plan (PlanProbeOk) both pass through unaffected,
	// as does a staleness-calculation error (PlanProbeStalenessError,
	// handled separately at rule 4 below, since content-trust and
	// staleness are distinct failure classes).
	if reason, gated := planProbeSkip(in.PlanProbes[planKey(t.Repo, t.Number)]); gated {
		return skip(reason)
	}

	// planning (#828) is true when this ticket is a fresh stage-aware
	// planning candidate: dispatch.planRefined is on, it carries Refined but
	// not yet Planned, and no plan file has been matched to it yet.
	// Deliberately implies plan == nil (mirrors resuming's shape above) --
	// every gate below that dereferences plan guards on !planning (or
	// plan != nil) accordingly. The sibling case -- an existing but stale
	// plan re-planned autonomously -- is the separate replanning flag
	// computed at rule 4 below; Decision.Planning is set on both paths, but
	// only replanning also sets Decision.Replan.
	planning := !resuming && in.Config.PlanRefined && plan == nil &&
		hasLabel(t.Labels, labelRefined) && !hasLabel(t.Labels, labelPlanned)

	// Per-repository lean-authorization gate (#851): a fresh Refined
	// planning candidate additionally requires the repo's own committed
	// `planning.autonomy` to resolve lean -- dispatch.planRefined (the fleet
	// kill switch) is necessary but no longer sufficient. Evaluated here,
	// immediately after planning is determined and before every board-state
	// gate below, so a denial reports its own specific reason rather than
	// falling through to "not Planned". Never consulted for an ordinary
	// already-Planned ticket (planning is false there).
	if planning {
		if reason, gated := autonomyGateSkip(in.RepoAutonomy[t.Repo]); gated {
			return skip(reason)
		}
	}

	// Pickup rule 1: board state.
	if !resuming && !planning && !hasLabel(t.Labels, "Planned") {
		return skip("not Planned")
	}
	if hasLabel(t.Labels, "Working") {
		return skip("already Working")
	}
	if hasLabel(t.Labels, "Blocked") {
		return skip("blocked")
	}
	// Open-PR-inventory-completeness gate (#881), evaluated immediately
	// before the ordinary HasOpenPR check below: a non-complete probe means
	// t.HasOpenPR could not actually be proven false, so it must never fall
	// through to that check on unverified input -- an affected repo reports
	// this one uniform reason across all its tickets.
	if reason, gated := openPRGateSkip(t); gated {
		return skip(reason)
	}
	if t.HasOpenPR {
		return skip("open PR exists")
	}
	if in.Config.PipelineStageGate {
		if reason, gated := stageGateSkip(t); gated {
			return skip(reason)
		}
	}

	// Pickup rule 2: exclusive human ownership. Multiple assignees are
	// intentionally ambiguous even when CurrentUser is among them.
	switch len(t.Assignees) {
	case 0:
		return skip("unassigned")
	case 1:
		if !strings.EqualFold(t.Assignees[0], in.CurrentUser) {
			return skip("assigned to @" + t.Assignees[0])
		}
	default:
		return skip("multiple assignees")
	}

	// Pickup rule 3: a matching plan exists, in the status this path expects
	// -- except for a planning candidate, which by definition (above) has no
	// matched plan yet and skips this check entirely rather than terminally
	// "no plan file".
	if plan == nil {
		if !planning {
			return skip("no plan file")
		}
	} else if resuming {
		if plan.Status != "awaiting-input" {
			return skip(reasonDraftNotAwaitingInput)
		}
	} else if plan.Status != "planned" {
		return skip("plan not ready")
	}

	// Resume-only gate (#827): does the ticket's comment thread hold a human
	// answer to its escalation? Zero I/O here -- the probe was already
	// resolved by resolveAnswerProbes/RunOnce before Decide was ever called
	// (Decide's own purity contract, mirroring dependencyGateSkip's doc
	// comment above).
	if resuming {
		if reason, gated := resumeGateSkip(t, in.Answers[planKey(t.Repo, t.Number)]); gated {
			return skip(reason)
		}
	}

	// Pickup rule 4: plan freshness. Deliberately skipped when resuming,
	// mirroring CheckPlan's own awaiting-input-before-planIsStale
	// short-circuit (watch/internal/pipeline/planfile.go:165-173): a draft
	// that waited days on a human is almost always commits-behind, and
	// applying this gate here would mean it could never re-resume. Also
	// skipped when planning: a planning candidate has no matched plan (nil),
	// so there is nothing to measure staleness against. When the plan IS
	// stale, dispatch.planRefined off keeps today's terminal skip byte-
	// identical ("plan stale, re-plan"); on, it turns into an actionable
	// autonomous re-plan (#828) -- replanning falls through the remaining
	// gates instead of terminating here.
	replanning := false
	if !resuming && !planning {
		// A staleness-calculation error (#852 AC5) must never resolve to
		// unknown-fresh: it is checked alongside the CommitsBehind
		// comparison, inside this same !resuming && !planning guard, so it
		// never gates a resuming ticket (mirroring CheckPlan's own
		// awaiting-input-before-planIsStale short-circuit) and never
		// applies to a planning candidate (which has no matched plan to
		// measure staleness against in the first place).
		if reason, gated := planStalenessSkip(in.PlanProbes[planKey(t.Repo, t.Number)]); gated {
			return skip(reason)
		}
		if plan.CommitsBehind > in.Config.PlanStalenessTolerance {
			if !in.Config.PlanRefined {
				return skip("plan stale, re-plan")
			}
			// Per-repository lean-authorization gate (#851): an autonomous
			// re-plan is subject to the same repo-lean requirement as a fresh
			// planning pickup above. A denial composes the staleness fact with
			// the autonomy reason, rather than either the flag-off literal
			// "plan stale, re-plan" (which would misleadingly imply the flag
			// itself is off) or the bare autonomy reason alone (which would lose
			// the staleness context).
			if reason, gated := autonomyGateSkip(in.RepoAutonomy[t.Repo]); gated {
				return skip(fmt.Sprintf("plan stale, re-plan blocked: %s", reason))
			}
			replanning = true
		}
	}

	// Pickup rule 5: Depends on #N dependency gate (#825). A plan written
	// before its dependency merges is stale on arrival, so this sits right
	// after plan freshness -- see dependencyGateSkip's doc comment.
	if reason, gated := dependencyGateSkip(t); gated {
		return skip(reason)
	}

	// Pickup rule 6: serialize siblings — at most one child per parent active.
	// No-ops for a planning candidate (plan == nil, #828 Q2/Risks: sibling
	// serialization is derived from the plan file, which doesn't exist yet
	// for a Refined-with-no-plan ticket -- documented limitation, not a bug).
	if plan != nil && plan.IsChild {
		if m, blocked := blockingSibling(t, plan, in, dispatchedChildByParent); blocked {
			return skip(fmt.Sprintf("waiting on sibling #%d", m))
		}
	}

	// Capacity/budget gates, in order.
	if in.Snapshot == nil {
		return skip("daemon unreachable")
	}
	if in.Snapshot.Summary.NeedInput >= in.Config.NeedInputThreshold {
		return skip("need-input pause")
	}
	if in.Snapshot.Summary.Running+dispatchedThisPass >= in.Config.ConcurrencyCap {
		return skip("concurrency cap reached")
	}
	if in.Prior+dispatchedThisPass >= in.Config.DailyQuota {
		return skip("daily quota reached")
	}
	if in.Config.QuietHours != nil && in.Config.QuietHours.Contains(in.Now) {
		return skip("quiet hours")
	}

	agent := resolveAgent(t, in)
	if agent == "" {
		return skip("budget exhausted")
	}

	if resuming {
		return Decision{Ticket: t, Plan: plan, Action: ActionDispatch, Resume: true, Reason: "resume — human answered", Agent: agent}
	}
	if planning || replanning {
		if replanning {
			return Decision{Ticket: t, Plan: plan, Action: ActionDispatch, Planning: true, Replan: true, Reason: "re-plan — plan stale", Agent: agent}
		}
		return Decision{Ticket: t, Plan: nil, Action: ActionDispatch, Planning: true, Reason: "plan — Refined, no plan file", Agent: agent}
	}
	return Decision{Ticket: t, Plan: plan, Action: ActionDispatch, Reason: "dispatch", Agent: agent}
}

// blockingSibling returns the number of the sibling that blocks dispatch of
// child t, if any. A sibling shares the same ParentID; it blocks when its ticket
// is currently active (Working, HasOpenPR, or a running window in the snapshot)
// or was already dispatched earlier this pass. The lowest such number is
// reported for determinism.
func blockingSibling(t Ticket, plan *Plan, in Inputs, dispatchedChildByParent map[string]int) (int, bool) {
	parent := plan.ParentID
	blocking := -1
	consider := func(n int) {
		if n > 0 && (blocking < 0 || n < blocking) {
			blocking = n
		}
	}

	if n, ok := dispatchedChildByParent[planKey(t.Repo, parent)]; ok {
		consider(n)
	}
	for i := range in.Plans {
		sp := &in.Plans[i]
		// Siblings share the repo and parent, are themselves children, and are
		// not this ticket.
		if sp.Repo != t.Repo || sp.TicketID == t.Number || sp.ParentID != parent || !sp.IsChild {
			continue
		}
		if st := ticketByNumber(in.Tickets, sp.Repo, sp.TicketID); st != nil && ticketActive(*st, in.Snapshot) {
			consider(sp.TicketID)
		}
	}

	if blocking >= 0 {
		return blocking, true
	}
	return 0, false
}

// ticketActive reports whether a ticket is currently in flight: it carries the
// Working label, has an open PR, or owns a running window in the snapshot (a
// window named "<number>" or "<number>-...").
func ticketActive(t Ticket, snap *watch.StateSnapshot) bool {
	if hasLabel(t.Labels, "Working") || t.HasOpenPR {
		return true
	}
	if snap == nil {
		return false
	}
	prefix := strconv.Itoa(t.Number)
	for _, w := range snap.Windows {
		if w.Status != "running" {
			continue
		}
		if w.WindowName == prefix || strings.HasPrefix(w.WindowName, prefix+"-") {
			return true
		}
	}
	return false
}

func ticketByNumber(tickets []Ticket, repo string, n int) *Ticket {
	for i := range tickets {
		if tickets[i].Repo == repo && tickets[i].Number == n {
			return &tickets[i]
		}
	}
	return nil
}

// resolveAgent walks the agent preference list — ticket label, then config
// preferences — and returns the first agent with budget. An empty return means
// all agents are exhausted.
func resolveAgent(t Ticket, in Inputs) string {
	for _, agent := range agentPreference(t, in.Config) {
		b := in.Budgets.Budget(agent)
		if b.Unlimited || b.Remaining > 0 {
			return agent
		}
	}
	return ""
}

// agentPreference builds the ordered list of agents to try for a ticket.
// The ticket's agent: label (or config default) comes first, then the config
// AgentPreference list with duplicates removed.
func agentPreference(t Ticket, cfg Config) []string {
	var prefs []string
	primary := t.Agent
	if primary == "" {
		primary = cfg.DefaultAgent
	}
	if primary != "" {
		prefs = append(prefs, primary)
	}
	for _, a := range cfg.AgentPreference {
		if !containsStr(prefs, a) {
			prefs = append(prefs, a)
		}
	}
	if len(prefs) == 0 && cfg.DefaultAgent != "" {
		prefs = append(prefs, cfg.DefaultAgent)
	}
	return prefs
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// stageGateSkip evaluates the persisted-pipeline-stage gate (#732) for t.
// It returns (reason, true) when the ticket should be skipped on stage
// grounds, and ("", false) when the gate passes.
func stageGateSkip(t Ticket) (string, bool) {
	switch t.StageProbe {
	case StageProbeAbsent:
		return "", false // no pipeline run here: the documented permissive exception
	case StageProbeError:
		return reasonPipelineUnreadable, true // broken input, not absent input
	case StageProbePresent:
		switch {
		case pipeline.Stage(t.Stage) == pipeline.StageFinalized:
			return reasonPipelineFinalized, true
		case !pipeline.IsKnownStage(pipeline.Stage(t.Stage)):
			// Defensive: today's collector classifies an unrecognized stage
			// as StageProbeError, so this pair is unreachable via
			// CollectTickets. Checked anyway (default-deny per
			// watch/docs/error-handling.md #636) so a future collector
			// change can never make an unknown stage dispatch silently.
			return reasonPipelineUnreadable, true
		default:
			return "", false // prepared/waiting/plan_approved/executed/reviewed: NOT gated
		}
	default:
		// Unrecognized StageProbe value: default-deny with its own distinct
		// reason (not reasonPipelineUnreadable) so a regression collapsing
		// this branch into StageProbeError is caught by assertion, per
		// #446/#598.
		return reasonStageProbeUnknown, true
	}
}

// mainSyncSkip evaluates the local-main-sync gate (#822) for t. It returns
// (reason, true) when t's repo failed to sync main and every ticket in that
// repo must be skipped, and ("", false) when the gate passes -- including
// MainSyncFetchFailed, which is deliberately left ungated since a transient
// fetch failure self-heals next pass.
func mainSyncSkip(t Ticket) (string, bool) {
	switch t.MainSync {
	case MainSyncSkipped, MainSyncSynced, MainSyncFetchFailed:
		return "", false
	case MainSyncDiverged:
		return reasonMainDiverged, true
	case MainSyncFailed:
		return reasonMainSyncFailed, true
	case MainSyncNotMain:
		return reasonMainNotMain, true
	case MainSyncDetached:
		return reasonMainDetached, true
	case MainSyncMissing:
		return reasonMainMissing, true
	default:
		// Unrecognized MainSync value: default-deny with its own distinct
		// reason (not reasonMainSyncFailed) so a regression collapsing this
		// branch is caught by assertion, per #446/#598.
		return reasonMainSyncUnknown, true
	}
}

// autonomyGateSkip evaluates the per-repository lean-authorization gate
// (#851) for a: it returns (reason, true) when a denies an unattended
// planning/re-plan dispatch, and ("", false) only for RepoAutonomyLean.
// Called only when Config.PlanRefined is true AND the ticket is a fresh
// Refined planning candidate or an autonomous re-plan candidate (decideTicket
// below) -- dispatch.planRefined remains the fleet kill switch, but is
// insufficient alone to authorize unattended planning.
func autonomyGateSkip(a RepoAutonomy) (string, bool) {
	switch a {
	case RepoAutonomyLean:
		return "", false
	case RepoAutonomyInteractive:
		return reasonAutonomyInteractive, true
	case RepoAutonomyMissing:
		return reasonAutonomyMissing, true
	case RepoAutonomyMalformed:
		return reasonAutonomyMalformed, true
	case RepoAutonomyUnreadable:
		return reasonAutonomyUnreadable, true
	case RepoAutonomyFetchUnconfirmed:
		return reasonAutonomyFetchUnconfirmed, true
	default:
		// Unrecognized/nil-map-miss/zero-value RepoAutonomy: default-deny
		// with its own distinct reason so a regression collapsing this
		// branch is caught by assertion, per #446/#598.
		return reasonAutonomyUnknown, true
	}
}

// planProbeSkip evaluates the content-trust half of the plan-probe gate
// (#852) for probe -- the PlanProbe classification of the plan file matched
// (or not) to one ticket. It returns (reason, true) when the plan file is
// broken input that must default-deny, and ("", false) for the three
// classes that pass: PlanProbeAbsent (verified absence -- no plan file
// matched this ticket at all, the zero value), PlanProbeOk (a healthy,
// cleanly parsed plan), and PlanProbeStalenessError (a distinct failure
// class handled separately by planStalenessSkip at rule 4, since a
// staleness-calculation error does not impugn the plan's own content).
func planProbeSkip(probe PlanProbe) (string, bool) {
	switch probe {
	case PlanProbeAbsent, PlanProbeOk, PlanProbeStalenessError:
		return "", false
	case PlanProbeReadError:
		return reasonPlanProbeUnreadable, true
	case PlanProbeParseError:
		return reasonPlanProbeMalformed, true
	case PlanProbeTicketIDError:
		return reasonPlanProbeTicketIDInvalid, true
	case PlanProbeAmbiguous:
		return reasonPlanProbeAmbiguous, true
	case PlanProbeIDMismatch:
		return reasonPlanProbeIdentityMismatch, true
	case PlanProbePathAnomaly:
		return reasonPlanProbePathAnomaly, true
	default:
		// Unrecognized PlanProbe value: default-deny with its own distinct
		// reason (not any of the known reasons above) so a regression
		// collapsing this branch is caught by assertion, per #446/#598.
		return reasonPlanProbeUnknown, true
	}
}

// planInventorySkip evaluates the plan-inventory gate (#884) for inv -- the
// PlanInventory classification of the repo's whole `.plans` directory read
// this pass. It returns (reason, true) when the directory could not be
// fully enumerated (unreadable or a mid-enumeration partial read), and
// ("", false) only for PlanInventoryVerified (the permissive zero value,
// including a nil-map/map-miss repo).
func planInventorySkip(inv PlanInventory) (string, bool) {
	switch inv {
	case PlanInventoryVerified:
		return "", false
	case PlanInventoryUnreadable:
		return reasonPlanInventoryUnreadable, true
	case PlanInventoryPartial:
		return reasonPlanInventoryPartial, true
	default:
		// Unrecognized PlanInventory value: default-deny with its own
		// distinct reason so a regression collapsing this branch is caught
		// by assertion, per #446/#598.
		return reasonPlanInventoryUnknown, true
	}
}

// planStalenessSkip evaluates the staleness-calculation half of the
// plan-probe gate (#852) for probe. Only PlanProbeStalenessError gates here
// -- every other PlanProbe value (including an unrecognized one, already
// caught by planProbeSkip above) is content-trust territory, not staleness.
// A staleness-calculation error must never resolve to unknown-fresh (AC5):
// treating it as "0 commits behind" would silently readmit a ticket whose
// freshness could not actually be verified.
func planStalenessSkip(probe PlanProbe) (string, bool) {
	if probe == PlanProbeStalenessError {
		return reasonPlanProbeStale, true
	}
	return "", false
}

// dependencyGateSkip evaluates the Depends-on-#N dependency gate (#825) for
// t, with zero I/O -- every DependencyState was already resolved by the
// collector (dependency.go/collect.go), never here (Decide's own purity
// contract). It returns (reason, true) when the lowest-numbered blocking
// dependency in t.DependsOn should skip dispatch, and ("", false) when every
// dependency resolves DependencyStateClosed (including the empty/nil
// DependsOn zero value, the true "ungated" case). Mirrors blockingSibling's
// lowest-number-wins determinism rather than reporting body-parse order: the
// lowest blocking number is reported when multiple dependencies block.
//
// t.DependencyAnomalies (#852) is checked first, before the numeric loop:
// a ticket whose body declares a syntactically matching "Depends on #N"
// reference that could not be classified as a valid dependency at all (an
// overflowing/out-of-range number) must hold rather than silently dispatch
// as if the reference never existed. The first anomaly in body order is
// reported (AC2), naming the malformed token via reasonDependencyMalformedFmt.
func dependencyGateSkip(t Ticket) (string, bool) {
	if len(t.DependencyAnomalies) > 0 {
		return fmt.Sprintf(reasonDependencyMalformedFmt, t.DependencyAnomalies[0]), true
	}

	blocking := -1
	var reason string
	consider := func(n int, r string) {
		if blocking < 0 || n < blocking {
			blocking = n
			reason = r
		}
	}

	for _, n := range t.DependsOn {
		switch t.DependencyStates[n] {
		case DependencyStateClosed:
			// not blocking
		case DependencyStateOpen:
			consider(n, fmt.Sprintf(reasonDependencyWaitingFmt, n))
		case DependencyStateUnresolved:
			consider(n, fmt.Sprintf(reasonDependencyUnresolvedFmt, n))
		default:
			// Unrecognized/missing DependencyState (a DependencyStates map
			// lookup miss for a number the ticket DOES declare a dependency
			// on): default-deny with its own distinct reason (not either
			// known reason above) so a regression collapsing this branch is
			// caught by assertion, per #446/#598.
			consider(n, fmt.Sprintf(reasonDependencyStateUnknownFmt, n))
		}
	}

	if blocking >= 0 {
		return reason, true
	}
	return "", false
}

// resumeGateSkip evaluates the escalation-answer gate (#827) for a resuming
// ticket t, with zero I/O -- probe was already resolved by
// resolveAnswerProbes/RunOnce, never here (Decide's own purity contract,
// mirroring dependencyGateSkip's doc comment above). It returns (reason,
// true) when probe should skip dispatch, and ("", false) only for
// AnswerProbeAnswered. A map miss (the zero value "" -- Inputs.Answers never
// covered this ticket) lands in the default branch alongside any other
// unrecognized value, since AnswerProbe deliberately has no permissive
// zero-value constant (types.go). t is accepted (unused today) to keep this
// gate's signature symmetric with the ticket-scoped gates above and leave
// room for a future ticket-scoped reason without an incompatible signature
// change.
func resumeGateSkip(t Ticket, probe AnswerProbe) (string, bool) {
	switch probe {
	case AnswerProbeAnswered:
		return "", false
	case AnswerProbeWaiting:
		return reasonAnswerWaiting, true
	case AnswerProbeUnresolved:
		return reasonAnswerUnresolved, true
	case AnswerProbeAnchorUnset:
		return reasonAnswerAnchorUnset, true
	case AnswerProbeAnchorMismatch:
		return reasonAnswerAnchorMismatch, true
	default:
		// Unrecognized/missing AnswerProbe value: default-deny with its own
		// distinct reason (not any of the four known reasons above) so a
		// regression collapsing this branch is caught by assertion, per
		// #446/#598.
		return reasonAnswerProbeUnknown, true
	}
}

// openPRGateSkip evaluates the open-PR-inventory-completeness gate (#881)
// for t, with zero I/O -- t.OpenPRProbe was already resolved by the
// collector's openPRInventory call (collect.go), never here (Decide's own
// purity contract, mirroring dependencyGateSkip's doc comment above). It
// returns (reason, true) when the probe could not prove t.HasOpenPR false,
// and ("", false) only for OpenPRProbeComplete (including the zero value --
// every pre-#881 Ticket construction site keeps today's ungated behavior).
func openPRGateSkip(t Ticket) (string, bool) {
	switch t.OpenPRProbe {
	case OpenPRProbeComplete:
		return "", false
	case OpenPRProbeCapExhausted:
		return reasonOpenPRCapExhausted, true
	case OpenPRProbeTruncated:
		return reasonOpenPRTruncated, true
	case OpenPRProbeMalformed:
		return reasonOpenPRMalformed, true
	case OpenPRProbeTimeout:
		return reasonOpenPRTimeout, true
	case OpenPRProbeUnreadable:
		return reasonOpenPRUnreadable, true
	default:
		// Unrecognized OpenPRProbe value: default-deny with its own distinct
		// reason (not any of the five known reasons above) so a regression
		// collapsing this branch is caught by assertion, per #446/#598.
		return reasonOpenPRProbeUnknown, true
	}
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}
