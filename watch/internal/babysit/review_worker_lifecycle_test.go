package babysit

// #1472: the supervisor's review worker, one branch writer per PR. These tests
// drive the real Run loop over the ciCapEnv harness (ci_cap_lifecycle_test.go):
// a route-based fake gh serves each tick's PR view, checks and PR conversation
// page, the panes hook answers each tick's single tmux pane inventory, and the
// state the supervisor saved after every tick is captured, so each scenario
// asserts recorded state and the `cenci run` calls made in each tick across
// several ticks -- not helper internals. Launch attribution comes from a ledger
// wrapped around the command seam, keyed by the pr view number (the tick). The
// nonce and launch clock are pinned through withReviewSeams, so nothing sleeps
// and nothing depends on the wall clock. A `gh pr merge` call fails the test
// (the harness's rule), so a "zero merge calls" claim is checked on every case.
//
// Lane 3's tick-level tests (babysit_test.go, automerge_test.go) already pin the
// single-tick decisions; what is added here is the multi-tick dimension the
// acceptance criteria name: the first tick after a window ends, a stop and
// re-arm that reloads state, and the priority order across ticks.

import (
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// rwIssue is the issue the fixture PR closes: it carries automerge:ok in the
// harness, so an automerge hold is decided at the review stage or later.
const rwIssue = 782

// rwRun is one `cenci run` call the supervisor made.
type rwRun struct {
	tick     int
	workflow string
	arg      string
}

// rwRuns is the ledger of `cenci run` calls, in call order, attributed to the
// tick (pr view number) that made them.
type rwRuns struct{ entries []rwRun }

// rwTrackRuns wraps the command seam to record every `cenci run <workflow> <arg>`.
// With a non-zero exit, the call itself fails with a real *exec.ExitError of that
// status (exit 3 is "the window was spawned but its setup failed").
func rwTrackRuns(e *ciCapEnv, exit int) *rwRuns {
	r := &rwRuns{}
	base := command
	command = func(name string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "run" {
			r.entries = append(r.entries, rwRun{tick: e.viewN, workflow: args[1], arg: args[2]})
			if exit != 0 {
				return exec.Command("sh", "-c", `printf '%s\n' "$1"; exit "$2"`, "sh", "cenci run: setting automatic-rename off: boom: tmux window already spawned", fmt.Sprint(exit)).CombinedOutput()
			}
		}
		return base(name, args...)
	}
	return r
}

func (r *rwRuns) all() []string {
	var out []string
	for _, e := range r.entries {
		out = append(out, e.workflow)
	}
	return out
}

func (r *rwRuns) at(tick int) []string {
	var out []string
	for _, e := range r.entries {
		if e.tick == tick {
			out = append(out, e.workflow)
		}
	}
	return out
}

// argAt is the single argument of the one workflow launched at tick.
func (r *rwRuns) argAt(t *testing.T, tick int, workflow string) string {
	t.Helper()
	var args []string
	for _, e := range r.entries {
		if e.tick == tick && e.workflow == workflow {
			args = append(args, e.arg)
		}
	}
	if len(args) != 1 {
		t.Fatalf("tick %d: %s launches = %q, want exactly one", tick, workflow, args)
	}
	return args[0]
}

// requireAt asserts the workflows launched at tick, in order (none: no launch).
func (r *rwRuns) requireAt(t *testing.T, tick int, want ...string) {
	t.Helper()
	if got := r.at(tick); !slices.Equal(got, want) {
		t.Errorf("tick %d: cenci run calls = %q, want %q", tick, got, want)
	}
}

// requireNone asserts the supervisor never launched anything.
func (r *rwRuns) requireNone(t *testing.T) {
	t.Helper()
	if got := r.all(); len(got) != 0 {
		t.Errorf("cenci run calls = %q, want none", got)
	}
}

// newRWEnv is the harness for one scenario: the review seams (launch clock and
// the nonces minted in order), a maintainer with write permission, and the
// fleet automerge switch when automerge is true (which also reads the pane
// inventory on every tick).
func newRWEnv(t *testing.T, automerge bool, nonces []string, steps ...ciCapStep) *ciCapEnv {
	t.Helper()
	if automerge {
		withFleetAutomergeEnabled(t, true)
	}
	if len(nonces) == 0 {
		nonces = []string{testReviewNonce}
	}
	withReviewSeams(t, testLaunchAt, nonces...)
	servePermission(t, maintainer, "write")
	return newCICapEnv(t, steps...)
}

// rwStep is one tick: the PR view, the checks and the PR conversation page.
func rwStep(view, checks string, comments ...string) ciCapStep {
	return ciCapStep{view: view, checks: checks, discussion: jsonArray(comments...)}
}

func rwGreen(head string, comments ...string) ciCapStep {
	return rwStep(ciCapOpen(head, rwIssue), ciCapGreen, comments...)
}

func rwFailing(head string, comments ...string) ciCapStep {
	return rwStep(ciCapOpen(head, rwIssue), ciCapFailing, comments...)
}

func rwDirtyFailing(head string, comments ...string) ciCapStep {
	return rwStep(ciCapDirty(head, rwIssue), ciCapFailing, comments...)
}

func rwEntry(id int64, body, outcome string) receiptEntry {
	return receiptEntry{ID: id, Revision: bodyRevision(body), Outcome: outcome}
}

// rwReceiptBy renders a receipt comment as its author, with explicit timestamps.
func rwReceiptBy(id int64, login, nonce, created, updated string, entries ...receiptEntry) string {
	return discussionJSON(id, login, testReceiptBody(nonce, testReceiptHead, entries...), created, updated)
}

// rwReceipt is a well-formed receipt by the maintainer, posted after the launch.
func rwReceipt(id int64, nonce string, entries ...receiptEntry) string {
	return rwReceiptBy(id, maintainer, nonce, afterLaunch, afterLaunch, entries...)
}

func rwPending(t *testing.T, label string, s State, pending, launched []string) {
	t.Helper()
	if !slices.Equal(s.PendingKeys, pending) || !slices.Equal(s.LaunchedKeys, launched) {
		t.Errorf("%s: pending/launched = %v/%v, want %v/%v", label, s.PendingKeys, s.LaunchedKeys, pending, launched)
	}
}

func rwNoMoreThanOneInventoryRead(t *testing.T, e *ciCapEnv, ticks int) {
	t.Helper()
	for tick := 1; tick <= ticks; tick++ {
		if e.inventoryReads[tick] > 1 {
			t.Errorf("tick %d: pane inventory reads = %d, want at most 1", tick, e.inventoryReads[tick])
		}
	}
}

func deadMerge() string { return paneRowOf("42-merge-repair", true, ownedRepoDir) }

func addressReviewRow(dead bool) string { return paneRowOf("42-address-review", dead, ownedRepoDir) }

// -- serialization with the other branch writers ------------------------------

// AC (#1470): a tick with failing CI and a new discussion comment makes exactly
// one `cenci run` (ci-repair); review waits while that window is live; the first
// tick after it ends with no writer live launches exactly one babysit-review.
func TestReviewWorker_FailingCIAndANewCommentLaunchOneWriterPerTick(t *testing.T) {
	e := newRWEnv(t, false, nil,
		rwFailing("head-a", humanComment(11, humanBody)), // 1: ci-repair, review waits
		rwGreen("head-b", humanComment(11, humanBody)),   // 2: fixed, but the ci-repair window is live
		rwGreen("head-b", humanComment(11, humanBody)),   // 3: the window ended
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", liveCI(), deadCI())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "ci-repair")
	runs.requireAt(t, 2)
	runs.requireAt(t, 3, "babysit-review")
	rwPending(t, "after tick 1", e.after(1), []string{"discussion:11"}, nil)
	rwPending(t, "after tick 2", e.after(2), []string{"discussion:11"}, nil)
	rwPending(t, "after tick 3", e.after(3), []string{"discussion:11"}, []string{"discussion:11"})
	if got := e.after(3).ReviewLaunch; got == nil || got.Nonce != testReviewNonce {
		t.Errorf("ReviewLaunch after the launch tick = %+v, want the minted nonce recorded", got)
	}
	rwNoMoreThanOneInventoryRead(t, e, 3)
}

// AC (#1470): a live <pr>-babysit-review pane makes zero launches of ci-repair,
// merge-repair or babysit-review at an unchanged head, after a head change, and
// after a supervisor stop and re-arm that reloads state.
func TestReviewWorker_LiveBabysitReviewPaneSuppressesEveryLaunchAcrossAReload(t *testing.T) {
	e := newRWEnv(t, false, nil,
		rwFailing("head-a", humanComment(11, humanBody), humanComment(13, "and one more")), // 1
		rwFailing("head-a", humanComment(11, humanBody), humanComment(13, "and one more")), // 2: unchanged head
		rwFailing("head-b", humanComment(11, humanBody), humanComment(13, "and one more")), // 3: head change; killed after this save
		rwFailing("head-b", humanComment(11, humanBody), humanComment(13, "and one more")), // 4: re-armed, state reloaded
		rwFailing("head-c", humanComment(11, humanBody), humanComment(13, "and one more")), // 5: head change after the reload
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = func(int) (string, error) { return liveReview(), nil }
	e.seed(outstandingReviewState())

	e.runKilledAtSleep(3)
	reloaded := load(e.path)
	if reloaded.ReviewLaunch == nil || !slices.Equal(reloaded.PendingKeys, []string{"discussion:11", "discussion:13"}) || !slices.Equal(reloaded.LaunchedKeys, []string{"discussion:11"}) {
		t.Fatalf("state reloaded from disk: launch %+v pending %v launched %v, want the outstanding launch and both keys kept", reloaded.ReviewLaunch, reloaded.PendingKeys, reloaded.LaunchedKeys)
	}
	if err := e.run(); err != nil {
		t.Fatalf("re-armed Run: %v", err)
	}

	runs.requireNone(t)
	for _, tick := range []int{1, 3, 5} {
		s := e.after(tick)
		rwPending(t, fmt.Sprintf("after tick %d", tick), s, []string{"discussion:11", "discussion:13"}, []string{"discussion:11"})
		if s.FixAttempts != 0 || s.ConflictFixAttempts != 0 {
			t.Errorf("after tick %d: ciFixAttempts=%d conflictFixAttempts=%d, want no attempt spent while the review window is live", tick, s.FixAttempts, s.ConflictFixAttempts)
		}
	}
	if got := e.after(5).ReviewLaunch; got == nil || got.Nonce != testReviewNonce {
		t.Errorf("ReviewLaunch after the re-arm = %+v, want the outstanding launch kept", got)
	}
}

// AC (#1470): a live review window holds automerge, even when CI is green and
// the discussion has a receipt, and across a stop and re-arm that reloads state.
func TestReviewWorker_LiveWindowHoldsAutomergeEvenWithAReceiptAcrossAReload(t *testing.T) {
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", humanComment(11, humanBody), rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"))), // 1
		rwGreen("head-a", humanComment(11, humanBody), rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"))), // 2: killed after this save
		rwGreen("head-a", humanComment(11, humanBody), rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"))), // 3: re-armed, window still live
		rwGreen("head-a", humanComment(11, humanBody), rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"))), // 4: window ended
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(liveReview(), liveReview(), liveReview(), deadReview())
	e.seed(outstandingReviewState())

	e.runKilledAtSleep(2)
	if err := e.run(); err != nil {
		t.Fatalf("re-armed Run: %v", err)
	}

	runs.requireNone(t)
	for _, tick := range []int{1, 2, 3} {
		s := e.after(tick)
		if s.AutomergeReason != reasonRepairWorkerLive || s.AutomergeDecision != "held" {
			t.Errorf("after tick %d: automerge %q (%q), want held for %q while the window is live", tick, s.AutomergeDecision, s.AutomergeReason, reasonRepairWorkerLive)
		}
		if s.ReviewLaunch == nil {
			t.Errorf("after tick %d: ReviewLaunch cleared while the window is live", tick)
		}
	}
	if s := e.after(4); s.AutomergeReason == reasonRepairWorkerLive || !hasCondition(s.AutomergeConditions, "review", true) || s.ReviewLaunch != nil {
		t.Errorf("after the window ended: reason %q conditions %v launch %+v, want the review stage passed and the launch settled", s.AutomergeReason, s.AutomergeConditions, s.ReviewLaunch)
	}
}

// AC (#1470): a comment created while a review window is live is pending and
// absent from launchedFeedbackKeys; a receipt from the earlier launch does not
// address it (it lists a key outside that launch's key set: unchanged); the first
// tick after the window ends launches babysit-review for it.
func TestReviewWorker_CommentCreatedWhileReviewIsLiveWaitsForTheNextWindow(t *testing.T) {
	const secondNonce = testWrongNonce
	e := newRWEnv(t, true, []string{secondNonce},
		rwGreen("head-a", humanComment(11, humanBody), humanComment(13, "a later ask")), // 1: 13 arrives while the window is live
		rwGreen("head-a", humanComment(11, humanBody), humanComment(13, "a later ask"),
			rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"), rwEntry(13, "a later ask", "addressed"))), // 2: the earlier launch's receipt, still live
		rwGreen("head-a", humanComment(11, humanBody), humanComment(13, "a later ask"),
			rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"), rwEntry(13, "a later ask", "addressed"))), // 3: window ended
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(liveReview(), liveReview(), deadReview())
	e.seed(outstandingReviewState())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	s1 := e.after(1)
	rwPending(t, "after tick 1", s1, []string{"discussion:11", "discussion:13"}, []string{"discussion:11"})
	s2 := e.after(2)
	rwPending(t, "after tick 2", s2, []string{"discussion:13"}, nil)
	if !slices.Equal(s2.AddressedKeys, []string{"discussion:11"}) {
		t.Errorf("after tick 2: addressed = %v, want only discussion:11: the receipt cannot address a key its launch never carried", s2.AddressedKeys)
	}
	if s2.AutomergeReason != reasonRepairWorkerLive {
		t.Errorf("after tick 2: automerge reason %q, want %q", s2.AutomergeReason, reasonRepairWorkerLive)
	}
	runs.requireAt(t, 1)
	runs.requireAt(t, 2)
	runs.requireAt(t, 3, "babysit-review")
	if got, want := runs.argAt(t, 3, "babysit-review"), "42 --receipt-nonce "+secondNonce+" discussion:13="+bodyRevision("a later ask"); got != want {
		t.Errorf("launch argument = %q, want %q: only the new comment, under a fresh nonce", got, want)
	}
	s3 := e.after(3)
	rwPending(t, "after tick 3", s3, []string{"discussion:13"}, []string{"discussion:13"})
	if s3.ReviewLaunch == nil || s3.ReviewLaunch.Nonce != secondNonce || !slices.Equal(s3.ReviewLaunch.Keys, []string{"discussion:13"}) {
		t.Errorf("ReviewLaunch = %+v, want the second launch for discussion:13", s3.ReviewLaunch)
	}
	if s3.ConsumedReviewNonces[testReviewNonce] != 12 {
		t.Errorf("ConsumedReviewNonces = %v, want the first nonce consumed by comment 12", s3.ConsumedReviewNonces)
	}
}

// AC (#1470): a conflict seen while review is live sets conflictRepairHeadSha and
// launches nothing; on the first tick after the window ends the single launch is
// merge-repair, then ci-repair, then babysit-review, one writer per tick.
func TestReviewWorker_ConflictDuringReviewRunsMergeRepairFirstThenTheLowerPriorityWriters(t *testing.T) {
	e := newRWEnv(t, false, nil,
		rwDirtyFailing("head-a", humanComment(11, humanBody)), // 1: review window live
		rwDirtyFailing("head-a", humanComment(11, humanBody)), // 2: still live
		rwDirtyFailing("head-a", humanComment(11, humanBody)), // 3: window ended: merge-repair
		rwFailing("head-a", humanComment(11, humanBody)),      // 4: conflict cleared, merge window ended, CI failing: ci-repair
		rwGreen("head-b", humanComment(11, humanBody)),        // 5: fixed, ci window ended: babysit-review
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(liveReview(), liveReview(), deadReview(), deadMerge(), deadCI())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if s := e.after(1); s.ConflictRepairSHA != "head-a" || s.ConflictFixAttempts != 0 {
		t.Errorf("after tick 1: conflictRepairHeadSha %q attempts %d, want the episode recorded with no attempt spent", s.ConflictRepairSHA, s.ConflictFixAttempts)
	}
	rwPending(t, "after tick 2", e.after(2), []string{"discussion:11"}, nil)
	runs.requireAt(t, 1)
	runs.requireAt(t, 2)
	runs.requireAt(t, 3, "merge-repair")
	runs.requireAt(t, 4, "ci-repair")
	runs.requireAt(t, 5, "babysit-review")
	if want := []string{"merge-repair", "ci-repair", "babysit-review"}; !slices.Equal(runs.all(), want) {
		t.Errorf("cenci run order = %q, want %q", runs.all(), want)
	}
	rwPending(t, "after the merge-repair tick", e.after(3), []string{"discussion:11"}, nil)
	rwPending(t, "after the ci-repair tick", e.after(4), []string{"discussion:11"}, nil)
	rwNoMoreThanOneInventoryRead(t, e, 5)
}

// AC (#1470, edge): a live <pr>-address-review window (human-launched, or
// interactive and launched before the upgrade) blocks every supervisor launch
// and automerge, names the window and says to close it once its edits are
// preserved; the first tick after it ends resumes the launch the PR needs.
func TestReviewWorker_LiveAddressReviewWindowBlocksEverySupervisorLaunch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		step     ciCapStep
		wantNext string
	}{
		{"ci-repair", rwStep(ciCapOpen("head-a", rwIssue), ciCapFailing), "ci-repair"},
		{"merge-repair", rwStep(ciCapDirty("head-a", rwIssue), ciCapFailing), "merge-repair"},
		{"babysit-review", rwStep(ciCapOpen("head-a", rwIssue), ciCapGreen, humanComment(11, humanBody)), "babysit-review"},
		{"automerge only", rwStep(ciCapOpen("head-a", rwIssue), ciCapGreen), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRWEnv(t, true, nil, tc.step, tc.step, tc.step, ciCapMerged(rwIssue))
			runs := rwTrackRuns(e, 0)
			e.panes = panesByTick(addressReviewRow(false), addressReviewRow(false), addressReviewRow(true))
			e.seed(State{Status: "running"})

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			for _, tick := range []int{1, 2} {
				runs.requireAt(t, tick)
				s := e.after(tick)
				if s.AutomergeDecision != "held" {
					t.Errorf("after tick %d: automerge decision %q, want held", tick, s.AutomergeDecision)
				}
				if !strings.Contains(s.AutomergeDetail, "work:42-address-review") || !strings.Contains(s.AutomergeDetail, "close it once its edits are preserved") {
					t.Errorf("after tick %d: detail %q, want it to name work:42-address-review and say to close it once its edits are preserved", tick, s.AutomergeDetail)
				}
				if tc.name == "babysit-review" || tc.name == "automerge only" {
					if s.AutomergeReason != reasonRepairWorkerLive {
						t.Errorf("after tick %d: reason %q, want %q", tick, s.AutomergeReason, reasonRepairWorkerLive)
					}
				}
			}
			if tc.wantNext == "" {
				runs.requireNone(t)
				if s := e.after(3); s.AutomergeReason == reasonRepairWorkerLive || !hasCondition(s.AutomergeConditions, "review", true) {
					t.Errorf("after the window closed: reason %q conditions %v, want the live hold lifted", s.AutomergeReason, s.AutomergeConditions)
				}
				return
			}
			runs.requireAt(t, 3, tc.wantNext)
			if len(runs.all()) != 1 {
				t.Errorf("cenci run calls = %q, want exactly the one after the window closed", runs.all())
			}
		})
	}
}

// -- launch safety -------------------------------------------------------------

// AC (#1470): an unreadable pane inventory makes zero babysit-review calls and
// records `workflow launch failed`; the supervisor retries and launches once the
// inventory reads again.
func TestReviewWorker_UnreadableInventoryLaunchesNothingThenRecovers(t *testing.T) {
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", humanComment(11, humanBody)), // 1: inventory unreadable
		rwGreen("head-a", humanComment(11, humanBody)), // 2: readable
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = func(tick int) (string, error) {
		if tick == 1 {
			return "", errors.New("tmux: lost server")
		}
		return noWorkerInventory, nil
	}
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	failed := e.after(1)
	if failed.AutomergeReason != reasonWorkflowLaunchFailed || failed.Status != "retrying" {
		t.Errorf("after the unreadable tick: reason %q status %q, want %q and retrying", failed.AutomergeReason, failed.Status, reasonWorkflowLaunchFailed)
	}
	rwPending(t, "after the unreadable tick", failed, []string{"discussion:11"}, nil)
	if failed.ReviewLaunch != nil {
		t.Errorf("ReviewLaunch = %+v, want none: nothing was launched", failed.ReviewLaunch)
	}
	runs.requireAt(t, 1)
	runs.requireAt(t, 2, "babysit-review")
	if len(e.sleeps) == 0 || e.sleeps[0] != 120*time.Second {
		t.Errorf("first wait = %v, want the 120s retry backoff", e.sleeps)
	}
	rwPending(t, "after the recovery tick", e.after(2), []string{"discussion:11"}, []string{"discussion:11"})
}

// AC (#1470): `cenci run` exit 3 for babysit-review records the launch as
// dispatched (nonce saved, keys launched), and the next tick, with that window
// live, opens no second one.
func TestReviewWorker_ExitThreeCountsAsDispatched(t *testing.T) {
	e := newRWEnv(t, false, nil,
		rwGreen("head-a", humanComment(11, humanBody)), // 1: exit 3
		rwGreen("head-a", humanComment(11, humanBody)), // 2: the spawned window is live
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 3)
	e.panes = panesByTick("", liveReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v, want a spawned window counted as dispatched", err)
	}

	s := e.after(1)
	rwPending(t, "after the exit-3 tick", s, []string{"discussion:11"}, []string{"discussion:11"})
	if s.ReviewLaunch == nil || s.ReviewLaunch.Nonce != testReviewNonce || !slices.Equal(s.ReviewLaunch.Keys, []string{"discussion:11"}) {
		t.Errorf("ReviewLaunch = %+v, want the nonce and keys recorded", s.ReviewLaunch)
	}
	if want := []string{"babysit-review"}; !slices.Equal(runs.all(), want) {
		t.Errorf("cenci run calls = %q, want exactly one", runs.all())
	}
}

// AC (#1470): a live <pr>-babysit-review pane whose @cenci-repo-dir is provably
// another repository does not block a launch.
func TestReviewWorker_ALiveWindowOfAnotherRepositoryDoesNotBlock(t *testing.T) {
	stubGitCommonDir(t, map[string]string{ownedRepoDir: "/repo/root/.git", "/elsewhere/repo": "/elsewhere/repo/.git"})
	e := newRWEnv(t, false, nil,
		rwGreen("head-a", humanComment(11, humanBody)),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = func(int) (string, error) { return paneRowOf("42-babysit-review", false, "/elsewhere/repo"), nil }
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	rwPending(t, "after the tick", e.after(1), []string{"discussion:11"}, []string{"discussion:11"})
}

// Edge case: a recorded launch session that no longer exists makes zero
// babysit-review calls on every tick, records `workflow launch failed` and keeps
// every key pending; once the session is back the launch goes out.
func TestReviewWorker_AVanishedSessionLaunchesNothingAndKeepsTheKeysPending(t *testing.T) {
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", humanComment(11, humanBody)), // 1: session gone
		rwGreen("head-a", humanComment(11, humanBody)), // 2: session gone
		rwGreen("head-a", humanComment(11, humanBody)), // 3: session back
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.sessionUp = func(attempt int) bool { return attempt >= 3 }
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, tick := range []int{1, 2} {
		s := e.after(tick)
		if s.AutomergeReason != reasonWorkflowLaunchFailed || s.ReviewLaunch != nil {
			t.Errorf("after tick %d: reason %q launch %+v, want %q and nothing recorded", tick, s.AutomergeReason, s.ReviewLaunch, reasonWorkflowLaunchFailed)
		}
		rwPending(t, fmt.Sprintf("after tick %d", tick), s, []string{"discussion:11"}, nil)
		runs.requireAt(t, tick)
	}
	runs.requireAt(t, 3, "babysit-review")
	if e.attempts != 3 {
		t.Errorf("launch attempts = %d, want 3 (two refused, one dispatched)", e.attempts)
	}
}

// -- the worker's end: receipt, or not -----------------------------------------

// AC: a window that ended without a valid receipt leaves its keys pending and
// launched, makes no relaunch for the same revision, holds under
// `review worker ended without receipt` and names `cenci babysit resolve`.
func TestReviewWorker_EndedWithoutAReceiptHoldsAndNeverRelaunchesTheSameRevision(t *testing.T) {
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", humanComment(11, humanBody)), // 1: live
		rwGreen("head-a", humanComment(11, humanBody)), // 2: ended
		rwGreen("head-a", humanComment(11, humanBody)), // 3: still no receipt
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(liveReview(), deadReview(), deadReview())
	e.seed(outstandingReviewState())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if s := e.after(1); s.AutomergeReason != reasonRepairWorkerLive {
		t.Errorf("while live: reason %q, want %q", s.AutomergeReason, reasonRepairWorkerLive)
	}
	for _, tick := range []int{2, 3} {
		s := e.after(tick)
		rwPending(t, fmt.Sprintf("after tick %d", tick), s, []string{"discussion:11"}, []string{"discussion:11"})
		if s.AutomergeReason != reasonReviewWorkerEndedWithoutReceipt || s.AutomergeDecision != "held" {
			t.Errorf("after tick %d: automerge %q (%q), want held for %q", tick, s.AutomergeDecision, s.AutomergeReason, reasonReviewWorkerEndedWithoutReceipt)
		}
		if !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
			t.Errorf("after tick %d: detail %q, want it to name `cenci babysit resolve 42 discussion:11`", tick, s.AutomergeDetail)
		}
		if len(s.AddressedKeys) != 0 {
			t.Errorf("after tick %d: addressed = %v, want none: an exit is not evidence", tick, s.AddressedKeys)
		}
	}
	runs.requireNone(t)
}

// AC: a valid receipt moves exactly the keys it lists with outcome addressed or
// no-action into addressedCommentKeys with the per-key record and the consumed
// nonce. A receipt posted while its pane is still live addresses the key but
// automerge stays held `branch repair worker still running` until the pane ends.
func TestReviewWorker_ValidReceiptAddressesTheKeyAndAutomergeWaitsForThePaneToEnd(t *testing.T) {
	for _, outcome := range []string{"addressed", "no-action"} {
		t.Run(outcome, func(t *testing.T) {
			page := []string{humanComment(11, humanBody), rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, outcome))}
			e := newRWEnv(t, true, nil,
				rwGreen("head-a", page...), // 1: receipt posted, pane live
				rwGreen("head-a", page...), // 2: pane ended
				rwGreen("head-a", page...), // 3: quiet
				ciCapMerged(rwIssue),
			)
			runs := rwTrackRuns(e, 0)
			e.panes = panesByTick(liveReview(), deadReview(), deadReview())
			e.seed(outstandingReviewState())

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			wantReceipt := map[string]discussionReceipt{"discussion:11": {CommentID: 11, Revision: bodyRevision(humanBody), HeadSHA: testReceiptHead, Outcome: outcome, ReceiptCommentID: 12, Source: receiptSourceReceipt}}
			for _, tick := range []int{1, 2, 3} {
				s := e.after(tick)
				if len(s.PendingKeys) != 0 || !slices.Equal(s.AddressedKeys, []string{"discussion:11"}) || len(s.LaunchedKeys) != 0 {
					t.Errorf("after tick %d: pending/addressed/launched = %v/%v/%v, want discussion:11 addressed only", tick, s.PendingKeys, s.AddressedKeys, s.LaunchedKeys)
				}
				if got := s.DiscussionReceipts; len(got) != 1 || got["discussion:11"] != wantReceipt["discussion:11"] {
					t.Errorf("after tick %d: DiscussionReceipts = %+v, want %+v", tick, got, wantReceipt)
				}
				if got := s.ConsumedReviewNonces; len(got) != 1 || got[testReviewNonce] != 12 {
					t.Errorf("after tick %d: ConsumedReviewNonces = %v, want the nonce consumed by comment 12", tick, got)
				}
			}
			if s := e.after(1); s.AutomergeReason != reasonRepairWorkerLive || s.ReviewLaunch == nil {
				t.Errorf("receipt while live: reason %q launch %+v, want %q with the launch kept", s.AutomergeReason, s.ReviewLaunch, reasonRepairWorkerLive)
			}
			for _, tick := range []int{2, 3} {
				if s := e.after(tick); s.AutomergeReason == reasonRepairWorkerLive || !hasCondition(s.AutomergeConditions, "review", true) || s.ReviewLaunch != nil {
					t.Errorf("after tick %d: reason %q conditions %v launch %+v, want the review stage passed once the pane ended", tick, s.AutomergeReason, s.AutomergeConditions, s.ReviewLaunch)
				}
			}
			runs.requireNone(t)
		})
	}
}

// AC: a blocked or failed receipt keeps the key pending and launched (no
// relaunch for the same revision), sets needs-input every tick and keeps
// automerge held, naming `cenci babysit resolve`.
func TestReviewWorker_BlockedOrFailedReceiptKeepsTheKeyPendingNeedsInputAndHeld(t *testing.T) {
	for _, outcome := range []string{"blocked", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			page := []string{humanComment(11, humanBody), rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, outcome))}
			e := newRWEnv(t, true, nil,
				rwGreen("head-a", page...), // 1: live
				rwGreen("head-a", page...), // 2: ended
				rwGreen("head-a", page...), // 3: ended
				ciCapMerged(rwIssue),
			)
			runs := rwTrackRuns(e, 0)
			e.panes = panesByTick(liveReview(), deadReview(), deadReview())
			e.seed(outstandingReviewState())

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			for _, tick := range []int{1, 2, 3} {
				s := e.after(tick)
				rwPending(t, fmt.Sprintf("after tick %d", tick), s, []string{"discussion:11"}, []string{"discussion:11"})
				if s.Status != "needs-input" || s.AutomergeDecision != "held" {
					t.Errorf("after tick %d: status %q automerge %q, want needs-input and held", tick, s.Status, s.AutomergeDecision)
				}
				if !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
					t.Errorf("after tick %d: detail %q, want it to name `cenci babysit resolve 42 discussion:11`", tick, s.AutomergeDetail)
				}
				want := discussionReceipt{CommentID: 11, Revision: bodyRevision(humanBody), HeadSHA: testReceiptHead, Outcome: outcome, ReceiptCommentID: 12, Source: receiptSourceReceipt}
				if got := s.DiscussionReceipts["discussion:11"]; got != want {
					t.Errorf("after tick %d: receipt record = %+v, want %+v", tick, got, want)
				}
				if len(s.AddressedKeys) != 0 {
					t.Errorf("after tick %d: addressed = %v, want none", tick, s.AddressedKeys)
				}
			}
			if s := e.after(2); s.AutomergeReason != reasonReviewPending {
				t.Errorf("after the window ended: reason %q, want %q", s.AutomergeReason, reasonReviewPending)
			}
			runs.requireNone(t)
		})
	}
}

// AC and edge cases: none of a banner-first comment, a wrong, never-minted or
// already-used nonce, a receipt from an author without write permission, a
// receipt created before the launch or an edited receipt marks a key addressed
// or launches anything; every receipt-shaped rejection is recorded.
func TestReviewWorker_RejectedReceiptsAddressNothingAndLaunchNothing(t *testing.T) {
	const neverMinted = "ffffffffffffffffffffffffffffffff"
	const priorLaunchEnd = "2026-11-10T11:59:59Z"
	entry := rwEntry(11, humanBody, "addressed")
	banner := "> 🤖 **cenci** — review reply posted by `/cenci:address-review` (posting replies).\n\nI addressed it"
	for _, tc := range []struct {
		name          string
		comment       string
		consumed      map[string]int64
		wantRejection string
	}{
		{"a banner-first comment without a receipt", discussionJSON(12, maintainer, banner, afterLaunch, afterLaunch), nil, ""},
		{"a wrong nonce", rwReceipt(12, testWrongNonce, entry), nil, receiptRejection{CommentID: 12, Reason: receiptRejectNonceUnknown}.String()},
		{"a never-minted nonce", rwReceipt(12, neverMinted, entry), nil, receiptRejection{CommentID: 12, Reason: receiptRejectNonceUnknown}.String()},
		{"an already-used nonce", rwReceipt(12, testWrongNonce, entry), map[string]int64{testWrongNonce: 99}, receiptRejection{CommentID: 12, Reason: receiptRejectNonceConsumed}.String()},
		{"an author without write permission", rwReceiptBy(12, "stranger", testReviewNonce, afterLaunch, afterLaunch, entry), nil, receiptRejection{CommentID: 12, Reason: receiptRejectNoWritePermission}.String()},
		{"a receipt created before the launch", rwReceiptBy(12, maintainer, testReviewNonce, priorLaunchEnd, priorLaunchEnd, entry), nil, receiptRejection{CommentID: 12, Reason: receiptRejectBeforeLaunch}.String()},
		{"an edited receipt", rwReceiptBy(12, maintainer, testReviewNonce, afterLaunch, "2026-11-10T12:00:09Z", entry), nil, receiptRejection{CommentID: 12, Reason: receiptRejectEdited}.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := []string{humanComment(11, humanBody), tc.comment}
			e := newRWEnv(t, true, nil,
				rwGreen("head-a", page...), // 1: live
				rwGreen("head-a", page...), // 2: ended
				rwGreen("head-a", page...), // 3: ended
				ciCapMerged(rwIssue),
			)
			runs := rwTrackRuns(e, 0)
			e.panes = panesByTick(liveReview(), deadReview(), deadReview())
			seed := outstandingReviewState()
			seed.ConsumedReviewNonces = tc.consumed
			e.seed(seed)

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			for _, tick := range []int{1, 2, 3} {
				s := e.after(tick)
				rwPending(t, fmt.Sprintf("after tick %d", tick), s, []string{"discussion:11"}, []string{"discussion:11"})
				if len(s.AddressedKeys) != 0 || len(s.DiscussionReceipts) != 0 {
					t.Errorf("after tick %d: addressed %v receipts %+v, want nothing recorded as addressed", tick, s.AddressedKeys, s.DiscussionReceipts)
				}
				if _, consumed := s.ConsumedReviewNonces[testReviewNonce]; consumed {
					t.Errorf("after tick %d: the outstanding nonce was consumed by a rejected comment", tick)
				}
				if tc.wantRejection != "" && !strings.Contains(s.AutomergeDetail, tc.wantRejection) {
					t.Errorf("after tick %d: detail %q, want the rejection %q", tick, s.AutomergeDetail, tc.wantRejection)
				}
				if tc.wantRejection == "" && strings.Contains(s.AutomergeDetail, "rejected") {
					t.Errorf("after tick %d: detail %q records a rejection for a comment that is not a receipt", tick, s.AutomergeDetail)
				}
			}
			if s := e.after(3); s.AutomergeReason != reasonReviewWorkerEndedWithoutReceipt || s.AutomergeDecision != "held" {
				t.Errorf("after the window ended: automerge %q (%q), want held for %q", s.AutomergeDecision, s.AutomergeReason, reasonReviewWorkerEndedWithoutReceipt)
			}
			runs.requireNone(t)
		})
	}
}

// Edge case: a nonce authorises one receipt. Once a receipt consumed it, a second
// comment carrying the same nonce -- a replay, here listing the key the first
// receipt left out -- addresses nothing, and the rejection is recorded.
func TestReviewWorker_AReplayedNonceAddressesNothingMore(t *testing.T) {
	first := rwReceipt(21, testReviewNonce, rwEntry(11, humanBody, "addressed"))
	replay := rwReceipt(22, testReviewNonce, rwEntry(12, "second ask", "addressed"))
	page := []string{humanComment(11, humanBody), humanComment(12, "second ask")}
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", append(slices.Clone(page), first)...),         // 1: the first receipt, window live
		rwGreen("head-a", append(slices.Clone(page), first, replay)...), // 2: a replay of its nonce, window live
		rwGreen("head-a", append(slices.Clone(page), first, replay)...), // 3: window ended
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(liveReview(), liveReview(), deadReview())
	seed := outstandingReviewState()
	seed.PendingKeys = []string{"discussion:11", "discussion:12"}
	seed.LaunchedKeys = []string{"discussion:11", "discussion:12"}
	seed.DiscussionRevisions["discussion:12"] = bodyRevision("second ask")
	seed.ReviewLaunch.Keys = []string{"discussion:11", "discussion:12"}
	// Both authors are trusted, so the #1539 head-move check has nothing to hold.
	seed.ReviewLaunch.TrustedKeys = []string{"discussion:11", "discussion:12"}
	e.seed(seed)

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, tick := range []int{1, 2, 3} {
		s := e.after(tick)
		if !slices.Equal(s.AddressedKeys, []string{"discussion:11"}) || !slices.Equal(s.PendingKeys, []string{"discussion:12"}) {
			t.Errorf("after tick %d: addressed/pending = %v/%v, want only discussion:11 addressed", tick, s.AddressedKeys, s.PendingKeys)
		}
		if s.ConsumedReviewNonces[testReviewNonce] != 21 {
			t.Errorf("after tick %d: ConsumedReviewNonces = %v, want the nonce consumed by comment 21, never by the replay", tick, s.ConsumedReviewNonces)
		}
	}
	want := receiptRejection{CommentID: 22, Reason: receiptRejectNonceConsumed}.String()
	for _, tick := range []int{2, 3} {
		if s := e.after(tick); !strings.Contains(s.AutomergeDetail, want) {
			t.Errorf("after tick %d: detail %q, want the rejection %q", tick, s.AutomergeDetail, want)
		}
	}
	if s := e.after(3); s.AutomergeReason != reasonReviewWorkerEndedWithoutReceipt {
		t.Errorf("after the window ended: reason %q, want %q for the key the receipt left out", s.AutomergeReason, reasonReviewWorkerEndedWithoutReceipt)
	}
	runs.requireNone(t)
}

// Edge case: a receipt whose revision differs from the comment's current body
// hash (edited after launch) leaves the key pending, and a new episode launches,
// at the new revision under a fresh nonce, once the worker ends.
func TestReviewWorker_ReceiptForAnEditedCommentLeavesItPendingAndAnotherEpisodeLaunches(t *testing.T) {
	const edited = "please also cover the empty input, and the nil one"
	page := []string{humanComment(11, edited), rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"))}
	e := newRWEnv(t, true, []string{testWrongNonce},
		rwGreen("head-a", page...), // 1: live; the receipt describes the old revision
		rwGreen("head-a", page...), // 2: ended: a new episode
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(liveReview(), deadReview())
	e.seed(outstandingReviewState())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := e.after(1)
	rwPending(t, "after tick 1", first, []string{"discussion:11"}, nil)
	if len(first.AddressedKeys) != 0 {
		t.Errorf("after tick 1: addressed = %v, want none: the receipt's revision is stale", first.AddressedKeys)
	}
	runs.requireAt(t, 1)
	runs.requireAt(t, 2, "babysit-review")
	if got, want := runs.argAt(t, 2, "babysit-review"), "42 --receipt-nonce "+testWrongNonce+" discussion:11="+bodyRevision(edited); got != want {
		t.Errorf("launch argument = %q, want %q", got, want)
	}
	second := e.after(2)
	rwPending(t, "after tick 2", second, []string{"discussion:11"}, []string{"discussion:11"})
	if second.ReviewLaunch == nil || second.ReviewLaunch.Nonce != testWrongNonce {
		t.Errorf("ReviewLaunch = %+v, want the new episode's launch", second.ReviewLaunch)
	}
}

// Edge case: a human comment that quotes a receipt marker below its first line is
// pending feedback, not a receipt: it consumes no nonce, addresses nothing and
// is itself launched to the worker once the window ends.
func TestReviewWorker_AHumanCommentQuotingAReceiptMarkerIsFeedbackNotAReceipt(t *testing.T) {
	servePermission(t, "alice", "admin")
	quoted := "this is odd:\n> <!-- cenci-review-receipt:" + testReviewNonce + " -->\n> head: " + testReceiptHead +
		"\n> - discussion:11 revision=" + bodyRevision(humanBody) + " outcome=addressed"
	page := []string{humanComment(11, humanBody), humanComment(21, quoted)}
	e := newRWEnv(t, true, []string{testWrongNonce},
		rwGreen("head-a", page...), // 1: live
		rwGreen("head-a", page...), // 2: ended
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(liveReview(), deadReview())
	e.seed(outstandingReviewState())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := e.after(1)
	rwPending(t, "after tick 1", first, []string{"discussion:11", "discussion:21"}, []string{"discussion:11"})
	if len(first.AddressedKeys) != 0 || len(first.ConsumedReviewNonces) != 0 || len(first.DiscussionReceipts) != 0 {
		t.Errorf("after tick 1: addressed %v consumed %v receipts %+v, want the quote to be no receipt", first.AddressedKeys, first.ConsumedReviewNonces, first.DiscussionReceipts)
	}
	if strings.Contains(first.AutomergeDetail, "rejected") {
		t.Errorf("detail %q records a rejection: a quoted marker is not a receipt-shaped comment", first.AutomergeDetail)
	}
	runs.requireAt(t, 1)
	runs.requireAt(t, 2, "babysit-review")
	if got := runs.argAt(t, 2, "babysit-review"); !strings.Contains(got, "discussion:21="+bodyRevision(quoted)) || strings.Contains(got, "discussion:11=") {
		t.Errorf("launch argument = %q, want only the quoting comment, discussion:21", got)
	}
}

// -- ingestion across ticks ------------------------------------------------------

// Edge cases: a PR with zero discussion comments and no other feedback is a
// quiet tick (no launch, no discussion hold, the poll backs off); `[bot]` logins
// and cenci-banner comments never become pending.
func TestReviewWorker_NoHumanDiscussionIsAQuietTickWithNoHold(t *testing.T) {
	bot := discussionJSON(31, "dependabot[bot]", "bump the lockfile", "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z")
	banner := discussionJSON(32, "alice", "> 🤖 **cenci** — review reply posted by `/cenci:address-review` (posting replies).\n\nre-checking", "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z")
	e := newRWEnv(t, true, nil,
		rwGreen("head-a"),              // 1: no comments at all
		rwGreen("head-a", bot, banner), // 2: only excluded comments
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	for _, tick := range []int{1, 2} {
		s := e.after(tick)
		if len(s.PendingKeys) != 0 || len(s.DiscussionRevisions) != 0 || s.ReviewLaunch != nil {
			t.Errorf("after tick %d: pending %v revisions %v launch %+v, want nothing tracked", tick, s.PendingKeys, s.DiscussionRevisions, s.ReviewLaunch)
		}
		if !hasCondition(s.AutomergeConditions, "review", true) {
			t.Errorf("after tick %d: reason %q conditions %v, want the review stage passed (no discussion hold)", tick, s.AutomergeReason, s.AutomergeConditions)
		}
	}
	if want := []time.Duration{120 * time.Second, 240 * time.Second}; !slices.Equal(e.sleeps, want) {
		t.Errorf("waits = %v, want %v: quiet ticks back off", e.sleeps, want)
	}
}

// Edge case: a pending discussion comment that was deleted holds with `review
// feedback state unknown`, tick after tick, across a stop, until `cenci babysit
// resolve` clears it; the re-armed supervisor then passes the review stage.
func TestReviewWorker_DeletedPendingCommentHoldsUntilResolveClearsItAcrossAStop(t *testing.T) {
	e := newRWEnv(t, true, nil,
		rwGreen("head-a"), // 1: comment 11 was deleted
		rwGreen("head-a"), // 2: still deleted; the supervisor is stopped after this save
		rwGreen("head-a"), // 3: re-armed after `resolve`
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(outstandingReviewState())

	e.runKilledAtSleep(2)
	// Tick 1's state was captured at tick 2's read; tick 2's is the file on disk.
	for label, s := range map[string]State{"tick 1": e.after(1), "tick 2": load(e.path)} {
		if s.AutomergeReason != reasonReviewStateUnknown || !slices.Equal(s.PendingKeys, []string{"discussion:11"}) {
			t.Errorf("after %s: reason %q pending %v, want %q with the key pending", label, s.AutomergeReason, s.PendingKeys, reasonReviewStateUnknown)
		}
	}

	// The supervisor is gone; the human stops it, resolves the key and re-arms.
	stubProcessOwned(t, false)
	stubProcessGone(t, true)
	if err := StopRepo("o/r", "42", e.dir); err != nil {
		t.Fatalf("StopRepo: %v", err)
	}
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
	t.Setenv("CENCI_ATTENDED", "")
	t.Setenv("CENCI_ATTENDED_ORIGIN", "")
	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
	if err := e.run(); err != nil {
		t.Fatalf("re-armed Run: %v", err)
	}

	cleared := e.after(3)
	if cleared.AutomergeReason == reasonReviewStateUnknown || len(cleared.PendingKeys) != 0 || !slices.Equal(cleared.AddressedKeys, []string{"discussion:11"}) {
		t.Errorf("after the resolve: reason %q pending %v addressed %v, want the hold cleared and the key addressed", cleared.AutomergeReason, cleared.PendingKeys, cleared.AddressedKeys)
	}
	if got := cleared.DiscussionReceipts["discussion:11"]; got.Source != receiptSourceHumanCLI || got.Outcome != receiptOutcomeAddressed {
		t.Errorf("receipt record = %+v, want a human-cli addressed record", got)
	}
	if !hasCondition(cleared.AutomergeConditions, "review", true) || cleared.ReviewLaunch != nil {
		t.Errorf("conditions %v launch %+v, want the review stage passed and the settled launch cleared", cleared.AutomergeConditions, cleared.ReviewLaunch)
	}
	runs.requireNone(t)
}

// -- author trust: the unattended worker never acts on a comment from an author
// without write permission (#1472 review-fix) ------------------------------------

// Security: a launch lists each comment author's repository permission. A key whose
// author the lookup definitely denies (not `admin`/`write`) carries `:untrusted` in
// the launch argument and is absent from the launch's trust record (TrustedKeys,
// the allow-list, which survives the save and reload between ticks); a trusted
// author's token has no suffix and is in the record.
func TestReviewWorker_LaunchMarksAuthorsWithoutWriteAccessUntrusted(t *testing.T) {
	servePermission(t, "stranger", "read")
	const second, third = "and one more", "a third ask"
	e := newRWEnv(t, false, nil,
		rwGreen("head-a", humanCommentBy(11, "stranger", humanBody), humanComment(13, second), humanCommentBy(15, "stranger", third)),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := "42 --receipt-nonce " + testReviewNonce +
		" discussion:11=" + bodyRevision(humanBody) + ":untrusted" +
		" discussion:13=" + bodyRevision(second) +
		" discussion:15=" + bodyRevision(third) + ":untrusted"
	if got := runs.argAt(t, 1, "babysit-review"); got != want {
		t.Errorf("launch argument = %q, want %q", got, want)
	}
	// e.after reads the state file from disk: the field survives save and reload.
	launched := e.after(1).ReviewLaunch
	if launched == nil || !slices.Equal(launched.Keys, []string{"discussion:11", "discussion:13", "discussion:15"}) || !slices.Equal(launched.TrustedKeys, []string{"discussion:13"}) {
		t.Errorf("ReviewLaunch = %+v, want all three keys and trusted [discussion:13]", launched)
	}
}

// A failed author lookup at launch (a gh error, a timeout) is not a denial: the key
// is skipped for that tick -- no launch, no nonce minted, the key stays pending and
// unlaunched, and the detail says the lookup will be retried -- and the next tick's
// answered lookup launches it with the same nonce and no `:untrusted` suffix.
func TestReviewWorker_FailedAuthorLookupAtLaunchIsRetriedNotMarkedUntrusted(t *testing.T) {
	servePermissionFailure(t, humanLogin)
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", humanComment(11, humanBody)), // 1: the lookup fails
		rwGreen("head-a", humanComment(11, humanBody)), // 2: the lookup answers
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = func(tick int) (string, error) {
		if tick == 2 {
			delete(permissionFailing, humanLogin)
		}
		return noWorkerInventory, nil
	}
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := e.after(1)
	rwPending(t, "after tick 1", first, []string{"discussion:11"}, nil)
	runs.requireAt(t, 1)
	if first.ReviewLaunch != nil || len(first.ConsumedReviewNonces) != 0 {
		t.Errorf("after tick 1: ReviewLaunch %+v consumed %v, want no launch and no nonce spent", first.ReviewLaunch, first.ConsumedReviewNonces)
	}
	if want := "author permission lookup failed for discussion:11; will retry"; !strings.Contains(first.AutomergeDetail, want) {
		t.Errorf("after tick 1: detail %q, want %q", first.AutomergeDetail, want)
	}
	if first.AutomergeDecision != "held" || first.AutomergeReason != reasonReviewPending {
		t.Errorf("after tick 1: automerge %q (%q), want held by the pending key", first.AutomergeDecision, first.AutomergeReason)
	}
	for _, bad := range []string{"lacks write permission", "gh:"} {
		if strings.Contains(first.AutomergeDetail, bad) {
			t.Errorf("after tick 1: detail %q contains %q for a lookup that merely failed", first.AutomergeDetail, bad)
		}
	}

	runs.requireAt(t, 2, "babysit-review")
	if got, want := runs.argAt(t, 2, "babysit-review"), "42 --receipt-nonce "+testReviewNonce+" discussion:11="+bodyRevision(humanBody); got != want {
		t.Errorf("launch argument = %q, want %q (no :untrusted suffix once the lookup answers)", got, want)
	}
	second := e.after(2)
	rwPending(t, "after tick 2", second, []string{"discussion:11"}, []string{"discussion:11"})
	if second.ReviewLaunch == nil || !slices.Equal(second.ReviewLaunch.TrustedKeys, []string{"discussion:11"}) {
		t.Errorf("after tick 2: ReviewLaunch = %+v, want discussion:11 recorded as trusted", second.ReviewLaunch)
	}
}

// With two candidate keys and a failed lookup for only one author, the launch
// carries just the other key; the skipped key stays pending and unlaunched and is
// launched on its own once its lookup answers.
func TestReviewWorker_FailedLookupForOneKeyLaunchesTheOtherAndRetriesTheSkippedOne(t *testing.T) {
	servePermissionFailure(t, "flaky")
	const second = "and one more"
	e := newRWEnv(t, false, []string{testReviewNonce, testWrongNonce},
		rwGreen("head-a", humanCommentBy(11, "flaky", humanBody), humanComment(13, second)), // 1: only 13 is launched
		rwGreen("head-a", humanCommentBy(11, "flaky", humanBody), humanComment(13, second)), // 2: the lookup answers
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	servePermission(t, "flaky", "write")
	e.panes = func(tick int) (string, error) {
		if tick == 2 {
			delete(permissionFailing, "flaky")
		}
		return noWorkerInventory, nil
	}
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	if got, want := runs.argAt(t, 1, "babysit-review"), "42 --receipt-nonce "+testReviewNonce+" discussion:13="+bodyRevision(second); got != want {
		t.Errorf("tick 1 launch argument = %q, want only the key whose author lookup answered: %q", got, want)
	}
	first := e.after(1)
	rwPending(t, "after tick 1", first, []string{"discussion:11", "discussion:13"}, []string{"discussion:13"})
	if first.ReviewLaunch == nil || !slices.Equal(first.ReviewLaunch.Keys, []string{"discussion:13"}) || !slices.Equal(first.ReviewLaunch.TrustedKeys, []string{"discussion:13"}) {
		t.Errorf("after tick 1: ReviewLaunch = %+v, want only discussion:13, trusted", first.ReviewLaunch)
	}
	runs.requireAt(t, 2, "babysit-review")
	if got, want := runs.argAt(t, 2, "babysit-review"), "42 --receipt-nonce "+testWrongNonce+" discussion:11="+bodyRevision(humanBody); got != want {
		t.Errorf("tick 2 launch argument = %q, want the skipped key, trusted: %q", got, want)
	}
}

// Default-deny across a stop and re-arm: the launch's trust record is saved with
// the launch, survives `stop` and the reload, and keeps governing the receipt. A
// key whose author was denied stays refused (`addressed` blocks it) even though a
// trusted key in the same receipt is addressed.
func TestReviewWorker_TrustRecordSurvivesAReloadAndAStopAndReArm(t *testing.T) {
	servePermission(t, "stranger", "read")
	const second = "second ask"
	page := []string{
		humanCommentBy(11, "stranger", humanBody),
		humanComment(12, second),
	}
	withReceipt := append(slices.Clone(page), rwReceipt(21, testReviewNonce, rwEntry(11, humanBody, "addressed"), rwEntry(12, second, "addressed")))
	e := newRWEnv(t, false, nil,
		rwGreen("head-a", page...),        // 1: launch; killed after this save
		rwGreen("head-a", withReceipt...), // 2: re-armed, the receipt is verified
		rwGreen("head-a", withReceipt...), // 3: still held
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	e.runKilledAtSleep(1)
	reloaded := load(e.path)
	if reloaded.ReviewLaunch == nil || !slices.Equal(reloaded.ReviewLaunch.Keys, []string{"discussion:11", "discussion:12"}) || !slices.Equal(reloaded.ReviewLaunch.TrustedKeys, []string{"discussion:12"}) {
		t.Fatalf("state reloaded from disk: launch %+v, want both keys and the trust record [discussion:12]", reloaded.ReviewLaunch)
	}

	stubProcessOwned(t, false)
	stubProcessGone(t, true)
	if err := StopRepo("o/r", "42", e.dir); err != nil {
		t.Fatalf("StopRepo: %v", err)
	}
	if stopped := load(e.path); stopped.ReviewLaunch == nil || !slices.Equal(stopped.ReviewLaunch.TrustedKeys, []string{"discussion:12"}) {
		t.Fatalf("stopped state: launch %+v, want the trust record kept", stopped.ReviewLaunch)
	}
	if err := e.run(); err != nil {
		t.Fatalf("re-armed Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	runs.requireAt(t, 2)
	s := e.after(2)
	rwPending(t, "after the re-arm", s, []string{"discussion:11"}, []string{"discussion:11"})
	if !slices.Equal(s.AddressedKeys, []string{"discussion:12"}) || s.Status != "needs-input" {
		t.Errorf("after the re-arm: addressed %v status %q, want only the trusted discussion:12 addressed and needs-input", s.AddressedKeys, s.Status)
	}
}

// Default-deny: a launch recorded with discussion keys and no trust record (a state
// saved by an earlier build, hand-edited or truncated) is treated as untrusted even
// though the author's permission would answer `write` today: `addressed` is refused,
// the key stays pending and held for a human `resolve`.
func TestReviewWorker_AddressedReceiptIsRefusedForALaunchWithNoTrustRecord(t *testing.T) {
	page := []string{
		humanComment(11, humanBody),
		rwReceipt(21, testReviewNonce, rwEntry(11, humanBody, "addressed")),
	}
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", page...),
		rwGreen("head-a", page...),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(deadReview(), deadReview())
	seed := outstandingReviewState()
	seed.ReviewLaunch.TrustedKeys = nil
	e.seed(seed)

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, tick := range []int{1, 2} {
		s := e.after(tick)
		rwPending(t, fmt.Sprintf("after tick %d", tick), s, []string{"discussion:11"}, []string{"discussion:11"})
		if len(s.AddressedKeys) != 0 || s.Status != "needs-input" || s.AutomergeDecision != "held" {
			t.Errorf("after tick %d: addressed %v status %q automerge %q, want nothing addressed, needs-input and held", tick, s.AddressedKeys, s.Status, s.AutomergeDecision)
		}
		if !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
			t.Errorf("after tick %d: detail %q, want it to name `cenci babysit resolve 42 discussion:11`", tick, s.AutomergeDetail)
		}
	}
	runs.requireNone(t)
}

// An `addressed` receipt for an untrusted key is refused: the key stays pending and
// launched, the status is needs-input, the hold names `cenci babysit resolve` and a
// note says why; a trusted key in the same receipt is addressed. The refusal is
// durable -- the key never moves on a later tick, and nothing is relaunched.
func TestReviewWorker_AddressedReceiptForAnUntrustedKeyLeavesItPendingNeedsInputAndHeld(t *testing.T) {
	servePermission(t, "stranger", "read")
	const second = "second ask"
	page := []string{
		humanCommentBy(11, "stranger", humanBody),
		humanComment(12, second),
		rwReceipt(21, testReviewNonce, rwEntry(11, humanBody, "addressed"), rwEntry(12, second, "addressed")),
	}
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", page...), // 1: the receipt, window live
		rwGreen("head-a", page...), // 2: window ended
		rwGreen("head-a", page...), // 3: still held
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(liveReview(), deadReview(), deadReview())
	seed := outstandingReviewState()
	seed.PendingKeys = []string{"discussion:11", "discussion:12"}
	seed.LaunchedKeys = []string{"discussion:11", "discussion:12"}
	seed.DiscussionRevisions["discussion:12"] = bodyRevision(second)
	seed.ReviewLaunch.Keys = []string{"discussion:11", "discussion:12"}
	seed.ReviewLaunch.TrustedKeys = []string{"discussion:12"}
	e.seed(seed)

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, tick := range []int{1, 2, 3} {
		s := e.after(tick)
		rwPending(t, fmt.Sprintf("after tick %d", tick), s, []string{"discussion:11"}, []string{"discussion:11"})
		if !slices.Equal(s.AddressedKeys, []string{"discussion:12"}) {
			t.Errorf("after tick %d: addressed = %v, want only the trusted discussion:12", tick, s.AddressedKeys)
		}
		if s.Status != "needs-input" || s.AutomergeDecision != "held" {
			t.Errorf("after tick %d: status %q automerge %q, want needs-input and held", tick, s.Status, s.AutomergeDecision)
		}
		if !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
			t.Errorf("after tick %d: detail %q, want it to name `cenci babysit resolve 42 discussion:11`", tick, s.AutomergeDetail)
		}
		if s.ConsumedReviewNonces[testReviewNonce] != 21 {
			t.Errorf("after tick %d: ConsumedReviewNonces = %v, want the authentic receipt to consume its nonce", tick, s.ConsumedReviewNonces)
		}
	}
	if s := e.after(1); !strings.Contains(s.AutomergeDetail, "discussion:11: its comment author is not recorded as having write permission") {
		t.Errorf("the tick that consumed the receipt: detail %q, want a note that discussion:11's author is not recorded as having write permission", s.AutomergeDetail)
	}
	runs.requireNone(t)
}

// `no-action` is accepted for an untrusted key: the comment requests nothing, so the
// key is addressed with the receipt's evidence.
func TestReviewWorker_NoActionReceiptForAnUntrustedKeyIsAccepted(t *testing.T) {
	servePermission(t, "stranger", "read")
	page := []string{
		humanCommentBy(11, "stranger", humanBody),
		rwReceipt(21, testReviewNonce, rwEntry(11, humanBody, "no-action")),
	}
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", page...),
		ciCapMerged(rwIssue),
	)
	rwTrackRuns(e, 0)
	seed := outstandingReviewState()
	seed.ReviewLaunch.TrustedKeys = nil
	e.seed(seed)

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := e.after(1)
	if len(s.PendingKeys) != 0 || !slices.Equal(s.AddressedKeys, []string{"discussion:11"}) || s.DiscussionReceipts["discussion:11"].Outcome != receiptOutcomeNoAction {
		t.Errorf("pending/addressed/receipt = %v/%v/%+v, want discussion:11 addressed as no-action", s.PendingKeys, s.AddressedKeys, s.DiscussionReceipts["discussion:11"])
	}
}

// A failed permission lookup for the receipt's author is not a denial: the receipt
// is rejected under its own reason (fail closed, so nothing is addressed), the
// nonce stays outstanding, and the next tick's successful lookup accepts the very
// same receipt.
func TestReviewWorker_FailedPermissionLookupIsReportedAsSuchAndRetriedNextTick(t *testing.T) {
	servePermissionFailure(t, maintainer)
	page := []string{humanComment(11, humanBody), rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"))}
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", page...), // 1: the lookup fails
		rwGreen("head-a", page...), // 2: the lookup answers
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = func(tick int) (string, error) {
		if tick == 1 {
			delete(permissionFailing, maintainer)
		}
		return liveReview(), nil
	}
	e.seed(outstandingReviewState())

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := e.after(1)
	rwPending(t, "after tick 1", first, []string{"discussion:11"}, []string{"discussion:11"})
	wantRejection := receiptRejection{CommentID: 12, Reason: receiptRejectPermissionUnknown}.String()
	if !strings.Contains(first.AutomergeDetail, wantRejection) {
		t.Errorf("after tick 1: detail %q, want %q", first.AutomergeDetail, wantRejection)
	}
	if strings.Contains(first.AutomergeDetail, string(receiptRejectNoWritePermission)) {
		t.Errorf("after tick 1: detail %q reports a denial for a lookup that merely failed", first.AutomergeDetail)
	}
	if len(first.ConsumedReviewNonces) != 0 {
		t.Errorf("after tick 1: ConsumedReviewNonces = %v, want the nonce kept outstanding", first.ConsumedReviewNonces)
	}
	second := e.after(2)
	if len(second.PendingKeys) != 0 || !slices.Equal(second.AddressedKeys, []string{"discussion:11"}) || second.ConsumedReviewNonces[testReviewNonce] != 12 {
		t.Errorf("after tick 2: pending/addressed/consumed = %v/%v/%v, want the retried receipt accepted", second.PendingKeys, second.AddressedKeys, second.ConsumedReviewNonces)
	}
	runs.requireNone(t)
}

// -- a launch is not cleared in the tick that created it (#1472 review-fix) --------

// A launch carrying only comment:/review: keys is settled the moment it exists (no
// discussion key is pending), so clearing it in its own tick lost the nonce before
// the worker's zero-key receipt could consume it and every later tick rejected that
// receipt as "nonce is not the outstanding launch's". The launch now stays
// outstanding until its receipt consumes the nonce and the window has ended.
func TestReviewWorker_CommentOnlyLaunchStaysOutstandingUntilItsZeroKeyReceiptConsumesTheNonce(t *testing.T) {
	const inline = `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	receipt := rwReceipt(12, testReviewNonce) // zero keys, posted by a write-access author
	e := newRWEnv(t, true, nil,
		ciCapStep{view: ciCapOpen("head-a", rwIssue), checks: ciCapGreen, comments: inline},                                 // 1: launch
		ciCapStep{view: ciCapOpen("head-a", rwIssue), checks: ciCapGreen, comments: inline, discussion: jsonArray(receipt)}, // 2: receipt posted, window live
		ciCapStep{view: ciCapOpen("head-a", rwIssue), checks: ciCapGreen, comments: inline, discussion: jsonArray(receipt)}, // 3: window ended
		ciCapStep{view: ciCapOpen("head-a", rwIssue), checks: ciCapGreen, comments: inline, discussion: jsonArray(receipt)}, // 4: quiet
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", liveReview(), deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	if got, want := runs.argAt(t, 1, "babysit-review"), "42 --receipt-nonce "+testReviewNonce; got != want {
		t.Errorf("launch argument = %q, want %q: no discussion token for an inline-only launch", got, want)
	}
	for _, tick := range []int{2, 3, 4} {
		runs.requireAt(t, tick)
	}
	if s := e.after(1); s.ReviewLaunch == nil || s.ReviewLaunch.Nonce != testReviewNonce || !slices.Equal(s.ReviewLaunch.Keys, []string{"comment:7"}) {
		t.Errorf("after the launch tick: ReviewLaunch = %+v, want it outstanding with comment:7", s.ReviewLaunch)
	}
	if s := e.after(2); s.ReviewLaunch == nil || s.ConsumedReviewNonces[testReviewNonce] != 12 {
		t.Errorf("after the receipt tick: launch %+v consumed %v, want the nonce consumed by comment 12 and the launch kept while the window is live", s.ReviewLaunch, s.ConsumedReviewNonces)
	}
	for _, tick := range []int{2, 3, 4} {
		if s := e.after(tick); strings.Contains(s.AutomergeDetail, "rejected") {
			t.Errorf("after tick %d: detail %q records a rejection for the worker's own receipt", tick, s.AutomergeDetail)
		}
	}
	if s := e.after(3); s.ReviewLaunch != nil || s.ConsumedReviewNonces[testReviewNonce] != 12 {
		t.Errorf("after the window ended: launch %+v consumed %v, want the launch cleared and the nonce still recorded", s.ReviewLaunch, s.ConsumedReviewNonces)
	}
}

// A comment-only launch whose worker never posts a receipt does not leak: it is
// cleared on the first tick that observes its window ended, after the launch tick.
func TestReviewWorker_CommentOnlyLaunchWithoutAReceiptIsClearedOnceTheWindowEnds(t *testing.T) {
	const inline = `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	step := ciCapStep{view: ciCapOpen("head-a", rwIssue), checks: ciCapGreen, comments: inline}
	e := newRWEnv(t, true, nil, step, step, step, ciCapMerged(rwIssue))
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", liveReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	runs.requireAt(t, 2)
	runs.requireAt(t, 3)
	for _, tick := range []int{1, 2} {
		if s := e.after(tick); s.ReviewLaunch == nil {
			t.Errorf("after tick %d: ReviewLaunch cleared before its window ended", tick)
		}
	}
	if s := e.after(3); s.ReviewLaunch != nil {
		t.Errorf("after the window ended: ReviewLaunch = %+v, want it cleared even with no receipt", s.ReviewLaunch)
	}
}

// -- #1539: untrusted-author limits enforced in Go -----------------------------
//
// The supervisor itself checks the author of every candidate key (discussion,
// inline comment and review), never launches a worker for untrusted feedback
// alone, caps how many launches an untrusted discussion key may ride in, raises a
// PR-level hold when an untrusted-carrying launch moved the head, and prunes the
// consumed-nonce map. Each case drives the real Run loop and asserts the saved
// state, the `cenci run babysit-review` arguments and the recorded detail.

// 40-hex PR heads, so a `resolve <pr> head:<sha>` hint names a well-formed SHA.
const (
	utHeadA = "1111111111111111111111111111111111111111"
	utHeadB = "2222222222222222222222222222222222222222"
	utHeadC = "3333333333333333333333333333333333333333"
)

// utInline renders one pull-request review comment by login.
func utInline(id int64, login string) string {
	return fmt.Sprintf(`{"id":%d,"updated_at":"2026-01-02T00:00:00Z","user":{"login":%q}}`, id, login)
}

// utReview renders one CHANGES_REQUESTED review by login.
func utReview(id int64, login string) string {
	return fmt.Sprintf(`{"id":%d,"state":"CHANGES_REQUESTED","submitted_at":"2026-01-02T00:00:00Z","user":{"login":%q}}`, id, login)
}

// utStep is a green tick at head serving the inline comments, the reviews and
// the PR conversation comments given.
func utStep(head string, inline, reviews []string, discussion ...string) ciCapStep {
	s := rwGreen(head, discussion...)
	if len(inline) > 0 {
		s.comments = jsonArray(inline...)
	}
	if len(reviews) > 0 {
		s.reviews = jsonArray(reviews...)
	}
	return s
}

// utNonces is n distinct 32-hex nonces, minted in order.
func utNonces(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%032x", i+1)
	}
	return out
}

// utConsumed is n consumed nonces whose receipt comments are long gone.
func utConsumed(n int) map[string]int64 {
	out := map[string]int64{}
	for i := range n {
		out[fmt.Sprintf("%032x", 1000+i)] = int64(5000 + i)
	}
	return out
}

// sameKeys reports whether a and b hold the same keys, in any order.
func sameKeys(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// permissionLookups counts the collaborator-permission reads per login.
func permissionLookups(e *ciCapEnv) map[string]int {
	out := map[string]int{}
	for _, c := range e.calls {
		if len(c) == 3 && c[0] == "gh" && c[1] == "api" {
			if m := permissionPathRe.FindStringSubmatch(c[2]); m != nil {
				out[m[1]]++
			}
		}
	}
	return out
}

// launchArgsContaining counts the babysit-review arguments that carry key.
func launchArgsContaining(r *rwRuns, key string) int {
	n := 0
	for _, e := range r.entries {
		if e.workflow == "babysit-review" && strings.Contains(e.arg, " "+key+"=") {
			n++
		}
	}
	return n
}

// AC 45: pending keys with no trusted author (a discussion key, an inline comment
// and a review, all by a `read` login) launch nothing and mint no nonce; every key
// is held for a write-access human, the status is needs-input, and the detail
// names `resolve` for the discussion key and GitHub for the other two.
func TestUntrustedReview_AC45_UntrustedOnlyTickLaunchesNothingAndHoldsEveryKey(t *testing.T) {
	servePermission(t, "stranger", "read")
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, []string{utInline(7, "stranger")}, []string{utReview(9, "stranger")}, humanCommentBy(11, "stranger", humanBody)),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	hdThreads(e, 7, func(int) bool { return false })
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	s := e.after(1)
	if s.ReviewLaunch != nil || len(s.ConsumedReviewNonces) != 0 {
		t.Errorf("ReviewLaunch %+v consumed %v, want no launch and no nonce minted", s.ReviewLaunch, s.ConsumedReviewNonces)
	}
	want := []string{"discussion:11", "comment:7", "review:9"}
	if !sameKeys(s.PendingKeys, want) || !sameKeys(s.HeldUntrustedKeys, want) || len(s.LaunchedKeys) != 0 {
		t.Errorf("pending/held/launched = %v/%v/%v, want %v pending and held, none launched", s.PendingKeys, s.HeldUntrustedKeys, s.LaunchedKeys, want)
	}
	if s.Status != "needs-input" {
		t.Errorf("status = %q, want needs-input", s.Status)
	}
	// #862: a comment:/review: key is resolved on GitHub, and `cenci babysit resolve`
	// then records it when the automation login did the resolving.
	for _, need := range []string{
		"cenci babysit resolve 42 discussion:11",
		"resolve comment:7 on GitHub, then run `cenci babysit resolve 42 comment:7` if it was resolved as the automation login",
		"resolve review:9 on GitHub, then run `cenci babysit resolve 42 review:9` if it was resolved as the automation login",
	} {
		if !strings.Contains(s.AutomergeDetail, need) {
			t.Errorf("detail %q, want it to contain %q", s.AutomergeDetail, need)
		}
	}
}

// AC 46: a launch carrying an inline comment by an `admin` author and a review by
// a `read` author records only the inline key as trusted, and each distinct login
// is looked up once in that tick (the review's author also wrote a discussion
// comment).
func TestUntrustedReview_AC46_InlineAndReviewAuthorsAreCheckedInGoOneLookupPerLogin(t *testing.T) {
	servePermission(t, "boss", "admin")
	servePermission(t, "stranger", "read")
	e := newRWEnv(t, false, nil,
		utStep(utHeadA, []string{utInline(7, "boss")}, []string{utReview(9, "stranger")}, humanCommentBy(11, "stranger", humanBody)),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	launched := e.after(1).ReviewLaunch
	if launched == nil {
		t.Fatal("no ReviewLaunch recorded, want the mixed launch")
	}
	if !slices.Contains(launched.TrustedKeys, "comment:7") || slices.Contains(launched.TrustedKeys, "review:9") || slices.Contains(launched.TrustedKeys, "discussion:11") {
		t.Errorf("trustedKeys = %v, want comment:7 and neither review:9 nor discussion:11", launched.TrustedKeys)
	}
	if !sameKeys(launched.Keys, []string{"comment:7", "review:9", "discussion:11"}) {
		t.Errorf("keys = %v, want all three carried", launched.Keys)
	}
	if got, want := permissionLookups(e), map[string]int{"boss": 1, "stranger": 1}; !maps.Equal(got, want) {
		t.Errorf("permission lookups = %v, want %v (one per distinct login)", got, want)
	}
}

// AC 47: an inline or review key whose author lookup fails is neither launched nor
// held that tick; it stays pending with the retry note, and the next tick's
// answered lookup launches it as trusted.
func TestUntrustedReview_AC47_UnknownInlineOrReviewAuthorLookupIsSkippedThenLaunchedOnRetry(t *testing.T) {
	for _, tc := range []struct {
		key     string
		inline  []string
		reviews []string
	}{
		{"comment:7", []string{utInline(7, "flaky")}, nil},
		{"review:9", nil, []string{utReview(9, "flaky")}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			servePermission(t, "flaky", "write")
			servePermissionFailure(t, "flaky")
			e := newRWEnv(t, true, nil,
				utStep(utHeadA, tc.inline, tc.reviews), // 1: the lookup fails
				utStep(utHeadA, tc.inline, tc.reviews), // 2: the lookup answers
				ciCapMerged(rwIssue),
			)
			runs := rwTrackRuns(e, 0)
			hdThreads(e, 7, func(int) bool { return false })
			e.panes = func(tick int) (string, error) {
				if tick == 2 {
					delete(permissionFailing, "flaky")
				}
				return noWorkerInventory, nil
			}
			e.seed(State{Status: "running"})

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			first := e.after(1)
			runs.requireAt(t, 1)
			if first.ReviewLaunch != nil || len(first.HeldUntrustedKeys) != 0 || !slices.Contains(first.PendingKeys, tc.key) {
				t.Errorf("after tick 1: launch %+v held %v pending %v, want %s pending, not launched and not held", first.ReviewLaunch, first.HeldUntrustedKeys, first.PendingKeys, tc.key)
			}
			if want := "author permission lookup failed for " + tc.key + "; will retry"; !strings.Contains(first.AutomergeDetail, want) {
				t.Errorf("after tick 1: detail %q, want %q", first.AutomergeDetail, want)
			}
			runs.requireAt(t, 2, "babysit-review")
			if second := e.after(2); second.ReviewLaunch == nil || !slices.Equal(second.ReviewLaunch.TrustedKeys, []string{tc.key}) {
				t.Errorf("after tick 2: ReviewLaunch = %+v, want %s launched as trusted", second.ReviewLaunch, tc.key)
			}
		})
	}
}

// AC 48: a launch with a trusted key still carries an untrusted discussion key as
// `discussion:<id>=<sha256>:untrusted`, unchanged.
func TestUntrustedReview_AC48_MixedLaunchStillCarriesTheUntrustedDiscussionTokenUnchanged(t *testing.T) {
	servePermission(t, "stranger", "read")
	const second = "and one more"
	e := newRWEnv(t, false, nil,
		utStep(utHeadA, nil, nil, humanCommentBy(11, "stranger", humanBody), humanComment(13, second)),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := "42 --receipt-nonce " + testReviewNonce + " discussion:11=" + bodyRevision(humanBody) + ":untrusted discussion:13=" + bodyRevision(second)
	if got := runs.argAt(t, 1, "babysit-review"); got != want {
		t.Errorf("launch argument = %q, want %q", got, want)
	}
	if s := e.after(1); len(s.HeldUntrustedKeys) != 0 || s.UntrustedLaunchCounts["discussion:11"] != 1 {
		t.Errorf("held %v counts %v, want nothing held and discussion:11 counted once", s.HeldUntrustedKeys, s.UntrustedLaunchCounts)
	}
}

// AC 49 (and edge case 64's still-denied branch): a `read` author edits one
// discussion comment 4 times while a trusted key is pending on every tick that
// could launch. Exactly 3 launch arguments carry it; after the third it is held,
// each later edit is looked up again and held again with its count kept, and the
// status is needs-input naming `resolve`.
func TestUntrustedReview_AC49_UntrustedDiscussionKeyRidesInAtMostThreeLaunchesAcrossEdits(t *testing.T) {
	servePermission(t, "stranger", "read")
	step := func(rev int) ciCapStep {
		return utStep(utHeadA, nil, nil,
			humanCommentBy(11, "stranger", fmt.Sprintf("untrusted ask v%d", rev)),
			humanComment(13, fmt.Sprintf("trusted ask v%d", rev)))
	}
	e := newRWEnv(t, false, utNonces(6),
		step(0), step(1), step(2), step(3), step(4), // 1-5: the original, then 4 edits
		step(4), // 6: quiet
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := launchArgsContaining(runs, "discussion:11"); n != 3 {
		t.Errorf("launch arguments carrying discussion:11 = %d, want exactly 3", n)
	}
	for tick := 1; tick <= 5; tick++ {
		runs.requireAt(t, tick, "babysit-review")
	}
	runs.requireAt(t, 6)
	for _, tick := range []int{4, 5} {
		if arg := runs.argAt(t, tick, "babysit-review"); strings.Contains(arg, "discussion:11") || !strings.Contains(arg, "discussion:13=") {
			t.Errorf("tick %d: launch argument %q, want only the trusted discussion:13", tick, arg)
		}
	}
	for _, tick := range []int{4, 5, 6} {
		s := e.after(tick)
		if !slices.Equal(s.HeldUntrustedKeys, []string{"discussion:11"}) || s.UntrustedLaunchCounts["discussion:11"] != 3 {
			t.Errorf("after tick %d: held %v counts %v, want discussion:11 held with its count kept at 3", tick, s.HeldUntrustedKeys, s.UntrustedLaunchCounts)
		}
		if slices.Contains(s.LaunchedKeys, "discussion:11") {
			t.Errorf("after tick %d: launched %v, want the held key out of launchedFeedbackKeys", tick, s.LaunchedKeys)
		}
		if s.Status != "needs-input" || !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
			t.Errorf("after tick %d: status %q detail %q, want needs-input naming `cenci babysit resolve 42 discussion:11`", tick, s.Status, s.AutomergeDetail)
		}
	}
}

// AC 50: a launch carried an untrusted item at head A and the first tick that
// sees its window ended reads head B: that tick holds automerge under `head moved
// during untrusted review launch`, sets needs-input and names `resolve <pr>
// head:<B>`. A receipt consumed while the window was live does not prevent it.
func TestUntrustedReview_AC50_HeadMovedDuringAnUntrustedLaunchRaisesAHeadMoveHold(t *testing.T) {
	servePermission(t, "stranger", "read")
	const second = "and one more"
	page := []string{humanCommentBy(11, "stranger", humanBody), humanComment(13, second)}
	withReceipt := append(slices.Clone(page), rwReceipt(21, testReviewNonce, rwEntry(11, humanBody, "no-action"), rwEntry(13, second, "addressed")))
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, nil, nil, page...),        // 1: launch at A
		utStep(utHeadB, nil, nil, withReceipt...), // 2: the worker pushed B and posted its receipt, window live
		utStep(utHeadB, nil, nil, withReceipt...), // 3: the window ended
		utStep(utHeadB, nil, nil, withReceipt...), // 4: still held
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", liveReview(), deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	if s := e.after(1); s.ReviewLaunch == nil || s.ReviewLaunch.HeadSHA != utHeadA {
		t.Fatalf("after the launch: ReviewLaunch = %+v, want headSha %s recorded", s.ReviewLaunch, utHeadA)
	}
	if s := e.after(2); s.HeadMoveHold != nil || s.AutomergeReason == reasonHeadMovedUntrustedReview {
		t.Errorf("while the window is live: hold %+v reason %q, want no head-move hold yet", s.HeadMoveHold, s.AutomergeReason)
	}
	for _, tick := range []int{3, 4} {
		s := e.after(tick)
		if s.AutomergeDecision != "held" || s.AutomergeReason != reasonHeadMovedUntrustedReview || s.Status != "needs-input" {
			t.Errorf("after tick %d: automerge %q (%q) status %q, want held for %q and needs-input", tick, s.AutomergeDecision, s.AutomergeReason, s.Status, reasonHeadMovedUntrustedReview)
		}
		if want := "cenci babysit resolve 42 head:" + utHeadB; !strings.Contains(s.AutomergeDetail, want) {
			t.Errorf("after tick %d: detail %q, want it to name %q", tick, s.AutomergeDetail, want)
		}
		if h := s.HeadMoveHold; h == nil || h.HeadSHA != utHeadB || h.LaunchHeadSHA != utHeadA || h.Nonce != testReviewNonce || h.RaisedAt.IsZero() {
			t.Errorf("after tick %d: headMoveHold = %+v, want head %s from a launch at %s (nonce %s) with a raise time", tick, h, utHeadB, utHeadA, testReviewNonce)
		}
	}
}

// AC 51: an untrusted-carrying launch whose window ends at the launch head raises
// no head-move hold.
func TestUntrustedReview_AC51_UntrustedLaunchWithAnUnchangedHeadRaisesNoHeadMoveHold(t *testing.T) {
	servePermission(t, "stranger", "read")
	page := []string{humanCommentBy(11, "stranger", humanBody), humanComment(13, "and one more")}
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, nil, nil, page...), // 1: launch at A
		utStep(utHeadA, nil, nil, page...), // 2: the window ended, head unchanged
		ciCapMerged(rwIssue),
	)
	rwTrackRuns(e, 0)
	e.panes = panesByTick("", deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := e.after(2)
	if s.HeadMoveHold != nil || s.AutomergeReason == reasonHeadMovedUntrustedReview {
		t.Errorf("hold %+v reason %q, want no head-move hold at an unchanged head", s.HeadMoveHold, s.AutomergeReason)
	}
	if s.ReviewLaunch == nil || !s.ReviewLaunch.HeadChecked {
		t.Errorf("ReviewLaunch = %+v, want the head check recorded as done", s.ReviewLaunch)
	}
}

// AC 52: a launch whose keys were all trusted raises no head-move hold when the
// head changed.
func TestUntrustedReview_AC52_AllTrustedLaunchRaisesNoHeadMoveHoldWhenTheHeadChanged(t *testing.T) {
	servePermission(t, "boss", "write")
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, []string{utInline(7, "boss")}, nil, humanComment(13, "and one more")), // 1: launch at A
		utStep(utHeadB, []string{utInline(7, "boss")}, nil, humanComment(13, "and one more")), // 2: ended at B
		ciCapMerged(rwIssue),
	)
	rwTrackRuns(e, 0)
	hdThreads(e, 7, func(int) bool { return false })
	e.panes = panesByTick("", deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if l := e.after(1).ReviewLaunch; l == nil || !sameKeys(l.TrustedKeys, l.Keys) {
		t.Fatalf("ReviewLaunch = %+v, want every carried key trusted", l)
	}
	if s := e.after(2); s.HeadMoveHold != nil || s.AutomergeReason == reasonHeadMovedUntrustedReview {
		t.Errorf("hold %+v reason %q, want no head-move hold for an all-trusted launch", s.HeadMoveHold, s.AutomergeReason)
	}
}

// Planning Q&A 1: a second untrusted-carrying launch that also moved the head
// replaces the recorded head with the newer one, so the human resolves once at
// the newest head. Review launches keep running while the hold is set.
func TestUntrustedReview_ASecondHeadMoveRaiseReplacesTheRecordedHeadWithTheNewerHead(t *testing.T) {
	servePermission(t, "stranger", "read")
	page := func(rev int) []string {
		return []string{humanCommentBy(11, "stranger", fmt.Sprintf("untrusted v%d", rev)), humanComment(13, fmt.Sprintf("trusted v%d", rev))}
	}
	nonces := utNonces(2)
	e := newRWEnv(t, true, nonces,
		utStep(utHeadA, nil, nil, page(0)...), // 1: launch at A
		utStep(utHeadB, nil, nil, page(1)...), // 2: ended at B -> hold B; both edited, launch again at B
		utStep(utHeadC, nil, nil, page(1)...), // 3: ended at C -> hold replaced by C
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 2, "babysit-review")
	if h := e.after(2).HeadMoveHold; h == nil || h.HeadSHA != utHeadB {
		t.Errorf("after tick 2: headMoveHold = %+v, want head %s", h, utHeadB)
	}
	s := e.after(3)
	if h := s.HeadMoveHold; h == nil || h.HeadSHA != utHeadC || h.LaunchHeadSHA != utHeadB || h.Nonce != nonces[1] {
		t.Errorf("after tick 3: headMoveHold = %+v, want head %s from the second launch at %s", h, utHeadC, utHeadB)
	}
	if !strings.Contains(s.AutomergeDetail, "head:"+utHeadC) || strings.Contains(s.AutomergeDetail, "head:"+utHeadB) {
		t.Errorf("after tick 3: detail %q, want it to name only head:%s", s.AutomergeDetail, utHeadC)
	}
}

// Decision (#1539): the head-move hold blocks automerge and sets needs-input
// only. With a recorded hold, failing CI still launches ci-repair and a conflict
// still launches merge-repair, each spending its own budget, and the hold is
// kept through the repair tick.
func TestUntrustedReview_HeadMoveHoldKeepsCIRepairAndMergeRepairLaunching(t *testing.T) {
	for _, tc := range []struct {
		workflow string
		step     ciCapStep
		spent    func(State) int
	}{
		{"ci-repair", rwFailing(utHeadB), func(s State) int { return s.FixAttempts }},
		{"merge-repair", rwDirtyFailing(utHeadB), func(s State) int { return s.ConflictFixAttempts }},
	} {
		t.Run(tc.workflow, func(t *testing.T) {
			e := newRWEnv(t, true, nil, tc.step, ciCapMerged(rwIssue))
			runs := rwTrackRuns(e, 0)
			hold := &headMoveHold{HeadSHA: utHeadB, LaunchHeadSHA: utHeadA, Nonce: testReviewNonce, RaisedAt: testLaunchAt}
			e.seed(State{Status: "needs-input", HeadMoveHold: hold})

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			runs.requireAt(t, 1, tc.workflow)
			if got := runs.argAt(t, 1, tc.workflow); !strings.HasPrefix(got, "42") {
				t.Errorf("%s argument = %q, want it to name PR 42", tc.workflow, got)
			}
			s := e.after(1)
			if n := tc.spent(s); n != 1 {
				t.Errorf("%s attempts after the launch = %d, want 1", tc.workflow, n)
			}
			if h := s.HeadMoveHold; h == nil || h.HeadSHA != utHeadB || h.LaunchHeadSHA != utHeadA {
				t.Errorf("headMoveHold after the %s tick = %+v, want the recorded hold at %s kept", tc.workflow, h, utHeadB)
			}
			if s.AutomergeDecision != "held" || s.Status != "needs-input" {
				t.Errorf("automerge %q status %q, want automerge held and needs-input while the hold is recorded", s.AutomergeDecision, s.Status)
			}
		})
	}
}

// Planning Q&A 3: with no trusted candidate, a denied key and an unknown lookup,
// nothing launches and nothing is held that tick; the unknown key gets the retry
// note.
func TestUntrustedReview_NoTrustedKeyWithAnUnknownLookupHoldsNothingThatTick(t *testing.T) {
	servePermission(t, "stranger", "read")
	servePermissionFailure(t, "flaky")
	e := newRWEnv(t, false, nil,
		utStep(utHeadA, []string{utInline(7, "flaky")}, nil, humanCommentBy(11, "stranger", humanBody)),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	s := e.after(1)
	if s.ReviewLaunch != nil || len(s.HeldUntrustedKeys) != 0 || !sameKeys(s.PendingKeys, []string{"discussion:11", "comment:7"}) {
		t.Errorf("launch %+v held %v pending %v, want both pending, nothing launched or held", s.ReviewLaunch, s.HeldUntrustedKeys, s.PendingKeys)
	}
	if want := "author permission lookup failed for comment:7; will retry"; !strings.Contains(s.AutomergeDetail, want) {
		t.Errorf("detail %q, want %q", s.AutomergeDetail, want)
	}
}

// Planning Q&A 3 with a key already at its cap: no trusted candidate, an unknown
// lookup, one denied discussion key at fixCap and one denied discussion key below
// it. Nothing launches; the capped key is held by the cap rule anyway, while the
// uncapped denied key stays pending and unheld (the unknown author may yet be
// trusted company) and the unknown key gets the retry note.
func TestUntrustedReview_NoTrustedKeyWithAnUnknownLookupStillHoldsAKeyAtItsCap(t *testing.T) {
	servePermission(t, "stranger", "read")
	servePermissionFailure(t, "flaky")
	const second = "a second ask"
	e := newRWEnv(t, false, nil,
		utStep(utHeadA, []string{utInline(7, "flaky")}, nil, humanCommentBy(11, "stranger", humanBody), humanCommentBy(13, "stranger", second)),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{
		Status:                "running",
		PendingKeys:           []string{"discussion:11"},
		DiscussionRevisions:   map[string]string{"discussion:11": bodyRevision(humanBody)},
		UntrustedLaunchCounts: map[string]int{"discussion:11": fixCap},
	})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	s := e.after(1)
	if s.ReviewLaunch != nil || len(s.ConsumedReviewNonces) != 0 {
		t.Errorf("ReviewLaunch %+v consumed %v, want no launch and no nonce minted", s.ReviewLaunch, s.ConsumedReviewNonces)
	}
	if !slices.Equal(s.HeldUntrustedKeys, []string{"discussion:11"}) {
		t.Errorf("held = %v, want only the capped discussion:11 held", s.HeldUntrustedKeys)
	}
	if !sameKeys(s.PendingKeys, []string{"discussion:11", "discussion:13", "comment:7"}) || len(s.LaunchedKeys) != 0 {
		t.Errorf("pending/launched = %v/%v, want all three pending and none launched", s.PendingKeys, s.LaunchedKeys)
	}
	if s.UntrustedLaunchCounts["discussion:11"] != fixCap || s.UntrustedLaunchCounts["discussion:13"] != 0 {
		t.Errorf("counts = %v, want discussion:11 kept at %d and discussion:13 uncounted", s.UntrustedLaunchCounts, fixCap)
	}
	if want := "author permission lookup failed for comment:7; will retry"; !strings.Contains(s.AutomergeDetail, want) {
		t.Errorf("detail %q, want %q", s.AutomergeDetail, want)
	}
	if !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") || strings.Contains(s.AutomergeDetail, "discussion:13") {
		t.Errorf("detail %q, want it to name the resolve for discussion:11 and nothing for the unheld discussion:13", s.AutomergeDetail)
	}
	if s.Status != "needs-input" {
		t.Errorf("status = %q, want needs-input for the held capped key", s.Status)
	}
}

// Edge case 64: an edit of a held untrusted discussion comment is looked up again
// in the tick that reads it: still denied, it is held again with no launch and its
// count kept; once its author has write access, the next edit launches it as
// trusted.
func TestUntrustedReview_Edge64_EditOfAHeldDiscussionKeyIsLookedUpAgainKeepsItsCountAndLaunchesOnceTrusted(t *testing.T) {
	servePermission(t, "stranger", "read")
	const v1, v2 = "edited once", "edited twice"
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, nil, nil, humanCommentBy(11, "stranger", humanBody)), // 1: held, no edit
		utStep(utHeadA, nil, nil, humanCommentBy(11, "stranger", v1)),        // 2: edited, still denied
		utStep(utHeadA, nil, nil, humanCommentBy(11, "stranger", v2)),        // 3: edited, now write
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = func(tick int) (string, error) {
		if tick == 3 {
			permissionServed["stranger"] = "write"
		}
		return noWorkerInventory, nil
	}
	e.seed(State{
		Status:                "running",
		PendingKeys:           []string{"discussion:11"},
		DiscussionRevisions:   map[string]string{"discussion:11": bodyRevision(humanBody)},
		HeldUntrustedKeys:     []string{"discussion:11"},
		UntrustedLaunchCounts: map[string]int{"discussion:11": fixCap},
	})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1)
	runs.requireAt(t, 2)
	for _, tick := range []int{1, 2} {
		s := e.after(tick)
		if !slices.Equal(s.HeldUntrustedKeys, []string{"discussion:11"}) || s.UntrustedLaunchCounts["discussion:11"] != fixCap || s.ReviewLaunch != nil {
			t.Errorf("after tick %d: held %v counts %v launch %+v, want discussion:11 held with its count kept and nothing launched", tick, s.HeldUntrustedKeys, s.UntrustedLaunchCounts, s.ReviewLaunch)
		}
	}
	if got, want := runs.argAt(t, 3, "babysit-review"), "42 --receipt-nonce "+testReviewNonce+" discussion:11="+bodyRevision(v2); got != want {
		t.Errorf("tick 3 launch argument = %q, want %q (trusted, no :untrusted suffix)", got, want)
	}
	if s := e.after(3); len(s.HeldUntrustedKeys) != 0 || s.ReviewLaunch == nil || !slices.Equal(s.ReviewLaunch.TrustedKeys, []string{"discussion:11"}) || s.UntrustedLaunchCounts["discussion:11"] != fixCap {
		t.Errorf("after tick 3: held %v launch %+v counts %v, want discussion:11 launched as trusted, released, its count unchanged", s.HeldUntrustedKeys, s.ReviewLaunch, s.UntrustedLaunchCounts)
	}
}

// Edge case 65: a held untrusted discussion key is never launched, so it never
// raises `review worker ended without receipt`; its hold is the needs-human one.
func TestUntrustedReview_Edge65_HeldDiscussionKeyNeverEndsWithoutAReceipt(t *testing.T) {
	servePermission(t, "stranger", "read")
	step := utStep(utHeadA, nil, nil, humanCommentBy(11, "stranger", humanBody))
	e := newRWEnv(t, true, nil, step, step, step, ciCapMerged(rwIssue))
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick(deadReview(), deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	for tick := 1; tick <= 3; tick++ {
		s := e.after(tick)
		if len(s.LaunchedKeys) != 0 || !slices.Equal(s.HeldUntrustedKeys, []string{"discussion:11"}) {
			t.Errorf("after tick %d: launched %v held %v, want discussion:11 held and never launched", tick, s.LaunchedKeys, s.HeldUntrustedKeys)
		}
		if s.AutomergeReason == reasonReviewWorkerEndedWithoutReceipt {
			t.Errorf("after tick %d: reason %q for a key no worker ever carried", tick, s.AutomergeReason)
		}
		if s.Status != "needs-input" || !strings.Contains(s.AutomergeDetail, "cenci babysit resolve 42 discussion:11") {
			t.Errorf("after tick %d: status %q detail %q, want needs-input naming the resolve", tick, s.Status, s.AutomergeDetail)
		}
	}
}

// Edge case 66: a held untrusted inline comment resolved on GitHub moves to
// addressed through reconcileFeedback, leaves the held set, and the status goes
// back to running.
func TestUntrustedReview_Edge66_HeldInlineKeyResolvedOnGitHubLeavesTheHeldSet(t *testing.T) {
	servePermission(t, "stranger", "read")
	step := utStep(utHeadA, []string{utInline(7, "stranger")}, nil)
	e := newRWEnv(t, true, nil, step, step, ciCapMerged(rwIssue))
	runs := rwTrackRuns(e, 0)
	hdThreads(e, 7, func(view int) bool { return view >= 2 })
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	if s := e.after(1); !slices.Equal(s.HeldUntrustedKeys, []string{"comment:7"}) || s.Status != "needs-input" || !strings.Contains(s.AutomergeDetail, "resolve comment:7 on GitHub, then run `cenci babysit resolve 42 comment:7`") {
		t.Errorf("after tick 1: held %v status %q detail %q, want comment:7 held, needs-input, resolve on GitHub then `resolve`", s.HeldUntrustedKeys, s.Status, s.AutomergeDetail)
	}
	if s := e.after(2); len(s.HeldUntrustedKeys) != 0 || !slices.Equal(s.AddressedKeys, []string{"comment:7"}) || s.Status != "running" {
		t.Errorf("after tick 2: held %v addressed %v status %q, want comment:7 addressed, released and running", s.HeldUntrustedKeys, s.AddressedKeys, s.Status)
	}
}

// Edge case 66 (the review: half): a held untrusted inline comment and a held
// untrusted review are each resolved on GitHub in turn. Each moves to addressed
// through reconcileFeedback and leaves the held set; the status stays needs-input
// while the review is still held and goes back to running once it is dismissed.
func TestUntrustedReview_Edge66_HeldReviewKeyDismissedOnGitHubLeavesTheHeldSet(t *testing.T) {
	servePermission(t, "stranger", "read")
	const dismissed = `{"id":9,"state":"DISMISSED","submitted_at":"2026-01-02T00:00:00Z","user":{"login":"stranger"}}`
	// #862: the dismissal counts through its event's actor -- here a write-access
	// maintainer (humanLogin), not the automation login.
	serveDismissals(t, dismissalPage(false, "", dismissalEvent(9, humanLogin)))
	inline := []string{utInline(7, "stranger")}
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, inline, []string{utReview(9, "stranger")}), // 1: both held
		utStep(utHeadA, inline, []string{utReview(9, "stranger")}), // 2: the inline thread is resolved
		utStep(utHeadA, inline, []string{dismissed}),               // 3: the review is dismissed
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	hdThreads(e, 7, func(view int) bool { return view >= 2 })
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	if s := e.after(1); !sameKeys(s.HeldUntrustedKeys, []string{"comment:7", "review:9"}) || s.Status != "needs-input" || !strings.Contains(s.AutomergeDetail, "resolve review:9 on GitHub, then run `cenci babysit resolve 42 review:9`") {
		t.Errorf("after tick 1: held %v status %q detail %q, want comment:7 and review:9 held, needs-input, resolve review:9 on GitHub then `resolve`", s.HeldUntrustedKeys, s.Status, s.AutomergeDetail)
	}
	if s := e.after(2); !slices.Equal(s.HeldUntrustedKeys, []string{"review:9"}) || !slices.Equal(s.AddressedKeys, []string{"comment:7"}) || s.Status != "needs-input" {
		t.Errorf("after tick 2: held %v addressed %v status %q, want only review:9 still held and needs-input", s.HeldUntrustedKeys, s.AddressedKeys, s.Status)
	}
	s := e.after(3)
	if len(s.HeldUntrustedKeys) != 0 || !sameKeys(s.AddressedKeys, []string{"comment:7", "review:9"}) || slices.Contains(s.PendingKeys, "review:9") || s.Status != "running" {
		t.Errorf("after tick 3: held %v addressed %v pending %v status %q, want review:9 addressed, released and running", s.HeldUntrustedKeys, s.AddressedKeys, s.PendingKeys, s.Status)
	}
	if strings.Contains(s.AutomergeDetail, "review:9") {
		t.Errorf("after tick 3: detail %q still names review:9", s.AutomergeDetail)
	}
}

// Edge case 67: a `[bot]` author's inline comment or review is still excluded by
// detection and costs no permission lookup.
func TestUntrustedReview_Edge67_BotInlineAndReviewAuthorsTriggerNoPermissionLookup(t *testing.T) {
	e := newRWEnv(t, false, nil,
		utStep(utHeadA, []string{utInline(7, "ci-helper[bot]")}, []string{utReview(9, "deps[bot]")}),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	if s := e.after(1); len(s.PendingKeys) != 0 || len(s.HeldUntrustedKeys) != 0 {
		t.Errorf("pending %v held %v, want bot feedback excluded", s.PendingKeys, s.HeldUntrustedKeys)
	}
	if got := permissionLookups(e); len(got) != 0 {
		t.Errorf("permission lookups = %v, want none for bot authors", got)
	}
}

// Edge case 68: an inline or review author whose login fails GitHub's login
// grammar is denied without a gh call.
func TestUntrustedReview_Edge68_InvalidInlineAndReviewLoginsAreDeniedWithoutAGhCall(t *testing.T) {
	e := newRWEnv(t, false, nil,
		utStep(utHeadA, []string{utInline(7, "-bad")}, []string{utReview(9, "bad_login")}),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	if s := e.after(1); !sameKeys(s.HeldUntrustedKeys, []string{"comment:7", "review:9"}) {
		t.Errorf("held %v, want both keys held as denied", s.HeldUntrustedKeys)
	}
	if got := permissionLookups(e); len(got) != 0 {
		t.Errorf("permission lookups = %v, want no gh call for an invalid login", got)
	}
}

// Q&A 4: a pending comment:/review: key whose inline comment or review is missing
// from this tick's read has the empty login as its author, which
// fetchWritePermission denies without a gh call. So the key is never trusted or
// launched (a granted author would launch it, an unknown lookup would leave it
// unheld): with no trusted company it is held for a write-access human.
func TestUntrustedReview_QA4_KeyMissingFromTheTicksReadIsDeniedWithoutAGhCallAndHeld(t *testing.T) {
	e := newRWEnv(t, false, nil,
		utStep(utHeadA, nil, nil),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	hdThreads(e, 7, func(int) bool { return false })
	e.seed(State{Status: "running", PendingKeys: []string{"comment:7", "review:9"}})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireNone(t)
	want := []string{"comment:7", "review:9"}
	s := e.after(1)
	if s.ReviewLaunch != nil || len(s.LaunchedKeys) != 0 {
		t.Errorf("ReviewLaunch %+v launched %v, want nothing launched for a missing item's key", s.ReviewLaunch, s.LaunchedKeys)
	}
	if !sameKeys(s.PendingKeys, want) || !sameKeys(s.HeldUntrustedKeys, want) {
		t.Errorf("pending/held = %v/%v, want %v pending and held as denied", s.PendingKeys, s.HeldUntrustedKeys, want)
	}
	if s.Status != "needs-input" {
		t.Errorf("status = %q, want needs-input", s.Status)
	}
	if n := countCalls(e.calls, "collaborators/"); n != 0 {
		t.Errorf("collaborator permission calls = %d, want none for an empty login", n)
	}
}

// Edge case 69: a legacy outstanding launch with no recorded head and no trust
// record raises the head-move hold when its window is seen ended (fail closed).
func TestUntrustedReview_Edge69_LegacyLaunchWithNoHeadSHAThatCarriedUntrustedInputFailsClosed(t *testing.T) {
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, nil, nil, humanComment(11, humanBody)),
		ciCapMerged(rwIssue),
	)
	rwTrackRuns(e, 0)
	e.panes = panesByTick(deadReview())
	seed := outstandingReviewState()
	seed.ReviewLaunch.TrustedKeys = nil
	e.seed(seed)

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := e.after(1)
	if h := s.HeadMoveHold; h == nil || h.HeadSHA != utHeadA || h.LaunchHeadSHA != "" {
		t.Errorf("headMoveHold = %+v, want a fail-closed hold at %s with no launch head", h, utHeadA)
	}
	if s.AutomergeReason != reasonHeadMovedUntrustedReview || s.Status != "needs-input" {
		t.Errorf("reason %q status %q, want %q and needs-input", s.AutomergeReason, s.Status, reasonHeadMovedUntrustedReview)
	}
}

// Edge case 70: a legacy launch whose trustedKeys lists only discussion keys
// treats its carried inline key as untrusted for the head-move check.
func TestUntrustedReview_Edge70_LegacyLaunchCarriedInlineKeysWithNoTrustRecordAreUntrusted(t *testing.T) {
	e := newRWEnv(t, true, nil,
		utStep(utHeadB, []string{utInline(7, "reviewer")}, nil, humanComment(11, humanBody)),
		ciCapMerged(rwIssue),
	)
	rwTrackRuns(e, 0)
	hdThreads(e, 7, func(int) bool { return false })
	e.panes = panesByTick(deadReview())
	seed := outstandingReviewState()
	seed.PendingKeys = []string{"comment:7", "discussion:11"}
	seed.LaunchedKeys = []string{"comment:7", "discussion:11"}
	seed.LastCommentAt = "2026-01-02T00:00:00Z"
	seed.ReviewLaunch.Keys = []string{"comment:7", "discussion:11"}
	seed.ReviewLaunch.TrustedKeys = []string{"discussion:11"}
	seed.ReviewLaunch.HeadSHA = utHeadA
	e.seed(seed)

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if h := e.after(1).HeadMoveHold; h == nil || h.HeadSHA != utHeadB || h.LaunchHeadSHA != utHeadA {
		t.Errorf("headMoveHold = %+v, want a hold at %s for the launch at %s", h, utHeadB, utHeadA)
	}
}

// Edge case 71: a launch whose window ends with no receipt is checked on the first
// tick that sees the window ended, while the launch is still outstanding, and the
// hold keeps the head it saw then.
func TestUntrustedReview_Edge71_LaunchEndedWithoutAReceiptIsCheckedOnTheFirstEndedTick(t *testing.T) {
	servePermission(t, "stranger", "read")
	page := []string{humanCommentBy(11, "stranger", humanBody), humanComment(13, "and one more")}
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, nil, nil, page...), // 1: launch at A
		utStep(utHeadB, nil, nil, page...), // 2: live at B
		utStep(utHeadB, nil, nil, page...), // 3: ended at B, no receipt
		utStep(utHeadC, nil, nil, page...), // 4: the head moved again
		ciCapMerged(rwIssue),
	)
	rwTrackRuns(e, 0)
	e.panes = panesByTick("", liveReview(), deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if s := e.after(2); s.HeadMoveHold != nil {
		t.Errorf("after tick 2: headMoveHold = %+v, want none while the window is live", s.HeadMoveHold)
	}
	s := e.after(3)
	if s.ReviewLaunch == nil || !s.ReviewLaunch.HeadChecked {
		t.Errorf("after tick 3: ReviewLaunch = %+v, want it still outstanding with the head check done", s.ReviewLaunch)
	}
	for _, tick := range []int{3, 4} {
		if h := e.after(tick).HeadMoveHold; h == nil || h.HeadSHA != utHeadB {
			t.Errorf("after tick %d: headMoveHold = %+v, want head %s (the head the ended tick saw)", tick, h, utHeadB)
		}
	}
}

// Edge case 72: a tick with an incomplete discussion read (page cap reached)
// prunes no consumed nonce.
func TestUntrustedReview_Edge72_IncompleteDiscussionReadPrunesNoConsumedNonce(t *testing.T) {
	e := newRWEnv(t, false, nil,
		ciCapStep{view: ciCapOpen(utHeadA, rwIssue), checks: ciCapGreen, discussion: fullDiscussionPage(100)},
		ciCapMerged(rwIssue),
	)
	rwTrackRuns(e, 0)
	seeded := utConsumed(50)
	e.seed(State{Status: "running", ConsumedReviewNonces: seeded})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := e.after(1).ConsumedReviewNonces; !maps.Equal(got, seeded) {
		t.Errorf("consumedReviewNonces has %d entries after a truncated read, want all %d kept", len(got), len(seeded))
	}
}

// AC 57 at tick level: one tick with a complete discussion read prunes 50 consumed
// nonces whose receipt comments are gone, keeping only the outstanding launch's
// nonce, also on a tick that launches (a nonce it superseded is dropped at once).
func TestUntrustedReview_AC57_ACompleteDiscussionReadPrunesConsumedNoncesToTheOutstandingLaunch(t *testing.T) {
	t.Run("no outstanding launch", func(t *testing.T) {
		e := newRWEnv(t, false, nil, rwGreen(utHeadA), ciCapMerged(rwIssue))
		rwTrackRuns(e, 0)
		e.seed(State{Status: "running", ConsumedReviewNonces: utConsumed(50)})

		if err := e.run(); err != nil {
			t.Fatalf("Run: %v", err)
		}

		if got := e.after(1).ConsumedReviewNonces; len(got) != 0 {
			t.Errorf("consumedReviewNonces = %v, want every stale entry pruned", got)
		}
	})
	t.Run("outstanding launch nonce kept", func(t *testing.T) {
		e := newRWEnv(t, false, nil, rwGreen(utHeadA, humanComment(11, humanBody)), ciCapMerged(rwIssue))
		rwTrackRuns(e, 0)
		e.panes = panesByTick(liveReview())
		seed := outstandingReviewState()
		seed.ConsumedReviewNonces = utConsumed(50)
		seed.ConsumedReviewNonces[testReviewNonce] = 99
		e.seed(seed)

		if err := e.run(); err != nil {
			t.Fatalf("Run: %v", err)
		}

		s := e.after(1)
		if want := map[string]int64{testReviewNonce: 99}; s.ReviewLaunch == nil || !maps.Equal(s.ConsumedReviewNonces, want) {
			t.Errorf("launch %+v consumedReviewNonces = %v, want only the outstanding launch's %v", s.ReviewLaunch, s.ConsumedReviewNonces, want)
		}
	})
	t.Run("launching tick", func(t *testing.T) {
		const second = "and one more"
		e := newRWEnv(t, false, nil, rwGreen(utHeadA, humanComment(11, humanBody), humanComment(13, second)), ciCapMerged(rwIssue))
		runs := rwTrackRuns(e, 0)
		e.panes = panesByTick(deadReview())
		seed := outstandingReviewState()
		seed.ReviewLaunch.Nonce = testWrongNonce
		seed.PendingKeys = []string{"discussion:11", "discussion:13"}
		seed.DiscussionRevisions["discussion:13"] = bodyRevision(second)
		seed.ConsumedReviewNonces = utConsumed(50)
		e.seed(seed)

		if err := e.run(); err != nil {
			t.Fatalf("Run: %v", err)
		}

		runs.requireAt(t, 1, "babysit-review")
		s := e.after(1)
		if s.ReviewLaunch == nil || s.ReviewLaunch.Nonce != testReviewNonce || len(s.ConsumedReviewNonces) != 0 {
			t.Errorf("launch %+v consumedReviewNonces = %v, want the new launch outstanding and every stale or superseded nonce pruned", s.ReviewLaunch, s.ConsumedReviewNonces)
		}
	})
}

// Edge case 76: trusted discussion keys are not counted toward the untrusted
// launch cap; a write-access author's edits keep launching once per revision.
func TestUntrustedReview_Edge76_TrustedDiscussionKeysAreNotCountedTowardTheCap(t *testing.T) {
	step := func(rev int) ciCapStep {
		return utStep(utHeadA, nil, nil, humanComment(13, fmt.Sprintf("trusted ask v%d", rev)))
	}
	e := newRWEnv(t, false, utNonces(5), step(0), step(1), step(2), step(3), step(4), ciCapMerged(rwIssue))
	runs := rwTrackRuns(e, 0)
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := launchArgsContaining(runs, "discussion:13"); n != 5 {
		t.Errorf("launch arguments carrying discussion:13 = %d, want 5 (one per revision)", n)
	}
	if s := e.after(5); len(s.UntrustedLaunchCounts) != 0 || len(s.HeldUntrustedKeys) != 0 {
		t.Errorf("counts %v held %v, want no untrusted bookkeeping for a trusted author", s.UntrustedLaunchCounts, s.HeldUntrustedKeys)
	}
}

// Q&A 5: an untrusted-carrying launch at head A whose window is first seen ended
// while the live PR head is empty defers the head check: that tick leaves
// headChecked unset and raises no head-move hold. The next tick with a known head
// B runs the check once and raises the hold naming `head:<B>`.
func TestUntrustedReview_QA5_EmptyLiveHeadDefersTheHeadCheckToTheNextKnownHead(t *testing.T) {
	servePermission(t, "stranger", "read")
	page := []string{humanCommentBy(11, "stranger", humanBody), humanComment(13, "and one more")}
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, nil, nil, page...), // 1: launch at A
		utStep("", nil, nil, page...),      // 2: the window ended, live head unknown
		utStep(utHeadB, nil, nil, page...), // 3: the window still ended, head B known
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	if s := e.after(1); s.ReviewLaunch == nil || s.ReviewLaunch.HeadSHA != utHeadA {
		t.Fatalf("after the launch: ReviewLaunch = %+v, want headSha %s recorded", s.ReviewLaunch, utHeadA)
	}
	deferred := e.after(2)
	if deferred.ReviewLaunch == nil || deferred.ReviewLaunch.HeadChecked {
		t.Errorf("after the empty-head tick: ReviewLaunch = %+v, want it outstanding with headChecked unset", deferred.ReviewLaunch)
	}
	if deferred.HeadMoveHold != nil || deferred.AutomergeReason == reasonHeadMovedUntrustedReview {
		t.Errorf("after the empty-head tick: hold %+v reason %q, want no head-move hold", deferred.HeadMoveHold, deferred.AutomergeReason)
	}
	checked := e.after(3)
	if checked.ReviewLaunch == nil || !checked.ReviewLaunch.HeadChecked {
		t.Errorf("after the known-head tick: ReviewLaunch = %+v, want headChecked set", checked.ReviewLaunch)
	}
	if h := checked.HeadMoveHold; h == nil || h.HeadSHA != utHeadB || h.LaunchHeadSHA != utHeadA {
		t.Errorf("after the known-head tick: headMoveHold = %+v, want head %s from the launch at %s", h, utHeadB, utHeadA)
	}
	if want := "cenci babysit resolve 42 head:" + utHeadB; checked.AutomergeReason != reasonHeadMovedUntrustedReview || !strings.Contains(checked.AutomergeDetail, want) {
		t.Errorf("after the known-head tick: reason %q detail %q, want %q naming %q", checked.AutomergeReason, checked.AutomergeDetail, reasonHeadMovedUntrustedReview, want)
	}
}

// requireDeferredHeadCheck asserts that the tick after which s was saved kept the
// outstanding launch nonce with its head check still owed and raised no hold.
func requireDeferredHeadCheck(t *testing.T, s State, nonce string) {
	t.Helper()
	if s.ReviewLaunch == nil || s.ReviewLaunch.Nonce != nonce || s.ReviewLaunch.HeadChecked {
		t.Errorf("after the empty-head tick: ReviewLaunch = %+v, want launch %s kept with headChecked unset", s.ReviewLaunch, nonce)
	}
	if s.HeadMoveHold != nil || s.AutomergeReason == reasonHeadMovedUntrustedReview {
		t.Errorf("after the empty-head tick: hold %+v reason %q, want no head-move hold yet", s.HeadMoveHold, s.AutomergeReason)
	}
}

// requireHeadMoveHold asserts a head-move hold at head raised for the launch at
// launchHead with nonce, named in the recorded detail.
func requireHeadMoveHold(t *testing.T, s State, head, launchHead, nonce string) {
	t.Helper()
	if h := s.HeadMoveHold; h == nil || h.HeadSHA != head || h.LaunchHeadSHA != launchHead || h.Nonce != nonce {
		t.Errorf("headMoveHold = %+v, want head %s from the launch at %s (nonce %s)", h, head, launchHead, nonce)
	}
	if want := "cenci babysit resolve 42 head:" + head; s.AutomergeReason != reasonHeadMovedUntrustedReview || !strings.Contains(s.AutomergeDetail, want) {
		t.Errorf("reason %q detail %q, want %q naming %q", s.AutomergeReason, s.AutomergeDetail, reasonHeadMovedUntrustedReview, want)
	}
}

// Q&A 5: the deferred head check is durable. An untrusted-carrying launch at head
// A whose receipt was accepted (its nonce consumed, so the launch is settled) is
// not cleared on an ended-window tick whose live head is empty: the record, with
// its head and headChecked unset, waits for the next tick with a known head B,
// which raises the head-move hold naming `head:<B>`.
func TestUntrustedReview_QA5_EmptyLiveHeadWithAnAcceptedReceiptKeepsTheLaunchUntilTheHeadIsKnown(t *testing.T) {
	servePermission(t, "stranger", "read")
	const second = "and one more"
	page := []string{humanCommentBy(11, "stranger", humanBody), humanComment(13, second)}
	withReceipt := append(slices.Clone(page), rwReceipt(21, testReviewNonce, rwEntry(11, humanBody, "no-action"), rwEntry(13, second, "addressed")))
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, nil, nil, page...),        // 1: launch at A
		utStep(utHeadA, nil, nil, withReceipt...), // 2: receipt accepted, window live
		utStep("", nil, nil, withReceipt...),      // 3: the window ended, live head unknown
		utStep(utHeadB, nil, nil, withReceipt...), // 4: head B known
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", liveReview(), deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	if s := e.after(2); s.ConsumedReviewNonces[testReviewNonce] != 21 {
		t.Fatalf("after the receipt tick: consumed %v, want the nonce consumed by comment 21", s.ConsumedReviewNonces)
	}
	requireDeferredHeadCheck(t, e.after(3), testReviewNonce)
	checked := e.after(4)
	if checked.ReviewLaunch != nil && !checked.ReviewLaunch.HeadChecked {
		t.Errorf("after the known-head tick: ReviewLaunch = %+v, want the head check done", checked.ReviewLaunch)
	}
	requireHeadMoveHold(t, checked, utHeadB, utHeadA, testReviewNonce)
}

// Q&A 5: a comment:/review:-only launch is settled the moment it exists, yet one
// that carried an untrusted review is not cleared on an ended-window tick with an
// empty live head; the next tick with a known head B runs the owed check and
// raises the head-move hold.
func TestUntrustedReview_QA5_EmptyLiveHeadWithACommentOnlyLaunchKeepsTheLaunchUntilTheHeadIsKnown(t *testing.T) {
	servePermission(t, "stranger", "read")
	inline, reviews := []string{utInline(7, "reviewer")}, []string{utReview(9, "stranger")}
	e := newRWEnv(t, true, nil,
		utStep(utHeadA, inline, reviews), // 1: launch at A
		utStep("", inline, reviews),      // 2: the window ended, live head unknown
		utStep(utHeadB, inline, reviews), // 3: head B known
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	hdThreads(e, 7, func(int) bool { return false })
	e.panes = panesByTick("", deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	if l := e.after(1).ReviewLaunch; l == nil || !sameKeys(l.Keys, []string{"comment:7", "review:9"}) || !slices.Equal(l.TrustedKeys, []string{"comment:7"}) {
		t.Fatalf("after the launch: ReviewLaunch = %+v, want comment:7 trusted and review:9 untrusted", l)
	}
	requireDeferredHeadCheck(t, e.after(2), testReviewNonce)
	requireHeadMoveHold(t, e.after(3), utHeadB, utHeadA, testReviewNonce)
}

// Q&A 5/6: while an ended-window untrusted launch still owes its head check (the
// live head is empty), a newly pending trusted key does not launch and does not
// overwrite it. Once the head is known, the owed check runs first (raising the
// hold, since the head moved) and the new key then launches.
func TestUntrustedReview_QA5_EmptyLiveHeadDoesNotSupersedeAnUncheckedLaunch(t *testing.T) {
	servePermission(t, "stranger", "read")
	page := []string{humanCommentBy(11, "stranger", humanBody), humanComment(13, "and one more")}
	later := append(slices.Clone(page), humanComment(15, "one more trusted ask"))
	nonces := utNonces(2)
	e := newRWEnv(t, true, nonces,
		utStep(utHeadA, nil, nil, page...),  // 1: launch at A
		utStep("", nil, nil, later...),      // 2: ended, head unknown, a trusted key is new
		utStep(utHeadB, nil, nil, later...), // 3: head B known
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", deadReview(), deadReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs.requireAt(t, 1, "babysit-review")
	runs.requireAt(t, 2)
	deferred := e.after(2)
	requireDeferredHeadCheck(t, deferred, nonces[0])
	if !slices.Contains(deferred.PendingKeys, "discussion:15") || slices.Contains(deferred.LaunchedKeys, "discussion:15") {
		t.Errorf("after the empty-head tick: pending %v launched %v, want discussion:15 pending and not launched", deferred.PendingKeys, deferred.LaunchedKeys)
	}
	runs.requireAt(t, 3, "babysit-review")
	checked := e.after(3)
	requireHeadMoveHold(t, checked, utHeadB, utHeadA, nonces[0])
	if l := checked.ReviewLaunch; l == nil || l.Nonce != nonces[1] || !slices.Equal(l.Keys, []string{"discussion:15"}) || l.HeadSHA != utHeadB {
		t.Errorf("after the known-head tick: ReviewLaunch = %+v, want the new launch of discussion:15 at %s", l, utHeadB)
	}
}

// Q&A 8: untrusted comment:/review: keys ride along in a mixed launch: they are
// carried in reviewLaunch.keys but left out of trustedKeys, and only discussion:
// keys count toward the untrusted launch cap, so neither gets a count entry or is
// held across more than fixCap launches while it stays pending.
func TestUntrustedReview_QA8_UntrustedInlineAndReviewKeysRideAlongUncountedAndNeverCapped(t *testing.T) {
	servePermission(t, "stranger", "read")
	step := func(rev int) ciCapStep {
		return utStep(utHeadA, []string{utInline(7, "stranger")}, []string{utReview(9, "stranger")},
			humanComment(13, fmt.Sprintf("trusted ask v%d", rev)))
	}
	launches := fixCap + 1
	steps := make([]ciCapStep, 0, launches+1)
	for rev := range launches {
		steps = append(steps, step(rev)) // each tick edits the trusted key, so each one launches
	}
	e := newRWEnv(t, false, utNonces(launches), append(steps, ciCapMerged(rwIssue))...)
	runs := rwTrackRuns(e, 0)
	hdThreads(e, 7, func(int) bool { return false })
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for tick := 1; tick <= launches; tick++ {
		runs.requireAt(t, tick, "babysit-review")
	}
	first := e.after(1).ReviewLaunch
	if first == nil {
		t.Fatal("no ReviewLaunch recorded, want the mixed launch")
	}
	if !sameKeys(first.Keys, []string{"comment:7", "review:9", "discussion:13"}) {
		t.Errorf("keys = %v, want the untrusted comment:7 and review:9 carried with discussion:13", first.Keys)
	}
	if !slices.Equal(first.TrustedKeys, []string{"discussion:13"}) {
		t.Errorf("trustedKeys = %v, want only discussion:13", first.TrustedKeys)
	}
	for tick := 1; tick <= launches; tick++ {
		s := e.after(tick)
		for _, k := range []string{"comment:7", "review:9"} {
			if _, counted := s.UntrustedLaunchCounts[k]; counted {
				t.Errorf("after tick %d: untrustedLaunchCounts = %v, want no entry for %s", tick, s.UntrustedLaunchCounts, k)
			}
			if slices.Contains(s.HeldUntrustedKeys, k) || !slices.Contains(s.PendingKeys, k) {
				t.Errorf("after tick %d: held %v pending %v, want %s pending and not held", tick, s.HeldUntrustedKeys, s.PendingKeys, k)
			}
		}
	}
}

// Q&A 9: a launch counted as dispatched with `cenci run` exit 3 counts toward the
// untrusted launch cap, like any other dispatched launch.
func TestUntrustedReview_QA9_ExitThreeDispatchCountsTowardTheUntrustedLaunchCap(t *testing.T) {
	servePermission(t, "stranger", "read")
	page := []string{humanCommentBy(11, "stranger", humanBody), humanComment(13, "and one more")}
	e := newRWEnv(t, false, nil,
		utStep(utHeadA, nil, nil, page...), // 1: exit 3
		utStep(utHeadA, nil, nil, page...), // 2: the spawned window is live
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 3)
	e.panes = panesByTick("", liveReview())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v, want a spawned window counted as dispatched", err)
	}

	if want := []string{"babysit-review"}; !slices.Equal(runs.all(), want) {
		t.Errorf("cenci run calls = %q, want exactly one", runs.all())
	}
	s := e.after(1)
	if s.ReviewLaunch == nil || s.ReviewLaunch.Nonce != testReviewNonce || !sameKeys(s.ReviewLaunch.Keys, []string{"discussion:11", "discussion:13"}) {
		t.Errorf("ReviewLaunch = %+v, want the exit-3 launch recorded with both keys", s.ReviewLaunch)
	}
	for _, tick := range []int{1, 2} {
		if got := e.after(tick).UntrustedLaunchCounts; got["discussion:11"] != 1 || len(got) != 1 {
			t.Errorf("after tick %d: untrustedLaunchCounts = %v, want discussion:11 counted once", tick, got)
		}
	}
}
