package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// deadlineRecorder is a windowProcKiller that records the context deadline,
// socket and target it was handed.
type deadlineRecorder struct {
	deadline    time.Time
	hasDeadline bool
	socket      string
	target      string
}

func (r *deadlineRecorder) KillWindowProcesses(ctx context.Context, socket, target string) error {
	r.deadline, r.hasDeadline = ctx.Deadline()
	r.socket = socket
	r.target = target
	return nil
}

// `cenci close` bounds each window's process kill like the daemon does: the
// SIGTERM->SIGKILL grace window plus the shared margin, so a hung runtime exec
// can never hang the command indefinitely.
func TestKillProcessesFunc_BoundsTheKillByGraceWindowPlusSharedMargin(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "3")
	rec := &deadlineRecorder{}

	start := time.Now()
	const socket = "/tmp/tmux-1000/cenci"
	if err := killProcessesFunc(rec)(socket, "=main:0"); err != nil {
		t.Fatalf("killProcessesFunc: %v", err)
	}

	if rec.target != "=main:0" {
		t.Errorf("target = %q, want =main:0", rec.target)
	}
	if rec.socket != socket {
		t.Errorf("socket = %q, want %q handed through to the killer", rec.socket, socket)
	}
	if !rec.hasDeadline {
		t.Fatal("KillWindowProcesses got a context with no deadline, want one bounding the kill")
	}
	want := 3*time.Second + sandbox.CloseKillMargin
	if got := rec.deadline.Sub(start); got < want-time.Second || got > want+time.Second {
		t.Errorf("deadline is %s after the call, want about %s (grace + sandbox.CloseKillMargin)", got, want)
	}
}

func TestRegisterPendingClose_PropagatesSnapshotIdentityToEventSocket(t *testing.T) {
	// The window's recorded socket reaches the event socket (#1430); a window
	// with none recorded sends none, so it can only match a default-server pane.
	const recorded = "/tmp/tmux-1000/cenci"
	for _, socket := range []string{recorded, ""} {
		name := socket
		if name == "" {
			name = "no recorded socket"
		}
		t.Run(name, func(t *testing.T) {
			// Keep the Unix socket path below macOS's sun_path limit.
			dir, err := os.MkdirTemp("", "cenci-close-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			t.Setenv("CENCI_SOCKET_DIR", dir)
			recv, err := ipc.NewEventReceiver(ipc.DefaultEventSocketPath())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = recv.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go recv.Accept(ctx)
			w := watch.WindowState{Session: "main", WindowIndex: "3", WindowName: "782-implement", PaneID: "%42", Socket: socket}
			if err := registerPendingClose(w); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-recv.PendingCloses():
				want := ipc.PendingClose{Session: w.Session, WindowIndex: w.WindowIndex, WindowName: w.WindowName, PaneID: "%42", Socket: socket}
				if got != want {
					t.Fatalf("registration = %+v, want %+v", got, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pending-close registration was not delivered")
			}
		})
	}
}
