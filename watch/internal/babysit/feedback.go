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
}

// feedbackVerdict is reconcilePendingFeedback's output. An empty Hold means
// clean: no held item. A non-empty Hold is one of the four #850 automerge
// reason constants, layered with optional diagnostic Detail -- raw and
// unsanitized here; evaluateAutomerge's stage 4 is the single sanitization
// point once Detail reaches automergeInputs.FeedbackDetail (automerge.go's
// FeedbackHold/FeedbackDetail field comment).
type feedbackVerdict struct {
	Hold   string
	Detail string
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
// repos/:owner/:repo/pulls/:number/reviews fetch babysit.go's tick() makes
// every tick -- that read does not paginate (full pagination is #854's
// scope), so a reviews slice at exactly this length cannot be proven
// complete. A stale APPROVED visible in the fetched page could otherwise be
// misread as a reviewer's latest word while a genuinely newer, still-
// blocking CHANGES_REQUESTED sits on an unfetched page. This is only a
// fail-closed tripwire, not real completeness proof: a fetch that happens to
// return exactly reviewsPageSize reviews and is actually complete also fails
// closed, which is the deliberately conservative tradeoff (#850 security
// review).
const reviewsPageSize = 30

// classifyReviewKey classifies a "review:<id>" key. A DISMISSED review
// resolves directly -- GitHub rewrites a dismissed review's own state to
// DISMISSED in place, no supersession lookup needed. A CHANGES_REQUESTED
// review resolves only when the same reviewer's latest effective review
// (latestEffectiveReview) is APPROVED or DISMISSED; any other state --
// including a review ID no longer present in fs.Reviews at all, or a state
// outside {DISMISSED, CHANGES_REQUESTED} (PendingKeys entries are only ever
// created for CHANGES_REQUESTED reviews, so anything else is anomalous) --
// fails closed to keyUnknown rather than guessing. A possibly-truncated
// fs.Reviews (reviewsPageSize) fails closed unconditionally, before any of
// that resolution logic runs at all -- an unpaginated fetch can never prove
// a reviewer's visible "latest" review really is their latest.
func classifyReviewKey(id string, fs feedbackState) keyStatus {
	if len(fs.Reviews) >= reviewsPageSize {
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
// ignored, never counted as supersession, per the plan's Assumption 2.
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
		if !found || r.SubmittedAt > best.SubmittedAt {
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
		out, err := command("gh", args...)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", reasonFeedbackUnreadable, strings.TrimSpace(string(out)), err)
		}
		var resp graphQLThreadsResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			return nil, fmt.Errorf("%s: %s", reasonFeedbackUnreadable, strings.TrimSpace(string(out)))
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

// reconcilePendingFeedback is the end-of-tick, GitHub-authoritative
// resolution pass (Q6): it lazily re-fetches review-thread state -- only when
// s.PendingKeys holds at least one "comment:"-prefixed entry, since
// "review:"-prefixed entries resolve entirely from reviews, already fetched
// this tick -- partitions s.PendingKeys accordingly, moves resolved keys to
// s.AddressedKeys, and advances s.LastCommentAt from s.PendingCommentAt
// (clearing PendingCommentAt/PendingHeadSHA) only once the pending set fully
// empties. A fetchReviewThreads failure holds every comment: key unchanged
// (fail closed) and never touches AddressedKeys/LastCommentAt for this tick.
func reconcilePendingFeedback(s *State, reviews []review) feedbackVerdict {
	hasCommentKey := false
	for _, k := range s.PendingKeys {
		if strings.HasPrefix(k, "comment:") {
			hasCommentKey = true
			break
		}
	}

	fs := feedbackState{Reviews: reviews}
	if hasCommentKey {
		threads, err := fetchReviewThreads(s.Repo, s.PR)
		if err != nil {
			hold := reasonFeedbackUnreadable
			if errors.Is(err, errFeedbackTruncated) {
				hold = reasonFeedbackTruncated
			}
			return feedbackVerdict{Hold: hold, Detail: err.Error()}
		}
		fs.ThreadResolved = threads
	}

	stillPending, resolved, hold, detail := partitionPendingKeys(s.PendingKeys, fs)
	s.PendingKeys = stillPending
	if len(resolved) > 0 {
		s.AddressedKeys = append(s.AddressedKeys, resolved...)
	}
	// Advance only when the pending set fully empties *and* this tick
	// actually resolved something -- guards the edge case where PendingKeys
	// was already empty (nothing to reconcile at all) from silently
	// overwriting LastCommentAt with a stray, inconsistent PendingCommentAt.
	if len(s.PendingKeys) == 0 && len(resolved) > 0 && s.PendingCommentAt != "" {
		s.LastCommentAt = s.PendingCommentAt
		s.PendingCommentAt, s.PendingHeadSHA = "", ""
	}
	return feedbackVerdict{Hold: hold, Detail: detail}
}
