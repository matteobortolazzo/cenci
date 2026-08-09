package babysit

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// -- test scaffolding ---------------------------------------------------------

// ghResp is one scripted reply for ghStub, keyed by a call's exact
// space-joined args.
type ghResp struct {
	stdout string
	err    error
}

// ghStub installs an arg-dispatching execGh double (#811): unlike
// withCommands' sequential response queue (babysit_test.go:337, which
// answers every call in call order regardless of its arguments),
// reconcileParents issues a data-dependent mix of reads across multiple
// distinct issue numbers whose relative order the test itself must not
// prescribe -- keying by the call's own joined arguments lets each script
// entry name exactly which `gh` invocation it answers. Every call, scripted
// or not, is still appended to calls in call order (mirroring withCommands'
// own "gh"-prefixed recorded-call shape), so ordering and call-count
// assertions keep working unchanged. A call with no matching script entry
// returns an error naming the unscripted call, which most tests below never
// expect to see hit a real assertion.
func ghStub(t *testing.T, calls *[][]string, script map[string]ghResp) {
	t.Helper()
	original := execGh
	execGh = func(args ...string) (string, string, error) {
		*calls = append(*calls, append([]string{"gh"}, args...))
		key := strings.Join(args, " ")
		resp, ok := script[key]
		if !ok {
			return "", "", fmt.Errorf("unscripted gh call: %s", key)
		}
		return resp.stdout, "", resp.err
	}
	t.Cleanup(func() { execGh = original })
}

// writeCalls filters calls down to every mutating `gh` invocation
// reconcileParents can issue (label create, issue edit, issue close) --
// every "zero writes" assertion below checks this list is empty, never a
// bare len(calls) count, so an unrelated extra read never masquerades as a
// write and vice versa.
func writeCalls(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		joined := strings.Join(c, " ")
		if strings.HasPrefix(joined, "gh issue edit ") ||
			strings.HasPrefix(joined, "gh issue close ") ||
			strings.HasPrefix(joined, "gh label create ") {
			out = append(out, c)
		}
	}
	return out
}

// countCalls reports how many entries in calls contain every one of parts
// (substring match against the call's own space-joined args).
func countCalls(calls [][]string, parts ...string) int {
	n := 0
	for _, c := range calls {
		joined := strings.Join(c, " ")
		match := true
		for _, p := range parts {
			if !strings.Contains(joined, p) {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

// indexOfCall returns the first index in calls whose joined args contain
// every one of parts, or -1.
func indexOfCall(calls [][]string, parts ...string) int {
	for i, c := range calls {
		joined := strings.Join(c, " ")
		match := true
		for _, p := range parts {
			if !strings.Contains(joined, p) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// commentsJSON builds a one-comment `repos/.../comments` page body carrying
// body as the comment's raw text.
func commentsJSON(body string) string {
	return fmt.Sprintf(`[{"id":1,"body":%q,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","user":{"login":"reviewer"}}]`, body)
}

// fullCommentPage builds a feedbackPageSize(100)-item comments page, none of
// which carry the gap-report marker -- used to drive fetchPaged's pagination
// past maxFeedbackPages(10) without ever finding a marker, so the parent
// gap-report scan's own completeness signal (complete == false) is the sole
// reason reconcileParents must not proceed.
func fullCommentPage(page int) string {
	items := make([]string, feedbackPageSize)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":%d,"body":"filler comment, no marker here","user":{"login":"reviewer"}}`, page*1000+i)
	}
	return "[" + strings.Join(items, ",") + "]"
}

const (
	repo = "o/r"

	// parentReadFor9 answers child issue 9's `--json parent` read with
	// parent 20.
	parentReadFor9 = `{"parent":{"number":20}}`
	// selfHealKey is the label-create self-heal call's exact args key,
	// mirroring babysit.go's own existing self-heal call shape.
	selfHealKey = "label create Implemented --repo o/r --color 6F42C1 --description PR merged — done"
	// inReviewSelfHealKey is the "In Review" label-create self-heal call's
	// exact args key (#811 Fix 3), mirroring selfHealKey's shape.
	inReviewSelfHealKey = "label create In Review --repo o/r --color FBCA04 --description Reviewing"
	// editKey20/closeKey20/verifyKey20 are parent 20's label-swap, close, and
	// post-close verification call keys.
	editKey20   = "issue edit 20 --repo o/r --remove-label In Review --add-label Implemented"
	closeKey20  = "issue close 20 --repo o/r --reason completed"
	verifyKey20 = "issue view 20 --repo o/r --json state"
	// graphKey20 is parent 20's `--json state,subIssues` read call key.
	graphKey20 = "issue view 20 --repo o/r --json state,subIssues"
	// commentsPage1Key20 is parent 20's first (and, in every single-page
	// test below, only) comments page.
	commentsPage1Key20 = "api repos/o/r/issues/20/comments?per_page=100&page=1"

	// allClosedGraph20 is parent 20's graph with two CLOSED sub-issues (5
	// and 9), both coherent with a lookup originating from either child.
	allClosedGraph20 = `{"state":"OPEN","subIssues":{"totalCount":2,"nodes":[{"number":5,"state":"CLOSED"},{"number":9,"state":"CLOSED"}]}}`
	// singleClosedGraph20 is parent 20's graph with exactly one CLOSED
	// sub-issue (9) -- used by single-child scenarios.
	singleClosedGraph20 = `{"state":"OPEN","subIssues":{"totalCount":1,"nodes":[{"number":9,"state":"CLOSED"}]}}`
)

// closeSequenceScript returns every scripted call reconcileParents needs to
// close parent 20 for a single closing child (9), given comments as the
// parent's one-page comment thread body.
func closeSequenceScript(comments string) map[string]ghResp {
	return map[string]ghResp{
		"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
		graphKey20:                              {stdout: singleClosedGraph20},
		commentsPage1Key20:                      {stdout: comments},
		selfHealKey:                             {stdout: `{}`},
		inReviewSelfHealKey:                     {stdout: `{}`},
		editKey20:                               {stdout: `{}`},
		closeKey20:                              {stdout: `{}`},
		verifyKey20:                             {stdout: `{"state":"CLOSED"}`},
	}
}

// -- graph gate chain (7 distinct cases) --------------------------------------

// TestReconcileParentsGraphGateChain pins the fail-closed gate chain over a
// parent's own native sub-issue graph (#811 Decision 5): every distinct
// upstream-read failure and every distinct structural-validity failure gets
// its own reason string, never a bare "something went wrong", and none of
// them ever writes to the parent.
func TestReconcileParentsGraphGateChain(t *testing.T) {
	for _, tc := range []struct {
		name          string
		script        map[string]ghResp
		wantErrSubstr string
	}{
		{
			name: "parent-read failure",
			script: map[string]ghResp{
				"issue view 9 --repo o/r --json parent": {err: errors.New("exit status 1: network unreachable")},
			},
			wantErrSubstr: reasonParentReadFailed,
		},
		{
			name: "sub-issue-read failure",
			script: map[string]ghResp{
				"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
				graphKey20:                              {err: errors.New("exit status 1: not found")},
			},
			wantErrSubstr: reasonParentGraphReadFailed,
		},
		{
			name: "malformed JSON (a sub-issue node decodes to its zero value)",
			script: map[string]ghResp{
				"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
				graphKey20:                              {stdout: `{"state":"OPEN","subIssues":{"totalCount":2,"nodes":[{},{"number":9,"state":"CLOSED"}]}}`},
			},
			wantErrSubstr: reasonParentGraphMalformed,
		},
		{
			name: "absent/nil subIssues graph",
			script: map[string]ghResp{
				"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
				graphKey20:                              {stdout: `{"state":"OPEN","subIssues":null}`},
			},
			wantErrSubstr: reasonParentGraphUnavailable,
		},
		{
			name: "empty graph (totalCount == 0)",
			script: map[string]ghResp{
				"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
				graphKey20:                              {stdout: `{"state":"OPEN","subIssues":{"totalCount":0,"nodes":[]}}`},
			},
			wantErrSubstr: reasonParentGraphEmpty,
		},
		{
			name: "truncated graph (len(nodes) != totalCount)",
			script: map[string]ghResp{
				"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
				graphKey20:                              {stdout: `{"state":"OPEN","subIssues":{"totalCount":3,"nodes":[{"number":5,"state":"CLOSED"},{"number":9,"state":"CLOSED"}]}}`},
			},
			wantErrSubstr: reasonParentGraphTruncated,
		},
		{
			name: "incoherent graph (originating child absent from nodes)",
			script: map[string]ghResp{
				"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
				graphKey20:                              {stdout: `{"state":"OPEN","subIssues":{"totalCount":2,"nodes":[{"number":5,"state":"CLOSED"},{"number":7,"state":"CLOSED"}]}}`},
			},
			wantErrSubstr: reasonParentGraphIncoherent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			ghStub(t, &calls, tc.script)
			outcomes, err := reconcileParents(repo, []int{9})
			if err == nil {
				t.Fatalf("reconcileParents: err = nil, want an error containing %q", tc.wantErrSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Fatalf("reconcileParents err = %q, want it to contain %q", err.Error(), tc.wantErrSubstr)
			}
			if got := writeCalls(calls); len(got) != 0 {
				t.Fatalf("reconcileParents issued writes on a gate failure: %#v", got)
			}
			if len(outcomes) != 0 {
				t.Fatalf("reconcileParents outcomes = %#v, want none on a gate failure", outcomes)
			}
		})
	}
}

// -- gap-report gate (4 explicit cases) ---------------------------------------

// TestReconcileParentsGapReportHoldsWithZeroWrites pins case (a): a
// non-blockquoted gap-report marker anywhere in the parent's comment thread
// holds the parent open -- zero writes, nil error, a `held` outcome.
func TestReconcileParentsGapReportHoldsWithZeroWrites(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, closeSequenceScript(commentsJSON("hello\n"+parentGapMarker+"\nmore")))
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: err = %v, want nil (a gap report is a hold, not an error)", err)
	}
	if got := writeCalls(calls); len(got) != 0 {
		t.Fatalf("reconcileParents issued writes despite a live gap-report marker: %#v", got)
	}
	if len(outcomes) != 1 || outcomes[0].Parent != 20 || outcomes[0].Kind != parentOutcomeHeld {
		t.Fatalf("outcomes = %#v, want exactly one held(20) outcome", outcomes)
	}
}

// TestReconcileParentsNoMarkerClosesParent pins case (b): no marker in any
// comment -- the label-edit-then-close write pair executes.
func TestReconcileParentsNoMarkerClosesParent(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, closeSequenceScript(commentsJSON("just an ordinary comment")))
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Parent != 20 || outcomes[0].Kind != parentOutcomeClosed {
		t.Fatalf("outcomes = %#v, want exactly one closed(20) outcome", outcomes)
	}
	editIdx := indexOfCall(calls, "gh issue edit 20")
	closeIdx := indexOfCall(calls, "gh issue close 20")
	if editIdx < 0 {
		t.Fatalf("no label-edit call for parent 20 in %#v", calls)
	}
	if closeIdx < 0 {
		t.Fatalf("no close call for parent 20 in %#v", calls)
	}
	if closeIdx < editIdx {
		t.Fatalf("close call (index %d) ran before the label edit (index %d): %#v", closeIdx, editIdx, calls)
	}
}

// TestReconcileParentsNoMarkerSelfHealsInReviewLabelBeforeEdit pins #811
// Fix 3: the "In Review" self-heal call fires before the label-swap edit,
// and its own error is ignored -- exactly mirroring the pre-existing
// "Implemented" self-heal (a repo lacking the "In Review" label entirely
// must not fail the parent close over it).
func TestReconcileParentsNoMarkerSelfHealsInReviewLabelBeforeEdit(t *testing.T) {
	var calls [][]string
	script := closeSequenceScript(commentsJSON("just an ordinary comment"))
	script[inReviewSelfHealKey] = ghResp{err: errors.New("exit status 1: label already exists")}
	ghStub(t, &calls, script)
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: err = %v, want nil: the In Review self-heal's own error must be ignored", err)
	}
	if len(outcomes) != 1 || outcomes[0].Parent != 20 || outcomes[0].Kind != parentOutcomeClosed {
		t.Fatalf("outcomes = %#v, want exactly one closed(20) outcome despite the self-heal call erroring", outcomes)
	}
	selfHealIdx := indexOfCall(calls, "gh label create In Review")
	editIdx := indexOfCall(calls, "gh issue edit 20")
	if selfHealIdx < 0 {
		t.Fatalf("no In Review label-create self-heal call in %#v", calls)
	}
	if editIdx < 0 || editIdx < selfHealIdx {
		t.Fatalf("want the In Review self-heal (index %d) strictly before the label edit (index %d): %#v", selfHealIdx, editIdx, calls)
	}
}

// TestReconcileParentsLabelEditFailureSkipsCloseAndIsNonTerminal pins #811
// Fix 1: a failure on the parent's own label-swap `issue edit` call must
// never let `issue close` run -- a close with the wrong labels still
// attached is unrecoverable, since the next tick's already-CLOSED branch
// short-circuits before ever touching labels again. The returned error must
// name the parent, be distinguishable (reasonParentLabelEditFailed) from
// every other reconcileParents reason string, and no outcome entry is
// produced for that parent.
func TestReconcileParentsLabelEditFailureSkipsCloseAndIsNonTerminal(t *testing.T) {
	var calls [][]string
	script := closeSequenceScript(commentsJSON("just an ordinary comment"))
	script[editKey20] = ghResp{err: errors.New("exit status 1: label edit failed")}
	ghStub(t, &calls, script)
	outcomes, err := reconcileParents(repo, []int{9})
	if err == nil {
		t.Fatal("reconcileParents: err = nil, want a non-terminal error when the parent's label edit fails")
	}
	if !strings.Contains(err.Error(), "20") {
		t.Fatalf("reconcileParents err = %q, want it to name parent #20", err.Error())
	}
	if !strings.Contains(err.Error(), reasonParentLabelEditFailed) {
		t.Fatalf("reconcileParents err = %q, want it to contain %q", err.Error(), reasonParentLabelEditFailed)
	}
	for _, reason := range []string{
		reasonParentReadFailed, reasonParentGraphReadFailed, reasonParentGraphUnavailable,
		reasonParentGraphEmpty, reasonParentGraphTruncated, reasonParentGraphMalformed,
		reasonParentGraphIncoherent, reasonParentCommentsUnreadable, reasonParentCommentsTruncated,
	} {
		if strings.Contains(err.Error(), reason) {
			t.Fatalf("reconcileParents err = %q, must not also read as the distinct reason %q", err.Error(), reason)
		}
	}
	if n := countCalls(calls, "gh issue close 20"); n != 0 {
		t.Fatalf("reconcileParents issued issue close 20 despite the preceding label edit failing: %#v", calls)
	}
	if len(outcomes) != 0 {
		t.Fatalf("outcomes = %#v, want none when the parent's label edit fails", outcomes)
	}
}

// TestReconcileParentsCommentsReadFailureIsDistinctError pins case (c): a
// `gh` failure on the parent's comments read is a distinct, non-terminal
// error -- distinguishable from every graph-read reason and from the
// pagination-truncation reason below -- with zero parent writes.
func TestReconcileParentsCommentsReadFailureIsDistinctError(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, map[string]ghResp{
		"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
		graphKey20:                              {stdout: singleClosedGraph20},
		commentsPage1Key20:                      {err: errors.New("exit status 1: rate limited")},
	})
	outcomes, err := reconcileParents(repo, []int{9})
	if err == nil {
		t.Fatal("reconcileParents: err = nil, want an error when the parent's comments read itself fails")
	}
	if !strings.Contains(err.Error(), reasonParentCommentsUnreadable) {
		t.Fatalf("reconcileParents err = %q, want it to contain %q", err.Error(), reasonParentCommentsUnreadable)
	}
	if strings.Contains(err.Error(), reasonParentCommentsTruncated) {
		t.Fatalf("reconcileParents err = %q, must not also read as the truncation reason", err.Error())
	}
	if got := writeCalls(calls); len(got) != 0 {
		t.Fatalf("reconcileParents issued writes despite a comments-read failure: %#v", got)
	}
	if len(outcomes) != 0 {
		t.Fatalf("outcomes = %#v, want none on a comments-read failure", outcomes)
	}
}

// TestReconcileParentsCommentsPaginationCapExhaustedIsDistinctTruncationError
// pins case (d): fetchPaged's own completeness signal reporting complete ==
// false (the page cap exhausted while every page was still full-sized) is
// its own distinct truncation error -- a marker could exist beyond the
// fetched pages, so this must never collapse into (c)'s plain
// comments-read-failure reason.
func TestReconcileParentsCommentsPaginationCapExhaustedIsDistinctTruncationError(t *testing.T) {
	var calls [][]string
	script := map[string]ghResp{
		"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
		graphKey20:                              {stdout: singleClosedGraph20},
	}
	for page := 1; page <= maxFeedbackPages; page++ {
		key := fmt.Sprintf("api repos/o/r/issues/20/comments?per_page=100&page=%d", page)
		script[key] = ghResp{stdout: fullCommentPage(page)}
	}
	ghStub(t, &calls, script)
	outcomes, err := reconcileParents(repo, []int{9})
	if err == nil {
		t.Fatal("reconcileParents: err = nil, want an error when the comments read hits the pagination cap without proving completeness")
	}
	if !strings.Contains(err.Error(), reasonParentCommentsTruncated) {
		t.Fatalf("reconcileParents err = %q, want it to contain %q", err.Error(), reasonParentCommentsTruncated)
	}
	if strings.Contains(err.Error(), reasonParentCommentsUnreadable) {
		t.Fatalf("reconcileParents err = %q, must not also read as the plain unreadable reason", err.Error())
	}
	if got := writeCalls(calls); len(got) != 0 {
		t.Fatalf("reconcileParents issued writes despite a truncated comments read: %#v", got)
	}
	if len(outcomes) != 0 {
		t.Fatalf("outcomes = %#v, want none on a truncated comments read", outcomes)
	}
}

// -- blockquote stripping (3 explicit cases) ----------------------------------

// TestReconcileParentsBlockquotedMarkerDoesNotHold pins that a gap-report
// marker appearing only on a `>`-prefixed (blockquoted) line -- e.g. a human
// quote-replying to an old gap report while discussing something else -- is
// stripped before the marker scan, so it does NOT hold: the close proceeds.
func TestReconcileParentsBlockquotedMarkerDoesNotHold(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, closeSequenceScript(commentsJSON("> "+parentGapMarker+"\nsome unrelated reply text")))
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Kind != parentOutcomeClosed {
		t.Fatalf("outcomes = %#v, want a closed(20) outcome: a blockquoted-only marker must not hold", outcomes)
	}
	if got := writeCalls(calls); len(got) == 0 {
		t.Fatal("reconcileParents issued no writes even though the only marker present was blockquoted")
	}
}

// TestReconcileParentsBareMarkerHoldsDespiteUnrelatedBlockquotedProse pins
// that a bare (non-blockquoted) marker still holds even when the same
// comment also contains unrelated blockquoted prose elsewhere in the body --
// the blockquote stripping must remove only the quoted lines, never mask a
// real marker living outside them.
func TestReconcileParentsBareMarkerHoldsDespiteUnrelatedBlockquotedProse(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, closeSequenceScript(commentsJSON("> some quoted prose about something else\n"+parentGapMarker+"\nmore text")))
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: err = %v, want nil (a hold is not an error)", err)
	}
	if len(outcomes) != 1 || outcomes[0].Kind != parentOutcomeHeld {
		t.Fatalf("outcomes = %#v, want a held(20) outcome", outcomes)
	}
	if got := writeCalls(calls); len(got) != 0 {
		t.Fatalf("reconcileParents issued writes despite a live bare marker: %#v", got)
	}
}

// TestReconcileParentsLeadingWhitespaceThenBlockquoteIsStrippedToo pins that
// a blockquote line prefixed by leading whitespace before the `>` (e.g.
// "   > ...") is stripped identically to an unindented one -- matching
// stripBlockquoteLines' `strings.TrimLeft(l, " \t")` contract.
func TestReconcileParentsLeadingWhitespaceThenBlockquoteIsStrippedToo(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, closeSequenceScript(commentsJSON("   > "+parentGapMarker+"\nsome text")))
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Kind != parentOutcomeClosed {
		t.Fatalf("outcomes = %#v, want a closed(20) outcome: a leading-whitespace blockquote must be stripped too", outcomes)
	}
	if got := writeCalls(calls); len(got) == 0 {
		t.Fatal("reconcileParents issued no writes even though the only marker present was a whitespace-indented blockquote")
	}
}

// -- read ordering + call budget ----------------------------------------------

// TestReconcileParentsDedupesSharedParentAcrossTwoChildren pins the ordered
// parent dedup (#811 Decision 3/4): two closing children sharing one parent
// must still produce exactly one parent-graph read and exactly one comments
// read, with the label-edit call strictly before the close call.
func TestReconcileParentsDedupesSharedParentAcrossTwoChildren(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, map[string]ghResp{
		"issue view 5 --repo o/r --json parent": {stdout: `{"parent":{"number":20}}`},
		"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
		graphKey20:                              {stdout: allClosedGraph20},
		commentsPage1Key20:                      {stdout: commentsJSON("no marker here")},
		selfHealKey:                             {stdout: `{}`},
		inReviewSelfHealKey:                     {stdout: `{}`},
		editKey20:                               {stdout: `{}`},
		closeKey20:                              {stdout: `{}`},
		verifyKey20:                             {stdout: `{"state":"CLOSED"}`},
	})
	outcomes, err := reconcileParents(repo, []int{5, 9})
	if err != nil {
		t.Fatalf("reconcileParents: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Parent != 20 || outcomes[0].Kind != parentOutcomeClosed {
		t.Fatalf("outcomes = %#v, want exactly one closed(20) outcome", outcomes)
	}
	if n := countCalls(calls, graphKey20); n != 1 {
		t.Fatalf("parent-graph reads = %d, want exactly 1: %#v", n, calls)
	}
	if n := countCalls(calls, "api repos/o/r/issues/20/comments"); n != 1 {
		t.Fatalf("comments reads = %d, want exactly 1: %#v", n, calls)
	}
	editIdx := indexOfCall(calls, "gh issue edit 20")
	closeIdx := indexOfCall(calls, "gh issue close 20")
	if editIdx < 0 || closeIdx < 0 || closeIdx < editIdx {
		t.Fatalf("want the label edit strictly before the close, got edit=%d close=%d: %#v", editIdx, closeIdx, calls)
	}
}

// TestReconcileParentsAlreadyClosedParentSkipsCommentsAndWrites pins #811
// Decision 7: a parent already CLOSED at graph-read time is an idempotent
// success determined before the comments read ever runs -- zero comment
// reads, zero writes, no error.
func TestReconcileParentsAlreadyClosedParentSkipsCommentsAndWrites(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, map[string]ghResp{
		"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
		// subIssues is deliberately null here: if the implementation ran the
		// subIssues gate chain before checking state, a nil-subIssues graph
		// would incorrectly fail closed instead of short-circuiting as
		// already-closed.
		graphKey20: {stdout: `{"state":"CLOSED","subIssues":null}`},
	})
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: err = %v, want nil for an already-CLOSED parent", err)
	}
	if len(outcomes) != 1 || outcomes[0].Parent != 20 || outcomes[0].Kind != parentOutcomeAlreadyClosed {
		t.Fatalf("outcomes = %#v, want exactly one already-closed(20) outcome", outcomes)
	}
	if got := writeCalls(calls); len(got) != 0 {
		t.Fatalf("reconcileParents issued writes for an already-CLOSED parent: %#v", got)
	}
	if n := countCalls(calls, "api repos/o/r/issues/20/comments"); n != 0 {
		t.Fatalf("comments reads = %d, want 0 for an already-CLOSED parent: %#v", n, calls)
	}
}

// TestReconcileParentsOpenSiblingSkipsCommentsRead pins that the sub-issue
// graph gate short-circuits on the very first non-CLOSED sibling: the
// comments read (an extra API call) must never run while any sibling is
// still open, and the hold is not an error.
func TestReconcileParentsOpenSiblingSkipsCommentsRead(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, map[string]ghResp{
		"issue view 9 --repo o/r --json parent": {stdout: parentReadFor9},
		graphKey20:                              {stdout: `{"state":"OPEN","subIssues":{"totalCount":2,"nodes":[{"number":5,"state":"OPEN"},{"number":9,"state":"CLOSED"}]}}`},
	})
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: err = %v, want nil: an open sibling is a hold, not an error", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("outcomes = %#v, want none while a sibling is still open", outcomes)
	}
	// The graph read itself must actually have happened -- otherwise this
	// test would pass trivially against a no-op stub that never even reaches
	// the sibling-open gate at all.
	if n := countCalls(calls, graphKey20); n != 1 {
		t.Fatalf("parent-graph reads = %d, want exactly 1 (the gate must actually run): %#v", n, calls)
	}
	if n := countCalls(calls, "api repos/o/r/issues/20/comments"); n != 0 {
		t.Fatalf("comments reads = %d, want 0 while a sibling is still open: %#v", n, calls)
	}
	if got := writeCalls(calls); len(got) != 0 {
		t.Fatalf("reconcileParents issued writes while a sibling is still open: %#v", got)
	}
}

// -- idempotency ---------------------------------------------------------------

// TestReconcileParentsCloseFailureConfirmedByVerificationReadIsIdempotent
// pins that a `gh issue close` command failure is not itself fatal: the
// mandatory single post-close `--json state` verification read reporting
// CLOSED is authoritative, exactly mirroring executeMerge's own
// verification-over-exit-code contract (merge.go).
func TestReconcileParentsCloseFailureConfirmedByVerificationReadIsIdempotent(t *testing.T) {
	var calls [][]string
	script := closeSequenceScript(commentsJSON("no marker here"))
	script[closeKey20] = ghResp{err: errors.New("exit status 1: transient network blip")}
	ghStub(t, &calls, script)
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: err = %v, want nil: the post-close verification read reporting CLOSED must win over the close command's own failure", err)
	}
	if len(outcomes) != 1 || outcomes[0].Parent != 20 || outcomes[0].Kind != parentOutcomeClosed {
		t.Fatalf("outcomes = %#v, want exactly one closed(20) outcome", outcomes)
	}
}

// TestReconcileParentsNoParentStopsAfterLabelTransition pins #811 Decision 2:
// a closing issue with no native parent relationship makes reconcileParents
// stop after the read that discovered that -- no further reads, no writes,
// no error, no outcome entry.
func TestReconcileParentsNoParentStopsAfterLabelTransition(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, map[string]ghResp{
		"issue view 9 --repo o/r --json parent": {stdout: `{"parent":null}`},
	})
	outcomes, err := reconcileParents(repo, []int{9})
	if err != nil {
		t.Fatalf("reconcileParents: %v", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("outcomes = %#v, want none when the closing issue has no parent", outcomes)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want exactly the one parent-relationship read and nothing else", calls)
	}
}

// TestReconcileParentsAcrossTwoTicksNeverReopensOrRemovesImplementedLabel
// pins that two independent reconcileParents calls -- simulating two
// separate merged-PR ticks that both reach the same parent, the second
// finding it already closed by the first -- never issue an `issue reopen`
// and never strip the `Implemented` label back off: reconciliation is
// monotonic, closed stays closed.
func TestReconcileParentsAcrossTwoTicksNeverReopensOrRemovesImplementedLabel(t *testing.T) {
	var calls1 [][]string
	ghStub(t, &calls1, closeSequenceScript(commentsJSON("no marker here")))
	outcomes1, err1 := reconcileParents(repo, []int{9})
	if err1 != nil {
		t.Fatalf("first tick reconcileParents: %v", err1)
	}
	if len(outcomes1) != 1 || outcomes1[0].Kind != parentOutcomeClosed {
		t.Fatalf("first tick outcomes = %#v, want a closed(20) outcome", outcomes1)
	}

	var calls2 [][]string
	ghStub(t, &calls2, map[string]ghResp{
		"issue view 5 --repo o/r --json parent": {stdout: `{"parent":{"number":20}}`},
		graphKey20:                              {stdout: `{"state":"CLOSED"}`},
	})
	outcomes2, err2 := reconcileParents(repo, []int{5})
	if err2 != nil {
		t.Fatalf("second tick reconcileParents: %v", err2)
	}
	if len(outcomes2) != 1 || outcomes2[0].Kind != parentOutcomeAlreadyClosed {
		t.Fatalf("second tick outcomes = %#v, want an already-closed(20) outcome", outcomes2)
	}
	if got := writeCalls(calls2); len(got) != 0 {
		t.Fatalf("second tick issued writes for an already-closed parent: %#v", got)
	}

	for _, c := range append(append([][]string{}, calls1...), calls2...) {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "reopen") {
			t.Fatalf("call log contains an issue reopen, which must never happen: %#v", c)
		}
		if strings.Contains(joined, "--remove-label Implemented") {
			t.Fatalf("call log removes the Implemented label, which must never happen: %#v", c)
		}
	}
}

// -- error co-occurrence (watch/docs/error-handling.md #927) -----------------

// TestReconcileParentsJoinsIndependentChildAndParentFailures pins #811
// Decision 8 (errors.Join, never a first-error early return): two closing
// children hitting two entirely independent failures -- one child's own
// parent-relationship read failing, a different child's resolved parent's
// graph read failing -- must both stay discoverable in the single returned
// error, naming both the failing child/parent and the failed operation for
// each (#927: testing each failure in isolation would pass even if a
// first-error picker silently dropped one, only a co-occurrence test catches
// that regression).
func TestReconcileParentsJoinsIndependentChildAndParentFailures(t *testing.T) {
	var calls [][]string
	ghStub(t, &calls, map[string]ghResp{
		"issue view 9 --repo o/r --json parent":           {err: errors.New("exit status 1: boom-9")},
		"issue view 15 --repo o/r --json parent":          {stdout: `{"parent":{"number":30}}`},
		"issue view 30 --repo o/r --json state,subIssues": {err: errors.New("exit status 1: boom-30")},
	})
	outcomes, err := reconcileParents(repo, []int{9, 15})
	if err == nil {
		t.Fatal("reconcileParents: err = nil, want a joined error naming both independent failures")
	}
	msg := err.Error()
	if !strings.Contains(msg, "9") || !strings.Contains(msg, reasonParentReadFailed) {
		t.Fatalf("reconcileParents err = %q, want it to name child #9 and %q", msg, reasonParentReadFailed)
	}
	if !strings.Contains(msg, "30") || !strings.Contains(msg, reasonParentGraphReadFailed) {
		t.Fatalf("reconcileParents err = %q, want it to also name parent #30 and %q", msg, reasonParentGraphReadFailed)
	}
	if got := writeCalls(calls); len(got) != 0 {
		t.Fatalf("reconcileParents issued writes despite two upstream read failures: %#v", got)
	}
	if len(outcomes) != 0 {
		t.Fatalf("outcomes = %#v, want none when both parents' resolution failed", outcomes)
	}
}
