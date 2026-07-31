package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	data, err := exec.Command("gh", "api", "user", "--jq", ".login").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh api user: %w: %s", err, strings.TrimSpace(string(data)))
	}
	login := strings.TrimSpace(string(data))
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
	data, err := exec.Command("gh", "issue", "list",
		"--repo", repo, "--state", "open",
		"--json", "number,title,body,labels,assignees", "--limit", "200").Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list %s: %w", repo, err)
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
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("parsing issues for %s: %w", repo, err)
	}

	openPR, err := openPRIssues(repo)
	if err != nil {
		return nil, err
	}

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
		if resolveDeps {
			if nums := parseDependsOn(is.Body); len(nums) > 0 {
				dependsOn = nums
				depStates = resolveDependencyStates(repo, nums, openNumbers, depCache, depBudget, out)
			}
		}

		tickets = append(tickets, Ticket{
			Repo:             repo,
			Number:           is.Number,
			Title:            is.Title,
			Labels:           labels,
			Assignees:        assignees,
			HasOpenPR:        openPR[is.Number],
			Agent:            agentFromLabels(labels),
			Stage:            stage,
			StageProbe:       probe,
			MainSync:         sync,
			DependsOn:        dependsOn,
			DependencyStates: depStates,
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

// openPRIssues returns the set of issue numbers with an open linked PR, via each
// open PR's closingIssuesReferences.
func openPRIssues(repo string) (map[int]bool, error) {
	data, err := exec.Command("gh", "pr", "list",
		"--repo", repo, "--state", "open",
		"--json", "closingIssuesReferences", "--limit", "200").Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list %s: %w", repo, err)
	}
	var prs []struct {
		ClosingIssuesReferences []struct {
			Number int `json:"number"`
		} `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(data, &prs); err != nil {
		return nil, fmt.Errorf("parsing prs for %s: %w", repo, err)
	}
	m := make(map[int]bool)
	for _, pr := range prs {
		for _, ref := range pr.ClosingIssuesReferences {
			m[ref.Number] = true
		}
	}
	return m, nil
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
// the plan lists stalenessPaths; nil uses git rev-list against dir. A file that
// cannot be read or whose front matter cannot be parsed is dropped -- and logged
// to out (#828 review fix #2): under the stage-aware planning-pickup gate,
// plan == nil now triggers a real dispatch rather than a no-op skip, so a
// transient parse hiccup on an actually-valid plan file is operationally
// equivalent to "never planned" and could otherwise launch a spurious/
// duplicate planning session with zero trace. Callers are expected to
// guarantee a non-nil out, mirroring CollectTickets above (RunOnce/
// RunReconcileOnce already default it to os.Stdout before calling here).
func ReadPlans(repo, dir string, commitsBehind func(sha string, paths []string) int, out io.Writer) ([]Plan, error) {
	if commitsBehind == nil {
		// Display-only usage (not decision-gating): degrade gracefully to 0
		// on a git failure rather than propagating it, preserving this
		// package's existing tested behavior (#560 item 1).
		commitsBehind = func(sha string, paths []string) int {
			n, _ := planfile.CommitsBehind(dir, sha, paths)
			return n
		}
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".plans", "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)

	var plans []Plan
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			logf(out, "dispatch: dropping plan file %s: reading: %v\n", path, err)
			continue
		}
		fm, ok := planfile.ParseFrontMatter(string(data))
		if !ok {
			logf(out, "dispatch: dropping plan file %s: front matter did not parse\n", path)
			continue
		}
		p := Plan{
			Repo:           repo,
			Path:           path,
			TicketID:       planfile.AtoiSafe(fm["ticketId"]),
			Status:         fm["status"],
			PlanCommitSha:  fm["planCommitSha"],
			IsChild:        fm["isChild"] == "true",
			IsLastChild:    fm["isLastChild"] == "true",
			ParentID:       planfile.AtoiSafe(fm["parentId"]),
			StalenessPaths: planfile.SplitPaths(fm["stalenessPaths"]),
		}
		if p.PlanCommitSha != "" {
			p.CommitsBehind = commitsBehind(p.PlanCommitSha, p.StalenessPaths)
		}
		plans = append(plans, p)
	}
	return plans, nil
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
