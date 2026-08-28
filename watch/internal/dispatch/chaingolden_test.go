package dispatch

// Ticket #912: the minimal, self-contained watch-side half of the cross-layer
// adversarial-chain golden fixture contract (per Q1: no ghWorld extension --
// graph assertions needing new ghWorld modelling, native sub-issues and
// milestone round-tripping, are #914's).
//
// This file reads flow/tests/adversarial-chain/fixtures/post-refinement-graph.json
// cross-module (runtime.Caller(0) climbing out of the watch/ Go module,
// mirroring internal/errcode/atlas_sync_test.go's precedent -- go:embed
// cannot cross the module root upward), fails loudly (t.Fatalf, never
// t.Skip) on a missing/unreadable/malformed fixture, and asserts the
// fixture's own internal schema/invariants independently of the flow-side
// driver.
//
// Never call t.Parallel() in any test in this file that grows to use the
// chain harness (newChainHarness/installFakeCenciProcess): os.Args[0]
// mutation is process-global, and babysit.localRepoRoot's bare `git
// rev-parse --show-toplevel` requires t.Chdir.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
)

// goldenAssignee mirrors the fixture's `assignees[{login}]` shape.
type goldenAssignee struct {
	Login string `json:"login"`
}

// goldenSubIssue mirrors the fixture's `parent.subIssues[{number,state}]`
// shape -- the native sub-issue links, asserted here only as a fixture
// invariant (number-set equality with children). Round-tripping this
// through ghWorld (modeled on ghIssue.SubIssues, never a new --json
// subIssues route/consumer, per Q3) is asserted by
// TestAutonomousChain_PositiveEndToEnd's assertGoldenFidelityUnchanged
// (chain_e2e_test.go), which pins parent.SubIssues byte-identical after
// every real `gh issue edit` mutation across the full positive scenario.
type goldenSubIssue struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

// goldenPlan mirrors the fixture's per-child `plan{}` front-matter object.
type goldenPlan struct {
	TicketID    int    `json:"ticketId"`
	Slug        string `json:"slug"`
	Status      string `json:"status"`
	IsChild     bool   `json:"isChild"`
	IsLastChild bool   `json:"isLastChild"`
	ParentID    int    `json:"parentId"`
}

// goldenParent mirrors the fixture's `parent{}` object. Milestone is kept as
// raw JSON: its sub-schema isn't otherwise consumed by any production type
// in this package, only checked here for presence. Round-tripping it through
// ghWorld (modeled on ghIssue.Milestone, never a new gh route/consumer, per
// Q3) is asserted by TestAutonomousChain_PositiveEndToEnd's
// assertGoldenFidelityUnchanged (chain_e2e_test.go).
type goldenParent struct {
	Number    int              `json:"number"`
	Title     string           `json:"title"`
	State     string           `json:"state"`
	Body      string           `json:"body"`
	Labels    []string         `json:"labels"`
	Assignees []goldenAssignee `json:"assignees"`
	Milestone json.RawMessage  `json:"milestone"`
	SubIssues []goldenSubIssue `json:"subIssues"`
}

// goldenChild mirrors one `children[]` entry.
type goldenChild struct {
	Slot      string           `json:"slot"`
	Number    int              `json:"number"`
	Title     string           `json:"title"`
	State     string           `json:"state"`
	Body      string           `json:"body"`
	Labels    []string         `json:"labels"`
	Assignees []goldenAssignee `json:"assignees"`
	Milestone json.RawMessage  `json:"milestone"`
	DependsOn []int            `json:"blockedBy"`
	Plan      goldenPlan       `json:"plan"`
}

// goldenGraph mirrors the fixture's top-level schema (v1), per the plan's
// Files to Create entry for flow/tests/adversarial-chain/fixtures/post-refinement-graph.json.
type goldenGraph struct {
	SchemaVersion int           `json:"schemaVersion"`
	Repo          string        `json:"repo"`
	Login         string        `json:"login"`
	Parent        goldenParent  `json:"parent"`
	Children      []goldenChild `json:"children"`
	WriteOrder    []string      `json:"writeOrder"`
}

// goldenGraphPath resolves the shared cross-layer golden fixture via
// runtime.Caller(0), climbing out of the watch/ Go module (the fixture lives
// under flow/, outside this module -- go:embed cannot cross the module root
// upward). This resolves at compile time from this source file's own
// absolute path, so it is independent of the test binary's working
// directory.
func goldenGraphPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed to resolve this test file's own path")
	}
	// file: <repo-root>/watch/internal/dispatch/chaingolden_test.go
	// climb: dispatch -> internal -> watch -> <repo-root>
	return filepath.Join(filepath.Dir(file), "..", "..", "..",
		"flow", "tests", "adversarial-chain", "fixtures", "post-refinement-graph.json")
}

// readGoldenGraph reads and decodes the golden fixture, failing loudly
// (t.Fatalf, never t.Skip) on a missing, unreadable, or malformed file -- a
// moved/renamed/corrupted fixture must break CI, not silently pass with zero
// coverage.
func readGoldenGraph(t *testing.T) goldenGraph {
	t.Helper()
	path := goldenGraphPath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read golden post-refinement graph fixture at %s: %v (a missing/renamed/unreadable fixture must fail loudly, not be silently skipped)", path, err)
	}
	var graph goldenGraph
	if err := json.Unmarshal(data, &graph); err != nil {
		t.Fatalf("failed to decode golden post-refinement graph fixture at %s: %v (a malformed fixture must fail loudly, not be silently skipped)", path, err)
	}
	return graph
}

// isNonEmptyJSONObject reports whether raw decodes to a JSON object with at
// least one key -- used to assert milestone{} is a real, populated object in
// the fixture rather than null/{}/absent.
func isNonEmptyJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	return len(m) > 0
}

// TestGoldenGraph_SchemaInvariants asserts the golden fixture's own internal
// schema and cross-field invariants, independently of any round-trip through
// production code (that's TestGoldenGraph_RoundTripsThroughCollectAndDecide /
// TestGoldenGraph_PlanFrontMatterRoundTripsThroughCheckPlan, added in a later
// step of #912's Implementation Order).
func TestGoldenGraph_SchemaInvariants(t *testing.T) {
	graph := readGoldenGraph(t)

	if graph.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %d, want 1", graph.SchemaVersion)
	}
	if graph.Repo == "" {
		t.Error("repo is empty")
	}
	if graph.Login == "" {
		t.Error("login is empty")
	}

	if len(graph.Children) == 0 {
		t.Fatal("children is empty; the fixture would make every downstream per-child assertion vacuously pass (guard against a silently-vacuous fixture)")
	}

	childNumbers := make(map[int]bool, len(graph.Children))
	for _, c := range graph.Children {
		childNumbers[c.Number] = true
	}

	lastChildCount := 0
	for _, c := range graph.Children {
		if c.Plan.ParentID != graph.Parent.Number {
			t.Errorf("child #%d: plan.parentId = %d, want parent.number %d", c.Number, c.Plan.ParentID, graph.Parent.Number)
		}
		if c.Plan.TicketID != c.Number {
			t.Errorf("child #%d: plan.ticketId = %d, want child.number %d", c.Number, c.Plan.TicketID, c.Number)
		}
		if c.Plan.IsLastChild {
			lastChildCount++
		}
		for _, dep := range c.DependsOn {
			if !childNumbers[dep] {
				t.Errorf("child #%d: blockedBy entry #%d does not resolve to any child in the fixture", c.Number, dep)
			}
		}
	}
	if lastChildCount != 1 {
		t.Errorf("exactly one child must have plan.isLastChild == true, got %d", lastChildCount)
	}

	subIssueNumbers := make(map[int]bool, len(graph.Parent.SubIssues))
	for _, si := range graph.Parent.SubIssues {
		subIssueNumbers[si.Number] = true
	}
	if len(subIssueNumbers) != len(childNumbers) {
		t.Errorf("parent.subIssues number-set (%d entries) does not match children number-set (%d entries)", len(subIssueNumbers), len(childNumbers))
	} else {
		for n := range childNumbers {
			if !subIssueNumbers[n] {
				t.Errorf("parent.subIssues is missing child #%d", n)
			}
		}
	}

	// Milestone and native sub-issue links are asserted here only as fixture
	// invariants (presence/non-emptiness); round-tripping either through
	// ghWorld (ghIssue.Milestone / ghIssue.SubIssues, preservation only, per
	// Q3 -- never a new gh route/consumer) is
	// TestAutonomousChain_PositiveEndToEnd's assertGoldenFidelityUnchanged
	// (chain_e2e_test.go), which pins both byte-identical after every real
	// `gh issue edit` across the full positive scenario.
	if !isNonEmptyJSONObject(graph.Parent.Milestone) {
		t.Error("parent.milestone is missing or empty")
	}
	if len(graph.Parent.SubIssues) == 0 {
		t.Error("parent.subIssues is empty")
	}
}

// loginsOf projects a slice of goldenAssignee into bare login strings, the
// shape h.seedIssue / Ticket.Assignees both expect.
func loginsOf(assignees []goldenAssignee) []string {
	out := make([]string, len(assignees))
	for i, a := range assignees {
		out[i] = a.Login
	}
	return out
}

// TestGoldenGraph_RoundTripsThroughCollectAndDecide seeds the fixture's own
// child issues into the chain harness's fake-GitHub world, then drives one
// real dry-run dispatch.RunOnce pass (CollectTickets + Decide) against them,
// asserting the collected Ticket fields round-trip the fixture's own data
// and that the fixture's dependency-free/dependent shape actually produces
// the two decisions #914 needs to build on:
//   - the first (dependency-free) child decides the lean planning-pickup
//     verdict ("plan — Refined, no plan file", TestAutonomousChain_
//     PositiveEndToEnd's phase A precedent) -- which requires this test to
//     NOT write a .plans/<n>-*.md file for it (a matched plan would make
//     dispatch.Decide's `planning` gate false and fall through to "not
//     Planned" instead, per decide.go's `plan == nil` requirement).
//   - the last (dependent) child decides "waiting on dependency #<first>",
//     gated on the fixture's own native blockedBy link.
//
// Plan-front-matter round-tripping through pipeline.CheckPlan is a
// deliberately separate concern, covered by
// TestGoldenGraph_PlanFrontMatterRoundTripsThroughCheckPlan below -- writing
// a plan file here would defeat this test's own "no plan file" pickup
// assertion.
func TestGoldenGraph_RoundTripsThroughCollectAndDecide(t *testing.T) {
	graph := readGoldenGraph(t)
	if len(graph.Children) < 2 {
		t.Fatalf("golden fixture must carry at least 2 children (a dependency-free slot and a dependent slot) for this round-trip, got %d", len(graph.Children))
	}

	h := newChainHarness(t)
	mut := &GHMutator{}

	for _, c := range graph.Children {
		h.seedIssue(c.Number, c.Title, c.Body, c.Labels, loginsOf(c.Assignees))
		// The fixture declares dependencies as GitHub's native blocked-by
		// links (what /cenci:refine writes), not as a body line, so they must
		// be seeded into the world explicitly -- seedIssue only carries the
		// fields that live in the issue body/labels/assignees.
		h.mutate(func(w *ghWorld) {
			w.Issues[c.Number].BlockedBy = append([]int{}, c.DependsOn...)
		})
	}
	h.commitAndPushConfig(chainRepoConfigLean)

	cfg := chainConfig(h)
	decisions, _ := chainRunOnce(t, cfg, mut, true /* dry-run: this test only observes collection/decision, never a real dispatch */)

	firstChild := graph.Children[0]
	lastChild := graph.Children[len(graph.Children)-1]

	// Seed-fidelity round-trip: the fake-GitHub world's stored issue data
	// (what CollectTickets itself reads back through the fake `gh`) must
	// equal the fixture's own content verbatim, proving the harness replays
	// the fixture rather than a paraphrase of it.
	for _, c := range graph.Children {
		is := h.world().Issues[c.Number]
		if is == nil {
			t.Fatalf("world missing seeded issue #%d", c.Number)
		}
		if is.Body != c.Body {
			t.Errorf("#%d: world body = %q, want fixture body %q", c.Number, is.Body, c.Body)
		}
		if !slices.Equal(is.Labels, c.Labels) {
			t.Errorf("#%d: world labels = %v, want fixture labels %v", c.Number, is.Labels, c.Labels)
		}
		if !slices.Equal(is.BlockedBy, c.DependsOn) {
			t.Errorf("#%d: world blockedBy = %v, want fixture blockedBy %v", c.Number, is.BlockedBy, c.DependsOn)
		}
	}

	firstDecision := decisionFor(t, decisions, firstChild.Number)
	if !slices.Equal(firstDecision.Ticket.Labels, firstChild.Labels) {
		t.Errorf("#%d: collected Ticket.Labels = %v, want fixture labels %v", firstChild.Number, firstDecision.Ticket.Labels, firstChild.Labels)
	}
	if wantAssignees := loginsOf(firstChild.Assignees); !slices.Equal(firstDecision.Ticket.Assignees, wantAssignees) {
		t.Errorf("#%d: collected Ticket.Assignees = %v, want fixture assignees %v", firstChild.Number, firstDecision.Ticket.Assignees, wantAssignees)
	}
	if firstDecision.Reason != "plan — Refined, no plan file" {
		t.Errorf("#%d decision = %q, want the lean planning-pickup verdict (the fixture's dependency-free manifest slot)", firstChild.Number, firstDecision.Reason)
	}

	lastDecision := decisionFor(t, decisions, lastChild.Number)
	if !slices.Equal(lastDecision.Ticket.DependsOn, lastChild.DependsOn) {
		t.Errorf("#%d: collected Ticket.DependsOn = %v, want fixture blockedBy %v", lastChild.Number, lastDecision.Ticket.DependsOn, lastChild.DependsOn)
	}
	wantReason := fmt.Sprintf("waiting on dependency #%d", firstChild.Number)
	if lastDecision.Reason != wantReason {
		t.Errorf("#%d decision = %q, want %q (gated on the fixture's own native blockedBy link)", lastChild.Number, lastDecision.Reason, wantReason)
	}
}

// TestGoldenGraph_PlanFrontMatterRoundTripsThroughCheckPlan writes a real
// .plans/<n>-*.md file from the fixture's last child's `plan{}` front
// matter, then runs it through the real pipeline.CheckPlan, asserting the
// returned PlanMeta's identity/split fields equal the fixture's own values.
// ReplanRequested: true short-circuits CheckPlan past planIsStale's gh/git
// freshness probe (mirrors planinventory_parity_test.go's fixture 4), so
// this round-trip needs no git repo or gh fixture at all -- only the plan
// file's own front matter is under test here.
func TestGoldenGraph_PlanFrontMatterRoundTripsThroughCheckPlan(t *testing.T) {
	graph := readGoldenGraph(t)
	if len(graph.Children) == 0 {
		t.Fatal("golden fixture must carry at least one child (non-vacuity guard)")
	}
	lastChild := graph.Children[len(graph.Children)-1]
	if !lastChild.Plan.IsLastChild {
		t.Fatalf("fixture's last child slot (#%d) must carry plan.isLastChild == true", lastChild.Number)
	}

	repoRoot := t.TempDir()
	writeChainPlan(t, repoRoot, lastChild.Number, [][2]string{
		{"status", lastChild.Plan.Status},
		{"isChild", strconv.FormatBool(lastChild.Plan.IsChild)},
		{"isLastChild", strconv.FormatBool(lastChild.Plan.IsLastChild)},
		{"parentId", strconv.Itoa(lastChild.Plan.ParentID)},
	})

	_, check, err := pipeline.CheckPlan(pipeline.PlanCheckOpts{
		ID: strconv.Itoa(lastChild.Number), RepoRoot: repoRoot, RepoSlug: graph.Repo, ReplanRequested: true,
	})
	if err != nil {
		t.Fatalf("CheckPlan(%d): unexpected error: %v", lastChild.Number, err)
	}
	if check.Decision != "replan" {
		t.Errorf("CheckPlan(%d).Decision = %q, want %q", lastChild.Number, check.Decision, "replan")
	}
	if check.Plan == nil {
		t.Fatalf("CheckPlan(%d).Plan is nil, want a parsed PlanMeta", lastChild.Number)
	}
	if check.Plan.TicketID != lastChild.Plan.TicketID {
		t.Errorf("PlanMeta.TicketID = %d, want fixture %d", check.Plan.TicketID, lastChild.Plan.TicketID)
	}
	if check.Plan.IsChild != lastChild.Plan.IsChild {
		t.Errorf("PlanMeta.IsChild = %v, want fixture %v", check.Plan.IsChild, lastChild.Plan.IsChild)
	}
	if check.Plan.IsLastChild != lastChild.Plan.IsLastChild {
		t.Errorf("PlanMeta.IsLastChild = %v, want fixture %v", check.Plan.IsLastChild, lastChild.Plan.IsLastChild)
	}
	if check.Plan.ParentID != lastChild.Plan.ParentID {
		t.Errorf("PlanMeta.ParentID = %d, want fixture %d", check.Plan.ParentID, lastChild.Plan.ParentID)
	}
}
