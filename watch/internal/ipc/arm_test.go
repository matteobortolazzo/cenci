package ipc

// Ticket #1094: the babysit-arm request/response transport on the event
// socket. These tests drive a real Unix socket end to end -- SetArmHandler,
// handleConn's arm routing branch, the response write-then-close contract,
// and SendArmRequest's own sentinel classification -- per the plan's chosen
// alternative (a synchronous injectable handler, not a PendingCloses-style
// channel). None of ArmRequest/ArmResponse/SetArmHandler/SendArmRequest/
// ErrArmUndelivered/ErrArmNoResponse exist yet; this file is expected to
// fail to compile until Phase 4 adds them.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func validArmRequest() ArmRequest {
	return ArmRequest{PR: "42", Repo: "o/r", Agent: "claude", Interval: 15 * time.Minute, TmuxPane: "%3"}
}

// TestEventReceiver_ArmRequestRoutesToHandlerNotEvents covers the routing
// half of the ticket's Decisions: a babysit-arm request reaches the
// synchronous injected handler with the exact fields the client sent, the
// handler's ack round-trips back through SendArmRequest, and the request
// never lands on the pre-existing Events() channel.
func TestEventReceiver_ArmRequestRoutesToHandlerNotEvents(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	var mu sync.Mutex
	var got ArmRequest
	var calls int
	recv.SetArmHandler(func(req ArmRequest) ArmResponse {
		mu.Lock()
		got = req
		calls++
		mu.Unlock()
		return ArmResponse{OK: true}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)

	want := validArmRequest()
	resp, err := SendArmRequest(path, want)
	if err != nil {
		t.Fatalf("SendArmRequest: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK = false, want true for an ack, resp=%+v", resp)
	}

	mu.Lock()
	gotCalls := calls
	gotReq := got
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("arm handler invocations = %d, want exactly 1", gotCalls)
	}
	if gotReq != want {
		t.Errorf("arm handler received %+v, want %+v", gotReq, want)
	}

	select {
	case evt := <-recv.Events():
		t.Fatalf("expected no hook event from an arm-request message, but got: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Good -- arm requests never land on Events().
	}
}

// TestEventReceiver_ArmResponseExactlyOneLineThenConnCloses covers the
// ticket's write-then-close contract: the daemon writes exactly one
// ack-or-nack JSON line on the same connection and then closes it -- never a
// second line, never left open.
func TestEventReceiver_ArmResponseExactlyOneLineThenConnCloses(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	recv.SetArmHandler(func(ArmRequest) ArmResponse { return ArmResponse{OK: true} })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	line := `{"kind":"babysit-arm","pr":"42","repo":"o/r","agent":"claude"}` + "\n"
	if _, err := conn.Write([]byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}

	reader := bufio.NewReader(conn)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read response line: %v", err)
	}
	var resp ArmResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &resp); err != nil {
		t.Fatalf("unmarshal response line %q: %v", first, err)
	}
	if !resp.OK {
		t.Errorf("resp.OK = false, want true, resp=%+v", resp)
	}

	// A second read past the one response line must observe the server
	// having closed the connection (EOF), not a second line and not a hang.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != io.EOF {
		t.Fatalf("read after the response line: n=%d err=%v, want io.EOF (server closes after exactly one line)", n, err)
	}
}

// TestEventReceiver_NilArmHandlerNacks covers the fail-closed contract for
// an arm request arriving before SetArmHandler was ever called: the receiver
// itself must nack rather than silently drop the connection or fall through
// to Events().
func TestEventReceiver_NilArmHandlerNacks(t *testing.T) {
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

	resp, err := SendArmRequest(path, validArmRequest())
	if err != nil {
		t.Fatalf("SendArmRequest: %v", err)
	}
	if resp.OK {
		t.Fatal("resp.OK = true, want a nack when no arm handler was ever installed (fail closed)")
	}
	if resp.Reason == "" {
		t.Error("resp.Reason is empty, want a non-empty stable reason on a nil-handler nack")
	}
}

// TestEventReceiver_MalformedArmRequestBodyNacks covers the second half of
// the fail-closed contract: the envelope's "kind" is recognized as
// babysit-arm, but the body fails to unmarshal into ArmRequest (a type
// mismatch on "pr", mirroring babysit.State's own `json:"pr"` tag
// convention). The handler must never be invoked, and the connection must
// still get a nack response rather than a silent drop.
func TestEventReceiver_MalformedArmRequestBodyNacks(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	var mu sync.Mutex
	called := false
	recv.SetArmHandler(func(ArmRequest) ArmResponse {
		mu.Lock()
		called = true
		mu.Unlock()
		return ArmResponse{OK: true}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// "pr" carries a number where ArmRequest's PR field is a string --
	// guaranteed to fail json.Unmarshal regardless of any other field's tag.
	line := `{"kind":"babysit-arm","pr":12345,"repo":"o/r","agent":"claude"}` + "\n"
	if _, err := conn.Write([]byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}

	reader := bufio.NewReader(conn)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read response line: %v", err)
	}
	var resp ArmResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &resp); err != nil {
		t.Fatalf("unmarshal response line %q: %v", first, err)
	}
	if resp.OK {
		t.Errorf("resp.OK = true, want a nack for a request body that fails to unmarshal")
	}

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Error("arm handler was invoked for a request that failed to unmarshal, want it never called")
	}
}

// TestSendArmRequest_OversizedRequestClassifiesAsNoResponse covers the
// client-side sentinel classification for a request line that exceeds the
// shared eventMaxBytes frame: the daemon's scanner rejects the line outright
// (mirroring TestEventReceiver_OversizedPayloadRejected) and never responds,
// so SendArmRequest must classify this as ErrArmNoResponse -- the daemon
// wrote nothing back, but something *was* written to the wire, so this is
// not a dial failure.
func TestSendArmRequest_OversizedRequestClassifiesAsNoResponse(t *testing.T) {
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Close() }()

	recv.SetArmHandler(func(ArmRequest) ArmResponse { return ArmResponse{OK: true} })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)

	huge := strings.Repeat("X", 5000)
	req := validArmRequest()
	req.Repo = huge

	_, err = SendArmRequest(path, req)
	if !errors.Is(err, ErrArmNoResponse) {
		t.Fatalf("SendArmRequest err = %v, want errors.Is(err, ErrArmNoResponse)", err)
	}
}

// TestSendArmRequest_DialFailureClassifiesAsUndelivered covers the other
// client-side sentinel: nothing was ever written to a socket with no
// listener, so SendArmRequest must classify this as ErrArmUndelivered, not
// ErrArmNoResponse -- the auto-adopted answer #8 distinction the babysit
// client relies on to map dial failure to "not armed" rather than "arm
// status unknown".
func TestSendArmRequest_DialFailureClassifiesAsUndelivered(t *testing.T) {
	path := tempSocket(t) // nothing is listening on this path

	_, err := SendArmRequest(path, validArmRequest())
	if !errors.Is(err, ErrArmUndelivered) {
		t.Fatalf("SendArmRequest err = %v, want errors.Is(err, ErrArmUndelivered)", err)
	}
}

// -- #1390: babysit-stop / babysit-status control requests -------------------
//
// The two kinds reuse the babysit-arm envelope: one request line, exactly one
// reply line, then the daemon closes. Each case below runs for both kinds so a
// divergence between the stop and status paths cannot hide.

// controlCase adapts one control kind to a common shape for the table tests.
type controlCase struct {
	name string
	kind string
	// install registers a handler counting its invocations; the handler acks.
	install func(r *EventReceiver, calls *atomic.Int32)
	// send issues the typed client call and returns ok/reason/err.
	send func(path string) (ok bool, reason string, err error)
}

func controlCases() []controlCase {
	return []controlCase{
		{
			name: "stop",
			kind: "babysit-stop",
			install: func(r *EventReceiver, calls *atomic.Int32) {
				r.SetBabysitStopHandler(func(BabysitStopRequest) BabysitStopResponse {
					calls.Add(1)
					return BabysitStopResponse{OK: true}
				})
			},
			send: func(path string) (bool, string, error) {
				resp, err := SendBabysitStop(path, BabysitStopRequest{PR: "42", Repo: "o/r"})
				return resp.OK, resp.Reason, err
			},
		},
		{
			name: "status",
			kind: "babysit-status",
			install: func(r *EventReceiver, calls *atomic.Int32) {
				r.SetBabysitStatusHandler(func(BabysitStatusRequest) BabysitStatusResponse {
					calls.Add(1)
					return BabysitStatusResponse{OK: true, Status: &BabysitStatus{PR: 42, Repo: "o/r"}}
				})
			},
			send: func(path string) (bool, string, error) {
				resp, err := SendBabysitStatus(path, BabysitStatusRequest{PR: "42", Repo: "o/r"})
				return resp.OK, resp.Reason, err
			},
		},
	}
}

func startControlReceiver(t *testing.T) (string, *EventReceiver) {
	t.Helper()
	path := tempSocket(t)
	recv, err := NewEventReceiver(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	return path, recv
}

func acceptControl(t *testing.T, recv *EventReceiver) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go recv.Accept(ctx)
	time.Sleep(10 * time.Millisecond)
}

func TestEventReceiver_ControlRequestRoutesToHandlerNotEvents(t *testing.T) {
	for _, tc := range controlCases() {
		t.Run(tc.name, func(t *testing.T) {
			path, recv := startControlReceiver(t)
			var calls atomic.Int32
			tc.install(recv, &calls)
			acceptControl(t, recv)

			ok, _, err := tc.send(path)
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if !ok {
				t.Fatal("ok = false, want the handler's ack")
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("handler invocations = %d, want exactly 1", got)
			}
			select {
			case evt := <-recv.Events():
				t.Fatalf("a %s request must never reach Events(), got %+v", tc.kind, evt)
			case <-time.After(150 * time.Millisecond):
			}
		})
	}
}

func TestEventReceiver_StatusResponseCarriesTheStatusObject(t *testing.T) {
	path, recv := startControlReceiver(t)
	want := BabysitStatus{PR: 42, Repo: "o/r", Status: "needs-input", CIFixAttempts: 2, ConflictFixAttempts: 1, FixCap: 3, SupervisorLive: false}
	var got BabysitStatusRequest
	recv.SetBabysitStatusHandler(func(req BabysitStatusRequest) BabysitStatusResponse {
		got = req
		return BabysitStatusResponse{OK: true, Status: &want}
	})
	acceptControl(t, recv)

	resp, err := SendBabysitStatus(path, BabysitStatusRequest{PR: "42", Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status == nil || *resp.Status != want {
		t.Fatalf("status = %+v, want %+v", resp.Status, want)
	}
	if got.PR != "42" || got.Repo != "o/r" {
		t.Fatalf("handler saw %+v, want pr=42 repo=o/r", got)
	}
}

func TestEventReceiver_ControlResponseExactlyOneLineThenConnCloses(t *testing.T) {
	for _, tc := range controlCases() {
		t.Run(tc.name, func(t *testing.T) {
			path, recv := startControlReceiver(t)
			var calls atomic.Int32
			tc.install(recv, &calls)
			acceptControl(t, recv)

			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			line := `{"kind":"` + tc.kind + `","pr":"42","repo":"o/r"}` + "\n"
			if _, err := conn.Write([]byte(line)); err != nil {
				t.Fatal(err)
			}
			first, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				t.Fatalf("read response line: %v", err)
			}
			var resp struct {
				OK bool `json:"ok"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &resp); err != nil || !resp.OK {
				t.Fatalf("response line %q: ok=%v err=%v, want an ack", first, resp.OK, err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if n, err := conn.Read(make([]byte, 16)); err != io.EOF {
				t.Fatalf("read after the response: n=%d err=%v, want io.EOF", n, err)
			}
		})
	}
}

func TestEventReceiver_NilControlHandlerNacks(t *testing.T) {
	for _, tc := range controlCases() {
		t.Run(tc.name, func(t *testing.T) {
			path, recv := startControlReceiver(t)
			acceptControl(t, recv)

			ok, reason, err := tc.send(path)
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if ok {
				t.Fatal("ok = true, want a nack when no handler was installed (fail closed)")
			}
			if reason == "" {
				t.Error("reason is empty, want a stable non-empty nack reason")
			}
			select {
			case evt := <-recv.Events():
				t.Fatalf("a %s request must never reach Events(), got %+v", tc.kind, evt)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestEventReceiver_MalformedControlBodyNacksWithoutCallingHandler(t *testing.T) {
	for _, tc := range controlCases() {
		t.Run(tc.name, func(t *testing.T) {
			path, recv := startControlReceiver(t)
			var calls atomic.Int32
			tc.install(recv, &calls)
			acceptControl(t, recv)

			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			// "pr" is a number where the request field is a string.
			line := `{"kind":"` + tc.kind + `","pr":12345,"repo":"o/r"}` + "\n"
			if _, err := conn.Write([]byte(line)); err != nil {
				t.Fatal(err)
			}
			first, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				t.Fatalf("read response line: %v", err)
			}
			var resp struct {
				OK     bool   `json:"ok"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &resp); err != nil {
				t.Fatalf("unmarshal %q: %v", first, err)
			}
			if resp.OK || resp.Reason == "" {
				t.Errorf("response = %+v, want a nack with a reason", resp)
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("handler invoked %d times for a malformed body, want 0", got)
			}
		})
	}
}

func TestEventReceiver_UnknownKindStillRoutesToEvents(t *testing.T) {
	path, recv := startControlReceiver(t)
	acceptControl(t, recv)

	line := `{"kind":"babysit-restart","event_type":"Stop","session_id":"s1","tmux_pane":"%1"}` + "\n"
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	select {
	case evt := <-recv.Events():
		if evt.EventType != "Stop" || evt.SessionID != "s1" {
			t.Fatalf("event = %+v, want the Stop event for s1", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an unknown kind must still route to Events()")
	}
}

func TestSendControl_DialFailureClassifiesAsUndelivered(t *testing.T) {
	path := tempSocket(t) // nothing is listening
	for _, tc := range controlCases() {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := tc.send(path); !errors.Is(err, ErrArmUndelivered) {
				t.Fatalf("err = %v, want errors.Is(err, ErrArmUndelivered)", err)
			}
		})
	}
}

// TestSendControl_HeldOpenConnectionClassifiesAsNoResponseAtTheDeadline pins
// the P25 deadline half: a daemon that reads the request but neither replies
// nor closes is reported as ErrArmNoResponse once controlResponseDeadline
// passes. The deadline is shortened through its package var so the test does
// not wait the production 5s.
func TestSendControl_HeldOpenConnectionClassifiesAsNoResponseAtTheDeadline(t *testing.T) {
	original := controlResponseDeadline
	controlResponseDeadline = 150 * time.Millisecond
	t.Cleanup(func() { controlResponseDeadline = original })

	for _, tc := range controlCases() {
		t.Run(tc.name, func(t *testing.T) {
			path := tempSocket(t)
			ln, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_, _ = bufio.NewReader(conn).ReadString('\n')
				<-release // hold the connection open, never reply
			}()

			start := time.Now()
			_, _, err = tc.send(path)
			if !errors.Is(err, ErrArmNoResponse) {
				t.Fatalf("err = %v, want errors.Is(err, ErrArmNoResponse)", err)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("took %v, want the shortened deadline to bound the wait", elapsed)
			}
		})
	}
}

// TestControlResponseDeadline_ProductionValueIsFiveSeconds pins the P25
// deadline itself: the shortening seam above must not hide a changed
// production value, and stop/status wait exactly as long as arm does.
func TestControlResponseDeadline_ProductionValueIsFiveSeconds(t *testing.T) {
	if controlResponseDeadline != 5*time.Second {
		t.Errorf("controlResponseDeadline = %v, want 5s", controlResponseDeadline)
	}
	if controlResponseDeadline != armResponseDeadline {
		t.Errorf("controlResponseDeadline = %v, want it equal to armResponseDeadline %v", controlResponseDeadline, armResponseDeadline)
	}
}

func TestSendControl_EOFWithoutReplyClassifiesAsNoResponse(t *testing.T) {
	for _, tc := range controlCases() {
		t.Run(tc.name, func(t *testing.T) {
			path := tempSocket(t)
			ln, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = bufio.NewReader(conn).ReadString('\n')
				_ = conn.Close()
			}()
			if _, _, err := tc.send(path); !errors.Is(err, ErrArmNoResponse) {
				t.Fatalf("err = %v, want errors.Is(err, ErrArmNoResponse)", err)
			}
		})
	}
}
