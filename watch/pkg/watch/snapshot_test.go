package watch_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v4/pkg/watch"
)

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
// shape for a zero-value DispatchState: Interval, LastRunAt, and LastError
// carry omitempty and must be absent, while the remaining fields (including
// the falsy bools and zero ints, which have no omitempty tag) must still be
// present.
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
// can rely on this wire format staying stable.
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
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	want := `{"enabled":true,"daemon_running":true,"interval":"5m","pass_running":true,"last_run_at":"2026-07-13T00:00:00Z","last_dispatched":2,"last_skipped":1,"last_error":"boom"}`
	if string(data) != want {
		t.Errorf("populated DispatchState JSON = %s, want %s", data, want)
	}
}
