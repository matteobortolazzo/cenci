package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
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
	raw := "claude-cenci-agentstack\ncodex-cenci-agentstack\nopencode-cenci-agentstack\nunrelated-container\n"
	got := parseNames(raw, "")
	want := []string{"claude-cenci-agentstack", "codex-cenci-agentstack", "opencode-cenci-agentstack"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("parseNames(raw, \"\") = %v, want %v", got, want)
	}
}

func TestAgentForContainerName(t *testing.T) {
	cases := []struct {
		name      string
		container string
		wantAgent string
		wantOK    bool
	}{
		{"claude prefix", "claude-cenci-agentstack", "claude", true},
		{"codex prefix", "codex-cenci-agentstack", "codex", true},
		{"opencode prefix", "opencode-cenci-agentstack", "opencode", true},
		{"non-matching name", "some-other-container", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent, ok := AgentForContainerName(tc.container)
			if ok != tc.wantOK || agent != tc.wantAgent {
				t.Errorf("AgentForContainerName(%q) = (%q, %v), want (%q, %v)", tc.container, agent, ok, tc.wantAgent, tc.wantOK)
			}
		})
	}
}

func TestParseNames_SubstringFilter(t *testing.T) {
	raw := "claude-cenci-agentstack\ncodex-cenci-otherrepo\n"
	got := parseNames(raw, "agentstack")
	if len(got) != 1 || got[0] != "claude-cenci-agentstack" {
		t.Errorf("parseNames(raw, \"agentstack\") = %v, want [claude-cenci-agentstack]", got)
	}
}

// TestSupportedAgents_OwnedResourcesRecognized pins SupportedAgents (#528) as
// the single source of truth every ownership matcher must derive from: for
// every entry (claude, codex, opencode) its sandbox container prefix, its
// per-agent home volume name, and its shared agent-CLI volume name must all
// be recognized. This is the direct unit test at the package boundary the
// ticket calls for, so a future 4th agent only needs one SupportedAgents
// edit instead of separate updates to sandboxNamePattern and two volume
// matchers drifting out of sync (the exact bug #528 is about).
func TestSupportedAgents_OwnedResourcesRecognized(t *testing.T) {
	if len(SupportedAgents) != 3 {
		t.Fatalf("SupportedAgents = %v, want exactly 3 entries (claude, codex, opencode)", SupportedAgents)
	}
	for _, agent := range SupportedAgents {
		t.Run(agent, func(t *testing.T) {
			containerName := agent + "-cenci-agentstack"
			if !sandboxNamePattern.MatchString(containerName) {
				t.Errorf("sandboxNamePattern does not match container name %q for agent %q", containerName, agent)
			}
			if !IsSandboxContainerName(containerName) {
				t.Errorf("IsSandboxContainerName(%q) = false, want true", containerName)
			}

			homeVolume := agent + "-cenci-home-agentstack"
			if !IsHomeVolumeName(homeVolume) {
				t.Errorf("IsHomeVolumeName(%q) = false, want true", homeVolume)
			}

			agentCLIVolume := "cenci-agent-cli-" + agent
			if !IsAgentCLIVolumeName(agentCLIVolume) {
				t.Errorf("IsAgentCLIVolumeName(%q) = false, want true", agentCLIVolume)
			}
		})
	}
}

// TestIsHomeVolumeName_RejectsForeignLookAlikes pins the narrow match-miss
// boundary: names that merely contain an agent's prefix as a substring, or
// belong to an agent outside SupportedAgents, must not match.
func TestIsHomeVolumeName_RejectsForeignLookAlikes(t *testing.T) {
	cases := []string{
		"opencode-notcenci-home-x", // wrong middle segment, not "-cenci-home-"
		"opencode-elsewhere",       // no "-home-" segment at all
		"unrelated-home-volume",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if IsHomeVolumeName(name) {
				t.Errorf("IsHomeVolumeName(%q) = true, want false (foreign look-alike)", name)
			}
		})
	}
}

// TestIsAgentCLIVolumeName_RejectsForeignLookAlikes mirrors the home-volume
// negative case for the agent-CLI volume matcher.
func TestIsAgentCLIVolumeName_RejectsForeignLookAlikes(t *testing.T) {
	cases := []string{
		"cenci-agent-cli-opencode-foo", // extra trailing segment
		"opencode-elsewhere",
		"cenci-agent-cli-unknownagent",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if IsAgentCLIVolumeName(name) {
				t.Errorf("IsAgentCLIVolumeName(%q) = true, want false (foreign look-alike)", name)
			}
		})
	}
}

// TestPatternSources_EscapeRegexMetacharacters pins that a future agent name
// containing a regex metacharacter is escaped via regexp.QuoteMeta before
// being spliced into a pattern, rather than silently broadening the match
// (a "co." agent must not match "codex" the way an unescaped "." would).
func TestPatternSources_EscapeRegexMetacharacters(t *testing.T) {
	original := SupportedAgents
	defer func() { SupportedAgents = original }()
	SupportedAgents = []string{"co."}

	sandboxRe := regexp.MustCompile(sandboxNamePatternSource())
	if sandboxRe.MatchString("codex-cenci-x") {
		t.Errorf("sandboxNamePatternSource() unescaped %q, want literal match only", "co.")
	}
	if !sandboxRe.MatchString("co.-cenci-x") {
		t.Errorf("sandboxNamePatternSource() should still match the literal agent name %q", "co.")
	}

	homeRe := regexp.MustCompile(homeVolumePatternSource())
	if homeRe.MatchString("codex-cenci-home-x") {
		t.Errorf("homeVolumePatternSource() unescaped %q, want literal match only", "co.")
	}

	cliRe := regexp.MustCompile(agentCLIVolumePatternSource())
	if cliRe.MatchString("cenci-agent-cli-codex") {
		t.Errorf("agentCLIVolumePatternSource() unescaped %q, want literal match only", "co.")
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

// TestContainerRuntimePreferDocker_PrefersDockerOverPodman pins the dind
// mode's runtime resolution (#585): unlike the default ContainerRuntime
// (podman-first), dind must run under Docker as the outer runtime, so
// ContainerRuntimePreferDocker resolves docker first when both are present.
func TestContainerRuntimePreferDocker_PrefersDockerOverPodman(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fakes only")
	}
	dir := t.TempDir()
	writeFakeBinary(t, dir, "podman", "exit 0")
	writeFakeBinary(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir)

	got, err := ContainerRuntimePreferDocker()
	if err != nil {
		t.Fatalf("ContainerRuntimePreferDocker: %v", err)
	}
	if got != "docker" {
		t.Errorf("ContainerRuntimePreferDocker() = %q, want docker (preferred when both are present)", got)
	}
}

func TestContainerRuntimePreferDocker_FallsBackToPodmanWhenDockerMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fakes only")
	}
	dir := t.TempDir()
	writeFakeBinary(t, dir, "podman", "exit 0")
	t.Setenv("PATH", dir)

	got, err := ContainerRuntimePreferDocker()
	if err != nil {
		t.Fatalf("ContainerRuntimePreferDocker: %v", err)
	}
	if got != "podman" {
		t.Errorf("ContainerRuntimePreferDocker() = %q, want podman", got)
	}
}

func TestContainerRuntimePreferDocker_ErrorsWhenNeitherFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	if _, err := ContainerRuntimePreferDocker(); err == nil {
		t.Error("expected an error when neither docker nor podman is on PATH")
	}
}

// TestIsDindVolumeName_RecognizesPerAgentDindVolumes mirrors
// TestSupportedAgents_OwnedResourcesRecognized for the new dind storage
// volume (#585): "<agent>-cenci-dind-<slug>", optionally suffixed
// "-<name>" the same way Scope.VolumeName's --name suffix handling works.
func TestIsDindVolumeName_RecognizesPerAgentDindVolumes(t *testing.T) {
	for _, agent := range SupportedAgents {
		t.Run(agent, func(t *testing.T) {
			plain := agent + "-cenci-dind-myrepo"
			if !IsDindVolumeName(plain) {
				t.Errorf("IsDindVolumeName(%q) = false, want true", plain)
			}
			named := agent + "-cenci-dind-myrepo-instance"
			if !IsDindVolumeName(named) {
				t.Errorf("IsDindVolumeName(%q) = false, want true (--name suffix)", named)
			}
		})
	}
}

// TestIsDindVolumeName_RejectsForeignLookAlikes mirrors the home/agent-CLI
// volume matchers' negative case for the dind storage volume matcher.
func TestIsDindVolumeName_RejectsForeignLookAlikes(t *testing.T) {
	cases := []string{
		"opencode-notcenci-dind-x", // wrong middle segment, not "-cenci-dind-"
		"opencode-elsewhere",       // no "-dind-" segment at all
		"unrelated-dind-volume",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if IsDindVolumeName(name) {
				t.Errorf("IsDindVolumeName(%q) = true, want false (foreign look-alike)", name)
			}
		})
	}
}
