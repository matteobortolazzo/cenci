package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveShortcut_ClaudeShortcuts(t *testing.T) {
	cases := map[string]string{
		"ch": "haiku",
		"cs": "sonnet",
		"co": "opus",
		"cf": "fable",
	}
	for token, wantModel := range cases {
		agent, model, ok := ResolveShortcut(token)
		if !ok || agent != "claude" || model != wantModel {
			t.Errorf("ResolveShortcut(%q) = (%q, %q, %v), want (claude, %q, true)", token, agent, model, ok, wantModel)
		}
	}
}

func TestResolveShortcut_CodexShortcuts(t *testing.T) {
	cases := map[string]string{
		"xl": "gpt-5.6-luna",
		"xt": "gpt-5.6-terra",
		"xs": "gpt-5.6-sol",
	}
	for token, wantModel := range cases {
		agent, model, ok := ResolveShortcut(token)
		if !ok || agent != "codex" || model != wantModel {
			t.Errorf("ResolveShortcut(%q) = (%q, %q, %v), want (codex, %q, true)", token, agent, model, ok, wantModel)
		}
	}
}

func TestResolveShortcut_Unrecognized(t *testing.T) {
	_, _, ok := ResolveShortcut("nope")
	if ok {
		t.Error("expected ok=false for an unrecognized token")
	}
}

func TestParseContainers_FiltersToSandboxPrefixAndParsesFields(t *testing.T) {
	raw := "claude-cenci-agentstack\tUp 2 hours\tcenci-sandbox:latest\n" +
		"codex-cenci-agentstack\tExited (0) 5 minutes ago\tcenci-sandbox:latest\n" +
		"some-other-container\tUp 1 hour\tnginx:latest\n"

	got := parseContainers(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 sandbox containers, got %d: %+v", len(got), got)
	}
	if got[0] != (Container{Name: "claude-cenci-agentstack", Status: "Up 2 hours", Image: "cenci-sandbox:latest"}) {
		t.Errorf("unexpected first container: %+v", got[0])
	}
	if got[1].Name != "codex-cenci-agentstack" {
		t.Errorf("unexpected second container name: %q", got[1].Name)
	}
}

func TestParseContainers_EmptyInput(t *testing.T) {
	if got := parseContainers(""); len(got) != 0 {
		t.Errorf("expected no containers for empty input, got %+v", got)
	}
}

func TestParseNames_FiltersToSandboxPrefix(t *testing.T) {
	raw := "claude-cenci-agentstack\ncodex-cenci-agentstack\nunrelated-container\n"
	got := parseNames(raw, "")
	want := []string{"claude-cenci-agentstack", "codex-cenci-agentstack"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parseNames(raw, \"\") = %v, want %v", got, want)
	}
}

func TestParseNames_SubstringFilter(t *testing.T) {
	raw := "claude-cenci-agentstack\ncodex-cenci-otherrepo\n"
	got := parseNames(raw, "agentstack")
	if len(got) != 1 || got[0] != "claude-cenci-agentstack" {
		t.Errorf("parseNames(raw, \"agentstack\") = %v, want [claude-cenci-agentstack]", got)
	}
}

// writeFakeBinary writes an executable POSIX shell script named name into
// dir, returning dir so callers can prepend it to PATH.
func writeFakeBinary(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func TestContainerRuntime_PrefersPodmanOverDocker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fakes only")
	}
	dir := t.TempDir()
	writeFakeBinary(t, dir, "podman", "exit 0")
	writeFakeBinary(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir)

	got, err := ContainerRuntime()
	if err != nil {
		t.Fatalf("ContainerRuntime: %v", err)
	}
	if got != "podman" {
		t.Errorf("ContainerRuntime() = %q, want podman (preferred when both are present)", got)
	}
}

func TestContainerRuntime_FallsBackToDockerWhenPodmanMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fakes only")
	}
	dir := t.TempDir()
	writeFakeBinary(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir)

	got, err := ContainerRuntime()
	if err != nil {
		t.Fatalf("ContainerRuntime: %v", err)
	}
	if got != "docker" {
		t.Errorf("ContainerRuntime() = %q, want docker", got)
	}
}

func TestContainerRuntime_ErrorsWhenNeitherFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	if _, err := ContainerRuntime(); err == nil {
		t.Error("expected an error when neither podman nor docker is on PATH")
	}
}
