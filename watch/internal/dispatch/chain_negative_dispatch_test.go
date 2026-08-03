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

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/run"
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
