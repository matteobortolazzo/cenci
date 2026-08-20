package dispatch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: t.TempDir(), Session: "test-session"}}
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

	if _, err := applyDispatch(dispatchTestConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
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

	_, err := applyDispatch(dispatchTestConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior)
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

	_, err := applyDispatch(dispatchTestConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior)
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

	if _, err := applyDispatch(dispatchTestConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, nil, &prior); err != nil {
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

	_, err := applyDispatch(dispatchTestConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
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

	_, err := applyDispatch(dispatchTestConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
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

	_, err := applyDispatch(dispatchTestConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
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

	_, err := applyDispatch(dispatchTestConfig(), resumeDispatchDeps(now), fakeController{}, mut, false, &buf, &prior)
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

	cfg := dispatchTestConfig()
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

	if _, err := applyDispatch(dispatchTestConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
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

	if _, err := applyDispatch(dispatchTestConfig(), dispatchableDeps(now), fakeController{}, mut, false, &buf, &prior); err != nil {
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
// lines by matching on them. Also covers the #927 session rendering: a
// dispatch line carries the repo's configured session (or "(unset)"), a skip
// line is unchanged.
func TestFormatDecisionPrefixesRepo(t *testing.T) {
	skip := Decision{
		Ticket: Ticket{Repo: "o/r", Number: 45},
		Action: ActionSkip,
		Reason: "not Planned",
	}
	if got, want := formatDecision(skip, nil), "o/r#45 skip: not Planned"; got != want {
		t.Errorf("skip line = %q, want %q", got, want)
	}

	dispatch := Decision{
		Ticket: Ticket{Repo: "o/r", Number: 78},
		Plan:   &Plan{Path: ".plans/78-add-cache.md"},
		Action: ActionDispatch,
		Reason: "dispatch",
		Agent:  "claude",
	}
	sessionByRepo := map[string]string{"o/r": "a-work"}
	if got, want := formatDecision(dispatch, sessionByRepo), `o/r#78 dispatch (claude, 78-add-cache.md, session "a-work"): dispatch`; got != want {
		t.Errorf("dispatch line = %q, want %q", got, want)
	}
	if got, want := formatDecision(dispatch, nil), "o/r#78 dispatch (claude, 78-add-cache.md, session (unset)): dispatch"; got != want {
		t.Errorf("dispatch line with no configured session = %q, want %q", got, want)
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
	got := formatDecision(d, map[string]string{"o/r": "cenci"})
	want := `o/r#42 dispatch (claude, 42-slug.md, session "cenci"): resume — human answered`
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

	cfg := dispatchTestConfig()
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

	cfg := dispatchTestConfig()
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

	cfg := dispatchTestConfig()
	cfg.PlanRefined = true
	cfg.Model = "claude-sonnet-5"
	// Session (#927): sourced from the repo entry, not a removed fleet-wide
	// Config.Session field.
	cfg.Repos[0].Session = "cenci"

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

	cfg := dispatchTestConfig()
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

	cfg := dispatchTestConfig()
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

	cfg := dispatchTestConfig()
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
	got := formatDecision(d, nil)
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
	got := formatDecision(d, nil)
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
//
// #877: the config is committed on origin (never local-only) -- the
// autonomy probe now only ever reads the remote-confirmed
// refs/remotes/origin/main object (mainSyncResult.AutonomyRef), so a config
// committed solely to local's main (never pushed) would resolve
// RepoAutonomyMissing at that ref instead of the intended interactive
// denial, defeating this test's purpose.
func TestRunOnce_InteractiveRepoConfigDeniesPlanningPickup(t *testing.T) {
	mainSyncGitEnv(t)
	isolateDaemonSocket(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, interactiveConfigJSON)

	installFakeGHOnPath(t, runOnceFakeGHWithIdentity(`{"name":"Refined"}`))
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an autonomy-denied repo must never spawn")
		return nil
	})

	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local, Session: "test-session"}}

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
//
// #877: the config is committed on origin (never local-only) -- see
// TestRunOnce_InteractiveRepoConfigDeniesPlanningPickup's doc comment for why
// a local-only commit would no longer exercise the intended verdict now that
// the probe only ever reads the remote-confirmed ref.
func TestRunOnce_LeanRepoConfigPassesAutonomyGate(t *testing.T) {
	mainSyncGitEnv(t)
	isolateDaemonSocket(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, leanConfigJSON)

	installFakeGHOnPath(t, runOnceFakeGHWithIdentity(`{"name":"Refined"}`))
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("no live daemon is wired up in this test; a spawn here would mean a snapshot leaked in unexpectedly")
		return nil
	})

	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local, Session: "test-session"}}

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

// -- planning.attended fleet switch RunOnce narrowing wiring (#1086) --------

// TestRunOnce_AttendedNarrowsLeanRepoDeniesPlanningPickup covers the
// dispatchDeps.RepoAutonomy narrowing wiring end-to-end through RunOnce
// (#1086): a real enrolled repo whose committed .cenci/config.json IS lean,
// with the fleet planning.attended switch on, must be denied with the
// attended-specific reason -- not the interactive denial
// (reasonAutonomyInteractive) and not a silent "not Planned" -- proving
// RunOnce's narrowing step is actually wired between probeRepoAutonomies and
// Decide, not merely present on the struct. The once-per-pass narrowing log
// line names exactly one lean repo narrowed and never carries the
// lazyboards-reserved " skip:"/" dispatch " substrings.
func TestRunOnce_AttendedNarrowsLeanRepoDeniesPlanningPickup(t *testing.T) {
	mainSyncGitEnv(t)
	isolateDaemonSocket(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, leanConfigJSON)

	installFakeGHOnPath(t, runOnceFakeGHWithIdentity(`{"name":"Refined"}`))
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an attended-narrowed repo must never spawn")
		return nil
	})

	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.PlanningAttended = true
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local, Session: "test-session"}}

	var buf bytes.Buffer
	if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}

	log := buf.String()
	if !strings.Contains(log, reasonAutonomyAttended) {
		t.Errorf("expected the attended denial reason logged, got %q", log)
	}
	if strings.Contains(log, reasonAutonomyInteractive) {
		t.Errorf("a lean repo narrowed by attended must never be denied with the interactive reason, got %q", log)
	}
	if strings.Contains(log, "not Planned") {
		t.Errorf("must be denied via the autonomy gate specifically, not fall through to \"not Planned\", got %q", log)
	}

	const wantNarrowLine = "dispatch: planning attended mode on (planning.attended); 1 lean repo(s) narrowed"
	if !strings.Contains(log, wantNarrowLine) {
		t.Errorf("expected the narrowing count log line %q, got log:\n%s", wantNarrowLine, log)
	}
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "planning attended mode on") {
			continue
		}
		if strings.Contains(line, " skip:") || strings.Contains(line, " dispatch ") {
			t.Errorf("narrowing log line must not contain the lazyboards-reserved substrings, got %q", line)
		}
	}
}

// TestRunOnce_AttendedNarrowing_DryRunAndRealPassIdentical covers the AC
// that the narrowing applies identically in `cenci dispatch --dry-run` and
// in a real pass: an attended-narrowed lean repo denies the same Refined
// planning candidate with the same reason regardless of dryRun.
func TestRunOnce_AttendedNarrowing_DryRunAndRealPassIdentical(t *testing.T) {
	mainSyncGitEnv(t)
	isolateDaemonSocket(t)

	runPass := func(dryRun bool) []Decision {
		local, origin := initOriginAndLocal(t)
		writeCommittedConfig(t, origin, leanConfigJSON)
		installFakeGHOnPath(t, runOnceFakeGHWithIdentity(`{"name":"Refined"}`))
		stubRunFn(t, func(run.Opts, run.Controller) error {
			t.Fatal("an attended-narrowed repo must never spawn")
			return nil
		})

		cfg := testConfig()
		cfg.PlanRefined = true
		cfg.PlanningAttended = true
		cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local, Session: "test-session"}}

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
	if dryDecisions[0].Action != ActionSkip || realDecisions[0].Action != ActionSkip {
		t.Fatalf("dry action = %q, real action = %q, want both ActionSkip (attended denies)", dryDecisions[0].Action, realDecisions[0].Action)
	}
	if dryDecisions[0].Reason != reasonAutonomyAttended || realDecisions[0].Reason != reasonAutonomyAttended {
		t.Fatalf("dry reason = %q, real reason = %q, want both %q", dryDecisions[0].Reason, realDecisions[0].Reason, reasonAutonomyAttended)
	}
}

// TestRunOnce_AttendedOff_LogByteIdenticalToNoPlanningBlock covers the
// byte-identity AC (#1086): a fleet config with an explicit
// "planning": {"attended": false} and a fleet config with no top-level
// "planning" block at all must produce byte-identical RunOnce log output --
// no narrowing-step log line, no RepoAutonomy narrowing, over the same
// (lean, Refined-candidate) fleet.
func TestRunOnce_AttendedOff_LogByteIdenticalToNoPlanningBlock(t *testing.T) {
	mainSyncGitEnv(t)

	runPass := func(planningBlockJSON string) string {
		isolateDaemonSocket(t)
		local, origin := initOriginAndLocal(t)
		writeCommittedConfig(t, origin, leanConfigJSON)

		installFakeGHOnPath(t, runOnceFakeGHWithIdentity(`{"name":"Refined"}`))
		stubRunFn(t, func(run.Opts, run.Controller) error {
			t.Fatal("no live daemon is wired up in this test; a spawn here would mean a snapshot leaked in unexpectedly")
			return nil
		})

		configPath := filepath.Join(t.TempDir(), "config.json")
		content := `{"dispatch": {"planRefined": true}` + planningBlockJSON + `}`
		if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
			t.Fatalf("writing config: %v", err)
		}
		cfg, err := LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local, Session: "test-session"}}

		var buf bytes.Buffer
		if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil); err != nil {
			t.Fatalf("RunOnce returned unexpected error: %v", err)
		}
		return buf.String()
	}

	withExplicitFalse := runPass(`, "planning": {"attended": false}`)
	withNoBlock := runPass("")

	if withExplicitFalse != withNoBlock {
		t.Errorf("log output differs between explicit attended:false and no planning block:\nexplicit false:\n%s\nno block:\n%s", withExplicitFalse, withNoBlock)
	}
}

// TestRunOnce_PlanningAttendedUnparseable_LogsOnceAndOrdinaryDispatchUnaffected
// covers the AC: a malformed planning.attended value (e.g. the string "yes")
// must never abort the pass -- LoadConfig succeeds, folds to attended (the
// restrictive direction), RunOnce logs exactly one line naming
// planning.attended, and an ordinary already-Planned ticket in the same pass
// still dispatches normally (the fold only narrows the autonomy map; it
// never gates ordinary Planned dispatch).
func TestRunOnce_PlanningAttendedUnparseable_LogsOnceAndOrdinaryDispatchUnaffected(t *testing.T) {
	mainSyncGitEnv(t)
	serveChainSnapshot(t, watch.StateSnapshot{}) // reachable, idle daemon

	repo, _ := initOriginAndLocal(t)
	planSha := gitTest(t, repo, "rev-parse", "HEAD")
	writePlan(t, repo, "20-x.md", "---\nticketId: 20\nstatus: planned\nplanCommitSha: "+planSha+"\n---\nbody\n")

	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue list") printf '[{"number":20,"title":"T20","labels":[{"name":"Planned"}],"assignees":[{"login":"octocat"}]}]' ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  "api user") printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`)

	var spawnedTickets []string
	stubRunFn(t, func(opts run.Opts, ctrl run.Controller) error {
		spawnedTickets = append(spawnedTickets, opts.WindowTicket)
		return nil
	})

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"planning": {"attended": "yes"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.PlanningAttendedUnparseable {
		t.Fatalf("test setup sanity check: cfg.PlanningAttendedUnparseable = %v, want true", cfg.PlanningAttendedUnparseable)
	}
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: repo, Session: "test-session"}}

	var buf bytes.Buffer
	if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}

	log := buf.String()
	if count := strings.Count(log, "planning.attended"); count != 1 {
		t.Errorf("log mentions planning.attended %d times, want exactly 1:\n%s", count, log)
	}
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "planning.attended") {
			continue
		}
		if strings.Contains(line, " skip:") || strings.Contains(line, " dispatch ") {
			t.Errorf("unparseable-value log line must not contain the lazyboards-reserved substrings, got %q", line)
		}
	}
	if len(spawnedTickets) != 1 || spawnedTickets[0] != "20" {
		t.Errorf("spawned tickets = %v, want exactly [\"20\"] (an unparseable planning.attended value must not block ordinary Planned dispatch)", spawnedTickets)
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
		{Repo: "healthy/repo", Dir: healthyDir, Session: "test-session"},
		{Repo: "broken/repo", Dir: brokenDir, Session: "test-session"},
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
//
// #877 additionally requires dry-run/real to agree on BOTH the
// authorization ref itself (AutonomyRef -- always the fully-qualified
// remote-tracking ref in both modes, since the merge, not the fetch, is the
// only thing dry-run skips) and the staleness verdict (CommitsBehind),
// computed against that same fetched object -- extended in place here
// rather than split into a second test, so a regression that breaks parity
// on only one of the two verdicts cannot hide behind the other still
// passing.
func TestProbeRepoAutonomies_DryRunAndRealPassAgreeUsingSameFreshRef(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	planSha := gitTest(t, local, "rev-parse", "HEAD")
	commitFile(t, origin, "advance.txt", "advance")
	writeCommittedConfig(t, origin, leanConfigJSON) // committed only on origin
	writePlan(t, local, "42-x.md", "---\nticketId: 42\nstatus: planned\nplanCommitSha: "+planSha+"\n---\nbody\n")

	repos := []RepoConfig{{Repo: "o/r", Dir: local}}

	dryRunSyncs := syncMains(repos, io.Discard, true)
	dryAutonomy := probeRepoAutonomies(repos, dryRunSyncs, io.Discard)
	dryPlans, _, _, err := readPlansForRepos(repos, dryRunSyncs, io.Discard)
	if err != nil {
		t.Fatalf("readPlansForRepos (dry-run) returned unexpected error: %v", err)
	}
	if dryAutonomy["o/r"] != RepoAutonomyLean {
		t.Errorf("dry-run autonomy = %q, want RepoAutonomyLean (fetched origin/main blob)", dryAutonomy["o/r"])
	}
	if dryRunSyncs["o/r"].AutonomyRef != remoteMainAuthRef {
		t.Errorf("dry-run AutonomyRef = %q, want %q", dryRunSyncs["o/r"].AutonomyRef, remoteMainAuthRef)
	}
	if len(dryPlans) != 1 {
		t.Fatalf("got %d dry-run plans, want 1: %+v", len(dryPlans), dryPlans)
	}

	realSyncs := syncMains(repos, io.Discard, false)
	realAutonomy := probeRepoAutonomies(repos, realSyncs, io.Discard)
	realPlans, _, _, err := readPlansForRepos(repos, realSyncs, io.Discard)
	if err != nil {
		t.Fatalf("readPlansForRepos (real pass) returned unexpected error: %v", err)
	}
	if realAutonomy["o/r"] != RepoAutonomyLean {
		t.Errorf("real-pass autonomy = %q, want RepoAutonomyLean (post-fast-forward local HEAD blob)", realAutonomy["o/r"])
	}
	if realSyncs["o/r"].AutonomyRef != remoteMainAuthRef {
		t.Errorf("real-pass AutonomyRef = %q, want %q -- both dry-run and real fetch, so AutonomyRef must agree", realSyncs["o/r"].AutonomyRef, remoteMainAuthRef)
	}
	if len(realPlans) != 1 {
		t.Fatalf("got %d real-pass plans, want 1: %+v", len(realPlans), realPlans)
	}
	if realPlans[0].CommitsBehind != dryPlans[0].CommitsBehind {
		t.Errorf("real-pass CommitsBehind = %d, want it to match the dry-run's %d", realPlans[0].CommitsBehind, dryPlans[0].CommitsBehind)
	}
}

// -- #877: RunOnce end-to-end, fetch-outage gating and a mixed fleet --------

// TestRunOnce_FetchOutage_HoldsPlanningLaunchesNothing_OrdinaryPlannedStillDispatches
// covers the ticket's core availability tradeoff in one pass: a repo whose
// `git fetch origin` fails this pass must hold its Refined planning
// candidate (launching no process at all), while an ordinary, already-
// approved `Planned` ticket in a DIFFERENT, healthy repo in the very same
// pass must still dispatch normally -- a fetch outage in one repo must never
// bleed into ordinary implementation work elsewhere.
func TestRunOnce_FetchOutage_HoldsPlanningLaunchesNothing_OrdinaryPlannedStillDispatches(t *testing.T) {
	mainSyncGitEnv(t)
	serveChainSnapshot(t, watch.StateSnapshot{}) // reachable, idle daemon

	// repoA: a Refined planning candidate whose origin remote is
	// unresolvable this pass -- MainSyncFetchFailed, AutonomyRef == "".
	repoA := t.TempDir()
	gitTest(t, repoA, "init", "-b", "main")
	commitFile(t, repoA, "base.txt", "base")
	gitTest(t, repoA, "remote", "add", "origin", filepath.Join(t.TempDir(), "does-not-exist"))

	// repoB: an ordinary, already-Planned, fresh ticket in a healthy repo.
	repoB, _ := initOriginAndLocal(t)
	planSha := gitTest(t, repoB, "rev-parse", "HEAD")
	writePlan(t, repoB, "20-x.md", "---\nticketId: 20\nstatus: planned\nplanCommitSha: "+planSha+"\n---\nbody\n")

	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue list")
    case "$*" in
      *"o/a"*) printf '[{"number":10,"title":"T10","labels":[{"name":"Refined"}],"assignees":[{"login":"octocat"}]}]' ;;
      *"o/b"*) printf '[{"number":20,"title":"T20","labels":[{"name":"Planned"}],"assignees":[{"login":"octocat"}]}]' ;;
      *) printf '[]' ;;
    esac
    ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  "api user") printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`)

	var spawnedTickets []string
	stubRunFn(t, func(opts run.Opts, ctrl run.Controller) error {
		if opts.WindowTicket == "10" {
			t.Fatal("a repo whose fetch failed this pass must never spawn a planning session")
		}
		spawnedTickets = append(spawnedTickets, opts.WindowTicket)
		return nil
	})

	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.Repos = []RepoConfig{{Repo: "o/a", Dir: repoA, Session: "session-a"}, {Repo: "o/b", Dir: repoB, Session: "session-b"}}

	var buf bytes.Buffer
	if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}

	log := buf.String()
	if !strings.Contains(log, "o/a#10 skip: "+reasonAutonomyFetchUnconfirmed) {
		t.Errorf("expected o/a#10 to be held with the fetch-unconfirmed reason, got log %q", log)
	}
	if len(spawnedTickets) != 1 || spawnedTickets[0] != "20" {
		t.Errorf("spawned tickets = %v, want exactly [\"20\"] (o/b's ordinary Planned pickup unaffected by o/a's fetch outage)", spawnedTickets)
	}
}

// TestRunOnce_MixedFleet_RemoteLocalCombinationsAndFailureModes covers the
// ticket's "mixed-fleet tests cover remote/local combinations, fetch
// outage, forced stale refs, malformed config, branch changes, and timeout"
// acceptance criterion in a single six-repo pass, each repo a fresh Refined
// planning candidate so the RunOnce -> probeRepoAutonomies ->
// Inputs.RepoAutonomy -> Decide wiring is exercised for every case, not just
// the pure-function gate.
func TestRunOnce_MixedFleet_RemoteLocalCombinationsAndFailureModes(t *testing.T) {
	mainSyncGitEnv(t)
	serveChainSnapshot(t, watch.StateSnapshot{}) // reachable, idle daemon

	// r1: remote lean, fetch ok -- the sole authorizing case.
	r1, origin1 := initOriginAndLocal(t)
	writeCommittedConfig(t, origin1, leanConfigJSON)

	// r2: local-only lean (unpushed), remote interactive, fetch ok -- must
	// deny; the local grant must never leak through. r2 first fetches +
	// ff-only merges origin2's interactive commit before adding its own
	// local-only lean commit on top, so local ends up genuinely AHEAD of
	// (a descendant of) the fetched origin/main -- committing the local-only
	// config without first merging origin's own new commit would instead
	// produce two independently-authored commits off the shared base, a
	// genuine MainSyncDiverged (caught while writing this test), which would
	// gate the ticket at rule 0 before autonomy is ever consulted and defeat
	// this case's purpose.
	r2, origin2 := initOriginAndLocal(t)
	writeCommittedConfig(t, origin2, interactiveConfigJSON)
	gitTest(t, r2, "fetch", "origin")
	gitTest(t, r2, "merge", "--ff-only", "origin/main")
	writeCommittedConfig(t, r2, leanConfigJSON) // local ahead, unpushed

	// r3: malformed remote config, fetch ok -- must deny as malformed.
	r3, origin3 := initOriginAndLocal(t)
	writeCommittedConfig(t, origin3, malformedConfigJSON)

	// r4: unresolvable remote -- fetch outage, AutonomyRef empty.
	r4 := t.TempDir()
	gitTest(t, r4, "init", "-b", "main")
	commitFile(t, r4, "base.txt", "base")
	gitTest(t, r4, "remote", "add", "origin", filepath.Join(t.TempDir(), "does-not-exist"))

	// r5: checked-out branch changes mid-pass, AFTER a successful fetch --
	// MainSyncNotMain gates every pickup in the repo before autonomy is
	// even consulted (rule 0 fires first).
	r5, origin5 := initOriginAndLocal(t)
	commitFile(t, origin5, "advance.txt", "advance")

	// r6: fetch hangs past gitWaitDelay -- forced-close timeout, classified
	// identically to r4's outage (MainSyncFetchFailed / AutonomyRef empty),
	// but reached via a different syncMain code path.
	r6, _ := initOriginAndLocal(t)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
is_fetch=0
for arg in "$@"; do
  if [ "$arg" = "fetch" ]; then is_fetch=1; fi
done
if [ "$1" = "-C" ] && [ "$is_fetch" = "1" ]; then
  if [ "$2" = %[2]q ]; then
    "%[1]s" "$@"
    status=$?
    "%[1]s" -C "$2" checkout -b sneaky-mixed-fleet >/dev/null 2>&1
    exit $status
  fi
  if [ "$2" = %[3]q ]; then
    (sleep 30 &)
    exit 0
  fi
fi
exec "%[1]s" "$@"
`, realGit, r5, r6)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue list")
    case "$*" in
      *"o/r1"*) printf '[{"number":101,"title":"T101","labels":[{"name":"Refined"}],"assignees":[{"login":"octocat"}]}]' ;;
      *"o/r2"*) printf '[{"number":102,"title":"T102","labels":[{"name":"Refined"}],"assignees":[{"login":"octocat"}]}]' ;;
      *"o/r3"*) printf '[{"number":103,"title":"T103","labels":[{"name":"Refined"}],"assignees":[{"login":"octocat"}]}]' ;;
      *"o/r4"*) printf '[{"number":104,"title":"T104","labels":[{"name":"Refined"}],"assignees":[{"login":"octocat"}]}]' ;;
      *"o/r5"*) printf '[{"number":105,"title":"T105","labels":[{"name":"Refined"}],"assignees":[{"login":"octocat"}]}]' ;;
      *"o/r6"*) printf '[{"number":106,"title":"T106","labels":[{"name":"Refined"}],"assignees":[{"login":"octocat"}]}]' ;;
      *) printf '[]' ;;
    esac
    ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  "api user") printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`)

	var spawnedTickets []string
	stubRunFn(t, func(opts run.Opts, ctrl run.Controller) error {
		spawnedTickets = append(spawnedTickets, opts.WindowTicket)
		return nil
	})

	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.Repos = []RepoConfig{
		{Repo: "o/r1", Dir: r1, Session: "session-r1"},
		{Repo: "o/r2", Dir: r2, Session: "session-r2"},
		{Repo: "o/r3", Dir: r3, Session: "session-r3"},
		{Repo: "o/r4", Dir: r4, Session: "session-r4"},
		{Repo: "o/r5", Dir: r5, Session: "session-r5"},
		{Repo: "o/r6", Dir: r6, Session: "session-r6"},
	}

	var buf bytes.Buffer
	if _, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}
	log := buf.String()

	if len(spawnedTickets) != 1 || spawnedTickets[0] != "101" {
		t.Errorf("spawned tickets = %v, want exactly [\"101\"] (the sole remote-lean, fetch-ok repo)", spawnedTickets)
	}
	wantSubstrings := map[string]string{
		"o/r2#102": "o/r2#102 skip: repo autonomy not lean",
		"o/r3#103": "o/r3#103 skip: repo config malformed",
		"o/r4#104": "o/r4#104 skip: " + reasonAutonomyFetchUnconfirmed,
		"o/r5#105": "o/r5#105 skip: " + reasonMainNotMain,
		"o/r6#106": "o/r6#106 skip: " + reasonAutonomyFetchUnconfirmed,
	}
	for name, want := range wantSubstrings {
		if !strings.Contains(log, want) {
			t.Errorf("%s: expected log to contain %q, got %q", name, want, log)
		}
	}
	if !strings.Contains(log, "o/r1#101 dispatch") {
		t.Errorf("expected o/r1#101 to dispatch (the sole remote-lean, fetch-ok repo), got log %q", log)
	}
}

// -- #882 AC7: a permission-denied or permission-unresolved resume answer
// spawns nothing and mutates no labels -------------------------------------

// TestApplyDispatchResumeUnauthorizedProbe_NeverSpawnsOrClaims covers AC7's
// "tests prove no label mutation or process launch occurs for every
// unauthorized/unknown path" requirement for the positively-denied class
// (AnswerProbeUnauthorized): the resolved answerer lacks current repository
// write permission, so the resume gate must skip before applyDispatch's
// spawn loop -- zero runFn invocations, zero label mutations, quota
// untouched.
func TestApplyDispatchResumeUnauthorizedProbe_NeverSpawnsOrClaims(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a permission-denied resume answer must never spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	deps := resumeDispatchDeps(now)
	deps.Answers["o/r#42"] = AnswerProbeUnauthorized

	decisions, err := applyDispatch(testConfig(), deps, fakeController{}, mut, false, &buf, &prior)
	if err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != ActionSkip || decisions[0].Resume {
		t.Fatalf("decisions = %+v, want a single gated skip with Resume=false", decisions)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("expected zero label mutations, got %+v", mut.labelEdits)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (nothing dispatched)", prior)
	}
}

// TestApplyDispatchResumePermissionProbeUnresolved_NeverSpawnsOrClaims covers
// AC7's other half: an unresolved permission-probe class (here, a permission
// API error) must gate identically to a positive denial -- zero runFn
// invocations, zero label mutations, quota untouched.
func TestApplyDispatchResumePermissionProbeUnresolved_NeverSpawnsOrClaims(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an unresolved permission probe must never spawn a resume")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mut := &fakeMutator{}
	prior := 0
	var buf bytes.Buffer

	deps := resumeDispatchDeps(now)
	deps.Answers["o/r#42"] = AnswerProbePermissionAPIError

	decisions, err := applyDispatch(testConfig(), deps, fakeController{}, mut, false, &buf, &prior)
	if err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != ActionSkip || decisions[0].Resume {
		t.Fatalf("decisions = %+v, want a single gated skip with Resume=false", decisions)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("expected zero label mutations, got %+v", mut.labelEdits)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (nothing dispatched)", prior)
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
		cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local, Session: "test-session"}}
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
