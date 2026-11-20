package dispatch

// Admission of dispatch launches against the shared capacity ledger (#1469).
// Every test here drives applyDispatch over a ledger directory the test owns and
// asserts what a user can see: which tickets launched, the one skip line each
// refused ticket gets, and which entries the ledger still holds. A recording
// ledger is used only where the criterion is itself about the calls (a ticket a
// Decide gate skips makes none).

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// -- fixtures -------------------------------------------------------------------

func intp(n int) *int { return &n }

func setLedgerSeam(t *testing.T, fn func() capacityLedger) {
	t.Helper()
	prev := capacityLedgerFn
	capacityLedgerFn = fn
	t.Cleanup(func() { capacityLedgerFn = prev })
}

// ledgerRoot returns a fresh state root (0700, as the store requires) and the
// capacity directory path inside it, which does not exist yet.
func ledgerRoot(t *testing.T) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("securing the ledger root: %v", err)
	}
	return root, filepath.Join(root, "capacity")
}

// useLedger points the ledger seam at a fresh ledger owned by this test, with a
// short lock timeout, and returns it with its directory.
func useLedger(t *testing.T) (*capacity.Ledger, string) {
	t.Helper()
	_, dir := ledgerRoot(t)
	l := capacity.NewLedger(dir, capacity.Options{LockTimeout: 200 * time.Millisecond})
	setLedgerSeam(t, func() capacityLedger { return l })
	return l, dir
}

// useInventoryFn points the tmux inventory seam at fn for one test.
func useInventoryFn(t *testing.T, fn func(socket string) (tmux.CleanupInventory, error)) {
	t.Helper()
	prev := capacityInventoryFn
	capacityInventoryFn = fn
	t.Cleanup(func() { capacityInventoryFn = prev })
}

// useLiveWindows makes every socket's complete inventory list the named windows
// (one live pane each, no recorded repo dir): the workers of a ledger's entries
// are still running.
func useLiveWindows(t *testing.T, names ...string) {
	t.Helper()
	useInventoryFn(t, func(string) (tmux.CleanupInventory, error) {
		var inv tmux.CleanupInventory
		for i, name := range names {
			inv.Windows = append(inv.Windows, tmux.CleanupWindow{
				WindowID: fmt.Sprintf("@%d", i+1), Name: name, Panes: []tmux.CleanupPane{{ID: fmt.Sprintf("%%%d", i+1)}},
			})
		}
		return inv, nil
	})
}

type capRepo struct {
	name string
	cap  *int
}

// capacityConfig writes a fleet config with the machine cap and the repos
// (each with an optional repo cap) and loads it, so Decide and the ledger's
// LoadLimits read one file.
func capacityConfig(t *testing.T, machine int, repos ...capRepo) Config {
	t.Helper()
	entries := make([]string, 0, len(repos))
	for _, r := range repos {
		e := fmt.Sprintf(`{"repo":%q,"dir":"","session":"test-session"`, r.name)
		if r.cap != nil {
			e += fmt.Sprintf(`,"concurrencyCap":%d`, *r.cap)
		}
		entries = append(entries, e+"}")
	}
	body := fmt.Sprintf(`{"dispatch":{"concurrencyCap":%d,"repos":[%s]}}`, machine, strings.Join(entries, ","))
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

type ticketRef struct {
	repo   string
	number int
	branch string
}

var capacityNow = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

// capacityDeps is the happy path for each ref: a Planned, authorized ticket with
// a fresh plan, on an idle, reachable daemon.
func capacityDeps(refs ...ticketRef) dispatchDeps {
	deps := dispatchDeps{Snapshot: &watch.StateSnapshot{}, Now: capacityNow, CurrentUser: "octocat"}
	for _, r := range refs {
		deps.Tickets = append(deps.Tickets, authorized(Ticket{
			Repo: r.repo, Number: r.number, Title: "t", Labels: []string{"Planned"}, Assignees: []string{"octocat"}, Branch: r.branch,
		}))
		deps.Plans = append(deps.Plans, Plan{Repo: r.repo, Path: fmt.Sprintf(".plans/%d-x.md", r.number), TicketID: r.number, Status: "planned", Sha256: testPlanSha})
	}
	return deps
}

// runPass runs one real (not dry) applyDispatch pass and returns its log.
func runPass(t *testing.T, cfg Config, deps dispatchDeps, ctrl run.Controller) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	_, err := applyDispatch(cfg, deps, ctrl, &fakeMutator{}, false, &buf)
	return buf.String(), err
}

func mustRunPass(t *testing.T, cfg Config, deps dispatchDeps) string {
	t.Helper()
	log, err := runPass(t, cfg, deps, fakeController{})
	if err != nil {
		t.Fatalf("applyDispatch: %v\nlog:\n%s", err, log)
	}
	return log
}

func decisionOf(decisions []Decision, number int) Decision {
	for _, d := range decisions {
		if d.Ticket.Number == number {
			return d
		}
	}
	return Decision{}
}

func hasLine(log, want string) bool {
	for _, line := range strings.Split(log, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func skipLine(repo string, number int, reason string) string {
	return fmt.Sprintf("%s#%d skip: %s", repo, number, reason)
}

func assertSkipped(t *testing.T, log, repo string, number int, reason string) {
	t.Helper()
	if want := skipLine(repo, number, reason); !hasLine(log, want) {
		t.Errorf("log has no line %q:\n%s", want, log)
	}
}

func assertLaunched(t *testing.T, got *[]string, want ...string) {
	t.Helper()
	if strings.Join(*got, ",") != strings.Join(want, ",") {
		t.Errorf("launched tickets = %v, want %v", *got, want)
	}
}

func heldEntries(t *testing.T, l *capacity.Ledger) []capacity.Entry {
	t.Helper()
	snap, err := l.Peek()
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	return snap.Entries
}

// seedHeld puts a confirmed entry for another launcher's worker in the ledger and
// returns it: a dispatch pickup ("<n>-implement") or a babysit repair.
func seedHeld(t *testing.T, l *capacity.Ledger, kind capacity.Kind, repo string, ticket int, branch string) {
	t.Helper()
	window := fmt.Sprintf("%d-%s", ticket, kind)
	if kind == capacity.KindImplement {
		window = fmt.Sprintf("%d-implement", ticket)
	}
	d, err := l.Admit(capacity.Request{Kind: kind, Repo: repo, Ticket: ticket, Branch: branch, Window: window, Limits: capacity.Limits{Machine: 99}})
	if err != nil || !d.Admitted {
		t.Fatalf("seeding the ledger: admitted=%v err=%v reason=%q", d.Admitted, err, d.Reason)
	}
	if err := l.Confirm(d.Permit, capacity.Confirmation{Window: window}); err != nil {
		t.Fatalf("confirming the seeded entry: %v", err)
	}
}

// recordingLedger counts Admit calls; everything else is the real ledger.
type recordingLedger struct {
	*capacity.Ledger
	admits int
}

func (r *recordingLedger) Admit(req capacity.Request) (capacity.Decision, error) {
	r.admits++
	return r.Ledger.Admit(req)
}

// -- the cap across passes and launchers ----------------------------------------

// With concurrencyCap 1, a second pass (a one-shot beside an interval pass) over
// the one ledger launches nothing and logs `skip: concurrency cap reached`.
func TestAdmitCapOneHoldsAcrossPasses(t *testing.T) {
	useLedger(t)
	useLiveWindows(t, "1-implement")
	cfg := capacityConfig(t, 1, capRepo{name: "o/r"})
	spawned := recordSpawns(t)

	mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 1}))
	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 2}))

	assertLaunched(t, spawned, "1")
	assertSkipped(t, log, "o/r", 2, "concurrency cap reached")
}

// Two simultaneous admissions for one repo and ticket launch exactly one worker;
// the other logs `skip: worker already reserved`.
func TestAdmitSameTicketReservedOnce(t *testing.T) {
	useLedger(t)
	useLiveWindows(t, "42-implement")
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	var mu sync.Mutex
	var spawned []string
	stubRunFn(t, func(o run.Opts, _ run.Controller) error {
		mu.Lock()
		defer mu.Unlock()
		spawned = append(spawned, o.WindowTicket)
		return nil
	})

	logs := make([]bytes.Buffer, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range logs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := applyDispatch(cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}), fakeController{}, &fakeMutator{}, false, &logs[i]); err != nil {
				t.Errorf("applyDispatch: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	assertLaunched(t, &spawned, "42")
	refused := 0
	for i := range logs {
		if hasLine(logs[i].String(), skipLine("o/r", 42, "worker already reserved")) {
			refused++
		}
	}
	if refused != 1 {
		t.Errorf("%d passes logged %q, want exactly 1\npass 0:\n%s\npass 1:\n%s", refused, "worker already reserved", logs[0].String(), logs[1].String())
	}
}

// Machine cap 3, repo A capped at 1, repo B uncapped, four eligible tickets in
// each: one pass launches 1 in A and 2 in B, and A's next ticket logs
// `skip: repo concurrency cap reached`.
func TestAdmitRepoCapLimitsOneRepoNotTheOther(t *testing.T) {
	useLedger(t)
	cfg := capacityConfig(t, 3, capRepo{name: "o/A", cap: intp(1)}, capRepo{name: "o/B"})
	spawned := recordSpawns(t)
	var refs []ticketRef
	for _, n := range []int{1, 2, 3, 4} {
		refs = append(refs, ticketRef{repo: "o/A", number: n})
	}
	for _, n := range []int{11, 12, 13, 14} {
		refs = append(refs, ticketRef{repo: "o/B", number: n})
	}

	log := mustRunPass(t, cfg, capacityDeps(refs...))

	assertLaunched(t, spawned, "1", "11", "12")
	for _, n := range []int{2, 3, 4} {
		assertSkipped(t, log, "o/A", n, "repo concurrency cap reached")
	}
	for _, n := range []int{13, 14} {
		assertSkipped(t, log, "o/B", n, "concurrency cap reached")
	}
}

// Another launcher's entry counts against the repo cap: Admit is the
// authoritative check, Decide's pre-filter only knows this pass.
func TestAdmitRepoCapCountsEntriesOfOtherLaunchers(t *testing.T) {
	l, _ := useLedger(t)
	seedHeld(t, l, capacity.KindImplement, "o/A", 99, "")
	useLiveWindows(t, "99-implement")
	cfg := capacityConfig(t, 3, capRepo{name: "o/A", cap: intp(1)}, capRepo{name: "o/B"})
	spawned := recordSpawns(t)

	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/A", number: 1}, ticketRef{repo: "o/B", number: 11}))

	assertLaunched(t, spawned, "11")
	assertSkipped(t, log, "o/A", 1, "repo concurrency cap reached")
	if strings.Count(log, "o/A#1 ") != 1 {
		t.Errorf("o/A#1 must have exactly one decision line:\n%s", log)
	}
}

func TestAdmitRepoCapEdgeCases(t *testing.T) {
	t.Run("a repo cap above the machine cap is the machine cap", func(t *testing.T) {
		useLedger(t)
		cfg := capacityConfig(t, 2, capRepo{name: "o/A", cap: intp(5)})
		spawned := recordSpawns(t)

		log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/A", number: 1}, ticketRef{repo: "o/A", number: 2}, ticketRef{repo: "o/A", number: 3}))

		assertLaunched(t, spawned, "1", "2")
		assertSkipped(t, log, "o/A", 3, "concurrency cap reached")
	})

	t.Run("a repo cap of 0 admits nothing for that repo only", func(t *testing.T) {
		useLedger(t)
		cfg := capacityConfig(t, 3, capRepo{name: "o/A", cap: intp(0)}, capRepo{name: "o/B"})
		spawned := recordSpawns(t)

		log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/A", number: 1}, ticketRef{repo: "o/B", number: 11}))

		assertLaunched(t, spawned, "11")
		assertSkipped(t, log, "o/A", 1, "repo concurrency cap reached")
	})

	t.Run("a machine cap of 0 pauses everything and writes nothing", func(t *testing.T) {
		_, dir := useLedger(t)
		cfg := capacityConfig(t, 0, capRepo{name: "o/A"}, capRepo{name: "o/B"})
		spawned := recordSpawns(t)

		log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/A", number: 1}, ticketRef{repo: "o/B", number: 11}))

		assertLaunched(t, spawned)
		assertSkipped(t, log, "o/A", 1, "concurrency cap reached")
		assertSkipped(t, log, "o/B", 11, "concurrency cap reached")
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("a paused pass created the ledger directory (Lstat err = %v)", err)
		}
	})
}

// -- the spawn outcome settles the reservation ----------------------------------

// errWindowController is a controller whose tmux window creation fails.
type errWindowController struct{ fakeController }

func (errWindowController) NewWindow(socket, session, name, shellCommand string) error {
	return errors.New("tmux: server exited unexpectedly")
}

// A launch that failed before any window existed leaves zero entries. One that
// reached NewWindow, whatever came back, may have a window: it stays held until a
// complete inventory proves there is none.
func TestSpawnOutcomeSettlesTheReservation(t *testing.T) {
	spawnedErr := fmt.Errorf("setting automatic-rename off: %w", run.ErrWindowSpawned)
	tests := []struct {
		name        string
		ctrl        run.Controller
		spawn       func(run.Opts, run.Controller) error
		wantHeld    int
		wantRunFail bool
	}{
		{
			name:        "failure before a window was created releases",
			ctrl:        fakeController{},
			spawn:       func(run.Opts, run.Controller) error { return errors.New("resolving the agent: nothing configured") },
			wantHeld:    0,
			wantRunFail: true,
		},
		{
			name: "window spawned but setup failed (exit 3) is held",
			ctrl: fakeController{},
			spawn: func(o run.Opts, c run.Controller) error {
				if err := c.NewWindow(o.TmuxSocket, "=s", o.WindowTicket+"-implement", "cmd"); err != nil {
					return err
				}
				return spawnedErr
			},
			wantHeld:    1,
			wantRunFail: true,
		},
		{
			name: "a failing window creation is ambiguous and held",
			ctrl: errWindowController{},
			spawn: func(o run.Opts, c run.Controller) error {
				return c.NewWindow(o.TmuxSocket, "=s", o.WindowTicket+"-implement", "cmd")
			},
			wantHeld:    1,
			wantRunFail: true,
		},
		{
			name:     "a clean spawn is held",
			ctrl:     fakeController{},
			spawn:    func(run.Opts, run.Controller) error { return nil },
			wantHeld: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, _ := useLedger(t)
			stubRunFn(t, tt.spawn)
			cfg := capacityConfig(t, 3, capRepo{name: "o/r"})

			log, err := runPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}), tt.ctrl)

			if (err != nil) != tt.wantRunFail {
				t.Fatalf("applyDispatch error = %v, want failure = %v\nlog:\n%s", err, tt.wantRunFail, log)
			}
			entries := heldEntries(t, l)
			if len(entries) != tt.wantHeld {
				t.Fatalf("the ledger holds %d entries (%+v), want %d", len(entries), entries, tt.wantHeld)
			}
			if tt.wantHeld == 0 {
				return
			}
			e := entries[0]
			if e.Repo != "o/r" || e.Ticket != 42 || e.State != capacity.StateConfirmed || e.Window != "42-implement" || e.Kind != capacity.KindImplement {
				t.Errorf("held entry = %+v, want o/r#42 confirmed to window 42-implement as an implement launch", e)
			}
			// A complete inventory read after the launch that shows no window frees it.
			released, err := l.Reconcile(capacity.Inventory{e.Socket: {Complete: true, ReadAt: time.Now()}})
			if err != nil || len(released) != 1 {
				t.Fatalf("Reconcile with an empty complete inventory = %v, %v; want the entry released", released, err)
			}
		})
	}
}

// Every later non-spawn exit releases the permit: a failed resume claim and a
// failed launch-record clear spawn nothing and hold nothing.
func TestAdmitReleasesOnEveryLaterNonSpawnExit(t *testing.T) {
	t.Run("a failed clear of the recorded launch", func(t *testing.T) {
		l, _ := useLedger(t)
		restore := clearLaunchFn
		clearLaunchFn = func(pipeline.ArtifactOpts) error { return errors.New("state file unreadable") }
		t.Cleanup(func() { clearLaunchFn = restore })
		spawned := recordSpawns(t)
		cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
		cfg.Repos[0].Dir = t.TempDir()

		log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}))

		assertLaunched(t, spawned)
		if got := heldEntries(t, l); len(got) != 0 {
			t.Errorf("the ledger holds %+v after a failed clear, want nothing\nlog:\n%s", got, log)
		}
	})

	t.Run("a failed resume claim", func(t *testing.T) {
		l, _ := useLedger(t)
		spawned := recordSpawns(t)
		cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
		var buf bytes.Buffer
		decisions, err := applyDispatch(cfg, resumeDispatchDeps(capacityNow), fakeController{}, &failingMutator{}, false, &buf)

		if err == nil {
			t.Fatalf("applyDispatch returned no error for a failed resume claim\nlog:\n%s", buf.String())
		}
		if d := decisionOf(decisions, 42); d.Action != ActionDispatch || !d.Resume {
			t.Fatalf("decision = %+v, want a resume dispatch (the fixture must reach the claim)", d)
		}
		assertLaunched(t, spawned)
		if got := heldEntries(t, l); len(got) != 0 {
			t.Errorf("the ledger holds %+v after a failed resume claim, want nothing", got)
		}
	})
}

// -- the ledger cannot be trusted -----------------------------------------------

func TestAdmitUnreadableLedgerAdmitsNothing(t *testing.T) {
	writeLedger := func(t *testing.T, dir, content string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ledger.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name   string
		setup  func(t *testing.T, root, dir string) (cleanup func())
		reason error
	}{
		{
			name: "a ledger that does not parse",
			setup: func(t *testing.T, _, dir string) func() {
				writeLedger(t, dir, "{definitely not a ledger")
				return func() {}
			},
			reason: capacity.ErrCorrupt,
		},
		{
			name: "an unknown schema version",
			setup: func(t *testing.T, _, dir string) func() {
				writeLedger(t, dir, `{"schema":2,"entries":[],"waits":[]}`)
				return func() {}
			},
			reason: capacity.ErrUnknownSchema,
		},
		{
			name: "a lock that cannot be taken in time",
			setup: func(t *testing.T, _, dir string) func() {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
					t.Fatal(err)
				}
				return func() { _ = f.Close() }
			},
			reason: capacity.ErrLockUnavailable,
		},
		{
			name: "a symlinked ledger file",
			setup: func(t *testing.T, root, dir string) func() {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(root, "elsewhere.json")
				if err := os.WriteFile(target, []byte(`{"schema":1,"entries":[],"waits":[]}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "ledger.json")); err != nil {
					t.Fatal(err)
				}
				return func() {}
			},
			reason: capacity.ErrNotRegular,
		},
		{
			name: "a symlinked capacity directory",
			setup: func(t *testing.T, root, dir string) func() {
				real := filepath.Join(root, "real")
				if err := os.Mkdir(real, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, dir); err != nil {
					t.Fatal(err)
				}
				return func() {}
			},
			reason: capacity.ErrUntrusted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, dir := ledgerRoot(t)
			l := capacity.NewLedger(dir, capacity.Options{LockTimeout: 100 * time.Millisecond})
			setLedgerSeam(t, func() capacityLedger { return l })
			cleanup := tt.setup(t, root, dir)
			defer cleanup()
			spawned := recordSpawns(t)
			cfg := capacityConfig(t, 3, capRepo{name: "o/r"})

			log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}))

			assertLaunched(t, spawned)
			assertSkipped(t, log, "o/r", 42, "capacity ledger unreadable: "+l.Path()+": "+tt.reason.Error())
			if entries, _ := os.ReadDir(filepath.Join(root, "real")); len(entries) != 0 {
				t.Errorf("files were written through the symlinked directory: %v", entries)
			}
		})
	}
}

// Limits are re-read from the config file at every admission; a file that cannot
// be read admits nothing and says why.
func TestAdmitUnreadableLimitsAdmitsNothing(t *testing.T) {
	l, _ := useLedger(t)
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	if err := os.WriteFile(cfg.Path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	spawned := recordSpawns(t)

	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}))

	assertLaunched(t, spawned)
	if !strings.Contains(log, "o/r#42 skip: capacity limits unreadable: ") {
		t.Errorf("log has no `capacity limits unreadable` skip line:\n%s", log)
	}
	if got := heldEntries(t, l); len(got) != 0 {
		t.Errorf("the ledger holds %+v, want nothing", got)
	}
}

// -- counting -------------------------------------------------------------------

// Each worker counts once: a ledger-held worker that is also a running daemon
// session, a running manual session with no entry, an orphaned container session;
// sessions in a non-running status count zero. Machine cap 2 with one ledger-held
// worker (#99): whether #1 launches shows how many slots the rest used.
func TestAdmitCountsEachWorkerOnce(t *testing.T) {
	running := func(name string) watch.WindowState { return watch.WindowState{WindowName: name, Status: "running"} }
	tests := []struct {
		name      string
		windows   []watch.WindowState
		running   int
		orphans   int
		wantAdmit bool
	}{
		{"a held worker that is also a running session counts once", []watch.WindowState{running("99-implement")}, 1, 0, true},
		{"a running manual session counts", []watch.WindowState{running("5-manual")}, 1, 0, false},
		{"an orphaned container session counts", nil, 0, 1, false},
		{"sessions that are not running count zero", []watch.WindowState{{WindowName: "5-manual", Status: "idle"}, {WindowName: "6-manual", Status: "done"}}, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, _ := useLedger(t)
			seedHeld(t, l, capacity.KindImplement, "o/r", 99, "")
			useLiveWindows(t, "99-implement")
			cfg := capacityConfig(t, 2, capRepo{name: "o/r"})
			spawned := recordSpawns(t)
			deps := capacityDeps(ticketRef{repo: "o/r", number: 1})
			deps.Snapshot = &watch.StateSnapshot{Windows: tt.windows, Summary: watch.StatusSummary{Running: tt.running}}
			deps.OrphanedContainerSessions = tt.orphans

			log := mustRunPass(t, cfg, deps)

			if tt.wantAdmit {
				assertLaunched(t, spawned, "1")
				return
			}
			assertLaunched(t, spawned)
			assertSkipped(t, log, "o/r", 1, "concurrency cap reached")
		})
	}
}

// -- Decide gates and read-only passes make no admission ------------------------

func TestAdmitIsNeverCalledForATicketADecideGateSkips(t *testing.T) {
	l, _ := useLedger(t)
	rec := &recordingLedger{Ledger: l}
	setLedgerSeam(t, func() capacityLedger { return rec })
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	spawned := recordSpawns(t)
	deps := capacityDeps(ticketRef{repo: "o/r", number: 42}, ticketRef{repo: "o/r", number: 43})
	deps.Tickets[0].AuthorizationProbe = AuthorizationProbeUnreadable

	log := mustRunPass(t, cfg, deps)

	assertSkipped(t, log, "o/r", 42, reasonAuthorizationUnreadable)
	if strings.Count(log, "o/r#42 ") != 1 {
		t.Errorf("the gated ticket must keep exactly its one skip line:\n%s", log)
	}
	assertLaunched(t, spawned, "43")
	if rec.admits != 1 {
		t.Errorf("Admit was called %d times, want 1 (only the ticket no gate skipped)", rec.admits)
	}
}

func TestDryRunMakesNoAdmissionAndWritesNothing(t *testing.T) {
	l, dir := useLedger(t)
	rec := &recordingLedger{Ledger: l}
	setLedgerSeam(t, func() capacityLedger { return rec })
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Error("a dry run must not spawn")
		return nil
	})

	var buf bytes.Buffer
	decisions, err := applyDispatch(cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}), fakeController{}, &fakeMutator{}, true, &buf)

	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if d := decisionOf(decisions, 42); d.Action != ActionDispatch {
		t.Errorf("dry-run decision = %+v, want Decide's dispatch unchanged", d)
	}
	if rec.admits != 0 {
		t.Errorf("a dry run called Admit %d times, want 0", rec.admits)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("a dry run created the ledger directory (Lstat err = %v)", err)
	}
}

// A nil daemon snapshot admits nothing: Decide skips every ticket, so the ledger
// is never asked.
func TestNilSnapshotAdmitsNothing(t *testing.T) {
	l, _ := useLedger(t)
	rec := &recordingLedger{Ledger: l}
	setLedgerSeam(t, func() capacityLedger { return rec })
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	spawned := recordSpawns(t)
	deps := capacityDeps(ticketRef{repo: "o/r", number: 42})
	deps.Snapshot = nil

	mustRunPass(t, cfg, deps)

	assertLaunched(t, spawned)
	if rec.admits != 0 {
		t.Errorf("Admit was called %d times with no snapshot, want 0", rec.admits)
	}
}

// A dispatch that Admit refuses leaves #1566's persisted daily quota count
// unchanged; one that launches records its one launch.
func TestRefusedAdmissionLeavesDailyQuotaUnchanged(t *testing.T) {
	l, _ := useLedger(t)
	q := useQuotaDir(t)
	seedHeld(t, l, capacity.KindImplement, "o/r", 99, "")
	useLiveWindows(t, "99-implement")
	cfg := capacityConfig(t, 1, capRepo{name: "o/r"})
	cfg.DailyQuota = intp(2)
	spawned := recordSpawns(t)

	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 1}))

	assertLaunched(t, spawned)
	assertSkipped(t, log, "o/r", 1, "concurrency cap reached")
	if got := quotaUsed(t, q, capacityNow); got != 0 {
		t.Errorf("persisted daily quota = %d after a refused admission, want 0", got)
	}

	cfg = capacityConfig(t, 2, capRepo{name: "o/r"})
	cfg.DailyQuota = intp(2)
	mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 2}))
	assertLaunched(t, spawned, "2")
	if got := quotaUsed(t, q, capacityNow); got != 1 {
		t.Errorf("persisted daily quota = %d after one launch, want 1", got)
	}
}

// The quota and the ledger take the one store lock when they share a directory
// (the production layout): the quota's record is made after the reservation is
// settled and never inside a ledger call, so a launch with a daily quota set
// neither deadlocks on the lock nor loses either write.
func TestDailyQuotaRecordNeverNestsInsideTheLedgerLock(t *testing.T) {
	_, dir := ledgerRoot(t)
	l := capacity.NewLedger(dir, capacity.Options{LockTimeout: 200 * time.Millisecond})
	setLedgerSeam(t, func() capacityLedger { return l })
	pointQuotaSeam(t, dir)
	q := capacity.NewDailyQuota(dir)
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	cfg.DailyQuota = intp(5)
	spawned := recordSpawns(t)

	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}))

	assertLaunched(t, spawned, "42")
	if strings.Contains(log, "could not") || strings.Contains(log, "failed") {
		t.Errorf("a launch with a quota and a ledger in one store logged a failure:\n%s", log)
	}
	if got := quotaUsed(t, q, capacityNow); got != 1 {
		t.Errorf("persisted daily quota = %d, want 1", got)
	}
	if got := heldEntries(t, l); len(got) != 1 {
		t.Errorf("the ledger holds %+v, want the one launched worker", got)
	}
}

// -- the branch-writer exclusion ------------------------------------------------

// Dispatch skips a branch a babysit repair holds (`branch writer active`). A fresh
// pickup with no recorded branch skips that check, and the ticket-level exclusivity
// still applies to it.
func TestAdmitSkipsABranchWithALiveBabysitWriter(t *testing.T) {
	l, _ := useLedger(t)
	seedHeld(t, l, capacity.KindCIRepair, "o/r", 55, "fix/x")
	useLiveWindows(t, "55-ci-repair")
	cfg := capacityConfig(t, 5, capRepo{name: "o/r"})
	spawned := recordSpawns(t)

	log := mustRunPass(t, cfg, capacityDeps(
		ticketRef{repo: "o/r", number: 7, branch: "fix/x"},
		ticketRef{repo: "o/r", number: 8},
		ticketRef{repo: "o/r", number: 55},
	))

	assertLaunched(t, spawned, "8")
	assertSkipped(t, log, "o/r", 7, "branch writer active")
	assertSkipped(t, log, "o/r", 55, "worker already reserved")
}

// -- reading the pipeline branch ------------------------------------------------

// The admission request carries the ticket's pipeline branch: a launch records
// its branch in the ledger entry, which a babysit supervisor then checks against.
func TestAdmitRecordsTheTicketBranch(t *testing.T) {
	l, _ := useLedger(t)
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	recordSpawns(t)

	mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42, branch: "feature/y"}))

	entries := heldEntries(t, l)
	if len(entries) != 1 || entries[0].Branch != "feature/y" {
		t.Fatalf("held entries = %+v, want one on branch feature/y", entries)
	}
}

// -- review round 1: the entry names the window `cenci run` really opens --------

// `cenci run` makes the directory absolute (and falls back to its cwd), and the
// ledger matches a window by directory: a relative or empty repos[].dir must be
// recorded the way the window will be.
func TestAdmitRecordsTheAbsoluteLaunchDirectory(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  string
		want func(wd string) string
	}{
		{"a relative repos[].dir", "rel/repo", func(wd string) string { return filepath.Join(wd, "rel", "repo") }},
		{"an unclean repos[].dir", "rel/../rel/repo/", func(wd string) string { return filepath.Join(wd, "rel", "repo") }},
		{"an empty repos[].dir is the launch cwd", "", func(wd string) string { return wd }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := useLedger(t)
			recordSpawns(t)
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "rel", "repo"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(root)
			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
			cfg.Repos[0].Dir = tc.dir

			log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}))

			entries := heldEntries(t, l)
			if want := tc.want(wd); len(entries) != 1 || entries[0].Dir != want {
				t.Fatalf("held entries = %+v, want one in directory %q\nlog:\n%s", entries, want, log)
			}
		})
	}
}

// -- review round 1: a failed settle is retried once and reported accurately -----

// settleLedger is the real ledger whose first Release or Confirm calls fail.
type settleLedger struct {
	*capacity.Ledger
	failReleases, failConfirms int
	releaseErr, confirmErr     error
	releaseCalls, confirmCalls int
}

func (s *settleLedger) Release(p capacity.Permit) error {
	s.releaseCalls++
	if s.releaseCalls <= s.failReleases {
		if s.releaseErr != nil {
			return s.releaseErr
		}
		return capacity.ErrIO
	}
	return s.Ledger.Release(p)
}

func (s *settleLedger) Confirm(p capacity.Permit, c capacity.Confirmation) error {
	s.confirmCalls++
	if s.confirmCalls <= s.failConfirms {
		return s.confirmErr
	}
	return s.Ledger.Confirm(p, c)
}

func noSettlePause(t *testing.T) {
	t.Helper()
	prev := settleRetryPause
	settleRetryPause = 0
	t.Cleanup(func() { settleRetryPause = prev })
}

func TestSettleRetriesAFailedReleaseOrConfirmOnce(t *testing.T) {
	failedBeforeWindow := func(run.Opts, run.Controller) error { return errors.New("resolving the agent: nothing configured") }
	cleanSpawn := func(run.Opts, run.Controller) error { return nil }
	const staysReserved = "it stays reserved while this process runs"
	tests := []struct {
		name         string
		spawn        func(run.Opts, run.Controller) error
		ledger       settleLedger
		wantEntries  int
		wantState    capacity.State
		wantCalls    func(l *settleLedger) int
		wantCallsN   int
		wantInLog    []string
		wantNotInLog []string
	}{
		{
			name: "a release that fails once is retried and frees the slot", spawn: failedBeforeWindow,
			ledger: settleLedger{failReleases: 1}, wantEntries: 0,
			wantCalls: func(l *settleLedger) int { return l.releaseCalls }, wantCallsN: 2,
			wantNotInLog: []string{"could not be released"},
		},
		{
			name: "a confirm that fails once is retried and confirms", spawn: cleanSpawn,
			ledger: settleLedger{failConfirms: 1, confirmErr: capacity.ErrIO}, wantEntries: 1, wantState: capacity.StateConfirmed,
			wantCalls: func(l *settleLedger) int { return l.confirmCalls }, wantCallsN: 2,
			wantNotInLog: []string{"could not be confirmed"},
		},
		{
			name: "a release that keeps failing is retried once, then says exactly what remains", spawn: failedBeforeWindow,
			ledger: settleLedger{failReleases: 99}, wantEntries: 1, wantState: capacity.StateReserved,
			wantCalls: func(l *settleLedger) int { return l.releaseCalls }, wantCallsN: 2,
			wantInLog:    []string{"capacity reservation could not be released", staysReserved, "ledger.json"},
			wantNotInLog: []string{"an inventory proving no worker frees it"},
		},
		{
			name: "a confirm that keeps failing is retried once, then says exactly what remains", spawn: cleanSpawn,
			ledger: settleLedger{failConfirms: 99, confirmErr: capacity.ErrIO}, wantEntries: 1, wantState: capacity.StateReserved,
			wantCalls: func(l *settleLedger) int { return l.confirmCalls }, wantCallsN: 2,
			wantInLog: []string{"capacity reservation could not be confirmed", staysReserved, "ledger.json"},
		},
		{
			name: "a confirm of an entry that is gone from the ledger is its own message and is not retried", spawn: cleanSpawn,
			ledger: settleLedger{failConfirms: 99, confirmErr: capacity.ErrNotHeld}, wantEntries: 1, wantState: capacity.StateReserved,
			wantCalls: func(l *settleLedger) int { return l.confirmCalls }, wantCallsN: 1,
			wantInLog:    []string{"capacity reservation is missing from the ledger (removed or reset); the worker is live but not counted by the ledger until the daemon sees it running"},
			wantNotInLog: []string{"stays reserved", "could not be confirmed", "already released", "another process"},
		},
		{
			name: "a release that fails permanently (a corrupt ledger) is not retried", spawn: failedBeforeWindow,
			ledger: settleLedger{failReleases: 99, releaseErr: capacity.ErrCorrupt}, wantEntries: 1, wantState: capacity.StateReserved,
			wantCalls: func(l *settleLedger) int { return l.releaseCalls }, wantCallsN: 1,
			wantInLog: []string{"capacity reservation could not be released", staysReserved},
		},
		{
			name: "a confirm that fails permanently (an invalid request) is not retried", spawn: cleanSpawn,
			ledger: settleLedger{failConfirms: 99, confirmErr: capacity.ErrInvalidRequest}, wantEntries: 1, wantState: capacity.StateReserved,
			wantCalls: func(l *settleLedger) int { return l.confirmCalls }, wantCallsN: 1,
			wantInLog: []string{"capacity reservation could not be confirmed", staysReserved},
		},
		{
			name: "a confirm that fails on a busy lock is retried once", spawn: cleanSpawn,
			ledger: settleLedger{failConfirms: 99, confirmErr: capacity.ErrLockUnavailable}, wantEntries: 1, wantState: capacity.StateReserved,
			wantCalls: func(l *settleLedger) int { return l.confirmCalls }, wantCallsN: 2,
			wantInLog: []string{"capacity reservation could not be confirmed", staysReserved},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noSettlePause(t)
			base, _ := useLedger(t)
			wrapped := tt.ledger
			wrapped.Ledger = base
			setLedgerSeam(t, func() capacityLedger { return &wrapped })
			stubRunFn(t, tt.spawn)
			cfg := capacityConfig(t, 3, capRepo{name: "o/r"})

			log, _ := runPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}), fakeController{})

			if got := tt.wantCalls(&wrapped); got != tt.wantCallsN {
				t.Errorf("settle calls = %d, want %d\nlog:\n%s", got, tt.wantCallsN, log)
			}
			entries := heldEntries(t, base)
			if len(entries) != tt.wantEntries || (tt.wantEntries == 1 && entries[0].State != tt.wantState) {
				t.Fatalf("held entries = %+v, want %d in state %q\nlog:\n%s", entries, tt.wantEntries, tt.wantState, log)
			}
			for _, s := range tt.wantInLog {
				if !strings.Contains(log, s) {
					t.Errorf("log has no %q:\n%s", s, log)
				}
			}
			for _, s := range tt.wantNotInLog {
				if strings.Contains(log, s) {
					t.Errorf("log has %q:\n%s", s, log)
				}
			}
		})
	}
}

// -- review round 1: a ledger that cannot be listed for its sockets is reported ---

type peekFailsLedger struct {
	capacityLedger
	err error
}

func (p peekFailsLedger) Peek() (capacity.Snapshot, error) { return capacity.Snapshot{}, p.err }

func TestObserveReportsALedgerThatCouldNotBePeeked(t *testing.T) {
	base, _ := useLedger(t)
	peekErr := errors.New("the ledger was replaced mid-read")
	setLedgerSeam(t, func() capacityLedger { return peekFailsLedger{capacityLedger: base, err: peekErr} })
	recordSpawns(t)
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})

	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}))

	if !strings.Contains(log, "dispatch: capacity: the ledger could not be read to list its sockets ("+peekErr.Error()+")") {
		t.Errorf("log does not report the failed ledger read:\n%s", log)
	}
}

// -- review round 2: a read that lost every race is "ledger unreadable" ----------

type racedLedger struct{ capacityLedger }

func (racedLedger) Peek() (capacity.Snapshot, error) { return capacity.Snapshot{}, capacity.ErrRaced }

func (racedLedger) Admit(capacity.Request) (capacity.Decision, error) {
	return capacity.Decision{}, capacity.ErrRaced
}

func TestARacedLedgerReadAdmitsNothingAndStatusReportsItUnreadable(t *testing.T) {
	base, _ := useLedger(t)
	setLedgerSeam(t, func() capacityLedger { return racedLedger{base} })
	spawned := recordSpawns(t)
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	wantReason := "capacity ledger unreadable: " + base.Path() + ": " + capacity.ErrRaced.Error()

	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}))
	st := ReadCapacityStatus(cfg)

	assertLaunched(t, spawned)
	assertSkipped(t, log, "o/r", 42, wantReason)
	if st.Err != wantReason {
		t.Errorf("status error = %q, want %q", st.Err, wantReason)
	}
}

// -- review round 1: a ledger-sourced string is quoted in a log line -------------

func TestReconcileLogQuotesTheRepoOfAReleasedEntry(t *testing.T) {
	l, _ := useLedger(t)
	seedHeld(t, l, capacity.KindImplement, "o/wei rd", 9, "")
	useInventoryFn(t, func(string) (tmux.CleanupInventory, error) { return tmux.CleanupInventory{}, nil })
	var buf bytes.Buffer

	reconcileCapacity(&buf)

	if want := `reconcile: capacity: released the implement reservation of "o/wei rd#9"`; !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want it to contain %q", buf.String(), want)
	}
}

// -- a helper child is a real process -------------------------------------------

// The babysit helper hook re-execs this binary as one babysit supervisor tick that
// reaches the ledger, the limits, the daemon snapshot and tmux through the
// environment. Its `cenci run` self-exec is this binary again as a fake `cenci
// run`. The repair it launches holds a slot: a ci-repair entry confirmed to the
// worker's window, on the ledger a dispatch process reads.
func TestHelperBabysitProcessAdmitsItsRepairThroughTheEnvironment(t *testing.T) {
	h := newChainHarness(t)
	stateHome := t.TempDir()
	if err := os.Chmod(stateHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateHome)
	h.commitAndPushConfig(chainRepoConfigLean)
	setFleetAutomergeEnabled(t, true)
	h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
	h.seedPR(ghPR{
		Number: 103, HeadRefName: "main", HeadRefOID: gitTest(t, h.local, "rev-parse", "HEAD"), BaseRefName: "main",
		ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Files: []string{"base.txt"},
	})
	h.seedChecks(103, ghCheck{Bucket: "fail", Name: "build", State: "FAILURE"})
	installFakeTmux(t)
	runLog := filepath.Join(t.TempDir(), "run.log")

	cmd := capacityHelperCmd("babysit",
		capacityStateDirEnv+"="+t.TempDir(), capacityChdirEnv+"="+h.local, capacityRunLogEnv+"="+runLog)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("babysit helper: %v\n%s", err, out)
	}

	ran, _ := os.ReadFile(runLog)
	if !strings.HasPrefix(string(ran), "run ci-repair 103 ") {
		t.Fatalf("the fake `cenci run` recorded %q, want a ci-repair launch for PR 103\nhelper output:\n%s", ran, out)
	}
	l := capacity.NewLedger(filepath.Join(stateHome, "cenci", "capacity"), capacity.Options{})
	entries := heldEntries(t, l)
	if len(entries) != 1 {
		t.Fatalf("the ledger holds %+v, want the one ci-repair entry", entries)
	}
	if e := entries[0]; e.Kind != capacity.KindCIRepair || e.Repo != h.repo || e.Ticket != 103 || e.State != capacity.StateConfirmed || e.Window != "103-ci-repair" {
		t.Errorf("ledger entry = %+v, want %s#103 ci-repair confirmed to window 103-ci-repair", e, h.repo)
	}
}

// The dispatch helper hook re-execs this binary as a one-pass dispatch process
// that reaches the ledger and the tmux inventory through the environment, like a
// real one. Two helper passes against one ledger with cap 1, the first launch's
// window staying live: one launches and the other is refused.
func TestHelperDispatchProcessesShareOneLedgerThroughTheEnvironment(t *testing.T) {
	h := newChainHarness(t)
	stateHome := t.TempDir()
	if err := os.Chmod(stateHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateHome)
	setFakeTmuxWindows(t, installFakeTmux(t), "201-implement")
	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedPermission("octocat", "admin")
	h.seedIssue(201, "A", "", []string{"Refined"}, []string{"octocat"})
	h.seedIssue(202, "B", "", []string{"Refined"}, []string{"octocat"})
	configPath := filepath.Join(t.TempDir(), "config.json")
	body := fmt.Sprintf(`{"dispatch":{"concurrencyCap":1,"planRefined":true,"repos":[{"repo":%q,"dir":%q,"session":"chain-session"}]}}`, h.repo, h.local)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	launchLog := filepath.Join(t.TempDir(), "launches.log")

	var outs []string
	for range 2 {
		cmd := capacityHelperCmd("dispatch", capacityConfigEnv+"="+configPath, capacityLaunchLogEnv+"="+launchLog)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper: %v\n%s", err, out)
		}
		outs = append(outs, string(out))
	}

	launched, _ := os.ReadFile(launchLog)
	if got := strings.Count(string(launched), "launch "); got != 1 {
		t.Fatalf("two cap-1 helper passes launched %d workers (%q), want exactly 1\nfirst:\n%s\nsecond:\n%s", got, launched, outs[0], outs[1])
	}
	if !strings.Contains(outs[1], "skip: concurrency cap reached") {
		t.Errorf("the second pass did not log `skip: concurrency cap reached`:\n%s", outs[1])
	}
}
