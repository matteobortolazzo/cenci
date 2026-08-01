package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/internal/planfile"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
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
// resolveDeps opts this call in or out of "Depends on #N" parsing/resolution
// (#825 review fix #1), mirroring how mainSync already opts in/out of the
// local-main sync per call -- a second, independent axis on the same call.
// RunOnce passes true (dispatch decisions need the gate); RunReconcileOnce
// passes false, since the reconciler never reads DependsOn/DependencyStates
// and would otherwise burn its own maxDependencyResolutions gh-call budget
// on a result it discards. false leaves every ticket's DependsOn/
// DependencyStates at their nil zero value -- the same "ungated" state a
// dependency-free issue already gets.
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
		"--json", "number,title,body,labels,assignees", "--limit", "200")
	if err != nil {
		// Detail is truncated to maxProbeLogDetailBytes (#852 second review
		// round, finding B): `--json number,title,body,labels,assignees
		// --limit 200` can return up to 200 full issue bodies (potentially
		// attacker-authored content on a public repo) in stdout, and a
		// ghTimeout/WaitDelay kill mid-stream can leave that large partial
		// payload spliced into this error string verbatim.
		return nil, fmt.Errorf("gh issue list %s: %w: %s", repo, err, truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes))
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
		// parseDependsOn/resolveDependencyStates entirely for every issue,
		// leaving DependsOn/DependencyStates at their nil zero value -- the
		// reconciler never reads these fields, so resolving them would only
		// waste the pass's gh issue view budget on a discarded result.
		var dependsOn []int
		var depStates map[int]DependencyState
		var depAnomalies []string
		if resolveDeps {
			nums, anomalies := parseDependsOn(is.Body)
			depAnomalies = anomalies
			if len(nums) > 0 {
				dependsOn = nums
				depStates = resolveDependencyStates(repo, nums, openNumbers, depCache, depBudget, out)
			}
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
			OpenPRProbe:         openPRProbe,
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

// ReadPlans globs <dir>/.plans/*.md, parses each file's flat YAML front matter,
// stamps repo (owner/repo) onto each plan, and fills CommitsBehind. commitsBehind
// resolves default-branch commits since a plan's sha, restricted to paths when
// the plan lists stalenessPaths; nil uses planfile.CommitsBehind against dir.
// Callers are expected to guarantee a non-nil out, mirroring CollectTickets
// above (RunOnce/RunReconcileOnce already default it to os.Stdout before
// calling here).
//
// The returned map[string]PlanProbe (#852), keyed by planKey(repo,
// ticketId), lets a caller distinguish a genuinely absent plan (no entry at
// all -- the zero-value PlanProbeAbsent) from one that exists but is broken
// in some way (read error, parse error, ticket-id error, staleness error)
// instead of both collapsing into "no matched Plan", the exact ambiguity
// this ticket exists to remove: under the stage-aware planning-pickup gate
// (decide.go), plan == nil used to trigger a real dispatch rather than a
// no-op skip, so a transient read/parse hiccup on an actually-valid plan
// file was operationally equivalent to "never planned" and could launch a
// spurious/duplicate planning session with zero trace (#828 review fix #2);
// under the reconciler (reconcile.go), it used to escalate to plan-invalid
// on the very same transient hiccup. A broken-but-present file is logged to
// out either way, and classified into probes when it can be attributed to a
// ticket (planFileTicketID falls back to the filename's numeric prefix when
// front matter can't be trusted); a file whose name doesn't start with
// digits either is logged but left unattributed -- it could never have
// matched a ticket under today's front-matter-only TicketID resolution
// either, so there is no ticket key to gate.
//
// probes is written success-wins-over-error, first-wins-among-equals per key
// (#852 second review round, finding A), so that probes[key] always
// describes whichever plan file actually wins the planByTicket match below
// -- not merely "the first file glob-processed for this key". Those are NOT
// the same "first": plans/planByTicket is first-wins only over files that
// parsed successfully (a probe-errored file is dropped from plans entirely,
// never added), so a plain first-wins-over-everything guard on probes alone
// (the Phase 6+7 review's original fix, finding #1) only closes the
// healthy-first/broken-second ordering. The reverse ordering -- a broken
// file that glob-sorts before its healthy duplicate -- still locked in the
// broken file's error classification even though the healthy file, being
// the first (and only) file added to plans, is exactly the one
// planByTicket/Decide/Reconcile will match. See setProbeFirstWins.
func ReadPlans(repo, dir string, commitsBehind func(sha string, paths []string) (int, error), out io.Writer) ([]Plan, map[string]PlanProbe, error) {
	if commitsBehind == nil {
		commitsBehind = func(sha string, paths []string) (int, error) {
			return planfile.CommitsBehind(dir, sha, paths)
		}
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".plans", "*.md"))
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(matches)

	probes := map[string]PlanProbe{}
	var plans []Plan
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			logf(out, "dispatch: plan file %s unreadable: %v\n", path, err)
			if id, ok := planFileTicketID(path); ok {
				setProbeFirstWins(probes, planKey(repo, id), PlanProbeReadError)
			}
			continue
		}
		fm, ok := planfile.ParseFrontMatter(string(data))
		if !ok {
			logf(out, "dispatch: plan file %s: front matter did not parse\n", path)
			if id, ok := planFileTicketID(path); ok {
				setProbeFirstWins(probes, planKey(repo, id), PlanProbeParseError)
			}
			continue
		}

		ticketID := planfile.AtoiSafe(fm["ticketId"])
		if ticketID <= 0 {
			id, ok := planFileTicketID(path)
			if !ok {
				logf(out, "dispatch: plan file %s: ticket id unresolvable (front matter and filename both unattributable)\n", path)
				continue
			}
			logf(out, "dispatch: plan file %s: ticketId front matter unresolvable, attributed to #%d via filename\n", path, id)
			ticketID = id
			key := planKey(repo, ticketID)
			setProbeFirstWins(probes, key, PlanProbeTicketIDError)
			continue
		}

		p := Plan{
			Repo:                repo,
			Path:                path,
			TicketID:            ticketID,
			Status:              fm["status"],
			PlanCommitSha:       fm["planCommitSha"],
			IsChild:             fm["isChild"] == "true",
			IsLastChild:         fm["isLastChild"] == "true",
			ParentID:            planfile.AtoiSafe(fm["parentId"]),
			StalenessPaths:      planfile.SplitPaths(fm["stalenessPaths"]),
			EscalationNonce:     validEscalationNonce(fm["escalationNonce"]),
			EscalationCommentID: validEscalationCommentID(fm["escalationCommentId"]),
		}
		key := planKey(repo, ticketID)
		if p.PlanCommitSha != "" {
			n, err := commitsBehind(p.PlanCommitSha, p.StalenessPaths)
			if err != nil {
				logf(out, "dispatch: plan file %s: staleness could not be determined: %v\n", path, err)
				setProbeFirstWins(probes, key, PlanProbeStalenessError)
				plans = append(plans, p)
				continue
			}
			p.CommitsBehind = n
		}
		setProbeFirstWins(probes, key, PlanProbeOk)
		plans = append(plans, p)
	}
	return plans, probes, nil
}

// setProbeFirstWins records classification for key in probes, but ONLY a
// plain first-wins guard (matching Reconcile/Decide's planByTicket
// first-wins guard, `if _, ok := planByTicket[key]; !ok { ... }`) is not
// enough (#852 second review round, finding A): plans/planByTicket is
// first-wins only over successfully-parsed files, since a probe-errored
// file is dropped from plans entirely. So the rule here has two parts:
//
//   - A success (PlanProbeOk / PlanProbeStalenessError) always wins over a
//     previously-recorded error (PlanProbeReadError / PlanProbeParseError /
//     PlanProbeTicketIDError) for the same key, regardless of processing
//     order -- a file that parsed fine and was added to plans is, by
//     construction, the one planByTicket will match, so its classification
//     must be the one probes reports even if a broken duplicate for the
//     same ticket happened to glob-sort (and so get probed) first.
//   - Among two classifications of the same kind (two successes, or two
//     errors), the existing first-wins-in-plans order still governs: the
//     first one recorded is kept, and later ones of the same kind are
//     ignored.
//
// In other words: errors are the only classification ever downgraded/
// overwritten, and only by a success -- a success is never overwritten by
// anything.
func setProbeFirstWins(probes map[string]PlanProbe, key string, classification PlanProbe) {
	existing, ok := probes[key]
	if !ok {
		probes[key] = classification
		return
	}
	if isPlanProbeError(existing) && !isPlanProbeError(classification) {
		probes[key] = classification
	}
}

// isPlanProbeError reports whether probe is one of the "broken file" error
// classifications (read/parse/ticket-id errors) as opposed to a success
// classification (Ok/StalenessError) -- used by setProbeFirstWins (#852
// second review round, finding A) to let a success always overwrite a
// previously-recorded error for the same key.
func isPlanProbeError(probe PlanProbe) bool {
	switch probe {
	case PlanProbeReadError, PlanProbeParseError, PlanProbeTicketIDError:
		return true
	default:
		return false
	}
}

// planFileTicketID extracts the leading numeric ticket id from a plan
// file's base name (`.plans/<ticketId>-<slug>.md`), so a probe failure
// (read/parse error) that leaves front matter unavailable can still be
// attributed to the right ticket (#852). Returns (0, false) when the name
// doesn't start with a digit run -- unattributable, since such a file could
// never have matched a ticket under today's front-matter-only TicketID
// resolution either.
func planFileTicketID(path string) (int, bool) {
	base := filepath.Base(path)
	i := 0
	for i < len(base) && base[i] >= '0' && base[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(base[:i])
	if err != nil {
		return 0, false
	}
	return n, true
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
