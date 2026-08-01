package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// openPRPageSize is the `first:` page size openPRInventory requests per
// `pullRequests` page -- GitHub GraphQL's practical maximum, so a repo's
// open-PR set is traversed in as few pages as possible.
const openPRPageSize = 100

// openPRClosingPageSize is the `first:` page size for each PR's nested
// closingIssuesReferences connection -- generously above any legitimate PR's
// closing-issue count (Q5/Risks: set generously so the repo-wide gate this
// bound backs only fires on a genuinely pathological PR).
const openPRClosingPageSize = 50

// maxOpenPRPages bounds openPRInventory's page loop -- a traversal still
// reporting hasNextPage: true after this many pages fails closed
// (OpenPRProbeCapExhausted) rather than silently treating a partial
// traversal as complete, mirroring internal/babysit/pagination.go's
// maxFeedbackPages discipline.
const maxOpenPRPages = 20

// maxOpenPRRecords bounds the total open-PR count openPRInventory will ever
// attempt to traverse: totalCount on the first page exceeding this
// short-circuits immediately as OpenPRProbeCapExhausted without fetching a
// second page -- a cheap pre-flight cap check, never a strict
// post-traversal equality proof (pageInfo.hasNextPage is the authoritative
// completeness signal).
const maxOpenPRRecords = 2000

// maxOpenPRStdoutBytes bounds each `gh api graphql` page's stdout/stderr via
// execGhBounded -- generous for a openPRPageSize/openPRClosingPageSize-sized
// page, but exceeding it is an explicit OpenPRProbeMalformed classification,
// never a silently-short JSON body.
const maxOpenPRStdoutBytes = 4 << 20 // 4 MiB

// maxOpenPRProbeDuration is the one shared wall-clock deadline for the
// *entire* openPRInventory traversal (plan's Alternatives Considered /
// Risks sections: "page size 100, max 20 pages, <=2000 records, one shared
// wall-clock deadline"), distinct from ghTimeout, which only bounds each
// individual page's own `gh api graphql` call. Without this, a repo whose
// every page is individually slow -- but never outright hangs past
// ghTimeout on any single call -- could otherwise run
// maxOpenPRPages*ghTimeout (20 * 60s = 1200s = 20 minutes) before this
// probe's own page-bound cap even kicks in. Set to twice ghTimeout: clearly
// too short to ever legitimately need this deadline for a well-behaved
// single-page-or-few-page traversal, but generous enough that a handful of
// genuinely slow-but-not-hung pages can still complete.
//
// A var, not a const (deliberately, unlike every sibling bound above): the
// shared-deadline test (openpr_test.go) overrides it to a small value for
// the duration of one test so that proving the shared deadline (not any
// individual page's own ghTimeout) is what cuts a traversal short does not
// require the test itself to run for minutes.
var maxOpenPRProbeDuration = 2 * ghTimeout

// openPRQuery is the `gh api graphql` query openPRInventory runs, one page
// per invocation, built from openPRPageSize/openPRClosingPageSize (#357: a
// literal referenced by more than one site must stay wired to its
// constant, not hand-duplicated). Explicit orderBy (CREATED_AT, ASC) makes
// cursor paging deterministic and reproducible in tests; correctness must
// not depend on that order. $cursor is a nullable GraphQL variable: the
// first page's invocation omits the "cursor" -f/-F argument entirely (an
// undeclared nullable variable is null), rather than passing an empty
// string, which the API may reject.
//
// Deliberately single-line (no embedded newlines): this string flows
// verbatim into a `-f query=...` exec.Cmd argument, and an embedded newline
// there would break a naive line-oriented reader of the invoked process's
// own argv logging (exactly the hazard collapseLines exists to guard
// against for gh's own stdout/stderr diagnostics elsewhere in this
// package).
var openPRQuery = fmt.Sprintf(
	"query($owner: String!, $name: String!, $cursor: String) { repository(owner: $owner, name: $name) { "+
		"pullRequests(states: OPEN, first: %d, after: $cursor, orderBy: {field: CREATED_AT, direction: ASC}) { "+
		"totalCount pageInfo { hasNextPage endCursor } nodes { number closingIssuesReferences(first: %d) { "+
		"pageInfo { hasNextPage } nodes { number } } } } } }",
	openPRPageSize, openPRClosingPageSize)

// openPRGraphQLResponse is the decoded shape of one openPRQuery page
// response -- see openpr_test.go's package doc comment for the exact JSON
// shape this mirrors. Errors is populated only on a GraphQL-level error
// payload (`{"errors":[...]}`), which is distinct from (and takes priority
// over) a zero-valued Data on an otherwise-successful gh exit.
//
// Repository is a pointer, not a value struct (#881 review fix #1): a
// response body that is valid JSON, carries no top-level errors[], and
// exits 0 -- e.g. `{}`, `{"data":{}}`, or `{"data":{"repository":null}}`,
// all of which a real "repo not found"/"no read access" GraphQL response
// can produce -- would otherwise decode cleanly into an all-zero
// PullRequests (TotalCount 0, PageInfo.HasNextPage false, Nodes empty),
// making that failure indistinguishable from "repo verifiably has zero
// open PRs" and falling straight through to OpenPRProbeComplete with an
// empty map. A pointer lets openPRInventory tell "repository absent from
// the response" apart from "repository present with no open PRs" and
// classify the former as OpenPRProbeMalformed instead.
type openPRGraphQLResponse struct {
	Data struct {
		Repository *struct {
			PullRequests struct {
				TotalCount int `json:"totalCount"`
				PageInfo   struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Number                  int `json:"number"`
					ClosingIssuesReferences struct {
						PageInfo struct {
							HasNextPage bool `json:"hasNextPage"`
						} `json:"pageInfo"`
						Nodes []struct {
							Number int `json:"number"`
						} `json:"nodes"`
					} `json:"closingIssuesReferences"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// openPRInventory returns the set of issue numbers with an open linked PR,
// via a bounded, cursor-paginated `gh api graphql` traversal of repo's open
// PRs' closingIssuesReferences (#881) -- replacing the single capped `gh pr
// list --limit 200` call (openPRIssues, deleted) that silently treated a hit
// cap as complete. The returned OpenPRProbe classifies whether the
// traversal actually proved completeness (pageInfo.hasNextPage == false);
// the map is always populated with every PR actually seen, even on a
// non-complete verdict (a partial inventory is strictly safer than
// discarding partial results -- the completeness gate fires regardless, see
// decide.go's openPRGateSkip). out receives one bounded, newline-collapsed
// diagnostic line on any non-complete verdict; callers are expected to
// guarantee a non-nil out.
func openPRInventory(repo string, out io.Writer) (map[int]bool, OpenPRProbe) {
	owner, name, _ := strings.Cut(repo, "/")
	m := make(map[int]bool)
	nestedTruncated := false
	cursor := ""

	// probeCtx is the one shared wall-clock deadline for the whole
	// traversal (#881 review fix #3, plan's Alternatives Considered/Risks:
	// "one shared wall-clock deadline for the whole probe") -- distinct
	// from each individual page's own ghTimeout-bounded call below. Every
	// page's context is derived from probeCtx, so a per-page context.Err()
	// reports DeadlineExceeded whether that page's own per-call bound or
	// this shared deadline is what actually fired first.
	probeCtx, cancel := context.WithTimeout(context.Background(), maxOpenPRProbeDuration)
	defer cancel()

	for page := 1; page <= maxOpenPRPages; page++ {
		args := []string{"api", "graphql",
			"-f", "query=" + openPRQuery,
			"-f", "owner=" + owner,
			"-f", "name=" + name,
		}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}

		pageCtx, pageCancel := context.WithTimeout(probeCtx, ghTimeout)
		stdout, stderr, err := execGhBoundedCtx(pageCtx, maxOpenPRStdoutBytes, args...)
		pageCancel()
		if err != nil {
			switch {
			case errors.Is(err, errGhTimeout):
				logf(out, "dispatch: open-PR probe %s: timed out: %s\n", repo, truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes))
				return m, OpenPRProbeTimeout
			case errors.Is(err, errGhOutputTruncated):
				logf(out, "dispatch: open-PR probe %s: response exceeded bounded cap: %s\n", repo, truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes))
				return m, OpenPRProbeMalformed
			default:
				logf(out, "dispatch: open-PR probe %s: gh api graphql failed: %v: %s\n", repo, err, truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes))
				return m, OpenPRProbeUnreadable
			}
		}

		var resp openPRGraphQLResponse
		if uerr := json.Unmarshal([]byte(stdout), &resp); uerr != nil || len(resp.Errors) > 0 {
			logf(out, "dispatch: open-PR probe %s: malformed page: %s\n", repo, truncateDetail(collapseLines(stdout), maxProbeLogDetailBytes))
			return m, OpenPRProbeMalformed
		}
		if resp.Data.Repository == nil {
			// #881 review fix #1: a null/absent repository (repo not found, no
			// read access, or a bare `{}`/`{"data":{}}` body) must never be
			// indistinguishable from a verifiably-empty open-PR set -- see
			// openPRGraphQLResponse's doc comment.
			logf(out, "dispatch: open-PR probe %s: repository absent from response (not found, no access, or null): %s\n", repo, truncateDetail(collapseLines(stdout), maxProbeLogDetailBytes))
			return m, OpenPRProbeMalformed
		}

		prs := resp.Data.Repository.PullRequests

		if page == 1 && prs.TotalCount > maxOpenPRRecords {
			logf(out, "dispatch: open-PR probe %s: totalCount %d exceeds the pagination bound (%d); holding as incomplete\n", repo, prs.TotalCount, maxOpenPRRecords)
			return m, OpenPRProbeCapExhausted
		}

		for _, node := range prs.Nodes {
			if node.ClosingIssuesReferences.PageInfo.HasNextPage {
				nestedTruncated = true
			}
			for _, ref := range node.ClosingIssuesReferences.Nodes {
				m[ref.Number] = true
			}
		}

		if !prs.PageInfo.HasNextPage {
			if nestedTruncated {
				logf(out, "dispatch: open-PR probe %s: a PR's closing-issue references overflowed their page bound; holding the whole repo as incomplete\n", repo)
				return m, OpenPRProbeTruncated
			}
			return m, OpenPRProbeComplete
		}

		next := prs.PageInfo.EndCursor
		if next == "" || next == cursor {
			// #881 review fix #2: next is taken verbatim from the GraphQL API
			// response, so it goes through the same truncateDetail bound every
			// other diagnostic in this file uses before it is spliced into a
			// log line.
			logf(out, "dispatch: open-PR probe %s: hasNextPage true with a non-advancing cursor %q; malformed page\n", repo, truncateDetail(next, maxProbeLogDetailBytes))
			return m, OpenPRProbeMalformed
		}
		cursor = next
	}

	logf(out, "dispatch: open-PR probe %s: page bound (%d) reached with more pages remaining; holding as incomplete\n", repo, maxOpenPRPages)
	return m, OpenPRProbeCapExhausted
}
