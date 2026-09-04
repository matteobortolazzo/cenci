package hostrepo

// Ticket #1095 Phase 3 (red): Resolve/parseOriginRemote are stubs in
// resolve.go that unconditionally return errNotImplemented / zero values, so
// every test below fails for the right reason (behavior not yet
// implemented) rather than a compile error. Phase 4 makes these pass.
//
// Fakes docker/podman on PATH (mirroring
// watch/internal/sandbox/launcher/faketest_test.go's writeFakeRuntime
// style, simplified to this package's own needs) plus real `git init` temp
// checkouts for the origin-remote matching half -- per the plan's test
// list. PATH is fully replaced (not prepended) with a directory containing
// only a symlink to the real `git` binary plus the fake docker/podman
// scripts, so an ambient real docker/podman elsewhere on the host PATH can
// never leak into these tests (watch/docs/test-isolation.md conventions).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// fakeContainer describes one running sandbox container a fake runtime's
// `ps --format {{.Names}}` reports, plus the /workspace bind-mount source
// its combined `inspect --format` record answers with. An empty source
// means "no /workspace mount" (a legitimate empty record, not malformed).
type fakeContainer struct {
	name   string
	source string
}

// binDir returns a PATH-ready directory containing a symlink to the real
// `git` binary, so tests can layer fake docker/podman scripts into it and
// fully replace PATH with just this directory -- git stays available for
// both the test fixtures' own `git init`/`remote add` setup and (once
// implemented) Resolve's own git remote reads, while no ambient real
// docker/podman on the host's actual PATH can ever be reached.
func binDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not found on PATH: %v", err)
	}
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		t.Fatalf("symlink git into fake PATH dir: %v", err)
	}
	return dir
}

// writeFakeRuntimeBin writes a fake docker/podman binary at
// <dir>/<binName>: `ps --format {{.Names}}` answers one container name per
// line (or exits psExit if non-zero); `inspect --format <fmt> <name...>`
// answers one inspectRecordSeparator-delimited mount record per requested
// name, in request order (or exits inspectExit if non-zero). The fake
// ignores the actual --format template text -- tests don't reimplement Go
// templating, they just pin the wire shape resolve.go's parser must consume.
// rawInspect, when non-empty, replaces the generated inspect output
// entirely (used to script a deliberately malformed response).
func writeFakeRuntimeBin(t *testing.T, dir, binName string, containers []fakeContainer, psExit, inspectExit int, rawInspect string) {
	t.Helper()
	var ps strings.Builder
	for _, c := range containers {
		ps.WriteString(c.name)
		ps.WriteString("\n")
	}

	var caseArms strings.Builder
	for _, c := range containers {
		record := ""
		if c.source != "" {
			record = c.source + "::/workspace::false\n"
		}
		fmt.Fprintf(&caseArms, "    %s) printf '%%s' %s ;;\n", exectest.ShellQuote(c.name), exectest.ShellQuote(record))
	}

	body := fmt.Sprintf(`#!/bin/sh
case "$1" in
ps)
  if [ %d -ne 0 ]; then exit %d; fi
  printf '%%s' %s
  exit 0
  ;;
inspect)
  if [ %d -ne 0 ]; then exit %d; fi
  rawinspect=%s
  if [ -n "$rawinspect" ]; then
    printf '%%s' "$rawinspect"
    exit 0
  fi
  shift 3
  for n in "$@"; do
    case "$n" in
%s    *) printf '' ;;
    esac
    printf -- '%s\n'
  done
  exit 0
  ;;
esac
exit 1
`, psExit, psExit, exectest.ShellQuote(ps.String()), inspectExit, inspectExit, exectest.ShellQuote(rawInspect), caseArms.String(), inspectRecordSeparator)
	exectest.WriteExecutable(t, filepath.Join(dir, binName), body)
}

// initGitRepoWithOrigin creates a real git checkout at dir with origin set
// to originURL -- the "real git init temp checkouts" half of the plan's
// test list, exercising Resolve's own git-remote reads once implemented
// rather than faking git too.
func initGitRepoWithOrigin(t *testing.T, dir, originURL string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "remote", "add", "origin", originURL)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// -- Resolve: single/ambiguous/dedup/zero-match verdicts --------------------

// TestResolve_SingleMatchingContainer_ReturnsHostDir covers AC 1's success
// case: exactly one running sandbox container whose /workspace source's
// origin remote matches the requested repo resolves to that host directory.
func TestResolve_SingleMatchingContainer_ReturnsHostDir(t *testing.T) {
	dir := binDir(t)
	repoDir := filepath.Join(t.TempDir(), "widgets")
	initGitRepoWithOrigin(t, repoDir, "git@github.com:acme/widgets.git")
	writeFakeRuntimeBin(t, dir, "docker", []fakeContainer{
		{name: "claude-cenci-widgets", source: repoDir},
	}, 0, 0, "")
	t.Setenv("PATH", dir)

	got, err := Resolve(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != repoDir {
		t.Errorf("Resolve = %q, want %q", got, repoDir)
	}
}

// TestResolve_TwoDistinctMatches_ErrAmbiguous covers the ticket's "fail
// closed on multiple distinct matches" decision: two running containers
// bound to two different checkouts of the same repo (e.g. two worktrees)
// must nack ambiguous, never guess one.
func TestResolve_TwoDistinctMatches_ErrAmbiguous(t *testing.T) {
	dir := binDir(t)
	base := t.TempDir()
	repoA := filepath.Join(base, "widgets-a")
	repoB := filepath.Join(base, "widgets-b")
	initGitRepoWithOrigin(t, repoA, "https://github.com/acme/widgets.git")
	initGitRepoWithOrigin(t, repoB, "https://github.com/acme/widgets.git")
	writeFakeRuntimeBin(t, dir, "docker", []fakeContainer{
		{name: "claude-cenci-widgets-a", source: repoA},
		{name: "claude-cenci-widgets-b", source: repoB},
	}, 0, 0, "")
	t.Setenv("PATH", dir)

	_, err := Resolve(context.Background(), "acme/widgets")
	if !errors.Is(err, ErrAmbiguous) {
		t.Errorf("Resolve err = %v, want errors.Is(err, ErrAmbiguous)", err)
	}
}

// TestResolve_TwoContainersSameCheckout_SingleMatch covers dedup: two
// running containers whose /workspace sources both resolve (one via a
// symlink) to the same real checkout must collapse to one match, never a
// false ambiguity.
func TestResolve_TwoContainersSameCheckout_SingleMatch(t *testing.T) {
	dir := binDir(t)
	base := t.TempDir()
	repoDir := filepath.Join(base, "real-widgets")
	initGitRepoWithOrigin(t, repoDir, "https://github.com/acme/widgets")
	symDir := filepath.Join(base, "sym-widgets")
	if err := os.Symlink(repoDir, symDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeFakeRuntimeBin(t, dir, "docker", []fakeContainer{
		{name: "claude-cenci-widgets-1", source: repoDir},
		{name: "codex-cenci-widgets-2", source: symDir},
	}, 0, 0, "")
	t.Setenv("PATH", dir)

	got, err := Resolve(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Both a symlink-normalized path and its target must resolve to the same
	// canonical directory as the single match.
	realGot, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", got, err)
	}
	if realGot != repoDir {
		t.Errorf("Resolve = %q (canonical %q), want the single deduped checkout %q", got, realGot, repoDir)
	}
}

// TestResolve_ZeroMatches_ErrNoMatch covers the ticket's "fail closed on
// zero matches" decision: a running container whose origin belongs to a
// different repo entirely must not match, and the caller gets ErrNoMatch.
func TestResolve_ZeroMatches_ErrNoMatch(t *testing.T) {
	dir := binDir(t)
	repoDir := filepath.Join(t.TempDir(), "other")
	initGitRepoWithOrigin(t, repoDir, "https://github.com/acme/other.git")
	writeFakeRuntimeBin(t, dir, "docker", []fakeContainer{
		{name: "claude-cenci-other", source: repoDir},
	}, 0, 0, "")
	t.Setenv("PATH", dir)

	_, err := Resolve(context.Background(), "acme/widgets")
	if !errors.Is(err, ErrNoMatch) {
		t.Errorf("Resolve err = %v, want errors.Is(err, ErrNoMatch)", err)
	}
}

// TestResolve_NonRepoBindSource_ExcludedAsNoMatch covers legacy-scope
// sandboxes (watch/internal/sandbox/launcher/scope.go): a /workspace source
// that isn't a git checkout at all (no origin remote to read) must be
// excluded from matching -- never an error by itself, never a false
// ambiguity/match -- leaving ErrNoMatch when it's the only container.
func TestResolve_NonRepoBindSource_ExcludedAsNoMatch(t *testing.T) {
	dir := binDir(t)
	notARepo := filepath.Join(t.TempDir(), "Repos")
	if err := os.MkdirAll(notARepo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFakeRuntimeBin(t, dir, "docker", []fakeContainer{
		{name: "claude-cenci-legacy", source: notARepo},
	}, 0, 0, "")
	t.Setenv("PATH", dir)

	_, err := Resolve(context.Background(), "acme/widgets")
	if !errors.Is(err, ErrNoMatch) {
		t.Errorf("Resolve err = %v, want errors.Is(err, ErrNoMatch) for a non-git bind source", err)
	}
	if errors.Is(err, ErrMalformedInspect) || errors.Is(err, ErrAmbiguous) {
		t.Errorf("Resolve err = %v, want a plain no-match, not malformed/ambiguous", err)
	}
}

// -- Resolve: fail-closed probe classification -------------------------------

// TestResolve_MalformedInspectOutput_ErrMalformedInspect covers the
// ticket's "a failed or unparsable inspect nacks; it never guesses"
// decision: an inspect response that doesn't parse into the expected
// mount-record shape must classify as ErrMalformedInspect, never silently
// read as "no /workspace mount found" (watch/docs/error-handling.md's
// default-deny rule).
func TestResolve_MalformedInspectOutput_ErrMalformedInspect(t *testing.T) {
	dir := binDir(t)
	writeFakeRuntimeBin(t, dir, "docker", []fakeContainer{
		{name: "claude-cenci-widgets", source: "/some/path"},
	}, 0, 0, "this is not the expected record shape at all")
	t.Setenv("PATH", dir)

	_, err := Resolve(context.Background(), "acme/widgets")
	if !errors.Is(err, ErrMalformedInspect) {
		t.Errorf("Resolve err = %v, want errors.Is(err, ErrMalformedInspect)", err)
	}
}

// TestResolve_NonZeroPsExit_ProbeFailureNotGuessed covers plan Q&A #4: a
// per-runtime `ps` failure must nack with a probe-failure error distinct
// from every match-verdict sentinel, never silently fall through to "zero
// matches".
func TestResolve_NonZeroPsExit_ProbeFailureNotGuessed(t *testing.T) {
	dir := binDir(t)
	writeFakeRuntimeBin(t, dir, "docker", nil, 1, 0, "")
	t.Setenv("PATH", dir)

	_, err := Resolve(context.Background(), "acme/widgets")
	assertProbeFailureNotGuessed(t, err)
}

// TestResolve_NonZeroInspectExit_ProbeFailureNotGuessed mirrors the ps case
// for a failed combined inspect probe.
func TestResolve_NonZeroInspectExit_ProbeFailureNotGuessed(t *testing.T) {
	dir := binDir(t)
	writeFakeRuntimeBin(t, dir, "docker", []fakeContainer{
		{name: "claude-cenci-widgets", source: "/some/path"},
	}, 0, 1, "")
	t.Setenv("PATH", dir)

	_, err := Resolve(context.Background(), "acme/widgets")
	assertProbeFailureNotGuessed(t, err)
}

// assertProbeFailureNotGuessed asserts err is a non-nil probe-failure error
// distinct from every match-verdict sentinel, and (per
// watch/docs/error-handling.md #446's error-content-specific-assertion
// rule) that it actually names the failing runtime -- a bare non-empty
// check alone would pass identically against a placeholder string that
// carries no real diagnostic content, including this package's own Phase 3
// stub.
func assertProbeFailureNotGuessed(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Resolve err = nil, want a non-nil probe-failure error")
	}
	if errors.Is(err, ErrNoMatch) || errors.Is(err, ErrAmbiguous) || errors.Is(err, ErrMalformedInspect) {
		t.Errorf("Resolve err = %v, want a probe-failure error distinct from every match-verdict sentinel (a runtime probe failure must nack, never guess a verdict)", err)
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("Resolve err = %v, want it to name the failing runtime (docker)", err)
	}
}

// -- Resolve: cross-runtime enumeration --------------------------------------

// TestResolve_BothRuntimesEnumerated covers the ticket's cross-runtime
// decision: the single matching container is scripted under podman only,
// with a healthy but non-matching docker fake also on PATH, proving
// Resolve actually enumerates every installed runtime rather than stopping
// at the first (docker, per sandbox.AvailableRuntimes' order).
func TestResolve_BothRuntimesEnumerated(t *testing.T) {
	dir := binDir(t)
	base := t.TempDir()
	dockerRepo := filepath.Join(base, "other")
	initGitRepoWithOrigin(t, dockerRepo, "https://github.com/acme/other.git")
	podmanRepo := filepath.Join(base, "widgets")
	initGitRepoWithOrigin(t, podmanRepo, "https://github.com/acme/widgets.git")

	writeFakeRuntimeBin(t, dir, "docker", []fakeContainer{
		{name: "claude-cenci-other", source: dockerRepo},
	}, 0, 0, "")
	writeFakeRuntimeBin(t, dir, "podman", []fakeContainer{
		{name: "claude-cenci-widgets", source: podmanRepo},
	}, 0, 0, "")
	t.Setenv("PATH", dir)

	got, err := Resolve(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != podmanRepo {
		t.Errorf("Resolve = %q, want the podman-owned checkout %q -- both runtimes must be enumerated", got, podmanRepo)
	}
}

// -- parseOriginRemote: pure normalization logic -----------------------------

// TestParseOriginRemote_NormalizesEveryRemoteURLForm is a direct unit test
// of the pure origin-normalization logic (watch/docs/test-strategy.md's
// exception for pure, deterministic parsing) -- ssh://, https://, and
// scp-style forms, with and without a trailing ".git" suffix and trailing
// slash, must all normalize to the same owner/name pair.
func TestParseOriginRemote_NormalizesEveryRemoteURLForm(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"scp-style with .git", "git@github.com:acme/widgets.git"},
		{"scp-style without .git", "git@github.com:acme/widgets"},
		{"https with .git", "https://github.com/acme/widgets.git"},
		{"https without .git", "https://github.com/acme/widgets"},
		{"https with trailing slash", "https://github.com/acme/widgets/"},
		{"ssh:// form", "ssh://git@github.com/acme/widgets.git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, name, err := parseOriginRemote(tc.url)
			if err != nil {
				t.Fatalf("parseOriginRemote(%q): %v", tc.url, err)
			}
			if owner != "acme" || name != "widgets" {
				t.Errorf("parseOriginRemote(%q) = (%q, %q), want (acme, widgets)", tc.url, owner, name)
			}
		})
	}
}

// TestParseOriginRemote_UnrecognizedFormReturnsError covers the fail-closed
// side: a URL with no discernible owner/name path must error, never return
// a guessed empty/partial pair.
func TestParseOriginRemote_UnrecognizedFormReturnsError(t *testing.T) {
	for _, url := range []string{"", "not-a-url", "https://github.com/onlyonesegment"} {
		err := func() error {
			_, _, err := parseOriginRemote(url)
			return err
		}()
		if err == nil {
			t.Errorf("parseOriginRemote(%q): want a non-nil error for an unrecognized remote URL", url)
			continue
		}
		// #446: content-specific, not just non-nil -- a placeholder message
		// (including this package's own Phase 3 stub) must not pass this
		// assertion by accident.
		if !strings.Contains(err.Error(), "remote") {
			t.Errorf("parseOriginRemote(%q) err = %q, want it to mention the malformed remote url", url, err.Error())
		}
	}
}
