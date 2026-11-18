package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// maxPlanningTrustProbes caps how many comment-thread reads one RunOnce pass
// makes for the planning trust gate (#847), mirroring maxAnswerProbes
// (resume.go). Past the cap a candidate resolves straight to
// PlanningTrustUnresolved without a gh call -- it is held, never trusted.
const maxPlanningTrustProbes = 50

// planningTrustProbeTimeout bounds each comment-thread read (#847), mirroring
// permissionProbeTimeout (permission.go). A var, not a const, so a test can
// shorten it instead of waiting out the real bound.
var planningTrustProbeTimeout = 60 * time.Second

// planningTrustBudget tracks one pass's comment-thread reads. logged records
// that the cap-hit line was already written, so it is logged once per pass.
type planningTrustBudget struct {
	calls  int
	logged bool
}

// planningTrustCandidate reports whether t is an unattended planning pickup or
// autonomous re-plan that the trust gate must check (#847). It is pure and is
// shared by the probe (planningTrustCandidates) and Decide's fresh-pickup
// site, so the two can only disagree if this function changes for one and not
// the other; Decide's dispatch backstop and the parity test cover that drift.
//
// The conditions mirror decideTicket's gates that run before the trust check
// or that decide whether a pickup can launch at all: not `Input Needed`,
// planRefined on, the repo resolved RepoAutonomyLean (after the attended
// narrowing), the only assignee is CurrentUser (case-insensitive), not
// Working/Blocked, and not already held by the main-sync, plan-inventory,
// machine-local filter or plan-probe gates. Then either a fresh pickup (no
// matched plan, Refined, not Planned) or a stale re-plan (Planned, plan status
// `planned`, CommitsBehind over the tolerance, staleness known).
func planningTrustCandidate(t Ticket, in Inputs, plan *Plan, filter RepoFilter) bool {
	if hasLabel(t.Labels, labelInputNeeded) || !in.Config.PlanRefined {
		return false
	}
	if in.RepoAutonomy[t.Repo] != RepoAutonomyLean {
		return false
	}
	if len(t.Assignees) != 1 || !strings.EqualFold(t.Assignees[0], in.CurrentUser) {
		return false
	}
	if hasLabel(t.Labels, "Working") || hasLabel(t.Labels, "Blocked") {
		return false
	}
	if _, gated := mainSyncSkip(t); gated {
		return false
	}
	if _, gated := planInventorySkip(in.PlanInventories[t.Repo]); gated {
		return false
	}
	if _, gated := filterSkip(filter, t); gated {
		return false
	}
	key := planKey(t.Repo, t.Number)
	if _, gated := planProbeSkip(in.PlanProbes[key]); gated {
		return false
	}
	if plan == nil {
		return hasLabel(t.Labels, labelRefined) && !hasLabel(t.Labels, labelPlanned)
	}
	if !hasLabel(t.Labels, labelPlanned) || plan.Status != "planned" {
		return false
	}
	if _, gated := planStalenessSkip(in.PlanProbes[key]); gated {
		return false
	}
	return plan.CommitsBehind > in.Config.PlanStalenessTolerance
}

// planningTrustCandidates returns every ticket in in.Tickets that
// planningTrustCandidate accepts, ordered by ticket number ascending (the key
// Decide sorts by) and then by repo ascending, a tie-break that belongs to the
// probe alone. Decide sorts by number only and leaves equal numbers in no
// defined order, so when one number appears in several repos at the budget
// boundary, the candidate held here may not be the one Decide reaches first.
// That is fail-safe: the held candidate reports `planning held: ticket trust
// unresolved` and is never wrongly trusted. It returns nil when planRefined is
// off.
func planningTrustCandidates(in Inputs) []Ticket {
	if !in.Config.PlanRefined {
		return nil
	}
	planByTicket := indexPlans(in.Plans)
	filterByRepo := buildFilterByRepo(in.Config.Repos)
	var candidates []Ticket
	for _, t := range in.Tickets {
		if planningTrustCandidate(t, in, planByTicket[planKey(t.Repo, t.Number)], filterByRepo[t.Repo]) {
			candidates = append(candidates, t)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Number != candidates[j].Number {
			return candidates[i].Number < candidates[j].Number
		}
		return candidates[i].Repo < candidates[j].Repo
	})
	return candidates
}

// resolvePlanningTrust probes every planning trust candidate once per pass
// (#847) and returns the verdicts keyed by planKey, for Inputs.PlanningTrust.
// It runs in RunOnce after resolveAnswerProbes, dry runs included (it is
// read-only). With planRefined off, or no candidate, it makes zero gh calls
// and returns nil.
//
// It owns its own permissionCache, separate from resolveAnswerProbes', so
// planning lookups never spend the resume path's maxPermissionLookupsPerRepo
// budget. The cache dies with the pass: nothing is carried into the next one,
// so a deleted comment or a revoked collaborator takes effect on the next pass.
// Probe failures are only logged and never reach collectErr.
func resolvePlanningTrust(in Inputs, out io.Writer) map[string]PlanningTrustProbe {
	candidates := planningTrustCandidates(in)
	if len(candidates) == 0 {
		return nil
	}
	verdicts := make(map[string]PlanningTrustProbe, len(candidates))
	budget := &planningTrustBudget{}
	permCache := newPermissionCache()
	for _, t := range candidates {
		repo := t.Repo
		permOf := func(login string) WritePermission {
			return permCache.resolveWritePermission(repo, login, out)
		}
		verdicts[planKey(t.Repo, t.Number)] = probePlanningTrust(t, budget, permOf, out)
	}
	return verdicts
}

// probePlanningTrust reads t's comment thread through the REST API and
// classifies it with classifyPlanningTrust (#847). The call is `gh api
// repos/<o>/<r>/issues/<n>/comments?per_page=100 --paginate` through
// execGhBoundedCtx, bounded by planningTrustProbeTimeout and capped at
// maxProbeStdoutBytes (watch/AGENTS.md #825/#852/#854). Every read failure --
// API error, timeout, truncation, malformed JSON or a JSON null, a comment
// with a missing or non-positive ID, and a spent per-pass budget -- is
// PlanningTrustUnresolved, and no permission lookup is made after one.
//
// Log lines name only repo#n, the failure class and the collapsed, truncated
// stderr of a failed call. The call's stdout is never logged: it carries
// comment bodies, which are attacker-controlled on a public repo. Lines start
// with "dispatch: " and avoid the " skip:"/" dispatch " substrings decision
// lines are classified by.
func probePlanningTrust(t Ticket, budget *planningTrustBudget, permOf func(string) WritePermission, out io.Writer) PlanningTrustProbe {
	unresolved := PlanningTrustProbe{Class: PlanningTrustUnresolved}
	if budget.calls >= maxPlanningTrustProbes {
		if !budget.logged {
			logf(out, "dispatch: planning trust probe cap (%d) reached, remaining candidates held\n", maxPlanningTrustProbes)
			budget.logged = true
		}
		return unresolved
	}
	budget.calls++

	ctx, cancel := context.WithTimeout(context.Background(), planningTrustProbeTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", t.Repo, t.Number)
	stdout, stderr, err := execGhBoundedCtx(ctx, maxProbeStdoutBytes, "api", endpoint, "--paginate")
	if err != nil {
		// Truncation is checked first so it wins over a timeout that also
		// fired mid-stream; the joined sentinel errors are never printed,
		// because errors.Join's text spans several lines.
		detail := truncateDetail(collapseLines(stderr), maxProbeLogDetailBytes)
		switch {
		case errors.Is(err, errGhOutputTruncated):
			logf(out, "dispatch: planning trust probe for %s#%d exceeded the %d-byte stdout cap\n", t.Repo, t.Number, maxProbeStdoutBytes)
		case errors.Is(err, errGhTimeout):
			logf(out, "dispatch: planning trust probe for %s#%d timed out: %s\n", t.Repo, t.Number, detail)
		default:
			logf(out, "dispatch: planning trust probe for %s#%d failed: %v (%s)\n", t.Repo, t.Number, err, detail)
		}
		return unresolved
	}

	// A JSON null decodes without error into a nil slice, so nil after a
	// successful decode is treated as malformed rather than an empty thread.
	var comments []restIssueComment
	if uerr := json.Unmarshal([]byte(stdout), &comments); uerr != nil || comments == nil {
		logf(out, "dispatch: planning trust probe for %s#%d returned malformed JSON\n", t.Repo, t.Number)
		return unresolved
	}
	// A missing or non-positive comment ID is malformed data: logged here so
	// the hold is visible, and held before any permission lookup.
	// classifyPlanningTrust keeps its own ID guard as defense in depth.
	for _, c := range comments {
		if c.ID <= 0 {
			logf(out, "dispatch: planning trust probe for %s#%d returned a comment with a non-positive id\n", t.Repo, t.Number)
			return unresolved
		}
	}
	return classifyPlanningTrust(t.Author, t.AuthorIsBot, comments, permOf)
}

// trustVerdict is one login's outcome inside classifyPlanningTrust. The zero
// value is trustDenied, never trustGranted, so an unset verdict cannot grant
// trust (default-deny, watch/docs/error-handling.md). Nothing compares the
// values numerically; only equality is used.
type trustVerdict int

const (
	trustDenied trustVerdict = iota
	trustUnsettled
	trustGranted
)

// permissionTrustVerdict maps a WritePermission onto a trust verdict, with a
// default-deny default: only granted trusts, a recognized non-write value
// (and an invalid login) denies, and every other class -- the zero value and
// any unrecognized value included -- is unsettled. The invalid-login case is
// denied here, unlike the resume path's writePermissionAnswerProbe, which
// reports it as its own unresolved class (#847 Q3).
func permissionTrustVerdict(p WritePermission) trustVerdict {
	switch p {
	case WritePermissionGranted:
		return trustGranted
	case WritePermissionDenied, WritePermissionLoginInvalid:
		return trustDenied
	default:
		return trustUnsettled
	}
}

// classifyPlanningTrust is the pure core of the trust probe (#847): it never
// shells out. The issue author has no association prefilter (gh issue list
// carries none); a bot author (isBotLogin, which covers `app/<slug>`, or gh's
// is_bot flag) or a login failing githubLoginPattern is denied without a
// lookup, and any other author is resolved through permOf.
//
// A comment counts as trusted only when its author is not a bot (isBotLogin or
// REST `user.type == "Bot"`), its author_association is OWNER, MEMBER or
// COLLABORATOR, its login matches githubLoginPattern, and permOf grants
// `admin`/`write`. A comment failing any of the first three never reaches
// permOf. The `<!-- cenci-` marker is not consulted at all: it never grants
// trust, so a marker comment by an untrusted login still holds the ticket.
//
// Every comment is evaluated, with no short-circuit, so the reported class
// does not depend on scan order. Precedence, highest first: a non-positive
// comment ID (malformed data, unresolved, checked before any lookup), then any
// unsettled permission lookup, then a denied author, then a denied comment
// (the lowest such ID, since comments are sorted by ID ascending first).
// Unresolved outranks denied (#882): both hold the ticket, only the reason
// differs.
func classifyPlanningTrust(author string, authorIsBot bool, comments []restIssueComment, permOf func(string) WritePermission) PlanningTrustProbe {
	for _, c := range comments {
		if c.ID <= 0 {
			return PlanningTrustProbe{Class: PlanningTrustUnresolved}
		}
	}
	sorted := make([]restIssueComment, len(comments))
	copy(sorted, comments)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	unsettled := false
	authorVerdict := trustDenied
	if !authorIsBot && !isBotLogin(author) && githubLoginPattern.MatchString(author) {
		authorVerdict = permissionTrustVerdict(permOf(author))
	}
	if authorVerdict == trustUnsettled {
		unsettled = true
	}

	var deniedID int64
	for _, c := range sorted {
		verdict := trustDenied
		if !isBotLogin(c.Author.Login) && c.Author.Type != "Bot" &&
			isAuthorizedAssociation(c.AuthorAssociation) &&
			githubLoginPattern.MatchString(c.Author.Login) {
			verdict = permissionTrustVerdict(permOf(c.Author.Login))
		}
		switch verdict {
		case trustUnsettled:
			unsettled = true
		case trustDenied:
			if deniedID == 0 {
				deniedID = c.ID
			}
		}
	}

	switch {
	case unsettled:
		return PlanningTrustProbe{Class: PlanningTrustPermissionUnresolved}
	case authorVerdict == trustDenied:
		return PlanningTrustProbe{Class: PlanningTrustAuthorDenied}
	case deniedID > 0:
		return PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: deniedID}
	default:
		return PlanningTrustProbe{Class: PlanningTrustTrusted}
	}
}
