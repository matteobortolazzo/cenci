package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/measurement"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// Native Claude measurements (#1450) are display-only, session-scoped state:
// they never change status, LastEvent or the window, never create a session,
// and are re-validated here because containers write to the same socket.

type measurementClock struct{ t time.Time }

func (c *measurementClock) now() time.Time { return c.t }

func (c *measurementClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newMeasurementDaemon(t *testing.T, mc *tmuxtest.MockClient) (*Daemon, *measurementClock) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	d := newTestDaemon(mc)
	clk := &measurementClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	d.now = clk.now
	return d, clk
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// sampleBody renders a complete, valid measurement whose context tokens carry
// the given value, so two samples can be told apart in the snapshot.
func sampleBody(session string, measuredAt time.Time, tokens int64) string {
	return fmt.Sprintf(`{"session_id":%q,"measured_at":%q,"source":"claude-mods","claude_version":"2.1.0",`+
		`"context":{"tokens":%d,"window":200000,"percent":%g},`+
		`"rate_limits":[{"kind":"five_hour","percent_used":10,"resets_at":%q},{"kind":"seven_day","percent_used":20,"resets_at":%q}]}`,
		session, rfc3339(measuredAt), tokens, float64(tokens)/2000, rfc3339(measuredAt.Add(2*time.Hour)), rfc3339(measuredAt.Add(48*time.Hour)))
}

func envelope(body string) ipc.NativeMeasurement {
	return ipc.NativeMeasurement{Measurement: json.RawMessage(body)}
}

func (d *Daemon) deliverSample(session string, measuredAt time.Time, tokens int64) {
	d.applyNativeMeasurement(envelope(sampleBody(session, measuredAt, tokens)))
}

// track makes session a tracked, running session whose window is findable by task.
func track(d *Daemon, session, task string) {
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: session, Agent: "claude", TaskName: task})
}

func windowFor(t *testing.T, snap ipc.StateSnapshot, task string) ipc.WindowState {
	t.Helper()
	w := windowByTask(&snap, task)
	if w == nil {
		t.Fatalf("no window with task %q in %+v", task, snap.Windows)
	}
	return *w
}

// storedTokens reports the context tokens the window shows, and whether it
// carries a measurement with a context at all.
func storedTokens(w ipc.WindowState) (int64, bool) {
	if w.NativeMeasurement == nil || w.NativeMeasurement.Context == nil || w.NativeMeasurement.Context.Tokens == nil {
		return 0, false
	}
	return *w.NativeMeasurement.Context.Tokens, true
}

func wantTokens(t *testing.T, w ipc.WindowState, want int64) {
	t.Helper()
	got, ok := storedTokens(w)
	if !ok {
		t.Fatalf("window shows no measurement tokens (native = %+v), want %d", w.NativeMeasurement, want)
	}
	if got != want {
		t.Errorf("stored tokens = %d, want %d", got, want)
	}
}

func wantNoMeasurement(t *testing.T, w ipc.WindowState) {
	t.Helper()
	if w.NativeMeasurement != nil {
		t.Errorf("window shows a measurement %+v, want none", w.NativeMeasurement)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// -- storage and display-only behavior -----------------------------------------

func TestNativeMeasurement_StoredOnTrackedSessionAndLeavesStatusAndHeadroomAlone(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	d.headroom = map[string]float64{"claude": 0.4}
	track(d, "s1", "task-one")
	headroomBefore := d.buildSnapshot().Headroom

	d.deliverSample("s1", clk.now().Add(-time.Minute), 4242)

	snap := d.buildSnapshot()
	w := windowFor(t, snap, "task-one")
	wantTokens(t, w, 4242)
	m := w.NativeMeasurement
	if m.Source != "claude-mods" || m.ClaudeVersion != "2.1.0" {
		t.Errorf("source/version = %q/%q", m.Source, m.ClaudeVersion)
	}
	if len(m.Quota) != 2 || m.Quota[0].Kind != "five_hour" || m.Quota[1].Kind != "weekly" {
		t.Errorf("quota = %+v, want five_hour and weekly", m.Quota)
	}
	if len(m.Missing) != 0 || len(m.Invalid) != 0 {
		t.Errorf("missing/invalid = %v/%v, want none", m.Missing, m.Invalid)
	}
	if w.Status != "running" || snap.Summary.Running != 1 || snap.Summary.Total != 1 {
		t.Errorf("status %q summary %+v: a measurement must not change status", w.Status, snap.Summary)
	}
	if !reflect.DeepEqual(snap.Headroom, headroomBefore) || snap.Headroom["claude"] != 0.4 {
		t.Errorf("headroom = %v, want the existing key and value untouched (%v)", snap.Headroom, headroomBefore)
	}
}

// spyFrontend counts OnEvent calls over the real tmux frontend (P7: a
// measurement must never reach frontend.OnEvent).
type spyFrontend struct {
	frontend.Frontend
	onEvents int
}

func (s *spyFrontend) OnEvent(sess *frontend.SessionState, event ipc.HookEvent) frontend.Observations {
	s.onEvents++
	return s.Frontend.OnEvent(sess, event)
}

// TestNativeMeasurement_NeverChangesStatusLastEventOrWindow pins P7 for idle and
// done sessions: status, LastEvent, window name and style stay put, and the
// frontend never sees the measurement.
func TestNativeMeasurement_NeverChangesStatusLastEventOrWindow(t *testing.T) {
	cases := []struct {
		name   string
		events []ipc.HookEvent
		status string
	}{
		{"idle", []ipc.HookEvent{
			{EventType: "SessionStart", SessionID: "s1", TmuxPane: "%0"},
		}, "idle"},
		{"done", []ipc.HookEvent{
			{EventType: "SessionStart", SessionID: "s1", TmuxPane: "%0"},
			{EventType: "UserPromptSubmit", SessionID: "s1", TmuxPane: "%0"},
			{EventType: "Stop", SessionID: "s1", TmuxPane: "%0"},
		}, "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{
				{SessionName: "main", WindowIndex: "0", WindowName: "docker", PaneIndex: "0",
					PaneCurrentCmd: "claude", PaneTitle: "⠋ working", PaneID: "%0"},
			}}
			d, clk := newMeasurementDaemon(t, mc)
			spy := &spyFrontend{Frontend: d.frontend}
			d.frontend = spy
			for _, e := range tc.events {
				d.handleEvent(e)
			}

			sess := d.sessions["s1"]
			if sess == nil || sess.Status.String() != tc.status {
				t.Fatalf("precondition: session = %+v, want status %s", sess, tc.status)
			}
			wi := d.frontend.WindowInfo("s1")
			if wi == nil {
				t.Fatal("precondition: expected a tracked window")
			}
			lastEvent, nameBefore := sess.LastEvent, wi.WindowName
			renames, opts, onEvents := len(mc.Renames), len(mc.WindowOpts), spy.onEvents

			clk.advance(10 * time.Minute)
			d.deliverSample("s1", clk.now().Add(-time.Minute), 777)

			wantTokens(t, onlyWindow(t, d), 777)
			if got := d.sessions["s1"].Status.String(); got != tc.status {
				t.Errorf("status = %s, want %s", got, tc.status)
			}
			if !d.sessions["s1"].LastEvent.Equal(lastEvent) {
				t.Errorf("LastEvent = %v, want %v unchanged", d.sessions["s1"].LastEvent, lastEvent)
			}
			if after := d.frontend.WindowInfo("s1"); after == nil || after.WindowName != nameBefore {
				t.Errorf("window = %+v, want name %q unchanged", after, nameBefore)
			}
			if len(mc.Renames) != renames || len(mc.WindowOpts) != opts {
				t.Errorf("tmux driven by a measurement: renames %d->%d, window options %d->%d",
					renames, len(mc.Renames), opts, len(mc.WindowOpts))
			}
			if spy.onEvents != onEvents {
				t.Errorf("OnEvent called %d time(s) for a measurement, want 0", spy.onEvents-onEvents)
			}
		})
	}
}

// TestNativeMeasurement_DoesNotKeepASessionAlive pins the bounded way out: a
// measurement does not refresh LastEvent, so a paneless session that only
// receives measurements still expires at the TTL.
func TestNativeMeasurement_DoesNotKeepASessionAlive(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	track(d, "s1", "task-one")

	d.deliverSample("s1", clk.now().Add(-time.Minute), 1)
	wantTokens(t, windowFor(t, d.buildSnapshot(), "task-one"), 1)

	for elapsed := time.Duration(0); elapsed <= d.cfg.SessionTTL; elapsed += 10 * time.Minute {
		clk.advance(10 * time.Minute)
		d.deliverSample("s1", clk.now().Add(-time.Minute), 2)
	}
	d.runSweep()

	if snap := d.buildSnapshot(); len(snap.Windows) != 0 {
		t.Errorf("windows = %+v, want the idle session expired despite its measurements", snap.Windows)
	}
}

// -- ordering: P22, P23 ---------------------------------------------------------

func TestNativeMeasurement_OlderSampleIsDroppedAndStoredOneUnchanged(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	track(d, "s1", "task-one")
	newer := clk.now().Add(-time.Minute)

	d.deliverSample("s1", newer, 100)
	d.deliverSample("s1", newer.Add(-time.Second), 999)

	w := windowFor(t, d.buildSnapshot(), "task-one")
	wantTokens(t, w, 100)
	if got, want := w.NativeMeasurement.MeasuredAt, rfc3339(newer); got != want {
		t.Errorf("measured_at = %q, want %q unchanged", got, want)
	}
}

func TestNativeMeasurement_DuplicateMeasuredAtIsDroppedAndStoredOneUnchanged(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	track(d, "s1", "task-one")
	at := clk.now().Add(-time.Minute)

	d.deliverSample("s1", at, 100)
	d.deliverSample("s1", at, 999) // a mod reload or a retried send

	wantTokens(t, windowFor(t, d.buildSnapshot(), "task-one"), 100)
}

func TestNativeMeasurement_NewerSampleReplacesTheStoredOne(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	track(d, "s1", "task-one")
	at := clk.now().Add(-2 * time.Minute)

	d.deliverSample("s1", at, 100)
	d.deliverSample("s1", at.Add(time.Second), 200)

	wantTokens(t, windowFor(t, d.buildSnapshot(), "task-one"), 200)
}

// -- isolation: P24, P25, P26 -----------------------------------------------------

func TestNativeMeasurement_TwoSessionsEachShowOnlyTheirOwnSample(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	track(d, "s1", "task-one")
	track(d, "s2", "task-two")
	at := clk.now().Add(-time.Minute)

	d.deliverSample("s1", at, 111)
	d.deliverSample("s2", at, 222)

	snap := d.buildSnapshot()
	wantTokens(t, windowFor(t, snap, "task-one"), 111)
	wantTokens(t, windowFor(t, snap, "task-two"), 222)
}

func TestNativeMeasurement_ForAnUntrackedSessionIsDroppedAndCreatesNoWindow(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	// Positive control: a tracked session takes a sample in the same fixture, so
	// "no window for the ghost" cannot pass just because every sample is dropped.
	track(d, "s1", "task-one")
	d.deliverSample("s1", clk.now().Add(-time.Minute), 1)
	wantTokens(t, windowFor(t, d.buildSnapshot(), "task-one"), 1)

	d.deliverSample("ghost", clk.now().Add(-time.Minute), 5)

	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 || snap.Summary.Total != 1 || len(d.sessions) != 1 {
		t.Errorf("windows %+v, summary %+v, sessions %d: a measurement must never create a session", snap.Windows, snap.Summary, len(d.sessions))
	}
}

func TestNativeMeasurement_ForAnEndedSessionIsDropped(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	track(d, "s1", "task-one")
	d.deliverSample("s1", clk.now().Add(-2*time.Minute), 1)
	wantTokens(t, windowFor(t, d.buildSnapshot(), "task-one"), 1)
	d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "s1"})

	d.deliverSample("s1", clk.now().Add(-time.Minute), 2) // a late sample after SessionEnd

	if snap := d.buildSnapshot(); len(snap.Windows) != 0 {
		t.Errorf("windows = %+v, want none: a late sample must not resurrect the session", snap.Windows)
	}
}

// TestNativeMeasurement_NeverMatchesAPaneKeyedSession pins the lookup rule:
// d.sessions[session_id] only, with no pane: fallback. A pane-only session
// cannot be reached by any session_id.
func TestNativeMeasurement_NeverMatchesAPaneKeyedSession(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", TmuxPane: "%0"}) // tracked as "pane:%0"
	if _, ok := d.sessions["pane:%0"]; !ok {
		t.Fatal("precondition: expected a pane-keyed session")
	}
	// Positive control on the same fixture: a tracked, id-keyed session does take a sample.
	track(d, "s1", "task-one")
	d.deliverSample("s1", clk.now().Add(-time.Minute), 1)
	wantTokens(t, windowFor(t, d.buildSnapshot(), "task-one"), 1)

	for _, id := range []string{"pane:%0", "%0", "0"} {
		d.deliverSample(id, clk.now().Add(-time.Minute), 9)
	}

	for _, w := range d.buildSnapshot().Windows {
		if w.TaskName != "task-one" {
			wantNoMeasurement(t, w)
		}
	}
	if len(d.sessions) != 2 {
		t.Errorf("sessions = %d, want 2: no session may be created", len(d.sessions))
	}
}

// TestNativeMeasurement_ForASessionWithAnInvalidIDIsDropped pins that the
// daemon validates session_id itself even when handleEvent tracked that key.
func TestNativeMeasurement_ForASessionWithAnInvalidIDIsDropped(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "bad id!"})
	track(d, "s1", "task-one")
	d.deliverSample("s1", clk.now().Add(-time.Minute), 1)
	wantTokens(t, windowFor(t, d.buildSnapshot(), "task-one"), 1)

	d.deliverSample("bad id!", clk.now().Add(-time.Minute), 9)

	for _, w := range d.buildSnapshot().Windows {
		if w.TaskName != "task-one" {
			wantNoMeasurement(t, w)
		}
	}
}

// TestNativeMeasurement_SuccessorOfClearOrResumeStartsWithNone pins P26: after
// SessionEnd with reason clear or resume, the successor shows no measurement
// until its own sample arrives, and the ended session's id no longer takes one.
func TestNativeMeasurement_SuccessorOfClearOrResumeStartsWithNone(t *testing.T) {
	for _, reason := range []string{"clear", "resume"} {
		t.Run(reason, func(t *testing.T) {
			mc := &tmuxtest.MockClient{Panes: []tmux.PaneInfo{
				{SessionName: "main", WindowIndex: "0", WindowName: "docker", PaneIndex: "0",
					PaneCurrentCmd: "claude", PaneTitle: "⠋ working", PaneID: "%0"},
			}}
			d, clk := newMeasurementDaemon(t, mc)
			d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sessA", TmuxPane: "%0"})
			d.deliverSample("sessA", clk.now().Add(-2*time.Minute), 111)
			wantTokens(t, onlyWindow(t, d), 111)

			d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "sessA", TmuxPane: "%0", SessionEndReason: reason})
			d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sessB", TmuxPane: "%0"})

			wantNoMeasurement(t, onlyWindow(t, d))

			d.deliverSample("sessA", clk.now().Add(-time.Minute), 222) // the ended id
			wantNoMeasurement(t, onlyWindow(t, d))

			d.deliverSample("sessB", clk.now().Add(-time.Minute), 333)
			wantTokens(t, onlyWindow(t, d), 333)
		})
	}
}

// -- re-validation: P4, P17, P19, P28, P29 -------------------------------------------

func TestNativeMeasurement_WholeSampleErrorsStoreNothingAndKeepTheStoredSample(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	track(d, "s1", "task-one")
	at := clk.now().Add(-time.Minute)
	d.deliverSample("s1", at, 111)
	wantTokens(t, windowFor(t, d.buildSnapshot(), "task-one"), 111)

	newer := at.Add(time.Second)
	bodies := map[string]string{
		"not JSON":                  `not json`,
		"array":                     `[]`,
		"no session":                strings.Replace(sampleBody("s1", newer, 999), `"session_id":"s1",`, "", 1),
		"empty session":             strings.Replace(sampleBody("s1", newer, 999), `"session_id":"s1"`, `"session_id":""`, 1),
		"measured_at missing":       strings.Replace(sampleBody("s1", newer, 999), `"measured_at":"`+rfc3339(newer)+`",`, "", 1),
		"measured_at not RFC 3339":  strings.Replace(sampleBody("s1", newer, 999), rfc3339(newer), "yesterday", 1),
		"measured_at in the future": sampleBody("s1", clk.now().Add(6*time.Minute), 999),
	}
	for name, body := range bodies {
		d.applyNativeMeasurement(envelope(body))
		if got, _ := storedTokens(windowFor(t, d.buildSnapshot(), "task-one")); got != 111 {
			t.Errorf("%s: stored tokens = %d, want 111 unchanged", name, got)
		}
	}
	if len(d.sessions) != 1 {
		t.Errorf("sessions = %d, want 1", len(d.sessions))
	}
}

// TestNativeMeasurement_InvalidFieldsAreRejectedPerFieldByTheDaemon pins P28's
// daemon half: an envelope that bypassed cenci notify gets the same per-field
// rejections, listed in missing and invalid, while the valid fields are kept.
func TestNativeMeasurement_InvalidFieldsAreRejectedPerFieldByTheDaemon(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	track(d, "s1", "task-one")
	at := clk.now().Add(-time.Minute)
	body := fmt.Sprintf(`{"session_id":"s1","measured_at":%q,"source":"claude-mods","claude_version":"2.1.0",`+
		`"context":{"tokens":"NaN","window":-5,"percent":50},`+
		`"rate_limits":[{"kind":"five_hour","percent_used":101,"resets_at":%q},{"kind":"seven_day\u001b[31m","percent_used":20,"resets_at":%q}]}`,
		rfc3339(at), rfc3339(at.Add(time.Hour)), rfc3339(at.Add(time.Hour)))

	d.applyNativeMeasurement(envelope(body))

	snap := d.buildSnapshot()
	w := windowFor(t, snap, "task-one")
	m := w.NativeMeasurement
	if m == nil || m.Context == nil {
		t.Fatalf("native = %+v, want the valid fields stored", m)
	}
	if m.Context.Tokens != nil || m.Context.Window != nil {
		t.Errorf("tokens/window = %v/%v, want both rejected (nil)", m.Context.Tokens, m.Context.Window)
	}
	if m.Context.Percent == nil || *m.Context.Percent != 50 {
		t.Errorf("percent = %v, want 50 kept", m.Context.Percent)
	}
	wantMissing := []string{"context.tokens", "context.window", "rate_limits[0].percent_used"}
	if got := sortedCopy(m.Missing); !reflect.DeepEqual(got, wantMissing) {
		t.Errorf("missing = %v, want %v", got, wantMissing)
	}
	if got := sortedCopy(m.Invalid); !reflect.DeepEqual(got, wantMissing) {
		t.Errorf("invalid = %v, want %v", got, wantMissing)
	}
	if len(m.Quota) != 2 || m.Quota[0].PercentUsed != nil || m.Quota[1].PercentUsed == nil {
		t.Fatalf("quota = %+v, want the first percent_used rejected and the second kept", m.Quota)
	}
	if m.Quota[1].Kind != "unknown:seven_day" {
		t.Errorf("second kind = %q, want unknown:seven_day (escape stripped, never the weekly window)", m.Quota[1].Kind)
	}
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"\x1b", `\u001b`} {
		if strings.Contains(string(encoded), raw) {
			t.Errorf("snapshot %s carries a raw escape", encoded)
		}
	}
}

// -- drop logging (watch/docs/hook-events.md) -----------------------------------------

func TestNativeMeasurement_EveryDropIsLoggedUnderVerboseOnly(t *testing.T) {
	drops := map[string]func(d *Daemon, clk *measurementClock){
		"untracked session": func(d *Daemon, clk *measurementClock) {
			d.deliverSample("ghost", clk.now().Add(-time.Minute), 1)
		},
		"malformed body": func(d *Daemon, _ *measurementClock) {
			d.applyNativeMeasurement(envelope(`not json`))
		},
		"older sample": func(d *Daemon, clk *measurementClock) {
			d.deliverSample("s1", clk.now().Add(-2*time.Minute), 1)
			d.deliverSample("s1", clk.now().Add(-3*time.Minute), 2)
		},
	}
	for name, drop := range drops {
		for _, verbose := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s verbose=%t", name, verbose), func(t *testing.T) {
				d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
				d.cfg.Verbose = verbose
				track(d, "s1", "task-one")
				buf := captureLog(t)

				drop(d, clk)

				if got := strings.TrimSpace(buf.String()); verbose && got == "" {
					t.Error("a dropped measurement left no log line under cfg.Verbose")
				} else if !verbose && got != "" {
					t.Errorf("a dropped measurement logged without cfg.Verbose: %q", got)
				}
			})
		}
	}
}

// -- pre-change daemon path (P27) ---------------------------------------------------------

// TestNativeMeasurement_PreChangeDaemonDropsTheEnvelopeAsAnEmptyKeyEvent feeds a
// decoded envelope to handleEvent the way an older daemon routes it: it carries
// no session id or pane, so it is dropped and creates nothing. Regression guard
// for existing behavior; it holds before and after #1450.
func TestNativeMeasurement_PreChangeDaemonDropsTheEnvelopeAsAnEmptyKeyEvent(t *testing.T) {
	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	line := `{"kind":"native-measurement","measurement":` + sampleBody("s1", clk.now().Add(-time.Minute), 5) + `}`
	var legacy ipc.HookEvent
	if err := json.Unmarshal([]byte(line), &legacy); err != nil {
		t.Fatal(err)
	}

	d.handleEvent(legacy)

	if len(d.sessions) != 0 || len(d.buildSnapshot().Windows) != 0 {
		t.Errorf("sessions %d, windows %+v: the envelope must be dropped", len(d.sessions), d.buildSnapshot().Windows)
	}
}

// -- end to end through the event socket (P28) ---------------------------------------------

func readLatestSnapshot(path string) *watch.StateSnapshot {
	c, err := watch.Dial(path)
	if err != nil {
		return nil
	}
	defer func() { _ = c.Close() }()
	// A server with nothing cached yet sends nothing; closing unblocks the read.
	timer := time.AfterFunc(300*time.Millisecond, func() { _ = c.Close() })
	defer timer.Stop()
	snap, err := c.ReadSnapshot()
	if err != nil {
		return nil
	}
	return snap
}

func awaitSnapshot(t *testing.T, path, what string, pred func(*watch.StateSnapshot) bool) *watch.StateSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snap := readLatestSnapshot(path); snap != nil && pred(snap) {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

func writeRawEventLine(t *testing.T, path, line string) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial event socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write event socket: %v", err)
	}
}

func windowByTask(snap *watch.StateSnapshot, task string) *watch.WindowState {
	for i := range snap.Windows {
		if snap.Windows[i].TaskName == task {
			return &snap.Windows[i]
		}
	}
	return nil
}

// TestNativeMeasurement_EndToEndThroughTheEventSocket drives a real
// EventReceiver, the daemon loop and a real broadcast server, and writes raw
// bytes straight to the event socket the way a container can. It pins that
// attachReceiver wires the measurement channel into the loop, that the daemon
// re-validates (P28), and that the snapshot reaches a client.
func TestNativeMeasurement_EndToEndThroughTheEventSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CENCI_SOCKET_DIR", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	eventPath := filepath.Join(dir, "e.sock")
	broadcastPath := filepath.Join(dir, "b.sock")

	recv, err := ipc.NewEventReceiver(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	srv, err := ipc.NewServer(broadcastPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	d := newTestDaemon(&tmuxtest.MockClient{})
	d.events = recv.Events()
	d.ipc = srv
	d.attachReceiver(recv)

	ctx, cancel := context.WithCancel(context.Background())
	go recv.Accept(ctx)
	go srv.Accept(ctx)
	done := make(chan error, 1)
	go func() { done <- d.loop(ctx, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("loop returned %v, want nil", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("loop did not exit after cancel")
		}
	})

	if err := ipc.SendEvent(eventPath, ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "e2e-sess", Agent: "claude", TaskName: "e2e-task"}); err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	awaitSnapshot(t, broadcastPath, "the tracked session", func(s *watch.StateSnapshot) bool {
		return windowByTask(s, "e2e-task") != nil
	})

	now := time.Now().UTC()
	line := func(body string) string { return `{"kind":"native-measurement","measurement":` + body + `}` }
	// Whole-sample failures, none of which may be stored or create a window.
	writeRawEventLine(t, eventPath, `{"kind":"native-measurement","measurement":{"session_id":"e2e-sess","measured_at":"`+rfc3339(now)+`","context":{"tokens":NaN}}}`)
	writeRawEventLine(t, eventPath, line(sampleBody("ghost", now.Add(-time.Minute), 1)))
	writeRawEventLine(t, eventPath, line(sampleBody("../x", now.Add(-time.Minute), 1)))
	writeRawEventLine(t, eventPath, line(sampleBody("e2e-sess", now.Add(time.Hour), 1)))
	// Per-field failures: delivered, then rejected field by field.
	invalidFields := fmt.Sprintf(`{"session_id":"e2e-sess","measured_at":%q,"source":"claude-mods","claude_version":"2.1.0",`+
		`"context":{"tokens":"NaN","window":-5,"percent":50},`+
		`"rate_limits":[{"kind":"five_hour","percent_used":101,"resets_at":%q},{"kind":"seven_day\u001b[31m","percent_used":20,"resets_at":%q}]}`,
		rfc3339(now.Add(-2*time.Minute)), rfc3339(now.Add(time.Hour)), rfc3339(now.Add(time.Hour)))
	writeRawEventLine(t, eventPath, line(invalidFields))

	first := awaitSnapshot(t, broadcastPath, "the partially valid measurement", func(s *watch.StateSnapshot) bool {
		w := windowByTask(s, "e2e-task")
		return w != nil && w.NativeMeasurement != nil
	})
	m := windowByTask(first, "e2e-task").NativeMeasurement
	if m.Context == nil || m.Context.Tokens != nil || m.Context.Window != nil {
		t.Fatalf("context = %+v, want tokens and window rejected", m.Context)
	}
	if m.Context.Percent == nil || *m.Context.Percent != 50 {
		t.Errorf("percent = %v, want 50 kept", m.Context.Percent)
	}
	wantMissing := []string{"context.tokens", "context.window", "rate_limits[0].percent_used"}
	if got := sortedCopy(m.Missing); !reflect.DeepEqual(got, wantMissing) {
		t.Errorf("missing = %v, want %v", got, wantMissing)
	}
	if len(m.Quota) != 2 || m.Quota[1].Kind != "unknown:seven_day" {
		t.Errorf("quota = %+v, want the escape stripped from the second kind", m.Quota)
	}

	// A newer valid sample is a best-effort barrier only: the receiver runs one
	// goroutine per connection, so it does not order the lines sent above. Each
	// raw line is also covered by a deterministic apply-level test; this poll
	// catches a wrongly stored future-dated sample or a ghost window in the
	// common case where those lines were handled first.
	writeRawEventLine(t, eventPath, line(sampleBody("e2e-sess", now.Add(-time.Minute), 4242)))
	last := awaitSnapshot(t, broadcastPath, "the valid sample", func(s *watch.StateSnapshot) bool {
		w := windowByTask(s, "e2e-task")
		return w != nil && w.NativeMeasurement != nil && w.NativeMeasurement.Context != nil &&
			w.NativeMeasurement.Context.Tokens != nil
	})
	w := windowByTask(last, "e2e-task")
	if *w.NativeMeasurement.Context.Tokens != 4242 {
		t.Errorf("tokens = %d, want 4242 (the future-dated sample must not have pinned the session)", *w.NativeMeasurement.Context.Tokens)
	}
	if len(w.NativeMeasurement.Missing) != 0 {
		t.Errorf("missing = %v, want none for the valid sample", w.NativeMeasurement.Missing)
	}
	if len(last.Windows) != 1 {
		t.Errorf("windows = %+v, want only the tracked session: no sample may create one", last.Windows)
	}
	if w.Status != "running" {
		t.Errorf("status = %q, want running", w.Status)
	}
}

// -- snapshot size under hostile measurements ------------------------------------------------

// hostileBody renders a measurement whose rate_limits holds n entries with every
// field rejected, so each entry adds its paths to both the missing and invalid
// lists, plus maximum-length source and version labels. With n at the cap it is
// the largest measurement the parser stores; with a larger n it is a frame-filling
// body the parser must reject as one field.
func hostileBody(session string, measuredAt time.Time, n int) string {
	entry := `{"kind":1,"percent_used":"x","resets_at":"x"}`
	entries := strings.TrimSuffix(strings.Repeat(entry+",", n), ",")
	label := strings.Repeat("a", 64)
	return fmt.Sprintf(`{"session_id":%q,"measured_at":%q,"source":%q,"claude_version":%q,`+
		`"context":{"tokens":"x","window":"x","percent":"x"},"rate_limits":[%s]}`,
		session, rfc3339(measuredAt), label, label, entries)
}

// TestNativeMeasurement_MaxSizeMeasurementsAcrossManySessionsStayReadable pins
// the snapshot-blinding fix: a hostile writer can store a maximum-size
// measurement on every tracked session, and a real client must still read the
// broadcast snapshot (watch.Client caps one line at 64 KiB).
func TestNativeMeasurement_MaxSizeMeasurementsAcrossManySessionsStayReadable(t *testing.T) {
	const sessions = 20
	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	broadcastPath := filepath.Join(dir, "b.sock")
	srv, err := ipc.NewServer(broadcastPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Accept(ctx)

	d, clk := newMeasurementDaemon(t, &tmuxtest.MockClient{})
	d.ipc = srv
	for i := 0; i < sessions; i++ {
		track(d, fmt.Sprintf("s%02d", i), fmt.Sprintf("task-%02d", i))
	}
	// Alternate the largest storable measurement with a ~4 KiB oversized one.
	const oversizedEntries = 80
	for i := 0; i < sessions; i++ {
		n := measurement.MaxRateLimitEntries
		if i%2 == 1 {
			n = oversizedEntries
		}
		d.applyNativeMeasurement(envelope(hostileBody(fmt.Sprintf("s%02d", i), clk.now().Add(-time.Minute), n)))
	}

	snap := awaitSnapshot(t, broadcastPath, "every window's measurement", func(s *watch.StateSnapshot) bool {
		if len(s.Windows) != sessions {
			return false
		}
		for _, w := range s.Windows {
			if w.NativeMeasurement == nil {
				return false
			}
		}
		return true
	})
	capped, rejected := 0, 0
	for _, w := range snap.Windows {
		switch m := w.NativeMeasurement; {
		case len(m.Quota) == measurement.MaxRateLimitEntries:
			capped++
		case m.Quota == nil:
			rejected++
		default:
			t.Errorf("window %s: %d quota windows, want %d or none", w.TaskName, len(m.Quota), measurement.MaxRateLimitEntries)
		}
	}
	if capped != sessions/2 || rejected != sessions/2 {
		t.Errorf("%d windows at the cap and %d rejected, want %d of each", capped, rejected, sessions/2)
	}
	encoded, err := json.Marshal(d.buildSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("snapshot line is %d bytes for %d hostile sessions", len(encoded), sessions)
}
