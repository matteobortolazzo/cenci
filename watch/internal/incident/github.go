package incident

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type GitHub struct{ StateDir string }

func (g GitHub) Find(ctx context.Context, r Resource, branch string) (string, error) {
	out, err := command(ctx, "", nil, nil, "gh", "pr", "list", "--repo", r.Repo, "--state", "all", "--head", branch, "--base", r.Base, "--limit", "100", "--json", "url,headRefName,headRepositoryOwner")
	if err != nil {
		return "", err
	}
	var rows []struct {
		URL   string `json:"url"`
		Head  string `json:"headRefName"`
		Owner *struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return "", err
	}
	owner := strings.Split(r.Repo, "/")[0]
	for _, p := range rows {
		if p.Head == branch && p.Owner != nil && strings.EqualFold(p.Owner.Login, owner) {
			if !strings.HasPrefix(p.URL, "https://github.com/"+r.Repo+"/pull/") {
				return "", fmt.Errorf("unexpected incident PR URL")
			}
			return p.URL, nil
		}
	}
	return "", nil
}
func (g GitHub) Draft(ctx context.Context, r Resource, branch, body string) (string, error) {
	dir := filepath.Join(g.StateDir, "pr-bodies")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, Key(r.Repo+branch)+".md")
	if err := durableFile(path, []byte(body)); err != nil {
		return "", err
	}
	out, err := command(ctx, "", nil, nil, "gh", "pr", "create", "--repo", r.Repo, "--base", r.Base, "--head", branch, "--draft", "--title", "fix: investigate incident "+strings.TrimPrefix(branch, "cenci/incident/")[:12], "--body-file", path)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(out, "https://github.com/"+r.Repo+"/pull/") {
		return "", fmt.Errorf("unexpected draft PR response")
	}
	return out, nil
}
