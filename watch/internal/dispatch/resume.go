package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// escalationAnchorPrefix (#849) is the persisted escalation anchor's fixed
// marker prefix -- the full marker is escalationAnchorPrefix + <nonce> +
// " -->". Replaces the pre-#849 single-string `<!-- cenci-planner-escalation
// -->` marker: anchor identity is now the immutable stored comment ID, and
// this nonce-bearing marker only *binds* that ID to the persisted draft
// (verified by classifyComments below) -- it is never scanned for on its own
// to *locate* the anchor. Shared contract, quoted verbatim (per the plan's
// Risks section) in this file, flow/skills/implement/phases/phase-1-plan.md's
// `## Escalation Anchor` section, flow/skills/implement/SKILL.md's
// awaiting-input branch, and asserted as a literal by
// flow/tests/escalation-anchor-contract.test.sh.
const escalationAnchorPrefix = "<!-- cenci-planner-escalation:"

// escalationNoncePattern is the nonce format both the flow producer and every
// consumer must validate against before trusting it (#849). Minted via
// `openssl rand -hex 16`, so a genuine nonce is always exactly 32 lowercase
// hex characters; anything else is treated as absent.
var escalationNoncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ghIssueCommentsTimeout bounds every individual escalation-answer REST probe
// call (#827/#849), mirroring ghIssueViewTimeout's rationale (dependency.go):
// a hung network call must never stall a dispatch pass indefinitely.
const ghIssueCommentsTimeout = 60 * time.Second

// ghIssueCommentsWaitDelay bounds how long cmd.Wait can block after the gh
// process itself has exited or been killed by ghIssueCommentsTimeout's
// context, mirroring ghIssueViewWaitDelay (dependency.go) --
// watch/docs/go-gotchas.md's cmd.WaitDelay rule (#822).
const ghIssueCommentsWaitDelay = 5 * time.Second

// maxAnswerProbes caps how many escalation-answer REST probe calls one
// RunOnce pass may make (#827), mirroring maxDependencyResolutions
// (dependency.go, #825 review fix #1): without a cap, a large fleet of
// `Input Needed` tickets could spawn unbounded gh subprocesses / GitHub API
// calls per pass. Once the cap is reached, any further probe resolves
// directly to AnswerProbeUnresolved without shelling out -- still fails
// closed (never resumes), it just stops making gh calls.
const maxAnswerProbes = 50

// maxProbeStdoutBytes bounds the total stdout one escalation-answer REST
// probe's `gh api ... --paginate` call may produce (#849 Risks section):
// `--paginate` on a very long comment thread could otherwise return an
// unbounded payload. Exceeding the cap fails closed to AnswerProbeUnresolved
// rather than decoding a truncated/corrupted partial JSON payload.
const maxProbeStdoutBytes = 1 << 20 // 1 MiB

// answerProbeBudget bounds and tracks one RunOnce pass's total escalation
// answer probe gh calls, mirroring dependencyResolutionBudget. logged tracks
// whether the cap-hit line has already been emitted this pass, so hitting
// the cap logs exactly once regardless of how many further tickets follow.
type answerProbeBudget struct {
	calls  int
	logged bool
}

// restCommentAuthor mirrors the REST comments API's `user` sub-object
// (#849): a login plus a first-class Type field ("Bot" for GitHub Apps/bots,
// "User" otherwise) -- unlike `gh issue view --json comments`, which exposes
// only login and forced a login-shape heuristic (isBotLogin) to guess at
// bot-ness.
type restCommentAuthor struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// restIssueComment mirrors one entry of `gh api
// repos/<owner>/<repo>/issues/<number>/comments`'s response shape (#849):
// the immutable numeric id, plus body/author/author_association.
type restIssueComment struct {
	ID                int64             `json:"id"`
	Body              string            `json:"body"`
	Author            restCommentAuthor `json:"user"`
	AuthorAssociation string            `json:"author_association"`
}

// resolveAnswerProbes probes every `Input Needed` ticket in tickets for a
// human answer to its escalation (#827/#849), keyed by planKey(repo, number)
// so Decide's Inputs.Answers can look it up without threading the probe
// through Ticket at all -- mirrors RunReconcileOnce's countAttempts loop
// (reconcile_run.go:349-368), the label-scoped per-ticket gh read outside the
// collector precedent. planByTicket (built by indexPlans, decide.go) supplies
// each ticket's persisted escalation anchor fields -- resolveAnswerProbes is
// the sole reader of Plan.EscalationNonce/EscalationCommentID outside
// Decide itself, since Decide's own gate chain must stay pure (no I/O).
// Bounded by maxAnswerProbes across the whole call.
func resolveAnswerProbes(tickets []Ticket, planByTicket map[string]*Plan, out io.Writer) map[string]AnswerProbe {
	answers := make(map[string]AnswerProbe)
	budget := &answerProbeBudget{}
	for _, t := range tickets {
		if !hasLabel(t.Labels, labelInputNeeded) {
			continue
		}
		var anchorID int64
		var nonce string
		if p := planByTicket[planKey(t.Repo, t.Number)]; p != nil {
			anchorID = p.EscalationCommentID
			nonce = p.EscalationNonce
		}
		answers[planKey(t.Repo, t.Number)] = probeEscalationAnswer(t.Repo, t.Number, anchorID, nonce, budget, out)
	}
	return answers
}

// validAnchor reports whether anchorID/nonce are well-formed enough to even
// attempt a probe (#849): anchorID must be positive and nonce must match
// escalationNoncePattern. Shared by probeEscalationAnswer (so an invalid
// anchor never burns a gh call or the pass's budget) and classifyComments
// (so the pure function is independently correct without relying on its
// impure caller having already checked).
func validAnchor(anchorID int64, nonce string) bool {
	return anchorID > 0 && escalationNoncePattern.MatchString(nonce)
}

// probeEscalationAnswer shells out to the REST comments API for issue number
// in repo, classifying its comment thread into the AnswerProbe closed set
// (#849) by exact stored comment ID + nonce match (classifyComments below).
// A missing/malformed anchor never reaches the network at all --
// AnswerProbeAnchorUnset is returned immediately, without consuming budget or
// making a gh call, mirroring the fail-closed-without-network-cost shape of
// the pass's other gates.
//
// The call is `gh api "repos/<owner>/<repo>/issues/<number>/comments?per_page=100"
// --paginate` (#849 plan Q1: REST over `gh issue view --json comments`, so
// the numeric comment id and `user.type` are first-class fields instead of a
// URL-fragment scrape / login-shape heuristic), bounded by
// ghIssueCommentsTimeout/ghIssueCommentsWaitDelay (watch/docs/go-gotchas.md
// #822) so a hung network call can never stall a dispatch pass indefinitely,
// mirroring mainsync.go's/dependency.go's subprocess conventions per
// watch/AGENTS.md's #825 rule: exec.CommandContext + timeout + WaitDelay +
// separate stdout/stderr buffers (never CombinedOutput) + one collapsed,
// truncated log line. stdout is additionally capped at maxProbeStdoutBytes
// (#849 Risks section): --paginate on a very long thread could otherwise
// return an unbounded payload, and an oversized payload fails closed to
// AnswerProbeUnresolved rather than decoding a truncated/corrupted partial
// JSON payload.
//
// Per-pass call budget (#825 review fix #1 precedent): past maxAnswerProbes,
// no gh call is made at all and the cap-hit line is logged exactly once via
// budget.logged.
func probeEscalationAnswer(repo string, number int, anchorID int64, nonce string, budget *answerProbeBudget, out io.Writer) AnswerProbe {
	if !validAnchor(anchorID, nonce) {
		return AnswerProbeAnchorUnset
	}

	if budget.calls >= maxAnswerProbes {
		if !budget.logged {
			logf(out, "dispatch: escalation answer probe cap (%d) reached, remaining unresolved\n", maxAnswerProbes)
			budget.logged = true
		}
		return AnswerProbeUnresolved
	}
	budget.calls++

	ctx, cancel := context.WithTimeout(context.Background(), ghIssueCommentsTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", repo, number)
	cmd := exec.CommandContext(ctx, "gh", "api", endpoint, "--paginate")
	cmd.WaitDelay = ghIssueCommentsWaitDelay
	var stdout cappedBuffer
	stdout.max = maxProbeStdoutBytes
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	if stdout.exceeded {
		logf(out, "dispatch: escalation answer probe for %s#%d exceeded the %d-byte stdout cap, remaining unresolved\n",
			repo, number, maxProbeStdoutBytes)
		return AnswerProbeUnresolved
	}
	if err != nil {
		detail := truncateDetail(collapseLines(stdout.buf.String()+stderr.String()), maxProbeLogDetailBytes)
		logf(out, "dispatch: escalation answer probe failed for %s#%d: %v (%s)\n", repo, number, err, detail)
		return AnswerProbeUnresolved
	}

	var comments []restIssueComment
	if err := json.Unmarshal(stdout.buf.Bytes(), &comments); err != nil {
		detail := truncateDetail(collapseLines(stdout.buf.String()+stderr.String()), maxProbeLogDetailBytes)
		logf(out, "dispatch: escalation answer probe failed for %s#%d: %v (%s)\n", repo, number, err, detail)
		return AnswerProbeUnresolved
	}
	return classifyComments(comments, anchorID, nonce)
}

// cappedBuffer is an io.Writer that stops accumulating bytes once buf would
// exceed max total bytes, recording that the cap was hit rather than
// silently truncating -- the caller must treat exceeded as a hard failure
// (#849 Risks section), never attempt to decode the partial buf. Write never
// returns an error on overflow (returning len(p), nil for the discarded
// bytes): a write error here would abort exec.Cmd's own output-copying
// goroutine with a spurious "short write" failure instead of the clean,
// classified AnswerProbeUnresolved this cap exists to produce.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.exceeded {
		return len(p), nil
	}
	if c.buf.Len()+len(p) > c.max {
		c.exceeded = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

// maxProbeLogDetailBytes bounds the collapsed gh output logged/errored on a
// failed gh call whose stdout can carry substantial (possibly
// attacker-authored, on a public repo) content (#827 review fix #3): unlike
// dependency.go's small `{"state":...}` payload this logging pattern was
// originally copied from, a call like `--json comments` or `--json
// closingIssuesReferences` can return an entire comment thread or PR list on
// a busy repo — an unbounded log line/error string here would let that
// content flood dispatch's log output, and a ghTimeout kill mid-stream
// (#852 review finding #3) can additionally leave a large partial payload in
// stdout even on an otherwise-small call. Reused at every execGh call site
// in this package whose diagnostic detail is not already known-small
// (currentGitHubLogin/collectRepoTickets in collect.go, openPRInventory in
// openpr.go, countAttempts in reconcile_run.go, probeEscalationAnswer
// below).
const maxProbeLogDetailBytes = 500

// truncationMarker is appended by truncateDetail when it cuts content off,
// counted against max so the result never exceeds max bytes overall (#852:
// dependency.go's maxDependencyTokenBytes cap on an anomaly token relies on
// this being a hard, inclusive bound, not merely a floor).
const truncationMarker = "... (truncated)"

// truncateDetail bounds s to at most max bytes for logging (inclusive of
// truncationMarker when it cuts content off). Byte-based (not rune-aware) is
// acceptable here: this is diagnostic log detail only, never parsed
// downstream, so a severed multi-byte rune at the cut boundary is a cosmetic
// wrinkle at worst.
func truncateDetail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= len(truncationMarker) {
		return s[:max]
	}
	return s[:max-len(truncationMarker)] + truncationMarker
}

// classifyComments is the pure core of the answer probe (#849): it never
// shells out or reads a clock. Anchor identity is the exact stored
// comment ID (anchorID) -- never a content scan for "the last anchor-shaped
// comment" -- so a forged or duplicate marker anywhere else in the thread
// can never become "the" anchor (AC2). anchorID/nonce are validated first
// (validAnchor); a missing/malformed pair fails closed to
// AnswerProbeAnchorUnset before comments are even consulted. Comments are
// sorted by ID ascending before locating the anchor and scanning "after" it
// (#849 Risks section: GitHub REST returns ascending-by-creation and comment
// IDs are monotonic, but sorting here removes any dependency on the API
// actually preserving that order). If no comment in the thread carries
// anchorID, or the comment at that ID does not contain the exact nonce
// marker in its blockquote-stripped body, the result is
// AnswerProbeAnchorMismatch -- distinct from AnswerProbeAnchorUnset, per the
// fail-closed matrix's own distinct-default discipline (#446/#598).
//
// Once the anchor is located and verified, every comment strictly after it
// is scanned for the first one that is an authorized human answer: not
// cenci-authored (isCenciAuthored, a `<!-- cenci-` marker anywhere in its
// blockquote-stripped body -- catches a later duplicate/forged anchor-shaped
// comment too, AC2), not bot-authored (isBotLogin OR author.Type == "Bot" --
// #849 drops isBotLogin from anchor *authentication* entirely, since the
// anchor is now identified by ID, but the answer *filter* keeps it and adds
// the REST-only Type field, AC1/AC4), and carrying an authorized
// authorAssociation (isAuthorizedAssociation -- OWNER/MEMBER/COLLABORATOR
// only, AC4). The first such comment is AnswerProbeAnswered; if none
// qualifies, AnswerProbeWaiting.
//
// Blockquote stripping (stripBlockquoteLines, applied by both isCenciAuthored
// and the anchor's own marker check) is load-bearing for the same reason it
// was pre-#849: a human using GitHub's "Quote reply" on the escalation
// comment copies the marker verbatim into their own reply's `>`-quoted
// lines, and a raw (unstripped) scan would misclassify that quote-reply
// (AC3).
func classifyComments(comments []restIssueComment, anchorID int64, nonce string) AnswerProbe {
	if !validAnchor(anchorID, nonce) {
		return AnswerProbeAnchorUnset
	}

	sorted := make([]restIssueComment, len(comments))
	copy(sorted, comments)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	anchorIdx := -1
	for i, c := range sorted {
		if c.ID == anchorID {
			anchorIdx = i
			break
		}
	}
	if anchorIdx < 0 {
		return AnswerProbeAnchorMismatch
	}

	marker := escalationAnchorPrefix + nonce + " -->"
	if !strings.Contains(stripBlockquoteLines(sorted[anchorIdx].Body), marker) {
		return AnswerProbeAnchorMismatch
	}

	for i := anchorIdx + 1; i < len(sorted); i++ {
		c := sorted[i]
		if isCenciAuthored(c.Body) ||
			isBotLogin(c.Author.Login) || c.Author.Type == "Bot" ||
			!isAuthorizedAssociation(c.AuthorAssociation) {
			continue
		}
		return AnswerProbeAnswered
	}
	return AnswerProbeWaiting
}

// isBotLogin classifies a comment author login as bot-shaped: a `*[bot]`
// suffix (GitHub App-created accounts, e.g. "renovate[bot]") or an `app/*`
// prefix (a GitHub App slug). Purely a fallback signal for the answer filter
// now (#849): the REST API's first-class `user.type == "Bot"` field
// (checked alongside this in classifyComments) is the authoritative bot
// flag; this login-shape heuristic only widens the net for a bot-like
// login the API itself did not flag as Type "Bot".
func isBotLogin(login string) bool {
	return strings.HasSuffix(login, "[bot]") || strings.HasPrefix(login, "app/")
}

// isAuthorizedAssociation reports whether assoc -- a comment's
// `author_association` value -- is trusted to answer an escalation on
// behalf of the repo (#827 review fix #1): `OWNER`, `MEMBER`, or
// `COLLABORATOR`. Deliberately excludes `CONTRIBUTOR`,
// `FIRST_TIME_CONTRIBUTOR`, `NONE`, and any other/unrecognized value --
// those are exactly the untrusted arbitrary-commenter case this check
// exists to close: on a public or wide-collaborator repo, any GitHub user
// can comment on an `Input Needed` issue, and without this check a login
// that merely avoids the bot-shape/cenci-marker exclusions would be enough
// to trigger an unattended `implement` planning session with broad tool
// access.
func isAuthorizedAssociation(assoc string) bool {
	switch assoc {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

// isCenciAuthored reports whether body -- a comment's raw body -- carries a
// hidden `<!-- cenci-<kind> -->` marker (#827; every comment helper in
// reconcile.go embeds one, see cenciMarkerPrefix's doc comment) after
// stripping `>`-quoted blockquote lines first. Blockquote stripping is
// load-bearing: GitHub's "Quote reply" copies the escalation comment's
// anchor HTML verbatim into the reply's own body, so a human quoting it
// would otherwise be misclassified as cenci's own comment. Takes a plain
// body string (not a comment struct, #849): reused identically for both the
// gh-issue-view-shaped and REST-shaped comment types, since a marker check
// only ever needs the body text.
func isCenciAuthored(body string) bool {
	return strings.Contains(stripBlockquoteLines(body), cenciMarkerPrefix)
}

// stripBlockquoteLines removes every line whose first non-space character is
// `>` (a Markdown blockquote line -- GitHub's "Quote reply" feature prefixes
// every quoted line this way), so a marker/anchor copied verbatim into a
// quote is never mistaken for content the commenter authored themselves.
func stripBlockquoteLines(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " \t"), ">") {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}
