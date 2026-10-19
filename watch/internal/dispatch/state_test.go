package dispatch

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// probeOf extracts the StateProbe carried by a *StateLoadError, failing the
// test if err does not carry one -- every non-nil Load error in this package
// must classify into the closed StateProbe set, and per
// watch/docs/error-handling.md (#412) each failure class must be asserted by
// its own probe value, never merely "some error" (a regression collapsing
// classes would otherwise pass).
func probeOf(t *testing.T, err error) StateProbe {
	t.Helper()
	var loadErr *StateLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("err = %v (%T), want a *StateLoadError carrying a StateProbe", err, err)
	}
	return loadErr.Probe
}

// -- Load classification: absence vs. the five broken-input classes --

func TestStateStoreLoadMissingFileReturnsEmptyState(t *testing.T) {
	dir := t.TempDir()
	store := &stateStore{path: filepath.Join(dir, "reconcile.json")}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load on a missing file returned an unexpected error (absence is not corruption): %v", err)
	}
	if state.Observations == nil || state.ApplyFailures == nil {
		t.Fatalf("Load on a missing file must return initialized (non-nil) maps, got %+v", state)
	}
	if len(state.Observations) != 0 || len(state.ApplyFailures) != 0 {
		t.Errorf("Load on a missing file must return empty maps, got %+v", state)
	}
}

func TestStateStoreLoadUnreadableFileReturnsReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 0000 does not block reads")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o000); err != nil {
		t.Fatalf("writing unreadable fixture: %v", err)
	}
	store := &stateStore{path: path}

	_, err := store.Load()
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("Load err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if probe := probeOf(t, err); probe != StateProbeReadError {
		t.Errorf("probe = %q, want %q", probe, StateProbeReadError)
	}
}

func TestStateStoreLoadTruncatedJSONReturnsDecodeError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	if err := os.WriteFile(path, []byte(`{"observations":{`), 0o600); err != nil {
		t.Fatalf("writing truncated fixture: %v", err)
	}
	store := &stateStore{path: path}

	_, err := store.Load()
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("Load err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if probe := probeOf(t, err); probe != StateProbeDecodeError {
		t.Errorf("probe = %q, want %q", probe, StateProbeDecodeError)
	}
}

func TestStateStoreLoadUnknownSchemaVersionReturnsSchemaError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	body := `{"schemaVersion":99,"observations":{"o/r#42":"2026-07-10T12:00:00Z"},"applyFailures":{}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	store := &stateStore{path: path}

	_, err := store.Load()
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("Load err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if probe := probeOf(t, err); probe != StateProbeSchemaError {
		t.Errorf("probe = %q, want %q", probe, StateProbeSchemaError)
	}
}

func TestStateStoreLoadNegativeSchemaVersionReturnsSchemaError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	body := `{"schemaVersion":-1,"observations":{},"applyFailures":{}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	store := &stateStore{path: path}

	_, err := store.Load()
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("Load err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if probe := probeOf(t, err); probe != StateProbeSchemaError {
		t.Errorf("probe = %q, want %q", probe, StateProbeSchemaError)
	}
}

func TestStateStoreLoadNegativeApplyFailuresCounterReturnsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	body := `{"schemaVersion":1,"observations":{"o/r#42":"2026-07-10T12:00:00Z"},"applyFailures":{"o/r#42":-1}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	store := &stateStore{path: path}

	_, err := store.Load()
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("Load err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if probe := probeOf(t, err); probe != StateProbeInvalid {
		t.Errorf("probe = %q, want %q", probe, StateProbeInvalid)
	}
}

func TestStateStoreLoadZeroValueObservationTimestampReturnsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	body := `{"schemaVersion":1,"observations":{"o/r#42":"0001-01-01T00:00:00Z"},"applyFailures":{}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	store := &stateStore{path: path}

	_, err := store.Load()
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("Load err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if probe := probeOf(t, err); probe != StateProbeInvalid {
		t.Errorf("probe = %q, want %q", probe, StateProbeInvalid)
	}
}

func TestStateStoreLoadEmptyTicketKeyReturnsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	body := `{"schemaVersion":1,"observations":{"":"2026-07-10T12:00:00Z"},"applyFailures":{}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	store := &stateStore{path: path}

	_, err := store.Load()
	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Fatalf("Load err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", err)
	}
	if probe := probeOf(t, err); probe != StateProbeInvalid {
		t.Errorf("probe = %q, want %q", probe, StateProbeInvalid)
	}
}

// TestStateLoadErrorSentinelAndCauseAreReachable is the direct
// package-boundary sentinel test mandated by watch/docs/error-handling.md
// (#412): errors.Is against the sentinel must be true, and the wrapped cause
// must remain reachable via errors.Is/errors.As (Go 1.25 multi-error Unwrap).
func TestStateLoadErrorSentinelAndCauseAreReachable(t *testing.T) {
	cause := errors.New("underlying cause")
	err := &StateLoadError{Probe: StateProbeDecodeError, Path: "reconcile.json", Err: cause}

	if !errors.Is(err, ErrReconcileStateUnreadable) {
		t.Error("StateLoadError must satisfy errors.Is(err, ErrReconcileStateUnreadable)")
	}
	if !errors.Is(err, cause) {
		t.Error("StateLoadError must keep the wrapped cause reachable via errors.Is")
	}
}

// TestStateStoreLoadsOldFormatWithoutApplyFailures is the direct store-level
// regression test for the ReconcileState schema back-compat requirement: an
// old reconcile.json written before ApplyFailures (and before #883's
// schemaVersion) existed must still load cleanly, with ApplyFailures
// resolving to an empty (not nil) map. Relocated verbatim from
// reconcile_run_test.go (#883).
func TestStateStoreLoadsOldFormatWithoutApplyFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	old := `{"observations":{"o/r#42":"2026-07-10T12:00:00Z"}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("writing old-format fixture: %v", err)
	}

	store := &stateStore{path: path}
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned an unexpected error on an old-format (schemaVersion-less) file: %v", err)
	}
	if state.ApplyFailures == nil {
		t.Error("ApplyFailures must load as an empty map (nil→empty) for back-compat with pre-#265 state files, got nil")
	}
	if _, ok := state.Observations["o/r#42"]; !ok {
		t.Error("existing observations must still load from an old-format file")
	}
}

// -- Save: round-trip fidelity, crash-safety fault injection, perms, symlinks --

// TestStateStoreSaveThenLoadRoundTripPreservesObservationsAndApplyFailures is
// the direct regression test for AC #4: observation timestamps and
// apply-failure counters must survive a save/reload cycle byte-identically,
// and the on-disk JSON must now carry the current schema version.
func TestStateStoreSaveThenLoadRoundTripPreservesObservationsAndApplyFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	store := &stateStore{path: path}

	want := ReconcileState{
		Observations:  map[string]time.Time{"o/r#42": time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)},
		ApplyFailures: map[string]int{"o/r#42": 2},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned an unexpected error: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned an unexpected error: %v", err)
	}
	if !got.Observations["o/r#42"].Equal(want.Observations["o/r#42"]) {
		t.Errorf("observation timestamp = %v, want byte-identical %v (AC #4)", got.Observations["o/r#42"], want.Observations["o/r#42"])
	}
	if got.ApplyFailures["o/r#42"] != want.ApplyFailures["o/r#42"] {
		t.Errorf("apply-failure counter = %d, want %d (AC #4)", got.ApplyFailures["o/r#42"], want.ApplyFailures["o/r#42"])
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	var onDisk struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parsing saved file: %v", err)
	}
	if onDisk.SchemaVersion != 1 {
		t.Errorf("on-disk schemaVersion = %d, want 1", onDisk.SchemaVersion)
	}
}

func TestStateStoreSaveInjectedWriteFailureLeavesPreviousStateIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")

	previous := ReconcileState{
		Observations:  map[string]time.Time{"o/r#1": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		ApplyFailures: map[string]int{"o/r#1": 1},
	}
	plain := &stateStore{path: path}
	if err := plain.Save(previous); err != nil {
		t.Fatalf("seeding previous state: %v", err)
	}

	failing := &stateStore{
		path: path,
		writeTemp: func(f *os.File, data []byte) (int, error) {
			return 0, errors.New("injected write failure")
		},
	}
	next := ReconcileState{
		Observations:  map[string]time.Time{"o/r#2": time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)},
		ApplyFailures: map[string]int{"o/r#2": 5},
	}
	if err := failing.Save(next); err == nil {
		t.Fatal("expected Save to surface the injected write failure")
	}

	got, err := plain.Load()
	if err != nil {
		t.Fatalf("reloading after the failed save: %v", err)
	}
	if len(got.Observations) != 1 || !got.Observations["o/r#1"].Equal(previous.Observations["o/r#1"]) {
		t.Errorf("state after a failed write must be the previous complete state unchanged, got %+v", got)
	}

	leftover, _ := filepath.Glob(filepath.Join(dir, "reconcile.json.*.tmp"))
	if len(leftover) != 0 {
		t.Errorf("expected no leftover temp files after a failed write, got %v", leftover)
	}
}

func TestStateStoreSaveInjectedSyncFailureLeavesPreviousStateIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")

	previous := ReconcileState{
		Observations:  map[string]time.Time{"o/r#1": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		ApplyFailures: map[string]int{"o/r#1": 1},
	}
	plain := &stateStore{path: path}
	if err := plain.Save(previous); err != nil {
		t.Fatalf("seeding previous state: %v", err)
	}

	failing := &stateStore{
		path: path,
		syncTemp: func(f *os.File) error {
			return errors.New("injected sync failure")
		},
	}
	next := ReconcileState{
		Observations:  map[string]time.Time{"o/r#2": time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)},
		ApplyFailures: map[string]int{"o/r#2": 5},
	}
	if err := failing.Save(next); err == nil {
		t.Fatal("expected Save to surface the injected sync failure")
	}

	got, err := plain.Load()
	if err != nil {
		t.Fatalf("reloading after the failed save: %v", err)
	}
	if len(got.Observations) != 1 || !got.Observations["o/r#1"].Equal(previous.Observations["o/r#1"]) {
		t.Errorf("state after a failed fsync must be the previous complete state unchanged, got %+v", got)
	}

	leftover, _ := filepath.Glob(filepath.Join(dir, "reconcile.json.*.tmp"))
	if len(leftover) != 0 {
		t.Errorf("expected no leftover temp files after a failed fsync, got %v", leftover)
	}
}

func TestStateStoreSaveInjectedRenameFailureLeavesPreviousStateIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")

	previous := ReconcileState{
		Observations:  map[string]time.Time{"o/r#1": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		ApplyFailures: map[string]int{"o/r#1": 1},
	}
	plain := &stateStore{path: path}
	if err := plain.Save(previous); err != nil {
		t.Fatalf("seeding previous state: %v", err)
	}

	failing := &stateStore{
		path: path,
		renameTemp: func(oldpath, newpath string) error {
			return errors.New("injected rename failure")
		},
	}
	next := ReconcileState{
		Observations:  map[string]time.Time{"o/r#2": time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)},
		ApplyFailures: map[string]int{"o/r#2": 5},
	}
	if err := failing.Save(next); err == nil {
		t.Fatal("expected Save to surface the injected rename failure")
	}

	got, err := plain.Load()
	if err != nil {
		t.Fatalf("reloading after the failed save: %v", err)
	}
	if len(got.Observations) != 1 || !got.Observations["o/r#1"].Equal(previous.Observations["o/r#1"]) {
		t.Errorf("state after a failed rename must be the previous complete state unchanged, got %+v", got)
	}

	leftover, _ := filepath.Glob(filepath.Join(dir, "reconcile.json.*.tmp"))
	if len(leftover) != 0 {
		t.Errorf("expected no leftover temp files after a failed rename, got %v", leftover)
	}
}

func TestStateStoreSaveFileModeIsRestrictive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	store := &stateStore{path: path}

	if err := store.Save(emptyReconcileState()); err != nil {
		t.Fatalf("Save returned an unexpected error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file mode = %o, want 0600", perm)
	}
}

func TestStateStoreSaveCreatesDirWithRestrictiveMode(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "cenci")
	path := filepath.Join(stateDir, "reconcile.json")
	store := &stateStore{path: path}

	if err := store.Save(emptyReconcileState()); err != nil {
		t.Fatalf("Save returned an unexpected error: %v", err)
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("newly created state dir mode = %o, want 0700", perm)
	}
}

// TestStateStoreSaveDoesNotFollowPlantedSymlinkAtLegacyTempName is the direct
// regression test for AC #5: a symlink pre-planted at the legacy predictable
// temp name (path+".tmp", the weaker pattern this ticket replaces) pointing
// outside the state directory must never be written through, because
// os.CreateTemp's randomized name never collides with it.
func TestStateStoreSaveDoesNotFollowPlantedSymlinkAtLegacyTempName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")

	outside := t.TempDir()
	targetPath := filepath.Join(outside, "victim.json")
	originalContent := []byte("do-not-overwrite")
	if err := os.WriteFile(targetPath, originalContent, 0o600); err != nil {
		t.Fatalf("writing outside target: %v", err)
	}

	legacyTempName := path + ".tmp"
	if err := os.Symlink(targetPath, legacyTempName); err != nil {
		t.Fatalf("planting symlink: %v", err)
	}

	store := &stateStore{path: path}
	if err := store.Save(ReconcileState{
		Observations:  map[string]time.Time{"o/r#1": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		ApplyFailures: map[string]int{},
	}); err != nil {
		t.Fatalf("Save returned an unexpected error: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading outside target: %v", err)
	}
	if string(got) != string(originalContent) {
		t.Errorf("outside target was written through the planted symlink: got %q, want unchanged %q", got, originalContent)
	}
}

// TestStateStoreSaveSweepsAgedTempsButKeepsFreshOnes is the direct test for
// sweepStaleTemps's concurrent-writer safety: an aged (>1h) leftover temp
// from a prior crashed save is swept, but a fresh temp (standing in for a
// concurrently in-flight daemon/cron writer) is left alone.
func TestStateStoreSaveSweepsAgedTempsButKeepsFreshOnes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")

	aged := filepath.Join(dir, "reconcile.json.aged123.tmp")
	fresh := filepath.Join(dir, "reconcile.json.fresh456.tmp")
	if err := os.WriteFile(aged, []byte("stale"), 0o600); err != nil {
		t.Fatalf("writing aged temp: %v", err)
	}
	if err := os.WriteFile(fresh, []byte("fresh"), 0o600); err != nil {
		t.Fatalf("writing fresh temp: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(aged, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	store := &stateStore{path: path}
	if err := store.Save(emptyReconcileState()); err != nil {
		t.Fatalf("Save returned an unexpected error: %v", err)
	}

	if _, err := os.Stat(aged); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected the aged (>1h) temp file to be swept, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("expected the fresh temp file to be retained (concurrent-writer safety), stat err = %v", err)
	}
}

// -- #1291: OOM sections (last-seen counters, per-ticket baselines, retry times)

const oomTestContainer = "docker/claude-cenci-local"

// writeStateBody writes body as reconcile.json in a fresh dir and returns a
// store over it.
func writeStateBody(t *testing.T, body string) *stateStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reconcile.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return &stateStore{path: path}
}

// AC9: the three OOM sections survive a Save/Load cycle through the
// crash-safe store, under schemaVersion 1.
func TestStateStore_OOMSectionsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reconcile.json")
	store := &stateStore{path: path}
	retryAt := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	want := ReconcileState{
		Observations:  map[string]time.Time{"o/r#42": retryAt.Add(-time.Minute)},
		ApplyFailures: map[string]int{},
		OOMLastSeen:   map[string]int64{oomTestContainer: 4},
		OOMBaselines:  map[string]map[string]int64{"o/r#42": {oomTestContainer: 3}},
		OOMRetryAt:    map[string]time.Time{"o/r#7": retryAt},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OOMDropped != nil {
		t.Errorf("OOMDropped = %v, want nil for valid sections", got.OOMDropped)
	}
	if got.OOMLastSeen[oomTestContainer] != 4 || len(got.OOMLastSeen) != 1 {
		t.Errorf("OOMLastSeen = %v, want {%s: 4}", got.OOMLastSeen, oomTestContainer)
	}
	if got.OOMBaselines["o/r#42"][oomTestContainer] != 3 || len(got.OOMBaselines) != 1 {
		t.Errorf("OOMBaselines = %v, want {o/r#42: {%s: 3}}", got.OOMBaselines, oomTestContainer)
	}
	if !got.OOMRetryAt["o/r#7"].Equal(retryAt) || len(got.OOMRetryAt) != 1 {
		t.Errorf("OOMRetryAt = %v, want {o/r#7: %v}", got.OOMRetryAt, retryAt)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	var onDisk map[string]json.RawMessage
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parsing saved file: %v", err)
	}
	if string(onDisk["schemaVersion"]) != "1" {
		t.Errorf("schemaVersion = %s, want 1", onDisk["schemaVersion"])
	}
	for _, key := range []string{"oomLastSeen", "oomBaselines", "oomRetryAt"} {
		if _, ok := onDisk[key]; !ok {
			t.Errorf("saved file has no %q key:\n%s", key, raw)
		}
	}
}

// A pass with no OOM data writes exactly the bytes a pre-#1291 build wrote.
func TestStateStore_NoOOMDataWritesTodaysBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reconcile.json")
	store := &stateStore{path: path}
	obs := map[string]time.Time{"o/r#42": time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	failures := map[string]int{"o/r#42": 1}
	if err := store.Save(ReconcileState{Observations: obs, ApplyFailures: failures, OOMLastSeen: map[string]int64{}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	legacy, err := json.MarshalIndent(struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Observations  map[string]time.Time `json:"observations"`
		ApplyFailures map[string]int       `json:"applyFailures"`
	}{1, obs, failures}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(legacy) {
		t.Errorf("saved bytes:\n%s\nwant today's bytes:\n%s", raw, legacy)
	}
}

// AC9: a file written before #1291 (no OOM keys) loads with empty OOM sections
// and nothing dropped.
func TestStateStore_MissingOOMSectionsLoadEmpty(t *testing.T) {
	store := writeStateBody(t, `{"schemaVersion":1,"observations":{"o/r#42":"2026-10-19T12:00:00Z"},"applyFailures":{"o/r#42":1}}`)
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OOMDropped != nil {
		t.Errorf("OOMDropped = %v, want nil: missing sections are not invalid", got.OOMDropped)
	}
	if got.OOMLastSeen == nil || got.OOMBaselines == nil || got.OOMRetryAt == nil {
		t.Errorf("OOM sections must load as empty, non-nil maps, got %+v", got)
	}
	if len(got.OOMLastSeen)+len(got.OOMBaselines)+len(got.OOMRetryAt) != 0 {
		t.Errorf("OOM sections = %v %v %v, want empty", got.OOMLastSeen, got.OOMBaselines, got.OOMRetryAt)
	}
}

// AC9: an OOM section of the wrong JSON type or holding an invalid entry never
// holds the pass: Load succeeds, Observations and ApplyFailures load unchanged,
// every OOM section is empty, and OOMDropped carries the reason for the one log
// line applyReconcile writes. A valid section beside the invalid one is dropped
// too, so the three sections never disagree.
func TestStateStore_InvalidOOMSectionsLoadEmptyWithoutHold(t *testing.T) {
	const valid = `"oomRetryAt":{"o/r#7":"2026-10-19T11:00:00Z"}`
	cases := []struct {
		name    string
		section string
	}{
		{"last-seen of the wrong type", `"oomLastSeen":"docker/claude-cenci-local"`},
		{"baselines of the wrong type", `"oomBaselines":[1,2]`},
		{"baseline container map of the wrong type", `"oomBaselines":{"o/r#42":5}`},
		{"retry time of the wrong type", `"oomRetryAt":{"o/r#42":5}`},
		{"empty container key in last-seen", `"oomLastSeen":{"":3}`},
		{"negative last-seen counter", `"oomLastSeen":{"docker/claude-cenci-local":-1}`},
		{"empty ticket key in baselines", `"oomBaselines":{"":{"docker/claude-cenci-local":1}}`},
		{"empty container key in a baseline", `"oomBaselines":{"o/r#42":{"":1}}`},
		{"negative baseline", `"oomBaselines":{"o/r#42":{"docker/claude-cenci-local":-2}}`},
		{"empty ticket key in retry times", `"oomRetryAt":{"":"2026-10-19T11:00:00Z"}`},
		{"zero retry timestamp", `"oomRetryAt":{"o/r#42":"0001-01-01T00:00:00Z"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"schemaVersion":1,"observations":{"o/r#42":"2026-10-19T12:00:00Z"},"applyFailures":{"o/r#42":2},` + tc.section
			if !strings.HasPrefix(tc.section, `"oomRetryAt"`) {
				body += "," + valid
			}
			store := writeStateBody(t, body+"}")

			got, err := store.Load()
			if err != nil {
				t.Fatalf("Load = %v, want no error: an invalid OOM section must never become a StateLoadError hold", err)
			}
			if got.OOMDropped == nil {
				t.Error("OOMDropped = nil, want the reason the OOM sections were dropped")
			}
			if len(got.OOMLastSeen)+len(got.OOMBaselines)+len(got.OOMRetryAt) != 0 {
				t.Errorf("OOM sections = %v %v %v, want all empty", got.OOMLastSeen, got.OOMBaselines, got.OOMRetryAt)
			}
			if got.OOMLastSeen == nil || got.OOMBaselines == nil || got.OOMRetryAt == nil {
				t.Error("dropped OOM sections must still be non-nil maps")
			}
			if !got.Observations["o/r#42"].Equal(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)) || len(got.Observations) != 1 {
				t.Errorf("Observations = %v, want the file's single observation unchanged", got.Observations)
			}
			if got.ApplyFailures["o/r#42"] != 2 || len(got.ApplyFailures) != 1 {
				t.Errorf("ApplyFailures = %v, want {o/r#42: 2} unchanged", got.ApplyFailures)
			}
		})
	}
}

// The dispatch-side backoff load reads the reconciler's OOM retry times
// read-only from the default state path; an absent file is no backoff, and an
// unreadable file or dropped OOM sections are an error the dispatcher logs.
func TestDefaultOOMBackoffLoad(t *testing.T) {
	retryAt := time.Date(2026, 10, 19, 11, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		body    string // "" leaves no file
		want    map[string]time.Time
		wantErr bool
	}{
		{name: "absent file", want: map[string]time.Time{}},
		{name: "retry times", body: `{"schemaVersion":1,"observations":{},"applyFailures":{},"oomRetryAt":{"o/r#42":"2026-10-19T11:00:00Z"}}`,
			want: map[string]time.Time{"o/r#42": retryAt}},
		{name: "corrupt file", body: `{"observations":{`, wantErr: true},
		{name: "invalid OOM section", body: `{"schemaVersion":1,"oomRetryAt":{"o/r#42":"0001-01-01T00:00:00Z"}}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateHome := t.TempDir()
			t.Setenv("XDG_STATE_HOME", stateHome)
			if tc.body != "" {
				if err := os.MkdirAll(filepath.Join(stateHome, "cenci"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stateHome, "cenci", "reconcile.json"), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			got, err := defaultOOMBackoffLoad()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("defaultOOMBackoffLoad = %v, nil; want an error", got)
				}
				if got != nil {
					t.Errorf("map = %v, want nil beside an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("defaultOOMBackoffLoad: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("map = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if !got[k].Equal(v) {
					t.Errorf("map[%s] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}
