package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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

// NewForAuditWithRuntime returns an Engine suitable for Audit's observed-mode
// dispatch (ticket #627): like NewForAudit, no asset-dir/base-tag resolution
// and Stderr always io.Discard, but it best-effort resolves the container
// runtime via sandbox.ContainerRuntime() so Audit can probe a scoped
// container's actual running state. A resolution failure (no runtime
// installed) degrades to a runtime-less Engine rather than returning an
// error — Audit must still produce a planned-only report when no container
// runtime is available at all, exactly as it always has.
func NewForAuditWithRuntime(stdin io.Reader, stdout io.Writer) *Engine {
	e := NewForAudit(stdin, stdout)
	if runtime, err := sandbox.ContainerRuntime(); err == nil {
		e.Runtime = runtime
	}
	return e
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

// WithRuntime returns a shallow copy of e with Runtime reassigned to
// runtime, sharing every other field (AssetDir, BaseTag, and the stdio
// streams) with e. It is the small helper host-wide/scope-resolving
// callers (sandbox_cmd.go, diagnose_cmd.go, support_bundle_cmd.go) use to
// run a single-runtime-signature engine action (UpdateAgent, UpdatePlugins,
// Diagnose, Prune, ...) against a specific runtime in a caller-side loop,
// without widening Engine.Runtime itself into a set — the launch path
// depends on Engine carrying exactly one resolved runtime (#629).
func (e *Engine) WithRuntime(runtime string) *Engine {
	targeted := *e
	targeted.Runtime = runtime
	return &targeted
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
// rebuilding an image never refreshes them. The plugin list is per-repo
// configurable (sandbox.plugins, #1002), so this no longer names a fixed
// (cenci, cenci-watch) pair.
func (e *Engine) printPluginRefreshHint() {
	_, _ = fmt.Fprintln(e.Stdout, "Note: pipeline plugins are provisioned per home volume, not baked into images — run `cenci sandbox update-plugins` to refresh existing sandboxes.")
}

// parseSingleLineID default-denies a single-value ID probe's output
// (watch/docs/error-handling.md #628/#598): empty after trimming, or more
// than one line, is treated as unparsable rather than a permissive zero
// value. No "sha256:" prefix is required — podman and docker differ on this.
func parseSingleLineID(raw string) (string, error) {
	lines := splitLines(raw)
	if len(lines) != 1 || lines[0] == "" {
		return "", fmt.Errorf("unparsable image ID output %q", raw)
	}
	return lines[0], nil
}

// imageID resolves the create-time content ID of the freshly built image, for
// printStaleContainerNotice's staleness comparison.
func (e *Engine) imageID(image string) (string, error) {
	out, err := exec.Command(e.Runtime, "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return "", fmt.Errorf("%s image inspect %s: %w", e.Runtime, image, err)
	}
	id, err := parseSingleLineID(string(out))
	if err != nil {
		return "", fmt.Errorf("%s image inspect %s: %w", e.Runtime, image, err)
	}
	return id, nil
}

// parseContainerImageLine default-denies the combined per-container inspect
// probe's output: it must be exactly one line splitting into exactly two
// non-empty "|"-separated fields (the reference and the create-time image
// ID). Fewer or more fields, an empty field, or an interior newline are all
// treated as unparsable — never partially trusted (parseObservedInspect's
// exactly-N-field precedent, audit_observed.go).
func parseContainerImageLine(raw string) (ref, id string, err error) {
	lines := splitLines(raw)
	if len(lines) != 1 {
		return "", "", fmt.Errorf("unparsable container image output %q", raw)
	}
	parts := strings.Split(lines[0], "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("unparsable container image output %q", raw)
	}
	return parts[0], parts[1], nil
}

// containerImage reads a running container's create-time image reference and
// resolved image ID via a single combined inspect probe, mirroring
// inspectObservedPosture's multi-field-in-one-probe shape (audit_observed.go).
func (e *Engine) containerImage(name string) (ref, id string, err error) {
	out, err := exec.Command(e.Runtime, "inspect", "--format", "{{.Config.Image}}|{{.Image}}", name).Output()
	if err != nil {
		return "", "", fmt.Errorf("%s inspect %s: %w", e.Runtime, name, err)
	}
	ref, id, err = parseContainerImageLine(string(out))
	if err != nil {
		return "", "", fmt.Errorf("%s inspect %s: %w", e.Runtime, name, err)
	}
	return ref, id, nil
}

// sameImageRef reports whether a container's create-time image reference ref
// names image, tolerating podman's "localhost/" prefix on local images. The
// leading "/" in the suffix check is load-bearing: it pins the match to a
// registry/namespace boundary, so "cenci-sandbox-velka:latest" can never
// suffix-match "cenci-sandbox:latest" — only a genuine "<registry-or-empty>/
// <image>" form matches.
func sameImageRef(ref, image string) bool {
	return ref == image || strings.HasSuffix(ref, "/"+image)
}

// printStaleContainerNotice names, on e.Stdout, any running sandbox
// containers created from image's reference whose create-time image ID no
// longer matches the freshly built image — i.e. containers that will keep
// running the superseded image until they are stopped and relaunched.
//
// The image reference (read via the container's {{.Config.Image}}) is used
// only as a candidate filter, to avoid flagging containers created from an
// entirely different image (e.g. a different repo's image, or the monolith,
// on a multi-repo/multi-purpose host) as a false positive. The staleness
// test itself remains the image-ID comparison — a cache-hit/no-op rebuild
// that reuses the same image ID still prints nothing — so the ticket's
// "Image-ID comparison, not tag comparison" Decision is preserved, not
// contradicted, by also reading the reference.
//
// Every probe failure is non-fatal: it prints a warning to e.Stderr naming
// the failing probe and continues (the build itself already succeeded).
// A per-container probe failure or unparsable output is skipped, not
// treated as "not stale"; a container whose reference simply doesn't match
// image is a silent skip (not a warning) — this rebuild never superseded
// that container's image in the first place.
func (e *Engine) printStaleContainerNotice(image string) {
	freshID, err := e.imageID(image)
	if err != nil {
		_, _ = fmt.Fprintf(e.Stderr, "Warning: could not resolve %s's image ID via %s image inspect (%v); skipping the stale-container check.\n", image, e.Runtime, err)
		return
	}

	names, err := sandbox.RunningSandboxContainers(e.Runtime, "")
	if err != nil {
		_, _ = fmt.Fprintf(e.Stderr, "Warning: could not list running sandbox containers via %s ps (%v); skipping the stale-container check.\n", e.Runtime, err)
		return
	}

	var stale []string
	for _, name := range names {
		ref, id, err := e.containerImage(name)
		if err != nil {
			_, _ = fmt.Fprintf(e.Stderr, "Warning: could not resolve %s's image via %s inspect (%v); skipping it in the stale-container check.\n", name, e.Runtime, err)
			continue
		}
		if !sameImageRef(ref, image) {
			continue
		}
		if id != freshID {
			stale = append(stale, name)
		}
	}
	if len(stale) == 0 {
		return
	}
	sort.Strings(stale)
	_, _ = fmt.Fprintf(e.Stdout, "Note: %s was rebuilt, but these running sandboxes still use the previous image and keep it until relaunched: %s. Run `cenci sandbox stop <name>`, then relaunch.\n", image, strings.Join(stale, ", "))
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
	e.printStaleContainerNotice(MonolithImage)
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
	e.printStaleContainerNotice(image)
	e.warnFragmentDrift(Scope{UsingRepoImage: true, RepoRoot: repoRoot, Image: image})
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
		e.warnFragmentDrift(scope)
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

// agentRefreshContainerName names the detached background-refresh container
// for agent (docs/cli-conventions.md runtime-object table). The fixed name
// makes a concurrent duplicate start fail closed on the runtime's name
// conflict instead of stacking redundant updaters, and lets an operator who
// spots the container in `docker ps` identify what it is.
func agentRefreshContainerName(agent string) string {
	return "cenci-agent-cli-refresh-" + agent
}

// agentRefreshRunArgs is agentUpdateRunArgs' detached variant for the
// launch-time TTL refresh (#745): --detach hands the updater to the runtime
// daemon so the launch never waits on the (potentially very large — ~130MB
// on the wire for codex) npm download, and the fixed --name provides
// duplicate-start dedup (see agentRefreshContainerName). Same hardening,
// image, and script invocation as agentUpdateRunArgs; never a version
// argument — the background path only ever tracks latest, since an explicit
// version pin always goes through the foreground UpdateAgent.
func (e *Engine) agentRefreshRunArgs(agent string) []string {
	return []string{"run", "--rm", "--detach", "--name", agentRefreshContainerName(agent),
		"--user", "root", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--entrypoint", "/bin/bash",
		"-v", AgentCLIVolumeName(agent) + ":/opt/cenci-agent",
		MonolithImage, "/usr/local/bin/lib/agent-cli.sh", "update", agent}
}

// startAgentRefreshDetached starts the isolated updater detached and returns
// as soon as the runtime has accepted it, capturing (not echoing) the
// --detach stdout so the started container's id never leaks into the launch
// output (the agentVolumePopulated exec pattern, not e.command's wired
// streams). Callers must have ensured MonolithImage already — on the only
// call path (refreshStaleAgentVolumeIfNeeded, reached via EnsureAgentVolume)
// agentVolumePopulated has just done so. A start failure carries the
// runtime's stderr so a name conflict with an already-running refresh stays
// diagnosable; the update's own outcome is deliberately unobserved — the
// volume stays populated either way, and agent-cli.sh's .last-attempt stamp
// keeps the 1h backoff throttling retries.
func (e *Engine) startAgentRefreshDetached(agent string) error {
	if _, err := exec.Command(e.Runtime, e.agentRefreshRunArgs(agent)...).Output(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("%s detached agent refresh failed to start: %w: %s", e.Runtime, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("%s detached agent refresh failed to start: %w", e.Runtime, err)
	}
	return nil
}

// agentVolumeCheckRunArgs builds a cheap, short-lived probe that verifies an
// already-existing shared agent volume actually contains an installed CLI
// and reports its populated/version/pin/last_success/last_attempt facts via
// `agent-cli.sh status <agent>` (landed in #708): network-isolated (npm
// access is never needed for a read-only check), non-root, read-only volume
// mount, and hardened the same way as the updater. It always runs against
// MonolithImage for the same reason agentUpdateRunArgs does.
func (e *Engine) agentVolumeCheckRunArgs(agent string) []string {
	return []string{"run", "--rm", "--network", "none", "--user", "dev",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--entrypoint", "/bin/bash",
		"-v", AgentCLIVolumeName(agent) + ":/opt/cenci-agent:ro",
		MonolithImage, "/usr/local/bin/lib/agent-cli.sh", "status", agent}
}

// maintenanceRunArgs is retained for plugin maintenance, which legitimately
// needs the credential-bearing home volume. Agent CLI updates never use it.
// plugins sets CENCI_SANDBOX_PLUGINS (space-joined) so the one-shot
// container's pluginRefreshCommand expansion resolves the same repo-scoped
// list a real launch would (#1002).
func (e *Engine) maintenanceRunArgs(agent string, scope Scope, command string, plugins []string) []string {
	return []string{"run", "--rm", "--user", "root",
		"-e", fmt.Sprintf("HOST_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("HOST_GID=%d", os.Getgid()),
		"-e", "CENCI_SANDBOX_AGENT=" + agent,
		"-e", "CENCI_SANDBOX_PLUGINS=" + sandboxPluginsEnvValue(plugins),
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

// agentVolumeStatus is agent-cli.sh status <agent>'s parsed facts: whether
// the volume is populated, its installed version, an optional pinned version
// (non-empty means auto-refresh is disabled until --unpin), and the last
// successful/attempted refresh times (the zero time.Time when the
// corresponding fact was absent or unparseable — see parseAgentVolumeStatus).
type agentVolumeStatus struct {
	Populated   bool
	Version     string
	Pin         string
	LastSuccess time.Time
	LastAttempt time.Time
}

// parseAgentVolumeStatus parses agent-cli.sh status's five contractually
// fixed key=value lines (populated, version, pin, last_success,
// last_attempt — sandbox/lib/agent-cli.sh:444-448). It is a default-deny
// parser (watch/docs/error-handling.md #628, the parseReusePosture lesson):
// empty stdout, a missing/truncated line, or an unrecognized "populated"
// value all return ok=false, treated as unpopulated (bootstrap), never a
// permissive zero-value guess. An empty or unparseable
// last_success/last_attempt on an otherwise well-formed status degrades only
// that one field to the zero time.Time (per the plan's resolved Q1:
// unknown -> stale) rather than failing the whole parse.
func parseAgentVolumeStatus(stdout []byte) (agentVolumeStatus, bool) {
	facts := make(map[string]string)
	for _, line := range splitLines(string(stdout)) {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return agentVolumeStatus{}, false
		}
		facts[key] = value
	}
	for _, key := range []string{"populated", "version", "pin", "last_success", "last_attempt"} {
		if _, ok := facts[key]; !ok {
			return agentVolumeStatus{}, false
		}
	}
	status := agentVolumeStatus{
		Version: facts["version"],
		Pin:     facts["pin"],
	}
	switch facts["populated"] {
	case "yes":
		status.Populated = true
	case "no":
		status.Populated = false
	default:
		return agentVolumeStatus{}, false
	}
	status.LastSuccess = parseAgentVolumeStatusEpoch(facts["last_success"])
	status.LastAttempt = parseAgentVolumeStatusEpoch(facts["last_attempt"])
	return status, true
}

// parseAgentVolumeStatusEpoch parses an agent-cli.sh status epoch-seconds
// fact, degrading an empty or unparseable value to the zero time.Time.
func parseAgentVolumeStatusEpoch(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}

// defaultAgentCLITTL is the default staleness window for the launch-time
// agent-CLI refresh, overridable via CENCI_SANDBOX_AGENT_CLI_TTL_HOURS.
const defaultAgentCLITTL = 24 * time.Hour

// agentCLIRefreshBackoff throttles the launch-time refresh to at most once
// per hour after a refresh attempt (successful or not), so an offline/
// captive-portal host doesn't eat the npm-timeout cost on every launch.
const agentCLIRefreshBackoff = time.Hour

// maxAgentCLITTLHours bounds CENCI_SANDBOX_AGENT_CLI_TTL_HOURS (10 years) so
// the int-hours-to-time.Duration conversion below can never overflow: a
// parsed value beyond this ceiling is rejected the same way an unparseable
// or negative value is, rather than silently wrapping time.Duration's
// int64-nanoseconds range into 0 (indistinguishable from the documented
// "0=disabled") or a nonsense negative duration (security review finding B).
const maxAgentCLITTLHours = 24 * 365 * 10

// agentCLITTL reads CENCI_SANDBOX_AGENT_CLI_TTL_HOURS (integer hours; unset
// or empty means the 24h default; 0 disables auto-refresh entirely). Unlike
// reap.go:79's silent ParseFloat fallback, an unparseable, negative, or
// implausibly large (overflow-risking) value must never silently disable
// auto-refresh: it warns to stderr (naming the variable and the offending
// value, per watch/docs/error-handling.md #446) and falls back to the 24h
// default.
func agentCLITTL(stderr io.Writer) time.Duration {
	raw := os.Getenv("CENCI_SANDBOX_AGENT_CLI_TTL_HOURS")
	if raw == "" {
		return defaultAgentCLITTL
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours < 0 || hours > maxAgentCLITTLHours {
		_, _ = fmt.Fprintf(stderr, "Warning: CENCI_SANDBOX_AGENT_CLI_TTL_HOURS=%q is invalid (must be a non-negative integer number of hours, at most %d); falling back to the %dh default.\n", raw, maxAgentCLITTLHours, int(defaultAgentCLITTL.Hours()))
		return defaultAgentCLITTL
	}
	return time.Duration(hours) * time.Hour
}

// agentVolumePopulated cheaply verifies that an already-existing shared agent
// volume actually contains an installed CLI, and returns its parsed
// populated/version/pin/last_success/last_attempt facts (agentVolumeStatus)
// for EnsureAgentVolume's staleness policy. Two real failure modes make
// volumeExists alone untrustworthy: (a) a concurrent first launch's `docker
// run -v` auto-creates the named volume long before that launch's own
// install finishes, so a later launch's volumeExists check can observe a
// still-empty volume as "existing"; (b) a previously failed bootstrap whose
// cleanup `volume rm` itself failed (see EnsureAgentVolume) leaves a broken
// volume that would otherwise be trusted forever. A non-zero exit from the
// probe itself (agent binary missing/not executable) means "not populated";
// any other failure (exec transport, runtime error) is reported as an error
// rather than silently treated as unpopulated; exit 0 with unparseable
// stdout is also treated as unpopulated (default-deny).
func (e *Engine) agentVolumePopulated(agent string) (agentVolumeStatus, bool, error) {
	if err := e.EnsureMonolithImage(); err != nil {
		return agentVolumeStatus{}, false, err
	}
	out, err := exec.Command(e.Runtime, e.agentVolumeCheckRunArgs(agent)...).Output()
	if err == nil {
		status, ok := parseAgentVolumeStatus(out)
		if !ok {
			return agentVolumeStatus{}, false, nil
		}
		return status, status.Populated, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return agentVolumeStatus{}, false, nil
	}
	return agentVolumeStatus{}, false, fmt.Errorf("%s agent volume check failed: %w", e.Runtime, err)
}

// refreshStaleAgentVolumeIfNeeded implements the launch-time TTL refresh
// policy for an already-populated shared agent volume (the design's
// staleness branch): skip entirely when the scoped container is already
// running (attach must stay instant — a running container keeps its
// started-with version regardless) or when the TTL is disabled
// (CENCI_SANDBOX_AGENT_CLI_TTL_HOURS=0); otherwise a missing/unparseable
// last_success is treated as stale (unknown -> stale, per the plan's
// resolved Q1), refresh-eligible subject to the same 1h last_attempt
// backoff and pin skip as a normal TTL-stale volume. A last_success in the
// future (a corrupted stamp file, or a host clock that jumped backward via
// NTP correction) is also treated as stale rather than "impossibly fresh" —
// otherwise now.Sub(LastSuccess) goes negative and is never greater than the
// TTL, permanently suppressing the security-relevant auto-refresh (security
// review finding A). Symmetrically, a future last_attempt is anomalous data
// and must never throttle a refresh: only a non-negative duration under the
// 1h backoff counts as "recently attempted". A non-empty pin skips the
// refresh with a one-line notice naming the pinned version and the --unpin
// remedy (writing no stamps, so the notice repeats on later stale launches
// by design); otherwise it prints the refresh notice and starts the updater
// detached (#745, startAgentRefreshDetached) — the launch proceeds
// immediately on the existing (already-populated, just-stale) version and
// the next launch picks up the refreshed CLI, so a full-download refresh
// (~15min for codex on a slow link) never stalls `cenci open`. The refresh
// stays best-effort: a start failure (e.g. a name conflict with an
// already-running refresh) only warns to stderr.
func (e *Engine) refreshStaleAgentVolumeIfNeeded(agent string, status agentVolumeStatus, containerRunning bool) {
	ttl := agentCLITTL(e.Stderr)
	if containerRunning || ttl == 0 {
		return
	}
	now := time.Now()
	stale := status.LastSuccess.IsZero() || status.LastSuccess.After(now) || now.Sub(status.LastSuccess) > ttl
	if !stale {
		return
	}
	if !status.LastAttempt.IsZero() {
		sinceAttempt := now.Sub(status.LastAttempt)
		if sinceAttempt >= 0 && sinceAttempt < agentCLIRefreshBackoff {
			return
		}
	}
	if status.Pin != "" {
		_, _ = fmt.Fprintf(e.Stdout, "Note: shared %s CLI is pinned to %s and more than %dh stale; run 'cenci sandbox update-agent %s --unpin' to resume automatic updates.\n", agent, status.Pin, int(ttl.Hours()), agent)
		return
	}
	_, _ = fmt.Fprintf(e.Stdout, "Shared %s CLI is more than %dh stale; refreshing it in the background — this launch keeps the current version, the next one picks up the update.\n", agent, int(ttl.Hours()))
	if err := e.startAgentRefreshDetached(agent); err != nil {
		_, _ = fmt.Fprintf(e.Stderr, "Warning: could not start the background refresh of shared %s CLI (%v); continuing with the existing version.\n", agent, err)
	}
}

// EnsureAgentVolume bootstraps a genuinely absent shared volume and, for an
// already-existing one, cheaply verifies it is actually populated before
// trusting it (see agentVolumePopulated). Only a missing or verified-empty
// volume triggers UpdateAgent as a bootstrap; the updater script's own flock
// on the volume's lock file makes a concurrent/redundant update safe. For an
// already-populated volume, refreshStaleAgentVolumeIfNeeded applies the
// launch-time TTL staleness policy. containerRunning is threaded in from
// Launch's hoisted containerRunning probe (see launch.go) so the staleness
// branch can skip entirely on the attach path.
func (e *Engine) EnsureAgentVolume(agent string, containerRunning bool) error {
	name := AgentCLIVolumeName(agent)
	exists, err := e.volumeExists(name)
	if err != nil {
		return err
	}
	if exists {
		status, populated, err := e.agentVolumePopulated(agent)
		if err != nil {
			return err
		}
		if populated {
			e.refreshStaleAgentVolumeIfNeeded(agent, status, containerRunning)
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
// running-container and one-shot-volume update paths. The trailing plugin-
// list arguments expand ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch} in-
// container (#1002) — the only design that serves RefreshRunningPlugins'
// repo-less host-wide sweep, since each container resolves its OWN create-
// time list this way rather than a list resolved host-side for one repo. The
// single-dash form is load-bearing: it distinguishes "unset" (a legacy
// container, defaults to the pair) from an explicitly empty value (provision/
// update nothing), which ${VAR:-default} would collapse together. The third
// positional "cenci" in provision_plugins/provision_codex_plugins (the
// marketplace name matteobortolazzo/cenci is registered under) is untouched
// — only the trailing plugin-list tokens changed.
func pluginRefreshCommand(agent string) string {
	switch agent {
	case "codex":
		return `source /usr/local/bin/lib/migrate-settings.sh && provision_codex_plugins /home/dev/.codex cenci matteobortolazzo/cenci ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch} && update_codex_plugins /home/dev/.codex cenci 0 ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch}`
	case "opencode":
		return `source /usr/local/bin/lib/migrate-settings.sh && provision_opencode_plugins /home/dev/.cenci-src matteobortolazzo/cenci && update_opencode_plugins /home/dev/.cenci-src matteobortolazzo/cenci 0`
	}
	return `source /usr/local/bin/lib/migrate-settings.sh && heal_plugin_installs /home/dev/.claude/plugins && provision_plugins /home/dev/.claude/plugins cenci matteobortolazzo/cenci ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch} && update_plugins /home/dev/.claude/plugins cenci 0 ${CENCI_SANDBOX_PLUGINS-cenci cenci-watch}`
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
// container, or in a one-shot container against the home volume. The
// running-container branch execs into a container that already inherited
// its create-time CENCI_SANDBOX_PLUGINS env, so it needs no config read
// (#1002); only the one-shot volume-update branch resolves sandbox.plugins
// for scope, propagating a malformed-config error unchanged (a running
// target must never hard-fail on a config problem it doesn't even read).
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

	plugins, err := ResolveSandboxPlugins(scope)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(e.Stdout, "Updating plugins in volume '%s'...\n", scope.VolumeName)
	args := e.maintenanceRunArgs(agent, scope, pluginRefreshCommand(agent), plugins)
	if err := e.command(args...).Run(); err != nil {
		return fmt.Errorf("%s run (update-plugins): %w", e.Runtime, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, "Done.")
	return nil
}

// RefreshRunningPlugins refreshes plugins in every currently running sandbox
// container on the host, across EVERY installed runtime (not just e.Runtime)
// — the one Engine method that genuinely sweeps every runtime internally
// rather than being invoked once per runtime by a caller-side WithRuntime
// loop, since it has no single scope/container a caller could resolve
// ownership for first (#629). It is not scoped to the caller's repo, matching
// the scope-independence of the daemon-restart auto-behavior it mirrors, and
// infers each container's agent from its name. Best-effort: a single
// container's failure, or a single runtime's listing failure, doesn't stop
// the rest from refreshing — every failure is aggregated into one returned
// error via errors.Join (AC #4: a failed runtime's containers are never
// silently treated as "none running"). Zero running containers across every
// runtime is not an error; a note is printed instead.
func (e *Engine) RefreshRunningPlugins() error {
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		return err
	}

	all, listErr := sandbox.RunningSandboxContainersAll(runtimes, "")
	if len(all) == 0 {
		if listErr != nil {
			return listErr
		}
		_, _ = fmt.Fprintln(e.Stdout, "No running sandbox containers to refresh.")
		return nil
	}

	var errs []error
	if listErr != nil {
		errs = append(errs, listErr)
	}
	for _, rc := range all {
		agent, ok := sandbox.AgentForContainerName(rc.Name)
		if !ok {
			continue
		}
		if err := e.WithRuntime(rc.Runtime).refreshRunningContainerPlugins(agent, rc.Name); err != nil {
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
