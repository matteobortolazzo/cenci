package pipeline

// Unit tests for the label lifecycle transitions (ticket #559): stage
// gating, "working"'s ownership check (mirrors
// flow/skills/ticket-ownership/SKILL.md), self-healing label
// create/apply/remove, and the sentinel errors. Package-boundary ("white
// box") tests, matching retry_test.go's own convention: scripts the
// package-internal `command` seam (retry.go) directly rather than a fake
// `gh` binary on PATH, since this is an in-package test with the seam
// already available -- retry_test.go/store_test.go establish that
// convention for this package (the black-box `exectest.WriteExecutable`
// fake-binary style is reserved for pipeline_cmd_test.go's subprocess
// tests, which can't reach the in-process seam).

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// -- fake gh scripting via the `command` seam ------------------------------

// ghCall is one recorded invocation of the `command` seam.
type ghCall struct {
	name string
	args []string
}

func (c ghCall) String() string {
	return c.name + " " + strings.Join(c.args, " ")
}

// hasFlag reports whether args contains --flag immediately followed by
// value (or, for a boolean-style flag with no value to check, just that
// --flag is present when value is "").
func (c ghCall) hasFlag(flag, value string) bool {
	for i, a := range c.args {
		if a == flag {
			if value == "" {
				return true
			}
			return i+1 < len(c.args) && c.args[i+1] == value
		}
	}
	return false
}

func (c ghCall) is(tokens ...string) bool {
	if len(c.args) < len(tokens) {
		return false
	}
	for i, tok := range tokens {
		if c.args[i] != tok {
			return false
		}
	}
	return true
}

// fakeGh scripts the `command` seam for one test: a stateful gh fake that
// tracks assignees (so the "no assignees -> auto-claim -> re-verify" path
// can be exercised deterministically) and lets each test configure how
// `gh label create` responds.
type fakeGh struct {
	t         *testing.T
	login     string
	assignees []string
	calls     []ghCall

	// labelCreateBehavior configures the response to `gh label create`:
	// "ok" (exit 0), "already-exists" (gh's real "already exists" error
	// text, exit 1), or "genuine-failure" (an unrelated failure, exit 1).
	labelCreateBehavior string

	// ticketUpdatedAt is the response to the post-edit `gh issue view <id>
	// --json updatedAt` freshness-baseline fetch (#669).
	ticketUpdatedAt string
}

func newFakeGh(t *testing.T, login string, assignees []string) *fakeGh {
	t.Helper()
	return &fakeGh{
		t:                   t,
		login:               login,
		assignees:           append([]string{}, assignees...),
		labelCreateBehavior: "ok",
		ticketUpdatedAt:     "2026-07-20T20:05:00Z",
	}
}

func (f *fakeGh) install() {
	f.t.Helper()
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		call := ghCall{name: name, args: args}
		f.calls = append(f.calls, call)
		return f.respond(call)
	}
	f.t.Cleanup(func() { command = original })
}

func (f *fakeGh) respond(call ghCall) ([]byte, error) {
	switch {
	case call.name == "gh" && call.is("api", "user"):
		return []byte(f.login + "\n"), nil
	case call.name == "gh" && call.is("issue", "view") && call.hasFlag("--json", "updatedAt"):
		return []byte(`{"updatedAt":"` + f.ticketUpdatedAt + `"}`), nil
	case call.name == "gh" && call.is("issue", "view"):
		return []byte(f.assigneesJSON()), nil
	case call.name == "gh" && call.is("issue", "edit") && call.hasFlag("--add-assignee", ""):
		for i, a := range call.args {
			if a == "--add-assignee" && i+1 < len(call.args) {
				f.assignees = append(f.assignees, call.args[i+1])
			}
		}
		return nil, nil
	case call.name == "gh" && call.is("label", "create"):
		switch f.labelCreateBehavior {
		case "already-exists":
			return []byte("could not add label: 'Working' already exists"), fmt.Errorf("exit status 1")
		case "genuine-failure":
			return []byte("HTTP 401: Bad credentials"), fmt.Errorf("exit status 1")
		default:
			return nil, nil
		}
	case call.name == "gh" && call.is("issue", "edit"):
		return nil, nil
	}
	f.t.Fatalf("unexpected gh invocation: %s", call)
	return nil, nil
}

func (f *fakeGh) assigneesJSON() string {
	type assignee struct {
		Login string `json:"login"`
	}
	type payload struct {
		Assignees []assignee `json:"assignees"`
	}
	p := payload{}
	for _, login := range f.assignees {
		p.Assignees = append(p.Assignees, assignee{Login: login})
	}
	b, err := json.Marshal(p)
	if err != nil {
		f.t.Fatalf("marshal fake assignees payload: %v", err)
	}
	return string(b)
}

func (f *fakeGh) callsMatching(tokens ...string) []ghCall {
	var out []ghCall
	for _, c := range f.calls {
		if c.is(tokens...) {
			out = append(out, c)
		}
	}
	return out
}

// mustSeedState writes a pipeline state file at stage for id under
// stateDir, so ApplyLabelTransition has something to gate against.
// StateDir is used verbatim as the directory holding <id>.json (mirroring
// pipeline.Opts' own StateDir contract in pipeline.go), not the canonical
// <repo>/.cenci/pipeline/<id>.json shape statePath builds.
func mustSeedState(t *testing.T, stateDir, id string, stage Stage) {
	t.Helper()
	path := filepath.Join(stateDir, id+".json")
	if err := saveState(path, State{SchemaVersion: CurrentSchemaVersion, ID: id, Stage: stage, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

// -- happy paths: one per transition ---------------------------------------

func TestApplyLabelTransition_Working_HappyPath_SoleAssigneeMatches(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("ApplyLabelTransition(working): %v", err)
	}

	if calls := gh.callsMatching("label", "create"); len(calls) == 0 {
		t.Error("expected a `gh label create` call for the self-healing label create step")
	} else if !calls[0].hasFlag("--repo", "o/r") {
		t.Errorf("gh label create call = %v, want --repo o/r", calls[0])
	}
	addCalls := gh.callsMatching("issue", "edit")
	found := false
	for _, c := range addCalls {
		if c.hasFlag("--add-label", "Working") {
			found = true
			if c.hasFlag("--remove-label", "") {
				t.Errorf("working transition must not remove any label, got %v", c)
			}
		}
	}
	if !found {
		t.Error("expected a `gh issue edit --add-label Working` call")
	}
	if claimCalls := gh.callsMatching("issue", "edit"); len(claimCalls) > 0 {
		for _, c := range claimCalls {
			if c.hasFlag("--add-assignee", "") {
				t.Errorf("sole-assignee-matches path must never claim an assignee, got %v", c)
			}
		}
	}
}

func TestApplyLabelTransition_Working_FromWaitingForPlanApproval_PlanFileResume(t *testing.T) {
	// A saved-plan pickup (plan-file mode) re-applies Working while the
	// persisted stage is still waiting_for_plan_approval — bare `plan`
	// recorded that stage when the plan was persisted, and `plan --approve`
	// only runs later in Phase 2's Gate Check (#668).
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageWaitingForPlanApproval)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("ApplyLabelTransition(working) from waiting_for_plan_approval: %v", err)
	}

	found := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if c.hasFlag("--add-label", "Working") {
			found = true
			if c.hasFlag("--remove-label", "") {
				t.Errorf("working transition must not remove any label, got %v", c)
			}
		}
	}
	if !found {
		t.Error("expected a `gh issue edit --add-label Working` call")
	}
}

func TestApplyLabelTransition_Planned_HappyPath_AddsPlannedRemovesWorking(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageWaitingForPlanApproval)
	gh := newFakeGh(t, "octocat", nil)
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "planned"}); err != nil {
		t.Fatalf("ApplyLabelTransition(planned): %v", err)
	}

	found := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if c.hasFlag("--add-label", "Planned") {
			found = true
			if !c.hasFlag("--remove-label", "Working") {
				t.Errorf("non-trivial planned transition must remove Working, got %v", c)
			}
		}
	}
	if !found {
		t.Error("expected a `gh issue edit --add-label Planned` call")
	}
}

func TestApplyLabelTransition_Planned_Trivial_KeepsWorking(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageWaitingForPlanApproval)
	gh := newFakeGh(t, "octocat", nil)
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "planned", Trivial: true}); err != nil {
		t.Fatalf("ApplyLabelTransition(planned, trivial): %v", err)
	}

	found := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if c.hasFlag("--add-label", "Planned") {
			found = true
			if c.hasFlag("--remove-label", "") {
				t.Errorf("Trivial Fast Path's planned transition must keep Working (no --remove-label), got %v", c)
			}
		}
	}
	if !found {
		t.Error("expected a `gh issue edit --add-label Planned` call")
	}
}

func TestApplyLabelTransition_InReview_HappyPath_AddsInReviewRemovesWorking(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageFinalized)
	gh := newFakeGh(t, "octocat", nil)
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "in-review"}); err != nil {
		t.Fatalf("ApplyLabelTransition(in-review): %v", err)
	}

	found := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if c.hasFlag("--add-label", "In Review") {
			found = true
			if !c.hasFlag("--remove-label", "Working") {
				t.Errorf("in-review transition must remove Working, got %v", c)
			}
		}
	}
	if !found {
		t.Error("expected a `gh issue edit --add-label \"In Review\"` call")
	}
}

func TestApplyLabelTransition_InReview_WithParent_CascadesToParent(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageFinalized)
	gh := newFakeGh(t, "octocat", nil)
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "in-review", ParentID: "10"}); err != nil {
		t.Fatalf("ApplyLabelTransition(in-review, parent): %v", err)
	}

	foundParent := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if len(c.args) > 2 && c.args[2] == "10" && c.hasFlag("--add-label", "In Review") {
			foundParent = true
		}
	}
	if !foundParent {
		t.Error("expected a `gh issue edit 10 --add-label \"In Review\"` cascade call to the parent ticket")
	}
}

// -- freshness baseline: post-edit ticketUpdatedAt recording (#669) --------

func TestApplyLabelTransition_Planned_RecordsTicketUpdatedAtBaseline(t *testing.T) {
	// The pipeline's own `gh issue edit` label swap bumps the ticket's
	// updatedAt past the plan's createdAt; without a recorded baseline,
	// plan-check would mark every freshly persisted plan stale (#669).
	// After the swap, the transition must fetch the post-edit updatedAt
	// and persist it as State.TicketUpdatedAt.
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageWaitingForPlanApproval)
	gh := newFakeGh(t, "octocat", nil)
	gh.ticketUpdatedAt = "2026-07-20T20:07:00Z"
	gh.install()

	s, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "planned"})
	if err != nil {
		t.Fatalf("ApplyLabelTransition(planned): %v", err)
	}
	if s.TicketUpdatedAt != "2026-07-20T20:07:00Z" {
		t.Errorf("returned State.TicketUpdatedAt = %q, want the post-edit updatedAt %q", s.TicketUpdatedAt, "2026-07-20T20:07:00Z")
	}

	persisted, err := loadState(filepath.Join(stateDir, "42.json"))
	if err != nil {
		t.Fatalf("load persisted state: %v", err)
	}
	if persisted.TicketUpdatedAt != "2026-07-20T20:07:00Z" {
		t.Errorf("persisted TicketUpdatedAt = %q, want %q", persisted.TicketUpdatedAt, "2026-07-20T20:07:00Z")
	}

	// Ordering (#620): the baseline fetch must run AFTER the label edit —
	// a pre-edit fetch would record a stale updatedAt that the edit itself
	// immediately invalidates, reintroducing the bug.
	editIdx, fetchIdx := -1, -1
	for i, c := range gh.calls {
		if c.is("issue", "edit") && c.hasFlag("--add-label", "Planned") {
			editIdx = i
		}
		if c.is("issue", "view") && c.hasFlag("--json", "updatedAt") {
			fetchIdx = i
		}
	}
	if editIdx == -1 || fetchIdx == -1 {
		t.Fatalf("expected both a label edit and an updatedAt fetch, got calls %v", gh.calls)
	}
	if fetchIdx < editIdx {
		t.Errorf("updatedAt fetch (call %d) ran before the label edit (call %d); baseline must be post-edit", fetchIdx, editIdx)
	}
}

func TestApplyLabelTransition_Working_RecordsTicketUpdatedAtBaseline(t *testing.T) {
	// The working transition (including a saved-plan pickup's re-apply)
	// also edits the ticket; recording its post-edit updatedAt keeps a
	// later re-run of plan-check from seeing that edit as user drift.
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.ticketUpdatedAt = "2026-07-20T20:09:00Z"
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("ApplyLabelTransition(working): %v", err)
	}
	persisted, err := loadState(filepath.Join(stateDir, "42.json"))
	if err != nil {
		t.Fatalf("load persisted state: %v", err)
	}
	if persisted.TicketUpdatedAt != "2026-07-20T20:09:00Z" {
		t.Errorf("persisted TicketUpdatedAt = %q, want %q", persisted.TicketUpdatedAt, "2026-07-20T20:09:00Z")
	}
}

func TestApplyLabelTransition_BaselineFetchFailure_PropagatesError(t *testing.T) {
	// An unverifiable freshness baseline must never be silently dropped
	// (#560 item 1): a missing baseline would make the next plan-check
	// misclassify the pipeline's own edit as user drift.
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageWaitingForPlanApproval)
	gh := newFakeGh(t, "octocat", nil)
	gh.ticketUpdatedAt = "not-a-timestamp"
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "planned"})
	if err == nil {
		t.Fatal("ApplyLabelTransition(planned) with an unparsable post-edit updatedAt: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "updatedAt") {
		t.Errorf("error = %q, want it to name the updatedAt baseline fetch", err.Error())
	}
}

// -- stage-mismatch domain errors (content-specific, rule #446) -----------

func TestApplyLabelTransition_Working_WrongStage_ReturnsErrWrongStageForLabel(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageNew)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"})
	if err == nil {
		t.Fatal("ApplyLabelTransition(working) from the wrong stage: want an error, got nil")
	}
	if !errors.Is(err, ErrWrongStageForLabel) {
		t.Errorf("error = %v, want errors.Is(_, ErrWrongStageForLabel)", err)
	}
	if !strings.Contains(err.Error(), string(StagePrepared)) {
		t.Errorf("error = %q, want it to name the required stage %q", err.Error(), StagePrepared)
	}
	if !strings.Contains(err.Error(), string(StageNew)) {
		t.Errorf("error = %q, want it to name the actual stage %q", err.Error(), StageNew)
	}
	if len(gh.calls) != 0 {
		t.Errorf("gh calls = %v, want none (the stage gate must fail before any gh call)", gh.calls)
	}
}

func TestApplyLabelTransition_Planned_WrongStage_ReturnsErrWrongStageForLabel(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	gh := newFakeGh(t, "octocat", nil)
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "planned"})
	if err == nil {
		t.Fatal("ApplyLabelTransition(planned) from the wrong stage: want an error, got nil")
	}
	if !errors.Is(err, ErrWrongStageForLabel) {
		t.Errorf("error = %v, want errors.Is(_, ErrWrongStageForLabel)", err)
	}
	if len(gh.calls) != 0 {
		t.Errorf("gh calls = %v, want none (the stage gate must fail before any gh call)", gh.calls)
	}
}

func TestApplyLabelTransition_InReview_WrongStage_ReturnsErrWrongStageForLabel(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageExecuted)
	gh := newFakeGh(t, "octocat", nil)
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "in-review"})
	if err == nil {
		t.Fatal("ApplyLabelTransition(in-review) from the wrong stage: want an error, got nil")
	}
	if !errors.Is(err, ErrWrongStageForLabel) {
		t.Errorf("error = %v, want errors.Is(_, ErrWrongStageForLabel)", err)
	}
	if len(gh.calls) != 0 {
		t.Errorf("gh calls = %v, want none (the stage gate must fail before any gh call)", gh.calls)
	}
}

// -- minimum-stage gating (#636): at-minimum and past-minimum success -------
//
// labelStagePrecondition became a single minimum Stage per transition
// instead of an exact-stage whitelist. The at-minimum case for each
// transition is already covered by the happy-path tests above
// (TestApplyLabelTransition_Working_HappyPath_SoleAssigneeMatches at
// StagePrepared, TestApplyLabelTransition_Planned_HappyPath_... at
// StageWaitingForPlanApproval, TestApplyLabelTransition_InReview_HappyPath_...
// at StageFinalized); the tests below cover the past-minimum case, which the
// old exact-stage whitelist rejected but the new minimum gate must accept.

func TestApplyLabelTransition_Working_PastMinimum_AtExecuted_Succeeds(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageExecuted)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("ApplyLabelTransition(working) past minimum stage (executed): %v", err)
	}

	found := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if c.hasFlag("--add-label", "Working") {
			found = true
		}
	}
	if !found {
		t.Error("expected the Working label to still be applied when re-applied past the minimum stage")
	}
}

func TestApplyLabelTransition_Planned_PastMinimum_AtExecuted_Succeeds(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StageExecuted)
	gh := newFakeGh(t, "octocat", nil)
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "planned"}); err != nil {
		t.Fatalf("ApplyLabelTransition(planned) past minimum stage (executed): %v", err)
	}

	found := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if c.hasFlag("--add-label", "Planned") {
			found = true
		}
	}
	if !found {
		t.Error("expected the Planned label to still be applied when re-applied past the minimum stage")
	}
}

// TestApplyLabelTransition_InReview_MinimumIsMaximumRank_PastMinimumUnreachable
// documents, rather than silently omits, that "in-review"'s minimum stage
// (finalized) is also the state machine's maximum rank: there is no stage
// past finalized, so unlike working's and planned's, a past-minimum success
// case for in-review cannot exist. The at-minimum case is already covered by
// TestApplyLabelTransition_InReview_HappyPath_AddsInReviewRemovesWorking.
func TestApplyLabelTransition_InReview_MinimumIsMaximumRank_PastMinimumUnreachable(t *testing.T) {
	t.Skip("in-review's minimum stage (finalized) is the maximum rank in the total order; no stage exists past it, so no past-minimum case is possible -- see TestApplyLabelTransition_InReview_HappyPath_AddsInReviewRemovesWorking for the at-minimum case")
}

// -- unknown persisted stage (#636): hard fail, never a silent no-op --------

// TestApplyLabelTransition_UnknownPersistedStage_HardFails locks in the
// default-deny requirement (watch/AGENTS.md #598/#628): a corrupt/forward-
// incompatible persisted stage value must never satisfy any transition's
// minimum-stage gate, must still be classified as ErrWrongStageForLabel
// (preserving the CLI's existing exit-code path), with a message that is
// content-distinct from the ordinary wrong-stage case (it names the
// persisted value as unknown/unrecognized, not merely "too early"), and
// must make zero gh calls.
func TestApplyLabelTransition_UnknownPersistedStage_HardFails(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", Stage("bogus"))
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"})
	if err == nil {
		t.Fatal("ApplyLabelTransition(working) with an unknown persisted stage: want an error, got nil")
	}
	if !errors.Is(err, ErrWrongStageForLabel) {
		t.Errorf("error = %v, want errors.Is(_, ErrWrongStageForLabel)", err)
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error = %q, want a content-distinct message naming the unknown persisted stage", err.Error())
	}
	if len(gh.calls) != 0 {
		t.Errorf("gh calls = %v, want none (the stage gate must fail before any gh call)", gh.calls)
	}
}

// -- ownership branches (only for "working") --------------------------------

func TestApplyLabelTransition_Working_ForeignAssignee_ReturnsErrForeignAssignee(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	gh := newFakeGh(t, "octocat", []string{"someone-else"})
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"})
	if err == nil {
		t.Fatal("ApplyLabelTransition(working) with a foreign assignee: want an error, got nil")
	}
	if !errors.Is(err, ErrForeignAssignee) {
		t.Errorf("error = %v, want errors.Is(_, ErrForeignAssignee)", err)
	}
	if calls := gh.callsMatching("label", "create"); len(calls) != 0 {
		t.Errorf("expected no label-create call when ownership is rejected, got %v", calls)
	}
}

func TestApplyLabelTransition_Working_MultipleAssignees_ReturnsErrMultipleAssignees(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	gh := newFakeGh(t, "octocat", []string{"octocat", "someone-else"})
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"})
	if err == nil {
		t.Fatal("ApplyLabelTransition(working) with multiple assignees: want an error, got nil")
	}
	if !errors.Is(err, ErrMultipleAssignees) {
		t.Errorf("error = %v, want errors.Is(_, ErrMultipleAssignees)", err)
	}
}

func TestApplyLabelTransition_Working_NoAssignees_AutoClaims(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	gh := newFakeGh(t, "octocat", nil)
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("ApplyLabelTransition(working) with no assignees (auto-claim path): %v", err)
	}

	claimed := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if c.hasFlag("--add-assignee", "octocat") {
			claimed = true
		}
	}
	if !claimed {
		t.Error("expected a `gh issue edit --add-assignee octocat` auto-claim call when the ticket had no assignees")
	}
	// After claiming, the label must still be applied -- auto-claim isn't
	// the terminal step.
	found := false
	for _, c := range gh.callsMatching("issue", "edit") {
		if c.hasFlag("--add-label", "Working") {
			found = true
		}
	}
	if !found {
		t.Error("expected the Working label to still be applied after auto-claiming the ticket")
	}
}

// -- login format validation (mirrors ticket-ownership's full contract) ----

func TestApplyLabelTransition_Working_InvalidLoginFormat_ReturnsError(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	// A login containing a character outside ^[A-Za-z0-9-]+$ (e.g. a
	// malicious/malformed `gh api user` response) must be rejected before
	// it's ever used for ownership comparison/claiming.
	gh := newFakeGh(t, "octo cat; rm -rf", []string{"octo cat; rm -rf"})
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"})
	if err == nil {
		t.Fatal("ApplyLabelTransition(working) with a malformed gh login: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error = %q, want it to mention the login-format validation failure", err.Error())
	}
	if calls := gh.callsMatching("issue", "view"); len(calls) != 0 {
		t.Errorf("expected no assignee lookup once the login itself fails validation, got %v", calls)
	}
}

// -- self-healing label create -----------------------------------------------

func TestApplyLabelTransition_LabelAlreadyExists_TreatedAsSuccess(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.labelCreateBehavior = "already-exists"
	gh.install()

	if _, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"}); err != nil {
		t.Fatalf("ApplyLabelTransition with a pre-existing label: want success (already-exists treated as success), got: %v", err)
	}
}

func TestApplyLabelTransition_LabelCreateGenuineFailure_ReturnsError(t *testing.T) {
	stateDir := t.TempDir()
	mustSeedState(t, stateDir, "42", StagePrepared)
	gh := newFakeGh(t, "octocat", []string{"octocat"})
	gh.labelCreateBehavior = "genuine-failure"
	gh.install()

	_, err := ApplyLabelTransition(LabelOpts{ID: "42", StateDir: stateDir, RepoSlug: "o/r", Transition: "working"})
	if err == nil {
		t.Fatal("ApplyLabelTransition with a genuine label-create failure: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "label") {
		t.Errorf("error = %q, want it to mention the label-create failure", err.Error())
	}
}

// -- sentinel identity (#412: a direct unit test at the package boundary) --

func TestLabelSentinelErrors_AreDistinctAndDetectableViaErrorsIs(t *testing.T) {
	sentinels := map[string]error{
		"ErrWrongStageForLabel": ErrWrongStageForLabel,
		"ErrForeignAssignee":    ErrForeignAssignee,
		"ErrMultipleAssignees":  ErrMultipleAssignees,
	}
	for name, sentinel := range sentinels {
		if sentinel == nil {
			t.Errorf("%s must not be nil", name)
			continue
		}
		if !errors.Is(sentinel, sentinel) {
			t.Errorf("%s must satisfy errors.Is against itself", name)
		}
	}
	seen := map[error]string{}
	for name, sentinel := range sentinels {
		if other, dup := seen[sentinel]; dup {
			t.Errorf("%s and %s must be distinct sentinel values, got the same error", name, other)
		}
		seen[sentinel] = name
	}
}
