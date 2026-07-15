package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"my-project", "my-project"},
		{"MyProject", "myproject"},
		{"My Project", "my-project"},
		{"a/b", "a-b"},
		{"under_score.dot-dash", "under_score.dot-dash"},
		// One dash per RUNE, not per byte: É (2 bytes) → one dash.
		{"Émoji Repo", "-moji-repo"},
		{"naïve", "na-ve"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := Slugify(tc.in); got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestComputeWorkdir(t *testing.T) {
	if got := ComputeWorkdir("/home/u/repo", "/home/u/repo"); got != "/workspace" {
		t.Errorf("repo root: got %q, want /workspace", got)
	}
	if got := ComputeWorkdir("/home/u/repo", "/home/u/repo/sub/dir"); got != "/workspace/sub/dir" {
		t.Errorf("subdir: got %q, want /workspace/sub/dir", got)
	}
}

func TestComputeLegacyWorkdir(t *testing.T) {
	if got := ComputeLegacyWorkdir("/home/u/Repos", "/home/u/Repos/proj"); got != "/workspace/proj" {
		t.Errorf("inside: got %q, want /workspace/proj", got)
	}
	if got := ComputeLegacyWorkdir("/home/u/Repos", "/somewhere/else"); got != "/workspace" {
		t.Errorf("outside: got %q, want /workspace", got)
	}
}

func TestHasRepoImageAndSelectImage(t *testing.T) {
	repo := t.TempDir()
	if HasRepoImage(repo) {
		t.Error("HasRepoImage true without .cenci/Dockerfile")
	}
	if got := SelectImage(repo, "proj"); got != "cenci-sandbox:latest" {
		t.Errorf("SelectImage without repo Dockerfile = %q, want cenci-sandbox:latest", got)
	}

	writeFile(t, filepath.Join(repo, ".cenci", "Dockerfile"), "FROM cenci-sandbox-base:latest\n")
	if !HasRepoImage(repo) {
		t.Error("HasRepoImage false with .cenci/Dockerfile present")
	}
	if got := SelectImage(repo, "proj"); got != "cenci-sandbox-proj:latest" {
		t.Errorf("SelectImage with repo Dockerfile = %q, want cenci-sandbox-proj:latest", got)
	}
}

func TestComputeScope_LegacyMode(t *testing.T) {
	// A bare temp dir is not a git repo, so scope falls back to the legacy
	// shared-workspace scheme.
	cwd := t.TempDir()
	home := t.TempDir()

	scope := ComputeScope("claude", "", cwd, home)
	if scope.WorkspaceScope != "legacy" {
		t.Fatalf("WorkspaceScope = %q, want legacy", scope.WorkspaceScope)
	}
	if scope.ContainerName != "claude-cenci-default" {
		t.Errorf("ContainerName = %q, want claude-cenci-default", scope.ContainerName)
	}
	if scope.VolumeName != "claude-cenci-home-default" {
		t.Errorf("VolumeName = %q, want claude-cenci-home-default", scope.VolumeName)
	}
	if scope.Hostname != "sandbox-default" {
		t.Errorf("Hostname = %q, want sandbox-default", scope.Hostname)
	}
	if scope.Image != "cenci-sandbox:latest" {
		t.Errorf("Image = %q, want cenci-sandbox:latest", scope.Image)
	}
	if want := filepath.Join(home, "Repos"); scope.WorkspaceBindHost != want {
		t.Errorf("WorkspaceBindHost = %q, want %q", scope.WorkspaceBindHost, want)
	}
	if scope.Workdir != "/workspace" {
		t.Errorf("Workdir = %q, want /workspace (cwd outside workspace host)", scope.Workdir)
	}

	named := ComputeScope("codex", "mybox", cwd, home)
	if named.ContainerName != "codex-cenci-mybox" {
		t.Errorf("named ContainerName = %q, want codex-cenci-mybox", named.ContainerName)
	}
	if named.VolumeName != "codex-cenci-home-mybox" {
		t.Errorf("named VolumeName = %q, want codex-cenci-home-mybox", named.VolumeName)
	}
}

func TestComputeScope_RepoMode(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	parent := t.TempDir()
	repo := filepath.Join(parent, "My Repo")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// macOS TempDir may be a symlink; resolve like git rev-parse does.
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	home := t.TempDir()

	scope := ComputeScope("claude", "", filepath.Join(resolved, "sub"), home)
	if scope.WorkspaceScope != "repo" {
		t.Fatalf("WorkspaceScope = %q, want repo", scope.WorkspaceScope)
	}
	if scope.ContainerName != "claude-cenci-my-repo" {
		t.Errorf("ContainerName = %q, want claude-cenci-my-repo", scope.ContainerName)
	}
	if scope.VolumeName != "claude-cenci-home-my-repo" {
		t.Errorf("VolumeName = %q, want claude-cenci-home-my-repo", scope.VolumeName)
	}
	if scope.Hostname != "sandbox-my-repo" {
		t.Errorf("Hostname = %q, want sandbox-my-repo", scope.Hostname)
	}
	if scope.RepoRoot != resolved {
		t.Errorf("RepoRoot = %q, want %q", scope.RepoRoot, resolved)
	}
	if scope.WorkspaceBindHost != resolved {
		t.Errorf("WorkspaceBindHost = %q, want %q", scope.WorkspaceBindHost, resolved)
	}
	if scope.Workdir != "/workspace/sub" {
		t.Errorf("Workdir = %q, want /workspace/sub", scope.Workdir)
	}
	if scope.Image != "cenci-sandbox:latest" || scope.UsingRepoImage {
		t.Errorf("Image/UsingRepoImage = %q/%v, want monolith/false", scope.Image, scope.UsingRepoImage)
	}

	named := ComputeScope("claude", "feature", resolved, home)
	if named.ContainerName != "claude-cenci-my-repo-feature" {
		t.Errorf("named ContainerName = %q, want claude-cenci-my-repo-feature", named.ContainerName)
	}
	if named.Workdir != "/workspace" {
		t.Errorf("repo-root Workdir = %q, want /workspace", named.Workdir)
	}

	writeFile(t, filepath.Join(repo, ".cenci", "Dockerfile"), "FROM cenci-sandbox-base:latest\n")
	withImage := ComputeScope("claude", "", resolved, home)
	if !withImage.UsingRepoImage || withImage.Image != "cenci-sandbox-my-repo:latest" {
		t.Errorf("repo image scope = %q/%v, want cenci-sandbox-my-repo:latest/true", withImage.Image, withImage.UsingRepoImage)
	}
}
