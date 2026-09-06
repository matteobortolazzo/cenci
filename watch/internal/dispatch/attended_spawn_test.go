package dispatch

import (
	"bytes"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// TestApplyDispatchSpawnsUnattended covers #1087's defense-in-depth AC at the
// call site the #824 rule cares about: applyDispatch must actually SET
// run.Opts.Unattended, not merely have the option available. run.Run turns
// that flag into a CENCI_ATTENDED=0 pin in the spawned window's environment,
// which the sandbox launcher then honors over the host planning.attended flag
// (watch/internal/sandbox/launcher/attended_test.go covers that half). Without
// the flag wired in here, a mid-flight `cenci planning attended on` would let
// a dispatched session route into an interactive AskUserQuestion inside a
// detached tmux window and wait there forever with the ticket on Working.
func TestApplyDispatchSpawnsUnattended(t *testing.T) {
	var captured []run.Opts
	stubRunFn(t, func(o run.Opts, _ run.Controller) error {
		captured = append(captured, o)
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	prior := 0

	if _, err := applyDispatch(dispatchTestConfig(), dispatchableDeps(now), fakeController{}, &fakeMutator{}, false, &buf, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	if len(captured) != 1 {
		t.Fatalf("expected 1 spawn, got %d: %+v", len(captured), captured)
	}
	if !captured[0].Unattended {
		t.Errorf("dispatch spawn Opts.Unattended = false, want true; got %+v", captured[0])
	}
}
