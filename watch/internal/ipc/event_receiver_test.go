package ipc

import (
	"context"
	"fmt"
	"net"
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
