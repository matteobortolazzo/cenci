package launcher

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// Engine bundles what every launcher operation needs: the resolved container
// runtime, the sandbox asset directory, the content-hash base tag derived
// from it, and the stdio streams output/prompts flow through.
type Engine struct {
	Runtime  string
	AssetDir string
	BaseTag  string
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
}

// New resolves the container runtime (podman preferred, matching cenci-sand),
// the sandbox asset directory, and the base tag, and returns a ready Engine.
func New(stdin io.Reader, stdout, stderr io.Writer) (*Engine, error) {
	runtime, err := sandbox.ContainerRuntime()
	if err != nil {
		return nil, err
	}
	assetDir, err := ResolveAssetDir()
	if err != nil {
		return nil, err
	}
	tag, err := BaseTag(assetDir)
	if err != nil {
		return nil, err
	}
	return &Engine{
		Runtime:  runtime,
		AssetDir: assetDir,
		BaseTag:  tag,
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

// BaseImage is the content-hash-tagged base image reference.
func (e *Engine) BaseImage() string {
	return baseImageRepo + ":" + e.BaseTag
}

// command builds a runtime invocation with stdout/stderr wired to the
// engine's streams (build progress, warnings).
func (e *Engine) command(args ...string) *exec.Cmd {
	cmd := exec.Command(e.Runtime, args...)
	cmd.Stdout = e.Stdout
	cmd.Stderr = e.Stderr
	return cmd
}

// imageExists reports whether the runtime knows the image, quietly (`image
// inspect` output discarded, matching cenci-sand's &>/dev/null probes).
func (e *Engine) imageExists(image string) bool {
	return exec.Command(e.Runtime, "image", "inspect", image).Run() == nil
}

// BuildBase builds the content-hash-tagged base image (plus the :latest
// alias tag) from Dockerfile.base.
func (e *Engine) BuildBase() error {
	_, _ = fmt.Fprintf(e.Stdout, "Building %s with %s...\n", e.BaseImage(), e.Runtime)
	cmd := e.command("build",
		"-f", filepath.Join(e.AssetDir, "Dockerfile.base"),
		"-t", e.BaseImage(),
		"-t", baseImageRepo+":latest",
		e.AssetDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build %s: %w", e.Runtime, e.BaseImage(), err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done.")
	return nil
}

// ensureBaseImage builds the base image only when the current content-hash
// tag is missing.
func (e *Engine) ensureBaseImage() error {
	if e.imageExists(e.BaseImage()) {
		return nil
	}
	return e.BuildBase()
}

// BuildMonolith builds the shared cenci-sandbox:latest image FROM the base,
// building the base first if its current tag is missing.
func (e *Engine) BuildMonolith() error {
	if err := e.ensureBaseImage(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Building %s with %s...\n", MonolithImage, e.Runtime)
	cmd := e.command("build",
		"--build-arg", "BASE_VERSION="+e.BaseTag,
		"-t", MonolithImage,
		"-f", filepath.Join(e.AssetDir, "Dockerfile"),
		e.AssetDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build %s: %w", e.Runtime, MonolithImage, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done.")
	return nil
}

// BuildRepoImage builds a repo's own image from <repo-root>/.cenci/Dockerfile,
// building the base first if its current tag is missing.
func (e *Engine) BuildRepoImage(repoRoot, image string) error {
	if err := e.ensureBaseImage(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Building %s with %s...\n", image, e.Runtime)
	cmd := e.command("build",
		"--build-arg", "BASE_VERSION="+e.BaseTag,
		"-t", image,
		"-f", filepath.Join(repoRoot, ".cenci", "Dockerfile"),
		filepath.Join(repoRoot, ".cenci"))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build %s: %w", e.Runtime, image, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done.")
	return nil
}

// BuildSelected builds whichever image the scope selected: the repo's own
// image when it opted in via .cenci/Dockerfile, otherwise the monolith.
func (e *Engine) BuildSelected(scope Scope) error {
	if scope.UsingRepoImage {
		return e.BuildRepoImage(scope.RepoRoot, scope.Image)
	}
	return e.BuildMonolith()
}

// EnsureImage builds the scope's image only when it is missing.
func (e *Engine) EnsureImage(scope Scope) error {
	if e.imageExists(scope.Image) {
		return nil
	}
	return e.BuildSelected(scope)
}

// containerRunning reports whether a container with exactly this name is
// currently running.
func (e *Engine) containerRunning(name string) (bool, error) {
	out, err := exec.Command(e.Runtime, "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		return false, fmt.Errorf("%s ps: %w", e.Runtime, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == name {
			return true, nil
		}
	}
	return false, nil
}

// UpdatePlugins provisions anything missing, then refreshes plugins with
// ttl 0 (bypassing the entrypoint's 30-minute stamp) inside the running
// container, or in a one-shot container against the home volume. Claude uses
// its host-mounted binary; Codex uses the CLI baked into the image.
func (e *Engine) UpdatePlugins(agent string, scope Scope) error {
	var updateCmd string
	if agent == "codex" {
		updateCmd = `source /usr/local/bin/lib/migrate-settings.sh && provision_codex_plugins /home/dev/.codex cenci matteobortolazzo/cenci cenci cenci-watch && update_codex_plugins /home/dev/.codex cenci 0 cenci cenci-watch`
	} else {
		updateCmd = `source /usr/local/bin/lib/migrate-settings.sh && heal_plugin_installs /home/dev/.claude/plugins && provision_plugins /home/dev/.claude/plugins cenci matteobortolazzo/cenci cenci cenci-watch && update_plugins /home/dev/.claude/plugins cenci 0 cenci cenci-watch`
	}

	running, err := e.containerRunning(scope.ContainerName)
	if err != nil {
		return err
	}
	if running {
		_, _ = fmt.Fprintf(e.Stdout, "Updating plugins in running '%s'...\n", scope.ContainerName)
		cmd := e.command("exec", "-u", "dev", scope.ContainerName, "/bin/bash", "-c", updateCmd)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s exec %s: %w", e.Runtime, scope.ContainerName, err)
		}
		_, _ = fmt.Fprintln(e.Stdout, "Done. Agent sessions pick up the update on their next start.")
		return nil
	}

	_, _ = fmt.Fprintf(e.Stdout, "Updating plugins in volume '%s'...\n", scope.VolumeName)
	args := []string{"run", "--rm", "--entrypoint", "/bin/bash",
		"-e", "CENCI_SANDBOX_AGENT=" + agent,
		"-v", scope.VolumeName + ":/home/dev"}
	if agent == "claude" {
		claudeBin, err := resolveHostBinary("claude")
		if err != nil {
			return fmt.Errorf("--update-plugins needs the claude binary on the host for Claude sandboxes")
		}
		args = append(args, "-v", claudeBin+":/usr/local/bin/claude:ro")
	}
	args = append(args, scope.Image, "-c", updateCmd)
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s run (update-plugins): %w", e.Runtime, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done.")
	return nil
}

// resolveHostBinary resolves name from PATH and follows symlinks to the real
// file (readlink -f), matching how cenci-sand resolves host binaries before
// bind-mounting them into the container.
func resolveHostBinary(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return resolved, nil
}
