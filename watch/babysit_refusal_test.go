package main_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeRecordingGh installs a fake `gh` that appends its arguments to a log and
// fails, returning the bin dir to prepend to PATH and the log path. Any
// invocation of gh proves the supervisor started doing network work.
func writeRecordingGh(t *testing.T) (binDir, logPath string) {
	t.Helper()
	root := t.TempDir()
	binDir = filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(root, "gh.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binDir, logPath
}

// `cenci babysit <pr> --agent opencode` is a usage error (exit 2) raised before
// any supervisor state is written and before babysit.Run touches gh (#1215).
// CENCI_SANDBOX is left unset so the refusal cannot come from the in-container
// forwarding path. This covers the disabled-agent path end-to-end only; the
// capability gate itself is pinned by the function-level
// TestBabysitAgentRefusal in babysit_cmd_test.go.
func TestBabysitRefusesAgentWithoutArmCapability_ExitsUsageErrorBeforeAnyState(t *testing.T) {
	home := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	ghBin, ghLog := writeRecordingGh(t)

	cmd := exec.Command(binaryPath, "babysit", "123", "--agent", "opencode", "--state-dir", stateDir)
	cmd.Env = []string{
		"PATH=" + ghBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
		"XDG_STATE_HOME=" + home,
		"CENCI_SOCKET_DIR=" + filepath.Join(home, "run"),
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2, got err=%v, stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cenci babysit:") || !strings.Contains(stderr.String(), "OpenCode is temporarily disabled") {
		t.Errorf("stderr missing the refusal: %q", stderr.String())
	}
	if _, statErr := os.Stat(stateDir); !os.IsNotExist(statErr) {
		t.Errorf("refusal wrote supervisor state at %s: %v", stateDir, statErr)
	}
	if data, readErr := os.ReadFile(ghLog); readErr == nil && len(data) != 0 {
		t.Errorf("refusal reached babysit.Run; gh was invoked: %q", data)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Errorf("reading gh log: %v", readErr)
	}
}
