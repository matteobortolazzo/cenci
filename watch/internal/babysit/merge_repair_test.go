package babysit

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestConflictRepairAlwaysUsesOpusWithoutChangingSupervisorAgent(t *testing.T) {
	for _, agent := range []string{"codex", "claude", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			var calls [][]string
			withCommands(t, []string{conflictingOpenPR("abc"), `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`, `[]`, `[]`}, &calls)
			s := State{PR: "42", Repo: "o/r", Agent: agent, LaunchSession: "work", LaunchDir: "/repo/root", IntervalSeconds: 300}
			if _, _, err := tick(&s); err != nil {
				t.Fatal(err)
			}
			args, ok := launchCallArgs(calls, "merge-repair")
			if !ok {
				t.Fatalf("missing merge-repair launch: %v", calls)
			}
			assertFlagValue(t, args, "--agent", "claude")
			assertFlagValue(t, args, "--model", "opus")
			// #1497: forcing claude/opus must not drop the unattended pin.
			assertPinnedUnattendedOnce(t, args)
			assertFlagValue(t, args, "--session", "work")
			assertFlagValue(t, args, "--dir", "/repo/root")
			if s.Agent != agent || s.Status != "running" || s.ConflictRepairSHA != "abc" || s.ConflictFixAttempts != 1 {
				t.Fatalf("unexpected repair state: %+v", s)
			}
		})
	}
}

func TestLegacyConflictNotificationDoesNotSuppressRepair(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("abc"), `[]`, `[]`, `[]`}, &calls)
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := State{SchemaVersion: stateSchemaVersion, PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", ConflictNotifiedSHA: "abc", Status: "needs-input"}
	if err := save(path, legacy); err != nil {
		t.Fatal(err)
	}
	s := load(path)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if countWorkflowLaunches(calls, "merge-repair") != 1 || s.Status != "running" || s.ConflictRepairSHA != "abc" {
		t.Fatalf("legacy notification blocked automatic repair: state=%+v, calls=%v", s, calls)
	}
	if err := save(path, s); err != nil {
		t.Fatal(err)
	}
	s = load(path)
	if s.ConflictRepairSHA != "abc" || s.ConflictFixAttempts != 1 || s.ConflictNotifiedSHA != "abc" {
		t.Fatalf("repair state failed round trip: %+v", s)
	}
}

func TestKnownConflictResolutionResetsBudgetBeforeCIRetryCap(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{cleanOpenPR("abc"), `[{"bucket":"fail","name":"test","state":"FAILURE"}]`, `[]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", ConflictRepairSHA: "previous", ConflictFixAttempts: 3, FixAttempts: fixCap}
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("CI retry cap error = %v, want nil: the cap pauses CI repair without stopping the tick", err)
	}
	if s.ConflictRepairSHA != "" || s.ConflictFixAttempts != 0 || s.Status != "needs-input" || !s.CIRepairPaused {
		t.Fatalf("CI retry cap retained stale conflict episode or lost the pause: %+v", s)
	}
}

func TestConflictRepairBudgetSurvivesUnknownAndResetsOnlyWhenClear(t *testing.T) {
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", IntervalSeconds: 300}
	for _, head := range []string{"a", "b", "c"} {
		var calls [][]string
		withCommands(t, []string{conflictingOpenPR(head), `[]`, `[]`, `[]`}, &calls)
		if _, _, err := tick(&s); err != nil {
			t.Fatal(err)
		}
		if countWorkflowLaunches(calls, "merge-repair") != 1 {
			t.Fatalf("missing repair for %s: %v", head, calls)
		}
	}
	var calls [][]string
	unknown := `{"state":"OPEN","headRefOid":"d","mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN"}`
	withCommands(t, []string{unknown, `[]`, `[]`, `[]`}, &calls)
	if _, delay, err := tick(&s); err != nil || delay != 300*time.Second {
		t.Fatalf("unknown conflict episode tick delay=%v, err=%v", delay, err)
	}
	if s.ConflictFixAttempts != 3 || s.ConflictRepairSHA != "c" {
		t.Fatalf("unknown mergeability erased budget: %+v", s)
	}
	withCommands(t, []string{conflictingOpenPR("d"), `[]`, `[]`, `[]`}, &calls)
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("cap must keep polling: %v", err)
	}
	if countWorkflowLaunches(calls, "babysit-attention") != 1 || s.Status != "needs-input" || s.ConflictFixAttempts != 3 {
		t.Fatalf("cap failed: state=%+v, calls=%v", s, calls)
	}
	calls = nil
	withCommands(t, []string{conflictingOpenPR("d"), `[]`, `[]`, `[]`}, &calls)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if countWorkflowLaunches(calls, "babysit-attention") != 0 || countWorkflowLaunches(calls, "merge-repair") != 0 || s.Status != "needs-input" {
		t.Fatalf("capped same-head observation dispatched again: state=%+v, calls=%v", s, calls)
	}
	withCommands(t, []string{cleanOpenPR("d"), `[]`, `[]`, `[]`}, &calls)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.ConflictFixAttempts != 0 || s.ConflictRepairSHA != "" || s.ConflictNotifiedSHA != "" || s.Status != "running" {
		t.Fatalf("clear conflict did not reset budget: %+v", s)
	}
	withCommands(t, []string{conflictingOpenPR("e"), `[]`, `[]`, `[]`}, &calls)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.ConflictFixAttempts != 1 || s.ConflictRepairSHA != "e" {
		t.Fatalf("new episode failed to repair: %+v", s)
	}
}

func TestConflictDefersCIRepairWithoutConsumingSameHeadDispatch(t *testing.T) {
	var calls [][]string
	failing := `[{"bucket":"fail","name":"test","state":"FAILURE"}]`
	withCommands(t, []string{conflictingOpenPR("abc"), failing, `[]`, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work"}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.FixAttempts != 0 || s.LastRepairedSHA != "" || s.RepairPending || countWorkflowLaunches(calls, "ci-repair") != 0 {
		t.Fatalf("conflict consumed CI dispatch: state=%+v, calls=%v", s, calls)
	}
	calls = nil
	withCommands(t, []string{cleanOpenPR("abc"), failing, `[]`, `[]`}, &calls)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	args, ok := launchCallArgs(calls, "ci-repair")
	if !ok || s.FixAttempts != 1 || s.LastRepairedSHA != "abc" {
		t.Fatalf("same-head CI repair stranded: state=%+v, calls=%v", s, calls)
	}
	assertFlagValue(t, args, "--agent", "codex")
}

func TestConflictDefersFeedbackUntilClear(t *testing.T) {
	var calls [][]string
	comments := `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	withCommands(t, []string{conflictingOpenPR("abc"), `[]`, comments, `[]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work"}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.PendingKeys, []string{"comment:7"}) || len(s.LaunchedKeys) != 0 || countAddressReviewLaunches(calls) != 0 {
		t.Fatalf("feedback was lost or dispatched concurrently: state=%+v, calls=%v", s, calls)
	}
	calls = nil
	unknown := `{"state":"OPEN","headRefOid":"abc","mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN"}`
	withCommands(t, []string{unknown, `[{"bucket":"fail","name":"test","state":"FAILURE"}]`, comments, `[]`, unresolvedThreadFor(7)}, &calls)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if countAddressReviewLaunches(calls) != 0 || countWorkflowLaunches(calls, "ci-repair") != 0 || len(s.LaunchedKeys) != 0 || s.LastRepairedSHA != "" || s.FixAttempts != 0 {
		t.Fatalf("unknown mergeability released a concurrent writer: state=%+v, calls=%v", s, calls)
	}
	calls = nil
	withCommands(t, []string{cleanOpenPR("abc"), `[]`, comments, `[]`, unresolvedThreadFor(7)}, &calls)
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	args, ok := launchCallArgs(calls, "address-review")
	if !ok || !slices.Equal(s.LaunchedKeys, []string{"comment:7"}) {
		t.Fatalf("deferred feedback did not launch: state=%+v, calls=%v", s, calls)
	}
	assertFlagValue(t, args, "--agent", "codex")
}

func TestConflictRepairLifecycleRecoversEndedWorkersAtSameHead(t *testing.T) {
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", LaunchDir: "/repo/root", IntervalSeconds: 300}
	for _, step := range []struct {
		name, head string
		active     bool
		wantRepair bool
		wantCap    bool
		attempts   int
	}{
		{"initial launch", "abc", false, true, false, 1},
		{"same head live", "abc", true, false, false, 1},
		{"changed head live", "def", true, false, false, 1},
		{"same head ended retries", "abc", false, true, false, 2},
		{"changed head ended retries", "def", false, true, false, 3},
		{"third attempt still live", "def", true, false, false, 3},
		{"third attempt ended reaches cap", "def", false, false, true, 3},
		{"cap deduplicates attention", "def", false, false, false, 3},
	} {
		t.Run(step.name, func(t *testing.T) {
			var calls [][]string
			withCommands(t, []string{conflictingOpenPR(step.head), `[]`, `[]`, `[]`}, &calls)
			reads := stubPanes(t, panesAlways(paneRowOf("42-merge-repair", !step.active, "/repo/root")))
			terminal, delay, err := tick(&s)
			if terminal || err != nil || delay != 300*time.Second {
				t.Fatalf("tick terminal=%v, delay=%v, err=%v", terminal, delay, err)
			}
			if !slices.Equal(*reads, []string{"|work"}) {
				t.Fatalf("inventory reads = %v, want one of the recorded session", *reads)
			}
			if (countWorkflowLaunches(calls, "merge-repair") == 1) != step.wantRepair || (countWorkflowLaunches(calls, "babysit-attention") == 1) != step.wantCap || s.ConflictFixAttempts != step.attempts {
				t.Fatalf("wrong lifecycle dispatch: state=%+v calls=%v", s, calls)
			}
		})
	}
}

func TestConflictRepairProbeFailureDoesNotLaunchDuplicate(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("abc"), `[]`}, &calls)
	stubPanes(t, func(int) (string, error) { return "", errors.New("tmux unavailable") })
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", ConflictRepairSHA: "abc", ConflictFixAttempts: 1}
	if _, _, err := tick(&s); err == nil || !strings.Contains(err.Error(), "tmux unavailable") {
		t.Fatalf("probe failure not surfaced: %v", err)
	}
	if countWorkflowLaunches(calls, "merge-repair") != 0 || s.ConflictFixAttempts != 1 || s.ConflictRepairSHA != "abc" || s.AutomergeReason != reasonWorkflowLaunchFailed {
		t.Fatalf("probe failure consumed repair state: state=%+v calls=%v", s, calls)
	}
}

func TestConflictRepairDefersToManuallyStartedWorker(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("abc"), `[]`, `[]`, `[]`}, &calls)
	stubPanes(t, panesAlways(paneRowOf("42-merge-repair", false, "/repo/root")))
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", LaunchDir: "/repo/root", IntervalSeconds: 300}
	if _, delay, err := tick(&s); err != nil || delay != 300*time.Second {
		t.Fatalf("manual worker tick delay=%v err=%v", delay, err)
	}
	if countWorkflowLaunches(calls, "merge-repair") != 0 || s.ConflictFixAttempts != 0 || s.ConflictRepairSHA != "abc" || s.Status != "running" {
		t.Fatalf("manual worker duplicated or consumed budget: state=%+v calls=%v", s, calls)
	}
}

// #1457 (plan Q&A 5): the merge-repair cap attention also waits for no live
// writer. At the cap, a live CI worker (any live writer counts) opens no
// attention and records no notification; once it ends, the next tick opens
// exactly one attention and records it.
func TestConflictCapAttentionWaitsForLiveWriter(t *testing.T) {
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", LaunchDir: "/repo/root", IntervalSeconds: 300,
		ConflictRepairSHA: "abc", ConflictFixAttempts: fixCap}

	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("abc"), `[]`, `[]`, `[]`}, &calls)
	stubPanes(t, panesAlways(paneRowOf("42-ci-repair", false, "/repo/root")))
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick with a live writer at the cap: %v", err)
	}
	if n := countWorkflowLaunches(calls, "babysit-attention"); n != 0 || s.ConflictNotifiedSHA != "" {
		t.Fatalf("cap attention opened beside a live writer: launches=%d notified=%q", n, s.ConflictNotifiedSHA)
	}

	calls = nil
	withCommands(t, []string{conflictingOpenPR("abc"), `[]`, `[]`, `[]`}, &calls)
	stubPanes(t, panesAlways(paneRowOf("42-ci-repair", true, "/repo/root")))
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick after the writer ended: %v", err)
	}
	if n := countWorkflowLaunches(calls, "babysit-attention"); n != 1 || s.ConflictNotifiedSHA != "abc" {
		t.Fatalf("cap attention after the writer ended: launches=%d notified=%q, want 1 and abc", n, s.ConflictNotifiedSHA)
	}
	if n := countWorkflowLaunches(calls, "merge-repair"); n != 0 || s.ConflictFixAttempts != fixCap {
		t.Fatalf("merge-repair launched at the cap: launches=%d attempts=%d", n, s.ConflictFixAttempts)
	}
}

// #1457: GitHub reporting the PR clear while this PR's merge-repair worker is
// still live keeps the conflict episode, launches no ci-repair and keeps review
// deferred. The first clear tick after the pane ends releases the episode, and
// failing CI is then eligible on that same tick.
func TestConflictReleaseWaitsForTheMergeWorker(t *testing.T) {
	failing := `[{"bucket":"fail","name":"test","state":"FAILURE"}]`
	comments := `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", LaunchDir: "/repo/root", IntervalSeconds: 300,
		ConflictRepairSHA: "abc", ConflictFixAttempts: 1}

	var calls [][]string
	withCommands(t, []string{cleanOpenPR("abc"), failing, comments, `[]`, unresolvedThreadFor(7)}, &calls)
	stubPanes(t, panesAlways(paneRowOf("42-merge-repair", false, "/repo/root")))
	if _, delay, err := tick(&s); err != nil || delay != 300*time.Second {
		t.Fatalf("clear tick with a live merge worker: delay=%v err=%v", delay, err)
	}
	if s.ConflictRepairSHA != "abc" || s.ConflictFixAttempts != 1 {
		t.Fatalf("the episode was released while the merge worker was live: %+v", s)
	}
	if n := countWorkflowLaunches(calls, "ci-repair") + countAddressReviewLaunches(calls); n != 0 || s.FixAttempts != 0 {
		t.Fatalf("a second writer launched beside the live merge worker: launches=%d FixAttempts=%d calls=%v", n, s.FixAttempts, calls)
	}
	if !slices.Equal(s.PendingKeys, []string{"comment:7"}) || len(s.LaunchedKeys) != 0 {
		t.Fatalf("review feedback pending=%v launched=%v, want [comment:7] pending and unlaunched", s.PendingKeys, s.LaunchedKeys)
	}

	calls = nil
	withCommands(t, []string{cleanOpenPR("abc"), failing, comments, `[]`, unresolvedThreadFor(7)}, &calls)
	stubPanes(t, panesAlways(paneRowOf("42-merge-repair", true, "/repo/root")))
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("clear tick after the merge worker ended: %v", err)
	}
	if s.ConflictRepairSHA != "" || s.ConflictFixAttempts != 0 {
		t.Fatalf("the episode was not released once the merge worker ended: %+v", s)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 || s.FixAttempts != 1 {
		t.Fatalf("failing CI after the release: ci-repair launches=%d FixAttempts=%d, want 1/1", n, s.FixAttempts)
	}
	if n := countAddressReviewLaunches(calls); n != 0 || len(s.LaunchedKeys) != 0 {
		t.Fatalf("address-review launched on the ci-repair tick: launches=%d launched=%v", n, s.LaunchedKeys)
	}
}

// #1457: a live `42-merge-repair` window whose repository has a different git
// common dir belongs to another repository's PR 42: it neither defers this
// PR's merge repair nor blocks its CI repair.
func TestForeignMergeRepairWindowDoesNotBlock(t *testing.T) {
	stubGitCommonDir(t, map[string]string{"/repo/root": "/repo/root/.git", "/elsewhere/repo": "/elsewhere/repo/.git"})
	foreign := paneRowOf("42-merge-repair", false, "/elsewhere/repo")

	var calls [][]string
	withCommands(t, []string{conflictingOpenPR("abc"), `[]`, `[]`, `[]`}, &calls)
	stubPanes(t, panesAlways(foreign))
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", LaunchDir: "/repo/root", IntervalSeconds: 300}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if countWorkflowLaunches(calls, "merge-repair") != 1 || s.ConflictFixAttempts != 1 {
		t.Fatalf("a foreign merge-repair window deferred this PR's merge repair: state=%+v calls=%v", s, calls)
	}

	calls = nil
	withCommands(t, []string{cleanOpenPR("def"), `[{"bucket":"fail","name":"test","state":"FAILURE"}]`, `[]`, `[]`}, &calls)
	stubPanes(t, panesAlways(foreign))
	ci := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", LaunchDir: "/repo/root", IntervalSeconds: 300}
	if _, _, err := tick(&ci); err != nil {
		t.Fatal(err)
	}
	if countWorkflowLaunches(calls, "ci-repair") != 1 || ci.FixAttempts != 1 {
		t.Fatalf("a foreign merge-repair window blocked this PR's CI repair: state=%+v calls=%v", ci, calls)
	}
}
