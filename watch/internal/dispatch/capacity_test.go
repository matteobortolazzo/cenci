package dispatch

// Admission of dispatch launches against the shared capacity ledger (#1469).
// Every test here drives applyDispatch over a ledger directory the test owns and
// asserts what a user can see: which tickets launched, the one skip line each
// refused ticket gets, and which entries the ledger still holds. A recording
// ledger is used only where the criterion is itself about the calls (a ticket a
// Decide gate skips makes none).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	cfg, err := LoadConfig(fleetFile(t, machine, repos...))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

// fleetFile writes a fleet config.json with the machine cap and the repos (each
// with an optional repo cap) into a directory of its own and returns its path.
func fleetFile(t *testing.T, machine int, repos ...capRepo) string {
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
	return path
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
				writeLedger(t, dir, `{"schema":3,"entries":[],"waits":[]}`)
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
	const staysReserved = "it stays reserved until the next pass of this process retries the settle"
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
			wantNotInLog: []string{"an inventory proving no worker frees it", "it stays reserved while this process runs"},
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

// -- #1578 item 1: under --config, dispatch admits under the stricter limits ------

// useAdmissionLimits installs the production limits source for one test, with the
// default fleet config at defaultPath and a counting reader, and returns the paths
// the reader was asked for, in order.
func useAdmissionLimits(t *testing.T, defaultPath string) *[]string {
	t.Helper()
	prevLimits, prevDefault, prevLoad := capacityLimitsFn, defaultFleetConfigPathFn, loadLimitsFn
	t.Cleanup(func() { capacityLimitsFn, defaultFleetConfigPathFn, loadLimitsFn = prevLimits, prevDefault, prevLoad })
	loads := &[]string{}
	capacityLimitsFn = loadAdmissionLimits
	defaultFleetConfigPathFn = func() string { return defaultPath }
	loadLimitsFn = func(path string) (capacity.Limits, error) {
		*loads = append(*loads, path)
		return capacity.LoadLimits(path)
	}
	return loads
}

type liveWorker struct {
	repo   string
	ticket int
}

// holdLiveWorkers confirms one ledger entry per worker and makes tmux list each
// worker's window, so every one counts against the caps.
func holdLiveWorkers(t *testing.T, l *capacity.Ledger, workers ...liveWorker) {
	t.Helper()
	names := make([]string, 0, len(workers))
	for _, w := range workers {
		seedHeld(t, l, capacity.KindImplement, w.repo, w.ticket, "")
		names = append(names, fmt.Sprintf("%d-implement", w.ticket))
	}
	useLiveWindows(t, names...)
}

func loadedConfig(t *testing.T, path string) Config {
	t.Helper()
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

// Each limit binds in both directions: whichever of the default fleet config and
// the --config file is stricter wins, for the machine cap and per repo.
func TestAdmitUnderAnAlternateConfigTheStricterLimitWins(t *testing.T) {
	const reasonMachine, reasonRepo = "concurrency cap reached", "repo concurrency cap reached"
	tests := []struct {
		name                   string
		defMachine, altMachine int
		defRepos, altRepos     []capRepo
		live                   []liveWorker
		repo                   string
		wantSkip               string
	}{
		{name: "default 2, alt 5, two live workers", defMachine: 2, altMachine: 5, live: []liveWorker{{"o/r", 91}, {"o/r", 92}}, repo: "o/r", wantSkip: reasonMachine},
		{name: "default 3, alt 1, one live worker", defMachine: 3, altMachine: 1, live: []liveWorker{{"o/r", 91}}, repo: "o/r", wantSkip: reasonMachine},
		{name: "both allow it", defMachine: 3, altMachine: 3, live: []liveWorker{{"o/r", 91}}, repo: "o/r"},
		{
			name: "default A=2, alt A=1 is 1", defMachine: 9, altMachine: 9, defRepos: []capRepo{{name: "o/A", cap: intp(2)}}, altRepos: []capRepo{{name: "o/A", cap: intp(1)}},
			live: []liveWorker{{"o/A", 91}}, repo: "o/A", wantSkip: reasonRepo,
		},
		{
			name: "default B=1 with no cap for B in alt is 1", defMachine: 9, altMachine: 9, defRepos: []capRepo{{name: "o/B", cap: intp(1)}}, altRepos: []capRepo{{name: "o/B"}},
			live: []liveWorker{{"o/B", 91}}, repo: "o/B", wantSkip: reasonRepo,
		},
		{
			name: "no cap in default with alt C=1 is 1", defMachine: 9, altMachine: 9, defRepos: []capRepo{{name: "o/C"}}, altRepos: []capRepo{{name: "o/C", cap: intp(1)}},
			live: []liveWorker{{"o/C", 91}}, repo: "o/C", wantSkip: reasonRepo,
		},
		{
			name: "a repo cap of 2 in both lets a second worker in", defMachine: 9, altMachine: 9, defRepos: []capRepo{{name: "o/A", cap: intp(2)}}, altRepos: []capRepo{{name: "o/A", cap: intp(2)}},
			live: []liveWorker{{"o/A", 91}}, repo: "o/A",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, _ := useLedger(t)
			holdLiveWorkers(t, l, tt.live...)
			useAdmissionLimits(t, fleetFile(t, tt.defMachine, tt.defRepos...))
			altRepos := tt.altRepos
			if !slices.ContainsFunc(altRepos, func(r capRepo) bool { return r.name == tt.repo }) {
				altRepos = append(altRepos, capRepo{name: tt.repo})
			}
			cfg := loadedConfig(t, fleetFile(t, tt.altMachine, altRepos...))
			spawned := recordSpawns(t)

			log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: tt.repo, number: 1}))

			if tt.wantSkip == "" {
				assertLaunched(t, spawned, "1")
				return
			}
			assertLaunched(t, spawned)
			assertSkipped(t, log, tt.repo, 1, tt.wantSkip)
		})
	}
}

// With no default fleet config file, the default machine cap binds beside the
// alternate file's own: min(alt, 3).
func TestAdmitUnderAnAlternateConfigWithNoDefaultFileUsesTheDefaultMachineCap(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent", "config.json")
	for _, tc := range []struct {
		name       string
		altMachine int
		live       int
		wantSkip   bool
	}{
		{"alt above the default cap", 5, capacity.DefaultMachineCap, true},
		{"alt below the default cap", 1, 1, true},
		{"alt above the default cap with a slot left", 5, capacity.DefaultMachineCap - 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := useLedger(t)
			var workers []liveWorker
			for i := range tc.live {
				workers = append(workers, liveWorker{"o/r", 90 + i})
			}
			holdLiveWorkers(t, l, workers...)
			useAdmissionLimits(t, missing)
			cfg := loadedConfig(t, fleetFile(t, tc.altMachine, capRepo{name: "o/r"}))
			spawned := recordSpawns(t)

			log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 1}))

			if !tc.wantSkip {
				assertLaunched(t, spawned, "1")
				return
			}
			assertLaunched(t, spawned)
			assertSkipped(t, log, "o/r", 1, "concurrency cap reached")
		})
	}
}

// A default fleet config that cannot be read, or holds invalid limits, refuses
// every admission with the reason babysit records for the same file: the file's own
// LoadLimits error, unwrapped. An unresolved default path is the same refusal.
func TestAdmitUnderAnAlternateConfigRefusesWhenTheDefaultLimitsAreUnreadable(t *testing.T) {
	const prefix = "capacity limits unreadable: "
	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	tests := []struct {
		name    string
		path    func(t *testing.T) string
		wantErr string // "" derives it from LoadLimits of the path
	}{
		{"not json", func(t *testing.T) string { return write(t, "{ not json") }, ""},
		{"an invalid repo cap", func(t *testing.T) string {
			return write(t, `{"dispatch":{"repos":[{"repo":"o/r","concurrencyCap":-1}]}}`)
		}, ""},
		{"a path that is not resolved", func(*testing.T) string { return "" }, "the config path is unresolved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, _ := useLedger(t)
			path := tt.path(t)
			wantErr := tt.wantErr
			if wantErr == "" {
				_, err := capacity.LoadLimits(path)
				if err == nil {
					t.Fatalf("the fixture %q must make LoadLimits fail", path)
				}
				wantErr = err.Error()
			}
			useAdmissionLimits(t, path)
			cfg := loadedConfig(t, fleetFile(t, 5, capRepo{name: "o/r"}))
			spawned := recordSpawns(t)

			log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 1}, ticketRef{repo: "o/r", number: 2}))

			assertLaunched(t, spawned)
			assertSkipped(t, log, "o/r", 1, prefix+wantErr)
			assertSkipped(t, log, "o/r", 2, prefix+wantErr)
			if got := heldEntries(t, l); len(got) != 0 {
				t.Errorf("the ledger holds %+v after a refused admission, want nothing", got)
			}
		})
	}
}

// A --config path that resolves to the default fleet path reads the file once and
// admits as a run without the flag: the one file's limits, no second read.
func TestAdmitWithTheDefaultConfigPathReadsTheFileOnce(t *testing.T) {
	spellings := map[string]func(t *testing.T, path string) string{
		"the same path": func(_ *testing.T, path string) string { return path },
		"an unclean spelling": func(_ *testing.T, path string) string {
			return filepath.Dir(path) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(path)
		},
		"a relative spelling": func(t *testing.T, path string) string {
			t.Chdir(filepath.Dir(path))
			return filepath.Base(path)
		},
	}
	for name, spell := range spellings {
		t.Run(name, func(t *testing.T) {
			l, _ := useLedger(t)
			holdLiveWorkers(t, l, liveWorker{"o/r", 91})
			def := fleetFile(t, 2, capRepo{name: "o/r"})
			loads := useAdmissionLimits(t, def)
			cfg := loadedConfig(t, spell(t, def))
			spawned := recordSpawns(t)

			mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 1}))

			assertLaunched(t, spawned, "1")
			if len(*loads) != 1 {
				t.Errorf("the limits file was read %d times (%q), want once", len(*loads), *loads)
			}
		})
	}
}

// readConfig follows a symlink, like babysit does: a symlinked default that
// resolves to a regular file binds, one that resolves to something that is not a
// regular file refuses admission with the reason babysit records.
func TestAdmitUnderAnAlternateConfigFollowsASymlinkedDefault(t *testing.T) {
	t.Run("to a regular file", func(t *testing.T) {
		l, _ := useLedger(t)
		holdLiveWorkers(t, l, liveWorker{"o/r", 91})
		link := filepath.Join(t.TempDir(), "config.json")
		if err := os.Symlink(fleetFile(t, 1), link); err != nil {
			t.Fatal(err)
		}
		useAdmissionLimits(t, link)
		cfg := loadedConfig(t, fleetFile(t, 5, capRepo{name: "o/r"}))
		spawned := recordSpawns(t)

		log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 1}))

		assertLaunched(t, spawned)
		assertSkipped(t, log, "o/r", 1, "concurrency cap reached")
	})
	for _, tc := range []struct {
		name   string
		target func(t *testing.T) string
	}{
		{"to a directory", func(t *testing.T) string { return t.TempDir() }},
		{"to a FIFO", func(t *testing.T) string {
			fifo := filepath.Join(t.TempDir(), "fifo")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			return fifo
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := useLedger(t)
			link := filepath.Join(t.TempDir(), "config.json")
			if err := os.Symlink(tc.target(t), link); err != nil {
				t.Fatal(err)
			}
			_, err := capacity.LoadLimits(link)
			if err == nil {
				t.Fatal("the fixture must make LoadLimits fail")
			}
			useAdmissionLimits(t, link)
			cfg := loadedConfig(t, fleetFile(t, 5, capRepo{name: "o/r"}))
			spawned := recordSpawns(t)

			log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 1}))

			assertLaunched(t, spawned)
			assertSkipped(t, log, "o/r", 1, "capacity limits unreadable: "+err.Error())
			if got := heldEntries(t, l); len(got) != 0 {
				t.Errorf("the ledger holds %+v, want nothing", got)
			}
		})
	}
}

// A pause (a machine cap of 0) in either file admits nothing: the pause wins.
func TestAdmitUnderAnAlternateConfigAPauseWins(t *testing.T) {
	for _, tc := range []struct {
		name               string
		defMachine, altCap int
	}{{"alt paused, default 3", 3, 0}, {"default paused, alt 3", 0, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			useLedger(t)
			useAdmissionLimits(t, fleetFile(t, tc.defMachine))
			cfg := loadedConfig(t, fleetFile(t, tc.altCap, capRepo{name: "o/r"}))
			spawned := recordSpawns(t)

			log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 1}))

			assertLaunched(t, spawned)
			assertSkipped(t, log, "o/r", 1, "concurrency cap reached")
		})
	}
}

// A repo cap above the effective (stricter) machine cap is clamped to it: with the
// default at 2, a repo capped at 5 in the alternate file still gets 2 workers.
func TestAdmitUnderAnAlternateConfigClampsARepoCapToTheEffectiveMachineCap(t *testing.T) {
	useLedger(t)
	useAdmissionLimits(t, fleetFile(t, 2))
	cfg := loadedConfig(t, fleetFile(t, 5, capRepo{name: "o/A", cap: intp(5)}))
	spawned := recordSpawns(t)

	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/A", number: 1}, ticketRef{repo: "o/A", number: 2}, ticketRef{repo: "o/A", number: 3}))

	assertLaunched(t, spawned, "1", "2")
	assertSkipped(t, log, "o/A", 3, "concurrency cap reached")
}

// `cenci dispatch status --config <alt>` shows the effective machine cap and, for
// each repo this config lists, the effective repo cap. A repo capped only in the
// default file is not listed: this process cannot launch it.
func TestReadCapacityStatusShowsTheEffectiveLimitsUnderAnAlternateConfig(t *testing.T) {
	useLedger(t)
	useAdmissionLimits(t, fleetFile(t, 2,
		capRepo{name: "o/A", cap: intp(1)}, capRepo{name: "o/B", cap: intp(4)}, capRepo{name: "o/E", cap: intp(1)}))
	cfg := loadedConfig(t, fleetFile(t, 5,
		capRepo{name: "o/A", cap: intp(3)}, capRepo{name: "o/B"}, capRepo{name: "o/C"}, capRepo{name: "o/D", cap: intp(9)}))

	st := ReadCapacityStatus(cfg)

	if st.Total != 2 {
		t.Errorf("total = %d, want the effective machine cap 2", st.Total)
	}
	got := map[string]int{}
	var order []string
	for _, r := range st.Repos {
		got[r.Repo] = r.Cap
		order = append(order, r.Repo)
	}
	want := map[string]int{"o/A": 1, "o/B": 2, "o/D": 2}
	if len(got) != len(want) || strings.Join(order, ",") != "o/A,o/B,o/D" {
		t.Fatalf("repos listed = %v, want %v in config order (a repo capped only in the default file is not listed)", order, want)
	}
	for repo, c := range want {
		if got[repo] != c {
			t.Errorf("%s cap = %d, want %d", repo, got[repo], c)
		}
	}
}

// -- #1578 item 2: a dispatch pass heals its own failed settles ------------------

const (
	healedReleaseLine = "dispatch: capacity: released the implement reservation left reserved by an earlier failed settle; no worker window exists"
	healedConfirmLine = "dispatch: capacity: confirmed the implement reservation left reserved by an earlier failed settle; its worker window is running"
)

// leaveReserved runs one pass whose launch of ticket number settles badly twice,
// so the ledger keeps a `reserved` entry launched by this process (the state a
// failed Release or Confirm leaves behind), and returns that pass's log. confirm
// false: the launch failed before any window and its Release fails. confirm true:
// the spawn succeeded and its Confirm fails.
func leaveReserved(t *testing.T, base *capacity.Ledger, cfg Config, number int, confirm bool) string {
	t.Helper()
	noSettlePause(t)
	wrapped := &settleLedger{Ledger: base}
	if confirm {
		wrapped.failConfirms, wrapped.confirmErr = 99, capacity.ErrIO
		stubRunFn(t, func(run.Opts, run.Controller) error { return nil })
	} else {
		wrapped.failReleases = 99
		stubRunFn(t, func(run.Opts, run.Controller) error { return errors.New("resolving the agent: nothing configured") })
	}
	setLedgerSeam(t, func() capacityLedger { return wrapped })
	log, _ := runPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: number}), fakeController{})
	setLedgerSeam(t, func() capacityLedger { return base })
	entries := heldEntries(t, base)
	if len(entries) != 1 || entries[0].State != capacity.StateReserved || entries[0].Ticket != number {
		t.Fatalf("the failed settle left %+v, want one reserved entry for #%d\nlog:\n%s", entries, number, log)
	}
	return log
}

// countInventoryReads makes every socket's inventory a complete, empty one (no
// tmux server) and counts the reads.
func countInventoryReads(t *testing.T) *int {
	t.Helper()
	reads := new(int)
	useInventoryFn(t, func(string) (tmux.CleanupInventory, error) {
		*reads++
		return tmux.CleanupInventory{}, tmux.ErrNoServer
	})
	return reads
}

func useClock(t *testing.T, now func() time.Time) {
	t.Helper()
	prev := capacityClock
	capacityClock = now
	t.Cleanup(func() { capacityClock = prev })
}

// A Release that failed twice leaves a reserved entry of this process, which no
// inventory frees while the process lives. The next pass removes it before its
// first Admit, so the freed slot is usable at once: with cap 1, the next ticket
// launches. A tmux server that is not running is a complete, empty inventory.
func TestSelfHealReleasesAReservationLeftByAFailedRelease(t *testing.T) {
	base, _ := useLedger(t)
	cfg := capacityConfig(t, 1, capRepo{name: "o/r"})
	leaveReserved(t, base, cfg, 42, false)
	useInventoryFn(t, func(string) (tmux.CleanupInventory, error) { return tmux.CleanupInventory{}, tmux.ErrNoServer })
	spawned := recordSpawns(t)

	log := mustRunPass(t, cfg, capacityDeps(ticketRef{repo: "o/r", number: 43}))

	if !hasLine(log, healedReleaseLine) {
		t.Errorf("log has no %q:\n%s", healedReleaseLine, log)
	}
	assertLaunched(t, spawned, "43")
	entries := heldEntries(t, base)
	if len(entries) != 1 || entries[0].Ticket != 43 {
		t.Errorf("the ledger holds %+v, want only the new launch #43", entries)
	}
}

// A Confirm that failed twice leaves a reserved entry whose worker window exists:
// the next pass confirms it, and the slot stays held by the live worker.
func TestSelfHealConfirmsAReservationLeftByAFailedConfirmWhoseWindowIsRunning(t *testing.T) {
	base, _ := useLedger(t)
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	leaveReserved(t, base, cfg, 42, true)
	useLiveWindows(t, "42-implement")

	log := mustRunPass(t, cfg, capacityDeps())

	if !hasLine(log, healedConfirmLine) {
		t.Errorf("log has no %q:\n%s", healedConfirmLine, log)
	}
	entries := heldEntries(t, base)
	if len(entries) != 1 || entries[0].Ticket != 42 || entries[0].State != capacity.StateConfirmed {
		t.Errorf("the ledger holds %+v, want #42 confirmed", entries)
	}
}

// An inventory that cannot prove the worker gone leaves the entry reserved and
// logs nothing as released: a tmux read that failed, a listing that dropped a row,
// a listing read before the entry last changed.
func TestSelfHealKeepsAReservationTheInventoryCannotJudge(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"a tmux read that failed", func(t *testing.T) {
			useInventoryFn(t, func(string) (tmux.CleanupInventory, error) { return tmux.CleanupInventory{}, errors.New("tmux: timed out") })
		}},
		{"a listing that dropped a row", func(t *testing.T) {
			useInventoryFn(t, func(string) (tmux.CleanupInventory, error) {
				return tmux.CleanupInventory{Dropped: []string{"malformed row"}}, nil
			})
		}},
		{"a listing read before the entry last changed", func(t *testing.T) {
			countInventoryReads(t)
			useClock(t, func() time.Time { return time.Now().Add(-time.Hour) })
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, _ := useLedger(t)
			cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
			leaveReserved(t, base, cfg, 42, false)
			tt.setup(t)

			log := mustRunPass(t, cfg, capacityDeps())

			for _, bad := range []string{"released the", "confirmed the"} {
				if strings.Contains(log, bad) {
					t.Errorf("log says %q although the inventory proved nothing:\n%s", bad, log)
				}
			}
			if entries := heldEntries(t, base); len(entries) != 1 || entries[0].State != capacity.StateReserved {
				t.Errorf("the ledger holds %+v, want the one entry still reserved", entries)
			}
		})
	}
}

// A reserved entry whose launcher is another live process, and every babysit-kind
// entry, is never settled by the self-heal, and neither costs an inventory read. A
// confirmed entry of this process is not the self-heal's either.
func TestSelfHealNeverTouchesForeignOrBabysitEntriesAndReadsNothingForThem(t *testing.T) {
	base, _ := useLedger(t)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("starting the foreign launcher: %v", err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})
	limits := capacity.Limits{Machine: 99}
	foreign, err := base.Admit(capacity.Request{Kind: capacity.KindImplement, Repo: "o/r", Ticket: 7, Window: "7-implement", Launcher: sleeper.Process.Pid, Limits: limits})
	if err != nil || !foreign.Admitted {
		t.Fatalf("seeding the foreign entry: %+v, %v", foreign, err)
	}
	repair, err := base.Admit(capacity.Request{Kind: capacity.KindCIRepair, Repo: "o/r", Ticket: 8, Window: "8-ci-repair", Limits: limits})
	if err != nil || !repair.Admitted {
		t.Fatalf("seeding the babysit entry: %+v, %v", repair, err)
	}
	seedHeld(t, base, capacity.KindImplement, "o/r", 9, "")
	before := heldEntries(t, base)
	reads := countInventoryReads(t)
	cfg := capacityConfig(t, 99, capRepo{name: "o/r"})

	log := mustRunPass(t, cfg, capacityDeps())

	if *reads != 0 {
		t.Errorf("the self-heal read %d tmux inventories with no reserved dispatch entry of this process, want 0", *reads)
	}
	for _, bad := range []string{"released the", "confirmed the"} {
		if strings.Contains(log, bad) {
			t.Errorf("log says %q:\n%s", bad, log)
		}
	}
	after := heldEntries(t, base)
	if len(after) != len(before) {
		t.Fatalf("the ledger holds %+v, want the three seeded entries unchanged (%+v)", after, before)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("entry %d changed: %+v, want %+v", i, after[i], before[i])
		}
	}
}

// The self-heal runs at the start of every real pass, whichever entry point the
// pass comes through: `cenci dispatch --once` (RunOnce), `cenci dispatch loop`
// (dispatchTick, one tick of RunLoop) and the daemon's combined loop
// (combinedTick, one pass of runCombinedLoop). Each leaves a reserved entry of
// this process behind, then drives the real entry point over a real-git, fake-gh
// fixture with cap 1: the stale entry is gone from the ledger file, the heal line
// is logged, and the next ticket launches into the freed slot, which it can only
// do when the heal ran before that pass's first Admit.
func TestSelfHealRunsAtTheStartOfEveryRealPassEntryPoint(t *testing.T) {
	entryPoints := []struct {
		name string
		pass func(t *testing.T, configPath string, out *bytes.Buffer)
	}{
		{"RunOnce", func(t *testing.T, configPath string, out *bytes.Buffer) {
			cfg, err := LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, out); err != nil {
				t.Fatalf("RunOnce: %v\n%s", err, out.String())
			}
		}},
		{"dispatchTick", func(t *testing.T, configPath string, out *bytes.Buffer) {
			if err := dispatchTick(configPath, fakeController{}, &fakeMutator{}, out, ""); err != nil {
				t.Fatalf("dispatchTick: %v\n%s", err, out.String())
			}
		}},
		{"combinedTick", func(t *testing.T, configPath string, out *bytes.Buffer) {
			attention := make(chan watch.AttentionUpdate, 2)
			state := watch.DispatchState{DaemonRunning: true}
			var windows []watch.WindowState
			var headroom map[string]float64
			combinedTick(context.Background(), configPath, fakeController{}, &fakeMutator{}, out, &memStore{}, attention, &state, &windows, &headroom)
		}},
	}
	for _, ep := range entryPoints {
		t.Run(ep.name, func(t *testing.T) {
			base, _ := useLedger(t)
			loop := newFilterLoop(t, watch.StateSnapshot{}, filterPassIssue{number: 43})
			body := fmt.Sprintf(`{"dispatch":{"loopEnabled":true,"concurrencyCap":1,"repos":[{"repo":"o/r","dir":%q,"session":"test-session"}]}}`, loop.dir)
			writeFile(t, loop.path, body)
			leaveReserved(t, base, capacityConfig(t, 1, capRepo{name: "o/r"}), 42, false)
			useLiveWindows(t, "43-implement")
			spawned := recordSpawns(t)
			var buf bytes.Buffer

			ep.pass(t, loop.path, &buf)

			if !hasLine(buf.String(), healedReleaseLine) {
				t.Errorf("log has no %q:\n%s", healedReleaseLine, buf.String())
			}
			assertLaunched(t, spawned, "43")
			entries := heldEntries(t, base)
			if len(entries) != 1 || entries[0].Ticket != 43 {
				t.Errorf("the ledger holds %+v, want only the new launch #43 (the stale #42 reservation healed)\nlog:\n%s", entries, buf.String())
			}
		})
	}
}

// A dry run makes no ledger write and reads no inventory: the reserved entry stays
// exactly as the file holds it.
func TestSelfHealDoesNotRunOnADryRun(t *testing.T) {
	base, _ := useLedger(t)
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	leaveReserved(t, base, cfg, 42, false)
	reads := countInventoryReads(t)
	before, err := os.ReadFile(base.Path())
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := applyDispatch(cfg, capacityDeps(), fakeController{}, &fakeMutator{}, true, &buf); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	if *reads != 0 {
		t.Errorf("a dry run read %d tmux inventories, want 0", *reads)
	}
	if after, _ := os.ReadFile(base.Path()); string(after) != string(before) {
		t.Errorf("a dry run rewrote the ledger:\n%s", after)
	}
}

// A pass in this process that has reserved a slot and is still launching holds an
// entry that looks exactly like a failed settle. A concurrent pass in the same
// process must not settle it: here the second pass would release the slot of a
// launch that is about to be confirmed.
func TestSelfHealSkipsAReservationAnotherPassOfThisProcessIsStillSettling(t *testing.T) {
	base, _ := useLedger(t)
	reads := countInventoryReads(t)
	cfg := capacityConfig(t, 3, capRepo{name: "o/r"})
	started, proceed := make(chan struct{}), make(chan struct{})
	stubRunFn(t, func(run.Opts, run.Controller) error {
		close(started)
		<-proceed
		return nil
	})
	var launching bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := applyDispatch(cfg, capacityDeps(ticketRef{repo: "o/r", number: 42}), fakeController{}, &fakeMutator{}, false, &launching)
		done <- err
	}()
	<-started

	log := mustRunPass(t, cfg, capacityDeps())
	close(proceed)
	if err := <-done; err != nil {
		t.Fatalf("the launching pass: %v\n%s", err, launching.String())
	}

	if strings.Contains(log, "released the") || *reads != 0 {
		t.Errorf("the concurrent pass settled a reservation that is still in flight (%d reads):\n%s", *reads, log)
	}
	entries := heldEntries(t, base)
	if len(entries) != 1 || entries[0].Ticket != 42 || entries[0].State != capacity.StateConfirmed {
		t.Errorf("the ledger holds %+v, want #42 confirmed by the pass that launched it\nlog:\n%s", entries, launching.String())
	}
	if strings.Contains(launching.String(), "missing from the ledger") {
		t.Errorf("the launching pass lost its reservation:\n%s", launching.String())
	}
}

// The log of a settle that failed says the next pass of this process retries it.
func TestAFailedSettleSaysTheNextPassRetriesIt(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirm=%v", confirm), func(t *testing.T) {
			base, _ := useLedger(t)
			cfg := capacityConfig(t, 3, capRepo{name: "o/r"})

			log := leaveReserved(t, base, cfg, 42, confirm)

			if want := "the next pass of this process retries the settle"; !strings.Contains(log, want) {
				t.Errorf("log has no %q:\n%s", want, log)
			}
			if strings.Contains(log, "while this process runs") {
				t.Errorf("log still says the entry stays reserved while this process runs:\n%s", log)
			}
		})
	}
}
