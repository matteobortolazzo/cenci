package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/planfile"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// currentGitHubLogin returns the login of the active gh account. Dispatch uses
// this identity instead of Git commit metadata because author name/email are
// not reliable GitHub-account identifiers.
func currentGitHubLogin() (string, error) {
	stdout, stderr, err := execGh("api", "user", "--jq", ".login")
	if err != nil {
		// Detail is truncated to maxProbeLogDetailBytes (#852 review finding
		// #3): a ghTimeout kill mid-stream can leave stdout holding a large
		// partial payload, and this detail is spliced verbatim into an error
		// string that may end up logged.
		return "", fmt.Errorf("gh api user: %w: %s", err, truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes))
	}
	login := strings.TrimSpace(stdout)
	if login == "" {
		return "", fmt.Errorf("gh api user returned an empty login")
	}
	return login, nil
}

// CollectTickets gathers open issues across the configured repos via the gh CLI,
// resolving each ticket's assignees, Agent from an agent:<name> label, and
// HasOpenPR from open PRs' closing-issue references. Best-effort, mirroring
// run's gh usage. A failure on one repo does not block collection from the rest:
// every repo is attempted, and any per-repo failures are joined into the
// returned error so the caller's log names every failing repo, not just the first.
//
// mainSync stamps each collected ticket with its repo's local-main-sync
// outcome (#822, collector-filled, mirrors Stage/StageProbe). A nil map (the
// reconciler's CollectTickets call, which deliberately never syncs) leaves
// every ticket at the ungated zero value (MainSyncSkipped) -- a nil-map
// lookup is safe in Go and needs no special-casing here.
//
// resolveDeps opts this call in or out of building the dependency gate input
// -- both native blocked-by conversion and legacy "Depends on #N"
// parsing/resolution (#825 review fix #1) -- mirroring how mainSync already
// opts in/out of the local-main sync per call, a second, independent axis on
// the same call. RunOnce passes true (dispatch decisions need the gate);
// RunReconcileOnce passes false, since the reconciler never reads
// DependsOn/DependencyStates and would otherwise burn its own
// maxDependencyResolutions gh-call budget on a result it discards. false
// leaves every ticket's DependsOn/DependencyStates at their nil zero value --
// the same "ungated" state a dependency-free issue already gets.
//
// out receives one log line per gh issue view fallback failure and per
// pass-wide dependency-resolution cap hit (#825 review fix #3); callers are
// expected to guarantee a non-nil out (RunOnce/RunReconcileOnce already
// default it to os.Stdout before calling here).
func CollectTickets(repos []RepoConfig, mainSync map[string]MainSync, resolveDeps bool, out io.Writer) ([]Ticket, error) {
	var tickets []Ticket
	var errs []error
	for _, rc := range repos {
		ts, err := collectRepoTickets(rc, mainSync[rc.Repo], resolveDeps, out)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tickets = append(tickets, ts...)
	}
	return tickets, errors.Join(errs...)
}

func collectRepoTickets(rc RepoConfig, sync MainSync, resolveDeps bool, out io.Writer) ([]Ticket, error) {
	repo := rc.Repo
	stdout, stderr, err := execGh("issue", "list",
		"--repo", repo, "--state", "open",
		"--json", "number,title,body,labels,assignees,blockedBy", "--limit", "200")
	if err != nil {
		// Detail is truncated to maxProbeLogDetailBytes (#852 second review
		// round, finding B): `--json number,title,body,labels,assignees,blockedBy
		// --limit 200` can return up to 200 full issue bodies (potentially
		// attacker-authored content on a public repo) in stdout, and a
		// ghTimeout/WaitDelay kill mid-stream can leave that large partial
		// payload spliced into this error string verbatim.
		detail := truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes)
		if isUnknownGhJSONField(stderr) {
			// A gh predating the blockedBy field rejects the whole call, so
			// this surfaces as a hard, named collection failure rather than a
			// silent degradation. Failing the pass is the safe direction: the
			// alternative -- retrying without blockedBy -- would leave every
			// natively-declared blocker invisible to the gate and dispatch
			// blocked tickets as if they were ready.
			return nil, fmt.Errorf("gh issue list %s: %w (requires gh >= %s for native issue dependencies; upgrade gh): %s", repo, err, minGhVersionNativeDeps, detail)
		}
		return nil, fmt.Errorf("gh issue list %s: %w: %s", repo, err, detail)
	}
	var issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		BlockedBy blockedByConnection `json:"blockedBy"`
	}
	if err := json.Unmarshal([]byte(stdout), &issues); err != nil {
		return nil, fmt.Errorf("parsing issues for %s: %w", repo, err)
	}

	// openPR is the bounded, cursor-paginated open-PR inventory probe
	// (#881, openpr.go's openPRInventory) -- replacing the single capped
	// `gh pr list --limit 200` call (openPRIssues, deleted) that silently
	// treated a hit cap as complete. Per Q1, a non-complete probe is logged
	// and gates the affected ticket(s) via decide.go's openPRGateSkip; it
	// is a gate input, not a collection failure, so it never returns an
	// error here (mirrors resolveAnswerProbes/fetchDependencyState,
	// dispatch.go:133-137's rule) -- openPRInventory itself does the
	// bounded logging to out.
	openPR, openPRProbe := openPRInventory(repo, out)

	// openNumbers is the pass's own collected open-issue set for repo,
	// consulted by resolveDependencyStates as the fast path (#825): a number
	// found here is DependencyStateOpen with no gh call. depCache memoizes
	// every gh issue view fallback call across every issue in this one repo
	// call, so N tickets depending on the same out-of-window number cost
	// exactly one gh issue view call, not N. depBudget bounds the total gh
	// issue view fallback calls across this whole pass (#825 review fix #1),
	// created once here -- the same lifetime/scope as depCache. Both are
	// unused (and left nil/zero) when resolveDeps is false.
	var openNumbers map[int]bool
	var depCache map[int]DependencyState
	var depBudget *dependencyResolutionBudget
	if resolveDeps {
		openNumbers = make(map[int]bool, len(issues))
		for _, is := range issues {
			openNumbers[is.Number] = true
		}
		depCache = map[int]DependencyState{}
		depBudget = &dependencyResolutionBudget{}
	}

	tickets := make([]Ticket, 0, len(issues))
	for _, is := range issues {
		labels := make([]string, len(is.Labels))
		for i, l := range is.Labels {
			labels[i] = l.Name
		}
		assignees := make([]string, len(is.Assignees))
		for i, a := range is.Assignees {
			assignees[i] = a.Login
		}
		stage, probe := probeStage(rc.Dir, is.Number)

		// resolveDeps==false (the reconciler's call, #825 review fix #1) skips
		// the whole dependency gate input -- native blocked-by conversion and
		// prose parsing/resolution alike -- for every issue, leaving
		// DependsOn/DependencyStates at their nil zero value. The reconciler
		// never reads these fields, so resolving them would only waste the
		// pass's gh issue view budget on a discarded result. (The native side
		// costs no gh calls, but is skipped too so that resolveDeps==false
		// keeps meaning exactly one thing: no dependency gate input at all.)
		var dependsOn []int
		var depStates map[int]DependencyState
		var depAnomalies []string
		var nativeAnomalies []string
		if resolveDeps {
			nativeNums, nativeStates, nativeAnoms := nativeDependencies(repo, is.BlockedBy)
			nativeAnomalies = nativeAnoms
			proseNums, proseAnoms := parseDependsOn(is.Body)
			depAnomalies = proseAnoms
			dependsOn, depStates = mergeDependencies(nativeNums, nativeStates, proseNums,
				func(nums []int) map[int]DependencyState {
					return resolveDependencyStates(repo, nums, openNumbers, depCache, depBudget, out)
				})
		}

		tickets = append(tickets, Ticket{
			Repo:                repo,
			Number:              is.Number,
			Title:               is.Title,
			Labels:              labels,
			Assignees:           assignees,
			HasOpenPR:           openPR[is.Number],
			Agent:               agentFromLabels(labels),
			Stage:               stage,
			StageProbe:          probe,
			MainSync:            sync,
			DependsOn:           dependsOn,
			DependencyStates:    depStates,
			DependencyAnomalies: depAnomalies,

			NativeDependencyAnomalies: nativeAnomalies,

			OpenPRProbe: openPRProbe,
		})
	}
	return tickets, nil
}

// probeStage classifies dir's persisted `cenci pipeline` stage for one
// ticket into the StageProbe closed set (#732). An empty dir is NEVER
// probed: pipeline.GetArtifacts with both RepoRoot and StateDir empty falls
// back to resolving a repo root from the daemon's own working directory,
// which would read an unrelated repo's state.
func probeStage(dir string, number int) (string, StageProbe) {
	if dir == "" {
		return "", StageProbeAbsent
	}
	s, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: strconv.Itoa(number), RepoRoot: dir})
	if err != nil {
		return "", StageProbeError // unreadable/undecodable → default-deny
	}
	switch {
	case s.Stage == pipeline.StageNew:
		return string(s.Stage), StageProbeAbsent // missing file AND a literal "new" both land here
	case !pipeline.IsKnownStage(s.Stage):
		return string(s.Stage), StageProbeError // e.g. "bogus", or a file with an empty/absent stage field
	default:
		return string(s.Stage), StageProbePresent
	}
}

func agentFromLabels(labels []string) string {
	for _, l := range labels {
		if strings.HasPrefix(l, "agent:") {
			return strings.TrimPrefix(l, "agent:")
		}
	}
	return ""
}

// PlanScan is ReadPlans' full result: the healthy plans matched this pass,
// the per-key PlanProbe classification for every claimed ticket key, and the
// PlanInventory verdict for the whole `.plans` directory read (#884).
type PlanScan struct {
	Plans     []Plan
	Probes    map[string]PlanProbe
	Inventory PlanInventory
}

// ReadPlans enumerates <dir>/.plans via planfile.Read/Select (#884), the
// single shared identity contract also used by pipeline.CheckPlan and
// adoptPlanFileStage: the numeric filename prefix and front-matter ticketId
// must agree for a healthy plan, and two or more claims on one ticket key
// (any mix of healthy/broken) are an ambiguity hold rather than a first-wins
// resolution (AC3/AC4). commitsBehind resolves default-branch commits since
// a plan's sha, restricted to paths when the plan lists stalenessPaths; nil
// uses planfile.CommitsBehind against dir. Callers are expected to guarantee
// a non-nil out, mirroring CollectTickets above (RunOnce/RunReconcileOnce
// already default it to os.Stdout before calling here).
//
// PlanScan.Inventory (#884) distinguishes a proven-complete read
// (PlanInventoryVerified, the zero value) from an unreadable or
// mid-enumeration-partial `.plans` directory: an incomplete read can never
// prove absence for ANY ticket in the repo, so ReadPlans returns a non-nil
// error and an empty Plans/Probes result -- the caller (readPlansForRepos,
// RunReconcileOnce) is responsible for propagating the repo-wide hold (Q5)
// rather than treating the failure as if the repo had simply been skipped.
//
// PlanScan.Probes (#852/#884), keyed by planKey(repo, ticketId), lets a
// caller distinguish a genuinely absent plan (no entry at all -- the
// zero-value PlanProbeAbsent) from one that exists but is broken in some way
// (read error, parse error, path anomaly, identity mismatch, ambiguity,
// staleness error) instead of both collapsing into "no matched Plan": under
// the stage-aware planning-pickup gate (decide.go), plan == nil triggers a
// real dispatch, so a transient read/parse hiccup on an actually-valid plan
// file must never be operationally equivalent to "never planned" (#828
// review fix #2); under the reconciler (reconcile.go), it must never
// escalate to plan-invalid on the same transient hiccup. A broken-but-
// present file is logged to out either way. A filename/front-matter
// identity mismatch (Q2) writes the SAME PlanProbeIDMismatch classification
// under BOTH claimed keys -- the filename's claim and the front matter's
// claim -- so neither ticket silently proceeds as if the other's claim did
// not exist. An unattributable entry (no numeric filename prefix at all --
// Q1) is logged as a bounded anomaly but claims no key at all.
func ReadPlans(repo, dir string, commitsBehind func(sha string, paths []string) (int, error), out io.Writer) (PlanScan, error) {
	// probeStage's dir == "" guard precedent (collect.go:187, this file):
	// never resolve a repo root from the daemon's own working directory.
	// Unlike probeStage (which treats an empty dir as the permissive
	// StageProbeAbsent), an empty repo dir here must fail closed -- a plan
	// inventory read against the wrong directory must never be reported as
	// verified absence (#884 review round 2 finding #3). planfile.Read
	// itself now also guards repoRoot == "" internally (defense-in-depth),
	// but this early return additionally avoids ever constructing a
	// ".plans"-relative-to-cwd path or doing any I/O against it.
	if dir == "" {
		err := fmt.Errorf("read .plans directory for %s: repo dir is empty", repo)
		logf(out, "dispatch: .plans directory for %s (repo dir unset) could not be read: %v\n", repo, err)
		return PlanScan{Inventory: PlanInventoryUnreadable}, err
	}

	if commitsBehind == nil {
		commitsBehind = func(sha string, paths []string) (int, error) {
			return planfile.CommitsBehind(dir, sha, paths)
		}
	}

	inv := planfile.Read(dir)
	if !inv.Complete() {
		classification := PlanInventoryUnreadable
		if inv.State == planfile.DirPartial {
			classification = PlanInventoryPartial
		}
		logf(out, "dispatch: .plans directory for %s (%s) could not be fully read (%s): %v\n", repo, dir, inv.State, inv.Err)
		return PlanScan{Inventory: classification}, fmt.Errorf("read .plans directory for %s: %w", repo, inv.Err)
	}

	// Per-file bounded logging for every entry that is not a clean healthy
	// claim, preserved from the pre-#884 implementation.
	for _, e := range inv.Entries {
		if e.Health != planfile.HealthOK {
			logf(out, "dispatch: plan file %s: %s\n", e.Path, e.Reason)
		}
	}

	// Distinct claimed ticket ids, gathered from every entry's attributable
	// identity -- mirrors planfile.Select's own claim rules (see
	// inventory_test.go's doc comment) so every claimed key gets exactly
	// one Select-derived classification below.
	ids := map[int]bool{}
	for _, e := range inv.Entries {
		switch e.Health {
		case planfile.HealthOK:
			ids[e.TicketID] = true
		case planfile.HealthUnreadable, planfile.HealthMalformed, planfile.HealthPathAnomaly:
			ids[e.FilenameID] = true
		case planfile.HealthIDMismatch:
			if e.FilenameID != 0 {
				ids[e.FilenameID] = true
			}
			if e.FrontMatterID != 0 {
				ids[e.FrontMatterID] = true
			}
		}
	}
	sortedIDs := make([]int, 0, len(ids))
	for id := range ids {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Ints(sortedIDs)

	probes := map[string]PlanProbe{}
	var plans []Plan
	for _, id := range sortedIDs {
		sel := inv.Select(id)
		key := planKey(repo, id)
		switch sel.Result {
		case planfile.SelectSingle:
			p := buildPlan(repo, *sel.Entry)
			if p.PlanCommitSha != "" {
				n, err := commitsBehind(p.PlanCommitSha, p.StalenessPaths)
				if err != nil {
					logf(out, "dispatch: plan file %s: staleness could not be determined: %v\n", p.Path, err)
					probes[key] = PlanProbeStalenessError
					plans = append(plans, p)
					continue
				}
				p.CommitsBehind = n
			}
			probes[key] = PlanProbeOk
			plans = append(plans, p)
		case planfile.SelectAmbiguous:
			probes[key] = PlanProbeAmbiguous
			// Phase 6+7 review finding #5: an ambiguous-but-individually-
			// healthy claim previously never reached the log at all (the
			// per-entry loop above only logs Health != HealthOK), so an
			// operator had no log-derived way to find the colliding files.
			// sel.Reason already names every claiming path (ambiguityReason).
			logf(out, "dispatch: %s\n", sel.Reason)
		case planfile.SelectBroken:
			// Defensive (Phase 6+7 review finding #6): Select's construction
			// never returns SelectBroken with zero Entries against a
			// Complete() inventory today, but this guards a future refactor
			// from turning an index into a panic instead of a bounded
			// classification.
			if len(sel.Entries) == 0 {
				probes[key] = PlanProbeParseError
				continue
			}
			probes[key] = brokenPlanProbeFor(sel.Entries[0].Health)
		}
	}

	return PlanScan{Plans: plans, Probes: probes, Inventory: PlanInventoryVerified}, nil
}

// buildPlan builds a Plan from a healthy (Health == HealthOK) planfile.Entry.
func buildPlan(repo string, e planfile.Entry) Plan {
	fm := e.FrontMatter
	return Plan{
		Repo:                repo,
		Path:                e.Path,
		TicketID:            e.TicketID,
		Status:              fm["status"],
		PlanCommitSha:       fm["planCommitSha"],
		IsChild:             fm["isChild"] == "true",
		IsLastChild:         fm["isLastChild"] == "true",
		ParentID:            planfile.AtoiSafe(fm["parentId"]),
		StalenessPaths:      planfile.SplitPaths(fm["stalenessPaths"]),
		EscalationNonce:     validEscalationNonce(fm["escalationNonce"]),
		EscalationCommentID: validEscalationCommentID(fm["escalationCommentId"]),
	}
}

// brokenPlanProbeFor maps a single claiming entry's Health to the PlanProbe
// classification ReadPlans records for a SelectBroken key (#884).
func brokenPlanProbeFor(h planfile.Health) PlanProbe {
	switch h {
	case planfile.HealthUnreadable:
		return PlanProbeReadError
	case planfile.HealthMalformed:
		return PlanProbeParseError
	case planfile.HealthPathAnomaly:
		return PlanProbePathAnomaly
	case planfile.HealthIDMismatch:
		return PlanProbeIDMismatch
	default:
		// Unreachable given entryClaims' construction (planfile.Select only
		// ever returns SelectBroken for one of the four classes above), but
		// default-denies with the most conservative classification rather
		// than panicking, per watch/docs/error-handling.md's discipline.
		return PlanProbeParseError
	}
}

// validEscalationNonce validates v -- a plan's escalationNonce front-matter
// value -- against escalationNoncePattern (#849). A malformed or absent
// value resolves to "" (absent), never a plan-file drop: ReadPlans is a real
// consumer of the anchor (unlike pipeline.CheckPlan's deliberately
// unvalidated echo), so it must fail closed here rather than trust an
// unverified nonce.
func validEscalationNonce(v string) string {
	if escalationNoncePattern.MatchString(v) {
		return v
	}
	return ""
}

// validEscalationCommentID parses v -- a plan's escalationCommentId
// front-matter value -- as a base-10 int64 (#849). A parse failure or a
// non-positive result resolves to 0 (absent), per the plan's Assumptions:
// "escalationCommentId is parsed with strconv.ParseInt(_, 10, 64); <= 0 or a
// parse error is treated as absent (fails closed)."
func validEscalationCommentID(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// ReadSnapshot dials the daemon broadcast socket, reads one snapshot, and
// closes. Any error yields a nil snapshot so Decide skips safely.
func ReadSnapshot(socketPath string) (*watch.StateSnapshot, error) {
	c, err := watch.Dial(socketPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.ReadSnapshot()
}
