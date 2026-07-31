package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// escalationAnchor is the hidden HTML comment the unattended planner stamps
// on its escalation comment (#826) when it stops cleanly to ask a human a
// question. classifyComments treats it as the pass/fail line for "is there a
// human answer yet": only a comment positioned strictly after the LAST
// comment containing this anchor can ever classify as an answer.
//
// Shared contract (quoted verbatim, per the plan's Risks section, in both
// this file and flow/skills/implement/phases/phase-1-plan.md's
// `## Resume From Draft` section, and asserted as a literal by
// flow/tests/dispatch-resume-contract.test.sh): a comment is a human answer
// iff it is positioned after the last comment containing
// `<!-- cenci-planner-escalation -->`, its body — with `>`-prefixed
// blockquote lines stripped — contains no `<!-- cenci-` marker, its author
// login is neither `*[bot]` nor `app/*`, and its author association is one
// of `OWNER`, `MEMBER`, or `COLLABORATOR` (#827 review fix #1: any other
// association — e.g. `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, or `NONE` — is
// never an authorized human answer, no matter how human-shaped the comment
// otherwise looks; this closes the untrusted-arbitrary-commenter hole on a
// public or wide-collaborator repo).
const escalationAnchor = "<!-- cenci-planner-escalation -->"

// ghIssueCommentsTimeout bounds every individual `gh issue view --json
// comments` escalation-answer probe call (#827), mirroring
// ghIssueViewTimeout's rationale (dependency.go): a hung network call must
// never stall a dispatch pass indefinitely.
const ghIssueCommentsTimeout = 60 * time.Second

// ghIssueCommentsWaitDelay bounds how long cmd.Wait can block after the gh
// process itself has exited or been killed by ghIssueCommentsTimeout's
// context, mirroring ghIssueViewWaitDelay (dependency.go) --
// watch/docs/go-gotchas.md's cmd.WaitDelay rule (#822).
const ghIssueCommentsWaitDelay = 5 * time.Second

// maxAnswerProbes caps how many `gh issue view` escalation-answer probe
// calls one RunOnce pass may make (#827), mirroring maxDependencyResolutions
// (dependency.go, #825 review fix #1): without a cap, a large fleet of
// `Input Needed` tickets could spawn unbounded gh subprocesses / GitHub API
// calls per pass. Once the cap is reached, any further probe resolves
// directly to AnswerProbeUnresolved without shelling out -- still fails
// closed (never resumes), it just stops making gh calls.
const maxAnswerProbes = 50

// answerProbeBudget bounds and tracks one RunOnce pass's total escalation
// answer probe gh calls, mirroring dependencyResolutionBudget. logged tracks
// whether the cap-hit line has already been emitted this pass, so hitting
// the cap logs exactly once regardless of how many further tickets follow.
type answerProbeBudget struct {
	calls  int
	logged bool
}

// commentAuthor is the `author` sub-object gh's `--json comments` returns
// for each comment. Empirically verified against gh's live schema (the plan's
// Open Question): `gh issue view --json comments` exposes only `author.login`
// -- no `author.is_bot` field -- so bot detection here is necessarily
// login-shape-based (isBotLogin), not a dedicated bot flag.
type commentAuthor struct {
	Login string `json:"login"`
}

// issueComment is one entry of gh's `--json comments` payload, the fields
// classifyComments needs. AuthorAssociation is included automatically
// whenever `comments` is requested — gh exposes no way to select individual
// comment sub-fields, so no extra `--json` flag is needed to obtain it
// (empirically verified against gh's live schema, #827 review fix #1:
// `gh issue view <n> --repo matteobortolazzo/cenci --json comments` against
// a real commented issue in this repo returned `"authorAssociation": "OWNER"`
// per comment with no additional flag).
type issueComment struct {
	Body              string        `json:"body"`
	Author            commentAuthor `json:"author"`
	AuthorAssociation string        `json:"authorAssociation"`
}

// resolveAnswerProbes probes every `Input Needed` ticket in tickets for a
// human answer to its escalation (#827), keyed by planKey(repo, number) so
// Decide's Inputs.Answers can look it up without threading the probe through
// Ticket at all -- mirrors RunReconcileOnce's countAttempts loop
// (reconcile_run.go:349-368), the label-scoped per-ticket gh read outside the
// collector precedent. Bounded by maxAnswerProbes across the whole call.
func resolveAnswerProbes(tickets []Ticket, out io.Writer) map[string]AnswerProbe {
	answers := make(map[string]AnswerProbe)
	budget := &answerProbeBudget{}
	for _, t := range tickets {
		if !hasLabel(t.Labels, labelInputNeeded) {
			continue
		}
		answers[planKey(t.Repo, t.Number)] = probeEscalationAnswer(t.Repo, t.Number, budget, out)
	}
	return answers
}

// probeEscalationAnswer shells out to `gh issue view` for issue number in
// repo, classifying its comment thread into the AnswerProbe closed set
// (#827). A nonzero exit or malformed JSON fails closed to
// AnswerProbeUnresolved rather than assuming answered or waiting, and logs
// one collapsed line naming repo+number (mirrors fetchDependencyState,
// dependency.go). The call is bounded by ghIssueCommentsTimeout/
// ghIssueCommentsWaitDelay (watch/docs/go-gotchas.md #822) so a hung network
// call can never stall a dispatch pass indefinitely.
//
// stdout and stderr are captured into separate buffers rather than via
// CombinedOutput (#825 review round 2 fix #2, restated per watch/AGENTS.md's
// #825 rule): a benign stderr diagnostic on an otherwise-successful (exit 0)
// call would otherwise get merged into the bytes decoded as JSON, corrupting
// the payload and silently misclassifying the result as Unresolved. Only
// stdout is decoded; stderr (collapsed alongside stdout for the log line,
// truncated to maxProbeLogDetailBytes — #827 review fix #3, unlike
// fetchDependencyState's small `{"state":...}` payload this pattern was
// copied from, `--json comments` stdout can be an entire comment thread) is
// diagnostic detail only.
//
// Per-pass call budget (#825 review fix #1 precedent): past maxAnswerProbes,
// no gh call is made at all and the cap-hit line is logged exactly once via
// budget.logged.
func probeEscalationAnswer(repo string, number int, budget *answerProbeBudget, out io.Writer) AnswerProbe {
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
	cmd := exec.CommandContext(ctx, "gh", "issue", "view", strconv.Itoa(number),
		"--repo", repo, "--json", "comments")
	cmd.WaitDelay = ghIssueCommentsWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := truncateDetail(collapseLines(stdout.String()+stderr.String()), maxProbeLogDetailBytes)
		logf(out, "dispatch: escalation answer probe failed for %s#%d: %v (%s)\n", repo, number, err, detail)
		return AnswerProbeUnresolved
	}

	var v struct {
		Comments []issueComment `json:"comments"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &v); err != nil {
		detail := truncateDetail(collapseLines(stdout.String()+stderr.String()), maxProbeLogDetailBytes)
		logf(out, "dispatch: escalation answer probe failed for %s#%d: %v (%s)\n", repo, number, err, detail)
		return AnswerProbeUnresolved
	}
	return classifyComments(v.Comments)
}

// maxProbeLogDetailBytes bounds the collapsed gh output logged on a failed
// escalation-answer probe (#827 review fix #3): unlike dependency.go's small
// `{"state":...}` payload this logging pattern was copied from, `--json
// comments` stdout can be the entire comment thread on a busy ticket — an
// unbounded log line here would let one ticket's comment content flood
// dispatch's log output.
const maxProbeLogDetailBytes = 500

// truncateDetail bounds s to at most max bytes for logging, appending a
// truncation marker when it cuts content off. Byte-based (not rune-aware) is
// acceptable here: this is diagnostic log detail only, never parsed
// downstream, so a severed multi-byte rune at the cut boundary is a cosmetic
// wrinkle at worst.
func truncateDetail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "... (truncated)"
}

// classifyComments is the pure core of the answer probe (#827): it never
// shells out or reads a clock. It implements the shared detection contract
// (see escalationAnchor's doc comment, quoted verbatim there and in
// phase-1-plan.md's `## Resume From Draft` section): find the LAST comment
// (by position, not timestamp -- gh's comments array is already
// chronologically ordered) whose blockquote-stripped body contains
// escalationAnchor AND whose author login is bot-shaped (isBotLogin, #827
// review round 2 fix #1); an empty thread or no such comment is
// AnswerProbeNoAnchor. Requiring the anchor-bearing comment itself to be
// bot-authored closes a griefing/misattribution hole: without it, any
// commenter -- including an unauthorized NONE/CONTRIBUTOR association -- could
// post an anchor-shaped comment and become "the last anchor," either stalling
// auto-resume (a fake anchor posted after the genuine one resets the scan
// past the real human answer) or misattributing a later OWNER/MEMBER/
// COLLABORATOR comment as the answer to a question that was never actually
// escalated. A non-bot-authored anchor-shaped comment is simply skipped --
// the scan keeps looking for the next most recent genuinely bot-authored
// anchor, falling back to AnswerProbeNoAnchor if none exists. Otherwise, scan
// every comment strictly after it: the
// first one that is neither cenci-authored (isCenciAuthored) nor
// bot-authored (isBotLogin), AND carries an authorized authorAssociation
// (isAuthorizedAssociation -- #827 review fix #1) is a human answer
// (AnswerProbeAnswered); if none qualifies, AnswerProbeWaiting -- including
// when every otherwise-qualifying comment is skipped solely for lacking an
// authorized association. That is deliberate: an unauthorized commenter on a
// public/wide-collaborator repo must never trigger a resumed unattended
// planning session, but it is also not itself an error, so dispatch keeps
// waiting for an authorized human rather than failing closed to
// AnswerProbeUnresolved (which would imply the probe itself broke).
//
// The anchor scan itself must strip blockquote lines first, not just the
// marker check below: a human using GitHub's "Quote reply" on the escalation
// comment copies the anchor HTML verbatim into their own reply's `>`-quoted
// lines. Scanning the raw (unstripped) body for the anchor would then
// misidentify that quote-reply as a NEW anchor-bearing comment, resetting
// "last anchor" past the genuine answer that follows it -- exactly the
// quote-reply false negative this rule exists to prevent.
func classifyComments(comments []issueComment) AnswerProbe {
	stripped := make([]string, len(comments))
	for i, c := range comments {
		stripped[i] = stripBlockquoteLines(c.Body)
	}

	lastAnchor := -1
	for i, b := range stripped {
		if strings.Contains(b, escalationAnchor) && isBotLogin(comments[i].Author.Login) {
			lastAnchor = i
		}
	}
	if lastAnchor < 0 {
		return AnswerProbeNoAnchor
	}
	for i := lastAnchor + 1; i < len(comments); i++ {
		if strings.Contains(stripped[i], cenciMarkerPrefix) ||
			isBotLogin(comments[i].Author.Login) ||
			!isAuthorizedAssociation(comments[i].AuthorAssociation) {
			continue
		}
		return AnswerProbeAnswered
	}
	return AnswerProbeWaiting
}

// isBotLogin classifies a gh comment author login as bot-shaped: a `*[bot]`
// suffix (GitHub App-created accounts, e.g. "renovate[bot]") or an `app/*`
// prefix (a GitHub App slug). Login-shape-based, not a dedicated bot flag --
// gh's `--json comments` payload exposes only `author.login`, no
// `author.is_bot` (verified empirically against gh's live schema; the plan's
// Open Question). A self-hosted automation posting under a plain user login
// would be misread as human -- bounded impact, see watch/README.md.
//
// Doubles as classifyComments' anchor-authorship gate (#827 review round 2
// fix #1): cenci's own escalation-posting identity is bot-shaped (see every
// anchor fixture in resume_test.go), so requiring the anchor-bearing comment
// itself to pass isBotLogin is how the lastAnchor scan tells a genuine
// cenci-posted anchor apart from an arbitrary commenter's anchor-shaped text.
func isBotLogin(login string) bool {
	return strings.HasSuffix(login, "[bot]") || strings.HasPrefix(login, "app/")
}

// isAuthorizedAssociation reports whether assoc -- a gh comment's
// `authorAssociation` value -- is trusted to answer an escalation on behalf
// of the repo (#827 review fix #1): `OWNER`, `MEMBER`, or `COLLABORATOR`.
// Deliberately excludes `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, `NONE`, and
// any other/unrecognized value -- those are exactly the untrusted
// arbitrary-commenter case this check exists to close: on a public or
// wide-collaborator repo, any GitHub user can comment on an `Input Needed`
// issue, and without this check a login that merely avoids the
// bot-shape/cenci-marker exclusions would be enough to trigger an unattended
// `implement` planning session with broad tool access.
func isAuthorizedAssociation(assoc string) bool {
	switch assoc {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

// isCenciAuthored reports whether c is a comment cenci itself posted --
// carrying a hidden `<!-- cenci-<kind> -->` marker (#827; every comment
// helper in reconcile.go embeds one, see cenciMarkerPrefix's doc comment) --
// after stripping `>`-quoted blockquote lines first. Blockquote stripping is
// load-bearing: GitHub's "Quote reply" copies the escalation comment's
// anchor HTML verbatim into the reply's own body, so a human quoting it would
// otherwise be misclassified as cenci's own comment.
func isCenciAuthored(c issueComment) bool {
	return strings.Contains(stripBlockquoteLines(c.Body), cenciMarkerPrefix)
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
