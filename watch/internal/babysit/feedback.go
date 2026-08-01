package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// GitHub-authoritative review-feedback resolution (#850). babysit.go's old
// rule cleared every PendingKeys entry the moment the PR's head SHA changed,
// treating any push -- unrelated or a repair attempt -- as proof reviewer
// feedback had been addressed. This file replaces that with a resolution
// pass driven entirely by GitHub's own state: an inline-comment thread clears
// only when GitHub reports it isResolved, and a CHANGES_REQUESTED review
// clears only when it is itself DISMISSED or the same reviewer's latest
// effective review is APPROVED. Anything GitHub does not positively confirm
// -- an absent thread/review, an unrecognized review state, an unsupported
// key type, or an API/parsing failure -- holds indefinitely rather than
// silently resolving (watch/docs/error-handling.md's default-deny rule).
//
// #885 extends this same default-deny discipline to previously-resolved
// keys: AddressedKeys is not a permanent verdict either. Both the end-of-tick
// pass (reconcileFeedback) and the pre-merge boundary (revalidateFeedback)
// reclassify AddressedKeys against fresh GitHub state every time, so a
// resolved thread/review GitHub later reports reopened returns to pending
// under its own reasonFeedbackReopened hold, and an addressed key GitHub no
// longer reports at all holds under reasonReviewStateUnknown rather than
// being silently treated as still resolved.

// keyStatus is classifyPendingKey's per-key classification outcome.
type keyStatus int

const (
	// keyPending: GitHub reports this item still outstanding.
	keyPending keyStatus = iota
	// keyResolved: GitHub positively confirms this item is resolved.
	keyResolved
	// keyUnknown: GitHub does not report this item at all (deleted comment,
	// force-push-purged thread, review ID no longer listed), or its review
	// state falls outside the recognized closed set. Never treated as
	// resolved -- absence is not proof of resolution.
	keyUnknown
	// keyUnsupported: a PendingKeys entry of a type this classifier does not
	// recognize at all (not a "comment:" or "review:" key).
	keyUnsupported
)

// feedbackState is the fully-explicit input classifyPendingKey/
// partitionPendingKeys need: this tick's GraphQL review-thread resolution
// state (keyed by comment databaseId, matching PendingKeys' "comment:<id>"
// encoding), and this tick's already-fetched reviews payload. Pure and
// I/O-free, mirroring evaluateAutomerge's automergeInputs discipline
// (automerge.go:120-159) so the multi-reviewer/multi-thread matrix is
// testable without any `gh` scripting.
type feedbackState struct {
	ThreadResolved map[int64]bool
	Reviews        []review

	// ReviewsComplete records whether this tick's reviews detection read
	// was proven complete via fetchPaged's completeness signal (#854) --
	// replaces the reviewsPageSize length tripwire, which failed closed on
	// any reviews slice at exactly reviewsPageSize length even when that
	// read was genuinely complete. Defaults to the zero value (false) so
	// an un-set fixture fails closed rather than silently resolving.
	ReviewsComplete bool
}

// feedbackVerdict is reconcileFeedback's (tick's mutating pass) and
// revalidateFeedback's (the pre-merge recheck's read-only pass) shared
// output. An empty Hold means clean: no held item. A non-empty Hold is one
// of the five #850/#885 automerge reason constants, layered with optional
// diagnostic Detail -- raw and unsanitized here; evaluateAutomerge's stage 4
// is the single sanitization point once Detail reaches
// automergeInputs.FeedbackDetail (automerge.go's FeedbackHold/FeedbackDetail
// field comment). Reopened (#885) names every previously-addressed key this
// pass just reclassified back to pending, independent of whether Hold itself
// ended up as reasonFeedbackReopened (a classification-level unknown/
// unsupported key elsewhere in the same pass can outrank it in Hold -- see
// classifyFeedback) -- callers that need "did anything reopen at all"
// (tick's own actionable bookkeeping) read this field directly rather than
// string-comparing Hold.
type feedbackVerdict struct {
	Hold     string
	Detail   string
	Reopened []string
}

// classifyPendingKey classifies one PendingKeys entry against fs. Default-
// deny throughout: a malformed key, an absent thread/review, or a review
// state outside the recognized closed set all classify as keyUnknown, never
// keyResolved.
func classifyPendingKey(key string, fs feedbackState) keyStatus {
	switch {
	case strings.HasPrefix(key, "comment:"):
		return classifyCommentKey(strings.TrimPrefix(key, "comment:"), fs)
	case strings.HasPrefix(key, "review:"):
		return classifyReviewKey(strings.TrimPrefix(key, "review:"), fs)
	default:
		return keyUnsupported
	}
}

// classifyCommentKey classifies a "comment:<id>" key: resolved only when
// GitHub's thread map positively confirms it, unknown when the thread map
// doesn't mention this id at all (absence is never proof of resolution).
func classifyCommentKey(id string, fs feedbackState) keyStatus {
	commentID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return keyUnknown
	}
	resolved, ok := fs.ThreadResolved[commentID]
	if !ok {
		return keyUnknown
	}
	if resolved {
		return keyResolved
	}
	return keyPending
}

// reviewsPageSize is GitHub's default REST per_page for the
// repos/:owner/:repo/pulls/:number/reviews fetch -- retained only as the
// exact-page-size fixture size feedback_test.go's matrix uses; classifyReviewKey
// itself no longer compares against it (#854 replaces the length tripwire
// with feedbackState.ReviewsComplete below).
const reviewsPageSize = 30

// classifyReviewKey classifies a "review:<id>" key. A DISMISSED review
// resolves directly -- GitHub rewrites a dismissed review's own state to
// DISMISSED in place, no supersession lookup needed. A CHANGES_REQUESTED
// review resolves only when the same reviewer's latest effective review
// (latestEffectiveReview) is APPROVED or DISMISSED; any other state --
// including a review ID no longer present in fs.Reviews at all, or a state
// outside {DISMISSED, CHANGES_REQUESTED} (PendingKeys entries are only ever
// created for CHANGES_REQUESTED reviews, so anything else is anomalous) --
// fails closed to keyUnknown rather than guessing. A reviews read that
// cannot be proven complete (fs.ReviewsComplete false, #854's fetchPaged
// completeness signal) fails closed unconditionally, before any of that
// resolution logic runs at all -- an incomplete fetch can never prove a
// reviewer's visible "latest" review really is their latest, regardless of
// how short the fetched slice happens to be.
func classifyReviewKey(id string, fs feedbackState) keyStatus {
	if !fs.ReviewsComplete {
		return keyUnknown
	}
	reviewID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return keyUnknown
	}
	target := findReview(fs.Reviews, reviewID)
	if target == nil {
		return keyUnknown
	}
	if target.State == "DISMISSED" {
		return keyResolved
	}
	if target.State != "CHANGES_REQUESTED" {
		return keyUnknown
	}
	latest, ok := latestEffectiveReview(fs.Reviews, target.User.Login)
	if !ok {
		return keyUnknown
	}
	switch latest.State {
	case "APPROVED", "DISMISSED":
		return keyResolved
	case "CHANGES_REQUESTED":
		return keyPending
	default:
		return keyUnknown
	}
}

// findReview returns the review in reviews with the given id, or nil when
// GitHub no longer reports it at all.
func findReview(reviews []review, id int64) *review {
	for i := range reviews {
		if reviews[i].ID == id {
			return &reviews[i]
		}
	}
	return nil
}

// latestEffectiveReview returns the most recently submitted review by login
// whose state is in the effective closed set {APPROVED, CHANGES_REQUESTED,
// DISMISSED} -- COMMENTED and PENDING (and any other state string) are
// ignored, never counted as supersession, per the plan's Assumption 2. Ties
// on SubmittedAt (GitHub returns uniform RFC3339 "Z" timestamps, so a
// same-second double-submission is possible) are broken by the higher
// database ID (#885's AC 4): review IDs are monotonic enough to
// deterministically order same-timestamp reviews, and the result must be
// independent of the order the API happens to return them in.
func latestEffectiveReview(reviews []review, login string) (review, bool) {
	var best review
	found := false
	for _, r := range reviews {
		if r.User.Login != login {
			continue
		}
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		default:
			continue
		}
		if !found || r.SubmittedAt > best.SubmittedAt || (r.SubmittedAt == best.SubmittedAt && r.ID > best.ID) {
			best = r
			found = true
		}
	}
	return best, found
}

// partitionPendingKeys splits pending by classifyPendingKey's verdict:
// resolved keys move out into resolved, everything else (pending, unknown,
// unsupported) stays in stillPending. A single keyUnknown or keyUnsupported
// entry poisons the overall hold verdict (first one found wins, mirroring
// evaluateAutomerge's first-failing-gate-wins discipline) even while other
// keys in the same set resolve cleanly. detail names the specific offending
// key, consistent with every other hold reason in this package carrying a
// diagnostic Detail identifying the cause rather than just the reason
// constant.
func partitionPendingKeys(pending []string, fs feedbackState) (stillPending, resolved []string, hold, detail string) {
	for _, key := range pending {
		status := classifyPendingKey(key, fs)
		if status == keyResolved {
			resolved = append(resolved, key)
			continue
		}
		stillPending = append(stillPending, key)
		if hold != "" {
			continue
		}
		switch status {
		case keyUnknown:
			hold = reasonReviewStateUnknown
			detail = key + ": not found or in an unrecognized state"
		case keyUnsupported:
			hold = reasonFeedbackUnsupported
			detail = key + ": unsupported key type"
		}
	}
	return stillPending, resolved, hold, detail
}

// feedbackClassification is classifyFeedback's pure output: Pending/Resolved
// mirror partitionPendingKeys' outcome for the pending set exactly (today's
// semantics, unchanged), Reopened names every previously-addressed key this
// pass reclassified back to pending (#885, AC 1/AC 2), and Hold/Detail carry
// the single overall verdict for the whole pass -- both sets combined --
// under the plan's exact precedence: a fetch-level hold is decided by the
// caller before classifyFeedback ever runs; within classifyFeedback itself, a
// classification-level hold (reasonReviewStateUnknown / reasonFeedbackUnsupported,
// pending set scanned before addressed) always outranks reasonFeedbackReopened,
// which itself only wins when nothing in either set is unknown/unsupported.
// Deterministic output order: Pending/Resolved in original pending order,
// Reopened in original addressed order.
type feedbackClassification struct {
	Pending  []string
	Resolved []string
	Reopened []string
	Hold     string
	Detail   string
}

// classifyFeedback generalizes partitionPendingKeys to also reclassify
// AddressedKeys against fresh GitHub state (#885): pending-set keys keep
// partitionPendingKeys' exact semantics (classifyFeedback delegates to it
// directly, so "no addressed keys" is byte-for-byte identical to the pre-#885
// behavior); addressed-set keys contribute Reopened on keyPending (a
// previously-resolved thread/review found blocking again), and extend the
// classification-level hold to keyUnknown/keyUnsupported while staying
// addressed either way (Q1's fail-closed extension: absence is never proof
// of resolution for a previously-addressed key either, and it is also never
// proof of a reopen). A keyResolved addressed key is a silent no-op -- it
// stays in AddressedKeys, appearing in none of Pending/Resolved/Reopened.
func classifyFeedback(pending, addressed []string, fs feedbackState) feedbackClassification {
	stillPending, resolved, hold, detail := partitionPendingKeys(pending, fs)
	out := feedbackClassification{Pending: stillPending, Resolved: resolved, Hold: hold, Detail: detail}

	for _, key := range addressed {
		switch classifyPendingKey(key, fs) {
		case keyResolved:
			// No-op: GitHub still confirms this key resolved, it stays in
			// AddressedKeys untouched.
		case keyPending:
			out.Reopened = append(out.Reopened, key)
			if out.Hold == "" {
				out.Hold = reasonFeedbackReopened
				out.Detail = key + ": review feedback reopened"
			}
		case keyUnknown:
			if out.Hold == "" || out.Hold == reasonFeedbackReopened {
				out.Hold = reasonReviewStateUnknown
				out.Detail = key + ": not found or in an unrecognized state"
			}
		case keyUnsupported:
			if out.Hold == "" || out.Hold == reasonFeedbackReopened {
				out.Hold = reasonFeedbackUnsupported
				out.Detail = key + ": unsupported key type"
			}
		}
	}
	return out
}

// maxThreadPages bounds fetchReviewThreads' cursor loop: hasNextPage still
// true after this many pages fails closed (errFeedbackTruncated) rather than
// silently treating a partial traversal as complete.
const maxThreadPages = 10

// errFeedbackTruncated is the sentinel for an incomplete GraphQL traversal --
// either the page cap was exhausted with hasNextPage still true, or a
// thread's comments.totalCount exceeds the node count actually returned.
// Both are "the read is incomplete", distinct from errFeedbackUnreadable's
// "the read itself failed" (watch/docs/error-handling.md #446: distinct
// failure classes, direct errors.Is assertion per #412).
var errFeedbackTruncated = errors.New("review feedback state truncated")

// graphQLThreadsResponse is the `gh api graphql` response envelope for the
// reviewThreads query. Errors is checked explicitly and separately from Data
// -- gh api graphql returns HTTP 200 with partial/zero-value Data on a field
// error, which a decode-succeeded check alone would treat as empty-but-valid
// (the #822 trap in a new guise: error and benign must be distinct
// branches).
type graphQLThreadsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						IsResolved bool `json:"isResolved"`
						Comments   struct {
							TotalCount int `json:"totalCount"`
							Nodes      []struct {
								DatabaseID int64 `json:"databaseId"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// reviewThreadsQuery is the GraphQL query fetchReviewThreads sends, one page
// at a time via an explicit cursor loop (never `gh api --paginate`, which
// emits concatenated JSON documents ghJSON's single json.Unmarshal cannot
// parse, and which hides the page count that "incomplete traversal" needs to
// observe).
const reviewThreadsQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100,after:$cursor){pageInfo{hasNextPage endCursor}nodes{isResolved comments(first:100){totalCount nodes{databaseId}}}}}}}`

// fetchReviewThreads fetches every review thread's isResolved state for pr in
// repo ("owner/name"), returning a map from each thread's comment databaseId
// to that thread's isResolved value, merged across every page. Any of: a
// non-2-field-clean decode, a non-empty top-level errors[] envelope, an
// exhausted maxThreadPages with hasNextPage still true, or a per-thread
// comments.totalCount exceeding the fetched node count -- all fail closed,
// never a partial-but-treated-as-complete map.
func fetchReviewThreads(repo, pr string) (map[int64]bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("%s: malformed repo %q, want owner/name", reasonFeedbackUnreadable, repo)
	}
	result := map[int64]bool{}
	cursor := ""
	for page := 0; page < maxThreadPages; page++ {
		args := []string{"api", "graphql",
			"-f", "query=" + reviewThreadsQuery,
			"-f", "owner=" + owner,
			"-f", "name=" + name,
			"-F", "number=" + pr,
		}
		// Only pass cursor when non-empty: an explicit "" would send the
		// GraphQL variable as an empty string rather than omitting it
		// (which resolves to null, reviewThreads' after:$cursor's intended
		// "start from the first page" value).
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		stdout, stderr, err := execGh(args...)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", reasonFeedbackUnreadable, strings.TrimSpace(stderr), err)
		}
		var resp graphQLThreadsResponse
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			return nil, fmt.Errorf("%s: %s", reasonFeedbackUnreadable, strings.TrimSpace(stdout))
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("%s: %s", reasonFeedbackUnreadable, resp.Errors[0].Message)
		}
		threads := resp.Data.Repository.PullRequest.ReviewThreads
		for _, node := range threads.Nodes {
			if node.Comments.TotalCount != len(node.Comments.Nodes) {
				return nil, fmt.Errorf("%w: thread reports %d comments but only %d were returned", errFeedbackTruncated, node.Comments.TotalCount, len(node.Comments.Nodes))
			}
			for _, c := range node.Comments.Nodes {
				result[c.DatabaseID] = node.IsResolved
			}
		}
		if !threads.PageInfo.HasNextPage {
			return result, nil
		}
		cursor = threads.PageInfo.EndCursor
	}
	return nil, fmt.Errorf("%w: exceeded maxThreadPages (%d) with more pages still reported", errFeedbackTruncated, maxThreadPages)
}

// fetchFeedbackState builds this pass's feedbackState: the lazy GraphQL
// thread fetch only runs when keys (pending union addressed, per #885's Q3 --
// widened from pending-only) holds at least one "comment:"-prefixed entry,
// since "review:"-prefixed entries resolve entirely from reviews, already
// fetched by the caller. hold/detail are non-empty only on a
// fetchReviewThreads failure (reasonFeedbackUnreadable/reasonFeedbackTruncated);
// the caller must not classify or mutate anything when hold != "" -- the read
// itself could not be trusted.
func fetchFeedbackState(repo, pr string, keys []string, reviews []review, reviewsComplete bool) (fs feedbackState, hold, detail string) {
	hasCommentKey := false
	for _, k := range keys {
		if strings.HasPrefix(k, "comment:") {
			hasCommentKey = true
			break
		}
	}

	fs = feedbackState{Reviews: reviews, ReviewsComplete: reviewsComplete}
	if !hasCommentKey {
		return fs, "", ""
	}
	threads, err := fetchReviewThreads(repo, pr)
	if err != nil {
		hold = reasonFeedbackUnreadable
		if errors.Is(err, errFeedbackTruncated) {
			hold = reasonFeedbackTruncated
		}
		return fs, hold, err.Error()
	}
	fs.ThreadResolved = threads
	return fs, "", ""
}

// fetchAndClassifyFeedback runs the fetch+classify sequence shared by
// reconcileFeedback (mutating) and revalidateFeedback (read-only, #885): it
// fetches this pass's feedbackState via fetchFeedbackState (the widened lazy
// gate: pending union addressed) and, on success, classifies both
// s.PendingKeys and s.AddressedKeys via classifyFeedback. ok is false only on
// a fetchFeedbackState failure, in which case hold/detail carry that failure
// and c is the zero value -- callers must not classify or mutate anything in
// that case (fail closed).
func fetchAndClassifyFeedback(s *State, reviews []review, reviewsComplete bool) (c feedbackClassification, hold, detail string, ok bool) {
	keys := append(append([]string{}, s.PendingKeys...), s.AddressedKeys...)
	fs, fetchHold, fetchDetail := fetchFeedbackState(s.Repo, s.PR, keys, reviews, reviewsComplete)
	if fetchHold != "" {
		return feedbackClassification{}, fetchHold, fetchDetail, false
	}
	return classifyFeedback(s.PendingKeys, s.AddressedKeys, fs), "", "", true
}

// reconcileFeedback is the end-of-tick, GitHub-authoritative resolution pass
// (Q6, generalized by #885 to reclassify AddressedKeys too): it runs
// fetchAndClassifyFeedback and applies the verdict onto State -- resolved
// keys move PendingKeys -> AddressedKeys and drop out of LaunchedKeys (their
// episode is over, #885's split of resolution truth from launch dedup);
// reopened keys move AddressedKeys -> PendingKeys (LaunchedKeys is left
// alone: a reopened key was already dropped from LaunchedKeys when it
// originally resolved, so it starts its new episode unlaunched and tick's
// own PendingKeys \ LaunchedKeys launch trigger picks it up again).
// LastCommentAt advances (clearing PendingCommentAt/PendingHeadSHA) only once
// every key pending at this pass's *start* has cleared and this pass
// actually resolved something -- scoped via classifyFeedback's Pending
// output, which (per partitionPendingKeys' unchanged semantics) never
// includes a newly reopened key, so a reopen landing in the same pass a
// resolution happens can never spuriously suppress -- or a reopen alone can
// never spuriously trigger -- the watermark advance. A fetch failure holds
// every key unchanged in both directions (fail closed): reconcileFeedback
// returns before classifying or mutating anything. reviewsComplete is this
// tick's fetchPaged completeness signal (#854) for the reviews read:
// threaded straight into feedbackState so classifyReviewKey fails closed on
// an unproven-complete reviews list regardless of how short it happens to be.
func reconcileFeedback(s *State, reviews []review, reviewsComplete bool) feedbackVerdict {
	c, fetchHold, fetchDetail, ok := fetchAndClassifyFeedback(s, reviews, reviewsComplete)
	if !ok {
		return feedbackVerdict{Hold: fetchHold, Detail: fetchDetail}
	}

	s.PendingKeys = append(append([]string{}, c.Pending...), c.Reopened...)
	if len(c.Resolved) > 0 {
		s.AddressedKeys = append(s.AddressedKeys, c.Resolved...)
		s.LaunchedKeys = removeKeys(s.LaunchedKeys, c.Resolved)
	}
	if len(c.Reopened) > 0 {
		s.AddressedKeys = removeKeys(s.AddressedKeys, c.Reopened)
	}

	if len(c.Pending) == 0 && len(c.Resolved) > 0 && s.PendingCommentAt != "" {
		s.LastCommentAt = s.PendingCommentAt
		s.PendingCommentAt, s.PendingHeadSHA = "", ""
	}
	return feedbackVerdict{Hold: c.Hold, Detail: c.Detail, Reopened: c.Reopened}
}

// revalidateFeedback is reconcileFeedback's non-mutating counterpart for the
// pre-merge boundary (merge.go's recheckAutomergeInputs, Decision 9, #885):
// it runs the same fetchAndClassifyFeedback pass as reconcileFeedback -- same
// widened lazy gate, same classification of both s.PendingKeys and
// s.AddressedKeys -- but never writes back onto s, since the recheck must not
// double-book State (#854's rejected "reuse the mutating reconcile"
// alternative still holds -- merge.go's own doc comment). Returns the
// revalidated pending set (still-pending plus newly-reopened keys, in
// classifyFeedback's deterministic order) for the caller to fold in alongside
// its own new-feedback detection, plus the verdict. On a fetch failure the
// returned pending set falls back to s.PendingKeys unchanged (fail closed --
// the caller is expected to route verdict.Hold into its own FeedbackHold
// rather than treat this as success).
func revalidateFeedback(s *State, reviews []review, reviewsComplete bool) (pending []string, verdict feedbackVerdict) {
	c, fetchHold, fetchDetail, ok := fetchAndClassifyFeedback(s, reviews, reviewsComplete)
	if !ok {
		return append([]string{}, s.PendingKeys...), feedbackVerdict{Hold: fetchHold, Detail: fetchDetail}
	}

	pending = append(append([]string{}, c.Pending...), c.Reopened...)
	return pending, feedbackVerdict{Hold: c.Hold, Detail: c.Detail, Reopened: c.Reopened}
}

// removeKeys returns a new slice containing every element of keys not
// present in remove -- keys \ remove. Used both here (dropping resolved keys
// out of LaunchedKeys and reopened keys out of AddressedKeys, #885's split of
// resolution truth from launch-dedup bookkeeping) and in babysit.go's tick
// (PendingKeys \ LaunchedKeys, the single per-tick address-review launch
// trigger). keys itself is never mutated in place.
func removeKeys(keys, remove []string) []string {
	if len(remove) == 0 {
		return keys
	}
	drop := make(map[string]bool, len(remove))
	for _, k := range remove {
		drop[k] = true
	}
	var out []string
	for _, k := range keys {
		if !drop[k] {
			out = append(out, k)
		}
	}
	return out
}
