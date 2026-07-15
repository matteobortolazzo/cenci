package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Scope is the per-launch namespacing cenci-sand derives before any container
// work: names, image, workspace mount, and workdir. Inside a git repo,
// containers/volumes/images are namespaced per repo so each project gets its
// own isolated sandbox and, optionally, its own image
// (<repo-root>/.cenci/Dockerfile). Outside a git repo, the legacy
// shared-workspace scheme applies (whole ~/Repos mount, one container per
// instance name) so existing claude-cenci-default-style volumes keep working
// untouched.
type Scope struct {
	ContainerName     string
	VolumeName        string
	Hostname          string
	Image             string
	UsingRepoImage    bool
	WorkspaceBindHost string
	Workdir           string
	// WorkspaceScope is the WORKSPACE_SCOPE env value the entrypoint
	// receives: "repo" or "legacy".
	WorkspaceScope string
	// RepoRoot is the git toplevel in repo mode, empty in legacy mode.
	RepoRoot string
}

// workspaceContainer is the container-side workspace mount point.
const workspaceContainer = "/workspace"

// Slugify lowercases the input and replaces each rune outside [a-z0-9_.-]
// with a dash, turning a repo directory name into a container/volume/image
// safe suffix. It iterates runes (not bytes) so a multi-byte character maps
// to exactly one dash, matching the bash implementation's LC_ALL=C.UTF-8
// character-at-a-time behavior.
func Slugify(input string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(input) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ResolveRepoRoot returns the absolute root of the git repo containing cwd,
// or an error when cwd isn't inside a git repo. Callers use the error to pick
// between the per-repo scheme and the legacy fallback.
func ResolveRepoRoot(cwd string) (string, error) {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ComputeWorkdir maps a host cwd inside repoRoot to the equivalent path under
// the container's /workspace mount: "/workspace" for the repo root itself,
// "/workspace/<relative-subpath>" for a subdirectory.
func ComputeWorkdir(repoRoot, cwd string) string {
	if cwd == repoRoot {
		return workspaceContainer
	}
	return workspaceContainer + strings.TrimPrefix(cwd, repoRoot)
}

// ComputeLegacyWorkdir maps a host cwd to the equivalent path under the
// container's /workspace mount for the non-git fallback scheme (whole ~/Repos
// mount): "/workspace/<relative-subpath>" when cwd is under workspaceHost,
// "/workspace" otherwise.
func ComputeLegacyWorkdir(workspaceHost, cwd string) string {
	if strings.HasPrefix(cwd, workspaceHost) {
		return workspaceContainer + strings.TrimPrefix(cwd, workspaceHost)
	}
	return workspaceContainer
}

// HasRepoImage reports whether repoRoot opts into its own image via a
// Dockerfile at <repo-root>/.cenci/Dockerfile.
func HasRepoImage(repoRoot string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, ".cenci", "Dockerfile"))
	return err == nil && info.Mode().IsRegular()
}

// SelectImage returns the image tag to use for this repo: the per-repo image
// when <repo-root>/.cenci/Dockerfile exists, otherwise the shared monolith.
func SelectImage(repoRoot, repoSlug string) string {
	if HasRepoImage(repoRoot) {
		return "cenci-sandbox-" + repoSlug + ":latest"
	}
	return MonolithImage
}

// ComputeScope derives the full launch scope for agent from cwd. An empty
// instanceName means "--name was not given": in repo mode no suffix is
// appended, in legacy mode the name defaults to "default" — both exactly as
// cenci-sand's INSTANCE_NAME/NAME_GIVEN pair behaves.
func ComputeScope(agent, instanceName, cwd, home string) Scope {
	prefix := agent + "-cenci"

	if repoRoot, err := ResolveRepoRoot(cwd); err == nil {
		slug := Slugify(filepath.Base(repoRoot))
		suffix := ""
		if instanceName != "" {
			suffix = "-" + instanceName
		}
		return Scope{
			ContainerName:     prefix + "-" + slug + suffix,
			VolumeName:        prefix + "-home-" + slug + suffix,
			Hostname:          "sandbox-" + slug + suffix,
			Image:             SelectImage(repoRoot, slug),
			UsingRepoImage:    HasRepoImage(repoRoot),
			WorkspaceBindHost: repoRoot,
			Workdir:           ComputeWorkdir(repoRoot, cwd),
			WorkspaceScope:    "repo",
			RepoRoot:          repoRoot,
		}
	}

	name := instanceName
	if name == "" {
		name = "default"
	}
	workspaceHost := filepath.Join(home, "Repos")
	return Scope{
		ContainerName:     prefix + "-" + name,
		VolumeName:        prefix + "-home-" + name,
		Hostname:          "sandbox-" + name,
		Image:             MonolithImage,
		WorkspaceBindHost: workspaceHost,
		Workdir:           ComputeLegacyWorkdir(workspaceHost, cwd),
		WorkspaceScope:    "legacy",
	}
}
