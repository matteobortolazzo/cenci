package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
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

func TestGhJSONAcceptsChecksExitWithValidJSON(t *testing.T) {
	original := execGh
	execGh = func(...string) (string, string, error) {
		return `[{"bucket":"fail","name":"test"}]`, "", errors.New("exit status 1")
	}
	t.Cleanup(func() { execGh = original })
	var checks []check
	if err := ghJSON(&checks, "pr", "checks", "42"); err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Bucket != "fail" {
		t.Fatalf("checks=%#v", checks)
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

// withCommands stubs both the package's `gh` seam (execGh) and its
// non-`gh` seam (command, still used for `git rev-parse` and the `cenci
// run` self-exec) for the duration of a test, serving responses in order
// for every `gh` invocation and recording every call (both seams) into
// calls in call order -- preserving the pre-#854 `[]string{"gh", ...}`
// recorded-call shape so every existing assertion keeps working unchanged.
func withCommands(t *testing.T, responses []string, calls *[][]string) {
	t.Helper()
	originalCommand := command
	originalExecGh := execGh
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
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
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
	// A new comment:7 key is appended to PendingKeys before reconcileFeedback's
	// end-of-tick resolution pass runs later in the same tick, so the lazy
	// GraphQL thread fetch fires immediately -- the 5th scripted response.
	// Left unresolved so PendingKeys still equals [comment:7] afterward,
	// matching the assertions below; the #885 dedup-driven address-review
	// launch (PendingKeys \ LaunchedKeys, fired after reconcile) still finds
	// comment:7 unlaunched and dispatches it.
	unresolvedThread := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"isResolved":false,"comments":{"totalCount":1,"nodes":[{"databaseId":7}]}}]}}}}}`
	withCommands(t, []string{openPR(), `[]`, `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`, `[]`, unresolvedThread}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 300, CurrentDelaySeconds: 900}
	_, delay, err := tick(&s)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 300*time.Second || s.PendingCommentAt == "" || !reflect.DeepEqual(s.PendingKeys, []string{"comment:7"}) {
		t.Fatalf("unexpected state: %#v", s)
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

func TestTickMergedRelabelsClosingIssues(t *testing.T) {
	var calls [][]string
	merged := `{"number":42,"title":"Done","state":"MERGED","url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	withCommands(t, []string{merged, `{}`, `{}`}, &calls)
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
			s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
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
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
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
	t.Cleanup(func() {
		command = originalCommand
		execGh = originalExecGh
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
