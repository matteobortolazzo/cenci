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

// RunCombinedLoop runs the dispatch pass and the reconcile pass on one interval
// inside the daemon, reloading Config from configPath every tick. Each tick
// dispatches new pickups (threading the in-memory daily-quota tally),
// reconciles stranded work, then pushes the current set of surfaced failures
// onto attention as synthetic "failed" windows so the daemon can badge them on
// the snapshot. It is the ctx-aware sibling of RunLoop and blocks until ctx is
// cancelled.
func RunCombinedLoop(ctx context.Context, configPath string, ctrl run.Controller, mut TicketMutator, interval time.Duration, out io.Writer, attention chan<- watch.AttentionUpdate) {
	if out == nil {
		out = os.Stdout
	}
	store := NewStateStore("")
	prior := 0

	combinedTick(ctx, configPath, ctrl, mut, out, &prior, store, attention)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			combinedTick(ctx, configPath, ctrl, mut, out, &prior, store, attention)
		}
	}
}

// combinedTick is RunCombinedLoop's per-tick body: reload Config from
// configPath once, then feed the same reloaded cfg to both RunOnce and
// RunReconcileOnce before pushing the failure overlay onto attention. On a
// reload error the whole tick is skipped — no dispatch, no reconcile, no
// attention push — and prior is left untouched, so a bad edit between ticks
// cannot crash the loop or run against a stale/partial config.
func combinedTick(ctx context.Context, configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, prior *int, store ObservationStore, attention chan<- watch.AttentionUpdate) {
	cfg, ok := reloadConfig(configPath, out)
	if !ok {
		return
	}

	RunOnce(cfg, ctrl, mut, false, out, prior)
	result := RunReconcileOnce(cfg, mut, false, out, store)
	if attention != nil {
		update := watch.AttentionUpdate{Windows: failedWindows(result.Failed), Headroom: computeHeadroom(cfg)}
		select {
		case attention <- update:
		case <-ctx.Done():
		}
	}
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
