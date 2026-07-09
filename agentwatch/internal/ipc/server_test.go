package ipc

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func tempSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.sock")
}

func TestServer_BroadcastToClient(t *testing.T) {
	path := tempSocket(t)
	srv, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)

	// Allow listener to start.
	time.Sleep(10 * time.Millisecond)

	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Allow connection to be accepted.
	time.Sleep(10 * time.Millisecond)

	snap := StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Windows: []WindowState{
			{Session: "main", WindowIndex: "0", Status: "running", TaskName: "writing tests"},
		},
		Summary: StatusSummary{Total: 1, Running: 1},
	}
	srv.Broadcast(snap)

	got, err := c.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Running != 1 {
		t.Errorf("expected 1 running, got %d", got.Summary.Running)
	}
	if got.Windows[0].TaskName != "writing tests" {
		t.Errorf("expected task name 'writing tests', got %q", got.Windows[0].TaskName)
	}
}

func TestServer_MultipleClients(t *testing.T) {
	path := tempSocket(t)
	srv, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)

	time.Sleep(10 * time.Millisecond)

	c1, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c1.Close() }()

	c2, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.Close() }()

	time.Sleep(10 * time.Millisecond)

	snap := StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Summary:   StatusSummary{Total: 2, Running: 2},
	}
	srv.Broadcast(snap)

	got1, err := c1.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	got2, err := c2.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}

	if got1.Summary.Total != 2 || got2.Summary.Total != 2 {
		t.Errorf("expected both clients to get total=2, got %d and %d", got1.Summary.Total, got2.Summary.Total)
	}
}

func TestServer_NewClientGetsCachedState(t *testing.T) {
	path := tempSocket(t)
	srv, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)

	time.Sleep(10 * time.Millisecond)

	// Broadcast before any client connects.
	snap := StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Summary:   StatusSummary{Total: 3, Done: 3},
	}
	srv.Broadcast(snap)

	// New client connects — should receive cached state.
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	got, err := c.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Done != 3 {
		t.Errorf("expected cached done=3, got %d", got.Summary.Done)
	}
}

func TestServer_ClientDisconnect(t *testing.T) {
	path := tempSocket(t)
	srv, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)

	time.Sleep(10 * time.Millisecond)

	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}

	// Allow connection to be accepted.
	time.Sleep(10 * time.Millisecond)

	// Close client before broadcast.
	_ = c.Close()

	// Broadcast should not panic — broken client is removed.
	snap := StateSnapshot{Timestamp: "now"}
	srv.Broadcast(snap)

	srv.mu.Lock()
	count := len(srv.clients)
	srv.mu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 clients after disconnect, got %d", count)
	}
}

func TestServer_CloseGivesClientEOF(t *testing.T) {
	path := tempSocket(t)
	srv, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)

	time.Sleep(10 * time.Millisecond)

	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	time.Sleep(10 * time.Millisecond)

	_ = srv.Close()

	_, err = c.ReadSnapshot()
	if err == nil {
		t.Error("expected error after server close, got nil")
	}
}

func TestServer_DeadClientsCleanedUp(t *testing.T) {
	path := tempSocket(t)
	srv, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)

	time.Sleep(10 * time.Millisecond)

	// Seed cached state so new clients get a snapshot.
	snap := StateSnapshot{
		Timestamp: "2024-01-01T00:00:00Z",
		Summary:   StatusSummary{Total: 1, Running: 1},
	}
	srv.Broadcast(snap)

	// Connect and immediately close 20 clients (exceeds maxClients=16).
	for i := 0; i < 20; i++ {
		c, err := Dial(path)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = c.Close()
	}

	// Wait for monitor goroutines to detect disconnects.
	time.Sleep(50 * time.Millisecond)

	srv.mu.Lock()
	count := len(srv.clients)
	srv.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 dead clients, got %d", count)
	}

	// A new client should connect and receive the cached snapshot.
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	got, err := c.ReadSnapshot()
	if err != nil {
		t.Fatalf("expected snapshot, got error: %v", err)
	}
	if got.Summary.Running != 1 {
		t.Errorf("expected running=1, got %d", got.Summary.Running)
	}
}

func TestServer_StaleSocketCleanup(t *testing.T) {
	path := tempSocket(t)

	// Create first server.
	srv1, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = srv1.Close()

	// Second server should succeed (stale socket removed).
	srv2, err := NewServer(path)
	if err != nil {
		t.Fatalf("expected stale socket cleanup, got: %v", err)
	}
	_ = srv2.Close()
}
