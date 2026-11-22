package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
