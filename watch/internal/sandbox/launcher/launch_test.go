package launcher

import (
	"bytes"
	"strings"
	"testing"
)

// TestValidateAgent pins the launcher's single agent gate: claude, codex, and
// opencode (#490) are the only accepted values; anything else is a usage
// error.
func TestValidateAgent(t *testing.T) {
	cases := []struct {
		agent   string
		wantErr bool
	}{
		{"claude", false},
		{"codex", false},
		{"opencode", false},
		{"gemini", true},
		{"", true},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			err := ValidateAgent(tc.agent)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateAgent(%q) = nil, want a usage error", tc.agent)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateAgent(%q) = %v, want nil", tc.agent, err)
			}
		})
	}
}

// TestDefaultModel pins the per-agent model default applied when neither a
// shortcut nor an explicit --model chose one. OpenCode has no cenci-side
// model default: config-driven permissions with no --model equivalent to
// force, so DefaultModel("opencode") must return "" and --model is only
// forwarded when the user explicitly passes one (#490).
func TestDefaultModel(t *testing.T) {
	cases := []struct {
		agent string
		want  string
	}{
		{"claude", "sonnet"},
		{"codex", "gpt-5.6-terra"},
		{"opencode", ""},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			if got := DefaultModel(tc.agent); got != tc.want {
				t.Errorf("DefaultModel(%q) = %q, want %q", tc.agent, got, tc.want)
			}
		})
	}
}

// Creation-time env must never carry TMUX_PANE: `docker run -e TMUX_PANE=...`
// lands in /proc/1/environ of the long-lived shared container and goes stale
// as soon as the creating pane closes, at which point `cenci sandbox
// reap-orphans` classified PID 1 as an orphan, killed it, and tore down the
// container with every attached agent session (#356). Pane identity is
// injected per exec session instead (Launch's execEnvArgs).
func TestAssembleRunArgs_NoCreationTimeTmuxPane(t *testing.T) {
	t.Setenv("TMUX_PANE", "%20")
	home := t.TempDir()
	e := &Engine{Runtime: "docker", Stderr: &bytes.Buffer{}}
	scope := Scope{
		ContainerName:     "claude-cenci-repo",
		VolumeName:        "claude-cenci-repo-home",
		Hostname:          "cenci-repo",
		Image:             "claude-cenci-repo:latest",
		WorkspaceBindHost: t.TempDir(),
		Workdir:           "/workspace",
		WorkspaceScope:    "repo",
	}

	args, err := e.assembleRunArgs("claude", "/usr/local/bin/cenci", "/run/user/1000/cenci", true, scope, Options{Agent: "claude"}, home, false)
	if err != nil {
		t.Fatalf("assembleRunArgs: %v", err)
	}

	for _, a := range args {
		if strings.Contains(a, "TMUX_PANE") {
			t.Errorf("container-creation args must not carry TMUX_PANE (goes stale on the shared container, #356); got %q in:\n%s", a, strings.Join(args, " "))
		}
	}
}
