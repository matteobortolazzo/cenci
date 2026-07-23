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

// TestAudit_JSONOutput_HasProbeApplicableStagedFields covers the new #598
// credentialSources fields at the command level: every credentialSources
// entry must carry "probe"/"applicable"/"staged" keys alongside the
// existing "present".
func TestAudit_JSONOutput_HasProbeApplicableStagedFields(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude", "--json")
	cmd.Env = auditEnv(home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit --json: %v\n%s", err, output)
	}
	out := string(output)

	for _, key := range []string{`"probe"`, `"applicable"`, `"staged"`} {
		if !strings.Contains(out, key) {
			t.Errorf("cenci audit --json output missing expected credentialSources field %s; got:\n%s", key, out)
		}
	}

	var parsed struct {
		CredentialSources []struct {
			Type       string `json:"type"`
			Present    bool   `json:"present"`
			Probe      string `json:"probe"`
			Applicable bool   `json:"applicable"`
			Staged     bool   `json:"staged"`
		} `json:"credentialSources"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("cenci audit --json produced invalid JSON: %v\noutput:\n%s", err, output)
	}
	if len(parsed.CredentialSources) == 0 {
		t.Fatalf("expected at least one credentialSources entry, got none; output:\n%s", output)
	}
	for _, src := range parsed.CredentialSources {
		if src.Probe == "" {
			t.Errorf("credentialSources[%s].probe is empty, want a non-empty probe state", src.Type)
		}
	}
}

// TestAudit_JSONOutput_EnvSecretFlagsContextKeyTrue covers create-time env
// names getting the same explicit secret classification as forwarded exec
// env (#598): CONTEXT7_API_KEY must be reported with "secret":true, and its
// VALUE must never appear anywhere in the JSON output.
func TestAudit_JSONOutput_EnvSecretFlagsContextKeyTrue(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()
	const contextSecret = "sentinel-secret-value-cmdjson1"
	t.Setenv("CONTEXT7_API_KEY", contextSecret)

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude", "--json")
	cmd.Env = auditEnv(home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit --json: %v\n%s", err, output)
	}

	var parsed struct {
		Env []struct {
			Name   string `json:"name"`
			Secret bool   `json:"secret"`
		} `json:"env"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("cenci audit --json produced invalid JSON: %v\noutput:\n%s", err, output)
	}

	var contextEnv *struct {
		Name   string `json:"name"`
		Secret bool   `json:"secret"`
	}
	for i := range parsed.Env {
		if parsed.Env[i].Name == "CONTEXT7_API_KEY" {
			contextEnv = &parsed.Env[i]
		}
	}
	if contextEnv == nil {
		t.Fatalf("expected CONTEXT7_API_KEY in env, got %+v", parsed.Env)
	}
	if !contextEnv.Secret {
		t.Errorf("env CONTEXT7_API_KEY secret = false, want true")
	}
	if strings.Contains(string(output), contextSecret) {
		t.Errorf("cenci audit --json leaked the CONTEXT7_API_KEY sentinel value:\n%s", output)
	}
}

// TestAudit_JSONOutput_CrossAgentCredential_PresentNotApplicableNotStaged is
// the command-level cross-agent scenario from the #598 plan: a claude audit
// with codex auth present on the host must report codex as present:true but
// applicable:false and staged:false.
func TestAudit_JSONOutput_CrossAgentCredential_PresentNotApplicableNotStaged(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"token":"sentinel"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude", "--json")
	cmd.Env = auditEnv(home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit --json: %v\n%s", err, output)
	}

	var parsed struct {
		CredentialSources []struct {
			Type       string `json:"type"`
			Present    bool   `json:"present"`
			Probe      string `json:"probe"`
			Applicable bool   `json:"applicable"`
			Staged     bool   `json:"staged"`
		} `json:"credentialSources"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("cenci audit --json produced invalid JSON: %v\noutput:\n%s", err, output)
	}

	var codex *struct {
		Type       string `json:"type"`
		Present    bool   `json:"present"`
		Probe      string `json:"probe"`
		Applicable bool   `json:"applicable"`
		Staged     bool   `json:"staged"`
	}
	for i := range parsed.CredentialSources {
		if parsed.CredentialSources[i].Type == "codex" {
			codex = &parsed.CredentialSources[i]
		}
	}
	if codex == nil {
		t.Fatalf("expected a codex credentialSources entry, got %+v", parsed.CredentialSources)
	}
	// Probe must be non-empty (e.g. "present") -- this is what forces the
	// present/applicable/staged assertions below to actually exercise the new
	// schema instead of vacuously passing on the current schema's zero-value
	// defaults for fields that don't exist yet.
	if codex.Probe == "" {
		t.Errorf("codex credentialSources.probe is empty, want a non-empty probe state (e.g. %q)", "present")
	}
	if !codex.Present {
		t.Errorf("codex credentialSources.present = false, want true (auth.json exists on host)")
	}
	if codex.Applicable {
		t.Errorf("codex credentialSources.applicable = true for a claude audit, want false")
	}
	if codex.Staged {
		t.Errorf("codex credentialSources.staged = true for a claude audit, want false (not applicable)")
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
