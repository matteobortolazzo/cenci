package dispatch

import (
	"context"
	"testing"
	"time"
)

func TestCombinedSchedulerCadenceAndPolling(t *testing.T) {
	start := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	cfg := Config{LoopEnabled: true, DaemonInterval: 5 * time.Minute}
	var s combinedScheduler

	if run, _ := s.plan(start, cfg); !run {
		t.Fatal("initial enable must run immediately")
	}
	s.complete(start, cfg)
	if run, wait := s.plan(start.Add(time.Minute), cfg); run || wait != loopCheckInterval {
		t.Fatalf("at +1m got run=%v wait=%v, want false/%v", run, wait, loopCheckInterval)
	}
	if run, wait := s.plan(start.Add(4*time.Minute), cfg); run || wait != loopCheckInterval {
		t.Fatalf("at +4m got run=%v wait=%v, want false/%v", run, wait, loopCheckInterval)
	}
	if run, _ := s.plan(start.Add(5*time.Minute), cfg); !run {
		t.Fatal("configured five-minute deadline must run")
	}
}

func TestCombinedSchedulerDisableReenableAndIntervalChanges(t *testing.T) {
	start := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	cfg := Config{LoopEnabled: true, DaemonInterval: 5 * time.Minute}
	var s combinedScheduler
	s.plan(start, cfg)
	s.complete(start, cfg)

	if run, wait := s.plan(start.Add(time.Minute), Config{LoopEnabled: false}); run || wait != loopCheckInterval {
		t.Fatalf("disable got run=%v wait=%v, want false/%v", run, wait, loopCheckInterval)
	}
	if !s.next.IsZero() || !s.lastCompleted.IsZero() {
		t.Fatal("disable must reset the retained schedule")
	}
	if run, _ := s.plan(start.Add(2*time.Minute), cfg); !run {
		t.Fatal("re-enable must run immediately")
	}
	s.complete(start.Add(2*time.Minute), cfg)

	shorter := Config{LoopEnabled: true, DaemonInterval: time.Minute}
	if run, _ := s.plan(start.Add(3*time.Minute), shorter); !run {
		t.Fatal("an interval edit whose recalculated deadline has passed must run")
	}
	s.complete(start.Add(3*time.Minute), shorter)
	if run, wait := s.plan(start.Add(3*time.Minute+30*time.Second), shorter); run || wait != 30*time.Second {
		t.Fatalf("sub-minute cadence got run=%v wait=%v, want false/30s", run, wait)
	}
}

func TestCombinedSchedulerContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitContext(ctx, time.Hour) {
		t.Fatal("cancelled context must stop the supervisor wait")
	}
}

func TestStateIntervalHidesNonpositiveValues(t *testing.T) {
	if got := stateInterval(0); got != "" {
		t.Fatalf("zero interval = %q, want empty", got)
	}
	if got := stateInterval(-time.Minute); got != "" {
		t.Fatalf("negative interval = %q, want empty", got)
	}
}
