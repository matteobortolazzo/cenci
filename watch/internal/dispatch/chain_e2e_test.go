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
	"slices"
	"strconv"
	"strings"
	"testing"

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
)

// chainConfig builds a dispatch.Config enrolling h.repo at h.local, with
// stage-aware lean planning turned on. Individual tests override fields as
// needed (e.g. PlanRefined false for the non-lean negative variant).
func chainConfig(h *chainHarness) Config {
	cfg := testConfig()
	cfg.PlanRefined = true
	cfg.Repos = []RepoConfig{{Repo: h.repo, Dir: h.local}}
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

	// -- A. Refinement output: decision-complete child tickets --------------
	// #101 is the lean, dependency-free child; #102 declares "Depends on
	// #101" in its body, both left Refined (no plan file yet) with explicit
	// per-child automation verdicts asserted below via a dry-run pass.
	h.seedIssue(101, "Lean child", "First child, no dependency.", []string{"Refined", "automerge:ok"}, []string{"octocat"})
	h.seedIssue(102, "Dependent child", "Depends on #101\n\nSecond child.", []string{"Refined"}, []string{"octocat"})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, mut, true /* dry-run */)
	if got := decisionFor(t, decisions, 101).Reason; got != "plan — Refined, no plan file" {
		t.Fatalf("phase A: #101 decision = %q, want the lean planning-pickup verdict", got)
	}
	if got := decisionFor(t, decisions, 102).Reason; got != "waiting on dependency #101" {
		t.Fatalf("phase A: #102 decision = %q, want it decided waiting on its dependency", got)
	}

	// -- B. Lean pickup: fleet + repository authorization both present ------
	var captured []run.Opts
	stubRunFn(t, func(o run.Opts, _ run.Controller) error {
		captured = append(captured, o)
		return nil
	})

	decisions, log := chainRunOnce(t, cfg, mut, false /* real */)
	if len(captured) != 1 {
		t.Fatalf("phase B: expected exactly one real pickup, got %d: %+v", len(captured), captured)
	}
	if captured[0].Ticket != "101" || captured[0].WindowTicket != "101" {
		t.Fatalf("phase B: runFn Opts = %+v, want the bare ticket number 101", captured[0])
	}
	if got := decisionFor(t, decisions, 102).Reason; got != "waiting on dependency #101" {
		t.Fatalf("phase B: #102 must remain gated on its dependency, got %q", got)
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Working") {
		t.Fatalf("phase B: #101 must carry Working after the lean claim, got labels %v", is.Labels)
	}
	if strings.Contains(log, "dispatch: #102 run failed") {
		t.Fatalf("phase B: #102 must never attempt a spawn, log:\n%s", log)
	}
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
	if _, err := pipeline.Run(pipeline.Opts{Stage: "review", ID: "101", RepoRoot: h.local}); err != nil {
		t.Fatalf("phase E: pipeline review failed: %v", err)
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

	// -- F. Babysit: pass-only CI + resolved feedback -> immediate squash ----
	setFleetAutomergeEnabled(t, true)
	stateDir := t.TempDir()
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
}

// chainDecisions is a small convenience wrapper: run a dry-run pass and
// return just the decisions, for an assertion that doesn't need the log text.
func chainDecisions(t *testing.T, cfg Config, mut *GHMutator) []Decision {
	t.Helper()
	decisions, _ := chainRunOnce(t, cfg, mut, true)
	return decisions
}

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

	restore := chainChdir(t, h.local)
	defer restore()
	stateDir := t.TempDir()
	if err := babysit.Run(babysit.Options{PR: "103", Agent: "claude", StateDir: stateDir, Once: true}); err != nil {
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
	writeCommittedConfig(t, h.local, interactiveConfigJSON)

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
