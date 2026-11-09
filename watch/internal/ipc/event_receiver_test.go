package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestEventReceiver_ValidEvent(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	event := HookEvent{
		EventType:        "Stop",
		SessionID:        "sess-123",
		TmuxPane:         "%5",
		NotificationType: "info",
		ToolName:         "Write",
		Timestamp:        "2024-01-01T00:00:00Z",
	}
	if err := SendEvent(path, event); err != nil {
		t.Fatalf("SendEvent: %v", err)
	}

	select {
	case got := <-recv.Events():
		if got.EventType != "Stop" {
			t.Errorf("EventType: want %q, got %q", "Stop", got.EventType)
		}
		if got.SessionID != "sess-123" {
			t.Errorf("SessionID: want %q, got %q", "sess-123", got.SessionID)
		}
		if got.TmuxPane != "%5" {
			t.Errorf("TmuxPane: want %q, got %q", "%5", got.TmuxPane)
		}
		if got.NotificationType != "info" {
			t.Errorf("NotificationType: want %q, got %q", "info", got.NotificationType)
		}
		if got.ToolName != "Write" {
			t.Errorf("ToolName: want %q, got %q", "Write", got.ToolName)
		}
		if got.Timestamp != "2024-01-01T00:00:00Z" {
			t.Errorf("Timestamp: want %q, got %q", "2024-01-01T00:00:00Z", got.Timestamp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventReceiver_OversizedPayloadRejected(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	// Build a JSON line that exceeds 4096 bytes but stays under the default
	// 64 KiB scanner limit. A ~5000-character event_type field produces a
	// payload of roughly 5100 bytes total.
	bigField := strings.Repeat("X", 5000)
	payload := fmt.Sprintf(
		`{"event_type":"%s","session_id":"s","tmux_pane":"%%0","timestamp":"t"}`,
		bigField,
	)
	payload += "\n"

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.Close()

	// The oversized event must NOT appear on the channel.
	select {
	case evt := <-recv.Events():
		t.Fatalf("expected no event, but got one with EventType length %d", len(evt.EventType))
	case <-time.After(200 * time.Millisecond):
		// Good -- no event received.
	}
}

func TestEventReceiver_InvalidJSON(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	// Send malformed JSON.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte("{not valid json}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.Close()

	// Invalid JSON must NOT appear on the channel.
	select {
	case evt := <-recv.Events():
		t.Fatalf("expected no event, but got: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Good -- no event received.
	}
}

func TestEventReceiver_ConnectionLimitRejectsExcess(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	// Open maxEventConns raw connections that don't send data.
	// Each blocks in handleConn waiting for data until the read deadline.
	// This fills the connection semaphore.
	blockers := make([]net.Conn, maxEventConns)
	for i := 0; i < maxEventConns; i++ {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("blocking dial %d: %v", i, err)
		}
		blockers[i] = conn
	}
	defer func() {
		for _, c := range blockers {
			_ = c.Close()
		}
	}()

	// Allow all blocking connections to be accepted and acquire semaphore slots.
	time.Sleep(50 * time.Millisecond)

	// Now try to send a real event. With the semaphore full, the server should
	// reject or close this connection before processing the event.
	event := HookEvent{
		EventType: "PostToolUse",
		SessionID: "sess-excess",
		TmuxPane:  "%99",
		Timestamp: "2024-01-01T00:00:01Z",
	}
	// SendEvent may or may not return an error depending on implementation.
	// The key assertion is that the event must NOT appear on the channel.
	_ = SendEvent(path, event)

	select {
	case got := <-recv.Events():
		t.Fatalf("expected no event when at connection limit, but received: %+v", got)
	case <-time.After(500 * time.Millisecond):
		// No event received within timeout -- connection was correctly rejected.
	}
}

func TestEventReceiver_ConnectionLimitAllowsAfterRelease(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	// Fill all semaphore slots with blocking connections (no data sent).
	blockers := make([]net.Conn, maxEventConns)
	for i := 0; i < maxEventConns; i++ {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("blocking dial %d: %v", i, err)
		}
		blockers[i] = conn
	}

	// Allow all blocking connections to be accepted.
	time.Sleep(50 * time.Millisecond)

	// Verify the semaphore is full by confirming a send is rejected.
	_ = SendEvent(path, HookEvent{
		EventType: "PreToolUse",
		SessionID: "sess-rejected",
		TmuxPane:  "%1",
		Timestamp: "2024-01-01T00:00:00Z",
	})

	select {
	case <-recv.Events():
		t.Fatal("expected event to be rejected while at capacity")
	case <-time.After(200 * time.Millisecond):
		// Confirmed: at capacity, events are rejected.
	}

	// Release all blocking connections.
	for _, c := range blockers {
		_ = c.Close()
	}

	// Allow handlers to exit and release semaphore slots.
	time.Sleep(50 * time.Millisecond)

	// Now send one more event -- it should be accepted since all slots are free.
	event := HookEvent{
		EventType: "PostToolUse",
		SessionID: "sess-after",
		TmuxPane:  "%2",
		ToolName:  "Read",
		Timestamp: "2024-01-01T00:00:01Z",
	}
	if err := SendEvent(path, event); err != nil {
		t.Fatalf("SendEvent after release: %v", err)
	}

	select {
	case got := <-recv.Events():
		if got.SessionID != "sess-after" {
			t.Errorf("SessionID = %q, want %q", got.SessionID, "sess-after")
		}
		if got.EventType != "PostToolUse" {
			t.Errorf("EventType = %q, want %q", got.EventType, "PostToolUse")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event after semaphore release")
	}
}

// -- pending-close routing (#522) --------------------------------------------

// TestSendPendingClose_RoundTripsToPendingCloses asserts SendPendingClose's
// message round-trips to the new PendingCloses() channel, and does NOT land
// on the existing Events() channel (the two message kinds route separately).
func TestSendPendingClose_RoundTripsToPendingCloses(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	pc := PendingClose{PaneID: "%7", Session: "main", WindowIndex: "0", WindowName: "42-implement"}
	if err := SendPendingClose(path, pc); err != nil {
		t.Fatalf("SendPendingClose: %v", err)
	}

	select {
	case got := <-recv.PendingCloses():
		if got != pc {
			t.Errorf("PendingCloses() = %+v, want %+v", got, pc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending-close")
	}

	select {
	case evt := <-recv.Events():
		t.Fatalf("expected no hook event from a pending-close message, but got: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Good -- pending-close messages never land on Events().
	}
}

// TestEventReceiver_HookEventWithoutKindRoutesToEvents asserts backward
// compatibility: a plain hook-event line (no "kind" discriminator field, as
// every pre-#522 cenci notify caller sends) still routes to Events() and
// never to the new PendingCloses() channel.
func TestEventReceiver_HookEventWithoutKindRoutesToEvents(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	event := HookEvent{
		EventType: "Stop",
		SessionID: "sess-no-kind",
		TmuxPane:  "%0",
		Timestamp: "2024-01-01T00:00:00Z",
	}
	if err := SendEvent(path, event); err != nil {
		t.Fatalf("SendEvent: %v", err)
	}

	select {
	case got := <-recv.Events():
		if got.EventType != "Stop" || got.SessionID != "sess-no-kind" {
			t.Errorf("Events() = %+v, want EventType=Stop SessionID=sess-no-kind", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hook event")
	}

	select {
	case pc := <-recv.PendingCloses():
		t.Fatalf("expected no pending-close from a plain hook-event message, but got: %+v", pc)
	case <-time.After(200 * time.Millisecond):
		// Good -- hook events never land on PendingCloses().
	}

	// #1094 AC2 regression: an *unknown* kind (not just an absent one, and
	// not "pending-close" or "babysit-arm") must also still route to
	// Events() unchanged -- the fallback is "every other kind", not just
	// "no kind at all".
	unknownKindLine := `{"kind":"some-future-kind","event_type":"Stop","session_id":"sess-unknown-kind","tmux_pane":"%9","timestamp":"2024-01-01T00:00:00Z"}` + "\n"
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte(unknownKindLine)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.Close()

	select {
	case got := <-recv.Events():
		if got.EventType != "Stop" || got.SessionID != "sess-unknown-kind" {
			t.Errorf("Events() = %+v, want EventType=Stop SessionID=sess-unknown-kind for an unrecognized kind", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the unknown-kind hook event")
	}

	select {
	case pc := <-recv.PendingCloses():
		t.Fatalf("expected no pending-close from an unrecognized-kind message, but got: %+v", pc)
	case <-time.After(200 * time.Millisecond):
		// Good -- an unrecognized kind never lands on PendingCloses() either.
	}
}

// -- pending-close socket (#1430) ---------------------------------------------

// TestEventReceiver_PendingCloseSocketDecodesFromTheRawLine pins the wire key:
// a raw pending-close line (the way a container can write one) carrying
// `socket` decodes into PendingClose.Socket, and a line without the key -- an
// older `cenci close` -- decodes to the empty socket.
func TestEventReceiver_PendingCloseSocketDecodesFromTheRawLine(t *testing.T) {
	const socket = "/tmp/tmux-1000/cenci"
	tests := []struct {
		name string
		line string
		want PendingClose
	}{
		{
			name: "with a socket key",
			line: `{"kind":"pending-close","pane_id":"%5","session":"main","window_index":"2","window_name":"42-implement","socket":"` + socket + `"}`,
			want: PendingClose{PaneID: "%5", Session: "main", WindowIndex: "2", WindowName: "42-implement", Socket: socket},
		},
		{
			name: "without a socket key",
			line: `{"kind":"pending-close","pane_id":"%5","session":"main","window_index":"2","window_name":"42-implement"}`,
			want: PendingClose{PaneID: "%5", Session: "main", WindowIndex: "2", WindowName: "42-implement"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recv, path := startReceiver(t)

			writeRawLine(t, path, tt.line)

			select {
			case got := <-recv.PendingCloses():
				if got != tt.want {
					t.Errorf("PendingCloses() = %+v, want %+v", got, tt.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for pending-close")
			}
		})
	}
}

// TestSendPendingClose_CarriesSocketAndOmitsItWhenEmpty asserts the sender's
// wire line has a `socket` key only when one is recorded, so a window with no
// recorded socket sends the exact pre-#1430 line.
func TestSendPendingClose_CarriesSocketAndOmitsItWhenEmpty(t *testing.T) {
	const socket = "/tmp/tmux-1000/cenci"
	for _, tt := range []struct {
		name       string
		pc         PendingClose
		wantSocket bool
	}{
		{"recorded socket", PendingClose{PaneID: "%5", Session: "main", WindowIndex: "2", WindowName: "w", Socket: socket}, true},
		{"empty socket", PendingClose{PaneID: "%5", Session: "main", WindowIndex: "2", WindowName: "w"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := tempSocket(t)
			ln, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			lines := make(chan string, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				line, _ := bufio.NewReader(conn).ReadString('\n')
				lines <- line
			}()

			if err := SendPendingClose(path, tt.pc); err != nil {
				t.Fatalf("SendPendingClose: %v", err)
			}

			select {
			case line := <-lines:
				var keys map[string]any
				if err := json.Unmarshal([]byte(line), &keys); err != nil {
					t.Fatalf("decode %q: %v", line, err)
				}
				got, present := keys["socket"]
				if present != tt.wantSocket || (present && got != socket) {
					t.Errorf("socket key = %v (present %v) in %s, want present=%v", got, present, line, tt.wantSocket)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for the line")
			}
		})
	}
}

// -- native-measurement routing (#1450) ---------------------------------------

func startReceiver(t *testing.T) (*EventReceiver, string) {
	t.Helper()
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go recv.Accept(ctx)
	return recv, path
}

// writeRawLine writes bytes straight to the event socket, bypassing every
// sender, the way a container (or a buggy mod) can.
func writeRawLine(t *testing.T, path, line string) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func sameJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decode got %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("decode want %s: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("body = %s, want %s", got, want)
	}
}

const measurementBody = `{"session_id":"sess-1","measured_at":"2026-11-08T11:59:00Z","source":"claude-mods","context":{"tokens":1000,"window":200000,"percent":0.5}}`

// TestSendNativeMeasurement_RoundTripsToNativeMeasurements asserts the envelope
// lands on its own channel, body intact, and on neither Events() nor
// PendingCloses().
func TestSendNativeMeasurement_RoundTripsToNativeMeasurements(t *testing.T) {
	recv, path := startReceiver(t)

	if err := SendNativeMeasurement(path, NativeMeasurement{Measurement: json.RawMessage(measurementBody)}); err != nil {
		t.Fatalf("SendNativeMeasurement: %v", err)
	}

	select {
	case got := <-recv.NativeMeasurements():
		sameJSON(t, got.Measurement, []byte(measurementBody))
	case evt := <-recv.Events():
		t.Fatalf("a native measurement must not reach Events(), got %+v", evt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the native measurement")
	}

	select {
	case evt := <-recv.Events():
		t.Fatalf("unexpected hook event: %+v", evt)
	case pc := <-recv.PendingCloses():
		t.Fatalf("unexpected pending close: %+v", pc)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestSendNativeMeasurement_WireShapeDecodesAsAnEmptyHookEvent pins P27: the
// line's top-level keys are exactly kind and measurement, so a pre-change
// daemon decodes it as a HookEvent with empty session_id and tmux_pane and
// drops it.
func TestSendNativeMeasurement_WireShapeDecodesAsAnEmptyHookEvent(t *testing.T) {
	path := tempSocket(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	lines := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		lines <- line
	}()

	if err := SendNativeMeasurement(path, NativeMeasurement{Measurement: json.RawMessage(measurementBody)}); err != nil {
		t.Fatalf("SendNativeMeasurement: %v", err)
	}

	var line string
	select {
	case line = <-lines:
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was written to the socket")
	}
	if !strings.HasSuffix(line, "\n") {
		t.Errorf("line %q is not newline-terminated", line)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &top); err != nil {
		t.Fatalf("line %q is not a JSON object: %v", line, err)
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"kind", "measurement"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("top-level keys = %v, want %v", keys, want)
	}
	var kind string
	if err := json.Unmarshal(top["kind"], &kind); err != nil || kind != "native-measurement" {
		t.Errorf("kind = %q (err %v), want the literal native-measurement", kind, err)
	}
	sameJSON(t, top["measurement"], []byte(measurementBody))

	var legacy HookEvent
	if err := json.Unmarshal([]byte(line), &legacy); err != nil {
		t.Fatalf("a pre-change daemon could not decode the line as a HookEvent: %v", err)
	}
	if legacy.SessionID != "" || legacy.TmuxPane != "" || legacy.EventType != "" {
		t.Errorf("HookEvent = %+v, want empty session_id, tmux_pane and event_type", legacy)
	}
}

// TestEventReceiver_NativeMeasurementWithoutAnObjectBodyIsDropped asserts a
// malformed envelope never reaches Events() or the measurement channel, and
// that the receiver keeps serving afterwards. A body that is an object but
// carries invalid fields is delivered as-is: per-field validation belongs to
// the daemon, so one bad field cannot fail the whole decode.
func TestEventReceiver_NativeMeasurementWithoutAnObjectBodyIsDropped(t *testing.T) {
	recv, path := startReceiver(t)

	for _, line := range []string{
		`{"kind":"native-measurement"}`,
		`{"kind":"native-measurement","measurement":null}`,
		`{"kind":"native-measurement","measurement":"text"}`,
		`{"kind":"native-measurement","measurement":5}`,
		`{"kind":"native-measurement","measurement":true}`,
		`{"kind":"native-measurement","measurement":[1,2]}`,
		`{"kind":"native-measurement","measurement":{"session_id":`,
	} {
		writeRawLine(t, path, line)
	}
	const fieldInvalid = `{"session_id":5,"context":"abc","rate_limits":"x"}`
	writeRawLine(t, path, `{"kind":"native-measurement","measurement":`+fieldInvalid+`}`)

	select {
	case got := <-recv.NativeMeasurements():
		sameJSON(t, got.Measurement, []byte(fieldInvalid))
	case <-time.After(2 * time.Second):
		t.Fatal("the object body with invalid fields must still be delivered")
	}

	select {
	case got := <-recv.NativeMeasurements():
		t.Fatalf("a malformed envelope was delivered: %s", got.Measurement)
	case evt := <-recv.Events():
		t.Fatalf("a malformed native-measurement reached Events(): %+v", evt)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestEventReceiver_ContextCancelStopsAccept(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		recv.Accept(ctx)
		close(done)
	}()

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	cancel()

	select {
	case <-done:
		// Accept returned after context cancellation.
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not return after context cancellation")
	}
}

// receiveRawLine serves a receiver on a fresh socket, writes line to it
// verbatim (no struct marshalling on the sending side) and returns the one
// HookEvent the receiver decoded from it.
func receiveRawLine(t *testing.T, line string) HookEvent {
	t.Helper()
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.Close()

	select {
	case got := <-recv.Events():
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the hook event")
		return HookEvent{}
	}
}

// #1429: the receiver decodes the wire key "tmux_socket" into
// HookEvent.TmuxSocket, from a raw line (the shape any in-container process can
// write) and from what SendEvent marshals.
func TestEventReceiver_DecodesTmuxSocket(t *testing.T) {
	const socket = "/run/user/1000/tmux-1000/default"

	t.Run("raw line", func(t *testing.T) {
		got := receiveRawLine(t, `{"event_type":"Stop","session_id":"sess-sock","tmux_pane":"%5","tmux_socket":"`+socket+`","timestamp":"2024-01-01T00:00:00Z"}`)
		if got.TmuxSocket != socket {
			t.Errorf("TmuxSocket = %q, want %q", got.TmuxSocket, socket)
		}
		if got.TmuxPane != "%5" || got.SessionID != "sess-sock" {
			t.Errorf("event = %+v, want the neighbouring fields decoded too", got)
		}
	})

	t.Run("SendEvent round trip", func(t *testing.T) {
		path := tempSocket(t)
		recv, err := NewEventReceiver(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = recv.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go recv.Accept(ctx)
		time.Sleep(10 * time.Millisecond)

		if err := SendEvent(path, HookEvent{EventType: "Stop", SessionID: "sess-rt", TmuxPane: "%5", TmuxSocket: socket}); err != nil {
			t.Fatalf("SendEvent: %v", err)
		}
		select {
		case got := <-recv.Events():
			if got.TmuxSocket != socket {
				t.Errorf("TmuxSocket = %q, want %q after a SendEvent round trip", got.TmuxSocket, socket)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for the hook event")
		}
	})
}

// #1429: a line from an older `cenci notify`, with no "tmux_socket" key,
// decodes field for field as it did before, with an empty TmuxSocket.
func TestEventReceiver_HookEventWithoutTmuxSocketDecodesAsBefore(t *testing.T) {
	got := receiveRawLine(t, `{"event_type":"PreToolUse","session_id":"sess-old","agent":"claude","tmux_pane":"%2","tool_name":"Write","timestamp":"2024-01-01T00:00:00Z"}`)

	want := HookEvent{
		EventType: "PreToolUse",
		SessionID: "sess-old",
		Agent:     "claude",
		TmuxPane:  "%2",
		ToolName:  "Write",
		Timestamp: "2024-01-01T00:00:00Z",
	}
	if got != want {
		t.Errorf("decoded event = %+v, want %+v", got, want)
	}
}
