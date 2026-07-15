package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// CollectTickets gathers open issues across the configured repos via the gh CLI,
// resolving each ticket's Agent from an agent:<name> label and HasOpenPR from
// open PRs' closing-issue references. Best-effort, mirroring run's gh usage. A
// failure on one repo does not block collection from the rest: every repo is
// attempted, and any per-repo failures are joined into the returned error so
// the caller's log names every failing repo, not just the first.
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
		"--json", "number,title,labels", "--limit", "200").Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list %s: %w", repo, err)
	}
	var issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
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
		tickets = append(tickets, Ticket{
			Repo:      repo,
			Number:    is.Number,
			Title:     is.Title,
			Labels:    labels,
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
		commitsBehind = func(sha string, paths []string) int { return gitCommitsBehind(dir, sha, paths) }
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
		fm, ok := parseFrontMatter(string(data))
		if !ok {
			continue
		}
		p := Plan{
			Repo:           repo,
			Path:           path,
			TicketID:       atoiSafe(fm["ticketId"]),
			Status:         fm["status"],
			PlanCommitSha:  fm["planCommitSha"],
			IsChild:        fm["isChild"] == "true",
			IsLastChild:    fm["isLastChild"] == "true",
			ParentID:       atoiSafe(fm["parentId"]),
			StalenessPaths: splitPaths(fm["stalenessPaths"]),
		}
		if p.PlanCommitSha != "" {
			p.CommitsBehind = commitsBehind(p.PlanCommitSha, p.StalenessPaths)
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// parseFrontMatter reads the leading `---`-delimited block as flat key: value
// scalars (no yaml dependency). It returns false when no front matter is found.
func parseFrontMatter(content string) (map[string]string, bool) {
	content = strings.TrimPrefix(content, "\ufeff") // drop a leading UTF-8 BOM
	// The opening fence must be a line of its own ("---\n" or "---\r\n"), not a
	// key line that merely starts with dashes.
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return nil, false
	}
	rest := strings.TrimPrefix(content[len("---"):], "\r")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}

	m := make(map[string]string)
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		m[key] = val
	}
	return m, true
}

// atoiSafe parses an int, returning 0 for empty, "null", or malformed values.
func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// splitPaths parses a comma-separated stalenessPaths value into cleaned
// repo-relative paths: entries are trimmed and empties dropped, so trailing
// commas and stray spaces are harmless.
func splitPaths(s string) []string {
	var paths []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// gitCommitsBehind counts default-branch commits since sha. With paths it
// counts only commits touching them (`rev-list -- <paths>`), so unrelated
// monorepo churn cannot mark a scoped plan stale; without paths it keeps the
// whole-repo count.
func gitCommitsBehind(dir, sha string, paths []string) int {
	args := []string{"-C", dir, "rev-list", "--count", sha + "..HEAD"}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return 0
	}
	return atoiSafe(strings.TrimSpace(string(out)))
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
