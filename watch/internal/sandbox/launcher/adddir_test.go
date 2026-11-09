package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// -- `cenci open --add-dir` resolution (ticket #737) ----------------------
//
// resolveAddDirs is the single host-side resolution/validation site shared by
// resolveLaunchContext (Launch and DryRun) and auditWithSecrets (Audit). These
// are integration tests against the real filesystem and real git: every
// fixture lives under one resolved temp root so the sensitive-location and
// primary-overlap checks see the same path forms a real launch would.

// addDirFixture is one resolved temp tree: a fake $HOME, a git repo as the
// primary workspace (not under $HOME and not a parent of anything else), and a
// scope computed from that repo exactly as a real launch would.
type addDirFixture struct {
	root  string
	home  string
	repo  string
	other string // an unrelated directory, a valid add-dir target
	scope Scope
}

func newAddDirFixture(t *testing.T) addDirFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	f := addDirFixture{
		root:  root,
		home:  filepath.Join(root, "home"),
		repo:  filepath.Join(root, "work", "app", "repo"),
		other: filepath.Join(root, "elsewhere", "other"),
	}
	for _, d := range []string{f.home, f.repo, f.other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", f.repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	isolateAddDirEnv(t, f.home)
	t.Chdir(f.root)
	f.scope = ComputeScope("claude", "", f.repo, f.home)
	return f
}

// isolateAddDirEnv makes the --add-dir denylist hermetic for one test: HOME is
// home, no ambient XDG, tmux or ssh-agent location can overlap the fixture, and
// the socket dir is pinned to a private 0700 temp dir so socket directory
// resolution never creates or hardens a real /tmp/cenci-<uid>
// (watch/docs/test-isolation.md). The socket dir is a short os.MkdirTemp name,
// not a t.TempDir() child: a long test name would push the path past the Unix
// socket path limit the resolver enforces. MkdirTemp creates it 0700.
//
// The denylist also reads $PATH and the tool-home variables (#1460), so those
// are pinned too: PATH is /usr/bin:/bin plus the directory of the git binary the
// fixtures need, and the tool homes are cleared, so a developer's own PATH
// entries or GOPATH can never make a fixture directory look like a host tool
// location.
func isolateAddDirEnv(t *testing.T, home string) {
	t.Helper()
	socketDir, err := os.MkdirTemp("", "ad")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	path := "/usr/bin:/bin"
	if gitPath, err := exec.LookPath("git"); err == nil {
		path += ":" + filepath.Dir(gitPath)
	}
	t.Setenv("PATH", path)
	for _, name := range addDirToolHomeEnv {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", "")
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("CENCI_SOCKET_DIR", socketDir)
}

// addDirToolHomeEnv are the variables that relocate a developer tool home the
// denylist guards (#1460). They are spelled out here, not read from the
// production guard builder, so a variable the builder forgot to guard cannot
// also be forgotten by the isolation.
var addDirToolHomeEnv = []string{"GOBIN", "GOPATH", "CARGO_HOME", "RUSTUP_HOME", "NVM_DIR", "PYENV_ROOT"}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// wantAddDirUsageError asserts err is a usage error whose single-line message
// names the offending path (%q-quoted) and carries marker, so one failure
// class can never silently collapse into another (error-handling.md #446).
func wantAddDirUsageError(t *testing.T, err error, path, marker string) {
	t.Helper()
	if err == nil {
		t.Fatalf("resolveAddDirs accepted %q, want a usage error containing %q", path, marker)
	}
	if !IsUsage(err) {
		t.Fatalf("error = %T %v, want a *UsageError (exit 2)", err, err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("usage error spans several lines: %q", err.Error())
	}
	if !strings.Contains(err.Error(), marker) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), marker)
	}
	if !strings.Contains(err.Error(), quoted(path)) {
		t.Errorf("error = %q, want it to name the offending path as %s", err.Error(), quoted(path))
	}
}

// quoted renders s exactly as the usage errors' %q verbs do.
func quoted(s string) string { return strconv.Quote(s) }

func TestAddDirResolve_NoFlag_ReturnsNothing(t *testing.T) {
	f := newAddDirFixture(t)
	got, err := resolveAddDirs(nil, f.scope, f.home)
	if err != nil || len(got) != 0 {
		t.Fatalf("resolveAddDirs(nil) = %+v, %v; want nothing", got, err)
	}
}

func TestAddDirResolve_AbsolutePath_MapsToReposSlug(t *testing.T) {
	f := newAddDirFixture(t)
	got, err := resolveAddDirs([]string{f.other}, f.scope, f.home)
	if err != nil {
		t.Fatalf("resolveAddDirs: %v", err)
	}
	want := []addDirMount{{Host: f.other, Container: "/repos/other"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAddDirResolve_RelativePath_ResolvesAgainstHostCwdToAbsoluteBindSource(t *testing.T) {
	f := newAddDirFixture(t)
	t.Chdir(f.repo)
	got, err := resolveAddDirs([]string{"../../../elsewhere/other"}, f.scope, f.home)
	if err != nil {
		t.Fatalf("resolveAddDirs: %v", err)
	}
	if len(got) != 1 || got[0].Host != f.other {
		t.Fatalf("got %+v, want the absolute resolved path %q as the bind source", got, f.other)
	}
}

func TestAddDirResolve_SymlinkToDir_UsesTargetPathAndTargetSlug(t *testing.T) {
	f := newAddDirFixture(t)
	link := filepath.Join(f.root, "shortcut")
	if err := os.Symlink(f.other, link); err != nil {
		t.Fatal(err)
	}
	got, err := resolveAddDirs([]string{link}, f.scope, f.home)
	if err != nil {
		t.Fatalf("resolveAddDirs: %v", err)
	}
	want := addDirMount{Host: f.other, Container: "/repos/other"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want %+v (slug comes from the target's basename, not the link's)", got, want)
	}
}

func TestAddDirResolve_SymlinkIntoSensitiveLocation_RunsChecksOnTheTarget(t *testing.T) {
	f := newAddDirFixture(t)
	ssh := mkdir(t, f.home, ".ssh")
	link := filepath.Join(f.root, "innocent")
	if err := os.Symlink(ssh, link); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAddDirs([]string{link}, f.scope, f.home)
	wantAddDirUsageError(t, err, link, "sensitive host location")
}

func TestAddDirResolve_SymlinkIntoPrimaryRepo_RunsOverlapCheckOnTheTarget(t *testing.T) {
	f := newAddDirFixture(t)
	link := filepath.Join(f.root, "into-repo")
	if err := os.Symlink(f.repo, link); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAddDirs([]string{link}, f.scope, f.home)
	wantAddDirUsageError(t, err, link, "overlaps the primary workspace")
}

func TestAddDirResolve_SameDirBySeveralSpellings_DedupesToOneMount(t *testing.T) {
	f := newAddDirFixture(t)
	link := filepath.Join(f.root, "alias")
	if err := os.Symlink(f.other, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(f.root)
	spellings := []string{
		f.other,
		f.other + "/",
		filepath.Join(f.other, "..", "other"),
		link,
		"elsewhere/other",
	}
	got, err := resolveAddDirs(spellings, f.scope, f.home)
	if err != nil {
		t.Fatalf("resolveAddDirs: %v", err)
	}
	if len(got) != 1 || got[0].Host != f.other {
		t.Fatalf("got %+v, want exactly one mount for %q", got, f.other)
	}
}

func TestAddDirResolve_TwoDirs_KeepsFirstSeenOrder(t *testing.T) {
	f := newAddDirFixture(t)
	b := mkdir(t, f.root, "zeta", "b-dir")
	a := mkdir(t, f.root, "alpha", "a-dir")
	got, err := resolveAddDirs([]string{b, a}, f.scope, f.home)
	if err != nil {
		t.Fatalf("resolveAddDirs: %v", err)
	}
	want := []addDirMount{{b, "/repos/b-dir"}, {a, "/repos/a-dir"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAddDirResolve_SlugOfMixedCaseAndSpacesFollowsSlugify(t *testing.T) {
	f := newAddDirFixture(t)
	dir := mkdir(t, f.root, "elsewhere", "My Notes")
	got, err := resolveAddDirs([]string{dir}, f.scope, f.home)
	if err != nil {
		t.Fatalf("resolveAddDirs: %v", err)
	}
	if len(got) != 1 || got[0].Container != "/repos/my-notes" || got[0].Host != dir {
		t.Fatalf("got %+v, want %q mounted at /repos/my-notes", got, dir)
	}
}

func TestAddDirResolve_SlugEqualToPrimarysSlug_IsAllowed(t *testing.T) {
	f := newAddDirFixture(t)
	sameName := mkdir(t, f.root, "elsewhere", filepath.Base(f.repo))
	got, err := resolveAddDirs([]string{sameName}, f.scope, f.home)
	if err != nil {
		t.Fatalf("resolveAddDirs rejected a dir whose slug equals the primary's (the primary lives at /workspace, not /repos): %v", err)
	}
	if len(got) != 1 || got[0].Container != "/repos/"+Slugify(filepath.Base(f.repo)) {
		t.Fatalf("got %+v", got)
	}
}

func TestAddDirResolve_SiblingsSharingAStringPrefix_AreNotOverlapsOrSensitive(t *testing.T) {
	f := newAddDirFixture(t)
	evilRepo := mkdir(t, filepath.Dir(f.repo), filepath.Base(f.repo)+"-evil")
	sshBackup := mkdir(t, f.home, ".ssh-backup")
	gnupgOld := mkdir(t, f.home, ".gnupg.old")
	got, err := resolveAddDirs([]string{evilRepo, sshBackup, gnupgOld}, f.scope, f.home)
	if err != nil {
		t.Fatalf("component-wise comparison must not treat a string-prefix sibling as an overlap: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %+v, want 3 mounts", got)
	}
}

func TestAddDirResolve_OrdinaryDirInsideHome_IsAllowed(t *testing.T) {
	f := newAddDirFixture(t)
	lib := mkdir(t, f.home, "code", "shared-lib")
	got, err := resolveAddDirs([]string{lib}, f.scope, f.home)
	if err != nil {
		t.Fatalf("a project directory inside $HOME is the normal case, got: %v", err)
	}
	if len(got) != 1 || got[0].Container != "/repos/shared-lib" {
		t.Fatalf("got %+v", got)
	}
}

func TestAddDirResolve_NestedAddDirs_AreAllowed(t *testing.T) {
	f := newAddDirFixture(t)
	inner := mkdir(t, f.other, "inner")
	got, err := resolveAddDirs([]string{f.other, inner}, f.scope, f.home)
	if err != nil {
		t.Fatalf("resolveAddDirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want both nested dirs mounted", got)
	}
}

// -- usage errors: input shape -------------------------------------------

func TestAddDirResolve_EmptyPath_IsUsageError(t *testing.T) {
	f := newAddDirFixture(t)
	_, err := resolveAddDirs([]string{""}, f.scope, f.home)
	wantAddDirUsageError(t, err, "", "non-empty")
}

func TestAddDirResolve_CharactersThatBreakVSyntaxOrInspectParsing_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	for name, path := range map[string]string{
		"colon":     filepath.Join(f.root, "elsewhere", "a:b"),
		"newline":   filepath.Join(f.root, "elsewhere", "a\nb"),
		"tab":       filepath.Join(f.root, "elsewhere", "a\tb"),
		"escape":    filepath.Join(f.root, "elsewhere", "a\x1bb"),
		"bidi":      filepath.Join(f.root, "elsewhere", "a\u202eb"),
		"c1 NEL":    filepath.Join(f.root, "elsewhere", "a\u0085b"),
		"double ::": filepath.Join(f.root, "elsewhere", "a::b"),
	} {
		t.Run(name, func(t *testing.T) {
			// The character check runs before the existence check, so the
			// rejection is about the spelling, not about a missing dir.
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "':' or a non-printable character")
		})
	}
}

func TestAddDirResolve_SymlinkWhoseTargetPathHasAColon_IsUsageError(t *testing.T) {
	f := newAddDirFixture(t)
	target := mkdir(t, f.root, "elsewhere", "bad:name")
	link := filepath.Join(f.root, "clean-looking")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAddDirs([]string{link}, f.scope, f.home)
	wantAddDirUsageError(t, err, link, "':' or a non-printable character")
}

func TestAddDirResolve_NonExistentPath_IsUsageError(t *testing.T) {
	f := newAddDirFixture(t)
	missing := filepath.Join(f.root, "elsewhere", "missing")
	_, err := resolveAddDirs([]string{missing}, f.scope, f.home)
	wantAddDirUsageError(t, err, missing, "does not exist")
}

func TestAddDirResolve_RegularFile_IsUsageError(t *testing.T) {
	f := newAddDirFixture(t)
	file := filepath.Join(f.other, "notes.txt")
	writeFile(t, file, "hello")
	_, err := resolveAddDirs([]string{file}, f.scope, f.home)
	wantAddDirUsageError(t, err, file, "not a directory")
}

func TestAddDirResolve_TwoDirsWithTheSameSlug_IsUsageErrorNamingBoth(t *testing.T) {
	f := newAddDirFixture(t)
	a := mkdir(t, f.root, "one", "shared")
	b := mkdir(t, f.root, "two", "shared")
	_, err := resolveAddDirs([]string{a, b}, f.scope, f.home)
	wantAddDirUsageError(t, err, b, "both mount at")
	if !strings.Contains(err.Error(), quoted(a)) {
		t.Errorf("error = %q, want it to name the first colliding dir %s too", err.Error(), quoted(a))
	}
}

// -- usage errors: primary workspace overlap --------------------------------

func TestAddDirResolve_PrimaryWorkspaceOverlap_IsUsageErrorInBothDirections(t *testing.T) {
	f := newAddDirFixture(t)
	sub := mkdir(t, f.repo, "pkg", "deep")
	for name, path := range map[string]string{
		"primary repo root":      f.repo,
		"subdirectory":           sub,
		"parent of primary repo": filepath.Dir(f.repo),
		"grandparent":            filepath.Dir(filepath.Dir(f.repo)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "overlaps the primary workspace")
		})
	}
}

func TestAddDirResolve_LegacyScope_DirUnderReposWorkspaceIsRejectedAsAlreadyCovered(t *testing.T) {
	f := newAddDirFixture(t)
	plain := mkdir(t, f.root, "not-a-repo")
	legacy := ComputeScope("claude", "", plain, f.home)
	if legacy.WorkspaceScope != "legacy" {
		t.Fatalf("fixture scope = %q, want legacy", legacy.WorkspaceScope)
	}
	under := mkdir(t, f.home, "Repos", "proj")
	_, err := resolveAddDirs([]string{under}, legacy, f.home)
	wantAddDirUsageError(t, err, under, "overlaps the primary workspace")

	ok, err := resolveAddDirs([]string{f.other}, legacy, f.home)
	if err != nil || len(ok) != 1 {
		t.Fatalf("a dir outside ~/Repos must be accepted in legacy scope, got %+v, %v", ok, err)
	}
}

// -- usage errors: sensitive host locations ---------------------------------

func TestAddDirResolve_SensitiveLocations_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	claudeDir := mkdir(t, f.home, ".claude")
	cases := map[string]string{
		"filesystem root":                   "/",
		"home directory":                    f.home,
		"parent of home":                    f.root,
		"ssh directory":                     mkdir(t, f.home, ".ssh"),
		"inside ssh":                        mkdir(t, f.home, ".ssh", "keys"),
		"gnupg directory":                   mkdir(t, f.home, ".gnupg"),
		"inside gnupg":                      mkdir(t, f.home, ".gnupg", "private-keys-v1.d"),
		"directory holding claude creds":    claudeDir,
		"inside a credential directory":     mkdir(t, claudeDir, "projects"),
		"directory holding codex creds":     mkdir(t, f.home, ".codex"),
		"directory holding gh creds":        mkdir(t, f.home, ".config", "gh"),
		"parent of a credential directory":  filepath.Join(f.home, ".config"),
		"directory holding azure creds":     mkdir(t, f.home, ".azure"),
		"directory holding opencode creds":  mkdir(t, f.home, ".local", "share", "opencode"),
		"cenci fleet config directory":      mkdir(t, f.home, ".config", "cenci"),
		"inside cenci fleet config":         mkdir(t, f.home, ".config", "cenci", "sub"),
		"cenci state directory":             mkdir(t, f.home, ".local", "state", "cenci"),
		"cenci install link directory":      mkdir(t, f.home, ".local", "bin"),
		"parent of cenci state and install": filepath.Join(f.home, ".local"),
		"docker client config and socket":   mkdir(t, f.home, ".docker"),
		"inside the docker directory":       mkdir(t, f.home, ".docker", "run"),
		"colima socket directory":           mkdir(t, f.home, ".colima"),
		"orbstack socket directory":         mkdir(t, f.home, ".orbstack"),
		"systemd user unit directory":       mkdir(t, f.home, ".config", "systemd", "user"),
		"parent of the user unit directory": filepath.Join(f.home, ".config", "systemd"),
		"login autostart directory":         mkdir(t, f.home, ".config", "autostart"),
		"user systemd data directory":       mkdir(t, f.home, ".local", "share", "systemd"),
		"inside the user systemd data dir":  mkdir(t, f.home, ".local", "share", "systemd", "user"),
		"launchd agents directory":          mkdir(t, f.home, "Library", "LaunchAgents"),
		"parent of launchd agents":          filepath.Join(f.home, "Library"),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
}

func TestAddDirResolve_CenciSocketDirectory_IsUsageError(t *testing.T) {
	f := newAddDirFixture(t)
	socketDir := mkdir(t, f.root, "run", "cenci-sockets")
	if err := os.Chmod(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CENCI_SOCKET_DIR", socketDir)
	for name, path := range map[string]string{
		"the socket directory":    socketDir,
		"inside socket directory": mkdir(t, socketDir, "x"),
		"parent of the socket":    filepath.Dir(socketDir),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
}

func TestAddDirResolve_XDGRelocatedCenciDirectories_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	xdgConfig := mkdir(t, f.root, "xdg", "config")
	xdgState := mkdir(t, f.root, "xdg", "state")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_STATE_HOME", xdgState)
	for name, path := range map[string]string{
		"relocated fleet config": mkdir(t, xdgConfig, "cenci"),
		"relocated state":        mkdir(t, xdgState, "cenci"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
	// The XDG base itself is not a cenci directory: a sibling of cenci/ is fine.
	if got, err := resolveAddDirs([]string{mkdir(t, xdgConfig, "other-tool")}, f.scope, f.home); err != nil || len(got) != 1 {
		t.Fatalf("a sibling of the relocated cenci dir must be accepted, got %+v, %v", got, err)
	}
}

func TestAddDirResolve_HomeBehindASymlink_StillProtectsItsSensitiveDirs(t *testing.T) {
	f := newAddDirFixture(t)
	realHome := f.home
	linkedHome := filepath.Join(f.root, "home-link")
	if err := os.Symlink(realHome, linkedHome); err != nil {
		t.Fatal(err)
	}
	ssh := mkdir(t, realHome, ".ssh")
	// $HOME is spelled through the link, the target through the real path.
	_, err := resolveAddDirs([]string{ssh}, f.scope, linkedHome)
	wantAddDirUsageError(t, err, ssh, "sensitive host location")
	// And the other way round: the target is spelled through the link.
	_, err = resolveAddDirs([]string{filepath.Join(linkedHome, ".ssh")}, f.scope, realHome)
	wantAddDirUsageError(t, err, filepath.Join(linkedHome, ".ssh"), "sensitive host location")
}

// -- usage errors: linked git worktrees -------------------------------------

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestAddDirResolve_LinkedWorktree_IsRejectedAndHintNamesTheMainCheckout(t *testing.T) {
	f := newAddDirFixture(t)
	mainCheckout := mkdir(t, f.root, "elsewhere", "main-checkout")
	gitIn(t, mainCheckout, "init", "-q")
	gitIn(t, mainCheckout, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(f.root, "elsewhere", "wt")
	gitIn(t, mainCheckout, "worktree", "add", "-q", worktree)

	_, err := resolveAddDirs([]string{worktree}, f.scope, f.home)
	wantAddDirUsageError(t, err, worktree, "linked git worktree")
	if !strings.Contains(err.Error(), "main checkout "+quoted(mainCheckout)) {
		t.Errorf("error = %q, want the hint to name the main checkout as %s", err.Error(), quoted(mainCheckout))
	}

	// The main checkout itself is a normal repo and stays acceptable.
	got, err := resolveAddDirs([]string{mainCheckout}, f.scope, f.home)
	if err != nil || len(got) != 1 {
		t.Fatalf("the main checkout must be accepted, got %+v, %v", got, err)
	}
}

func TestAddDirResolve_DotGitFileGitCannotResolve_IsRejectedWithGenericHint(t *testing.T) {
	f := newAddDirFixture(t)
	dir := mkdir(t, f.root, "elsewhere", "broken-link")
	writeFile(t, filepath.Join(dir, ".git"), "gitdir: /nonexistent/host/path\n")

	_, err := resolveAddDirs([]string{dir}, f.scope, f.home)
	wantAddDirUsageError(t, err, dir, "linked git worktree")
	if strings.Contains(err.Error(), "main checkout \"") {
		t.Errorf("error = %q, want a generic hint (no main checkout can be proven)", err.Error())
	}
}

func TestAddDirResolve_WorktreeRejectedEvenWhenItsMainCheckoutIsNotMounted(t *testing.T) {
	f := newAddDirFixture(t)
	mainCheckout := mkdir(t, f.root, "elsewhere", "m")
	gitIn(t, mainCheckout, "init", "-q")
	gitIn(t, mainCheckout, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(f.root, "elsewhere", "w2")
	gitIn(t, mainCheckout, "worktree", "add", "-q", worktree)

	_, err := resolveAddDirs([]string{worktree, mainCheckout}, f.scope, f.home)
	wantAddDirUsageError(t, err, worktree, "linked git worktree")
}

// -- the extended denylist: system, runtime and autostart locations (#737) ---

// systemDirs are the host directories the agent (root with passwordless sudo
// inside the container) must never get a writable bind of.
var systemDirs = []string{
	"/etc", "/boot", "/root", "/usr", "/bin", "/sbin", "/lib", "/lib64",
	"/proc", "/sys", "/dev", "/run", "/private/etc", "/Library", "/System",
}

// refusedBySomeGuard reports whether any guard of home's denylist refuses a
// target spelled as path (a purely lexical target: no filesystem needed). It
// compares by path string only (refusesByPath): the file-identity half of a
// guard would refuse /root/child or /boot/child through an EACCES Stat on a
// non-root runner, passing vacuously; identity has its own tests. It ignores
// the daemon-socket-directory guard and $TMPDIR: both normally live under /tmp,
// so each would refuse /tmp and mask whether the static /tmp guards do their
// own job.
func refusedBySomeGuard(t *testing.T, home, path string) bool {
	t.Helper()
	t.Setenv("TMPDIR", "")
	guards, err := addDirSensitiveGuards(home)
	if err != nil {
		t.Fatalf("addDirSensitiveGuards: %v", err)
	}
	for _, g := range guards {
		if strings.Contains(g.why, "daemon socket directory") {
			continue
		}
		if g.refusesByPath([]string{path}) {
			return true
		}
	}
	return false
}

func TestAddDirGuards_SystemDirectories_AreRefusedEvenWhenAbsentOnThisHost(t *testing.T) {
	newAddDirFixture(t)
	for _, dir := range systemDirs {
		t.Run(dir, func(t *testing.T) {
			for _, target := range []string{dir, filepath.Join(dir, "child")} {
				if !refusedBySomeGuard(t, "/home/dev", target) {
					t.Errorf("target %q is not refused, want a sensitive-location guard", target)
				}
			}
		})
	}
}

func TestAddDirGuards_DirectoriesWhereReposLegitimatelyLive_AreNotRefused(t *testing.T) {
	newAddDirFixture(t)
	for _, target := range []string{
		"/var/repos/app", "/var/www/app", "/opt/app", "/srv/app", "/srv", "/mnt/work/app", "/mnt", "/home/other/app",
		"/tmp/app", "/private/tmp/app", "/var/tmp/app",
		"/etcetera", "/usr-local/app", "/rootless", "/var/lib2/app", "/var/spooled/app", "/opt/homebrew-old/app",
	} {
		t.Run(target, func(t *testing.T) {
			if refusedBySomeGuard(t, "/home/dev", target) {
				t.Errorf("target %q is refused, want it accepted (repos live there)", target)
			}
		})
	}
}

// coveringDirs are shared parents whose own mount would expose everything in
// them (host tmux/ssh-agent sockets in the tmp dirs, root-run state in /var and
// /opt): the dir and every parent of it are refused, a child is not.
var coveringDirs = []string{"/tmp", "/private/tmp", "/var/tmp", "/var", "/private/var", "/opt"}

func TestAddDirGuards_SharedParentDirectories_AreRefusedButTheirChildrenAreNot(t *testing.T) {
	newAddDirFixture(t)
	for _, dir := range coveringDirs {
		t.Run(dir, func(t *testing.T) {
			for _, refused := range []string{dir, filepath.Dir(dir)} {
				if !refusedBySomeGuard(t, "/home/dev", refused) {
					t.Errorf("target %q is not refused, want the shared directory and its parents refused", refused)
				}
			}
			if child := filepath.Join(dir, "app"); refusedBySomeGuard(t, "/home/dev", child) {
				t.Errorf("target %q is refused, want a project directory inside %s accepted", child, dir)
			}
		})
	}
}

// The shared-parent test above cannot tell forbidCovering from forbidExact for
// every directory: the parent it probes (/ or /var) is refused by another guard
// anyway. Pin the mode itself, on the real guards and on a synthetic one over a
// real directory tree (path and identity halves).
func TestAddDirGuards_CoveringMode_RefusesTheDirAndEveryParentButNoChild(t *testing.T) {
	f := newAddDirFixture(t)
	t.Setenv("TMPDIR", "")
	guards, err := addDirSensitiveGuards("/home/dev")
	if err != nil {
		t.Fatalf("addDirSensitiveGuards: %v", err)
	}
	for _, dir := range coveringDirs {
		found := false
		for _, g := range guards {
			if g.path == dir && g.mode == forbidCovering {
				found = true
			}
		}
		if !found {
			t.Errorf("no forbidCovering guard for %s, want the shared directory guarded as covering", dir)
		}
	}

	shared := mkdir(t, f.root, "shared", "inner")
	guard := addDirGuard{path: shared, mode: forbidCovering, why: "synthetic"}
	for target, want := range map[string]bool{
		shared:                           true,
		filepath.Dir(shared):             true,
		f.root:                           true,
		filepath.Join(shared, "app"):     false,
		filepath.Join(shared, "a", "b"):  false,
		filepath.Join(f.root, "sibling"): false,
	} {
		if got := guard.refusesByPath([]string{target}); got != want {
			t.Errorf("refusesByPath(%q) = %v, want %v", target, got, want)
		}
		if got := guard.refuses([]string{target}); got != want {
			t.Errorf("refuses(%q) = %v, want %v", target, got, want)
		}
	}
}

// overlapDirs are root-run host state directories: the dir, anything inside it
// and its parents are refused. Their parents (/var, /opt, /) are covering
// guards of their own, so only "the dir and inside it" is pinned here.
var overlapDirs = []string{"/var/spool", "/var/lib", "/var/db", "/nix", "/opt/homebrew", "/snap"}

func TestAddDirGuards_HostStateDirectories_AreRefusedWithEverythingInside(t *testing.T) {
	newAddDirFixture(t)
	for _, dir := range overlapDirs {
		t.Run(dir, func(t *testing.T) {
			for _, target := range []string{dir, filepath.Join(dir, "child", "deep")} {
				if !refusedBySomeGuard(t, "/home/dev", target) {
					t.Errorf("target %q is not refused, want a sensitive-location guard", target)
				}
			}
		})
	}
}

func TestAddDirGuards_TmpSocketDirectories_AreRefusedWithEverythingInside(t *testing.T) {
	newAddDirFixture(t)
	// emacsclient --eval runs code in the host's emacs server, whose socket
	// lives in /tmp/emacs<uid>.
	for _, dir := range []string{"/tmp/tmux-" + strconv.Itoa(os.Getuid()), "/tmp/.X11-unix", "/tmp/emacs" + strconv.Itoa(os.Getuid())} {
		t.Run(dir, func(t *testing.T) {
			for _, target := range []string{dir, filepath.Join(dir, "default")} {
				if !refusedBySomeGuard(t, "/home/dev", target) {
					t.Errorf("target %q is not refused, want the host socket directory guarded", target)
				}
			}
		})
	}
}

// The agent is root with sudo, so a root home must not make every project dir
// under it unmountable, while /root itself and the credential directories in it
// stay protected through the $HOME guards. A home strictly inside /root (such as
// /root/me) gets the per-home guards for /root as well, so /root/.ssh and the
// other control and credential directories of /root stay refused while a
// project directory under /root or under the home is accepted. Compared by path
// string only: the identity half of a guard needs a readable /root, which a
// non-root runner has not.
func TestAddDirGuards_RootHome_ExemptsTheRootSystemGuardButStillRefusesRootItself(t *testing.T) {
	newAddDirFixture(t)
	for _, home := range []string{"/root", "/root/me"} {
		if guardedByPath(t, home, "/root/code/x") {
			t.Errorf("home=%s: /root/code/x is refused, want a project dir inside a root home accepted", home)
		}
		if !guardedByPath(t, home, "/root") {
			t.Errorf("home=%s: /root itself is accepted, want it refused", home)
		}
	}
	if !guardedByPath(t, "/home/dev", "/root/code/x") {
		t.Error("home=/home/dev: /root/code/x is accepted, want the /root system guard to apply")
	}
}

// guardedByPath reports whether any guard of home's denylist refuses target by
// path string alone (see the comment above).
func guardedByPath(t *testing.T, home, target string) bool {
	t.Helper()
	guards, err := addDirSensitiveGuards(home)
	if err != nil {
		t.Fatalf("addDirSensitiveGuards: %v", err)
	}
	for _, g := range guards {
		if g.refusesByPath([]string{target}) {
			return true
		}
	}
	return false
}

func TestAddDirGuards_HomeInsideRoot_StillGuardsRootsOwnCredentialAndControlDirectories(t *testing.T) {
	newAddDirFixture(t)
	for _, home := range []string{"/root", "/root/me"} {
		t.Run(home, func(t *testing.T) {
			for _, refused := range []string{
				"/root", "/root/.ssh", "/root/.ssh/keys", "/root/.gnupg",
				"/root/.config/systemd/user", "/root/.config/autostart", "/root/.config/cenci",
				"/root/.docker", "/root/.local/bin",
			} {
				if !guardedByPath(t, home, refused) {
					t.Errorf("home=%s: %s is accepted, want /root's own credential and control directory refused", home, refused)
				}
			}
			if guardedByPath(t, home, "/root/code/x") {
				t.Errorf("home=%s: /root/code/x is refused, want a non-sensitive /root directory accepted", home)
			}
		})
	}
	// The home's own project directories stay usable, its own dot-directories
	// stay refused.
	if guardedByPath(t, "/root/me", "/root/me/code/x") {
		t.Error("home=/root/me: /root/me/code/x is refused, want it accepted")
	}
	if !guardedByPath(t, "/root/me", "/root/me/.ssh") {
		t.Error("home=/root/me: /root/me/.ssh is accepted, want it refused")
	}
	// A home outside /root keeps the blanket /root guard.
	if !guardedByPath(t, "/home/dev", "/root/.ssh") {
		t.Error("home=/home/dev: /root/.ssh is accepted, want it refused")
	}
}

func TestAddDirResolve_ExistingSystemDirectories_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	for _, dir := range systemDirs {
		t.Run(dir, func(t *testing.T) {
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Skipf("%s is not a directory on this host", dir)
			}
			_, err := resolveAddDirs([]string{dir}, f.scope, f.home)
			wantAddDirUsageError(t, err, dir, "sensitive host location")
		})
	}
}

func TestAddDirResolve_RuntimeDirectories_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	xdgRuntime := mkdir(t, f.root, "xdg", "runtime")
	t.Setenv("XDG_RUNTIME_DIR", xdgRuntime)
	for name, path := range map[string]string{
		"the XDG runtime directory":  xdgRuntime,
		"inside the XDG runtime dir": mkdir(t, xdgRuntime, "docker"),
		"parent of the XDG runtime":  filepath.Dir(xdgRuntime),
		// /run/user/<uid> has no guard of its own: /run subsumes it. The lexical
		// system-directory test pins /run and its children on every host.
		"the run directory itself": "/run",
	} {
		t.Run(name, func(t *testing.T) {
			if path == "/run" && !pathExists(path) {
				t.Skipf("%s does not exist on this host", path)
			}
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestAddDirResolve_RelativeXDGRuntimeDir_AddsNoGuard(t *testing.T) {
	f := newAddDirFixture(t)
	t.Setenv("XDG_RUNTIME_DIR", "relative/run")
	if got, err := resolveAddDirs([]string{f.other}, f.scope, f.home); err != nil || len(got) != 1 {
		t.Fatalf("a non-absolute XDG_RUNTIME_DIR must be ignored, got %+v, %v", got, err)
	}
}

func TestAddDirResolve_XDGRelocatedSystemdUnits_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	xdgConfig := mkdir(t, f.root, "xdg", "config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	for name, path := range map[string]string{
		"relocated systemd directory":  mkdir(t, xdgConfig, "systemd"),
		"relocated systemd user units": mkdir(t, xdgConfig, "systemd", "user"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
}

func TestAddDirResolve_XDGRelocatedLoginAutostartLocations_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	xdgConfig := mkdir(t, f.root, "xdg", "config")
	xdgData := mkdir(t, f.root, "xdg", "data")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_DATA_HOME", xdgData)
	for name, path := range map[string]string{
		"relocated autostart directory": mkdir(t, xdgConfig, "autostart"),
		"inside relocated autostart":    mkdir(t, xdgConfig, "autostart", "apps"),
		"relocated data systemd":        mkdir(t, xdgData, "systemd"),
		"inside relocated data systemd": mkdir(t, xdgData, "systemd", "user"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
	// The XDG bases themselves hold unrelated data: a sibling is fine.
	if got, err := resolveAddDirs([]string{mkdir(t, xdgData, "my-project")}, f.scope, f.home); err != nil || len(got) != 1 {
		t.Fatalf("a sibling of the relocated systemd dir must be accepted, got %+v, %v", got, err)
	}
}

// The host tmux server, the ssh-agent and the X server keep their sockets in
// well-known directories. With sudo in the container a writable bind of one is
// host code execution (`sudo tmux -S <socket> new-window <cmd>`).
//
// A socket is exposed by mounting its directory or any ancestor of it, never by
// mounting a directory next to it or inside it, so the directory of a socket
// named by $TMUX or $SSH_AUTH_SOCK is guarded as a covering directory (itself
// and its parents refused, a project inside it accepted). $TMUX_TMPDIR names a
// base, not a socket: tmux creates its socket directory <base>/tmux-<uid>, and
// that directory is guarded with everything inside it.
func TestAddDirResolve_HostSocketDirectoriesNamedByTheEnvironment_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	uid := strconv.Itoa(os.Getuid())
	for name, tc := range map[string]struct {
		env      string
		value    func(dir string) string
		subdir   string
		guarded  func(dir string) string // the directory the guard names
		overlaps bool                    // true: everything inside guarded is refused too
	}{
		"SSH_AUTH_SOCK": {"SSH_AUTH_SOCK", func(dir string) string { return filepath.Join(dir, "agent.4242") }, "ssh-XXXX", func(dir string) string { return dir }, false},
		"TMUX":          {"TMUX", func(dir string) string { return filepath.Join(dir, "default") + ",4242,0" }, "tmux-live", func(dir string) string { return dir }, false},
		"TMUX_TMPDIR":   {"TMUX_TMPDIR", func(dir string) string { return dir }, "tmux-tmpdir", func(dir string) string { return filepath.Join(dir, "tmux-"+uid) }, true},
		"TMPDIR":        {"TMPDIR", func(dir string) string { return dir }, "scratch", func(dir string) string { return dir }, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := mkdir(t, f.root, "sockets", tc.subdir)
			t.Setenv(tc.env, tc.value(dir))
			guarded := mkdir(t, tc.guarded(dir))
			refused := []string{guarded, filepath.Dir(guarded)}
			if tc.overlaps {
				refused = append(refused, mkdir(t, guarded, "inner"))
			}
			for _, path := range refused {
				_, err := resolveAddDirs([]string{path}, f.scope, f.home)
				wantAddDirUsageError(t, err, path, "sensitive host location")
			}
			// Projects live next to and inside a covering directory.
			if !tc.overlaps {
				child := mkdir(t, guarded, "app")
				if got, err := resolveAddDirs([]string{child}, f.scope, f.home); err != nil || len(got) != 1 {
					t.Errorf("a project directory inside %s must be accepted, got %+v, %v", guarded, got, err)
				}
			}
			if tc.env == "TMUX_TMPDIR" {
				sibling := mkdir(t, dir, "app")
				if got, err := resolveAddDirs([]string{sibling}, f.scope, f.home); err != nil || len(got) != 1 {
					t.Errorf("a project directory beside tmux's socket directory must be accepted, got %+v, %v", got, err)
				}
			}
		})
	}
}

// A socket that sits directly in $HOME (a password-manager ssh-agent, `tmux -S
// ~/x.sock`) or $TMUX_TMPDIR=/tmp must not turn every repository into a refusal:
// the home directory is refused, the projects inside it are not.
func TestAddDirResolve_SocketDirectlyInHome_RefusesHomeButNotItsChildren(t *testing.T) {
	f := newAddDirFixture(t)
	project := mkdir(t, f.home, "code", "app")
	for name, env := range map[string]string{
		"SSH_AUTH_SOCK": "SSH_AUTH_SOCK",
		"TMUX":          "TMUX",
	} {
		t.Run(name, func(t *testing.T) {
			value := filepath.Join(f.home, ".bitwarden-ssh-agent.sock")
			if env == "TMUX" {
				value += ",4242,0"
			}
			t.Setenv(env, value)
			_, err := resolveAddDirs([]string{f.home}, f.scope, f.home)
			wantAddDirUsageError(t, err, f.home, "sensitive host location")
			if got, err := resolveAddDirs([]string{project}, f.scope, f.home); err != nil || len(got) != 1 {
				t.Fatalf("a project inside $HOME must be accepted with a socket directly in $HOME, got %+v, %v", got, err)
			}
		})
	}
}

func TestAddDirGuards_TmuxTmpdirIsTmp_RefusesTmuxsSocketDirectoryButNotOtherTmpChildren(t *testing.T) {
	newAddDirFixture(t)
	t.Setenv("TMUX_TMPDIR", "/tmp")
	if !refusedBySomeGuard(t, "/home/dev", "/tmp/tmux-"+strconv.Itoa(os.Getuid())) {
		t.Error("/tmp/tmux-<uid> is accepted, want tmux's socket directory refused")
	}
	if refusedBySomeGuard(t, "/home/dev", "/tmp/app") {
		t.Error("/tmp/app is refused, want a project directory inside $TMUX_TMPDIR=/tmp accepted")
	}
}

func TestAddDirResolve_TmpdirChildren_AreAllowed(t *testing.T) {
	f := newAddDirFixture(t)
	tmp := mkdir(t, f.root, "sockets", "tmp")
	t.Setenv("TMPDIR", tmp)
	child := mkdir(t, tmp, "app")
	got, err := resolveAddDirs([]string{child}, f.scope, f.home)
	if err != nil || len(got) != 1 {
		t.Fatalf("a project directory inside $TMPDIR must be accepted, got %+v, %v", got, err)
	}
}

// ssh and tmux resolve a relative $SSH_AUTH_SOCK / $TMUX_TMPDIR against their
// own working directory, so a set-but-relative value is guarded as the path it
// names from the current directory (the fixture runs in f.root), not ignored.
func TestAddDirResolve_RelativeSSHAuthSockAndTmuxTmpdir_AreGuardedAgainstTheCurrentDirectory(t *testing.T) {
	f := newAddDirFixture(t)
	uid := strconv.Itoa(os.Getuid())
	sshDir := mkdir(t, f.root, "agents")
	tmuxSockets := mkdir(t, f.root, "tmuxrel", "tmux-"+uid)
	t.Setenv("SSH_AUTH_SOCK", "agents/agent.sock")
	t.Setenv("TMUX_TMPDIR", "tmuxrel")
	for _, path := range []string{sshDir, f.root, tmuxSockets, mkdir(t, tmuxSockets, "inner")} {
		_, err := resolveAddDirs([]string{path}, f.scope, f.home)
		wantAddDirUsageError(t, err, path, "sensitive host location")
	}
	for _, path := range []string{mkdir(t, sshDir, "app"), mkdir(t, f.root, "tmuxrel", "app")} {
		if got, err := resolveAddDirs([]string{path}, f.scope, f.home); err != nil || len(got) != 1 {
			t.Errorf("%s must be accepted, got %+v, %v", path, got, err)
		}
	}
}

// $TMUX is set by the tmux server with an absolute path, and the temp and XDG
// variables are absolute by specification: a relative value of those is ignored.
func TestAddDirResolve_RelativeTMUXAndTMPDIRAndEmptySocketValues_AddNoGuard(t *testing.T) {
	f := newAddDirFixture(t)
	t.Setenv("TMUX", "relative/default,1,0")
	t.Setenv("TMPDIR", "relative/tmp")
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("TMUX_TMPDIR", "")
	if got, err := resolveAddDirs([]string{f.other}, f.scope, f.home); err != nil || len(got) != 1 {
		t.Fatalf("relative $TMUX/$TMPDIR and empty socket values must be ignored, got %+v, %v", got, err)
	}
}

// Guards run on every spelling before the already-mounted skip, so whether a
// path is accepted never depends on which spelling of its directory came first.
func TestAddDirResolve_GuardedSpellingOfAnAcceptedDirectory_IsRefusedInEitherOrder(t *testing.T) {
	f := newAddDirFixture(t)
	ssh := mkdir(t, f.home, ".ssh")
	link := filepath.Join(ssh, "link")
	if err := os.Symlink(f.other, link); err != nil {
		t.Fatal(err)
	}
	for name, paths := range map[string][]string{
		"guarded spelling first": {link, f.other},
		"guarded spelling last":  {f.other, link},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs(paths, f.scope, f.home)
			wantAddDirUsageError(t, err, link, "sensitive host location")
		})
	}
}

// -- fail closed: a denylist that cannot be built or evaluated refuses --------

// wantUsageErrorWith asserts err is a one-line usage error containing every
// marker.
func wantUsageErrorWith(t *testing.T, err error, markers ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("resolveAddDirs succeeded, want a usage error containing %q", markers)
	}
	if !IsUsage(err) {
		t.Fatalf("error = %T %v, want a *UsageError (exit 2)", err, err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("usage error spans several lines: %q", err.Error())
	}
	for _, m := range markers {
		if !strings.Contains(err.Error(), m) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), m)
		}
	}
}

func TestAddDirResolve_NonAbsoluteHome_IsUsageErrorWhenDirsAreRequested(t *testing.T) {
	f := newAddDirFixture(t)
	for _, home := range []string{"foo", "", "./home"} {
		t.Run("home="+home, func(t *testing.T) {
			_, err := resolveAddDirs([]string{f.other}, f.scope, home)
			wantUsageErrorWith(t, err, "absolute $HOME", quoted(home))
		})
	}
	// Without --add-dir the home is never consulted.
	if got, err := resolveAddDirs(nil, f.scope, "foo"); err != nil || got != nil {
		t.Fatalf("resolveAddDirs(nil) with a relative home = %+v, %v; want nothing", got, err)
	}
}

func TestAddDirGuard_PathsThatCannotBeCompared_AreAMatchNotAMiss(t *testing.T) {
	for _, mode := range []addDirForbidden{forbidCovering, forbidOverlap} {
		g := addDirGuard{path: "relative/guard", mode: mode}
		if !g.refuses([]string{"/abs/target"}) {
			t.Errorf("mode %d: a guard that cannot be related to the target must refuse it", mode)
		}
	}
	if !pathContains("relative", "/abs") {
		t.Error("pathContains must treat a filepath.Rel error as a match")
	}
}

func TestAddDirResolve_UnresolvableSocketDir_IsUsageErrorNamingTheCause(t *testing.T) {
	f := newAddDirFixture(t)
	t.Setenv("CENCI_SOCKET_DIR", "relative/socket-dir")
	_, err := resolveAddDirs([]string{f.other}, f.scope, f.home)
	wantUsageErrorWith(t, err, "daemon socket directory", "CENCI_SOCKET_DIR", "must be an absolute path")

	// The audit and launch paths without --add-dir never resolve it here.
	if got, err := resolveAddDirs(nil, f.scope, f.home); err != nil || got != nil {
		t.Fatalf("resolveAddDirs(nil) = %+v, %v; want nothing and no socket resolution", got, err)
	}
}

func TestAddDirResolve_UnreadableGitEntry_IsUsageErrorNotAnAssumedNoGit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory search permission")
	}
	f := newAddDirFixture(t)
	dir := mkdir(t, f.root, "elsewhere", "locked")
	// Readable but not searchable: Lstat(dir/.git) fails with EACCES, which
	// is not "there is no .git".
	if err := os.Chmod(dir, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := resolveAddDirs([]string{dir}, f.scope, f.home)
	wantAddDirUsageError(t, err, dir, ".git entry cannot be inspected")
}

// -- file identity: a spelling the string comparison cannot equate -----------

func TestAddDirIdentity_ContainsReachesTheSameDirectoryThroughAnotherSpelling(t *testing.T) {
	f := newAddDirFixture(t)
	real := mkdir(t, f.root, "real")
	sub := mkdir(t, real, "sub")
	link := filepath.Join(f.root, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name          string
		parent, child string
		want          bool
	}{
		{"same directory through a link", real, link, true},
		{"descendant through a link", real, filepath.Join(link, "sub"), true},
		{"deeper descendant through a link", real, filepath.Join(link, "sub", "not", "yet"), true},
		{"link as the parent of a real descendant", link, sub, true},
		{"a parent is not inside its child", sub, real, false},
		{"an unrelated directory", f.other, link, false},
		{"an absent parent", filepath.Join(f.root, "absent"), link, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityContains(tc.parent, tc.child); got != tc.want {
				t.Errorf("identityContains(%q, %q) = %v, want %v", tc.parent, tc.child, got, tc.want)
			}
		})
	}
}

func TestAddDirGuard_MatchesByFileIdentityNotOnlyByPathString(t *testing.T) {
	f := newAddDirFixture(t)
	home := mkdir(t, f.root, "idhome")
	keys := mkdir(t, home, ".ssh", "keys")
	link := filepath.Join(f.root, "home-alias")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	// The target forms are deliberately the unresolved spelling: a path string
	// comparison finds no relation, only file identity does.
	cases := []struct {
		name   string
		guard  addDirGuard
		target string
		want   bool
	}{
		{"overlap, target inside the guard", addDirGuard{path: filepath.Join(home, ".ssh"), mode: forbidOverlap}, filepath.Join(link, ".ssh", "keys"), true},
		{"overlap, target is a parent of the guard", addDirGuard{path: keys, mode: forbidOverlap}, filepath.Join(link, ".ssh"), true},
		{"covering, target is an ancestor of the guard", addDirGuard{path: filepath.Join(home, ".ssh"), mode: forbidCovering}, link, true},
		{"exact, target is the guard", addDirGuard{path: home, mode: forbidExact}, link, true},
		{"covering, target inside the guard stays allowed", addDirGuard{path: home, mode: forbidCovering}, filepath.Join(link, ".ssh"), false},
		{"unrelated target", addDirGuard{path: filepath.Join(home, ".ssh"), mode: forbidOverlap}, f.other, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.guard.refuses([]string{tc.target}); got != tc.want {
				t.Errorf("refuses(%q) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

// A guard path whose identity cannot be read (a Stat error other than "absent")
// might be exactly the target's directory: like pathContains, the identity
// comparison treats it as a match, never as a miss. Only a missing path or a
// non-directory ancestor is skipped.
func TestAddDirGuard_UnreadableGuardPath_IsAMatchNotAMiss(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory search permission")
	}
	f := newAddDirFixture(t)
	locked := mkdir(t, f.root, "locked")
	guardDir := mkdir(t, locked, "guard")
	// Readable but not searchable: Stat(locked/guard) fails with EACCES.
	if err := os.Chmod(locked, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	for _, mode := range []addDirForbidden{forbidExact, forbidCovering, forbidOverlap} {
		g := addDirGuard{path: guardDir, mode: mode}
		if !g.refusesByIdentity([]string{f.other}) {
			t.Errorf("mode %d: a guard path that cannot be stat'ed must refuse, not be skipped", mode)
		}
	}
	if !identityContains(f.other, guardDir) || !sameFile(f.other, guardDir) {
		t.Error("identityContains/sameFile must report a match when a Stat error is not 'absent'")
	}
}

func TestAddDirGuard_AbsentOrNonDirectoryGuardPath_IsNotAMatch(t *testing.T) {
	f := newAddDirFixture(t)
	file := filepath.Join(f.root, "afile")
	writeFile(t, file, "x")
	for name, guardPath := range map[string]string{
		"absent path":                         filepath.Join(f.root, "absent", "guard"),
		"path below a regular file (ENOTDIR)": filepath.Join(file, "guard"),
	} {
		for _, mode := range []addDirForbidden{forbidExact, forbidCovering, forbidOverlap} {
			t.Run(name, func(t *testing.T) {
				g := addDirGuard{path: guardPath, mode: mode}
				if g.refusesByIdentity([]string{f.other}) {
					t.Errorf("mode %d: %q cannot be the target, want no identity match", mode, guardPath)
				}
			})
		}
	}
}

// A case-insensitive filesystem (the macOS default) lets ~/.SSH name the very
// directory ~/.ssh does; no path-string comparison equates them, so this
// pins the file-identity match end to end. Skipped on case-sensitive
// filesystems.
func TestAddDirResolve_CaseVariantOfASensitiveDirectory_IsRefusedOnCaseInsensitiveFilesystems(t *testing.T) {
	f := newAddDirFixture(t)
	ssh := mkdir(t, f.home, ".ssh")
	mkdir(t, ssh, "keys")
	variant := filepath.Join(f.home, ".SSH")
	if _, err := os.Stat(variant); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	for name, path := range map[string]string{
		"upper-case directory":    variant,
		"inside the case variant": filepath.Join(variant, "keys"),
		"mixed-case child":        filepath.Join(ssh, "KEYS"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := os.Stat(path); err != nil {
				t.Skipf("%s does not exist", path)
			}
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
}

// -- #1460: temp-root socket directories --------------------------------------
//
// The host's per-session socket directories live directly under the temp roots
// (ssh-agent, other users' tmux and emacs servers, the X11 helpers). They are
// refused by the start of the direct child's name, together with everything
// inside them; the temp roots themselves stay mountable only as a project
// directory below them.

var tempRoots = []string{"/tmp", "/private/tmp", "/var/tmp"}

// tempSocketDirNames are direct-child names the guard refuses. None of them is
// the current user's /tmp/tmux-<uid> or /tmp/emacs<uid>, so each one is caught by
// the name pattern alone and not by one of the older fixed guards.
var tempSocketDirNames = []string{
	"ssh-XXXX", "tmux-4242", "emacs4242", "emacs", ".ICE-unix", ".XIM-unix", ".font-unix", "com.apple.launchd.AbCdEf",
}

func TestAddDirGuards_TempRootSocketDirectoryPatterns_AreRefusedWithEverythingInside(t *testing.T) {
	newAddDirFixture(t)
	for _, root := range tempRoots {
		for _, name := range tempSocketDirNames {
			dir := filepath.Join(root, name)
			for _, target := range []string{dir, filepath.Join(dir, "inner", "deep")} {
				if !refusedBySomeGuard(t, "/home/dev", target) {
					t.Errorf("target %q is accepted, want another session's socket directory refused", target)
				}
			}
		}
	}
}

func TestAddDirGuards_TempRootSocketDirectoryPatterns_MatchWithoutRegardToCase(t *testing.T) {
	newAddDirFixture(t)
	// A case-insensitive filesystem (the macOS default) reaches /tmp/ssh-x as
	// /tmp/SSH-x: the pattern is compared on ASCII-lowered names.
	for _, name := range []string{"SSH-XXXX", "Tmux-4242", "EMACS4242", ".ice-unix", ".Xim-Unix", ".FONT-UNIX", "COM.APPLE.LAUNCHD.x"} {
		target := filepath.Join("/tmp", name, "inner")
		if !refusedBySomeGuard(t, "/home/dev", target) {
			t.Errorf("target %q is accepted, want the case variant of a socket directory refused", target)
		}
	}
}

// The patterns match the start of a DIRECT child's name only: a longer name
// that merely contains one, a name one level down, and the temp roots' own
// project directories stay accepted.
func TestAddDirGuards_TempRootSocketDirectoryPatterns_DoNotRefuseOrdinaryProjectDirectories(t *testing.T) {
	newAddDirFixture(t)
	for _, target := range []string{
		"/tmp/app", "/tmp/sshfoo", "/tmp/ssh", "/tmp/tmux", "/tmp/my-tmux-notes", "/tmp/xssh-1", "/var/tmp/build",
		"/private/tmp/app", "/tmp/app/ssh-keys-notes", "/var/tmp/app/.ICE-unix", "/tmp/com.apple.launchd",
	} {
		if refusedBySomeGuard(t, "/home/dev", target) {
			t.Errorf("target %q is refused, want it accepted: the patterns match only the start of a direct child's name", target)
		}
	}
}

func TestAddDirResolve_TmpdirSocketDirectories_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	tmp := mkdir(t, f.root, "sockets", "tmp")
	t.Setenv("TMPDIR", tmp)
	for name, path := range map[string]string{
		"another session's ssh-agent directory": mkdir(t, tmp, "ssh-AbCd"),
		"inside it":                             mkdir(t, tmp, "ssh-AbCd", "inner"),
		"another user's tmux directory":         mkdir(t, tmp, "tmux-4242"),
		"a case variant of an emacs directory":  mkdir(t, tmp, "Emacs4242"),
		"the ICE socket directory":              mkdir(t, tmp, ".ICE-unix"),
		"a launchd socket directory":            mkdir(t, tmp, "com.apple.launchd.AbCdEf"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
	for _, path := range []string{mkdir(t, tmp, "sshfoo"), mkdir(t, tmp, "my-tmux-notes"), mkdir(t, tmp, "app", "ssh-notes")} {
		if got, err := resolveAddDirs([]string{path}, f.scope, f.home); err != nil || len(got) != 1 {
			t.Errorf("%s must be accepted, got %+v, %v", path, got, err)
		}
	}
}

// $TMPDIR may be spelled through a symlink while the target is spelled through
// the real path: the pattern guard compares both spellings of its root.
func TestAddDirResolve_TmpdirBehindASymlink_StillRefusesItsSocketDirectories(t *testing.T) {
	f := newAddDirFixture(t)
	realTmp := mkdir(t, f.root, "sockets", "real-tmp")
	link := filepath.Join(f.root, "sockets", "link-tmp")
	if err := os.Symlink(realTmp, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", link)
	for _, path := range []string{mkdir(t, realTmp, "ssh-ZZ"), mkdir(t, realTmp, "tmux-4242", "inner")} {
		_, err := resolveAddDirs([]string{path}, f.scope, f.home)
		wantAddDirUsageError(t, err, path, "sensitive host location")
	}
}

func TestAddDirGuard_ChildPatternMode_MatchesDirectChildrenByIdentityOnCaseInsensitiveFilesystems(t *testing.T) {
	f := newAddDirFixture(t)
	root := mkdir(t, f.root, "Roots", "shared")
	child := mkdir(t, root, "ssh-1", "inner")
	variant := filepath.Join(f.root, "roots", "SHARED")
	if _, err := os.Stat(variant); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	guard := addDirGuard{path: variant, mode: forbidChildPattern, pattern: "ssh-*", why: "synthetic"}
	if !guard.refusesByIdentity([]string{child}) {
		t.Errorf("%q is accepted, want it refused through the root's other-case spelling", child)
	}
	if guard.refusesByIdentity([]string{mkdir(t, root, "app")}) {
		t.Errorf("a child whose name matches no pattern must not be refused by identity")
	}
}

// The identity half of the pattern guard runs on any filesystem: the guard root
// is a symlink to the real root, so the real spelling of a child shares no path
// prefix with it and only os.SameFile can relate the two.
func TestAddDirGuard_ChildPatternMode_MatchesDirectChildrenThroughASymlinkedRootByIdentity(t *testing.T) {
	f := newAddDirFixture(t)
	realRoot := mkdir(t, f.root, "sockets", "real-root")
	link := filepath.Join(f.root, "sockets", "link-root")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	guard := addDirGuard{path: link, mode: forbidChildPattern, pattern: "ssh-*", why: "synthetic"}

	for name, target := range map[string]string{
		"a child by its real spelling":        mkdir(t, realRoot, "ssh-1"),
		"inside a child by its real spelling": mkdir(t, realRoot, "ssh-2", "inner"),
		"a child through the symlink":         filepath.Join(link, "ssh-1"),
		"inside a child through the symlink":  filepath.Join(link, "ssh-2", "inner"),
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if !guard.refusesByIdentity([]string{target}) {
				t.Errorf("%q is accepted, want it refused by identity", target)
			}
		})
	}
	for name, target := range map[string]string{
		"a name that only starts like the pattern, real spelling": mkdir(t, realRoot, "sshfoo"),
		"a name containing the pattern, real spelling":            mkdir(t, realRoot, "my-tmux-notes"),
		"a name that only starts like the pattern, via the link":  filepath.Join(link, "sshfoo"),
		"a name containing the pattern, via the link":             filepath.Join(link, "my-tmux-notes"),
		"the root itself, real spelling":                          realRoot,
		"a matching name one level down":                          mkdir(t, realRoot, "app", "ssh-notes"),
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			if guard.refusesByIdentity([]string{target}) {
				t.Errorf("%q is refused by identity, want it accepted", target)
			}
		})
	}
}

// -- #1460: every absolute $PATH entry -----------------------------------------

func TestAddDirResolve_PathEntries_AreUsageErrorsNamingPATH(t *testing.T) {
	f := newAddDirFixture(t)
	tools := mkdir(t, f.root, "tools", "dev")
	binDir := mkdir(t, tools, "bin")
	second := mkdir(t, f.root, "tools", "other", "bin")
	t.Setenv("PATH", binDir+":"+second+":"+os.Getenv("PATH"))
	for name, path := range map[string]string{
		"a $PATH entry":              binDir,
		"the second $PATH entry":     second,
		"inside a $PATH entry":       mkdir(t, binDir, "scripts"),
		"a parent of a $PATH entry":  tools,
		"a parent of the second one": filepath.Dir(second),
		"a grandparent of both":      filepath.Dir(tools),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
			if !strings.Contains(err.Error(), "$PATH") {
				t.Errorf("error = %q, want the reason to mention $PATH", err.Error())
			}
		})
	}
	// A sibling of an entry is neither inside it nor a parent of it.
	sibling := mkdir(t, tools, "project")
	if got, err := resolveAddDirs([]string{sibling}, f.scope, f.home); err != nil || len(got) != 1 {
		t.Fatalf("a sibling of a $PATH entry must be accepted, got %+v, %v", got, err)
	}
}

// Relative and empty entries mean "the current directory" to a shell, and the
// guard ignores them like every other relative environment value that names no
// absolute location: the fixture runs in f.root, so honoring "." or "bin" would
// refuse everything below it.
func TestAddDirResolve_RelativeAndEmptyPathEntries_AddNoGuard(t *testing.T) {
	f := newAddDirFixture(t)
	absolute := mkdir(t, f.root, "tools", "bin")
	relativeBin := mkdir(t, f.root, "bin")
	t.Setenv("PATH", "bin:.::"+absolute+":"+os.Getenv("PATH"))
	for _, path := range []string{relativeBin, f.other} {
		if got, err := resolveAddDirs([]string{path}, f.scope, f.home); err != nil || len(got) != 1 {
			t.Errorf("%s must be accepted next to relative and empty $PATH entries, got %+v, %v", path, got, err)
		}
	}
	_, err := resolveAddDirs([]string{absolute}, f.scope, f.home)
	wantAddDirUsageError(t, err, absolute, "sensitive host location")
}

func TestAddDirGuards_PathEntryAbsentOnThisHost_StillGuardsItsLocation(t *testing.T) {
	newAddDirFixture(t)
	t.Setenv("PATH", "/srv/tools/bin:"+os.Getenv("PATH"))
	for _, target := range []string{"/srv/tools/bin", "/srv/tools/bin/child", "/srv/tools"} {
		if !refusedBySomeGuard(t, "/home/dev", target) {
			t.Errorf("target %q is accepted, want the absent $PATH entry guarded", target)
		}
	}
	for _, target := range []string{"/srv/tools2", "/srv/other", "/srv/tools/src"} {
		if refusedBySomeGuard(t, "/home/dev", target) {
			t.Errorf("target %q is refused, want it accepted: neither inside the $PATH entry nor a parent of it", target)
		}
	}
}

// -- #1460: developer tool homes ----------------------------------------------

// homeToolDirs are the tool homes derived from $HOME (relative to it): each is
// refused, with everything inside it, wherever the tool runs from.
var homeToolDirs = []string{
	"go/bin", ".cargo", ".rustup", ".nvm", ".pyenv", ".volta", ".bun", ".deno", ".rbenv", ".asdf",
	".local/share/mise", ".sdkman", ".npm-global",
}

func TestAddDirResolve_HomeToolHomes_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	for _, rel := range homeToolDirs {
		t.Run(rel, func(t *testing.T) {
			dir := mkdir(t, f.home, filepath.FromSlash(rel))
			for _, path := range []string{dir, mkdir(t, dir, "inner", "deep")} {
				_, err := resolveAddDirs([]string{path}, f.scope, f.home)
				wantAddDirUsageError(t, err, path, "sensitive host location")
			}
		})
	}
	// A directory that holds a tool home is a parent of it: ~/go (holding
	// ~/go/bin) and ~/.local/share (holding ~/.local/share/mise) are refused
	// while a project beside the tool home is not.
	for name, path := range map[string]string{
		"parent of ~/go/bin":            mkdir(t, f.home, "go"),
		"parent of ~/.local/share/mise": mkdir(t, f.home, ".local", "share"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveAddDirs([]string{path}, f.scope, f.home)
			wantAddDirUsageError(t, err, path, "sensitive host location")
		})
	}
	for _, path := range []string{mkdir(t, f.home, "go", "src", "app"), mkdir(t, f.home, ".cargo-old"), mkdir(t, f.home, "code", "cargo")} {
		if got, err := resolveAddDirs([]string{path}, f.scope, f.home); err != nil || len(got) != 1 {
			t.Errorf("%s must be accepted, got %+v, %v", path, got, err)
		}
	}
}

func TestAddDirGuards_AbsentHomeToolHomes_AreGuardedAndUnrelatedDirectoriesAccepted(t *testing.T) {
	newAddDirFixture(t)
	for _, rel := range homeToolDirs {
		target := filepath.Join("/home/dev", filepath.FromSlash(rel), "child")
		if !refusedBySomeGuard(t, "/home/dev", target) {
			t.Errorf("target %q is accepted, want the tool home guarded even when it does not exist", target)
		}
	}
	for _, target := range []string{"/home/dev/code/app", "/home/dev/.config/other-tool", "/home/dev/.cargo-old", "/home/dev/gopher"} {
		if guardedByPath(t, "/home/dev", target) {
			t.Errorf("target %q is refused, want an unrelated directory accepted", target)
		}
	}
}

func TestAddDirResolve_RelocatedToolHomes_AreUsageErrorsWithInsideAndParent(t *testing.T) {
	f := newAddDirFixture(t)
	tools := mkdir(t, f.root, "tools")
	gopath1, gopath2 := mkdir(t, tools, "gopath1"), mkdir(t, tools, "gopath2")
	for name, tc := range map[string]struct {
		env     string
		value   string
		guarded []string
	}{
		"GOBIN":       {"GOBIN", mkdir(t, tools, "gobin"), []string{filepath.Join(tools, "gobin")}},
		"GOPATH":      {"GOPATH", gopath1 + ":" + gopath2, []string{filepath.Join(gopath1, "bin"), filepath.Join(gopath2, "bin")}},
		"CARGO_HOME":  {"CARGO_HOME", mkdir(t, tools, "cargo"), []string{filepath.Join(tools, "cargo")}},
		"RUSTUP_HOME": {"RUSTUP_HOME", mkdir(t, tools, "rustup"), []string{filepath.Join(tools, "rustup")}},
		"NVM_DIR":     {"NVM_DIR", mkdir(t, tools, "nvm"), []string{filepath.Join(tools, "nvm")}},
		"PYENV_ROOT":  {"PYENV_ROOT", mkdir(t, tools, "pyenv"), []string{filepath.Join(tools, "pyenv")}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			for _, dir := range tc.guarded {
				mkdir(t, dir)
				for label, path := range map[string]string{
					"itself": dir, "inside": mkdir(t, dir, "inner"), "parent": filepath.Dir(dir),
				} {
					_, err := resolveAddDirs([]string{path}, f.scope, f.home)
					if err == nil {
						t.Errorf("%s (%s) %q is accepted, want a sensitive host location", label, tc.env, path)
						continue
					}
					wantAddDirUsageError(t, err, path, "sensitive host location")
				}
			}
			// A project beside the tool home, and a Go source tree next to the
			// GOPATH bin directory, are neither inside nor a parent of it.
			for _, path := range []string{mkdir(t, tools, "project"), mkdir(t, gopath1, "src", "app")} {
				if got, err := resolveAddDirs([]string{path}, f.scope, f.home); err != nil || len(got) != 1 {
					t.Errorf("%s must be accepted, got %+v, %v", path, got, err)
				}
			}
		})
	}
}

// Go itself rejects a relative GOBIN or GOPATH entry, so the guard ignores it;
// the other tools resolve a relative home against their working directory, so
// the guard follows them there (the fixture runs in f.root).
func TestAddDirResolve_RelativeToolHomeValues_FollowTheToolsOwnRule(t *testing.T) {
	f := newAddDirFixture(t)
	relGoBin := mkdir(t, f.root, "relgobin")
	relGoPath := mkdir(t, f.root, "relgopath", "bin")
	t.Setenv("GOBIN", "relgobin")
	t.Setenv("GOPATH", "relgopath")
	for _, path := range []string{relGoBin, relGoPath, filepath.Dir(relGoPath)} {
		if got, err := resolveAddDirs([]string{path}, f.scope, f.home); err != nil || len(got) != 1 {
			t.Errorf("%s must be accepted: a relative GOBIN/GOPATH adds no guard, got %+v, %v", path, got, err)
		}
	}
	relCargo := mkdir(t, f.root, "relcargo")
	t.Setenv("CARGO_HOME", "relcargo")
	_, err := resolveAddDirs([]string{relCargo}, f.scope, f.home)
	wantAddDirUsageError(t, err, relCargo, "sensitive host location")
}

// -- #1460: tool config directories whose contents the host executes ---------

// homeConfigDirs are the command-running config directories derived from $HOME
// (relative to it).
var homeConfigDirs = []string{
	".config/git", ".config/fish", ".config/tmux", ".tmux", ".oh-my-zsh", ".config/nvim", ".vim", ".emacs.d",
	".config/emacs", ".config/direnv", ".config/environment.d", ".config/containers", ".aws", ".kube",
}

// xdgConfigDirs are the children of an absolute $XDG_CONFIG_HOME the guard names.
var xdgConfigDirs = []string{"git", "fish", "tmux", "nvim", "emacs", "direnv", "environment.d", "containers"}

func TestAddDirResolve_HomeConfigDirectories_AreUsageErrors(t *testing.T) {
	f := newAddDirFixture(t)
	for _, rel := range homeConfigDirs {
		t.Run(rel, func(t *testing.T) {
			dir := mkdir(t, f.home, filepath.FromSlash(rel))
			for _, path := range []string{dir, mkdir(t, dir, "inner")} {
				_, err := resolveAddDirs([]string{path}, f.scope, f.home)
				wantAddDirUsageError(t, err, path, "sensitive host location")
			}
		})
	}
	// The list is curated, not "all of ~/.config": a sibling tool stays mountable.
	for _, path := range []string{mkdir(t, f.home, ".config", "other-tool"), mkdir(t, f.home, ".vim-old"), mkdir(t, f.home, ".kube-notes")} {
		if got, err := resolveAddDirs([]string{path}, f.scope, f.home); err != nil || len(got) != 1 {
			t.Errorf("%s must be accepted, got %+v, %v", path, got, err)
		}
	}
}

func TestAddDirGuards_AbsentHomeConfigDirectories_AreGuardedEvenWhenAbsentOnThisHost(t *testing.T) {
	newAddDirFixture(t)
	for _, rel := range homeConfigDirs {
		target := filepath.Join("/home/dev", filepath.FromSlash(rel), "child")
		if !refusedBySomeGuard(t, "/home/dev", target) {
			t.Errorf("target %q is accepted, want the config directory guarded even when it does not exist", target)
		}
	}
}

func TestAddDirResolve_XDGRelocatedConfigDirectories_AreUsageErrorsAndOtherToolsAreNot(t *testing.T) {
	f := newAddDirFixture(t)
	xdgConfig := mkdir(t, f.root, "xdg", "config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	for _, name := range xdgConfigDirs {
		t.Run(name, func(t *testing.T) {
			dir := mkdir(t, xdgConfig, name)
			for _, path := range []string{dir, mkdir(t, dir, "inner")} {
				_, err := resolveAddDirs([]string{path}, f.scope, f.home)
				wantAddDirUsageError(t, err, path, "sensitive host location")
			}
		})
	}
	if got, err := resolveAddDirs([]string{mkdir(t, xdgConfig, "other-tool")}, f.scope, f.home); err != nil || len(got) != 1 {
		t.Fatalf("a sibling of the relocated config dirs must be accepted, got %+v, %v", got, err)
	}
}

// A relative $XDG_CONFIG_HOME is invalid by the XDG specification and ignored.
func TestAddDirResolve_RelativeXDGConfigHome_AddsNoConfigGuard(t *testing.T) {
	f := newAddDirFixture(t)
	t.Setenv("XDG_CONFIG_HOME", "rel-config")
	git := mkdir(t, f.root, "rel-config", "git")
	if got, err := resolveAddDirs([]string{git}, f.scope, f.home); err != nil || len(got) != 1 {
		t.Fatalf("a relative XDG_CONFIG_HOME must be ignored, got %+v, %v", got, err)
	}
}

// -- #1460: the other-sandbox scan's building blocks ------------------------------

func TestAddDirStrictlyInsideBind_OnlyATargetBelowTheSourceMatches(t *testing.T) {
	f := newAddDirFixture(t)
	source := mkdir(t, f.root, "other", "src")
	link := filepath.Join(f.root, "src-link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		target, source string
		want           bool
	}{
		"a child of the source":                   {mkdir(t, source, "child"), source, true},
		"a deeper descendant":                     {mkdir(t, source, "a", "b"), source, true},
		"equal to the source":                     {source, source, false},
		"the parent of the source":                {filepath.Dir(source), source, false},
		"a grandparent of the source":             {f.root, source, false},
		"a sibling of the source":                 {mkdir(t, f.root, "other", "sibling"), source, false},
		"a sibling sharing the source's prefix":   {mkdir(t, f.root, "other", "src-notes"), source, false},
		"a child, the source given as a symlink":  {filepath.Join(source, "child"), link, true},
		"the source's own real path via the link": {source, link, false},
		"a source that is absent on this host":    {mkdir(t, f.root, "elsewhere", "x"), filepath.Join(f.root, "absent", "src"), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := strictlyInsideBind(tc.target, tc.source); got != tc.want {
				t.Errorf("strictlyInsideBind(%q, %q) = %v, want %v", tc.target, tc.source, got, tc.want)
			}
		})
	}
}

func TestAddDirOtherSandboxWritableBinds_OnlyWorkspaceAndReposMountsCountAndMustBeAbsolute(t *testing.T) {
	got, err := otherSandboxWritableBinds([]reuseMount{
		{Source: "/srv/app", Destination: "/workspace"},
		{Source: "/srv/lib/", Destination: "/repos/lib"},
		// Mounts that are not read-write host binds cenci creates are ignored, even
		// when their source is not a path.
		{Source: "claude-cenci-home-app", Destination: "/home/dev"},
		{Source: "cenci-agent-cli-claude", Destination: "/opt/cenci-agent"},
		{Source: "/run/user/1000/cenci", Destination: "/run/user/1000/cenci"},
		{Source: "relative", Destination: "/repository"},
	})
	if err != nil {
		t.Fatalf("otherSandboxWritableBinds: %v", err)
	}
	if want := []string{"/srv/app", "/srv/lib"}; !slices.Equal(got, want) {
		t.Errorf("sources = %q, want %q", got, want)
	}

	for name, m := range map[string]reuseMount{
		"a named volume on /workspace": {Source: "workspace-vol", Destination: "/workspace"},
		"a relative source on /repos":  {Source: "rel/dir", Destination: "/repos/x"},
		"an empty source":              {Source: "", Destination: "/workspace"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := otherSandboxWritableBinds([]reuseMount{m}); err == nil || !strings.Contains(err.Error(), "not an absolute host path") {
				t.Errorf("otherSandboxWritableBinds(%+v) = %q, %v; want an error naming the non-absolute source", m, got, err)
			}
		})
	}
}

// A bind follows the inode: when the host renames a running sandbox's source,
// `inspect` still reports the old path and the agent still writes under the new
// name, which no comparison on the reported path can relate to a target. The
// sandbox is unverifiable, so it refuses; a source that exists passes.
func TestAddDirRequireBindSourcesOnHost_AbsentSourceRefusesAndPresentOneDoesNot(t *testing.T) {
	f := newAddDirFixture(t)
	present := mkdir(t, f.root, "present")
	gone := mkdir(t, f.root, "gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.root, "afile")
	writeFile(t, file, "x")

	if err := requireBindSourcesOnHost("claude-cenci-x", []reuseMount{
		{Source: present, Destination: "/workspace"},
		{Source: present, Destination: "/repos/lib"},
		// Mounts that are not writable host binds are never examined.
		{Source: gone, Destination: "/home/dev"},
		{Source: "claude-cenci-home-app", Destination: "/opt/cenci-agent"},
	}); err != nil {
		t.Errorf("requireBindSourcesOnHost with only existing writable sources = %v, want nil", err)
	}

	for name, m := range map[string]reuseMount{
		"a deleted /workspace source":      {Source: gone, Destination: "/workspace"},
		"a deleted /repos source":          {Source: gone, Destination: "/repos/lib"},
		"a path below a regular file":      {Source: filepath.Join(file, "sub"), Destination: "/workspace"},
		"a dangling symlink as the source": {Source: dangling(t, f.root, gone), Destination: "/repos/x"},
	} {
		t.Run(name, func(t *testing.T) {
			err := requireBindSourcesOnHost("claude-cenci-x", []reuseMount{{Source: present, Destination: "/workspace"}, m})
			if err == nil {
				t.Fatal("requireBindSourcesOnHost = nil, want a refusal")
			}
			for _, want := range []string{"cannot be checked against other running sandboxes", "claude-cenci-x", strconv.Quote(m.Source), "no longer exists on the host"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), want)
				}
			}
			if IsUsage(err) {
				t.Errorf("a scan failure is a runtime error (exit 1), not a usage error: %v", err)
			}
		})
	}
}

// A Stat error that is not "absent" says nothing about whether the source exists,
// so it refuses too, with its own wording.
func TestAddDirRequireBindSourcesOnHost_UnreadableSourceRefuses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory search permission")
	}
	f := newAddDirFixture(t)
	locked := mkdir(t, f.root, "locked")
	source := mkdir(t, locked, "src")
	if err := os.Chmod(locked, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	err := requireBindSourcesOnHost("claude-cenci-x", []reuseMount{{Source: source, Destination: "/workspace"}})

	if err == nil {
		t.Fatal("requireBindSourcesOnHost = nil, want a refusal for a source that cannot be stat'ed")
	}
	for _, want := range []string{"cannot be checked against other running sandboxes", "claude-cenci-x", strconv.Quote(source), "cannot be examined on the host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

// dangling returns a symlink under root that points at the absent path gone.
func dangling(t *testing.T, root, gone string) string {
	t.Helper()
	link := filepath.Join(root, "dangling-link")
	if err := os.Symlink(gone, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// exitErrorWithStderr returns the *exec.ExitError a runtime that exits 1 with the
// given stderr produces from the untimed .Output() the inspect probes use.
func exitErrorWithStderr(t *testing.T, stderr string) error {
	t.Helper()
	_, err := exec.Command("sh", "-c", `printf '%s' "$1" >&2; exit 1`, "sh", stderr).Output()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("fixture command error = %T %v, want an *exec.ExitError", err, err)
	}
	return err
}

func TestAddDirContainerVanished_OnlyANoSuchContainerOrObjectExitErrorCounts(t *testing.T) {
	for stderr, want := range map[string]bool{
		"Error: No such container: claude-cenci-x":      true,
		"Error: No such object: claude-cenci-x":         true,
		"Error: no such container claude-cenci-x":       true,
		"Error: no such object: claude-cenci-x":         true,
		"Error: permission denied":                      false,
		"Error: No such volume: claude-cenci-x":         false,
		"Cannot connect to the Docker daemon":           false,
		"":                                              false,
		"no such  container (two spaces, not the text)": false,
	} {
		t.Run(stderr, func(t *testing.T) {
			err := exitErrorWithStderr(t, stderr)
			if got := containerVanished(err); got != want {
				t.Errorf("containerVanished(exit 1, stderr %q) = %v, want %v", stderr, got, want)
			}
			// The classification survives the "<runtime> inspect <name>: %w" wrap.
			if got := containerVanished(fmt.Errorf("docker inspect x reuse posture: %w", err)); got != want {
				t.Errorf("wrapped containerVanished(stderr %q) = %v, want %v", stderr, got, want)
			}
		})
	}
	// The same words in an error that is not an *exec.ExitError (a runtime that
	// could not run at all) prove nothing about the container.
	if containerVanished(errors.New("docker inspect x: no such container")) {
		t.Error("a non-exit error must never count as a vanished container")
	}
}

func TestAddDirScanOtherSandboxes_ListingFailureFailsClosed(t *testing.T) {
	newAddDirFixture(t)
	dryRunWithCallLog(t)
	t.Setenv("FAKE_PS_EXIT", "1")

	err := dryRunEngine().scanOtherSandboxes([]addDirMount{{Host: "/srv/x", Container: "/repos/x"}})

	if err == nil || !strings.Contains(err.Error(), "cannot be checked against other running sandboxes") {
		t.Fatalf("scanOtherSandboxes error = %v, want it to contain %q", err, "cannot be checked against other running sandboxes")
	}
	if IsUsage(err) {
		t.Errorf("a scan failure is a runtime error (exit 1), not a usage error: %v", err)
	}
}
