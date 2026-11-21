package capacity

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testSocket   = "/run/user/1000/tmux-1000/cenci"
	testLauncher = 4242
	launcherBoot = "100"
)

// testEnv is a real ledger over a temp directory with an injected clock and
// launcher probe.
type testEnv struct {
	t        *testing.T
	dir      string
	ledger   *Ledger
	mu       sync.Mutex
	now      time.Time
	procs    map[int]string // pid -> start time, for live processes
	pid      int            // the process the ledger believes is calling
	machine  int
	repoCaps map[string]int
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{
		t:        t,
		dir:      trustedDir(t),
		now:      time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		procs:    map[int]string{testLauncher: launcherBoot},
		pid:      testLauncher,
		machine:  3,
		repoCaps: map[string]int{},
	}
	e.ledger = e.newLedger(200 * time.Millisecond)
	return e
}

func (e *testEnv) newLedger(lockTimeout time.Duration) *Ledger {
	return NewLedger(e.dir, Options{Now: e.clock, Probe: e.probe, PID: e.callerPid, LockTimeout: lockTimeout})
}

// callerPid is the process the ledger sees as the caller of Wait and Restore.
func (e *testEnv) callerPid() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pid
}

// as runs fn with the ledger's caller switched to pid, then switches it back, so
// a test can play two supervisors against one ledger.
func (e *testEnv) as(pid int, fn func()) {
	e.t.Helper()
	e.mu.Lock()
	prev := e.pid
	e.pid = pid
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.pid = prev
		e.mu.Unlock()
	}()
	fn()
}

func (e *testEnv) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *testEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

func (e *testEnv) probe(pid int) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	start, ok := e.procs[pid]
	return start, ok
}

func (e *testEnv) setProc(pid int, start string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.procs[pid] = start
}

func (e *testEnv) killProc(pid int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.procs, pid)
}

func windowName(kind Kind, ticket int) string { return fmt.Sprintf("%d-%s", ticket, kind) }

func repoDir(repo string) string { return "/work/" + repo }

func (e *testEnv) req(kind Kind, repo string, ticket int) Request {
	caps := map[string]int{}
	for k, v := range e.repoCaps {
		caps[k] = v
	}
	return Request{
		Kind:     kind,
		Repo:     repo,
		Ticket:   ticket,
		Socket:   testSocket,
		Window:   windowName(kind, ticket),
		Dir:      repoDir(repo),
		Launcher: testLauncher,
		Limits:   Limits{Machine: e.machine, Repos: caps},
	}
}

func (e *testEnv) admit(req Request) Decision {
	e.t.Helper()
	d, err := e.ledger.Admit(req)
	if err != nil {
		e.t.Fatalf("Admit(%s %s#%d): %v", req.Kind, req.Repo, req.Ticket, err)
	}
	return d
}

func (e *testEnv) mustAdmit(req Request) Permit {
	e.t.Helper()
	d := e.admit(req)
	if !d.Admitted {
		e.t.Fatalf("Admit(%s %s#%d) refused: %q, want admitted", req.Kind, req.Repo, req.Ticket, d.Reason)
	}
	return d.Permit
}

func (e *testEnv) mustRefuse(req Request, reason string) {
	e.t.Helper()
	d := e.admit(req)
	if d.Admitted {
		e.t.Fatalf("Admit(%s %s#%d) admitted, want refusal %q", req.Kind, req.Repo, req.Ticket, reason)
	}
	if d.Reason != reason {
		e.t.Fatalf("Admit(%s %s#%d) reason = %q, want %q", req.Kind, req.Repo, req.Ticket, d.Reason, reason)
	}
}

func (e *testEnv) snapshot() Snapshot {
	e.t.Helper()
	s, err := e.ledger.Peek()
	if err != nil {
		e.t.Fatalf("Peek: %v", err)
	}
	return s
}

func (e *testEnv) held() []Entry { e.t.Helper(); return e.snapshot().Entries }

func (e *testEnv) confirm(p Permit) {
	e.t.Helper()
	err := e.ledger.Confirm(p, Confirmation{Socket: testSocket, Window: windowName(p.Kind, p.Ticket), Dir: repoDir(p.Repo)})
	if err != nil {
		e.t.Fatalf("Confirm: %v", err)
	}
}

// inv is a complete inventory of testSocket read now.
func (e *testEnv) inv(windows ...Window) Inventory {
	return Inventory{testSocket: {Complete: true, ReadAt: e.clock(), Windows: windows}}
}

func live(name, dir string) Window { return Window{Name: name, Dir: dir, PaneIDs: []string{"%1"}} }

func dead(name, dir string) Window {
	return Window{Name: name, Dir: dir, PaneIDs: []string{"%1"}, AllDead: true}
}

func (e *testEnv) reconcile(inv Inventory) []Entry {
	e.t.Helper()
	released, err := e.ledger.Reconcile(inv)
	if err != nil {
		e.t.Fatalf("Reconcile: %v", err)
	}
	return released
}

func (e *testEnv) writeLedger(content string) string {
	e.t.Helper()
	path := filepath.Join(e.dir, "ledger.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func (e *testEnv) readLedger() string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.dir, "ledger.json"))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

func assertPathFree(t *testing.T, err error, dir string) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(msg, dir) {
		t.Errorf("error %q leaks the store path", msg)
	}
	for _, banned := range []string{" dispatch ", " skip:"} {
		if strings.Contains(msg, banned) {
			t.Errorf("error %q contains %q", msg, banned)
		}
	}
}

// ---- lifecycle -------------------------------------------------------------

func TestAdmitConfirmExitReconcileLeavesZeroEntries(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))

	if got := e.held(); len(got) != 1 || got[0].State != StateReserved || got[0].Ticket != 1 {
		t.Fatalf("after Admit entries = %+v, want one reserved entry for #1", got)
	}
	e.confirm(p)
	got := e.held()
	if len(got) != 1 || got[0].State != StateConfirmed || got[0].Socket != testSocket || got[0].Window != "1-implement" || got[0].Dir != "/work/o/a" {
		t.Fatalf("after Confirm entries = %+v, want one confirmed entry on %s", got, testSocket)
	}

	if released := e.reconcile(e.inv(live("1-implement", "/work/o/a"))); len(released) != 0 {
		t.Fatalf("a live worker's entry was released: %+v", released)
	}
	if len(e.held()) != 1 {
		t.Fatalf("entries after a live inventory = %d, want 1", len(e.held()))
	}

	if released := e.reconcile(e.inv()); len(released) != 1 || released[0].Ticket != 1 {
		t.Fatalf("released = %+v, want the one exited worker", released)
	}
	if got := e.held(); len(got) != 0 {
		t.Fatalf("entries after the worker exited = %+v, want none", got)
	}
}

func TestReleaseFreesTheSlotAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.mustRefuse(e.req(KindImplement, "o/a", 2), ReasonMachineCap)
	if err := e.ledger.Release(p); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := e.ledger.Release(p); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	e.mustAdmit(e.req(KindImplement, "o/a", 2))
}

func TestConfirmOfAPermitThatIsNoLongerHeldIsAnError(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	if err := e.ledger.Release(p); err != nil {
		t.Fatal(err)
	}
	err := e.ledger.Confirm(p, Confirmation{Socket: testSocket, Window: "1-implement", Dir: "/work/o/a"})
	if !errors.Is(err, ErrNotHeld) {
		t.Fatalf("Confirm = %v, want ErrNotHeld", err)
	}
	if got := e.held(); len(got) != 0 {
		t.Fatalf("Confirm resurrected an entry: %+v", got)
	}
}

func TestReleaseAndConfirmNeverCreateTheLedger(t *testing.T) {
	_, parent, dir := storeDirs(t)
	l := NewLedger(dir, Options{})
	p := Permit{HolderID: "nobody", Kind: KindImplement, Repo: "o/a", Ticket: 1}
	if err := l.Release(p); err != nil {
		t.Fatalf("Release on an absent ledger: %v", err)
	}
	if err := l.Confirm(p, Confirmation{Socket: "s", Window: "w", Dir: "/d"}); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("Confirm on an absent ledger = %v, want ErrNotHeld", err)
	}
	if _, err := l.Reconcile(Inventory{}); err != nil {
		t.Fatalf("Reconcile on an absent ledger: %v", err)
	}
	if err := l.DropWait("o/a", 1); err != nil {
		t.Fatalf("DropWait on an absent ledger: %v", err)
	}
	if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only and cleanup paths created %s (err %v)", parent, err)
	}
}

func TestPeekTakesNoLockAndCreatesNothing(t *testing.T) {
	_, parent, dir := storeDirs(t)
	l := NewLedger(dir, Options{})
	snap, err := l.Peek()
	if err != nil || len(snap.Entries) != 0 || len(snap.Waits) != 0 {
		t.Fatalf("Peek(absent) = %+v, %v; want empty", snap, err)
	}
	if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Peek created %s (err %v)", parent, err)
	}

	e := newEnv(t)
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	release := make(chan struct{})
	holding := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.ledger.store.WithLock(func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	start := time.Now()
	got, err := e.ledger.Peek()
	elapsed := time.Since(start)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err != nil || len(got.Entries) != 1 {
		t.Fatalf("Peek under a held lock = %+v, %v; want the entry", got, err)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("Peek waited %s on the lock", elapsed)
	}
}

func TestEmptyLedgerMarshalsEmptyArraysNotNull(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	if err := e.ledger.Release(p); err != nil {
		t.Fatal(err)
	}
	content := e.readLedger()
	if strings.Contains(content, "null") {
		t.Fatalf("ledger contains null: %s", content)
	}
	if !strings.Contains(content, `"entries":[]`) || !strings.Contains(content, `"waits":[]`) {
		t.Fatalf("ledger = %s, want empty entries and waits arrays", content)
	}
	if got, err := e.ledger.Peek(); err != nil || got.Entries == nil || got.Waits == nil {
		t.Fatalf("Peek = %+v, %v; want non-nil empty slices", got, err)
	}
}

func TestLedgerWiderThanTheDailyQuotaCapRoundTrips(t *testing.T) {
	e := newEnv(t)
	e.machine = 100
	for i := 1; i <= 20; i++ {
		e.mustAdmit(e.req(KindImplement, "o/a", i))
	}
	if info, err := os.Stat(filepath.Join(e.dir, "ledger.json")); err != nil || info.Size() <= 4096 {
		t.Fatalf("ledger size = %v, %v; want > 4096 bytes to exercise the ledger read limit", info, err)
	}
	if got := e.held(); len(got) != 20 {
		t.Fatalf("entries = %d, want 20", len(got))
	}
}

// ---- caps ------------------------------------------------------------------

func TestMachineCapRefusesPastTheCap(t *testing.T) {
	e := newEnv(t)
	e.machine = 2
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.mustAdmit(e.req(KindPlan, "o/b", 2))
	e.mustRefuse(e.req(KindImplement, "o/c", 3), ReasonMachineCap)
	if got := e.held(); len(got) != 2 {
		t.Fatalf("entries = %d, want 2: a refusal must not write an entry", len(got))
	}
}

func TestMachineCapZeroAdmitsNothing(t *testing.T) {
	e := newEnv(t)
	e.machine = 0
	for _, k := range []Kind{KindImplement, KindCIRepair} {
		e.mustRefuse(e.req(k, "o/a", 1), ReasonMachineCap)
	}
}

func TestRepoCapLimitsOneRepoWhileOthersStillAdmit(t *testing.T) {
	e := newEnv(t)
	e.machine = 3
	e.repoCaps["o/a"] = 1
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.mustRefuse(e.req(KindImplement, "o/a", 2), ReasonRepoCap)
	e.mustRefuse(e.req(KindImplement, "o/a", 3), ReasonRepoCap)
	e.mustAdmit(e.req(KindImplement, "o/b", 4))
	e.mustAdmit(e.req(KindImplement, "o/b", 5))
	e.mustRefuse(e.req(KindImplement, "o/b", 6), ReasonMachineCap)
}

func TestRepoCapZeroAdmitsNothingForThatRepo(t *testing.T) {
	e := newEnv(t)
	e.repoCaps["o/a"] = 0
	e.mustRefuse(e.req(KindImplement, "o/a", 1), ReasonRepoCap)
	e.mustRefuse(e.req(KindCIRepair, "o/a", 2), ReasonRepoCap)
	e.mustAdmit(e.req(KindImplement, "o/b", 3))
}

func TestRepoCapAboveMachineCapIsClampedToTheMachineCap(t *testing.T) {
	e := newEnv(t)
	e.machine = 2
	e.repoCaps["o/a"] = 10
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.mustAdmit(e.req(KindImplement, "o/a", 2))
	e.mustRefuse(e.req(KindImplement, "o/a", 3), ReasonMachineCap)
}

func TestRepoCapCountsRepairWorkersAndMatchesRepoNamesIgnoringCase(t *testing.T) {
	e := newEnv(t)
	e.repoCaps["o/a"] = 1
	e.mustAdmit(e.req(KindCIRepair, "O/A", 1))
	req := e.req(KindImplement, "o/a", 2)
	e.mustRefuse(req, ReasonRepoCap)
}

func TestManualSessionsAndOrphansCountAgainstTheMachineCapOnly(t *testing.T) {
	e := newEnv(t)
	e.machine = 3
	e.repoCaps["o/a"] = 2
	req := e.req(KindImplement, "o/a", 1)
	// One manual session plus two orphans fill the machine cap of 3, although the
	// repo itself runs nothing: the refusal is the machine's, not the repo's.
	req.Obs = Observation{
		Sessions: []Session{{Socket: testSocket, Window: "scratch", Running: true}},
		Orphans:  2,
	}
	e.mustRefuse(req, ReasonMachineCap)

	// Two observed workers leave a slot, and the repo cap (2) ignores them.
	req.Obs = Observation{Orphans: 2}
	e.mustAdmit(req)
	req2 := e.req(KindImplement, "o/a", 2)
	req2.Obs = Observation{Orphans: 1}
	e.mustAdmit(req2)
	req3 := e.req(KindImplement, "o/a", 3)
	req3.Obs = Observation{}
	e.mustRefuse(req3, ReasonRepoCap)
}

func TestLoweringTheCapNeverReleasesAnythingAndBlocksAdmission(t *testing.T) {
	e := newEnv(t)
	e.machine = 3
	var permits []Permit
	for i := 1; i <= 3; i++ {
		p := e.mustAdmit(e.req(KindImplement, "o/a", i))
		e.confirm(p)
		permits = append(permits, p)
	}
	e.machine = 1
	e.mustRefuse(e.req(KindImplement, "o/a", 4), ReasonMachineCap)
	if got := e.held(); len(got) != 3 {
		t.Fatalf("entries = %d after lowering the cap, want 3 untouched", len(got))
	}
	// Still blocked until used slots drop below the cap.
	for _, p := range permits[:2] {
		if err := e.ledger.Release(p); err != nil {
			t.Fatal(err)
		}
	}
	e.mustRefuse(e.req(KindImplement, "o/a", 4), ReasonMachineCap)
	if err := e.ledger.Release(permits[2]); err != nil {
		t.Fatal(err)
	}
	e.mustAdmit(e.req(KindImplement, "o/a", 4))
}

// ---- exclusivity -----------------------------------------------------------

func TestSecondAdmissionOfTheSameTicketIsRefusedAsAlreadyReserved(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindImplement, "o/a", 7))
	e.mustRefuse(e.req(KindImplement, "o/a", 7), ReasonWorkerReserved)
	e.mustRefuse(e.req(KindResume, "o/a", 7), ReasonWorkerReserved)
	// Same number in another repo is another ticket.
	e.mustAdmit(e.req(KindImplement, "o/b", 7))
	if got := e.held(); len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
}

func TestExclusivityIsCheckedBeforeTheCaps(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	e.mustAdmit(e.req(KindImplement, "o/a", 7))
	e.mustRefuse(e.req(KindImplement, "o/a", 7), ReasonWorkerReserved)
}

func TestSimultaneousAdmissionsOfOneTicketLaunchExactlyOne(t *testing.T) {
	e := newEnv(t)
	e.machine = 3
	results := make(chan Decision, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := e.newLedger(5 * time.Second).Admit(e.req(KindImplement, "o/a", 42))
			if err != nil {
				t.Error(err)
			}
			results <- d
		}()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for d := range results {
		if d.Admitted {
			admitted++
		} else if d.Reason != ReasonWorkerReserved {
			t.Errorf("refusal reason = %q, want %q", d.Reason, ReasonWorkerReserved)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted = %d, want exactly 1", admitted)
	}
}

func TestSimultaneousAdmissionsForTheFinalSlotLaunchExactlyOne(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	results := make(chan Decision, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kind := KindImplement
			if i%2 == 1 {
				kind = KindCIRepair
			}
			d, err := e.newLedger(5 * time.Second).Admit(e.req(kind, "o/a", 100+i))
			if err != nil {
				t.Error(err)
			}
			results <- d
		}(i)
	}
	wg.Wait()
	close(results)
	admitted := 0
	for d := range results {
		if d.Admitted {
			admitted++
		} else if d.Reason != ReasonMachineCap {
			t.Errorf("refusal reason = %q, want %q", d.Reason, ReasonMachineCap)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted = %d, want exactly 1 for the final slot", admitted)
	}
}

// ---- branch writer ---------------------------------------------------------

func TestBranchWriterConflictsInBothDirections(t *testing.T) {
	t.Run("dispatch writer blocks babysit", func(t *testing.T) {
		e := newEnv(t)
		dispatch := e.req(KindImplement, "o/a", 1)
		dispatch.Branch = "feat/x"
		e.mustAdmit(dispatch)
		for _, k := range []Kind{KindCIRepair, KindMergeRepair, KindBabysitReview} {
			babysit := e.req(k, "o/a", 90)
			babysit.Branch = "feat/x"
			e.mustRefuse(babysit, ReasonBranchWriter)
		}
	})
	t.Run("babysit writer blocks dispatch", func(t *testing.T) {
		e := newEnv(t)
		babysit := e.req(KindCIRepair, "o/a", 90)
		babysit.Branch = "feat/x"
		e.mustAdmit(babysit)
		for _, k := range []Kind{KindImplement, KindPlan, KindReplan, KindResume} {
			dispatch := e.req(k, "o/a", 1)
			dispatch.Branch = "feat/x"
			e.mustRefuse(dispatch, ReasonBranchWriter)
		}
	})
}

func TestBranchWriterCheckIgnoresOtherBranchesReposAndEmptyBranches(t *testing.T) {
	e := newEnv(t)
	e.machine = 10
	held := e.req(KindCIRepair, "o/a", 90)
	held.Branch = "feat/x"
	e.mustAdmit(held)

	otherBranch := e.req(KindImplement, "o/a", 1)
	otherBranch.Branch = "feat/y"
	e.mustAdmit(otherBranch)

	otherRepo := e.req(KindImplement, "o/b", 2)
	otherRepo.Branch = "feat/x"
	e.mustAdmit(otherRepo)

	freshPickup := e.req(KindImplement, "o/a", 3)
	freshPickup.Branch = ""
	e.mustAdmit(freshPickup)
}

func TestBranchWriterCheckDoesNotApplyWithinOneFamily(t *testing.T) {
	e := newEnv(t)
	first := e.req(KindCIRepair, "o/a", 90)
	first.Branch = "feat/x"
	e.mustAdmit(first)
	second := e.req(KindBabysitReview, "o/a", 91)
	second.Branch = "feat/x"
	e.mustAdmit(second)
}

func TestBranchWriterCheckRunsBeforeTheCaps(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	dispatch := e.req(KindImplement, "o/a", 1)
	dispatch.Branch = "feat/x"
	e.mustAdmit(dispatch)
	babysit := e.req(KindCIRepair, "o/a", 90)
	babysit.Branch = "feat/x"
	e.mustRefuse(babysit, ReasonBranchWriter)
}

// ---- waits and priority ----------------------------------------------------

func (e *testEnv) wait(kind Kind, repo string, ticket int, reason string) {
	e.t.Helper()
	err := e.ledger.Wait(WaitRequest{Kind: kind, Repo: repo, Ticket: ticket, Reason: reason, Interval: 10 * time.Second})
	if err != nil {
		e.t.Fatalf("Wait: %v", err)
	}
}

func TestWaitRecordsNeverOccupyASlot(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	e.wait(KindCIRepair, "o/a", 90, "concurrency cap reached")
	e.wait(KindMergeRepair, "o/b", 91, "concurrency cap reached")
	if got := e.held(); len(got) != 0 {
		t.Fatalf("entries = %+v, want none: a wait holds no slot", got)
	}
	// Waiting repairs do not block each other: whichever asks first with a free
	// slot is admitted.
	e.mustAdmit(e.req(KindCIRepair, "o/b", 91))
	e.mustRefuse(e.req(KindCIRepair, "o/a", 90), ReasonMachineCap)
}

func TestDispatchNeedsMoreFreeSlotsThanLiveWaits(t *testing.T) {
	e := newEnv(t)
	e.machine = 2
	e.mustAdmit(e.req(KindImplement, "o/a", 1)) // one slot left
	e.wait(KindCIRepair, "o/a", 90, "concurrency cap reached")

	e.mustRefuse(e.req(KindImplement, "o/b", 2), ReasonMachineCap)
	// The waiting repair itself takes the free slot, and its wait is dropped.
	e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	if got := e.snapshot().Waits; len(got) != 0 {
		t.Fatalf("waits = %+v, want the admitted repair's wait dropped", got)
	}
}

func TestDispatchAdmitsWhenFreeSlotsExceedLiveWaits(t *testing.T) {
	e := newEnv(t)
	e.machine = 3
	e.mustAdmit(e.req(KindImplement, "o/a", 1)) // two slots left
	e.wait(KindCIRepair, "o/a", 90, "concurrency cap reached")
	e.mustAdmit(e.req(KindImplement, "o/b", 2))
	e.mustRefuse(e.req(KindImplement, "o/b", 3), ReasonMachineCap)
}

func TestWaitExpiresAfterThreePollIntervalsWithoutARefresh(t *testing.T) {
	e := newEnv(t)
	e.machine = 2
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.wait(KindCIRepair, "o/a", 90, "concurrency cap reached")

	e.advance(29 * time.Second)
	e.mustRefuse(e.req(KindImplement, "o/b", 2), ReasonMachineCap)

	e.advance(2 * time.Second) // 31s > 3 x 10s
	e.mustAdmit(e.req(KindImplement, "o/b", 2))
	if got := e.snapshot().Waits; len(got) != 0 {
		t.Fatalf("waits = %+v, want the expired wait pruned", got)
	}
}

func TestWaitRefreshKeepsSinceAndExtendsExpiry(t *testing.T) {
	e := newEnv(t)
	e.wait(KindCIRepair, "o/a", 90, "concurrency cap reached")
	since := e.snapshot().Waits[0].Since
	e.advance(20 * time.Second)
	e.wait(KindCIRepair, "o/a", 90, "capacity observation unavailable: daemon snapshot")

	waits := e.snapshot().Waits
	if len(waits) != 1 {
		t.Fatalf("waits = %+v, want one record per PR", waits)
	}
	w := waits[0]
	if !w.Since.Equal(since) {
		t.Errorf("Since = %s, want the first-wait time %s", w.Since, since)
	}
	if w.Reason != "capacity observation unavailable: daemon snapshot" {
		t.Errorf("Reason = %q, want the refreshed reason", w.Reason)
	}
	if want := e.clock().Add(30 * time.Second); !w.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %s, want %s", w.ExpiresAt, want)
	}
}

func TestDropWaitRemovesOnlyThatPRsRecord(t *testing.T) {
	e := newEnv(t)
	e.wait(KindCIRepair, "o/a", 90, "x")
	e.wait(KindCIRepair, "o/a", 91, "x")
	if err := e.ledger.DropWait("o/a", 90); err != nil {
		t.Fatal(err)
	}
	waits := e.snapshot().Waits
	if len(waits) != 1 || waits[0].Ticket != 91 {
		t.Fatalf("waits = %+v, want only #91", waits)
	}
	if err := e.ledger.DropWait("o/a", 90); err != nil {
		t.Fatalf("dropping an absent wait: %v", err)
	}
}

func TestSnapshotLiveWaitsIgnoresExpiredRecords(t *testing.T) {
	e := newEnv(t)
	e.wait(KindCIRepair, "o/a", 90, "x")
	snap := e.snapshot()
	if got := snap.LiveWaits(e.clock()); got != 1 {
		t.Fatalf("LiveWaits = %d, want 1", got)
	}
	if got := snap.LiveWaits(e.clock().Add(time.Minute)); got != 0 {
		t.Fatalf("LiveWaits after expiry = %d, want 0", got)
	}
}

// ---- wait ownership (schema 2) ----------------------------------------------

const (
	// waitV1 is a schema 1 wait: seven keys, no owner. It was recorded at 09:00,
	// the test clock's start, and expires at 09:03.
	waitV1 = `{"repo":"o/a","ticket":90,"kind":"ci-repair","reason":"concurrency cap reached","since":"2026-11-20T08:59:00Z","refreshedAt":"2026-11-20T09:00:00Z","expiresAt":"2026-11-20T09:03:00Z"}`
	// entryV1 is a confirmed ci-repair entry, valid under both schemas.
	entryV1 = `{"holderId":"h","kind":"ci-repair","repo":"o/b","ticket":5,"branch":"b","launcherPid":7,"launcherStart":"12","socket":"s","window":"5-ci-repair","dir":"/d","launchId":"L","state":"confirmed","createdAt":"2026-11-20T09:00:00Z","updatedAt":"2026-11-20T09:00:01Z"}`
)

// waitV2 is the same wait with an owner: pid and start are raw JSON values.
func waitV2(pid, start string) string {
	return strings.Replace(waitV1, `"expiresAt":"2026-11-20T09:03:00Z"`, `"expiresAt":"2026-11-20T09:03:00Z","ownerPid":`+pid+`,"ownerStart":`+start, 1)
}

func schemaOne(waits string) string { return `{"schema":1,"entries":[],"waits":[` + waits + `]}` }
func schemaTwo(waits string) string { return `{"schema":2,"entries":[],"waits":[` + waits + `]}` }

// waitAs records a wait while the ledger sees pid as its caller.
func (e *testEnv) waitAs(pid int, kind Kind, repo string, ticket int) {
	e.t.Helper()
	e.as(pid, func() { e.wait(kind, repo, ticket, "concurrency cap reached") })
}

// supervisor is a live process other than the dispatch caller: it owns waits.
func (e *testEnv) supervisor(pid int, start string) int {
	e.setProc(pid, start)
	return pid
}

func TestWaitRecordsTheCallingProcessAsItsOwner(t *testing.T) {
	e := newEnv(t)
	e.wait(KindCIRepair, "o/a", 90, "concurrency cap reached")
	w := e.snapshot().Waits[0]
	if w.OwnerPID != testLauncher || w.OwnerStart != launcherBoot {
		t.Fatalf("owner = %d/%q, want the caller %d/%q", w.OwnerPID, w.OwnerStart, testLauncher, launcherBoot)
	}
}

func TestWaitRefreshByADifferentLiveProcessTakesOverOwnership(t *testing.T) {
	e := newEnv(t)
	first := e.supervisor(6001, "50")
	second := e.supervisor(6002, "60")
	e.waitAs(first, KindCIRepair, "o/a", 90)
	since := e.snapshot().Waits[0].Since
	e.advance(5 * time.Second)

	e.waitAs(second, KindCIRepair, "o/a", 90)

	waits := e.snapshot().Waits
	if len(waits) != 1 {
		t.Fatalf("waits = %+v, want one record per PR", waits)
	}
	if w := waits[0]; w.OwnerPID != second || w.OwnerStart != "60" || !w.Since.Equal(since) {
		t.Fatalf("wait = %+v, want owner %d/60 and the first Since %s", w, second, since)
	}
	// The first supervisor's death no longer matters: the wait is the second's.
	e.killProc(first)
	e.machine = 2
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.mustRefuse(e.req(KindImplement, "o/b", 2), ReasonMachineCap)
}

// The three operations that already drop an expired wait also drop one whose
// owner is gone, so a SIGKILLed supervisor does not hold a slot free for 3 poll
// intervals.
func TestWaitWhoseOwnerIsDeadIsDroppedByAdmitAndTheSlotIsUsable(t *testing.T) {
	e := newEnv(t)
	e.machine = 2
	e.mustAdmit(e.req(KindImplement, "o/a", 1)) // one slot left
	owner := e.supervisor(6001, "50")
	e.waitAs(owner, KindCIRepair, "o/a", 90)
	e.mustRefuse(e.req(KindImplement, "o/b", 2), ReasonMachineCap) // the live wait keeps the last slot free

	e.killProc(owner)

	e.mustAdmit(e.req(KindImplement, "o/b", 2))
	if got := e.snapshot().Waits; len(got) != 0 {
		t.Fatalf("waits = %+v, want the dead owner's wait removed by Admit", got)
	}
}

func TestWaitWhoseOwnerIsDeadIsDroppedByReconcile(t *testing.T) {
	e := newEnv(t)
	owner := e.supervisor(6001, "50")
	e.waitAs(owner, KindCIRepair, "o/a", 90)
	e.killProc(owner)

	e.reconcile(e.inv())

	if got := e.snapshot().Waits; len(got) != 0 {
		t.Fatalf("waits = %+v, want the dead owner's wait removed by Reconcile", got)
	}
}

func TestWaitWhoseOwnerIsDeadIsDroppedByAnotherWait(t *testing.T) {
	e := newEnv(t)
	owner := e.supervisor(6001, "50")
	e.waitAs(owner, KindCIRepair, "o/a", 90)
	e.killProc(owner)

	e.wait(KindMergeRepair, "o/b", 91, "concurrency cap reached")

	waits := e.snapshot().Waits
	if len(waits) != 1 || waits[0].Ticket != 91 {
		t.Fatalf("waits = %+v, want only the live caller's wait #91", waits)
	}
}

func TestWaitWhoseOwnerPIDWasReusedIsDropped(t *testing.T) {
	e := newEnv(t)
	e.machine = 2
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	owner := e.supervisor(6001, "50")
	e.waitAs(owner, KindCIRepair, "o/a", 90)
	e.setProc(owner, "999999") // the same pid, another process: a different start time

	e.mustAdmit(e.req(KindImplement, "o/b", 2))
	if got := e.snapshot().Waits; len(got) != 0 {
		t.Fatalf("waits = %+v, want the wait of a reused pid removed", got)
	}
}

func TestWaitOwnerWithoutAStartTimeIsDroppedOnlyWhenItsPIDIsNotAlive(t *testing.T) {
	e := newEnv(t)
	e.machine = 2
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	owner := e.supervisor(6001, "") // a platform with no start time
	e.waitAs(owner, KindCIRepair, "o/a", 90)
	if w := e.snapshot().Waits[0]; w.OwnerPID != owner || w.OwnerStart != "" {
		t.Fatalf("wait owner = %d/%q, want %d with an empty start", w.OwnerPID, w.OwnerStart, owner)
	}

	// The pid is alive, and a start time that appeared later is no disagreement.
	e.setProc(owner, "70")
	e.mustRefuse(e.req(KindImplement, "o/b", 2), ReasonMachineCap)
	if len(e.snapshot().Waits) != 1 {
		t.Fatal("a live owner's wait was dropped")
	}

	e.killProc(owner)
	e.mustAdmit(e.req(KindImplement, "o/b", 2))
	if got := e.snapshot().Waits; len(got) != 0 {
		t.Fatalf("waits = %+v, want the wait of a pid that is gone removed", got)
	}
}

// The #1469 priority rule, unchanged: a wait whose owner is alive makes dispatch
// need more free slots than live waits until the wait expires.
func TestWaitWhoseOwnerIsAliveKeepsThePriorityRuleUntilItExpires(t *testing.T) {
	e := newEnv(t)
	e.machine = 2
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	owner := e.supervisor(6001, "50")
	e.waitAs(owner, KindCIRepair, "o/a", 90)

	e.advance(29 * time.Second)
	e.mustRefuse(e.req(KindImplement, "o/b", 2), ReasonMachineCap)
	e.advance(2 * time.Second) // past the 3 x 10s expiry
	e.mustAdmit(e.req(KindImplement, "o/b", 2))
}

func TestSchemaOneWaitIsKeptWithTimeOnlyExpiryWhateverTheProcessesDo(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	e.writeLedger(schemaOne(waitV1))
	e.killProc(testLauncher) // no process of any pid is alive

	e.mustRefuse(e.req(KindImplement, "o/b", 2), ReasonMachineCap) // the wait still keeps the only slot free
	waits := e.snapshot().Waits
	if len(waits) != 1 || waits[0].OwnerPID != 0 || waits[0].OwnerStart != "" {
		t.Fatalf("waits = %+v, want the schema 1 wait kept with ownerPid 0", waits)
	}
	e.reconcile(e.inv())
	if len(e.snapshot().Waits) != 1 {
		t.Fatal("Reconcile dropped a schema 1 wait before its expiresAt")
	}

	e.advance(3*time.Minute + time.Second) // past 09:03
	e.setProc(testLauncher, launcherBoot)
	e.mustAdmit(e.req(KindImplement, "o/b", 2))
	if got := e.snapshot().Waits; len(got) != 0 {
		t.Fatalf("waits = %+v, want the schema 1 wait removed at its expiresAt", got)
	}
}

func TestSchemaOneWaitIsRemovedByDropWait(t *testing.T) {
	e := newEnv(t)
	e.writeLedger(schemaOne(waitV1))
	if err := e.ledger.DropWait("o/a", 90); err != nil {
		t.Fatal(err)
	}
	if got := e.snapshot().Waits; len(got) != 0 {
		t.Fatalf("waits = %+v, want the schema 1 wait dropped", got)
	}
}

// Confirm, Release and DropWait are not wait sweeps: a dead owner's record stays
// until Admit, Reconcile or Wait runs, or it expires.
func TestConfirmAndReleaseLeaveADeadOwnersWaitAlone(t *testing.T) {
	e := newEnv(t)
	owner := e.supervisor(6001, "50")
	e.waitAs(owner, KindCIRepair, "o/a", 90)
	held := e.mustAdmit(e.req(KindImplement, "o/b", 1))
	other := e.mustAdmit(e.req(KindImplement, "o/c", 2))
	e.killProc(owner)

	e.confirm(held)
	if err := e.ledger.Release(other); err != nil {
		t.Fatal(err)
	}

	if got := e.snapshot().Waits; len(got) != 1 {
		t.Fatalf("waits = %+v, want the wait untouched by Confirm and Release", got)
	}
}

func TestNewLedgerWritesSchemaTwoWithTheTwoOwnerWaitKeys(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.wait(KindCIRepair, "o/a", 90, "concurrency cap reached")

	var file struct {
		Schema  int                          `json:"schema"`
		Entries []map[string]json.RawMessage `json:"entries"`
		Waits   []map[string]json.RawMessage `json:"waits"`
	}
	if err := json.Unmarshal([]byte(e.readLedger()), &file); err != nil {
		t.Fatal(err)
	}
	if file.Schema != 2 {
		t.Errorf("schema = %d, want 2", file.Schema)
	}
	wantWait := []string{"repo", "ticket", "kind", "reason", "since", "refreshedAt", "expiresAt", "ownerPid", "ownerStart"}
	if len(file.Waits) != 1 || len(file.Waits[0]) != len(wantWait) {
		t.Fatalf("waits = %v, want one wait with exactly the keys %v", file.Waits, wantWait)
	}
	for _, k := range wantWait {
		if _, ok := file.Waits[0][k]; !ok {
			t.Errorf("wait has no %q key", k)
		}
	}
	if string(file.Waits[0]["ownerPid"]) != strconv.Itoa(testLauncher) || string(file.Waits[0]["ownerStart"]) != `"`+launcherBoot+`"` {
		t.Errorf("owner keys = %s/%s, want %d/%q", file.Waits[0]["ownerPid"], file.Waits[0]["ownerStart"], testLauncher, launcherBoot)
	}
	// Schema 2 grows only the wait: an entry keeps its fourteen keys.
	if len(file.Entries) != 1 || len(file.Entries[0]) != 14 {
		t.Errorf("entries = %v, want one entry with its 14 keys", file.Entries)
	}
}

func TestSchemaOneLedgerIsReadAsIsAndUpgradedByTheNextWrite(t *testing.T) {
	e := newEnv(t)
	original := `{"schema":1,"entries":[` + entryV1 + `],"waits":[` + waitV1 + `]}`
	path := e.writeLedger(original)

	snap := e.snapshot()
	if len(snap.Entries) != 1 || snap.Entries[0].HolderID != "h" || snap.Entries[0].LauncherPID != 7 || snap.Entries[0].LauncherStart != "12" ||
		snap.Entries[0].State != StateConfirmed || snap.Entries[0].Window != "5-ci-repair" || snap.Entries[0].LaunchID != "L" {
		t.Fatalf("entries = %+v, want the schema 1 entry read unchanged", snap.Entries)
	}
	if len(snap.Waits) != 1 || snap.Waits[0].OwnerPID != 0 || snap.Waits[0].OwnerStart != "" || snap.Waits[0].Reason != "concurrency cap reached" {
		t.Fatalf("waits = %+v, want the schema 1 wait read with no owner", snap.Waits)
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatalf("a read rewrote the schema 1 file: %s", data)
	}

	e.mustAdmit(e.req(KindImplement, "o/c", 1)) // the next write

	upgraded := e.readLedger()
	if !strings.HasPrefix(upgraded, `{"schema":2,`) {
		t.Fatalf("ledger after the write = %s, want schema 2", upgraded)
	}
	after := e.snapshot()
	if len(after.Entries) != 2 || after.Entries[0] != snap.Entries[0] {
		t.Fatalf("entries = %+v, want the schema 1 entry kept unchanged beside the new one", after.Entries)
	}
	if len(after.Waits) != 1 || after.Waits[0] != snap.Waits[0] {
		t.Fatalf("waits = %+v, want the schema 1 wait kept (ownerPid 0)", after.Waits)
	}
	if !strings.Contains(upgraded, `"ownerPid":0,"ownerStart":""`) {
		t.Fatalf("ledger = %s, want the upgraded wait to carry ownerPid 0 and an empty ownerStart", upgraded)
	}
}

func TestSchemaTwoLedgerWithAnOwnedWaitReadsBack(t *testing.T) {
	e := newEnv(t)
	e.writeLedger(schemaTwo(waitV2("4242", `"100"`)))
	w := e.snapshot().Waits
	if len(w) != 1 || w[0].OwnerPID != 4242 || w[0].OwnerStart != "100" {
		t.Fatalf("waits = %+v, want the owner read back", w)
	}
}

// ---- release rules ---------------------------------------------------------

func TestReservedEntryWithLiveLauncherIsKeptEvenWhenNoWindowExists(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	if released := e.reconcile(e.inv()); len(released) != 0 {
		t.Fatalf("released = %+v: a missing window while the launcher is live is not proof of death", released)
	}
	if len(e.held()) != 1 {
		t.Fatal("the reservation was lost")
	}
}

func TestReservedEntryIsReleasedOnceTheLauncherIsDeadAndAnInventoryShowsNoWindow(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.killProc(testLauncher)
	if released := e.reconcile(e.inv()); len(released) != 1 {
		t.Fatalf("released = %+v, want the crashed launcher's reservation", released)
	}
	if got := e.held(); len(got) != 0 {
		t.Fatalf("entries = %+v, want none", got)
	}
}

func TestReservedEntryWithADeadLauncherKeepsHoldingWhileItsWindowIsAlive(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.killProc(testLauncher)
	if released := e.reconcile(e.inv(live("1-implement", "/work/o/a"))); len(released) != 0 {
		t.Fatalf("released = %+v: the spawn may have succeeded, the window is live", released)
	}
}

func TestLauncherPIDReusedByAnotherProcessCountsAsDead(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.setProc(testLauncher, "999999") // same pid, different start time
	if released := e.reconcile(e.inv()); len(released) != 1 {
		t.Fatalf("released = %+v, want the entry released: a reused pid is a dead launcher", released)
	}
}

func TestLauncherWithoutAStartTimeFallsBackToLiveness(t *testing.T) {
	e := newEnv(t)
	e.setProc(testLauncher, "") // a platform with no start time
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	if got := e.held()[0].LauncherStart; got != "" {
		t.Fatalf("LauncherStart = %q, want empty", got)
	}
	if released := e.reconcile(e.inv()); len(released) != 0 {
		t.Fatalf("released = %+v: a live launcher with no start time is alive", released)
	}
	e.killProc(testLauncher)
	if released := e.reconcile(e.inv()); len(released) != 1 {
		t.Fatalf("released = %+v, want the dead launcher's entry released", released)
	}
}

func TestConfirmedEntryIsReleasedWithoutLookingAtTheLauncher(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.confirm(p)
	// The launcher is alive (a long-lived daemon), but the worker is gone.
	if released := e.reconcile(e.inv()); len(released) != 1 {
		t.Fatalf("released = %+v, want the confirmed entry released by the inventory alone", released)
	}
}

func TestConfirmedEntryWithAliveWindowIsKeptEvenWhenTheLauncherIsDead(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.confirm(p)
	e.killProc(testLauncher)
	if released := e.reconcile(e.inv(live("1-implement", "/work/o/a"))); len(released) != 0 {
		t.Fatalf("released = %+v, want the live worker held", released)
	}
}

func TestDeadWorkerPaneReleasesOnTheNextCompleteInventory(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.confirm(p)
	if released := e.reconcile(e.inv(dead("1-implement", "/work/o/a"))); len(released) != 1 {
		t.Fatalf("released = %+v, want the dead-pane worker released", released)
	}
}

func TestWindowMatchingUsesTheRepoDirectory(t *testing.T) {
	e := newEnv(t)
	e.machine = 10
	a := e.mustAdmit(e.req(KindImplement, "o/a", 42))
	b := e.mustAdmit(e.req(KindImplement, "o/b", 42))
	e.confirm(a)
	e.confirm(b)

	// Both repos own ticket 42: only repo b's window exists.
	released := e.reconcile(e.inv(live("42-implement", "/work/o/b")))
	if len(released) != 1 || released[0].Repo != "o/a" {
		t.Fatalf("released = %+v, want only repo a's entry", released)
	}
	if got := e.held(); len(got) != 1 || got[0].Repo != "o/b" {
		t.Fatalf("entries = %+v, want repo b's entry kept", got)
	}
}

func TestWindowWithoutARepoDirMatchesConservatively(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.confirm(p)
	if released := e.reconcile(e.inv(live("1-implement", ""))); len(released) != 0 {
		t.Fatalf("released = %+v: a live window with no recorded dir may be ours", released)
	}
	// Cleaned paths are compared.
	if released := e.reconcile(e.inv(live("1-implement", "/work//o/a/"))); len(released) != 0 {
		t.Fatalf("released = %+v: a cleaned-equal dir is the same window", released)
	}
}

func TestIncompleteInventoryAndUnreadSocketsKeepEveryEntry(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.confirm(p)

	incomplete := Inventory{testSocket: {Complete: false, ReadAt: e.clock()}}
	if released := e.reconcile(incomplete); len(released) != 0 {
		t.Fatalf("released = %+v on an incomplete inventory (dropped rows, timeout or malformed listing)", released)
	}
	if released := e.reconcile(Inventory{}); len(released) != 0 {
		t.Fatalf("released = %+v for a socket that was not read", released)
	}
	other := Inventory{"/another/socket": {Complete: true, ReadAt: e.clock()}}
	if released := e.reconcile(other); len(released) != 0 {
		t.Fatalf("released = %+v: another socket's inventory proves nothing", released)
	}
	if len(e.held()) != 1 {
		t.Fatal("an entry was lost")
	}
}

func TestInventoryReadBeforeTheEntryLastChangedProvesNothing(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.advance(time.Second)
	e.confirm(p)

	stale := Inventory{testSocket: {Complete: true, ReadAt: e.clock().Add(-500 * time.Millisecond)}}
	if released := e.reconcile(stale); len(released) != 0 {
		t.Fatalf("released = %+v: the listing predates the Confirm, the window may not have been visible yet", released)
	}
	zero := Inventory{testSocket: {Complete: true}}
	if released := e.reconcile(zero); len(released) != 0 {
		t.Fatalf("released = %+v: an inventory with no read time proves nothing", released)
	}
	if released := e.reconcile(e.inv()); len(released) != 1 {
		t.Fatalf("released = %+v, want release on a fresh complete inventory", released)
	}
}

func TestAdmitAppliesTheReleaseRulesToTheCallersInventoryFirst(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.confirm(p)
	e.mustRefuse(e.req(KindImplement, "o/a", 2), ReasonMachineCap)

	req := e.req(KindImplement, "o/a", 2)
	req.Obs.Inventory = e.inv() // the worker is gone
	e.mustAdmit(req)
	if got := e.held(); len(got) != 1 || got[0].Ticket != 2 {
		t.Fatalf("entries = %+v, want only the new admission", got)
	}
}

func TestAdmitKeepsEntriesWhenTheCallersInventoryIsIncomplete(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	e.confirm(p)
	req := e.req(KindImplement, "o/a", 2)
	req.Obs.Inventory = Inventory{testSocket: {Complete: false, ReadAt: e.clock()}}
	e.mustRefuse(req, ReasonMachineCap)
}

func TestSnapshotSocketsListsEachEntrySocketOnce(t *testing.T) {
	e := newEnv(t)
	e.machine = 10
	for i := 1; i <= 3; i++ {
		e.confirm(e.mustAdmit(e.req(KindImplement, "o/a", i)))
	}
	other := e.req(KindImplement, "o/b", 9)
	other.Socket = "/other/socket"
	p := e.mustAdmit(other)
	if err := e.ledger.Confirm(p, Confirmation{Socket: "/other/socket", Window: "9-implement", Dir: "/work/o/b"}); err != nil {
		t.Fatal(err)
	}
	got := e.snapshot().Sockets()
	if len(got) != 2 || got[0] != "/other/socket" || got[1] != testSocket {
		t.Fatalf("Sockets = %v, want [/other/socket %s]", got, testSocket)
	}
}

// ---- adoption --------------------------------------------------------------

func TestRestartedSupervisorAdoptsItsOwnHeldEntry(t *testing.T) {
	e := newEnv(t)
	first := e.req(KindCIRepair, "o/a", 90)
	p := e.mustAdmit(first)
	e.killProc(testLauncher) // the supervisor died after Admit

	const restarted = 5151
	e.setProc(restarted, "777")
	again := e.req(KindCIRepair, "o/a", 90)
	again.Launcher = restarted
	again.Adopt = true
	d := e.admit(again)
	if !d.Admitted || !d.Permit.Adopted || d.Permit.HolderID != p.HolderID {
		t.Fatalf("Admit = %+v, want the original entry adopted", d)
	}
	got := e.held()
	if len(got) != 1 || got[0].LauncherPID != restarted || got[0].LauncherStart != "777" {
		t.Fatalf("entries = %+v, want one entry now owned by the restarted launcher", got)
	}
}

func TestAdoptionNeedsANonAdoptingCallerToBeRefused(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	e.killProc(testLauncher)
	again := e.req(KindCIRepair, "o/a", 90)
	again.Launcher = 5151
	e.setProc(5151, "777")
	e.mustRefuse(again, ReasonWorkerReserved)
}

func TestAdoptionNeverStealsFromALiveForeignLauncher(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	other := e.req(KindCIRepair, "o/a", 90)
	other.Launcher = 5151
	other.Adopt = true
	e.setProc(5151, "777")
	e.mustRefuse(other, ReasonWorkerReserved)
}

func TestAdoptionRequiresTheSameKind(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	e.killProc(testLauncher)
	other := e.req(KindMergeRepair, "o/a", 90)
	other.Launcher = 5151
	other.Adopt = true
	e.setProc(5151, "777")
	e.mustRefuse(other, ReasonWorkerReserved)
}

func TestAdoptionDoesNotNeedAFreeSlot(t *testing.T) {
	e := newEnv(t)
	e.machine = 1
	p := e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	e.killProc(testLauncher)
	e.setProc(5151, "777")
	again := e.req(KindCIRepair, "o/a", 90)
	again.Launcher = 5151
	again.Adopt = true
	d := e.admit(again)
	if !d.Admitted || d.Permit.HolderID != p.HolderID {
		t.Fatalf("Admit = %+v, want the entry that already holds the only slot adopted", d)
	}
}

// ---- restoring an adopted entry ---------------------------------------------

const adopter = 5151

// adopt has a supervisor that died after Admit replaced by a restarted one
// (adopter, start time 777), and returns the adopter's permit. The predecessor
// was launched by testLauncher (start time launcherBoot).
func (e *testEnv) adopt(kind Kind, repo string, ticket int) Permit {
	e.t.Helper()
	e.killProc(testLauncher)
	e.setProc(adopter, "777")
	req := e.req(kind, repo, ticket)
	req.Launcher, req.Adopt = adopter, true
	d := e.admit(req)
	if !d.Admitted || !d.Permit.Adopted {
		e.t.Fatalf("Admit = %+v, want the predecessor's entry adopted", d)
	}
	return d.Permit
}

func (e *testEnv) restore(pid int, p Permit) error {
	e.t.Helper()
	var err error
	e.as(pid, func() { err = e.ledger.Restore(p) })
	return err
}

func TestAdoptedPermitCarriesThePredecessorsLauncherInMemory(t *testing.T) {
	e := newEnv(t)
	first := e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	if first.PrevLauncherPID != 0 || first.PrevLauncherStart != "" {
		t.Fatalf("a fresh permit = %+v, want no pre-adoption launcher", first)
	}
	adopted := e.adopt(KindCIRepair, "o/a", 90)
	if adopted.PrevLauncherPID != testLauncher || adopted.PrevLauncherStart != launcherBoot {
		t.Fatalf("permit = %+v, want the pre-adoption launcher %d/%q", adopted, testLauncher, launcherBoot)
	}
	if got := e.held()[0]; got.LauncherPID != adopter || got.LauncherStart != "777" {
		t.Fatalf("entry = %+v, want the adopter as its launcher", got)
	}
}

func TestRestoreReturnsAConfirmedPredecessorToItsPreAdoptionState(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	e.confirm(p)
	before := e.held()[0]
	e.advance(time.Minute)
	permit := e.adopt(KindCIRepair, "o/a", 90)
	if permit.State != StateConfirmed {
		t.Fatalf("Permit.State = %q, want confirmed", permit.State)
	}
	e.advance(time.Minute)
	staleRead := e.inv() // read before the restore, shows no window
	e.advance(time.Second)

	if err := e.restore(adopter, permit); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got := e.held()
	if len(got) != 1 {
		t.Fatalf("entries = %+v, want the entry kept", got)
	}
	g := got[0]
	if g.State != StateConfirmed || g.LauncherPID != testLauncher || g.LauncherStart != launcherBoot {
		t.Errorf("entry = %+v, want confirmed under the predecessor's launcher %d/%q", g, testLauncher, launcherBoot)
	}
	if g.HolderID != before.HolderID || g.Socket != before.Socket || g.Window != before.Window || g.Dir != before.Dir ||
		g.LaunchID != before.LaunchID || g.Branch != before.Branch || g.Kind != before.Kind || !g.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("entry = %+v, want every other field as it was: %+v", g, before)
	}
	if !g.UpdatedAt.Equal(e.clock()) {
		t.Errorf("UpdatedAt = %s, want the restore time %s", g.UpdatedAt, e.clock())
	}
	// The restore voids an inventory read before it: it cannot release the worker.
	if released := e.reconcile(staleRead); len(released) != 0 {
		t.Fatalf("an inventory read before the restore released %+v", released)
	}
	// A complete inventory read after it frees the entry, the confirmed rule.
	e.advance(time.Second)
	if released := e.reconcile(e.inv()); len(released) != 1 {
		t.Fatalf("released = %+v, want the restored confirmed entry freed by a later complete inventory", released)
	}
}

func TestRestoreReturnsAReservedPredecessorToItsDeadLauncher(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	permit := e.adopt(KindCIRepair, "o/a", 90)
	if permit.State != StateReserved {
		t.Fatalf("Permit.State = %q, want reserved", permit.State)
	}
	e.advance(time.Minute)

	if err := e.restore(adopter, permit); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	g := e.held()[0]
	if g.State != StateReserved || g.LauncherPID != testLauncher || g.LauncherStart != launcherBoot {
		t.Fatalf("entry = %+v, want reserved under the predecessor's launcher %d/%q", g, testLauncher, launcherBoot)
	}
	// Its launcher is dead and no window exists: the existing release rules free it.
	e.advance(time.Second)
	if released := e.reconcile(e.inv()); len(released) != 1 {
		t.Fatalf("released = %+v, want the restored reserved entry freed (dead launcher, no window)", released)
	}
}

func TestRestoreOfAnEntryThatIsGoneIsNotHeldAndCreatesNothing(t *testing.T) {
	e := newEnv(t)
	err := e.restore(testLauncher, Permit{HolderID: "gone", Kind: KindCIRepair, Repo: "o/a", Ticket: 90, Adopted: true, State: StateConfirmed, PrevLauncherPID: 7, PrevLauncherStart: "12"})
	if !errors.Is(err, ErrNotHeld) {
		t.Fatalf("Restore = %v, want ErrNotHeld", err)
	}
	if _, serr := os.Stat(filepath.Join(e.dir, "ledger.json")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("Restore created the ledger: %v", serr)
	}

	// With the ledger present, a holder that is not in it is the same answer and
	// adds no entry.
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	if err := e.restore(testLauncher, Permit{HolderID: "gone", Adopted: true, State: StateReserved, PrevLauncherPID: 7}); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("Restore = %v, want ErrNotHeld", err)
	}
	if got := e.held(); len(got) != 1 {
		t.Fatalf("entries = %+v, want only the original entry", got)
	}
}

func TestRestoreByAProcessThatDidNotLaunchTheEntryChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	permit := e.adopt(KindCIRepair, "o/a", 90)
	before := e.held()[0]
	e.setProc(7777, "5")
	e.advance(time.Minute)

	if err := e.restore(7777, permit); err != nil {
		t.Fatalf("Restore by another process = %v, want nil and no change", err)
	}
	if got := e.held()[0]; got != before {
		t.Fatalf("entry = %+v, want it untouched: %+v", got, before)
	}
	// A reused pid is not the launcher either.
	e.setProc(adopter, "888888")
	if err := e.restore(adopter, permit); err != nil {
		t.Fatalf("Restore by a reused pid = %v, want nil and no change", err)
	}
	if got := e.held()[0]; got != before {
		t.Fatalf("entry = %+v, want it untouched by a reused pid: %+v", got, before)
	}
}

func TestRestoreLeavesAnEntryTheAdopterAlreadyConfirmed(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	permit := e.adopt(KindCIRepair, "o/a", 90)
	e.advance(time.Second)
	if err := e.ledger.Confirm(permit, Confirmation{Socket: testSocket, Window: windowName(KindCIRepair, 90), Dir: repoDir("o/a")}); err != nil {
		t.Fatal(err)
	}
	before := e.held()[0]
	e.advance(time.Second)

	if err := e.restore(adopter, permit); err != nil {
		t.Fatalf("Restore = %v, want nil and no change: the entry is no longer reserved", err)
	}
	if got := e.held()[0]; got != before {
		t.Fatalf("entry = %+v, want the confirmed entry untouched: %+v", got, before)
	}
}

func TestRestoreRefusesAPermitThatWasNotAdopted(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
	before := e.held()[0]

	if err := e.restore(testLauncher, p); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Restore of a fresh permit = %v, want ErrInvalidRequest", err)
	}
	// An adopted permit that cannot name a readable pre-adoption state would write
	// an entry the ledger could not read back.
	adopted := Permit{HolderID: p.HolderID, Adopted: true, State: StateReserved, PrevLauncherPID: 7}
	for name, bad := range map[string]Permit{
		"no launcher pid":       {HolderID: p.HolderID, Adopted: true, State: StateReserved},
		"launcher pid past int": {HolderID: p.HolderID, Adopted: true, State: StateReserved, PrevLauncherPID: math.MaxInt32 + 1},
		"unknown state":         {HolderID: p.HolderID, Adopted: true, State: "limbo", PrevLauncherPID: 7},
		"unclean start":         {HolderID: p.HolderID, Adopted: true, State: StateReserved, PrevLauncherPID: 7, PrevLauncherStart: "1\n2"},
	} {
		if err := e.restore(testLauncher, bad); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("Restore(%s) = %v, want ErrInvalidRequest", name, err)
		}
	}
	if got := e.held()[0]; got != before {
		t.Fatalf("entry = %+v, want it untouched: %+v", got, before)
	}
	if err := e.restore(testLauncher, adopted); err != nil {
		t.Fatalf("Restore of a well-formed adopted permit = %v", err)
	}
}

// ---- failure modes ---------------------------------------------------------

func TestUnreadableLedgerAdmitsNothingAndIsNeverReset(t *testing.T) {
	good := `{"schema":1,"entries":[],"waits":[]}`
	entry := `{"holderId":"h","kind":"implement","repo":"o/a","ticket":1,"branch":"","launcherPid":1,"launcherStart":"","socket":"","window":"","dir":"","launchId":"","state":"reserved","createdAt":"2026-11-20T09:00:00Z","updatedAt":"2026-11-20T09:00:00Z"}`
	withEntry := func(e string) string { return `{"schema":1,"entries":[` + e + `],"waits":[]}` }
	cases := map[string]struct {
		content string
		want    error
	}{
		"not json":                  {`not json`, ErrCorrupt},
		"empty file":                {``, ErrCorrupt},
		"json null":                 {`null`, ErrCorrupt},
		"trailing data":             {good + `{}`, ErrCorrupt},
		"missing schema":            {`{"entries":[],"waits":[]}`, ErrCorrupt},
		"null schema":               {`{"schema":null,"entries":[],"waits":[]}`, ErrCorrupt},
		"string schema":             {`{"schema":"1","entries":[],"waits":[]}`, ErrCorrupt},
		"unknown schema":            {`{"schema":3,"entries":[],"waits":[]}`, ErrUnknownSchema},
		"schema zero":               {`{"schema":0,"entries":[],"waits":[]}`, ErrUnknownSchema},
		"negative schema":           {`{"schema":-1,"entries":[],"waits":[]}`, ErrUnknownSchema},
		"unknown schema other keys": {`{"schema":9,"slots":[]}`, ErrUnknownSchema},
		"missing entries":           {`{"schema":1,"waits":[]}`, ErrCorrupt},
		"null entries":              {`{"schema":1,"entries":null,"waits":[]}`, ErrCorrupt},
		"missing waits":             {`{"schema":1,"entries":[]}`, ErrCorrupt},
		"null waits":                {`{"schema":1,"entries":[],"waits":null}`, ErrCorrupt},
		"extra top-level key":       {`{"schema":1,"entries":[],"waits":[],"x":1}`, ErrCorrupt},
		"entry extra key":           {withEntry(strings.Replace(entry, `"repo"`, `"extra":1,"repo"`, 1)), ErrCorrupt},
		"entry missing key":         {withEntry(strings.Replace(entry, `"branch":"",`, ``, 1)), ErrCorrupt},
		"entry null field":          {withEntry(strings.Replace(entry, `"branch":""`, `"branch":null`, 1)), ErrCorrupt},
		"entry bad kind":            {withEntry(strings.Replace(entry, `"implement"`, `"bogus"`, 1)), ErrCorrupt},
		"entry bad state":           {withEntry(strings.Replace(entry, `"reserved"`, `"limbo"`, 1)), ErrCorrupt},
		"entry zero ticket":         {withEntry(strings.Replace(entry, `"ticket":1`, `"ticket":0`, 1)), ErrCorrupt},
		"entry empty repo":          {withEntry(strings.Replace(entry, `"o/a"`, `""`, 1)), ErrCorrupt},
		"entry empty holder":        {withEntry(strings.Replace(entry, `"holderId":"h"`, `"holderId":""`, 1)), ErrCorrupt},
		"entry bad time":            {withEntry(strings.Replace(entry, `"createdAt":"2026-11-20T09:00:00Z"`, `"createdAt":"soon"`, 1)), ErrCorrupt},
		"duplicate holder":          {withEntry(entry + `,` + entry), ErrCorrupt},
		"confirmed without window":  {withEntry(strings.Replace(entry, `"reserved"`, `"confirmed"`, 1)), ErrCorrupt},
		"wait missing key":          {`{"schema":1,"entries":[],"waits":[{"repo":"o/a"}]}`, ErrCorrupt},
		// Schema 2 waits carry exactly nine keys, schema 1 waits exactly seven.
		"schema 2 wait with the 7 schema 1 keys": {schemaTwo(waitV1), ErrCorrupt},
		"schema 1 wait with the 9 schema 2 keys": {schemaOne(waitV2("4242", `"100"`)), ErrCorrupt},
		"schema 2 wait missing ownerStart":       {schemaTwo(strings.Replace(waitV2("4242", `"100"`), `,"ownerStart":"100"`, ``, 1)), ErrCorrupt},
		"schema 2 negative ownerPid":             {schemaTwo(waitV2("-4", `"100"`)), ErrCorrupt},
		"schema 2 ownerPid past int32":           {schemaTwo(waitV2("2147483648", `"100"`)), ErrCorrupt},
		"schema 2 fractional ownerPid":           {schemaTwo(waitV2("4.5", `"100"`)), ErrCorrupt},
		"schema 2 string ownerPid":               {schemaTwo(waitV2(`"4242"`, `"100"`)), ErrCorrupt},
		"schema 2 null ownerPid":                 {schemaTwo(waitV2("null", `"100"`)), ErrCorrupt},
		"schema 2 numeric ownerStart":            {schemaTwo(waitV2("4242", `100`)), ErrCorrupt},
		"schema 2 null ownerStart":               {schemaTwo(waitV2("4242", `null`)), ErrCorrupt},
		"schema 2 ownerStart with a newline":     {schemaTwo(waitV2("4242", `"1\n0"`)), ErrCorrupt},
		"schema 2 over-long ownerStart":          {schemaTwo(waitV2("4242", `"`+strings.Repeat("9", 600)+`"`)), ErrCorrupt},
		// Strings the ledger writes back into log and status lines.
		"entry newline in repo":         {withEntry(strings.Replace(entry, `"o/a"`, `"o/a\nreconcile: forged"`, 1)), ErrCorrupt},
		"entry control char in repo":    {withEntry(strings.Replace(entry, `"o/a"`, `"o/a\u001b[2J"`, 1)), ErrCorrupt},
		"entry newline in branch":       {withEntry(strings.Replace(entry, `"branch":""`, `"branch":"b\nx"`, 1)), ErrCorrupt},
		"entry control char in window":  {withEntry(strings.Replace(entry, `"window":""`, `"window":"7-ci\u0007"`, 1)), ErrCorrupt},
		"entry newline in socket":       {withEntry(strings.Replace(entry, `"socket":""`, `"socket":"/s\n/t"`, 1)), ErrCorrupt},
		"entry newline in dir":          {withEntry(strings.Replace(entry, `"dir":""`, `"dir":"/d\n/e"`, 1)), ErrCorrupt},
		"entry over-long repo":          {withEntry(strings.Replace(entry, `"o/a"`, `"o/`+strings.Repeat("a", 600)+`"`, 1)), ErrCorrupt},
		"entry over-long branch":        {withEntry(strings.Replace(entry, `"branch":""`, `"branch":"`+strings.Repeat("b", 600)+`"`, 1)), ErrCorrupt},
		"entry zero launcher pid":       {withEntry(strings.Replace(entry, `"launcherPid":1`, `"launcherPid":0`, 1)), ErrCorrupt},
		"entry negative launcher pid":   {withEntry(strings.Replace(entry, `"launcherPid":1`, `"launcherPid":-3`, 1)), ErrCorrupt},
		"entry launcher pid past int32": {withEntry(strings.Replace(entry, `"launcherPid":1`, `"launcherPid":2147483648`, 1)), ErrCorrupt},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			path := e.writeLedger(tc.content)
			_, err := e.ledger.Admit(e.req(KindImplement, "o/a", 1))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Admit error = %v, want %v", err, tc.want)
			}
			assertPathFree(t, err, e.dir)
			if _, err := e.ledger.Peek(); !errors.Is(err, tc.want) {
				t.Errorf("Peek error = %v, want %v", err, tc.want)
			}
			if err := e.ledger.Wait(WaitRequest{Kind: KindCIRepair, Repo: "o/a", Ticket: 9, Reason: "x", Interval: time.Second}); !errors.Is(err, tc.want) {
				t.Errorf("Wait error = %v, want %v", err, tc.want)
			}
			if _, err := e.ledger.Reconcile(e.inv()); !errors.Is(err, tc.want) {
				t.Errorf("Reconcile error = %v, want %v", err, tc.want)
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil || string(data) != tc.content {
				t.Fatalf("the unreadable ledger was rewritten: %q (%v), want it untouched", data, rerr)
			}
		})
	}
}

func TestValidSchemaOneEntryDecodes(t *testing.T) {
	e := newEnv(t)
	e.writeLedger(`{"schema":1,"entries":[{"holderId":"h","kind":"ci-repair","repo":"o/a","ticket":5,"branch":"b","launcherPid":7,"launcherStart":"12","socket":"s","window":"5-ci-repair","dir":"/d","launchId":"L","state":"confirmed","createdAt":"2026-11-20T09:00:00Z","updatedAt":"2026-11-20T09:00:01Z"}],"waits":[]}`)
	got := e.held()
	want := Entry{HolderID: "h", Kind: KindCIRepair, Repo: "o/a", Ticket: 5, Branch: "b", LauncherPID: 7, LauncherStart: "12", Socket: "s", Window: "5-ci-repair", Dir: "/d", LaunchID: "L", State: StateConfirmed}
	if len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
	g := got[0]
	g.CreatedAt, g.UpdatedAt = time.Time{}, time.Time{}
	if g != want {
		t.Fatalf("entry = %+v, want %+v", g, want)
	}
}

func TestLedgerOverTheSizeBoundIsRefusedNeverTruncated(t *testing.T) {
	e := newEnv(t)
	e.machine = 100000
	now := e.clock()
	state := ledgerState{Schema: 1, Waits: []Wait{}}
	for i := 1; i <= maxEntries; i++ {
		state.Entries = append(state.Entries, Entry{
			HolderID: fmt.Sprintf("h%d", i), Kind: KindImplement, Repo: "o/a", Ticket: i, LauncherPID: 1,
			State: StateReserved, CreatedAt: now, UpdatedAt: now,
		})
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := e.writeLedger(string(data))

	if _, err := e.ledger.Admit(e.req(KindImplement, "o/a", maxEntries+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Admit at the bound = %v, want ErrTooLarge", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(data) {
		t.Fatal("the ledger at its bound was modified")
	}

	state.Entries = append(state.Entries, Entry{
		HolderID: "extra", Kind: KindImplement, Repo: "o/a", Ticket: maxEntries + 1, LauncherPID: 1,
		State: StateReserved, CreatedAt: now, UpdatedAt: now,
	})
	over, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	e.writeLedger(string(over))
	if _, err := e.ledger.Peek(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Peek of %d entries = %v, want ErrTooLarge", maxEntries+1, err)
	}
}

func TestLedgerFileOverTheReadLimitIsNotRegularBounded(t *testing.T) {
	e := newEnv(t)
	e.writeLedger(strings.Repeat("x", 300<<10))
	if _, err := e.ledger.Peek(); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Peek = %v, want ErrNotRegular", err)
	}
	_, err := e.ledger.Admit(e.req(KindImplement, "o/a", 1))
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Admit = %v, want ErrNotRegular", err)
	}
}

func TestSymlinkedLedgerFileIsRefusedAndNothingIsWrittenThrough(t *testing.T) {
	e := newEnv(t)
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	const content = `{"schema":1,"entries":[],"waits":[]}`
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(e.dir, "ledger.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := e.ledger.Admit(e.req(KindImplement, "o/a", 1))
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Admit = %v, want ErrNotRegular", err)
	}
	assertPathFree(t, err, e.dir)
	if data, _ := os.ReadFile(target); string(data) != content {
		t.Fatalf("the symlink target was written through: %q", data)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced (%v, %v)", info, err)
	}
}

func TestSymlinkedCapacityDirectoryIsRefusedAndNothingIsWrittenThrough(t *testing.T) {
	_, parent, dir := storeDirs(t)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	realDir := t.TempDir()
	if err := os.Symlink(realDir, dir); err != nil {
		t.Fatal(err)
	}
	l := NewLedger(dir, Options{LockTimeout: 100 * time.Millisecond})
	req := Request{Kind: KindImplement, Repo: "o/a", Ticket: 1, Launcher: 1, Limits: Limits{Machine: 3}}
	_, err := l.Admit(req)
	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("Admit = %v, want ErrUntrusted", err)
	}
	assertPathFree(t, err, dir)
	if _, err := l.Peek(); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("Peek = %v, want ErrUntrusted", err)
	}
	if entries, _ := os.ReadDir(realDir); len(entries) != 0 {
		t.Fatalf("files were written through the symlinked directory: %v", entries)
	}
}

func TestLockTimeoutRefusesAdmissionAndLeavesTheLedgerUntouched(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindImplement, "o/a", 1))
	before := e.readLedger()

	release := make(chan struct{})
	holding := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.ledger.store.WithLock(func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	start := time.Now()
	_, err := e.ledger.Admit(e.req(KindImplement, "o/b", 2))
	elapsed := time.Since(start)
	close(release)
	if herr := <-done; herr != nil {
		t.Fatal(herr)
	}
	if !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("Admit = %v, want ErrLockUnavailable", err)
	}
	assertPathFree(t, err, e.dir)
	if elapsed < 150*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("lock wait = %s, want about the 200ms test timeout", elapsed)
	}
	if after := e.readLedger(); after != before {
		t.Fatal("the ledger changed while the lock was unavailable")
	}
}

// ---- review round 1 ----------------------------------------------------------

// A ledger that exists is never modified unlocked: with `.lock` gone, the cleanup
// operations take the lock (recreating the file) instead of racing an admission.
// With no ledger there is nothing to protect and nothing is created.
func TestCleanupOperationsStillLockWhenTheLockFileIsGoneButTheLedgerExists(t *testing.T) {
	ops := map[string]func(e *testEnv, p Permit) error{
		"Reconcile": func(e *testEnv, _ Permit) error { _, err := e.ledger.Reconcile(e.inv()); return err },
		"Release":   func(e *testEnv, p Permit) error { return e.ledger.Release(p) },
		"DropWait":  func(e *testEnv, _ Permit) error { return e.ledger.DropWait("o/a", 1) },
		"Confirm": func(e *testEnv, p Permit) error {
			return e.ledger.Confirm(p, Confirmation{Socket: testSocket, Window: windowName(p.Kind, p.Ticket), Dir: repoDir(p.Repo)})
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
			lock := filepath.Join(e.dir, ".lock")
			if err := os.Remove(lock); err != nil {
				t.Fatal(err)
			}

			if err := op(e, p); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			if _, err := os.Lstat(lock); err != nil {
				t.Fatalf("%s ran without taking the lock: %v", name, err)
			}
		})
	}

	t.Run("serializes with a concurrent holder once the lock file is back", func(t *testing.T) {
		e := newEnv(t)
		e.mustAdmit(e.req(KindImplement, "o/a", 1))
		if err := os.Remove(filepath.Join(e.dir, ".lock")); err != nil {
			t.Fatal(err)
		}
		release, holding, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			done <- e.ledger.store.WithLock(func() error {
				close(holding)
				<-release
				return nil
			})
		}()
		<-holding
		_, err := e.ledger.Reconcile(e.inv())
		close(release)
		if herr := <-done; herr != nil {
			t.Fatal(herr)
		}
		if !errors.Is(err, ErrLockUnavailable) {
			t.Fatalf("Reconcile beside a lock holder = %v, want ErrLockUnavailable", err)
		}
	})

	t.Run("no ledger means no lock file and no ledger", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.ledger.Reconcile(e.inv()); err != nil {
			t.Fatal(err)
		}
		if err := e.ledger.DropWait("o/a", 1); err != nil {
			t.Fatal(err)
		}
		if got := listDir(t, e.dir); len(got) != 0 {
			t.Fatalf("cleanup paths created %v in an empty store", got)
		}
	})
}

// A request whose strings could never be read back is refused up front, so an
// admission cannot make the ledger unreadable for everyone else.
func TestAdmitRefusesRequestStringsTheLedgerCouldNotReadBack(t *testing.T) {
	for name, mutate := range map[string]func(r *Request){
		"newline in repo":     func(r *Request) { r.Repo = "o/a\nforged" },
		"control char branch": func(r *Request) { r.Branch = "b\x07" },
		"newline in window":   func(r *Request) { r.Window = "1-implement\nx" },
		"newline in socket":   func(r *Request) { r.Socket = "/s\n/t" },
		"newline in dir":      func(r *Request) { r.Dir = "/d\n/e" },
		"over-long branch":    func(r *Request) { r.Branch = strings.Repeat("b", 600) },
		"over-long repo":      func(r *Request) { r.Repo = "o/" + strings.Repeat("a", 600) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			req := e.req(KindImplement, "o/a", 1)
			mutate(&req)
			if _, err := e.ledger.Admit(req); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Admit = %v, want ErrInvalidRequest", err)
			}
			if got := listDir(t, e.dir); len(got) != 0 {
				t.Fatalf("a refused request created %v", got)
			}
		})
	}

	t.Run("confirm", func(t *testing.T) {
		e := newEnv(t)
		p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
		err := e.ledger.Confirm(p, Confirmation{Socket: testSocket, Window: "1-implement\nx", Dir: "/d"})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Confirm = %v, want ErrInvalidRequest", err)
		}
		if got := e.held(); len(got) != 1 || got[0].State != StateReserved {
			t.Fatalf("entries = %+v, want the reservation untouched", got)
		}
	})
}

// Adopting an entry makes the adopter the launcher of a launch in flight: the
// entry is reserved again (so only a dead launcher plus a complete inventory
// frees it) and an inventory read before the adoption proves nothing. Until the
// adopter confirms, another process's inventory cannot release the slot out from
// under the worker being started.
func TestAdoptedConfirmedEntryIsProtectedUntilTheAdopterConfirms(t *testing.T) {
	e := newEnv(t)
	p := e.mustAdmit(e.req(KindCIRepair, "o/a", 90))
	e.confirm(p)
	e.killProc(testLauncher) // the supervisor died; its worker's window is gone
	e.setProc(5151, "777")
	e.advance(time.Minute)
	staleRead := e.inv() // read before the adoption

	e.advance(time.Second)
	again := e.req(KindCIRepair, "o/a", 90)
	again.Launcher, again.Adopt = 5151, true
	d := e.admit(again)
	if !d.Admitted || !d.Permit.Adopted || d.Permit.HolderID != p.HolderID {
		t.Fatalf("Admit = %+v, want the original entry adopted", d)
	}
	if d.Permit.State != StateConfirmed {
		t.Errorf("Permit.State = %q, want it to report how far the original launch got (confirmed)", d.Permit.State)
	}
	if got := e.held(); len(got) != 1 || got[0].State != StateReserved {
		t.Fatalf("entries = %+v, want the adopted entry reserved while the launch is in flight", got)
	}

	if released := e.reconcile(staleRead); len(released) != 0 {
		t.Fatalf("an inventory read before the adoption released %+v", released)
	}
	e.advance(time.Second)
	if released := e.reconcile(e.inv()); len(released) != 0 {
		t.Fatalf("an inventory with no window yet released %+v while the adopter is alive and launching", released)
	}

	e.advance(time.Second)
	if err := e.ledger.Confirm(d.Permit, Confirmation{Socket: testSocket, Window: windowName(KindCIRepair, 90), Dir: repoDir("o/a")}); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Second)
	if released := e.reconcile(e.inv()); len(released) != 1 {
		t.Fatalf("released %+v, want the confirmed entry freed once its window is gone", released)
	}
}

func TestLedgerUsesAFiveSecondLockTimeoutWhileTheQuotaKeepsThree(t *testing.T) {
	dir := trustedDir(t)
	if got := NewLedger(dir, Options{}).store.lockTimeout; got != 5*time.Second {
		t.Errorf("ledger lock timeout = %s, want 5s", got)
	}
	if got := NewDailyQuota(dir).store.lockTimeout; got != 3*time.Second {
		t.Errorf("daily quota lock timeout = %s, want 3s", got)
	}
}

func TestAdmitRejectsAnInvalidRequest(t *testing.T) {
	e := newEnv(t)
	for name, mutate := range map[string]func(*Request){
		"unknown kind": func(r *Request) { r.Kind = "bogus" },
		"empty kind":   func(r *Request) { r.Kind = "" },
		"empty repo":   func(r *Request) { r.Repo = "" },
		"zero ticket":  func(r *Request) { r.Ticket = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			req := e.req(KindImplement, "o/a", 1)
			mutate(&req)
			if _, err := e.ledger.Admit(req); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Admit = %v, want ErrInvalidRequest", err)
			}
		})
	}
	if _, err := os.Lstat(filepath.Join(e.dir, "ledger.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an invalid request created the ledger")
	}
}

func TestAdmittedEntryRecordsTheDecisionsFields(t *testing.T) {
	e := newEnv(t)
	req := e.req(KindMergeRepair, "o/a", 90)
	req.Branch = "feat/x"
	req.LaunchID = "launch-1"
	d := e.admit(req)
	if !d.Admitted || d.Permit.Kind != KindMergeRepair || d.Permit.Repo != "o/a" || d.Permit.Ticket != 90 || d.Permit.HolderID == "" || d.Permit.Adopted {
		t.Fatalf("decision = %+v", d)
	}
	got := e.held()
	if len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
	g := got[0]
	if g.HolderID != d.Permit.HolderID || g.Kind != KindMergeRepair || g.Repo != "o/a" || g.Ticket != 90 || g.Branch != "feat/x" ||
		g.LauncherPID != testLauncher || g.LauncherStart != launcherBoot || g.Socket != testSocket || g.Window != "90-merge-repair" ||
		g.LaunchID != "launch-1" || g.State != StateReserved || !g.CreatedAt.Equal(e.clock()) {
		t.Fatalf("entry = %+v", g)
	}
}

// ---- launcher probe --------------------------------------------------------

func TestParseProcStatStartTimeSurvivesAnAwkwardCommandName(t *testing.T) {
	stat := "4242 (my (weird) cmd) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 100 18446744073709551615"
	start, state, err := parseProcStat([]byte(stat))
	if err != nil || start != "987654" || state != 'S' {
		t.Fatalf("parseProcStat = %q, %q, %v; want start 987654 state S", start, string(state), err)
	}
	for _, bad := range []string{"", "no parens at all", "1 (x)", "1 (x) S 1 2"} {
		if _, _, err := parseProcStat([]byte(bad)); err == nil {
			t.Errorf("parseProcStat(%q) = nil error, want malformed", bad)
		}
	}
}

func TestDefaultProbeSeesOwnProcessAndAFinishedChild(t *testing.T) {
	start, alive := defaultProbe(os.Getpid())
	if !alive {
		t.Fatal("the test process reads as dead")
	}
	if runtime.GOOS == "linux" && start == "" {
		t.Fatal("no start time for the live test process on linux")
	}
	again, _ := defaultProbe(os.Getpid())
	if again != start {
		t.Fatalf("start time changed between reads: %q vs %q", start, again)
	}

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("no true binary: %v", err)
	}
	if _, alive := defaultProbe(cmd.Process.Pid); alive {
		t.Fatal("a reaped child reads as alive")
	}
	if _, alive := defaultProbe(0); alive {
		t.Fatal("pid 0 reads as alive")
	}
	if _, alive := defaultProbe(-5); alive {
		t.Fatal("a negative pid reads as alive")
	}
}

func TestAdmitDefaultsTheLauncherToTheCallingProcess(t *testing.T) {
	dir := trustedDir(t)
	l := NewLedger(dir, Options{})
	req := Request{Kind: KindImplement, Repo: "o/a", Ticket: 1, Limits: Limits{Machine: 1}}
	d, err := l.Admit(req)
	if err != nil || !d.Admitted {
		t.Fatalf("Admit = %+v, %v", d, err)
	}
	snap, err := l.Peek()
	if err != nil || len(snap.Entries) != 1 || snap.Entries[0].LauncherPID != os.Getpid() {
		t.Fatalf("entries = %+v, %v; want the calling pid %d", snap.Entries, err, os.Getpid())
	}
}

// ---- review round 2 ------------------------------------------------------------

func TestIsTransientIsTrueOnlyForABusyOrBrieflyUnreadableStore(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{ErrLockUnavailable, true},
		{ErrIO, true},
		{fmt.Errorf("wrapped: %w", ErrIO), true},
		{ErrInvalidRequest, false},
		{ErrCorrupt, false},
		{ErrUnknownSchema, false},
		{ErrTooLarge, false},
		{ErrNotHeld, false},
		{ErrNotRegular, false},
		{ErrRaced, false},
		{ErrUntrusted, false},
		{nil, false},
		{errors.New("something else"), false},
	} {
		if got := IsTransient(tc.err); got != tc.want {
			t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRetryTransientRunsAFailedOperationOnceMoreOnlyForATransientError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		errs      []error // the error of each successive call
		wantCalls int
		wantErr   error
	}{
		{"success is not repeated", []error{nil}, 1, nil},
		{"a transient failure is retried and the retry's success wins", []error{ErrIO, nil}, 2, nil},
		{"a transient failure that persists is retried exactly once", []error{ErrLockUnavailable, ErrLockUnavailable, nil}, 2, ErrLockUnavailable},
		{"ErrNotHeld is never retried", []error{ErrNotHeld, nil}, 1, ErrNotHeld},
		{"ErrInvalidRequest is never retried", []error{ErrInvalidRequest, nil}, 1, ErrInvalidRequest},
		{"ErrCorrupt is never retried", []error{ErrCorrupt, nil}, 1, ErrCorrupt},
		{"ErrUnknownSchema is never retried", []error{ErrUnknownSchema, nil}, 1, ErrUnknownSchema},
		{"ErrTooLarge is never retried", []error{ErrTooLarge, nil}, 1, ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := RetryTransient(0, func() error {
				calls++
				return tc.errs[min(calls, len(tc.errs))-1]
			})
			if calls != tc.wantCalls || !errors.Is(err, tc.wantErr) && err != tc.wantErr {
				t.Errorf("calls = %d, err = %v; want %d calls and %v", calls, err, tc.wantCalls, tc.wantErr)
			}
		})
	}
}

func TestRetryTransientWaitsThePauseBeforeTheRetryOnly(t *testing.T) {
	const pause = 60 * time.Millisecond
	var slept []time.Duration
	prev := retrySleep
	retrySleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { retrySleep = prev })

	_ = RetryTransient(pause, func() error { return nil })
	if len(slept) != 0 {
		t.Errorf("a first-try success waited the retry pause: %v", slept)
	}
	_ = RetryTransient(pause, func() error { return ErrNotHeld })
	if len(slept) != 0 {
		t.Errorf("a permanent failure waited the retry pause: %v", slept)
	}
	calls := 0
	_ = RetryTransient(pause, func() error {
		if calls == 1 && len(slept) == 0 {
			t.Error("the retry ran before the pause")
		}
		calls++
		return ErrIO
	})
	if calls != 2 || len(slept) != 1 || slept[0] != pause {
		t.Errorf("calls = %d, pauses = %v; want 2 calls with one %s pause between them", calls, slept, pause)
	}
	slept = nil
	_ = RetryTransient(0, func() error { return ErrIO })
	if len(slept) != 0 {
		t.Errorf("a zero pause slept: %v", slept)
	}
}

// A corrupt entry or wait is reported with its position and the name of the check
// that failed, never a value from the file, so an operator can find it and the
// text stays safe for a log. errors.Is(ErrCorrupt) still holds.
func TestCorruptEntryErrorNamesTheEntryAndTheCheckButNoValue(t *testing.T) {
	entry := func(holder string, edit func(string) string) string {
		base := `{"holderId":"` + holder + `","kind":"implement","repo":"o/a","ticket":1,"branch":"","launcherPid":1,"launcherStart":"","socket":"","window":"","dir":"","launchId":"","state":"reserved","createdAt":"2026-11-20T09:00:00Z","updatedAt":"2026-11-20T09:00:00Z"}`
		if edit != nil {
			base = edit(base)
		}
		return base
	}
	third := func(edit func(string) string) string {
		return `{"schema":1,"entries":[` + entry("h1", nil) + `,` + entry("h2", nil) + `,` + entry("h3", edit) + `],"waits":[]}`
	}
	replace := func(old, repl string) func(string) string {
		return func(s string) string { return strings.Replace(s, old, repl, 1) }
	}
	wait := `{"repo":"o/a","ticket":0,"kind":"ci-repair","reason":"x","since":"2026-11-20T09:00:00Z","refreshedAt":"2026-11-20T09:00:00Z","expiresAt":"2026-11-20T09:03:00Z"}`
	cases := []struct {
		name, content, want, secret string
	}{
		{"window empty on a confirmed entry", third(replace(`"reserved"`, `"confirmed"`)), "entry 3: confirmed entry has no window", ""},
		{"launcher pid out of range", third(replace(`"launcherPid":1`, `"launcherPid":2147483648`)), "entry 3: launcher pid out of range", "2147483648"},
		{"repo holds a control character", third(replace(`"o/a"`, `"o/a\nreconcile: forged"`)), "entry 3: repo invalid", "forged"},
		{"dir holds a control character", third(replace(`"dir":""`, `"dir":"/d\u001b[2J"`)), "entry 3: dir invalid", "[2J"},
		{"unknown kind", third(replace(`"implement"`, `"bogus-kind"`)), "entry 3: kind unknown", "bogus-kind"},
		{"unknown state", third(replace(`"reserved"`, `"limbo-state"`)), "entry 3: state unknown", "limbo-state"},
		{"ticket out of range", third(replace(`"ticket":1`, `"ticket":0`)), "entry 3: ticket out of range", ""},
		{"empty holder id", third(replace(`"holderId":"h3"`, `"holderId":""`)), "entry 3: holder id empty", ""},
		{"duplicate holder", third(replace(`"holderId":"h3"`, `"holderId":"h1"`)), "entry 3: duplicate holder", "h1"},
		{"missing key", third(replace(`"branch":"",`, ``)), "entry 3: keys missing or unknown", ""},
		{"null field", third(replace(`"branch":""`, `"branch":null`)), "entry 3: keys missing or unknown", ""},
		{"wrong field type", third(replace(`"ticket":1`, `"ticket":"one"`)), "entry 3: field type invalid", ""},
		{"wait with no ticket", `{"schema":1,"entries":[],"waits":[` + wait + `]}`, "wait 1: ticket out of range", ""},
		{"wait with a negative owner pid", schemaTwo(waitV2("-4", `"100"`)), "wait 1: owner pid out of range", ""},
		{"wait with an owner pid past int32", schemaTwo(waitV2("2147483648", `"100"`)), "wait 1: owner pid out of range", "2147483648"},
		{"wait with a non-string owner start", schemaTwo(waitV2("4242", `100`)), "wait 1: field type invalid", ""},
		{"wait with an unclean owner start", schemaTwo(waitV2("4242", `"1\u001b[2J"`)), "wait 1: ownerStart invalid", "[2J"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			path := e.writeLedger(tc.content)

			_, err := e.ledger.Admit(e.req(KindImplement, "o/b", 9))

			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Admit error = %v, want it to wrap ErrCorrupt", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Admit error = %q, want it to name %q", err, tc.want)
			}
			if tc.secret != "" && strings.Contains(err.Error(), tc.secret) {
				t.Errorf("error %q carries the file's value %q", err, tc.secret)
			}
			assertPathFree(t, err, e.dir)
			if _, perr := e.ledger.Peek(); !errors.Is(perr, ErrCorrupt) || !strings.Contains(perr.Error(), tc.want) {
				t.Errorf("Peek error = %v, want ErrCorrupt naming %q", perr, tc.want)
			}
			if data, rerr := os.ReadFile(path); rerr != nil || string(data) != tc.content {
				t.Fatalf("the unreadable ledger was rewritten: %q (%v), want it untouched", data, rerr)
			}
		})
	}
}

// A request that names a value the ledger could not store and read back, or one
// the read side would refuse (which would make the whole ledger unreadable), is
// refused up front: nothing is written.
func TestRequestWithAnUnstorableFieldIsRefusedAndNothingIsWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(r *Request)
	}{
		{"a directory that is not valid UTF-8", func(r *Request) { r.Dir = "/work/\xffdir" }},
		{"a socket that is not valid UTF-8", func(r *Request) { r.Socket = "/run/\xc3(sock" }},
		{"a window that is not valid UTF-8", func(r *Request) { r.Window = "7-\xff" }},
		{"a negative launcher", func(r *Request) { r.Launcher = -1 }},
		{"a launcher past int32", func(r *Request) { r.Launcher = math.MaxInt32 + 1 }},
		{"a launch id with a control character", func(r *Request) { r.LaunchID = "abc\ndef" }},
		{"a launch id that is not valid UTF-8", func(r *Request) { r.LaunchID = "ab\xff" }},
		{"an over-long launch id", func(r *Request) { r.LaunchID = strings.Repeat("a", maxFieldBytes+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			req := e.req(KindImplement, "o/a", 1)
			tc.edit(&req)

			_, err := e.ledger.Admit(req)

			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Admit error = %v, want ErrInvalidRequest", err)
			}
			assertPathFree(t, err, e.dir)
			if _, statErr := os.Stat(filepath.Join(e.dir, ledgerFile)); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("a refused request wrote the ledger (stat err %v)", statErr)
			}
		})
	}
}

func TestRequestAtTheLauncherAndLaunchIDBoundsIsAdmittedAndReadsBack(t *testing.T) {
	e := newEnv(t)
	req := e.req(KindImplement, "o/a", 1)
	req.Launcher = math.MaxInt32
	e.setProc(math.MaxInt32, "5")
	req.LaunchID = strings.Repeat("a", maxFieldBytes)

	e.mustAdmit(req)

	if got := e.held(); len(got) != 1 || got[0].LauncherPID != math.MaxInt32 || len(got[0].LaunchID) != maxFieldBytes {
		t.Fatalf("entries = %+v, want the boundary values stored and readable", got)
	}
}

func TestConfirmWithAnUnstorableFieldIsRefusedAndTheReservationStays(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Confirmation
	}{
		{"a directory that is not valid UTF-8", Confirmation{Socket: testSocket, Window: "1-implement", Dir: "/work/\xffdir"}},
		{"a socket that is not valid UTF-8", Confirmation{Socket: "/run/\xff", Window: "1-implement"}},
		{"a launch id with a control character", Confirmation{Socket: testSocket, Window: "1-implement", LaunchID: "a\tb"}},
		{"a launch id that is not valid UTF-8", Confirmation{Socket: testSocket, Window: "1-implement", LaunchID: "\xff"}},
		{"an over-long launch id", Confirmation{Socket: testSocket, Window: "1-implement", LaunchID: strings.Repeat("a", maxFieldBytes+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			p := e.mustAdmit(e.req(KindImplement, "o/a", 1))
			before := e.readLedger()

			err := e.ledger.Confirm(p, tc.c)

			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Confirm error = %v, want ErrInvalidRequest", err)
			}
			if after := e.readLedger(); after != before {
				t.Error("a refused confirmation changed the ledger")
			}
			if got := e.held(); len(got) != 1 || got[0].State != StateReserved {
				t.Errorf("entries = %+v, want the reservation kept as it was", got)
			}
		})
	}
}

// A lock-free read that keeps losing its race with a writer's rename is its own
// error, so a log can tell a busy writer from a symlink or special file; it still
// fails closed: Peek reads nothing and Admit admits nothing.
func TestALedgerThatKeepsBeingReplacedIsRacedNotNotRegularAndAdmitsNothing(t *testing.T) {
	e := newEnv(t)
	const content = `{"schema":1,"entries":[],"waits":[]}`
	e.writeLedger(content)
	afterReadLstat = func() {
		if err := e.ledger.store.WriteFile(ledgerFile, []byte(content)); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterReadLstat = nil })

	_, peekErr := e.ledger.Peek()
	_, admitErr := e.ledger.Admit(e.req(KindImplement, "o/a", 1))

	for name, err := range map[string]error{"Peek": peekErr, "Admit": admitErr} {
		if !errors.Is(err, ErrRaced) || errors.Is(err, ErrNotRegular) {
			t.Errorf("%s error = %v, want ErrRaced", name, err)
		}
	}
	afterReadLstat = nil
	if got := e.held(); len(got) != 0 {
		t.Errorf("entries = %+v, want none admitted", got)
	}
}

func TestWorkerInSaysWhetherAnInventoryShowsTheEntrysWorker(t *testing.T) {
	e := newEnv(t)
	e.mustAdmit(e.req(KindCIRepair, "o/a", 7))
	entry := e.held()[0]
	name, dir := entry.Window, entry.Dir
	complete := func(readAt time.Time, windows ...Window) Inventory {
		return Inventory{testSocket: {Complete: true, ReadAt: readAt, Windows: windows}}
	}
	later := entry.UpdatedAt.Add(time.Second)
	before := entry.UpdatedAt.Add(-time.Second)
	for _, tc := range []struct {
		name string
		inv  Inventory
		want WorkerState
	}{
		{"a live window of the entry", complete(later, live(name, dir)), WorkerAlive},
		{"a live window read before the entry last changed still shows a worker", complete(before, live(name, dir)), WorkerAlive},
		{"a complete inventory with no window", complete(later), WorkerAbsent},
		{"only a window of another name", complete(later, live("8-ci-repair", dir)), WorkerAbsent},
		{"only a dead pane", complete(later, dead(name, dir)), WorkerAbsent},
		{"a window in another directory", complete(later, live(name, "/elsewhere")), WorkerAbsent},
		{"no window in an inventory read before the entry last changed", complete(before), WorkerUnknown},
		{"an incomplete inventory", Inventory{testSocket: {Complete: false, ReadAt: later}}, WorkerUnknown},
		{"an unset read time", Inventory{testSocket: {Complete: true}}, WorkerUnknown},
		{"a socket that was not read", Inventory{"/other": {Complete: true, ReadAt: later}}, WorkerUnknown},
		{"no inventory", nil, WorkerUnknown},
	} {
		if got := entry.WorkerIn(tc.inv); got != tc.want {
			t.Errorf("%s: WorkerIn = %v, want %v", tc.name, got, tc.want)
		}
	}
}
