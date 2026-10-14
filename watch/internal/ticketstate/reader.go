package ticketstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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
	results := make(map[int]Result, len(nums))
	var valid []int
	seen := make(map[int]bool, len(nums))
	for _, n := range nums {
		if seen[n] {
			continue
		}
		seen[n] = true
		if n <= 0 {
			results[n] = Result{Err: fmt.Errorf("invalid ticket number %d", n)}
			continue
		}
		if n > maxTicketNumber {
			results[n] = Result{Err: fmt.Errorf("invalid ticket number %d: exceeds GraphQL Int range (max %d)", n, maxTicketNumber)}
			continue
		}
		valid = append(valid, n)
	}

	if err := validateRepo(repo); err != nil {
		for _, n := range valid {
			results[n] = Result{Err: err}
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
// validated positive ints.
func buildQuery(nums []int) string {
	var b strings.Builder
	b.WriteString("query($owner:String!,$name:String!){repository(owner:$owner,name:$name){")
	for _, n := range nums {
		num := strconv.Itoa(n)
		b.WriteString(" " + aliasPrefix + num + ": issueOrPullRequest(number:" + num + "){__typename" +
			" ... on Issue{state closedByPullRequestsReferences(first: 25, includeClosedPrs: true){pageInfo{hasNextPage} nodes{merged}}}" +
			" ... on PullRequest{state merged}}")
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

// readChunk makes one gh call for nums and classifies every one of them.
func readChunk(ctx context.Context, repo Repo, nums []int) map[int]Result {
	out := make(map[int]Result, len(nums))
	failAll := func(err error) map[int]Result {
		err = fmt.Errorf("reading ticket state from %s/%s: %w", repo.Owner, repo.Name, err)
		for _, n := range nums {
			out[n] = Result{Err: err}
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

	for _, n := range nums {
		alias := aliasPrefix + strconv.Itoa(n)
		if msg, errored := aliasErrs[alias]; errored {
			out[n] = Result{Err: fmt.Errorf("#%d: %s", n, msg)}
			continue
		}
		raw, ok := resp.Data.Repository[alias]
		if !ok {
			out[n] = Result{Err: fmt.Errorf("#%d: missing from gh response", n)}
			continue
		}
		verdict, err := classify(raw)
		if err != nil {
			out[n] = Result{Err: fmt.Errorf("#%d: %w", n, err)}
			continue
		}
		out[n] = Result{Verdict: verdict}
	}
	return out
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
// field distinguishable from a zero value (go-gotchas rules 18 and 23).
type node struct {
	Typename *string `json:"__typename"`
	State    *string `json:"state"`
	Merged   *bool   `json:"merged"`
	Closing  *struct {
		PageInfo *struct {
			HasNextPage *bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []struct {
			Merged *bool `json:"merged"`
		} `json:"nodes"`
	} `json:"closedByPullRequestsReferences"`
}

// classify maps one alias node to a verdict. Anything it does not recognise is
// an error (Unknown), never a benign answer.
func classify(raw json.RawMessage) (Verdict, error) {
	var n *node
	if err := json.Unmarshal(raw, &n); err != nil {
		return Unknown, fmt.Errorf("decoding node: %w", err)
	}
	if n == nil {
		return Unknown, errors.New("node is null")
	}
	if n.Typename == nil || n.State == nil {
		return Unknown, errors.New("node has no __typename or state")
	}

	switch *n.Typename {
	case "Issue":
		switch *n.State {
		case "CLOSED":
			return Finished, nil
		case "OPEN":
			return classifyOpenIssue(n)
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

// classifyOpenIssue: an open issue is Finished once any linked closing PR has
// merged, even when the list is truncated, since a merge anywhere settles it.
// Without a merge, a truncated or incomplete list cannot prove NotFinished: that
// includes an absent or null nodes list and any entry that is null or lacks a
// merged flag, since such an entry might have been the merged PR.
func classifyOpenIssue(n *node) (Verdict, error) {
	if n.Closing == nil || n.Closing.PageInfo == nil || n.Closing.PageInfo.HasNextPage == nil {
		return Unknown, errors.New("open issue has no closing-PR pagination info")
	}
	incomplete := n.Closing.Nodes == nil
	for _, pr := range n.Closing.Nodes {
		if pr.Merged == nil {
			// A null entry decodes to the zero value, so it lands here too.
			incomplete = true
			continue
		}
		if *pr.Merged {
			return Finished, nil
		}
	}
	if incomplete {
		return Unknown, errors.New("open issue has a missing or malformed closing-PR nodes list")
	}
	if *n.Closing.PageInfo.HasNextPage {
		return Unknown, errors.New("closing-PR list truncated")
	}
	return NotFinished, nil
}
