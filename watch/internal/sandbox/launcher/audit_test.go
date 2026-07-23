package launcher

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -- cenci audit (ticket #588) -------------------------------------------
//
// `cenci audit [flags]` reports the effective sandbox security posture the
// launcher WOULD apply for the current repo/agent/flags, derived by reusing
// the launcher's existing assemble* construction methods (never re-deriving
// them). These are integration-style table tests against the real Audit
// method and WriteText renderer, with a temp HOME and (where repo scope
// matters) a temp git repo — no fake container runtime is needed, since
// Audit is entirely read-only and the assemble* methods it reuses never
// shell out to the runtime themselves.
//
// NOTE (red phase): Audit and WriteText are currently stubs that always
// return ErrAuditNotImplemented / write nothing (see audit.go). Every test
// below therefore fails immediately at its "Audit: %v" fatal check until
// the real implementation lands — that is the intended red-phase state.

// newAuditTestRepo git-inits a temp dir (skipping the test if git isn't on
// PATH, mirroring scope_test.go's TestComputeScope_RepoMode precedent) and
// returns its resolved absolute path.
func newAuditTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	return resolved
}

// findCredentialSource returns the CredentialSource entry matching typ, or
// nil when none is present.
func findCredentialSource(sources []CredentialSource, typ string) *CredentialSource {
	for i := range sources {
		if sources[i].Type == typ {
			return &sources[i]
		}
	}
	return nil
}

// auditEngine returns a minimal Engine suitable for calling Audit: no
// resolved container runtime (Audit never shells out to it) and a
// discarded Stderr (the reused assemble* methods print launch-time
// isolation warnings that don't apply to a report).
func auditEngine() *Engine {
	return &Engine{Stderr: bytes.NewBuffer(nil)}
}

// -- baseline / scope ------------------------------------------------------

// TestAudit_RepoScopeDefault_IsBaselineWithNoWeakenings pins the default-safe
// baseline: inside a repo, with no opt-in flags, boundaryWeakenings must be
// explicitly empty and dind/network must both report their unweakened
// defaults.
func TestAudit_RepoScopeDefault_IsBaselineWithNoWeakenings(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if posture.Scope != "repo" {
		t.Errorf("Scope = %q, want repo", posture.Scope)
	}
	if posture.RepoRoot != repo {
		t.Errorf("RepoRoot = %q, want %q", posture.RepoRoot, repo)
	}
	if posture.Image.Type != ImageTypeMonolith {
		t.Errorf("Image.Type = %q, want %q (no .cenci/Dockerfile)", posture.Image.Type, ImageTypeMonolith)
	}
	if posture.Network.Mode != NetworkModeBridge || posture.Network.Weakened {
		t.Errorf("Network = %+v, want bridge/not-weakened", posture.Network)
	}
	if posture.Dind.Enabled {
		t.Errorf("Dind.Enabled = true, want false (no --dind, no repo config)")
	}
	if len(posture.BoundaryWeakenings) != 0 {
		t.Errorf("BoundaryWeakenings = %+v, want explicitly empty for the default-safe baseline", posture.BoundaryWeakenings)
	}
}

// TestAudit_LegacyScope_DindUnavailable covers a non-git cwd: scope falls
// back to legacy, and dind is unavailable (source "off") since it is only
// ever available in repo scope (#585).
func TestAudit_LegacyScope_DindUnavailable(t *testing.T) {
	cwd := t.TempDir() // not a git repo
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if posture.Scope != "legacy" {
		t.Fatalf("Scope = %q, want legacy", posture.Scope)
	}
	if posture.RepoRoot != "" {
		t.Errorf("RepoRoot = %q, want empty in legacy scope", posture.RepoRoot)
	}
	if posture.Dind.Enabled {
		t.Errorf("Dind.Enabled = true in legacy scope, want false")
	}
	if posture.Dind.Source != DindSourceOff {
		t.Errorf("Dind.Source = %q, want %q in legacy scope", posture.Dind.Source, DindSourceOff)
	}
}

// -- --host-network boundary weakening --------------------------------------

// TestAudit_HostNetwork_MarksBoundaryWeakening_DindStaysAbsent covers the
// only boundary-weakening flag today: --host-network flips Network to
// host/weakened and adds a BoundaryWeakenings entry, without touching Dind.
func TestAudit_HostNetwork_MarksBoundaryWeakening_DindStaysAbsent(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude", HostNetwork: true})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if posture.Network.Mode != NetworkModeHost || !posture.Network.Weakened {
		t.Errorf("Network = %+v, want host/weakened", posture.Network)
	}
	if len(posture.BoundaryWeakenings) == 0 {
		t.Fatalf("BoundaryWeakenings is empty, want a --host-network entry")
	}
	found := false
	for _, w := range posture.BoundaryWeakenings {
		if w.Option == "--host-network" {
			found = true
			if !w.Active {
				t.Errorf("--host-network BoundaryWeakening.Active = false, want true")
			}
			if w.Effect == "" {
				t.Errorf("--host-network BoundaryWeakening.Effect is empty, want a description of the weakening")
			}
		}
	}
	if !found {
		t.Errorf("expected a --host-network entry in BoundaryWeakenings, got %+v", posture.BoundaryWeakenings)
	}
	if posture.Dind.Enabled {
		t.Errorf("Dind.Enabled = true with only --host-network set, want false")
	}
}

// -- dind attribution --------------------------------------------------------

// TestAudit_DindFlag_PopulatesDindSection covers --dind: Dind.Enabled true,
// Source "flag", and Runtime/StorageVolume populated.
func TestAudit_DindFlag_PopulatesDindSection(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude", Dind: true})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if !posture.Dind.Enabled {
		t.Fatalf("Dind.Enabled = false, want true with --dind")
	}
	if posture.Dind.Source != DindSourceFlag {
		t.Errorf("Dind.Source = %q, want %q", posture.Dind.Source, DindSourceFlag)
	}
	if posture.Dind.StorageVolume == "" {
		t.Errorf("Dind.StorageVolume is empty, want the per-repo dind storage volume name")
	}
	if posture.Dind.Runtime == "" {
		t.Errorf("Dind.Runtime is empty, want the sysbox-runc runtime identifier")
	}
}

// TestAudit_DindRepoConfig_SourceIsConfig covers the .cenci/config.json
// sandbox.dind=true fallback (no --dind flag given): Source must attribute
// it to "config", not "flag".
func TestAudit_DindRepoConfig_SourceIsConfig(t *testing.T) {
	repo := newAuditTestRepo(t)
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if !posture.Dind.Enabled {
		t.Fatalf("Dind.Enabled = false, want true from repo config")
	}
	if posture.Dind.Source != DindSourceConfig {
		t.Errorf("Dind.Source = %q, want %q", posture.Dind.Source, DindSourceConfig)
	}
}

// TestAudit_Dind_NeverReportedAsBoundaryWeakening is a dedicated, explicit
// assertion of the confirmed Q&A planning decision (Q1): dind sits in its
// own "Nested Docker (sysbox-isolated)" section/JSON object and must NEVER
// appear in BoundaryWeakenings — only --host-network is a boundary
// weakening. This is deliberately its own test, not incidental coverage
// inside another dind test.
func TestAudit_Dind_NeverReportedAsBoundaryWeakening(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude", Dind: true})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if !posture.Dind.Enabled {
		t.Fatalf("Dind.Enabled = false, want true with --dind (precondition for this test)")
	}

	for _, w := range posture.BoundaryWeakenings {
		if strings.Contains(strings.ToLower(w.Option), "dind") {
			t.Errorf("dind must never appear in BoundaryWeakenings (Q1 planning decision: dind has its own section) — found %+v", w)
		}
	}
}

// -- image type ---------------------------------------------------------

// TestAudit_ImageType_RepoVsMonolith covers image.type flipping from
// "monolith" to "repo" once <repo-root>/.cenci/Dockerfile appears
// (scope.UsingRepoImage).
func TestAudit_ImageType_RepoVsMonolith(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	eng := auditEngine()

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (monolith): %v", err)
	}
	if posture.Image.Type != ImageTypeMonolith {
		t.Errorf("Image.Type = %q, want %q before .cenci/Dockerfile exists", posture.Image.Type, ImageTypeMonolith)
	}

	writeFile(t, filepath.Join(repo, ".cenci", "Dockerfile"), "FROM cenci-sandbox-base:latest\n")
	posture, err = eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (repo image): %v", err)
	}
	if posture.Image.Type != ImageTypeRepo {
		t.Errorf("Image.Type = %q, want %q once .cenci/Dockerfile exists", posture.Image.Type, ImageTypeRepo)
	}
}

// -- credential sources (non-fatal) --------------------------------------

// TestAudit_CodexCredentials_PresentAndAbsentAreNonFatal covers Audit's
// non-fatal credential handling: validateCredentials would normally hard-fail
// a codex launch with no auth present (a returned error); Audit must instead
// record present:false in CredentialSources and return a nil error in both
// the absent and present cases.
func TestAudit_CodexCredentials_PresentAndAbsentAreNonFatal(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	eng := auditEngine()

	posture, err := eng.Audit(Options{Agent: "codex"})
	if err != nil {
		t.Fatalf("Audit (codex, no creds) = %v, want nil (non-fatal credential handling)", err)
	}
	src := findCredentialSource(posture.CredentialSources, CredentialTypeCodex)
	if src == nil {
		t.Fatalf("expected a codex credentialSources entry, got %+v", posture.CredentialSources)
	}
	if src.Present {
		t.Errorf("codex credential source Present = true without ~/.codex/auth.json, want false")
	}
	if src.HostPath == "" {
		t.Errorf("codex credential source HostPath is empty, want the resolved ~/.codex/auth.json path")
	}

	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"sentinel-secret-value-abc"}`)
	posture, err = eng.Audit(Options{Agent: "codex"})
	if err != nil {
		t.Fatalf("Audit (codex, with creds): %v", err)
	}
	src = findCredentialSource(posture.CredentialSources, CredentialTypeCodex)
	if src == nil || !src.Present {
		t.Fatalf("expected a present codex credentialSources entry, got %+v", posture.CredentialSources)
	}
}

// TestAudit_ClaudeAndGhCredentials_PresentAndAbsent covers claude's optional
// staged credential and GitHub CLI credential staging, both present and
// absent.
func TestAudit_ClaudeAndGhCredentials_PresentAndAbsent(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	eng := auditEngine()

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (no creds): %v", err)
	}
	for _, typ := range []string{CredentialTypeClaude, CredentialTypeGh} {
		src := findCredentialSource(posture.CredentialSources, typ)
		if src == nil {
			t.Fatalf("expected a %s credentialSources entry, got %+v", typ, posture.CredentialSources)
		}
		if src.Present {
			t.Errorf("%s credential source Present = true without a staged file, want false", typ)
		}
	}

	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"token":"sentinel"}`)
	writeFile(t, filepath.Join(home, ".config", "gh", "hosts.yml"), "github.com:\n  oauth_token: sentinel\n")
	posture, err = eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (with creds): %v", err)
	}
	for _, typ := range []string{CredentialTypeClaude, CredentialTypeGh} {
		src := findCredentialSource(posture.CredentialSources, typ)
		if src == nil || !src.Present {
			t.Fatalf("expected a present %s credentialSources entry, got %+v", typ, posture.CredentialSources)
		}
	}
}

// TestAudit_PencilCredentialSource_PresentAndAbsent covers the Pencil CLI
// session's optional staged credential (headless design reads inside the
// sandbox): the pencil credentialSources entry must always be reported with
// its resolved host path, flipping Present only once
// ~/.pencil/session-cli.json actually exists.
func TestAudit_PencilCredentialSource_PresentAndAbsent(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	eng := auditEngine()

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (no pencil session): %v", err)
	}
	src := findCredentialSource(posture.CredentialSources, CredentialTypePencil)
	if src == nil {
		t.Fatalf("expected a pencil credentialSources entry, got %+v", posture.CredentialSources)
	}
	if src.Present {
		t.Errorf("pencil credential source Present = true without a staged file, want false")
	}
	wantPath := filepath.Join(home, ".pencil", "session-cli.json")
	if src.HostPath != wantPath {
		t.Errorf("pencil credential source HostPath = %q, want %q", src.HostPath, wantPath)
	}

	writeFile(t, wantPath, `{"token":"sentinel"}`)
	posture, err = eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (with pencil session): %v", err)
	}
	src = findCredentialSource(posture.CredentialSources, CredentialTypePencil)
	if src == nil || !src.Present {
		t.Fatalf("expected a present pencil credentialSources entry, got %+v", posture.CredentialSources)
	}
}

// TestAudit_ForwardedEnv_IncludesPenCliKeyNameOnly covers the PEN_CLI_KEY
// per-exec passthrough (Pencil CLI auth for headless design reads): once set
// on the host it must be reported in ForwardedEnv by name, marked Secret,
// and its value must never appear in the marshaled posture.
func TestAudit_ForwardedEnv_IncludesPenCliKeyNameOnly(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	const penSecret = "sentinel-secret-value-pen789"
	t.Setenv("PEN_CLI_KEY", penSecret)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	found := false
	for _, ev := range posture.ForwardedEnv {
		if ev.Name == "PEN_CLI_KEY" {
			found = true
			if !ev.Secret {
				t.Errorf("PEN_CLI_KEY forwarded env Secret = false, want true")
			}
		}
	}
	if !found {
		t.Errorf("expected PEN_CLI_KEY in ForwardedEnv names, got %+v", posture.ForwardedEnv)
	}

	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), penSecret) {
		t.Errorf("JSON output leaks the PEN_CLI_KEY sentinel value:\n%s", data)
	}
}

// -- cross-agent applicability / staging (ticket #598) ---------------------

// TestAudit_CrossAgentCredential_CodexPresentButNotApplicable_NotStaged is
// the primary Acceptance-Criteria fixture from #598: a claude audit with
// Codex auth present on the host must report codex as Present:true but
// Applicable:false and Staged:false (the mount plan only ever stages codex
// creds for agent=="codex" — validateCredentials/launch.go), while claude's
// own credential (and gh, staged for every agent by assembleVolumeMounts)
// must report Staged:true.
func TestAudit_CrossAgentCredential_CodexPresentButNotApplicable_NotStaged(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"sentinel"}`)
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"token":"sentinel"}`)
	writeFile(t, filepath.Join(home, ".config", "gh", "hosts.yml"), "github.com:\n  oauth_token: sentinel\n")

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	codex := findCredentialSource(posture.CredentialSources, CredentialTypeCodex)
	if codex == nil {
		t.Fatalf("expected a codex credentialSources entry, got %+v", posture.CredentialSources)
	}
	if !codex.Present {
		t.Errorf("codex credential Present = false, want true (~/.codex/auth.json exists)")
	}
	if codex.Applicable {
		t.Errorf("codex credential Applicable = true for a claude audit, want false (codex creds only apply to agent=codex)")
	}
	if codex.Staged {
		t.Errorf("codex credential Staged = true for a claude audit, want false (mount plan never stages codex creds for a non-codex agent)")
	}

	for _, typ := range []string{CredentialTypeClaude, CredentialTypeGh} {
		src := findCredentialSource(posture.CredentialSources, typ)
		if src == nil {
			t.Fatalf("expected a %s credentialSources entry, got %+v", typ, posture.CredentialSources)
		}
		if !src.Present {
			t.Errorf("%s credential Present = false, want true", typ)
		}
		if !src.Applicable {
			t.Errorf("%s credential Applicable = false, want true (claude/gh apply to every agent)", typ)
		}
		if !src.Staged {
			t.Errorf("%s credential Staged = false, want true (present + applicable => staged)", typ)
		}
	}
}

// TestAudit_StagedImpliesApplicableAndPresent_AcrossAgents is a dedicated
// invariant test (per the plan's Architectural Context): staged must never
// be true unless applicable && present both hold, for every credential type
// and every agent — Staged is read from the already-computed mounts set
// (never independently re-derived), so this invariant guards against future
// drift between the two.
func TestAudit_StagedImpliesApplicableAndPresent_AcrossAgents(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"token":"sentinel"}`)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"sentinel"}`)
	writeFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{"token":"sentinel"}`)
	writeFile(t, filepath.Join(home, ".config", "gh", "hosts.yml"), "github.com:\n  oauth_token: sentinel\n")
	writeFile(t, filepath.Join(home, ".pencil", "session-cli.json"), `{"token":"sentinel"}`)

	for _, agent := range []string{"claude", "codex", "opencode"} {
		posture, err := auditEngine().Audit(Options{Agent: agent})
		if err != nil {
			t.Fatalf("Audit(%s): %v", agent, err)
		}
		for _, src := range posture.CredentialSources {
			if src.Staged && (!src.Applicable || !src.Present) {
				t.Errorf("agent %s: credential %s Staged=true but Applicable=%t Present=%t; invariant staged => applicable && present violated",
					agent, src.Type, src.Applicable, src.Present)
			}
		}
	}
}

// -- credential probe: missing vs unreadable/error (ticket #598) -----------

// TestAudit_CredentialProbe_MissingVsDirectoryInPlace_DistinctFieldsAndText
// covers the three-state probe: a plain-absent credential must report
// Probe:CredentialProbeMissing, while a directory sitting at the same path
// (root-safe, deterministic per Q&A item 2 — no chmod/permission-denied
// primary fixture, since tests may run as root) must report
// Probe:CredentialProbeError, distinctly. Both cases keep Present:false
// (isRegularFile stays false for a directory), and WriteText's rendered text
// must differ between the two states — a content-specific assertion, not
// just a non-empty check (AGENTS.md #446).
func TestAudit_CredentialProbe_MissingVsDirectoryInPlace_DistinctFieldsAndText(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	eng := auditEngine()

	// Missing: no ~/.codex/auth.json at all.
	missingPosture, err := eng.Audit(Options{Agent: "codex"})
	if err != nil {
		t.Fatalf("Audit (missing): %v", err)
	}
	missingSrc := findCredentialSource(missingPosture.CredentialSources, CredentialTypeCodex)
	if missingSrc == nil {
		t.Fatalf("expected a codex credentialSources entry, got %+v", missingPosture.CredentialSources)
	}
	if missingSrc.Probe != CredentialProbeMissing {
		t.Errorf("missing codex credential Probe = %q, want %q", missingSrc.Probe, CredentialProbeMissing)
	}
	if missingSrc.Present {
		t.Errorf("missing codex credential Present = true, want false")
	}
	var missingText bytes.Buffer
	if err := missingPosture.WriteText(&missingText); err != nil {
		t.Fatalf("WriteText (missing): %v", err)
	}

	// Error: a directory sits at the codex auth.json path.
	codexAuthPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(codexAuthPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	errorPosture, err := eng.Audit(Options{Agent: "codex"})
	if err != nil {
		t.Fatalf("Audit (directory-in-place): %v", err)
	}
	errorSrc := findCredentialSource(errorPosture.CredentialSources, CredentialTypeCodex)
	if errorSrc == nil {
		t.Fatalf("expected a codex credentialSources entry, got %+v", errorPosture.CredentialSources)
	}
	if errorSrc.Probe != CredentialProbeError {
		t.Errorf("directory-in-place codex credential Probe = %q, want %q", errorSrc.Probe, CredentialProbeError)
	}
	if errorSrc.Present {
		t.Errorf("directory-in-place codex credential Present = true, want false (not a regular file)")
	}
	var errorText bytes.Buffer
	if err := errorPosture.WriteText(&errorText); err != nil {
		t.Fatalf("WriteText (directory-in-place): %v", err)
	}

	if missingSrc.Probe == errorSrc.Probe {
		t.Errorf("missing and directory-in-place probes must report distinct Probe values, both got %q", missingSrc.Probe)
	}
	if missingText.String() == errorText.String() {
		t.Errorf("missing and directory-in-place probe states must render distinct text, got identical output:\n%s", missingText.String())
	}
	if !strings.Contains(errorText.String(), "unreadable") && !strings.Contains(errorText.String(), "error") {
		t.Errorf("directory-in-place probe text does not distinguish an error/unreadable state, got:\n%s", errorText.String())
	}
	if strings.Contains(missingText.String(), "unreadable") || strings.Contains(missingText.String(), "error") {
		t.Errorf("missing probe text must not claim an unreadable/error state, got:\n%s", missingText.String())
	}
}

// TestCredentialStatusText_UnrecognizedProbe_NotRenderedAsAbsent covers a
// silent-failure regression: Probe is a plain string, not a compiler-enforced
// enum, so a CredentialSource carrying an unrecognized/zero-value Probe (a
// future probe state added without updating credentialStatusText, or a test
// literal that forgot to set Probe) must render as a visibly distinct
// "unknown probe state" — never silently collapsed into the most reassuring
// "absent" text.
func TestCredentialStatusText_UnrecognizedProbe_NotRenderedAsAbsent(t *testing.T) {
	c := CredentialSource{
		Type:     CredentialTypeCodex,
		HostPath: "/home/user/.codex/auth.json",
		Present:  false,
		Probe:    "some-future-probe-state",
	}
	got := credentialStatusText(c, "codex")
	if got == "absent" {
		t.Errorf("unrecognized Probe rendered as plain %q, want a distinct unknown-state text", got)
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("expected the unrecognized Probe text to call out an unknown state, got %q", got)
	}
	if !strings.Contains(got, "some-future-probe-state") {
		t.Errorf("expected the unrecognized Probe text to include the actual Probe value, got %q", got)
	}
}

// -- env-secret classification (ticket #598) -------------------------------

// TestAudit_EnvSecretClassification_ContextKeySecretTermNot covers giving
// create-time env names the same explicit secret classification semantics
// as forwarded exec env (isSecretEnvName wrapping forwardedEnvVarNames):
// CONTEXT7_API_KEY must be marked Secret:true, while an ordinary,
// non-secret create-time name (TERM) must be marked Secret:false.
func TestAudit_EnvSecretClassification_ContextKeySecretTermNot(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	t.Setenv("CONTEXT7_API_KEY", "sentinel-secret-value-envsecret1")

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	var contextEnv, termEnv *EnvVarName
	for i := range posture.Env {
		switch posture.Env[i].Name {
		case "CONTEXT7_API_KEY":
			contextEnv = &posture.Env[i]
		case "TERM":
			termEnv = &posture.Env[i]
		}
	}
	if contextEnv == nil {
		t.Fatalf("expected CONTEXT7_API_KEY in Env, got %+v", posture.Env)
	}
	if !contextEnv.Secret {
		t.Errorf("CONTEXT7_API_KEY Env entry Secret = false, want true")
	}
	if termEnv == nil {
		t.Fatalf("expected TERM in Env, got %+v", posture.Env)
	}
	if termEnv.Secret {
		t.Errorf("TERM Env entry Secret = true, want false (not a create-time secret name)")
	}
}

// -- env names only -------------------------------------------------------

// TestAudit_EnvReportedAsNamesOnly covers the create-time env list: names
// are reported, and CONTEXT7_API_KEY (an optional passthrough) shows up by
// name once set on the host.
func TestAudit_EnvReportedAsNamesOnly(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	t.Setenv("CONTEXT7_API_KEY", "sentinel-secret-value-xyz123")

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if len(posture.Env) == 0 {
		t.Fatalf("Env is empty, want at least the create-time env names")
	}
	found := false
	for _, ev := range posture.Env {
		if ev.Name == "CONTEXT7_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected CONTEXT7_API_KEY in Env names, got %+v", posture.Env)
	}
}

// -- mounts / volumes ------------------------------------------------------

// TestAudit_MountsIncludeExpectedKinds covers Posture.Mounts' classification
// and the workspace mount's host path.
func TestAudit_MountsIncludeExpectedKinds(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(posture.Mounts) == 0 {
		t.Fatalf("Mounts is empty, want at least the workspace/home/agent-cli mounts")
	}

	kinds := map[string]bool{}
	for _, m := range posture.Mounts {
		kinds[m.Kind] = true
	}
	for _, want := range []string{MountKindWorkspace, MountKindHomeVolume, MountKindAgentCLIVolume} {
		if !kinds[want] {
			t.Errorf("expected a mount with kind %q, got mounts %+v", want, posture.Mounts)
		}
	}
	for _, m := range posture.Mounts {
		if m.Kind == MountKindWorkspace && m.Source != repo {
			t.Errorf("workspace mount Source = %q, want repo root %q", m.Source, repo)
		}
	}
}

// TestAudit_NoHostRuntimeSocketMounted pins the acceptance criterion that the
// only mounted socket is the read-only cenci status socket — never a host
// docker/podman socket.
func TestAudit_NoHostRuntimeSocketMounted(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(posture.Mounts) == 0 {
		t.Fatalf("Mounts is empty; want at least the workspace/home/agent-cli mounts to assert against")
	}

	for _, m := range posture.Mounts {
		if strings.Contains(m.Source, "docker.sock") || strings.Contains(m.Destination, "docker.sock") ||
			strings.Contains(m.Source, "podman.sock") || strings.Contains(m.Destination, "podman.sock") {
			t.Errorf("audit must never report a host docker/podman socket mount, got %+v", m)
		}
		if m.Kind == MountKindCenciSocket && !m.ReadOnly {
			t.Errorf("cenci-socket mount must be read-only, got %+v", m)
		}
	}
}

// TestMountKindForDestination_UnrecognizedDestination_ReturnsUnknown covers
// mountKindForDestination's default case: a destination not in its fixed
// switch must return the explicit MountKindUnknown sentinel, not an
// unlabeled "" — a visible drift signal if a future mount destination is
// added to assembleVolumeMounts/validateCredentials without updating this
// switch. Not reachable through Audit() with any real mount destination
// today, so tested directly against the unexported function.
func TestMountKindForDestination_UnrecognizedDestination_ReturnsUnknown(t *testing.T) {
	got := mountKindForDestination("/some/unknown-destination-not-in-the-switch")
	if got != MountKindUnknown {
		t.Errorf("mountKindForDestination(unrecognized) = %q, want %q", got, MountKindUnknown)
	}
}

// -- reseedCreds ------------------------------------------------------------

// TestAudit_ReseedCredsReflectsOption covers Posture.ReseedCreds mirroring
// the --reseed-creds opt-in.
func TestAudit_ReseedCredsReflectsOption(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude", ReseedCreds: true})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if !posture.ReseedCreds {
		t.Errorf("ReseedCreds = false with --reseed-creds, want true")
	}
}

// -- secret-leak regression ------------------------------------------------

// TestAudit_SecretLeakRegression_NeverEmitsSecretValues is a dedicated,
// content-specific regression test (per watch/AGENTS.md's rule on
// error/content-specific assertions): it seeds real secret VALUES (never
// just checking for non-empty output) into credential files and provider
// API key env vars, then asserts neither WriteText's text output nor the
// marshaled JSON contains any of those sentinel byte strings anywhere.
func TestAudit_SecretLeakRegression_NeverEmitsSecretValues(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	const contextSecret = "sentinel-secret-value-xyz123"
	const openaiSecret = "sentinel-secret-value-openai456"
	const penSecret = "sentinel-secret-value-pen789"
	t.Setenv("CONTEXT7_API_KEY", contextSecret)
	t.Setenv("OPENAI_API_KEY", openaiSecret)
	t.Setenv("PEN_CLI_KEY", penSecret)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"`+contextSecret+`"}`)
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"token":"`+openaiSecret+`"}`)
	writeFile(t, filepath.Join(home, ".pencil", "session-cli.json"), `{"token":"`+penSecret+`"}`)

	posture, err := auditEngine().Audit(Options{Agent: "codex"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	var textBuf bytes.Buffer
	if err := posture.WriteText(&textBuf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(textBuf.String(), contextSecret) {
		t.Errorf("text output leaks the CONTEXT7_API_KEY sentinel value:\n%s", textBuf.String())
	}
	if strings.Contains(textBuf.String(), openaiSecret) {
		t.Errorf("text output leaks the OPENAI_API_KEY sentinel value:\n%s", textBuf.String())
	}
	if strings.Contains(textBuf.String(), penSecret) {
		t.Errorf("text output leaks the PEN_CLI_KEY sentinel value:\n%s", textBuf.String())
	}

	jsonBytes, err := json.MarshalIndent(posture, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	if strings.Contains(string(jsonBytes), contextSecret) {
		t.Errorf("JSON output leaks the CONTEXT7_API_KEY sentinel value:\n%s", jsonBytes)
	}
	if strings.Contains(string(jsonBytes), openaiSecret) {
		t.Errorf("JSON output leaks the OPENAI_API_KEY sentinel value:\n%s", jsonBytes)
	}
	if strings.Contains(string(jsonBytes), penSecret) {
		t.Errorf("JSON output leaks the PEN_CLI_KEY sentinel value:\n%s", jsonBytes)
	}

	// Extend the regression to the new cross-agent (present-but-not-staged)
	// and probe-error (directory-in-place) fixtures introduced by #598: none
	// of the sentinel secret values above may leak in either state.
	writeFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{"token":"`+contextSecret+`"}`)
	if err := os.RemoveAll(filepath.Join(home, ".config", "gh", "hosts.yml")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "gh", "hosts.yml"), 0o755); err != nil {
		t.Fatalf("MkdirAll (directory-in-place gh hosts.yml): %v", err)
	}

	crossPosture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (cross-agent claude, with error-probe gh path): %v", err)
	}

	var crossTextBuf bytes.Buffer
	if err := crossPosture.WriteText(&crossTextBuf); err != nil {
		t.Fatalf("WriteText (cross-agent): %v", err)
	}
	for _, secret := range []string{contextSecret, openaiSecret, penSecret} {
		if strings.Contains(crossTextBuf.String(), secret) {
			t.Errorf("cross-agent text output leaks a sentinel secret value:\n%s", crossTextBuf.String())
		}
	}

	crossJSONBytes, err := json.MarshalIndent(crossPosture, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent (cross-agent): %v", err)
	}
	for _, secret := range []string{contextSecret, openaiSecret, penSecret} {
		if strings.Contains(string(crossJSONBytes), secret) {
			t.Errorf("cross-agent JSON output leaks a sentinel secret value:\n%s", crossJSONBytes)
		}
	}
}

// TestAudit_JSON_EmptySlicesSerializeAsEmptyArrayNotNull is a dedicated,
// content-specific regression test (per watch/AGENTS.md's rule on
// error/content-specific assertions): for the default (no --dind, no
// --host-network) case, boundaryWeakenings and forwardedEnv must both be
// empty — Go's json.Marshal serializes a nil slice as `null`, not `[]`, and
// since "no boundary weakenings" / "no forwarded env" are the common
// default-safe cases, a nil slice here would routinely break
// jq '.boundaryWeakenings[]'-style scripting consumers. Asserted via
// strings.Contains on the raw JSON bytes (not by unmarshaling back into a Go
// slice, which wouldn't distinguish null from [] after unmarshaling).
func TestAudit_JSON_EmptySlicesSerializeAsEmptyArrayNotNull(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Explicitly clear every provider API key assembleExecEnv would forward
	// for "claude" (CONTEXT7_API_KEY, PEN_CLI_KEY) so forwardedEnv is
	// deterministically empty regardless of the host process's own
	// environment.
	t.Setenv("CONTEXT7_API_KEY", "")
	t.Setenv("PEN_CLI_KEY", "")
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := string(data)

	if !strings.Contains(out, `"boundaryWeakenings":[]`) {
		t.Errorf("JSON output does not contain \"boundaryWeakenings\":[], want an explicitly empty array (not null); got:\n%s", out)
	}
	if !strings.Contains(out, `"forwardedEnv":[]`) {
		t.Errorf("JSON output does not contain \"forwardedEnv\":[], want an explicitly empty array (not null); got:\n%s", out)
	}
	if strings.Contains(out, `"boundaryWeakenings":null`) {
		t.Errorf("JSON output contains \"boundaryWeakenings\":null; regression — must be an empty array; got:\n%s", out)
	}
	if strings.Contains(out, `"forwardedEnv":null`) {
		t.Errorf("JSON output contains \"forwardedEnv\":null; regression — must be an empty array; got:\n%s", out)
	}
}

// -- JSON stability -------------------------------------------------------

// TestAudit_JSON_HasStableFieldNamesAndNeverSerializesEnvValue asserts the
// exact top-level and nested field names the JSON schema promises are
// present in the marshaled output, and that a "value" key (which would
// indicate an env/secret value slipped into the JSON) never appears.
// --dind and --host-network are both set so every optional/omitempty field
// (dind.runtime, dind.storageVolume, boundaryWeakenings[].*) is populated
// and actually exercised by the assertion.
func TestAudit_JSON_HasStableFieldNamesAndNeverSerializesEnvValue(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	t.Setenv("CONTEXT7_API_KEY", "sentinel-secret-value-xyz123")

	posture, err := auditEngine().Audit(Options{Agent: "claude", Dind: true, HostNetwork: true})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	data, err := json.MarshalIndent(posture, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	out := string(data)

	for _, key := range []string{
		`"agent"`, `"scope"`, `"repoRoot"`,
		`"image"`, `"reference"`, `"type"`,
		`"workspace"`, `"hostPath"`, `"containerPath"`, `"readOnly"`,
		`"network"`, `"mode"`, `"weakened"`,
		`"dind"`, `"enabled"`, `"source"`, `"runtime"`, `"storageVolume"`,
		`"mounts"`, `"destination"`, `"kind"`,
		`"volumes"`, `"name"`,
		`"env"`, `"forwardedEnv"`, `"secret"`,
		`"credentialSources"`, `"present"`, `"probe"`, `"applicable"`, `"staged"`,
		`"boundaryWeakenings"`, `"option"`, `"active"`, `"effect"`,
		`"reseedCreds"`,
	} {
		if !strings.Contains(out, key) {
			t.Errorf("JSON output missing expected field %s; got:\n%s", key, out)
		}
	}
	if strings.Contains(out, `"value"`) {
		t.Errorf("JSON output must never serialize a \"value\" key (env values are never captured); got:\n%s", out)
	}
}
