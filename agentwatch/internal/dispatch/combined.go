package dispatch

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/run"
	"github.com/matteobortolazzo/agent-stack/agentwatch/pkg/watch"
)

// loopCheckInterval is the sleep duration combinedTick returns on a
// bad-reload or disabled tick: short enough that a fixed config edit or a
// loopEnabled flip is picked up promptly, without busy-looping.
const loopCheckInterval = 60 * time.Second

// RunCombinedLoop runs the dispatch pass and the reconcile pass inside the
// daemon, reloading Config from configPath every tick and dynamically sizing
// its own sleep from the reloaded cfg.DaemonInterval (loopCheckInterval while
// disabled or on a bad reload). It owns the persistent dispatch-state and
// per-tick daily-quota tally across ticks, dispatches new pickups, reconciles
// stranded work, and pushes both a start-of-tick and an end-of-tick
// watch.AttentionUpdate (failed-window overlay, headroom, and dispatch state)
// onto attention so the daemon can badge the snapshot. It is the ctx-aware
// sibling of RunLoop and blocks until ctx is cancelled. The daemon always
// starts this loop; a disabled config simply keeps it idling on
// loopCheckInterval ticks.
func RunCombinedLoop(ctx context.Context, configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, attention chan<- watch.AttentionUpdate) {
	if out == nil {
		out = os.Stdout
	}
	store := NewStateStore("")
	prior := 0
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64

	for {
		sleep := combinedTick(ctx, configPath, ctrl, mut, out, &prior, store, attention, &state, &windows, &headroom)
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
	}
}

// combinedTick is RunCombinedLoop's per-tick body: reload Config from
// configPath once, then feed the same reloaded cfg to both RunOnce and
// RunReconcileOnce, publishing a start-of-tick and an end-of-tick
// watch.AttentionUpdate on an enabled tick. It returns the sleep duration the
// caller should wait before the next tick.
//
// On a reload error the whole tick is skipped -- no dispatch, no reconcile,
// no attention push -- and prior/state are left untouched, so a bad edit
// between ticks cannot crash the loop or run against a stale/partial config;
// loopCheckInterval is returned so the next attempt comes soon.
//
// On a disabled tick (cfg.LoopEnabled false), state is updated to
// Enabled:false/PassRunning:false and published with nil Windows/Headroom, so
// any previously badged failed windows and headroom are cleared within one
// interval; no pass runs; loopCheckInterval is returned.
//
// On an enabled tick, state.PassRunning is set and published (start-of-tick)
// before RunOnce/RunReconcileOnce run -- reusing *windows/*headroom from the
// previous tick's end-of-tick publish so the start-of-tick update doesn't
// blank the failed-window/headroom overlays for the pass's duration -- then
// updated with the pass's results and published again (end-of-tick)
// alongside the freshly computed failed-window overlay and headroom, which
// are also stashed into *windows/*headroom for the next tick's start-of-tick
// publish. The resolved cfg.DaemonInterval is returned, guarded to
// loopCheckInterval if not positive.
func combinedTick(ctx context.Context, configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, prior *int, store ObservationStore, attention chan<- watch.AttentionUpdate, state *watch.DispatchState, windows *[]watch.WindowState, headroom *map[string]float64) time.Duration {
	cfg, ok := reloadConfig(configPath, out)
	if !ok {
		return loopCheckInterval
	}

	if !cfg.LoopEnabled {
		state.Enabled = false
		state.PassRunning = false
		state.LastError = ""
		publish(ctx, attention, state, nil, nil)
		return loopCheckInterval
	}

	state.Enabled = true
	state.Interval = formatInterval(cfg.DaemonInterval)
	state.PassRunning = true
	publish(ctx, attention, state, *windows, *headroom)

	decisions, derr := RunOnce(cfg, ctrl, mut, false, out, prior)
	result, rerr := RunReconcileOnce(cfg, mut, false, out, store)

	state.PassRunning = false
	state.LastRunAt = time.Now().UTC().Format(time.RFC3339)
	state.LastDispatched = countDispatched(decisions)
	state.LastSkipped = countSkipped(decisions)
	state.LastError = firstErr(derr, rerr)

	*windows = failedWindows(result.Failed)
	*headroom = computeHeadroom(cfg)
	publish(ctx, attention, state, *windows, *headroom)

	sleep := cfg.DaemonInterval
	if sleep <= 0 {
		sleep = loopCheckInterval
	}
	return sleep
}

// publish sends a watch.AttentionUpdate carrying windows, headroom, and a
// value copy of *state onto attention, so the daemon can never alias the
// loop's mutable DispatchState across ticks. The send is ctx-guarded so a
// cancelled loop's tick body never blocks indefinitely.
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

// countDispatched counts the decisions whose Action is ActionDispatch.
func countDispatched(decisions []Decision) int {
	n := 0
	for _, d := range decisions {
		if d.Action == ActionDispatch {
			n++
		}
	}
	return n
}

// countSkipped counts the decisions whose Action is ActionSkip.
func countSkipped(decisions []Decision) int {
	n := 0
	for _, d := range decisions {
		if d.Action == ActionSkip {
			n++
		}
	}
	return n
}

// firstErr returns the first non-nil error's message among errs, or "" if
// all are nil. Used to populate DispatchState.LastError from the dispatch and
// reconcile passes' errors without stacking two error strings together.
func firstErr(errs ...error) string {
	for _, err := range errs {
		if err != nil {
			return err.Error()
		}
	}
	return ""
}

// computeHeadroom returns the current per-agent-type headroom snapshot, or an
// empty map when AgentLimits isn't configured (buildBudgetProvider falls back
// to a FloorProvider, which has no Headroom()).
func computeHeadroom(cfg Config) map[string]float64 {
	up, ok := buildBudgetProvider(cfg, time.Now()).(*UsageProvider)
	if !ok {
		return map[string]float64{}
	}
	return up.Headroom()
}

// failedWindows maps surfaced-failure tickets to synthetic "failed" window
// entries. A failed ticket has no real tmux window, so this is the only way it
// reaches the snapshot and the frontends.
func failedWindows(failed []Ticket) []watch.WindowState {
	out := make([]watch.WindowState, 0, len(failed))
	for _, t := range failed {
		out = append(out, watch.WindowState{
			WindowName: failedWindowName(t),
			Status:     "failed",
		})
	}
	return out
}

// failedWindowName builds the `<number>-implement` window name external tools
// join on. Dispatch always runs the implement workflow (applyDispatch in
// dispatch.go sets Workflow: "implement"), so a synthetic failed entry carries
// the same `<number>-<skill>` join shape a real dispatched window gets from
// run.windowName.
func failedWindowName(t Ticket) string {
	return fmt.Sprintf("%d-implement", t.Number)
}
