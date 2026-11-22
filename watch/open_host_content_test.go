package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenHostContentRejectsReuseWithMissingSkillMount(t *testing.T) {
	f := newAddDirOpenFixture(t)
	skill := mkdirAll(t, f.home, ".claude", "skills", "personal-review")
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	policyDir := mkdirAll(t, f.home, ".config", "cenci")
	policy, err := json.Marshal(map[string]any{
		"sandboxHostProfiles": map[string]any{
			"claude": map[string]any{
				"repositories": []string{f.repo},
				"content": map[string]any{"skillDirectories": []string{skill}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "config.json"), policy, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"open"}, {"open", "--dry-run"}} {
		f.resetCallLog(t)
		result := runCenci(t, f.runningEnv(), f.repo, args...)
		if result.exitCode == 0 || !strings.Contains(result.stderr, "cenci sandbox stop "+f.container) {
			t.Errorf("%v missing recreation remedy: exit %d, %s", args, result.exitCode, result.stderr)
		}
		for _, call := range callLogLines(t, f.callLog) {
			if strings.HasPrefix(call, "rm ") || strings.Contains(call, "--dangerously-skip-permissions") {
				t.Errorf("%v changed or attached to incompatible container: %s", args, call)
			}
		}
	}
}
