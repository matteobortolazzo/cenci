package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain pins the fleetConfigPath seam to "" (disabled) for every test in
// the package (watch/docs/test-isolation.md's "audit ALL existing tests"
// rule): without this, a dev machine with automerge.enabled: true in
// ~/.config/cenci/config.json would make every pre-existing tick test issue
// an unscripted 5th gh call and fail. Tests that need automerge enabled
// override the seam themselves (see automerge_test.go's
// withFleetAutomergeEnabled) and restore it via t.Cleanup, which puts the
// seam back to this "" pin, not whatever the real environment had set.
func TestMain(m *testing.M) {
	original := fleetConfigPath
	fleetConfigPath = func() string { return "" }
	code := m.Run()
	fleetConfigPath = original
	os.Exit(code)
}

// TestGhJSONRejectsNonzeroExitEvenWithValidJSON is the #886 fail-closed
// rewrite of the deleted TestGhJSONAcceptsChecksExitWithValidJSON: ghJSON no
// longer tolerates a nonzero gh exit just because stdout still happens to
// decode as valid JSON, for both `gh pr checks` (the old carve-out's exact
// exit-8-pending case) and an ordinary non-checks command -- covering AC 1
// ("non-check gh commands ... fail closed") and AC 2 ("gh pr checks accepts
// only the documented pending exit with complete valid JSON") together.
func TestGhJSONRejectsNonzeroExitEvenWithValidJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"gh pr checks exit 8 with valid JSON must now be an error (no more carve-out)", []string{"pr", "checks", "42"}},
		{"a non-checks command exiting nonzero with valid JSON stdout must also error", []string{"pr", "view", "42"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := execGh
			execGh = func(...string) (string, string, error) {
				return `[{"bucket":"fail","name":"test"}]`, "", errors.New("exit status 1")
			}
			t.Cleanup(func() { execGh = original })
			var checks []check
			if err := ghJSON(&checks, tc.args...); err == nil {
				t.Fatalf("ghJSON(%v): err = nil, want an error: a nonzero gh exit must fail closed even when stdout decodes as valid JSON", tc.args)
			}
		})
	}
}

// TestGhJSONReturnsErrorWhenOutputTruncatedEvenIfDecodable pins #854's item
// 3 fix: execGh's errGhOutputTruncated must surface unconditionally, even
// when the (truncated) stdout still happens to decode successfully as
// valid JSON -- previously ghJSON's "decode succeeded ⇒ success" shortcut
// ran before the error was ever inspected, silently discarding the
// truncation signal and treating a possibly-incomplete read as complete.
func TestGhJSONReturnsErrorWhenOutputTruncatedEvenIfDecodable(t *testing.T) {
	original := execGh
	execGh = func(...string) (string, string, error) {
		return `[{"bucket":"pass","name":"test"}]`, "", errGhOutputTruncated
	}
	t.Cleanup(func() { execGh = original })
	var checks []check
	err := ghJSON(&checks, "pr", "checks", "42")
	if err == nil {
		t.Fatal("ghJSON: err = nil, want an error when execGh reports truncation, even though stdout still decodes as valid JSON")
	}
	if !errors.Is(err, errGhOutputTruncated) {
		t.Fatalf("ghJSON err = %v, want errors.Is(err, errGhOutputTruncated)", err)
	}
}

// TestGhJSONFailureClassification pins that ghJSON's strict rewrite (#886)
// surfaces execGh's classified sentinel errors unchanged (errors.Is) and
// that classifyGhFailure resolves each to its own distinct, content-specific
// class -- never a bare non-empty check (watch/docs/error-handling.md #446).
func TestGhJSONFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ghErr   error
		wantErr error
		want    string
	}{
		{"timeout", errGhTimeout, errGhTimeout, failureClassTimeout},
		{"cancelled", errGhCancelled, errGhCancelled, failureClassCancelled},
		{"truncated", errGhOutputTruncated, errGhOutputTruncated, failureClassTruncated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := execGh
			execGh = func(...string) (string, string, error) { return "", "", tc.ghErr }
			t.Cleanup(func() { execGh = original })
			var checks []check
			err := ghJSON(&checks, "pr", "checks", "42")
			if err == nil {
				t.Fatal("ghJSON: err = nil, want an error")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ghJSON err = %v, want errors.Is(err, %v)", err, tc.wantErr)
			}
			if got := classifyGhFailure(err); got != tc.want {
				t.Fatalf("classifyGhFailure(ghJSON err) = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGhJSONNonJSONBodyClassifiesAsParse pins ghJSON's decode-failure
// sentinel (errGhDecode, #886): a zero-exit gh call whose stdout is not
// valid JSON at all must classify as failureClassParse, distinct from every
// other failure class.
func TestGhJSONNonJSONBodyClassifiesAsParse(t *testing.T) {
	original := execGh
	execGh = func(...string) (string, string, error) { return "not json", "", nil }
	t.Cleanup(func() { execGh = original })
	var checks []check
	err := ghJSON(&checks, "pr", "checks", "42")
	if err == nil {
		t.Fatal("ghJSON: err = nil, want an error on a non-JSON body with a zero gh exit")
	}
	if !errors.Is(err, errGhDecode) {
		t.Fatalf("ghJSON err = %v, want errors.Is(err, errGhDecode)", err)
	}
	if got := classifyGhFailure(err); got != failureClassParse {
		t.Fatalf("classifyGhFailure(ghJSON err) = %q, want %q", got, failureClassParse)
	}
}

// withCommands stubs both the package's `gh` seam (execGh) and its
// non-`gh` seam (command, still used for `git rev-parse` and the `cenci
// run` self-exec) for the duration of a test, serving responses in order
// for every `gh` invocation and recording every call (both seams) into
// calls in call order -- preserving the pre-#854 `[]string{"gh", ...}`
// recorded-call shape so every existing assertion keeps working unchanged.
//
// It also installs default stubs for the two #975 tmux seams
// (currentTmuxSession/tmuxHasSession): a live session name and a true
// probe are the "everything is fine" defaults, so every pre-existing
// tick()-driven test keeps exercising its intended branch once launch()
// starts consulting these seams (Phase 4) instead of failing on the new
// "no session recorded"/"session gone" gates. Tests that care about the
// tmux-target resolution/probe behavior itself override them directly,
// after calling withCommands.
func withCommands(t *testing.T, responses []string, calls *[][]string) {
	t.Helper()
	originalCommand := command
	originalExecGh := execGh
	originalCurrentTmuxSession := currentTmuxSession
	originalTmuxHasSession := tmuxHasSession
	i := 0
	command = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		return []byte(""), nil
	}
	execGh = func(args ...string) (string, string, error) {
		*calls = append(*calls, append([]string{"gh"}, args...))
		if i >= len(responses) {
			return "", "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
		out := responses[i]
		i++
		return out, "", nil
	}
	currentTmuxSession = func() (string, error) { return "test-session", nil }
	tmuxHasSession = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		currentTmuxSession = originalCurrentTmuxSession
		tmuxHasSession = originalTmuxHasSession
	})
}

func openPR() string {
	return `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[]}`
}

func TestTickQuietBacksOff(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{openPR(), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, delay, err := tick(&s)
	if err != nil || terminal {
		t.Fatalf("tick = terminal %v, err %v", terminal, err)
	}
	if delay != 120*time.Second || s.CurrentDelaySeconds != 120 {
		t.Fatalf("delay = %v, state = %d", delay, s.CurrentDelaySeconds)
	}
	if s.LastHeadSHA != "abc" || s.FixAttempts != 0 {
		t.Fatalf("unexpected CI state: %#v", s)
	}
}

func TestTickLaunchesAddressReviewForNewFeedback(t *testing.T) {
	var calls [][]string
	// #897: reconcileFeedback now runs before detectNewFeedbackKeys, so the
	// new comment:7 key does not exist yet when reconcileFeedback's own
	// hasCommentKey gate is evaluated (PendingKeys/AddressedKeys are both
	// still empty at that point) -- its lazy GraphQL thread fetch never fires
	// this tick, and the 5th scripted response below (unresolvedThread) is
	// never consumed; withCommands tolerates the surplus. comment:7 is added
	// to PendingKeys only after reconcileFeedback returns, so it still
	// reaches the #885 dedup-driven address-review launch (PendingKeys \
	// LaunchedKeys) unlaunched, and PendingKeys still equals [comment:7]
	// afterward, matching the assertions below.
	unresolvedThread := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"isResolved":false,"comments":{"totalCount":1,"nodes":[{"databaseId":7}]}}]}}}}}`
	withCommands(t, []string{openPR(), `[]`, `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`, `[]`, unresolvedThread}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900, LaunchSession: "work"}
	_, delay, err := tick(&s)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 300*time.Second || s.PendingCommentAt == "" || !reflect.DeepEqual(s.PendingKeys, []string{"comment:7"}) {
		t.Fatalf("unexpected state: %#v", s)
	}
	if !reflect.DeepEqual(s.LaunchedKeys, []string{"comment:7"}) {
		t.Fatalf("LaunchedKeys = %v, want [comment:7]: still dispatched exactly once, holding automerge until GitHub reports resolution", s.LaunchedKeys)
	}
	found := false
	for _, c := range calls {
		if len(c) > 3 && c[1] == "run" && c[2] == "address-review" && c[3] == "42" {
			found = true
		}
	}
	if !found {
		t.Fatalf("address-review was not launched: %#v", calls)
	}
}

// TestTickBannerFirstLineCommentIsExcludedFromDetection is #897 Item 3 (AC
// 10): an inline comment whose first line is the cenci attribution banner
// must be ignored entirely by detectNewFeedbackKeys -- no key recorded, no
// dispatch, no automerge hold, and no contribution to PendingCommentAt --
// since once Item 2 makes post-resolution comments actionable, cenci's own
// address-review reply would otherwise re-trigger dispatch indefinitely.
func TestTickBannerFirstLineCommentIsExcludedFromDetection(t *testing.T) {
	bannerBody := "> 🤖 **cenci** — review reply posted by `/cenci:address-review` (posting replies).\n\nJust re-checking this."
	bodyJSON, err := json.Marshal(bannerBody)
	if err != nil {
		t.Fatal(err)
	}
	comments := fmt.Sprintf(`[{"id":7,"body":%s,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`, bodyJSON)
	var calls [][]string
	withCommands(t, []string{openPR(), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, comments, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900, LaunchSession: "work"}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.PendingKeys) != 0 {
		t.Fatalf("PendingKeys = %v, want empty: a banner-first-line comment must never become a key", s.PendingKeys)
	}
	if s.PendingCommentAt != "" {
		t.Fatalf("PendingCommentAt = %q, want empty: a banner-excluded comment must not contribute to the newest timestamp either", s.PendingCommentAt)
	}
	if n := countAddressReviewLaunches(calls); n != 0 {
		t.Fatalf("address-review launches = %d, want 0: a banner-excluded comment must never dispatch: %#v", n, calls)
	}
}

// TestTickBannerOnLaterLineIsStillDetectedNormally is #897 Item 3's
// watch/AGENTS.md-mandated regression case: the banner exclusion is narrowed
// to the comment's *first* line only -- a comment that merely contains the
// banner text somewhere later in its body (e.g. a human quoting cenci's own
// reply while raising a new point) must still be detected exactly as an
// ordinary new comment would be.
func TestTickBannerOnLaterLineIsStillDetectedNormally(t *testing.T) {
	quotedBannerBody := "Some genuine new review text.\n\n> 🤖 **cenci** — review reply posted by `/cenci:address-review` (posting replies)."
	bodyJSON, err := json.Marshal(quotedBannerBody)
	if err != nil {
		t.Fatal(err)
	}
	comments := fmt.Sprintf(`[{"id":7,"body":%s,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`, bodyJSON)
	var calls [][]string
	withCommands(t, []string{openPR(), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, comments, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900, LaunchSession: "work"}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:7"}) {
		t.Fatalf("PendingKeys = %v, want [comment:7]: the banner exclusion must be scoped to the first line only", s.PendingKeys)
	}
	if s.PendingCommentAt == "" {
		t.Fatal("PendingCommentAt = \"\", want the comment's timestamp recorded")
	}
	if n := countAddressReviewLaunches(calls); n != 1 {
		t.Fatalf("address-review launches = %d, want exactly 1: %#v", n, calls)
	}
}

func TestTickMergedRelabelsClosingIssues(t *testing.T) {
	var calls [][]string
	merged := `{"number":42,"title":"Done","state":"MERGED","url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	// The 4th response answers #811's new post-relabel parent-relationship
	// read (`gh issue view 9 --json parent`); a null parent means
	// reconcileParents does no further work, so this test's original
	// relabel-only assertions still hold unchanged.
	withCommands(t, []string{merged, `{}`, `{}`, `{"parent":null}`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, _, err := tick(&s)
	if err != nil || !terminal {
		t.Fatalf("tick = terminal %v, err %v", terminal, err)
	}
	want := []string{"gh", "issue", "edit", "9", "--repo", "o/r", "--add-label", "Implemented", "--remove-label", "In Review"}
	found := false
	for _, c := range calls {
		if reflect.DeepEqual(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("issue relabel missing: %#v", calls)
	}
}

// prViewCallKey is the exact `pr view` call key tick() issues, shared by the
// #811 merge-time parent-reconciliation wiring tests below (each needs to
// script it via the arg-dispatching ghStub, unlike withCommands' sequential
// queue, since these tests also need to script tick's variable-length
// downstream reconcileParents calls by their own distinct argument shape).
func prViewCallKey(pr string) string {
	return fmt.Sprintf("pr view %s --repo o/r --json %s", pr, prViewFields)
}

// TestTickMergedNonChildClosingIssueMakesExactlyOneExtraParentRead pins #811
// items 1 and 4 together: a closing issue that is not itself a split child
// (its native `parent` relationship is null) leaves the existing per-child
// relabel untouched, costs exactly one extra `gh` call (the parent-read),
// and never reaches the parent write/comment-read paths at all.
func TestTickMergedNonChildClosingIssueMakesExactlyOneExtraParentRead(t *testing.T) {
	var calls [][]string
	merged := `{"number":42,"title":"Done","state":"MERGED","url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	ghStub(t, &calls, map[string]ghResp{
		prViewCallKey("42"): {stdout: merged},
		selfHealKey:         {stdout: `{}`},
		"issue edit 9 --repo o/r --add-label Implemented --remove-label In Review": {stdout: `{}`},
		"issue view 9 --repo o/r --json parent":                                    {stdout: `{"parent":null}`},
	})
	s := State{PR: "42", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, _, err := tick(&s)
	if err != nil || !terminal {
		t.Fatalf("tick = terminal %v, err %v", terminal, err)
	}
	wantEdit := []string{"gh", "issue", "edit", "9", "--repo", "o/r", "--add-label", "Implemented", "--remove-label", "In Review"}
	found := false
	for _, c := range calls {
		if reflect.DeepEqual(c, wantEdit) {
			found = true
		}
	}
	if !found {
		t.Fatalf("existing per-child issue relabel missing: %#v", calls)
	}
	if n := countCalls(calls, "issue view 9 --repo o/r --json parent"); n != 1 {
		t.Fatalf("parent-relationship reads = %d, want exactly 1: %#v", n, calls)
	}
	got := writeCalls(calls)
	// #811 fix: the write list also legitimately contains tick's own
	// pre-existing, unconditional "gh label create Implemented" self-heal
	// call (scripted above via selfHealKey -- an unscripted call would fail
	// this test outright -- proving it does fire), alongside the pre-existing
	// child relabel; the substantive #811 property this test pins is that
	// neither of those pre-existing writes, nor any new one, ever targets
	// parent #20 (no *parent* write) on a null-parent closing issue.
	for _, c := range got {
		if strings.Contains(strings.Join(c, " "), "20") {
			t.Fatalf("issued a parent-numbered write on a null-parent closing issue: %#v", got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("writes = %#v, want exactly the pre-existing self-heal and child relabel", got)
	}
	if n := countCalls(calls, "api ", "/comments"); n != 0 {
		t.Fatalf("comment reads = %d, want 0 when the closing issue has no parent: %#v", n, calls)
	}
}

// TestTickMergedParentReadFailureIsNonTerminalAndPreservesChildEdit pins
// #811 item 2: a failure reading a closing child's own parent relationship
// must not be swallowed as "no parent", must surface as tick's own
// non-terminal error (naming the child and the failed operation, so the
// backoff loop retries), and must never discard the child label transition
// that already succeeded earlier in the same tick.
func TestTickMergedParentReadFailureIsNonTerminalAndPreservesChildEdit(t *testing.T) {
	var calls [][]string
	merged := `{"number":42,"title":"Done","state":"MERGED","url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	ghStub(t, &calls, map[string]ghResp{
		prViewCallKey("42"): {stdout: merged},
		selfHealKey:         {stdout: `{}`},
		"issue edit 9 --repo o/r --add-label Implemented --remove-label In Review": {stdout: `{}`},
		"issue view 9 --repo o/r --json parent":                                    {err: errors.New("exit status 1: network unreachable")},
	})
	s := State{PR: "42", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, _, err := tick(&s)
	if err == nil {
		t.Fatal("tick: err = nil, want a non-terminal error when the child's parent-relationship read fails")
	}
	if terminal {
		t.Fatal("tick: terminal = true, want false so the backoff loop retries a parent-read failure")
	}
	if !strings.Contains(err.Error(), "9") || !strings.Contains(err.Error(), reasonParentReadFailed) {
		t.Fatalf("tick err = %q, want it to name child #9 and %q", err.Error(), reasonParentReadFailed)
	}
	wantEdit := []string{"gh", "issue", "edit", "9", "--repo", "o/r", "--add-label", "Implemented", "--remove-label", "In Review"}
	found := false
	for _, c := range calls {
		if reflect.DeepEqual(c, wantEdit) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the already-succeeded child relabel must survive a later parent-read failure in the same tick: %#v", calls)
	}
}

// TestTickMergedGapReportHoldIsTerminalWithDistinguishableStdoutLine pins
// #811 item 9: a parent held by a recorded gap-report comment is a terminal
// success (tick returns true, 0, nil, exactly like the ordinary
// "PR merged" case), not an error, but prints one extra distinguishable line
// naming the held parent before the existing terminal report -- and issues
// zero parent writes while the child's own relabel is retained.
func TestTickMergedGapReportHoldIsTerminalWithDistinguishableStdoutLine(t *testing.T) {
	var calls [][]string
	merged := `{"number":42,"title":"Done","state":"MERGED","url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	script := map[string]ghResp{
		prViewCallKey("42"): {stdout: merged},
		selfHealKey:         {stdout: `{}`},
		"issue edit 9 --repo o/r --add-label Implemented --remove-label In Review": {stdout: `{}`},
		"issue view 9 --repo o/r --json parent":                                    {stdout: parentReadFor9},
		graphKey20:                                                                 {stdout: singleClosedGraph20},
		commentsPage1Key20:                                                         {stdout: commentsJSON("hello\n" + parentGapMarker + "\nmore")},
	}
	ghStub(t, &calls, script)

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	originalStdout := os.Stdout
	os.Stdout = w
	s := State{PR: "42", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, _, err := tick(&s)
	_ = w.Close()
	os.Stdout = originalStdout
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}
	if err != nil || !terminal {
		t.Fatalf("tick = terminal %v, err %v, want terminal success (a gap-report hold is not an error)", terminal, err)
	}
	if !strings.Contains(string(out), "20") {
		t.Fatalf("tick stdout = %q, want a distinguishable line naming held parent #20", string(out))
	}
	wantEdit := []string{"gh", "issue", "edit", "9", "--repo", "o/r", "--add-label", "Implemented", "--remove-label", "In Review"}
	found := false
	for _, c := range calls {
		if reflect.DeepEqual(c, wantEdit) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the child relabel must be retained alongside a held parent: %#v", calls)
	}
	if n := countCalls(calls, "issue edit 20"); n != 0 {
		t.Fatalf("parent 20 must never be edited while held by a gap report: %#v", calls)
	}
	if n := countCalls(calls, "issue close 20"); n != 0 {
		t.Fatalf("parent 20 must never be closed while held by a gap report: %#v", calls)
	}
}

// TestTickAcrossTwoSeparateMergesClosesParentOnlyAfterSecondChild is the
// #811 Fix 2 regression: the ticket's core motivating scenario (#791/#793),
// exercised through the real tick() entry point across two independent
// merge events -- not reconcileOneParent/reconcileParents directly, and not
// a single reconcileParents call already holding both children. The first
// tick merges the PR closing sibling child #5 alone: parent #20 still has
// an open sibling (#9), so it stays open with zero parent writes. A wholly
// separate, second tick call then merges the PR closing sibling child #9:
// now every one of parent #20's sub-issues is CLOSED and no gap report is
// present, so this second tick closes the parent. Each tick's own full
// expected call sequence is asserted.
func TestTickAcrossTwoSeparateMergesClosesParentOnlyAfterSecondChild(t *testing.T) {
	mergedFirst := `{"number":42,"title":"Child A","state":"MERGED","url":"https://example/pr/42","closingIssuesReferences":[{"number":5}]}`
	var calls1 [][]string
	ghStub(t, &calls1, map[string]ghResp{
		prViewCallKey("42"): {stdout: mergedFirst},
		selfHealKey:         {stdout: `{}`},
		"issue edit 5 --repo o/r --add-label Implemented --remove-label In Review": {stdout: `{}`},
		"issue view 5 --repo o/r --json parent":                                    {stdout: `{"parent":{"number":20}}`},
		graphKey20:                                                                 {stdout: `{"state":"OPEN","subIssues":{"totalCount":2,"nodes":[{"number":5,"state":"CLOSED"},{"number":9,"state":"OPEN"}]}}`},
	})
	s1 := State{PR: "42", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal1, _, err1 := tick(&s1)
	if err1 != nil || !terminal1 {
		t.Fatalf("first tick = terminal %v, err %v, want terminal success", terminal1, err1)
	}
	wantEdit5 := []string{"gh", "issue", "edit", "5", "--repo", "o/r", "--add-label", "Implemented", "--remove-label", "In Review"}
	wantParentRead5 := []string{"gh", "issue", "view", "5", "--repo", "o/r", "--json", "parent"}
	wantGraphRead20 := []string{"gh", "issue", "view", "20", "--repo", "o/r", "--json", "state,subIssues"}
	wantSelfHeal := []string{"gh", "label", "create", "Implemented", "--repo", "o/r", "--color", "6F42C1", "--description", "PR merged — done"}
	wantPrView42 := []string{"gh", "pr", "view", "42", "--repo", "o/r", "--json", prViewFields}
	wantCalls1 := [][]string{wantPrView42, wantSelfHeal, wantEdit5, wantParentRead5, wantGraphRead20}
	if len(calls1) != len(wantCalls1) {
		t.Fatalf("first tick calls = %#v, want exactly %#v", calls1, wantCalls1)
	}
	for _, w := range wantCalls1 {
		found := false
		for _, c := range calls1 {
			if reflect.DeepEqual(c, w) {
				found = true
			}
		}
		if !found {
			t.Fatalf("first tick calls = %#v, missing expected call %#v", calls1, w)
		}
	}
	if got := writeCalls(calls1); len(got) != 2 {
		// The pre-existing self-heal and child #5 relabel are the only two
		// writes; parent #20 itself must receive zero writes while sibling #9
		// is still open.
		t.Fatalf("first tick writes = %#v, want exactly the self-heal and child relabel (no parent-20 write)", got)
	}
	for _, c := range calls1 {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "issue edit 20") || strings.Contains(joined, "issue close 20") {
			t.Fatalf("first tick issued a parent-20 write while sibling #9 is still open: %#v", calls1)
		}
	}

	mergedSecond := `{"number":43,"title":"Child B","state":"MERGED","url":"https://example/pr/43","closingIssuesReferences":[{"number":9}]}`
	var calls2 [][]string
	ghStub(t, &calls2, map[string]ghResp{
		prViewCallKey("43"): {stdout: mergedSecond},
		selfHealKey:         {stdout: `{}`},
		"issue edit 9 --repo o/r --add-label Implemented --remove-label In Review": {stdout: `{}`},
		"issue view 9 --repo o/r --json parent":                                    {stdout: parentReadFor9},
		graphKey20:                                                                 {stdout: allClosedGraph20},
		commentsPage1Key20:                                                         {stdout: commentsJSON("no marker here")},
		inReviewSelfHealKey:                                                        {stdout: `{}`},
		editKey20:                                                                  {stdout: `{}`},
		closeKey20:                                                                 {stdout: `{}`},
		verifyKey20:                                                                {stdout: `{"state":"CLOSED"}`},
	})
	s2 := State{PR: "43", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal2, _, err2 := tick(&s2)
	if err2 != nil || !terminal2 {
		t.Fatalf("second tick = terminal %v, err %v, want terminal success", terminal2, err2)
	}
	wantEdit9 := []string{"gh", "issue", "edit", "9", "--repo", "o/r", "--add-label", "Implemented", "--remove-label", "In Review"}
	wantParentEdit20 := []string{"gh", "issue", "edit", "20", "--repo", "o/r", "--remove-label", "In Review", "--add-label", "Implemented"}
	wantParentClose20 := []string{"gh", "issue", "close", "20", "--repo", "o/r", "--reason", "completed"}
	for _, w := range [][]string{wantEdit9, wantParentEdit20, wantParentClose20} {
		found := false
		for _, c := range calls2 {
			if reflect.DeepEqual(c, w) {
				found = true
			}
		}
		if !found {
			t.Fatalf("second tick calls = %#v, missing expected call %#v", calls2, w)
		}
	}
	editIdx := indexOfCall(calls2, "gh issue edit 20")
	closeIdx := indexOfCall(calls2, "gh issue close 20")
	if editIdx < 0 || closeIdx < 0 || closeIdx < editIdx {
		t.Fatalf("second tick: want parent #20's label edit strictly before its close, got edit=%d close=%d: %#v", editIdx, closeIdx, calls2)
	}
}

func TestStatePathDoesNotExposeRepositoryName(t *testing.T) {
	p := statePath("/state", "secret-owner/private-repo", "42")
	if strings.Contains(p, "secret") || !strings.HasSuffix(p, "-42.json") {
		t.Fatalf("unsafe state path: %s", p)
	}
}

// -- close guard (#787) ------------------------------------------------------

// TestTickRecordsClosingIssuesAndCIStatus pins the two join keys the close
// guard reads off disk (#787): the issues the PR closes (so a ticket number
// resolves to a supervisor) and the collapsed CI verdict (so a green PR stops
// holding its window open). Both come from data tick already fetches.
func TestTickRecordsClosingIssuesAndCIStatus(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	for _, tc := range []struct {
		name   string
		checks string
		want   string
	}{
		{"failing beats pending", `[{"bucket":"pending","name":"a"},{"bucket":"fail","name":"b"}]`, "failing"},
		{"pending beats pass", `[{"bucket":"pass","name":"a"},{"bucket":"pending","name":"b"}]`, "pending"},
		{"all pass is green", `[{"bucket":"pass","name":"a"}]`, "green"},
		{"no checks stays empty", `[]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			withCommands(t, []string{prWithIssue, tc.checks, `[]`, `[]`}, &calls)
			s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}
			if _, _, err := tick(&s); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.ClosingIssues, []int{782}) {
				t.Errorf("ClosingIssues = %v, want [782]", s.ClosingIssues)
			}
			if s.CIStatus != tc.want {
				t.Errorf("CIStatus = %q, want %q", s.CIStatus, tc.want)
			}
		})
	}
}

// TestCancelBucketCIStillAllowsClose is a #787 regression pin (#854 Test
// Strategy): the strict pass-only automerge classifier (automergeCIHold) is
// deliberately a separate function from ciStatus(), which still feeds
// BlocksClose -- a cancel-bucket check must not make ciStatus() report
// "failing"/"pending" and wedge a tmux window open, even though the same
// cancel bucket now holds automerge under its own reason.
func TestCancelBucketCIStillAllowsClose(t *testing.T) {
	stubProcessOwned(t, true)
	dir := t.TempDir()
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withCommands(t, []string{prWithIssue, `[{"bucket":"cancel","name":"a"}]`, `[]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.CIStatus != "green" {
		t.Fatalf("CIStatus = %q, want %q: ciStatus() must keep collapsing a cancel-only bucket to green (#787 unchanged), even though automergeCIHold now holds it under its own reason", s.CIStatus, "green")
	}
	writeGuardState(t, dir, s)
	blocks, _ := BlocksClose("782", "", dir)
	if blocks {
		t.Fatal("BlocksClose = true, want false: a cancel-bucket-only CI result must not hold the window open")
	}
}

// -- CI exit-code tolerance (#923) -------------------------------------------
//
// gh pr checks documents two non-zero "normal state" exit codes -- 8
// (pending) and 1 (a check failed) -- and both still write complete, valid
// JSON to stdout. The bare ghJSON call at both of tick's/
// recheckAutomergeInputs' checks-fetch sites treats every nonzero exit as a
// hard read failure, so a normal pending or failing poll aborts the tick
// early, leaving s.ClosingIssues/s.CIStatus unpublished (the close guard's
// join key and verdict) and never reaching the ci-repair launch or
// automerge evaluation below it. These tests drive that fetch through tick
// itself (never an unexported helper directly) via the extended
// withScriptedCommands stub, which can now express "nonzero exit with JSON
// on stdout AND text on stderr" in a single scripted call.

// TestTickPendingCIExitCodeToleranceKeepsPollingAtInterval pins the exit-8
// (pending) carve-out: the checks fetch still decodes the pending-bucket
// JSON despite the nonzero exit, so the tick reaches the CIStatus/
// ClosingIssues publication and must not back off (a pending CI poll is a
// normal, actionable state, not a quiet no-op).
func TestTickPendingCIExitCodeToleranceKeepsPollingAtInterval(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: prWithIssue},
		{
			out:    `[{"bucket":"pending","name":"test","state":"PENDING"}]`,
			err:    &ghExitError{code: 8, err: errors.New("exit status 8")},
			stderr: "Some checks are still pending",
		},
		{out: `[]`},
		{out: `[]`},
	}, &calls)

	// CurrentDelaySeconds starts already backed off, so an unfixed tick that
	// still treats this as a quiet no-op (or a hard failure) is visible: the
	// former would double it further, the latter would leave it untouched.
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 900}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v, want a pending-CI poll to succeed", err)
	}
	if s.CIStatus != "pending" {
		t.Errorf("CIStatus = %q, want %q", s.CIStatus, "pending")
	}
	if !reflect.DeepEqual(s.ClosingIssues, []int{782}) {
		t.Errorf("ClosingIssues = %v, want [782]", s.ClosingIssues)
	}
	if s.CurrentDelaySeconds != s.IntervalSeconds {
		t.Errorf("CurrentDelaySeconds = %d, want %d (IntervalSeconds): a pending CI poll must not back off", s.CurrentDelaySeconds, s.IntervalSeconds)
	}
	if s.Status == "retrying" {
		t.Errorf("Status = %q, want tick to never leave the supervisor in a retrying state for a successful pending-CI poll", s.Status)
	}
}

// TestTickFailingCIExitCodeToleranceLaunchesCIRepair pins the exit-1
// (failing) carve-out: the checks fetch still decodes the fail-bucket JSON
// despite the nonzero exit, reaching the existing failing-checks branch that
// launches ci-repair -- today's bare ghJSON call aborts before ever getting
// there. withScriptedCommands (unlike withCommands) installs no tmux seam
// defaults, so this stubs tmuxHasSession and sets LaunchSession itself
// (launch_test.go's pattern), or the launch would fail and the tick would
// exercise reasonWorkflowLaunchFailed instead.
func TestTickFailingCIExitCodeToleranceLaunchesCIRepair(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: prWithIssue},
		{
			out:    `[{"bucket":"fail","name":"test","state":"FAILURE"}]`,
			err:    &ghExitError{code: 1, err: errors.New("exit status 1")},
			stderr: "1 of 1 checks failing",
		},
		{out: `[]`},
		{out: `[]`},
	}, &calls)
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v, want a failing-CI poll to succeed", err)
	}
	if s.CIStatus != "failing" {
		t.Errorf("CIStatus = %q, want %q", s.CIStatus, "failing")
	}
	if _, found := launchCallArgs(calls, "ci-repair"); !found {
		t.Fatalf("ci-repair was not launched: %#v", calls)
	}
}

// TestTickChecksUndecodableExitOneNoChecksReportedIsBenign pins the narrow
// exit-1-only stderr fallback: when stdout does not decode at all AND
// stderr matches "no checks reported", the PR genuinely has no checks yet --
// a benign, non-error state, not a read failure.
func TestTickChecksUndecodableExitOneNoChecksReportedIsBenign(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: prWithIssue},
		{
			out:    "not json",
			err:    &ghExitError{code: 1, err: errors.New("exit status 1")},
			stderr: "no checks reported on the 'x' branch",
		},
		{out: `[]`},
		{out: `[]`},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v, want the 'no checks reported' fallback to succeed with zero checks", err)
	}
	if s.CIStatus != "" {
		t.Errorf("CIStatus = %q, want %q (zero checks)", s.CIStatus, "")
	}
	if _, found := launchCallArgs(calls, "ci-repair"); found {
		t.Fatalf("ci-repair must not launch when there are zero checks: %#v", calls)
	}
}

// TestTickChecksUndecodableExitOneUnrelatedStderrIsHardFailure narrows the
// "no checks reported" fallback to its exact stderr match (watch/AGENTS.md's
// Critical Rule against broadening a match-miss exclusion into a silent
// catch-all): undecodable stdout plus unrelated stderr text must remain a
// hard read failure, routed through reasonUpstreamChecksUnreadable. The
// fleet automerge switch is enabled so the recorded reason surfaces the read
// failure itself, rather than recordUpstreamReadFailure's kill-switch
// override (automerge.go:1032-1038: with the switch off, every hold reason
// collapses to reasonAutomergeDisabled).
func TestTickChecksUndecodableExitOneUnrelatedStderrIsHardFailure(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: prWithIssue},
		{
			out:    "not json",
			err:    &ghExitError{code: 1, err: errors.New("exit status 1")},
			stderr: "rate limit exceeded",
		},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want unrelated stderr text to remain a hard read failure")
	}
	if s.AutomergeReason != reasonUpstreamChecksUnreadable {
		t.Fatalf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonUpstreamChecksUnreadable)
	}
}

// TestTickChecksExitEightUndecodableIsHardFailureEvenWithNoChecksStderr pins
// that the exit-1-only stderr fallback never widens to exit 8: undecodable
// stdout on exit 8 must be a hard failure even when stderr happens to match
// the "no checks reported" pattern.
func TestTickChecksExitEightUndecodableIsHardFailureEvenWithNoChecksStderr(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: prWithIssue},
		{
			out:    "not json",
			err:    &ghExitError{code: 8, err: errors.New("exit status 8")},
			stderr: "no checks reported on the 'x' branch",
		},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want exit 8 with undecodable stdout to remain a hard failure regardless of stderr content")
	}
}

// TestTickChecksNonCommandFailureClassesAreNotSwallowed is the single most
// important new test: the exit-code tolerance must be gated on
// classifyGhFailure(err) == failureClassCommand, so a timeout, cancellation,
// or truncation joined alongside an otherwise-qualifying ghExitError must
// still be a hard read failure, even though the wrapped exit code (8,
// pending) and stdout (decodable pending JSON) would otherwise qualify for
// the carve-out.
func TestTickChecksNonCommandFailureClassesAreNotSwallowed(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	pendingJSON := `[{"bucket":"pending","name":"test","state":"PENDING"}]`
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"truncated", errors.Join(&ghExitError{code: 8, err: errors.New("exit status 8")}, errGhOutputTruncated)},
		{"timeout", errors.Join(&ghExitError{code: 8, err: errors.New("exit status 8")}, errGhTimeout)},
		{"cancelled", errors.Join(&ghExitError{code: 8, err: errors.New("exit status 8")}, errGhCancelled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyGhFailure(tc.err); got == failureClassCommand {
				t.Fatalf("test setup: classifyGhFailure(%v) = %q, want a non-command class -- otherwise this case does not exercise the gate", tc.err, got)
			}
			var calls [][]string
			withScriptedCommands(t, []scriptedCall{
				{out: prWithIssue},
				{out: pendingJSON, err: tc.err, stderr: "still pending"},
			}, &calls)
			s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
			if _, _, err := tick(&s); err == nil {
				t.Fatalf("tick: err = nil, want a %s checks-fetch failure to remain a hard failure even though the wrapped exit code (8, pending) and stdout would otherwise qualify for the tolerance", tc.name)
			}
		})
	}
}

// TestTickGenuineChecksReadFailureKeepsClosingIssuesAndSetsCIStatusUnknown
// covers the on-disk half of the close guard's AC: a genuine (non-tolerated)
// checks read failure must still leave s.ClosingIssues populated (published
// before the checks fetch now runs, from the already-completed pr view) and
// must set the new ciStatusUnknown value -- and BlocksClose, reading that
// state back off disk for a live supervisor, must hold.
func TestTickGenuineChecksReadFailureKeepsClosingIssuesAndSetsCIStatusUnknown(t *testing.T) {
	stubProcessOwned(t, true)
	dir := t.TempDir()
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: prWithIssue},
		{
			out:    "not json",
			err:    &ghExitError{code: 1, err: errors.New("exit status 1")},
			stderr: "rate limit exceeded",
		},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, PID: 4242}
	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want the genuine checks-fetch failure to surface")
	}
	if !reflect.DeepEqual(s.ClosingIssues, []int{782}) {
		t.Fatalf("ClosingIssues = %v, want [782]: the close-guard join key must be published before the checks fetch, even when the checks fetch itself fails", s.ClosingIssues)
	}
	if s.CIStatus != ciStatusUnknown {
		t.Fatalf("CIStatus = %q, want ciStatusUnknown %q", s.CIStatus, ciStatusUnknown)
	}

	writeGuardState(t, dir, s)
	blocks, _ := BlocksClose("782", "", dir)
	if !blocks {
		t.Fatal("BlocksClose = false, want true: a live supervisor whose last checks read genuinely failed must hold the close guard, not fail open")
	}
}

// TestTickPRViewReadFailureSetsCIStatusUnknown covers the other tick
// upstream-read-failure site: a gh pr view failure (before ClosingIssues can
// even be published, since it comes from the pr view response itself) must
// still record ciStatusUnknown.
func TestTickPRViewReadFailureSetsCIStatusUnknown(t *testing.T) {
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{err: errors.New("exit status 1")},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want the pr view failure to surface")
	}
	if s.CIStatus != ciStatusUnknown {
		t.Fatalf("CIStatus = %q, want ciStatusUnknown %q", s.CIStatus, ciStatusUnknown)
	}
}

// TestTickFailingCIActionableOnlyWhileRepairPending exercises the
// RepairPending scoping in both directions, on a tick where the head SHA is
// unchanged (LastHeadSHA pinned to the fixture PR's headRefOid, per
// watch/docs/test-strategy.md's fixture-pinning rule) so the pre-existing
// "new failing head" branch never fires and only the new actionable-seeding
// logic is under test: a failing CIStatus is actionable while RepairPending
// is true (the supervisor keeps polling at IntervalSeconds, since a repair
// agent is expected to push), and still backs off when RepairPending is
// false.
func TestTickFailingCIActionableOnlyWhileRepairPending(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	failJSON := `[{"bucket":"fail","name":"test","state":"FAILURE"}]`
	for _, tc := range []struct {
		name          string
		repairPending bool
		wantDelay     int64
	}{
		{"RepairPending true stays at interval (actionable)", true, 60},
		{"RepairPending false still backs off (not actionable)", false, 1800},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			withScriptedCommands(t, []scriptedCall{
				{out: prWithIssue},
				{out: failJSON},
				{out: `[]`},
				{out: `[]`},
			}, &calls)
			s := State{
				PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 900,
				LastHeadSHA:   "abc",
				RepairPending: tc.repairPending,
			}
			if _, _, err := tick(&s); err != nil {
				t.Fatalf("tick: %v", err)
			}
			if s.CurrentDelaySeconds != tc.wantDelay {
				t.Fatalf("CurrentDelaySeconds = %d, want %d", s.CurrentDelaySeconds, tc.wantDelay)
			}
		})
	}
}

// TestRunWritesStateBeforeFirstTick covers the arm-to-first-poll window
// (#787): `cenci babysit` writes its state file before the supervisor loop
// starts polling, so a lazyboards cleanup firing between "supervisor armed"
// and "first tick completed" can already see that a supervisor owns the PR.
func TestRunWritesStateBeforeFirstTick(t *testing.T) {
	dir := t.TempDir()
	var atFirstTick *State
	originalCommand := command
	command = func(name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte("/repo/root\n"), nil
		}
		return []byte(""), nil
	}
	originalExecGh := execGh
	execGh = func(args ...string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "repo":
			return "o/r\n", "", nil
		case len(args) > 1 && args[0] == "pr" && args[1] == "view":
			s := load(statePath(dir, "o/r", "42"))
			atFirstTick = &s
			return "", "", errors.New("exit status 1")
		}
		return "", "", nil
	}
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
	})

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: time.Minute, Once: true}); err == nil {
		t.Fatal("Run: want the stubbed gh failure to surface")
	}
	if atFirstTick == nil {
		t.Fatal("the first tick never ran")
	}
	if atFirstTick.PR != "42" {
		t.Errorf("state at first tick has PR %q, want the state file already written with PR 42", atFirstTick.PR)
	}
	if atFirstTick.RepoRoot != "/repo/root" {
		t.Errorf("state at first tick has RepoRoot %q, want /repo/root", atFirstTick.RepoRoot)
	}
	if atFirstTick.PID != os.Getpid() {
		t.Errorf("state at first tick has PID %d, want the supervisor's own pid %d", atFirstTick.PID, os.Getpid())
	}
}

// writeGuardState writes s as a supervisor state file in dir under an
// arbitrary name — BlocksClose globs the directory rather than recomputing a
// repo-hashed path, so the file name is deliberately not the production one.
func writeGuardState(t *testing.T, dir string, s State) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state-"+s.PR+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

// stubProcessOwned replaces the /proc-backed liveness check for the duration
// of a test so the guard matrix can model "live supervisor" and "dead
// supervisor" without spawning real processes.
func stubProcessOwned(t *testing.T, owned bool) {
	t.Helper()
	original := processOwned
	processOwned = func(int, string) bool { return owned }
	t.Cleanup(func() { processOwned = original })
}

func TestBlocksCloseMatrix(t *testing.T) {
	live := State{PR: "790", RepoRoot: "/repo/root", ClosingIssues: []int{782}, CIStatus: "failing", PID: 4242, Status: "running"}
	for _, tc := range []struct {
		name       string
		state      State
		procOwned  bool
		ticket     string
		repoRoot   string
		wantBlocks bool
	}{
		{"live supervisor with failing CI blocks", live, true, "782", "/repo/root", true},
		{"live supervisor with pending CI blocks", func() State { s := live; s.CIStatus = "pending"; return s }(), true, "782", "/repo/root", true},
		{"green CI allows", func() State { s := live; s.CIStatus = "green"; return s }(), true, "782", "/repo/root", false},
		{"unknown CI allows", func() State { s := live; s.CIStatus = ""; return s }(), true, "782", "/repo/root", false},
		// #923: a genuine gh read failure (as opposed to "no checks reported
		// yet", which still collapses to "" above) now writes the dedicated
		// ciStatusUnknown value and must hold the guard just like
		// "failing"/"pending" -- the "" and "green" allow rows above stay
		// green, unchanged by this ticket.
		{"unknown CI (read failure) blocks", func() State { s := live; s.CIStatus = ciStatusUnknown; return s }(), true, "782", "/repo/root", true},
		{"dead supervisor allows", live, false, "782", "/repo/root", false},
		{"paused supervisor with no pid blocks", func() State { s := live; s.PID = 0; s.Status = "needs-input"; return s }(), false, "782", "/repo/root", true},
		{"another ticket allows", live, true, "999", "/repo/root", false},
		{"different repo root allows", live, true, "782", "/other/root", false},
		{"unknown caller repo root fails open to blocking", live, true, "782", "", true},
		{"unknown state repo root fails open to blocking", func() State { s := live; s.RepoRoot = ""; return s }(), true, "782", "/repo/root", true},
		{"non-numeric ticket allows", live, true, "add-dark-mode", "/repo/root", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubProcessOwned(t, tc.procOwned)
			dir := t.TempDir()
			writeGuardState(t, dir, tc.state)
			blocks, reason := BlocksClose(tc.ticket, tc.repoRoot, dir)
			if blocks != tc.wantBlocks {
				t.Fatalf("BlocksClose = %v (%q), want %v", blocks, reason, tc.wantBlocks)
			}
			if blocks && !strings.Contains(reason, "#790") {
				t.Errorf("reason = %q, want it to name the supervised PR", reason)
			}
			if !blocks && reason != "" {
				t.Errorf("reason = %q, want empty when nothing blocks", reason)
			}
		})
	}
}

// TestBlocksCloseFailsOpenOnUnreadableState covers the fail-open contract
// (#787): the guard must never wedge a window open because of an I/O or
// decode failure, so a missing state directory and a corrupt state file both
// allow the close.
func TestBlocksCloseFailsOpenOnUnreadableState(t *testing.T) {
	stubProcessOwned(t, true)

	missing := filepath.Join(t.TempDir(), "never-created")
	if blocks, _ := BlocksClose("782", "/repo/root", missing); blocks {
		t.Error("a missing state directory must fail open (allow the close)")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if blocks, _ := BlocksClose("782", "/repo/root", dir); blocks {
		t.Error("a corrupt state file must fail open (allow the close)")
	}
}

// TestBlocksCloseScansPastNonMatchingEntries asserts a corrupt or unrelated
// entry never short-circuits the scan: a real blocking supervisor later in
// the directory must still be found.
func TestBlocksCloseScansPastNonMatchingEntries(t *testing.T) {
	stubProcessOwned(t, true)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aaa-corrupt.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	writeGuardState(t, dir, State{PR: "111", RepoRoot: "/repo/root", ClosingIssues: []int{1}, CIStatus: "green", PID: 1})
	writeGuardState(t, dir, State{PR: "790", RepoRoot: "/repo/root", ClosingIssues: []int{782}, CIStatus: "pending", PID: 4242})

	blocks, reason := BlocksClose("782", "/repo/root", dir)
	if !blocks {
		t.Fatalf("BlocksClose = false, want the later blocking supervisor to be found")
	}
	if !strings.Contains(reason, "#790") {
		t.Errorf("reason = %q, want it to name PR #790", reason)
	}
}

// TestDefaultProcessOwnedRejectsUnusedPID keeps the real liveness check
// honest: the stubbed matrix above proves the decision logic, this proves the
// production probe still says "not ours" for a pid that isn't running.
func TestDefaultProcessOwnedRejectsUnusedPID(t *testing.T) {
	pid := unusedPID(t)
	if defaultProcessOwned(pid, "790") {
		t.Errorf("defaultProcessOwned(%d) = true, want false for a pid that is not running", pid)
	}
}

// unusedPID returns a pid with no live process behind it.
func unusedPID(t *testing.T) int {
	t.Helper()
	for pid := 1 << 20; pid > 0; pid-- {
		if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
			return pid
		}
	}
	t.Skip("no unused pid available")
	return 0
}

// -- automerge State persistence (#824) --------------------------------------

// TestStateSaveLoadRoundTripsAutomergeFields pins the data contract the close
// guard and the supervisor's own detached-mode logging depend on: since the
// supervisor's cmd.Stdout is nil under detached mode, the automerge decision
// log line only reaches a terminal under --once, so the decision must survive
// a save/load round trip through the state file instead.
func TestStateSaveLoadRoundTripsAutomergeFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	want := State{
		PR:                "42",
		Repo:              "o/r",
		AutomergeDecision: "held",
		AutomergeReason:   reasonLabelMissing,
		AutomergeDetail:   "some captured gh output",
		AutomergeConditions: []conditionResult{
			{Key: "enabled", Reached: true, Pass: true},
			{Key: "label", Reached: true, Pass: false},
		},
		AutomergeCheckedAt: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	}
	if err := save(path, want); err != nil {
		t.Fatal(err)
	}
	got := load(path)
	if got.AutomergeDecision != want.AutomergeDecision {
		t.Errorf("AutomergeDecision = %q, want %q", got.AutomergeDecision, want.AutomergeDecision)
	}
	if got.AutomergeReason != want.AutomergeReason {
		t.Errorf("AutomergeReason = %q, want %q", got.AutomergeReason, want.AutomergeReason)
	}
	if got.AutomergeDetail != want.AutomergeDetail {
		t.Errorf("AutomergeDetail = %q, want %q", got.AutomergeDetail, want.AutomergeDetail)
	}
	if !reflect.DeepEqual(got.AutomergeConditions, want.AutomergeConditions) {
		t.Errorf("AutomergeConditions = %#v, want %#v", got.AutomergeConditions, want.AutomergeConditions)
	}
	if !got.AutomergeCheckedAt.Equal(want.AutomergeCheckedAt) {
		t.Errorf("AutomergeCheckedAt = %v, want %v", got.AutomergeCheckedAt, want.AutomergeCheckedAt)
	}
}

// -- #850: GitHub-authoritative review-feedback resolution --------------------

// unresolvedThreadFor builds a GraphQL reviewThreads response reporting a
// single unresolved thread whose one comment carries id.
func unresolvedThreadFor(id int64) string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"isResolved":false,"comments":{"totalCount":1,"nodes":[{"databaseId":%d}]}}]}}}}}`, id)
}

// resolvedThreadFor builds a GraphQL reviewThreads response reporting a
// single resolved thread whose one comment carries id.
func resolvedThreadFor(id int64) string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"isResolved":true,"comments":{"totalCount":1,"nodes":[{"databaseId":%d}]}}]}}}}}`, id)
}

// TestTickUnrelatedPushLeavesFeedbackPending is AC 1: the PR's head commit
// advances for a reason unrelated to the pending feedback (GitHub still
// reports the thread unresolved) -- PendingKeys must stay unchanged and
// AddressedKeys must stay empty. This is the exact regression the deleted
// head-change-clearing block (babysit.go:253-258) caused.
func TestTickUnrelatedPushLeavesFeedbackPending(t *testing.T) {
	var calls [][]string
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"new-sha","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA:      "old-sha",
		PendingKeys:      []string{"comment:5"},
		PendingCommentAt: "2026-01-01T00:00:00Z",
		PendingHeadSHA:   "old-sha",
		LaunchSession:    "work",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) {
		t.Fatalf("PendingKeys = %v, want unchanged [comment:5]: an unrelated push must not resolve pending feedback", s.PendingKeys)
	}
	if len(s.AddressedKeys) != 0 {
		t.Fatalf("AddressedKeys = %v, want empty", s.AddressedKeys)
	}
}

// TestTickRepairPushWithoutResolutionLeavesFeedbackPending is AC 2: a push
// from babysit's own repair session (ci-repair or address-review) is still
// just a local signal -- proving an attempt, not reviewer acceptance -- so it
// must not clear PendingKeys either, even though it advances the head exactly
// the way the deleted rule keyed off of (PendingHeadSHA no longer proves
// anything).
func TestTickRepairPushWithoutResolutionLeavesFeedbackPending(t *testing.T) {
	var calls [][]string
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"repair-sha","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA:      "pre-repair-sha",
		PendingKeys:      []string{"comment:5"},
		PendingCommentAt: "2026-01-01T00:00:00Z",
		PendingHeadSHA:   "pre-repair-sha",
		LaunchSession:    "work",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) {
		t.Fatalf("PendingKeys = %v, want unchanged [comment:5]: a repair push alone is not proof of reviewer resolution", s.PendingKeys)
	}
	if len(s.AddressedKeys) != 0 {
		t.Fatalf("AddressedKeys = %v, want empty", s.AddressedKeys)
	}
}

// TestTickResolvedThreadClearsPendingKey is AC 3's thread path: once
// GitHub's review-thread state reports isResolved, the key moves to
// AddressedKeys, LastCommentAt advances to the cleared set's watermark, and
// PendingCommentAt/PendingHeadSHA both clear.
func TestTickResolvedThreadClearsPendingKey(t *testing.T) {
	var calls [][]string
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"new-sha","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, resolvedThreadFor(5)}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA:      "old-sha",
		LastCommentAt:    "2025-12-01T00:00:00Z",
		PendingKeys:      []string{"comment:5"},
		PendingCommentAt: "2026-01-01T00:00:00Z",
		PendingHeadSHA:   "old-sha",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.PendingKeys) != 0 {
		t.Fatalf("PendingKeys = %v, want empty once GitHub reports the thread resolved", s.PendingKeys)
	}
	if !reflect.DeepEqual(s.AddressedKeys, []string{"comment:5"}) {
		t.Fatalf("AddressedKeys = %v, want [comment:5]", s.AddressedKeys)
	}
	if s.LastCommentAt != "2026-01-01T00:00:00Z" {
		t.Errorf("LastCommentAt = %q, want it advanced to the cleared pending set's watermark", s.LastCommentAt)
	}
	if s.PendingCommentAt != "" || s.PendingHeadSHA != "" {
		t.Errorf("PendingCommentAt/PendingHeadSHA = %q/%q, want both cleared once the pending set empties", s.PendingCommentAt, s.PendingHeadSHA)
	}
}

// TestTickReorderDetectsAndResolvesResolvedThreadCommentAcrossTwoTicks is
// #897 Item 2 (AC 6/7/8): a brand-new comment on a thread GitHub already
// classifies as resolved must not be silently dropped in the same tick it is
// detected. Before the fix, detectNewFeedbackKeys appended the new key to
// PendingKeys before reconcileFeedback ran, so reconcileFeedback's own
// end-of-tick fetch (triggered by the very key just added) immediately
// classified it resolved and moved it straight to AddressedKeys -- no
// dispatch, no hold, ever. After the fix, reconcileFeedback runs first (when
// the key does not exist yet, so its lazy GraphQL thread fetch does not even
// fire) and detectNewFeedbackKeys runs after, so the new key survives to the
// PendingKeys \ LaunchedKeys launch trigger this same tick. The *next* tick
// is the one that authoritatively reclassifies it once GitHub's thread state
// is actually consulted for a now-tracked key.
func TestTickReorderDetectsAndResolvesResolvedThreadCommentAcrossTwoTicks(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900, LaunchSession: "work"}

	// Tick 1: the new comment lands on a thread GitHub will later report
	// resolved, but PendingKeys/AddressedKeys are both still empty when this
	// tick's reconcileFeedback pass runs, so its lazy GraphQL thread fetch
	// never fires (hasCommentKey is computed from the pre-tick state) --
	// exactly 5 gh calls, no threads probe.
	var calls1 [][]string
	newComment := `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	withCommands(t, []string{
		automergeEligiblePR(),
		`[{"bucket":"pass","name":"test","state":"SUCCESS"}]`,
		newComment,
		`[]`,
		`{"labels":[{"name":"automerge:ok"}]}`,
	}, &calls1)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:7"}) {
		t.Fatalf("tick 1: PendingKeys = %v, want [comment:7]: the new comment must survive this tick's reconcile pass, never be silently classified away", s.PendingKeys)
	}
	if !reflect.DeepEqual(s.LaunchedKeys, []string{"comment:7"}) {
		t.Fatalf("tick 1: LaunchedKeys = %v, want [comment:7]: the reorder must let this tick's own launch trigger see the new key", s.LaunchedKeys)
	}
	if len(s.AddressedKeys) != 0 {
		t.Fatalf("tick 1: AddressedKeys = %v, want empty: nothing may resolve a key before its own thread state was ever consulted", s.AddressedKeys)
	}
	if n := countAddressReviewLaunches(calls1); n != 1 {
		t.Fatalf("tick 1: address-review launches = %d, want exactly 1: %#v", n, calls1)
	}
	if s.AutomergeDecision != "held" || s.AutomergeReason != reasonReviewPending {
		t.Fatalf("tick 1: AutomergeDecision/Reason = %q/%q, want held/%q: the freshly-detected key must hold automerge this same tick", s.AutomergeDecision, s.AutomergeReason, reasonReviewPending)
	}
	if s.LastCommentAt != "" {
		t.Fatalf("tick 1: LastCommentAt = %q, want unchanged (empty): nothing resolved yet, so the watermark must not advance", s.LastCommentAt)
	}
	tick1PendingCommentAt := s.PendingCommentAt
	if tick1PendingCommentAt == "" {
		t.Fatal("tick 1: PendingCommentAt = \"\", want the new comment's timestamp recorded")
	}

	// Tick 2: GitHub now reports the thread resolved. No new comments arrive,
	// so this is purely reconcileFeedback reclassifying the key tick 1
	// recorded -- the key moves to AddressedKeys, drops out of LaunchedKeys,
	// triggers no second dispatch, and the feedback-specific automerge hold
	// clears. The labels response deliberately omits "automerge:ok" so this
	// tick's own automerge outcome holds for an unrelated reason, keeping the
	// assertion scoped to "no longer reasonReviewPending" rather than
	// requiring a full green merge chain.
	var calls2 [][]string
	withCommands(t, []string{
		automergeEligiblePR(),
		`[{"bucket":"pass","name":"test","state":"SUCCESS"}]`,
		`[]`,
		`[]`,
		resolvedThreadFor(7),
		`{"labels":[]}`,
	}, &calls2)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.PendingKeys) != 0 {
		t.Fatalf("tick 2: PendingKeys = %v, want empty once GitHub reports the thread resolved", s.PendingKeys)
	}
	if !reflect.DeepEqual(s.AddressedKeys, []string{"comment:7"}) {
		t.Fatalf("tick 2: AddressedKeys = %v, want [comment:7]", s.AddressedKeys)
	}
	if len(s.LaunchedKeys) != 0 {
		t.Fatalf("tick 2: LaunchedKeys = %v, want empty: a resolved key's episode is over", s.LaunchedKeys)
	}
	if n := countAddressReviewLaunches(calls2); n != 0 {
		t.Fatalf("tick 2: address-review must not relaunch for an already-launched, now-resolved key: %#v", calls2)
	}
	if s.AutomergeReason == reasonReviewPending {
		t.Fatal("tick 2: AutomergeReason still reasonReviewPending, want the feedback hold cleared now that the key resolved")
	}
	if s.LastCommentAt != tick1PendingCommentAt {
		t.Fatalf("tick 2: LastCommentAt = %q, want it advanced to tick 1's PendingCommentAt (%q) -- monotonic, never regressing", s.LastCommentAt, tick1PendingCommentAt)
	}
	if s.PendingCommentAt != "" || s.PendingHeadSHA != "" {
		t.Fatalf("tick 2: PendingCommentAt/PendingHeadSHA = %q/%q, want both cleared once the pending set empties", s.PendingCommentAt, s.PendingHeadSHA)
	}
}

// TestDetectNewFeedbackKeysSameSecondCommentNotNarrowedBySinceWatermark is a
// direct unit test on detectNewFeedbackKeys -- the one deliberate exception
// to this ticket's integration-first strategy, justified because AC 8's
// same-second non-narrowing case requires a comment timestamp that is
// byte-equal to the watermark reconcileFeedback advances s.LastCommentAt to
// *during the same tick*, strictly before detectNewFeedbackKeys itself runs.
// A tick-level fixture cannot express this deterministically: it would
// require reconcileFeedback's own advance and the new comment's `updated_at`
// to land on the exact same value it independently computes. Calling
// detectNewFeedbackKeys directly lets the test hold `since` (the entry-time
// watermark, snapshotted before reconcileFeedback ran) and s.LastCommentAt
// (already advanced by a simulated reconcileFeedback pass) as two distinct,
// explicit values.
//
// #897 Decision/Q2: without threading an explicit `since` parameter, a
// same-second comment landing after a prior tick's fetch would be
// permanently dropped once reconcileFeedback advances s.LastCommentAt to
// that same second -- exactly the narrowing AC 8 forbids.
func TestDetectNewFeedbackKeysSameSecondCommentNotNarrowedBySinceWatermark(t *testing.T) {
	const (
		since             = "2026-01-01T00:00:00Z" // entry-time watermark, snapshotted before reconcileFeedback ran this tick
		advancedWatermark = "2026-01-02T00:00:00Z" // s.LastCommentAt, already advanced by this same tick's reconcileFeedback pass
	)

	t.Run("comment at the entry-time watermark exactly is not new", func(t *testing.T) {
		s := &State{LastCommentAt: advancedWatermark}
		comments := []comment{{ID: 1, UpdatedAt: since, User: struct{ Login string }{"reviewer"}}}
		keys, newest := detectNewFeedbackKeys(s, comments, nil, since)
		if len(keys) != 0 {
			t.Fatalf("keys = %v, want empty: a comment timestamped exactly at `since` is not strictly newer", keys)
		}
		if newest != advancedWatermark {
			t.Fatalf("newest = %q, want unchanged %q", newest, advancedWatermark)
		}
	})

	t.Run("comment at the just-advanced LastCommentAt (same second) is still new against the older since watermark", func(t *testing.T) {
		s := &State{LastCommentAt: advancedWatermark}
		comments := []comment{{ID: 2, UpdatedAt: advancedWatermark, User: struct{ Login string }{"reviewer"}}}
		keys, newest := detectNewFeedbackKeys(s, comments, nil, since)
		if !reflect.DeepEqual(keys, []string{"comment:2"}) {
			t.Fatalf("keys = %v, want [comment:2]: AC 8 -- a same-second comment must not be narrowed out just because reconcileFeedback already advanced LastCommentAt to that same second", keys)
		}
		// newest is seeded from the live s.LastCommentAt (never from `since`),
		// so the watermark can never move backward: this comment's own
		// timestamp equals that seed, so newest must stay exactly at it, not
		// regress to `since`.
		if newest != advancedWatermark {
			t.Fatalf("newest = %q, want %q: the watermark must never move backward from the live LastCommentAt seed", newest, advancedWatermark)
		}
	})

	t.Run("comment newer than the live LastCommentAt advances newest beyond it", func(t *testing.T) {
		const newer = "2026-01-03T00:00:00Z"
		s := &State{LastCommentAt: advancedWatermark}
		comments := []comment{{ID: 3, UpdatedAt: newer, User: struct{ Login string }{"reviewer"}}}
		keys, newest := detectNewFeedbackKeys(s, comments, nil, since)
		if !reflect.DeepEqual(keys, []string{"comment:3"}) {
			t.Fatalf("keys = %v, want [comment:3]", keys)
		}
		if newest != newer {
			t.Fatalf("newest = %q, want %q", newest, newer)
		}
	})

	t.Run("an already-seen key is skipped regardless of the since/LastCommentAt split", func(t *testing.T) {
		s := &State{LastCommentAt: advancedWatermark, PendingKeys: []string{"comment:4"}}
		comments := []comment{{ID: 4, UpdatedAt: advancedWatermark, User: struct{ Login string }{"reviewer"}}}
		keys, _ := detectNewFeedbackKeys(s, comments, nil, since)
		if len(keys) != 0 {
			t.Fatalf("keys = %v, want empty: an already-tracked key must never be re-reported as new", keys)
		}
	})
}

// TestTickDismissedOrSupersededChangeRequestClears is AC 3's review path:
// a CHANGES_REQUESTED review clears only when it is DISMISSED or the
// reviewer's latest effective review is APPROVED -- asserted as separate
// subtests. Neither subtest's pending set contains a comment: key, so the
// lazy GraphQL thread fetch must never be invoked either.
func TestTickDismissedOrSupersededChangeRequestClears(t *testing.T) {
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"new-sha","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	for _, tc := range []struct {
		name    string
		reviews string
	}{
		{"blocking review dismissed", `[{"id":10,"state":"DISMISSED","submitted_at":"2026-01-02T00:00:00Z","user":{"login":"alice"}}]`},
		{"reviewer's latest effective review is approved", `[{"id":10,"state":"CHANGES_REQUESTED","submitted_at":"2026-01-01T00:00:00Z","user":{"login":"alice"}},{"id":11,"state":"APPROVED","submitted_at":"2026-01-02T00:00:00Z","user":{"login":"alice"}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, tc.reviews}, &calls)
			s := State{
				PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
				LastHeadSHA:      "old-sha",
				PendingKeys:      []string{"review:10"},
				PendingCommentAt: "2026-01-01T00:00:00Z",
				PendingHeadSHA:   "old-sha",
			}
			if _, _, err := tick(&s); err != nil {
				t.Fatal(err)
			}
			if len(s.PendingKeys) != 0 {
				t.Fatalf("PendingKeys = %v, want empty", s.PendingKeys)
			}
			if !reflect.DeepEqual(s.AddressedKeys, []string{"review:10"}) {
				t.Fatalf("AddressedKeys = %v, want [review:10]", s.AddressedKeys)
			}
			for _, c := range calls {
				if len(c) > 2 && c[1] == "api" && c[2] == "graphql" {
					t.Fatalf("a review:-only pending set must never call the GraphQL thread fetch: %#v", calls)
				}
			}
		})
	}
}

// TestTickFreshlyDetectedReviewSupersededSameTickClears is the Should-Fix
// follow-up from #897 Phase 6/7 review: pins the same-tick review-supersession
// block (babysit.go's #897/#920 fix, gated on reviewsComplete) for a review
// key detectNewFeedbackKeys returns brand-new THIS tick -- unlike
// TestTickDismissedOrSupersededChangeRequestClears above, which pre-seeds
// PendingKeys with the review key and exercises the pre-existing
// classifyPendingKey path. Here the CHANGES_REQUESTED review is never
// pre-seeded: it is only ever discovered by this tick's own
// detectNewFeedbackKeys call, already superseded by a later same-login
// APPROVED/DISMISSED review in that very same reviews fetch. It must resolve
// straight to AddressedKeys without ever surfacing in PendingKeys or
// LaunchedKeys, and without triggering an address-review launch.
func TestTickFreshlyDetectedReviewSupersededSameTickClears(t *testing.T) {
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"new-sha","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	for _, tc := range []struct {
		name    string
		reviews string
	}{
		{"superseded by approved", `[{"id":10,"state":"CHANGES_REQUESTED","submitted_at":"2026-01-01T00:00:00Z","user":{"login":"alice"}},{"id":11,"state":"APPROVED","submitted_at":"2026-01-02T00:00:00Z","user":{"login":"alice"}}]`},
		{"superseded by dismissed", `[{"id":10,"state":"CHANGES_REQUESTED","submitted_at":"2026-01-01T00:00:00Z","user":{"login":"alice"}},{"id":11,"state":"DISMISSED","submitted_at":"2026-01-02T00:00:00Z","user":{"login":"alice"}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, tc.reviews}, &calls)
			s := State{
				PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
				LastHeadSHA:   "old-sha",
				LaunchSession: "work",
			}
			if _, _, err := tick(&s); err != nil {
				t.Fatal(err)
			}
			if len(s.PendingKeys) != 0 {
				t.Fatalf("PendingKeys = %v, want empty: a freshly-detected but already-superseded review must never surface as pending", s.PendingKeys)
			}
			if len(s.LaunchedKeys) != 0 {
				t.Fatalf("LaunchedKeys = %v, want empty", s.LaunchedKeys)
			}
			if !reflect.DeepEqual(s.AddressedKeys, []string{"review:10"}) {
				t.Fatalf("AddressedKeys = %v, want [review:10]", s.AddressedKeys)
			}
			for _, c := range calls {
				if len(c) > 2 && c[1] == "run" && c[2] == "address-review" {
					t.Fatalf("a freshly-detected review already superseded in the same tick must never trigger an address-review launch: %#v", calls)
				}
			}
		})
	}
}

// TestTickNewFeedbackAfterRepairTrackedIndependently is AC 4: a brand-new
// comment arriving while an older comment is still unresolved gets its own
// independent key and does not clear (or get merged with) the older one.
func TestTickNewFeedbackAfterRepairTrackedIndependently(t *testing.T) {
	var calls [][]string
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"new-sha","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	newComment := `[{"id":9,"updated_at":"2026-01-03T00:00:00Z","user":{"login":"reviewer"}}]`
	bothUnresolved := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"isResolved":false,"comments":{"totalCount":1,"nodes":[{"databaseId":5}]}},{"isResolved":false,"comments":{"totalCount":1,"nodes":[{"databaseId":9}]}}]}}}}}`
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, newComment, `[]`, bothUnresolved}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA:      "old-sha",
		LastCommentAt:    "2026-01-01T00:00:00Z",
		PendingKeys:      []string{"comment:5"},
		PendingCommentAt: "2026-01-01T00:00:00Z",
		PendingHeadSHA:   "old-sha",
		LaunchSession:    "work",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5", "comment:9"}) {
		t.Fatalf("PendingKeys = %v, want [comment:5 comment:9]: the new comment must be tracked independently, not merged with or clearing the old one", s.PendingKeys)
	}
}

// TestTickDoesNotRelaunchAddressReviewForStillPendingFeedback pins that the
// pre-existing launch-dedup discipline survives #850's changes: a comment
// that is already pending and still unresolved must not trigger a second
// address-review launch on a later tick.
func TestTickDoesNotRelaunchAddressReviewForStillPendingFeedback(t *testing.T) {
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"new-sha","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	comments := `[{"id":5,"updated_at":"2026-01-01T00:00:00Z","user":{"login":"reviewer"}}]`

	var calls [][]string
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, comments, `[]`, unresolvedThreadFor(5)}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	launches := 0
	for _, c := range calls {
		if len(c) > 3 && c[1] == "run" && c[2] == "address-review" {
			launches++
		}
	}
	if launches != 1 {
		t.Fatalf("first tick: address-review launches = %d, want exactly 1", launches)
	}

	calls = nil
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, comments, `[]`, unresolvedThreadFor(5)}, &calls)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if len(c) > 3 && c[1] == "run" && c[2] == "address-review" {
			t.Fatalf("second tick: address-review must not relaunch for a comment that is already pending and still unresolved: %#v", calls)
		}
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) {
		t.Fatalf("PendingKeys = %v, want [comment:5] still pending after the second tick", s.PendingKeys)
	}
}

// -- #885: reopen/launch-dedup/restart at the tick boundary -------------------
//
// TestPendingFeedbackSurvivesRestart (AC 6: a save/load round trip with
// SchemaVersion 1 and pre-existing PendingKeys, then a tick, proving the hold
// reproduces purely from persisted state) is superseded by the schema-
// migration tests below: TestLoadMigratesSchema1SeedsLaunchedKeysFromPendingKeys
// pins the same SchemaVersion-1-round-trip case (now asserting the migrated
// SchemaVersion 2 and the seeded LaunchedKeys, since a legacy SchemaVersion-1
// state no longer round-trips as 1 -- load() always migrates it), and
// TestTickAfterSchemaMigrationDoesNotRelaunchAlreadyPendingKey below carries
// forward its "the hold reproduces purely from persisted state" tick-level
// assertion.

// countAddressReviewLaunches counts how many recorded calls are a `cenci run
// address-review` launch -- shared by every reopen/dedup test below.
func countAddressReviewLaunches(calls [][]string) int {
	n := 0
	for _, c := range calls {
		if len(c) > 3 && c[1] == "run" && c[2] == "address-review" {
			n++
		}
	}
	return n
}

// TestTickReopensAddressedKeyAndDedupsLaunchAcrossTicks is AC 1/AC 2/Decision
// 3: a previously-addressed key whose thread GitHub now reports unresolved
// moves back to PendingKeys and fires exactly one address-review launch for
// the new episode; two further ticks with the thread still unresolved fire
// no additional launch, proving the dedup persists beyond a single
// subsequent tick.
func TestTickReopensAddressedKeyAndDedupsLaunchAcrossTicks(t *testing.T) {
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"sha1","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA:   "sha1",
		AddressedKeys: []string{"comment:5"},
		LaunchSession: "work",
	}

	// Tick 1: the reopen transition itself -- launches exactly once.
	var calls1 [][]string
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}, &calls1)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) {
		t.Fatalf("tick 1: PendingKeys = %v, want [comment:5]: the reopened key must move back to pending", s.PendingKeys)
	}
	if len(s.AddressedKeys) != 0 {
		t.Fatalf("tick 1: AddressedKeys = %v, want empty", s.AddressedKeys)
	}
	if !reflect.DeepEqual(s.LaunchedKeys, []string{"comment:5"}) {
		t.Fatalf("tick 1: LaunchedKeys = %v, want [comment:5]", s.LaunchedKeys)
	}
	if n := countAddressReviewLaunches(calls1); n != 1 {
		t.Fatalf("tick 1: address-review launches = %d, want exactly 1", n)
	}

	// Tick 2: still unresolved -- no relaunch for the same episode.
	var calls2 [][]string
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}, &calls2)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if n := countAddressReviewLaunches(calls2); n != 0 {
		t.Fatalf("tick 2: address-review must not relaunch for the same still-unresolved episode: %#v", calls2)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) {
		t.Fatalf("tick 2: PendingKeys = %v, want unchanged [comment:5]", s.PendingKeys)
	}

	// Tick 3: still unresolved -- still no relaunch, proving the dedup
	// persists across more than one subsequent tick.
	var calls3 [][]string
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}, &calls3)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if n := countAddressReviewLaunches(calls3); n != 0 {
		t.Fatalf("tick 3: address-review must not relaunch: %#v", calls3)
	}
}

// TestTickReopenLaunchFailureRetriesNextTickAndPreservesReopenState is the
// Test Strategy's "launch failure on a reopened key" case: the reopen is
// still recorded into PendingKeys (and out of AddressedKeys) even though the
// address-review launch itself fails -- "merge safety must not depend on
// launch success" (Decision 3) -- LaunchedKeys stays unmarked so the very
// next tick retries the launch.
func TestTickReopenLaunchFailureRetriesNextTickAndPreservesReopenState(t *testing.T) {
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"sha1","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA:   "sha1",
		AddressedKeys: []string{"comment:5"},
		LaunchSession: "work",
	}

	responses := []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}
	var calls [][]string
	originalCommand := command
	originalExecGh := execGh
	i := 0
	execGh = func(args ...string) (string, string, error) {
		calls = append(calls, append([]string{"gh"}, args...))
		if i >= len(responses) {
			return "", "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
		out := responses[i]
		i++
		return out, "", nil
	}
	command = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte("boom"), errors.New("exit status 1")
	}
	// #975: a stubbed tmuxHasSession is required so the address-review
	// launch attempt below reaches the command() self-exec (and fails
	// there, as intended) rather than short-circuiting on the recorded-
	// session gate before command() is ever called.
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		tmuxHasSession = originalTmuxHasSession
	})

	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want the address-review launch failure for the reopened key to surface as a tick error")
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"comment:5"}) {
		t.Fatalf("PendingKeys = %v, want [comment:5]: merge safety must record the reopen even though the launch failed", s.PendingKeys)
	}
	if len(s.AddressedKeys) != 0 {
		t.Fatalf("AddressedKeys = %v, want empty: the reopened key must leave AddressedKeys regardless of launch success", s.AddressedKeys)
	}
	if len(s.LaunchedKeys) != 0 {
		t.Fatalf("LaunchedKeys = %v, want empty: a failed launch must not mark the key as launched, so the next tick retries", s.LaunchedKeys)
	}

	// Next tick: LaunchedKeys is still empty, so PendingKeys \ LaunchedKeys
	// still contains comment:5 -- retry the launch, this time succeeding.
	var calls2 [][]string
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}, &calls2)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if n := countAddressReviewLaunches(calls2); n != 1 {
		t.Fatalf("retry tick: address-review launches = %d, want exactly 1 (the retried launch for the still-undelivered episode)", n)
	}
	if !reflect.DeepEqual(s.LaunchedKeys, []string{"comment:5"}) {
		t.Fatalf("LaunchedKeys = %v, want [comment:5] after the retried launch succeeds", s.LaunchedKeys)
	}
}

// TestLoadMigratesSchema1SeedsLaunchedKeysFromPendingKeys is AC 6's restart
// case: a legacy schema-1 state file (no LaunchedKeys field at all) must
// have LaunchedKeys seeded from its existing PendingKeys on load, and
// SchemaVersion bumped to 2 -- so upgrading a supervisor with in-flight
// feedback does not fire one spurious address-review launch for work that
// was already dispatched under the old schema.
func TestLoadMigratesSchema1SeedsLaunchedKeysFromPendingKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	saved := State{
		SchemaVersion: 1,
		PR:            "42",
		Repo:          "o/r",
		PendingKeys:   []string{"comment:5", "review:10"},
	}
	if err := save(path, saved); err != nil {
		t.Fatal(err)
	}
	got := load(path)
	if got.SchemaVersion != 2 {
		t.Fatalf("SchemaVersion = %d, want migrated to 2", got.SchemaVersion)
	}
	if !reflect.DeepEqual(got.LaunchedKeys, []string{"comment:5", "review:10"}) {
		t.Fatalf("LaunchedKeys = %v, want seeded from PendingKeys %v so upgrading mid-episode does not fire a spurious relaunch", got.LaunchedKeys, saved.PendingKeys)
	}
}

// TestTickAfterSchemaMigrationDoesNotRelaunchAlreadyPendingKey is the
// tick-level continuation of the schema-1 migration: immediately after a
// legacy restart, a tick observing the same still-unresolved pending key
// must not fire a spurious address-review launch, and the hold itself must
// reproduce purely from persisted state (formerly
// TestPendingFeedbackSurvivesRestart's assertion, folded in here since both
// exercise the exact same schema-1-restart-then-tick shape).
func TestTickAfterSchemaMigrationDoesNotRelaunchAlreadyPendingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	saved := State{
		SchemaVersion:       1,
		PR:                  "42",
		Repo:                "o/r",
		Agent:               "codex",
		IntervalSeconds:     60,
		CurrentDelaySeconds: 60,
		LastHeadSHA:         "sha1",
		PendingKeys:         []string{"comment:5"},
		PendingCommentAt:    "2026-01-01T00:00:00Z",
		PendingHeadSHA:      "sha1",
	}
	if err := save(path, saved); err != nil {
		t.Fatal(err)
	}
	restarted := load(path)
	if !reflect.DeepEqual(restarted.LaunchedKeys, []string{"comment:5"}) {
		t.Fatalf("test setup: restarted.LaunchedKeys = %v, want [comment:5] seeded by the migration", restarted.LaunchedKeys)
	}

	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"sha1","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	var calls [][]string
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}, &calls)
	if _, _, err := tick(&restarted); err != nil {
		t.Fatal(err)
	}
	if n := countAddressReviewLaunches(calls); n != 0 {
		t.Fatalf("a legacy schema-1 restart migrating PendingKeys into LaunchedKeys must not fire a spurious relaunch on the very next tick: %#v", calls)
	}
	if !reflect.DeepEqual(restarted.PendingKeys, []string{"comment:5"}) {
		t.Fatalf("PendingKeys = %v, want the hold to reproduce from persisted state alone: [comment:5]", restarted.PendingKeys)
	}
}

// TestSchema2StateRoundTripsReopenAndLaunchDedupMarker is AC 6's other
// restart case: a schema-2 state file mid-episode -- a reopened key already
// pending AND already launched -- must round-trip both PendingKeys and
// LaunchedKeys independently through save/load.
func TestSchema2StateRoundTripsReopenAndLaunchDedupMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	want := State{
		SchemaVersion: 2,
		PR:            "42",
		Repo:          "o/r",
		PendingKeys:   []string{"comment:5"},
		LaunchedKeys:  []string{"comment:5"},
	}
	if err := save(path, want); err != nil {
		t.Fatal(err)
	}
	got := load(path)
	if got.SchemaVersion != 2 {
		t.Fatalf("SchemaVersion = %d, want 2 (unchanged, already current)", got.SchemaVersion)
	}
	if !reflect.DeepEqual(got.PendingKeys, want.PendingKeys) {
		t.Fatalf("PendingKeys = %v, want %v", got.PendingKeys, want.PendingKeys)
	}
	if !reflect.DeepEqual(got.LaunchedKeys, want.LaunchedKeys) {
		t.Fatalf("LaunchedKeys = %v, want %v: the launch-dedup marker must survive a restart independent of the reopen itself", got.LaunchedKeys, want.LaunchedKeys)
	}
}

// TestTickAfterSchema2RestartDoesNotRelaunchAlreadyLaunchedReopen is the
// tick-level continuation of the schema-2 round trip: a restart carrying a
// mid-episode LaunchedKeys marker must not relaunch on the next tick, and
// the reopen itself must survive the restart unchanged.
func TestTickAfterSchema2RestartDoesNotRelaunchAlreadyLaunchedReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	saved := State{
		SchemaVersion:       2,
		PR:                  "42",
		Repo:                "o/r",
		Agent:               "codex",
		IntervalSeconds:     60,
		CurrentDelaySeconds: 60,
		LastHeadSHA:         "sha1",
		PendingKeys:         []string{"comment:5"},
		LaunchedKeys:        []string{"comment:5"},
		PendingCommentAt:    "2026-01-01T00:00:00Z",
		PendingHeadSHA:      "sha1",
	}
	if err := save(path, saved); err != nil {
		t.Fatal(err)
	}
	restarted := load(path)

	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"sha1","closingIssuesReferences":[],"url":"https://example/pr/42"}`
	var calls [][]string
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`, unresolvedThreadFor(5)}, &calls)
	if _, _, err := tick(&restarted); err != nil {
		t.Fatal(err)
	}
	if n := countAddressReviewLaunches(calls); n != 0 {
		t.Fatalf("a restart carrying a mid-episode LaunchedKeys marker must not relaunch on the next tick: %#v", calls)
	}
	if !reflect.DeepEqual(restarted.PendingKeys, []string{"comment:5"}) {
		t.Fatalf("PendingKeys = %v, want unchanged [comment:5] across the restart", restarted.PendingKeys)
	}
}

// -- #975: supervisor log file must not perturb the close guard's glob ------

// TestBlocksCloseIgnoresSupervisorLogFiles pins a #975 regression: the new
// per-repo/PR supervisor log file (auto-adopted answer #6,
// "<state-dir>/<sha256(repo)[:6]hex>-<pr>.log") lives beside the state file
// in the same state directory. BlocksClose's directory scan
// (filepath.Glob(dir, "*.json")) must keep finding and correctly
// classifying the real state file even with an unrelated ".log" sibling
// present -- the log file must never be mistaken for state, and must never
// cause the real state file to be skipped.
func TestBlocksCloseIgnoresSupervisorLogFiles(t *testing.T) {
	stubProcessOwned(t, true)
	dir := t.TempDir()
	writeGuardState(t, dir, State{PR: "790", RepoRoot: "/repo/root", ClosingIssues: []int{782}, CIStatus: "failing", PID: 4242, Status: "running"})
	if err := os.WriteFile(filepath.Join(dir, "aabbcc-790.log"), []byte("supervisor stdout/stderr\n"), 0600); err != nil {
		t.Fatal(err)
	}

	blocks, reason := BlocksClose("782", "/repo/root", dir)
	if !blocks {
		t.Fatal("BlocksClose = false, want the real state file still found alongside its .log sibling")
	}
	if !strings.Contains(reason, "#790") {
		t.Errorf("reason = %q, want it to name PR #790", reason)
	}
}
