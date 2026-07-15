package launcher

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
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
printf '%s\n' "$*" >> ` + shellQuote(callLog) + `
if [ "$1" = image ] && [ "$2" = inspect ]; then exit ` + inspectExit + `; fi
exit 0
`
	writeExecutable(t, filepath.Join(dir, "docker"), body)
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
	wantMono := "build --build-arg BASE_VERSION=abc123def456 -t cenci-sandbox:latest -f /assets/Dockerfile /assets"
	if !containsLine(calls, wantBase) {
		t.Errorf("base build missing; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !containsLine(calls, wantMono) {
		t.Errorf("monolith build missing; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestBuildRepoImage_UsesRepoDockerfileContext(t *testing.T) {
	e, callLog := buildEngine(t, false) // base exists, only the repo build runs

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	calls := readCallLog(t, callLog)
	want := "build --build-arg BASE_VERSION=abc123def456 -t cenci-sandbox-myrepo:latest -f /repo/.cenci/Dockerfile /repo/.cenci"
	if !containsLine(calls, want) {
		t.Errorf("repo build argv missing %q; calls:\n%s", want, strings.Join(calls, "\n"))
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
