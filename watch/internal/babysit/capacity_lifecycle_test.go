package babysit

// #1469: every babysit branch-writer launch (ci-repair, merge-repair,
// babysit-review) goes through the shared admission ledger. These tests drive the
// real tick and Run loops over a real ledger in a temp directory, with the
// daemon snapshot, orphan count, tmux inventory and limits seams answered by a
// harness (watch/docs/test-isolation.md), and a fake `cenci run` that, like the
// real one, leaves a window behind. Waiting is observed on the ledger (its wait
// records and entries) and on the persisted state.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

const (
	capRepo       = "o/r"
	capRepoDir    = "/repo/root"
	capOtherRepo  = "o/other"
	capOtherDir   = "/other/dir"
	capWaitReason = capacity.ReasonMachineCap
	// capDefaultSocket is what tmux.ResolveSocket yields in the harness: the first
	// field of the $TMUX it installs, the socket `cenci run` falls back to when the
	// supervisor passes no --tmux-socket.
	capDefaultSocket = "/run/user/1000/tmux-1000/default"
	// capDeadLauncher is a pid the harness probe reports as gone.
	capDeadLauncher = 4_000_000
	// capForeignLauncher is a pid the harness probe reports as a live process that
	// is not this supervisor: another launcher whose entry no tick here may settle.
	capForeignLauncher = 4_000_001
)

var branchWriters = []string{"ci-repair", "merge-repair", "babysit-review"}

// grantPermit hands a direct launch() caller a real permit from the package's
// ledger seam. babysit-attention is not charged and gets none. Every permit is
// for its own ticket, so a leftover entry of an earlier test never refuses it.
func grantPermit(t *testing.T, workflow string) *capacity.Permit {
	t.Helper()
	if workflow == "babysit-attention" {
		return nil
	}
	grantSeq++
	d, err := capacityLedgerFn().Admit(capacity.Request{
		Kind: capacity.Kind(workflow), Repo: capRepo, Ticket: grantSeq,
		Window: fmt.Sprintf("%d-%s", grantSeq, workflow), Limits: capacity.Limits{Machine: 99},
	})
	if err != nil || !d.Admitted {
		t.Fatalf("grantPermit(%s): decision %+v, err %v", workflow, d, err)
	}
	return &d.Permit
}

var grantSeq = 9000

// capHarness is a host the admission seams read: a real ledger over a temp
// directory with an injected clock and launcher probe, and the observations a
// supervisor makes before it launches.
type capHarness struct {
	t       *testing.T
	ledger  *capacity.Ledger
	handle  ledgerHandle
	dir     string
	cfgPath string
	clock   time.Time

	sessions   []capacity.Session
	snapErr    error
	orphans    int
	orphanErr  error
	windows    map[string][]tmux.CleanupWindow
	invErr     map[string]error
	invDropped map[string][]string
	nextID     int
}

// newCapHarness installs the seams for a host whose machine cap is machineCap.
func newCapHarness(t *testing.T, machineCap int) *capHarness {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "cenci")
	dir := filepath.Join(parent, "capacity")
	for _, p := range []string{parent, dir} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	h := &capHarness{
		t: t, dir: dir, clock: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		windows: map[string][]tmux.CleanupWindow{}, invErr: map[string]error{}, invDropped: map[string][]string{},
		cfgPath: filepath.Join(t.TempDir(), "config.json"),
	}
	t.Setenv("TMUX", capDefaultSocket+",4242,0")
	h.ledger = capacity.NewLedger(dir, capacity.Options{Now: h.now, Probe: h.probe, LockTimeout: 200 * time.Millisecond})
	h.handle = h.ledger
	h.setConfig(fmt.Sprintf(`{"dispatch":{"concurrencyCap":%d}}`, machineCap))

	originals := []any{capacityLedgerFn, daemonSnapshotFn, orphanCountFn, cleanupInventoryFn, limitsPath, capacityClock, settleRetryPause}
	settleRetryPause = 0
	capacityLedgerFn = func() ledgerHandle { return h.handle }
	daemonSnapshotFn = func() ([]capacity.Session, error) { return slices.Clone(h.sessions), h.snapErr }
	orphanCountFn = func() (int, error) { return h.orphans, h.orphanErr }
	cleanupInventoryFn = h.cleanupInventory
	limitsPath = func() string { return h.cfgPath }
	capacityClock = h.now
	t.Cleanup(func() {
		capacityLedgerFn = originals[0].(func() ledgerHandle)
		daemonSnapshotFn = originals[1].(func() ([]capacity.Session, error))
		orphanCountFn = originals[2].(func() (int, error))
		cleanupInventoryFn = originals[3].(func(string) (tmux.CleanupInventory, error))
		limitsPath = originals[4].(func() string)
		capacityClock = originals[5].(func() time.Time)
		settleRetryPause = originals[6].(time.Duration)
	})
	return h
}

// now is the harness clock: every read advances it a millisecond, so an
// inventory read after an admission is always later than it.
func (h *capHarness) now() time.Time {
	h.clock = h.clock.Add(time.Millisecond)
	return h.clock
}

func (h *capHarness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

// probe knows this process (the launcher of every admission a supervisor
// makes) and capForeignLauncher (a live launcher that is someone else); every
// other pid, capDeadLauncher included, is gone.
func (h *capHarness) probe(pid int) (string, bool) {
	switch pid {
	case os.Getpid():
		return "1000", true
	case capForeignLauncher:
		return "2000", true
	}
	return "", false
}

func (h *capHarness) setConfig(body string) {
	h.t.Helper()
	if err := os.WriteFile(h.cfgPath, []byte(body), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *capHarness) cleanupInventory(socket string) (tmux.CleanupInventory, error) {
	if err := h.invErr[socket]; err != nil {
		return tmux.CleanupInventory{}, err
	}
	return tmux.CleanupInventory{Windows: slices.Clone(h.windows[socket]), Dropped: h.invDropped[socket]}, nil
}

func (h *capHarness) addWindow(socket, name, dir string, dead bool) {
	h.nextID++
	h.windows[socket] = append(h.windows[socket], tmux.CleanupWindow{
		WindowID: "@" + strconv.Itoa(h.nextID), Name: name, RepoDir: dir,
		Panes: []tmux.CleanupPane{{ID: "%" + strconv.Itoa(h.nextID), Dead: dead}},
	})
}

func (h *capHarness) removeWindow(socket, name string) {
	h.windows[socket] = slices.DeleteFunc(h.windows[socket], func(w tmux.CleanupWindow) bool { return w.Name == name })
}

// seedImplement admits and confirms n live implement workers of another repo.
func (h *capHarness) seedImplement(n int) {
	h.t.Helper()
	for i := range n {
		ticket := 100 + i
		window := fmt.Sprintf("%d-implement", ticket)
		d, err := h.ledger.Admit(capacity.Request{
			Kind: capacity.KindImplement, Repo: capOtherRepo, Ticket: ticket, Socket: "", Window: window, Dir: capOtherDir,
			Limits: capacity.Limits{Machine: 99},
		})
		if err != nil || !d.Admitted {
			h.t.Fatalf("seed implement #%d: decision %+v, err %v", ticket, d, err)
		}
		if err := h.ledger.Confirm(d.Permit, capacity.Confirmation{Socket: "", Window: window, Dir: capOtherDir}); err != nil {
			h.t.Fatal(err)
		}
		h.addWindow("", window, capOtherDir, false)
	}
}

// admitAs reserves a slot for a worker whose launcher is pid (use capDeadLauncher
// for a supervisor that was killed), without confirming it.
func (h *capHarness) admitAs(pid int, req capacity.Request) capacity.Permit {
	h.t.Helper()
	req.Launcher = pid
	if req.Limits.Machine == 0 {
		req.Limits = capacity.Limits{Machine: 99}
	}
	d, err := h.ledger.Admit(req)
	if err != nil || !d.Admitted {
		h.t.Fatalf("admit %+v: decision %+v, err %v", req, d, err)
	}
	return d.Permit
}

// permit reserves a slot for s's repair of workflow, the way admitRepair does.
func (h *capHarness) permit(s *State, workflow string) *capacity.Permit {
	h.t.Helper()
	ticket, _ := strconv.Atoi(s.PR)
	p := h.admitAs(os.Getpid(), capacity.Request{
		Kind: capacity.Kind(workflow), Repo: s.Repo, Ticket: ticket, Branch: "feature",
		Socket: s.LaunchSocket, Window: s.PR + "-" + workflow, Dir: s.LaunchDir,
	})
	return &p
}

func (h *capHarness) entries() []capacity.Entry {
	h.t.Helper()
	snap, err := h.ledger.Peek()
	if err != nil {
		h.t.Fatalf("Peek: %v", err)
	}
	return snap.Entries
}

func (h *capHarness) waits() []capacity.Wait {
	h.t.Helper()
	snap, err := h.ledger.Peek()
	if err != nil {
		h.t.Fatalf("Peek: %v", err)
	}
	return snap.Waits
}

// trackRuns makes every successful `cenci run` of the current command seam leave
// its window behind the way the real one does (internal/run resolveSocket and
// launchRepoDir): on the socket it was given or else the resolved default one, in
// the directory it was given made absolute or else the working directory.
func (h *capHarness) trackRuns() {
	previous := command
	command = func(name string, args ...string) ([]byte, error) {
		out, err := previous(name, args...)
		if err == nil && len(args) > 2 && args[0] == "run" {
			socket, dir := "", ""
			for i, a := range args {
				switch {
				case a == "--tmux-socket" && i+1 < len(args):
					socket = args[i+1]
				case a == "--dir" && i+1 < len(args):
					dir = args[i+1]
				}
			}
			if socket == "" {
				socket = capDefaultSocket
			}
			if dir == "" {
				dir, _ = os.Getwd()
			} else {
				dir, _ = filepath.Abs(dir)
			}
			if fields := strings.Fields(args[2]); len(fields) > 0 {
				h.addWindow(socket, fields[0]+"-"+args[1], dir, false)
			}
		}
		return out, err
	}
	h.t.Cleanup(func() { command = previous })
}

// tick runs one tick of s against the scripted gh responses and returns the
// recorded calls and what the tick printed. A failing tick fails the test.
func (h *capHarness) tick(s *State, responses []string) (calls [][]string, stdout string) {
	h.t.Helper()
	calls, stdout, err := h.tickErr(s, responses)
	if err != nil {
		h.t.Fatalf("tick: %v", err)
	}
	return calls, stdout
}

func (h *capHarness) tickErr(s *State, responses []string) (calls [][]string, stdout string, err error) {
	h.t.Helper()
	withCommands(h.t, responses, &calls)
	h.trackRuns()
	restore := captureStdout(h.t)
	_, _, err = tick(s)
	return calls, restore(), err
}

// repairCase is one tick that wants a repair of one kind.
type repairCase struct {
	name, workflow string
	kind           capacity.Kind
	state          State
	responses      []string
}

func repairCases() []repairCase {
	base := func() State {
		return State{PR: "42", Repo: capRepo, Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 900, LaunchSession: "work", LaunchDir: capRepoDir}
	}
	merge := base()
	merge.LastHeadSHA = "abc"
	return []repairCase{
		{"ci-repair", "ci-repair", capacity.KindCIRepair, ciRepairSeedState(), ciRepairTickResponses("sha-new")},
		{"merge-repair", "merge-repair", capacity.KindMergeRepair, merge, []string{conflictingOpenPR("abc"), ciCapGreen, `[]`, `[]`}},
		{"babysit-review", "babysit-review", capacity.KindBabysitReview, base(),
			[]string{openPR(), `[]`, `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`, `[]`, unresolvedThreadFor(7)}},
	}
}

func repairCaseByName(t *testing.T, name string) repairCase {
	t.Helper()
	for _, c := range repairCases() {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no repair case %q", name)
	return repairCase{}
}

// quietState is a supervisor with no repair history; quietResponses is one green
// tick of it, with nothing to repair.
func quietState() State {
	return State{PR: "42", Repo: capRepo, Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work", LaunchDir: capRepoDir}
}

func quietResponses() []string { return []string{openPR(), ciCapGreen, `[]`, `[]`} }

// ---- AC74: a full host runs no `cenci run` and records a capacity wait ------

func TestRepairAdmission_FullHostRunsNoCenciRunAndRecordsACapacityWait(t *testing.T) {
	for _, c := range repairCases() {
		t.Run(c.name, func(t *testing.T) {
			h := newCapHarness(t, 1)
			h.seedImplement(1)
			s := c.state

			calls, out := h.tick(&s, c.responses)

			if n := countWorkflowLaunches(calls, c.workflow); n != 0 {
				t.Fatalf("%s launches = %d, want 0 on a full host: %#v", c.workflow, n, calls)
			}
			if !strings.Contains(out, "concurrency cap reached") {
				t.Errorf("stdout = %q, want it to say concurrency cap reached", out)
			}
			if s.CapacityWait == nil || s.CapacityWait.Reason != capWaitReason {
				t.Fatalf("CapacityWait = %+v, want a wait with reason %q", s.CapacityWait, capWaitReason)
			}
			if s.CapacityWait.Since.IsZero() || s.CapacityWait.RefreshedAt.IsZero() {
				t.Errorf("CapacityWait = %+v, want since and refreshedAt set", s.CapacityWait)
			}
			waits := h.waits()
			if len(waits) != 1 || waits[0].Ticket != 42 || waits[0].Repo != capRepo || waits[0].Kind != c.kind || waits[0].Reason != capWaitReason {
				t.Errorf("ledger waits = %+v, want one %s wait of %s#42 for %q", waits, c.kind, capRepo, capWaitReason)
			}
			if got := h.entries(); len(got) != 1 || got[0].Kind != capacity.KindImplement {
				t.Errorf("ledger entries = %+v, want only the implement entry: a waiting repair holds no slot", got)
			}
		})
	}
}

// ---- AC76: a wait spends nothing ---------------------------------------------

func TestRepairAdmission_ACapacityWaitSpendsNoAttemptAndKeepsFeedbackPending(t *testing.T) {
	for _, c := range repairCases() {
		t.Run(c.name, func(t *testing.T) {
			h := newCapHarness(t, 1)
			h.seedImplement(1)
			s := c.state
			before := s

			h.tick(&s, c.responses)

			assertCIRepairFieldsUnchanged(t, s, before)
			if s.ConflictFixAttempts != before.ConflictFixAttempts || s.ConflictRepairSHA != before.ConflictRepairSHA || s.ConflictNotifiedSHA != before.ConflictNotifiedSHA {
				t.Errorf("conflict bookkeeping = {%d %q %q}, want unchanged {%d %q %q}", s.ConflictFixAttempts, s.ConflictRepairSHA, s.ConflictNotifiedSHA,
					before.ConflictFixAttempts, before.ConflictRepairSHA, before.ConflictNotifiedSHA)
			}
			if s.CIRepairTracked != before.CIRepairTracked {
				t.Errorf("CIRepairTracked = %v, want unchanged %v", s.CIRepairTracked, before.CIRepairTracked)
			}
			if s.ReviewLaunch != nil || len(s.LaunchedKeys) != 0 || len(s.ConsumedReviewNonces) != 0 {
				t.Errorf("review launch %+v launched %v consumed %v, want none: a wait launches and records nothing", s.ReviewLaunch, s.LaunchedKeys, s.ConsumedReviewNonces)
			}
			if c.workflow == "babysit-review" {
				if !slices.Equal(s.PendingKeys, []string{"comment:7"}) {
					t.Errorf("pendingCommentKeys = %v, want [comment:7] kept pending", s.PendingKeys)
				}
			}
			if s.CurrentDelaySeconds != s.IntervalSeconds {
				t.Errorf("CurrentDelaySeconds = %d, want the base interval %d: a wait keeps the tick actionable", s.CurrentDelaySeconds, s.IntervalSeconds)
			}
		})
	}
}

// ---- AC75: two supervisors wait, exactly one repair launches ----------------

func TestRepairAdmission_TwoSupervisorsWaitThenExactlyOneRepairLaunchesWhenASlotFrees(t *testing.T) {
	h := newCapHarness(t, 1)
	h.seedImplement(1)
	first, second := ciRepairSeedState(), ciRepairSeedState()
	second.PR = "43"
	responses := ciRepairTickResponses("sha-new")

	for _, s := range []*State{&first, &second} {
		calls, _ := h.tick(s, responses)
		if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
			t.Fatalf("PR %s launched %d ci-repair while the implement worker held the only slot", s.PR, n)
		}
	}
	if got := len(h.waits()); got != 2 {
		t.Fatalf("ledger waits = %d, want both supervisors waiting", got)
	}

	// The implementation's fake worker exits; the next inventory proves it.
	h.removeWindow("", "100-implement")
	calls, _ := h.tick(&first, responses)
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("first supervisor launched %d ci-repair after the slot freed, want 1: %#v", n, calls)
	}
	calls, _ = h.tick(&second, responses)
	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("second supervisor launched %d ci-repair, want 0: the slot is taken", n)
	}

	entries := h.entries()
	if len(entries) != 1 || entries[0].Ticket != 42 || entries[0].Kind != capacity.KindCIRepair || entries[0].State != capacity.StateConfirmed {
		t.Errorf("ledger entries = %+v, want only PR 42's confirmed ci-repair", entries)
	}
	waits := h.waits()
	if len(waits) != 1 || waits[0].Ticket != 43 {
		t.Errorf("ledger waits = %+v, want only PR 43 still waiting", waits)
	}
	if second.CapacityWait == nil {
		t.Error("the second supervisor's state records no capacity wait")
	}
	if first.CapacityWait != nil {
		t.Errorf("the launched supervisor's state still records %+v, want the wait cleared", first.CapacityWait)
	}
}

// ---- AC77: launch() refuses a branch writer without a reservation -----------

func TestLaunch_RefusesABranchWriterWithoutAMatchingPermit(t *testing.T) {
	wrongKinds := map[string]capacity.Kind{"ci-repair": capacity.KindMergeRepair, "merge-repair": capacity.KindBabysitReview, "babysit-review": capacity.KindCIRepair}
	for _, wf := range branchWriters {
		for _, tc := range []struct {
			name   string
			permit *capacity.Permit
		}{
			{"nil permit", nil},
			{"an implement permit", &capacity.Permit{Kind: capacity.KindImplement, HolderID: "x"}},
			{"another writer's permit", &capacity.Permit{Kind: wrongKinds[wf], HolderID: "x"}},
		} {
			t.Run(wf+"/"+tc.name, func(t *testing.T) {
				var calls [][]string
				withCommands(t, nil, &calls)
				probes := 0
				tmuxHasSession = func(string, string) (bool, error) { probes++; return true, nil }
				s := State{PR: "42", Repo: capRepo, Agent: "claude", LaunchSession: "work", LaunchDir: capRepoDir}

				err := launch(&s, wf, "42 x", tc.permit)

				if err == nil || !strings.Contains(err.Error(), "capacity reservation") {
					t.Fatalf("launch err = %v, want a refusal naming the missing capacity reservation", err)
				}
				if len(calls) != 0 || probes != 0 {
					t.Errorf("a refused launch ran %d commands and %d session probes, want none: %#v", len(calls), probes, calls)
				}
			})
		}
	}

	t.Run("babysit-attention is not charged", func(t *testing.T) {
		var calls [][]string
		withCommands(t, nil, &calls)
		s := State{PR: "42", Repo: capRepo, Agent: "claude", LaunchSession: "work", LaunchDir: capRepoDir}
		if err := launch(&s, "babysit-attention", "42 CI retry cap reached", nil); err != nil {
			t.Fatalf("launch: %v", err)
		}
		if n := countWorkflowLaunches(calls, "babysit-attention"); n != 1 {
			t.Errorf("babysit-attention launches = %d, want 1 with no permit", n)
		}
	})
}

// A full host does not hold back babysit-attention: it is a human attention
// prompt, not branch-writing work, so it is never charged, never waits and never
// asks the ledger.
func TestRepairAdmission_BabysitAttentionIsNotChargedOrAdmitted(t *testing.T) {
	h := newCapHarness(t, 1)
	h.seedImplement(1)
	rec := &recordingLedger{ledgerHandle: h.ledger}
	h.handle = rec
	s := State{PR: "42", Repo: capRepo, Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, LaunchSession: "work", LaunchDir: capRepoDir, FixAttempts: fixCap}

	calls, _ := h.tick(&s, []string{cleanOpenPR("sha-x"), ciCapFailing, `[]`, `[]`})

	if n := countWorkflowLaunches(calls, "babysit-attention"); n != 1 {
		t.Fatalf("babysit-attention launches = %d, want 1 on a full host: %#v", n, calls)
	}
	if len(rec.admits) != 0 {
		t.Errorf("Admit calls = %+v, want none for an uncharged launch", rec.admits)
	}
	if s.CapacityWait != nil || len(h.waits()) != 0 {
		t.Errorf("a capacity wait was recorded for babysit-attention: %+v %+v", s.CapacityWait, h.waits())
	}
}

// recordingLedger records every Admit and delegates to the real ledger.
type recordingLedger struct {
	ledgerHandle
	admits []capacity.Request
}

func (r *recordingLedger) Admit(req capacity.Request) (capacity.Decision, error) {
	r.admits = append(r.admits, req)
	return r.ledgerHandle.Admit(req)
}

// ---- outcome rule: confirm or release after launch() ------------------------

func TestLaunch_ConfirmsTheReservationToTheDeterministicWindowAfterAnyCenciRunExit(t *testing.T) {
	for _, socket := range []string{"", "/s/a"} {
		for _, code := range []int{0, 3, 1, 2} {
			for _, wf := range branchWriters {
				t.Run(fmt.Sprintf("%s/exit %d/socket %q", wf, code, socket), func(t *testing.T) {
					h := newCapHarness(t, 3)
					var calls [][]string
					withCommands(t, nil, &calls)
					if code != 0 {
						stubCenciRunExit(t, &calls, code, "cenci run: boom")
					}
					s := State{PR: "42", Repo: capRepo, Agent: "claude", LaunchSession: "work", LaunchSocket: socket, LaunchDir: capRepoDir}
					permit := h.permit(&s, wf)

					err := launch(&s, wf, "42 x", permit)

					if (err != nil) != (code != 0) {
						t.Fatalf("launch err = %v for exit %d", err, code)
					}
					entries := h.entries()
					if len(entries) != 1 {
						t.Fatalf("ledger entries = %+v, want the one reservation kept after a `cenci run` that exited %d", entries, code)
					}
					// An empty recorded socket is the one `cenci run` resolves.
					wantSocket := socket
					if wantSocket == "" {
						wantSocket = capDefaultSocket
					}
					e := entries[0]
					if e.State != capacity.StateConfirmed || e.Window != "42-"+wf || e.Socket != wantSocket || e.Dir != capRepoDir {
						t.Errorf("entry = %+v, want confirmed to window 42-%s on socket %q dir %s", e, wf, wantSocket, capRepoDir)
					}
				})
			}
		}
	}
}

func TestLaunch_ReleasesTheReservationOfALaunchThatFailedBeforeCenciRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(s *State)
	}{
		{"no recorded session", func(s *State) { s.LaunchSession = "" }},
		{"recorded session gone", func(*State) { tmuxHasSession = func(string, string) (bool, error) { return false, nil } }},
		{"session probe failed", func(*State) {
			tmuxHasSession = func(string, string) (bool, error) { return false, errors.New("tmux timed out") }
		}},
	} {
		for _, wf := range branchWriters {
			t.Run(tc.name+"/"+wf, func(t *testing.T) {
				h := newCapHarness(t, 3)
				var calls [][]string
				withCommands(t, nil, &calls)
				s := State{PR: "42", Repo: capRepo, Agent: "claude", LaunchSession: "work", LaunchDir: capRepoDir}
				permit := h.permit(&s, wf)
				tc.prep(&s)

				if err := launch(&s, wf, "42 x", permit); err == nil {
					t.Fatal("launch err = nil, want the failure")
				}

				if got := h.entries(); len(got) != 0 {
					t.Errorf("ledger entries = %+v, want none: a launch that failed before `cenci run` is released", got)
				}
				if n := countWorkflowLaunches(calls, wf); n != 0 {
					t.Errorf("cenci run calls = %d, want 0", n)
				}
			})
		}
	}
}

// An exit-3 spawn keeps its permit until a complete inventory proves no worker.
func TestRepairAdmission_AnExitThreeSpawnKeepsItsPermitUntilAnInventoryProvesNoWorker(t *testing.T) {
	h := newCapHarness(t, 1)
	var calls [][]string
	s := ciRepairSeedState()
	withCommands(t, ciRepairTickResponses("sha-new"), &calls)
	stubCenciRunExit(t, &calls, 3, "cenci run: setting automatic-rename off: boom: tmux window already spawned")
	restore := captureStdout(t)
	_, _, err := tick(&s)
	restore()
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if s.FixAttempts != 2 {
		t.Fatalf("ciFixAttempts = %d, want 2: exit 3 counts as dispatched", s.FixAttempts)
	}
	if got := h.entries(); len(got) != 1 || got[0].State != capacity.StateConfirmed || got[0].Window != "42-ci-repair" {
		t.Fatalf("ledger entries = %+v, want the permit kept, confirmed to 42-ci-repair", got)
	}

	// The window really exists: a later complete inventory keeps the entry.
	h.addWindow(capDefaultSocket, "42-ci-repair", capRepoDir, false)
	quiet := quietState()
	h.tick(&quiet, quietResponses())
	if got := h.entries(); len(got) != 1 {
		t.Fatalf("ledger entries = %+v after an inventory showing the window, want it kept", got)
	}

	// The window is gone: the next complete inventory proves no worker.
	h.removeWindow(capDefaultSocket, "42-ci-repair")
	h.tick(&quiet, quietResponses())
	if got := h.entries(); len(got) != 0 {
		t.Errorf("ledger entries = %+v after an inventory proving no worker, want none", got)
	}
}

// ---- AC78/AC79: releases and adoption ---------------------------------------

func TestRepairAdmission_AKilledSupervisorsPermitIsReleasedOnlyByAnInventoryProvingNoWorker(t *testing.T) {
	reserve := func(h *capHarness, launcher int) {
		h.admitAs(launcher, capacity.Request{Kind: capacity.KindCIRepair, Repo: capRepo, Ticket: 42, Branch: "feature", Socket: "", Window: "42-ci-repair", Dir: capRepoDir})
	}
	for _, tc := range []struct {
		name     string
		launcher int
		prep     func(h *capHarness)
		want     int
	}{
		{"dead launcher and a complete inventory with no window", capDeadLauncher, func(*capHarness) {}, 0},
		{"dead launcher but the window exists", capDeadLauncher, func(h *capHarness) { h.addWindow("", "42-ci-repair", capRepoDir, false) }, 1},
		{"dead launcher but the inventory failed", capDeadLauncher, func(h *capHarness) { h.invErr[""] = errors.New("tmux: timed out") }, 1},
		{"dead launcher but the inventory dropped a row", capDeadLauncher, func(h *capHarness) { h.invDropped[""] = []string{`window "@9": bad row`} }, 1},
		{"live launcher with no window is not proof", capForeignLauncher, func(*capHarness) {}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(t, 3)
			reserve(h, tc.launcher)
			tc.prep(h)
			s := quietState()

			_, out := h.tick(&s, quietResponses())

			if got := h.entries(); len(got) != tc.want {
				t.Errorf("ledger entries = %+v, want %d kept", got, tc.want)
			}
			if tc.want == 1 && tc.name == "dead launcher but the inventory failed" && !strings.Contains(out, "tmux: timed out") {
				t.Errorf("stdout = %q, want the inventory error reported", out)
			}
		})
	}
}

func TestRepairAdmission_ADeadWorkerPaneReleasesOnTheNextCompleteInventory(t *testing.T) {
	h := newCapHarness(t, 3)
	p := h.permit(&State{PR: "42", Repo: capRepo, LaunchDir: capRepoDir}, "ci-repair")
	if err := h.ledger.Confirm(*p, capacity.Confirmation{Socket: "", Window: "42-ci-repair", Dir: capRepoDir}); err != nil {
		t.Fatal(err)
	}
	h.addWindow("", "42-ci-repair", capRepoDir, true)
	s := quietState()

	h.tick(&s, quietResponses())

	if got := h.entries(); len(got) != 0 {
		t.Errorf("ledger entries = %+v, want the dead pane's entry released", got)
	}
}

func TestRepairAdmission_ARestartedSupervisorAdoptsItsOwnHeldEntry(t *testing.T) {
	h := newCapHarness(t, 1)
	// The previous supervisor reserved the slot and was killed before it spawned;
	// the inventory cannot be read, so the entry is neither proven gone nor freed.
	h.admitAs(capDeadLauncher, capacity.Request{Kind: capacity.KindCIRepair, Repo: capRepo, Ticket: 42, Branch: "feature", Socket: "", Window: "42-ci-repair", Dir: capRepoDir})
	h.invErr[""] = errors.New("tmux: timed out")
	holder := h.entries()[0].HolderID
	s := ciRepairSeedState()

	calls, _ := h.tick(&s, ciRepairTickResponses("sha-new"))

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("ci-repair launches = %d, want 1: the restarted supervisor adopts its entry even with the cap full", n)
	}
	entries := h.entries()
	if len(entries) != 1 || entries[0].HolderID != holder {
		t.Fatalf("ledger entries = %+v, want the original entry %s adopted, not a second one", entries, holder)
	}
	if entries[0].State != capacity.StateConfirmed || entries[0].LauncherPID != os.Getpid() {
		t.Errorf("adopted entry = %+v, want it confirmed and owned by this supervisor", entries[0])
	}
}

// ---- AC80 / edge cases 109, 110: the wait record's lifecycle ----------------

func TestRepairAdmission_AWaitIsRefreshedEachTickAndExpiresAfterThreePollIntervals(t *testing.T) {
	h := newCapHarness(t, 1)
	h.seedImplement(1)
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	h.tick(&s, c.responses)
	first := h.waits()
	if s.CapacityWait == nil {
		t.Fatal("the first tick recorded no capacity wait")
	}
	since := s.CapacityWait.Since
	h.advance(10 * time.Second)
	h.tick(&s, c.responses)
	second := h.waits()
	if len(second) == 0 || s.CapacityWait == nil {
		t.Fatalf("waits = %+v state wait = %+v after the second tick, want a refreshed wait", second, s.CapacityWait)
	}

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("waits = %d then %d, want one record refreshed in place", len(first), len(second))
	}
	if !second[0].RefreshedAt.After(first[0].RefreshedAt) || !second[0].Since.Equal(first[0].Since) {
		t.Errorf("wait %+v then %+v, want refreshedAt advanced and since kept", first[0], second[0])
	}
	if !s.CapacityWait.Since.Equal(since) || !s.CapacityWait.RefreshedAt.After(since) {
		t.Errorf("state wait = %+v, want since %v kept and refreshedAt advanced", s.CapacityWait, since)
	}
	if got, want := second[0].ExpiresAt.Sub(second[0].RefreshedAt), 3*time.Duration(s.IntervalSeconds)*time.Second; got != want {
		t.Errorf("wait lives %v after a refresh, want 3 poll intervals = %v", got, want)
	}

	// Nobody refreshes it for 3 intervals: it no longer counts.
	h.advance(3*time.Minute + time.Second)
	snap, err := h.ledger.Peek()
	if err != nil {
		t.Fatal(err)
	}
	if n := snap.LiveWaits(h.now()); n != 0 {
		t.Errorf("live waits = %d after 3 unrefreshed poll intervals, want 0", n)
	}
}

func TestRepairAdmission_TheWaitIsDroppedWhenTheTickNoLongerNeedsARepair(t *testing.T) {
	h := newCapHarness(t, 1)
	h.seedImplement(1)
	c := repairCaseByName(t, "ci-repair")
	s := c.state
	h.tick(&s, c.responses)
	if len(h.waits()) != 1 || s.CapacityWait == nil {
		t.Fatalf("setup: waits %+v state %+v, want a wait", h.waits(), s.CapacityWait)
	}

	// CI turned green: the repair is not needed any more.
	h.tick(&s, []string{cleanOpenPR("sha-new"), ciCapGreen, `[]`, `[]`})

	if got := h.waits(); len(got) != 0 {
		t.Errorf("ledger waits = %+v, want the wait dropped", got)
	}
	if s.CapacityWait != nil {
		t.Errorf("CapacityWait = %+v, want cleared", s.CapacityWait)
	}
}

// Edge case 109: a PR that merges or closes while its repair is waiting drops
// the wait record and launches nothing.
func TestRepairAdmission_AMergeOrCloseWhileWaitingDropsTheWaitAndLaunchesNothing(t *testing.T) {
	for _, terminal := range []struct {
		name string
		step ciCapStep
	}{{"merged", ciCapMerged()}, {"closed", ciCapClosed()}} {
		t.Run(terminal.name, func(t *testing.T) {
			h := newCapHarness(t, 1)
			h.seedImplement(1)
			e := newCICapEnv(t, failingAt("head-a"), failingAt("head-a"), terminal.step)
			h.trackRuns()
			e.seed(State{Status: "running"})

			if err := e.run(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			for _, tick := range []int{1, 2} {
				if e.after(tick).CapacityWait == nil {
					t.Errorf("tick %d: state records no capacity wait", tick)
				}
			}
			if got := h.waits(); len(got) != 0 {
				t.Errorf("ledger waits = %+v after the PR %s, want the wait dropped", got, terminal.name)
			}
			if got := runOrder(e.calls); len(got) != 0 {
				t.Errorf("cenci run calls = %q, want none", got)
			}
			e.requireStateRemoved()
			if got := h.entries(); len(got) != 1 {
				t.Errorf("ledger entries = %+v, want the implement entry untouched", got)
			}
		})
	}
}

// Edge case 110: `cenci babysit stop <pr>` while the repair is waiting removes
// that PR's wait record, and only that one.
func TestRepairAdmission_StopWhileWaitingRemovesThatPRsWaitRecord(t *testing.T) {
	for _, stop := range []string{"StopRepo", "Stop"} {
		t.Run(stop, func(t *testing.T) {
			h := newCapHarness(t, 1)
			dir := t.TempDir()
			now := time.Now().UTC()
			st := State{
				SchemaVersion: stateSchemaVersion, PR: "42", Repo: capRepo, Agent: "claude", Status: "running", IntervalSeconds: 60,
				CapacityWait: &capacityWait{Reason: capWaitReason, Since: now, RefreshedAt: now},
			}
			if err := save(statePath(dir, capRepo, "42"), st); err != nil {
				t.Fatal(err)
			}
			for _, pr := range []int{42, 43} {
				if err := h.ledger.Wait(capacity.WaitRequest{Kind: capacity.KindCIRepair, Repo: capRepo, Ticket: pr, Reason: capWaitReason, Interval: time.Minute}); err != nil {
					t.Fatal(err)
				}
			}

			var err error
			if stop == "StopRepo" {
				err = StopRepo(capRepo, "42", dir)
			} else {
				original := execGh
				execGh = func(...string) (string, string, error) { return capRepo + "\n", "", nil }
				t.Cleanup(func() { execGh = original })
				t.Setenv("CENCI_SANDBOX", "")
				restore := captureStdout(t)
				err = Stop("42", dir)
				restore()
			}
			if err != nil {
				t.Fatalf("%s: %v", stop, err)
			}

			waits := h.waits()
			if len(waits) != 1 || waits[0].Ticket != 43 {
				t.Errorf("ledger waits = %+v, want only PR 43's wait left", waits)
			}
			if stopped := load(statePath(dir, capRepo, "42")); stopped.Status != "stopped" || stopped.CapacityWait != nil {
				t.Errorf("stopped state = status %q wait %+v, want stopped with no wait", stopped.Status, stopped.CapacityWait)
			}
		})
	}
}

// ---- AC81: one critical section ---------------------------------------------

func TestRepairAdmission_TheWriterCheckAndTheCapacityCheckAreOneAdmit(t *testing.T) {
	h := newCapHarness(t, 2)
	rec := &recordingLedger{ledgerHandle: h.ledger}
	h.handle = rec
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, _ := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
		t.Fatalf("ci-repair launches = %d, want 1", n)
	}
	if len(rec.admits) != 1 {
		t.Fatalf("Admit calls = %d, want exactly one critical section: %+v", len(rec.admits), rec.admits)
	}
	got := rec.admits[0]
	if got.Kind != capacity.KindCIRepair || got.Repo != capRepo || got.Ticket != 42 || got.Branch != "feature" ||
		got.Window != "42-ci-repair" || got.Dir != capRepoDir || got.Socket != capDefaultSocket || !got.Adopt {
		t.Errorf("Admit request = %+v, want ci-repair o/r#42 on branch feature, window 42-ci-repair, dir %s, the resolved socket %s, adopting", got, capRepoDir, capDefaultSocket)
	}
	if got.Limits.Machine != 2 {
		t.Errorf("Admit limits = %+v, want the machine cap 2 read from the config", got.Limits)
	}
	if si, ok := got.Obs.Inventory[""]; !ok || !si.Complete || si.ReadAt.IsZero() {
		t.Errorf("Admit inventory = %+v, want a complete read of the recorded socket carrying its ReadAt", got.Obs.Inventory)
	}
}

func TestAdmitRepair_ALiveWorkerOfThisPRIsRefusedInsideTheAdmission(t *testing.T) {
	h := newCapHarness(t, 3)
	rec := &recordingLedger{ledgerHandle: h.ledger}
	h.handle = rec
	s := ciRepairSeedState()

	permit, reason := admitRepair(&s, prView{Number: 42, HeadRefName: "feature"}, capacity.KindCIRepair, workerLiveness{mergeLive: true})

	if permit != nil || reason != capacity.ReasonBranchWriter {
		t.Errorf("admitRepair = (%+v, %q), want a refusal %q: the PR's writer is live", permit, reason, capacity.ReasonBranchWriter)
	}
	if len(h.entries()) != 0 {
		t.Errorf("ledger entries = %+v, want none", h.entries())
	}
}

func TestRepairAdmission_ALiveDispatchWriterOnTheBranchDefersTheRepair(t *testing.T) {
	h := newCapHarness(t, 3)
	d, err := h.ledger.Admit(capacity.Request{
		Kind: capacity.KindImplement, Repo: capRepo, Ticket: 7, Branch: "feature", Socket: "", Window: "7-implement", Dir: capRepoDir,
		Limits: capacity.Limits{Machine: 99},
	})
	if err != nil || !d.Admitted {
		t.Fatalf("seed: %+v %v", d, err)
	}
	if err := h.ledger.Confirm(d.Permit, capacity.Confirmation{Socket: "", Window: "7-implement", Dir: capRepoDir}); err != nil {
		t.Fatal(err)
	}
	h.addWindow("", "7-implement", capRepoDir, false)
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, out := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 beside a live dispatch writer on the branch", n)
	}
	if !strings.Contains(out, capacity.ReasonBranchWriter) || s.CapacityWait == nil || s.CapacityWait.Reason != capacity.ReasonBranchWriter {
		t.Errorf("stdout %q wait %+v, want the reason %q", out, s.CapacityWait, capacity.ReasonBranchWriter)
	}
	assertCIRepairFieldsUnchanged(t, s, c.state)
}

func TestRepairAdmission_ASiblingRepairOfThisPRHeldInTheLedgerRefusesAsAlreadyReserved(t *testing.T) {
	h := newCapHarness(t, 3)
	p := h.permit(&State{PR: "42", Repo: capRepo, LaunchDir: capRepoDir}, "merge-repair")
	if err := h.ledger.Confirm(*p, capacity.Confirmation{Socket: "", Window: "42-merge-repair", Dir: capRepoDir}); err != nil {
		t.Fatal(err)
	}
	h.addWindow("", "42-merge-repair", capRepoDir, false)
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, out := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 while the ledger holds this PR's merge-repair", n)
	}
	if !strings.Contains(out, capacity.ReasonWorkerReserved) {
		t.Errorf("stdout = %q, want the reason %q", out, capacity.ReasonWorkerReserved)
	}
}

// ---- caps, the machine pause and repos without a cap ------------------------

func TestRepairAdmission_ConfiguredLimits(t *testing.T) {
	const repoCapped = `{"dispatch":{"concurrencyCap":3,"repos":[{"repo":"o/r","concurrencyCap":1}]}}`
	c := repairCaseByName(t, "ci-repair")
	for _, tc := range []struct {
		name       string
		config     string
		state      func(State) State
		seed       func(h *capHarness)
		wantLaunch bool
		wantReason string
	}{
		{"machine cap 0 pauses babysit repairs", `{"dispatch":{"concurrencyCap":0}}`, nil, func(*capHarness) {}, false, capacity.ReasonMachineCap},
		{"the repo cap refuses a repo at its cap", repoCapped, nil, func(h *capHarness) {
			h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindImplement, Repo: capRepo, Ticket: 9, Socket: "", Window: "9-implement", Dir: capRepoDir})
		}, false, capacity.ReasonRepoCap},
		{"a repo absent from dispatch.repos is bound by the machine cap only", repoCapped, func(s State) State { s.Repo = "o/free"; return s }, func(h *capHarness) {
			h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindImplement, Repo: capRepo, Ticket: 9, Socket: "", Window: "9-implement", Dir: capRepoDir})
		}, true, ""},
		{"the default cap of 3 applies without a config file", "", nil, func(h *capHarness) {
			for i := range 3 {
				h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindImplement, Repo: capOtherRepo, Ticket: 50 + i, Socket: "", Window: fmt.Sprintf("%d-implement", 50+i), Dir: capOtherDir})
			}
		}, false, capacity.ReasonMachineCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(t, 3)
			if tc.config == "" {
				h.cfgPath = filepath.Join(t.TempDir(), "absent.json")
			} else {
				h.setConfig(tc.config)
			}
			tc.seed(h)
			// Every seeded worker is alive in tmux.
			for _, e := range h.entries() {
				h.addWindow("", e.Window, e.Dir, false)
			}
			s := c.state
			if tc.state != nil {
				s = tc.state(s)
			}

			calls, _ := h.tick(&s, c.responses)

			if got := countWorkflowLaunches(calls, "ci-repair") == 1; got != tc.wantLaunch {
				t.Fatalf("ci-repair launched = %v, want %v (wait %+v)", got, tc.wantLaunch, s.CapacityWait)
			}
			if !tc.wantLaunch && (s.CapacityWait == nil || s.CapacityWait.Reason != tc.wantReason) {
				t.Errorf("CapacityWait = %+v, want reason %q", s.CapacityWait, tc.wantReason)
			}
		})
	}
}

// ---- observations: manual workers, orphans, daemon snapshot -----------------

func TestRepairAdmission_ObservedWorkersCountAgainstTheCapLikeDispatch(t *testing.T) {
	c := repairCaseByName(t, "ci-repair")
	for _, tc := range []struct {
		name       string
		machine    int
		prep       func(h *capHarness)
		wantLaunch bool
	}{
		{"a running manual session fills the cap", 1, func(h *capHarness) {
			h.sessions = []capacity.Session{{Socket: "", PaneID: "%7", Window: "manual-fix", Running: true}}
		}, false},
		{"a manual session that is not running counts zero", 1, func(h *capHarness) {
			h.sessions = []capacity.Session{{Socket: "", PaneID: "%7", Window: "manual-fix", Running: false}}
		}, true},
		{"an orphaned container session fills the cap", 1, func(h *capHarness) { h.orphans = 1 }, false},
		{"a ledger-held worker that also runs in the daemon counts once", 2, func(h *capHarness) {
			h.seedImplement(1)
			h.sessions = []capacity.Session{{Socket: "", PaneID: "%1", Window: "100-implement", Running: true}}
		}, true},
		{"a running session with no entry beside a held worker fills cap 2", 2, func(h *capHarness) {
			h.seedImplement(1)
			h.sessions = []capacity.Session{{Socket: "", PaneID: "%1", Window: "100-implement", Running: true}, {Socket: "", PaneID: "%8", Window: "manual", Running: true}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(t, tc.machine)
			tc.prep(h)
			s := c.state

			calls, _ := h.tick(&s, c.responses)

			if got := countWorkflowLaunches(calls, "ci-repair") == 1; got != tc.wantLaunch {
				t.Errorf("ci-repair launched = %v, want %v (wait %+v)", got, tc.wantLaunch, s.CapacityWait)
			}
		})
	}
}

// ---- AC83: lowering the cap never terminates repair work --------------------

func TestRepairAdmission_LoweringTheCapKillsNothing(t *testing.T) {
	h := newCapHarness(t, 3)
	h.seedImplement(2)
	other := State{PR: "43", Repo: capRepo, LaunchDir: capRepoDir}
	p := h.permit(&other, "ci-repair")
	if err := h.ledger.Confirm(*p, capacity.Confirmation{Socket: "", Window: "43-ci-repair", Dir: capRepoDir}); err != nil {
		t.Fatal(err)
	}
	h.addWindow("", "43-ci-repair", capRepoDir, false)
	h.setConfig(`{"dispatch":{"concurrencyCap":1}}`)
	before := slices.Clone(h.entries())
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, _ := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 over the lowered cap", n)
	}
	for _, call := range calls {
		for _, word := range call {
			if slices.Contains([]string{"kill-window", "kill-session", "kill-pane", "kill-server", "close", "stop"}, word) {
				t.Errorf("recorded call %q looks like a kill: lowering the cap must never terminate work", call)
			}
		}
	}
	if got := h.entries(); !slices.EqualFunc(got, before, func(a, b capacity.Entry) bool { return a.HolderID == b.HolderID && a.State == b.State }) {
		t.Errorf("ledger entries = %+v, want all %d unchanged", got, len(before))
	}
}

// ---- edge cases 111 and 112, and the unreadable ledger: fail closed ---------

func TestRepairAdmission_FailsClosedWithTheExactWaitReason(t *testing.T) {
	c := repairCaseByName(t, "ci-repair")
	for _, tc := range []struct {
		name string
		prep func(h *capHarness)
		want func(h *capHarness) string // exact reason, or a prefix when wantPrefix
		// prefix means the reason is only checked as a prefix (it carries a parse error).
		prefix bool
	}{
		{"edge 111: the daemon snapshot is unavailable", func(h *capHarness) { h.snapErr = errors.New("dial: no daemon") },
			func(*capHarness) string { return "capacity observation unavailable: daemon snapshot" }, false},
		{"orphaned container sessions cannot be counted", func(h *capHarness) { h.orphanErr = errors.New("podman ps: boom") },
			func(*capHarness) string { return "capacity observation unavailable: orphaned container sessions" }, false},
		{"edge 112: config.json is invalid JSON", func(h *capHarness) { h.setConfig(`{"dispatch":`) },
			func(*capHarness) string { return "capacity limits unreadable: " }, true},
		{"edge 112: a repo cap is not an integer", func(h *capHarness) {
			h.setConfig(`{"dispatch":{"concurrencyCap":3,"repos":[{"repo":"o/r","concurrencyCap":"two"}]}}`)
		}, func(*capHarness) string { return "capacity limits unreadable: dispatch.repos[].concurrencyCap" }, true},
		{"edge 112: config.json cannot be read as a file", func(h *capHarness) {
			if err := os.Remove(h.cfgPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(h.cfgPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}, func(*capHarness) string { return "capacity limits unreadable: " }, true},
		{"the config path cannot be resolved", func(*capHarness) { limitsPath = func() string { return "" } },
			func(*capHarness) string { return "capacity limits unreadable: " }, true},
		{"a corrupt ledger", func(h *capHarness) {
			if err := os.WriteFile(h.ledger.Path(), []byte(`{"schema":1,`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, func(h *capHarness) string {
			return "capacity ledger unreadable: " + h.ledger.Path() + ": " + capacity.ErrCorrupt.Error()
		}, false},
		{"a ledger with an unknown schema", func(h *capHarness) {
			if err := os.WriteFile(h.ledger.Path(), []byte(`{"schema":9,"entries":[],"waits":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, func(h *capHarness) string {
			return "capacity ledger unreadable: " + h.ledger.Path() + ": " + capacity.ErrUnknownSchema.Error()
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(t, 3)
			tc.prep(h)
			s := c.state
			before := s

			calls, out := h.tick(&s, c.responses)

			if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
				t.Fatalf("ci-repair launches = %d, want 0: admission fails closed", n)
			}
			want := tc.want(h)
			if s.CapacityWait == nil {
				t.Fatalf("CapacityWait = nil, want a wait with reason %q", want)
			}
			got := s.CapacityWait.Reason
			if (tc.prefix && !strings.HasPrefix(got, want)) || (!tc.prefix && got != want) {
				t.Errorf("wait reason = %q, want %q (prefix=%v)", got, want, tc.prefix)
			}
			if !strings.Contains(out, got) {
				t.Errorf("stdout = %q, want it to report the reason %q", out, got)
			}
			assertCIRepairFieldsUnchanged(t, s, before)
		})
	}
}

// A symlinked capacity directory is untrusted: nothing is admitted and nothing
// is written through the link.
func TestRepairAdmission_ASymlinkedCapacityDirectoryAdmitsNothing(t *testing.T) {
	h := newCapHarness(t, 3)
	target := t.TempDir()
	linked := filepath.Join(t.TempDir(), "cenci")
	if err := os.Mkdir(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linked, "capacity")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	h.handle = capacity.NewLedger(link, capacity.Options{Now: h.now, Probe: h.probe})
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, _ := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 through a symlinked capacity directory", n)
	}
	wantPrefix := "capacity ledger unreadable: " + filepath.Join(link, "ledger.json") + ": "
	if s.CapacityWait == nil || !strings.HasPrefix(s.CapacityWait.Reason, wantPrefix) {
		t.Errorf("CapacityWait = %+v, want a reason starting %q", s.CapacityWait, wantPrefix)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Errorf("link target holds %v (err %v), want nothing written through the symlink", entries, err)
	}
}

// A ledger whose store lock cannot be taken in time admits nothing: the repair
// waits with the exact `capacity ledger unreadable: <path>: <reason>` line and the
// ledger is left as it was (AC67).
func TestRepairAdmission_ALedgerLockTimeoutAdmitsNothing(t *testing.T) {
	h := newCapHarness(t, 3)
	lock, err := os.OpenFile(filepath.Join(h.dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, out := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0: a ledger lock timeout admits nothing", n)
	}
	want := "capacity ledger unreadable: " + h.ledger.Path() + ": " + capacity.ErrLockUnavailable.Error()
	if s.CapacityWait == nil || s.CapacityWait.Reason != want {
		t.Fatalf("CapacityWait = %+v, want reason %q", s.CapacityWait, want)
	}
	if !strings.Contains(s.CapacityWait.Reason, "lock") || !strings.Contains(out, want) {
		t.Errorf("reason %q / stdout %q, want the lock reason reported", s.CapacityWait.Reason, out)
	}
	if _, err := os.Stat(h.ledger.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ledger file stat = %v, want none written while the lock was held", err)
	}
}

// A ledger.json that is a symlink is untrusted: nothing is admitted and nothing is
// written through the link (edge case 36).
func TestRepairAdmission_ASymlinkedLedgerFileAdmitsNothing(t *testing.T) {
	h := newCapHarness(t, 3)
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	const content = `{"schema":1,"entries":[],"waits":[]}`
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, h.ledger.Path()); err != nil {
		t.Fatal(err)
	}
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, out := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 through a symlinked ledger file", n)
	}
	want := "capacity ledger unreadable: " + h.ledger.Path() + ": " + capacity.ErrNotRegular.Error()
	if s.CapacityWait == nil || s.CapacityWait.Reason != want {
		t.Fatalf("CapacityWait = %+v, want reason %q", s.CapacityWait, want)
	}
	if !strings.Contains(out, want) {
		t.Errorf("stdout = %q, want the reason %q reported", out, want)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != content {
		t.Errorf("symlink target = %q (err %v), want %q: nothing written through the link", data, err, content)
	}
	if info, err := os.Lstat(h.ledger.Path()); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("ledger.json Lstat = %v, %v, want the symlink left in place", info, err)
	}
}

// ---- every tick reconciles the ledger with a complete inventory -------------

func TestTick_ReconcilesTheLedgerWithTheOwnAndLedgerSockets(t *testing.T) {
	h := newCapHarness(t, 3)
	// A confirmed worker on another socket, gone from it; and one on the own
	// socket that is still there.
	d := h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindImplement, Repo: capOtherRepo, Ticket: 61, Socket: "/s/other", Window: "61-implement", Dir: capOtherDir})
	if err := h.ledger.Confirm(d, capacity.Confirmation{Socket: "/s/other", Window: "61-implement", Dir: capOtherDir}); err != nil {
		t.Fatal(err)
	}
	h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindImplement, Repo: capOtherRepo, Ticket: 62, Socket: "", Window: "62-implement", Dir: capOtherDir})
	h.addWindow("", "62-implement", capOtherDir, false)
	var read []string
	original := cleanupInventoryFn
	cleanupInventoryFn = func(socket string) (tmux.CleanupInventory, error) {
		read = append(read, socket)
		return original(socket)
	}
	t.Cleanup(func() { cleanupInventoryFn = original })
	s := quietState()
	s.LaunchSocket = "/s/a"

	h.tick(&s, quietResponses())

	for _, want := range []string{"/s/a", "/s/other", ""} {
		if !slices.Contains(read, want) {
			t.Errorf("inventory read of sockets %q, want %q among them", read, want)
		}
	}
	if got := h.entries(); len(got) != 1 || got[0].Ticket != 62 {
		t.Errorf("ledger entries = %+v, want the vanished /s/other worker released and the live one kept", got)
	}
}

// ---- activity and the persisted worker liveness ------------------------------

func TestTick_RecordsWhetherARepairWorkerIsLive(t *testing.T) {
	for _, tc := range []struct {
		name      string
		panes     string
		responses []string
		wasLive   bool
		want      bool
	}{
		{"a live ci-repair worker", liveCI(), ciRepairTickResponses("sha-new"), false, true},
		{"a repair launched this tick is live", noWorkerInventory, ciRepairTickResponses("sha-new"), false, true},
		{"the worker ended and CI is green", deadCI(), []string{cleanOpenPR("sha-new"), ciCapGreen, `[]`, `[]`}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newCapHarness(t, 3)
			var calls [][]string
			withCommands(t, tc.responses, &calls)
			stubPanes(t, panesAlways(tc.panes))
			s := ciRepairSeedState()
			s.RepairWorkerLive = tc.wasLive

			restore := captureStdout(t)
			_, _, err := tick(&s)
			restore()
			if err != nil {
				t.Fatalf("tick: %v", err)
			}

			if s.RepairWorkerLive != tc.want {
				t.Errorf("RepairWorkerLive = %v, want %v", s.RepairWorkerLive, tc.want)
			}
		})
	}
}

// statusActivity saves s as the PR's state file and reads `cenci babysit status`'s
// activity back through StatusRepo, the way an operator sees it.
func statusActivity(t *testing.T, s State) string {
	t.Helper()
	dir := t.TempDir()
	s.SchemaVersion = stateSchemaVersion
	if err := save(statePath(dir, s.Repo, s.PR), s); err != nil {
		t.Fatal(err)
	}
	got, err := StatusRepo(s.Repo, s.PR, dir)
	if err != nil {
		t.Fatalf("StatusRepo: %v", err)
	}
	return got.Activity
}

// A repair worker that is still running reads `repairing` across quiet ticks
// (CI pending, nothing to decide, no pane probe) and reads `monitoring` again once
// a tick's reconcile proves the worker gone. No quiet tick adds a pane read.
func TestTick_ARunningRepairReadsRepairingAcrossQuietTicks(t *testing.T) {
	h := newCapHarness(t, 3)
	reads := stubPanes(t, panesAlways(noWorkerInventory))
	s := ciRepairSeedState()

	h.tick(&s, ciRepairTickResponses("sha-new"))
	if got := statusActivity(t, s); got != "repairing" {
		t.Fatalf("activity after the launch tick = %q, want repairing", got)
	}
	readsAfterLaunch := len(*reads)
	quiet := []string{cleanOpenPR("sha-new"), ciCapPending, `[]`, `[]`}

	for i := 1; i <= 2; i++ {
		h.tick(&s, quiet)
		if got := statusActivity(t, s); got != "repairing" {
			t.Fatalf("activity after quiet tick %d with the worker running = %q, want repairing", i, got)
		}
	}
	if len(*reads) != readsAfterLaunch {
		t.Errorf("pane inventory reads = %d, want %d: a quiet tick must not probe", len(*reads), readsAfterLaunch)
	}

	h.removeWindow(capDefaultSocket, "42-ci-repair")
	h.tick(&s, quiet)
	if got := statusActivity(t, s); got != "monitoring" {
		t.Errorf("activity after a tick proved the worker gone = %q, want monitoring", got)
	}
	if got := h.entries(); len(got) != 0 {
		t.Errorf("ledger entries = %+v, want the gone worker's entry released", got)
	}
}

// A worker the ledger cannot vouch for is not carried forward blindly in the other
// direction either: an unreadable ledger on a quiet tick keeps the previous
// verdict, because nothing proved the worker gone.
func TestTick_AnUnreadableLedgerKeepsTheRepairVerdictOnAQuietTick(t *testing.T) {
	h := newCapHarness(t, 3)
	stubPanes(t, panesAlways(noWorkerInventory))
	s := ciRepairSeedState()
	h.tick(&s, ciRepairTickResponses("sha-new"))
	if err := os.WriteFile(h.ledger.Path(), []byte(`{"schema":1,`), 0o600); err != nil {
		t.Fatal(err)
	}

	h.tick(&s, []string{cleanOpenPR("sha-new"), ciCapPending, `[]`, `[]`})

	if got := statusActivity(t, s); got != "repairing" {
		t.Errorf("activity with an unreadable ledger = %q, want repairing kept", got)
	}
}

func TestStatusRepo_ActivityPrecedenceMakesNoTmuxOrLedgerReads(t *testing.T) {
	newCapHarness(t, 3)
	capacityLedgerFn = func() ledgerHandle { t.Fatal("StatusRepo read the ledger"); return nil }
	cleanupInventoryFn = func(string) (tmux.CleanupInventory, error) {
		t.Fatal("StatusRepo read a tmux inventory")
		return tmux.CleanupInventory{}, nil
	}
	tmuxPaneInventory = func(string, string) (string, error) { t.Fatal("StatusRepo read the pane inventory"); return "", nil }
	now := time.Now().UTC()
	wait := func(refreshed time.Time) *capacityWait {
		return &capacityWait{Reason: capWaitReason, Since: refreshed.Add(-time.Minute), RefreshedAt: refreshed}
	}
	for _, tc := range []struct {
		name  string
		state State
		want  string
	}{
		{"nothing special is monitoring", State{Status: "running"}, "monitoring"},
		{"a live repair worker is repairing", State{Status: "running", RepairWorkerLive: true}, "repairing"},
		{"a fresh capacity wait is waiting-for-capacity", State{Status: "running", CapacityWait: wait(now)}, "waiting-for-capacity"},
		{"a wait not refreshed for 3 intervals is stale", State{Status: "running", CapacityWait: wait(now.Add(-4 * time.Minute))}, "monitoring"},
		{"a stale wait with no interval uses the 1m floor", State{Status: "running", IntervalSeconds: 0, CapacityWait: wait(now.Add(-4 * time.Minute))}, "monitoring"},
		{"repairing beats waiting", State{Status: "running", RepairWorkerLive: true, CapacityWait: wait(now)}, "repairing"},
		{"needs-input beats repairing", State{Status: "needs-input", RepairWorkerLive: true}, "needs-input"},
		{"needs-input beats waiting", State{Status: "needs-input", CapacityWait: wait(now)}, "needs-input"},
		{"a retrying supervisor with no worker is monitoring", State{Status: "retrying"}, "monitoring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st := tc.state
			st.SchemaVersion, st.PR, st.Repo = stateSchemaVersion, "42", capRepo
			if st.IntervalSeconds == 0 && !strings.Contains(tc.name, "no interval") {
				st.IntervalSeconds = 60
			}
			if err := save(statePath(dir, capRepo, "42"), st); err != nil {
				t.Fatal(err)
			}

			got, err := StatusRepo(capRepo, "42", dir)

			if err != nil {
				t.Fatalf("StatusRepo: %v", err)
			}
			if got.Activity != tc.want {
				t.Errorf("activity = %q, want %q", got.Activity, tc.want)
			}
		})
	}
}

// ---- review round 1: the entry names the window `cenci run` really opens ----

// `cenci run` resolves an empty socket to tmux.ResolveSocket and makes --dir
// absolute (internal/run), and the ledger matches a window by socket, name and
// directory. An entry recorded raw would never match its own live window, and the
// next complete inventory would release it while the worker runs.
func TestRepairAdmission_TheEntryNamesTheSocketAndDirectoryCenciRunWillRecord(t *testing.T) {
	for _, tc := range []struct {
		name, socket, dir string
		wantSocket        string
		wantDir           func(repo string) string
	}{
		{"a relative --dir", "/s/a", ".", "/s/a", func(repo string) string { return repo }},
		{"a relative nested --dir", "/s/a", "sub/../deeper", "/s/a", func(repo string) string { return filepath.Join(repo, "deeper") }},
		{"no --tmux-socket", "", capRepoDir, capDefaultSocket, func(string) string { return capRepoDir }},
		{"no --tmux-socket and no --dir", "", "", capDefaultSocket, func(repo string) string { return repo }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(t, 3)
			repo := t.TempDir()
			t.Chdir(repo)
			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			wantDir := tc.wantDir(wd)
			s := ciRepairSeedState()
			s.LaunchSocket, s.LaunchDir = tc.socket, tc.dir

			calls, _ := h.tick(&s, ciRepairTickResponses("sha-new"))

			if n := countWorkflowLaunches(calls, "ci-repair"); n != 1 {
				t.Fatalf("ci-repair launches = %d, want 1: %#v", n, calls)
			}
			entries := h.entries()
			if len(entries) != 1 || entries[0].Socket != tc.wantSocket || entries[0].Dir != wantDir || entries[0].State != capacity.StateConfirmed {
				t.Fatalf("ledger entries = %+v, want one confirmed entry on socket %q in dir %q", entries, tc.wantSocket, wantDir)
			}

			// The worker's window exists under the resolved socket and the absolute
			// directory: a complete inventory keeps the entry.
			quiet := quietState()
			quiet.LaunchSocket, quiet.LaunchDir = tc.socket, tc.dir
			h.tick(&quiet, quietResponses())
			if got := h.entries(); len(got) != 1 {
				t.Fatalf("ledger entries = %+v after an inventory showing the worker window, want the entry kept", got)
			}

			// The window is gone: the next complete inventory proves no worker.
			h.removeWindow(tc.wantSocket, "42-ci-repair")
			h.tick(&quiet, quietResponses())
			if got := h.entries(); len(got) != 0 {
				t.Errorf("ledger entries = %+v after the window vanished, want it released", got)
			}
		})
	}
}

// When the socket or directory `cenci run` would use cannot be resolved, the
// admission is refused with a concrete reason instead of storing a value that
// would never match the worker's window.
func TestRepairAdmission_AnUnresolvableLaunchTargetFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, s *State)
	}{
		{"an empty first field in $TMUX", func(t *testing.T, s *State) {
			t.Setenv("TMUX", ",1,2")
			s.LaunchSocket = ""
		}},
		{"a launch directory that cannot be made absolute", func(t *testing.T, s *State) {
			gone := t.TempDir()
			t.Chdir(gone)
			if err := os.Remove(gone); err != nil {
				t.Fatal(err)
			}
			s.LaunchDir = "relative/dir"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(t, 3)
			s := ciRepairSeedState()
			tc.prep(t, &s)
			before := s

			calls, out := h.tick(&s, ciRepairTickResponses("sha-new"))

			if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
				t.Fatalf("ci-repair launches = %d, want 0: the launch target cannot be resolved", n)
			}
			if got := h.entries(); len(got) != 0 {
				t.Errorf("ledger entries = %+v, want none: nothing is stored unnormalized", got)
			}
			const prefix = "capacity launch target unresolved: "
			if s.CapacityWait == nil || !strings.HasPrefix(s.CapacityWait.Reason, prefix) {
				t.Fatalf("CapacityWait = %+v, want a wait whose reason starts with %q", s.CapacityWait, prefix)
			}
			if !strings.Contains(out, s.CapacityWait.Reason) {
				t.Errorf("stdout = %q, want it to report the reason %q", out, s.CapacityWait.Reason)
			}
			assertCIRepairFieldsUnchanged(t, s, before)
		})
	}
}

// A recorded socket that is not a valid path is refused by launch()'s own
// re-check with a typed error, before any window can exist. The reservation of
// that attempt never carries the malformed value (a tab in it would make the
// ledger unreadable for every launcher) and is released.
func TestRepairAdmission_AMalformedRecordedSocketIsNeverStoredAndTheLaunchFailsWithItsOwnError(t *testing.T) {
	for name, socket := range map[string]string{
		"a relative path": "relative/socket",
		"a tab":           capDefaultSocket + "\tx",
		"a newline":       capDefaultSocket + "\nx",
	} {
		t.Run(name, func(t *testing.T) {
			h := newCapHarness(t, 3)
			s := ciRepairSeedState()
			s.LaunchSocket = socket
			var calls [][]string
			withCommands(t, ciRepairTickResponses("sha-new"), &calls)
			tmuxHasSession = func(socket, _ string) (bool, error) {
				if !tmux.ValidSocket(socket) {
					return false, tmux.ErrMalformedSocket
				}
				return true, nil
			}
			restore := captureStdout(t)
			_, _, err := tick(&s)
			restore()

			if !errors.Is(err, tmux.ErrMalformedSocket) {
				t.Fatalf("tick err = %v, want the launch's own %v", err, tmux.ErrMalformedSocket)
			}
			if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
				t.Errorf("ci-repair launches = %d, want 0", n)
			}
			if got := h.entries(); len(got) != 0 {
				t.Errorf("ledger entries = %+v, want none: the doomed attempt's reservation is released", got)
			}
			if _, err := h.ledger.Peek(); err != nil {
				t.Errorf("the ledger is unreadable after the attempt: %v", err)
			}
		})
	}
}

// ---- review round 1: only a machine-cap refusal leaves a wait in the ledger ----

// Each live wait makes dispatch need one more free slot. A repair refused with
// `branch writer active` is not waiting for host capacity, so it must not throttle
// dispatch into another repo.
func TestRepairAdmission_ARefusalThatIsNotTheMachineCapLeavesNoLedgerWait(t *testing.T) {
	h := newCapHarness(t, 2)
	// A dispatch worker holds this PR's branch; one of the two slots is free.
	p := h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindImplement, Repo: capRepo, Ticket: 7, Branch: "feature", Socket: capDefaultSocket, Window: "7-implement", Dir: capRepoDir})
	if err := h.ledger.Confirm(p, capacity.Confirmation{Socket: capDefaultSocket, Window: "7-implement", Dir: capRepoDir}); err != nil {
		t.Fatal(err)
	}
	h.addWindow(capDefaultSocket, "7-implement", capRepoDir, false)
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, out := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 beside a live dispatch writer on the branch", n)
	}
	if s.CapacityWait == nil || s.CapacityWait.Reason != capacity.ReasonBranchWriter || !strings.Contains(out, capacity.ReasonBranchWriter) {
		t.Errorf("wait %+v stdout %q, want the reason %q kept in the persisted state and printed", s.CapacityWait, out, capacity.ReasonBranchWriter)
	}
	if got := h.waits(); len(got) != 0 {
		t.Errorf("ledger waits = %+v, want none: the repair is not waiting for a free slot", got)
	}
	d, err := h.ledger.Admit(capacity.Request{
		Kind: capacity.KindImplement, Repo: capOtherRepo, Ticket: 200, Socket: capDefaultSocket, Window: "200-implement", Dir: capOtherDir,
		Limits: capacity.Limits{Machine: 2},
	})
	if err != nil || !d.Admitted {
		t.Errorf("dispatch admission for another repo = %+v, %v; want it admitted: the free slot is not reserved for a repair that cannot use it", d, err)
	}
}

// A wait the PR already holds is dropped the moment its reason is no longer the
// machine cap.
func TestRepairAdmission_AWaitIsDroppedWhenTheReasonStopsBeingTheMachineCap(t *testing.T) {
	h := newCapHarness(t, 1)
	h.seedImplement(1)
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	h.tick(&s, c.responses)
	if got := h.waits(); len(got) != 1 {
		t.Fatalf("ledger waits = %+v after the first refusal, want the machine-cap wait", got)
	}

	// A dispatch worker now holds the PR's branch: the refusal changes.
	p := h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindImplement, Repo: capRepo, Ticket: 7, Branch: "feature", Socket: capDefaultSocket, Window: "7-implement", Dir: capRepoDir})
	if err := h.ledger.Confirm(p, capacity.Confirmation{Socket: capDefaultSocket, Window: "7-implement", Dir: capRepoDir}); err != nil {
		t.Fatal(err)
	}
	h.addWindow(capDefaultSocket, "7-implement", capRepoDir, false)
	h.tick(&s, c.responses)

	if s.CapacityWait == nil || s.CapacityWait.Reason != capacity.ReasonBranchWriter {
		t.Fatalf("CapacityWait = %+v, want the persisted wait to follow the new reason %q", s.CapacityWait, capacity.ReasonBranchWriter)
	}
	if got := h.waits(); len(got) != 0 {
		t.Errorf("ledger waits = %+v, want the wait record dropped: the repair no longer waits for host capacity", got)
	}
}

// ---- review round 1: adoption never strands or exposes a live worker --------

// A supervisor that finds its worker still running does not launch and does not
// touch its entry: the entry stays confirmed, so the window's exit releases it.
func TestRepairAdmission_ALiveWorkerLeavesItsConfirmedEntryConfirmed(t *testing.T) {
	h := newCapHarness(t, 3)
	p := h.admitAs(capDeadLauncher, capacity.Request{Kind: capacity.KindCIRepair, Repo: capRepo, Ticket: 42, Branch: "feature", Socket: capDefaultSocket, Window: "42-ci-repair", Dir: capRepoDir})
	if err := h.ledger.Confirm(p, capacity.Confirmation{Socket: capDefaultSocket, Window: "42-ci-repair", Dir: capRepoDir}); err != nil {
		t.Fatal(err)
	}
	h.addWindow(capDefaultSocket, "42-ci-repair", capRepoDir, false)
	s := ciRepairSeedState()
	var calls [][]string
	withCommands(t, ciRepairTickResponses("sha-new"), &calls)
	stubPanes(t, func(int) (string, error) { return paneRowOf("42-ci-repair", false, capRepoDir), nil })
	restore := captureStdout(t)
	_, _, err := tick(&s)
	restore()
	if err != nil {
		t.Fatalf("tick: %v", err)
	}

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0 beside the PR's own live worker", n)
	}
	got := h.entries()
	if len(got) != 1 || got[0].HolderID != p.HolderID || got[0].State != capacity.StateConfirmed {
		t.Errorf("ledger entries = %+v, want the original entry still confirmed", got)
	}
}

// ---- review round 1: a failed Peek is reported, not dropped ------------------

type peekFailsLedger struct {
	ledgerHandle
	err error
}

func (p peekFailsLedger) Peek() (capacity.Snapshot, error) { return capacity.Snapshot{}, p.err }

func TestReadCapacityInventory_ReportsALedgerThatCouldNotBePeeked(t *testing.T) {
	newCapHarness(t, 3)
	peekErr := errors.New("the ledger was replaced mid-read")

	inv, notes := readCapacityInventory(peekFailsLedger{ledgerHandle: capacityLedgerFn(), err: peekErr}, "/s/own", true)

	if _, ok := inv["/s/own"]; !ok {
		t.Errorf("inventory = %+v, want the supervisor's own socket still read", inv)
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, peekErr.Error()) }) {
		t.Errorf("notes = %q, want one naming the ledger read failure %q", notes, peekErr)
	}
}

// ---- review round 2: settle retries, accurate messages, the next tick's self-heal ----

// settleFailLedger is the real ledger whose first failReleases Release calls and
// first failConfirms Confirm calls fail with releaseErr / confirmErr.
type settleFailLedger struct {
	ledgerHandle
	releaseErr, confirmErr     error
	failReleases, failConfirms int
	releaseCalls, confirmCalls int
}

func (l *settleFailLedger) Release(p capacity.Permit) error {
	l.releaseCalls++
	if l.releaseCalls <= l.failReleases {
		return l.releaseErr
	}
	return l.ledgerHandle.Release(p)
}

func (l *settleFailLedger) Confirm(p capacity.Permit, c capacity.Confirmation) error {
	l.confirmCalls++
	if l.confirmCalls <= l.failConfirms {
		return l.confirmErr
	}
	return l.ledgerHandle.Confirm(p, c)
}

// failSettles makes the supervisor's ledger seam the failing wrapper; healLedger
// puts the real ledger back.
func (h *capHarness) failSettles(l *settleFailLedger) *settleFailLedger {
	l.ledgerHandle = h.ledger
	h.handle = l
	return l
}

func (h *capHarness) healLedger() { h.handle = h.ledger }

// settleState is a supervisor armed on a session; ran false leaves it with no
// recorded session, so launch() fails before `cenci run`.
func settleState(ran bool) State {
	s := State{PR: "42", Repo: capRepo, Agent: "claude", LaunchSession: "work", LaunchDir: capRepoDir}
	if !ran {
		s.LaunchSession = ""
	}
	return s
}

// A Release or Confirm that fails for a reason a second try can fix (the store was
// busy or briefly unreadable) is retried once, as dispatch's settle is.
func TestSettlePermit_ATransientFailureIsRetriedOnceAndSettles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ran         bool
		err         error
		wantEntries int
		wantCalls   func(l *settleFailLedger) int
	}{
		{"a release that fails once on I/O", false, capacity.ErrIO, 0, func(l *settleFailLedger) int { return l.releaseCalls }},
		{"a release that fails once on a busy lock", false, capacity.ErrLockUnavailable, 0, func(l *settleFailLedger) int { return l.releaseCalls }},
		{"a confirm that fails once on I/O", true, capacity.ErrIO, 1, func(l *settleFailLedger) int { return l.confirmCalls }},
		{"a confirm that fails once on a busy lock", true, capacity.ErrLockUnavailable, 1, func(l *settleFailLedger) int { return l.confirmCalls }},
	} {
		for _, wf := range branchWriters {
			t.Run(tc.name+"/"+wf, func(t *testing.T) {
				h := newCapHarness(t, 3)
				var calls [][]string
				withCommands(t, nil, &calls)
				s := settleState(tc.ran)
				permit := h.permit(&s, wf)
				l := h.failSettles(&settleFailLedger{releaseErr: tc.err, confirmErr: tc.err, failReleases: 1, failConfirms: 1})
				restore := captureStdout(t)

				_ = launch(&s, wf, "42 x", permit)
				out := restore()

				if n := tc.wantCalls(l); n != 2 {
					t.Errorf("settle calls = %d, want 2: the failure is retried once", n)
				}
				if strings.Contains(out, "could not be") {
					t.Errorf("stdout = %q, want no failure line: the retry settled it", out)
				}
				entries := h.entries()
				if len(entries) != tc.wantEntries || (tc.wantEntries == 1 && entries[0].State != capacity.StateConfirmed) {
					t.Errorf("ledger entries = %+v, want %d (confirmed when kept)", entries, tc.wantEntries)
				}
			})
		}
	}
}

// A failure no second try can fix (a corrupt or unknown-schema ledger, an invalid
// request) is reported at once, not retried.
func TestSettlePermit_APermanentFailureIsNotRetried(t *testing.T) {
	for _, err := range []error{capacity.ErrCorrupt, capacity.ErrUnknownSchema, capacity.ErrInvalidRequest, capacity.ErrTooLarge, capacity.ErrNotHeld} {
		for _, ran := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/ran %v", err, ran), func(t *testing.T) {
				h := newCapHarness(t, 3)
				var calls [][]string
				withCommands(t, nil, &calls)
				s := settleState(ran)
				permit := h.permit(&s, "ci-repair")
				l := h.failSettles(&settleFailLedger{releaseErr: err, confirmErr: err, failReleases: 99, failConfirms: 99})
				restore := captureStdout(t)

				_ = launch(&s, "ci-repair", "42 x", permit)
				out := restore()

				if got := l.releaseCalls + l.confirmCalls; got != 1 {
					t.Errorf("settle calls = %d, want 1: %v is not retried", got, err)
				}
				// A confirm of an entry that is gone says so (ledger.json was removed or
				// reset) instead of claiming the entry stays reserved.
				want := "could not be"
				if ran && errors.Is(err, capacity.ErrNotHeld) {
					want = "is missing from the ledger (removed or reset)"
				}
				if !strings.Contains(out, want) {
					t.Errorf("stdout = %q, want the failure reported as %q", out, want)
				}
				if want != "could not be" && strings.Contains(out, "stays reserved") {
					t.Errorf("stdout = %q, want no claim that a missing entry stays reserved", out)
				}
				// `cenci run` may have exited non-zero with no window created.
				if want != "could not be" && !strings.Contains(out, "the worker, if one was started, is not counted by the ledger") {
					t.Errorf("stdout = %q, want no claim that a worker is live: only that one, if started, is not counted", out)
				}
			})
		}
	}
}

// What a failed settle says about recovery fits the error. A transient one is
// retried by the next tick. A permanent one is not: the next tick's ledger read
// fails first, so the line points at the unreadable-ledger line and the manual
// recovery in watch/README.md instead of promising a retry.
func TestSettlePermit_AFailedSettlePromisesARetryOnlyForATransientError(t *testing.T) {
	for _, tc := range []struct {
		err       error
		transient bool
	}{
		{capacity.ErrIO, true},
		{capacity.ErrLockUnavailable, true},
		{capacity.ErrCorrupt, false},
		{capacity.ErrUnknownSchema, false},
		{capacity.ErrUntrusted, false},
		{capacity.ErrTooLarge, false},
		{capacity.ErrInvalidRequest, false},
	} {
		for _, ran := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/ran %v", tc.err, ran), func(t *testing.T) {
				h := newCapHarness(t, 3)
				var calls [][]string
				withCommands(t, nil, &calls)
				s := settleState(ran)
				permit := h.permit(&s, "ci-repair")
				h.failSettles(&settleFailLedger{releaseErr: tc.err, confirmErr: tc.err, failReleases: 99, failConfirms: 99})
				restore := captureStdout(t)

				_ = launch(&s, "ci-repair", "42 x", permit)
				out := restore()

				if !strings.Contains(out, "stays reserved while this supervisor runs") {
					t.Errorf("stdout = %q, want the entry reported as staying reserved while this supervisor runs", out)
				}
				if tc.transient {
					if !strings.Contains(out, "the next tick retries") {
						t.Errorf("stdout = %q, want the next tick's retry named for a transient error", out)
					}
					return
				}
				if strings.Contains(out, "the next tick retries") || strings.Contains(out, "settles it from the tmux inventory") {
					t.Errorf("stdout = %q, want no promise that the next tick settles a permanent failure", out)
				}
				for _, want := range []string{"capacity ledger unreadable", "watch/README.md"} {
					if !strings.Contains(out, want) {
						t.Errorf("stdout = %q, want it to point at %q", out, want)
					}
				}
			})
		}
	}
}

// A settle that keeps failing leaves the entry reserved for as long as this
// supervisor runs, and the line says so (never that an inventory frees it, which a
// live launcher prevents). The next tick, with the ledger readable again, settles
// it from the tmux inventory it reads anyway: a worker window that is running
// confirms the entry, a complete inventory with none releases it, and an inventory
// that cannot say leaves it.
func TestSettlePermit_AFailedSettleSaysWhatRemainsAndTheNextTickSettlesIt(t *testing.T) {
	const window = "42-ci-repair"
	for _, tc := range []struct {
		name      string
		ran       bool
		prep      func(h *capHarness)
		wantKept  bool
		wantState capacity.State
		wantLive  bool   // State.RepairWorkerLive after the healing tick
		wantLine  string // what the healing tick prints
	}{
		{"a failed release and no worker window: the next tick releases it", false, func(*capHarness) {}, false, "", false, "released the ci-repair reservation"},
		{"a failed release but a worker window runs: the next tick confirms it", false, func(h *capHarness) { h.addWindow("", window, capRepoDir, false) }, true, capacity.StateConfirmed, true, "confirmed the ci-repair reservation"},
		{"a failed release and an unreadable inventory: it stays reserved", false, func(h *capHarness) { h.invErr[""] = errors.New("tmux: timed out") }, true, capacity.StateReserved, true, ""},
		{"a failed confirm and the worker window runs: the next tick confirms it", true, func(h *capHarness) { h.addWindow("", window, capRepoDir, false) }, true, capacity.StateConfirmed, true, "confirmed the ci-repair reservation"},
		{"a failed confirm and the worker window is gone: the next tick releases it", true, func(*capHarness) {}, false, "", false, "released the ci-repair reservation"},
		{"a failed confirm and a dead pane: the next tick releases it", true, func(h *capHarness) { h.addWindow("", window, capRepoDir, true) }, false, "", false, "released the ci-repair reservation"},
		{"a failed confirm and an inventory that dropped a row: it stays reserved", true, func(h *capHarness) { h.invDropped[""] = []string{`window "@9": bad row`} }, true, capacity.StateReserved, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(t, 3)
			var calls [][]string
			withCommands(t, nil, &calls)
			s := settleState(tc.ran)
			permit := h.permit(&s, "ci-repair")
			h.failSettles(&settleFailLedger{releaseErr: capacity.ErrIO, confirmErr: capacity.ErrIO, failReleases: 99, failConfirms: 99})
			restore := captureStdout(t)
			_ = launch(&s, "ci-repair", "42 x", permit)
			out := restore()

			verb := map[bool]string{false: "released", true: "confirmed"}[tc.ran]
			if !strings.Contains(out, "could not be "+verb) || !strings.Contains(out, "stays reserved while this supervisor runs") {
				t.Errorf("stdout = %q, want the %s failure saying the entry stays reserved while this supervisor runs", out, verb)
			}
			if strings.Contains(out, "an inventory proving no worker frees it") {
				t.Errorf("stdout = %q, want no claim that an inventory frees the entry: its launcher is this live supervisor", out)
			}
			if got := h.entries(); len(got) != 1 || got[0].State != capacity.StateReserved {
				t.Fatalf("ledger entries = %+v, want the one entry still reserved", got)
			}

			h.healLedger()
			tc.prep(h)
			next := quietState()
			next.RepairWorkerLive = true
			_, healed := h.tick(&next, quietResponses())

			got := h.entries()
			if tc.wantKept != (len(got) == 1) || (tc.wantKept && got[0].State != tc.wantState) {
				t.Errorf("ledger entries after the next tick = %+v, want kept=%v in state %q", got, tc.wantKept, tc.wantState)
			}
			if tc.wantLine == "" && (strings.Contains(healed, "released the ci-repair reservation") || strings.Contains(healed, "confirmed the ci-repair reservation")) {
				t.Errorf("stdout = %q, want no settle line when the inventory cannot say", healed)
			}
			if tc.wantLine != "" && !strings.Contains(healed, tc.wantLine) {
				t.Errorf("stdout = %q, want it to contain %q", healed, tc.wantLine)
			}
			if next.RepairWorkerLive != tc.wantLive {
				t.Errorf("RepairWorkerLive = %v, want %v after the healing tick", next.RepairWorkerLive, tc.wantLive)
			}
		})
	}
}

// A self-heal that cannot write is reported and changes nothing; the tick goes on.
func TestTick_AFailedSelfHealIsReportedAndTheTickGoesOn(t *testing.T) {
	h := newCapHarness(t, 3)
	s := settleState(true)
	h.permit(&s, "ci-repair")
	l := h.failSettles(&settleFailLedger{releaseErr: capacity.ErrIO, failReleases: 99})
	next := quietState()

	_, out := h.tick(&next, quietResponses())

	if l.releaseCalls == 0 {
		t.Fatal("the tick never tried to release the stranded entry")
	}
	if !strings.Contains(out, "reservation could not be settled") || !strings.Contains(out, "stays reserved") {
		t.Errorf("stdout = %q, want the failed settle reported with what remains", out)
	}
	if got := h.entries(); len(got) != 1 || got[0].State != capacity.StateReserved {
		t.Errorf("ledger entries = %+v, want the entry left reserved", got)
	}
}

// A self-heal Confirm of an entry that is gone from the ledger (removed or reset)
// says so, in the words settlePermit uses, instead of claiming it stays reserved.
func TestTick_ASelfHealConfirmOfAMissingEntryIsReportedAsMissing(t *testing.T) {
	h := newCapHarness(t, 3)
	s := settleState(true)
	h.permit(&s, "ci-repair")
	h.addWindow("", "42-ci-repair", capRepoDir, false)
	l := h.failSettles(&settleFailLedger{confirmErr: capacity.ErrNotHeld, failConfirms: 99})
	next := quietState()

	_, out := h.tick(&next, quietResponses())

	if l.confirmCalls != 1 {
		t.Fatalf("confirm calls = %d, want 1: a missing entry is not retried", l.confirmCalls)
	}
	if !strings.Contains(out, "is missing from the ledger (removed or reset)") {
		t.Errorf("stdout = %q, want the entry reported as missing from the ledger", out)
	}
	if strings.Contains(out, "stays reserved") {
		t.Errorf("stdout = %q, want no claim that a missing entry stays reserved", out)
	}
}

// The self-heal settles only a reserved repair entry of this PR that this
// supervisor itself launched. Everything else is another process's or another
// PR's, and an inventory with no window proves nothing about it.
func TestTick_TheSelfHealLeavesEveryOtherReservedEntryAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		admit func(h *capHarness)
	}{
		{"another PR's repair of this supervisor", func(h *capHarness) {
			h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindCIRepair, Repo: capRepo, Ticket: 43, Branch: "feature", Window: "43-ci-repair", Dir: capRepoDir})
		}},
		{"the same PR number in another repo", func(h *capHarness) {
			h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindCIRepair, Repo: capOtherRepo, Ticket: 42, Branch: "feature", Window: "42-ci-repair", Dir: capOtherDir})
		}},
		{"a dispatch worker of the same ticket number", func(h *capHarness) {
			h.admitAs(os.Getpid(), capacity.Request{Kind: capacity.KindImplement, Repo: capRepo, Ticket: 42, Window: "42-implement", Dir: capRepoDir})
		}},
		{"this PR's repair of a live foreign launcher", func(h *capHarness) {
			h.admitAs(capForeignLauncher, capacity.Request{Kind: capacity.KindCIRepair, Repo: capRepo, Ticket: 42, Branch: "feature", Window: "42-ci-repair", Dir: capRepoDir})
		}},
		{"an unreadable inventory leaves a dead launcher's entry for adoption", func(h *capHarness) {
			h.invErr[""] = errors.New("tmux: timed out")
			h.admitAs(capDeadLauncher, capacity.Request{Kind: capacity.KindCIRepair, Repo: capRepo, Ticket: 42, Branch: "feature", Window: "42-ci-repair", Dir: capRepoDir})
		}},
		// The ledger's ownership test also compares the launcher's start time: this pid
		// with another start time is a reused pid, another process's entry. Its worker
		// window is running, so the pid alone would have the self-heal confirm it.
		{"this PR's repair of this pid with another start time", func(h *capHarness) {
			reusedPid := capacity.NewLedger(h.dir, capacity.Options{Now: h.now, Probe: func(int) (string, bool) { return "999", true }})
			d, err := reusedPid.Admit(capacity.Request{Kind: capacity.KindCIRepair, Repo: capRepo, Ticket: 42, Branch: "feature", Window: "42-ci-repair", Dir: capRepoDir, Launcher: os.Getpid(), Limits: capacity.Limits{Machine: 99}})
			if err != nil || !d.Admitted {
				t.Fatalf("admit with another start time: decision %+v, err %v", d, err)
			}
			h.addWindow("", "42-ci-repair", capRepoDir, false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(t, 3)
			tc.admit(h)
			before := h.entries()
			s := quietState()

			_, out := h.tick(&s, quietResponses())

			got := h.entries()
			if len(got) != 1 || got[0] != before[0] {
				t.Errorf("ledger entries = %+v, want %+v untouched", got, before)
			}
			if strings.Contains(out, "reservation") && strings.Contains(out, "settle") {
				t.Errorf("stdout = %q, want no settle line", out)
			}
		})
	}
}

// A read that lost every race with a writer is "ledger unreadable" like any other
// read error: the repair waits with the exact reason and nothing launches.
func TestRepairAdmission_ARacedLedgerReadFailsClosedWithTheLedgerUnreadableReason(t *testing.T) {
	h := newCapHarness(t, 3)
	h.handle = racedLedger{h.ledger}
	c := repairCaseByName(t, "ci-repair")
	s := c.state

	calls, _ := h.tick(&s, c.responses)

	if n := countWorkflowLaunches(calls, "ci-repair"); n != 0 {
		t.Fatalf("ci-repair launches = %d, want 0: admission fails closed", n)
	}
	want := "capacity ledger unreadable: " + h.ledger.Path() + ": " + capacity.ErrRaced.Error()
	if s.CapacityWait == nil || s.CapacityWait.Reason != want {
		t.Errorf("CapacityWait = %+v, want the reason %q", s.CapacityWait, want)
	}
}

type racedLedger struct{ ledgerHandle }

func (racedLedger) Peek() (capacity.Snapshot, error) { return capacity.Snapshot{}, capacity.ErrRaced }

func (racedLedger) Admit(capacity.Request) (capacity.Decision, error) {
	return capacity.Decision{}, capacity.ErrRaced
}
