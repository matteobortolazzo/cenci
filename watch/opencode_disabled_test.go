package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These commands must refuse before requiring assets, credentials, tmux,
// a runtime, or a daemon. A config override must not reopen the paused client.
func TestOpenCodeDisabledEntryPoints(t *testing.T) {
	for _, args := range [][]string{
		{"open", "--agent", "opencode"},
		{"open", "--agent=opencode", "--dry-run"},
		{"open", "--agent", "opencode", "--shell"},
		{"run", "implement", "42", "--agent", "opencode"},
		{"run", "implement", "42", "--agent", "opencode", "--no-sandbox", "--dry-run"},
		{"babysit", "42", "--agent", "opencode", "--once"},
		{"sandbox", "update-agent", "--agent", "opencode"},
		{"sandbox", "update-plugins", "--agent", "opencode"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			home := t.TempDir()
			cmd := exec.Command(binaryPath, args...)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home,
				"XDG_STATE_HOME=" + home, "CENCI_SOCKET_DIR=" + filepath.Join(home, "run")}
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "OpenCode is temporarily disabled") {
				t.Fatalf("expected explicit pause refusal, err=%v, output=%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(home, "cenci", "babysit")); !os.IsNotExist(err) {
				t.Fatalf("refusal created supervisor state: %v", err)
			}
		})
	}
}

func TestOpenCodeDisabledConfigCannotBypassPause(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config.json")
	if err := os.WriteFile(config, []byte(`{"defaultAgent":"opencode","agents":{"opencode":{"command":"touch","workflows":{"implement":{"args":["/tmp/must-not-run"]}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binaryPath, "run", "implement", "42", "--config", config, "--no-sandbox", "--dry-run")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "OpenCode is temporarily disabled") {
		t.Fatalf("config reopened OpenCode: err=%v, output=%s", err, out)
	}
}
