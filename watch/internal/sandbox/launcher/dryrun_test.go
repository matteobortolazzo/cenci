package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- cenci open --dry-run (ticket #589) ----------------------------------
//
// `cenci open --dry-run` prints the exact, redacted docker/podman argv the
// launcher would run for a real launch -- the detached container-create
// command and the interactive agent-attach command -- followed by the full
// `cenci audit` Posture breakdown, without creating any container, volume,
// or network (see internal/sandbox/launcher/dryrun.go's planned DryRun/
// DryRunPlan/WriteText). DryRun must be a faithful mirror of Launch's own
// failure modes (agent validation, ResolveDind, runtime resolution, dind
// preflight, credential validation) rather than a best-effort argv, and its
// argv must come from the exact same construction helpers Launch/runAgent
// use -- never a parallel/duplicate command-building path.
//
// These are integration-style tests against the real (planned) DryRun
// method and WriteText renderer, with a temp HOME and (where repo scope
// matters) a temp git repo -- mirroring audit_test.go's hermetic pattern.
// newAuditTestRepo and writeFile are shared helpers already defined in
// audit_test.go/basetag_test.go (same package); reused here rather than
// duplicated.
//
// NOTE (red phase): DryRun, DryRunPlan, and WriteText do not exist yet --
// they are added by dryrun.go in the next, non-red phase. Every reference to
// them below is therefore a compile error until that lands; that is the
// intended red-phase state, not a bug to fix by stubbing the implementation
// in this file.

// dryRunEngine returns a minimal Engine suitable for calling DryRun, with
// Runtime preset to "docker" (hermetic, like launch_test.go's
// TestAssembleRunArgs_NoCreationTimeTmuxPane) so DryRun never needs to probe
// the host PATH for a real container runtime during runtime resolution.
func dryRunEngine() *Engine {
	return &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
}

// -- create argv content --------------------------------------------------

// TestDryRun_CreateArgv_ContainsRunNameWorkspaceRwAgentCliRoAndCredsRo pins
// the container-create argv's headline content: a leading "run --name ..."
// invocation, the read-write workspace bind mount, the read-only shared
// agent-CLI volume mount, and a read-only staged credential mount -- so a
// reader can tell ro from rw at a glance, per the ticket's AC.
func TestDryRun_CreateArgv_ContainsRunNameWorkspaceRwAgentCliRoAndCredsRo(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"token":"unused-in-this-test"}`)

	plan, err := dryRunEngine().DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	joined := strings.Join(plan.CreateArgv, " ")
	if !strings.HasPrefix(joined, "run --name ") {
		t.Errorf("CreateArgv = %q, want a leading \"run --name \" container-creation argv", joined)
	}
	wantWorkspace := "-v " + repo + ":/workspace"
	if !strings.Contains(joined, wantWorkspace) {
		t.Errorf("CreateArgv missing the read-write workspace mount %q, got:\n%s", wantWorkspace, joined)
	}
	if strings.Contains(joined, repo+":/workspace:ro") {
		t.Errorf("CreateArgv marked the workspace mount read-only; want read-write:\n%s", joined)
	}
	if !strings.Contains(joined, "/opt/cenci-agent:ro") {
		t.Errorf("CreateArgv missing the read-only shared agent CLI mount, got:\n%s", joined)
	}
	wantCreds := home + "/.claude/.credentials.json:/tmp/host-claude-creds/.credentials.json:ro"
	if !strings.Contains(joined, wantCreds) {
		t.Errorf("CreateArgv missing the read-only claude credential mount %q, got:\n%s", wantCreds, joined)
	}
}

// -- attach argv content ---------------------------------------------------

// TestDryRun_AttachArgv_ContainsExecAgentFlagsAndForwardedArgs pins the
// interactive agent-attach argv: a leading "exec -it ..." invocation, the
// resolved agent binary with its per-agent flags (--dangerously-skip-
// permissions --model sonnet for claude's defaults), and opts.AgentArgs
// appended verbatim after them (the trailing "--" forwarded-args section).
func TestDryRun_AttachArgv_ContainsExecAgentFlagsAndForwardedArgs(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	plan, err := dryRunEngine().DryRun(Options{Agent: "claude", AgentArgs: []string{"--resume"}})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	joined := strings.Join(plan.AttachArgv, " ")
	if !strings.HasPrefix(joined, "exec -it ") {
		t.Errorf("AttachArgv = %q, want a leading \"exec -it \" interactive-attach argv", joined)
	}
	wantTail := "/opt/cenci-agent/current/node_modules/.bin/claude --dangerously-skip-permissions --model sonnet --resume"
	if !strings.HasSuffix(joined, wantTail) {
		t.Errorf("AttachArgv = %q, want a trailing %q (agent flags + forwarded \"--resume\")", joined, wantTail)
	}
}

// -- secret redaction (mirrors TestAudit_SecretLeakRegression) -------------

// TestDryRun_SecretLeakRegression_NeverEmitsForwardedSecretValues mirrors
// audit_test.go's TestAudit_SecretLeakRegression_NeverEmitsSecretValues: it
// seeds real secret VALUES for the forwarded provider keys (never just
// checking for non-empty output), then asserts WriteText's rendered argv
// masks the value while keeping the token structurally visible
// ("CONTEXT7_API_KEY=<redacted>") and never leaks either sentinel value
// anywhere in the printed output.
func TestDryRun_SecretLeakRegression_NeverEmitsForwardedSecretValues(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	const contextSecret = "sentinel-secret-value-xyz123"
	const openaiSecret = "sentinel-secret-value-openai456"
	t.Setenv("CONTEXT7_API_KEY", contextSecret)
	t.Setenv("OPENAI_API_KEY", openaiSecret) // also satisfies codex's credential check below

	plan, err := dryRunEngine().DryRun(Options{Agent: "codex"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	var buf bytes.Buffer
	if err := plan.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "CONTEXT7_API_KEY=<redacted>") {
		t.Errorf("expected the redacted CONTEXT7_API_KEY marker in WriteText output, got:\n%s", out)
	}
	if strings.Contains(out, contextSecret) {
		t.Errorf("WriteText output leaks the CONTEXT7_API_KEY sentinel value:\n%s", out)
	}
	if strings.Contains(out, openaiSecret) {
		t.Errorf("WriteText output leaks the OPENAI_API_KEY sentinel value:\n%s", out)
	}
}

// -- full Posture body ------------------------------------------------------

// TestDryRun_Posture_FullBodyPresentInWriteText asserts anchor strings from
// every section of the full cenci audit Posture body (mounts, boundary
// weakenings, forwarded exec env), proving DryRun's WriteText reuses
// Posture.WriteText verbatim rather than a trimmed five-category summary
// (confirmed Q&A planning decision Q3).
func TestDryRun_Posture_FullBodyPresentInWriteText(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	plan, err := dryRunEngine().DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	var buf bytes.Buffer
	if err := plan.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"Mounts:", "Boundary weakenings", "Forwarded exec env"} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteText output missing the full Posture body's %q section, got:\n%s", want, out)
		}
	}
}

// -- error-path mirroring (content-specific, per watch/AGENTS.md #446) ------

// TestDryRun_CodexNoAuth_ReturnsNonUsageErrorMentioningCodexAuth covers
// DryRun's faithful mirror of Launch's hard credential-validation failure: a
// codex launch with no auth staged/forwarded is an exit-1 (non-usage)
// error, content-specifically distinguished from the exit-2 usage-error
// class covered by the other error-path tests below.
func TestDryRun_CodexNoAuth_ReturnsNonUsageErrorMentioningCodexAuth(t *testing.T) {
	cwd := t.TempDir() // not a git repo; dind is irrelevant to this failure
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)

	_, err := dryRunEngine().DryRun(Options{Agent: "codex"})
	if err == nil {
		t.Fatal("DryRun with no codex auth staged = nil error, want the hard credential-validation failure Launch would also return")
	}
	if !strings.Contains(err.Error(), "requires Codex auth") {
		t.Errorf("DryRun error = %q, want it to mention \"requires Codex auth\"", err.Error())
	}
	if IsUsage(err) {
		t.Errorf("DryRun codex-no-auth error classified as a usage error (exit 2); want the exit-1 credential-failure class")
	}
}

// TestDryRun_DindAndNoDind_ReturnsUsageErrorCannotBeCombined covers the
// --dind/--no-dind conflict: a usage error (exit 2), content-specifically
// distinguished from the other error classes below.
func TestDryRun_DindAndNoDind_ReturnsUsageErrorCannotBeCombined(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	_, err := dryRunEngine().DryRun(Options{Agent: "claude", Dind: true, NoDind: true})
	if err == nil || !IsUsage(err) {
		t.Fatalf("DryRun with --dind --no-dind together = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("DryRun error = %q, want it to mention \"cannot be combined\"", err.Error())
	}
}

// TestDryRun_DindInLegacyScope_ReturnsUsageErrorRequiresRepoScope covers
// --dind requested outside repo scope: a usage error (exit 2),
// content-specifically distinguished from the --dind/--no-dind conflict
// above and the podman-outer-runtime failure below.
func TestDryRun_DindInLegacyScope_ReturnsUsageErrorRequiresRepoScope(t *testing.T) {
	cwd := t.TempDir() // not a git repo -> legacy scope
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)

	_, err := dryRunEngine().DryRun(Options{Agent: "claude", Dind: true})
	if err == nil || !IsUsage(err) {
		t.Fatalf("DryRun with --dind in legacy scope = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "requires repo scope") {
		t.Errorf("DryRun error = %q, want it to mention \"requires repo scope\"", err.Error())
	}
}

// TestDryRun_DindWithPodmanRuntime_ReturnsNonUsageErrorRequiresDocker
// deterministically exercises dindPreflight without needing a real sysbox
// install: presetting Runtime to "podman" (rather than resolving it from
// PATH) makes the "dind requires Docker as the outer runtime" failure exit-1
// (not a usage error) and content-specifically distinct from the two usage
// errors above.
func TestDryRun_DindWithPodmanRuntime_ReturnsNonUsageErrorRequiresDocker(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	eng := &Engine{Runtime: "podman", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	_, err := eng.DryRun(Options{Agent: "claude", Dind: true})
	if err == nil {
		t.Fatal("DryRun with --dind and a podman runtime = nil error, want the dindPreflight failure")
	}
	if IsUsage(err) {
		t.Errorf("DryRun dind/podman-outer-runtime error classified as a usage error (exit 2); want the exit-1 preflight-failure class")
	}
	if !strings.Contains(err.Error(), "requires Docker as the outer container runtime") {
		t.Errorf("DryRun error = %q, want it to mention the Docker-as-outer-runtime requirement", err.Error())
	}
}

// -- no side effects ---------------------------------------------------------

// TestDryRun_NoSideEffects_NoRuntimeInvocationsAndExecAttachNeverCalled is a
// dedicated regression test for the ticket's core AC: DryRun must never
// shell out to the container runtime (a scripted-runtime recorder preset as
// Runtime records zero invocations -- covering run/create/volume/exec, since
// the fake logs every invocation regardless of verb) and must never invoke
// the execAttach seam (the final interactive handoff runAgent uses).
func TestDryRun_NoSideEffects_NoRuntimeInvocationsAndExecAttachNeverCalled(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	// fakeDir first on PATH so any accidental "docker" invocation resolves to
	// the recording fake, while "git" (ComputeScope's repo-root probe) still
	// resolves down the rest of the inherited PATH.
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	origExecAttach := execAttach
	execAttachCalled := false
	execAttach = func(path string, argv, env []string) error {
		execAttachCalled = true
		return nil
	}
	defer func() { execAttach = origExecAttach }()

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	_, err := eng.DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	if execAttachCalled {
		t.Error("DryRun invoked the execAttach seam; it must never attach/exec into a container")
	}
	if lines := readCallLog(t, callLog); len(lines) != 0 {
		t.Errorf("DryRun invoked the container runtime %d time(s), want zero side effects; calls:\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

// -- host-network isolation-warning dedup (code review fix, ticket #589) ---

// TestDryRun_HostNetwork_AddsNetworkHostAndWarnsOnce covers --host-network
// combined with --dry-run: the create argv carries "--network host", and the
// isolation warning assembleOptionalFeatures prints appears exactly once
// across the combined create-argv-build + WriteText output -- proving the
// Stderr=io.Discard clone DryRun hands to Audit (see dryrun.go) actually
// avoids a double warning. Mirrors
// TestOpenHostNetwork_AddsNetworkHostWithWarning (sandbox_open_test.go) at
// the launcher level.
func TestDryRun_HostNetwork_AddsNetworkHostAndWarnsOnce(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	stderr := &bytes.Buffer{}
	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: stderr}
	plan, err := eng.DryRun(Options{Agent: "claude", HostNetwork: true})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	joined := strings.Join(plan.CreateArgv, " ")
	if !strings.Contains(joined, "--network host") {
		t.Errorf("CreateArgv missing \"--network host\", got:\n%s", joined)
	}

	var out bytes.Buffer
	if err := plan.WriteText(&out); err != nil {
		t.Fatalf("WriteText: %v", err)
	}

	const warning = "weakens the container's isolation boundary"
	combined := stderr.String() + out.String()
	if got := strings.Count(combined, warning); got != 1 {
		t.Errorf("isolation warning appeared %d time(s) across create-argv-build stderr + WriteText output, want exactly 1:\n%s", got, combined)
	}
}

// -- successful dind dry-run preview (code review fix, ticket #589) --------

// TestDryRun_Dind_SuccessfulPreviewShowsSysboxRuntimeWithNoSideEffects
// mirrors TestOpenDind_ViaRepoConfig_AddsSysboxRuntimeVolumeAndEnv
// (sandbox_open_test.go) but for a successful `--dry-run --dind` preview: a
// scripted docker fake answers dindPreflight's sysbox-runc registration
// probe via FAKE_INFO_RUNTIMES, and the create argv shows
// "--runtime=sysbox-runc" with zero container/volume side effects -- only
// the registration-probe invocation itself (an inherent, non-side-effecting
// read Launch would also perform) is logged.
func TestDryRun_Dind_SuccessfulPreviewShowsSysboxRuntimeWithNoSideEffects(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_INFO_RUNTIMES", `{"sysbox-runc":{},"runc":{}}`)

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	plan, err := eng.DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	joined := strings.Join(plan.CreateArgv, " ")
	if !strings.Contains(joined, "--runtime=sysbox-runc") {
		t.Errorf("CreateArgv missing \"--runtime=sysbox-runc\", got:\n%s", joined)
	}

	lines := readCallLog(t, callLog)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "info") {
		t.Errorf("expected exactly one runtime invocation (the sysbox-runc registration probe), want zero container/volume side effects; got calls:\n%s", strings.Join(lines, "\n"))
	}
}
