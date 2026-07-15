package launcher

import (
	"bytes"
	"strings"
	"testing"
)

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

	args, err := e.assembleRunArgs("claude", "/usr/local/bin/claude", "/usr/local/bin/cenci", "/run/user/1000/cenci", true, scope, Options{Agent: "claude"}, home)
	if err != nil {
		t.Fatalf("assembleRunArgs: %v", err)
	}

	for _, a := range args {
		if strings.Contains(a, "TMUX_PANE") {
			t.Errorf("container-creation args must not carry TMUX_PANE (goes stale on the shared container, #356); got %q in:\n%s", a, strings.Join(args, " "))
		}
	}
}
