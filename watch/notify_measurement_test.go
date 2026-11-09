package main_test

// Real-binary tests for `cenci notify -agent claude -measurement` (#1451), the
// producer side of the native-measurement envelope #1450 ingests. The mods
// module (hooks/register.js, through hooks/measure.sh) writes one JSON object to
// stdin, {"cwd": <abs path>, "measurement": {...}}, and notify rebuilds the body
// from an allowlist, applies watch.excludeCwd, re-checks it with
// measurement.Parse and sends it without ever starting a daemon.
//
// Every test drives the built binary over stdin and a temp event socket
// (watch/docs/hook-events.md: JSON tags are proven through the real entry
// point). Non-delivery is proven the only way a fire-and-forget receiver allows:
// send the candidate, then a control sample, wait for the control and drain a
// short window (the pattern of watch.excludeCwd's tests in notify_test.go).
// Isolation follows watch/docs/test-isolation.md: XDG_CONFIG_HOME,
// XDG_STATE_HOME, CENCI_SOCKET_DIR and CENCI_SANDBOX never come from the
// ambient environment.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/measurement"
)

const (
	measureControlSession = "control-session"
	measureSource         = "session.measure"
	measureDrainWindow    = 200 * time.Millisecond
	// measureLineCap is the receiver's per-line limit (eventMaxBytes); notify
	// reads at most this much stdin.
	measureLineCap = 4096
	// measureCostUSD is a value that must never leave the mod's process: it is
	// searched for in every delivered body.
	measureCostUSD = "12.3456"
)

// measureFixture is a temp event socket with a running receiver and a state
// home, so a stray hook-health record would be visible.
type measureFixture struct {
	*notifyFixture
	stateHome string
}

// newMeasureFixture isolates the socket dir, the fleet config (no
// watch.excludeCwd), the state home and CENCI_SANDBOX for one test.
func newMeasureFixture(t *testing.T) *measureFixture {
	t.Helper()
	fx := newNotifyFixture(t) // isolates CENCI_SOCKET_DIR
	writeFleetConfig(t, "")   // isolates XDG_CONFIG_HOME, no config file
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("CENCI_SANDBOX", "")
	return &measureFixture{notifyFixture: fx, stateHome: stateHome}
}

// sampleMeasurement is a complete, valid body in the mod's snake_case shape.
func sampleMeasurement(session string) map[string]any {
	return map[string]any{
		"session_id":  session,
		"measured_at": time.Now().UTC().Format(time.RFC3339),
		"context": map[string]any{
			"tokens":  float64(1000),
			"window":  float64(200000),
			"percent": float64(0.5),
		},
		"rate_limits": []any{
			map[string]any{"kind": "five_hour", "percent_used": 23.5, "resets_at": "2026-11-08T15:00:00Z"},
			map[string]any{"kind": "seven_day", "percent_used": float64(7), "resets_at": "2026-11-12T09:30:00Z"},
		},
	}
}

// withSource is the body notify is expected to send: the mod's allowlisted body
// plus the constant source label notify owns.
func withSource(body map[string]any) map[string]any {
	out := make(map[string]any, len(body)+1)
	for k, v := range body {
		out[k] = v
	}
	out["source"] = measureSource
	return out
}

// measureStdin renders the mod-to-notify stdin contract; a nil cwd omits the key.
func measureStdin(t *testing.T, cwd *string, body any) string {
	t.Helper()
	payload := map[string]any{"measurement": body}
	if cwd != nil {
		payload["cwd"] = *cwd
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// measureRun runs `cenci notify -agent <agent> -measurement` with stdin. An empty
// socket omits -event-socket so the default socket is used. TMUX_PANE and
// CENCI_SANDBOX are cleared so the run is paneless and on the host. Every
// outcome of the mode is silent with exit 0, so a non-zero exit fails the test.
func measureRun(t *testing.T, socket, agent, stdin string, extraEnv ...string) (stdout, stderr string) {
	t.Helper()
	args := []string{"notify", "-agent", agent, "-measurement"}
	if socket != "" {
		args = append(args, "-event-socket", socket)
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(append(os.Environ(), "TMUX_PANE=", "CENCI_SANDBOX="), extraEnv...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("notify -measurement: %v (stdout: %q, stderr: %q)", err, outBuf.String(), errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}

func measureSessionOf(t *testing.T, raw []byte) string {
	t.Helper()
	var top struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("delivered body %s is not JSON: %v", raw, err)
	}
	return top.SessionID
}

// measureJSONEqual fails unless got and want decode to the same JSON value.
func measureJSONEqual(t *testing.T, got []byte, want any) {
	t.Helper()
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decode delivered body %s: %v", got, err)
	}
	if err := json.Unmarshal(wantBytes, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("delivered body = %s\nwant            = %s", got, wantBytes)
	}
}

// nextMeasurement waits for one delivered body of the given session.
func nextMeasurement(t *testing.T, fx *measureFixture, session string) []byte {
	t.Helper()
	select {
	case nm := <-fx.receiver.NativeMeasurements():
		if got := measureSessionOf(t, nm.Measurement); got != session {
			t.Fatalf("delivered session_id = %q, want %q (body %s)", got, session, nm.Measurement)
		}
		return nm.Measurement
	case <-time.After(2 * time.Second):
		t.Fatalf("no native measurement for session %q was delivered", session)
		return nil
	}
}

// collectMeasurements sends the control sample, waits for it, drains for a short
// window and returns every non-control body that arrived. Send order does not
// guarantee receive order (the receiver handles each connection in its own
// goroutine), so absence is asserted only after the control has arrived.
func collectMeasurements(t *testing.T, fx *measureFixture) [][]byte {
	t.Helper()
	measureRun(t, fx.socket, "claude", measureStdin(t, nil, sampleMeasurement(measureControlSession)))
	var got [][]byte
	timeout := time.After(2 * time.Second)
	for sawControl := false; !sawControl; {
		select {
		case nm := <-fx.receiver.NativeMeasurements():
			if measureSessionOf(t, nm.Measurement) == measureControlSession {
				sawControl = true
			} else {
				got = append(got, nm.Measurement)
			}
		case <-timeout:
			t.Fatal("control measurement never arrived")
		}
	}
	drain := time.After(measureDrainWindow)
	for {
		select {
		case nm := <-fx.receiver.NativeMeasurements():
			if measureSessionOf(t, nm.Measurement) != measureControlSession {
				got = append(got, nm.Measurement)
			}
		case <-drain:
			return got
		}
	}
}

// assertNothingDelivered fails if any non-control body reaches the receiver.
func assertNothingDelivered(t *testing.T, fx *measureFixture) {
	t.Helper()
	if got := collectMeasurements(t, fx); len(got) != 0 {
		t.Fatalf("delivered %d bodies, want none: %s", len(got), got)
	}
}

// assertNoHookHealthRecord fails if the run left anything under the state home:
// the hook-health categories are a closed set and none describes a measurement.
func assertNoHookHealthRecord(t *testing.T, fx *measureFixture) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fx.stateHome, "cenci")); !os.IsNotExist(err) {
		t.Errorf("state home has a cenci directory after a measurement run (stat err = %v), want no hook-health record", err)
	}
}

// TestNotifyMeasurementDeliversTheProjectedAllowlist: the delivered body equals
// the mod's allowlisted body plus the constant source; the cwd is compared with
// watch.excludeCwd and never forwarded; nothing reaches the hook-event path; the
// run is silent.
func TestNotifyMeasurementDeliversTheProjectedAllowlist(t *testing.T) {
	fx := newMeasureFixture(t)
	const cwd = "/home/someone/work/repo"
	body := sampleMeasurement("sess-1")

	stdout, stderr := measureRun(t, fx.socket, "claude", measureStdin(t, strPtr(cwd), body))

	if stdout != "" || stderr != "" {
		t.Errorf("stdout = %q, stderr = %q, want both empty", stdout, stderr)
	}
	got := nextMeasurement(t, fx, "sess-1")
	measureJSONEqual(t, got, withSource(body))
	if strings.Contains(string(got), cwd) {
		t.Errorf("the cwd leaked into the delivered body: %s", got)
	}

	// It is a real, parseable sample.
	sample, err := measurement.Parse(got, time.Now())
	if err != nil {
		t.Fatalf("measurement.Parse(delivered body): %v", err)
	}
	if sample.Snapshot.Source != measureSource {
		t.Errorf("source = %q, want %q", sample.Snapshot.Source, measureSource)
	}
	if len(sample.Snapshot.Invalid) != 0 {
		t.Errorf("invalid = %v, want none", sample.Snapshot.Invalid)
	}

	// It is lifecycle-neutral: it never takes the hook-event path.
	select {
	case ev := <-fx.receiver.Events():
		t.Fatalf("a measurement reached Events(): %+v", ev)
	case <-time.After(measureDrainWindow):
	}
	assertNoHookHealthRecord(t, fx)
}

// TestNotifyMeasurementNeverForwardsCostOrUnlistedFields (P46, second layer):
// even when the mod's stdin carries `cost` at the top level, inside `context`
// and inside a rate-limit entry, plus other unlisted keys, none of them reaches
// the socket, and a mod-supplied `source` is replaced by the constant.
func TestNotifyMeasurementNeverForwardsCostOrUnlistedFields(t *testing.T) {
	fx := newMeasureFixture(t)
	clean := sampleMeasurement("sess-leak")

	dirty := sampleMeasurement("sess-leak")
	dirty["measured_at"] = clean["measured_at"]
	dirty["cost"] = map[string]any{"usd": 12.3456}
	dirty["startedAt"] = float64(1700000000000)
	dirty["email"] = "dev@example.com"
	dirty["source"] = "attacker label"
	ctx := dirty["context"].(map[string]any)
	ctx["cost"] = map[string]any{"usd": 12.3456}
	ctx["breakdown"] = map[string]any{"gridRows": []any{"secret prompt row"}}
	limits := dirty["rate_limits"].([]any)
	first := limits[0].(map[string]any)
	first["cost"] = map[string]any{"usd": 12.3456}
	first["status"] = "allowed"

	measureRun(t, fx.socket, "claude", measureStdin(t, strPtr("/home/someone/work"), dirty))

	got := nextMeasurement(t, fx, "sess-leak")
	measureJSONEqual(t, got, withSource(clean))
	for _, forbidden := range []string{"cost", "usd", measureCostUSD, "startedAt", "email", "breakdown", "secret prompt row", "attacker", "status"} {
		if strings.Contains(string(got), forbidden) {
			t.Errorf("delivered body carries %q: %s", forbidden, got)
		}
	}

	// The shared validator's stored form carries no cost either (P46: not on the
	// wire and not in the snapshot).
	sample, err := measurement.Parse(got, time.Now())
	if err != nil {
		t.Fatalf("measurement.Parse(delivered body): %v", err)
	}
	stored, err := json.Marshal(sample.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"cost", "usd", measureCostUSD} {
		if strings.Contains(string(stored), forbidden) {
			t.Errorf("stored snapshot carries %q: %s", forbidden, stored)
		}
	}
}

// TestNotifyMeasurementDeliversFieldLevelInvalidValuesForTheDaemonToMark: only a
// whole-sample failure drops a sample. A bad single field is forwarded as the
// mod sent it, so the daemon's own Parse names it in `invalid`.
func TestNotifyMeasurementDeliversFieldLevelInvalidValuesForTheDaemonToMark(t *testing.T) {
	fx := newMeasureFixture(t)
	body := sampleMeasurement("sess-field")
	body["context"].(map[string]any)["tokens"] = float64(-5)
	body["rate_limits"].([]any)[0].(map[string]any)["resets_at"] = "tomorrow"

	measureRun(t, fx.socket, "claude", measureStdin(t, strPtr("/home/someone/work"), body))

	got := nextMeasurement(t, fx, "sess-field")
	measureJSONEqual(t, got, withSource(body))
	sample, err := measurement.Parse(got, time.Now())
	if err != nil {
		t.Fatalf("measurement.Parse(delivered body): %v", err)
	}
	wantInvalid := []string{"context.tokens", "rate_limits[0].resets_at"}
	if !reflect.DeepEqual(sample.Snapshot.Invalid, wantInvalid) {
		t.Errorf("invalid = %v, want %v", sample.Snapshot.Invalid, wantInvalid)
	}
}

// TestNotifyMeasurementExcludedCwdIsNotDelivered (P30): a measurement from a cwd
// listed in watch.excludeCwd, or below it at a segment boundary, is not
// delivered.
func TestNotifyMeasurementExcludedCwdIsNotDelivered(t *testing.T) {
	for _, cwd := range []string{"/excluded", "/excluded/repo/sub", "/excluded/"} {
		t.Run(cwd, func(t *testing.T) {
			fx := newMeasureFixture(t)
			writeFleetConfig(t, excludeConfig(t, "/excluded"))

			stdout, stderr := measureRun(t, fx.socket, "claude",
				measureStdin(t, strPtr(cwd), sampleMeasurement("sess-excluded")))

			if stdout != "" || stderr != "" {
				t.Errorf("stdout = %q, stderr = %q, want both empty", stdout, stderr)
			}
			assertNothingDelivered(t, fx)
			assertNoHookHealthRecord(t, fx)
		})
	}
}

// TestNotifyMeasurementNonExcludedCwdStaysVisible is the Critical Rule 1
// regression for P30: the exclusion is narrow. A non-listed cwd, a missing,
// empty or relative cwd, a sibling-prefix path and a missing or unusable config
// all stay delivered.
func TestNotifyMeasurementNonExcludedCwdStaysVisible(t *testing.T) {
	cases := []struct {
		name   string
		config string // empty: excludeConfig("/excluded")
		cwd    *string
	}{
		{"unmanaged cwd outside the list", "", strPtr("/home/someone/work")},
		{"no cwd key", "", nil},
		{"empty cwd", "", strPtr("")},
		{"relative cwd", "", strPtr("excluded/x")},
		{"sibling prefix /excludedfoo", "", strPtr("/excludedfoo")},
		{"below the sibling prefix", "", strPtr("/excludedfoo/probe")},
		{"parent of the listed path", "", strPtr("/")},
		{"dot-dot resolves out of the listed path", "", strPtr("/excluded/../home/x")},
		{"malformed config fails open", `{"watch":{"excludeCwd":["/excluded"]`, strPtr("/excluded/probe")},
		{"excludeCwd a string fails open", `{"watch":{"excludeCwd":"/excluded"}}`, strPtr("/excluded/probe")},
		{"bad element fails open", `{"watch":{"excludeCwd":["/excluded",5]}}`, strPtr("/excluded/probe")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newMeasureFixture(t)
			config := tc.config
			if config == "" {
				config = excludeConfig(t, "/excluded")
			}
			writeFleetConfig(t, config)
			body := sampleMeasurement("sess-visible")

			_, stderr := measureRun(t, fx.socket, "claude", measureStdin(t, tc.cwd, body))

			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			measureJSONEqual(t, nextMeasurement(t, fx, "sess-visible"), withSource(body))
		})
	}
}

// TestNotifyMeasurementExclusionIsPerSample: the decision is made per run, so a
// later sample of the same session from a non-listed cwd is delivered even
// though an earlier one was dropped.
func TestNotifyMeasurementExclusionIsPerSample(t *testing.T) {
	fx := newMeasureFixture(t)
	writeFleetConfig(t, excludeConfig(t, "/excluded"))
	dropped := sampleMeasurement("sess-per-sample")
	kept := sampleMeasurement("sess-per-sample")
	kept["context"].(map[string]any)["tokens"] = float64(2000)

	measureRun(t, fx.socket, "claude", measureStdin(t, strPtr("/excluded/probe"), dropped))
	measureRun(t, fx.socket, "claude", measureStdin(t, strPtr("/home/someone/work"), kept))

	got := collectMeasurements(t, fx)
	if len(got) != 1 {
		t.Fatalf("delivered %d bodies for the session, want exactly 1 (the kept sample): %s", len(got), got)
	}
	measureJSONEqual(t, got[0], withSource(kept))
}

// TestNotifyMeasurementDropsUnusableInputSilently: every unusable input ends in
// exit 0 with empty stdout and stderr, no hook-health record and nothing sent.
func TestNotifyMeasurementDropsUnusableInputSilently(t *testing.T) {
	valid := func(mutate func(map[string]any)) string {
		body := sampleMeasurement("sess-drop")
		mutate(body)
		return measureStdin(t, strPtr("/home/someone/work"), body)
	}
	cases := []struct {
		name  string
		agent string
		stdin string
	}{
		{"agent codex", "codex", valid(func(map[string]any) {})},
		{"agent opencode", "opencode", valid(func(map[string]any) {})},
		{"no agent", "", valid(func(map[string]any) {})},
		{"session_id with a space", "claude", valid(func(b map[string]any) { b["session_id"] = "bad id" })},
		{"session_id missing", "claude", valid(func(b map[string]any) { delete(b, "session_id") })},
		{"session_id not a string", "claude", valid(func(b map[string]any) { b["session_id"] = float64(5) })},
		{"measured_at missing", "claude", valid(func(b map[string]any) { delete(b, "measured_at") })},
		{"measured_at not RFC 3339", "claude", valid(func(b map[string]any) { b["measured_at"] = "yesterday" })},
		{"measured_at an hour ahead", "claude", valid(func(b map[string]any) {
			b["measured_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		})},
		{"measurement null", "claude", `{"cwd":"/home/someone/work","measurement":null}`},
		{"measurement a string", "claude", `{"cwd":"/home/someone/work","measurement":"text"}`},
		{"measurement an array", "claude", `{"cwd":"/home/someone/work","measurement":[1,2]}`},
		{"measurement missing", "claude", `{"cwd":"/home/someone/work"}`},
		{"stdin not JSON", "claude", `not json`},
		{"stdin empty", "claude", ``},
		{"stdin truncated", "claude", `{"cwd":"/home/someone/work","measurement":{"session_id":`},
		{"stdin over the 4 KiB line cap", "claude", valid(func(b map[string]any) { b["padding"] = strings.Repeat("x", measureLineCap) })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newMeasureFixture(t)

			stdout, stderr := measureRun(t, fx.socket, tc.agent, tc.stdin)

			if stdout != "" || stderr != "" {
				t.Errorf("stdout = %q, stderr = %q, want both empty", stdout, stderr)
			}
			assertNothingDelivered(t, fx)
			assertNoHookHealthRecord(t, fx)
		})
	}
}

// TestNotifyMeasurementAcceptsStdinAtTheLineCap pins the other side of the 4 KiB
// bound: a payload of exactly 4096 bytes is still read whole and delivered.
func TestNotifyMeasurementAcceptsStdinAtTheLineCap(t *testing.T) {
	fx := newMeasureFixture(t)
	body := sampleMeasurement("sess-cap")
	const cwd = "/home/someone/work"
	body["padding"] = ""
	empty := measureStdin(t, strPtr(cwd), body)
	body["padding"] = strings.Repeat("x", measureLineCap-len(empty))
	stdin := measureStdin(t, strPtr(cwd), body)
	if len(stdin) != measureLineCap {
		t.Fatalf("fixture is %d bytes, want exactly %d", len(stdin), measureLineCap)
	}

	measureRun(t, fx.socket, "claude", stdin)

	delete(body, "padding")
	measureJSONEqual(t, nextMeasurement(t, fx, "sess-cap"), withSource(body))
}

// TestNotifyMeasurementWithNoDaemonDoesNotAutostartOne (P31): with no daemon
// listening, delivery exits 0 silently and starts nothing: after the run neither
// the PID file nor the event socket exists. The socket dir is an isolated temp
// dir. No positive autostart control is run: it would launch a real daemon
// against the ambient tmux.
func TestNotifyMeasurementWithNoDaemonDoesNotAutostartOne(t *testing.T) {
	setTempSocketDir(t)
	writeFleetConfig(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CENCI_SANDBOX", "")
	socketDir := os.Getenv("CENCI_SOCKET_DIR")
	pidPath := ipc.DefaultPIDPath()
	eventSocket := ipc.DefaultEventSocketPath()
	// An empty path would make os.Stat("") fail vacuously, and an over-long one
	// falls back to a shared tmp dir; either would void the absence assertions.
	for _, path := range []string{pidPath, eventSocket} {
		if path == "" || socketDir == "" || !strings.HasPrefix(path, socketDir+string(filepath.Separator)) {
			t.Fatalf("path %q is not inside the isolated socket dir %q", path, socketDir)
		}
	}
	// A regression that spawns a daemon must not leak it past the test.
	t.Cleanup(func() {
		_, pidErr := os.Stat(pidPath)
		_, sockErr := os.Stat(eventSocket)
		if pidErr != nil && sockErr != nil {
			return
		}
		if out, err := exec.Command(binaryPath, "daemon", "stop").CombinedOutput(); err != nil {
			t.Errorf("cleanup: daemon stop failed: %v: %s", err, out)
		}
	})

	stdout, stderr := measureRun(t, "", "claude",
		measureStdin(t, strPtr("/home/someone/work"), sampleMeasurement("sess-no-daemon")), "TMUX=")

	if stdout != "" || stderr != "" {
		t.Errorf("stdout = %q, stderr = %q, want both empty", stdout, stderr)
	}
	for _, path := range []string{pidPath, eventSocket} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s exists after a measurement with no daemon (stat err = %v), want no daemon autostart", path, err)
		}
	}
}

// TestNotifyMeasurementDefaultSocketReceivesTheSample is the positive control for
// P31: with no -event-socket, a receiver on the default socket path does get the
// sample, so the no-autostart test above exercises the same path.
func TestNotifyMeasurementDefaultSocketReceivesTheSample(t *testing.T) {
	setTempSocketDir(t)
	writeFleetConfig(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CENCI_SANDBOX", "")
	path := ipc.DefaultEventSocketPath()
	socketDir := os.Getenv("CENCI_SOCKET_DIR")
	if path == "" || !strings.HasPrefix(path, socketDir+string(filepath.Separator)) {
		t.Fatalf("path %q is not inside the isolated socket dir %q", path, socketDir)
	}
	receiver, err := ipc.NewEventReceiver(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go receiver.Accept(ctx)
	body := sampleMeasurement("sess-default-socket")

	measureRun(t, "", "claude", measureStdin(t, strPtr("/home/someone/work"), body), "TMUX=")

	select {
	case nm := <-receiver.NativeMeasurements():
		measureJSONEqual(t, nm.Measurement, withSource(body))
	case <-time.After(2 * time.Second):
		t.Fatal("the sample never reached the receiver on the default socket")
	}
}
