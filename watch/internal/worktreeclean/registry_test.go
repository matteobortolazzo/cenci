package worktreeclean

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
)

// Registry tests (#1267). The registry is additive coverage for the daemon's
// worktree-cleanup pass: it only ever widens which repos a pass looks at, so the
// cases that matter are the ones that would register something that is not a
// repo, forget one that is, or lose the file. They run over real git repos and
// real files; nothing is mocked.

const unparseableOriginSecret = "SECRET"

// registryFile is a registry path whose parent directories do not exist yet.
func registryFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(realPath(t, t.TempDir()), "state", "cenci", "worktree-repos.json")
}

// gitHubRepo is a real main checkout whose origin names acme/<name>, so the
// ticketstate Resolver accepts it.
func gitHubRepo(t *testing.T, name string) *fixtureRepo {
	t.Helper()
	r := newRepo(t, name)
	r.git(t, "remote", "add", "origin", "https://github.com/acme/"+name+".git")
	return r
}

func writeRegistryRaw(t *testing.T, path, body string) {
	t.Helper()
	writeFile(t, path, body)
}

// persisted decodes the registry file as written on disk.
func persisted(t *testing.T, path string) (version int, repos []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry file: %v", err)
	}
	var f struct {
		SchemaVersion int      `json:"schemaVersion"`
		Repos         []string `json:"repos"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("registry file is not JSON: %v\n%s", err, data)
	}
	return f.SchemaVersion, f.Repos
}

func mustLoad(t *testing.T, path string) []string {
	t.Helper()
	got, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return got
}

func mustUpdate(t *testing.T, u RegistryUpdate) []string {
	t.Helper()
	got, problems := UpdateRegistry(context.Background(), u)
	if len(problems) != 0 {
		t.Fatalf("UpdateRegistry problems = %+v, want none", problems)
	}
	return got
}

func sorted(dirs ...string) []string {
	out := slices.Clone(dirs)
	slices.Sort(out)
	return out
}

// ---------------------------------------------------------------------------
// Default paths
// ---------------------------------------------------------------------------

func TestDefaultPaths_FollowTheXDGDirectories(t *testing.T) {
	state, config := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", config)

	if got, want := DefaultRegistryPath(), filepath.Join(state, "cenci", "worktree-repos.json"); got != want {
		t.Errorf("DefaultRegistryPath() = %q, want %q", got, want)
	}
	if got, want := DefaultUserConfigPath(), filepath.Join(config, "cenci", "config.json"); got != want {
		t.Errorf("DefaultUserConfigPath() = %q, want %q", got, want)
	}
}

func TestDefaultPaths_FallBackToTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	if got, want := DefaultRegistryPath(), filepath.Join(home, ".local", "state", "cenci", "worktree-repos.json"); got != want {
		t.Errorf("DefaultRegistryPath() = %q, want %q", got, want)
	}
	if got, want := DefaultUserConfigPath(), filepath.Join(home, ".config", "cenci", "config.json"); got != want {
		t.Errorf("DefaultUserConfigPath() = %q, want %q", got, want)
	}
}

// The XDG spec says a relative base directory is invalid and must be ignored: the
// paths fall back to the home-based defaults instead of resolving against the cwd.
func TestDefaultPaths_IgnoreARelativeXDGDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "relative/state")
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	t.Setenv("HOME", home)

	if got, want := DefaultRegistryPath(), filepath.Join(home, ".local", "state", "cenci", "worktree-repos.json"); got != want {
		t.Errorf("DefaultRegistryPath() = %q, want %q", got, want)
	}
	if got, want := DefaultUserConfigPath(), filepath.Join(home, ".config", "cenci", "config.json"); got != want {
		t.Errorf("DefaultUserConfigPath() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// SaveRegistry / LoadRegistry
// ---------------------------------------------------------------------------

func TestRegistry_SaveThenLoadRoundTripsSortedAndDeduplicated(t *testing.T) {
	path := registryFile(t)

	if err := SaveRegistry(path, []string{"/b/repo", "/a/repo", "/b/repo"}); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}

	if got, want := mustLoad(t, path), []string{"/a/repo", "/b/repo"}; !slices.Equal(got, want) {
		t.Errorf("LoadRegistry = %v, want %v", got, want)
	}
	version, repos := persisted(t, path)
	if version != 1 || !slices.Equal(repos, []string{"/a/repo", "/b/repo"}) {
		t.Errorf("file = schemaVersion %d, repos %v; want schemaVersion 1, repos [/a/repo /b/repo]", version, repos)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("registry file mode = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("registry directory mode = %v, want 0700 for a directory the save created", dirInfo.Mode().Perm())
	}
}

// go-gotchas rule 1: an empty list must marshal as [], not null.
func TestRegistry_SaveWritesAnEmptyRegistryAsAnArray(t *testing.T) {
	path := registryFile(t)

	if err := SaveRegistry(path, nil); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	compact := strings.Join(strings.Fields(string(data)), "")
	if !strings.Contains(compact, `"repos":[]`) || strings.Contains(compact, "null") {
		t.Errorf("registry file = %s, want \"repos\": [] and no null", data)
	}
}

// The save replaces the file atomically (a temp file renamed into place): no
// temp file is left behind, and a file that already exists with a looser mode
// does not keep it.
func TestRegistry_SaveReplacesTheFileAtomicallyWithMode0600(t *testing.T) {
	path := registryFile(t)
	writeRegistryRaw(t, path, `{"schemaVersion":1,"repos":["/old/repo"]}`)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveRegistry(path, []string{"/new/repo"}); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}

	if got := mustLoad(t, path); !slices.Equal(got, []string{"/new/repo"}) {
		t.Errorf("LoadRegistry = %v, want [/new/repo]", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("registry file mode = %v after replacing a 0644 file, want 0600", info.Mode().Perm())
	}
	names, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0].Name() != filepath.Base(path) {
		t.Errorf("registry directory holds %v, want only %s (no temp file left behind)", names, filepath.Base(path))
	}
}

func TestLoadRegistry_MissingFileIsAnEmptyRegistry(t *testing.T) {
	got, err := LoadRegistry(filepath.Join(t.TempDir(), "none.json"))

	if err != nil || len(got) != 0 {
		t.Errorf("LoadRegistry(missing) = %v, %v; want an empty registry and no error", got, err)
	}
}

// Entries read back from disk are validated again: only an absolute, clean path
// free of NUL and newline survives, so a hand-edited or forged file cannot name a
// relative directory or smuggle control characters into a log line.
func TestLoadRegistry_DropsEntriesThatAreNotAbsoluteCleanPaths(t *testing.T) {
	path := registryFile(t)
	writeRegistryRaw(t, path, `{"schemaVersion":1,"repos":[
		"/good/one",
		"relative/repo",
		"",
		"/unclean/../repo",
		"/trailing/slash/",
		"/double//slash",
		"/with\nnewline",
		"/with\u0000nul",
		"/good/two"
	]}`)

	got := mustLoad(t, path)

	if want := []string{"/good/one", "/good/two"}; !slices.Equal(got, want) {
		t.Errorf("LoadRegistry = %q, want only %q", got, want)
	}
}

func TestLoadRegistry_RejectsAFileItCannotTrust(t *testing.T) {
	tests := []struct {
		name string
		body string // written to the registry path; "" means make the path a directory
	}{
		{"not JSON", "this is not json"},
		{"truncated", `{"schemaVersion":1,"repos":["/a`},
		{"unknown schema version", `{"schemaVersion":2,"repos":["/a/repo"]}`},
		{"missing schema version", `{"repos":["/a/repo"]}`},
		{"repos is not a list", `{"schemaVersion":1,"repos":"/a/repo"}`},
		{"larger than the size bound", `{"schemaVersion":1,"repos":["` + strings.Repeat("a", 70<<10) + `"]}`},
		{"a directory", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := registryFile(t)
			if tt.body == "" {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeRegistryRaw(t, path, tt.body)
			}

			got, err := LoadRegistry(path)

			if err == nil {
				t.Errorf("LoadRegistry = %v, nil; want an error", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DispatchRepoDirs
// ---------------------------------------------------------------------------

func TestDispatchRepoDirs_MissingConfigContributesNothing(t *testing.T) {
	got, err := DispatchRepoDirs(filepath.Join(t.TempDir(), "config.json"))

	if err != nil || len(got) != 0 {
		t.Errorf("DispatchRepoDirs(missing) = %v, %v; want nothing and no error", got, err)
	}
}

func TestDispatchRepoDirs_ReadsTheDirOfEveryEnrolledRepo(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"two repos in order", `{"dispatch":{"repos":[{"repo":"acme/a","dir":"/work/a","session":"main"},{"repo":"acme/b","dir":"/work/b"}]}}`, []string{"/work/a", "/work/b"}},
		{"other keys and blocks are ignored", `{"agents":{},"dispatch":{"concurrencyCap":2,"repos":[{"repo":"acme/a","dir":"/work/a","extra":true}]}}`, []string{"/work/a"}},
		{"a repo without a dir is skipped", `{"dispatch":{"repos":[{"repo":"acme/a"},{"repo":"acme/b","dir":"/work/b"}]}}`, []string{"/work/b"}},
		{"values are returned as written, validation is the registry's job", `{"dispatch":{"repos":[{"dir":"relative/dir"}]}}`, []string{"relative/dir"}},
		{"no dispatch block", `{"agents":{}}`, nil},
		{"dispatch without repos", `{"dispatch":{"concurrencyCap":2}}`, nil},
		{"empty repos", `{"dispatch":{"repos":[]}}`, nil},
		// go-gotchas rule 15: struct-tag decoding would match these case-insensitively.
		{"keys match exactly: Dispatch.Repos.Dir", `{"Dispatch":{"Repos":[{"Dir":"/work/a"}]}}`, nil},
		{"keys match exactly: repos[].Dir", `{"dispatch":{"repos":[{"Dir":"/work/a"}]}}`, nil},
		{"dir at the wrong level", `{"dispatch":{"dir":"/work/a"},"dir":"/work/b"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeFile(t, path, tt.body)

			got, err := DispatchRepoDirs(path)

			if err != nil {
				t.Fatalf("DispatchRepoDirs: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("DispatchRepoDirs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDispatchRepoDirs_ReportsAConfigItCannotRead(t *testing.T) {
	tests := []struct {
		name string
		body string // "" means make the path a directory
	}{
		{"malformed JSON", `{"dispatch":`},
		{"top level is not an object", `["dispatch"]`},
		{"dispatch is not an object", `{"dispatch":[]}`},
		{"repos is not a list", `{"dispatch":{"repos":{}}}`},
		{"repo entry is not an object", `{"dispatch":{"repos":["/work/a"]}}`},
		{"dir is not a string", `{"dispatch":{"repos":[{"dir":5}]}}`},
		{"larger than the size bound", `{"pad":"` + strings.Repeat("a", 70<<10) + `"}`},
		{"a directory", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if tt.body == "" {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, path, tt.body)
			}

			got, err := DispatchRepoDirs(path)

			if err == nil {
				t.Errorf("DispatchRepoDirs = %v, nil; want an error", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateRegistry
// ---------------------------------------------------------------------------

// A candidate from the dispatch config and one from a window are registered, and
// a fresh load (a restarted daemon) sees both.
func TestUpdateRegistry_RegistersValidatedCandidatesAndTheyOutliveTheCall(t *testing.T) {
	a, b := gitHubRepo(t, "alpha"), gitHubRepo(t, "beta")
	path := registryFile(t)

	got := mustUpdate(t, RegistryUpdate{Path: path, DispatchDirs: []string{a.Root}, WindowDirs: []string{b.Root}})

	want := sorted(a.Root, b.Root)
	if !slices.Equal(got, want) {
		t.Errorf("UpdateRegistry = %v, want %v", got, want)
	}
	if reloaded := mustLoad(t, path); !slices.Equal(reloaded, want) {
		t.Errorf("a fresh LoadRegistry = %v, want %v: the repos must outlive the call and the windows", reloaded, want)
	}

	// Registered once, still there when nothing new is offered (windows closed).
	if again := mustUpdate(t, RegistryUpdate{Path: path}); !slices.Equal(again, want) {
		t.Errorf("UpdateRegistry with no candidates = %v, want the registered %v", again, want)
	}
}

// With no usable registry path (no home directory, or a relative path) nothing is
// persisted and nothing is created in the working directory; the pass still gets
// the candidates, and one problem says the registry is not kept.
func TestUpdateRegistry_WithoutAnAbsolutePathPersistsNothingButStillReturnsTheCandidates(t *testing.T) {
	for _, path := range []string{"", filepath.Join("state", "worktree-repos.json")} {
		t.Run("path="+path, func(t *testing.T) {
			a := gitHubRepo(t, "alpha")
			cwd := realPath(t, t.TempDir())
			t.Chdir(cwd)

			got, problems := UpdateRegistry(context.Background(), RegistryUpdate{Path: path, DispatchDirs: []string{a.Root}})

			if want := []string{a.Root}; !slices.Equal(got, want) {
				t.Errorf("UpdateRegistry = %v, want the candidate union %v", got, want)
			}
			if len(problems) != 1 || problems[0].Source != RegistrySourceFile || problems[0].Err == nil {
				t.Errorf("problems = %+v, want exactly one registry-file problem", problems)
			}
			if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
				t.Errorf("working directory holds %v (err %v), want nothing written there", entries, err)
			}
		})
	}
}

// SaveRegistry refuses a path it cannot place, rather than writing into the cwd.
func TestSaveRegistry_RefusesAnEmptyOrRelativePath(t *testing.T) {
	for _, path := range []string{"", "worktree-repos.json"} {
		cwd := realPath(t, t.TempDir())
		t.Chdir(cwd)

		if err := SaveRegistry(path, []string{"/srv/alpha"}); err == nil {
			t.Errorf("SaveRegistry(%q) = nil, want an error", path)
		}
		if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
			t.Errorf("SaveRegistry(%q) left %v in the working directory (err %v)", path, entries, err)
		}
	}
}

// What is registered is the main checkout, never the worktree a window happened to
// be launched in (which disappears when the worktree is reclaimed) or a
// subdirectory.
func TestUpdateRegistry_RegistersTheMainRootNotTheWorktreeOrSubdirectory(t *testing.T) {
	r := gitHubRepo(t, "alpha")
	wt := addWorktree(t, r, "42-feature", "feature-42")
	sub := filepath.Join(r.Root, "pkg", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := registryFile(t)

	got := mustUpdate(t, RegistryUpdate{Path: path, DispatchDirs: []string{r.Root}, WindowDirs: []string{wt.Path, sub}})

	if want := []string{r.Root}; !slices.Equal(got, want) {
		t.Errorf("UpdateRegistry = %v, want exactly the main root %v", got, want)
	}
	if reloaded := mustLoad(t, path); !slices.Equal(reloaded, []string{r.Root}) {
		t.Errorf("persisted = %v, want [%s]: a worktree path must not be what survives", reloaded, r.Root)
	}
}

// A value that fails validation is never registered, whichever source it came
// from; each is reported against its source; and the valid control candidate in
// the same call still registers (the call ran).
func TestUpdateRegistry_NeverRegistersACandidateThatFailsValidation(t *testing.T) {
	control := gitHubRepo(t, "control")
	noOrigin := newRepo(t, "no-origin")
	badOrigin := newRepo(t, "bad-origin")
	badOrigin.git(t, "remote", "add", "origin", "https://user:"+unparseableOriginSecret+"@github.com/onlyone")
	notGit := realPath(t, t.TempDir())
	missing := filepath.Join(notGit, "no", "such", "dir")
	badDispatch := []string{"relative/repo", notGit, noOrigin.Root, missing}
	badWindow := []string{"/abs/with\nnewline", "/abs/with\x00nul", badOrigin.Root}
	path := registryFile(t)

	got, problems := UpdateRegistry(context.Background(), RegistryUpdate{
		Path:         path,
		DispatchDirs: append(slices.Clone(badDispatch), control.Root),
		WindowDirs:   badWindow,
	})

	if want := []string{control.Root}; !slices.Equal(got, want) {
		t.Errorf("UpdateRegistry = %v, want only the valid %v", got, want)
	}
	if reloaded := mustLoad(t, path); !slices.Equal(reloaded, []string{control.Root}) {
		t.Errorf("persisted = %v, want only [%s]", reloaded, control.Root)
	}
	for source, want := range map[RegistrySource][]string{RegistrySourceDispatch: badDispatch, RegistrySourceWindow: badWindow} {
		var dirs []string
		for _, p := range problems {
			if p.Source != source {
				continue
			}
			dirs = append(dirs, p.Dir)
			if p.Err == nil {
				t.Errorf("problem for %q has no error", p.Dir)
			} else if strings.Contains(p.Err.Error(), unparseableOriginSecret) {
				t.Errorf("problem for %q leaks the origin credentials: %v", p.Dir, p.Err)
			}
		}
		if !slices.Equal(sorted(dirs...), sorted(want...)) {
			t.Errorf("%s problems name %q, want exactly the rejected %q", source, sorted(dirs...), sorted(want...))
		}
	}
	for _, p := range problems {
		if p.Dir == control.Root {
			t.Errorf("the valid candidate was reported: %+v", p)
		}
	}
}

// ResolveRepo's containment check is what stops a forged .git file: Resolver
// alone accepts a directory whose .git file points into another repository, so
// the registry must not register it (or the other repo under a path that is not
// its own).
func TestUpdateRegistry_NeverRegistersADirectoryWithAForgedGitFile(t *testing.T) {
	victim := gitHubRepo(t, "victim")
	forged := filepath.Join(realPath(t, t.TempDir()), "forged")
	writeFile(t, filepath.Join(forged, ".git"), "gitdir: "+victim.CommonDir+"\n")
	if _, err := (&ticketstate.Resolver{}).Resolve(context.Background(), forged); err != nil {
		t.Fatalf("fixture premise broken: the forged directory no longer passes the Resolver (%v), so this test no longer proves the second check", err)
	}
	if _, err := ResolveRepo(context.Background(), forged); err == nil {
		t.Fatal("fixture premise broken: ResolveRepo accepted the forged directory")
	}
	path := registryFile(t)

	got, problems := UpdateRegistry(context.Background(), RegistryUpdate{Path: path, WindowDirs: []string{forged}})

	if len(got) != 0 {
		t.Errorf("UpdateRegistry = %v, want nothing registered", got)
	}
	if reloaded := mustLoad(t, path); len(reloaded) != 0 {
		t.Errorf("persisted = %v, want nothing", reloaded)
	}
	if len(problems) != 1 || problems[0].Source != RegistrySourceWindow || problems[0].Dir != forged {
		t.Errorf("problems = %+v, want one window problem for %q", problems, forged)
	}
}

func TestUpdateRegistry_DropsAnEntryWhoseDirectoryNoLongerExists(t *testing.T) {
	gone, kept := gitHubRepo(t, "gone"), gitHubRepo(t, "kept")
	path := registryFile(t)
	mustUpdate(t, RegistryUpdate{Path: path, DispatchDirs: []string{gone.Root, kept.Root}})
	if err := os.RemoveAll(gone.Root); err != nil {
		t.Fatal(err)
	}

	got := mustUpdate(t, RegistryUpdate{Path: path})

	if want := []string{kept.Root}; !slices.Equal(got, want) {
		t.Errorf("UpdateRegistry = %v, want %v", got, want)
	}
	if reloaded := mustLoad(t, path); !slices.Equal(reloaded, []string{kept.Root}) {
		t.Errorf("persisted = %v, want the gone repo dropped: [%s]", reloaded, kept.Root)
	}
}

// Only a stat that says "does not exist" drops an entry. When the stat fails for
// another reason (here: no permission to search the parent) the repo may well
// still be there, so it stays registered, and is only left out of this pass.
func TestUpdateRegistry_KeepsButSkipsAnEntryItCannotStat(t *testing.T) {
	hidden, visible := gitHubRepo(t, "hidden"), gitHubRepo(t, "visible")
	path := registryFile(t)
	mustUpdate(t, RegistryUpdate{Path: path, DispatchDirs: []string{hidden.Root, visible.Root}})
	parent := filepath.Dir(hidden.Root)
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if _, err := os.Lstat(hidden.Root); err == nil || os.IsNotExist(err) {
		t.Skipf("cannot make the entry unstattable here (Lstat err = %v); running as root?", err)
	}

	got, problems := UpdateRegistry(context.Background(), RegistryUpdate{Path: path})

	if want := []string{visible.Root}; !slices.Equal(got, want) {
		t.Errorf("UpdateRegistry = %v, want this pass limited to %v", got, want)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if reloaded := mustLoad(t, path); !slices.Equal(reloaded, sorted(hidden.Root, visible.Root)) {
		t.Errorf("persisted = %v, want the unstattable entry kept: %v", reloaded, sorted(hidden.Root, visible.Root))
	}
	if !slices.ContainsFunc(problems, func(p RegistryProblem) bool {
		return p.Source == RegistrySourceEntry && p.Dir == hidden.Root && p.Err != nil
	}) {
		t.Errorf("problems = %+v, want an entry problem naming %q", problems, hidden.Root)
	}
}

// The file is rewritten only when its content changes. A positive control in the
// same fixture shows the mtime check can fail.
func TestUpdateRegistry_WritesOnlyWhenTheRegistryChanges(t *testing.T) {
	a, b := gitHubRepo(t, "alpha"), gitHubRepo(t, "beta")
	path := registryFile(t)
	mustUpdate(t, RegistryUpdate{Path: path, DispatchDirs: []string{a.Root}})
	old := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	setMtime(t, path, old)

	mustUpdate(t, RegistryUpdate{Path: path, DispatchDirs: []string{a.Root}, WindowDirs: []string{a.Root}})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("registry mtime = %v after an update that changed nothing, want it untouched (%v)", info.ModTime(), old)
	}

	mustUpdate(t, RegistryUpdate{Path: path, DispatchDirs: []string{b.Root}})
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Equal(old) {
		t.Error("registry mtime unchanged after registering a new repo: the file was not rewritten (the control for the check above)")
	}
	if got := mustLoad(t, path); !slices.Equal(got, sorted(a.Root, b.Root)) {
		t.Errorf("persisted = %v, want %v", got, sorted(a.Root, b.Root))
	}
}

// A corrupt file is reported and rebuilt from this call's candidates: the
// registry only widens coverage, so losing an entry delays cleanup until the
// repo's next window and can never cause a removal.
func TestUpdateRegistry_RebuildsACorruptFileFromThisCallsCandidates(t *testing.T) {
	a := gitHubRepo(t, "alpha")
	path := registryFile(t)
	writeRegistryRaw(t, path, "{corrupt")

	got, problems := UpdateRegistry(context.Background(), RegistryUpdate{Path: path, DispatchDirs: []string{a.Root}})

	if want := []string{a.Root}; !slices.Equal(got, want) {
		t.Errorf("UpdateRegistry = %v, want %v", got, want)
	}
	if reloaded := mustLoad(t, path); !slices.Equal(reloaded, []string{a.Root}) {
		t.Errorf("persisted = %v, want the file rebuilt as [%s]", reloaded, a.Root)
	}
	if len(problems) != 1 || problems[0].Source != RegistrySourceFile || problems[0].Dir != path || problems[0].Err == nil {
		t.Errorf("problems = %+v, want one registry-file problem naming %q", problems, path)
	}
}

// A call that cannot resolve anything (its context is already done) registers
// nothing new but never forgets what is registered: only a missing directory
// drops an entry, not a failed check.
func TestUpdateRegistry_ACancelledCallKeepsEveryEntry(t *testing.T) {
	a, b := gitHubRepo(t, "alpha"), gitHubRepo(t, "beta")
	path := registryFile(t)
	mustUpdate(t, RegistryUpdate{Path: path, DispatchDirs: []string{a.Root}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, problems := UpdateRegistry(ctx, RegistryUpdate{Path: path, WindowDirs: []string{b.Root}})

	if reloaded := mustLoad(t, path); !slices.Equal(reloaded, []string{a.Root}) {
		t.Errorf("persisted = %v, want [%s]: a cancelled call must not lose or add entries", reloaded, a.Root)
	}
	if !slices.ContainsFunc(problems, func(p RegistryProblem) bool { return p.Source == RegistrySourceWindow && p.Dir == b.Root }) {
		t.Errorf("problems = %+v, want the window candidate %q reported as not registered", problems, b.Root)
	}
}
