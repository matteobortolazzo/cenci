package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/pkg/watch"
)

func TestFailedWindowsMapping(t *testing.T) {
	got := failedWindows([]Ticket{
		{Repo: "o/r", Number: 42, Title: "Fix the thing!"},
		{Repo: "o/r", Number: 7, Title: ""},
	})
	if len(got) != 2 {
		t.Fatalf("got %d windows, want 2", len(got))
	}
	// Dispatch always runs the implement workflow, so the synthetic failed
	// entry carries the `<number>-implement` join shape regardless of title.
	if got[0].WindowName != "42-implement" || got[0].Status != "failed" {
		t.Errorf("window[0] = %+v, want 42-implement/failed", got[0])
	}
	if got[1].WindowName != "7-implement" || got[1].Status != "failed" {
		t.Errorf("window[1] = %+v, want 7-implement/failed", got[1])
	}
}

// --- combinedTick headroom wiring (#169) ---
//
// combinedTick computes per-agent-type headroom from the same UsageProvider
// machinery Budget() uses, on its existing interval, and delivers it into the
// pushed watch.AttentionUpdate alongside the existing failed-window overlay.

// writeDispatchConfig writes a raw "dispatch" block to configPath, giving
// tests full control over fields (agentLimits, claudeSessionDir) that
// seedDispatchConfig's EnrollRepo-only helper does not expose.
func writeDispatchConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing dispatch config %s: %v", path, err)
	}
}

func TestCombinedTickPushesHeadroomWhenAgentLimitsConfigured(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "claude-sessions")
	project := filepath.Join(sessionDir, "project-a")
	writeFixtureJSONL(t, project, "s1.jsonl", fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"usage":{"output_tokens":1000}}}
`, time.Now().UTC().Format(time.RFC3339Nano)))

	configPath := filepath.Join(dir, "config.json")
	writeDispatchConfig(t, configPath, fmt.Sprintf(
		`{"dispatch":{"loopEnabled":true,"repos":[],"agentLimits":{"claude":{"fiveHourTokens":10000,"weeklyTokens":100000}},"claudeSessionDir":%q}}`,
		sessionDir))

	prior := 0
	mut := &fakeMutator{}
	store := &memStore{}
	// Capacity 2: an enabled tick sends a start-of-tick and an end-of-tick
	// update synchronously, and nothing drains between the two sends.
	attention := make(chan watch.AttentionUpdate, 2)
	var buf bytes.Buffer
	ctx := context.Background()
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64

	combinedTick(ctx, configPath, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)

	<-attention // start-of-tick: headroom is computed only in the end-of-tick publish.

	select {
	case update := <-attention: // end-of-tick
		h, ok := update.Headroom["claude"]
		if !ok {
			t.Fatalf("expected claude to be present in headroom map, got %v", update.Headroom)
		}
		if h <= 0 || h >= 1 {
			t.Errorf("claude headroom = %v, want strictly between 0 and 1 (partial usage recorded)", h)
		}
	default:
		t.Fatal("expected an end-of-tick attention push on a successful tick")
	}
}

// TestCombinedTickStartOfTickReusesPreviousEndOfTickHeadroom locks in that the
// start-of-tick publish carries the previous tick's end-of-tick Headroom
// forward (per the approved plan's "previous run's fields otherwise
// unchanged") instead of publishing nil, which would blank the headroom
// overlay in tmux/waybar for the whole duration of the dispatch+reconcile
// pass on every tick.
func TestCombinedTickStartOfTickReusesPreviousEndOfTickHeadroom(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "claude-sessions")
	project := filepath.Join(sessionDir, "project-a")
	writeFixtureJSONL(t, project, "s1.jsonl", fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"usage":{"output_tokens":1000}}}
`, time.Now().UTC().Format(time.RFC3339Nano)))

	configPath := filepath.Join(dir, "config.json")
	writeDispatchConfig(t, configPath, fmt.Sprintf(
		`{"dispatch":{"loopEnabled":true,"repos":[],"agentLimits":{"claude":{"fiveHourTokens":10000,"weeklyTokens":100000}},"claudeSessionDir":%q}}`,
		sessionDir))

	prior := 0
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 2)
	var buf bytes.Buffer
	ctx := context.Background()
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64

	combinedTick(ctx, configPath, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)

	<-attention // tick 1 start-of-tick: nothing published yet, headroom still nil.
	tick1End := <-attention
	if len(tick1End.Headroom) == 0 {
		t.Fatalf("expected tick 1 end-of-tick to carry a non-empty headroom map, got %v", tick1End.Headroom)
	}

	combinedTick(ctx, configPath, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)

	tick2Start := <-attention
	if !reflect.DeepEqual(tick2Start.Headroom, tick1End.Headroom) {
		t.Errorf("tick 2 start-of-tick Headroom = %v, want tick 1's end-of-tick Headroom %v carried forward", tick2Start.Headroom, tick1End.Headroom)
	}
	<-attention // drain tick 2 end-of-tick
}

func TestCombinedTickHeadroomEmptyWhenNoAgentLimitsConfigured(t *testing.T) {
	dir := t.TempDir()
	path := seedDispatchConfig(t, dir, "o/A")

	prior := 0
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 2)
	var buf bytes.Buffer
	ctx := context.Background()
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64

	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)

	<-attention // start-of-tick: headroom is computed only in the end-of-tick publish.

	select {
	case update := <-attention: // end-of-tick
		if len(update.Headroom) != 0 {
			t.Errorf("expected empty headroom map with no agentLimits configured, got %v", update.Headroom)
		}
	default:
		t.Fatal("expected an end-of-tick attention push on a successful tick")
	}
}
