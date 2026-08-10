package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
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

// Every test below uses auditSecurityEnv (audit_security_faketest_test.go)
// for its subprocess environment: an isolated HOME and a fresh
// XDG_RUNTIME_DIR with no live daemon socket — audit must never start one
// (it probes read-only, unlike Launch's resolveCenciWiring) — plus PATH
// pinned to a fake docker/podman (writeAuditFakeRuntimes) whose FAKE_PS
// defaults to empty (no running container). Ticket #627's
// NewForAuditWithRuntime resolves a real runtime via
// internal/sandbox.ContainerRuntime(), so every pre-existing hermetic test in
// this file must pin PATH the same way — otherwise a host with a real
// docker/podman installed (daemon up or down) could perturb these
// exact-output assertions (watch/docs/test-strategy.md #620).
func TestAudit_TextOutput_ReportsAgentAndSections(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
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
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
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
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
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
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
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
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
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
			cmd.Env = auditSecurityEnv(t, home, t.TempDir())
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

// TestAudit_MalformedDindConfig_Exits1 mirrors the real-launch and dry-run
// #632 hard-fail (sandbox_open_test.go's TestOpen_MalformedDindConfig_Exits1
// and open_dryrun_test.go's TestOpenDryRun_MalformedDindConfig_Exits1) for
// `cenci audit`: per Q2, audit hard-fails on malformed stored config exactly
// like launch/dry-run, rather than degrading it to a warning finding.
func TestAudit_MalformedDindConfig_Exits1(t *testing.T) {
	repoRoot := malformedDindRepoEnv(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected malformed .cenci/config.json to exit 1 (hard fail, not a usage error), got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "config.json") {
		t.Errorf("expected a path-bearing error naming config.json, got:\n%s", output)
	}
}

// TestAuditNoDind_SucceedsDespiteMalformedConfig pinned --no-dind as a
// config-free escape hatch for `cenci audit` too pre-#1002 (#632). #1002
// narrows it the same way as the launch/dry-run cases: Audit now ALSO
// resolves sandbox.plugins unconditionally after resolveDindForHost, so the
// same corrupt config.json --no-dind used to route around now hard-fails the
// plugins read instead.
func TestAuditNoDind_SucceedsDespiteMalformedConfig(t *testing.T) {
	repoRoot := malformedDindRepoEnv(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude", "--no-dind")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected audit --no-dind with a malformed .cenci/config.json to exit 1 (the plugins read still hard-fails on it, #1002), got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "config.json") {
		t.Errorf("expected a path-bearing error naming config.json, got:\n%s", output)
	}
}

// TestAudit_MalformedSandboxPluginsConfig_Exits1 pins RepoSandboxPlugins'
// #632-mirroring hard-fail reaching `cenci audit`: a well-formed JSON
// document whose "sandbox.plugins" value is outside the closed set must
// hard-fail (exit 1) with a path-bearing, non-usage error naming the
// offending value, exactly like the dind-config hard-fail above.
func TestAudit_MalformedSandboxPluginsConfig_Exits1(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".cenci"), 0o755); err != nil {
		t.Fatalf("MkdirAll .cenci: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".cenci", "config.json"), []byte(`{"sandbox":{"plugins":["cenci","bogus-plugin"]}}`), 0o644); err != nil {
		t.Fatalf("write .cenci/config.json: %v", err)
	}

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected an unrecognized sandbox.plugins value to exit 1 (hard fail, not a usage error), got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "config.json") {
		t.Errorf("expected a path-bearing error naming config.json, got:\n%s", output)
	}
	if !strings.Contains(string(output), "bogus-plugin") {
		t.Errorf("expected the error to name the offending value \"bogus-plugin\", got:\n%s", output)
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
	cmd.Env = auditSecurityEnv(t, home, xdg)
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

// -- ticket #627: observed vs planned posture (command level) ---------------
//
// NOTE (red phase): main.go/audit_cmd.go do not yet swap to
// launcher.NewForAuditWithRuntime, and internal/sandbox/launcher does not
// yet expose Basis/InspectWarning/observed derivation — every test below
// currently observes the CURRENT command's always-planned, no-basis JSON/
// text output instead of the behavior asserted here. That is the intended
// red-phase state.

// TestAudit_JSONOutput_BasisPlannedByDefault_NoLiveRuntime covers the
// default hermetic case: with the fake runtime reporting nothing running,
// `cenci audit` must report basis:"planned" and no inspectWarning.
func TestAudit_JSONOutput_BasisPlannedByDefault_NoLiveRuntime(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "audit", "--agent", "claude", "--json")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit --json: %v\n%s", err, output)
	}
	out := string(output)
	// `cenci audit --json` renders via json.MarshalIndent (unmodified by this
	// ticket), which always inserts a space after the colon — a compact
	// `"basis":"planned"` substring (no space) would never match this
	// command's actual indented output; every other JSON assertion in this
	// file either checks a bare key name or unmarshals into a struct rather
	// than a compact key:value substring, so this is a corrected test-authoring
	// bug, not a production-code/formatting change.
	if !strings.Contains(out, `"basis": "planned"`) {
		t.Errorf("expected \"basis\": \"planned\" with no running scoped container, got:\n%s", out)
	}
	if strings.Contains(out, `"inspectWarning"`) {
		t.Errorf("expected no \"inspectWarning\" field when inspection never failed, got:\n%s", out)
	}
}

// TestAudit_JSONOutput_RunningHostNetworkContainer_BasisRunningAndWeakened
// covers AC #1 at the command level: a running host-network container must
// be reported as weakened without the caller repeating --host-network.
func TestAudit_JSONOutput_RunningHostNetworkContainer_BasisRunningAndWeakened(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()
	scope := launcher.ComputeScope("claude", "", repo, home)

	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "cenci-sandbox:latest|host|runc||\n\n")

	cmd, _ := auditFakeRuntimeCmd(t, repo, home, "audit", "--agent", "claude", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit --json: %v\n%s", err, output)
	}

	var parsed struct {
		Basis   string `json:"basis"`
		Network struct {
			Mode     string `json:"mode"`
			Weakened bool   `json:"weakened"`
		} `json:"network"`
		BoundaryWeakenings []struct {
			Option string `json:"option"`
		} `json:"boundaryWeakenings"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("cenci audit --json produced invalid JSON: %v\noutput:\n%s", err, output)
	}
	if parsed.Basis != "running" {
		t.Errorf("basis = %q, want %q", parsed.Basis, "running")
	}
	if parsed.Network.Mode != "host" || !parsed.Network.Weakened {
		t.Errorf("network = %+v, want host/weakened", parsed.Network)
	}
	found := false
	for _, w := range parsed.BoundaryWeakenings {
		if w.Option == "--host-network" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a --host-network boundary weakening reported automatically (no --host-network flag was passed), got %+v", parsed.BoundaryWeakenings)
	}
}

// TestAudit_JSONOutput_MalformedInspect_InspectWarningPlannedExit0 covers
// Q2/AC #9/#11 at the command level: a running container whose combined
// inspect probe returns unparsable output must still exit 0 and render
// basis:"planned" with a non-empty inspectWarning — never a hard failure,
// never a silent collapse to the default-safe baseline.
func TestAudit_JSONOutput_MalformedInspect_InspectWarningPlannedExit0(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()
	scope := launcher.ComputeScope("claude", "", repo, home)

	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "not-the-expected-shape\n")

	cmd, _ := auditFakeRuntimeCmd(t, repo, home, "audit", "--agent", "claude", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit --json (malformed inspect) must exit 0, got %v\n%s", err, output)
	}

	var parsed struct {
		Basis          string `json:"basis"`
		InspectWarning string `json:"inspectWarning"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("cenci audit --json produced invalid JSON: %v\noutput:\n%s", err, output)
	}
	if parsed.Basis != "planned" {
		t.Errorf("basis = %q, want %q on inspect failure (never collapse to running)", parsed.Basis, "planned")
	}
	if parsed.InspectWarning == "" {
		t.Errorf("inspectWarning is empty, want a non-empty warning on malformed inspect output")
	}
	if strings.Contains(string(output), "default-safe baseline") {
		t.Errorf("output claims the default-safe baseline despite an inspect failure, got:\n%s", output)
	}
}

// TestAudit_JSONOutput_InspectExecFailure_InspectWarningPlannedExit0 covers
// ticket #681 at the command level: the container disappearing between `ps`
// and `inspect` (the combined observed-inspect probe's EXEC itself failing,
// not a parse failure) must still exit 0 and render basis:"planned" with a
// non-empty inspectWarning — never a hard failure, never a silent collapse
// to the default-safe baseline. This exercises FAKE_OBSERVED_POSTURE_EXIT,
// which writeAuditFakeRuntime (audit_security_faketest_test.go:58) already
// honours — no fake change needed here.
func TestAudit_JSONOutput_InspectExecFailure_InspectWarningPlannedExit0(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()
	scope := launcher.ComputeScope("claude", "", repo, home)

	// t.Setenv must precede auditFakeRuntimeCmd: cmd.Env snapshots
	// os.Environ() at construction time.
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE_EXIT", "1")

	cmd, _ := auditFakeRuntimeCmd(t, repo, home, "audit", "--agent", "claude", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit --json (inspect exec failure) must exit 0, got %v\n%s", err, output)
	}

	var parsed struct {
		Basis          string `json:"basis"`
		InspectWarning string `json:"inspectWarning"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("cenci audit --json produced invalid JSON: %v\noutput:\n%s", err, output)
	}
	if parsed.Basis != "planned" {
		t.Errorf("basis = %q, want %q on inspect exec failure (never collapse to running)", parsed.Basis, "planned")
	}
	if parsed.InspectWarning == "" {
		t.Errorf("inspectWarning is empty, want a non-empty warning on inspect exec failure")
	}
	if strings.Contains(string(output), "default-safe baseline") {
		t.Errorf("output claims the default-safe baseline despite an inspect exec failure, got:\n%s", output)
	}
}

// TestAudit_TextOutput_PsUnreachable_InspectWarningPlannedExit0 covers
// AC #9's ps/daemon-unreachable case at the command level: exit 0, a
// visible warning, no default-safe baseline claim.
func TestAudit_TextOutput_PsUnreachable_InspectWarningPlannedExit0(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()

	t.Setenv("FAKE_PS_EXIT", "1")

	cmd, _ := auditFakeRuntimeCmd(t, repo, home, "audit", "--agent", "claude")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit (ps/daemon unreachable) must exit 0, got %v\n%s", err, output)
	}
	out := string(output)
	if strings.Contains(out, "default-safe baseline") {
		t.Errorf("output claims the default-safe baseline despite a ps/daemon failure, got:\n%s", out)
	}
	// A multi-word phrase (not a bare "warning" substring): this test's own
	// HOME tempdir path (embedded in the narrative's credential-source
	// lines) contains the test function's name, which itself contains
	// "InspectWarning" — a bare substring check would accidentally match
	// that path text regardless of whether the command actually rendered a
	// warning.
	if !strings.Contains(strings.ToLower(out), "could not be") {
		t.Errorf("expected a visible warning explaining the running container's posture could not be verified, got:\n%s", out)
	}
}

// TestAudit_NoMutation_CallLogOnlyPsAndInspect is the command-level
// counterpart of internal/sandbox/launcher/audit_test.go's
// TestAudit_ObservedMode_NoMutation_CallLogOnlyPsAndInspect: with a running
// scoped container, `cenci audit` must issue only read-only ps/inspect
// calls, never a mutating verb.
func TestAudit_NoMutation_CallLogOnlyPsAndInspect(t *testing.T) {
	repo := auditRepoDir(t)
	home := t.TempDir()
	scope := launcher.ComputeScope("claude", "", repo, home)

	t.Setenv("FAKE_PS", scope.ContainerName+"\n")

	cmd, callLog := auditFakeRuntimeCmd(t, repo, home, "audit", "--agent", "claude")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci audit: %v\n%s", err, output)
	}

	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		t.Fatal("expected at least a ps call in the call log, got none")
	}
	for _, line := range strings.Split(trimmed, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if verb := fields[0]; verb != "ps" && verb != "inspect" {
			t.Errorf("audit issued a non-read-only runtime call %q; want only ps/inspect, full log:\n%s", line, data)
		}
	}
}
