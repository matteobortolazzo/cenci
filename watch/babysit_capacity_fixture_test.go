package main_test

// Ticket #1469: the fixture a black-box `cenci babysit --once` world needs for
// capacity admission. Every branch-writer launch (ci-repair, merge-repair,
// babysit-review) is now admitted against the host's load before `cenci run`
// starts, and admission fails closed when it cannot observe that load, so a fake
// world that expects a launch must answer the same questions a real host does:
//
//   - the daemon's broadcast socket serves a snapshot (serveCapacityDaemon), here
//     one with no running session, so the host load is the ledger alone;
//   - XDG_STATE_HOME is an isolated directory, so the admission ledger never
//     touches the developer's real state (watch/docs/test-isolation.md; both
//     worlds already own one);
//   - the fake tmux answers the server-wide `list-panes -a` listing of
//     tmux.ListCleanupInventory with no panes: a complete, empty inventory.
//
// The limits file needs nothing: an absent key is the default cap of 3, and a
// world's single repair is far below it.

import (
	"context"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// serveCapacityDaemon serves an empty daemon snapshot on the broadcast socket
// under socketDir, the CENCI_SOCKET_DIR of the world's child processes, so
// admission's snapshot read succeeds the way it does next to a live daemon. The
// path is resolved through watch.DefaultSocketPath with the same override the
// child sees, so the two can never name different sockets.
func serveCapacityDaemon(t *testing.T, socketDir string) {
	t.Helper()
	serveSnapshotAt(t, socketDir, watch.StateSnapshot{})
}

// serveSnapshotAt points CENCI_SOCKET_DIR at socketDir and serves snap to every
// reader of the broadcast socket there.
func serveSnapshotAt(t *testing.T, socketDir string, snap watch.StateSnapshot) {
	t.Helper()
	t.Setenv("CENCI_SOCKET_DIR", socketDir)
	srv, err := ipc.NewServer(watch.DefaultSocketPath())
	if err != nil {
		t.Fatalf("serving the capacity daemon snapshot: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})
	go srv.Accept(ctx)
	srv.Broadcast(snap)
}
