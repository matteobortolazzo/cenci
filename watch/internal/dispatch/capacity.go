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
}

// The seams below are package variables, like runFn, so no test reaches a real
// ledger, tmux server or config file (watch/docs/test-isolation.md); TestMain
// installs hermetic ones.
var (
	// capacityLedgerFn returns the shared admission ledger.
	capacityLedgerFn = func() capacityLedger { return capacity.NewLedger(capacity.Dir(), capacity.Options{}) }
	// capacityLimitsFn reads the worker limits for one admission. Production
	// re-reads dispatch.concurrencyCap and dispatch.repos[].concurrencyCap from the
	// config file the pass resolved, so a lowered cap applies to the next admission.
	capacityLimitsFn = func(cfg Config) (capacity.Limits, error) { return capacity.LoadLimits(cfg.Path) }
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
	decision, err := a.ledger.Admit(capacity.Request{
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
// process runs: a one-shot pass clears it once it has exited, the daemon after a
// restart.
const reservedUntil = "it stays reserved while this process runs; once the process has exited the first complete inventory with no worker window frees it (the daemon: after a restart), or remove ledger.json after confirming no worker is live"

// release gives a permit back for a launch that failed before it created any
// window. A failed release is retried once, then reported with what remains: the
// entry stays reserved and holds the slot (see reservedUntil).
func (a *admitter) release(permit *capacity.Permit, number int) {
	if permit == nil {
		return
	}
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
