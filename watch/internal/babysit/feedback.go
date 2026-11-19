package babysit

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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
// keys: AddressedKeys is not a permanent verdict either. Both tick's own
// resolution pass (reconcileFeedback) and the pre-merge boundary
// (revalidateFeedback) reclassify AddressedKeys against fresh GitHub state
// every time, so a
// resolved thread/review GitHub later reports reopened returns to pending
// under its own reasonFeedbackReopened hold, and an addressed key GitHub no
// longer reports at all holds under reasonReviewStateUnknown rather than
// being silently treated as still resolved.
//
// #862 adds an actor check to every one of those GitHub-side signals (actor.go):
// a resolution, dismissal or superseding approval counts only when its actor is
// not the automation login and is the item's own author or has admin/write
// permission. Anything else holds the key pending in State.ActorHeldKeys.

// keyStatus is classifyKey's per-key classification outcome.
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
	// keyActorDenied (#862): GitHub reports the item resolved, but its actor does
	// not pass the actor rule (the automation login, an unknown actor, or a login
	// that neither authored it nor has admin/write). The key stays pending in
	// State.ActorHeldKeys.
	keyActorDenied
	// keyActorLookupFailed (#862): a lookup the actor rule needed failed (the
	// automation login, the dismissal timeline, a permission, or no actorContext at
	// all). Not a denial: the key stays where it was and the next tick retries.
	keyActorLookupFailed
)

// threadState is one review thread as GitHub reports it (#862): whether it is
// resolved, who resolved it (ResolvedByKnown is false for a null resolver, e.g. a
// deleted account) and who wrote its first comment, the thread's own author.
type threadState struct {
	Resolved        bool
	ResolvedBy      string
	ResolvedByKnown bool
	Author          string
}

// feedbackState is the fully-explicit input classifyKey/
// partitionPending need: this tick's GraphQL review-thread resolution
// state (keyed by comment databaseId, matching PendingKeys' "comment:<id>"
// encoding), and this tick's already-fetched reviews payload. Pure and
// I/O-free, mirroring evaluateAutomerge's automergeInputs discipline
// (automerge.go:120-159) so the multi-reviewer/multi-thread matrix is
// testable without any `gh` scripting.
type feedbackState struct {
	Threads map[int64]threadState
	Reviews []review

	// Attestations is State.ActorAttestations (#862); Actors is the lazily-read
	// automation login, dismissal timeline and permission lookups. A nil Actors
	// fails closed: nothing resolves.
	Attestations map[string]actorAttestation
	Actors       *actorContext
	// PR names the supervised PR in the `cenci babysit resolve <pr> <key>` recovery
	// an actor hold's detail carries.
	PR string

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

// keyVerdict is classifyKey's output: the key's status and, for keyActorDenied,
// the actor GitHub reports ("" when it is unknown) and why that actor was denied.
type keyVerdict struct {
	Status keyStatus
	Actor  string
	Why    string
}

// classifyKey classifies one PendingKeys entry against fs, with the actor
// evidence (#862) a caller that records or reports a denial needs. Default-deny
// throughout: a malformed key, an absent thread/review, or a review state
// outside the recognized closed set all classify as keyUnknown, never
// keyResolved.
func classifyKey(key string, fs feedbackState) keyVerdict {
	switch {
	case strings.HasPrefix(key, "comment:"):
		return classifyCommentKey(key, strings.TrimPrefix(key, "comment:"), fs)
	case strings.HasPrefix(key, "review:"):
		return classifyReviewKey(key, strings.TrimPrefix(key, "review:"), fs)
	default:
		return keyVerdict{Status: keyUnsupported}
	}
}

// checkActor is the actor rule (#862) for a resolution of key that GitHub reports
// as made by actor (known is false for a null or unmatched actor, whyUnknown then
// being the denial's reason; verb names what the actor did otherwise). A
// feedbackState with no actorContext fails closed as a lookup failure, never a
// resolution.
func (fs feedbackState) checkActor(key, actor string, known bool, author, whyUnknown, verb string) keyVerdict {
	if fs.Actors == nil {
		return keyVerdict{Status: keyActorLookupFailed}
	}
	var attestation *actorAttestation
	if a, ok := fs.Attestations[key]; ok {
		attestation = &a
	}
	switch classifyActor(fs.Actors, actor, known, author, attestation) {
	case actorAccepted:
		return keyVerdict{Status: keyResolved}
	case actorLookupFailed:
		return keyVerdict{Status: keyActorLookupFailed}
	}
	why := whyUnknown
	if known {
		why = fmt.Sprintf("%s by %s, which is the automation login or lacks write access", verb, actor)
	}
	return keyVerdict{Status: keyActorDenied, Actor: actor, Why: why}
}

// checkDismissal applies checkActor to the dismissal of r, whose actor is read from
// the dismissal timeline (the review's own author is the author the rule accepts).
// A timeline that cannot be read (or an earlier pass failure) is a lookup failure.
func (fs feedbackState) checkDismissal(key string, r review) keyVerdict {
	if fs.Actors == nil || fs.Actors.err != nil {
		return keyVerdict{Status: keyActorLookupFailed}
	}
	actor, known := fs.Actors.dismissalActor(r.ID)
	if fs.Actors.err != nil {
		return keyVerdict{Status: keyActorLookupFailed}
	}
	return fs.checkActor(key, actor, known, r.User.Login, "dismissal actor unknown", "dismissed")
}

// classifyCommentKey classifies a "comment:<id>" key: resolved only when
// GitHub's thread map positively confirms it and the thread's resolver passes the
// actor rule (#862), unknown when the thread map doesn't mention this id at all
// (absence is never proof of resolution).
func classifyCommentKey(key, id string, fs feedbackState) keyVerdict {
	commentID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return keyVerdict{Status: keyUnknown}
	}
	thread, ok := fs.Threads[commentID]
	if !ok {
		return keyVerdict{Status: keyUnknown}
	}
	if !thread.Resolved {
		return keyVerdict{Status: keyPending}
	}
	return fs.checkActor(key, thread.ResolvedBy, thread.ResolvedByKnown, thread.Author, "resolver unknown", "resolved")
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
// (latestEffectiveReview) is APPROVED or DISMISSED. #862: each of those
// resolutions also has to pass the actor rule -- a dismissal through the actor of
// its ReviewDismissedEvent (the review's own author being the author the rule
// accepts), an APPROVED review through its own author (the reviewer, who is also
// the target's author). Any other state --
// including a review ID no longer present in fs.Reviews at all, or a state
// outside {DISMISSED, CHANGES_REQUESTED} (PendingKeys entries are only ever
// created for CHANGES_REQUESTED reviews, so anything else is anomalous) --
// fails closed to keyUnknown rather than guessing. A reviews read that
// cannot be proven complete (fs.ReviewsComplete false, #854's fetchPaged
// completeness signal) fails closed unconditionally, before any of that
// resolution logic runs at all -- an incomplete fetch can never prove a
// reviewer's visible "latest" review really is their latest, regardless of
// how short the fetched slice happens to be.
func classifyReviewKey(key, id string, fs feedbackState) keyVerdict {
	if !fs.ReviewsComplete {
		return keyVerdict{Status: keyUnknown}
	}
	reviewID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return keyVerdict{Status: keyUnknown}
	}
	target := findReview(fs.Reviews, reviewID)
	if target == nil {
		return keyVerdict{Status: keyUnknown}
	}
	if target.State == "DISMISSED" {
		return fs.checkDismissal(key, *target)
	}
	if target.State != "CHANGES_REQUESTED" {
		return keyVerdict{Status: keyUnknown}
	}
	latest, ok := latestEffectiveReview(fs.Reviews, target.User.Login)
	if !ok {
		return keyVerdict{Status: keyUnknown}
	}
	switch latest.State {
	case "APPROVED":
		return fs.checkActor(key, latest.User.Login, latest.User.Login != "", target.User.Login, "approver unknown", "approved")
	case "DISMISSED":
		return fs.checkDismissal(key, latest)
	case "CHANGES_REQUESTED":
		return keyVerdict{Status: keyPending}
	default:
		return keyVerdict{Status: keyUnknown}
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

// partitionPending splits pending by classifyKey's verdict: resolved keys move
// out into Resolved, everything else (pending, unknown, unsupported) stays in
// Pending. A single keyUnknown or keyUnsupported entry poisons the overall hold
// verdict (first one found wins, mirroring evaluateAutomerge's
// first-failing-gate-wins discipline) even while other keys in the same set
// resolve cleanly. Detail names the specific offending key, consistent with
// every other hold reason in this package carrying a diagnostic Detail
// identifying the cause rather than just the reason constant. #862: a key whose
// resolution fails the actor rule stays in Pending too, raising
// reasonResolutionActorUnauthorized (a failed lookup, reasonFeedbackUnreadable)
// the same way, and the result carries the actor facts of every pending key.
func partitionPending(pending []string, fs feedbackState) feedbackClassification {
	var out feedbackClassification
	for _, key := range pending {
		v := classifyKey(key, fs)
		out.noteActor(key, v, fs)
		if v.Status == keyResolved {
			out.Resolved = append(out.Resolved, key)
			continue
		}
		out.Pending = append(out.Pending, key)
		if out.Hold == "" {
			out.Hold, out.Detail = holdFor(key, v, fs.PR)
		}
	}
	return out
}

// holdFor is the classification-level hold a verdict raises: reasonReviewStateUnknown,
// reasonFeedbackUnsupported, reasonResolutionActorUnauthorized (#862, detail naming
// the key, why and its `resolve` recovery) or, for a failed actor lookup that is not
// a denial, reasonFeedbackUnreadable. A resolved or pending key raises none.
func holdFor(key string, v keyVerdict, pr string) (hold, detail string) {
	switch v.Status {
	case keyUnknown:
		return reasonReviewStateUnknown, key + ": not found or in an unrecognized state"
	case keyUnsupported:
		return reasonFeedbackUnsupported, key + ": unsupported key type"
	case keyActorDenied:
		return reasonResolutionActorUnauthorized, key + ": " + v.Why + "; run " + resolveHint(pr, []string{key})
	case keyActorLookupFailed:
		return reasonFeedbackUnreadable, "resolution actor permission lookup failed for " + key + "; will retry"
	}
	return "", ""
}

// feedbackClassification is classifyFeedback's pure output: Pending/Resolved
// mirror partitionPending's outcome for the pending set exactly (today's
// semantics, unchanged), Reopened names every previously-addressed key this
// pass reclassified back to pending (#885, AC 1/AC 2), and Hold/Detail carry
// the single overall verdict for the whole pass -- both sets combined --
// under the plan's exact precedence: a fetch-level hold is decided by the
// caller before classifyFeedback ever runs; within classifyFeedback itself, a
// classification-level hold (reasonReviewStateUnknown / reasonFeedbackUnsupported /
// reasonResolutionActorUnauthorized / a failed actor lookup's reasonFeedbackUnreadable,
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

	// #862: ActorHeld maps each key GitHub reports resolved but whose actor failed
	// the actor rule to the actor login seen ("" for a null or unknown actor).
	// ActorRevoked is the subset that was addressed and moves back to pending.
	// ActorUndecided names keys the pass could not decide an actor verdict for (not
	// reported by GitHub, an unsupported type, a failed lookup): their ActorHeldKeys
	// membership is left as it was. AttestationDrops names attested keys GitHub now
	// reports unresolved.
	ActorHeld        map[string]string
	ActorRevoked     []string
	ActorUndecided   []string
	AttestationDrops []string
}

// noteActor records the #862 actor facts of key's verdict v.
func (c *feedbackClassification) noteActor(key string, v keyVerdict, fs feedbackState) {
	if _, attested := fs.Attestations[key]; attested && v.Status == keyPending {
		c.AttestationDrops = append(c.AttestationDrops, key)
	}
	switch v.Status {
	case keyActorDenied:
		if c.ActorHeld == nil {
			c.ActorHeld = map[string]string{}
		}
		c.ActorHeld[key] = v.Actor
	case keyUnknown, keyUnsupported, keyActorLookupFailed:
		c.ActorUndecided = append(c.ActorUndecided, key)
	}
}

// classifyFeedback generalizes partitionPending to also reclassify
// AddressedKeys against fresh GitHub state (#885): pending-set keys keep
// partitionPending's exact semantics (classifyFeedback delegates to
// partitionPending, so "no addressed keys" is identical to the pre-#885
// behavior); addressed-set keys contribute Reopened on keyPending (a
// previously-resolved thread/review found blocking again), and extend the
// classification-level hold to keyUnknown/keyUnsupported while staying
// addressed either way (Q1's fail-closed extension: absence is never proof
// of resolution for a previously-addressed key either, and it is also never
// proof of a reopen). A keyResolved addressed key is a silent no-op -- it
// stays in AddressedKeys, appearing in none of Pending/Resolved/Reopened.
// #862: an addressed key whose current resolution fails the actor rule is
// ActorRevoked (not Reopened) and raises the actor hold, a classification-level
// hold like unknown; a failed lookup leaves it addressed.
func classifyFeedback(pending, addressed []string, fs feedbackState) feedbackClassification {
	out := partitionPending(pending, fs)

	for _, key := range addressed {
		v := classifyKey(key, fs)
		out.noteActor(key, v, fs)
		switch v.Status {
		case keyResolved:
			// No-op: GitHub still confirms this key resolved, it stays in
			// AddressedKeys untouched.
		case keyPending:
			out.Reopened = append(out.Reopened, key)
			if out.Hold == "" {
				out.Hold = reasonFeedbackReopened
				out.Detail = key + ": review feedback reopened"
			}
		default:
			if v.Status == keyActorDenied {
				out.ActorRevoked = append(out.ActorRevoked, key)
			}
			if out.Hold == "" || out.Hold == reasonFeedbackReopened {
				out.Hold, out.Detail = holdFor(key, v, fs.PR)
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
						// ResolvedBy is a pointer so a null resolver (an unresolved
						// thread, or a deleted account) is detectable (#862).
						ResolvedBy *struct {
							Login string `json:"login"`
						} `json:"resolvedBy"`
						Comments struct {
							TotalCount int `json:"totalCount"`
							Nodes      []struct {
								DatabaseID int64 `json:"databaseId"`
								Author     *struct {
									Login string `json:"login"`
								} `json:"author"`
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
const reviewThreadsQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100,after:$cursor){pageInfo{hasNextPage endCursor}nodes{isResolved resolvedBy{login} comments(first:100){totalCount nodes{databaseId author{login}}}}}}}}`

// fetchReviewThreads fetches every review thread's state for pr in repo
// ("owner/name"), returning a map from each thread's comment databaseId to that
// thread's threadState (#862: isResolved, who resolved it, and the first comment's
// author, which is the thread's own author), merged across every page. Any of: a
// non-2-field-clean decode, a non-empty top-level errors[] envelope, an
// exhausted maxThreadPages with hasNextPage still true, or a per-thread
// comments.totalCount exceeding the fetched node count -- all fail closed,
// never a partial-but-treated-as-complete map.
func fetchReviewThreads(repo, pr string) (map[int64]threadState, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("%s: malformed repo %q, want owner/name", reasonFeedbackUnreadable, repo)
	}
	result := map[int64]threadState{}
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
			state := threadState{Resolved: node.IsResolved}
			if node.ResolvedBy != nil && node.ResolvedBy.Login != "" {
				state.ResolvedBy, state.ResolvedByKnown = node.ResolvedBy.Login, true
			}
			if len(node.Comments.Nodes) > 0 && node.Comments.Nodes[0].Author != nil {
				state.Author = node.Comments.Nodes[0].Author.Login
			}
			for _, c := range node.Comments.Nodes {
				result[c.DatabaseID] = state
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
// itself could not be trusted. #862: the state also carries s's attestations and
// the pass's actorContext, whose reads stay lazy (nothing here calls them).
func fetchFeedbackState(s *State, keys []string, reviews []review, reviewsComplete bool, actors *actorContext) (fs feedbackState, hold, detail string) {
	hasCommentKey := false
	for _, k := range keys {
		if strings.HasPrefix(k, "comment:") {
			hasCommentKey = true
			break
		}
	}

	fs = feedbackState{Reviews: reviews, ReviewsComplete: reviewsComplete, Attestations: s.ActorAttestations, Actors: actors, PR: s.PR}
	if !hasCommentKey {
		return fs, "", ""
	}
	threads, err := fetchReviewThreads(s.Repo, s.PR)
	if err != nil {
		hold, detail = feedbackErrHold(err)
		return fs, hold, detail
	}
	fs.Threads = threads
	return fs, "", ""
}

// feedbackErrHold is the fetch-level hold for a failed (or truncated) read: its
// reason and err's text as the detail.
func feedbackErrHold(err error) (hold, detail string) {
	if errors.Is(err, errFeedbackTruncated) {
		return reasonFeedbackTruncated, err.Error()
	}
	return reasonFeedbackUnreadable, err.Error()
}

// fetchAndClassifyFeedback runs the fetch+classify sequence shared by
// reconcileFeedback (mutating) and revalidateFeedback (read-only, #885): it
// fetches this pass's feedbackState via fetchFeedbackState (the widened lazy
// gate: pending union addressed) and, on success, classifies both
// s.PendingKeys and s.AddressedKeys via classifyFeedback. ok is false only on
// a fetchFeedbackState failure, in which case hold/detail carry that failure
// and c is the zero value -- callers must not classify or mutate anything in
// that case (fail closed). #862: the same holds for a failure of the pass's
// automation-login or dismissal-timeline read (actors.err, sticky once set): it
// is a fetch-level hold, so no key moves in either direction.
func fetchAndClassifyFeedback(s *State, reviews []review, reviewsComplete bool, actors *actorContext) (c feedbackClassification, hold, detail string, ok bool) {
	// #1472: `discussion:` keys are partitioned out ahead of this classifier --
	// they are neither a thread nor a review, never go through review-thread
	// lookup, and would otherwise classify as an unsupported key type. The
	// comment:/review: pass below is byte-identical for a state without them.
	pending, _ := splitDiscussionKeys(s.PendingKeys)
	addressed, _ := splitDiscussionKeys(s.AddressedKeys)
	keys := append(append([]string{}, pending...), addressed...)
	fs, fetchHold, fetchDetail := fetchFeedbackState(s, keys, reviews, reviewsComplete, actors)
	if fetchHold != "" {
		return feedbackClassification{}, fetchHold, fetchDetail, false
	}
	c = classifyFeedback(pending, addressed, fs)
	if actors != nil && actors.err != nil {
		hold, detail = feedbackErrHold(actors.err)
		return feedbackClassification{}, hold, detail, false
	}
	return c, "", "", true
}

// reconcileFeedback is tick's GitHub-authoritative resolution pass (Q6,
// generalized by #885 to reclassify AddressedKeys too; reordered by #897 to
// run before this tick's own new-feedback detection, rather than after --
// see babysit.go's tick for the current call order): it runs
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
// output, which (per partitionPending's unchanged semantics) never
// includes a newly reopened key, so a reopen landing in the same pass a
// resolution happens can never spuriously suppress -- or a reopen alone can
// never spuriously trigger -- the watermark advance. A fetch failure holds
// every key unchanged in both directions (fail closed): reconcileFeedback
// returns before classifying or mutating anything. reviewsComplete is this
// tick's fetchPaged completeness signal (#854) for the reviews read:
// threaded straight into feedbackState so classifyReviewKey fails closed on
// an unproven-complete reviews list regardless of how short it happens to be.
// #862: actors is the pass's lazily-read actor-rule input. A resolution that
// fails the rule leaves its key pending (an addressed key moves back to pending,
// ActorRevoked) and records it, with the actor seen, in State.ActorHeldKeys; a key
// that resolves or reports unresolved again leaves that set, and one the pass
// could not decide keeps its membership. An attestation is dropped whenever its
// key reports unresolved; it is otherwise kept, since an addressed key is
// re-checked every tick.
func reconcileFeedback(s *State, reviews []review, reviewsComplete bool, actors *actorContext) feedbackVerdict {
	c, fetchHold, fetchDetail, ok := fetchAndClassifyFeedback(s, reviews, reviewsComplete, actors)
	if !ok {
		return feedbackVerdict{Hold: fetchHold, Detail: fetchDetail}
	}

	// The discussion keys pass through untouched (reconcileDiscussion owns them).
	_, discussionPending := splitDiscussionKeys(s.PendingKeys)
	s.PendingKeys = append(append(append(append([]string{}, c.Pending...), c.Reopened...), c.ActorRevoked...), discussionPending...)
	if len(c.Resolved) > 0 {
		s.AddressedKeys = append(s.AddressedKeys, c.Resolved...)
		s.LaunchedKeys = removeKeys(s.LaunchedKeys, c.Resolved)
	}
	if len(c.Reopened) > 0 {
		s.AddressedKeys = removeKeys(s.AddressedKeys, c.Reopened)
	}
	if len(c.ActorRevoked) > 0 {
		s.AddressedKeys = removeKeys(s.AddressedKeys, c.ActorRevoked)
	}
	s.ActorHeldKeys = nextActorHeldKeys(s.ActorHeldKeys, c)
	for _, key := range c.AttestationDrops {
		delete(s.ActorAttestations, key)
	}

	if len(c.Pending) == 0 && len(c.Resolved) > 0 && s.PendingCommentAt != "" {
		s.LastCommentAt = s.PendingCommentAt
		s.PendingCommentAt, s.PendingHeadSHA = "", ""
	}
	return feedbackVerdict{Hold: c.Hold, Detail: c.Detail, Reopened: c.Reopened}
}

// nextActorHeldKeys is State.ActorHeldKeys after a pass classified as c: every key
// the pass denied (with the actor it saw), plus the old entries of the keys it could
// not decide. A key that resolved or reports unresolved leaves the set. Nil when
// empty, so the field stays omitted.
func nextActorHeldKeys(old map[string]string, c feedbackClassification) map[string]string {
	held := map[string]string{}
	for _, key := range c.ActorUndecided {
		if actor, wasHeld := old[key]; wasHeld {
			held[key] = actor
		}
	}
	maps.Copy(held, c.ActorHeld)
	if len(held) == 0 {
		return nil
	}
	return held
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
func revalidateFeedback(s *State, reviews []review, reviewsComplete bool, actors *actorContext) (pending []string, verdict feedbackVerdict) {
	c, fetchHold, fetchDetail, ok := fetchAndClassifyFeedback(s, reviews, reviewsComplete, actors)
	if !ok {
		return append([]string{}, s.PendingKeys...), feedbackVerdict{Hold: fetchHold, Detail: fetchDetail}
	}

	pending = append(append(append([]string{}, c.Pending...), c.Reopened...), c.ActorRevoked...)
	return pending, feedbackVerdict{Hold: c.Hold, Detail: c.Detail, Reopened: c.Reopened}
}

// -- ordinary PR discussion (#1472) -------------------------------------------
//
// `discussion:<id>` keys are resolved only by authorized evidence (a receipt
// bound to a launch nonce, or `cenci babysit resolve`), never by GitHub state, so
// they have their own classifier. A key's body revision (State.DiscussionRevisions)
// is what detects an edit.

// isDiscussionKey reports whether key is a `discussion:<id>` feedback key.
func isDiscussionKey(key string) bool { return strings.HasPrefix(key, discussionKeyPrefix) }

// splitDiscussionKeys partitions keys into the non-discussion keys and the
// discussion keys, each in its original order.
func splitDiscussionKeys(keys []string) (other, discussion []string) {
	for _, k := range keys {
		if isDiscussionKey(k) {
			discussion = append(discussion, k)
		} else {
			other = append(other, k)
		}
	}
	return other, discussion
}

// discussionKeyID parses the comment id out of a discussion key (0 when malformed).
func discussionKeyID(key string) int64 {
	id, err := strconv.ParseInt(strings.TrimPrefix(key, discussionKeyPrefix), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// compareDiscussionKeys orders discussion keys by comment id.
func compareDiscussionKeys(a, b string) int {
	return cmp.Compare(discussionKeyID(a), discussionKeyID(b))
}

// discussionClassification is classifyDiscussion's pure output. New are human
// comments never seen; Edited are pending keys whose body changed since last
// seen (their launch episode is over); Reopened are addressed keys whose body
// changed since they were addressed; Unknown are pending keys whose comment is
// gone. Hold/Detail carry the single verdict: Unknown outranks Reopened, as
// classifyFeedback's classification-level holds do.
type discussionClassification struct {
	New, Edited, Reopened, Unknown []string
	Hold, Detail                   string
}

// addressedRevision is the body revision an addressed discussion key was
// addressed at: its receipt's revision, else the last revision seen.
func addressedRevision(s *State, key string) string {
	if r, ok := s.DiscussionReceipts[key]; ok && r.Revision != "" {
		return r.Revision
	}
	return s.DiscussionRevisions[key]
}

// classifyDiscussion classifies s's discussion keys against current, the body
// revision of every human discussion comment GitHub reports now (a key absent
// from current is deleted or excluded). It is pure and read-only. A deleted
// pending key is unknown, never resolved; a deleted addressed key is a no-op
// (resolve only accepts pending keys, so a hold on it would have no recovery,
// Q&A 7).
func classifyDiscussion(s *State, current map[string]string) discussionClassification {
	var out discussionClassification
	known := map[string]bool{}
	_, pending := splitDiscussionKeys(s.PendingKeys)
	_, addressed := splitDiscussionKeys(s.AddressedKeys)
	for _, key := range pending {
		known[key] = true
		cur, ok := current[key]
		switch {
		case !ok:
			out.Unknown = append(out.Unknown, key)
		case cur != s.DiscussionRevisions[key]:
			out.Edited = append(out.Edited, key)
		}
	}
	for _, key := range addressed {
		known[key] = true
		if cur, ok := current[key]; ok && cur != addressedRevision(s, key) {
			out.Reopened = append(out.Reopened, key)
		}
	}
	for key := range current {
		if _, seen := s.DiscussionRevisions[key]; !seen && !known[key] {
			out.New = append(out.New, key)
		}
	}
	slices.SortFunc(out.New, compareDiscussionKeys)

	switch {
	case len(out.Unknown) > 0:
		out.Hold, out.Detail = reasonReviewStateUnknown, out.Unknown[0]+": not found or in an unrecognized state"
	case len(out.Reopened) > 0:
		out.Hold, out.Detail = reasonFeedbackReopened, out.Reopened[0]+": review feedback reopened"
	}
	return out
}

// reconcileDiscussion applies classifyDiscussion onto s: new comments become
// pending keys, an edited pending key leaves LaunchedKeys (a new episode), a
// reopened key moves back to pending (its receipt no longer describes the
// current body), and every recorded revision follows the comment. comments must
// be a complete read: a partial one would turn a missing comment into a
// deletion. changed reports whether anything moved.
func reconcileDiscussion(s *State, comments []discussionComment) (verdict feedbackVerdict, changed bool) {
	current := discussionRevisionMap(comments)
	c := classifyDiscussion(s, current)
	if len(c.New)+len(c.Edited)+len(c.Reopened) > 0 && s.DiscussionRevisions == nil {
		s.DiscussionRevisions = map[string]string{}
	}
	// An edit or reopen also releases a held untrusted key (#1539): the launch
	// trigger looks its author up again this tick, and a key still denied is held
	// again with its untrusted launch count kept.
	for _, key := range c.Reopened {
		s.AddressedKeys = removeKeys(s.AddressedKeys, []string{key})
		s.PendingKeys = append(s.PendingKeys, key)
		s.LaunchedKeys = removeKeys(s.LaunchedKeys, []string{key})
		s.HeldUntrustedKeys = removeKeys(s.HeldUntrustedKeys, []string{key})
		delete(s.DiscussionReceipts, key)
		s.DiscussionRevisions[key] = current[key]
	}
	for _, key := range c.Edited {
		s.LaunchedKeys = removeKeys(s.LaunchedKeys, []string{key})
		s.HeldUntrustedKeys = removeKeys(s.HeldUntrustedKeys, []string{key})
		delete(s.DiscussionReceipts, key)
		s.DiscussionRevisions[key] = current[key]
	}
	for _, key := range c.New {
		s.PendingKeys = append(s.PendingKeys, key)
		s.DiscussionRevisions[key] = current[key]
	}
	return feedbackVerdict{Hold: c.Hold, Detail: c.Detail, Reopened: c.Reopened}, len(c.New)+len(c.Edited)+len(c.Reopened) > 0
}

// revalidateDiscussion is reconcileDiscussion's non-mutating counterpart for the
// pre-merge recheck (merge.go): it never writes onto s and never consumes a
// receipt (Q&A 6). It returns every discussion key that is pending now: the
// recorded pending keys, reopened keys and keys not seen before. A comment that
// appeared, or an edit that landed, after the tick's own read therefore holds the
// merge.
func revalidateDiscussion(s *State, comments []discussionComment) (pending []string, verdict feedbackVerdict) {
	c := classifyDiscussion(s, discussionRevisionMap(comments))
	_, recorded := splitDiscussionKeys(s.PendingKeys)
	pending = append(append(append([]string{}, recorded...), c.Reopened...), c.New...)
	return pending, feedbackVerdict{Hold: c.Hold, Detail: c.Detail, Reopened: c.Reopened}
}

// holdRank orders feedback holds for combineHolds: a classification-level or
// read-level hold outranks a revoked resolution, which outranks the
// ended-without-receipt hold (lowest precedence). The head-move security hold
// (#1539) is named explicitly at the top tier; tick combines it first, so it also
// wins a tie. reasonResolutionActorUnauthorized (#862) takes the default top tier,
// as every classification-level hold does.
func holdRank(hold string) int {
	switch hold {
	case "":
		return 0
	case reasonReviewWorkerEndedWithoutReceipt:
		return 1
	case reasonFeedbackReopened:
		return 2
	case reasonHeadMovedUntrustedReview:
		return 3
	default:
		return 3
	}
}

// combineHolds returns the higher-ranked of two holds; on a tie the first wins,
// so the comment:/review: verdict is never displaced by an equal discussion one.
func combineHolds(a, b feedbackVerdict) feedbackVerdict {
	out := a
	if holdRank(b.Hold) > holdRank(a.Hold) {
		out.Hold, out.Detail = b.Hold, b.Detail
	}
	out.Reopened = append(append([]string{}, a.Reopened...), b.Reopened...)
	if len(out.Reopened) == 0 {
		out.Reopened = nil
	}
	return out
}

// markDiscussionAddressed moves key from pending, launched and held to addressed
// and records rec as its evidence. Shared by an accepted receipt and `resolve`.
func markDiscussionAddressed(s *State, key string, rec discussionReceipt) {
	s.PendingKeys = removeKeys(s.PendingKeys, []string{key})
	s.LaunchedKeys = removeKeys(s.LaunchedKeys, []string{key})
	s.HeldUntrustedKeys = removeKeys(s.HeldUntrustedKeys, []string{key})
	if !slices.Contains(s.AddressedKeys, key) {
		s.AddressedKeys = append(s.AddressedKeys, key)
	}
	if s.DiscussionReceipts == nil {
		s.DiscussionReceipts = map[string]discussionReceipt{}
	}
	s.DiscussionReceipts[key] = rec
}

// receiptedAtCurrentRevision reports whether key has a receipt (any outcome)
// recorded for its current revision.
func receiptedAtCurrentRevision(s *State, key string) bool {
	r, ok := s.DiscussionReceipts[key]
	return ok && r.Revision != "" && r.Revision == s.DiscussionRevisions[key]
}

// unreceiptedDiscussionKeys are the pending discussion keys already launched to
// a worker with no receipt for their current revision: once the worker's window
// has ended they hold under reasonReviewWorkerEndedWithoutReceipt, with no
// relaunch for the same revision, until `cenci babysit resolve` or an edit.
func unreceiptedDiscussionKeys(s *State) []string {
	_, pending := splitDiscussionKeys(s.PendingKeys)
	var out []string
	for _, key := range pending {
		if slices.Contains(s.LaunchedKeys, key) && !receiptedAtCurrentRevision(s, key) {
			out = append(out, key)
		}
	}
	return out
}

// needsHumanDiscussionKeys are the pending discussion keys whose worker reported
// `blocked` or `failed` for the current revision: no relaunch, status
// needs-input, `cenci babysit resolve` names the way out.
func needsHumanDiscussionKeys(s *State) []string {
	_, pending := splitDiscussionKeys(s.PendingKeys)
	var out []string
	for _, key := range pending {
		if !receiptedAtCurrentRevision(s, key) {
			continue
		}
		switch s.DiscussionReceipts[key].Outcome {
		case receiptOutcomeBlocked, receiptOutcomeFailed:
			out = append(out, key)
		}
	}
	return out
}

// resolveHint renders the human recovery for keys, naming at most three.
func resolveHint(pr string, keys []string) string {
	shown := keys
	if len(shown) > 3 {
		shown = shown[:3]
	}
	hints := make([]string, len(shown))
	for i, k := range shown {
		hints[i] = "`cenci babysit resolve " + pr + " " + k + "`"
	}
	out := strings.Join(hints, ", ")
	if len(keys) > len(shown) {
		out += fmt.Sprintf(" (and %d more)", len(keys)-len(shown))
	}
	return out
}

// heldUntrustedNote renders the recovery for the held untrusted keys (#1539): a
// write-access human handles the feedback, then runs `cenci babysit resolve` for
// each discussion key, or resolves each comment:/review: key on GitHub and, #862,
// runs `cenci babysit resolve` for it when it was resolved as the automation login
// (a resolution by that login is held for its actor). Each list names at most
// three keys.
func heldUntrustedNote(pr string, held []string) string {
	other, discussion := splitDiscussionKeys(held)
	var steps []string
	if len(discussion) > 0 {
		steps = append(steps, "run "+resolveHint(pr, discussion))
	}
	for i, k := range other {
		if i == 3 {
			steps = append(steps, fmt.Sprintf("(and %d more on GitHub)", len(other)-3))
			break
		}
		steps = append(steps, "resolve "+k+" on GitHub, then run "+resolveHint(pr, []string{k})+" if it was resolved as the automation login")
	}
	return "untrusted feedback held for a write-access human (its author lacks write permission, or its untrusted launch cap is used up); once handled, " + strings.Join(steps, "; ")
}

// actorHeldNote renders the recovery for the keys held for an unauthorized
// resolution actor (#862), in key order: an attended host human confirms the
// resolution is theirs, then runs `cenci babysit resolve` for each key.
func actorHeldNote(pr string, held map[string]string) string {
	keys := slices.Sorted(maps.Keys(held))
	return "resolution held for an unauthorized actor (the automation login, an unknown actor, or a login without write access); if it was yours, run " + resolveHint(pr, keys)
}

// removeKeys returns a new slice containing every element of keys not
// present in remove -- keys \ remove. Used both here (dropping resolved keys
// out of LaunchedKeys and reopened keys out of AddressedKeys, #885's split of
// resolution truth from launch-dedup bookkeeping) and in babysit.go's tick
// (PendingKeys \ LaunchedKeys, the single per-tick babysit-review launch
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
