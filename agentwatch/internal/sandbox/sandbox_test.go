package sandbox

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBatchFlag_KnownVerbs(t *testing.T) {
	cases := map[string]string{
		"build":          "--build",
		"build-base":     "--build-base",
		"update-plugins": "--update-plugins",
		"reseed-creds":   "--reseed-creds",
		"reap-orphans":   "--reap-orphans",
	}
	for verb, want := range cases {
		got, ok := BatchFlag(verb)
		if !ok {
			t.Errorf("BatchFlag(%q): expected ok=true", verb)
		}
		if got != want {
			t.Errorf("BatchFlag(%q) = %q, want %q", verb, got, want)
		}
	}
}

func TestBatchFlag_PruneNotInTable(t *testing.T) {
	// prune takes an optional --volumes flag, so it is handled separately in
	// main.go rather than through this 1:1 table.
	if _, ok := BatchFlag("prune"); ok {
		t.Error("expected prune to be excluded from the batch flag table")
	}
}

func TestBatchFlag_UnknownVerb(t *testing.T) {
	if _, ok := BatchFlag("bogus"); ok {
		t.Error("expected ok=false for an unknown verb")
	}
}

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
	raw := "claude-sand-agentstack\tUp 2 hours\tagent-sandbox:latest\n" +
		"codex-sand-agentstack\tExited (0) 5 minutes ago\tagent-sandbox:latest\n" +
		"some-other-container\tUp 1 hour\tnginx:latest\n"

	got := parseContainers(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 sandbox containers, got %d: %+v", len(got), got)
	}
	if got[0] != (Container{Name: "claude-sand-agentstack", Status: "Up 2 hours", Image: "agent-sandbox:latest"}) {
		t.Errorf("unexpected first container: %+v", got[0])
	}
	if got[1].Name != "codex-sand-agentstack" {
		t.Errorf("unexpected second container name: %q", got[1].Name)
	}
}

func TestParseContainers_EmptyInput(t *testing.T) {
	if got := parseContainers(""); len(got) != 0 {
		t.Errorf("expected no containers for empty input, got %+v", got)
	}
}

func TestParseNames_FiltersToSandboxPrefix(t *testing.T) {
	raw := "claude-sand-agentstack\ncodex-sand-agentstack\nunrelated-container\n"
	got := parseNames(raw, "")
	want := []string{"claude-sand-agentstack", "codex-sand-agentstack"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parseNames(raw, \"\") = %v, want %v", got, want)
	}
}

func TestParseNames_SubstringFilter(t *testing.T) {
	raw := "claude-sand-agentstack\ncodex-sand-otherrepo\n"
	got := parseNames(raw, "agentstack")
	if len(got) != 1 || got[0] != "claude-sand-agentstack" {
		t.Errorf("parseNames(raw, \"agentstack\") = %v, want [claude-sand-agentstack]", got)
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
		t.Errorf("ContainerRuntime() = %q, want podman (agent-sand prefers podman when both are present)", got)
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

func TestRunAgentSand_MissingBinaryReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	_, err := RunAgentSand([]string{"--build"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Error("expected an error when agent-sand is not on PATH")
	}
}

func TestRunAgentSand_ReturnsChildExitCodeAndWiresArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake only")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	writeFakeBinary(t, dir, "agent-sand", `printf '%s\n' "$@" > "`+capture+`"
exit 3`)
	t.Setenv("PATH", dir)

	code, err := RunAgentSand([]string{"--build"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("RunAgentSand: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if string(got) != "--build\n" {
		t.Errorf("captured argv = %q, want %q", got, "--build\n")
	}
}
