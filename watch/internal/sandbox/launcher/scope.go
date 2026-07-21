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
	// DindVolumeName is the per-repo dind storage volume name
	// (<agent>-cenci-dind-<slug>), populated in repo mode only; empty in
	// legacy mode, since dind is only ever available in repo scope (#585).
	DindVolumeName string
}

// AgentCLIVolumeName is the host-global, per-agent CLI volume. It is
// deliberately independent of repositories and named sandbox instances.
func AgentCLIVolumeName(agent string) string {
	return "cenci-agent-cli-" + agent
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
	if cwd == workspaceHost {
		return workspaceContainer
	}
	if strings.HasPrefix(cwd, workspaceHost+string(filepath.Separator)) {
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
		containerName := prefix + "-" + slug + suffix
		return Scope{
			ContainerName:     containerName,
			VolumeName:        volumeNameForContainer(containerName),
			Hostname:          "sandbox-" + slug + suffix,
			Image:             SelectImage(repoRoot, slug),
			UsingRepoImage:    HasRepoImage(repoRoot),
			WorkspaceBindHost: repoRoot,
			Workdir:           ComputeWorkdir(repoRoot, cwd),
			WorkspaceScope:    "repo",
			RepoRoot:          repoRoot,
			DindVolumeName:    dindVolumeNameForContainer(containerName),
		}
	}

	name := instanceName
	if name == "" {
		name = "default"
	}
	containerName := prefix + "-" + name
	workspaceHost := filepath.Join(home, "Repos")
	return Scope{
		ContainerName:     containerName,
		VolumeName:        volumeNameForContainer(containerName),
		Hostname:          "sandbox-" + name,
		Image:             MonolithImage,
		WorkspaceBindHost: workspaceHost,
		Workdir:           ComputeLegacyWorkdir(workspaceHost, cwd),
		WorkspaceScope:    "legacy",
	}
}

// cenciMarker is the "-cenci-" separator every sandbox container/volume name
// pair is namespaced around ("${agent}-cenci-<slug>" / "${agent}-cenci-home-
// <slug>"; see docs/cli-conventions.md's runtime object naming table).
const cenciMarker = "-cenci-"

// volumeNameForContainer derives a container's home-volume name from its
// container name by inserting "home-" right after the shared cenciMarker
// both ComputeScope and ScopeForContainer build container/volume name pairs
// around (e.g. "claude-cenci-myrepo" -> "claude-cenci-home-myrepo"). Returns
// "" when containerName carries no cenciMarker to split on — there is no
// reliable insertion point to guess at.
func volumeNameForContainer(containerName string) string {
	idx := strings.Index(containerName, cenciMarker)
	if idx < 0 {
		return ""
	}
	return containerName[:idx] + cenciMarker + "home-" + containerName[idx+len(cenciMarker):]
}

// dindVolumeNameForContainer mirrors volumeNameForContainer for the dind
// storage volume: it inserts "dind-" right after the shared cenciMarker
// instead of "home-" (e.g. "claude-cenci-myrepo" ->
// "claude-cenci-dind-myrepo"). Returns "" when containerName carries no
// cenciMarker to split on.
func dindVolumeNameForContainer(containerName string) string {
	idx := strings.Index(containerName, cenciMarker)
	if idx < 0 {
		return ""
	}
	return containerName[:idx] + cenciMarker + "dind-" + containerName[idx+len(cenciMarker):]
}

// ScopeForContainer derives a Scope directly from an already-known container
// name and image (as reported by sandbox.ListContainers), without resolving
// any cwd/repo. It is the support-bundle collection path (ticket #573):
// unlike ComputeScope, which derives scope from the current cwd/repo,
// ScopeForContainer reports on containers it never launched, so
// Workspace/RepoRoot-related fields stay zero rather than fabricating a
// value that doesn't apply.
func ScopeForContainer(containerName, image string) Scope {
	return Scope{
		ContainerName: containerName,
		VolumeName:    volumeNameForContainer(containerName),
		Image:         image,
	}
}
