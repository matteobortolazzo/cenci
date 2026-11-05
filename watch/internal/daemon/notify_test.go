package daemon

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/errcode"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/hookhealth"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// The four pre-existing tests below must isolate CENCI_SANDBOX (ambient in
// this dev container, per watch/docs/test-isolation.md #202) plus the
// socket-dir and drop-marker seams, or they silently stop exercising the
// host-branch code they claim to test.

func TestDeliverEventSucceedsWithoutRecovery(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "")
	useTempSocketDir(t)
	isolateDropMarkerPaths(t)

	event := ipc.HookEvent{EventType: "Stop", SessionID: "session-1"}
	var sent []ipc.HookEvent
	ensured := false

	deliverEvent(ipc.DefaultEventSocketPath(), event, func(_ string, got ipc.HookEvent) error {
		sent = append(sent, got)
		return nil
	}, func() { ensured = true })

	if ensured {
		t.Error("successful delivery must not ensure the daemon")
	}
	if !reflect.DeepEqual(sent, []ipc.HookEvent{event}) {
		t.Errorf("sent events = %#v, want original event once", sent)
	}
}

func TestDeliverEventRecoversAndRetriesSameEvent(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "")
	useTempSocketDir(t)
	isolateDropMarkerPaths(t)

	event := ipc.HookEvent{EventType: "PreToolUse", SessionID: "session-2", ToolName: "shell"}
	var sent []ipc.HookEvent
	ensured := 0

	deliverEvent(ipc.DefaultEventSocketPath(), event, func(_ string, got ipc.HookEvent) error {
		sent = append(sent, got)
		if len(sent) == 1 {
			return errors.New("daemon absent")
		}
		return nil
	}, func() { ensured++ })

	if ensured != 1 {
		t.Errorf("ensure calls = %d, want 1", ensured)
	}
	if !reflect.DeepEqual(sent, []ipc.HookEvent{event, event}) {
		t.Errorf("sent events = %#v, want exact event twice", sent)
	}
}

func TestDeliverEventFailedRecoveryRemainsNonFatal(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "")
	useTempSocketDir(t)
	isolateDropMarkerPaths(t)

	event := ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "session-3"}
	sends := 0
	ensured := 0

	deliverEvent(ipc.DefaultEventSocketPath(), event, func(string, ipc.HookEvent) error {
		sends++
		return errors.New("still unavailable")
	}, func() { ensured++ })

	if sends != 2 {
		t.Errorf("send attempts = %d, want 2", sends)
	}
	if ensured != 1 {
		t.Errorf("ensure calls = %d, want 1", ensured)
	}
}

func TestDeliverEventCustomSocketDoesNotStartDaemon(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "")
	useTempSocketDir(t)
	isolateDropMarkerPaths(t)

	event := ipc.HookEvent{EventType: "Stop", SessionID: "managed-instance"}
	sends := 0
	ensured := false

	deliverEvent("/tmp/custom-cenci.sock", event, func(string, ipc.HookEvent) error {
		sends++
		return errors.New("unavailable")
	}, func() { ensured = true })

	if ensured {
		t.Error("custom event socket must not start the default daemon")
	}
	if sends != 1 {
		t.Errorf("send attempts = %d, want only the initial attempt", sends)
	}
}

// -- sandbox-branch vs host-branch drop-marker coverage (#1122) ------------

// TestDeliverEventSandboxBranch_MarksMarkerAfterRetryExhausted asserts that
// inside a sandbox (CENCI_SANDBOX=1), a terminal delivery failure against the
// default event socket classifies the failure via mountInfoPath and writes
// an undelivered-events marker carrying the event's session id and the
// classified code.
func TestDeliverEventSandboxBranch_MarksMarkerAfterRetryExhausted(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "1")
	useTempSocketDir(t)
	marker, mountinfo := isolateDropMarkerPaths(t)

	// A healthy socket-dir mount, so the classifier attaches the
	// recoverable, host-scoped code.
	socketPath := ipc.DefaultEventSocketPath()
	socketDir := filepath.Dir(socketPath)
	content := "645 640 0:58 / " + socketDir + " rw,relatime shared:349 - tmpfs tmpfs rw\n"
	if err := os.WriteFile(mountinfo, []byte(content), 0o644); err != nil {
		t.Fatalf("write fake mountinfo: %v", err)
	}

	event := ipc.HookEvent{EventType: "Stop", SessionID: "sandbox-session-1"}
	sends := 0

	deliverEvent(socketPath, event, func(string, ipc.HookEvent) error {
		sends++
		return errors.New("event socket unreachable")
	}, func() {})

	if sends != 2 {
		t.Fatalf("send attempts = %d, want 2", sends)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("expected a marker to be written after the retry was exhausted, read err: %v", err)
	}
	m, err := ParseUndeliveredEventsMarker(data)
	if err != nil {
		t.Fatalf("parse marker: %v", err)
	}
	if m.SessionID != event.SessionID {
		t.Errorf("marker SessionID = %q, want %q", m.SessionID, event.SessionID)
	}
	if m.Code != errcode.DaemonSocketMissing {
		t.Errorf("marker Code = %q, want %q", m.Code, errcode.DaemonSocketMissing)
	}
}

// TestDeliverEventSandboxBranch_ClearsMarkerOnSuccess asserts a successful
// in-sandbox delivery clears any stale marker left by an earlier failure.
func TestDeliverEventSandboxBranch_ClearsMarkerOnSuccess(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "1")
	useTempSocketDir(t)
	marker, _ := isolateDropMarkerPaths(t)

	writeUndeliveredMarker("stale-session", errcode.DaemonSocketMissing, "stale failure from an earlier attempt")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("precondition: marker must exist before the successful delivery: %v", err)
	}

	socketPath := ipc.DefaultEventSocketPath()
	event := ipc.HookEvent{EventType: "Stop", SessionID: "sandbox-session-2"}

	deliverEvent(socketPath, event, func(string, ipc.HookEvent) error {
		return nil
	}, func() {})

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("expected the marker to be cleared after a successful delivery, stat err = %v", err)
	}
}

// TestDeliverEventHostBranch_NeverTouchesMountinfoOrMarker asserts the host
// branch (CENCI_SANDBOX unset) never enters the classify/write flow at all —
// not just that it happens to fail. mountInfoPath is pointed at a sentinel
// path that does not exist: if the host branch ever read it (a
// CENCI_SANDBOX-gate regression), the resulting "unreadable mountinfo"
// classification would still be a valid, writable marker — so the marker's
// absence is proof the gate held, not just that a read happened to fail
// silently.
func TestDeliverEventHostBranch_NeverTouchesMountinfoOrMarker(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "")
	useTempSocketDir(t)
	marker, _ := isolateDropMarkerPaths(t)
	mountInfoPath = filepath.Join(t.TempDir(), "does-not-exist")

	socketPath := ipc.DefaultEventSocketPath()
	event := ipc.HookEvent{EventType: "Stop", SessionID: "host-session-1"}
	sends := 0

	deliverEvent(socketPath, event, func(string, ipc.HookEvent) error {
		sends++
		return errors.New("daemon unreachable")
	}, func() {})

	if sends != 2 {
		t.Fatalf("send attempts = %d, want 2 (unchanged host retry behavior)", sends)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("host branch must never write an undelivered-events marker, stat err = %v", err)
	}
}

// TestDeliverEventSandboxBranch_UnwritableMarkerDirNeverBreaksDelivery
// exercises AC #5's non-fatality requirement end to end: an unreachable
// socket combined with an unwritable marker directory must not panic or
// otherwise break delivery. This is deliberately a package-internal test
// (not a black-box `cenci notify` subprocess test): the marker path is a
// hardcoded /home/dev/... constant with no env-var override, and this dev
// container's own HOME is /home/dev — a subprocess-level equivalent would
// risk writing to (or already having) a real file on the host outside test
// isolation. See watch/notify_test.go for the black-box host-branch
// regression coverage this ticket also requires.
func TestDeliverEventSandboxBranch_UnwritableMarkerDirNeverBreaksDelivery(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "1")
	useTempSocketDir(t)
	_, mountinfo := isolateDropMarkerPaths(t)
	if err := os.WriteFile(mountinfo, []byte(""), 0o644); err != nil {
		t.Fatalf("write fake mountinfo: %v", err)
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	markerPath = filepath.Join(dir, "marker")

	socketPath := ipc.DefaultEventSocketPath()
	event := ipc.HookEvent{EventType: "Stop", SessionID: "sandbox-session-3"}

	deliverEvent(socketPath, event, func(string, ipc.HookEvent) error {
		return errors.New("event socket unreachable")
	}, func() {})

	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("expected no marker file in an unwritable directory, stat err = %v", err)
	}
}

// -- hook-health diagnostics (#1239) ----------------------------------------
//
// These drive deliverEvent with the real ipc.SendEvent over real unix sockets
// and assert on what the read-only reporter sees in an isolated store.

// isolateHookHealthStore points the hook-health store at a fresh temp dir.
func isolateHookHealthStore(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func hookHealthReport() hookhealth.Report {
	return hookhealth.Read(hookhealth.Dir(), time.Now())
}

// seedHookHealthRecord writes a valid record aged by age, bypassing the
// writer's rate limits so recovery ordering is deterministic without sleeping.
func seedHookHealthRecord(t *testing.T, name, client string, age time.Duration) {
	t.Helper()
	dir := hookhealth.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create store: %v", err)
	}
	content := []byte(strconv.FormatInt(time.Now().Add(-age).Unix(), 10) + " " + client + "\n")
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

// listenShortSocket listens on a unix socket at path (pair with shortSocketPath
// to stay under sun_path) and closes it on cleanup.
func listenShortSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
}

// listenUnreachableSocket listens on path, then strips every permission bit so
// a dial fails with EACCES while the socket file still exists. It skips when
// the process can dial anyway (root, or CAP_DAC_OVERRIDE).
func listenUnreachableSocket(t *testing.T, path string) {
	t.Helper()
	listenShortSocket(t, path)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod socket: %v", err)
	}
	conn, err := net.Dial("unix", path)
	if err == nil {
		_ = conn.Close()
		t.Skip("socket file mode 000 is still dialable here (running as root?)")
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Skipf("mode-000 socket dial failed with %v, not EACCES", err)
	}
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "events.sock")
}

// TestHookHealthDeliverEventClassifiesTerminalFailures covers AC1/AC2's
// socket-unavailable vs delivery-failure split on both the default socket
// (recorded after its one on-demand retry) and a custom socket (recorded after
// its single attempt).
func TestHookHealthDeliverEventClassifiesTerminalFailures(t *testing.T) {
	cases := []struct {
		name        string
		client      string
		socket      func(t *testing.T) string // prepares the socket; returns its path
		wantEnsures int
		want        hookhealth.Category
	}{
		{"default socket without a listener", "claude", func(t *testing.T) string {
			return ipc.DefaultEventSocketPath()
		}, 1, hookhealth.SocketUnavailable},
		{"custom socket without a listener", "codex", func(t *testing.T) string {
			return shortSocketPath(t)
		}, 0, hookhealth.SocketUnavailable},
		{"empty socket path", "claude", func(t *testing.T) string {
			return ""
		}, 0, hookhealth.SocketUnavailable},
		{"default socket whose file denies access", "codex", func(t *testing.T) string {
			path := ipc.DefaultEventSocketPath()
			listenUnreachableSocket(t, path)
			return path
		}, 1, hookhealth.DeliveryFailure},
		{"custom socket whose file denies access", "claude", func(t *testing.T) string {
			path := shortSocketPath(t)
			listenUnreachableSocket(t, path)
			return path
		}, 0, hookhealth.DeliveryFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX", "")
			useTempSocketDir(t)
			isolateDropMarkerPaths(t)
			isolateHookHealthStore(t)
			socket := tc.socket(t)

			ensures := 0
			deliverEvent(socket, ipc.HookEvent{EventType: "Stop", SessionID: "hh-1", Agent: tc.client}, ipc.SendEvent, func() { ensures++ })

			if ensures != tc.wantEnsures {
				t.Errorf("ensure calls = %d, want %d (retry order must be unchanged)", ensures, tc.wantEnsures)
			}
			report := hookHealthReport()
			if report.State != hookhealth.StateCurrentFailure {
				t.Fatalf("state = %q, want current-failure", report.State)
			}
			if len(report.Failures) != 1 || report.Failures[0].Category != tc.want || report.Failures[0].Client != tc.client {
				t.Fatalf("failures = %+v, want exactly one %s for %s", report.Failures, tc.want, tc.client)
			}
		})
	}
}

// TestHookHealthDeliverEventSuccessWritesLastOK covers the success writer on
// both socket kinds.
func TestHookHealthDeliverEventSuccessWritesLastOK(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "default socket"
		if custom {
			name = "custom socket"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX", "")
			useTempSocketDir(t)
			isolateDropMarkerPaths(t)
			isolateHookHealthStore(t)
			socket := ipc.DefaultEventSocketPath()
			if custom {
				socket = shortSocketPath(t)
			}
			listenShortSocket(t, socket)

			deliverEvent(socket, ipc.HookEvent{EventType: "Stop", SessionID: "hh-2", Agent: "codex"}, ipc.SendEvent, func() {
				t.Error("a delivered event must not ensure the daemon")
			})

			report := hookHealthReport()
			if report.State != hookhealth.StateHealthy {
				t.Fatalf("state = %q, want healthy", report.State)
			}
			if report.LastOK == nil || report.LastOK.Client != "codex" {
				t.Fatalf("last-ok = %+v, want a codex record", report.LastOK)
			}
		})
	}
}

// TestHookHealthDeliverEventSuccessAfterFailureRecovers covers AC5: a delivery
// after an earlier failure makes the reporter say recovered, not current-failure.
func TestHookHealthDeliverEventSuccessAfterFailureRecovers(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "")
	useTempSocketDir(t)
	isolateDropMarkerPaths(t)
	isolateHookHealthStore(t)
	seedHookHealthRecord(t, string(hookhealth.SocketUnavailable), "claude", 2*time.Minute)
	if got := hookHealthReport().State; got != hookhealth.StateCurrentFailure {
		t.Fatalf("precondition: state = %q, want current-failure", got)
	}
	socket := shortSocketPath(t)
	listenShortSocket(t, socket)

	deliverEvent(socket, ipc.HookEvent{EventType: "Stop", SessionID: "hh-3", Agent: "claude"}, ipc.SendEvent, func() {})

	report := hookHealthReport()
	if report.State != hookhealth.StateRecovered {
		t.Fatalf("state = %q, want recovered", report.State)
	}
	if len(report.Failures) != 1 || report.Failures[0].Category != hookhealth.SocketUnavailable {
		t.Errorf("recovered failures = %+v, want the superseded socket-unavailable", report.Failures)
	}
}

// TestHookHealthSandboxBranchStillWritesMarkerAlongsideRecord covers AC8: the
// in-sandbox undelivered marker is written exactly as before, and the
// hook-health record is written next to it.
func TestHookHealthSandboxBranchStillWritesMarkerAlongsideRecord(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "1")
	useTempSocketDir(t)
	marker, mountinfo := isolateDropMarkerPaths(t)
	isolateHookHealthStore(t)
	socketPath := ipc.DefaultEventSocketPath()
	content := "645 640 0:58 / " + filepath.Dir(socketPath) + " rw,relatime shared:349 - tmpfs tmpfs rw\n"
	if err := os.WriteFile(mountinfo, []byte(content), 0o644); err != nil {
		t.Fatalf("write fake mountinfo: %v", err)
	}

	deliverEvent(socketPath, ipc.HookEvent{EventType: "Stop", SessionID: "hh-sandbox", Agent: "claude"}, ipc.SendEvent, func() {})

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("sandbox marker must still be written: %v", err)
	}
	m, err := ParseUndeliveredEventsMarker(data)
	if err != nil {
		t.Fatalf("parse marker: %v", err)
	}
	if m.SessionID != "hh-sandbox" || m.Code != errcode.DaemonSocketMissing {
		t.Errorf("marker = %+v, want session hh-sandbox with code %s", m, errcode.DaemonSocketMissing)
	}
	report := hookHealthReport()
	if report.State != hookhealth.StateCurrentFailure || len(report.Failures) != 1 || report.Failures[0].Category != hookhealth.SocketUnavailable {
		t.Errorf("report = %+v, want a socket-unavailable current failure inside the sandbox", report)
	}
}

// TestHookHealthDeliverEventIgnoresOtherAgents pins that only claude and codex
// ever record (OpenCode stays disabled, #1219; an unset agent is unknown).
func TestHookHealthDeliverEventIgnoresOtherAgents(t *testing.T) {
	for _, agent := range []string{"opencode", "", "someone-else"} {
		t.Run("agent="+agent, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX", "")
			useTempSocketDir(t)
			isolateDropMarkerPaths(t)
			isolateHookHealthStore(t)

			deliverEvent(ipc.DefaultEventSocketPath(), ipc.HookEvent{EventType: "Stop", SessionID: "hh-4", Agent: agent}, ipc.SendEvent, func() {})
			live := shortSocketPath(t)
			listenShortSocket(t, live)
			deliverEvent(live, ipc.HookEvent{EventType: "Stop", SessionID: "hh-4", Agent: agent}, ipc.SendEvent, func() {})

			if _, err := os.Lstat(hookhealth.Dir()); !os.IsNotExist(err) {
				t.Errorf("agent %q created a hook-health store (stat err = %v), want none", agent, err)
			}
		})
	}
}

// TestHookHealthClassifyTerminalErrors pins the pure error-to-category split
// with synthetic errors, so the ECONNREFUSED branch and the delivery-failure
// branches are covered without privileges or a real denied socket.
func TestHookHealthClassifyTerminalErrors(t *testing.T) {
	dial := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", errno)}
	}
	cases := []struct {
		name   string
		socket string
		err    error
		want   hookhealth.Category
	}{
		{"dial ENOENT", "/run/s.sock", dial(syscall.ENOENT), hookhealth.SocketUnavailable},
		{"dial ECONNREFUSED", "/run/s.sock", dial(syscall.ECONNREFUSED), hookhealth.SocketUnavailable},
		{"dial EACCES", "/run/s.sock", dial(syscall.EACCES), hookhealth.DeliveryFailure},
		{"dial EPERM", "/run/s.sock", dial(syscall.EPERM), hookhealth.DeliveryFailure},
		{"dial EAGAIN", "/run/s.sock", dial(syscall.EAGAIN), hookhealth.DeliveryFailure},
		{"write error", "/run/s.sock", io.ErrClosedPipe, hookhealth.DeliveryFailure},
		{"empty socket path", "", io.ErrClosedPipe, hookhealth.SocketUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyHookHealth(tc.socket, tc.err); got != tc.want {
				t.Errorf("classifyHookHealth(%q, %v) = %s, want %s", tc.socket, tc.err, got, tc.want)
			}
		})
	}
}

// TestHookHealthStaleSocketIsUnavailable covers the real ECONNREFUSED path: a
// socket file left behind by a closed listener refuses the dial, which is an
// unavailable socket, not a delivery failure.
func TestHookHealthStaleSocketIsUnavailable(t *testing.T) {
	t.Setenv("CENCI_SANDBOX", "")
	useTempSocketDir(t)
	isolateDropMarkerPaths(t)
	isolateHookHealthStore(t)
	socket := shortSocketPath(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen %s: %v", socket, err)
	}
	unixLn, ok := ln.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener is %T, want *net.UnixListener", ln)
	}
	unixLn.SetUnlinkOnClose(false)
	if err := unixLn.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("stale socket file must remain: %v", err)
	}

	deliverEvent(socket, ipc.HookEvent{EventType: "Stop", SessionID: "hh-stale", Agent: "claude"}, ipc.SendEvent, func() {})

	report := hookHealthReport()
	if report.State != hookhealth.StateCurrentFailure || len(report.Failures) != 1 || report.Failures[0].Category != hookhealth.SocketUnavailable {
		t.Fatalf("report = %+v, want a socket-unavailable current failure", report)
	}
}
