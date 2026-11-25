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
				"content":      map[string]any{"skillDirectories": []string{skillDir}},
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

// A refused host-profile source or key fails the preview before the launcher
// makes any runtime call. The fake docker logs every invocation, and a control
// with a valid source proves the same preview does reach it, so an empty log
// means the refusal came first.
func TestDryRunHostProfileRefusalMakesNoRuntimeCall(t *testing.T) {
	setup := func(t *testing.T) (env hostProfileEnv, callLog string) {
		t.Helper()
		env = newHostProfileEnv(t)
		t.Chdir(env.repo)
		return env, dryRunWithCallLog(t)
	}

	t.Run("control: a valid source reaches the runtime", func(t *testing.T) {
		env, callLog := setup(t)
		skill := writeSkill(t, filepath.Join(env.home, ".claude", "skills", "ok"))
		env.writeProfile(t, "claude", map[string]any{"skillDirectories": []string{skill}})
		if _, err := dryRunEngine().DryRun(Options{Agent: "claude"}); err != nil {
			t.Fatal(err)
		}
		if len(readCallLog(t, callLog)) == 0 {
			t.Fatal("the fake runtime recorded nothing for a valid preview, so the no-call assertions below would be vacuous")
		}
	})

	// configure writes the refused policy and returns the offending entry, if any.
	cases := []struct {
		name      string
		configure func(t *testing.T, env hostProfileEnv) string
		key       string
		reason    string
	}{
		{
			name: "guarded skill source",
			configure: func(t *testing.T, env hostProfileEnv) string {
				skill := writeSkill(t, filepath.Join(env.home, ".ssh", "skills", "dryrun"))
				env.writeProfile(t, "claude", map[string]any{"skillDirectories": []string{skill}})
				return skill
			},
			key:    "sandboxHostProfiles.claude.content.skillDirectories[0]",
			reason: "SSH keys",
		},
		{
			name: "opencode key",
			configure: func(t *testing.T, env hostProfileEnv) string {
				env.writeProfile(t, "opencode", nil)
				return ""
			},
			key: `sandboxHostProfiles["opencode"]`,
		},
		{
			name: "plugins",
			configure: func(t *testing.T, env hostProfileEnv) string {
				env.writeConfig(t, map[string]any{"sandboxHostProfiles": map[string]any{"claude": map[string]any{
					"repositories": []string{env.repo},
					"plugins":      map[string]any{"example": map[string]any{"enabled": true}},
				}}})
				return ""
			},
			key:    "sandboxHostProfiles.claude.plugins",
			reason: "not yet supported",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, callLog := setup(t)
			want := []string{env.config, tc.key, tc.reason}
			if entry := tc.configure(t, env); entry != "" {
				want = append(want, entry)
			}

			_, err := dryRunEngine().DryRun(Options{Agent: "claude"})
			if err == nil {
				t.Fatalf("preview accepted a refused host profile (%s)", tc.name)
			}
			for _, w := range want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal does not contain %q: %v", w, err)
				}
			}
			if calls := readCallLog(t, callLog); len(calls) != 0 {
				t.Fatalf("the refusal came after runtime calls: %q", calls)
			}
		})
	}
}

func TestHostProfileAttachRequiresAuthorizedWorkspace(t *testing.T) {
	repo, home := newAuditTestRepo(t), t.TempDir()
	other := filepath.Join(t.TempDir(), filepath.Base(repo))
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// This profile authorizes this repo without requiring content mounts.
	// Other features may inherit preferences or plugins in this same scope.
	policy, err := json.Marshal(map[string]any{"sandboxHostProfiles": map[string]any{
		"claude": map[string]any{"repositories": []string{repo}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".config", "cenci", "config.json"), string(policy))
	dryRunWithCallLog(t)
	scope := ComputeScope("claude", "", repo, home)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_INSPECT_MOUNTS", AgentCLIVolumeName("claude")+"|/opt/cenci-agent|false\n")
	for _, root := range []string{other, repo} {
		t.Setenv("FAKE_REUSE_POSTURE", "off|runc|0\n"+root+"::/workspace\n")
		t.Setenv("FAKE_OBSERVED_POSTURE", "cenci:latest|bridge|runc||0\n"+root+"::/workspace::true\n")
		_, err := dryRunEngine().DryRun(Options{Agent: "claude"})
		audit, auditErr := dryRunEngine().Audit(Options{Agent: "claude"})
		if auditErr != nil {
			t.Fatal(auditErr)
		}
		if root == repo && err != nil {
			t.Fatal(err)
		}
		if root == other {
			if err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Fatalf("different workspace allowed or misdiagnosed: %v", err)
			}
			if audit.HostContent == nil || audit.HostContent.Status != "recreate" || !strings.Contains(audit.HostContent.Warning, "workspace") {
				t.Fatalf("missing repo-scope audit warning: %+v", audit.HostContent)
			}
		}
	}
}
