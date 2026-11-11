package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// -- discussion read, revision, receipt and permission tests (#1472) ----------
//
// These tests are unit-level on purpose: every function under test is a pure
// parse/verify decision (or a gh read behind the execGh seam), and each case
// maps to one acceptance criterion or edge case of the ticket. They assert
// returned values and decisions, never call sequences.

const (
	discussionTestRepo = "octo/repo"
	discussionTestPR   = "7"

	// 32-hex nonces: the launch's own, and the one a spoofer guesses.
	testReviewNonce = "0123456789abcdef0123456789abcdef"
	testWrongNonce  = "fedcba9876543210fedcba9876543210"

	testReceiptHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// testLaunchAt carries a sub-second part on purpose: verifyReceipt must
// truncate it to the second before comparing.
var testLaunchAt = time.Date(2026, 11, 10, 12, 0, 0, 700_000_000, time.UTC)

// ghReply is one scripted `gh` answer.
type ghReply struct {
	out string
	err error
}

// withDiscussionGh replaces the execGh seam with a path-switch fake: every
// `gh api <path>` is answered from replies by exact path, and an unscripted
// path fails loudly. It returns the recorded paths.
func withDiscussionGh(t *testing.T, replies map[string]ghReply) *[]string {
	t.Helper()
	var calls []string
	original := execGh
	execGh = func(args ...string) (string, string, error) {
		path := strings.Join(args, " ")
		calls = append(calls, path)
		r, ok := replies[path]
		if !ok {
			return "", "", fmt.Errorf("unscripted gh call: %s", path)
		}
		return r.out, "", r.err
	}
	t.Cleanup(func() { execGh = original })
	return &calls
}

func discussionPagePath(page int) string {
	return fmt.Sprintf("api repos/%s/issues/%s/comments?per_page=%d&page=%d", discussionTestRepo, discussionTestPR, feedbackPageSize, page)
}

// discussionJSON renders one wire comment object.
func discussionJSON(id int64, login, body, created, updated string) string {
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"id":%d,"user":{"login":%q},"body":%s,"created_at":%q,"updated_at":%q}`, id, login, b, created, updated)
}

func jsonArray(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

// fullDiscussionPage renders feedbackPageSize human comments sharing one
// created_at/updated_at pair, ids starting at start.
func fullDiscussionPage(start int64) string {
	items := make([]string, feedbackPageSize)
	for i := range items {
		id := start + int64(i)
		items[i] = discussionJSON(id, "alice", fmt.Sprintf("comment %d", id), "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z")
	}
	return jsonArray(items...)
}

// -- fetchDiscussion ----------------------------------------------------------

func TestDiscussionFetchAssemblesPagesAndKeepsEqualTimestamps(t *testing.T) {
	withDiscussionGh(t, map[string]ghReply{
		discussionPagePath(1): {out: fullDiscussionPage(1)},
		discussionPagePath(2): {out: jsonArray(discussionJSON(101, "bob", "second page", "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z"))},
	})

	got, complete, err := fetchDiscussion(discussionTestRepo, discussionTestPR)
	if err != nil {
		t.Fatalf("fetchDiscussion: %v", err)
	}
	if !complete {
		t.Fatal("a short last page must report complete=true")
	}
	if len(got) != feedbackPageSize+1 {
		t.Fatalf("len = %d, want %d (every comment retained, timestamps do not dedup)", len(got), feedbackPageSize+1)
	}
	last := got[len(got)-1]
	want := discussionComment{ID: 101, Login: "bob", Body: "second page", CreatedAt: "2026-11-10T11:00:00Z", UpdatedAt: "2026-11-10T11:00:00Z"}
	if last != want {
		t.Fatalf("last = %+v, want %+v", last, want)
	}
	if got[0].CreatedAt != last.CreatedAt || got[0].ID == last.ID {
		t.Fatalf("equal-timestamp comments must both be retained: %+v vs %+v", got[0], last)
	}
}

func TestDiscussionFetchEmptyBodyIsValid(t *testing.T) {
	withDiscussionGh(t, map[string]ghReply{
		discussionPagePath(1): {out: jsonArray(discussionJSON(5, "alice", "", "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z"))},
	})
	got, complete, err := fetchDiscussion(discussionTestRepo, discussionTestPR)
	if err != nil || !complete || len(got) != 1 || got[0].Body != "" {
		t.Fatalf("got=%+v complete=%v err=%v; an empty-string body is present, not malformed", got, complete, err)
	}
}

func TestDiscussionFetchZeroCommentsIsCompleteAndEmpty(t *testing.T) {
	withDiscussionGh(t, map[string]ghReply{discussionPagePath(1): {out: "[]"}})
	got, complete, err := fetchDiscussion(discussionTestRepo, discussionTestPR)
	if err != nil || !complete || len(got) != 0 {
		t.Fatalf("got=%+v complete=%v err=%v, want an empty complete read", got, complete, err)
	}
}

func TestDiscussionFetchAPIFailureIsAnErrorNeverAnEmptySet(t *testing.T) {
	withDiscussionGh(t, map[string]ghReply{discussionPagePath(1): {err: errors.New("exit status 1")}})
	got, complete, err := fetchDiscussion(discussionTestRepo, discussionTestPR)
	if err == nil {
		t.Fatalf("a gh failure must be an error, got comments=%+v complete=%v", got, complete)
	}
	if complete || got != nil {
		t.Fatalf("a failed read must return no comments and complete=false, got %+v complete=%v", got, complete)
	}
}

func TestDiscussionFetchMidPaginationFailureIsAnError(t *testing.T) {
	withDiscussionGh(t, map[string]ghReply{
		discussionPagePath(1): {out: fullDiscussionPage(1)},
		discussionPagePath(2): {err: errors.New("exit status 1")},
	})
	got, complete, err := fetchDiscussion(discussionTestRepo, discussionTestPR)
	if err == nil || complete || got != nil {
		t.Fatalf("got=%d complete=%v err=%v, want an error with no partial result", len(got), complete, err)
	}
}

func TestDiscussionFetchUndecodablePageIsAnError(t *testing.T) {
	withDiscussionGh(t, map[string]ghReply{discussionPagePath(1): {out: "not json"}})
	_, _, err := fetchDiscussion(discussionTestRepo, discussionTestPR)
	if err == nil {
		t.Fatal("a page that does not decode must be an error")
	}
}

func TestDiscussionFetchMalformedObjectsAreErrors(t *testing.T) {
	cases := []struct {
		name string
		page string
	}{
		{"missing id", `[{"user":{"login":"alice"},"body":"x","created_at":"2026-11-10T11:00:00Z","updated_at":"2026-11-10T11:00:00Z"}]`},
		{"null id", `[{"id":null,"user":{"login":"alice"},"body":"x"}]`},
		{"zero id", `[{"id":0,"user":{"login":"alice"},"body":"x"}]`},
		{"missing user", `[{"id":1,"body":"x"}]`},
		{"null user", `[{"id":1,"user":null,"body":"x"}]`},
		{"user without login", `[{"id":1,"user":{},"body":"x"}]`},
		{"null login", `[{"id":1,"user":{"login":null},"body":"x"}]`},
		{"missing body", `[{"id":1,"user":{"login":"alice"}}]`},
		{"null body", `[{"id":1,"user":{"login":"alice"},"body":null}]`},
		{"null element", `[null]`},
		{"one bad object among good ones", jsonArray(
			discussionJSON(1, "alice", "fine", "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z"),
			`{"id":2,"user":{"login":"bob"}}`,
		)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDiscussionGh(t, map[string]ghReply{discussionPagePath(1): {out: tc.page}})
			got, _, err := fetchDiscussion(discussionTestRepo, discussionTestPR)
			if !errors.Is(err, errDiscussionMalformed) {
				t.Fatalf("err = %v, want errDiscussionMalformed", err)
			}
			if got != nil {
				t.Fatalf("a malformed read must return no comments, got %+v", got)
			}
		})
	}
}

func TestDiscussionFetchPageCapReachedIsIncompleteNotAnError(t *testing.T) {
	replies := map[string]ghReply{}
	for page := 1; page <= maxFeedbackPages; page++ {
		replies[discussionPagePath(page)] = ghReply{out: fullDiscussionPage(int64(page-1)*feedbackPageSize + 1)}
	}
	withDiscussionGh(t, replies)

	got, complete, err := fetchDiscussion(discussionTestRepo, discussionTestPR)
	if err != nil {
		t.Fatalf("hitting the page cap is a truncation, not an unreadable read: %v", err)
	}
	if complete {
		t.Fatal("full pages through the cap must report complete=false")
	}
	if len(got) != maxFeedbackPages*feedbackPageSize {
		t.Fatalf("len = %d, want %d", len(got), maxFeedbackPages*feedbackPageSize)
	}
}

// -- bodyRevision -------------------------------------------------------------

func TestBodyRevisionIsHexSHA256OfTheBody(t *testing.T) {
	const abcSHA256 = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := bodyRevision("abc"); got != abcSHA256 {
		t.Fatalf("bodyRevision(abc) = %q, want %q", got, abcSHA256)
	}
}

func TestBodyRevisionChangesWhenTheBodyIsEdited(t *testing.T) {
	before := bodyRevision("please rename the flag")
	after := bodyRevision("please rename the flag, then update the docs")
	if before == after {
		t.Fatal("an edited body must yield a new revision")
	}
	if bodyRevision("please rename the flag") != before {
		t.Fatal("the same body must always yield the same revision")
	}
}

// -- human discussion filter and revision map --------------------------------

func TestDiscussionHumanFilterKeepsOnlyUnattributedHumanComments(t *testing.T) {
	comments := []discussionComment{
		{ID: 1, Login: "alice", Body: "please fix the typo"},
		{ID: 2, Login: "dependabot[bot]", Body: "bump x"},
		{ID: 3, Login: "alice", Body: "> 🤖 **cenci** — review receipt posted by `/cenci:address-review` (babysit-review).\n<!-- cenci-review-receipt:" + testReviewNonce + " -->"},
		{ID: 4, Login: "bob", Body: "> 🤖 **cenci** — merged automatically by `cenci babysit`"},
		// A human quoting a receipt marker below the first line is feedback.
		{ID: 5, Login: "carol", Body: "this looks wrong:\n> <!-- cenci-review-receipt:" + testReviewNonce + " -->"},
		// Equal timestamps with another comment must not matter.
		{ID: 6, Login: "dave", Body: "me too", CreatedAt: "2026-11-10T11:00:00Z", UpdatedAt: "2026-11-10T11:00:00Z"},
		{ID: 7, Login: "erin", Body: "same second", CreatedAt: "2026-11-10T11:00:00Z", UpdatedAt: "2026-11-10T11:00:00Z"},
	}
	got := humanDiscussion(comments)
	var ids []int64
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if want := []int64{1, 5, 6, 7}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("human discussion ids = %v, want %v (bot and banner-first excluded; equal timestamps both kept)", ids, want)
	}
}

func TestDiscussionRevisionMapKeysByDiscussionKeyAndHashesBodies(t *testing.T) {
	comments := []discussionComment{
		{ID: 11, Login: "alice", Body: "first"},
		{ID: 12, Login: "bot[bot]", Body: "ignored"},
	}
	got := discussionRevisionMap(comments)
	want := map[string]string{"discussion:11": bodyRevision("first")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revision map = %v, want %v", got, want)
	}
	if discussionKey(11) != "discussion:11" {
		t.Fatalf("discussionKey(11) = %q", discussionKey(11))
	}
}

// -- parseReceipt -------------------------------------------------------------

// testReceiptBody renders a receipt comment body in the contract grammar.
// Other tests in this package may reuse it as the one fixture builder.
func testReceiptBody(nonce, head string, entries ...receiptEntry) string {
	var b strings.Builder
	b.WriteString("> 🤖 **cenci** — review receipt posted by `/cenci:address-review` (babysit-review).\n")
	b.WriteString("<!-- cenci-review-receipt:" + nonce + " -->\n")
	b.WriteString("head: " + head + "\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- discussion:%d revision=%s outcome=%s\n", e.ID, e.Revision, e.Outcome)
	}
	return b.String()
}

func TestReceiptParseValid(t *testing.T) {
	revA, revB := bodyRevision("a"), bodyRevision("b")
	body := testReceiptBody(testReviewNonce, testReceiptHead,
		receiptEntry{ID: 1, Revision: revA, Outcome: "addressed"},
		receiptEntry{ID: 2, Revision: revB, Outcome: "no-action"},
	)
	got, err := parseReceipt(body)
	if err != nil {
		t.Fatalf("parseReceipt: %v", err)
	}
	want := reviewReceipt{
		Nonce:   testReviewNonce,
		HeadSHA: testReceiptHead,
		Entries: []receiptEntry{{ID: 1, Revision: revA, Outcome: "addressed"}, {ID: 2, Revision: revB, Outcome: "no-action"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt = %+v, want %+v", got, want)
	}
}

func TestReceiptParseAcceptsSHA256HeadsAndEveryOutcome(t *testing.T) {
	head := strings.Repeat("b", 64)
	rev := bodyRevision("x")
	for _, outcome := range []string{"addressed", "no-action", "blocked", "failed"} {
		got, err := parseReceipt(testReceiptBody(testReviewNonce, head, receiptEntry{ID: 9, Revision: rev, Outcome: outcome}))
		if err != nil || got.HeadSHA != head || got.Entries[0].Outcome != outcome {
			t.Fatalf("outcome %s: got=%+v err=%v", outcome, got, err)
		}
	}
}

func TestReceiptParseZeroKeysIsValid(t *testing.T) {
	got, err := parseReceipt(testReceiptBody(testReviewNonce, testReceiptHead))
	if err != nil {
		t.Fatalf("a receipt listing no keys is valid: %v", err)
	}
	if len(got.Entries) != 0 || got.Nonce != testReviewNonce {
		t.Fatalf("receipt = %+v", got)
	}
}

func TestReceiptParseToleratesCRLFLineEndings(t *testing.T) {
	body := strings.ReplaceAll(testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 3, Revision: bodyRevision("c"), Outcome: "addressed"}), "\n", "\r\n")
	if _, err := parseReceipt(body); err != nil {
		t.Fatalf("GitHub web edits produce CRLF bodies: %v", err)
	}
}

func TestReceiptParseStrictGrammarRejections(t *testing.T) {
	rev := bodyRevision("x")
	marker := "<!-- cenci-review-receipt:" + testReviewNonce + " -->"
	head := "head: " + testReceiptHead
	entry := "- discussion:1 revision=" + rev + " outcome=addressed"
	cases := []struct {
		name string
		body string
	}{
		{"duplicate keys", marker + "\n" + head + "\n" + entry + "\n" + entry + "\n"},
		{"duplicate key with different outcome", marker + "\n" + head + "\n" + entry + "\n- discussion:1 revision=" + rev + " outcome=failed\n"},
		{"two head lines", marker + "\n" + head + "\n" + head + "\n" + entry + "\n"},
		{"no head line", marker + "\n" + entry + "\n"},
		{"two markers", marker + "\n" + marker + "\n" + head + "\n"},
		{"two different markers", marker + "\n<!-- cenci-review-receipt:" + testWrongNonce + " -->\n" + head + "\n"},
		{"short nonce", "<!-- cenci-review-receipt:abc -->\n" + head + "\n"},
		{"uppercase nonce", "<!-- cenci-review-receipt:" + strings.ToUpper(testReviewNonce) + " -->\n" + head + "\n"},
		{"marker with trailing text", marker + " extra\n" + head + "\n"},
		{"head of wrong length", marker + "\nhead: " + strings.Repeat("a", 41) + "\n"},
		{"head not hex", marker + "\nhead: " + strings.Repeat("z", 40) + "\n"},
		{"head without space after colon", marker + "\nhead:" + testReceiptHead + "\n"},
		{"unknown outcome", marker + "\n" + head + "\n- discussion:1 revision=" + rev + " outcome=done\n"},
		{"short revision", marker + "\n" + head + "\n- discussion:1 revision=abc outcome=addressed\n"},
		{"missing revision", marker + "\n" + head + "\n- discussion:1 outcome=addressed\n"},
		{"missing outcome", marker + "\n" + head + "\n- discussion:1 revision=" + rev + "\n"},
		{"non-numeric id", marker + "\n" + head + "\n- discussion:abc revision=" + rev + " outcome=addressed\n"},
		{"id overflows int64", marker + "\n" + head + "\n- discussion:99999999999999999999 revision=" + rev + " outcome=addressed\n"},
		{"extra field", marker + "\n" + head + "\n- discussion:1 revision=" + rev + " outcome=addressed extra=1\n"},
		{"one bad line poisons good lines", marker + "\n" + head + "\n" + entry + "\n- discussion:2 revision=nope outcome=addressed\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReceipt(tc.body)
			if !errors.Is(err, errReceiptMalformed) {
				t.Fatalf("err = %v (receipt %+v), want errReceiptMalformed", err, got)
			}
		})
	}
}

func TestReceiptParseNoMarkerIsNotAReceipt(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"plain human comment", "please rename the flag"},
		{"empty", ""},
		{"quoted marker only", "> <!-- cenci-review-receipt:" + testReviewNonce + " -->\nhead: " + testReceiptHead},
		{"indented quote marker", "  > <!-- cenci-review-receipt:" + testReviewNonce + " -->"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseReceipt(tc.body)
			if !errors.Is(err, errNotReceipt) {
				t.Fatalf("err = %v, want errNotReceipt", err)
			}
		})
	}
}

func TestReceiptParseIgnoresQuotedLinesEntirely(t *testing.T) {
	rev := bodyRevision("x")
	// A real receipt that also quotes a malformed discussion line and a second
	// marker: the quoted lines are removed before any rule applies.
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: rev, Outcome: "addressed"}) +
		"> - discussion:2 revision=nope outcome=bogus\n" +
		"> <!-- cenci-review-receipt:" + testWrongNonce + " -->\n" +
		"> head: " + strings.Repeat("c", 40) + "\n"
	got, err := parseReceipt(body)
	if err != nil {
		t.Fatalf("quoted lines must not influence the receipt: %v", err)
	}
	if got.Nonce != testReviewNonce || len(got.Entries) != 1 {
		t.Fatalf("receipt = %+v", got)
	}
}

// -- verifyReceipt ------------------------------------------------------------

// testLaunch is a launch whose every discussion key was trusted (its author's
// lookup granted): TrustedKeys is the allow-list the receipt check reads.
func testLaunch(keys ...string) *reviewLaunchRecord {
	var trusted []string
	for _, k := range keys {
		if isDiscussionKey(k) {
			trusted = append(trusted, k)
		}
	}
	return &reviewLaunchRecord{Nonce: testReviewNonce, LaunchedAt: testLaunchAt, Keys: keys, TrustedKeys: trusted}
}

// receiptComment builds a never-edited receipt comment created at created.
func receiptComment(id int64, login, body, created string) discussionComment {
	return discussionComment{ID: id, Login: login, Body: body, CreatedAt: created, UpdatedAt: created}
}

const afterLaunch = "2026-11-10T12:00:05Z"

func allowAll(string) writePermission { return writePermissionGranted }

// verifyFixture is a launch over discussion:1 and discussion:2 with their
// current revisions.
type verifyFixture struct {
	launch    *reviewLaunchRecord
	revisions map[string]string
	rev1      string
	rev2      string
}

func newVerifyFixture() verifyFixture {
	rev1, rev2 := bodyRevision("first comment"), bodyRevision("second comment")
	return verifyFixture{
		launch:    testLaunch("discussion:1", "discussion:2"),
		revisions: map[string]string{"discussion:1": rev1, "discussion:2": rev2},
		rev1:      rev1,
		rev2:      rev2,
	}
}

func rejectionReasons(r receiptResult) []receiptRejectReason {
	var out []receiptRejectReason
	for _, rej := range r.Rejections {
		out = append(out, rej.Reason)
	}
	return out
}

func TestReceiptVerifyValidMovesExactlyTheListedKeys(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead,
		receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"},
		receiptEntry{ID: 2, Revision: f.rev2, Outcome: "no-action"},
	)
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)

	if !got.Accepted || got.Nonce != testReviewNonce || got.ReceiptCommentID != 900 || got.HeadSHA != testReceiptHead {
		t.Fatalf("result = %+v, want an accepted receipt for comment 900", got)
	}
	want := map[string]discussionReceipt{
		"discussion:1": {CommentID: 1, Revision: f.rev1, HeadSHA: testReceiptHead, Outcome: "addressed", ReceiptCommentID: 900, Source: receiptSourceReceipt},
		"discussion:2": {CommentID: 2, Revision: f.rev2, HeadSHA: testReceiptHead, Outcome: "no-action", ReceiptCommentID: 900, Source: receiptSourceReceipt},
	}
	if !reflect.DeepEqual(got.Addressed, want) {
		t.Fatalf("Addressed = %+v, want %+v", got.Addressed, want)
	}
	if len(got.Unresolved) != 0 || len(got.Rejections) != 0 {
		t.Fatalf("Unresolved=%v Rejections=%v, want none", got.Unresolved, got.Rejections)
	}
}

func TestReceiptVerifyBlockedAndFailedStayUnresolved(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead,
		receiptEntry{ID: 1, Revision: f.rev1, Outcome: "blocked"},
		receiptEntry{ID: 2, Revision: f.rev2, Outcome: "failed"},
	)
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)

	if !got.Accepted {
		t.Fatalf("a blocked/failed receipt is still an authenticated receipt that consumes its nonce: %+v", got)
	}
	if len(got.Addressed) != 0 {
		t.Fatalf("blocked/failed must never be addressed: %+v", got.Addressed)
	}
	if got.Unresolved["discussion:1"].Outcome != "blocked" || got.Unresolved["discussion:2"].Outcome != "failed" || len(got.Unresolved) != 2 {
		t.Fatalf("Unresolved = %+v", got.Unresolved)
	}
}

// An `addressed` outcome is not evidence for a key the launch marked untrusted
// (its comment author is not in the launch's trust record): the receipt is still authentic and
// consumes the nonce, but the key lands in Unresolved as blocked with a note, never
// in Addressed. no-action, blocked and failed are taken as reported, and a trusted
// key in the same receipt still moves.
func TestReceiptVerifyAddressedIsRefusedForAnUntrustedKey(t *testing.T) {
	f := newVerifyFixture()
	rev3 := bodyRevision("third comment")
	f.revisions["discussion:3"] = rev3
	f.launch = testLaunch("discussion:1", "discussion:2", "discussion:3")
	f.launch.TrustedKeys = []string{"discussion:2"}
	body := testReceiptBody(testReviewNonce, testReceiptHead,
		receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"},
		receiptEntry{ID: 2, Revision: f.rev2, Outcome: "addressed"},
		receiptEntry{ID: 3, Revision: rev3, Outcome: "no-action"},
	)
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)

	if !got.Accepted {
		t.Fatalf("the receipt is authentic and consumes its nonce: %+v", got)
	}
	if _, moved := got.Addressed["discussion:1"]; moved {
		t.Fatalf("addressed must never move an untrusted key: %+v", got.Addressed)
	}
	if rec := got.Unresolved["discussion:1"]; rec.Outcome != receiptOutcomeBlocked || rec.Revision != f.rev1 || rec.ReceiptCommentID != 900 {
		t.Fatalf("Unresolved[discussion:1] = %+v, want a blocked record at the current revision", rec)
	}
	if len(got.Refused) != 1 || !strings.Contains(got.Refused[0], "discussion:1") || !strings.Contains(got.Refused[0], "not recorded as having write permission") {
		t.Fatalf("Refused = %q, want one note naming discussion:1 and the missing write-permission record", got.Refused)
	}
	if _, moved := got.Addressed["discussion:2"]; !moved {
		t.Fatalf("a trusted key in the same receipt must still move: %+v", got.Addressed)
	}
	if _, moved := got.Addressed["discussion:3"]; !moved {
		t.Fatalf("no-action is accepted for an untrusted key (the comment requests nothing): %+v", got.Addressed)
	}
}

func TestReceiptVerifyBlockedAndFailedAreTakenAsReportedForAnUntrustedKey(t *testing.T) {
	f := newVerifyFixture()
	f.launch.TrustedKeys = nil
	body := testReceiptBody(testReviewNonce, testReceiptHead,
		receiptEntry{ID: 1, Revision: f.rev1, Outcome: "blocked"},
		receiptEntry{ID: 2, Revision: f.rev2, Outcome: "failed"},
	)
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)
	if got.Unresolved["discussion:1"].Outcome != "blocked" || got.Unresolved["discussion:2"].Outcome != "failed" || len(got.Refused) != 0 {
		t.Fatalf("Unresolved = %+v Refused = %q, want the reported outcomes and no refusal note", got.Unresolved, got.Refused)
	}
}

// Default-deny: a launch with discussion keys and no trust record at all (a state
// saved by an earlier build, a hand-edited or truncated file, a writer that forgot
// the field) is not trusted. `addressed` is refused for every discussion key it
// lists, `no-action` is still taken as reported, and a comment-only launch has no
// discussion key to refuse.
func TestReceiptVerifyAddressedIsRefusedWhenTheLaunchHasNoTrustRecord(t *testing.T) {
	f := newVerifyFixture()
	f.launch = &reviewLaunchRecord{Nonce: testReviewNonce, LaunchedAt: testLaunchAt, Keys: []string{"discussion:1", "discussion:2", "comment:7"}}
	body := testReceiptBody(testReviewNonce, testReceiptHead,
		receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"},
		receiptEntry{ID: 2, Revision: f.rev2, Outcome: "no-action"},
	)
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)

	if !got.Accepted {
		t.Fatalf("the receipt is authentic and consumes its nonce: %+v", got)
	}
	if _, moved := got.Addressed["discussion:1"]; moved {
		t.Fatalf("addressed must never move a key with no trust record: %+v", got.Addressed)
	}
	if rec := got.Unresolved["discussion:1"]; rec.Outcome != receiptOutcomeBlocked {
		t.Fatalf("Unresolved[discussion:1] = %+v, want a blocked record", rec)
	}
	if len(got.Refused) != 1 || !strings.Contains(got.Refused[0], "discussion:1") {
		t.Fatalf("Refused = %q, want one note naming discussion:1", got.Refused)
	}
	if _, moved := got.Addressed["discussion:2"]; !moved {
		t.Fatalf("no-action needs no trust: %+v", got.Addressed)
	}
}

// A permission lookup that could not answer is neither a grant nor a denial: the
// receipt is rejected under its own reason, fail closed, and the nonce stays
// outstanding so a later pass (or a later valid receipt) can still consume it.
func TestReceiptVerifyUnknownPermissionIsRejectedDistinctlyAndDoesNotConsumeTheNonce(t *testing.T) {
	f := newVerifyFixture()
	entry := receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"}
	flaky := receiptComment(900, "maint", testReceiptBody(testReviewNonce, testReceiptHead, entry), afterLaunch)
	perm := func(string) writePermission { return writePermissionUnknown }

	got := verifyReceipt(f.launch, nil, []discussionComment{flaky}, f.revisions, perm)
	if got.Accepted || len(got.Addressed) != 0 {
		t.Fatalf("an unknown permission must address nothing: %+v", got)
	}
	if want := []receiptRejectReason{receiptRejectPermissionUnknown}; !reflect.DeepEqual(rejectionReasons(got), want) {
		t.Fatalf("rejections = %v, want %v", rejectionReasons(got), want)
	}
	if strings.Contains(string(receiptRejectPermissionUnknown), "lacks") || !strings.Contains(string(receiptRejectPermissionUnknown), "retry") {
		t.Fatalf("the unknown-permission reason %q must read as a failed lookup that will retry, not a denial", receiptRejectPermissionUnknown)
	}

	// The same receipt is accepted once the lookup answers: the nonce was not spent.
	again := verifyReceipt(f.launch, nil, []discussionComment{flaky}, f.revisions, allowAll)
	if !again.Accepted || again.ReceiptCommentID != 900 {
		t.Fatalf("after the lookup recovers: %+v, want the receipt accepted", again)
	}
}

func TestReceiptVerifyZeroKeysConsumesTheNonceAndMovesNothing(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead)
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)
	if !got.Accepted || got.Nonce != testReviewNonce {
		t.Fatalf("result = %+v, want the nonce consumed", got)
	}
	if len(got.Addressed) != 0 || len(got.Unresolved) != 0 {
		t.Fatalf("a zero-key receipt moves nothing: %+v", got)
	}
}

func TestReceiptVerifyKeyOutsideTheLaunchSetIsLeftAlone(t *testing.T) {
	f := newVerifyFixture()
	outside := bodyRevision("a comment that was never in this launch")
	f.revisions["discussion:3"] = outside
	body := testReceiptBody(testReviewNonce, testReceiptHead,
		receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"},
		receiptEntry{ID: 3, Revision: outside, Outcome: "addressed"},
	)
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)
	if !got.Accepted {
		t.Fatalf("result = %+v", got)
	}
	if _, moved := got.Addressed["discussion:3"]; moved {
		t.Fatalf("a key outside the launch's key set must stay unchanged: %+v", got.Addressed)
	}
	if _, moved := got.Addressed["discussion:1"]; !moved {
		t.Fatalf("the in-set key must still move: %+v", got.Addressed)
	}
}

func TestReceiptVerifyRevisionMismatchLeavesTheKeyPending(t *testing.T) {
	f := newVerifyFixture()
	staleRev := bodyRevision("first comment before it was edited")
	body := testReceiptBody(testReviewNonce, testReceiptHead,
		receiptEntry{ID: 1, Revision: staleRev, Outcome: "addressed"},
		receiptEntry{ID: 2, Revision: f.rev2, Outcome: "addressed"},
	)
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)
	if !got.Accepted {
		t.Fatalf("result = %+v", got)
	}
	if _, moved := got.Addressed["discussion:1"]; moved {
		t.Fatalf("an edited comment (revision mismatch) must stay pending: %+v", got.Addressed)
	}
	if _, moved := got.Addressed["discussion:2"]; !moved {
		t.Fatalf("the matching key must move: %+v", got.Addressed)
	}
}

func TestReceiptVerifyStaleBlockedOutcomeIsNotReportedUnresolved(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: bodyRevision("old"), Outcome: "blocked"})
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)
	if len(got.Unresolved) != 0 || len(got.Addressed) != 0 {
		t.Fatalf("a receipt for a stale revision says nothing about the current one: %+v", got)
	}
}

func TestReceiptVerifyDeletedCommentKeyLeavesTheKeyPending(t *testing.T) {
	f := newVerifyFixture()
	delete(f.revisions, "discussion:1")
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"})
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)
	if len(got.Addressed) != 0 {
		t.Fatalf("a key with no current revision (comment deleted) must not move: %+v", got.Addressed)
	}
}

func TestReceiptVerifyRejectsNoncesThatAreNotOutstanding(t *testing.T) {
	f := newVerifyFixture()
	rev := f.rev1
	entry := receiptEntry{ID: 1, Revision: rev, Outcome: "addressed"}
	cases := []struct {
		name     string
		launch   *reviewLaunchRecord
		consumed map[string]int64
		nonce    string
		want     receiptRejectReason
	}{
		{"spoofed or wrong nonce", f.launch, nil, testWrongNonce, receiptRejectNonceUnknown},
		{"never minted: no outstanding launch", nil, nil, testReviewNonce, receiptRejectNonceUnknown},
		{"already used by another comment", f.launch, map[string]int64{testReviewNonce: 111}, testReviewNonce, receiptRejectNonceConsumed},
		{"superseded by a newer launch (recorded with id 0)", testLaunch("discussion:1"), map[string]int64{testWrongNonce: 0}, testWrongNonce, receiptRejectNonceConsumed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			perm := func(string) writePermission { called = true; return writePermissionGranted }
			body := testReceiptBody(tc.nonce, testReceiptHead, entry)
			got := verifyReceipt(tc.launch, tc.consumed, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, perm)
			if got.Accepted || len(got.Addressed) != 0 || len(got.Unresolved) != 0 {
				t.Fatalf("a bad nonce must address nothing: %+v", got)
			}
			if want := []receiptRejectReason{tc.want}; !reflect.DeepEqual(rejectionReasons(got), want) {
				t.Fatalf("rejections = %v, want %v", rejectionReasons(got), want)
			}
			if got.Rejections[0].CommentID != 900 {
				t.Fatalf("rejection comment id = %d, want 900", got.Rejections[0].CommentID)
			}
			if called {
				t.Fatal("permission must be looked up only when the nonce matches")
			}
		})
	}
}

func TestReceiptVerifySkipsTheCommentThatAlreadyConsumedTheNonceSilently(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"})
	// The original receipt is re-read on every tick: it consumed the nonce.
	got := verifyReceipt(f.launch, map[string]int64{testReviewNonce: 900}, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)
	if got.Accepted || len(got.Addressed) != 0 || len(got.Rejections) != 0 {
		t.Fatalf("re-reading the consuming comment must be silent and a no-op: %+v", got)
	}
}

func TestReceiptVerifyReplayOfAConsumedNonceIsRecordedAsRejection(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 2, Revision: f.rev2, Outcome: "addressed"})
	original := receiptComment(900, "maint", testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"}), afterLaunch)
	replay := receiptComment(901, "maint", body, "2026-11-10T12:00:09Z")
	got := verifyReceipt(f.launch, map[string]int64{testReviewNonce: 900}, []discussionComment{original, replay}, f.revisions, allowAll)
	if got.Accepted || len(got.Addressed) != 0 {
		t.Fatalf("a replayed nonce must address nothing: %+v", got)
	}
	if len(got.Rejections) != 1 || got.Rejections[0].CommentID != 901 || got.Rejections[0].Reason != receiptRejectNonceConsumed {
		t.Fatalf("rejections = %+v, want exactly comment 901 as nonce consumed", got.Rejections)
	}
	if !strings.Contains(got.Rejections[0].String(), "901") {
		t.Fatalf("rejection text must name the comment: %q", got.Rejections[0].String())
	}
}

func TestReceiptVerifySecondValidReceiptInTheSamePassIsRejected(t *testing.T) {
	f := newVerifyFixture()
	first := receiptComment(900, "maint", testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"}), afterLaunch)
	second := receiptComment(901, "maint", testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 2, Revision: f.rev2, Outcome: "addressed"}), "2026-11-10T12:00:09Z")
	// Delivered newest-first on purpose: the earliest comment wins.
	got := verifyReceipt(f.launch, nil, []discussionComment{second, first}, f.revisions, allowAll)
	if !got.Accepted || got.ReceiptCommentID != 900 {
		t.Fatalf("result = %+v, want the earlier comment 900 to consume the nonce", got)
	}
	if _, moved := got.Addressed["discussion:2"]; moved {
		t.Fatalf("the second receipt must not move its key: %+v", got.Addressed)
	}
	if want := []receiptRejectReason{receiptRejectNonceConsumed}; !reflect.DeepEqual(rejectionReasons(got), want) {
		t.Fatalf("rejections = %v, want %v", rejectionReasons(got), want)
	}
}

func TestReceiptVerifyAuthorWithoutWritePermissionIsRejected(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"})
	var asked []string
	perm := func(login string) writePermission { asked = append(asked, login); return writePermissionDenied }
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "drive-by", body, afterLaunch)}, f.revisions, perm)
	if got.Accepted || len(got.Addressed) != 0 {
		t.Fatalf("an author without write permission must address nothing: %+v", got)
	}
	if want := []receiptRejectReason{receiptRejectNoWritePermission}; !reflect.DeepEqual(rejectionReasons(got), want) {
		t.Fatalf("rejections = %v, want %v", rejectionReasons(got), want)
	}
	if !reflect.DeepEqual(asked, []string{"drive-by"}) {
		t.Fatalf("permission asked for %v, want the receipt author only", asked)
	}
}

func TestReceiptVerifyPermissionDeniedDoesNotConsumeTheNonceForALaterValidReceipt(t *testing.T) {
	f := newVerifyFixture()
	forged := receiptComment(900, "drive-by", testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"}), afterLaunch)
	genuine := receiptComment(901, "maint", testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"}), "2026-11-10T12:00:09Z")
	perm := func(login string) writePermission {
		if login == "maint" {
			return writePermissionGranted
		}
		return writePermissionDenied
	}
	got := verifyReceipt(f.launch, nil, []discussionComment{forged, genuine}, f.revisions, perm)
	if !got.Accepted || got.ReceiptCommentID != 901 {
		t.Fatalf("result = %+v, want the authorized receipt 901 to be accepted", got)
	}
}

func TestReceiptVerifyCreatedAtMustBeStrictlyLaterThanTheLaunchSecond(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"})
	cases := []struct {
		name    string
		created string
		accept  bool
	}{
		{"created long before the launch", "2026-11-10T11:00:00Z", false},
		{"created one second before", "2026-11-10T11:59:59Z", false},
		{"created in the launch second (launch truncated to the second)", "2026-11-10T12:00:00Z", false},
		{"created one second after the launch second", "2026-11-10T12:00:01Z", true},
		{"created much later", "2026-11-10T13:00:00Z", true},
		{"unparsable timestamp fails closed", "yesterday", false},
		{"empty timestamp fails closed", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, tc.created)}, f.revisions, allowAll)
			if got.Accepted != tc.accept {
				t.Fatalf("created_at %q: accepted = %v, want %v (%+v)", tc.created, got.Accepted, tc.accept, got)
			}
			if !tc.accept && len(got.Addressed) != 0 {
				t.Fatalf("a rejected receipt must address nothing: %+v", got.Addressed)
			}
		})
	}
}

func TestReceiptVerifyEarlyReceiptIsRecordedAsRejection(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"})
	got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, "2026-11-10T11:00:00Z")}, f.revisions, allowAll)
	if want := []receiptRejectReason{receiptRejectBeforeLaunch}; !reflect.DeepEqual(rejectionReasons(got), want) {
		t.Fatalf("rejections = %v, want %v", rejectionReasons(got), want)
	}
}

func TestReceiptVerifyEditedReceiptIsRejected(t *testing.T) {
	f := newVerifyFixture()
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"})
	edited := discussionComment{ID: 900, Login: "maint", Body: body, CreatedAt: afterLaunch, UpdatedAt: "2026-11-10T12:30:00Z"}
	got := verifyReceipt(f.launch, nil, []discussionComment{edited}, f.revisions, allowAll)
	if got.Accepted || len(got.Addressed) != 0 {
		t.Fatalf("an edited receipt must address nothing: %+v", got)
	}
	if want := []receiptRejectReason{receiptRejectEdited}; !reflect.DeepEqual(rejectionReasons(got), want) {
		t.Fatalf("rejections = %v, want %v", rejectionReasons(got), want)
	}
}

func TestReceiptVerifyStrictGrammarFailureWithTheLaunchNonceIsRejected(t *testing.T) {
	f := newVerifyFixture()
	good := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 1, Revision: f.rev1, Outcome: "addressed"})
	cases := map[string]string{
		"duplicate keys":  good + "- discussion:1 revision=" + f.rev1 + " outcome=failed\n",
		"two head lines":  good + "head: " + testReceiptHead + "\n",
		"two markers":     good + "<!-- cenci-review-receipt:" + testReviewNonce + " -->\n",
		"bad outcome":     good + "- discussion:2 revision=" + f.rev2 + " outcome=maybe\n",
		"truncated entry": good + "- discussion:2 revision=" + f.rev2[:10] + "\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := verifyReceipt(f.launch, nil, []discussionComment{receiptComment(900, "maint", body, afterLaunch)}, f.revisions, allowAll)
			if got.Accepted || len(got.Addressed) != 0 || len(got.Unresolved) != 0 {
				t.Fatalf("a grammar violation rejects the whole receipt, even the valid lines: %+v", got)
			}
			if want := []receiptRejectReason{receiptRejectMalformed}; !reflect.DeepEqual(rejectionReasons(got), want) {
				t.Fatalf("rejections = %v, want %v", rejectionReasons(got), want)
			}
		})
	}
}

func TestReceiptVerifyIgnoresCommentsThatAreNotReceipts(t *testing.T) {
	f := newVerifyFixture()
	comments := []discussionComment{
		receiptComment(1, "alice", "please rename the flag", afterLaunch),
		// A human quoting a receipt marker below the first line.
		receiptComment(2, "carol", "this is odd:\n> <!-- cenci-review-receipt:"+testReviewNonce+" -->\n> head: "+testReceiptHead+"\n> - discussion:1 revision="+f.rev1+" outcome=addressed", afterLaunch),
	}
	got := verifyReceipt(f.launch, nil, comments, f.revisions, allowAll)
	if got.Accepted || len(got.Addressed) != 0 || len(got.Rejections) != 0 {
		t.Fatalf("quoted markers and plain comments are not receipts and are not even rejections: %+v", got)
	}
}

func TestReceiptVerifyWithoutAnyCommentsDoesNothing(t *testing.T) {
	f := newVerifyFixture()
	got := verifyReceipt(f.launch, nil, nil, f.revisions, allowAll)
	if got.Accepted || len(got.Addressed) != 0 || len(got.Unresolved) != 0 || len(got.Rejections) != 0 {
		t.Fatalf("result = %+v, want a zero result", got)
	}
}

// -- fetchWritePermission -----------------------------------------------------

func permissionPath(login string) string {
	return fmt.Sprintf("api repos/%s/collaborators/%s/permission", discussionTestRepo, login)
}

func TestWritePermissionGrantedOnlyForAdminAndWrite(t *testing.T) {
	const (
		granted = writePermissionGranted
		denied  = writePermissionDenied
		unknown = writePermissionUnknown
	)
	cases := []struct {
		name  string
		reply ghReply
		want  writePermission
	}{
		{"admin", ghReply{out: `{"permission":"admin"}`}, granted},
		{"write", ghReply{out: `{"permission":"write"}`}, granted},
		{"read", ghReply{out: `{"permission":"read"}`}, denied},
		{"triage", ghReply{out: `{"permission":"triage"}`}, denied},
		{"none", ghReply{out: `{"permission":"none"}`}, denied},
		{"maintain is not in the accepted set", ghReply{out: `{"permission":"maintain"}`}, denied},
		{"unrecognized value", ghReply{out: `{"permission":"superuser"}`}, denied},
		{"case differs", ghReply{out: `{"permission":"Admin"}`}, denied},
		{"empty value", ghReply{out: `{"permission":""}`}, denied},
		{"role_name is never consulted", ghReply{out: `{"permission":"read","role_name":"write"}`}, denied},
		{"nested user.permissions is never consulted", ghReply{out: `{"permission":"read","user":{"permissions":{"admin":true}}}`}, denied},
		{"null permission is a failed lookup", ghReply{out: `{"permission":null}`}, unknown},
		{"missing permission field is a failed lookup", ghReply{out: `{"role_name":"admin"}`}, unknown},
		{"malformed json is a failed lookup", ghReply{out: `not json`}, unknown},
		{"empty output is a failed lookup", ghReply{out: ``}, unknown},
		{"non-object json is a failed lookup", ghReply{out: `["write"]`}, unknown},
		{"api failure is a failed lookup", ghReply{err: errors.New("exit status 1")}, unknown},
		{"api failure that still printed a granting body", ghReply{out: `{"permission":"admin"}`, err: errors.New("exit status 1")}, unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDiscussionGh(t, map[string]ghReply{permissionPath("octocat"): tc.reply})
			if got := fetchWritePermission(discussionTestRepo, "octocat"); got != tc.want {
				t.Fatalf("fetchWritePermission = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWritePermissionInvalidLoginIsDeniedWithoutShellingOut(t *testing.T) {
	logins := []string{
		"",
		"-leading-hyphen",
		"has space",
		"a/b",
		"../../etc",
		"user?x=1",
		"semi;colon",
		"new\nline",
		strings.Repeat("a", 40),
		"dependabot[bot]",
	}
	for _, login := range logins {
		t.Run(fmt.Sprintf("%q", login), func(t *testing.T) {
			calls := withDiscussionGh(t, map[string]ghReply{})
			if fetchWritePermission(discussionTestRepo, login) != writePermissionDenied {
				t.Fatalf("login %q must be denied", login)
			}
			if len(*calls) != 0 {
				t.Fatalf("an invalid login must never reach gh, saw %v", *calls)
			}
		})
	}
}

func TestWritePermissionAcceptsGitHubLoginShapes(t *testing.T) {
	for _, login := range []string{"a", "octo-cat", "Octo123", strings.Repeat("a", 39)} {
		t.Run(login, func(t *testing.T) {
			withDiscussionGh(t, map[string]ghReply{permissionPath(login): {out: `{"permission":"write"}`}})
			if fetchWritePermission(discussionTestRepo, login) != writePermissionGranted {
				t.Fatalf("login %q is a valid GitHub login with write permission", login)
			}
		})
	}
}

// -- nonce seam ---------------------------------------------------------------

func TestReceiptNonceSeamMintsDistinct32HexNonces(t *testing.T) {
	shape := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		n, err := newReviewNonce()
		if err != nil {
			t.Fatalf("newReviewNonce: %v", err)
		}
		if !shape.MatchString(n) {
			t.Fatalf("nonce %q is not 32 lowercase hex", n)
		}
		if seen[n] {
			t.Fatalf("nonce %q was minted twice", n)
		}
		seen[n] = true
	}
}

func TestReceiptMarkerRendersTheContractLiteral(t *testing.T) {
	if got, want := receiptMarker(testReviewNonce), "<!-- cenci-review-receipt:"+testReviewNonce+" -->"; got != want {
		t.Fatalf("receiptMarker = %q, want %q", got, want)
	}
	// The marker a worker prints must parse back as that nonce.
	got, err := parseReceipt(receiptMarker(testReviewNonce) + "\nhead: " + testReceiptHead + "\n")
	if err != nil || got.Nonce != testReviewNonce {
		t.Fatalf("round trip: got=%+v err=%v", got, err)
	}
}
