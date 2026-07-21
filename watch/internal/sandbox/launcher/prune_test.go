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

	if err := e.Prune(false, false); err != nil {
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
	t.Setenv("FAKE_PS", "claude-cenci-old\ncodex-cenci-stale\nopencode-cenci-old\nunrelated-container\n")

	if err := e.Prune(false, false); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLine(calls, "rm claude-cenci-old") || !containsLine(calls, "rm codex-cenci-stale") || !containsLine(calls, "rm opencode-cenci-old") {
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
			t.Setenv("FAKE_VOLUMES", "claude-cenci-home-old\ncodex-cenci-home-stale\nopencode-cenci-home-old\ncenci-agent-cli-claude\ncenci-agent-cli-opencode\nunrelated-volume\n")

			if err := e.Prune(false, true); err != nil {
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
	t.Setenv("FAKE_VOLUMES", "claude-cenci-home-old\ncodex-cenci-home-stale\nopencode-cenci-home-old\ncenci-agent-cli-codex\ncenci-agent-cli-opencode\nunrelated-volume\n")

	if err := e.Prune(false, true); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLine(calls, "volume rm claude-cenci-home-old codex-cenci-home-stale opencode-cenci-home-old cenci-agent-cli-codex cenci-agent-cli-opencode") {
		t.Errorf("expected one volume rm with all matching names including opencode; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestPrune_VolumesRejectsForeignLookAlikes pins the narrow match-miss
// boundary for the volume matchers: names that merely resemble an
// opencode-owned volume (wrong middle segment, extra trailing segment, or no
// "-home-"/"-agent-cli-" marker at all) must never appear in the batch
// volume rm call.
func TestPrune_VolumesRejectsForeignLookAlikes(t *testing.T) {
	e, callLog, _, _ := pruneEngine(t, "y\n")
	t.Setenv("FAKE_VOLUMES", "opencode-cenci-home-old\nopencode-notcenci-home-x\ncenci-agent-cli-opencode-foo\nopencode-elsewhere\n")

	if err := e.Prune(false, true); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLine(calls, "volume rm opencode-cenci-home-old") {
		t.Errorf("expected the genuine opencode home volume to be removed; calls:\n%s", strings.Join(calls, "\n"))
	}
	for _, foreign := range []string{"opencode-notcenci-home-x", "cenci-agent-cli-opencode-foo", "opencode-elsewhere"} {
		if containsLineWithAll(calls, foreign) {
			t.Errorf("foreign look-alike volume %q removed; calls:\n%s", foreign, strings.Join(calls, "\n"))
		}
	}
}

// TestPrune_VolumesClassifiesDindVolumesAsNonCredential pins the new dind
// storage volume bucket (#585): *-cenci-dind-* volumes carry no credentials
// (they hold a nested Docker's image/container storage, not the home
// volume's session history), so prune classifies them alongside the
// non-credential agent-CLI volumes rather than the credential-bearing home
// volumes, and removes them in the same single batch call.
func TestPrune_VolumesClassifiesDindVolumesAsNonCredential(t *testing.T) {
	e, callLog, _, stderr := pruneEngine(t, "y\n")
	t.Setenv("FAKE_VOLUMES", "claude-cenci-home-old\nclaude-cenci-dind-myrepo\ncodex-cenci-dind-otherrepo\nunrelated-volume\n")

	if err := e.Prune(false, true); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLineWithAll(calls, "volume rm", "claude-cenci-home-old", "claude-cenci-dind-myrepo", "codex-cenci-dind-otherrepo") {
		t.Errorf("expected dind volumes removed alongside other stale volumes in one batch call; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !strings.Contains(stderr.String(), "claude-cenci-dind-myrepo") || !strings.Contains(stderr.String(), "codex-cenci-dind-otherrepo") {
		t.Errorf("expected dind volumes to be listed in the confirmation prompt; stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Credential-bearing home volumes") {
		t.Errorf("expected the credential-bearing heading to still be printed for the home volume; stderr:\n%s", stderr.String())
	}
}

func TestPrune_NoMatchingVolumes(t *testing.T) {
	e, callLog, stdout, _ := pruneEngine(t, "y\n")
	t.Setenv("FAKE_VOLUMES", "unrelated-volume\n")

	if err := e.Prune(false, true); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if !strings.Contains(stdout.String(), "No sandbox volumes found.") {
		t.Errorf("missing no-volumes message; stdout:\n%s", stdout.String())
	}
	if containsPrefix(readCallLog(t, callLog), "volume rm") {
		t.Error("volume rm issued with no matching volumes")
	}
}

// fakeRepoImages is the shared FAKE_IMAGES fixture for the --images tests: a
// per-repo derived image, the monolith, the current base :latest alias, and
// the current base hash tag (matching pruneEngine's BaseTag, so the
// unrelated superseded-base-tag cleanup at the top of Prune leaves it
// alone) — none of the latter three should ever be treated as an --images
// candidate.
const fakeRepoImages = "cenci-sandbox-myrepo:latest\ncenci-sandbox:latest\ncenci-sandbox-base:latest\ncenci-sandbox-base:abc123def456\n"

func TestPrune_ImagesDefaultDeny(t *testing.T) {
	for name, stdin := range map[string]string{"explicit-n": "n\n", "empty-stdin": ""} {
		t.Run(name, func(t *testing.T) {
			e, callLog, stdout, stderr := pruneEngine(t, stdin)
			t.Setenv("FAKE_IMAGES", fakeRepoImages)

			if err := e.Prune(true, false); err != nil {
				t.Fatalf("Prune: %v", err)
			}

			calls := readCallLog(t, callLog)
			if containsLine(calls, "rmi cenci-sandbox-myrepo:latest") {
				t.Errorf("image removed without confirmation; calls:\n%s", strings.Join(calls, "\n"))
			}
			if !strings.Contains(stdout.String(), "Skipping image removal.") {
				t.Errorf("missing skip message; stdout:\n%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "Remove these images? [y/N]") {
				t.Errorf("missing prompt on stderr; stderr:\n%s", stderr.String())
			}
		})
	}
}

func TestPrune_ImagesConfirmedRemoves(t *testing.T) {
	e, callLog, _, _ := pruneEngine(t, "y\n")
	t.Setenv("FAKE_IMAGES", fakeRepoImages)

	if err := e.Prune(true, false); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLine(calls, "rmi cenci-sandbox-myrepo:latest") {
		t.Errorf("per-repo image not removed; calls:\n%s", strings.Join(calls, "\n"))
	}
	if containsLine(calls, "rmi cenci-sandbox:latest") {
		t.Errorf("monolith image removed; calls:\n%s", strings.Join(calls, "\n"))
	}
	if containsLine(calls, "rmi cenci-sandbox-base:latest") || containsLine(calls, "rmi cenci-sandbox-base:abc123def456") {
		t.Errorf("base image removed; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestPrune_ImagesExcludeMonolithAndBase(t *testing.T) {
	// Even with confirmation, the monolith and base tags must never appear
	// in the candidate listing shown to the user — assert on the listing
	// itself, not just the resulting rmi calls, so a regression in the
	// filtering logic (as opposed to the confirm gate) is caught directly.
	e, _, _, stderr := pruneEngine(t, "n\n")
	t.Setenv("FAKE_IMAGES", fakeRepoImages)

	if err := e.Prune(true, false); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	listedLines := strings.Split(stderr.String(), "\n")
	if !containsLine(listedLines, "cenci-sandbox-myrepo:latest") {
		t.Errorf("expected per-repo image in candidate listing; stderr:\n%s", stderr.String())
	}
	if containsLine(listedLines, "cenci-sandbox:latest") {
		t.Errorf("monolith image listed as a candidate; stderr:\n%s", stderr.String())
	}
	if containsLine(listedLines, "cenci-sandbox-base:latest") || containsLine(listedLines, "cenci-sandbox-base:abc123def456") {
		t.Errorf("base image listed as a candidate; stderr:\n%s", stderr.String())
	}
}

func TestPrune_NoMatchingImages(t *testing.T) {
	e, callLog, stdout, stderr := pruneEngine(t, "y\n")
	t.Setenv("FAKE_IMAGES", "cenci-sandbox:latest\ncenci-sandbox-base:latest\ncenci-sandbox-base:abc123def456\n")

	if err := e.Prune(true, false); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if !strings.Contains(stdout.String(), "No sandbox images found.") {
		t.Errorf("missing no-images message; stdout:\n%s", stdout.String())
	}
	if containsPrefix(readCallLog(t, callLog), "rmi") {
		t.Error("rmi issued with no matching images")
	}
	if strings.Contains(stderr.String(), "Remove these images? [y/N]") {
		t.Error("prompt shown with no matching images")
	}
}

func TestPrune_ImagesAndVolumesTwoPrompts(t *testing.T) {
	// Regression test for the shared-bufio.Reader fix: two independent
	// bufio.NewReader(e.Stdin) instances would let the first reader's
	// internal buffer swallow the byte meant for the second prompt, so
	// only the first "y\n" would ever take effect.
	e, callLog, _, _ := pruneEngine(t, "y\ny\n")
	t.Setenv("FAKE_IMAGES", fakeRepoImages)
	t.Setenv("FAKE_VOLUMES", "claude-cenci-home-old\nopencode-cenci-home-old\ncenci-agent-cli-claude\ncenci-agent-cli-opencode\nunrelated-volume\n")

	if err := e.Prune(true, true); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLine(calls, "rmi cenci-sandbox-myrepo:latest") {
		t.Errorf("per-repo image not removed; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !containsLine(calls, "volume rm claude-cenci-home-old opencode-cenci-home-old cenci-agent-cli-claude cenci-agent-cli-opencode") {
		t.Errorf("volumes including opencode not removed; calls:\n%s", strings.Join(calls, "\n"))
	}
}
