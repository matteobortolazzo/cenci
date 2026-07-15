package dispatch

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v4/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v4/pkg/watch"
)

// stubRunFn swaps runFn for a stub for the duration of the test, so
// applyDispatch never reaches the real run.Run (which would ensure a daemon
// and spawn a tmux window from `go test`).
func stubRunFn(t *testing.T, fn func(run.Opts, run.Controller) error) {
	t.Helper()
	restore := runFn
	runFn = fn
	t.Cleanup(func() { runFn = restore })
}

// dispatchableDeps is the happy path: one Planned ticket #42 with an approved,
// fresh plan and a reachable, idle daemon — every gate passes.
func dispatchableDeps(now time.Time) dispatchDeps {
	return dispatchDeps{
		Tickets:  []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Planned"}}},
		Plans:    []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "approved"}},
		Snapshot: &watch.StateSnapshot{},
		Now:      now,
	}
}

func TestApplyDispatchClaimsWorkingLabelAfterSpawn(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return nil })

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	if _, err := applyDispatch(testConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	// Exactly one claim: the synchronous Working label must be applied once per
	// spawned ticket — a duplicate edit would be a real double-mutation bug.
	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit (the Working claim), got %d: %+v", len(mut.labelEdits), mut.labelEdits)
	}
	e := mut.labelEdits[0]
	if e.repo != "o/r" || e.number != 42 || !containsStr(e.add, labelWorking) || len(e.remove) != 0 {
		t.Errorf("unexpected claim edit: %+v, want add=[%s] remove=[] on o/r#42", e, labelWorking)
	}
	if prior != 1 {
		t.Errorf("prior = %d, want 1 (incremented per successful dispatch)", prior)
	}
}

func TestApplyDispatchDryRunSkipsClaim(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Error("dry-run must not spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0

	if _, err := applyDispatch(testConfig(), dispatchableDeps(now), fakeController{}, mut, true, nil, &prior); err != nil {
		t.Fatalf("dry-run returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 0 {
		t.Errorf("dry-run must not claim, got %+v", mut.labelEdits)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (dry-run must not consume quota)", prior)
	}
}

func TestApplyDispatchNoClaimOnSpawnFailure(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return errors.New("tmux exploded") })

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	_, err := applyDispatch(testConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior)
	if err == nil || !strings.Contains(err.Error(), "tmux exploded") {
		t.Fatalf("spawn failure error = %v, want tmux exploded", err)
	}

	if len(mut.labelEdits) != 0 {
		t.Errorf("a failed spawn must not claim the ticket, got %+v", mut.labelEdits)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (failed dispatch must not consume quota)", prior)
	}
	if !strings.Contains(buf.String(), "run failed") {
		t.Errorf("expected the spawn failure to be logged, got %q", buf.String())
	}
}

func TestApplyDispatchClaimFailureLogsAndContinues(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return nil })

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &failingMutator{}
	prior := 0
	var buf bytes.Buffer

	_, err := applyDispatch(testConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior)
	if err == nil || !strings.Contains(err.Error(), "label edit") {
		t.Fatalf("claim failure error = %v, want label edit error", err)
	}

	// The spawn already happened, so quota is consumed and the pass carries on;
	// the failed claim is only logged (reconcile recovers the label drift).
	if prior != 1 {
		t.Errorf("prior = %d, want 1 (spawn succeeded; claim failure must not roll back quota)", prior)
	}
	if !strings.Contains(buf.String(), "claim") {
		t.Errorf("expected the claim failure to be logged, got %q", buf.String())
	}
}

// failingMutator errors on every mutation, for the claim-failure path.
type failingMutator struct{}

func (failingMutator) EditLabels(string, int, []string, []string) error {
	return errors.New("gh label edit failed")
}

func (failingMutator) Comment(string, int, string) error {
	return errors.New("gh comment failed")
}

func (failingMutator) EnsureLabels(string, []string) error {
	return nil
}

// TestApplyDispatchPassesModelToRunOpts locks in that a pinned Config.Model
// (from config.json's "dispatch.model" or a --model CLI override) reaches
// every spawned session's run.Opts.Model, so a dispatch pass never depends on
// whatever ambient/account-level default model happens to be active.
func TestApplyDispatchPassesModelToRunOpts(t *testing.T) {
	var captured run.Opts
	stubRunFn(t, func(opts run.Opts, _ run.Controller) error {
		captured = opts
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	cfg := testConfig()
	cfg.Model = "claude-sonnet-5"

	if _, err := applyDispatch(cfg, dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	if captured.Model != "claude-sonnet-5" {
		t.Errorf("captured Opts.Model = %q, want %q", captured.Model, "claude-sonnet-5")
	}
	if !strings.Contains(buf.String(), "claude-sonnet-5") {
		t.Errorf("expected the resolved model to be logged for visibility, got %q", buf.String())
	}
}

// TestApplyDispatchOmitsModelWhenUnset locks in that an unset Config.Model
// leaves run.Opts.Model empty, preserving the existing fallback to
// agents.*.model inside run.BuildCommand — no behavior change for callers
// that never configure a pinned model.
func TestApplyDispatchOmitsModelWhenUnset(t *testing.T) {
	var captured run.Opts
	stubRunFn(t, func(opts run.Opts, _ run.Controller) error {
		captured = opts
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	if _, err := applyDispatch(testConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	if captured.Model != "" {
		t.Errorf("captured Opts.Model = %q, want empty", captured.Model)
	}
}

// TestFormatDecisionPrefixesRepo locks in the owner/repo prefix on decision
// lines so multi-repo fleet output is unambiguous, and keeps the ` skip:` /
// ` dispatch ` substrings intact — downstream consumers (lazyboards) classify
// lines by matching on them.
func TestFormatDecisionPrefixesRepo(t *testing.T) {
	skip := Decision{
		Ticket: Ticket{Repo: "o/r", Number: 45},
		Action: ActionSkip,
		Reason: "not Planned",
	}
	if got, want := formatDecision(skip), "o/r#45 skip: not Planned"; got != want {
		t.Errorf("skip line = %q, want %q", got, want)
	}

	dispatch := Decision{
		Ticket: Ticket{Repo: "o/r", Number: 78},
		Plan:   &Plan{Path: ".plans/78-add-cache.md"},
		Action: ActionDispatch,
		Reason: "dispatch",
		Agent:  "claude",
	}
	if got, want := formatDecision(dispatch), "o/r#78 dispatch (claude, 78-add-cache.md): dispatch"; got != want {
		t.Errorf("dispatch line = %q, want %q", got, want)
	}
}
