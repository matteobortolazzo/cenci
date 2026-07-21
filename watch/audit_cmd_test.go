package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -- cenci audit (ticket #588) ---------------------------------------------
//
// `cenci audit [flags]` is a read-only report on the effective sandbox
// security posture the launcher WOULD apply for the current repo/agent/
// flags — see internal/sandbox/launcher/audit.go. These black-box tests
// drive the real built `cenci` binary as a subprocess, mirroring
// diagnose_cmd_test.go/diagnose_test.go's pattern (same package, shared
// binaryPath built once in TestMain in main_test.go).
//
// NOTE (red phase): main.go does not yet route the "audit" verb to
// runAudit (that wiring lands in a later, non-red phase per the ticket's
// Files to Modify list) — every test below currently observes the generic
// "cenci: unknown subcommand \"audit\"" fallback instead of real audit
// behavior, which is the intended red-phase failure.

// auditRepoDir git-inits a temp dir (skipping the test if git isn't
// available) and returns its path, for the tests that need repo scope.
func auditRepoDir(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v\n%s", err, out)
	}
	return repo
}

// auditEnv builds a minimal black-box environment for `cenci audit` runs:
// an isolated HOME and a fresh XDG_RUNTIME_DIR with no live daemon socket —
// audit must never start one (it probes read-only, unlike Launch's
// resolveCenciWiring).
func auditEnv(home, xdg string) []string {
	return append(os.Environ(),
		"HOME="+home,
		"XDG_RUNTIME_DIR="+xdg,
	)
}

func TestAudit_TextOutput_ReportsAgentAndSections(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude")
	cmd.Env = auditEnv(home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit: %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "claude") {
		t.Errorf("expected the audited agent \"claude\" reported in the text output, got:\n%s", out)
	}
	if !strings.Contains(out, "Boundary weakenings") {
		t.Errorf("expected a clearly demarcated \"Boundary weakenings\" section, got:\n%s", out)
	}
	if !strings.Contains(out, "Nested Docker") {
		t.Errorf("expected a separate \"Nested Docker (sysbox-isolated)\" section, got:\n%s", out)
	}
}

func TestAudit_JSONOutput_ParsesWithExpectedFields(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude", "--json")
	cmd.Env = auditEnv(home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit --json: %v\n%s", err, output)
	}

	var parsed map[string]any
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("cenci audit --json produced invalid JSON: %v\noutput:\n%s", err, output)
	}
	for _, key := range []string{
		"agent", "scope", "image", "workspace", "network", "dind",
		"mounts", "volumes", "env", "forwardedEnv", "credentialSources",
		"boundaryWeakenings", "reseedCreds",
	} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("cenci audit --json output missing field %q; got:\n%s", key, output)
		}
	}
}

func TestAudit_UsageErrors_Exit2(t *testing.T) {
	tests := []struct {
		name string
		args []string
		dir  func(t *testing.T) string
	}{
		{
			name: "--dind and --no-dind together",
			args: []string{"audit", "--dind", "--no-dind"},
			dir:  auditRepoDir,
		},
		{
			name: "--dind outside repo scope",
			args: []string{"audit", "--dind"},
			dir:  func(t *testing.T) string { return t.TempDir() }, // not a git repo -> legacy scope
		},
		{
			name: "unknown flag",
			args: []string{"audit", "--bogus"},
			dir:  func(t *testing.T) string { return t.TempDir() },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := tc.dir(t)

			cmd := exec.Command(binaryPath, tc.args...)
			cmd.Env = auditEnv(home, t.TempDir())
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("expected a usage error exit 2, got %T %v\n%s", err, err, output)
			}
			// Distinguishes a real "cenci audit:"-prefixed usage error from
			// the generic "cenci: unknown subcommand" fallback main.go
			// prints for an unrecognized top-level verb — both happen to
			// exit 2, so the exit code alone can't tell them apart (mirrors
			// diagnose_test.go's TestDiagnose_UsageErrors_Exit2 precedent).
			if !strings.Contains(string(output), "cenci audit:") {
				t.Errorf("expected a \"cenci audit:\"-prefixed usage error, not the generic unknown-subcommand fallback, got:\n%s", output)
			}
		})
	}
}

// TestAudit_NeverStartsADaemon pins audit's read-only contract: no live
// event socket or PID file exists under XDG_RUNTIME_DIR after the run —
// audit must probe wiring read-only and never call daemon.EnsureRunning(),
// unlike Launch's resolveCenciWiring (see sandbox_open_test.go's
// TestOpen_NoEventsSocket_LaunchesUnwiredWithWarning for the contrasting
// case where a launch DOES attempt to start the daemon).
func TestAudit_NeverStartsADaemon(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()
	xdg := t.TempDir()

	cmd := exec.Command(binaryPath, "audit")
	cmd.Env = auditEnv(home, xdg)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit: %v\n%s", err, output)
	}

	socketPath := filepath.Join(xdg, "cenci", "cenci-events.sock")
	if _, statErr := os.Stat(socketPath); statErr == nil {
		t.Errorf("cenci audit must never start the daemon; found a live event socket at %s", socketPath)
	}
	pidPath := filepath.Join(xdg, "cenci", "cenci.pid")
	if _, statErr := os.Stat(pidPath); statErr == nil {
		t.Errorf("cenci audit must never start the daemon; found a PID file at %s", pidPath)
	}
}
