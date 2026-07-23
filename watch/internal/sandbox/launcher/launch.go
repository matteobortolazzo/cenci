package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/internal/errcode"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// Options are the launch parameters `cenci open` collects: which agent and
// model to run, the instance name (empty = not given), attach/isolation
// modifiers, and the verbatim passthrough args for the agent CLI.
type Options struct {
	Agent       string
	Model       string
	Name        string
	Shell       bool
	Dind        bool
	NoDind      bool
	HostNetwork bool
	ReseedCreds bool
	AgentArgs   []string
}

// UsageError marks an input-validation failure the caller should surface as
// a usage error (exit 2 per docs/cli-conventions.md) rather than a runtime
// failure.
type UsageError struct {
	msg string
}

func (e *UsageError) Error() string { return e.msg }

// usageErrorf builds a UsageError.
func usageErrorf(format string, args ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

// IsUsage reports whether err is (or wraps) a UsageError.
func IsUsage(err error) bool {
	var ue *UsageError
	return errors.As(err, &ue)
}

// cenciSocketMountDest is the container-side mount point for the host socket
// directory (matches the 'dev' user's uid 1000, so its XDG_RUNTIME_DIR lands
// here regardless of the host uid).
const cenciSocketMountDest = "/run/user/1000/cenci"

// readyPollInterval is the wait_until_ready poll cadence; a package var so
// tests can shrink the 600-poll (60s) budget.
var readyPollInterval = 100 * time.Millisecond

var exactSemverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// readyPollAttempts is the wait_until_ready poll budget (600 × 100ms = 60s).
const readyPollAttempts = 600

// execAttach is the final interactive handoff: replace this process with the
// container runtime CLI so it owns the TTY, signals, and resize handling
// exactly as if the user had run docker/podman directly, and its exit code
// propagates natively. A seam (package var) so black-box tests with a fake
// runtime still exercise the real exec path.
var execAttach = func(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}

// ValidateAgent checks the agent value the way cenci-sand does, as a usage
// error. Exported so callers can validate before building an Engine.
func ValidateAgent(agent string) error {
	switch agent {
	case "claude", "codex", "opencode":
		return nil
	}
	return usageErrorf("unknown agent %q. Valid agents: claude, codex, opencode.", agent)
}

// IsExactSemver rejects npm tags and ranges while allowing exact stable and
// prerelease versions for controlled rollout or rollback.
func IsExactSemver(version string) bool {
	return exactSemverPattern.MatchString(version)
}

// DefaultModel returns the per-agent model default applied when neither a
// shortcut nor an explicit --model chose one. OpenCode has no cenci-side
// default: its permissions are config-driven with no --model equivalent to
// force, so a bare `opencode` launch never forwards --model unless the
// caller explicitly passed one.
func DefaultModel(agent string) string {
	switch agent {
	case "codex":
		return "gpt-5.6-terra"
	case "opencode":
		return ""
	}
	return "sonnet"
}

// launchCtx is the shared preamble both Launch and DryRun resolve before
// they can branch/build: agent/model defaults, cwd/home, the computed scope,
// and the dind decision. Extracted (ticket #620) so scope/dind/runtime
// resolution/preflight can never drift between a real launch and its dry-run
// preview.
type launchCtx struct {
	Agent  string
	Model  string
	Home   string
	Scope  Scope
	DindOn bool
}

// resolveLaunchContext resolves the shared preamble: agent/model
// defaults+validation, cwd/home, ComputeScope, ResolveDind (a UsageError on
// conflicting/misplaced --dind flags), container-runtime resolution
// (mutates e.Runtime only when unset — docker-first under dind, else
// podman-first), and dindPreflight when dind is on. Both Launch and DryRun
// call this so their failure modes for these steps can never diverge.
func (e *Engine) resolveLaunchContext(opts Options) (launchCtx, error) {
	agent := opts.Agent
	if agent == "" {
		agent = "claude"
	}
	if err := ValidateAgent(agent); err != nil {
		return launchCtx{}, err
	}
	model := opts.Model
	if model == "" {
		model = DefaultModel(agent)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return launchCtx{}, fmt.Errorf("cannot determine working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return launchCtx{}, fmt.Errorf("cannot determine home directory: %w", err)
	}
	scope := ComputeScope(agent, opts.Name, cwd, home)

	dindOn, err := ResolveDind(opts, scope)
	if err != nil {
		return launchCtx{}, err
	}
	if e.Runtime == "" {
		if dindOn {
			e.Runtime, err = sandbox.ContainerRuntimePreferDocker()
		} else {
			e.Runtime, err = sandbox.ContainerRuntime()
		}
		if err != nil {
			return launchCtx{}, err
		}
	}
	if dindOn {
		if err := e.dindPreflight(); err != nil {
			return launchCtx{}, err
		}
	}

	return launchCtx{Agent: agent, Model: model, Home: home, Scope: scope, DindOn: dindOn}, nil
}

// planArgvs performs the read-only container-disposition probing
// (containerRunning + containerHasSharedAgentMount) and returns the create
// and attach argvs plus whether a real launch would attach to an existing
// container instead of creating one. Both Launch and DryRun call this so the
// running/compatible/incompatible branch decision and both argvs can never
// drift between a real launch and its preview:
//
//   - running + compatible (has the shared read-only agent-CLI mount):
//     attaching=true, createArgv=nil — a real launch only attaches, never
//     creates.
//   - running + incompatible (predates the shared read-only agent-CLI
//     mount): the same hard error Launch has always returned.
//   - not running: createArgv is built via buildRunArgv (credential
//     validation included — its hard error propagates unchanged),
//     attaching=false.
//
// attachArgv is always built (buildAgentExecArgv is pure and cannot fail)
// since both branches need it: Launch's runAgent rebuilds it from the same
// inputs, and DryRun always previews it.
//
// A containerRunning/containerHasSharedAgentMount probe failure (e.g. the
// container runtime daemon is unreachable) is returned unchanged, exactly as
// Launch has always propagated it — DryRun must fail the same way a real
// launch would rather than silently assuming "not running".
func (e *Engine) planArgvs(ctx launchCtx, opts Options, cenciBin, socketDir string, cenciAvailable bool) (createArgv, attachArgv []string, attaching bool, err error) {
	agentCmdArgs := buildAgentCmdArgs(ctx.Agent, ctx.Model)
	execEnvArgs := assembleExecEnv(ctx.Agent)
	attachArgv = buildAgentExecArgv(ctx.Scope.ContainerName, ctx.Agent, agentCmdArgs, execEnvArgs, opts)

	running, err := e.containerRunning(ctx.Scope.ContainerName)
	if err != nil {
		return nil, nil, false, err
	}
	if running {
		compatible, err := e.containerHasSharedAgentMount(ctx.Scope.ContainerName, ctx.Agent)
		if err != nil {
			return nil, nil, false, err
		}
		if !compatible {
			return nil, nil, false, fmt.Errorf("running container '%s' predates shared read-only agent CLIs; run 'cenci sandbox stop %s', then relaunch", ctx.Scope.ContainerName, ctx.Scope.ContainerName)
		}
		return nil, attachArgv, true, nil
	}

	createArgv, err = e.buildRunArgv(ctx.Agent, cenciBin, socketDir, cenciAvailable, ctx.Scope, opts, ctx.Home, ctx.DindOn)
	if err != nil {
		return nil, nil, false, err
	}
	return createArgv, attachArgv, false, nil
}

// Launch runs the interactive path: scope the launch, make sure the image
// exists, wire cenci, then attach to a running container or create a
// detached one and attach into it. On success it does not return — the final
// attach execs the container runtime in place of this process.
func (e *Engine) Launch(opts Options) error {
	ctx, err := e.resolveLaunchContext(opts)
	if err != nil {
		return err
	}

	if err := e.EnsureImage(ctx.Scope); err != nil {
		return err
	}
	if err := e.EnsureAgentVolume(ctx.Agent); err != nil {
		return err
	}

	// The selected agent is already present in its global shared volume; no
	// executable is sourced from the host or credential-bearing home volume.
	// Credentials are still staged from the host.
	cenciBin, socketDir, cenciAvailable := e.resolveCenciWiring()

	agentCmdArgs := buildAgentCmdArgs(ctx.Agent, ctx.Model)

	// Provider API keys are forwarded per-exec only (never baked into the
	// container-lifetime create-time env/PID-1 environ), and scoped to the
	// agent that can use them: OpenCode reads ANTHROPIC_API_KEY/
	// OPENAI_API_KEY natively, Codex only OPENAI_API_KEY, and Claude neither
	// (#490).
	execEnvArgs := assembleExecEnv(ctx.Agent)

	// The disposition decision (attach vs create), the incompatible hard
	// error, and both argvs come from the shared planArgvs helper so they can
	// never drift from DryRun's preview of the same decision.
	createArgv, _, attaching, err := e.planArgvs(ctx, opts, cenciBin, socketDir, cenciAvailable)
	if err != nil {
		return err
	}

	if attaching {
		// Attach to an already-running container: its mounts are fixed for
		// its whole lifetime, so only warn about missing cenci wiring (#195)
		// and wait for the async entrypoint when the lifecycle label says we
		// created it detached.
		if err := e.warnIfUnwired(ctx.Scope.ContainerName, cenciAvailable); err != nil {
			return err
		}
		label, err := e.lifecycleLabel(ctx.Scope.ContainerName)
		if err != nil {
			return err
		}
		if label == "detached" {
			if err := e.waitUntilReady(ctx.Scope); err != nil {
				return err
			}
		}
		return e.runAgent(ctx.Scope.ContainerName, ctx.Agent, agentCmdArgs, execEnvArgs, opts)
	}

	// Remove a stopped container of the same name if one exists. Deliberately
	// sequenced AFTER planArgvs (which includes buildRunArgv's credential
	// validation) succeeds, not unconditionally before it as pre-#620 code
	// did: planArgvs must stay pure/read-only-safe so DryRun can reuse it,
	// which pushed rm out of it and after its call. A side effect: a launch
	// that now fails credential validation against a stopped, same-named
	// container leaves that container in place instead of deleting it — a
	// deliberate, no-worse-than-before behavior change, not an oversight.
	_ = exec.Command(e.Runtime, "rm", ctx.Scope.ContainerName).Run()

	// Start the shared container detached, then exec this agent into it: PID 1
	// stays independent from every tmux pane, so closing the first pane can't
	// SIGHUP the container and tear down later exec sessions.
	create := exec.Command(e.Runtime, createArgv...)
	create.Stdout = nil // cenci-sand discards the container id (>/dev/null)
	create.Stderr = e.Stderr
	if err := create.Run(); err != nil {
		return fmt.Errorf("%s run: %w", e.Runtime, err)
	}

	// The entrypoint performs credential and plugin setup before running the
	// readiness command. Do not race the first agent against that
	// initialization.
	if err := e.waitUntilReady(ctx.Scope); err != nil {
		return err
	}
	return e.runAgent(ctx.Scope.ContainerName, ctx.Agent, agentCmdArgs, execEnvArgs, opts)
}

// buildAgentCmdArgs builds the per-agent CLI flags Launch/DryRun append after
// the resolved agent binary. The container is the security boundary: the
// agent runs with full permissions inside it (rejected if root — we run as
// uid 1000 'dev'). Codex also bypasses hook trust: trust lives in the user
// config layer and provisioning never seeds it, so without the flag the
// cenci-watch hooks are silently skipped as "pending review" and sandbox
// sessions never report to the daemon. There is no supported way to persist
// trust non-interactively (openai/codex#21615), and the config trust-store
// key format is flagged in-source as temporary — the per-invocation flag is
// the only stable route (#426). Pure aside from its inputs — no printing, no
// side effects — so both Launch and DryRun can call it and stay
// byte-identical to each other.
func buildAgentCmdArgs(agent, model string) []string {
	switch agent {
	case "codex":
		return []string{"--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "--model", model}
	case "opencode":
		// No --dangerously-skip-permissions equivalent exists for OpenCode:
		// permissions are config-driven via the seeded opencode.json
		// permission block. --model is only forwarded when the caller
		// explicitly passed one (DefaultModel("opencode") is "").
		if model != "" {
			return []string{"--model", model}
		}
		return nil
	default:
		return []string{"--dangerously-skip-permissions", "--model", model}
	}
}

// assembleExecEnv builds the per-exec (not create-time) "-e"/"-u" argument
// list every agent exec session receives: the attach user, pane identity,
// the CENCI_SANDBOX marker/agent name, and provider API key passthroughs
// forwarded per-exec only (never baked into the container-lifetime
// create-time env/PID-1 environ), scoped to the agent that can use them:
// OpenCode reads ANTHROPIC_API_KEY/OPENAI_API_KEY natively, Codex only
// OPENAI_API_KEY, and Claude neither (#490). Pure aside from reading host
// env vars — it depends only on agent, so both Launch and Audit's posture
// classification (audit.go) can call it and stay byte-identical to each
// other rather than re-deriving this list independently.
func assembleExecEnv(agent string) []string {
	execEnvArgs := []string{"-u", "dev",
		"-e", "TMUX_PANE=" + os.Getenv("TMUX_PANE"),
		"-e", "CENCI_SANDBOX=1",
		"-e", "CENCI_SANDBOX_AGENT=" + agent}
	if v := os.Getenv("CONTEXT7_API_KEY"); v != "" {
		execEnvArgs = append(execEnvArgs, "-e", "CONTEXT7_API_KEY="+v)
	}
	if v := os.Getenv("PEN_CLI_KEY"); v != "" {
		execEnvArgs = append(execEnvArgs, "-e", "PEN_CLI_KEY="+v)
	}
	if agent == "opencode" {
		if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
			execEnvArgs = append(execEnvArgs, "-e", "ANTHROPIC_API_KEY="+v)
		}
	}
	if agent == "codex" || agent == "opencode" {
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			execEnvArgs = append(execEnvArgs, "-e", "OPENAI_API_KEY="+v)
		}
	}
	return execEnvArgs
}

// resolveCenciWiring resolves the cenci binary to bind-mount and the host
// socket directory, starting the daemon on demand. The bash launcher shelled
// out to `cenci socket-dir` and `nohup cenci daemon`; natively this IS the
// cenci binary, so the socket dir comes from ipc.DefaultSocketDir() in
// process and a missing daemon is started via daemon.EnsureRunning().
// Missing wiring is never fatal — the session just won't report to the host
// status bars (a warning says so).
func (e *Engine) resolveCenciWiring() (cenciBin, socketDir string, available bool) {
	cenciBin, err := resolveHostBinary("cenci")
	if err != nil {
		// Not on PATH (dev build): mount the running executable itself.
		self, selfErr := os.Executable()
		if selfErr != nil {
			return "", "", false
		}
		if resolved, evalErr := filepath.EvalSymlinks(self); evalErr == nil {
			self = resolved
		}
		cenciBin = self
	}

	socketDir, dirErr := ipc.DefaultSocketDir()
	if dirErr != nil {
		_, _ = fmt.Fprintf(e.Stderr, "Warning: cenci socket directory unavailable (%v); skipping cenci wiring for this launch.\n", dirErr)
		return cenciBin, "", false
	}

	eventsSocket := ipc.DefaultEventSocketPath()

	// The host daemon starts lazily, so right after boot the events socket may
	// not exist even though cenci is installed. Missing it here would create
	// the shared container without any cenci wiring — permanently, since the
	// container is long-lived and later launches only exec into it (#195).
	// Start the daemon ourselves (a redundant start is a no-op) and wait
	// briefly for the socket to appear.
	if !isSocket(eventsSocket) {
		daemon.EnsureRunning()
		for i := 0; i < 30 && !isSocket(eventsSocket); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if isSocket(eventsSocket) {
		return cenciBin, socketDir, true
	}
	_, _ = fmt.Fprintln(e.Stderr, "Warning: cenci is installed but its events socket is unavailable; sessions in this container will not report to the host status bars.")
	return cenciBin, socketDir, false
}

// isSocket reports whether path exists and is a unix socket.
func isSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// assembleRunArgs builds the `docker/podman run` argument list, preserving
// the entrypoint contract: --user root at create time (entrypoint.sh remaps
// 'dev' to HOST_UID/HOST_GID before any process runs under it, then
// unconditionally drops privileges — #154), the detached lifecycle label,
// and the HOST_UID/HOST_GID/WORKSPACE_SCOPE env contract. Docker/Podman
// persist --user as the container's Config.User for its whole lifetime, so
// every later exec call site passes an explicit `-u dev`.
//
// The full arg list is assembled by delegating to focused sub-methods below
// (base flags, volume mounts, env vars, codex credential validation, and
// opt-in features), called in sequence and threaded through a single args
// slice. Grouping by theme means some -v/-e flags end up in a different
// relative order than cenci-sand's original single-block layout; that's
// behaviorally identical since docker/podman treat flag order between
// distinct -v/-e flags as independent — only the trailing image + command
// (appended by the caller, buildRunArgv, after this returns) must stay last.
func (e *Engine) assembleRunArgs(agent, cenciBin, socketDir string, cenciAvailable bool, scope Scope, opts Options, home string, dindOn bool) ([]string, error) {
	args := e.baseRunArgs(scope)
	args = append(args, e.assembleVolumeMounts(agent, cenciBin, socketDir, cenciAvailable, scope, home, dindOn)...)
	args = append(args, e.assembleEnv(agent, scope, opts, dindOn)...)

	credArgs, err := e.validateCredentials(agent, home)
	if err != nil {
		return nil, err
	}
	args = append(args, credArgs...)

	args = append(args, e.assembleOptionalFeatures(opts)...)

	// The sysbox-runc OCI runtime replaces the default runc for this
	// container only, in dind mode. Appended last (still before the trailing
	// image + command buildRunArgv appends after this returns) so it never
	// shifts --name off the front of the argv the run-line assertions key
	// off of.
	if dindOn {
		args = append(args, "--runtime=sysbox-runc")
	}

	return args, nil
}

// buildRunArgv wraps assembleRunArgs with the trailing image + detached PID-1
// command, returning the full argv after the runtime binary (leading "run"
// token included) — exactly what Launch execs to create the detached
// container. Shared by Launch and DryRun so the create argv can never drift
// between a real launch and its dry-run preview. The validateCredentials hard
// error (inside assembleRunArgs) propagates through unchanged.
func (e *Engine) buildRunArgv(agent, cenciBin, socketDir string, cenciAvailable bool, scope Scope, opts Options, home string, dindOn bool) ([]string, error) {
	runArgs, err := e.assembleRunArgs(agent, cenciBin, socketDir, cenciAvailable, scope, opts, home, dindOn)
	if err != nil {
		return nil, err
	}
	runArgs = append(runArgs, scope.Image, "-c", "touch /tmp/cenci-ready && exec sleep infinity")
	return append([]string{"run"}, runArgs...), nil
}

// baseRunArgs builds the container identity/lifecycle flags shared by every
// launch: name, hostname, the detached lifecycle label, init/rm flags,
// workdir, and the create-time --user root (see assembleRunArgs's doc
// comment for why --user root is required at create time).
func (e *Engine) baseRunArgs(scope Scope) []string {
	return []string{
		"--name", scope.ContainerName,
		"--hostname", scope.Hostname,
		"--label", "cenci-sand.lifecycle=detached",
		"-d", "--init", "--rm",
		"--workdir", scope.Workdir,
		"--user", "root",
	}
}

// assembleVolumeMounts builds every bind/named-volume mount: the workspace
// and home volumes, git config (read-only, if present), the optional cenci
// binary + host socket dir wiring (paired with its own XDG_RUNTIME_DIR env
// under the same cenciAvailable guard as the mount itself),
// claude credentials staging, GitHub CLI credentials staging, and Pencil CLI
// session staging (headless design reads). Agent CLIs
// live in the persistent home, so no agent binary is mounted here. Codex
// credentials are handled separately by validateCredentials, since a missing
// codex auth source is a hard launch error rather than an optional mount.
func (e *Engine) assembleVolumeMounts(agent, cenciBin, socketDir string, cenciAvailable bool, scope Scope, home string, dindOn bool) []string {
	args := []string{
		"-v", scope.WorkspaceBindHost + ":" + workspaceContainer,
		"-v", scope.VolumeName + ":/home/dev",
		"-v", AgentCLIVolumeName(agent) + ":/opt/cenci-agent:ro",
	}

	// Dind's own isolated Docker storage (nested `docker` inside the sandbox
	// never touches the host runtime).
	if dindOn {
		args = append(args, "-v", scope.DindVolumeName+":/var/lib/docker")
	}

	// Git config (read-only, if exists).
	for _, gitconfig := range []string{
		filepath.Join(home, ".config", "git", "config"),
		filepath.Join(home, ".gitconfig"),
	} {
		if isRegularFile(gitconfig) {
			args = append(args, "-v", gitconfig+":/home/dev/.gitconfig:ro")
			break
		}
	}

	// Cenci (optional). TMUX_PANE is deliberately NOT set here: a
	// creation-time value lands in /proc/1/environ of the long-lived shared
	// container and goes stale once the creating pane closes, which made
	// reap-orphans kill PID 1 and tear down every attached session (#356).
	// Pane identity is injected per exec session instead (execEnvArgs).
	if cenciAvailable {
		args = append(args,
			"-v", cenciBin+":/usr/local/bin/cenci:ro",
			"-v", socketDir+":"+cenciSocketMountDest+":ro",
			"-e", "XDG_RUNTIME_DIR=/run/user/1000",
		)
	}

	// Claude credentials (read-only staging — entrypoint seeds into /home/dev
	// only when the volume has none, since rotating refresh tokens fork per
	// copy; #259).
	claudeCreds := filepath.Join(home, ".claude", ".credentials.json")
	if isRegularFile(claudeCreds) {
		args = append(args, "-v", claudeCreds+":/tmp/host-claude-creds/.credentials.json:ro")
	}

	// GitHub CLI credentials (read-only staging — entrypoint copies to
	// /home/dev).
	ghHosts := filepath.Join(home, ".config", "gh", "hosts.yml")
	if isRegularFile(ghHosts) {
		args = append(args, "-v", ghHosts+":/tmp/host-gh-config/hosts.yml:ro")
	}

	// Pencil CLI session (read-only staging — entrypoint seeds into /home/dev
	// only when the volume has none, mirroring the rotating-credential
	// caution of #259). Enables headless design reads (`pen interactive`)
	// inside the sandbox; a host PEN_CLI_KEY forwarded per-exec takes
	// precedence over the seeded session inside the CLI.
	pencilSession := filepath.Join(home, ".pencil", "session-cli.json")
	if isRegularFile(pencilSession) {
		args = append(args, "-v", pencilSession+":/tmp/host-pencil-creds/session-cli.json:ro")
	}

	return args
}

// assembleEnv builds the non-credential, non-optional-feature -e flags:
// TERM (with the xterm-256color fallback), the CENCI_SANDBOX marker/agent
// name, the HOST_UID/HOST_GID/WORKSPACE_SCOPE entrypoint contract, the
// opt-in reseed-creds flag, and the COLORTERM/CONTEXT7_API_KEY passthroughs.
func (e *Engine) assembleEnv(agent string, scope Scope, opts Options, dindOn bool) []string {
	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}

	args := []string{
		"-e", "TERM=" + term,
		"-e", "CENCI_SANDBOX=1",
		"-e", "CENCI_SANDBOX_AGENT=" + agent,
		"-e", "CENCI_AGENT_CLI=/opt/cenci-agent/current/node_modules/.bin/" + agent,
		"-e", fmt.Sprintf("HOST_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("HOST_GID=%d", os.Getgid()),
		"-e", "WORKSPACE_SCOPE=" + scope.WorkspaceScope,
	}

	// Force a reseed from the host (recovery after the volume's token chain
	// died, e.g. all sessions were revoked).
	if opts.ReseedCreds {
		args = append(args, "-e", "CENCI_SANDBOX_RESEED_CREDS=1")
	}

	if dindOn {
		args = append(args, "-e", "CENCI_SANDBOX_DIND=1")
	}

	if v := os.Getenv("COLORTERM"); v != "" {
		args = append(args, "-e", "COLORTERM="+v)
	}
	if v := os.Getenv("CONTEXT7_API_KEY"); v != "" {
		args = append(args, "-e", "CONTEXT7_API_KEY="+v)
	}

	return args
}

// validateCredentials checks agent-specific auth requirements and returns
// the corresponding mount/env args. Codex and OpenCode both have a hard
// requirement here (ChatGPT/subscription sign-in and/or API key — fail hard
// if neither is present); claude credentials are optional staging handled in
// assembleVolumeMounts. A present OPENAI_API_KEY for codex is forwarded
// per-exec only (execEnvArgs above) and never baked into create-time env, so
// it only counts toward the auth check here.
func (e *Engine) validateCredentials(agent, home string) ([]string, error) {
	switch agent {
	case "codex":
		var args []string
		hasAuth := false
		codexAuth := filepath.Join(home, ".codex", "auth.json")
		if isRegularFile(codexAuth) {
			args = append(args, "-v", codexAuth+":/tmp/host-codex-creds/auth.json:ro")
			hasAuth = true
		}
		if os.Getenv("OPENAI_API_KEY") != "" {
			hasAuth = true
		}
		if !hasAuth {
			return nil, fmt.Errorf("--agent codex requires Codex auth. Run 'codex login' on the host (creates ~/.codex/auth.json) or export OPENAI_API_KEY.") //nolint:staticcheck // user-facing message ported verbatim from cenci-sand
		}
		return args, nil
	case "opencode":
		// Unlike codex, a present provider API key is forwarded per-exec
		// only (execEnvArgs above) and must never be baked into the
		// create-time env, so it only counts toward hasAuth here.
		var args []string
		hasAuth := false
		opencodeAuth := filepath.Join(home, ".local", "share", "opencode", "auth.json")
		if isRegularFile(opencodeAuth) {
			args = append(args, "-v", opencodeAuth+":/tmp/host-opencode-creds/auth.json:ro")
			hasAuth = true
		}
		if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
			hasAuth = true
		}
		if !hasAuth {
			return nil, fmt.Errorf("--agent opencode requires OpenCode auth. Run 'opencode auth login' on the host (creates ~/.local/share/opencode/auth.json) or export ANTHROPIC_API_KEY/OPENAI_API_KEY.") //nolint:staticcheck // mirrors the codex auth error's punctuation
		}
		return args, nil
	default:
		return nil, nil
	}
}

// assembleOptionalFeatures builds the flags for opt-in isolation-weakening
// features: --network host (with a warning, since the container is the
// security boundary).
func (e *Engine) assembleOptionalFeatures(opts Options) []string {
	var args []string

	// Host network mode (fallback for manual OAuth inside container).
	if opts.HostNetwork {
		_, _ = fmt.Fprintln(e.Stderr, "Warning: --host-network weakens the container's isolation boundary (the container is the security boundary); only use it for manual OAuth callback.")
		args = append(args, "--network", "host")
	}

	return args
}

// isRegularFile reports whether path exists and is a regular file.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// lifecycleLabel reads the container's cenci-sand.lifecycle label; containers
// created by this launcher initialize asynchronously and carry "detached",
// older attached containers have no label and are already initialized.
func (e *Engine) lifecycleLabel(name string) (string, error) {
	out, err := exec.Command(e.Runtime, "inspect", "--format",
		`{{ index .Config.Labels "cenci-sand.lifecycle" }}`, name).Output()
	if err != nil {
		return "", fmt.Errorf("%s inspect %s lifecycle label: %w", e.Runtime, name, err)
	}
	return trimTrailingNewline(string(out)), nil
}

func (e *Engine) containerHasSharedAgentMount(name, agent string) (bool, error) {
	out, err := exec.Command(e.Runtime, "inspect", "--format",
		`{{range .Mounts}}{{printf "%s|%s|%t\n" .Name .Destination .RW}}{{end}}`, name).Output()
	if err != nil {
		return false, fmt.Errorf("%s inspect %s mounts: %w", e.Runtime, name, err)
	}
	want := AgentCLIVolumeName(agent) + "|/opt/cenci-agent|false"
	for _, line := range splitLines(string(out)) {
		if line == want {
			return true, nil
		}
	}
	return false, nil
}

// warnIfUnwired detects a running container that was created while the host
// events socket was missing — attaching can never bring the cenci wiring
// back (mounts are fixed for the container's lifetime), so say how to fix it
// instead of letting sessions silently vanish from the host status bars
// (#195).
func (e *Engine) warnIfUnwired(name string, cenciAvailable bool) error {
	if !cenciAvailable {
		return nil
	}
	out, err := exec.Command(e.Runtime, "inspect", "--format",
		`{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}`, name).Output()
	if err != nil {
		return fmt.Errorf("%s inspect %s mounts: %w", e.Runtime, name, err)
	}
	for _, line := range splitLines(string(out)) {
		if line == cenciSocketMountDest {
			return nil
		}
	}
	_, _ = fmt.Fprintf(e.Stderr, "Warning: container '%s' was created without cenci wiring; its sessions will not report to the host status bars. Run '%s stop %s' and relaunch to fix.\n", name, e.Runtime, name)
	return nil
}

func (e *Engine) containerStartupState(name string) (status, exitCode string, err error) {
	out, err := exec.Command(e.Runtime, "inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", name).Output()
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return "", "", fmt.Errorf("unexpected container state %q", strings.TrimSpace(string(out)))
	}
	return fields[0], fields[1], nil
}

// readHomeVolumeFile reads path from scope's home volume via a short-lived
// container (`run --rm --user root --entrypoint /bin/cat ...`), since the
// failed workload container may already be gone (auto-removed by --rm). It
// returns the trimmed content and whether the read succeeded with non-empty
// content.
func (e *Engine) readHomeVolumeFile(scope Scope, path string) (string, bool) {
	cmd := exec.Command(e.Runtime, "run", "--rm", "--user", "root", "--entrypoint", "/bin/cat",
		"-v", scope.VolumeName+":/home/dev", scope.Image, path)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// cat legitimately exiting non-zero (file absent) is the expected,
			// silent per-tier case. Anything else means the runtime binary
			// itself failed to run, which will make all three home-volume
			// reads and the `docker logs` fallback fail identically — worth
			// surfacing so the operator isn't misdirected to the generic
			// fallback message (#473).
			_, _ = fmt.Fprintf(e.Stderr, "Warning: failed to run %s to read %s from the home volume (%v); startup diagnostics may be incomplete.\n", e.Runtime, path, err)
		}
		return "", false
	}
	content := strings.TrimSpace(string(out))
	return content, content != ""
}

// lastLines returns at most the last n lines of s.
func lastLines(s string, n int) string {
	lines := splitLines(s)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// startupFailureDetail returns diagnostic detail for a container that failed
// during startup, checked in precedence order:
//
//  1. /home/dev/.cenci-agent-startup-error — sandbox/entrypoint.sh writes a
//     human-readable message here and exits non-zero when the agent CLI path
//     is missing or not executable, before the readiness marker is ever
//     touched, so this persistent marker is checked first.
//  2. /home/dev/.cenci-boot.log — the entrypoint's teed boot log (last 50
//     lines), covering any other entrypoint failure that occurred after the
//     boot log tee was installed.
//  3. /home/dev/.cenci-startup-failed — the generic EXIT-trap marker the
//     entrypoint writes for any non-zero exit once the tee is installed, for
//     failures where the boot log itself is empty.
//  4. The container's last 50 `docker/podman logs` lines, for failures
//     outside the entrypoint's own diagnostics (e.g. before the tee/trap were
//     installed).
//  5. A fully generic fallback string, if none of the above yielded anything.
//
// All home-volume reads happen via a short-lived container against the home
// volume, since the failed container itself may already be gone.
//
// The returned errcode.Code classifies which case fired:
// errcode.SandboxStartAgentCLIMissing for case 1 (the agent-CLI-missing
// marker); errcode.SandboxStartGenericEntrypoint for every other case
// (2 through 5), since they all represent the same "generic entrypoint
// failure" class the ticket distinguishes from the agent-CLI-missing case.
func (e *Engine) startupFailureDetail(scope Scope) (string, errcode.Code) {
	if content, ok := e.readHomeVolumeFile(scope, "/home/dev/.cenci-agent-startup-error"); ok {
		return content, errcode.SandboxStartAgentCLIMissing
	}
	if content, ok := e.readHomeVolumeFile(scope, "/home/dev/.cenci-boot.log"); ok {
		return lastLines(content, 50), errcode.SandboxStartGenericEntrypoint
	}
	if content, ok := e.readHomeVolumeFile(scope, "/home/dev/.cenci-startup-failed"); ok {
		return content, errcode.SandboxStartGenericEntrypoint
	}
	logs := exec.Command(e.Runtime, "logs", "--tail", "50", scope.ContainerName)
	if out, err := logs.CombinedOutput(); err == nil && strings.TrimSpace(string(out)) != "" {
		return strings.TrimSpace(string(out)), errcode.SandboxStartGenericEntrypoint
	}
	return "entrypoint exited before initialization completed", errcode.SandboxStartGenericEntrypoint
}

// waitUntilReady polls for the entrypoint's /tmp/cenci-ready marker so the
// first agent never races credential/plugin initialization. An entrypoint
// failure is surfaced immediately from its persistent marker or container
// logs instead of degrading into a generic 60-second timeout.
func (e *Engine) waitUntilReady(scope Scope) error {
	for attempt := 0; attempt < readyPollAttempts; attempt++ {
		if exec.Command(e.Runtime, "exec", "-u", "dev", scope.ContainerName, "test", "-e", "/tmp/cenci-ready").Run() == nil {
			return nil
		}
		status, exitCode, err := e.containerStartupState(scope.ContainerName)
		if err != nil && attempt < 3 {
			time.Sleep(readyPollInterval)
			continue
		}
		if err != nil || status == "exited" || status == "dead" {
			displayStatus, displayExit := status, exitCode
			if err != nil {
				displayStatus, displayExit = "unknown", "unknown"
			}
			detail, code := e.startupFailureDetail(scope)
			return fmt.Errorf("container '%s' failed during startup (status %s, exit %s) [%s]: %s",
				scope.ContainerName, displayStatus, displayExit, code, detail)
		}
		time.Sleep(readyPollInterval)
	}
	return fmt.Errorf("container '%s' did not become ready within 60 seconds.", scope.ContainerName) //nolint:staticcheck // user-facing message ported verbatim from cenci-sand
}

// runAgent execs the runtime CLI in place of this process for the final
// interactive attach (`exec -it ...`), so the docker/podman CLI owns the
// TTY/signals/resize and its exit code propagates natively. Only returns on
// failure.
func (e *Engine) runAgent(name, agent string, agentCmdArgs, execEnvArgs []string, opts Options) error {
	runtimePath, err := exec.LookPath(e.Runtime)
	if err != nil {
		return fmt.Errorf("%s not found on PATH: %w", e.Runtime, err)
	}

	if opts.Shell {
		_, _ = fmt.Fprintf(e.Stdout, "Attaching shell to running '%s'...\n", name)
	}
	argv := append([]string{runtimePath}, buildAgentExecArgv(name, agent, agentCmdArgs, execEnvArgs, opts)...)
	return execAttach(runtimePath, argv, os.Environ())
}

// buildAgentExecArgv builds the interactive agent-attach argv (after the
// runtime binary): the per-exec env, the DISABLE_UPDATES=1 native-update
// suppression (claude, non-shell only), the container name, and either
// /bin/bash (--shell) or the resolved agent binary with its per-agent flags
// and the trailing "--" forwarded args. Pure argv assembly — no printing, no
// side effects — so both runAgent and DryRun can call it and stay
// byte-identical to each other.
func buildAgentExecArgv(name, agent string, agentCmdArgs, execEnvArgs []string, opts Options) []string {
	argv := []string{"exec", "-it"}
	argv = append(argv, execEnvArgs...)
	if !opts.Shell && agent == "claude" {
		argv = append(argv, "-e", "DISABLE_UPDATES=1")
	}
	argv = append(argv, name)
	if opts.Shell {
		argv = append(argv, "/bin/bash")
	} else {
		argv = append(argv, "/opt/cenci-agent/current/node_modules/.bin/"+agent)
		argv = append(argv, agentCmdArgs...)
		argv = append(argv, opts.AgentArgs...)
	}
	return argv
}

// trimTrailingNewline drops a single trailing newline (command output).
func trimTrailingNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

// splitLines splits command output into lines without the trailing empty
// element a final newline would produce.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
