package babysit

import (
	"context"
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
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// ledgerHandle is the admission ledger plus the path an "unreadable" line names.
// *capacity.Ledger is the one production implementation; the interface exists so
// a test can install a recording fake.
type ledgerHandle interface {
	capacity.Admission
	Path() string
	// Owns reports whether an entry was launched by the live process pid, by the
	// rule the ledger's own adoption uses (pid and start time).
	Owns(e capacity.Entry, pid int) bool
}

// The seams below are package variables, like command/execGh/tmuxPaneInventory,
// so no test reaches a real ledger, daemon, container runtime or tmux server
// (watch/docs/test-isolation.md); TestMain installs hermetic ones.
var (
	// capacityLedgerFn returns the shared admission ledger.
	capacityLedgerFn = defaultCapacityLedger
	// daemonSnapshotFn returns the running agent sessions of the daemon.
	daemonSnapshotFn = defaultDaemonSnapshot
	// orphanCountFn counts the orphaned container sessions.
	orphanCountFn = defaultOrphanCount
	// cleanupInventoryFn reads one tmux socket's window inventory.
	cleanupInventoryFn = tmux.ListCleanupInventory
	// limitsPath resolves the fleet config.json the limits are read from. It is
	// its own seam, not fleetConfigPath: that one disables automerge when "".
	limitsPath = defaultFleetConfigPath
	// capacityClock stamps inventory reads and wait records.
	capacityClock = time.Now
	// settleRetryPause is the pause before the one retry of a failed Release or
	// Confirm; tests set it to 0.
	settleRetryPause = 100 * time.Millisecond
)

// capacityWait is State.CapacityWait: why the repair is waiting, when it first
// waited and when this supervisor last refreshed the wait.
type capacityWait struct {
	Reason      string    `json:"reason"`
	Since       time.Time `json:"since"`
	RefreshedAt time.Time `json:"refreshedAt"`
}

func defaultCapacityLedger() ledgerHandle {
	return capacity.NewLedger(capacity.Dir(), capacity.Options{})
}

// daemonSnapshotTimeout bounds the one snapshot read of an admission.
const daemonSnapshotTimeout = 5 * time.Second

// defaultDaemonSnapshot dials the daemon's broadcast socket and reads one
// snapshot, giving up after daemonSnapshotTimeout. Any failure, a daemon that
// is not running included, is an error: admission fails closed on it.
func defaultDaemonSnapshot() ([]capacity.Session, error) {
	client, err := watch.Dial(watch.DefaultSocketPath())
	if err != nil {
		return nil, err
	}
	// Closing the connection also ends the reading goroutine on a timeout.
	defer func() { _ = client.Close() }()
	type result struct {
		snap *watch.StateSnapshot
		err  error
	}
	done := make(chan result, 1)
	go func() {
		snap, err := client.ReadSnapshot()
		done <- result{snap, err}
	}()
	timer := time.NewTimer(daemonSnapshotTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		return sessionsOf(r.snap), nil
	case <-timer.C:
		return nil, context.DeadlineExceeded
	}
}

// sessionsOf lists the snapshot's windows as capacity sessions. Only a window in
// the running status counts against the cap, the rule Summary.Running uses.
func sessionsOf(snap *watch.StateSnapshot) []capacity.Session {
	sessions := make([]capacity.Session, 0, len(snap.Windows))
	for _, w := range snap.Windows {
		sessions = append(sessions, capacity.Session{Socket: w.Socket, PaneID: w.PaneID, Window: w.WindowName, Running: w.Status == "running"})
	}
	return sessions
}

// orphanCountTimeout bounds the whole orphan count (every runtime, every
// container), on top of the per-call bounds inside sandbox.CountOrphanedSessions.
const orphanCountTimeout = 60 * time.Second

// defaultOrphanCount counts the orphaned container-side agent sessions the way
// dispatch's capacity gate does. No container runtime on PATH is a zero count:
// there is nowhere for an orphan to live. Every other error is returned.
func defaultOrphanCount() (int, error) {
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), orphanCountTimeout)
	defer cancel()
	return sandbox.CountOrphanedSessions(ctx, runtimes, io.Discard, io.Discard)
}

// repairOutcome is what one attempt to launch a branch-writing repair came to.
type repairOutcome int

const (
	// repairNone: nothing was launched and nothing is waiting.
	repairNone repairOutcome = iota
	// repairLaunched: the admission reserved a slot and `cenci run` was started.
	repairLaunched
	// repairWaiting: the admission refused the repair; it waits for capacity.
	repairWaiting
)

// chargedKind maps a launch workflow to the ledger kind it is charged as. Only
// the three branch writers are charged; babysit-attention is a human attention
// prompt and is not.
func chargedKind(workflow string) (capacity.Kind, bool) {
	switch workflow {
	case "ci-repair":
		return capacity.KindCIRepair, true
	case "merge-repair":
		return capacity.KindMergeRepair, true
	case "babysit-review":
		return capacity.KindBabysitReview, true
	}
	return "", false
}

// repairWindow is the deterministic window `cenci run` names a repair worker's
// window: `<PR>-<workflow>`, the name observeWorkers looks for.
func repairWindow(s *State, workflow string) string { return s.PR + "-" + workflow }

// pollInterval is the supervisor's base poll interval, with Run's one-minute
// floor.
func pollInterval(s *State) time.Duration {
	return max(time.Duration(s.IntervalSeconds)*time.Second, time.Minute)
}

// unreadableLedger is the reason an admitter reports when the ledger cannot be
// read or locked.
func unreadableLedger(ledger ledgerHandle, err error) string {
	path := ledger.Path()
	if path == "" {
		path = "(unresolved)"
	}
	return "capacity ledger unreadable: " + path + ": " + err.Error()
}

// readCapacityInventory reads the tmux inventory of the supervisor's own socket
// and of every socket the ledger names, each stamped with the time just before
// its listing began (a ledger entry that changed after that cannot be released by
// it). A socket with no server is a complete, empty inventory. A listing that
// failed, or dropped a row, is incomplete: it proves nothing and its entries are
// kept. The notes say which socket could not be read and why.
func readCapacityInventory(ledger ledgerHandle, own string, withOwn bool) (capacity.Inventory, []string) {
	var sockets []string
	if withOwn {
		sockets = append(sockets, own)
	}
	inv := capacity.Inventory{}
	var notes []string
	if snap, err := ledger.Peek(); err != nil {
		// Not fatal: the ledger sockets are only the ones to read besides our own,
		// and a ledger that cannot be read admits and releases nothing anyway. It is
		// still reported, never dropped silently.
		notes = append(notes, fmt.Sprintf("capacity: the capacity ledger could not be read to list its sockets (%v); only the supervisor's own socket is read", err))
	} else {
		for _, socket := range snap.Sockets() {
			if !slices.Contains(sockets, socket) {
				sockets = append(sockets, socket)
			}
		}
	}
	for _, socket := range sockets {
		readAt := capacityClock()
		listing, err := cleanupInventoryFn(socket)
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

// reconcileCapacity is every tick's housekeeping: it reads the inventory of the
// own socket and of each ledger socket and lets the ledger release the entries it
// proves gone, so the next admitter, whoever it is, sees the freed slots. A ledger
// with no entry has nothing to release and costs no tmux read. A failure is
// reported and changes nothing: an unreadable ledger keeps every entry, and the
// tick goes on.
//
// It also answers whether the ledger still holds a repair entry of this PR after
// the release (held), and whether the ledger could say (known): the quiet-tick
// source of State.RepairWorkerLive, which costs no read beyond the ones above.
func reconcileCapacity(s *State) (held, known bool) {
	ledger := capacityLedgerFn()
	snap, err := ledger.Peek()
	if err != nil {
		fmt.Printf("PR #%s: %s\n", s.PR, unreadableLedger(ledger, err))
		return false, false
	}
	if len(snap.Entries) == 0 {
		return false, true
	}
	inv, notes := readCapacityInventory(ledger, s.LaunchSocket, s.LaunchSession != "")
	for _, note := range notes {
		fmt.Printf("PR #%s: %s\n", s.PR, note)
	}
	released, err := ledger.Reconcile(inv)
	if err != nil {
		fmt.Printf("PR #%s: %s\n", s.PR, unreadableLedger(ledger, err))
		return false, false
	}
	released = append(released, settleOwnReservations(s, ledger, snap.Entries, released, inv)...)
	ticket, _ := strconv.Atoi(s.PR)
	for _, e := range snap.Entries {
		freed := slices.ContainsFunc(released, func(r capacity.Entry) bool { return r.HolderID == e.HolderID })
		if !freed && e.Kind.Repair() && e.Repo == s.Repo && e.Ticket == ticket {
			return true, true
		}
	}
	return false, true
}

// settleOwnReservations is the self-heal for a reservation this supervisor could
// not settle (settlePermit's Release or Confirm failed twice). Such an entry is
// `reserved` and its launcher is this live process, so the ledger's release rules
// never free it: no inventory alone can, until the supervisor exits. Left alone it
// would hold a slot, read as a repair in progress and refuse any later repair of
// this PR as `worker already reserved`.
//
// It runs at the start of a tick, before any admission, and the tick is
// synchronous, so nothing this supervisor launched is still in flight and an entry
// admitted in the same tick can never be met here. It settles only a reserved
// repair entry of this PR whose launcher is this process, by the ledger's own
// ownership rule (pid and start time, so a reused pid is not this process): when the inventory shows
// its worker window running it is confirmed, when a complete inventory read after
// the entry last changed shows none it is released, and when the inventory cannot
// say (a socket not read, an incomplete listing) it is left for a later tick. It
// returns the entries it released, so the caller counts them as freed.
func settleOwnReservations(s *State, ledger ledgerHandle, entries, freed []capacity.Entry, inv capacity.Inventory) (released []capacity.Entry) {
	ticket, _ := strconv.Atoi(s.PR)
	pid := os.Getpid()
	for _, e := range entries {
		if e.State != capacity.StateReserved || !e.Kind.Repair() || e.Repo != s.Repo || e.Ticket != ticket || !ledger.Owns(e, pid) {
			continue
		}
		if slices.ContainsFunc(freed, func(r capacity.Entry) bool { return r.HolderID == e.HolderID }) {
			continue
		}
		permit := capacity.Permit{HolderID: e.HolderID, Kind: e.Kind, Repo: e.Repo, Ticket: e.Ticket, Window: e.Window, State: e.State}
		switch e.WorkerIn(inv) {
		case capacity.WorkerAlive:
			confirmation := capacity.Confirmation{Socket: e.Socket, Window: e.Window, Dir: e.Dir}
			if err := settleOnce(func() error { return ledger.Confirm(permit, confirmation) }); err != nil {
				if errors.Is(err, capacity.ErrNotHeld) {
					fmt.Printf("PR #%s: capacity: the %s reservation is missing from the ledger (removed or reset); its worker window is running but is not counted by the ledger until the daemon sees it running\n", s.PR, e.Kind)
					continue
				}
				fmt.Printf("PR #%s: capacity: the %s reservation could not be settled (%v); it stays reserved\n", s.PR, e.Kind, err)
				continue
			}
			fmt.Printf("PR #%s: capacity: confirmed the %s reservation left reserved by an earlier failed settle; its worker window is running\n", s.PR, e.Kind)
		case capacity.WorkerAbsent:
			if err := settleOnce(func() error { return ledger.Release(permit) }); err != nil {
				fmt.Printf("PR #%s: capacity: the %s reservation could not be settled (%v); it stays reserved\n", s.PR, e.Kind, err)
				continue
			}
			fmt.Printf("PR #%s: capacity: released the %s reservation left reserved by an earlier failed settle; no worker window exists\n", s.PR, e.Kind)
			released = append(released, e)
		}
	}
	return released
}

// launchIdentity is the tmux socket and directory the ledger entry of a repair
// names: what `cenci run` will record for the window it opens, since the ledger
// matches a window by socket, name and directory. It mirrors internal/run's
// resolveSocket (an empty recorded socket is tmux.ResolveSocket) and
// launchRepoDir (the trimmed directory made absolute, else the working
// directory); this package cannot import internal/run. `cenci run` is a child of
// the supervisor, so it sees the same environment and working directory. An error
// means the value `cenci run` would use cannot be known, and the admission
// refuses rather than store one that cannot match.
//
// A recorded socket that is not a valid socket path is the one case that is not
// refused here: launch() re-checks it before any tmux call and fails with a typed
// error (tmux.ErrMalformedSocket, tmux.ErrUntrustedSocket) that tick surfaces,
// and settlePermit releases the reservation, so no window can ever exist for it.
// The reservation of that doomed attempt names the empty socket for the few
// moments it lives, never the malformed value, which the ledger would refuse and
// which has no place in a file every launcher reads.
func launchIdentity(s *State) (socket, dir string, err error) {
	socket = s.LaunchSocket
	switch {
	case socket == "":
		if socket, err = tmux.ResolveSocket(); err != nil {
			return "", "", fmt.Errorf("resolving the tmux socket: %w", err)
		}
	case !tmux.ValidSocket(socket):
		socket = ""
	}
	if dir = strings.TrimSpace(s.LaunchDir); dir != "" {
		dir, err = filepath.Abs(dir)
	} else {
		dir, err = os.Getwd()
	}
	if err != nil {
		return "", "", fmt.Errorf("resolving the launch directory: %w", err)
	}
	return socket, dir, nil
}

// admitRepair is the babysit admission, run immediately before a merge-repair,
// ci-repair or babysit-review launch. It returns the permit for the repair, or the
// reason the repair must wait. Every failure to know the host's load is a
// refusal, never a grant: an unreadable config or ledger, an unavailable daemon
// snapshot or orphan count.
//
// w is this PR's worker liveness, read by the tick just before: a live writer of
// this PR is refused here too, so the writer check and the capacity check cannot
// disagree. The ledger then checks, under its one lock, that no other worker holds
// this PR or this branch, the repo cap and the machine cap, and an admission also
// adopts this supervisor's own entry left by a killed predecessor.
func admitRepair(s *State, pr prView, kind capacity.Kind, w workerLiveness) (*capacity.Permit, string) {
	if w.any() {
		return nil, capacity.ReasonBranchWriter
	}
	ticket, err := strconv.Atoi(s.PR)
	if err != nil || ticket <= 0 {
		return nil, "capacity request invalid: the PR number is not a positive integer"
	}
	socket, dir, err := launchIdentity(s)
	if err != nil {
		return nil, "capacity launch target unresolved: " + err.Error()
	}
	limits, err := capacity.LoadLimits(limitsPath())
	if err != nil {
		return nil, "capacity limits unreadable: " + err.Error()
	}
	sessions, err := daemonSnapshotFn()
	if err != nil {
		fmt.Printf("PR #%s: capacity: reading the daemon snapshot failed: %v\n", s.PR, err)
		return nil, "capacity observation unavailable: daemon snapshot"
	}
	orphans, err := orphanCountFn()
	if err != nil {
		fmt.Printf("PR #%s: capacity: counting orphaned container sessions failed: %v\n", s.PR, err)
		return nil, "capacity observation unavailable: orphaned container sessions"
	}
	ledger := capacityLedgerFn()
	inv, notes := readCapacityInventory(ledger, s.LaunchSocket, s.LaunchSession != "")
	for _, note := range notes {
		fmt.Printf("PR #%s: %s\n", s.PR, note)
	}
	decision, err := ledger.Admit(capacity.Request{
		Kind: kind, Repo: s.Repo, Ticket: ticket, Branch: pr.HeadRefName,
		Socket: socket, Window: repairWindow(s, string(kind)), Dir: dir,
		Adopt:  true,
		Limits: limits,
		Obs:    capacity.Observation{Sessions: sessions, Orphans: orphans, Inventory: inv},
	})
	switch {
	case errors.Is(err, capacity.ErrInvalidRequest):
		return nil, "capacity request invalid: " + err.Error()
	case err != nil:
		return nil, unreadableLedger(ledger, err)
	case !decision.Admitted:
		return nil, decision.Reason
	}
	return &decision.Permit, ""
}

// recordCapacityWait records that the repair of kind is refused and waits: in the
// persisted state (what `cenci babysit status` reads) for every reason, and, only
// when the host is full (capacity.ReasonMachineCap), as a wait record in the
// ledger, which holds no slot but makes dispatch leave a free one for repairs. Any
// other refusal (a writer on the branch, the repo cap, an unreadable limit) is not
// a wait for a slot the repair could use, so it keeps no ledger wait, and one this
// PR already holds is dropped. The record is refreshed every tick the repair is
// still refused and expires after 3 poll intervals without a refresh. Nothing else
// changes: no attempt is spent, no feedback key moves.
func recordCapacityWait(s *State, kind capacity.Kind, reason string) {
	now := capacityClock().UTC()
	since := now
	if s.CapacityWait != nil && !s.CapacityWait.Since.IsZero() {
		since = s.CapacityWait.Since
	}
	s.CapacityWait = &capacityWait{Reason: reason, Since: since, RefreshedAt: now}
	fmt.Printf("PR #%s: %s; %s waits for capacity, no window opened\n", s.PR, reason, kind)
	ticket, _ := strconv.Atoi(s.PR)
	if reason != capacity.ReasonMachineCap {
		if err := capacityLedgerFn().DropWait(s.Repo, ticket); err != nil {
			fmt.Printf("PR #%s: the capacity wait could not be dropped from the ledger: %v\n", s.PR, err)
		}
		return
	}
	err := capacityLedgerFn().Wait(capacity.WaitRequest{Kind: kind, Repo: s.Repo, Ticket: ticket, Reason: reason, Interval: pollInterval(s)})
	if err != nil {
		fmt.Printf("PR #%s: the capacity wait could not be recorded in the ledger: %v\n", s.PR, err)
	}
}

// dropCapacityWait ends the wait: the repair launched, is no longer needed, or
// the PR merged, closed or was stopped. A failed ledger write is reported and
// harmless: the record expires on its own after 3 poll intervals.
func dropCapacityWait(s *State) {
	if s.CapacityWait == nil {
		return
	}
	s.CapacityWait = nil
	ticket, _ := strconv.Atoi(s.PR)
	if err := capacityLedgerFn().DropWait(s.Repo, ticket); err != nil {
		fmt.Printf("PR #%s: the capacity wait could not be dropped from the ledger: %v\n", s.PR, err)
	}
}

// settleOnce runs one Release or Confirm with the one bounded retry dispatch has
// (capacity.RetryTransient): a transient failure (the store busy or briefly
// unreadable) is retried once after settleRetryPause, a permanent one is not.
func settleOnce(op func() error) error {
	return capacity.RetryTransient(settleRetryPause, op)
}

// reservedWhileRunning says what a failed settle leaves behind. The launcher of
// the entry is this supervisor, and a `reserved` entry is released only when its
// launcher is dead, so no inventory alone frees it while the supervisor runs: it
// holds its slot and reads as a repair in progress until a later tick settles it
// (settleOwnReservations) or the supervisor exits.
//
// Only a transient failure is retried by the next tick. A permanent one (a
// corrupt, unknown-schema, over-size or untrusted ledger, an invalid request) makes
// the next tick's ledger read fail before the self-heal runs, so recovery is manual.
// An entry that is no longer held is already gone and holds nothing.
func reservedWhileRunning(err error) string {
	const staysReserved = "it stays reserved while this supervisor runs; "
	const afterExit = "a complete inventory with no worker window frees it once this supervisor has exited"
	switch {
	case errors.Is(err, capacity.ErrNotHeld):
		return "the entry is no longer in the ledger, so it holds no slot"
	case capacity.IsTransient(err):
		return staysReserved + "the next tick retries and settles it from the tmux inventory, and otherwise " + afterExit
	}
	return staysReserved + "the failure is permanent, so no tick retries it: see the capacity ledger unreadable line and the manual recovery in watch/README.md, otherwise " + afterExit
}

// settlePermit applies the outcome rule once launch() is done with a permit. A
// launch that failed before `cenci run` ran created nothing, so the slot is
// released. Once `cenci run` has run, whatever its exit status, a window may exist
// (exit 3 means it does), so the reservation is confirmed to the deterministic
// window name and only a later complete inventory that shows no such window
// releases it. A Release or Confirm that fails transiently is retried once; one
// that still fails leaves the entry reserved, which the next tick's self-heal
// (settleOwnReservations) settles.
func settlePermit(s *State, workflow string, permit *capacity.Permit, ran bool) {
	if permit == nil {
		return
	}
	ledger := capacityLedgerFn()
	if !ran {
		if err := settleOnce(func() error { return ledger.Release(*permit) }); err != nil {
			fmt.Printf("PR #%s: the %s capacity reservation could not be released (%v); %s\n", s.PR, workflow, err, reservedWhileRunning(err))
		}
		return
	}
	// The same socket and directory the admission recorded and `cenci run` used. A
	// failure to resolve them leaves the reservation as the admission stored it
	// (it already names them) rather than confirming it to a value that cannot
	// match the worker's window.
	socket, dir, err := launchIdentity(s)
	if err == nil {
		confirmation := capacity.Confirmation{Socket: socket, Window: repairWindow(s, workflow), Dir: dir}
		err = settleOnce(func() error { return ledger.Confirm(*permit, confirmation) })
	}
	switch {
	case errors.Is(err, capacity.ErrNotHeld):
		// A reserved entry whose launcher is alive is never freed by an inventory, so
		// the entry is missing because ledger.json was removed, reset or replaced.
		fmt.Printf("PR #%s: the %s capacity reservation is missing from the ledger (removed or reset); the worker, if one was started, is not counted by the ledger until the daemon sees it running\n", s.PR, workflow)
	case err != nil:
		fmt.Printf("PR #%s: the %s capacity reservation could not be confirmed (%v); %s\n", s.PR, workflow, err, reservedWhileRunning(err))
	}
}

// repairWorkerLive is State.RepairWorkerLive for one tick. A tick that probed the
// panes (observed) decides from what it saw. A quiet tick reads no inventory
// (#1457) and adds no tmux read for this: the ledger, after the tick's own release
// of the entries a complete inventory proves gone, says whether a repair of this
// PR is still held (heldInLedger), and when it cannot be read (ledgerKnown false)
// nothing proved the worker gone, so the previous verdict stands.
func repairWorkerLive(prev, launched bool, observed *workerLiveness, heldInLedger, ledgerKnown bool) bool {
	switch {
	case launched:
		return true
	case observed != nil:
		return observed.ciLive || observed.mergeLive || observed.reviewLive
	case ledgerKnown:
		return heldInLedger
	}
	return prev
}

// activityOf is the `activity` key of `cenci babysit status`, derived from the
// persisted state alone: needs-input, then repairing (a repair worker was live at
// the last tick), then waiting-for-capacity (a wait refreshed within 3 poll
// intervals), then monitoring.
func activityOf(s State, now time.Time) string {
	switch {
	case s.Status == "needs-input":
		return "needs-input"
	case s.RepairWorkerLive:
		return "repairing"
	case s.CapacityWait != nil && now.Before(s.CapacityWait.RefreshedAt.Add(capacity.WaitIntervals*pollInterval(&s))):
		return "waiting-for-capacity"
	}
	return "monitoring"
}
