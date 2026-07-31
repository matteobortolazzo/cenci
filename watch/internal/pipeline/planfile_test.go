package pipeline

// Tests for CheckPlan (ticket #560): the read-only `cenci pipeline plan-check
// <id>` decision function. Package-boundary ("white box") tests, matching
// labels_test.go/worktree_test.go's own convention: gh responses are
// scripted through the shared `command` seam (retry.go); git operations
// (planfile.CommitsBehind's commits-behind counting) run for real against a
// temp git repo, per worktree_test.go's own "no fakes for git" convention --
// planfile.CommitsBehind shells out via exec.Command directly, not through
// this package's `command` seam, so it cannot be mocked here even if we
// wanted to.
//
// CheckPlan's design (this test file is the source of truth for
// planfile.go's shape, not yet written):
//
//	func CheckPlan(o PlanCheckOpts) (State, PlanCheck, error)
//
//	type PlanCheckOpts struct {
//	    ID              string
//	    ReplanRequested bool
//	    RepoRoot        string
//	    StateDir        string
//	    RepoSlug        string
//	}
//
//	type PlanCheck struct {
//	    Decision string    // "resume" | "stale" | "replan" | "awaiting-input" | "none" | "multiple"
//	    Paths    []string  // matched .plans/<id>-*.md path(s)
//	    Plan     *PlanMeta // populated whenever exactly one plan file parsed cleanly
//	}
//
//	type PlanMeta struct {
//	    Mode        string
//	    Slug        string
//	    TicketID    int
//	    IsChild     bool
//	    IsLastChild bool
//	    ParentID    int
//	}
//
// Error/decision pairing (the three declared sentinels, rule #412/#446):
//   - zero .plans/<id>-*.md matches: decision "none", non-nil error wrapping
//     ErrPlanNotFound.
//   - two or more matches: decision "multiple", Paths holds every match,
//     non-nil error wrapping ErrMultiplePlans.
//   - exactly one match that fails front-matter/section/slug validation:
//     decision left unset (""), non-nil error wrapping ErrPlanMalformed, with
//     a distinct message marker per failure class (front matter vs. missing
//     section vs. invalid slug).
//   - exactly one match that validates cleanly: decision is one of
//     "replan"/"stale"/"resume" per the freshness/replan rules below, with a
//     nil error -- these are legitimate classification outcomes, not domain
//     failures.
//
// Freshness rule (deterministic, no judgment fallback): stale when ANY of
// commits-behind (git, scoped to the plan's stalenessPaths) > 0, ticket
// state != OPEN, or ticket updatedAt > plan's createdAt. ReplanRequested
// short-circuits straight to "replan" without ever computing freshness (no
// git or gh calls). A front matter status of "awaiting-input" (#826)
// short-circuits straight to "awaiting-input" the same way -- checked after
// ReplanRequested (so --replan-requested remains the deliberate discard
// escape hatch even for a draft) but before freshness.
//
// CheckPlan never gates on or mutates the persisted pipeline Stage -- it
// only echoes whatever is on disk (StageNew when no state file exists yet),
// exactly like GetArtifacts.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// -- fixtures ---------------------------------------------------------------

// validPlanBody supplies all four sections plan-check requires per the
// plan's Assumptions ("## Ticket Details", "## Implementation Plan",
// "## Architectural Context", "## Design Context").
const validPlanBody = `
## Ticket Details
some ticket details

## Implementation Plan
do the thing

## Architectural Context
some architectural context

## Design Context
some design context
`

// defaultPlanFields builds a realistic v1 front-matter field set for id/slug,
// anchored at sha with the given createdAt (RFC3339) timestamp.
func defaultPlanFields(id, slug, sha, createdAt string) map[string]string {
	return map[string]string{
		"version":        "1",
		"mode":           "ticket",
		"ticketId":       id,
		"ticketTitle":    "Some ticket",
		"slug":           slug,
		"isChild":        "false",
		"isLastChild":    "false",
		"parentId":       "null",
		"createdAt":      createdAt,
		"status":         "planned",
		"planCommitSha":  sha,
		"stalenessPaths": "watch, flow",
	}
}

// planFrontMatterOrder keeps front-matter emission deterministic across the
// map iteration, matching real .plans/*.md files' key ordering.
//
// escalationNonce/escalationCommentId (#849) are appended at the end: they
// are only ever present on an awaiting-input draft, and defaultPlanFields
// below never sets them, so their presence here is additive/backward
// compatible with every existing fixture.
var planFrontMatterOrder = []string{
	"version", "mode", "ticketId", "ticketTitle", "slug", "isChild",
	"isLastChild", "parentId", "createdAt", "status", "planCommitSha",
	"stalenessPaths", "escalationNonce", "escalationCommentId",
}

func planFrontMatter(fields map[string]string) string {
	var b strings.Builder
	for _, k := range planFrontMatterOrder {
		if v, ok := fields[k]; ok {
			b.WriteString(k + ": " + v + "\n")
		}
	}
	return b.String()
}

// writePlanFile writes .plans/<id>-<slug>.md with the given front-matter
// fields and body, returning the file's path.
func writePlanFile(t *testing.T, repoRoot, id, slug string, fields map[string]string, body string) string {
	t.Helper()
	return writeRawPlanFile(t, repoRoot, id, slug, "---\n"+planFrontMatter(fields)+"---\n"+body)
}

// writeRawPlanFile writes .plans/<id>-<slug>.md with content verbatim, for
// tests exercising malformed/missing front matter that planFrontMatter can't
// express.
func writeRawPlanFile(t *testing.T, repoRoot, id, slug, content string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, ".plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .plans: %v", err)
	}
	path := filepath.Join(dir, id+"-"+slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	return path
}

// gitHeadSha returns the HEAD commit sha for repoRoot. initGitRepoWithCommit
// (worktree_test.go, same package) creates the one-commit repos these tests
// reuse.
func gitHeadSha(t *testing.T, repoRoot string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// gitCommitFile adds one commit touching relPath under repoRoot, for the
// staleness-scoping tests.
func gitCommitFile(t *testing.T, repoRoot, relPath, content string) {
	t.Helper()
	full := filepath.Join(repoRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", relPath)
	run("commit", "-q", "-m", "update "+relPath)
}

// -- fake gh ticket scripting via the `command` seam (retry.go) ------------

// fakeGhTicket scripts `gh issue view <id> --json state,updatedAt [--repo
// O/R]` with a fixed state/updatedAt response, recording every invocation.
type fakeGhTicket struct {
	t         *testing.T
	state     string
	updatedAt string
	calls     []ghCall
}

func newFakeGhTicket(t *testing.T, state, updatedAt string) *fakeGhTicket {
	t.Helper()
	return &fakeGhTicket{t: t, state: state, updatedAt: updatedAt}
}

func (f *fakeGhTicket) install() {
	f.t.Helper()
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		call := ghCall{name: name, args: args}
		f.calls = append(f.calls, call)
		if name == "gh" && call.is("issue", "view") {
			payload := `{"state":"` + f.state + `","updatedAt":"` + f.updatedAt + `"}`
			return []byte(payload), nil
		}
		f.t.Fatalf("unexpected command invocation: %s", call)
		return nil, nil
	}
	f.t.Cleanup(func() { command = original })
}

// -- valid: sha == HEAD, ticket OPEN & fresh -> resume ----------------------

func TestCheckPlan_Valid_FreshShaAndOpenTicket_ReturnsResume(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"
	path := writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z") // updatedAt <= createdAt
	gh.install()

	state, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: %v", err)
	}
	if check.Decision != "resume" {
		t.Errorf("Decision = %q, want resume", check.Decision)
	}
	if state.Stage != StageNew {
		t.Errorf("State.Stage = %q, want new (no state file seeded)", state.Stage)
	}
	if len(check.Paths) != 1 || check.Paths[0] != path {
		t.Errorf("Paths = %v, want [%s]", check.Paths, path)
	}
	if check.Plan == nil {
		t.Fatal("Plan metadata must be echoed on a resume decision")
	}
	if check.Plan.Mode != "ticket" {
		t.Errorf("Plan.Mode = %q, want ticket", check.Plan.Mode)
	}
	if check.Plan.Slug != "add-thing" {
		t.Errorf("Plan.Slug = %q, want add-thing", check.Plan.Slug)
	}
	if check.Plan.TicketID != 42 {
		t.Errorf("Plan.TicketID = %d, want 42", check.Plan.TicketID)
	}
	if check.Plan.IsChild {
		t.Error("Plan.IsChild = true, want false")
	}
	if check.Plan.IsLastChild {
		t.Error("Plan.IsLastChild = true, want false")
	}
	if check.Plan.ParentID != 0 {
		t.Errorf("Plan.ParentID = %d, want 0 (null)", check.Plan.ParentID)
	}
	if calls := gh.calls; len(calls) == 0 {
		t.Error("expected a `gh issue view` freshness call")
	} else if !calls[0].hasFlag("--repo", "o/r") {
		t.Errorf("gh issue view call = %v, want --repo o/r", calls[0])
	}
}

// -- malformed: distinct failure classes, all ErrPlanMalformed --------------

func TestCheckPlan_Malformed_MissingImplementationPlanSection_ReturnsErrPlanMalformed(t *testing.T) {
	repoRoot := t.TempDir()
	calls := recordingCommand(t)
	bodyMissingSection := `
## Ticket Details
some ticket details

## Architectural Context
some architectural context

## Design Context
some design context
`
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", "deadbeef", "2026-07-20T20:00:00Z"), bodyMissingSection)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err == nil {
		t.Fatal("CheckPlan with a plan file missing ## Implementation Plan: want an error, got nil")
	}
	if !errors.Is(err, ErrPlanMalformed) {
		t.Errorf("error = %v, want errors.Is(_, ErrPlanMalformed)", err)
	}
	if !strings.Contains(err.Error(), "## Implementation Plan") {
		t.Errorf("error = %q, want it to name the missing section", err.Error())
	}
	if check.Decision != "" {
		t.Errorf("Decision = %q, want unset on a malformed plan", check.Decision)
	}
	if len(*calls) != 0 {
		t.Errorf("gh/git calls = %v, want none (malformed content must fail before any freshness check)", *calls)
	}
}

func TestCheckPlan_Malformed_MissingOrUnterminatedFrontMatter_ReturnsErrPlanMalformed(t *testing.T) {
	cases := map[string]string{
		"no front matter at all": "## Ticket Details\nx\n## Implementation Plan\nx\n## Architectural Context\nx\n## Design Context\nx\n",
		"unterminated front matter": "---\nversion: 1\nmode: ticket\nticketId: 42\nslug: add-thing\n" +
			"## Ticket Details\nx\n## Implementation Plan\nx\n## Architectural Context\nx\n## Design Context\nx\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			repoRoot := t.TempDir()
			calls := recordingCommand(t)
			writeRawPlanFile(t, repoRoot, "42", "add-thing", content)

			_, _, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !errors.Is(err, ErrPlanMalformed) {
				t.Errorf("error = %v, want errors.Is(_, ErrPlanMalformed)", err)
			}
			if !strings.Contains(err.Error(), "front matter") {
				t.Errorf("error = %q, want it to mention the front-matter failure", err.Error())
			}
			if len(*calls) != 0 {
				t.Errorf("gh/git calls = %v, want none", *calls)
			}
		})
	}
}

func TestCheckPlan_Malformed_InvalidSlug_ReturnsErrPlanMalformed(t *testing.T) {
	invalidSlugs := []string{"", ".", "..", "foo..bar", "bad slug", "bad/slug"}
	for _, slug := range invalidSlugs {
		t.Run(slug, func(t *testing.T) {
			repoRoot := t.TempDir()
			calls := recordingCommand(t)
			fields := defaultPlanFields("42", "placeholder", "deadbeef", "2026-07-20T20:00:00Z")
			fields["slug"] = slug
			// Filename must stay valid regardless of the (possibly invalid)
			// slug value under test, so the glob still finds exactly one
			// match for CheckPlan to validate.
			writePlanFile(t, repoRoot, "42", "placeholder", fields, validPlanBody)

			_, _, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
			if err == nil {
				t.Fatalf("slug %q: want an error, got nil", slug)
			}
			if !errors.Is(err, ErrPlanMalformed) {
				t.Errorf("slug %q: error = %v, want errors.Is(_, ErrPlanMalformed)", slug, err)
			}
			if !strings.Contains(err.Error(), "slug") {
				t.Errorf("slug %q: error = %q, want it to mention the invalid-slug failure", slug, err.Error())
			}
			if len(*calls) != 0 {
				t.Errorf("slug %q: gh/git calls = %v, want none", slug, *calls)
			}
		})
	}
}

// -- stale (git): commits-behind scoped to stalenessPaths -------------------

func TestCheckPlan_StaleGit_ScopedCommitsBehind_ReturnsStale(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)
	// stalenessPaths is "watch, flow" (defaultPlanFields) -- touch a path in
	// scope so the commits-behind count is non-zero.
	gitCommitFile(t, repoRoot, "watch/main.go", "watch change")

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z")
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: %v", err)
	}
	if check.Decision != "stale" {
		t.Errorf("Decision = %q, want stale", check.Decision)
	}
}

func TestCheckPlan_StaleGit_UnrelatedPathCommit_ScopingRegression_NotStale(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)
	// stalenessPaths is "watch, flow" -- this commit touches neither, so the
	// scoped commits-behind count must stay 0 despite HEAD moving forward.
	gitCommitFile(t, repoRoot, "docs/unrelated.md", "unrelated change")

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z")
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: %v", err)
	}
	if check.Decision != "resume" {
		t.Errorf("Decision = %q, want resume (an unrelated-path commit outside stalenessPaths must not mark the plan stale)", check.Decision)
	}
}

// -- freshness-check failures: propagated as errors, never silently resume
// (#560 item 1/2/8) ----------------------------------------------------------

// TestCheckPlan_StaleGit_CommitsBehindFailure_ReturnsErrorNotResume pins the
// item-1 fix: a git rev-list failure (here, a well-formed-looking sha that
// simply does not exist in this repo) must surface as a genuine CheckPlan
// error with decision left unset, never silently resolve to "resume" the way
// the pre-fix CommitsBehind-swallows-to-0 behavior would have.
func TestCheckPlan_StaleGit_CommitsBehindFailure_ReturnsErrorNotResume(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" // valid format, unreachable
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z")
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err == nil {
		t.Fatal("CheckPlan with an unreachable planCommitSha: want an error, got nil")
	}
	if check.Decision != "" {
		t.Errorf("Decision = %q, want unset (a commits-behind failure must never resolve to resume/stale)", check.Decision)
	}
	if errors.Is(err, ErrPlanMalformed) {
		t.Errorf("error = %v, want a freshness-check failure distinct from ErrPlanMalformed", err)
	}
	if !strings.Contains(err.Error(), "commits behind") {
		t.Errorf("error = %q, want it to name the commits-behind failure", err.Error())
	}
	if len(gh.calls) != 0 {
		t.Errorf("gh calls = %v, want none (a git failure must short-circuit before the ticket freshness check)", gh.calls)
	}
}

// TestCheckPlan_StaleGit_InvalidPlanCommitShaFormat_ReturnsErrPlanMalformed
// pins the item-2 fix: a planCommitSha that could be parsed as a git option
// (leading `-`) is rejected before it ever reaches `git rev-list`.
func TestCheckPlan_StaleGit_InvalidPlanCommitShaFormat_ReturnsErrPlanMalformed(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	calls := recordingCommand(t)
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", "-not-a-sha", createdAt), validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err == nil {
		t.Fatal("CheckPlan with a malformed planCommitSha: want an error, got nil")
	}
	if !errors.Is(err, ErrPlanMalformed) {
		t.Errorf("error = %v, want errors.Is(_, ErrPlanMalformed)", err)
	}
	if !strings.Contains(err.Error(), "planCommitSha") {
		t.Errorf("error = %q, want it to name the invalid planCommitSha", err.Error())
	}
	if check.Decision != "" {
		t.Errorf("Decision = %q, want unset on a malformed planCommitSha", check.Decision)
	}
	if len(*calls) != 0 {
		t.Errorf("gh calls = %v, want none (an invalid planCommitSha must fail validation before any freshness check)", *calls)
	}
}

// TestCheckPlan_FreshnessFailureMessages_AreContentDistinct is the rule-#446
// regression guard item 8 calls for: decision "" now has two failure
// classes (ErrPlanMalformed vs. an unwrapped freshness-check failure), and
// the freshness-check failure itself has more than one distinct cause (git
// commits-behind vs. `gh issue view`). Assert none of their messages
// collapse to an indistinguishable placeholder.
func TestCheckPlan_FreshnessFailureMessages_AreContentDistinct(t *testing.T) {
	createdAt := "2026-07-20T20:00:00Z"

	// Class 1: ErrPlanMalformed (invalid planCommitSha format).
	malformedRoot := t.TempDir()
	initGitRepoWithCommit(t, malformedRoot)
	writePlanFile(t, malformedRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", "-not-a-sha", createdAt), validPlanBody)
	_, _, malformedErr := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: malformedRoot, RepoSlug: "o/r"})
	if malformedErr == nil {
		t.Fatal("want a malformed-sha error, got nil")
	}

	// Class 2: git commits-behind failure (unreachable sha).
	gitFailRoot := t.TempDir()
	initGitRepoWithCommit(t, gitFailRoot)
	sha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	writePlanFile(t, gitFailRoot, "43", "add-thing", defaultPlanFields("43", "add-thing", sha, createdAt), validPlanBody)
	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z")
	gh.install()
	_, _, gitFailErr := CheckPlan(PlanCheckOpts{ID: "43", RepoRoot: gitFailRoot, RepoSlug: "o/r"})
	if gitFailErr == nil {
		t.Fatal("want a commits-behind-failure error, got nil")
	}

	// Class 3: `gh issue view` failure (git succeeds; gh does not).
	ghFailRoot := t.TempDir()
	initGitRepoWithCommit(t, ghFailRoot)
	freshSha := gitHeadSha(t, ghFailRoot)
	writePlanFile(t, ghFailRoot, "44", "add-thing", defaultPlanFields("44", "add-thing", freshSha, createdAt), validPlanBody)
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		return []byte("boom"), errors.New("boom")
	}
	t.Cleanup(func() { command = original })
	_, _, ghFailErr := CheckPlan(PlanCheckOpts{ID: "44", RepoRoot: ghFailRoot, RepoSlug: "o/r"})
	if ghFailErr == nil {
		t.Fatal("want a gh-failure error, got nil")
	}

	messages := map[string]string{
		"malformed sha": malformedErr.Error(),
		"git failure":   gitFailErr.Error(),
		"gh failure":    ghFailErr.Error(),
	}
	seen := map[string]string{}
	for name, msg := range messages {
		if other, dup := seen[msg]; dup {
			t.Errorf("%s and %s produced the identical error message %q -- rule #446 requires content-distinct messages per failure class", name, other, msg)
		}
		seen[msg] = name
	}
	if !errors.Is(malformedErr, ErrPlanMalformed) {
		t.Errorf("malformed-sha error = %v, want errors.Is(_, ErrPlanMalformed)", malformedErr)
	}
	if errors.Is(gitFailErr, ErrPlanMalformed) || errors.Is(ghFailErr, ErrPlanMalformed) {
		t.Error("freshness-check failures (git/gh) must not wrap ErrPlanMalformed -- distinct failure class from a malformed plan file")
	}
}

// -- stale (ticket): closed state, or updatedAt drift ------------------------

func TestCheckPlan_StaleTicket_ClosedState_ReturnsStale(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)

	gh := newFakeGhTicket(t, "CLOSED", "2026-07-20T19:00:00Z")
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: %v", err)
	}
	if check.Decision != "stale" {
		t.Errorf("Decision = %q, want stale (ticket state is CLOSED)", check.Decision)
	}
}

func TestCheckPlan_StaleTicket_UpdatedAtAfterCreatedAt_ReturnsStale(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T21:00:00Z") // after createdAt
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: %v", err)
	}
	if check.Decision != "stale" {
		t.Errorf("Decision = %q, want stale (ticket updatedAt is after the plan's createdAt)", check.Decision)
	}
}

// -- freshness baseline: the pipeline's own label edits are not drift (#669) -

// seedStateWithBaseline writes a pipeline state file carrying a recorded
// post-label-edit ticketUpdatedAt baseline, as ApplyLabelTransition persists
// it.
func seedStateWithBaseline(t *testing.T, stateDir, id string, stage Stage, ticketUpdatedAt string) {
	t.Helper()
	path := filepath.Join(stateDir, id+".json")
	s := State{SchemaVersion: CurrentSchemaVersion, ID: id, Stage: stage, TicketUpdatedAt: ticketUpdatedAt}
	if err := saveState(path, s); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

func TestCheckPlan_PipelineOwnLabelEdit_CoveredByBaseline_ReturnsResume(t *testing.T) {
	// Regression (#669): persisting a plan stamps createdAt, then the
	// pipeline's own `planned` label swap bumps the ticket's updatedAt past
	// it — previously making every freshly persisted plan report stale with
	// zero repo churn. With the post-edit updatedAt recorded as the state's
	// baseline, that self-inflicted bump must classify as resume.
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)

	stateDir := t.TempDir()
	seedStateWithBaseline(t, stateDir, "42", StageWaitingForPlanApproval, "2026-07-20T20:01:00Z")

	// The ticket's updatedAt equals the recorded baseline: the last edit
	// was the pipeline's own label swap, not a user edit.
	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T20:01:00Z")
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: %v", err)
	}
	if check.Decision != "resume" {
		t.Errorf("Decision = %q, want resume (updatedAt matches the recorded post-label-edit baseline)", check.Decision)
	}
}

func TestCheckPlan_TicketEditedAfterBaseline_ReturnsStale(t *testing.T) {
	// A genuine ticket edit after the recorded baseline is still drift.
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)

	stateDir := t.TempDir()
	seedStateWithBaseline(t, stateDir, "42", StageWaitingForPlanApproval, "2026-07-20T20:01:00Z")

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T22:00:00Z") // after the baseline
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: %v", err)
	}
	if check.Decision != "stale" {
		t.Errorf("Decision = %q, want stale (ticket updatedAt is after the recorded baseline)", check.Decision)
	}
}

func TestCheckPlan_MalformedBaselineInState_ReturnsErrorNotResume(t *testing.T) {
	// An unverifiable freshness signal must never silently default to
	// "resume" (#560 item 1), matching the surrounding parse-failure
	// handling with a content-distinct message (rule #446).
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)

	stateDir := t.TempDir()
	seedStateWithBaseline(t, stateDir, "42", StageWaitingForPlanApproval, "not-a-timestamp")

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T21:00:00Z")
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, RepoSlug: "o/r"})
	if err == nil {
		t.Fatalf("CheckPlan with a malformed baseline: want an error, got decision %q", check.Decision)
	}
	if !strings.Contains(err.Error(), "ticketUpdatedAt") {
		t.Errorf("error = %q, want it to name the malformed ticketUpdatedAt baseline", err.Error())
	}
}

// -- awaiting-input (#826): a draft plan short-circuits before freshness ----

// TestCheckPlan_AwaitingInputStatus_ReturnsAwaitingInputDecision covers the
// new decision itself: a plan file whose front matter carries
// status: awaiting-input must classify as "awaiting-input", carry the
// echoed plan metadata, a nil error, and make zero git/gh calls (it must
// never reach planIsStale).
func TestCheckPlan_AwaitingInputStatus_ReturnsAwaitingInputDecision(t *testing.T) {
	repoRoot := t.TempDir()
	calls := recordingCommand(t)
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	path := writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan with an awaiting-input draft: unexpected error: %v", err)
	}
	if check.Decision != "awaiting-input" {
		t.Errorf("Decision = %q, want awaiting-input", check.Decision)
	}
	if len(check.Paths) != 1 || check.Paths[0] != path {
		t.Errorf("Paths = %v, want [%s]", check.Paths, path)
	}
	if check.Plan == nil {
		t.Fatal("Plan metadata must be echoed on an awaiting-input decision")
	}
	if check.Plan.Slug != "add-thing" || check.Plan.TicketID != 42 {
		t.Errorf("Plan = %+v, want slug=add-thing ticketId=42", check.Plan)
	}
	if len(*calls) != 0 {
		t.Errorf("gh/git calls = %v, want none (awaiting-input must short-circuit before any freshness check)", *calls)
	}
}

// TestCheckPlan_AwaitingInputStatus_WinsOverStaleness proves the decision
// ordering: even a plan whose planCommitSha is unreachable (which would
// otherwise surface a commits-behind error, or -- if reachable and stale --
// a "stale" decision) must classify as "awaiting-input" instead, since the
// awaiting-input check runs strictly before planIsStale.
func TestCheckPlan_AwaitingInputStatus_WinsOverStaleness(t *testing.T) {
	repoRoot := t.TempDir()
	calls := recordingCommand(t)
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: unexpected error: %v", err)
	}
	if check.Decision != "awaiting-input" {
		t.Errorf("Decision = %q, want awaiting-input (must win over a would-be staleness/freshness-failure verdict)", check.Decision)
	}
	if len(*calls) != 0 {
		t.Errorf("gh/git calls = %v, want none", *calls)
	}
}

// TestCheckPlan_AwaitingInputStatus_LosesToReplanRequested proves the
// escape-hatch ordering (the plan's Assumptions): --replan-requested still
// short-circuits first, ahead of the awaiting-input check.
func TestCheckPlan_AwaitingInputStatus_LosesToReplanRequested(t *testing.T) {
	repoRoot := t.TempDir()
	calls := recordingCommand(t)
	fields := defaultPlanFields("42", "add-thing", "deadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r", ReplanRequested: true})
	if err != nil {
		t.Fatalf("CheckPlan: unexpected error: %v", err)
	}
	if check.Decision != "replan" {
		t.Errorf("Decision = %q, want replan (--replan-requested must short-circuit ahead of the awaiting-input check)", check.Decision)
	}
	if len(*calls) != 0 {
		t.Errorf("gh/git calls = %v, want none", *calls)
	}
}

// -- replan: short-circuits regardless of freshness --------------------------

func TestCheckPlan_ReplanRequested_ReturnsReplanRegardlessOfFreshness(t *testing.T) {
	repoRoot := t.TempDir()
	calls := recordingCommand(t)
	// A deliberately stale-looking sha (never a real commit in this repo,
	// and no git repo initialized at all) proves freshness is never computed
	// once ReplanRequested short-circuits.
	writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", "deadbeef", "2026-07-20T20:00:00Z"), validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r", ReplanRequested: true})
	if err != nil {
		t.Fatalf("CheckPlan: %v", err)
	}
	if check.Decision != "replan" {
		t.Errorf("Decision = %q, want replan", check.Decision)
	}
	if len(*calls) != 0 {
		t.Errorf("gh calls = %v, want none (--replan-requested must short-circuit before any freshness check)", *calls)
	}
}

// -- discovery: zero matches -> none, two matches -> multiple ---------------

func TestCheckPlan_Discovery_NoMatches_ReturnsNone(t *testing.T) {
	repoRoot := t.TempDir()
	calls := recordingCommand(t)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err == nil {
		t.Fatal("CheckPlan with no matching plan file: want an error, got nil")
	}
	if !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("error = %v, want errors.Is(_, ErrPlanNotFound)", err)
	}
	if check.Decision != "none" {
		t.Errorf("Decision = %q, want none", check.Decision)
	}
	if len(check.Paths) != 0 {
		t.Errorf("Paths = %v, want empty", check.Paths)
	}
	if len(*calls) != 0 {
		t.Errorf("gh/git calls = %v, want none (discovery must fail before any freshness check)", *calls)
	}
}

func TestCheckPlan_Discovery_TwoMatches_ReturnsMultiple(t *testing.T) {
	repoRoot := t.TempDir()
	calls := recordingCommand(t)
	pathA := writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", "deadbeef", "2026-07-20T20:00:00Z"), validPlanBody)
	pathB := writePlanFile(t, repoRoot, "42", "add-thing-v2", defaultPlanFields("42", "add-thing-v2", "deadbeef", "2026-07-20T20:00:00Z"), validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err == nil {
		t.Fatal("CheckPlan with two matching plan files: want an error, got nil")
	}
	if !errors.Is(err, ErrMultiplePlans) {
		t.Errorf("error = %v, want errors.Is(_, ErrMultiplePlans)", err)
	}
	if check.Decision != "multiple" {
		t.Errorf("Decision = %q, want multiple", check.Decision)
	}
	got := append([]string{}, check.Paths...)
	sort.Strings(got)
	want := []string{pathA, pathB}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Paths = %v, want both %v", check.Paths, want)
	}
	if len(*calls) != 0 {
		t.Errorf("gh/git calls = %v, want none (ambiguous discovery must fail before any freshness check)", *calls)
	}
}

// -- state combos: CheckPlan is stage-agnostic and read-only ----------------

func TestCheckPlan_StageAgnostic_EchoesPersistedStageWithoutMutating(t *testing.T) {
	stages := []Stage{StageNew, StagePrepared, StageWaitingForPlanApproval}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			repoRoot := t.TempDir()
			initGitRepoWithCommit(t, repoRoot)
			sha := gitHeadSha(t, repoRoot)
			createdAt := "2026-07-20T20:00:00Z"
			writePlanFile(t, repoRoot, "42", "add-thing", defaultPlanFields("42", "add-thing", sha, createdAt), validPlanBody)

			stateDir := t.TempDir()
			if stage != StageNew {
				mustSeedState(t, stateDir, "42", stage)
			}

			gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z")
			gh.install()

			got, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, StateDir: stateDir, RepoSlug: "o/r"})
			if err != nil {
				t.Fatalf("CheckPlan: %v", err)
			}
			if got.Stage != stage {
				t.Errorf("returned State.Stage = %q, want %q (CheckPlan must not gate on stage)", got.Stage, stage)
			}
			if check.Decision != "resume" {
				t.Errorf("Decision = %q, want resume", check.Decision)
			}

			// Confirm CheckPlan is read-only: the persisted state must still
			// report the same stage after the call, not something CheckPlan
			// itself wrote.
			reloaded, err := GetArtifacts(ArtifactOpts{ID: "42", StateDir: stateDir})
			if err != nil {
				t.Fatalf("GetArtifacts after CheckPlan: %v", err)
			}
			if reloaded.Stage != stage {
				t.Errorf("persisted State.Stage after CheckPlan = %q, want unchanged %q", reloaded.Stage, stage)
			}
		})
	}
}

// -- escalation anchor echo (#849): PlanMeta gains EscalationNonce/
// EscalationCommentID, echoed alongside every other front-matter field on
// the awaiting-input decision. Per the plan's Assumptions, this echo is
// deliberately unvalidated -- CheckPlan never promotes a missing/malformed
// anchor to ErrPlanMalformed, so a draft stays repairable.

func TestCheckPlan_AwaitingInput_EscalationAnchor_PresentValid_Echoed(t *testing.T) {
	repoRoot := t.TempDir()
	calls := recordingCommand(t)
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	fields["escalationNonce"] = "0123456789abcdef0123456789abcdef"
	fields["escalationCommentId"] = "123456789"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: unexpected error: %v", err)
	}
	if check.Decision != "awaiting-input" {
		t.Fatalf("Decision = %q, want awaiting-input", check.Decision)
	}
	if check.Plan == nil {
		t.Fatal("Plan metadata must be echoed on an awaiting-input decision")
	}
	if check.Plan.EscalationNonce != "0123456789abcdef0123456789abcdef" {
		t.Errorf("Plan.EscalationNonce = %q, want the front-matter nonce echoed verbatim", check.Plan.EscalationNonce)
	}
	if check.Plan.EscalationCommentID != 123456789 {
		t.Errorf("Plan.EscalationCommentID = %d, want 123456789", check.Plan.EscalationCommentID)
	}
	if len(*calls) != 0 {
		t.Errorf("gh/git calls = %v, want none (awaiting-input must short-circuit before any freshness check)", *calls)
	}
}

// TestCheckPlan_AwaitingInput_EscalationAnchor_Absent_ZeroValues is a
// non-regression assertion (AC5's "absent fields echo zero values without
// changing the decision or the exit code"): it is expected to PASS already
// today, since PlanMeta's stub fields default to the zero value with no
// front-matter keys present at all -- there is no new logic gap to expose
// here. Kept alongside the present/malformed cases below for a complete
// present/absent/malformed table per the plan's Test Strategy.
func TestCheckPlan_AwaitingInput_EscalationAnchor_Absent_ZeroValues(t *testing.T) {
	repoRoot := t.TempDir()
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: unexpected error: %v", err)
	}
	if check.Decision != "awaiting-input" {
		t.Errorf("Decision = %q, want awaiting-input (absent anchor fields must not change the decision)", check.Decision)
	}
	if check.Plan == nil {
		t.Fatal("Plan metadata must be echoed")
	}
	if check.Plan.EscalationNonce != "" {
		t.Errorf("Plan.EscalationNonce = %q, want empty (absent from front matter)", check.Plan.EscalationNonce)
	}
	if check.Plan.EscalationCommentID != 0 {
		t.Errorf("Plan.EscalationCommentID = %d, want 0 (absent from front matter)", check.Plan.EscalationCommentID)
	}
}

// TestCheckPlan_AwaitingInput_EscalationAnchor_MalformedNonce_EchoedRaw
// covers the malformed-nonce half of the present/absent/malformed table:
// PlanMeta's echo is deliberately unvalidated (the plan's Assumptions:
// "echoes ... but does not promote missing/malformed anchor data to
// ErrPlanMalformed"), so a nonce that fails escalationNoncePattern must
// still be echoed verbatim -- rejection is each consumer's job, not
// plan-check's. Paired with a valid escalationCommentId so this test stays
// red purely on the not-yet-wired nonce echo, not incidentally on the ID.
func TestCheckPlan_AwaitingInput_EscalationAnchor_MalformedNonce_EchoedRaw(t *testing.T) {
	repoRoot := t.TempDir()
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	fields["escalationNonce"] = "not-hex-and-wrong-length"
	fields["escalationCommentId"] = "42"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: unexpected error: %v (malformed anchor data must never become ErrPlanMalformed)", err)
	}
	if check.Decision != "awaiting-input" {
		t.Errorf("Decision = %q, want awaiting-input (a malformed nonce must not change the decision)", check.Decision)
	}
	if check.Plan == nil {
		t.Fatal("Plan metadata must be echoed")
	}
	if check.Plan.EscalationNonce != "not-hex-and-wrong-length" {
		t.Errorf("Plan.EscalationNonce = %q, want the malformed value echoed verbatim (echo-only, unvalidated)", check.Plan.EscalationNonce)
	}
	if check.Plan.EscalationCommentID != 42 {
		t.Errorf("Plan.EscalationCommentID = %d, want 42 (the paired valid ID)", check.Plan.EscalationCommentID)
	}
}

// TestCheckPlan_AwaitingInput_EscalationAnchor_MalformedCommentId_ParsesToZero
// covers the malformed-ID half: a non-numeric escalationCommentId must
// parse to 0 (a strconv.ParseInt failure), never propagate as a validation
// error. Paired with a valid nonce so this test stays red purely on the
// not-yet-wired ID echo, not incidentally on the nonce.
func TestCheckPlan_AwaitingInput_EscalationAnchor_MalformedCommentId_ParsesToZero(t *testing.T) {
	repoRoot := t.TempDir()
	fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
	fields["status"] = "awaiting-input"
	fields["escalationNonce"] = "0123456789abcdef0123456789abcdef"
	fields["escalationCommentId"] = "not-a-number"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan: unexpected error: %v (malformed anchor data must never become ErrPlanMalformed)", err)
	}
	if check.Plan == nil {
		t.Fatal("Plan metadata must be echoed")
	}
	if check.Plan.EscalationCommentID != 0 {
		t.Errorf("Plan.EscalationCommentID = %d, want 0 (non-numeric value must parse to 0, never propagate as an error)", check.Plan.EscalationCommentID)
	}
	if check.Plan.EscalationNonce != "0123456789abcdef0123456789abcdef" {
		t.Errorf("Plan.EscalationNonce = %q, want the paired valid nonce echoed verbatim", check.Plan.EscalationNonce)
	}
}

// -- sentinel identity (#412: a direct unit test at the package boundary) --

func TestPlanCheckSentinelErrors_AreDistinctAndDetectableViaErrorsIs(t *testing.T) {
	sentinels := map[string]error{
		"ErrPlanNotFound":  ErrPlanNotFound,
		"ErrPlanMalformed": ErrPlanMalformed,
		"ErrMultiplePlans": ErrMultiplePlans,
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
