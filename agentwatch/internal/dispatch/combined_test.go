package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	if got[0].WindowName != "42-fix-the-thing" || got[0].Status != "failed" {
		t.Errorf("window[0] = %+v, want 42-fix-the-thing/failed", got[0])
	}
	// No title ⇒ bare number.
	if got[1].WindowName != "7" || got[1].Status != "failed" {
		t.Errorf("window[1] = %+v, want 7/failed", got[1])
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
		`{"dispatch":{"repos":[],"agentLimits":{"claude":{"fiveHourTokens":10000,"weeklyTokens":100000}},"claudeSessionDir":%q}}`,
		sessionDir))

	prior := 0
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 1)
	var buf bytes.Buffer
	ctx := context.Background()

	combinedTick(ctx, configPath, fakeController{}, mut, &buf, &prior, store, attention)

	select {
	case update := <-attention:
		h, ok := update.Headroom["claude"]
		if !ok {
			t.Fatalf("expected claude to be present in headroom map, got %v", update.Headroom)
		}
		if h <= 0 || h >= 1 {
			t.Errorf("claude headroom = %v, want strictly between 0 and 1 (partial usage recorded)", h)
		}
	default:
		t.Fatal("expected an attention push on a successful tick")
	}
}

func TestCombinedTickHeadroomEmptyWhenNoAgentLimitsConfigured(t *testing.T) {
	dir := t.TempDir()
	path := seedDispatchConfig(t, dir, "o/A")

	prior := 0
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 1)
	var buf bytes.Buffer
	ctx := context.Background()

	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention)

	select {
	case update := <-attention:
		if len(update.Headroom) != 0 {
			t.Errorf("expected empty headroom map with no agentLimits configured, got %v", update.Headroom)
		}
	default:
		t.Fatal("expected an attention push on a successful tick")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Fix the thing!":       "fix-the-thing",
		"  Trim  spaces  ":     "trim-spaces",
		"already-slugged":      "already-slugged",
		"Multiple   ---dashes": "multiple-dashes",
		"":                     "",
		"!!!":                  "",
		"CamelCase123":         "camelcase123",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
