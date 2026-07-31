package dispatch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- #827: classifyComments / isBotLogin / isCenciAuthored (pure) ------------

// TestClassifyComments covers the Test Strategy table's answer-classification
// matrix: bot-login forms, cenci-marker forms, quote-reply stripping,
// multi-anchor ordering, no-anchor, empty, (#827 review fix #1)
// authorAssociation authorization, and (#827 review round 2 fix #1) the
// anchor-bearing comment itself being required to pass isBotLogin. Each case
// states the shared contract
// verbatim (types.go/resume.go doc comments quote it too): a comment is a
// human answer iff it is positioned after the last comment containing
// escalationAnchor, its body -- with `>`-prefixed blockquote lines stripped --
// contains no cenciMarkerPrefix, its author login is neither `*[bot]` nor
// `app/*`, and its author association is one of `OWNER`, `MEMBER`, or
// `COLLABORATOR`.
//
// Every fixture below that expects AnswerProbeAnswered sets an authorized
// AuthorAssociation ("COLLABORATOR") explicitly, so this table also pins the
// review fix #1 requirement that the authorization check is an ADDITIONAL
// AND condition alongside the pre-existing bot-login/marker checks, not a
// replacement for them -- an unauthorized comment must not slip through
// merely for being human-login-shaped and marker-free.
func TestClassifyComments(t *testing.T) {
	tests := []struct {
		name     string
		comments []issueComment
		want     AnswerProbe
	}{
		{
			name:     "empty comment thread has no anchor",
			comments: nil,
			want:     AnswerProbeNoAnchor,
		},
		{
			name: "no anchor comment at all",
			comments: []issueComment{
				{Body: "just some ordinary comment", Author: commentAuthor{Login: "octocat"}},
			},
			want: AnswerProbeNoAnchor,
		},
		{
			name: "anchor present, nothing after it",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
			},
			want: AnswerProbeWaiting,
		},
		{
			name: "genuine human reply after the anchor answers",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "Here's my answer.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "COLLABORATOR"},
			},
			want: AnswerProbeAnswered,
		},
		{
			name: "bot-login form [bot] suffix is not a human answer",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "an automated follow-up", Author: commentAuthor{Login: "renovate[bot]"}},
			},
			want: AnswerProbeWaiting,
		},
		{
			name: "bot-login form app/ prefix is not a human answer",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "an automated follow-up", Author: commentAuthor{Login: "app/github-actions"}},
			},
			want: AnswerProbeWaiting,
		},
		{
			name: "cenci-marker form (any <!-- cenci- marker) is not a human answer",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "cenci's own follow-up\n<!-- cenci-dispatch-attempt -->", Author: commentAuthor{Login: "octocat"}},
			},
			want: AnswerProbeWaiting,
		},
		{
			name: "quote-reply followed by genuine new content after the quote answers",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "> Question text.\n> " + escalationAnchor + "\n\nHere is my real answer.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "COLLABORATOR"},
			},
			want: AnswerProbeAnswered,
		},
		{
			name: "multi-anchor ordering: only after the LAST anchor counts",
			comments: []issueComment{
				{Body: "First question.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "A human answer to the first round.", Author: commentAuthor{Login: "octocat"}},
				{Body: "Follow-up question.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
			},
			want: AnswerProbeWaiting,
		},
		{
			name: "multi-anchor ordering: a reply after the second anchor answers",
			comments: []issueComment{
				{Body: "First question.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "A human answer to the first round.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "COLLABORATOR"},
				{Body: "Follow-up question.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "A human answer to the follow-up.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "COLLABORATOR"},
			},
			want: AnswerProbeAnswered,
		},
		{
			name: "OWNER association answers",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "Here's my answer.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "OWNER"},
			},
			want: AnswerProbeAnswered,
		},
		{
			name: "MEMBER association answers",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "Here's my answer.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "MEMBER"},
			},
			want: AnswerProbeAnswered,
		},
		{
			name: "COLLABORATOR association answers",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "Here's my answer.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "COLLABORATOR"},
			},
			want: AnswerProbeAnswered,
		},
		{
			// #827 review fix #1: an untrusted arbitrary commenter on a
			// public/wide-collaborator repo -- otherwise a perfect "human
			// answer" shape (non-bot login, no marker, positioned after the
			// anchor) -- must NOT be treated as an authorized human answer.
			// Dispatch must keep waiting for an authorized human rather than
			// resume from this comment.
			name: "CONTRIBUTOR association is not an authorized human answer",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "Here's my answer.", Author: commentAuthor{Login: "randomuser"}, AuthorAssociation: "CONTRIBUTOR"},
			},
			want: AnswerProbeWaiting,
		},
		{
			name: "NONE association is not an authorized human answer",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "Here's my answer.", Author: commentAuthor{Login: "randomuser"}, AuthorAssociation: "NONE"},
			},
			want: AnswerProbeWaiting,
		},
		{
			// #827 review round 2 fix #1: a fake anchor-shaped comment posted
			// by an unauthorized, non-bot author AFTER the genuine anchor and
			// genuine human answer must not become the new "last anchor" --
			// that would stall auto-resume by resetting the scan past the
			// real answer. The griefer's comment is skipped; the genuine
			// bot-authored anchor from index 0 is still "the" anchor, so the
			// answer at index 1 (already positioned after it) still counts.
			name: "anchor-shaped comment from an unauthorized non-bot author does not override the genuine anchor",
			comments: []issueComment{
				{Body: "Question text.\n" + escalationAnchor, Author: commentAuthor{Login: "cenci-bot[bot]"}},
				{Body: "Here's my answer.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "COLLABORATOR"},
				{Body: "fake anchor to grief the resume\n" + escalationAnchor, Author: commentAuthor{Login: "randomuser"}, AuthorAssociation: "NONE"},
			},
			want: AnswerProbeAnswered,
		},
		{
			// #827 review round 2 fix #1: when the ONLY anchor-shaped comment
			// in the thread comes from an unauthorized, non-bot author, it
			// must never be treated as an anchor at all -- there is nothing
			// genuine to resume from yet.
			name: "anchor-shaped comment from an unauthorized non-bot author with no genuine anchor is no anchor at all",
			comments: []issueComment{
				{Body: "fake anchor, no real escalation ever happened\n" + escalationAnchor, Author: commentAuthor{Login: "randomuser"}, AuthorAssociation: "NONE"},
				{Body: "Here's my answer.", Author: commentAuthor{Login: "octocat"}, AuthorAssociation: "COLLABORATOR"},
			},
			want: AnswerProbeNoAnchor,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyComments(tc.comments)
			if got != tc.want {
				t.Errorf("classifyComments(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsBotLogin(t *testing.T) {
	tests := []struct {
		login string
		want  bool
	}{
		{"octocat", false},
		{"renovate[bot]", true},
		{"app/github-actions", true},
		{"", false},
		{"[bot]", true},
		{"appleseed", false}, // must not false-positive on a login merely starting with "app"
	}
	for _, tc := range tests {
		if got := isBotLogin(tc.login); got != tc.want {
			t.Errorf("isBotLogin(%q) = %v, want %v", tc.login, got, tc.want)
		}
	}
}

// TestIsAuthorizedAssociation pins the #827 review fix #1 closed set: only
// OWNER, MEMBER, and COLLABORATOR are trusted to answer an escalation on
// behalf of the repo. CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR, NONE, and an
// empty/unrecognized value must all be rejected -- default-deny, not an
// enumerated allow-list with a permissive fallback.
func TestIsAuthorizedAssociation(t *testing.T) {
	tests := []struct {
		assoc string
		want  bool
	}{
		{"OWNER", true},
		{"MEMBER", true},
		{"COLLABORATOR", true},
		{"CONTRIBUTOR", false},
		{"FIRST_TIME_CONTRIBUTOR", false},
		{"NONE", false},
		{"", false},
		{"owner", false}, // gh's live schema returns uppercase; must not case-fold
	}
	for _, tc := range tests {
		if got := isAuthorizedAssociation(tc.assoc); got != tc.want {
			t.Errorf("isAuthorizedAssociation(%q) = %v, want %v", tc.assoc, got, tc.want)
		}
	}
}

func TestIsCenciAuthored(t *testing.T) {
	tests := []struct {
		name string
		c    issueComment
		want bool
	}{
		{"plain human body", issueComment{Body: "sounds good, ship it"}, false},
		{"carries a cenci marker", issueComment{Body: "text\n<!-- cenci-dispatch-attempt -->"}, true},
		{"marker only inside a quoted blockquote line is stripped", issueComment{Body: "> <!-- cenci-planner-escalation -->\nmy real reply"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCenciAuthored(tc.c); got != tc.want {
				t.Errorf("isCenciAuthored(%+v) = %v, want %v", tc.c, got, tc.want)
			}
		})
	}
}

// -- #827: probeEscalationAnswer (impure gh adapter) -------------------------

func TestProbeEscalationAnswer_NonzeroExit_UnresolvedWithOneCollapsedLogLine(t *testing.T) {
	installFakeGH(t, "printf 'boom\\nmore boom\\n' >&2\nexit 1\n")

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, &answerProbeBudget{}, &buf)
	if got != AnswerProbeUnresolved {
		t.Fatalf("probe = %q, want AnswerProbeUnresolved", got)
	}
	if strings.Count(buf.String(), "o/r#99") != 1 {
		t.Fatalf("expected exactly one log line naming o/r#99, got: %q", buf.String())
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("expected a single collapsed log line (one trailing newline), got: %q", buf.String())
	}
}

// TestProbeEscalationAnswer_LargeGhOutputOnFailure_LogLineBounded pins #827
// review fix #3: unlike dependency.go's small `{"state":...}` payload this
// logging pattern was copied from, `--json comments` stdout/stderr can carry
// an entire comment thread, so the logged failure detail must be bounded
// even when the fake `gh` fixture returns a large payload -- an unbounded
// log line would let one ticket's comment content flood dispatch's log
// output.
func TestProbeEscalationAnswer_LargeGhOutputOnFailure_LogLineBounded(t *testing.T) {
	installFakeGH(t, `yes x | tr -d '\n' | head -c 5000 >&2
exit 1
`)

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, &answerProbeBudget{}, &buf)
	if got != AnswerProbeUnresolved {
		t.Fatalf("probe = %q, want AnswerProbeUnresolved", got)
	}
	if got := buf.Len(); got > maxProbeLogDetailBytes+200 {
		t.Fatalf("log line not bounded: got %d bytes (want roughly <= %d), first 200 bytes: %q",
			got, maxProbeLogDetailBytes, buf.String()[:200])
	}
}

func TestProbeEscalationAnswer_MalformedJSON_Unresolved(t *testing.T) {
	installFakeGH(t, "printf 'not valid json'\n")

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, &answerProbeBudget{}, &buf)
	if got != AnswerProbeUnresolved {
		t.Fatalf("probe = %q, want AnswerProbeUnresolved", got)
	}
	if !strings.Contains(buf.String(), "o/r#99") {
		t.Fatalf("expected a logged failure detail naming o/r#99, got: %q", buf.String())
	}
}

// TestProbeEscalationAnswer_ExitZeroWithStderrNoise_StillDecodesStdout pins
// the separate-stdout/stderr-buffer requirement (#825 review round 2 fix #2):
// a benign stderr diagnostic on an otherwise-successful (exit 0) call must
// never get merged into the bytes decoded as JSON.
func TestProbeEscalationAnswer_ExitZeroWithStderrNoise_StillDecodesStdout(t *testing.T) {
	body := fmt.Sprintf(`{"comments":[{"body":%q,"author":{"login":"cenci-bot[bot]"}},{"body":"my real answer","author":{"login":"octocat"},"authorAssociation":"COLLABORATOR"}]}`,
		"Question text. "+escalationAnchor)
	installFakeGH(t, fmt.Sprintf(`
printf 'a benign warning on stderr\n' >&2
printf '%s'
`, body))

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, &answerProbeBudget{}, &buf)
	if got != AnswerProbeAnswered {
		t.Fatalf("probe = %q, want AnswerProbeAnswered (stderr noise must not corrupt the decoded stdout JSON)", got)
	}
}

// TestProbeEscalationAnswer_CapBoundsGhCalls covers the 51st-probe case: past
// maxAnswerProbes, no further gh call is made and every call after the cap
// resolves AnswerProbeUnresolved, with the cap-hit line logged exactly once.
func TestProbeEscalationAnswer_CapBoundsGhCalls(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	body := fmt.Sprintf(`{"comments":[{"body":%q,"author":{"login":"cenci-bot[bot]"}},{"body":"my real answer","author":{"login":"octocat"},"authorAssociation":"COLLABORATOR"}]}`,
		"Question text. "+escalationAnchor)
	installFakeGH(t, fmt.Sprintf(`
printf 'x' >> "%s"
printf '%s'
`, countFile, body))

	const extra = 5
	var buf bytes.Buffer
	budget := &answerProbeBudget{}
	for i := 0; i < maxAnswerProbes+extra; i++ {
		got := probeEscalationAnswer("o/r", 1000+i, budget, &buf)
		want := AnswerProbeAnswered
		if i >= maxAnswerProbes {
			want = AnswerProbeUnresolved
		}
		if got != want {
			t.Fatalf("probe[%d] = %q, want %q", i, got, want)
		}
	}

	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("reading call-count file: %v", err)
	}
	if len(data) != maxAnswerProbes {
		t.Fatalf("gh invoked %d time(s), want exactly maxAnswerProbes (%d)", len(data), maxAnswerProbes)
	}

	capMsg := fmt.Sprintf("escalation answer probe cap (%d) reached", maxAnswerProbes)
	if got := strings.Count(buf.String(), capMsg); got != 1 {
		t.Fatalf("cap-hit line naming %q logged %d time(s), want exactly 1: %q", capMsg, got, buf.String())
	}
}

// -- #827: resolveAnswerProbes (label-scoped per-pass loop) ------------------

// TestResolveAnswerProbes_OnlyProbesInputNeededTickets mirrors
// countAttempts' loop precedent (reconcile_run.go's RunReconcileOnce): only
// tickets carrying labelInputNeeded are ever probed; a Planned/Working ticket
// never causes a gh call at all.
func TestResolveAnswerProbes_OnlyProbesInputNeededTickets(t *testing.T) {
	installFakeGH(t, `
case "$1 $2" in
  "issue view") printf '{"comments":[]}' ;;
  *) exit 1 ;;
esac
`)

	tickets := []Ticket{
		{Repo: "o/r", Number: 1, Labels: []string{"Planned"}},
		{Repo: "o/r", Number: 2, Labels: []string{"Input Needed"}},
	}
	got := resolveAnswerProbes(tickets, nil)
	if _, ok := got[planKey("o/r", 1)]; ok {
		t.Errorf("a Planned ticket must never be probed, got entry: %+v", got)
	}
	if got[planKey("o/r", 2)] != AnswerProbeNoAnchor {
		t.Errorf("Input Needed ticket probe = %q, want AnswerProbeNoAnchor (empty comments)", got[planKey("o/r", 2)])
	}
}
