package ipc

import (
	"context"
	"testing"
	"time"
)

func TestClient_ReadMultipleSnapshots(t *testing.T) {
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
	defer func() { _ = c.Close() }()

	time.Sleep(10 * time.Millisecond)

	// Send two snapshots.
	srv.Broadcast(StateSnapshot{Timestamp: "t1", Summary: StatusSummary{Total: 1}})
	srv.Broadcast(StateSnapshot{Timestamp: "t2", Summary: StatusSummary{Total: 2}})

	got1, err := c.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got1.Timestamp != "t1" {
		t.Errorf("expected t1, got %q", got1.Timestamp)
	}

	got2, err := c.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got2.Timestamp != "t2" {
		t.Errorf("expected t2, got %q", got2.Timestamp)
	}
}
