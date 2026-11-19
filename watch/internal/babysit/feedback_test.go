package babysit

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// -- pure classifier (#850) ---------------------------------------------------
//
// classifyKey / latestEffectiveReview / partitionPending are pure,
// I/O-free functions (mirroring evaluateAutomerge's discipline,
// automerge.go:161-166) so the multi-reviewer / multi-thread / mixed-state
// matrix is testable without any `gh` scripting.

// login builds the anonymous User field review embeds, for concise fixture
// construction throughout this file.
func login(name string) struct{ Login string } {
	return struct{ Login string }{Login: name}
}

// TestClassifyPendingKeyMatrix pins each of the nine key/thread/review shapes
// from the plan's Test Strategy table to its exact keyStatus -- per
// watch/docs/error-handling.md #446 the assertion is the exact status, never
// a bare "held"/"not resolved" check, so a regression collapsing two failure
// classes (e.g. absent-thread and unresolved-thread) into the same status is
// caught.
func TestClassifyPendingKeyMatrix(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		fs   feedbackState
		want keyStatus
	}{
		{
			"resolved-thread",
			"comment:5",
			feedbackState{Threads: map[int64]threadState{5: resolvedThread("alice", "alice")}, Actors: okActors(nil)},
			keyResolved,
		},
		{
			"unresolved-thread",
			"comment:5",
			feedbackState{Threads: map[int64]threadState{5: {}}},
			keyPending,
		},
		{
			"absent-thread: GitHub no longer reports this thread at all (deleted comment, force-push purge) -- never treated as resolved",
			"comment:5",
			feedbackState{Threads: map[int64]threadState{6: resolvedThread("alice", "alice")}},
			keyUnknown,
		},
		{
			"dismissed-review: GitHub rewrites a dismissed review's own state to DISMISSED in place",
			"review:10",
			feedbackState{Reviews: []review{{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}}, ReviewsComplete: true, Actors: okActors(map[int64]string{10: "alice"})},
			keyResolved,
		},
		{
			"reviewer-later-approved: same reviewer's newer review supersedes the blocking one",
			"review:10",
			feedbackState{Reviews: []review{
				{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
				{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
			}, ReviewsComplete: true, Actors: okActors(nil)},
			keyResolved,
		},
		{
			"reviewer-later-changes-requested: still blocking, no supersession",
			"review:10",
			feedbackState{Reviews: []review{
				{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
				{ID: 12, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
			}, ReviewsComplete: true},
			keyPending,
		},
		{
			"absent-review: GitHub no longer reports this review ID at all",
			"review:10",
			feedbackState{Reviews: []review{{ID: 55, State: "APPROVED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("bob")}}, ReviewsComplete: true},
			keyUnknown,
		},
		{
			"unknown-state-string: a review state outside {APPROVED, CHANGES_REQUESTED, DISMISSED, COMMENTED, PENDING}",
			"review:10",
			feedbackState{Reviews: []review{{ID: 10, State: "REQUEST_CHANGES", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")}}, ReviewsComplete: true},
			keyUnknown,
		},
		{
			"unsupported-prefix: a key type this classifier does not recognize at all",
			"label:1",
			feedbackState{},
			keyUnsupported,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyKey(tc.key, tc.fs).Status
			if got != tc.want {
				t.Fatalf("classifyKey(%q).Status = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestClassifyReviewKeyFailsClosedWhenReviewsListPotentiallyTruncated pins
// the #850 security-review fix: babysit.go's reviews fetch (`gh api
// repos/.../pulls/N/reviews`) does not paginate (full pagination is #854's
// scope), so a reviews slice at GitHub's default per-page size (30) can
// never be proven complete. Without this guard, a stale APPROVED visible in
// the fetched page would be misread by latestEffectiveReview as the
// reviewer's latest word, silently resolving a review: key even though a
// genuinely newer, still-blocking CHANGES_REQUESTED could be sitting on an
// unfetched page.
func TestClassifyReviewKeyFailsClosedWhenReviewsListPotentiallyTruncated(t *testing.T) {
	reviews := make([]review, reviewsPageSize)
	// The pending key's own review: still CHANGES_REQUESTED.
	reviews[0] = review{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")}
	// A "stale" APPROVED by the same reviewer, visible within the fetched
	// page and later than the CHANGES_REQUESTED above -- without the
	// reviewsPageSize guard, latestEffectiveReview would pick this as
	// alice's latest word and classifyReviewKey would resolve the key.
	reviews[1] = review{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}
	for i := 2; i < reviewsPageSize; i++ {
		reviews[i] = review{ID: int64(1000 + i), State: "APPROVED", SubmittedAt: "2026-01-01T00:00:00Z", User: login(fmt.Sprintf("filler%d", i))}
	}
	if len(reviews) != reviewsPageSize {
		t.Fatalf("test setup: len(reviews) = %d, want exactly reviewsPageSize (%d)", len(reviews), reviewsPageSize)
	}

	fs := feedbackState{Reviews: reviews}
	got := classifyKey("review:10", fs).Status
	if got != keyUnknown {
		t.Fatalf("classifyKey(review:10).Status = %v, want keyUnknown: a reviews list at the page-size cap can never be trusted as complete, regardless of what the visible data suggests", got)
	}

	c := partitionPending([]string{"review:10"}, fs)
	resolved, hold := c.Resolved, c.Hold
	if len(resolved) != 0 {
		t.Fatalf("resolved = %v, want empty: the possibly-truncated list must not resolve review:10", resolved)
	}
	if hold != reasonReviewStateUnknown {
		t.Fatalf("hold = %q, want %q", hold, reasonReviewStateUnknown)
	}
}

// TestClassifyReviewKeyResolvesNormallyBelowPageSize is the regression
// counterpart: a reviews slice below reviewsPageSize that is proven complete
// resolves exactly as before.
func TestClassifyReviewKeyResolvesNormallyBelowPageSize(t *testing.T) {
	reviews := []review{
		{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
		{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
	}
	if len(reviews) >= reviewsPageSize {
		t.Fatalf("test setup: len(reviews) = %d, want below reviewsPageSize (%d)", len(reviews), reviewsPageSize)
	}
	got := classifyKey("review:10", feedbackState{Reviews: reviews, ReviewsComplete: true, Actors: okActors(nil)}).Status
	if got != keyResolved {
		t.Fatalf("classifyKey(review:10).Status = %v, want keyResolved: below the page-size cap and proven complete, the reviewer's later APPROVED still supersedes normally", got)
	}
}

// TestClassifyReviewKeyResolvesAtPageSizeWhenComplete pins the #854 fix
// replacing the reviewsPageSize length tripwire: a reviews slice at exactly
// reviewsPageSize length that IS proven complete (ReviewsComplete: true)
// must resolve normally -- the previous len(fs.Reviews) >= reviewsPageSize
// guard alone fails closed on any PR with exactly reviewsPageSize reviews,
// regardless of whether the read is actually complete.
func TestClassifyReviewKeyResolvesAtPageSizeWhenComplete(t *testing.T) {
	reviews := make([]review, reviewsPageSize)
	reviews[0] = review{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")}
	for i := 1; i < reviewsPageSize; i++ {
		reviews[i] = review{ID: int64(1000 + i), State: "APPROVED", SubmittedAt: "2026-01-01T00:00:00Z", User: login(fmt.Sprintf("filler%d", i))}
	}
	if len(reviews) != reviewsPageSize {
		t.Fatalf("test setup: len(reviews) = %d, want exactly reviewsPageSize (%d)", len(reviews), reviewsPageSize)
	}
	fs := feedbackState{Reviews: reviews, ReviewsComplete: true, Actors: okActors(map[int64]string{10: "alice"})}
	got := classifyKey("review:10", fs).Status
	if got != keyResolved {
		t.Fatalf("classifyKey(review:10).Status = %v, want keyResolved: a proven-complete reviews list at exactly reviewsPageSize must resolve normally, not fail closed on length alone", got)
	}
}

// TestClassifyReviewKeyFailsClosedWhenIncomplete pins the fail-closed half
// of the same fix: a SHORT reviews slice (well under reviewsPageSize) that
// is explicitly marked incomplete (ReviewsComplete: false) must still fail
// closed -- the truncation signal, not the length heuristic, is now the
// source of truth for completeness.
func TestClassifyReviewKeyFailsClosedWhenIncomplete(t *testing.T) {
	reviews := []review{
		{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
		{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
	}
	if len(reviews) >= reviewsPageSize {
		t.Fatalf("test setup: len(reviews) = %d, want well under reviewsPageSize (%d)", len(reviews), reviewsPageSize)
	}
	fs := feedbackState{Reviews: reviews, ReviewsComplete: false}
	got := classifyKey("review:10", fs).Status
	if got != keyUnknown {
		t.Fatalf("classifyKey(review:10).Status = %v, want keyUnknown: an explicitly-incomplete reviews read must fail closed even well under reviewsPageSize", got)
	}
}

// TestLatestEffectiveReviewIgnoresCommented pins that a COMMENTED review
// submitted after a CHANGES_REQUESTED one does not count as supersession --
// latestEffectiveReview must return the still-blocking CHANGES_REQUESTED
// review, not the later COMMENTED one, and not report "no effective review".
func TestLatestEffectiveReviewIgnoresCommented(t *testing.T) {
	reviews := []review{
		{ID: 1, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
		{ID: 2, State: "COMMENTED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
	}
	got, ok := latestEffectiveReview(reviews, "alice")
	if !ok {
		t.Fatal("latestEffectiveReview: ok = false, want true (the CHANGES_REQUESTED review still qualifies)")
	}
	if got.ID != 1 || got.State != "CHANGES_REQUESTED" {
		t.Fatalf("latestEffectiveReview = %+v, want the CHANGES_REQUESTED review (ID 1), not the later COMMENTED one", got)
	}
}

// TestPartitionPendingKeysMixedState is the AC: multiple reviewers, multiple
// threads, mixed resolved/unresolved state. Resolved keys move out of
// stillPending into resolved even when a separate unknown key is also
// present, and that single unknown key still poisons the overall hold verdict
// -- a pure-function outcome unreachable through tick scripting alone.
func TestPartitionPendingKeysMixedState(t *testing.T) {
	pending := []string{"comment:1", "comment:2", "review:10", "review:11", "review:99"}
	fs := feedbackState{
		Threads: map[int64]threadState{1: resolvedThread("alice", "alice"), 2: {}},
		Reviews: []review{
			{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("bob")},
			{ID: 11, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("carol")},
		},
		ReviewsComplete: true,
		Actors:          okActors(map[int64]string{10: "bob"}),
	}
	c := partitionPending(pending, fs)
	stillPending, resolved, hold, detail := c.Pending, c.Resolved, c.Hold, c.Detail

	wantResolved := []string{"comment:1", "review:10"}
	if !reflect.DeepEqual(resolved, wantResolved) {
		t.Errorf("resolved = %v, want %v", resolved, wantResolved)
	}
	wantStillPending := []string{"comment:2", "review:11", "review:99"}
	if !reflect.DeepEqual(stillPending, wantStillPending) {
		t.Errorf("stillPending = %v, want %v", stillPending, wantStillPending)
	}
	if hold != reasonReviewStateUnknown {
		t.Errorf("hold = %q, want %q (review:99 is absent from fs.Reviews)", hold, reasonReviewStateUnknown)
	}
	if !strings.Contains(detail, "review:99") {
		t.Errorf("detail = %q, want it to identify the offending key (review:99)", detail)
	}
}

// TestPartitionPendingKeysUnsupportedKeyPoisonsVerdict pins the fourth
// classification outcome (unsupported prefix) as its own distinct hold
// reason, never collapsed into reasonReviewStateUnknown.
func TestPartitionPendingKeysUnsupportedKeyPoisonsVerdict(t *testing.T) {
	c := partitionPending([]string{"label:1"}, feedbackState{})
	stillPending, resolved, hold, detail := c.Pending, c.Resolved, c.Hold, c.Detail
	if len(resolved) != 0 {
		t.Errorf("resolved = %v, want empty", resolved)
	}
	if !reflect.DeepEqual(stillPending, []string{"label:1"}) {
		t.Errorf("stillPending = %v, want [label:1]", stillPending)
	}
	if hold != reasonFeedbackUnsupported {
		t.Errorf("hold = %q, want %q", hold, reasonFeedbackUnsupported)
	}
	if !strings.Contains(detail, "label:1") {
		t.Errorf("detail = %q, want it to identify the offending key (label:1)", detail)
	}
}

// -- fetchReviewThreads (#850) ------------------------------------------------
//
// fetchReviewThreads is the one piece of new I/O this ticket introduces: a
// `gh api graphql` call over the existing `command` seam (no new test seam,
// per watch/AGENTS.md's existing single-seam convention), with an explicit
// cursor loop bounded by maxThreadPages.

// scriptedGraphQLCommand stubs the execGh seam to serve bodies in order for
// every invocation (always a nil error -- a non-nil `gh` exit combined with a
// still-parseable body is exercised separately, mirroring ghJSON's tolerant
// contract; these tests focus on the GraphQL-specific failure shapes), and
// records every call into calls.
func scriptedGraphQLCommand(t *testing.T, bodies []string, calls *[][]string) {
	t.Helper()
	original := execGh
	i := 0
	execGh = func(args ...string) (string, string, error) {
		*calls = append(*calls, append([]string{"gh"}, args...))
		if i >= len(bodies) {
			t.Fatalf("unexpected extra command call: %v", args)
		}
		out := bodies[i]
		i++
		return out, "", nil
	}
	t.Cleanup(func() { execGh = original })
}

func threadPage(hasNextPage bool, endCursor string, nodes ...string) string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":%v,"endCursor":%q},"nodes":[%s]}}}}}`,
		hasNextPage, endCursor, strings.Join(nodes, ","))
}

func threadNode(resolved bool, totalCount int, ids ...int64) string {
	var idParts []string
	for _, id := range ids {
		idParts = append(idParts, fmt.Sprintf(`{"databaseId":%d}`, id))
	}
	return fmt.Sprintf(`{"isResolved":%v,"comments":{"totalCount":%d,"nodes":[%s]}}`, resolved, totalCount, strings.Join(idParts, ","))
}

// TestFetchReviewThreadsTraversesEveryPage pins the cursor loop: it follows
// pageInfo.hasNextPage/endCursor across pages and merges every page's
// comment-ID -> isResolved entries into one map, threading the previous
// page's endCursor into the next request.
func TestFetchReviewThreadsTraversesEveryPage(t *testing.T) {
	page1 := threadPage(true, "CURSOR1", threadNode(false, 1, 100))
	page2 := threadPage(false, "", threadNode(true, 1, 200))
	var calls [][]string
	scriptedGraphQLCommand(t, []string{page1, page2}, &calls)

	got, err := fetchReviewThreads("o/r", "42")
	if err != nil {
		t.Fatalf("fetchReviewThreads: %v", err)
	}
	want := map[int64]threadState{100: {Resolved: false}, 200: {Resolved: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fetchReviewThreads = %v, want %v (merged across both pages)", got, want)
	}
	if len(calls) != 2 {
		t.Fatalf("gh calls = %d, want exactly 2 (one per page)", len(calls))
	}
	if strings.Contains(strings.Join(calls[0], " "), "cursor=") {
		t.Errorf("first call = %v, want no cursor= argument at all on the first page (an explicit empty string would send GraphQL an empty string instead of the null after:$cursor needs)", calls[0])
	}
	if !strings.Contains(strings.Join(calls[1], " "), "CURSOR1") {
		t.Errorf("second call = %v, want it to carry the first page's endCursor (CURSOR1)", calls[1])
	}
}

// TestFetchReviewThreadsIncompleteTraversalFailsClosed pins that neither an
// exhausted page cap nor a per-thread comment-count mismatch is ever treated
// as a complete-but-partial result -- both fail closed via the same
// errFeedbackTruncated sentinel (watch/docs/error-handling.md #412: a direct,
// errors.Is-based assertion at the package boundary).
func TestFetchReviewThreadsIncompleteTraversalFailsClosed(t *testing.T) {
	t.Run("hasNextPage still true at the page cap", func(t *testing.T) {
		bodies := make([]string, maxThreadPages)
		for i := range bodies {
			bodies[i] = threadPage(true, fmt.Sprintf("C%d", i), threadNode(false, 1, int64(i)))
		}
		var calls [][]string
		scriptedGraphQLCommand(t, bodies, &calls)

		_, err := fetchReviewThreads("o/r", "42")
		if err == nil {
			t.Fatal("fetchReviewThreads: err = nil, want a truncation error when hasNextPage is still true at the page cap")
		}
		if !errors.Is(err, errFeedbackTruncated) {
			t.Fatalf("fetchReviewThreads err = %v, want errors.Is(err, errFeedbackTruncated)", err)
		}
		if len(calls) != maxThreadPages {
			t.Fatalf("gh calls = %d, want exactly maxThreadPages (%d): no (maxThreadPages+1)-th call may be attempted", len(calls), maxThreadPages)
		}
	})

	t.Run("comments.totalCount exceeds the fetched node count", func(t *testing.T) {
		page := threadPage(false, "", threadNode(false, 2, 300)) // totalCount 2, only 1 node
		var calls [][]string
		scriptedGraphQLCommand(t, []string{page}, &calls)

		_, err := fetchReviewThreads("o/r", "42")
		if err == nil {
			t.Fatal("fetchReviewThreads: err = nil, want a truncation error on a per-thread comment-count mismatch")
		}
		if !errors.Is(err, errFeedbackTruncated) {
			t.Fatalf("fetchReviewThreads err = %v, want errors.Is(err, errFeedbackTruncated)", err)
		}
	})
}

// TestFetchReviewThreadsFailsClosedOnGraphQLErrors pins the #822 trap in a
// new guise: `gh api graphql` can return HTTP 200 with a non-empty top-level
// errors[] alongside partial/zero-value data, which a naive decode-only check
// would treat as empty-but-valid. That must be a distinct failure from
// truncation, and malformed JSON must fail the same way.
func TestFetchReviewThreadsFailsClosedOnGraphQLErrors(t *testing.T) {
	t.Run("HTTP 200 with a non-empty errors[] envelope", func(t *testing.T) {
		// Valid JSON: "data" (zero-value/empty) is a sibling of a non-empty
		// top-level "errors", exactly the shape gh api graphql actually
		// returns on a field error. This must exercise the len(resp.Errors)
		// > 0 branch specifically -- not the malformed-JSON branch below --
		// so the exact wrapped message (not a raw-body substring that would
		// also appear if the JSON were merely malformed) is asserted.
		body := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}},"errors":[{"message":"Could not resolve to a PullRequest with the number 42."}]}`
		var calls [][]string
		scriptedGraphQLCommand(t, []string{body}, &calls)

		_, err := fetchReviewThreads("o/r", "42")
		if err == nil {
			t.Fatal("fetchReviewThreads: err = nil, want an error: a 200 response with errors[] is not empty-but-valid data")
		}
		if errors.Is(err, errFeedbackTruncated) {
			t.Fatalf("fetchReviewThreads err = %v, want a distinct class from errFeedbackTruncated", err)
		}
		wantErr := reasonFeedbackUnreadable + ": Could not resolve to a PullRequest with the number 42."
		if err.Error() != wantErr {
			t.Fatalf("fetchReviewThreads err = %q, want exactly %q (the len(resp.Errors) > 0 branch's wrapped message, not a raw-body echo)", err.Error(), wantErr)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		var calls [][]string
		scriptedGraphQLCommand(t, []string{"not json"}, &calls)

		_, err := fetchReviewThreads("o/r", "42")
		if err == nil {
			t.Fatal("fetchReviewThreads: err = nil, want an error on malformed JSON")
		}
		if errors.Is(err, errFeedbackTruncated) {
			t.Fatalf("fetchReviewThreads err = %v, want a distinct class from errFeedbackTruncated", err)
		}
	})

	t.Run("gh command itself fails", func(t *testing.T) {
		original := execGh
		execGh = func(args ...string) (string, string, error) {
			return "", "authentication required", errors.New("exit status 1")
		}
		t.Cleanup(func() { execGh = original })

		_, err := fetchReviewThreads("o/r", "42")
		if err == nil {
			t.Fatal("fetchReviewThreads: err = nil, want an error when the gh command itself fails (previously discarded via out, _ :=)")
		}
		if errors.Is(err, errFeedbackTruncated) {
			t.Fatalf("fetchReviewThreads err = %v, want a distinct class from errFeedbackTruncated", err)
		}
		if !strings.Contains(err.Error(), "authentication required") {
			t.Fatalf("fetchReviewThreads err = %v, want it to surface the captured gh output", err)
		}
	})
}

// -- reconcileFeedback: the lazy GraphQL gate (#850) --------------------------

// TestReconcileSkipsGraphQLWhenOnlyReviewKeysPending pins the lazy gate: a
// PendingKeys set containing only "review:"-prefixed keys must resolve
// entirely from the reviews payload already fetched this tick and must issue
// zero GraphQL calls -- asserted on recorded calls, not just the outcome.
func TestReconcileSkipsGraphQLWhenOnlyReviewKeysPending(t *testing.T) {
	var calls [][]string
	original := execGh
	execGh = func(args ...string) (string, string, error) {
		calls = append(calls, append([]string{"gh"}, args...))
		return "", "", fmt.Errorf("unexpected command: %v", args)
	}
	t.Cleanup(func() { execGh = original })

	s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"review:10"}, PendingCommentAt: "2026-01-01T00:00:00Z", PendingHeadSHA: "abc"}
	reviews := []review{{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}}

	verdict := reconcileFeedback(s, reviews, true, okActors(map[int64]string{10: "alice"}))

	if verdict.Hold != "" {
		t.Fatalf("verdict.Hold = %q, want empty (the dismissed review resolves cleanly)", verdict.Hold)
	}
	if len(calls) != 0 {
		t.Fatalf("calls = %#v, want zero: a review:-only pending set must never call the GraphQL thread fetch", calls)
	}
	if len(s.PendingKeys) != 0 {
		t.Fatalf("PendingKeys = %v, want empty: the dismissed review must clear", s.PendingKeys)
	}
	if !reflect.DeepEqual(s.AddressedKeys, []string{"review:10"}) {
		t.Fatalf("AddressedKeys = %v, want [review:10]", s.AddressedKeys)
	}
	if s.PendingCommentAt != "" || s.PendingHeadSHA != "" {
		t.Fatalf("PendingCommentAt/PendingHeadSHA = %q/%q, want both cleared once the pending set empties", s.PendingCommentAt, s.PendingHeadSHA)
	}
}

// TestReconcileNoPendingKeysLeavesLastCommentAtUnchanged pins the #850
// security-review guard: with no PendingKeys ever pending (the common,
// well-formed case), reconcileFeedback must not touch LastCommentAt
// at all -- only actually resolving something this tick (len(resolved) > 0)
// may advance it, preventing a stray/inconsistent PendingCommentAt from
// silently overwriting LastCommentAt when nothing was reconciled.
func TestReconcileNoPendingKeysLeavesLastCommentAtUnchanged(t *testing.T) {
	s := &State{Repo: "o/r", PR: "42", LastCommentAt: "2025-12-01T00:00:00Z"}
	verdict := reconcileFeedback(s, nil, true, okActors(nil))
	if verdict.Hold != "" {
		t.Fatalf("verdict.Hold = %q, want empty", verdict.Hold)
	}
	if s.LastCommentAt != "2025-12-01T00:00:00Z" {
		t.Fatalf("LastCommentAt = %q, want unchanged: nothing was pending or resolved this tick", s.LastCommentAt)
	}
}

// -- #885: reclassify all known feedback (pending AND previously addressed) --
//
// classifyFeedback generalizes partitionPending/classifyKey to
// also reclassify AddressedKeys against fresh GitHub state: a previously
// resolved key that GitHub now reports outstanding again returns to pending
// under its own distinct reasonFeedbackReopened hold (Q2), while an absent
// addressed key holds under the existing reasonReviewStateUnknown without
// being treated as reopened (Q1's fail-closed extension). classifyFeedback
// itself stays pure and I/O-free (feedback.go's existing discipline), so
// this whole matrix is testable without any `gh` scripting.

// TestLatestEffectiveReviewSameTimestampTieBreakHigherIDWins is AC 4: two
// same-reviewer reviews sharing an identical SubmittedAt must resolve to the
// higher-database-ID review, independent of which order the API happens to
// return them in -- a naive ">" comparison on SubmittedAt alone leaves same-
// timestamp ties resolved by slice-scan order, which is not guaranteed
// stable across requests.
func TestLatestEffectiveReviewSameTimestampTieBreakHigherIDWins(t *testing.T) {
	ts := "2026-01-01T00:00:00Z"
	lower := review{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: ts, User: login("alice")}
	higher := review{ID: 11, State: "APPROVED", SubmittedAt: ts, User: login("alice")}
	for _, tc := range []struct {
		name    string
		reviews []review
	}{
		{"lower ID first in the slice", []review{lower, higher}},
		{"higher ID first in the slice", []review{higher, lower}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := latestEffectiveReview(tc.reviews, "alice")
			if !ok {
				t.Fatal("latestEffectiveReview: ok = false, want true")
			}
			if got.ID != 11 || got.State != "APPROVED" {
				t.Fatalf("latestEffectiveReview = %+v, want the higher-ID review (ID 11, APPROVED) regardless of input order", got)
			}
		})
	}
}

// TestLatestEffectiveReviewSameTimestampTieBreakIndependentOfWhichStateHasHigherID
// pins that the tie-break is purely "higher database ID wins", not an
// accidental "APPROVED beats CHANGES_REQUESTED" shortcut: the case where the
// higher-ID review is itself CHANGES_REQUESTED must select that one too,
// again independent of input ordering.
func TestLatestEffectiveReviewSameTimestampTieBreakIndependentOfWhichStateHasHigherID(t *testing.T) {
	ts := "2026-01-01T00:00:00Z"
	for _, tc := range []struct {
		name      string
		a, b      review
		wantID    int64
		wantState string
	}{
		{
			"higher ID is APPROVED, wins over the lower-ID CHANGES_REQUESTED",
			review{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: ts, User: login("alice")},
			review{ID: 11, State: "APPROVED", SubmittedAt: ts, User: login("alice")},
			11, "APPROVED",
		},
		{
			"higher ID is CHANGES_REQUESTED, wins over the lower-ID APPROVED",
			review{ID: 20, State: "APPROVED", SubmittedAt: ts, User: login("bob")},
			review{ID: 21, State: "CHANGES_REQUESTED", SubmittedAt: ts, User: login("bob")},
			21, "CHANGES_REQUESTED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range [][]review{{tc.a, tc.b}, {tc.b, tc.a}} {
				got, ok := latestEffectiveReview(order, tc.a.User.Login)
				if !ok {
					t.Fatal("latestEffectiveReview: ok = false, want true")
				}
				if got.ID != tc.wantID || got.State != tc.wantState {
					t.Fatalf("latestEffectiveReview(order=%v) = %+v, want ID=%d State=%s", order, got, tc.wantID, tc.wantState)
				}
			}
		})
	}
}

// TestClassifyFeedbackReopensAddressedCommentKey is AC 1/AC 2: a
// previously-addressed "comment:"-prefixed key whose fresh GraphQL read now
// reports isResolved:false moves to Reopened under the distinct
// reasonFeedbackReopened hold (Q2 rejects collapsing this into the ordinary
// reasonReviewPending), carrying the reopened key's name in Detail.
func TestClassifyFeedbackReopensAddressedCommentKey(t *testing.T) {
	fs := feedbackState{Threads: map[int64]threadState{5: {}}, ReviewsComplete: true}
	got := classifyFeedback(nil, []string{"comment:5"}, fs)
	if !reflect.DeepEqual(got.Reopened, []string{"comment:5"}) {
		t.Fatalf("Reopened = %v, want [comment:5]", got.Reopened)
	}
	if got.Hold != reasonFeedbackReopened {
		t.Fatalf("Hold = %q, want %q", got.Hold, reasonFeedbackReopened)
	}
	if !strings.Contains(got.Detail, "comment:5") {
		t.Fatalf("Detail = %q, want it to name the reopened key comment:5", got.Detail)
	}
	if len(got.Resolved) != 0 {
		t.Fatalf("Resolved = %v, want empty", got.Resolved)
	}
	if len(got.Pending) != 0 {
		t.Fatalf("Pending = %v, want empty: the reopened key came from the addressed set, not the pending set", got.Pending)
	}
}

// TestClassifyFeedbackAddressedKeyAbsentHoldsAsUnknownNotReopened is Q1's
// fail-closed extension: an addressed key GitHub no longer reports at all
// (deleted comment, force-push-purged thread) holds under the existing
// reasonReviewStateUnknown -- absence is never proof of resolution, but it
// is also never proof of a reopen, so the key must not appear in Reopened
// either. It stays addressed (classifyFeedback reports nothing for it at
// all -- the caller leaves it untouched in AddressedKeys).
func TestClassifyFeedbackAddressedKeyAbsentHoldsAsUnknownNotReopened(t *testing.T) {
	fs := feedbackState{Threads: map[int64]threadState{}, ReviewsComplete: true}
	got := classifyFeedback(nil, []string{"comment:5"}, fs)
	if len(got.Reopened) != 0 {
		t.Fatalf("Reopened = %v, want empty: an absent thread must never be treated as reopened", got.Reopened)
	}
	if got.Hold != reasonReviewStateUnknown {
		t.Fatalf("Hold = %q, want %q", got.Hold, reasonReviewStateUnknown)
	}
	if !strings.Contains(got.Detail, "comment:5") {
		t.Fatalf("Detail = %q, want it to identify the offending key (comment:5)", got.Detail)
	}
}

// TestClassifyFeedbackReopensAddressedReviewKey pins the review: half of
// AC 1/AC 2: an addressed "review:"-prefixed key whose reviewer has since
// submitted a fresh CHANGES_REQUESTED (the new latest effective review,
// superseding the earlier resolution) also reopens.
func TestClassifyFeedbackReopensAddressedReviewKey(t *testing.T) {
	fs := feedbackState{
		Reviews: []review{
			{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
			{ID: 12, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-03T00:00:00Z", User: login("alice")},
		},
		ReviewsComplete: true,
	}
	got := classifyFeedback(nil, []string{"review:10"}, fs)
	if !reflect.DeepEqual(got.Reopened, []string{"review:10"}) {
		t.Fatalf("Reopened = %v, want [review:10]", got.Reopened)
	}
	if got.Hold != reasonFeedbackReopened {
		t.Fatalf("Hold = %q, want %q", got.Hold, reasonFeedbackReopened)
	}
}

// TestClassifyFeedbackPrecedenceOrder pins the Implementation Order's exact
// hold precedence: classification-level holds (reasonReviewStateUnknown /
// reasonFeedbackUnsupported), scanning the pending set fully before the
// addressed set, always beat reasonFeedbackReopened -- even when a reopened
// key is discovered earlier in scan order than the unknown/unsupported one.
// Only once no unknown/unsupported key exists anywhere does a reopened key
// actually win the overall Hold.
func TestClassifyFeedbackPrecedenceOrder(t *testing.T) {
	for _, tc := range []struct {
		name               string
		pending, addressed []string
		fs                 feedbackState
		wantHold           string
	}{
		{
			"an unknown key in the pending set beats a reopened key in the addressed set",
			[]string{"comment:1"},
			[]string{"comment:2"},
			feedbackState{Threads: map[int64]threadState{2: {}}, ReviewsComplete: true}, // comment:1 absent -> unknown; comment:2 reopened
			reasonReviewStateUnknown,
		},
		{
			"an unsupported key in the pending set beats a reopened key in the addressed set",
			[]string{"label:1"},
			[]string{"comment:2"},
			feedbackState{Threads: map[int64]threadState{2: {}}, ReviewsComplete: true},
			reasonFeedbackUnsupported,
		},
		{
			"an unknown key in the addressed set beats a reopened key found earlier in addressed scan order",
			nil,
			[]string{"comment:2", "comment:3"}, // comment:2 reopens; comment:3 is absent (unknown)
			feedbackState{Threads: map[int64]threadState{2: {}}, ReviewsComplete: true}, // comment:3 absent from the map entirely
			reasonReviewStateUnknown,
		},
		{
			"with no unknown/unsupported key anywhere, a reopened key wins the overall hold",
			nil,
			[]string{"comment:2"},
			feedbackState{Threads: map[int64]threadState{2: {}}, ReviewsComplete: true},
			reasonFeedbackReopened,
		},
		{
			"an actor-denied key in the pending set beats a reopened key in the addressed set (#862)",
			[]string{"comment:1"},
			[]string{"comment:2"},
			feedbackState{Threads: map[int64]threadState{1: resolvedThread(defaultAutomationLogin, "alice"), 2: {}}, ReviewsComplete: true, Actors: okActors(nil)},
			reasonResolutionActorUnauthorized,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFeedback(tc.pending, tc.addressed, tc.fs)
			if got.Hold != tc.wantHold {
				t.Fatalf("Hold = %q, want %q", got.Hold, tc.wantHold)
			}
		})
	}
}

// TestClassifyFeedbackMixedState is AC 5: pending resolved/still-pending,
// addressed resolved(no-op)/reopened/unknown all in a single pass, asserting
// the exact Resolved/Pending/Reopened lists (not just the overall Hold) and
// that the deterministic output order is "original pending order, then
// reopened in addressed order" (Implementation Order step 2).
func TestClassifyFeedbackMixedState(t *testing.T) {
	pending := []string{"comment:1", "comment:2", "review:10"}
	addressed := []string{"comment:5", "comment:6", "review:20"}
	fs := feedbackState{
		Threads: map[int64]threadState{
			1: resolvedThread("alice", "alice"), // pending -> resolves
			2: {},                               // pending -> stays pending
			5: {},                               // addressed -> reopens
			// 6 intentionally absent -> addressed -> unknown
		},
		Actors: okActors(map[int64]string{10: "carol"}),
		Reviews: []review{
			{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("carol")}, // pending -> resolves
			{ID: 20, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("dave")},
			{ID: 21, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-03T00:00:00Z", User: login("dave")}, // supersedes 20, still blocking -> addressed review:20 reopens
		},
		ReviewsComplete: true,
	}
	got := classifyFeedback(pending, addressed, fs)

	wantResolved := []string{"comment:1", "review:10"}
	if !reflect.DeepEqual(got.Resolved, wantResolved) {
		t.Errorf("Resolved = %v, want %v", got.Resolved, wantResolved)
	}
	wantPending := []string{"comment:2"}
	if !reflect.DeepEqual(got.Pending, wantPending) {
		t.Errorf("Pending = %v, want %v", got.Pending, wantPending)
	}
	wantReopened := []string{"comment:5", "review:20"}
	if !reflect.DeepEqual(got.Reopened, wantReopened) {
		t.Errorf("Reopened = %v, want %v", got.Reopened, wantReopened)
	}
	if got.Hold != reasonReviewStateUnknown {
		t.Errorf("Hold = %q, want %q (comment:6 is absent from the addressed set, which beats the two reopened keys)", got.Hold, reasonReviewStateUnknown)
	}
	if !strings.Contains(got.Detail, "comment:6") {
		t.Errorf("Detail = %q, want it to identify the offending key (comment:6)", got.Detail)
	}
}

// TestClassifyFeedbackWithNoAddressedKeysMatchesPendingOnlySemantics pins
// the Implementation Order's explicit backward-compatibility requirement:
// "Pending-set keys keep today's semantics exactly." With an empty
// addressed set, classifyFeedback's Resolved/Pending/Hold output must match
// the pre-#885 partitionPending behavior exactly (compare
// TestPartitionPendingKeysMixedState above).
func TestClassifyFeedbackWithNoAddressedKeysMatchesPendingOnlySemantics(t *testing.T) {
	pending := []string{"comment:1", "comment:2", "review:10"}
	fs := feedbackState{
		Threads:         map[int64]threadState{1: resolvedThread("alice", "alice"), 2: {}},
		Reviews:         []review{{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("bob")}},
		ReviewsComplete: true,
		Actors:          okActors(map[int64]string{10: "bob"}),
	}
	got := classifyFeedback(pending, nil, fs)
	wantResolved := []string{"comment:1", "review:10"}
	wantPending := []string{"comment:2"}
	if !reflect.DeepEqual(got.Resolved, wantResolved) {
		t.Errorf("Resolved = %v, want %v", got.Resolved, wantResolved)
	}
	if !reflect.DeepEqual(got.Pending, wantPending) {
		t.Errorf("Pending = %v, want %v", got.Pending, wantPending)
	}
	if len(got.Reopened) != 0 {
		t.Errorf("Reopened = %v, want empty: no addressed set was passed", got.Reopened)
	}
	if got.Hold != "" {
		t.Errorf("Hold = %q, want empty", got.Hold)
	}
}

// TestReconcileFeedbackFetchFailureLeavesAddressedKeysUnmovedInEitherDirection
// is the Test Strategy's "truncated / unreadable thread read with addressed
// keys present" case: a fetch-level failure (malformed body, or a
// truncated traversal) must hold under its existing distinct reason and
// never move any key in either direction -- an addressed key must not
// spuriously reopen, and a pending key must not spuriously resolve, on an
// unreliable read.
func TestReconcileFeedbackFetchFailureLeavesAddressedKeysUnmovedInEitherDirection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantHold string
	}{
		{"unreadable: malformed GraphQL body", "not json", reasonFeedbackUnreadable},
		{
			"truncated: a thread's comments.totalCount exceeds its fetched node count",
			threadPage(false, "", threadNode(false, 2, 5)),
			reasonFeedbackTruncated,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			scriptedGraphQLCommand(t, []string{tc.body}, &calls)
			s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"comment:1"}, AddressedKeys: []string{"comment:5"}}
			verdict := reconcileFeedback(s, nil, true, okActors(nil))
			if verdict.Hold != tc.wantHold {
				t.Fatalf("verdict.Hold = %q, want %q", verdict.Hold, tc.wantHold)
			}
			if !reflect.DeepEqual(s.PendingKeys, []string{"comment:1"}) {
				t.Fatalf("PendingKeys = %v, want unchanged [comment:1]", s.PendingKeys)
			}
			if !reflect.DeepEqual(s.AddressedKeys, []string{"comment:5"}) {
				t.Fatalf("AddressedKeys = %v, want unchanged [comment:5]: a fetch failure must never reopen or resolve anything", s.AddressedKeys)
			}
		})
	}
}

// TestReconcileFeedbackGraphQLGateWidenedToAddressedCommentKeys is Q3: the
// lazy GraphQL thread fetch must fire whenever ANY known key -- pending or
// addressed -- is a "comment:"-prefixed key, not just a pending one, so
// reopen detection works even when PendingKeys itself holds only
// "review:"-prefixed entries (or is empty).
func TestReconcileFeedbackGraphQLGateWidenedToAddressedCommentKeys(t *testing.T) {
	var calls [][]string
	scriptedGraphQLCommand(t, []string{threadPage(false, "", threadNode(true, 1, 5))}, &calls)
	s := &State{Repo: "o/r", PR: "42", PendingKeys: nil, AddressedKeys: []string{"comment:5"}}
	reconcileFeedback(s, nil, true, okActors(nil))
	if len(calls) == 0 {
		t.Fatal("reconcileFeedback issued no GraphQL call: the lazy gate must fire for an addressed comment: key too, not just a pending one")
	}
}

// -- resolution-actor authorization (#862) ------------------------------------

// AC 26: the reviewThreads query asks GitHub who resolved each thread and who
// wrote each comment -- the two facts the actor rule needs. Asserted on the query
// text with its whitespace removed, so the check holds for any formatting.
func TestReviewThreadsQueryRequestsResolverAndCommentAuthor(t *testing.T) {
	compact := strings.Join(strings.Fields(reviewThreadsQuery), "")
	if !strings.Contains(compact, "resolvedBy{login}") {
		t.Errorf("reviewThreadsQuery = %q, want it to request each thread's resolvedBy { login }", reviewThreadsQuery)
	}
	if !strings.Contains(compact, "author{login}") {
		t.Errorf("reviewThreadsQuery = %q, want it to request each comment's author { login }", reviewThreadsQuery)
	}
}

// The thread state carries the resolver, whether it is known, and the thread's own
// author: the FIRST comment's. A reply's key maps to the same thread state. A null
// resolver (a deleted account) and a null author are both "unknown", not "".
func TestFetchReviewThreadsParsesResolverAndThreadAuthor(t *testing.T) {
	page := threadPage(false, "",
		actorThreadNode(true, "bob", threadComment(100, "alice"), threadComment(101, "carol")),
		actorThreadNode(true, "", threadComment(200, "alice")),
		actorThreadNode(false, "", threadComment(300, "")),
	)
	var calls [][]string
	scriptedGraphQLCommand(t, []string{page}, &calls)

	got, err := fetchReviewThreads("o/r", "42")

	if err != nil {
		t.Fatalf("fetchReviewThreads: %v", err)
	}
	want := map[int64]threadState{
		100: {Resolved: true, ResolvedBy: "bob", ResolvedByKnown: true, Author: "alice"},
		101: {Resolved: true, ResolvedBy: "bob", ResolvedByKnown: true, Author: "alice"},
		200: {Resolved: true, ResolvedBy: "", ResolvedByKnown: false, Author: "alice"},
		300: {Resolved: false, ResolvedBy: "", ResolvedByKnown: false, Author: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fetchReviewThreads = %#v, want %#v", got, want)
	}
}

// reviewsFor is a complete reviews read: CHANGES_REQUESTED review 10 by alice,
// optionally followed by later reviews.
func reviewsFor(later ...review) []review {
	return append([]review{{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")}}, later...)
}

var dismissedByAlice = []review{{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}}

// The actor matrix for classifyKey, asserted as exact keyStatus values: a
// resolution that fails the actor rule is keyActorDenied (distinct from keyPending,
// keyUnknown and a lookup failure), a failed lookup is keyActorLookupFailed, and the
// lazy reads (identity, dismissal timeline, permission) are counted per case.
func TestClassifyPendingKeyActorMatrix(t *testing.T) {
	const bot = defaultAutomationLogin
	attest := func(key, actor string) map[string]actorAttestation {
		return map[string]actorAttestation{key: {Actor: actor, Source: attestationSourceHumanCLI}}
	}
	for _, tc := range []struct {
		name  string
		key   string
		build func(f *fakeActors) feedbackState
		want  keyStatus
		// reads is {identity, dismissal timeline, permission} when the case pins them.
		reads   *[3]int
		wantErr bool
	}{
		// -- comment keys: the thread's resolver --
		{name: "comment: resolved by the thread's own author", key: "comment:5", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread("alice", "alice")}}
			}, reads: &[3]int{1, 0, 0}},
		{name: "comment: resolved by the automation login", key: "comment:5", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread(bot, "alice")}}
			}, reads: &[3]int{1, 0, 0}},
		{name: "comment: the automation login's own thread, resolved by it", key: "comment:5", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread(bot, bot)}}
			}},
		{name: "comment: resolved by a write-access login", key: "comment:5", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				f.perms["maintainer"] = writePermissionGranted
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread("maintainer", "alice")}}
			}, reads: &[3]int{1, 0, 1}},
		{name: "comment: resolved by a login without write access", key: "comment:5", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread("outsider", "alice")}}
			}, reads: &[3]int{1, 0, 1}},
		{name: "comment: an unknown permission is a lookup failure", key: "comment:5", want: keyActorLookupFailed,
			build: func(f *fakeActors) feedbackState {
				f.perms["outsider"] = writePermissionUnknown
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread("outsider", "alice")}}
			}},
		{name: "comment: a null resolver is denied", key: "comment:5", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread("", "alice")}}
			}, reads: &[3]int{0, 0, 0}},
		{name: "comment: an unresolved thread needs no actor lookup", key: "comment:5", want: keyPending,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: {Author: "alice"}}}
			}, reads: &[3]int{0, 0, 0}},
		{name: "comment: a thread GitHub no longer reports needs no actor lookup", key: "comment:5", want: keyUnknown,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{6: resolvedThread("alice", "alice")}}
			}, reads: &[3]int{0, 0, 0}},
		{name: "comment: an automation identity failure is a lookup failure with a sticky error", key: "comment:5", want: keyActorLookupFailed, wantErr: true,
			build: func(f *fakeActors) feedbackState {
				f.loginErr = errors.New("gh: api user failed")
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread("alice", "alice")}}
			}},
		{name: "comment: an attestation for the automation login accepts its resolution", key: "comment:5", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread(bot, "alice")}, Attestations: attest("comment:5", bot)}
			}},
		{name: "comment: an attestation for another actor does not vouch", key: "comment:5", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread(bot, "alice")}, Attestations: attest("comment:5", "maintainer")}
			}},
		{name: "comment: an attestation on another key does not vouch", key: "comment:5", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Threads: map[int64]threadState{5: resolvedThread(bot, "alice")}, Attestations: attest("comment:6", bot)}
			}},

		// -- review keys: the dismissal event's actor, or the superseding review's author --
		{name: "review: dismissed by the review's own author", key: "review:10", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{10: "alice"}
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true}
			}, reads: &[3]int{1, 1, 0}},
		{name: "review: dismissed by the automation login", key: "review:10", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{10: bot}
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true}
			}},
		{name: "review: dismissed with no event in a complete timeline", key: "review:10", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{99: "alice"}
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true}
			}, reads: &[3]int{0, 1, 0}},
		{name: "review: dismissed by an unknown actor (a null or conflicting event)", key: "review:10", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{10: ""}
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true}
			}},
		{name: "review: dismissed by a write-access login", key: "review:10", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{10: "maintainer"}
				f.perms["maintainer"] = writePermissionGranted
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true}
			}, reads: &[3]int{1, 1, 1}},
		{name: "review: dismissed by a login without write access", key: "review:10", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{10: "outsider"}
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true}
			}},
		{name: "review: an unreadable timeline is a lookup failure with a sticky error", key: "review:10", want: keyActorLookupFailed, wantErr: true,
			build: func(f *fakeActors) feedbackState {
				f.dismissalsErr = errors.New("gh: timeline failed")
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true}
			}},
		{name: "review: superseded by the same reviewer's approval (its author)", key: "review:10", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Reviews: reviewsFor(review{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}), ReviewsComplete: true}
			}, reads: &[3]int{1, 0, 0}},
		{name: "review: superseded by an approval from the automation login", key: "review:10", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Reviews: []review{
					{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login(bot)},
					{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login(bot)},
				}, ReviewsComplete: true}
			}, reads: &[3]int{1, 0, 0}},
		{name: "review: superseded by a dismissal by the automation login (the LATER review's event)", key: "review:10", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{11: bot, 10: "alice"}
				return feedbackState{Reviews: reviewsFor(review{ID: 11, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}), ReviewsComplete: true}
			}, reads: &[3]int{1, 1, 0}},
		{name: "review: superseded by a dismissal by a write-access login", key: "review:10", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{11: "maintainer"}
				f.perms["maintainer"] = writePermissionGranted
				return feedbackState{Reviews: reviewsFor(review{ID: 11, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}), ReviewsComplete: true}
			}},
		{name: "review: superseded by a dismissal with no event", key: "review:10", want: keyActorDenied,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Reviews: reviewsFor(review{ID: 11, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}), ReviewsComplete: true}
			}},
		{name: "review: superseded by a later CHANGES_REQUESTED stays pending with no lookup at all (Edge 56)", key: "review:10", want: keyPending,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Reviews: reviewsFor(review{ID: 12, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}), ReviewsComplete: true}
			}, reads: &[3]int{0, 0, 0}},
		{name: "review: an unproven-complete reviews read is unknown with no lookup", key: "review:10", want: keyUnknown,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{10: "alice"}
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: false}
			}, reads: &[3]int{0, 0, 0}},
		{name: "review: an attestation for the dismissing actor accepts it", key: "review:10", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				f.dismissals = map[int64]string{10: bot}
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true, Attestations: attest("review:10", bot)}
			}},
		{name: "review: an attestation for an unknown dismissal actor accepts it", key: "review:10", want: keyResolved,
			build: func(f *fakeActors) feedbackState {
				return feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true, Attestations: attest("review:10", "")}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeActors()
			fs := tc.build(f)
			fs.Actors = f.ctx()

			got := classifyKey(tc.key, fs).Status

			if got != tc.want {
				t.Fatalf("classifyKey(%q).Status = %v, want %v", tc.key, got, tc.want)
			}
			if tc.reads != nil {
				if gotReads := [3]int{f.loginReads, f.dismissalReads, permTotal(f.permReads)}; gotReads != *tc.reads {
					t.Errorf("reads {identity, timeline, permission} = %v, want %v", gotReads, *tc.reads)
				}
			}
			if (fs.Actors.err != nil) != tc.wantErr {
				t.Errorf("sticky error = %v, want an error = %v", fs.Actors.err, tc.wantErr)
			}
		})
	}
}

func permTotal(m map[string]int) int {
	n := 0
	for _, c := range m {
		n += c
	}
	return n
}

// The pure classifier fails closed when it was handed no actorContext: a resolution
// can never be accepted without the actor rule's inputs.
func TestClassifyPendingKeyWithoutAnActorContextNeverResolves(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		fs   feedbackState
	}{
		{"a resolved thread", "comment:5", feedbackState{Threads: map[int64]threadState{5: resolvedThread("alice", "alice")}}},
		{"a dismissed review", "review:10", feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true}},
		{"an approved-over review", "review:10", feedbackState{Reviews: reviewsFor(review{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}), ReviewsComplete: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyKey(tc.key, tc.fs).Status; got != keyActorLookupFailed {
				t.Fatalf("classifyKey(%q).Status with no Actors = %v, want keyActorLookupFailed (never keyResolved)", tc.key, got)
			}
		})
	}
}

// classifyFeedback's actor outputs: the hold reason and its detail, the actor each
// held key is recorded with ("" when unknown), a revoked addressed key, a lookup
// failure that is not a denial, and the attestations to drop.
func TestClassifyFeedbackActorOutputs(t *testing.T) {
	const bot = defaultAutomationLogin

	t.Run("a pending key resolved by the automation login stays pending and held", func(t *testing.T) {
		fs := feedbackState{Threads: map[int64]threadState{5: resolvedThread(bot, "alice")}, ReviewsComplete: true, Actors: okActors(nil)}
		got := classifyFeedback([]string{"comment:5"}, nil, fs)

		if !reflect.DeepEqual(got.Pending, []string{"comment:5"}) || len(got.Resolved) != 0 {
			t.Errorf("Pending/Resolved = %v/%v, want [comment:5]/none", got.Pending, got.Resolved)
		}
		if got.Hold != reasonResolutionActorUnauthorized || !strings.Contains(got.Detail, "comment:5") {
			t.Errorf("Hold/Detail = %q/%q, want %q naming comment:5", got.Hold, got.Detail, reasonResolutionActorUnauthorized)
		}
		if want := map[string]string{"comment:5": bot}; !reflect.DeepEqual(got.ActorHeld, want) {
			t.Errorf("ActorHeld = %v, want %v", got.ActorHeld, want)
		}
	})
	t.Run("a null resolver is held with the detail `resolver unknown` and an empty actor", func(t *testing.T) {
		fs := feedbackState{Threads: map[int64]threadState{5: resolvedThread("", "alice")}, ReviewsComplete: true, Actors: okActors(nil)}
		got := classifyFeedback([]string{"comment:5"}, nil, fs)

		if got.Hold != reasonResolutionActorUnauthorized || !strings.Contains(got.Detail, "resolver unknown") {
			t.Errorf("Hold/Detail = %q/%q, want %q with `resolver unknown`", got.Hold, got.Detail, reasonResolutionActorUnauthorized)
		}
		if actor, ok := got.ActorHeld["comment:5"]; !ok || actor != "" {
			t.Errorf("ActorHeld = %v, want comment:5 held with an empty actor", got.ActorHeld)
		}
	})
	t.Run("a dismissed review with no event is held with the detail `dismissal actor unknown`", func(t *testing.T) {
		fs := feedbackState{Reviews: dismissedByAlice, ReviewsComplete: true, Actors: okActors(nil)}
		got := classifyFeedback([]string{"review:10"}, nil, fs)

		if got.Hold != reasonResolutionActorUnauthorized || !strings.Contains(got.Detail, "dismissal actor unknown") {
			t.Errorf("Hold/Detail = %q/%q, want %q with `dismissal actor unknown`", got.Hold, got.Detail, reasonResolutionActorUnauthorized)
		}
		if actor, ok := got.ActorHeld["review:10"]; !ok || actor != "" {
			t.Errorf("ActorHeld = %v, want review:10 held with an empty actor", got.ActorHeld)
		}
	})
	t.Run("an addressed key re-resolved by the automation login is revoked, not reopened", func(t *testing.T) {
		fs := feedbackState{Threads: map[int64]threadState{5: resolvedThread(bot, "alice")}, ReviewsComplete: true, Actors: okActors(nil)}
		got := classifyFeedback(nil, []string{"comment:5"}, fs)

		if !reflect.DeepEqual(got.ActorRevoked, []string{"comment:5"}) || len(got.Reopened) != 0 {
			t.Errorf("ActorRevoked/Reopened = %v/%v, want [comment:5]/none", got.ActorRevoked, got.Reopened)
		}
		if got.Hold != reasonResolutionActorUnauthorized || !strings.Contains(got.Detail, "comment:5") {
			t.Errorf("Hold/Detail = %q/%q, want %q naming comment:5", got.Hold, got.Detail, reasonResolutionActorUnauthorized)
		}
		if want := map[string]string{"comment:5": bot}; !reflect.DeepEqual(got.ActorHeld, want) {
			t.Errorf("ActorHeld = %v, want %v", got.ActorHeld, want)
		}
	})
	t.Run("an addressed key whose resolution still passes is a silent no-op", func(t *testing.T) {
		fs := feedbackState{Threads: map[int64]threadState{5: resolvedThread("alice", "alice")}, ReviewsComplete: true, Actors: okActors(nil)}
		got := classifyFeedback(nil, []string{"comment:5"}, fs)

		if got.Hold != "" || len(got.ActorRevoked) != 0 || len(got.ActorHeld) != 0 || len(got.Reopened) != 0 {
			t.Errorf("got %+v, want nothing: GitHub still confirms the key resolved by an authorized actor", got)
		}
	})
	t.Run("a failed permission lookup is not a denial: the key stays where it was under the unreadable hold", func(t *testing.T) {
		for _, tc := range []struct {
			name               string
			pending, addressed []string
		}{
			{"pending", []string{"comment:5"}, nil},
			{"addressed", nil, []string{"comment:5"}},
		} {
			f := newFakeActors()
			f.perms["outsider"] = writePermissionUnknown
			fs := feedbackState{Threads: map[int64]threadState{5: resolvedThread("outsider", "alice")}, ReviewsComplete: true, Actors: f.ctx()}
			got := classifyFeedback(tc.pending, tc.addressed, fs)

			if got.Hold != reasonFeedbackUnreadable || !strings.Contains(got.Detail, "resolution actor permission lookup failed for comment:5; will retry") {
				t.Errorf("%s: Hold/Detail = %q/%q, want %q with the retry detail", tc.name, got.Hold, got.Detail, reasonFeedbackUnreadable)
			}
			if len(got.ActorHeld) != 0 || len(got.ActorRevoked) != 0 || len(got.Resolved) != 0 || len(got.Reopened) != 0 {
				t.Errorf("%s: got %+v, want no key moved or held", tc.name, got)
			}
			if !reflect.DeepEqual(got.Pending, tc.pending) {
				t.Errorf("%s: Pending = %v, want %v", tc.name, got.Pending, tc.pending)
			}
		}
	})
	t.Run("attestations of keys GitHub now reports unresolved are dropped, the others kept", func(t *testing.T) {
		fs := feedbackState{
			Threads: map[int64]threadState{
				5: {Author: "alice"},            // reopened: attested key, now unresolved
				6: resolvedThread(bot, "alice"), // still resolved by the attested actor
				// 7 absent: unknown never drops
			},
			ReviewsComplete: true,
			Actors:          okActors(nil),
			Attestations: map[string]actorAttestation{
				"comment:5": {Actor: bot}, "comment:6": {Actor: bot}, "comment:7": {Actor: bot},
			},
		}
		got := classifyFeedback([]string{"comment:6", "comment:7"}, []string{"comment:5"}, fs)

		if !reflect.DeepEqual(got.AttestationDrops, []string{"comment:5"}) {
			t.Errorf("AttestationDrops = %v, want [comment:5]", got.AttestationDrops)
		}
		if !reflect.DeepEqual(got.Reopened, []string{"comment:5"}) {
			t.Errorf("Reopened = %v, want [comment:5]: the dropped attestation's key goes back through the ordinary reopen path", got.Reopened)
		}
		if !reflect.DeepEqual(got.Resolved, []string{"comment:6"}) {
			t.Errorf("Resolved = %v, want [comment:6]: the attestation still vouches for the actor GitHub reports", got.Resolved)
		}
	})
}

// reconcileFeedback applies the actor verdict onto State.
func TestReconcileFeedbackAppliesTheActorVerdict(t *testing.T) {
	const bot = defaultAutomationLogin
	run := func(t *testing.T, s *State, thread string, actors *actorContext) feedbackVerdict {
		t.Helper()
		var calls [][]string
		scriptedGraphQLCommand(t, []string{thread}, &calls)
		return reconcileFeedback(s, nil, true, actors)
	}
	resolvedBy := func(resolver string) string {
		return threadsResponse(actorThreadNode(true, resolver, threadComment(5, "alice")))
	}

	t.Run("a pending key resolved by the automation login is held, not addressed", func(t *testing.T) {
		s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"comment:5"}, LaunchedKeys: []string{"comment:5"}, PendingCommentAt: "2026-01-01T00:00:00Z"}
		verdict := run(t, s, resolvedBy(bot), okActors(nil))

		if verdict.Hold != reasonResolutionActorUnauthorized {
			t.Errorf("Hold = %q, want %q", verdict.Hold, reasonResolutionActorUnauthorized)
		}
		if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) || len(s.AddressedKeys) != 0 || !reflect.DeepEqual(s.LaunchedKeys, []string{"comment:5"}) {
			t.Errorf("pending/addressed/launched = %v/%v/%v, want comment:5 left pending and launched", s.PendingKeys, s.AddressedKeys, s.LaunchedKeys)
		}
		if want := map[string]string{"comment:5": bot}; !reflect.DeepEqual(s.ActorHeldKeys, want) {
			t.Errorf("ActorHeldKeys = %v, want %v", s.ActorHeldKeys, want)
		}
		if s.PendingCommentAt == "" || s.LastCommentAt != "" {
			t.Errorf("PendingCommentAt/LastCommentAt = %q/%q, want the watermark not advanced for a held key", s.PendingCommentAt, s.LastCommentAt)
		}
	})
	t.Run("an addressed key re-resolved by the automation login moves back to pending, held", func(t *testing.T) {
		s := &State{Repo: "o/r", PR: "42", AddressedKeys: []string{"comment:5"}}
		run(t, s, resolvedBy(bot), okActors(nil))

		if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) || len(s.AddressedKeys) != 0 {
			t.Errorf("pending/addressed = %v/%v, want comment:5 back in pending", s.PendingKeys, s.AddressedKeys)
		}
		if want := map[string]string{"comment:5": bot}; !reflect.DeepEqual(s.ActorHeldKeys, want) {
			t.Errorf("ActorHeldKeys = %v, want %v", s.ActorHeldKeys, want)
		}
	})
	t.Run("a held key an authorized actor now resolves is addressed and leaves the held set", func(t *testing.T) {
		s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"comment:5"}, LaunchedKeys: []string{"comment:5"}, ActorHeldKeys: map[string]string{"comment:5": bot}}
		run(t, s, resolvedBy("alice"), okActors(nil))

		if len(s.PendingKeys) != 0 || !reflect.DeepEqual(s.AddressedKeys, []string{"comment:5"}) || len(s.ActorHeldKeys) != 0 {
			t.Errorf("pending/addressed/held = %v/%v/%v, want comment:5 addressed and released", s.PendingKeys, s.AddressedKeys, s.ActorHeldKeys)
		}
	})
	t.Run("a held key GitHub reports unresolved again leaves the held set", func(t *testing.T) {
		s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"comment:5"}, LaunchedKeys: []string{"comment:5"}, ActorHeldKeys: map[string]string{"comment:5": bot}}
		run(t, s, threadsResponse(actorThreadNode(false, "", threadComment(5, "alice"))), okActors(nil))

		if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) || len(s.ActorHeldKeys) != 0 {
			t.Errorf("pending/held = %v/%v, want comment:5 pending and no longer held", s.PendingKeys, s.ActorHeldKeys)
		}
	})
	t.Run("an unknown permission leaves a held key's membership unchanged", func(t *testing.T) {
		f := newFakeActors()
		f.perms["outsider"] = writePermissionUnknown
		s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"comment:5"}, ActorHeldKeys: map[string]string{"comment:5": bot}}
		verdict := run(t, s, resolvedBy("outsider"), f.ctx())

		if verdict.Hold != reasonFeedbackUnreadable {
			t.Errorf("Hold = %q, want %q", verdict.Hold, reasonFeedbackUnreadable)
		}
		if want := map[string]string{"comment:5": bot}; !reflect.DeepEqual(s.ActorHeldKeys, want) || !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) {
			t.Errorf("held/pending = %v/%v, want both unchanged", s.ActorHeldKeys, s.PendingKeys)
		}
	})
	t.Run("a key GitHub no longer reports keeps its held membership", func(t *testing.T) {
		s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"comment:5"}, ActorHeldKeys: map[string]string{"comment:5": bot}}
		verdict := run(t, s, threadsResponse(actorThreadNode(true, "alice", threadComment(999, "alice"))), okActors(nil))

		if verdict.Hold != reasonReviewStateUnknown {
			t.Errorf("Hold = %q, want %q", verdict.Hold, reasonReviewStateUnknown)
		}
		if want := map[string]string{"comment:5": bot}; !reflect.DeepEqual(s.ActorHeldKeys, want) {
			t.Errorf("ActorHeldKeys = %v, want %v unchanged", s.ActorHeldKeys, want)
		}
	})
	t.Run("a key GitHub reports unresolved drops its attestation, a resolved one keeps it", func(t *testing.T) {
		attest := func(keys ...string) map[string]actorAttestation {
			m := map[string]actorAttestation{}
			for _, k := range keys {
				m[k] = actorAttestation{Actor: bot, Source: attestationSourceHumanCLI}
			}
			return m
		}
		s := &State{Repo: "o/r", PR: "42", AddressedKeys: []string{"comment:5", "comment:6"}, ActorAttestations: attest("comment:5", "comment:6")}
		page := threadsResponse(
			actorThreadNode(false, "", threadComment(5, "alice")),
			actorThreadNode(true, bot, threadComment(6, "alice")),
		)
		run(t, s, page, okActors(nil))

		if !reflect.DeepEqual(s.ActorAttestations, attest("comment:6")) {
			t.Errorf("ActorAttestations = %v, want only comment:6 kept", s.ActorAttestations)
		}
		if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) || !reflect.DeepEqual(s.AddressedKeys, []string{"comment:6"}) {
			t.Errorf("pending/addressed = %v/%v, want comment:5 reopened and comment:6 still addressed (the attestation vouches)", s.PendingKeys, s.AddressedKeys)
		}
	})
}

// A pass-level failure -- the automation identity, an unreadable timeline, a truncated
// timeline -- is a fetch-level hold: it names its own reason, and NO key moves in either
// direction, not even the ones that needed no lookup.
func TestReconcileFeedbackActorLookupFailureHoldsTheWholePassWithNothingMoved(t *testing.T) {
	for _, tc := range []struct {
		name       string
		actors     func() *actorContext
		wantHold   string
		wantDetail string
	}{
		{"the automation identity lookup fails", func() *actorContext {
			f := newFakeActors()
			f.loginErr = errors.New("gh: api user failed")
			return f.ctx()
		}, reasonFeedbackUnreadable, "automation identity lookup failed"},
		{"the dismissal timeline is unreadable", func() *actorContext {
			f := newFakeActors()
			f.dismissalsErr = errors.New("gh: timeline failed")
			return f.ctx()
		}, reasonFeedbackUnreadable, ""},
		{"the dismissal timeline is truncated", func() *actorContext {
			f := newFakeActors()
			f.dismissalsErr = fmt.Errorf("%w: exceeded maxThreadPages", errFeedbackTruncated)
			return f.ctx()
		}, reasonFeedbackTruncated, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			// comment:1 would resolve (its own author); review:10 needs the timeline.
			scriptedGraphQLCommand(t, []string{threadsResponse(actorThreadNode(true, "alice", threadComment(1, "alice")))}, &calls)
			s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"comment:1", "review:10"}, AddressedKeys: []string{"review:20"},
				ActorHeldKeys: map[string]string{"review:10": "x"}}

			verdict := reconcileFeedback(s, dismissedAndOther(), true, tc.actors())

			if verdict.Hold != tc.wantHold {
				t.Errorf("Hold = %q, want %q", verdict.Hold, tc.wantHold)
			}
			if !strings.Contains(verdict.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to contain %q", verdict.Detail, tc.wantDetail)
			}
			if !reflect.DeepEqual(s.PendingKeys, []string{"comment:1", "review:10"}) || !reflect.DeepEqual(s.AddressedKeys, []string{"review:20"}) || !reflect.DeepEqual(s.ActorHeldKeys, map[string]string{"review:10": "x"}) {
				t.Errorf("pending/addressed/held = %v/%v/%v, want every key where it was", s.PendingKeys, s.AddressedKeys, s.ActorHeldKeys)
			}
		})
	}
}

// dismissedAndOther is review 10 DISMISSED and review 20 DISMISSED, both by alice.
func dismissedAndOther() []review {
	return []review{
		{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
		{ID: 20, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
	}
}

// AC 39: heldUntrustedNote renders, for each held comment:/review: key, `resolve <key>
// on GitHub, then run `cenci babysit resolve <pr> <key>` if it was resolved as the
// automation login`; a discussion key keeps its own `run `cenci babysit resolve ...“
// step. The exact rendered string is asserted.
func TestHeldUntrustedNoteRendersTheGitHubThenResolveRecovery(t *testing.T) {
	const prefix = "untrusted feedback held for a write-access human (its author lacks write permission, or its untrusted launch cap is used up); once handled, "
	got := heldUntrustedNote("42", []string{"discussion:11", "comment:7", "review:9"})
	want := prefix +
		"run `cenci babysit resolve 42 discussion:11`; " +
		"resolve comment:7 on GitHub, then run `cenci babysit resolve 42 comment:7` if it was resolved as the automation login; " +
		"resolve review:9 on GitHub, then run `cenci babysit resolve 42 review:9` if it was resolved as the automation login"
	if got != want {
		t.Fatalf("heldUntrustedNote =\n%q\nwant\n%q", got, want)
	}
}

// -- ordinary PR discussion (#1472) -------------------------------------------

// AC: a state holding only discussion: keys makes zero GraphQL calls, never
// classifies a discussion key as unsupported, and leaves every key untouched; the
// comment:/review: pass is otherwise unchanged (the cases above).
func TestReconcileFeedbackDiscussionOnlyStateMakesNoGraphQLCallAndKeepsEveryKey(t *testing.T) {
	var calls [][]string
	scriptedGraphQLCommand(t, nil, &calls) // any call fails the test
	s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"discussion:11"}, AddressedKeys: []string{"discussion:9"}, LaunchedKeys: []string{"discussion:11"}}

	verdict := reconcileFeedback(s, nil, true, okActors(nil))

	if verdict.Hold != "" || len(verdict.Reopened) != 0 {
		t.Errorf("verdict = %+v, want clean: a discussion key is not an unsupported key type", verdict)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || !reflect.DeepEqual(s.AddressedKeys, []string{"discussion:9"}) || !reflect.DeepEqual(s.LaunchedKeys, []string{"discussion:11"}) {
		t.Errorf("keys changed: pending %v addressed %v launched %v", s.PendingKeys, s.AddressedKeys, s.LaunchedKeys)
	}
	if len(calls) != 0 {
		t.Errorf("gh calls = %v, want none", calls)
	}
}

// A mixed state still resolves its comment: keys and leaves its discussion keys
// pending in their original positions' relative order.
func TestReconcileFeedbackMixedStateResolvesOnlyTheCommentKeys(t *testing.T) {
	var calls [][]string
	scriptedGraphQLCommand(t, []string{threadsResponse(actorThreadNode(true, "alice", threadComment(7, "alice")))}, &calls)
	s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"discussion:11", "comment:7"}, LaunchedKeys: []string{"discussion:11", "comment:7"}}

	reconcileFeedback(s, nil, true, okActors(nil))

	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || !reflect.DeepEqual(s.AddressedKeys, []string{"comment:7"}) || !reflect.DeepEqual(s.LaunchedKeys, []string{"discussion:11"}) {
		t.Errorf("pending %v addressed %v launched %v, want comment:7 resolved and discussion:11 untouched", s.PendingKeys, s.AddressedKeys, s.LaunchedKeys)
	}
}

// classifyDiscussion is pure: new human comments, edits of pending keys, reopened
// addressed keys and deleted pending keys, each with its own reason; a deleted
// addressed key is a no-op.
func TestClassifyDiscussion(t *testing.T) {
	r1, r2 := strings.Repeat("1", 64), strings.Repeat("2", 64)
	for _, tc := range []struct {
		name    string
		state   State
		current map[string]string
		want    discussionClassification
	}{
		{"a never-seen comment is new", State{}, map[string]string{"discussion:1": r1},
			discussionClassification{New: []string{"discussion:1"}}},
		{"a pending key at its recorded revision is unchanged", State{PendingKeys: []string{"discussion:1"}, DiscussionRevisions: map[string]string{"discussion:1": r1}},
			map[string]string{"discussion:1": r1}, discussionClassification{}},
		{"an edited pending key is edited, not reopened", State{PendingKeys: []string{"discussion:1"}, DiscussionRevisions: map[string]string{"discussion:1": r1}},
			map[string]string{"discussion:1": r2}, discussionClassification{Edited: []string{"discussion:1"}}},
		{"an edited addressed key reopens", State{AddressedKeys: []string{"discussion:2"}, DiscussionRevisions: map[string]string{"discussion:2": r1},
			DiscussionReceipts: map[string]discussionReceipt{"discussion:2": {Revision: r1}}},
			map[string]string{"discussion:2": r2},
			discussionClassification{Reopened: []string{"discussion:2"}, Hold: reasonFeedbackReopened, Detail: "discussion:2: review feedback reopened"}},
		{"an addressed key at its revision is a no-op", State{AddressedKeys: []string{"discussion:2"}, DiscussionRevisions: map[string]string{"discussion:2": r1}},
			map[string]string{"discussion:2": r1}, discussionClassification{}},
		{"a deleted pending key is unknown", State{PendingKeys: []string{"discussion:1"}, DiscussionRevisions: map[string]string{"discussion:1": r1}},
			map[string]string{}, discussionClassification{Unknown: []string{"discussion:1"}, Hold: reasonReviewStateUnknown, Detail: "discussion:1: not found or in an unrecognized state"}},
		{"a deleted addressed key is a no-op", State{AddressedKeys: []string{"discussion:2"}, DiscussionRevisions: map[string]string{"discussion:2": r1}},
			map[string]string{}, discussionClassification{}},
		{"unknown outranks reopened", State{PendingKeys: []string{"discussion:1"}, AddressedKeys: []string{"discussion:2"},
			DiscussionRevisions: map[string]string{"discussion:1": r1, "discussion:2": r1}},
			map[string]string{"discussion:2": r2},
			discussionClassification{Reopened: []string{"discussion:2"}, Unknown: []string{"discussion:1"}, Hold: reasonReviewStateUnknown, Detail: "discussion:1: not found or in an unrecognized state"}},
		{"new keys are ordered by comment id", State{}, map[string]string{"discussion:12": r1, "discussion:3": r2},
			discussionClassification{New: []string{"discussion:3", "discussion:12"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDiscussion(&tc.state, tc.current); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("classifyDiscussion = %+v, want %+v", got, tc.want)
			}
		})
	}
}
