package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

const (
	imageAgentLifecycleLabel = "cenci.agent-cli"
	imageAgentLifecycleValue = "shared-v2"
	imageBaseVersionLabel    = "cenci.base-version"
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
	e, err := newEngineBase(stdin, stdout, stderr)
	if err != nil {
		return nil, err
	}
	e.Runtime = runtime
	return e, nil
}

// NewForLaunch resolves the sandbox asset directory and base tag, like New,
// but leaves Runtime unset (""). The interactive `cenci open` path only
// knows whether dind mode is on after ResolveDind runs (it decides between
// docker-first and podman-first runtime resolution), which needs the launch
// scope Launch itself computes — so runtime resolution happens inside Launch
// instead of eagerly here (#585).
func NewForLaunch(stdin io.Reader, stdout, stderr io.Writer) (*Engine, error) {
	return newEngineBase(stdin, stdout, stderr)
}

// NewForAudit returns a minimal Engine suitable for Audit: only stdio, with
// Stderr always set to io.Discard so assembleOptionalFeatures' --host-network
// warning is never printed while Audit is only classifying its emitted
// tokens into a report. Unlike New/NewForLaunch, it performs no asset-dir/
// base-tag/container-runtime resolution — Audit is entirely read-only and
// never shells out to the container runtime or needs the sandbox asset dir.
func NewForAudit(stdin io.Reader, stdout io.Writer) *Engine {
	return &Engine{Stdin: stdin, Stdout: stdout, Stderr: io.Discard}
}

// newEngineBase resolves the sandbox asset directory and the base tag,
// returning an Engine with everything except Runtime populated. Shared by
// New (which resolves Runtime eagerly) and NewForLaunch (which leaves it for
// the caller to resolve later).
func newEngineBase(stdin io.Reader, stdout, stderr io.Writer) (*Engine, error) {
	assetDir, err := ResolveAssetDir()
	if err != nil {
		return nil, err
	}
	tag, err := BaseTag(assetDir)
	if err != nil {
		return nil, err
	}
	return &Engine{
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

// imageExists reports whether the runtime lists the exact image. A successful
// empty listing means missing; a listing failure remains an infrastructure
// error instead of being collapsed into "missing".
func (e *Engine) imageExists(image string) (bool, error) {
	out, err := exec.Command(e.Runtime, "images", "--format", "{{.Repository}}:{{.Tag}}", image).Output()
	if err != nil {
		return false, fmt.Errorf("%s images: %w", e.Runtime, err)
	}
	for _, line := range splitLines(string(out)) {
		if line == image {
			return true, nil
		}
	}
	return false, nil
}

// imageCurrent reports whether an image contains the shared-v2 agent helper
// contract and was built against the engine's current BaseTag. Mutable
// :latest tags from older releases (stale agent-cli label) or from before the
// current base-image content hash (stale cenci.base-version label) are
// treated as not current so the caller rebuilds them. baseDrift distinguishes
// the two: it is true only when the agent-cli label is current AND the baked
// base-version label is present but provably no longer matches e.BaseTag, so
// callers can print a "rebuilding due to base drift" notice specifically for
// that case (never for a missing image, a stale agent-cli label, or a legacy
// image built before this label existed at all — an absent base-version
// label is a plain missing-freshness rebuild, not provable drift).
func (e *Engine) imageCurrent(image string) (current bool, baseDrift bool, err error) {
	exists, err := e.imageExists(image)
	if err != nil || !exists {
		return false, false, err
	}
	out, err := exec.Command(e.Runtime, "image", "inspect", "--format",
		`{{ index .Config.Labels "`+imageAgentLifecycleLabel+`" }}|{{ index .Config.Labels "`+imageBaseVersionLabel+`" }}`, image).Output()
	if err != nil {
		return false, false, fmt.Errorf("%s image inspect %s: %w", e.Runtime, image, err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) < 2 {
		_, _ = fmt.Fprintf(e.Stderr, "Warning: %s image inspect %s returned unparsable output (no agent-cli|base-version separator); treating as not current.\n", e.Runtime, image)
		return false, false, nil
	}
	agentCLI, baseVersion := parts[0], parts[1]
	if agentCLI != imageAgentLifecycleValue {
		return false, false, nil
	}
	if baseVersion == "" {
		return false, false, nil
	}
	if baseVersion != e.BaseTag {
		return false, true, nil
	}
	return true, false, nil
}

// printPluginRefreshHint reminds after a user-invoked build that the
// pipeline plugins live in the per-scope home volumes, not in image layers —
// rebuilding an image never refreshes them.
func (e *Engine) printPluginRefreshHint() {
	_, _ = fmt.Fprintln(e.Stdout, "Note: pipeline plugins (cenci, cenci-watch) are provisioned per home volume, not baked into images — run `cenci sandbox update-plugins` to refresh existing sandboxes.")
}

// BuildBase builds the content-hash-tagged base image (plus the :latest
// alias tag) from Dockerfile.base.
func (e *Engine) BuildBase() error {
	if err := e.buildBase(); err != nil {
		return err
	}
	e.printPluginRefreshHint()
	return nil
}

// buildBase is BuildBase without the plugin-refresh hint, shared with
// ensureBaseImage so an implicit base build under BuildMonolith or
// BuildRepoImage prints the hint exactly once, from the outer build.
func (e *Engine) buildBase() error {
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
	exists, err := e.imageExists(e.BaseImage())
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return e.buildBase()
}

// BuildMonolith builds the shared cenci-sandbox:latest image FROM the base,
// building the base first if its current tag is missing. Agent CLIs live in
// persistent home volumes and are never image build inputs.
func (e *Engine) BuildMonolith() error {
	if err := e.ensureBaseImage(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Building %s with %s...\n", MonolithImage, e.Runtime)
	args := []string{"build", "--build-arg", "BASE_VERSION=" + e.BaseTag,
		"--label", imageAgentLifecycleLabel + "=" + imageAgentLifecycleValue,
		"--label", imageBaseVersionLabel + "=" + e.BaseTag,
		"-t", MonolithImage, "-f", filepath.Join(e.AssetDir, "Dockerfile"), e.AssetDir}
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s build %s: %w", e.Runtime, MonolithImage, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done.")
	e.printPluginRefreshHint()
	return nil
}

// BuildRepoImage builds a repo's own image from <repo-root>/.cenci/Dockerfile,
// building the base first if its current tag is missing.
func (e *Engine) BuildRepoImage(repoRoot, image string) error {
	if err := e.ensureBaseImage(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Building %s with %s...\n", image, e.Runtime)
	args := []string{"build", "--build-arg", "BASE_VERSION=" + e.BaseTag,
		"--label", imageAgentLifecycleLabel + "=" + imageAgentLifecycleValue,
		"--label", imageBaseVersionLabel + "=" + e.BaseTag,
		"-t", image, "-f", filepath.Join(repoRoot, ".cenci", "Dockerfile"), filepath.Join(repoRoot, ".cenci")}
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s build %s: %w", e.Runtime, image, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done.")
	e.printPluginRefreshHint()
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

// CheckSelected reports whether the image scope selected (the repo's own
// image when it opted in via .cenci/Dockerfile, otherwise the monolith) is
// already current, reusing the same imageCurrent freshness gate BuildSelected
// and EnsureImage rely on. It never builds — this is a read-only reporting
// entry point for `cenci sandbox build --check`.
func (e *Engine) CheckSelected(scope Scope) (current bool, err error) {
	current, _, err = e.imageCurrent(scope.Image)
	return current, err
}

// EnsureImage builds the scope's image only when it is missing, its
// agent-cli label is stale, or its baked cenci.base-version label no longer
// matches the engine's current BaseTag (base-image drift). A base-drift
// rebuild prints a notice first so the user understands why the first launch
// after a base change is slower.
func (e *Engine) EnsureImage(scope Scope) error {
	current, baseDrift, err := e.imageCurrent(scope.Image)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	if baseDrift {
		e.printBaseDriftNotice(scope.Image)
	}
	return e.BuildSelected(scope)
}

// EnsureMonolithImage builds the shared monolith image only when its current
// content-hash tag/shared-agent label/base-version label is missing or
// stale, regardless of which image the current launch scope selected. A
// base-drift rebuild prints a notice first (see EnsureImage). The agent-CLI
// updater and the shared volume's populated-check both run exclusively
// against MonolithImage — a trusted image checked into this repo — never a
// per-repo image, so a malicious repo image can never gain root write access
// to the host-global agent CLI volume every sandbox shares.
func (e *Engine) EnsureMonolithImage() error {
	current, baseDrift, err := e.imageCurrent(MonolithImage)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	if baseDrift {
		e.printBaseDriftNotice(MonolithImage)
	}
	return e.BuildMonolith()
}

// printBaseDriftNotice prints a lightweight notice that image's rebuild was
// triggered specifically by base-version drift (as opposed to a missing
// image or a stale agent-cli label), so the user understands why the first
// launch after a base change is slower.
func (e *Engine) printBaseDriftNotice(image string) {
	_, _ = fmt.Fprintf(e.Stdout, "Note: %s was built against an older sandbox base (base-version drift); rebuilding — this first launch after a base change is slower.\n", image)
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

func (e *Engine) volumeExists(name string) (bool, error) {
	out, err := exec.Command(e.Runtime, "volume", "ls", "--format", "{{.Name}}").Output()
	if err != nil {
		return false, fmt.Errorf("%s volume ls: %w", e.Runtime, err)
	}
	for _, line := range splitLines(string(out)) {
		if line == name {
			return true, nil
		}
	}
	return false, nil
}

// agentUpdateRunArgs builds the updater invocation. It always runs against
// MonolithImage (the trusted, checked-in shared image), never a per-repo
// image: repo images are caller-supplied build inputs from
// <repo-root>/.cenci/Dockerfile, and this container runs as root with the
// host-global agent CLI volume mounted read-write, so honoring a repo image
// here would let a malicious repo image gain root write access to the volume
// every sandbox on the host mounts read-only. --cap-drop=ALL and
// --security-opt=no-new-privileges harden the root process without blocking
// network access, which the updater's npm install still needs.
func (e *Engine) agentUpdateRunArgs(agent, version string) []string {
	args := []string{"run", "--rm", "--user", "root",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--entrypoint", "/bin/bash",
		"-v", AgentCLIVolumeName(agent) + ":/opt/cenci-agent",
		MonolithImage, "/usr/local/bin/lib/agent-cli.sh", "update", agent}
	if version != "" {
		args = append(args, version)
	}
	return args
}

// agentVolumeCheckRunArgs builds a cheap, short-lived probe that verifies an
// already-existing shared agent volume actually contains an installed CLI:
// network-isolated (npm access is never needed for a read-only check),
// non-root, read-only volume mount, and hardened the same way as the
// updater. It always runs against MonolithImage for the same reason
// agentUpdateRunArgs does.
func (e *Engine) agentVolumeCheckRunArgs(agent string) []string {
	path := "/opt/cenci-agent/current/node_modules/.bin/" + agent
	return []string{"run", "--rm", "--network", "none", "--user", "dev",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--entrypoint", "/bin/sh",
		"-v", AgentCLIVolumeName(agent) + ":/opt/cenci-agent:ro",
		MonolithImage, "-c", "test -x " + path}
}

// maintenanceRunArgs is retained for plugin maintenance, which legitimately
// needs the credential-bearing home volume. Agent CLI updates never use it.
func (e *Engine) maintenanceRunArgs(agent string, scope Scope, command string) []string {
	return []string{"run", "--rm", "--user", "root",
		"-e", fmt.Sprintf("HOST_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("HOST_GID=%d", os.Getgid()),
		"-e", "CENCI_SANDBOX_AGENT=" + agent,
		"-e", "CENCI_AGENT_CLI=/opt/cenci-agent/current/node_modules/.bin/" + agent,
		"-v", scope.VolumeName + ":/home/dev",
		"-v", AgentCLIVolumeName(agent) + ":/opt/cenci-agent:ro",
		scope.Image, "-c", command}
}

// UpdateAgent updates the host-global agent volume in a short-lived container
// that receives no home/workspace/credential/socket mounts or secret env.
// The updater always runs against MonolithImage (see agentUpdateRunArgs), so
// this ensures that image exists/is current first, independent of whatever
// image the caller's launch scope selected.
func (e *Engine) UpdateAgent(agent, version string) error {
	if err := ValidateAgent(agent); err != nil {
		return err
	}
	if err := e.EnsureMonolithImage(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Updating %s in shared volume '%s' (used by every sandbox on this host)...\n", agent, AgentCLIVolumeName(agent))
	args := e.agentUpdateRunArgs(agent, version)
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s isolated agent updater failed: %w", e.Runtime, err)
	}
	return nil
}

// agentVolumePopulated cheaply verifies that an already-existing shared agent
// volume actually contains an installed CLI. Two real failure modes make
// volumeExists alone untrustworthy: (a) a concurrent first launch's `docker
// run -v` auto-creates the named volume long before that launch's own
// install finishes, so a later launch's volumeExists check can observe a
// still-empty volume as "existing"; (b) a previously failed bootstrap whose
// cleanup `volume rm` itself failed (see EnsureAgentVolume) leaves a broken
// volume that would otherwise be trusted forever. A non-zero exit from the
// probe itself (agent binary missing/not executable) means "not populated";
// any other failure (exec transport, runtime error) is reported as an error
// rather than silently treated as unpopulated.
func (e *Engine) agentVolumePopulated(agent string) (bool, error) {
	if err := e.EnsureMonolithImage(); err != nil {
		return false, err
	}
	err := exec.Command(e.Runtime, e.agentVolumeCheckRunArgs(agent)...).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("%s agent volume check failed: %w", e.Runtime, err)
}

// EnsureAgentVolume bootstraps a genuinely absent shared volume and, for an
// already-existing one, cheaply verifies it is actually populated before
// trusting it (see agentVolumePopulated). Only a missing or verified-empty
// volume triggers UpdateAgent; the updater script's own flock on the
// volume's lock file makes a concurrent/redundant update safe.
func (e *Engine) EnsureAgentVolume(agent string) error {
	name := AgentCLIVolumeName(agent)
	exists, err := e.volumeExists(name)
	if err != nil {
		return err
	}
	if exists {
		populated, err := e.agentVolumePopulated(agent)
		if err != nil {
			return err
		}
		if populated {
			return nil
		}
	}
	if err := e.UpdateAgent(agent, ""); err != nil {
		// The mount created (or left behind) an empty/broken volume. Remove it
		// so the next launch retries bootstrap instead of mistaking a failed
		// install for an existing CLI. A failed removal here leaves the volume
		// broken on the host for every sandbox that mounts it, so tell the
		// operator instead of swallowing it.
		if rmErr := e.command("volume", "rm", name).Run(); rmErr != nil {
			_, _ = fmt.Fprintf(e.Stderr, "Warning: shared agent volume '%s' is broken and could not be removed automatically (%v); every sandbox on this host mounts it, so remove it manually with '%s volume rm %s' before the next launch.\n", name, rmErr, e.Runtime, name)
		}
		return err
	}
	return nil
}

// pluginRefreshCommand returns the in-container ttl-0 plugin refresh command
// (bypassing the entrypoint's 30-minute stamp) for agent, shared by both the
// running-container and one-shot-volume update paths.
func pluginRefreshCommand(agent string) string {
	switch agent {
	case "codex":
		return `source /usr/local/bin/lib/migrate-settings.sh && provision_codex_plugins /home/dev/.codex cenci matteobortolazzo/cenci cenci cenci-watch && update_codex_plugins /home/dev/.codex cenci 0 cenci cenci-watch`
	case "opencode":
		return `source /usr/local/bin/lib/migrate-settings.sh && provision_opencode_plugins /home/dev/.cenci-src matteobortolazzo/cenci && update_opencode_plugins /home/dev/.cenci-src matteobortolazzo/cenci 0`
	}
	return `source /usr/local/bin/lib/migrate-settings.sh && heal_plugin_installs /home/dev/.claude/plugins && provision_plugins /home/dev/.claude/plugins cenci matteobortolazzo/cenci cenci cenci-watch && update_plugins /home/dev/.claude/plugins cenci 0 cenci cenci-watch`
}

// refreshRunningContainerPlugins execs agent's ttl-0 plugin refresh command
// into the already-running containerName. Shared by UpdatePlugins' running
// path and RefreshRunningPlugins.
func (e *Engine) refreshRunningContainerPlugins(agent, containerName string) error {
	_, _ = fmt.Fprintf(e.Stdout, "Updating plugins in running '%s'...\n", containerName)
	cmd := e.command("exec", "-u", "dev", containerName, "/bin/bash", "-c", pluginRefreshCommand(agent))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s exec %s: %w", e.Runtime, containerName, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done. Agent sessions pick up the update on their next start.")
	return nil
}

// UpdatePlugins provisions anything missing, then refreshes plugins with
// ttl 0 (bypassing the entrypoint's 30-minute stamp) inside the running
// container, or in a one-shot container against the home volume.
func (e *Engine) UpdatePlugins(agent string, scope Scope) error {
	if err := ValidateAgent(agent); err != nil {
		return err
	}

	running, err := e.containerRunning(scope.ContainerName)
	if err != nil {
		return err
	}
	if running {
		return e.refreshRunningContainerPlugins(agent, scope.ContainerName)
	}

	_, _ = fmt.Fprintf(e.Stdout, "Updating plugins in volume '%s'...\n", scope.VolumeName)
	args := e.maintenanceRunArgs(agent, scope, pluginRefreshCommand(agent))
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s run (update-plugins): %w", e.Runtime, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done.")
	return nil
}

// RefreshRunningPlugins refreshes plugins in every currently running sandbox
// container on the host (not scoped to the caller's repo, matching the
// scope-independence of the daemon-restart auto-behavior it mirrors),
// inferring each container's agent from its name. Best-effort: a single
// container's failure doesn't stop the rest from refreshing — every failure
// is aggregated into one returned error via errors.Join. Zero running
// containers is not an error; a note is printed instead.
func (e *Engine) RefreshRunningPlugins() error {
	names, err := sandbox.RunningSandboxContainers(e.Runtime, "")
	if err != nil {
		return err
	}
	if len(names) == 0 {
		_, _ = fmt.Fprintln(e.Stdout, "No running sandbox containers to refresh.")
		return nil
	}

	var errs []error
	for _, name := range names {
		agent, ok := sandbox.AgentForContainerName(name)
		if !ok {
			continue
		}
		if err := e.refreshRunningContainerPlugins(agent, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resolveHostBinary resolves name from PATH and follows symlinks to the real
// file (readlink -f). Used to locate the host `cenci` binary to bind-mount for
// in-container wiring; agent CLIs are installed into the persistent home, not
// resolved from the host.
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
