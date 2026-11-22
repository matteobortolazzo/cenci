package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
