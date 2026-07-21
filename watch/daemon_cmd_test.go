package main_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

// -- daemon start --json / CENCI_LOG_JSON ---------------------------------
//
// `cenci daemon start` gains an opt-in --json flag (default sourced from
// CENCI_LOG_JSON) that routes its own start/signal -v lines through the new
// internal/logging seam as structured JSON instead of plain text. Only the
// command-layer lines are covered (Q2) — internal/daemon's own -v output is
// untouched. These tests spawn the real built binary in the background
// (mirroring startDaemonBackground in daemon_test.go, same package) so they
// can capture its stderr (log.Printf's default destination) across a clean
// SIGTERM shutdown.

// capturingDaemon wraps a backgrounded `daemon start` subprocess with its
// captured stderr, mirroring bgDaemon in daemon_test.go but additionally
// wiring stderr to a buffer so the JSON/plain-text startup line can be
// inspected after the process exits.
type capturingDaemon struct {
	cmd    *exec.Cmd
	done   chan struct{}
	stderr *bytes.Buffer
}

// startDaemonCapturingStderr spawns `cenci daemon start` with extraArgs,
// waits for its PID file (proving it actually started, not just parsed
// flags), and returns it with stderr captured. The caller's test must have
// already set XDG_RUNTIME_DIR via t.Setenv, and any CENCI_LOG_JSON value it
// wants via t.Setenv before calling this helper.
func startDaemonCapturingStderr(t *testing.T, extraArgs ...string) *capturingDaemon {
	t.Helper()
	args := append([]string{"daemon", "start"}, extraArgs...)
	cmd := exec.Command(binaryPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start 'daemon start %s': %v", strings.Join(extraArgs, " "), err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	})
	waitForFile(t, ipc.DefaultPIDPath(), 3*time.Second)
	return &capturingDaemon{cmd: cmd, done: done, stderr: &stderr}
}

// stopAndCollectStderr sends SIGTERM, waits for clean exit, and returns the
// full captured stderr (safe to read only after done closes: exec.Cmd's
// internal stderr-copying goroutine is guaranteed finished by the time
// Wait() returns).
func stopAndCollectStderr(t *testing.T, d *capturingDaemon) string {
	t.Helper()
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	select {
	case <-d.done:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not exit after SIGTERM")
	}
	return d.stderr.String()
}

// firstNonEmptyLine returns the first non-blank line of s, trimmed.
func firstNonEmptyLine(t *testing.T, s string) string {
	t.Helper()
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	t.Fatalf("expected at least one non-empty output line, got:\n%q", s)
	return ""
}

// TestDaemonStart_JSONFlag_EmitsParseableJSONStartupLine covers the
// explicit --json flag: the "cenci starting ..." -v line must come out as a
// single parseable JSON object with severity "info" and a message
// mentioning the startup line, not the old plain-text format.
func TestDaemonStart_JSONFlag_EmitsParseableJSONStartupLine(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	d := startDaemonCapturingStderr(t, "-v", "--json")

	out := stopAndCollectStderr(t, d)
	line := firstNonEmptyLine(t, out)

	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("--json startup line is not valid JSON: %v\nfull stderr:\n%s", err, out)
	}
	if got["severity"] != "info" {
		t.Errorf("severity = %v, want info", got["severity"])
	}
	msg, _ := got["message"].(string)
	if !strings.Contains(msg, "cenci starting") {
		t.Errorf("message = %q, want it to contain \"cenci starting\"", msg)
	}
}

// TestDaemonStart_CENCI_LOG_JSON_DefaultsJSONWithoutFlag covers the env-var
// default: with CENCI_LOG_JSON set and no --json flag at all, the startup
// line must still come out as JSON — the env var is the flag's default,
// exactly like every other CENCI_-prefixed host var per
// docs/cli-conventions.md.
func TestDaemonStart_CENCI_LOG_JSON_DefaultsJSONWithoutFlag(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CENCI_LOG_JSON", "1")
	d := startDaemonCapturingStderr(t, "-v")

	out := stopAndCollectStderr(t, d)
	line := firstNonEmptyLine(t, out)

	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("CENCI_LOG_JSON=1 startup line is not valid JSON: %v\nfull stderr:\n%s", err, out)
	}
	if got["severity"] != "info" {
		t.Errorf("severity = %v, want info", got["severity"])
	}
}

// TestDaemonStart_JSONFlagOverridesEnvVar covers precedence: an explicit
// --json=false must win over CENCI_LOG_JSON=1 (the env var only supplies
// the flag's default), leaving the startup line in plain text.
func TestDaemonStart_JSONFlagOverridesEnvVar(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CENCI_LOG_JSON", "1")
	d := startDaemonCapturingStderr(t, "-v", "--json=false")

	out := stopAndCollectStderr(t, d)
	line := firstNonEmptyLine(t, out)

	if strings.HasPrefix(line, "{") {
		t.Errorf("expected plain-text output when --json=false overrides CENCI_LOG_JSON=1, got a JSON-looking line: %s", line)
	}
	if !strings.Contains(line, "cenci starting") {
		t.Errorf("expected the plain \"cenci starting\" line, got: %s", line)
	}
}

// TestDaemonStart_WithoutJSONFlagOrEnv_PlainTextUnchanged is the baseline
// regression: with neither --json nor CENCI_LOG_JSON set, the startup line
// must remain today's plain text, never JSON.
func TestDaemonStart_WithoutJSONFlagOrEnv_PlainTextUnchanged(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	d := startDaemonCapturingStderr(t, "-v")

	out := stopAndCollectStderr(t, d)
	line := firstNonEmptyLine(t, out)

	if strings.HasPrefix(line, "{") {
		t.Errorf("did not expect JSON output when --json/CENCI_LOG_JSON are unset, got: %s", line)
	}
	if !strings.Contains(line, "cenci starting (event-driven, sweep every") {
		t.Errorf("expected the unchanged plain startup line, got: %s", line)
	}
}
