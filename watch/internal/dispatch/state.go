package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// stateStore persists ReconcileState as JSON on disk.
//
// writeTemp/syncTemp/renameTemp are unexported test seams (mirroring
// GHMutator.createLabel, reconcile_run.go): a nil field falls back to the
// real *os.File.Write/Sync and os.Rename implementation. Same-package tests
// construct &stateStore{path: p, renameTemp: ...} directly to inject a
// failure at each crash-safety boundary (write, fsync, rename) without an
// exported constructor.
type stateStore struct {
	path string

	writeTemp  func(f *os.File, data []byte) (int, error)
	syncTemp   func(f *os.File) error
	renameTemp func(oldpath, newpath string) error
}

// reconcileState is the on-disk DTO for ReconcileState. SchemaVersion (#883)
// distinguishes the current persisted shape from the legacy (pre-#883)
// format, which has no "schemaVersion" key at all and unmarshals it to the
// int zero value -- migrateReconcileState treats that the same as an
// explicit 0 (both are "legacy v0"), so existing on-disk files load cleanly.
//
// The three OOM sections (#1291) are additive optional keys under the same
// schema version. They stay raw here so a wrong-typed section never fails the
// whole decode (which would be a StateProbeDecodeError hold): decodeOOMSections
// decodes and validates them separately, and omitempty keeps a pass with no OOM
// data writing exactly the pre-#1291 bytes.
type reconcileState struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Observations  map[string]time.Time `json:"observations"`
	ApplyFailures map[string]int       `json:"applyFailures"`
	OOMLastSeen   json.RawMessage      `json:"oomLastSeen,omitempty"`
	OOMBaselines  json.RawMessage      `json:"oomBaselines,omitempty"`
	OOMRetryAt    json.RawMessage      `json:"oomRetryAt,omitempty"`
}

// toPublic converts the on-disk DTO to the in-memory ReconcileState the rest
// of the package uses. A named conversion function (rather than a bare type
// conversion) is required because reconcileState carries the extra
// SchemaVersion field and the raw OOM sections, so the two struct types no
// longer share an identical field set.
func (s reconcileState) toPublic() ReconcileState {
	st := ReconcileState{Observations: s.Observations, ApplyFailures: s.ApplyFailures}
	st.OOMLastSeen, st.OOMBaselines, st.OOMRetryAt, st.OOMDropped = decodeOOMSections(s)
	return st
}

// decodeOOMSections decodes and validates the OOM sections (#1291). Any section
// of the wrong JSON type or holding an invalid entry (an empty key, a negative
// counter, a zero timestamp) drops all three -- they describe one shared
// baseline model, so keeping some would let them disagree -- and returns why in
// dropped. Never an error to Load: losing this state only means the next
// recovery takes the ordinary budgeted path, unlike the grace and apply-retry
// state, whose loss is unsafe (#883).
func decodeOOMSections(s reconcileState) (lastSeen map[string]int64, baselines map[string]map[string]int64, retryAt map[string]time.Time, dropped error) {
	lastSeen, baselines, retryAt = map[string]int64{}, map[string]map[string]int64{}, map[string]time.Time{}
	err := func() error {
		if err := decodeOOMSection(s.OOMLastSeen, &lastSeen); err != nil {
			return fmt.Errorf("oomLastSeen: %w", err)
		}
		if err := decodeOOMSection(s.OOMBaselines, &baselines); err != nil {
			return fmt.Errorf("oomBaselines: %w", err)
		}
		if err := decodeOOMSection(s.OOMRetryAt, &retryAt); err != nil {
			return fmt.Errorf("oomRetryAt: %w", err)
		}
		return validateOOMSections(lastSeen, baselines, retryAt)
	}()
	if err != nil {
		return map[string]int64{}, map[string]map[string]int64{}, map[string]time.Time{}, err
	}
	return lastSeen, baselines, retryAt, nil
}

// decodeOOMSection unmarshals one raw section into dst; an absent section (or a
// JSON null) leaves dst as the empty map it was given.
func decodeOOMSection[M ~map[K]V, K comparable, V any](raw json.RawMessage, dst *M) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return err
	}
	if *dst == nil {
		*dst = M{}
	}
	return nil
}

// validateOOMSections checks the integrity invariants of the decoded OOM
// sections, mirroring validateReconcileState's rules for the safety state.
func validateOOMSections(lastSeen map[string]int64, baselines map[string]map[string]int64, retryAt map[string]time.Time) error {
	for k, v := range lastSeen {
		if k == "" {
			return errors.New("empty container key in oomLastSeen")
		}
		if v < 0 {
			return fmt.Errorf("negative oomLastSeen counter for %q: %d", k, v)
		}
	}
	for ticket, containers := range baselines {
		if ticket == "" {
			return errors.New("empty ticket key in oomBaselines")
		}
		for k, v := range containers {
			if k == "" {
				return fmt.Errorf("empty container key in oomBaselines for %q", ticket)
			}
			if v < 0 {
				return fmt.Errorf("negative oomBaselines counter for %q/%q: %d", ticket, k, v)
			}
		}
	}
	for k, v := range retryAt {
		if k == "" {
			return errors.New("empty ticket key in oomRetryAt")
		}
		if v.IsZero() {
			return fmt.Errorf("zero-value oomRetryAt timestamp for %q", k)
		}
	}
	return nil
}

// marshalOOMSection renders one OOM section for Save; an empty map stays nil so
// omitempty leaves its key out of the file.
func marshalOOMSection[M ~map[K]V, K comparable, V any](m M) (json.RawMessage, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

// oomBackoffLoadFn is the dispatch-side seam (#1291) that reads the OOM retry
// times the reconciler persisted, keyed by planKey(repo, number) like every
// other ticket-keyed map in this package. It is read-only: dispatch never
// writes reconcile.json. An unreadable file, or OOM sections Load had to drop,
// is an error for the caller to log; the map is then nil. Tests stub it in
// TestMain so no test reads a developer's real state file.
var oomBackoffLoadFn = defaultOOMBackoffLoad

// defaultOOMBackoffLoad reads the OOM retry times from the default state path.
func defaultOOMBackoffLoad() (map[string]time.Time, error) {
	st, err := NewStateStore("").Load()
	if err != nil {
		return nil, err
	}
	if st.OOMDropped != nil {
		return nil, fmt.Errorf("reconcile state OOM sections dropped: %w", st.OOMDropped)
	}
	return st.OOMRetryAt, nil
}

// currentReconcileSchema is the schema version this build writes. Bumped
// whenever the on-disk shape changes in a way that needs an explicit
// migration step in migrateReconcileState.
const currentReconcileSchema = 1

// DefaultStatePath resolves $XDG_STATE_HOME/cenci/reconcile.json, falling
// back to ~/.local/state when XDG_STATE_HOME is unset.
func DefaultStatePath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "cenci", "reconcile.json")
}

// NewStateStore returns a disk-backed ReconcileStore. An empty path resolves
// the XDG default.
func NewStateStore(path string) ReconcileStore {
	if path == "" {
		path = DefaultStatePath()
	}
	return &stateStore{path: path}
}

// emptyReconcileState is the zero-value ReconcileState with every map
// initialized, so Load never hands a caller a nil map on any error path.
func emptyReconcileState() ReconcileState {
	return ReconcileState{
		Observations:  map[string]time.Time{},
		ApplyFailures: map[string]int{},
		OOMLastSeen:   map[string]int64{},
		OOMBaselines:  map[string]map[string]int64{},
		OOMRetryAt:    map[string]time.Time{},
	}
}

// StateProbe classifies a stateStore.Load attempt into a closed set (mirrors
// PlanProbe, types.go): StateProbeAbsent is the zero value ("") because "no
// file yet" is valid empty initial state, not corruption -- every
// pre-#883 construction site keeps today's behavior unchanged without being
// touched. Every other non-ok value is a broken-input class that must
// default-deny: applyReconcile aborts the mutating pass rather than
// synthesizing empty state and later overwriting the evidence.
type StateProbe string

const (
	// StateProbeAbsent is the zero value: no state file exists yet -- the
	// valid empty-initial-state case, not corruption.
	StateProbeAbsent StateProbe = ""
	// StateProbeOk means the file was read, decoded, migrated, and validated
	// cleanly.
	StateProbeOk StateProbe = "ok"
	// StateProbeReadError means the file exists but os.ReadFile failed (e.g.
	// a permission error). Broken input, not absent input: must default-deny.
	StateProbeReadError StateProbe = "read_error"
	// StateProbeDecodeError means the file was read but is not valid JSON
	// (e.g. truncated by a crash mid-write, the exact failure mode this
	// ticket makes crash-safe going forward). Must default-deny.
	StateProbeDecodeError StateProbe = "decode_error"
	// StateProbeSchemaError means the file decoded but carries an
	// unknown/unsupported schemaVersion (including a negative one). Must
	// default-deny.
	StateProbeSchemaError StateProbe = "schema_error"
	// StateProbeInvalid means the file decoded and migrated cleanly but
	// failed integrity validation (an empty ticket key, a zero-value
	// observation timestamp, or a negative apply-failures counter). Must
	// default-deny.
	StateProbeInvalid StateProbe = "invalid"
)

// ErrReconcileStateUnreadable is the sentinel every non-absence Load failure
// wraps (via StateLoadError.Unwrap), so callers can classify a corruption
// hold with errors.Is without depending on the specific StateProbe value.
var ErrReconcileStateUnreadable = errors.New("reconcile state unreadable")

// StateLoadError is returned by stateStore.Load for every non-absence
// failure. Probe carries the specific broken-input class (for logging and
// tests); Err is the underlying cause (fs.ErrPermission, *json.SyntaxError,
// ...), kept reachable via errors.Is/errors.As through Unwrap() []error
// (Go 1.25 multi-error unwrap) alongside ErrReconcileStateUnreadable.
type StateLoadError struct {
	Probe StateProbe
	Path  string
	Err   error
}

func (e *StateLoadError) Error() string {
	return fmt.Sprintf("reconcile state %s at %s: %v", e.Probe, e.Path, e.Err)
}

func (e *StateLoadError) Unwrap() []error {
	return []error{ErrReconcileStateUnreadable, e.Err}
}

// migrateReconcileState normalizes st in place to the current schema:
// schemaVersion absent (unmarshals to 0) or explicitly 0 is the legacy
// pre-#883 format and is migrated forward (nil maps back-filled, preserving
// the pre-existing ApplyFailures==nil back-compat behavior);
// currentReconcileSchema is accepted as-is; any other value (including
// negative) is an unsupported schema and returns an error so the caller
// classifies it as StateProbeSchemaError.
func migrateReconcileState(st *reconcileState) error {
	switch st.SchemaVersion {
	case 0, currentReconcileSchema:
		// legacy v0 (absent/explicit 0) or current: fall through to back-fill.
	default:
		return fmt.Errorf("unsupported reconcile state schemaVersion %d", st.SchemaVersion)
	}
	if st.Observations == nil {
		st.Observations = map[string]time.Time{}
	}
	if st.ApplyFailures == nil {
		st.ApplyFailures = map[string]int{}
	}
	st.SchemaVersion = currentReconcileSchema
	return nil
}

// validateReconcileState checks integrity invariants that migration alone
// can't establish: every observation/apply-failure key must be a non-empty
// ticket key, every observation timestamp must be non-zero (a zero-value
// time.Time can never be a real first-seen-failing moment), and every
// apply-failures counter must be non-negative. Any violation returns an
// error so the caller classifies it as StateProbeInvalid.
func validateReconcileState(st reconcileState) error {
	for k, v := range st.Observations {
		if k == "" {
			return errors.New("empty ticket key in observations")
		}
		if v.IsZero() {
			return fmt.Errorf("zero-value observation timestamp for %q", k)
		}
	}
	for k, v := range st.ApplyFailures {
		if k == "" {
			return errors.New("empty ticket key in applyFailures")
		}
		if v < 0 {
			return fmt.Errorf("negative applyFailures counter for %q: %d", k, v)
		}
	}
	return nil
}

func (s *stateStore) Load() (ReconcileState, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return emptyReconcileState(), nil
		}
		return emptyReconcileState(), &StateLoadError{Probe: StateProbeReadError, Path: s.path, Err: err}
	}
	var st reconcileState
	if err := json.Unmarshal(data, &st); err != nil {
		return emptyReconcileState(), &StateLoadError{Probe: StateProbeDecodeError, Path: s.path, Err: err}
	}
	if err := migrateReconcileState(&st); err != nil {
		return emptyReconcileState(), &StateLoadError{Probe: StateProbeSchemaError, Path: s.path, Err: err}
	}
	if err := validateReconcileState(st); err != nil {
		return emptyReconcileState(), &StateLoadError{Probe: StateProbeInvalid, Path: s.path, Err: err}
	}
	return st.toPublic(), nil
}

// staleTempAge gates sweepStaleTemps: only a temp file older than this is
// considered abandoned (a crashed prior Save), never one that could belong
// to a concurrently in-flight writer (the daemon's loop and a --reconcile
// cron entry can both call Save around the same time).
const staleTempAge = 1 * time.Hour

// sweepStaleTemps removes leftover base+".*.tmp" files in dir whose mtime is
// older than staleTempAge. It is best-effort and never returns an error or
// gates the caller's decision -- the one justified exception to
// watch/docs/error-handling.md's never-swallow rule, because a sweep failure
// must never block a Save that would otherwise succeed, and treating a
// symlink or non-regular leftover as removable would defeat the same
// symlink hardening os.CreateTemp's randomized name provides for the temp
// file Save itself writes.
func sweepStaleTemps(dir, base string) {
	matches, err := filepath.Glob(filepath.Join(dir, base+".*.tmp"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleTempAge)
	for _, m := range matches {
		info, err := os.Lstat(m)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(m)
		}
	}
}

// Save writes state to s.path crash-safely: a randomized same-directory temp
// file (os.CreateTemp defeats a pre-planted symlink at a predictable name,
// mirrors enroll.go's writeRawConfig) is written, fsynced, closed, then
// atomically renamed over the final path; the parent directory is then
// fsynced too so the rename itself is durable, not just the temp file's
// bytes. Every failure branch after CreateTemp removes the temp file so a
// failed Save never leaves a stray *.tmp behind, and the final path -- since
// os.Rename is atomic on the same filesystem -- is always either the
// previous complete state or the new complete state, never truncated.
//
// A directory-fsync failure is surfaced as an error even though the rename
// has already committed at that point (state is durable at the OS level
// either way): deliberate and conservative, visible over silent.
func (s *stateStore) Save(state ReconcileState) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state dir %s: %w", dir, err)
	}

	dto := reconcileState{
		SchemaVersion: currentReconcileSchema,
		Observations:  state.Observations,
		ApplyFailures: state.ApplyFailures,
	}
	var err error
	if dto.OOMLastSeen, err = marshalOOMSection(state.OOMLastSeen); err != nil {
		return fmt.Errorf("marshaling reconcile state oomLastSeen: %w", err)
	}
	if dto.OOMBaselines, err = marshalOOMSection(state.OOMBaselines); err != nil {
		return fmt.Errorf("marshaling reconcile state oomBaselines: %w", err)
	}
	if dto.OOMRetryAt, err = marshalOOMSection(state.OOMRetryAt); err != nil {
		return fmt.Errorf("marshaling reconcile state oomRetryAt: %w", err)
	}
	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling reconcile state: %w", err)
	}

	base := filepath.Base(s.path)
	sweepStaleTemps(dir, base)

	tmp, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp state file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	writeTemp := s.writeTemp
	if writeTemp == nil {
		writeTemp = func(f *os.File, data []byte) (int, error) { return f.Write(data) }
	}
	if _, err := writeTemp(tmp, data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing temp state file %s: %w", tmpPath, err)
	}

	syncTemp := s.syncTemp
	if syncTemp == nil {
		syncTemp = func(f *os.File) error { return f.Sync() }
	}
	if err := syncTemp(tmp); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("syncing temp state file %s: %w", tmpPath, err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp state file %s: %w", tmpPath, err)
	}

	renameTemp := s.renameTemp
	if renameTemp == nil {
		renameTemp = os.Rename
	}
	if err := renameTemp(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp state file %s to %s: %w", tmpPath, s.path, err)
	}

	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening state dir %s to fsync the rename: %w", dir, err)
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	if syncErr != nil {
		return fmt.Errorf("syncing state dir %s: %w", dir, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing state dir %s: %w", dir, closeErr)
	}
	return nil
}
