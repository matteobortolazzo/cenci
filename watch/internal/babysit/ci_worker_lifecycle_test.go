package babysit

// #1457: the CI retry episode is gated on worker liveness, not on a permanent
// "a launch happened for this SHA" marker. These fixtures drive the real Run
// loop over the ciCapEnv harness (ci_cap_lifecycle_test.go), whose panes hook
// answers each tick's single tmux pane inventory, so every launch decision goes
// through the real inventory parser and ownership rules. A pane row is owned
// when its repo-dir is the recorded /repo/root (LaunchDir and RepoRoot).

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const ownedRepoDir = "/repo/root"

func liveCI() string { return paneRowOf("42-ci-repair", false, ownedRepoDir) }
func deadCI() string { return paneRowOf("42-ci-repair", true, ownedRepoDir) }

// panesByTick serves tick (or stubPanes read) k the k-th inventory; "" or past the list is none.
func panesByTick(inventories ...string) func(int) (string, error) {
	return func(tick int) (string, error) {
		if tick >= 1 && tick <= len(inventories) && inventories[tick-1] != "" {
			return inventories[tick-1], nil
		}
		return noWorkerInventory, nil
	}
}

// runOrder lists the workflow of every `cenci run` call, in call order.
func runOrder(calls [][]string) []string {
	var order []string
	for _, c := range calls {
		if len(c) > 2 && c[1] == "run" {
			order = append(order, c[2])
		}
	}
	return order
}

// trackedAt is a supervisor with attempts ci-repair launches dispatched under
// #1457, the last at head.
func trackedAt(attempts int, head string) State {
	return State{
		FixAttempts: attempts, RepairPending: true, LastHeadSHA: head, LastRepairedSHA: head, CIRepairTracked: true,
		CIStatus: ciStatusFailing, Status: "running",
	}
}

func failingAt(head string) ciCapStep { return ciCapTickOf(ciCapOpen(head), ciCapFailing) }

// AC 1: at one unchanged failing head, each ended worker is retried until the
// cap, then exactly one CI retry cap attention opens and Run keeps polling.
func TestCIWorker_SameFailingHeadRetriesEachEndedWorkerToTheCap(t *testing.T) {
	e := newCICapEnv(t,
		failingAt("head-a"), // 1: attempt 1
		failingAt("head-a"), // 2: live
		failingAt("head-a"), // 3: live
		failingAt("head-a"), // 4: dead pane retained: attempt 2
		failingAt("head-a"), // 5: live
		failingAt("head-a"), // 6: absent: attempt 3
		failingAt("head-a"), // 7: live
		failingAt("head-a"), // 8: dead: cap attention
		failingAt("head-a"), // 9: already notified
		ciCapMerged(),
	)
	e.panes = panesByTick("", liveCI(), liveCI(), deadCI(), liveCI(), "", liveCI(), deadCI(), deadCI())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"ci-repair", "ci-repair", "ci-repair", "babysit-attention"}
	if got := runOrder(e.calls); !slices.Equal(got, want) {
		t.Fatalf("cenci run calls = %q, want %q", got, want)
	}
	for tick, attempts := range map[int]int{1: 1, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3, 8: 3, 9: 3} {
		if got := e.after(tick).FixAttempts; got != attempts {
			t.Errorf("tick %d: ciFixAttempts = %d, want %d", tick, got, attempts)
		}
	}
	if s := e.after(7); s.CIRepairPaused || s.CICapNotified {
		t.Errorf("tick 7 (third worker live): ciRepairPaused=%v ciCapNotified=%v, want neither", s.CIRepairPaused, s.CICapNotified)
	}
	if s := e.after(8); !s.CIRepairPaused || !s.CICapNotified || s.Status != "needs-input" {
		t.Errorf("tick 8: ciRepairPaused=%v ciCapNotified=%v status=%q, want true/true/needs-input", s.CIRepairPaused, s.CICapNotified, s.Status)
	}
	if arg, _ := launchArg(e.calls, "babysit-attention"); arg != "42 CI retry cap reached" {
		t.Errorf("attention arg = %q, want %q", arg, "42 CI retry cap reached")
	}
	if e.viewN != 10 {
		t.Errorf("pr view calls = %d, want 10: Run must keep polling after the cap", e.viewN)
	}
	for tick := 1; tick <= 9; tick++ {
		if e.inventoryReads[tick] != 1 {
			t.Errorf("tick %d: inventory reads = %d, want exactly 1", tick, e.inventoryReads[tick])
		}
	}
}

// AC 2: a new failing head while this PR's CI worker is live launches nothing;
// once the pane ends, the next failing tick launches one attempt.
func TestCIWorker_HeadChangeDuringALiveWorkerWaitsForIt(t *testing.T) {
	e := newCICapEnv(t, failingAt("head-b"), failingAt("head-b"), ciCapMerged())
	e.panes = panesByTick(liveCI(), deadCI())
	e.seed(trackedAt(1, "head-a"))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if s := e.after(1); s.FixAttempts != 1 || s.LastRepairedSHA != "head-a" {
		t.Errorf("tick 1 at a new head with a live worker: ciFixAttempts=%d lastRepaired=%q, want 1/head-a", s.FixAttempts, s.LastRepairedSHA)
	}
	if got := runOrder(e.calls); !slices.Equal(got, []string{"ci-repair"}) {
		t.Fatalf("cenci run calls = %q, want one ci-repair after the worker ended", got)
	}
	if s := e.after(2); s.FixAttempts != 2 || s.LastRepairedSHA != "head-b" || !s.CIRepairTracked {
		t.Errorf("tick 2: ciFixAttempts=%d lastRepaired=%q tracked=%v, want 2/head-b/true", s.FixAttempts, s.LastRepairedSHA, s.CIRepairTracked)
	}
}

// AC 3: the worker pushes head B; pending at B neither resets nor launches,
// failing at B while the worker is live launches nothing, and after it ends
// exactly one ci-repair follows for B with the supervisor's recorded agent.
func TestCIWorker_PushThenFailRelaunchesWithTheRecordedAgent(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			e := newCICapEnv(t,
				ciCapTickOf(ciCapOpen("head-b"), ciCapPending), // 1: pushed, CI pending
				failingAt("head-b"),                            // 2: failing, worker still live
				failingAt("head-b"),                            // 3: worker ended
				ciCapMerged(),
			)
			e.agent = agent
			e.panes = panesByTick(liveCI(), liveCI(), deadCI())
			e.seed(trackedAt(1, "head-a"))

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			for _, tick := range []int{1, 2} {
				if s := e.after(tick); s.FixAttempts != 1 || !s.RepairPending {
					t.Errorf("tick %d: ciFixAttempts=%d ciRepairPending=%v, want 1/true (no reset, no launch)", tick, s.FixAttempts, s.RepairPending)
				}
			}
			if got := runOrder(e.calls); !slices.Equal(got, []string{"ci-repair"}) {
				t.Fatalf("cenci run calls = %q, want exactly one ci-repair", got)
			}
			args, _ := launchCallArgs(e.calls, "ci-repair")
			assertFlagValue(t, args, "--agent", agent)
			if s := e.after(3); s.FixAttempts != 2 || s.LastRepairedSHA != "head-b" {
				t.Errorf("tick 3: ciFixAttempts=%d lastRepaired=%q, want 2/head-b", s.FixAttempts, s.LastRepairedSHA)
			}
		})
	}
}

// AC 4: the PR turns conflicting while its CI worker is live: no merge-repair
// (the episode is recorded). After the CI worker ends merge-repair launches
// first; pending review keys wait and the CI budget is untouched. A conflict
// after the known-clear release starts a fresh conflict episode.
func TestCIWorker_SiblingConflictWaitsForTheCIWorkerThenMergeRepairGoesFirst(t *testing.T) {
	const comment = `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	e := newCICapEnv(t,
		ciCapStep{view: ciCapDirty("head-a"), checks: ciCapFailing, comments: comment}, // 1: CI worker live
		ciCapStep{view: ciCapDirty("head-a"), checks: ciCapFailing, comments: comment}, // 2: CI worker ended
		ciCapStep{view: ciCapOpen("head-a"), checks: ciCapGreen, comments: comment},    // 3: released, green
		ciCapStep{view: ciCapDirty("head-a"), checks: ciCapGreen, comments: comment},   // 4: a new conflict
		ciCapMerged(),
	)
	e.panes = panesByTick(liveCI(), deadCI())
	e.seed(trackedAt(1, "head-a"))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	held := e.after(1)
	if held.ConflictRepairSHA != "head-a" || held.ConflictFixAttempts != 0 || held.FixAttempts != 1 {
		t.Errorf("tick 1: conflictRepairSha=%q conflictFixAttempts=%d ciFixAttempts=%d, want head-a/0/1 (episode recorded, nothing launched)",
			held.ConflictRepairSHA, held.ConflictFixAttempts, held.FixAttempts)
	}
	for _, tick := range []int{1, 2} {
		s := e.after(tick)
		if !slices.Equal(s.PendingKeys, []string{"comment:7"}) || len(s.LaunchedKeys) != 0 || s.FixAttempts != 1 {
			t.Errorf("tick %d: pending=%v launched=%v ciFixAttempts=%d, want [comment:7] pending, unlaunched, budget 1", tick, s.PendingKeys, s.LaunchedKeys, s.FixAttempts)
		}
	}
	if s := e.after(2); s.ConflictFixAttempts != 1 {
		t.Errorf("tick 2: conflictFixAttempts = %d, want 1", s.ConflictFixAttempts)
	}
	want := []string{"merge-repair", "babysit-review", "merge-repair"}
	if got := runOrder(e.calls); !slices.Equal(got, want) {
		t.Fatalf("cenci run calls = %q, want %q", got, want)
	}
	if s := e.after(3); s.ConflictRepairSHA != "" || s.ConflictFixAttempts != 0 || s.FixAttempts != 0 {
		t.Errorf("tick 3: conflict episode %q/%d ciFixAttempts=%d, want released and the green reset", s.ConflictRepairSHA, s.ConflictFixAttempts, s.FixAttempts)
	}
	if s := e.after(4); s.ConflictFixAttempts != 1 {
		t.Errorf("tick 4: conflictFixAttempts = %d, want 1 (a fresh episode)", s.ConflictFixAttempts)
	}
}

// AC 6: failing CI and new feedback on one tick launch only ci-repair; nothing
// launches while that worker is live; address-review launches on the first
// tick with no live writer where CI repair is not eligible (green).
func TestCIWorker_SameTickCIAndFeedbackLaunchesOnlyCIRepair(t *testing.T) {
	const comment = `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	e := newCICapEnv(t,
		ciCapStep{view: ciCapOpen("head-a"), checks: ciCapFailing, comments: comment}, // 1
		ciCapStep{view: ciCapOpen("head-a"), checks: ciCapFailing, comments: comment}, // 2: live
		ciCapStep{view: ciCapOpen("head-b"), checks: ciCapGreen, comments: comment},   // 3: ended, green
		ciCapMerged(),
	)
	e.panes = panesByTick("", liveCI(), deadCI())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := runOrder(e.calls); !slices.Equal(got, []string{"ci-repair", "babysit-review"}) {
		t.Fatalf("cenci run calls = %q, want ci-repair then babysit-review", got)
	}
	for _, tick := range []int{1, 2} {
		if s := e.after(tick); !slices.Equal(s.PendingKeys, []string{"comment:7"}) || len(s.LaunchedKeys) != 0 {
			t.Errorf("tick %d: pending=%v launched=%v, want [comment:7] pending and unlaunched", tick, s.PendingKeys, s.LaunchedKeys)
		}
	}
	if s := e.after(3); !slices.Equal(s.LaunchedKeys, []string{"comment:7"}) {
		t.Errorf("tick 3: launched=%v, want [comment:7]", s.LaunchedKeys)
	}
}

// AC 7: a green rerun at the failed head while its CI worker is live keeps the
// episode and holds automerge on the worker; the first green tick after the
// worker ends resets it.
func TestCIWorker_SameHeadGreenWhileTheWorkerIsLiveHoldsThenResets(t *testing.T) {
	const issue = 782
	withFleetAutomergeEnabled(t, true)
	e := newCICapEnv(t,
		ciCapTickOf(ciCapOpen("head-a", issue), ciCapGreen),
		ciCapTickOf(ciCapOpen("head-a", issue), ciCapGreen),
		ciCapMerged(issue),
	)
	e.panes = panesByTick(liveCI(), deadCI())
	e.seed(trackedAt(1, "head-a"))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	held := e.after(1)
	if held.FixAttempts != 1 || !held.RepairPending {
		t.Errorf("tick 1: ciFixAttempts=%d ciRepairPending=%v, want 1/true", held.FixAttempts, held.RepairPending)
	}
	if held.AutomergeDecision != "held" || held.AutomergeReason != reasonRepairWorkerLive {
		t.Errorf("tick 1 automerge = %q (%q), want held (%q)", held.AutomergeDecision, held.AutomergeReason, reasonRepairWorkerLive)
	}
	if s := e.after(2); s.FixAttempts != 0 || s.RepairPending {
		t.Errorf("tick 2: ciFixAttempts=%d ciRepairPending=%v, want 0/false", s.FixAttempts, s.RepairPending)
	}
	if n := len(runOrder(e.calls)); n != 0 {
		t.Errorf("cenci run calls = %d, want none", n)
	}
}

// AC 8: each negative fixture asserts the exact launches and attempt counts.
// An unreadable, empty or malformed inventory fails the tick with `workflow
// launch failed`; a live 42-ci-repair of another repository does not block;
// a manual one in this repository does; sibling names never count.
func TestCIWorker_InventoryNegatives(t *testing.T) {
	for _, tc := range []struct {
		name         string
		inventory    func(int) (string, error)
		wantErr      bool
		wantLaunches int
	}{
		{"inventory command error", func(int) (string, error) { return "", errors.New("tmux: server exited") }, true, 0},
		{"empty inventory", func(int) (string, error) { return "", nil }, true, 0},
		{"wrong column count", func(int) (string, error) { return "42-ci-repair\t0\n", nil }, true, 0},
		{"pane_dead not 0 or 1", func(int) (string, error) { return "42-ci-repair\tyes\t/repo/root\n", nil }, true, 0},
		{"foreign live 42-ci-repair does not block", panesAlways(paneRowOf("42-ci-repair", false, "/elsewhere/repo")), false, 1},
		{"manual live 42-ci-repair in this repository suppresses", panesAlways(liveCI()), false, 0},
		{"sibling names never count", panesAlways(paneRowOf("42-ci-repair-old", false, ownedRepoDir) + paneRowOf("142-ci-repair", false, ownedRepoDir)), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFleetAutomergeEnabled(t, true)
			stubGitCommonDir(t, map[string]string{ownedRepoDir: "/repo/root/.git", "/elsewhere/repo": "/elsewhere/repo/.git"})
			e := newCICapEnv(t, failingAt("head-a"), ciCapMerged())
			e.panes = tc.inventory
			e.seed(State{Status: "running"})

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			s := e.after(1)
			if n := len(runOrder(e.calls)); n != tc.wantLaunches || s.FixAttempts != tc.wantLaunches {
				t.Errorf("cenci run calls=%d ciFixAttempts=%d, want %d/%d", n, s.FixAttempts, tc.wantLaunches, tc.wantLaunches)
			}
			if tc.wantErr && (s.Status != "retrying" || s.AutomergeReason != reasonWorkflowLaunchFailed) {
				t.Errorf("status=%q automergeReason=%q, want retrying/%q", s.Status, s.AutomergeReason, reasonWorkflowLaunchFailed)
			}
			if e.inventoryReads[1] != 1 {
				t.Errorf("inventory reads = %d, want 1", e.inventoryReads[1])
			}
		})
	}
}

// AC 8: a pre-spawn launch refusal consumes nothing and the next tick retries;
// a `cenci run` exit 3 consumes one attempt and the live pane blocks a second.
func TestCIWorker_LaunchOutcomesConsumeOnlyDispatchedAttempts(t *testing.T) {
	t.Run("pre-spawn refusal", func(t *testing.T) {
		e := newCICapEnv(t, failingAt("head-a"), failingAt("head-a"), ciCapMerged())
		e.sessionUp = func(attempt int) bool { return attempt > 1 }
		e.seed(State{Status: "running"})
		if err := e.run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if s := e.after(1); s.FixAttempts != 0 || s.Status != "retrying" {
			t.Errorf("tick 1: ciFixAttempts=%d status=%q, want 0/retrying", s.FixAttempts, s.Status)
		}
		if s := e.after(2); s.FixAttempts != 1 || len(runOrder(e.calls)) != 1 {
			t.Errorf("tick 2: ciFixAttempts=%d cenci run calls=%d, want 1/1", s.FixAttempts, len(runOrder(e.calls)))
		}
	})
	t.Run("exit 3", func(t *testing.T) {
		e := newCICapEnv(t, failingAt("head-a"), failingAt("head-a"), ciCapMerged())
		inner := command
		command = func(name string, args ...string) ([]byte, error) {
			if len(args) > 1 && args[0] == "run" && args[1] == "ci-repair" {
				e.calls = append(e.calls, append([]string{name}, args...))
				return exec.Command("sh", "-c", "echo 'window spawned'; exit 3").CombinedOutput()
			}
			return inner(name, args...)
		}
		e.panes = panesByTick("", liveCI())
		e.seed(State{Status: "running"})
		if err := e.run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if s := e.after(2); s.FixAttempts != 1 || len(runOrder(e.calls)) != 1 {
			t.Errorf("after the live tick: ciFixAttempts=%d cenci run calls=%d, want 1/1", s.FixAttempts, len(runOrder(e.calls)))
		}
	})
}

// AC 9: a live 42-ci-repair with an empty @cenci-repo-dir is a writer of unknown
// ownership: it suppresses the launch and the status names its session:window
// and the recovery instruction (even with automerge disabled); once the pane is
// dead it stops blocking.
func TestCIWorker_UnknownOwnershipBlocksAndReportsRecovery(t *testing.T) {
	e := newCICapEnv(t, failingAt("head-a"), failingAt("head-a"), ciCapMerged())
	e.panes = panesByTick(paneRowOf("42-ci-repair", false, ""), paneRowOf("42-ci-repair", true, ""))
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := e.after(1)
	if s.FixAttempts != 0 || s.AutomergeReason != reasonAutomergeDisabled {
		t.Errorf("tick 1: ciFixAttempts=%d automergeReason=%q, want 0/%q", s.FixAttempts, s.AutomergeReason, reasonAutomergeDisabled)
	}
	for _, want := range []string{"work:42-ci-repair", "repository is unknown", "let the worker finish", "close that window once its edits are preserved", "cenci babysit reset 42"} {
		if !strings.Contains(s.AutomergeDetail, want) {
			t.Errorf("tick 1 automergeDetail = %q, want it to contain %q", s.AutomergeDetail, want)
		}
	}
	if got := runOrder(e.calls); !slices.Equal(got, []string{"ci-repair"}) {
		t.Errorf("cenci run calls = %q, want one ci-repair once the pane is dead", got)
	}
	if s := e.after(2); strings.Contains(s.AutomergeDetail, "repository is unknown") {
		t.Errorf("tick 2 automergeDetail = %q, want no stale unknown-ownership note", s.AutomergeDetail)
	}
}

// AC 10: a state saved at two attempts and reloaded while the worker is live
// launches nothing; after it ends, exactly one attempt, then the cap.
func TestCIWorker_PersistAndReloadNeverResetsTheBudget(t *testing.T) {
	e := newCICapEnv(t,
		failingAt("head-a"), // 1: live
		failingAt("head-a"), // 2: reloaded, still live
		failingAt("head-a"), // 3: ended: attempt 3
		failingAt("head-a"), // 4: live
		failingAt("head-a"), // 5: ended: cap
		ciCapMerged(),
	)
	e.panes = panesByTick(liveCI(), liveCI(), deadCI(), liveCI(), deadCI())
	e.seed(trackedAt(2, "head-a"))

	e.runKilledAtSleep(1)
	if reloaded := load(e.path); reloaded.FixAttempts != 2 || !reloaded.CIRepairTracked {
		t.Fatalf("reloaded state: ciFixAttempts=%d tracked=%v, want 2/true", reloaded.FixAttempts, reloaded.CIRepairTracked)
	}
	if err := e.run(); err != nil {
		t.Fatalf("restarted Run: %v", err)
	}

	if got := runOrder(e.calls); !slices.Equal(got, []string{"ci-repair", "babysit-attention"}) {
		t.Fatalf("cenci run calls = %q, want one ci-repair then the cap attention", got)
	}
	if s := e.after(2); s.FixAttempts != 2 {
		t.Errorf("tick 2 after the reload: ciFixAttempts = %d, want 2", s.FixAttempts)
	}
	if s := e.after(5); s.FixAttempts != fixCap || !s.CIRepairPaused || !s.CICapNotified {
		t.Errorf("tick 5: ciFixAttempts=%d paused=%v notified=%v, want %d/true/true", s.FixAttempts, s.CIRepairPaused, s.CICapNotified, fixCap)
	}
}

// AC 11: a pre-#1457 state file (lastRepairedCiHeadSha at the failing head, no
// ciRepairTracked) loads, launches no ci-repair at that head and reports the
// recovery instruction; a different failing head makes CI repair eligible.
func TestCIWorker_LegacySameHeadMarkerKeepsItsHoldUntilTheHeadChanges(t *testing.T) {
	e := newCICapEnv(t, failingAt("head-a"), failingAt("head-b"), ciCapMerged())
	if err := os.WriteFile(e.path, []byte(`{"schemaVersion":2,"pr":"42","repo":"o/r","agent":"claude","intervalSeconds":60,"currentDelaySeconds":60,`+
		`"lastCiHeadSha":"head-a","lastRepairedCiHeadSha":"head-a","ciFixAttempts":1,"ciRepairPending":true,`+
		`"status":"running","ciStatus":"failing","updatedAt":"2026-11-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	held := e.after(1)
	if held.FixAttempts != 1 || held.CIRepairTracked {
		t.Errorf("tick 1: ciFixAttempts=%d tracked=%v, want 1/false (legacy hold)", held.FixAttempts, held.CIRepairTracked)
	}
	for _, want := range []string{"head-a", "work", "cenci babysit reset 42"} {
		if !strings.Contains(held.AutomergeDetail, want) {
			t.Errorf("tick 1 automergeDetail = %q, want it to contain %q", held.AutomergeDetail, want)
		}
	}
	if got := runOrder(e.calls); !slices.Equal(got, []string{"ci-repair"}) {
		t.Fatalf("cenci run calls = %q, want one ci-repair, at head-b", got)
	}
	if s := e.after(2); s.FixAttempts != 2 || s.LastRepairedSHA != "head-b" || !s.CIRepairTracked {
		t.Errorf("tick 2: ciFixAttempts=%d lastRepaired=%q tracked=%v, want 2/head-b/true", s.FixAttempts, s.LastRepairedSHA, s.CIRepairTracked)
	}
}

// Edge: a custom, non-finite ci-repair template keeps its pane live: every
// relaunch is suppressed for as long as it lives, and nothing kills it.
func TestCIWorker_NonFiniteWorkerSuppressesEveryRelaunchAndIsNeverKilled(t *testing.T) {
	e := newCICapEnv(t, failingAt("head-a"), failingAt("head-b"), failingAt("head-c"), ciCapMerged())
	e.panes = panesAlways(liveCI())
	e.seed(trackedAt(1, "head-a"))

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := len(runOrder(e.calls)); n != 0 {
		t.Errorf("cenci run calls = %d, want 0 while the worker lives", n)
	}
	for _, c := range e.calls {
		if slices.ContainsFunc(c, func(arg string) bool { return strings.Contains(arg, "kill") }) {
			t.Errorf("the supervisor tried to kill something: %q", c)
		}
	}
	if s := e.after(3); s.FixAttempts != 1 || !s.RepairPending {
		t.Errorf("tick 3: ciFixAttempts=%d ciRepairPending=%v, want 1/true", s.FixAttempts, s.RepairPending)
	}
}

// Edge: a PR that turns MERGED while its CI worker is live runs the terminal
// handling without probing or launching.
func TestCIWorker_MergedWhileTheWorkerIsLiveNeitherProbesNorLaunches(t *testing.T) {
	e := newCICapEnv(t, failingAt("head-a"), ciCapMerged(782))
	e.panes = panesByTick("", liveCI())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if e.inventoryReads[2] != 0 {
		t.Errorf("MERGED tick inventory reads = %d, want 0", e.inventoryReads[2])
	}
	if got := runOrder(e.calls); !slices.Equal(got, []string{"ci-repair"}) {
		t.Errorf("cenci run calls = %q, want only tick 1's ci-repair", got)
	}
	if !e.ghCalled("label", "create", "Implemented") {
		t.Error("the terminal cleanup did not run")
	}
	e.requireStateRemoved()
}

// Edge: a PR that turns CLOSED while its CI worker is live runs the terminal
// handling (no Implemented relabel) without probing or launching.
func TestCIWorker_ClosedWhileTheWorkerIsLiveNeitherProbesNorLaunches(t *testing.T) {
	e := newCICapEnv(t, failingAt("head-a"), ciCapClosed(782))
	e.panes = panesByTick("", liveCI())
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if e.inventoryReads[2] != 0 {
		t.Errorf("CLOSED tick inventory reads = %d, want 0", e.inventoryReads[2])
	}
	if got := runOrder(e.calls); !slices.Equal(got, []string{"ci-repair"}) {
		t.Errorf("cenci run calls = %q, want only tick 1's ci-repair", got)
	}
	if e.ghCalled("label") || e.ghCalled("issue", "edit") {
		t.Errorf("a CLOSED PR ran the Implemented relabel: %v", e.calls)
	}
	e.requireStateRemoved()
}

// Edge: a conflicting tick with an empty head SHA keeps the print-only
// behavior: no inventory read and no launch.
func TestCIWorker_ConflictWithAnUnknownHeadNeitherProbesNorLaunches(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR(""), `[]`, `[]`, `[]`}, &calls)
	reads := stubPanes(t, panesAlways(noWorkerInventory))
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", LaunchDir: ownedRepoDir, IntervalSeconds: 300}

	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if len(*reads) != 0 || len(runOrder(calls)) != 0 || s.ConflictRepairSHA != "" || s.ConflictFixAttempts != 0 {
		t.Errorf("reads=%v cenci run calls=%q conflict=%q/%d, want no read, no launch, no bookkeeping", *reads, runOrder(calls), s.ConflictRepairSHA, s.ConflictFixAttempts)
	}
}

// Q1: a quiet green tick with no budget state to reset never reads the
// inventory, so a recorded session that has gone cannot fail it.
func TestCIWorker_QuietGreenTickDoesNotProbe(t *testing.T) {
	e := newCICapEnv(t, ciCapTickOf(ciCapOpen("head-a"), ciCapGreen), ciCapMerged())
	e.panes = func(int) (string, error) { return "", errors.New("can't find session: work") }
	e.seed(State{Status: "running"})

	if err := e.run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if e.inventoryReads[1] != 0 || e.after(1).Status != "running" {
		t.Errorf("quiet green tick: inventory reads=%d status=%q, want 0/running", e.inventoryReads[1], e.after(1).Status)
	}
}
