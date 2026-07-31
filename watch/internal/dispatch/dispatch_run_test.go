package dispatch

import (
	"bytes"
	"errors"
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
  "pr list") printf '[]' ;;
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
}

func (m *orderRecordingMutator) EditLabels(repo string, number int, add, remove []string) error {
	if m.failEditLabels {
		m.callOrder = append(m.callOrder, "edit-fail")
		return errors.New("swap failed")
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
// spawn failure that follows a successful pre-spawn swap must not attempt a
// second (post-spawn) label edit -- the resume path never claims twice.
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
	if len(mut.edits) != 1 {
		t.Fatalf("expected exactly 1 label edit (the pre-spawn swap only, no second post-spawn claim), got %d: %+v", len(mut.edits), mut.edits)
	}
	if prior != 0 {
		t.Errorf("prior = %d, want 0 (a failed spawn must not consume quota)", prior)
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
// #42 with no plan file and a reachable, idle daemon.
func planningDispatchDeps(now time.Time) dispatchDeps {
	return dispatchDeps{
		Tickets:     []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Refined"}, Assignees: []string{"octocat"}}},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
	}
}

// replanDispatchDeps is the autonomous re-plan happy path: one Planned
// ticket #42 whose plan is stale beyond testConfig's tolerance.
func replanDispatchDeps(now time.Time) dispatchDeps {
	return dispatchDeps{
		Tickets:     []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Planned"}, Assignees: []string{"octocat"}}},
		Plans:       []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "planned", CommitsBehind: 10}},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
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
