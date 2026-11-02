package babysit

// #1391: host-only `cenci babysit reset <pr>`. These tests run a real Reset
// against a temp state directory. Gh and git go through the package's stubbed
// execGh and command seams, the detached supervisor start is captured through
// startSupervisor, and the P7/P15/P16 liveness checks use real child processes
// so the real defaultProcessOwned and SIGTERM paths are exercised
// (watch/docs/test-strategy.md). The test process's own PID is never recorded
// in a state file (watch/docs/test-isolation.md).

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type resetEnv struct {
	dir, path, lock string
	started         [][]string
	onStart         func()
	startErr        error
}

// newResetEnv isolates a host-side Reset: the sandbox and supervisor env vars
// are cleared, gh answers the repo lookup and the arm-time pr view, and
// startSupervisor records the child argv instead of spawning anything.
func newResetEnv(t *testing.T) *resetEnv {
	t.Helper()
	t.Setenv("CENCI_SANDBOX", "")
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
	e := &resetEnv{dir: t.TempDir()}
	e.path = statePath(e.dir, "o/r", "42")
	e.lock = e.path + ".lock"

	originalGh, originalCommand := execGh, command
	originalTmux, originalStart := currentTmuxSession, startSupervisor
	originalWait, originalPoll := resetExitWait, resetPollInterval
	execGh = func(args ...string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "repo":
			return "o/r\n", "", nil
		case len(args) > 1 && args[0] == "pr" && args[1] == "view":
			return openPR(), "", nil
		}
		return "", "", fmt.Errorf("unexpected gh call: %v", args)
	}
	command = func(name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte("/repo/root\n"), nil
		}
		return []byte(""), nil
	}
	currentTmuxSession = func() (string, error) { return "ambient-session", nil }
	startSupervisor = func(cmd *exec.Cmd) error {
		e.started = append(e.started, cmd.Args)
		if e.onStart != nil {
			e.onStart()
		}
		return e.startErr
	}
	resetPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		execGh, command = originalGh, originalCommand
		currentTmuxSession, startSupervisor = originalTmux, originalStart
		resetExitWait, resetPollInterval = originalWait, originalPoll
	})
	return e
}

// requireNoStart fails the test when Reset armed a supervisor.
func (e *resetEnv) requireNoStart(t *testing.T) {
	t.Helper()
	if len(e.started) != 0 {
		t.Errorf("supervisor starts = %d, want 0", len(e.started))
	}
}

// pausedState is a supervisor paused at both retry caps, with feedback
// bookkeeping a reset must keep.
func pausedState() State {
	return State{
		SchemaVersion: stateSchemaVersion, PR: "42", Repo: "o/r", Agent: "codex",
		IntervalSeconds: 300, CurrentDelaySeconds: 900,
		LastHeadSHA: "sha-x", LastRepairedSHA: "sha-x", FixAttempts: fixCap, RepairPending: true,
		ConflictFixAttempts: fixCap, ConflictNotifiedSHA: "sha-c", ConflictRepairSHA: "sha-r",
		LastCommentAt: "2026-10-01T10:00:00Z",
		AddressedKeys: []string{"comment:1"}, PendingKeys: []string{"comment:2"}, LaunchedKeys: []string{"comment:2"},
		Status: "needs-input", LaunchSession: "work", LaunchDir: "/repo/work",
	}
}

// mustReadState reads the state strictly and pins its identity. load swallows a
// read or decode error into a zero State, which would let a zeros-only budget
// check pass on a missing or corrupt file; a non-empty PR and Repo rules that out.
func mustReadState(t *testing.T, path string) State {
	t.Helper()
	s, err := readStateStrict(path)
	if err != nil {
		t.Fatalf("read state %s: %v", path, err)
	}
	if s.PR == "" || s.Repo == "" {
		t.Fatalf("state %s has no identity (pr %q, repo %q), want a real saved state", path, s.PR, s.Repo)
	}
	return s
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeLock(t *testing.T, path string, pid int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixtureProc is a real child process in its own process group, reaped by a
// goroutine and killed at test cleanup.
type fixtureProc struct {
	pid  int
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *fixtureProc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func spawnFixture(t *testing.T, cmd *exec.Cmd) *fixtureProc {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &fixtureProc{pid: cmd.Process.Pid, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
		}
	})
	return p
}

// startOwnedFixture starts a process whose cmdline contains "babysit" and the
// PR "42", so the real defaultProcessOwned accepts it. With ignoreTerm it
// ignores SIGTERM, and the helper waits until the trap is installed so a
// signal can never beat it.
func startOwnedFixture(t *testing.T, ignoreTerm bool) *fixtureProc {
	t.Helper()
	if !procfsReadable() {
		t.Skip("real ownership check needs a readable procfs")
	}
	ready := filepath.Join(t.TempDir(), "ready")
	script := `: > "$1"; sleep 30; :`
	if ignoreTerm {
		script = `trap "" TERM; ` + script
	}
	p := spawnFixture(t, exec.Command("sh", "-c", script, "babysit-42-fixture", ready))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture process never became ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startUnrelatedFixture starts a process the ownership check must reject.
func startUnrelatedFixture(t *testing.T) *fixtureProc {
	t.Helper()
	if !procfsReadable() {
		t.Skip("real ownership check needs a readable procfs")
	}
	return spawnFixture(t, exec.Command("sleep", "30"))
}

func TestReset_PausedSupervisorZeroesBudgetKeepsFeedbackAndReArms(t *testing.T) {
	e := newResetEnv(t)
	writeStateFile(t, e.path, pausedState())

	done := captureStdout(t)
	err := Reset("42", e.dir, nil)
	out := done()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if len(e.started) != 1 {
		t.Fatalf("supervisor starts = %d, want 1", len(e.started))
	}
	wantArgv := []string{os.Args[0], "babysit", "42", "--agent", "codex", "--interval", "5m0s", "--state-dir", e.dir, "--session", "work", "--dir", "/repo/work"}
	if !reflect.DeepEqual(e.started[0], wantArgv) {
		t.Errorf("supervisor argv = %q, want %q", e.started[0], wantArgv)
	}
	if !strings.Contains(out, "Reset the retry budget for PR #42.") {
		t.Errorf("stdout = %q, want the reset line", out)
	}

	s := mustReadState(t, e.path)
	if s.FixAttempts != 0 || s.ConflictFixAttempts != 0 || s.RepairPending || s.LastRepairedSHA != "" || s.ConflictNotifiedSHA != "" || s.PID != 0 {
		t.Errorf("budget not zeroed: ciFixAttempts=%d conflictFixAttempts=%d ciRepairPending=%v lastRepairedCiHeadSha=%q conflictNotifiedHeadSha=%q pid=%d",
			s.FixAttempts, s.ConflictFixAttempts, s.RepairPending, s.LastRepairedSHA, s.ConflictNotifiedSHA, s.PID)
	}
	if !reflect.DeepEqual(s.AddressedKeys, []string{"comment:1"}) || !reflect.DeepEqual(s.PendingKeys, []string{"comment:2"}) || !reflect.DeepEqual(s.LaunchedKeys, []string{"comment:2"}) || s.LastCommentAt != "2026-10-01T10:00:00Z" {
		t.Errorf("feedback bookkeeping changed: addressed=%v pending=%v launched=%v lastCommentTimestamp=%q", s.AddressedKeys, s.PendingKeys, s.LaunchedKeys, s.LastCommentAt)
	}
	if s.ConflictRepairSHA != "sha-r" {
		t.Errorf("conflictRepairHeadSha = %q, want it kept as sha-r", s.ConflictRepairSHA)
	}
}

func TestReset_LiveOwnedSupervisorIsTerminatedLockRemovedThenReArmed(t *testing.T) {
	e := newResetEnv(t)
	p := startOwnedFixture(t, false)
	s := pausedState()
	s.PID = p.pid
	s.Status = "running"
	writeStateFile(t, e.path, s)
	writeLock(t, e.lock, p.pid)

	var stillOwned, lockGone bool
	var atStart State
	e.onStart = func() {
		stillOwned = processOwned(p.pid, "42")
		_, statErr := os.Stat(e.lock)
		lockGone = os.IsNotExist(statErr)
		atStart = mustReadState(t, e.path)
	}

	done := captureStdout(t)
	err := Reset("42", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if len(e.started) != 1 {
		t.Fatalf("supervisor starts = %d, want 1", len(e.started))
	}
	if stillOwned {
		t.Error("the old supervisor was still running when the new one started")
	}
	if !lockGone {
		t.Error("the old supervisor's lock still existed when the new one started")
	}
	if atStart.FixAttempts != 0 || atStart.ConflictFixAttempts != 0 || atStart.PID != 0 {
		t.Errorf("state at re-arm = %+v, want the budget zeroed and pid 0", atStart)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the old supervisor never exited")
	}
	status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("old supervisor exit = %v, want it ended by SIGTERM", p.cmd.ProcessState)
	}
}

// Q6: the state is re-read strictly after the supervisor has exited and the
// lock is gone. The fixture supervisor, on SIGTERM, overwrites the state file
// with a save that moved the feedback bookkeeping (as a real supervisor's tick
// could while dying) and only then exits. Zeroing the pre-signal read would
// roll that save back; the budget fields still come out zeroed.
func TestReset_SaveMadeWhileSupervisorDiesSurvivesTheReset(t *testing.T) {
	e := newResetEnv(t)
	if !procfsReadable() {
		t.Skip("real ownership check needs a readable procfs")
	}

	staged := filepath.Join(t.TempDir(), "dying-save.json")
	ready := filepath.Join(t.TempDir(), "ready")
	script := `trap 'cp "$2" "$3"; exit 0' TERM; : > "$1"; sleep 30 & wait`
	p := spawnFixture(t, exec.Command("sh", "-c", script, "babysit-42-fixture", ready, staged, e.path))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture process never became ready")
		}
		time.Sleep(5 * time.Millisecond)
	}

	before := pausedState()
	before.PID = p.pid
	before.Status = "running"
	writeStateFile(t, e.path, before)
	writeLock(t, e.lock, p.pid)

	dying := before
	dying.LastCommentAt = "2026-10-02T09:30:00Z"
	dying.AddressedKeys = []string{"comment:1", "comment:3"}
	writeStateFile(t, staged, dying)

	done := captureStdout(t)
	err := Reset("42", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	s := mustReadState(t, e.path)
	if s.LastCommentAt != "2026-10-02T09:30:00Z" || !reflect.DeepEqual(s.AddressedKeys, []string{"comment:1", "comment:3"}) {
		t.Errorf("the save made while the supervisor was dying was overwritten: lastCommentTimestamp=%q addressed=%v", s.LastCommentAt, s.AddressedKeys)
	}
	if s.FixAttempts != 0 || s.ConflictFixAttempts != 0 || s.RepairPending || s.LastRepairedSHA != "" || s.ConflictNotifiedSHA != "" || s.PID != 0 {
		t.Errorf("budget not zeroed: %+v", s)
	}
}

// Q7: the budget is saved before the re-arm, so a re-arm that fails leaves the
// zeroed budget on disk and Reset names the failure and the manual re-arm.
func TestReset_ReArmFailureReportsBudgetResetButNotReArmed(t *testing.T) {
	e := newResetEnv(t)
	e.startErr = errors.New("spawn failed")
	writeStateFile(t, e.path, pausedState())

	done := captureStdout(t)
	err := Reset("42", e.dir, nil)
	_ = done()

	const want = "budget reset for PR #42 but not re-armed: start supervisor: spawn failed; re-arm with cenci babysit 42 --agent codex"
	if err == nil || err.Error() != want {
		t.Fatalf("Reset err = %v, want exactly %q", err, want)
	}
	if len(e.started) != 1 {
		t.Errorf("supervisor starts = %d, want the one failed attempt", len(e.started))
	}
	s := mustReadState(t, e.path)
	if s.FixAttempts != 0 || s.ConflictFixAttempts != 0 || s.RepairPending || s.LastRepairedSHA != "" || s.ConflictNotifiedSHA != "" || s.PID != 0 {
		t.Errorf("budget not left zeroed after the failed re-arm: %+v", s)
	}
}

func TestReset_MissingStateRefusesAndStartsNothing(t *testing.T) {
	e := newResetEnv(t)

	err := Reset("42", e.dir, nil)

	if err == nil || err.Error() != "no supervisor found for PR #42" {
		t.Fatalf("Reset err = %v, want exactly %q", err, "no supervisor found for PR #42")
	}
	if !errors.Is(err, ErrNoSupervisor) {
		t.Errorf("Reset err = %v, want errors.Is ErrNoSupervisor", err)
	}
	e.requireNoStart(t)
	if _, statErr := os.Stat(e.path); !os.IsNotExist(statErr) {
		t.Errorf("a state file was created (stat err = %v)", statErr)
	}
}

func TestReset_UnreadableStateReturnsStrictReadErrorAndWritesNothing(t *testing.T) {
	t.Run("decode error", func(t *testing.T) {
		e := newResetEnv(t)
		const garbage = "{not json"
		if err := os.WriteFile(e.path, []byte(garbage), 0o600); err != nil {
			t.Fatal(err)
		}

		err := Reset("42", e.dir, nil)

		if err == nil || !strings.Contains(err.Error(), "decode babysit state") {
			t.Fatalf("Reset err = %v, want the strict decode error", err)
		}
		if got := string(readFileBytes(t, e.path)); got != garbage {
			t.Errorf("state file = %q, want it unchanged", got)
		}
		e.requireNoStart(t)
	})

	t.Run("read error", func(t *testing.T) {
		e := newResetEnv(t)
		if err := os.Mkdir(e.path, 0o700); err != nil {
			t.Fatal(err)
		}

		err := Reset("42", e.dir, nil)

		if err == nil || !strings.Contains(err.Error(), "read babysit state") {
			t.Fatalf("Reset err = %v, want the strict read error", err)
		}
		if info, statErr := os.Stat(e.path); statErr != nil || !info.IsDir() {
			t.Errorf("state path was replaced (stat err = %v)", statErr)
		}
		e.requireNoStart(t)
	})
}

func TestReset_UnownedPIDRefusesWithStateAndLockUnchanged(t *testing.T) {
	e := newResetEnv(t)
	p := startUnrelatedFixture(t)
	s := pausedState()
	s.PID = p.pid
	writeStateFile(t, e.path, s)
	writeLock(t, e.lock, p.pid)
	stateBefore, lockBefore := readFileBytes(t, e.path), readFileBytes(t, e.lock)

	err := Reset("42", e.dir, nil)

	want := fmt.Sprintf("refusing to signal pid %d", p.pid)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Reset err = %v, want it to contain %q", err, want)
	}
	if !p.alive() {
		t.Error("the unrelated process was signalled")
	}
	if !bytes.Equal(readFileBytes(t, e.path), stateBefore) || !bytes.Equal(readFileBytes(t, e.lock), lockBefore) {
		t.Error("state file or lock changed")
	}
	e.requireNoStart(t)
}

func TestReset_SupervisorThatIgnoresTermTimesOutWithNothingChanged(t *testing.T) {
	e := newResetEnv(t)
	resetExitWait = 300 * time.Millisecond
	p := startOwnedFixture(t, true)
	s := pausedState()
	s.PID = p.pid
	s.Status = "running"
	writeStateFile(t, e.path, s)
	writeLock(t, e.lock, p.pid)
	stateBefore, lockBefore := readFileBytes(t, e.path), readFileBytes(t, e.lock)

	err := Reset("42", e.dir, nil)

	if err == nil || !strings.Contains(err.Error(), "supervisor for PR #42 did not exit") {
		t.Fatalf("Reset err = %v, want the did-not-exit timeout", err)
	}
	if !p.alive() {
		t.Error("the TERM-ignoring supervisor should still be running")
	}
	if !bytes.Equal(readFileBytes(t, e.path), stateBefore) || !bytes.Equal(readFileBytes(t, e.lock), lockBefore) {
		t.Error("state file or lock changed after the timeout")
	}
	e.requireNoStart(t)
}

func TestReset_LockWithoutPIDRefusesAndChangesNothing(t *testing.T) {
	e := newResetEnv(t)
	writeStateFile(t, e.path, pausedState())
	writeLock(t, e.lock, 99999)
	stateBefore, lockBefore := readFileBytes(t, e.path), readFileBytes(t, e.lock)

	err := Reset("42", e.dir, nil)

	if err == nil || !strings.Contains(err.Error(), "supervisor already running for PR #42") {
		t.Fatalf("Reset err = %v, want the already-running refusal", err)
	}
	if !bytes.Equal(readFileBytes(t, e.path), stateBefore) || !bytes.Equal(readFileBytes(t, e.lock), lockBefore) {
		t.Error("state file or lock changed")
	}
	e.requireNoStart(t)
}

func TestReset_ZeroIntervalReArmsWithFifteenMinuteDefault(t *testing.T) {
	e := newResetEnv(t)
	s := pausedState()
	s.IntervalSeconds = 0
	writeStateFile(t, e.path, s)

	done := captureStdout(t)
	err := Reset("42", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if len(e.started) != 1 {
		t.Fatalf("supervisor starts = %d, want 1", len(e.started))
	}
	argv := e.started[0]
	idx := -1
	for i, a := range argv {
		if a == "--interval" {
			idx = i
		}
	}
	if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != "15m0s" {
		t.Errorf("argv = %q, want --interval 15m0s", argv)
	}
}

func TestReset_AgentRefusalPrecedesAnyWriteOrSignal(t *testing.T) {
	e := newResetEnv(t)
	p := startOwnedFixture(t, false)
	s := pausedState()
	s.PID = p.pid
	s.Status = "running"
	writeStateFile(t, e.path, s)
	writeLock(t, e.lock, p.pid)
	stateBefore, lockBefore := readFileBytes(t, e.path), readFileBytes(t, e.lock)

	const reason = "agent codex may not be re-armed here"
	var asked string
	err := Reset("42", e.dir, func(agent string) string {
		asked = agent
		return reason
	})

	if !errors.Is(err, ErrAgentRefused) || err.Error() != reason {
		t.Fatalf("Reset err = %v, want ErrAgentRefused carrying %q", err, reason)
	}
	if asked != "codex" {
		t.Errorf("refusal consulted for agent %q, want the recorded agent codex", asked)
	}
	if !p.alive() {
		t.Error("the live supervisor was signalled before the refusal")
	}
	if !bytes.Equal(readFileBytes(t, e.path), stateBefore) || !bytes.Equal(readFileBytes(t, e.lock), lockBefore) {
		t.Error("state file or lock changed")
	}
	e.requireNoStart(t)
}

// The re-arm goes through Run's arming branch, which is skipped when
// CENCI_BABYSIT_SUPERVISOR is set; Reset must fail closed instead of zeroing
// a budget it cannot re-arm.
func TestReset_InsideSupervisorEnvRefuses(t *testing.T) {
	e := newResetEnv(t)
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
	p := startOwnedFixture(t, false)
	s := pausedState()
	s.PID = p.pid
	s.Status = "running"
	writeStateFile(t, e.path, s)
	stateBefore := readFileBytes(t, e.path)

	err := Reset("42", e.dir, nil)

	if err == nil {
		t.Fatal("Reset err = nil, want a refusal inside the supervisor")
	}
	if !p.alive() {
		t.Error("the live supervisor was signalled")
	}
	if !bytes.Equal(readFileBytes(t, e.path), stateBefore) {
		t.Error("state file changed")
	}
	e.requireNoStart(t)
}

func tickScriptFor(pr string, checks string) []string {
	return []string{pr, checks, `[]`, `[]`}
}

// P22: the pause left lastRepairedCiHeadSha equal to the still-failing head, so
// tick stays quiet until the reset clears it. The control tick on the
// pre-reset state proves the cleared field is what changes the outcome.
func TestReset_ThenTickDispatchesCIRepairAtUnchangedFailingHead(t *testing.T) {
	e := newResetEnv(t)
	paused := pausedState()
	paused.ConflictFixAttempts, paused.ConflictNotifiedSHA, paused.ConflictRepairSHA = 0, "", ""
	writeStateFile(t, e.path, paused)
	failing := `[{"bucket":"fail","name":"test","state":"FAILURE"}]`

	done := captureStdout(t)
	err := Reset("42", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	var controlCalls [][]string
	withCommands(t, tickScriptFor(cleanOpenPR("sha-x"), failing), &controlCalls)
	control := paused
	if _, _, err := tick(&control); err != nil {
		t.Fatalf("control tick: %v", err)
	}
	if n := countWorkflowLaunches(controlCalls, "ci-repair"); n != 0 {
		t.Fatalf("control: ci-repair launches before the reset = %d, want 0 (the head was already repaired)", n)
	}

	var calls [][]string
	withCommands(t, tickScriptFor(cleanOpenPR("sha-x"), failing), &calls)
	s := mustReadState(t, e.path)
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick after reset: %v", err)
	}
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("ci-repair launches after the reset = %d, want 1: %#v", n, calls)
	}
	if n := countWorkflowLaunches(calls, "babysit-attention"); n != 0 {
		t.Errorf("babysit-attention launches = %d, want 0", n)
	}
	if s.FixAttempts != 1 || s.LastRepairedSHA != "sha-x" {
		t.Errorf("after the dispatch FixAttempts=%d LastRepairedSHA=%q, want 1 and sha-x", s.FixAttempts, s.LastRepairedSHA)
	}
}

// P23: a merge-cap pause (cap reached, attention already raised for the head)
// on a still-conflicting PR launches merge-repair on the next tick once the
// reset has cleared the counter and the notified head.
func TestReset_ThenTickLaunchesMergeRepairForStillConflictingPR(t *testing.T) {
	e := newResetEnv(t)
	paused := pausedState()
	paused.FixAttempts, paused.RepairPending, paused.LastRepairedSHA = 0, false, ""
	paused.LastHeadSHA = "def"
	paused.ConflictRepairSHA, paused.ConflictNotifiedSHA = "def", "def"
	writeStateFile(t, e.path, paused)
	passing := `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`

	done := captureStdout(t)
	err := Reset("42", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	var controlCalls [][]string
	withCommands(t, tickScriptFor(conflictingOpenPR("def"), passing), &controlCalls)
	control := paused
	if _, _, err := tick(&control); err != nil {
		t.Fatalf("control tick: %v", err)
	}
	if n := countWorkflowLaunches(controlCalls, "merge-repair"); n != 0 {
		t.Fatalf("control: merge-repair launches before the reset = %d, want 0 (cap reached)", n)
	}

	var calls [][]string
	withCommands(t, tickScriptFor(conflictingOpenPR("def"), passing), &calls)
	s := mustReadState(t, e.path)
	if _, _, err := tick(&s); err != nil {
		t.Fatalf("tick after reset: %v", err)
	}
	if n := countWorkflowLaunches(calls, "merge-repair"); n != 1 {
		t.Fatalf("merge-repair launches after the reset = %d, want 1: %#v", n, calls)
	}
	if n := countWorkflowLaunches(calls, "babysit-attention"); n != 0 {
		t.Errorf("babysit-attention launches = %d, want 0", n)
	}
	if s.ConflictFixAttempts != 1 {
		t.Errorf("ConflictFixAttempts = %d, want 1 after the dispatch", s.ConflictFixAttempts)
	}
}

// P32: the budget reset is host-only, so neither the event-socket protocol nor
// the daemon may carry a reset kind or handler. The scan finds the stop kind
// first, so a wrong path or an emptied file set cannot pass vacuously.
func TestBudgetReset_HasNoEventSocketKindOrDaemonHandler(t *testing.T) {
	var all strings.Builder
	for _, pattern := range []string{"../ipc/*.go", "../daemon/*.go"} {
		files, err := filepath.Glob(pattern)
		if err != nil || len(files) == 0 {
			t.Fatalf("glob %s: %v (%d files)", pattern, err, len(files))
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			all.Write(b)
		}
	}
	text := strings.ToLower(all.String())
	if !strings.Contains(text, "babysitstop") {
		t.Fatal("scan control: the forwarded stop kind was not found, so the scan reads the wrong files")
	}
	for _, needle := range []string{"babysit-reset", "babysitreset", "babysit_reset"} {
		if strings.Contains(text, needle) {
			t.Errorf("ipc or daemon sources contain %q; the budget reset must stay host-only", needle)
		}
	}
}

// P35: a reset lifts the cap's counters, never the cap itself.
func TestFixCapStaysThree(t *testing.T) {
	if fixCap != 3 {
		t.Fatalf("fixCap = %d, want 3", fixCap)
	}
}
