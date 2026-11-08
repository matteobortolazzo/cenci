package watch_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// -- StatusSummary.Escalated (#826) -----------------------------------------

// TestStatusSummary_EscalatedWireKey locks in the JSON key "escalated" for
// the new counter, distinct from the pre-existing "need_input" key -- the
// exact naming asymmetry the plan calls out (label "Input Needed", internal
// counter "Escalated"/"escalated", deliberately NOT "input_needed" so it
// cannot be confused with NeedInput/"need_input" at a glance).
func TestStatusSummary_EscalatedWireKey(t *testing.T) {
	data, err := json.Marshal(watch.StatusSummary{NeedInput: 1, Escalated: 2})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"escalated":2`) {
		t.Errorf("marshaled summary = %s, want an %q key", data, `"escalated":2`)
	}
	if !strings.Contains(string(data), `"need_input":1`) {
		t.Errorf("marshaled summary = %s, want the pre-existing %q key preserved", data, `"need_input":1`)
	}
}

// -- WindowState.Socket (#1429) ---------------------------------------------

// A window on the default server (an empty socket) carries no "socket" key, so
// a snapshot is byte-identical to one from a daemon that predates the field.
// This is a JSON-shape unit test on purpose: an integration test decoding into
// the same struct cannot catch a tag or omitempty typo.
func TestWindowState_SocketOmittedWhenEmpty(t *testing.T) {
	data, err := json.Marshal(watch.WindowState{Session: "main", WindowIndex: "0", PaneID: "%5", Status: "idle"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), `"socket"`) {
		t.Errorf("marshaled window = %s, want no %q key when Socket is empty", data, "socket")
	}
}

// A window on a named server surfaces its socket under the wire key "socket".
func TestWindowState_SocketPresentWhenSet(t *testing.T) {
	const socket = "/run/user/1000/tmux-1000/work"
	data, err := json.Marshal(watch.WindowState{Session: "main", WindowIndex: "0", PaneID: "%5", Status: "idle", Socket: socket})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if want := `"socket":"` + socket + `"`; !strings.Contains(string(data), want) {
		t.Errorf("marshaled window = %s, want %s", data, want)
	}

	var back watch.WindowState
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if back.Socket != socket {
		t.Errorf("round-tripped Socket = %q, want %q", back.Socket, socket)
	}
}

// A snapshot from an older daemon has no "socket" key and still decodes, with an
// empty Socket (the default server), leaving every other field intact.
func TestWindowState_OlderSnapshotWithoutSocketDecodes(t *testing.T) {
	const older = `{"session":"main","window_index":"2","pane_id":"%7","window_name":"42-implement","task_name":"t","status":"running","manually_named":false}`
	var w watch.WindowState
	if err := json.Unmarshal([]byte(older), &w); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if w.Socket != "" {
		t.Errorf("Socket = %q, want empty for a snapshot without the key", w.Socket)
	}
	if w.Session != "main" || w.WindowIndex != "2" || w.PaneID != "%7" || w.WindowName != "42-implement" || w.Status != "running" {
		t.Errorf("decoded window = %+v, want the older fields intact", w)
	}
}

// -- StateSnapshot.Dispatch (#219) -----------------------------------------

// TestStateSnapshot_DispatchOmittedWhenNil locks in that a nil Dispatch field
// leaves the "dispatch" key out of the marshaled JSON entirely, so every
// existing NDJSON consumer of StateSnapshot (predating this field) is
// completely unaffected until #220 wires up live population.
func TestStateSnapshot_DispatchOmittedWhenNil(t *testing.T) {
	snap := watch.StateSnapshot{Timestamp: "t1", Summary: watch.StatusSummary{Total: 1}}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), `"dispatch"`) {
		t.Errorf("marshaled snapshot = %s, want no %q key when Dispatch is nil", data, "dispatch")
	}
}

// TestStateSnapshot_DispatchKeyPresentWhenPopulated locks in that a non-nil
// Dispatch field surfaces as a top-level "dispatch" key alongside the
// existing StateSnapshot keys.
func TestStateSnapshot_DispatchKeyPresentWhenPopulated(t *testing.T) {
	snap := watch.StateSnapshot{
		Timestamp: "t1",
		Summary:   watch.StatusSummary{Total: 1},
		Dispatch:  &watch.DispatchState{Enabled: true},
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := got["dispatch"]; !ok {
		t.Fatalf("marshaled snapshot = %s, want a %q key when Dispatch is populated", data, "dispatch")
	}
	if _, ok := got["timestamp"]; !ok {
		t.Errorf("marshaled snapshot = %s, want the pre-existing %q key preserved", data, "timestamp")
	}
	if _, ok := got["summary"]; !ok {
		t.Errorf("marshaled snapshot = %s, want the pre-existing %q key preserved", data, "summary")
	}
}

// -- DispatchState wire shape (#219) ----------------------------------------

// TestDispatchState_ZeroValueOmitsEmptyFields locks in the exact tagged JSON
// shape for a zero-value DispatchState: Interval, LastRunAt, LastError, and
// ResolveError (#446) carry omitempty and must be absent, while the
// remaining fields (including the falsy bools and zero ints, which have no
// omitempty tag) must still be present.
func TestDispatchState_ZeroValueOmitsEmptyFields(t *testing.T) {
	data, err := json.Marshal(watch.DispatchState{})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	want := `{"enabled":false,"daemon_running":false,"pass_running":false,"last_dispatched":0,"last_skipped":0}`
	if string(data) != want {
		t.Errorf("zero-value DispatchState JSON = %s, want %s", data, want)
	}
}

// TestDispatchState_MarshalsAllTaggedFields locks in the exact tagged JSON
// shape for a fully populated DispatchState, so #220's daemon-side population
// can rely on this wire format staying stable. ResolveError (#446) is
// populated here too, locking in its "resolve_error" wire key.
func TestDispatchState_MarshalsAllTaggedFields(t *testing.T) {
	state := watch.DispatchState{
		Enabled:        true,
		DaemonRunning:  true,
		Interval:       "5m",
		PassRunning:    true,
		LastRunAt:      "2026-07-13T00:00:00Z",
		LastDispatched: 2,
		LastSkipped:    1,
		LastError:      "boom",
		ResolveError:   "read snapshot: unexpected end of JSON input",
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	want := `{"enabled":true,"daemon_running":true,"interval":"5m","pass_running":true,"last_run_at":"2026-07-13T00:00:00Z","last_dispatched":2,"last_skipped":1,"last_error":"boom","resolve_error":"read snapshot: unexpected end of JSON input"}`
	if string(data) != want {
		t.Errorf("populated DispatchState JSON = %s, want %s", data, want)
	}
}

// -- WindowState.NativeMeasurement (#1450) ----------------------------------

func ptr[T any](v T) *T { return &v }

// nativeFixture is a fully populated measurement: every optional key is
// present, so a key-set assertion sees the whole wire shape.
func nativeFixture() *watch.NativeMeasurement {
	return &watch.NativeMeasurement{
		MeasuredAt:    "2026-11-08T11:59:00Z",
		Source:        "claude-mods",
		ClaudeVersion: "2.1.0",
		Context: &watch.NativeContext{
			Tokens:     ptr(int64(250000)),
			Window:     ptr(int64(200000)),
			Percent:    ptr(125.0),
			OverWindow: true,
		},
		Quota: []watch.NativeQuotaWindow{
			{Kind: "five_hour", PercentUsed: ptr(42.5), ResetsAt: "2026-11-08T15:00:00Z"},
		},
		Missing: []string{"claude_version"},
		Invalid: []string{},
	}
}

func snapshotWithNative(m *watch.NativeMeasurement) watch.StateSnapshot {
	return watch.StateSnapshot{
		Timestamp: "2026-11-08T12:00:00Z",
		Windows: []watch.WindowState{{
			Session: "main", WindowIndex: "0", WindowName: "42-implement",
			TaskName: "t", Status: "running", NativeMeasurement: m,
		}},
		Summary: watch.StatusSummary{Total: 1, Running: 1},
	}
}

func jsonKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode object %s: %v", raw, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func wantKeys(t *testing.T, what string, raw []byte, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := jsonKeys(t, raw); !reflect.DeepEqual(got, want) {
		t.Errorf("%s keys = %v, want %v (in %s)", what, got, want, raw)
	}
}

// allKeys collects every object key at any depth.
func allKeys(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			*out = append(*out, k)
			allKeys(child, out)
		}
	case []any:
		for _, child := range x {
			allKeys(child, out)
		}
	}
}

// TestWindowState_NativeMeasurementWireShape pins P2 and P6: the measurement
// sits under a per-window native_measurement object with separate context and
// quota members, and the snapshot gains no other key.
func TestWindowState_NativeMeasurementWireShape(t *testing.T) {
	data, err := json.Marshal(snapshotWithNative(nativeFixture()))
	if err != nil {
		t.Fatal(err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "StateSnapshot", data, "timestamp", "windows", "summary")

	var windows []json.RawMessage
	if err := json.Unmarshal(top["windows"], &windows); err != nil || len(windows) != 1 {
		t.Fatalf("windows = %s, err %v", top["windows"], err)
	}
	wantKeys(t, "WindowState", windows[0],
		"session", "window_index", "window_name", "task_name", "status", "manually_named", "native_measurement")

	var win map[string]json.RawMessage
	if err := json.Unmarshal(windows[0], &win); err != nil {
		t.Fatal(err)
	}
	nm := win["native_measurement"]
	wantKeys(t, "native_measurement", nm,
		"measured_at", "source", "claude_version", "context", "quota", "missing", "invalid")

	var parts map[string]json.RawMessage
	if err := json.Unmarshal(nm, &parts); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "context", parts["context"], "tokens", "window", "percent", "over_window")

	var quota []json.RawMessage
	if err := json.Unmarshal(parts["quota"], &quota); err != nil || len(quota) != 1 {
		t.Fatalf("quota = %s, err %v", parts["quota"], err)
	}
	wantKeys(t, "quota entry", quota[0], "kind", "percent_used", "resets_at")
}

// TestStateSnapshot_NativeMeasurementCarriesNoAccountOrAggregateKey pins P6:
// measurements are per window only. No key anywhere in the encoded snapshot may
// name an account, profile, fleet, organisation, user or aggregate.
func TestStateSnapshot_NativeMeasurementCarriesNoAccountOrAggregateKey(t *testing.T) {
	data, err := json.Marshal(snapshotWithNative(nativeFixture()))
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	var keys []string
	allKeys(decoded, &keys)
	if !contains(keys, "native_measurement") {
		t.Fatalf("precondition: native_measurement missing from %s", data)
	}
	for _, k := range keys {
		for _, banned := range []string{"account", "profile", "fleet", "aggregate", "org", "user", "email", "total_quota", "overall"} {
			if strings.Contains(strings.ToLower(k), banned) {
				t.Errorf("snapshot key %q names %q; native measurements are per window only", k, banned)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestWindowState_NativeMeasurementOmittedWhenNil keeps every pre-#1450 NDJSON
// consumer unaffected: a window without a measurement has no such key.
func TestWindowState_NativeMeasurementOmittedWhenNil(t *testing.T) {
	data, err := json.Marshal(watch.WindowState{Session: "main", Status: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "native_measurement") {
		t.Errorf("window JSON = %s, want no native_measurement key when nil", data)
	}
}

// TestStateSnapshot_HeadroomKeepsItsShapeAlongsideNativeMeasurement pins P2:
// the existing top-level headroom key, type and values do not change.
func TestStateSnapshot_HeadroomKeepsItsShapeAlongsideNativeMeasurement(t *testing.T) {
	snap := snapshotWithNative(nativeFixture())
	snap.Headroom = map[string]float64{"claude": 0.4}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	if got := string(top["headroom"]); got != `{"claude":0.4}` {
		t.Errorf("headroom = %s, want {\"claude\":0.4}", got)
	}
}

// TestNativeMeasurement_QuotaNullVersusEmptyArray pins the distinction between
// an absent rate_limits key (null) and an empty array ([]).
func TestNativeMeasurement_QuotaNullVersusEmptyArray(t *testing.T) {
	absent := nativeFixture()
	absent.Quota = nil
	empty := nativeFixture()
	empty.Quota = []watch.NativeQuotaWindow{}

	absentJSON, err := json.Marshal(absent)
	if err != nil {
		t.Fatal(err)
	}
	emptyJSON, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(absentJSON), `"quota":null`) {
		t.Errorf("absent quota JSON = %s, want \"quota\":null", absentJSON)
	}
	if !strings.Contains(string(emptyJSON), `"quota":[]`) {
		t.Errorf("empty quota JSON = %s, want \"quota\":[]", emptyJSON)
	}
}

// TestNativeMeasurement_MissingAndInvalidEncodeAsEmptyArrays pins the
// nil-slice JSON rule (watch/docs/go-gotchas.md): an empty list is [], and the
// keys are never omitted, so a consumer can tell "nothing missing" from an old
// daemon.
func TestNativeMeasurement_MissingAndInvalidEncodeAsEmptyArrays(t *testing.T) {
	m := nativeFixture()
	m.Missing = make([]string, 0)
	m.Invalid = make([]string, 0)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"missing":[]`, `"invalid":[]`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON = %s, want %s", data, want)
		}
	}
	if strings.Contains(string(data), `"missing":null`) || strings.Contains(string(data), `"invalid":null`) {
		t.Errorf("JSON = %s, empty lists must never encode as null", data)
	}
}

// TestNativeMeasurement_RoundTripKeepsRejectedValuesNil pins "a rejected value
// is a nil pointer, never 0" across the wire.
func TestNativeMeasurement_RoundTripKeepsRejectedValuesNil(t *testing.T) {
	m := nativeFixture()
	m.Context = &watch.NativeContext{Window: ptr(int64(200000))}
	data, err := json.Marshal(snapshotWithNative(m))
	if err != nil {
		t.Fatal(err)
	}
	var got watch.StateSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	nm := got.Windows[0].NativeMeasurement
	if nm == nil || nm.Context == nil {
		t.Fatalf("decoded snapshot lost the measurement: %s", data)
	}
	if nm.Context.Tokens != nil || nm.Context.Percent != nil {
		t.Errorf("rejected tokens/percent decoded as %v/%v, want nil (not zero)", nm.Context.Tokens, nm.Context.Percent)
	}
	if nm.Context.Window == nil || *nm.Context.Window != 200000 {
		t.Errorf("window = %v, want 200000", nm.Context.Window)
	}
}

// preChangeWindowState and preChangeSnapshot are test-local copies of the
// snapshot structs as they were before #1450, standing in for an old consumer.
type preChangeWindowState struct {
	Session       string `json:"session"`
	WindowIndex   string `json:"window_index"`
	PaneID        string `json:"pane_id,omitempty"`
	WindowName    string `json:"window_name"`
	TaskName      string `json:"task_name"`
	Status        string `json:"status"`
	Agent         string `json:"agent,omitempty"`
	ManuallyNamed bool   `json:"manually_named"`
}

type preChangeSnapshot struct {
	Timestamp string                 `json:"timestamp"`
	Windows   []preChangeWindowState `json:"windows"`
	Summary   watch.StatusSummary    `json:"summary"`
	Headroom  map[string]float64     `json:"headroom,omitempty"`
}

// TestStateSnapshot_NewJSONDecodesIntoPreChangeStruct pins P8 (second half): an
// old consumer decodes a snapshot that carries native_measurement without error
// and keeps every field it knows.
func TestStateSnapshot_NewJSONDecodesIntoPreChangeStruct(t *testing.T) {
	data, err := json.Marshal(snapshotWithNative(nativeFixture()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"native_measurement"`) {
		t.Fatalf("precondition: snapshot JSON %s carries no native_measurement", data)
	}
	var old preChangeSnapshot
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatalf("pre-change struct failed to decode a new snapshot: %v", err)
	}
	if len(old.Windows) != 1 || old.Windows[0].WindowName != "42-implement" || old.Windows[0].Status != "running" {
		t.Errorf("pre-change decode = %+v, want the window fields intact", old)
	}
	if old.Summary.Running != 1 {
		t.Errorf("pre-change summary = %+v, want running=1", old.Summary)
	}
}

// TestStateSnapshot_PreChangeJSONDecodesWithNilMeasurement pins P8 (first
// half): a snapshot line from an older daemon has no native_measurement key and
// decodes with a nil measurement.
func TestStateSnapshot_PreChangeJSONDecodesWithNilMeasurement(t *testing.T) {
	old := preChangeSnapshot{
		Timestamp: "2026-11-08T12:00:00Z",
		Windows:   []preChangeWindowState{{Session: "main", WindowIndex: "0", WindowName: "42-implement", TaskName: "t", Status: "idle"}},
		Summary:   watch.StatusSummary{Total: 1, Idle: 1},
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var got watch.StateSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode pre-change snapshot: %v", err)
	}
	if len(got.Windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(got.Windows))
	}
	if got.Windows[0].NativeMeasurement != nil {
		t.Errorf("native measurement = %+v, want nil for a pre-change snapshot", got.Windows[0].NativeMeasurement)
	}
}

// TestStateSnapshot_NativeMeasurementRoundTrips proves the decode side of the
// public wire contract: what the daemon encodes, a client reads back whole.
func TestStateSnapshot_NativeMeasurementRoundTrips(t *testing.T) {
	want := nativeFixture()
	data, err := json.Marshal(snapshotWithNative(want))
	if err != nil {
		t.Fatal(err)
	}
	var got watch.StateSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Windows[0].NativeMeasurement, want) {
		t.Errorf("round trip = %+v, want %+v", got.Windows[0].NativeMeasurement, want)
	}
}
