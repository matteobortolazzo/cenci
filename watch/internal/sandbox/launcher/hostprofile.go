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
	versionConfigPath string

	Repositories    []string          `json:"repositories"`
	Content         hostContentConfig `json:"content"`
	Preferences     json.RawMessage   `json:"preferences"`
	Version         json.RawMessage   `json:"version"`
	Plugins         json.RawMessage   `json:"plugins"`
	Authorized      bool              `json:"-"`
	Mounts          []MountPosture    `json:"-"`
	ContentIdentity string            `json:"-"`
	configPath      string
	preferences     hostPreferences
}

func resolveHostProfile(agent string, scope Scope, home string) (hostProfile, error) {
	path := run.DefaultConfigPath()
	profile, err := readHostProfile(agent, scope, home, path)
	if err != nil || (agent != "claude" && agent != "codex") {
		return profile, err
	}
	accountHome, err := versionAccountHome()
	if err != nil {
		return hostProfile{}, err
	}
	anchor := filepath.Join(accountHome, ".config", "cenci", "config.json")
	if filepath.Clean(path) != anchor {
		if len(profile.Version) > 0 {
			return hostProfile{}, fmt.Errorf("configure the agent version policy in the OS account's %s; HOME and XDG_CONFIG_HOME cannot select a host executable", anchor)
		}
		// The store is shared across repos and across XDG environments. An
		// alternate config cannot authorize host execution or disable the
		// account's policy and start a competing managed latest update.
		global, err := readHostProfile(agent, Scope{}, accountHome, anchor)
		if err != nil {
			return hostProfile{}, err
		}
		profile.Version = global.Version
	}
	profile.versionConfigPath = anchor
	policy, err := profile.versionPolicy()
	if err != nil {
		return hostProfile{}, err
	}
	if policy.Mode == "follow-host" && scope.WorkspaceBindHost != "" {
		if err := policy.rejectWritableBind(scope.WorkspaceBindHost, "workspace"); err != nil {
			return hostProfile{}, err
		}
	}
	return profile, nil
}

func readHostProfile(agent string, scope Scope, home, path string) (hostProfile, error) {
	var profile hostProfile
	if agent != "claude" && agent != "codex" {
		return profile, nil
	}
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
	// Only Claude Code and Codex have a host profile. Refusing the other keys on
	// every resolution keeps a profile for a paused agent from sitting unread.
	var unsupported []string
	for key := range config.Profiles {
		if key != "claude" && key != "codex" {
			unsupported = append(unsupported, key)
		}
	}
	if len(unsupported) > 0 {
		sort.Strings(unsupported)
		// %q keeps a control character in a JSON key from reaching the terminal.
		return profile, fmt.Errorf("%s: sandboxHostProfiles[%q] is not supported; only sandboxHostProfiles.claude and sandboxHostProfiles.codex exist", path, unsupported[0])
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
	if len(profile.Plugins) > 0 && string(profile.Plugins) != "null" {
		return hostProfile{}, fmt.Errorf("%s: sandboxHostProfiles.%s.plugins is not yet supported; remove it", path, agent)
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
	if err == nil {
		profile.preferences, err = resolvePreferences(profile.Preferences, agent, scope, home)
	}
	return profile, err
}

// hostContentOwnerTrusted reports whether an entry owned by uid may supply host
// content: the invoking user or root. It is a variable only so a test can mark
// one path as foreign-owned without being root; production never reassigns it.
var hostContentOwnerTrusted = func(_ string, uid uint32) bool {
	return int(uid) == os.Getuid() || uid == 0
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
		if !hostContentOwnerTrusted(p, st.Uid) || info.Mode().Perm()&0022 != 0 && !stickyAncestor {
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

// hostProfileGuards is the sensitive-location denylist for host-profile sources:
// addDirSensitiveGuards with one change. Personal skills and instructions live
// under ~/.claude and ~/.codex, so each agent home is refused whole (a source
// equal to it or to a parent of it) instead of with everything inside, and only
// its credential file is refused by overlap. $CODEX_HOME gets the same treatment
// as ~/.codex, a relative value resolved against the working directory as
// --add-dir does.
func hostProfileGuards(home string) ([]addDirGuard, error) {
	guards, err := addDirSensitiveGuards(home)
	if err != nil {
		return nil, hostProfileRuntimeError(err)
	}
	type agentHome struct{ dir, credentials string }
	var homes []agentHome
	// superseded are the credential guards addDirHomeGuards builds for the Claude
	// and Codex homes. Only those are replaced by the whole-home guard: another
	// guard that merely has the same path ($PATH, the fleet config, a socket
	// directory) keeps its own reason, so it is matched by reason as well.
	var superseded []addDirGuard
	// addDirSensitiveGuards builds the per-home guards for /root as well only
	// when $HOME is strictly inside it.
	userHomes := []string{home}
	if filepath.Clean(home) != addDirRootHome && pathContains(addDirRootHome, home) {
		userHomes = append(userHomes, addDirRootHome)
	}
	for _, h := range userHomes {
		for _, spec := range credentialSourceSpecs {
			if spec.typ == CredentialTypeClaude || spec.typ == CredentialTypeCodex {
				credentials := spec.pathFrom(h)
				dir := filepath.Dir(credentials)
				homes = append(homes, agentHome{dir: dir, credentials: credentials})
				superseded = append(superseded, addDirGuard{path: dir, mode: forbidOverlap, why: "it holds " + spec.typ + " credentials"})
			}
		}
	}
	codexHome, err := addDirEnvPath("CODEX_HOME")
	if err != nil {
		return nil, hostProfileRuntimeError(err)
	}
	if codexHome != "" {
		homes = append(homes, agentHome{dir: codexHome, credentials: filepath.Join(codexHome, "auth.json")})
	}
	guards = slices.DeleteFunc(guards, func(g addDirGuard) bool {
		return slices.ContainsFunc(superseded, func(s addDirGuard) bool {
			return s.path == g.path && s.mode == g.mode && s.why == g.why
		})
	})
	for _, h := range homes {
		guards = append(guards,
			addDirGuard{path: h.dir, mode: forbidCovering, why: "the agent home " + h.dir + " holds credentials, so it or a parent of it cannot be a source; select a directory inside it"},
			addDirGuard{path: h.credentials, mode: forbidOverlap, why: "it holds agent credentials"},
		)
	}
	return guards, nil
}

// hostProfileRuntimeError rewords a guard-building failure, a UsageError worded
// for --add-dir, as the runtime failure (exit 1) a host-profile refusal is: a
// host-profile launch has no --add-dir to blame.
func hostProfileRuntimeError(err error) error {
	return errors.New(strings.TrimPrefix(err.Error(), "--add-dir cannot be validated: "))
}

// refuseSensitiveHostSource checks the as-written and the symlink-resolved form
// of a source against guards and returns the first reason that matches.
func refuseSensitiveHostSource(guards []addDirGuard, path string) error {
	forms := uniqueStrings(path, resolveExistingPrefix(path))
	for _, g := range guards {
		if g.refuses(forms) {
			return fmt.Errorf("it is a sensitive host location (%s); it cannot be mounted into the sandbox", g.why)
		}
	}
	return nil
}

func (p *hostProfile) resolveContent(agent string, scope Scope, home string) error {
	content := fmt.Sprintf("%s: sandboxHostProfiles.%s.content", p.configPath, agent)
	var guards []addDirGuard
	if len(p.Content.SkillDirectories) > 0 || p.Content.InstructionFile != "" {
		var err error
		if guards, err = hostProfileGuards(home); err != nil {
			return fmt.Errorf("%s: cannot evaluate the sensitive-location guards: %w", content, err)
		}
	}
	// origin names the config key and entry each destination came from, so a
	// collision or a late failure names the entry that caused it.
	origin := map[string]string{}
	var mounts []MountPosture
	for i, skill := range p.Content.SkillDirectories {
		key := fmt.Sprintf("%s.skillDirectories[%d] %q", content, i, skill)
		refuse := func(err error) error {
			return fmt.Errorf("%s: %w", key, err)
		}
		if err := checkHostProfilePath(skill, home, scope); err != nil {
			return refuse(err)
		}
		if err := refuseSensitiveHostSource(guards, skill); err != nil {
			return refuse(err)
		}
		name := filepath.Base(skill)
		if filepath.Base(filepath.Dir(skill)) != "skills" || strings.HasPrefix(name, ".") {
			return refuse(errors.New("a host skill must name an individual visible directory under skills/"))
		}
		if err := validateSkillDirectory(skill); err != nil {
			return refuse(err)
		}
		mount := MountPosture{Source: skill, Destination: hostContentRoot + "/skills/" + name, ReadOnly: true, Kind: MountKindHostContent}
		if prev, taken := origin[mount.Destination]; taken {
			return refuse(fmt.Errorf("host content collision at %s with %s; select only one source with that name", mount.Destination, prev))
		}
		origin[mount.Destination] = key
		mounts = append(mounts, mount)
	}
	if src := p.Content.InstructionFile; src != "" {
		key := fmt.Sprintf("%s.instructionFile %q", content, src)
		refuse := func(err error) error {
			return fmt.Errorf("%s: %w", key, err)
		}
		if err := checkHostProfilePath(src, home, scope); err != nil {
			return refuse(err)
		}
		if err := refuseSensitiveHostSource(guards, src); err != nil {
			return refuse(err)
		}
		name := filepath.Base(src)
		if agent == "claude" && name != "CLAUDE.md" || agent == "codex" && name != "AGENTS.md" && name != "AGENTS.override.md" {
			return refuse(fmt.Errorf("unsupported %s host instruction filename %q", agent, name))
		}
		info, err := os.Stat(src)
		if err != nil {
			return refuse(err)
		}
		if err := validatePersonalFile(src, info); err != nil {
			return refuse(err)
		}
		mount := MountPosture{Source: src, Destination: hostContentRoot + "/instructions/" + name, ReadOnly: true, Kind: MountKindHostContent}
		origin[mount.Destination] = key
		mounts = append(mounts, mount)
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Destination < mounts[j].Destination })
	hash := sha256.New()
	for _, m := range mounts {
		info, err := os.Stat(m.Source)
		if err != nil {
			return fmt.Errorf("%s: %w", origin[m.Destination], err)
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
	if !info.Mode().IsRegular() || st.Nlink != 1 || info.Mode().Perm()&0444 == 0 || info.Mode().Perm()&0022 != 0 || !hostContentOwnerTrusted(path, st.Uid) {
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
			if !hostContentOwnerTrusted(path, st.Uid) || info.Mode().Perm()&0022 != 0 {
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
