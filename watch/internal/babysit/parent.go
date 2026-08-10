package babysit

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// parentGapMarker is the hidden HTML-comment marker
// `skills/implement/phases/phase-9-pr.md`'s parent acceptance-criteria gap
// report embeds in its posted comment (flow/docs/comment-attribution.md's
// `parent-gap-report` `<kind>`, registered there alongside the marker's
// literal). Its presence on a parent issue's comment thread -- read only
// after stripBlockquoteLines strips every blockquoted line, mirroring
// isCenciAuthored's own defense against a quote-reply forgery
// (watch/internal/dispatch/resume.go) -- holds merge-time parent
// reconciliation: the report records a known, human-unresolved
// acceptance-criteria gap, so the parent must not be auto-closed underneath
// it even though every sub-issue is CLOSED.
const parentGapMarker = "<!-- cenci-parent-gap-report -->"

// Reason-prefix constants for reconcileParents' distinguishable error/outcome
// strings (#811). Each names one, and only one, distinct fail-closed gate or
// terminal outcome, so a caller's strings.Contains assertion never conflates
// two different failure modes (watch/docs/error-handling.md #446).
const (
	// reasonParentReadFailed is used when a closing child issue's own native
	// `parent` relationship (`gh issue view <child> --json parent`) cannot be
	// read at all -- a `gh` command failure, never "no parent".
	reasonParentReadFailed = "read parent relationship"

	// reasonParentGraphReadFailed is used when a parent issue's own
	// `gh issue view <parent> --json state,subIssues` call fails outright --
	// a `gh` command failure, distinct from every content-validity reason
	// below, which all require the read to have succeeded first.
	reasonParentGraphReadFailed = "read sub-issue graph"

	// reasonParentGraphUnavailable is used when subIssues decodes as JSON
	// null or is absent from an otherwise-successful read -- distinct from
	// reasonParentGraphReadFailed (the read itself failing).
	reasonParentGraphUnavailable = "sub-issue graph unavailable"

	// reasonParentGraphEmpty is used when subIssues.totalCount == 0.
	reasonParentGraphEmpty = "sub-issue graph empty"

	// reasonParentGraphTruncated is used when len(subIssues.nodes) !=
	// subIssues.totalCount -- more sub-issues exist than were returned.
	reasonParentGraphTruncated = "sub-issue graph truncated"

	// reasonParentGraphMalformed is used when the graph read succeeded and
	// is internally consistent (nodes present, counts matching) but at least
	// one node itself decoded to its zero value -- content GitHub could not,
	// or did not, actually populate.
	reasonParentGraphMalformed = "sub-issue graph malformed"

	// reasonParentGraphIncoherent is used when the child issue whose closure
	// triggered this parent lookup is not itself present among the parent's
	// own sub-issue nodes -- the two native relationships disagree.
	reasonParentGraphIncoherent = "sub-issue graph incoherent"

	// reasonParentCommentsUnreadable is used when the parent's own issue
	// comments (the gap-report scan) cannot be read at all -- a `gh`/fetchPaged
	// failure, distinct from every graph-read reason above (this read only
	// ever runs once the graph gate chain has already passed) and from
	// reasonParentCommentsTruncated below.
	reasonParentCommentsUnreadable = "parent gap-report comments unreadable"

	// reasonParentCommentsTruncated is used when fetchPaged's own
	// completeness signal reports complete == false (the page cap was
	// exhausted while pages were still full-sized) -- a marker beyond the
	// fetched pages can never be ruled out, so this is a distinct fail-closed
	// reason from reasonParentCommentsUnreadable, never folded into it.
	reasonParentCommentsTruncated = "parent gap-report comments truncated"

	// reasonParentLabelEditFailed is used when the parent's own label-swap
	// `gh issue edit` call (remove "In Review", add "Implemented") fails.
	// Distinct from every reason above: it is the one gate that runs after
	// every read-side gate has already passed and the parent is genuinely
	// ready to close, so a failure here must still surface as a
	// non-terminal, retryable error rather than being silently discarded --
	// and, critically, must stop reconcileOneParent from ever issuing the
	// subsequent `issue close` call. A close with no successful label swap
	// would permanently close the parent under the wrong labels: the next
	// tick's already-CLOSED branch (graph.State == "CLOSED") short-circuits
	// before ever touching labels again, so that mistake is unrecoverable.
	reasonParentLabelEditFailed = "parent label edit failed"
)

// parentOutcome's Kind values -- the closed set of distinguishable terminal
// outcomes reconcileParents reports for one distinct open parent it actually
// reached the comments gate for. A parent held on the sub-issue graph gate
// (some sibling still open) produces no parentOutcome entry at all -- it is
// not yet reconcileParents' concern, and the next tick's own child-merge
// event will re-trigger the lookup.
const (
	parentOutcomeClosed        = "closed"
	parentOutcomeHeld          = "held"
	parentOutcomeAlreadyClosed = "already-closed"
)

// parentOutcome describes what reconcileParents did for one distinct open
// parent issue.
type parentOutcome struct {
	// Parent is the parent issue number.
	Parent int
	// Kind is one of the parentOutcomeXxx constants above.
	Kind string
}

// parentRelation is the `gh issue view <n> --json parent` response shape.
// Parent is a pointer so a genuinely absent/null native parent relationship
// is distinguishable from a decoded zero value (mirroring gh.go's
// graphQLQueueResponse pointer-field convention).
type parentRelation struct {
	Parent *struct {
		Number int `json:"number"`
	} `json:"parent"`
}

// subIssueNode is one entry in a parent's `subIssues.nodes` graph.
type subIssueNode struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

// parentGraph is the `gh issue view <n> --json state,subIssues` response
// shape. SubIssues is a pointer so a JSON-null/absent graph is
// distinguishable from a present-but-empty one (reasonParentGraphUnavailable
// vs reasonParentGraphEmpty).
type parentGraph struct {
	State     string `json:"state"`
	SubIssues *struct {
		TotalCount int            `json:"totalCount"`
		Nodes      []subIssueNode `json:"nodes"`
	} `json:"subIssues"`
}

// reconcileParents reconciles split-parent completion from live GitHub
// sub-issue state for every parent reachable from closing -- the issue
// numbers a just-merged PR closed this tick (#811). Ordered, deduped by
// parent (a parent shared by two closing children in the same tick is only
// ever read/mutated once), fail-closed on every graph/comment read failure
// (errors.Join over every distinct child/parent failure, never a first-error
// early return, so an unrelated success alongside a failure both stay
// discoverable), and never mutates a parent unless its full sub-issue graph
// is verified all-CLOSED and its comment thread carries no unresolved
// parentGapMarker.
func reconcileParents(repo string, closing []int) ([]parentOutcome, error) {
	var errs []error
	var order []int
	originatingChild := map[int]int{}
	seen := map[int]bool{}

	for _, child := range closing {
		var rel parentRelation
		if err := ghJSON(&rel, "issue", "view", strconv.Itoa(child), "--repo", repo, "--json", "parent"); err != nil {
			errs = append(errs, fmt.Errorf("child #%d: %s: %w", child, reasonParentReadFailed, err))
			continue
		}
		if rel.Parent == nil {
			continue
		}
		p := rel.Parent.Number
		if seen[p] {
			continue
		}
		seen[p] = true
		order = append(order, p)
		originatingChild[p] = child
	}

	var outcomes []parentOutcome
	for _, p := range order {
		outcome, err := reconcileOneParent(repo, p, originatingChild[p])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if outcome != nil {
			outcomes = append(outcomes, *outcome)
		}
	}

	return outcomes, errors.Join(errs...)
}

// reconcileOneParent reconciles one distinct parent's completion state,
// given the child issue number whose closure discovered it (used solely for
// the graph's coherence check). Returns (nil, nil) on a hold that is not an
// error (an open sibling), a non-nil outcome on every terminal success
// (already-closed, held, closed), or a non-nil error naming parent (and, for
// the parent-read failure this never sees -- that lives in reconcileParents
// -- naming child) on every fail-closed gate.
func reconcileOneParent(repo string, parent, originatingChild int) (*parentOutcome, error) {
	var graph parentGraph
	if err := ghJSON(&graph, "issue", "view", strconv.Itoa(parent), "--repo", repo, "--json", "state,subIssues"); err != nil {
		return nil, fmt.Errorf("parent #%d: %s: %w", parent, reasonParentGraphReadFailed, err)
	}
	if graph.State == "CLOSED" {
		return &parentOutcome{Parent: parent, Kind: parentOutcomeAlreadyClosed}, nil
	}
	if graph.SubIssues == nil {
		return nil, fmt.Errorf("parent #%d: %s", parent, reasonParentGraphUnavailable)
	}
	if graph.SubIssues.TotalCount == 0 {
		return nil, fmt.Errorf("parent #%d: %s", parent, reasonParentGraphEmpty)
	}
	if len(graph.SubIssues.Nodes) != graph.SubIssues.TotalCount {
		return nil, fmt.Errorf("parent #%d: %s", parent, reasonParentGraphTruncated)
	}
	foundChild := false
	allClosed := true
	for _, n := range graph.SubIssues.Nodes {
		if n.Number == 0 || n.State == "" {
			return nil, fmt.Errorf("parent #%d: %s", parent, reasonParentGraphMalformed)
		}
		if n.Number == originatingChild {
			foundChild = true
		}
		if n.State != "CLOSED" {
			allClosed = false
		}
	}
	if !foundChild {
		return nil, fmt.Errorf("parent #%d: %s", parent, reasonParentGraphIncoherent)
	}
	if !allClosed {
		// A sibling is still open: this is a hold, not an error, and not yet
		// reconcileParents' concern (the next tick's own child-merge event
		// re-triggers the lookup) -- no outcome entry.
		return nil, nil
	}

	comments, complete, err := fetchPaged[comment]("repos/" + repo + "/issues/" + strconv.Itoa(parent) + "/comments")
	if err != nil {
		return nil, fmt.Errorf("parent #%d: %s: %w", parent, reasonParentCommentsUnreadable, err)
	}
	if !complete {
		return nil, fmt.Errorf("parent #%d: %s", parent, reasonParentCommentsTruncated)
	}
	for _, c := range comments {
		if strings.Contains(stripBlockquoteLines(c.Body), parentGapMarker) {
			return &parentOutcome{Parent: parent, Kind: parentOutcomeHeld}, nil
		}
	}

	_, _, _ = execGh("label", "create", "Implemented", "--repo", repo, "--color", "6F42C1", "--description", "PR merged — done")
	// Self-heal "In Review" the same way (ignored error, mirroring the
	// "Implemented" self-heal above): a repo that never had this label at
	// all must not fail the label swap below just because
	// --remove-label references a label that doesn't exist yet.
	_, _, _ = execGh("label", "create", "In Review", "--repo", repo, "--color", "FBCA04", "--description", "Reviewing")
	if _, stderr, err := execGh("issue", "edit", strconv.Itoa(parent), "--repo", repo, "--remove-label", "In Review", "--add-label", "Implemented"); err != nil {
		// Write ordering is label-edit-then-close (#811 Assumption): a failed
		// label edit must skip the close entirely, never proceeding to
		// permanently close the parent under the wrong labels.
		return nil, fmt.Errorf("parent #%d: %s: %s: %w", parent, reasonParentLabelEditFailed, strings.TrimSpace(stderr), err)
	}
	_, _, _ = execGh("issue", "close", strconv.Itoa(parent), "--repo", repo, "--reason", "completed")

	// A single post-close verification read is authoritative over the close
	// command's own exit code (mirroring executeMerge's verification-over-
	// exit-code contract, merge.go): a transient `gh issue close` failure
	// must not be fatal when GitHub confirms the parent is CLOSED anyway.
	var verify struct {
		State string `json:"state"`
	}
	if err := ghJSON(&verify, "issue", "view", strconv.Itoa(parent), "--repo", repo, "--json", "state"); err != nil {
		return nil, fmt.Errorf("parent #%d: verify close: %w", parent, err)
	}
	if verify.State != "CLOSED" {
		return nil, fmt.Errorf("parent #%d: close not confirmed, state %q", parent, verify.State)
	}
	return &parentOutcome{Parent: parent, Kind: parentOutcomeClosed}, nil
}

// stripBlockquoteLines removes every line whose first non-space character is
// `>` (a Markdown blockquote line -- GitHub's "Quote reply" feature prefixes
// every quoted line this way), so a gap-report marker copied verbatim into a
// human's quote reply is never mistaken for a live, unresolved report.
// Package-local copy of watch/internal/dispatch/resume.go's
// stripBlockquoteLines (that package's own isCenciAuthored uses it for the
// identical quote-reply-forgery defense).
func stripBlockquoteLines(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " \t"), ">") {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}
