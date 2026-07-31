package dispatch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- #849: classifyComments (pure, ID+nonce anchor matching) -----------------

// TestClassifyComments covers the Test Strategy table's answer-classification
// matrix under the #849 ID+nonce anchor contract: exact-ID+nonce match
// recognizes an ordinary-user-authored anchor (AC1); a later forged/
// duplicate marker-shaped comment (any login) never disqualifies the exact-ID
// match (AC2); a quoted marker inside a candidate answer does not disqualify
// a genuine answer, and an anchor whose only marker occurrence is inside a
// quoted line is not verified (AC3); bot-login forms, `user.type == "Bot"`,
// cenci-marker forms, and every unauthorized authorAssociation after a valid
// anchor all yield Waiting, never Answered (AC4); missing nonce, malformed
// nonce, missing ID, ID absent from the thread, and ID present but nonce
// absent from its body each yield their own distinct probe class (the
// fail-closed matrix, #446/#598 distinct-default discipline).
func TestClassifyComments(t *testing.T) {
	const nonce = "0123456789abcdef0123456789abcdef"
	marker := func(n string) string { return escalationAnchorPrefix + n + " -->" }
	user := func(login, typ string) restCommentAuthor { return restCommentAuthor{Login: login, Type: typ} }

	tests := []struct {
		name     string
		comments []restIssueComment
		anchorID int64
		nonce    string
		want     AnswerProbe
	}{
		{
			name:     "empty comment thread with a well-formed anchor is a mismatch (nothing to match against)",
			comments: nil,
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnchorMismatch,
		},
		{
			name: "ordinary user-authored anchor, exact ID+nonce match, authorized reply answers (#849 AC1)",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("matteobortolazzo", "User")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnswered,
		},
		{
			name: "a later forged/duplicate marker comment (any login) never disqualifies the exact-ID match (#849 AC2)",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
				{ID: 102, Body: "forged\n" + marker(nonce), Author: user("randomuser", "User")},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnswered,
		},
		{
			name: "a quoted marker inside a candidate answer does not disqualify a genuine answer (#849 AC3)",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "> Question text.\n> " + marker(nonce) + "\n\nHere is my real answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnswered,
		},
		{
			name: "anchor's own marker occurring only inside a quoted line is not a genuine anchor (#849 AC3)",
			comments: []restIssueComment{
				{ID: 100, Body: "> Original question.\n> " + marker(nonce), Author: user("cenci-bot", "Bot")},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnchorMismatch,
		},
		{
			name: "author.Type == Bot excludes a candidate reply even with a non-bot-shaped login (#849 AC4)",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "an automated follow-up", Author: user("plainlogin", "Bot"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeWaiting,
		},
		{
			name: "bot-login form [bot] suffix excludes a candidate reply even with Type User",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "an automated follow-up", Author: user("renovate[bot]", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeWaiting,
		},
		{
			name: "bot-login form app/ prefix excludes a candidate reply even with Type User",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "an automated follow-up", Author: user("app/github-actions", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeWaiting,
		},
		{
			name: "cenci-marker form (any <!-- cenci- marker) is not a human answer",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "cenci's own follow-up\n<!-- cenci-dispatch-attempt -->", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeWaiting,
		},
		{
			name: "OWNER association answers (#849 AC4)",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "OWNER"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnswered,
		},
		{
			name: "MEMBER association answers (#849 AC4)",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "MEMBER"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnswered,
		},
		{
			name: "CONTRIBUTOR association after a valid anchor is not authorized (#849 AC4)",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("randomuser", "User"), AuthorAssociation: "CONTRIBUTOR"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeWaiting,
		},
		{
			name: "NONE association after a valid anchor is not authorized (#849 AC4)",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("randomuser", "User"), AuthorAssociation: "NONE"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeWaiting,
		},
		{
			name: "nothing after the anchor -> waiting",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeWaiting,
		},
		{
			name: "missing nonce (empty) fails closed distinctly from a content mismatch",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: "",
			want: AnswerProbeAnchorUnset,
		},
		{
			name: "malformed nonce (fails escalationNoncePattern) fails closed as anchor-unset",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: "not-hex-and-wrong-length",
			want: AnswerProbeAnchorUnset,
		},
		{
			name: "missing ID (anchorID <= 0) fails closed as anchor-unset",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 0, nonce: nonce,
			want: AnswerProbeAnchorUnset,
		},
		{
			name: "negative ID fails closed as anchor-unset",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
			},
			anchorID: -1, nonce: nonce,
			want: AnswerProbeAnchorUnset,
		},
		{
			name: "ID absent from the thread fails closed as anchor-mismatch, distinct from anchor-unset",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 999, nonce: nonce,
			want: AnswerProbeAnchorMismatch,
		},
		{
			name: "ID present but the nonce is absent from that comment's body fails closed as anchor-mismatch",
			comments: []restIssueComment{
				{ID: 100, Body: "Question text with no marker at all.", Author: user("cenci-bot", "Bot")},
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnchorMismatch,
		},
		{
			name: "comments supplied out of ID order are sorted before locating the anchor and scanning after it",
			comments: []restIssueComment{
				{ID: 101, Body: "Here's my answer.", Author: user("octocat", "User"), AuthorAssociation: "COLLABORATOR"},
				{ID: 100, Body: "Question text.\n" + marker(nonce), Author: user("cenci-bot", "Bot")},
			},
			anchorID: 100, nonce: nonce,
			want: AnswerProbeAnswered,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyComments(tc.comments, tc.anchorID, tc.nonce)
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
		body string
		want bool
	}{
		{"plain human body", "sounds good, ship it", false},
		{"carries a cenci marker", "text\n<!-- cenci-dispatch-attempt -->", true},
		{"marker only inside a quoted blockquote line is stripped", "> <!-- cenci-planner-escalation:0123456789abcdef0123456789abcdef -->\nmy real reply", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCenciAuthored(tc.body); got != tc.want {
				t.Errorf("isCenciAuthored(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// -- #849: probeEscalationAnswer (impure REST adapter) -----------------------

const testNonce = "0123456789abcdef0123456789abcdef"

func TestProbeEscalationAnswer_AnchorUnset_NeverCallsGh(t *testing.T) {
	installFakeGH(t, "echo 'gh must not be invoked for an unset anchor' >&2\nexit 1\n")

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, 0, "", &answerProbeBudget{}, &buf)
	if got != AnswerProbeAnchorUnset {
		t.Fatalf("probe = %q, want AnswerProbeAnchorUnset", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no log output (no gh call attempted), got: %q", buf.String())
	}
}

func TestProbeEscalationAnswer_NonzeroExit_UnresolvedWithOneCollapsedLogLine(t *testing.T) {
	installFakeGH(t, "printf 'boom\\nmore boom\\n' >&2\nexit 1\n")

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, 100, testNonce, &answerProbeBudget{}, &buf)
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
// logging pattern was copied from, the comments endpoint's stdout/stderr can
// carry an entire comment thread, so the logged failure detail must be
// bounded even when the fake `gh` fixture returns a large payload.
func TestProbeEscalationAnswer_LargeGhOutputOnFailure_LogLineBounded(t *testing.T) {
	installFakeGH(t, `yes x | tr -d '\n' | head -c 5000 >&2
exit 1
`)

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, 100, testNonce, &answerProbeBudget{}, &buf)
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
	got := probeEscalationAnswer("o/r", 99, 100, testNonce, &answerProbeBudget{}, &buf)
	if got != AnswerProbeUnresolved {
		t.Fatalf("probe = %q, want AnswerProbeUnresolved", got)
	}
	if !strings.Contains(buf.String(), "o/r#99") {
		t.Fatalf("expected a logged failure detail naming o/r#99, got: %q", buf.String())
	}
}

// restCommentsJSON renders a minimal REST-shaped comments array JSON body: an
// anchor comment (bot-authored) followed by a qualifying human reply.
func restCommentsJSON(nonce string) string {
	return fmt.Sprintf(
		`[{"id":100,"body":%q,"user":{"login":"cenci-bot","type":"Bot"}},`+
			`{"id":101,"body":"my real answer","user":{"login":"octocat","type":"User"},"author_association":"COLLABORATOR"}]`,
		"Question text. "+escalationAnchorPrefix+nonce+" -->")
}

// TestProbeEscalationAnswer_ExitZeroWithStderrNoise_StillDecodesStdout pins
// the separate-stdout/stderr-buffer requirement (#825 review round 2 fix #2):
// a benign stderr diagnostic on an otherwise-successful (exit 0) call must
// never get merged into the bytes decoded as JSON.
func TestProbeEscalationAnswer_ExitZeroWithStderrNoise_StillDecodesStdout(t *testing.T) {
	installFakeGH(t, fmt.Sprintf(`
printf 'a benign warning on stderr\n' >&2
printf '%s'
`, restCommentsJSON(testNonce)))

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, 100, testNonce, &answerProbeBudget{}, &buf)
	if got != AnswerProbeAnswered {
		t.Fatalf("probe = %q, want AnswerProbeAnswered (stderr noise must not corrupt the decoded stdout JSON)", got)
	}
}

// TestProbeEscalationAnswer_OversizedPaginatePayload_Unresolved covers the
// Test Strategy table's deferred gap (#849 Phase 3 note): a `--paginate`
// payload exceeding maxProbeStdoutBytes must fail closed to
// AnswerProbeUnresolved rather than decoding a truncated/corrupted partial
// JSON payload.
func TestProbeEscalationAnswer_OversizedPaginatePayload_Unresolved(t *testing.T) {
	installFakeGH(t, fmt.Sprintf("yes x | tr -d '\\n' | head -c %d\n", maxProbeStdoutBytes+1000))

	var buf bytes.Buffer
	got := probeEscalationAnswer("o/r", 99, 100, testNonce, &answerProbeBudget{}, &buf)
	if got != AnswerProbeUnresolved {
		t.Fatalf("probe = %q, want AnswerProbeUnresolved (oversized --paginate payload must fail closed)", got)
	}
	if !strings.Contains(buf.String(), "o/r#99") {
		t.Fatalf("expected a logged detail naming o/r#99, got: %q", buf.String())
	}
}

// TestProbeEscalationAnswer_CapBoundsGhCalls covers the 51st-probe case: past
// maxAnswerProbes, no further gh call is made and every call after the cap
// resolves AnswerProbeUnresolved, with the cap-hit line logged exactly once.
func TestProbeEscalationAnswer_CapBoundsGhCalls(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	body := restCommentsJSON(testNonce)
	installFakeGH(t, fmt.Sprintf(`
printf 'x' >> "%s"
printf '%s'
`, countFile, body))

	const extra = 5
	var buf bytes.Buffer
	budget := &answerProbeBudget{}
	for i := 0; i < maxAnswerProbes+extra; i++ {
		got := probeEscalationAnswer("o/r", 1000+i, 100, testNonce, budget, &buf)
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

// -- #827/#849: resolveAnswerProbes (label-scoped per-pass loop) -------------

// TestResolveAnswerProbes_OnlyProbesInputNeededTickets mirrors
// countAttempts' loop precedent (reconcile_run.go's RunReconcileOnce): only
// tickets carrying labelInputNeeded are ever probed; a Planned/Working ticket
// never causes a gh call at all. Ticket #2 has no matching plan in
// planByTicket, so its anchor is unset and it resolves without ever shelling
// out to gh (proving the anchor-unset short-circuit is wired through
// resolveAnswerProbes, not just probeEscalationAnswer in isolation).
func TestResolveAnswerProbes_OnlyProbesInputNeededTickets(t *testing.T) {
	installFakeGH(t, "echo 'gh must not be invoked' >&2\nexit 1\n")

	tickets := []Ticket{
		{Repo: "o/r", Number: 1, Labels: []string{"Planned"}},
		{Repo: "o/r", Number: 2, Labels: []string{"Input Needed"}},
	}
	got := resolveAnswerProbes(tickets, map[string]*Plan{}, nil)
	if _, ok := got[planKey("o/r", 1)]; ok {
		t.Errorf("a Planned ticket must never be probed, got entry: %+v", got)
	}
	if got[planKey("o/r", 2)] != AnswerProbeAnchorUnset {
		t.Errorf("Input Needed ticket with no matched plan probe = %q, want AnswerProbeAnchorUnset", got[planKey("o/r", 2)])
	}
}

// TestResolveAnswerProbes_UsesMatchedPlanAnchorFields covers the plan index
// wiring end to end: a matched Plan's EscalationCommentID/EscalationNonce
// are threaded into the REST probe, and a real gh call is made when the
// anchor is well-formed.
func TestResolveAnswerProbes_UsesMatchedPlanAnchorFields(t *testing.T) {
	installFakeGH(t, fmt.Sprintf("printf '%s'\n", restCommentsJSON(testNonce)))

	tickets := []Ticket{
		{Repo: "o/r", Number: 2, Labels: []string{"Input Needed"}},
	}
	planByTicket := map[string]*Plan{
		planKey("o/r", 2): {Repo: "o/r", TicketID: 2, EscalationCommentID: 100, EscalationNonce: testNonce},
	}
	got := resolveAnswerProbes(tickets, planByTicket, nil)
	if got[planKey("o/r", 2)] != AnswerProbeAnswered {
		t.Errorf("probe = %q, want AnswerProbeAnswered", got[planKey("o/r", 2)])
	}
}
