package babysit

// #1472 (absorbing the babysit part of #1477): feedback holds survive a stop, a
// re-arm and a restart. Each scenario drives the real Run loop over the ciCapEnv
// harness (ci_cap_lifecycle_test.go), stops the supervisor (killed after a save,
// then `cenci babysit stop` for a supervisor that is gone), re-arms it through
// Run's arming branch (the detached child is never spawned: startSupervisor is
// stubbed) and runs the child's ticks, asserting the state saved after each
// tick and the `cenci run` ledger of review_worker_lifecycle_test.go. Nothing
// sleeps: the wait is the harness's recording stub, and the nonce and launch
// clock are pinned.
//
//   P2  stop and re-arm with unresolved feedback restores PendingKeys and keeps
//       the review-stage automerge hold until authoritative resolution
//   P3  an unreadable or undecodable re-arm state is a named hold, never an
//       empty PendingKeys
//   P19 a comment posted while supervision is down is collected after re-arm
//       and holds the merge
//   plus the schema-v2 upgrade and the exact save/reload round trip of the
//   discussion fields.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	// hdInlineComment is a reviewer's inline (pull-request review) comment 7.
	hdInlineComment = `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	// hdLateInlineComment is the inline comment 9 a reviewer leaves while
	// supervision is down.
	hdLateInlineComment = `[{"id":9,"updated_at":"2026-01-03T00:00:00Z","user":{"login":"reviewer"}}]`
)

// hdInline is a green tick on which the reviewer's inline comment 7 is on the PR.
func hdInline(head string) ciCapStep {
	return ciCapStep{view: ciCapOpen(head, rwIssue), checks: ciCapGreen, comments: hdInlineComment, discussion: "[]"}
}

// hdThreads answers the reviewThreads GraphQL read: thread resolved(view) for
// comment id, where view is the pr view number of the tick being served.
func hdThreads(e *ciCapEnv, id int64, resolved func(view int) bool) {
	base := execGh
	execGh = func(args ...string) (string, string, error) {
		if len(args) > 1 && args[0] == "api" && args[1] == "graphql" {
			if resolved(e.viewN) {
				return resolvedThreadFor(id), "", nil
			}
			return unresolvedThreadFor(id), "", nil
		}
		return base(args...)
	}
}

// hdStop is `cenci babysit stop` for a supervisor that is already gone (a killed
// process, a rebooted host): its recorded pid is provably dead, so the stop only
// rewrites the state. It returns the stopped state read back from disk.
func hdStop(t *testing.T, e *ciCapEnv) State {
	t.Helper()
	stubProcessOwned(t, false)
	stubProcessGone(t, true)
	if err := StopRepo("o/r", "42", e.dir); err != nil {
		t.Fatalf("StopRepo: %v", err)
	}
	stopped := mustReadState(t, e.path)
	if stopped.Status != "stopped" || stopped.PID != 0 {
		t.Fatalf("state after stop = status %q pid %d, want stopped with no pid", stopped.Status, stopped.PID)
	}
	return stopped
}

// hdArm is `cenci babysit 42 --agent claude`: the arming branch of Run reads the
// state strictly, writes status `arming`, and starts the supervisor child, which
// the stub records instead of spawning. It returns the armed state and the number
// of supervisors started, and leaves the environment as the child sees it.
func hdArm(t *testing.T, e *ciCapEnv) (State, int) {
	t.Helper()
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
	started := 0
	original := startSupervisor
	startSupervisor = func(*exec.Cmd) error { started++; return nil }
	t.Cleanup(func() { startSupervisor = original })
	done := captureStdout(t)
	err := Run(e.opts(false))
	_ = done()
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
	if err != nil {
		t.Fatalf("arming Run: %v", err)
	}
	return mustReadState(t, e.path), started
}

// P2: unresolved inline feedback survives a stop and a re-arm. The re-armed
// supervisor restores comment:7 as pending, does not relaunch a worker for it
// (it is still recorded as launched), keeps the review-stage hold, and releases
// it only when GitHub reports the thread resolved.
func TestHoldDurability_P2_InlineFeedbackStaysHeldAcrossAStopAndReArmUntilGitHubResolvesIt(t *testing.T) {
	e := newRWEnv(t, true, nil,
		hdInline("head-a"), // 1: detected, launched
		hdInline("head-a"), // 2: thread unresolved; killed after this save
		rwGreen("head-a"),  // 3: the arming parent's own pr view
		hdInline("head-a"), // 4: re-armed, thread still unresolved
		hdInline("head-a"), // 5: thread resolved on GitHub
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	hdThreads(e, 7, func(view int) bool { return view >= 5 })
	e.seed(State{Status: "running"})

	e.runKilledAtSleep(2)
	running := load(e.path)
	rwPending(t, "before the stop", running, []string{"comment:7"}, []string{"comment:7"})
	if running.AutomergeReason != reasonReviewPending {
		t.Fatalf("before the stop: reason %q, want %q", running.AutomergeReason, reasonReviewPending)
	}

	stopped := hdStop(t, e)
	rwPending(t, "stopped file", stopped, []string{"comment:7"}, []string{"comment:7"})
	armed, started := hdArm(t, e)
	rwPending(t, "armed state", armed, []string{"comment:7"}, []string{"comment:7"})
	if armed.Status != "arming" || started != 1 {
		t.Errorf("armed state: status %q supervisors started %d, want arming and one start", armed.Status, started)
	}
	if err := e.run(); err != nil {
		t.Fatalf("re-armed Run: %v", err)
	}

	held := e.after(4)
	rwPending(t, "after the first re-armed tick", held, []string{"comment:7"}, []string{"comment:7"})
	if held.AutomergeReason != reasonReviewPending || held.AutomergeDecision != "held" {
		t.Errorf("after the first re-armed tick: automerge %q (%q), want held for %q", held.AutomergeDecision, held.AutomergeReason, reasonReviewPending)
	}
	released := e.after(5)
	if len(released.PendingKeys) != 0 || !slices.Equal(released.AddressedKeys, []string{"comment:7"}) || !hasCondition(released.AutomergeConditions, "review", true) {
		t.Errorf("after GitHub resolved the thread: pending %v addressed %v conditions %v, want comment:7 addressed and the review stage passed", released.PendingKeys, released.AddressedKeys, released.AutomergeConditions)
	}
	runs.requireAt(t, 1, "babysit-review")
	for _, tick := range []int{2, 4, 5} {
		runs.requireAt(t, tick)
	}
}

// P2: a pending discussion comment survives a stop and a re-arm with its
// revision and outstanding launch. The worker's window ended while supervision
// was down, so the re-armed supervisor holds under `review worker ended without
// receipt` (no relaunch for the same revision) until the nonce-bound receipt
// arrives, which is the authoritative evidence that releases it.
func TestHoldDurability_P2_DiscussionStaysHeldAcrossAStopAndReArmUntilAReceiptArrives(t *testing.T) {
	receipt := rwReceipt(12, testReviewNonce, rwEntry(11, humanBody, "addressed"))
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", humanComment(11, humanBody)),          // 1: detected, launched
		rwGreen("head-a", humanComment(11, humanBody)),          // 2: window live; killed after this save
		rwGreen("head-a", humanComment(11, humanBody)),          // 3: the arming parent's own pr view
		rwGreen("head-a", humanComment(11, humanBody)),          // 4: re-armed, window ended, no receipt
		rwGreen("head-a", humanComment(11, humanBody), receipt), // 5: the receipt arrives
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	e.panes = panesByTick("", liveReview(), "", deadReview(), deadReview())
	e.seed(State{Status: "running"})

	e.runKilledAtSleep(2)
	stopped := hdStop(t, e)
	rwPending(t, "stopped file", stopped, []string{"discussion:11"}, []string{"discussion:11"})
	if stopped.DiscussionRevisions["discussion:11"] != bodyRevision(humanBody) || stopped.ReviewLaunch == nil || stopped.ReviewLaunch.Nonce != testReviewNonce {
		t.Errorf("stopped file: revisions %v launch %+v, want the revision and the outstanding launch kept", stopped.DiscussionRevisions, stopped.ReviewLaunch)
	}
	armed, started := hdArm(t, e)
	rwPending(t, "armed state", armed, []string{"discussion:11"}, []string{"discussion:11"})
	if started != 1 {
		t.Errorf("supervisors started = %d, want 1", started)
	}
	if err := e.run(); err != nil {
		t.Fatalf("re-armed Run: %v", err)
	}

	held := e.after(4)
	rwPending(t, "after the first re-armed tick", held, []string{"discussion:11"}, []string{"discussion:11"})
	if held.AutomergeReason != reasonReviewWorkerEndedWithoutReceipt || held.AutomergeDecision != "held" {
		t.Errorf("after the first re-armed tick: automerge %q (%q), want held for %q", held.AutomergeDecision, held.AutomergeReason, reasonReviewWorkerEndedWithoutReceipt)
	}
	released := e.after(5)
	if len(released.PendingKeys) != 0 || !slices.Equal(released.AddressedKeys, []string{"discussion:11"}) || !hasCondition(released.AutomergeConditions, "review", true) {
		t.Errorf("after the receipt: pending %v addressed %v conditions %v, want discussion:11 addressed and the review stage passed", released.PendingKeys, released.AddressedKeys, released.AutomergeConditions)
	}
	if got := released.DiscussionReceipts["discussion:11"]; got.Source != receiptSourceReceipt || got.ReceiptCommentID != 12 {
		t.Errorf("receipt record = %+v, want the receipt comment 12 recorded", got)
	}
	runs.requireAt(t, 1, "babysit-review")
	runs.requireAt(t, 4)
	runs.requireAt(t, 5)
}

// P19: comments posted while supervision is down -- one in the PR conversation,
// one inline -- are collected on the first tick after the re-arm, hold the
// merge, and are handed to exactly one worker launch.
func TestHoldDurability_P19_CommentsPostedWhileSupervisionIsDownAreCollectedAndHoldTheMerge(t *testing.T) {
	e := newRWEnv(t, true, nil,
		rwGreen("head-a"), // 1: nothing to address; killed after this save
		rwGreen("head-a"), // 2: the arming parent's own pr view
		ciCapStep{view: ciCapOpen("head-a", rwIssue), checks: ciCapGreen, comments: hdLateInlineComment, discussion: jsonArray(humanComment(15, "posted while the supervisor was down"))}, // 3
		ciCapStep{view: ciCapOpen("head-a", rwIssue), checks: ciCapGreen, comments: hdLateInlineComment, discussion: jsonArray(humanComment(15, "posted while the supervisor was down"))}, // 4
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	hdThreads(e, 9, func(int) bool { return false })
	e.seed(State{Status: "running"})

	e.runKilledAtSleep(1)
	if quiet := load(e.path); len(quiet.PendingKeys) != 0 || !hasCondition(quiet.AutomergeConditions, "review", true) {
		t.Fatalf("before the stop: pending %v conditions %v, want nothing pending and the review stage passed", quiet.PendingKeys, quiet.AutomergeConditions)
	}
	hdStop(t, e)
	viewsWhileDown := e.viewN
	if _, started := hdArm(t, e); started != 1 {
		t.Fatalf("supervisors started = %d, want 1", started)
	}
	if e.viewN != viewsWhileDown+1 {
		t.Errorf("pr views while supervision was down = %d, want only the arming parent's own read", e.viewN-viewsWhileDown)
	}
	if err := e.run(); err != nil {
		t.Fatalf("re-armed Run: %v", err)
	}

	for _, tick := range []int{3, 4} {
		s := e.after(tick)
		// The set matters, not the order the two key families were appended in.
		if !slices.Equal(slices.Sorted(slices.Values(s.PendingKeys)), []string{"comment:9", "discussion:15"}) {
			t.Errorf("after tick %d: pending = %v, want both late comments collected", tick, s.PendingKeys)
		}
		if s.AutomergeDecision != "held" {
			t.Errorf("after tick %d: automerge decision %q, want held", tick, s.AutomergeDecision)
		}
	}
	if s := e.after(3); s.AutomergeReason != reasonReviewPending {
		t.Errorf("after the collecting tick: reason %q, want %q", s.AutomergeReason, reasonReviewPending)
	}
	// The launched worker's window has ended with no receipt: the hold stays, under
	// its own reason, and nothing is relaunched for the same revisions.
	if s := e.after(4); s.AutomergeReason != reasonReviewWorkerEndedWithoutReceipt {
		t.Errorf("after the next tick: reason %q, want %q", s.AutomergeReason, reasonReviewWorkerEndedWithoutReceipt)
	}
	runs.requireAt(t, 3, "babysit-review")
	runs.requireAt(t, 4)
	if got, want := runs.argAt(t, 3, "babysit-review"), "42 --receipt-nonce "+testReviewNonce+" discussion:15="+bodyRevision("posted while the supervisor was down"); got != want {
		t.Errorf("launch argument = %q, want %q", got, want)
	}
}

// P3: an unreadable or undecodable state at re-arm is a named hold, never an
// empty PendingKeys. `status` prints `babysit state unreadable` with the file
// path; the arm exits with an error without overwriting the file or starting a
// supervisor; a restarted supervisor child refuses before its first tick; and
// `cenci babysit stop`, which the message names, is the documented recovery.
func TestHoldDurability_P3_UnreadableStateIsANamedHoldAndNeverStartsFromEmptyPendingKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{"truncated JSON", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"schemaVersion":2,"pr":"42","repo":"o/r","pendingCommentKeys":["discussion:11"`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"an empty file", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a directory where the file should be", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRWEnv(t, true, nil, rwGreen("head-a", humanComment(11, humanBody)))
			runs := rwTrackRuns(e, 0)
			// The harness's own pr view insists on a readable state file (so a zero
			// State can never pass a negative assertion vacuously); here the state is
			// unreadable on purpose, so the PR is served by a plain fake that only
			// counts the reads. Every other call still goes to the harness.
			harness := execGh
			execGh = func(args ...string) (string, string, error) {
				if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
					e.viewN++
					return ciCapOpen("head-a", rwIssue), "", nil
				}
				return harness(args...)
			}
			tc.write(t, e.path)
			before, beforeErr := os.ReadFile(e.path)

			// status names the hold and the path.
			err := Status("42", e.dir)
			if err == nil || !strings.HasPrefix(err.Error(), reasonStateUnreadable+": "+e.path+": ") {
				t.Fatalf("Status err = %v, want %q followed by the path", err, reasonStateUnreadable+": "+e.path+": ")
			}

			// the arm refuses without touching the file or starting a supervisor.
			t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
			started := 0
			originalStart := startSupervisor
			startSupervisor = func(*exec.Cmd) error { started++; return nil }
			t.Cleanup(func() { startSupervisor = originalStart })
			err = Run(e.opts(false))
			if err == nil || !strings.Contains(err.Error(), reasonStateUnreadable) || !strings.Contains(err.Error(), e.path) {
				t.Fatalf("arming Run err = %v, want %q naming %s", err, reasonStateUnreadable, e.path)
			}
			if started != 0 {
				t.Errorf("supervisors started = %d, want 0", started)
			}

			// a restarted supervisor child refuses before its first tick.
			t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
			viewsBefore := e.viewN
			err = Run(e.opts(true))
			if err == nil || !strings.Contains(err.Error(), reasonStateUnreadable) {
				t.Fatalf("supervisor child Run err = %v, want %q", err, reasonStateUnreadable)
			}
			if e.viewN != viewsBefore {
				t.Errorf("the supervisor child read the PR %d times, want 0: it must not tick over an unreadable state", e.viewN-viewsBefore)
			}
			after, afterErr := os.ReadFile(e.path)
			if (beforeErr == nil) != (afterErr == nil) || !bytes.Equal(before, after) {
				t.Errorf("the unreadable state changed: %q -> %q (errors %v -> %v)", before, after, beforeErr, afterErr)
			}
			runs.requireNone(t)

			// the recovery the message names: stop removes the file, a fresh arm follows.
			t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
			done := captureStdout(t)
			stderrDone := captureStderr(t)
			stopErr := Stop("42", e.dir)
			stderr := stderrDone()
			_ = done()
			if stopErr != nil {
				t.Fatalf("Stop: %v", stopErr)
			}
			if _, statErr := os.Stat(e.path); !os.IsNotExist(statErr) {
				t.Fatalf("after stop: stat err = %v, want the unreadable file removed", statErr)
			}
			// Discarding the feedback bookkeeping is never silent: the note names the
			// reason, the path and what was lost.
			if !strings.Contains(stderr, reasonStateUnreadable) || !strings.Contains(stderr, e.path) || !strings.Contains(stderr, "discarded") {
				t.Errorf("stop stderr = %q, want a note naming %q, the path %s and the discarded bookkeeping", stderr, reasonStateUnreadable, e.path)
			}
			if armed, startedNow := hdArm(t, e); armed.Status != "arming" || startedNow != 1 || len(armed.PendingKeys) != 0 {
				t.Errorf("fresh arm after the recovery: status %q starts %d pending %v, want arming, one start and a deliberately fresh state", armed.Status, startedNow, armed.PendingKeys)
			}
		})
	}
}

// Upgrade: a schema-v2 state file written before the discussion fields existed
// loads, and on its first tick every non-excluded human discussion comment with
// no matching receipt is pending; `[bot]` logins and cenci-banner comments
// (every receipt comment included) are not, and one worker is launched for them.
func TestHoldDurability_SchemaV2StateUpgradesAndTheFirstTickRedetectsHumanDiscussion(t *testing.T) {
	const v2 = `{"schemaVersion":2,"pr":"42","repo":"o/r","agent":"claude","intervalSeconds":60,"currentDelaySeconds":60,` +
		`"lastCiHeadSha":"head-a","status":"running","ciStatus":"green","updatedAt":"2026-11-01T00:00:00Z"}`
	stray := rwReceipt(18, testWrongNonce, rwEntry(11, "first ask", "addressed"))
	bot := discussionJSON(17, "dependabot[bot]", "bump the lockfile", "2026-11-10T11:00:00Z", "2026-11-10T11:00:00Z")
	e := newRWEnv(t, true, nil,
		rwGreen("head-a", humanComment(11, "first ask"), humanComment(12, "second ask"), bot, stray),
		rwGreen("head-a", humanComment(11, "first ask"), humanComment(12, "second ask"), bot, stray),
		ciCapMerged(rwIssue),
	)
	runs := rwTrackRuns(e, 0)
	if err := os.WriteFile(e.path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := readStateStrict(e.path)
	if err != nil || loaded.SchemaVersion != 2 || len(loaded.DiscussionRevisions) != 0 || loaded.ReviewLaunch != nil || len(loaded.ConsumedReviewNonces) != 0 {
		t.Fatalf("v2 file: err %v state %+v, want it to load with every discussion field empty", err, loaded)
	}

	if err := e.run(); err != nil {
		t.Fatalf("Run over the v2 file: %v", err)
	}

	first := e.after(1)
	wantRevisions := map[string]string{"discussion:11": bodyRevision("first ask"), "discussion:12": bodyRevision("second ask")}
	if !slices.Equal(first.PendingKeys, []string{"discussion:11", "discussion:12"}) || !reflect.DeepEqual(first.DiscussionRevisions, wantRevisions) {
		t.Errorf("after the first tick: pending %v revisions %v, want the two human comments, and neither the bot nor the receipt comment", first.PendingKeys, first.DiscussionRevisions)
	}
	if first.AutomergeReason != reasonReviewPending || first.SchemaVersion != 2 {
		t.Errorf("after the first tick: reason %q schema %d, want %q at schema 2 (no bump)", first.AutomergeReason, first.SchemaVersion, reasonReviewPending)
	}
	runs.requireAt(t, 1, "babysit-review")
	if got, want := runs.argAt(t, 1, "babysit-review"), "42 --receipt-nonce "+testReviewNonce+" discussion:11="+bodyRevision("first ask")+" discussion:12="+bodyRevision("second ask"); got != want {
		t.Errorf("launch argument = %q, want %q", got, want)
	}
	runs.requireAt(t, 2)
}

// Round trip: the discussion fields a supervisor holds -- pending and addressed
// discussion keys with their revisions, the per-key receipts, the outstanding
// launch nonce with its time and key set, and the consumed nonces -- survive a
// save and reload exactly, re-save to identical bytes, and pass unchanged through
// a supervisor's load, tick and save.
func TestHoldDurability_DiscussionFieldsSurviveSaveReloadAndATickExactly(t *testing.T) {
	revA, revB, revC := bodyRevision("a"), bodyRevision("b"), bodyRevision("c")
	want := State{
		SchemaVersion: stateSchemaVersion, PR: "42", Repo: "o/r", Agent: "claude", IntervalSeconds: 60, CurrentDelaySeconds: 60,
		Status: "running", LaunchSession: "work", LaunchDir: ownedRepoDir,
		PendingKeys:   []string{"discussion:11", "discussion:13"},
		LaunchedKeys:  []string{"discussion:11"},
		AddressedKeys: []string{"discussion:9", "discussion:10"},
		DiscussionRevisions: map[string]string{
			"discussion:9": revA, "discussion:10": revB, "discussion:11": revC, "discussion:13": bodyRevision("d"),
		},
		DiscussionReceipts: map[string]discussionReceipt{
			"discussion:9":  {CommentID: 9, Revision: revA, HeadSHA: testReceiptHead, Outcome: receiptOutcomeAddressed, ReceiptCommentID: 8, Source: receiptSourceReceipt},
			"discussion:10": {CommentID: 10, Revision: revB, Outcome: receiptOutcomeAddressed, Source: receiptSourceHumanCLI},
		},
		ReviewLaunch:         &reviewLaunchRecord{Nonce: testReviewNonce, LaunchedAt: testLaunchAt, Keys: []string{"discussion:11"}},
		ConsumedReviewNonces: map[string]int64{testWrongNonce: 8, "cccccccccccccccccccccccccccccccc": 0},
	}

	// save -> reload -> save.
	path := filepath.Join(t.TempDir(), "state.json")
	if err := save(path, want); err != nil {
		t.Fatal(err)
	}
	firstBytes := readFileBytes(t, path)
	got, err := readStateStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded state differs:\n got %+v\nwant %+v", got, want)
	}
	if err := save(path, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFileBytes(t, path), firstBytes) {
		t.Error("re-saving the reloaded state changed the file")
	}
	for _, field := range []string{`"discussionRevisions"`, `"discussionReceipts"`, `"reviewLaunch"`, `"consumedReviewNonces"`, `"receiptCommentId"`, `"launchedAt"`} {
		if !bytes.Contains(firstBytes, []byte(field)) {
			t.Errorf("the state file lacks %s", field)
		}
	}

	// load -> tick -> save: a live review window changes nothing about the
	// discussion fields (every key keeps its recorded revision, no receipt moves).
	e := newRWEnv(t, false, nil,
		rwGreen("head-a", humanComment(9, "a"), humanComment(10, "b"), humanComment(11, "c"), humanComment(13, "d")),
		ciCapMerged(rwIssue),
	)
	rwTrackRuns(e, 0)
	e.panes = func(int) (string, error) { return liveReview(), nil }
	e.seed(want)
	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	after := e.after(1)
	for name, pair := range map[string][2]any{
		"pending":   {after.PendingKeys, want.PendingKeys},
		"launched":  {after.LaunchedKeys, want.LaunchedKeys},
		"addressed": {after.AddressedKeys, want.AddressedKeys},
		"revisions": {after.DiscussionRevisions, want.DiscussionRevisions},
		"receipts":  {after.DiscussionReceipts, want.DiscussionReceipts},
		"launch":    {after.ReviewLaunch, want.ReviewLaunch},
		"consumed":  {after.ConsumedReviewNonces, want.ConsumedReviewNonces},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s after load, tick and save = %v, want %v", name, pair[0], pair[1])
		}
	}
}
