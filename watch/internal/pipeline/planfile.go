package pipeline

// CheckPlan (ticket #560): the read-only `cenci pipeline plan-check <id>`
// decision function. Discovers `.plans/<id>-*.md` (0/1/2+ matches ->
// none/validate/multiple), validates the single match's front matter,
// required sections, and slug via internal/planfile's shared parser, then --
// unless --replan-requested short-circuits first, or the front matter
// carries status: awaiting-input (#826: a draft persisted by the unattended
// escalation path, blocked on a human) -- computes a deterministic
// freshness verdict from git commits-behind (planfile.CommitsBehind, scoped
// to the plan's stalenessPaths) and a retry-wrapped `gh issue view
// <id> --json state,updatedAt` (reusing retry.go's command/retryDo/ghRetry
// seams exactly like labels.go). See planfile_test.go's package doc comment
// for the full decision table this implementation satisfies.
//
// CheckPlan never gates on or mutates the persisted pipeline Stage: it only
// echoes whatever GetArtifacts already reports for o.ID, exactly like every
// other read-only mechanics verb.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/planfile"
)

// Sentinel errors, detectable via errors.Is at the package boundary (rule
// #412). Each remains a distinct value with a content-distinct message per
// failure class (rule #446).
var (
	// ErrPlanNotFound is returned when no `.plans/<id>-*.md` file matches.
	ErrPlanNotFound = errors.New("plan file not found")

	// ErrPlanMalformed is returned when the single matching plan file fails
	// front-matter parsing, a required-section scan, or slug validation.
	ErrPlanMalformed = errors.New("plan file malformed")

	// ErrMultiplePlans is returned when two or more files match.
	ErrMultiplePlans = errors.New("multiple plan files found")

	// ErrPlanInventoryUnreadable (#884) is returned when the repo's `.plans`
	// directory itself could not be fully enumerated (unreadable, or a
	// mid-enumeration partial read) -- a proven-incomplete read can never
	// distinguish absence from a hidden second/matching file, so it is
	// routed through the same "" decision as ErrPlanMalformed/the freshness
	// failure (Q3: closed decision set, no new plan-check decision string)
	// rather than reusing ErrPlanNotFound, which would misleadingly claim
	// verified absence.
	ErrPlanInventoryUnreadable = errors.New("plan inventory directory unreadable")

	// ErrPlanIdentityMismatch (#884, Q2) is returned when a plan file's
	// numeric filename prefix and front-matter ticketId disagree (or the
	// front matter carries no ticketId at all, Q4: no legacy exemption) --
	// both the filename claim and the front-matter claim are held with this
	// same failure class.
	ErrPlanIdentityMismatch = errors.New("plan file ticket identity mismatch")
)

// requiredPlanSections are the headings plan-check requires, per the plan's
// Assumptions. "## Design Context" was dropped along with Pencil/design
// removal (flow/skills/implement/phases/phase-1-plan.md's "Persist the
// Plan" step no longer writes or verifies it).
var requiredPlanSections = []string{
	"## Ticket Details",
	"## Implementation Plan",
	"## Architectural Context",
}

// planSlugPattern mirrors flow/skills/implement/SKILL.md's existing slug
// validation pattern (ticketless-mode slug generation).
var planSlugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// planCommitShaPattern constrains planCommitSha before it reaches `git
// rev-list --count <sha>..HEAD`: a value starting with `-` could otherwise be
// parsed as a git option instead of a revision (#560 item 2).
var planCommitShaPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// PlanCheckOpts are the resolved inputs to one `cenci pipeline plan-check
// <id>` invocation. RepoRoot/StateDir mirror pipeline.Opts' own precedence
// (StateDir, when set, locates the echoed persisted State verbatim;
// RepoRoot, or the git repo root resolved from the working directory,
// anchors both the echoed State's canonical path and the `.plans/` glob).
// RepoSlug is the "<owner>/<repo>" gh --repo target, mirroring
// LabelOpts.RepoSlug.
type PlanCheckOpts struct {
	ID              string
	ReplanRequested bool
	RepoRoot        string
	StateDir        string
	RepoSlug        string
}

// PlanCheck is CheckPlan's decision result.
type PlanCheck struct {
	// Decision is one of "resume", "stale", "replan", "awaiting-input",
	// "none", "multiple". "awaiting-input" (#826) means the plan's front
	// matter carries status: awaiting-input -- a draft persisted by the
	// unattended escalation path, blocked on a human answering the open
	// questions on the ticket; it carries a nil error like the other
	// non-"none"/"multiple" decisions.
	Decision string

	// Paths holds every matched .plans/<id>-*.md path: empty for "none",
	// the single match for "resume"/"stale"/"replan"/"awaiting-input",
	// every match for "multiple".
	Paths []string

	// Plan is populated whenever exactly one plan file parsed and
	// validated cleanly: every decision except "none"/"multiple", plus
	// the "" decision's freshness-check-failure sub-case (validation
	// already succeeded before the git/gh freshness check failed). Plan
	// stays nil only for "none", "multiple", and the "" decision's other
	// sub-case (ErrPlanMalformed, where validation itself failed).
	Plan *PlanMeta

	// DraftFreshness (#853) is populated only on the "awaiting-input"
	// decision: "fresh" (zero commits behind planCommitSha, scoped to
	// stalenessPaths), "stale" (one or more), or "unknown" (an empty or
	// malformed planCommitSha, or a CommitsBehind failure -- an
	// unverifiable baseline). See draftFreshness's doc comment for the
	// deliberate divergence from planfile.CommitsBehindRef's sha == "" ⇒
	// fresh convention. Empty string for every other decision.
	DraftFreshness string

	// DraftFreshnessErr (#853 review) carries the underlying
	// planfile.CommitsBehind error's text when DraftFreshness is "unknown"
	// specifically because that call failed (an unreachable sha, a
	// transient git error, a corrupted worktree) -- as opposed to the
	// empty/malformed-sha cases, which never reach CommitsBehind and leave
	// this empty. Mirrors planIsStale's identical class of CommitsBehind
	// error, which is propagated in full via fmt.Errorf's %w rather than
	// discarded (see planIsStale's own doc comment); this field lets
	// pipeline_plan_cmd.go extend its "unknown"-case CLI warning with the
	// same diagnostic detail instead of collapsing every unverifiable
	// baseline into an indistinguishable message. Empty string whenever
	// DraftFreshness is not "unknown", or is "unknown" for a reason other
	// than a CommitsBehind failure.
	DraftFreshnessErr string
}

// PlanMeta is the validated front-matter metadata echoed alongside a
// classification decision. JSON field names mirror the plan front matter's
// own key names (Q&A #1: "echoing validated front-matter metadata (mode,
// slug, ticketId, isChild/isLastChild/parentId) in the JSON output").
//
// EscalationNonce/EscalationCommentID (#849) are echo-only: unlike every
// other field above, they are never validated here -- CheckPlan echoes
// whatever the front matter carries (including a malformed nonce, or a
// comment ID that fails to parse) without promoting it to ErrPlanMalformed,
// so an awaiting-input draft with an incomplete anchor stays repairable
// rather than becoming an unopenable plan file. Each consumer
// (flow/skills/implement/SKILL.md, internal/dispatch) validates and fails
// closed on its own.
//
// EscalationNonce non-empty with EscalationCommentID <= 0 is the *named*
// nonce-without-ID mid-transaction state (#880): the state a crash between
// persisting a fresh replacement nonce and persisting its comment ID leaves
// behind, deliberately reusing this existing shape rather than a new
// front-matter key. It is never treated as corruption by any consumer --
// flow/skills/implement/SKILL.md's awaiting-input Plan Verification names it
// explicitly and routes to `## Repair Escalation Anchor` case (i), and
// internal/dispatch's classifyComments fails closed on it as
// AnswerProbeAnchorUnset. See TestCheckPlan_AwaitingInput_EscalationAnchor_NonceWithoutID_CommentIDZero.
type PlanMeta struct {
	Mode                string `json:"mode"`
	Slug                string `json:"slug"`
	TicketID            int    `json:"ticketId"`
	IsChild             bool   `json:"isChild"`
	IsLastChild         bool   `json:"isLastChild"`
	ParentID            int    `json:"parentId"`
	EscalationNonce     string `json:"escalationNonce"`
	EscalationCommentID int64  `json:"escalationCommentId"`
}

// CheckPlan discovers, validates, and classifies the `.plans/<id>-*.md`
// plan file for o.ID. See this file's package-level doc comment and
// planfile_test.go for the full decision/error-pairing contract.
func CheckPlan(o PlanCheckOpts) (State, PlanCheck, error) {
	if !idPattern.MatchString(o.ID) {
		return State{Stage: StageNew}, PlanCheck{}, fmt.Errorf("invalid ticket id %q: must match ^\\d+$", o.ID)
	}

	// Best-effort, read-only echo of the persisted state -- CheckPlan
	// performs no state-machine transition and must not fail the whole
	// call just because the state file can't be loaded.
	state, _ := GetArtifacts(ArtifactOpts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})

	repoRoot, err := resolvePlanRepoRoot(o.RepoRoot)
	if err != nil {
		return state, PlanCheck{}, err
	}

	// o.ID is already validated against ^\d+$ above, so strconv.Atoi never
	// fails here in practice.
	id, err := strconv.Atoi(o.ID)
	if err != nil {
		return state, PlanCheck{}, fmt.Errorf("invalid ticket id %q: %w", o.ID, err)
	}

	// planfile.Read/Select (#884) is the single shared identity contract
	// also used by dispatch.ReadPlans and adoptPlanFileStage: the numeric
	// filename prefix and front-matter ticketId must agree for a healthy
	// claim, and it never attributes a dot-prefixed candidate file
	// (`.plans/.<id>-<slug>.candidate.md`, #880) to any ticket -- `##
	// Resume From Draft` step 6 assembles the finalized plan there and
	// validates it before an atomic `mv` replaces the real draft, so a
	// candidate sitting alongside a valid awaiting-input draft
	// mid-transaction must never turn a single real match into "multiple"
	// -- see TestCheckPlan_CandidateFileAlongsideDraft_StillSingleMatch_AwaitingInput/
	// _Resume.
	inv := planfile.Read(repoRoot)
	if !inv.Complete() {
		return state, PlanCheck{}, fmt.Errorf("plan inventory for ticket %s: %s could not be fully read: %v: %w", o.ID, inv.Dir, inv.Err, ErrPlanInventoryUnreadable)
	}

	sel := inv.Select(id)
	var path string
	switch sel.Result {
	case planfile.SelectAbsent:
		return state, PlanCheck{Decision: "none"}, fmt.Errorf("no plan file found for ticket %s: %w", o.ID, ErrPlanNotFound)
	case planfile.SelectAmbiguous:
		paths := entryPaths(sel.Entries)
		return state, PlanCheck{Decision: "multiple", Paths: paths}, fmt.Errorf("multiple plan files found for ticket %s: %w", o.ID, ErrMultiplePlans)
	case planfile.SelectBroken:
		// Defensive (Phase 6+7 review finding #6): Select's construction
		// never returns SelectBroken with zero Entries against a Complete()
		// inventory today, but this guards a future refactor from turning an
		// index into a panic instead of a bounded error.
		if len(sel.Entries) == 0 {
			return state, PlanCheck{}, fmt.Errorf("plan inventory for ticket %s: broken selection with no claiming entries: %w", o.ID, ErrPlanInventoryUnreadable)
		}
		e := sel.Entries[0]
		switch e.Health {
		case planfile.HealthIDMismatch:
			return state, PlanCheck{Paths: []string{e.Path}}, fmt.Errorf("plan file %s: %s: %w", e.Path, e.Reason, ErrPlanIdentityMismatch)
		case planfile.HealthMalformed:
			return state, PlanCheck{Paths: []string{e.Path}}, fmt.Errorf("plan file %s: missing or unterminated front matter: %w", e.Path, ErrPlanMalformed)
		default: // HealthUnreadable, HealthPathAnomaly
			return state, PlanCheck{Paths: []string{e.Path}}, fmt.Errorf("plan file %s: %s", e.Path, e.Reason)
		}
	case planfile.SelectSingle:
		path = sel.Entry.Path
	default:
		// Fail-closed default-deny (Phase 6+7 review finding #6), mirroring
		// this file's other closed-set switches: an unrecognized future
		// SelectResult value must never silently fall into the healthy
		// SelectSingle path and dereference a nil sel.Entry.
		return state, PlanCheck{}, fmt.Errorf("plan inventory for ticket %s: unrecognized selection result %q", o.ID, sel.Result)
	}
	matches := []string{path}

	// Consume the content planfile.Read already read once during the
	// inventory scan (sel.Entry.Content, populated for every HealthOK
	// entry -- the only entries planfile.Select ever returns via
	// SelectSingle) instead of re-opening path (Phase 6+7 review finding
	// #3): a second by-path os.ReadFile is a TOCTOU window -- identity was
	// proven against the first read, but a second read follows whatever
	// (possibly a symlink, possibly a mid-transaction replacement, #880)
	// now sits at that path.
	fm, meta, err := parseAndValidatePlan(path, sel.Entry.Content)
	if err != nil {
		return state, PlanCheck{Paths: matches}, err
	}

	if o.ReplanRequested {
		return state, PlanCheck{Decision: "replan", Paths: matches, Plan: meta}, nil
	}

	// #826: a draft persisted by the unattended escalation path
	// (status: awaiting-input) is not implementable yet -- it is blocked on
	// a human answering the open questions on the ticket. Checked after the
	// replan short-circuit (replan is the deliberate discard escape hatch,
	// and must win) but before planIsStale, so a plan still awaiting input
	// never pays for a gh freshness round-trip it can't act on anyway
	// (planIsStale's `gh issue view ... --json state,updatedAt` call). It
	// still pays for a git-only DraftFreshness check (#853, below) -- a
	// resume decision that skips re-exploration must be authorized by a
	// verified freshness baseline, and git carries none of gh's cost/rate
	// concerns.
	if fm["status"] == "awaiting-input" {
		freshness, freshnessErr := draftFreshness(fm, repoRoot)
		check := PlanCheck{Decision: "awaiting-input", Paths: matches, Plan: meta, DraftFreshness: freshness}
		if freshnessErr != nil {
			check.DraftFreshnessErr = freshnessErr.Error()
		}
		return state, check, nil
	}

	stale, err := planIsStale(o, fm, repoRoot, state.TicketUpdatedAt)
	if err != nil {
		return state, PlanCheck{Paths: matches, Plan: meta}, err
	}

	decision := "resume"
	if stale {
		decision = "stale"
	}
	return state, PlanCheck{Decision: decision, Paths: matches, Plan: meta}, nil
}

// entryPaths extracts each entry's Path, in order, for PlanCheck.Paths on
// the "multiple" decision (#884): entries is already sorted by Path
// (planfile.Inventory's own contract).
func entryPaths(entries []planfile.Entry) []string {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	return paths
}

// resolvePlanRepoRoot mirrors resolveStatePath's RepoRoot precedence (Opts'
// StateDir precedence is irrelevant here -- the `.plans/` directory always
// lives under the repo root, never under an overridden state directory).
func resolvePlanRepoRoot(repoRoot string) (string, error) {
	if repoRoot != "" {
		return repoRoot, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	root, err := resolveRepoRoot(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}
	return root, nil
}

// parseAndValidatePlan validates one plan file's content: front matter must
// parse, all requiredPlanSections must be present, and the front matter's
// slug must pass planSlugPattern (rejecting "."/".."/any ".."-containing
// value). Each failure class returns a content-distinct ErrPlanMalformed
// message (rule #446).
func parseAndValidatePlan(path, content string) (map[string]string, *PlanMeta, error) {
	fm, ok := planfile.ParseFrontMatter(content)
	if !ok {
		return nil, nil, fmt.Errorf("plan file %s: missing or unterminated front matter: %w", path, ErrPlanMalformed)
	}

	for _, section := range requiredPlanSections {
		if !strings.Contains(content, section) {
			return nil, nil, fmt.Errorf("plan file %s: missing required section %q: %w", path, section, ErrPlanMalformed)
		}
	}

	slug := fm["slug"]
	if !isValidPlanSlug(slug) {
		return nil, nil, fmt.Errorf("plan file %s: invalid slug %q: %w", path, slug, ErrPlanMalformed)
	}

	// EscalationCommentID (#849) is echoed as a parsed int64, unlike every
	// other PlanMeta field's plain string/bool conversion -- a non-numeric
	// value parses to 0 (never propagated as an error): this echo is
	// deliberately unvalidated (see PlanMeta's doc comment), so a malformed
	// value must never become ErrPlanMalformed.
	commentID, err := strconv.ParseInt(fm["escalationCommentId"], 10, 64)
	if err != nil {
		commentID = 0
	}

	meta := &PlanMeta{
		Mode:                fm["mode"],
		Slug:                slug,
		TicketID:            planfile.AtoiSafe(fm["ticketId"]),
		IsChild:             fm["isChild"] == "true",
		IsLastChild:         fm["isLastChild"] == "true",
		ParentID:            planfile.AtoiSafe(fm["parentId"]),
		EscalationNonce:     fm["escalationNonce"],
		EscalationCommentID: commentID,
	}
	return fm, meta, nil
}

// isValidPlanSlug applies the skill's existing slug pattern plus its
// explicit "."/".."/"..-containing" rejections (the regex alone permits a
// dot-only value, which can traverse a directory when used as a standalone
// path segment).
func isValidPlanSlug(slug string) bool {
	if slug == "" || slug == "." || slug == ".." || strings.Contains(slug, "..") {
		return false
	}
	return planSlugPattern.MatchString(slug)
}

// draftFreshness computes PlanCheck.DraftFreshness for the "awaiting-input"
// decision (#853): a deterministic, git-only staleness check (no gh
// round-trip) over the draft's planCommitSha, scoped to stalenessPaths --
// exactly the same commits-behind primitive planIsStale uses, but skipping
// planIsStale's ticket-state/updatedAt gh comparison entirely, since a
// still-open escalation has no "ticket closed" or "ticket edited" freshness
// signal to check yet.
//
// "unknown" is returned for every unverifiable baseline: an empty
// planCommitSha, a malformed one (fails planCommitShaPattern), or a
// CommitsBehind failure (unreachable sha, transient git error, corrupted
// worktree). This is a deliberate divergence from
// planfile.CommitsBehindRef's sha == "" ⇒ fresh convention (#852 D2): that
// convention exists for a legacy pre-planCommitSha plan file, where
// "no baseline recorded" is absence-by-design and safe to treat as fresh.
// Here the resume decision that DraftFreshness feeds directly controls
// whether Phase 1 skips re-exploration -- an unverifiable baseline must
// never be treated as authorization to skip it, so empty/malformed/
// unreachable all fall to "unknown", which flow/skills/implement's
// consuming skill treats exactly like "stale" (never like "fresh").
//
// The returned error is non-nil only for a genuine CommitsBehind failure
// (the third "unknown" cause above) -- the empty/malformed-sha cases return
// ("unknown", nil), since they never reach CommitsBehind and already have
// their own self-explanatory reason. Callers thread this error into
// PlanCheck.DraftFreshnessErr (review fix, #853) instead of discarding it:
// planIsStale, this function's sibling below, propagates the identical
// class of CommitsBehind error in full rather than collapsing it to a bare
// sentinel, and this keeps that same diagnostic trail available at the CLI
// boundary.
func draftFreshness(fm map[string]string, repoRoot string) (string, error) {
	sha := fm["planCommitSha"]
	if sha == "" || !planCommitShaPattern.MatchString(sha) {
		return "unknown", nil
	}
	paths := planfile.SplitPaths(fm["stalenessPaths"])
	behind, err := planfile.CommitsBehind(repoRoot, sha, paths)
	if err != nil {
		return "unknown", err
	}
	if behind > 0 {
		return "stale", nil
	}
	return "fresh", nil
}

// planIsStale computes the deterministic freshness verdict (Q&A #3/#39):
// stale when ANY of commits-behind (git, scoped to the plan's
// stalenessPaths) > 0, ticket state != OPEN, or ticket updatedAt > the
// plan's createdAt AND > the recorded post-label-edit baseline (#669). No
// judgment-pass fallback.
//
// baseline is the persisted State.TicketUpdatedAt: the ticket's updatedAt
// as observed immediately after the pipeline's own most recent label edit.
// Persisting a plan is always followed by the `planned` label swap, whose
// `gh issue edit` bumps updatedAt past the plan's createdAt — comparing
// against createdAt alone marked every freshly persisted plan stale with
// zero repo churn. An empty baseline (pre-#669 state file, or a state file
// that could not be loaded) falls back to the createdAt-only comparison.
//
// A commits-behind failure (invalid/unreachable planCommitSha, transient git
// error, corrupted worktree) is propagated as a genuine error rather than
// defaulting to "not stale" (#560 item 1): an unverifiable freshness signal
// must never be silently treated as "0 commits behind, resume". This mirrors
// the ticket-freshness failure handling below it (gh error, JSON decode,
// time.Parse), each with a content-distinct message (rule #446).
func planIsStale(o PlanCheckOpts, fm map[string]string, repoRoot, baseline string) (bool, error) {
	sha := fm["planCommitSha"]
	if sha != "" && !planCommitShaPattern.MatchString(sha) {
		return false, fmt.Errorf("plan file for ticket %s: invalid planCommitSha %q: must match %s: %w",
			o.ID, sha, planCommitShaPattern.String(), ErrPlanMalformed)
	}
	paths := planfile.SplitPaths(fm["stalenessPaths"])
	behind, err := planfile.CommitsBehind(repoRoot, sha, paths)
	if err != nil {
		return false, fmt.Errorf("count commits behind for ticket %s: %w", o.ID, err)
	}
	if behind > 0 {
		return true, nil
	}

	args := []string{"issue", "view", o.ID, "--json", "state,updatedAt"}
	if o.RepoSlug != "" {
		args = append(args, "--repo", o.RepoSlug)
	}
	out, err := ghRetry(defaultRetryConfig(), args...)
	if err != nil {
		return false, fmt.Errorf("check ticket freshness for %s: %w", o.ID, err)
	}

	var payload struct {
		State     string `json:"state"`
		UpdatedAt string `json:"updatedAt"`
	}
	if jerr := json.Unmarshal(out, &payload); jerr != nil {
		return false, fmt.Errorf("decode ticket freshness for %s: %w", o.ID, jerr)
	}
	if payload.State != "OPEN" {
		return true, nil
	}

	updatedAt, uerr := time.Parse(time.RFC3339, payload.UpdatedAt)
	if uerr != nil {
		return false, fmt.Errorf("parse ticket updatedAt %q for %s: %w", payload.UpdatedAt, o.ID, uerr)
	}
	createdAt, cerr := time.Parse(time.RFC3339, fm["createdAt"])
	if cerr != nil {
		return false, fmt.Errorf("parse plan createdAt %q for %s: %w", fm["createdAt"], o.ID, cerr)
	}

	threshold := createdAt
	if baseline != "" {
		b, berr := time.Parse(time.RFC3339, baseline)
		if berr != nil {
			return false, fmt.Errorf("parse recorded ticketUpdatedAt baseline %q for %s: %w", baseline, o.ID, berr)
		}
		if b.After(threshold) {
			threshold = b
		}
	}
	return updatedAt.After(threshold), nil
}
