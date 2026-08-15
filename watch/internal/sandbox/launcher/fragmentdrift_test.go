package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// -- Fixtures ----------------------------------------------------------------
//
// Fragment content mirrors the real sandbox/fragments/*.dockerfile shape
// (banner line + body) closely enough to exercise the detector's real
// containment/banner/marker logic without depending on the actual fragment
// files on disk, per watch/docs/test-strategy.md's fixture discipline.

const dockerBannerLine = "# ── Docker CLI + inner engine (dind, #586) ────────────────────────"

// dockerFragmentOld models the pre-v1.19.1 fragment (#1037's fix not yet
// applied) — same banner, different body — the exact velka acceptance-test
// shape: a repo's committed managed block still carries this content while
// the installed plugin now ships dockerFragmentNew.
const dockerFragmentOld = dockerBannerLine + "\n" +
	"RUN apt-get update && apt-get install -y docker-ce docker-ce-cli containerd.io\n" +
	"RUN usermod -aG docker dev\n"

// dockerFragmentNew models the installed plugin's current fragment, post
// #1037/#1038: an extra fixed line the old fragment never had.
const dockerFragmentNew = dockerBannerLine + "\n" +
	"RUN apt-get update && apt-get install -y docker-ce docker-ce-cli containerd.io\n" +
	"RUN usermod -aG docker dev\n" +
	"RUN echo added-in-v1.19.1-fix\n"

const rustBannerLine = "# ── Rust ─────────────────────────────────────────────────────────"

const rustFragmentOriginal = rustBannerLine + "\n" +
	"RUN apt-get install -y rustc cargo\n"

const rustFragmentMutated = rustBannerLine + "\n" +
	"RUN apt-get install -y rustc cargo clippy\n"

const dotnetBannerLine = "# ── .NET SDK ─────────────────────────────────────────────────────"

// dotnetFragment builds a dotnet fragment body with the given
// ARG DOTNET_SDK_VERSION default — mirroring SKILL.md's "the only row with a
// version-from-token adjustment" substitution.
func dotnetFragment(version string) string {
	return dotnetBannerLine + "\n" +
		"ARG DOTNET_SDK_VERSION=" + version + "\n" +
		"RUN apt-get install -y dotnet-sdk-${DOTNET_SDK_VERSION}\n"
}

// dotnetFragmentWithAutoDetectComment builds a dotnet fragment carrying the
// "could not be auto-detected" comment line SKILL.md:499 inserts immediately
// after the ARG line when no major version could be extracted from the stack
// token.
func dotnetFragmentWithAutoDetectComment(version string) string {
	return dotnetBannerLine + "\n" +
		"ARG DOTNET_SDK_VERSION=" + version + "\n" +
		"# .NET version could not be auto-detected from the stack token — using fragment default. See sandbox/README.md to pin manually.\n" +
		"RUN apt-get install -y dotnet-sdk-${DOTNET_SDK_VERSION}\n"
}

// managedBlock wraps inner in the # cenci:managed-begin / -end markers
// SKILL.md's step 5e generates, bounding the region detectFragmentDrift
// compares.
func managedBlock(inner string) string {
	return "# cenci:managed-begin\n" + inner + "# cenci:managed-end\n"
}

type fragmentFixture struct {
	name    string // e.g. "docker.dockerfile"
	content string
}

// writeFragmentAssetFixture creates a temp asset dir containing
// fragments/<name> for each fixture — only the fragments/ subdirectory
// matters for detectFragmentDrift; Dockerfile.base/entrypoint.sh/lib/ are
// irrelevant here and deliberately omitted.
func writeFragmentAssetFixture(t *testing.T, fragments ...fragmentFixture) string {
	t.Helper()
	dir := t.TempDir()
	fragDir := filepath.Join(dir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatalf("mkdir fragments: %v", err)
	}
	for _, f := range fragments {
		if err := os.WriteFile(filepath.Join(fragDir, f.name), []byte(f.content), 0o644); err != nil {
			t.Fatalf("write fragment %s: %v", f.name, err)
		}
	}
	return dir
}

// writeRepoDockerfile creates a temp repo root with .cenci/Dockerfile
// containing content.
func writeRepoDockerfile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	cenciDir := filepath.Join(dir, ".cenci")
	if err := os.MkdirAll(cenciDir, 0o755); err != nil {
		t.Fatalf("mkdir .cenci: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cenciDir, "Dockerfile"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .cenci/Dockerfile: %v", err)
	}
	return dir
}

// containsSubstring reports whether any element of list contains sub.
// detectFragmentDrift's exact string format for a drifted fragment (bare
// fragment name vs full "<name>.dockerfile") is not pinned by the plan
// (Files to Create only promises "naming only installed fragment file
// names"), so these tests assert on substring presence/absence and list
// length rather than an exact string, keeping the contract (a fragment was
// or wasn't reported as drifted) pinned without over-specifying formatting
// left to the implementation.
func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// -- detectFragmentDrift: unit-level parsing/matching matrix -----------------

// TestDetectFragmentDrift_NoDrift_ByteIdenticalBlock pins AC #2: a repo
// managed block that byte-matches the installed fragment reports no drift.
func TestDetectFragmentDrift_NoDrift_ByteIdenticalBlock(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew))

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected no drift for a byte-identical managed block, got %v", drifted)
	}
}

// TestDetectFragmentDrift_SelectedAndDrifted_ReportsFragment pins the core
// selected-but-stale detection: a repo block carrying the fragment's banner
// but stale content is reported.
func TestDetectFragmentDrift_SelectedAndDrifted_ReportsFragment(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentOld))

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift: %v", err)
	}
	if len(drifted) != 1 || !containsSubstring(drifted, "docker") {
		t.Errorf("expected exactly one drifted fragment naming docker, got %v", drifted)
	}
}

// TestDetectFragmentDrift_UnselectedFragmentMutated_NoDrift pins AC #4: a
// config-selected fragment (rust) that the repo's block never selects is
// never reported, even though its installed content differs from some other
// baseline — the repo block simply never mentions it.
func TestDetectFragmentDrift_UnselectedFragmentMutated_NoDrift(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t,
		fragmentFixture{"docker.dockerfile", dockerFragmentNew},
		fragmentFixture{"rust.dockerfile", rustFragmentMutated},
	)
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew)) // never mentions rust at all

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected no drift for an unselected fragment, got %v", drifted)
	}
}

// TestDetectFragmentDrift_DotnetVersionSubstitution_NoDrift pins decision
// constraint 5, the one false positive that would sink the feature: a repo
// block whose only difference from the installed fragment is the
// ARG DOTNET_SDK_VERSION default (SKILL.md's per-repo substitution) must not
// be reported as drift.
func TestDetectFragmentDrift_DotnetVersionSubstitution_NoDrift(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"dotnet.dockerfile", dotnetFragment("10.0.100")})
	repoRoot := writeRepoDockerfile(t, managedBlock(dotnetFragment("8.0.100")))

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected no drift for a dotnet ARG DOTNET_SDK_VERSION-only difference, got %v", drifted)
	}
}

// TestDetectFragmentDrift_DotnetVersionSubstitutionWithAutoDetectComment_NoDrift
// pins the auto-detect-failure comment-line variant (SKILL.md:499): the
// inline comment inserted immediately after the ARG line when no major
// version could be extracted from the stack token must also not trip drift.
func TestDetectFragmentDrift_DotnetVersionSubstitutionWithAutoDetectComment_NoDrift(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"dotnet.dockerfile", dotnetFragment("10.0.100")})
	repoRoot := writeRepoDockerfile(t, managedBlock(dotnetFragmentWithAutoDetectComment("10.0.100")))

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected no drift when the dotnet fragment carries the auto-detect-failure comment line, got %v", drifted)
	}
}

// TestDetectFragmentDrift_DotnetOtherArgDiffers_ReportsDrift is the narrow-
// exclusion negative (watch/AGENTS.md): normalization is anchored to the
// literal ARG DOTNET_SDK_VERSION= line only. Any *other* line differing must
// still drift, proving the dotnet exception never widens into "ignore any
// ARG line".
func TestDetectFragmentDrift_DotnetOtherArgDiffers_ReportsDrift(t *testing.T) {
	installed := dotnetBannerLine + "\n" +
		"ARG DOTNET_SDK_VERSION=10.0.100\n" +
		"RUN apt-get install -y dotnet-sdk-${DOTNET_SDK_VERSION}\n"
	repoBlock := dotnetBannerLine + "\n" +
		"ARG DOTNET_SDK_VERSION=10.0.100\n" +
		"RUN apt-get install -y dotnet-sdk-${DOTNET_SDK_VERSION} some-extra-package\n"

	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"dotnet.dockerfile", installed})
	repoRoot := writeRepoDockerfile(t, managedBlock(repoBlock))

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift: %v", err)
	}
	if len(drifted) != 1 || !containsSubstring(drifted, "dotnet") {
		t.Errorf("expected a drift report when a non-ARG-DOTNET_SDK_VERSION line differs, got %v", drifted)
	}
}

// TestDetectFragmentDrift_CRLFLineEndings_NoDrift pins the CRLF false-positive
// fix (#1048 review): a repo .cenci/Dockerfile whose managed block is
// byte-identical to the installed fragment except that every line ends in
// CRLF rather than LF (e.g. a Windows checkout's core.autocrlf) must not be
// reported as drifted — extraction and comparison must normalize line
// endings before the containment check, not just when matching marker lines.
func TestDetectFragmentDrift_CRLFLineEndings_NoDrift(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	crlfBlock := strings.ReplaceAll(managedBlock(dockerFragmentNew), "\n", "\r\n")
	repoRoot := writeRepoDockerfile(t, crlfBlock)

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected no drift for a CRLF-identical managed block, got %v", drifted)
	}
}

// TestDetectFragmentDrift_MarkerMode_IdentifiesDriftEvenWithoutBanner pins
// the marker-preferred identification path (decision constraint 9): a repo
// block wrapped in # cenci:fragment-begin/-end markers is identified as
// selected via the marker alone. The repo block below deliberately carries
// no banner line at all, so a banner-only implementation would report no
// drift here — proving the marker path is actually exercised, not just
// coincidentally satisfied by the banner fallback.
func TestDetectFragmentDrift_MarkerMode_IdentifiesDriftEvenWithoutBanner(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	markedBlock := "# cenci:fragment-begin docker\n" +
		"RUN apt-get install -y docker-ce-old\n" +
		"# cenci:fragment-end docker\n"
	repoRoot := writeRepoDockerfile(t, managedBlock(markedBlock))

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift: %v", err)
	}
	if len(drifted) != 1 || !containsSubstring(drifted, "docker") {
		t.Errorf("expected the marker to identify docker as selected and drifted, got %v", drifted)
	}
}

// TestDetectFragmentDrift_BannerFallback_PresentAndAbsent pins the legacy
// fallback (decision constraint 4/9): a marker-less block still identifies a
// selected-but-stale fragment via its banner line, and a fragment whose
// banner never appears anywhere in the block is never reported.
func TestDetectFragmentDrift_BannerFallback_PresentAndAbsent(t *testing.T) {
	t.Run("banner_present_reports_drift", func(t *testing.T) {
		assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
		repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentOld)) // banner present, content stale, no marker

		drifted, err := detectFragmentDrift(assetDir, repoRoot)
		if err != nil {
			t.Fatalf("detectFragmentDrift: %v", err)
		}
		if len(drifted) != 1 || !containsSubstring(drifted, "docker") {
			t.Errorf("expected the banner fallback to identify docker as drifted, got %v", drifted)
		}
	})

	t.Run("banner_absent_not_selected", func(t *testing.T) {
		assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
		repoRoot := writeRepoDockerfile(t, managedBlock(rustFragmentOriginal)) // docker's banner never appears

		drifted, err := detectFragmentDrift(assetDir, repoRoot)
		if err != nil {
			t.Fatalf("detectFragmentDrift: %v", err)
		}
		if len(drifted) != 0 {
			t.Errorf("expected no drift when the fragment's banner is absent from the block, got %v", drifted)
		}
	})
}

// TestDetectFragmentDrift_MalformedManagedMarkers_SilentNoDrift pins decision
// constraint 1: absent or malformed managed-block markers are a silent skip
// (unmanaged/legacy file, not drift) — never an error, never a reported
// fragment, regardless of how different the repo's content is from the
// installed fragment.
func TestDetectFragmentDrift_MalformedManagedMarkers_SilentNoDrift(t *testing.T) {
	cases := map[string]string{
		"no_markers_at_all": dockerFragmentOld,
		"only_begin_marker": "# cenci:managed-begin\n" + dockerFragmentOld,
		"only_end_marker":   dockerFragmentOld + "# cenci:managed-end\n",
		"duplicate_begin_markers": "# cenci:managed-begin\n" + dockerFragmentOld +
			"# cenci:managed-begin\n" + dockerFragmentOld + "# cenci:managed-end\n",
		"out_of_order_end_before_begin": "# cenci:managed-end\n" + dockerFragmentOld + "# cenci:managed-begin\n",
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
			repoRoot := writeRepoDockerfile(t, content)

			drifted, err := detectFragmentDrift(assetDir, repoRoot)
			if err != nil {
				t.Fatalf("detectFragmentDrift with malformed markers (%s): want no error, got %v", name, err)
			}
			if len(drifted) != 0 {
				t.Errorf("detectFragmentDrift with malformed markers (%s): want no drift reported, got %v", name, drifted)
			}
		})
	}
}

// TestDetectFragmentDrift_FragmentsDirMissing_ReturnsError pins the
// asset-side probe-failure edge (distinct from the repo-side silent-skip
// cases above): a missing <assetDir>/fragments directory is an
// infrastructure failure the caller (Engine.warnFragmentDrift) must convert
// into a single stderr warning, not a silent "no drift".
func TestDetectFragmentDrift_FragmentsDirMissing_ReturnsError(t *testing.T) {
	assetDir := t.TempDir() // no fragments/ subdirectory created
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew))

	if _, err := detectFragmentDrift(assetDir, repoRoot); err == nil {
		t.Fatal("detectFragmentDrift with a missing fragments/ dir: want error, got nil")
	}
}

// TestDetectFragmentDrift_DockerfileUnreadablePermissions_ReturnsError pins
// the read-error/silent-failure fix (#1048 review, finding 4): a repo
// .cenci/Dockerfile that exists but can't be read (permission denied) is
// distinct from "missing" — it must propagate an error, symmetric with
// loadFragments' existing error propagation, rather than silently collapsing
// into ([]string(nil), nil) like the documented missing-file case.
func TestDetectFragmentDrift_DockerfileUnreadablePermissions_ReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits have no effect when running as root")
	}
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew))
	dockerfilePath := filepath.Join(repoRoot, ".cenci", "Dockerfile")
	if err := os.Chmod(dockerfilePath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dockerfilePath, 0o644) })

	if _, err := detectFragmentDrift(assetDir, repoRoot); err == nil {
		t.Fatal("detectFragmentDrift with an unreadable .cenci/Dockerfile: want error, got nil")
	}
}

// TestDetectFragmentDrift_DockerfileNonRegular_SilentNoDrift pins the
// bounded-read guard (#1048 review, finding 4): a .cenci/Dockerfile path that
// isn't a regular file (here, a directory) must be silently skipped exactly
// like a missing file — never an error, and never read into memory.
func TestDetectFragmentDrift_DockerfileNonRegular_SilentNoDrift(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := t.TempDir()
	dockerfilePath := filepath.Join(repoRoot, ".cenci", "Dockerfile")
	if err := os.MkdirAll(dockerfilePath, 0o755); err != nil {
		t.Fatalf("mkdir non-regular Dockerfile path: %v", err)
	}

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift with a non-regular .cenci/Dockerfile: want no error, got %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("detectFragmentDrift with a non-regular .cenci/Dockerfile: want no drift reported, got %v", drifted)
	}
}

// TestDetectFragmentDrift_DockerfileOversized_SilentNoDrift pins the
// bounded-read guard's size cap (#1048 review, finding 4): a
// .cenci/Dockerfile exceeding maxManagedDockerfileSize is silently skipped
// like a missing file, even though its (unbounded) content would otherwise
// report a genuine drift.
func TestDetectFragmentDrift_DockerfileOversized_SilentNoDrift(t *testing.T) {
	assetDir := writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	paddingLine := "# padding line to exceed the size cap\n"
	padding := strings.Repeat(paddingLine, (maxManagedDockerfileSize/len(paddingLine))+1000)
	repoRoot := writeRepoDockerfile(t, padding+managedBlock(dockerFragmentOld)) // would drift if read

	drifted, err := detectFragmentDrift(assetDir, repoRoot)
	if err != nil {
		t.Fatalf("detectFragmentDrift with an oversized .cenci/Dockerfile: want no error, got %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("detectFragmentDrift with an oversized .cenci/Dockerfile: want no drift reported, got %v", drifted)
	}
}

// -- (*Engine).warnFragmentDrift / EnsureImage / BuildRepoImage integration --

// TestBuildRepoImage_FragmentDrift_PrintsWarningNamingConfigureRemedy pins
// AC #1's build path — the velka acceptance test: a marker-less repo block
// carrying a pre-v1.19.1 docker fragment causes BuildRepoImage to print a
// stderr warning naming docker and the literal /cenci:configure remedy.
func TestBuildRepoImage_FragmentDrift_PrintsWarningNamingConfigureRemedy(t *testing.T) {
	e, _ := buildEngine(t, false)
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentOld))

	if err := e.BuildRepoImage(repoRoot, "cenci-sandbox-velka:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	warning := errOut.String()
	if !strings.Contains(warning, "docker") {
		t.Errorf("expected the warning to name the drifted docker fragment, got:\n%s", warning)
	}
	if !strings.Contains(warning, "/cenci:configure") {
		t.Errorf("expected the warning to name /cenci:configure as the remedy, got:\n%s", warning)
	}
	if !strings.Contains(warning, "Warning:") {
		t.Errorf("expected the fragment-drift warning to follow the Warning: convention (e.Stderr), got:\n%s", warning)
	}
}

// TestEnsureImage_FragmentDrift_OnCurrentImage_PrintsWarningWithoutRebuild
// pins AC #1's open path: the same drift fixture through EnsureImage with an
// already-current image (no rebuild triggered) still prints the warning —
// `cenci open` warns without ever rebuilding.
func TestEnsureImage_FragmentDrift_OnCurrentImage_PrintsWarningWithoutRebuild(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present and current by default
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentOld))

	scope := Scope{Image: MonolithImage, UsingRepoImage: true, RepoRoot: repoRoot}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	if calls := readCallLog(t, callLog); containsPrefix(calls, "build") {
		t.Errorf("expected no rebuild for an already-current image, calls:\n%s", strings.Join(calls, "\n"))
	}
	warning := errOut.String()
	if !strings.Contains(warning, "docker") {
		t.Errorf("expected the warning to name the drifted docker fragment on the no-rebuild path, got:\n%s", warning)
	}
	if !strings.Contains(warning, "/cenci:configure") {
		t.Errorf("expected the warning to name /cenci:configure as the remedy, got:\n%s", warning)
	}
}

// TestEnsureImage_NoFragmentDrift_NoWarning pins AC #2 at the integration
// seam: a repo block byte-matching every installed fragment produces no
// warning through EnsureImage.
func TestEnsureImage_NoFragmentDrift_NoWarning(t *testing.T) {
	e, _ := buildEngine(t, false)
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew))

	scope := Scope{Image: MonolithImage, UsingRepoImage: true, RepoRoot: repoRoot}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	if errOut.String() != "" {
		t.Errorf("expected no fragment-drift warning for a byte-identical managed block, got stderr:\n%s", errOut.String())
	}
}

// TestEnsureImage_FragmentDrift_ArgvIdenticalToNoDriftCase pins AC #3: the
// fragment-drift check never perturbs the runtime call sequence, whether or
// not drift is present — it is a host-side file read, not a runtime probe.
func TestEnsureImage_FragmentDrift_ArgvIdenticalToNoDriftCase(t *testing.T) {
	drift, driftLog := buildEngine(t, false)
	drift.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	driftRepoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentOld))
	driftScope := Scope{Image: MonolithImage, UsingRepoImage: true, RepoRoot: driftRepoRoot}
	if err := drift.EnsureImage(driftScope); err != nil {
		t.Fatalf("EnsureImage (drift fixture): %v", err)
	}

	noDrift, noDriftLog := buildEngine(t, false)
	noDrift.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	noDriftRepoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew))
	noDriftScope := Scope{Image: MonolithImage, UsingRepoImage: true, RepoRoot: noDriftRepoRoot}
	if err := noDrift.EnsureImage(noDriftScope); err != nil {
		t.Fatalf("EnsureImage (no-drift fixture): %v", err)
	}

	driftCalls := readCallLog(t, driftLog)
	noDriftCalls := readCallLog(t, noDriftLog)
	if !reflect.DeepEqual(driftCalls, noDriftCalls) {
		t.Errorf("expected identical runtime argv sequences regardless of fragment drift;\ndrift calls:\n%s\nno-drift calls:\n%s",
			strings.Join(driftCalls, "\n"), strings.Join(noDriftCalls, "\n"))
	}
}

// TestBuildRepoImage_FragmentDrift_UnselectedFragmentMutated_NoWarning is the
// integration-level companion to the AC #4 unit test: mutating a
// config-selected fragment (rust) the repo block never selects must not trip
// a warning through the real BuildRepoImage call site either.
func TestBuildRepoImage_FragmentDrift_UnselectedFragmentMutated_NoWarning(t *testing.T) {
	e, _ := buildEngine(t, false)
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = writeFragmentAssetFixture(t,
		fragmentFixture{"docker.dockerfile", dockerFragmentNew},
		fragmentFixture{"rust.dockerfile", rustFragmentMutated},
	)
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew)) // matches docker exactly, never mentions rust

	if err := e.BuildRepoImage(repoRoot, "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	if errOut.String() != "" {
		t.Errorf("expected no warning when only an unselected fragment changed, got stderr:\n%s", errOut.String())
	}
}

// TestBuildRepoImage_FragmentDrift_CalledTwice_WarnsBothTimesFileUnchanged
// pins AC #5: a rebuild alone does not clear the drift state.
// BuildRepoImage never writes .cenci/Dockerfile, so invoking it twice against
// the same on-disk fixture must warn on both invocations, and the file's
// bytes on disk must be unchanged afterwards.
func TestBuildRepoImage_FragmentDrift_CalledTwice_WarnsBothTimesFileUnchanged(t *testing.T) {
	e, _ := buildEngine(t, false)
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentOld))
	dockerfilePath := filepath.Join(repoRoot, ".cenci", "Dockerfile")

	before, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("read fixture Dockerfile: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := e.BuildRepoImage(repoRoot, "cenci-sandbox-velka:latest"); err != nil {
			t.Fatalf("BuildRepoImage (invocation %d): %v", i+1, err)
		}
	}

	warning := errOut.String()
	if n := strings.Count(warning, "docker"); n < 2 {
		t.Errorf("expected the docker drift warning to appear on both invocations, got %d occurrence(s); stderr:\n%s", n, warning)
	}

	after, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("re-read fixture Dockerfile: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("expected <repoRoot>/.cenci/Dockerfile to be byte-unchanged across rebuilds (BuildRepoImage must never write it); before:\n%s\nafter:\n%s", before, after)
	}
}

// TestCheckSelected_FragmentDrift_StillReportsCurrentTrue pins decision
// constraint 6: fragment drift must never flip imageCurrent's/CheckSelected's
// "current" boolean — install.sh's `cenci sandbox build --check` exit-code
// contract stays purely about image freshness, never fragment content.
func TestCheckSelected_FragmentDrift_StillReportsCurrentTrue(t *testing.T) {
	e, _ := buildEngine(t, false) // image present, agent-cli and base version current
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentOld)) // drifted

	scope := Scope{Image: MonolithImage, UsingRepoImage: true, RepoRoot: repoRoot}
	current, err := e.CheckSelected(scope)
	if err != nil {
		t.Fatalf("CheckSelected: %v", err)
	}
	if !current {
		t.Errorf("CheckSelected on a current-but-fragment-drifted image: current = false, want true")
	}
}

// TestEngineWarnFragmentDrift_MonolithScope_NeverRuns pins decision
// constraint 7: the check is scoped to Scope.UsingRepoImage. RepoRoot here
// deliberately points at a fixture that WOULD warn if the detector ran
// (proving the scope gate, not a coincidence of an empty/matching fixture).
func TestEngineWarnFragmentDrift_MonolithScope_NeverRuns(t *testing.T) {
	e, _ := buildEngine(t, false)
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentOld)) // would drift if the detector ran

	scope := Scope{Image: MonolithImage, UsingRepoImage: false, RepoRoot: repoRoot}
	e.warnFragmentDrift(scope)

	if errOut.String() != "" {
		t.Errorf("expected no fragment-drift check for a monolith scope (UsingRepoImage=false), got stderr:\n%s", errOut.String())
	}
}

// TestEngineWarnFragmentDrift_FragmentsDirMissing_OneWarningBuildUnaffected
// pins the asset-side probe-failure edge at the Engine seam: a missing
// <assetDir>/fragments directory produces exactly one stderr warning naming
// the failing probe, and the build itself still succeeds (non-fatal, same
// discipline as printStaleContainerNotice's probe failures).
func TestEngineWarnFragmentDrift_FragmentsDirMissing_OneWarningBuildUnaffected(t *testing.T) {
	e, _ := buildEngine(t, false)
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = t.TempDir() // no fragments/ subdirectory
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew))

	if err := e.BuildRepoImage(repoRoot, "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	warning := errOut.String()
	if n := strings.Count(warning, "Warning:"); n != 1 {
		t.Errorf("expected exactly one stderr warning for a missing fragments/ dir, got %d; stderr:\n%s", n, warning)
	}
	if !strings.Contains(warning, "fragments") {
		t.Errorf("expected the warning to name the failing probe (fragments dir), got:\n%s", warning)
	}
}

// TestEngineWarnFragmentDrift_DockerfileUnreadablePermissions_OneWarningBuildUnaffected
// is the repo-side symmetric companion to the fragments-dir-missing test
// above (#1048 review, finding 4): an existing but unreadable
// .cenci/Dockerfile (permission denied) must also produce exactly one
// stderr probe-failure warning at the Engine seam, not a silent skip, and
// the build itself still succeeds.
func TestEngineWarnFragmentDrift_DockerfileUnreadablePermissions_OneWarningBuildUnaffected(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits have no effect when running as root")
	}
	e, _ := buildEngine(t, false)
	var errOut bytes.Buffer
	e.Stderr = &errOut
	e.AssetDir = writeFragmentAssetFixture(t, fragmentFixture{"docker.dockerfile", dockerFragmentNew})
	repoRoot := writeRepoDockerfile(t, managedBlock(dockerFragmentNew))
	dockerfilePath := filepath.Join(repoRoot, ".cenci", "Dockerfile")
	if err := os.Chmod(dockerfilePath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dockerfilePath, 0o644) })

	if err := e.BuildRepoImage(repoRoot, "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	warning := errOut.String()
	if n := strings.Count(warning, "Warning:"); n != 1 {
		t.Errorf("expected exactly one stderr warning for an unreadable .cenci/Dockerfile, got %d; stderr:\n%s", n, warning)
	}
	if !strings.Contains(warning, "Dockerfile") {
		t.Errorf("expected the warning to name the failing probe (.cenci/Dockerfile), got:\n%s", warning)
	}
}
