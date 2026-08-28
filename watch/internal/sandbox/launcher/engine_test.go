package launcher

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// buildEngine wires an Engine to a fake docker whose image listing is either
// empty or contains the current base and monolith image. The `image inspect`
// branch emits the combined agent-cli/base-version label format
// (`<agent-cli>|<base-version>`); tests can override either half via
// FAKE_IMAGE_AGENT_LIFECYCLE / FAKE_IMAGE_BASE_VERSION (t.Setenv) to drive the
// staleness gate without a real container runtime. The defaults
// (shared-v2/abc123def456) match the Engine's own BaseTag below, so an
// unmodified call reports "current". FAKE_IMAGE_BASE_VERSION is read with the
// `${VAR+x}` set-check (not `${VAR:-default}`) so a test can t.Setenv it to
// the empty string to simulate a legacy image whose base-version label was
// never baked at all, distinct from simply not setting it. FAKE_IMAGE_INSPECT_RAW,
// when set, replaces the whole `image inspect` line verbatim (no `|`
// separator enforced), to simulate unparsable inspect output.
//
// printStaleContainerNotice's three probes (ticket #947) are also scripted:
//
//	FAKE_IMAGE_ID       — the freshly built image's ID, answered by the
//	                      `image inspect --format '{{.Id}}'` probe. Checked
//	                      BEFORE the label-format branch above, keyed on the
//	                      distinctive `{{.Id}}` format-string token, so it
//	                      never intercepts the agent-cli/base-version probe.
//	                      Defaults to "sha256:fresh".
//	FAKE_IMAGE_ID_EXIT  — that probe's exit code (default 0); nonzero
//	                      simulates the image-ID probe itself failing.
//	FAKE_PS             — `ps --format {{.Names}}` stdout (running
//	                      containers only); consumed by
//	                      sandbox.RunningSandboxContainers. Defaults to
//	                      empty (no running containers).
//	FAKE_PS_EXIT        — the plain running-only `ps` invocation's exit code
//	                      (default 0); nonzero simulates the `ps` probe
//	                      itself failing. Never applied to `ps -a`, which
//	                      always exits 0.
//	FAKE_PS_ALL         — `ps -a ...` stdout; returned ONLY for a `ps -a`
//	                      invocation, never for the plain running-only `ps`
//	                      above, so a test can script a stopped container
//	                      here to prove the probe never widens to `-a`.
//	FAKE_CONTAINER_IMAGES — space-separated `name=<ref>|<id>` pairs, one per
//	                      container, answering the combined per-container
//	                      probe `inspect --format
//	                      '{{.Config.Image}}|{{.Image}}'` (keyed on the
//	                      distinctive `{{.Image}}` token). The fake looks up
//	                      the requested container's name and emits
//	                      `${pair#*=}` verbatim as the probe's stdout —
//	                      shell builtins only (`for`/`case`/`${var#...}`),
//	                      since PATH contains only this fake. A pair written
//	                      without a `|` (e.g. `name=garbage`) or as
//	                      `name=` naturally produces unparsable/empty probe
//	                      output with no extra knob. A container absent from
//	                      the list defaults to
//	                      "cenci-sandbox:latest|<FAKE_IMAGE_ID default>" —
//	                      the monolith tag at the fresh ID — so it is silent
//	                      either way (monolith rebuild: ref matches, ID
//	                      equal, not stale; per-repo rebuild: ref doesn't
//	                      match, skipped) and never produces a spurious
//	                      warning in an existing test.
//	FAKE_INSPECT_IMAGE_EXIT — the per-container combined probe's exit code
//	                      (default 0); nonzero simulates that probe failing
//	                      for whichever container was requested.
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
  case "$*" in
  *'{{.Id}}'*)
    printf '%s\n' "${FAKE_IMAGE_ID:-sha256:fresh}"
    exit "${FAKE_IMAGE_ID_EXIT:-0}"
    ;;
  esac
  if [ -n "${FAKE_IMAGE_INSPECT_RAW+x}" ]; then
    printf '%s\n' "$FAKE_IMAGE_INSPECT_RAW"
    exit 0
  fi
  if [ -n "${FAKE_IMAGE_BASE_VERSION+x}" ]; then
    base="$FAKE_IMAGE_BASE_VERSION"
  else
    base="abc123def456"
  fi
  printf '%s|%s\n' "${FAKE_IMAGE_AGENT_LIFECYCLE:-shared-v2}" "$base"
  exit 0
fi
if [ "$1" = ps ]; then
  if [ "$2" = "-a" ]; then
    printf '%s' "$FAKE_PS_ALL"
    exit 0
  fi
  printf '%s' "$FAKE_PS"
  exit "${FAKE_PS_EXIT:-0}"
fi
if [ "$1" = inspect ]; then
  case "$*" in
  *'{{.Image}}'*)
    name="$4"
    result=""
    found=0
    for pair in $FAKE_CONTAINER_IMAGES; do
      case "$pair" in
      "$name="*)
        result="${pair#*=}"
        found=1
        ;;
      esac
    done
    if [ "$found" = 0 ]; then
      result="cenci-sandbox:latest|${FAKE_IMAGE_ID:-sha256:fresh}"
    fi
    printf '%s\n' "$result"
    exit "${FAKE_INSPECT_IMAGE_EXIT:-0}"
    ;;
  esac
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
	wantMonolith := "build --build-arg BASE_VERSION=abc123def456 --label cenci.agent-cli=shared-v2 --label cenci.base-version=abc123def456 -t cenci-sandbox:latest -f /assets/Dockerfile /assets"
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
	wantRepo := "build --build-arg BASE_VERSION=abc123def456 --label cenci.agent-cli=shared-v2 --label cenci.base-version=abc123def456 -t cenci-sandbox-myrepo:latest -f /repo/.cenci/Dockerfile /repo/.cenci"
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

// baseDriftNoticeSubstring is the load-bearing fragment of the base-drift
// rebuild notice (per watch rule #446, tests assert this exact phrase rather
// than a bare non-empty-output check, so the drift and missing-image build
// paths stay distinguishable).
const baseDriftNoticeSubstring = "built against an older sandbox base"

// TestEnsureImage_RebuildsWhenBaseVersionStale pins the ticket-#493 fix: an
// image whose agent-cli label is current but whose baked cenci.base-version
// label no longer matches the engine's BaseTag must be treated as stale and
// rebuilt, even though the plain agent-cli check alone would call it current.
func TestEnsureImage_RebuildsWhenBaseVersionStale(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present
	t.Setenv("FAKE_IMAGE_BASE_VERSION", "OLDTAG")

	scope := Scope{Image: MonolithImage}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	if calls := readCallLog(t, callLog); !containsPrefix(calls, "build") {
		t.Errorf("expected a rebuild when the baked base-version label is stale; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureImage_SkipsWhenBaseVersionCurrent is the companion positive case:
// an image whose baked base-version label matches the engine's current
// BaseTag must not be rebuilt (no regression to the missing-image-only fast
// path).
func TestEnsureImage_SkipsWhenBaseVersionCurrent(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present
	t.Setenv("FAKE_IMAGE_BASE_VERSION", "abc123def456")

	scope := Scope{Image: MonolithImage}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	if calls := readCallLog(t, callLog); containsPrefix(calls, "build") {
		t.Errorf("expected no rebuild when the baked base-version label matches; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureMonolithImage_RebuildsWhenBaseVersionStale is the
// EnsureMonolithImage analogue of TestEnsureImage_RebuildsWhenBaseVersionStale
// — the ticket's "both monolith and per-repo images honor the check"
// acceptance criterion.
func TestEnsureMonolithImage_RebuildsWhenBaseVersionStale(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present
	t.Setenv("FAKE_IMAGE_BASE_VERSION", "OLDTAG")

	if err := e.EnsureMonolithImage(); err != nil {
		t.Fatalf("EnsureMonolithImage: %v", err)
	}

	if calls := readCallLog(t, callLog); !containsPrefix(calls, "build") {
		t.Errorf("expected the monolith to rebuild when its baked base-version label is stale; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureImage_BaseDriftRebuild_PrintsNotice pins the ticket's optional
// follow-up (included in scope per the plan's Q&A): a rebuild triggered
// specifically by base-version drift must print a one-line stdout notice so
// the user understands why the first launch after a base change is slower.
func TestEnsureImage_BaseDriftRebuild_PrintsNotice(t *testing.T) {
	e, _ := buildEngine(t, false) // image present, agent-cli current
	t.Setenv("FAKE_IMAGE_BASE_VERSION", "OLDTAG")

	scope := Scope{Image: MonolithImage}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, baseDriftNoticeSubstring) {
		t.Errorf("expected a base-drift rebuild notice containing %q, got:\n%s", baseDriftNoticeSubstring, out)
	}
}

// TestEnsureImage_MissingImageBuild_NoDriftNotice is the companion negative
// case: a plain missing-image build (the pre-existing fast path, no baked
// label to compare against at all) must never print the base-drift notice —
// only a provable version mismatch counts as "drift".
func TestEnsureImage_MissingImageBuild_NoDriftNotice(t *testing.T) {
	e, callLog := buildEngine(t, true) // image missing entirely

	scope := Scope{Image: MonolithImage}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	if calls := readCallLog(t, callLog); !containsPrefix(calls, "build") {
		t.Errorf("expected a build for the missing image; calls:\n%s", strings.Join(calls, "\n"))
	}
	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, baseDriftNoticeSubstring) {
		t.Errorf("expected no base-drift notice for a missing-image build, got:\n%s", out)
	}
}

// TestEnsureImage_MissingBaseVersionLabel_RebuildsWithoutDriftNotice covers a
// legacy image built before the cenci.base-version label existed at all:
// present, agent-cli-current image whose base-version label is absent (empty)
// rather than merely stale. The plan's Assumptions/Risks sections are
// explicit that an absent label can never prove drift — only a present but
// mismatched label can — so this must rebuild (current=false) without
// printing the base-drift notice, distinct from
// TestEnsureImage_MissingImageBuild_NoDriftNotice's "image missing entirely"
// case, which never reaches the inspect branch at all.
func TestEnsureImage_MissingBaseVersionLabel_RebuildsWithoutDriftNotice(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present, agent-cli current
	t.Setenv("FAKE_IMAGE_BASE_VERSION", "")

	scope := Scope{Image: MonolithImage}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	if calls := readCallLog(t, callLog); !containsPrefix(calls, "build") {
		t.Errorf("expected a rebuild when the base-version label is absent; calls:\n%s", strings.Join(calls, "\n"))
	}
	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, baseDriftNoticeSubstring) {
		t.Errorf("expected no base-drift notice when the base-version label is merely absent (not provably stale), got:\n%s", out)
	}
}

// TestImageCurrent_UnparsableInspectOutput_WarnsOnStderr pins the
// silent-failure fix: when `image inspect` returns output without the
// expected `<agent-cli>|<base-version>` separator (a template/runtime
// incompatibility), imageCurrent must not silently fall back to "not
// current, no error" — it must warn on Stderr so a persistent tooling bug
// doesn't look identical to a normal rebuild-needed case forever.
func TestImageCurrent_UnparsableInspectOutput_WarnsOnStderr(t *testing.T) {
	e, _ := buildEngine(t, false) // image present
	t.Setenv("FAKE_IMAGE_INSPECT_RAW", "shared-v2")

	current, baseDrift, err := e.imageCurrent(MonolithImage)
	if err != nil {
		t.Fatalf("imageCurrent: %v", err)
	}
	if current || baseDrift {
		t.Errorf("imageCurrent(unparsable output) = (%v, %v), want (false, false)", current, baseDrift)
	}

	out := e.Stderr.(*bytes.Buffer).String()
	if !strings.Contains(out, "unparsable") {
		t.Errorf("expected a Stderr warning about unparsable image inspect output, got:\n%s", out)
	}
}

// pluginHintCount returns how many times the plugin-refresh hint appears in
// the engine's captured output.
func pluginHintCount(e *Engine) int {
	return strings.Count(e.Stdout.(*bytes.Buffer).String(), "cenci sandbox update-plugins")
}

// The pipeline plugins live in home volumes, not image layers, so every
// user-invoked build must remind exactly once that existing sandboxes refresh
// via `cenci sandbox update-plugins` (#423).
func TestBuildBase_PrintsPluginRefreshHint(t *testing.T) {
	e, _ := buildEngine(t, true)

	if err := e.BuildBase(); err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	if n := pluginHintCount(e); n != 1 {
		t.Errorf("plugin refresh hint printed %d times, want 1; output:\n%s", n, e.Stdout.(*bytes.Buffer).String())
	}
}

func TestBuildMonolith_PrintsPluginRefreshHintOnce(t *testing.T) {
	e, _ := buildEngine(t, true) // base missing → built implicitly first

	if err := e.BuildMonolith(); err != nil {
		t.Fatalf("BuildMonolith: %v", err)
	}

	if n := pluginHintCount(e); n != 1 {
		t.Errorf("plugin refresh hint printed %d times, want 1; output:\n%s", n, e.Stdout.(*bytes.Buffer).String())
	}
}

func TestBuildRepoImage_PrintsPluginRefreshHintOnce(t *testing.T) {
	e, _ := buildEngine(t, true) // base missing → built implicitly first

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	if n := pluginHintCount(e); n != 1 {
		t.Errorf("plugin refresh hint printed %d times, want 1; output:\n%s", n, e.Stdout.(*bytes.Buffer).String())
	}
}

// -- printStaleContainerNotice (ticket #947) --------------------------------
//
// staleContainerNoticeSubstring is the load-bearing fragment of the
// post-rebuild stale-container notice (watch rule #446: assert exact phrase,
// not a bare non-empty-output check).
const staleContainerNoticeSubstring = "still use the previous image"

// TestBuildRepoImage_RunningContainerOnOldImage_PrintsStaleNotice pins AC #1:
// a running sandbox container created from the just-rebuilt image reference,
// whose create-time image ID differs from the freshly built image's ID,
// produces a one-line stdout notice naming it.
func TestBuildRepoImage_RunningContainerOnOldImage_PrintsStaleNotice(t *testing.T) {
	e, _ := buildEngine(t, false)
	t.Setenv("FAKE_PS", "claude-cenci-myrepo\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-myrepo=cenci-sandbox-myrepo:latest|sha256:old")

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected a stale-container notice containing %q, got:\n%s", staleContainerNoticeSubstring, out)
	}
	if !strings.Contains(out, "claude-cenci-myrepo") {
		t.Errorf("expected the notice to name claude-cenci-myrepo, got:\n%s", out)
	}
	if !strings.Contains(out, "cenci sandbox stop") {
		t.Errorf("expected the notice to include the `cenci sandbox stop <name>` remedy, got:\n%s", out)
	}
}

// TestBuildRepoImage_RunningContainerOnFreshImage_NoStaleNotice pins AC #3: a
// running container already on the freshly built image ID (cache-hit / no-op
// rebuild) produces no notice.
func TestBuildRepoImage_RunningContainerOnFreshImage_NoStaleNotice(t *testing.T) {
	e, _ := buildEngine(t, false)
	t.Setenv("FAKE_PS", "claude-cenci-myrepo\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-myrepo=cenci-sandbox-myrepo:latest|sha256:fresh")

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected no stale-container notice when the running container is already on the fresh image, got:\n%s", out)
	}
}

// TestBuildMonolith_RunningContainerOnOldImage_PrintsStaleNotice pins AC #2:
// the same notice fires for a monolith rebuild, naming containers still on
// cenci-sandbox:latest's superseded image ID.
func TestBuildMonolith_RunningContainerOnOldImage_PrintsStaleNotice(t *testing.T) {
	e, _ := buildEngine(t, true) // base missing -> built implicitly first
	t.Setenv("FAKE_PS", "codex-cenci-agentstack\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "codex-cenci-agentstack=cenci-sandbox:latest|sha256:old")

	if err := e.BuildMonolith(); err != nil {
		t.Fatalf("BuildMonolith: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected a stale-container notice containing %q, got:\n%s", staleContainerNoticeSubstring, out)
	}
	if !strings.Contains(out, "codex-cenci-agentstack") {
		t.Errorf("expected the notice to name codex-cenci-agentstack, got:\n%s", out)
	}
}

// TestBuildRepoImage_StoppedContainerOnOldImage_NoStaleNotice pins AC #4: a
// stopped container on the old image produces no notice, because the probe
// uses running-only `ps` (never `ps -a`) — the stopped container is scripted
// into FAKE_PS_ALL, which the fake only answers for a `ps -a` invocation, and
// is deliberately left out of FAKE_PS.
func TestBuildRepoImage_StoppedContainerOnOldImage_NoStaleNotice(t *testing.T) {
	e, callLog := buildEngine(t, false)
	t.Setenv("FAKE_PS_ALL", "claude-cenci-stopped\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-stopped=cenci-sandbox-myrepo:latest|sha256:old")

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected no stale-container notice for a stopped container, got:\n%s", out)
	}

	calls := readCallLog(t, callLog)
	if containsPrefix(calls, "ps -a") {
		t.Errorf("expected the probe to never run `ps -a`; calls:\n%s", strings.Join(calls, "\n"))
	}
	if containsLineWithAll(calls, "{{.Image}}", "claude-cenci-stopped") {
		t.Errorf("expected no per-container inspect for a container the running-only ps never listed; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestBuildRepoImage_NonSandboxContainerOnOldImage_NoStaleNotice pins AC #5:
// a running container whose name fails sandbox.IsSandboxContainerName is
// never named — RunningSandboxContainers filters it out before the
// per-container probe ever runs.
func TestBuildRepoImage_NonSandboxContainerOnOldImage_NoStaleNotice(t *testing.T) {
	e, callLog := buildEngine(t, false)
	t.Setenv("FAKE_PS", "some-other-container\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "some-other-container=cenci-sandbox-myrepo:latest|sha256:old")

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected no stale-container notice for a non-sandbox container name, got:\n%s", out)
	}

	calls := readCallLog(t, callLog)
	if containsLineWithAll(calls, "{{.Image}}", "some-other-container") {
		t.Errorf("expected no per-container inspect for a name that fails IsSandboxContainerName; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestBuildRepoImage_MultipleStaleContainers_OneLineSorted pins AC #6:
// multiple stale containers are reported on a single line, names sorted
// lexicographically regardless of `ps`'s reported order.
func TestBuildRepoImage_MultipleStaleContainers_OneLineSorted(t *testing.T) {
	e, _ := buildEngine(t, false)
	t.Setenv("FAKE_PS", "codex-cenci-myrepo\nclaude-cenci-myrepo\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "codex-cenci-myrepo=cenci-sandbox-myrepo:latest|sha256:old claude-cenci-myrepo=cenci-sandbox-myrepo:latest|sha256:old2")

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if n := strings.Count(out, staleContainerNoticeSubstring); n != 1 {
		t.Errorf("expected exactly one stale-container notice line, got %d; output:\n%s", n, out)
	}
	if !strings.Contains(out, "claude-cenci-myrepo, codex-cenci-myrepo") {
		t.Errorf("expected the notice to name both containers, sorted lexicographically (claude before codex), got:\n%s", out)
	}
}

// TestBuildRepoImage_NoRunningContainers_PrintsNothing pins AC #7: with no
// running containers at all, no stale-container notice is printed.
func TestBuildRepoImage_NoRunningContainers_PrintsNothing(t *testing.T) {
	e, _ := buildEngine(t, false)

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected no stale-container notice when nothing is running, got:\n%s", out)
	}
}

// TestBuildBase_NeverProbesContainers pins that BuildBase is excluded by
// construction (ticket Decision): no `ps` and no `inspect` call of any shape
// at all — BuildBase never even reaches imageCurrent, since it always runs
// unconditionally.
func TestBuildBase_NeverProbesContainers(t *testing.T) {
	e, callLog := buildEngine(t, true)

	if err := e.BuildBase(); err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	calls := readCallLog(t, callLog)
	for _, c := range calls {
		if strings.HasPrefix(c, "ps") {
			t.Errorf("BuildBase must never call ps; calls:\n%s", strings.Join(calls, "\n"))
		}
		if strings.Contains(c, "inspect") {
			t.Errorf("BuildBase must never call inspect; calls:\n%s", strings.Join(calls, "\n"))
		}
	}
}

// TestBuildRepoImage_ContainerProbeFails_WarnsOnStderrAndBuildSucceeds pins
// AC #9/#10: a failing ps / image inspect / per-container inspect, or
// unparsable per-container output, prints a warning to stderr naming the
// failing probe and the build still returns nil (exit status stays 0).
func TestBuildRepoImage_ContainerProbeFails_WarnsOnStderrAndBuildSucceeds(t *testing.T) {
	t.Run("exit_nonzero", func(t *testing.T) {
		e, _ := buildEngine(t, false)
		var errOut bytes.Buffer
		e.Stderr = &errOut
		t.Setenv("FAKE_PS", "claude-cenci-myrepo\n")
		t.Setenv("FAKE_INSPECT_IMAGE_EXIT", "1")

		if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
			t.Fatalf("BuildRepoImage: %v", err)
		}

		warning := errOut.String()
		if !strings.Contains(warning, "claude-cenci-myrepo") {
			t.Errorf("expected the warning to name the failing container, got:\n%s", warning)
		}
		if !strings.Contains(warning, "inspect") {
			t.Errorf("expected the warning to name the failing probe (inspect), got:\n%s", warning)
		}
	})

	t.Run("unparsable_single_field", func(t *testing.T) {
		e, _ := buildEngine(t, false)
		var errOut bytes.Buffer
		e.Stderr = &errOut
		t.Setenv("FAKE_PS", "claude-cenci-myrepo\n")
		t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-myrepo=garbage")

		if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
			t.Fatalf("BuildRepoImage: %v", err)
		}

		warning := errOut.String()
		if !strings.Contains(warning, "claude-cenci-myrepo") {
			t.Errorf("expected the warning to name the failing container, got:\n%s", warning)
		}
		if !strings.Contains(warning, "unparsable") {
			t.Errorf("expected the warning to describe the output as unparsable, got:\n%s", warning)
		}
	})

	t.Run("unparsable_empty", func(t *testing.T) {
		e, _ := buildEngine(t, false)
		var errOut bytes.Buffer
		e.Stderr = &errOut
		t.Setenv("FAKE_PS", "claude-cenci-myrepo\n")
		t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-myrepo=")

		if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
			t.Fatalf("BuildRepoImage: %v", err)
		}

		warning := errOut.String()
		if !strings.Contains(warning, "claude-cenci-myrepo") {
			t.Errorf("expected the warning to name the failing container, got:\n%s", warning)
		}
		if !strings.Contains(warning, "unparsable") {
			t.Errorf("expected the warning to describe the output as unparsable, got:\n%s", warning)
		}
	})

	t.Run("image_id_probe_fails", func(t *testing.T) {
		e, _ := buildEngine(t, false)
		var errOut bytes.Buffer
		e.Stderr = &errOut
		t.Setenv("FAKE_PS", "claude-cenci-myrepo\n")
		t.Setenv("FAKE_IMAGE_ID_EXIT", "1")

		if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
			t.Fatalf("BuildRepoImage: %v", err)
		}

		warning := errOut.String()
		if !strings.Contains(warning, "cenci-sandbox-myrepo:latest") {
			t.Errorf("expected the warning to name the freshly built image, got:\n%s", warning)
		}
		if !strings.Contains(warning, "image inspect") {
			t.Errorf("expected the warning to name the failing probe (image inspect), got:\n%s", warning)
		}
	})

	t.Run("ps_fails", func(t *testing.T) {
		e, _ := buildEngine(t, false)
		var errOut bytes.Buffer
		e.Stderr = &errOut
		t.Setenv("FAKE_PS_EXIT", "1")

		if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
			t.Fatalf("BuildRepoImage: %v", err)
		}

		warning := errOut.String()
		if !strings.Contains(warning, "docker ps") {
			t.Errorf("expected the warning to name the failing probe (docker ps), got:\n%s", warning)
		}
	})
}

// TestEnsureImage_RebuildWithRunningContainer_PrintsStaleNotice pins AC #11:
// the `cenci open` implicit rebuild through EnsureImage -> BuildSelected
// emits the notice when it attaches to a container on the superseded image.
func TestEnsureImage_RebuildWithRunningContainer_PrintsStaleNotice(t *testing.T) {
	e, _ := buildEngine(t, false) // image present, but forced stale below
	t.Setenv("FAKE_IMAGE_BASE_VERSION", "OLDTAG")
	t.Setenv("FAKE_PS", "claude-cenci-agentstack\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-agentstack=cenci-sandbox:latest|sha256:old")

	scope := Scope{Image: MonolithImage}
	if err := e.EnsureImage(scope); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected the implicit-rebuild path to print a stale-container notice, got:\n%s", out)
	}
	if !strings.Contains(out, "claude-cenci-agentstack") {
		t.Errorf("expected the notice to name claude-cenci-agentstack, got:\n%s", out)
	}
}

// TestCheckSelected_NeverProbesContainers pins AC #12: `--check` never
// builds and never probes containers. It legitimately still runs the
// label-format `image inspect` (imageCurrent's freshness read), so this
// asserts absence of `ps`, of the combined per-container inspect
// (`{{.Config.Image}}|{{.Image}}`), and of the image-ID inspect (`{{.Id}}`)
// specifically — not absence of `image inspect` outright.
func TestCheckSelected_NeverProbesContainers(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present, current

	scope := Scope{Image: MonolithImage}
	current, err := e.CheckSelected(scope)
	if err != nil {
		t.Fatalf("CheckSelected: %v", err)
	}
	if !current {
		t.Errorf("CheckSelected(current image) current = false, want true")
	}

	calls := readCallLog(t, callLog)
	for _, c := range calls {
		if strings.HasPrefix(c, "ps") {
			t.Errorf("CheckSelected must never call ps; calls:\n%s", strings.Join(calls, "\n"))
		}
		if strings.Contains(c, "{{.Image}}") {
			t.Errorf("CheckSelected must never run the per-container inspect probe; calls:\n%s", strings.Join(calls, "\n"))
		}
		if strings.Contains(c, "{{.Id}}") {
			t.Errorf("CheckSelected must never run the image-ID inspect probe; calls:\n%s", strings.Join(calls, "\n"))
		}
	}
}

// TestBuildRepoImage_ContainerOnDifferentRepoImage_NoStaleNotice pins the
// decision (b) guard: a running container whose create-time image reference
// does NOT match the rebuilt tag produces no notice, even though its image
// ID differs — this is the cross-image false positive an ID-only comparison
// would have produced. A filtered-out container is a silent skip, never a
// probe failure, so stderr must stay empty too.
func TestBuildRepoImage_ContainerOnDifferentRepoImage_NoStaleNotice(t *testing.T) {
	e, _ := buildEngine(t, false)
	var errOut bytes.Buffer
	e.Stderr = &errOut
	t.Setenv("FAKE_PS", "claude-cenci-otherrepo\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-otherrepo=cenci-sandbox-otherrepo:latest|sha256:old")

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-myrepo:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected no stale-container notice for a container on an unrelated repo image, got:\n%s", out)
	}
	if errOut.String() != "" {
		t.Errorf("expected a filtered-out container to be a silent skip, not a probe-failure warning, got stderr:\n%s", errOut.String())
	}
}

// TestBuildMonolith_ContainerOnRepoImage_NoStaleNotice pins the same guard
// for a monolith rebuild (the update-agent / agent-volume-bootstrap blast
// radius via EnsureMonolithImage), and pins that "cenci-sandbox-velka:latest"
// does not suffix-match "cenci-sandbox:latest" (the leading "/" in
// sameImageRef's suffix check is load-bearing).
func TestBuildMonolith_ContainerOnRepoImage_NoStaleNotice(t *testing.T) {
	e, _ := buildEngine(t, true) // image missing entirely -> EnsureMonolithImage builds it
	t.Setenv("FAKE_PS", "claude-cenci-velka\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-velka=cenci-sandbox-velka:latest|sha256:old")

	if err := e.EnsureMonolithImage(); err != nil {
		t.Fatalf("EnsureMonolithImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected no stale-container notice for a per-repo-image container on a monolith rebuild, got:\n%s", out)
	}
}

// TestBuildRepoImage_PodmanPrefixedImageRef_PrintsStaleNotice guards the
// false-negative regression a strict-equality reference filter would
// introduce: podman normalizes local images with a "localhost/" prefix, so
// sameImageRef must still match "localhost/cenci-sandbox-velka:latest"
// against the rebuilt tag "cenci-sandbox-velka:latest".
func TestBuildRepoImage_PodmanPrefixedImageRef_PrintsStaleNotice(t *testing.T) {
	e, _ := buildEngine(t, false)
	t.Setenv("FAKE_PS", "claude-cenci-velka\n")
	t.Setenv("FAKE_CONTAINER_IMAGES", "claude-cenci-velka=localhost/cenci-sandbox-velka:latest|sha256:old")

	if err := e.BuildRepoImage("/repo", "cenci-sandbox-velka:latest"); err != nil {
		t.Fatalf("BuildRepoImage: %v", err)
	}

	out := e.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, staleContainerNoticeSubstring) {
		t.Errorf("expected a stale-container notice for a podman-prefixed matching reference, got:\n%s", out)
	}
	if !strings.Contains(out, "claude-cenci-velka") {
		t.Errorf("expected the notice to name claude-cenci-velka, got:\n%s", out)
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

// agentVolumeStatusLines builds the five key=value lines the real
// `agent-cli.sh status <agent>` probe prints (populated, version, pin,
// last_success, last_attempt — sandbox/lib/agent-cli.sh:444-448), always
// reporting populated=yes with a fixed version. lastSuccess/lastAttempt are
// resolved to unix-epoch-seconds strings at fixture-write time (the zero
// time.Time renders as the empty string, matching the shell contract's
// "unset fact" framing) — the plan's chosen alternative to a clock seam: the
// fake runtime fully controls staleness via relative Go-computed timestamps,
// never a production-only injection point.
func agentVolumeStatusLines(pin string, lastSuccess, lastAttempt time.Time) string {
	ls, la := "", ""
	if !lastSuccess.IsZero() {
		ls = fmt.Sprintf("%d", lastSuccess.Unix())
	}
	if !lastAttempt.IsZero() {
		la = fmt.Sprintf("%d", lastAttempt.Unix())
	}
	return fmt.Sprintf("populated=yes\nversion=1.2.3\npin=%s\nlast_success=%s\nlast_attempt=%s\n", pin, ls, la)
}

// volumeCheckEngineStatus wires an Engine to a fake docker that always
// reports the current monolith image as present/current — the `image
// inspect` branch emits the combined agent-cli/base-version label format
// (shared-v2|abc123def456, matching the Engine's own BaseTag below) so image
// builds never interfere — and the given agent's shared volume as already
// existing. The `agent-cli.sh status <agent>` probe (agentVolumeCheckRunArgs)
// prints statusLines verbatim and exits checkExit; `agent-cli.sh update`
// prints a fake container id (a real runtime's --detach stdout) and exits
// updateExit; `volume rm` exits rmExit — so tests can drive
// EnsureAgentVolume's populated-check, TTL/backoff/pin staleness branch, and
// warning paths without a real container runtime.
func volumeCheckEngineStatus(t *testing.T, statusLines string, checkExit, updateExit, rmExit int) (e *Engine, callLog string, stderr *bytes.Buffer) {
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
  printf '%%s\n' 'shared-v2|abc123def456'
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
  *'agent-cli.sh status'*) printf '%%s' %s; exit %d ;;
  *'agent-cli.sh update'*) printf 'fake-refresh-container-id\n'; exit %d ;;
  esac
  exit 0
fi
exit 0
`, exectest.ShellQuote(callLogPath), rmExit, exectest.ShellQuote(statusLines), checkExit, updateExit)
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

// volumeCheckEngine is volumeCheckEngineStatus's 3-int wrapper, kept so the
// four pre-#710 EnsureAgentVolume tests below keep their original meaning: it
// defaults the probe's status to populated=yes with a `last_success` fixed at
// "now" (fresh, well inside the default 24h TTL) and an empty pin/
// last_attempt, so those tests exercise only the populated-check branch, never
// the new TTL staleness branch.
func volumeCheckEngine(t *testing.T, checkExit, updateExit, rmExit int) (e *Engine, callLog string, stderr *bytes.Buffer) {
	t.Helper()
	statusLines := agentVolumeStatusLines("", time.Now(), time.Time{})
	return volumeCheckEngineStatus(t, statusLines, checkExit, updateExit, rmExit)
}

// TestEnsureAgentVolume_ExistingButUnpopulated_FallsThroughToUpdate pins
// finding 3a: volumeExists alone is not trusted for an existing volume;
// EnsureAgentVolume must cheaply verify it is populated and fall through to
// UpdateAgent when it isn't.
func TestEnsureAgentVolume_ExistingButUnpopulated_FallsThroughToUpdate(t *testing.T) {
	e, callLog, _ := volumeCheckEngine(t, 1, 0, 0) // check fails, update succeeds

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLineWithAll(calls, "agent-cli.sh", "update", "claude") {
		t.Errorf("expected an unpopulated existing volume to fall through to the updater; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_Bootstrap_UpdaterStaysForeground pins #745's
// deliberate scope boundary: only the staleness-refresh branch detaches.
// Bootstrap (missing/unpopulated volume) must keep running the updater in
// the foreground — there is nothing to launch with until the install
// finishes, and its failure path (error return + compensating volume rm)
// depends on observing the update's outcome.
func TestEnsureAgentVolume_Bootstrap_UpdaterStaysForeground(t *testing.T) {
	e, callLog, _ := volumeCheckEngine(t, 1, 0, 0) // check fails, update succeeds

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); containsLineWithAll(calls, "agent-cli.sh", "update", "--detach") {
		t.Errorf("expected the bootstrap updater to run in the foreground (no --detach); calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_Populated_SkipsUpdate is the companion positive case:
// a populated-check that succeeds must never trigger the updater.
func TestEnsureAgentVolume_Populated_SkipsUpdate(t *testing.T) {
	e, callLog, _ := volumeCheckEngine(t, 0, 0, 0) // check succeeds

	if err := e.EnsureAgentVolume("claude", false); err != nil {
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

	if err := e.EnsureAgentVolume("claude", false); err == nil {
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

	if err := e.EnsureAgentVolume("claude", false); err == nil {
		t.Fatal("expected EnsureAgentVolume to return the update failure")
	}

	if strings.Contains(stderr.String(), "could not be removed") {
		t.Errorf("expected no broken-volume warning when removal succeeds, got:\n%s", stderr.String())
	}
}

// -- agentVolumeCheckRunArgs (ticket #710) ----------------------------------

// TestAgentVolumeCheckRunArgs_InvokesAgentCLIStatusWithHardening pins the
// #708-era probe swap: the populated-check now runs `agent-cli.sh status
// <agent>` (mirroring agentUpdateRunArgs' --entrypoint /bin/bash shape)
// instead of `test -x <path>`, while keeping every hardening flag the old
// probe had (network-isolated, non-root, read-only mount, dropped
// capabilities, no-new-privileges) and always targeting MonolithImage.
func TestAgentVolumeCheckRunArgs_InvokesAgentCLIStatusWithHardening(t *testing.T) {
	e := &Engine{}
	got := e.agentVolumeCheckRunArgs("claude")
	want := []string{"run", "--rm", "--network", "none", "--user", "dev",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--entrypoint", "/bin/bash",
		"-v", "cenci-agent-cli-claude:/opt/cenci-agent:ro",
		MonolithImage, "/usr/local/bin/lib/agent-cli.sh", "status", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agentVolumeCheckRunArgs(\"claude\") = %#v, want %#v", got, want)
	}
}

// -- agentRefreshRunArgs (ticket #745) --------------------------------------

// TestAgentRefreshRunArgs_DetachedNamedWithHardening pins the launch-time
// background refresh invocation (#745): identical to agentUpdateRunArgs'
// hardening, image, and script invocation, but --detach (the launch must
// never wait on the npm download) and --name'd with the fixed
// cenci-agent-cli-refresh-<agent> so a concurrent duplicate start fails
// closed on the name conflict instead of stacking redundant updaters. Never
// a version argument — the background path only ever tracks latest.
func TestAgentRefreshRunArgs_DetachedNamedWithHardening(t *testing.T) {
	e := &Engine{}
	got := e.agentRefreshRunArgs("claude")
	want := []string{"run", "--rm", "--detach", "--name", "cenci-agent-cli-refresh-claude",
		"--user", "root", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--entrypoint", "/bin/bash",
		"-v", "cenci-agent-cli-claude:/opt/cenci-agent",
		MonolithImage, "/usr/local/bin/lib/agent-cli.sh", "update", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agentRefreshRunArgs(\"claude\") = %#v, want %#v", got, want)
	}
}

// -- parseAgentVolumeStatus (ticket #710) -----------------------------------
//
// parseAgentVolumeStatus parses agent-cli.sh status's five key=value lines
// (populated, version, pin, last_success, last_attempt). It is a
// default-deny parser (watch/docs/error-handling.md #628, parseReusePosture
// lesson): missing/absent "populated=yes|no" framing or any other
// unrecognized shape returns ok=false (treated as unpopulated -> bootstrap),
// never a permissive zero-value guess. A malformed/absent last_success or
// last_attempt on an otherwise well-formed "populated=yes" status parses to
// the zero time.Time (per the plan's resolved Q1: unknown -> stale,
// refresh-eligible), which is a distinct, narrower failure mode than the
// overall default-deny direction.

func TestParseAgentVolumeStatus_PopulatedYesFullFacts_Parses(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	attempt := now.Add(-10 * time.Minute)
	stdout := fmt.Sprintf("populated=yes\nversion=1.2.3\npin=1.0.0\nlast_success=%d\nlast_attempt=%d\n", now.Unix(), attempt.Unix())

	status, ok := parseAgentVolumeStatus([]byte(stdout))
	if !ok {
		t.Fatalf("parseAgentVolumeStatus(%q) ok = false, want true", stdout)
	}
	if !status.Populated {
		t.Error("expected Populated = true")
	}
	if status.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", status.Version)
	}
	if status.Pin != "1.0.0" {
		t.Errorf("Pin = %q, want 1.0.0", status.Pin)
	}
	if !status.LastSuccess.Equal(now) {
		t.Errorf("LastSuccess = %v, want %v", status.LastSuccess, now)
	}
	if !status.LastAttempt.Equal(attempt) {
		t.Errorf("LastAttempt = %v, want %v", status.LastAttempt, attempt)
	}
}

func TestParseAgentVolumeStatus_PopulatedNo_ReportsUnpopulated(t *testing.T) {
	stdout := "populated=no\nversion=\npin=\nlast_success=\nlast_attempt=\n"

	status, ok := parseAgentVolumeStatus([]byte(stdout))
	if !ok {
		t.Fatalf("parseAgentVolumeStatus(%q) ok = false, want true (well-formed, just unpopulated)", stdout)
	}
	if status.Populated {
		t.Error("expected Populated = false for populated=no")
	}
}

func TestParseAgentVolumeStatus_EmptyLastSuccessAndLastAttempt_ParseToZeroTime(t *testing.T) {
	stdout := "populated=yes\nversion=1.2.3\npin=\nlast_success=\nlast_attempt=\n"

	status, ok := parseAgentVolumeStatus([]byte(stdout))
	if !ok {
		t.Fatalf("parseAgentVolumeStatus(%q) ok = false, want true", stdout)
	}
	if !status.LastSuccess.IsZero() {
		t.Errorf("LastSuccess = %v, want the zero time for an empty fact", status.LastSuccess)
	}
	if !status.LastAttempt.IsZero() {
		t.Errorf("LastAttempt = %v, want the zero time for an empty fact", status.LastAttempt)
	}
}

func TestParseAgentVolumeStatus_UnparseableLastSuccess_ParsesToZeroTimeNotDefaultDeny(t *testing.T) {
	// Per the plan's resolved Q1: an unparseable/garbled last_success on an
	// otherwise well-formed status is "unknown -> stale", not a whole-status
	// default-deny rejection -- the overall parse still succeeds (ok=true),
	// only LastSuccess degrades to the zero time.
	stdout := "populated=yes\nversion=1.2.3\npin=\nlast_success=not-a-number\nlast_attempt=\n"

	status, ok := parseAgentVolumeStatus([]byte(stdout))
	if !ok {
		t.Fatalf("parseAgentVolumeStatus(%q) ok = false, want true (malformed last_success alone must not default-deny the whole status)", stdout)
	}
	if !status.LastSuccess.IsZero() {
		t.Errorf("LastSuccess = %v, want the zero time for an unparseable fact", status.LastSuccess)
	}
}

func TestParseAgentVolumeStatus_EmptyStdout_DefaultDeny(t *testing.T) {
	_, ok := parseAgentVolumeStatus([]byte(""))
	if ok {
		t.Error("parseAgentVolumeStatus(\"\") ok = true, want false (default-deny on empty stdout)")
	}
}

func TestParseAgentVolumeStatus_MissingPopulatedLine_DefaultDeny(t *testing.T) {
	stdout := "version=1.2.3\npin=\nlast_success=\nlast_attempt=\n"

	_, ok := parseAgentVolumeStatus([]byte(stdout))
	if ok {
		t.Errorf("parseAgentVolumeStatus(%q) ok = true, want false (default-deny: no populated= line)", stdout)
	}
}

func TestParseAgentVolumeStatus_TruncatedOutput_DefaultDeny(t *testing.T) {
	// Only two of the five contractually-fixed lines are present.
	stdout := "populated=yes\nversion=1.2.3\n"

	_, ok := parseAgentVolumeStatus([]byte(stdout))
	if ok {
		t.Errorf("parseAgentVolumeStatus(%q) ok = true, want false (default-deny: truncated framing)", stdout)
	}
}

func TestParseAgentVolumeStatus_UnrecognizedPopulatedValue_DefaultDeny(t *testing.T) {
	// "populated" is a closed yes/no enum; any other value is ambiguous
	// metadata and must be rejected conservatively
	// (watch/docs/go-gotchas.md #598), never silently collapsed into either
	// yes or no.
	stdout := "populated=maybe\nversion=\npin=\nlast_success=\nlast_attempt=\n"

	_, ok := parseAgentVolumeStatus([]byte(stdout))
	if ok {
		t.Errorf("parseAgentVolumeStatus(%q) ok = true, want false (default-deny: unrecognized populated= value)", stdout)
	}
}

func TestParseAgentVolumeStatus_GarbageStdout_DefaultDeny(t *testing.T) {
	stdout := "this is not key=value framing at all\n\x00\x01garbage"

	_, ok := parseAgentVolumeStatus([]byte(stdout))
	if ok {
		t.Errorf("parseAgentVolumeStatus(%q) ok = true, want false (default-deny on garbage stdout)", stdout)
	}
}

// -- agentVolumePopulated classification (ticket #710) ----------------------
//
// agentVolumePopulated's exit-code classification is unchanged by the #710
// probe swap: any *exec.ExitError from the probe still means "unpopulated"
// (never a hard error), a non-ExitError (runtime/transport failure) still
// stays a hard error, and exit 0 with unparseable stdout is newly, explicitly
// treated as unpopulated too (default-deny).

func TestAgentVolumePopulated_ProbeExitError_ReturnsUnpopulatedNoHardError(t *testing.T) {
	statusLines := agentVolumeStatusLines("", time.Now(), time.Time{})
	e, _, _ := volumeCheckEngineStatus(t, statusLines, 1, 0, 0) // probe exits 1 (unpopulated)

	_, populated, err := e.agentVolumePopulated("claude")
	if err != nil {
		t.Fatalf("expected a nonzero probe exit (*exec.ExitError) to classify as unpopulated, not a hard error: %v", err)
	}
	if populated {
		t.Error("populated = true, want false for a nonzero probe exit")
	}
}

func TestAgentVolumePopulated_ProbeExitZeroWithUnparsableStdout_DefaultDenyUnpopulated(t *testing.T) {
	e, _, _ := volumeCheckEngineStatus(t, "not-a-valid-status-line\n", 0, 0, 0)

	_, populated, err := e.agentVolumePopulated("claude")
	if err != nil {
		t.Fatalf("expected exit-0-but-unparsable probe stdout to default-deny (unpopulated), not a hard error: %v", err)
	}
	if populated {
		t.Error("populated = true, want false (default-deny) for unparsable exit-0 stdout")
	}
}

func TestAgentVolumePopulated_RuntimeUnreachable_ReturnsHardError(t *testing.T) {
	e := &Engine{
		Runtime: "cenci-test-runtime-does-not-exist-710",
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
	}

	_, populated, err := e.agentVolumePopulated("claude")
	if err == nil {
		t.Fatal("expected a hard error when the runtime binary itself cannot be executed, not a silent \"unpopulated\" classification")
	}
	if populated {
		t.Error("populated must be false alongside the hard error")
	}
}

func TestEnsureAgentVolume_ProbeExitZeroWithUnparsableStdout_DefaultDenyBootstraps(t *testing.T) {
	e, callLog, _ := volumeCheckEngineStatus(t, "not-a-valid-status-line\n", 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); !containsLineWithAll(calls, "agent-cli.sh", "update", "claude") {
		t.Errorf("expected exit-0-but-unparsable probe stdout to default-deny (treated as unpopulated) and fall through to bootstrap; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// -- agentCLITTL env parsing (ticket #710) -----------------------------------
//
// CENCI_SANDBOX_AGENT_CLI_TTL_HOURS overrides the 24h default TTL in integer
// hours. Unlike reap.go:79's silent ParseFloat fallback, an unparseable or
// negative value must warn to stderr (content-specific,
// watch/docs/error-handling.md #446) and fall back to 24h -- it must never
// silently disable auto-refresh.

func TestAgentCLITTL_Unset_DefaultsTo24HoursNoWarning(t *testing.T) {
	var stderr bytes.Buffer
	if got := agentCLITTL(&stderr); got != defaultAgentCLITTL {
		t.Errorf("agentCLITTL() = %v, want %v", got, defaultAgentCLITTL)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no warning when the env var is unset, got:\n%s", stderr.String())
	}
}

func TestAgentCLITTL_EmptyString_DefaultsTo24HoursNoWarning(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_AGENT_CLI_TTL_HOURS", "")
	var stderr bytes.Buffer
	if got := agentCLITTL(&stderr); got != defaultAgentCLITTL {
		t.Errorf("agentCLITTL() = %v, want %v", got, defaultAgentCLITTL)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no warning for an explicitly empty value, got:\n%s", stderr.String())
	}
}

func TestAgentCLITTL_Zero_DisablesWithNoWarning(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_AGENT_CLI_TTL_HOURS", "0")
	var stderr bytes.Buffer
	if got := agentCLITTL(&stderr); got != 0 {
		t.Errorf("agentCLITTL() = %v, want 0 (disabled)", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no warning for the documented 0=disabled value, got:\n%s", stderr.String())
	}
}

func TestAgentCLITTL_PositiveOverride_NoWarning(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_AGENT_CLI_TTL_HOURS", "6")
	var stderr bytes.Buffer
	if got := agentCLITTL(&stderr); got != 6*time.Hour {
		t.Errorf("agentCLITTL() = %v, want 6h", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no warning for a valid positive override, got:\n%s", stderr.String())
	}
}

func TestAgentCLITTL_Unparseable_WarnsAndFallsBackTo24Hours(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_AGENT_CLI_TTL_HOURS", "not-a-number")
	var stderr bytes.Buffer
	if got := agentCLITTL(&stderr); got != defaultAgentCLITTL {
		t.Errorf("agentCLITTL() = %v, want the 24h fallback", got)
	}
	if !strings.Contains(stderr.String(), "CENCI_SANDBOX_AGENT_CLI_TTL_HOURS") {
		t.Errorf("expected the warning to name CENCI_SANDBOX_AGENT_CLI_TTL_HOURS, got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not-a-number") {
		t.Errorf("expected the warning to include the offending value, got:\n%s", stderr.String())
	}
}

func TestAgentCLITTL_Negative_WarnsAndFallsBackTo24Hours(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_AGENT_CLI_TTL_HOURS", "-1")
	var stderr bytes.Buffer
	if got := agentCLITTL(&stderr); got != defaultAgentCLITTL {
		t.Errorf("agentCLITTL() = %v, want the 24h fallback", got)
	}
	if !strings.Contains(stderr.String(), "CENCI_SANDBOX_AGENT_CLI_TTL_HOURS") {
		t.Errorf("expected the warning to name CENCI_SANDBOX_AGENT_CLI_TTL_HOURS, got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "-1") {
		t.Errorf("expected the warning to include the offending value, got:\n%s", stderr.String())
	}
}

// TestAgentCLITTL_HugeValue_WarnsAndFallsBackTo24Hours pins security review
// finding B: time.Duration(hours) * time.Hour overflows silently for very
// large hours (e.g. wrapping to exactly 0 -- indistinguishable from the
// documented "0=disabled" -- or to a nonsense negative duration), which
// would violate the invariant that unparseable/negative input must never
// silently disable auto-refresh. A value beyond maxAgentCLITTLHours must warn
// and fall back to the 24h default via the same warning path as an
// unparseable/negative value, not silently disable or corrupt the TTL.
func TestAgentCLITTL_HugeValue_WarnsAndFallsBackTo24Hours(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_AGENT_CLI_TTL_HOURS", "999999999999999")
	var stderr bytes.Buffer
	if got := agentCLITTL(&stderr); got != defaultAgentCLITTL {
		t.Errorf("agentCLITTL() = %v, want the 24h fallback (not silently 0 or negative from overflow)", got)
	}
	if !strings.Contains(stderr.String(), "CENCI_SANDBOX_AGENT_CLI_TTL_HOURS") {
		t.Errorf("expected the warning to name CENCI_SANDBOX_AGENT_CLI_TTL_HOURS, got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "999999999999999") {
		t.Errorf("expected the warning to include the offending value, got:\n%s", stderr.String())
	}
}

// -- EnsureAgentVolume staleness policy (ticket #710) ------------------------

func TestEnsureAgentVolume_FreshWithinTTL_NoUpdaterRuns(t *testing.T) {
	statusLines := agentVolumeStatusLines("", time.Now(), time.Time{})
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); containsLineWithAll(calls, "agent-cli.sh", "update") {
		t.Errorf("expected a fresh (within-TTL) volume to skip the updater; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_StaleLastSuccess_StartsDetachedRefresh pins ticket
// #745's headline AC: a TTL-stale populated volume starts the updater
// detached (--detach, runtime-daemon-owned) under the fixed dedup name
// cenci-agent-cli-refresh-<agent>, so the launch never blocks on the
// (potentially very large — ~130MB for codex) npm download. The runtime's
// --detach stdout (the started container's id) must be swallowed, never
// echoed into the launch output, and the stdout notice must say the refresh
// happens in the background — not the foreground "Updating ... in shared
// volume" message, which stays reserved for the blocking bootstrap/explicit
// paths.
func TestEnsureAgentVolume_StaleLastSuccess_StartsDetachedRefresh(t *testing.T) {
	stale := time.Now().Add(-25 * time.Hour)
	statusLines := agentVolumeStatusLines("", stale, time.Time{})
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLineWithAll(calls, "agent-cli.sh", "update", "claude", "--detach", "--name cenci-agent-cli-refresh-claude") {
		t.Errorf("expected a TTL-stale volume to start a detached, named refresh via the updater; calls:\n%s", strings.Join(calls, "\n"))
	}
	out := e.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "background") {
		t.Errorf("expected the stale-refresh notice to say the refresh happens in the background, got:\n%s", out)
	}
	if strings.Contains(out, "Updating claude in shared volume") {
		t.Errorf("expected the stale-refresh path to not print the blocking foreground update message, got:\n%s", out)
	}
	if strings.Contains(out, "fake-refresh-container-id") {
		t.Errorf("expected the detached run's container-id stdout to be swallowed, not echoed into the launch output, got:\n%s", out)
	}
}

// TestEnsureAgentVolume_MissingLastSuccess_TreatedAsStaleAndRefreshes pins the
// plan's resolved Q1: a populated volume with an absent/unparseable
// last_success (e.g. created before #708 shipped stamp files) is unknown ->
// stale, triggering a one-time refresh.
func TestEnsureAgentVolume_MissingLastSuccess_TreatedAsStaleAndRefreshes(t *testing.T) {
	statusLines := agentVolumeStatusLines("", time.Time{}, time.Time{})
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); !containsLineWithAll(calls, "agent-cli.sh", "update", "claude") {
		t.Errorf("expected a populated volume with no recorded last_success to be treated as stale and refreshed; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_StaleRefreshFails_WarnsButStillReturnsNil pins the
// best-effort AC: a refresh that fails to start (since #745 the launch only
// observes the detached start, e.g. a name conflict with an
// already-running refresh, never the update's own outcome) warns and the
// launch proceeds on the existing (already-populated, just-stale) version
// -- it must NOT remove the volume the way the bootstrap-failure path does,
// since the volume already has a working install.
func TestEnsureAgentVolume_StaleRefreshFails_WarnsButStillReturnsNil(t *testing.T) {
	stale := time.Now().Add(-25 * time.Hour)
	statusLines := agentVolumeStatusLines("", stale, time.Time{})
	e, callLog, stderr := volumeCheckEngineStatus(t, statusLines, 0, 1, 0) // populated+stale, update fails

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("expected EnsureAgentVolume to be best-effort and return nil on a failed refresh, got: %v", err)
	}

	if !strings.Contains(stderr.String(), "claude") {
		t.Errorf("expected a stderr warning naming the agent for the failed best-effort refresh, got:\n%s", stderr.String())
	}
	if calls := readCallLog(t, callLog); containsPrefix(calls, "volume rm") {
		t.Errorf("a stale-refresh failure must not remove the already-populated volume; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_BackoffWithinLastAttempt_NoRefresh pins the 1h
// last_attempt throttle: an offline/captive-portal host must not eat the
// npm-timeout cost on every launch, only once per hour.
func TestEnsureAgentVolume_BackoffWithinLastAttempt_NoRefresh(t *testing.T) {
	stale := time.Now().Add(-25 * time.Hour)
	recentAttempt := time.Now().Add(-30 * time.Minute)
	statusLines := agentVolumeStatusLines("", stale, recentAttempt)
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); containsLineWithAll(calls, "agent-cli.sh", "update") {
		t.Errorf("expected a stale volume within the 1h last_attempt backoff to skip the refresh; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestEnsureAgentVolume_PinnedAndStale_PrintsNoticeNoRefresh(t *testing.T) {
	stale := time.Now().Add(-25 * time.Hour)
	statusLines := agentVolumeStatusLines("1.2.3", stale, time.Time{})
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); containsLineWithAll(calls, "agent-cli.sh", "update") {
		t.Errorf("expected a pinned, TTL-stale volume to skip the refresh; calls:\n%s", strings.Join(calls, "\n"))
	}
	out := e.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "1.2.3") {
		t.Errorf("expected the pinned-stale notice to name the pinned version, got:\n%s", out)
	}
	if !strings.Contains(out, "--unpin") {
		t.Errorf("expected the pinned-stale notice to mention the --unpin remedy, got:\n%s", out)
	}
}

// TestEnsureAgentVolume_PinnedButFresh_LaunchesSilently pins the design's
// explicit ordering: the pin check lives inside the staleness branch, after
// the TTL comparison, so a pinned volume still inside the TTL launches
// silently instead of nagging on every launch.
func TestEnsureAgentVolume_PinnedButFresh_LaunchesSilently(t *testing.T) {
	statusLines := agentVolumeStatusLines("1.2.3", time.Now(), time.Time{})
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); containsLineWithAll(calls, "agent-cli.sh", "update") {
		t.Errorf("expected a pinned, fresh volume to skip the refresh; calls:\n%s", strings.Join(calls, "\n"))
	}
	out := e.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, "--unpin") {
		t.Errorf("expected a pinned volume inside the TTL to launch silently (no notice), got:\n%s", out)
	}
}

// TestEnsureAgentVolume_RunningContainer_SkipsStalenessBranchButStillChecksPopulated
// pins the attach skip: a running scoped container keeps its started-with
// version regardless, so the staleness branch is skipped entirely (even with
// a very stale volume), while the populated/bootstrap check still runs.
func TestEnsureAgentVolume_RunningContainer_SkipsStalenessBranchButStillChecksPopulated(t *testing.T) {
	stale := time.Now().Add(-25 * time.Hour)
	statusLines := agentVolumeStatusLines("", stale, time.Time{})
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", true); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	calls := readCallLog(t, callLog)
	if !containsLineWithAll(calls, "agent-cli.sh", "status") {
		t.Errorf("expected the populated/bootstrap probe to still run on the attach path; calls:\n%s", strings.Join(calls, "\n"))
	}
	if containsLineWithAll(calls, "agent-cli.sh", "update") {
		t.Errorf("expected a running (attach) container to skip the staleness refresh even with a TTL-stale volume; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_FutureLastSuccess_TreatedAsStaleAndRefreshes pins
// security review finding A: a last_success timestamp in the future (a
// corrupted stamp file, or a host clock that jumped backward via an NTP
// correction) must not permanently suppress the auto-refresh. Without the
// fix, now.Sub(LastSuccess) is negative and never greater than the TTL, so
// the volume looks "fresh" forever.
func TestEnsureAgentVolume_FutureLastSuccess_TreatedAsStaleAndRefreshes(t *testing.T) {
	future := time.Now().Add(48 * time.Hour)
	statusLines := agentVolumeStatusLines("", future, time.Time{})
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); !containsLineWithAll(calls, "agent-cli.sh", "update", "claude") {
		t.Errorf("expected a future last_success to be treated as stale and refreshed; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestEnsureAgentVolume_FutureLastAttempt_DoesNotThrottleStaleRefresh pins the
// backoff half of security review finding A: a last_attempt timestamp in the
// future makes now.Sub(LastAttempt) negative, which must not be misread as
// "recently attempted" (negative duration < the 1h backoff) and silently
// block a stale volume's refresh forever.
func TestEnsureAgentVolume_FutureLastAttempt_DoesNotThrottleStaleRefresh(t *testing.T) {
	stale := time.Now().Add(-25 * time.Hour)
	futureAttempt := time.Now().Add(48 * time.Hour)
	statusLines := agentVolumeStatusLines("", stale, futureAttempt)
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); !containsLineWithAll(calls, "agent-cli.sh", "update", "claude") {
		t.Errorf("expected a future last_attempt to not throttle a stale volume's refresh; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestEnsureAgentVolume_TTLZero_DisablesStalenessBranchEntirely(t *testing.T) {
	t.Setenv("CENCI_SANDBOX_AGENT_CLI_TTL_HOURS", "0")
	stale := time.Now().Add(-9999 * time.Hour)
	statusLines := agentVolumeStatusLines("", stale, time.Time{})
	e, callLog, _ := volumeCheckEngineStatus(t, statusLines, 0, 0, 0)

	if err := e.EnsureAgentVolume("claude", false); err != nil {
		t.Fatalf("EnsureAgentVolume: %v", err)
	}

	if calls := readCallLog(t, callLog); containsLineWithAll(calls, "agent-cli.sh", "update") {
		t.Errorf("expected CENCI_SANDBOX_AGENT_CLI_TTL_HOURS=0 to disable auto-refresh entirely; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// -- CheckSelected ----------------------------------------------------------

// TestCheckSelected_CurrentImage_ReturnsTrueNoBuild pins the happy path for
// the ticket-#519 `--check` gate: an already-current image reports
// current=true and never triggers a build (CheckSelected must only read
// freshness, never rebuild).
func TestCheckSelected_CurrentImage_ReturnsTrueNoBuild(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present, agent-cli + base-version current

	scope := Scope{Image: MonolithImage}
	current, err := e.CheckSelected(scope)
	if err != nil {
		t.Fatalf("CheckSelected: %v", err)
	}
	if !current {
		t.Errorf("CheckSelected(current image) current = false, want true")
	}

	if calls := readCallLog(t, callLog); containsPrefix(calls, "build") {
		t.Errorf("CheckSelected must never build; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestCheckSelected_MissingImage_ReturnsFalseNoBuild pins the missing-image
// case: CheckSelected reports current=false without attempting to build the
// missing image (unlike EnsureImage/BuildSelected).
func TestCheckSelected_MissingImage_ReturnsFalseNoBuild(t *testing.T) {
	e, callLog := buildEngine(t, true) // image missing entirely

	scope := Scope{Image: MonolithImage}
	current, err := e.CheckSelected(scope)
	if err != nil {
		t.Fatalf("CheckSelected: %v", err)
	}
	if current {
		t.Errorf("CheckSelected(missing image) current = true, want false")
	}

	if calls := readCallLog(t, callLog); containsPrefix(calls, "build") {
		t.Errorf("CheckSelected must never build; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestCheckSelected_StaleAgentCLILabel_ReturnsFalseNoBuild pins the stale
// agent-cli label case (image present, base-version current, but the baked
// agent-cli label no longer matches imageAgentLifecycleValue).
func TestCheckSelected_StaleAgentCLILabel_ReturnsFalseNoBuild(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present
	t.Setenv("FAKE_IMAGE_AGENT_LIFECYCLE", "shared-v1")

	scope := Scope{Image: MonolithImage}
	current, err := e.CheckSelected(scope)
	if err != nil {
		t.Fatalf("CheckSelected: %v", err)
	}
	if current {
		t.Errorf("CheckSelected(stale agent-cli label) current = true, want false")
	}

	if calls := readCallLog(t, callLog); containsPrefix(calls, "build") {
		t.Errorf("CheckSelected must never build; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestCheckSelected_BaseVersionDrift_ReturnsFalseNoBuild pins the
// base-version-drift case (image present, agent-cli label current, but the
// baked cenci.base-version label no longer matches the engine's BaseTag).
func TestCheckSelected_BaseVersionDrift_ReturnsFalseNoBuild(t *testing.T) {
	e, callLog := buildEngine(t, false) // image present
	t.Setenv("FAKE_IMAGE_BASE_VERSION", "OLDTAG")

	scope := Scope{Image: MonolithImage}
	current, err := e.CheckSelected(scope)
	if err != nil {
		t.Fatalf("CheckSelected: %v", err)
	}
	if current {
		t.Errorf("CheckSelected(base-version drift) current = true, want false")
	}

	if calls := readCallLog(t, callLog); containsPrefix(calls, "build") {
		t.Errorf("CheckSelected must never build; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// -- RefreshRunningPlugins ------------------------------------------------

// wantClaudeRefreshCmd/wantCodexRefreshCmd (#1002, red phase): the trailing
// plugin-list arguments in each provision_*/update_* call now expand
// ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch} in-container instead of the
// literal "cenci cenci-watch" pair, so the same command works for
// RefreshRunningPlugins' repo-less host-wide sweep, the scoped exec path, and
// the one-shot volume update path -- each container resolves its own list
// from its own create-time env (or falls back to the default pair when the
// var is unset, e.g. a legacy container predating this ticket). The THIRD
// positional "cenci" in "provision_plugins <dir> cenci matteobortolazzo/cenci
// ..." is the marketplace name, not a plugin, and stays literal -- only the
// trailing plugin-list portion changes (plan Risks: wrong positional
// replaced).
const wantClaudeRefreshCmd = `source /usr/local/bin/lib/migrate-settings.sh && heal_plugin_installs /home/dev/.claude/plugins && provision_plugins /home/dev/.claude/plugins cenci matteobortolazzo/cenci ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch} && update_plugins /home/dev/.claude/plugins cenci 0 ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch}`

const wantCodexRefreshCmd = `source /usr/local/bin/lib/migrate-settings.sh && provision_codex_plugins /home/dev/.codex cenci matteobortolazzo/cenci ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch} && update_codex_plugins /home/dev/.codex cenci 0 ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch}`

// refreshRuntimeEngine wires an Engine to a fake docker that answers `ps
// --format {{.Names}}` with psOutput and fails `exec` calls whose target
// container name (argv[4], after `exec -u dev`) is in failNames — so
// RefreshRunningPlugins tests can drive the mixed-container/best-effort
// paths without a real container runtime.
func refreshRuntimeEngine(t *testing.T, psOutput string, failNames ...string) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	callLogPath := filepath.Join(dir, "calls.txt")
	body := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = ps ]; then
  printf '%%s' %s
  exit 0
fi
if [ "$1" = exec ]; then
  target="$4"
  for f in %s; do
    if [ "$target" = "$f" ]; then
      exit 1
    fi
  done
  exit 0
fi
exit 0
`, exectest.ShellQuote(callLogPath), exectest.ShellQuote(psOutput), strings.Join(failNames, " "))
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
	}, callLogPath
}

// TestRefreshRunningPlugins_RefreshesEachRunningContainerByAgent pins the
// mixed-container happy path: every running claude-cenci-*/codex-cenci-*
// container gets exec'd with -u dev and its agent-specific ttl-0 refresh
// command.
func TestRefreshRunningPlugins_RefreshesEachRunningContainerByAgent(t *testing.T) {
	e, callLog := refreshRuntimeEngine(t, "claude-cenci-agentstack\ncodex-cenci-otherrepo\n")

	if err := e.RefreshRunningPlugins(); err != nil {
		t.Fatalf("RefreshRunningPlugins: %v", err)
	}

	calls := readCallLog(t, callLog)
	wantClaude := "exec -u dev claude-cenci-agentstack /bin/bash -c " + wantClaudeRefreshCmd
	if !containsLine(calls, wantClaude) {
		t.Errorf("expected claude refresh exec %q; calls:\n%s", wantClaude, strings.Join(calls, "\n"))
	}
	wantCodex := "exec -u dev codex-cenci-otherrepo /bin/bash -c " + wantCodexRefreshCmd
	if !containsLine(calls, wantCodex) {
		t.Errorf("expected codex refresh exec %q; calls:\n%s", wantCodex, strings.Join(calls, "\n"))
	}
}

// TestRefreshRunningPlugins_SkipsNonSandboxContainerNames pins that a
// non-sandbox container running alongside sandbox containers never gets
// exec'd.
func TestRefreshRunningPlugins_SkipsNonSandboxContainerNames(t *testing.T) {
	e, callLog := refreshRuntimeEngine(t, "claude-cenci-agentstack\nsome-other-container\n")

	if err := e.RefreshRunningPlugins(); err != nil {
		t.Fatalf("RefreshRunningPlugins: %v", err)
	}

	calls := readCallLog(t, callLog)
	if containsLineWithAll(calls, "exec", "some-other-container") {
		t.Errorf("expected non-sandbox container to be skipped; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestRefreshRunningPlugins_NoRunningContainers_PrintsNoteNoExecCalls pins
// the zero-containers case: a note is printed and no exec calls are made
// (and RefreshRunningPlugins returns no error — zero running containers is
// not a failure).
func TestRefreshRunningPlugins_NoRunningContainers_PrintsNoteNoExecCalls(t *testing.T) {
	e, callLog := refreshRuntimeEngine(t, "")

	if err := e.RefreshRunningPlugins(); err != nil {
		t.Fatalf("RefreshRunningPlugins: %v", err)
	}

	if calls := readCallLog(t, callLog); containsPrefix(calls, "exec") {
		t.Errorf("expected no exec calls when no containers are running; calls:\n%s", strings.Join(calls, "\n"))
	}
	if out := e.Stdout.(*bytes.Buffer).String(); !strings.Contains(out, "No running sandbox containers") {
		t.Errorf("expected a note about no running containers, got:\n%s", out)
	}
}

// TestRefreshRunningPlugins_OneContainerFails_OthersStillRefreshedAndErrorAggregated
// pins the best-effort contract: a single container's exec failure must not
// stop the others from being refreshed, and the failure surfaces in the
// aggregated returned error.
func TestRefreshRunningPlugins_OneContainerFails_OthersStillRefreshedAndErrorAggregated(t *testing.T) {
	e, callLog := refreshRuntimeEngine(t, "claude-cenci-agentstack\ncodex-cenci-otherrepo\n", "claude-cenci-agentstack")

	err := e.RefreshRunningPlugins()
	if err == nil {
		t.Fatal("expected an aggregated error when one container's refresh fails")
	}
	if !strings.Contains(err.Error(), "claude-cenci-agentstack") {
		t.Errorf("expected the aggregated error to name the failing container, got: %v", err)
	}

	calls := readCallLog(t, callLog)
	wantCodex := "exec -u dev codex-cenci-otherrepo /bin/bash -c " + wantCodexRefreshCmd
	if !containsLine(calls, wantCodex) {
		t.Errorf("expected the other container to still be refreshed; calls:\n%s", strings.Join(calls, "\n"))
	}
}
