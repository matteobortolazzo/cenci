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
	"io"
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

// requireStillRunning fails when p exits shortly after the code under test
// ran: a signalled `sleep` dies within milliseconds, so a plain immediate
// check could race the signal.
func requireStillRunning(t *testing.T, p *fixtureProc) {
	t.Helper()
	select {
	case <-p.done:
		t.Errorf("process %d exited: it was signalled", p.pid)
	case <-time.After(200 * time.Millisecond):
	}
}

// deadPID returns the pid of a real child that was started, killed and reaped
// (#1490). It deliberately does not use spawnFixture: that helper's cleanup
// kills the process group at test end, which could hit a recycled group once
// the pid is gone. It skips unless a signal-0 probe proves the pid is gone, so
// a recycled pid can never turn into a test that signals a stranger.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill fixture child: %v", err)
	}
	_ = cmd.Wait()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Skipf("pid %d is not provably gone after the reap (probe err = %v)", pid, err)
	}
	return pid
}

// captureStderr mirrors captureStdout for os.Stderr.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	return func() string {
		os.Stderr = orig
		_ = w.Close()
		b, _ := io.ReadAll(r)
		_ = r.Close()
		return string(b)
	}
}

// goneNote is the host-only stderr line printed when a dead recorded pid is
// cleared (#1490).
func goneNote(pid int) string {
	return fmt.Sprintf("cenci babysit: recorded supervisor pid %d for PR #42 is no longer running; cleared its stale state", pid)
}

// refusalText is the live-but-not-ours refusal, pinned byte for byte by
// TestStop_HostOutputAndErrorsUnchanged (#1390 P34).
func refusalText(pid int) string {
	return fmt.Sprintf("refusing to signal pid %d: it is not the recorded cenci babysit process", pid)
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

// #1431: the re-armed supervisor gets the recorded launch socket on its argv,
// beside --session, and the saved state keeps it; a state recorded before the
// socket existed re-arms with today's argv (asserted by the tests above).
func TestReset_ReArmsWithTheRecordedLaunchSocket(t *testing.T) {
	e := newResetEnv(t)
	paused := pausedState()
	paused.LaunchSocket = "/s/a"
	writeStateFile(t, e.path, paused)

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
	assertFlagValue(t, argv, "--session", "work")
	assertFlagValue(t, argv, "--tmux-socket", "/s/a")
	assertFlagValue(t, argv, "--dir", "/repo/work")
	if s := mustReadState(t, e.path); s.LaunchSocket != "/s/a" {
		t.Errorf("saved launchSocket = %q, want /s/a kept", s.LaunchSocket)
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

// A save that fails after the supervisor was quiesced says it was stopped and not
// re-armed, and how to re-arm; the cause stays matchable. The temp file's path is a
// directory, which fails the write whatever the uid.
func TestReset_FailedSaveAfterTheSupervisorWasStoppedSaysSoAndKeepsTheCause(t *testing.T) {
	e := newResetEnv(t)
	writeStateFile(t, e.path, pausedState())
	if err := os.Mkdir(e.path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}

	err := Reset("42", e.dir, nil)

	if err == nil {
		t.Fatal("Reset: err = nil, want the failed save reported")
	}
	for _, want := range []string{"stopped", "retry budget was not reset", "nothing was re-armed", "cenci babysit 42 --agent codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Reset err = %q, want it to contain %q", err, want)
		}
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("Reset err = %v, want the underlying *os.PathError kept for errors.As", err)
	}
	e.requireNoStart(t)
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

	want := fmt.Sprintf("refusing to signal pid %d: it is not the recorded cenci babysit process", p.pid)
	if err == nil || err.Error() != want {
		t.Fatalf("Reset err = %v, want exactly %q", err, want)
	}
	if !errors.Is(err, ErrSupervisorUnverified) {
		t.Errorf("Reset err = %v, want errors.Is ErrSupervisorUnverified", err)
	}
	requireStillRunning(t, p)
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

// P22, #1457: a legacy state (lastRepairedCiHeadSha equal to the still-failing
// head, written before the ciRepairTracked marker existed) keeps the same-head
// hold and reports the recovery instruction, so tick stays quiet until the
// reset clears it. The control tick on the pre-reset state proves the cleared
// field is what changes the outcome; the budget is below the cap so the control
// cannot pass through the cap branch instead.
func TestReset_ThenTickDispatchesCIRepairAtUnchangedFailingHead(t *testing.T) {
	e := newResetEnv(t)
	paused := pausedState()
	paused.ConflictFixAttempts, paused.ConflictNotifiedSHA, paused.ConflictRepairSHA = 0, "", ""
	paused.FixAttempts = 1
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
	if n := countWorkflowLaunches(controlCalls, "babysit-attention"); n != 0 || control.FixAttempts != 1 {
		t.Fatalf("control: attention launches=%d FixAttempts=%d, want 0/1", n, control.FixAttempts)
	}
	if !strings.Contains(control.AutomergeDetail, "cenci babysit reset 42") || !strings.Contains(control.AutomergeDetail, "work") {
		t.Errorf("control: AutomergeDetail = %q, want the legacy recovery instruction naming the session and the reset", control.AutomergeDetail)
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
	if s.FixAttempts != 1 || s.LastRepairedSHA != "sha-x" || !s.CIRepairTracked {
		t.Errorf("after the dispatch FixAttempts=%d LastRepairedSHA=%q ciRepairTracked=%v, want 1, sha-x and true", s.FixAttempts, s.LastRepairedSHA, s.CIRepairTracked)
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

// ciPausedLiveState is a CI-paused supervisor whose process is alive: it keeps
// its pid while paused (#1456), so reset and stop reach it through the
// verified-pid path.
func ciPausedLiveState(pid int) State {
	s := pausedState()
	s.PID = pid
	s.CIRepairPaused, s.CICapNotified, s.CIRepairTracked = true, true, true
	return s
}

// #1456 AC 8: a reset of a CI-paused state with a live verified supervisor
// stops that process and clears the five budget fields plus both cap markers;
// #1457 adds the ci-repair tracking marker as the eighth.
func TestReset_CIPausedLiveSupervisorClearsAllEightFields(t *testing.T) {
	e := newResetEnv(t)
	p := startOwnedFixture(t, false)
	writeStateFile(t, e.path, ciPausedLiveState(p.pid))
	writeLock(t, e.lock, p.pid)

	done := captureStdout(t)
	err := Reset("42", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if len(e.started) != 1 {
		t.Fatalf("supervisor starts = %d, want 1", len(e.started))
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the paused supervisor never exited")
	}
	status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("paused supervisor exit = %v, want it ended by SIGTERM", p.cmd.ProcessState)
	}
	requireEightFieldsCleared(t, mustReadState(t, e.path))
	raw := string(readFileBytes(t, e.path))
	if strings.Contains(raw, "ciRepairTracked") {
		t.Errorf("state file %s still carries ciRepairTracked after the reset", raw)
	}
}

// #1456 AC 8: stop against the live PID of a CI-paused supervisor signals that
// process and removes the state.
func TestStop_CIPausedLiveSupervisorIsSignalledAndStateRemoved(t *testing.T) {
	dir, path := hostStopFixture(t, "o/r", "42")
	p := startOwnedFixture(t, false)
	writeStateFile(t, path, ciPausedLiveState(p.pid))

	done := captureStdout(t)
	err := Stop("42", dir)
	out := done()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if out != "Stopped babysitting PR #42.\n" {
		t.Errorf("stdout = %q, want %q", out, "Stopped babysitting PR #42.\n")
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the paused supervisor never exited")
	}
	status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("paused supervisor exit = %v, want it ended by SIGTERM", p.cmd.ProcessState)
	}
	requireStoppedState(t, path)
}

// requireStoppedState pins what `stop` leaves behind (#1472): a state with status
// stopped, no pid and no retry budget, rather than a removed file.
func requireStoppedState(t *testing.T, path string) State {
	t.Helper()
	got := mustReadState(t, path)
	if got.Status != "stopped" || got.PID != 0 || got.FixAttempts != 0 || got.CIRepairPaused || got.Agent != "" {
		t.Errorf("state after stop = status %q pid %d fixAttempts %d paused %v agent %q, want a stopped, budget-free state", got.Status, got.PID, got.FixAttempts, got.CIRepairPaused, got.Agent)
	}
	return got
}

// P35: a reset lifts the cap's counters, never the cap itself.
func TestFixCapStaysThree(t *testing.T) {
	if fixCap != 3 {
		t.Fatalf("fixCap = %d, want 3", fixCap)
	}
}

// -- #1490: a recorded supervisor pid that has died -------------------------

// requireEightFieldsCleared pins the reset's eight cleared fields and pid 0.
func requireEightFieldsCleared(t *testing.T, s State) {
	t.Helper()
	if s.FixAttempts != 0 || s.ConflictFixAttempts != 0 || s.RepairPending || s.LastRepairedSHA != "" || s.ConflictNotifiedSHA != "" || s.CIRepairPaused || s.CICapNotified || s.CIRepairTracked || s.PID != 0 {
		t.Errorf("reset fields not cleared: ciFixAttempts=%d conflictFixAttempts=%d ciRepairPending=%v lastRepairedCiHeadSha=%q conflictNotifiedHeadSha=%q ciRepairPaused=%v ciCapNotified=%v ciRepairTracked=%v pid=%d",
			s.FixAttempts, s.ConflictFixAttempts, s.RepairPending, s.LastRepairedSHA, s.ConflictNotifiedSHA, s.CIRepairPaused, s.CICapNotified, s.CIRepairTracked, s.PID)
	}
}

func requireMissing(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s still exists (stat err = %v)", what, err)
	}
}

// AC1: a CI-paused supervisor that died (a reboot) leaves its state and lock.
// Reset treats the proven-gone pid as already stopped, clears the lock before
// the re-arm starts, zeroes all eight fields and re-arms exactly once.
func TestReset_DeadPIDClearsStaleLockZeroesBudgetAndReArms(t *testing.T) {
	e := newResetEnv(t)
	dead := deadPID(t)
	s := ciPausedLiveState(dead)
	s.FixAttempts = 3
	writeStateFile(t, e.path, s)
	writeLock(t, e.lock, dead)

	var lockGone bool
	var atStart State
	e.onStart = func() {
		_, statErr := os.Stat(e.lock)
		lockGone = os.IsNotExist(statErr)
		atStart = mustReadState(t, e.path)
	}

	doneErr := captureStderr(t)
	doneOut := captureStdout(t)
	err := Reset("42", e.dir, nil)
	out, errOut := doneOut(), doneErr()
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
	if !lockGone {
		t.Error("the dead supervisor's lock still existed when the new one started")
	}
	requireEightFieldsCleared(t, atStart)
	requireEightFieldsCleared(t, mustReadState(t, e.path))
	if !strings.Contains(out, "Reset the retry budget for PR #42.") {
		t.Errorf("stdout = %q, want the reset line", out)
	}
	if !strings.Contains(errOut, goneNote(dead)) {
		t.Errorf("stderr = %q, want it to contain %q", errOut, goneNote(dead))
	}
}

// AC2: stop on a state that records a dead pid removes the state and the stale
// lock and reports the usual stdout line; the gone note goes to stderr only.
func TestStop_DeadPIDRemovesStateAndLock(t *testing.T) {
	dir, path := hostStopFixture(t, "o/r", "42")
	lock := path + ".lock"
	dead := deadPID(t)
	writeStateFile(t, path, ciPausedLiveState(dead))
	writeLock(t, lock, dead)

	doneErr := captureStderr(t)
	doneOut := captureStdout(t)
	err := Stop("42", dir)
	out, errOut := doneOut(), doneErr()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if out != "Stopped babysitting PR #42.\n" {
		t.Errorf("stdout = %q, want %q", out, "Stopped babysitting PR #42.\n")
	}
	requireStoppedState(t, path)
	requireMissing(t, lock, "lock file")
	if !strings.Contains(errOut, goneNote(dead)) {
		t.Errorf("stderr = %q, want it to contain %q", errOut, goneNote(dead))
	}
}

// "Sends no signal": a dead pid cannot observably receive one, so the gone
// branch is proven over a live unrelated process with the seam stubbed to
// gone. The process must survive, and Go's pidfd-backed Signal on a dead pid
// returns ErrProcessDone, so a nil return alone would prove nothing.
func TestReset_GonePIDIsNeverSignalled(t *testing.T) {
	e := newResetEnv(t)
	p := startUnrelatedFixture(t)
	stubProcessGone(t, true)
	writeStateFile(t, e.path, ciPausedLiveState(p.pid))

	doneErr := captureStderr(t)
	doneOut := captureStdout(t)
	err := Reset("42", e.dir, nil)
	_, errOut := doneOut(), doneErr()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	requireStillRunning(t, p)
	if len(e.started) != 1 {
		t.Errorf("supervisor starts = %d, want 1", len(e.started))
	}
	if !strings.Contains(errOut, goneNote(p.pid)) {
		t.Errorf("stderr = %q, want the gone note, which proves the gone branch ran", errOut)
	}
}

func TestStop_GonePIDIsNeverSignalled(t *testing.T) {
	dir, path := hostStopFixture(t, "o/r", "42")
	p := startUnrelatedFixture(t)
	stubProcessGone(t, true)
	writeStateFile(t, path, ciPausedLiveState(p.pid))

	doneErr := captureStderr(t)
	doneOut := captureStdout(t)
	err := Stop("42", dir)
	out, errOut := doneOut(), doneErr()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	requireStillRunning(t, p)
	if out != "Stopped babysitting PR #42.\n" {
		t.Errorf("stdout = %q, want %q", out, "Stopped babysitting PR #42.\n")
	}
	requireStoppedState(t, path)
	if !strings.Contains(errOut, goneNote(p.pid)) {
		t.Errorf("stderr = %q, want the gone note, which proves the gone branch ran", errOut)
	}
}

// AC4: a recorded pid that is a live process not owned by this PR keeps the
// exact refusal text and class, and nothing on disk changes. The Reset twin is
// TestReset_UnownedPIDRefusesWithStateAndLockUnchanged.
func TestStop_LiveUnrelatedPIDRefusesWithStateAndLockUnchanged(t *testing.T) {
	dir, path := hostStopFixture(t, "o/r", "42")
	lock := path + ".lock"
	p := startUnrelatedFixture(t)
	writeStateFile(t, path, ciPausedLiveState(p.pid))
	writeLock(t, lock, p.pid)
	stateBefore, lockBefore := readFileBytes(t, path), readFileBytes(t, lock)

	doneOut := captureStdout(t)
	err := Stop("42", dir)
	out := doneOut()

	if err == nil || err.Error() != refusalText(p.pid) {
		t.Fatalf("Stop err = %v, want exactly %q", err, refusalText(p.pid))
	}
	if !errors.Is(err, ErrSupervisorUnverified) {
		t.Errorf("Stop err = %v, want errors.Is ErrSupervisorUnverified", err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing on a refused stop", out)
	}
	requireStillRunning(t, p)
	if !bytes.Equal(readFileBytes(t, path), stateBefore) || !bytes.Equal(readFileBytes(t, lock), lockBefore) {
		t.Error("state file or lock changed")
	}
}

// The seam's probe matrix. Only a proven ESRCH is gone; every other result,
// including a pid the caller may not signal, is not gone (default-deny,
// watch/docs/error-handling.md).
func TestDefaultProcessGone(t *testing.T) {
	dead := deadPID(t)
	live := spawnFixture(t, exec.Command("sleep", "30"))
	if os.Geteuid() == 0 {
		t.Log("running as root: kill(1, 0) succeeds, so the EPERM result is not exercised for pid 1")
	}
	for _, tc := range []struct {
		name string
		pid  int
		want bool
	}{
		{"a reaped child (ESRCH) is gone", dead, true},
		{"a live child is not gone", live.pid, false},
		{"pid 1 (EPERM unprivileged, alive as root) is not gone", 1, false},
		{"pid 0 is not gone", 0, false},
		{"pid -1 is not gone", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultProcessGone(tc.pid); got != tc.want {
				t.Errorf("defaultProcessGone(%d) = %v, want %v", tc.pid, got, tc.want)
			}
		})
	}
}

// goneHarness runs Reset or Stop against a state recording a dead pid, so the
// lock variants and edge cases below hold for both commands.
type goneHarness struct {
	command             string
	statePath, lockPath string
	run                 func() error
	starts              func() int
	stderr              string
}

// newGoneHarness writes s (with its pid set to a freshly dead one unless the
// caller already set one) and returns the harness plus the recorded pid.
func newGoneHarness(t *testing.T, command string, s State) (*goneHarness, int) {
	t.Helper()
	h := &goneHarness{command: command}
	if s.PID == 0 {
		s.PID = deadPID(t)
	}
	switch command {
	case "reset":
		e := newResetEnv(t)
		h.statePath, h.lockPath = e.path, e.lock
		h.starts = func() int { return len(e.started) }
		h.run = func() error { return Reset("42", e.dir, nil) }
	case "stop":
		dir, path := hostStopFixture(t, "o/r", "42")
		h.statePath, h.lockPath = path, path+".lock"
		h.starts = func() int { return 0 }
		h.run = func() error { return Stop("42", dir) }
	default:
		t.Fatalf("unknown command %q", command)
	}
	writeStateFile(t, h.statePath, s)
	inner := h.run
	h.run = func() error {
		doneErr := captureStderr(t)
		doneOut := captureStdout(t)
		err := inner()
		doneOut()
		h.stderr = doneErr()
		return err
	}
	return h, s.PID
}

// requireCleared asserts the command succeeded and cleared everything.
func (h *goneHarness) requireCleared(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", h.command, err)
	}
	requireMissing(t, h.lockPath, "lock file")
	switch h.command {
	case "reset":
		if h.starts() != 1 {
			t.Errorf("supervisor starts = %d, want 1", h.starts())
		}
		requireEightFieldsCleared(t, mustReadState(t, h.statePath))
	case "stop":
		requireStoppedState(t, h.statePath)
	}
}

// requireRefused asserts a refusal that left state and lock byte-identical and
// started nothing. stateBefore and lockBefore are the file contents captured
// before the command ran.
func (h *goneHarness) requireRefused(t *testing.T, err error, wantText string, stateBefore, lockBefore []byte) {
	t.Helper()
	if err == nil || err.Error() != wantText {
		t.Fatalf("%s err = %v, want exactly %q", h.command, err, wantText)
	}
	if !errors.Is(err, ErrSupervisorUnverified) {
		t.Errorf("%s err = %v, want errors.Is ErrSupervisorUnverified", h.command, err)
	}
	if !bytes.Equal(readFileBytes(t, h.statePath), stateBefore) {
		t.Error("state file changed")
	}
	if !bytes.Equal(readFileBytes(t, h.lockPath), lockBefore) {
		t.Error("lock file changed")
	}
	if h.starts() != 0 {
		t.Errorf("supervisor starts = %d, want 0", h.starts())
	}
}

var goneCommands = []string{"reset", "stop"}

// Lock variants for a dead recorded pid. The lock is cleared unless it names a
// live supervisor for this PR or cannot be read as a pid: removing a live
// supervisor's lock would allow two supervisors, and an empty lock most likely
// means an arm is mid-flight (Run writes the pid right after its O_EXCL create).
func TestResetAndStop_DeadPIDLockVariants(t *testing.T) {
	for _, command := range goneCommands {
		t.Run(command+"/no lock", func(t *testing.T) {
			h, _ := newGoneHarness(t, command, ciPausedLiveState(0))
			h.requireCleared(t, h.run())
		})

		t.Run(command+"/lock naming a dead pid is stale and removed", func(t *testing.T) {
			h, _ := newGoneHarness(t, command, ciPausedLiveState(0))
			writeLock(t, h.lockPath, deadPID(t))
			h.requireCleared(t, h.run())
		})

		t.Run(command+"/lock naming a live unrelated process is stale and removed", func(t *testing.T) {
			h, _ := newGoneHarness(t, command, ciPausedLiveState(0))
			other := startUnrelatedFixture(t)
			writeLock(t, h.lockPath, other.pid)
			h.requireCleared(t, h.run())
			requireStillRunning(t, other)
		})

		t.Run(command+"/lock naming a live supervisor for this PR is refused", func(t *testing.T) {
			h, _ := newGoneHarness(t, command, ciPausedLiveState(0))
			owner := startOwnedFixture(t, false)
			writeLock(t, h.lockPath, owner.pid)
			stateBefore, lockBefore := readFileBytes(t, h.statePath), readFileBytes(t, h.lockPath)

			err := h.run()

			h.requireRefused(t, err, fmt.Sprintf("supervisor already running for PR #42 (%d)", owner.pid), stateBefore, lockBefore)
			requireStillRunning(t, owner)
		})

		for _, tc := range []struct{ name, contents, wantText string }{
			{"empty", "", "supervisor already running for PR #42 ()"},
			{"unparseable", "garbage\n", "supervisor already running for PR #42 (garbage)"},
		} {
			t.Run(command+"/"+tc.name+" lock is refused", func(t *testing.T) {
				h, _ := newGoneHarness(t, command, ciPausedLiveState(0))
				if err := os.WriteFile(h.lockPath, []byte(tc.contents), 0o600); err != nil {
					t.Fatal(err)
				}
				stateBefore, lockBefore := readFileBytes(t, h.statePath), readFileBytes(t, h.lockPath)

				err := h.run()

				h.requireRefused(t, err, tc.wantText, stateBefore, lockBefore)
			})
		}
	}
}

// Edge cases on a dead recorded pid.
func TestResetAndStop_DeadPIDEdgeCases(t *testing.T) {
	for _, command := range goneCommands {
		t.Run(command+"/a running status takes the same gone path as needs-input", func(t *testing.T) {
			s := pausedState()
			s.Status = "running"
			h, dead := newGoneHarness(t, command, s)
			writeLock(t, h.lockPath, dead)

			h.requireCleared(t, h.run())

			if !strings.Contains(h.stderr, goneNote(dead)) {
				t.Errorf("stderr = %q, want it to contain %q", h.stderr, goneNote(dead))
			}
		})

		// Only a proven ESRCH is gone: when the probe is not conclusive (here
		// stubbed to "not gone", as EPERM would give) the unchanged refusal
		// applies, even though the pid is in fact dead.
		t.Run(command+"/a probe that does not prove the pid gone keeps the refusal", func(t *testing.T) {
			h, dead := newGoneHarness(t, command, ciPausedLiveState(0))
			writeLock(t, h.lockPath, dead)
			stubProcessGone(t, false)
			stateBefore, lockBefore := readFileBytes(t, h.statePath), readFileBytes(t, h.lockPath)

			err := h.run()

			h.requireRefused(t, err, refusalText(dead), stateBefore, lockBefore)
		})
	}
}

// The agent check runs before anything is removed or written, gone pid or not.
func TestReset_DeadPIDAgentRefusalPrecedesLockRemovalAndStateWrite(t *testing.T) {
	e := newResetEnv(t)
	dead := deadPID(t)
	writeStateFile(t, e.path, ciPausedLiveState(dead))
	writeLock(t, e.lock, dead)
	stateBefore, lockBefore := readFileBytes(t, e.path), readFileBytes(t, e.lock)

	const reason = "agent codex may not be re-armed here"
	err := Reset("42", e.dir, func(string) string { return reason })

	if !errors.Is(err, ErrAgentRefused) || err.Error() != reason {
		t.Fatalf("Reset err = %v, want ErrAgentRefused carrying %q", err, reason)
	}
	if !bytes.Equal(readFileBytes(t, e.path), stateBefore) || !bytes.Equal(readFileBytes(t, e.lock), lockBefore) {
		t.Error("state file or lock changed")
	}
	e.requireNoStart(t)
}

// The host-only guards keep their existing messages and change nothing, even
// when the recorded pid is dead.
func TestReset_DeadPIDHostOnlyGuardsKeepTheirMessages(t *testing.T) {
	for _, tc := range []struct{ name, env, want string }{
		{
			"inside a sandbox", "CENCI_SANDBOX",
			"not reset: the babysit supervisor runs on the host and its retry budget can only be reset there; run `cenci babysit reset 42` from a host tmux pane",
		},
		{
			"inside a supervisor", "CENCI_BABYSIT_SUPERVISOR",
			"not reset: cannot re-arm from inside a babysit supervisor; run `cenci babysit reset` from a host tmux pane",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newResetEnv(t)
			t.Setenv(tc.env, "1")
			dead := deadPID(t)
			writeStateFile(t, e.path, ciPausedLiveState(dead))
			writeLock(t, e.lock, dead)
			stateBefore, lockBefore := readFileBytes(t, e.path), readFileBytes(t, e.lock)

			err := Reset("42", e.dir, nil)

			if err == nil || err.Error() != tc.want {
				t.Fatalf("Reset err = %v, want exactly %q", err, tc.want)
			}
			if !bytes.Equal(readFileBytes(t, e.path), stateBefore) || !bytes.Equal(readFileBytes(t, e.lock), lockBefore) {
				t.Error("state file or lock changed")
			}
			e.requireNoStart(t)
		})
	}
}

// -- #1472: stop keeps the feedback bookkeeping; arm is strict ----------------

// feedbackRichState is a CI-paused supervisor whose feedback bookkeeping (every
// key list, the watermarks, the discussion fields) a stop must keep, and whose
// budgets, agent and CI verdict it must clear.
func feedbackRichState(pid int) State {
	s := ciPausedLiveState(pid)
	s.CIStatus, s.ClosingIssues, s.RepoRoot = ciStatusFailing, []int{9}, "/repo/root"
	s.PendingCommentAt, s.PendingHeadSHA = "2026-10-02T10:00:00Z", "sha-p"
	s.PendingKeys = []string{"comment:2", "discussion:11"}
	s.LaunchedKeys = []string{"comment:2", "discussion:11"}
	s.AddressedKeys = []string{"comment:1", "discussion:9"}
	s.DiscussionRevisions = map[string]string{"discussion:11": strings.Repeat("b", 64), "discussion:9": strings.Repeat("a", 64)}
	s.DiscussionReceipts = map[string]discussionReceipt{"discussion:9": {CommentID: 9, Revision: strings.Repeat("a", 64), Outcome: "addressed", ReceiptCommentID: 8, Source: receiptSourceReceipt}}
	s.ReviewLaunch = &reviewLaunchRecord{Nonce: testReviewNonce, LaunchedAt: testLaunchAt, Keys: []string{"comment:2", "discussion:11"}}
	s.ConsumedReviewNonces = map[string]int64{testWrongNonce: 8}
	return s
}

// Q&A 1 (P2): stop SIGTERMs the verified supervisor, waits for it to exit,
// removes the lock and rewrites the state keeping only the feedback bookkeeping,
// with status `stopped` and no pid.
func TestStopKeepsOnlyTheFeedbackBookkeepingAfterTheSupervisorExits(t *testing.T) {
	e := newResetEnv(t)
	owner := startOwnedFixture(t, false)
	writeStateFile(t, e.path, feedbackRichState(owner.pid))
	writeLock(t, e.lock, owner.pid)

	if err := StopRepo("o/r", "42", e.dir); err != nil {
		t.Fatalf("StopRepo: %v", err)
	}

	select {
	case <-owner.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor was not terminated")
	}
	requireMissing(t, e.lock, "supervisor lock")
	want := feedbackRichState(0)
	got := mustReadState(t, e.path)
	for name, pair := range map[string][2]any{
		"pending":   {got.PendingKeys, want.PendingKeys},
		"launched":  {got.LaunchedKeys, want.LaunchedKeys},
		"addressed": {got.AddressedKeys, want.AddressedKeys},
		"revisions": {got.DiscussionRevisions, want.DiscussionRevisions},
		"receipts":  {got.DiscussionReceipts, want.DiscussionReceipts},
		"launch":    {got.ReviewLaunch, want.ReviewLaunch},
		"consumed":  {got.ConsumedReviewNonces, want.ConsumedReviewNonces},
		"pendingAt": {got.PendingCommentAt, want.PendingCommentAt},
		"pendingHd": {got.PendingHeadSHA, want.PendingHeadSHA},
		"lastAt":    {got.LastCommentAt, want.LastCommentAt},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s = %v, want %v kept across stop", name, pair[0], pair[1])
		}
	}
	if got.Status != "stopped" || got.PID != 0 || got.Repo != "o/r" || got.PR != "42" {
		t.Errorf("status/pid/repo/pr = %q/%d/%q/%q, want stopped/0/o/r/42", got.Status, got.PID, got.Repo, got.PR)
	}
	if got.Agent != "" || got.FixAttempts != 0 || got.ConflictFixAttempts != 0 || got.RepairPending || got.CIRepairPaused || got.CICapNotified ||
		got.CIStatus != "" || len(got.ClosingIssues) != 0 || got.LastRepairedSHA != "" || got.ConflictNotifiedSHA != "" {
		t.Errorf("agent, budgets, CI status and closing issues must be cleared by stop, got %+v", got)
	}
}

// P2: a re-arm over a stopped state adopts a fresh agent, keeps the pending keys
// and so keeps the review-stage hold, and never starts from empty PendingKeys.
func TestArmOverAStoppedStateAdoptsANewAgentAndKeepsTheFeedback(t *testing.T) {
	e := newResetEnv(t)
	writeStateFile(t, e.path, feedbackRichState(0))
	if err := StopRepo("o/r", "42", e.dir); err != nil {
		t.Fatalf("StopRepo: %v", err)
	}

	if err := Run(Options{PR: "42", Agent: "claude", StateDir: e.dir, Interval: time.Minute, Session: "work", Dir: "/repo/root"}); err != nil {
		t.Fatalf("Run (re-arm): %v", err)
	}

	got := mustReadState(t, e.path)
	if got.Agent != "claude" || got.Status != "arming" || !reflect.DeepEqual(got.PendingKeys, []string{"comment:2", "discussion:11"}) {
		t.Errorf("agent/status/pending = %q/%q/%v, want claude/arming/the kept pending keys", got.Agent, got.Status, got.PendingKeys)
	}
	if len(e.started) != 1 {
		t.Errorf("supervisor starts = %d, want 1", len(e.started))
	}
}

// P3 / AC: an unreadable or undecodable state at arm exits with the named reason
// and the path, never overwrites the file, never starts a supervisor and never
// starts from empty PendingKeys; the same holds in the supervisor child.
func TestArmRefusesAnUnreadableStateAndLeavesItByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		child   bool
	}{
		{"truncated JSON at arm", `{"schemaVersion":2,"pr":"42","repo":"o/r","pendingCommentKeys":["discussion:1"`, false},
		{"empty file at arm", "", false},
		{"truncated JSON in the supervisor child", `{"schemaVersion":2,"pr":"42","repo":"o/r","pendingCommentKeys":[`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newResetEnv(t)
			if tc.child {
				t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
			}
			if err := os.WriteFile(e.path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}

			err := Run(Options{PR: "42", Agent: "claude", StateDir: e.dir, Interval: time.Minute, Once: tc.child, Session: "work", Dir: "/repo/root"})

			if err == nil || !strings.Contains(err.Error(), reasonStateUnreadable) || !strings.Contains(err.Error(), e.path) {
				t.Fatalf("Run err = %v, want %q naming %s", err, reasonStateUnreadable, e.path)
			}
			if !bytes.Equal(readFileBytes(t, e.path), []byte(tc.content)) {
				t.Error("the unreadable state file was overwritten")
			}
			e.requireNoStart(t)
			requireMissing(t, e.lock, "supervisor lock")
		})
	}
}

// AC: the host `status` of an unreadable state names the path and the cause and
// fails; the daemon-relayed class stays path-free; a stopped state reports
// `stopped`.
func TestStatusNamesTheUnreadablePathOnTheHostAndReportsStopped(t *testing.T) {
	t.Run("unreadable state", func(t *testing.T) {
		dir, path := hostStopFixture(t, "o/r", "42")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := Status("42", dir)
		if err == nil || !strings.HasPrefix(err.Error(), reasonStateUnreadable+": "+path+": ") {
			t.Fatalf("Status err = %v, want %q followed by the path and the cause", err, reasonStateUnreadable+": "+path+": ")
		}
		if _, relayed := StatusRepo("o/r", "42", dir); strings.Contains(relayed.Error(), path) {
			t.Errorf("StatusRepo error %q names the state path", relayed)
		}
	})
	t.Run("stopped state", func(t *testing.T) {
		dir, path := hostStopFixture(t, "o/r", "42")
		writeStateFile(t, path, State{PR: "42", Repo: "o/r", Status: "stopped"})
		done := captureStdout(t)
		err := Status("42", dir)
		out := done()
		if err != nil || !strings.Contains(out, `"status":"stopped"`) {
			t.Errorf("Status = %v, stdout %q, want a stopped report", err, out)
		}
	})
}
