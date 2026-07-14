package dispatch

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/run"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/pkg/watch"
)

// loopCheckInterval bounds configuration latency without changing dispatch
// cadence. The supervisor polls at least this often and runs passes only at
// their configured deadlines.
const loopCheckInterval = 60 * time.Second

type combinedScheduler struct {
	enabled       bool
	lastCompleted time.Time
	next          time.Time
}

func effectiveInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return loopCheckInterval
	}
	return d
}

// plan applies the current configuration and reports whether a pass is due.
// An enable transition always runs immediately; interval edits move the next
// deadline relative to the previous completion without inventing a pass.
func (s *combinedScheduler) plan(now time.Time, cfg Config) (run bool, wait time.Duration) {
	if !cfg.LoopEnabled {
		s.enabled = false
		s.lastCompleted = time.Time{}
		s.next = time.Time{}
		return false, loopCheckInterval
	}
	if !s.enabled {
		s.enabled = true
		s.next = now
	}
	if !s.lastCompleted.IsZero() {
		s.next = s.lastCompleted.Add(effectiveInterval(cfg.DaemonInterval))
	}
	if !now.Before(s.next) {
		return true, 0
	}
	return false, minDuration(loopCheckInterval, s.next.Sub(now))
}

func (s *combinedScheduler) complete(now time.Time, cfg Config) {
	s.lastCompleted = now
	s.next = now.Add(effectiveInterval(cfg.DaemonInterval))
}

func minDuration(a, b time.Duration) time.Duration {
	if b < a {
		return b
	}
	return a
}

// RunCombinedLoop is the daemon-owned supervisor. It reloads configuration
// every minute (or sooner when a pass is due), while dispatching only at the
// configured cadence. Individual pass failures are published and logged but
// do not stop the daemon.
func RunCombinedLoop(ctx context.Context, configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, attention chan<- watch.AttentionUpdate) {
	if out == nil {
		out = os.Stdout
	}
	store := NewStateStore("")
	prior := 0
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64
	var scheduler combinedScheduler

	for {
		cfg, ok := reloadConfig(configPath, out)
		if !ok {
			if !waitContext(ctx, loopCheckInterval) {
				return
			}
			continue
		}
		state.Interval = stateInterval(cfg.DaemonInterval)
		runPass, wait := scheduler.plan(time.Now(), cfg)
		if !cfg.LoopEnabled {
			state.Enabled = false
			state.PassRunning = false
			state.LastError = ""
			windows = nil
			headroom = nil
			publish(ctx, attention, &state, nil, nil)
		} else if runPass {
			runCombinedPass(ctx, cfg, ctrl, mut, out, &prior, store, attention, &state, &windows, &headroom)
			scheduler.complete(time.Now(), cfg)
			wait = minDuration(loopCheckInterval, effectiveInterval(cfg.DaemonInterval))
		}
		if !waitContext(ctx, wait) {
			return
		}
	}
}

func waitContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func stateInterval(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return formatInterval(d)
}

// combinedTick remains a deterministic single-pass helper for direct tests.
// Scheduling is deliberately owned by combinedScheduler above.
func combinedTick(ctx context.Context, configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, prior *int, store ReconcileStore, attention chan<- watch.AttentionUpdate, state *watch.DispatchState, windows *[]watch.WindowState, headroom *map[string]float64) time.Duration {
	cfg, ok := reloadConfig(configPath, out)
	if !ok {
		return loopCheckInterval
	}
	state.Interval = stateInterval(cfg.DaemonInterval)
	if !cfg.LoopEnabled {
		state.Enabled = false
		state.PassRunning = false
		state.LastError = ""
		*windows = nil
		*headroom = nil
		publish(ctx, attention, state, nil, nil)
		return loopCheckInterval
	}
	runCombinedPass(ctx, cfg, ctrl, mut, out, prior, store, attention, state, windows, headroom)
	return effectiveInterval(cfg.DaemonInterval)
}

func runCombinedPass(ctx context.Context, cfg Config, ctrl run.Controller, mut TicketMutator, out io.Writer, prior *int, store ReconcileStore, attention chan<- watch.AttentionUpdate, state *watch.DispatchState, windows *[]watch.WindowState, headroom *map[string]float64) {
	state.Enabled = true
	state.Interval = stateInterval(cfg.DaemonInterval)
	state.PassRunning = true
	publish(ctx, attention, state, *windows, *headroom)

	before := *prior
	decisions, dispatchErr := RunOnce(cfg, ctrl, mut, false, out, prior)
	result, reconcileErr := RunReconcileOnce(cfg, mut, false, out, store)

	state.PassRunning = false
	state.LastRunAt = time.Now().UTC().Format(time.RFC3339)
	state.LastDispatched = *prior - before
	state.LastSkipped = countSkipped(decisions)
	state.LastError = sanitizeLastError(passError(dispatchErr, reconcileErr))
	*windows = failedWindows(result.Failed)
	*headroom = computeHeadroom(cfg)
	publish(ctx, attention, state, *windows, *headroom)
}

// sanitizeLastError normalizes a pass error for safe inclusion in the
// status JSON's pipe-delimited/menu frontends: it replaces the field
// delimiter and line breaks with spaces, then collapses whitespace runs.
func sanitizeLastError(s string) string {
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("|", " ", "\n", " ", "\r", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func passError(dispatchErr, reconcileErr error) string {
	if dispatchErr != nil {
		return "dispatch_pass_failed"
	}
	if reconcileErr != nil {
		return "reconcile_pass_failed"
	}
	return ""
}

func publish(ctx context.Context, attention chan<- watch.AttentionUpdate, state *watch.DispatchState, windows []watch.WindowState, headroom map[string]float64) {
	if attention == nil {
		return
	}
	s := *state
	u := watch.AttentionUpdate{Dispatch: &s, Windows: windows, Headroom: headroom}
	select {
	case attention <- u:
	case <-ctx.Done():
	}
}

func countSkipped(decisions []Decision) int {
	n := 0
	for _, d := range decisions {
		if d.Action == ActionSkip {
			n++
		}
	}
	return n
}

func computeHeadroom(cfg Config) map[string]float64 {
	up, ok := buildBudgetProvider(cfg, time.Now()).(*UsageProvider)
	if !ok {
		return map[string]float64{}
	}
	return up.Headroom()
}

func failedWindows(failed []Ticket) []watch.WindowState {
	out := make([]watch.WindowState, 0, len(failed))
	for _, t := range failed {
		out = append(out, watch.WindowState{WindowName: failedWindowName(t), Status: "failed"})
	}
	return out
}

func failedWindowName(t Ticket) string { return fmt.Sprintf("%d-implement", t.Number) }
