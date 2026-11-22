package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDryRunHostPreferences(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			repo, home := newAuditTestRepo(t), t.TempDir()
			t.Chdir(repo)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("OPENAI_API_KEY", "fixture")
			setFakeDockerNotRunning(t)
			name, data := "settings.json", `{"model":"opus","effortLevel":"low","hooks":{"x":"SECRET-CANARY"}}`
			if agent == "codex" {
				name, data = "config.toml", "model = \"fixture-model\"\nmodel_reasoning_effort=\"low\"\n[mcp_servers.ignored]\ncommand=\"SECRET-CANARY\""
			}
			source := filepath.Join(home, "."+agent, name)
			writeFile(t, source, data)
			writeFile(t, filepath.Join(home, ".config", "cenci", "config.json"), fmt.Sprintf(`{"sandboxHostProfiles":{%q:{"repositories":[%q],"preferences":{"source":%q}}}}`, agent, repo, source))
			eng := dryRunEngine()
			plan, err := eng.DryRun(Options{Agent: agent})
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(plan.AttachArgv, "--model") {
				t.Fatalf("default model overrides native config: %q", plan.AttachArgv)
			}
			plan, err = eng.DryRun(Options{Agent: agent, Model: "explicit"})
			if err != nil || !slices.Contains(plan.AttachArgv, "explicit") {
				t.Fatalf("explicit model lost: %v %v", plan, err)
			}
			p, err := auditEngine().Audit(Options{Agent: agent})
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), source) || !slices.Contains(p.Preferences.Selected, "model") || len(p.Preferences.Skipped) != 1 || strings.Contains(string(b), "SECRET-CANARY") {
				t.Fatalf("unexpected redacted report: %s", b)
			}
			if raw, err := os.ReadFile(source); err != nil || string(raw) != data {
				t.Fatal("host config changed")
			}
			for _, bad := range []string{"[truncated", `{"model":"/host/command"}`} {
				if agent == "codex" && strings.HasPrefix(bad, "{") {
					bad = "model='/host/command'"
				}
				writeFile(t, source, bad)
				if _, err := eng.DryRun(Options{Agent: agent}); err == nil {
					t.Fatal("accepted invalid selected config")
				}
			}
		})
	}
}
