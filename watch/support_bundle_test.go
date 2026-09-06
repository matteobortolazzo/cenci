package main_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

// -- support-bundle ------------------------------------------------------
//
// `cenci support-bundle [--output|-o PATH] [--yes|-y]` (ticket #573) collects
// a sanitized diagnostic archive: host/runtime versions, environment
// variable NAMES only (never values), daemon reachability, config.json (or a
// "(not found ...)" placeholder), a per-container read-only diagnose report
// (reusing internal/sandbox/launcher's Diagnose, buffer-captured via
// ScopeForContainer), and a tailed boot log per container. It prints the
// manifest to stdout, then confirms (default-deny; --yes skips) before
// writing a top-level "cenci-support-bundle-<UTCstamp>/" dir into a .tar.gz.
// These black-box tests drive the real built `cenci` binary as a subprocess
// via writeScriptedRuntimes/writeAssetFixture (shared with
// sandbox_open_test.go/diagnose_test.go, same package).

// fakePS is a scripted `ps -a` listing with one running and one exited
// sandbox container across both agents, used by every support-bundle test
// that wants at least one container.
const fakePS = "claude-cenci-myrepo\tUp 2 hours\tcenci-sandbox:latest\n" +
	"codex-cenci-other\tExited (0) 3 minutes ago\tcenci-sandbox:latest\n"

// sbEnv builds the black-box environment for native `cenci support-bundle`
// runs. XDG_CONFIG_HOME is pinned explicitly (not just inherited) so the
// default config.json resolution is deterministic regardless of the host's
// own XDG_CONFIG_HOME.
func sbEnv(t *testing.T, fakeDir, assets string) (env []string, home string) {
	t.Helper()
	home = t.TempDir()
	xdg := t.TempDir()
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}
	env = append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"CENCI_SANDBOX_ASSETS="+assets,
		"CENCI_SOCKET_DIR="+xdg,
		"FAKE_VOLUMES=cenci-agent-cli-claude\ncenci-agent-cli-codex\n",
		"FAKE_IMAGE_BASE_VERSION="+tag,
	)
	return env, home
}

// writeConfigJSON seeds $HOME/.config/cenci/config.json with content.
func writeConfigJSON(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "cenci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// bundleTopDirPattern matches the top-level "cenci-support-bundle-<UTC
// stamp>/" directory every archive entry must be nested under, capturing the
// path relative to it. The stamp format is 20060102T150405Z.
var bundleTopDirPattern = regexp.MustCompile(`^cenci-support-bundle-\d{8}T\d{6}Z/(.*)$`)

// extractBundle opens the .tar.gz at path and returns its file entries keyed
// by path relative to the single top-level "cenci-support-bundle-<stamp>/"
// directory (directory entries themselves, including that top-level one, are
// skipped). It fails the test if any entry falls outside that one top-level
// directory, or if more than one distinct top-level directory appears.
func extractBundle(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open bundle %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader for %s: %v", path, err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	entries := map[string][]byte{}
	var topDir string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read %s: %v", path, err)
		}

		m := bundleTopDirPattern.FindStringSubmatch(hdr.Name)
		if m == nil {
			t.Fatalf("archive entry %q is not nested under a cenci-support-bundle-<UTCstamp>/ dir", hdr.Name)
		}
		stamp := strings.TrimSuffix(hdr.Name, m[1])
		if topDir == "" {
			topDir = stamp
		} else if stamp != topDir {
			t.Fatalf("archive has more than one top-level dir: %q and %q", topDir, stamp)
		}

		rel := m[1]
		if hdr.Typeflag == tar.TypeDir || rel == "" {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read entry %s: %v", hdr.Name, err)
		}
		entries[rel] = data
	}
	return entries
}

func entryNames(entries map[string][]byte) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestSupportBundle_WritesArchiveWithExpectedEntries(t *testing.T) {
	fakeDir := t.TempDir()
	// Docker-only (not writeScriptedRuntimes' both-fakes setup): this test
	// verifies general bundle contents, not dual-runtime behavior, and
	// FAKE_PS below is unscoped (not _DOCKER/_PODMAN-keyed) so both fakes
	// would otherwise report the same two containers under both runtimes —
	// a genuine same-name collision (#629) that legitimately produces
	// runtime-disambiguated diagnose-*/logs/boot-* filenames, which would
	// break this test's plain-filename expectations below. See
	// TestSupportBundle_Collision_SameNameContainersUnderBothRuntimes_BothAppearAsDistinctEntries
	// in sandbox_dual_runtime_test.go for that collision behavior itself.
	writeDockerOnlyRuntime(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home := sbEnv(t, fakeDir, assets)
	writeConfigJSON(t, home, `{"marker":"config-marker-xyz"}`)

	const bootLog = "boot log line one\nboot log line two\n"
	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS="+fakePS, "FAKE_LOGS="+bootLog)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	entries := extractBundle(t, out)
	want := []string{
		"manifest.txt",
		"versions.txt",
		"environment.txt",
		"daemon-status.txt",
		"config.json",
		"diagnose-claude-cenci-myrepo.txt",
		"diagnose-codex-cenci-other.txt",
		"logs/boot-claude-cenci-myrepo.log",
		"logs/boot-codex-cenci-other.log",
	}
	sort.Strings(want)
	if got := entryNames(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("archive entries = %v, want %v", got, want)
	}

	if !strings.Contains(string(entries["diagnose-claude-cenci-myrepo.txt"]), "claude-cenci-myrepo") {
		t.Errorf("expected diagnose-claude-cenci-myrepo.txt to reference its container, got:\n%s", entries["diagnose-claude-cenci-myrepo.txt"])
	}
	if !strings.Contains(string(entries["diagnose-codex-cenci-other.txt"]), "codex-cenci-other") {
		t.Errorf("expected diagnose-codex-cenci-other.txt to reference its container, got:\n%s", entries["diagnose-codex-cenci-other.txt"])
	}
	if got := string(entries["logs/boot-claude-cenci-myrepo.log"]); !strings.Contains(got, bootLog) {
		t.Errorf("logs/boot-claude-cenci-myrepo.log = %q, want to contain the tailed boot log %q", got, bootLog)
	}
	if got := string(entries["logs/boot-codex-cenci-other.log"]); !strings.Contains(got, bootLog) {
		t.Errorf("logs/boot-codex-cenci-other.log = %q, want to contain the tailed boot log %q", got, bootLog)
	}
}

func TestSupportBundle_EnvironmentValuesStripped(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS=", "CENCI_SUPPORT_TEST_SECRET=topsecret")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	entries := extractBundle(t, out)
	envTxt, ok := entries["environment.txt"]
	if !ok {
		t.Fatalf("missing environment.txt entry")
	}
	if !strings.Contains(string(envTxt), "CENCI_SUPPORT_TEST_SECRET") {
		t.Errorf("expected environment.txt to list the variable NAME, got:\n%s", envTxt)
	}
	if strings.Contains(string(envTxt), "topsecret") {
		t.Errorf("environment.txt must list names only, never values, got:\n%s", envTxt)
	}

	for name, data := range entries {
		if strings.Contains(string(data), "topsecret") {
			t.Errorf("secret value leaked into archive entry %q:\n%s", name, data)
		}
	}
	if strings.Contains(string(output), "topsecret") {
		t.Errorf("secret value leaked into stdout (manifest/prompt), got:\n%s", output)
	}
}

func TestSupportBundle_ConfigIncludedVerbatim(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, home := sbEnv(t, fakeDir, assets)
	const marker = "config-marker-a1b2c3"
	writeConfigJSON(t, home, `{"marker":"`+marker+`"}`)

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS=")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	entries := extractBundle(t, out)
	config, ok := entries["config.json"]
	if !ok {
		t.Fatalf("missing config.json entry")
	}
	if !strings.Contains(string(config), marker) {
		t.Errorf("expected config.json content included verbatim (marker %q), got:\n%s", marker, config)
	}
}

func TestSupportBundle_ConfigMissing_PlaceholderEntry(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets) // no config.json seeded

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS=")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	entries := extractBundle(t, out)
	config, ok := entries["config.json"]
	if !ok {
		t.Fatalf("missing config.json entry (a placeholder is expected even when the file is absent)")
	}
	if !strings.HasPrefix(string(config), "(not found") {
		t.Errorf("expected a \"(not found ...)\" placeholder, got:\n%s", config)
	}
}

func TestSupportBundle_ManifestBeforeWrite(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS=")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	text := string(output)
	manifestIdx := strings.Index(text, "Support bundle manifest:")
	wroteIdx := strings.Index(text, "wrote "+out)
	if manifestIdx == -1 {
		t.Fatalf("expected the manifest section header (\"Support bundle manifest:\") in stdout, got:\n%s", text)
	}
	if wroteIdx == -1 {
		t.Fatalf("expected a %q line in stdout, got:\n%s", "wrote "+out, text)
	}
	if manifestIdx >= wroteIdx {
		t.Errorf("expected the manifest to print before the wrote-confirmation line, got:\n%s", text)
	}
}

func TestSupportBundle_ConfirmationDefaultDeny(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
	}{
		{"stdin EOF", ""},
		{"explicit no", "n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, _ := sbEnv(t, fakeDir, assets)

			outDir := t.TempDir()
			out := filepath.Join(outDir, "bundle.tar.gz")
			cmd := exec.Command(binaryPath, "support-bundle", "-o", out)
			cmd.Env = append(env, "FAKE_PS=")
			cmd.Dir = t.TempDir()
			cmd.Stdin = strings.NewReader(tc.stdin)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("support-bundle: %v\n%s", err, output)
			}

			text := string(output)
			if !strings.Contains(text, "aborted") {
				t.Errorf("expected \"aborted\" in stdout on decline, got:\n%s", text)
			}
			if strings.Contains(text, "wrote "+out) {
				t.Errorf("must not print a wrote-confirmation line after declining, got:\n%s", text)
			}
			if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
				t.Errorf("expected no file at %s after declining, stat err = %v", out, statErr)
			}
		})
	}
}

func TestSupportBundle_ConfirmationAccepted(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	outDir := t.TempDir()
	out := filepath.Join(outDir, "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "-o", out)
	cmd.Env = append(env, "FAKE_PS=")
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader("y\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	text := string(output)
	if !strings.Contains(text, "wrote "+out) {
		t.Errorf("expected a wrote-confirmation line after accepting, got:\n%s", text)
	}
	if _, statErr := os.Stat(out); statErr != nil {
		t.Errorf("expected the bundle file to exist at %s after accepting: %v", out, statErr)
	}
}

func TestSupportBundle_DefaultOutputPathInCwd(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	cwd := t.TempDir()
	cmd := exec.Command(binaryPath, "support-bundle", "--yes")
	cmd.Env = append(env, "FAKE_PS=")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	matches, globErr := filepath.Glob(filepath.Join(cwd, "cenci-support-bundle-*.tar.gz"))
	if globErr != nil {
		t.Fatalf("glob: %v", globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one default-named bundle in cwd, got %v\noutput:\n%s", matches, output)
	}
}

func TestSupportBundle_RefusesClobber(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	outDir := t.TempDir()
	out := filepath.Join(outDir, "bundle.tar.gz")
	const preexisting = "not a bundle, pre-existing content"
	if err := os.WriteFile(out, []byte(preexisting), 0o644); err != nil {
		t.Fatalf("seed pre-existing file: %v", err)
	}

	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS=")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got %T %v\n%s", err, err, output)
	}
	if !strings.Contains(string(output), "cenci support-bundle:") {
		t.Errorf("expected the \"cenci support-bundle:\" error prefix, got:\n%s", output)
	}

	data, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("read pre-existing file: %v", readErr)
	}
	if string(data) != preexisting {
		t.Errorf("pre-existing output file was modified, got:\n%s, want:\n%s", data, preexisting)
	}
}

func TestSupportBundle_NoContainers(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS=") // no containers
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Containers found: 0") {
		t.Errorf("expected the manifest to note zero containers, got:\n%s", output)
	}

	entries := extractBundle(t, out)
	want := []string{"manifest.txt", "versions.txt", "environment.txt", "daemon-status.txt", "config.json"}
	sort.Strings(want)
	if got := entryNames(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("archive entries = %v, want %v (no diagnose-*/boot-* entries with zero containers)", got, want)
	}
}

// TestSupportBundle_ContainerListingFailureVisible pins #572's
// failure-visibility-consistency rule for support-bundle: when
// sandbox.ListContainers fails (e.g. `<runtime> ps` errors), that failure
// must be surfaced visibly in the bundle rather than silently collapsing to
// "0 containers found" (indistinguishable from a genuinely empty host).
func TestSupportBundle_ContainerListingFailureVisible(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS=", "FAKE_PS_EXIT=1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	// The manifest (printed to stdout) must not silently claim zero
	// containers without any indication that listing actually failed.
	if !strings.Contains(string(output), "Containers found: 0") {
		t.Errorf("expected the manifest to note zero containers collected, got:\n%s", output)
	}

	entries := extractBundle(t, out)
	versions, ok := entries["versions.txt"]
	if !ok {
		t.Fatalf("missing versions.txt entry")
	}
	if !strings.Contains(string(versions), "containers: unavailable:") {
		t.Errorf("expected versions.txt to surface the container-listing failure visibly, got:\n%s", versions)
	}
}

func TestSupportBundle_ArchiveFileMode0600(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS=")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	info, statErr := os.Stat(out)
	if statErr != nil {
		t.Fatalf("stat bundle: %v", statErr)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("expected the bundle to not be group/world-accessible (may contain secrets), got mode %o", perm)
	}
}

func TestSupportBundle_ManifestListsSizesAndSecretsCaveat(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _ := sbEnv(t, fakeDir, assets)

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	cmd := exec.Command(binaryPath, "support-bundle", "--yes", "-o", out)
	cmd.Env = append(env, "FAKE_PS="+fakePS)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("support-bundle: %v\n%s", err, output)
	}

	text := string(output)
	if !strings.Contains(text, "versions.txt (") || !strings.Contains(text, " bytes)") {
		t.Errorf("expected the manifest to list per-entry byte sizes, got:\n%s", text)
	}
	if !strings.Contains(text, "Review before sharing") {
		t.Errorf("expected the manifest to include the secrets-review caveat, got:\n%s", text)
	}

	entries := extractBundle(t, out)
	manifest, ok := entries["manifest.txt"]
	if !ok {
		t.Fatalf("missing manifest.txt entry")
	}
	if !strings.Contains(string(manifest), "Review before sharing") {
		t.Errorf("expected the embedded manifest.txt to include the secrets-review caveat, got:\n%s", manifest)
	}
}

func TestSupportBundle_UsageErrors_Exit2(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"support-bundle", "--bogus"}},
		{"positional arg", []string{"support-bundle", "extra-arg"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			writeScriptedRuntimes(t, fakeDir)
			assets := writeAssetFixture(t)
			env, _ := sbEnv(t, fakeDir, assets)

			cmd := exec.Command(binaryPath, tc.args...)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("expected a usage error exit 2, got %T %v\n%s", err, err, output)
			}
			// Distinguishes a real "cenci support-bundle:"-prefixed usage
			// error from the generic "cenci: unknown subcommand" fallback
			// main.go prints for an unrecognized top-level verb — both
			// happen to exit 2, so the exit code alone can't tell them
			// apart.
			if !strings.Contains(string(output), "cenci support-bundle:") {
				t.Errorf("expected a \"cenci support-bundle:\"-prefixed usage error, not the generic unknown-subcommand fallback, got:\n%s", output)
			}
		})
	}
}
