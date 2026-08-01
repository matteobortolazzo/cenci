package dispatch

// planinventory_parity_test.go: AC5 cross-package parity tests (#884) -- one
// temp-repo fixture per plan-inventory failure class, asserting that
// pipeline.CheckPlan (the manual `cenci pipeline plan-check` decision) and
// dispatch.ReadPlans + Decide (autonomous dispatch) reach the same
// selection or the same failure class against the identical on-disk
// .plans/ fixture. Fixtures are chosen to short-circuit before any gh/git
// call: discovery/identity failures already fail pipeline.CheckPlan before
// any freshness check (existing convention, planfile_test.go's
// TestCheckPlan_Discovery_*), and the one healthy-single-match fixture below
// passes ReplanRequested: true to skip planIsStale's `gh issue view`
// round-trip entirely.
//
// This file exercises #884's full cross-package rewire directly:
// dispatch.ReadPlans returning PlanScan, the new PlanProbe values
// (PlanProbeAmbiguous, PlanProbeIDMismatch), the new PlanInventory closed
// set (PlanInventoryVerified, PlanInventoryUnreadable), Inputs.PlanInventories,
// the new decide.go reason constants (reasonPlanProbeIdentityMismatch,
// reasonPlanProbeAmbiguous, reasonPlanInventoryUnreadable), and pipeline's
// new sentinels (ErrPlanInventoryUnreadable, ErrPlanIdentityMismatch) --
// none of which exist yet (Implementation Order steps 2-6 of the #884
// plan). This whole file is therefore expected to fail to compile until
// those steps land; see the Phase 3 delegation's red-phase report.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/pipeline"
)

// validParityPlanBody supplies the four sections plan-check requires
// (mirrors internal/pipeline/planfile_test.go's validPlanBody), so a
// pipeline.CheckPlan call against a fixture written here never fails
// ErrPlanMalformed's section scan for reasons unrelated to the failure
// class under test.
const validParityPlanBody = "## Ticket Details\nx\n\n## Implementation Plan\nx\n\n## Architectural Context\nx\n\n## Design Context\nx\n"

// writeParityPlan writes repoRoot/.plans/name with content, creating .plans
// if needed, and returns the file's full path.
func writeParityPlan(t *testing.T, repoRoot, name, content string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, ".plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// -- fixture 1: filename/front-matter identity mismatch (AC2, Q2) ----------

// TestParity_IdentityMismatch_BothSidesHoldSameFailureClass covers AC2/AC5:
// a plan file named for ticket #900 but whose front matter claims #884 must
// hold BOTH keys, on both consumers, via the same failure class (Q2: "hold
// both #900 and #884 -- both the filename claim and the front-matter claim
// are held with the same 'plan file ticket identity mismatch' reason").
func TestParity_IdentityMismatch_BothSidesHoldSameFailureClass(t *testing.T) {
	repoRoot := t.TempDir()
	writeParityPlan(t, repoRoot, "900-foo.md", "---\nticketId: 884\nstatus: planned\n---\n"+validParityPlanBody)

	// -- dispatch side --
	scan, err := ReadPlans("o/r", repoRoot, nil, io.Discard)
	if err != nil {
		t.Fatalf("ReadPlans: unexpected top-level error: %v", err)
	}
	if len(scan.Plans) != 0 {
		t.Errorf("Plans = %+v, want none (a mismatched claim must never be added to plans)", scan.Plans)
	}
	for _, id := range []int{900, 884} {
		key := planKey("o/r", id)
		if got := scan.Probes[key]; got != PlanProbeIDMismatch {
			t.Errorf("Probes[%q] = %q, want PlanProbeIDMismatch", key, got)
		}
	}

	in := Inputs{
		Config:          testConfig(),
		PlanProbes:      scan.Probes,
		PlanInventories: map[string]PlanInventory{"o/r": scan.Inventory},
	}
	in.Tickets = []Ticket{
		{Repo: "o/r", Number: 900, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		{Repo: "o/r", Number: 884, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
	}
	assertDecisions(t, Decide(in), []wantDecision{
		{884, ActionSkip, reasonPlanProbeIdentityMismatch, ""},
		{900, ActionSkip, reasonPlanProbeIdentityMismatch, ""},
	})

	// -- pipeline side --
	for _, id := range []string{"900", "884"} {
		_, check, err := pipeline.CheckPlan(pipeline.PlanCheckOpts{ID: id, RepoRoot: repoRoot, RepoSlug: "o/r"})
		if !errors.Is(err, pipeline.ErrPlanIdentityMismatch) {
			t.Errorf("CheckPlan(%s) error = %v, want errors.Is(_, ErrPlanIdentityMismatch)", id, err)
		}
		if check.Decision != "" {
			t.Errorf(`CheckPlan(%s).Decision = %q, want "" (Q3: routed through the closed decision set)`, id, check.Decision)
		}
	}
}

// -- fixture 2: ambiguous duplicate claims (AC3/AC4) ------------------------

// TestParity_AmbiguousDuplicateClaims_BothSidesHoldSameFailureClass covers
// AC3: two healthy plan files claiming the same ticket must hold, on both
// consumers, rather than either silently picking one (dispatch's former
// first-wins bug) or the two disagreeing about which is "the" plan.
func TestParity_AmbiguousDuplicateClaims_BothSidesHoldSameFailureClass(t *testing.T) {
	repoRoot := t.TempDir()
	pathA := writeParityPlan(t, repoRoot, "42-a.md", "---\nticketId: 42\nstatus: planned\n---\n"+validParityPlanBody)
	pathB := writeParityPlan(t, repoRoot, "42-b.md", "---\nticketId: 42\nstatus: planned\n---\n"+validParityPlanBody)

	// -- dispatch side --
	scan, err := ReadPlans("o/r", repoRoot, nil, io.Discard)
	if err != nil {
		t.Fatalf("ReadPlans: unexpected top-level error: %v", err)
	}
	if len(scan.Plans) != 0 {
		t.Errorf("Plans = %+v, want none (an ambiguous claim must never be added to plans)", scan.Plans)
	}
	key := planKey("o/r", 42)
	if got := scan.Probes[key]; got != PlanProbeAmbiguous {
		t.Errorf("Probes[%q] = %q, want PlanProbeAmbiguous", key, got)
	}

	in := Inputs{
		Config:          testConfig(),
		PlanProbes:      scan.Probes,
		PlanInventories: map[string]PlanInventory{"o/r": scan.Inventory},
	}
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}}}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, reasonPlanProbeAmbiguous, ""}})

	// -- pipeline side --
	_, check, err := pipeline.CheckPlan(pipeline.PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if !errors.Is(err, pipeline.ErrMultiplePlans) {
		t.Errorf("CheckPlan(42) error = %v, want errors.Is(_, ErrMultiplePlans)", err)
	}
	if check.Decision != "multiple" {
		t.Errorf("CheckPlan(42).Decision = %q, want multiple", check.Decision)
	}
	if len(check.Paths) != 2 {
		t.Errorf("CheckPlan(42).Paths = %v, want both %s and %s", check.Paths, pathA, pathB)
	}
}

// -- fixture 3: unreadable directory, repo-wide hold (Q5) -------------------

// TestParity_UnreadableDirectory_RepoWideHold_OrdinaryResumePlanningAlike
// covers Q5: an unreadable `.plans` holds EVERY ticket in the repo --
// ordinary Planned dispatch, resume (Input Needed), and planning pickup
// (Refined) alike -- on both consumers, since a directory read failure
// can never be attributed to one ticket key.
func TestParity_UnreadableDirectory_RepoWideHold_OrdinaryResumePlanningAlike(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, ".plans"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// -- dispatch side --
	scan, err := ReadPlans("o/r", repoRoot, nil, io.Discard)
	if err == nil {
		t.Fatal("ReadPlans: want a non-nil error for an unreadable .plans directory")
	}
	if scan.Inventory != PlanInventoryUnreadable {
		t.Errorf("Inventory = %q, want PlanInventoryUnreadable", scan.Inventory)
	}
	if len(scan.Plans) != 0 {
		t.Errorf("Plans = %+v, want none", scan.Plans)
	}

	in := Inputs{
		Config:          testConfig(),
		PlanInventories: map[string]PlanInventory{"o/r": scan.Inventory},
	}
	in.Config.PlanRefined = true
	in.Tickets = []Ticket{
		{Repo: "o/r", Number: 1, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		{Repo: "o/r", Number: 2, Labels: []string{"Input Needed"}, Assignees: []string{"octocat"}},
		{Repo: "o/r", Number: 3, Labels: []string{"Refined"}, Assignees: []string{"octocat"}},
	}
	assertDecisions(t, Decide(in), []wantDecision{
		{1, ActionSkip, reasonPlanInventoryUnreadable, ""},
		{2, ActionSkip, reasonPlanInventoryUnreadable, ""},
		{3, ActionSkip, reasonPlanInventoryUnreadable, ""},
	})

	// -- pipeline side --
	_, check, err := pipeline.CheckPlan(pipeline.PlanCheckOpts{ID: "1", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if !errors.Is(err, pipeline.ErrPlanInventoryUnreadable) {
		t.Errorf("CheckPlan(1) error = %v, want errors.Is(_, ErrPlanInventoryUnreadable)", err)
	}
	if check.Decision != "" {
		t.Errorf(`CheckPlan(1).Decision = %q, want ""`, check.Decision)
	}
}

// -- fixture 4: unattributable file holds nothing (Q1 regression guard) ----

// TestParity_UnattributableFile_HoldsNothing_SiblingTicketUnaffectedBothSides
// covers Q1's regression guard: a legitimate ticketless plan file must never
// gate a real sibling ticket in the same repo, on either consumer.
func TestParity_UnattributableFile_HoldsNothing_SiblingTicketUnaffectedBothSides(t *testing.T) {
	repoRoot := t.TempDir()
	writeParityPlan(t, repoRoot, "some-notes.md", "just some notes, no front matter\n")
	siblingPath := writeParityPlan(t, repoRoot, "42-sibling.md", "---\nticketId: 42\nstatus: planned\nslug: sibling\n---\n"+validParityPlanBody)

	// -- dispatch side --
	scan, err := ReadPlans("o/r", repoRoot, nil, io.Discard)
	if err != nil {
		t.Fatalf("ReadPlans: unexpected top-level error: %v", err)
	}
	if scan.Inventory != PlanInventoryVerified {
		t.Errorf("Inventory = %q, want PlanInventoryVerified (an unattributable file must never taint the whole repo's inventory)", scan.Inventory)
	}
	if len(scan.Plans) != 1 || scan.Plans[0].TicketID != 42 {
		t.Fatalf("Plans = %+v, want exactly the sibling's healthy plan for ticket 42", scan.Plans)
	}

	in := Inputs{
		Config:          testConfig(),
		Plans:           scan.Plans,
		PlanProbes:      scan.Probes,
		PlanInventories: map[string]PlanInventory{"o/r": scan.Inventory},
		Snapshot:        snapshot(0, 0),
		Budgets:         FloorProvider{},
		CurrentUser:     "octocat",
	}
	in.Tickets = []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}}}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})

	// -- pipeline side -- ReplanRequested: true short-circuits straight past
	// planIsStale, so this fixture needs no gh/git mocking.
	_, check, err := pipeline.CheckPlan(pipeline.PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r", ReplanRequested: true})
	if err != nil {
		t.Fatalf("CheckPlan(42): unexpected error: %v", err)
	}
	if check.Decision != "replan" {
		t.Errorf("CheckPlan(42).Decision = %q, want replan", check.Decision)
	}
	if len(check.Paths) != 1 || check.Paths[0] != siblingPath {
		t.Errorf("CheckPlan(42).Paths = %v, want exactly [%s] (the unattributable file must never appear)", check.Paths, siblingPath)
	}
}
