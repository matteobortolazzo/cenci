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
