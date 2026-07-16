package launcher

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
)

// buildEngine wires an Engine to a fake docker whose image listing is either
// empty or contains the current base and monolith image.
func buildEngine(t *testing.T, imagesMissing bool) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	images := "cenci-sandbox-base:abc123def456\\ncenci-sandbox:latest\\n"
	if imagesMissing {
		images = ""
	}
	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
if [ "$1" = images ]; then
  printf '` + images + `'
  exit 0
fi
if [ "$1" = image ] && [ "$2" = inspect ]; then
  printf '%s\n' shared-v2
  exit 0
fi
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

	if err := e.BuildMonolith(); err != nil {
		t.Fatalf("BuildMonolith: %v", err)
	}

	calls := readCallLog(t, callLog)
	wantBase := "build -f /assets/Dockerfile.base -t cenci-sandbox-base:abc123def456 -t cenci-sandbox-base:latest /assets"
	if !containsLine(calls, wantBase) {
		t.Errorf("base build missing; calls:\n%s", strings.Join(calls, "\n"))
	}
	wantMonolith := "build --build-arg BASE_VERSION=abc123def456 --label cenci.agent-cli=shared-v2 -t cenci-sandbox:latest -f /assets/Dockerfile /assets"
	if !containsLine(calls, wantMonolith) {
		t.Errorf("monolith build argv missing %q; calls:\n%s", wantMonolith, strings.Join(calls, "\n"))
	}
	for _, removed := range []string{"INSTALL_CLAUDE", "INSTALL_CODEX", "AGENTS_REFRESH"} {
		if containsLineWithAll(calls, removed) {
			t.Errorf("monolith build still passes removed %s argument; calls:\n%s", removed, strings.Join(calls, "\n"))
		}
	}
}

func TestBuildRepoImage_UsesRepoDockerfileContext(t *testing.T) {
	e, callLog := buildEngine(t, false) // base exists, only the repo build runs

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	calls := readCallLog(t, callLog)
	wantRepo := "build --build-arg BASE_VERSION=abc123def456 --label cenci.agent-cli=shared-v2 -t cenci-sandbox-myrepo:latest -f /repo/.cenci/Dockerfile /repo/.cenci"
	if !containsLine(calls, wantRepo) {
		t.Errorf("repo build argv missing %q; calls:\n%s", wantRepo, strings.Join(calls, "\n"))
	}
	if containsPrefix(calls, "build -f /assets/Dockerfile.base") {
		t.Errorf("base rebuilt although inspect succeeded; calls:\n%s", strings.Join(calls, "\n"))
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
