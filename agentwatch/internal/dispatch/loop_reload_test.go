package dispatch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/run"
	"github.com/matteobortolazzo/agent-stack/agentwatch/pkg/watch"
)

// Ticket #122: RunLoop and RunCombinedLoop must reload dispatch.Config from a
// configPath every tick (instead of once at startup), and must skip the whole
// tick — no dispatch, no reconcile, no attention push — on a reload error,
// preserving the in-memory quota tally. Per the approved plan, the per-tick
// body is extracted into unexported dispatchTick/combinedTick helpers so these
// tests can drive single ticks deterministically without a real time.Ticker.
//
// These tests are RED by design: dispatchTick and combinedTick do not exist
// yet (Phase 3, tests only). `go build`/`go vet` on this package must fail
// with "undefined: dispatchTick" and "undefined: combinedTick" until the
// implementation phase adds them.

// fakeController is a no-op run.Controller. The reload tests never reach an
// actual dispatch call — CollectTickets fails first in this gh-less test
// environment — so every method here only needs to satisfy the interface.
type fakeController struct{}

func (fakeController) CurrentSession() (string, error)                    { return "", nil }
func (fakeController) IsGroupedSession(session string) (bool, error)      { return false, nil }
func (fakeController) NewWindow(session, name, shellCommand string) error { return nil }
func (fakeController) SetWindowOption(target, key, value string) error    { return nil }

// seedDispatchConfig writes a fresh config.json under dir enrolling repo, and
// returns its path. Using EnrollRepo (rather than hand-written JSON) exercises
// the same write path the `dispatch enroll` verb and board-driven enrollment
// use, per the ticket's live-enrollment scenario.
func seedDispatchConfig(t *testing.T, dir, repo string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if _, err := EnrollRepo(path, RepoIdentity{Repo: repo, Dir: t.TempDir()}); err != nil {
		t.Fatalf("seeding config with %s: %v", repo, err)
	}
	return path
}

// corruptConfig overwrites path with invalid JSON so the next LoadConfig call
// returns a parse error, simulating a bad edit landing between ticks.
func corruptConfig(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{ not valid json"), 0o600); err != nil {
		t.Fatalf("corrupting config %s: %v", path, err)
	}
}

// drainAttention non-blockingly empties ch, so a prior tick's push (if any)
// cannot be mistaken for the tick under test, and cannot block a later tick's
// send against a full buffered channel.
func drainAttention(ch <-chan watch.AttentionUpdate) {
	select {
	case <-ch:
	default:
	}
}

// --- dispatchTick (RunLoop's per-tick body) ---

func TestDispatchTickBadReloadSkipsTick(t *testing.T) {
	dir := t.TempDir()
	path := seedDispatchConfig(t, dir, "o/A")

	prior := 3
	var buf bytes.Buffer

	// Baseline tick against a valid config.
	dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior)
	buf.Reset()

	// Config goes bad between ticks.
	corruptConfig(t, path)

	dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior)

	if prior != 3 {
		t.Errorf("prior quota tally changed on a skipped tick: got %d, want 3", prior)
	}
	if !strings.Contains(buf.String(), "config") {
		t.Errorf("expected the reload error to be logged to out, got %q", buf.String())
	}
}

func TestDispatchTickReloadPicksUpEnrollment(t *testing.T) {
	dir := t.TempDir()
	path := seedDispatchConfig(t, dir, "o/A")

	prior := 0
	var buf bytes.Buffer

	dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior)

	// Repo B is enrolled between ticks (e.g. via `dispatch enroll` or the
	// board-driven flow from lazyboards#260).
	if _, err := EnrollRepo(path, RepoIdentity{Repo: "o/B", Dir: t.TempDir()}); err != nil {
		t.Fatalf("enrolling repo B: %v", err)
	}

	buf.Reset()
	dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior)

	// No gh seam exists for CollectTickets (by design, per the approved plan),
	// so the reload is observed via the deterministic gh collection-error log
	// line naming the newly-enrolled repo.
	if !strings.Contains(buf.String(), "o/B") {
		t.Errorf("expected the reloaded repo set (including o/B) to reach RunOnce, got log: %q", buf.String())
	}
}

// --- combinedTick (RunCombinedLoop's per-tick body) ---

func TestCombinedTickBadReloadSkipsTick(t *testing.T) {
	dir := t.TempDir()
	path := seedDispatchConfig(t, dir, "o/A")

	prior := 5
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 1)
	var buf bytes.Buffer
	ctx := context.Background()

	// Baseline tick against a valid config; drain whatever it pushes so it
	// can't be mistaken for (or block) the tick under test.
	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention)
	drainAttention(attention)
	buf.Reset()

	corruptConfig(t, path)

	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention)

	if prior != 5 {
		t.Errorf("prior quota tally changed on a skipped tick: got %d, want 5", prior)
	}
	if !strings.Contains(buf.String(), "config") {
		t.Errorf("expected the reload error to be logged to out, got %q", buf.String())
	}
	select {
	case got := <-attention:
		t.Errorf("expected no attention push on a skipped tick, got %v", got)
	default:
	}
	if len(mut.labelEdits) != 0 || len(mut.comments) != 0 {
		t.Errorf("expected no reconcile mutations on a skipped tick, got edits=%v comments=%v", mut.labelEdits, mut.comments)
	}
}

func TestCombinedTickReloadPicksUpEnrollment(t *testing.T) {
	dir := t.TempDir()
	path := seedDispatchConfig(t, dir, "o/A")

	prior := 0
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 1)
	var buf bytes.Buffer
	ctx := context.Background()

	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention)
	drainAttention(attention)
	buf.Reset()

	if _, err := EnrollRepo(path, RepoIdentity{Repo: "o/B", Dir: t.TempDir()}); err != nil {
		t.Fatalf("enrolling repo B: %v", err)
	}

	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention)

	if !strings.Contains(buf.String(), "o/B") {
		t.Errorf("expected the reloaded repo set (including o/B) to reach RunOnce/RunReconcileOnce, got log: %q", buf.String())
	}
	// A successful tick still pushes the (here: empty) failed-window overlay,
	// proving the tick ran through to completion rather than being skipped.
	select {
	case <-attention:
	default:
		t.Error("expected an attention push on a successful tick")
	}
}

// runControllerCompileCheck pins fakeController to the real run.Controller
// interface at compile time.
var _ run.Controller = fakeController{}
