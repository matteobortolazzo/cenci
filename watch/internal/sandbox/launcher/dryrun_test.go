package launcher

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
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
	setFakeDockerNotRunning(t)

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

// TestDryRun_CreateArgv_StagesPencilSessionOnlyWhenPresent pins the optional
// Pencil CLI session staging mount (headless design reads inside the
// sandbox): a host ~/.pencil/session-cli.json is staged read-only at
// /tmp/host-pencil-creds/session-cli.json, and no pencil staging mount
// appears at all when the host has no session file.
func TestDryRun_CreateArgv_StagesPencilSessionOnlyWhenPresent(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	setFakeDockerNotRunning(t)

	plan, err := dryRunEngine().DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun (no pencil session): %v", err)
	}
	joined := strings.Join(plan.CreateArgv, " ")
	if strings.Contains(joined, "host-pencil-creds") {
		t.Errorf("CreateArgv stages a pencil credential mount without a host session file:\n%s", joined)
	}

	writeFile(t, filepath.Join(home, ".pencil", "session-cli.json"), `{"token":"unused-in-this-test"}`)
	plan, err = dryRunEngine().DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun (with pencil session): %v", err)
	}
	joined = strings.Join(plan.CreateArgv, " ")
	wantPencil := home + "/.pencil/session-cli.json:/tmp/host-pencil-creds/session-cli.json:ro"
	if !strings.Contains(joined, wantPencil) {
		t.Errorf("CreateArgv missing the read-only pencil session mount %q, got:\n%s", wantPencil, joined)
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
	setFakeDockerNotRunning(t)

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
	setFakeDockerNotRunning(t)

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
	setFakeDockerNotRunning(t)

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

// -- error-path mirroring (watch/docs/error-handling.md #446) ------------

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
	// Clear any provider credential inherited from the host/CI environment
	// (AC6): validateCredentials treats a present OPENAI_API_KEY as
	// sufficient codex auth on its own, so an inherited value would silently
	// make this negative test pass for the wrong reason (or fail to hit the
	// hard error at all) depending on the machine running it.
	t.Setenv("OPENAI_API_KEY", "")
	setFakeDockerNotRunning(t)

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

// TestDryRun_NoSideEffects_NoMutatingRuntimeCallsAndExecAttachNeverCalled is
// a dedicated regression test for the ticket's core AC4: DryRun must never
// shell out to a *mutating* container-runtime verb (run/rm/exec/create/
// volume create/network) and must never invoke the execAttach seam (the
// final interactive handoff runAgent uses). Since #620 makes DryRun perform
// its own read-only container-disposition probing (ps + inspect, via the
// shared planArgvs helper), a bare "zero invocations" assertion would no
// longer hold -- read-only ps/inspect calls are allowed and expected; only
// mutating verbs are forbidden. Each forbidden verb is enumerated explicitly
// (AGENTS #446/plan Risks) so a future accidental run/rm creeping into the
// dry-run path can't hide behind a loosened "some invocations are fine"
// assertion.
func TestDryRun_NoSideEffects_NoMutatingRuntimeCallsAndExecAttachNeverCalled(t *testing.T) {
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

	lines := readCallLog(t, callLog)
	for _, mutatingPrefix := range []string{"run ", "rm ", "exec ", "create ", "volume create", "network"} {
		if containsPrefix(lines, mutatingPrefix) {
			t.Errorf("DryRun issued a mutating runtime call with prefix %q; want zero mutation (read-only ps/inspect only). calls:\n%s", mutatingPrefix, strings.Join(lines, "\n"))
		}
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if verb := fields[0]; verb != "ps" && verb != "inspect" {
			t.Errorf("DryRun issued an unexpected runtime call %q; want only read-only ps/inspect probes, calls:\n%s", line, strings.Join(lines, "\n"))
		}
	}
}

// TestDryRun_NoAgentVolumeStatusProbe_RegressionForLaunchTimeTTLRefresh pins
// ticket #710's explicit regression AC: DryRun must still perform no
// probe-driven refresh. EnsureAgentVolume (and, inside it, the new
// agent-cli.sh status/update TTL staleness branch) is a Launch-only side
// effect DryRun has always deliberately skipped (dryrun.go's doc comment);
// this stays true even though Launch itself now performs the probe/refresh
// unconditionally before planArgvs. Named explicitly (rather than relying
// solely on TestDryRun_NoSideEffects_NoMutatingRuntimeCallsAndExecAttachNeverCalled's
// broader ps/inspect-only allowlist above) so a future regression that
// reintroduces the probe into DryRun's path fails with a message naming the
// exact ticket-#710 behavior it broke.
func TestDryRun_NoAgentVolumeStatusProbe_RegressionForLaunchTimeTTLRefresh(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if _, err := eng.DryRun(Options{Agent: "claude"}); err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	if lines := readCallLog(t, callLog); containsLineWithAll(lines, "agent-cli.sh") {
		t.Errorf("DryRun must never invoke the agent-cli.sh status/update probe (EnsureAgentVolume is a Launch-only side effect); calls:\n%s", strings.Join(lines, "\n"))
	}
}

// -- launch-branch fidelity (ticket #620) -----------------------------------

// TestDryRun_ExistingCompatibleContainer_RendersAttachOnlyWithNoCreateArgv
// covers AC1/AC2: when a scoped container is already running and compatible
// (has the shared read-only agent-CLI mount), a real launch would attach
// only -- DryRun must render the same attach-only branch, reporting the
// disposition (Mode == "attach", ContainerName set) and emitting no create
// argv at all, rather than the always-create preview #589 shipped with.
func TestDryRun_ExistingCompatibleContainer_RendersAttachOnlyWithNoCreateArgv(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_INSPECT_MOUNTS", AgentCLIVolumeName("claude")+"|/opt/cenci-agent|false\n")

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	plan, err := eng.DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	if plan.Mode != "attach" {
		t.Errorf("Mode = %q, want \"attach\" for an already-running compatible container", plan.Mode)
	}
	if plan.CreateArgv != nil {
		t.Errorf("CreateArgv = %v, want nil (no create argv) for the attach-only branch", plan.CreateArgv)
	}
	if plan.ContainerName != scope.ContainerName {
		t.Errorf("ContainerName = %q, want %q", plan.ContainerName, scope.ContainerName)
	}

	var out bytes.Buffer
	if err := plan.WriteText(&out); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	text := out.String()
	if strings.Contains(text, "run --name") {
		t.Errorf("WriteText rendered a container-create argv for an already-running compatible container, got:\n%s", text)
	}
}

// TestDryRun_Posture_StaysPlannedBasisEvenWhenContainerRunning is a
// regression test (per watch/docs/test-strategy.md #620: "do not assume
// existing tests will detect the behavior change") for the
// `clone.Runtime = ""` fix in DryRun: the Posture breakdown DryRun
// composes must always stay basis:"planned" -- a preview of what a real
// launch WOULD apply -- even when a real scoped container happens to be
// running under that name (the same precondition
// TestDryRun_ExistingCompatibleContainer_... exercises for the
// attach-branch argv). Without clearing clone.Runtime, Audit's
// ticket #627 observed-mode dispatch would fire on the clone and flip this
// Posture to basis:"running", contradicting DryRun's own "preview, not
// observation" contract (dryrun.go's DryRun doc comment).
func TestDryRun_Posture_StaysPlannedBasisEvenWhenContainerRunning(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_INSPECT_MOUNTS", AgentCLIVolumeName("claude")+"|/opt/cenci-agent|false\n")

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	plan, err := eng.DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	if plan.Mode != "attach" {
		t.Fatalf("Mode = %q, want \"attach\" (precondition: a real container is running under this scoped name)", plan.Mode)
	}
	if plan.Posture.Basis != PostureBasisPlanned {
		t.Errorf("Posture.Basis = %q, want %q -- DryRun's preview must never flip to basis:%q even when a real container happens to be running under the scoped name", plan.Posture.Basis, PostureBasisPlanned, PostureBasisRunning)
	}
}

// TestDryRun_IncompatibleRunningContainer_MirrorsLaunchHardError covers Q2's
// resolution: a running container that predates the shared read-only
// agent-CLI mount makes a real Launch hard-error rather than attach; DryRun
// must fail the exact same way (content-specific assertion per AGENTS #446,
// not just a non-nil check), rather than silently printing a plan for a
// launch that would actually fail.
func TestDryRun_IncompatibleRunningContainer_MirrorsLaunchHardError(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	// FAKE_INSPECT_MOUNTS deliberately left unset: the running container
	// reports no shared read-only agent-CLI mount at all, i.e. it predates
	// the migration -- incompatible.

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	_, err := eng.DryRun(Options{Agent: "claude"})
	if err == nil {
		t.Fatal("DryRun against an incompatible running container = nil error, want the same hard error a real Launch would return")
	}
	if !strings.Contains(err.Error(), "predates shared read-only agent CLIs") {
		t.Errorf("DryRun error = %q, want it to mention \"predates shared read-only agent CLIs\"", err.Error())
	}
}

// TestDryRun_MissingInitialDaemonSocket_OmitsWiringMountsAndFlagsIndeterminate
// covers Q1's resolution (Option A): when no container is running (create
// branch) and the events socket is merely missing -- not unresolvable, the
// distinguishing signal cenciWiringReadOnly already exposes via a non-empty
// socketDir -- a real launch's daemon.EnsureRunning() might bring cenci
// wiring up or might not, so the outcome is genuinely indeterminate
// read-only. DryRun must render the create argv WITHOUT the cenci wiring
// mounts (consistent with the read-only probe's determinate-false posture)
// and set CenciWiringUnknown so the preview never claims to be exact.
func TestDryRun_MissingInitialDaemonSocket_OmitsWiringMountsAndFlagsIndeterminate(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	// FAKE_PS left empty: no running container, so the create branch is the
	// one where wiring indeterminacy applies (per the plan's Assumption: the
	// CenciWiringUnknown caveat is scoped to the create branch only).

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	plan, err := eng.DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	if plan.Mode != "create" {
		t.Fatalf("Mode = %q, want \"create\" (no container running)", plan.Mode)
	}
	if !plan.CenciWiringUnknown {
		t.Error("CenciWiringUnknown = false, want true: the events socket is missing (not unresolvable), so a real launch's daemon-start outcome can't be determined read-only")
	}

	joined := strings.Join(plan.CreateArgv, " ")
	if strings.Contains(joined, ":/usr/local/bin/cenci:ro") {
		t.Errorf("CreateArgv includes the cenci-binary wiring mount despite indeterminate wiring; got:\n%s", joined)
	}
	if strings.Contains(joined, "XDG_RUNTIME_DIR=") {
		t.Errorf("CreateArgv includes the cenci wiring XDG_RUNTIME_DIR env despite indeterminate wiring; got:\n%s", joined)
	}

	var out bytes.Buffer
	if err := plan.WriteText(&out); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "daemon") {
		t.Errorf("WriteText missing an indeterminacy caveat mentioning the not-yet-running daemon, got:\n%s", text)
	}
	if !strings.Contains(text, "could not be determined") {
		t.Errorf("WriteText missing an explicit \"could not be determined\" indeterminacy caveat, got:\n%s", text)
	}
}

// TestDryRun_DeterminateWiring_LiveSocketIncludesMountsAndClearsUnknownFlag
// is the determinate counterpart to the missing-socket test above: binding a
// real unix socket at the default events-socket path makes cenciWiringReadOnly
// report available=true, so the create argv DOES include the cenci wiring
// mounts and CenciWiringUnknown is false. Without this test, CenciWiringUnknown
// could be hardcoded true and every other test would still pass (plan Risks) --
// this proves the flag is a genuine, meaningful signal (AGENTS #446-style
// boundary coverage).
func TestDryRun_DeterminateWiring_LiveSocketIncludesMountsAndClearsUnknownFlag(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	xdgDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)

	eventsSocket := ipc.DefaultEventSocketPath()
	ln, err := net.Listen("unix", eventsSocket)
	if err != nil {
		t.Fatalf("bind events socket at %s: %v", eventsSocket, err)
	}
	defer func() { _ = ln.Close() }()

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	// FAKE_PS left empty: no running container -> create branch, matching
	// the missing-socket counterpart above.

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	plan, err := eng.DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	if plan.CenciWiringUnknown {
		t.Error("CenciWiringUnknown = true, want false: a live events socket makes the wiring outcome determinate")
	}
	joined := strings.Join(plan.CreateArgv, " ")
	if !strings.Contains(joined, ":/usr/local/bin/cenci:ro") {
		t.Errorf("CreateArgv missing the cenci-binary wiring mount despite a live events socket; got:\n%s", joined)
	}
	if !strings.Contains(joined, "XDG_RUNTIME_DIR=/run/user/1000") {
		t.Errorf("CreateArgv missing the cenci wiring XDG_RUNTIME_DIR env despite a live events socket; got:\n%s", joined)
	}
}

// -- ticket #628: reuse-posture reject/reuse mirrors -------------------------
//
// planArgvs is shared by Launch and DryRun (ticket #620), so the new
// reuse-posture checks (host-socket exposure, DinD-mode mismatch, ambiguous
// posture) must reject/attach identically in both. At least one reject case
// and one reuse case are mirrored here (dryrun_test.go:437-459's pattern);
// the full case matrix (all four rejections plus both compatible-reuse
// paths) lives in sandbox_open_test.go's black-box `cenci open` tests.

// TestDryRun_RunningContainerWithHostSocketMount_RejectsEvenWithCompatibleAgentMount
// mirrors TestOpen_RunningContainerWithHostSocketMount_RejectsEvenWithCompatibleAgentMount
// (sandbox_open_test.go): a running container exposing a host Docker/Podman
// socket must never be previewed as attach-only, even though its shared
// agent-CLI mount is otherwise compatible.
func TestDryRun_RunningContainerWithHostSocketMount_RejectsEvenWithCompatibleAgentMount(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_INSPECT_MOUNTS", AgentCLIVolumeName("claude")+"|/opt/cenci-agent|false\n")
	t.Setenv("FAKE_REUSE_POSTURE", "off|runc|0\n/var/run/docker.sock::/var/run/docker.sock\n\n")

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	_, err := eng.DryRun(Options{Agent: "claude"})
	if err == nil {
		t.Fatal("DryRun against a running container exposing a host socket = nil error, want the same rejection a real Launch would return")
	}
	if !strings.Contains(err.Error(), "host Docker/Podman socket") {
		t.Errorf("DryRun error = %q, want it to mention the host Docker/Podman socket rejection", err.Error())
	}
	if !strings.Contains(err.Error(), "cenci sandbox stop "+scope.ContainerName) || !strings.Contains(err.Error(), "then relaunch") {
		t.Errorf("DryRun error = %q, want the stop+relaunch recovery instruction", err.Error())
	}
}

// TestDryRun_CompatibleDindContainer_RendersAttachOnlyWithNoCreateArgv mirrors
// TestOpenDind_CompatibleDindContainer_ReusesSuccessfully (sandbox_open_test.go):
// a running container whose derived DinD posture matches the requested
// --dind must still preview as attach-only, exactly like a real Launch would
// reuse it.
func TestDryRun_CompatibleDindContainer_RendersAttachOnlyWithNoCreateArgv(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_INSPECT_MOUNTS", AgentCLIVolumeName("claude")+"|/opt/cenci-agent|false\n")
	t.Setenv("FAKE_REUSE_POSTURE", "on|sysbox-runc|1\nworkspace-vol::/workspace\ndind-vol::/var/lib/docker\n\n")
	t.Setenv("FAKE_INFO_RUNTIMES", `{"sysbox-runc":{},"runc":{}}`)

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	plan, err := eng.DryRun(Options{Agent: "claude", Dind: true})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	if plan.Mode != "attach" {
		t.Errorf("Mode = %q, want \"attach\" for a compatible existing DinD container", plan.Mode)
	}
	if plan.CreateArgv != nil {
		t.Errorf("CreateArgv = %v, want nil (no create argv) for the attach-only branch", plan.CreateArgv)
	}
}

// TestDryRun_MalformedReusePostureOutput_Rejects mirrors
// TestOpen_MalformedReusePostureHeader_Rejects (sandbox_open_test.go): a
// scripted docker fake answering the reuse-posture inspect with a header
// that exits 0 but doesn't match the expected "<label>|<runtime>|<dindenv>"
// shape must reject the reuse attempt rather than silently falling through
// to the fully permissive zero-value posture (Critical finding, ticket
// #628: parseReusePosture must fail closed on malformed inspect output,
// exactly like the existing dindUnknown and exec-error rejection paths).
func TestDryRun_MalformedReusePostureOutput_Rejects(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_INSPECT_MOUNTS", AgentCLIVolumeName("claude")+"|/opt/cenci-agent|false\n")
	t.Setenv("FAKE_REUSE_POSTURE", "garbage-no-pipes\n")

	eng := &Engine{Runtime: "docker", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	_, err := eng.DryRun(Options{Agent: "claude"})
	if err == nil {
		t.Fatal("DryRun against a container with malformed reuse-posture inspect output = nil error, want a fail-closed rejection")
	}
	if !strings.Contains(err.Error(), "reuse posture") {
		t.Errorf("DryRun error = %q, want it to mention the reuse posture parse failure", err.Error())
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
	setFakeDockerNotRunning(t)

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

	// Two invocations: the sysbox-runc registration probe (dindPreflight) and
	// the read-only containerRunning disposition probe planArgvs now always
	// performs (#620) -- both reads, zero container/volume side effects.
	lines := readCallLog(t, callLog)
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "info") || !strings.HasPrefix(lines[1], "ps ") {
		t.Errorf("expected exactly the sysbox-runc registration probe followed by the read-only containerRunning probe, want zero container/volume side effects; got calls:\n%s", strings.Join(lines, "\n"))
	}
}
