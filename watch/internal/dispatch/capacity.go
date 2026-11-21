package dispatch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/worktreeclean"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// Dispatch's side of the shared admission ledger (#1469): every launch reserves
// a slot with Admit between Decide and the spawn, hands the reservation to the
// worker's window with Confirm (or gives it back with Release when it can prove
// nothing was created), and the reconciler lets the ledger release the entries a
// complete tmux inventory proves gone. The ledger, its counting rule and the
// limits parser live in internal/capacity; this file only gathers what an
// admission reads before it takes the ledger lock (the daemon's sessions, the
// orphan count, the tmux inventory) and maps the answer onto skip reasons.

// capacityLedger is the admission ledger plus the path an "unreadable" line names.
// *capacity.Ledger is the one production implementation; the interface exists so
// a test can install a recording fake.
type capacityLedger interface {
	capacity.Admission
	Path() string
	// Owns reports whether an entry was launched by the live process pid, by the
	// rule the ledger's own adoption uses (pid and start time).
	Owns(e capacity.Entry, pid int) bool
}

// The seams below are package variables, like runFn, so no test reaches a real
// ledger, tmux server or config file (watch/docs/test-isolation.md); TestMain
// installs hermetic ones.
var (
	// capacityLedgerFn returns the shared admission ledger.
	capacityLedgerFn = func() capacityLedger { return capacity.NewLedger(capacity.Dir(), capacity.Options{}) }
	// capacityLimitsFn reads the worker limits for one admission. Production
	// re-reads the limits from the config file(s) on every admission, so a lowered
	// cap applies to the next one (loadAdmissionLimits).
	capacityLimitsFn = loadAdmissionLimits
	// defaultFleetConfigPathFn resolves the default fleet config path, the one file
	// babysit always reads its limits from.
	defaultFleetConfigPathFn = run.DefaultConfigPath
	// loadLimitsFn reads the limits of one config file.
	loadLimitsFn = capacity.LoadLimits
	// capacityInventoryFn reads one tmux socket's window inventory.
	capacityInventoryFn = tmux.ListCleanupInventory
	// capacityClock stamps inventory reads.
	capacityClock = time.Now
	// settleRetryPause is the pause before the one retry of a failed Release or
	// Confirm; tests set it to 0.
	settleRetryPause = 100 * time.Millisecond
)

// unreadableLedger is the reason an admitter reports when the ledger cannot be
// read or locked.
func unreadableLedger(ledger capacityLedger, err error) string {
	path := ledger.Path()
	if path == "" {
		path = "(unresolved)"
	}
	return fmt.Sprintf(reasonCapacityLedgerUnreadableFmt, path, err)
}

// loadAdmissionLimits is the production limits source, read at every admission.
// Babysit always reads the default fleet config, and a supervisor armed by the
// daemon cannot learn a dispatch process's --config flag, so a pass run on another
// config admits under the stricter of the two files' limits (capacity.Stricter): an
// alternate file can never raise dispatch above what babysit enforces. A pass on
// the default config reads that one file once.
//
// A default file that cannot be read or holds invalid limits is an error, returned
// unwrapped so the reason matches the one babysit records for the same file; an
// absent one is the default machine cap, and an unresolved default path (no home)
// is the same error babysit reports. Fail closed: the caller admits nothing.
func loadAdmissionLimits(cfg Config) (capacity.Limits, error) {
	limits, err := loadLimitsFn(cfg.Path)
	if err != nil {
		return capacity.Limits{}, err
	}
	def := defaultFleetConfigPathFn()
	if def != "" && samePath(cfg.Path, def) {
		return limits, nil
	}
	defLimits, err := loadLimitsFn(def)
	if err != nil {
		return capacity.Limits{}, err
	}
	return capacity.Stricter(limits, defLimits), nil
}

// samePath reports whether a and b name one file by their absolute, cleaned
// spelling. A path that cannot be made absolute (the working directory is gone)
// compares as it is cleaned; a mismatch only means both files are read.
func samePath(a, b string) bool {
	abs := func(p string) string {
		if v, err := filepath.Abs(p); err == nil {
			return v
		}
		return filepath.Clean(p)
	}
	return abs(a) == abs(b)
}

// -- observation --------------------------------------------------------------

// sessionsOf lists the snapshot's windows as capacity sessions. Only a window in
// the running status counts against the cap, the rule Summary.Running uses.
func sessionsOf(snap *watch.StateSnapshot) []capacity.Session {
	sessions := make([]capacity.Session, 0, len(snap.Windows))
	for _, w := range snap.Windows {
		sessions = append(sessions, capacity.Session{Socket: w.Socket, PaneID: w.PaneID, Window: w.WindowName, Running: w.Status == "running"})
	}
	return sessions
}

// readCapacityInventory reads the tmux inventory of each socket, every one
// stamped with the time just before its listing began (a ledger entry that
// changed after that cannot be released by it). A socket with no server is a
// complete, empty inventory. A listing that failed, or dropped a row, is
// incomplete: it proves nothing and the entries on it are kept. The notes say
// which socket could not be read and why.
func readCapacityInventory(sockets []string) (capacity.Inventory, []string) {
	inv := capacity.Inventory{}
	var notes []string
	for _, socket := range sockets {
		readAt := capacityClock()
		listing, err := capacityInventoryFn(socket)
		switch {
		case errors.Is(err, tmux.ErrNoServer):
			inv[socket] = capacity.SocketInventory{Complete: true, ReadAt: readAt}
		case err != nil:
			inv[socket] = capacity.SocketInventory{ReadAt: readAt}
			notes = append(notes, fmt.Sprintf("capacity: the tmux inventory of socket %q is unreadable (%v); reservations on it are kept", socket, err))
		case len(listing.Dropped) > 0:
			inv[socket] = capacity.SocketInventory{ReadAt: readAt}
			notes = append(notes, fmt.Sprintf("capacity: the tmux inventory of socket %q dropped %d malformed row(s) (%s); reservations on it are kept", socket, len(listing.Dropped), listing.Dropped[0]))
		default:
			windows := make([]capacity.Window, 0, len(listing.Windows))
			for _, w := range listing.Windows {
				ids := make([]string, 0, len(w.Panes))
				for _, p := range w.Panes {
					ids = append(ids, p.ID)
				}
				windows = append(windows, capacity.Window{Name: w.Name, Dir: w.RepoDir, PaneIDs: ids, AllDead: w.AllDead()})
			}
			inv[socket] = capacity.SocketInventory{Complete: true, ReadAt: readAt, Windows: windows}
		}
	}
	return inv, notes
}

// reconcileCapacity lets the ledger release the entries a complete tmux inventory
// proves gone, so the next admitter, whoever it is, sees the freed slots (P20).
// A failure is reported and changes nothing: an unreadable ledger keeps every
// entry, and so does an inventory that failed or dropped a row (P7). A ledger with
// no entry has nothing to judge and costs no tmux read.
func reconcileCapacity(out io.Writer) {
	ledger := capacityLedgerFn()
	snap, err := ledger.Peek()
	if err != nil {
		logf(out, "reconcile: %s\n", unreadableLedger(ledger, err))
		return
	}
	if len(snap.Entries) == 0 {
		return
	}
	inv, notes := readCapacityInventory(snap.Sockets())
	for _, note := range notes {
		logf(out, "reconcile: %s\n", note)
	}
	released, err := ledger.Reconcile(inv)
	if err != nil {
		logf(out, "reconcile: %s\n", unreadableLedger(ledger, err))
		return
	}
	for _, e := range released {
		// The repo and window come from the ledger file: quoted, so a value with odd
		// characters cannot forge or reshape a log line.
		logf(out, "reconcile: capacity: released the %s reservation of %q (no worker window %q)\n", e.Kind, fmt.Sprintf("%s#%d", e.Repo, e.Ticket), e.Window)
	}
}

// -- admission ----------------------------------------------------------------

// admitter is one pass's admission state: the observation every Admit of the
// pass shares, read once at the first admission.
type admitter struct {
	cfg       Config
	deps      dispatchDeps
	ledger    capacityLedger
	socket    string
	dirByRepo map[string]string
	out       io.Writer

	observed bool
	obs      capacity.Observation
	// unavailable is the reason the host's load could not be observed: every
	// admission of the pass is refused with it.
	unavailable string
}

func newAdmitter(cfg Config, deps dispatchDeps, socket string, dirByRepo map[string]string, out io.Writer) *admitter {
	return &admitter{cfg: cfg, deps: deps, ledger: capacityLedgerFn(), socket: socket, dirByRepo: dirByRepo, out: out}
}

// observe reads, once per pass and before any ledger lock is taken, what the
// counting rule needs besides the ledger: the daemon's sessions, the orphaned
// container sessions and the inventory of the ledger's sockets. A nil snapshot or
// an orphan count that failed admits nothing, like Decide's own gates.
func (a *admitter) observe() (capacity.Observation, string) {
	if a.observed {
		return a.obs, a.unavailable
	}
	a.observed = true
	switch {
	case a.deps.Snapshot == nil:
		a.unavailable = fmt.Sprintf(reasonCapacityObservationUnavailableFmt, "daemon snapshot")
	case a.deps.ContainerSessionProbe != ContainerSessionProbeOK:
		a.unavailable = fmt.Sprintf(reasonCapacityObservationUnavailableFmt, "orphaned container sessions")
	}
	if a.unavailable != "" {
		return a.obs, a.unavailable
	}
	// An unreadable ledger is also reported by the first Admit, as the skip reason;
	// here the failed read is logged too, so a transient one (the ledger replaced
	// mid-read) is never dropped silently, and the pass goes on with an empty
	// inventory, which releases nothing.
	inv := capacity.Inventory{}
	snap, err := a.ledger.Peek()
	switch {
	case err != nil:
		logf(a.out, "dispatch: capacity: the ledger could not be read to list its sockets (%v)\n", err)
	case len(snap.Entries) > 0:
		var notes []string
		inv, notes = readCapacityInventory(snap.Sockets())
		for _, note := range notes {
			logf(a.out, "dispatch: %s\n", note)
		}
	}
	a.obs = capacity.Observation{Sessions: sessionsOf(a.deps.Snapshot), Orphans: a.deps.OrphanedContainerSessions, Inventory: inv}
	return a.obs, ""
}

// kindOf is the ledger kind a dispatch decision is charged as.
func kindOf(d Decision) capacity.Kind {
	switch {
	case d.Resume:
		return capacity.KindResume
	case d.Planning && d.Replan:
		return capacity.KindReplan
	case d.Planning:
		return capacity.KindPlan
	}
	return capacity.KindImplement
}

// windowNameOf is the deterministic window `cenci run` names a dispatched
// session's window: `<number>-implement`, the name the reconciler and the
// inventory join on.
func windowNameOf(number int) string { return strconv.Itoa(number) + "-implement" }

// admit reserves a slot for d, or returns the skip reason. Every failure to know
// the host's load is a refusal, never a grant: unreadable limits, an unreadable
// ledger, an unavailable observation.
func (a *admitter) admit(d Decision) (*capacity.Permit, string) {
	obs, unavailable := a.observe()
	if unavailable != "" {
		return nil, unavailable
	}
	limits, err := capacityLimitsFn(a.cfg)
	if err != nil {
		return nil, fmt.Sprintf(reasonCapacityLimitsUnreadableFmt, err)
	}
	dir, err := launchDir(a.dirByRepo[d.Ticket.Repo])
	if err != nil {
		return nil, "capacity launch target unresolved: resolving the launch directory: " + err.Error()
	}
	decision, err := a.admitInFlight(capacity.Request{
		Kind:   kindOf(d),
		Repo:   d.Ticket.Repo,
		Ticket: d.Ticket.Number,
		Branch: d.Ticket.Branch,
		Socket: a.socket,
		Window: windowNameOf(d.Ticket.Number),
		Dir:    dir,
		Limits: limits,
		Obs:    obs,
	})
	switch {
	case errors.Is(err, capacity.ErrInvalidRequest):
		return nil, "capacity request invalid: " + err.Error()
	case err != nil:
		return nil, unreadableLedger(a.ledger, err)
	case !decision.Admitted:
		return nil, decision.Reason
	}
	return &decision.Permit, ""
}

// admitInFlight is Admit that also registers the reservation as in flight (see
// inFlight) before any other pass of this process can look at the ledger.
func (a *admitter) admitInFlight(req capacity.Request) (capacity.Decision, error) {
	inFlight.Lock()
	defer inFlight.Unlock()
	decision, err := a.ledger.Admit(req)
	if err == nil && decision.Admitted {
		inFlight.holders[decision.Permit.HolderID] = struct{}{}
	}
	return decision, err
}

// admitDecisions runs Admit for every decision that would spawn and rewrites each
// refusal to an ActionSkip, so a refused ticket keeps exactly one skip line. It
// returns the permits by decision index. A ticket no Decide gate let through is
// never admitted, and neither is one the spawn loop would leave untouched for lack
// of a session verdict.
func (a *admitter) admitDecisions(decisions []Decision, eligibility map[string]SessionEligibility) map[int]*capacity.Permit {
	permits := map[int]*capacity.Permit{}
	for i, d := range decisions {
		if !spawns(d) || eligibility[d.Ticket.Repo] != SessionEligible {
			continue
		}
		permit, reason := a.admit(d)
		if permit == nil {
			decisions[i] = Decision{Ticket: d.Ticket, Action: ActionSkip, Reason: reason}
			continue
		}
		permits[i] = permit
	}
	return permits
}

// spawns reports whether applyDispatch's spawn loop acts on d: a dispatch of any
// kind. A planning pickup carries no plan by design (#828).
func spawns(d Decision) bool {
	return d.Action == ActionDispatch && (d.Plan != nil || d.Planning)
}

// settleOnce runs one Release or Confirm and, when it failed for a reason a second
// try can fix (capacity.IsTransient: the store was busy or briefly unreadable),
// once more after settleRetryPause. A permanent error (ErrNotHeld, a corrupt or
// unknown-schema ledger, an invalid request) is not retried: trying again cannot
// change it. It is called outside the ledger lock; each attempt takes the lock for
// itself.
func settleOnce(op func() error) error {
	return capacity.RetryTransient(settleRetryPause, op)
}

// reservedUntil says when an entry this process could not settle clears. The
// launcher of the entry is this process, and a `reserved` entry is released only
// when its launcher is dead, so an inventory alone never frees it while this
// process runs. The next non-dry-run pass of this process retries the settle from
// the tmux inventory (settleOwnReservations); a pass that cannot read the ledger,
// or an inventory that cannot say, leaves it, and once the process has exited the
// first complete inventory with no worker window frees it.
const reservedUntil = "it stays reserved until the next pass of this process retries the settle from the tmux inventory; once this process has exited the first complete inventory with no worker window frees it, or remove ledger.json after confirming no worker is live"

// inFlight is the set of reservations a pass of this process admitted and has not
// finished settling (release or confirm). settleOwnReservations skips them: an
// entry in the set is a launch still under way, not one a failed settle left
// behind, and the passes of one process are not always sequential (the daemon's
// combined loop and a test run several at once). The mutex also spans each Admit
// and its registration, so the self-heal never sees an admitted entry that is not
// yet in the set.
var inFlight = struct {
	sync.Mutex
	holders map[string]struct{}
}{holders: map[string]struct{}{}}

// settled ends a pass's hold on a permit, whatever the settle came to: an entry
// that is still reserved after a failed settle is the self-heal's from now on.
func settled(permit *capacity.Permit) {
	inFlight.Lock()
	defer inFlight.Unlock()
	delete(inFlight.holders, permit.HolderID)
}

// settleOwnReservations is the self-heal for a reservation this process could not
// settle (release or confirm failed twice, see reservedUntil). Such an entry is
// `reserved` and its launcher is this live process, so the ledger's release rules
// never free it: no inventory alone can, until the process exits. Left alone it
// would hold a slot for the process lifetime, which for the daemon is until a
// restart.
//
// It runs at the start of every non-dry-run pass, before the first Admit. It
// settles only a reserved dispatch entry (never a babysit repair) launched by this
// process, by the ledger's own ownership rule (pid and start time, so a reused pid
// is not this process) and not in flight: when the entry's worker window is in the
// tmux inventory it is confirmed, when a complete inventory read after the entry
// last changed shows none (a tmux server that is not running is such an inventory)
// it is released, and when the inventory cannot say (a socket not read, an
// incomplete listing, a read before the entry's updatedAt) it is left for a later
// pass. With no such entry it reads no inventory at all.
func settleOwnReservations(ledger capacityLedger, out io.Writer) {
	inFlight.Lock()
	snap, err := ledger.Peek()
	if err != nil {
		inFlight.Unlock()
		logf(out, "dispatch: capacity: the ledger could not be read to settle earlier reservations (%v)\n", err)
		return
	}
	pid := os.Getpid()
	var own []capacity.Entry
	for _, e := range snap.Entries {
		if e.State != capacity.StateReserved || e.Kind.Repair() {
			continue
		}
		if _, busy := inFlight.holders[e.HolderID]; busy || !ledger.Owns(e, pid) {
			continue
		}
		// Claimed, so a concurrent self-heal of this process cannot settle it too.
		inFlight.holders[e.HolderID] = struct{}{}
		own = append(own, e)
	}
	inFlight.Unlock()
	if len(own) == 0 {
		return
	}
	defer func() {
		inFlight.Lock()
		defer inFlight.Unlock()
		for _, e := range own {
			delete(inFlight.holders, e.HolderID)
		}
	}()

	var sockets []string
	for _, e := range own {
		if !slices.Contains(sockets, e.Socket) {
			sockets = append(sockets, e.Socket)
		}
	}
	inv, notes := readCapacityInventory(sockets)
	for _, note := range notes {
		logf(out, "dispatch: %s\n", note)
	}
	for _, e := range own {
		permit := capacity.Permit{HolderID: e.HolderID, Kind: e.Kind, Repo: e.Repo, Ticket: e.Ticket, Window: e.Window, State: e.State}
		switch e.WorkerIn(inv) {
		case capacity.WorkerAlive:
			confirmation := capacity.Confirmation{Socket: e.Socket, Window: e.Window, Dir: e.Dir}
			err := settleOnce(func() error { return ledger.Confirm(permit, confirmation) })
			switch {
			case errors.Is(err, capacity.ErrNotHeld):
				logf(out, "dispatch: capacity: the %s reservation is missing from the ledger (removed or reset); its worker window is running but is not counted by the ledger until the daemon sees it running\n", e.Kind)
			case err != nil:
				logf(out, "dispatch: capacity: the %s reservation could not be settled (%v); it stays reserved\n", e.Kind, err)
			default:
				logf(out, "dispatch: capacity: confirmed the %s reservation left reserved by an earlier failed settle; its worker window is running\n", e.Kind)
			}
		case capacity.WorkerAbsent:
			if err := settleOnce(func() error { return ledger.Release(permit) }); err != nil {
				logf(out, "dispatch: capacity: the %s reservation could not be settled (%v); it stays reserved\n", e.Kind, err)
				continue
			}
			logf(out, "dispatch: capacity: released the %s reservation left reserved by an earlier failed settle; no worker window exists\n", e.Kind)
		}
	}
}

// release gives a permit back for a launch that failed before it created any
// window. A failed release is retried once, then reported with what remains: the
// entry stays reserved and holds the slot (see reservedUntil).
func (a *admitter) release(permit *capacity.Permit, number int) {
	if permit == nil {
		return
	}
	defer settled(permit)
	if err := settleOnce(func() error { return a.ledger.Release(*permit) }); err != nil {
		logf(a.out, "dispatch: #%d capacity reservation could not be released (%v); %s\n", number, err, reservedUntil)
	}
}

// confirm hands a permit over to the worker's deterministic window. A failed
// confirm is retried once if it was transient, then reported: the entry stays
// reserved, which holds the slot (conservative; see reservedUntil). A permit that
// is no longer held is its own message: a reserved entry whose launcher (this
// process) is alive is never released by an inventory, so its entry is missing
// because ledger.json was removed, reset or replaced. Nothing stays reserved
// then, and the worker is counted only while the daemon sees it running.
func (a *admitter) confirm(permit *capacity.Permit, d Decision) {
	if permit == nil {
		return
	}
	defer settled(permit)
	number := d.Ticket.Number
	dir, err := launchDir(a.dirByRepo[d.Ticket.Repo])
	if err == nil {
		confirmation := capacity.Confirmation{Socket: a.socket, Window: windowNameOf(number), Dir: dir}
		err = settleOnce(func() error { return a.ledger.Confirm(*permit, confirmation) })
	}
	switch {
	case errors.Is(err, capacity.ErrNotHeld):
		logf(a.out, "dispatch: #%d capacity reservation is missing from the ledger (removed or reset); the worker is live but not counted by the ledger until the daemon sees it running\n", number)
	case err != nil:
		logf(a.out, "dispatch: #%d capacity reservation could not be confirmed (%v); %s\n", number, err, reservedUntil)
	}
}

// launchDir is the directory `cenci run` will name the worker's window with, the
// way internal/run's launchRepoDir computes it: the trimmed directory made
// absolute, else the working directory. The ledger matches a window by directory,
// so an entry recorded raw (a relative or empty repos[].dir) would never match its
// own live window.
func launchDir(dir string) (string, error) {
	if dir = strings.TrimSpace(dir); dir != "" {
		return filepath.Abs(dir)
	}
	return os.Getwd()
}

// spawnTracker wraps the controller handed to runFn and records whether a tmux
// window creation was attempted. Never called: the launch definitely failed
// before any window existed, and the permit is released. Called, whatever came
// back (success, a failure, run.ErrWindowSpawned), a window may exist and the
// permit is confirmed.
type spawnTracker struct {
	run.Controller
	newWindowCalled bool
}

func (s *spawnTracker) NewWindow(socket, session, name, shellCommand string) error {
	s.newWindowCalled = true
	return s.Controller.NewWindow(socket, session, name, shellCommand)
}

// -- `cenci dispatch status` ----------------------------------------------------

// CapacityStatus is what `cenci dispatch status` prints about the host's slots.
// Used is meaningful only when Err is empty; Err is the concrete reason the
// machine count could not be known.
type CapacityStatus struct {
	Used  int
	Total int
	Err   string
	Repos []RepoCapacity
}

// RepoCapacity is one capped repo's slots: Cap is the effective cap (the lower of
// the repo's and the machine's). Used is the repo's ledger entries and is meaningful
// only when Known; Err says why it is not.
type RepoCapacity struct {
	Repo  string
	Used  int
	Cap   int
	Known bool
	Err   string
}

// statusSnapshotTimeout bounds the one daemon snapshot read of a status call.
const statusSnapshotTimeout = 5 * time.Second

// ReadCapacityStatus reads the capacity picture `cenci dispatch status` prints,
// with the counting rule admission uses (capacity.CountUsage) and without taking
// a lock or creating anything: the ledger is read with Peek. When the machine
// count cannot be known, Err says exactly why (the daemon snapshot, the orphan
// count, the limits or the ledger) and the command still exits 0.
func ReadCapacityStatus(cfg Config) CapacityStatus {
	st := CapacityStatus{Total: cfg.ConcurrencyCap}
	limits, err := capacityLimitsFn(cfg)
	if err != nil {
		st.Err = fmt.Sprintf(reasonCapacityLimitsUnreadableFmt, err)
		return st
	}
	st.Total = limits.Machine

	ledger := capacityLedgerFn()
	snap, err := ledger.Peek()
	if err != nil {
		st.Err = unreadableLedger(ledger, err)
		st.Repos = repoCapacities(cfg, limits, nil, st.Err)
		return st
	}
	st.Repos = repoCapacities(cfg, limits, snap.Entries, "")

	daemon, err := worktreeclean.ReadSnapshotWithin(watch.DefaultSocketPath(), statusSnapshotTimeout)
	if err != nil {
		st.Err = fmt.Sprintf(reasonCapacityObservationUnavailableFmt, "daemon snapshot")
		return st
	}
	orphans, err := containerSessionProbeFn()
	if err != nil {
		st.Err = fmt.Sprintf(reasonCapacityObservationUnavailableFmt, "orphaned container sessions")
		return st
	}
	var inv capacity.Inventory
	if len(snap.Entries) > 0 {
		inv, _ = readCapacityInventory(snap.Sockets())
	}
	st.Used = capacity.CountUsage(snap.Entries, capacity.Observation{Sessions: sessionsOf(daemon), Orphans: orphans, Inventory: inv}).Machine
	return st
}

// repoCapacities lists the repos that have a repo cap, once each, in config order.
// entries nil with a reason means the ledger could not be read.
func repoCapacities(cfg Config, limits capacity.Limits, entries []capacity.Entry, unreadable string) []RepoCapacity {
	usage := capacity.CountUsage(entries, capacity.Observation{})
	var out []RepoCapacity
	var seen []string
	for _, rc := range cfg.Repos {
		repoCap, ok := limits.EffectiveRepoCap(rc.Repo)
		key := strings.ToLower(rc.Repo)
		if !ok || slices.Contains(seen, key) {
			continue
		}
		seen = append(seen, key)
		out = append(out, RepoCapacity{Repo: rc.Repo, Cap: repoCap, Used: usage.Repo(rc.Repo), Known: unreadable == "", Err: unreadable})
	}
	return out
}
