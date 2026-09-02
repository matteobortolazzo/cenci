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
