package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// Ticket #122: RunLoop and RunCombinedLoop must reload dispatch.Config from a
// configPath every tick (instead of once at startup), and must skip the whole
// tick — no dispatch, no reconcile, no attention push — on a reload error,
// preserving the in-memory quota tally. Per the approved plan, the per-tick
// body is extracted into unexported dispatchTick/combinedTick helpers so these
// tests can drive single ticks deterministically without a real time.Ticker.
//
// Ticket #220: the daemon always runs RunCombinedLoop's supervisor goroutine
// now, so combinedTick is rewritten into a dynamic-sleep per-tick body that
// takes a persistent *watch.DispatchState and returns the next sleep
// interval (loopCheckInterval on a disabled/bad-reload tick, the resolved
// cfg.DaemonInterval otherwise), publishing a start-of-tick and an
// end-of-tick watch.AttentionUpdate.Dispatch overlay on an enabled tick.

// fakeController is a no-op run.Controller. The reload tests never reach an
// actual dispatch call — CollectTickets fails first in this gh-less test
// environment — so every method here only needs to satisfy the interface.
type fakeController struct{}

func (fakeController) CurrentSession() (string, error)                    { return "", nil }
func (fakeController) IsGroupedSession(session string) (bool, error)      { return false, nil }
func (fakeController) NewWindow(session, name, shellCommand string) error { return nil }
func (fakeController) SetWindowOption(target, key, value string) error    { return nil }

// HasSession always reports the session as live (#927): the reload tests
// never construct a scenario meant to exercise the per-repo session gate, so
// a fixed (true, nil) keeps every existing fakeController-based test's
// dispatch decisions ungated by session configuration.
func (fakeController) HasSession(session string) (bool, error) { return true, nil }

// seedDispatchConfig writes a fresh config.json under dir enrolling repo, and
// returns its path. Using EnrollRepo (rather than hand-written JSON) exercises
// the same write path the `dispatch enroll` verb and board-driven enrollment
// use, per the ticket's live-enrollment scenario. It also enables the embedded
// loop (SetLoopEnabled) so combinedTick tests built on this helper exercise
// the enabled-tick path (#220) rather than the disabled short-circuit;
// dispatchTick ignores dispatch.loopEnabled entirely, so this is a no-op for
// dispatchTick tests sharing the helper.
func seedDispatchConfig(t *testing.T, dir, repo string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if _, _, err := EnrollRepo(path, RepoIdentity{Repo: repo, Dir: t.TempDir()}, ""); err != nil {
		t.Fatalf("seeding config with %s: %v", repo, err)
	}
	if err := SetLoopEnabled(path, true); err != nil {
		t.Fatalf("enabling loop for %s: %v", path, err)
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

// drainAttention non-blockingly empties ch of every pending value (an
// enabled combinedTick sends two per tick: a start-of-tick and an
// end-of-tick update, per #220), so a prior tick's push(es) cannot be
// mistaken for the tick under test, and cannot block a later tick's send
// against a full buffered channel.
func drainAttention(ch <-chan watch.AttentionUpdate) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// --- dispatchTick (RunLoop's per-tick body) ---

func TestDispatchTickBadReloadSkipsTick(t *testing.T) {
	dir := t.TempDir()
	path := seedDispatchConfig(t, dir, "o/A")

	prior := 3
	var buf bytes.Buffer

	// Baseline tick against a valid config.
	_ = dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior, "")
	buf.Reset()

	// Config goes bad between ticks.
	corruptConfig(t, path)

	if err := dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior, ""); err == nil {
		t.Error("expected bad reload to return an error")
	}

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

	_ = dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior, "")

	// Repo B is enrolled between ticks (e.g. via `dispatch enroll` or the
	// board-driven flow from lazyboards#260).
	if _, _, err := EnrollRepo(path, RepoIdentity{Repo: "o/B", Dir: t.TempDir()}, ""); err != nil {
		t.Fatalf("enrolling repo B: %v", err)
	}

	buf.Reset()
	_ = dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior, "")

	// No gh seam exists for CollectTickets (by design, per the approved plan),
	// so the reload is observed via the deterministic gh collection-error log
	// line naming the newly-enrolled repo.
	if !strings.Contains(buf.String(), "o/B") {
		t.Errorf("expected the reloaded repo set (including o/B) to reach RunOnce, got log: %q", buf.String())
	}
}

// TestDispatchTickModelOverrideWinsOverPersistedConfig locks in that a
// non-empty modelOverride (threaded from the --model CLI flag through RunLoop)
// beats whatever "dispatch.model" is persisted in config.json, and that the
// override is re-applied every tick even though dispatchTick reloads Config
// fresh from disk each time (so it can't be silently dropped after tick 1, the
// class of bug that motivated this field).
func TestDispatchTickModelOverrideWinsOverPersistedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	repoDir := t.TempDir()
	cfgJSON := fmt.Sprintf(`{"dispatch": {"model": "fable", "repos": [{"repo": "o/A", "dir": %q}]}}`, repoDir)
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	prior := 0
	var buf bytes.Buffer

	_ = dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior, "claude-sonnet-5")

	if !strings.Contains(buf.String(), "claude-sonnet-5") {
		t.Errorf("expected the --model override to be logged, got %q", buf.String())
	}
	if strings.Contains(buf.String(), "fable") {
		t.Errorf("expected the --model override to win over the persisted config model, got %q", buf.String())
	}

	// Second tick: dispatchTick reloads Config from disk again, which would
	// re-read "fable" if the override weren't re-applied every time.
	buf.Reset()
	_ = dispatchTick(path, fakeController{}, &fakeMutator{}, &buf, &prior, "claude-sonnet-5")
	if !strings.Contains(buf.String(), "claude-sonnet-5") {
		t.Errorf("expected the override to survive a config reload on tick 2, got %q", buf.String())
	}
}

// --- combinedTick (RunCombinedLoop's per-tick body) ---

func TestCombinedTickBadReloadSkipsTick(t *testing.T) {
	dir := t.TempDir()
	path := seedDispatchConfig(t, dir, "o/A")

	prior := 5
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

	// Baseline tick against a valid config; drain whatever it pushes so it
	// can't be mistaken for (or block) the tick under test.
	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)
	drainAttention(attention)
	buf.Reset()

	corruptConfig(t, path)

	got := combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)

	if got != loopCheckInterval {
		t.Errorf("combinedTick returned %v on a bad reload, want loopCheckInterval (%v)", got, loopCheckInterval)
	}
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
	attention := make(chan watch.AttentionUpdate, 2)
	var buf bytes.Buffer
	ctx := context.Background()
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64

	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)
	drainAttention(attention)
	buf.Reset()

	if _, _, err := EnrollRepo(path, RepoIdentity{Repo: "o/B", Dir: t.TempDir()}, ""); err != nil {
		t.Fatalf("enrolling repo B: %v", err)
	}

	got := combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)

	if got != 5*time.Minute {
		t.Errorf("combinedTick returned %v, want the configured 5m daemonInterval", got)
	}
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

// --- combinedTick enabled/disabled dispatch-state publishing (#220) ---

// TestCombinedTickDisabledPublishesClearedStateAndSkipsPasses locks in the
// disabled branch of the always-on supervisor loop: when dispatch.loopEnabled
// resolves false, combinedTick must publish exactly one update carrying
// Enabled:false, PassRunning:false, and the preserved DaemonRunning flag, with
// nil Windows/Headroom (clearing any prior failed-window badges and headroom
// within one interval), must not run RunOnce/RunReconcileOnce at all (no
// reconcile mutations), and must return loopCheckInterval.
func TestCombinedTickDisabledPublishesClearedStateAndSkipsPasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"dispatch": {"loopEnabled": false}}`)

	prior := 0
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 2)
	var buf bytes.Buffer
	ctx := context.Background()
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64

	got := combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)

	if got != loopCheckInterval {
		t.Errorf("combinedTick returned %v for a disabled loop, want loopCheckInterval (%v)", got, loopCheckInterval)
	}

	select {
	case update := <-attention:
		if update.Dispatch == nil {
			t.Fatal("expected a Dispatch update on a disabled tick, got nil")
		}
		if update.Dispatch.Enabled {
			t.Errorf("Dispatch.Enabled = true, want false")
		}
		if update.Dispatch.PassRunning {
			t.Errorf("Dispatch.PassRunning = true, want false")
		}
		if !update.Dispatch.DaemonRunning {
			t.Errorf("Dispatch.DaemonRunning = false, want true (preserved across the disabled branch)")
		}
		if update.Windows != nil {
			t.Errorf("Windows = %v, want nil on a disabled tick", update.Windows)
		}
		if update.Headroom != nil {
			t.Errorf("Headroom = %v, want nil on a disabled tick", update.Headroom)
		}
	default:
		t.Fatal("expected exactly one attention push on a disabled tick")
	}

	select {
	case got := <-attention:
		t.Errorf("expected exactly one attention push on a disabled tick, got a second: %+v", got)
	default:
	}

	if len(mut.labelEdits) != 0 || len(mut.comments) != 0 {
		t.Errorf("expected no reconcile mutations on a disabled tick, got edits=%v comments=%v", mut.labelEdits, mut.comments)
	}
}

// TestCombinedTickEnabledPublishesStartAndEndOfTick locks in the enabled
// branch's two-publish contract: a start-of-tick update with PassRunning:true
// and the prior run's fields intact, followed (after the pass runs) by an
// end-of-tick update with PassRunning:false and refreshed counts/LastRunAt.
// combinedTick must return the configured daemonInterval.
func TestCombinedTickEnabledPublishesStartAndEndOfTick(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// No repos configured, so RunOnce/RunReconcileOnce succeed with zero
	// dispatched/skipped tickets and no gh call at all (empty repos loop
	// body never executes).
	writeFile(t, path, `{"dispatch": {"loopEnabled": true, "daemonInterval": "5m", "repos": []}}`)

	prior := 0
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 2)
	var buf bytes.Buffer
	ctx := context.Background()
	state := watch.DispatchState{DaemonRunning: true, LastDispatched: 7, LastSkipped: 2}
	var windows []watch.WindowState
	var headroom map[string]float64

	got := combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)

	if got != 5*time.Minute {
		t.Errorf("combinedTick returned %v, want the configured 5m daemonInterval", got)
	}

	var start, end watch.AttentionUpdate
	select {
	case start = <-attention:
	default:
		t.Fatal("expected a start-of-tick attention push")
	}
	select {
	case end = <-attention:
	default:
		t.Fatal("expected an end-of-tick attention push")
	}

	if start.Dispatch == nil {
		t.Fatal("start-of-tick update missing Dispatch")
	}
	if !start.Dispatch.PassRunning {
		t.Errorf("start-of-tick PassRunning = false, want true")
	}
	if start.Dispatch.LastDispatched != 7 || start.Dispatch.LastSkipped != 2 {
		t.Errorf("start-of-tick counts = %+v, want the prior run's counts (7, 2) preserved", start.Dispatch)
	}

	if end.Dispatch == nil {
		t.Fatal("end-of-tick update missing Dispatch")
	}
	if end.Dispatch.PassRunning {
		t.Errorf("end-of-tick PassRunning = true, want false")
	}
	if end.Dispatch.LastRunAt == "" {
		t.Error("end-of-tick LastRunAt not set")
	}
	if end.Dispatch.LastDispatched != 0 || end.Dispatch.LastSkipped != 0 {
		t.Errorf("end-of-tick counts = %+v, want 0/0 (empty repos, nothing to dispatch/skip)", end.Dispatch)
	}
}

// TestCombinedTickLastErrorNotSticky locks in that DispatchState.LastError
// reflects only the most recent pass's outcome, not a sticky "ever failed"
// flag: a tick against a configured repo fails (CollectTickets shells out to
// gh, which is unavailable in this test env) and publishes a non-empty
// LastError, but a subsequent tick against an empty-repos config (which never
// shells out, so it succeeds) clears LastError back to "".
func TestCombinedTickLastErrorNotSticky(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"dispatch": {"loopEnabled": true, "daemonInterval": "5m", "repos": [{"repo": "o/A", "dir": "/tmp"}]}}`)

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
	<-attention // start-of-tick
	errored := <-attention

	if errored.Dispatch == nil || errored.Dispatch.LastError != "dispatch_pass_failed" {
		t.Fatalf("LastError = %+v, want dispatch_pass_failed", errored.Dispatch)
	}

	writeFile(t, path, `{"dispatch": {"loopEnabled": true, "daemonInterval": "5m", "repos": []}}`)

	combinedTick(ctx, path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)
	<-attention // start-of-tick
	cleared := <-attention

	if cleared.Dispatch == nil || cleared.Dispatch.LastError != "" {
		var got string
		if cleared.Dispatch != nil {
			got = cleared.Dispatch.LastError
		}
		t.Errorf("LastError = %q, want cleared back to \"\" on a subsequent successful tick", got)
	}
}

// runControllerCompileCheck pins fakeController to the real run.Controller
// interface at compile time.
var _ run.Controller = fakeController{}

// -- #1385: machine-local pickup filters reload every pass ---------------------

// filterLoop is a real-git, fake-gh fixture (setupFilterPass) driven through a
// real config file, so each pass reloads the filters from disk like production.
type filterLoop struct {
	path string
	dir  string
}

func newFilterLoop(t *testing.T, snap watch.StateSnapshot, issues ...filterPassIssue) filterLoop {
	t.Helper()
	cfg := setupFilterPass(t, snap, issues...)
	return filterLoop{path: filepath.Join(t.TempDir(), "config.json"), dir: cfg.Repos[0].Dir}
}

// write replaces the config file; filters is the raw JSON value of the repo's
// "filters" key, or "" for an entry without one.
func (f filterLoop) write(t *testing.T, filters string) {
	t.Helper()
	field := ""
	if filters != "" {
		field = `,"filters":` + filters
	}
	body := fmt.Sprintf(`{"dispatch":{"loopEnabled":true,"repos":[{"repo":"o/r","dir":%q,"session":"test-session"%s}]}}`, f.dir, field)
	writeFile(t, f.path, body)
}

const (
	filterMilestone3 = `{"milestones":[3]}`
	filterMilestone7 = `{"milestones":[7]}`
	// filterTypo is a misspelled key: it must fail the load, never read as
	// "unrestricted".
	filterTypo = `{"milestone":[3]}`
)

func assertFilterDispatched(t *testing.T, log string, number int) {
	t.Helper()
	if want := fmt.Sprintf("o/r#%d dispatch (", number); !strings.Contains(log, want) {
		t.Errorf("log lacks a dispatch line %q:\n%s", want, log)
	}
}

func assertFilterSkipped(t *testing.T, log, reason string) {
	t.Helper()
	if want := "o/r#42 skip: " + reason; !strings.Contains(log, want) {
		t.Errorf("log lacks the skip line %q:\n%s", want, log)
	}
}

func TestDispatchTickFilterEditChangesDecisionsOnNextPass(t *testing.T) {
	loop := newFilterLoop(t, watch.StateSnapshot{}, filterPassIssue{number: 42, milestone: 3})
	spawned := recordSpawns(t)
	prior := 0
	var buf bytes.Buffer

	loop.write(t, filterMilestone3)
	if err := dispatchTick(loop.path, fakeController{}, &fakeMutator{}, &buf, &prior, ""); err != nil {
		t.Fatalf("dispatchTick under milestone 3: %v\n%s", err, buf.String())
	}
	assertFilterDispatched(t, buf.String(), 42)
	if !reflect.DeepEqual(*spawned, []string{"42"}) || prior != 1 {
		t.Fatalf("spawned %v prior %d, want ticket 42 spawned once", *spawned, prior)
	}

	// The operator re-points this machine at milestone 7; no restart.
	loop.write(t, filterMilestone7)
	buf.Reset()
	if err := dispatchTick(loop.path, fakeController{}, &fakeMutator{}, &buf, &prior, ""); err != nil {
		t.Fatalf("dispatchTick under milestone 7: %v\n%s", err, buf.String())
	}
	assertFilterSkipped(t, buf.String(), "filter: milestone 3 not in [7]")
	if len(*spawned) != 1 || prior != 1 {
		t.Errorf("spawned %v prior %d after the edit, want no further dispatch", *spawned, prior)
	}
}

func TestDispatchTickFilterBadEditSkipsPassAndCorrectedFileDispatchesAgain(t *testing.T) {
	loop := newFilterLoop(t, watch.StateSnapshot{}, filterPassIssue{number: 42, milestone: 3})
	spawned := recordSpawns(t)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	loop.write(t, filterMilestone3)
	if err := dispatchTick(loop.path, fakeController{}, mut, &buf, &prior, ""); err != nil {
		t.Fatalf("baseline dispatchTick: %v\n%s", err, buf.String())
	}
	edits := len(mut.labelEdits)

	loop.write(t, filterTypo)
	buf.Reset()
	err := dispatchTick(loop.path, fakeController{}, mut, &buf, &prior, "")
	if !errors.Is(err, errConfigReload) {
		t.Fatalf("bad filter edit returned %v, want errConfigReload", err)
	}
	for _, want := range []string{"dispatch: loading config:", "filters.milestone"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "o/r#42") {
		t.Errorf("a skipped pass must record no decisions:\n%s", buf.String())
	}
	if len(*spawned) != 1 || prior != 1 || len(mut.labelEdits) != edits {
		t.Errorf("skipped pass changed state: spawned %v prior %d edits %d (was %d)", *spawned, prior, len(mut.labelEdits), edits)
	}

	loop.write(t, filterMilestone3)
	buf.Reset()
	if err := dispatchTick(loop.path, fakeController{}, mut, &buf, &prior, ""); err != nil {
		t.Fatalf("dispatchTick after the correction: %v\n%s", err, buf.String())
	}
	assertFilterDispatched(t, buf.String(), 42)
	if len(*spawned) != 2 || prior != 2 {
		t.Errorf("spawned %v prior %d after the correction, want dispatch restored", *spawned, prior)
	}
}

// syncBuffer is an io.Writer a test can poll while RunLoop writes to it from
// its own goroutine.
type syncBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	wake chan struct{}
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{wake: make(chan struct{}, 1)} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return n, err
}

func (b *syncBuffer) count(sub string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), sub)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitCount blocks until sub appears at least n times in the log.
func (b *syncBuffer) waitCount(t *testing.T, sub string, n int) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for b.count(sub) < n {
		select {
		case <-b.wake:
		case <-timeout.C:
			t.Fatalf("timed out waiting for %d x %q in the log:\n%s", n, sub, b.String())
		}
	}
}

// A bad reload skips the pass but does not end `cenci dispatch --interval`:
// RunLoop keeps ticking, and the corrected file dispatches on the next pass.
func TestDispatchTickFilterRunLoopKeepsTickingOnReloadError(t *testing.T) {
	loop := newFilterLoop(t, watch.StateSnapshot{}, filterPassIssue{number: 42, milestone: 3})
	stubRunFn(t, func(run.Opts, run.Controller) error { return nil })
	loop.write(t, filterTypo)

	out := newSyncBuffer()
	done := make(chan error, 1)
	go func() {
		done <- RunLoop(loop.path, fakeController{}, &fakeMutator{}, 5*time.Millisecond, out, "")
	}()

	// The initial tick and at least two interval ticks all hit the bad file.
	out.waitCount(t, "dispatch: loading config:", 3)
	select {
	case err := <-done:
		t.Fatalf("RunLoop returned %v on a bad reload, want it to keep looping", err)
	default:
	}
	if strings.Contains(out.String(), "o/r#42") {
		t.Errorf("a skipped pass must record no decisions:\n%s", out.String())
	}

	loop.write(t, filterMilestone3)
	out.waitCount(t, "o/r#42 dispatch (", 1)

	// End the loop deterministically before the fixtures are torn down: a gh
	// outage fails the next collection, which is fatal to RunLoop (only the
	// reload sentinel is tolerated), so the goroutine returns instead of
	// outliving the test.
	installFakeGHOnPath(t, "exit 1\n")
	select {
	case err := <-done:
		if err == nil || errors.Is(err, errConfigReload) {
			t.Errorf("RunLoop ended with %v, want the collection failure", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunLoop kept running after a failed collection, want it to stop")
	}
}

// Every other pass error stays fatal: only the reload sentinel is tolerated.
func TestDispatchTickFilterRunLoopStaysFatalOnPassError(t *testing.T) {
	path := seedDispatchConfig(t, t.TempDir(), "o/A")
	installFakeGH(t, "exit 1\n")

	err := RunLoop(path, fakeController{}, &fakeMutator{}, time.Hour, &bytes.Buffer{}, "")
	if err == nil {
		t.Fatal("RunLoop returned nil for a failed collection, want the error")
	}
	if errors.Is(err, errConfigReload) {
		t.Errorf("a collection failure must not read as a config reload error: %v", err)
	}
}

func TestCombinedTickFilterEditChangesDecisionsOnNextPass(t *testing.T) {
	loop := newFilterLoop(t, watch.StateSnapshot{}, filterPassIssue{number: 42, milestone: 3})
	spawned := recordSpawns(t)
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 2)
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64
	prior := 0
	var buf bytes.Buffer
	tick := func() time.Duration {
		defer drainAttention(attention)
		return combinedTick(context.Background(), loop.path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)
	}

	loop.write(t, filterMilestone3)
	tick()
	assertFilterDispatched(t, buf.String(), 42)
	if !reflect.DeepEqual(*spawned, []string{"42"}) || prior != 1 {
		t.Fatalf("spawned %v prior %d, want ticket 42 spawned once", *spawned, prior)
	}

	loop.write(t, filterMilestone7)
	buf.Reset()
	tick()
	assertFilterSkipped(t, buf.String(), "filter: milestone 3 not in [7]")
	if len(*spawned) != 1 || prior != 1 {
		t.Errorf("spawned %v prior %d after the edit, want no further dispatch", *spawned, prior)
	}
}

func TestCombinedTickFilterBadEditSkipsPassAndCorrectedFileDispatchesAgain(t *testing.T) {
	loop := newFilterLoop(t, watch.StateSnapshot{}, filterPassIssue{number: 42, milestone: 3})
	spawned := recordSpawns(t)
	mut := &fakeMutator{}
	store := &memStore{}
	attention := make(chan watch.AttentionUpdate, 2)
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64
	prior := 0
	var buf bytes.Buffer
	tick := func() time.Duration {
		return combinedTick(context.Background(), loop.path, fakeController{}, mut, &buf, &prior, store, attention, &state, &windows, &headroom)
	}

	loop.write(t, filterMilestone3)
	tick()
	drainAttention(attention)
	edits := len(mut.labelEdits)

	loop.write(t, filterTypo)
	buf.Reset()
	if got := tick(); got != loopCheckInterval {
		t.Errorf("combinedTick returned %v on a bad filter edit, want loopCheckInterval (%v)", got, loopCheckInterval)
	}
	for _, want := range []string{"dispatch: loading config:", "filters.milestone"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "o/r#42") {
		t.Errorf("a skipped pass must record no decisions:\n%s", buf.String())
	}
	select {
	case got := <-attention:
		t.Errorf("a skipped pass must push no attention update, got %+v", got)
	default:
	}
	if len(*spawned) != 1 || prior != 1 || len(mut.labelEdits) != edits || len(mut.comments) != 0 {
		t.Errorf("skipped pass changed state: spawned %v prior %d edits %d (was %d) comments %v",
			*spawned, prior, len(mut.labelEdits), edits, mut.comments)
	}

	loop.write(t, filterMilestone3)
	buf.Reset()
	tick()
	drainAttention(attention)
	assertFilterDispatched(t, buf.String(), 42)
	if len(*spawned) != 2 || prior != 2 {
		t.Errorf("spawned %v prior %d after the correction, want dispatch restored", *spawned, prior)
	}
}

// windowCallRecorder records every window mutation a pass issues through the
// run.Controller seam (NewWindow and SetWindowOption), so a test can prove a
// pass left the windows alone. run.Controller exposes no kill/close method, so a
// kill is unreachable from dispatch by construction; creating or re-styling a
// window is the only window effect dispatch has.
type windowCallRecorder struct {
	fakeController
	calls []string
}

func (r *windowCallRecorder) NewWindow(session, name, shellCommand string) error {
	r.calls = append(r.calls, "NewWindow "+session+" "+name)
	return nil
}

func (r *windowCallRecorder) SetWindowOption(target, key, value string) error {
	r.calls = append(r.calls, "SetWindowOption "+target+" "+key)
	return nil
}

// Tightening the filters while a session runs leaves the session alone: the
// window is not touched, the ticket's labels and comments are not edited, and
// the persisted retry state is exactly what it was before the edit. The ticket
// only stops being a candidate for new pickup. Babysit supervision is armed only
// from a spawned run, never from dispatch, so failOnSpawn is the observable proof
// that nothing was armed or re-armed.
func TestCombinedTickFilterTighteningLeavesRunningSessionUntouched(t *testing.T) {
	running := watch.StateSnapshot{Windows: []watch.WindowState{{WindowName: "42-implement", Status: "running"}}}
	loop := newFilterLoop(t, running)
	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Ticket 42","labels":[{"name":"Working"}],"assignees":[{"login":"octocat"}],"milestone":{"number":7,"title":"v7"}}]' ;;
  "issue view") printf '{"comments":[]}' ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  "api user") printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`)
	stubOOMProbe(t)
	failOnSpawn(t)

	mut := &fakeMutator{}
	ctl := &windowCallRecorder{}
	store := &memStore{state: ReconcileState{
		Observations: map[string]time.Time{"o/r#42": time.Now().Add(-2 * time.Minute)},
	}}
	attention := make(chan watch.AttentionUpdate, 2)
	state := watch.DispatchState{DaemonRunning: true}
	var windows []watch.WindowState
	var headroom map[string]float64
	prior := 0
	var buf bytes.Buffer
	tick := func() {
		combinedTick(context.Background(), loop.path, ctl, mut, &buf, &prior, store, attention, &state, &windows, &headroom)
		drainAttention(attention)
	}

	loop.write(t, filterMilestone7)
	tick()
	before := store.state

	loop.write(t, filterMilestone3)
	buf.Reset()
	tick()

	assertFilterSkipped(t, buf.String(), "filter: milestone 7 not in [3]")
	if len(mut.labelEdits) != 0 || len(mut.comments) != 0 || len(mut.ensureCalls) != 0 {
		t.Errorf("tightening must not touch the running ticket: edits %+v comments %+v ensures %+v", mut.labelEdits, mut.comments, mut.ensureCalls)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0", prior)
	}
	if !reflect.DeepEqual(store.state, before) {
		t.Errorf("retry state changed by the edit:\n before %+v\n after  %+v", before, store.state)
	}
	if len(windows) != 0 {
		t.Errorf("failed-window overlay = %+v, want none for a live session", windows)
	}
	if len(ctl.calls) != 0 {
		t.Errorf("tightening must not create, re-style or touch any window: %q", ctl.calls)
	}
}
