package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// graphQLQueueResponse is the `gh api graphql` response envelope for the
// merge-queue probe. Errors is checked explicitly and separately from Data,
// mirroring feedback.go's graphQLThreadsResponse (#822: a 200 response with
// a non-empty errors[] must never be treated as empty-but-valid data).
// IsInMergeQueue/IsMergeQueueEnabled are pointer fields so a GraphQL field
// genuinely absent from the response is distinguishable from an explicit
// false (dispatch/config.go:102-123's absent-vs-false pattern).
type graphQLQueueResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				IsInMergeQueue      *bool  `json:"isInMergeQueue"`
				IsMergeQueueEnabled *bool  `json:"isMergeQueueEnabled"`
				HeadRefOID          string `json:"headRefOid"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// mergeQueueQuery also fetches headRefOid alongside the two merge-queue
// fields, but that field is not currently consumed for the pre-merge
// head-SHA re-check (Decision 9): recheckAutomergeInputs discards it and
// instead sources the freshest head SHA from its own `gh pr view` re-fetch
// (merge.go's own prView.HeadRefOID), so a push landing between the first
// evaluation and this point is still caught before `gh pr merge` is ever
// attempted.
const mergeQueueQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){isInMergeQueue isMergeQueueEnabled headRefOid}}}`

// recheckAutomergeInputs re-fetches every input the pre-merge re-evaluation
// (Decision 9, #854) requires, immediately before runAutomerge ever issues
// `gh pr merge` on a first-pass Merge==true verdict: PR view (head SHA,
// mergeable, changed files), checks, closing-issue labels, base-ref policy,
// paginated comments/reviews (feeding a read-only new-feedback detection
// pass via detectNewFeedbackKeys, never reconcilePendingFeedback -- see the
// plan's rejected alternative), and merge-queue state. Enabled and
// AllowedMethods carry over unchanged from first (repo settings / local
// config, not PR state, per the plan's Assumption); FeedbackHold/
// FeedbackDetail also carry over from first (a first-pass Merge==true
// verdict already proved they were clean, and re-running #850's GraphQL
// thread-resolution read here would risk double-bookkeeping -- see the
// plan's rejected alternative). On any read failure, returns a distinct
// reasonUpstream*Unreadable reason for the caller to hold under (reusing
// the existing one-decision-per-tick constants, per the plan's Assumption)
// rather than a new parallel constant set.
func recheckAutomergeInputs(s *State, first automergeInputs) (automergeInputs, string, error) {
	var pr prView
	if err := ghJSON(&pr, "pr", "view", s.PR, "--repo", s.Repo, "--json", prViewFields); err != nil {
		return automergeInputs{}, reasonUpstreamPRUnreadable, err
	}
	var checks []check
	if err := ghJSON(&checks, "pr", "checks", s.PR, "--repo", s.Repo, "--json", "bucket,name,state"); err != nil {
		return automergeInputs{}, reasonUpstreamChecksUnreadable, err
	}
	comments, commentsComplete, err := fetchPaged[comment]("repos/" + s.Repo + "/pulls/" + s.PR + "/comments")
	if err != nil {
		return automergeInputs{}, reasonUpstreamCommentsUnreadable, err
	}
	reviews, reviewsComplete, err := fetchPaged[review]("repos/" + s.Repo + "/pulls/" + s.PR + "/reviews")
	if err != nil {
		return automergeInputs{}, reasonUpstreamReviewsUnreadable, err
	}

	in := automergeInputs{
		Enabled:           first.Enabled,
		Checks:            checks,
		RepairPending:     s.RepairPending,
		FeedbackHold:      first.FeedbackHold,
		FeedbackDetail:    first.FeedbackDetail,
		IsDraft:           pr.IsDraft,
		Mergeable:         pr.Mergeable,
		HeadRefOID:        pr.HeadRefOID,
		ChangedFiles:      pr.ChangedFiles,
		Additions:         pr.Additions,
		Deletions:         pr.Deletions,
		CommentsComplete:  commentsComplete,
		ReviewsComplete:   reviewsComplete,
		AllowedMethods:    first.AllowedMethods,
		AllowedMethodsErr: first.AllowedMethodsErr,
	}
	for _, i := range pr.ClosingIssuesReferences {
		in.ClosingIssues = append(in.ClosingIssues, i.Number)
	}
	for _, f := range pr.Files {
		in.Files = append(in.Files, f.Path)
	}
	if len(in.ClosingIssues) > 0 {
		in.IssueLabels, in.LabelsErr = fetchClosingIssueLabels(s.Repo, in.ClosingIssues)
	}

	newKeys, _ := detectNewFeedbackKeys(s, comments, reviews)
	in.PendingKeys = append(append([]string{}, s.PendingKeys...), newKeys...)

	cfg, err := fetchPolicy(s.Repo, pr.BaseRefName)
	switch {
	case err != nil:
		in.PolicyErr = err
	default:
		if policy, reason := resolvePolicy(cfg, in.Files); reason != "" {
			in.PolicyReason = reason
		} else {
			in.Policy = &policy
			in.MergeMethod = policy.MergeMethod
		}
	}

	inQueue, enabled, _, queueErr := fetchMergeQueueState(s.Repo, s.PR)
	in.QueueInMergeQueue = inQueue
	in.QueueEnabled = enabled
	in.QueueErr = queueErr
	in.QueueProbed = true

	return in, "", nil
}

// fetchMergeQueueState probes pr's merge-queue state via `gh api graphql`
// (Decision 4, Q3): gh pr view exposes no queue fields on this gh version,
// so this is the only signal. headRefOID is the PR's current head commit as
// reported by this same query, but it is not currently used by
// recheckAutomergeInputs's pre-merge re-evaluation -- that function sources
// its freshest head-SHA signal from its own `gh pr view` re-fetch instead.
func fetchMergeQueueState(repo, pr string) (inQueue, enabled *bool, headRefOID string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, nil, "", fmt.Errorf("malformed repo %q, want owner/name", repo)
	}
	stdout, stderr, err := execGh("api", "graphql",
		"-f", "query="+mergeQueueQuery,
		"-f", "owner="+owner,
		"-f", "name="+name,
		"-F", "number="+pr,
	)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", strings.TrimSpace(stderr), err)
	}
	var resp graphQLQueueResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		return nil, nil, "", fmt.Errorf("decode merge-queue probe: %s", strings.TrimSpace(stdout))
	}
	if len(resp.Errors) > 0 {
		return nil, nil, "", errors.New(resp.Errors[0].Message)
	}
	pull := resp.Data.Repository.PullRequest
	return pull.IsInMergeQueue, pull.IsMergeQueueEnabled, pull.HeadRefOID, nil
}

// executeMerge issues `gh pr merge <pr> --repo <repo> --squash
// --match-head-commit <headSHA>` -- never --delete-branch, never
// --merge/--rebase (epic #661 Decision 4; #854 squash-only) -- and, only on
// a zero exit, performs exactly one post-merge refetch (Q5: one refetch, no
// polling), recording success only when that refetch reports state ==
// MERGED. A zero exit whose refetch reports anything else is an
// indeterminate failure, never success (Decision 6); a refetch that itself
// fails to read is a distinct reason from an indeterminate-but-readable
// non-MERGED state. Returns whether the merge was confirmed, plus decision
// updated with the final Merge/Reason/Detail.
func executeMerge(s *State, headSHA string, decision automergeDecision) (bool, automergeDecision) {
	stdout, stderr, err := execGh("pr", "merge", s.PR, "--repo", s.Repo, "--squash", "--match-head-commit", headSHA)
	if err != nil {
		decision.Merge = false
		decision.Reason = reasonMergeFailed
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		decision.Detail = sanitizeDetail(detail)
		return false, decision
	}

	var fresh prView
	if verifyErr := ghJSON(&fresh, "pr", "view", s.PR, "--repo", s.Repo, "--json", prViewFields); verifyErr != nil {
		decision.Merge = false
		decision.Reason = reasonMergeVerifyUnreadable
		decision.Detail = sanitizeDetail(verifyErr.Error())
		return false, decision
	}
	if fresh.State != "MERGED" {
		decision.Merge = false
		decision.Reason = reasonMergeIndeterminate
		decision.Detail = ""
		return false, decision
	}
	decision.Merge = true
	decision.Reason = ""
	decision.Detail = ""
	return true, decision
}
