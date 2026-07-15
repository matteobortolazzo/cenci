package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/run"
)

// RepoIdentity is a resolved (owner/name, absolute dir) pair for a git
// repository, as detected from its "origin" remote.
type RepoIdentity struct {
	Repo string // "owner/name"
	Dir  string // absolute path
}

// RepoEnrollment is the answer to "is this repo enrolled in dispatch, and
// with which dir".
type RepoEnrollment struct {
	Repo     string `json:"repo"`
	Dir      string `json:"dir"`
	Enrolled bool   `json:"enrolled"`
}

// DetectRepoIdentity resolves owner/name from dir's git remote "origin" and
// returns dir as an absolute path.
func DetectRepoIdentity(dir string) (RepoIdentity, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RepoIdentity{}, fmt.Errorf("%s is not a git repository: no .git directory", dir)
		}
		return RepoIdentity{}, fmt.Errorf("checking for .git in %s: %w", dir, err)
	}

	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return RepoIdentity{}, fmt.Errorf("getting origin remote url for %s: %w", dir, err)
	}
	url := strings.TrimSpace(string(out))

	owner, name, err := parseRemoteURL(url)
	if err != nil {
		return RepoIdentity{}, fmt.Errorf("parsing origin remote url %q: %w", redactURL(url), err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return RepoIdentity{}, fmt.Errorf("resolving absolute path for %s: %w", dir, err)
	}

	return RepoIdentity{Repo: owner + "/" + name, Dir: absDir}, nil
}

// parseRemoteURL extracts owner/name from a git remote URL. It dispatches by
// prefix (ssh://, https:// / http://, else scp-style user@host:path), is
// host-agnostic, and tolerates optional user@ creds, .git suffix, and a
// trailing slash. It strictly requires the last two path segments to form
// owner/name.
func parseRemoteURL(url string) (owner, name string, err error) {
	var path string
	switch {
	case strings.HasPrefix(url, "ssh://"), strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "http://"):
		_, rest, _ := strings.Cut(url, "://")
		_, p, ok := strings.Cut(rest, "/")
		if !ok {
			return "", "", fmt.Errorf("no path in url %q", redactURL(url))
		}
		path = p
	default:
		// scp-style: [user@]host:path
		_, p, ok := strings.Cut(url, ":")
		if !ok {
			return "", "", fmt.Errorf("unrecognized remote url %q", redactURL(url))
		}
		path = p
	}

	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", fmt.Errorf("empty path in remote url %q", redactURL(url))
	}

	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return "", "", fmt.Errorf("remote url %q has fewer than two path segments", redactURL(url))
	}
	owner = segments[len(segments)-2]
	name = segments[len(segments)-1]
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("remote url %q has an empty owner or name segment", redactURL(url))
	}
	return owner, name, nil
}

// redactURL masks embedded userinfo (e.g. a token or password in
// "https://user:pass@host/..." or scp-style "user@host:path") before a
// remote URL is included in error text, so credentials never leak into logs.
func redactURL(url string) string {
	at := strings.Index(url, "@")
	if at == -1 {
		return url
	}
	if start := strings.LastIndexAny(url[:at], "/:"); start != -1 {
		return url[:start+1] + "***" + url[at:]
	}
	return "***" + url[at:]
}

// EnrollRepo idempotently adds (or updates the dir of) identity in
// dispatch.repos, preserving every other key verbatim. An empty path
// resolves run.DefaultConfigPath().
func EnrollRepo(path string, identity RepoIdentity) (changed bool, err error) {
	path, err = resolveConfigPath(path)
	if err != nil {
		return false, err
	}

	top, err := readRawConfig(path)
	if err != nil {
		return false, err
	}
	dispatchRaw, repos, err := loadRepos(top)
	if err != nil {
		return false, err
	}

	newRepos := make([]RepoConfig, len(repos))
	copy(newRepos, repos)

	found := false
	for i, r := range newRepos {
		if r.Repo == identity.Repo {
			found = true
			if r.Dir != identity.Dir {
				newRepos[i].Dir = identity.Dir
				changed = true
			}
			break
		}
	}
	if !found {
		newRepos = append(newRepos, RepoConfig(identity))
		changed = true
	}
	if !changed {
		return false, nil
	}

	if err := storeRepos(dispatchRaw, newRepos, top); err != nil {
		return false, err
	}
	if err := writeRawConfig(path, top); err != nil {
		return false, err
	}
	return true, nil
}

// UnenrollRepo idempotently removes repo from dispatch.repos. An empty path
// resolves run.DefaultConfigPath().
func UnenrollRepo(path string, repo string) (changed bool, err error) {
	path, err = resolveConfigPath(path)
	if err != nil {
		return false, err
	}

	top, err := readRawConfig(path)
	if err != nil {
		return false, err
	}
	dispatchRaw, repos, err := loadRepos(top)
	if err != nil {
		return false, err
	}

	newRepos := make([]RepoConfig, 0, len(repos))
	for _, r := range repos {
		if r.Repo == repo {
			changed = true
			continue
		}
		newRepos = append(newRepos, r)
	}
	if !changed {
		return false, nil
	}

	if err := storeRepos(dispatchRaw, newRepos, top); err != nil {
		return false, err
	}
	if err := writeRawConfig(path, top); err != nil {
		return false, err
	}
	return true, nil
}

// QueryEnrollment reports whether repo is enrolled in dispatch.repos and, if
// so, its configured dir. An empty path resolves run.DefaultConfigPath().
func QueryEnrollment(path string, repo string) (RepoEnrollment, error) {
	path, err := resolveConfigPath(path)
	if err != nil {
		return RepoEnrollment{}, err
	}

	top, err := readRawConfig(path)
	if err != nil {
		return RepoEnrollment{}, err
	}
	_, repos, err := loadRepos(top)
	if err != nil {
		return RepoEnrollment{}, err
	}

	for _, r := range repos {
		if r.Repo == repo {
			return RepoEnrollment{Repo: r.Repo, Dir: r.Dir, Enrolled: true}, nil
		}
	}
	return RepoEnrollment{Repo: repo, Enrolled: false}, nil
}

// resolveConfigPath resolves an empty path via run.DefaultConfigPath(),
// erroring out rather than falling back to a bare relative path when no
// default can be resolved either.
func resolveConfigPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	path = run.DefaultConfigPath()
	if path == "" {
		return "", fmt.Errorf("no config path given and no default config path could be resolved")
	}
	return path, nil
}

// readRawConfig reads path into a top-level map[string]json.RawMessage, so
// that keys this package doesn't understand are preserved verbatim on
// write-back. A missing file yields an empty map, not an error.
func readRawConfig(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	top := map[string]json.RawMessage{}
	if len(data) == 0 {
		return top, nil
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return top, nil
}

// writeRawConfig writes top to path atomically: a randomized-name temp file
// is written at 0600 in the same directory then renamed over the final path;
// the randomized name (via os.CreateTemp) prevents a pre-planted symlink at a
// predictable path from being followed. Missing parent directories are
// created at 0700.
func writeRawConfig(path string, top map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config dir %s: %w", dir, err)
	}

	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp config in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing temp config %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp config %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp config %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

// loadRepos decodes the "dispatch" block of top (if present) into a raw map
// so unknown dispatch keys survive round-tripping, plus the typed
// dispatch.repos list.
func loadRepos(top map[string]json.RawMessage) (dispatchRaw map[string]json.RawMessage, repos []RepoConfig, err error) {
	dispatchRaw = map[string]json.RawMessage{}
	if raw, ok := top["dispatch"]; ok {
		if err := json.Unmarshal(raw, &dispatchRaw); err != nil {
			return nil, nil, fmt.Errorf("parsing dispatch block: %w", err)
		}
	}
	if raw, ok := dispatchRaw["repos"]; ok {
		if err := json.Unmarshal(raw, &repos); err != nil {
			return nil, nil, fmt.Errorf("parsing dispatch.repos: %w", err)
		}
	}
	return dispatchRaw, repos, nil
}

// storeRepos re-marshals repos into dispatchRaw["repos"] and dispatchRaw back
// into top["dispatch"], leaving every other key in both maps untouched.
func storeRepos(dispatchRaw map[string]json.RawMessage, repos []RepoConfig, top map[string]json.RawMessage) error {
	reposRaw, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("marshaling repos: %w", err)
	}
	if dispatchRaw == nil {
		dispatchRaw = map[string]json.RawMessage{}
	}
	dispatchRaw["repos"] = reposRaw

	dispatchBlockRaw, err := json.Marshal(dispatchRaw)
	if err != nil {
		return fmt.Errorf("marshaling dispatch block: %w", err)
	}
	top["dispatch"] = dispatchBlockRaw
	return nil
}
