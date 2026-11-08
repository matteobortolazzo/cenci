package main_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSandboxUpdatePluginsCodex_RunsOneShotVolumeUpdate(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--agent", "codex")
	cmd.Env = batchEnv(t, fakeDir, assets) // image exists, no running container
	cmd.Dir = t.TempDir()                  // non-git cwd → legacy "default" scope
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-plugins --agent codex: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	prefix := "run --rm --user root -e HOST_UID=" + itoa(os.Getuid()) +
		" -e HOST_GID=" + itoa(os.Getgid()) +
		" -e CENCI_SANDBOX_AGENT=codex -e CENCI_SANDBOX_PLUGINS=cenci cenci-watch -e CENCI_AGENT_CLI=/opt/cenci-agent/current/node_modules/.bin/codex" +
		" -v codex-cenci-home-default:/home/dev -v cenci-agent-cli-codex:/opt/cenci-agent:ro cenci-sandbox:latest -c "
	line, ok := findLineWithPrefix(lines, prefix)
	if !ok {
		t.Fatalf("expected a one-shot volume update run [%s...], got calls:\n%s", prefix, strings.Join(lines, "\n"))
	}
	if !strings.Contains(line, "provision_codex_plugins") {
		t.Errorf("expected the codex provisioning command, got: %s", line)
	}
}

func TestSandboxUpdatePlugins_BadAgent_Exits2NoRuntimeCalls(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--agent", "bogus")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls on a bad --agent, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUpdatePluginsAll_RefreshesEveryRunningContainer(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--all")
	cmd.Env = append(batchEnv(t, fakeDir, assets), "FAKE_PS=claude-cenci-one\nclaude-cenci-two\ncodex-cenci-three\nopencode-cenci-paused\n")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox update-plugins --all: %v\n%s", err, output)
	}

	lines := callLogLines(t, callLog)
	for _, name := range []string{"claude-cenci-one", "claude-cenci-two", "codex-cenci-three"} {
		if !anyLineContains(lines, "exec -u dev "+name+" /bin/bash -c") {
			t.Errorf("expected a plugin refresh exec for %q, got calls:\n%s", name, strings.Join(lines, "\n"))
		}
	}
	if anyLineContains(lines, "exec -u dev opencode-cenci-paused") {
		t.Fatalf("bulk plugin refresh touched disabled OpenCode: %v", lines)
	}
}

func TestSandboxUpdatePluginsAll_WithExplicitName_UsageError(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--all", "--name", "x")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected --all --name usage error exit 2, got %T %v\n%s", err, err, output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls for --all --name usage error, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSandboxUpdatePluginsAll_WithExplicitAgent_UsageError(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)

	cmd := exec.Command(binaryPath, "sandbox", "update-plugins", "--all", "--agent", "codex")
	cmd.Env = batchEnv(t, fakeDir, assets)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected --all --agent usage error exit 2, got %T %v\n%s", err, err, output)
	}
	if lines := callLogLines(t, callLog); len(lines) > 0 {
		t.Errorf("expected no runtime calls for --all --agent usage error, got:\n%s", strings.Join(lines, "\n"))
	}
}
