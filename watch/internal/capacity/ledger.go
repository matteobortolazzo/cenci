package capacity

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// The admission ledger: one JSON file, ledger.json, in the capacity store. It
// holds one entry per launched worker (a dispatch pickup or a babysit repair)
// and one wait record per repair that found the host full. Every operation is a
// read-modify-write under the store's whole-store lock, so two dispatch passes,
// several babysit supervisors and the daemon agree on the machine's slots
// without a scheduler service.
//
// An entry is `reserved` from Admit until Confirm (or Release), then
// `confirmed`. It is released only when a complete tmux inventory proves the
// worker gone: a confirmed entry by the inventory alone, a reserved one also
// only once its launcher (pid plus start time) is dead. Everything the ledger
// cannot read or judge with certainty keeps the entry, and an unreadable ledger
// admits nothing and is never reset.
//
// The ledger does no subprocess or socket I/O: the tmux inventory, the daemon
// sessions and the orphan count arrive in the Observation the caller read
// before the lock, and the launcher probe is a pid lookup.

// Typed, path-free errors (see also the package's store errors).
var (
	// ErrTooLarge means the ledger is over its entry or byte bound. It is
	// refused as unreadable, never truncated.
	ErrTooLarge = errors.New("the capacity ledger is over its size bound")
	// ErrNotHeld means the permit's entry is no longer in the ledger.
	ErrNotHeld = errors.New("the capacity permit is no longer held")
	// ErrInvalidRequest means the request names no valid launch.
	ErrInvalidRequest = errors.New("the capacity request is invalid")
)

// Refusal reasons. They end up in dispatch skip lines and babysit wait reasons.
const (
	ReasonMachineCap     = "concurrency cap reached"
	ReasonRepoCap        = "repo concurrency cap reached"
	ReasonWorkerReserved = "worker already reserved"
	ReasonBranchWriter   = "branch writer active"
)

const (
	ledgerFile   = "ledger.json"
	ledgerSchema = 1
	// maxLedgerBytes is the ledger's own read limit (the store's default stays
	// 4096 for daily-quota.json), and a write over it is refused so the ledger can
	// never grow past what it can read back.
	maxLedgerBytes = 256 << 10
	maxEntries     = 512
	maxWaits       = 512
	// maxFieldBytes bounds the repo, branch, window and socket strings of an
	// entry (a unix socket path is under 108 bytes), and maxDirBytes its
	// directory (PATH_MAX). The ledger's strings end up in log and status lines, so
	// they are bounded and carry no control character.
	maxFieldBytes = 512
	maxDirBytes   = 4096
	// ledgerLockTimeout is the ledger's lock timeout; DailyQuota keeps the
	// store's 3s default.
	ledgerLockTimeout = 5 * time.Second
)

// WaitIntervals is how many poll intervals a wait record survives without a
// refresh. `cenci babysit status` applies the same rule to its persisted state.
const WaitIntervals = 3

// Kind is the kind of worker an entry holds a slot for.
type Kind string

// The seven worker kinds. The first four are launched by dispatch, the last
// three by a babysit supervisor.
const (
	KindImplement     Kind = "implement"
	KindPlan          Kind = "plan"
	KindReplan        Kind = "replan"
	KindResume        Kind = "resume"
	KindCIRepair      Kind = "ci-repair"
	KindMergeRepair   Kind = "merge-repair"
	KindBabysitReview Kind = "babysit-review"
)

func (k Kind) valid() bool {
	switch k {
	case KindImplement, KindPlan, KindReplan, KindResume, KindCIRepair, KindMergeRepair, KindBabysitReview:
		return true
	}
	return false
}

// Repair reports whether k is a babysit repair launch, which is admitted when
// one slot is free and gets priority over new dispatch pickups.
func (k Kind) Repair() bool {
	return k == KindCIRepair || k == KindMergeRepair || k == KindBabysitReview
}

// State is an entry's lifecycle state.
type State string

const (
	// StateReserved is the slot held between Admit and Confirm.
	StateReserved State = "reserved"
	// StateConfirmed is a slot handed over to a worker's tmux window.
	StateConfirmed State = "confirmed"
)

// Entry is one held slot.
type Entry struct {
	HolderID string `json:"holderId"`
	Kind     Kind   `json:"kind"`
	Repo     string `json:"repo"`
	// Ticket is the ticket number for dispatch and the PR number for babysit.
	Ticket int    `json:"ticket"`
	Branch string `json:"branch"`
	// LauncherPID and LauncherStart identify the launching process; the start
	// time is empty where it cannot be read, and liveness alone decides then.
	LauncherPID   int    `json:"launcherPid"`
	LauncherStart string `json:"launcherStart"`
	// Socket, Window and Dir locate the worker's tmux window. A reserved entry
	// holds the expected ones from the request, and Confirm sets the real ones.
	Socket    string    `json:"socket"`
	Window    string    `json:"window"`
	Dir       string    `json:"dir"`
	LaunchID  string    `json:"launchId"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is the last Admit or Confirm: an inventory read before it cannot
	// release the entry.
	UpdatedAt time.Time `json:"updatedAt"`
}

// Wait is a repair that found the host full. It holds no slot; it makes
// dispatch leave a free slot for repairs, and it expires unless refreshed.
type Wait struct {
	Repo        string    `json:"repo"`
	Ticket      int       `json:"ticket"`
	Kind        Kind      `json:"kind"`
	Reason      string    `json:"reason"`
	Since       time.Time `json:"since"`
	RefreshedAt time.Time `json:"refreshedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// ledgerState is the persisted file: {"schema":1,"entries":[...],"waits":[...]}.
type ledgerState struct {
	Schema  int     `json:"schema"`
	Entries []Entry `json:"entries"`
	Waits   []Wait  `json:"waits"`
}

var (
	entryKeys = []string{"holderId", "kind", "repo", "ticket", "branch", "launcherPid", "launcherStart", "socket", "window", "dir", "launchId", "state", "createdAt", "updatedAt"}
	waitKeys  = []string{"repo", "ticket", "kind", "reason", "since", "refreshedAt", "expiresAt"}
)

// Snapshot is a read of the ledger.
type Snapshot struct {
	Entries []Entry
	Waits   []Wait
}

// LiveWaits counts the wait records not yet expired at now.
func (s Snapshot) LiveWaits(now time.Time) int {
	n := 0
	for _, w := range s.Waits {
		if w.ExpiresAt.After(now) {
			n++
		}
	}
	return n
}

// Sockets lists, sorted and once each, the tmux sockets the entries name: the
// inventories a caller must read for Reconcile or Admit to judge them.
func (s Snapshot) Sockets() []string {
	out := make([]string, 0, len(s.Entries))
	for _, e := range s.Entries {
		if !slices.Contains(out, e.Socket) {
			out = append(out, e.Socket)
		}
	}
	slices.Sort(out)
	return out
}

// Request is one admission. Limits and Obs are read by the caller just before
// the call; Admit takes no other input and does no I/O besides the ledger.
type Request struct {
	Kind   Kind
	Repo   string // owner/name
	Ticket int    // ticket number for dispatch, PR number for babysit
	// Branch is the branch the worker will write. Empty (a fresh pickup with no
	// recorded branch) skips the branch-writer check; the ticket check still
	// applies.
	Branch string
	// Socket, Window and Dir are the worker's expected tmux identity: the socket
	// it will run on, its deterministic window name and the repo directory.
	// Reserved entries are released by looking for this window.
	Socket, Window, Dir string
	LaunchID            string
	// Launcher is the launching process; 0 means the calling process.
	Launcher int
	// Adopt lets a babysit supervisor take over its own entry for the same repo,
	// PR and kind whose launcher is dead (a restart) instead of admitting a
	// second one. A live foreign launcher's entry is never adopted.
	Adopt  bool
	Limits Limits
	Obs    Observation
}

func (r Request) validate() error {
	switch {
	case !r.Kind.valid():
		return fmt.Errorf("%w: unknown kind", ErrInvalidRequest)
	case r.Repo == "":
		return fmt.Errorf("%w: no repo", ErrInvalidRequest)
	case r.Ticket <= 0:
		return fmt.Errorf("%w: no ticket", ErrInvalidRequest)
	case r.Launcher < 0 || r.Launcher > math.MaxInt32:
		return fmt.Errorf("%w: the launcher pid is out of range", ErrInvalidRequest)
	case !cleanIdentity(r.Repo, r.Branch, r.Window, r.Socket, r.Dir) || !cleanField(r.LaunchID, maxFieldBytes):
		return fmt.Errorf("%w: %s", ErrInvalidRequest, badFieldReason)
	}
	return nil
}

// badFieldReason is why a request or confirmation field is refused. The identity
// strings are checked as the read side checks them (Entry.invalid), so a ledger
// this build writes is never one it cannot read back; the launch id gets the same
// bound because it ends up in the same file.
const badFieldReason = "a field is over-long, holds a control character or is not valid UTF-8"

// Permit is a held slot, handed back by an admitted Admit. It is passed to
// Confirm and Release. For an adopted entry, State says how far the original
// launch got: a reserved entry may or may not have spawned, so the adopter
// decides by observing the worker, never by the permit alone.
type Permit struct {
	HolderID string
	Kind     Kind
	Repo     string
	Ticket   int
	Window   string
	State    State
	Adopted  bool
}

// Decision is the outcome of one Admit. Reason is set when it is not admitted.
type Decision struct {
	Admitted bool
	Reason   string
	Permit   Permit
}

// Confirmation hands a reservation over to the worker's window.
type Confirmation struct {
	Socket, Window, Dir string
	LaunchID            string
}

// WaitRequest records or refreshes a repair's wait. The record expires 3
// Intervals after this call unless refreshed.
type WaitRequest struct {
	Kind     Kind
	Repo     string
	Ticket   int
	Reason   string
	Interval time.Duration
}

// Admission is the one interface dispatch and babysit share.
type Admission interface {
	Admit(Request) (Decision, error)
	Confirm(Permit, Confirmation) error
	Release(Permit) error
	Reconcile(Inventory) ([]Entry, error)
	Wait(WaitRequest) error
	DropWait(repo string, ticket int) error
	Peek() (Snapshot, error)
}

var _ Admission = (*Ledger)(nil)

// Options inject the ledger's environment. The zero value is production: the
// wall clock, the /proc-based launcher probe and the 5s lock timeout.
type Options struct {
	Now func() time.Time
	// Probe reports a process's start time (empty where unavailable) and whether
	// it is alive.
	Probe       func(pid int) (start string, alive bool)
	LockTimeout time.Duration
}

// Ledger is the admission ledger over a capacity directory (see Dir).
type Ledger struct {
	store *Store
	now   func() time.Time
	probe func(pid int) (string, bool)
}

// NewLedger returns a Ledger over dir.
func NewLedger(dir string, opts Options) *Ledger {
	l := &Ledger{store: NewStore(dir), now: opts.Now, probe: opts.Probe}
	l.store.lockTimeout = ledgerLockTimeout
	if opts.LockTimeout > 0 {
		l.store.lockTimeout = opts.LockTimeout
	}
	if l.now == nil {
		l.now = time.Now
	}
	if l.probe == nil {
		l.probe = defaultProbe
	}
	return l
}

// Path is the ledger file, for the caller's "capacity ledger unreadable: <path>:
// <reason>" line. The typed errors themselves are path-free.
func (l *Ledger) Path() string {
	if l.store.dir == "" {
		return ""
	}
	return filepath.Join(l.store.dir, ledgerFile)
}

// ---- persistence -----------------------------------------------------------

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// decodeStrict decodes one object that must carry exactly keys, none null, and
// no unknown field. json.Unmarshal of null is a silent no-op, so a null would
// read as the zero value and the next write would overwrite it. It returns the
// name of the check that failed, or "" when the object decoded: never a value
// from the file, so the reason is safe for a log.
func decodeStrict(raw json.RawMessage, keys []string, out any) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != len(keys) {
		return "keys missing or unknown"
	}
	for _, k := range keys {
		if v, ok := fields[k]; !ok || isNull(v) {
			return "keys missing or unknown"
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return "field type invalid"
	}
	return ""
}

// corrupt is ErrCorrupt with the position of the record and the name of the
// failing check ("entry 3: window invalid"), so an operator doing the manual
// recovery knows where to look. It carries no value from the file and no path.
func corrupt(kind string, index int, check string) error {
	return fmt.Errorf("%w: %s %d: %s", ErrCorrupt, kind, index+1, check)
}

// decodeLedger decodes strictly. A schema other than 1 is reported before the
// other keys are looked at, since a newer schema may have different ones.
func decodeLedger(data []byte) (ledgerState, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil || top == nil {
		return ledgerState{}, ErrCorrupt
	}
	if _, err := dec.Token(); err != io.EOF {
		return ledgerState{}, ErrCorrupt
	}
	rawSchema, ok := top["schema"]
	if !ok || isNull(rawSchema) {
		return ledgerState{}, ErrCorrupt
	}
	var schema int
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return ledgerState{}, ErrCorrupt
	}
	if schema != ledgerSchema {
		return ledgerState{}, ErrUnknownSchema
	}
	if len(top) != 3 {
		return ledgerState{}, ErrCorrupt
	}
	var rawEntries, rawWaits []json.RawMessage
	for key, dst := range map[string]*[]json.RawMessage{"entries": &rawEntries, "waits": &rawWaits} {
		raw, ok := top[key]
		if !ok || isNull(raw) || json.Unmarshal(raw, dst) != nil {
			return ledgerState{}, ErrCorrupt
		}
	}
	if len(rawEntries) > maxEntries || len(rawWaits) > maxWaits {
		return ledgerState{}, ErrTooLarge
	}
	st := ledgerState{Schema: ledgerSchema, Entries: make([]Entry, 0, len(rawEntries)), Waits: make([]Wait, 0, len(rawWaits))}
	holders := map[string]bool{}
	for i, raw := range rawEntries {
		var e Entry
		if check := decodeStrict(raw, entryKeys, &e); check != "" {
			return ledgerState{}, corrupt("entry", i, check)
		}
		if check := e.invalid(); check != "" {
			return ledgerState{}, corrupt("entry", i, check)
		}
		if holders[e.HolderID] {
			return ledgerState{}, corrupt("entry", i, "duplicate holder")
		}
		holders[e.HolderID] = true
		st.Entries = append(st.Entries, e)
	}
	for i, raw := range rawWaits {
		var w Wait
		if check := decodeStrict(raw, waitKeys, &w); check != "" {
			return ledgerState{}, corrupt("wait", i, check)
		}
		if check := w.invalid(); check != "" {
			return ledgerState{}, corrupt("wait", i, check)
		}
		st.Waits = append(st.Waits, w)
	}
	return st, nil
}

// cleanField reports whether s is bounded, valid UTF-8 and holds no control
// character (a newline included), so it cannot forge a log or status line. UTF-8
// validity matters beyond logs: json.Marshal replaces an invalid byte with U+FFFD,
// so a value that is not valid UTF-8 would not equal itself after the round trip
// through the file, and a worker's window could then never match its entry.
func cleanField(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// badIdentityField names the first of the strings a ledger entry or request
// carries that cleanField refuses, or "" when all are clean.
func badIdentityField(repo, branch, window, socket, dir string) string {
	for _, f := range []struct {
		name, value string
		limit       int
	}{
		{"repo", repo, maxFieldBytes}, {"branch", branch, maxFieldBytes}, {"window", window, maxFieldBytes},
		{"socket", socket, maxFieldBytes}, {"dir", dir, maxDirBytes},
	} {
		if !cleanField(f.value, f.limit) {
			return f.name
		}
	}
	return ""
}

// cleanIdentity checks the strings a ledger entry or request carries.
func cleanIdentity(repo, branch, window, socket, dir string) bool {
	return badIdentityField(repo, branch, window, socket, dir) == ""
}

// invalid names the first check the entry fails, or "" when it is valid. The names
// are the text of the ErrCorrupt a read reports.
func (e Entry) invalid() string {
	switch {
	case e.HolderID == "":
		return "holder id empty"
	case !e.Kind.valid():
		return "kind unknown"
	case e.Repo == "":
		return "repo empty"
	case e.Ticket <= 0:
		return "ticket out of range"
	case e.CreatedAt.IsZero():
		return "createdAt unset"
	case e.UpdatedAt.IsZero():
		return "updatedAt unset"
	case e.LauncherPID <= 0 || e.LauncherPID > math.MaxInt32:
		return "launcher pid out of range"
	}
	if field := badIdentityField(e.Repo, e.Branch, e.Window, e.Socket, e.Dir); field != "" {
		return field + " invalid"
	}
	switch e.State {
	case StateReserved:
		return ""
	case StateConfirmed:
		if e.Window == "" {
			return "confirmed entry has no window"
		}
		return ""
	}
	return "state unknown"
}

// invalid names the first check the wait fails, or "" when it is valid.
func (w Wait) invalid() string {
	switch {
	case w.Repo == "":
		return "repo empty"
	case w.Ticket <= 0:
		return "ticket out of range"
	case !w.Kind.valid():
		return "kind unknown"
	case w.Since.IsZero():
		return "since unset"
	case w.RefreshedAt.IsZero():
		return "refreshedAt unset"
	case w.ExpiresAt.IsZero():
		return "expiresAt unset"
	}
	return ""
}

// read reads and decodes the ledger without a lock and without creating
// anything. An absent store or file is the empty ledger.
func (l *Ledger) read() (ledgerState, error) {
	data, exists, err := l.store.readFileLimit(ledgerFile, maxLedgerBytes)
	if err != nil {
		return ledgerState{}, err
	}
	if !exists {
		return ledgerState{Schema: ledgerSchema, Entries: []Entry{}, Waits: []Wait{}}, nil
	}
	return decodeLedger(data)
}

// save writes the ledger, refusing one over the byte bound.
func (l *Ledger) save(st ledgerState) error {
	st.Schema = ledgerSchema
	if st.Entries == nil {
		st.Entries = []Entry{}
	}
	if st.Waits == nil {
		st.Waits = []Wait{}
	}
	if len(st.Entries) > maxEntries || len(st.Waits) > maxWaits {
		return ErrTooLarge
	}
	data, err := json.Marshal(st)
	if err != nil {
		return ErrIO
	}
	data = append(data, '\n')
	if len(data) > maxLedgerBytes {
		return ErrTooLarge
	}
	return l.store.WriteFile(ledgerFile, data)
}

// mutate runs fn on the ledger under the store lock and writes the result when
// fn reports a change. With create false it never creates the store (a read-only
// or cleanup path): an absent store is the empty ledger.
func (l *Ledger) mutate(create bool, fn func(st *ledgerState, now time.Time) (changed bool, err error)) error {
	run := func(fn func() error) error { return l.store.withReadLock(ledgerFile, fn) }
	if create {
		run = l.store.WithLock
	}
	return run(func() error {
		st, err := l.read()
		if err != nil {
			return err
		}
		changed, err := fn(&st, l.now().UTC())
		if err != nil || !changed {
			return err
		}
		return l.save(st)
	})
}

// Peek reads the ledger without the lock, never blocks and creates nothing; it
// is what `cenci dispatch status` and a dry run use. A write replaces the file
// atomically, so Peek sees the old or the new ledger.
func (l *Ledger) Peek() (Snapshot, error) {
	st, err := l.read()
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Entries: st.Entries, Waits: st.Waits}, nil
}

// ---- settling an entry -----------------------------------------------------

// IsTransient reports whether err is one a second try can fix: the store lock was
// busy (ErrLockUnavailable) or the store was briefly unreadable or unwritable
// (ErrIO). Every other error is permanent for the call that got it: a bad
// request, a corrupt, over-size or unknown-schema ledger, an entry that is no
// longer held, a path that is not trusted.
func IsTransient(err error) bool {
	return errors.Is(err, ErrLockUnavailable) || errors.Is(err, ErrIO)
}

// retrySleep waits out RetryTransient's pause; it is a variable so a test can
// observe it instead of sleeping.
var retrySleep = time.Sleep

// RetryTransient runs one Release or Confirm and, when it failed with a transient
// error, once more after pause (none when pause is 0). It is the one bounded retry
// dispatch and babysit share. It is called outside the ledger lock: each attempt
// takes the lock for itself.
func RetryTransient(pause time.Duration, op func() error) error {
	err := op()
	if !IsTransient(err) {
		return err
	}
	if pause > 0 {
		retrySleep(pause)
	}
	return op()
}

// WorkerState is what a tmux inventory says about an entry's worker window.
type WorkerState int

const (
	// WorkerUnknown: the inventory cannot say (the socket was not read, the listing
	// was incomplete, or it was read before the entry last changed and shows no
	// window).
	WorkerUnknown WorkerState = iota
	// WorkerAlive: a window that may be the entry's worker, with a live pane, is in
	// the listing.
	WorkerAlive
	// WorkerAbsent: a complete listing read after the entry last changed holds no
	// live window that may be the entry's worker.
	WorkerAbsent
)

// WorkerIn judges the entry's worker window against inv with the release rules'
// own matching (windowMatches) and conservatism, but without looking at the
// launcher: it is how a launcher settles its own reserved entry (a babysit
// supervisor after a failed Release or Confirm).
func (e Entry) WorkerIn(inv Inventory) WorkerState {
	si, ok := inv[e.Socket]
	if !ok || !si.Complete || si.ReadAt.IsZero() {
		return WorkerUnknown
	}
	for _, w := range si.Windows {
		if windowMatches(e, w) && !w.AllDead {
			return WorkerAlive
		}
	}
	if si.ReadAt.Before(e.UpdatedAt) {
		return WorkerUnknown
	}
	return WorkerAbsent
}

// ---- launcher probe --------------------------------------------------------

// defaultProbe reads a process's start time and liveness. On Linux the start
// time is field 22 of /proc/<pid>/stat (clock ticks since boot, so a reused pid
// has a different one) and a zombie is dead; elsewhere only liveness is known.
func defaultProbe(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		if err == nil {
			if start, state, perr := parseProcStat(data); perr == nil {
				return start, state != 'Z' && state != 'X'
			}
		}
	}
	err := syscall.Kill(pid, 0)
	return "", err == nil || errors.Is(err, syscall.EPERM)
}

var errProcStat = errors.New("malformed /proc stat")

// parseProcStat returns the start time (field 22) and the state (field 3) of a
// /proc/<pid>/stat line. The command name is field 2 and may hold spaces and
// parentheses, so the fields are counted from the last ')'.
func parseProcStat(data []byte) (start string, state byte, err error) {
	s := string(data)
	end := strings.LastIndexByte(s, ')')
	if end < 0 || !strings.Contains(s[:end], "(") {
		return "", 0, errProcStat
	}
	fields := strings.Fields(s[end+1:])
	// fields[0] is field 3, so field 22 is fields[19].
	if len(fields) < 20 || fields[0] == "" {
		return "", 0, errProcStat
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", 0, errProcStat
	}
	return fields[19], fields[0][0], nil
}

// ownedBy is the one definition of "e was launched by process pid, whose start
// time is start": the pid matches and the start times do not disagree (either one
// unknown does not count as a disagreement, as in launcherDead).
func (e Entry) ownedBy(pid int, start string) bool {
	return e.LauncherPID == pid && (e.LauncherStart == "" || start == "" || e.LauncherStart == start)
}

// Owns reports whether e was launched by the process pid, probing its start time
// the way Admit does, so a pid reused by an unrelated process is not the launcher.
func (l *Ledger) Owns(e Entry, pid int) bool {
	start, _ := l.probe(pid)
	return e.ownedBy(pid, start)
}

// launcherDead reports whether e's launcher is gone: no such process, or the
// pid now has a different start time (it was reused by an unrelated process).
func (l *Ledger) launcherDead(e Entry) bool {
	if e.LauncherPID <= 0 {
		return true
	}
	start, alive := l.probe(e.LauncherPID)
	if !alive {
		return true
	}
	return e.LauncherStart != "" && start != "" && start != e.LauncherStart
}

// ---- release rules ---------------------------------------------------------

// provenGone applies the release rules to e against the caller's inventory. It
// is conservative everywhere: a socket that was not read, an incomplete
// inventory, a listing read before the entry last changed, or any window that
// may be the worker's keeps the entry, and a reserved entry additionally needs a
// dead launcher (a missing window while the launcher lives proves nothing).
func (l *Ledger) provenGone(e Entry, inv Inventory) bool {
	si, ok := inv[e.Socket]
	if !ok || !si.Complete || si.ReadAt.IsZero() || si.ReadAt.Before(e.UpdatedAt) {
		return false
	}
	for _, w := range si.Windows {
		if windowMatches(e, w) && !w.AllDead {
			return false
		}
	}
	// Accepted residual: ReadAt is the time just before the listing began, so a
	// window created after it is invisible to it; only the launcher check (a dead
	// launcher) keeps a reserved entry safe from such a stale inventory.
	return e.State != StateReserved || l.launcherDead(e)
}

// releaseGone removes every entry the inventory proves gone and returns them.
func (l *Ledger) releaseGone(st *ledgerState, inv Inventory) []Entry {
	kept := make([]Entry, 0, len(st.Entries))
	var released []Entry
	for _, e := range st.Entries {
		if l.provenGone(e, inv) {
			released = append(released, e)
			continue
		}
		kept = append(kept, e)
	}
	st.Entries = kept
	return released
}

// pruneWaits removes expired wait records and reports whether any went.
func (st *ledgerState) pruneWaits(now time.Time) bool {
	before := len(st.Waits)
	st.Waits = slices.DeleteFunc(st.Waits, func(w Wait) bool { return !w.ExpiresAt.After(now) })
	return len(st.Waits) != before
}

func (st *ledgerState) dropWait(repo string, ticket int) bool {
	before := len(st.Waits)
	st.Waits = slices.DeleteFunc(st.Waits, func(w Wait) bool { return w.Ticket == ticket && strings.EqualFold(w.Repo, repo) })
	return len(st.Waits) != before
}

func (st *ledgerState) indexOf(holder string) int {
	return slices.IndexFunc(st.Entries, func(e Entry) bool { return e.HolderID == holder })
}

// ---- operations ------------------------------------------------------------

func permitOf(e Entry, adopted bool) Permit {
	return Permit{HolderID: e.HolderID, Kind: e.Kind, Repo: e.Repo, Ticket: e.Ticket, Window: e.Window, State: e.State, Adopted: adopted}
}

func refuse(reason string) Decision { return Decision{Reason: reason} }

func newHolderID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrIO
	}
	return hex.EncodeToString(b[:]), nil
}

// Admit reserves a slot for req or refuses it, under one lock and in this
// order: (a) the release rules, applied to req.Obs.Inventory, free what a
// complete inventory proves gone; (b) ticket exclusivity (`worker already
// reserved`, or a babysit supervisor adopting its own dead launcher's entry);
// (c) the branch-writer check (`branch writer active`: the same repo and branch
// held by the other family, dispatch against babysit); (d) the repo cap; (e) the
// machine cap. A dispatch launch needs more free slots than live wait records,
// a repair needs one. An error means nothing was admitted: the ledger is
// unreadable (never reset), the lock timed out, or the request is invalid.
func (l *Ledger) Admit(req Request) (Decision, error) {
	if err := req.validate(); err != nil {
		return Decision{}, err
	}
	launcher := req.Launcher
	if launcher == 0 {
		launcher = os.Getpid()
	}
	start, _ := l.probe(launcher)
	var dec Decision
	err := l.mutate(true, func(st *ledgerState, now time.Time) (bool, error) {
		changed := st.pruneWaits(now)
		if len(l.releaseGone(st, req.Obs.Inventory)) > 0 {
			changed = true
		}
		d, mutated, err := l.evaluate(st, req, launcher, start, now)
		dec = d
		return changed || mutated, err
	})
	if err != nil {
		return Decision{}, err
	}
	return dec, nil
}

// evaluate is the pure part of Admit over the already-released ledger. It
// reports whether it changed st.
func (l *Ledger) evaluate(st *ledgerState, req Request, launcher int, start string, now time.Time) (Decision, bool, error) {
	for i := range st.Entries {
		e := &st.Entries[i]
		if e.Ticket != req.Ticket || !strings.EqualFold(e.Repo, req.Repo) {
			continue
		}
		own := e.ownedBy(launcher, start)
		if req.Adopt && e.Kind == req.Kind && (own || l.launcherDead(*e)) {
			// The permit reports how far the original launch got; the entry itself
			// becomes a launch in flight again. Reserved with this launcher alive, it
			// cannot be released by anyone's inventory (not even one that has not yet
			// seen the new window), and UpdatedAt = now voids every inventory read
			// before the adoption. The adopter settles it: Confirm, or Release when
			// the launch fails before `cenci run`. Babysit refuses to adopt while the
			// PR's worker is live, so a live worker never meets this reset.
			permit := permitOf(*e, true)
			e.LauncherPID, e.LauncherStart = launcher, start
			e.State, e.UpdatedAt = StateReserved, now
			st.dropWait(req.Repo, req.Ticket)
			return Decision{Admitted: true, Permit: permit}, true, nil
		}
		return refuse(ReasonWorkerReserved), false, nil
	}

	if req.Branch != "" {
		for _, e := range st.Entries {
			if strings.EqualFold(e.Repo, req.Repo) && e.Branch == req.Branch && e.Kind.Repair() != req.Kind.Repair() {
				return refuse(ReasonBranchWriter), false, nil
			}
		}
	}

	usage := CountUsage(st.Entries, req.Obs)
	if c, ok := req.Limits.RepoCap(req.Repo); ok && c <= req.Limits.Machine && usage.Repo(req.Repo) >= c {
		return refuse(ReasonRepoCap), false, nil
	}
	need := 1
	if !req.Kind.Repair() {
		need += Snapshot{Waits: st.Waits}.LiveWaits(now)
	}
	if req.Limits.Machine-usage.Machine < need {
		return refuse(ReasonMachineCap), false, nil
	}

	if len(st.Entries) >= maxEntries {
		return Decision{}, false, ErrTooLarge
	}
	holder, err := newHolderID()
	if err != nil {
		return Decision{}, false, err
	}
	e := Entry{
		HolderID: holder, Kind: req.Kind, Repo: req.Repo, Ticket: req.Ticket, Branch: req.Branch,
		LauncherPID: launcher, LauncherStart: start,
		Socket: req.Socket, Window: req.Window, LaunchID: req.LaunchID,
		State: StateReserved, CreatedAt: now, UpdatedAt: now,
	}
	if req.Dir != "" {
		e.Dir = filepath.Clean(req.Dir)
	}
	st.Entries = append(st.Entries, e)
	st.dropWait(req.Repo, req.Ticket)
	return Decision{Admitted: true, Permit: permitOf(e, false)}, true, nil
}

// Confirm hands the permit's reservation over to the worker's window: it sets
// the entry's socket, window and directory and moves it to confirmed. It
// creates nothing; a permit that is no longer held is ErrNotHeld.
func (l *Ledger) Confirm(p Permit, c Confirmation) error {
	if c.Window == "" {
		return fmt.Errorf("%w: no window", ErrInvalidRequest)
	}
	if !cleanIdentity("", "", c.Window, c.Socket, c.Dir) || !cleanField(c.LaunchID, maxFieldBytes) {
		return fmt.Errorf("%w: %s", ErrInvalidRequest, badFieldReason)
	}
	return l.mutate(false, func(st *ledgerState, now time.Time) (bool, error) {
		i := st.indexOf(p.HolderID)
		if i < 0 {
			return false, ErrNotHeld
		}
		e := &st.Entries[i]
		e.Socket, e.Window, e.State, e.UpdatedAt = c.Socket, c.Window, StateConfirmed, now
		if c.Dir != "" {
			e.Dir = filepath.Clean(c.Dir)
		}
		if c.LaunchID != "" {
			e.LaunchID = c.LaunchID
		}
		return true, nil
	})
}

// Release frees the permit's slot, for a launch that definitely failed before
// any window existed. It creates nothing, and releasing a slot that is gone is
// not an error.
func (l *Ledger) Release(p Permit) error {
	return l.mutate(false, func(st *ledgerState, _ time.Time) (bool, error) {
		i := st.indexOf(p.HolderID)
		if i < 0 {
			return false, nil
		}
		st.Entries = slices.Delete(st.Entries, i, i+1)
		return true, nil
	})
}

// Reconcile applies the release rules to inv and returns the entries it freed.
// It creates nothing, and an inventory that is incomplete, unread or older than
// an entry frees nothing for that entry.
func (l *Ledger) Reconcile(inv Inventory) ([]Entry, error) {
	var released []Entry
	err := l.mutate(false, func(st *ledgerState, now time.Time) (bool, error) {
		pruned := st.pruneWaits(now)
		released = l.releaseGone(st, inv)
		return pruned || len(released) > 0, nil
	})
	if err != nil {
		return nil, err
	}
	return released, nil
}

// Wait records or refreshes a repair's wait for capacity. The record keeps its
// first Since, holds no slot, and expires 3 Intervals from now unless refreshed.
func (l *Ledger) Wait(w WaitRequest) error {
	if !w.Kind.valid() || w.Repo == "" || w.Ticket <= 0 || w.Interval <= 0 {
		return fmt.Errorf("%w: incomplete wait", ErrInvalidRequest)
	}
	return l.mutate(true, func(st *ledgerState, now time.Time) (bool, error) {
		st.pruneWaits(now)
		rec := Wait{Repo: w.Repo, Ticket: w.Ticket, Kind: w.Kind, Reason: w.Reason, Since: now, RefreshedAt: now, ExpiresAt: now.Add(WaitIntervals * w.Interval)}
		if i := slices.IndexFunc(st.Waits, func(o Wait) bool { return o.Ticket == w.Ticket && strings.EqualFold(o.Repo, w.Repo) }); i >= 0 {
			rec.Since = st.Waits[i].Since
			st.Waits[i] = rec
			return true, nil
		}
		if len(st.Waits) >= maxWaits {
			return false, ErrTooLarge
		}
		st.Waits = append(st.Waits, rec)
		return true, nil
	})
}

// DropWait removes a repair's wait record (it merged, closed, stopped or no
// longer needs a repair). It creates nothing.
func (l *Ledger) DropWait(repo string, ticket int) error {
	return l.mutate(false, func(st *ledgerState, _ time.Time) (bool, error) {
		return st.dropWait(repo, ticket), nil
	})
}
