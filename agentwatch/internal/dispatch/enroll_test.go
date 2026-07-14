package dispatch

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/run"
)

// -- test helpers --------------------------------------------------------

// genericConfig decodes a config.json file loosely enough to assert both the
// mutated dispatch.repos list and every other key survives round-tripping
// untouched.
type genericConfig struct {
	Agents       map[string]json.RawMessage `json:"agents"`
	DefaultAgent string                     `json:"defaultAgent"`
	Sandbox      *bool                      `json:"sandbox"`
	Dispatch     genericDispatch            `json:"dispatch"`
}

type genericDispatch struct {
	Repos          []RepoConfig `json:"repos"`
	ConcurrencyCap *int         `json:"concurrencyCap"`
}

func mustDecodeConfig(t *testing.T, path string) genericConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var cfg genericConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decoding %s: %v\ncontent:\n%s", path, err, data)
	}
	return cfg
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func reposContains(repos []RepoConfig, repo, dir string) bool {
	for _, r := range repos {
		if r.Repo == repo && r.Dir == dir {
			return true
		}
	}
	return false
}

// runGit runs `git -C dir <args>`, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initGitRemote creates a fresh git repo in a temp dir with the given origin
// remote URL and returns the repo directory.
func initGitRemote(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", remoteURL)
	return dir
}

// -- EnrollRepo -----------------------------------------------------------

func TestEnrollRepo_CreatesDispatchBlockInNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Nested dir does not exist yet: exercises the MkdirAll(0700) path.
	path := filepath.Join(tmpDir, "nested", "config.json")
	identity := RepoIdentity{Repo: "owner/name", Dir: "/abs/path/to/repo"}

	changed, err := EnrollRepo(path, identity)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	if !changed {
		t.Fatalf("EnrollRepo: want changed=true for a brand-new file")
	}

	dirInfo, err := os.Stat(filepath.Join(tmpDir, "nested"))
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir perm = %o, want 0700", perm)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file perm = %o, want 0600", perm)
	}

	// No leftover .tmp sibling after the atomic rename.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}

	cfg := mustDecodeConfig(t, path)
	if !reposContains(cfg.Dispatch.Repos, identity.Repo, identity.Dir) {
		t.Errorf("dispatch.repos = %+v, want to contain %+v", cfg.Dispatch.Repos, identity)
	}
	if len(cfg.Dispatch.Repos) != 1 {
		t.Errorf("dispatch.repos = %+v, want exactly one entry", cfg.Dispatch.Repos)
	}

	// 2-space indent: at depth 1 "dispatch" is the sole top-level key.
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("\n  \"dispatch\": {\n")) {
		t.Errorf("expected 2-space-indented JSON, got:\n%s", raw)
	}
}

func TestEnrollRepo_PreservesUnknownTopLevelKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "agents": {"claude": {"command": "claude"}},
  "defaultAgent": "claude",
  "sandbox": true,
  "dispatch": {"repos": []}
}`)

	identity := RepoIdentity{Repo: "owner/name", Dir: "/abs/dir"}
	changed, err := EnrollRepo(path, identity)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	if !changed {
		t.Fatalf("EnrollRepo: want changed=true")
	}

	cfg := mustDecodeConfig(t, path)
	if cfg.DefaultAgent != "claude" {
		t.Errorf("defaultAgent = %q, want %q", cfg.DefaultAgent, "claude")
	}
	if cfg.Sandbox == nil || !*cfg.Sandbox {
		t.Errorf("sandbox = %v, want true", cfg.Sandbox)
	}
	if _, ok := cfg.Agents["claude"]; !ok {
		t.Errorf("agents = %+v, want key %q preserved", cfg.Agents, "claude")
	}
	if !reposContains(cfg.Dispatch.Repos, identity.Repo, identity.Dir) {
		t.Errorf("dispatch.repos = %+v, want to contain %+v", cfg.Dispatch.Repos, identity)
	}
}

func TestEnrollRepo_PreservesUnknownDispatchKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "dispatch": {
    "repos": [],
    "concurrencyCap": 7
  }
}`)

	identity := RepoIdentity{Repo: "owner/name", Dir: "/abs/dir"}
	changed, err := EnrollRepo(path, identity)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	if !changed {
		t.Fatalf("EnrollRepo: want changed=true")
	}

	cfg := mustDecodeConfig(t, path)
	if cfg.Dispatch.ConcurrencyCap == nil || *cfg.Dispatch.ConcurrencyCap != 7 {
		t.Errorf("dispatch.concurrencyCap = %v, want 7", cfg.Dispatch.ConcurrencyCap)
	}
	if !reposContains(cfg.Dispatch.Repos, identity.Repo, identity.Dir) {
		t.Errorf("dispatch.repos = %+v, want to contain %+v", cfg.Dispatch.Repos, identity)
	}
}

func TestEnrollRepo_IdempotentSameRepoDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	identity := RepoIdentity{Repo: "owner/name", Dir: "/abs/dir"}

	if changed, err := EnrollRepo(path, identity); err != nil || !changed {
		t.Fatalf("first EnrollRepo: changed=%v err=%v", changed, err)
	}

	bytesBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	statBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	changed, err := EnrollRepo(path, identity)
	if err != nil {
		t.Fatalf("second EnrollRepo: %v", err)
	}
	if changed {
		t.Errorf("second EnrollRepo: want changed=false for identical repo+dir")
	}

	bytesAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	statAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	if !bytes.Equal(bytesBefore, bytesAfter) {
		t.Errorf("file bytes changed on a no-op enroll")
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("mtime changed on a no-op enroll: before=%v after=%v", statBefore.ModTime(), statAfter.ModTime())
	}
}

func TestEnrollRepo_UpdatesDirInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repo := "owner/name"

	if changed, err := EnrollRepo(path, RepoIdentity{Repo: repo, Dir: "/dir/a"}); err != nil || !changed {
		t.Fatalf("first EnrollRepo: changed=%v err=%v", changed, err)
	}

	changed, err := EnrollRepo(path, RepoIdentity{Repo: repo, Dir: "/dir/b"})
	if err != nil {
		t.Fatalf("second EnrollRepo: %v", err)
	}
	if !changed {
		t.Errorf("second EnrollRepo: want changed=true when the dir differs")
	}

	cfg := mustDecodeConfig(t, path)
	if len(cfg.Dispatch.Repos) != 1 {
		t.Fatalf("dispatch.repos = %+v, want exactly one entry (updated in place)", cfg.Dispatch.Repos)
	}
	if cfg.Dispatch.Repos[0].Dir != "/dir/b" {
		t.Errorf("dispatch.repos[0].Dir = %q, want %q", cfg.Dispatch.Repos[0].Dir, "/dir/b")
	}
}

// -- UnenrollRepo -----------------------------------------------------------

func TestUnenrollRepo_RemovesMatchingEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := EnrollRepo(path, RepoIdentity{Repo: "owner/a", Dir: "/dir/a"}); err != nil {
		t.Fatalf("EnrollRepo a: %v", err)
	}
	if _, err := EnrollRepo(path, RepoIdentity{Repo: "owner/b", Dir: "/dir/b"}); err != nil {
		t.Fatalf("EnrollRepo b: %v", err)
	}

	changed, err := UnenrollRepo(path, "owner/a")
	if err != nil {
		t.Fatalf("UnenrollRepo: %v", err)
	}
	if !changed {
		t.Errorf("UnenrollRepo: want changed=true")
	}

	cfg := mustDecodeConfig(t, path)
	if len(cfg.Dispatch.Repos) != 1 || cfg.Dispatch.Repos[0].Repo != "owner/b" {
		t.Errorf("dispatch.repos = %+v, want only owner/b left", cfg.Dispatch.Repos)
	}
}

func TestUnenrollRepo_NotEnrolledIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := EnrollRepo(path, RepoIdentity{Repo: "owner/a", Dir: "/dir/a"}); err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}

	bytesBefore, _ := os.ReadFile(path)
	statBefore, _ := os.Stat(path)

	changed, err := UnenrollRepo(path, "owner/never-enrolled")
	if err != nil {
		t.Fatalf("UnenrollRepo: %v", err)
	}
	if changed {
		t.Errorf("UnenrollRepo: want changed=false for a repo that was never enrolled")
	}

	bytesAfter, _ := os.ReadFile(path)
	statAfter, _ := os.Stat(path)
	if !bytes.Equal(bytesBefore, bytesAfter) {
		t.Errorf("file bytes changed on a no-op unenroll")
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("mtime changed on a no-op unenroll")
	}
}

func TestUnenrollRepo_MissingConfigFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	changed, err := UnenrollRepo(path, "owner/name")
	if err != nil {
		t.Fatalf("UnenrollRepo: want no error for a missing config file, got %v", err)
	}
	if changed {
		t.Errorf("UnenrollRepo: want changed=false for a missing config file")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("UnenrollRepo must not create a config file when none exists")
	}
}

// -- QueryEnrollment ----------------------------------------------------

func TestQueryEnrollment_Enrolled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	identity := RepoIdentity{Repo: "owner/name", Dir: "/abs/dir"}
	if _, err := EnrollRepo(path, identity); err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}

	got, err := QueryEnrollment(path, identity.Repo)
	if err != nil {
		t.Fatalf("QueryEnrollment: %v", err)
	}
	want := RepoEnrollment{Repo: identity.Repo, Dir: identity.Dir, Enrolled: true}
	if got != want {
		t.Errorf("QueryEnrollment = %+v, want %+v", got, want)
	}
}

func TestQueryEnrollment_NotEnrolled(t *testing.T) {
	t.Run("file exists but repo absent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if _, err := EnrollRepo(path, RepoIdentity{Repo: "owner/other", Dir: "/dir/other"}); err != nil {
			t.Fatalf("EnrollRepo: %v", err)
		}

		got, err := QueryEnrollment(path, "owner/name")
		if err != nil {
			t.Fatalf("QueryEnrollment: %v", err)
		}
		if got.Enrolled {
			t.Errorf("QueryEnrollment = %+v, want Enrolled=false", got)
		}
	})

	t.Run("missing config file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")

		got, err := QueryEnrollment(path, "owner/name")
		if err != nil {
			t.Fatalf("QueryEnrollment: want no error for a missing config file, got %v", err)
		}
		if got.Enrolled {
			t.Errorf("QueryEnrollment = %+v, want Enrolled=false for a missing config file", got)
		}
	})
}

// -- DetectRepoIdentity ---------------------------------------------------

func TestDetectRepoIdentity_URLForms(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"scp-style ssh with .git", "git@github.com:owner/name.git"},
		{"scp-style ssh without .git", "git@gitlab.example.com:owner/name"},
		{"https with .git", "https://github.com/owner/name.git"},
		{"https without .git", "https://gitlab.example.com/owner/name"},
		{"https with trailing slash", "https://github.com/owner/name/"},
		{"https with .git and trailing slash", "https://github.com/owner/name.git/"},
		{"https with user creds and .git", "https://user@github.com/owner/name.git"},
		{"https with user creds without .git", "https://user@gitlab.example.com/owner/name"},
		{"ssh:// scheme with .git", "ssh://git@github.com/owner/name.git"},
		{"ssh:// scheme without .git", "ssh://git@gitlab.example.com/owner/name"},
		{"ssh:// scheme with trailing slash", "ssh://git@github.com/owner/name/"},
		{"nested group path uses last two segments", "https://gitlab.example.com/group/subgroup/owner/name.git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initGitRemote(t, tt.url)

			id, err := DetectRepoIdentity(dir)
			if err != nil {
				t.Fatalf("DetectRepoIdentity(%q) [remote %q]: %v", dir, tt.url, err)
			}
			if id.Repo != "owner/name" {
				t.Errorf("Repo = %q, want %q", id.Repo, "owner/name")
			}

			wantDir, err := filepath.Abs(dir)
			if err != nil {
				t.Fatalf("filepath.Abs: %v", err)
			}
			if id.Dir != wantDir {
				t.Errorf("Dir = %q, want %q", id.Dir, wantDir)
			}
			if !filepath.IsAbs(id.Dir) {
				t.Errorf("Dir = %q, want an absolute path", id.Dir)
			}
		})
	}
}

func TestDetectRepoIdentity_Errors(t *testing.T) {
	t.Run("no .git dir", func(t *testing.T) {
		dir := t.TempDir() // no git init at all

		if _, err := DetectRepoIdentity(dir); err == nil {
			t.Errorf("DetectRepoIdentity: want error for a dir with no .git")
		}
	})

	t.Run("no origin remote", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init")

		if _, err := DetectRepoIdentity(dir); err == nil {
			t.Errorf("DetectRepoIdentity: want error when no origin remote is configured")
		}
	})

	t.Run("unparseable remote", func(t *testing.T) {
		dir := initGitRemote(t, "not-a-valid-remote-url")

		if _, err := DetectRepoIdentity(dir); err == nil {
			t.Errorf("DetectRepoIdentity: want error for an unparseable remote URL")
		}
	})

	t.Run("remote with a single path segment", func(t *testing.T) {
		dir := initGitRemote(t, "https://github.com/justonesegment")

		if _, err := DetectRepoIdentity(dir); err == nil {
			t.Errorf("DetectRepoIdentity: want error when the remote has fewer than two path segments")
		}
	})
}

// -- writeRawConfig symlink-follow hardening -------------------------------

// TestEnrollRepo_DoesNotFollowPreplantedTmpSymlink guards against a
// symlink-follow attack on writeRawConfig's temp-file write: if an attacker
// (or another process) can write to the config directory, they could
// pre-plant a symlink at the historically predictable "config.json.tmp" path
// pointing at a sentinel file elsewhere. A vulnerable writeRawConfig that does
// os.WriteFile(path+".tmp", ...) would follow that symlink and clobber the
// sentinel. writeRawConfig must instead use a randomized temp name (e.g. via
// os.CreateTemp), so the pre-planted symlink is never touched and the real
// config write still lands at path.
func TestEnrollRepo_DoesNotFollowPreplantedTmpSymlink(t *testing.T) {
	base := t.TempDir()
	configDir := filepath.Join(base, "cfg")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir configDir: %v", err)
	}

	sentinel := filepath.Join(base, "sentinel.txt")
	const sentinelContent = "do not touch me\n"
	if err := os.WriteFile(sentinel, []byte(sentinelContent), 0o600); err != nil {
		t.Fatalf("writing sentinel: %v", err)
	}

	path := filepath.Join(configDir, "config.json")
	// Pre-plant a symlink at the old, predictable ".tmp" path, pointing
	// outside the config directory at the sentinel file.
	if err := os.Symlink(sentinel, path+".tmp"); err != nil {
		t.Fatalf("symlinking predictable tmp path to sentinel: %v", err)
	}

	identity := RepoIdentity{Repo: "owner/name", Dir: "/abs/dir"}
	changed, err := EnrollRepo(path, identity)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	if !changed {
		t.Fatalf("EnrollRepo: want changed=true")
	}

	sentinelAfter, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("reading sentinel after EnrollRepo: %v", err)
	}
	if string(sentinelAfter) != sentinelContent {
		t.Fatalf("sentinel content changed: want %q, got %q (writeRawConfig followed the pre-planted symlink)", sentinelContent, sentinelAfter)
	}

	cfg := mustDecodeConfig(t, path)
	if !reposContains(cfg.Dispatch.Repos, identity.Repo, identity.Dir) {
		t.Errorf("dispatch.repos = %+v, want to contain %+v", cfg.Dispatch.Repos, identity)
	}
}

// -- Empty-path parity ----------------------------------------------------

func TestEmptyPath_ResolvesDefaultConfigPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	wantPath := run.DefaultConfigPath()
	if wantPath == "" {
		t.Fatal("run.DefaultConfigPath() returned empty path with XDG_CONFIG_HOME set")
	}

	identity := RepoIdentity{Repo: "owner/name", Dir: "/abs/dir"}

	changed, err := EnrollRepo("", identity)
	if err != nil {
		t.Fatalf("EnrollRepo(\"\", ...): %v", err)
	}
	if !changed {
		t.Fatalf("EnrollRepo(\"\", ...): want changed=true")
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("EnrollRepo(\"\", ...) did not write to run.DefaultConfigPath() (%s): %v", wantPath, err)
	}

	got, err := QueryEnrollment("", identity.Repo)
	if err != nil {
		t.Fatalf("QueryEnrollment(\"\", ...): %v", err)
	}
	if !got.Enrolled {
		t.Errorf("QueryEnrollment(\"\", ...) = %+v, want Enrolled=true", got)
	}

	changed, err = UnenrollRepo("", identity.Repo)
	if err != nil {
		t.Fatalf("UnenrollRepo(\"\", ...): %v", err)
	}
	if !changed {
		t.Errorf("UnenrollRepo(\"\", ...): want changed=true")
	}
}
