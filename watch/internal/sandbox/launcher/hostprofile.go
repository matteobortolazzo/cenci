package launcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

const hostContentRoot = "/opt/cenci-host-content"

type hostContentConfig struct {
	SkillDirectories []string `json:"skillDirectories"`
	InstructionFile  string   `json:"instructionFile"`
}

// Policy input and resolved content are kept together so create, reuse and
// audit cannot disagree about which host files the repository is authorized for.
type hostProfile struct {
	Repositories    []string          `json:"repositories"`
	Content         hostContentConfig `json:"content"`
	Preferences     json.RawMessage   `json:"preferences"`
	Version         json.RawMessage   `json:"version"`
	Plugins         json.RawMessage   `json:"plugins"`
	Authorized      bool              `json:"-"`
	Mounts          []MountPosture    `json:"-"`
	ContentIdentity string            `json:"-"`
	configPath      string
}

func resolveHostProfile(agent string, scope Scope, home string) (hostProfile, error) {
	var profile hostProfile
	if agent != "claude" && agent != "codex" {
		return profile, nil
	}
	path := run.DefaultConfigPath()
	if path == "" {
		return profile, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return profile, nil
	}
	if err != nil {
		return profile, err
	}
	if !info.Mode().IsRegular() {
		return profile, fmt.Errorf("host config %s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return profile, err
	}
	var config struct {
		Profiles map[string]json.RawMessage `json:"sandboxHostProfiles"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return profile, fmt.Errorf("%s: %w", path, err)
	}
	raw, ok := config.Profiles[agent]
	if !ok {
		return profile, nil
	}
	if err := checkHostProfilePath(path, home, scope); err != nil {
		return profile, err
	}
	if err := validatePersonalFile(path, info); err != nil {
		return profile, err
	}
	if len(raw) == 0 || raw[0] != '{' {
		return profile, fmt.Errorf("%s: sandboxHostProfiles.%s must be an object", path, agent)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return hostProfile{}, fmt.Errorf("%s: sandboxHostProfiles.%s: %w", path, agent, err)
	}
	profile.configPath = path
	for _, repo := range profile.Repositories {
		if !filepath.IsAbs(repo) || filepath.Clean(repo) != repo || !addDirPathIsPlain(repo) {
			return hostProfile{}, fmt.Errorf("%s: invalid repository scope %q; use a canonical absolute repository root", path, repo)
		}
	}
	if scope.WorkspaceScope != "repo" || !slices.Contains(profile.Repositories, scope.RepoRoot) {
		return profile, nil
	}
	// The repo may decline authorization, but can never select a path or grant
	// itself a repository scope.
	repoConfig, _, err := readRepoSandboxObject(scope.RepoRoot)
	if err != nil {
		return hostProfile{}, err
	}
	if raw, ok := repoConfig["hostProfile"]; ok {
		switch string(raw) {
		case "false":
			return profile, nil
		case "true":
		default:
			return hostProfile{}, fmt.Errorf("%s: sandbox.hostProfile must be true or false", repoConfigPath(scope.RepoRoot))
		}
	}
	profile.Authorized = true
	err = profile.resolveContent(agent, scope, home)
	return profile, err
}

// Only canonical paths under the host home may authorize or supply content.
// Refuse checkout-resident or group/other-writable paths, including their
// ancestors: a repository-selected XDG path must never become a trust root.
func checkHostProfilePath(path, home string, scope Scope) error {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || !filepath.IsAbs(path) || filepath.Clean(path) != path || !addDirPathIsPlain(path) || strings.ContainsAny(path, ",\"") || path == home || !pathContains(home, path) {
		return fmt.Errorf("host profile path %q must be an absolute canonical path under %q without mount delimiters", path, home)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("host profile %q: %w", path, err)
	}
	// macOS /var may itself be a link. Compare below the canonical home.
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return err
	}
	if real != filepath.Join(realHome, strings.TrimPrefix(path, home+string(filepath.Separator))) {
		return fmt.Errorf("host profile %q uses a symlink outside its canonical location", path)
	}
	if scope.WorkspaceBindHost != "" && identityContains(scope.WorkspaceBindHost, real) {
		return fmt.Errorf("host profile path %q is inside the writable workspace", path)
	}
	for p := real; ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		// System temp directories may contain a private home in tests or
		// ephemeral environments. Sticky ownership protects its child name.
		stickyAncestor := !pathContains(realHome, p) && info.Mode()&os.ModeSticky != 0
		if int(st.Uid) != os.Getuid() && st.Uid != 0 || info.Mode().Perm()&0022 != 0 && !stickyAncestor {
			return fmt.Errorf("host profile %q must be owned by the current user or root and writable only by its owner", p)
		}
		if info.IsDir() {
			_, err := os.Lstat(filepath.Join(p, ".git"))
			if err == nil {
				return fmt.Errorf("host profile path %q is inside a repository at %q", path, p)
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}

func (p *hostProfile) resolveContent(agent string, scope Scope, home string) error {
	var mounts []MountPosture
	for _, skill := range p.Content.SkillDirectories {
		if err := checkHostProfilePath(skill, home, scope); err != nil {
			return err
		}
		name := filepath.Base(skill)
		if filepath.Base(filepath.Dir(skill)) != "skills" || strings.HasPrefix(name, ".") {
			return fmt.Errorf("host skill %q must name an individual visible directory under skills/", skill)
		}
		if err := validateSkillDirectory(skill); err != nil {
			return err
		}
		mounts = append(mounts, MountPosture{Source: skill, Destination: hostContentRoot + "/skills/" + name, ReadOnly: true, Kind: MountKindHostContent})
	}
	if src := p.Content.InstructionFile; src != "" {
		if err := checkHostProfilePath(src, home, scope); err != nil {
			return err
		}
		name := filepath.Base(src)
		if agent == "claude" && name != "CLAUDE.md" || agent == "codex" && name != "AGENTS.md" && name != "AGENTS.override.md" {
			return fmt.Errorf("unsupported %s host instruction filename %q", agent, name)
		}
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		if err := validatePersonalFile(src, info); err != nil {
			return err
		}
		mounts = append(mounts, MountPosture{Source: src, Destination: hostContentRoot + "/instructions/" + name, ReadOnly: true, Kind: MountKindHostContent})
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Destination < mounts[j].Destination })
	hash := sha256.New()
	for i, m := range mounts {
		if i > 0 && mounts[i-1].Destination == m.Destination {
			return fmt.Errorf("host content collision at %s; select only one source with that name", m.Destination)
		}
		info, err := os.Stat(m.Source)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		// A bind follows its inode. Atomic replacement of an instruction file
		// leaves a running mount attached to the old file despite an identical path.
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d:%d\n", m.Source, m.Destination, st.Dev, st.Ino)
	}
	if len(mounts) > 0 {
		p.ContentIdentity = fmt.Sprintf("%x", hash.Sum(nil))
	}
	p.Mounts = mounts
	return nil
}

func validatePersonalFile(path string, info fs.FileInfo) error {
	st := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || st.Nlink != 1 || info.Mode().Perm()&0444 == 0 || info.Mode().Perm()&0022 != 0 || int(st.Uid) != os.Getuid() && st.Uid != 0 {
		return fmt.Errorf("host content %q must be a readable, singly linked regular file writable only by its owner", path)
	}
	return nil
}

func validateSkillDirectory(root string) error {
	if _, err := os.Stat(filepath.Join(root, "SKILL.md")); err != nil {
		return fmt.Errorf("host skill %q needs SKILL.md: %w", root, err)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(root, "SKILL.md") {
				return fmt.Errorf("host skill %q: SKILL.md must be a regular file", root)
			}
			st := info.Sys().(*syscall.Stat_t)
			if int(st.Uid) != os.Getuid() && st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
				return fmt.Errorf("unsafe owner or directory permissions at %q", path)
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || filepath.IsAbs(target) || !pathContains(root, filepath.Join(filepath.Dir(path), target)) || !pathContains(root, resolved) {
				return fmt.Errorf("host skill %q: broken, absolute or escaping symlink %q", root, path)
			}
			// Relative links to in-tree regular files are portable inside the bind.
			info, err = os.Stat(path)
			if err != nil {
				return err
			}
		}
		return validatePersonalFile(path, info)
	})
}

func sameHostContent(expected, actual []MountPosture) bool {
	seen := make(map[MountPosture]bool, len(expected))
	for _, m := range expected {
		seen[m] = true
	}
	for _, m := range actual {
		if m.Kind != MountKindHostContent {
			continue
		}
		if !seen[m] {
			return false
		}
		delete(seen, m)
	}
	return len(seen) == 0
}
