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
// classifyPendingKey / latestEffectiveReview / partitionPendingKeys are pure,
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
			feedbackState{ThreadResolved: map[int64]bool{5: true}},
			keyResolved,
		},
		{
			"unresolved-thread",
			"comment:5",
			feedbackState{ThreadResolved: map[int64]bool{5: false}},
			keyPending,
		},
		{
			"absent-thread: GitHub no longer reports this thread at all (deleted comment, force-push purge) -- never treated as resolved",
			"comment:5",
			feedbackState{ThreadResolved: map[int64]bool{6: true}},
			keyUnknown,
		},
		{
			"dismissed-review: GitHub rewrites a dismissed review's own state to DISMISSED in place",
			"review:10",
			feedbackState{Reviews: []review{{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}}},
			keyResolved,
		},
		{
			"reviewer-later-approved: same reviewer's newer review supersedes the blocking one",
			"review:10",
			feedbackState{Reviews: []review{
				{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
				{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
			}},
			keyResolved,
		},
		{
			"reviewer-later-changes-requested: still blocking, no supersession",
			"review:10",
			feedbackState{Reviews: []review{
				{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
				{ID: 12, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
			}},
			keyPending,
		},
		{
			"absent-review: GitHub no longer reports this review ID at all",
			"review:10",
			feedbackState{Reviews: []review{{ID: 55, State: "APPROVED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("bob")}}},
			keyUnknown,
		},
		{
			"unknown-state-string: a review state outside {APPROVED, CHANGES_REQUESTED, DISMISSED, COMMENTED, PENDING}",
			"review:10",
			feedbackState{Reviews: []review{{ID: 10, State: "REQUEST_CHANGES", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")}}},
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
			got := classifyPendingKey(tc.key, tc.fs)
			if got != tc.want {
				t.Fatalf("classifyPendingKey(%q) = %v, want %v", tc.key, got, tc.want)
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
	got := classifyPendingKey("review:10", fs)
	if got != keyUnknown {
		t.Fatalf("classifyPendingKey(review:10) = %v, want keyUnknown: a reviews list at the page-size cap can never be trusted as complete, regardless of what the visible data suggests", got)
	}

	_, resolved, hold, _ := partitionPendingKeys([]string{"review:10"}, fs)
	if len(resolved) != 0 {
		t.Fatalf("resolved = %v, want empty: the possibly-truncated list must not resolve review:10", resolved)
	}
	if hold != reasonReviewStateUnknown {
		t.Fatalf("hold = %q, want %q", hold, reasonReviewStateUnknown)
	}
}

// TestClassifyReviewKeyResolvesNormallyBelowPageSize is the regression
// counterpart: a reviews slice below reviewsPageSize is unaffected by the
// truncation guard and resolves exactly as before.
func TestClassifyReviewKeyResolvesNormallyBelowPageSize(t *testing.T) {
	reviews := []review{
		{ID: 10, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("alice")},
		{ID: 11, State: "APPROVED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")},
	}
	if len(reviews) >= reviewsPageSize {
		t.Fatalf("test setup: len(reviews) = %d, want below reviewsPageSize (%d)", len(reviews), reviewsPageSize)
	}
	got := classifyPendingKey("review:10", feedbackState{Reviews: reviews})
	if got != keyResolved {
		t.Fatalf("classifyPendingKey(review:10) = %v, want keyResolved: below the page-size cap, the reviewer's later APPROVED still supersedes normally", got)
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
		ThreadResolved: map[int64]bool{1: true, 2: false},
		Reviews: []review{
			{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("bob")},
			{ID: 11, State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:00:00Z", User: login("carol")},
		},
	}
	stillPending, resolved, hold, detail := partitionPendingKeys(pending, fs)

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
	stillPending, resolved, hold, detail := partitionPendingKeys([]string{"label:1"}, feedbackState{})
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

// scriptedGraphQLCommand stubs the command seam to serve bodies in order for
// every invocation (always a nil error -- a non-nil `gh` exit combined with a
// still-parseable body is exercised separately, mirroring ghJSON's tolerant
// contract; these tests focus on the GraphQL-specific failure shapes), and
// records every call into calls.
func scriptedGraphQLCommand(t *testing.T, bodies []string, calls *[][]string) {
	t.Helper()
	original := command
	i := 0
	command = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if i >= len(bodies) {
			t.Fatalf("unexpected extra command call: %s %v", name, args)
		}
		out := bodies[i]
		i++
		return []byte(out), nil
	}
	t.Cleanup(func() { command = original })
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
	want := map[int64]bool{100: false, 200: true}
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
		original := command
		command = func(name string, args ...string) ([]byte, error) {
			return []byte("authentication required"), errors.New("exit status 1")
		}
		t.Cleanup(func() { command = original })

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

// -- reconcilePendingFeedback: the lazy GraphQL gate (#850) -------------------

// TestReconcileSkipsGraphQLWhenOnlyReviewKeysPending pins the lazy gate: a
// PendingKeys set containing only "review:"-prefixed keys must resolve
// entirely from the reviews payload already fetched this tick and must issue
// zero GraphQL calls -- asserted on recorded calls, not just the outcome.
func TestReconcileSkipsGraphQLWhenOnlyReviewKeysPending(t *testing.T) {
	var calls [][]string
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, fmt.Errorf("unexpected command: %s %v", name, args)
	}
	t.Cleanup(func() { command = original })

	s := &State{Repo: "o/r", PR: "42", PendingKeys: []string{"review:10"}, PendingCommentAt: "2026-01-01T00:00:00Z", PendingHeadSHA: "abc"}
	reviews := []review{{ID: 10, State: "DISMISSED", SubmittedAt: "2026-01-02T00:00:00Z", User: login("alice")}}

	verdict := reconcilePendingFeedback(s, reviews)

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
// well-formed case), reconcilePendingFeedback must not touch LastCommentAt
// at all -- only actually resolving something this tick (len(resolved) > 0)
// may advance it, preventing a stray/inconsistent PendingCommentAt from
// silently overwriting LastCommentAt when nothing was reconciled.
func TestReconcileNoPendingKeysLeavesLastCommentAtUnchanged(t *testing.T) {
	s := &State{Repo: "o/r", PR: "42", LastCommentAt: "2025-12-01T00:00:00Z"}
	verdict := reconcilePendingFeedback(s, nil)
	if verdict.Hold != "" {
		t.Fatalf("verdict.Hold = %q, want empty", verdict.Hold)
	}
	if s.LastCommentAt != "2025-12-01T00:00:00Z" {
		t.Fatalf("LastCommentAt = %q, want unchanged: nothing was pending or resolved this tick", s.LastCommentAt)
	}
}
