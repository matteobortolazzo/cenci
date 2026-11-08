package dispatch

// Ticket #921 (child of #915): the dispatch-boundary adversarial negative
// test variants (2/2) -- an unreadable plan-inventory directory (6), a plan
// filename/front-matter identity mismatch and duplicate healthy plan claims
// (7), a linked PR beyond the first 200 open-PR results plus an incomplete
// pagination verdict (8), a corrupt reconciliation state file (9), and the
// remaining authorization clauses -- permission-probe fetch failure,
// local-only (unpushed) lean grant, remotely revoked lean grant (10) -- each
// its own named test driving real RunOnce/RunReconcileOnce over the chain
// fake (chainfake_test.go), asserting the exact production reason constant
// plus the absence of mutation/merge/spawn on every hold path. Variant (10)'s
// fourth clause -- an organization member without CURRENT repository write
// permission -- is proven by the existing
// TestAutonomousChain_PermissionDeniedAnswererNeverResumes
// (chain_e2e_test.go:844) and is cited, not duplicated, here.
//
// Never call t.Parallel() anywhere in this file (chainfake_test.go's
// os.Args[0] mutation is process-global, and babysit's localRepoRoot needs a
// stable t.Chdir) -- mirrors chain_e2e_test.go's and
// chain_negative_babysit_test.go's own rule.
//
// No production Go file is modified by this ticket (#887's test-and-docs-only
// decision), and chainfake_test.go is not modified (Q2): the permission-probe
// fetch-failure variant reuses the existing joined-argv fallback route
// ("api repos/o/r/collaborators/octocat/permission", chainRouteKey's default
// branch), never a new chainRouteKey case.
//
// Ticket #1464 adds section 11: the plan-authorization hold and the
// restart-safe escalation recovery, each driven across the real boundaries --
// plan persistence through pipeline.Run (`plan --authorization`), the real
// pipeline.ApplyLabelTransition, the real `cenci dispatch approve` engine
// (pipeline.ApproveHeldPlan), and real RunOnce/RunReconcileOnce passes whose
// answer probes go through resolveAnswerProbes. The harness pins the
// dispatcher's unattended posture (newChainHarness); only the human release
// flips it, for that one call.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// chainRunOnceExpectingError runs one real dispatch.RunOnce pass against cfg,
// like chainRunOnce (chain_e2e_test.go), but asserts RunOnce returns a
// NON-nil error instead of t.Fatalf-ing on one. Variant (6)'s unreadable
// `.plans` directory makes readPlansForRepos return an error that RunOnce
// propagates as its own collection error (dispatch.go:129-132,160) even
// though Decide still computes a full decision table over the resulting
// PlanInventoryUnreadable classification -- chainRunOnce's own unconditional
// t.Fatalf on any error would misreport that expected propagation as a
// harness failure. This helper lives here, never in chainfake_test.go (plan
// Assumptions), since only this one variant needs it.
func chainRunOnceExpectingError(t *testing.T, cfg Config, mut *GHMutator, dryRun bool) ([]Decision, string, error) {
	t.Helper()
	var buf bytes.Buffer
	decisions, err := RunOnce(cfg, fakeController{}, mut, dryRun, &buf, nil)
	if err == nil {
		t.Fatal("expected RunOnce to return a non-nil error (an unreadable .plans directory), got nil")
	}
	return decisions, buf.String(), err
}

// seedOpenPRs bulk-seeds n open PRs (numbered start..start+n-1), all headed
// at headSHA against "main" and Mergeable "MERGEABLE", in ONE h.mutate
// closure -- never n individual seedPR load/save cycles (plan Risks: 250
// sequential load/save cycles would be needlessly expensive). Only the
// highest-numbered PR (start+n-1) carries closingIssues, so
// chainOpenPRPage's ascending sort places the ticket-closing PR onto the
// LAST page of the traversal, genuinely exercising completeness beyond the
// first page rather than merely padding the PR count.
func seedOpenPRs(h *chainHarness, start, n int, headSHA string, closingIssues []int) {
	h.mutate(func(w *ghWorld) {
		for i := 0; i < n; i++ {
			num := start + i
			pr := ghPR{Number: num, HeadRefName: "main", HeadRefOID: headSHA, BaseRefName: "main", Mergeable: "MERGEABLE"}
			if i == n-1 {
				pr.ClosingIssues = closingIssues
			}
			w.PRs[num] = &pr
		}
	})
}

// -- 6. Unreadable plan directory holds every ticket in the repo -----------

// TestAutonomousChain_UnreadablePlanDirectoryHoldsEveryTicketInTheRepo proves
// #915's variant (6): a `.plans` directory that cannot be enumerated at all
// (chmod 0000) must hold EVERY ticket in that repo -- the hold is repo-wide
// (decide.go:335's planInventorySkip, evaluated before the plan lookup for
// any individual ticket), not scoped to whichever ticket's own plan file
// happens to be unreadable. Proven with two tickets on otherwise-distinct
// pickup paths (a fresh Refined planning candidate and an ordinary
// already-Planned ticket), both denied identically.
//
// Root-skip caveat (coverage-map fact, AC3): skipped under root, since chmod
// 0000 does not block root's own reads (mirrors state_test.go:48).
func TestAutonomousChain_UnreadablePlanDirectoryHoldsEveryTicketInTheRepo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 0000 does not block reads")
	}

	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	h.seedIssue(101, "Fresh planning candidate", "body", []string{"Refined"}, []string{"octocat"})
	h.seedIssue(102, "Ordinary Planned ticket", "body", []string{"Planned"}, []string{"octocat"})

	// writeChainPlan (for an unrelated ticket #999) creates .plans; its own
	// content is irrelevant once the directory itself is chmod 0000 -- an
	// unreadable directory can never be enumerated regardless of what it
	// holds.
	writeChainPlan(t, h.local, 999, nil)
	plansDir := filepath.Join(h.local, ".plans")
	if err := os.Chmod(plansDir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(plansDir, 0o755) }()

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an unreadable .plans directory must never authorize a spawn for any ticket in the repo")
		return nil
	})

	cfg := chainConfig(h)
	decisions, log, err := chainRunOnceExpectingError(t, cfg, &GHMutator{}, false)
	if !strings.Contains(err.Error(), "read .plans directory for") {
		t.Fatalf("RunOnce err = %v, want it to name the unreadable .plans directory read", err)
	}
	if !strings.Contains(log, "could not be fully read") {
		t.Fatalf("log = %q, want it to record the PlanInventoryUnreadable classification", log)
	}

	for _, number := range []int{101, 102} {
		got := decisionFor(t, decisions, number).Reason
		if got != reasonPlanInventoryUnreadable {
			t.Fatalf("#%d decision = %q, want %q", number, got, reasonPlanInventoryUnreadable)
		}
	}
	assertNoHoldPathSideEffects(t, h)
}

// -- 7. Plan filename/front-matter identity mismatch and duplicate claims --

// TestAutonomousChain_PlanIdentityMismatchHoldsBothClaimedTickets proves
// #915's variant (7)(a): a plan file named for one ticket but whose front
// matter claims another must hold BOTH claimed tickets with the identical
// reasonPlanProbeIdentityMismatch reason (collect.go's ReadPlans: a
// HealthIDMismatch entry records PlanProbeIDMismatch under both the
// filename's claim and the front matter's claim, Q2).
func TestAutonomousChain_PlanIdentityMismatchHoldsBothClaimedTickets(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	h.seedIssue(101, "Filename claim", "body", []string{"Refined"}, []string{"octocat"})
	h.seedIssue(102, "Front-matter claim", "body", []string{"Refined"}, []string{"octocat"})

	// The filename claims #101; the front-matter ticketId claims #102.
	writePlan(t, h.local, "101-chain.md", "---\nticketId: 102\nstatus: planned\n---\nbody\n")

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a plan identity mismatch must never authorize a spawn under either claimed ticket")
		return nil
	})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, false)
	for _, number := range []int{101, 102} {
		got := decisionFor(t, decisions, number).Reason
		if got != reasonPlanProbeIdentityMismatch {
			t.Fatalf("#%d decision = %q, want %q", number, got, reasonPlanProbeIdentityMismatch)
		}
	}
	assertNoHoldPathSideEffects(t, h)
}

// TestAutonomousChain_DuplicatePlanClaimsHoldTheTicket proves #915's variant
// (7)(b): two individually-healthy plan files both claiming the same ticket
// key resolve planfile.SelectAmbiguous, never a first-wins resolution, and
// must hold the ticket with reasonPlanProbeAmbiguous.
func TestAutonomousChain_DuplicatePlanClaimsHoldTheTicket(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	h.seedIssue(101, "Duplicated ticket", "body", []string{"Refined"}, []string{"octocat"})

	writeChainPlan(t, h.local, 101, [][2]string{{"status", "planned"}})
	// writeChainPlan hardcodes "<n>-chain.md", so the second individually-
	// healthy claim on the same ticket key goes through writePlan directly
	// (collect_test.go:314).
	writePlan(t, h.local, "101-second.md", "---\nticketId: 101\nstatus: planned\n---\nbody\n")

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("two individually-healthy claims on one ticket key must never authorize a spawn")
		return nil
	})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, false)
	got := decisionFor(t, decisions, 101).Reason
	if got != reasonPlanProbeAmbiguous {
		t.Fatalf("decision = %q, want %q", got, reasonPlanProbeAmbiguous)
	}
	assertNoHoldPathSideEffects(t, h)
}

// -- 8. Linked PR beyond 200 results, and incomplete pagination -------------

// TestAutonomousChain_LinkedPRBeyondFirstTwoHundredResultsStillPreventsDuplicateDispatch
// proves #915's variant (8)(a): 250 open PRs (page size 100, so a real
// three-page traversal) with only the highest-numbered PR (sorted onto the
// LAST page) closing the ticket -- the open-PR-exists gate must still fire,
// proving completeness genuinely covers pages beyond the first two hundred
// results, not merely the first page. dryRun=false with stubRunFn fataling
// proves an actual non-spawn, unlike the existing dry-run, verdict-only
// TestChainFake_PaginationHasNextPage.
func TestAutonomousChain_LinkedPRBeyondFirstTwoHundredResultsStillPreventsDuplicateDispatch(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	h.seedIssue(101, "Already has an open PR", "body", []string{"Planned"}, []string{"octocat"})

	headSHA := gitTest(t, h.local, "rev-parse", "HEAD")
	seedOpenPRs(h, 1, 250, headSHA, []int{101})

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a linked PR beyond the first 200 results must still prevent a duplicate dispatch")
		return nil
	})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, false)
	decision := decisionFor(t, decisions, 101)
	if decision.Ticket.OpenPRProbe != OpenPRProbeComplete {
		t.Fatalf("OpenPRProbe = %q, want %q", decision.Ticket.OpenPRProbe, OpenPRProbeComplete)
	}
	if decision.Reason != "open PR exists" {
		t.Fatalf("decision reason = %q, want %q", decision.Reason, "open PR exists")
	}

	var pageCalls []string
	for _, inv := range h.world().Invocations {
		if strings.Contains(inv, "graphql") && strings.Contains(inv, "closingIssuesReferences") {
			pageCalls = append(pageCalls, inv)
		}
	}
	if len(pageCalls) != 3 {
		t.Fatalf("graphql page calls = %v, want exactly 3 (250 PRs, page size 100)", pageCalls)
	}
	if strings.Contains(pageCalls[0], "cursor=") {
		t.Fatalf("first page call = %q, want no cursor argument", pageCalls[0])
	}
	if !strings.Contains(pageCalls[1], "cursor=2") {
		t.Fatalf("second page call = %q, want cursor=2", pageCalls[1])
	}
	if !strings.Contains(pageCalls[2], "cursor=3") {
		t.Fatalf("third page call = %q, want cursor=3", pageCalls[2])
	}
	assertNoHoldPathSideEffects(t, h)
}

// TestAutonomousChain_OpenPRPaginationCapExhaustedHoldsPickup proves #915's
// variant (8)(b): the injectable pre-flight totalCount override (Q3) forces
// maxOpenPRRecords+1 on the very first page, so the pagination cap fires
// before ever fetching a second page -- exactly one `gh api graphql`
// invocation -- and the incomplete OpenPRProbeCapExhausted verdict must hold
// the ticket, never fall through as if HasOpenPR were verifiably false.
func TestAutonomousChain_OpenPRPaginationCapExhaustedHoldsPickup(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	h.seedIssue(101, "Cap-exhausted pickup", "body", []string{"Planned"}, []string{"octocat"})
	h.seedOpenPRPageOverride(ghOpenPRPageOverride{ForceTotalCount: chainIntPtr(maxOpenPRRecords + 1)})

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an incomplete open-PR pagination verdict must never authorize a dispatch")
		return nil
	})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, false)
	decision := decisionFor(t, decisions, 101)
	if decision.Ticket.OpenPRProbe != OpenPRProbeCapExhausted {
		t.Fatalf("OpenPRProbe = %q, want %q", decision.Ticket.OpenPRProbe, OpenPRProbeCapExhausted)
	}
	if decision.Reason != reasonOpenPRCapExhausted {
		t.Fatalf("decision reason = %q, want %q", decision.Reason, reasonOpenPRCapExhausted)
	}

	var pageCalls int
	for _, inv := range h.world().Invocations {
		if strings.Contains(inv, "graphql") && strings.Contains(inv, "closingIssuesReferences") {
			pageCalls++
		}
	}
	if pageCalls != 1 {
		t.Fatalf("graphql page calls = %d, want exactly 1 (the pre-flight short-circuit, openpr.go:203-206)", pageCalls)
	}
	assertNoHoldPathSideEffects(t, h)
}

// -- 9. Corrupt reconciliation state holds without mutating or overwriting --

// TestAutonomousChain_CorruptReconcileStateHoldsWithoutMutatingOrOverwriting
// proves #915's variant (9): a torn-write-corrupted reconcile-state file
// makes RunReconcileOnce's pre-collection hold probe (reconcile_run.go:
// 276-279) return before CollectTickets is ever called -- a corrupt state
// file burns ZERO gh call budget, not merely zero mutation -- and the
// returned error is the exact ErrReconcileStateUnreadable sentinel
// (StateProbeDecodeError), never masked by a same-pass collect error even
// though a collect-breaking ambiguity is seeded anyway (#886's sentinel-wins
// branch, reconcile_run.go:349-355). Driven in-process (Q4): a cross-process
// re-exec (h.reExecReconcile) could only surface the child's stringified
// error, never a real errors.Is(err, ErrReconcileStateUnreadable) -- the
// process-boundary property itself is proven separately by
// TestAutonomousChain_ReconcileStateSurvivesReExec.
func TestAutonomousChain_CorruptReconcileStateHoldsWithoutMutatingOrOverwriting(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	// Seeded anyway, per Q4: proves the sentinel wins even though a same-pass
	// collect failure was also possible -- though the pre-collection hold
	// probe below means CollectTickets (and this ambiguity) is never actually
	// reached this pass.
	h.seedIssue(101, "Stranded ticket", "body", []string{"Working"}, []string{"octocat"})
	h.seedAmbiguity(ghAmbiguity{Route: "issue list", Kind: "ambiguous-success"})

	statePath := filepath.Join(t.TempDir(), "reconcile.json")
	if err := NewStateStore(statePath).Save(ReconcileState{
		Observations:  map[string]time.Time{h.repo + "#101": time.Now()},
		ApplyFailures: map[string]int{},
	}); err != nil {
		t.Fatalf("seeding valid state: %v", err)
	}
	truncateReconcileState(t, statePath)

	corrupted, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading corrupted state file: %v", err)
	}
	beforeHash := sha256.Sum256(corrupted)

	cfg := chainConfig(h)
	var buf bytes.Buffer
	_, runErr := RunReconcileOnce(cfg, &GHMutator{}, false, &buf, NewStateStore(statePath))
	if !errors.Is(runErr, ErrReconcileStateUnreadable) {
		t.Fatalf("RunReconcileOnce err = %v, want errors.Is(err, ErrReconcileStateUnreadable)", runErr)
	}
	var loadErr *StateLoadError
	if !errors.As(runErr, &loadErr) || loadErr.Probe != StateProbeDecodeError {
		t.Fatalf("RunReconcileOnce err = %v, want a *StateLoadError with Probe=%q", runErr, StateProbeDecodeError)
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading state file after the pass: %v", err)
	}
	afterHash := sha256.Sum256(after)
	if beforeHash != afterHash {
		t.Fatal("the corrupt state file must remain byte-identical after the pass (never overwritten)")
	}

	if calls := chainInvocationsWithPrefix(h.world(), "issue list "); len(calls) != 0 {
		t.Fatalf("gh issue list invocations = %v, want none (the pre-collection hold probe must return before CollectTickets ever runs)", calls)
	}
	assertNoHoldPathSideEffects(t, h)
}

// -- 10. Remaining authorization clauses -------------------------------------

// TestAutonomousChain_PermissionProbeFetchFailureNeverResumes proves #915's
// variant (10)'s first remaining clause: the collaborator-permission `gh`
// call itself failing (never merely a denied permission value) must resolve
// reasonAnswerPermissionAPIError and never resume. Mirrors
// TestAutonomousChain_PermissionDeniedAnswererNeverResumes's fixture minus
// seedPermission, injecting the fetch failure instead. The seeded Route
// string is the joined-argv fallback (Q2), pinned to fetchWritePermission's
// own endpoint construction: fmt.Sprintf("repos/%s/collaborators/%s/permission",
// repo, login) (permission.go:91).
func TestAutonomousChain_PermissionProbeFetchFailureNeverResumes(t *testing.T) {
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

	h.addIssueComment(101, "octocat", "User", "MEMBER", "Go with approach A.")

	h.seedAmbiguity(ghAmbiguity{Route: "api repos/o/r/collaborators/octocat/permission", Kind: "ambiguous-success"})

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a permission-probe fetch failure must never spawn a resume session")
		return nil
	})

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, &GHMutator{}, false)
	decision := decisionFor(t, decisions, 101)
	if decision.Resume {
		t.Fatalf("decision = %+v, want Resume=false (permission probe fetch failure)", decision)
	}
	if decision.Reason != reasonAnswerPermissionAPIError {
		t.Fatalf("decision reason = %q, want %q", decision.Reason, reasonAnswerPermissionAPIError)
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Input Needed") || containsStr(is.Labels, "Working") {
		t.Fatalf("labels must remain unchanged (+Input Needed, no Working) after a failed permission probe, got %v", is.Labels)
	}
	assertNoHoldPathSideEffects(t, h)
}

// TestAutonomousChain_LocalOnlyLeanGrantNeverAuthorizesPlanning proves #915's
// variant (10)'s second remaining clause: a lean grant committed only to the
// LOCAL checkout's main, never pushed, must never authorize unattended
// planning. syncMain classifies "local ahead of origin" as MainSyncSynced
// (mainsync.go:265, verified) -- so the repo gate does not short-circuit and
// the autonomy probe genuinely runs at AutonomyRef (origin/main, which never
// received this commit): `git ls-tree` at that ref finds no .cenci/config.json
// at all, resolving RepoAutonomyMissing.
func TestAutonomousChain_LocalOnlyLeanGrantNeverAuthorizesPlanning(t *testing.T) {
	h := newChainHarness(t)
	writeCommittedConfig(t, h.local, chainRepoConfigLean)

	h.seedIssue(101, "Lean child", "First child, no dependency.", []string{"Refined"}, []string{"octocat"})

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a local-only unpushed lean grant must never authorize unattended planning")
		return nil
	})

	cfg := chainConfig(h)
	decisions, log := chainRunOnce(t, cfg, &GHMutator{}, false)
	got := decisionFor(t, decisions, 101).Reason
	if got != reasonAutonomyMissing {
		t.Fatalf("decision = %q, want %q", got, reasonAutonomyMissing)
	}
	if strings.Contains(log, "not Planned") {
		t.Fatalf("must be denied via the autonomy gate, not fall through to \"not Planned\": %s", log)
	}
	assertNoHoldPathSideEffects(t, h)
}

// TestAutonomousChain_RemotelyRevokedLeanGrantStopsPlanning proves #915's
// variant (10)'s third remaining clause (Q1): a lean grant is pushed and
// genuinely observed lean by a first (dry-run) pass -- dryRun skips both the
// spawn and the Working-label claim (applyDispatch's own dryRun
// short-circuit), so the ticket remains an untouched Refined-only planning
// candidate -- then ORIGIN's committed config alone flips lean -> interactive
// strictly between the two passes. The second, real pass must resolve
// reasonAutonomyInteractive: a distinct scenario from
// TestAutonomousChain_NonLeanRepoConfigStopsBeforeUnattendedPlanning (which
// never held a lean grant at all), reusing the same constant deliberately
// (Q1).
func TestAutonomousChain_RemotelyRevokedLeanGrantStopsPlanning(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	h.seedIssue(101, "Lean child", "First child, no dependency.", []string{"Refined"}, []string{"octocat"})

	cfg := chainConfig(h)

	firstDecisions, _ := chainRunOnce(t, cfg, &GHMutator{}, true /* dry-run: observe lean without mutating */)
	firstDecision := decisionFor(t, firstDecisions, 101)
	if firstDecision.Action != ActionDispatch || !firstDecision.Planning {
		t.Fatalf("first pass decision = %+v, want a genuine lean-authorized planning dispatch", firstDecision)
	}

	// Revocation observable only through the remote-confirmed AutonomyRef
	// (Q1): committed to ORIGIN directly, never local.
	writeCommittedConfig(t, h.origin, interactiveConfigJSON)

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a remotely revoked lean grant must never authorize unattended planning")
		return nil
	})

	decisions, log := chainRunOnce(t, cfg, &GHMutator{}, false)
	got := decisionFor(t, decisions, 101).Reason
	if got != reasonAutonomyInteractive {
		t.Fatalf("decision = %q, want %q", got, reasonAutonomyInteractive)
	}
	if strings.Contains(log, "not Planned") {
		t.Fatalf("must be denied via the autonomy gate, not fall through to \"not Planned\": %s", log)
	}
	if is := h.world().Issues[101]; containsStr(is.Labels, "Planned") || containsStr(is.Labels, "Working") {
		t.Fatalf("labels must remain unchanged after a denied planning pickup, got %v", is.Labels)
	}
	assertNoHoldPathSideEffects(t, h)
}

// -- 11. #1464: plan-authorization hold and escalation recovery -------------

// The scenario bodies appended after requiredPlanBody, one per reason Phase
// 1's `## New Plan` fallback is taken. The fallback choice itself belongs to
// the Markdown procedure (pinned by flow's contract tests); what the tests
// below prove is that each such plan, once persisted with
// `--authorization held`, reaches the same held outcome at the watch
// boundary whatever its content says.
const (
	chainSensitivePathPlanBody = "\n### Files to Modify\n- `watch/internal/pipeline/authorization.go` -- matches the sensitive-path backstop's `auth` pattern.\n"
	chainOpenQuestionsPlanBody = "\n### Open Questions\n- Should the retry budget be per repository or fleet-wide? (unresolved)\n"
	chainLeanDecidedPlanBody   = "\n## Q&A from Planning\ndecided: keep the default interval -- matches the existing loop (alternative: shorten it)\n\n### Open Questions\n- Should the launch token rotate on every launch? (security-sensitive, unresolved)\n"
)

// chainOtherEscalationNonce is a second well-formed nonce, distinct from
// chainEscalationNonce, for the escalation-scoping test.
const chainOtherEscalationNonce = "fedcba9876543210fedcba9876543210"

func chainPipelineStatePath(h *chainHarness, number int) string {
	return filepath.Join(h.local, ".cenci", "pipeline", strconv.Itoa(number)+".json")
}

func chainReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

// chainLogHasLine reports whether log carries line as one whole line.
func chainLogHasLine(log, line string) bool {
	return slices.Contains(strings.Split(log, "\n"), line)
}

// chainIssueEdits counts the `gh issue edit` calls (every label mutation)
// the fake has served so far.
func chainIssueEdits(h *chainHarness) int {
	return len(chainInvocationsWithPrefix(h.world(), "issue edit "))
}

// chainNewPlanFallback replays Phase 1's `## New Plan` fallback for ticket
// number the way an unattended planning session runs it, through the real
// pipeline boundary: the ticket is claimed (Refined + Working), `pipeline
// prepare` runs, the plan is written with front matter `approval: <approval>`
// plus body, `pipeline plan --authorization held` records the hold, and the
// real `planned` transition applies +Planned -Working. It then asserts the
// persisted outcome the hold ACs name: stage waiting_for_plan_approval, a held
// record bound to the plan's exact bytes, Planned on and Working off. It
// returns the plan's path.
func chainNewPlanFallback(t *testing.T, h *chainHarness, number int, approval, body string) string {
	t.Helper()
	id := strconv.Itoa(number)
	h.seedIssue(number, "Held plan", "body", []string{"Refined", "Working"}, []string{"octocat"})
	if _, err := pipeline.Run(pipeline.Opts{Stage: "prepare", ID: id, RepoRoot: h.local}); err != nil {
		t.Fatalf("pipeline prepare #%d: %v", number, err)
	}
	head := gitTest(t, h.local, "rev-parse", "HEAD")
	content := "---\nticketId: " + id + "\nslug: chain\nstatus: planned\napproval: " + approval + "\nplanCommitSha: " + head + "\n---\n" + requiredPlanBody + body
	name := id + "-chain.md"
	writePlan(t, h.local, name, content)
	if _, err := pipeline.Run(pipeline.Opts{Stage: "plan", ID: id, RepoRoot: h.local, Authorization: string(pipeline.AuthorizationHeld)}); err != nil {
		t.Fatalf("pipeline plan #%d --authorization held: %v", number, err)
	}
	if _, err := pipeline.ApplyLabelTransition(pipeline.LabelOpts{ID: id, RepoRoot: h.local, RepoSlug: h.repo, Transition: "planned"}); err != nil {
		t.Fatalf("planned label transition #%d: %v", number, err)
	}

	planPath := filepath.Join(h.local, ".plans", name)
	if got := string(chainReadFile(t, planPath)); !chainLogHasLine(got, "approval: "+approval) {
		t.Fatalf("plan front matter = %q, want an `approval: %s` line", got, approval)
	}
	st, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: id, RepoRoot: h.local})
	if err != nil {
		t.Fatalf("reloading #%d state: %v", number, err)
	}
	if st.Stage != pipeline.StageWaitingForPlanApproval {
		t.Fatalf("#%d stage = %q, want %q", number, st.Stage, pipeline.StageWaitingForPlanApproval)
	}
	a, class := pipeline.ParseAuthorization(st.Authorization)
	if class != pipeline.AuthorizationRecorded || a.Kind != pipeline.AuthorizationHeld || a.PlanSHA256 != pipeline.PlanFingerprint(content) {
		t.Fatalf("#%d authorization = %+v (%s), want held bound to the plan's sha256 %s", number, a, class, pipeline.PlanFingerprint(content))
	}
	if is := h.world().Issues[number]; !containsStr(is.Labels, "Planned") || containsStr(is.Labels, "Working") {
		t.Fatalf("#%d labels = %v, want +Planned -Working", number, is.Labels)
	}
	return planPath
}

// chainCapturePass runs one real RunOnce pass with a fresh GHMutator (as a
// restarted daemon builds one), recording every run.Run the pass makes.
func chainCapturePass(t *testing.T, h *chainHarness, cfg Config, dryRun bool) ([]run.Opts, []Decision, string) {
	t.Helper()
	var captured []run.Opts
	stubRunFn(t, func(o run.Opts, _ run.Controller) error {
		captured = append(captured, o)
		return nil
	})
	decisions, log := chainRunOnce(t, cfg, &GHMutator{}, dryRun)
	return captured, decisions, log
}

// chainHeldPass runs one real pass over the on-disk state and asserts it is
// inert for ticket number: a skip with wantReason, rendered in the decision
// table as `<repo>#<n> skip: <wantReason>`, zero run.Run calls, zero `gh
// issue edit` calls, and a byte-identical pipeline state file (so no stage
// change) and label set. It returns the decision line, so a restarted pass
// can be compared with the first one.
func chainHeldPass(t *testing.T, h *chainHarness, cfg Config, number int, wantReason string) string {
	t.Helper()
	statePath := chainPipelineStatePath(h, number)
	stateBefore := chainReadFile(t, statePath)
	labelsBefore := append([]string{}, h.world().Issues[number].Labels...)
	editsBefore := chainIssueEdits(h)

	captured, decisions, log := chainCapturePass(t, h, cfg, false)
	if d := decisionFor(t, decisions, number); d.Action != ActionSkip || d.Reason != wantReason {
		t.Fatalf("#%d decision = %+v, want a skip with %q", number, d, wantReason)
	}
	line := fmt.Sprintf("%s#%d skip: %s", h.repo, number, wantReason)
	if !chainLogHasLine(log, line) {
		t.Fatalf("decision table missing the line %q:\n%s", line, log)
	}
	if len(captured) != 0 {
		t.Fatalf("run.Run calls = %+v, want none for a held plan", captured)
	}
	if n := chainIssueEdits(h); n != editsBefore {
		t.Fatalf("gh issue edit calls went from %d to %d, want no label edit for a held plan", editsBefore, n)
	}
	if after := chainReadFile(t, statePath); !bytes.Equal(after, stateBefore) {
		t.Fatalf("#%d pipeline state changed during a held pass:\nbefore %s\nafter  %s", number, stateBefore, after)
	}
	if labels := h.world().Issues[number].Labels; !slices.Equal(labels, labelsBefore) {
		t.Fatalf("#%d labels = %v, want them unchanged at %v", number, labels, labelsBefore)
	}
	return line
}

// chainApproveAsHuman releases ticket number's plan the way `cenci dispatch
// approve <id>` does (the command is a thin printer over this engine; its
// exit codes and output are pinned in watch/dispatch_approve_cmd_test.go),
// from an attended session: the harness's unattended pin is lifted for this
// one call only.
func chainApproveAsHuman(t *testing.T, h *chainHarness, number int) pipeline.ApproveResult {
	t.Helper()
	t.Setenv(pipeline.AttendedEnv, "1")
	defer t.Setenv(pipeline.AttendedEnv, "0")
	res, err := pipeline.ApproveHeldPlan(pipeline.ApproveOpts{ID: strconv.Itoa(number), RepoRoot: h.local})
	if err != nil {
		t.Fatalf("approving #%d: %v", number, err)
	}
	return res
}

// chainAssertRelease asserts res released planPath's exact bytes (wantSha),
// replacing a record of kind wantReplaced, and that ticket number's state now
// carries a human (source cli) record bound to wantSha.
func chainAssertRelease(t *testing.T, h *chainHarness, number int, res pipeline.ApproveResult, planPath, wantSha, wantReplaced string) {
	t.Helper()
	wantSuffix := filepath.Join(".plans", filepath.Base(planPath))
	if !res.Released || !strings.HasSuffix(res.PlanPath, wantSuffix) || res.Fingerprint != wantSha || res.ReplacedKind != wantReplaced {
		t.Fatalf("approve result = %+v, want Released for %s (sha256 %s) replacing %q", res, wantSuffix, wantSha, wantReplaced)
	}
	st, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: strconv.Itoa(number), RepoRoot: h.local})
	if err != nil {
		t.Fatalf("reloading #%d state: %v", number, err)
	}
	a, class := pipeline.ParseAuthorization(st.Authorization)
	if class != pipeline.AuthorizationRecorded || a.Kind != pipeline.AuthorizationHuman || a.Source != pipeline.AuthorizationSourceCLI || a.PlanSHA256 != wantSha {
		t.Fatalf("#%d authorization = %+v (%s), want human/cli bound to %s", number, a, class, wantSha)
	}
}

// chainAssertSingleLaunch asserts the pass made exactly one run.Run, an
// ordinary (non-resume, non-planning) dispatch of ticket number with
// planPath's `.plans/<file>` argument into the repo's configured session.
func chainAssertSingleLaunch(t *testing.T, captured []run.Opts, decisions []Decision, number int, planPath string) {
	t.Helper()
	d := decisionFor(t, decisions, number)
	if d.Action != ActionDispatch || d.Resume || d.Planning || d.Reason != "dispatch" || d.Note != "" {
		t.Fatalf("#%d decision = %+v, want an ordinary dispatch with no note", number, d)
	}
	wantArg := filepath.Join(".plans", filepath.Base(planPath))
	if len(captured) != 1 || captured[0].Ticket != wantArg || captured[0].Session != "chain-session" || !captured[0].Unattended {
		t.Fatalf("run.Run calls = %+v, want exactly one unattended launch of %q in session chain-session", captured, wantArg)
	}
}

// chainEscalate replays an unattended planner escalation for ticket number:
// it seeds the claimed ticket (Refined + Working), posts the trusted anchor
// comment carrying nonce, and hands off to chainEscalateWithAnchor. It
// returns the anchor's comment id and the draft's path.
func chainEscalate(t *testing.T, h *chainHarness, number int, nonce string) (int64, string) {
	t.Helper()
	h.seedIssue(number, "Escalated ticket", "body", []string{"Refined", "Working"}, []string{"octocat"})
	anchorID := h.addIssueComment(number, "cenci-bot", "Bot", "", "Which approach should the planner take: A or B?\n<!-- cenci-planner-escalation:"+nonce+" -->")
	return anchorID, chainEscalateWithAnchor(t, h, number, nonce, anchorID)
}

// chainEscalateWithAnchor runs the pipeline half of an escalation for an
// already-seeded ticket, as Phase 1 does: `pipeline prepare` and `pipeline
// await-input`, the awaiting-input draft recording anchorID and nonce
// verbatim (whichever ticket's thread actually holds them), and the real
// `input-needed` transition (+Input Needed -Working).
func chainEscalateWithAnchor(t *testing.T, h *chainHarness, number int, nonce string, anchorID int64) string {
	t.Helper()
	id := strconv.Itoa(number)
	for _, stage := range []string{"prepare", "await-input"} {
		if _, err := pipeline.Run(pipeline.Opts{Stage: stage, ID: id, RepoRoot: h.local}); err != nil {
			t.Fatalf("pipeline %s #%d: %v", stage, number, err)
		}
	}
	path := writeChainPlan(t, h.local, number, [][2]string{
		{"status", "awaiting-input"},
		{"escalationNonce", nonce},
		{"escalationCommentId", strconv.FormatInt(anchorID, 10)},
	})
	if _, err := pipeline.ApplyLabelTransition(pipeline.LabelOpts{ID: id, RepoRoot: h.local, RepoSlug: h.repo, Transition: "input-needed"}); err != nil {
		t.Fatalf("input-needed label transition #%d: %v", number, err)
	}
	if is := h.world().Issues[number]; !containsStr(is.Labels, "Input Needed") || containsStr(is.Labels, "Working") {
		t.Fatalf("#%d labels = %v, want +Input Needed -Working", number, is.Labels)
	}
	return path
}

// chainAuthorizedAnswer posts an authorized human answer on ticket number:
// an OWNER association plus a current repository write permission (#882).
func chainAuthorizedAnswer(h *chainHarness, number int) {
	h.seedPermission("octocat", "write")
	h.addIssueComment(number, "octocat", "User", "OWNER", "Go with approach A.")
}

// chainAssertSingleResume asserts the pass made exactly one run.Run, a resume
// of ticket number with its draft's `.plans/<file>` argument.
func chainAssertSingleResume(t *testing.T, captured []run.Opts, decisions []Decision, number int, draftPath string) {
	t.Helper()
	if d := decisionFor(t, decisions, number); d.Action != ActionDispatch || !d.Resume || d.Reason != "resume — human answered" {
		t.Fatalf("#%d decision = %+v, want a resume dispatch", number, d)
	}
	wantArg := filepath.Join(".plans", filepath.Base(draftPath))
	if len(captured) != 1 || captured[0].Ticket != wantArg {
		t.Fatalf("run.Run calls = %+v, want exactly one resume launch of %q", captured, wantArg)
	}
}

// chainReconcilePass runs one real RunReconcileOnce pass against a fresh
// state store, as a restarted daemon's first reconcile does.
func chainReconcilePass(t *testing.T, cfg Config) {
	t.Helper()
	var buf bytes.Buffer
	if _, err := RunReconcileOnce(cfg, &GHMutator{}, false, &buf, NewStateStore(filepath.Join(t.TempDir(), "reconcile.json"))); err != nil {
		t.Fatalf("RunReconcileOnce: %v\nlog:\n%s", err, buf.String())
	}
}

// chainAssertFallbackHold is the shared body of the three `## New Plan`
// fallback hold tests: the persisted held plan is skipped with `awaiting
// human plan review` and leaves everything untouched, and a simulated daemon
// restart (a new RunOnce, with a fresh mutator, over the same on-disk state)
// gives the identical result.
func chainAssertFallbackHold(t *testing.T, approval, body string) {
	t.Helper()
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	planPath := chainNewPlanFallback(t, h, 101, approval, body)
	planBefore := chainReadFile(t, planPath)
	cfg := chainConfig(h)

	first := chainHeldPass(t, h, cfg, 101, reasonAuthorizationHeld)
	restarted := chainHeldPass(t, h, cfg, 101, reasonAuthorizationHeld)
	if restarted != first {
		t.Fatalf("restarted pass decision line = %q, want the identical %q", restarted, first)
	}
	if after := chainReadFile(t, planPath); !bytes.Equal(after, planBefore) {
		t.Fatal("the held plan file must stay byte-identical across held passes")
	}
	if invs := chainCenciInvocations(t, h.cenciLogPath); len(invs) != 0 {
		t.Fatalf("cenci self-exec invocations = %v, want none for a held plan", invs)
	}
}

// TestAutonomousChain_SensitivePathFallbackHoldsPlanForHumanReview proves
// #1464's sensitive-path hold: an unattended run whose plan names a sensitive
// path is saved through the `## New Plan` fallback (`approval:
// pending-human`, a held record, +Planned -Working), and no pass -- before or
// after a daemon restart -- launches it, edits a label or moves its stage.
// Before #1464 this exact state dispatched `.plans/<file>` on the next pass.
func TestAutonomousChain_SensitivePathFallbackHoldsPlanForHumanReview(t *testing.T) {
	chainAssertFallbackHold(t, "pending-human", chainSensitivePathPlanBody)
}

// TestAutonomousChain_OpenQuestionsFallbackHoldsPlanForHumanReview is the
// Open Questions AC's own case: a plan that took the `## New Plan` fallback
// because of an unresolved Open Question gets the same hold.
func TestAutonomousChain_OpenQuestionsFallbackHoldsPlanForHumanReview(t *testing.T) {
	chainAssertFallbackHold(t, "pending-human", chainOpenQuestionsPlanBody)
}

// TestAutonomousChain_LeanDecidedFallbackHoldsPlanForHumanReview is the
// `lean-decided` fallback AC's own case: a held plan whose front matter reads
// `approval: lean-decided` (the value the self-approving Lean Approval Path
// also writes) is held all the same -- the hold lives in the state record,
// never in front matter.
func TestAutonomousChain_LeanDecidedFallbackHoldsPlanForHumanReview(t *testing.T) {
	chainAssertFallbackHold(t, "lean-decided", chainLeanDecidedPlanBody)
}

// TestAutonomousChain_CLIApprovalReleasesTheExactPlanThenLaunches proves the
// CLI-release AC: from an attended session `cenci dispatch approve` records a
// human (cli) authorization bound to the held plan's SHA-256, and the next
// RunOnce launches the ticket with that `.plans/<file>` argument.
func TestAutonomousChain_CLIApprovalReleasesTheExactPlanThenLaunches(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	planPath := chainNewPlanFallback(t, h, 101, "pending-human", chainOpenQuestionsPlanBody)
	cfg := chainConfig(h)
	chainHeldPass(t, h, cfg, 101, reasonAuthorizationHeld)

	wantSha := pipeline.PlanFingerprint(string(chainReadFile(t, planPath)))
	res := chainApproveAsHuman(t, h, 101)
	chainAssertRelease(t, h, 101, res, planPath, wantSha, string(pipeline.AuthorizationHeld))

	captured, decisions, _ := chainCapturePass(t, h, cfg, false)
	chainAssertSingleLaunch(t, captured, decisions, 101, planPath)
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Working") {
		t.Fatalf("labels = %v, want the launch's Working claim", is.Labels)
	}
}

// TestAutonomousChain_ChangedPlanAfterApprovalNeedsFreshApproval proves the
// changed-plan AC: one byte appended to an approved plan makes the next pass
// (and a restarted one) skip it with `plan changed since approval` and launch
// nothing; approving again binds the new bytes and releases it.
func TestAutonomousChain_ChangedPlanAfterApprovalNeedsFreshApproval(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	planPath := chainNewPlanFallback(t, h, 101, "pending-human", chainSensitivePathPlanBody)
	cfg := chainConfig(h)

	approvedSha := pipeline.PlanFingerprint(string(chainReadFile(t, planPath)))
	chainAssertRelease(t, h, 101, chainApproveAsHuman(t, h, 101), planPath, approvedSha, string(pipeline.AuthorizationHeld))

	f, err := os.OpenFile(planPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	changedSha := pipeline.PlanFingerprint(string(chainReadFile(t, planPath)))
	if changedSha == approvedSha {
		t.Fatal("appending a byte must change the plan's fingerprint")
	}

	first := chainHeldPass(t, h, cfg, 101, reasonAuthorizationChanged)
	if restarted := chainHeldPass(t, h, cfg, 101, reasonAuthorizationChanged); restarted != first {
		t.Fatalf("restarted pass decision line = %q, want the identical %q", restarted, first)
	}

	chainAssertRelease(t, h, 101, chainApproveAsHuman(t, h, 101), planPath, changedSha, string(pipeline.AuthorizationHuman))
	captured, decisions, _ := chainCapturePass(t, h, cfg, false)
	chainAssertSingleLaunch(t, captured, decisions, 101, planPath)
}

// TestAutonomousChain_EscalationHoldsSurviveWorkerDeathRestartAndReconcile
// proves #1464's P1: a planner escalated (Input Needed, an awaiting-input
// draft) and its worker died after a partial label mutation, leaving a stray
// Working. A restarted daemon's reconcile removes only the stray Working;
// both holds -- the Input Needed label and the awaiting-input draft -- remain,
// across another restart too. An unauthorized reply (no repository
// association; a member without write permission) resumes nothing; only an
// authorized human answer resumes, exactly once.
func TestAutonomousChain_EscalationHoldsSurviveWorkerDeathRestartAndReconcile(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	_, draftPath := chainEscalate(t, h, 101, chainEscalationNonce)
	draftBefore := chainReadFile(t, draftPath)

	// Worker death: the dying session left Working behind beside Input
	// Needed (#853 AC3's crash-after-partial-label-mutation signature).
	h.mutate(func(w *ghWorld) { w.Issues[101].Labels = append(w.Issues[101].Labels, "Working") })

	cfg := chainConfig(h)
	assertBothHolds := func(step string) {
		t.Helper()
		if is := h.world().Issues[101]; !containsStr(is.Labels, "Input Needed") || containsStr(is.Labels, "Working") {
			t.Fatalf("%s: labels = %v, want Input Needed kept and the stray Working gone", step, is.Labels)
		}
		if after := chainReadFile(t, draftPath); !bytes.Equal(after, draftBefore) {
			t.Fatalf("%s: the awaiting-input draft changed", step)
		}
		st, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: "101", RepoRoot: h.local})
		if err != nil {
			t.Fatalf("%s: reloading state: %v", step, err)
		}
		if st.Stage != pipeline.StageWaitingForInput {
			t.Fatalf("%s: stage = %q, want %q", step, st.Stage, pipeline.StageWaitingForInput)
		}
	}
	heldResume := func(step, wantReason string) {
		t.Helper()
		editsBefore := chainIssueEdits(h)
		captured, decisions, _ := chainCapturePass(t, h, cfg, false)
		if d := decisionFor(t, decisions, 101); d.Action != ActionSkip || d.Reason != wantReason {
			t.Fatalf("%s: decision = %+v, want a skip with %q", step, d, wantReason)
		}
		if len(captured) != 0 {
			t.Fatalf("%s: run.Run calls = %+v, want none", step, captured)
		}
		if n := chainIssueEdits(h); n != editsBefore {
			t.Fatalf("%s: gh issue edit calls went from %d to %d, want none", step, editsBefore, n)
		}
		assertBothHolds(step)
	}

	chainReconcilePass(t, cfg)
	assertBothHolds("after the restarted reconcile")
	heldResume("first pass after restart", reasonAnswerWaiting)

	h.addIssueComment(101, "drive-by", "User", "NONE", "Just resume it.")
	heldResume("reply with no repository association", reasonAnswerWaiting)

	h.addIssueComment(101, "member-reader", "User", "MEMBER", "Go with approach B.")
	heldResume("reply from a member without write permission", reasonAnswerUnauthorized)

	chainReconcilePass(t, cfg)
	heldResume("second restart", reasonAnswerUnauthorized)

	chainAuthorizedAnswer(h, 101)
	captured, decisions, _ := chainCapturePass(t, h, cfg, false)
	chainAssertSingleResume(t, captured, decisions, 101, draftPath)
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Working") || containsStr(is.Labels, "Input Needed") {
		t.Fatalf("labels after the resume claim = %v, want +Working -Input Needed", is.Labels)
	}
}

// TestAutonomousChain_AnswerPostedWhileDaemonStoppedResumesExactlyOnce proves
// #1464's P18: an authorized answer posted while the daemon is stopped is
// picked up by the first successful pass after restart -- not by a failed
// one -- and resumes exactly once; later passes never resume again.
func TestAutonomousChain_AnswerPostedWhileDaemonStoppedResumesExactlyOnce(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	_, draftPath := chainEscalate(t, h, 101, chainEscalationNonce)
	cfg := chainConfig(h)

	captured, decisions, _ := chainCapturePass(t, h, cfg, false)
	if d := decisionFor(t, decisions, 101); d.Reason != reasonAnswerWaiting || len(captured) != 0 {
		t.Fatalf("before the stop: decision = %+v, run.Run calls = %+v, want a waiting skip and no launch", d, captured)
	}

	// The daemon is stopped: the answer lands with no pass to observe it.
	chainAuthorizedAnswer(h, 101)

	// The restarted daemon's first pass fails at ticket collection (one
	// failing `gh issue list`), so it is not a successful pass and must not
	// resume.
	h.seedAmbiguity(ghAmbiguity{Route: "issue list", Kind: "ambiguous-success", OnCall: h.world().RouteCalls["issue list"] + 1})
	spawns := 0
	stubRunFn(t, func(run.Opts, run.Controller) error {
		spawns++
		return nil
	})
	editsBefore := chainIssueEdits(h)
	_, failedLog, _ := chainRunOnceExpectingError(t, cfg, &GHMutator{}, false)
	if !strings.Contains(failedLog, "dispatch: collecting tickets:") {
		t.Fatalf("failed pass log = %q, want the ticket-collection failure", failedLog)
	}
	if spawns != 0 || chainIssueEdits(h) != editsBefore {
		t.Fatalf("failed pass: run.Run calls = %d, label edits %d -> %d, want none", spawns, editsBefore, chainIssueEdits(h))
	}
	if is := h.world().Issues[101]; !containsStr(is.Labels, "Input Needed") {
		t.Fatalf("failed pass: labels = %v, want Input Needed kept", is.Labels)
	}

	captured, decisions, _ = chainCapturePass(t, h, cfg, false)
	chainAssertSingleResume(t, captured, decisions, 101, draftPath)

	for i := 1; i <= 2; i++ {
		captured, decisions, _ = chainCapturePass(t, h, cfg, false)
		if d := decisionFor(t, decisions, 101); d.Resume || len(captured) != 0 {
			t.Fatalf("pass %d after the resume: decision = %+v, run.Run calls = %+v, want no second resume", i, d, captured)
		}
	}
}

// TestAutonomousChain_EscalationAnswerStaysScopedToItsTicket proves #1464's
// escalation-scoping AC through the real resolveAnswerProbes/Reconcile/
// applyDispatch path: an authorized answer to ticket A's escalation resumes
// A alone. Ticket B (its own escalation, unanswered) waits; ticket B' (a
// draft whose anchor is A's comment and nonce, with an authorized reply on
// its own thread) is refused as an anchor mismatch; and ticket C (a held
// plan whose thread carries A's escalation marker and an authorized
// "approved") stays held, its state byte-identical.
func TestAutonomousChain_EscalationAnswerStaysScopedToItsTicket(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)

	anchorA, draftA := chainEscalate(t, h, 101, chainEscalationNonce)
	chainEscalate(t, h, 102, chainOtherEscalationNonce)
	h.seedIssue(103, "Draft anchored to A", "body", []string{"Refined", "Working"}, []string{"octocat"})
	chainEscalateWithAnchor(t, h, 103, chainEscalationNonce, anchorA)
	chainNewPlanFallback(t, h, 104, "pending-human", chainOpenQuestionsPlanBody)

	chainAuthorizedAnswer(h, 101)
	h.addIssueComment(103, "octocat", "User", "OWNER", "Go with approach A.")
	h.addIssueComment(104, "octocat", "User", "OWNER", "<!-- cenci-planner-escalation:"+chainEscalationNonce+" -->\nApproved, ship it.")

	stateC := chainReadFile(t, chainPipelineStatePath(h, 104))
	labelsBefore := map[int][]string{}
	for _, n := range []int{102, 103, 104} {
		labelsBefore[n] = append([]string{}, h.world().Issues[n].Labels...)
	}

	cfg := chainConfig(h)
	editsBefore := chainIssueEdits(h)
	chainReconcilePass(t, cfg)
	if n := chainIssueEdits(h); n != editsBefore {
		t.Fatalf("reconcile made %d label edits, want none for answered/held tickets", n-editsBefore)
	}

	captured, decisions, _ := chainCapturePass(t, h, cfg, false)
	chainAssertSingleResume(t, captured, decisions, 101, draftA)
	wantReasons := map[int]string{
		102: reasonAnswerWaiting,
		103: reasonAnswerAnchorMismatch,
		104: reasonAuthorizationHeld,
	}
	for n, want := range wantReasons {
		if d := decisionFor(t, decisions, n); d.Action != ActionSkip || d.Reason != want {
			t.Fatalf("#%d decision = %+v, want a skip with %q", n, d, want)
		}
		if labels := h.world().Issues[n].Labels; !slices.Equal(labels, labelsBefore[n]) {
			t.Fatalf("#%d labels = %v, want them unchanged at %v", n, labels, labelsBefore[n])
		}
	}
	if n := chainIssueEdits(h); n != editsBefore+1 {
		t.Fatalf("gh issue edit calls this pass = %d, want exactly 1 (A's resume claim)", n-editsBefore)
	}
	if after := chainReadFile(t, chainPipelineStatePath(h, 104)); !bytes.Equal(after, stateC) {
		t.Fatal("#104's held authorization must stay byte-identical after another ticket's escalation was answered")
	}
}

// TestAutonomousChain_DryRunOnceShowsResumeWithoutMutation proves #1464's
// dry-run resume edge case: under `--dry-run --once` an Input Needed ticket
// with an authorized answer is shown as a resume decision (noted `session not
// probed (dry-run)`), with zero label edits and zero spawns.
func TestAutonomousChain_DryRunOnceShowsResumeWithoutMutation(t *testing.T) {
	h := newChainHarness(t)
	h.commitAndPushConfig(chainRepoConfigLean)
	_, draftPath := chainEscalate(t, h, 101, chainEscalationNonce)
	chainAuthorizedAnswer(h, 101)
	stateBefore := chainReadFile(t, chainPipelineStatePath(h, 101))
	labelsBefore := append([]string{}, h.world().Issues[101].Labels...)
	editsBefore := chainIssueEdits(h)

	captured, decisions, log := chainCapturePass(t, h, chainConfig(h), true)
	d := decisionFor(t, decisions, 101)
	if d.Action != ActionDispatch || !d.Resume || d.Reason != "resume — human answered" || d.Note != noteSessionNotProbed {
		t.Fatalf("decision = %+v, want a resume dispatch noted %q", d, noteSessionNotProbed)
	}
	line := fmt.Sprintf("%s#101 dispatch (claude, %s, session %q): resume — human answered; %s", h.repo, filepath.Base(draftPath), "chain-session", noteSessionNotProbed)
	if !chainLogHasLine(log, line) {
		t.Fatalf("decision table missing the line %q:\n%s", line, log)
	}
	if len(captured) != 0 {
		t.Fatalf("run.Run calls = %+v, want none in a dry run", captured)
	}
	if n := chainIssueEdits(h); n != editsBefore {
		t.Fatalf("gh issue edit calls went from %d to %d, want none in a dry run", editsBefore, n)
	}
	if labels := h.world().Issues[101].Labels; !slices.Equal(labels, labelsBefore) {
		t.Fatalf("labels = %v, want them unchanged at %v", labels, labelsBefore)
	}
	if after := chainReadFile(t, chainPipelineStatePath(h, 101)); !bytes.Equal(after, stateBefore) {
		t.Fatal("a dry run must leave the pipeline state byte-identical")
	}
}
