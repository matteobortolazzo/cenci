package dispatch

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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
func RunCombinedLoop(ctx context.Context, configPath string, ctrl run.Controller, mut TicketMutator, interval time.Duration, out io.Writer, attention chan<- []watch.WindowState) {
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
func combinedTick(ctx context.Context, configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, prior *int, store ObservationStore, attention chan<- []watch.WindowState) {
	cfg, ok := reloadConfig(configPath, out)
	if !ok {
		return
	}

	RunOnce(cfg, ctrl, false, out, prior)
	result := RunReconcileOnce(cfg, mut, false, out, store)
	if attention != nil {
		select {
		case attention <- failedWindows(result.Failed):
		case <-ctx.Done():
		}
	}
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

// failedWindowMaxLen mirrors run.windowNameMaxLen so a synthetic failed entry
// carries the same bounded "<number>-<slug>" join key as a real dispatched
// window, keeping external name matching consistent.
const failedWindowMaxLen = 40

// failedWindowName builds the "<number>-<slug>" window name external tools join
// on, falling back to the bare number when the title yields no slug. The result
// is capped to failedWindowMaxLen with the leading number kept intact (slugify
// output is ASCII, so a byte cap is a rune cap).
func failedWindowName(t Ticket) string {
	slug := slugify(t.Title)
	if slug == "" {
		return strconv.Itoa(t.Number)
	}
	name := fmt.Sprintf("%d-%s", t.Number, slug)
	if len(name) > failedWindowMaxLen {
		name = strings.TrimRight(name[:failedWindowMaxLen], "-")
	}
	return name
}

// slugify lowercases s and collapses runs of non-alphanumeric characters into
// single hyphens, trimming leading/trailing hyphens.
func slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
