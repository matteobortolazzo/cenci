package babysit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRepositoryAutomergePolicyDogfood exercises the committed repository
// policy through the production resolver and matcher. The shell activation
// contract checks the authored JSON shape; this test proves representative
// protected and eligible paths have the intended runtime meaning.
func TestRepositoryAutomergePolicyDogfood(t *testing.T) {
	cfg := loadRepositoryPolicyForTest(t)

	protected := []string{
		"flow/skills/refine/SKILL.md",
		"watch/internal/daemon/daemon.go",
		"watch/cli_helpers.go",
		"watch/pkg/watch/client.go",
		"watch/plugin/hooks/install.sh",
		"watch/.goreleaser.yml",
		"sandbox/Dockerfile.base",
		"sandbox/skills/setup/SKILL.md",
	}
	for _, path := range protected {
		t.Run("protected_"+filepath.Base(path), func(t *testing.T) {
			policy, reason := resolvePolicy(cfg, []string{path})
			if reason != "" {
				t.Fatalf("resolvePolicy(%q) reason = %q, want a project policy", path, reason)
			}
			if !dogfoodPathProtected(t, policy.ProtectedPaths, path) {
				t.Fatalf("path %q is automerge-eligible, want protected", path)
			}
		})
	}

	allowed := []struct {
		path               string
		maxFiles, maxLines int
	}{
		{"flow/docs/orchestration.md", 8, 400},
		{"flow/tests/example.test.sh", 8, 400},
		{"watch/docs/tmux.md", 10, 500},
		{"watch/tests/example.test.sh", 10, 500},
		{"sandbox/docs/runtime.md", 8, 400},
		{"sandbox/tests/example.test.sh", 8, 400},
	}
	for _, tc := range allowed {
		t.Run("allowed_"+filepath.Base(tc.path), func(t *testing.T) {
			policy, reason := resolvePolicy(cfg, []string{tc.path})
			if reason != "" {
				t.Fatalf("resolvePolicy(%q) reason = %q, want a project policy", tc.path, reason)
			}
			if dogfoodPathProtected(t, policy.ProtectedPaths, tc.path) {
				t.Fatalf("path %q is protected, want representative simple-PR eligibility", tc.path)
			}
			if policy.MaxChangedFiles != tc.maxFiles || policy.MaxDiffLines != tc.maxLines {
				t.Fatalf("policy caps for %q = %d files/%d lines, want %d/%d", tc.path, policy.MaxChangedFiles, policy.MaxDiffLines, tc.maxFiles, tc.maxLines)
			}
			if policy.MergeMethod != "squash" {
				t.Fatalf("policy merge method for %q = %q, want squash", tc.path, policy.MergeMethod)
			}
		})
	}

	if _, reason := resolvePolicy(cfg, []string{"README.md"}); reason != reasonPolicyAbsent {
		t.Fatalf("root-owned README policy reason = %q, want %q", reason, reasonPolicyAbsent)
	}
}

func loadRepositoryPolicyForTest(t *testing.T) repoConfigFile {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate the dogfood test")
	}
	configPath := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", ".cenci", "config.json"))
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read repository policy %s: %v", configPath, err)
	}
	var cfg repoConfigFile
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("parse repository policy %s: %v", configPath, err)
	}
	return cfg
}

func dogfoodPathProtected(t *testing.T, patterns []string, path string) bool {
	t.Helper()
	for _, pattern := range patterns {
		matched, err := matchesProtected(pattern, path)
		if err != nil {
			t.Fatalf("matchesProtected(%q, %q): %v", pattern, path, err)
		}
		if matched {
			return true
		}
	}
	return false
}
