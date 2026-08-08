package dispatch

// Ticket #855: the end-to-end regression for epic #661's autonomous
// dependency chain. TestAutonomousChain_PositiveEndToEnd drives the full
// refinement -> lean planning -> trusted escalation/resume -> implementation
// -> fail-closed babysit merge -> main sync -> dependent-pickup chain through
// the REAL dispatch/pipeline/babysit production code (never a mock of the
// engine under test), against the fake `gh` process (chainfake_test.go) and
// real temp git repos. The five negative variants each mutate exactly one
// fixture input from the positive baseline and assert the exact reason
// constant plus the absence of the downstream side effect, mirroring
// automerge_test.go's TestEvaluateAutomergeConditionMatrix discipline.
//
// Never call t.Parallel() anywhere in this file (chainfake_test.go's
// os.Args[0] mutation is process-global, and babysit's localRepoRoot needs a
// stable t.Chdir).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/internal/run"
)

const chainRepoConfigLean = `{"planning":{"autonomy":"lean"},"automerge":{"maxChangedFiles":50,"maxDiffLines":5000,"protectedPaths":[],"mergeMethod":"squash"}}`

const chainEscalationNonce = "0123456789abcdef0123456789abcdef"

// The following mirror internal/babysit's own unexported automerge reason
// constants (automerge.go) verbatim: this package cannot import them (they
// are unexported in a sibling package), so the exact literal strings are
// pinned here for content-specific assertions against the persisted
// AutomergeReason, per automerge_test.go's TestEvaluateAutomergeConditionMatrix
// discipline (never a bare true/false check).
const (
	chainReasonCICheckCancelled = "CI check cancelled"      // babysit.reasonCICheckCancelled
	chainReasonReviewPending    = "review feedback pending" // babysit.reasonReviewPending
	// chainReasonMergeIndeterminate mirrors babysit.reasonMergeIndeterminate
	// (automerge.go) -- the "gh pr merge exited zero but PR is not MERGED"
	// class, per #914's "indeterminate" ambiguity kind (a fake response the
	// server accepted with no mutation).
	chainReasonMergeIndeterminate = "gh pr merge exited zero but PR is not MERGED"
	// chainFailureClassTruncated mirrors babysit.failureClassTruncated
	// (gh.go) -- classifyGhFailure's verdict for a bounded-buffer overflow
	// on either stream, per #914's "truncate" ambiguity kind.
	chainFailureClassTruncated = "truncated"
)

// chainConfig builds a dispatch.Config enrolling h.repo at h.local, with
// stage-aware lean planning turned on. Individual tests override fields as
// needed (e.g. PlanRefined false for the non-lean negative variant).
func chainConfig(h *chainHarness) Config {
	cfg := testConfig()
	cfg.PlanRefined = true
	// Session (#927): every chain test drives a real dispatch.RunOnce pass
	// that expects to spawn, so the per-repo session gate must resolve a
	// configured session here -- fakeController{} (used throughout this
	// suite) always reports HasSession true, so any non-empty value works.
	cfg.Repos = []RepoConfig{{Repo: h.repo, Dir: h.local, Session: "chain-session"}}
	return cfg
}

// decisionFor finds ticket number's Decision in decisions, failing the test
// if absent.
func decisionFor(t *testing.T, decisions []Decision, number int) Decision {
	t.Helper()
	for _, d := range decisions {
		if d.Ticket.Number == number {
			return d
		}
	}
	t.Fatalf("no decision found for ticket #%d among %+v", number, decisions)
	return Decision{}
}

// chainRunOnce runs one real dispatch.RunOnce pass against cfg using a real
// GHMutator (so label mutations flow through the fake gh and are visible to
// the NEXT pass), returning the decisions and the captured log text.
func chainRunOnce(t *testing.T, cfg Config, mut *GHMutator, dryRun bool) ([]Decision, string) {
	t.Helper()
	var buf bytes.Buffer
	decisions, err := RunOnce(cfg, fakeController{}, mut, dryRun, &buf, nil)
	if err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v\nlog:\n%s", err, buf.String())
	}
	return decisions, buf.String()
}

// TestAutonomousChain_PositiveEndToEnd is the ticket's headline scenario: the
// 10-step chain, each phase asserting its own externally-visible outcome.
func TestAutonomousChain_PositiveEndToEnd(t *testing.T) {
	h := newChainHarness(t)
	mut := &GHMutator{}

	h.commitAndPushConfig(chainRepoConfigLean)

	// -- A. Seed the shared golden fixture graph and assert the exact
	// three-ticket decision map ----------------------------------------------
	// The graph is #912's committed fixture (flow/tests/adversarial-chain/
	// fixtures/post-refinement-graph.json): parent #100 (OPEN, Refined, no
	// plan{} front matter) plus children #101 (lean, dependency-free) and
	// #102 ("Depends on #101"). Per Q4: #100 carries no plan file, and
	// Decide's rule 6 sibling-serialization keys off plan front-matter
	// parentId (decide.go:513,563-578), so a plan-less parent is an ordinary
	// second lean-planning-pickup candidate, not excluded as an umbrella --
	// #100 is picked up alongside #101 in phase B below and deliberately
	// left `Working` for the rest of this scenario (never driven further
	// here): it is reused as TestAutonomousChain_ReconcileStateSurvivesReExec's
	// stranded subject. A future edit that "cleans up" #100's Working state
	// would silently strip that test's fixture -- do not touch it below.
	//
	// The fixture's missing automerge:ok (by design: it's a post-refinement
	// graph) is granted through a real `gh issue edit` at the phase-F
	// boundary below, never pre-seeded.
	graph := readGoldenGraph(t)
	seedFromGoldenGraph(t, h, graph)

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, mut, true /* dry-run */)
	wantReasons := map[int]string{
		graph.Parent.Number: "plan — Refined, no plan file",
		101:                 "plan — Refined, no plan file",
		102:                 "waiting on dependency #101",
	}
	for number, want := range wantReasons {
		if got := decisionFor(t, decisions, number).Reason; got != want {
			t.Fatalf("phase A: #%d decision = %q, want %q", number, got, want)
		}
	}
	assertGoldenFidelityUnchanged(t, h, graph, "phase A")

	// -- B. Lean pickup: fleet + repository authorization both present ------
	// Both #100 (the plan-less parent) and #101 (the lean child) are real
	// dispatch pickups this pass; #102 stays gated on its dependency.
	var captured []run.Opts
	stubRunFn(t, func(o run.Opts, _ run.Controller) error {
		captured = append(captured, o)
		return nil
	})

	decisions, log := chainRunOnce(t, cfg, mut, false /* real */)
	gotPickup := map[string]bool{}
	for _, o := range captured {
		gotPickup[o.Ticket] = true
	}
	wantPickup := map[string]bool{"100": true, "101": true}
	if !reflect.DeepEqual(gotPickup, wantPickup) {
		t.Fatalf("phase B: pickup set = %v, want exactly %v", gotPickup, wantPickup)
	}
	if got := decisionFor(t, decisions, 102).Reason; got != "waiting on dependency #101" {
		t.Fatalf("phase B: #102 must remain gated on its dependency, got %q", got)
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Working") {
		t.Fatalf("phase B: #101 must carry Working after the lean claim, got labels %v", is.Labels)
	}
	if is := h.world().Issues[100]; !containsStr(is.Labels, "Working") {
		t.Fatalf("phase B: #100 must carry Working after its own lean claim, got labels %v", is.Labels)
	}
	if strings.Contains(log, "dispatch: #102 run failed") {
		t.Fatalf("phase B: #102 must never attempt a spawn, log:\n%s", log)
	}
	assertGoldenFidelityUnchanged(t, h, graph, "phase B")
	captured = nil

	// -- C. Planner escalation: persist a trusted anchor, reach Input Needed -
	if _, err := pipeline.Run(pipeline.Opts{Stage: "prepare", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase C: pipeline prepare failed: %v", err)
	}
	if _, err := pipeline.Run(pipeline.Opts{Stage: "await-input", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase C: pipeline await-input failed: %v", err)
	}

	anchorBody := "Which approach should the planner take: A or B?\n<!-- cenci-planner-escalation:" + chainEscalationNonce + " -->"
	if err := mut.Comment(h.repo, 101, anchorBody); err != nil {
		t.Fatalf("phase C: posting escalation anchor comment: %v", err)
	}
	anchorComments := h.world().Comments[101]
	anchorID := anchorComments[len(anchorComments)-1].ID

	activeAnchors := 0
	for _, c := range anchorComments {
		if strings.Contains(c.Body, "cenci-planner-escalation:") {
			activeAnchors++
		}
	}
	if activeAnchors != 1 {
		t.Fatalf("phase C: expected exactly one active escalation anchor comment on #101, got %d", activeAnchors)
	}

	planPath := writeChainPlan(t, h.local, 101, [][2]string{
		{"status", "awaiting-input"},
		{"escalationNonce", chainEscalationNonce},
		{"escalationCommentId", strconv.FormatInt(anchorID, 10)},
	})

	if _, err := pipeline.ApplyLabelTransition(pipeline.LabelOpts{
		ID: "101", RepoRoot: h.local, RepoSlug: h.repo, Transition: "input-needed",
	}); err != nil {
		t.Fatalf("phase C: input-needed label transition failed: %v", err)
	}

	state, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: "101", RepoRoot: h.local})
	if err != nil {
		t.Fatalf("phase C: fresh GetArtifacts reload failed: %v", err)
	}
	if state.Stage != pipeline.StageWaitingForInput {
		t.Fatalf("phase C: persisted stage = %q, want %q", state.Stage, pipeline.StageWaitingForInput)
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Input Needed") || containsStr(is.Labels, "Working") {
		t.Fatalf("phase C: expected +Input Needed -Working, got labels %v", is.Labels)
	}

	if got := decisionFor(t, chainDecisions(t, cfg, mut), 101).Reason; got != reasonAnswerWaiting {
		t.Fatalf("phase C: #101 decision = %q, want %q", got, reasonAnswerWaiting)
	}
	assertGoldenFidelityUnchanged(t, h, graph, "phase C")

	// -- D. Authorized answer resumes and finalizes the plan -----------------
	// #882: author_association alone (OWNER, seeded below) is no longer
	// sufficient authorization -- octocat must also carry a current
	// repository write permission, resolved through the real write-permission
	// probe route (chainCollaboratorPermission). Without this seed, the
	// gate would deny the resume once #882 lands (the plan's Implementation
	// Order step 5: land this fixture/seed before the gate, not after, or
	// this existing phase D would fail for the wrong reason).
	h.seedPermission("octocat", "write")
	h.addIssueComment(101, "octocat", "User", "OWNER", "Go with approach A.")

	decisions, _ = chainRunOnce(t, cfg, mut, false)
	resumeDecision := decisionFor(t, decisions, 101)
	if !resumeDecision.Resume || resumeDecision.Reason != "resume — human answered" {
		t.Fatalf("phase D: #101 decision = %+v, want a resume dispatch", resumeDecision)
	}
	wantTicketArg := filepath.Join(".plans", filepath.Base(planPath))
	if len(captured) != 1 || captured[0].Ticket != wantTicketArg {
		t.Fatalf("phase D: runFn Opts = %+v, want Ticket=%q", captured, wantTicketArg)
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Working") || containsStr(is.Labels, "Input Needed") {
		t.Fatalf("phase D: expected +Working -Input Needed after resume claim, got labels %v", is.Labels)
	}
	assertGoldenFidelityUnchanged(t, h, graph, "phase D")
	captured = nil

	freshSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	writeChainPlan(t, h.local, 101, [][2]string{
		{"status", "planned"},
		{"planCommitSha", freshSHA},
	})
	if _, err := pipeline.Run(pipeline.Opts{Stage: "plan", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase D: pipeline plan (bare) failed: %v", err)
	}
	if _, err := pipeline.Run(pipeline.Opts{Stage: "plan", Approve: true, ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase D: pipeline plan --approve failed: %v", err)
	}
	if _, err := pipeline.ApplyLabelTransition(pipeline.LabelOpts{
		ID: "101", RepoRoot: h.local, RepoSlug: h.repo, Transition: "planned",
	}); err != nil {
		t.Fatalf("phase D: planned label transition failed: %v", err)
	}

	// -- E. Implementation produces a PR tied to the granted ticket ----------
	decisions, _ = chainRunOnce(t, cfg, mut, false)
	implDecision := decisionFor(t, decisions, 101)
	if implDecision.Resume || implDecision.Planning || implDecision.Reason != "dispatch" {
		t.Fatalf("phase E: #101 decision = %+v, want an ordinary dispatch", implDecision)
	}
	if len(captured) != 1 {
		t.Fatalf("phase E: expected exactly one implementation pickup, got %+v", captured)
	}

	gitTest(t, h.local, "checkout", "-b", "implement-101")
	commitFile(t, h.local, "feature.txt", "impl")
	gitTest(t, h.local, "push", "origin", "implement-101")
	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	gitTest(t, h.local, "checkout", "main")

	h.seedPR(ghPR{
		Number: 103, HeadRefName: "implement-101", HeadRefOID: headSHA, BaseRefName: "main",
		ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Additions: 1, Files: []string{"feature.txt"},
	})

	if _, err := pipeline.Run(pipeline.Opts{Stage: "execute", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase E: pipeline execute failed: %v", err)
	}
	// GetArtifacts reload after EVERY stage transition (AC1), not only
	// await-input (phase C's precedent above): each call re-reads the
	// flock-guarded state file from disk, proving the stage transition is
	// actually durable, not merely reflected in the in-process Output value
	// pipeline.Run already returned.
	if st, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase E: GetArtifacts reload after execute failed: %v", err)
	} else if st.Stage != pipeline.StageExecuted {
		t.Fatalf("phase E: persisted stage after execute = %q, want %q", st.Stage, pipeline.StageExecuted)
	}
	if _, err := pipeline.Run(pipeline.Opts{Stage: "review", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase E: pipeline review failed: %v", err)
	}
	if st, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase E: GetArtifacts reload after review failed: %v", err)
	} else if st.Stage != pipeline.StageReviewed {
		t.Fatalf("phase E: persisted stage after review = %q, want %q", st.Stage, pipeline.StageReviewed)
	}
	if _, err := pipeline.Run(pipeline.Opts{Stage: "finalize", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase E: pipeline finalize failed: %v", err)
	}
	if _, err := pipeline.ApplyLabelTransition(pipeline.LabelOpts{
		ID: "101", RepoRoot: h.local, RepoSlug: h.repo, Transition: "in-review",
	}); err != nil {
		t.Fatalf("phase E: in-review label transition failed: %v", err)
	}

	decisions, _ = chainRunOnce(t, cfg, mut, true)
	final101 := decisionFor(t, decisions, 101)
	if !final101.Ticket.HasOpenPR {
		t.Fatalf("phase E: #101 must be collected with HasOpenPR=true once the PR is open, got %+v", final101.Ticket)
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "In Review") {
		t.Fatalf("phase E: #101 must carry In Review, got labels %v", is.Labels)
	}
	assertGoldenFidelityUnchanged(t, h, graph, "phase E")

	// -- F. Babysit: pass-only CI + resolved feedback -> immediate squash ----
	setFleetAutomergeEnabled(t, true)
	stateDir := t.TempDir()

	// The fixture's missing automerge:ok is by design (Q4 assumptions): grant
	// it here through a real `gh issue edit` at the phase-F boundary, then
	// assert it landed, rather than pre-seeding it in phase A.
	if err := mut.EditLabels(h.repo, 101, []string{"automerge:ok"}, nil); err != nil {
		t.Fatalf("phase F: granting automerge:ok via a real gh issue edit: %v", err)
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "automerge:ok") {
		t.Fatalf("phase F: expected automerge:ok after the real gh issue edit, got labels %v", is.Labels)
	}

	h.seedChecks(103, ghCheck{Bucket: "pass", Name: "build", State: "SUCCESS"}, ghCheck{Bucket: "pass", Name: "test", State: "SUCCESS"})
	// A reviewer's earlier CHANGES_REQUESTED review, since superseded by their
	// own later APPROVED review -- exercises babysit's real GitHub-authoritative
	// resolution (classifyReviewKey/latestEffectiveReview, #850), not merely
	// "there was never any feedback to begin with".
	h.seedReview(103, "reviewer", "CHANGES_REQUESTED", "2026-01-01T00:00:00Z")
	h.seedReview(103, "reviewer", "APPROVED", "2026-01-02T00:00:00Z")

	restoreWD := chainChdir(t, h.local)
	defer restoreWD()
	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true}); err != nil {
		t.Fatalf("phase F: first babysit.Run returned unexpected error: %v", err)
	}

	mergeCalls := chainMergeInvocations(h.world())
	if len(mergeCalls) != 1 {
		t.Fatalf("phase F: expected exactly one gh pr merge invocation, got %d: %v", len(mergeCalls), mergeCalls)
	}
	call := mergeCalls[0]
	if !strings.Contains(call, "--squash") || !strings.Contains(call, "--match-head-commit") {
		t.Fatalf("phase F: merge invocation missing squash/match-head-commit: %q", call)
	}
	if strings.Contains(call, "--merge") || strings.Contains(call, "--rebase") || strings.Contains(call, "--delete-branch") || strings.Contains(call, "--admin") {
		t.Fatalf("phase F: merge invocation must never carry --merge/--rebase/--delete-branch/--admin: %q", call)
	}

	st1 := readBabysitState(t, stateDir)
	if st1.AutomergeDecision != "merge" {
		t.Fatalf("phase F: AutomergeDecision = %q, want %q", st1.AutomergeDecision, "merge")
	}

	// Restart/reload: a second Run against the same --state-dir observes MERGED.
	restoreWD = chainChdir(t, h.local)
	defer restoreWD()
	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true}); err != nil {
		t.Fatalf("phase F: second babysit.Run returned unexpected error: %v", err)
	}

	if is := h.world().Issues[101]; !containsStr(is.Labels, "Implemented") || containsStr(is.Labels, "In Review") {
		t.Fatalf("phase F: expected +Implemented -In Review after the reload tick, got labels %v", is.Labels)
	}

	// -- G. Next dispatch pass fast-forwards enrolled main -------------------
	beforeHEAD := gitTest(t, h.local, "rev-parse", "HEAD")
	decisions, _ = chainRunOnce(t, cfg, mut, false)
	afterHEAD := gitTest(t, h.local, "rev-parse", "HEAD")
	originHEAD := gitTest(t, h.origin, "rev-parse", "HEAD")
	if afterHEAD == beforeHEAD {
		t.Fatalf("phase G: local main must fast-forward to the squash-merged origin commit, HEAD unchanged at %s", beforeHEAD)
	}
	if afterHEAD != originHEAD {
		t.Fatalf("phase G: local HEAD = %s, want it to match origin HEAD %s", afterHEAD, originHEAD)
	}

	// -- H. Dependent ticket #102 flips from waiting to eligible and picked up
	depDecision := decisionFor(t, decisions, 102)
	if depDecision.Action != ActionDispatch || depDecision.Reason != "plan — Refined, no plan file" {
		t.Fatalf("phase H: #102 decision = %+v, want an eligible planning dispatch now that #101 is closed", depDecision)
	}
	var got102 bool
	for _, o := range captured {
		if o.Ticket == "102" {
			got102 = true
		}
	}
	if !got102 {
		t.Fatalf("phase H: expected runFn invoked for #102, got %+v", captured)
	}

	// babysit's launch() has three call sites: ci-repair and babysit-attention
	// (both gated on a FAILING check, babysit.go -- never applicable here, CI
	// is pass-only) and address-review, whose #885 launch trigger
	// (PendingKeys \ LaunchedKeys) is evaluated only *after*
	// reconcileFeedback's own end-of-tick resolution pass has run, so it
	// fires only for a key still genuinely pending once that pass completes
	// -- never for a key that both first appears and resolves within the
	// very same tick. Phase F seeds a fresh PR's first-ever review history (a
	// CHANGES_REQUESTED immediately superseded by the same reviewer's later
	// APPROVED, both already on GitHub by babysit's very first tick), so the
	// review resolves before the launch trigger is ever evaluated -- zero
	// address-review invocations are expected on either tick here, and never
	// ci-repair/babysit-attention either.
	wantInvocations := []string{}
	if invs := chainCenciInvocations(t, h.cenciLogPath); !slices.Equal(invs, wantInvocations) {
		t.Fatalf("phase F: cenci self-exec invocations = %v, want exactly %v", invs, wantInvocations)
	}

	// -- I. Reconciliation state: a real RunReconcileOnce pass persists its
	// grace-clock/apply-retry-budget state to disk (AC1's "reconciliation
	// state" bullet) -- the chain suite never ran the reconciler at all
	// before #914.
	// This pass's own reconciliation-state assertion below needs a REALISTIC
	// grace-clock/apply-retry-budget policy, not chainConfig's zero-valued
	// GracePeriod/RetryBudget/ApplyRetryBudget (harmless for the dispatch
	// passes above, which never read them -- only Reconcile/applyReconcile
	// do): a zero GracePeriod would escalate #100 straight to dispatch-failed
	// on this very first observation, leaving no Observations entry to
	// assert against. DefaultConfig()'s values are exactly what LoadConfig
	// falls back to for a real fleet with no explicit override -- the same
	// policy TestChainReconcileHelper's re-exec'd child runs under (its
	// on-disk config file never sets these fields either).
	def := DefaultConfig()
	cfg.GracePeriod = def.GracePeriod
	cfg.RetryBudget = def.RetryBudget
	cfg.ApplyRetryBudget = def.ApplyRetryBudget

	statePath := filepath.Join(t.TempDir(), "reconcile.json")
	store := NewStateStore(statePath)
	var reconcileLog bytes.Buffer
	if _, err := RunReconcileOnce(cfg, mut, false, &reconcileLog, store); err != nil {
		t.Fatalf("RunReconcileOnce returned unexpected error: %v\nlog:\n%s", err, reconcileLog.String())
	}
	reconcileState, err := store.Load()
	if err != nil {
		t.Fatalf("reloading the persisted reconciliation state: %v", err)
	}
	// Exact-key assertion (code review #914 finding #5), not merely
	// "non-nil maps": #100 is left Working (no live window, no open PR) by
	// phase B and never driven further, so this real RunReconcileOnce pass
	// must start its grace-observation clock -- reconcile.go's first-seen
	// branch always records Observations[key] = Now on a ticket's very first
	// observation, even before GracePeriod elapses.
	key100 := h.repo + "#" + strconv.Itoa(graph.Parent.Number)
	if _, ok := reconcileState.Observations[key100]; !ok {
		t.Fatalf("persisted reconciliation state missing Observations[%q] for the stranded parent #%d, got %+v", key100, graph.Parent.Number, reconcileState)
	}

	// #101 was CLOSED by phase F's squash merge (chainPRMerge's
	// closing-issue logic, PR #103's ClosingIssues == [101]) -- expected
	// CLOSED here, OPEN through every earlier phase A-E assertion above.
	assertGoldenFidelityUnchanged(t, h, graph, "end of scenario", 101)
}

// chainDecisions is a small convenience wrapper: run a dry-run pass and
// return just the decisions, for an assertion that doesn't need the log text.
func chainDecisions(t *testing.T, cfg Config, mut *GHMutator) []Decision {
	t.Helper()
	decisions, _ := chainRunOnce(t, cfg, mut, true)
	return decisions
}

// chainGoldenMilestone mirrors the fixture's `{number,title}` milestone
// shape -- the same shape ghIssue.Milestone (*ghMilestone, chainfake_test.go)
// is modeled on, per Q3: preservation-only fidelity, never a new gh route.
type chainGoldenMilestone struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// decodeChainGoldenMilestone decodes one child/parent's raw fixture
// milestone field into chainGoldenMilestone.
func decodeChainGoldenMilestone(t *testing.T, raw json.RawMessage) chainGoldenMilestone {
	t.Helper()
	var m chainGoldenMilestone
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decoding fixture milestone %s: %v", string(raw), err)
	}
	return m
}

// assertGoldenFidelityUnchanged asserts every seeded issue's milestone, body,
// and state, and the parent's native sub-issue links, are still
// byte-identical (body) / correctly-tracked (state) against the golden
// fixture's own values -- proven after every real `gh issue edit` mutation
// the positive scenario drives (Q3: milestone/sub-issues modeled on ghWorld
// for preservation only; round-tripping through a new gh route/production
// consumer is explicitly out of scope). closedNumbers names the issue
// numbers expected CLOSED at this point in the scenario (code review #914
// finding #7: AC1 explicitly requires asserting state, not just labels --
// #101 flips to CLOSED once phase F's squash merge closes it via
// chainPRMerge's closing-issue logic); every other seeded issue is expected
// OPEN.
func assertGoldenFidelityUnchanged(t *testing.T, h *chainHarness, graph goldenGraph, step string, closedNumbers ...int) {
	t.Helper()

	world := h.world()
	closed := make(map[int]bool, len(closedNumbers))
	for _, n := range closedNumbers {
		closed[n] = true
	}

	assertBodyAndState := func(number int, wantBody string) {
		is := world.Issues[number]
		if is == nil {
			t.Fatalf("%s: world missing issue #%d", step, number)
		}
		if is.Body != wantBody {
			t.Fatalf("%s: issue #%d body = %q, want fixture body %q (byte-identical, AC1)", step, number, is.Body, wantBody)
		}
		wantState := "OPEN"
		if closed[number] {
			wantState = "CLOSED"
		}
		if got := is.effectiveState(); got != wantState {
			t.Fatalf("%s: issue #%d state = %q, want %q", step, number, got, wantState)
		}
	}

	wantParentMilestone := decodeChainGoldenMilestone(t, graph.Parent.Milestone)
	parent := world.Issues[graph.Parent.Number]
	if parent == nil {
		t.Fatalf("%s: world missing parent issue #%d", step, graph.Parent.Number)
	}
	if parent.Milestone == nil || parent.Milestone.Number != wantParentMilestone.Number || parent.Milestone.Title != wantParentMilestone.Title {
		t.Fatalf("%s: parent #%d milestone = %+v, want %+v", step, graph.Parent.Number, parent.Milestone, wantParentMilestone)
	}
	assertBodyAndState(graph.Parent.Number, graph.Parent.Body)

	wantSubIssues := make([]ghSubIssue, len(graph.Parent.SubIssues))
	for i, si := range graph.Parent.SubIssues {
		wantSubIssues[i] = ghSubIssue(si)
	}
	if !slices.Equal(parent.SubIssues, wantSubIssues) {
		t.Fatalf("%s: parent #%d subIssues = %+v, want %+v", step, graph.Parent.Number, parent.SubIssues, wantSubIssues)
	}

	for _, c := range graph.Children {
		wantChildMilestone := decodeChainGoldenMilestone(t, c.Milestone)
		child := world.Issues[c.Number]
		if child == nil {
			t.Fatalf("%s: world missing child issue #%d", step, c.Number)
		}
		if child.Milestone == nil || child.Milestone.Number != wantChildMilestone.Number || child.Milestone.Title != wantChildMilestone.Title {
			t.Fatalf("%s: child #%d milestone = %+v, want %+v", step, c.Number, child.Milestone, wantChildMilestone)
		}
		assertBodyAndState(c.Number, c.Body)
	}
}

// writeChainDispatchConfigFile marshals cfg's Repos/PlanRefined into the
// on-disk "dispatch" config.json block LoadConfig decodes, for the re-exec'd
// TestChainReconcileHelper process (chainfake_test.go) -- a separate OS
// process shares no Go memory with the parent test, so RunReconcileOnce's
// Config there must come from a real file, not the in-memory cfg value this
// test built via chainConfig.
func writeChainDispatchConfigFile(t *testing.T, path string, cfg Config) {
	t.Helper()
	planRefined := cfg.PlanRefined
	payload := struct {
		Dispatch dispatchFile `json:"dispatch"`
	}{
		Dispatch: dispatchFile{
			Repos:       cfg.Repos,
			PlanRefined: &planRefined,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling chain dispatch config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing chain dispatch config %s: %v", path, err)
	}
}

// chainBoolPtr/chainStrPtr/chainIntPtr are small pointer-literal helpers for
// chainOpenPRPage's injectable override fields (chainfake_test.go), which
// need *bool/*string/*int to distinguish "force this value" from "no
// override, use the real computed default".
func chainBoolPtr(b bool) *bool    { return &b }
func chainStrPtr(s string) *string { return &s }
func chainIntPtr(n int) *int       { return &n }

// chainChdir chdirs into dir for the duration of a babysit.Run call (its
// localRepoRoot() shells a bare `git rev-parse --show-toplevel` with no
// directory override), returning a restore function. Never combined with
// t.Parallel() in this file.
func chainChdir(t *testing.T, dir string) func() {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatal(err)
		}
	}
}

// chainMergeInvocations filters world's invocation log down to `pr merge`
// calls, for exact-argv assertions.
func chainMergeInvocations(world *ghWorld) []string {
	var out []string
	for _, inv := range world.Invocations {
		if strings.HasPrefix(inv, "pr merge ") {
			out = append(out, inv)
		}
	}
	return out
}

// readBabysitState globs stateDir for the one persisted babysit state file
// and decodes it, mirroring babysit's own statePath hashing scheme without
// depending on its unexported details.
func readBabysitState(t *testing.T, stateDir string) babysit.State {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(stateDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one babysit state file in %s, got %v", stateDir, matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var s babysit.State
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("decoding babysit state file %s: %v", matches[0], err)
	}
	return s
}

// -- 1. Cancelled CI stops before merge ---------------------------------------

func TestAutonomousChain_CancelledCIStopsBeforeMerge(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	setFleetAutomergeEnabled(t, true)

	h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	h.seedPR(ghPR{
		Number: 103, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main",
		ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Files: []string{"base.txt"},
	})
	h.seedChecks(103, ghCheck{Bucket: "pass", Name: "build", State: "SUCCESS"}, ghCheck{Bucket: "cancel", Name: "flaky", State: "CANCELLED"})

	restore := chainChdir(t, h.local)
	defer restore()
	stateDir := t.TempDir()
	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true}); err != nil {
		t.Fatalf("babysit.Run returned unexpected error: %v", err)
	}

	if len(chainMergeInvocations(h.world())) != 0 {
		t.Fatal("expected no gh pr merge invocation with a cancelled CI check")
	}
	st := readBabysitState(t, stateDir)
	if st.AutomergeReason != chainReasonCICheckCancelled {
		t.Fatalf("AutomergeReason = %q, want %q", st.AutomergeReason, chainReasonCICheckCancelled)
	}
	if containsStr(h.world().Issues[101].Labels, "Implemented") {
		t.Fatal("ticket must not transition to Implemented when CI was cancelled")
	}
}

// -- 2. Unresolved review feedback stops before merge -------------------------

func TestAutonomousChain_UnresolvedReviewStopsBeforeMerge(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	setFleetAutomergeEnabled(t, true)

	h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	h.seedPR(ghPR{
		Number: 103, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main",
		ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Files: []string{"base.txt"},
	})
	h.seedChecks(103, ghCheck{Bucket: "pass", Name: "build", State: "SUCCESS"})
	h.seedReview(103, "reviewer", "CHANGES_REQUESTED", "2026-01-01T00:00:00Z")

	// The unresolved CHANGES_REQUESTED review is still un-launched, so
	// tick's address-review launch trigger fires before automerge's own
	// hold reason is even evaluated (#975 gates every launch() call on a
	// recorded tmux session).
	installFakeTmux(t)
	restore := chainChdir(t, h.local)
	defer restore()
	stateDir := t.TempDir()
	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true, Session: chainFakeTmuxSession}); err != nil {
		t.Fatalf("babysit.Run returned unexpected error: %v", err)
	}

	if len(chainMergeInvocations(h.world())) != 0 {
		t.Fatal("expected no gh pr merge invocation with unresolved CHANGES_REQUESTED feedback")
	}
	st := readBabysitState(t, stateDir)
	if st.AutomergeReason != chainReasonReviewPending {
		t.Fatalf("AutomergeReason = %q, want %q", st.AutomergeReason, chainReasonReviewPending)
	}
	if containsStr(h.world().Issues[101].Labels, "Implemented") {
		t.Fatal("ticket must not transition to Implemented while review feedback is unresolved")
	}
}

// -- 3. Non-lean repository config stops before unattended planning ----------

func TestAutonomousChain_NonLeanRepoConfigStopsBeforeUnattendedPlanning(t *testing.T) {
	h := newChainHarness(t)
	// #877: use commitAndPushConfig (commits on origin, then fetches +
	// ff-only merges into local), not a bare writeCommittedConfig(h.local,
	// ...) -- the autonomy probe now only ever reads the remote-confirmed
	// refs/remotes/origin/main object (mainSyncResult.AutonomyRef), so a
	// config committed solely to local's main (never pushed) would resolve
	// RepoAutonomyMissing at that ref instead of the intended interactive
	// denial, defeating this test's purpose.
	h.commitAndPushConfig(interactiveConfigJSON)

	h.seedIssue(101, "Lean child", "First child, no dependency.", []string{"Refined"}, []string{"octocat"})

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a non-lean repo must never spawn an unattended planning session")
		return nil
	})

	cfg := chainConfig(h)
	decisions, log := chainRunOnce(t, cfg, &GHMutator{}, false)
	got := decisionFor(t, decisions, 101).Reason
	if got != reasonAutonomyInteractive {
		t.Fatalf("decision = %q, want %q", got, reasonAutonomyInteractive)
	}
	if strings.Contains(log, "not Planned") {
		t.Fatalf("must be denied via the autonomy gate, not fall through to \"not Planned\": %s", log)
	}
}

// -- 4. Stale resume routes through re-plan, cannot be stamped fresh ----------

func TestAutonomousChain_StaleResumeRoutesToReplanAndCannotBeStampedFreshByAnAnswer(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	staleSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	commitFile(t, h.local, "unrelated.txt", "advance past the draft's baseline")

	h.seedIssue(101, "Escalated child", "body", []string{"Input Needed"}, []string{"octocat"})
	anchorBody := "Question?\n<!-- cenci-planner-escalation:" + chainEscalationNonce + " -->"
	anchorID := h.addIssueComment(101, "cenci-bot", "Bot", "", anchorBody)

	writeChainPlan(t, h.local, 101, [][2]string{
		{"status", "awaiting-input"},
		{"planCommitSha", staleSHA},
		{"escalationNonce", chainEscalationNonce},
		{"escalationCommentId", strconv.FormatInt(anchorID, 10)},
	})

	_, check, err := pipeline.CheckPlan(pipeline.PlanCheckOpts{ID: "101", RepoRoot: h.local, RepoSlug: h.repo})
	if err != nil {
		t.Fatalf("CheckPlan returned unexpected error: %v", err)
	}
	if check.Decision != "awaiting-input" || check.DraftFreshness != "stale" {
		t.Fatalf("CheckPlan = %+v, want decision=awaiting-input draftFreshness=stale", check)
	}

	// An authorized human answer must never "stamp the draft fresh": re-running
	// CheckPlan afterwards must report the identical stale verdict, since
	// DraftFreshness is a pure git commits-behind computation, oblivious to the
	// comment thread an answer would satisfy.
	h.addIssueComment(101, "octocat", "User", "OWNER", "Proceed with approach A.")

	_, checkAfterAnswer, err := pipeline.CheckPlan(pipeline.PlanCheckOpts{ID: "101", RepoRoot: h.local, RepoSlug: h.repo})
	if err != nil {
		t.Fatalf("CheckPlan (after answer) returned unexpected error: %v", err)
	}
	if checkAfterAnswer.Decision != "awaiting-input" || checkAfterAnswer.DraftFreshness != "stale" {
		t.Fatalf("CheckPlan after an authorized answer = %+v, want it STILL stale (an answer must never stamp a stale draft fresh)", checkAfterAnswer)
	}
}

// -- 5. Failed main sync stops dependent pickup at the repository gate -------

func TestAutonomousChain_FailedMainSyncStopsDependentPickupAtRepoGate(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	// Diverge local and origin independently -- syncMain must classify this
	// MainSyncDiverged, gating EVERY ticket in the repo before any
	// ticket-level gate (including the dependency gate) is ever consulted.
	commitFile(t, h.local, "local-only.txt", "local change")
	commitFile(t, h.origin, "origin-only.txt", "origin change")

	h.seedIssue(101, "Closed dependency", "body", nil, nil)
	h.mutate(func(w *ghWorld) { w.Issues[101].State = "CLOSED" })
	h.seedIssue(102, "Dependent child", "Depends on #101", []string{"Refined"}, []string{"octocat"})

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a diverged repo must never dispatch any ticket")
		return nil
	})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, false)
	got := decisionFor(t, decisions, 102).Reason
	if got != reasonMainDiverged {
		t.Fatalf("decision = %q, want %q (the repo gate must short-circuit before the dependency gate)", got, reasonMainDiverged)
	}
	if strings.Contains(got, "dependency") {
		t.Fatalf("must not fall through to a dependency-gate reason, got %q", got)
	}
}

// -- 6. Permission-denied answerer never resumes (#882) ----------------------

// TestAutonomousChain_PermissionDeniedAnswererNeverResumes covers AC2/AC7:
// an organization member without CURRENT repository write permission cannot
// resume an escalated ticket even with an otherwise-trusted author
// association (MEMBER, the pre-#882 sole gate) -- zero label mutation, zero
// spawn. Drives resolveAnswerProbes/Decide/applyDispatch through the REAL
// production code against the fake `gh` write-permission route
// (chainCollaboratorPermission), never a mock of the gate itself.
func TestAutonomousChain_PermissionDeniedAnswererNeverResumes(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	h.seedIssue(101, "Escalated child", "body", []string{"Input Needed"}, []string{"octocat"})
	anchorBody := "Question?\n<!-- cenci-planner-escalation:" + chainEscalationNonce + " -->"
	anchorID := h.addIssueComment(101, "cenci-bot", "Bot", "", anchorBody)

	writeChainPlan(t, h.local, 101, [][2]string{
		{"status", "awaiting-input"},
		{"escalationNonce", chainEscalationNonce},
		{"escalationCommentId", strconv.FormatInt(anchorID, 10)},
	})

	// octocat carries MEMBER association (the pre-#882 sole authorization
	// gate) but is seeded with only "read" repository permission -- an
	// organization member without repository write access, AC2's exact
	// scenario -- so the reply must never resume even though it would have
	// under the old author_association-only rule.
	h.seedPermission("octocat", "read")
	h.addIssueComment(101, "octocat", "User", "MEMBER", "Go with approach A.")

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a permission-denied answerer must never spawn a resume session")
		return nil
	})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, false)
	decision := decisionFor(t, decisions, 101)
	if decision.Resume {
		t.Fatalf("decision = %+v, want Resume=false (permission denied)", decision)
	}
	if decision.Reason != reasonAnswerUnauthorized {
		t.Fatalf("decision reason = %q, want %q", decision.Reason, reasonAnswerUnauthorized)
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Input Needed") || containsStr(is.Labels, "Working") {
		t.Fatalf("labels must remain unchanged (+Input Needed, no Working) after a denied answer, got %v", is.Labels)
	}
}

// -- 7. Restart/reload: reconciliation state survives a re-exec -------------

// TestAutonomousChain_ReconcileStateSurvivesReExec covers AC2's re-exec half:
// the reconciliation state file's grace-observation clock and apply-retry
// counter must survive a fresh process re-exec across the same persisted
// state file (torn-write/held-lock risk, #883). #100 (left `Working` by
// TestAutonomousChain_PositiveEndToEnd's phase B, its window presumed dead)
// is the stranded subject, seeded fresh here rather than sharing that test's
// harness instance -- AC3's "one shared ghWorld per scenario group" scopes to
// related sub-cases sharing one harness, not to independent Test functions.
//
// #914 owns only this reconciliation-state half of AC2's "refine checkpoint
// and reconciliation state file" phrasing (Q1): the refine-checkpoint half
// (ensure-issue.sh crash-tested against the real on-disk checkpoint) is
// #913's own AC1 and is not duplicated here.
func TestAutonomousChain_ReconcileStateSurvivesReExec(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	h.seedIssue(100, "Stranded parent", "body", []string{"Working"}, []string{"octocat"})
	h.seedPermission("octocat", "write")

	// An ambiguity making the reconciler's own `gh issue edit` mutation fail
	// keeps #100 genuinely stranded: process A's first pass must both start
	// the grace-observation clock AND increment ApplyFailures.
	h.seedAmbiguity(ghAmbiguity{Route: "issue edit", Kind: "ambiguous-success"})

	cfg := chainConfig(h)
	configPath := filepath.Join(t.TempDir(), "config.json")
	writeChainDispatchConfigFile(t, configPath, cfg)
	statePath := filepath.Join(t.TempDir(), "reconcile.json")
	key := h.repo + "#100"

	// Seed a prior grace-clock observation, far enough in the past that
	// GracePeriod has already elapsed by the time process A's own pass runs.
	// watch/docs/dispatch-reconcile.md: a ticket without prior state is
	// always write-only on its first observation (firstSeen == Now, so
	// Now.Sub(firstSeen) == 0 is never >= a positive GracePeriod) -- a truly
	// virgin ticket's first-ever pass always defers, producing no apply
	// attempt at all. Seeding here is what lets process A's SINGLE pass both
	// re-confirm this same observation (re-injected on the apply failure
	// below) and genuinely attempt (and fail) its apply mutation.
	seededFirstSeen := time.Now().Add(-1 * time.Hour)
	if err := NewStateStore(statePath).Save(ReconcileState{
		Observations:  map[string]time.Time{key: seededFirstSeen},
		ApplyFailures: map[string]int{},
	}); err != nil {
		t.Fatalf("seeding prior reconcile state: %v", err)
	}

	// wantApplyError=true: this pass's own apply-mutation attempt against
	// #100 is expected to fail, since the "ambiguous-success" ambiguity
	// seeded above makes gh issue edit fail every time.
	h.reExecReconcile(t, configPath, statePath, true)
	stateA, err := NewStateStore(statePath).Load()
	if err != nil {
		t.Fatalf("process A: loading persisted reconcile state: %v", err)
	}
	obsA, ok := stateA.Observations[key]
	if !ok {
		t.Fatalf("process A: expected an Observations entry for %q, got %+v", key, stateA)
	}
	failA := stateA.ApplyFailures[key]
	// Exact expected count (code review #914 finding #6), not merely
	// "nonzero": process A runs exactly one reconcile pass against exactly
	// one seeded ambiguity, so the counter must be exactly 1.
	if failA != 1 {
		t.Fatalf("process A: expected ApplyFailures[%q] == 1, got %d (state=%+v)", key, failA, stateA)
	}

	// Process B re-execs against the IDENTICAL statePath: the grace clock
	// must NOT restart, and the apply-retry counter must be unchanged (no
	// new attempt happened between A and B beyond what A itself recorded).
	// wantApplyError=false: process B is a dry run (reExecReconcile's second
	// call against this harness) -- applyReconcile returns before any apply
	// attempt, so no error is expected.
	h.reExecReconcile(t, configPath, statePath, false)
	stateB, err := NewStateStore(statePath).Load()
	if err != nil {
		t.Fatalf("process B: loading persisted reconcile state: %v", err)
	}
	obsB, ok := stateB.Observations[key]
	if !ok {
		t.Fatalf("process B: expected Observations[%q] to survive re-exec, got %+v", key, stateB)
	}
	if !obsB.Equal(obsA) {
		t.Fatalf("process B: Observations[%q] = %v, want the identical first-seen timestamp %v (the grace clock must not restart across re-exec)", key, obsB, obsA)
	}
	if stateB.ApplyFailures[key] != failA {
		t.Fatalf("process B: ApplyFailures[%q] = %d, want unchanged %d", key, stateB.ApplyFailures[key], failA)
	}

	// A follow-up dispatch pass proves the lean-autonomy grant and the
	// active identity (assignee set) survived the re-exec unchanged.
	//
	// Code review #914 finding #9: h.seedPermission("octocat", "write")
	// above models a realistic write-permission identity for octocat, but
	// #100 in this scenario is picked up via the ordinary lean-autonomy
	// pickup gate, never through an Input-Needed escalation/resume flow --
	// so this follow-up pass never actually reaches resolveAnswerProbes'
	// write-permission check for #100, and the assertions below do NOT
	// exercise that verdict. TestAutonomousChain_PositiveEndToEnd's phase D
	// is what genuinely asserts resume authorization against a real
	// escalation (#882's write-permission gate); do not read this comment as
	// claiming coverage this test does not have.
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, true)
	if got := decisionFor(t, decisions, 100).Reason; got == reasonAutonomyInteractive {
		t.Fatalf("decision after re-exec = %q, the lean-autonomy grant must survive re-exec", got)
	}
	if is := h.world().Issues[100]; !containsStr(is.Assignees, "octocat") {
		t.Fatalf("expected identity (assignee octocat) to survive re-exec, got %v", is.Assignees)
	}
}

// -- 8. Restart/reload: pipeline stage state survives an in-process reload --

// TestAutonomousChain_PipelineStageStateSurvivesReload covers AC2's
// in-process-reload half for pipeline stage/artifact state (Decision:
// pipeline stage state is reloaded in-process, never re-exec'd --
// pipeline.GetArtifacts already re-reads from disk under a flock on every
// call, so a second independent call models the identical reload path a
// fresh process would take, at a fraction of the runtime).
func TestAutonomousChain_PipelineStageStateSurvivesReload(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedIssue(101, "Reload subject", "body", []string{"Refined"}, []string{"octocat"})

	if _, err := pipeline.Run(pipeline.Opts{Stage: "prepare", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("pipeline prepare failed: %v", err)
	}
	if _, err := pipeline.Run(pipeline.Opts{Stage: "await-input", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("pipeline await-input failed: %v", err)
	}
	writeChainPlan(t, h.local, 101, [][2]string{{"status", "awaiting-input"}})

	before, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: "101", RepoRoot: h.local})
	if err != nil {
		t.Fatalf("first GetArtifacts reload failed: %v", err)
	}
	after, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: "101", RepoRoot: h.local})
	if err != nil {
		t.Fatalf("second GetArtifacts reload failed: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("pipeline stage state changed across an in-process reload with no intervening mutation:\nbefore=%+v\nafter=%+v", before, after)
	}
	if after.Stage != pipeline.StageWaitingForInput {
		t.Fatalf("Stage = %q, want %q", after.Stage, pipeline.StageWaitingForInput)
	}
}

// -- 9. Restart/reload: babysit decision state survives an in-process reload

// TestAutonomousChain_BabysitDecisionStateSurvivesReload covers AC2's
// in-process-reload half for babysit's decision state (Decision:
// babysit.Run(--once) already reloads its state file per invocation -- no
// re-exec needed). A held (never merged) decision is used deliberately so
// the reload has genuinely nothing new to observe: an unresolved
// CHANGES_REQUESTED review holds the merge on both ticks identically.
func TestAutonomousChain_BabysitDecisionStateSurvivesReload(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	setFleetAutomergeEnabled(t, true)

	h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	h.seedPR(ghPR{
		Number: 103, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main",
		ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Files: []string{"base.txt"},
	})
	h.seedChecks(103, ghCheck{Bucket: "pass", Name: "build", State: "SUCCESS"})
	h.seedReview(103, "reviewer", "CHANGES_REQUESTED", "2026-01-01T00:00:00Z")

	// See the identical note in TestAutonomousChain_UnresolvedReviewStopsBeforeMerge.
	installFakeTmux(t)
	restore := chainChdir(t, h.local)
	defer restore()
	stateDir := t.TempDir()
	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true, Session: chainFakeTmuxSession}); err != nil {
		t.Fatalf("first babysit.Run returned unexpected error: %v", err)
	}
	before := readBabysitState(t, stateDir)

	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true, Session: chainFakeTmuxSession}); err != nil {
		t.Fatalf("second babysit.Run returned unexpected error: %v", err)
	}
	after := readBabysitState(t, stateDir)

	if before.AutomergeDecision != after.AutomergeDecision {
		t.Fatalf("AutomergeDecision changed across reload: before=%q after=%q", before.AutomergeDecision, after.AutomergeDecision)
	}
	if before.AutomergeReason != after.AutomergeReason {
		t.Fatalf("AutomergeReason changed across reload: before=%q after=%q", before.AutomergeReason, after.AutomergeReason)
	}
	if !reflect.DeepEqual(before.AutomergeConditions, after.AutomergeConditions) {
		t.Fatalf("AutomergeConditions changed across reload:\nbefore=%+v\nafter=%+v", before.AutomergeConditions, after.AutomergeConditions)
	}
	if before.FixAttempts != after.FixAttempts {
		t.Fatalf("FixAttempts changed across reload: before=%d after=%d", before.FixAttempts, after.FixAttempts)
	}
	if !slices.Equal(before.PendingKeys, after.PendingKeys) {
		t.Fatalf("PendingKeys changed across reload: before=%v after=%v", before.PendingKeys, after.PendingKeys)
	}
	// CurrentDelaySeconds is deliberately NOT asserted unchanged here: it is
	// babysit's own polling-cadence backoff (babysit.go's tick loop), which
	// intentionally increases on every consecutive quiet/held tick with
	// nothing new to observe -- that IS the mechanism working as designed,
	// not a decision-state leak. It is orthogonal to the decision fields
	// above (AutomergeDecision/Reason/Conditions/FixAttempts/PendingKeys),
	// which this test asserts survive the reload unchanged.
	if after.CurrentDelaySeconds < before.CurrentDelaySeconds {
		t.Fatalf("CurrentDelaySeconds must never DECREASE across a repeated held tick: before=%d after=%d", before.CurrentDelaySeconds, after.CurrentDelaySeconds)
	}
}

// -- 10. Chain-fake capability self-tests: one per ambiguity kind (AC3) ------
//
// Each test below proves one ambiguity kind against its real production-side
// classification BEFORE anything else in the suite consumes it (Implementation
// Order step 1; root AGENTS.md's "prose claim is not coverage" rule) --
// #915's negative variants build on this capability layer.

// TestChainFake_AmbiguousSuccess covers the "ambiguous-success" kind: the
// fake mutates the world (the server accepted the mutation) then returns a
// nonzero exit (the client saw a failure) -- proven directly against
// GHMutator.EditLabels, the real production TicketMutator implementation.
func TestChainFake_AmbiguousSuccess(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedIssue(101, "t", "body", []string{"Refined"}, []string{"octocat"})

	h.seedAmbiguity(ghAmbiguity{Route: "issue edit", OnCall: 1, Kind: "ambiguous-success"})

	mut := &GHMutator{}
	if err := mut.EditLabels(h.repo, 101, []string{"Working"}, nil); err == nil {
		t.Fatal("expected EditLabels to surface a client-visible failure (ambiguous success: the server accepted the mutation, the client saw an error)")
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Working") {
		t.Fatalf("expected the label mutation to have landed server-side despite the client-visible error, got labels %v", is.Labels)
	}
}

// TestChainFake_Indeterminate covers the "indeterminate" kind: `gh pr merge`
// exits 0 with NO mutation, feeding babysit's real reasonMergeIndeterminate
// classification (merge.go's post-merge refetch observing the PR still not
// MERGED).
func TestChainFake_Indeterminate(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	setFleetAutomergeEnabled(t, true)

	h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	h.seedPR(ghPR{
		Number: 103, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main",
		ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Files: []string{"base.txt"},
	})
	h.seedChecks(103, ghCheck{Bucket: "pass", Name: "build", State: "SUCCESS"})

	h.seedAmbiguity(ghAmbiguity{Route: "pr merge", OnCall: 1, Kind: "indeterminate"})

	restore := chainChdir(t, h.local)
	defer restore()
	stateDir := t.TempDir()
	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true}); err != nil {
		t.Fatalf("babysit.Run returned unexpected error: %v", err)
	}

	st := readBabysitState(t, stateDir)
	if st.AutomergeReason != chainReasonMergeIndeterminate {
		t.Fatalf("AutomergeReason = %q, want %q (gh pr merge exited zero but the PR never actually flipped to MERGED)", st.AutomergeReason, chainReasonMergeIndeterminate)
	}
	if h.world().PRs[103].effectiveState() == "MERGED" {
		t.Fatal("the indeterminate injection must leave the PR genuinely un-merged (exit 0, no mutation)")
	}
}

// TestChainFake_Truncation covers the "truncate" kind at BOTH consumers a
// >4MiB stdout body trips: babysit's ghOutputCap (failureClassTruncated) and
// dispatch's maxOpenPRStdoutBytes (OpenPRProbeMalformed, per errGhOutputTruncated's
// classification in openpr.go -- distinct from OpenPRProbeTruncated, which is
// reserved for the nested closingIssuesReferences pagination case, covered
// separately by TestChainFake_PaginationNestedTruncation below).
// Code review #914 finding #8: both subtests below are variants of ONE
// scenario (the truncate ambiguity kind, exercised at its two distinct
// consumer boundaries) -- the plan's own Alternatives Considered section
// explicitly rejects a fresh ghWorld per scenario for exactly this reason
// (AC3 requires one shared ghWorld per scenario group), so both share a
// single newChainHarness call here rather than each t.Run constructing its
// own.
func TestChainFake_Truncation(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	t.Run("dispatch openPRInventory boundary", func(t *testing.T) {
		h.seedIssue(101, "t", "body", []string{"Refined"}, []string{"octocat"})
		h.seedAmbiguity(ghAmbiguity{Route: "api graphql", OnCall: 1, Kind: "truncate"})

		cfg := chainConfig(h)
		decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, true)
		got := decisionFor(t, decisions, 101).Ticket.OpenPRProbe
		if got != OpenPRProbeMalformed {
			t.Fatalf("OpenPRProbe = %q, want %q (a >4MiB stdout body must trip dispatch's own maxOpenPRStdoutBytes cap)", got, OpenPRProbeMalformed)
		}
	})

	t.Run("babysit boundary", func(t *testing.T) {
		setFleetAutomergeEnabled(t, true)
		// Overwrites #101's whole record (h.seedIssue is create-or-overwrite)
		// with the labels/assignees this sub-case needs, independent of the
		// dry-run-only sub-case above (which never mutated #101).
		h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
		headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
		h.seedPR(ghPR{
			Number: 103, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main",
			ClosingIssues: []int{101}, Mergeable: "MERGEABLE", Files: []string{"base.txt"},
		})
		// "pr checks" is a distinct route key from "api graphql" above, with
		// its own independent RouteCalls ordinal counter -- OnCall:1 here
		// fires on this sub-case's own first `pr checks` call regardless of
		// how many other routes the shared world has already served.
		h.seedAmbiguity(ghAmbiguity{Route: "pr checks", OnCall: 1, Kind: "truncate"})

		restore := chainChdir(t, h.local)
		defer restore()
		stateDir := t.TempDir()
		// tick's own `pr checks` read (babysit.go) -- not
		// recheckAutomergeInputs' pre-merge re-check -- is what this
		// injection trips, so babysit.Run(--once) genuinely propagates the
		// upstream-read error (babysit.go's tick/Run: an Once-mode tick
		// error is returned directly), rather than swallowing it into a
		// same-pass "held" decision with a nil Run error. The upstream-read
		// failure is still recorded into state before Run returns
		// (recordUpstreamReadFailure -> recordHold, babysit.go's tick).
		if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true}); err == nil {
			t.Fatal("expected babysit.Run to propagate the pr-checks upstream-read failure, got nil error")
		}
		st := readBabysitState(t, stateDir)
		if st.AutomergeFailureClass != chainFailureClassTruncated {
			t.Fatalf("AutomergeFailureClass = %q, want %q", st.AutomergeFailureClass, chainFailureClassTruncated)
		}
	})
}

// TestChainFake_Timeout covers the "timeout" kind (Q2): a genuine
// errGhTimeout classification via a short-deadline seam (maxOpenPRProbeDuration
// overridden to ~100ms) plus a blocking (never sleeping) shim -- never a
// ghWaitDelay hold, which is already covered by dispatch/gh_test.go:44 and
// mainsync_test.go:447 and is not re-covered here.
func TestChainFake_Timeout(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedIssue(101, "t", "body", []string{"Refined"}, []string{"octocat"})

	orig := maxOpenPRProbeDuration
	maxOpenPRProbeDuration = 100 * time.Millisecond
	t.Cleanup(func() { maxOpenPRProbeDuration = orig })

	h.seedAmbiguity(ghAmbiguity{Route: "api graphql", OnCall: 1, Kind: "timeout"})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, true)
	got := decisionFor(t, decisions, 101).Ticket.OpenPRProbe
	if got != OpenPRProbeTimeout {
		t.Fatalf("OpenPRProbe = %q, want %q (a genuine errGhTimeout classification via the short-deadline seam)", got, OpenPRProbeTimeout)
	}
}

// TestChainFake_PaginationHasNextPage proves >100 seeded open PRs drive a
// REAL multi-page `gh api graphql` traversal (pageSlice already pages
// world.PRs -- no override needed for this one case).
func TestChainFake_PaginationHasNextPage(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedIssue(101, "t", "body", []string{"Refined"}, []string{"octocat"})

	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	for n := 200; n < 350; n++ {
		h.seedPR(ghPR{Number: n, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main", Mergeable: "MERGEABLE"})
	}

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, true)
	if got := decisionFor(t, decisions, 101).Ticket.OpenPRProbe; got != OpenPRProbeComplete {
		t.Fatalf("OpenPRProbe = %q, want %q once every page is traversed", got, OpenPRProbeComplete)
	}

	var pageCalls int
	for _, inv := range h.world().Invocations {
		if strings.Contains(inv, "graphql") && strings.Contains(inv, "closingIssuesReferences") {
			pageCalls++
		}
	}
	if pageCalls < 2 {
		t.Fatalf("expected a real multi-page traversal (>150 open PRs, page size 100), got %d graphql page call(s)", pageCalls)
	}
}

// TestChainFake_PaginationEndCursorMalformed covers the injectable
// non-advancing-cursor malformed case (chainOpenPRPage's endCursor override):
// hasNextPage true with a cursor identical to the one just consumed must
// classify OpenPRProbeMalformed, never loop forever.
func TestChainFake_PaginationEndCursorMalformed(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedIssue(101, "t", "body", []string{"Refined"}, []string{"octocat"})

	h.seedOpenPRPageOverride(ghOpenPRPageOverride{
		ForceHasNextPage: chainBoolPtr(true),
		ForceEndCursor:   chainStrPtr("1"), // identical to the cursor just consumed
	})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, true)
	if got := decisionFor(t, decisions, 101).Ticket.OpenPRProbe; got != OpenPRProbeMalformed {
		t.Fatalf("OpenPRProbe = %q, want %q (a non-advancing cursor must never be followed)", got, OpenPRProbeMalformed)
	}
}

// TestChainFake_PaginationTotalCountExceedsCap covers the injectable
// totalCount override: forcing a pre-flight totalCount above maxOpenPRRecords
// classifies OpenPRProbeCapExhausted without needing to seed 2000+ real PRs.
func TestChainFake_PaginationTotalCountExceedsCap(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedIssue(101, "t", "body", []string{"Refined"}, []string{"octocat"})

	h.seedOpenPRPageOverride(ghOpenPRPageOverride{ForceTotalCount: chainIntPtr(maxOpenPRRecords + 1)})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, true)
	if got := decisionFor(t, decisions, 101).Ticket.OpenPRProbe; got != OpenPRProbeCapExhausted {
		t.Fatalf("OpenPRProbe = %q, want %q", got, OpenPRProbeCapExhausted)
	}
}

// TestChainFake_PaginationNestedTruncation covers the injectable nested
// closingIssuesReferences.pageInfo.hasNextPage override: a PR whose own
// closing-issue references overflow their page bound must classify
// OpenPRProbeTruncated (distinct from OpenPRProbeMalformed/CapExhausted).
func TestChainFake_PaginationNestedTruncation(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	h.seedIssue(101, "t", "body", []string{"Refined"}, []string{"octocat"})

	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	h.seedPR(ghPR{Number: 103, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main", Mergeable: "MERGEABLE", ClosingIssues: []int{101}})
	h.seedOpenPRPageOverride(ghOpenPRPageOverride{ForceNestedHasNextPage: chainBoolPtr(true)})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, true)
	if got := decisionFor(t, decisions, 101).Ticket.OpenPRProbe; got != OpenPRProbeTruncated {
		t.Fatalf("OpenPRProbe = %q, want %q", got, OpenPRProbeTruncated)
	}
}

// TestChainFake_StateChangeBetweenEvaluations covers the "state-change" kind:
// a named declarative op (never a Go closure -- the gh handler runs in a
// re-exec'd process) fires on a specific ordinal call, reopening review
// feedback between babysit's first evaluation and recheckAutomergeInputs'
// pre-merge re-check (merge.go:73) -- the merge must be held, never fired,
// once the between-evaluations state change is discovered.
func TestChainFake_StateChangeBetweenEvaluations(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	setFleetAutomergeEnabled(t, true)

	h.seedIssue(101, "Granted ticket", "body", []string{"In Review", "automerge:ok"}, []string{"octocat"})
	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	// ChangedFiles: 1 is load-bearing (test-strategy.md: pin every dependent
	// field a fixture relies on, not just the one under test) -- without it
	// the FIRST evaluation itself already holds on "PR has no changed
	// files", so recheckAutomergeInputs' own second `pr view` (the ordinal
	// this ambiguity's OnCall:2 targets) is never reached at all, and this
	// test would pass for the wrong reason (any hold, not the intended
	// between-evaluations state change).
	h.seedPR(ghPR{
		Number: 103, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main",
		ClosingIssues: []int{101}, Mergeable: "MERGEABLE", ChangedFiles: 1, Files: []string{"base.txt"},
	})
	h.seedChecks(103, ghCheck{Bucket: "pass", Name: "build", State: "SUCCESS"})

	// OnCall:2 targets recheckAutomergeInputs' own re-read of `pr view`, NOT
	// tick's first evaluation (OnCall:1) -- the "first vs. final evaluation"
	// boundary this ambiguity kind exists to test.
	h.seedAmbiguity(ghAmbiguity{Route: "pr view", OnCall: 2, Kind: "state-change", Payload: "reopen-review"})

	restore := chainChdir(t, h.local)
	defer restore()
	stateDir := t.TempDir()
	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true}); err != nil {
		t.Fatalf("babysit.Run returned unexpected error: %v", err)
	}

	if len(chainMergeInvocations(h.world())) != 0 {
		t.Fatal("a state change discovered between the first and final evaluation must hold the merge, never let gh pr merge fire")
	}
	st := readBabysitState(t, stateDir)
	if st.AutomergeDecision != "held" {
		t.Fatalf("AutomergeDecision = %q, want %q (the pre-merge re-check must catch the between-evaluations state change)", st.AutomergeDecision, "held")
	}
	// Content-specific, not a bare hold: proves the SPECIFIC between-
	// evaluations state change (the reopened review, discovered only by
	// recheckAutomergeInputs' second `pr view`/reviews re-read) is what
	// held the merge -- not merely "some" unrelated hold reason, which
	// would let this test pass without the injection ever having fired.
	if st.AutomergeReason != chainReasonReviewPending {
		t.Fatalf("AutomergeReason = %q, want %q (the reopened review discovered at recheck)", st.AutomergeReason, chainReasonReviewPending)
	}
}

// TestChainFake_ScenarioInventoryIsFixed covers AC3's "fixed scenario count,
// bounded by construction": chainAmbiguityScenarios (the table #915's
// negative variants will drive via t.Run subtests over one shared harness)
// must never silently grow or shrink without a matching, deliberate update
// to chainScenarioCount.
func TestChainFake_ScenarioInventoryIsFixed(t *testing.T) {
	if len(chainAmbiguityScenarios) != chainScenarioCount {
		t.Fatalf("len(chainAmbiguityScenarios) = %d, want the fixed chainScenarioCount %d (bounded by construction, per AC3)", len(chainAmbiguityScenarios), chainScenarioCount)
	}
	seen := map[string]bool{}
	for _, sc := range chainAmbiguityScenarios {
		if seen[sc.Name] {
			t.Fatalf("duplicate scenario name %q in chainAmbiguityScenarios", sc.Name)
		}
		seen[sc.Name] = true
	}
}
