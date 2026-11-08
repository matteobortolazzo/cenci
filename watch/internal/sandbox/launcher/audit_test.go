package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
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

// -- ticket #737: --add-dir as a boundary weakening ---------------------------

// addDirAuditEnv is a repo cwd, an isolated HOME and two resolved extra dirs.
func addDirAuditEnv(t *testing.T) (repo, home string, extras []string) {
	t.Helper()
	repo = newAuditTestRepo(t)
	home = t.TempDir()
	isolateAddDirEnv(t, home)
	t.Chdir(repo)
	for _, name := range []string{"notes-repo", "Shared Lib"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		extras = append(extras, resolved)
	}
	return repo, home, extras
}

// TestAddDirAudit_PlannedPosture_ListsOneWeakeningPerDirInTextAndJSON covers
// the planned-posture AC: one BoundaryWeakening per added dir, option
// "--add-dir", carrying the host and container paths in text and in --json.
func TestAddDirAudit_PlannedPosture_ListsOneWeakeningPerDirInTextAndJSON(t *testing.T) {
	_, _, extras := addDirAuditEnv(t)

	posture, err := auditEngine().Audit(Options{Agent: "claude", AddDirs: extras})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	wantContainer := []string{"/repos/notes-repo", "/repos/shared-lib"}
	if len(posture.BoundaryWeakenings) != 2 {
		t.Fatalf("BoundaryWeakenings = %+v, want one entry per added dir", posture.BoundaryWeakenings)
	}
	for i, w := range posture.BoundaryWeakenings {
		if w.Option != "--add-dir" || !w.Active || w.HostPath != extras[i] || w.ContainerPath != wantContainer[i] || w.Effect == "" {
			t.Errorf("BoundaryWeakenings[%d] = %+v, want active --add-dir %s -> %s with an effect", i, w, extras[i], wantContainer[i])
		}
	}
	if posture.Network.Weakened {
		t.Errorf("--add-dir must not flip the network posture: %+v", posture.Network)
	}

	var added []MountPosture
	for _, m := range posture.Mounts {
		if m.Kind == MountKindAddedDir {
			added = append(added, m)
		}
	}
	if len(added) != 2 || added[0].Source != extras[0] || added[0].Destination != wantContainer[0] || added[0].ReadOnly || added[1].ReadOnly {
		t.Errorf("added-dir mounts = %+v, want two read-write binds %v -> %v", added, extras, wantContainer)
	}
	for _, v := range posture.Volumes {
		if v.Kind == MountKindAddedDir {
			t.Errorf("a host bind must not be listed as a named volume: %+v", v)
		}
	}

	var text bytes.Buffer
	if err := posture.WriteText(&text); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	for i := range extras {
		want := "--add-dir active=true: " + extras[i] + " -> " + wantContainer[i] + ": "
		if !strings.Contains(text.String(), want) {
			t.Errorf("text output missing %q:\n%s", want, text.String())
		}
	}

	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed struct {
		BoundaryWeakenings []map[string]any `json:"boundaryWeakenings"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.BoundaryWeakenings) != 2 {
		t.Fatalf("JSON boundaryWeakenings = %v, want two entries", parsed.BoundaryWeakenings)
	}
	for i, w := range parsed.BoundaryWeakenings {
		if w["option"] != "--add-dir" || w["hostPath"] != extras[i] || w["containerPath"] != wantContainer[i] || w["active"] != true {
			t.Errorf("JSON boundaryWeakenings[%d] = %v, want option/hostPath/containerPath", i, w)
		}
	}
}

// TestAddDirAudit_HostNetworkEntryKeepsItsExactJSONKeys pins the omitempty
// contract: the new hostPath/containerPath fields never appear on the
// pre-existing --host-network entry, and it still comes before --add-dir.
func TestAddDirAudit_HostNetworkEntryKeepsItsExactJSONKeys(t *testing.T) {
	_, _, extras := addDirAuditEnv(t)

	posture, err := auditEngine().Audit(Options{Agent: "claude", HostNetwork: true, AddDirs: extras[:1]})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed struct {
		BoundaryWeakenings []map[string]any `json:"boundaryWeakenings"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.BoundaryWeakenings) != 2 {
		t.Fatalf("boundaryWeakenings = %v, want --host-network then --add-dir", parsed.BoundaryWeakenings)
	}
	net := parsed.BoundaryWeakenings[0]
	if net["option"] != "--host-network" {
		t.Fatalf("first entry = %v, want --host-network", net)
	}
	keys := make([]string, 0, len(net))
	for k := range net {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"active", "effect", "option"}) {
		t.Errorf("--host-network entry keys = %v, want exactly active/effect/option (hostPath and containerPath are omitempty)", keys)
	}
	if parsed.BoundaryWeakenings[1]["option"] != "--add-dir" {
		t.Errorf("second entry = %v, want --add-dir", parsed.BoundaryWeakenings[1])
	}
}

// TestAddDirAudit_HostSocketMountEntryKeepsItsExactJSONKeys pins the omitempty
// contract for the observed host-socket-mount entry: it serializes with exactly
// active/effect/option, never the new hostPath/containerPath, even when an
// --add-dir entry sits next to it in the same observed posture.
func TestAddDirAudit_HostSocketMountEntryKeepsItsExactJSONKeys(t *testing.T) {
	posture := addDirObservedPosture(t, "cenci-sandbox:latest|bridge|runc||0\n"+
		"/host/repo::/workspace::true\n"+
		"/var/run/docker.sock::/var/run/docker.sock::true\n"+
		"/srv/alpha::/repos/alpha::true\n\n", Options{})

	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed struct {
		BoundaryWeakenings []map[string]any `json:"boundaryWeakenings"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var socket, addDir map[string]any
	for _, w := range parsed.BoundaryWeakenings {
		switch w["option"] {
		case "host-socket-mount":
			socket = w
		case "--add-dir":
			addDir = w
		}
	}
	if socket == nil || addDir == nil {
		t.Fatalf("boundaryWeakenings = %v, want a host-socket-mount and an --add-dir entry", parsed.BoundaryWeakenings)
	}
	keys := make([]string, 0, len(socket))
	for k := range socket {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"active", "effect", "option"}) {
		t.Errorf("host-socket-mount entry keys = %v, want exactly active/effect/option (hostPath and containerPath are omitempty)", keys)
	}
	if addDir["hostPath"] != "/srv/alpha" || addDir["containerPath"] != "/repos/alpha" {
		t.Errorf("--add-dir entry = %v, want hostPath/containerPath populated", addDir)
	}
}

// TestAddDirAudit_NoWeakenings_JSONIsAnEmptyListNotNull is the regression
// guard for the empty-list AC: no added dirs and no other weakening must
// serialize as [], never null (it already holds today; this pins it).
func TestAddDirAudit_NoWeakenings_JSONIsAnEmptyListNotNull(t *testing.T) {
	addDirAuditEnv(t)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := string(raw["boundaryWeakenings"]); got != "[]" {
		t.Errorf(`"boundaryWeakenings" = %s, want []`, got)
	}
}

// TestAddDirAudit_Explain_NamesTheMountAndStopsClaimingOnlyTheRepoIsMounted
// covers `cenci security explain`: the narrative lists each added dir with its
// host -> container paths and a blast-radius note, and the "only the current
// repo" sentence is replaced because it would no longer be true.
func TestAddDirAudit_Explain_NamesTheMountAndStopsClaimingOnlyTheRepoIsMounted(t *testing.T) {
	_, _, extras := addDirAuditEnv(t)

	withDir, err := auditEngine().Audit(Options{Agent: "claude", AddDirs: extras[:1]})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	var out bytes.Buffer
	if err := withDir.WriteExplanation(&out); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	for _, want := range []string{"⚠ --add-dir: " + extras[0] + " -> /repos/notes-repo: ", "Blast radius: the agent can read, modify and delete everything in that host"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("explanation missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Only the current repo") {
		t.Errorf("explanation still claims only the repo is mounted:\n%s", out.String())
	}

	baseline, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	out.Reset()
	if err := baseline.WriteExplanation(&out); err != nil {
		t.Fatalf("WriteExplanation: %v", err)
	}
	if !strings.Contains(out.String(), "Only the current repo") || strings.Contains(out.String(), "--add-dir") {
		t.Errorf("a launch without --add-dir must keep the original narrative:\n%s", out.String())
	}
}

func TestAddDirAudit_RejectedPath_IsUsageError(t *testing.T) {
	repo, _, _ := addDirAuditEnv(t)

	_, err := auditEngine().Audit(Options{Agent: "claude", AddDirs: []string{repo}})
	if !IsUsage(err) {
		t.Fatalf("Audit error = %v, want a usage error for an add-dir overlapping the primary workspace", err)
	}
}

// TestAddDirMountKindForDestination_OnlyPathsUnderReposComponentWiseAreAddedDirs
// is a direct classifier test: the sibling-prefix cases cannot be reached
// through a real launch, which only ever emits /repos/<slug>.
func TestAddDirMountKindForDestination_OnlyPathsUnderReposComponentWiseAreAddedDirs(t *testing.T) {
	for dest, want := range map[string]string{
		"/repos/alpha":     MountKindAddedDir,
		"/repos/a/b":       MountKindAddedDir,
		"/repos":           MountKindUnknown,
		"/repos-evil/x":    MountKindUnknown,
		"/reposx":          MountKindUnknown,
		"/workspace-repos": MountKindUnknown,
		"/workspace":       MountKindWorkspace,
	} {
		if got := mountKindForDestination(dest); got != want {
			t.Errorf("mountKindForDestination(%q) = %q, want %q", dest, got, want)
		}
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
// just a non-empty check (watch/docs/error-handling.md #446).
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

// dashEnvNames parses every token following a "-e" flag in args, handling both
// "-e NAME=value" and the bare "-e NAME" (#759) — mirrors classifyEnvNames's
// own parse rule — and returns the NAMEs in argv order.
func dashEnvNames(args []string) []string {
	var names []string
	for i := 0; i < len(args); i++ {
		if args[i] != "-e" || i+1 >= len(args) {
			continue
		}
		name := args[i+1]
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			name = name[:idx]
		}
		names = append(names, name)
	}
	return names
}

// bareEnvNames returns, in argv order, only the NAMEs passed in the value-less
// "-e NAME" form (no "=value") — the form secret passthroughs use (#759).
func bareEnvNames(args []string) []string {
	var names []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" && !strings.Contains(args[i+1], "=") {
			names = append(names, args[i+1])
		}
	}
	return names
}

// secretSuffixedEmissionViolations enumerates every "-e" name assembleExecEnv
// (built from forwarding, for every supported agent) and assembleEnv (every
// dindOn/ReseedCreds combination) emit, and returns one message per name that
// looks like a secret (suffix heuristic _KEY$|_TOKEN$|_SECRET$|_PASSWORD$) but
// is NOT registered in registered.Has — plus the set of every name seen, for
// the caller's anti-vacuity check. forwarding and registered are separate
// parameters so a test can forward a name through a test-only set and prove the
// check reports it against the production set.
func secretSuffixedEmissionViolations(forwarding, registered secretEnvSet) (violations []string, seen map[string]bool) {
	looksSecret := func(name string) bool {
		for _, suffix := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD"} {
			if strings.HasSuffix(name, suffix) {
				return true
			}
		}
		return false
	}

	seen = make(map[string]bool)
	scope := Scope{WorkspaceScope: "repo"}

	for _, agent := range sandbox.SupportedAgents {
		for _, name := range dashEnvNames(assembleExecEnv(agent, forwarding)) {
			seen[name] = true
			if looksSecret(name) && !registered.Has(name) {
				violations = append(violations, fmt.Sprintf("assembleExecEnv(%q) emitted secret-suffixed env %q, which is NOT in the resolved secret set (resolveSecretEnvSet, watch/internal/sandbox/launcher/secretenv.go) — register it or dry-run output will leak its value unredacted", agent, name))
			}
		}

		for _, dindOn := range []bool{false, true} {
			for _, reseed := range []bool{false, true} {
				opts := Options{Agent: agent, ReseedCreds: reseed}
				args := (&Engine{}).assembleEnv(agent, scope, opts, dindOn, DefaultSandboxPlugins())
				for _, name := range dashEnvNames(args) {
					seen[name] = true
					if looksSecret(name) && !registered.Has(name) {
						violations = append(violations, fmt.Sprintf("assembleEnv(%q, dindOn=%t, ReseedCreds=%t) emitted secret-suffixed env %q, which is NOT in the resolved secret set (resolveSecretEnvSet, watch/internal/sandbox/launcher/secretenv.go) — register it or dry-run output will leak its value unredacted", agent, dindOn, reseed, name))
					}
				}
			}
		}
	}
	return violations, seen
}

// TestForwardedEnvVarNames_CoversEverySecretSuffixedEnvName is a completeness
// guard over the resolved secret set (resolveSecretEnvSet, secretenv.go): it
// enumerates every "-e" name assembleExecEnv/assembleEnv actually emit today,
// across every supported agent and every dindOn/ReseedCreds combination, and
// fails any name that looks like a secret (matches the suffix heuristic
// _KEY$|_TOKEN$|_SECRET$|_PASSWORD$) but is NOT in the resolved set — the one
// set both isSecretEnvName (cenci audit's reporting) and redactSecretEnv
// (dryrun.go's `cenci open --dry-run` redaction) classify against. A miss
// there would silently render an unredacted secret value in dry-run output.
// The check lives in secretSuffixedEmissionViolations so a sub-test can prove
// it can fail (FOO_TOKEN).
//
// Known blind spots (deliberately out of scope, per ticket #760's Q&A,
// restated against the #1201 resolved-set shape):
//
//  1. An oddly-named future BUILT-IN secret that matches none of the four
//     suffixes (_KEY/_TOKEN/_SECRET/_PASSWORD) stays undetected by this
//     heuristic — accepted tradeoff over a second hand-maintained allowlist,
//     since there is nothing to keep in sync against the single resolved set.
//     The suffix heuristic applies to built-in names only: a name from the
//     user's sandboxEnv.forward config (#1016) is registered by Has
//     unconditionally, never by suffix, so a non-suffixed configured name such
//     as MY_THING is not a blind spot (see the configured-names sub-test).
//  2. A future "-e" forward whose host gate var isn't also t.Setenv'd below
//     is silently dropped from the enumeration this test drives — whoever
//     adds a new gated forward to assembleExecEnv/assembleEnv MUST also add
//     its host env var to the t.Setenv block below, or this test will never
//     see the emitted name at all.
//  3. "-e" names emitted OUTSIDE assembleExecEnv/assembleEnv are out of
//     scope for this test (AC #1): assembleVolumeMounts's XDG_RUNTIME_DIR
//     and CENCI_SOCKET_DIR forwards and buildAgentExecArgv's
//     DISABLE_UPDATES forward (all launch.go), and engine.go's
//     maintenanceRunArgs. None of the four emits a secret-suffixed name
//     today.
func TestForwardedEnvVarNames_CoversEverySecretSuffixedEnvName(t *testing.T) {
	// Every host gate var assembleExecEnv/assembleEnv currently read behind an
	// "only forward when set" conditional must be set here, or its forwarded
	// name never appears in the argv this test parses (blind spot 2 above).
	t.Setenv("CONTEXT7_API_KEY", "sentinel-context7")
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-anthropic")
	t.Setenv("OPENAI_API_KEY", "sentinel-openai")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")

	t.Run("production set registers every emitted secret-suffixed name", func(t *testing.T) {
		secrets := builtinSecretEnvSet()
		violations, seen := secretSuffixedEmissionViolations(secrets, secrets)
		for _, v := range violations {
			t.Error(v)
		}

		// Anti-vacuity guard: assert each of the three currently-registered
		// secrets was actually observed emitted somewhere above. This is a
		// per-name presence check (not a bare "not empty" — watch/docs/
		// test-strategy.md #914), and deliberately a SUBSET check rather than
		// set equality: a legitimately added-and-registered future secret must
		// not fail this test.
		for _, want := range []string{"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
			if !seen[want] {
				t.Errorf("expected %q to be emitted by some agent/branch combination (gate var not driving emission?), got emitted set %v", want, seen)
			}
		}
	})

	// Config-supplied names (#1016) are registered by Has unconditionally: the
	// non-suffixed MY_THING matches none of the four suffixes, so only the
	// configuration can be what classifies it. Driving the emission check against
	// the merged set proves the configured name is both emitted and registered,
	// and the built-in set's own Has proves the suffix heuristic never did it.
	t.Run("configured names are registered unconditionally, not by suffix", func(t *testing.T) {
		const plain, suffixed = "MY_THING", "TAVILY_API_KEY"
		t.Setenv(plain, "sentinel-my-thing")
		t.Setenv(suffixed, "sentinel-tavily")
		configured, err := resolveSecretEnvSet([]string{plain, suffixed})
		if err != nil {
			t.Fatalf("resolveSecretEnvSet: %v", err)
		}

		if builtinSecretEnvSet().Has(plain) {
			t.Fatalf("the built-in set classifies %s as a secret; this arm needs a name only configuration can register", plain)
		}
		for _, name := range []string{plain, suffixed} {
			if !configured.Has(name) {
				t.Errorf("Has(%q) = false for a configured name, want true unconditionally", name)
			}
		}

		violations, seen := secretSuffixedEmissionViolations(configured, configured)
		for _, v := range violations {
			t.Error(v)
		}
		for _, name := range []string{plain, suffixed} {
			if !seen[name] {
				t.Errorf("configured name %q was never observed emitted (anti-vacuity); seen %v", name, seen)
			}
		}
	})

	// Proves the guard can fail: a test-only set forwards FOO_TOKEN (every
	// agent), which the production set does not register, so the check against
	// the production set must report it once per agent's exec env.
	t.Run("an unregistered FOO_TOKEN forward is reported", func(t *testing.T) {
		t.Setenv("FOO_TOKEN", "sentinel-foo")
		forwarding := newSecretEnvSet([]secretEnvSpec{{Name: "FOO_TOKEN"}})
		violations, seen := secretSuffixedEmissionViolations(forwarding, builtinSecretEnvSet())
		if !seen["FOO_TOKEN"] {
			t.Fatalf("FOO_TOKEN forwarded through the test-only set was never observed emitted; seen %v", seen)
		}
		var foo int
		for _, v := range violations {
			if strings.Contains(v, `"FOO_TOKEN"`) {
				foo++
			}
		}
		if foo != len(sandbox.SupportedAgents) {
			t.Errorf("FOO_TOKEN reported %d times, want once per supported agent (%d); violations: %v", foo, len(sandbox.SupportedAgents), violations)
		}
	})
}

// TestAudit_EnvSecretClassification_ContextKeySecretTermNot covers the secret
// classification split between create-time env and forwarded exec env (#598,
// reshaped by #1201). With the host CONTEXT7_API_KEY set: the key is no longer
// forwarded at container creation, so it is ABSENT from posture.Env (JSON
// "env"); it is forwarded per exec, so it is PRESENT in ForwardedEnv (JSON
// "forwardedEnv") with Secret:true; an ordinary create-time name (TERM) stays
// in Env with Secret:false; and the sentinel value appears nowhere in the
// marshaled posture.
func TestAudit_EnvSecretClassification_ContextKeySecretTermNot(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	const contextSecret = "sentinel-secret-value-envsecret1"
	t.Setenv("CONTEXT7_API_KEY", contextSecret)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	var termEnv *EnvVarName
	for i := range posture.Env {
		switch posture.Env[i].Name {
		case "CONTEXT7_API_KEY":
			t.Errorf("Env must not carry CONTEXT7_API_KEY (no create-time forward, #1201), got %+v", posture.Env)
		case "TERM":
			termEnv = &posture.Env[i]
		}
	}
	if termEnv == nil {
		t.Fatalf("expected TERM in Env, got %+v", posture.Env)
	}
	if termEnv.Secret {
		t.Errorf("TERM Env entry Secret = true, want false (not a create-time secret name)")
	}

	var forwarded *ForwardedEnvVar
	for i := range posture.ForwardedEnv {
		if posture.ForwardedEnv[i].Name == "CONTEXT7_API_KEY" {
			forwarded = &posture.ForwardedEnv[i]
		}
	}
	if forwarded == nil {
		t.Fatalf("expected CONTEXT7_API_KEY in ForwardedEnv, got %+v", posture.ForwardedEnv)
	}
	if !forwarded.Secret {
		t.Errorf("CONTEXT7_API_KEY ForwardedEnv entry Secret = false, want true")
	}

	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), contextSecret) {
		t.Errorf("posture JSON leaks the CONTEXT7_API_KEY sentinel value:\n%s", data)
	}
}

// TestClassifyEnvNames_BareSecretToken covers ticket #759: once
// assembleExecEnv/assembleEnv emit a bare "-e NAME" token (no "=value") for
// a secret env, classifyEnvNames must still parse the name correctly and
// mark it Secret:true -- an explicit assertion rather than an inference
// from other passing tests (AC6).
func TestClassifyEnvNames_BareSecretToken(t *testing.T) {
	envs := classifyEnvNames([]string{"-e", "OPENAI_API_KEY"}, builtinSecretEnvSet())
	if len(envs) != 1 {
		t.Fatalf("classifyEnvNames([-e OPENAI_API_KEY]) = %+v, want exactly one entry", envs)
	}
	if envs[0].Name != "OPENAI_API_KEY" {
		t.Errorf("Name = %q, want %q", envs[0].Name, "OPENAI_API_KEY")
	}
	if !envs[0].Secret {
		t.Errorf("Secret = false, want true for OPENAI_API_KEY")
	}
}

// TestClassifyEnvNames_CreateTimeArgs_ClassifiesAgainstResolvedSet keeps
// direct coverage of create-time secret classification (#1201): since the
// create-time path no longer emits any built-in secret, classifyEnvNames
// called on synthetic create-time args is the only way to exercise Secret:true
// there. The "-e NAME=value" form (TERM) and the bare "-e NAME" form
// (OPENAI_API_KEY) both parse, and classification is agent-independent: a name
// registered but scoped away from the audited agent (ANTHROPIC_API_KEY for
// claude, which never forwards it) is still Secret:true, so dry-run redaction
// is not weakened by per-agent forwarding.
func TestClassifyEnvNames_CreateTimeArgs_ClassifiesAgainstResolvedSet(t *testing.T) {
	secrets := builtinSecretEnvSet()

	got := classifyEnvNames([]string{"-e", "TERM=xterm-256color", "-e", "OPENAI_API_KEY"}, secrets)
	want := []EnvVarName{{Name: "TERM", Secret: false}, {Name: "OPENAI_API_KEY", Secret: true}}
	if !slices.Equal(got, want) {
		t.Errorf("classifyEnvNames(TERM=..., OPENAI_API_KEY) = %+v, want %+v", got, want)
	}

	// Classification takes no agent: ANTHROPIC_API_KEY is secret even though a
	// claude launch never forwards it.
	scoped := classifyEnvNames([]string{"-e", "ANTHROPIC_API_KEY"}, secrets)
	if !slices.Equal(scoped, []EnvVarName{{Name: "ANTHROPIC_API_KEY", Secret: true}}) {
		t.Errorf("classifyEnvNames(ANTHROPIC_API_KEY) = %+v, want exactly one Secret:true entry (classification is agent-independent)", scoped)
	}
}

// setAllSecretHostKeys sets the three built-in secret host vars to value (or
// to the empty string) for the duration of the test.
func setAllSecretHostKeys(t *testing.T, value func(name string) string) {
	t.Helper()
	for _, name := range []string{"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(name, value(name))
	}
}

// TestForwardedSecretNames_ExactPerAgentAndOrder pins the per-agent forwarding
// matrix exactly (#1201), including the argv ORDER the exec goldens require
// (CONTEXT7, then ANTHROPIC, then OPENAI). Unit-level on purpose: the
// opencode row is unreachable black-box (#1219 refuses it in
// resolveLaunchContext). With all three host keys set: claude forwards
// {CONTEXT7_API_KEY}, codex {CONTEXT7_API_KEY, OPENAI_API_KEY}, opencode all
// three. With all three set to the empty string every agent forwards nothing,
// in the create-time argv as well as the attach argv. The create-time argv
// carries no secret name for any agent in either case.
func TestForwardedSecretNames_ExactPerAgentAndOrder(t *testing.T) {
	secrets := builtinSecretEnvSet()
	scope := Scope{WorkspaceScope: "repo"}
	e := &Engine{Runtime: "docker", Stderr: &bytes.Buffer{}}

	t.Run("all three keys set", func(t *testing.T) {
		setAllSecretHostKeys(t, func(name string) string { return "sentinel-" + name })
		cases := []struct {
			agent string
			want  []string
		}{
			{"claude", []string{"CONTEXT7_API_KEY"}},
			{"codex", []string{"CONTEXT7_API_KEY", "OPENAI_API_KEY"}},
			{"opencode", []string{"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"}},
		}
		for _, tc := range cases {
			t.Run(tc.agent, func(t *testing.T) {
				if got := bareEnvNames(assembleExecEnv(tc.agent, secrets)); !slices.Equal(got, tc.want) {
					t.Errorf("assembleExecEnv(%q) bare -e names = %v, want exactly %v (in this order)", tc.agent, got, tc.want)
				}
				create := e.assembleEnv(tc.agent, scope, Options{Agent: tc.agent}, false, DefaultSandboxPlugins())
				if got := bareEnvNames(create); len(got) != 0 {
					t.Errorf("assembleEnv(%q) bare -e names = %v, want none (no create-time secret forward, #1201)", tc.agent, got)
				}
				if strings.Contains(strings.Join(create, " "), "CONTEXT7_API_KEY") {
					t.Errorf("assembleEnv(%q) mentions CONTEXT7_API_KEY, want no create-time forward; got %v", tc.agent, create)
				}
			})
		}
	})

	t.Run("all three keys empty", func(t *testing.T) {
		setAllSecretHostKeys(t, func(string) string { return "" })
		for _, agent := range sandbox.SupportedAgents {
			if got := bareEnvNames(assembleExecEnv(agent, secrets)); len(got) != 0 {
				t.Errorf("assembleExecEnv(%q) bare -e names = %v with empty host keys, want {}", agent, got)
			}
			create := e.assembleEnv(agent, scope, Options{Agent: agent}, false, DefaultSandboxPlugins())
			if got := bareEnvNames(create); len(got) != 0 {
				t.Errorf("assembleEnv(%q) bare -e names = %v with empty host keys, want {}", agent, got)
			}
		}
	})
}

// TestAudit_ForwardedEnvAgreesWithAttachArgv_EveryAgent pins that Audit's
// report and the real attach argv can never drift (#1201): for every agent in
// sandbox.SupportedAgents, with all three host keys set, the secret names in
// Audit(...).ForwardedEnv equal, as a set, the bare "-e" secret names in the
// attach argv planArgvs builds for the same agent. resolveLaunchContext
// refuses opencode (#1219) while Audit accepts it, so the launchCtx is built
// directly against an absolute-path fake runtime (no running container, so
// planArgvs takes the create branch and still returns the attach argv).
func TestAudit_ForwardedEnvAgreesWithAttachArgv_EveryAgent(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	t.Setenv("FAKE_PS", "")
	setAllSecretHostKeys(t, func(name string) string { return "sentinel-" + name })
	secrets := builtinSecretEnvSet()

	for _, agent := range sandbox.SupportedAgents {
		t.Run(agent, func(t *testing.T) {
			eng, _ := auditEngineWithFakeRuntime(t)

			posture, err := eng.Audit(Options{Agent: agent})
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			var auditNames []string
			for _, f := range posture.ForwardedEnv {
				if f.Secret {
					auditNames = append(auditNames, f.Name)
				}
			}

			ctx := launchCtx{
				Agent:   agent,
				Home:    home,
				Scope:   ComputeScope(agent, "", repo, home),
				Plugins: DefaultSandboxPlugins(),
				Secrets: secrets,
			}
			_, attachArgv, attaching, err := eng.planArgvs(ctx, Options{Agent: agent}, "", "", false)
			if err != nil {
				t.Fatalf("planArgvs: %v", err)
			}
			if attaching {
				t.Fatalf("planArgvs attached to a running container, want the create branch (FAKE_PS empty)")
			}
			var argvNames []string
			for _, name := range bareEnvNames(attachArgv) {
				if secrets.Has(name) {
					argvNames = append(argvNames, name)
				}
			}

			slices.Sort(auditNames)
			slices.Sort(argvNames)
			if !slices.Equal(auditNames, argvNames) {
				t.Errorf("Audit ForwardedEnv secret names = %v, attach argv bare secret names = %v; want the same set", auditNames, argvNames)
			}
			if len(argvNames) == 0 {
				t.Errorf("attach argv forwarded no secret name for %q with all three host keys set (vacuous agreement)", agent)
			}
		})
	}
}

// -- ticket #1016: sandboxEnv.forward classification and weakenings ----------

// configuredForwardAuditFixture isolates HOME, enters a fresh git repo and
// writes a fleet config whose sandboxEnv.forward lists names, so Audit reads it
// through the real loader. The three built-in host keys are emptied so only the
// configured names (and whatever a test sets) are forwarded.
func configuredForwardAuditFixture(t *testing.T, names ...string) {
	t.Helper()
	repo := newAuditTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	setAllSecretHostKeys(t, func(string) string { return "" })
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	writeFleetConfig(t, `{"sandboxEnv":{"forward":[`+strings.Join(quoted, ",")+`]}}`)
}

// forwardWeakenings filters a posture's weakenings down to the
// sandboxEnv.forward entries.
func forwardWeakenings(p Posture) []BoundaryWeakening {
	var out []BoundaryWeakening
	for _, w := range p.BoundaryWeakenings {
		if w.Option == "sandboxEnv.forward" {
			out = append(out, w)
		}
	}
	return out
}

// TestAudit_ConfiguredForward_NonSuffixedNameIsSecretInForwardedEnv covers the
// classification AC: MY_THING matches none of the secret suffixes, yet a
// configured name is Secret:true in forwardedEnv, and only when it would really
// be forwarded (set and non-empty); the unset configured name has no entry.
func TestAudit_ConfiguredForward_NonSuffixedNameIsSecretInForwardedEnv(t *testing.T) {
	configuredForwardAuditFixture(t, "MY_THING", "UNSET_THING")
	const sentinel = "sk-cenci-sentinel-my-thing"
	t.Setenv("MY_THING", sentinel)
	t.Setenv("UNSET_THING", "")

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	var mine *ForwardedEnvVar
	for i, f := range posture.ForwardedEnv {
		switch f.Name {
		case "MY_THING":
			mine = &posture.ForwardedEnv[i]
		case "UNSET_THING":
			t.Errorf("forwardedEnv lists the unset configured name UNSET_THING; audit lists only what would actually be forwarded: %+v", posture.ForwardedEnv)
		}
	}
	if mine == nil {
		t.Fatalf("forwardedEnv has no MY_THING entry: %+v", posture.ForwardedEnv)
	}
	if !mine.Secret {
		t.Errorf("forwardedEnv MY_THING Secret = false, want true (every configured name is secret)")
	}

	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), sentinel) {
		t.Errorf("posture JSON leaks the MY_THING sentinel value:\n%s", data)
	}
}

func TestAudit_ConfiguredForward_PlannedWeakeningNamesTheVariable(t *testing.T) {
	configuredForwardAuditFixture(t, "MY_THING", "TAVILY_API_KEY", "UNSET_THING")
	t.Setenv("MY_THING", "sentinel-1")
	t.Setenv("TAVILY_API_KEY", "sentinel-2")
	t.Setenv("UNSET_THING", "")

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if posture.Basis != PostureBasisPlanned {
		t.Fatalf("Basis = %q, want planned", posture.Basis)
	}
	got := forwardWeakenings(posture)
	if len(got) != 2 {
		t.Fatalf("sandboxEnv.forward weakenings = %+v, want one per forwarded configured name (MY_THING, TAVILY_API_KEY) and none for the unset one", got)
	}
	for i, name := range []string{"MY_THING", "TAVILY_API_KEY"} {
		if got[i].Option != "sandboxEnv.forward" || !got[i].Active {
			t.Errorf("weakening[%d] = %+v, want option sandboxEnv.forward active", i, got[i])
		}
		if !strings.Contains(got[i].Effect, name) {
			t.Errorf("weakening[%d] effect %q does not name %s (config order)", i, got[i].Effect, name)
		}
	}
	if len(posture.BoundaryWeakenings) != 2 {
		t.Errorf("BoundaryWeakenings = %+v, want only the two configured forwards", posture.BoundaryWeakenings)
	}
}

// TestAudit_ConfiguredForward_WeakeningJSONKeysAreUnchanged pins AC13/14 for the
// new entry: it reuses BoundaryWeakening, so the entry's key set is exactly the
// existing {option, active, effect}.
func TestAudit_ConfiguredForward_WeakeningJSONKeysAreUnchanged(t *testing.T) {
	configuredForwardAuditFixture(t, "MY_THING")
	t.Setenv("MY_THING", "sentinel")

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var parsed struct {
		BoundaryWeakenings []map[string]any `json:"boundaryWeakenings"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(parsed.BoundaryWeakenings) != 1 {
		t.Fatalf("boundaryWeakenings = %v, want exactly one entry", parsed.BoundaryWeakenings)
	}
	keys := make([]string, 0, 3)
	for k := range parsed.BoundaryWeakenings[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"active", "effect", "option"}; !slices.Equal(keys, want) {
		t.Errorf("weakening entry keys = %v, want %v", keys, want)
	}
}

func TestAudit_ConfiguredForward_TextOutputShowsWeakeningsAndNoBaselineLine(t *testing.T) {
	configuredForwardAuditFixture(t, "MY_THING")
	t.Setenv("MY_THING", "sentinel")

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	var buf bytes.Buffer
	if err := posture.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "⚠ Boundary weakenings") {
		t.Errorf("text output lacks the ⚠ Boundary weakenings section:\n%s", out)
	}
	if strings.Contains(out, "none (default-safe baseline)") {
		t.Errorf("text output prints the reassuring baseline line while a user-added secret crosses the boundary:\n%s", out)
	}
	if !strings.Contains(out, "MY_THING") {
		t.Errorf("text output does not name MY_THING:\n%s", out)
	}
}

// TestAudit_ConfiguredForward_UnsetOrEmptyNameAddsNothing: a configured name
// that is not set (or is empty) is never forwarded, so audit reports neither a
// forwardedEnv nor a weakening entry, and the empty weakenings list still
// serializes as [] (the nil-slice trap, watch/docs/go-gotchas.md).
func TestAudit_ConfiguredForward_UnsetOrEmptyNameAddsNothing(t *testing.T) {
	configuredForwardAuditFixture(t, "MY_THING")
	t.Setenv("MY_THING", "")

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	for _, f := range posture.ForwardedEnv {
		if f.Name == "MY_THING" {
			t.Errorf("forwardedEnv lists MY_THING although it is empty on the host: %+v", posture.ForwardedEnv)
		}
	}
	if posture.BoundaryWeakenings == nil || len(posture.BoundaryWeakenings) != 0 {
		t.Errorf("BoundaryWeakenings = %#v, want a non-nil empty slice", posture.BoundaryWeakenings)
	}
	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"boundaryWeakenings":[]`) {
		t.Errorf("posture JSON must carry \"boundaryWeakenings\":[] when nothing is forwarded:\n%s", data)
	}
}

// TestAudit_ConfiguredForward_BuiltinDuplicates covers the edge cases: a
// configured built-in that is already forwarded to the audited agent widens
// nothing (no weakening), one that is not forwarded to that agent by default
// gets exactly one entry.
func TestAudit_ConfiguredForward_BuiltinDuplicates(t *testing.T) {
	cases := []struct {
		agent    string
		name     string
		wantNone bool
	}{
		{"claude", "CONTEXT7_API_KEY", true},
		{"claude", "OPENAI_API_KEY", false},
		{"claude", "ANTHROPIC_API_KEY", false},
		{"codex", "OPENAI_API_KEY", true},
		{"codex", "ANTHROPIC_API_KEY", false},
		{"opencode", "ANTHROPIC_API_KEY", true},
		{"opencode", "OPENAI_API_KEY", true},
		{"opencode", "CONTEXT7_API_KEY", true},
	}
	for _, tc := range cases {
		t.Run(tc.agent+"/"+tc.name, func(t *testing.T) {
			configuredForwardAuditFixture(t, tc.name)
			t.Setenv(tc.name, "sentinel-"+tc.name)
			// codex's credential check is non-fatal for Audit, so no auth file
			// is needed.

			posture, err := auditEngine().Audit(Options{Agent: tc.agent})
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			got := forwardWeakenings(posture)
			switch {
			case tc.wantNone && len(got) != 0:
				t.Errorf("%s already forwarded to %s by default, got weakening(s) %+v, want none", tc.name, tc.agent, got)
			case !tc.wantNone && len(got) != 1:
				t.Errorf("%s is not forwarded to %s by default, got weakening(s) %+v, want exactly one", tc.name, tc.agent, got)
			case !tc.wantNone && !strings.Contains(got[0].Effect, tc.name):
				t.Errorf("weakening effect %q does not name %s", got[0].Effect, tc.name)
			}

			// Whether or not it is a weakening, the name is forwarded for this
			// agent now and classified secret.
			var forwarded bool
			for _, f := range posture.ForwardedEnv {
				if f.Name == tc.name {
					forwarded = true
					if !f.Secret {
						t.Errorf("forwardedEnv %s Secret = false, want true", tc.name)
					}
				}
			}
			if !forwarded {
				t.Errorf("forwardedEnv lacks %s for %s after configuring it: %+v", tc.name, tc.agent, posture.ForwardedEnv)
			}
		})
	}
}

// TestAudit_ConfiguredForward_RunningBasisCopiesWeakenings covers the
// "running" half of the weakening AC: per-exec state has no inspect source, so
// the observed posture carries the planned sandboxEnv.forward entries next to
// whatever the inspect probe found (here a host-network container).
func TestAudit_ConfiguredForward_RunningBasisCopiesWeakenings(t *testing.T) {
	configuredForwardAuditFixture(t, "MY_THING", "UNSET_THING")
	t.Setenv("MY_THING", "sentinel")
	t.Setenv("UNSET_THING", "")

	scope := ComputeScope("claude", "", mustGetwd(t), os.Getenv("HOME"))
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")

	t.Run("bridge container", func(t *testing.T) {
		t.Setenv("FAKE_OBSERVED_POSTURE", "cenci-sandbox:latest|bridge|runc||0\n\n")
		posture, err := eng.Audit(Options{Agent: "claude"})
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if posture.Basis != PostureBasisRunning {
			t.Fatalf("Basis = %q, want running", posture.Basis)
		}
		got := forwardWeakenings(posture)
		if len(got) != 1 || !strings.Contains(got[0].Effect, "MY_THING") || !got[0].Active {
			t.Errorf("running-basis sandboxEnv.forward weakenings = %+v, want exactly one active entry naming MY_THING", got)
		}
		if len(posture.BoundaryWeakenings) != 1 {
			t.Errorf("BoundaryWeakenings = %+v, want only the configured forward", posture.BoundaryWeakenings)
		}
	})

	t.Run("host-network container keeps its own weakening alongside", func(t *testing.T) {
		t.Setenv("FAKE_OBSERVED_POSTURE", "cenci-sandbox:latest|host|runc||0\n\n")
		posture, err := eng.Audit(Options{Agent: "claude"})
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		var options []string
		for _, w := range posture.BoundaryWeakenings {
			options = append(options, w.Option)
		}
		if want := []string{"--host-network", "sandboxEnv.forward"}; !slices.Equal(options, want) {
			t.Errorf("weakening options = %v, want %v", options, want)
		}
	})

	t.Run("an unforwarded name adds nothing on the running basis either", func(t *testing.T) {
		t.Setenv("MY_THING", "")
		t.Setenv("FAKE_OBSERVED_POSTURE", "cenci-sandbox:latest|bridge|runc||0\n\n")
		posture, err := eng.Audit(Options{Agent: "claude"})
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if posture.Basis != PostureBasisRunning {
			t.Fatalf("Basis = %q, want running", posture.Basis)
		}
		if posture.BoundaryWeakenings == nil || len(posture.BoundaryWeakenings) != 0 {
			t.Errorf("BoundaryWeakenings = %#v, want a non-nil empty slice", posture.BoundaryWeakenings)
		}
	})
}

// mustGetwd returns the test's working directory; the audit fixtures chdir
// into a resolved repo, so it equals the repo path ComputeScope derives.
func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return wd
}

// TestAudit_ConfiguredForward_AttendedStaysNonSecret covers the last edge
// case: with a sandboxEnv.forward config present, CENCI_ATTENDED still
// classifies Secret:false and is still reported in forwardedEnv.
func TestAudit_ConfiguredForward_AttendedStaysNonSecret(t *testing.T) {
	repo := newAuditTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	setAllSecretHostKeys(t, func(string) string { return "" })
	t.Setenv("MY_THING", "sentinel")
	writeFleetConfig(t, `{"sandboxEnv":{"forward":["MY_THING"]},"planning":{"attended":true}}`)

	secrets, err := loadSecretEnvSet()
	if err != nil {
		t.Fatalf("loadSecretEnvSet: %v", err)
	}
	if isSecretEnvName(secrets, "CENCI_ATTENDED") {
		t.Errorf("CENCI_ATTENDED classified secret with a sandboxEnv.forward config present, want non-secret")
	}

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	var attended *ForwardedEnvVar
	for i, f := range posture.ForwardedEnv {
		if f.Name == "CENCI_ATTENDED" {
			attended = &posture.ForwardedEnv[i]
		}
	}
	if attended == nil {
		t.Fatalf("forwardedEnv lacks CENCI_ATTENDED: %+v", posture.ForwardedEnv)
	}
	if attended.Secret {
		t.Errorf("forwardedEnv CENCI_ATTENDED Secret = true, want false")
	}
}

// TestAudit_MalformedForwardConfig_HardErrorBeforeAnyRuntimeCall: Audit fails
// closed on a malformed sandboxEnv.forward with a plain error, and (the engine
// has a resolved runtime) makes no ps/inspect call before failing.
func TestAudit_MalformedForwardConfig_HardErrorBeforeAnyRuntimeCall(t *testing.T) {
	repo := newAuditTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	writeFleetConfig(t, `{"sandboxEnv":{"forward":["FOO=s3cr3t-sentinel"]}}`)
	eng, callLog := auditEngineWithFakeRuntime(t)

	_, err := eng.Audit(Options{Agent: "claude"})
	if err == nil {
		t.Fatalf("Audit succeeded, want a hard config error")
	}
	if IsUsage(err) {
		t.Errorf("error %q is a usage error, want a plain error (exit 1)", err)
	}
	if !strings.Contains(err.Error(), "FOO=<redacted>") || strings.Contains(err.Error(), "s3cr3t-sentinel") {
		t.Errorf("error %q must show FOO=<redacted> and never the value", err)
	}
	if lines := readCallLog(t, callLog); len(lines) != 0 {
		t.Errorf("Audit made runtime calls before failing on the config: %v", lines)
	}
}

// -- env names only -------------------------------------------------------

// TestAudit_EnvReportedAsNamesOnly covers the env lists: names only, never
// values. The create-time Env list still reports its names (TERM), but
// CONTEXT7_API_KEY (an optional passthrough) is no longer one of them since
// #1201 — it shows up by name in ForwardedEnv once set on the host, and its
// value is nowhere in the marshaled posture.
func TestAudit_EnvReportedAsNamesOnly(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	const contextSecret = "sentinel-secret-value-xyz123"
	t.Setenv("CONTEXT7_API_KEY", contextSecret)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if len(posture.Env) == 0 {
		t.Fatalf("Env is empty, want at least the create-time env names")
	}
	for _, ev := range posture.Env {
		if ev.Name == "CONTEXT7_API_KEY" {
			t.Errorf("Env must not carry CONTEXT7_API_KEY (no create-time forward, #1201), got %+v", posture.Env)
		}
	}
	var forwarded *ForwardedEnvVar
	for i := range posture.ForwardedEnv {
		if posture.ForwardedEnv[i].Name == "CONTEXT7_API_KEY" {
			forwarded = &posture.ForwardedEnv[i]
		}
	}
	if forwarded == nil {
		t.Fatalf("expected CONTEXT7_API_KEY in ForwardedEnv names, got %+v", posture.ForwardedEnv)
	}
	if !forwarded.Secret {
		t.Errorf("CONTEXT7_API_KEY ForwardedEnv entry Secret = false, want true")
	}

	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), contextSecret) {
		t.Errorf("posture JSON leaks the CONTEXT7_API_KEY sentinel value:\n%s", data)
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
// content-specific regression test (per watch/docs/error-handling.md's
// rule on error/content-specific assertions (#446)): it seeds real secret
// VALUES (never just checking for non-empty output) into credential files
// and provider API key env vars, then asserts neither WriteText's text
// output nor the marshaled JSON contains any of those sentinel byte
// strings anywhere.
func TestAudit_SecretLeakRegression_NeverEmitsSecretValues(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	const contextSecret = "sentinel-secret-value-xyz123"
	const openaiSecret = "sentinel-secret-value-openai456"
	t.Setenv("CONTEXT7_API_KEY", contextSecret)
	t.Setenv("OPENAI_API_KEY", openaiSecret)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"`+contextSecret+`"}`)
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"token":"`+openaiSecret+`"}`)

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
	for _, secret := range []string{contextSecret, openaiSecret} {
		if strings.Contains(crossTextBuf.String(), secret) {
			t.Errorf("cross-agent text output leaks a sentinel secret value:\n%s", crossTextBuf.String())
		}
	}

	crossJSONBytes, err := json.MarshalIndent(crossPosture, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent (cross-agent): %v", err)
	}
	for _, secret := range []string{contextSecret, openaiSecret} {
		if strings.Contains(string(crossJSONBytes), secret) {
			t.Errorf("cross-agent JSON output leaks a sentinel secret value:\n%s", crossJSONBytes)
		}
	}
}

// TestAudit_JSON_EmptySlicesSerializeAsEmptyArrayNotNull is a dedicated,
// content-specific regression test (per watch/docs/error-handling.md's
// rule on error/content-specific assertions (#446)): for the default (no
// --dind, no --host-network) case, boundaryWeakenings must be empty — Go's
// json.Marshal serializes a nil slice as `null`, not `[]`, and since "no
// boundary weakenings" is the common default-safe case, a nil slice here
// would routinely break jq '.boundaryWeakenings[]'-style scripting
// consumers. Asserted via strings.Contains on the raw JSON bytes (not by
// unmarshaling back into a Go slice, which wouldn't distinguish null from []
// after unmarshaling).
//
// forwardedEnv is covered differently since #1087: a real Audit can no longer
// produce an empty one, because CENCI_ATTENDED is forwarded on every exec
// regardless of host state. Its nil-slice guarantee therefore lives at the
// producer — forwardedEnvVars must return a non-nil slice — plus a marshal of
// that same emptied slice to prove the serialization itself, so the `null`
// regression stays closed for any future Posture whose ForwardedEnv genuinely
// is empty.
func TestAudit_JSON_EmptySlicesSerializeAsEmptyArrayNotNull(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Explicitly clear every provider API key assembleExecEnv would forward
	// for "claude" (CONTEXT7_API_KEY) so forwardedEnv carries only the
	// unconditional CENCI_ATTENDED entry regardless of the host process's own
	// environment.
	t.Setenv("CONTEXT7_API_KEY", "")
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
	if strings.Contains(out, `"boundaryWeakenings":null`) {
		t.Errorf("JSON output contains \"boundaryWeakenings\":null; regression — must be an empty array; got:\n%s", out)
	}

	// forwardedEnv's nil-slice guarantee, at the producer (#1087).
	if posture.ForwardedEnv == nil {
		t.Fatal("posture.ForwardedEnv is nil; forwardedEnvVars must always return an allocated slice so the JSON never serializes as null")
	}
	emptied := posture
	emptied.ForwardedEnv = posture.ForwardedEnv[:0]
	emptiedData, err := json.Marshal(emptied)
	if err != nil {
		t.Fatalf("Marshal (emptied forwardedEnv): %v", err)
	}
	emptiedOut := string(emptiedData)
	if !strings.Contains(emptiedOut, `"forwardedEnv":[]`) {
		t.Errorf("JSON output does not contain \"forwardedEnv\":[], want an explicitly empty array (not null); got:\n%s", emptiedOut)
	}
	if strings.Contains(emptiedOut, `"forwardedEnv":null`) {
		t.Errorf("JSON output contains \"forwardedEnv\":null; regression — must be an empty array; got:\n%s", emptiedOut)
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

// -- ticket #627: observed vs planned posture -------------------------------
//
// When the scoped container is actually running, Audit derives its posture
// from real inspect data (basis:"running") instead of the hypothetical
// next-launch derivation (basis:"planned"). These tests drive Audit against
// auditEngineWithFakeRuntime's fake docker (faketest_test.go) so they can
// script ps/inspect responses without a real container runtime.
//
// NOTE (red phase): PostureBasisRunning/PostureBasisPlanned, Posture.Basis,
// and Posture.InspectWarning do not exist yet (land in audit.go, a later
// phase) — every test below fails to COMPILE until then. That is the
// intended red-phase state.

// TestAudit_ObservedMode_RunningHostNetwork_BasisRunningWeakenedWithoutFlag
// covers AC #1: a running host-network container must be reported as
// weakened purely from observed inspect data, without the caller repeating
// --host-network.
func TestAudit_ObservedMode_RunningHostNetwork_BasisRunningWeakenedWithoutFlag(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "cenci-sandbox:latest|host|runc||0\n"+
		"/host/repo::/workspace::true\n\n")

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if posture.Basis != PostureBasisRunning {
		t.Errorf("Basis = %q, want %q for a running scoped container", posture.Basis, PostureBasisRunning)
	}
	if posture.Network.Mode != NetworkModeHost || !posture.Network.Weakened {
		t.Errorf("Network = %+v, want host/weakened from the observed inspect data — no --host-network flag was passed", posture.Network)
	}
	found := false
	for _, w := range posture.BoundaryWeakenings {
		if w.Option == "--host-network" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a --host-network boundary weakening reported automatically from the observed running container, got %+v", posture.BoundaryWeakenings)
	}
	if posture.InspectWarning != "" {
		t.Errorf("InspectWarning = %q, want empty for a successful observed inspect", posture.InspectWarning)
	}
}

// TestAudit_ObservedMode_MountsImageDindRuntimeVolumesFromInspect covers
// AC #2: mounts (kind/ro-rw), image, dind runtime, and named volumes must
// come from actual inspect data, not the repo-scope-computed planned
// derivation — the observed image here deliberately differs from what
// ComputeScope would select, and .RW/true maps to ReadOnly:false.
func TestAudit_ObservedMode_MountsImageDindRuntimeVolumesFromInspect(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "my-repo-image:latest|bridge|sysbox-runc|on|1\n"+
		repo+"::/workspace::true\n"+
		"claude-cenci-home-repo::/home/dev::true\n"+
		"claude-cenci-dind-repo::/var/lib/docker::true\n"+
		"/run/user/1000/cenci-sockets::"+cenciSocketMountDest+"::true\n\n")

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if posture.Image.Reference != "my-repo-image:latest" {
		t.Errorf("Image.Reference = %q, want the inspected image %q, not the repo-scope-computed image", posture.Image.Reference, "my-repo-image:latest")
	}
	if !posture.Dind.Enabled {
		t.Fatalf("Dind.Enabled = false, want true (observed cenci-sand.dind label is \"on\")")
	}
	if posture.Dind.Runtime != "sysbox-runc" {
		t.Errorf("Dind.Runtime = %q, want the observed OCI runtime %q", posture.Dind.Runtime, "sysbox-runc")
	}

	kinds := map[string]bool{}
	for _, m := range posture.Mounts {
		kinds[m.Kind] = true
		if m.Kind == MountKindWorkspace {
			if m.Source != repo {
				t.Errorf("observed workspace mount Source = %q, want %q", m.Source, repo)
			}
			if m.ReadOnly {
				t.Errorf("observed workspace mount ReadOnly = true, want false (probe's RW field was \"true\")")
			}
		}
	}
	for _, want := range []string{MountKindWorkspace, MountKindHomeVolume, MountKindDindVolume, MountKindCenciSocket} {
		if !kinds[want] {
			t.Errorf("expected an observed mount with kind %q, got mounts %+v", want, posture.Mounts)
		}
	}

	volKinds := map[string]bool{}
	for _, v := range posture.Volumes {
		volKinds[v.Kind] = true
	}
	for _, want := range []string{MountKindHomeVolume, MountKindDindVolume} {
		if !volKinds[want] {
			t.Errorf("expected an observed named volume with kind %q, got volumes %+v", want, posture.Volumes)
		}
	}
}

// TestAudit_ObservedMode_HostSocketMount_BoundaryWeakening covers AC #2's
// socket-exposure clause: an observed host Docker/Podman socket mount must
// surface as a boundary weakening even though network mode stays bridge
// (unweakened) — distinct from the --host-network weakening.
func TestAudit_ObservedMode_HostSocketMount_BoundaryWeakening(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "cenci-sandbox:latest|bridge|runc||0\n"+
		"/var/run/docker.sock::/var/run/docker.sock::true\n\n")

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if posture.Network.Weakened {
		t.Errorf("Network.Weakened = true, want false (bridge mode, no host-network) — the weakening under test is the observed host socket, not network mode")
	}
	found := false
	for _, w := range posture.BoundaryWeakenings {
		if strings.Contains(strings.ToLower(w.Option), "socket") || strings.Contains(strings.ToLower(w.Effect), "socket") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a boundary weakening reported for the observed host Docker/Podman socket mount, got %+v", posture.BoundaryWeakenings)
	}
}

// TestAudit_ObservedMode_ContainerNotRunning_BasisPlannedNoWarning covers
// AC #3: `docker ps` only lists running containers, so a never-launched and
// a stopped/stale container are indistinguishable at this probe and must
// both report the same unweakened planned posture, with no inspectWarning
// (a host with no running scoped session legitimately has a planned-only
// posture — not an inspect failure).
func TestAudit_ObservedMode_ContainerNotRunning_BasisPlannedNoWarning(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	eng, _ := auditEngineWithFakeRuntime(t)
	// FAKE_PS deliberately left unset: no container of this name is running.

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if posture.Basis != PostureBasisPlanned {
		t.Errorf("Basis = %q, want %q for a not-running container", posture.Basis, PostureBasisPlanned)
	}
	if posture.InspectWarning != "" {
		t.Errorf("InspectWarning = %q, want empty — a legitimately absent/stopped container is not an inspect failure", posture.InspectWarning)
	}
}

// TestAudit_ObservedMode_MalformedInspect_BasisPlannedWithInspectWarningNoBaseline
// covers Q2/AC #9/#11: a running container whose combined inspect probe
// returns unparsable output must still render a report — basis:"planned",
// a non-empty InspectWarning, exit-equivalent nil error, and NEVER the
// reassuring "default-safe baseline" line.
func TestAudit_ObservedMode_MalformedInspect_BasisPlannedWithInspectWarningNoBaseline(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "not-the-expected-shape\n")

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v, want nil error (an inspect failure degrades to a warning, never a hard failure)", err)
	}
	if posture.Basis != PostureBasisPlanned {
		t.Errorf("Basis = %q, want %q — an inspect failure must never report basis:\"running\"", posture.Basis, PostureBasisPlanned)
	}
	if posture.InspectWarning == "" {
		t.Fatal("InspectWarning is empty, want a non-empty warning that the running container's actual posture could not be verified")
	}

	var buf bytes.Buffer
	if err := posture.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(buf.String(), "default-safe baseline") {
		t.Errorf("WriteText output claims the default-safe baseline despite an inspect failure, got:\n%s", buf.String())
	}
}

// TestAudit_ObservedMode_InspectExecFailure_BasisPlannedWithInspectWarning
// covers ticket #681: the container disappearing between `ps` and `inspect`
// (the combined observed-inspect probe's exec itself failing — a nonzero
// exit, not a parse failure) must degrade the same way as a malformed
// response: basis:"planned", a non-empty InspectWarning naming the failure,
// nil error, and never the reassuring "default-safe baseline" line.
//
// The branch discriminator below is required, not optional: exec failure
// (audit_observed.go:73-75) wraps the raw *exec.ExitError, while a parse
// failure (audit_observed.go:76-79) wraps the ErrMalformedObservedInspect
// sentinel — without asserting errors.Is(...) is false here, this test could
// not be distinguished from the already-covered
// TestAudit_ObservedMode_MalformedInspect_... parse-failure test above.
func TestAudit_ObservedMode_InspectExecFailure_BasisPlannedWithInspectWarning(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE_EXIT", "1")

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v, want nil error (an inspect exec failure degrades to a warning, never a hard failure)", err)
	}
	if posture.Basis != PostureBasisPlanned {
		t.Errorf("Basis = %q, want %q — an inspect exec failure must never report basis:\"running\"", posture.Basis, PostureBasisPlanned)
	}
	if posture.InspectWarning == "" {
		t.Fatal("InspectWarning is empty, want a non-empty warning that the running container's actual posture could not be verified")
	}
	if !strings.Contains(posture.InspectWarning, "exit status") {
		t.Errorf("InspectWarning = %q, want it to mention the underlying exit status", posture.InspectWarning)
	}

	var buf bytes.Buffer
	if err := posture.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(buf.String(), "default-safe baseline") {
		t.Errorf("WriteText output claims the default-safe baseline despite an inspect exec failure, got:\n%s", buf.String())
	}

	// Branch discriminator: a direct call must surface a non-sentinel-wrapped
	// error, proving this test drove the exec-failure branch, not the
	// already-covered parse-failure branch.
	_, directErr := eng.inspectObservedPosture(scope.ContainerName)
	if directErr == nil {
		t.Fatal("inspectObservedPosture: expected an error when FAKE_OBSERVED_POSTURE_EXIT=1, got nil")
	}
	if errors.Is(directErr, ErrMalformedObservedInspect) {
		t.Errorf("inspectObservedPosture error unexpectedly wraps ErrMalformedObservedInspect (%v) — this indicates the exec-failure path was NOT exercised, only the already-covered parse-failure path", directErr)
	}
}

// TestAudit_ObservedMode_PsUnreachable_BasisPlannedWithInspectWarningNoBaseline
// covers AC #9: a ps/daemon failure (runtime present but unreachable) must
// degrade the same way as a malformed inspect — never a hard error, never a
// silent collapse to the default-safe baseline.
func TestAudit_ObservedMode_PsUnreachable_BasisPlannedWithInspectWarningNoBaseline(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS_EXIT", "1")

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v, want nil error (a ps/daemon failure degrades to a warning, never a hard failure)", err)
	}
	if posture.Basis != PostureBasisPlanned {
		t.Errorf("Basis = %q, want %q — a ps/daemon failure must never report basis:\"running\"", posture.Basis, PostureBasisPlanned)
	}
	if posture.InspectWarning == "" {
		t.Fatal("InspectWarning is empty, want a non-empty warning that container disposition could not be determined")
	}

	var buf bytes.Buffer
	if err := posture.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(buf.String(), "default-safe baseline") {
		t.Errorf("WriteText output claims the default-safe baseline despite a ps/daemon failure, got:\n%s", buf.String())
	}
}

// TestAudit_ObservedMode_NextExecQualifier_ForwardedEnvAndReseedCreds covers
// Q4: forwardedEnv/reseedCreds have no inspect source (they are per-exec-only
// values), so observed mode must keep deriving them from the same planned
// construction logic while clearly labeling them "next-exec" in the
// text/narrative rendering — never presented as observed facts.
func TestAudit_ObservedMode_NextExecQualifier_ForwardedEnvAndReseedCreds(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	t.Setenv("CONTEXT7_API_KEY", "sentinel-secret-value-nextexec1")

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")

	posture, err := eng.Audit(Options{Agent: "claude", ReseedCreds: true})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if posture.Basis != PostureBasisRunning {
		t.Fatalf("Basis = %q, want %q (precondition for this test)", posture.Basis, PostureBasisRunning)
	}
	if len(posture.ForwardedEnv) == 0 {
		t.Fatalf("ForwardedEnv is empty, want CONTEXT7_API_KEY (planned-derivation values still populate observed mode)")
	}
	if !posture.ReseedCreds {
		t.Fatalf("ReseedCreds = false, want true (planned-derivation value still populates observed mode)")
	}

	var buf bytes.Buffer
	if err := posture.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "next-exec") {
		t.Errorf("expected a \"next-exec\" qualifier on the forwarded-env/reseed section for a running-basis report, got:\n%s", out)
	}
	if !strings.Contains(out, "not observed") {
		t.Errorf("expected the next-exec qualifier to explicitly say these values are \"not observed\" from the running container, got:\n%s", out)
	}
}

// TestAudit_ObservedMode_NoMutation_CallLogOnlyPsAndInspect is a dedicated
// regression test (mirroring dryrun_test.go's
// TestDryRun_NoSideEffects_NoMutatingRuntimeCallsAndExecAttachNeverCalled):
// Audit's observed-mode dispatch must never issue a mutating runtime verb —
// only the read-only ps/inspect probes.
func TestAudit_ObservedMode_NoMutation_CallLogOnlyPsAndInspect(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)
	eng, callLog := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")

	if _, err := eng.Audit(Options{Agent: "claude"}); err != nil {
		t.Fatalf("Audit: %v", err)
	}

	lines := readCallLog(t, callLog)
	if len(lines) == 0 {
		t.Fatal("expected at least a ps call in the call log, got none")
	}
	for _, mutatingPrefix := range []string{"run ", "create ", "rm ", "start ", "stop "} {
		if containsPrefix(lines, mutatingPrefix) {
			t.Errorf("Audit issued a mutating runtime call with prefix %q; want read-only ps/inspect only. calls:\n%s", mutatingPrefix, strings.Join(lines, "\n"))
		}
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if verb := fields[0]; verb != "ps" && verb != "inspect" {
			t.Errorf("Audit issued an unexpected runtime call %q; want only ps/inspect probes, calls:\n%s", line, strings.Join(lines, "\n"))
		}
	}
}

// -- ticket #627: JSON basis/inspectWarning ----------------------------------

// TestAudit_JSON_BasisFieldPresent_PlannedAndRunning covers AC #4/#8's JSON
// side: basis is present with the correct value in both modes.
func TestAudit_JSON_BasisFieldPresent_PlannedAndRunning(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	planned, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (planned): %v", err)
	}
	if planned.Basis != PostureBasisPlanned {
		t.Errorf("Basis = %q, want %q for a runtime-less engine", planned.Basis, PostureBasisPlanned)
	}
	plannedJSON, err := json.Marshal(planned)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(plannedJSON), `"basis":"planned"`) {
		t.Errorf("planned JSON output missing \"basis\":\"planned\", got:\n%s", plannedJSON)
	}
	if strings.Contains(string(plannedJSON), `"inspectWarning"`) {
		t.Errorf("planned JSON output must omit inspectWarning when empty (omitempty), got:\n%s", plannedJSON)
	}

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")

	running, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (running): %v", err)
	}
	if running.Basis != PostureBasisRunning {
		t.Errorf("Basis = %q, want %q for a running scoped container", running.Basis, PostureBasisRunning)
	}
	runningJSON, err := json.Marshal(running)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(runningJSON), `"basis":"running"`) {
		t.Errorf("running JSON output missing \"basis\":\"running\", got:\n%s", runningJSON)
	}
}

// TestAudit_JSON_InspectWarningPresentOnFailure covers the inverse of
// #588's omitempty regression: when InspectWarning IS set, it must actually
// appear in the JSON (not just be non-omitted-when-empty).
func TestAudit_JSON_InspectWarningPresentOnFailure(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "not-the-expected-shape\n")

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	data, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, `"inspectWarning":"`) {
		t.Errorf("JSON output missing a non-empty \"inspectWarning\" field on inspect failure, got:\n%s", out)
	}
	if !strings.Contains(out, `"basis":"planned"`) {
		t.Errorf("JSON output missing \"basis\":\"planned\" on inspect failure, got:\n%s", out)
	}
}

// TestAudit_ObservedMode_DindLabelUnknown_NotRenderedAsDisabledOrBaseline
// covers #598/#627: a running container whose cenci-sand.dind label is an
// unrecognized value must report Dind.Source == DindSourceUnknown (never a
// confident dindOff/DindSourceObserved "disabled" attribution), and
// WriteText must not claim the "default-safe baseline" — an indeterminate
// dind state must not co-occur with that reassuring line, even though this
// case has no InspectWarning (the inspect call itself succeeded; only the
// label's meaning is ambiguous).
func TestAudit_ObservedMode_DindLabelUnknown_NotRenderedAsDisabledOrBaseline(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	scope := ComputeScope("claude", "", repo, home)
	eng, _ := auditEngineWithFakeRuntime(t)
	t.Setenv("FAKE_PS", scope.ContainerName+"\n")
	t.Setenv("FAKE_OBSERVED_POSTURE", "cenci-sandbox:latest|bridge|runc|some-unrecognized-value|0\n"+
		"/host/repo::/workspace::true\n\n")

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if posture.Basis != PostureBasisRunning {
		t.Fatalf("Basis = %q, want %q for a running scoped container", posture.Basis, PostureBasisRunning)
	}
	if posture.InspectWarning != "" {
		t.Errorf("InspectWarning = %q, want empty — the inspect call itself succeeded, only the dind label's meaning is ambiguous", posture.InspectWarning)
	}
	if posture.Dind.Source != DindSourceUnknown {
		t.Fatalf("Dind.Source = %q, want %q for an unrecognized cenci-sand.dind label", posture.Dind.Source, DindSourceUnknown)
	}
	if posture.Dind.Enabled {
		t.Errorf("Dind.Enabled = true, want false (unknown is reported via Source, not Enabled)")
	}

	var buf bytes.Buffer
	if err := posture.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "default-safe baseline") {
		t.Errorf("WriteText output claims the default-safe baseline despite an indeterminate dind state, got:\n%s", out)
	}
	if !strings.Contains(out, "source: "+DindSourceUnknown) {
		t.Errorf("WriteText output missing the unknown dind source, got:\n%s", out)
	}

	dataJSON, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(dataJSON), `"source":"`+DindSourceUnknown+`"`) {
		t.Errorf("JSON output missing dind source %q, got:\n%s", DindSourceUnknown, dataJSON)
	}
	if strings.Contains(string(dataJSON), `"inspectWarning"`) {
		t.Errorf("JSON output must omit inspectWarning when empty (omitempty) even though dind is indeterminate, got:\n%s", dataJSON)
	}
}

// TestAudit_DarwinDindConfig_ReportsPlatformUnsupported pins #962's audit
// attribution: on macOS a dind repo config resolves off, and the report must
// say WHY — a plain DindSourceOff would be indistinguishable from "nobody
// asked for dind", leaving a macOS user auditing a dind repo unable to tell
// the two apart. Source carries the platform-unsupported value and Note
// explains the Linux-only sysbox constraint.
func TestAudit_DarwinDindConfig_ReportsPlatformUnsupported(t *testing.T) {
	setDindHostOS(t, "darwin")
	repo := newAuditTestRepo(t)
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit on darwin with a dind repo config = %v, want a successful report", err)
	}

	if posture.Dind.Enabled {
		t.Error("Dind.Enabled = true on darwin, want false")
	}
	if posture.Dind.Source != DindSourcePlatformUnsupported {
		t.Errorf("Dind.Source = %q, want %q", posture.Dind.Source, DindSourcePlatformUnsupported)
	}
	if !strings.Contains(posture.Dind.Note, "macOS") {
		t.Errorf("Dind.Note = %q, want it to name the platform", posture.Dind.Note)
	}
	if posture.Dind.Runtime != "" || posture.Dind.StorageVolume != "" {
		t.Errorf("Dind runtime/storageVolume = %q/%q, want both empty — no sysbox runtime and no dind volume are applied on a degraded launch", posture.Dind.Runtime, posture.Dind.StorageVolume)
	}

	// The degrade must reach the human report too, not just the JSON.
	var buf bytes.Buffer
	if err := posture.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.Contains(buf.String(), DindSourcePlatformUnsupported) {
		t.Errorf("WriteText output does not mention %q:\n%s", DindSourcePlatformUnsupported, buf.String())
	}
}

// TestAudit_LinuxDindConfig_StaysEnabled is the counterpart regression: the
// #962 platform gate must never degrade a Linux host's dind posture.
func TestAudit_LinuxDindConfig_StaysEnabled(t *testing.T) {
	setDindHostOS(t, "linux")
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
		t.Fatal("Dind.Enabled = false on linux with a dind repo config, want true")
	}
	if posture.Dind.Source != DindSourceConfig {
		t.Errorf("Dind.Source = %q, want %q", posture.Dind.Source, DindSourceConfig)
	}
}
