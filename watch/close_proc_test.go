package main

import (
	"context"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// deadlineRecorder is a windowProcKiller that records the context deadline and
// target it was handed.
type deadlineRecorder struct {
	deadline    time.Time
	hasDeadline bool
	target      string
}

func (r *deadlineRecorder) KillWindowProcesses(ctx context.Context, target string) error {
	r.deadline, r.hasDeadline = ctx.Deadline()
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
	if err := killProcessesFunc(rec)("=main:0"); err != nil {
		t.Fatalf("killProcessesFunc: %v", err)
	}

	if rec.target != "=main:0" {
		t.Errorf("target = %q, want =main:0", rec.target)
	}
	if !rec.hasDeadline {
		t.Fatal("KillWindowProcesses got a context with no deadline, want one bounding the kill")
	}
	want := 3*time.Second + sandbox.CloseKillMargin
	if got := rec.deadline.Sub(start); got < want-time.Second || got > want+time.Second {
		t.Errorf("deadline is %s after the call, want about %s (grace + sandbox.CloseKillMargin)", got, want)
	}
}
