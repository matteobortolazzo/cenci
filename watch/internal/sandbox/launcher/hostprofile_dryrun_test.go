package launcher

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDryRunPersonalSkillPreviewAndReuse(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	configDir := filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Chdir(repo)
	log := dryRunWithCallLog(t)

	skillDir := filepath.Join(home, ".claude", "skills", "dryrun")
	policyDir := filepath.Join(configDir, "cenci")
	for _, dir := range []string{skillDir, policyDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	const marker = "personal instructions never printed"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(map[string]any{
		"sandboxHostProfiles": map[string]any{
			"claude": map[string]any{
				"repositories": []string{repo},
				"content": map[string]any{"skillDirectories": []string{skillDir}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "config.json"), policy, 0600); err != nil {
		t.Fatal(err)
	}

	opts := Options{Agent: "claude"}
	plan, err := dryRunEngine().DryRun(opts)
	if err != nil {
		t.Fatal(err)
	}
	var report bytes.Buffer
	if err := plan.WriteText(&report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.String(), skillDir) || strings.Contains(report.String(), marker) {
		t.Fatalf("expected a path-only personal skill preview, got %s", report.String())
	}
	var readOnlyBind bool
	for _, arg := range plan.CreateArgv {
		if strings.Contains(arg, skillDir) && strings.Contains(arg, "readonly") &&
			strings.Contains(arg, "bind-recursive=disabled") {
			readOnlyBind = true
		}
	}
	if !readOnlyBind {
		t.Errorf("preview omitted the selected read-only, nonrecursive skill mount: %q", plan.CreateArgv)
	}

	scope := ComputeScope("claude", "", repo, home)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_INSPECT_MOUNTS", AgentCLIVolumeName("claude")+"|/opt/cenci-agent|false\n")
	if _, err := dryRunEngine().DryRun(opts); err == nil {
		t.Fatal("preview accepted running container missing the selected skill mount")
	}
	for _, call := range readCallLog(t, log) {
		if strings.HasPrefix(call, "exec ") || strings.HasPrefix(call, "run ") {
			t.Fatalf("preview ran a workload command: %s", call)
		}
	}
}
