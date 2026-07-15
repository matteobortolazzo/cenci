package dispatch

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v4/pkg/watch"
)

// Inputs is the full, explicit input to Decide. Now is an injected clock value
// and Snapshot is nil when the daemon is unreachable — both keep Decide pure.
type Inputs struct {
	Tickets  []Ticket
	Plans    []Plan
	Snapshot *watch.StateSnapshot // nil ⇒ daemon unreachable
	Budgets  BudgetProvider
	Now      time.Time // injected clock value
	Prior    int       // dispatches already counted in the current quota window
	Config   Config
}

// Decide is pure: identical Inputs yield an identical ordered []Decision with no
// I/O. Tickets are evaluated in ascending number order; every ticket yields
// exactly one Decision. The first failing gate wins the skip reason.
func Decide(in Inputs) []Decision {
	tickets := make([]Ticket, len(in.Tickets))
	copy(tickets, in.Tickets)
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].Number < tickets[j].Number })

	// Match a plan to a ticket by (repo, ticketId) — issue numbers are only
	// unique within a repo, so a bare ticketId would collide across repos. First
	// wins on duplicates.
	planByTicket := make(map[string]*Plan, len(in.Plans))
	for i := range in.Plans {
		p := &in.Plans[i]
		key := planKey(p.Repo, p.TicketID)
		if _, ok := planByTicket[key]; !ok {
			planByTicket[key] = p
		}
	}

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

func decideTicket(t Ticket, in Inputs, planByTicket map[string]*Plan, dispatchedThisPass int, dispatchedChildByParent map[string]int) Decision {
	skip := func(reason string) Decision {
		return Decision{Ticket: t, Action: ActionSkip, Reason: reason}
	}

	// Pickup rule 1: board state.
	if !hasLabel(t.Labels, "Planned") {
		return skip("not Planned")
	}
	if hasLabel(t.Labels, "Working") {
		return skip("already Working")
	}
	if hasLabel(t.Labels, "Blocked") {
		return skip("blocked")
	}
	if t.HasOpenPR {
		return skip("open PR exists")
	}

	// Pickup rule 2: an approved plan exists.
	plan := planByTicket[planKey(t.Repo, t.Number)]
	if plan == nil {
		return skip("no plan file")
	}
	if plan.Status != "approved" {
		return skip("plan not approved")
	}

	// Pickup rule 3: plan freshness.
	if plan.CommitsBehind > in.Config.PlanStalenessTolerance {
		return skip("plan stale, re-plan")
	}

	// Pickup rule 4: serialize siblings — at most one child per parent active.
	if plan.IsChild {
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

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}
