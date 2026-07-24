package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
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

// -- AvailableRuntimes / RuntimesWithContainer / RuntimesWithVolume /
// ListAllContainers / RunningSandboxContainersAll (#629 dual-runtime
// enumeration and ownership resolution) -------------------------------------
//
// On a host with both docker and podman installed, host-wide sandbox
// commands must enumerate every installed runtime rather than collapsing to
// one preferred runtime (AvailableRuntimes), and scope-resolving commands
// must resolve every runtime that actually owns a given container/volume
// name — including both, on a same-name collision — rather than silently
// picking one (RuntimesWithContainer/RuntimesWithVolume). A failed
// per-runtime query must never be silently read as "this runtime doesn't
// have it" (AC #4).

// writeRuntimeStub writes a fake runtime binary (docker or podman) to dir
// that answers `ps -a --format ...` (ListContainers/ListAllContainers) with
// psAll/psAllExit, `ps --format ...` (RunningSandboxContainers/
// RunningSandboxContainersAll) with ps/psExit, and `volume ls --format ...`
// (RuntimesWithVolume) with volumes/volumesExit — independent exit codes per
// query shape so ownership/aggregation failure tests can fail one query
// shape without the other.
func writeRuntimeStub(t *testing.T, dir, name string, psAll string, psAllExit int, ps string, psExit int, volumes string, volumesExit int) {
	t.Helper()
	body := fmt.Sprintf(`case "$1" in
ps)
  if [ "$2" = "-a" ]; then
    printf '%%s' %s
    exit %d
  fi
  printf '%%s' %s
  exit %d
  ;;
volume)
  if [ "$2" = "ls" ]; then
    printf '%%s' %s
    exit %d
  fi
  ;;
esac
exit 0`, exectest.ShellQuote(psAll), psAllExit, exectest.ShellQuote(ps), psExit, exectest.ShellQuote(volumes), volumesExit)
	writeFakeBinary(t, dir, name, body)
}

func TestAvailableRuntimes_BothPresent_DeterministicDockerThenPodmanOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fakes only")
	}
	dir := t.TempDir()
	// Written podman-first on disk to prove the returned order is
	// deterministic (docker, podman) rather than PATH/write order.
	writeFakeBinary(t, dir, "podman", "exit 0")
	writeFakeBinary(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir)

	got, err := AvailableRuntimes()
	if err != nil {
		t.Fatalf("AvailableRuntimes: %v", err)
	}
	want := []string{"docker", "podman"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("AvailableRuntimes() = %v, want %v (deterministic docker-then-podman order, independent of launch preference)", got, want)
	}
}

func TestAvailableRuntimes_OnlyDockerPresent(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir)

	got, err := AvailableRuntimes()
	if err != nil {
		t.Fatalf("AvailableRuntimes: %v", err)
	}
	if len(got) != 1 || got[0] != "docker" {
		t.Errorf("AvailableRuntimes() = %v, want [docker]", got)
	}
}

func TestAvailableRuntimes_OnlyPodmanPresent(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "podman", "exit 0")
	t.Setenv("PATH", dir)

	got, err := AvailableRuntimes()
	if err != nil {
		t.Fatalf("AvailableRuntimes: %v", err)
	}
	if len(got) != 1 || got[0] != "podman" {
		t.Errorf("AvailableRuntimes() = %v, want [podman]", got)
	}
}

func TestAvailableRuntimes_ErrorsNamingBothWhenNeitherFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	_, err := AvailableRuntimes()
	if err == nil {
		t.Fatal("expected an error when neither docker nor podman is on PATH")
	}
	if !strings.Contains(err.Error(), "docker") || !strings.Contains(err.Error(), "podman") {
		t.Errorf("AvailableRuntimes() error = %q, want it to name both docker and podman", err)
	}
}

func TestRuntimesWithContainer_SingleOwner(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "claude-cenci-x\tUp 2 hours\tcenci-sandbox:latest\n", 0, "", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", "", 0, "", 0, "", 0)
	t.Setenv("PATH", dir)

	owners, err := RuntimesWithContainer([]string{"docker", "podman"}, "claude-cenci-x")
	if err != nil {
		t.Fatalf("RuntimesWithContainer: %v", err)
	}
	if len(owners) != 1 || owners[0] != "docker" {
		t.Errorf("RuntimesWithContainer() = %v, want [docker]", owners)
	}
}

// TestRuntimesWithContainer_BothOwnSameName_ReturnsBothOwners pins AC #3: a
// same-name collision must return every owning runtime, never silently one.
func TestRuntimesWithContainer_BothOwnSameName_ReturnsBothOwners(t *testing.T) {
	dir := t.TempDir()
	row := "claude-cenci-x\tUp 2 hours\tcenci-sandbox:latest\n"
	writeRuntimeStub(t, dir, "docker", row, 0, "", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", row, 0, "", 0, "", 0)
	t.Setenv("PATH", dir)

	owners, err := RuntimesWithContainer([]string{"docker", "podman"}, "claude-cenci-x")
	if err != nil {
		t.Fatalf("RuntimesWithContainer: %v", err)
	}
	if len(owners) != 2 || owners[0] != "docker" || owners[1] != "podman" {
		t.Errorf("RuntimesWithContainer() = %v, want [docker podman] (both owners on a same-name collision)", owners)
	}
}

func TestRuntimesWithContainer_NoOwner_ReturnsEmptyNoError(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 0, "", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", "", 0, "", 0, "", 0)
	t.Setenv("PATH", dir)

	owners, err := RuntimesWithContainer([]string{"docker", "podman"}, "claude-cenci-x")
	if err != nil {
		t.Fatalf("RuntimesWithContainer: %v", err)
	}
	if len(owners) != 0 {
		t.Errorf("RuntimesWithContainer() = %v, want none", owners)
	}
}

// TestRuntimesWithContainer_OneRuntimeQueryFails_AggregatesErrorRatherThanReadingAbsent
// pins AC #4: a failed per-runtime query must never be silently read as
// "this runtime doesn't have it" — it must surface as a non-nil error so a
// caller can never mistake a query failure for a genuine "not found".
func TestRuntimesWithContainer_OneRuntimeQueryFails_AggregatesErrorRatherThanReadingAbsent(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 1, "", 0, "", 0) // docker's `ps -a` fails
	writeRuntimeStub(t, dir, "podman", "", 0, "", 0, "", 0) // podman succeeds, empty
	t.Setenv("PATH", dir)

	owners, err := RuntimesWithContainer([]string{"docker", "podman"}, "claude-cenci-x")
	if err == nil {
		t.Fatal("expected a non-nil error when one runtime's query fails, never silently absent")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("error = %v, want it to name the failing runtime (docker)", err)
	}
	if len(owners) != 0 {
		t.Errorf("owners = %v, want none reported as owning it despite the docker failure", owners)
	}
}

func TestRuntimesWithVolume_SingleOwner(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 0, "", 0, "cenci-agent-cli-claude\n", 0)
	writeRuntimeStub(t, dir, "podman", "", 0, "", 0, "", 0)
	t.Setenv("PATH", dir)

	owners, err := RuntimesWithVolume([]string{"docker", "podman"}, "cenci-agent-cli-claude")
	if err != nil {
		t.Fatalf("RuntimesWithVolume: %v", err)
	}
	if len(owners) != 1 || owners[0] != "docker" {
		t.Errorf("RuntimesWithVolume() = %v, want [docker]", owners)
	}
}

// TestRuntimesWithVolume_BothOwnSameName_ReturnsBothOwners pins the
// update-agent "runtime already has it" resolution (Q4): when the shared
// agent-CLI volume already exists under both runtimes, both must be
// reported so the caller updates every runtime that already has it.
func TestRuntimesWithVolume_BothOwnSameName_ReturnsBothOwners(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 0, "", 0, "cenci-agent-cli-claude\n", 0)
	writeRuntimeStub(t, dir, "podman", "", 0, "", 0, "cenci-agent-cli-claude\n", 0)
	t.Setenv("PATH", dir)

	owners, err := RuntimesWithVolume([]string{"docker", "podman"}, "cenci-agent-cli-claude")
	if err != nil {
		t.Fatalf("RuntimesWithVolume: %v", err)
	}
	if len(owners) != 2 || owners[0] != "docker" || owners[1] != "podman" {
		t.Errorf("RuntimesWithVolume() = %v, want [docker podman]", owners)
	}
}

func TestRuntimesWithVolume_NoOwner_ReturnsEmptyNoError(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 0, "", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", "", 0, "", 0, "", 0)
	t.Setenv("PATH", dir)

	owners, err := RuntimesWithVolume([]string{"docker", "podman"}, "cenci-agent-cli-claude")
	if err != nil {
		t.Fatalf("RuntimesWithVolume: %v", err)
	}
	if len(owners) != 0 {
		t.Errorf("RuntimesWithVolume() = %v, want none", owners)
	}
}

// TestRuntimesWithVolume_OneRuntimeQueryFails_AggregatesErrorRatherThanReadingAbsent
// mirrors the container-ownership failure-visibility pin for volumes (AC #4).
func TestRuntimesWithVolume_OneRuntimeQueryFails_AggregatesErrorRatherThanReadingAbsent(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 0, "", 0, "", 1) // docker's `volume ls` fails
	writeRuntimeStub(t, dir, "podman", "", 0, "", 0, "", 0)
	t.Setenv("PATH", dir)

	owners, err := RuntimesWithVolume([]string{"docker", "podman"}, "cenci-agent-cli-claude")
	if err == nil {
		t.Fatal("expected a non-nil error when one runtime's volume query fails")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("error = %v, want it to name the failing runtime (docker)", err)
	}
	if len(owners) != 0 {
		t.Errorf("owners = %v, want none reported despite the docker failure", owners)
	}
}

func TestListAllContainers_TagsEachRowWithItsOwningRuntime(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "claude-cenci-x\tUp 2 hours\tcenci-sandbox:latest\n", 0, "", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", "codex-cenci-y\tExited (0) 1 hour ago\tcenci-sandbox:latest\n", 0, "", 0, "", 0)
	t.Setenv("PATH", dir)

	rows, err := ListAllContainers([]string{"docker", "podman"})
	if err != nil {
		t.Fatalf("ListAllContainers: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListAllContainers() = %+v, want 2 rows", rows)
	}
	if rows[0].Runtime != "docker" || rows[0].Container.Name != "claude-cenci-x" {
		t.Errorf("unexpected first row: %+v", rows[0])
	}
	if rows[1].Runtime != "podman" || rows[1].Container.Name != "codex-cenci-y" {
		t.Errorf("unexpected second row: %+v", rows[1])
	}
}

// TestListAllContainers_SameNameUnderBothRuntimes_ReturnsTwoDistinctRows pins
// AC #3: a same-name collision must produce two distinct, runtime-tagged
// rows rather than silently deduplicating.
func TestListAllContainers_SameNameUnderBothRuntimes_ReturnsTwoDistinctRows(t *testing.T) {
	dir := t.TempDir()
	row := "claude-cenci-x\tUp 2 hours\tcenci-sandbox:latest\n"
	writeRuntimeStub(t, dir, "docker", row, 0, "", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", row, 0, "", 0, "", 0)
	t.Setenv("PATH", dir)

	rows, err := ListAllContainers([]string{"docker", "podman"})
	if err != nil {
		t.Fatalf("ListAllContainers: %v", err)
	}
	if len(rows) != 2 || rows[0].Runtime == rows[1].Runtime {
		t.Errorf("ListAllContainers() = %+v, want two distinct runtime-tagged rows for the same container name", rows)
	}
}

// TestListAllContainers_OneRuntimeFails_ReturnsPartialRowsPlusAggregatedError
// pins AC #4 for the aggregating lister: the healthy runtime's rows must
// still come back, plus a non-nil error naming the failing runtime — never a
// silently empty result.
func TestListAllContainers_OneRuntimeFails_ReturnsPartialRowsPlusAggregatedError(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "claude-cenci-x\tUp 2 hours\tcenci-sandbox:latest\n", 0, "", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", "", 1, "", 0, "", 0) // podman's `ps -a` fails
	t.Setenv("PATH", dir)

	rows, err := ListAllContainers([]string{"docker", "podman"})
	if err == nil {
		t.Fatal("expected an aggregated error when one runtime's listing fails")
	}
	if !strings.Contains(err.Error(), "podman") {
		t.Errorf("error = %v, want it to name the failing runtime (podman)", err)
	}
	if len(rows) != 1 || rows[0].Runtime != "docker" {
		t.Errorf("rows = %+v, want the healthy docker row still present (partial results, never silently empty)", rows)
	}
}

func TestRunningSandboxContainersAll_TagsEachNameWithItsOwningRuntime(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 0, "claude-cenci-x\n", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", "", 0, "codex-cenci-y\n", 0, "", 0)
	t.Setenv("PATH", dir)

	got, err := RunningSandboxContainersAll([]string{"docker", "podman"}, "")
	if err != nil {
		t.Fatalf("RunningSandboxContainersAll: %v", err)
	}
	want := []RunningContainer{{Runtime: "docker", Name: "claude-cenci-x"}, {Runtime: "podman", Name: "codex-cenci-y"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("RunningSandboxContainersAll() = %+v, want %+v", got, want)
	}
}

func TestRunningSandboxContainersAll_SubstringFilterAppliesAcrossRuntimes(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 0, "claude-cenci-agentstack\n", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", "", 0, "codex-cenci-otherrepo\n", 0, "", 0)
	t.Setenv("PATH", dir)

	got, err := RunningSandboxContainersAll([]string{"docker", "podman"}, "agentstack")
	if err != nil {
		t.Fatalf("RunningSandboxContainersAll: %v", err)
	}
	if len(got) != 1 || got[0].Name != "claude-cenci-agentstack" {
		t.Errorf("RunningSandboxContainersAll(filter) = %+v, want only the agentstack match", got)
	}
}

// TestRunningSandboxContainersAll_OneRuntimeFails_ReturnsPartialPlusAggregatedError
// mirrors ListAllContainers' failure-visibility pin (AC #4) for the running-
// containers aggregator `stop`/`update-plugins --all` rely on.
func TestRunningSandboxContainersAll_OneRuntimeFails_ReturnsPartialPlusAggregatedError(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeStub(t, dir, "docker", "", 0, "claude-cenci-x\n", 0, "", 0)
	writeRuntimeStub(t, dir, "podman", "", 0, "", 1, "", 0) // podman's plain `ps` fails
	t.Setenv("PATH", dir)

	got, err := RunningSandboxContainersAll([]string{"docker", "podman"}, "")
	if err == nil {
		t.Fatal("expected an aggregated error when one runtime's listing fails")
	}
	if !strings.Contains(err.Error(), "podman") {
		t.Errorf("error = %v, want it to name the failing runtime (podman)", err)
	}
	if len(got) != 1 || got[0].Runtime != "docker" {
		t.Errorf("got = %+v, want the healthy docker entry still present", got)
	}
}
