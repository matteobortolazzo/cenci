package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestHostContentIdentityTracksReplacementAndSelection(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "personal", "AGENTS.override.md")
	writeFile(t, src, "first")
	skill := filepath.Join(home, ".agents", "skills", "example")
	writeFile(t, filepath.Join(skill, "SKILL.md"), "first")
	resolve := func() hostProfile {
		t.Helper()
		p := hostProfile{Authorized: true, Content: hostContentConfig{SkillDirectories: []string{skill}, InstructionFile: src}}
		if err := p.resolveContent("codex", Scope{}, home); err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := resolve()
	for _, runtime := range []string{"docker", "podman"} {
		arg := "bind-recursive=disabled"
		if runtime == "podman" {
			arg = "bind-nonrecursive"
		}
		if !slices.Contains(before.createArgs(runtime), "type=bind,src="+skill+",dst="+hostContentRoot+"/skills/example,readonly,"+arg) {
			t.Fatalf("%s omitted nonrecursive read-only bind", runtime)
		}
	}
	writeFile(t, src, "second")
	writeFile(t, filepath.Join(skill, "resource.txt"), "a new resource")
	if after := resolve(); after.ContentIdentity != before.ContentIdentity {
		t.Fatal("in-place host file edit changed mount identity")
	}
	next := src + ".new"
	writeFile(t, next, "new inode")
	if err := os.Rename(next, src); err != nil {
		t.Fatal(err)
	}
	if after := resolve(); after.ContentIdentity == before.ContentIdentity {
		t.Fatal("atomic replacement did not require recreation")
	}
	if err := before.rejectWritableOverlaps([]addDirMount{{Host: skill}}); err == nil {
		t.Fatal("same source available via writable --add-dir")
	}
}

func TestHostContentRejectsUnsafeSources(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"bad:name", "bad,name", "bad\"name", "missing"} {
		src := filepath.Join(home, "skills", name)
		if name != "missing" {
			writeFile(t, filepath.Join(src, "SKILL.md"), "skill")
		}
		p := hostProfile{Content: hostContentConfig{SkillDirectories: []string{src}}}
		if err := p.resolveContent("claude", Scope{}, home); err == nil {
			t.Fatalf("accepted invalid selection %q", src)
		}
	}
	src := filepath.Join(home, "skills", "valid")
	writeFile(t, filepath.Join(src, "SKILL.md"), "skill")
	p := hostProfile{Content: hostContentConfig{SkillDirectories: []string{src, src}}}
	if err := p.resolveContent("claude", Scope{}, home); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("duplicate skill selection: %v", err)
	}
	p.Content.SkillDirectories = []string{src}
	if err := os.Chmod(src, 0777); err != nil {
		t.Fatal(err)
	}
	if err := p.resolveContent("claude", Scope{}, home); err == nil {
		t.Fatal("accepted writable skill directory")
	}
}

func TestRepoProvidedXDGConfigCannotAuthorizeHostProfile(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	configRoot := filepath.Join(repo, "untrusted")
	config := filepath.Join(configRoot, "cenci", "config.json")
	writeFile(t, config, `{"sandboxHostProfiles":{"claude":{"repositories":[]}}}`)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	if _, err := resolveHostProfile("claude", Scope{WorkspaceScope: "repo", RepoRoot: repo, WorkspaceBindHost: repo}, home); err == nil {
		t.Fatal("accepted repository-controlled fleet config")
	}
}

func TestHostProfileContentAudit(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			repo := newAuditTestRepo(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Chdir(repo)
			skill := filepath.Join(home, ".agents", "skills", "review notes")
			instr := filepath.Join(home, "custom codex", "AGENTS.md")
			if agent == "claude" {
				skill = filepath.Join(home, ".claude", "skills", "review notes")
				instr = filepath.Join(home, ".claude", "CLAUDE.md")
			}
			for _, p := range []string{filepath.Join(skill, "SKILL.md"), instr} {
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("personal"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			audit := func(want int) {
				t.Helper()
				got, err := auditEngine().Audit(Options{Agent: agent})
				if err != nil {
					t.Fatal(err)
				}
				n := 0
				for _, m := range got.Mounts {
					if m.Kind == "host-content" {
						n++
						if !m.ReadOnly {
							t.Fatal(m)
						}
					}
				}
				if n != want {
					t.Fatalf("got %d personal content mounts, want %d", n, want)
				}
			}
			writeSandboxConfig(t, repo, `{"sandboxHostProfiles":{"claude":{"repositories":["*"]}}}`)
			audit(0)
			config := filepath.Join(home, ".config", "cenci", "config.json")
			if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
				t.Fatal(err)
			}
			profile := map[string]any{"repositories": []string{repo}, "content": map[string]any{"skillDirectories": []string{skill}, "instructionFile": instr}}
			b, err := json.Marshal(map[string]any{"sandboxHostProfiles": map[string]any{agent: profile}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, b, 0600); err != nil {
				t.Fatal(err)
			}
			audit(2)
			writeSandboxConfig(t, repo, `{"sandbox":{"hostProfile":false}}`)
			audit(0)
			writeSandboxConfig(t, repo, `{}`)
			link := filepath.Join(skill, "escape")
			for _, target := range []string{instr, filepath.Join(home, "missing")} {
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				if _, err := auditEngine().Audit(Options{Agent: agent}); err == nil {
					t.Fatalf("accepted unsafe link %s", link)
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(instr); err != nil {
				t.Fatal(err)
			}
			if _, err := auditEngine().Audit(Options{Agent: agent}); err == nil {
				t.Fatal("accepted missing selected instruction file")
			}
		})
	}
}

// hostProfileEnv is a temporary host for the refusal and accept matrix below: a
// private $HOME, the fleet config under it, a repository the policy authorizes
// and an isolated environment for the sensitive-location guards ($PATH, the
// tool homes, the XDG variables and the daemon socket directory).
type hostProfileEnv struct {
	home, repo, config string
}

func newHostProfileEnv(t *testing.T) hostProfileEnv {
	t.Helper()
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	isolateAddDirEnv(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", "")
	return hostProfileEnv{home: home, repo: repo, config: filepath.Join(home, ".config", "cenci", "config.json")}
}

func (e hostProfileEnv) writeConfig(t *testing.T, config any) {
	t.Helper()
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, e.config, string(b))
}

// writeProfile authorizes the repository for agent with the given content.
func (e hostProfileEnv) writeProfile(t *testing.T, agent string, content map[string]any) {
	t.Helper()
	profile := map[string]any{"repositories": []string{e.repo}}
	if content != nil {
		profile["content"] = content
	}
	e.writeConfig(t, map[string]any{"sandboxHostProfiles": map[string]any{agent: profile}})
}

func (e hostProfileEnv) resolve(agent string) (hostProfile, error) {
	return resolveHostProfile(agent, ComputeScope(agent, "", e.repo, e.home), e.home)
}

func writeSkill(t *testing.T, dir string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, "SKILL.md"), "skill")
	return dir
}

func hostProfileKey(agent, field string) string {
	return "sandboxHostProfiles." + agent + ".content." + field
}

// wantRefusal asserts that err is a refusal that names the fleet config, the
// config key, the offending entry and, when reasons are given, at least one of
// them (compared case-insensitively). An entry is accepted raw or quoted: a
// refusal may format a non-printable entry with %q.
func wantRefusal(t *testing.T, err error, env hostProfileEnv, key, entry string, reasons ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted %s %q", key, entry)
	}
	msg := err.Error()
	if !strings.Contains(msg, env.config) {
		t.Errorf("refusal does not name the config file %q: %v", env.config, err)
	}
	if !strings.Contains(msg, key) {
		t.Errorf("refusal does not name the config key %q: %v", key, err)
	}
	if entry != "" && !strings.Contains(msg, entry) && !strings.Contains(msg, strconv.Quote(entry)) {
		t.Errorf("refusal does not name the offending entry %q: %v", entry, err)
	}
	if len(reasons) == 0 {
		return
	}
	lower := strings.ToLower(msg)
	for _, reason := range reasons {
		if strings.Contains(lower, strings.ToLower(reason)) {
			return
		}
	}
	t.Errorf("refusal does not give the reason (want one of %q): %v", reasons, err)
}

func wantMounts(t *testing.T, got hostProfile, want ...MountPosture) {
	t.Helper()
	if !got.Authorized {
		t.Fatal("repository was not authorized")
	}
	if len(got.Mounts) != len(want) {
		t.Fatalf("got %d host-content mounts %+v, want %d", len(got.Mounts), got.Mounts, len(want))
	}
	for _, m := range want {
		if !slices.Contains(got.Mounts, m) {
			t.Errorf("missing mount %+v in %+v", m, got.Mounts)
		}
	}
}

func skillMount(src string) MountPosture {
	return MountPosture{Source: src, Destination: hostContentRoot + "/skills/" + filepath.Base(src), ReadOnly: true, Kind: MountKindHostContent}
}

func instructionMount(src string) MountPosture {
	return MountPosture{Source: src, Destination: hostContentRoot + "/instructions/" + filepath.Base(src), ReadOnly: true, Kind: MountKindHostContent}
}

// P4, P9: every refusal names the config key and the offending entry, and the
// guarded locations give their reason. The guarded skill sources are well-formed
// skills/<name> directories, so only the sensitive-location guards can refuse
// them. The agent-home sources must be refused for that reason, not for the
// generic skills/<name> shape.
func TestHostProfileContentRefusalsNameTheKeyAndTheEntry(t *testing.T) {
	const skill, instruction = "skillDirectories[0]", "instructionFile"
	under := func(rel string) func(*testing.T, hostProfileEnv) string {
		return func(t *testing.T, env hostProfileEnv) string {
			return writeSkill(t, filepath.Join(env.home, filepath.FromSlash(rel)))
		}
	}
	file := func(rel string) func(*testing.T, hostProfileEnv) string {
		return func(t *testing.T, env hostProfileEnv) string {
			p := filepath.Join(env.home, filepath.FromSlash(rel))
			writeFile(t, p, "personal")
			return p
		}
	}
	dir := func(rel string, mode os.FileMode) func(*testing.T, hostProfileEnv) string {
		return func(t *testing.T, env hostProfileEnv) string {
			p := filepath.Join(env.home, filepath.FromSlash(rel))
			if err := os.MkdirAll(p, mode); err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
	// skillWithLink builds a valid skill holding one symlink named link.
	skillWithLink := func(target func(env hostProfileEnv) string) func(*testing.T, hostProfileEnv) string {
		return func(t *testing.T, env hostProfileEnv) string {
			writeFile(t, filepath.Join(env.home, "outside.txt"), "outside")
			d := writeSkill(t, filepath.Join(env.home, "skills", "x"))
			if err := os.Symlink(target(env), filepath.Join(d, "link")); err != nil {
				t.Fatal(err)
			}
			return d
		}
	}
	// linkIn names the symlink skillWithLink adds to the skill at entry.
	linkIn := func(entry string) []string { return []string{filepath.Join(entry, "link")} }
	onPath := func(rel string, mk func(*testing.T, hostProfileEnv) string) func(*testing.T, hostProfileEnv) string {
		return func(t *testing.T, env hostProfileEnv) string {
			t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+filepath.Join(env.home, filepath.FromSlash(rel)))
			return mk(t, env)
		}
	}

	cases := []struct {
		name, agent, field string
		reasons            []string
		// also lists substrings the refusal must contain besides the entry
		// itself, such as the offending link inside a skill.
		also  func(entry string) []string
		setup func(*testing.T, hostProfileEnv) string
	}{
		// P4: skill directories.
		{name: "relative skill path", field: skill, setup: func(*testing.T, hostProfileEnv) string { return "skills/relative" }},
		{name: "missing skill", field: skill, setup: func(t *testing.T, env hostProfileEnv) string { return filepath.Join(env.home, "skills", "missing") }},
		{name: "skill is a regular file", field: skill, setup: file("skills/x")},
		{name: "skill without SKILL.md", field: skill, setup: dir("skills/x", 0o755)},
		{name: "skill outside a skills directory", field: skill, setup: under("other/x")},
		{name: "skill directory writable by others", field: skill, setup: func(t *testing.T, env hostProfileEnv) string {
			d := under("skills/x")(t, env)
			if err := os.Chmod(d, 0o777); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{name: "skill file writable by others", field: skill, setup: func(t *testing.T, env hostProfileEnv) string {
			d := under("skills/x")(t, env)
			if err := os.Chmod(filepath.Join(d, "SKILL.md"), 0o666); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{name: "nested directory writable by others", field: skill, setup: func(t *testing.T, env hostProfileEnv) string {
			d := under("skills/x")(t, env)
			sub := filepath.Join(d, "assets")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(sub, 0o777); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		// P16: a mount delimiter is refused with its reason, before any runtime
		// call (resolution only reads the filesystem).
		{name: "colon in skill path", field: skill, reasons: []string{"mount delimiters"}, setup: under("skills/bad:name")},
		{name: "comma in skill path", field: skill, reasons: []string{"mount delimiters"}, setup: under("skills/bad,name")},
		{name: "quote in skill path", field: skill, reasons: []string{"mount delimiters"}, setup: under("skills/bad\"name")},
		{name: "non-printable in skill path", field: skill, reasons: []string{"mount delimiters"}, setup: under("skills/bad\x01name")},
		// P14, P15: a link that leaves the skill, or leads nowhere, is refused
		// and the refusal names the link itself, not only the skill.
		{name: "absolute symlink inside skill", field: skill, reasons: []string{"symlink"}, also: linkIn, setup: skillWithLink(func(env hostProfileEnv) string { return filepath.Join(env.home, "outside.txt") })},
		{name: "broken symlink inside skill", field: skill, reasons: []string{"symlink"}, also: linkIn, setup: skillWithLink(func(hostProfileEnv) string { return "missing.txt" })},
		{name: "escaping symlink inside skill", field: skill, reasons: []string{"symlink"}, also: linkIn, setup: skillWithLink(func(hostProfileEnv) string { return "../../outside.txt" })},
		{name: "skill entry is a symlink", field: skill, setup: func(t *testing.T, env hostProfileEnv) string {
			real := under("real/x")(t, env)
			link := filepath.Join(env.home, "skills", "x")
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},

		// P4: the instruction file.
		{name: "relative instruction path", field: instruction, setup: func(*testing.T, hostProfileEnv) string { return "CLAUDE.md" }},
		{name: "missing instruction file", field: instruction, setup: func(t *testing.T, env hostProfileEnv) string { return filepath.Join(env.home, "missing", "CLAUDE.md") }},
		{name: "instruction file is a directory", field: instruction, setup: dir("CLAUDE.md", 0o755)},
		{name: "instruction file writable by others", field: instruction, setup: func(t *testing.T, env hostProfileEnv) string {
			p := file("CLAUDE.md")(t, env)
			if err := os.Chmod(p, 0o666); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{name: "colon in instruction path", field: instruction, reasons: []string{"mount delimiters"}, setup: file("we:ird/CLAUDE.md")},
		{name: "non-printable in instruction path", field: instruction, reasons: []string{"mount delimiters"}, setup: file("we\x01ird/CLAUDE.md")},
		{name: "instruction file is a symlink", field: instruction, setup: func(t *testing.T, env hostProfileEnv) string {
			real := file("real/CLAUDE.md")(t, env)
			link := filepath.Join(env.home, "CLAUDE.md")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{name: "instruction file is a broken symlink", field: instruction, setup: func(t *testing.T, env hostProfileEnv) string {
			link := filepath.Join(env.home, "CLAUDE.md")
			if err := os.Symlink(filepath.Join(env.home, "gone", "CLAUDE.md"), link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{name: "unsupported claude instruction name", field: instruction, setup: file("notes.md")},
		{name: "unsupported codex instruction name", agent: "codex", field: instruction, setup: file("CLAUDE.md")},

		// P9: sensitive locations, each with its reason.
		{name: "skill under .ssh", field: skill, reasons: []string{"SSH keys"}, setup: under(".ssh/skills/x")},
		{name: "skill under cenci's fleet config", field: skill, reasons: []string{"fleet config"}, setup: under(".config/cenci/skills/x")},
		{name: "skill under a PATH entry", field: skill, reasons: []string{"$PATH"}, setup: onPath("tools", under("tools/skills/x"))},
		{name: "skill under the session runtime directory", field: skill, reasons: []string{"runtime directory"}, setup: func(t *testing.T, env hostProfileEnv) string {
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(env.home, "run"))
			return under("run/skills/x")(t, env)
		}},
		{name: "instruction file under .ssh", field: instruction, reasons: []string{"SSH keys"}, setup: file(".ssh/CLAUDE.md")},
		{name: "instruction file under a PATH entry", field: instruction, reasons: []string{"$PATH"}, setup: onPath("tools", file("tools/CLAUDE.md"))},

		// P9: the credential files. The instruction-file guard runs before the
		// filename check, so a credential file is refused as one, not for its name.
		{name: "Claude credential file as the instruction file", field: instruction, reasons: []string{"agent credentials"}, setup: file(".claude/.credentials.json")},
		{name: "Codex credential file as the instruction file", agent: "codex", field: instruction, reasons: []string{"agent credentials"}, setup: file(".codex/auth.json")},
		{name: "Codex credential file as a Claude instruction file", field: instruction, reasons: []string{"agent credentials"}, setup: file(".codex/auth.json")},
		{name: "$CODEX_HOME credential file as the instruction file", agent: "codex", field: instruction, reasons: []string{"agent credentials"}, setup: func(t *testing.T, env hostProfileEnv) string {
			t.Setenv("CODEX_HOME", filepath.Join(env.home, "state", "codex"))
			return file("state/codex/auth.json")(t, env)
		}},

		// P9: runtime client config, cenci state and the socket directories.
		{name: "skill under ~/.docker", field: skill, reasons: []string{"Docker client config"}, setup: under(".docker/skills/x")},
		{name: "skill under ~/.colima", field: skill, reasons: []string{"Colima runtime sockets"}, setup: under(".colima/skills/x")},
		{name: "skill under ~/.orbstack", field: skill, reasons: []string{"OrbStack runtime sockets"}, setup: under(".orbstack/skills/x")},
		{name: "skill under cenci's state", field: skill, reasons: []string{"cenci's state"}, setup: under(".local/state/cenci/skills/x")},
		{name: "skill under $XDG_STATE_HOME/cenci", field: skill, reasons: []string{"cenci's state"}, setup: func(t *testing.T, env hostProfileEnv) string {
			t.Setenv("XDG_STATE_HOME", filepath.Join(env.home, "state"))
			return under("state/cenci/skills/x")(t, env)
		}},
		{name: "skill under the daemon socket directory", field: skill, reasons: []string{"daemon socket directory"}, setup: func(t *testing.T, env hostProfileEnv) string {
			t.Setenv("CENCI_SOCKET_DIR", filepath.Join(env.home, "sock"))
			return under("sock/skills/x")(t, env)
		}},
		{name: "skill under the tmux socket directory of $TMUX_TMPDIR", field: skill, reasons: []string{"$TMUX_TMPDIR"}, setup: func(t *testing.T, env hostProfileEnv) string {
			t.Setenv("TMUX_TMPDIR", filepath.Join(env.home, "tmuxtmp"))
			return under("tmuxtmp/tmux-" + strconv.Itoa(os.Getuid()) + "/skills/x")(t, env)
		}},
		{name: "skill holding the running tmux server's socket directory", field: skill, reasons: []string{"$TMUX"}, setup: func(t *testing.T, env hostProfileEnv) string {
			d := under("skills/x")(t, env)
			t.Setenv("TMUX", filepath.Join(d, "sock", "server")+",1234,0")
			return d
		}},
		{name: "skill holding the ssh-agent socket directory", field: skill, reasons: []string{"$SSH_AUTH_SOCK"}, setup: func(t *testing.T, env hostProfileEnv) string {
			d := under("skills/x")(t, env)
			t.Setenv("SSH_AUTH_SOCK", filepath.Join(d, "agent.sock"))
			return d
		}},

		// P9, P34: an entire home or agent home is never a source. The $HOME and
		// parent-of-$HOME cases are refused by checkHostProfilePath before any
		// guard runs, so they say nothing about the guards. The agent homes are
		// refused only by the covering "agent home" guard: the unmodified overlap
		// guard says "it holds ... credentials", so requiring "agent home" fails
		// if the covering guard is removed.
		{name: "$HOME itself", field: skill, setup: func(t *testing.T, env hostProfileEnv) string { return env.home }},
		{name: "parent of $HOME", field: skill, setup: func(t *testing.T, env hostProfileEnv) string { return filepath.Dir(env.home) }},
		{name: "~/.claude itself", field: skill, reasons: []string{"agent home"}, setup: dir(".claude", 0o700)},
		{name: "~/.codex itself", agent: "codex", field: skill, reasons: []string{"agent home"}, setup: dir(".codex", 0o700)},
		{name: "$CODEX_HOME itself", agent: "codex", field: skill, reasons: []string{"agent home"}, setup: func(t *testing.T, env hostProfileEnv) string {
			p := dir("state/codex", 0o700)(t, env)
			t.Setenv("CODEX_HOME", p)
			return p
		}},
		// A parent of $CODEX_HOME is refused too, not only the home itself.
		{name: "parent of $CODEX_HOME", agent: "codex", field: skill, reasons: []string{"agent home"}, setup: func(t *testing.T, env hostProfileEnv) string {
			parent := writeSkill(t, filepath.Join(env.home, "skills", "x"))
			codexHome := filepath.Join(parent, "codex")
			if err := os.MkdirAll(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_HOME", codexHome)
			return parent
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := tc.agent
			if agent == "" {
				agent = "claude"
			}
			env := newHostProfileEnv(t)
			entry := tc.setup(t, env)
			content := map[string]any{"skillDirectories": []string{entry}}
			if tc.field == instruction {
				content = map[string]any{"instructionFile": entry}
			}
			env.writeProfile(t, agent, content)
			_, err := env.resolve(agent)
			wantRefusal(t, err, env, hostProfileKey(agent, tc.field), entry, tc.reasons...)
			if err == nil || tc.also == nil {
				return
			}
			for _, want := range tc.also(entry) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

func TestHostProfileRefusalIndexesTheOffendingSkillEntry(t *testing.T) {
	env := newHostProfileEnv(t)
	good := writeSkill(t, filepath.Join(env.home, "skills", "good"))
	bad := filepath.Join(env.home, "skills", "missing")
	env.writeProfile(t, "claude", map[string]any{"skillDirectories": []string{good, bad}})
	_, err := env.resolve("claude")
	wantRefusal(t, err, env, hostProfileKey("claude", "skillDirectories[1]"), bad)
	if err != nil && strings.Contains(err.Error(), "skillDirectories[0]") {
		t.Errorf("refusal blames the valid first entry: %v", err)
	}
}

// distrustOwnerOf makes the owner check treat each path as owned by a foreign
// user, so the refusal can be exercised without root (chown needs it).
func distrustOwnerOf(t *testing.T, paths ...string) {
	t.Helper()
	prev := hostContentOwnerTrusted
	t.Cleanup(func() { hostContentOwnerTrusted = prev })
	hostContentOwnerTrusted = func(path string, uid uint32) bool {
		return !slices.Contains(paths, path) && prev(path, uid)
	}
}

// P4: a source, or any entry the validation walks, owned by another user is
// refused, naming the config key and the entry. Runs without root.
func TestHostProfileRefusesAForeignOwnerWithoutRoot(t *testing.T) {
	cases := []struct {
		name  string
		field string
		agent string
		setup func(*testing.T, hostProfileEnv) (entry string, foreign []string)
	}{
		{"skill file", "skillDirectories[0]", "claude", func(t *testing.T, env hostProfileEnv) (string, []string) {
			d := writeSkill(t, filepath.Join(env.home, "skills", "x"))
			return d, []string{filepath.Join(d, "SKILL.md")}
		}},
		{"skill directory", "skillDirectories[0]", "claude", func(t *testing.T, env hostProfileEnv) (string, []string) {
			d := writeSkill(t, filepath.Join(env.home, "skills", "x"))
			return d, []string{d}
		}},
		{"nested skill directory", "skillDirectories[0]", "claude", func(t *testing.T, env hostProfileEnv) (string, []string) {
			d := writeSkill(t, filepath.Join(env.home, "skills", "x"))
			sub := filepath.Join(d, "assets")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			return d, []string{sub}
		}},
		{"skills parent directory", "skillDirectories[0]", "claude", func(t *testing.T, env hostProfileEnv) (string, []string) {
			d := writeSkill(t, filepath.Join(env.home, "skills", "x"))
			return d, []string{filepath.Join(env.home, "skills")}
		}},
		{"instruction file", "instructionFile", "claude", func(t *testing.T, env hostProfileEnv) (string, []string) {
			p := filepath.Join(env.home, "CLAUDE.md")
			writeFile(t, p, "personal")
			return p, []string{p}
		}},
		{"codex instruction file", "instructionFile", "codex", func(t *testing.T, env hostProfileEnv) (string, []string) {
			p := filepath.Join(env.home, "AGENTS.md")
			writeFile(t, p, "personal")
			return p, []string{p}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newHostProfileEnv(t)
			entry, foreign := tc.setup(t, env)
			content := map[string]any{"skillDirectories": []string{entry}}
			if tc.field == "instructionFile" {
				content = map[string]any{"instructionFile": entry}
			}
			env.writeProfile(t, tc.agent, content)
			if _, err := env.resolve(tc.agent); err != nil {
				t.Fatalf("a user-owned source is refused before the owner is distrusted: %v", err)
			}
			distrustOwnerOf(t, foreign...)
			_, err := env.resolve(tc.agent)
			wantRefusal(t, err, env, hostProfileKey(tc.agent, tc.field), entry, "owned by", "owner")
		})
	}
}

// P4: a source owned by another user is refused. Creating one needs root, so
// this is the real-chown counterpart of the seam-driven test above.
func TestHostProfileRefusesForeignOwnedContent(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("a file owned by another user can only be created as root")
	}
	const other = 65534
	env := newHostProfileEnv(t)
	skill := writeSkill(t, filepath.Join(env.home, "skills", "x"))
	if err := os.Chown(filepath.Join(skill, "SKILL.md"), other, -1); err != nil {
		t.Fatal(err)
	}
	env.writeProfile(t, "claude", map[string]any{"skillDirectories": []string{skill}})
	_, err := env.resolve("claude")
	wantRefusal(t, err, env, hostProfileKey("claude", "skillDirectories[0]"), skill)

	instruction := filepath.Join(env.home, "CLAUDE.md")
	writeFile(t, instruction, "personal")
	if err := os.Chown(instruction, other, -1); err != nil {
		t.Fatal(err)
	}
	env.writeProfile(t, "claude", map[string]any{"instructionFile": instruction})
	_, err = env.resolve("claude")
	wantRefusal(t, err, env, hostProfileKey("claude", "instructionFile"), instruction)
}

// P4: malformed JSON and wrong-typed values name the fleet config, and the
// wrong-typed ones name their key.
func TestHostProfileMalformedPolicyNamesTheConfig(t *testing.T) {
	cases := []struct {
		name string
		body func(repo string) string
		key  string
	}{
		{"malformed JSON", func(string) string { return "{not json" }, ""},
		{"profile is not an object", func(string) string { return `{"sandboxHostProfiles":{"claude":[]}}` }, "sandboxHostProfiles.claude"},
		{"skill list has the wrong type", func(repo string) string {
			return `{"sandboxHostProfiles":{"claude":{"repositories":[` + strconv.Quote(repo) + `],"content":{"skillDirectories":"skills/x"}}}}`
		}, "sandboxHostProfiles.claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newHostProfileEnv(t)
			writeFile(t, env.config, tc.body(env.repo))
			_, err := env.resolve("claude")
			if err == nil {
				t.Fatal("accepted a malformed fleet config")
			}
			if !strings.Contains(err.Error(), env.config) || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("want the config path %q and key %q in %v", env.config, tc.key, err)
			}
		})
	}
}

// P9, P34: sources under the agent homes' own subdirectories stay usable, so
// refusing an entire agent home must not refuse ~/.claude/skills/<name>.
func TestHostProfileAcceptsPersonalLocations(t *testing.T) {
	cases := []struct {
		name, agent, home string
		skill             string
		instruction       string
	}{
		{"claude skill and CLAUDE.md under ~/.claude", "claude", ".claude", "skills/x", "CLAUDE.md"},
		{"codex skill and AGENTS.md under ~/.codex", "codex", ".codex", "skills/x", "AGENTS.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newHostProfileEnv(t)
			skill := writeSkill(t, filepath.Join(env.home, tc.home, filepath.FromSlash(tc.skill)))
			instruction := filepath.Join(env.home, tc.home, tc.instruction)
			writeFile(t, instruction, "personal")
			env.writeProfile(t, tc.agent, map[string]any{"skillDirectories": []string{skill}, "instructionFile": instruction})
			got, err := env.resolve(tc.agent)
			if err != nil {
				t.Fatal(err)
			}
			wantMounts(t, got, skillMount(skill), instructionMount(instruction))
		})
	}
}

// P12: a skill source under a non-default Codex home resolves and mounts, while
// that home itself is refused as an entire agent home.
func TestHostProfileCodexHomeIsRefusedButItsSkillsAreAccepted(t *testing.T) {
	env := newHostProfileEnv(t)
	codexHome := filepath.Join(env.home, "state", "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	skill := writeSkill(t, filepath.Join(codexHome, "skills", "mine"))
	env.writeProfile(t, "codex", map[string]any{"skillDirectories": []string{skill}})
	got, err := env.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, got, skillMount(skill))

	env.writeProfile(t, "codex", map[string]any{"skillDirectories": []string{codexHome}})
	_, err = env.resolve("codex")
	wantRefusal(t, err, env, hostProfileKey("codex", "skillDirectories[0]"), codexHome, "agent home")
}

// P32: only Claude Code and Codex have a host profile. Any other key under
// sandboxHostProfiles is refused on every Claude or Codex resolution, whether or
// not it sits next to the agent's own profile.
func TestHostProfileRejectsUnsupportedAgentKeys(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, key := range []string{"opencode", "gemini", "Claude"} {
			for _, withOwnProfile := range []bool{false, true} {
				name := fmt.Sprintf("%s resolution rejects %s (own profile %t)", agent, key, withOwnProfile)
				t.Run(name, func(t *testing.T) {
					env := newHostProfileEnv(t)
					profiles := map[string]any{key: map[string]any{"repositories": []string{env.repo}}}
					if withOwnProfile {
						profiles[agent] = map[string]any{"repositories": []string{env.repo}}
					}
					env.writeConfig(t, map[string]any{"sandboxHostProfiles": profiles})
					_, err := env.resolve(agent)
					if err == nil {
						t.Fatalf("accepted sandboxHostProfiles.%s", key)
					}
					want := "sandboxHostProfiles[" + strconv.Quote(key) + "]"
					if !strings.Contains(err.Error(), env.config) || !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal must name %q and the config key %s: %v", env.config, want, err)
					}
				})
			}
		}
	}

	// The key is printed quoted: a control character in a JSON key must not
	// reach the terminal.
	t.Run("a control character in the key is escaped", func(t *testing.T) {
		env := newHostProfileEnv(t)
		const key = "bad\x1b[31mkey"
		env.writeConfig(t, map[string]any{"sandboxHostProfiles": map[string]any{key: map[string]any{"repositories": []string{env.repo}}}})
		_, err := env.resolve("claude")
		if err == nil {
			t.Fatal("accepted an unsupported key with a control character")
		}
		if strings.Contains(err.Error(), "\x1b") {
			t.Errorf("the refusal carries a raw ESC byte: %q", err.Error())
		}
		if want := "sandboxHostProfiles[" + strconv.Quote(key) + "]"; !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name the quoted key %s: %v", want, err)
		}
	})
}

// Plugins stay unsupported under the current rule: any value other than null is
// refused, an empty object included, and null reads as absent.
func TestHostProfilePluginsNullIsAbsentAndEmptyObjectIsRefused(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent+" null", func(t *testing.T) {
			env := newHostProfileEnv(t)
			env.writeConfig(t, map[string]any{"sandboxHostProfiles": map[string]any{agent: map[string]any{
				"repositories": []string{env.repo},
				"plugins":      nil,
			}}})
			if _, err := env.resolve(agent); err != nil {
				t.Fatalf("plugins: null is an absent value but was refused: %v", err)
			}
		})
		t.Run(agent+" empty object", func(t *testing.T) {
			env := newHostProfileEnv(t)
			env.writeConfig(t, map[string]any{"sandboxHostProfiles": map[string]any{agent: map[string]any{
				"repositories": []string{env.repo},
				"plugins":      map[string]any{},
			}}})
			_, err := env.resolve(agent)
			if err == nil {
				t.Fatal("accepted plugins: {}")
			}
			for _, want := range []string{env.config, "sandboxHostProfiles." + agent + ".plugins", "not yet supported"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not contain %q: %v", want, err)
				}
			}
		})
	}
}

// An agent home guard replaces only the credential overlap guard it supersedes.
// A different guard whose path happens to equal $CODEX_HOME keeps its own reason.
func TestHostProfileCodexHomeEqualToAnotherGuardedPathKeepsThatGuard(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		// setup points the guarded location at a path under $HOME and returns
		// it; the test then makes that path $CODEX_HOME.
		setup func(*testing.T, hostProfileEnv) string
	}{
		{"cenci's fleet config", "fleet config", func(t *testing.T, env hostProfileEnv) string {
			return filepath.Join(env.home, ".config", "cenci")
		}},
		{"a $PATH entry", "$PATH", func(t *testing.T, env hostProfileEnv) string {
			tools := filepath.Join(env.home, "tools")
			t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+tools)
			return tools
		}},
		{"the daemon socket directory", "daemon socket directory", func(t *testing.T, env hostProfileEnv) string {
			sock := filepath.Join(env.home, "sock")
			t.Setenv("CENCI_SOCKET_DIR", sock)
			return sock
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newHostProfileEnv(t)
			codexHome := tc.setup(t, env)
			t.Setenv("CODEX_HOME", codexHome)
			skill := writeSkill(t, filepath.Join(codexHome, "skills", "x"))
			env.writeProfile(t, "codex", map[string]any{"skillDirectories": []string{skill}})
			_, err := env.resolve("codex")
			wantRefusal(t, err, env, hostProfileKey("codex", "skillDirectories[0]"), skill, tc.reason)
		})
	}
}

// A relative $CODEX_HOME is resolved against the working directory like
// --add-dir's relative env paths, never silently ignored.
func TestHostProfileResolvesARelativeCodexHome(t *testing.T) {
	env := newHostProfileEnv(t)
	base := filepath.Join(env.home, "state")
	codexHome := filepath.Join(base, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	t.Setenv("CODEX_HOME", "codex")

	env.writeProfile(t, "codex", map[string]any{"skillDirectories": []string{codexHome}})
	_, err := env.resolve("codex")
	wantRefusal(t, err, env, hostProfileKey("codex", "skillDirectories[0]"), codexHome, "agent home")

	skill := writeSkill(t, filepath.Join(codexHome, "skills", "mine"))
	env.writeProfile(t, "codex", map[string]any{"skillDirectories": []string{skill}})
	got, err := env.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, got, skillMount(skill))
}

// A relative $CODEX_HOME that cannot be made absolute is a refusal naming the
// config key and the variable, a plain runtime error rather than an --add-dir
// usage error.
func TestHostProfileUnresolvableCodexHomeIsARuntimeRefusal(t *testing.T) {
	env := newHostProfileEnv(t)
	skill := writeSkill(t, filepath.Join(env.home, ".codex", "skills", "x"))
	env.writeProfile(t, "codex", map[string]any{"skillDirectories": []string{skill}})
	scope := ComputeScope("codex", "", env.repo, env.home)

	gone := filepath.Join(env.home, "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("this platform still resolves a removed working directory")
	}
	t.Setenv("CODEX_HOME", "relative/codex")

	_, err := resolveHostProfile("codex", scope, env.home)
	if err == nil {
		t.Fatal("accepted a source while $CODEX_HOME could not be resolved")
	}
	if IsUsage(err) {
		t.Errorf("a host-profile refusal must exit 1, not be a usage error (exit 2): %v", err)
	}
	if strings.Contains(err.Error(), "--add-dir") {
		t.Errorf("a host-profile refusal must not mention --add-dir: %v", err)
	}
	for _, want := range []string{env.config, "sandboxHostProfiles.codex.content", "CODEX_HOME"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not contain %q: %v", want, err)
		}
	}
}

// Two entries that resolve to the same destination name are refused, and the
// refusal names both of them.
func TestHostProfileCollisionNamesBothEntries(t *testing.T) {
	env := newHostProfileEnv(t)
	first := writeSkill(t, filepath.Join(env.home, "a", "skills", "x"))
	second := writeSkill(t, filepath.Join(env.home, "b", "skills", "x"))
	env.writeProfile(t, "claude", map[string]any{"skillDirectories": []string{first, second}})
	_, err := env.resolve("claude")
	wantRefusal(t, err, env, hostProfileKey("claude", "skillDirectories[0]"), first, "collision")
	wantRefusal(t, err, env, hostProfileKey("claude", "skillDirectories[1]"), second, "collision")
}

// P36: this change implements content (plus the shipped preferences and
// version); plugins are refused as not yet supported, naming their key.
func TestHostProfileRejectsPlugins(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			env := newHostProfileEnv(t)
			env.writeConfig(t, map[string]any{"sandboxHostProfiles": map[string]any{agent: map[string]any{
				"repositories": []string{env.repo},
				"plugins":      map[string]any{"example": map[string]any{"enabled": true}},
			}}})
			_, err := env.resolve(agent)
			if err == nil {
				t.Fatal("accepted plugins")
			}
			for _, want := range []string{env.config, "sandboxHostProfiles." + agent + ".plugins", "not yet supported"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not contain %q: %v", want, err)
				}
			}
		})
	}
}

// Every host-profile refusal exits 1, like the config-reading refusals of the
// forward-env and repository policies. The guards are shared with --add-dir,
// whose guard-evaluation failure is a usage error worded for that flag; a host
// profile launch has no --add-dir, so it must surface as a plain runtime failure.
func TestHostProfileGuardEvaluationFailureIsARuntimeRefusalNamingTheKey(t *testing.T) {
	env := newHostProfileEnv(t)
	skill := writeSkill(t, filepath.Join(env.home, ".claude", "skills", "x"))
	env.writeProfile(t, "claude", map[string]any{"skillDirectories": []string{skill}})
	t.Setenv("CENCI_SOCKET_DIR", "relative/socket-dir")

	_, err := env.resolve("claude")
	if err == nil {
		t.Fatal("accepted a source while the sensitive-location guards could not be evaluated")
	}
	if IsUsage(err) {
		t.Errorf("a host-profile refusal must exit 1, not be a usage error (exit 2): %v", err)
	}
	if strings.Contains(err.Error(), "--add-dir") {
		t.Errorf("a host-profile refusal must not mention --add-dir: %v", err)
	}
	for _, want := range []string{env.config, "sandboxHostProfiles.claude.content", "daemon socket directory", "CENCI_SOCKET_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not contain %q: %v", want, err)
		}
	}
}

// The agent-key allowlist must not break a config that carries both supported
// agents: each resolution reads its own profile and ignores the other.
func TestHostProfileAcceptsClaudeAndCodexProfilesTogether(t *testing.T) {
	env := newHostProfileEnv(t)
	skill := writeSkill(t, filepath.Join(env.home, ".claude", "skills", "x"))
	env.writeConfig(t, map[string]any{"sandboxHostProfiles": map[string]any{
		"claude": map[string]any{"repositories": []string{env.repo}, "content": map[string]any{"skillDirectories": []string{skill}}},
		"codex":  map[string]any{"repositories": []string{env.repo}},
	}})
	got, err := env.resolve("claude")
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, got, skillMount(skill))
	got, err = env.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, got)
}

// The same refusals reach cenci audit, which resolves the profile without a
// runtime.
func TestHostProfileAuditRefusesGuardedSourceAndUnsupportedKey(t *testing.T) {
	env := newHostProfileEnv(t)
	t.Chdir(env.repo)

	skill := writeSkill(t, filepath.Join(env.home, ".ssh", "skills", "x"))
	env.writeProfile(t, "claude", map[string]any{"skillDirectories": []string{skill}})
	_, err := auditEngine().Audit(Options{Agent: "claude"})
	wantRefusal(t, err, env, hostProfileKey("claude", "skillDirectories[0]"), skill, "SSH keys")

	env.writeConfig(t, map[string]any{"sandboxHostProfiles": map[string]any{"opencode": map[string]any{"repositories": []string{env.repo}}}})
	_, err = auditEngine().Audit(Options{Agent: "claude"})
	if err == nil || !strings.Contains(err.Error(), `sandboxHostProfiles["opencode"]`) {
		t.Fatalf("audit must refuse the opencode key by name, got %v", err)
	}
}
