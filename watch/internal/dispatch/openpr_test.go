package dispatch

// Ticket #881: direct probe tests for openpr.go's bounded, cursor-paginated
// open-PR inventory probe -- openPRInventory(repo string, out io.Writer)
// (map[int]bool, OpenPRProbe). It replaces the single capped `gh pr list
// --limit 200` call (openPRIssues, deleted -- moved here from collect.go)
// with an explicitly bounded `gh api graphql` traversal, classifying the
// outcome into the OpenPRProbe closed set (types.go) instead of silently
// treating a hit cap as complete.
//
// Every gh invocation this probe makes is `gh api graphql ...`, so a
// PATH-shimmed fake dispatches on "$1 $2" == "api graphql" -- matching every
// other fake-gh fixture in this package (collect_test.go,
// dispatch_run_test.go, mainsync_test.go, chainfake_test.go). Each page's
// response shares one query shape (the contract Phase 4's real query must
// satisfy):
//
//	{"data":{"repository":{"pullRequests":{
//	  "totalCount": <int>,
//	  "pageInfo": {"hasNextPage": <bool>, "endCursor": <string|null>},
//	  "nodes": [
//	    {"number": <PR number>, "closingIssuesReferences": {
//	      "pageInfo": {"hasNextPage": <bool>},
//	      "nodes": [{"number": <issue number>}, ...]
//	    }},
//	    ...
//	  ]
//	}}}}
//
// totalCount is read only from the FIRST page's response, purely as a cheap
// pre-flight cap check (never a strict post-traversal equality proof --
// pageInfo.hasNextPage is the authoritative completeness signal, per the
// plan's Assumptions): a totalCount exceeding maxOpenPRRecords short-circuits
// immediately as OpenPRProbeCapExhausted without fetching a second page.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// openPRScriptedPage is one gh invocation's scripted response for
// installOpenPRScriptedGH.
type openPRScriptedPage struct {
	stdout string
	stderr string
	exit   int
	sleep  time.Duration
}

// installOpenPRScriptedGH installs a `gh` PATH shim (prepended, never
// replacing PATH -- collect_test.go:28-33's documented hazard: PATH
// replacement would make git unresolvable) that serves pages[i] as the
// (i+1)-th invocation's response, appending the full argv of every
// invocation (one per line) to a call-log file. An invocation beyond
// len(pages) is an unrecognized-invocation error (exit 1, distinct stderr
// marker), so a runaway loop making more calls than scripted fails loudly
// rather than silently repeating the last page. The returned func reads the
// call log back, so a test can assert exact call counts (AC6).
func installOpenPRScriptedGH(t *testing.T, pages []openPRScriptedPage) func() []string {
	t.Helper()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	logFile := filepath.Join(dir, "calls.log")
	respDir := filepath.Join(dir, "resp")
	if err := os.MkdirAll(respDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, p := range pages {
		n := i + 1
		if err := os.WriteFile(filepath.Join(respDir, strconv.Itoa(n)+".out"), []byte(p.stdout), 0o644); err != nil {
			t.Fatal(err)
		}
		if p.stderr != "" {
			if err := os.WriteFile(filepath.Join(respDir, strconv.Itoa(n)+".err"), []byte(p.stderr), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(respDir, strconv.Itoa(n)+".exit"), []byte(strconv.Itoa(p.exit)), 0o644); err != nil {
			t.Fatal(err)
		}
		if p.sleep > 0 {
			if err := os.WriteFile(filepath.Join(respDir, strconv.Itoa(n)+".sleep"), []byte(strconv.Itoa(int(p.sleep.Seconds()))), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[1]q
n=$(cat %[2]q 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > %[2]q
sleepfile=%[3]s/$n.sleep
[ -f "$sleepfile" ] && sleep "$(cat "$sleepfile")"
if [ "$n" -gt %[4]d ]; then
  echo "openpr fake: unscripted extra gh invocation $n" >&2
  exit 1
fi
outfile=%[3]s/$n.out
[ -f "$outfile" ] && cat "$outfile"
errfile=%[3]s/$n.err
[ -f "$errfile" ] && cat "$errfile" >&2
exit "$(cat %[3]s/$n.exit)"
`, logFile, countFile, respDir, len(pages))

	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		data, err := os.ReadFile(logFile)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		var out []string
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
}

// openPRFixturePR describes one PR node in a scripted page response: number
// is the PR's own number (only used to keep fixture PRs distinguishable),
// closing is the set of issue numbers its closingIssuesReferences.nodes
// carries, and closingHasNextPage stamps the nested
// closingIssuesReferences.pageInfo.hasNextPage bit (AC2's "nested
// truncation" class).
type openPRFixturePR struct {
	number             int
	closing            []int
	closingHasNextPage bool
}

// openPRPageJSON assembles one gh api graphql page response in the shape
// openPRInventory's query is expected to request (see this file's package
// doc comment above).
func openPRPageJSON(totalCount int, hasNextPage bool, endCursor string, prs []openPRFixturePR) string {
	nodes := make([]string, 0, len(prs))
	for _, pr := range prs {
		closingNodes := make([]string, 0, len(pr.closing))
		for _, n := range pr.closing {
			closingNodes = append(closingNodes, fmt.Sprintf(`{"number":%d}`, n))
		}
		nodes = append(nodes, fmt.Sprintf(
			`{"number":%d,"closingIssuesReferences":{"pageInfo":{"hasNextPage":%v},"nodes":[%s]}}`,
			pr.number, pr.closingHasNextPage, strings.Join(closingNodes, ",")))
	}
	cursorJSON := "null"
	if endCursor != "" {
		cursorJSON = strconv.Quote(endCursor)
	}
	return fmt.Sprintf(
		`{"data":{"repository":{"pullRequests":{"totalCount":%d,"pageInfo":{"hasNextPage":%v,"endCursor":%s},"nodes":[%s]}}}}`,
		totalCount, hasNextPage, cursorJSON, strings.Join(nodes, ","))
}

// fillerPRs builds count PRs, numbered from page*100000, closing no issues --
// used to pad a page to a realistic size without polluting the returned map.
func fillerPRs(page, count int) []openPRFixturePR {
	prs := make([]openPRFixturePR, count)
	for i := 0; i < count; i++ {
		prs[i] = openPRFixturePR{number: page*100000 + i}
	}
	return prs
}

// -- AC1: a linked PR beyond the first 200 open PRs still blocks dispatch --

// TestOpenPRInventory_LinkedPRBeyondFirstPageBound_StillDetected covers AC1:
// 3 fake pages (300 PRs total, well within maxOpenPRRecords), with the only
// PR closing issue #42 living on a different page in each subtest -- proving
// aggregation across all fetched pages, not merely the first
// openPRPageSize-sized window the legacy `gh pr list --limit 200` call used
// to cap at. The second subtest re-runs with the target moved to the FIRST
// page (the pages served in the opposite relative position) to prove
// correctness does not depend on pagination order (implementation notes:
// "include pagination-order changes in tests").
func TestOpenPRInventory_LinkedPRBeyondFirstPageBound_StillDetected(t *testing.T) {
	const total = 3 * openPRPageSize

	t.Run("target on the last page", func(t *testing.T) {
		pages := []openPRScriptedPage{
			{stdout: openPRPageJSON(total, true, "c2", fillerPRs(0, openPRPageSize))},
			{stdout: openPRPageJSON(total, true, "c3", fillerPRs(1, openPRPageSize))},
			{stdout: openPRPageJSON(total, false, "", []openPRFixturePR{{number: 999999, closing: []int{42}}})},
		}
		installOpenPRScriptedGH(t, pages)

		m, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeComplete {
			t.Fatalf("probe = %q, want OpenPRProbeComplete", probe)
		}
		if !m[42] {
			t.Fatalf("map = %v, want issue #42 present (linked PR beyond the legacy 200-PR window)", m)
		}
	})

	t.Run("target on the first page (reversed relative position)", func(t *testing.T) {
		pages := []openPRScriptedPage{
			{stdout: openPRPageJSON(total, true, "c2", []openPRFixturePR{{number: 999999, closing: []int{42}}})},
			{stdout: openPRPageJSON(total, true, "c3", fillerPRs(1, openPRPageSize))},
			{stdout: openPRPageJSON(total, false, "", fillerPRs(2, openPRPageSize))},
		}
		installOpenPRScriptedGH(t, pages)

		m, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeComplete {
			t.Fatalf("probe = %q, want OpenPRProbeComplete", probe)
		}
		if !m[42] {
			t.Fatalf("map = %v, want issue #42 present regardless of which page it appears on (order-independence)", m)
		}
	})
}

// -- AC2: distinct reasons per failure class ---------------------------------

// TestOpenPRInventory_CapExhaustion_PageBoundReachedWithHasNextPageTrue
// covers the first cap-exhaustion trigger: the page bound (maxOpenPRPages)
// is reached while the server still reports hasNextPage: true.
// totalCount is pinned to exactly maxOpenPRRecords (never exceeding it) so
// the OTHER cap-exhaustion trigger (the totalCount pre-flight short-circuit,
// tested separately below) never fires here.
func TestOpenPRInventory_CapExhaustion_PageBoundReachedWithHasNextPageTrue(t *testing.T) {
	pages := make([]openPRScriptedPage, maxOpenPRPages)
	for i := range pages {
		pages[i] = openPRScriptedPage{
			stdout: openPRPageJSON(maxOpenPRRecords, true, fmt.Sprintf("c%d", i+2), fillerPRs(i, openPRPageSize)),
		}
	}
	calls := installOpenPRScriptedGH(t, pages)

	_, probe := openPRInventory("o/r", io.Discard)
	if probe != OpenPRProbeCapExhausted {
		t.Fatalf("probe = %q, want OpenPRProbeCapExhausted", probe)
	}
	if got := len(calls()); got != maxOpenPRPages {
		t.Fatalf("gh invocations = %d, want exactly maxOpenPRPages (%d)", got, maxOpenPRPages)
	}
}

// TestOpenPRInventory_CapExhaustion_TotalCountPreflightShortCircuits_OneCallOnly
// covers the SECOND cap-exhaustion trigger named explicitly by the plan's
// Test Strategy: a totalCount exceeding maxOpenPRRecords on the very first
// page short-circuits immediately -- exactly 1 gh call, never traversing
// further pages just to hit the ordinary page-bound cap.
func TestOpenPRInventory_CapExhaustion_TotalCountPreflightShortCircuits_OneCallOnly(t *testing.T) {
	pages := []openPRScriptedPage{
		{stdout: openPRPageJSON(maxOpenPRRecords+1, true, "c2", fillerPRs(0, openPRPageSize))},
	}
	calls := installOpenPRScriptedGH(t, pages)

	_, probe := openPRInventory("o/r", io.Discard)
	if probe != OpenPRProbeCapExhausted {
		t.Fatalf("probe = %q, want OpenPRProbeCapExhausted", probe)
	}
	if got := len(calls()); got != 1 {
		t.Fatalf("gh invocations = %d, want exactly 1 (the totalCount pre-flight short-circuit must never traverse further pages)", got)
	}
}

// TestOpenPRInventory_NestedTruncation_ClosingIssuesReferencesOverflow covers
// the nested-truncation class: a PR whose closingIssuesReferences.pageInfo
// reports hasNextPage: true must classify OpenPRProbeTruncated, distinct
// from the outer pagination's own cap-exhaustion class. The partially-seen
// issue is still present in the returned map (plan Assumptions: "a partial
// inventory still sets HasOpenPR = true for every PR actually seen").
func TestOpenPRInventory_NestedTruncation_ClosingIssuesReferencesOverflow(t *testing.T) {
	pages := []openPRScriptedPage{
		{stdout: openPRPageJSON(1, false, "", []openPRFixturePR{
			{number: 5, closing: []int{42}, closingHasNextPage: true},
		})},
	}
	installOpenPRScriptedGH(t, pages)

	m, probe := openPRInventory("o/r", io.Discard)
	if probe != OpenPRProbeTruncated {
		t.Fatalf("probe = %q, want OpenPRProbeTruncated", probe)
	}
	if !m[42] {
		t.Errorf("map = %v, want the partially-seen issue #42 still present", m)
	}
}

// TestOpenPRInventory_MalformedPage covers AC2's "malformed page" class,
// content-specifically distinct from every other failure class
// (watch/docs/error-handling.md #446): a non-JSON body, a GraphQL errors[]
// payload, a stdout-cap overflow, a hasNextPage: true page with an
// empty/non-advancing endCursor, and a null/absent `repository` (#881
// review fix #1) must all classify OpenPRProbeMalformed.
func TestOpenPRInventory_MalformedPage(t *testing.T) {
	t.Run("non-JSON body", func(t *testing.T) {
		installOpenPRScriptedGH(t, []openPRScriptedPage{{stdout: "not json at all"}})

		_, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeMalformed {
			t.Fatalf("probe = %q, want OpenPRProbeMalformed", probe)
		}
	})

	t.Run("GraphQL errors[] payload", func(t *testing.T) {
		installOpenPRScriptedGH(t, []openPRScriptedPage{{stdout: `{"errors":[{"message":"field not found"}]}`}})

		_, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeMalformed {
			t.Fatalf("probe = %q, want OpenPRProbeMalformed", probe)
		}
	})

	t.Run("stdout-cap overflow", func(t *testing.T) {
		// Far beyond any reasonable maxOpenPRStdoutBytes bound Phase 4 picks.
		installOpenPRScriptedGH(t, []openPRScriptedPage{{stdout: strings.Repeat("x", 8<<20)}})

		_, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeMalformed {
			t.Fatalf("probe = %q, want OpenPRProbeMalformed (a stdout-cap overflow classifies as malformed, distinct from a plain command failure)", probe)
		}
	})

	t.Run("hasNextPage true with empty endCursor", func(t *testing.T) {
		installOpenPRScriptedGH(t, []openPRScriptedPage{
			{stdout: openPRPageJSON(1, true, "", []openPRFixturePR{{number: 5, closing: []int{42}}})},
		})

		_, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeMalformed {
			t.Fatalf("probe = %q, want OpenPRProbeMalformed (hasNextPage true with no cursor to advance on)", probe)
		}
	})

	t.Run("hasNextPage true with non-advancing endCursor", func(t *testing.T) {
		installOpenPRScriptedGH(t, []openPRScriptedPage{
			{stdout: openPRPageJSON(1, true, "same-cursor", nil)},
			{stdout: openPRPageJSON(1, true, "same-cursor", nil)},
		})

		_, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeMalformed {
			t.Fatalf("probe = %q, want OpenPRProbeMalformed (a cursor that never advances across pages must never be trusted)", probe)
		}
	})

	// #881 review fix #1: a response body that is valid JSON, carries no
	// top-level errors[], and exits 0 -- but whose `data.repository` is
	// null or entirely absent -- must never decode as an all-zero,
	// verifiably-complete, zero-open-PR inventory (OpenPRProbeComplete with
	// an empty map). A real "repo not found"/"no read access" GraphQL
	// response looks exactly like this, so silently treating it as
	// "complete, zero PRs" would reintroduce the exact fail-open class this
	// ticket exists to eliminate.

	t.Run("data.repository explicitly null", func(t *testing.T) {
		installOpenPRScriptedGH(t, []openPRScriptedPage{{stdout: `{"data":{"repository":null}}`}})

		_, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeMalformed {
			t.Fatalf("probe = %q, want OpenPRProbeMalformed (a null repository must never be indistinguishable from a verifiably-empty inventory)", probe)
		}
	})

	t.Run("data present but repository key absent entirely", func(t *testing.T) {
		installOpenPRScriptedGH(t, []openPRScriptedPage{{stdout: `{"data":{}}`}})

		_, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeMalformed {
			t.Fatalf("probe = %q, want OpenPRProbeMalformed", probe)
		}
	})

	t.Run("entirely empty body", func(t *testing.T) {
		installOpenPRScriptedGH(t, []openPRScriptedPage{{stdout: `{}`}})

		_, probe := openPRInventory("o/r", io.Discard)
		if probe != OpenPRProbeMalformed {
			t.Fatalf("probe = %q, want OpenPRProbeMalformed", probe)
		}
	})
}

// TestOpenPRInventory_Timeout_SleepingShimClassifiesTimeout covers the
// timeout class: a `gh` invocation that sleeps far longer than the probe's
// bound must be killed and classify OpenPRProbeTimeout, returning well
// before the full sleep duration elapses. errors.Is(err, errGhTimeout) at
// the execGhBounded boundary itself is pinned directly in gh_test.go (#412).
func TestOpenPRInventory_Timeout_SleepingShimClassifiesTimeout(t *testing.T) {
	installOpenPRScriptedGH(t, []openPRScriptedPage{{sleep: 120 * time.Second}})

	start := time.Now()
	_, probe := openPRInventory("o/r", io.Discard)
	elapsed := time.Since(start)

	if probe != OpenPRProbeTimeout {
		t.Fatalf("probe = %q, want OpenPRProbeTimeout", probe)
	}
	if elapsed >= 100*time.Second {
		t.Fatalf("openPRInventory took %v, want it bounded well within ghTimeout, not the full 120s sleep", elapsed)
	}
}

// TestOpenPRInventory_SharedDeadline_ExceededAcrossPages covers #881 review
// fix #3: the plan's "one shared wall-clock deadline for the whole probe"
// requirement, distinct from ghTimeout's own per-call bound. Page 1 sleeps
// briefly and completes normally (reporting hasNextPage: true, well within
// both its own ghTimeout and the overridden shared budget); page 2 sleeps
// far longer than what remains of the shared budget by the time it starts,
// but far short of its own ghTimeout (60s) -- so if openPRInventory only
// bounded each page individually (the pre-fix behavior), this traversal
// would run to completion (or hit ghTimeout on page 2 much later); with the
// shared deadline wired in, page 2's process is killed once the shared
// budget's remainder elapses, well before either its own full sleep
// duration or ghTimeout, and the outcome still classifies as the same
// OpenPRProbeTimeout failure class as an individual page timeout.
//
// Once page 2's underlying process is killed, cmd.Wait can still block for
// up to ghWaitDelay (5s) waiting on the scripted shim's own `sleep`
// grandchild to relinquish the inherited stdout/stderr pipes (the same
// mechanic TestOpenPRInventory_Timeout_SleepingShimClassifiesTimeout's own
// elapsed-time bound already accounts for) -- so this test's own bound
// gives headroom for that on top of the shared budget's remainder, while
// staying an order of magnitude below page 2's scripted sleep and below
// what a page-individual-only ghTimeout would take (~65s).
//
// maxOpenPRProbeDuration is overridden to a small value for this test only
// (restored via t.Cleanup) specifically so proving this does not require
// the test itself to run for tens of seconds or minutes.
func TestOpenPRInventory_SharedDeadline_ExceededAcrossPages(t *testing.T) {
	orig := maxOpenPRProbeDuration
	maxOpenPRProbeDuration = 3 * time.Second
	t.Cleanup(func() { maxOpenPRProbeDuration = orig })

	pages := []openPRScriptedPage{
		{stdout: openPRPageJSON(2, true, "c2", fillerPRs(0, 1)), sleep: 1 * time.Second},
		{sleep: 120 * time.Second},
	}
	installOpenPRScriptedGH(t, pages)

	start := time.Now()
	_, probe := openPRInventory("o/r", io.Discard)
	elapsed := time.Since(start)

	if probe != OpenPRProbeTimeout {
		t.Fatalf("probe = %q, want OpenPRProbeTimeout (the shared probe deadline elapsing mid-page-2 must classify the same as an individual page timeout)", probe)
	}
	if elapsed >= 20*time.Second {
		t.Fatalf("openPRInventory took %v, want the shared 3s probe deadline (plus ghWaitDelay headroom) to cut page 2 short well before either its own 120s scripted sleep or ghTimeout's 60s per-call bound", elapsed)
	}
}

// TestOpenPRInventory_MidPaginationFailure_Page1OkPage2Fails covers the
// mid-pagination-failure class: page 1 succeeds, page 2 exits nonzero.
// Distinct from cap exhaustion (the traversal broke, not merely hit a
// bound) -- classifies OpenPRProbeUnreadable. The page-1 issue is still
// present in the returned partial map.
func TestOpenPRInventory_MidPaginationFailure_Page1OkPage2Fails(t *testing.T) {
	pages := []openPRScriptedPage{
		{stdout: openPRPageJSON(200, true, "c2", []openPRFixturePR{{number: 5, closing: []int{42}}})},
		{stderr: "gh: rate limited\n", exit: 1},
	}
	calls := installOpenPRScriptedGH(t, pages)

	m, probe := openPRInventory("o/r", io.Discard)
	if probe != OpenPRProbeUnreadable {
		t.Fatalf("probe = %q, want OpenPRProbeUnreadable", probe)
	}
	if !m[42] {
		t.Errorf("map = %v, want the successfully-fetched page 1's issue #42 still present (partial results preserved)", m)
	}
	if got := len(calls()); got != 2 {
		t.Fatalf("gh invocations = %d, want exactly 2 (page 1 succeeded, page 2 failed)", got)
	}
}

// -- AC3: empty/small complete inventories preserve current eligible
// behavior, exactly one gh call each -----------------------------------------

func TestOpenPRInventory_EmptyInventory_OneCallComplete(t *testing.T) {
	calls := installOpenPRScriptedGH(t, []openPRScriptedPage{{stdout: openPRPageJSON(0, false, "", nil)}})

	m, probe := openPRInventory("o/r", io.Discard)
	if probe != OpenPRProbeComplete {
		t.Fatalf("probe = %q, want OpenPRProbeComplete", probe)
	}
	if len(m) != 0 {
		t.Fatalf("map = %v, want empty", m)
	}
	if got := len(calls()); got != 1 {
		t.Fatalf("gh invocations = %d, want exactly 1", got)
	}
}

func TestOpenPRInventory_FivePRs_OneCallComplete(t *testing.T) {
	prs := []openPRFixturePR{
		{number: 1, closing: []int{10}},
		{number: 2, closing: []int{11}},
		{number: 3, closing: []int{12}},
		{number: 4, closing: []int{13}},
		{number: 5, closing: []int{14}},
	}
	calls := installOpenPRScriptedGH(t, []openPRScriptedPage{{stdout: openPRPageJSON(5, false, "", prs)}})

	m, probe := openPRInventory("o/r", io.Discard)
	if probe != OpenPRProbeComplete {
		t.Fatalf("probe = %q, want OpenPRProbeComplete", probe)
	}
	for _, n := range []int{10, 11, 12, 13, 14} {
		if !m[n] {
			t.Errorf("map = %v, want issue #%d present", m, n)
		}
	}
	if got := len(calls()); got != 1 {
		t.Fatalf("gh invocations = %d, want exactly 1", got)
	}
}

// -- AC4: multiple PRs closing one issue, and one PR closing multiple
// issues, split across a page boundary ---------------------------------------

func TestOpenPRInventory_MultiMapping_SplitAcrossPageBoundary(t *testing.T) {
	pages := []openPRScriptedPage{
		{stdout: openPRPageJSON(3, true, "c2", []openPRFixturePR{
			{number: 1, closing: []int{42}}, // first PR closing #42
		})},
		{stdout: openPRPageJSON(3, false, "", []openPRFixturePR{
			{number: 2, closing: []int{42}},         // second PR ALSO closing #42
			{number: 3, closing: []int{10, 11, 12}}, // one PR closing three issues
		})},
	}
	installOpenPRScriptedGH(t, pages)

	m, probe := openPRInventory("o/r", io.Discard)
	if probe != OpenPRProbeComplete {
		t.Fatalf("probe = %q, want OpenPRProbeComplete", probe)
	}
	for _, n := range []int{42, 10, 11, 12} {
		if !m[n] {
			t.Errorf("map = %v, want issue #%d present", m, n)
		}
	}
}
