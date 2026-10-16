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
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			installShim(t, "git", fmt.Sprintf(`for a in "$@"; do
  if [ "$a" = config ]; then %s; fi
done
exec '%s' "$@"`, tc.shim, realGit))

			facts, err := Inspect(context.Background(), r.Repo(), e)
			if err == nil {
				t.Fatalf("Inspect = %+v, nil, want an error when the filter probe does not cleanly report no match", facts)
			}
			if !strings.Contains(err.Error(), tc.wantI) {
				t.Errorf("err = %q, want git's diagnostic %q kept", err, tc.wantI)
			}
		})
	}
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
