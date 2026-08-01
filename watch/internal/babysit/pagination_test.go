package babysit

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// -- fetchPaged (#854) ---------------------------------------------------------
//
// fetchPaged is the one piece of new generic I/O this ticket introduces for
// the comments/reviews detection reads -- an explicit per_page=100&page=N
// loop over the execGh seam, mirroring fetchReviewThreads' cursor-loop
// discipline (feedback.go:274-328) but for REST pagination instead of
// GraphQL cursors.

type pageItem struct {
	ID int `json:"id"`
}

// withScriptedExecGh replaces the package's execGh seam for the test's
// duration with one that serves bodies (as stdout, "" stderr, nil error) in
// order for every invocation, recording every call's args into calls.
func withScriptedExecGh(t *testing.T, bodies []string, calls *[][]string) {
	t.Helper()
	original := execGh
	i := 0
	execGh = func(args ...string) (string, string, error) {
		*calls = append(*calls, args)
		if i >= len(bodies) {
			return "", "", fmt.Errorf("unexpected call: %s", strings.Join(args, " "))
		}
		out := bodies[i]
		i++
		return out, "", nil
	}
	t.Cleanup(func() { execGh = original })
}

// page renders a JSON array of {"id": n} objects for the given ids.
func page(ids ...int) string {
	var b strings.Builder
	b.WriteString("[")
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d}`, id)
	}
	b.WriteString("]")
	return b.String()
}

// fullPage renders a JSON array of exactly feedbackPageSize {"id": n}
// objects, ids starting at startID.
func fullPage(startID int) string {
	ids := make([]int, feedbackPageSize)
	for i := range ids {
		ids[i] = startID + i
	}
	return page(ids...)
}

// TestFetchPagedAssemblesMultiplePages pins the multi-page assembly case: a
// full first page followed by a short second page must merge into one items
// slice across exactly two gh calls, with complete=true once the short page
// terminates the traversal.
func TestFetchPagedAssemblesMultiplePages(t *testing.T) {
	var calls [][]string
	withScriptedExecGh(t, []string{fullPage(1), page(1000, 1001)}, &calls)

	items, complete, err := fetchPaged[pageItem]("repos/o/r/pulls/1/comments")
	if err != nil {
		t.Fatalf("fetchPaged: %v", err)
	}
	if !complete {
		t.Fatal("complete = false, want true: the second page came back short, terminating the traversal")
	}
	if len(items) != feedbackPageSize+2 {
		t.Fatalf("len(items) = %d, want %d (both pages assembled)", len(items), feedbackPageSize+2)
	}
	if len(calls) != 2 {
		t.Fatalf("gh calls = %d, want exactly 2 (one per page)", len(calls))
	}
}

// TestFetchPagedShortPageTerminatesTraversal pins the common case: a single
// page shorter than feedbackPageSize is itself proof of completeness, no
// second request is ever attempted.
func TestFetchPagedShortPageTerminatesTraversal(t *testing.T) {
	var calls [][]string
	withScriptedExecGh(t, []string{page(1, 2, 3)}, &calls)

	items, complete, err := fetchPaged[pageItem]("repos/o/r/pulls/1/comments")
	if err != nil {
		t.Fatalf("fetchPaged: %v", err)
	}
	if !complete {
		t.Fatal("complete = false, want true: a single short page is a complete read")
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	if len(calls) != 1 {
		t.Fatalf("gh calls = %d, want exactly 1: a short first page must not trigger a second request", len(calls))
	}
}

// TestFetchPagedCapExhaustionFailsClosed pins the cap-exhaustion contract: a
// fetch still returning full-sized pages after maxFeedbackPages pages
// reports complete=false (not an error) -- the caller decides how to react
// to an unproven-complete read, but the signal itself must never silently
// collapse to "complete".
func TestFetchPagedCapExhaustionFailsClosed(t *testing.T) {
	bodies := make([]string, maxFeedbackPages)
	for i := range bodies {
		bodies[i] = fullPage(i * feedbackPageSize)
	}
	var calls [][]string
	withScriptedExecGh(t, bodies, &calls)

	items, complete, err := fetchPaged[pageItem]("repos/o/r/pulls/1/comments")
	if err != nil {
		t.Fatalf("fetchPaged: unexpected error %v (cap exhaustion is complete=false, not an error)", err)
	}
	if complete {
		t.Fatal("complete = true, want false: every page up to the cap came back full-sized, so more items may exist beyond it")
	}
	if len(items) != maxFeedbackPages*feedbackPageSize {
		t.Fatalf("len(items) = %d, want %d (every fetched page still assembled even though incomplete)", len(items), maxFeedbackPages*feedbackPageSize)
	}
	if len(calls) != maxFeedbackPages {
		t.Fatalf("gh calls = %d, want exactly maxFeedbackPages (%d): no (cap+1)-th page may be attempted", len(calls), maxFeedbackPages)
	}
}

// TestFetchPagedMidPaginationFailureIsError pins the #850-style guarantee
// applied to REST pagination: a gh failure partway through a multi-page
// traversal must never be treated as a complete-but-partial result -- it is
// a distinct error (errPaginationFailed), and no partial items are returned
// at all.
func TestFetchPagedMidPaginationFailureIsError(t *testing.T) {
	var calls [][]string
	original := execGh
	i := 0
	bodies := []string{fullPage(1)}
	execGh = func(args ...string) (string, string, error) {
		calls = append(calls, args)
		if i < len(bodies) {
			out := bodies[i]
			i++
			return out, "", nil
		}
		return "", "rate limited", errors.New("exit status 1")
	}
	t.Cleanup(func() { execGh = original })

	items, complete, err := fetchPaged[pageItem]("repos/o/r/pulls/1/comments")
	if err == nil {
		t.Fatal("fetchPaged: err = nil, want an error: a mid-pagination gh failure must never be treated as a complete-but-partial result")
	}
	if !errors.Is(err, errPaginationFailed) {
		t.Fatalf("fetchPaged err = %v, want errors.Is(err, errPaginationFailed)", err)
	}
	if items != nil {
		t.Fatalf("items = %v, want nil: a mid-pagination failure must not return partial items as if they were usable", items)
	}
	if complete {
		t.Fatal("complete = true, want false on a mid-pagination failure")
	}
	if len(calls) != 2 {
		t.Fatalf("gh calls = %d, want exactly 2 (the successful first page, then the failing second page)", len(calls))
	}
}

// TestFetchPagedNonJSONPageIsError pins that a non-JSON page body -- unlike
// ghJSON's now-removed "valid JSON on non-zero exit" tolerance (#886) -- is
// always an error for fetchPaged; strict, no exit-code tolerance inherited.
func TestFetchPagedNonJSONPageIsError(t *testing.T) {
	var calls [][]string
	withScriptedExecGh(t, []string{"not json"}, &calls)

	_, complete, err := fetchPaged[pageItem]("repos/o/r/pulls/1/comments")
	if err == nil {
		t.Fatal("fetchPaged: err = nil, want an error on a non-JSON page body")
	}
	if complete {
		t.Fatal("complete = true, want false on a non-JSON page body")
	}
	if len(calls) != 1 {
		t.Fatalf("gh calls = %d, want exactly 1 (the single failing page request)", len(calls))
	}
}

// TestFetchPagedRequestsExpectedPageParameters pins the exact request shape
// (Assumption: explicit per_page=100&page=N, never `gh api --paginate`):
// each page request must carry the path plus per_page/page query
// parameters, with page incrementing across calls.
func TestFetchPagedRequestsExpectedPageParameters(t *testing.T) {
	var calls [][]string
	withScriptedExecGh(t, []string{fullPage(1), page(999)}, &calls)

	if _, _, err := fetchPaged[pageItem]("repos/o/r/pulls/1/comments"); err != nil {
		t.Fatalf("fetchPaged: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("gh calls = %d, want exactly 2", len(calls))
	}
	call1 := strings.Join(calls[0], " ")
	call2 := strings.Join(calls[1], " ")
	if !strings.Contains(call1, "per_page=100") || !strings.Contains(call1, "page=1") {
		t.Errorf("first call = %q, want it to request per_page=100&page=1", call1)
	}
	if !strings.Contains(call2, "per_page=100") || !strings.Contains(call2, "page=2") {
		t.Errorf("second call = %q, want it to request per_page=100&page=2", call2)
	}
	if strings.Contains(call1, "--paginate") || strings.Contains(call2, "--paginate") {
		t.Errorf("calls must never use gh api --paginate (hides the completeness signal): %q / %q", call1, call2)
	}
}
