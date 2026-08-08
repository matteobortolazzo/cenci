package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/dispatch"
)

// runAutomerge implements `cenci automerge on|off|status` (#964): the CLI
// writer/reader for the fleet-wide automerge.enabled kill switch in
// ~/.config/cenci/config.json, mirroring `cenci dispatch loop`'s shape. All
// three verbs print the same resolved state; on/off persist the toggle
// first. The repo-policy section is informational — it reads the working
// tree's .cenci/config.json, while the `cenci babysit` supervisor's
// enforcement always reads the PR's base branch — so it answers "what am I
// editing here", not "what will the next merge be judged by".
func runAutomerge(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci automerge: expected a subcommand: on, off, or status")
		os.Exit(2)
	}
	verb := args[0]

	fs := flag.NewFlagSet("automerge "+verb, flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/cenci/config.json)")
	dir := fs.String("dir", ".", "repo directory for the policy summary (default: current directory)")
	jsonOut := fs.Bool("json", false, "print result as JSON")
	_ = fs.Parse(args[1:])

	rejectExtra("cenci automerge", fs.Args())

	switch verb {
	case "on":
		if err := dispatch.SetAutomergeEnabled(*configPath, true); err != nil {
			fmt.Fprintf(os.Stderr, "cenci automerge: %v\n", err)
			os.Exit(1)
		}
	case "off":
		if err := dispatch.SetAutomergeEnabled(*configPath, false); err != nil {
			fmt.Fprintf(os.Stderr, "cenci automerge: %v\n", err)
			os.Exit(1)
		}
	case "status":
		// no mutation; just resolve and print below.
	default:
		fmt.Fprintf(os.Stderr, "cenci automerge: unknown subcommand %q\n", verb)
		os.Exit(2)
	}

	resolvedPath, err := dispatch.ResolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci automerge: %v\n", err)
		os.Exit(1)
	}
	enabled, err := dispatch.QueryAutomergeEnabled(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci automerge: %v\n", err)
		os.Exit(1)
	}

	repo, repoErr := summarizeAutomergeRepo(*dir)

	if *jsonOut {
		out := struct {
			Enabled bool                `json:"enabled"`
			Config  string              `json:"config"`
			Repo    *automergeRepoState `json:"repo,omitempty"`
		}{Enabled: enabled, Config: resolvedPath, Repo: repo}
		if repoErr != nil {
			out.Repo = &automergeRepoState{Root: *dir, Error: repoErr.Error()}
		}
		data, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cenci automerge: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}

	fmt.Print(renderAutomergeState(enabled, resolvedPath, repo, repoErr))
}

// automergeRepoState is the repo-policy half of the automerge status output.
type automergeRepoState struct {
	Root   string                          `json:"root"`
	Error  string                          `json:"error,omitempty"`
	Scopes []dispatch.AutomergePolicyScope `json:"scopes,omitempty"`
}

// summarizeAutomergeRepo resolves dir's repo root (git rev-parse
// --show-toplevel, falling back to dir itself when dir isn't inside a git
// repository) and summarizes its .cenci/config.json automerge policy. A nil
// result with nil error means "no repo policy to report" (no config file
// found) — the status output then stays fleet-only. A non-nil error means a
// config file exists but couldn't be summarized (unreadable/malformed),
// which the caller must surface: babysit denies on exactly that state.
func summarizeAutomergeRepo(dir string) (*automergeRepoState, error) {
	root := dir
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			root = top
		}
	}
	sum, err := dispatch.SummarizeRepoPolicy(root)
	if err != nil {
		return nil, err
	}
	if !sum.ConfigPresent {
		return nil, nil
	}
	return &automergeRepoState{Root: sum.Root, Scopes: sum.Scopes}, nil
}

// renderAutomergeState formats the resolved automerge state as
// human-readable text for `automerge on|off|status`.
func renderAutomergeState(enabled bool, configPath string, repo *automergeRepoState, repoErr error) string {
	var b strings.Builder

	enabledStr := "disabled"
	if enabled {
		enabledStr = "enabled"
	}
	fmt.Fprintf(&b, "Automerge: %s (automerge.enabled in %s)\n", enabledStr, configPath)
	if !enabled {
		fmt.Fprintf(&b, "  enable with: cenci automerge on\n")
	}

	switch {
	case repoErr != nil:
		fmt.Fprintf(&b, "Repo policy: unreadable or malformed — every PR denies until fixed (%v)\n", repoErr)
	case repo != nil:
		fmt.Fprintf(&b, "Repo policy (%s/.cenci/config.json, working tree; enforcement reads the PR's base branch):\n", repo.Root)
		for _, s := range repo.Scopes {
			name := s.Scope
			if name == "" {
				name = "top-level"
			}
			switch {
			case !s.Present:
				fmt.Fprintf(&b, "  %s: absent — denies (policy absent)\n", name)
			case s.Malformed:
				fmt.Fprintf(&b, "  %s: malformed — denies (needs positive maxChangedFiles and maxDiffLines)\n", name)
			default:
				fmt.Fprintf(&b, "  %s: files<=%d lines<=%d method=%s protected=%d\n", name, s.MaxChangedFiles, s.MaxDiffLines, s.MergeMethod, s.ProtectedPaths)
			}
		}
		fmt.Fprintf(&b, "Per-ticket grant: each issue a PR closes additionally needs the automerge:ok label (granted at /cenci:refine).\n")
	}

	return b.String()
}
