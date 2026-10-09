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
	withCommands(t, []string{cleanOpenPR("abc"), `[{"bucket":"fail","name":"test","state":"FAILURE"}]`}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", ConflictRepairSHA: "previous", ConflictFixAttempts: 3, FixAttempts: fixCap}
	if _, _, err := tick(&s); !errors.Is(err, errNeedsInput) {
		t.Fatalf("CI retry cap error = %v", err)
	}
	if s.ConflictRepairSHA != "" || s.ConflictFixAttempts != 0 || s.Status != "needs-input" {
		t.Fatalf("CI retry cap retained stale conflict episode: %+v", s)
	}
}

func TestConflictRepairBudgetSurvivesUnknownAndResetsOnlyWhenClear(t *testing.T) {
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", IntervalSeconds: 300}
	for _, head := range []string{"a", "b", "c"} {
		var calls [][]string
		withCommands(t, []string{conflictingOpenPR(head), `[]`, `[]`, `[]`}, &calls)
		mergeRepairActive = func(string, string) (bool, error) { return false, nil }
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
	mergeRepairActive = func(string, string) (bool, error) { return false, nil }
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("cap must keep polling: %v", err)
	}
	if countWorkflowLaunches(calls, "babysit-attention") != 1 || s.Status != "needs-input" || s.ConflictFixAttempts != 3 {
		t.Fatalf("cap failed: state=%+v, calls=%v", s, calls)
	}
	calls = nil
	withCommands(t, []string{conflictingOpenPR("d"), `[]`, `[]`, `[]`}, &calls)
	mergeRepairActive = func(string, string) (bool, error) { return false, nil }
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
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", IntervalSeconds: 300}
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
			mergeRepairActive = func(session, pr string) (bool, error) {
				if session != "work" || pr != "42" {
					t.Fatalf("liveness target = %q/%q", session, pr)
				}
				return step.active, nil
			}
			terminal, delay, err := tick(&s)
			if terminal || err != nil || delay != 300*time.Second {
				t.Fatalf("tick terminal=%v, delay=%v, err=%v", terminal, delay, err)
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
	mergeRepairActive = func(string, string) (bool, error) { return false, errors.New("tmux unavailable") }
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
	mergeRepairActive = func(string, string) (bool, error) { return true, nil }
	s := State{PR: "42", Repo: "o/r", Agent: "codex", LaunchSession: "work", IntervalSeconds: 300}
	if _, delay, err := tick(&s); err != nil || delay != 300*time.Second {
		t.Fatalf("manual worker tick delay=%v err=%v", delay, err)
	}
	if countWorkflowLaunches(calls, "merge-repair") != 0 || s.ConflictFixAttempts != 0 || s.ConflictRepairSHA != "abc" || s.Status != "running" {
		t.Fatalf("manual worker duplicated or consumed budget: state=%+v calls=%v", s, calls)
	}
}
