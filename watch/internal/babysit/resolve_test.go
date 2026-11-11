package babysit

// #1472: host-only, attended-only `cenci babysit resolve <pr> discussion:<id>`.
// These tests run a real Resolve against a temp state directory, reusing the
// Reset harness (newResetEnv): gh and git go through the stubbed seams, the
// detached supervisor start is captured through startSupervisor, and the
// liveness checks use real child processes (watch/docs/test-strategy.md). The
// attended and sandbox gates are env vars, so every test sets them explicitly
// (watch/docs/test-isolation.md).

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

var (
	resolveRevB = strings.Repeat("b", 64)
	resolveRevC = strings.Repeat("c", 64)
)

// newResolveEnv is newResetEnv plus an attended session: CENCI_ATTENDED and its
// origin marker are cleared, so a test that wants an unattended session sets them.
func newResolveEnv(t *testing.T) *resetEnv {
	t.Helper()
	e := newResetEnv(t)
	t.Setenv("CENCI_ATTENDED", "")
	t.Setenv("CENCI_ATTENDED_ORIGIN", "")
	return e
}

// resolveState is a live-looking supervisor for PR 42 with two pending
// discussion keys (one launched with an outstanding review launch), a pending
// inline key, one addressed inline key and a non-zero retry budget a resolve
// must not touch.
func resolveState(pid int) State {
	return State{
		SchemaVersion: stateSchemaVersion, PR: "42", Repo: "o/r", Agent: "codex",
		IntervalSeconds: 300, Status: "needs-input", PID: pid,
		LaunchSession: "work", LaunchDir: "/repo/work", FixAttempts: 2,
		AddressedKeys:       []string{"comment:1"},
		PendingKeys:         []string{"comment:2", "discussion:11", "discussion:12"},
		LaunchedKeys:        []string{"comment:2", "discussion:11"},
		DiscussionRevisions: map[string]string{"discussion:11": resolveRevB, "discussion:12": resolveRevC},
		ReviewLaunch:        &reviewLaunchRecord{Nonce: testReviewNonce, LaunchedAt: testLaunchAt, Keys: []string{"comment:2", "discussion:11"}},
	}
}

// requireRefused asserts a refusal left the state file byte-identical and
// started no supervisor.
func (e *resetEnv) requireRefused(t *testing.T, err error, want string, before []byte) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Resolve err = %v, want a refusal containing %q", err, want)
	}
	if !bytes.Equal(readFileBytes(t, e.path), before) {
		t.Error("the refused resolve changed the state file")
	}
	e.requireNoStart(t)
}

func TestResolve_RefusesInsideASandboxAndLeavesTheStateByteIdentical(t *testing.T) {
	e := newResolveEnv(t)
	t.Setenv("CENCI_SANDBOX", "1")
	writeStateFile(t, e.path, resolveState(0))
	before := readFileBytes(t, e.path)

	err := Resolve("42", "discussion:11", e.dir, nil)

	e.requireRefused(t, err, "runs on the host", before)
	if !strings.Contains(err.Error(), "cenci babysit resolve 42 discussion:11") {
		t.Errorf("Resolve err = %q, want it to name the host command", err)
	}
}

// An unattended session can never resolve, whatever set CENCI_ATTENDED=0: the
// origin marker that exempts a host-config value for plan approval does not
// exempt this gate (fail closed, so a supervisor-launched worker is refused).
func TestResolve_RefusesAnUnattendedSessionWhateverTheOrigin(t *testing.T) {
	for _, origin := range []string{"", "host-config", "something-else"} {
		t.Run("origin "+origin, func(t *testing.T) {
			e := newResolveEnv(t)
			t.Setenv("CENCI_ATTENDED", "0")
			t.Setenv("CENCI_ATTENDED_ORIGIN", origin)
			writeStateFile(t, e.path, resolveState(0))
			before := readFileBytes(t, e.path)

			err := Resolve("42", "discussion:11", e.dir, nil)

			e.requireRefused(t, err, "CENCI_ATTENDED=0", before)
		})
	}
}

func TestResolve_AttendedValueOtherThanZeroIsAllowed(t *testing.T) {
	e := newResolveEnv(t)
	t.Setenv("CENCI_ATTENDED", "1")
	writeStateFile(t, e.path, resolveState(0))

	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, nil)
	_ = done()

	if err != nil {
		t.Fatalf("Resolve with CENCI_ATTENDED=1: %v", err)
	}
}

func TestResolve_InsideASupervisorRefusesWithoutSignallingIt(t *testing.T) {
	e := newResolveEnv(t)
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
	p := startOwnedFixture(t, false)
	writeStateFile(t, e.path, resolveState(p.pid))
	writeLock(t, e.lock, p.pid)
	before := readFileBytes(t, e.path)

	err := Resolve("42", "discussion:11", e.dir, nil)

	e.requireRefused(t, err, "inside a babysit supervisor", before)
	if !p.alive() {
		t.Error("the live supervisor was signalled")
	}
}

func TestResolve_InlineAndReviewKeysAreResolvedOnGitHub(t *testing.T) {
	for _, key := range []string{"comment:2", "review:7"} {
		t.Run(key, func(t *testing.T) {
			e := newResolveEnv(t)
			s := resolveState(0)
			s.PendingKeys = append(s.PendingKeys, "review:7")
			writeStateFile(t, e.path, s)
			before := readFileBytes(t, e.path)

			err := Resolve("42", key, e.dir, nil)

			e.requireRefused(t, err, "resolve it on GitHub", before)
			if !strings.Contains(err.Error(), key) {
				t.Errorf("Resolve err = %q, want it to name %s", err, key)
			}
		})
	}
}

// resolve only accepts a PENDING discussion key: an addressed key, an unknown key
// and a pending key of another kind are all refused, with nothing changed.
func TestResolve_RefusesAKeyThatIsNotPending(t *testing.T) {
	for name, key := range map[string]string{"an addressed key": "discussion:9", "an unknown key": "discussion:99"} {
		t.Run(name, func(t *testing.T) {
			e := newResolveEnv(t)
			s := resolveState(0)
			s.AddressedKeys = append(s.AddressedKeys, "discussion:9")
			writeStateFile(t, e.path, s)
			before := readFileBytes(t, e.path)

			err := Resolve("42", key, e.dir, nil)

			e.requireRefused(t, err, key+" is not pending for PR #42", before)
		})
	}
}

func TestResolve_MalformedPRAndKeyAreUsageErrors(t *testing.T) {
	for _, tc := range []struct{ pr, key string }{
		{"abc", "discussion:11"}, {"0", "discussion:11"}, {"42", ""}, {"42", "foo"},
		{"42", "discussion:"}, {"42", "discussion:abc"}, {"42", "discussion:0"}, {"42", "discussion:-3"},
		{"42", "comment:"}, {"42", "review:"},
	} {
		t.Run(tc.pr+" "+tc.key, func(t *testing.T) {
			e := newResolveEnv(t)
			writeStateFile(t, e.path, resolveState(0))
			before := readFileBytes(t, e.path)

			err := Resolve(tc.pr, tc.key, e.dir, nil)

			if !errors.Is(err, ErrResolveUsage) {
				t.Fatalf("Resolve(%q, %q) err = %v, want errors.Is ErrResolveUsage", tc.pr, tc.key, err)
			}
			if !bytes.Equal(readFileBytes(t, e.path), before) {
				t.Error("state file changed")
			}
			e.requireNoStart(t)
		})
	}
	for _, key := range []string{"discussion:11", "comment:5", "review:7"} {
		if !ValidResolveKey(key) {
			t.Errorf("ValidResolveKey(%q) = false, want true", key)
		}
	}
}

func TestResolve_MissingStateRefusesAndStartsNothing(t *testing.T) {
	e := newResolveEnv(t)

	err := Resolve("42", "discussion:11", e.dir, nil)

	if err == nil || err.Error() != "no supervisor found for PR #42" || !errors.Is(err, ErrNoSupervisor) {
		t.Fatalf("Resolve err = %v, want ErrNoSupervisor %q", err, "no supervisor found for PR #42")
	}
	if _, statErr := os.Stat(e.path); !os.IsNotExist(statErr) {
		t.Errorf("a state file was created (stat err = %v)", statErr)
	}
	e.requireNoStart(t)
}

// An unreadable or undecodable state is named with its path and cause, never
// mistaken for an empty one, and the file stays byte-identical.
func TestResolve_UnreadableStateIsRefusedByteIdentical(t *testing.T) {
	t.Run("undecodable", func(t *testing.T) {
		e := newResolveEnv(t)
		const garbage = `{"schemaVersion":2,"pr":"42","pendingCommentKeys":["discussion:11"`
		if err := os.WriteFile(e.path, []byte(garbage), 0o600); err != nil {
			t.Fatal(err)
		}

		err := Resolve("42", "discussion:11", e.dir, nil)

		e.requireRefused(t, err, reasonStateUnreadable+": "+e.path+": ", []byte(garbage))
		if !errors.Is(err, ErrStateUnreadable) {
			t.Errorf("Resolve err = %v, want errors.Is ErrStateUnreadable", err)
		}
	})
	t.Run("unreadable path", func(t *testing.T) {
		e := newResolveEnv(t)
		if err := os.Mkdir(e.path, 0o700); err != nil {
			t.Fatal(err)
		}

		err := Resolve("42", "discussion:11", e.dir, nil)

		if err == nil || !strings.HasPrefix(err.Error(), reasonStateUnreadable+": "+e.path+": ") {
			t.Fatalf("Resolve err = %v, want %q followed by the path and the cause", err, reasonStateUnreadable)
		}
		if info, statErr := os.Stat(e.path); statErr != nil || !info.IsDir() {
			t.Errorf("state path was replaced (stat err = %v)", statErr)
		}
		e.requireNoStart(t)
	})
}

// Success: the key moves from pending and launched to addressed with a
// human-cli receipt at the recorded revision; everything else (other keys, the
// retry budget, the outstanding launch) is untouched; the supervisor is re-armed
// with the recorded settings.
func TestResolve_RecordsAHumanCLIReceiptAndReArmsWithTheRecordedSettings(t *testing.T) {
	e := newResolveEnv(t)
	writeStateFile(t, e.path, resolveState(0))

	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, nil)
	out := done()

	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(out, "Resolved discussion:11 for PR #42.") {
		t.Errorf("stdout = %q, want the resolved line", out)
	}
	s := mustReadState(t, e.path)
	if want := []string{"comment:2", "discussion:12"}; !reflect.DeepEqual(s.PendingKeys, want) {
		t.Errorf("PendingKeys = %v, want %v", s.PendingKeys, want)
	}
	if want := []string{"comment:2"}; !reflect.DeepEqual(s.LaunchedKeys, want) {
		t.Errorf("LaunchedKeys = %v, want %v", s.LaunchedKeys, want)
	}
	if want := []string{"comment:1", "discussion:11"}; !reflect.DeepEqual(s.AddressedKeys, want) {
		t.Errorf("AddressedKeys = %v, want %v", s.AddressedKeys, want)
	}
	wantReceipt := discussionReceipt{CommentID: 11, Revision: resolveRevB, Outcome: "addressed", ReceiptCommentID: 0, Source: "human-cli"}
	if got := s.DiscussionReceipts["discussion:11"]; got != wantReceipt {
		t.Errorf("receipt = %+v, want %+v", got, wantReceipt)
	}
	if len(s.DiscussionReceipts) != 1 {
		t.Errorf("receipts = %v, want only the resolved key's", s.DiscussionReceipts)
	}
	if !reflect.DeepEqual(s.DiscussionRevisions, resolveState(0).DiscussionRevisions) {
		t.Errorf("DiscussionRevisions = %v, want them unchanged", s.DiscussionRevisions)
	}
	if s.FixAttempts != 2 {
		t.Errorf("FixAttempts = %d, want the retry budget untouched (resolve is not reset)", s.FixAttempts)
	}
	if s.PID != 0 {
		t.Errorf("PID = %d, want 0 for the re-arm", s.PID)
	}

	if len(e.started) != 1 {
		t.Fatalf("supervisor starts = %d, want 1", len(e.started))
	}
	wantArgv := []string{os.Args[0], "babysit", "42", "--agent", "codex", "--interval", "5m0s", "--state-dir", e.dir, "--session", "work", "--dir", "/repo/work"}
	if !reflect.DeepEqual(e.started[0], wantArgv) {
		t.Errorf("supervisor argv = %q, want %q", e.started[0], wantArgv)
	}
}

// A save that fails after the supervisor was quiesced says so: the key was not
// resolved, nothing was re-armed, and how to re-arm; the cause stays matchable.
// The temp file's path is a directory, which fails the write whatever the uid.
func TestResolve_FailedSaveAfterTheSupervisorWasStoppedSaysSoAndKeepsTheCause(t *testing.T) {
	e := newResolveEnv(t)
	writeStateFile(t, e.path, resolveState(0))
	if err := os.Mkdir(e.path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}

	err := Resolve("42", "discussion:11", e.dir, nil)

	if err == nil {
		t.Fatal("Resolve: err = nil, want the failed save reported")
	}
	for _, want := range []string{"stopped", "discussion:11 was not resolved", "nothing was re-armed", "cenci babysit 42 --agent codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve err = %q, want it to contain %q", err, want)
		}
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("Resolve err = %v, want the underlying *os.PathError kept for errors.As", err)
	}
	e.requireNoStart(t)
}

// A key a babysit-review receipt left pending because its comment author lacks write
// permission (the launch's trust record does not list it, the worker's `addressed`
// was refused) is the human's to handle: `resolve` clears it like any pending
// discussion key and keeps the launch's trust record as it was.
func TestResolve_ClearsAnUntrustedPendingKeyTheWorkerCouldNotAddress(t *testing.T) {
	e := newResolveEnv(t)
	s := resolveState(0)
	s.ReviewLaunch.TrustedKeys = []string{"discussion:12"}
	s.DiscussionReceipts = map[string]discussionReceipt{
		"discussion:11": {CommentID: 11, Revision: resolveRevB, Outcome: receiptOutcomeBlocked, ReceiptCommentID: 21, Source: receiptSourceReceipt},
	}
	writeStateFile(t, e.path, s)

	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, nil)
	_ = done()

	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := mustReadState(t, e.path)
	if slices.Contains(got.PendingKeys, "discussion:11") || !slices.Contains(got.AddressedKeys, "discussion:11") {
		t.Errorf("pending/addressed = %v/%v, want discussion:11 addressed", got.PendingKeys, got.AddressedKeys)
	}
	if rec := got.DiscussionReceipts["discussion:11"]; rec.Source != receiptSourceHumanCLI || rec.Outcome != receiptOutcomeAddressed {
		t.Errorf("receipt = %+v, want a human-cli addressed record replacing the refused one", rec)
	}
	if got.ReviewLaunch == nil || !slices.Equal(got.ReviewLaunch.TrustedKeys, []string{"discussion:12"}) {
		t.Errorf("ReviewLaunch = %+v, want the outstanding launch (and its trust record) kept", got.ReviewLaunch)
	}
}

// Q&A 12: a stopped state records no agent and the human stopped it on purpose,
// so the resolution is written and nothing is started -- and the agent check, which
// only guards a re-arm, is never consulted.
func TestResolve_StoppedStateIsWrittenAndNeverReArmed(t *testing.T) {
	e := newResolveEnv(t)
	writeStateFile(t, e.path, stoppedState(resolveState(0), "o/r", "42"))
	refusal := func(string) string { t.Error("the agent check ran for a stopped state"); return "refused" }

	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, refusal)
	out := done()

	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	e.requireNoStart(t)
	if !strings.Contains(out, "Resolved discussion:11 for PR #42") || !strings.Contains(out, "not re-armed") || !strings.Contains(out, "cenci babysit 42 --agent") {
		t.Errorf("stdout = %q, want the resolved line and the re-arm hint", out)
	}
	s := mustReadState(t, e.path)
	if s.Status != "stopped" || s.PID != 0 || s.Agent != "" {
		t.Errorf("status/pid/agent = %q/%d/%q, want a stopped state with no agent kept", s.Status, s.PID, s.Agent)
	}
	if want := []string{"comment:2", "discussion:12"}; !reflect.DeepEqual(s.PendingKeys, want) {
		t.Errorf("PendingKeys = %v, want %v", s.PendingKeys, want)
	}
	if !reflect.DeepEqual(s.AddressedKeys, []string{"comment:1", "discussion:11"}) || s.DiscussionReceipts["discussion:11"].Source != "human-cli" {
		t.Errorf("addressed = %v receipt = %+v, want discussion:11 addressed with a human-cli receipt", s.AddressedKeys, s.DiscussionReceipts["discussion:11"])
	}
}

// A live owned supervisor is verified, SIGTERMed and awaited, its lock removed,
// and only then is the resolution written and a new supervisor started: the old
// one can never save over it.
func TestResolve_LiveOwnedSupervisorIsTerminatedThenReArmed(t *testing.T) {
	e := newResolveEnv(t)
	p := startOwnedFixture(t, false)
	writeStateFile(t, e.path, resolveState(p.pid))
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
	err := Resolve("42", "discussion:11", e.dir, nil)
	_ = done()

	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(e.started) != 1 {
		t.Fatalf("supervisor starts = %d, want 1", len(e.started))
	}
	if stillOwned || !lockGone {
		t.Errorf("at re-arm: old supervisor still running = %v, old lock removed = %v, want false/true", stillOwned, lockGone)
	}
	if atStart.PID != 0 || !reflect.DeepEqual(atStart.AddressedKeys, []string{"comment:1", "discussion:11"}) {
		t.Errorf("state at re-arm: pid %d addressed %v, want pid 0 with discussion:11 addressed", atStart.PID, atStart.AddressedKeys)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the old supervisor never exited")
	}
	if status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("old supervisor exit = %v, want it ended by SIGTERM", p.cmd.ProcessState)
	}
}

// The state is re-read strictly after the supervisor exits: the fixture saves a
// state on SIGTERM in which the supervisor had already accepted a receipt for the
// key. The resolution must apply to that fresh state (another key it moved stays
// moved), the already-addressed key is not recorded a second time, and the
// supervisor is still re-armed.
func TestResolve_UsesTheStateSavedWhileTheSupervisorDied(t *testing.T) {
	e := newResolveEnv(t)
	if !procfsReadable() {
		t.Skip("real ownership check needs a readable procfs")
	}
	staged := filepath.Join(t.TempDir(), "dying-save.json")
	ready := filepath.Join(t.TempDir(), "ready")
	script := `trap 'cp "$2" "$3"; exit 0' TERM; : > "$1"; sleep 30 & wait`
	p := spawnFixture(t, exec.Command("sh", "-c", script, "babysit-42-fixture", ready, staged, e.path))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture process never became ready")
		}
	}
	before := resolveState(p.pid)
	writeStateFile(t, e.path, before)
	writeLock(t, e.lock, p.pid)

	dying := before
	dying.PendingKeys = []string{"comment:2"}
	dying.LaunchedKeys = []string{"comment:2"}
	dying.AddressedKeys = []string{"comment:1", "discussion:11", "discussion:12"}
	dying.DiscussionReceipts = map[string]discussionReceipt{
		"discussion:11": {CommentID: 11, Revision: resolveRevB, Outcome: "addressed", ReceiptCommentID: 900, Source: "receipt"},
	}
	writeStateFile(t, staged, dying)

	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, nil)
	out := done()

	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	s := mustReadState(t, e.path)
	if !reflect.DeepEqual(s.AddressedKeys, []string{"comment:1", "discussion:11", "discussion:12"}) || !reflect.DeepEqual(s.PendingKeys, []string{"comment:2"}) {
		t.Errorf("addressed %v pending %v, want the save made while the supervisor died kept", s.AddressedKeys, s.PendingKeys)
	}
	if got := s.DiscussionReceipts["discussion:11"]; got.Source != "receipt" || got.ReceiptCommentID != 900 {
		t.Errorf("receipt = %+v, want the supervisor's own receipt kept, not overwritten", got)
	}
	if !strings.Contains(out, "already") {
		t.Errorf("stdout = %q, want a note that the key was already addressed", out)
	}
	if len(e.started) != 1 {
		t.Errorf("supervisor starts = %d, want the supervisor re-armed", len(e.started))
	}
}

// The agent check guards the re-arm: a refused agent is reported as
// ErrAgentRefused before any signal or write.
func TestResolve_RefusedAgentPrecedesAnySignalOrWrite(t *testing.T) {
	e := newResolveEnv(t)
	p := startOwnedFixture(t, false)
	writeStateFile(t, e.path, resolveState(p.pid))
	writeLock(t, e.lock, p.pid)
	before := readFileBytes(t, e.path)
	refusal := func(agent string) string { return "agent " + agent + " is disabled" }

	err := Resolve("42", "discussion:11", e.dir, refusal)

	if !errors.Is(err, ErrAgentRefused) || err.Error() != "agent codex is disabled" {
		t.Fatalf("Resolve err = %v, want ErrAgentRefused %q", err, "agent codex is disabled")
	}
	if !p.alive() {
		t.Error("the live supervisor was signalled")
	}
	if !bytes.Equal(readFileBytes(t, e.path), before) {
		t.Error("state file changed")
	}
	e.requireNoStart(t)
}

func TestResolve_UnownedPIDRefusesWithStateUnchanged(t *testing.T) {
	e := newResolveEnv(t)
	p := startUnrelatedFixture(t)
	writeStateFile(t, e.path, resolveState(p.pid))
	before := readFileBytes(t, e.path)

	err := Resolve("42", "discussion:11", e.dir, nil)

	e.requireRefused(t, err, refusalText(p.pid), before)
	if !errors.Is(err, ErrSupervisorUnverified) {
		t.Errorf("Resolve err = %v, want errors.Is ErrSupervisorUnverified", err)
	}
	requireStillRunning(t, p)
}

// The resolution is saved before the re-arm, so a re-arm that fails leaves it on
// disk and Resolve names the failure and the manual re-arm.
func TestResolve_ReArmFailureReportsResolvedButNotReArmed(t *testing.T) {
	e := newResolveEnv(t)
	e.startErr = errors.New("spawn failed")
	writeStateFile(t, e.path, resolveState(0))

	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, nil)
	_ = done()

	const want = "resolved discussion:11 for PR #42 but not re-armed: start supervisor: spawn failed; re-arm with cenci babysit 42 --agent codex"
	if err == nil || err.Error() != want {
		t.Fatalf("Resolve err = %v, want exactly %q", err, want)
	}
	if s := mustReadState(t, e.path); !reflect.DeepEqual(s.AddressedKeys, []string{"comment:1", "discussion:11"}) {
		t.Errorf("AddressedKeys = %v, want the resolution kept on disk", s.AddressedKeys)
	}
}

// Edge case: a pending discussion comment that was deleted holds with
// `review feedback state unknown`, and `resolve` is the documented recovery that
// clears the hold; the next tick is quiet for it and settles the launch.
func TestResolve_ClearsTheDeletedPendingCommentHold(t *testing.T) {
	e := newResolveEnv(t)
	resetGh, resetCommand := execGh, command
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withCommands(t, automergeOnResponses(), &calls)
	serveDiscussion(t) // comment 11 was deleted on GitHub
	s := outstandingReviewState()
	s.SchemaVersion = stateSchemaVersion
	tickOK(t, &s)
	if s.AutomergeReason != reasonReviewStateUnknown || !reflect.DeepEqual(s.PendingKeys, []string{"discussion:11"}) {
		t.Fatalf("before resolve: reason %q pending %v, want the %q hold on discussion:11", s.AutomergeReason, s.PendingKeys, reasonReviewStateUnknown)
	}
	writeStateFile(t, e.path, s)
	execGh, command = resetGh, resetCommand

	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var next [][]string
	withCommands(t, automergeOnResponses(), &next)
	serveDiscussion(t)
	after := mustReadState(t, e.path)
	tickOK(t, &after)
	if after.AutomergeReason == reasonReviewStateUnknown || len(after.PendingKeys) != 0 || !reflect.DeepEqual(after.AddressedKeys, []string{"discussion:11"}) {
		t.Errorf("after resolve: reason %q pending %v addressed %v, want the unknown hold cleared and the key addressed", after.AutomergeReason, after.PendingKeys, after.AddressedKeys)
	}
	if !hasCondition(after.AutomergeConditions, "review", true) {
		t.Errorf("review-stage conditions = %v, want the review stage to pass", after.AutomergeConditions)
	}
	if after.ReviewLaunch != nil {
		t.Errorf("ReviewLaunch = %+v, want the settled launch cleared by the tick", after.ReviewLaunch)
	}
	if countWorkflowLaunches(next, "babysit-review") != 0 {
		t.Error("the next tick relaunched babysit-review for the resolved key")
	}
}

// A resolved key whose comment is edited afterwards reopens: the human resolved
// the revision the supervisor last saw, not the new body.
func TestResolve_AnEditAfterwardsReopensTheKey(t *testing.T) {
	e := newResolveEnv(t)
	s := outstandingReviewState()
	s.SchemaVersion = stateSchemaVersion
	writeStateFile(t, e.path, s)
	done := captureStdout(t)
	err := Resolve("42", "discussion:11", e.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var calls [][]string
	withCommands(t, openPR2(), &calls)
	serveDiscussion(t, humanComment(11, humanBody+" (edited)"))
	after := mustReadState(t, e.path)
	tickOK(t, &after)

	if !reflect.DeepEqual(after.PendingKeys, []string{"discussion:11"}) || len(after.AddressedKeys) != 0 {
		t.Errorf("pending %v addressed %v, want the edited comment pending again", after.PendingKeys, after.AddressedKeys)
	}
}

// -- Reset on a stopped state --------------------------------------------------

// A stopped state records no agent and no budget, so a reset has nothing to zero
// and no settings to re-arm with: it refuses with the re-arm hint, changing and
// starting nothing.
func TestReset_RefusesAStoppedStateWithAReArmHint(t *testing.T) {
	e := newResetEnv(t)
	writeStateFile(t, e.path, stoppedState(resolveState(0), "o/r", "42"))
	before := readFileBytes(t, e.path)

	err := Reset("42", e.dir, nil)

	if err == nil || !strings.Contains(err.Error(), "PR #42 is stopped") || !strings.Contains(err.Error(), "cenci babysit 42 --agent") {
		t.Fatalf("Reset err = %v, want a stopped refusal with the re-arm hint", err)
	}
	if !bytes.Equal(readFileBytes(t, e.path), before) {
		t.Error("state file changed")
	}
	e.requireNoStart(t)
}
