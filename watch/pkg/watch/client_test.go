package watch_test

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// tempSocket returns a bind-safe Unix socket path. macOS caps sun_path at 104
// bytes and t.TempDir() embeds the (long) test name, so build a short dir here.
func tempSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "aw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func TestDial_NoListenerReturnsErrDaemonUnreachable(t *testing.T) {
	path := tempSocket(t) // no listener bound to this path

	_, err := watch.Dial(path)
	if !errors.Is(err, watch.ErrDaemonUnreachable) {
		t.Fatalf("Dial = %v, want errors.Is(err, watch.ErrDaemonUnreachable)", err)
	}
}

func TestClient_ReadsSnapshotsInOrderThenClose(t *testing.T) {
	path := tempSocket(t)

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Server: accept one connection, write two NDJSON snapshots, then close.
	served := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			served <- aerr
			return
		}
		defer func() { _ = conn.Close() }()
		enc := json.NewEncoder(conn) // Encoder appends a trailing newline per call.
		if werr := enc.Encode(watch.StateSnapshot{Timestamp: "t1", Summary: watch.StatusSummary{Total: 1}}); werr != nil {
			served <- werr
			return
		}
		if werr := enc.Encode(watch.StateSnapshot{
			Timestamp: "t2",
			Windows:   []watch.WindowState{{WindowName: "42-fix-bug", Status: "running"}},
			Summary:   watch.StatusSummary{Total: 2, Running: 1},
		}); werr != nil {
			served <- werr
			return
		}
		served <- nil
	}()

	c, err := watch.Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	got1, err := c.ReadSnapshot()
	if err != nil {
		t.Fatalf("ReadSnapshot 1: %v", err)
	}
	if got1.Timestamp != "t1" || got1.Summary.Total != 1 {
		t.Errorf("snapshot 1 = %+v, want t1/total=1", got1)
	}

	got2, err := c.ReadSnapshot()
	if err != nil {
		t.Fatalf("ReadSnapshot 2: %v", err)
	}
	if got2.Timestamp != "t2" || got2.Summary.Running != 1 {
		t.Errorf("snapshot 2 = %+v, want t2/running=1", got2)
	}
	if len(got2.Windows) != 1 || got2.Windows[0].WindowName != "42-fix-bug" {
		t.Errorf("snapshot 2 windows = %+v, want one window named 42-fix-bug", got2.Windows)
	}

	if err := <-served; err != nil {
		t.Fatalf("server: %v", err)
	}

	// After a clean server close, the next read reports net.ErrClosed.
	if _, err := c.ReadSnapshot(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("ReadSnapshot after close = %v, want net.ErrClosed", err)
	}
}
