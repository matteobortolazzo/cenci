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
	"reflect"
	"strings"
	"testing"
	"time"

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

// -- ResolveContainer: the bound-container resolve (#1135) -------------------

// rcFixture is one ResolveContainer scenario: a fake runtime and a logging git
// wrapper first (and only) on PATH, a /workspace source on disk, and the two
// argv logs the fakes append to.
type rcFixture struct {
	source     string // the /workspace bind source the fake inspect reports
	runtimeLog string // one line per fake-runtime invocation
	gitLog     string // one line per git invocation
}

// rcOpts scripts the fake runtime's single canned answer. Every invocation of
// the fake gets the same answer, so a stray `ps` is logged (and then caught by
// the argv assertions) rather than failing on its own.
type rcOpts struct {
	runtime string // fake binary name; "" means docker
	origin  string // the source's origin URL; "" leaves the source a non-git directory
	stdout  string // fake stdout; "{{SOURCE}}" is replaced by the source path
	stderr  string
	exit    int
	block   bool // print stderr, then block (until the caller's deadline kills it)

	gitBlock bool // the git wrapper blocks instead of running git (until the deadline kills it)
	gitExit  int  // when non-zero, the git wrapper exits with this status instead of running git
}

const (
	argvSep     = "\x1f"
	rcContainer = "claude-cenci-widgets"
)

// argvLogLine is the shell snippet each fake uses to record its argv: every
// argument terminated by a unit separator, one invocation per line.
func argvLogLine(logPath string) string {
	return "printf '%s\\037' \"$@\" >> " + exectest.ShellQuote(logPath) + "\nprintf '\\n' >> " + exectest.ShellQuote(logPath) + "\n"
}

func newRCFixture(t *testing.T, o rcOpts) rcFixture {
	t.Helper()
	runtime := o.runtime
	if runtime == "" {
		runtime = "docker"
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not found on PATH: %v", err)
	}
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("sleep not found on PATH: %v", err)
	}

	f := rcFixture{source: filepath.Join(t.TempDir(), "widgets")}
	if o.origin != "" {
		initGitRepoWithOrigin(t, f.source, o.origin)
	} else if err := os.MkdirAll(f.source, 0o755); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	logs := t.TempDir()
	f.runtimeLog = filepath.Join(logs, "runtime.log")
	f.gitLog = filepath.Join(logs, "git.log")

	stdout := strings.ReplaceAll(o.stdout, "{{SOURCE}}", f.source)
	body := "#!/bin/sh\n" + argvLogLine(f.runtimeLog) +
		"printf '%s' " + exectest.ShellQuote(stdout) + "\n" +
		"printf '%s' " + exectest.ShellQuote(o.stderr) + " >&2\n"
	if o.block {
		body += "exec " + exectest.ShellQuote(sleepBin) + " 30\n"
	}
	body += fmt.Sprintf("exit %d\n", o.exit)
	exectest.WriteExecutable(t, filepath.Join(bin, runtime), body)
	gitBody := "#!/bin/sh\n" + argvLogLine(f.gitLog)
	switch {
	case o.gitBlock:
		gitBody += "exec " + exectest.ShellQuote(sleepBin) + " 30\n"
	case o.gitExit != 0:
		gitBody += fmt.Sprintf("exit %d\n", o.gitExit)
	default:
		gitBody += "exec " + exectest.ShellQuote(realGit) + " \"$@\"\n"
	}
	exectest.WriteExecutable(t, filepath.Join(bin, "git"), gitBody)

	t.Setenv("PATH", bin)
	return f
}

// calls reads a fake's argv log back as one []string per invocation; a log that
// was never written means the fake never ran.
func calls(t *testing.T, logPath string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, argvSep), argvSep))
	}
	return out
}

// foundMount is the inspect answer for a container with a /workspace mount: the
// combinedInspectFormat record, then the template's own trailing newline.
const foundMount = "{{SOURCE}}::/workspace::false\n" + inspectRecordSeparator + "\n"

// TestResolveContainer_MatchingContainer_RunsOneInspectAndOneOriginRead covers
// the criterion that the bound resolve runs exactly one `inspect` of that
// container and one `git remote get-url origin` on its /workspace source, and
// never `ps`: the recorded argv of both fakes is the whole call set.
func TestResolveContainer_MatchingContainer_RunsOneInspectAndOneOriginRead(t *testing.T) {
	f := newRCFixture(t, rcOpts{runtime: "podman", origin: "git@github.com:acme/widgets.git", stdout: foundMount})

	got, err := ResolveContainer(context.Background(), "podman", rcContainer, "acme/widgets")
	if err != nil {
		t.Fatalf("ResolveContainer: %v", err)
	}
	if got != f.source {
		t.Errorf("ResolveContainer = %q, want the bound source %q", got, f.source)
	}
	wantRuntime := [][]string{{"inspect", "--type", "container", "--format", combinedInspectFormat, rcContainer}}
	if gotCalls := calls(t, f.runtimeLog); !reflect.DeepEqual(gotCalls, wantRuntime) {
		t.Errorf("runtime argv = %q, want exactly %q (one inspect of the bound container, never ps)", gotCalls, wantRuntime)
	}
	wantGit := [][]string{{"-C", f.source, "remote", "get-url", "origin"}}
	if gotCalls := calls(t, f.gitLog); !reflect.DeepEqual(gotCalls, wantGit) {
		t.Errorf("git argv = %q, want exactly %q", gotCalls, wantGit)
	}
}

// A container that is gone (the sandbox runs with --rm, so a stopped one
// vanishes) is "not running": ErrNoMatch, and no git read. Both runtimes' wordings.
func TestResolveContainer_NotRunning_ErrNoMatch(t *testing.T) {
	for _, tc := range []struct{ name, stderr string }{
		{"docker", "Error: No such container: " + rcContainer + "\n"},
		{"podman", "Error: no such object: " + rcContainer + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRCFixture(t, rcOpts{origin: "https://github.com/acme/widgets.git", stderr: tc.stderr, exit: 1})

			_, err := ResolveContainer(context.Background(), "docker", rcContainer, "acme/widgets")
			if !errors.Is(err, ErrNoMatch) {
				t.Fatalf("ResolveContainer err = %v, want errors.Is(err, ErrNoMatch)", err)
			}
			if n := len(calls(t, f.runtimeLog)); n != 1 {
				t.Errorf("runtime ran %d times, want exactly 1 inspect", n)
			}
			if g := calls(t, f.gitLog); g != nil {
				t.Errorf("git ran for a vanished container: %q", g)
			}
		})
	}
}

// A running container with no /workspace mount has no checkout to resolve.
func TestResolveContainer_NoWorkspaceMount_ErrNoMatch(t *testing.T) {
	f := newRCFixture(t, rcOpts{origin: "https://github.com/acme/widgets.git", stdout: inspectRecordSeparator + "\n"})

	_, err := ResolveContainer(context.Background(), "docker", rcContainer, "acme/widgets")
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("ResolveContainer err = %v, want errors.Is(err, ErrNoMatch)", err)
	}
	if g := calls(t, f.gitLog); g != nil {
		t.Errorf("git ran with no source to read: %q", g)
	}
}

// The bound container's origin must still name the requested repo: a different
// repo, or a source that is not a git checkout at all, is ErrNoMatch -- after
// exactly one origin read.
func TestResolveContainer_OriginMismatch_ErrNoMatch(t *testing.T) {
	for _, tc := range []struct{ name, origin string }{
		{"another repo", "https://github.com/acme/other.git"},
		{"not a git checkout", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRCFixture(t, rcOpts{origin: tc.origin, stdout: foundMount})

			_, err := ResolveContainer(context.Background(), "docker", rcContainer, "acme/widgets")
			if !errors.Is(err, ErrNoMatch) {
				t.Fatalf("ResolveContainer err = %v, want errors.Is(err, ErrNoMatch)", err)
			}
			if n := len(calls(t, f.gitLog)); n != 1 {
				t.Errorf("git ran %d times, want exactly 1 origin read", n)
			}
		})
	}
}

// A `git remote get-url origin` that fails with a plain non-zero exit (no origin
// remote, an unreadable checkout) fails closed as ErrNoMatch, the same as a
// mismatch. A git read that stalls past the caller's deadline is different: the
// timeout stays visible to errors.Is and must never read as ErrNoMatch, or the
// arm path would report "host repo could not be resolved" for a slow disk.
func TestResolveContainer_OriginRead_ExitIsNoMatchButDeadlineStaysVisible(t *testing.T) {
	t.Run("non-zero git exit is ErrNoMatch", func(t *testing.T) {
		newRCFixture(t, rcOpts{origin: "https://github.com/acme/widgets.git", stdout: foundMount, gitExit: 128})

		_, err := ResolveContainer(context.Background(), "docker", rcContainer, "acme/widgets")
		if !errors.Is(err, ErrNoMatch) {
			t.Fatalf("ResolveContainer err = %v, want errors.Is(err, ErrNoMatch)", err)
		}
	})
	t.Run("git stalled past the deadline is a timeout, not ErrNoMatch", func(t *testing.T) {
		f := newRCFixture(t, rcOpts{origin: "https://github.com/acme/widgets.git", stdout: foundMount, gitBlock: true})
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := ResolveContainer(ctx, "docker", rcContainer, "acme/widgets")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ResolveContainer err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
		}
		if errors.Is(err, ErrNoMatch) {
			t.Errorf("ResolveContainer err = %v, a git timeout must not read as no-match", err)
		}
		if n := len(calls(t, f.gitLog)); n != 1 {
			t.Errorf("git ran %d times, want exactly 1 origin read", n)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("ResolveContainer took %v, want it bounded by the caller's deadline", elapsed)
		}
	})
}

// An inspect answer that does not parse into exactly one record fails closed as
// a wrapped ErrMalformedInspect -- never "no mount", never ErrNoMatch.
func TestResolveContainer_MalformedInspect_FailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, stdout string }{
		{"garbage", "this is not the expected record shape at all"},
		{"empty output", ""},
		{"two records for one container", foundMount + foundMount},
		{"record without a terminating separator", "{{SOURCE}}::/workspace::false\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRCFixture(t, rcOpts{origin: "https://github.com/acme/widgets.git", stdout: tc.stdout})

			_, err := ResolveContainer(context.Background(), "docker", rcContainer, "acme/widgets")
			if !errors.Is(err, ErrMalformedInspect) {
				t.Fatalf("ResolveContainer err = %v, want errors.Is(err, ErrMalformedInspect)", err)
			}
			if errors.Is(err, ErrNoMatch) {
				t.Errorf("ResolveContainer err = %v, a malformed answer must not read as no-match", err)
			}
			if g := calls(t, f.gitLog); g != nil {
				t.Errorf("git ran after a malformed inspect: %q", g)
			}
		})
	}
}

// Every inspect failure that is not a narrow "the container is gone" is a probe
// error -- distinct from every verdict sentinel, naming the runtime -- so a
// runtime that cannot be asked never reads as "not running" (watch/AGENTS.md's
// narrow match-miss rule).
func TestResolveContainer_OtherInspectFailure_IsAProbeError(t *testing.T) {
	for _, tc := range []struct{ name, stderr string }{
		{"daemon unreachable", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n"},
		{"permission denied", "permission denied while trying to connect to the Docker daemon socket\n"},
		{"a missing image is not a missing container", "Error: No such image: " + rcContainer + "\n"},
		{"no stderr at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRCFixture(t, rcOpts{origin: "https://github.com/acme/widgets.git", stderr: tc.stderr, exit: 1})

			_, err := ResolveContainer(context.Background(), "docker", rcContainer, "acme/widgets")
			assertProbeFailureNotGuessed(t, err)
			if g := calls(t, f.gitLog); g != nil {
				t.Errorf("git ran after a failed inspect: %q", g)
			}
		})
	}
}

// A deadline is never "the container is gone", even when the killed runtime had
// already printed "no such container": the vanished check needs a real exit
// error, not a timeout, and the deadline stays visible to errors.Is.
func TestResolveContainer_Deadline_IsAProbeErrorEvenWithVanishedStderr(t *testing.T) {
	newRCFixture(t, rcOpts{
		origin: "https://github.com/acme/widgets.git",
		stderr: "Error: No such container: " + rcContainer + "\n",
		block:  true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := ResolveContainer(ctx, "docker", rcContainer, "acme/widgets")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveContainer err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
	if errors.Is(err, ErrNoMatch) {
		t.Errorf("ResolveContainer err = %v, a timeout must not read as a vanished container", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("ResolveContainer took %v, want it bounded by the caller's deadline", elapsed)
	}
}

// The runtime and container name are re-validated here, not trusted from the
// caller: an unsupported runtime is never executed as a command, and a name that
// is not a sandbox container never reaches an inspect argv.
func TestResolveContainer_RejectsAnUnsupportedRuntimeOrContainerName(t *testing.T) {
	for _, tc := range []struct{ name, runtime, container string }{
		{"runtime outside docker/podman", "sh", rcContainer},
		{"empty runtime", "", rcContainer},
		{"container that is not a sandbox", "docker", "postgres"},
		{"empty container", "docker", ""},
		{"container shaped like a flag", "docker", "--all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := tc.runtime
			if bin == "" {
				bin = "docker"
			}
			f := newRCFixture(t, rcOpts{runtime: bin, origin: "https://github.com/acme/widgets.git", stdout: foundMount})

			_, err := ResolveContainer(context.Background(), tc.runtime, tc.container, "acme/widgets")
			if err == nil {
				t.Fatal("ResolveContainer err = nil, want a rejection")
			}
			if errors.Is(err, ErrNoMatch) || errors.Is(err, ErrMalformedInspect) || errors.Is(err, ErrAmbiguous) {
				t.Errorf("ResolveContainer err = %v, want a plain rejection distinct from every verdict sentinel", err)
			}
			if c := calls(t, f.runtimeLog); c != nil {
				t.Errorf("runtime was executed for a rejected input: %q", c)
			}
		})
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
