package babysit

// #1456: the CI repair budget running out pauses only CI repair. These tests
// drive the real Run loop across several ticks against a route-based fake gh
// (pr view and pr checks served from a per-tick queue, everything else routed
// by subcommand), a recording sleep stub and the tmuxHasSession seam, which
// only launch() consults -- so launch attempts are counted without a real
// tmux (watch/docs/test-strategy.md). Every fixture ends with MERGED or CLOSED
// so Run returns on its own; the state file the supervisor saved before each
// pr view is captured inside the fake handler (launch_test.go's pattern), so
// saved[k] is the state after tick k. A `gh pr merge` call fails the test.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	ciCapFailing     = `[{"bucket":"fail","name":"test","state":"FAILURE"}]`
	ciCapPending     = `[{"bucket":"pending","name":"test","state":"PENDING"}]`
	ciCapGreen       = `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`
	ciCapCancel      = `[{"bucket":"cancel","name":"test","state":"CANCELLED"}]`
	ciCapEmptyBucket = `[{"bucket":"","name":"test","state":""}]`
	ciCapSkipping    = `[{"bucket":"skipping","name":"test","state":"SKIPPED"}]`
	ciCapNoChecks    = `[]`
)

// ciCapPR renders a `gh pr view` body. mergeState DIRTY models a conflict from
// a sibling merge; anything else is a clean, mergeable PR.
func ciCapPR(state, head, mergeState string, closing ...int) string {
	mergeable := "MERGEABLE"
	if mergeState == "DIRTY" {
		mergeable = "CONFLICTING"
	}
	refs := make([]string, 0, len(closing))
	for _, n := range closing {
		refs = append(refs, fmt.Sprintf(`{"number":%d}`, n))
	}
	return fmt.Sprintf(`{"number":42,"title":"Change","state":%q,"headRefName":"feature","headRefOid":%q,"mergeable":%q,"mergeStateStatus":%q,"url":"https://example/pr/42","closingIssuesReferences":[%s]}`,
		state, head, mergeable, mergeState, strings.Join(refs, ","))
}

func ciCapOpen(head string, closing ...int) string {
	return ciCapPR("OPEN", head, "CLEAN", closing...)
}

func ciCapDirty(head string, closing ...int) string {
	return ciCapPR("OPEN", head, "DIRTY", closing...)
}

// ciCapStep is one tick's GitHub answers: the pr view body and either the pr
// checks body or a pr checks failure.
type ciCapStep struct {
	view      string
	checks    string
	checksErr error
	// comments is the pull-request review-comments page served to this tick;
	// empty means no comments.
	comments string
}

func ciCapTickOf(view, checks string) ciCapStep {
	return ciCapStep{view: view, checks: checks}
}

func ciCapMerged(closing ...int) ciCapStep {
	return ciCapStep{view: ciCapPR("MERGED", "", "", closing...)}
}

func ciCapClosed(closing ...int) ciCapStep {
	return ciCapStep{view: ciCapPR("CLOSED", "", "", closing...)}
}

// ciCapKill is the panic value the sleep stub raises to model a supervisor
// process dying right after a tick's save (its deferred lock removal still
// runs, which is what lets the "restarted" Run take the lock again).
type ciCapKill struct{}

type ciCapEnv struct {
	t     *testing.T
	dir   string
	path  string
	steps []ciCapStep
	viewN int
	// saved[k] is the state file as it stood when pr view number k+1 was
	// served: the arm-time eager save for k == 0, otherwise the state after
	// tick k.
	saved  []State
	calls  [][]string
	sleeps []time.Duration
	// commentReads[k] and reviewReads[k] count the review-comment and review
	// reads tick k (1-based, the pr view number) performed: proof the tick kept
	// reconciling review feedback.
	commentReads map[int]int
	reviewReads  map[int]int
	// attempts counts launch() attempts, which are the only callers of the
	// tmuxHasSession seam; sessionUp decides each attempt's outcome (nil: all
	// succeed).
	attempts    int
	sessionUp   func(attempt int) bool
	killAtSleep int
}

func newCICapEnv(t *testing.T, steps ...ciCapStep) *ciCapEnv {
	t.Helper()
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
	e := &ciCapEnv{t: t, dir: t.TempDir(), steps: steps, commentReads: map[int]int{}, reviewReads: map[int]int{}}
	e.path = statePath(e.dir, "o/r", "42")

	originalGh, originalCommand := execGh, command
	originalTmux, originalSleep := tmuxHasSession, sleep
	t.Cleanup(func() {
		execGh, command = originalGh, originalCommand
		tmuxHasSession, sleep = originalTmux, originalSleep
	})

	execGh = e.gh
	command = func(name string, args ...string) ([]byte, error) {
		e.calls = append(e.calls, append([]string{name}, args...))
		if name == "git" {
			return []byte("/repo/root\n"), nil
		}
		return []byte(""), nil
	}
	tmuxHasSession = func(string) (bool, error) {
		e.attempts++
		return e.sessionUp == nil || e.sessionUp(e.attempts), nil
	}
	sleep = func(d time.Duration) {
		e.sleeps = append(e.sleeps, d)
		if e.killAtSleep > 0 && len(e.sleeps) == e.killAtSleep {
			panic(ciCapKill{})
		}
	}
	return e
}

func (e *ciCapEnv) gh(args ...string) (string, string, error) {
	e.calls = append(e.calls, append([]string{"gh"}, args...))
	if len(args) == 0 {
		return "", "", errors.New("empty gh call")
	}
	switch {
	case args[0] == "repo":
		return "o/r\n", "", nil
	case len(args) > 1 && args[0] == "pr" && args[1] == "view":
		e.viewN++
		// A state file is always on disk when the supervisor reads the PR, so an
		// unreadable one is a failure: a zero State would let the negative
		// assertions (not paused, not notified) pass vacuously. t.Errorf is safe
		// here: the handler runs on Run's goroutine.
		saved, err := readStateStrict(e.path)
		if err != nil {
			e.t.Errorf("pr view #%d: the saved state is unreadable: %v", e.viewN, err)
		}
		e.saved = append(e.saved, saved)
		if e.viewN > len(e.steps) {
			e.t.Errorf("pr view #%d: the fixture ran out of ticks; ending the run with MERGED", e.viewN)
			return ciCapPR("MERGED", "", ""), "", nil
		}
		return e.steps[e.viewN-1].view, "", nil
	case len(args) > 1 && args[0] == "pr" && args[1] == "checks":
		step := e.steps[e.viewN-1]
		if step.checksErr != nil {
			return "", "gh: read failed", step.checksErr
		}
		return step.checks, "", nil
	case len(args) > 1 && args[0] == "pr" && args[1] == "merge":
		e.t.Errorf("gh pr merge must never be called: %v", args)
		return "", "", errors.New("merge forbidden by the fixture")
	case args[0] == "api":
		switch {
		case len(args) > 1 && strings.Contains(args[1], "/pulls/42/comments"):
			e.commentReads[e.viewN]++
			if c := e.steps[e.viewN-1].comments; c != "" {
				return c, "", nil
			}
		case len(args) > 1 && strings.Contains(args[1], "/pulls/42/reviews"):
			e.reviewReads[e.viewN]++
		}
		return "[]", "", nil
	case len(args) > 1 && args[0] == "issue" && args[1] == "view":
		switch ciCapJSONField(args) {
		case "labels":
			return `{"labels":[{"name":"automerge:ok"}]}`, "", nil
		case "parent":
			return `{"parent":{"number":900}}`, "", nil
		case "state,subIssues":
			return `{"state":"CLOSED"}`, "", nil
		}
	case args[0] == "label" || (len(args) > 1 && args[0] == "issue" && args[1] == "edit"):
		return "", "", nil
	}
	e.t.Errorf("unexpected gh call: %v", args)
	return "", "", fmt.Errorf("unexpected gh call: %v", args)
}

func ciCapJSONField(args []string) string {
	i := slices.Index(args, "--json")
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

func (e *ciCapEnv) seed(s State) {
	e.t.Helper()
	s.SchemaVersion = stateSchemaVersion
	s.PR, s.Repo, s.Agent = "42", "o/r", "claude"
	s.IntervalSeconds = 60
	if err := save(e.path, s); err != nil {
		e.t.Fatal(err)
	}
}

func (e *ciCapEnv) opts(once bool) Options {
	return Options{PR: "42", Agent: "claude", StateDir: e.dir, Interval: time.Minute, Once: once, Session: "work", Dir: "/repo/root"}
}

func (e *ciCapEnv) run() error {
	e.t.Helper()
	return Run(e.opts(false))
}

// runKilledAtSleep runs the supervisor until its n-th wait overall, then
// kills it there: the state saved just before that wait is what a restarted
// supervisor will find.
func (e *ciCapEnv) runKilledAtSleep(n int) {
	e.t.Helper()
	e.killAtSleep = n
	killed := false
	var runErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(ciCapKill); !ok {
					panic(r)
				}
				killed = true
			}
		}()
		runErr = Run(e.opts(false))
	}()
	e.killAtSleep = 0
	if !killed {
		e.t.Fatalf("Run returned before its wait #%d (Run returned: %v)", n, runErr)
	}
}

// after returns the state as saved after tick k (1-based).
func (e *ciCapEnv) after(k int) State {
	e.t.Helper()
	if len(e.saved) <= k {
		e.t.Fatalf("no state after tick %d: only %d pr views were served (Run stopped early?)", k, e.viewN)
	}
	return e.saved[k]
}

func (e *ciCapEnv) launches(workflow string) int {
	return countWorkflowLaunches(e.calls, workflow)
}

func (e *ciCapEnv) ghCalled(words ...string) bool {
	for _, c := range e.calls {
		if len(c) > len(words) && c[0] == "gh" && slices.Equal(c[1:1+len(words)], words) {
			return true
		}
	}
	return false
}

func (e *ciCapEnv) requireStateRemoved() {
	e.t.Helper()
	if _, err := os.Stat(e.path); !os.IsNotExist(err) {
		e.t.Errorf("state file still exists after the terminal tick (stat err = %v)", err)
	}
}

// atCap is a supervisor whose CI budget is used up: three ci-repair launches
// done, the last at head-old.
func atCap(mod ...func(*State)) State {
	s := State{
		FixAttempts: fixCap, RepairPending: true,
		LastHeadSHA: "head-old", LastRepairedSHA: "head-old",
		CIStatus: ciStatusFailing, Status: "running",
	}
	for _, m := range mod {
		m(&s)
	}
	return s
}

// pausedAtCap is atCap after the cap tick: paused, notified, needs-input.
func pausedAtCap(mod ...func(*State)) State {
	s := atCap(func(s *State) {
		s.CIRepairPaused, s.CICapNotified, s.Status = true, true, "needs-input"
	})
	for _, m := range mod {
		m(&s)
	}
	return s
}

func requirePaused(t *testing.T, label string, s State) {
	t.Helper()
	if s.FixAttempts != fixCap || !s.RepairPending || !s.CIRepairPaused {
		t.Errorf("%s: ciFixAttempts=%d ciRepairPending=%v ciRepairPaused=%v, want %d/true/true (still paused)", label, s.FixAttempts, s.RepairPending, s.CIRepairPaused, fixCap)
	}
}

func requireRecovered(t *testing.T, label string, s State) {
	t.Helper()
	if s.FixAttempts != 0 || s.RepairPending || s.CIRepairPaused || s.CICapNotified || s.Status != "running" {
		t.Errorf("%s: ciFixAttempts=%d ciRepairPending=%v ciRepairPaused=%v ciCapNotified=%v status=%q, want 0/false/false/false/running",
			label, s.FixAttempts, s.RepairPending, s.CIRepairPaused, s.CICapNotified, s.Status)
	}
}

// -- AC 1 --------------------------------------------------------------------

func TestCICap_AC1_RunKeepsPollingAfterTheCapTickUntilMerged(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 1: reaches the cap
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 2: still failing
		ciCapMerged(), // tick 3
	)
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if e.viewN != 3 {
		t.Fatalf("pr view calls = %d, want 3: Run must keep polling after the cap tick", e.viewN)
	}
	s := e.after(1)
	if s.Status != "needs-input" || s.FixAttempts != fixCap || !s.CIRepairPaused || s.PID != os.Getpid() {
		t.Errorf("state after the cap tick: status=%q ciFixAttempts=%d ciRepairPaused=%v pid=%d, want needs-input/%d/true/%d",
			s.Status, s.FixAttempts, s.CIRepairPaused, s.PID, fixCap, os.Getpid())
	}
	if n := e.launches("babysit-attention"); n != 1 {
		t.Errorf("babysit-attention launches = %d, want 1", n)
	}
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
	if len(e.sleeps) != 2 {
		t.Errorf("waits = %d, want 2 (one after each non-terminal tick)", len(e.sleeps))
	}
	// Only CI repair is paused: the cap tick and the paused tick after it
	// both kept reconciling review feedback (comments and reviews reads).
	for _, tick := range []int{1, 2} {
		if e.commentReads[tick] == 0 || e.reviewReads[tick] == 0 {
			t.Errorf("tick %d: comment reads=%d review reads=%d, want both > 0 while CI repair is paused",
				tick, e.commentReads[tick], e.reviewReads[tick])
		}
	}
	e.requireStateRemoved()
}

// A new non-bot review comment arriving while CI repair is paused still
// launches address-review: the pause covers CI repair only.
func TestCICap_ReviewFeedbackIsStillAddressedWhileCIIsPaused(t *testing.T) {
	const newComment = `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing),                                   // tick 1: cap
		ciCapStep{view: ciCapOpen("head-a"), checks: ciCapFailing, comments: newComment}, // tick 2: paused, a reviewer comments
		ciCapMerged(),
	)
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := e.launches("address-review"); n != 1 {
		t.Errorf("address-review launches = %d, want 1 for the comment that arrived while CI was paused", n)
	}
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
	s := e.after(2)
	requirePaused(t, "after the review-feedback tick", s)
	if s.Status != "needs-input" {
		t.Errorf("status = %q, want needs-input (CI is still paused)", s.Status)
	}
	if !slices.Equal(s.PendingKeys, []string{"comment:7"}) || !slices.Equal(s.LaunchedKeys, []string{"comment:7"}) {
		t.Errorf("pendingKeys=%v launchedKeys=%v, want [comment:7] for both", s.PendingKeys, s.LaunchedKeys)
	}
}

// -- AC 2 --------------------------------------------------------------------

func TestCICap_AC2_OneAttentionAcrossHeadsAndAReload(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 1: cap at A
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 2: A again
		ciCapTickOf(ciCapOpen("head-b"), ciCapFailing), // tick 3: new head B
		// the supervisor is killed after tick 3's save and a new Run reloads
		ciCapTickOf(ciCapOpen("head-c"), ciCapFailing), // tick 4: new head C
		ciCapMerged(),
	)
	e.seed(atCap())

	e.runKilledAtSleep(3)
	reloaded := load(e.path)
	if !reloaded.CIRepairPaused || !reloaded.CICapNotified || reloaded.FixAttempts != fixCap {
		t.Fatalf("state reloaded from disk: ciRepairPaused=%v ciCapNotified=%v ciFixAttempts=%d, want true/true/%d",
			reloaded.CIRepairPaused, reloaded.CICapNotified, reloaded.FixAttempts, fixCap)
	}
	if err := e.run(); err != nil {
		t.Fatalf("restarted Run: %v", err)
	}

	if n := e.launches("babysit-attention"); n != 1 {
		t.Errorf("babysit-attention launches across A, A, B, reload, C = %d, want exactly 1", n)
	}
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
}

// -- AC 3 --------------------------------------------------------------------

func TestCICap_AC3_FailedAttentionLaunchKeepsTheCapAndRetries(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 1: the launch fails
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 2: the retry succeeds
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 3: already notified
		ciCapMerged(),
	)
	e.sessionUp = func(attempt int) bool { return attempt > 1 }
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	failed := e.after(1)
	requirePaused(t, "after the failed launch", failed)
	if failed.CICapNotified {
		t.Error("ciCapNotified set after a failed launch, want it set only after a successful one")
	}
	if failed.AutomergeReason != reasonWorkflowLaunchFailed {
		t.Errorf("AutomergeReason after the failed launch = %q, want %q", failed.AutomergeReason, reasonWorkflowLaunchFailed)
	}
	if failed.Status != "retrying" {
		t.Errorf("status after the failed launch = %q, want retrying", failed.Status)
	}
	if len(e.sleeps) == 0 || e.sleeps[0] != 120*time.Second {
		t.Errorf("first wait = %v, want the 120s retry backoff", e.sleeps)
	}
	retried := e.after(2)
	requirePaused(t, "after the retried launch", retried)
	if !retried.CICapNotified || retried.Status != "needs-input" {
		t.Errorf("after the retried launch: ciCapNotified=%v status=%q, want true/needs-input", retried.CICapNotified, retried.Status)
	}
	if e.attempts != 2 {
		t.Errorf("launch attempts = %d, want exactly 2 (one failed, one succeeded)", e.attempts)
	}
	if n := e.launches("babysit-attention"); n != 1 {
		t.Errorf("babysit-attention runs = %d, want 1", n)
	}
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
}

// -- AC 4 --------------------------------------------------------------------

func TestCICap_AC4_ManualPushThenGreenAtTheNewHeadEndsTheEpisode(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 1: cap
		ciCapTickOf(ciCapOpen("head-b"), ciCapPending), // tick 2: manual push, pending
		ciCapTickOf(ciCapOpen("head-b"), ciCapGreen),   // tick 3: green at B
		ciCapMerged(),
	)
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	requirePaused(t, "after the pending tick at the pushed head", e.after(2))
	requireRecovered(t, "after the green tick at the new head", e.after(3))
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
}

func TestCICap_AC4_FailedChecksRerunGreenAtTheSameHeadEndsTheEpisode(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 1: cap at A
		ciCapTickOf(ciCapOpen("head-a"), ciCapGreen),   // tick 2: rerun green, no push
		ciCapMerged(),
	)
	// LastHeadSHA already equals the capped head: only a head-independent
	// green reset can clear the episode.
	e.seed(atCap(func(s *State) { s.LastHeadSHA = "head-a" }))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	requireRecovered(t, "after the same-head green rerun", e.after(2))
}

// -- AC 5 --------------------------------------------------------------------

func TestCICap_AC5_NoRecoveryAndNoMergeWithoutAFreshGreen(t *testing.T) {
	const issue = 782
	for _, tc := range []struct {
		name       string
		step       ciCapStep
		wantReason string
	}{
		{"pending checks at the capped head", ciCapTickOf(ciCapOpen("head-a", issue), ciCapPending), reasonCICheckPending},
		{"unknown: a cancel bucket", ciCapTickOf(ciCapOpen("head-a", issue), ciCapCancel), reasonCICheckCancelled},
		{"unknown: an empty bucket", ciCapTickOf(ciCapOpen("head-a", issue), ciCapEmptyBucket), reasonCIBucketEmpty},
		{"zero checks", ciCapTickOf(ciCapOpen("head-a", issue), ciCapNoChecks), reasonNoChecks},
		{"a gh pr checks read error", ciCapStep{view: ciCapOpen("head-a", issue), checksErr: errors.New("exit status 1")}, reasonUpstreamChecksUnreadable},
		{"a head change with pending checks", ciCapTickOf(ciCapOpen("head-b", issue), ciCapPending), reasonCICheckPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFleetAutomergeEnabled(t, true)
			e := newCICapEnv(t,
				ciCapTickOf(ciCapOpen("head-a", issue), ciCapFailing), // tick 1: cap
				tc.step, // tick 2: no fresh green
				ciCapMerged(issue),
			)
			e.seed(atCap())

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			s := e.after(2)
			requirePaused(t, "after the non-green tick", s)
			if s.AutomergeDecision != "held" || s.AutomergeReason != tc.wantReason {
				t.Errorf("automerge decision = %q (%q), want held (%q)", s.AutomergeDecision, s.AutomergeReason, tc.wantReason)
			}
			if e.ghCalled("pr", "merge") {
				t.Error("gh pr merge was called")
			}
		})
	}
}

// -- AC 6 --------------------------------------------------------------------

func TestCICap_AC6_SiblingMergeConflictIsRepairedWhileCIStaysPaused(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing),  // tick 1: cap
		ciCapTickOf(ciCapDirty("head-a"), ciCapFailing), // tick 2: a sibling merge conflicts the PR
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing),  // tick 3: conflict cleared
		ciCapMerged(),
	)
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	conflicted := e.after(2)
	if conflicted.ConflictFixAttempts != 1 {
		t.Errorf("conflictFixAttempts after the DIRTY tick = %d, want 1 (own budget)", conflicted.ConflictFixAttempts)
	}
	requirePaused(t, "after the DIRTY tick", conflicted)
	cleared := e.after(3)
	if cleared.ConflictFixAttempts != 0 || cleared.ConflictRepairSHA != "" {
		t.Errorf("conflict episode after the CLEAN tick: attempts=%d repairSha=%q, want released", cleared.ConflictFixAttempts, cleared.ConflictRepairSHA)
	}
	requirePaused(t, "after the conflict-clear tick", cleared)
	if cleared.Status != "needs-input" {
		t.Errorf("status on the conflict-clear tick = %q, want needs-input (CI is still paused)", cleared.Status)
	}
	if n := e.launches("merge-repair"); n != 1 {
		t.Errorf("merge-repair launches = %d, want 1", n)
	}
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
	if n := e.launches("babysit-attention"); n != 1 {
		t.Errorf("babysit-attention launches = %d, want 1 (the CI cap's, not repeated)", n)
	}
}

// -- AC 7 --------------------------------------------------------------------

func TestCICap_AC7_MergedAfterTheCapRunsTerminalCleanup(t *testing.T) {
	const issue = 782
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a", issue), ciCapFailing), // tick 1: cap
		ciCapMerged(issue),
	)
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !e.ghCalled("label", "create", "Implemented") {
		t.Error("the Implemented label was not ensured")
	}
	if !e.ghCalled("issue", "edit", "782", "--repo", "o/r", "--add-label", "Implemented") {
		t.Error("closing issue #782 was not relabelled Implemented")
	}
	if !e.ghCalled("issue", "view", "782", "--repo", "o/r", "--json", "parent") || !e.ghCalled("issue", "view", "900", "--repo", "o/r", "--json", "state,subIssues") {
		t.Error("reconcileParents did not run for the closed child")
	}
	e.requireStateRemoved()
}

func TestCICap_AC7_ClosedAfterTheCapRemovesStateWithoutRelabelling(t *testing.T) {
	const issue = 782
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a", issue), ciCapFailing), // tick 1: cap
		ciCapClosed(issue),
	)
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if e.viewN != 2 {
		t.Fatalf("pr view calls = %d, want 2: Run must keep polling after the cap tick", e.viewN)
	}
	if e.ghCalled("label") || e.ghCalled("issue", "edit") {
		t.Errorf("a CLOSED PR ran the Implemented relabel: %v", e.calls)
	}
	e.requireStateRemoved()
}

// -- AC 9 --------------------------------------------------------------------

// legacyCIPausedState is a state file written before ciRepairPaused and
// ciCapNotified existed: a supervisor that hit the CI cap, launched attention,
// zeroed its pid and exited.
const legacyCIPausedState = `{"schemaVersion":2,"pr":"42","repo":"o/r","agent":"claude","intervalSeconds":60,"currentDelaySeconds":60,` +
	`"lastCiHeadSha":"head-old","lastRepairedCiHeadSha":"head-old","ciFixAttempts":3,"ciRepairPending":true,` +
	`"status":"needs-input","ciStatus":"failing","updatedAt":"2026-11-01T00:00:00Z"}`

func writeLegacyCIPausedState(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(legacyCIPausedState), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCICap_AC9_LegacyPausedStateLoadsAndReportsNotLive(t *testing.T) {
	e := newCICapEnv(t)
	stubProcessOwned(t, true)
	writeLegacyCIPausedState(t, e.path)

	s, err := readStateStrict(e.path)
	if err != nil {
		t.Fatalf("readStateStrict: %v", err)
	}
	if s.Status != "needs-input" || s.FixAttempts != fixCap || s.PID != 0 || s.CIRepairPaused || s.CICapNotified {
		t.Errorf("legacy state decoded as %+v, want needs-input, ciFixAttempts %d, pid 0 and no new markers", s, fixCap)
	}
	st, err := StatusRepo("o/r", "42", e.dir)
	if err != nil {
		t.Fatalf("StatusRepo: %v", err)
	}
	if st.SupervisorLive {
		t.Error("supervisorLive = true for a PID-0 state, want false until it is armed again")
	}
}

func TestCICap_AC9_ArmingParentDerivesNotifiedBeforeTheArmingWrite(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapGreen), // the arming parent's own pr view
		ciCapTickOf(ciCapOpen("head-b"), ciCapFailing),
		ciCapMerged(),
	)
	writeLegacyCIPausedState(t, e.path)

	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
	originalStart := startSupervisor
	started := 0
	startSupervisor = func(*exec.Cmd) error { started++; return nil }
	t.Cleanup(func() { startSupervisor = originalStart })
	done := captureStdout(t)
	err := Run(e.opts(false))
	_ = done()
	if err != nil {
		t.Fatalf("arming Run: %v", err)
	}
	if started != 1 {
		t.Fatalf("supervisor starts = %d, want 1", started)
	}
	armed := load(e.path)
	if armed.Status != "arming" || !armed.CIRepairPaused || !armed.CICapNotified {
		t.Fatalf("armed state: status=%q ciRepairPaused=%v ciCapNotified=%v, want arming/true/true", armed.Status, armed.CIRepairPaused, armed.CICapNotified)
	}

	// The detached child's first failing tick, at a head the legacy state
	// never saw.
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
	if err := e.run(); err != nil {
		t.Fatalf("supervisor Run: %v", err)
	}
	// The failing tick must really have reached the cap branch and succeeded:
	// had it errored, Run would retry, read the MERGED step and still return
	// nil with zero launches, so zero launches alone proves nothing.
	if e.viewN != 3 {
		t.Errorf("pr view calls = %d, want 3 (the parent's, the child's failing tick, MERGED)", e.viewN)
	}
	if s := e.after(2); !s.CIRepairPaused || !s.CICapNotified || s.Status != "needs-input" {
		t.Errorf("state after the child's failing tick: ciRepairPaused=%v ciCapNotified=%v status=%q, want true/true/needs-input", s.CIRepairPaused, s.CICapNotified, s.Status)
	}
	if e.attempts != 0 {
		t.Errorf("launch attempts after arming = %d, want 0 (the old state implies a sent notification)", e.attempts)
	}
	if n := e.launches("babysit-attention"); n != 0 {
		t.Errorf("babysit-attention launches after arming = %d, want 0 (the old state implies a sent notification)", n)
	}
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
}

func TestCICap_AC9_SupervisorChildDerivesNotifiedFromALegacyState(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-b"), ciCapFailing),
		ciCapMerged(),
	)
	writeLegacyCIPausedState(t, e.path)

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := e.launches("babysit-attention"); n != 0 {
		t.Errorf("babysit-attention launches = %d, want 0 (the old state implies a sent notification)", n)
	}
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
	s := e.after(1)
	if !s.CIRepairPaused || !s.CICapNotified || s.Status != "needs-input" {
		t.Errorf("state after the failing tick: ciRepairPaused=%v ciCapNotified=%v status=%q, want true/true/needs-input", s.CIRepairPaused, s.CICapNotified, s.Status)
	}
}

// -- AC 10 -------------------------------------------------------------------

func TestCICap_AC10_RestartedSupervisorDoesNotRenotifyOrRepair(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-new"), ciCapFailing),
		ciCapMerged(),
	)
	e.seed(pausedAtCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	started := e.after(0)
	if started.Status != "needs-input" || started.PID != os.Getpid() || !started.CIRepairPaused || !started.CICapNotified {
		t.Errorf("state at startup: status=%q pid=%d ciRepairPaused=%v ciCapNotified=%v, want needs-input/%d/true/true",
			started.Status, started.PID, started.CIRepairPaused, started.CICapNotified, os.Getpid())
	}
	if n := e.launches("babysit-attention"); n != 0 {
		t.Errorf("babysit-attention launches = %d, want 0", n)
	}
	if n := e.launches("ci-repair"); n != 0 {
		t.Errorf("ci-repair launches = %d, want 0", n)
	}
	if e.attempts != 0 {
		t.Errorf("launch attempts = %d, want 0", e.attempts)
	}
}

// -- Edge cases --------------------------------------------------------------

func TestCICap_Edge_ThirdWorkersHeadStillFailingLaunchesNothing(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // still the head the third ci-repair worked on
		ciCapMerged(),
	)
	e.seed(atCap(func(s *State) { s.LastHeadSHA, s.LastRepairedSHA = "head-a", "head-a" }))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if e.attempts != 0 || e.launches("ci-repair") != 0 || e.launches("babysit-attention") != 0 {
		t.Errorf("launch attempts=%d ci-repair=%d babysit-attention=%d, want none: re-eligibility of this head is #1457's job",
			e.attempts, e.launches("ci-repair"), e.launches("babysit-attention"))
	}
	if s := e.after(1); s.CIRepairPaused || s.CICapNotified {
		t.Errorf("the cap branch ran for an already-repaired head: ciRepairPaused=%v ciCapNotified=%v", s.CIRepairPaused, s.CICapNotified)
	}
}

func TestCICap_Edge_ConflictHeldGreenDoesNotResetThenTheFirstFreeGreenDoes(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapDirty("head-a"), ciCapGreen), // tick 1: green, but the conflict hold keeps priority
		ciCapTickOf(ciCapOpen("head-a"), ciCapGreen),  // tick 2: conflict released, first non-held green
		ciCapMerged(),
	)
	e.seed(pausedAtCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	held := e.after(1)
	requirePaused(t, "after the conflict-held green tick", held)
	if held.ConflictFixAttempts != 1 {
		t.Errorf("conflictFixAttempts = %d, want 1 (conflict repair keeps priority)", held.ConflictFixAttempts)
	}
	requireRecovered(t, "after the first non-conflict-held green tick", e.after(2))
}

func TestCICap_Edge_PendingNotificationWaitsForTheConflictHoldToClear(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapDirty("head-a"), ciCapFailing), // tick 1: conflict-held, notification still pending
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing),  // tick 2: conflict released
		ciCapMerged(),
	)
	e.seed(pausedAtCap(func(s *State) { s.CICapNotified = false }))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := e.launches("babysit-attention"); n != 1 {
		t.Fatalf("babysit-attention launches = %d, want 1", n)
	}
	if held := e.after(1); held.CICapNotified {
		t.Error("the attention notification was sent while the conflict hold was active")
	}
	if cleared := e.after(2); !cleared.CICapNotified || !cleared.CIRepairPaused {
		t.Errorf("after the conflict cleared: ciCapNotified=%v ciRepairPaused=%v, want the retried notification sent", cleared.CICapNotified, cleared.CIRepairPaused)
	}
}

func TestCICap_Edge_PendingNotificationIsDroppedAtGreen(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapGreen),
		ciCapMerged(),
	)
	e.seed(pausedAtCap(func(s *State) { s.CICapNotified = false }))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	requireRecovered(t, "after the green tick", e.after(1))
	if e.attempts != 0 || e.launches("babysit-attention") != 0 {
		t.Errorf("launch attempts=%d babysit-attention=%d, want none: the pending notification is dropped at green", e.attempts, e.launches("babysit-attention"))
	}
}

func TestCICap_Edge_AllSkippingChecksCountAsGreenAndEndTheEpisode(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapSkipping),
		ciCapMerged(),
	)
	e.seed(pausedAtCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	requireRecovered(t, "after the all-skipping tick", e.after(1))
}

func TestCICap_Edge_OnceRunAtTheCapSavesPausedStateAndReturnsWithoutSleeping(t *testing.T) {
	e := newCICapEnv(t, ciCapTickOf(ciCapOpen("head-a"), ciCapFailing))
	e.seed(atCap())

	if err := Run(e.opts(true)); err != nil {
		t.Fatalf("Run --once: %v", err)
	}

	if len(e.sleeps) != 0 {
		t.Errorf("waits = %v, want none under --once", e.sleeps)
	}
	s := load(e.path)
	if s.Status != "needs-input" || !s.CIRepairPaused || !s.CICapNotified || s.FixAttempts != fixCap {
		t.Errorf("saved state: status=%q ciRepairPaused=%v ciCapNotified=%v ciFixAttempts=%d, want needs-input/true/true/%d",
			s.Status, s.CIRepairPaused, s.CICapNotified, s.FixAttempts, fixCap)
	}
	if n := e.launches("babysit-attention"); n != 1 {
		t.Errorf("babysit-attention launches = %d, want 1", n)
	}
}

func TestCICap_Edge_LegacyMergeCapStateIsNotTakenAsASentCINotification(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing),
		ciCapMerged(),
	)
	// A pre-#1456 needs-input state from the merge-repair cap: the conflict
	// notification marker is set, so it says nothing about the CI cap.
	e.seed(atCap(func(s *State) {
		s.Status, s.ConflictNotifiedSHA = "needs-input", "head-c"
	}))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := e.launches("babysit-attention"); n != 1 {
		t.Errorf("babysit-attention launches = %d, want 1 (the CI cap was never notified)", n)
	}
	if s := e.after(1); !s.CICapNotified || !s.CIRepairPaused {
		t.Errorf("after the tick: ciCapNotified=%v ciRepairPaused=%v, want true/true", s.CICapNotified, s.CIRepairPaused)
	}
}

func TestCICap_Edge_NewFailureAfterAGreenResetStartsAFreshEpisode(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 1: cap, attention #1
		ciCapTickOf(ciCapOpen("head-b"), ciCapGreen),   // tick 2: green reset
		ciCapTickOf(ciCapOpen("head-c"), ciCapFailing), // tick 3: ci-repair #1
		ciCapTickOf(ciCapOpen("head-d"), ciCapFailing), // tick 4: ci-repair #2
		ciCapTickOf(ciCapOpen("head-e"), ciCapFailing), // tick 5: ci-repair #3
		ciCapTickOf(ciCapOpen("head-f"), ciCapFailing), // tick 6: cap again, attention #2
		ciCapTickOf(ciCapOpen("head-g"), ciCapFailing), // tick 7: no third attention
		ciCapMerged(),
	)
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	requireRecovered(t, "after the green reset", e.after(2))
	fresh := e.after(3)
	if fresh.FixAttempts != 1 || !fresh.RepairPending || fresh.CIRepairPaused || fresh.CICapNotified {
		t.Errorf("after the first new failure: ciFixAttempts=%d ciRepairPending=%v ciRepairPaused=%v ciCapNotified=%v, want 1/true/false/false",
			fresh.FixAttempts, fresh.RepairPending, fresh.CIRepairPaused, fresh.CICapNotified)
	}
	second := e.after(6)
	if second.FixAttempts != fixCap || !second.CIRepairPaused || !second.CICapNotified {
		t.Errorf("after the second cap: ciFixAttempts=%d ciRepairPaused=%v ciCapNotified=%v, want %d/true/true", second.FixAttempts, second.CIRepairPaused, second.CICapNotified, fixCap)
	}
	if n := e.launches("ci-repair"); n != fixCap {
		t.Errorf("ci-repair launches = %d, want %d (a fresh budget for the new episode)", n, fixCap)
	}
	if n := e.launches("babysit-attention"); n != 2 {
		t.Errorf("babysit-attention launches = %d, want 2 (one per episode)", n)
	}
}

func TestCICap_Edge_FailedAttentionLaunchThenMergedRunsCleanupAndTriesNoMore(t *testing.T) {
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a"), ciCapFailing), // tick 1: the launch fails
		ciCapMerged(),
	)
	e.sessionUp = func(int) bool { return false }
	e.seed(atCap())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if e.attempts != 1 {
		t.Errorf("launch attempts = %d, want 1: no attention launch is tried once the PR is MERGED", e.attempts)
	}
	if !e.ghCalled("label", "create", "Implemented") {
		t.Error("the terminal cleanup did not run")
	}
	e.requireStateRemoved()
}
