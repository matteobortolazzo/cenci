package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

// -- cenci security explain (ticket #594) -----------------------------------
//
// `cenci security explain [flags]` renders the same Posture `cenci audit`
// derives (see internal/sandbox/launcher/audit.go) as a text-only,
// plain-language "why this is/isn't safe" narrative, via the new
// Posture.WriteExplanation method (internal/sandbox/launcher/explain.go).
// These black-box tests drive the real built `cenci` binary as a subprocess,
// mirroring audit_cmd_test.go's pattern (same package, shared binaryPath
// built once in TestMain in main_test.go).
//
// NOTE (red phase): main.go does not yet route the "security" verb to a
// runSecurityGroup dispatcher, and no runSecurityExplain exists (that wiring
// lands in a later, non-red phase per the ticket's Files to Modify list) —
// every test below currently observes the generic "cenci: unknown
// subcommand \"security\"" fallback instead of real security-explain
// behavior, which is the intended red-phase failure.

// securityRepoDir git-inits a temp dir (skipping the test if git isn't
// available) and returns its path, for the tests that need repo scope.
func securityRepoDir(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v\n%s", err, out)
	}
	return repo
}

// Every test below uses auditSecurityEnv (audit_security_faketest_test.go)
// for its subprocess environment: an isolated HOME and a fresh
// XDG_RUNTIME_DIR with no live daemon socket — explain reuses Audit's
// read-only posture derivation and must never start one either — plus PATH
// pinned to a fake docker/podman (writeAuditFakeRuntimes) whose FAKE_PS
// defaults to empty (no running container). Ticket #627's
// NewForAuditWithRuntime resolves a real runtime via
// internal/sandbox.ContainerRuntime(), so every pre-existing hermetic test in
// this file must pin PATH the same way (watch/docs/test-strategy.md #620).
func TestSecurityExplain_TextOutput_ReportsFramingAndWeakenings(t *testing.T) {
	repo := securityRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "security", "explain", "--agent", "claude")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci security explain: %v\n%s", err, output)
	}
	out := string(output)
	if !strings.Contains(out, "boundary") {
		t.Errorf("expected threat-model framing naming the container as the security boundary, got:\n%s", out)
	}
	if !strings.Contains(out, "Boundary weakenings") {
		t.Errorf("expected a \"Boundary weakenings\" section, got:\n%s", out)
	}
}

// TestSecurityExplain_CodexCredsPresentButNotApplicable_NotNarratedAsStaged
// is the #598 command-level acceptance-criteria assertion: a claude
// `security explain` with Codex auth present on the host must NOT narrate
// codex credentials as bind-mounted/copied/staged — the mount plan never
// stages codex creds for a non-codex agent.
func TestSecurityExplain_CodexCredsPresentButNotApplicable_NotNarratedAsStaged(t *testing.T) {
	repo := securityRepoDir(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"token":"sentinel"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cmd := exec.Command(binaryPath, "security", "explain", "--agent", "claude")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci security explain: %v\n%s", err, output)
	}
	out := string(output)

	credIdx := strings.Index(out, "Credential sources:")
	forwardedIdx := strings.Index(out, "Forwarded exec env")
	if credIdx == -1 || forwardedIdx == -1 || forwardedIdx <= credIdx {
		t.Fatalf("expected a \"Credential sources:\" section before \"Forwarded exec env\", got:\n%s", out)
	}
	credSection := out[credIdx:forwardedIdx]

	if !strings.Contains(credSection, "codex") {
		t.Errorf("expected codex credentials named in the narrative, got:\n%s", credSection)
	}
	if strings.Contains(credSection, "bind-mounted") || strings.Contains(credSection, "copied") {
		t.Errorf("expected a claude security explain with codex creds present to NOT narrate codex as staged/mounted, got:\n%s", credSection)
	}
}

func TestSecurityExplain_UsageErrors_Exit2(t *testing.T) {
	tests := []struct {
		name string
		args []string
		dir  func(t *testing.T) string
	}{
		{
			name: "--dind and --no-dind together",
			args: []string{"security", "explain", "--dind", "--no-dind"},
			dir:  securityRepoDir,
		},
		{
			name: "--dind outside repo scope",
			args: []string{"security", "explain", "--dind"},
			dir:  func(t *testing.T) string { return t.TempDir() }, // not a git repo -> legacy scope
		},
		{
			name: "unknown flag",
			args: []string{"security", "explain", "--bogus"},
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
			// Distinguishes a real "cenci security explain:"-prefixed usage
			// error from the generic "cenci: unknown subcommand" fallback
			// main.go prints for an unrecognized top-level verb — both
			// happen to exit 2, so the exit code alone can't tell them apart
			// (mirrors audit_cmd_test.go's TestAudit_UsageErrors_Exit2
			// precedent).
			if !strings.Contains(string(output), "cenci security explain:") {
				t.Errorf("expected a \"cenci security explain:\"-prefixed usage error, not the generic unknown-subcommand fallback, got:\n%s", output)
			}
		})
	}
}

// TestSecurity_UnknownSubverb_Exit2 covers `cenci security bogus`: an
// unrecognized subverb of the "security" group must exit 2 with a
// "cenci security:"-prefixed message, not the generic top-level
// unknown-subcommand fallback.
func TestSecurity_UnknownSubverb_Exit2(t *testing.T) {
	cmd := exec.Command(binaryPath, "security", "bogus")
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected a usage error exit 2, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "cenci security:") {
		t.Errorf("expected a \"cenci security:\"-prefixed unknown-subcommand error, got:\n%s", output)
	}
}

// TestSecurity_BareInvocation_Exit2WithSubcommandHint covers bare `cenci
// security` (no subcommand): it must exit 2 with a hint listing the
// available subcommands, including "explain" — mirrors
// runSandboxGroup's "expected a subcommand: ..." precedent
// (sandbox_cmd.go).
func TestSecurity_BareInvocation_Exit2WithSubcommandHint(t *testing.T) {
	cmd := exec.Command(binaryPath, "security")
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected a usage error exit 2, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "explain") {
		t.Errorf("expected a subcommand hint listing \"explain\", got:\n%s", output)
	}
}

// TestSecurityExplain_NeverStartsADaemon mirrors audit_cmd_test.go's
// TestAudit_NeverStartsADaemon: no live event socket or PID file exists
// under XDG_RUNTIME_DIR after the run — security explain reuses Audit's
// read-only posture derivation and must never call daemon.EnsureRunning()
// either.
func TestSecurityExplain_NeverStartsADaemon(t *testing.T) {
	repo := securityRepoDir(t)
	home := t.TempDir()
	xdg := t.TempDir()

	cmd := exec.Command(binaryPath, "security", "explain")
	cmd.Env = auditSecurityEnv(t, home, xdg)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci security explain: %v\n%s", err, output)
	}

	socketPath := filepath.Join(xdg, "cenci", "cenci-events.sock")
	if _, statErr := os.Stat(socketPath); statErr == nil {
		t.Errorf("cenci security explain must never start the daemon; found a live event socket at %s", socketPath)
	}
	pidPath := filepath.Join(xdg, "cenci", "cenci.pid")
	if _, statErr := os.Stat(pidPath); statErr == nil {
		t.Errorf("cenci security explain must never start the daemon; found a PID file at %s", pidPath)
	}
}

// -- ticket #627: observed vs planned narrative (command level) -------------
//
// NOTE (red phase): security_cmd.go does not yet swap to
// launcher.NewForAuditWithRuntime, and internal/sandbox/launcher does not
// yet render basis/inspectWarning/the corrected network wording — every
// test below currently observes the CURRENT command's output instead. That
// is the intended red-phase state.

// TestSecurityExplain_TextOutput_StatesPlannedByDefault_NoLiveRuntime
// covers AC #5's planned side at the command level: with the fake runtime
// reporting nothing running, `cenci security explain` must state it is
// narrating a plan, not observed state.
func TestSecurityExplain_TextOutput_StatesPlannedByDefault_NoLiveRuntime(t *testing.T) {
	repo := securityRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "security", "explain", "--agent", "claude")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci security explain: %v\n%s", err, output)
	}
	out := strings.ToLower(string(output))
	// A multi-word phrase (not a bare "plan"/"observed" substring) so this
	// assertion can never accidentally match text unrelated to basis framing
	// — including, notably, the test's own t.TempDir()-derived HOME path
	// embedded in the narrative's credential-source lines, which contains
	// this test's function name (and therefore the substring "planned").
	if !strings.Contains(out, "describing a plan") && !strings.Contains(out, "describes a plan") {
		t.Errorf("expected the narrative to state it is describing a plan (no running scoped container), got:\n%s", output)
	}
}

// TestSecurityExplain_TextOutput_RunningContainer_StatesObserved covers
// AC #5's observed side: with a running scoped container, `cenci security
// explain` must state it is narrating observed state.
func TestSecurityExplain_TextOutput_RunningContainer_StatesObserved(t *testing.T) {
	repo := securityRepoDir(t)
	home := t.TempDir()
	scope := launcher.ComputeScope("claude", "", repo, home)

	t.Setenv("FAKE_PS", scope.ContainerName+"\n")

	cmd, _ := auditFakeRuntimeCmd(t, repo, home, "security", "explain", "--agent", "claude")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci security explain: %v\n%s", err, output)
	}
	out := strings.ToLower(string(output))
	// Multi-word phrase for the same reason as the planned-side test above:
	// this test's own name/HOME tempdir path contains the substring
	// "Observed", which a bare strings.Contains(out, "observed") would
	// accidentally match regardless of the narrative's actual content.
	if !strings.Contains(out, "observed running") {
		t.Errorf("expected the narrative to state it is describing observed running state, got:\n%s", output)
	}
}

// TestSecurityExplain_BridgeNetwork_FullSentenceNarrative covers AC #6/#7 at
// the command level: separate network namespace, no published inbound
// ports, and outbound reachability to host/LAN/internet as complete
// sentences (not isolated keywords).
func TestSecurityExplain_BridgeNetwork_FullSentenceNarrative(t *testing.T) {
	repo := securityRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "security", "explain", "--agent", "claude")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci security explain: %v\n%s", err, output)
	}
	out := string(output)

	if !strings.Contains(out, "separate network namespace") {
		t.Errorf("expected the bridge narrative to state a separate network namespace, got:\n%s", out)
	}
	if !strings.Contains(out, "no published inbound ports") && !strings.Contains(out, "publishes no inbound ports") {
		t.Errorf("expected the bridge narrative to state no published inbound ports, got:\n%s", out)
	}
	if !strings.Contains(out, "LAN") {
		t.Errorf("expected the bridge narrative to name LAN reachability explicitly, got:\n%s", out)
	}
	if strings.Contains(out, "outbound-only and isolated from the host") {
		t.Errorf("bridge narrative must never claim complete host isolation / universal outbound-only enforcement, got:\n%s", out)
	}
}

// TestSecurityExplain_HostNetwork_BlastRadiusFullSentence covers AC #8 at
// the command level: --host-network output must continue to identify the
// increased blast radius clearly, including the shared-namespace and
// host-localhost-exposure wording.
func TestSecurityExplain_HostNetwork_BlastRadiusFullSentence(t *testing.T) {
	repo := securityRepoDir(t)
	home := t.TempDir()

	cmd := exec.Command(binaryPath, "security", "explain", "--agent", "claude", "--host-network")
	cmd.Env = auditSecurityEnv(t, home, t.TempDir())
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci security explain --host-network: %v\n%s", err, output)
	}
	out := string(output)

	if !strings.Contains(out, "blast radius") {
		t.Errorf("expected the host-network narrative to keep the blast-radius warning, got:\n%s", out)
	}
	if !strings.Contains(out, "localhost") {
		t.Errorf("expected the host-network narrative to state host-localhost exposure, got:\n%s", out)
	}
	if !strings.Contains(out, "namespace separation") {
		t.Errorf("expected the host-network narrative to state the loss of namespace separation, got:\n%s", out)
	}
}

// TestSecurityExplain_MalformedInspect_WarningAndPlanNoBaseline_Exit0 covers
// Q2/AC #9 at the command level: `cenci security explain` must never
// collapse an inspect failure into the reassuring default-safe baseline,
// and must still exit 0.
func TestSecurityExplain_MalformedInspect_WarningAndPlanNoBaseline_Exit0(t *testing.T) {
	repo := securityRepoDir(t)
	home := t.TempDir()
	scope := launcher.ComputeScope("claude", "", repo, home)

	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "not-the-expected-shape\n")

	cmd, _ := auditFakeRuntimeCmd(t, repo, home, "security", "explain", "--agent", "claude")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cenci security explain (malformed inspect) must exit 0, got %v\n%s", err, output)
	}
	out := string(output)
	if strings.Contains(out, "Boundary weakenings: none (default-safe baseline)") {
		t.Errorf("output claims the default-safe baseline despite an inspect failure, got:\n%s", out)
	}
	// A multi-word phrase (not a bare "warning" substring): this test's own
	// HOME tempdir path (embedded in the narrative's credential-source
	// lines) contains the test function's name, which itself contains
	// "Warning" — a bare substring check would accidentally match that path
	// text regardless of whether the command actually rendered a warning.
	if !strings.Contains(strings.ToLower(out), "could not be") {
		t.Errorf("expected a visible warning explaining the running container's posture could not be verified, got:\n%s", out)
	}
}
