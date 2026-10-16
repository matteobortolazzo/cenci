package worktreeclean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests cover the threat model of a later deletion feature: a sandboxed
// agent can write .git/config, .gitattributes and a worktree's files through the
// shared mount, so Inspect must never run a repo-configured command, must never
// start a process inside a submodule, and must refuse (keep the entry) whenever it
// cannot prove otherwise. Every "never runs" check is a marker file that the
// configured command would create, paired with a control showing the same
// configuration does run it under a plain git call.

// markerScript writes an executable <name>.sh that creates a marker file and
// then runs body; it returns the script and marker paths.
func markerScript(t *testing.T, r *fixtureRepo, name, body string) (script, marker string) {
	t.Helper()
	marker = filepath.Join(r.Tmp, name+"-ran")
	script = filepath.Join(r.Tmp, name+".sh")
	writeFile(t, script, "#!/bin/sh\ntouch '"+marker+"'\n"+body+"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script, marker
}

func assertNotRan(t *testing.T, marker, what string) {
	t.Helper()
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Errorf("%s ran (Lstat err = %v), want it never executed", what, err)
	}
}

func assertRan(t *testing.T, marker, what string) {
	t.Helper()
	if _, err := os.Lstat(marker); err != nil {
		t.Fatalf("control: %s did not run under a plain git call (Lstat err = %v), so the not-run check proves nothing", what, err)
	}
}

// --- submodules ---

// submoduleGit is `git -c protocol.file.allow=always` for submodule commands on
// local paths (git refuses the file transport by default).
func submoduleGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runGit(t, dir, append([]string{"-c", "protocol.file.allow=always"}, args...)...)
}

// A submodule is a deliberate limit: `git status` can only see inside one by
// starting a child git in the submodule directory, and that child loads the
// submodule's own (agent-writable) config and attributes. The only mode that
// avoids the child (--ignore-submodules=all) hides a dirty submodule, so a
// worktree holding any gitlink (mode 160000) in its index or HEAD tree is an
// error (entry kept), never a Facts value.

// assertGitlinkRefused asserts Inspect refuses with an error naming submodules
// and returns no Facts at all.
func assertGitlinkRefused(t *testing.T, r *fixtureRepo, w *fixtureWT) {
	t.Helper()
	facts, err := Inspect(context.Background(), r.Repo(), entryFor(t, r, w))
	if err == nil {
		t.Fatalf("Inspect = %+v, nil, want an error for a worktree holding a submodule/gitlink (entry kept)", facts)
	}
	if !strings.Contains(err.Error(), "submodule") {
		t.Errorf("err = %q, want it to name submodules/gitlinks", err)
	}
	if facts != (Facts{}) {
		t.Errorf("facts = %+v, want none for a refused worktree", facts)
	}
}

func TestInspectRefusesAWorktreeWithARealSubmodule(t *testing.T) {
	r := newRepo(t, "hostrepo")
	source := newRepo(t, "subsource")
	submoduleGit(t, r.Root, "submodule", "add", "-q", source.Root, "sub")
	r.git(t, "commit", "-q", "-m", "add the submodule")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	submoduleGit(t, w.Path, "submodule", "update", "--init", "-q")

	assertGitlinkRefused(t, r, w)
}

func TestInspectRefusesABareGitlinkWithoutGitmodules(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	w.git(t, "update-index", "--add", "--cacheinfo", "160000,"+w.head(t)+",sub")
	runGit(t, w.Path, "init", "-q", "sub")

	assertGitlinkRefused(t, r, w)
}

func TestInspectRefusesAGitlinkPresentOnlyInHeadsTree(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	w.git(t, "update-index", "--add", "--cacheinfo", "160000,"+w.head(t)+",sub")
	w.git(t, "commit", "-q", "-m", "add a gitlink")
	// Staged deletion: the index no longer holds the gitlink, but HEAD's tree
	// still does -- and --ignore-submodules=all hides a staged gitlink change.
	w.git(t, "rm", "--cached", "-q", "sub")
	if out := w.git(t, "ls-files", "--stage", "sub"); out != "" {
		t.Fatalf("fixture error: index still lists sub: %q", out)
	}

	assertGitlinkRefused(t, r, w)
}

func TestInspectNeverRunsASubmodulesConfiguredFilter(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	sub := filepath.Join(w.Path, "sub")
	runGit(t, w.Path, "init", "-q", "sub")
	writeFile(t, filepath.Join(sub, "file.txt"), "content\n")
	runGit(t, sub, "add", "file.txt")
	runGit(t, sub, "commit", "-q", "-m", "init sub")
	w.git(t, "update-index", "--add", "--cacheinfo", "160000,"+runGit(t, sub, "rev-parse", "HEAD")+",sub")

	// The attack: the submodule's own config and attributes (written after the
	// commit so the setup itself never runs the filter) clean-filter a file whose
	// stat then differs from the submodule's index.
	script, marker := markerScript(t, r, "sub-filter", "cat")
	runGit(t, sub, "config", "filter.x.clean", script)
	writeFile(t, filepath.Join(sub, ".gitattributes"), "* filter=x\n")
	setMtime(t, filepath.Join(sub, "file.txt"), time.Unix(1609459200, 0))

	facts, err := Inspect(context.Background(), r.Repo(), entryFor(t, r, w))
	if err == nil {
		t.Fatalf("Inspect = %+v, nil, want an error for a worktree holding a submodule", facts)
	}
	assertNotRan(t, marker, "the submodule's own clean filter")

	// Control: a plain status in the worktree starts the submodule's child
	// status, which runs the filter.
	w.git(t, "status", "--porcelain")
	assertRan(t, marker, "the submodule's own clean filter")
}

// --- configured commands ---

func TestInspectRefusesAWorktreeWithAConfiguredFilterWithoutRunningIt(t *testing.T) {
	for _, key := range []string{"clean", "process"} {
		t.Run("filter.evil."+key, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			// A tracked file whose stat differs from the index but whose content is
			// unchanged: git status must read it, which runs its clean filter.
			writeFile(t, filepath.Join(w.Path, ".gitattributes"), "tracked.txt filter=evil\n")
			setMtime(t, filepath.Join(w.Path, "tracked.txt"), time.Unix(1609459200, 0))
			script, marker := markerScript(t, r, "filter-"+key, "cat")
			r.git(t, "config", "filter.evil."+key, script)
			e := entryFor(t, r, w)

			facts, err := Inspect(context.Background(), r.Repo(), e)
			if err == nil {
				t.Fatalf("Inspect = %+v, nil, want an error for a repo-configured filter (entry kept)", facts)
			}
			if !strings.Contains(err.Error(), "filter") {
				t.Errorf("err = %q, want it to name the filter configuration", err)
			}
			assertNotRan(t, marker, "the configured filter command")

			w.git(t, "status", "--porcelain")
			assertRan(t, marker, "the configured filter command")
		})
	}
}

func TestInspectToleratesAGlobalScopeFilterButNotALocalOne(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	global := filepath.Join(r.Tmp, "global-gitconfig")
	writeFile(t, global, "[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n\tprocess = git-lfs filter-process\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	e := entryFor(t, r, w)

	if facts, err := Inspect(context.Background(), r.Repo(), e); err != nil {
		t.Fatalf("Inspect with only a global-scope filter: %v (%+v), want the host's own config tolerated", err, facts)
	}

	r.git(t, "config", "filter.lfs.clean", "git-lfs clean -- %f")
	if facts, err := Inspect(context.Background(), r.Repo(), e); err == nil {
		t.Errorf("Inspect = %+v, nil, want an error once the same filter is configured locally", facts)
	}
}

func TestInspectWorktreeScopeFilterIsRefused(t *testing.T) {
	r := newRepo(t, "hostrepo")
	r.git(t, "config", "extensions.worktreeConfig", "true")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	w.git(t, "config", "--worktree", "filter.evil.clean", "true")

	if facts, err := Inspect(context.Background(), r.Repo(), entryFor(t, r, w)); err == nil {
		t.Errorf("Inspect = %+v, nil, want an error for a worktree-scope filter", facts)
	}
}

func TestInspectFilterProbeFailureIsAnError(t *testing.T) {
	cases := []struct {
		name  string
		shim  string
		wantI string
	}{
		{"config exits 2", `echo 'fatal: bad config' >&2; exit 2`, "bad config"},
		{"no match but noise on stderr", `echo 'warning: odd' >&2; exit 1`, "odd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			e := entryFor(t, r, w)
			// Only the filter probe (the one asking for scopes) is intercepted: the
			// snapshot's own config probe (--file) must reach the real git, or this
			// would test that probe instead.
			shimGit(t, fmt.Sprintf(`for a in "$@"; do
  if [ "$a" = --show-scope ]; then %s; fi
done`, tc.shim))

			facts, err := Inspect(context.Background(), r.Repo(), e)
			if err == nil {
				t.Fatalf("Inspect = %+v, nil, want an error when the filter probe does not cleanly report no match", facts)
			}
			if !strings.Contains(err.Error(), tc.wantI) {
				t.Errorf("err = %q, want git's diagnostic %q kept", err, tc.wantI)
			}
			if !strings.Contains(err.Error(), "filter configuration") {
				t.Errorf("err = %q, want it to come from the filter probe", err)
			}
		})
	}
}

// The snapshot's own config probe (`git config --file ... --get-regexp`) has the
// same fail-closed rule: only exit 1 with no output at all is a clean no-match.
func TestInspectSnapshotConfigProbeFailureIsAnError(t *testing.T) {
	cases := []struct {
		name  string
		shim  string
		wantI string
	}{
		{"config exits 2", `echo 'fatal: bad config' >&2; exit 2`, "bad config"},
		{"no match but noise on stderr", `echo 'warning: odd' >&2; exit 1`, "odd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			e := entryFor(t, r, w)
			parent := useSnapshotParent(t)

			// Control: with the real git the untouched fixture inspects fine, so the
			// refusals below come from the probe's outcome.
			mustInspect(t, r, e)

			shimGit(t, fmt.Sprintf(`for a in "$@"; do
  if [ "$a" = --file ]; then %s; fi
done`, tc.shim))
			facts, err := inspectWT(t, r, e)
			assertRefused(t, facts, err, "check the snapshot's config", parent)
			if !strings.Contains(err.Error(), tc.wantI) {
				t.Errorf("err = %q, want git's diagnostic %q kept", err, tc.wantI)
			}
		})
	}

	t.Run("exit 1 with no output passes", func(t *testing.T) {
		r := newRepo(t, "hostrepo")
		w := addWorktree(t, r, "1-x", "feature/1-x")
		e := entryFor(t, r, w)
		shimGit(t, `for a in "$@"; do
  if [ "$a" = --file ]; then exit 1; fi
done`)
		if facts, err := inspectWT(t, r, e); err != nil {
			t.Errorf("Inspect = %+v, %v, want a clean no-match (exit 1, no output) to pass", facts, err)
		}
	})
}

// signedCommit adds a commit carrying a gpgsig header with a fake armored
// signature of the given kind ("PGP" or "SSH"; nothing but a log that shows
// signatures ever reads it) on the worktree's branch.
func signedCommit(t *testing.T, w *fixtureWT, kind string) {
	t.Helper()
	tree := w.git(t, "rev-parse", "HEAD^{tree}")
	body := fmt.Sprintf("tree %s\nparent %s\nauthor Fixture <fixture@example.invalid> %d +0000\n"+
		"committer Fixture <fixture@example.invalid> %d +0000\n"+
		"gpgsig -----BEGIN %s SIGNATURE-----\n \n fakefakefake\n -----END %s SIGNATURE-----\n\nsigned\n",
		tree, w.head(t), oldCommitDate, oldCommitDate, kind, kind)
	file := filepath.Join(w.R.Tmp, "signed-commit.txt")
	writeFile(t, file, body)
	sha := w.git(t, "hash-object", "-t", "commit", "-w", file)
	w.git(t, "update-ref", "HEAD", sha)
}

func TestInspectNeverRunsAConfiguredSignatureProgram(t *testing.T) {
	for _, tc := range []struct{ key, kind string }{{"gpg.program", "PGP"}, {"gpg.ssh.program", "SSH"}} {
		key := tc.key
		t.Run(key, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			w := addWorktree(t, r, "1-x", "feature/1-x")
			signedCommit(t, w, tc.kind)
			script, marker := markerScript(t, r, "gpg", "exit 1")
			r.git(t, "config", "log.showSignature", "true")
			// ssh verification refuses to start without an allowed-signers file.
			signers := filepath.Join(r.Tmp, "allowed-signers")
			writeFile(t, signers, "fixture@example.invalid ssh-ed25519 AAAA\n")
			r.git(t, "config", "gpg.ssh.allowedSignersFile", signers)
			r.git(t, "config", key, script)
			e := entryFor(t, r, w)

			facts, err := Inspect(context.Background(), r.Repo(), e)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if facts.LastActivity.IsZero() {
				t.Error("LastActivity is zero, want the signed commit's date to have been read")
			}
			assertNotRan(t, marker, key)

			w.git(t, "log", "-1", "--format=%ct")
			assertRan(t, marker, key)
		})
	}
}

func TestInspectIgnoresAmbientGitTraceAndConfigEnvironment(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	script, marker := markerScript(t, r, "ambient-filter", "cat")
	traces := map[string]string{
		"GIT_TRACE":             filepath.Join(r.Tmp, "trace.out"),
		"GIT_TRACE2":            filepath.Join(r.Tmp, "trace2.out"),
		"GIT_TRACE2_EVENT":      filepath.Join(r.Tmp, "trace2-event.out"),
		"GIT_TRACE_PERFORMANCE": filepath.Join(r.Tmp, "trace-perf.out"),
	}
	for k, v := range traces {
		t.Setenv(k, v)
	}
	// Each of these would make a filter appear at command scope (the probe would
	// refuse) or make git fail outright.
	t.Setenv("GIT_CONFIG_PARAMETERS", "'filter.evil.clean'='"+script+"'")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "filter.evil2.clean")
	t.Setenv("GIT_CONFIG_VALUE_0", script)
	t.Setenv("GIT_ATTR_SOURCE", "refs/heads/does-not-exist")

	facts, err := Inspect(context.Background(), r.Repo(), e)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if facts.Branch != "feature/1-x" || facts.Dirty || facts.Untracked {
		t.Errorf("facts = %+v, want the real clean worktree", facts)
	}
	assertNotRan(t, marker, "an ambient-config filter")
	for k, p := range traces {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s=%s was honored (Lstat err = %v), want ambient tracing stripped", k, p, err)
		}
	}
}

// --- the Repo argument ---

func TestInspectRejectsARepoThatIsNotTheResolvedMainCheckout(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	other := newRepo(t, "otherrepo")
	e := entryFor(t, r, w)

	// Control: the genuine Repo inspects fine, so the refusals below come from
	// the validation.
	if _, err := Inspect(context.Background(), r.Repo(), e); err != nil {
		t.Fatalf("control Inspect: %v", err)
	}
	cases := []struct {
		name string
		repo Repo
	}{
		{"zero Repo", Repo{}},
		{"relative MainRoot", Repo{MainRoot: "hostrepo", CommonDir: filepath.Join("hostrepo", ".git")}},
		{"unclean MainRoot", Repo{MainRoot: r.Root + "/../hostrepo", CommonDir: r.CommonDir}},
		{"trailing slash MainRoot", Repo{MainRoot: r.Root + "/", CommonDir: r.CommonDir}},
		{"relative CommonDir", Repo{MainRoot: r.Root, CommonDir: ".git"}},
		{"unclean CommonDir", Repo{MainRoot: r.Root, CommonDir: r.CommonDir + "/."}},
		{"CommonDir of another repo", Repo{MainRoot: r.Root, CommonDir: other.CommonDir}},
		{"CommonDir not named .git under MainRoot", Repo{MainRoot: r.Root, CommonDir: filepath.Join(r.Root, ".worktrees", "1-x")}},
		{"MainRoot of another repo", Repo{MainRoot: other.Root, CommonDir: r.CommonDir}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts, err := Inspect(context.Background(), tc.repo, e)
			if err == nil {
				t.Fatalf("Inspect(%+v) = %+v, nil, want an error", tc.repo, facts)
			}
			if facts != (Facts{}) {
				t.Errorf("facts = %+v, want none for a refused Repo", facts)
			}
		})
	}
}

// --- bounds and partial facts ---

func TestInspectSizeWalkIsBoundedByItsTimeout(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	setVar(t, &sizeTimeout, time.Nanosecond)

	_, err := Inspect(context.Background(), r.Repo(), e)
	if !errors.Is(err, errTimeout) {
		t.Errorf("err = %v, want errors.Is(_, errTimeout): an expired size walk must not report a partial size", err)
	}
}

func TestInspectSizeWalkOverTheEntryCapIsAnError(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	setVar(t, &maxSizeEntries, 2)

	_, err := Inspect(context.Background(), r.Repo(), e)
	if !errors.Is(err, errOutputTruncated) {
		t.Errorf("err = %v, want errors.Is(_, errOutputTruncated): an unfinished size walk must keep the entry", err)
	}
}

func TestInspectSizeWalkAtTheEntryCapIsNotAnError(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	total := 0
	if err := filepath.WalkDir(w.Path, func(string, os.DirEntry, error) error { total++; return nil }); err != nil {
		t.Fatal(err)
	}
	setVar(t, &maxSizeEntries, total)

	if _, err := Inspect(context.Background(), r.Repo(), e); err != nil {
		t.Errorf("err = %v, want nil for exactly %d entries (the cap)", err, total)
	}
}

func TestDiskUsageOfAVanishedRootIsAnError(t *testing.T) {
	// Unreachable through Inspect (mapEntry checked the directory a moment
	// earlier), so this is the direct check that a root deleted mid-inspection
	// can never read as a size of zero.
	gone := filepath.Join(realPath(t, t.TempDir()), "gone")
	if size, err := diskUsage(context.Background(), gone); err == nil {
		t.Errorf("diskUsage(vanished root) = %d, nil, want an error", size)
	}
}

func TestInspectLaterStageFailureReturnsEarlierFactsWithTheError(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	setVar(t, &sizeTimeout, time.Nanosecond)

	facts, err := Inspect(context.Background(), r.Repo(), e)
	if err == nil {
		t.Fatal("Inspect returned nil, want the size stage's error")
	}
	if facts.Branch != "feature/1-x" || facts.HeadSHA != w.head(t) {
		t.Errorf("facts = %+v, want the status-derived Branch and HeadSHA kept alongside the error", facts)
	}
}

// --- the private snapshot git dir (#1278) ---

// argvCall is one git invocation recorded by the argv-logging shim.
type argvCall struct {
	args []string
	// gitDirs are the values of every --git-dir= argument.
	gitDirs []string
	// sub is the git subcommand: the argument right after --work-tree=<path>.
	sub string
}

func (c argvCall) has(arg string) bool {
	for _, a := range c.args {
		if a == arg {
			return true
		}
	}
	return false
}

// shimGit shadows git with a shim that runs prelude and then execs the real git
// with the original arguments. The real git is resolved before the shim hides it.
func shimGit(t *testing.T, prelude string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	installShim(t, "git", fmt.Sprintf("%s\nexec '%s' \"$@\"", prelude, realGit))
}

// logGitArgv shadows git with a shim that appends each call's argv to a log and
// then execs the real git, and returns a func that reads the log back.
func logGitArgv(t *testing.T, r *fixtureRepo, workTree string) func() []argvCall {
	t.Helper()
	argvLog := filepath.Join(r.Tmp, "git-argv.log")
	shimGit(t, fmt.Sprintf(`{ for a in "$@"; do printf '%%s\037' "$a"; done; printf '\n'; } >> '%s'`, argvLog))
	return func() []argvCall {
		t.Helper()
		data, err := os.ReadFile(argvLog)
		if err != nil {
			t.Fatalf("read the git argv log: %v", err)
		}
		var calls []argvCall
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			c := argvCall{args: strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f")}
			for i, a := range c.args {
				if gd, ok := strings.CutPrefix(a, "--git-dir="); ok {
					c.gitDirs = append(c.gitDirs, gd)
				}
				if a == "--work-tree="+workTree && i+1 < len(c.args) {
					c.sub = c.args[i+1]
				}
			}
			calls = append(calls, c)
		}
		return calls
	}
}

// AC1: every git process Inspect starts uses one private snapshot as --git-dir
// and the mapped worktree as --work-tree, never the live admin or common dir.
func TestInspectPointsEveryGitCallAtOnePrivateSnapshotGitDir(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	parent := useSnapshotParent(t)
	readLog := logGitArgv(t, r, w.Path)

	if _, err := Inspect(context.Background(), r.Repo(), e); err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	calls := readLog()
	if len(calls) == 0 {
		t.Fatal("the shim logged no git call: it is not the git Inspect runs")
	}
	snapshotDir := ""
	for _, c := range calls {
		if len(c.gitDirs) != 1 || c.sub == "" {
			t.Errorf("git %q: want exactly one --git-dir and a --work-tree=%s, got git-dirs %q and subcommand %q", c.args, w.Path, c.gitDirs, c.sub)
			continue
		}
		gd := c.gitDirs[0]
		switch {
		case gd == w.AdminDir || gd == r.CommonDir:
			t.Errorf("git %s used the live git dir %s, want the private snapshot", c.sub, gd)
		case filepath.Dir(gd) != parent:
			t.Errorf("git %s used --git-dir=%s, want a snapshot directly under %s", c.sub, gd, parent)
		case snapshotDir == "":
			snapshotDir = gd
		case gd != snapshotDir:
			t.Errorf("git %s used --git-dir=%s, want the one snapshot %s for every call", c.sub, gd, snapshotDir)
		}
	}

	covered := map[string]int{}
	for _, c := range calls {
		switch {
		case c.sub == "config" && c.has("--show-scope"):
			covered["filter probe"]++
		case c.sub == "ls-files" && c.has("--stage"):
			covered["gitlink scan of the index"]++
		case c.sub == "ls-files" && c.has("--cached"):
			covered["activity listing"]++
		case c.sub == "status", c.sub == "ls-tree", c.sub == "for-each-ref", c.sub == "log":
			covered[c.sub]++
		}
	}
	for _, name := range []string{"filter probe", "status", "ls-tree", "for-each-ref", "log", "activity listing"} {
		if covered[name] == 0 {
			t.Errorf("no %s call was logged, so its --git-dir is unchecked", name)
		}
	}
	if got := covered["gitlink scan of the index"]; got != 2 {
		t.Errorf("%d gitlink scans of the index were logged, want 2 (before and after status)", got)
	}
}

// AC2: configuration written into the live repo after the snapshot is built
// must not reach any git read. The hook adds a clean filter plus the
// .gitattributes that selects it; tracked.txt is stat-dirty, so a git that read
// the live config would run the filter while checking it.
func TestInspectIgnoresAFilterAddedToTheLiveConfigAfterTheSnapshotIsBuilt(t *testing.T) {
	for _, tc := range []struct {
		name          string
		worktreeScope bool
	}{
		{"common config", false},
		{"worktree config", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			if tc.worktreeScope {
				r.git(t, "config", "extensions.worktreeConfig", "true")
			}
			w := addWorktree(t, r, "1-x", "feature/1-x")
			setMtime(t, filepath.Join(w.Path, "tracked.txt"), time.Unix(1609459200, 0))
			script, marker := markerScript(t, r, "late-filter", "cat")
			e := entryFor(t, r, w)
			useSnapshotParent(t)
			fired := onSnapshot(t, func(string) {
				if tc.worktreeScope {
					w.git(t, "config", "--worktree", "filter.x.clean", script)
				} else {
					r.git(t, "config", "filter.x.clean", script)
				}
				writeFile(t, filepath.Join(w.Path, ".gitattributes"), "tracked.txt filter=x\n")
			})

			facts, err := Inspect(context.Background(), r.Repo(), e)
			requireSnapshotBuilt(t, fired)
			if err != nil {
				t.Fatalf("Inspect: %v (%+v), want the late filter to be invisible to it", err, facts)
			}
			if facts.Branch != "feature/1-x" || facts.HeadSHA != w.head(t) {
				t.Errorf("facts = %+v, want Branch feature/1-x and the worktree's HeadSHA", facts)
			}
			assertNotRan(t, marker, "the filter added after the snapshot")

			// Control: the same live state does run the filter under a plain git.
			w.git(t, "status", "--porcelain")
			assertRan(t, marker, "the filter added after the snapshot")
		})
	}
}

// AC4: an include.* or includeIf.* key in the copied local or worktree config is
// refused (git would read the included file live again), without reading it.
func TestInspectRefusesIncludesInTheConfigWithoutReadingThem(t *testing.T) {
	cases := []struct {
		name          string
		worktreeScope bool
		set           func(t *testing.T, r *fixtureRepo, w *fixtureWT, inc string)
	}{
		{"path in the local config", false, func(t *testing.T, r *fixtureRepo, _ *fixtureWT, inc string) {
			r.git(t, "config", "include.path", inc)
		}},
		{"conditional include in the local config", false, func(t *testing.T, r *fixtureRepo, _ *fixtureWT, inc string) {
			r.git(t, "config", "includeIf.gitdir:/.path", inc)
		}},
		{"path in the worktree config", true, func(t *testing.T, _ *fixtureRepo, w *fixtureWT, inc string) {
			w.git(t, "config", "--worktree", "include.path", inc)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "hostrepo")
			if tc.worktreeScope {
				r.git(t, "config", "extensions.worktreeConfig", "true")
			}
			w := addWorktree(t, r, "1-x", "feature/1-x")
			writeFile(t, filepath.Join(w.Path, ".gitattributes"), "tracked.txt filter=x\n")
			setMtime(t, filepath.Join(w.Path, "tracked.txt"), time.Unix(1609459200, 0))
			// The script path lands in error text; keep the refusal marker out of it.
			script, marker := markerScript(t, r, "extra-filter", "cat")
			inc := filepath.Join(r.Tmp, "extra.gitconfig")
			writeFile(t, inc, "[filter \"x\"]\n\tclean = "+script+"\n")
			tc.set(t, r, w, inc)
			e := entryFor(t, r, w)
			parent := useSnapshotParent(t)

			facts, err := Inspect(context.Background(), r.Repo(), e)
			assertRefused(t, facts, err, "include", r.Tmp, parent)
			assertNotRan(t, marker, "the filter defined by the included file")

			// Control: a plain git follows the include and runs the filter.
			w.git(t, "status", "--porcelain")
			assertRan(t, marker, "the filter defined by the included file")
		})
	}

	// The refusal is for the include itself: an included file with nothing
	// dangerous in it is refused too, and not for a filter (which the filter
	// probe would already catch).
	t.Run("an include of a harmless file", func(t *testing.T) {
		r := newRepo(t, "hostrepo")
		w := addWorktree(t, r, "1-x", "feature/1-x")
		inc := filepath.Join(r.Tmp, "extra.gitconfig")
		writeFile(t, inc, "[user]\n\tname = Extra\n")
		r.git(t, "config", "include.path", inc)
		e := entryFor(t, r, w)
		parent := useSnapshotParent(t)

		facts, err := Inspect(context.Background(), r.Repo(), e)
		assertRefused(t, facts, err, "include", r.Tmp, parent)
	})
}

// gitProbeShim shadows git so that any call carrying --file (the snapshot
// builder's probe of its own config copies) answers with stdout and exit 0, and
// every other call goes to the real git.
func gitProbeShim(t *testing.T, stdout string) {
	t.Helper()
	shimGit(t, fmt.Sprintf(`for a in "$@"; do
  if [ "$a" = --file ]; then printf '%s\n'; exit 0; fi
done`, stdout))
}

// AC5: ref storage other than `files` is refused. The shim pins the check's own
// message on any git version; the real reftable repo needs git >= 2.45.
func TestInspectRefusesRefStorageOtherThanFiles(t *testing.T) {
	t.Run("a probe that reports reftable", func(t *testing.T) {
		r := newRepo(t, "hostrepo")
		w := addWorktree(t, r, "1-x", "feature/1-x")
		e := entryFor(t, r, w)
		parent := useSnapshotParent(t)
		gitProbeShim(t, "extensions.refstorage reftable")

		facts, err := Inspect(context.Background(), r.Repo(), e)
		assertRefused(t, facts, err, "refstorage", r.Tmp, parent)
	})

	t.Run("a probe that reports files is accepted", func(t *testing.T) {
		r := newRepo(t, "hostrepo")
		w := addWorktree(t, r, "1-x", "feature/1-x")
		e := entryFor(t, r, w)
		useSnapshotParent(t)
		gitProbeShim(t, "extensions.refstorage files")

		facts, err := Inspect(context.Background(), r.Repo(), e)
		if err != nil {
			t.Fatalf("Inspect: %v, want extensions.refStorage=files accepted (the check refuses exactly the non-files case)", err)
		}
		if facts.Branch != "feature/1-x" {
			t.Errorf("facts = %+v, want the worktree's real facts", facts)
		}
	})

	t.Run("a real reftable repository", func(t *testing.T) {
		isolateGit(t)
		tmp := realPath(t, t.TempDir())
		root := filepath.Join(tmp, "hostrepo")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := tryGit(root, oldCommitDate, "init", "-q", "-b", "main", "--ref-format=reftable"); err != nil {
			t.Skipf("git without reftable support (needs >= 2.45): %v", err)
		}
		writeFile(t, filepath.Join(root, "tracked.txt"), "tracked\n")
		runGit(t, root, "add", "-A")
		runGit(t, root, "commit", "-q", "-m", "init")
		r := &fixtureRepo{Tmp: tmp, Root: root, CommonDir: filepath.Join(root, ".git")}
		w := addWorktree(t, r, "1-x", "feature/1-x")
		e := entryFor(t, r, w)
		parent := useSnapshotParent(t)

		facts, err := Inspect(context.Background(), r.Repo(), e)
		assertRefused(t, facts, err, "refstorage", r.Tmp, parent)
	})
}

// AC9: Inspect writes nothing to the live repo. The control, in the same
// fixture, shows a plain git status does move the index mtime, so the check can
// fail.
func TestInspectLeavesTheLiveIndexMtimeAndConfigUntouched(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	// Stat-dirty: the content matches the index but the stat does not, which is
	// what makes a plain git status rewrite the index.
	setMtime(t, filepath.Join(w.Path, "tracked.txt"), time.Unix(1609459200, 0))
	index := filepath.Join(w.AdminDir, "index")
	setMtime(t, index, time.Unix(1609545600, 0))
	e := entryFor(t, r, w)
	useSnapshotParent(t)
	configPath := filepath.Join(r.CommonDir, "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.Lstat(index)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Inspect(context.Background(), r.Repo(), e); err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	indexAfter, err := os.Lstat(index)
	if err != nil {
		t.Fatal(err)
	}
	if indexAfter.ModTime().UnixNano() != indexBefore.ModTime().UnixNano() {
		t.Errorf("admin index mtime = %d, want the unchanged %d", indexAfter.ModTime().UnixNano(), indexBefore.ModTime().UnixNano())
	}
	if configAfter, err := os.ReadFile(configPath); err != nil || string(configAfter) != string(configBefore) {
		t.Errorf("common config changed (read err %v), want it byte-identical", err)
	}

	w.git(t, "status", "--porcelain")
	controlAfter, err := os.Lstat(index)
	if err != nil {
		t.Fatal(err)
	}
	if controlAfter.ModTime().UnixNano() == indexBefore.ModTime().UnixNano() {
		t.Error("control: a plain git status left the index mtime alone, so the unchanged-mtime check above cannot fail")
	}
}

// Racy git: an entry whose mtime is not older than the index file's own mtime
// cannot be trusted on stat alone, so git compares content. The snapshot's index
// copy must keep the source mtime; a fresh "now" mtime would make such an entry
// look settled and a same-size edit would read as clean (a missed dirty file).
// ctime is ignored (core.trustctime=false) so the edit is invisible to stat.
func TestInspectReportsASameSizeEditInTheIndexsOwnTimestampWindowAsDirty(t *testing.T) {
	r := newRepo(t, "hostrepo")
	r.git(t, "config", "core.trustctime", "false")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	tracked := filepath.Join(w.Path, "tracked.txt")
	index := filepath.Join(w.AdminDir, "index")
	window := time.Unix(1609459200, 0)
	setMtime(t, tracked, window)
	w.git(t, "update-index", "--refresh")
	writeFile(t, tracked, "trackeD\n") // same size and inode as "tracked\n"
	setMtime(t, tracked, window)
	e := entryFor(t, r, w)
	useSnapshotParent(t)
	liveStatus := func() string { return w.git(t, "--no-optional-locks", "status", "--porcelain") }

	// Control 1: with an index written after the entry's mtime the edit hides
	// behind the matching stat -- the failure mode a fresh index mtime would cause.
	setMtime(t, index, window.Add(time.Hour))
	if got := liveStatus(); got != "" {
		t.Fatalf("control: status = %q with a settled index, want clean (the edit hidden behind its stat)", got)
	}
	// Control 2: in the entry's own timestamp window git must compare content.
	setMtime(t, index, window)
	if got := liveStatus(); !strings.Contains(got, "tracked.txt") {
		t.Fatalf("control: status = %q in the racy window, want tracked.txt modified", got)
	}

	facts, err := Inspect(context.Background(), r.Repo(), e)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !facts.Dirty {
		t.Errorf("facts = %+v, want Dirty: the same-size edit made in the index's timestamp window must not read as clean", facts)
	}
}

// A status output that parses to an error is a refusal like any other: no Facts,
// never the half-parsed ones.
func TestInspectMalformedStatusReturnsNoFacts(t *testing.T) {
	r := newRepo(t, "hostrepo")
	w := addWorktree(t, r, "1-x", "feature/1-x")
	e := entryFor(t, r, w)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A valid branch.head before a malformed branch.oid: parseStatus has already
	// filled Branch when it fails.
	installShim(t, "git", fmt.Sprintf(`for a in "$@"; do
  if [ "$a" = status ]; then printf '# branch.head feature/1-x\0# branch.oid not-a-sha\0'; exit 0; fi
done
exec '%s' "$@"`, realGit))

	facts, err := Inspect(context.Background(), r.Repo(), e)
	if err == nil {
		t.Fatalf("Inspect = %+v, nil, want the parse error", facts)
	}
	if !strings.Contains(err.Error(), "malformed branch.oid") {
		t.Errorf("err = %q, want the parse error kept", err)
	}
	if facts != (Facts{}) {
		t.Errorf("facts = %+v, want none for a refusal", facts)
	}
}
