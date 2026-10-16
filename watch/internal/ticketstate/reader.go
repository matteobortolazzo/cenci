package ticketstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Verdict is the closed set of answers to "is this ticket's work over?".
// Unknown is the zero value so an unset Verdict never reads as Finished.
type Verdict int

const (
	Unknown Verdict = iota
	NotFinished
	Finished
)

// String names the verdict for logs and test output. A value outside the closed
// set renders as "Verdict(N)", never as one of the valid names.
func (v Verdict) String() string {
	switch v {
	case Unknown:
		return "Unknown"
	case NotFinished:
		return "NotFinished"
	case Finished:
		return "Finished"
	default:
		return fmt.Sprintf("Verdict(%d)", int(v))
	}
}

// Result is one ticket's verdict. Err is non-nil exactly when Verdict is
// Unknown, so a caller can never act on a verdict without a successful read.
type Result struct {
	Verdict Verdict
	Err     error
}

// Outcome is the closed set of detailed answers to "what happened to this
// ticket?". OutcomeUnknown is the zero value so an unset Outcome never reads as
// one that qualifies for removal.
type Outcome int

const (
	OutcomeUnknown Outcome = iota
	OutcomeOpen
	OutcomeIssueClosed
	OutcomePRMerged
	OutcomePRClosedUnmerged
)

// String names the outcome for logs and test output. A value outside the closed
// set renders as "Outcome(N)", never as one of the valid names.
func (o Outcome) String() string {
	switch o {
	case OutcomeUnknown:
		return "Unknown"
	case OutcomeOpen:
		return "Open"
	case OutcomeIssueClosed:
		return "IssueClosed"
	case OutcomePRMerged:
		return "PRMerged"
	case OutcomePRClosedUnmerged:
		return "PRClosedUnmerged"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// OutcomeResult is one ticket's detailed outcome. Err is non-nil exactly when
// Outcome is OutcomeUnknown, so a caller can never act on an outcome without a
// successful read. Heads holds the validated head SHAs of the merged PR (the
// number itself), or of an issue's merged closing PRs, de-duplicated, and is
// non-empty exactly when Outcome is OutcomePRMerged. For a closed issue that is
// every merged closing PR; for an open issue it is only the qualifying ones (merged
// into the default branch after the issue's last reopen). For an issue whose
// closing-PR list was truncated (more than the 25 fetched), Heads may be a partial
// set: it is valid only as a positive "this head merged" match, never as the
// complete set of merged heads.
type OutcomeResult struct {
	Outcome Outcome
	Heads   []string
	Err     error
}

// Repo identifies a GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

// MaxBatch is the most ticket numbers sent in one gh api graphql call.
const MaxBatch = 50

// repoPartRE is the shape accepted for a repo owner or name. Both reach gh only
// as `-f` variables, but a leading dash would still be parsed by gh as a flag.
var repoPartRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// headSHARE is the shape accepted for a head SHA: lowercase hex, 40 (SHA-1) or
// 64 (SHA-256) characters. A consumer may pass the value to git as an argument,
// so a leading dash or any other shape must never get through (go-gotchas rule 16).
var headSHARE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// maxTicketNumber is the largest number sent. GraphQL Int is 32-bit, and an
// out-of-range literal is an un-aliased coercion error that would fail the
// whole batch, so a larger number is rejected on its own before any call.
const maxTicketNumber = math.MaxInt32

// aliasPrefix starts every GraphQL alias; the rest is strconv.Itoa of a
// validated positive int, so nothing caller-controlled reaches the query text.
const aliasPrefix = "n"

// Reader reads ticket state through `gh api graphql`. The zero value is ready.
type Reader struct{}

// Read returns one Result per distinct requested number, making one gh call per
// MaxBatch numbers. It fails open: a Finished verdict is only ever produced
// from a successfully decoded node, and every failed read is an Unknown result
// carrying the cause. A whole-call failure errors every number in its batch; a
// failure tied to a single alias errors only that number.
func (r *Reader) Read(ctx context.Context, repo Repo, nums []int) map[int]Result {
	fetched := fetch(ctx, repo, nums)
	results := make(map[int]Result, len(fetched))
	for n, f := range fetched {
		if f.err != nil {
			results[n] = Result{Err: f.err}
			continue
		}
		verdict, err := classify(f.raw, f.defaultBranch)
		if err != nil {
			results[n] = Result{Err: fmt.Errorf("#%d: %w", n, err)}
			continue
		}
		results[n] = Result{Verdict: verdict}
	}
	return results
}

// ReadOutcomes returns one OutcomeResult per distinct requested number, through
// the same single query, batching and failure handling as Read. It fails open
// the same way and more strictly: a removable outcome (issue closed, PR merged,
// PR closed unmerged) is only ever produced from a successfully decoded, fully
// recognized node, and any other case is OutcomeUnknown carrying the cause.
func (r *Reader) ReadOutcomes(ctx context.Context, repo Repo, nums []int) map[int]OutcomeResult {
	fetched := fetch(ctx, repo, nums)
	results := make(map[int]OutcomeResult, len(fetched))
	for n, f := range fetched {
		if f.err != nil {
			results[n] = OutcomeResult{Err: f.err}
			continue
		}
		outcome, heads, err := classifyOutcome(f.raw, f.defaultBranch)
		if err != nil {
			results[n] = OutcomeResult{Err: fmt.Errorf("#%d: %w", n, err)}
			continue
		}
		results[n] = OutcomeResult{Outcome: outcome, Heads: heads}
	}
	return results
}

// fetchResult is one number's raw alias node, or the reason it could not be read.
// defaultBranch is the repository's default branch name, read once per batch and
// shared by every number in it; it is empty when the response did not carry a
// usable one, which only matters to an open issue with a merged closing PR.
type fetchResult struct {
	raw           json.RawMessage
	defaultBranch string
	err           error
}

// fetch validates the repo and the numbers, then reads every distinct valid
// number in batches of at most MaxBatch, one gh call per batch. It returns one
// entry per distinct requested number; a number that could not be read carries
// its error and no node.
func fetch(ctx context.Context, repo Repo, nums []int) map[int]fetchResult {
	results := make(map[int]fetchResult, len(nums))
	var valid []int
	seen := make(map[int]bool, len(nums))
	for _, n := range nums {
		if seen[n] {
			continue
		}
		seen[n] = true
		if n <= 0 {
			results[n] = fetchResult{err: fmt.Errorf("invalid ticket number %d", n)}
			continue
		}
		if n > maxTicketNumber {
			results[n] = fetchResult{err: fmt.Errorf("invalid ticket number %d: exceeds GraphQL Int range (max %d)", n, maxTicketNumber)}
			continue
		}
		valid = append(valid, n)
	}

	if err := validateRepo(repo); err != nil {
		for _, n := range valid {
			results[n] = fetchResult{err: err}
		}
		return results
	}

	for start := 0; start < len(valid); start += MaxBatch {
		chunk := valid[start:min(start+MaxBatch, len(valid))]
		for n, res := range readChunk(ctx, repo, chunk) {
			results[n] = res
		}
	}
	return results
}

// validRepoPart reports whether s is acceptable as a repo owner or name. It is
// the one validator shared by the reader (validateRepo) and the origin parser
// (parseRemoteURL), so an Identity the resolver hands out always passes Read.
func validRepoPart(s string) bool {
	return repoPartRE.MatchString(s) && !strings.HasPrefix(s, "-")
}

func validateRepo(repo Repo) error {
	if !validRepoPart(repo.Owner) || !validRepoPart(repo.Name) {
		return fmt.Errorf("invalid repo %q/%q", repo.Owner, repo.Name)
	}
	return nil
}

// buildQuery returns the single-line batch query for nums, which must all be
// validated positive ints. Besides each number's state and closing PRs it selects
// the repository's default branch once, each closing PR's base branch and merge
// time, and each issue's most recent reopen event: what an open issue's merged
// closing PR needs to prove it qualifies. Everything but the aliases is constant.
func buildQuery(nums []int) string {
	var b strings.Builder
	b.WriteString("query($owner:String!,$name:String!){repository(owner:$owner,name:$name){defaultBranchRef{name}")
	for _, n := range nums {
		num := strconv.Itoa(n)
		b.WriteString(" " + aliasPrefix + num + ": issueOrPullRequest(number:" + num + "){__typename" +
			" ... on Issue{state timelineItems(itemTypes:[REOPENED_EVENT], last:1){nodes{... on ReopenedEvent{createdAt}}}" +
				" closedByPullRequestsReferences(first: 25, includeClosedPrs: true){pageInfo{hasNextPage} nodes{merged headRefOid mergedAt baseRefName}}}" +
			" ... on PullRequest{state merged headRefOid}}")
	}
	b.WriteString("}}")
	return b.String()
}

type gqlError struct {
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

type gqlResponse struct {
	Data struct {
		Repository map[string]json.RawMessage `json:"repository"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

// readChunk makes one gh call for nums and returns every one of their raw nodes
// or per-number errors. The batch's default branch name rides on every node; a
// missing or unusable one does not fail the batch.
func readChunk(ctx context.Context, repo Repo, nums []int) map[int]fetchResult {
	out := make(map[int]fetchResult, len(nums))
	failAll := func(err error) map[int]fetchResult {
		err = fmt.Errorf("reading ticket state from %s/%s: %w", repo.Owner, repo.Name, err)
		for _, n := range nums {
			out[n] = fetchResult{err: err}
		}
		return out
	}

	stdout, stderr, runErr := execGh(ctx, "api", "graphql",
		"-f", "query="+buildQuery(nums),
		"-f", "owner="+repo.Owner,
		"-f", "name="+repo.Name)

	// Only a plain non-zero exit may still carry a usable body. A timeout, an
	// over-cap stream or a failure to start gh leaves nothing to trust.
	var exitErr *exec.ExitError
	if errors.Is(runErr, errTimeout) || errors.Is(runErr, errOutputTruncated) || (runErr != nil && !errors.As(runErr, &exitErr)) {
		return failAll(withStderr(runErr, stderr))
	}

	var resp gqlResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		if runErr != nil {
			return failAll(withStderr(runErr, stderr))
		}
		return failAll(fmt.Errorf("decoding gh output: %w", err))
	}

	known := make(map[string]bool, len(nums))
	for _, n := range nums {
		known[aliasPrefix+strconv.Itoa(n)] = true
	}
	aliasErrs, untied := splitErrors(resp.Errors, known)
	if len(untied) > 0 {
		return failAll(fmt.Errorf("graphql errors: %s", strings.Join(untied, "; ")))
	}
	if resp.Data.Repository == nil {
		// An HTTP failure body (e.g. {"message":"Bad credentials"}) carries no
		// repository key and no errors[]; the exit status and stderr are then
		// the only explanation, so they must not be dropped.
		if runErr != nil {
			return failAll(fmt.Errorf("gh returned no repository data%s: %w", errorSuffix(resp.Errors), withStderr(runErr, stderr)))
		}
		return failAll(fmt.Errorf("gh returned no repository data%s", errorSuffix(resp.Errors)))
	}
	if runErr != nil && len(aliasErrs) == 0 {
		return failAll(withStderr(runErr, stderr))
	}

	defaultBranch := decodeDefaultBranch(resp.Data.Repository["defaultBranchRef"])
	for _, n := range nums {
		alias := aliasPrefix + strconv.Itoa(n)
		if msg, errored := aliasErrs[alias]; errored {
			out[n] = fetchResult{err: fmt.Errorf("#%d: %s", n, msg)}
			continue
		}
		raw, ok := resp.Data.Repository[alias]
		if !ok {
			out[n] = fetchResult{err: fmt.Errorf("#%d: missing from gh response", n)}
			continue
		}
		out[n] = fetchResult{raw: raw, defaultBranch: defaultBranch}
	}
	return out
}

// decodeDefaultBranch returns the default branch name from a raw defaultBranchRef,
// or "" when it is absent, null, of the wrong shape or empty. The batch still
// reads: only an open issue with a merged closing PR needs the name.
func decodeDefaultBranch(raw json.RawMessage) string {
	var ref *struct {
		Name *string `json:"name"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil || ref == nil || ref.Name == nil {
		return ""
	}
	return *ref.Name
}

// splitErrors attributes each GraphQL error to a known alias via its path
// (["repository", "<alias>", ...]; a deeper path still ties to the alias).
// Errors without such a path come back as untied messages.
func splitErrors(errs []gqlError, known map[string]bool) (byAlias map[string]string, untied []string) {
	byAlias = make(map[string]string)
	for _, e := range errs {
		msg := collapse(e.Message)
		if len(e.Path) >= 2 {
			root, _ := e.Path[0].(string)
			alias, _ := e.Path[1].(string)
			if root == "repository" && known[alias] {
				if prev, ok := byAlias[alias]; ok {
					msg = prev + "; " + msg
				}
				byAlias[alias] = msg
				continue
			}
		}
		untied = append(untied, msg)
	}
	return byAlias, untied
}

// errorSuffix renders any GraphQL error messages as ": a; b", or "".
func errorSuffix(errs []gqlError) string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, collapse(e.Message))
	}
	if len(msgs) == 0 {
		return ""
	}
	return ": " + strings.Join(msgs, "; ")
}

// node is one issueOrPullRequest result. Pointer fields keep an absent or null
// field distinguishable from a zero value (go-gotchas rules 18 and 23). Timeline
// holds an issue's most recent reopen event; each closing node carries the merge
// time and base branch an open issue needs to decide whether that merge qualifies.
type node struct {
	Typename   *string `json:"__typename"`
	State      *string `json:"state"`
	Merged     *bool   `json:"merged"`
	HeadRefOid *string `json:"headRefOid"`
	Timeline   *struct {
		Nodes []*struct {
			CreatedAt *string `json:"createdAt"`
		} `json:"nodes"`
	} `json:"timelineItems"`
	Closing *struct {
		PageInfo *struct {
			HasNextPage *bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []struct {
			Merged      *bool   `json:"merged"`
			HeadRefOid  *string `json:"headRefOid"`
			MergedAt    *string `json:"mergedAt"`
			BaseRefName *string `json:"baseRefName"`
		} `json:"nodes"`
	} `json:"closedByPullRequestsReferences"`
}

// decodeNode decodes one alias node and requires the two fields every
// classification starts from.
func decodeNode(raw json.RawMessage) (*node, error) {
	var n *node
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, fmt.Errorf("decoding node: %w", err)
	}
	if n == nil {
		return nil, errors.New("node is null")
	}
	if n.Typename == nil || n.State == nil {
		return nil, errors.New("node has no __typename or state")
	}
	return n, nil
}

// classify maps one alias node to a verdict. Anything it does not recognise is
// an error (Unknown), never a benign answer.
func classify(raw json.RawMessage, defaultBranch string) (Verdict, error) {
	n, err := decodeNode(raw)
	if err != nil {
		return Unknown, err
	}

	switch *n.Typename {
	case "Issue":
		switch *n.State {
		case "CLOSED":
			return Finished, nil
		case "OPEN":
			return classifyOpenIssue(n, defaultBranch)
		}
	case "PullRequest":
		if n.Merged != nil && *n.Merged {
			return Finished, nil
		}
		switch *n.State {
		case "MERGED", "CLOSED":
			return Finished, nil
		case "OPEN":
			return NotFinished, nil
		}
	default:
		return Unknown, fmt.Errorf("unrecognized __typename %q", *n.Typename)
	}
	return Unknown, fmt.Errorf("unrecognized %s state %q", *n.Typename, *n.State)
}

// Fixed cause texts for a merged closing PR that does not settle an open issue.
// They never echo a branch name or timestamp from the response.
const (
	causeNoBase     = "merged closing PR has no base branch"
	causeNoDefault  = "repository default branch unknown"
	causeNonDefault = "merged closing PR targets a non-default branch"
	causeMergedAt   = "merged closing PR has a missing or malformed mergedAt"
	causeTimeline   = "issue reopen timeline missing or malformed"
	causePreReopen  = "merged closing PR did not merge after the issue's last reopen"
)

// unqualifiedCauseOrder is the fixed order distinct causes are reported in.
var unqualifiedCauseOrder = []string{causeNoBase, causeNoDefault, causeNonDefault, causeMergedAt, causeTimeline, causePreReopen}

// qualifier decides which of an open issue's merged closing PRs qualify: base
// branch equal to the repository's default branch, and merged strictly after the
// issue's last reopen (or the issue never reopened). A merged entry that cannot be
// checked does not qualify. Both classifiers share it, so Read and ReadOutcomes
// agree by construction. The reopen time is derived on the first merged entry only,
// so a malformed timeline never matters to an issue without a merged closing PR.
type qualifier struct {
	n             *node
	defaultBranch string
	reopenKnown   bool
	reopen        time.Time // zero when the issue was never reopened
	reopenErr     bool
	unmet         map[string]bool
}

// qualifies reports whether a merged closing PR settles the open issue, recording
// the cause when it does not.
func (q *qualifier) qualifies(base, mergedAt *string) bool {
	cause := q.unmetCause(base, mergedAt)
	if cause == "" {
		return true
	}
	if q.unmet == nil {
		q.unmet = make(map[string]bool)
	}
	q.unmet[cause] = true
	return false
}

// unmetCause returns the fixed cause a merged closing PR does not qualify, or ""
// when it does. The checks run cheapest first; the reopen timeline is read lazily.
func (q *qualifier) unmetCause(base, mergedAt *string) string {
	if base == nil {
		return causeNoBase
	}
	if q.defaultBranch == "" {
		return causeNoDefault
	}
	if *base != q.defaultBranch {
		return causeNonDefault
	}
	if mergedAt == nil {
		return causeMergedAt
	}
	merged, err := time.Parse(time.RFC3339, *mergedAt)
	if err != nil {
		return causeMergedAt
	}
	if !q.reopenKnown {
		q.reopen, q.reopenErr = lastReopen(q.n)
		q.reopenKnown = true
	}
	if q.reopenErr {
		return causeTimeline
	}
	if !merged.After(q.reopen) {
		return causePreReopen
	}
	return ""
}

// cause joins the distinct causes recorded so far in a fixed order, or "" when no
// merged closing PR failed to qualify.
func (q *qualifier) cause() string {
	var causes []string
	for _, c := range unqualifiedCauseOrder {
		if q.unmet[c] {
			causes = append(causes, c)
		}
	}
	return strings.Join(causes, "; ")
}

// lastReopen returns the time of the issue's most recent reopen, or the zero time
// when the timeline is present and empty (never reopened). A missing or null
// timeline or nodes list, a null entry, or a missing or unparseable createdAt is
// reported as malformed. Should more than one entry come back, the latest counts
// and every entry must parse.
func lastReopen(n *node) (time.Time, bool) {
	if n.Timeline == nil || n.Timeline.Nodes == nil {
		return time.Time{}, true
	}
	var latest time.Time
	for _, entry := range n.Timeline.Nodes {
		if entry == nil || entry.CreatedAt == nil {
			return time.Time{}, true
		}
		at, err := time.Parse(time.RFC3339, *entry.CreatedAt)
		if err != nil {
			return time.Time{}, true
		}
		if at.After(latest) {
			latest = at
		}
	}
	return latest, false
}

// classifyOpenIssue: an open issue is Finished once a linked closing PR that
// qualifies has merged, even when the list is truncated, since a qualifying merge
// anywhere settles it. A PR qualifies when it merged into the repository's default
// branch strictly after the issue's last reopen, or the issue was never reopened.
// Merged closing PRs of which none qualify, or cannot be checked, leave the issue
// Unknown with the distinct causes: the old merge may predate a reopen, or sit on
// a stacked branch that has not reached the default branch. Without a merged
// closing PR, a truncated or incomplete list cannot prove NotFinished: that
// includes an absent or null nodes list and any entry that is null or lacks a
// merged flag, since such an entry might have been the merged PR.
func classifyOpenIssue(n *node, defaultBranch string) (Verdict, error) {
	if n.Closing == nil || n.Closing.PageInfo == nil || n.Closing.PageInfo.HasNextPage == nil {
		return Unknown, errors.New("open issue has no closing-PR pagination info")
	}
	q := qualifier{n: n, defaultBranch: defaultBranch}
	incomplete := n.Closing.Nodes == nil
	for _, pr := range n.Closing.Nodes {
		if pr.Merged == nil {
			// A null entry decodes to the zero value, so it lands here too.
			incomplete = true
			continue
		}
		if *pr.Merged && q.qualifies(pr.BaseRefName, pr.MergedAt) {
			return Finished, nil
		}
	}
	if cause := q.cause(); cause != "" {
		return Unknown, errors.New(cause)
	}
	if incomplete {
		return Unknown, errors.New("open issue has a missing or malformed closing-PR nodes list")
	}
	if *n.Closing.PageInfo.HasNextPage {
		return Unknown, errors.New("closing-PR list truncated")
	}
	return NotFinished, nil
}

// classifyOutcome maps one alias node to a detailed outcome. It is stricter than
// classify: it may answer OutcomeUnknown where classify says Finished, but never
// a removable outcome where classify says NotFinished or Unknown. Anything it
// does not fully recognise, and any merged node without a valid head SHA, is an
// error (OutcomeUnknown), never a benign answer.
func classifyOutcome(raw json.RawMessage, defaultBranch string) (Outcome, []string, error) {
	n, err := decodeNode(raw)
	if err != nil {
		return OutcomeUnknown, nil, err
	}

	switch *n.Typename {
	case "Issue":
		return classifyIssueOutcome(n, defaultBranch)
	case "PullRequest":
		return classifyPROutcome(n)
	default:
		return OutcomeUnknown, nil, fmt.Errorf("unrecognized __typename %q", *n.Typename)
	}
}

// classifyPROutcome: a merged PR (merged flag or MERGED state) carries its own
// head SHA. A CLOSED PR is only "closed unmerged" when the merged flag is
// explicitly false; a null or missing flag cannot prove it.
func classifyPROutcome(n *node) (Outcome, []string, error) {
	state := *n.State
	switch state {
	case "OPEN", "CLOSED", "MERGED":
	default:
		return OutcomeUnknown, nil, fmt.Errorf("unrecognized %s state %q", *n.Typename, state)
	}

	if state == "MERGED" || (n.Merged != nil && *n.Merged) {
		head, err := validHead(n.HeadRefOid)
		if err != nil {
			return OutcomeUnknown, nil, fmt.Errorf("merged PR: %w", err)
		}
		return OutcomePRMerged, []string{head}, nil
	}
	if state == "OPEN" {
		return OutcomeOpen, nil, nil
	}
	if n.Merged == nil {
		return OutcomeUnknown, nil, errors.New("closed PR has no merged flag")
	}
	return OutcomePRClosedUnmerged, nil, nil
}

// classifyIssueOutcome: a closed issue with any merged closing PR is "PR merged",
// even when the list is truncated, since a merge anywhere settles it; an open
// issue is "PR merged" only through a qualifying merged closing PR (merged into
// the default branch strictly after the issue's last reopen, or never reopened),
// and merged closing PRs of which none qualify, or cannot be checked, make it an
// error with the distinct causes. The heads of a truncated list are partial, and a
// head that was not fetched can only make a consumer keep more; an open issue's
// heads are only the qualifying ones. A merged entry whose own SHA is missing or
// malformed is different, qualifying or not: it makes the whole number an error
// (OutcomeUnknown), never a merge with a partial head set. Without a merge, a
// truncated or incomplete list cannot prove "closed" or "open" and is an error:
// that includes an absent or null nodes list and any entry that is null or lacks a
// merged flag.
func classifyIssueOutcome(n *node, defaultBranch string) (Outcome, []string, error) {
	state := *n.State
	switch state {
	case "OPEN", "CLOSED":
	default:
		return OutcomeUnknown, nil, fmt.Errorf("unrecognized %s state %q", *n.Typename, state)
	}
	if n.Closing == nil || n.Closing.PageInfo == nil || n.Closing.PageInfo.HasNextPage == nil {
		return OutcomeUnknown, nil, fmt.Errorf("%s issue has no closing-PR pagination info", strings.ToLower(state))
	}

	q := qualifier{n: n, defaultBranch: defaultBranch}
	var heads []string
	incomplete := n.Closing.Nodes == nil
	for _, pr := range n.Closing.Nodes {
		if pr.Merged == nil {
			// A null entry decodes to the zero value, so it lands here too.
			incomplete = true
			continue
		}
		if !*pr.Merged {
			continue
		}
		head, err := validHead(pr.HeadRefOid)
		if err != nil {
			return OutcomeUnknown, nil, fmt.Errorf("merged closing PR: %w", err)
		}
		if state == "OPEN" && !q.qualifies(pr.BaseRefName, pr.MergedAt) {
			continue
		}
		if !slices.Contains(heads, head) {
			heads = append(heads, head)
		}
	}
	if len(heads) > 0 {
		return OutcomePRMerged, heads, nil
	}
	if cause := q.cause(); cause != "" {
		return OutcomeUnknown, nil, errors.New(cause)
	}
	if incomplete {
		return OutcomeUnknown, nil, fmt.Errorf("%s issue has a missing or malformed closing-PR nodes list", strings.ToLower(state))
	}
	if *n.Closing.PageInfo.HasNextPage {
		return OutcomeUnknown, nil, errors.New("closing-PR list truncated")
	}
	if state == "CLOSED" {
		return OutcomeIssueClosed, nil, nil
	}
	return OutcomeOpen, nil, nil
}

// validHead returns the head SHA only when it is present and well-formed, so a
// value that later reaches git as an argument can never be a flag or a ref. The
// rejected value is never echoed: it is outside text.
func validHead(head *string) (string, error) {
	if head == nil {
		return "", errors.New("no head SHA")
	}
	if !headSHARE.MatchString(*head) {
		return "", errors.New("malformed head SHA")
	}
	return *head, nil
}
