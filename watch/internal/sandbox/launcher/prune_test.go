package launcher

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// pruneEngine builds an Engine wired to a fake docker on PATH and buffer
// streams, returning the engine, its call-log path, and the output buffers.
func pruneEngine(t *testing.T, stdin string) (*Engine, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeFakeRuntime(t, dir, "docker", callLog)
	t.Setenv("PATH", dir)

	var stdout, stderr bytes.Buffer
	e := &Engine{
		Runtime:  "docker",
		AssetDir: t.TempDir(),
		BaseTag:  "abc123def456",
		Stdin:    strings.NewReader(stdin),
		Stdout:   &stdout,
		Stderr:   &stderr,
	}
	return e, callLog, &stdout, &stderr
}

func TestPrune_RemovesSupersededBaseTagsKeepsCurrentAndLatest(t *testing.T) {
	e, callLog, _, _ := pruneEngine(t, "")
	t.Setenv("FAKE_IMAGES",
		"cenci-sandbox-base:abc123def456\ncenci-sandbox-base:latest\ncenci-sandbox-base:0ldstale0000\n")

	if err := e.Prune(false); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLine(calls, "rmi cenci-sandbox-base:0ldstale0000") {
		t.Errorf("superseded tag not removed; calls:\n%s", strings.Join(calls, "\n"))
	}
	if containsLine(calls, "rmi cenci-sandbox-base:abc123def456") || containsLine(calls, "rmi cenci-sandbox-base:latest") {
		t.Errorf("current or latest tag removed; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !containsLine(calls, "image prune -f") {
		t.Errorf("dangling image prune missing; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestPrune_RemovesOnlySandboxContainers(t *testing.T) {
	e, callLog, _, _ := pruneEngine(t, "")
	t.Setenv("FAKE_PS", "claude-cenci-old\ncodex-cenci-stale\nunrelated-container\n")

	if err := e.Prune(false); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLine(calls, "rm claude-cenci-old") || !containsLine(calls, "rm codex-cenci-stale") {
		t.Errorf("stopped sandbox containers not removed; calls:\n%s", strings.Join(calls, "\n"))
	}
	if containsLine(calls, "rm unrelated-container") {
		t.Errorf("non-sandbox container removed; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestPrune_VolumesDefaultDeny(t *testing.T) {
	for name, stdin := range map[string]string{"explicit-n": "n\n", "empty-stdin": ""} {
		t.Run(name, func(t *testing.T) {
			e, callLog, stdout, stderr := pruneEngine(t, stdin)
			t.Setenv("FAKE_VOLUMES", "claude-cenci-home-old\ncodex-cenci-home-stale\ncenci-agent-cli-claude\nunrelated-volume\n")

			if err := e.Prune(true); err != nil {
				t.Fatalf("Prune: %v", err)
			}

			calls := readCallLog(t, callLog)
			if containsPrefix(calls, "volume rm") {
				t.Errorf("volumes removed without confirmation; calls:\n%s", strings.Join(calls, "\n"))
			}
			if !strings.Contains(stdout.String(), "Skipping volume removal.") {
				t.Errorf("missing skip message; stdout:\n%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "Remove these volumes? [y/N]") {
				t.Errorf("missing prompt on stderr; stderr:\n%s", stderr.String())
			}
			if !strings.Contains(stderr.String(), "Credential-bearing home volumes") || !strings.Contains(stderr.String(), "Shared agent CLI volumes") {
				t.Errorf("volume classes not distinguished; stderr:\n%s", stderr.String())
			}
		})
	}
}

func TestPrune_VolumesConfirmedRemovesAllInOneCall(t *testing.T) {
	e, callLog, _, _ := pruneEngine(t, "y\n")
	t.Setenv("FAKE_VOLUMES", "claude-cenci-home-old\ncodex-cenci-home-stale\ncenci-agent-cli-codex\nunrelated-volume\n")

	if err := e.Prune(true); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLine(calls, "volume rm claude-cenci-home-old codex-cenci-home-stale cenci-agent-cli-codex") {
		t.Errorf("expected one volume rm with all matching names; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestPrune_NoMatchingVolumes(t *testing.T) {
	e, callLog, stdout, _ := pruneEngine(t, "y\n")
	t.Setenv("FAKE_VOLUMES", "unrelated-volume\n")

	if err := e.Prune(true); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if !strings.Contains(stdout.String(), "No sandbox volumes found.") {
		t.Errorf("missing no-volumes message; stdout:\n%s", stdout.String())
	}
	if containsPrefix(readCallLog(t, callLog), "volume rm") {
		t.Error("volume rm issued with no matching volumes")
	}
}
