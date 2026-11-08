package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

func TestSandboxBuildBase_BuildsBaseImageNatively(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "build-base")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build-base: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	want := "build -f " + assets + "/Dockerfile.base -t cenci-sandbox-base:" + tag + " -t cenci-sandbox-base:latest " + assets
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("runtime calls = %v, want exactly [%s]", lines, want)
	}
	if !strings.Contains(string(output), "Building cenci-sandbox-base:"+tag) {
		t.Errorf("expected build progress message, got:\n%s", output)
	}
}

func TestSandboxBuild_MonolithBuildsBaseFirstWhenMissing(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_IMAGES=")
	cmd.Dir = t.TempDir() // non-git cwd → monolith image
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	if _, ok := findLineWithPrefix(lines, "images --format {{.Repository}}:{{.Tag}} cenci-sandbox-base:"+tag); !ok {
		t.Errorf("expected a typed base-image listing, got calls:\n%s", strings.Join(lines, "\n"))
	}
	if !anyLineContains(lines, "-t cenci-sandbox-base:"+tag) {
		t.Errorf("expected the missing base image to be built first, got calls:\n%s", strings.Join(lines, "\n"))
	}
	want := "build --build-arg BASE_VERSION=" + tag + " --label cenci.agent-cli=shared-v2 --label cenci.base-version=" + tag + " -t cenci-sandbox:latest -f " + assets + "/Dockerfile " + assets
	if !anyLineContains(lines, want) {
		t.Errorf("expected monolith build call %q, got calls:\n%s", want, strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "INSTALL_CLAUDE") || anyLineContains(lines, "INSTALL_CODEX") || anyLineContains(lines, "AGENTS_REFRESH") {
		t.Errorf("build still passed removed agent build arguments; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxBuild_RepoImageFromRepoDockerfile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}

	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".cenci"), 0o755); err != nil {
		t.Fatalf("mkdir .cenci: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".cenci", "Dockerfile"), []byte("FROM cenci-sandbox-base\n"), 0o644); err != nil {
		t.Fatalf("write repo Dockerfile: %v", err)
	}
	slug := launcher.Slugify(filepath.Base(repo))
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_IMAGES=")
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox build (repo): %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	want := "build --build-arg BASE_VERSION=" + tag + " --label cenci.agent-cli=shared-v2 --label cenci.base-version=" + tag + " -t cenci-sandbox-" + slug + ":latest -f " + repoRoot + "/.cenci/Dockerfile " + repoRoot + "/.cenci"
	if !anyLineContains(lines, want) {
		t.Errorf("expected repo-image build call %q, got calls:\n%s", want, strings.Join(lines, "\n"))
	}
	if anyLineContains(lines, "INSTALL_CLAUDE") || anyLineContains(lines, "INSTALL_CODEX") || anyLineContains(lines, "AGENTS_REFRESH") {
		t.Errorf("repo build still passed removed agent build arguments; calls:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxBuild_AgentsFlagIsGone(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "build", "--agents", "claude")
	cmd.Env = batchEnv(t, fakeDir, assets)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected removed --agents flag to exit 2, got %T %v\n%s", err, err, output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls for removed --agents flag, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxBuild_BuildFailureExits1(t *testing.T) {
	fakeDir := t.TempDir()
	writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "build")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_IMAGES=", "FAKE_BUILD_EXIT=3")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "cenci sandbox build:") {
		t.Errorf("expected a 'cenci sandbox build:' error, got:\n%s", output)
	}
}
