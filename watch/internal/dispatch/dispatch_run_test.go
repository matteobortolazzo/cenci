package dispatch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/run"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
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

// dispatchableDeps is the happy path: one Planned ticket #42 with a planned,
// fresh plan and a reachable, idle daemon — every gate passes.
func dispatchableDeps(now time.Time) dispatchDeps {
	return dispatchDeps{
		Tickets:     []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Planned"}, Assignees: []string{"octocat"}}},
		Plans:       []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "planned"}},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
	}
}

func TestRunOnceFailsClosedWithoutGitHubIdentity(t *testing.T) {
	installFakeGH(t, `
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Fix thing","labels":[{"name":"Planned"}],"assignees":[{"login":"octocat"}]}]' ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  "api user") printf 'not authenticated\n' >&2; exit 1 ;;
  *) exit 1 ;;
esac
`)
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("identity failure must prevent every spawn")
		return nil
	})

	var buf bytes.Buffer
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: t.TempDir()}}
	decisions, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil)
	if err == nil || !strings.Contains(err.Error(), "detecting current GitHub user") {
		t.Fatalf("error = %v, want current-user detection failure", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
	if !strings.Contains(buf.String(), "not authenticated") {
		t.Errorf("log = %q, want gh diagnostic", buf.String())
	}
}

func TestRunOnceEmptyPassDoesNotRequireGitHubIdentity(t *testing.T) {
	installFakeGH(t, "printf 'identity must not be requested\\n' >&2\nexit 1\n")
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("empty pass must not spawn")
		return nil
	})

	decisions, err := RunOnce(testConfig(), fakeController{}, &fakeMutator{}, false, nil, nil)
	if err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
}

// TestBuildBudgetProviderOpenCodeFallsBackToUnlimited is a #488
// regression/confirmation test: buildBudgetProvider's per-agent TokenReader
// switch has no "opencode" case (and none is added by this ticket — real
// per-agent usage accounting for OpenCode is future work), so an operator who
// configures agentLimits for "opencode" gets a Budget with no reader wired
// in. UsageProvider.Budget already degrades that to Unlimited (absent a
// floor), so this is expected to pass without any dispatch.go changes — it
// pins "missing budget accounting degrades honestly rather than blocking
// dispatch" (#488 acceptance criteria) specifically for opencode.
func TestBuildBudgetProviderOpenCodeFallsBackToUnlimited(t *testing.T) {
	cfg := testConfig()
	cfg.AgentLimits = map[string]AgentLimit{
		"opencode": {FiveHourTokens: 10000, WeeklyTokens: 100000},
	}

	provider := buildBudgetProvider(cfg, time.Now())
	b := provider.Budget("opencode")
	if !b.Unlimited {
		t.Errorf("expected opencode budget Unlimited (no TokenReader wired for it), got %+v", b)
	}

	// Headroom() must omit opencode entirely (no reader configured), matching
	// UsageProvider's documented "omitted, not reported as unlimited" contract.
	up, ok := provider.(*UsageProvider)
	if !ok {
		t.Fatalf("buildBudgetProvider with non-empty AgentLimits must return *UsageProvider, got %T", provider)
	}
	if _, ok := up.Headroom()["opencode"]; ok {
		t.Error("expected opencode omitted from Headroom() (no reader configured)")
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

// resumeDispatchDeps is the resume happy path (#827): one Input Needed
// ticket #42 with an awaiting-input draft, an Answered probe, and a
// reachable, idle daemon -- every gate passes and Decide returns Resume=true.
func resumeDispatchDeps(now time.Time) dispatchDeps {
	return dispatchDeps{
		Tickets:     []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Input Needed"}, Assignees: []string{"octocat"}}},
		Plans:       []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "awaiting-input"}},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
		Answers:     map[string]AnswerProbe{"o/r#42": AnswerProbeAnswered},
	}
}

// orderRecordingMutator records EditLabels calls into callOrder, alongside
// runFn's own "spawn" entry (appended directly by the test's stub, which
// captures mut by closure), so a test can assert the swap-before-spawn
// ordering across both seams without depending on either's internals --
// mirrors fakeMutator/escalatingMutator's callOrder convention
// (reconcile_run_test.go). failEditLabels simulates the resume swap's gh
// call failing.
type orderRecordingMutator struct {
	callOrder      []string
	edits          []labelEdit
	failEditLabels bool
	// failRollback, when set, fails only the SECOND EditLabels call (the
	// post-spawn-failure rollback) while the first (the pre-spawn claim
	// swap) still succeeds -- unlike failEditLabels, which fails every call
	// including the first (#853).
	failRollback bool
}

func (m *orderRecordingMutator) EditLabels(repo string, number int, add, remove []string) error {
	if m.failEditLabels {
		m.callOrder = append(m.callOrder, "edit-fail")
		return errors.New("swap failed")
	}
	if m.failRollback && len(m.edits) == 1 {
		m.callOrder = append(m.callOrder, "rollback-fail")
		return errors.New("rollback failed")
	}
	m.edits = append(m.edits, labelEdit{repo, number, add, remove})
	m.callOrder = append(m.callOrder, "edit")
	return nil
}

func (m *orderRecordingMutator) Comment(string, int, string) error { return nil }

func (m *orderRecordingMutator) EnsureLabels(string, []string) error { return nil }

// TestApplyDispatchResumeSwapsLabelBeforeSpawn covers the Test Strategy
// table's swap-before-spawn ordering assertion: on a resume dispatch, the
// Input Needed -> Working label swap must land BEFORE the spawn, exactly
// once, with add=[Working] remove=[Input Needed] -- and prior increments
// exactly as an ordinary dispatch would.
func TestApplyDispatchResumeSwapsLabelBeforeSpawn(t *testing.T) {
	mut := &orderRecordingMutator{}
	stubRunFn(t, func(run.Opts, run.Controller) error {
		mut.callOrder = append(mut.callOrder, "spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	prior := 0

	if _, err := applyDispatch(testConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, nil, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	want := []string{"edit", "spawn"}
	if len(mut.callOrder) != len(want) || mut.callOrder[0] != want[0] || mut.callOrder[1] != want[1] {
		t.Fatalf("call order = %v, want %v (the label swap must land before the spawn)", mut.callOrder, want)
	}
	if len(mut.edits) != 1 {
		t.Fatalf("expected exactly 1 label edit (the resume swap), got %d: %+v", len(mut.edits), mut.edits)
	}
	e := mut.edits[0]
	if e.repo != "o/r" || e.number != 42 || !containsStr(e.add, labelWorking) || !containsStr(e.remove, labelInputNeeded) {
		t.Errorf("unexpected resume swap edit: %+v, want add=[%s] remove=[%s] on o/r#42", e, labelWorking, labelInputNeeded)
	}
	if prior != 1 {
		t.Errorf("prior = %d, want 1 (incremented per successful resume dispatch)", prior)
	}
}

// TestApplyDispatchResumeSwapFailurePreventsSpawnAndClaim covers: a failed
// pre-spawn swap must never spawn, must never increment prior, and must
// surface the error -- unlike the ordinary path's post-spawn claim failure
// (which is logged-and-continue, since the spawn already happened there).
func TestApplyDispatchResumeSwapFailurePreventsSpawnAndClaim(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a failed resume swap must never spawn")
		return nil
	})

	mut := &orderRecordingMutator{failEditLabels: true}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	prior := 0
	var buf bytes.Buffer

	_, err := applyDispatch(testConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
	if err == nil || !strings.Contains(err.Error(), "swap failed") {
		t.Fatalf("error = %v, want the swap failure surfaced", err)
	}
	if !strings.Contains(buf.String(), "resume claim failed") {
		t.Errorf("expected the resume claim failure to be logged, got %q", buf.String())
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (a failed swap must never spawn or consume quota)", prior)
	}
}

// TestApplyDispatchResumeSpawnFailureAfterGoodSwapDoesNotDoubleEdit covers: a
// spawn failure that follows a successful pre-spawn swap must, on an
// ordinary (non-ErrWindowSpawned) failure, roll the ticket back to Input
// Needed -- exactly one further edit, and that edit must be a rollback
// (+Input Needed -Working), never a second claim (+Working again). The
// "never claims twice" intent from before #853 survives as "the second edit
// is a rollback, not a claim" (#853).
func TestApplyDispatchResumeSpawnFailureAfterGoodSwapDoesNotDoubleEdit(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return errors.New("tmux exploded") })

	mut := &orderRecordingMutator{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	prior := 0
	var buf bytes.Buffer

	_, err := applyDispatch(testConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
	if err == nil || !strings.Contains(err.Error(), "tmux exploded") {
		t.Fatalf("error = %v, want tmux exploded", err)
	}
	if len(mut.edits) != 2 {
		t.Fatalf("expected exactly 2 label edits (the pre-spawn claim swap, then a rollback), got %d: %+v", len(mut.edits), mut.edits)
	}
	claim := mut.edits[0]
	if !containsStr(claim.add, labelWorking) || !containsStr(claim.remove, labelInputNeeded) {
		t.Errorf("unexpected first edit (claim swap): %+v, want add=[%s] remove=[%s]", claim, labelWorking, labelInputNeeded)
	}
	rollback := mut.edits[1]
	if rollback.repo != "o/r" || rollback.number != 42 || !containsStr(rollback.add, labelInputNeeded) || !containsStr(rollback.remove, labelWorking) {
		t.Errorf("unexpected second edit: %+v, want add=[%s] remove=[%s] on o/r#42 (a rollback, not a second claim)", rollback, labelInputNeeded, labelWorking)
	}
	if containsStr(rollback.add, labelWorking) {
		t.Errorf("the rollback edit must never re-add Working (that would be a second claim): %+v", rollback)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (a failed spawn must not consume quota)", prior)
	}
}

// TestApplyDispatchResumeSpawnFailureWithErrWindowSpawned_RetainsWorkingNoRollback
// covers Q1's confirmed-alive launch evidence: when the spawn failure is
// wrapped with run.ErrWindowSpawned (the tmux window was demonstrably
// created before the failure, e.g. a post-NewWindow SetWindowOption error),
// Working must be retained -- no rollback edit -- since the reconciler's
// interrupted-resume recovery is the backstop if the session turns out to be
// dead.
func TestApplyDispatchResumeSpawnFailureWithErrWindowSpawned_RetainsWorkingNoRollback(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		return fmt.Errorf("setting automatic-rename off: %w", run.ErrWindowSpawned)
	})

	mut := &orderRecordingMutator{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	prior := 0
	var buf bytes.Buffer

	_, err := applyDispatch(testConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
	if err == nil || !errors.Is(err, run.ErrWindowSpawned) {
		t.Fatalf("error = %v, want errors.Is(_, run.ErrWindowSpawned)", err)
	}
	if len(mut.edits) != 1 {
		t.Fatalf("expected exactly 1 label edit (the claim swap only, no rollback -- the window was demonstrably created), got %d: %+v", len(mut.edits), mut.edits)
	}
	claim := mut.edits[0]
	if !containsStr(claim.add, labelWorking) || !containsStr(claim.remove, labelInputNeeded) {
		t.Errorf("unexpected edit: %+v, want the claim swap retained (add=[%s] remove=[%s])", claim, labelWorking, labelInputNeeded)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (a failed spawn must not consume quota even when Working is retained)", prior)
	}
}

// TestApplyDispatchResumeSpawnFailureRollbackAlsoFails_QuotaUntouchedErrorSurfaced
// covers the rollback-failure case: the spawn fails (ordinary failure, not
// ErrWindowSpawned), and the rollback edit itself also fails. Quota (prior)
// must stay untouched and the error must surface -- both the spawn and
// rollback failures are the caller's problem, never silently swallowed.
func TestApplyDispatchResumeSpawnFailureRollbackAlsoFails_QuotaUntouchedErrorSurfaced(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return errors.New("tmux exploded") })

	mut := &orderRecordingMutator{failRollback: true}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	prior := 0
	var buf bytes.Buffer

	_, err := applyDispatch(testConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
	if err == nil {
		t.Fatal("expected an error when both the spawn and the rollback fail")
	}
	if !strings.Contains(err.Error(), "tmux exploded") && !strings.Contains(err.Error(), "rollback failed") {
		t.Errorf("error = %v, want it to surface the spawn or rollback failure", err)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (a failed spawn must not consume quota, regardless of rollback outcome)", prior)
	}
	// Only the pre-spawn claim swap actually landed; the rollback attempt
	// itself failed, so it never lands in edits.
	if len(mut.edits) != 1 {
		t.Errorf("expected exactly 1 successful edit (the claim swap; the rollback failed), got %d: %+v", len(mut.edits), mut.edits)
	}
	if !strings.Contains(buf.String(), "rollback") {
		t.Errorf("expected the rollback failure to be logged, got %q", buf.String())
	}
}

// TestApplyDispatchResumeDryRunSkipsSwapButPrintsTable covers: --dry-run must
// never swap labels or spawn, but the resume decision line must still print.
func TestApplyDispatchResumeDryRunSkipsSwapButPrintsTable(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Error("dry-run must not spawn")
		return nil
	})

	mut := &orderRecordingMutator{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	prior := 0
	var buf bytes.Buffer

	if _, err := applyDispatch(testConfig(), resumeDispatchDeps(now), fakeController{}, mut, true, &buf, &prior); err != nil {
		t.Fatalf("dry-run returned unexpected error: %v", err)
	}
	if len(mut.edits) != 0 {
		t.Errorf("dry-run must not swap labels, got %+v", mut.edits)
	}
	if !strings.Contains(buf.String(), "resume — human answered") {
		t.Errorf("expected the resume decision line to still be printed, got %q", buf.String())
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

// TestApplyDispatchSetsWindowTicketToTicketNumber locks the join-key contract:
// dispatch passes the plan-file path as run.Opts.Ticket (the implement skill's
// argument) but must set WindowTicket to the numeric issue number so run.Run
// names the window `<number>-implement` — the shape Lazyboards and dispatch's
// own ticketActive/reconcile matching join on. Without WindowTicket the plan
// path (non-numeric) would slug into a name nothing can match.
func TestApplyDispatchSetsWindowTicketToTicketNumber(t *testing.T) {
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

	if captured.WindowTicket != "42" {
		t.Errorf("captured Opts.WindowTicket = %q, want %q (the numeric issue number)", captured.WindowTicket, "42")
	}
	if captured.Ticket != ".plans/42-x.md" {
		t.Errorf("captured Opts.Ticket = %q, want the plan path %q (the skill argument)", captured.Ticket, ".plans/42-x.md")
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

// TestFormatDecisionRendersResumeLine covers the Test Strategy table's
// formatDecision resume-line assertion (#827): the exact rendered line, and
// that it still carries the load-bearing ` dispatch ` substring downstream
// consumers (lazyboards) match on -- Resume must never perturb that
// contract.
func TestFormatDecisionRendersResumeLine(t *testing.T) {
	d := Decision{
		Ticket: Ticket{Repo: "o/r", Number: 42},
		Plan:   &Plan{Path: ".plans/42-slug.md"},
		Action: ActionDispatch,
		Resume: true,
		Reason: "resume — human answered",
		Agent:  "claude",
	}
	got := formatDecision(d)
	want := "o/r#42 dispatch (claude, 42-slug.md): resume — human answered"
	if got != want {
		t.Errorf("resume line = %q, want %q", got, want)
	}
	if !strings.Contains(got, " dispatch ") {
		t.Errorf("resume line %q must still carry the load-bearing %q substring", got, " dispatch ")
	}
}

// -- #828: stage-aware Refined pickup and autonomous re-plan ----------------

// planningDispatchDeps is the planning-pickup happy path: one Refined ticket
// #42 with no plan file and a reachable, idle daemon. RepoAutonomy grants
// "o/r" lean (#851): dispatch.planRefined alone is no longer sufficient to
// authorize an unattended planning dispatch.
func planningDispatchDeps(now time.Time) dispatchDeps {
	return dispatchDeps{
		Tickets:      []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Refined"}, Assignees: []string{"octocat"}}},
		Snapshot:     &watch.StateSnapshot{},
		Now:          now,
		CurrentUser:  "octocat",
		RepoAutonomy: leanAutonomy("o/r"),
	}
}

// replanDispatchDeps is the autonomous re-plan happy path: one Planned
// ticket #42 whose plan is stale beyond testConfig's tolerance. RepoAutonomy
// grants "o/r" lean (#851), matching planningDispatchDeps above.
func replanDispatchDeps(now time.Time) dispatchDeps {
	return dispatchDeps{
		Tickets:      []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Planned"}, Assignees: []string{"octocat"}}},
		Plans:        []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "planned", CommitsBehind: 10}},
		Snapshot:     &watch.StateSnapshot{},
		Now:          now,
		CurrentUser:  "octocat",
		RepoAutonomy: leanAutonomy("o/r"),
	}
}

// TestApplyDispatchPlanningPickupLaunchArgs covers the Test Strategy table's
// launch-shape requirement: runFn receives Ticket == "42" (the bare ticket
// number, no plan file) and WindowTicket == "42" for a planning pickup.
func TestApplyDispatchPlanningPickupLaunchArgs(t *testing.T) {
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
	cfg.PlanRefined = true

	if _, err := applyDispatch(cfg, planningDispatchDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	if captured.Ticket != "42" {
		t.Errorf("captured Opts.Ticket = %q, want %q (bare ticket number, no plan file)", captured.Ticket, "42")
	}
	if captured.WindowTicket != "42" {
		t.Errorf("captured Opts.WindowTicket = %q, want %q", captured.WindowTicket, "42")
	}
}

// TestApplyDispatchReplanLaunchArgs covers the Test Strategy table's
// launch-shape requirement: runFn receives Ticket == "42 replan" (the
// --replan-requested escape hatch) and WindowTicket == "42" for a re-plan.
func TestApplyDispatchReplanLaunchArgs(t *testing.T) {
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
	cfg.PlanRefined = true

	if _, err := applyDispatch(cfg, replanDispatchDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	if captured.Ticket != "42 replan" {
		t.Errorf("captured Opts.Ticket = %q, want %q (the --replan-requested escape hatch)", captured.Ticket, "42 replan")
	}
	if captured.WindowTicket != "42" {
		t.Errorf("captured Opts.WindowTicket = %q, want %q (bare number, always)", captured.WindowTicket, "42")
	}
}

// TestApplyDispatchPlanningPickupThreadsModelAndSession locks in that
// Model/Session are threaded to a planning dispatch identically to an
// ordinary dispatch.
func TestApplyDispatchPlanningPickupThreadsModelAndSession(t *testing.T) {
	var captured run.Opts
	stubRunFn(t, func(opts run.Opts, _ run.Controller) error {
		captured = opts
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0

	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.Model = "claude-sonnet-5"
	cfg.Session = "cenci"

	if _, err := applyDispatch(cfg, planningDispatchDeps(now), fakeController{}, mut, false, nil, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}
	if captured.Model != "claude-sonnet-5" || captured.Session != "cenci" {
		t.Errorf("Model/Session = %q/%q, want threaded identically to an ordinary dispatch", captured.Model, captured.Session)
	}
}

// TestApplyDispatchPlanningPickupClaimsWorkingAfterSpawn covers the Test
// Strategy table's "Working added after a successful spawn for both kinds"
// requirement for the planning-pickup kind.
func TestApplyDispatchPlanningPickupClaimsWorkingAfterSpawn(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return nil })

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	cfg := testConfig()
	cfg.PlanRefined = true

	if _, err := applyDispatch(cfg, planningDispatchDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit (the Working claim), got %d: %+v", len(mut.labelEdits), mut.labelEdits)
	}
	e := mut.labelEdits[0]
	if e.repo != "o/r" || e.number != 42 || !containsStr(e.add, labelWorking) || len(e.remove) != 0 {
		t.Errorf("unexpected claim edit: %+v, want add=[%s] remove=[] on o/r#42", e, labelWorking)
	}
}

// TestApplyDispatchReplanClaimsWorkingAfterSpawn covers the Test Strategy
// table's "Working added after a successful spawn for both kinds"
// requirement for the re-plan kind.
func TestApplyDispatchReplanClaimsWorkingAfterSpawn(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return nil })

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	cfg := testConfig()
	cfg.PlanRefined = true

	if _, err := applyDispatch(cfg, replanDispatchDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	if len(mut.labelEdits) != 1 {
		t.Fatalf("expected 1 label edit (the Working claim), got %d: %+v", len(mut.labelEdits), mut.labelEdits)
	}
	e := mut.labelEdits[0]
	if e.repo != "o/r" || e.number != 42 || !containsStr(e.add, labelWorking) || len(e.remove) != 0 {
		t.Errorf("unexpected claim edit: %+v, want add=[%s] remove=[] on o/r#42", e, labelWorking)
	}
}

// TestApplyDispatchPlanningPickupNoClaimOnSpawnFailure covers the Test
// Strategy table's "no claim on spawn failure" requirement for the
// planning-pickup kind.
func TestApplyDispatchPlanningPickupNoClaimOnSpawnFailure(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return errors.New("tmux exploded") })

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	cfg := testConfig()
	cfg.PlanRefined = true

	_, err := applyDispatch(cfg, planningDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
	if err == nil || !strings.Contains(err.Error(), "tmux exploded") {
		t.Fatalf("spawn failure error = %v, want tmux exploded", err)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("a failed spawn must not claim the ticket, got %+v", mut.labelEdits)
	}
}

// TestApplyDispatchPlanningPickupDryRunSkipsClaim covers the Test Strategy
// table's "no claim on dry-run" requirement for the planning-pickup kind.
func TestApplyDispatchPlanningPickupDryRunSkipsClaim(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Error("dry-run must not spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0

	cfg := testConfig()
	cfg.PlanRefined = true

	if _, err := applyDispatch(cfg, planningDispatchDeps(now), fakeController{}, mut, true, nil, &prior); err != nil {
		t.Fatalf("dry-run returned unexpected error: %v", err)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("dry-run must not claim, got %+v", mut.labelEdits)
	}
}

// TestFormatDecisionRendersPlanningPickupLine covers the Test Strategy
// table's formatDecision justification: a Plan == nil dispatch (the
// planning-pickup kind) must render with the load-bearing " dispatch "
// substring, not silently fall into the " skip:" branch.
func TestFormatDecisionRendersPlanningPickupLine(t *testing.T) {
	d := Decision{
		Ticket:   Ticket{Repo: "o/r", Number: 42},
		Action:   ActionDispatch,
		Planning: true,
		Reason:   "plan — Refined, no plan file",
		Agent:    "claude",
	}
	got := formatDecision(d)
	if !strings.Contains(got, " dispatch ") {
		t.Errorf("planning-pickup line %q must carry the load-bearing %q substring, not render as a skip", got, " dispatch ")
	}
	if strings.Contains(got, "skip:") {
		t.Errorf("planning-pickup line %q must never render as a skip (today's Plan == nil branch falls through to skip:)", got)
	}
}

// TestFormatDecisionRendersReplanLine covers the re-plan variant of the same
// contract: Plan != nil, Planning && Replan both true, still carries the
// load-bearing " dispatch " substring.
func TestFormatDecisionRendersReplanLine(t *testing.T) {
	d := Decision{
		Ticket:   Ticket{Repo: "o/r", Number: 42},
		Plan:     &Plan{Path: ".plans/42-x.md"},
		Action:   ActionDispatch,
		Planning: true,
		Replan:   true,
		Reason:   "re-plan — plan stale",
		Agent:    "claude",
	}
	got := formatDecision(d)
	if !strings.Contains(got, " dispatch ") {
		t.Errorf("re-plan line %q must carry the load-bearing %q substring", got, " dispatch ")
	}
}

// -- #851: dispatchDeps.RepoAutonomy wiring end-to-end through RunOnce, and
// the dry-run/real-pass parity tests at the readPlansForRepos /
// probeRepoAutonomies seams -------------------------------------------------

// runOnceFakeGHWithIdentity returns a fake gh script serving one open issue
// (numbered 42, the given labels, assigned to "octocat") plus the "api user"
// identity call RunOnce needs whenever it collects a non-empty ticket set.
func runOnceFakeGHWithIdentity(labelsJSON string) string {
	return `
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Fix thing","labels":[` + labelsJSON + `],"assignees":[{"login":"octocat"}]}]' ;;
  "api graphql") printf '` + emptyOpenPRPageJSON + `' ;;
  "api user") printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`
}

// isolateDaemonSocket redirects socket resolution to an empty temp dir
// (watch/docs/test-isolation.md's "ambient daemon socket isolation" rule),
// so RunOnce's ReadSnapshot deterministically fails (nil snapshot) rather
// than dialing a real daemon that might happen to be running on the
// dev/CI machine.
func isolateDaemonSocket(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

// TestRunOnce_InteractiveRepoConfigDeniesPlanningPickup covers the
// dispatchDeps.RepoAutonomy wiring end-to-end through RunOnce (#851): a real
// enrolled repo whose committed .cenci/config.json is NOT lean must deny a
// Refined planning pickup with the autonomy-specific reason -- proving
// RunOnce's probeRepoAutonomies -> dispatchDeps.RepoAutonomy -> Decide
// plumbing is actually wired, not merely present on the struct. This
// terminates before RunOnce's daemon-reachability check (rule 0's autonomy
// gate for a planning candidate is evaluated first), so it needs no fake
// daemon.
func TestRunOnce_InteractiveRepoConfigDeniesPlanningPickup(t *testing.T) {
	mainSyncGitEnv(t)
	isolateDaemonSocket(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, interactiveConfigJSON)

	installFakeGHOnPath(t, runOnceFakeGHWithIdentity(`{"name":"Refined"}`))
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an autonomy-denied repo must never spawn")
		return nil
	})

	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local}}

	var buf bytes.Buffer
	if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}

	log := buf.String()
	if !strings.Contains(log, "repo autonomy not lean") {
		t.Errorf("expected the interactive-config denial reason logged, got %q", log)
	}
	if strings.Contains(log, "not Planned") {
		t.Errorf("must be denied via the autonomy gate specifically, not fall through to \"not Planned\", got %q", log)
	}
}

// TestRunOnce_LeanRepoConfigPassesAutonomyGate is the positive counterpart:
// a real enrolled repo whose committed config IS lean must NOT be denied by
// the autonomy gate. No fake daemon is wired up (isolateDaemonSocket), so
// the decision terminates at the next gate down the chain
// ("daemon unreachable") -- proving autonomy passed without requiring a full
// dispatch to actually fire in this test environment.
func TestRunOnce_LeanRepoConfigPassesAutonomyGate(t *testing.T) {
	mainSyncGitEnv(t)
	isolateDaemonSocket(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, leanConfigJSON)

	installFakeGHOnPath(t, runOnceFakeGHWithIdentity(`{"name":"Refined"}`))
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("no live daemon is wired up in this test; a spawn here would mean a snapshot leaked in unexpectedly")
		return nil
	})

	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local}}

	var buf bytes.Buffer
	if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}

	log := buf.String()
	if strings.Contains(log, "repo autonomy not lean") {
		t.Errorf("a lean config must never be denied by the autonomy gate, got %q", log)
	}
	if !strings.Contains(log, "daemon unreachable") {
		t.Errorf("expected the pass to fall through to the daemon-unreachable gate (proving autonomy passed), got %q", log)
	}
}

// TestReadPlansForRepos_DryRunCommitsBehindMatchesPostFastForwardRealPass
// covers the plan's Implementation Order step 8 dry-run parity test at the
// readPlansForRepos seam: origin ahead by N commits past a plan's
// planCommitSha. Dry-run's CommitsBehind (computed against the fetched
// origin/main blob, FreshRef=="origin/main") must equal the post-fast-forward
// real pass's CommitsBehind (computed against local HEAD, FreshRef=="HEAD"),
// dry-run must leave local HEAD unmoved, and the real pass must actually
// fast-forward.
func TestReadPlansForRepos_DryRunCommitsBehindMatchesPostFastForwardRealPass(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	planSha := gitTest(t, local, "rev-parse", "HEAD")
	commitFile(t, origin, "advance1.txt", "a")
	commitFile(t, origin, "advance2.txt", "b")
	commitFile(t, origin, "advance3.txt", "c")
	const wantCommitsBehind = 3

	writePlan(t, local, "42-x.md", "---\nticketId: 42\nstatus: planned\nplanCommitSha: "+planSha+"\n---\nbody\n")

	repos := []RepoConfig{{Repo: "o/r", Dir: local}}

	// Dry-run pass: fetch + classify only, never merge.
	dryRunSyncs := syncMains(repos, io.Discard, true)
	dryPlans, _, _, err := readPlansForRepos(repos, dryRunSyncs, io.Discard)
	if err != nil {
		t.Fatalf("readPlansForRepos (dry-run) returned unexpected error: %v", err)
	}
	if len(dryPlans) != 1 {
		t.Fatalf("got %d dry-run plans, want 1: %+v", len(dryPlans), dryPlans)
	}
	if dryPlans[0].CommitsBehind != wantCommitsBehind {
		t.Errorf("dry-run CommitsBehind = %d, want %d", dryPlans[0].CommitsBehind, wantCommitsBehind)
	}
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != planSha {
		t.Errorf("dry-run must never move local HEAD, got %s want %s", got, planSha)
	}

	// Real pass: fetch + fast-forward.
	realSyncs := syncMains(repos, io.Discard, false)
	realPlans, _, _, err := readPlansForRepos(repos, realSyncs, io.Discard)
	if err != nil {
		t.Fatalf("readPlansForRepos (real pass) returned unexpected error: %v", err)
	}
	if len(realPlans) != 1 {
		t.Fatalf("got %d real-pass plans, want 1: %+v", len(realPlans), realPlans)
	}
	if realPlans[0].CommitsBehind != dryPlans[0].CommitsBehind {
		t.Errorf("real-pass CommitsBehind = %d, want it to match the dry-run's %d", realPlans[0].CommitsBehind, dryPlans[0].CommitsBehind)
	}
	originHEAD := gitTest(t, origin, "rev-parse", "HEAD")
	if got := gitTest(t, local, "rev-parse", "HEAD"); got != originHEAD {
		t.Errorf("real pass must fast-forward local HEAD to origin HEAD, got %s want %s", got, originHEAD)
	}
}

// TestReadPlansForRepos_PopulatesInventoryForEveryRepoEveryPass covers #884's
// wiring requirement: readPlansForRepos' returned map[string]PlanInventory
// carries an entry for EVERY configured repo, every pass -- including a repo
// whose `.plans` directory read failed (PlanInventoryUnreadable), which
// pre-#884 would have silently vanished from every returned map via the old
// bare `continue` (indistinguishable from verified absence). Two repos: one
// healthy (a single valid plan file), one whose `.plans` is a regular file
// (ENOTDIR, ever-present -- no root/permission dependency).
func TestReadPlansForRepos_PopulatesInventoryForEveryRepoEveryPass(t *testing.T) {
	healthyDir := t.TempDir()
	writePlan(t, healthyDir, "42-x.md", "---\nticketId: 42\nstatus: planned\n---\nbody\n")

	brokenDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenDir, ".plans"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos := []RepoConfig{{Repo: "healthy/repo", Dir: healthyDir}, {Repo: "broken/repo", Dir: brokenDir}}
	plans, _, inventories, err := readPlansForRepos(repos, nil, io.Discard)
	if err == nil {
		t.Fatal("readPlansForRepos: want a non-nil error (one repo's .plans directory is unreadable)")
	}
	if len(plans) != 1 || plans[0].TicketID != 42 {
		t.Fatalf("plans = %+v, want exactly the healthy repo's plan for ticket 42", plans)
	}
	if got := inventories["healthy/repo"]; got != PlanInventoryVerified {
		t.Errorf(`inventories["healthy/repo"] = %q, want PlanInventoryVerified`, got)
	}
	if got, ok := inventories["broken/repo"]; !ok || got != PlanInventoryUnreadable {
		t.Errorf(`inventories["broken/repo"] = (%q, present=%v), want (PlanInventoryUnreadable, true) -- a failed read must still be recorded, never silently absent from the map`, got, ok)
	}
}

// TestRunReconcileOnce_PopulatesInventoryForEveryRepoEveryPass is
// TestReadPlansForRepos_PopulatesInventoryForEveryRepoEveryPass's
// RunReconcileOnce-side twin (Phase 6+7 review finding #7): the plan's Test
// Strategy claims RunOnce AND RunReconcileOnce both populate PlanInventories
// for every configured repo every pass, but only RunOnce's readPlansForRepos
// had a dedicated wiring test -- RunReconcileOnce's own identical inline
// ReadPlans loop (reconcile_run.go) had no test exercising the
// collector->deps wiring itself, only applyReconcile's pure-function tests,
// which construct ReconcileInputs.PlanInventories by hand. Two repos: one
// healthy (an existing, empty `.plans` -- verified absence) and one broken
// (`.plans` is a regular file, ENOTDIR). Both repos carry one Planned,
// not-Working ticket; under testConfig's zero GracePeriod, the healthy
// repo's ticket must escalate to plan-invalid immediately (its plan
// inventory really was verified absent), while the broken repo's ticket
// must defer (grace observation preserved) rather than escalate on an
// unverified inventory -- a behavioral difference that can only be produced
// by PlanInventories actually reaching Reconcile through RunReconcileOnce's
// real collector wiring, not a hand-built fixture.
func TestRunReconcileOnce_PopulatesInventoryForEveryRepoEveryPass(t *testing.T) {
	installFakeGH(t, `
case "$1 $2" in
  "issue list")
    case "$*" in
      *"healthy/repo"*) printf '[{"number":1,"title":"T1","labels":[{"name":"Planned"}],"assignees":[]}]' ;;
      *"broken/repo"*) printf '[{"number":2,"title":"T2","labels":[{"name":"Planned"}],"assignees":[]}]' ;;
      *) printf '[]' ;;
    esac
    ;;
  "pr list") printf '[]' ;;
  *) exit 1 ;;
esac
`)
	serveChainSnapshot(t, watch.StateSnapshot{}) // reachable daemon, no live windows

	healthyDir := t.TempDir() // .plans never created -> verified absent
	brokenDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenDir, ".plans"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig()
	cfg.Repos = []RepoConfig{
		{Repo: "healthy/repo", Dir: healthyDir},
		{Repo: "broken/repo", Dir: brokenDir},
	}

	mut := &fakeMutator{}
	store := &memStore{}
	var buf bytes.Buffer
	if _, err := RunReconcileOnce(cfg, mut, false, &buf, store); err == nil {
		t.Fatal("RunReconcileOnce: want a non-nil error (broken/repo's .plans directory is unreadable)")
	}

	var healthyEdit *labelEdit
	for i, e := range mut.labelEdits {
		if e.repo == "healthy/repo" && e.number == 1 {
			healthyEdit = &mut.labelEdits[i]
		}
		if e.repo == "broken/repo" && e.number == 2 {
			t.Errorf("broken/repo#2 must never mutate on an unverified plan inventory, got %+v", e)
		}
	}
	if healthyEdit == nil {
		t.Fatalf("expected a label edit for healthy/repo#1 (verified-absent plan, zero grace), got %+v", mut.labelEdits)
	}
	if !containsStr(healthyEdit.add, labelPlanInvalid) || !containsStr(healthyEdit.remove, labelPlanned) {
		t.Errorf("healthy/repo#1 label edit = %+v, want add=[%s] remove=[%s]", healthyEdit, labelPlanInvalid, labelPlanned)
	}

	// broken/repo#2's gated defer never starts a fresh grace clock on first
	// sight (it only carries an already-existing one forward, mirroring
	// planProbeSkip's identical gated-defer shape) -- so on this first pass
	// it is absent from both Failed and the persisted observations, exactly
	// like a ticket the pure engine never touched, which is itself proof
	// the label mutation never fired.
	if _, ok := store.state.Observations["broken/repo#2"]; ok {
		t.Errorf("broken/repo#2 must not gain a fresh observation from a gated defer on first sight, got %+v", store.state.Observations)
	}
	if _, ok := store.state.Observations["healthy/repo#1"]; ok {
		t.Errorf("healthy/repo#1's observation must be resolved into an action, not carried forward, got %+v", store.state.Observations)
	}
}

// TestProbeRepoAutonomies_DryRunAndRealPassAgreeUsingSameFreshRef is the
// autonomy-side twin of the CommitsBehind parity test above: a lean config
// committed only on origin (not yet merged locally) must be visible to
// BOTH the dry-run pass (reading the fetched origin/main blob) and the
// subsequent real pass (reading post-fast-forward local HEAD) -- rendering
// the identical eligibility verdict, per the ticket's "render the same
// eligibility/re-plan result that the subsequent real synchronized pass
// would produce" acceptance criterion.
func TestProbeRepoAutonomies_DryRunAndRealPassAgreeUsingSameFreshRef(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	commitFile(t, origin, "advance.txt", "advance")
	writeCommittedConfig(t, origin, leanConfigJSON) // committed only on origin

	repos := []RepoConfig{{Repo: "o/r", Dir: local}}

	dryRunSyncs := syncMains(repos, io.Discard, true)
	dryAutonomy := probeRepoAutonomies(repos, dryRunSyncs, io.Discard)
	if dryAutonomy["o/r"] != RepoAutonomyLean {
		t.Errorf("dry-run autonomy = %q, want RepoAutonomyLean (fetched origin/main blob)", dryAutonomy["o/r"])
	}

	realSyncs := syncMains(repos, io.Discard, false)
	realAutonomy := probeRepoAutonomies(repos, realSyncs, io.Discard)
	if realAutonomy["o/r"] != RepoAutonomyLean {
		t.Errorf("real-pass autonomy = %q, want RepoAutonomyLean (post-fast-forward local HEAD blob)", realAutonomy["o/r"])
	}
}

// -- #881 AC6: an incomplete open-PR inventory spawns nothing, for both an
// ordinary Planned pickup and a Refined planning pickup ---------------------

// TestApplyDispatchOrdinaryPlannedPickup_IncompleteOpenPRInventory_NeverSpawnsOrClaims
// covers AC6's "tests ... prove no implementation/planning process is
// spawned on incomplete inventory" requirement for the ordinary
// already-Planned pickup kind: an incomplete OpenPRProbe must gate the
// ticket before it ever reaches applyDispatch's spawn loop -- zero runFn
// invocations, zero label mutations.
func TestApplyDispatchOrdinaryPlannedPickup_IncompleteOpenPRInventory_NeverSpawnsOrClaims(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an incomplete open-PR inventory must never spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	deps := dispatchableDeps(now)
	deps.Tickets[0].OpenPRProbe = OpenPRProbeUnreadable

	decisions, err := applyDispatch(testConfig(), deps, fakeController{}, mut, false, &buf, &prior)
	if err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != ActionSkip {
		t.Fatalf("decisions = %+v, want a single gated skip", decisions)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("expected zero label mutations, got %+v", mut.labelEdits)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (nothing dispatched)", prior)
	}
}

// TestApplyDispatchRefinedPlanningPickup_IncompleteOpenPRInventory_NeverSpawnsOrClaims
// covers AC6's Refined-planning-pickup half explicitly (Decision.Planning):
// an incomplete OpenPRProbe must gate a fresh Refined planning candidate
// exactly like an ordinary Planned pickup -- zero runFn invocations, zero
// label mutations.
func TestApplyDispatchRefinedPlanningPickup_IncompleteOpenPRInventory_NeverSpawnsOrClaims(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an incomplete open-PR inventory must never spawn a planning session either")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	cfg := testConfig()
	cfg.PlanRefined = true
	deps := planningDispatchDeps(now)
	deps.Tickets[0].OpenPRProbe = OpenPRProbeUnreadable

	decisions, err := applyDispatch(cfg, deps, fakeController{}, mut, false, &buf, &prior)
	if err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != ActionSkip {
		t.Fatalf("decisions = %+v, want a single gated skip", decisions)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("expected zero label mutations, got %+v", mut.labelEdits)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (nothing dispatched)", prior)
	}
}

// -- #881 AC5: dry-run and real dispatch consume the same completeness
// verdict --------------------------------------------------------------------

// TestRunOnce_DryRunAndRealPassProduceIdenticalOpenPRGateReason covers AC5
// end-to-end through the real collector: RunOnce(..., dryRun: true, ...) and
// RunOnce(..., dryRun: false, ...), run in sequence against the same
// scripted fake-gh world (a mid-pagination open-PR probe failure), must
// produce identical []Decision reasons -- proving dry-run and the real pass
// consume the same collector-stamped OpenPRProbe verdict, not two
// independently-computed ones. The repo Dir points at a real, freshly
// git-inited-and-cloned checkout (initOriginAndLocal) with no `.plans`
// directory, so both MainSync (a clean, up-to-date checkout) and the #884
// plan-inventory gate (a verified-absent `.plans` read) stay ungated,
// isolating the assertion to the open-PR gate alone. Before #884, an unset
// Dir was itself sufficient to keep MainSync ungated and ReadPlans was never
// consulted; #884 made an unset Dir fail closed for plan inventory too, so
// isolating this test now requires a real repo checkout rather than an
// empty Dir.
func TestRunOnce_DryRunAndRealPassProduceIdenticalOpenPRGateReason(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)

	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	script := fmt.Sprintf(`
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Fix thing","labels":[{"name":"Planned"}],"assignees":[{"login":"octocat"}]}]' ;;
  "api graphql")
    n=$(cat %[1]q 2>/dev/null || echo 0)
    n=$((n + 1))
    echo "$n" > %[1]q
    if [ $((n %% 2)) = "1" ]; then
      printf '{"data":{"repository":{"pullRequests":{"totalCount":150,"pageInfo":{"hasNextPage":true,"endCursor":"c2"},"nodes":[]}}}}'
    else
      echo "boom" >&2
      exit 1
    fi
    ;;
  "api user") printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`, countFile)

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a gated ticket must never spawn")
		return nil
	})

	runPass := func(dryRun bool) []Decision {
		installFakeGHOnPath(t, script)
		cfg := testConfig()
		cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local}}
		var buf bytes.Buffer
		decisions, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, dryRun, &buf, nil)
		if err != nil {
			t.Fatalf("RunOnce returned unexpected error: %v", err)
		}
		return decisions
	}

	dryDecisions := runPass(true)
	realDecisions := runPass(false)

	if len(dryDecisions) != 1 || len(realDecisions) != 1 {
		t.Fatalf("dry=%+v real=%+v, want exactly one decision each", dryDecisions, realDecisions)
	}
	if dryDecisions[0].Action != ActionSkip {
		t.Fatalf("dry-run action = %q, want ActionSkip (an incomplete open-PR probe must gate)", dryDecisions[0].Action)
	}
	if realDecisions[0].Action != ActionSkip {
		t.Fatalf("real-pass action = %q, want ActionSkip (an incomplete open-PR probe must gate)", realDecisions[0].Action)
	}
	if dryDecisions[0].Reason != realDecisions[0].Reason {
		t.Fatalf("dry-run reason %q != real-pass reason %q, want identical (AC5)", dryDecisions[0].Reason, realDecisions[0].Reason)
	}
}
