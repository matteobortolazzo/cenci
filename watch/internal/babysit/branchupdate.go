package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The head-pinned, non-force branch update (#1475). A clean BEHIND PR (mergeable
// MERGEABLE, mergeStateStatus BEHIND) gets one server-side merge-method update per
// observed (head OID, base OID) pair: the `updatePullRequestBranch` GraphQL
// mutation with `updateMethod: MERGE` and `expectedHeadOid` set to the head this
// tick observed, so a push by anyone else is rejected by the server and never
// overwritten. It is never `gh pr update-branch` (which re-reads the head itself
// and pins to that), never a rebase, never a force-push, reset, stash or discard,
// and it never writes `automerge:ok` or any other merge grant: the merge itself
// still goes through runAutomerge's gate, which re-reads everything on the
// resulting head.

// branchUpdate outcomes, the closed set of branchUpdate.Outcome.
const (
	// branchUpdateIssued: the mutation succeeded, the head has not moved yet.
	branchUpdateIssued = "issued"
	// branchUpdateUpdated: a later read saw the head move off the attempted one.
	branchUpdateUpdated = "updated"
	// branchUpdateFailed: one counted failed attempt at this pair.
	branchUpdateFailed = "failed"
	// branchUpdateUnsupported: the PR cannot be updated by babysit (a fork, a merge
	// queue, a branch-protection refusal). Terminal until `cenci babysit reset`.
	branchUpdateUnsupported = "unsupported"
)

// mergeStateBehind is GitHub's mergeStateStatus for a branch that must be updated
// before it can merge: the one state the update acts on and the merge gate holds.
const mergeStateBehind = "BEHIND"

// branchUpdateCap bounds consecutive failed attempts across base movements. It
// mirrors the conflict episode's three-launch budget (fixCap).
const branchUpdateCap = 3

// Why a PR is unsupported, the `(<reason>)` of the input-needed notice.
const (
	unsupportedFork       = "cross-repository PR"
	unsupportedProtection = "branch protection"
	unsupportedMergeQueue = "merge queue"
)

// branchUpdate is State.BranchUpdate: the per-(head, base) record of the update.
// HeadSHA and BaseSHA are the pair the last attempt was made at. FailedAttempts
// counts consecutive failed attempts across pairs and is zeroed by an update that
// landed. Notified marks that the one babysit-attention session for a hold
// (unsupported or exhausted) was launched.
type branchUpdate struct {
	HeadSHA        string    `json:"headSha"`
	BaseSHA        string    `json:"baseSha"`
	Outcome        string    `json:"outcome"`
	FailedAttempts int       `json:"failedAttempts,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	AttemptedAt    time.Time `json:"attemptedAt"`
	Notified       bool      `json:"notified,omitempty"`
}

// branchUpdatePreflightQuery re-reads, just before the call, everything the
// update is pinned to or refused for.
const branchUpdatePreflightQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){id headRefOid baseRefOid isInMergeQueue isCrossRepository mergeStateStatus}}}`

// branchUpdateMutation is the update itself: always the MERGE method, pinned to
// expectedHeadOid.
const branchUpdateMutation = `mutation($pullRequestId:ID!,$expectedHeadOid:GitObjectID!){updatePullRequestBranch(input:{pullRequestId:$pullRequestId,expectedHeadOid:$expectedHeadOid,updateMethod:MERGE}){pullRequest{id headRefOid}}}`

// graphQLForbidden is the errors[].type GitHub answers when branch protection
// (or the token's permissions) refuses the update.
const graphQLForbidden = "FORBIDDEN"

// branchUpdateExhausted reports whether r is the third consecutive failure.
func branchUpdateExhausted(r *branchUpdate) bool {
	return r != nil && r.Outcome == branchUpdateFailed && r.FailedAttempts >= branchUpdateCap
}

// branchUpdateHeld reports whether s carries a hold only a human clears: an
// unsupported PR or an exhausted attempt budget. Both are recovered by
// `cenci babysit reset <pr>`, which clears the record.
func branchUpdateHeld(s *State) bool {
	return branchUpdateHold(s) != ""
}

// branchUpdateHold is the input-needed text of a hold, "" when there is none.
func branchUpdateHold(s *State) string {
	switch {
	case s.BranchUpdate == nil:
		return ""
	case s.BranchUpdate.Outcome == branchUpdateUnsupported:
		return fmt.Sprintf("%s (%s)", noteBranchUpdateUnsupported, s.BranchUpdate.Reason)
	case branchUpdateExhausted(s.BranchUpdate):
		return noteBranchUpdateExhausted
	}
	return ""
}

// branchUpdateHoldNote is the hold's recovery instruction for AutomergeDetail.
func branchUpdateHoldNote(s *State) string {
	hold := branchUpdateHold(s)
	if hold == "" {
		return ""
	}
	return fmt.Sprintf("%s; update the PR branch yourself, then cenci babysit reset %s re-arms the branch update", hold, s.PR)
}

// branchUpdateTick is what tick knows when it reaches the update.
type branchUpdateTick struct {
	// conflictHeld: a merge-conflict episode owns the branch.
	conflictHeld bool
	// launched names the branch writer this tick launched ("" when none), and
	// waiting says a repair was refused for capacity and will launch later.
	launched string
	waiting  bool
	// live reads (once per tick) the repair workers of this PR.
	live func() (workerLiveness, error)
	// checkpoint persists the state before the call (nil: not persisted).
	checkpoint func() error
}

// branchUpdateStep is the tick's whole update step. It settles an issued record
// from this tick's view, keeps the unsupported/exhausted holds, and for a clean
// BEHIND PR with no attempt at this (head, base) pair and no live writer makes
// the one pinned update. inFlight says an update was issued and its head move is
// still awaited (the tick stays actionable); issuedNow says this very tick
// issued it (the babysit-review launch waits for the new head).
//
// Nothing is consumed by anything that is not a call: UNKNOWN mergeable, an
// unknown head or base, a deferral, a failed or stale preflight read.
func branchUpdateStep(s *State, pr prView, bt branchUpdateTick) (inFlight, issuedNow bool, err error) {
	settleBranchUpdate(s, pr)
	if branchUpdateHeld(s) {
		return false, false, notifyBranchUpdateHold(s)
	}
	// Only a clean BEHIND PR is updated; a conflict goes to merge-repair, and
	// UNKNOWN (or any other mergeable value) is re-read next tick.
	if pr.MergeStateStatus != mergeStateBehind || bt.conflictHeld || pr.Mergeable != "MERGEABLE" {
		return false, false, nil
	}
	if pr.HeadRefOID == "" || pr.BaseRefOID == "" || pr.HeadRefName == "" {
		fmt.Printf("PR #%s: branch update skipped: the PR head or base commit is unknown\n", s.PR)
		return false, false, nil
	}
	prev := s.BranchUpdate
	if prev != nil && prev.HeadSHA == pr.HeadRefOID && prev.BaseSHA == pr.BaseRefOID {
		// One attempt per pair, whatever it came to: polls and restarts make no
		// second call. An issued one is settled by the head moving; only a reset
		// or a new base re-arms it.
		if prev.Outcome == branchUpdateIssued {
			fmt.Printf("PR #%s: branch update already issued for head %s on base %s; waiting for the head to move\n", s.PR, pr.HeadRefOID, pr.BaseRefOID)
			return true, false, nil
		}
		return false, false, nil
	}
	if pr.IsCrossRepository != nil && *pr.IsCrossRepository {
		return false, false, markBranchUpdateUnsupported(s, pr, unsupportedFork, "")
	}

	// A branch writer this tick launched, or one refused for capacity that will
	// launch later, owns the branch first.
	switch {
	case bt.launched != "":
		fmt.Printf("PR #%s: branch update deferred: %s active\n", s.PR, bt.launched)
		return false, false, nil
	case bt.waiting:
		fmt.Printf("PR #%s: branch update deferred: a repair is waiting for capacity\n", s.PR)
		return false, false, nil
	}
	if bt.live != nil {
		w, err := bt.live()
		if err != nil {
			return false, false, err
		}
		if w.any() {
			fmt.Printf("PR #%s: branch update deferred: %s active\n", s.PR, strings.Join(liveWriterNames(w), ", "))
			return false, false, nil
		}
	}
	if writer, err := ledgerBranchWriter(s, pr); err != nil {
		fmt.Printf("PR #%s: branch update deferred: %s\n", s.PR, err)
		return false, false, nil
	} else if writer != "" {
		fmt.Printf("PR #%s: branch update deferred: %s active\n", s.PR, writer)
		return false, false, nil
	}

	pf, err := readBranchUpdatePreflight(s.Repo, s.PR)
	if err != nil {
		fmt.Printf("PR #%s: branch update skipped: the preflight read failed (%v); re-evaluating next tick\n", s.PR, err)
		return false, false, nil
	}
	switch {
	case pf.isInMergeQueue:
		return false, false, markBranchUpdateUnsupported(s, pr, unsupportedMergeQueue, "")
	case pf.isCrossRepository:
		return false, false, markBranchUpdateUnsupported(s, pr, unsupportedFork, "")
	case pf.headRefOID != pr.HeadRefOID || pf.baseRefOID != pr.BaseRefOID:
		fmt.Printf("PR #%s: branch update skipped: the PR head or base moved since this tick read it; re-evaluating next tick\n", s.PR)
		return false, false, nil
	case pf.mergeStateStatus != mergeStateBehind:
		fmt.Printf("PR #%s: branch update skipped: the PR is no longer BEHIND (%s); re-evaluating next tick\n", s.PR, pf.mergeStateStatus)
		return false, false, nil
	}

	// Persist the attempt before the call: a supervisor that dies between the
	// call and the next save restarts to find the pair already attempted, and
	// makes no second call. A checkpoint that cannot be written makes no call.
	failed := 0
	if prev != nil {
		failed = prev.FailedAttempts
	}
	s.BranchUpdate = &branchUpdate{HeadSHA: pr.HeadRefOID, BaseSHA: pr.BaseRefOID, Outcome: branchUpdateIssued, FailedAttempts: failed, AttemptedAt: time.Now().UTC()}
	if bt.checkpoint != nil {
		if err := bt.checkpoint(); err != nil {
			s.BranchUpdate = prev
			return false, false, fmt.Errorf("branch update: the attempt could not be persisted, so no update was made: %w", err)
		}
	}

	stdout, stderr, callErr := execGh("api", "graphql",
		"-f", "query="+branchUpdateMutation,
		"-f", "pullRequestId="+pf.id,
		"-f", "expectedHeadOid="+pr.HeadRefOID,
	)
	return classifyBranchUpdate(s, pr, stdout, stderr, callErr)
}

// classifyBranchUpdate turns the mutation's outcome into the record. A success
// stays issued: the merge commit is created asynchronously, so the returned
// head may still be the old one, and only a later tick's read that sees the head
// move records it as updated. FORBIDDEN is unsupported (branch protection); every
// other error, a timeout included, is one counted failed attempt.
func classifyBranchUpdate(s *State, pr prView, stdout, stderr string, callErr error) (inFlight, issuedNow bool, err error) {
	errs := decodeGraphQLErrors(stdout)
	if callErr == nil && len(errs) == 0 && branchUpdateReturnedPullRequest(stdout) {
		fmt.Printf("PR #%s: branch update issued for head %s on base %s\n", s.PR, pr.HeadRefOID, pr.BaseRefOID)
		return true, true, nil
	}
	if slices.ContainsFunc(errs, func(e graphQLError) bool { return e.Type == graphQLForbidden }) {
		return false, false, markBranchUpdateUnsupported(s, pr, unsupportedProtection, branchUpdateFailureReason(callErr, stderr, errs))
	}
	r := s.BranchUpdate
	r.Outcome = branchUpdateFailed
	r.FailedAttempts++
	r.Reason = branchUpdateFailureReason(callErr, stderr, errs)
	fmt.Printf("PR #%s: branch update failed (attempt %d of %d): %s\n", s.PR, r.FailedAttempts, branchUpdateCap, r.Reason)
	if branchUpdateExhausted(r) {
		fmt.Printf("PR #%s: %s\n", s.PR, noteBranchUpdateExhausted)
		return false, false, notifyBranchUpdateHold(s)
	}
	return false, false, nil
}

// branchUpdateFailureReason is the one-line, sanitized cause of a failed call.
func branchUpdateFailureReason(callErr error, stderr string, errs []graphQLError) string {
	var parts []string
	for _, e := range errs {
		if e.Message != "" {
			parts = append(parts, e.Message)
		}
	}
	if len(parts) == 0 {
		if msg := strings.TrimSpace(stderr); msg != "" {
			parts = append(parts, msg)
		}
	}
	if len(parts) == 0 && callErr != nil {
		parts = append(parts, callErr.Error())
	}
	if len(parts) == 0 {
		parts = append(parts, "the update returned no pull request")
	}
	return sanitizeDetail(strings.Join(parts, "; "))
}

// branchUpdateReturnedPullRequest reports whether the mutation's payload carries
// a pull request; a null payload is not a success.
func branchUpdateReturnedPullRequest(stdout string) bool {
	var body struct {
		Data struct {
			Update *struct {
				PullRequest *struct {
					ID string `json:"id"`
				} `json:"pullRequest"`
			} `json:"updatePullRequestBranch"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(stdout), &body) != nil {
		return false
	}
	return body.Data.Update != nil && body.Data.Update.PullRequest != nil
}

// settleBranchUpdate settles an issued record from this tick's view: a head that
// moved off the attempted one is the update (or a later push) landing, so the pair
// is spent and the failure streak ends. A head that has not moved leaves it
// issued, with no second call.
func settleBranchUpdate(s *State, pr prView) {
	r := s.BranchUpdate
	if r == nil || r.Outcome != branchUpdateIssued || pr.HeadRefOID == "" || pr.HeadRefOID == r.HeadSHA {
		return
	}
	r.Outcome = branchUpdateUpdated
	r.FailedAttempts = 0
	r.Reason = ""
	fmt.Printf("PR #%s: branch update settled: the head moved from %s to %s\n", s.PR, r.HeadSHA, pr.HeadRefOID)
}

// markBranchUpdateUnsupported records the PR as unsupported (no further attempt)
// and sends the one attention notice.
func markBranchUpdateUnsupported(s *State, pr prView, reason, detail string) error {
	s.BranchUpdate = &branchUpdate{HeadSHA: pr.HeadRefOID, BaseSHA: pr.BaseRefOID, Outcome: branchUpdateUnsupported, Reason: reason, AttemptedAt: time.Now().UTC()}
	if detail != "" {
		fmt.Printf("PR #%s: branch update refused: %s\n", s.PR, detail)
	}
	fmt.Printf("PR #%s: %s\n", s.PR, branchUpdateHold(s))
	return notifyBranchUpdateHold(s)
}

// notifyBranchUpdateHold launches the one babysit-attention session of a hold. A
// launch failure leaves Notified unset, so the next tick retries it.
func notifyBranchUpdateHold(s *State) error {
	r := s.BranchUpdate
	if r == nil || r.Notified || !branchUpdateHeld(s) {
		return nil
	}
	if err := launch(s, "babysit-attention", s.PR+" "+branchUpdateHold(s), nil); err != nil {
		recordUpstreamReadFailure(s, reasonWorkflowLaunchFailed, err)
		return err
	}
	r.Notified = true
	return nil
}

// liveWriterNames names the live repair workers of w.
func liveWriterNames(w workerLiveness) []string {
	var names []string
	if w.ciLive {
		names = append(names, "ci-repair")
	}
	if w.mergeLive {
		names = append(names, "merge-repair")
	}
	if w.reviewLive {
		names = append(names, "babysit-review")
	}
	if w.addressReviewLive {
		names = append(names, "address-review")
	}
	return names
}

// ledgerBranchWriter peeks the shared capacity ledger for an entry that writes
// this PR's branch: any worker, dispatch's or a babysit repair, holds it. An
// unreadable ledger cannot say there is none, so it defers: the returned error is
// the reason line.
func ledgerBranchWriter(s *State, pr prView) (writer string, err error) {
	ledger := capacityLedgerFn()
	snap, peekErr := ledger.Peek()
	if peekErr != nil {
		return "", errors.New(unreadableLedger(ledger, peekErr))
	}
	for _, e := range snap.Entries {
		if strings.EqualFold(e.Repo, s.Repo) && e.Branch == pr.HeadRefName {
			return string(e.Kind), nil
		}
	}
	return "", nil
}

// branchUpdatePreflight is the authoritative re-read made just before the call.
type branchUpdatePreflight struct {
	id                                       string
	headRefOID, baseRefOID, mergeStateStatus string
	isInMergeQueue, isCrossRepository        bool
}

// readBranchUpdatePreflight reads the PR's node id, head, base, merge-queue and
// fork state and merge status in one GraphQL call. A failed call, an error in the
// body or any missing field is an error: the update is never made on a partial
// read.
func readBranchUpdatePreflight(repo, pr string) (branchUpdatePreflight, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return branchUpdatePreflight{}, fmt.Errorf("malformed repo %q, want owner/name", repo)
	}
	stdout, stderr, err := execGh("api", "graphql",
		"-f", "query="+branchUpdatePreflightQuery,
		"-f", "owner="+owner,
		"-f", "name="+name,
		"-F", "number="+pr,
	)
	if err != nil {
		return branchUpdatePreflight{}, fmt.Errorf("%s: %w", sanitizeDetail(strings.TrimSpace(stderr)), err)
	}
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					ID                string `json:"id"`
					HeadRefOID        string `json:"headRefOid"`
					BaseRefOID        string `json:"baseRefOid"`
					IsInMergeQueue    *bool  `json:"isInMergeQueue"`
					IsCrossRepository *bool  `json:"isCrossRepository"`
					MergeStateStatus  string `json:"mergeStateStatus"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		return branchUpdatePreflight{}, errors.New("the preflight response did not decode")
	}
	if len(resp.Errors) > 0 {
		return branchUpdatePreflight{}, errors.New(sanitizeDetail(resp.Errors[0].Message))
	}
	p := resp.Data.Repository.PullRequest
	if p == nil || p.ID == "" || p.HeadRefOID == "" || p.BaseRefOID == "" || p.MergeStateStatus == "" || p.IsInMergeQueue == nil || p.IsCrossRepository == nil {
		return branchUpdatePreflight{}, errors.New("the preflight response is incomplete")
	}
	return branchUpdatePreflight{
		id: p.ID, headRefOID: p.HeadRefOID, baseRefOID: p.BaseRefOID, mergeStateStatus: p.MergeStateStatus,
		isInMergeQueue: *p.IsInMergeQueue, isCrossRepository: *p.IsCrossRepository,
	}, nil
}
