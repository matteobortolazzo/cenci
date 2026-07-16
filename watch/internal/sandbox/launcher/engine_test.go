package launcher

import (
	"bytes"
	"fmt"
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

// TestUpdateAgent_NeverAcceptsAnImageParameter pins the finding-1 fix at the
// type level: UpdateAgent no longer takes an image argument at all, so a
// caller literally cannot thread a scope's (possibly malicious, per-repo)
// image into the updater — it always targets MonolithImage.
func TestUpdateAgent_NeverAcceptsAnImageParameter(t *testing.T) {
	e, callLog := buildEngine(t, false)

	if err := e.UpdateAgent("claude", ""); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	calls := readCallLog(t, callLog)
	want := "run --rm --user root --cap-drop=ALL --security-opt=no-new-privileges --entrypoint /bin/bash -v cenci-agent-cli-claude:/opt/cenci-agent " + MonolithImage + " /usr/local/bin/lib/agent-cli.sh update claude"
	if !containsLine(calls, want) {
		t.Errorf("UpdateAgent argv missing %q; calls:\n%s", want, strings.Join(calls, "\n"))
	}
}

// volumeCheckEngine wires an Engine to a fake docker that always reports the
// current monolith image as present/current (so image builds never
// interfere) and the given agent's shared volume as already existing, with
// controllable exit codes for the populated-check run, the updater run, and
// `volume rm` — so tests can drive EnsureAgentVolume's fallback and warning
// paths (finding 3) without a real container runtime.
func volumeCheckEngine(t *testing.T, checkExit, updateExit, rmExit int) (e *Engine, callLog string, stderr *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	callLogPath := filepath.Join(dir, "calls.txt")
	body := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = images ]; then
  printf 'cenci-sandbox:latest\n'
  exit 0
fi
if [ "$1" = image ] && [ "$2" = inspect ]; then
  printf '%%s\n' shared-v2
  exit 0
fi
if [ "$1" = volume ] && [ "$2" = ls ]; then
  printf 'cenci-agent-cli-claude\n'
  exit 0
fi
if [ "$1" = volume ] && [ "$2" = rm ]; then
  exit %d
fi
if [ "$1" = run ]; then
  case "$*" in
  *'test -x /opt/cenci-agent'*) exit %d ;;
  *'agent-cli.sh update'*) exit %d ;;
  esac
  exit 0
fi
exit 0
`, exectest.ShellQuote(callLogPath), rmExit, checkExit, updateExit)
	exectest.WriteExecutable(t, filepath.Join(dir, "docker"), body)
	t.Setenv("PATH", dir)

	var out, errOut bytes.Buffer
	return &Engine{
		Runtime:  "docker",
		AssetDir: "/assets",
		BaseTag:  "abc123def456",
		Stdin:    strings.NewReader(""),
		Stdout:   &out,
		Stderr:   &errOut,
	}, callLogPath, &errOut
}

// TestEnsureAgentVolume_ExistingButUnpopulated_FallsThroughToUpdate pins
// finding 3a: volumeExists alone is not trusted for an existing volume;
// EnsureAgentVolume must cheaply verify it is populated and fall through to
// UpdateAgent when it isn't.
func TestEnsureAgentVolume_ExistingButUnpopulated_FallsThroughToUpdate(t *testing.T) {
	e, callLog, _ := volumeCheckEngine(t, 1, 0, 0) // check fails, update succeeds

	if err := e.EnsureAgentVolume("claude"); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLineWithAll(calls, "agent-cli.sh", "update", "claude") {
		t.Errorf("expected an unpopulated existing volume to fall through to the updater; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_Populated_SkipsUpdate is the companion positive case:
// a populated-check that succeeds must never trigger the updater.
func TestEnsureAgentVolume_Populated_SkipsUpdate(t *testing.T) {
	e, callLog, _ := volumeCheckEngine(t, 0, 0, 0) // check succeeds

	if err := e.EnsureAgentVolume("claude"); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	calls := readCallLog(t, callLog)
	if containsLineWithAll(calls, "agent-cli.sh", "update") {
		t.Errorf("expected a populated existing volume to skip the updater; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_UpdateFailureAndVolumeRmFailure_WarnsOperator pins
// finding 3b: when the compensating `volume rm` after a failed update itself
// fails, the operator must be warned (not left with a silently broken,
// host-global volume trusted forever) with the volume name and the manual
// removal command.
func TestEnsureAgentVolume_UpdateFailureAndVolumeRmFailure_WarnsOperator(t *testing.T) {
	e, _, stderr := volumeCheckEngine(t, 1, 1, 1) // check fails, update fails, rm fails

	if err := e.EnsureAgentVolume("claude"); err == nil {
		t.Fatal("expected EnsureAgentVolume to return the update failure")
	}

	warning := stderr.String()
	if !strings.Contains(warning, "cenci-agent-cli-claude") {
		t.Errorf("expected the warning to name the broken volume, got:\n%s", warning)
	}
	if !strings.Contains(warning, "volume rm") {
		t.Errorf("expected the warning to include the manual removal command, got:\n%s", warning)
	}
}

// TestEnsureAgentVolume_UpdateFailureButRmSucceeds_NoWarning is the
// companion negative case: when the compensating removal itself succeeds,
// there's nothing left broken on the host to warn the operator about.
func TestEnsureAgentVolume_UpdateFailureButRmSucceeds_NoWarning(t *testing.T) {
	e, _, stderr := volumeCheckEngine(t, 1, 1, 0) // check fails, update fails, rm succeeds

	if err := e.EnsureAgentVolume("claude"); err == nil {
		t.Fatal("expected EnsureAgentVolume to return the update failure")
	}

	if strings.Contains(stderr.String(), "could not be removed") {
		t.Errorf("expected no broken-volume warning when removal succeeds, got:\n%s", stderr.String())
	}
}
