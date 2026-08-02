package dispatch

// Ticket #855: the shared fixture layer for the autonomous-dependency-chain
// end-to-end regression (chain_e2e_test.go). This file provides:
//
//   - ghWorld: a JSON-persisted model of GitHub state (issues, PRs, checks,
//     comments, reviews, repo settings, merge-queue state) that a re-exec'd
//     test-binary helper process (TestChainGhFakeHelper) reads/mutates on
//     every `gh` invocation -- serialized by a flock on a sibling lock file
//     (withChainWorldLock) so the SAME world is visible, without lost
//     updates, across dispatch/pipeline/babysit's independent `gh` subprocess
//     calls.
//   - installChainGH: writes a `gh` PATH shim that re-execs this test binary
//     as the helper process, PREPENDED to PATH (never replacing it) so the
//     real `git` binary stays resolvable (collect_test.go:28-33's documented
//     hazard).
//   - installFakeCenciProcess: records babysit.launch's `cenci run <workflow>`
//     invocations by swapping os.Args[0] to a recording shim.
//   - serveChainSnapshot: a real ipc/broadcast-socket daemon double, so
//     dispatch.RunOnce's ReadSnapshot gets a real, non-nil snapshot instead of
//     failing closed on "daemon unreachable" (decide.go's Snapshot == nil gate).
//   - git fixture helpers layered on the package's existing mainSyncGitEnv /
//     initOriginAndLocal / gitTest / commitFile / writeCommittedConfig /
//     writePlan.
//
// Never call t.Parallel() in any test using this file: os.Args[0] mutation
// (installFakeCenciProcess) is process-global, and babysit.localRepoRoot's
// bare `git rev-parse --show-toplevel` requires t.Chdir.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// -- ghWorld: the persisted fake-GitHub model --------------------------------

type ghIssue struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	State     string   `json:"state"` // "OPEN" (default/zero) or "CLOSED"
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	UpdatedAt string   `json:"updatedAt"`

	// Milestone and SubIssues (#914) are world-model fidelity only: no
	// production code in this repo consumes either field (Q3), so there is
	// no new `gh` route or `--json` field-switch entry for them -- they
	// exist solely so the golden-fixture-seeded positive scenario can assert
	// both are byte-identical after every real `gh issue edit` mutation
	// (assertGoldenFidelityUnchanged, chain_e2e_test.go).
	Milestone *ghMilestone `json:"milestone,omitempty"`
	SubIssues []ghSubIssue `json:"subIssues,omitempty"`
}

// ghMilestone mirrors the golden fixture's `{number,title}` milestone shape.
type ghMilestone struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// ghSubIssue mirrors the golden fixture's native sub-issue link shape
// (`{number,state}`).
type ghSubIssue struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

func (i *ghIssue) effectiveState() string {
	if i.State == "" {
		return "OPEN"
	}
	return i.State
}

type ghPR struct {
	Number        int      `json:"number"`
	State         string   `json:"state"` // "OPEN" (default/zero), "MERGED", "CLOSED"
	HeadRefName   string   `json:"headRefName"`
	HeadRefOID    string   `json:"headRefOid"`
	BaseRefName   string   `json:"baseRefName"`
	IsDraft       bool     `json:"isDraft"`
	Mergeable     string   `json:"mergeable"`
	ChangedFiles  int      `json:"changedFiles"`
	Additions     int      `json:"additions"`
	Deletions     int      `json:"deletions"`
	Files         []string `json:"files"`
	ClosingIssues []int    `json:"closingIssues"`
	URL           string   `json:"url"`
}

func (p *ghPR) effectiveState() string {
	if p.State == "" {
		return "OPEN"
	}
	return p.State
}

// ghComment mirrors the shape both the issues-comments REST endpoint
// (dispatch's escalation-answer probe) and the PR review-comments endpoint
// (babysit's feedback detection) need. AuthorAssociation/Type matter only to
// the escalation-answer consumer; babysit's comment/review consumers ignore
// them.
type ghComment struct {
	ID                int64  `json:"id"`
	Body              string `json:"body"`
	Login             string `json:"login"`
	Type              string `json:"type"` // "User" or "Bot"
	AuthorAssociation string `json:"authorAssociation"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

type ghReview struct {
	ID          int64  `json:"id"`
	State       string `json:"state"`
	Login       string `json:"login"`
	SubmittedAt string `json:"submittedAt"`
}

type ghCheck struct {
	Bucket string `json:"bucket"`
	Name   string `json:"name"`
	State  string `json:"state"`
}

type ghRepoSettings struct {
	AllowSquash bool `json:"allowSquash"`
	AllowMerge  bool `json:"allowMerge"`
	AllowRebase bool `json:"allowRebase"`
}

type ghMergeQueueState struct {
	InMergeQueue bool `json:"inMergeQueue"`
	Enabled      bool `json:"enabled"`
}

// ghAmbiguity is one declarative, JSON-serializable fault-injection spec
// (#914 plan): the gh handler runs in a re-exec'd helper process, so a Go
// closure or parent-process table would be invisible to it -- only
// something that round-trips through the persisted world JSON works.
//
// Route is the chainRouteKey classification of the gh invocation this spec
// targets ("issue edit", "pr merge", "pr view", "pr checks", "api graphql",
// ...). OnCall is the 1-indexed ordinal occurrence of that route this spec
// fires on; zero (the default) matches every occurrence of the route,
// unconditionally. Kind is one of "ambiguous-success" / "indeterminate" /
// "truncate" / "timeout" / "state-change". Payload carries the
// "state-change" kind's named declarative op (e.g. "reopen-review",
// optionally suffixed "op:value" for parameterized ops).
type ghAmbiguity struct {
	Route   string `json:"route"`
	OnCall  int    `json:"onCall,omitempty"`
	Kind    string `json:"kind"`
	Payload string `json:"payload,omitempty"`
}

// ghOpenPRPageOverride makes #881's `gh api graphql` open-PR inventory page
// response fully injectable (#914 plan step 2): hasNextPage, endCursor
// (including a non-advancing malformed-cursor case), a totalCount override
// (so a cap-exhausted verdict doesn't require seeding 2000+ real PRs), and
// the nested closingIssuesReferences.pageInfo.hasNextPage. A nil field means
// "no override, use the real computed default" (chainOpenPRPage's own
// pageSlice-driven pagination over world.PRs).
type ghOpenPRPageOverride struct {
	ForceHasNextPage       *bool   `json:"forceHasNextPage,omitempty"`
	ForceEndCursor         *string `json:"forceEndCursor,omitempty"`
	ForceTotalCount        *int    `json:"forceTotalCount,omitempty"`
	ForceNestedHasNextPage *bool   `json:"forceNestedHasNextPage,omitempty"`
}

// ghWorld is the full persisted fake-GitHub state, single-repo scoped (this
// harness always uses one repo, "o/r") for simplicity.
type ghWorld struct {
	Issues   map[int]*ghIssue    `json:"issues"`
	PRs      map[int]*ghPR       `json:"prs"`
	Checks   map[int][]ghCheck   `json:"checks"`     // keyed by PR number
	Comments map[int][]ghComment `json:"comments"`   // issue comments, keyed by issue number
	PRNotes  map[int][]ghComment `json:"prComments"` // PR review comments, keyed by PR number
	Reviews  map[int][]ghReview  `json:"reviews"`    // keyed by PR number

	RepoSettings ghRepoSettings            `json:"repoSettings"`
	MergeQueue   map[int]ghMergeQueueState `json:"mergeQueue"` // keyed by PR number

	// Permissions (#882) is the fake collaborator-permission table: keyed by
	// lowercased login (mirrors GitHub's own case-insensitive login
	// matching), value the top-level `permission` string GET
	// .../collaborators/<login>/permission would return ("admin", "write",
	// "read", "triage", "none", or any other value a test wants to exercise
	// the unrecognized-value class with). A login with no seeded entry
	// defaults to "none" in chainCollaboratorPermission -- an org member or
	// removed collaborator with no repo access still gets HTTP 200
	// permission:"none", never a 404 (the plan's Assumptions).
	Permissions map[string]string `json:"permissions"`

	CurrentLogin string `json:"currentLogin"`

	NextCommentID int64 `json:"nextCommentId"`
	NextReviewID  int64 `json:"nextReviewId"`

	// Invocations is an append-only log of every gh argv served this test,
	// space-joined, so assertions can pin exact call shapes (e.g. the
	// squash-only merge argv) without depending on call ordering elsewhere.
	Invocations []string `json:"invocations"`

	// Ambiguities (#914) is the fault-injection spec list applyAmbiguity
	// consults on every gh invocation; RouteCalls is the per-route ordinal
	// counter it bumps to resolve each spec's OnCall selector.
	Ambiguities []ghAmbiguity  `json:"ambiguities,omitempty"`
	RouteCalls  map[string]int `json:"routeCalls"`

	// OpenPRPageOverride (#914) makes the open-PR inventory page response
	// fully injectable; nil means no override anywhere in the page.
	OpenPRPageOverride *ghOpenPRPageOverride `json:"openPRPageOverride,omitempty"`
}

func newGhWorld() *ghWorld {
	return &ghWorld{
		Issues:       map[int]*ghIssue{},
		PRs:          map[int]*ghPR{},
		Checks:       map[int][]ghCheck{},
		Comments:     map[int][]ghComment{},
		PRNotes:      map[int][]ghComment{},
		Reviews:      map[int][]ghReview{},
		MergeQueue:   map[int]ghMergeQueueState{},
		Permissions:  map[string]string{},
		RouteCalls:   map[string]int{},
		CurrentLogin: "octocat",
		RepoSettings: ghRepoSettings{AllowSquash: true},
	}
}

// saveGhWorld persists world to path atomically (write-then-rename), so a
// crash mid-write never corrupts the file a subsequent gh invocation reads.
func saveGhWorld(path string, world *ghWorld) error {
	data, err := json.MarshalIndent(world, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadGhWorld reads world from path, returning a fresh empty world if the
// file does not exist yet (the first gh invocation of a test).
func loadGhWorld(path string) (*ghWorld, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newGhWorld(), nil
		}
		return nil, err
	}
	world := newGhWorld()
	if err := json.Unmarshal(data, world); err != nil {
		return nil, err
	}
	if world.Issues == nil {
		world.Issues = map[int]*ghIssue{}
	}
	if world.PRs == nil {
		world.PRs = map[int]*ghPR{}
	}
	if world.Checks == nil {
		world.Checks = map[int][]ghCheck{}
	}
	if world.Comments == nil {
		world.Comments = map[int][]ghComment{}
	}
	if world.PRNotes == nil {
		world.PRNotes = map[int][]ghComment{}
	}
	if world.Reviews == nil {
		world.Reviews = map[int][]ghReview{}
	}
	if world.MergeQueue == nil {
		world.MergeQueue = map[int]ghMergeQueueState{}
	}
	if world.Permissions == nil {
		world.Permissions = map[string]string{}
	}
	if world.RouteCalls == nil {
		world.RouteCalls = map[string]int{}
	}
	return world, nil
}

// withChainWorldLock holds an exclusive, blocking flock on a sibling
// "<worldPath>.lock" file for the duration of fn, serializing every re-exec'd
// gh helper process's load-mutate-save cycle against the SAME world file
// (mirrors internal/pipeline/lock.go's flock-guarded transitions -- that
// helper is unexported and package-scoped to pipeline, so it cannot be
// reused directly from package dispatch, but the pattern -- a blocking
// syscall.Flock on a plain lock file, released on fn's return -- is
// reproduced here at the same size). Without this, concurrent `gh`
// invocations (e.g. dispatch collecting multiple tickets concurrently) could
// silently drop each other's mutation on a last-writer-wins race, which
// would specifically undermine a negative variant's "must NOT be present"
// assertion by racing a lost update into looking like a clean pass.
func withChainWorldLock(worldPath string, fn func() error) error {
	lockPath := worldPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open chain world lock %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock chain world lock %s: %w", lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}

// -- chain test harness helpers driving the world from the TEST process -----

// chainHarness bundles every fixture handle a chain test needs.
type chainHarness struct {
	t            *testing.T
	worldPath    string
	local        string // local git checkout (the enrolled repo's Dir)
	origin       string // origin git repo (bare-equivalent: a plain non-bare repo the local clones from)
	repo         string // "owner/repo" slug, e.g. "o/r"
	cenciLogPath string // installFakeCenciProcess's recorded-invocation log

	// testBin (#914) is the real test-binary path (os.Args[0]), captured
	// BEFORE installFakeCenciProcess overwrites os.Args[0] with a recording
	// shim -- reExecReconcile spawns this, never the (by-then-mutated)
	// os.Args[0], to re-exec the actual test binary rather than the cenci
	// recorder shim.
	testBin string

	// reconcileCalls (#914) counts reExecReconcile invocations against this
	// harness: the first call runs a real (non-dry-run) reconcile pass, every
	// subsequent call is a dry run (see reExecReconcile's doc comment).
	reconcileCalls int
}

// newChainHarness wires the full fixture stack: real git origin/local repos,
// the gh PATH shim, the fake cenci process recorder, and a served daemon
// snapshot -- everything RunOnce/pipeline.Run/babysit.Run need to run for
// real. now (an idle, empty snapshot) is broadcast so budget/concurrency
// gates pass by default; callers needing a different snapshot should call
// serveChainSnapshot again with their own value.
//
// Defense-in-depth (never expected to matter given installChainGH's PATH
// shim, but cheap insurance against a future call site or a missing
// installChainGH call ever letting the real `gh` binary resolve and act on
// ambient credentials): neutralize every gh auth-resolution env var.
//
// XDG_CONFIG_HOME is ALWAYS pinned to a fresh harness-owned temp dir here
// (security review #914 finding #1), never left to whatever the process
// happened to inherit: applyStateChangeOp's "disable-fleet-automerge" op
// (below) reads this var and unconditionally overwrites
// <dir>/cenci/config.json -- if a developer's ambient XDG_CONFIG_HOME
// pointed at a real config dir (common) and a future scenario seeded that
// op, it would silently clobber their real ~/.config/cenci/config.json.
// setFleetAutomergeEnabled overrides this to its own temp dir for tests that
// need a specific automerge.enabled value; every other test still gets a
// harness-scoped (never a real) config dir.
func newChainHarness(t *testing.T) *chainHarness {
	t.Helper()
	// Captured before installFakeCenciProcess (below) overwrites os.Args[0]
	// with a recording shim -- reExecReconcile needs the REAL test binary.
	testBin := os.Args[0]
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")
	t.Setenv("GH_HOST", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)

	h := &chainHarness{
		t:         t,
		worldPath: filepath.Join(t.TempDir(), "ghworld.json"),
		local:     local,
		origin:    origin,
		repo:      "o/r",
		testBin:   testBin,
	}
	installChainGH(t, h.worldPath, h.local, h.origin, h.repo)
	h.cenciLogPath = installFakeCenciProcess(t)
	serveChainSnapshot(t, watch.StateSnapshot{})
	return h
}

// world reads the current fixture world, failing the test on a read error.
func (h *chainHarness) world() *ghWorld {
	h.t.Helper()
	w, err := loadGhWorld(h.worldPath)
	if err != nil {
		h.t.Fatalf("reading chain gh world: %v", err)
	}
	return w
}

// mutate loads the world, applies fn, and saves it back -- the harness-side
// counterpart to the fake gh helper's own load/mutate/save cycle, used to
// seed fixtures and to simulate GitHub-side events (a human's comment) the
// production code under test never itself produces.
func (h *chainHarness) mutate(fn func(w *ghWorld)) {
	h.t.Helper()
	w := h.world()
	fn(w)
	if err := saveGhWorld(h.worldPath, w); err != nil {
		h.t.Fatalf("saving chain gh world: %v", err)
	}
}

// seedIssue creates (or overwrites) an issue in the world.
func (h *chainHarness) seedIssue(number int, title, body string, labels, assignees []string) {
	h.mutate(func(w *ghWorld) {
		w.Issues[number] = &ghIssue{
			Number:    number,
			Title:     title,
			Body:      body,
			Labels:    append([]string{}, labels...),
			Assignees: append([]string{}, assignees...),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}
	})
}

// seedPR creates (or overwrites) a PR in the world.
func (h *chainHarness) seedPR(pr ghPR) {
	h.mutate(func(w *ghWorld) {
		cp := pr
		w.PRs[pr.Number] = &cp
	})
}

// seedChecks sets the check-run buckets for a PR.
func (h *chainHarness) seedChecks(pr int, checks ...ghCheck) {
	h.mutate(func(w *ghWorld) { w.Checks[pr] = checks })
}

// seedReview appends a review to a PR's review list, auto-assigning an ID.
func (h *chainHarness) seedReview(pr int, login, state, submittedAt string) int64 {
	var id int64
	h.mutate(func(w *ghWorld) {
		w.NextReviewID++
		id = w.NextReviewID
		w.Reviews[pr] = append(w.Reviews[pr], ghReview{ID: id, State: state, Login: login, SubmittedAt: submittedAt})
	})
	return id
}

// seedPermission sets login's current repository write permission (#882's
// top-level `permission` field from GET .../collaborators/<login>/permission),
// keyed case-insensitively (mirrors GitHub's own case-insensitive login
// matching) so a test can seed "octocat" and answer as "Octocat" without
// drift. A login with no seeded entry defaults to "none" in
// chainCollaboratorPermission below.
func (h *chainHarness) seedPermission(login, permission string) {
	h.mutate(func(w *ghWorld) {
		if w.Permissions == nil {
			w.Permissions = map[string]string{}
		}
		w.Permissions[strings.ToLower(login)] = permission
	})
}

// seedAmbiguity appends one fault-injection spec to the world's Ambiguities
// list, mirroring the harness's other seedX helpers above.
func (h *chainHarness) seedAmbiguity(a ghAmbiguity) {
	h.mutate(func(w *ghWorld) {
		w.Ambiguities = append(w.Ambiguities, a)
	})
}

// seedOpenPRPageOverride sets the open-PR inventory page-response override,
// mirroring the harness's other seedX helpers above.
func (h *chainHarness) seedOpenPRPageOverride(o ghOpenPRPageOverride) {
	h.mutate(func(w *ghWorld) {
		w.OpenPRPageOverride = &o
	})
}

// addIssueComment appends a comment to an issue's comment thread (the
// escalation anchor / an answering human / a forged duplicate), returning
// its auto-assigned ID.
func (h *chainHarness) addIssueComment(issue int, login, typ, assoc, body string) int64 {
	var id int64
	h.mutate(func(w *ghWorld) {
		w.NextCommentID++
		id = w.NextCommentID
		w.Comments[issue] = append(w.Comments[issue], ghComment{
			ID: id, Body: body, Login: login, Type: typ, AuthorAssociation: assoc,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
	})
	return id
}

// commitAndPushConfig commits content as .cenci/config.json on h.local's main
// and immediately pushes it to origin, so both sides start genuinely in sync
// -- a config commit landed on local's main WITHOUT a matching push would
// leave local one commit ahead of origin, and once origin later advances
// independently too (e.g. phase F's squash merge), syncMain would correctly
// (and confusingly, for an otherwise-unrelated test) classify the repo
// MainSyncDiverged.
func (h *chainHarness) commitAndPushConfig(content string) {
	h.t.Helper()
	// Committed directly on origin (never local, then pushed): origin has
	// "main" checked out, and a plain `git push` into a non-bare repo's
	// currently-checked-out branch is refused by git itself
	// (receive.denyCurrentBranch). local then fast-forwards to match via a
	// real fetch + --ff-only merge, so both sides end up at the exact same
	// commit rather than two independently-authored commits with identical
	// content (which would themselves count as a genuine divergence).
	writeCommittedConfig(h.t, h.origin, content)
	gitTest(h.t, h.local, "fetch", "origin")
	gitTest(h.t, h.local, "merge", "--ff-only", "origin/main")
}

// setFleetAutomergeEnabled writes the fleet-wide $XDG_CONFIG_HOME/cenci/config.json
// automerge.enabled kill switch babysit's loadFleetEnabled reads.
func setFleetAutomergeEnabled(t *testing.T, enabled bool) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "xdgconfig")
	t.Setenv("XDG_CONFIG_HOME", dir)
	cenciDir := filepath.Join(dir, "cenci")
	if err := os.MkdirAll(cenciDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"automerge":{"enabled":%v}}`, enabled)
	if err := os.WriteFile(filepath.Join(cenciDir, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requiredPlanBody assembles a minimal plan body carrying every section
// pipeline.CheckPlan requires (## Ticket Details / Implementation Plan /
// Architectural Context / Design Context), so every plan file this harness
// writes is valid input to CheckPlan as well as ReadPlans.
const requiredPlanBody = "## Ticket Details\nDetails.\n\n## Implementation Plan\nPlan.\n\n## Architectural Context\nContext.\n\n## Design Context\nN/A.\n"

// writeChainPlan writes a `.plans/<number>-chain.md` file under dir with the
// given front-matter pairs (order-preserving) plus requiredPlanBody, so it
// validates against both ReadPlans (dispatch) and CheckPlan (pipeline).
func writeChainPlan(t *testing.T, dir string, number int, frontMatter [][2]string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("ticketId: " + strconv.Itoa(number) + "\n")
	sb.WriteString("slug: chain\n")
	for _, kv := range frontMatter {
		sb.WriteString(kv[0] + ": " + kv[1] + "\n")
	}
	sb.WriteString("---\n")
	sb.WriteString(requiredPlanBody)

	name := strconv.Itoa(number) + "-chain.md"
	writePlan(t, dir, name, sb.String())
	return filepath.Join(dir, ".plans", name)
}

// -- installChainGH: the PATH-level gh shim ----------------------------------

const (
	chainGhWorldEnv   = "CENCI_CHAIN_GH_WORLD"
	chainGhLocalEnv   = "CENCI_CHAIN_GH_LOCAL_DIR"
	chainGhOriginEnv  = "CENCI_CHAIN_GH_ORIGIN_DIR"
	chainGhRepoEnv    = "CENCI_CHAIN_GH_REPO"
	chainGhShimDirEnv = "CENCI_CHAIN_GH_SHIM_DIR" // #914 security fix #2: lets a re-exec'd child verify gh resolves inside this dir, not a real ambient gh
)

// installChainGH writes a `gh` shim that re-execs the current test binary as
// the helper process (TestChainGhFakeHelper), and PREPENDS its directory to
// PATH -- never replaces PATH wholesale, so the real `git` binary stays
// resolvable (collect_test.go:17-42's installFakeGHOnPath precedent; a
// PATH-replacing shim would silently reclassify every repo as
// MainSyncFailed). The env vars carrying the world path and repo dirs are set
// on the TEST process via t.Setenv; every gh subprocess this harness spawns
// (dispatch's execGh, pipeline's command, babysit's execGh/command) inherits
// them via the default os.Environ()-based Env, all the way down through the
// shell shim's `exec`.
func installChainGH(t *testing.T, worldPath, localDir, originDir, repo string) {
	t.Helper()
	t.Setenv(chainGhWorldEnv, worldPath)
	t.Setenv(chainGhLocalEnv, localDir)
	t.Setenv(chainGhOriginEnv, originDir)
	t.Setenv(chainGhRepoEnv, repo)

	testBin := os.Args[0]
	dir := t.TempDir()
	t.Setenv(chainGhShimDirEnv, dir)
	script := "#!/bin/sh\nexec " + shellQuoteChain(testBin) +
		" -test.run='^TestChainGhFakeHelper$' -test.v=false -- \"$@\"\n"
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// shellQuoteChain wraps s in single quotes for safe embedding in the shim
// script, escaping any embedded single quote the POSIX-sh way.
func shellQuoteChain(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestChainGhFakeHelper is the env-guarded helper-process entry point (#855
// plan): when CENCI_CHAIN_GH_WORLD is unset, this is a normal (no-op) test
// function skipped by every ordinary `go test` run. When invoked as the
// re-exec'd `gh` shim (installChainGH above), it serves exactly one argv,
// mutates the persisted world, writes the response to stdout, and calls
// os.Exit -- never returning to testing.Main, which would otherwise print
// "PASS"/coverage output that would corrupt the JSON stdout a real caller
// (dispatch/pipeline/babysit) expects.
func TestChainGhFakeHelper(t *testing.T) {
	worldPath := os.Getenv(chainGhWorldEnv)
	if worldPath == "" {
		t.Skip("not invoked as the chain gh fake helper")
		return
	}

	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}

	localDir := os.Getenv(chainGhLocalEnv)
	originDir := os.Getenv(chainGhOriginEnv)

	var stdout, stderr string
	var code int
	var block bool
	lockErr := withChainWorldLock(worldPath, func() error {
		world, err := loadGhWorld(worldPath)
		if err != nil {
			return fmt.Errorf("loading world: %w", err)
		}
		world.Invocations = append(world.Invocations, strings.Join(args, " "))

		stdout, stderr, code, block = dispatchChainGhCall(t, world, args, localDir, originDir)

		if err := saveGhWorld(worldPath, world); err != nil {
			return fmt.Errorf("saving world: %w", err)
		}
		return nil
	})
	if lockErr != nil {
		fmt.Fprintf(os.Stderr, "chain gh fake: %v\n", lockErr)
		os.Exit(1)
	}

	if block {
		// "timeout" ambiguity (#914 Q2): block on a real syscall read from an
		// os.Pipe read end whose write end is never closed -- never
		// time.Sleep, so there is no goroutine-asleep panic risk. This
		// deliberately runs AFTER the world lock above was released and
		// saved (Risks: blocking while holding the flock would wedge any
		// concurrent gh call). The parent's own bounded deadline
		// (dispatch's maxOpenPRProbeDuration, overridden to ~100ms by the
		// test) kills this process via exec.CommandContext before this read
		// ever returns, producing a genuine ctx.Err()==DeadlineExceeded ->
		// errGhTimeout classification.
		r, _, err := os.Pipe()
		if err != nil {
			os.Exit(1)
		}
		var buf [1]byte
		_, _ = r.Read(buf[:])
		os.Exit(1)
	}

	if _, err := fmt.Fprint(os.Stdout, stdout); err != nil {
		fmt.Fprintf(os.Stderr, "chain gh fake: writing stdout: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprint(os.Stderr, stderr)
	os.Exit(code)
}

// -- installFakeCenciProcess: recording babysit's `cenci run <workflow>` -----

// installFakeCenciProcess swaps os.Args[0] to a recording shim script for the
// duration of the test (restored via t.Cleanup), so babysit.launch's
// `command(os.Args[0], "run", workflow, arg, "--agent", agent)` self-exec
// invocations are captured instead of trying to run this test binary as a
// real `cenci`. Invocations are appended (one argv per line) to a log file
// under a fresh temp dir; chainCenciInvocations reads it back.
func installFakeCenciProcess(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "cenci-invocations.log")
	script := "#!/bin/sh\necho \"$*\" >> " + shellQuoteChain(logPath) + "\nexit 0\n"
	path := filepath.Join(dir, "cenci-fake")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := os.Args[0]
	os.Args[0] = path
	t.Cleanup(func() { os.Args[0] = orig })
	return logPath
}

// chainCenciInvocations reads back every recorded `cenci run ...` invocation
// from installFakeCenciProcess's log file, one entry per line, oldest first.
func chainCenciInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// -- TestChainReconcileHelper / reExecReconcile: re-exec'd reconcile pass ---

const (
	chainReconcileConfigEnv = "CENCI_CHAIN_RECONCILE_CONFIG"
	chainReconcileStateEnv  = "CENCI_CHAIN_RECONCILE_STATE"
	chainReconcileDryRunEnv = "CENCI_CHAIN_RECONCILE_DRYRUN"
)

// TestChainReconcileHelper is the env-guarded re-exec'd reconcile helper
// (#914 plan): mirrors TestChainGhFakeHelper's skip-when-unset shape
// (chainfake_test.go:514) so an ordinary `go test` run leaves it a no-op.
// When invoked (via reExecReconcile below), it loads dispatch.Config from
// CENCI_CHAIN_RECONCILE_CONFIG, asserts the chain gh fake's PATH shim is
// genuinely inherited (never the ambient real gh -- a lost shim would
// silently defer every verdict or hit real GitHub), runs exactly one
// RunReconcileOnce pass against CENCI_CHAIN_RECONCILE_STATE, emits a small
// JSON summary on stdout, and os.Exit's -- never returning to testing.Main,
// which would otherwise print "PASS"/coverage output into the process's
// stdout/exit-code contract the caller depends on.
func TestChainReconcileHelper(t *testing.T) {
	configPath := os.Getenv(chainReconcileConfigEnv)
	if configPath == "" {
		t.Skip("not invoked as the chain reconcile helper")
		return
	}
	statePath := os.Getenv(chainReconcileStateEnv)
	dryRun := os.Getenv(chainReconcileDryRunEnv) == "true"

	// Child-side assertion (plan Risks: "Fake-gh PATH inheritance in the
	// child"): the chain gh fake's world-path env var and a resolvable `gh`
	// on PATH must both be genuinely inherited, or this pass would silently
	// defer every verdict (nil snapshot) or shell out to a real `gh`.
	if os.Getenv(chainGhWorldEnv) == "" {
		fmt.Fprintln(os.Stderr, "chain reconcile helper: chain gh fake world not inherited (lost PATH shim env)")
		os.Exit(1)
	}
	// Security fix #2: exec.LookPath("gh") != nil alone proves nothing -- it
	// passes just as well for a real ambient /usr/bin/gh. Resolve the exact
	// path and require it live inside installChainGH's own shim dir
	// (chainGhShimDirEnv, set by the SAME t.Setenv call that installed the
	// shim), so a lost/bypassed shim fails loudly instead of silently
	// deferring every verdict or hitting real GitHub.
	shimDir := os.Getenv(chainGhShimDirEnv)
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain reconcile helper: gh not resolvable on PATH: %v\n", err)
		os.Exit(1)
	}
	if shimDir == "" || filepath.Dir(ghPath) != shimDir {
		fmt.Fprintf(os.Stderr, "chain reconcile helper: gh resolved to %q, not the chain gh fake shim dir %q (lost PATH shim / a real ambient gh took precedence)\n", ghPath, shimDir)
		os.Exit(1)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain reconcile helper: loading config %s: %v\n", configPath, err)
		os.Exit(1)
	}

	result, runErr := RunReconcileOnce(cfg, &GHMutator{}, dryRun, os.Stdout, NewStateStore(statePath))
	summary := struct {
		Recoveries int    `json:"recoveries"`
		Failed     int    `json:"failed"`
		Escalated  int    `json:"escalated"`
		Error      string `json:"error,omitempty"`
	}{Recoveries: len(result.Recoveries), Failed: len(result.Failed), Escalated: len(result.Escalated)}
	if runErr != nil {
		// A RunReconcileOnce apply-level error (e.g. an injected ambiguity
		// making `gh issue edit` fail) is an expected, OBSERVABLE outcome
		// this harness exercises, not an infrastructure failure -- report it
		// in the summary rather than a nonzero exit, which reExecReconcile's
		// caller would otherwise treat as this helper itself misbehaving.
		summary.Error = runErr.Error()
	}
	data, _ := json.Marshal(summary)
	if _, err := fmt.Fprintln(os.Stdout, string(data)); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// chainReconcileHelperTimeout/chainReconcileHelperWaitDelay bound
// reExecReconcile's re-exec'd child process (security fix #3), mirroring
// gh.go's ghTimeout/ghWaitDelay conventions (watch/AGENTS.md's subprocess
// rule for internal/dispatch/internal/babysit): if the child blocks on the
// reconcile-state flock -- exactly the risk this test exists to detect --
// it must be killed within a bounded deadline instead of stalling the whole
// `go test` binary until the global test timeout. A genuinely healthy pass
// against this harness's fake gh completes in well under a second, so a few
// seconds of headroom is ample without ever meaningfully slowing the suite.
const (
	chainReconcileHelperTimeout   = 10 * time.Second
	chainReconcileHelperWaitDelay = 2 * time.Second
)

// chainReconcileSummary mirrors TestChainReconcileHelper's emitted JSON
// summary line.
type chainReconcileSummary struct {
	Recoveries int    `json:"recoveries"`
	Failed     int    `json:"failed"`
	Escalated  int    `json:"escalated"`
	Error      string `json:"error,omitempty"`
}

// reExecReconcile spawns TestChainReconcileHelper as a genuine, separate OS
// process (via h.testBin, captured before installFakeCenciProcess mutates
// os.Args[0]) against configPath/statePath, proving RunReconcileOnce's
// crash-safe Load/Save cycle survives a real process boundary (#883's
// torn-write/held-lock risk) rather than only an in-process call.
// os.Environ() is passed through explicitly so the child inherits the gh
// PATH shim, XDG_RUNTIME_DIR (the live snapshot socket), and
// XDG_STATE_HOME.
//
// The FIRST call against a given harness runs a real (non-dry-run) pass, so
// its own apply-mutation attempt can genuinely land or fail against the
// fake gh boundary. Every subsequent call is a dry run: it still re-exercises
// the state file's crash-safe Load path in a fresh process (proving the
// grace-observation/apply-retry-counter state survives a re-exec), but never
// attempts a second mutation that would race the counters the first pass
// already recorded -- mirroring a restart's read-only status probe before
// resuming normal reconciliation.
//
// The child is bounded by chainReconcileHelperTimeout/WaitDelay (security fix
// #3): a hung reconcile-state flock kills the child instead of stalling `go
// test` until the global timeout; ctx.Err() == context.DeadlineExceeded is
// asserted explicitly so a timeout kill is never confused with an ordinary
// nonzero exit. wantApplyError asserts the child's parsed JSON summary.Error
// is non-empty (this call's own seeded ambiguity is expected to make an
// apply attempt fail) or empty (an ordinary pass) -- silent-failure fix #4:
// without this, an UNEXPECTED reconcile error (unrelated to any
// intentionally-seeded ambiguity) would look identical to success, since
// RunReconcileOnce's own error was previously only ever surfaced into
// summary.Error, never asserted on by the caller.
func (h *chainHarness) reExecReconcile(t *testing.T, configPath, statePath string, wantApplyError bool) chainReconcileSummary {
	t.Helper()
	h.reconcileCalls++
	dryRun := "false"
	if h.reconcileCalls > 1 {
		dryRun = "true"
	}

	ctx, cancel := context.WithTimeout(context.Background(), chainReconcileHelperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.testBin, "-test.run=^TestChainReconcileHelper$", "-test.v=false")
	cmd.WaitDelay = chainReconcileHelperWaitDelay
	cmd.Env = append(append([]string{}, os.Environ()...),
		chainReconcileConfigEnv+"="+configPath,
		chainReconcileStateEnv+"="+statePath,
		chainReconcileDryRunEnv+"="+dryRun,
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("reconcile helper: timed out after %s -- possible reconcile-state flock hang (the exact risk this test exists to detect), distinct from an ordinary nonzero exit: %s", chainReconcileHelperTimeout, out)
	}
	if err != nil {
		t.Fatalf("reconcile helper: %v: %s", err, out)
	}

	// RunReconcileOnce's own logf calls write diagnostic lines to os.Stdout
	// ahead of TestChainReconcileHelper's final JSON summary line -- decode
	// only the last line, not the whole combined output.
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	lastLine := lines[len(lines)-1]
	var summary chainReconcileSummary
	if jsonErr := json.Unmarshal([]byte(lastLine), &summary); jsonErr != nil {
		t.Fatalf("reconcile helper: decoding JSON summary from output line %q: %v\nfull output:\n%s", lastLine, jsonErr, out)
	}
	if wantApplyError && summary.Error == "" {
		t.Fatalf("reconcile helper: expected a reconcile apply error from the seeded ambiguity, got none; summary=%+v", summary)
	}
	if !wantApplyError && summary.Error != "" {
		t.Fatalf("reconcile helper: unexpected reconcile error %q (not attributable to a seeded ambiguity)", summary.Error)
	}
	return summary
}

// truncateReconcileState is a byte-level torn-write helper: it truncates
// path (the persisted reconcile state JSON) to half its current length,
// simulating a crash mid-write so a test can assert stateStore.Load's
// StateProbeDecodeError classification against a genuinely malformed file,
// rather than a hand-authored synthetic invalid-JSON fixture.
func truncateReconcileState(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("truncateReconcileState: reading %s: %v", path, err)
	}
	half := len(data) / 2
	if err := os.WriteFile(path, data[:half], 0o600); err != nil {
		t.Fatalf("truncateReconcileState: writing %s: %v", path, err)
	}
}

// TestTruncateReconcileStateProducesDecodeError is a direct, dedicated test
// for truncateReconcileState's own behavior (watch/docs/test-strategy.md:
// an unplanned helper needs direct coverage, not only incidental use
// elsewhere) -- proving the byte-level torn-write helper actually produces
// a genuinely undecodable file, classified StateProbeDecodeError by
// stateStore.Load (mirrors a crash mid-write, #883).
func TestTruncateReconcileStateProducesDecodeError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reconcile.json")
	store := NewStateStore(path)
	if err := store.Save(ReconcileState{
		Observations:  map[string]time.Time{"o/r#1": time.Now()},
		ApplyFailures: map[string]int{"o/r#1": 1},
	}); err != nil {
		t.Fatalf("seeding valid state: %v", err)
	}

	truncateReconcileState(t, path)

	_, err := store.Load()
	if err == nil {
		t.Fatal("expected Load to fail against a torn-write-truncated file")
	}
	var loadErr *StateLoadError
	if !errors.As(err, &loadErr) || loadErr.Probe != StateProbeDecodeError {
		t.Fatalf("Load error = %v, want a StateLoadError with Probe=%q", err, StateProbeDecodeError)
	}
}

// -- serveChainSnapshot: a real daemon-socket double -------------------------

// serveChainSnapshot serves snap over a real ipc broadcast server bound at
// watch.DefaultSocketPath(), redirecting XDG_RUNTIME_DIR to a short,
// freshly-created temp dir first (Unix socket paths are capped around 108
// bytes; os.MkdirTemp("", "cenci") keeps the base short, unlike t.TempDir(),
// which embeds the full test name). This lets dispatch.RunOnce's
// ReadSnapshot dial a real snapshot instead of failing closed on "daemon
// unreachable" (decide.go's Inputs.Snapshot == nil gate) -- the plan's
// deliberate inversion of dispatch_run_test.go's isolateDaemonSocket
// precedent (which asserts the OPPOSITE: no daemon reachable).
func serveChainSnapshot(t *testing.T, snap watch.StateSnapshot) {
	t.Helper()
	runtimeDir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	srv, err := ipc.NewServer(watch.DefaultSocketPath())
	if err != nil {
		t.Fatalf("starting chain snapshot server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})
	go srv.Accept(ctx)

	// Bounded dial-retry (#914 plan: no sleeps anywhere, including this
	// harness's own former 20ms listener wait): ipc.NewServer already binds
	// the listener synchronously (net.Listen, above), and Server.Broadcast
	// caches the snapshot for any client accepted later regardless of
	// ordering (ipc/server.go's lastState), so this dial merely confirms the
	// socket is genuinely connectable before returning -- cheap
	// insurance, never a sleep.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, dialErr := net.Dial("unix", watch.DefaultSocketPath())
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chain snapshot server: socket never became dialable: %v", dialErr)
		}
		runtime.Gosched() // trivial nitpick #914 #10: yield instead of busy-spinning between retries
	}
	srv.Broadcast(snap)
}

// -- runGitChain / squash-merge plumbing (invoked from the helper process) --

// runGitChain runs a git command against dir from within the re-exec'd
// helper process (never from the main test process), returning combined
// output for diagnostics.
func runGitChain(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// validateChainGitRefArg fails the (helper-process) test immediately if ref
// looks like a command-line flag (leading '-'): this fixture splices
// fixture-controlled ref/branch values directly into git argv without a "--"
// separator, so a value like "--upload-pack=evil" would be misparsed as an
// option instead of a literal ref/branch argument. Every current caller
// passes a value entirely under this fixture's own control, so this should
// never fire in practice -- it exists purely so a future caller that ever
// splices in an untrusted value fails loudly instead of silently
// misbehaving.
func validateChainGitRefArg(t *testing.T, ref, label string) {
	t.Helper()
	if strings.HasPrefix(ref, "-") {
		t.Fatalf("chain git fixture: refusing to use %s %q as a git ref/branch argument: leading '-' would be misparsed as a flag", label, ref)
	}
}

// squashMergeOntoOrigin performs a real squash merge of branch (fetched from
// localDir, which already holds it) onto originDir's currently checked-out
// branch (main), producing exactly one new commit -- the fixture's "real
// squash-merge on origin" (#855 plan). Fetching FROM localDir INTO originDir
// only ever updates refs, never originDir's working tree/HEAD, so it is safe
// regardless of receive.denyCurrentBranch.
func squashMergeOntoOrigin(t *testing.T, originDir, localDir, branch string) error {
	t.Helper()
	validateChainGitRefArg(t, branch, "branch")
	const tmpRef = "refs/heads/__chain_pr_merge__"
	if out, err := runGitChain(originDir, "fetch", localDir, branch+":"+tmpRef); err != nil {
		return fmt.Errorf("fetch: %w: %s", err, out)
	}
	if out, err := runGitChain(originDir, "merge", "--squash", tmpRef); err != nil {
		return fmt.Errorf("merge --squash: %w: %s", err, out)
	}
	if out, err := runGitChain(originDir, "commit", "-m", "squash merge"); err != nil {
		return fmt.Errorf("commit: %w: %s", err, out)
	}
	_, _ = runGitChain(originDir, "update-ref", "-d", tmpRef)
	return nil
}

// -- the gh call dispatcher ---------------------------------------------------

// flagValues returns every value immediately following an occurrence of
// --name in args (gh accepts repeatable flags like --add-label/--remove-label).
func flagValues(args []string, name string) []string {
	var out []string
	for i, a := range args {
		if a == name && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// flagValue returns the single value following the first occurrence of
// --name in args, or "" if absent.
func flagValue(args []string, name string) string {
	v := flagValues(args, name)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// graphQLFieldValues parses every "-f key=value" / "-F key=value" pair in
// args into a map, for the graphql call sites (owner/name/number/cursor).
func graphQLFieldValues(args []string) map[string]string {
	out := map[string]string{}
	for i, a := range args {
		if (a == "-f" || a == "-F") && i+1 < len(args) {
			kv := args[i+1]
			if k, v, ok := strings.Cut(kv, "="); ok {
				out[k] = v
			}
		}
	}
	return out
}

// chainRouteKey classifies args into an ambiguity route key -- a coarser
// classification than dispatchChainGhCall's own switch (it does not
// distinguish by flag content), so a scenario's Route string ("issue edit",
// "pr merge", "pr view", "pr checks", "api graphql", ...) matches the call
// it names regardless of exact flag ordering.
func chainRouteKey(args []string) string {
	if len(args) >= 2 {
		switch {
		case args[0] == "issue" && args[1] == "edit":
			return "issue edit"
		case args[0] == "issue" && args[1] == "view":
			return "issue view"
		case args[0] == "issue" && args[1] == "comment":
			return "issue comment"
		case args[0] == "issue" && args[1] == "list":
			return "issue list"
		case args[0] == "label" && args[1] == "create":
			return "label create"
		case args[0] == "pr" && args[1] == "view":
			return "pr view"
		case args[0] == "pr" && args[1] == "checks":
			return "pr checks"
		case args[0] == "pr" && args[1] == "merge":
			return "pr merge"
		case args[0] == "api" && args[1] == "graphql":
			return "api graphql"
		}
	}
	return strings.Join(args, " ")
}

// applyAmbiguity bumps world.RouteCalls' per-route ordinal counter for this
// call and returns the ghAmbiguity (if any) scripted to fire on this
// specific ordinal: OnCall == 0 matches every call on the route
// (unconditional); a positive OnCall matches only that 1-indexed ordinal
// occurrence. A "state-change" kind mutates world immediately here (before
// the call's own normal handler runs), so a later call within the same or a
// subsequent pass observes the mutated state; every other kind is applied
// by dispatchChainGhCall around the normal handler's own result.
func applyAmbiguity(world *ghWorld, args []string) (ghAmbiguity, bool) {
	key := chainRouteKey(args)
	if world.RouteCalls == nil {
		world.RouteCalls = map[string]int{}
	}
	world.RouteCalls[key]++
	ordinal := world.RouteCalls[key]

	for _, a := range world.Ambiguities {
		if a.Route != key {
			continue
		}
		if a.OnCall != 0 && a.OnCall != ordinal {
			continue
		}
		if a.Kind == "state-change" {
			applyStateChangeOp(world, args, a.Payload)
		}
		return a, true
	}
	return ghAmbiguity{}, false
}

// applyStateChangeOp applies one of the closed set of named declarative
// state-change ops a "state-change" ambiguity may request (#914 plan):
// reopen-review, advance-head-sha, disable-fleet-automerge, add-label
// (payload "add-label:<name>"), set-pr-state (payload "set-pr-state:
// <state>"). args[2] is the PR/issue number for every route this fixture
// serves that a state-change op targets.
func applyStateChangeOp(world *ghWorld, args []string, payload string) {
	op, arg, _ := strings.Cut(payload, ":")
	number := 0
	if len(args) >= 3 {
		number, _ = strconv.Atoi(args[2])
	}
	switch op {
	case "reopen-review":
		if number == 0 {
			return
		}
		world.NextReviewID++
		world.Reviews[number] = append(world.Reviews[number], ghReview{
			ID: world.NextReviewID, State: "CHANGES_REQUESTED", Login: "state-change-reviewer",
			SubmittedAt: time.Now().UTC().Format(time.RFC3339),
		})
	case "advance-head-sha":
		if p := world.PRs[number]; p != nil {
			p.HeadRefOID += "-advanced"
		}
	case "disable-fleet-automerge":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			return
		}
		cenciDir := filepath.Join(dir, "cenci")
		if err := os.MkdirAll(cenciDir, 0o755); err != nil {
			return
		}
		_ = os.WriteFile(filepath.Join(cenciDir, "config.json"), []byte(`{"automerge":{"enabled":false}}`), 0o644)
	case "add-label":
		if is := world.Issues[number]; is != nil && arg != "" && !containsStr(is.Labels, arg) {
			is.Labels = append(is.Labels, arg)
		}
	case "set-pr-state":
		if p := world.PRs[number]; p != nil && arg != "" {
			p.State = arg
		}
	}
}

// chainTruncatePaddingBytes exceeds both babysit's ghOutputCap and
// dispatch's maxOpenPRStdoutBytes (each 4 MiB) so a single "truncate"
// ambiguity injection trips either bounded-buffer cap regardless of which
// package's `gh` caller is consuming it. Generated at emit time only inside
// dispatchChainGhCall -- never persisted in the world JSON (#914 Risks).
const chainTruncatePaddingBytes = 5 << 20 // 5 MiB

// dispatchChainGhCall routes one gh invocation's args to its handler,
// mutating world in place and returning (stdout, stderr, exitCode, block).
// block signals the "timeout" ambiguity: the caller (TestChainGhFakeHelper)
// must block on a real syscall read AFTER releasing the world lock and
// saving world, never inside this call. Success paths never write to
// stderr: pipeline.go's own `command` seam uses CombinedOutput, so any stray
// stderr text on a zero-exit call would corrupt the JSON payload it decodes.
func dispatchChainGhCall(t *testing.T, world *ghWorld, args []string, localDir, originDir string) (stdout, stderr string, code int, block bool) {
	if len(args) == 0 {
		return "", "gh: no arguments\n", 1, false
	}
	joined := strings.Join(args, " ")

	amb, matched := applyAmbiguity(world, args)

	// "timeout" and "truncate" short-circuit before the normal handler:
	// neither kind cares about the call's own real response content, and
	// truncate's padding must never be derived from (or leak into) the
	// persisted world.
	if matched && amb.Kind == "timeout" {
		return "", "", 0, true
	}
	if matched && amb.Kind == "truncate" {
		return strings.Repeat("x", chainTruncatePaddingBytes), "", 0, false
	}

	// "indeterminate" (feeds babysit's reasonMergeIndeterminate): only
	// `gh pr merge` currently models this kind -- chainPRMerge below skips
	// its own state mutation but still reports the ordinary success exit.
	indeterminateMerge := matched && amb.Kind == "indeterminate"

	switch {
	case len(args) >= 2 && args[0] == "issue" && args[1] == "list":
		stdout, stderr, code = chainIssueList(world), "", 0

	case len(args) >= 2 && args[0] == "issue" && args[1] == "view":
		stdout, stderr, code = chainIssueView(world, args)

	case len(args) >= 2 && args[0] == "issue" && args[1] == "edit":
		stdout, stderr, code = chainIssueEdit(world, args)

	case len(args) >= 2 && args[0] == "issue" && args[1] == "comment":
		stdout, stderr, code = chainIssueComment(world, args)

	case len(args) >= 2 && args[0] == "label" && args[1] == "create":
		stdout, stderr, code = "", "", 0 // idempotent no-op: label creation always "succeeds"

	case len(args) >= 2 && args[0] == "pr" && args[1] == "view":
		stdout, stderr, code = chainPRView(world, args)

	case len(args) >= 2 && args[0] == "pr" && args[1] == "checks":
		stdout, stderr, code = chainPRChecks(world, args)

	case len(args) >= 2 && args[0] == "pr" && args[1] == "merge":
		stdout, stderr, code = chainPRMerge(t, world, args, localDir, originDir, indeterminateMerge)

	case len(args) >= 2 && args[0] == "repo" && args[1] == "view":
		stdout, stderr, code = `{"nameWithOwner":"`+os.Getenv(chainGhRepoEnv)+`"}`, "", 0

	case args[0] == "api" && flagValue(args, "--jq") == ".login":
		stdout, stderr, code = world.CurrentLogin+"\n", "", 0

	case len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
		stdout, stderr, code = chainGraphQL(world, args)

	case args[0] == "api" && strings.Contains(joined, "/contents/.cenci/config.json"):
		stdout, stderr, code = chainRepoContents(t, args, localDir)

	case args[0] == "api" && strings.Contains(joined, "/issues/") && strings.Contains(joined, "/comments"):
		stdout, stderr, code = chainIssueCommentsAPI(world, args)

	case args[0] == "api" && strings.Contains(joined, "/collaborators/") && strings.HasSuffix(joined, "/permission"):
		stdout, stderr, code = chainCollaboratorPermission(world, args), "", 0

	case args[0] == "api" && strings.Contains(joined, "/pulls/") && strings.Contains(joined, "/comments"):
		stdout, stderr, code = chainPRCommentsAPI(world, args)

	case args[0] == "api" && strings.Contains(joined, "/pulls/") && strings.Contains(joined, "/reviews"):
		stdout, stderr, code = chainPRReviewsAPI(world, args)

	case args[0] == "api" && flagValue(args, "--jq") != "" && strings.HasPrefix(pathArg(args), "repos/"):
		stdout, stderr, code = chainAllowedMethods(world), "", 0

	default:
		return "", "chain gh fake: unrecognized invocation: " + joined + "\n", 1, false
	}

	if matched && amb.Kind == "ambiguous-success" {
		// The server accepted the mutation (already landed above, via the
		// handler's own real mutation of world); the client sees this
		// failure instead -- writing stderr here is fine, this is no longer
		// a zero-exit success path.
		stderr = "chain gh fake: ambiguous success (server accepted the mutation; client sees this failure)\n"
		code = 1
	}

	return stdout, stderr, code, false
}

// pathArg returns the endpoint path argument -- always args[1] for every "api"
// call shape this fixture routes here: fetchPaged's `gh api <path>` and
// automerge's `gh api repos/<repo> --jq <expr>` both place the path
// immediately after "api" with no flag in between. The one call shape that
// puts a flag first (the -H'd .cenci/config.json read) is dispatched straight
// to chainRepoContents, never through pathArg.
func pathArg(args []string) string {
	if len(args) < 2 {
		return ""
	}
	return args[1]
}

func chainIssueList(world *ghWorld) string {
	var nums []int
	for n, is := range world.Issues {
		if is.effectiveState() == "OPEN" {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	var sb strings.Builder
	sb.WriteString("[")
	for i, n := range nums {
		is := world.Issues[n]
		if i > 0 {
			sb.WriteString(",")
		}
		labels, _ := json.Marshal(labelObjs(is.Labels))
		assignees, _ := json.Marshal(assigneeObjs(is.Assignees))
		body, _ := json.Marshal(is.Body)
		title, _ := json.Marshal(is.Title)
		fmt.Fprintf(&sb, `{"number":%d,"title":%s,"body":%s,"labels":%s,"assignees":%s}`,
			is.Number, title, body, labels, assignees)
	}
	sb.WriteString("]")
	return sb.String()
}

func labelObjs(names []string) []map[string]string {
	out := make([]map[string]string, len(names))
	for i, n := range names {
		out[i] = map[string]string{"name": n}
	}
	return out
}

func assigneeObjs(logins []string) []map[string]string {
	out := make([]map[string]string, len(logins))
	for i, l := range logins {
		out[i] = map[string]string{"login": l}
	}
	return out
}

// chainOpenPRPage serves one page of #881's `gh api graphql` open-PR
// inventory query, reusing pageSlice (the same per_page=100 pagination
// helper the REST-paginated call sites in this file already use) so the
// fixture reflects world.PRs' open set across as many pages as needed --
// dispatch.RunOnce's phase E assertion (chain_e2e_test.go, HasOpenPR ==
// true) exercises this through the real openPRInventory call, not a mock of
// it. page is 1-indexed, resolved from the request's "cursor" field
// (graphQLFieldValues) -- this fixture's cursor convention is simply the
// next page number as a decimal string, since every chain test's PR count
// never legitimately requires more than a couple of pages.
func chainOpenPRPage(world *ghWorld, page int) string {
	var nums []int
	for n, pr := range world.PRs {
		if pr.effectiveState() == "OPEN" {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	items := pageSlice(nums, page)
	const chainOpenPRPageSize = 100 // mirrors pageSlice's own pageSize constant
	hasNext := page*chainOpenPRPageSize < len(nums)
	endCursor := "null"
	if hasNext {
		endCursor = strconv.Quote(strconv.Itoa(page + 1))
	}
	totalCount := len(nums)

	// #914 step 2: make hasNextPage/endCursor/totalCount/the nested
	// closingIssuesReferences.pageInfo.hasNextPage fully injectable -- every
	// field a real GitHub GraphQL response could report anomalously without
	// needing to seed thousands of real PRs or a genuinely malformed cursor.
	override := world.OpenPRPageOverride
	nestedHasNext := false
	if override != nil {
		if override.ForceTotalCount != nil {
			totalCount = *override.ForceTotalCount
		}
		if override.ForceHasNextPage != nil {
			hasNext = *override.ForceHasNextPage
		}
		if override.ForceEndCursor != nil {
			endCursor = strconv.Quote(*override.ForceEndCursor)
		}
		if override.ForceNestedHasNextPage != nil {
			nestedHasNext = *override.ForceNestedHasNextPage
		}
	}

	var nodes []string
	for _, n := range items {
		pr := world.PRs[n]
		var closingNodes []string
		for _, ci := range pr.ClosingIssues {
			closingNodes = append(closingNodes, fmt.Sprintf(`{"number":%d}`, ci))
		}
		nodes = append(nodes, fmt.Sprintf(
			`{"number":%d,"closingIssuesReferences":{"pageInfo":{"hasNextPage":%v},"nodes":[%s]}}`,
			n, nestedHasNext, strings.Join(closingNodes, ",")))
	}
	return fmt.Sprintf(
		`{"data":{"repository":{"pullRequests":{"totalCount":%d,"pageInfo":{"hasNextPage":%v,"endCursor":%s},"nodes":[%s]}}}}`,
		totalCount, hasNext, endCursor, strings.Join(nodes, ","))
}

func chainIssueView(world *ghWorld, args []string) (stdout, stderr string, code int) {
	if len(args) < 3 {
		return "", "chain gh fake: unrecognized invocation: " + strings.Join(args, " ") + "\n", 1
	}
	id, err := strconv.Atoi(args[2])
	if err != nil {
		return "", "gh: invalid issue id\n", 1
	}
	fields := strings.Split(flagValue(args, "--json"), ",")
	is := world.Issues[id]
	if is == nil {
		return "", "gh: could not resolve to an Issue\n", 1
	}
	obj := map[string]any{}
	for _, f := range fields {
		switch strings.TrimSpace(f) {
		case "number":
			obj["number"] = is.Number
		case "title":
			obj["title"] = is.Title
		case "state":
			obj["state"] = is.effectiveState()
		case "assignees":
			obj["assignees"] = assigneeObjs(is.Assignees)
		case "labels":
			obj["labels"] = labelObjs(is.Labels)
		case "updatedAt":
			obj["updatedAt"] = is.UpdatedAt
		case "comments":
			var arr []map[string]string
			for _, c := range world.Comments[id] {
				arr = append(arr, map[string]string{"body": c.Body})
			}
			obj["comments"] = arr
		}
	}
	data, _ := json.Marshal(obj)
	return string(data), "", 0
}

// removeChainLabel returns labels with every occurrence of name removed.
func removeChainLabel(labels []string, name string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l != name {
			out = append(out, l)
		}
	}
	return out
}

func chainIssueEdit(world *ghWorld, args []string) (stdout, stderr string, code int) {
	if len(args) < 3 {
		return "", "chain gh fake: unrecognized invocation: " + strings.Join(args, " ") + "\n", 1
	}
	id, err := strconv.Atoi(args[2])
	if err != nil {
		return "", "gh: invalid issue id\n", 1
	}
	is := world.Issues[id]
	if is == nil {
		return "", "gh: could not resolve to an Issue\n", 1
	}
	for _, add := range flagValues(args, "--add-label") {
		if !containsStr(is.Labels, add) {
			is.Labels = append(is.Labels, add)
		}
	}
	for _, rm := range flagValues(args, "--remove-label") {
		is.Labels = removeChainLabel(is.Labels, rm)
	}
	for _, a := range flagValues(args, "--add-assignee") {
		if !containsStr(is.Assignees, a) {
			is.Assignees = append(is.Assignees, a)
		}
	}
	is.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return "", "", 0
}

func chainIssueComment(world *ghWorld, args []string) (string, string, int) {
	if len(args) < 3 {
		return "", "chain gh fake: unrecognized invocation: " + strings.Join(args, " ") + "\n", 1
	}
	id, err := strconv.Atoi(args[2])
	if err != nil {
		return "", "gh: invalid issue id\n", 1
	}
	body := flagValue(args, "--body")
	world.NextCommentID++
	cid := world.NextCommentID
	world.Comments[id] = append(world.Comments[id], ghComment{
		ID: cid, Body: body, Login: "cenci-bot", Type: "Bot",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d\n", os.Getenv(chainGhRepoEnv), id, cid), "", 0
}

func chainPRView(world *ghWorld, args []string) (string, string, int) {
	if len(args) < 3 {
		return "", "chain gh fake: unrecognized invocation: " + strings.Join(args, " ") + "\n", 1
	}
	pr, err := strconv.Atoi(args[2])
	if err != nil {
		return "", "gh: invalid pr id\n", 1
	}
	p := world.PRs[pr]
	if p == nil {
		return "", "gh: could not resolve to a PullRequest\n", 1
	}
	files := make([]map[string]string, len(p.Files))
	for i, f := range p.Files {
		files[i] = map[string]string{"path": f}
	}
	refs := make([]map[string]int, len(p.ClosingIssues))
	for i, ci := range p.ClosingIssues {
		refs[i] = map[string]int{"number": ci}
	}
	obj := map[string]any{
		"number":                  p.Number,
		"title":                   "chain PR",
		"state":                   p.effectiveState(),
		"headRefName":             p.HeadRefName,
		"headRefOid":              p.HeadRefOID,
		"baseRefName":             p.BaseRefName,
		"url":                     p.URL,
		"mergedAt":                nil,
		"closingIssuesReferences": refs,
		"mergeable":               p.Mergeable,
		"isDraft":                 p.IsDraft,
		"changedFiles":            p.ChangedFiles,
		"additions":               p.Additions,
		"deletions":               p.Deletions,
		"files":                   files,
	}
	data, _ := json.Marshal(obj)
	return string(data), "", 0
}

func chainPRChecks(world *ghWorld, args []string) (string, string, int) {
	if len(args) < 3 {
		return "", "chain gh fake: unrecognized invocation: " + strings.Join(args, " ") + "\n", 1
	}
	pr, err := strconv.Atoi(args[2])
	if err != nil {
		return "", "gh: invalid pr id\n", 1
	}
	data, _ := json.Marshal(world.Checks[pr])
	return string(data), "", 0
}

// chainPRMerge fails closed exactly like real GitHub would on a draft PR, a
// PR whose Mergeable state is not MERGEABLE, or a requested merge method
// (--squash/--merge/--rebase) the repo's settings don't allow -- rather than
// unconditionally succeeding regardless of world state. indeterminate
// (#914's "indeterminate" ambiguity kind) skips the merge's own state
// mutation (world stays genuinely un-merged) while still reporting the
// ordinary zero-exit success message -- `gh pr merge` exited zero, but the
// PR never actually flipped to MERGED, feeding babysit's real
// reasonMergeIndeterminate classification.
func chainPRMerge(t *testing.T, world *ghWorld, args []string, localDir, originDir string, indeterminate bool) (string, string, int) {
	t.Helper()
	if len(args) < 3 {
		return "", "chain gh fake: unrecognized invocation: " + strings.Join(args, " ") + "\n", 1
	}
	pr, err := strconv.Atoi(args[2])
	if err != nil {
		return "", "gh: invalid pr id\n", 1
	}
	p := world.PRs[pr]
	if p == nil {
		return "", "gh: could not resolve to a PullRequest\n", 1
	}
	if p.IsDraft {
		return "", "gh: pull request is a draft\n", 1
	}
	if p.Mergeable != "MERGEABLE" {
		return "", "gh: pull request is not mergeable\n", 1
	}
	if containsStr(args, "--squash") && !world.RepoSettings.AllowSquash {
		return "", "gh: squash merges are not allowed for this repository\n", 1
	}
	if containsStr(args, "--merge") && !world.RepoSettings.AllowMerge {
		return "", "gh: merge commits are not allowed for this repository\n", 1
	}
	if containsStr(args, "--rebase") && !world.RepoSettings.AllowRebase {
		return "", "gh: rebase merges are not allowed for this repository\n", 1
	}
	wantSHA := flagValue(args, "--match-head-commit")
	if wantSHA != "" && wantSHA != p.HeadRefOID {
		return "", "gh: head commit does not match --match-head-commit\n", 1
	}
	if indeterminate {
		return fmt.Sprintf("✓ Squash and merged pull request #%d\n", pr), "", 0
	}
	if localDir != "" && originDir != "" && p.HeadRefName != "" {
		if err := squashMergeOntoOrigin(t, originDir, localDir, p.HeadRefName); err != nil {
			return "", fmt.Sprintf("gh: squash merge failed: %v\n", err), 1
		}
	}
	p.State = "MERGED"
	for _, ci := range p.ClosingIssues {
		if is := world.Issues[ci]; is != nil {
			is.State = "CLOSED"
		}
	}
	return fmt.Sprintf("✓ Squash and merged pull request #%d\n", pr), "", 0
}

func chainGraphQL(world *ghWorld, args []string) (string, string, int) {
	// flagValue only returns the FIRST -f value (query=...); re-scan all -f/-F
	// pairs to find "query" specifically, since owner/name are also -f pairs.
	fields := graphQLFieldValues(args)
	query := fields["query"]
	number, _ := strconv.Atoi(fields["number"])

	switch {
	case strings.Contains(query, "isInMergeQueue"):
		qs := world.MergeQueue[number]
		obj := map[string]any{
			"data": map[string]any{
				"repository": map[string]any{
					"pullRequest": map[string]any{
						"isInMergeQueue":      qs.InMergeQueue,
						"isMergeQueueEnabled": qs.Enabled,
						"headRefOid":          "",
					},
				},
			},
		}
		data, _ := json.Marshal(obj)
		return string(data), "", 0

	// #881: dispatch.openPRInventory's bounded, cursor-paginated open-PR
	// query -- distinguished from the isInMergeQueue query above by the
	// presence of the nested closingIssuesReferences field name, which no
	// other graphql query this fixture serves ever requests.
	case strings.Contains(query, "closingIssuesReferences"):
		page := 1
		if c := fields["cursor"]; c != "" {
			if n, err := strconv.Atoi(c); err == nil {
				page = n
			}
		}
		return chainOpenPRPage(world, page), "", 0
	}
	return "", "gh: unrecognized graphql query\n", 1
}

// chainRepoContents serves fetchPolicy's `.cenci/config.json` read by
// shelling out to real git against localDir's committed content -- the same
// committed file dispatch's own probeRepoAutonomy reads via `git show`, so
// both layers see one consistent source of truth.
func chainRepoContents(t *testing.T, args []string, localDir string) (string, string, int) {
	t.Helper()
	endpoint := ""
	for _, a := range args {
		if strings.Contains(a, "/contents/.cenci/config.json") {
			endpoint = a
			break
		}
	}
	ref := "HEAD"
	if idx := strings.Index(endpoint, "?ref="); idx >= 0 {
		if r, err := url.QueryUnescape(endpoint[idx+len("?ref="):]); err == nil && r != "" {
			ref = r
		}
	}
	validateChainGitRefArg(t, ref, "ref")
	out, err := runGitChain(localDir, "show", ref+":.cenci/config.json")
	if err != nil {
		return "", "gh: could not resolve .cenci/config.json at ref " + ref + "\n", 1
	}
	return out, "", 0
}

func chainIssueCommentsAPI(world *ghWorld, args []string) (string, string, int) {
	endpoint := pathArg(args)
	number := extractNumberBetween(endpoint, "/issues/", "/comments")
	comments := world.Comments[number]
	arr := make([]map[string]any, len(comments))
	for i, c := range comments {
		arr[i] = map[string]any{
			"id":                 c.ID,
			"body":               c.Body,
			"user":               map[string]string{"login": c.Login, "type": c.Type},
			"author_association": c.AuthorAssociation,
		}
	}
	data, _ := json.Marshal(arr)
	return string(data), "", 0
}

func chainPRCommentsAPI(world *ghWorld, args []string) (string, string, int) {
	endpoint := pathArg(args)
	number := extractNumberBetween(endpoint, "/pulls/", "/comments")
	page := pageParam(endpoint)
	items := pageSlice(world.PRNotes[number], page)
	arr := make([]map[string]any, len(items))
	for i, c := range items {
		arr[i] = map[string]any{
			"id": c.ID, "body": c.Body,
			"created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
			"user": map[string]string{"login": c.Login},
		}
	}
	data, _ := json.Marshal(arr)
	return string(data), "", 0
}

func chainPRReviewsAPI(world *ghWorld, args []string) (string, string, int) {
	endpoint := pathArg(args)
	number := extractNumberBetween(endpoint, "/pulls/", "/reviews")
	page := pageParam(endpoint)
	items := pageSlice(world.Reviews[number], page)
	arr := make([]map[string]any, len(items))
	for i, r := range items {
		arr[i] = map[string]any{
			"id": r.ID, "state": r.State, "submitted_at": r.SubmittedAt,
			"user": map[string]string{"login": r.Login},
		}
	}
	data, _ := json.Marshal(arr)
	return string(data), "", 0
}

func chainAllowedMethods(world *ghWorld) string {
	obj := map[string]bool{
		"squash": world.RepoSettings.AllowSquash,
		"merge":  world.RepoSettings.AllowMerge,
		"rebase": world.RepoSettings.AllowRebase,
	}
	data, _ := json.Marshal(obj)
	return string(data)
}

// chainCollaboratorPermission serves #882's write-permission probe route:
// `gh api repos/<owner>/<repo>/collaborators/<login>/permission`. A login
// with no seeded world.Permissions entry defaults to "none" (an org member
// or removed collaborator with no repo access, per the plan's Assumptions --
// GitHub returns HTTP 200 permission:"none" here, never a 404).
func chainCollaboratorPermission(world *ghWorld, args []string) string {
	endpoint := pathArg(args)
	login := extractStringBetween(endpoint, "/collaborators/", "/permission")
	permission, ok := world.Permissions[strings.ToLower(login)]
	if !ok {
		permission = "none"
	}
	data, _ := json.Marshal(map[string]string{"permission": permission})
	return string(data)
}

// extractStringBetween pulls the substring between prefix and suffix out of
// s, e.g. ("repos/o/r/collaborators/octocat/permission", "/collaborators/",
// "/permission") -> "octocat". Mirrors extractNumberBetween below, for a
// string segment rather than an integer one.
func extractStringBetween(s, prefix, suffix string) string {
	i := strings.Index(s, prefix)
	if i < 0 {
		return ""
	}
	rest := s[i+len(prefix):]
	j := strings.Index(rest, suffix)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// extractNumberBetween pulls the integer segment between prefix and suffix
// substrings out of s, e.g. ("repos/o/r/issues/101/comments?...", "/issues/", "/comments") -> 101.
// Built on extractStringBetween above: an unmatched prefix/suffix yields "",
// which strconv.Atoi maps to 0, the same not-found sentinel this function
// already returned before #882 introduced extractStringBetween.
func extractNumberBetween(s, prefix, suffix string) int {
	n, _ := strconv.Atoi(extractStringBetween(s, prefix, suffix))
	return n
}

// pageParam extracts the "page=N" query parameter from endpoint, defaulting
// to 1 when absent or malformed.
func pageParam(endpoint string) int {
	// Deliberately searches for "&page=" (not the bare substring "page="):
	// every endpoint this fixture serves is built as
	// "...?per_page=100&page=N" (pagination.go's fetchPaged), and "page=" is
	// ALSO a substring of "per_page=100" -- a bare Index(endpoint, "page=")
	// would match inside "per_page=" first and parse its numeric value
	// instead of the real page parameter.
	if idx := strings.Index(endpoint, "&page="); idx >= 0 {
		rest := endpoint[idx+len("&page="):]
		if amp := strings.IndexAny(rest, "&"); amp >= 0 {
			rest = rest[:amp]
		}
		if n, err := strconv.Atoi(rest); err == nil {
			return n
		}
	}
	return 1
}

// pageSlice returns the feedbackPageSize-sized slice of items for the given
// 1-indexed page. Our fixtures never exceed one page, so every fixture-driven
// call terminates (short page) on page 1.
func pageSlice[T any](items []T, page int) []T {
	const pageSize = 100
	start := (page - 1) * pageSize
	if start >= len(items) {
		return nil
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// seedFromGoldenGraph seeds the parent + every child issue from #912's
// golden fixture graph (readGoldenGraph/loginsOf, chaingolden_test.go) into
// h's world: labels, assignees, body (from h.seedIssue), plus milestone and
// the parent's native sub-issue links (world-model fidelity only, per Q3 --
// no new gh route/consumer). Reuses the fixture's own decoder rather than
// re-decoding the JSON a second time.
func seedFromGoldenGraph(t *testing.T, h *chainHarness, graph goldenGraph) {
	t.Helper()

	h.seedIssue(graph.Parent.Number, graph.Parent.Title, graph.Parent.Body, graph.Parent.Labels, loginsOf(graph.Parent.Assignees))
	parentMilestone := decodeChainGoldenMilestone(t, graph.Parent.Milestone)
	subIssues := make([]ghSubIssue, len(graph.Parent.SubIssues))
	for i, si := range graph.Parent.SubIssues {
		subIssues[i] = ghSubIssue(si)
	}
	h.mutate(func(w *ghWorld) {
		is := w.Issues[graph.Parent.Number]
		is.Milestone = &ghMilestone{Number: parentMilestone.Number, Title: parentMilestone.Title}
		is.SubIssues = subIssues
	})

	for _, c := range graph.Children {
		h.seedIssue(c.Number, c.Title, c.Body, c.Labels, loginsOf(c.Assignees))
		childMilestone := decodeChainGoldenMilestone(t, c.Milestone)
		h.mutate(func(w *ghWorld) {
			w.Issues[c.Number].Milestone = &ghMilestone{Number: childMilestone.Number, Title: childMilestone.Title}
		})
	}
}

// chainAmbiguityScenario names one ambiguity-injection scenario #915's
// negative variants drive via t.Run subtests over one shared harness (AC3:
// one shared ghWorld per scenario group). Kind is the ghAmbiguity.Kind (or,
// for the pagination variants, the injectable ghOpenPRPageOverride facet)
// this scenario exercises.
type chainAmbiguityScenario struct {
	Name string
	Kind string
}

// chainAmbiguityScenarios is the fixed inventory of ambiguity-injection
// scenarios this ticket's capability self-tests prove (TestChainFake_*
// above), bounded by construction: chainScenarioCount pins the exact count
// so the table can never silently grow or shrink without a matching,
// deliberate update (TestChainFake_ScenarioInventoryIsFixed).
var chainAmbiguityScenarios = []chainAmbiguityScenario{
	{Name: "ambiguous-success", Kind: "ambiguous-success"},
	{Name: "indeterminate", Kind: "indeterminate"},
	{Name: "truncate-dispatch-boundary", Kind: "truncate"},
	{Name: "truncate-babysit-boundary", Kind: "truncate"},
	{Name: "timeout", Kind: "timeout"},
	// Trivial nitpick #914 #11: "pagination:"-prefixed, a namespace distinct
	// from ghAmbiguity.Kind's own vocabulary (never passed to
	// applyAmbiguity -- these route through ghOpenPRPageOverride instead), so
	// a future ghAmbiguity{Kind: "page-has-next-page"} can never collide
	// with (and silently no-op against) one of these.
	{Name: "pagination-has-next-page", Kind: "pagination:has-next-page"},
	{Name: "pagination-end-cursor-malformed", Kind: "pagination:end-cursor-malformed"},
	{Name: "pagination-total-count-exceeds-cap", Kind: "pagination:total-count-exceeds-cap"},
	{Name: "pagination-nested-truncation", Kind: "pagination:nested-truncation"},
	{Name: "state-change-between-evaluations", Kind: "state-change"},
}

// chainScenarioCount is the fixed scenario count #916's coverage map cites
// (AC3). Update this deliberately, alongside chainAmbiguityScenarios itself,
// whenever the inventory genuinely changes.
const chainScenarioCount = 10
