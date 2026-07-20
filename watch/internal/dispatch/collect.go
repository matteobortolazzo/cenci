package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

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
func CollectTickets(repos []RepoConfig) ([]Ticket, error) {
	var out []Ticket
	var errs []error
	for _, rc := range repos {
		tickets, err := collectRepoTickets(rc.Repo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, tickets...)
	}
	return out, errors.Join(errs...)
}

func collectRepoTickets(repo string) ([]Ticket, error) {
	data, err := exec.Command("gh", "issue", "list",
		"--repo", repo, "--state", "open",
		"--json", "number,title,labels,assignees", "--limit", "200").Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list %s: %w", repo, err)
	}
	var issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
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
		tickets = append(tickets, Ticket{
			Repo:      repo,
			Number:    is.Number,
			Title:     is.Title,
			Labels:    labels,
			Assignees: assignees,
			HasOpenPR: openPR[is.Number],
			Agent:     agentFromLabels(labels),
		})
	}
	return tickets, nil
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
// the plan lists stalenessPaths; nil uses git rev-list against dir. Unparseable
// files are skipped.
func ReadPlans(repo, dir string, commitsBehind func(sha string, paths []string) int) ([]Plan, error) {
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
			continue
		}
		fm, ok := planfile.ParseFrontMatter(string(data))
		if !ok {
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
