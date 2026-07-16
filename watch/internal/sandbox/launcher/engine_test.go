package launcher

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
)

// buildEngine wires an Engine to a fake docker whose `image inspect` exit
// code is scripted: inspectFails=true makes every inspect fail so ensure
// paths build.
func buildEngine(t *testing.T, inspectFails bool) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	inspectExit := "0"
	if inspectFails {
		inspectExit = "1"
	}
	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
if [ "$1" = image ] && [ "$2" = inspect ]; then exit ` + inspectExit + `; fi
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, "docker"), body)
	t.Setenv("PATH", dir)

	var out bytes.Buffer
	return &Engine{
		Runtime:  "docker",
		AssetDir: "/assets",
		BaseTag:  "abc123def456",
		Stdin:    strings.NewReader(""),
		Stdout:   &out,
		Stderr:   &out,
	}, callLog
}

func TestBuildBase_ArgvMatchesCenciSand(t *testing.T) {
	e, callLog := buildEngine(t, true)

	if err := e.BuildBase(); err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	want := "build -f /assets/Dockerfile.base -t cenci-sandbox-base:abc123def456 -t cenci-sandbox-base:latest /assets"
	if calls := readCallLog(t, callLog); !containsLine(calls, want) {
		t.Errorf("BuildBase argv missing %q; calls:\n%s", want, strings.Join(calls, "\n"))
	}
}

func TestBuildMonolith_BuildsBaseFirstWhenMissing(t *testing.T) {
	e, callLog := buildEngine(t, true)

	if err := e.BuildMonolith(AllAgents()); err != nil {
		t.Fatalf("BuildMonolith: %v", err)
	}

	calls := readCallLog(t, callLog)
	wantBase := "build -f /assets/Dockerfile.base -t cenci-sandbox-base:abc123def456 -t cenci-sandbox-base:latest /assets"
	if !containsLine(calls, wantBase) {
		t.Errorf("base build missing; calls:\n%s", strings.Join(calls, "\n"))
	}
	// AGENTS_REFRESH is a wall-clock timestamp, so match the stable pieces by
	// substring rather than the whole line.
	if !containsLineWithAll(calls,
		"build --build-arg BASE_VERSION=abc123def456",
		"--build-arg INSTALL_CLAUDE=1", "--build-arg INSTALL_CODEX=1", "--build-arg AGENTS_REFRESH=",
		"-t cenci-sandbox:latest -f /assets/Dockerfile /assets") {
		t.Errorf("monolith build missing/omitted agent args; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestBuildMonolith_ClaudeOnlyDropsCodexLayer(t *testing.T) {
	e, callLog := buildEngine(t, false) // base exists

	if err := e.BuildMonolith(BuildAgents{Claude: true}); err != nil {
		t.Fatalf("BuildMonolith: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLineWithAll(calls, "-t cenci-sandbox:latest", "--build-arg INSTALL_CLAUDE=1", "--build-arg INSTALL_CODEX=0") {
		t.Errorf("claude-only monolith build should set INSTALL_CODEX=0; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestBuildRepoImage_UsesRepoDockerfileContext(t *testing.T) {
	e, callLog := buildEngine(t, false) // base exists, only the repo build runs

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	calls := readCallLog(t, callLog)
	// A per-repo image always bakes both agents (INSTALL_*=1), regardless of any
	// per-user selection.
	if !containsLineWithAll(calls,
		"build --build-arg BASE_VERSION=abc123def456",
		"--build-arg INSTALL_CLAUDE=1", "--build-arg INSTALL_CODEX=1", "--build-arg AGENTS_REFRESH=",
		"-t cenci-sandbox-myrepo:latest -f /repo/.cenci/Dockerfile /repo/.cenci") {
		t.Errorf("repo build argv missing expected pieces; calls:\n%s", strings.Join(calls, "\n"))
	}
	if containsPrefix(calls, "build -f /assets/Dockerfile.base") {
		t.Errorf("base rebuilt although inspect succeeded; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestParseBuildAgents(t *testing.T) {
	cases := []struct {
		in      string
		want    BuildAgents
		wantErr bool
	}{
		{"", BuildAgents{true, true}, false},
		{"claude,codex", BuildAgents{true, true}, false},
		{"claude", BuildAgents{true, false}, false},
		{"codex", BuildAgents{false, true}, false},
		{" codex , ", BuildAgents{false, true}, false},
		{"gemini", BuildAgents{}, true},
		{",", BuildAgents{}, true},
	}
	for _, c := range cases {
		got, err := ParseBuildAgents(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseBuildAgents(%q): expected error, got %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseBuildAgents(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseBuildAgents(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestEnsureImage_SkipsBuildWhenPresent(t *testing.T) {
	e, callLog := buildEngine(t, false)

	scope := Scope{Image: MonolithImage}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	if calls := readCallLog(t, callLog); containsPrefix(calls, "build") {
		t.Errorf("EnsureImage built although the image exists; calls:\n%s", strings.Join(calls, "\n"))
	}
}
