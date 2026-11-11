package babysit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// TestMain pins the fleetConfigPath seam to "" (disabled) for every test in
// the package (watch/docs/test-isolation.md's "audit ALL existing tests"
// rule): without this, a dev machine with automerge.enabled: true in
// ~/.config/cenci/config.json would make every pre-existing tick test issue
// an unscripted 5th gh call and fail. Tests that need automerge enabled
// override the seam themselves (see automerge_test.go's
// withFleetAutomergeEnabled) and restore it via t.Cleanup, which puts the
// seam back to this "" pin, not whatever the real environment had set.
//
// It also pins the #1457 worker-inventory seams: tmuxPaneInventory reports a
// session with no repair worker, and gitCommonDir fails, so no test runs a real
// tmux or git unless it installs its own stub or fake binary.
func TestMain(m *testing.M) {
	original := fleetConfigPath
	originalInventory := tmuxPaneInventory
	originalCommonDir := gitCommonDir
	fleetConfigPath = func() string { return "" }
	tmuxPaneInventory = func(string, string) (string, error) { return noWorkerInventory, nil }
	gitCommonDir = func(string) (string, error) { return "", errors.New("gitCommonDir is not stubbed in this test") }
	code := m.Run()
	fleetConfigPath = original
	tmuxPaneInventory = originalInventory
	gitCommonDir = originalCommonDir
	os.Exit(code)
}

// noWorkerInventory is a 3-column pane inventory with no repair worker in it.
const noWorkerInventory = "1-implement\t0\t\n"

// paneRowOf renders one inventory row: window name, pane_dead and repo-dir.
func paneRowOf(window string, dead bool, repoDir string) string {
	d := "0"
	if dead {
		d = "1"
	}
	return window + "\t" + d + "\t" + repoDir + "\n"
}

// stubPanes replaces the tmuxPaneInventory seam with inventory, which receives
// the 1-based read number, and returns the recorded reads as
// "<socket>|<session>".
func stubPanes(t *testing.T, inventory func(read int) (string, error)) *[]string {
	t.Helper()
	var reads []string
	original := tmuxPaneInventory
	tmuxPaneInventory = func(socket, session string) (string, error) {
		reads = append(reads, socket+"|"+session)
		return inventory(len(reads))
	}
	t.Cleanup(func() { tmuxPaneInventory = original })
	return &reads
}

// panesAlways serves the same inventory on every read.
func panesAlways(inventory string) func(int) (string, error) {
	return func(int) (string, error) { return inventory, nil }
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
// after calling withCommands. The #1457 pane inventory seam is reset to a
// session with no repair worker; stubPanes overrides it.
func withCommands(t *testing.T, responses []string, calls *[][]string) {
	t.Helper()
	originalCommand := command
	originalExecGh := execGh
	originalCurrentTmuxSession := currentTmuxSession
	originalTmuxHasSession := tmuxHasSession
	originalInventory := tmuxPaneInventory
	i := 0
	command = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		return []byte(""), nil
	}
	execGh = func(args ...string) (string, string, error) {
		// #1472: the PR conversation read and the collaborator-permission lookup
		// are answered by path, never consuming a positional slot nor appearing in
		// calls, so every positional script keeps its pre-#1472 shape.
		if out, err, ok := ghPathAnswer(args); ok {
			return out, "", err
		}
		*calls = append(*calls, append([]string{"gh"}, args...))
		if i >= len(responses) {
			return "", "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
		out := responses[i]
		i++
		return out, "", nil
	}
	currentTmuxSession = func() (string, error) { return "test-session", nil }
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	tmuxPaneInventory = func(string, string) (string, error) { return noWorkerInventory, nil }
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		currentTmuxSession = originalCurrentTmuxSession
		tmuxHasSession = originalTmuxHasSession
		tmuxPaneInventory = originalInventory
	})
}

// -- #1472: PR conversation comments and collaborator permission -------------

var (
	// discussionServed is the page body every fake gh serves, for any page, on
	// `repos/<repo>/issues/<n>/comments` (default: no comments); discussionErr, when
	// set, makes that read fail. permissionServed maps a login to the
	// `permission` value `repos/<repo>/collaborators/<login>/permission` answers
	// (an unlisted login is `none`; humanComment's author defaults to `write` so a
	// launch over her comments is trusted unless a test says otherwise).
	// permissionFailing lists logins whose lookup errors. All are restored by the
	// serve* helpers' cleanup.
	discussionServed = "[]"
	discussionErr    error
	// discussionSequence, when set, serves its k-th body (the last repeats) on
	// the k-th discussion read instead of discussionServed; discussionReads counts
	// the reads (a one-page read is one request).
	discussionSequence []string
	discussionReads    int
	permissionServed   = map[string]string{humanLogin: "write"}
	permissionFailing  = map[string]bool{}

	discussionPathRe = regexp.MustCompile(`^repos/[^/]+/[^/]+/issues/[0-9]+/comments\?`)
	permissionPathRe = regexp.MustCompile(`^repos/[^/]+/[^/]+/collaborators/([^/]+)/permission$`)
)

// ghPathAnswer answers the two #1472 gh reads by path. Every fake gh in this
// package that serves a tick calls it first, so a discussion read never shifts a
// positional script.
func ghPathAnswer(args []string) (out string, err error, ok bool) {
	if len(args) != 2 || args[0] != "api" {
		return "", nil, false
	}
	switch {
	case discussionPathRe.MatchString(args[1]):
		discussionReads++
		if len(discussionSequence) > 0 {
			return discussionSequence[min(discussionReads, len(discussionSequence))-1], nil, true
		}
		return discussionServed, discussionErr, true
	case permissionPathRe.MatchString(args[1]):
		login := permissionPathRe.FindStringSubmatch(args[1])[1]
		if permissionFailing[login] {
			return "", errors.New("gh: permission lookup failed"), true
		}
		p, listed := permissionServed[login]
		if !listed {
			p = "none"
		}
		return fmt.Sprintf(`{"permission":%q}`, p), nil, true
	}
	return "", nil, false
}

// serveDiscussion makes every fake gh answer the PR conversation read with the
// given comment objects (see discussionJSON).
func serveDiscussion(t *testing.T, comments ...string) {
	t.Helper()
	serveDiscussionBody(t, jsonArray(comments...), nil)
}

// serveDiscussionBody serves a raw page body and error for every page.
func serveDiscussionBody(t *testing.T, body string, err error) {
	t.Helper()
	originalBody, originalErr := discussionServed, discussionErr
	discussionServed, discussionErr = body, err
	t.Cleanup(func() { discussionServed, discussionErr = originalBody, originalErr })
}

// serveDiscussionSequence serves one body per discussion read, in order (the last
// repeats), and restarts the read count: a tick's read, then the pre-merge
// recheck's read, can differ.
func serveDiscussionSequence(t *testing.T, bodies ...string) {
	t.Helper()
	discussionSequence, discussionReads = bodies, 0
	t.Cleanup(func() { discussionSequence, discussionReads = nil, 0 })
}

// servePermission grants login the named collaborator permission.
func servePermission(t *testing.T, login, permission string) {
	t.Helper()
	original := permissionServed
	permissionServed = maps.Clone(original)
	permissionServed[login] = permission
	t.Cleanup(func() { permissionServed = original })
}

// servePermissionFailure makes the collaborator-permission lookup for login
// fail (a gh error), as a timeout or a decode failure would.
func servePermissionFailure(t *testing.T, login string) {
	t.Helper()
	original := permissionFailing
	permissionFailing = maps.Clone(original)
	permissionFailing[login] = true
	t.Cleanup(func() { permissionFailing = original })
}

// servePermission must override a login the fake already serves (alice defaults
// to write): the override wins, and the default is back once the test ends.
func TestServePermissionOverridesAnAlreadyServedLogin(t *testing.T) {
	path := "repos/o/r/collaborators/" + humanLogin + "/permission"
	served := func() string {
		out, _, _ := ghPathAnswer([]string{"api", path})
		return out
	}
	t.Run("override", func(t *testing.T) {
		servePermission(t, humanLogin, "read")
		if got, want := served(), `{"permission":"read"}`; got != want {
			t.Fatalf("permission for %s = %s, want %s", humanLogin, got, want)
		}
	})
	if got, want := served(), `{"permission":"write"}`; got != want {
		t.Fatalf("after the override's cleanup: permission = %s, want the default %s", got, want)
	}
}

// servePermissionFailure must make an already-failing-or-served login fail, and
// restore it afterwards.
func TestServePermissionFailureOverridesAnAlreadyServedLogin(t *testing.T) {
	path := "repos/o/r/collaborators/" + humanLogin + "/permission"
	t.Run("failure", func(t *testing.T) {
		permissionFailing[humanLogin] = false
		t.Cleanup(func() { delete(permissionFailing, humanLogin) })
		servePermissionFailure(t, humanLogin)
		if _, err, _ := ghPathAnswer([]string{"api", path}); err == nil {
			t.Fatalf("lookup for %s succeeded, want the failure to win over the earlier false entry", humanLogin)
		}
	})
}

// withReviewSeams pins the two review-launch seams: the nonces newReviewNonce
// mints, in order (the last repeats), and the launch clock.
func withReviewSeams(t *testing.T, at time.Time, nonces ...string) {
	t.Helper()
	originalNonce, originalClock := newReviewNonce, launchClock
	n := 0
	newReviewNonce = func() (string, error) {
		i := n
		if i >= len(nonces) {
			i = len(nonces) - 1
		}
		n++
		return nonces[i], nil
	}
	launchClock = func() time.Time { return at }
	t.Cleanup(func() { newReviewNonce, launchClock = originalNonce, originalClock })
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
		if len(c) > 3 && c[1] == "run" && c[2] == "babysit-review" && strings.HasPrefix(c[3], "42 --receipt-nonce ") {
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
		// #924: zero checks now reports ciStatusUnknown instead of "" (D2) --
		// "not started" must not read as "passed" -- and the first such
		// observation must also start the settle clock (checked below).
		{"no checks is unknown", `[]`, ciStatusUnknown},
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
			if tc.want == ciStatusUnknown && s.ChecksAbsentSince.IsZero() {
				t.Error("ChecksAbsentSince is zero, want tick to start the settle clock on the first zero-checks observation (#924)")
			}
		})
	}
}

// TestCancelBucketCIStatusUnknownBoundedClose is a #1129 inversion of the old
// #787/#854 regression pin (formerly TestCancelBucketCIStillAllowsClose): a
// cancel-bucket-only CI result used to collapse to ciStatusGreen and allow an
// immediate close -- that was ciStatus's fail-open (watch/docs/error-
// handling.md's default-deny rule). It now reports ciStatusUnknown, starts
// the ChecksAbsentSince/ChecksAbsentHeadSHA settle clock (bounded by
// checksSettleGrace, watch/AGENTS.md's #1079 bounded-way-out rule), blocks
// `cenci close` inside the grace, self-heals past it, and never launches
// ci-repair or any other attention workflow (actionable stays false, AC 9) --
// a cancelled check is not a failing one.
func TestCancelBucketCIStatusUnknownBoundedClose(t *testing.T) {
	stubProcessOwned(t, true)
	dir := t.TempDir()
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withCommands(t, []string{prWithIssue, `[{"bucket":"cancel","name":"a"}]`, `[]`, `[]`}, &calls)
	// PID must be set (mirroring TestBlocksCloseMatrix's live fixture) so
	// supervisorLive's own s.PID > 0 gate is satisfied and stubProcessOwned's
	// stubbed "live" answer is actually consulted -- tick() itself never
	// touches PID, so this value survives unchanged into writeGuardState.
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, PID: 4242}
	before := time.Now().UTC()
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.CIStatus != ciStatusUnknown {
		t.Fatalf("CIStatus = %q, want %q (#1129): a cancel-only bucket must no longer collapse to green", s.CIStatus, ciStatusUnknown)
	}
	if s.ChecksAbsentSince.Before(before) {
		t.Errorf("ChecksAbsentSince = %v, want a timestamp at/after %v: the settle clock must start on this first cancel-bucket observation", s.ChecksAbsentSince, before)
	}
	if _, found := launchCallArgs(calls, "ci-repair"); found {
		t.Fatalf("ci-repair was launched for a cancel-only bucket, want no launch (AC 9): %#v", calls)
	}
	// actionable (a local var in tick, not persisted) is only observable via
	// its backoff side effect: a cancel bucket triggers no other actionable
	// path (no failing checks, no pending review keys, automerge disabled by
	// default in tests), so a false actionable must double CurrentDelaySeconds
	// rather than leave it pinned at IntervalSeconds.
	if s.CurrentDelaySeconds != s.IntervalSeconds*2 {
		t.Errorf("CurrentDelaySeconds = %d, want %d: actionable must stay false for a cancel-only bucket (AC 9), backing off instead of polling at interval", s.CurrentDelaySeconds, s.IntervalSeconds*2)
	}

	writeGuardState(t, dir, s)
	blocks, _ := BlocksClose("782", "", dir)
	if !blocks {
		t.Fatal("BlocksClose = false, want true: a fresh ciStatusUnknown verdict must still block inside the settle grace")
	}

	// Past the settle grace, the same verdict must self-heal and stop
	// blocking -- the bounded-way-out half of the fix (#1079).
	pastGrace := s
	pastGrace.ChecksAbsentSince = time.Now().UTC().Add(-(checksSettleGrace + time.Minute))
	writeGuardState(t, dir, pastGrace)
	blocksPastGrace, _ := BlocksClose("782", "", dir)
	if blocksPastGrace {
		t.Fatal("BlocksClose = true, want false: a ciStatusUnknown verdict must stop blocking once checksSettleGrace has elapsed")
	}
}

// TestCIStatusPrecedenceMatrix pins ciStatus's full precedence contract
// (#1129 Decision): fail > pending > unknown > green. cancel/empty/
// unrecognized buckets are folded into ciStatusUnknown -- closing the guard's
// old fail-open -- but never outrank a genuine fail or pending, and
// skipping stays green-compatible (including a skipping-only, zero-pass set)
// so `cenci close` behavior on this repo's own path-filtered PRs is
// unchanged.
func TestCIStatusPrecedenceMatrix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		checks []check
		want   string
	}{
		{"pending plus cancel is pending: pending outranks unknown", []check{{Bucket: "pending"}, {Bucket: "cancel"}}, ciStatusPending},
		{"fail plus cancel is failing: fail outranks unknown", []check{{Bucket: "fail"}, {Bucket: "cancel"}}, ciStatusFailing},
		{"pass plus skipping is green (unchanged)", []check{{Bucket: "pass"}, {Bucket: "skipping"}}, ciStatusGreen},
		{"skipping-only with zero pass is green (unchanged)", []check{{Bucket: "skipping"}}, ciStatusGreen},
		{"empty-bucket-only is unknown (fail-open closed)", []check{{Bucket: ""}}, ciStatusUnknown},
		{"unrecognized-value-only is unknown (fail-open closed)", []check{{Bucket: "neutral"}}, ciStatusUnknown},
		{"cancel-only is unknown (fail-open closed, inverts old green)", []check{{Bucket: "cancel"}}, ciStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ciStatus(tc.checks)
			if got != tc.want {
				t.Fatalf("ciStatus(%v) = %q, want %q", tc.checks, got, tc.want)
			}
		})
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
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
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
	if s.CIStatus != ciStatusUnknown {
		t.Errorf("CIStatus = %q, want %q (zero checks) (#924)", s.CIStatus, ciStatusUnknown)
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
	if s.ChecksAbsentSince.IsZero() {
		t.Error("ChecksAbsentSince is zero, want tick to start the settle clock on a gh pr view failure too (#924 Q1: both unknown causes share the clock)")
	}
}

// -- checks-absent settle clock lifecycle (#924) -----------------------------
//
// Bounds the two ciStatusUnknown-writing causes (zero checks, and a genuine
// gh read failure) with the same checksSettleGrace clock (Q&A 11's Q1
// reconciliation with #923): tick starts/maintains ChecksAbsentSince and
// ChecksAbsentHeadSHA at every unknown-writing site, restarts on a new head
// SHA, and clears once real checks show up again.

// TestTickKeepsChecksAbsentClockAcrossRepeatedZeroChecksTicks pins that a
// second consecutive zero-checks tick, on the same head SHA, must not
// restart the clock -- otherwise a supervisor that keeps polling a no-CI PR
// would never cross the settle grace.
func TestTickKeepsChecksAbsentClockAcrossRepeatedZeroChecksTicks(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withCommands(t, []string{prWithIssue, `[]`, `[]`, `[]`}, &calls)
	original := time.Now().UTC().Add(-5 * time.Minute)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, ChecksAbsentSince: original, ChecksAbsentHeadSHA: "abc"}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.CIStatus != ciStatusUnknown {
		t.Fatalf("CIStatus = %q, want ciStatusUnknown", s.CIStatus)
	}
	if !s.ChecksAbsentSince.Equal(original) {
		t.Errorf("ChecksAbsentSince = %v, want unchanged %v: a repeated zero-checks observation on the same head SHA must not restart the clock", s.ChecksAbsentSince, original)
	}
}

// TestTickRestartsChecksAbsentClockOnNewHeadSHA pins the reset half: a new
// head commit invalidates the previous observation window entirely, even if
// checks are still zero.
func TestTickRestartsChecksAbsentClockOnNewHeadSHA(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"def","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withCommands(t, []string{prWithIssue, `[]`, `[]`, `[]`}, &calls)
	stale := time.Now().UTC().Add(-20 * time.Minute)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, ChecksAbsentSince: stale, ChecksAbsentHeadSHA: "abc"}
	before := time.Now().UTC()
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if !s.ChecksAbsentSince.After(stale) || s.ChecksAbsentSince.Before(before) {
		t.Errorf("ChecksAbsentSince = %v, want a fresh timestamp at/after %v (a new head SHA must restart the clock)", s.ChecksAbsentSince, before)
	}
	if s.ChecksAbsentHeadSHA != "def" {
		t.Errorf("ChecksAbsentHeadSHA = %q, want %q", s.ChecksAbsentHeadSHA, "def")
	}
}

// TestTickClearsChecksAbsentClockWhenChecksAppear pins the clear side: once
// real checks show up, the clock and its head SHA must be wiped so a later
// gap (e.g. a genuine read failure) starts its own fresh window instead of
// inheriting a stale one.
func TestTickClearsChecksAbsentClockWhenChecksAppear(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withCommands(t, []string{prWithIssue, `[{"bucket":"pass","name":"a"}]`, `[]`, `[]`}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		ChecksAbsentSince: time.Now().UTC().Add(-5 * time.Minute), ChecksAbsentHeadSHA: "abc",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.CIStatus != "green" {
		t.Fatalf("CIStatus = %q, want green", s.CIStatus)
	}
	if !s.ChecksAbsentSince.IsZero() {
		t.Errorf("ChecksAbsentSince = %v, want zero once real checks appear", s.ChecksAbsentSince)
	}
	if s.ChecksAbsentHeadSHA != "" {
		t.Errorf("ChecksAbsentHeadSHA = %q, want empty once real checks appear", s.ChecksAbsentHeadSHA)
	}
}

// TestTickStartsChecksAbsentClockOnGenuineChecksReadFailure covers the Q1
// reconciliation: a genuine (non-tolerated) gh pr checks failure must also
// start the settle clock, exactly like the zero-checks case, so a broken gh
// auth or a lost network eventually stops wedging the window open forever.
func TestTickStartsChecksAbsentClockOnGenuineChecksReadFailure(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: prWithIssue},
		{out: "not json", err: &ghExitError{code: 1, err: errors.New("exit status 1")}, stderr: "rate limit exceeded"},
	}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	before := time.Now().UTC()
	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want the genuine checks read failure to surface")
	}
	if s.ChecksAbsentSince.Before(before) {
		t.Errorf("ChecksAbsentSince = %v, want a timestamp at/after %v", s.ChecksAbsentSince, before)
	}
	if s.ChecksAbsentHeadSHA != "abc" {
		t.Errorf("ChecksAbsentHeadSHA = %q, want %q", s.ChecksAbsentHeadSHA, "abc")
	}
}

// TestTickKeepsChecksAbsentClockAcrossRepeatedChecksReadFailures pins the
// same no-restart rule for the read-failure cause: repeated failures on the
// same head SHA must not push the settle window out indefinitely.
func TestTickKeepsChecksAbsentClockAcrossRepeatedChecksReadFailures(t *testing.T) {
	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: prWithIssue},
		{out: "not json", err: &ghExitError{code: 1, err: errors.New("exit status 1")}, stderr: "rate limit exceeded"},
	}, &calls)
	original := time.Now().UTC().Add(-5 * time.Minute)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, ChecksAbsentSince: original, ChecksAbsentHeadSHA: "abc"}
	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want the genuine checks read failure to surface")
	}
	if !s.ChecksAbsentSince.Equal(original) {
		t.Errorf("ChecksAbsentSince = %v, want unchanged %v across repeated failures on the same head SHA", s.ChecksAbsentSince, original)
	}
}

// -- #1169: ci-repair dispatch race between a tick and CI turning red -------
//
// The ci-repair dispatch guard used to be keyed on LastHeadSHA -- but the
// head-gated reset branch advanced LastHeadSHA on any tick where checks are
// not red *yet* (pending, or green-so-far), burning the only edge the guard
// could fire on. (#1456 removed that head gate: the green reset is now
// head-independent and only runs on a conflict-free green tick.) A tick observing checks still pending or green at a head
// commit, followed by a later tick observing that SAME head commit's checks
// have turned failing, must still dispatch ci-repair -- previously it never
// did, and the PR backed off to the 1h ceiling forever. The two tests below
// drive tick 1 through a REAL tick() call (not fixture pre-seeding) to reach
// pending/green state, precisely because pre-seeding is what let this bug
// ship undetected in every pre-existing test.

// TestTickCIRepairDispatchesOnPendingToFailingTransitionSameSHA is #1169's
// Transition 1 (pending -> failing, same SHA): tick 1 observes
// ciStatusPending at headSHA (reached via a real tick() call), tick 2
// observes ciStatusFailing at the identical HeadRefOID. Tick 2 must dispatch
// exactly one ci-repair, set FixAttempts to 1 and RepairPending to true,
// stay actionable (no backoff), and print no "quiet -- no new actionable
// work" line. The absence assertion is paired with a positive one (the
// automerge-disabled decision line, which recordDecision unconditionally
// prints every tick) so an accidentally-empty stdout capture cannot pass
// vacuously.
func TestTickCIRepairDispatchesOnPendingToFailingTransitionSameSHA(t *testing.T) {
	headSHA := "sha-transition-pending"
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		// Tick 1: checks still pending at headSHA.
		{out: cleanOpenPR(headSHA)},
		{out: `[{"bucket":"pending","name":"test","state":"PENDING"}]`},
		{out: `[]`},
		{out: `[]`},
		// Tick 2: same head SHA, checks now failing -- #1169's exact race.
		{out: cleanOpenPR(headSHA)},
		{out: `[{"bucket":"fail","name":"test","state":"FAILURE"}]`},
		{out: `[]`},
		{out: `[]`},
	}, &calls)
	// withScriptedCommands (unlike withCommands) installs no tmux seam
	// defaults (babysit_test.go's withCommands doc comment), so the launch
	// probe and target must be stubbed/set explicitly here.
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}

	// Tick 1 reaches ciStatusPending through a real tick() call.
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if s.CIStatus != ciStatusPending {
		t.Fatalf("tick 1: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusPending)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("tick 1: ci-repair launches = %d, want 0 (test setup): %#v", n, calls)
	}

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	originalStdout := os.Stdout
	os.Stdout = w
	_, delay, err := tick(&s)
	_ = w.Close()
	os.Stdout = originalStdout
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}

	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if s.CIStatus != ciStatusFailing {
		t.Fatalf("tick 2: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusFailing)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("tick 2: ci-repair launches = %d, want exactly 1: a failing CI result at an already-observed head SHA must still dispatch a repair (#1169): %#v", n, calls)
	}
	if s.FixAttempts != 1 {
		t.Fatalf("tick 2: FixAttempts = %d, want 1", s.FixAttempts)
	}
	if !s.RepairPending {
		t.Fatal("tick 2: RepairPending = false, want true")
	}
	if delay != time.Duration(s.IntervalSeconds)*time.Second || s.CurrentDelaySeconds != s.IntervalSeconds {
		t.Fatalf("tick 2: delay = %v, CurrentDelaySeconds = %d, want IntervalSeconds (%ds): a dispatched repair must stay actionable, never back off", delay, s.CurrentDelaySeconds, s.IntervalSeconds)
	}
	stdout := string(out)
	if !strings.Contains(stdout, reasonAutomergeDisabled) {
		t.Fatalf("tick 2 stdout = %q, want it to contain the automerge decision line %q -- otherwise the absence check below could pass vacuously on an empty capture", stdout, reasonAutomergeDisabled)
	}
	if strings.Contains(stdout, "quiet — no new actionable work") {
		t.Fatalf("tick 2 stdout = %q, want no 'quiet' backoff line: dispatching ci-repair means this tick is actionable", stdout)
	}
}

// TestTickCIRepairDispatchesOnGreenToFailingTransitionSameSHA is #1169's
// Transition 2 (green -> failing, same SHA): the same two-tick shape as
// above, except tick 1 observes ciStatusGreen (e.g. a check that later
// re-runs and fails, or reports late) rather than pending. A narrow "don't
// burn the SHA while pending" fix would not close this transition -- the
// dedicated LastRepairedSHA field must.
func TestTickCIRepairDispatchesOnGreenToFailingTransitionSameSHA(t *testing.T) {
	headSHA := "sha-transition-green"
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		// Tick 1: checks all green at headSHA.
		{out: cleanOpenPR(headSHA)},
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
		{out: `[]`},
		{out: `[]`},
		// Tick 2: same head SHA, a check re-runs/reports late and fails.
		{out: cleanOpenPR(headSHA)},
		{out: `[{"bucket":"fail","name":"test","state":"FAILURE"}]`},
		{out: `[]`},
		{out: `[]`},
	}, &calls)
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}

	// Tick 1 reaches ciStatusGreen through a real tick() call.
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if s.CIStatus != ciStatusGreen {
		t.Fatalf("tick 1: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusGreen)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("tick 1: ci-repair launches = %d, want 0 (test setup): %#v", n, calls)
	}

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	originalStdout := os.Stdout
	os.Stdout = w
	_, delay, err := tick(&s)
	_ = w.Close()
	os.Stdout = originalStdout
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}

	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if s.CIStatus != ciStatusFailing {
		t.Fatalf("tick 2: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusFailing)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("tick 2: ci-repair launches = %d, want exactly 1: a failing CI result at an already-observed (previously green) head SHA must still dispatch a repair (#1169): %#v", n, calls)
	}
	if s.FixAttempts != 1 {
		t.Fatalf("tick 2: FixAttempts = %d, want 1", s.FixAttempts)
	}
	if !s.RepairPending {
		t.Fatal("tick 2: RepairPending = false, want true")
	}
	if delay != time.Duration(s.IntervalSeconds)*time.Second || s.CurrentDelaySeconds != s.IntervalSeconds {
		t.Fatalf("tick 2: delay = %v, CurrentDelaySeconds = %d, want IntervalSeconds (%ds): a dispatched repair must stay actionable, never back off", delay, s.CurrentDelaySeconds, s.IntervalSeconds)
	}
	stdout := string(out)
	if !strings.Contains(stdout, reasonAutomergeDisabled) {
		t.Fatalf("tick 2 stdout = %q, want it to contain the automerge decision line %q -- otherwise the absence check below could pass vacuously on an empty capture", stdout, reasonAutomergeDisabled)
	}
	if strings.Contains(stdout, "quiet — no new actionable work") {
		t.Fatalf("tick 2 stdout = %q, want no 'quiet' backoff line: dispatching ci-repair means this tick is actionable", stdout)
	}
}

// TestTickCIRepairNoDispatchStormOnRepeatedFailingSameSHA is the "no
// dispatch storm" AC, rewritten for #1457's liveness-based retry episode: a
// tick still failing at the head a repair was already dispatched for launches
// again only once that worker has ended. A tracked dispatch with a live
// `42-ci-repair` pane launches nothing; the same state with no live pane
// retries at the unchanged head; a legacy state (the SHA marker without the
// tracking marker, written before #1457) keeps the old same-head hold, because
// its worker cannot be probed. Every case stays actionable (no backoff).
func TestTickCIRepairNoDispatchStormOnRepeatedFailingSameSHA(t *testing.T) {
	headSHA := "sha-storm"
	for _, tc := range []struct {
		name         string
		tracked      bool
		inventory    string
		wantLaunches int
		wantAttempts int
	}{
		{"tracked and live: no second launch", true, liveCI(), 0, 1},
		{"tracked and ended: retried at the same head", true, deadCI(), 1, 2},
		{"legacy marker: same-head hold kept", false, noWorkerInventory, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			withScriptedCommands(t, []scriptedCall{
				{out: cleanOpenPR(headSHA)},
				{out: `[{"bucket":"fail","name":"test","state":"FAILURE"}]`},
				{out: `[]`},
				{out: `[]`},
			}, &calls)
			originalTmuxHasSession := tmuxHasSession
			tmuxHasSession = func(string, string) (bool, error) { return true, nil }
			t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })
			stubPanes(t, panesAlways(tc.inventory))

			s := State{
				PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 900,
				LastHeadSHA: headSHA, LastRepairedSHA: headSHA, CIRepairTracked: tc.tracked,
				FixAttempts: 1, RepairPending: true,
				LaunchSession: "work", LaunchDir: "/repo/root",
			}
			if _, _, err := tick(&s); err != nil {
				t.Fatalf("tick: %v", err)
			}
			if n := countWorkflowLaunches(calls, "ci-repair"); n != tc.wantLaunches {
				t.Fatalf("ci-repair launches = %d, want %d: %#v", n, tc.wantLaunches, calls)
			}
			if s.FixAttempts != tc.wantAttempts {
				t.Fatalf("FixAttempts = %d, want %d", s.FixAttempts, tc.wantAttempts)
			}
			if !s.RepairPending {
				t.Fatal("RepairPending = false, want true")
			}
			if s.CurrentDelaySeconds != s.IntervalSeconds {
				t.Fatalf("CurrentDelaySeconds = %d, want %d (IntervalSeconds): still actionable, no backoff", s.CurrentDelaySeconds, s.IntervalSeconds)
			}
		})
	}
}

// TestTickCIRepairReArmsOnNewFailingHeadSHA is the "new push re-arms" AC:
// after the head SHA changes and checks are still failing, a new ci-repair
// must dispatch and FixAttempts must increment -- fixCap still bounds
// repairs across distinct failing commits, unchanged from before #1169.
func TestTickCIRepairReArmsOnNewFailingHeadSHA(t *testing.T) {
	oldSHA, newSHA := "sha-old", "sha-new"
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: cleanOpenPR(newSHA)},
		{out: `[{"bucket":"fail","name":"test","state":"FAILURE"}]`},
		{out: `[]`},
		{out: `[]`},
	}, &calls)
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })

	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA: oldSHA, LastRepairedSHA: oldSHA,
		FixAttempts: 1, RepairPending: true,
		LaunchSession: "work",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("ci-repair launches = %d, want exactly 1: a new push at a distinct failing head SHA must re-arm dispatch: %#v", n, calls)
	}
	if s.FixAttempts != 2 {
		t.Fatalf("FixAttempts = %d, want 2: fixCap must still bound repairs across distinct failing commits", s.FixAttempts)
	}
	if s.LastHeadSHA != newSHA || s.LastRepairedSHA != newSHA {
		t.Fatalf("LastHeadSHA/LastRepairedSHA = %q/%q, want both %q", s.LastHeadSHA, s.LastRepairedSHA, newSHA)
	}
}

// TestTickCIRepairRetryBudgetSurvivesPendingTickBetweenFailingSHAs is a
// code-review follow-up to #1169: a pending tick landing between two
// distinct failing head SHAs (the repair agent pushed a fix, CI has not
// turned red -- or green -- yet) must not reset the retry budget. Before
// this fix, the reset branch fired unconditionally on any head-SHA
// change, including a still-pending one, silently zeroing FixAttempts and
// making fixCap unreachable on a persistently-red PR: a pending tick
// between two failing commits is the very same tick/CI race #1169 itself
// closed for the dispatch guard, not an edge case for the reset branch.
// This overrides the ticket's original Assumption that "the reset branch
// keeps advancing LastHeadSHA unchanged; only the dispatch guard's
// comparand changes" -- that assumption is incompatible with fixCap's own
// AC ("still bounds repairs across distinct failing commits"). The head
// gate itself was removed in #1456: the green reset is now head-independent
// and only runs on a conflict-free green tick, so a pending tick never resets.
func TestTickCIRepairRetryBudgetSurvivesPendingTickBetweenFailingSHAs(t *testing.T) {
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		// Tick 1: sha-1 failing -- dispatch #1.
		{out: cleanOpenPR("sha-1")},
		{out: `[{"bucket":"fail","name":"test","state":"FAILURE"}]`},
		{out: `[]`},
		{out: `[]`},
		// Tick 2: sha-2, checks still pending -- the repair agent pushed,
		// CI has not turned red (or green) yet.
		{out: cleanOpenPR("sha-2")},
		{out: `[{"bucket":"pending","name":"test","state":"PENDING"}]`},
		{out: `[]`},
		{out: `[]`},
		// Tick 3: sha-2, checks now failing -- dispatch #2 (a distinct SHA
		// from sha-1's LastRepairedSHA).
		{out: cleanOpenPR("sha-2")},
		{out: `[{"bucket":"fail","name":"test","state":"FAILURE"}]`},
		{out: `[]`},
		{out: `[]`},
	}, &calls)
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if s.FixAttempts != 1 {
		t.Fatalf("tick 1: FixAttempts = %d, want 1 (test setup)", s.FixAttempts)
	}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if s.CIStatus != ciStatusPending {
		t.Fatalf("tick 2: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusPending)
	}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 2 {
		t.Fatalf("ci-repair launches = %d, want exactly 2: a pending tick between two failing SHAs must not reset the retry budget: %#v", n, calls)
	}
	if s.FixAttempts != 2 {
		t.Fatalf("FixAttempts = %d, want 2: a still-pending tick at a new head SHA must not reset the retry budget -- only a GREEN observation proves the repair succeeded", s.FixAttempts)
	}
}

// TestTickCIRepairRetryBudgetSurvivesUnknownCIAtNewHeadSHA pins the other
// deliberately-conservative half of the green-only reset: a new head SHA
// observed with zero checks (ciStatusUnknown, #924) must not reset the retry
// budget either -- only a genuine GREEN observation proves the repair
// succeeded, not merely "CI hasn't reported red yet". LastHeadSHA must ALSO
// stay unchanged (a second code-review follow-up to #1169): only the green
// reset advances it, so a non-green tick leaves it alone and a LATER tick
// observing sha-new has actually gone green still resets the budget (see
// TestTickCIRepairGreenAfterUnknownAtNewSHAStillResetsBudget, which pins
// that exact chained scenario end to end).
func TestTickCIRepairRetryBudgetSurvivesUnknownCIAtNewHeadSHA(t *testing.T) {
	oldSHA, newSHA := "sha-old", "sha-new"
	var calls [][]string
	withCommands(t, []string{cleanOpenPR(newSHA), `[]`, `[]`, `[]`}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA: oldSHA, LastRepairedSHA: oldSHA,
		FixAttempts: 1, RepairPending: true,
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if s.CIStatus != ciStatusUnknown {
		t.Fatalf("CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusUnknown)
	}
	if s.FixAttempts != 1 {
		t.Fatalf("FixAttempts = %d, want unchanged 1: a zero-checks (ciStatusUnknown) observation at a new head SHA must not reset the retry budget", s.FixAttempts)
	}
	if !s.RepairPending {
		t.Fatal("RepairPending = false, want unchanged true")
	}
	if s.LastHeadSHA != oldSHA {
		t.Fatalf("LastHeadSHA = %q, want unchanged %q: the reset branch must not advance LastHeadSHA on a non-green tick, or a LATER green observation at %s could never re-enter the branch", s.LastHeadSHA, oldSHA, newSHA)
	}
}

// TestTickCIRepairSuccessfulRepairResetsFixAttemptsAndRepairPending is the
// "successful repair resets" AC: a tick observing a new head SHA with CI
// actually GREEN must still reset FixAttempts to 0 and RepairPending to
// false -- no regression in the automerge reasonRepairPending release
// path. The reset is now explicitly gated on ciStatusGreen (a code-review
// follow-up to #1169: a pending or unknown observation at a new head SHA
// must NOT reset the budget, see
// TestTickCIRepairRetryBudgetSurvivesPendingTickBetweenFailingSHAs/
// TestTickCIRepairRetryBudgetSurvivesUnknownCIAtNewHeadSHA above).
// LastRepairedSHA is untouched by this branch, so it still names the
// superseded SHA and is simply never matched again.
func TestTickCIRepairSuccessfulRepairResetsFixAttemptsAndRepairPending(t *testing.T) {
	oldSHA, newSHA := "sha-old", "sha-fixed"
	var calls [][]string
	withCommands(t, []string{cleanOpenPR(newSHA), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		LastHeadSHA: oldSHA, LastRepairedSHA: oldSHA,
		FixAttempts: 1, RepairPending: true,
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if s.FixAttempts != 0 {
		t.Fatalf("FixAttempts = %d, want reset to 0", s.FixAttempts)
	}
	if s.RepairPending {
		t.Fatal("RepairPending = true, want reset to false: the automerge reasonRepairPending release path depends on this")
	}
	if s.LastHeadSHA != newSHA {
		t.Fatalf("LastHeadSHA = %q, want %q", s.LastHeadSHA, newSHA)
	}
	if s.LastRepairedSHA != oldSHA {
		t.Fatalf("LastRepairedSHA = %q, want unchanged %q", s.LastRepairedSHA, oldSHA)
	}
}

// TestTickCIRepairGreenAfterPendingAtNewSHAStillResetsBudget is a second
// code-review follow-up to #1169: a GREEN observation must still be able to
// reset the retry budget even when an earlier tick already saw that same
// new head SHA as pending. Before this fix, the reset branch's
// `s.LastHeadSHA = pr.HeadRefOID` line ran unconditionally, outside the
// `if s.CIStatus == ciStatusGreen` guard -- so the PENDING tick at sha-2
// already advanced LastHeadSHA to sha-2, permanently spending the branch's
// only trigger for that SHA (`pr.HeadRefOID != s.LastHeadSHA`; that head
// gate was removed in #1456, the green reset no longer compares heads). The later
// GREEN tick at that same sha-2 could then never re-enter the branch, and
// RepairPending stayed stuck true forever -- blocking automerge.go:543's
// `!RepairPending` gate on any PR that took more than one poll to settle
// green after a repair, which is the ordinary case (CI virtually always
// reports pending at least once before passing). All three ticks are real
// tick() calls -- driving this precondition through fixture pre-seeding
// (as TestTickCIRepairSuccessfulRepairResetsFixAttemptsAndRepairPending
// above does, for an unrelated, narrower single-tick scenario) would land
// directly on the branch's first observation of the new SHA and never
// exercise the intervening non-green tick that actually breaks this.
func TestTickCIRepairGreenAfterPendingAtNewSHAStillResetsBudget(t *testing.T) {
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		// Tick 1: sha-1 failing -- dispatch.
		{out: cleanOpenPR("sha-1")},
		{out: `[{"bucket":"fail","name":"test","state":"FAILURE"}]`},
		{out: `[]`},
		{out: `[]`},
		// Tick 2: sha-2, checks still pending -- the repair agent pushed,
		// CI has not settled yet.
		{out: cleanOpenPR("sha-2")},
		{out: `[{"bucket":"pending","name":"test","state":"PENDING"}]`},
		{out: `[]`},
		{out: `[]`},
		// Tick 3: sha-2, checks now green -- the repair actually succeeded.
		{out: cleanOpenPR("sha-2")},
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
		{out: `[]`},
		{out: `[]`},
	}, &calls)
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if s.FixAttempts != 1 || !s.RepairPending {
		t.Fatalf("tick 1: FixAttempts=%d RepairPending=%v, want 1/true (test setup)", s.FixAttempts, s.RepairPending)
	}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if s.CIStatus != ciStatusPending {
		t.Fatalf("tick 2: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusPending)
	}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if s.CIStatus != ciStatusGreen {
		t.Fatalf("tick 3: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusGreen)
	}
	if s.FixAttempts != 0 {
		t.Fatalf("tick 3: FixAttempts = %d, want 0: a green observation at a head SHA an earlier tick already saw pending must still reset the retry budget", s.FixAttempts)
	}
	if s.RepairPending {
		t.Fatal("tick 3: RepairPending = true, want false: automerge.go:543's !RepairPending gate must release once a repair actually settles green")
	}
}

// TestTickCIRepairGreenAfterUnknownAtNewSHAStillResetsBudget is the
// ciStatusUnknown (#924, zero checks/unreadable) analogue of
// TestTickCIRepairGreenAfterPendingAtNewSHAStillResetsBudget above: unknown
// is gated identically to pending in the reset branch, so it must not
// spend the branch's trigger for that SHA either.
func TestTickCIRepairGreenAfterUnknownAtNewSHAStillResetsBudget(t *testing.T) {
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		// Tick 1: sha-1 failing -- dispatch.
		{out: cleanOpenPR("sha-1")},
		{out: `[{"bucket":"fail","name":"test","state":"FAILURE"}]`},
		{out: `[]`},
		{out: `[]`},
		// Tick 2: sha-2, zero checks reported -- ciStatusUnknown (#924).
		{out: cleanOpenPR("sha-2")},
		{out: `[]`},
		{out: `[]`},
		{out: `[]`},
		// Tick 3: sha-2, checks now green.
		{out: cleanOpenPR("sha-2")},
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
		{out: `[]`},
		{out: `[]`},
	}, &calls)
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if s.FixAttempts != 1 || !s.RepairPending {
		t.Fatalf("tick 1: FixAttempts=%d RepairPending=%v, want 1/true (test setup)", s.FixAttempts, s.RepairPending)
	}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if s.CIStatus != ciStatusUnknown {
		t.Fatalf("tick 2: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusUnknown)
	}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if s.CIStatus != ciStatusGreen {
		t.Fatalf("tick 3: CIStatus = %q, want %q (test setup)", s.CIStatus, ciStatusGreen)
	}
	if s.FixAttempts != 0 {
		t.Fatalf("tick 3: FixAttempts = %d, want 0: a green observation at a head SHA an earlier tick already saw ciStatusUnknown must still reset the retry budget", s.FixAttempts)
	}
	if s.RepairPending {
		t.Fatal("tick 3: RepairPending = true, want false")
	}
}

// TestTickCIRepairRetryCapReachedOnlyAfterDistinctFailingHeadSHAs is the
// "retry cap still per-distinct-SHA" AC: fixCap escalation must count
// distinct failing head commits, never repeated ticks at one SHA (Decision:
// "cap semantics stay per-commit, not per-tick"). Drives fixCap distinct
// failing SHAs to reach the cap; each round interleaves TWO kinds of tick
// that must not advance FixAttempts: a repeated tick at the same SHA while
// that round's worker is still live (storm check; since #1457 an ended worker
// is retried at the same SHA) AND a PENDING tick at the next round's SHA (the
// coordinator's code-review follow-up: the repair agent pushed, CI has not
// reported red -- or green -- yet, the common case, not an edge case).
// A final distinct failing SHA then trips the retry-cap escalation (launch
// babysit-attention once, record the CI pause, set Status to "needs-input"
// and keep going: the tick returns no error, #1456).
func TestTickCIRepairRetryCapReachedOnlyAfterDistinctFailingHeadSHAs(t *testing.T) {
	shas := []string{"sha-1", "sha-2", "sha-3", "sha-4"}
	if len(shas) != fixCap+1 {
		t.Fatalf("test setup: need exactly fixCap+1 (%d) distinct SHAs, got %d", fixCap+1, len(shas))
	}
	failJSON := `[{"bucket":"fail","name":"test","state":"FAILURE"}]`
	pendingJSON := `[{"bucket":"pending","name":"test","state":"PENDING"}]`
	var script []scriptedCall
	for i, sha := range shas[:fixCap] {
		// Dispatch tick: a distinct failing SHA.
		script = append(script, scriptedCall{out: cleanOpenPR(sha)}, scriptedCall{out: failJSON}, scriptedCall{out: `[]`}, scriptedCall{out: `[]`})
		// Repeated tick at the same SHA, still failing (storm check).
		script = append(script, scriptedCall{out: cleanOpenPR(sha)}, scriptedCall{out: failJSON}, scriptedCall{out: `[]`}, scriptedCall{out: `[]`})
		// A pending tick at the NEXT round's SHA.
		nextSHA := shas[i+1]
		script = append(script, scriptedCall{out: cleanOpenPR(nextSHA)}, scriptedCall{out: pendingJSON}, scriptedCall{out: `[]`}, scriptedCall{out: `[]`})
	}
	// The cap-reached tick: the final SHA now failing. It no longer returns
	// early, so the comments/reviews fetches follow the checks fetch.
	finalSHA := shas[fixCap]
	script = append(script, scriptedCall{out: cleanOpenPR(finalSHA)}, scriptedCall{out: failJSON}, scriptedCall{out: `[]`}, scriptedCall{out: `[]`})

	var calls [][]string
	withScriptedCommands(t, script, &calls)
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { tmuxHasSession = originalTmuxHasSession })
	// #1457: the repeated tick at each SHA sees that round's worker still live
	// (the even inventory reads; pending ticks read none), so it is the live
	// worker, not the SHA, that keeps it from launching again.
	stubPanes(t, func(read int) (string, error) {
		return paneRowOf("42-ci-repair", read%2 == 1, ""), nil
	})

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work"}
	for i, sha := range shas[:fixCap] {
		if _, _, err := tick(&s); err != nil {
			t.Fatalf("dispatch tick %d (%s): %v", i+1, sha, err)
		}
		if s.FixAttempts != i+1 {
			t.Fatalf("after dispatch tick %d (%s): FixAttempts = %d, want %d", i+1, sha, s.FixAttempts, i+1)
		}

		if _, _, err := tick(&s); err != nil {
			t.Fatalf("repeated tick at %s: %v", sha, err)
		}
		if s.FixAttempts != i+1 {
			t.Fatalf("after repeated tick at %s: FixAttempts = %d, want unchanged %d: the cap must count distinct failing SHAs, never repeated ticks", sha, s.FixAttempts, i+1)
		}

		nextSHA := shas[i+1]
		if _, _, err := tick(&s); err != nil {
			t.Fatalf("pending tick at %s: %v", nextSHA, err)
		}
		if s.CIStatus != ciStatusPending {
			t.Fatalf("pending tick at %s: CIStatus = %q, want %q (test setup)", nextSHA, s.CIStatus, ciStatusPending)
		}
		if s.FixAttempts != i+1 {
			t.Fatalf("after pending tick at %s: FixAttempts = %d, want unchanged %d: a pending tick at a new head SHA must not reset the retry budget", nextSHA, s.FixAttempts, i+1)
		}
	}
	if s.FixAttempts != fixCap {
		t.Fatalf("FixAttempts = %d, want %d at the cap", s.FixAttempts, fixCap)
	}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick at the fixCap+1-th distinct SHA (%s): err = %v, want nil: the cap pauses CI repair without stopping the tick", finalSHA, err)
	}
	if s.Status != "needs-input" {
		t.Fatalf("Status = %q, want %q", s.Status, "needs-input")
	}
	if !s.CIRepairPaused || !s.CICapNotified || s.FixAttempts != fixCap {
		t.Fatalf("CIRepairPaused=%v CICapNotified=%v FixAttempts=%d, want true/true/%d", s.CIRepairPaused, s.CICapNotified, s.FixAttempts, fixCap)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != fixCap {
		t.Fatalf("ci-repair launches = %d, want exactly %d (one per distinct failing SHA): %#v", n, fixCap, calls)
	}
	if n := countWorkflowLaunches(calls, "babysit-attention"); n != 1 {
		t.Fatalf("babysit-attention launches = %d, want exactly 1: %#v", n, calls)
	}
}

// TestTickFailingCIActionableOnlyWhileRepairPending exercises the
// RepairPending scoping in both directions, on a tick where the head SHA is
// unchanged (LastHeadSHA AND LastRepairedSHA both pinned to the fixture
// PR's headRefOid, per watch/docs/test-strategy.md's rule against pinning
// only one dependent identity field (#824)) so the dispatch guard does not
// fire, and the green reset cannot fire on a failing CIStatus, leaving only
// the actionable-seeding logic under test: a failing CIStatus is actionable
// while RepairPending is true (the supervisor keeps polling at
// IntervalSeconds, since a repair agent is expected to push), and still
// backs off when RepairPending is false.
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
				LastHeadSHA:     "abc",
				LastRepairedSHA: "abc",
				RepairPending:   tc.repairPending,
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

// #1456: the green reset no longer depends on the head moving. A rerun of the
// failed checks that goes green at the very commit a ci-repair worker was
// dispatched for clears RepairPending and the budget, whether or not the
// supervisor was ever CI-paused.
func TestTickGreenAtTheSameHeadClearsRepairPendingAndBudget(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{openPR(), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		FixAttempts: 1, RepairPending: true, LastHeadSHA: "abc", LastRepairedSHA: "abc", Status: "needs-input",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if s.FixAttempts != 0 || s.RepairPending || s.Status != "running" || s.LastHeadSHA != "abc" {
		t.Fatalf("after a same-head green: FixAttempts=%d RepairPending=%v Status=%q LastHeadSHA=%q, want 0/false/running/abc", s.FixAttempts, s.RepairPending, s.Status, s.LastHeadSHA)
	}
}

// #1456: a tick in a conflict hold that shows green checks never resets the CI
// budget -- conflict repair keeps priority until the first non-held green tick.
func TestTickConflictHeldGreenTickDoesNotResetTheCIBudget(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("abc"), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work",
		FixAttempts: 2, RepairPending: true, LastHeadSHA: "old", LastRepairedSHA: "old",
		CIRepairPaused: true, CICapNotified: true, Status: "needs-input",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if s.FixAttempts != 2 || !s.RepairPending || !s.CIRepairPaused || !s.CICapNotified {
		t.Fatalf("after a conflict-held green tick: FixAttempts=%d RepairPending=%v CIRepairPaused=%v CICapNotified=%v, want 2/true/true/true",
			s.FixAttempts, s.RepairPending, s.CIRepairPaused, s.CICapNotified)
	}
	if n := countWorkflowLaunches(calls, "merge-repair"); n != 1 {
		t.Fatalf("merge-repair launches = %d, want 1: conflict repair keeps priority: %#v", n, calls)
	}
}

// With every non-conflict-held green tick now clearing RepairPending, the
// automerge "CI repair pending" hold is only reachable while the reset is
// suppressed by a conflict hold: the conflict episode is held open (a recorded
// ConflictRepairSHA, mergeability still UNKNOWN) while the checks are green.
func TestTickAutomergeHeldOnCIRepairPendingWhileTheGreenResetIsSuppressed(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","baseRefName":"main","mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN","isDraft":false,"changedFiles":1,"additions":5,"deletions":2,"files":[{"path":"watch/internal/babysit/x.go"}],"url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: pr},
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
		{out: `[]`},
		{out: `[]`},
		{out: `{"labels":[{"name":"automerge:ok"}]}`},
	}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work",
		RepairPending: true, LastHeadSHA: "abc", ConflictRepairSHA: "abc",
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if s.AutomergeReason != reasonRepairPending {
		t.Fatalf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonRepairPending)
	}
	for _, c := range calls {
		if len(c) > 2 && c[1] == "pr" && c[2] == "merge" {
			t.Fatalf("gh pr merge must not be issued while held: %#v", calls)
		}
	}
}

// TestRunWritesStateBeforeFirstTick covers the arm-to-first-poll window
// (#787): `cenci babysit` writes its state file before the supervisor loop
// starts polling, so a lazyboards cleanup firing between "supervisor armed"
// and "first tick completed" can already see that a supervisor owns the PR.
//
// #924 AC 13 extends this to the eager save's own CIStatus/clock/
// ClosingIssues handling: a state with no prior verdict fills ciStatusUnknown
// and starts the settle clock (this is the no-parent, `--once`/detached-child
// arm path -- there is no parent-resolved verdict to inherit yet), but an
// already-recorded verdict (a re-arm over an existing state file) must never
// be downgraded, and an existing ClosingIssues join key must survive
// untouched either way (Q&A 5).
func TestRunWritesStateBeforeFirstTick(t *testing.T) {
	for _, tc := range []struct {
		name             string
		seed             *State
		wantCIStatus     string
		wantClockSet     bool
		wantClosingIssue int
	}{
		{
			name:         "no prior state fills unknown and starts the clock",
			wantCIStatus: ciStatusUnknown,
			wantClockSet: true,
		},
		{
			name:         "existing green verdict is never downgraded",
			seed:         &State{PR: "42", Repo: "o/r", Agent: "claude", CIStatus: "green"},
			wantCIStatus: "green",
			wantClockSet: false,
		},
		{
			name:             "existing ClosingIssues and unknown verdict survive the eager save",
			seed:             &State{PR: "42", Repo: "o/r", Agent: "claude", CIStatus: ciStatusUnknown, ClosingIssues: []int{782}, ChecksAbsentSince: time.Now().UTC().Add(-time.Minute)},
			wantCIStatus:     ciStatusUnknown,
			wantClockSet:     true,
			wantClosingIssue: 782,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.seed != nil {
				if err := save(statePath(dir, "o/r", "42"), *tc.seed); err != nil {
					t.Fatal(err)
				}
			}
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
			if atFirstTick.CIStatus != tc.wantCIStatus {
				t.Errorf("state at first tick has CIStatus %q, want %q", atFirstTick.CIStatus, tc.wantCIStatus)
			}
			if tc.wantClockSet && atFirstTick.ChecksAbsentSince.IsZero() {
				t.Error("state at first tick has a zero ChecksAbsentSince, want the eager save to start/keep the settle clock")
			}
			if !tc.wantClockSet && !atFirstTick.ChecksAbsentSince.IsZero() {
				t.Error("state at first tick has a non-zero ChecksAbsentSince, want no clock started when downgrading is refused")
			}
			if tc.wantClosingIssue != 0 {
				if !reflect.DeepEqual(atFirstTick.ClosingIssues, []int{tc.wantClosingIssue}) {
					t.Errorf("state at first tick has ClosingIssues = %v, want [%d] preserved from the seeded state", atFirstTick.ClosingIssues, tc.wantClosingIssue)
				}
			}
		})
	}
}

// -- detaching parent's pre-Start write (#924) -------------------------------
//
// `cenci babysit` returns as soon as it has detached its child, so the
// child's own eager save gives Phase 9 no happens-before edge (D3). The
// detaching parent must resolve the PR's closing issues with one gh pr view
// and persist the guard-visible state -- ClosingIssues, a blocking
// ciStatusUnknown verdict, and its settle clock -- *before* cmd.Start()
// runs. Every test here drives Run's real forking-parent branch (isolated
// per Q&A 20 from the ambient CENCI_SANDBOX/CENCI_BABYSIT_SUPERVISOR env in
// this dev container, mirroring launch_test.go:288-294) and asserts from
// inside the startSupervisor seam callback -- the one point guaranteed to
// run only after the parent's own save.

// TestRunParentArmWritesJoinKeyBeforeStart pins the ordering contract itself:
// by the time startSupervisor is invoked, the state file on disk already
// carries the join key and a blocking verdict.
func TestRunParentArmWritesJoinKeyBeforeStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")

	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
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
			return prWithIssue, "", nil
		}
		return "", "", nil
	}
	originalCurrentTmuxSession := currentTmuxSession
	currentTmuxSession = func() (string, error) { return "host-session", nil }
	var atStart *State
	originalStartSupervisor := startSupervisor
	startSupervisor = func(cmd *exec.Cmd) error {
		s := load(statePath(dir, "o/r", "42"))
		atStart = &s
		return nil
	}
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		currentTmuxSession = originalCurrentTmuxSession
		startSupervisor = originalStartSupervisor
	})

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: time.Minute}); err != nil {
		t.Fatalf("Run (arm): %v", err)
	}
	if atStart == nil {
		t.Fatal("startSupervisor was never invoked")
	}
	if !reflect.DeepEqual(atStart.ClosingIssues, []int{782}) {
		t.Errorf("state before Start has ClosingIssues = %v, want [782]", atStart.ClosingIssues)
	}
	if atStart.CIStatus != ciStatusUnknown {
		t.Errorf("state before Start has CIStatus = %q, want ciStatusUnknown %q", atStart.CIStatus, ciStatusUnknown)
	}
	if atStart.ChecksAbsentSince.IsZero() {
		t.Error("state before Start has a zero ChecksAbsentSince, want the parent to start the settle clock")
	}
	if atStart.Status != "arming" {
		t.Errorf("state before Start has Status = %q, want %q", atStart.Status, "arming")
	}
	if atStart.PID != 0 {
		t.Errorf("state before Start has PID = %d, want 0 (only the child fills its own pid)", atStart.PID)
	}
}

// TestRunParentArmGhPrViewFailureIsNonFatal pins AC 9: a failed parent-side
// gh pr view must not fail the arm itself -- Run still succeeds and still
// reaches startSupervisor. It also pins finding 4 from the Phase 6+7 review
// of #924: the failure must not be silent -- mirroring resolveLaunchTarget's
// own best-effort stderr convention, one line must be written to stderr so an
// operator has some signal during the window where ClosingIssues/CIStatus
// stay unpublished until the detached child's own first tick succeeds.
func TestRunParentArmGhPrViewFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")

	originalCommand := command
	command = func(name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte("/repo/root\n"), nil
		}
		return []byte(""), nil
	}
	originalExecGh := execGh
	execGh = func(args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "repo" {
			return "o/r\n", "", nil
		}
		return "", "", errors.New("exit status 1")
	}
	originalCurrentTmuxSession := currentTmuxSession
	currentTmuxSession = func() (string, error) { return "host-session", nil }
	startCalled := false
	originalStartSupervisor := startSupervisor
	startSupervisor = func(cmd *exec.Cmd) error {
		startCalled = true
		return nil
	}
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		currentTmuxSession = originalCurrentTmuxSession
		startSupervisor = originalStartSupervisor
	})

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	originalStderr := os.Stderr
	os.Stderr = w
	runErr := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: time.Minute})
	_ = w.Close()
	os.Stderr = originalStderr
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stderr: %v", readErr)
	}

	if runErr != nil {
		t.Fatalf("Run (arm): %v, want a failed parent-side gh pr view to be non-fatal", runErr)
	}
	if !startCalled {
		t.Fatal("startSupervisor was never invoked; a failed gh pr view must not abort the arm")
	}
	if !strings.Contains(string(out), "could not resolve closing issues") {
		t.Errorf("captured stderr = %q, want a message reporting the failed parent-side gh pr view", string(out))
	}
}

// TestRunParentArmRemovesFreshStateFileOnStartFailure pins the cleanup half
// of the parent's pre-Start write: when the parent creates a brand-new state
// file (no prior supervisor for this PR) and cmd.Start() then fails, the
// parent-created file must be removed -- a failed arm must not leave behind
// a phantom "supervisor owns this ticket" record the close guard would
// wrongly honor.
func TestRunParentArmRemovesFreshStateFileOnStartFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")

	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
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
			return prWithIssue, "", nil
		}
		return "", "", nil
	}
	originalCurrentTmuxSession := currentTmuxSession
	currentTmuxSession = func() (string, error) { return "host-session", nil }
	originalStartSupervisor := startSupervisor
	startSupervisor = func(cmd *exec.Cmd) error {
		return errors.New("fork/exec failed")
	}
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		currentTmuxSession = originalCurrentTmuxSession
		startSupervisor = originalStartSupervisor
	})

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: time.Minute}); err == nil {
		t.Fatal("Run (arm): err = nil, want the stubbed Start failure to surface")
	}
	if _, err := os.Stat(statePath(dir, "o/r", "42")); !os.IsNotExist(err) {
		t.Fatalf("state file still exists after a failed Start on a fresh arm: %v", err)
	}
}

// TestRunParentArmPreservesExistingStateFileOnStartFailure is the sibling of
// the above: a re-arm over an *existing* supervisor state file must not wipe
// it out on a failed Start -- only a state file the parent itself created
// fresh is removed, and the pre-existing verdict is never downgraded.
func TestRunParentArmPreservesExistingStateFileOnStartFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")

	path := statePath(dir, "o/r", "42")
	if err := save(path, State{PR: "42", Repo: "o/r", Agent: "claude", CIStatus: "green"}); err != nil {
		t.Fatal(err)
	}

	prWithIssue := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
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
			return prWithIssue, "", nil
		}
		return "", "", nil
	}
	originalCurrentTmuxSession := currentTmuxSession
	currentTmuxSession = func() (string, error) { return "host-session", nil }
	originalStartSupervisor := startSupervisor
	startSupervisor = func(cmd *exec.Cmd) error {
		return errors.New("fork/exec failed")
	}
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		currentTmuxSession = originalCurrentTmuxSession
		startSupervisor = originalStartSupervisor
	})

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: time.Minute}); err == nil {
		t.Fatal("Run (arm): err = nil, want the stubbed Start failure to surface")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pre-existing state file was removed after a failed re-arm Start: %v", err)
	}
	s := load(path)
	if s.CIStatus != "green" {
		t.Errorf("CIStatus = %q after a failed re-arm, want the pre-existing green verdict preserved, not downgraded to unknown", s.CIStatus)
	}
}

// TestRunParentArmRestartsClockForInheritedUnknownVerdict pins finding 1 from
// the Phase 6+7 review of #924: the parent must call noteChecksUnknown
// whenever the verdict it publishes is ciStatusUnknown, whether freshly
// filled in or inherited as-is from the loaded state (Q&A 16) -- not only
// when CIStatus was previously empty. A re-arm over an existing state file
// already carrying ciStatusUnknown, a stale/past-grace ChecksAbsentSince, and
// an old ChecksAbsentHeadSHA must restart the clock against the freshly-read
// HeadRefOID, not leave it stale/elapsed.
func TestRunParentArmRestartsClockForInheritedUnknownVerdict(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")

	path := statePath(dir, "o/r", "42")
	staleSince := time.Now().UTC().Add(-(checksSettleGrace + time.Hour))
	if err := save(path, State{
		PR:                  "42",
		Repo:                "o/r",
		Agent:               "claude",
		CIStatus:            ciStatusUnknown,
		ChecksAbsentSince:   staleSince,
		ChecksAbsentHeadSHA: "old-sha",
	}); err != nil {
		t.Fatal(err)
	}

	prNewHead := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"new-sha","url":"https://example/pr/42","closingIssuesReferences":[{"number":782}]}`
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
			return prNewHead, "", nil
		}
		return "", "", nil
	}
	originalCurrentTmuxSession := currentTmuxSession
	currentTmuxSession = func() (string, error) { return "host-session", nil }
	originalStartSupervisor := startSupervisor
	startSupervisor = func(cmd *exec.Cmd) error { return nil }
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		currentTmuxSession = originalCurrentTmuxSession
		startSupervisor = originalStartSupervisor
	})

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: time.Minute}); err != nil {
		t.Fatalf("Run (re-arm): %v", err)
	}

	s := load(path)
	if s.CIStatus != ciStatusUnknown {
		t.Fatalf("CIStatus = %q after re-arm, want %q preserved", s.CIStatus, ciStatusUnknown)
	}
	if s.ChecksAbsentHeadSHA != "new-sha" {
		t.Errorf("ChecksAbsentHeadSHA = %q after re-arm, want the freshly-read head SHA %q", s.ChecksAbsentHeadSHA, "new-sha")
	}
	if !s.ChecksAbsentSince.After(staleSince) {
		t.Errorf("ChecksAbsentSince = %v, want the clock restarted (after the stale %v), not left stale/elapsed", s.ChecksAbsentSince, staleSince)
	}
}

// TestRunParentArmDoesNotRemoveConcurrentlyArmedStateFileOnStartFailure pins
// finding 3 from the Phase 6+7 review of #924: `preexisting` is computed from
// an os.Stat taken before this parent's own save/Start, so a concurrent
// `cenci babysit` for the same PR can race past that stat, save its own
// arming write, and have its child start successfully before this parent's
// own Start fails. The failed-arm cleanup must not unconditionally remove
// the file in that case -- it must re-load it and only remove it if it still
// looks like this parent's own untouched write (Status "arming", PID 0),
// leaving a state some other process has since taken over in place.
func TestRunParentArmDoesNotRemoveConcurrentlyArmedStateFileOnStartFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")

	path := statePath(dir, "o/r", "42")
	// No pre-existing state file at this parent's own stat: preexisting ==
	// false for this arm.

	originalCommand := command
	command = func(name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte("/repo/root\n"), nil
		}
		return []byte(""), nil
	}
	originalExecGh := execGh
	execGh = func(args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "repo" {
			return "o/r\n", "", nil
		}
		return "", "", errors.New("exit status 1")
	}
	originalCurrentTmuxSession := currentTmuxSession
	currentTmuxSession = func() (string, error) { return "host-session", nil }
	originalStartSupervisor := startSupervisor
	startSupervisor = func(cmd *exec.Cmd) error {
		// Simulate a concurrent arm for the same PR that raced past this
		// parent's own preexisting stat, saved its own arming write, and
		// then had its child start successfully (a nonzero PID) -- all
		// before this parent's own Start returns its failure below.
		if err := save(path, State{PR: "42", Repo: "o/r", Agent: "claude", Status: "running", PID: 99999}); err != nil {
			t.Fatal(err)
		}
		return errors.New("fork/exec failed")
	}
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		currentTmuxSession = originalCurrentTmuxSession
		startSupervisor = originalStartSupervisor
	})

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: dir, Interval: time.Minute}); err == nil {
		t.Fatal("Run (arm): err = nil, want the stubbed Start failure to surface")
	}
	s := load(path)
	if s.PID != 99999 || s.Status != "running" {
		t.Fatalf("state file after failed Start = %+v, want the concurrently-armed live state (PID 99999, Status running) left in place, not removed/clobbered", s)
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

// stubProcessGone replaces the signal-0 "is this pid gone" probe for the
// duration of a test (#1490). Tests that stub processOwned to false with a
// made-up pid stub this to false too, so they keep exercising the
// live-but-not-ours refusal instead of depending on whether that pid exists.
func stubProcessGone(t *testing.T, gone bool) {
	t.Helper()
	original := processGone
	processGone = func(int) bool { return gone }
	t.Cleanup(func() { processGone = original })
}

func TestBlocksCloseMatrix(t *testing.T) {
	live := State{PR: "790", RepoRoot: "/repo/root", ClosingIssues: []int{782}, CIStatus: "failing", PID: 4242, Status: "running"}
	now := time.Now().UTC()
	insideGrace := now.Add(-2 * time.Minute)
	pastGrace := now.Add(-(checksSettleGrace + time.Minute))
	futureDated := now.Add(time.Hour)
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
		{"green CI with a fresh clock still allows (green is unconditional)", func() State { s := live; s.CIStatus = "green"; s.ChecksAbsentSince = insideGrace; return s }(), true, "782", "/repo/root", false},

		// #924 D2/AC 14: zero checks ("") is now default-deny -- it splits
		// into a fresh-clock row that blocks and a legacy-state row (no clock
		// ever recorded) that still allows, per the missing/zero ⇒
		// already-elapsed audit rule (Q&A 15, kept verbatim per Q1).
		{"zero checks with a fresh clock blocks (AC 14 inversion)", func() State { s := live; s.CIStatus = ""; s.ChecksAbsentSince = insideGrace; return s }(), true, "782", "/repo/root", true},
		{"zero checks with no clock (legacy state) allows", func() State { s := live; s.CIStatus = ""; return s }(), true, "782", "/repo/root", false},
		{"zero checks with a future-dated clock allows (anomalous timestamp audit)", func() State { s := live; s.CIStatus = ""; s.ChecksAbsentSince = futureDated; return s }(), true, "782", "/repo/root", false},
		{"zero checks past the settle grace allows", func() State { s := live; s.CIStatus = ""; s.ChecksAbsentSince = pastGrace; return s }(), true, "782", "/repo/root", false},

		// #923/#924 Q1: a genuine gh read failure writes ciStatusUnknown and
		// must gain its own fresh settle clock to keep blocking -- otherwise
		// the missing-clock allow rule above would silently give it a
		// zero-second hold, reverting #923.
		{"unknown CI (read failure) with a fresh clock blocks", func() State { s := live; s.CIStatus = ciStatusUnknown; s.ChecksAbsentSince = insideGrace; return s }(), true, "782", "/repo/root", true},
		{"unknown CI (read failure) past the settle grace allows", func() State { s := live; s.CIStatus = ciStatusUnknown; s.ChecksAbsentSince = pastGrace; return s }(), true, "782", "/repo/root", false},
		{"unknown CI (read failure) with no clock (legacy state) allows", func() State { s := live; s.CIStatus = ciStatusUnknown; return s }(), true, "782", "/repo/root", false},
		{"unknown CI (read failure) with a future-dated clock allows", func() State { s := live; s.CIStatus = ciStatusUnknown; s.ChecksAbsentSince = futureDated; return s }(), true, "782", "/repo/root", false},

		// Unrecognized verdicts fall under the same default-deny + grace rule
		// as unknown/"" -- never a silent catch-all allow (watch/AGENTS.md's
		// Critical Rule against broadening a match-miss exclusion).
		{"unrecognized CI value with a fresh clock blocks", func() State { s := live; s.CIStatus = "weird-value"; s.ChecksAbsentSince = insideGrace; return s }(), true, "782", "/repo/root", true},
		{"unrecognized CI value with no clock allows", func() State { s := live; s.CIStatus = "weird-value"; return s }(), true, "782", "/repo/root", false},
		{"unrecognized CI value past the settle grace allows", func() State { s := live; s.CIStatus = "weird-value"; s.ChecksAbsentSince = pastGrace; return s }(), true, "782", "/repo/root", false},

		{"dead supervisor allows", live, false, "782", "/repo/root", false},
		{"paused supervisor with no pid blocks", func() State { s := live; s.PID = 0; s.Status = "needs-input"; return s }(), false, "782", "/repo/root", true},
		// #924: arming (PID 0, Status "arming") must count as live, so a
		// re-armed supervisor's inherited-blocking verdict still blocks the
		// close before the child's own first tick runs -- as long as the
		// arm-time UpdatedAt is still within armingLivenessGrace.
		{"arming supervisor with PID 0 blocks", func() State { s := live; s.PID = 0; s.Status = "arming"; s.UpdatedAt = now; return s }(), false, "782", "/repo/root", true},
		// #924 (finding 2): an orphaned arm -- the child crashed/exited before
		// its own first save -- must not hold the guard forever; once
		// UpdatedAt is older than armingLivenessGrace, arming no longer
		// counts as live and the inherited verdict's own settle-grace rule
		// applies (here CIStatus "failing" blocks unconditionally once
		// non-live, matching supervisorLive's fall-through unrelated to
		// arming).
		{"stale arming supervisor with PID 0 is no longer live", func() State {
			s := live
			s.PID = 0
			s.Status = "arming"
			s.UpdatedAt = now.Add(-(armingLivenessGrace + time.Minute))
			return s
		}(), false, "782", "/repo/root", false},
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
// from babysit's own repair session (ci-repair or babysit-review) is still
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
				if len(c) > 2 && c[1] == "run" && c[2] == "babysit-review" {
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
		if len(c) > 3 && c[1] == "run" && c[2] == "babysit-review" {
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
		if len(c) > 3 && c[1] == "run" && c[2] == "babysit-review" {
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
		if len(c) > 3 && c[1] == "run" && c[2] == "babysit-review" {
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
		if out, err, ok := ghPathAnswer(args); ok {
			return out, "", err
		}
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
	// #975: a stubbed tmuxHasSession is required so the review
	// launch attempt below reaches the command() self-exec (and fails
	// there, as intended) rather than short-circuiting on the recorded-
	// session gate before command() is ever called.
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
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

// -- #1300: BlocksCloseStrict, the fail-closed variant for worktree reclaim --

// blockingGuardState is a live, failing-CI supervisor of PR 790 that closes
// ticket 782 -- it blocks under stubProcessOwned(t, true).
func blockingGuardState() State {
	return State{PR: "790", RepoRoot: "/repo/root", ClosingIssues: []int{782}, CIStatus: "failing", PID: 4242, Status: "running"}
}

// skipAsRoot skips a test that relies on file mode 000 stopping a read: root
// ignores permission bits.
func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions, so mode 000 does not make anything unreadable")
	}
}

// writeStrictFile writes name in dir with the given content and mode, and
// restores a readable mode on cleanup so t.TempDir can remove it.
func writeStrictFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	return path
}

// stateFailure is one way the babysit state cannot be read; setup builds it and
// returns the state-dir override to pass ("" = the standard location) plus the
// text an error naming the cause must contain.
type stateFailure struct {
	name   string
	root   bool // runs as root too; false relies on mode 000 and is skipped as root
	setup  func(t *testing.T) (override, wantInErr string)
	wantAC string
}

func stateFailures() []stateFailure {
	return []stateFailure{
		{name: "the state directory cannot be resolved", root: true, wantAC: "a", setup: func(t *testing.T) (string, string) {
			t.Setenv("HOME", "")
			t.Setenv("XDG_STATE_HOME", "")
			return "", ""
		}},
		{name: "the state directory cannot be listed", root: false, wantAC: "b", setup: func(t *testing.T) (string, string) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			writeGuardState(t, dir, blockingGuardState())
			if err := os.Chmod(dir, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
			return dir, dir
		}},
		{name: "a state file cannot be read", root: false, wantAC: "c", setup: func(t *testing.T) (string, string) {
			dir := t.TempDir()
			return dir, writeStrictFile(t, dir, "locked.json", "{}", 0)
		}},
		{name: "a state entry is a directory", root: true, wantAC: "c", setup: func(t *testing.T) (string, string) {
			dir := t.TempDir()
			path := filepath.Join(dir, "x.json")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			return dir, path
		}},
		{name: "a state file is corrupt JSON", root: true, wantAC: "d", setup: func(t *testing.T) (string, string) {
			dir := t.TempDir()
			return dir, writeStrictFile(t, dir, "corrupt.json", "{not json", 0600)
		}},
	}
}

// Each way the state cannot be read is an error with nothing blocked -- the
// caller must keep the worktree rather than read "not armed".
func TestBlocksCloseStrict_ReportsEveryStateReadFailure(t *testing.T) {
	for _, f := range stateFailures() {
		t.Run(f.name, func(t *testing.T) {
			if !f.root {
				skipAsRoot(t)
			}
			stubProcessOwned(t, true)
			override, wantInErr := f.setup(t)

			blocked, reason, err := BlocksCloseStrict("782", "/repo/root", override)

			if err == nil {
				t.Fatalf("BlocksCloseStrict = %v, %q, nil; want an error (AC %s)", blocked, reason, f.wantAC)
			}
			if blocked || reason != "" {
				t.Errorf("BlocksCloseStrict = %v, %q; want nothing blocked when the state cannot be read", blocked, reason)
			}
			if wantInErr != "" && !strings.Contains(err.Error(), wantInErr) {
				t.Errorf("error %q does not name %q, so the user cannot find what to fix", err, wantInErr)
			}
		})
	}
}

// BlocksClose keeps its fail-open contract for every one of those failures: it
// is what `cenci close` and the daemon rely on never to block on an error.
func TestBlocksClose_StaysFailOpenOnEveryStateReadFailure(t *testing.T) {
	for _, f := range stateFailures() {
		t.Run(f.name, func(t *testing.T) {
			if !f.root {
				skipAsRoot(t)
			}
			stubProcessOwned(t, true)
			override, _ := f.setup(t)

			blocked, reason := BlocksClose("782", "/repo/root", override)

			if blocked || reason != "" {
				t.Errorf("BlocksClose = %v, %q; want (false, \"\") on a state read failure", blocked, reason)
			}
		})
	}
}

func TestBlocksCloseStrict_MissingStateDirectoryMeansNothingIsArmed(t *testing.T) {
	stubProcessOwned(t, true)

	blocked, reason, err := BlocksCloseStrict("782", "/repo/root", filepath.Join(t.TempDir(), "never-created"))

	if blocked || reason != "" || err != nil {
		t.Errorf("BlocksCloseStrict = %v, %q, %v; want (false, \"\", nil)", blocked, reason, err)
	}
}

func TestBlocksCloseStrict_NonNumericTicketIsAnError(t *testing.T) {
	stubProcessOwned(t, true)
	dir := t.TempDir()
	writeGuardState(t, dir, blockingGuardState())

	blocked, _, err := BlocksCloseStrict("add-dark-mode", "/repo/root", dir)

	if err == nil || blocked {
		t.Errorf("BlocksCloseStrict = %v, %v; want an error and nothing blocked", blocked, err)
	}
}

// With every entry readable it answers exactly as BlocksClose does.
func TestBlocksCloseStrict_HealthyStateAnswersWithoutAnError(t *testing.T) {
	stubProcessOwned(t, true)
	dir := t.TempDir()
	writeGuardState(t, dir, blockingGuardState())

	blocked, reason, err := BlocksCloseStrict("782", "/repo/root", dir)
	if err != nil || !blocked || !strings.Contains(reason, "#790") {
		t.Errorf("BlocksCloseStrict(782) = %v, %q, %v; want (true, a reason naming #790, nil)", blocked, reason, err)
	}
	blocked, reason, err = BlocksCloseStrict("999", "/repo/root", dir)
	if err != nil || blocked || reason != "" {
		t.Errorf("BlocksCloseStrict(999) = %v, %q, %v; want (false, \"\", nil)", blocked, reason, err)
	}
}

// A supervisor that blocks the ticket wins over an unreadable or corrupt entry
// beside it, wherever in the sorted listing the bad entry falls.
func TestBlocksCloseStrict_ABlockingMatchOutranksReadErrors(t *testing.T) {
	tests := []struct {
		name  string
		root  bool
		plant func(t *testing.T, dir string)
	}{
		{name: "corrupt entries on both sides", root: true, plant: func(t *testing.T, dir string) {
			writeStrictFile(t, dir, "aaa-corrupt.json", "{not json", 0600)
			writeStrictFile(t, dir, "zzz-corrupt.json", "{not json", 0600)
		}},
		{name: "directory entries on both sides", root: true, plant: func(t *testing.T, dir string) {
			for _, name := range []string{"aaa.json", "zzz.json"} {
				if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "unreadable entries on both sides", root: false, plant: func(t *testing.T, dir string) {
			writeStrictFile(t, dir, "aaa-locked.json", "{}", 0)
			writeStrictFile(t, dir, "zzz-locked.json", "{}", 0)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.root {
				skipAsRoot(t)
			}
			stubProcessOwned(t, true)
			dir := t.TempDir()
			tt.plant(t, dir)
			writeGuardState(t, dir, blockingGuardState())

			blocked, reason, err := BlocksCloseStrict("782", "/repo/root", dir)

			if err != nil || !blocked || !strings.Contains(reason, "#790") {
				t.Errorf("BlocksCloseStrict = %v, %q, %v; want (true, a reason naming #790, nil)", blocked, reason, err)
			}
		})
	}
}

// Files that are not state, or that vanished mid-pass, are not read errors.
func TestBlocksCloseStrict_IgnoresEntriesThatAreNotState(t *testing.T) {
	tests := []struct {
		name  string
		plant func(t *testing.T, dir string)
	}{
		{name: "supervisor log and temp siblings", plant: func(t *testing.T, dir string) {
			writeStrictFile(t, dir, "aabbcc-790.log", "supervisor output\n", 0600)
			writeStrictFile(t, dir, "state-790.json.tmp", "{half written", 0600)
			writeStrictFile(t, dir, "state-790.json.lock", "4242\n", 0600)
		}},
		// A supervisor Stop removes its state file between the listing and the
		// read; a dangling symlink is that moment held still.
		{name: "a state file deleted between listing and read", plant: func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(dir, "removed-by-stop"), filepath.Join(dir, "gone.json")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubProcessOwned(t, true)
			dir := t.TempDir()
			tt.plant(t, dir)

			blocked, reason, err := BlocksCloseStrict("782", "/repo/root", dir)
			if blocked || reason != "" || err != nil {
				t.Errorf("alone: BlocksCloseStrict = %v, %q, %v; want (false, \"\", nil)", blocked, reason, err)
			}

			writeGuardState(t, dir, blockingGuardState())
			blocked, reason, err = BlocksCloseStrict("782", "/repo/root", dir)
			if err != nil || !blocked || !strings.Contains(reason, "#790") {
				t.Errorf("beside a blocking state: BlocksCloseStrict = %v, %q, %v; want (true, a reason naming #790, nil)", blocked, reason, err)
			}
		})
	}
}

// Distinguish a new merge-repair dispatch from a repeated observation.
const (
	conflictWindowOpenedMarker = "opened merge-repair"
	conflictStillOpenMarker    = "repair already dispatched"
)

// conflictingOpenPR is an OPEN PR fixture with mergeStateStatus DIRTY --
// #995's escalation-trigger predicate. mergeable CONFLICTING is paired with
// it the way real GitHub state reports it; headRefOid is parameterized so
// the dedup/re-arm tests below can control the head-SHA comparison
// explicitly.
func conflictingOpenPR(headSHA string) string {
	return fmt.Sprintf(`{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":%q,"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY","url":"https://example/pr/42","closingIssuesReferences":[]}`, headSHA)
}

// cleanOpenPR is an OPEN PR fixture with mergeStateStatus CLEAN (not
// conflicting) -- #995's conflict-cleared release case needs a PR whose
// conflict has resolved since the previous tick.
func cleanOpenPR(headSHA string) string {
	return fmt.Sprintf(`{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":%q,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","url":"https://example/pr/42","closingIssuesReferences":[]}`, headSHA)
}

// countWorkflowLaunches counts how many recorded calls are a `cenci run
// <workflow>` launch -- generalizes countAddressReviewLaunches (#885) to any
// workflow name, for #995's conflict-escalation tests below.
func countWorkflowLaunches(calls [][]string, workflow string) int {
	n := 0
	for _, c := range calls {
		if len(c) > 2 && c[1] == "run" && c[2] == workflow {
			n++
		}
	}
	return n
}

// TestPrConflictingTable pins prConflicting's fail-safe OR predicate (#995's
// Assumptions): MergeStateStatus == "DIRTY" is authoritative on its own;
// Mergeable == "CONFLICTING" only falls back to conflicting when
// MergeStateStatus is empty or "UNKNOWN" -- a narrow fail-safe, per
// watch/AGENTS.md's rule against broadening a match-miss into a silent
// catch-all. Any other non-empty, recognized status (e.g. BLOCKED) is
// authoritative and NOT conflicting -- pinned by its own regression case, as
// that rule requires.
func TestPrConflictingTable(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mergeable        string
		mergeStateStatus string
		want             bool
	}{
		{"DIRTY is conflicting regardless of mergeable", "CONFLICTING", "DIRTY", true},
		{"CONFLICTING with empty mergeStateStatus falls back to conflicting", "CONFLICTING", "", true},
		{"CONFLICTING with mergeStateStatus UNKNOWN falls back to conflicting", "CONFLICTING", "UNKNOWN", true},
		{"CONFLICTING with mergeStateStatus BLOCKED is NOT conflicting (regression: a recognized non-DIRTY status is authoritative)", "CONFLICTING", "BLOCKED", false},
		{"MERGEABLE with empty mergeStateStatus is not conflicting", "MERGEABLE", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr := prView{Mergeable: tc.mergeable, MergeStateStatus: tc.mergeStateStatus}
			if got := prConflicting(pr); got != tc.want {
				t.Fatalf("prConflicting(mergeable=%q, mergeStateStatus=%q) = %v, want %v", tc.mergeable, tc.mergeStateStatus, got, tc.want)
			}
		})
	}
}

// Green CI does not hide a conflict, even with automerge disabled.
func TestTickConflictingDIRTYLaunchesMergeRepairOnce(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("abc"), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900, LastHeadSHA: "abc", LaunchSession: "work"}

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	originalStdout := os.Stdout
	os.Stdout = w
	terminal, delay, err := tick(&s)
	_ = w.Close()
	os.Stdout = originalStdout
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}

	if err != nil || terminal {
		t.Fatalf("tick = terminal %v, err %v, want terminal false, err nil", terminal, err)
	}
	if delay != 300*time.Second || s.CurrentDelaySeconds != 300 {
		t.Fatalf("delay = %v, CurrentDelaySeconds = %d, want IntervalSeconds (300s): a conflicting tick must stay actionable, never back off", delay, s.CurrentDelaySeconds)
	}
	if s.Status != "running" {
		t.Fatalf("Status = %q, want running", s.Status)
	}
	if s.ConflictRepairSHA != "abc" {
		t.Fatalf("ConflictRepairSHA = %q, want abc", s.ConflictRepairSHA)
	}
	if n := countWorkflowLaunches(calls, "merge-repair"); n != 1 {
		t.Fatalf("merge-repair launches = %d, want exactly 1: %#v", n, calls)
	}
	stdout := string(out)
	if !strings.Contains(stdout, conflictWindowOpenedMarker) {
		t.Fatalf("tick stdout = %q, want it to contain %q", stdout, conflictWindowOpenedMarker)
	}
	if strings.Contains(stdout, conflictStillOpenMarker) {
		t.Fatalf("tick stdout = %q, want it to NOT contain the still-conflicting line %q on a first notification", stdout, conflictStillOpenMarker)
	}
	if strings.Contains(stdout, "quiet — no new actionable work") {
		t.Fatalf("tick stdout = %q, want no 'quiet' backoff line on a conflicting-but-actionable tick", stdout)
	}
}

// Repeated observations retain the normal polling interval without launching again.
func TestTickConflictSameSHAStillConflictingOpensNoSecondWindow(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("abc"), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	stubPanes(t, panesAlways(paneRowOf("42-merge-repair", false, "/repo/root")))
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900,
		LastHeadSHA: "abc", LaunchSession: "work", LaunchDir: "/repo/root",
		Status: "running", ConflictRepairSHA: "abc", ConflictFixAttempts: 1,
	}

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	originalStdout := os.Stdout
	os.Stdout = w
	_, delay, err := tick(&s)
	_ = w.Close()
	os.Stdout = originalStdout
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}

	if err != nil {
		t.Fatal(err)
	}
	if delay != 300*time.Second || s.CurrentDelaySeconds != 300 {
		t.Fatalf("delay = %v, CurrentDelaySeconds = %d, want IntervalSeconds (300s): a still-conflicting tick must stay actionable", delay, s.CurrentDelaySeconds)
	}
	if n := countWorkflowLaunches(calls, "merge-repair"); n != 0 {
		t.Fatalf("merge-repair launches = %d, want 0: the same head commit must not re-open a window: %#v", n, calls)
	}
	if s.ConflictRepairSHA != "abc" || s.ConflictFixAttempts != 1 {
		t.Fatalf("dedup consumed retry budget: %+v", s)
	}
	stdout := string(out)
	if !strings.Contains(stdout, conflictStillOpenMarker) {
		t.Fatalf("tick stdout = %q, want it to contain %q", stdout, conflictStillOpenMarker)
	}
	if strings.Contains(stdout, conflictWindowOpenedMarker) {
		t.Fatalf("tick stdout = %q, want it to NOT contain the window-opened line %q: no second window opened this tick", stdout, conflictWindowOpenedMarker)
	}
	if strings.Contains(stdout, "quiet — no new actionable work") {
		t.Fatalf("tick stdout = %q, want no 'quiet' backoff line", stdout)
	}
}

// A new conflicting head can launch another repair within the episode budget.
func TestTickConflictNewHeadSHAReArmsWindow(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("def"), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	stubPanes(t, panesAlways(paneRowOf("42-merge-repair", true, "/repo/root")))
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900,
		LastHeadSHA: "abc", LaunchSession: "work",
		Status: "running", ConflictRepairSHA: "abc", ConflictFixAttempts: 1,
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if n := countWorkflowLaunches(calls, "merge-repair"); n != 1 {
		t.Fatalf("merge-repair launches = %d, want exactly 1: a new head commit must re-arm the window: %#v", n, calls)
	}
	if s.ConflictRepairSHA != "def" || s.ConflictFixAttempts != 2 {
		t.Fatalf("new-head repair did not advance bookkeeping: %+v", s)
	}
	if s.Status != "running" {
		t.Fatalf("Status = %q, want running", s.Status)
	}
}

// TestTickConflictClearedResetsStatusAndConflictNotifiedSHA is #995's
// Implementation Order step 4 release case: once a later tick observes the
// conflict cleared, Status resets to "running" and ConflictNotifiedSHA
// clears (watch/AGENTS.md #1079's bounded-hold convention) -- and
// supervisorLive stops treating the PID-0 supervisor as live once that
// reset lands, so BlocksClose no longer holds `cenci close` open on its
// behalf.
func TestTickConflictClearedResetsStatusAndConflictNotifiedSHA(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{cleanOpenPR("def"), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900,
		LastHeadSHA: "def", LaunchSession: "work",
		Status: "needs-input", ConflictNotifiedSHA: "def", PID: 0,
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.Status != "running" {
		t.Fatalf("Status = %q, want %q: the conflict-cleared reset must land", s.Status, "running")
	}
	if s.ConflictNotifiedSHA != "" {
		t.Fatalf("ConflictNotifiedSHA = %q, want empty: cleared on release", s.ConflictNotifiedSHA)
	}
	if n := countWorkflowLaunches(calls, "babysit-attention"); n != 0 {
		t.Fatalf("babysit-attention launches = %d, want 0: a MERGEABLE PR must not escalate: %#v", n, calls)
	}
	if supervisorLive(s) {
		t.Fatal("supervisorLive = true, want false: the reset must stop holding cenci close open for a PID-0, non-arming, non-needs-input supervisor")
	}
}

// Merge repair takes precedence without consuming or resetting the CI budget.
func TestTickConflictPrecedesCIRepairAndRetryCapOnNewSHA(t *testing.T) {
	for _, tc := range []struct {
		name            string
		startFixAttempt int
		wantFixAttempts int
	}{
		{"below the retry cap", 0, 0},
		{"already at the retry cap", fixCap, fixCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			withCommands(t, []string{conflictingOpenPR("new-sha"), `[{"bucket":"fail","name":"test","state":"FAILURE"}]`, `[]`, `[]`}, &calls)
			s := State{
				PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900,
				LastHeadSHA: "old-sha", LaunchSession: "work",
				FixAttempts: tc.startFixAttempt,
			}
			_, _, err := tick(&s)
			if err != nil {
				t.Fatalf("tick: %v, want the conflict path to never return an error, even at the retry cap", err)
			}
			if n := countWorkflowLaunches(calls, "merge-repair"); n != 1 {
				t.Fatalf("merge-repair launches = %d, want exactly 1: %#v", n, calls)
			}
			if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
				t.Fatalf("ci-repair launches = %d, want 0: a conflicting tick must never launch ci-repair: %#v", n, calls)
			}
			if s.FixAttempts != tc.wantFixAttempts {
				t.Fatalf("FixAttempts = %d, want unchanged %d", s.FixAttempts, tc.wantFixAttempts)
			}
			if s.RepairPending {
				t.Fatal("RepairPending = true without a CI repair dispatch")
			}
			if s.LastHeadSHA != "old-sha" {
				t.Fatalf("LastHeadSHA = %q, want old-sha", s.LastHeadSHA)
			}
			if s.LastRepairedSHA != "" {
				t.Fatalf("LastRepairedSHA = %q without a CI repair dispatch", s.LastRepairedSHA)
			}
			if s.ConflictRepairSHA != "new-sha" {
				t.Fatalf("ConflictRepairSHA = %q, want new-sha", s.ConflictRepairSHA)
			}
			if s.Status != "running" {
				t.Fatalf("Status = %q, want running", s.Status)
			}
		})
	}
}

// Review feedback waits so two repair workflows do not write the same branch.
func TestTickConflictDefersAddressReviewLaunch(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{
		conflictingOpenPR("abc"),
		`[{"bucket":"pass","name":"test","state":"SUCCESS"}]`,
		`[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`,
		`[]`,
	}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900, LastHeadSHA: "abc", LaunchSession: "work"}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if n := countWorkflowLaunches(calls, "merge-repair"); n != 1 {
		t.Fatalf("merge-repair launches = %d, want exactly 1: %#v", n, calls)
	}
	if n := countAddressReviewLaunches(calls); n != 0 {
		t.Fatalf("address-review launches = %d, want zero concurrent branch writers: %#v", n, calls)
	}
}

// A failed launch remains visible and can retry without spending the repair budget.
func TestTickConflictLaunchFailureRecordsWorkflowLaunchFailedAndRetries(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	pr := conflictingOpenPR("abc")
	responses := []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`}
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
	originalTmuxHasSession := tmuxHasSession
	tmuxHasSession = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
		tmuxHasSession = originalTmuxHasSession
	})

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900, LastHeadSHA: "abc", LaunchSession: "work"}
	_, _, err := tick(&s)
	if err == nil {
		t.Fatal("tick: err = nil, want the conflict-path launch failure to surface as a tick error")
	}
	if s.CIRepairPaused || s.Status == "needs-input" {
		t.Fatalf("a failed conflict-path launch paused the supervisor: CIRepairPaused=%v Status=%q, want the conflict path to never pause it (Decision)", s.CIRepairPaused, s.Status)
	}
	if s.ConflictRepairSHA != "" || s.ConflictFixAttempts != 0 {
		t.Fatalf("failed launch consumed repair bookkeeping: %+v", s)
	}
	if s.AutomergeReason != reasonWorkflowLaunchFailed {
		t.Fatalf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonWorkflowLaunchFailed)
	}

	// Next tick: command() now succeeds, so the retry launches successfully.
	var calls2 [][]string
	withCommands(t, []string{pr, `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls2)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if n := countWorkflowLaunches(calls2, "merge-repair"); n != 1 {
		t.Fatalf("retry tick: merge-repair launches = %d, want exactly 1: %#v", n, calls2)
	}
	if s.ConflictRepairSHA != "abc" || s.ConflictFixAttempts != 1 {
		t.Fatalf("successful retry repair bookkeeping: %+v", s)
	}
}

// -- #1390: host `Stop` output pin (P34) ------------------------------------

// hostStopFixture isolates a host-side `Stop`/`Status` call: CENCI_SANDBOX
// cleared (watch/docs/test-isolation.md -- the dev sandbox sets it ambiently),
// the gh repo lookup answered with repo, and a temp state directory. It
// returns the directory and the state file path for (repo, pr).
func hostStopFixture(t *testing.T, repo, pr string) (dir, path string) {
	t.Helper()
	t.Setenv("CENCI_SANDBOX", "")
	original := execGh
	execGh = func(args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "repo" {
			return repo + "\n", "", nil
		}
		return "", "", fmt.Errorf("unexpected gh call: %v", args)
	}
	t.Cleanup(func() { execGh = original })
	dir = t.TempDir()
	return dir, statePath(dir, repo, pr)
}

func writeStateFile(t *testing.T, path string, s State) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStop_HostOutputAndErrorsUnchanged pins `cenci babysit stop <pr>` run on
// the host byte for byte (#1390 P34) before Stop is split into a repo-keyed
// StopRepo plus a wrapper: the success line, the no-supervisor error, and the
// refusal to signal an unverified pid.
func TestStop_HostOutputAndErrorsUnchanged(t *testing.T) {
	t.Run("stopped prints the host message and keeps only the feedback bookkeeping", func(t *testing.T) {
		dir, path := hostStopFixture(t, "o/r", "42")
		writeStateFile(t, path, State{PR: "42", Repo: "o/r", Status: "running"})

		done := captureStdout(t)
		err := Stop("42", dir)
		out := done()
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if out != "Stopped babysitting PR #42.\n" {
			t.Errorf("stdout = %q, want %q", out, "Stopped babysitting PR #42.\n")
		}
		if got := mustReadState(t, path); got.Status != "stopped" || got.PID != 0 {
			t.Errorf("state after Stop = status %q pid %d, want a stopped state with no pid", got.Status, got.PID)
		}
	})

	t.Run("missing state keeps the no supervisor text", func(t *testing.T) {
		dir, _ := hostStopFixture(t, "o/r", "42")
		err := Stop("42", dir)
		if err == nil || err.Error() != "no supervisor found for PR #42" {
			t.Fatalf("Stop err = %v, want exactly %q", err, "no supervisor found for PR #42")
		}
	})

	t.Run("a corrupt state is still treated as empty: removed and reported stopped", func(t *testing.T) {
		dir, path := hostStopFixture(t, "o/r", "42")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}

		done := captureStdout(t)
		err := Stop("42", dir)
		out := done()
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if out != "Stopped babysitting PR #42.\n" {
			t.Errorf("stdout = %q, want %q", out, "Stopped babysitting PR #42.\n")
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("corrupt state file still exists after host Stop (stat err = %v)", statErr)
		}
	})

	t.Run("an unverified pid keeps the refusal text and the state", func(t *testing.T) {
		dir, path := hostStopFixture(t, "o/r", "42")
		stubProcessOwned(t, false)
		stubProcessGone(t, false)
		writeStateFile(t, path, State{PR: "42", Repo: "o/r", PID: 4242, Status: "running"})

		err := Stop("42", dir)
		const want = "refusing to signal pid 4242: it is not the recorded cenci babysit process"
		if err == nil || err.Error() != want {
			t.Fatalf("Stop err = %v, want exactly %q", err, want)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("state file must survive a refused stop, stat err = %v", statErr)
		}
	})
}

// -- #1390: repo-keyed StopRepo / StatusRepo ---------------------------------

// TestStopRepo_ClassifiesFailuresBySentinel pins the sentinels the daemon
// maps to fixed reasons (watch/docs/error-handling.md: a new sentinel gets a
// direct errors.Is test at the package boundary) and that the host text of
// each class is unchanged.
func TestStopRepo_ClassifiesFailuresBySentinel(t *testing.T) {
	t.Run("missing state is ErrNoSupervisor", func(t *testing.T) {
		dir := t.TempDir()
		err := StopRepo("o/r", "42", dir)
		if !errors.Is(err, ErrNoSupervisor) {
			t.Fatalf("err = %v, want errors.Is(err, ErrNoSupervisor)", err)
		}
		if err.Error() != "no supervisor found for PR #42" {
			t.Errorf("err text = %q", err.Error())
		}
	})

	t.Run("an unverified pid is ErrSupervisorUnverified and is never signalled", func(t *testing.T) {
		dir := t.TempDir()
		stubProcessOwned(t, false)
		stubProcessGone(t, false)
		writeStateFile(t, statePath(dir, "o/r", "42"), State{PR: "42", Repo: "o/r", PID: 4242})
		err := StopRepo("o/r", "42", dir)
		if !errors.Is(err, ErrSupervisorUnverified) {
			t.Fatalf("err = %v, want errors.Is(err, ErrSupervisorUnverified)", err)
		}
		if errors.Is(err, ErrStopFailed) {
			t.Error("an unverified pid must not also classify as ErrStopFailed")
		}
	})

	t.Run("a failed signal is ErrStopFailed and keeps the state", func(t *testing.T) {
		dir := t.TempDir()
		stubProcessOwned(t, true)
		originalWait := resetExitWait
		resetExitWait = 50 * time.Millisecond
		t.Cleanup(func() { resetExitWait = originalWait })
		const gonePID = 2147483000 // above any real pid_max: the supervisor never exits
		path := statePath(dir, "o/r", "42")
		writeStateFile(t, path, State{PR: "42", Repo: "o/r", PID: gonePID})
		err := StopRepo("o/r", "42", dir)
		if !errors.Is(err, ErrStopFailed) {
			t.Fatalf("err = %v, want errors.Is(err, ErrStopFailed)", err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("state file must survive a failed signal, stat err = %v", statErr)
		}
	})

	t.Run("a failed state removal is ErrStopFailed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("a read-only directory does not stop root from removing the state")
		}
		dir := t.TempDir()
		path := statePath(dir, "o/r", "42")
		writeStateFile(t, path, State{PR: "42", Repo: "o/r", Status: "running"})
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		err := StopRepo("o/r", "42", dir)
		if !errors.Is(err, ErrStopFailed) {
			t.Fatalf("err = %v, want errors.Is(err, ErrStopFailed)", err)
		}
	})

	t.Run("a corrupt state is ErrStateUnreadable, is kept and names no path", func(t *testing.T) {
		dir := t.TempDir()
		path := statePath(dir, "o/r", "42")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := StopRepo("o/r", "42", dir)
		if !errors.Is(err, ErrStateUnreadable) {
			t.Fatalf("err = %v, want errors.Is(err, ErrStateUnreadable)", err)
		}
		if errors.Is(err, ErrNoSupervisor) {
			t.Error("a corrupt state must not classify as ErrNoSupervisor")
		}
		if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), ".json") {
			t.Errorf("err text %q names the state path", err.Error())
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("state file must survive an unreadable-state stop, stat err = %v", statErr)
		}
	})

	t.Run("an unreadable state file is ErrStateUnreadable, not ErrNoSupervisor", func(t *testing.T) {
		dir := t.TempDir()
		path := statePath(dir, "o/r", "42")
		if err := os.MkdirAll(path, 0o700); err != nil { // a directory where the file belongs
			t.Fatal(err)
		}
		err := StopRepo("o/r", "42", dir)
		if !errors.Is(err, ErrStateUnreadable) {
			t.Fatalf("err = %v, want errors.Is(err, ErrStateUnreadable)", err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("state must survive, stat err = %v", statErr)
		}
	})

	t.Run("a stopped supervisor keeps a stopped feedback-only state and returns nil", func(t *testing.T) {
		dir := t.TempDir()
		path := statePath(dir, "o/r", "42")
		writeStateFile(t, path, State{PR: "42", Repo: "o/r", Status: "running"})
		if err := StopRepo("o/r", "#42", dir); err != nil {
			t.Fatalf("StopRepo: %v", err)
		}
		if got := mustReadState(t, path); got.Status != "stopped" {
			t.Errorf("Status = %q, want stopped", got.Status)
		}
	})

	// #1490: the daemon-forwarded stop inherits the gone path, but it prints
	// nothing: the host-only stderr note belongs to the host Stop.
	t.Run("a dead recorded pid returns nil, keeps a stopped state, removes the lock and prints nothing", func(t *testing.T) {
		dir := t.TempDir()
		path := statePath(dir, "o/r", "42")
		lock := path + ".lock"
		dead := deadPID(t)
		writeStateFile(t, path, ciPausedLiveState(dead))
		writeLock(t, lock, dead)

		doneErr := captureStderr(t)
		doneOut := captureStdout(t)
		err := StopRepo("o/r", "42", dir)
		out, errOut := doneOut(), doneErr()

		if err != nil {
			t.Fatalf("StopRepo: %v", err)
		}
		if got := mustReadState(t, path); got.Status != "stopped" || got.PID != 0 {
			t.Errorf("state = status %q pid %d, want stopped with no pid", got.Status, got.PID)
		}
		requireMissing(t, lock, "lock file")
		if out != "" || errOut != "" {
			t.Errorf("StopRepo wrote stdout %q stderr %q, want nothing", out, errOut)
		}
	})

	t.Run("a dead recorded pid with a lock naming a live supervisor is ErrSupervisorUnverified and changes nothing", func(t *testing.T) {
		dir := t.TempDir()
		path := statePath(dir, "o/r", "42")
		lock := path + ".lock"
		owner := startOwnedFixture(t, false)
		writeStateFile(t, path, ciPausedLiveState(deadPID(t)))
		writeLock(t, lock, owner.pid)
		stateBefore, lockBefore := readFileBytes(t, path), readFileBytes(t, lock)

		err := StopRepo("o/r", "42", dir)

		if !errors.Is(err, ErrSupervisorUnverified) {
			t.Fatalf("err = %v, want errors.Is(err, ErrSupervisorUnverified)", err)
		}
		if errors.Is(err, ErrStopFailed) {
			t.Error("a live-supervisor lock refusal must not also classify as ErrStopFailed")
		}
		if want := fmt.Sprintf("supervisor already running for PR #42 (%d)", owner.pid); err.Error() != want {
			t.Errorf("err text = %q, want exactly %q", err.Error(), want)
		}
		requireStillRunning(t, owner)
		if !bytes.Equal(readFileBytes(t, path), stateBefore) || !bytes.Equal(readFileBytes(t, lock), lockBefore) {
			t.Error("state file or lock changed")
		}
	})
}

// TestStatusRepo_ReportsPersistedStateAndLiveness covers P24: liveness is
// "has a verified pid", not supervisorLive's broader needs-input/arming rule.
func TestStatusRepo_ReportsPersistedStateAndLiveness(t *testing.T) {
	const repo = "o/r"
	for _, tc := range []struct {
		name      string
		state     State
		owned     bool
		wantLive  bool
		wantState string
	}{
		{"pid 0 needs-input is not live", State{PR: "42", Status: "needs-input"}, true, false, "needs-input"},
		{"pid 0 arming is not live", State{PR: "42", Status: "arming"}, true, false, "arming"},
		{"owned pid is live", State{PR: "42", PID: 4242, Status: "running"}, true, true, "running"},
		{"unowned pid is not live", State{PR: "42", PID: 4242, Status: "running"}, false, false, "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stubProcessOwned(t, tc.owned)
			tc.state.FixAttempts = 2
			tc.state.ConflictFixAttempts = 1
			writeStateFile(t, statePath(dir, repo, "42"), tc.state)

			got, err := StatusRepo(repo, "42", dir)
			if err != nil {
				t.Fatalf("StatusRepo: %v", err)
			}
			want := ipc.BabysitStatus{PR: 42, Repo: repo, Status: tc.wantState, CIFixAttempts: 2, ConflictFixAttempts: 1, FixCap: fixCap, SupervisorLive: tc.wantLive}
			if got != want {
				t.Fatalf("StatusRepo = %+v, want %+v", got, want)
			}
		})
	}
}

func TestStatusRepo_MissingStateIsErrNoSupervisor(t *testing.T) {
	_, err := StatusRepo("o/r", "42", t.TempDir())
	if !errors.Is(err, ErrNoSupervisor) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNoSupervisor)", err)
	}
	if err.Error() != "no supervisor found for PR #42" {
		t.Errorf("err text = %q", err.Error())
	}
}

// TestStatusRepo_CorruptStateIsPathFree covers P4/P33: a corrupt or
// unreadable state yields a fixed message that never names the state path.
func TestStatusRepo_CorruptStateIsPathFree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(statePath(dir, "o/r", "42"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := StatusRepo("o/r", "42", dir)
	if !errors.Is(err, ErrStateUnreadable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrStateUnreadable)", err)
	}
	const want = "babysit state for PR #42 could not be read"
	if err.Error() != want {
		t.Fatalf("err text = %q, want exactly %q", err.Error(), want)
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("err text %q leaks the state directory", err.Error())
	}
}

// -- #1390 P9: attention launch reasons are plain labels ---------------------

func launchArg(calls [][]string, workflow string) (string, bool) {
	for _, c := range calls {
		if len(c) > 3 && c[1] == "run" && c[2] == workflow {
			return c[3], true
		}
	}
	return "", false
}

func TestTick_CIRetryCapLaunchReasonIsAPlainLabel(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{cleanOpenPR("sha-x"), `[{"bucket":"fail","name":"test","state":"FAILURE"}]`, `[]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work", FixAttempts: fixCap}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick err = %v, want nil: the cap pauses CI repair without stopping the tick", err)
	}
	arg, ok := launchArg(calls, "babysit-attention")
	if !ok {
		t.Fatalf("no babysit-attention launch: %#v", calls)
	}
	if arg != "42 CI retry cap reached" {
		t.Errorf("launch arg = %q, want %q", arg, "42 CI retry cap reached")
	}
}

func TestTick_MergeRepairCapLaunchReasonIsAPlainLabel(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("def"), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
	s := State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900,
		LastHeadSHA: "abc", LaunchSession: "work", Status: "running", ConflictRepairSHA: "abc", ConflictFixAttempts: fixCap,
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	arg, ok := launchArg(calls, "babysit-attention")
	if !ok {
		t.Fatalf("no babysit-attention launch: %#v", calls)
	}
	if arg != "42 merge repair retry cap reached" {
		t.Errorf("launch arg = %q, want %q", arg, "42 merge repair retry cap reached")
	}
}

// TestPackageHasNoDecideWhetherToRetryWording scans every Go file in the
// package (tests included) so the old launch-reason tail cannot survive in a
// comment, a constant or a fixture. The needle is assembled so this file does
// not match itself.
func TestPackageHasNoDecideWhetherToRetryWording(t *testing.T) {
	needle := "decide whether " + "to retry"
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob *.go: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), needle) {
			t.Errorf("%s still contains %q", f, needle)
		}
	}
}

// -- #1455: finite ci-repair workers -----------------------------------------

// ciRepairTickResponses scripts one failing-checks tick at headSHA (pr view,
// checks, comments, reviews).
func ciRepairTickResponses(headSHA string) []string {
	return []string{cleanOpenPR(headSHA), `[{"bucket":"fail","name":"test","state":"FAILURE"}]`, `[]`, `[]`}
}

// ciRepairSeedState is a supervisor with one repair already dispatched at an
// older head, so a dispatch, a refusal and a suppression are all observable on
// the four repair fields.
func ciRepairSeedState() State {
	return State{
		PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 900,
		LastHeadSHA: "sha-old", LastRepairedSHA: "sha-old", FixAttempts: 1, RepairPending: true,
		LaunchSession: "work", LaunchDir: "/repo/root",
	}
}

// assertCIRepairFieldsUnchanged compares the four ci-repair bookkeeping fields.
func assertCIRepairFieldsUnchanged(t *testing.T, got, want State) {
	t.Helper()
	if got.FixAttempts != want.FixAttempts || got.RepairPending != want.RepairPending ||
		got.LastHeadSHA != want.LastHeadSHA || got.LastRepairedSHA != want.LastRepairedSHA {
		t.Errorf("repair fields = {FixAttempts:%d RepairPending:%v LastHeadSHA:%q LastRepairedSHA:%q}, want unchanged {%d %v %q %q}",
			got.FixAttempts, got.RepairPending, got.LastHeadSHA, got.LastRepairedSHA,
			want.FixAttempts, want.RepairPending, want.LastHeadSHA, want.LastRepairedSHA)
	}
}

// A live `<PR>-ci-repair` worker in the recorded session suppresses another
// launch: no `cenci run`, no attempt consumed, and the tick stays actionable so
// the supervisor keeps polling at its interval.
func TestTickCIRepairLiveWorkerSuppressesDispatch(t *testing.T) {
	var calls [][]string
	withCommands(t, ciRepairTickResponses("sha-new"), &calls)
	reads := stubPanes(t, panesAlways(liveCI()))
	s := ciRepairSeedState()
	before := s

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 while 42-ci-repair is live: %#v", n, calls)
	}
	if !reflect.DeepEqual(*reads, []string{"|work"}) {
		t.Errorf("inventory reads %v, want exactly one of the recorded session [|work]", *reads)
	}
	assertCIRepairFieldsUnchanged(t, s, before)
	if s.CurrentDelaySeconds != s.IntervalSeconds {
		t.Errorf("CurrentDelaySeconds = %d, want %d: a live repair keeps the tick actionable", s.CurrentDelaySeconds, s.IntervalSeconds)
	}
}

// An unreadable pane inventory fails closed: no launch, no attempt consumed,
// and the tick records reasonWorkflowLaunchFailed with the probe error.
func TestTickCIRepairProbeErrorRefusesDispatch(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, ciRepairTickResponses("sha-new"), &calls)
	stubPanes(t, func(int) (string, error) { return "42-ci-repair\t0\n", nil })
	s := ciRepairSeedState()
	before := s

	_, _, err := tick(&s)
	if err == nil || !strings.Contains(err.Error(), "malformed pane inventory") {
		t.Fatalf("tick err = %v, want the probe failure", err)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 on an unreadable inventory: %#v", n, calls)
	}
	assertCIRepairFieldsUnchanged(t, s, before)
	if s.AutomergeReason != reasonWorkflowLaunchFailed {
		t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonWorkflowLaunchFailed)
	}
}

// With no recorded session the probe is never consulted; launch() reports its
// own "no tmux session was recorded" refusal and nothing is consumed.
func TestTickCIRepairNoSessionSkipsProbe(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, ciRepairTickResponses("sha-new"), &calls)
	reads := stubPanes(t, panesAlways(noWorkerInventory))
	s := ciRepairSeedState()
	s.LaunchSession = ""
	before := s

	_, _, err := tick(&s)
	if err == nil || !strings.Contains(err.Error(), "no tmux session was recorded") {
		t.Fatalf("tick err = %v, want launch()'s no-session refusal", err)
	}
	assertCIRepairFieldsUnchanged(t, s, before)
	if s.AutomergeReason != reasonWorkflowLaunchFailed {
		t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonWorkflowLaunchFailed)
	}
	if len(*reads) != 0 {
		t.Errorf("inventory reads = %v, want none without a recorded session", *reads)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Errorf("cenci run ci-repair calls = %d, want 0", n)
	}
}

// stubCenciRunExit makes the `cenci run` self-exec exit with code (a real
// *exec.ExitError), printing msg; every other command succeeds. Calls are
// recorded like withCommands does.
func stubCenciRunExit(t *testing.T, calls *[][]string, code int, msg string) {
	t.Helper()
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if len(args) > 0 && args[0] == "run" {
			return exec.Command("sh", "-c", `printf '%s\n' "$1"; exit "$2"`, "sh", msg, fmt.Sprint(code)).CombinedOutput()
		}
		return []byte(""), nil
	}
	t.Cleanup(func() { command = original })
}

// `cenci run` exit 3 means the window was spawned but its post-spawn setup
// failed: babysit counts the attempt as dispatched, and the next tick at the
// same head, with that window's pane live, starts no second ci-repair window.
func TestTickCIRepairWindowSpawnedCountsAsDispatched(t *testing.T) {
	var calls [][]string
	withCommands(t, append(ciRepairTickResponses("sha-new"), ciRepairTickResponses("sha-new")...), &calls)
	stubCenciRunExit(t, &calls, 3, "cenci run: setting automatic-rename off on \"work:42-ci-repair\": boom: tmux window already spawned")
	stubPanes(t, panesByTick("", liveCI()))
	s := ciRepairSeedState()

	restore := captureStdout(t)
	_, _, err := tick(&s)
	stdout := restore()
	if err != nil {
		t.Fatalf("tick 1: %v, want a spawned window counted as dispatched", err)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("tick 1: ci-repair launches = %d, want 1: %#v", n, calls)
	}
	if s.FixAttempts != 2 || !s.RepairPending || s.LastRepairedSHA != "sha-new" {
		t.Fatalf("tick 1: FixAttempts/RepairPending/LastRepairedSHA = %d/%v/%q, want 2/true/sha-new", s.FixAttempts, s.RepairPending, s.LastRepairedSHA)
	}
	if !strings.Contains(stdout, "warning") || !strings.Contains(stdout, "automatic-rename") {
		t.Errorf("stdout = %q, want a warning carrying cenci run's post-spawn failure", stdout)
	}

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("tick 2 at the same head: ci-repair launches = %d total, want still 1: %#v", n, calls)
	}
	if s.FixAttempts != 2 {
		t.Errorf("tick 2: FixAttempts = %d, want unchanged 2", s.FixAttempts)
	}
}

// Any other `cenci run` failure is still a pre-spawn refusal: nothing consumed.
func TestTickCIRepairOrdinaryLaunchFailureConsumesNothing(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, ciRepairTickResponses("sha-new"), &calls)
	stubCenciRunExit(t, &calls, 1, "cenci run: codex adapter unavailable")
	s := ciRepairSeedState()
	before := s

	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want the exit-1 launch failure")
	}
	assertCIRepairFieldsUnchanged(t, s, before)
	if s.AutomergeReason != reasonWorkflowLaunchFailed {
		t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonWorkflowLaunchFailed)
	}
}

// Exit 3 counts as dispatched for ci-repair and, since #1472, babysit-review
// (the nonce is saved and the keys are launched); a babysit-attention launch
// that exits 3 is still a failed launch.
func TestTickWindowSpawnedIsStillAFailureOutsideTheFiniteWorkers(t *testing.T) {
	var calls [][]string
	withCommands(t, ciRepairTickResponses("sha-new"), &calls)
	stubCenciRunExit(t, &calls, 3, "cenci run: tmux window already spawned")
	s := ciRepairSeedState()
	s.FixAttempts = fixCap

	if _, _, err := tick(&s); err == nil {
		t.Fatal("tick: err = nil, want a babysit-attention exit 3 to stay a launch failure")
	}
	if s.CICapNotified {
		t.Errorf("CICapNotified = true, want false after a failed babysit-attention launch")
	}
}

// The retry-cap path launches the interactive babysit-attention with its
// unchanged argument; since #1456 it pauses only CI repair, so the tick itself
// succeeds. Since #1457 it waits while the last worker is still live: that
// tick opens nothing, and the first tick after the pane ends opens it.
func TestTickCIRetryCapLaunchesAttentionUnchanged(t *testing.T) {
	var calls [][]string
	withCommands(t, append(ciRepairTickResponses("sha-new"), ciRepairTickResponses("sha-new")...), &calls)
	stubPanes(t, panesByTick(liveCI(), deadCI()))
	s := ciRepairSeedState()
	s.FixAttempts = fixCap

	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if n := countWorkflowLaunches(calls, "babysit-attention"); n != 0 || s.CIRepairPaused || s.CICapNotified {
		t.Fatalf("tick 1 with a live worker: attention launches=%d CIRepairPaused=%v CICapNotified=%v, want 0/false/false", n, s.CIRepairPaused, s.CICapNotified)
	}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick 2 err = %v, want nil: the cap pauses CI repair, not the supervisor", err)
	}
	if !s.CIRepairPaused || !s.CICapNotified {
		t.Errorf("CIRepairPaused/CICapNotified = %v/%v, want true/true", s.CIRepairPaused, s.CICapNotified)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0 at the cap", n)
	}
	arg, ok := launchArg(calls, "babysit-attention")
	if !ok || arg != "42 CI retry cap reached" {
		t.Errorf("babysit-attention arg = %q (found %v), want %q", arg, ok, "42 CI retry cap reached")
	}
}

// -- #1472: ordinary PR discussion, receipts and the finite babysit-review worker --

const (
	humanBody  = "please also cover the empty input"
	maintainer = "maintainer"
	humanLogin = "alice"
)

// humanComment renders a never-edited human discussion comment; every comment
// shares one created_at/updated_at pair on purpose (equal timestamps).
func humanComment(id int64, body string) string {
	return humanCommentBy(id, humanLogin, body)
}

// humanCommentBy is humanComment authored by login.
func humanCommentBy(id int64, login, body string) string {
	return discussionJSON(id, login, body, "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z")
}

// reviewState is a supervisor for PR 42 with a recorded session and directory.
func reviewState() State {
	return State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900, LaunchSession: "work", LaunchDir: ownedRepoDir}
}

// outstandingReviewState is reviewState with discussion:11 launched to a
// babysit-review worker (nonce testReviewNonce) and no receipt yet.
func outstandingReviewState() State {
	s := reviewState()
	s.PendingKeys = []string{"discussion:11"}
	s.LaunchedKeys = []string{"discussion:11"}
	s.DiscussionRevisions = map[string]string{"discussion:11": bodyRevision(humanBody)}
	s.ReviewLaunch = &reviewLaunchRecord{Nonce: testReviewNonce, LaunchedAt: testLaunchAt, Keys: []string{"discussion:11"}, TrustedKeys: []string{"discussion:11"}}
	return s
}

// serveReceipt serves discussion:11 and a receipt for it with outcome, posted by
// a maintainer who has write permission.
func serveReceipt(t *testing.T, outcome string) {
	t.Helper()
	servePermission(t, maintainer, "write")
	body := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 11, Revision: bodyRevision(humanBody), Outcome: outcome})
	serveDiscussion(t, humanComment(11, humanBody), discussionJSON(12, maintainer, body, afterLaunch, afterLaunch))
}

// automergeOnResponses scripts one tick on an eligible PR whose automerge chain
// stops at the review stage: pr view, green checks, comments, reviews, labels.
func automergeOnResponses() []string {
	return []string{automergeEligiblePR(), ciCapGreen, `[]`, `[]`, `{"labels":[{"name":"automerge:ok"}]}`}
}

func graphqlCalls(calls [][]string) int {
	n := 0
	for _, c := range calls {
		if len(c) > 2 && c[0] == "gh" && c[1] == "api" && c[2] == "graphql" {
			n++
		}
	}
	return n
}

func liveReview() string { return paneRowOf("42-babysit-review", false, ownedRepoDir) }

// tickOK runs one tick and fails the test on an error.
func tickOK(t *testing.T, s *State) {
	t.Helper()
	if _, _, err := tick(s); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// AC: a human PR conversation comment becomes a pending discussion key bound to
// its body revision; automerge holds on it; exactly one babysit-review launch
// carries the nonce and the revision; a discussion-only state never reads
// review threads.
func TestTickDiscussionCommentBecomesPendingAndLaunchesBabysitReviewOnce(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, automergeOnResponses(), &calls)
	withReviewSeams(t, testLaunchAt, testReviewNonce)
	serveDiscussion(t, humanComment(11, humanBody))
	s := reviewState()

	tickOK(t, &s)

	rev := bodyRevision(humanBody)
	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || !reflect.DeepEqual(s.LaunchedKeys, []string{"discussion:11"}) {
		t.Fatalf("pending/launched = %v/%v, want [discussion:11] both", s.PendingKeys, s.LaunchedKeys)
	}
	if !reflect.DeepEqual(s.DiscussionRevisions, map[string]string{"discussion:11": rev}) {
		t.Errorf("DiscussionRevisions = %v, want discussion:11 at %s", s.DiscussionRevisions, rev)
	}
	if s.AutomergeReason != reasonReviewPending {
		t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonReviewPending)
	}
	if n := countWorkflowLaunches(calls, "babysit-review"); n != 1 {
		t.Fatalf("babysit-review launches = %d, want exactly 1: %#v", n, calls)
	}
	if arg, _ := launchArg(calls, "babysit-review"); arg != "42 --receipt-nonce "+testReviewNonce+" discussion:11="+rev {
		t.Errorf("launch argument = %q, want the pr, the nonce and discussion:11=<revision>", arg)
	}
	if n := countWorkflowLaunches(calls, "address-review"); n != 0 {
		t.Errorf("address-review launches = %d, want 0: the supervisor no longer launches the interactive workflow", n)
	}
	want := &reviewLaunchRecord{Nonce: testReviewNonce, LaunchedAt: testLaunchAt, Keys: []string{"discussion:11"}, TrustedKeys: []string{"discussion:11"}}
	if !reflect.DeepEqual(s.ReviewLaunch, want) {
		t.Errorf("ReviewLaunch = %+v, want %+v", s.ReviewLaunch, want)
	}
	if n := graphqlCalls(calls); n != 0 {
		t.Errorf("gh api graphql calls = %d, want 0 for a discussion-only state", n)
	}
}

// AC: an unreadable discussion read leaves every key byte-identical and records
// the named reason, never an empty discussion set.
func TestTickDiscussionUnreadableLeavesKeysUntouchedAndRecordsTheReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		err  error
	}{
		{"gh exits non-zero", "", errors.New("exit status 1")},
		{"page does not decode", "not json", nil},
		{"object without id", `[{"user":{"login":"a"},"body":"x"}]`, nil},
		{"object without user.login", `[{"id":5,"user":{},"body":"x"}]`, nil},
		{"object without body", `[{"id":5,"user":{"login":"a"}}]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFleetAutomergeEnabled(t, true)
			var calls [][]string
			withCommands(t, []string{openPR(), ciCapGreen, `[]`, `[]`}, &calls)
			serveDiscussionBody(t, tc.body, tc.err)
			s := outstandingReviewState()
			s.AddressedKeys = []string{"discussion:9"}
			before := s

			if _, _, err := tick(&s); err == nil {
				t.Fatal("tick: err = nil, want the unreadable discussion to fail the tick")
			}
			if !reflect.DeepEqual(s.PendingKeys, before.PendingKeys) || !reflect.DeepEqual(s.AddressedKeys, before.AddressedKeys) || !reflect.DeepEqual(s.LaunchedKeys, before.LaunchedKeys) {
				t.Errorf("keys changed on an unreadable read: pending %v addressed %v launched %v", s.PendingKeys, s.AddressedKeys, s.LaunchedKeys)
			}
			if s.AutomergeReason != reasonUpstreamDiscussionUnreadable {
				t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonUpstreamDiscussionUnreadable)
			}
			if n := countWorkflowLaunches(calls, "babysit-review"); n != 0 {
				t.Errorf("babysit-review launches = %d, want 0", n)
			}
		})
	}
}

// AC (Q&A 13): a discussion read the page cap cut short holds with its own
// reason, mutates no discussion state, consumes no receipt and launches nothing,
// while the comment:/review: reconcile pass runs unchanged.
func TestTickDiscussionTruncatedReadHoldsWithoutDiscussionMutation(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, []string{automergeEligiblePR(), ciCapGreen, `[]`, `[]`, resolvedThreadFor(7), `{"labels":[{"name":"automerge:ok"}]}`}, &calls)
	withReviewSeams(t, testLaunchAt, testWrongNonce)
	servePermission(t, maintainer, "write")
	receipt := testReceiptBody(testReviewNonce, testReceiptHead, receiptEntry{ID: 11, Revision: bodyRevision(humanBody), Outcome: "addressed"})
	page := []string{humanComment(11, humanBody), discussionJSON(12, maintainer, receipt, afterLaunch, afterLaunch)}
	for id := int64(100); len(page) < feedbackPageSize; id++ {
		page = append(page, humanComment(id, "filler"))
	}
	serveDiscussionBody(t, jsonArray(page...), nil)
	s := outstandingReviewState()
	s.PendingKeys = []string{"comment:7", "discussion:11"}
	s.LaunchedKeys = []string{"comment:7"}
	before := s

	tickOK(t, &s)

	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || !reflect.DeepEqual(s.AddressedKeys, []string{"comment:7"}) {
		t.Errorf("pending/addressed = %v/%v, want the resolved thread moved and discussion:11 untouched", s.PendingKeys, s.AddressedKeys)
	}
	if !reflect.DeepEqual(s.DiscussionRevisions, before.DiscussionRevisions) || !reflect.DeepEqual(s.ReviewLaunch, before.ReviewLaunch) || len(s.ConsumedReviewNonces) != 0 || len(s.DiscussionReceipts) != 0 {
		t.Errorf("discussion state changed on a truncated read: %+v", s)
	}
	if s.AutomergeReason != reasonDiscussionReadTruncated {
		t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonDiscussionReadTruncated)
	}
	if n := countWorkflowLaunches(calls, "babysit-review"); n != 0 {
		t.Errorf("babysit-review launches = %d, want 0 on a truncated read", n)
	}
}

// AC: a valid receipt addresses exactly the keys it lists with addressed or
// no-action; the state records the evidence and the nonce is consumed.
func TestTickDiscussionValidReceiptAddressesTheListedKey(t *testing.T) {
	for _, outcome := range []string{"addressed", "no-action"} {
		t.Run(outcome, func(t *testing.T) {
			var calls [][]string
			withCommands(t, []string{openPR(), ciCapGreen, `[]`, `[]`}, &calls)
			serveReceipt(t, outcome)
			s := outstandingReviewState()

			tickOK(t, &s)

			if len(s.PendingKeys) != 0 || !reflect.DeepEqual(s.AddressedKeys, []string{"discussion:11"}) || len(s.LaunchedKeys) != 0 {
				t.Fatalf("pending/addressed/launched = %v/%v/%v, want discussion:11 addressed and out of launched", s.PendingKeys, s.AddressedKeys, s.LaunchedKeys)
			}
			want := map[string]discussionReceipt{"discussion:11": {CommentID: 11, Revision: bodyRevision(humanBody), HeadSHA: testReceiptHead, Outcome: outcome, ReceiptCommentID: 12, Source: receiptSourceReceipt}}
			if !reflect.DeepEqual(s.DiscussionReceipts, want) {
				t.Errorf("DiscussionReceipts = %+v, want %+v", s.DiscussionReceipts, want)
			}
			if !reflect.DeepEqual(s.ConsumedReviewNonces, map[string]int64{testReviewNonce: 12}) {
				t.Errorf("ConsumedReviewNonces = %v, want the nonce consumed by comment 12", s.ConsumedReviewNonces)
			}
			if s.ReviewLaunch != nil {
				t.Errorf("ReviewLaunch = %+v, want cleared: consumed and the window has ended", s.ReviewLaunch)
			}
			if n := countWorkflowLaunches(calls, "babysit-review"); n != 0 {
				t.Errorf("babysit-review launches = %d, want 0 once addressed", n)
			}
		})
	}
}

// AC: blocked and failed keys stay pending and launched (no relaunch), the status
// is needs-input and the hold names `cenci babysit resolve`.
func TestTickDiscussionBlockedReceiptKeepsTheKeyPendingAndNeedsInput(t *testing.T) {
	for _, outcome := range []string{"blocked", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			withFleetAutomergeEnabled(t, true)
			var calls [][]string
			withCommands(t, append(automergeOnResponses(), automergeOnResponses()...), &calls)
			serveReceipt(t, outcome)
			s := outstandingReviewState()

			tickOK(t, &s)
			tickOK(t, &s)

			if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || !reflect.DeepEqual(s.LaunchedKeys, []string{"discussion:11"}) {
				t.Fatalf("pending/launched = %v/%v, want discussion:11 still both", s.PendingKeys, s.LaunchedKeys)
			}
			if s.Status != "needs-input" {
				t.Errorf("Status = %q, want needs-input recomputed every tick", s.Status)
			}
			if !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
				t.Errorf("AutomergeDetail = %q, want it to name `cenci babysit resolve 42 discussion:11`", s.AutomergeDetail)
			}
			if s.DiscussionReceipts["discussion:11"].Outcome != outcome {
				t.Errorf("recorded receipt = %+v, want outcome %s", s.DiscussionReceipts["discussion:11"], outcome)
			}
			if n := countWorkflowLaunches(calls, "babysit-review"); n != 0 {
				t.Errorf("babysit-review launches = %d, want 0: no relaunch for the same revision", n)
			}
		})
	}
}

// Edge case: a receipt posted while its pane is still live addresses its keys,
// but automerge stays held until the pane ends.
func TestTickDiscussionReceiptWhileThePaneIsLiveStillHoldsAutomerge(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, automergeOnResponses(), &calls)
	stubPanes(t, panesAlways(liveReview()))
	serveReceipt(t, "addressed")
	s := outstandingReviewState()

	tickOK(t, &s)

	if !reflect.DeepEqual(s.AddressedKeys, []string{"discussion:11"}) || len(s.PendingKeys) != 0 {
		t.Errorf("pending/addressed = %v/%v, want discussion:11 addressed", s.PendingKeys, s.AddressedKeys)
	}
	if s.AutomergeReason != reasonRepairWorkerLive {
		t.Errorf("AutomergeReason = %q, want %q while 42-babysit-review is live", s.AutomergeReason, reasonRepairWorkerLive)
	}
	if s.ReviewLaunch == nil {
		t.Error("ReviewLaunch cleared while the window is live, want it kept until the window ends")
	}
}

// AC/edge cases: a receipt with a wrong, spoofed or already-used nonce, or from an
// author without write permission, addresses nothing; each rejection is recorded
// in automergeDetail.
func TestTickDiscussionRejectedReceiptsAddressNothingAndAreRecorded(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, automergeOnResponses(), &calls)
	servePermission(t, maintainer, "write")
	entry := receiptEntry{ID: 11, Revision: bodyRevision(humanBody), Outcome: "addressed"}
	serveDiscussion(t,
		humanComment(11, humanBody),
		discussionJSON(21, maintainer, testReceiptBody(testWrongNonce, testReceiptHead, entry), afterLaunch, afterLaunch),
		discussionJSON(22, "stranger", testReceiptBody(testReviewNonce, testReceiptHead, entry), afterLaunch, afterLaunch),
		discussionJSON(23, maintainer, testReceiptBody("ffffffffffffffffffffffffffffffff", testReceiptHead, entry), afterLaunch, afterLaunch),
	)
	s := outstandingReviewState()
	s.ConsumedReviewNonces = map[string]int64{"ffffffffffffffffffffffffffffffff": 99}

	tickOK(t, &s)

	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || len(s.AddressedKeys) != 0 {
		t.Fatalf("pending/addressed = %v/%v, want nothing addressed", s.PendingKeys, s.AddressedKeys)
	}
	if got := s.ConsumedReviewNonces[testReviewNonce]; got != 0 {
		t.Errorf("outstanding nonce consumed by comment %d, want it left outstanding", got)
	}
	for _, rejection := range []string{
		receiptRejection{CommentID: 21, Reason: receiptRejectNonceUnknown}.String(),
		receiptRejection{CommentID: 22, Reason: receiptRejectNoWritePermission}.String(),
		receiptRejection{CommentID: 23, Reason: receiptRejectNonceConsumed}.String(),
	} {
		if !strings.Contains(s.AutomergeDetail, rejection) {
			t.Errorf("AutomergeDetail = %q, want the rejection %q", s.AutomergeDetail, rejection)
		}
	}
}

// AC: a window that ended without a valid receipt leaves its keys pending and
// launched (no relaunch for the same revision) and holds under its own reason,
// naming `cenci babysit resolve`.
func TestTickDiscussionEndedWithoutReceiptHoldsAndDoesNotRelaunch(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, append(automergeOnResponses(), automergeOnResponses()...), &calls)
	serveDiscussion(t, humanComment(11, humanBody))
	s := outstandingReviewState()

	tickOK(t, &s)
	tickOK(t, &s)

	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || !reflect.DeepEqual(s.LaunchedKeys, []string{"discussion:11"}) {
		t.Errorf("pending/launched = %v/%v, want discussion:11 kept in both", s.PendingKeys, s.LaunchedKeys)
	}
	if s.AutomergeReason != reasonReviewWorkerEndedWithoutReceipt {
		t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonReviewWorkerEndedWithoutReceipt)
	}
	if !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
		t.Errorf("AutomergeDetail = %q, want it to name `cenci babysit resolve 42 discussion:11`", s.AutomergeDetail)
	}
	if n := countWorkflowLaunches(calls, "babysit-review"); n != 0 {
		t.Errorf("babysit-review launches = %d, want 0: no relaunch for the same revision", n)
	}
}

// AC: an addressed key whose comment is edited (new body SHA-256) goes back to
// pending under `review feedback reopened`, and a new launch carries the new
// revision.
func TestTickDiscussionEditedAddressedCommentReopensAndRelaunches(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, automergeOnResponses(), &calls)
	withReviewSeams(t, testLaunchAt, testWrongNonce)
	const edited = "actually, handle the nil case too"
	serveDiscussion(t, humanComment(11, edited))
	s := reviewState()
	s.AddressedKeys = []string{"discussion:11"}
	s.DiscussionRevisions = map[string]string{"discussion:11": bodyRevision(humanBody)}
	s.DiscussionReceipts = map[string]discussionReceipt{"discussion:11": {CommentID: 11, Revision: bodyRevision(humanBody), Outcome: "addressed", ReceiptCommentID: 12, Source: receiptSourceReceipt}}

	tickOK(t, &s)

	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || len(s.AddressedKeys) != 0 {
		t.Fatalf("pending/addressed = %v/%v, want the edited key reopened", s.PendingKeys, s.AddressedKeys)
	}
	if s.AutomergeReason != reasonFeedbackReopened {
		t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonFeedbackReopened)
	}
	if arg, ok := launchArg(calls, "babysit-review"); !ok || !strings.HasSuffix(arg, "discussion:11="+bodyRevision(edited)) {
		t.Errorf("launch argument = %q (found %v), want discussion:11 at the edited revision", arg, ok)
	}
	if s.DiscussionRevisions["discussion:11"] != bodyRevision(edited) {
		t.Errorf("DiscussionRevisions = %v, want the edited revision recorded", s.DiscussionRevisions)
	}
}

// AC: an edit of a pending, already-launched key leaves LaunchedKeys, so a new
// episode launches (superseding the old nonce, recorded with comment id 0).
func TestTickDiscussionEditedLaunchedKeyRelaunchesAndSupersedesTheNonce(t *testing.T) {
	var calls [][]string
	withCommands(t, openPR2(), &calls)
	withReviewSeams(t, testLaunchAt.Add(time.Hour), testWrongNonce)
	const edited = "actually, handle the nil case too"
	serveDiscussion(t, humanComment(11, edited))
	s := outstandingReviewState()

	tickOK(t, &s)

	if arg, ok := launchArg(calls, "babysit-review"); !ok || arg != "42 --receipt-nonce "+testWrongNonce+" discussion:11="+bodyRevision(edited) {
		t.Fatalf("launch argument = %q (found %v), want a relaunch at the edited revision with the new nonce", arg, ok)
	}
	if s.ReviewLaunch == nil || s.ReviewLaunch.Nonce != testWrongNonce {
		t.Errorf("ReviewLaunch = %+v, want the new launch", s.ReviewLaunch)
	}
	if got, ok := s.ConsumedReviewNonces[testReviewNonce]; !ok || got != 0 {
		t.Errorf("ConsumedReviewNonces = %v, want the superseded nonce recorded with comment id 0", s.ConsumedReviewNonces)
	}
}

func openPR2() []string { return []string{openPR(), ciCapGreen, `[]`, `[]`} }

// AC: two comments with identical timestamps arriving on different ticks are both
// pending (a seen-set of ids, not a `>=` watermark).
func TestTickDiscussionEqualTimestampsOnDifferentTicksAreBothDetected(t *testing.T) {
	var calls [][]string
	withCommands(t, append(openPR2(), openPR2()...), &calls)
	serveDiscussion(t, humanComment(11, "first"))
	s := reviewState()
	tickOK(t, &s)
	serveDiscussion(t, humanComment(11, "first"), humanComment(12, "second"))

	tickOK(t, &s)

	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11", "discussion:12"}) {
		t.Errorf("PendingKeys = %v, want both discussion comments", s.PendingKeys)
	}
}

// Edge cases: a deleted pending key holds as unknown; a deleted addressed key is
// a no-op; bot comments and cenci-banner comments never become keys; a PR with no
// discussion is quiet and skips the pane probe.
func TestTickDiscussionDeletedExcludedAndQuietCases(t *testing.T) {
	t.Run("a deleted pending key holds as unknown", func(t *testing.T) {
		withFleetAutomergeEnabled(t, true)
		var calls [][]string
		withCommands(t, automergeOnResponses(), &calls)
		serveDiscussion(t)
		s := outstandingReviewState()
		tickOK(t, &s)
		if s.AutomergeReason != reasonReviewStateUnknown || !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) {
			t.Errorf("reason %q pending %v, want %q with the key still pending", s.AutomergeReason, s.PendingKeys, reasonReviewStateUnknown)
		}
	})
	t.Run("a deleted addressed key is a no-op", func(t *testing.T) {
		withFleetAutomergeEnabled(t, true)
		var calls [][]string
		withCommands(t, automergeOnResponses(), &calls)
		serveDiscussion(t)
		s := reviewState()
		s.AddressedKeys = []string{"discussion:11"}
		s.DiscussionRevisions = map[string]string{"discussion:11": bodyRevision(humanBody)}
		tickOK(t, &s)
		if !reflect.DeepEqual(s.AddressedKeys, []string{"discussion:11"}) || len(s.PendingKeys) != 0 || !hasCondition(s.AutomergeConditions, "review", true) {
			t.Errorf("addressed %v pending %v review-stage conditions %v, want no hold", s.AddressedKeys, s.PendingKeys, s.AutomergeConditions)
		}
	})
	t.Run("bot and banner comments never become keys", func(t *testing.T) {
		var calls [][]string
		withCommands(t, openPR2(), &calls)
		banner := "> 🤖 **cenci** — review reply posted by `/cenci:address-review` (posting replies).\n\nre-checking"
		serveDiscussion(t, discussionJSON(31, "dependabot[bot]", "bump", "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z"), discussionJSON(32, "alice", banner, "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z"))
		s := reviewState()
		tickOK(t, &s)
		if len(s.PendingKeys) != 0 || len(s.DiscussionRevisions) != 0 || countWorkflowLaunches(calls, "babysit-review") != 0 {
			t.Errorf("pending %v revisions %v launches %d, want a quiet tick", s.PendingKeys, s.DiscussionRevisions, countWorkflowLaunches(calls, "babysit-review"))
		}
	})
	t.Run("zero comments is quiet and never probes", func(t *testing.T) {
		var calls [][]string
		withCommands(t, openPR2(), &calls)
		reads := stubPanes(t, panesAlways(noWorkerInventory))
		s := reviewState()
		tickOK(t, &s)
		if len(*reads) != 0 || countWorkflowLaunches(calls, "babysit-review") != 0 {
			t.Errorf("pane reads %v launches %d, want a quiet tick with no probe", *reads, countWorkflowLaunches(calls, "babysit-review"))
		}
		if s.CurrentDelaySeconds <= s.IntervalSeconds {
			t.Errorf("CurrentDelaySeconds = %d, want it backed off past %d", s.CurrentDelaySeconds, s.IntervalSeconds)
		}
	})
}

// Priority and one writer per PR: a live babysit-review pane suppresses every
// other branch writer; a discussion comment created while it is live is pending
// and unlaunched; a conflict seen while it is live records the episode and the
// first tick after it ends launches merge-repair first.
func TestTickLiveBabysitReviewWindowSuppressesEveryOtherWriter(t *testing.T) {
	failingDirty := func(head string) []string { return []string{conflictingOpenPR(head), ciCapFailing, `[]`, `[]`} }
	failingClean := func(head string) []string { return []string{cleanOpenPR(head), ciCapFailing, `[]`, `[]`} }
	for _, tc := range []struct {
		name      string
		responses []string
	}{
		{"unchanged head", append(failingClean("sha-a"), failingClean("sha-a")...)},
		{"after a head change", append(failingClean("sha-a"), failingClean("sha-b")...)},
		{"conflicting", append(failingDirty("sha-a"), failingDirty("sha-b")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			withCommands(t, tc.responses, &calls)
			stubPanes(t, panesAlways(liveReview()))
			serveDiscussion(t, humanComment(11, humanBody))
			s := outstandingReviewState()
			s.DiscussionRevisions = nil
			s.PendingKeys, s.LaunchedKeys = nil, nil

			tickOK(t, &s)
			tickOK(t, &s)

			if got := runOrder(calls); len(got) != 0 {
				t.Errorf("cenci run calls = %q, want none while 42-babysit-review is live", got)
			}
			if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || len(s.LaunchedKeys) != 0 {
				t.Errorf("pending/launched = %v/%v, want the comment pending and unlaunched", s.PendingKeys, s.LaunchedKeys)
			}
		})
	}

	t.Run("conflict then merge-repair first after the window ends", func(t *testing.T) {
		var calls [][]string
		withCommands(t, append(append(failingDirty("sha-a"), failingDirty("sha-a")...), failingDirty("sha-a")...), &calls)
		stubPanes(t, panesByTick(liveReview(), liveReview(), deadReview()))
		serveDiscussion(t, humanComment(11, humanBody))
		s := reviewState()

		tickOK(t, &s)
		if s.ConflictRepairSHA != "sha-a" || len(runOrder(calls)) != 0 {
			t.Fatalf("tick 1: conflictRepairHeadSha %q launches %q, want the episode recorded and nothing launched", s.ConflictRepairSHA, runOrder(calls))
		}
		tickOK(t, &s)
		tickOK(t, &s)
		if got := runOrder(calls); !reflect.DeepEqual(got, []string{"merge-repair"}) {
			t.Errorf("cenci run calls = %q, want exactly merge-repair first after the window ended", got)
		}
	})
}

func deadReview() string { return paneRowOf("42-babysit-review", true, ownedRepoDir) }

// A failing-CI tick with a new discussion comment makes exactly one `cenci run`
// (ci-repair); the first green tick after that window ends launches babysit-review.
func TestTickFailingCIAndNewDiscussionLaunchesOnlyCIRepairFirst(t *testing.T) {
	var calls [][]string
	withCommands(t, append(ciRepairTickResponses("sha-a"), cleanOpenPR("sha-b"), ciCapGreen, `[]`, `[]`), &calls)
	stubPanes(t, panesByTick("", deadCI()))
	serveDiscussion(t, humanComment(11, humanBody))
	s := reviewState()

	tickOK(t, &s)
	if got := runOrder(calls); !reflect.DeepEqual(got, []string{"ci-repair"}) {
		t.Fatalf("tick 1: cenci run calls = %q, want exactly ci-repair", got)
	}
	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || len(s.LaunchedKeys) != 0 {
		t.Errorf("tick 1: pending/launched = %v/%v, want pending and unlaunched", s.PendingKeys, s.LaunchedKeys)
	}
	tickOK(t, &s)
	if got := runOrder(calls); !reflect.DeepEqual(got, []string{"ci-repair", "babysit-review"}) {
		t.Errorf("cenci run calls = %q, want babysit-review on the first tick with no writer live", got)
	}
}

// Launch safety: an unreadable inventory, a vanished session, an address-review
// window, a live same-name window of another repository and exit 3.
func TestTickBabysitReviewLaunchGuards(t *testing.T) {
	t.Run("an unreadable pane inventory launches nothing and records the failure", func(t *testing.T) {
		withFleetAutomergeEnabled(t, true)
		var calls [][]string
		withCommands(t, openPR2(), &calls)
		stubPanes(t, func(int) (string, error) { return "42-babysit-review\t0\n", nil })
		serveDiscussion(t, humanComment(11, humanBody))
		s := reviewState()
		if _, _, err := tick(&s); err == nil {
			t.Fatal("tick: err = nil, want the inventory failure")
		}
		if n := countWorkflowLaunches(calls, "babysit-review"); n != 0 || s.AutomergeReason != reasonWorkflowLaunchFailed || len(s.LaunchedKeys) != 0 {
			t.Errorf("launches %d reason %q launched %v, want 0 / %q / none", n, s.AutomergeReason, s.LaunchedKeys, reasonWorkflowLaunchFailed)
		}
	})
	t.Run("a recorded session that no longer exists launches nothing", func(t *testing.T) {
		withFleetAutomergeEnabled(t, true)
		var calls [][]string
		withCommands(t, openPR2(), &calls)
		tmuxHasSession = func(string, string) (bool, error) { return false, nil }
		serveDiscussion(t, humanComment(11, humanBody))
		s := reviewState()
		if _, _, err := tick(&s); err == nil {
			t.Fatal("tick: err = nil, want the missing-session failure")
		}
		if n := countWorkflowLaunches(calls, "babysit-review"); n != 0 || s.AutomergeReason != reasonWorkflowLaunchFailed {
			t.Errorf("launches %d reason %q, want 0 / %q", n, s.AutomergeReason, reasonWorkflowLaunchFailed)
		}
		if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) || len(s.LaunchedKeys) != 0 || s.ReviewLaunch != nil {
			t.Errorf("pending/launched/launch = %v/%v/%v, want the key pending and nothing recorded", s.PendingKeys, s.LaunchedKeys, s.ReviewLaunch)
		}
	})
	t.Run("exit 3 counts as dispatched", func(t *testing.T) {
		var calls [][]string
		withCommands(t, openPR2(), &calls)
		stubCenciRunExit(t, &calls, 3, "cenci run: setting automatic-rename off: boom: tmux window already spawned")
		withReviewSeams(t, testLaunchAt, testReviewNonce)
		serveDiscussion(t, humanComment(11, humanBody))
		s := reviewState()
		tickOK(t, &s)
		if !reflect.DeepEqual(s.LaunchedKeys, []string{"discussion:11"}) || s.ReviewLaunch == nil || s.ReviewLaunch.Nonce != testReviewNonce {
			t.Errorf("launched %v launch %+v, want the keys launched and the nonce saved", s.LaunchedKeys, s.ReviewLaunch)
		}
	})
	t.Run("a live window of another repository does not block", func(t *testing.T) {
		var calls [][]string
		withCommands(t, openPR2(), &calls)
		stubGitCommonDir(t, map[string]string{ownedRepoDir: "/repo/root/.git", "/elsewhere/repo": "/elsewhere/repo/.git"})
		stubPanes(t, panesAlways(paneRowOf("42-babysit-review", false, "/elsewhere/repo")))
		serveDiscussion(t, humanComment(11, humanBody))
		s := reviewState()
		tickOK(t, &s)
		if n := countWorkflowLaunches(calls, "babysit-review"); n != 1 {
			t.Errorf("babysit-review launches = %d, want 1: a foreign window never blocks", n)
		}
	})
	t.Run("a live address-review window blocks every launch and automerge", func(t *testing.T) {
		withFleetAutomergeEnabled(t, true)
		var calls [][]string
		withCommands(t, automergeOnResponses(), &calls)
		stubPanes(t, panesAlways(paneRowOf("42-address-review", false, ownedRepoDir)))
		serveDiscussion(t, humanComment(11, humanBody))
		s := reviewState()
		tickOK(t, &s)
		if got := runOrder(calls); len(got) != 0 {
			t.Errorf("cenci run calls = %q, want none while 42-address-review is live", got)
		}
		if s.AutomergeReason != reasonRepairWorkerLive || !strings.Contains(s.AutomergeDetail, "work:42-address-review") || !strings.Contains(s.AutomergeDetail, "close it once its edits are preserved") {
			t.Errorf("reason %q detail %q, want the live hold naming the window and the close instruction", s.AutomergeReason, s.AutomergeDetail)
		}
	})
}

// Q&A 5: a launch carries at most maxReviewLaunchKeys discussion keys; the rest
// stay pending and unlaunched for a later tick.
func TestTickBabysitReviewLaunchCarriesAtMostFortyDiscussionKeys(t *testing.T) {
	var calls [][]string
	withCommands(t, openPR2(), &calls)
	withReviewSeams(t, testLaunchAt, testReviewNonce)
	var comments []string
	for id := int64(1); id <= 45; id++ {
		comments = append(comments, humanComment(id, fmt.Sprintf("comment %d", id)))
	}
	serveDiscussion(t, comments...)
	s := reviewState()

	tickOK(t, &s)

	arg, _ := launchArg(calls, "babysit-review")
	if n := strings.Count(arg, "discussion:"); n != maxReviewLaunchKeys {
		t.Errorf("launch carries %d discussion keys, want %d", n, maxReviewLaunchKeys)
	}
	if len(s.PendingKeys) != 45 || len(s.LaunchedKeys) != 40 {
		t.Errorf("pending %d launched %d, want 45 pending and 40 launched", len(s.PendingKeys), len(s.LaunchedKeys))
	}
}

// Save and reload preserve the new discussion fields exactly; a v2 file without
// them loads with an empty seen-set and its first tick re-detects every human
// discussion comment.
func TestStateDiscussionFieldsRoundTripAndV2UpgradeRedetects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := outstandingReviewState()
	want.SchemaVersion = stateSchemaVersion
	want.AddressedKeys = []string{"discussion:9"}
	want.DiscussionReceipts = map[string]discussionReceipt{"discussion:9": {CommentID: 9, Revision: strings.Repeat("a", 64), HeadSHA: testReceiptHead, Outcome: "no-action", ReceiptCommentID: 8, Source: receiptSourceHumanCLI}}
	want.ConsumedReviewNonces = map[string]int64{testWrongNonce: 8, "cccccccccccccccccccccccccccccccc": 0}
	if err := save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readStateStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reloaded state differs:\n got %+v\nwant %+v", got, want)
	}

	v2 := filepath.Join(t.TempDir(), "v2.json")
	if err := os.WriteFile(v2, []byte(`{"schemaVersion":2,"pr":"42","repo":"o/r","agent":"codex","status":"running"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := readStateStrict(v2)
	if err != nil || len(s.DiscussionRevisions) != 0 {
		t.Fatalf("v2 load: %v revisions %v, want an empty seen-set", err, s.DiscussionRevisions)
	}
	var calls [][]string
	withCommands(t, openPR2(), &calls)
	serveDiscussion(t, humanComment(11, "one"), humanComment(12, "two"))
	s.LaunchSession, s.LaunchDir = "work", ownedRepoDir
	tickOK(t, &s)
	if !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11", "discussion:12"}) {
		t.Errorf("PendingKeys = %v, want every human discussion comment re-detected", s.PendingKeys)
	}
}
